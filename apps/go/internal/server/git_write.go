package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Git writes: stage, unstage, discard and commit (ADR 0020), and the ref and
// remote actions in git_ref.go and git_remote.go (ADR 0021); wire contract in
// protocol/git_write.go.
//
// Flow. A git.* command reserves its identity (a concurrent retry waits for
// it), resolves the checkout to its repository toplevel and, under e.mu,
// takes the checkout writer lease as a non-thread holder unless a thread
// holds it (checkout_busy) or another Git write runs there (git_busy). It then
// revalidates the client's pins with the read policy of git.go, so a pin is
// compared against exactly what status showed, and refuses stale input
// without recording anything. Only then is the command journaled with a
// running receipt and a running GitOp (phase one); Git runs outside the lock
// under a cancellable 5 minute budget, and the final receipt replaces the
// running one (phase two). A server that stops between the phases leaves a
// running receipt, which startup turns into outcome_unknown; a retry never
// re-runs Git.
//
// Policy. Unlike reads, writes resolve Git configuration exactly as the
// user's CLI does, so local filters, hooks, fsmonitor and signing apply. The
// environment keeps what makes an unattended write safe: inherited GIT_*
// variables are removed (as for reads), no editor (GIT_EDITOR=:, core.editor
// and sequence.editor), no pager, no credential prompt, no lazy fetch from
// promisor remotes, no automatic gc or maintenance, and stdin is empty except
// for the commit message. Git runs in its own process group; cancellation
// sends SIGTERM (Git then removes its own locks) and SIGKILL after a grace
// period, so hooks do not outlive it. Locks are never deleted by the server.

const (
	gitWriteBudget      = 5 * time.Minute
	gitWriteOutputMax   = 64 << 10
	gitOpOutputTail     = 8 << 10
	gitCommitMessageMax = 64 << 10
	gitLockRetries      = 3
	gitLockRetryDelay   = 200 * time.Millisecond
	gitOpsRetained      = 32
	gitStopTimeout      = 5 * time.Second
	gitSaveRetries      = 3
	gitSaveRetryDelay   = 100 * time.Millisecond
	// gitHolderPrefix marks a Git write in writerHolder's result.
	gitHolderPrefix = "git:"
)

// gitWriteState is the engine's in-memory Git write bookkeeping, guarded by
// e.mu except for wg.
type gitWriteState struct {
	// holders maps a running command ID to the lease it holds (writer.go)
	// and its target, from lease acquisition until its outcome is recorded.
	holders map[string]gitHold
	// inflight is closed when that command's attempt ends.
	inflight map[string]chan struct{}
	// cancels ends a journaled command's Git process group.
	cancels map[string]context.CancelFunc
	// unsaved keeps final receipts whose phase-two save failed; retries are
	// answered from here and every later Git command retries the save.
	unsaved map[string]gitUnsaved
	// userCancels is git.cancel's handle on a journaled command (ADR 0021).
	userCancels map[string]*gitCancelState
	// opDirs caches each checkout key's discovered Git directory and
	// opPolling reports the waiter re-evaluation loop (git_operation.go).
	opDirs    map[string]gitOpDir
	opPolling bool
	// opSeq counts journaled operation-record changes, so a read that
	// observed the repository before one does not reconcile over it.
	opSeq uint64
	wg    sync.WaitGroup
}

// gitHold is a running Git write. Every hold occupies its repository's Git
// slot (git_busy); only a hold with lease also holds the checkout writer
// lease against agent turns (ADR 0021: fetch, push and branch creation do
// not).
type gitHold struct {
	top, common, threadID, projectID string
	lease, refOp                     bool
}

// gitCancelState is guarded by e.mu. cancellable says whether git.cancel is
// accepted now; requested records that it was.
type gitCancelState struct {
	cancel      context.CancelFunc
	cancellable bool
	requested   bool
}

type gitUnsaved struct {
	c protocol.Command
	r protocol.Receipt
}

func (e *engine) gitLocked() *gitWriteState {
	g := &e.git
	if g.holders == nil {
		g.holders, g.inflight, g.cancels, g.unsaved = map[string]gitHold{}, map[string]chan struct{}{}, map[string]context.CancelFunc{}, map[string]gitUnsaved{}
		g.userCancels = map[string]*gitCancelState{}
		g.opDirs = map[string]gitOpDir{}
	}
	return g
}

// pathsOverlap reports whether one path is the other or lies beneath it.
func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	within := func(x, y string) bool { return x == y || strings.HasPrefix(x, strings.TrimSuffix(y, "/")+"/") }
	return within(a, b) || within(b, a)
}

// gitWriteHolderLocked returns the Git write holding a lease that overlaps key.
func (e *engine) gitWriteHolderLocked(key string) string {
	for id, h := range e.git.holders {
		if h.lease && pathsOverlap(key, h.top) {
			return id
		}
	}
	return ""
}

// gitCommandGuardLocked refuses deleting a thread or removing a project while
// a Git write targeting it (or one of the project's threads) is in progress,
// so its outcome always has somewhere to be recorded.
func (e *engine) gitCommandGuardLocked(c protocol.Command) error {
	if c.Kind != "thread.delete" && c.Kind != "project.remove" {
		return nil
	}
	for id, h := range e.git.holders {
		busy := c.Kind == "thread.delete" && h.threadID == c.ThreadID
		if c.Kind == "project.remove" {
			busy = h.projectID == c.ProjectID
			if t := threadByID(&e.snap, h.threadID); t != nil && t.ProjectID == c.ProjectID {
				busy = true
			}
		}
		if busy {
			return failure("git_busy", "a Git change ("+id+") is running for this target; try again when it finishes")
		}
	}
	return nil
}

// pruneGitOps drops Git write records whose target thread or project no
// longer exists; they are thread- or project-owned data.
func pruneGitOps(s *protocol.Snapshot) {
	projects := map[string]bool{}
	for _, p := range s.Projects {
		projects[p.ID] = true
	}
	kept := s.GitOps[:0]
	for _, op := range s.GitOps {
		if (op.ThreadID == "" || threadByID(s, op.ThreadID) != nil) && (op.ProjectID == "" || projects[op.ProjectID]) {
			kept = append(kept, op)
		}
	}
	s.GitOps = kept
	if len(s.GitOps) == 0 {
		s.GitOps = nil
	}
	ops := s.GitOperations[:0]
	for _, rec := range s.GitOperations {
		if (rec.ThreadID == "" || threadByID(s, rec.ThreadID) != nil) && (rec.ProjectID == "" || projects[rec.ProjectID]) {
			ops = append(ops, rec)
		}
	}
	s.GitOperations = ops
	if len(s.GitOperations) == 0 {
		s.GitOperations = nil
	}
	pruneGitBackups(s)
}

func gitOpName(kind string) string { return strings.TrimPrefix(kind, "git.") }

