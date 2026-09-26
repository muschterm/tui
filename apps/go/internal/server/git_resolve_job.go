package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Agent conflict resolution (ADR 0023, S4; Q10; wire contract in
// protocol/git_write.go). A job is an ordinary agent turn in a job-kind
// thread (Thread.Job) that runs through the usual fixture or ACP machinery
// (approvals, questions, activity, interrupt). The operation's checkout
// reservation exempts it (writer.go, JobThreadID). The server never stages,
// continues, skips or aborts for the job: when its turn ends the record
// moves to agent_review, and GetOperationState.Review compares every job
// path with its saved copy. Accept and reject are the S3 commands.

const (
	gitJobInstructionsMax = 4 << 10
	gitJobOutsideMax      = 2000
	gitReviewDiffMax      = 64 << 10
)

func gitResolveJobKind(kind string) bool { return kind == protocol.GitKindResolveJobStart }

// jobControlKind reports the job commands that are ordinary (non-Git)
// commands: they only change the job thread and the record.
func jobControlKind(kind string) bool {
	switch kind {
	case protocol.GitKindResolveJobFollowup, protocol.GitKindResolveJobCancel, protocol.GitKindResolveJobEnd:
		return true
	}
	return false
}

func validateResolveJobStart(w *protocol.GitWrite) error {
	j := w.ResolveJob
	if j == nil || w.Integrate != nil || w.Operation != nil || w.Conflict != nil || w.Ref != nil || w.Sync != nil || w.Cancel != nil ||
		len(w.Paths) != 0 || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished || w.Confirmed {
		return failure("invalid", "git.resolve_job_start carries its payload in Git.ResolveJob only")
	}
	if j.AgentID == "" || j.Prompt != "" || len(j.OperationID) > gitOperationIDMax {
		return failure("invalid", "a resolution job needs an agent (and no prompt; use instructions)")
	}
	if len(j.Instructions) > gitJobInstructionsMax || !utf8.ValidString(j.Instructions) {
		return failure("invalid", "instructions must be UTF-8 text up to 4 KiB")
	}
	if len(j.Paths) > gitOperationConflictsMax {
		return failure("invalid", "too many paths")
	}
	for _, p := range j.Paths {
		if !validGitPath(p) {
			return failure("invalid", "paths must be checkout paths")
		}
	}
	return nil
}

// jobPrompt is the scoped instruction the agent receives.
func jobPrompt(st protocol.GitOperationState, conflicts []protocol.GitConflict, instructions string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolve the conflicts of the %s in progress in this checkout.\n\n", st.Kind)
	sides := operationSides(st)
	fmt.Fprintf(&b, "Sides: ours = %s; theirs = %s; base = %s.\n", sides.Ours, sides.Theirs, sides.Base)
	if st.Current != nil {
		fmt.Fprintf(&b, "Commit being applied: %s %s.\n", shortOid(st.Current.Oid), st.Current.Subject)
	}
	b.WriteString("\nConflicted files you may edit (and only these):\n")
	for _, c := range conflicts {
		fmt.Fprintf(&b, "- %q (%s)\n", c.Path, c.Kind)
	}
	b.WriteString(`
Edit each listed file so it contains the correct resolution, without conflict markers. You may read any file and run checks (builds, tests, linters).
Do not run git add, git rm, git commit, git checkout, git switch, git restore, git merge, git rebase, git cherry-pick, git revert, git stash or git reset, and do not continue, skip or abort the operation. Do not edit other files.
When you are done, stop and summarize what you changed and which checks you ran. The user reviews your edits before anything is staged.
`)
	if strings.TrimSpace(instructions) != "" {
		b.WriteString("\nThe user adds:\n" + instructions + "\n")
	}
	return b.String()
}

// outsideLines are the status entries outside paths, as review baseline.
func outsideLines(ctx context.Context, g *gitReader, paths []string) (lines []string, fingerprint string, incomplete bool, err error) {
	st, err := fullStatus(ctx, g)
	if err != nil {
		return nil, "", false, err
	}
	in := map[string]bool{}
	for _, p := range paths {
		in[p] = true
	}
	for _, e := range st.Entries {
		if in[e.Path] || in[e.OrigPath] {
			continue
		}
		lines = append(lines, e.Group+"\x00"+e.Path+"\x00"+e.Pin)
	}
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l + "\x00\x00"))
	}
	incomplete = st.Truncated || len(lines) > gitJobOutsideMax
	if len(lines) > gitJobOutsideMax {
		lines = nil
	}
	return lines, hex.EncodeToString(h.Sum(nil)), incomplete, nil
}

func prepareJobStart(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.ResolveJob
	st, err := w.observeLight(ctx, g)
	if err != nil {
		return nil, err
	}
	switch {
	case !managedOperation(st.Kind):
		return nil, failure("no_operation", "no merge, rebase, cherry-pick or revert is in progress")
	case st.StopReason != "":
		return nil, failure("not_supported", "the rebase stopped for an interactive step; resolve it in a terminal")
	case st.ConflictsTruncated:
		return nil, failure("not_supported", "too many conflicted paths for a resolution job; use a terminal")
	}
	byPath := map[string]protocol.GitConflict{}
	for _, cf := range st.Conflicts {
		byPath[cf.Path] = cf
	}
	var conflicts []protocol.GitConflict
	if len(req.Paths) == 0 {
		for _, cf := range st.Conflicts {
			if !cf.Submodule {
				conflicts = append(conflicts, cf)
			}
		}
	} else {
		for _, p := range sortedUnique(req.Paths) {
			cf, ok := byPath[p]
			switch {
			case !ok:
				return nil, failure("not_conflicted", p+" is not unmerged")
			case cf.Submodule:
				return nil, failure("not_supported", "submodule conflicts are resolved in a terminal")
			}
			conflicts = append(conflicts, cf)
		}
	}
	if len(conflicts) == 0 {
		return nil, failure("not_conflicted", "there are no conflicted files for an agent to resolve")
	}
	paths := make([]string, 0, len(conflicts))
	for _, cf := range conflicts {
		paths = append(paths, cf.Path)
	}
	job := protocol.ThreadJob{Kind: protocol.ThreadJobConflictResolution, OperationID: req.OperationID, Checkout: w.top, Paths: paths}
	var chosen *protocol.Agent
	var settings protocol.Settings
	var project *protocol.Project
	var adopted *protocol.GitOperationRecord
	var baseline *protocol.GitJobBaseline
	p := &gitPlan{paths: paths}
	p.journal = func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		if res == nil {
			syncJobStatesLocked(s, repoOperation)
			rec := gitOperationFor(s, w.top)
			matches := rec != nil && gitOperationActive(rec.State) && recordMatches(*rec, st)
			switch {
			case req.OperationID != "" && (!matches || rec.OperationID != req.OperationID):
				return failure("stale_operation", "the operation in progress is not the one this request names; refresh")
			case matches && rec.JobThreadID != "":
				return failure("job_exists", "a resolution job is already attached to this operation; end it first")
			case !matches:
				adopted = &protocol.GitOperationRecord{OperationID: "op-" + c.ID, Checkout: w.top, Kind: st.Kind, Branch: st.Branch, OrigHead: st.OrigHead,
					StartCommandID: c.ID, ThreadID: c.ThreadID, ProjectID: c.ProjectID, StartedAt: now}
				if st.Target != nil {
					adopted.Target = *st.Target
				}
			}
			a, err := resolveAgent(s, req.AgentID)
			if err != nil {
				return err
			}
			chosen = a
			settings = protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
			if a.Kind == agent.KindACP {
				settings = agent.DefaultSettings(*a)
			}
			if req.Settings != nil {
				if err := validateSettings(s, acpAgentID(*a), *req.Settings); err != nil {
					return err
				}
				settings = *req.Settings
			}
			projectID := c.ProjectID
			if t := threadByID(s, c.ThreadID); t != nil {
				projectID = t.ProjectID
			}
			for i := range s.Projects {
				if s.Projects[i].ID == projectID {
					copied := s.Projects[i]
					project = &copied
				}
			}
			if project == nil {
				return failure("not_found", "the project of this checkout was not found")
			}
			return nil
		}
		if res.State != protocol.GitStateSucceeded {
			return nil
		}
		rec := gitOperationFor(s, w.top)
		if adopted != nil || rec == nil || !gitOperationActive(rec.State) {
			if adopted == nil {
				return nil
			}
			adopted.State = protocol.GitOperationStoppedConflicts
			putGitOperation(s, *adopted)
			rec = gitOperationFor(s, w.top)
		}
		id := "job-" + c.ID
		job.OperationID, job.StartedAt = rec.OperationID, now
		thread := protocol.Thread{ID: id, ProjectID: project.ID, Project: project.Name, Title: "Resolve " + st.Kind + " conflicts", Checkout: w.top,
			Agent: chosen.Name, AgentID: acpAgentID(*chosen), State: "idle", Selected: settings, Effective: settings, QueueRevision: 1, Job: &job,
			Queue: []protocol.Prompt{{ID: "prompt-" + id, Text: jobPrompt(st, conflicts, req.Instructions), Revision: 1, Settings: settings}}}
		// A job in a managed worktree belongs to it: dispatch is gated on
		// the worktree and the job counts for removal (ADR 0024).
		thread.WorktreeID = worktreeAt(s, w.top)
		s.Threads = append(s.Threads, thread)
		rec.JobThreadID, rec.State, rec.UpdatedAt, rec.LastCommandID = id, protocol.GitOperationAgentRunning, now, c.ID
		// The first job of a stop sets the baseline; later jobs keep it, so
		// earlier unexplained changes stay gated.
		// A baseline of another stop (the operation moved on outside the
		// application) is replaced when a job starts at the new stop.
		if baseline != nil && (rec.JobBaseline == nil || rec.JobBaseline.StopKey != baseline.StopKey) {
			baseline.StartedAt = now
			rec.JobBaseline, rec.JobDecisions = baseline, nil
		}
		if res.Operation != nil {
			res.Operation.OperationID = rec.OperationID
		}
		return nil
	}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: st.Kind, HeadBefore: st.HeadOid, Outcome: protocol.GitOutcomeUnchanged}
		defer func() { res.Operation = op }()
		// Open documents are saved first, so the pre-job copy holds what the
		// user wrote there too.
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			return *refused
		}
		again, err := w.observeLight(ctx, g)
		if err != nil || stopKey(again) != stopKey(st) {
			return gitResult(protocol.GitStateFailed, "stale_operation", "the operation changed; nothing was started", nil)
		}
		// Every path's state before the agent touches it is saved (the
		// review's "before" and the reject target), recorded durably.
		for _, path := range paths {
			if _, err := w.ensurePathCopy(ctx, g, p, again, path, op); err != nil {
				return gitResult(protocol.GitStateFailed, "unavailable", "the conflicted files could not be saved before the agent starts: "+err.Error(), nil)
			}
		}
		// The pre-job copy: every job path's working file and index entries
		// exactly as they are now, the review's "before" and what reject
		// restores (edits the user made before the job are kept there).
		items := make([]copyItem, 0, len(paths))
		for _, path := range paths {
			_, _, entries, err := pathEntries(ctx, g, path)
			if err != nil {
				return gitResult(protocol.GitStateFailed, "unavailable", "the index could not be read; nothing was started", nil)
			}
			items = append(items, copyItem{path: path, entries: entries})
		}
		base, _, err := w.writeCopy(ctx, again, protocol.GitCopyBeforeJob, "", items)
		switch {
		case err != nil:
			return gitResult(protocol.GitStateFailed, "unavailable", "the conflicted files could not be saved before the agent starts: "+err.Error(), nil)
		case len(base.Missing) > 0:
			return gitResult(protocol.GitStateFailed, "not_supported", "these files cannot be saved before the agent starts (too large or special): "+listPaths(base.Missing)+"; nothing was started", nil)
		}
		if err := recordCopy(ctx, p, base, op); err != nil {
			return gitResult(protocol.GitStateFailed, "storage", "the pre-job copy could not be recorded; nothing was started", nil)
		}
		job.BaseCopy = base.CopyID
		// The content gate's baseline: the whole index, kept in the
		// application home.
		index, truncated, err := g.read(ctx, gitStatusMaxBytes, "ls-files", "--stage", "-z")
		baseline = &protocol.GitJobBaseline{StopKey: stopKey(again), Incomplete: truncated || err != nil}
		if !baseline.Incomplete {
			if id, err := putJobBaseline(w.baselineDir, index); err == nil {
				baseline.Listing = id
			} else {
				baseline.Incomplete = true
			}
		}
		lines, fp, incomplete, err := outsideLines(ctx, g, paths)
		if err != nil {
			return gitResult(protocol.GitStateFailed, "unavailable", "the working tree could not be read before the agent starts", nil)
		}
		job.BaseHead, job.BaseStop, job.BaseOutside, job.BaseOutsideFingerprint, job.OutsideIncomplete = again.HeadOid, stopKey(again), lines, fp, incomplete
		in := map[string]bool{}
		for _, path := range paths {
			in[path] = true
		}
		for _, cf := range again.Conflicts {
			if !in[cf.Path] {
				job.BaseOtherUnmerged = append(job.BaseOtherUnmerged, cf.Path)
			}
		}
		copied := again
		op.State = &copied
		return gitResult(protocol.GitStateSucceeded, "", fmt.Sprintf("Started a resolution job for %d conflicted files", len(paths)), nil)
	}
	return p, nil
}