// validateGitWrite checks the command shape before anything runs.
func validateGitWrite(c protocol.Command) error {
	w := c.Git
	if w == nil {
		return failure("invalid", "Git commands carry their payload in Git")
	}
	if (c.ThreadID == "") == (c.ProjectID == "") {
		return failure("invalid", "select exactly one project or thread")
	}
	if gitOperationKind(c.Kind) {
		return validateGitOperationWrite(c.Kind, w)
	}
	if w.Integrate != nil || w.Operation != nil {
		return failure("invalid", "integrate and operation payloads are not accepted for "+c.Kind)
	}
	if gitRefOrSyncKind(c.Kind) {
		return validateGitRefSync(c.Kind, w)
	}
	if w.Ref != nil || w.Sync != nil || w.Cancel != nil {
		return failure("invalid", "ref, sync and cancel payloads are not accepted here")
	}
	switch c.Kind {
	case protocol.GitKindStage, protocol.GitKindUnstage, protocol.GitKindDiscard:
		if len(w.Paths) != 1 {
			return failure("invalid", "exactly one path is supported")
		}
		if w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished {
			return failure("invalid", "commit fields are not accepted here")
		}
		p := w.Paths[0]
		if !utf8.ValidString(p.Path) || p.Pin == "" {
			return failure("invalid", "each path needs the pin its status entry showed")
		}
		if p.Group == protocol.GitGroupUntracked && strings.HasSuffix(p.Path, "/") && validGitPath(strings.TrimSuffix(p.Path, "/")) {
			return failure("not_supported", "nested repositories cannot be changed here")
		}
		if !validGitPath(p.Path) {
			return failure("invalid", "path must be a relative checkout path outside .git")
		}
		if p.Group == protocol.GitGroupConflicted {
			return failure("conflicted", "conflicted paths cannot be changed here; resolve the conflict first")
		}
		switch c.Kind {
		case protocol.GitKindStage:
			if p.Group != protocol.GitGroupUnstaged && p.Group != protocol.GitGroupUntracked {
				return failure("invalid", "only unstaged or untracked entries can be staged")
			}
		case protocol.GitKindUnstage:
			if p.Group != protocol.GitGroupStaged {
				return failure("invalid", "only staged entries can be unstaged")
			}
		case protocol.GitKindDiscard:
			if p.Group != protocol.GitGroupUnstaged && p.Group != protocol.GitGroupUntracked {
				return failure("invalid", "discard applies to unstaged or untracked entries")
			}
			if !w.Confirmed {
				return failure("confirmation_required", "discarding changes is permanent; confirm it first")
			}
		}
		if c.Kind != protocol.GitKindDiscard && w.Confirmed {
			return failure("invalid", "confirmation applies only to discard")
		}
	case protocol.GitKindCommit:
		if len(w.Paths) != 0 || w.Confirmed {
			return failure("invalid", "commit takes the staged set, not paths")
		}
		if len(w.Message) > gitCommitMessageMax || !utf8.ValidString(w.Message) || strings.ContainsRune(w.Message, 0) {
			return failure("invalid", "commit message must be UTF-8 text up to 64 KiB")
		}
		if strings.TrimSpace(w.Message) == "" {
			return failure("empty_message", "commit message is empty")
		}
		if w.ExpectedHead != protocol.GitUnbornHead && !gitFullHash.MatchString(w.ExpectedHead) {
			return failure("invalid", "expected_head must be the status head_oid or \"unborn\"")
		}
		if w.StagedFingerprint == "" {
			return failure("invalid", "staged_fingerprint from status is required")
		}
		if strings.HasPrefix(w.StagedFingerprint, gitTruncatedFingerprint) {
			return failure("status_truncated", "too many changes to review here; commit from a terminal")
		}
	default:
		return failure("unsupported_command", fmt.Sprintf("unsupported command %q", c.Kind))
	}
	return nil
}

var gitFullHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// gitWriteTargetLocked resolves the command's thread or project to a path.
func gitWriteTargetLocked(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.ThreadID != "" {
		if t := threadByID(s, c.ThreadID); t != nil {
			return t.Checkout, nil
		}
		return "", failure("not_found", "thread does not exist")
	}
	for _, p := range s.Projects {
		if p.ID == c.ProjectID {
			return p.Path, nil
		}
	}
	return "", failure("not_found", "project does not exist")
}

// gitWriteCommand handles every git.* command kind.
func (e *engine) gitWriteCommand(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	if c.Kind == protocol.GitKindCancel {
		return e.gitCancel(c)
	}
	if err := validateGitWrite(c); err != nil {
		return protocol.Receipt{}, err
	}
	e.mu.Lock()
	gs := e.gitLocked()
	for {
		wait, busy := gs.inflight[c.ID]
		if !busy {
			break
		}
		e.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return protocol.Receipt{}, ctx.Err()
		}
		e.mu.Lock()
	}
	e.persistUnsavedGitLocked()
	if u, ok := gs.unsaved[c.ID]; ok {
		e.mu.Unlock()
		if !sameCommand(u.c, c) {
			return protocol.Receipt{}, failure("identity_conflict", "command ID was already used with different content")
		}
		return u.r, nil
	}
	if r, err := e.store.Lookup(c); err != nil || r != nil {
		e.mu.Unlock()
		if r != nil {
			return *r, nil
		}
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return protocol.Receipt{}, err
		}
		// Whether this command ID already ran cannot be told; never imply
		// that nothing happened.
		e.logf("git write receipt lookup failed", "command", c.ID, "error", err)
		return protocol.Receipt{}, failure("unknown_outcome_lookup", "the server could not read whether this change already ran; refresh status before retrying")
	}
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	dir, err := gitWriteTargetLocked(&e.snap, c)
	if err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	}
	done := make(chan struct{})
	gs.inflight[c.ID] = done
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(gs.inflight, c.ID)
		close(done)
		e.mu.Unlock()
	}()
	return e.runGitWrite(ctx, c, dir)
}

func sameCommand(a, b protocol.Command) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// persistUnsavedGitLocked retries saving final receipts that failed to save.
func (e *engine) persistUnsavedGitLocked() {
	for id, u := range e.git.unsaved {
		if e.store.SaveReceipt(e.snap, u.c, u.r) == nil {
			delete(e.git.unsaved, id)
		}
	}
}

// gitPlan is a revalidated write ready to run. runGitWrite sets rt before
// run; a plan reads it through runtime(). journal, when set, updates the
// snapshot being saved with each phase under the engine lock: with a nil
// result in phase one (an error refuses the command, nothing recorded) and
// with the final result in phase two (which it may annotate).
type gitPlan struct {
	paths   []string
	run     func(ctx context.Context) protocol.GitResult
	rt      *gitRuntime
	journal func(s *protocol.Snapshot, res *protocol.GitResult, now string) error
}

// gitRuntime connects a running plan to the engine (ADR 0021). cancelCtx is
// run's context plus git.cancel; progress publishes a throttled GitOp
// progress; setCancellable changes whether git.cancel is accepted and, when
// disabling it, reports false if a cancel already arrived.
type gitRuntime struct {
	cancelCtx      context.Context
	progress       func(phase string, percent int)
	setCancellable func(bool) bool
	// rewrite brackets a Git command that rewrites working-tree files
	// (git_ref.go, beginWorktreeRewrite).
	rewrite func(ctx context.Context, top string) (end func(), err error)
}