// repoOperation reads the operation in progress at a repository toplevel
// with file stats only.
func repoOperation(checkout string) string { return gitDirOperation(discoverGitDir(checkout)) }

// jobSync is what a sync asks the engine to do outside the snapshot.
type jobSync struct {
	changed   bool
	interrupt []string // job threads whose turn must be stopped
	review    []string // toplevels whose review must be computed
}

// syncJobStatesLocked keeps records and their jobs consistent:
//   - an operation that is no longer in progress while a job is attached
//     was ended by the job's agent: the record becomes ended_by_job (the
//     last review stays on it), the job is detached and its running turn
//     stopped;
//   - a job thread that no longer exists (deleted) is detached, and the
//     record returns to stopped_conflicts (reconciled to ready);
//   - an agent_running record whose job turn ended becomes agent_review
//     (agent_interrupted when the turn was cancelled or failed), and its
//     review is due.
func syncJobStatesLocked(s *protocol.Snapshot, opKind func(checkout string) string) jobSync {
	var out jobSync
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range s.GitOperations {
		rec := &s.GitOperations[i]
		if rec.JobThreadID == "" {
			continue
		}
		t := threadByID(s, rec.JobThreadID)
		switch {
		case gitOperationActive(rec.State) && opKind != nil && opKind(rec.Checkout) != "" && opKind(rec.Checkout) != rec.Kind:
			// Another operation is in progress: reconciliation ends the
			// record; nothing to attribute here.
			continue
		case gitOperationActive(rec.State) && opKind != nil && opKind(rec.Checkout) == "":
			if t != nil {
				if activeTurn(t) {
					out.interrupt = append(out.interrupt, t.ID)
				}
				if !t.Closed {
					t.Closed = true
					t.LifecycleRevision++
				}
			}
			if rec.State == protocol.GitOperationAgentRunning {
				endRecord(rec, protocol.GitOperationEndedByJob, "ended_by_job", "the operation ended while the resolution agent was working (the agent continued, skipped or aborted it); review the branch", now)
			} else {
				endRecord(rec, protocol.GitOperationEndedExternal, "", gitOperationEndedMessage, now)
			}
			rec.JobBaseline, rec.JobDecisions = nil, nil
			rec.JobThreadID = ""
			out.changed = true
			continue
		case t == nil:
			rec.JobThreadID = ""
			switch rec.State {
			case protocol.GitOperationAgentRunning, protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
				rec.State = protocol.GitOperationStoppedConflicts
			}
			rec.UpdatedAt, out.changed = now, true
			continue
		}
		if rec.State != protocol.GitOperationAgentRunning {
			continue
		}
		switch {
		case activeTurn(t) || (t.State == "idle" && len(t.Queue) > 0 && !t.NeedsResume && !t.Closed):
			continue
		case t.NeedsResume || t.State == "failed" || t.State == "interrupted" || t.Closed:
			rec.State = protocol.GitOperationAgentInterrupted
		default:
			rec.State = protocol.GitOperationAgentReview
		}
		rec.UpdatedAt, out.changed = now, true
		rec.Review, rec.ReviewKey = nil, ""
		out.review = append(out.review, rec.Checkout)
	}
	return out
}

// syncJobsLocked applies syncJobStatesLocked to the engine snapshot,
// persists a change, stops the turns of jobs whose operation ended and
// computes due reviews in the background.
func (e *engine) syncJobsLocked() {
	out := syncJobStatesLocked(&e.snap, e.jobOpKindLocked)
	e.afterJobSyncLocked(out)
	if out.changed {
		e.snap.Revision++
		e.flushLocked()
	}
}

// jobOpKindLocked is the operation in progress at a checkout for job sync;
// while a Git write runs there (a continue, skip or abort) it reports the
// record's kind, so its intermediate state is never taken for the job's
// doing.
func (e *engine) jobOpKindLocked(checkout string) string {
	for _, h := range e.gitLocked().holders {
		if pathsOverlap(h.top, checkout) {
			if rec := gitOperationFor(&e.snap, checkout); rec != nil {
				return rec.Kind
			}
		}
	}
	dir, _ := e.gitDirForLocked(checkout)
	return gitDirOperation(dir)
}

// jobTurnEndedLocked drops the cached review of the job that threadID runs
// and computes a new one: a turn that really ended (also after a cancel)
// is reviewed as it ended.
func (e *engine) jobTurnEndedLocked(threadID string) {
	for i := range e.snap.GitOperations {
		rec := &e.snap.GitOperations[i]
		if rec.JobThreadID != threadID {
			continue
		}
		if rec.Review != nil {
			rec.Review, rec.ReviewKey = nil, ""
			e.snap.Revision++
			e.flushLocked()
		}
		e.afterJobSyncLocked(jobSync{review: []string{rec.Checkout}})
	}
}

// afterJobSyncLocked launches what a sync asked for.
func (e *engine) afterJobSyncLocked(out jobSync) {
	for _, id := range out.interrupt {
		if r := e.runs[id]; r != nil {
			go r.interrupt()
		}
	}
	for _, top := range out.review {
		go func() {
			ctx, cancel := context.WithTimeout(e.baseContext(), gitRequestBudget)
			defer cancel()
			_, _ = e.operationState(ctx, top, true)
		}()
	}
}

// endJobLocked detaches a job from its record and closes its thread (kept
// for its history).
func endJobLocked(s *protocol.Snapshot, rec *protocol.GitOperationRecord, now string) {
	if t := threadByID(s, rec.JobThreadID); t != nil && !t.Closed {
		t.Closed = true
		t.LifecycleRevision++
	}
	rec.JobThreadID = ""
	switch rec.State {
	case protocol.GitOperationAgentRunning, protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
		rec.State = protocol.GitOperationStoppedConflicts // reconciled to ready on the next read
	}
	rec.UpdatedAt = now
}

// refuseWhileJobRuns refuses operation and conflict commands while the
// job's agent is working.
func refuseWhileJobRuns(s *protocol.Snapshot, top string) error {
	syncJobStatesLocked(s, repoOperation)
	if rec := gitOperationFor(s, top); rec != nil && rec.State == protocol.GitOperationAgentRunning {
		return failure("job_running", "a resolution agent is working on this operation; cancel it first")
	}
	return nil
}

// applyJobControl applies git.resolve_job_followup, _cancel and _end.
func (e *engine) applyJobControl(s *protocol.Snapshot, c protocol.Command) (string, error) {
	req := c.Git
	if req == nil || req.ResolveJob == nil || req.ResolveJob.OperationID == "" || req.Integrate != nil || req.Operation != nil || req.Conflict != nil {
		return "", failure("invalid", c.Kind+" carries its payload in Git.ResolveJob with an operation_id")
	}
	j := req.ResolveJob
	if j.AgentID != "" || j.Settings != nil || len(j.Paths) != 0 || j.Instructions != "" || (c.Kind != protocol.GitKindResolveJobFollowup && j.Prompt != "") {
		return "", failure("invalid", c.Kind+" takes an operation_id (and a prompt for follow-up) only")
	}
	e.afterJobSyncLocked(syncJobStatesLocked(s, e.jobOpKindLocked))
	var rec *protocol.GitOperationRecord
	for i := range s.GitOperations {
		if s.GitOperations[i].OperationID == j.OperationID {
			rec = &s.GitOperations[i]
		}
	}
	if rec == nil || !gitOperationActive(rec.State) || rec.JobThreadID == "" {
		return "", failure("no_job", "no resolution job is attached to that operation")
	}
	t := threadByID(s, rec.JobThreadID)
	if t == nil {
		return "", failure("no_job", "the resolution job's thread no longer exists")
	}
	// The command's target must be the operation's checkout.
	if dir, err := gitWriteTargetLocked(s, c); err != nil {
		return "", err
	} else if !pathsOverlap(realPath(dir), realPath(rec.Checkout)) {
		return "", failure("invalid", "the operation does not belong to this project or thread")
	}
	_, dispatching := e.claiming[t.ID]
	now := time.Now().UTC().Format(time.RFC3339)
	switch c.Kind {
	case protocol.GitKindResolveJobFollowup:
		if rec.State == protocol.GitOperationAgentRunning || dispatching {
			return "", failure("job_running", "the agent is still working or stopping; wait for it or cancel it")
		}
		if strings.TrimSpace(j.Prompt) == "" || len(j.Prompt) > 16384 {
			return "", failure("invalid", "prompt must contain 1–16384 bytes")
		}
		// A follow-up is the user's explicit decision to continue the job,
		// also after an interruption or a restart.
		if t.Closed {
			return "", failure("no_job", "the resolution job was ended")
		}
		t.NeedsResume, t.Error, t.StopReason = false, "", ""
		if t.State != "idle" {
			t.State = "idle"
		}
		t.Queue = append(t.Queue, protocol.Prompt{ID: "prompt-" + c.ID, Text: j.Prompt, Revision: 1, Settings: t.Selected})
		t.QueueRevision++
		// The baseline stays the pre-job copy; decisions start over for
		// the new turn.
		t.Job.Decisions, t.Job.DecisionsTurn = nil, ""
		rec.Review, rec.ReviewKey = nil, ""
		rec.State, rec.UpdatedAt, rec.LastCommandID = protocol.GitOperationAgentRunning, now, c.ID
		e.startQueuedFixture(s, t)
	case protocol.GitKindResolveJobCancel:
		if !activeTurn(t) {
			return "", failure("not_active", "the resolution agent is not working")
		}
		if _, err := apply(s, protocol.Command{Version: protocol.Version, ID: c.ID, Kind: "thread.interrupt", ThreadID: t.ID}); err != nil {
			return "", err
		}
		rec.State, rec.UpdatedAt, rec.LastCommandID = protocol.GitOperationAgentInterrupted, now, c.ID
	case protocol.GitKindResolveJobEnd:
		if activeTurn(t) || dispatching {
			return "", failure("job_running", "the agent is still working or stopping; cancel it and wait")
		}
		id := t.ID
		endJobLocked(s, rec, now)
		rec.LastCommandID = c.ID
		// The job thread is removed with its history; the record keeps the
		// last review.
		if _, err := apply(s, protocol.Command{Version: protocol.Version, ID: c.ID, Kind: "thread.delete", ThreadID: id, Revision: threadByID(s, id).LifecycleRevision}); err != nil {
			return "", err
		}
		return id, nil
	}
	return t.ID, nil
}

// ---- Review ----

const (
	gitReviewBudget     = 10 * time.Second
	gitReviewTotalDiffs = 256 << 10
	gitReviewMaxEdits   = 500
	gitReviewMaxLines   = 50000
)

// jobReviewInput is what a review needs from the engine.
type jobReviewInput struct {
	job        protocol.ThreadJob
	threadID   string
	turnID     string
	stopReason string
	state      string
	copies     []protocol.GitConflictCopy
	cached     *protocol.GitResolveReview
	cachedKey  string
}

// jobInputLocked returns the attached job of top's record, if any.
func (e *engine) jobInputLocked(top string) *jobReviewInput {
	rec := gitOperationFor(&e.snap, top)
	if rec == nil || rec.JobThreadID == "" {
		return nil
	}
	t := threadByID(&e.snap, rec.JobThreadID)
	if t == nil || t.Job == nil {
		return nil
	}
	job := *t.Job
	job.Decisions = maps.Clone(t.Job.Decisions)
	in := &jobReviewInput{job: job, threadID: t.ID, turnID: t.TurnID, stopReason: t.StopReason, copies: e.conflictCopiesLocked(top, t.Job.BaseStop), cachedKey: rec.ReviewKey}
	if rec.Review != nil {
		cached := cloneReview(*rec.Review)
		in.cached = &cached
	}
	_, dispatching := e.claiming[t.ID]
	switch {
	case rec.State == protocol.GitOperationAgentRunning || activeTurn(t) || dispatching:
		// Also the cancel window: the turn has not ended yet.
		in.state = protocol.GitReviewRunning
	case rec.State == protocol.GitOperationAgentInterrupted:
		in.state = protocol.GitReviewInterrupted
	default:
		in.state = protocol.GitReviewReady
	}
	return in
}