func (p *gitPlan) runtime(ctx context.Context) *gitRuntime {
	if p.rt != nil {
		return p.rt
	}
	return &gitRuntime{cancelCtx: ctx, progress: func(string, int) {}, setCancellable: func(bool) bool { return true },
		rewrite: func(context.Context, string) (func(), error) { return func() {}, nil }}
}

// safePrepare and safeRun keep a panic in revalidation or Git handling from
// stranding the lease; they run without e.mu.
func safePrepare(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (plan *gitPlan, err error) {
	defer func() {
		if p := recover(); p != nil {
			plan, err = nil, failure("internal_error", fmt.Sprintf("Git change could not be prepared: %v", p))
		}
	}()
	return prepareGitWrite(ctx, g, w, c)
}

func safeRun(ctx context.Context, plan *gitPlan) (r protocol.GitResult) {
	defer func() {
		if p := recover(); p != nil {
			r = gitResult(protocol.GitStateOutcomeUnknown, "internal_error", fmt.Sprintf("the server failed while Git was running (%v); refresh status to see what changed", p), nil)
		}
	}()
	return plan.run(ctx)
}

func (e *engine) runGitWrite(ctx context.Context, c protocol.Command, dir string) (protocol.Receipt, error) {
	kind := operationKindPolicy(c.Kind, gitKindPolicy(c.Kind))
	prepCtx, cancelPrep := context.WithTimeout(ctx, gitWriteBudget)
	defer cancelPrep()
	if !gitReadable(inspectWorkspace(prepCtx, dir)) {
		return protocol.Receipt{}, failure("not_git", "workspace is not a readable Git checkout")
	}
	g, err := newGitReader(prepCtx, dir)
	if err != nil {
		return protocol.Receipt{}, err
	}
	w, err := newGitWriter(prepCtx, g)
	if err != nil {
		return protocol.Receipt{}, err
	}

	// Take the lease, or refuse; a Git write never queues.
	e.mu.Lock()
	gs := e.gitLocked()
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	// The target may have been deleted while the repository was resolved.
	if again, err := gitWriteTargetLocked(&e.snap, c); err != nil || again != dir {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("not_found", "the target changed while the request was prepared")
	}
	if holder := e.gitThreadHolderLocked(w.top); kind.lease && holder != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("checkout_busy", fmt.Sprintf("thread %q (%s) is working in this checkout; Git changes wait until it finishes", holder.Title, holder.ID))
	}
	// One Git write per worktree; ref and remote actions also serialize
	// with every write in the repository's other linked worktrees, since
	// they share refs.
	refOp := gitRefOrSyncKind(c.Kind) || gitOperationKind(c.Kind)
	for id, h := range gs.holders {
		if h.top == w.top || (h.common == w.commonDir && (refOp || h.refOp)) {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("git_busy", "another Git change ("+id+") is running in this repository")
		}
	}
	gs.holders[c.ID] = gitHold{top: w.top, common: w.commonDir, threadID: c.ThreadID, projectID: c.ProjectID, lease: kind.lease, refOp: refOp}
	e.rebalanceWritersAndFlushLocked()
	e.mu.Unlock()
	release := func() {
		e.mu.Lock()
		delete(gs.holders, c.ID)
		e.rebalanceWritersAndFlushLocked()
		e.mu.Unlock()
	}

	var plan *gitPlan
	for attempt := 0; ; attempt++ {
		plan, err = safePrepare(prepCtx, g, w, c)
		var pe *protocol.Error
		if err == nil || attempt >= gitLockRetries || !errors.As(err, &pe) || (pe.Code != "index_locked" && pe.Code != "ref_locked") {
			break
		}
		select {
		case <-time.After(gitLockRetryDelay):
		case <-prepCtx.Done():
		}
	}
	if err != nil {
		release()
		return protocol.Receipt{}, err
	}

	// Phase one: journal the intent before Git changes anything.
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		release()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	op := protocol.GitOp{Checkout: w.top, CommandID: c.ID, Op: gitOpName(c.Kind), State: protocol.GitStateRunning, Paths: plan.paths,
		ThreadID: c.ThreadID, ProjectID: c.ProjectID, StartedAt: time.Now().UTC().Format(time.RFC3339), Cancellable: kind.cancellable}
	next := clone(e.snap)
	putGitOp(&next, op)
	if plan.journal != nil {
		gs.opSeq++
		if err := plan.journal(&next, nil, op.StartedAt); err != nil {
			e.mu.Unlock()
			release()
			return protocol.Receipt{}, err
		}
	}
	next.Revision++
	r := protocol.Receipt{ID: c.ID, State: protocol.GitStateRunning, Revision: next.Revision, TargetID: w.top, Git: &protocol.GitResult{Op: op.Op, State: protocol.GitStateRunning}}
	if err := e.store.Save(next, &c, &r); err != nil {
		e.mu.Unlock()
		release()
		e.logf("git write not journaled", "command", c.ID, "error", err)
		return protocol.Receipt{}, failure("storage", "the change could not be recorded, so Git did not run")
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	parent := e.runctx
	if parent == nil {
		parent = context.Background()
	}
	opCtx, cancel := context.WithTimeout(parent, kind.budget)
	userCtx, userCancel := context.WithCancel(opCtx)
	gs.cancels[c.ID] = cancel
	gs.userCancels[c.ID] = &gitCancelState{cancel: userCancel, cancellable: kind.cancellable}
	plan.rt = e.gitRuntimeFor(c.ID, userCtx)
	gs.wg.Add(1)
	e.mu.Unlock()
	defer gs.wg.Done()

	result := safeRun(opCtx, plan)
	result.Op = op.Op
	// An operation command stopped by its budget is outcome_unknown whatever
	// Git had done: its hooks and follow-up steps were cut short.
	if opCtx.Err() != nil && (result.State != protocol.GitStateSucceeded || gitOperationKind(c.Kind)) {
		code := "cancelled"
		if (kind.network || gitOperationKind(c.Kind)) && errors.Is(opCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
		result.State, result.Code = protocol.GitStateOutcomeUnknown, code
		if result.Operation != nil {
			result.Operation.Outcome = protocol.GitOutcomeUnknown
		}
		result.Message = "Git was stopped before it finished; refresh status to see what changed"
		if code == "timeout" && gitOperationKind(c.Kind) {
			result.Message = fmt.Sprintf("Git did not finish within its %s budget and was stopped; ", kind.budget) + describeOperationAfter(result.Operation)
			if _, err := os.Lstat(filepath.Join(w.gitDir, "index.lock")); err == nil {
				result.Message += "; Git left index.lock behind: check that no Git process is running, then remove it"
			}
		}
	}
	userCancel()
	cancel()

	// Phase two: record the outcome and release the lease.
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(gs.cancels, c.ID)
	delete(gs.userCancels, c.ID)
	delete(gs.holders, c.ID)
	op.State, op.Code, op.Message, op.Commit = result.State, result.Code, result.Message, result.Commit
	op.Progress, op.Cancellable = nil, false
	op.Output = result.Output
	if len(op.Output) > gitOpOutputTail {
		op.Output = strings.ToValidUTF8(op.Output[len(op.Output)-gitOpOutputTail:], "")
	}
	op.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	next = clone(e.snap)
	putGitOp(&next, op)
	if plan.journal != nil {
		gs.opSeq++
		_ = plan.journal(&next, &result, op.FinishedAt)
	}
	next.Revision++
	r = protocol.Receipt{ID: c.ID, State: result.State, Revision: next.Revision, TargetID: w.top, Git: &result}
	// Memory holds the outcome whatever storage does: retries are answered
	// from it, the save is retried here and on later Git commands, and a
	// server that stops first reports outcome_unknown after restart.
	e.snap = next
	e.lastFlush = time.Now()
	e.publish()
	var saveErr error
	for attempt := 0; attempt < gitSaveRetries; attempt++ {
		if saveErr = e.store.SaveReceipt(e.snap, c, r); saveErr == nil {
			break
		}
		e.mu.Unlock()
		time.Sleep(gitSaveRetryDelay)
		e.mu.Lock()
	}
	if saveErr != nil {
		e.logf("git write outcome not persisted", "command", c.ID, "error", saveErr)
		gs.unsaved[c.ID] = gitUnsaved{c: c, r: r}
		e.dirty = true
	}
	e.rebalanceWritersAndFlushLocked()
	return r, nil
}

// gitThreadHolderLocked returns a thread holding the writer lease of any
// checkout overlapping top: an active turn or an ACP claim in progress.
func (e *engine) gitThreadHolderLocked(top string) *protocol.Thread {
	for i := range e.snap.Threads {
		if t := &e.snap.Threads[i]; activeTurn(t) && pathsOverlap(t.Checkout, top) {
			return t
		}
	}
	for id, key := range e.claiming {
		if pathsOverlap(key, top) {
			if t := threadByID(&e.snap, id); t != nil {
				return t
			}
			return &protocol.Thread{ID: id, Title: "deleted thread"}
		}
	}
	return nil
}

// putGitOp records op as its repository's latest Git write.
func putGitOp(s *protocol.Snapshot, op protocol.GitOp) {
	for i := range s.GitOps {
		if s.GitOps[i].Checkout == op.Checkout {
			s.GitOps = append(s.GitOps[:i], s.GitOps[i+1:]...)
			break
		}
	}
	s.GitOps = append(s.GitOps, op)
	for len(s.GitOps) > gitOpsRetained {
		dropped := false
		for i := range s.GitOps {
			if s.GitOps[i].State != protocol.GitStateRunning {
				s.GitOps = append(s.GitOps[:i], s.GitOps[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}
}

// recoverGitOps marks Git writes that were running when the server stopped
// and reconciles recorded merge and rebase operations (git_operation.go).
func recoverGitOps(s *protocol.Snapshot) {
	recoverGitOperations(s)
	for i := range s.GitOps {
		// Progress is never journaled deliberately, but a snapshot saved for
		// another reason can carry it.
		s.GitOps[i].Progress, s.GitOps[i].Cancellable = nil, false
		if s.GitOps[i].State == protocol.GitStateRunning {
			s.GitOps[i].State, s.GitOps[i].Code = protocol.GitStateOutcomeUnknown, "interrupted"
			s.GitOps[i].Message = gitInterruptedMessage
		}
	}
}

const gitInterruptedMessage = "the server stopped while Git was running; refresh status to see what changed"

// resolveInterruptedGitReceipts is recoverGitOps for stored receipts.
func resolveInterruptedGitReceipts(st interface {
	ResolveRunningReceipts(state, code, message string) (int64, error)
}) error {
	_, err := st.ResolveRunningReceipts(protocol.GitStateOutcomeUnknown, "interrupted", gitInterruptedMessage)
	return err
}

// stopGitWrites cancels running Git writes, waits (bounded) for their
// outcomes, and retries saving any outcome whose save failed. e.stopping must
// already be set.
func (e *engine) stopGitWrites() {
	e.mu.Lock()
	gs := e.gitLocked()
	for _, cancel := range gs.cancels {
		cancel()
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { gs.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(gitStopTimeout):
		e.logf("git writes did not finish before shutdown")
	}
	e.mu.Lock()
	e.persistUnsavedGitLocked()
	// A write still running cannot report; the final snapshot says so. Its
	// stored receipt stays running and becomes outcome_unknown on restart.
	for i := range e.snap.GitOps {
		if e.snap.GitOps[i].State == protocol.GitStateRunning {
			e.snap.GitOps[i].State, e.snap.GitOps[i].Code = protocol.GitStateOutcomeUnknown, "interrupted"
			e.snap.GitOps[i].Message = gitInterruptedMessage
			e.snap.GitOps[i].Progress, e.snap.GitOps[i].Cancellable = nil, false
		}
	}
	for i := range e.snap.GitOperations {
		if rec := &e.snap.GitOperations[i]; rec.State == protocol.GitOperationRunning {
			rec.State, rec.Code, rec.Message = protocol.GitOperationInterrupted, "interrupted", gitInterruptedMessage
		}
	}
	e.mu.Unlock()
}

// gitWriter runs mutating Git commands with CLI-parity configuration.
type gitWriter struct {
	top, gitDir string
	// commonDir is the repository's common directory (shared refs and
	// objects); linked worktrees of one repository share it.
	commonDir string
	restore   bool // `git restore` exists (Git 2.23+)
	// writeFetchHead: `git fetch --write-fetch-head` exists (Git 2.29+,
	// alongside fetch.writeFetchHEAD).
	writeFetchHead bool
}

var gitVersion = sync.OnceValues(func() ([2]int, error) {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		return [2]int{}, err
	}
	m := regexp.MustCompile(`git version (\d+)\.(\d+)`).FindSubmatch(out)
	if m == nil {
		return [2]int{}, errors.New("unrecognized git version")
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	return [2]int{major, minor}, nil
})

func newGitWriter(ctx context.Context, g *gitReader) (*gitWriter, error) {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, failure("unavailable", "Git directory could not be resolved")
	}
	v, err := gitVersion()
	if err != nil {
		return nil, failure("unavailable", "Git version could not be determined")
	}
	atLeast := func(major, minor int) bool { return v[0] > major || (v[0] == major && v[1] >= minor) }
	// GIT_NO_LAZY_FETCH exists from Git 2.44; older Git could fetch missing
	// objects from a promisor remote in the middle of a write.
	if !atLeast(2, 44) && partialClone(ctx, g) {
		return nil, failure("not_supported", "Git 2.44 or newer is required to change a partial clone here")
	}
	common, _, err := g.read(ctx, 4096, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, failure("unavailable", "Git common directory could not be resolved")
	}
	commonDir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(g.dir, commonDir)
	}
	if real, err := filepath.EvalSymlinks(commonDir); err == nil {
		commonDir = real
	}
	return &gitWriter{top: g.dir, gitDir: strings.TrimSpace(string(out)), commonDir: filepath.Clean(commonDir), restore: atLeast(2, 23), writeFetchHead: atLeast(2, 29)}, nil
}

// checkRefLocks refuses while another process holds a lock on one of refs
// (full names such as refs/heads/main) or on packed-refs. Refs live in the
// common directory, shared by linked worktrees.
func (w *gitWriter) checkRefLocks(refs ...string) error {
	for _, name := range append(refs, "packed-refs") {
		if _, err := os.Lstat(filepath.Join(w.commonDir, filepath.FromSlash(name)+".lock")); err == nil {
			return failure("ref_locked", "another Git process holds "+name+".lock; retry when it finishes")
		}
	}
	return nil
}

// partialClone reports extensions.partialClone or any promisor remote; an
// unreadable configuration counts as partial.
func partialClone(ctx context.Context, g *gitReader) bool {
	out, _, err := g.read(ctx, 4096, "config", "--get", "extensions.partialClone")
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return true
	}
	out, _, err = g.read(ctx, 64<<10, "config", "--bool", "--get-regexp", `^remote\..*\.promisor$`)
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return true
	}
	return bytes.Contains(out, []byte(" true"))
}

// run executes one write. stdout is returned when combined is false; the
// bounded output always holds stderr, plus stdout when combined.
func (w *gitWriter) run(ctx context.Context, stdin io.Reader, combined bool, args ...string) (stdout []byte, output *cappedOutput, err error) {
	r := w.runWith(ctx, gitRunOpts{stdin: stdin, combined: combined}, args...)
	return r.stdout, r.output, r.err
}

// gitRunOpts extends run for the ref and remote actions (git_remote.go).
type gitRunOpts struct {
	stdin    io.Reader
	combined bool
	// stall ends Git when it writes nothing for this long (0: never); the
	// run then reports stalled.
	stall time.Duration
	// progress receives Git's --progress phases; progress lines themselves
	// are kept out of the bounded output.
	progress func(phase string, percent int)
	// cMessages makes Git's messages untranslated so they can be parsed
	// (would_overwrite paths, rejection reasons); other locale categories
	// are kept.
	cMessages bool
	// network removes graphical display variables (gitWriteEnv).
	network bool
	// trace2 is a file that receives Git's trace2 events (push uses it to
	// learn whether the pre-push hook refused).
	trace2 string
	// env is added after the policy environment (git_operation.go uses it
	// for a private GIT_INDEX_FILE when writing a backup).
	env []string
}

type gitRunResult struct {
	stdout  []byte
	output  *cappedOutput
	tail    *gitTail
	err     error
	stalled bool
}

// gitKeptEnv reports whether an inherited GIT_* variable is kept for writes:
// variables that configure how the user's own git authenticates, connects,
// finds its global/system configuration or identifies the author (CLI
// parity, ADR 0021). Every other GIT_* variable is removed, in particular
// those that redirect the repository (GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES,
// GIT_COMMON_DIR, GIT_NAMESPACE), inject configuration that must not
// override ours (GIT_CONFIG_COUNT/KEY/VALUE, GIT_CONFIG_PARAMETERS), or
// prompt, page, edit or trace (GIT_ASKPASS, GIT_TERMINAL_PROMPT, GIT_EDITOR,
// GIT_PAGER, GIT_TRACE*).
func gitKeptEnv(key string) bool {
	switch key {
	case "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "GIT_PROXY_COMMAND", "GIT_ALLOW_PROTOCOL", "GIT_PROTOCOL_FROM_USER",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CEILING_DIRECTORIES",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE":
		return true
	}
	for _, prefix := range []string{"GIT_SSL_", "GIT_PROXY_SSL_", "GIT_HTTP_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// gitWriteEnv is the environment of every Git write: GIT_* variables other
// than gitKeptEnv's and pager settings are removed, no editor, prompt,
// askpass or lazy fetch, and no terminal for pinentry (GPG_TTY, SSH_TTY).
// Credential helpers, SSH_AUTH_SOCK and proxy variables are kept. network
// runs (fetch, pull, push) also lose DISPLAY and WAYLAND_DISPLAY, so no
// graphical prompt is started for them; commits keep DISPLAY for a
// graphical pinentry (ADR 0020).
func gitWriteEnv(cMessages, network bool) []string {
	var env []string
	lcAll := ""
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		switch {
		case strings.HasPrefix(key, "GIT_") && !gitKeptEnv(key), key == "PAGER", key == "GPG_TTY", key == "SSH_TTY", key == "SSH_ASKPASS", key == "SSH_ASKPASS_REQUIRE", key == "GCM_INTERACTIVE":
			continue
		case network && (key == "DISPLAY" || key == "WAYLAND_DISPLAY"):
			continue
		case cMessages && (key == "LC_ALL" || key == "LANGUAGE" || key == "LC_MESSAGES"):
			if key == "LC_ALL" {
				lcAll = value
			}
			continue
		}
		env = append(env, entry)
	}
	if cMessages && lcAll != "" {
		// LC_ALL overrode every category; keep that for all but messages.
		env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, "LANG=") })
		env = append(env, "LANG="+lcAll)
	}
	if cMessages {
		env = append(env, "LC_MESSAGES=C")
	}
	// An empty GIT_ASKPASS stops Git from consulting core.askPass and
	// SSH_ASKPASS; SSH_ASKPASS_REQUIRE=never stops ssh from asking.
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never", "GCM_INTERACTIVE=never",
		"GIT_NO_LAZY_FETCH=1", "GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:", "GIT_PAGER=cat", "PAGER=cat")
}

func (w *gitWriter) runWith(ctx context.Context, o gitRunOpts, args ...string) gitRunResult {
	base := []string{
		"--no-pager", "--literal-pathspecs", "-C", w.top,
		"-c", "core.editor=:", "-c", "sequence.editor=:", "-c", "core.pager=cat", "-c", "color.ui=false",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false",
	}
	var watch *gitStallWatch
	if o.stall > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		watch = newGitStallWatch(o.stall, cancel)
		defer watch.stop()
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	// GPG_TTY/SSH_TTY would point pinentry-curses at a user's terminal.
	cmd.Env = gitWriteEnv(o.cMessages, o.network)
	configureGitWriteProcess(cmd)
	if o.trace2 != "" {
		cmd.Env = append(cmd.Env, "GIT_TRACE2_EVENT="+o.trace2)
	}
	cmd.Env = append(cmd.Env, o.env...)
	output := &cappedOutput{limit: gitWriteOutputMax, drain: true}
	tail := &gitTail{}
	out := &cappedOutput{limit: gitStatusMaxBytes, drain: true}
	var errSink io.Writer = io.MultiWriter(output, tail)
	var lines *gitProgressLines
	if o.progress != nil || watch != nil {
		lines = &gitProgressLines{dst: errSink, progress: o.progress, watch: watch}
		errSink = lines
	}
	cmd.Stdin, cmd.Stderr = o.stdin, errSink
	switch {
	case o.combined:
		cmd.Stdout = errSink
	case watch != nil:
		cmd.Stdout = &gitTouchWriter{dst: out, watch: watch}
	default:
		cmd.Stdout = out
	}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if lines != nil {
		lines.flush()
	}
	// A hook's background job can keep stdout/stderr open after Git exits;
	// Git's own exit status still decides, and leftovers are ended.
	lingering := errors.Is(err, exec.ErrWaitDelay)
	if lingering && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if lingering || ctx.Err() != nil {
		endGitGroup(cmd)
	}
	if err == nil && out.truncated() {
		err = errors.New("git output exceeded its bound")
	}
	return gitRunResult{stdout: out.bytes(), output: output, tail: tail, err: err, stalled: watch != nil && watch.fired()}
}