func cloneReview(r protocol.GitResolveReview) protocol.GitResolveReview {
	r.Items = slices.Clone(r.Items)
	r.Violations = slices.Clone(r.Violations)
	return r
}

// reviewKey identifies the repository state a review describes: the job
// turn, HEAD, the unmerged entries, and the index entries and working-tree
// tokens of the job's paths.
func reviewKey(ctx context.Context, g *gitReader, st protocol.GitOperationState, in *jobReviewInput) string {
	h := sha256.New()
	fmt.Fprintf(h, "review-key-v2\x00%s\x00%s\x00%s\x00", in.turnID, st.HeadOid, st.UnmergedFingerprint)
	// The whole index and the status outside the job's paths.
	if out, truncated, err := g.read(ctx, gitStatusMaxBytes, "ls-files", "--stage", "-z"); err == nil && !truncated {
		h.Write(out)
	} else {
		h.Write([]byte(ID()))
	}
	if _, fp, _, err := outsideLines(ctx, g, in.job.Paths); err == nil {
		h.Write([]byte(fp))
	} else {
		h.Write([]byte(ID()))
	}
	for _, p := range in.job.Paths {
		h.Write([]byte(p + "\x00" + worktreeStat(g.dir, p) + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reviewFor returns the job's review: the cached one unless refresh (or none
// is cached for the job's latest turn), marked Stale when the repository
// changed since, with the user's current decisions. A fresh review is
// computed within gitReviewBudget and cached on the record. While the agent
// works no review is computed unless asked for.
func (e *engine) reviewFor(ctx context.Context, st protocol.GitOperationState, top string, in *jobReviewInput, refresh bool) *protocol.GitResolveReview {
	g, err := newGitReader(ctx, top)
	if err != nil {
		return &protocol.GitResolveReview{JobThreadID: in.threadID, State: in.state, Items: []protocol.GitResolveItem{}, Incomplete: true, TurnID: in.turnID, StopReason: in.stopReason}
	}
	key := reviewKey(ctx, g, st, in)
	var r protocol.GitResolveReview
	switch {
	case !refresh && in.cached != nil && in.cached.TurnID == in.turnID:
		r = *in.cached
		r.Stale = in.cachedKey != key
	case !refresh && in.state == protocol.GitReviewRunning:
		return &protocol.GitResolveReview{JobThreadID: in.threadID, State: in.state, Items: []protocol.GitResolveItem{}, TurnID: in.turnID}
	default:
		r = *computeReview(ctx, g, st, in)
		if in.state != protocol.GitReviewRunning {
			e.mu.Lock()
			if rec := gitOperationFor(&e.snap, top); rec != nil && rec.JobThreadID == in.threadID {
				next := clone(e.snap)
				nrec := gitOperationFor(&next, top)
				cached := cloneReview(r)
				nrec.Review, nrec.ReviewKey = &cached, key
				next.Revision++
				if e.store.Save(next, nil, nil) == nil {
					e.snap = next
					e.publish()
				}
			}
			e.mu.Unlock()
		}
	}
	r.State = in.state
	for i := range r.Items {
		r.Items[i].Decision = ""
		if in.job.DecisionsTurn == in.turnID {
			r.Items[i].Decision = in.job.Decisions[r.Items[i].Path]
		}
	}
	return &r
}

// computeReview compares every job path with its pre-job copy and checks
// what the agent may have done outside its scope, within gitReviewBudget.
// An item that cannot be compared is Unknown and makes the review
// Incomplete; nothing is claimed about it.
func computeReview(ctx context.Context, g *gitReader, st protocol.GitOperationState, in *jobReviewInput) *protocol.GitResolveReview {
	ctx, cancel := context.WithTimeout(ctx, gitReviewBudget)
	defer cancel()
	r := &protocol.GitResolveReview{JobThreadID: in.threadID, State: in.state, Items: []protocol.GitResolveItem{}, TurnID: in.turnID, StopReason: in.stopReason,
		ComputedAt: time.Now().UTC().Format(time.RFC3339)}
	job := in.job
	if st.HeadOid != job.BaseHead {
		r.Violations = append(r.Violations, "HEAD moved from "+shortOid(job.BaseHead)+" to "+shortOid(st.HeadOid))
	}
	if stopKey(st) != job.BaseStop {
		r.Violations = append(r.Violations, "the operation is no longer at the stop the job started at")
	}
	unmergedNow := map[string]bool{}
	for _, c := range st.Conflicts {
		unmergedNow[c.Path] = true
	}
	var resolvedOthers []string
	for _, p := range job.BaseOtherUnmerged {
		if !unmergedNow[p] {
			resolvedOthers = append(resolvedOthers, p)
		}
	}
	if len(resolvedOthers) > 0 {
		r.Violations = append(r.Violations, "other conflicted paths were resolved: "+listPaths(resolvedOthers))
	}
	lines, fp, incomplete, err := outsideLines(ctx, g, job.Paths)
	switch {
	case err != nil:
		r.Incomplete = true
	case fp != job.BaseOutsideFingerprint:
		changed := changedOutside(job.BaseOutside, lines)
		if job.OutsideIncomplete || incomplete || len(changed) == 0 {
			r.Violations = append(r.Violations, "files outside the conflicted paths changed")
		} else {
			r.Violations = append(r.Violations, "files outside the conflicted paths changed: "+listPaths(changed))
		}
	}
	r.Incomplete = r.Incomplete || job.OutsideIncomplete || incomplete
	base := job.BaseCopy
	if base == "" {
		// Jobs started before pre-job copies existed: the stop's copy.
		for _, c := range in.copies {
			if c.Reason == protocol.GitCopyAtStop {
				base = c.CopyID
				break
			}
		}
	}
	budget := gitReviewTotalDiffs
	var staged []string
	for _, p := range job.Paths {
		item := protocol.GitResolveItem{Path: p, CopyID: base}
		if ctx.Err() != nil {
			item.Unknown = true
		} else {
			item = reviewItem(ctx, g, base, p, &budget)
		}
		if item.Unknown {
			r.Incomplete = true
		}
		if item.Staged {
			staged = append(staged, p)
		}
		r.Items = append(r.Items, item)
	}
	if len(staged) > 0 {
		r.Violations = append(r.Violations, "conflicted paths were staged or removed from the index: "+listPaths(staged))
	}
	r.Fingerprint = reviewFingerprint(*r)
	return r
}

// reviewFingerprint digests what the user reviews: each item's state and
// pins, and the violations.
func reviewFingerprint(r protocol.GitResolveReview) string {
	h := sha256.New()
	h.Write([]byte("review-v1\x00"))
	for _, it := range r.Items {
		fmt.Fprintf(h, "%s\x00%t%t%t%t%t\x00%s\x00%s\x00%s\x00", it.Path, it.Changed, it.Staged, it.Deleted, it.HasMarkers, it.Unknown, it.ConflictPin, it.WorktreeToken, it.IndexOid)
	}
	for _, v := range r.Violations {
		h.Write([]byte(v + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// changedOutside lists paths whose outside status line differs.
func changedOutside(before, after []string) []string {
	key := func(l string) string {
		_, rest, _ := strings.Cut(l, "\x00")
		p, _, _ := strings.Cut(rest, "\x00")
		return p
	}
	b := map[string]string{}
	for _, l := range before {
		b[key(l)] += l + "\n"
	}
	a := map[string]string{}
	for _, l := range after {
		a[key(l)] += l + "\n"
	}
	var paths []string
	for p, v := range a {
		if b[p] != v {
			paths = append(paths, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			paths = append(paths, p)
		}
	}
	return sortedUnique(paths)
}

// hashContent is Git's blob ID for data, computed without writing it.
func hashContent(ctx context.Context, g *gitReader, data []byte) (string, error) {
	out, _, err := g.readInput(ctx, 4096, bytes.NewReader(data), "hash-object", "--no-filters", "--stdin")
	oid := strings.TrimSpace(string(out))
	if err != nil || !gitFullHash.MatchString(oid) {
		return "", fmt.Errorf("hash failed")
	}
	return oid, nil
}

// reviewItem compares one path's working file and index with the pre-job
// copy base. Equality is decided by object IDs; diffs are bounded by the
// shared budget.
func reviewItem(ctx context.Context, g *gitReader, base, p string, budget *int) protocol.GitResolveItem {
	item := protocol.GitResolveItem{Path: p, CopyID: base}
	unknown := func() protocol.GitResolveItem { item.Unknown = true; return item }
	pin, unmerged, entries, err := pathEntries(ctx, g, p)
	if err != nil {
		return unknown()
	}
	item.ConflictPin = pin
	tok := worktreeStat(g.dir, p)
	item.WorktreeToken = tok
	_, beforeOid, beforePresent := copyEntry(ctx, g, base, "w/"+p)
	if base == "" || copyWorktreeState(ctx, g, base, p) == "" {
		return unknown()
	}
	// Staged: the agent resolved the path in the index (it was unmerged
	// before the job, as every job path was).
	item.Staged = !unmerged
	if e0 := entries[0]; e0.Present {
		item.IndexOid = e0.Oid
	}
	var after []byte
	afterOid, afterPresent := "", false
	switch tokenKind(tok) {
	case tokenAbsent:
	case "reg", "lnk":
		_, _, data, err := readWorktreeFile(g.dir, p, tok, gitBackupBytesMax)
		if err != nil {
			return unknown()
		}
		if afterOid, err = hashContent(ctx, g, data); err != nil {
			return unknown()
		}
		after, afterPresent = data, true
	default:
		return unknown()
	}
	item.Deleted = beforePresent && !afterPresent
	item.Changed = beforePresent != afterPresent || beforeOid != afterOid
	var before []byte
	beforeTooLarge := false
	if beforePresent && (item.Changed || item.IndexOid != "") {
		data, truncated, err := g.read(ctx, gitConflictMarkMax, "cat-file", "blob", beforeOid)
		if err != nil {
			return unknown()
		}
		before, beforeTooLarge = data, truncated
	}
	item.Binary = bytes.IndexByte(after[:min(len(after), gitBinarySniff)], 0) >= 0 || bytes.IndexByte(before[:min(len(before), gitBinarySniff)], 0) >= 0
	if afterPresent && !item.Binary && len(after) <= gitConflictMarkMax {
		item.HasMarkers = hasConflictMarker(after, conflictMarkerSizes(ctx, g, []string{p}, func(s string) string { return s })[p])
	}
	diff := func(a, b []byte, tooLarge bool) (string, bool) {
		switch {
		case item.Binary:
			return "", false
		case tooLarge || len(b) > gitConflictMarkMax || *budget <= 0:
			return fmt.Sprintf("--- saved\n+++ now\n(%d bytes before, %d after: too large to show here)\n", len(a), len(b)), true
		}
		text, truncated := unifiedDiff(a, b)
		if len(text) > *budget {
			text, truncated = strings.ToValidUTF8(text[:*budget], "\uFFFD"), true
		}
		*budget -= len(text)
		return text, truncated
	}
	if item.Changed {
		item.Diff, item.DiffTruncated = diff(before, after, beforeTooLarge)
	}
	if item.IndexOid != "" && item.IndexOid != beforeOid {
		staged, truncated, err := g.read(ctx, gitConflictMarkMax, "cat-file", "blob", item.IndexOid)
		if err != nil {
			return unknown()
		}
		text, cut := diff(before, staged, beforeTooLarge || truncated)
		item.IndexDiff, item.DiffTruncated = text, item.DiffTruncated || cut
	}
	return item
}

// diffEdit is one line of an edit script: op ' ', '-' or '+', and the
// positions in the old (ai) and new (bi) text.
type diffEdit struct {
	op     byte
	text   string
	ai, bi int
}

// myersEdits is Myers' O(ND) diff with at most maxD edits; ok is false when
// the texts differ by more.
func myersEdits(a, b []string, maxD int) ([]diffEdit, bool) {
	n, m := len(a), len(b)
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int
	for d := 0; d <= maxD; d++ {
		trace = append(trace, slices.Clone(v))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace, off), true
			}
		}
	}
	return nil, false
}

func backtrack(a, b []string, trace [][]int, off int) []diffEdit {
	x, y := len(a), len(b)
	var script []diffEdit
	for d := len(trace) - 1; d >= 0; d-- {
		v := trace[d]
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		}
		prevX := v[off+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			script = append(script, diffEdit{' ', a[x-1], x - 1, y - 1})
			x, y = x-1, y-1
		}
		if d > 0 {
			if x == prevX {
				script = append(script, diffEdit{'+', b[y-1], x, y - 1})
			} else {
				script = append(script, diffEdit{'-', a[x-1], x - 1, y})
			}
		}
		x, y = prevX, prevY
	}
	slices.Reverse(script)
	return script
}

// unifiedDiff renders a bounded unified diff (three lines of context) of two
// texts; truncated when they are too long or differ too much to compare.
func unifiedDiff(before, after []byte) (string, bool) {
	a, b := splitLines(before), splitLines(after)
	if len(a) > gitReviewMaxLines || len(b) > gitReviewMaxLines {
		return fmt.Sprintf("--- saved\n+++ now\n(%d lines before, %d after: too long to compare here)\n", len(a), len(b)), true
	}
	script, ok := myersEdits(a, b, gitReviewMaxEdits)
	if !ok {
		return fmt.Sprintf("--- saved\n+++ now\n(%d lines before, %d after: too many differences to show here)\n", len(a), len(b)), true
	}
	var out strings.Builder
	out.WriteString("--- saved\n+++ now\n")
	const context = 3
	for k := 0; k < len(script); {
		if script[k].op == ' ' {
			k++
			continue
		}
		start := max(0, k-context)
		end := k
		for end < len(script) {
			if script[end].op != ' ' {
				end++
				continue
			}
			run := end
			for run < len(script) && script[run].op == ' ' {
				run++
			}
			if run-end > 2*context || run == len(script) {
				end = min(len(script), end+context)
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := script[start].ai, script[start].bi, 0, 0
		for _, e := range script[start:end] {
			if e.op != '+' {
				aLen++
			}
			if e.op != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aLen, bStart+1, bLen)
		for _, e := range script[start:end] {
			out.WriteByte(e.op)
			out.WriteString(e.text)
			if !strings.HasSuffix(e.text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		if out.Len() > gitReviewDiffMax {
			return strings.ToValidUTF8(out.String()[:gitReviewDiffMax], "\uFFFD"), true
		}
		k = end
	}
	return strings.ToValidUTF8(out.String(), "\uFFFD"), false
}

// splitLines keeps line terminators so a missing final newline shows.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// recordJobDecision records the user's accept (git.conflict_resolve) or
// reject (git.conflict_restore) of a job path under review, and for the
// content gate what any conflict command (also git.conflict_choose) left
// in the index when that could be read.
func recordJobDecision(s *protocol.Snapshot, top string, c protocol.Command, res *protocol.GitResult, entries string, read bool) {
	rec := gitOperationFor(s, top)
	if res.State != protocol.GitStateSucceeded || rec == nil || c.Git == nil || c.Git.Conflict == nil {
		return
	}
	// The content gate: what a user's resolve or restore left in the index
	// is explained, whether or not a job is still attached.
	if rec.JobBaseline != nil && read {
		if rec.JobDecisions == nil {
			rec.JobDecisions = map[string]string{}
		}
		rec.JobDecisions[c.Git.Conflict.Path] = entries
	}
	if rec.JobThreadID == "" {
		return
	}
	switch rec.State {
	case protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
	default:
		return
	}
	t := threadByID(s, rec.JobThreadID)
	if t == nil || t.Job == nil || !slices.Contains(t.Job.Paths, c.Git.Conflict.Path) {
		return
	}
	decision := ""
	switch c.Kind {
	case protocol.GitKindConflictResolve:
		decision = "accepted"
	case protocol.GitKindConflictRestore:
		decision = "rejected"
	default:
		return
	}
	if t.Job.DecisionsTurn != t.TurnID || t.Job.Decisions == nil {
		t.Job.Decisions, t.Job.DecisionsTurn = map[string]string{}, t.TurnID
	}
	t.Job.Decisions[c.Git.Conflict.Path] = decision
}

// ---- Content gate ----

// stageMap parses `ls-files --stage -z` into each path's entries
// ("mode oid stage", sorted, joined by ";").
func stageMap(out []byte) map[string]string {
	entries := map[string][]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if ok {
			entries[p] = append(entries[p], meta)
		}
	}
	m := make(map[string]string, len(entries))
	for p, list := range entries {
		slices.Sort(list)
		m[p] = strings.Join(list, ";")
	}
	return m
}

// entryOid returns the stage-0 object ID of entries, "" when unmerged or
// absent.
func entryOid(entries string) string {
	for _, e := range strings.Split(entries, ";") {
		if f := strings.Fields(e); len(f) == 3 && f[2] == "0" {
			return f[1]
		}
	}
	return ""
}

// agentChanges compares the index with the record's job baseline at the
// stop stop: every path whose entries differ and that no user decision
// explains is listed, with its staged content against the baseline. When
// the comparison cannot be made (Reason), the set is Incomplete and its
// Fingerprint covers the whole current listing, streamed without a limit.
func agentChanges(ctx context.Context, g *gitReader, rec protocol.GitOperationRecord, dir, stop string) *protocol.GitAgentChanges {
	ac := &protocol.GitAgentChanges{Items: []protocol.GitResolveItem{}}
	var cur, base []byte
	switch {
	case rec.JobBaseline.Incomplete:
		ac.Reason = "the index could not be recorded in full when the resolution agent started"
	case rec.JobBaseline.StopKey != stop:
		ac.Reason = "the operation is no longer at the stop where the resolution agent started"
	default:
		var truncated bool
		var err error
		if cur, truncated, err = g.read(ctx, gitStatusMaxBytes, "ls-files", "--stage", "-z"); err != nil || truncated {
			ac.Reason = "the index is too large or could not be read"
		} else if base, err = getJobBaseline(dir, rec.JobBaseline.Listing); err != nil {
			ac.Reason = "the index recorded when the resolution agent started is missing or damaged"
		}
	}
	h := sha256.New()
	h.Write([]byte("agent-changes-v2\x00" + stop + "\x00"))
	if ac.Reason != "" {
		ac.Incomplete = true
		h.Write([]byte("incomplete\x00"))
		if err := g.stream(ctx, h, "ls-files", "--stage", "-z"); err == nil {
			ac.Fingerprint = hex.EncodeToString(h.Sum(nil))[:32]
		} // else no fingerprint: nothing can be acknowledged
		return ac
	}
	before, now := stageMap(base), stageMap(cur)
	var paths []string
	for p, e := range now {
		if before[p] != e {
			paths = append(paths, p)
		}
	}
	for p := range before {
		if _, ok := now[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	budget := gitReviewTotalDiffs
	for _, p := range paths {
		if d, ok := rec.JobDecisions[p]; ok && d == now[p] {
			continue // the user's own decision left exactly this
		}
		fmt.Fprintf(h, "%s\x00%s\x00", p, now[p])
		if len(ac.Items) >= gitOperationConflictsMax {
			ac.Incomplete, ac.Reason = true, "more changed paths than can be listed"
			continue
		}
		item := protocol.GitResolveItem{Path: p, IndexOid: entryOid(now[p]), Staged: entryOid(now[p]) != "", Deleted: now[p] == "", Changed: true}
		oldOid := entryOid(before[p])
		var old, cur []byte
		if oldOid != "" {
			old, _, _ = g.read(ctx, gitConflictMarkMax, "cat-file", "blob", oldOid)
		}
		if item.IndexOid != "" {
			cur, _, _ = g.read(ctx, gitConflictMarkMax, "cat-file", "blob", item.IndexOid)
		}
		item.Binary = bytes.IndexByte(cur[:min(len(cur), gitBinarySniff)], 0) >= 0 || bytes.IndexByte(old[:min(len(old), gitBinarySniff)], 0) >= 0
		if !item.Binary && budget > 0 {
			text, cut := unifiedDiff(old, cur)
			if len(text) > budget {
				text, cut = strings.ToValidUTF8(text[:budget], "�"), true
			}
			budget -= len(text)
			item.IndexDiff, item.DiffTruncated = text, cut
		}
		ac.Items = append(ac.Items, item)
	}
	if len(ac.Items) > 0 || ac.Incomplete {
		ac.Fingerprint = hex.EncodeToString(h.Sum(nil))[:32]
	}
	return ac
}

// refuseAgentChanges is the content gate of continue. Skip is not gated:
// it resets the index and worktree of the stopped commit, so nothing staged
// is committed by it.
func refuseAgentChanges(ctx context.Context, g *gitReader, w *gitWriter, st protocol.GitOperationState, ack string) error {
	rec := w.record()
	if rec == nil || rec.JobBaseline == nil || !gitOperationActive(rec.State) {
		return nil
	}
	ac := agentChanges(ctx, g, *rec, w.baselineDir, stopKey(st))
	if !(len(ac.Items) > 0 || ac.Incomplete) {
		return nil
	}
	if ac.Fingerprint == "" {
		return failure("review_pending", "the index could not be read to compare it with the state before the resolution agent ran; nothing was changed, try again")
	}
	if ack == ac.Fingerprint {
		return nil
	}
	paths := make([]string, 0, len(ac.Items))
	for _, it := range ac.Items {
		paths = append(paths, it.Path)
	}
	what := "these staged changes are not explained by your own decisions: " + listPaths(paths)
	if ac.Incomplete {
		what = ac.Reason + "; the staged changes cannot be compared in full with the state before the resolution agent ran"
	}
	return failure("review_pending", what+"; review them (GitOperationState.AgentChanges) and acknowledge them, or accept or reject each path")
}

// operationLeftStop reports that a result proves the operation left the
// stop key: it completed or was aborted, or it is observed at another
// stop. Unknown outcomes prove nothing.
func operationLeftStop(op *protocol.GitOperationResult, key string) bool {
	switch op.Outcome {
	case protocol.GitOutcomeCompleted, protocol.GitOutcomeAborted:
		return true
	case protocol.GitOutcomeStopped, protocol.GitOutcomeStoppedConflicts:
		return op.State != nil && op.State.Kind != "" && stopKey(*op.State) != key
	}
	return false
}

// withIndexDecision records what an application stage or unstage of path
// left in the index as a user decision while the content gate is active.
// A failed read after the command records nothing (the path then stays
// unexplained).
func withIndexDecision(p *gitPlan, err error, g *gitReader, w *gitWriter, path string) (*gitPlan, error) {
	if err != nil || p == nil {
		return p, err
	}
	if rec := w.record(); rec == nil || rec.JobBaseline == nil || !gitOperationActive(rec.State) {
		return p, nil
	}
	run, journal := p.run, p.journal
	var entries string
	var read bool
	p.run = func(ctx context.Context) protocol.GitResult {
		res := run(ctx)
		if res.State == protocol.GitStateSucceeded {
			vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
			defer cancel()
			if out, truncated, err := g.read(vctx, 64<<10, "ls-files", "--stage", "-z", "--", path); err == nil && !truncated {
				entries, read = stageMap(out)[path], true
			}
		}
		return res
	}
	p.journal = func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		if journal != nil {
			if err := journal(s, res, now); err != nil {
				return err
			}
		}
		if res != nil && read && res.State == protocol.GitStateSucceeded {
			if rec := gitOperationFor(s, w.top); rec != nil && rec.JobBaseline != nil && gitOperationActive(rec.State) {
				if rec.JobDecisions == nil {
					rec.JobDecisions = map[string]string{}
				}
				rec.JobDecisions[path] = entries
			}
		}
		return nil
	}
	return p, nil
}

// ---- Baseline listings ----
//
// A job baseline's index listing is kept in the application home, named by
// its SHA-256, so Git garbage collection cannot remove it and a damaged
// file is detected. Listings no record references are swept.

const jobBaselineSweepAge = time.Hour

func putJobBaseline(dir string, data []byte) (string, error) {
	if dir == "" {
		return "", failure("unavailable", "no baseline storage")
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	name := filepath.Join(dir, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if _, err := getJobBaseline(dir, id); err == nil {
		now := time.Now()
		_ = os.Chtimes(name, now, now) // a sweep keeps what was just used
		return id, nil
	}
	f, err := os.CreateTemp(dir, "tmp-")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		return "", err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return id, nil
}

func getJobBaseline(dir, id string) ([]byte, error) {
	if dir == "" || len(id) != 64 || !gitFullHash.MatchString(id) {
		return nil, failure("unavailable", "no baseline listing")
	}
	f, err := os.Open(filepath.Join(dir, id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, gitStatusMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != id {
		return nil, failure("unavailable", "the baseline listing is damaged")
	}
	return data, nil
}

// sweepJobBaselines removes listings no operation record references, once
// they are old enough that no job start can still be recording them.
func (e *engine) sweepJobBaselines() {
	e.mu.Lock()
	dir := e.baselineDir
	keep := map[string]bool{}
	for _, rec := range e.snap.GitOperations {
		if rec.JobBaseline != nil {
			keep[rec.JobBaseline.Listing] = true
		}
	}
	e.mu.Unlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if keep[de.Name()] {
			continue
		}
		if info, err := de.Info(); err == nil && time.Since(info.ModTime()) > jobBaselineSweepAge {
			_ = os.Remove(filepath.Join(dir, de.Name()))
		}
	}
}

// record is recordLookup, nil without an engine.
func (w *gitWriter) record() *protocol.GitOperationRecord {
	if w.recordLookup == nil {
		return nil
	}
	return w.recordLookup()
}