// gitTailMax bounds gitTail.
const gitTailMax = 32 << 10

// gitTail keeps the last gitTailMax bytes written, so markers printed after
// a large hook output are still seen when the bounded output kept only the
// head.
type gitTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *gitTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > gitTailMax {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-gitTailMax:]...)
	}
	return len(p), nil
}

// text is what classification reads: the kept head, plus the tail when the
// head was truncated.
func (r gitRunResult) text() string {
	head := string(r.output.bytes())
	if !r.output.truncated() || r.tail == nil {
		return head
	}
	r.tail.mu.Lock()
	defer r.tail.mu.Unlock()
	return head + "\n" + string(r.tail.buf)
}

// lockFailure classifies Git's own lock refusal from its output.
func lockFailure(output string) string {
	switch {
	case strings.Contains(output, "index.lock"):
		return "index_locked"
	case strings.Contains(output, "HEAD.lock"), strings.Contains(output, "cannot lock ref"), strings.Contains(output, ".lock': File exists"):
		return "ref_locked"
	}
	return ""
}

// checkLocks refuses while Git's index or HEAD lock is held by someone else.
func (w *gitWriter) checkLocks(head bool) error {
	if _, err := os.Lstat(filepath.Join(w.gitDir, "index.lock")); err == nil {
		return failure("index_locked", "another Git process holds index.lock; retry when it finishes")
	}
	if head {
		if _, err := os.Lstat(filepath.Join(w.gitDir, "HEAD.lock")); err == nil {
			return failure("ref_locked", "another Git process holds HEAD.lock; retry when it finishes")
		}
	}
	return nil
}

func gitResult(state, code, message string, output *cappedOutput) protocol.GitResult {
	r := protocol.GitResult{State: state, Code: code, Message: message}
	if output != nil {
		r.Output, r.OutputTruncated = string(output.bytes()), output.truncated()
	}
	return r
}

// failedGit reports a Git command that exited unsuccessfully.
func failedGit(fallback, message string, output *cappedOutput) protocol.GitResult {
	code := fallback
	if output != nil {
		if lock := lockFailure(string(output.bytes())); lock != "" {
			code = lock
		}
	}
	return gitResult(protocol.GitStateFailed, code, message, output)
}

// revalidatedStatus reads status under the read policy (so pins compare
// with exactly what the client was shown). Unscoped reads omit untracked
// files: staged renames are only detected when both sides are in scope.
func revalidatedStatus(ctx context.Context, g *gitReader, path string) (protocol.GitStatus, error) {
	var st protocol.GitStatus
	args := append(statusArgs("no"), "--branch")
	if path != "" {
		args = append(statusArgs("all"), "--", path)
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
	if err != nil {
		return st, gitError(err)
	}
	if truncated {
		return st, failure("unavailable", "too many changes to revalidate this request")
	}
	parseGitStatus(out, false, 0, &st)
	annotateGitStatus(g.dir, &st)
	return st, nil
}

func findEntry(st protocol.GitStatus, path, group string) *protocol.GitStatusEntry {
	for i := range st.Entries {
		if st.Entries[i].Path == path && st.Entries[i].Group == group {
			return &st.Entries[i]
		}
	}
	return nil
}

// pinnedEntry revalidates one path pin against fresh status.
func pinnedEntry(ctx context.Context, g *gitReader, pin protocol.GitPathPin) (*protocol.GitStatusEntry, error) {
	scope := pin.Path
	if pin.Group == protocol.GitGroupStaged {
		scope = ""
	}
	st, err := revalidatedStatus(ctx, g, scope)
	if err != nil {
		return nil, err
	}
	if findEntry(st, pin.Path, protocol.GitGroupConflicted) != nil {
		return nil, failure("conflicted", "this path is conflicted; resolve the conflict first")
	}
	entry := findEntry(st, pin.Path, pin.Group)
	if entry == nil || entry.Pin != pin.Pin {
		// A worktree rename of an intent-to-add entry only pairs up in an
		// unscoped status, so a scoped recheck can never match its pin.
		if pin.Group == protocol.GitGroupUnstaged {
			if full, err := revalidatedStatus(ctx, g, ""); err == nil {
				if e := findEntry(full, pin.Path, pin.Group); e != nil && e.Worktree == "R" {
					return nil, failure("not_supported", "a renamed intent-to-add file cannot be changed here; use a terminal")
				}
			}
		}
		return nil, failure("stale_entry", "this change is no longer what status showed; refresh and review it again")
	}
	if entry.Submodule {
		return nil, failure("not_supported", "submodules cannot be changed here")
	}
	if entry.WorktreeStat == tokenUnavailable {
		return nil, failure("unavailable", "the file could not be examined safely")
	}
	return entry, nil
}

// worktreeNodeSupported refuses a worktree side that is neither a regular
// file, a symlink nor truly absent: a directory (whose contents Git would add
// or delete recursively), a path below a file or symlink, or a special file.
func worktreeNodeSupported(entry *protocol.GitStatusEntry) error {
	switch tokenKind(entry.WorktreeStat) {
	case "reg", "lnk", tokenAbsent:
		return nil
	case "dir":
		return failure("not_supported", "the path is now a directory; change it from a terminal")
	case tokenBlocked:
		return failure("not_supported", "a parent of this path is a file or symlink; change it from a terminal")
	}
	return failure("not_supported", "only regular files and symlinks can be changed here")
}

// pathConflicts refuses when the index (and, with head, HEAD's tree) holds
// an entry other than paths that is an ancestor or descendant of one of
// them: git add, restore and rm on a path silently replace the other side
// of such a file/directory pair, losing staged-only content.
func pathConflicts(ctx context.Context, g *gitReader, paths []string, head bool) error {
	own := map[string]bool{}
	var specs []string
	for _, p := range paths {
		own[p] = true
		specs = append(specs, p)
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			specs = append(specs, dir)
		}
	}
	related := func(name string) bool {
		if own[name] {
			return false
		}
		for _, p := range paths {
			if strings.HasPrefix(name, p+"/") || strings.HasPrefix(p, name+"/") {
				return true
			}
		}
		return false
	}
	check := func(args []string, name func(string) string) error {
		out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(args, append([]string{"--"}, specs...)...)...)
		if err != nil || truncated {
			return failure("unavailable", "the index could not be checked for conflicting paths")
		}
		for _, rec := range strings.Split(string(out), "\x00") {
			if rec != "" && related(name(rec)) {
				return failure("not_supported", "another staged path conflicts with this one (file/directory replacement); use a terminal")
			}
		}
		return nil
	}
	afterTab := func(rec string) string { _, n, _ := strings.Cut(rec, "\t"); return n }
	if err := check([]string{"ls-files", "--stage", "-z"}, afterTab); err != nil {
		return err
	}
	if head {
		return check([]string{"ls-tree", "-r", "-z", "--full-tree", "--name-only", "HEAD"}, func(rec string) string { return rec })
	}
	return nil
}

// recheckConflicts repeats pathConflicts immediately before Git runs.
func recheckConflicts(ctx context.Context, g *gitReader, paths []string, head bool) *protocol.GitResult {
	if err := pathConflicts(ctx, g, paths, head); err != nil {
		var pe *protocol.Error
		errors.As(err, &pe)
		r := gitResult(protocol.GitStateFailed, pe.Code, pe.Message+"; nothing was changed", nil)
		return &r
	}
	return nil
}

// recheckWorktree is the last look immediately before Git runs.
func recheckWorktree(top, path, token string) *protocol.GitResult {
	if worktreeStat(top, path) != token {
		r := gitResult(protocol.GitStateFailed, "stale_entry", "the file changed after review; nothing was changed", nil)
		return &r
	}
	return nil
}

func prepareGitWrite(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	switch c.Kind {
	case protocol.GitKindStage:
		return prepareStage(ctx, g, w, c.Git.Paths[0])
	case protocol.GitKindUnstage:
		return prepareUnstage(ctx, g, w, c.Git.Paths[0])
	case protocol.GitKindDiscard:
		return prepareDiscard(ctx, g, w, c.Git.Paths[0])
	case protocol.GitKindCommit:
		return prepareCommit(ctx, g, w, *c.Git)
	case protocol.GitKindBranchCreate:
		return prepareBranchCreate(ctx, g, w, *c.Git.Ref)
	case protocol.GitKindSwitch:
		return prepareSwitch(ctx, g, w, *c.Git.Ref)
	case protocol.GitKindResetSoft:
		return prepareResetSoft(ctx, g, w, *c.Git.Ref)
	case protocol.GitKindFetch:
		return prepareFetch(ctx, g, w, *c.Git.Sync)
	case protocol.GitKindPull:
		return preparePull(ctx, g, w, *c.Git.Sync)
	case protocol.GitKindPush:
		return preparePush(ctx, g, w, *c.Git.Sync)
	case protocol.GitKindMerge, protocol.GitKindRebase:
		return prepareIntegrate(ctx, g, w, c)
	case protocol.GitKindOperationAbort, protocol.GitKindOperationContinue, protocol.GitKindOperationSkip:
		return prepareOperationCommand(ctx, g, w, c)
	}
	return nil, failure("unsupported_command", fmt.Sprintf("unsupported command %q", c.Kind))
}

// prepareStage hashes the pinned worktree content as `git add` would
// (filters included) so the staged result can be compared with it.
func prepareStage(ctx context.Context, g *gitReader, w *gitWriter, pin protocol.GitPathPin) (*gitPlan, error) {
	entry, err := pinnedEntry(ctx, g, pin)
	if err != nil {
		return nil, err
	}
	if err := w.checkLocks(false); err != nil {
		return nil, err
	}
	if err := worktreeNodeSupported(entry); err != nil {
		return nil, err
	}
	if err := pathConflicts(ctx, g, []string{pin.Path}, false); err != nil {
		return nil, err
	}
	token := entry.WorktreeStat
	removal := token == tokenAbsent
	expect := ""
	if !removal {
		content, err := openWorktreeContent(w.top, pin.Path, entry.WorktreeStat)
		if err != nil {
			return nil, err
		}
		var stdin io.Reader
		args := []string{"hash-object", "--stdin"}
		if content.symlink {
			stdin, args = strings.NewReader(string(content.link)), append(args, "--no-filters")
		} else {
			defer content.file.Close()
			stdin, args = content.file, append(args, "--path="+pin.Path)
		}
		out, output, err := w.run(ctx, stdin, false, args...)
		if err != nil {
			return nil, failure("unavailable", "the file could not be hashed: "+strings.TrimSpace(string(output.bytes())))
		}
		expect = strings.TrimSpace(string(out))
	}
	return &gitPlan{paths: []string{pin.Path}, run: func(ctx context.Context) protocol.GitResult {
		if r := recheckWorktree(w.top, pin.Path, token); r != nil {
			return *r
		}
		if r := recheckConflicts(ctx, g, []string{pin.Path}, false); r != nil {
			return *r
		}
		_, output, err := w.run(ctx, nil, false, "add", "--", pin.Path)
		if err != nil {
			return failedGit("git_failed", "Git could not stage this path", output)
		}
		got, _, err := w.run(ctx, nil, false, "ls-files", "--stage", "-z", "--", pin.Path)
		if err != nil {
			return gitResult(protocol.GitStateSucceeded, "", "Staged; the result could not be verified", output)
		}
		oid := ""
		if f := strings.Fields(strings.TrimSuffix(string(got), "\x00")); len(f) >= 2 {
			oid = f[1]
		}
		switch {
		case removal && oid == "":
			return gitResult(protocol.GitStateSucceeded, "", "Staged removal of "+pin.Path, output)
		case !removal && oid == expect:
			return gitResult(protocol.GitStateSucceeded, "", "Staged "+pin.Path, output)
		}
		return gitResult(protocol.GitStateSucceeded, "staged_newer_content", "Staged "+pin.Path+", but its content changed after review; the newer content is staged", output)
	}}, nil
}

func prepareUnstage(ctx context.Context, g *gitReader, w *gitWriter, pin protocol.GitPathPin) (*gitPlan, error) {
	entry, err := pinnedEntry(ctx, g, pin)
	if err != nil {
		return nil, err
	}
	if err := w.checkLocks(false); err != nil {
		return nil, err
	}
	paths := []string{pin.Path}
	if entry.Index == "R" && entry.OrigPath != "" {
		paths = append(paths, entry.OrigPath)
	}
	unborn := !headExists(ctx, g)
	// Restoring from HEAD (or removing when unborn) rewrites every index entry
	// the pathspec covers, including a path's former file or directory side.
	if err := pathConflicts(ctx, g, paths, !unborn); err != nil {
		return nil, err
	}
	return &gitPlan{paths: paths, run: func(ctx context.Context) protocol.GitResult {
		if r := recheckConflicts(ctx, g, paths, !unborn); r != nil {
			return *r
		}
		var args []string
		switch {
		case unborn:
			args = append([]string{"rm", "--cached", "--quiet", "--force", "--"}, paths...)
		case w.restore:
			args = append([]string{"restore", "--staged", "--"}, paths...)
		default:
			args = append([]string{"reset", "--quiet", "HEAD", "--"}, paths...)
		}
		if _, output, err := w.run(ctx, nil, false, args...); err != nil {
			return failedGit("git_failed", "Git could not unstage this path", output)
		} else {
			return gitResult(protocol.GitStateSucceeded, "", "Unstaged "+strings.Join(paths, " and "), output)
		}
	}}, nil
}

// headExists reports whether HEAD resolves to a commit.
func headExists(ctx context.Context, g *gitReader) bool {
	_, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	return err == nil
}

func prepareDiscard(ctx context.Context, g *gitReader, w *gitWriter, pin protocol.GitPathPin) (*gitPlan, error) {
	entry, err := pinnedEntry(ctx, g, pin)
	if err != nil {
		return nil, err
	}
	token := entry.WorktreeStat
	if pin.Group == protocol.GitGroupUntracked {
		if token == tokenAbsent {
			return nil, failure("stale_entry", "the file no longer exists; refresh status")
		}
		if kind := tokenKind(token); kind != "reg" && kind != "lnk" {
			return nil, failure("not_supported", "only untracked regular files and symlinks can be discarded")
		}
		p := &gitPlan{paths: []string{pin.Path}}
		p.run = func(ctx context.Context) protocol.GitResult {
			// Open documents are saved and paused first (git_ref.go); a
			// saved edit then makes the pinned token stale below.
			end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
			defer end()
			if refused != nil {
				return *refused
			}
			if err := removeUntracked(w.top, pin.Path, token); err != nil {
				var pe *protocol.Error
				errors.As(err, &pe)
				return gitResult(protocol.GitStateFailed, pe.Code, pe.Message, nil)
			}
			return gitResult(protocol.GitStateSucceeded, "", "Deleted untracked "+pin.Path, nil)
		}
		return p, nil
	}
	// Restoring over a directory would delete everything in it, and a path
	// below a file or symlink would replace that node.
	if err := worktreeNodeSupported(entry); err != nil {
		return nil, err
	}
	if err := pathConflicts(ctx, g, []string{pin.Path}, false); err != nil {
		return nil, err
	}
	if err := w.checkLocks(false); err != nil {
		return nil, err
	}
	p := &gitPlan{paths: []string{pin.Path}}
	p.run = func(ctx context.Context) protocol.GitResult {
		// Open documents are saved and paused before Git rewrites the file
		// and reconciled afterwards (git_ref.go, beginWorktreeRewrite). This
		// runs before the last look, so an unsaved edit that the save
		// writes makes the discard stale instead of being overwritten.
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			return *refused
		}
		// Last look before overwriting: a newer edit must not be lost.
		if r := recheckWorktree(w.top, pin.Path, token); r != nil {
			return *r
		}
		if r := recheckConflicts(ctx, g, []string{pin.Path}, false); r != nil {
			return *r
		}
		args := []string{"checkout", "--", pin.Path}
		if w.restore {
			args = []string{"restore", "--worktree", "--", pin.Path}
		}
		if _, output, err := w.run(ctx, nil, false, args...); err != nil {
			return failedGit("git_failed", "Git could not restore this path", output)
		} else {
			return gitResult(protocol.GitStateSucceeded, "", "Discarded changes to "+pin.Path, output)
		}
	}
	return p, nil
}

func prepareCommit(ctx context.Context, g *gitReader, w *gitWriter, req protocol.GitWrite) (*gitPlan, error) {
	st, err := revalidatedStatus(ctx, g, "")
	if err != nil {
		return nil, err
	}
	switch op := g.operation(ctx); op {
	case "merge", "rebase", "cherry-pick", "revert", protocol.GitOperationAm:
		return nil, failure("operation_in_progress", "a "+op+" is in progress; finish or abort it before committing")
	}
	head := st.HeadOid
	if head == "" {
		head = protocol.GitUnbornHead
	}
	var staged []protocol.GitStatusEntry
	for _, e := range st.Entries {
		switch e.Group {
		case protocol.GitGroupConflicted:
			return nil, failure("conflicted", "the index has unmerged paths; resolve them before committing")
		case protocol.GitGroupStaged:
			staged = append(staged, e)
		}
	}
	switch {
	case req.ExpectedHead != head:
		return nil, failure("stale_head", "HEAD moved since status was read; refresh and review again")
	case req.StagedFingerprint != st.StagedFingerprint:
		return nil, failure("stale_status", "staged changes differ from what status showed; refresh and review again")
	case req.Amend && head == protocol.GitUnbornHead:
		return nil, failure("nothing_to_amend", "there is no commit to amend yet")
	case !req.Amend && len(staged) == 0:
		return nil, failure("nothing_staged", "stage changes before committing")
	}
	if published, unknown := g.headOnUpstream(ctx, st.Upstream != ""); req.Amend && (published || unknown) && !req.AcknowledgePublished {
		return nil, failure("published_commit", "HEAD is already on a remote-tracking branch; amending rewrites published history")
	}
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, output, err := w.run(ctx, nil, false, "var", v); err != nil {
			return nil, failure("identity_missing", "Git cannot determine who is committing: "+strings.TrimSpace(string(output.bytes())))
		}
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	expected := stagedTree(staged)
	return &gitPlan{run: func(ctx context.Context) protocol.GitResult {
		args := []string{"commit", "--file=-", "--cleanup=whitespace"}
		if req.Amend {
			args = append(args, "--amend")
		}
		_, output, runErr := w.run(ctx, strings.NewReader(req.Message), true, args...)
		// Verification runs even when the budget ended the commit, so the
		// outcome is established where possible; it has its own bound.
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, _, headErr := g.read(vctx, 4096, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
		newHead := strings.TrimSpace(string(after))
		moved := headErr == nil && newHead != head
		if runErr != nil {
			if !moved {
				return failedGit("commit_failed", "the commit was not created", output)
			}
			r := gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported a failure after HEAD moved; review the new commit", output)
			r.Commit = newHead
			return r
		}
		if !moved {
			return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported success but HEAD did not move", output)
		}
		r := gitResult(protocol.GitStateSucceeded, "", "Committed "+newHead[:min(len(newHead), 12)], output)
		r.Commit = newHead
		if !committedAsStaged(vctx, g, head, newHead, expected) {
			r.Code, r.Message = "hooks_changed_content", "Committed "+newHead[:min(len(newHead), 12)]+"; the staged content changed during the commit (hooks or another process); review what was committed"
		}
		return r
	}}, nil
}

// stagedTree maps each path the commit should change to "mode oid", with
// deletions (including rename sources) as zero mode and object ID.
func stagedTree(staged []protocol.GitStatusEntry) map[string]string {
	tree := map[string]string{}
	for _, e := range staged {
		tree[e.Path] = e.ModeIndex + " " + e.IndexOid
		if e.Index == "R" && e.OrigPath != "" {
			tree[e.OrigPath] = "000000 " + strings.Repeat("0", len(e.IndexOid))
		}
	}
	return tree
}

// committedAsStaged compares what the commit changed relative to the old
// HEAD with the staged set that was pinned.
func committedAsStaged(ctx context.Context, g *gitReader, oldHead, newHead string, expected map[string]string) bool {
	args := []string{"diff-tree", "-r", "-z", "--no-renames", "--raw", "--no-commit-id", "--root", newHead}
	if oldHead != protocol.GitUnbornHead {
		args = []string{"diff-tree", "-r", "-z", "--no-renames", "--raw", oldHead, newHead}
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
	if err != nil || truncated {
		return false
	}
	got := map[string]string{}
	tokens := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(tokens); i += 2 {
		f := strings.Fields(strings.TrimPrefix(tokens[i], ":"))
		if len(f) != 5 {
			return false
		}
		got[tokens[i+1]] = f[1] + " " + f[3]
	}
	if len(got) != len(expected) {
		return false
	}
	for p, v := range expected {
		if got[p] != v {
			return false
		}
	}
	return true
}
