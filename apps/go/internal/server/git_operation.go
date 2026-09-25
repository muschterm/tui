package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Merge and rebase (ADR 0023; wire contract in protocol/git_write.go).
//
// GET /v1/git/operation reads the operation in progress from Git's own state
// files and the index; GET /v1/git/integrate/preview reports what a merge or
// rebase would do. git.merge and git.rebase start one, and
// git.operation_abort, git.operation_continue and git.operation_skip act on
// the operation in progress, whether it was started here or in a terminal.
// All five use the journaled two-phase flow of git_write.go, hold the
// checkout writer lease and bracket Git with beginWorktreeRewrite, because
// every one of them rewrites working-tree files.
//
// Application-started operations are recorded in Snapshot.GitOperations
// (one per repository toplevel). The record is reconciled with the
// repository at startup (recoverGitOps) and on every read; the repository is
// the truth, the record only says whether the operation in progress is the
// one started here and what the user chose as its target.
//
// Reservation. While a merge, rebase, cherry-pick, revert or am is in
// progress in a checkout's worktree, the checkout's writer lease is held by
// that operation (writer.go, operationHolderLocked), so no agent turn starts
// there until it ends, however it was started. The holder is derived from a
// stat of the operation markers in the worktree's Git directory, which is
// discovered without running Git and cached briefly, so it can be evaluated
// under the engine lock. Waiters are re-evaluated once a second while any
// thread waits on an operation, so ending the operation in a terminal also
// releases them. A thread named as a record's JobThreadID (agent resolution,
// not implemented yet) is exempt.

const (
	gitOpHolderPrefix        = "gitop:"
	gitOperationPoll         = time.Second
	gitOpDirTTL              = 2 * time.Second
	gitOpDirCacheMax         = 1024
	gitOperationConflictsMax = 500
	gitOperationBinaryChecks = 64
	gitOperationSubjectMax   = 256
	gitOverwriteListed       = 20
	gitOverwriteCandidates   = 1000
	gitOperationIDMax        = 160
)

// gitOpDir caches one checkout key's discovered Git directory.
type gitOpDir struct {
	dir, real string
	at        time.Time
}

// gitOperationBudget is the budget of a merge, rebase, continue or skip,
// hooks included: replaying many commits takes longer than one write.
var gitOperationBudget = 30 * time.Minute

// operationKindPolicy extends gitKindPolicy for the operation commands.
func operationKindPolicy(kind string, k gitKind) gitKind {
	switch kind {
	case protocol.GitKindMerge, protocol.GitKindRebase, protocol.GitKindOperationContinue, protocol.GitKindOperationSkip:
		k.budget = gitOperationBudget
	}
	return k
}

func gitOperationKind(kind string) bool {
	switch kind {
	case protocol.GitKindMerge, protocol.GitKindRebase, protocol.GitKindOperationAbort, protocol.GitKindOperationContinue, protocol.GitKindOperationSkip:
		return true
	}
	return false
}

// managedOperation reports the operation kinds the commands act on.
func managedOperation(kind string) bool {
	switch kind {
	case protocol.GitOperationMerge, protocol.GitOperationRebase, protocol.GitOperationCherryPick, protocol.GitOperationRevert:
		return true
	}
	return false
}

// validateGitOperationWrite checks a merge, rebase or operation command's
// shape.
func validateGitOperationWrite(kind string, w *protocol.GitWrite) error {
	if len(w.Paths) != 0 || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished || w.Confirmed || w.Ref != nil || w.Sync != nil || w.Cancel != nil {
		return failure("invalid", "only the integrate or operation payload is accepted for "+kind)
	}
	switch kind {
	case protocol.GitKindMerge, protocol.GitKindRebase:
		i := w.Integrate
		if i == nil || w.Operation != nil {
			return failure("invalid", kind+" carries its payload in Git.Integrate")
		}
		// An empty expected branch (detached HEAD) is refused by prepare
		// with detached, so the client learns why.
		if (i.ExpectedBranch != "" && !plausibleBranchName(i.ExpectedBranch)) || !gitFullHash.MatchString(i.ExpectedHead) || !gitFullHash.MatchString(i.TargetOid) {
			return failure("invalid", kind+" needs the branch and head shown in status and a full target hash")
		}
		switch i.Source {
		case protocol.GitIntegrateUpstream, protocol.GitIntegrateBranch:
			if !validFullRef(i.TargetRef) {
				return failure("invalid", "target_ref must be a full local branch or remote-tracking ref")
			}
		case protocol.GitIntegrateCommit:
			if i.TargetRef != "" {
				return failure("invalid", "a commit target has no target_ref")
			}
		default:
			return failure("invalid", "source must be upstream, branch or commit")
		}
		if i.ExpectedReplayCount < 0 || (kind == protocol.GitKindMerge && (i.ExpectedReplayCount != 0 || i.AcknowledgePublished)) {
			return failure("invalid", "replay count and published acknowledgement apply only to rebase")
		}
	default:
		o := w.Operation
		if o == nil || w.Integrate != nil {
			return failure("invalid", kind+" carries its payload in Git.Operation")
		}
		if len(o.OperationID) > gitOperationIDMax || strings.ContainsFunc(o.OperationID, unicode.IsControl) || !utf8.ValidString(o.OperationID) {
			return failure("invalid", "invalid operation_id")
		}
		if !managedOperation(o.Kind) {
			return failure("invalid", "kind must be merge, rebase, cherry-pick or revert")
		}
		if !o.Confirmed {
			return failure("confirmation_required", "confirm this "+o.Kind+" action first")
		}
		if !gitFullHash.MatchString(o.ExpectedHead) || o.ExpectedStep < 0 {
			return failure("invalid", kind+" needs expected_head (and expected_step) from the operation state")
		}
		if kind == protocol.GitKindOperationSkip {
			if o.Kind != protocol.GitOperationRebase {
				return failure("invalid", "only a rebase can skip a commit")
			}
			if !gitFullHash.MatchString(o.SkipOid) {
				return failure("invalid", "skip needs skip_oid, the commit being dropped")
			}
		} else if o.SkipOid != "" {
			return failure("invalid", "skip_oid applies only to skip")
		}
		if kind != protocol.GitKindOperationContinue && o.UnmergedFingerprint != "" {
			return failure("invalid", "unmerged_fingerprint applies only to continue")
		}
		if kind == protocol.GitKindOperationContinue {
			if o.StagedFingerprint == "" || o.WorktreeFingerprint != "" || len(o.AcknowledgeDiscard) != 0 || o.DiscardsFingerprint != "" || o.AcknowledgeDropped != "" || o.AcknowledgeBackupMissing != "" {
				return failure("invalid", "continue pins staged_fingerprint and acknowledges markers only")
			}
		} else if o.WorktreeFingerprint == "" || o.StagedFingerprint != "" || len(o.AcknowledgeMarkers) != 0 || o.MarkersFingerprint != "" || o.AcknowledgeMarkersIncomplete || (kind == protocol.GitKindOperationSkip && o.AcknowledgeDropped != "") {
			return failure("invalid", kind+" pins worktree_fingerprint and acknowledges discarded files only")
		}
		for _, list := range [][]string{o.AcknowledgeDiscard, o.AcknowledgeMarkers} {
			if len(list) > gitStatusMaxItems {
				return failure("invalid", "too many acknowledged paths")
			}
			for _, p := range list {
				if !utf8.ValidString(p) || strings.ContainsRune(p, 0) {
					return failure("invalid", "invalid acknowledged path")
				}
			}
		}
	}
	return nil
}

// ---- Reading the operation in progress ----

// gitStateFileMax bounds a Git state file read.
const gitStateFileMax = 64 << 10

// readStateFile reads a small regular Git state file; "" when absent,
// unreadable, not a regular file or larger than gitStateFileMax. It opens
// without blocking, so a FIFO planted in the Git directory cannot stall a
// read under the engine lock.
func readStateFile(dir, name string) string {
	p := filepath.Join(dir, name)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() > gitStateFileMax {
		return ""
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > gitStateFileMax {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, gitStateFileMax))
	return string(b)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// stateOid reads a state file holding one object ID; only a full hash is
// accepted, so nothing read from the Git directory reaches Git as an option.
func stateOid(dir, name string) string {
	oid := firstLine(readStateFile(dir, name))
	if gitFullHash.MatchString(oid) {
		return oid
	}
	return ""
}

func stateInt(dir, name string) int {
	n, _ := strconv.Atoi(firstLine(readStateFile(dir, name)))
	return max(n, 0)
}

func shortOid(oid string) string { return oid[:min(len(oid), 12)] }

// boundedSubject keeps one bounded line of untrusted commit text.
func boundedSubject(s string) string {
	s = strings.ToValidUTF8(firstLine(s), "�")
	if len(s) <= gitOperationSubjectMax {
		return s
	}
	cut := gitOperationSubjectMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func commitSubject(ctx context.Context, g *gitReader, oid string) string {
	out, _, err := g.read(ctx, 4096, "log", "-1", "--no-show-signature", "--format=%s", oid, "--")
	if err != nil {
		return ""
	}
	return boundedSubject(string(out))
}

// refLabel names oid by a local branch, else a remote-tracking ref pointing
// at it, else its short hash.
func refLabel(ctx context.Context, g *gitReader, oid string) string {
	out, _, err := g.read(ctx, 64<<10, "for-each-ref", "--points-at", oid, "--format=%(refname)", "refs/heads/", "refs/remotes/")
	if err == nil {
		var remote string
		for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
				return name
			}
			if name, ok := strings.CutPrefix(ref, "refs/remotes/"); ok && remote == "" && !strings.HasSuffix(name, "/HEAD") {
				remote = name
			}
		}
		if remote != "" {
			return remote
		}
	}
	return shortOid(oid)
}

// mergeMsgName takes a branch name, which may itself contain quotes, from
// Git's merge message.
var mergeMsgName = regexp.MustCompile(`^Merge (?:remote-tracking )?branch '(.+)'(?: into .+)?$`)

// article returns "a" or "an" for an operation kind ("an am").
func article(kind string) string {
	if kind != "" && strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// observeOperation reads the operation in progress in gitDir's worktree:
// Git's state files, HEAD and the unmerged index entries. It never changes
// anything. Side labels are added by annotateOperation.
func observeOperation(ctx context.Context, g *gitReader, gitDir string) (protocol.GitOperationState, error) {
	st, _, err := observeOperationLists(ctx, g, gitDir)
	return st, err
}

// observeOperationLists is observeOperation plus what abort and skip back
// up (nil when nothing is in progress).
func observeOperationLists(ctx context.Context, g *gitReader, gitDir string) (protocol.GitOperationState, *opLists, error) {
	return observeOperationMode(ctx, g, gitDir, true)
}

// observeOperationMode is observeOperationLists; without full it skips what
// abort, skip and continue would touch (pins, discard lists, markers), for
// callers that only need the operation and its conflicts.
func observeOperationMode(ctx context.Context, g *gitReader, gitDir string, full bool) (protocol.GitOperationState, *opLists, error) {
	var lists *opLists
	st := protocol.GitOperationState{Kind: gitDirOperation(gitDir), Conflicts: []protocol.GitConflict{}}
	head, err := readHead(ctx, g)
	if err != nil {
		return st, nil, err
	}
	st.HeadOid, st.Branch = head.oid, head.branch
	var current string
	switch st.Kind {
	case protocol.GitOperationMerge:
		st.OrigHead = head.oid
		if oid := stateOid(gitDir, "MERGE_HEAD"); oid != "" {
			msg := firstLine(readStateFile(gitDir, "MERGE_MSG"))
			st.Target = &protocol.GitOperationCommit{Oid: oid, Subject: commitSubject(ctx, g, oid)}
			if m := mergeMsgName.FindStringSubmatch(msg); m != nil {
				st.Target.Label = boundedSubject(m[1])
			} else {
				st.Target.Label = refLabel(ctx, g, oid)
			}
			st.Current = &protocol.GitOperationCommit{Oid: oid, Label: st.Target.Label, Subject: boundedSubject(msg)}
		}
	case protocol.GitOperationRebase:
		dir, apply := filepath.Join(gitDir, "rebase-merge"), false
		if _, err := os.Stat(dir); err != nil {
			dir, apply = filepath.Join(gitDir, "rebase-apply"), true
		}
		st.Branch = ""
		if name, ok := strings.CutPrefix(firstLine(readStateFile(dir, "head-name")), "refs/heads/"); ok {
			st.Branch = name
		}
		st.OrigHead = stateOid(dir, "orig-head")
		if onto := stateOid(dir, "onto"); onto != "" {
			st.Target = &protocol.GitOperationCommit{Oid: onto, Label: refLabel(ctx, g, onto), Subject: commitSubject(ctx, g, onto)}
		}
		if apply {
			st.Step, st.Steps = stateInt(dir, "next"), stateInt(dir, "last")
			current = stateOid(dir, "original-commit")
		} else {
			st.Step, st.Steps = stateInt(dir, "msgnum"), stateInt(dir, "end")
			current = stateOid(dir, "stopped-sha")
		}
		if oid := stateOid(gitDir, "REBASE_HEAD"); oid != "" {
			current = oid
		}
		if !apply {
			st.StopReason = interactiveStop(dir)
		}
	case protocol.GitOperationCherryPick:
		st.OrigHead, current = head.oid, stateOid(gitDir, "CHERRY_PICK_HEAD")
	case protocol.GitOperationRevert:
		st.OrigHead, current = head.oid, stateOid(gitDir, "REVERT_HEAD")
	}
	if st.Kind == protocol.GitOperationCherryPick || st.Kind == protocol.GitOperationRevert {
		// A sequence's abort returns to where the sequence started.
		if start := stateOid(filepath.Join(gitDir, "sequencer"), "head"); start != "" {
			st.OrigHead = start
		}
	}
	switch {
	case st.StopReason != "" && head.oid != "":
		// An interactive stop has applied HEAD's commit (edit) or stopped
		// between commits; nothing is being replayed.
		label := "stopped after applying " + shortOid(head.oid) + " for editing"
		if st.StopReason != protocol.GitStopEdit {
			label = "stopped for an interactive step after " + shortOid(head.oid)
		}
		st.Current = &protocol.GitOperationCommit{Oid: head.oid, Label: label, Subject: commitSubject(ctx, g, head.oid)}
	case current != "":
		st.Current = &protocol.GitOperationCommit{Oid: current, Label: shortOid(current), Subject: commitSubject(ctx, g, current)}
	}
	if st.Kind != "" {
		if err := readConflicts(ctx, g, &st); err != nil {
			return st, nil, err
		}
	}
	st.Attempt = attemptID(gitDir, st.Kind)
	if full && managedOperation(st.Kind) {
		if lists, err = observeChanges(ctx, g, gitDir, &st); err != nil {
			return st, nil, err
		}
	}
	st.Can = operationActions(st)
	return st, lists, nil
}

// interactiveStop reports why a merge-backend rebase stopped when it was
// not replaying a commit with conflicts: the last todo command done was not
// a pick (edit, exec, break, reword, squash, fixup...), or Git left an
// amend marker (edit).
func interactiveStop(dir string) string {
	cmd := ""
	for _, line := range strings.Split(readStateFile(dir, "done"), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
			cmd = f[0]
		}
	}
	_, err := os.Lstat(filepath.Join(dir, "amend"))
	switch {
	case cmd == "edit" || cmd == "e" || (err == nil && (cmd == "" || cmd == "pick" || cmd == "p")):
		return protocol.GitStopEdit
	case cmd == "exec" || cmd == "x":
		return protocol.GitStopExec
	case cmd == "break" || cmd == "b":
		return protocol.GitStopBreak
	case cmd == "" || cmd == "pick" || cmd == "p":
		return ""
	}
	return protocol.GitStopInteractive
}

// conflictKind names a path's conflict by the stages present.
func conflictKind(base, ours, theirs bool) string {
	switch {
	case base && ours && theirs:
		return protocol.GitConflictBothModified
	case ours && theirs:
		return protocol.GitConflictBothAdded
	case base && ours:
		return protocol.GitConflictDeletedByThem
	case base && theirs:
		return protocol.GitConflictDeletedByUs
	case ours:
		return protocol.GitConflictAddedByUs
	case theirs:
		return protocol.GitConflictAddedByThem
	}
	return protocol.GitConflictBothDeleted
}

// readConflicts lists unmerged index entries (`ls-files --unmerged`) with
// their stages, and fingerprints every one of them.
func readConflicts(ctx context.Context, g *gitReader, st *protocol.GitOperationState) error {
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, "ls-files", "--unmerged", "-z")
	if err != nil {
		return failure("unavailable", "the index could not be read")
	}
	records := strings.Split(string(out), "\x00")
	records = records[:len(records)-1]
	h := sha256.New()
	h.Write([]byte("unmerged-v1\x00"))
	index := map[string]int{}
	byPath := map[string][]string{}
	for _, rec := range records {
		h.Write([]byte(rec + "\x00"))
		if _, p, ok := strings.Cut(rec, "\t"); ok {
			byPath[p] = append(byPath[p], rec)
		}
		meta, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || len(f[2]) != 1 || f[2][0] < '1' || f[2][0] > '3' {
			continue
		}
		i, seen := index[path]
		if !seen {
			if len(st.Conflicts) >= gitOperationConflictsMax {
				st.ConflictsTruncated = true
				continue
			}
			i = len(st.Conflicts)
			index[path] = i
			st.Conflicts = append(st.Conflicts, protocol.GitConflict{Path: path})
		}
		st.Conflicts[i].Stages[f[2][0]-'1'] = protocol.GitConflictStage{Present: true, Mode: f[0], Oid: f[1]}
	}
	switch {
	case truncated:
		st.ConflictsTruncated, st.UnmergedFingerprint = true, gitTruncatedFingerprint
	case len(records) > 0:
		st.UnmergedFingerprint = hex.EncodeToString(h.Sum(nil))
	}
	if len(st.Conflicts) == 0 {
		return nil
	}
	for i := range st.Conflicts {
		st.Conflicts[i].ConflictPin = indexPin(byPath[st.Conflicts[i].Path])
	}
	top := g.dir
	paths := make([]string, 0, len(st.Conflicts))
	for i := range st.Conflicts {
		c := &st.Conflicts[i]
		c.Kind = conflictKind(c.Stages[0].Present, c.Stages[1].Present, c.Stages[2].Present)
		for _, s := range c.Stages {
			c.Submodule = c.Submodule || s.Mode == "160000"
			c.Symlink = c.Symlink || s.Mode == "120000"
		}
		c.WorktreeStat = worktreeStat(top, c.Path)
		paths = append(paths, c.Path)
	}
	// An unset diff attribute (including the binary macro) marks binary.
	if out, _, err := g.read(ctx, 1<<20, append([]string{"check-attr", "-z", "diff", "--"}, paths...)...); err == nil {
		fields := strings.Split(string(out), "\x00")
		for j := 0; j+2 < len(fields); j += 3 {
			if i, ok := index[fields[j]]; ok && fields[j+2] == "unset" {
				st.Conflicts[i].Binary = true
			}
		}
	}
	// Git's own heuristic: a NUL within the first 8000 bytes of a side.
	for i := range st.Conflicts {
		c := &st.Conflicts[i]
		if c.Binary || c.Submodule {
			continue
		}
		if i >= gitOperationBinaryChecks {
			c.BinaryUnknown = true
			continue
		}
		for _, s := range c.Stages {
			if !s.Present || (s.Mode != "100644" && s.Mode != "100755") || !gitFullHash.MatchString(s.Oid) {
				continue
			}
			data, _, err := g.read(ctx, 8000, "cat-file", "blob", s.Oid)
			if err == nil && bytes.IndexByte(data, 0) >= 0 {
				c.Binary = true
				break
			}
		}
	}
	return nil
}

// operationActions reports which operation commands are accepted now.
func operationActions(st protocol.GitOperationState) protocol.GitOperationActions {
	no := func(reason string) protocol.GitOperationAction { return protocol.GitOperationAction{Reason: reason} }
	yes := protocol.GitOperationAction{Allowed: true}
	var a protocol.GitOperationActions
	switch {
	case st.Kind == "":
		a.Continue, a.Skip, a.Abort = no("nothing is in progress"), no("nothing is in progress"), no("nothing is in progress")
		return a
	case !managedOperation(st.Kind):
		reason := article(st.Kind) + " is not managed here; use a terminal"
		a.Continue, a.Skip, a.Abort = no(reason), no(reason), no(reason)
		return a
	}
	if len(st.HiddenEntries) > 0 {
		reason := "entries marked assume-unchanged or skip-worktree may hide local changes; use a terminal"
		a.Continue, a.Skip, a.Abort = no(reason), no(reason), no(reason)
		return a
	}
	a.Abort = yes
	switch {
	case len(st.AbortBlockedBy) > 0:
		a.Abort = no("unstaged changes to " + listPaths(st.AbortBlockedBy) + " would block the abort; stage or discard them first")
	case len(st.NestedOnAbort) > 0:
		a.Abort = no(nestedReason(st.NestedOnAbort))
	case st.DiscardsOnAbortIncomplete:
		a.Abort = no("too many changes to list what abort would reset; use a terminal")
	}
	interactive := "stopped for an interactive step; use a terminal"
	switch {
	case st.StopReason != "":
		a.Continue = no(interactive)
	case strings.HasPrefix(st.StagedFingerprint, gitTruncatedFingerprint):
		a.Continue = no("too many staged changes to review here; use a terminal")
	case checkPending(st) != nil:
		a.Continue = no(checkPending(st).(*protocol.Error).Message)
	case len(st.ContinueInTheWay) > 0:
		a.Continue = no("untracked or ignored files are in the way of the remaining commits; move them first")
	case st.ContinueUnchecked:
		a.Continue = no("the remaining commits cannot be checked for files in the way; use a terminal")
	case st.UnmergedFingerprint != "":
		n := strconv.Itoa(len(st.Conflicts))
		if st.ConflictsTruncated {
			n = "more than " + n
		}
		a.Continue = no(n + " conflicted paths remain; resolve and stage them first")
	default:
		a.Continue = yes
	}
	switch {
	case st.Kind != protocol.GitOperationRebase:
		a.Skip = no("only a rebase can skip a commit")
	case st.StopReason != "":
		a.Skip = no(interactive)
	case st.Current == nil:
		a.Skip = no("the rebase is not stopped at a commit")
	case len(st.NestedOnSkip) > 0:
		a.Skip = no(nestedReason(st.NestedOnSkip))
	case st.DiscardsOnSkipIncomplete:
		a.Skip = no("too many changes to list what skip would reset; use a terminal")
	case checkPending(st) != nil:
		a.Skip = no(checkPending(st).(*protocol.Error).Message)
	default:
		a.Skip = yes
	}
	return a
}

// annotateOperation marks st as the recorded application operation when rec
// is it, and labels the conflict sides by their roles.
func annotateOperation(st *protocol.GitOperationState, rec *protocol.GitOperationRecord) {
	if st.Kind == "" {
		return
	}
	st.Source = protocol.GitOperationSourceExternal
	if rec != nil && gitOperationActive(rec.State) && recordMatches(*rec, *st) {
		st.Source, st.OperationID = protocol.GitOperationSourceApp, rec.OperationID
		copied := *rec
		st.Record = &copied
		if rec.Target.Label != "" && st.Target != nil {
			st.Target.Label = rec.Target.Label
		}
	}
	st.Sides = operationSides(*st)
	for i := range st.Conflicts {
		st.Conflicts[i].Stages[0].Label = st.Sides.Base
		st.Conflicts[i].Stages[1].Label = st.Sides.Ours
		st.Conflicts[i].Stages[2].Label = st.Sides.Theirs
	}
}

// operationSides names stages 1-3 by role; "ours" and "theirs" mean
// different things for each operation.
func operationSides(st protocol.GitOperationState) protocol.GitConflictSides {
	headSide := "HEAD " + shortOid(st.HeadOid)
	if st.Branch != "" && st.Kind != protocol.GitOperationRebase {
		headSide = "HEAD · " + st.Branch
	}
	commit := func(c *protocol.GitOperationCommit) string {
		if c == nil {
			return "the commit being applied"
		}
		return strings.TrimSpace(shortOid(c.Oid) + " " + c.Subject)
	}
	var s protocol.GitConflictSides
	switch st.Kind {
	case protocol.GitOperationMerge:
		s.Ours, s.Base, s.Theirs = headSide, "common ancestor", "the merged commit"
		if t := st.Target; t != nil {
			s.Theirs = shortOid(t.Oid)
			if t.Label != "" && t.Label != shortOid(t.Oid) {
				s.Theirs = t.Label + " " + shortOid(t.Oid)
			}
		}
	case protocol.GitOperationRebase:
		onto := "upstream"
		if t := st.Target; t != nil {
			onto = t.Label
			if onto == "" {
				onto = shortOid(t.Oid)
			}
		}
		from := st.Branch
		if from == "" {
			from = "detached HEAD"
		}
		s.Ours = onto + " + rebased so far (HEAD " + shortOid(st.HeadOid) + ")"
		s.Theirs = commit(st.Current) + " from " + from
		s.Base = "parent of the replayed commit"
		if st.Current != nil {
			s.Base = "parent of " + shortOid(st.Current.Oid)
		}
	case protocol.GitOperationCherryPick:
		s.Ours, s.Theirs, s.Base = headSide, commit(st.Current), "parent of the picked commit"
		if st.Current != nil {
			s.Base = "parent of " + shortOid(st.Current.Oid)
		}
	case protocol.GitOperationRevert:
		s.Ours, s.Base, s.Theirs = headSide, commit(st.Current), "parent of the reverted commit"
		if st.Current != nil {
			s.Theirs = "parent of " + shortOid(st.Current.Oid) + " (reverting it)"
		}
	}
	return s
}

// ---- Records ----

// gitOperationActive reports record states whose operation may still be in
// progress.
func gitOperationActive(state string) bool {
	switch state {
	case protocol.GitOperationRunning, protocol.GitOperationStoppedConflicts, protocol.GitOperationReady, protocol.GitOperationInterrupted,
		protocol.GitOperationAgentRunning, protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
		return true
	}
	return false
}

// recordMatches reports whether the observed operation is the recorded one:
// same kind and target and, for a rebase, the same original head.
func recordMatches(rec protocol.GitOperationRecord, st protocol.GitOperationState) bool {
	if rec.Kind != st.Kind || st.Target == nil || st.Target.Oid != rec.Target.Oid {
		return false
	}
	return rec.Kind != protocol.GitOperationRebase || st.OrigHead == rec.OrigHead
}

func gitOperationFor(s *protocol.Snapshot, top string) *protocol.GitOperationRecord {
	for i := range s.GitOperations {
		if s.GitOperations[i].Checkout == top {
			return &s.GitOperations[i]
		}
	}
	return nil
}

// putGitOperation records rec as its repository's latest operation.
func putGitOperation(s *protocol.Snapshot, rec protocol.GitOperationRecord) {
	for i := range s.GitOperations {
		if s.GitOperations[i].Checkout == rec.Checkout {
			s.GitOperations[i] = rec
			return
		}
	}
	s.GitOperations = append(s.GitOperations, rec)
	for len(s.GitOperations) > gitOpsRetained {
		dropped := false
		for i := range s.GitOperations {
			if !gitOperationActive(s.GitOperations[i].State) {
				s.GitOperations = append(s.GitOperations[:i], s.GitOperations[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			break
		}
	}
}

func removeGitOperation(s *protocol.Snapshot, id string) {
	for i := range s.GitOperations {
		if s.GitOperations[i].OperationID == id {
			s.GitOperations = append(s.GitOperations[:i], s.GitOperations[i+1:]...)
			break
		}
	}
	if len(s.GitOperations) == 0 {
		s.GitOperations = nil
	}
}

func endRecord(rec *protocol.GitOperationRecord, state, code, message, now string) {
	rec.State, rec.Code, rec.Message, rec.UpdatedAt, rec.EndedAt = state, code, message, now, now
}

const gitOperationEndedMessage = "the operation is no longer in progress in the repository; it was finished or aborted outside the application"

// reconcileRecord brings an idle record in line with the observed
// repository. A running record belongs to the command that is running.
func reconcileRecord(rec *protocol.GitOperationRecord, st protocol.GitOperationState, now string) bool {
	if rec.State == protocol.GitOperationRunning || !gitOperationActive(rec.State) {
		return false
	}
	if !recordMatches(*rec, st) {
		endRecord(rec, protocol.GitOperationEndedExternal, "", gitOperationEndedMessage, now)
		return true
	}
	switch rec.State {
	case protocol.GitOperationAgentRunning, protocol.GitOperationAgentReview, protocol.GitOperationAgentInterrupted:
		// Reserved for agent resolution, which owns these transitions.
		return false
	}
	want := protocol.GitOperationReady
	if st.UnmergedFingerprint != "" {
		want = protocol.GitOperationStoppedConflicts
	}
	if rec.State == want {
		return false
	}
	rec.State, rec.UpdatedAt = want, now
	if rec.Code == "interrupted" {
		rec.Code, rec.Message = "", ""
	}
	return true
}

// recoverGitOperations runs at startup: a record whose command was running
// is interrupted if its operation is still in progress (the first read
// reconciles it) and ended otherwise; an idle record whose repository no
// longer has an operation of its kind in progress has ended. Only file stats
// run here.
func recoverGitOperations(s *protocol.Snapshot) {
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range s.GitOperations {
		rec := &s.GitOperations[i]
		if !gitOperationActive(rec.State) {
			continue
		}
		inProgress := gitDirOperation(discoverGitDir(rec.Checkout)) == rec.Kind
		switch {
		case rec.State == protocol.GitOperationRunning && inProgress:
			rec.State, rec.Code, rec.Message, rec.UpdatedAt = protocol.GitOperationInterrupted, "interrupted", gitInterruptedMessage, now
		case rec.State == protocol.GitOperationRunning:
			endRecord(rec, protocol.GitOperationEndedExternal, "interrupted", "the server stopped while Git was running and the operation is no longer in progress; review the branch", now)
		case !inProgress:
			endRecord(rec, protocol.GitOperationEndedExternal, "", gitOperationEndedMessage, now)
		}
	}
}

// ---- Reservation (writer.go) ----

// discoverGitDir finds the per-worktree Git directory for path as Git's
// discovery does (a .git directory, or a .git file naming "gitdir:", in path
// or an ancestor) without running Git. "" when there is none.
func discoverGitDir(p string) string {
	if !filepath.IsAbs(p) {
		return ""
	}
	for dir := filepath.Clean(p); ; {
		dot := filepath.Join(dir, ".git")
		if fi, err := os.Stat(dot); err == nil {
			if fi.IsDir() {
				return dot
			}
			if !fi.Mode().IsRegular() || fi.Size() > 4096 {
				return ""
			}
			rest, ok := strings.CutPrefix(firstLine(readStateFile(dir, ".git")), "gitdir:")
			if !ok {
				return ""
			}
			gd := strings.TrimSpace(rest)
			if !filepath.IsAbs(gd) {
				gd = filepath.Join(dir, gd)
			}
			return filepath.Clean(gd)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// realPath resolves symlinks, or cleans p when it cannot be resolved.
func realPath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// gitDirForLocked is discoverGitDir cached per checkout key for a moment:
// the key's Git directory and the key itself, both with symlinks resolved.
func (e *engine) gitDirForLocked(key string) (dir, real string) {
	gs := e.gitLocked()
	if c, ok := gs.opDirs[key]; ok && time.Since(c.at) < gitOpDirTTL {
		return c.dir, c.real
	}
	dir, real = discoverGitDir(key), realPath(key)
	if dir != "" {
		dir = realPath(dir)
	}
	if len(gs.opDirs) >= gitOpDirCacheMax {
		clear(gs.opDirs)
	}
	gs.opDirs[key] = gitOpDir{dir: dir, real: real, at: time.Now()}
	return dir, real
}

// operationHolderLocked returns the lease holder for key derived from an
// operation in progress: in the worktree containing key, or in a recorded
// operation's repository overlapping it (paths compared with symlinks
// resolved). Bisect does not reserve the checkout; except, when it is the
// JobThreadID of the record of that operation, is exempt.
func (e *engine) operationHolderLocked(s *protocol.Snapshot, key, except string) string {
	if !filepath.IsAbs(key) {
		return ""
	}
	keyDir, keyReal := e.gitDirForLocked(key)
	dirs := []string{keyDir}
	recDirs := make([]string, len(s.GitOperations))
	for i, rec := range s.GitOperations {
		if !gitOperationActive(rec.State) || !filepath.IsAbs(rec.Checkout) {
			continue
		}
		dir, real := e.gitDirForLocked(rec.Checkout)
		if dir == "" || (dir != keyDir && !pathsOverlap(real, keyReal)) {
			continue
		}
		recDirs[i] = dir
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
next:
	for _, dir := range dirs {
		kind := gitDirOperation(dir)
		if kind == "" || kind == protocol.GitOperationBisect {
			continue
		}
		id := ""
		for i, rec := range s.GitOperations {
			if recDirs[i] == dir && rec.Kind == kind {
				if rec.JobThreadID != "" && rec.JobThreadID == except {
					continue next
				}
				id = rec.OperationID
			}
		}
		return gitOpHolderPrefix + kind + ":" + id
	}
	return ""
}

// jobExemptLocked reports that an operation holds key for everyone except
// id, its resolution job.
func (e *engine) jobExemptLocked(s *protocol.Snapshot, key, id string) bool {
	return e.operationHolderLocked(s, key, "") != "" && e.operationHolderLocked(s, key, id) == ""
}

// operationWait converts an operation holder into a WriterWait.
func operationWait(holder string) (*protocol.WriterWait, bool) {
	rest, ok := strings.CutPrefix(holder, gitOpHolderPrefix)
	if !ok {
		return nil, false
	}
	kind, id, _ := strings.Cut(rest, ":")
	return &protocol.WriterWait{HolderOperation: kind, HolderOperationID: id}, true
}

// ensureOperationPollLocked re-evaluates waiters once a second while any
// thread waits on an operation, since an operation ended outside the
// application produces no event here.
func (e *engine) ensureOperationPollLocked() {
	gs := e.gitLocked()
	if gs.opPolling || e.stopping {
		return
	}
	gs.opPolling = true
	// Tracked with the Git writes, so shutdown waits for the loop; Add runs
	// under e.mu before stopping is set, never concurrently with Wait.
	gs.wg.Add(1)
	go func() {
		defer gs.wg.Done()
		ticker := time.NewTicker(gitOperationPoll)
		defer ticker.Stop()
		for range ticker.C {
			e.mu.Lock()
			if !e.stopping && e.flushErr == nil {
				e.rebalanceWritersAndFlushLocked()
			}
			waiting := false
			for i := range e.snap.Threads {
				if w := e.snap.Threads[i].WriterWait; w != nil && w.HolderOperation != "" {
					waiting = true
				}
			}
			if !waiting || e.stopping || e.flushErr != nil {
				gs.opPolling = false
				e.mu.Unlock()
				return
			}
			e.mu.Unlock()
		}
	}()
}

// ---- Reads ----

func (e *engine) gitOperation(w http.ResponseWriter, r *http.Request) {
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		e.mu.Lock()
		seq := e.gitLocked().opSeq
		e.mu.Unlock()
		st, top, err := readGitOperation(ctx, dir)
		if err != nil || top == "" {
			return st, err
		}
		e.attachOperation(&st, top, seq)
		return st, nil
	})
}

// readGitOperation observes the target's operation; top is the repository
// toplevel ("" for a non-Git target).
func readGitOperation(ctx context.Context, dir string) (protocol.GitOperationState, string, error) {
	st := protocol.GitOperationState{Workspace: inspectWorkspace(ctx, dir), Conflicts: []protocol.GitConflict{}}
	if !gitReadable(st.Workspace) {
		st.Can = operationActions(st)
		return st, "", nil
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return st, "", err
	}
	out, _, err := g.read(ctx, 4096, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return st, "", failure("unavailable", "Git directory could not be resolved")
	}
	observed, err := observeOperation(ctx, g, strings.TrimSpace(string(out)))
	if err != nil {
		return st, "", err
	}
	observed.Workspace = st.Workspace
	return observed, g.dir, nil
}

// attachOperation reconciles top's record with the observation, persists a
// change, re-evaluates waiters and annotates st. seq is the record change
// counter read before observing: when a Git write is running there, or any
// operation record changed since, the observation may predate the record
// and nothing is reconciled.
func (e *engine) attachOperation(st *protocol.GitOperationState, top string, seq uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	gs := e.gitLocked()
	busy := gs.opSeq != seq
	for _, h := range gs.holders {
		busy = busy || h.top == top
	}
	rec := gitOperationFor(&e.snap, top)
	if rec != nil && !busy && !e.stopping && reconcileRecord(rec, *st, time.Now().UTC().Format(time.RFC3339)) {
		e.snap.Revision++
		e.flushLocked()
	}
	e.rebalanceWritersAndFlushLocked()
	annotateOperation(st, gitOperationFor(&e.snap, top))
	if c := e.conflictCopyLocked(top, stopKey(*st)); c != nil && st.Kind != "" {
		st.ConflictCopy = c.CopyID
	}
}

func (e *engine) gitIntegratePreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readIntegratePreview(ctx, dir, q.Get("kind"), q.Get("target"))
	})
}

// integrateTarget resolves a preview target: a full ref or a full hash.
func integrateTarget(ctx context.Context, g *gitReader, target string) (ref, oid string, err error) {
	switch {
	case validFullRef(target):
		tip, _, rerr := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", target+"^{commit}")
		if rerr != nil {
			if gitExitCode(rerr) == 1 {
				return "", "", failure("not_found", "the target ref does not exist")
			}
			return "", "", failure("unavailable", "the target could not be resolved")
		}
		return target, strings.TrimSpace(string(tip)), nil
	case gitFullHash.MatchString(target):
		ok, cerr := commitExists(ctx, g, target)
		if cerr != nil {
			return "", "", cerr
		}
		if !ok {
			return "", "", failure("not_found", "the target commit does not exist here")
		}
		return "", target, nil
	}
	return "", "", failure("invalid", "target must be a full branch or remote-tracking ref, or a full commit hash")
}

// upstreamRef returns the current branch's upstream as a full ref, "" when
// it has none.
func upstreamRef(ctx context.Context, g *gitReader) string {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--symbolic-full-name", "--verify", "--quiet", "--end-of-options", "HEAD@{upstream}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func refShortName(ref string) string {
	if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return name
	}
	return strings.TrimPrefix(ref, "refs/remotes/")
}

func readIntegratePreview(ctx context.Context, dir, kind, target string) (protocol.GitIntegratePreview, error) {
	p := protocol.GitIntegratePreview{Kind: kind}
	if kind != protocol.GitOperationMerge && kind != protocol.GitOperationRebase {
		return p, failure("invalid", "kind must be merge or rebase")
	}
	if !validFullRef(target) && !gitFullHash.MatchString(target) {
		return p, failure("invalid", "target must be a full branch or remote-tracking ref, or a full commit hash")
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return p, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return p, err
	}
	head, err := readHead(ctx, g)
	if err != nil {
		return p, err
	}
	p.Branch, p.HeadOid = head.branch, head.oid
	ref, oid, err := integrateTarget(ctx, g, target)
	if err != nil {
		return p, err
	}
	p.TargetRef, p.TargetOid, p.TargetLabel, p.TargetSubject = ref, oid, shortOid(oid), commitSubject(ctx, g, oid)
	p.Source = protocol.GitIntegrateCommit
	if ref != "" {
		p.Source, p.TargetLabel = protocol.GitIntegrateBranch, refShortName(ref)
		if head.branch != "" && upstreamRef(ctx, g) == ref {
			p.Source = protocol.GitIntegrateUpstream
		}
	}
	block := func(err error) {
		if pe, ok := err.(*protocol.Error); ok && pe != nil && p.Blocked == "" {
			p.Blocked, p.BlockedMessage = pe.Code, pe.Message
		}
	}
	switch {
	case head.oid == "":
		block(failure("unborn", "the branch has no commits yet"))
		return p, nil
	case head.branch == "":
		block(failure("detached", "HEAD is detached; switch to a branch first"))
	}
	if err := refuseOperation(ctx, g, "starting a "+kind); err != nil {
		block(err)
	}
	if err := checkTrackedClean(ctx, g); err != nil {
		block(err)
	}
	if out, _, err := g.read(ctx, 4096, "merge-base", head.oid, oid); err == nil {
		p.MergeBase = strings.TrimSpace(string(out))
	}
	if up, err := isAncestor(ctx, g, oid, head.oid); err != nil {
		return p, err
	} else if up {
		p.UpToDate = true
		block(failure("already_up_to_date", "HEAD already contains "+p.TargetLabel))
	}
	if kind == protocol.GitOperationMerge {
		ff, err := isAncestor(ctx, g, head.oid, oid)
		if err != nil {
			return p, err
		}
		p.MergeFF = mergeFFMode(ctx, g, head.branch)
		p.FastForward = ff && !p.UpToDate && p.MergeFF != protocol.GitMergeNoFF
		if !p.UpToDate {
			block(ffOnlyRefusal(p.MergeFF, ff))
		}
	} else {
		merges, replay, err := rebaseRange(ctx, g, oid, head.oid)
		if err != nil {
			return p, err
		}
		p.ReplayCount, p.RangeHasMerges = replay, merges > 0
		if p.RangeHasMerges {
			block(failure("range_has_merges", "the commits to rebase include merge commits; rebase them in a terminal"))
		}
		p.Published = replay > 0 && leavesPublished(ctx, g, oid, head.oid)
	}
	if !p.UpToDate && p.Blocked == "" {
		block(checkWrittenPaths(ctx, g, head.oid, oid, kind == protocol.GitOperationRebase))
	}
	return p, nil
}

// mergeFFMode is the fast-forward setting `git merge` would use: merge.ff,
// overridden by --ff, --no-ff or --ff-only in branch.<name>.mergeOptions.
func mergeFFMode(ctx context.Context, g *gitReader, branch string) string {
	mode := protocol.GitMergeFF
	if out, _, err := g.read(ctx, 4096, "config", "--get", "merge.ff"); err == nil {
		switch strings.ToLower(strings.TrimSpace(string(out))) {
		case "only":
			mode = protocol.GitMergeFFOnly
		case "false", "no", "off", "0":
			mode = protocol.GitMergeNoFF
		}
	}
	if branch == "" {
		return mode
	}
	if out, _, err := g.read(ctx, 4096, "config", "--get", "branch."+branch+".mergeOptions"); err == nil {
		for _, opt := range strings.Fields(string(out)) {
			switch opt {
			case "--ff":
				mode = protocol.GitMergeFF
			case "--no-ff":
				mode = protocol.GitMergeNoFF
			case "--ff-only":
				mode = protocol.GitMergeFFOnly
			}
		}
	}
	return mode
}

// ffOnlyRefusal refuses a merge that is not a fast forward when only fast
// forwards are configured.
func ffOnlyRefusal(mode string, fastForward bool) error {
	if mode == protocol.GitMergeFFOnly && !fastForward {
		return failure("ff_only_configured", "merge.ff=only (or --ff-only in the branch's mergeOptions) allows only fast forwards, and this merge is not one; rebase, or merge in a terminal")
	}
	return nil
}

// rebaseRange counts the merge commits and all commits in target..head.
func rebaseRange(ctx context.Context, g *gitReader, target, head string) (merges, all int, err error) {
	count := func(extra ...string) (int, error) {
		out, _, err := g.read(ctx, 4096, append([]string{"rev-list", "--count", head, "^" + target}, extra...)...)
		if err != nil {
			return 0, failure("unavailable", "the commits to rebase could not be counted")
		}
		return strconv.Atoi(strings.TrimSpace(string(out)))
	}
	if merges, err = count("--merges"); err != nil {
		return 0, 0, err
	}
	all, err = count()
	return merges, all, err
}

// checkTrackedClean refuses staged or unstaged changes to tracked files and
// unmerged paths; untracked files are allowed.
func checkTrackedClean(ctx context.Context, g *gitReader) error {
	out, truncated, err := g.read(ctx, 4096, statusArgs("no")...)
	if err != nil {
		return failure("unavailable", "status could not be read")
	}
	if truncated || len(out) > 0 {
		return failure("dirty_tree", "the working tree has staged or unstaged changes; commit or discard them first (nothing is stashed)")
	}
	return nil
}

// gitWrittenPathsMax bounds the paths a start checks before Git runs.
const gitWrittenPathsMax = 20000

// operationPaths lists the paths a merge or rebase writes: every path that
// differs between HEAD and the target and, for a rebase, every path a
// replayed commit changes. added holds the paths such a change adds;
// complete is false when there were too many to list.
func operationPaths(ctx context.Context, g *gitReader, head, target string, rebase bool) (written map[string]bool, added []string, complete bool, err error) {
	written, complete = map[string]bool{}, true
	isAdded := map[string]bool{}
	add := func(args ...string) error {
		out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
		if err != nil {
			return failure("unavailable", "the files the operation writes could not be listed")
		}
		if truncated {
			complete = false
		}
		tokens := strings.Split(string(out), "\x00")
		for i := 0; i+1 < len(tokens); i++ {
			status := strings.TrimSpace(tokens[i])
			if len(status) != 1 || status[0] < 'A' || status[0] > 'Z' {
				continue
			}
			i++
			p := tokens[i]
			if p == "" {
				continue
			}
			if len(written) >= gitWrittenPathsMax {
				complete = false
				return nil
			}
			if status == "A" && !isAdded[p] {
				isAdded[p] = true
				added = append(added, p)
			}
			written[p] = true
		}
		return nil
	}
	if err := add("diff-tree", "-r", "-z", "--name-status", "--no-renames", head, target); err != nil {
		return nil, nil, false, err
	}
	if rebase {
		if err := add("log", "-z", "--format=", "--name-status", "--no-renames", head, "^"+target, "--"); err != nil {
			return nil, nil, false, err
		}
	}
	return written, added, complete, nil
}

// lsFilesChunk runs ls-files over paths in bounded batches and returns its
// NUL-separated records; ok is false when a batch failed or was truncated.
func lsFilesChunk(ctx context.Context, g *gitReader, args []string, paths []string) (records []string, ok bool) {
	for len(paths) > 0 {
		n := min(len(paths), 500)
		out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(append(append([]string{"ls-files"}, args...), "-z", "--"), paths[:n]...)...)
		if err != nil || truncated {
			return nil, false
		}
		for _, r := range strings.Split(string(out), "\x00") {
			if r != "" {
				records = append(records, r)
			}
		}
		paths = paths[n:]
	}
	return records, true
}

// blockingAncestor returns the nearest ancestor of p that exists and is not
// a directory, "" when there is none.
func blockingAncestor(top, p string) string {
	for a := path.Dir(p); a != "." && a != "/"; a = path.Dir(a) {
		if fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(a))); err == nil {
			if fi.IsDir() {
				return ""
			}
			return a
		}
	}
	return ""
}

// checkWrittenPaths is the pre-scan before a merge or rebase:
//
//   - an index entry marked assume-unchanged or skip-worktree on a path the
//     operation writes may hide a local change that status cannot see, so
//     the start is refused (not_supported);
//   - an untracked (including ignored) file in the way of a path the target
//     or a replayed commit adds is refused (would_overwrite): Git refuses
//     untracked files but a rebase overwrites ignored ones. A path blocked
//     only by a tracked file that the operation itself replaces (file to
//     directory) is not in the way.
func checkWrittenPaths(ctx context.Context, g *gitReader, head, target string, rebase bool) error {
	written, added, complete, err := operationPaths(ctx, g, head, target, rebase)
	if err != nil {
		return err
	}
	if !complete {
		return failure("not_supported", "too many files change to check them safely here; use a terminal")
	}
	list := make([]string, 0, len(written))
	for p := range written {
		if validGitPath(p) {
			list = append(list, p)
		}
	}
	slices.Sort(list)
	flags, ok := lsFilesChunk(ctx, g, []string{"-v"}, list)
	if !ok {
		return failure("unavailable", "the index could not be checked")
	}
	var hidden []string
	for _, r := range flags {
		tag, p, found := strings.Cut(r, " ")
		if found && len(tag) == 1 && (unicode.IsLower(rune(tag[0])) || tag == "S") {
			hidden = append(hidden, p)
		}
	}
	if len(hidden) > 0 {
		return failure("not_supported", "entries marked assume-unchanged or skip-worktree may hide local changes ("+listPaths(hidden)+"); use a terminal")
	}
	inTheWay, ok := untrackedAt(ctx, g, added)
	switch {
	case !ok:
		return failure("not_supported", "too many files to check for untracked files in the way; use a terminal")
	case len(inTheWay) > 0:
		return failure("would_overwrite", "untracked or ignored files are in the way: "+listPaths(sortedUnique(inTheWay))+"; move them first")
	}
	return nil
}

// listPaths names up to gitOverwriteListed paths for a message.
func listPaths(paths []string) string {
	listed := paths[:min(len(paths), gitOverwriteListed)]
	more := ""
	if len(paths) > len(listed) {
		more = fmt.Sprintf(" and %d more", len(paths)-len(listed))
	}
	return strings.Join(listed, ", ") + more
}

// ---- Commands ----

// operationArgs are the configuration overrides every operation command
// runs with: rerere may rewrite files from a recorded resolution but never
// stages them.
var operationArgs = []string{"-c", "rerere.autoUpdate=false", "-c", "submodule.recurse=false"}

var rerereLine = regexp.MustCompile(`(?m)^(?:Resolved|Staged) '(.+)' using previous resolution\.$`)

func rerereResolved(out string) []string {
	var paths []string
	for _, m := range rerereLine.FindAllStringSubmatch(out, 200) {
		paths = append(paths, m[1])
	}
	return paths
}

// nothingToCommit recognizes Git's refusal to continue with an empty result.
func nothingToCommit(out string) bool {
	for _, marker := range []string{"nothing to commit", "is now empty", "No changes - did you forget", "--allow-empty"} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return false
}

// operationObservation is observeOperation for plans, which already hold
// the writer.
func (w *gitWriter) observe(ctx context.Context, g *gitReader) (protocol.GitOperationState, error) {
	return observeOperation(ctx, g, w.gitDir)
}

// sameStop reports whether two observations are the same operation at the
// same point.
func sameStop(a, b protocol.GitOperationState) bool {
	cur := func(s protocol.GitOperationState) string {
		if s.Current == nil {
			return ""
		}
		return s.Current.Oid
	}
	return a.Kind == b.Kind && a.HeadOid == b.HeadOid && a.Step == b.Step && cur(a) == cur(b)
}

func checkIdentity(ctx context.Context, w *gitWriter) error {
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, output, err := w.run(ctx, nil, false, "var", v); err != nil {
			return failure("identity_missing", "Git cannot determine who is committing: "+strings.TrimSpace(string(output.bytes())))
		}
	}
	return nil
}

// operationOutcome classifies an operation command from the observations
// around it.
func operationOutcome(res *protocol.GitResult, op *protocol.GitOperationResult, before, after protocol.GitOperationState, run gitRunResult, verb string) {
	op.HeadAfter = after.HeadOid
	res.Commit = after.HeadOid
	out := run.text()
	op.RerereResolved = rerereResolved(out)
	remains := after.Kind != ""
	if remains {
		copied := after
		op.State = &copied
	}
	switch {
	case !remains && before.Kind != "":
		// The operation ended.
		op.Outcome = protocol.GitOutcomeCompleted
		*res = gitResult(protocol.GitStateSucceeded, "", "Finished the "+before.Kind, run.output)
		if run.err != nil {
			res.Code, res.Message = "hook_failed", "Finished the "+before.Kind+", but Git reported a failure afterwards (a hook); review the result"
		}
	case remains && after.Kind != before.Kind && before.Kind != "":
		op.Outcome = protocol.GitOutcomeUnknown
		*res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "a different operation is now in progress; review it", run.output)
	case remains && !sameStop(before, after) || (remains && before.Kind == ""):
		if after.UnmergedFingerprint != "" {
			op.Outcome = protocol.GitOutcomeStoppedConflicts
			*res = gitResult(protocol.GitStateSucceeded, "stopped_conflicts", fmt.Sprintf("The %s stopped with %d conflicted paths", after.Kind, len(after.Conflicts)), run.output)
			if after.Kind == protocol.GitOperationRebase && after.Steps > 0 {
				res.Message = fmt.Sprintf("The rebase stopped at %d/%d with %d conflicted paths", after.Step, after.Steps, len(after.Conflicts))
			}
		} else {
			op.Outcome = protocol.GitOutcomeStopped
			*res = gitResult(protocol.GitStateSucceeded, "stopped", "The "+after.Kind+" stopped before finishing; review, then continue or abort", run.output)
		}
	case run.err != nil && nothingToCommit(out):
		op.Outcome = protocol.GitOutcomeUnchanged
		*res = gitResult(protocol.GitStateFailed, "nothing_to_commit", "the resolution leaves nothing to commit, so Git did not "+verb+"; skip the commit or finish in a terminal", run.output)
	case run.err != nil:
		op.Outcome = protocol.GitOutcomeUnchanged
		*res = failedGit("git_failed", "Git did not "+verb, run.output)
	default:
		op.Outcome = protocol.GitOutcomeUnknown
		*res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported success but the operation did not move; review it", run.output)
	}
	res.Commit = after.HeadOid
}

// operationJournal keeps the matching record in step with an operation
// command. Phase one refuses a request whose OperationID is not the recorded
// operation in progress and marks a matching record running; phase two
// applies the outcome.
func operationJournal(c protocol.Command, top string, observed protocol.GitOperationState) func(*protocol.Snapshot, *protocol.GitResult, string) error {
	req := c.Git.Operation
	matched := ""
	var previous string
	return func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		rec := gitOperationFor(s, top)
		if res == nil {
			if rec != nil && gitOperationActive(rec.State) && recordMatches(*rec, observed) {
				matched, previous = rec.OperationID, rec.State
			}
			if req.OperationID != "" && req.OperationID != matched {
				return failure("stale_operation", "the operation in progress is not the one this request names; refresh")
			}
			if matched != "" {
				rec.State, rec.LastCommandID, rec.UpdatedAt = protocol.GitOperationRunning, c.ID, now
			}
			return nil
		}
		if res.Operation != nil && res.Operation.Backup != nil {
			putGitBackup(s, protocol.GitBackupEntry{Checkout: top, CheckoutKey: checkoutKey(top), Oid: res.Operation.Backup.Oid, IndexOid: res.Operation.Backup.IndexOid, CommandID: c.ID, CreatedAt: res.Operation.Backup.CreatedAt})
		}
		if res.Operation != nil {
			res.Operation.OperationID = matched
			if res.Operation.State != nil {
				annotateOperation(res.Operation.State, rec)
			}
		}
		if matched == "" || rec == nil || rec.OperationID != matched {
			return nil
		}
		applyOperationResult(rec, res, previous, now)
		if res.Operation != nil && res.Operation.State != nil {
			annotateOperation(res.Operation.State, rec)
		}
		return nil
	}
}

// applyOperationResult moves a record to the state its latest command left.
func applyOperationResult(rec *protocol.GitOperationRecord, res *protocol.GitResult, previous, now string) {
	rec.Code, rec.Message, rec.UpdatedAt = res.Code, res.Message, now
	op := res.Operation
	if op != nil && op.Backup != nil {
		rec.Backup = op.Backup
	}
	switch {
	case op != nil && op.Outcome == protocol.GitOutcomeCompleted:
		endRecord(rec, protocol.GitOperationCompleted, res.Code, res.Message, now)
	case op != nil && op.Outcome == protocol.GitOutcomeAborted:
		endRecord(rec, protocol.GitOperationAborted, res.Code, res.Message, now)
	case op != nil && op.State != nil && recordMatches(*rec, *op.State):
		rec.State = protocol.GitOperationReady
		if op.State.UnmergedFingerprint != "" {
			rec.State = protocol.GitOperationStoppedConflicts
		}
	case op != nil && op.State == nil && op.Outcome != protocol.GitOutcomeUnknown:
		endRecord(rec, protocol.GitOperationEndedExternal, res.Code, res.Message, now)
	case previous != "" && res.State == protocol.GitStateFailed:
		rec.State = previous
	default:
		rec.State = protocol.GitOperationInterrupted
	}
}

// prepareOperationCommand prepares git.operation_abort, _continue and
// _skip against the operation in progress.
func prepareOperationCommand(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.Operation
	st, lists, err := observeOperationLists(ctx, g, w.gitDir)
	if err != nil {
		return nil, err
	}
	if lists == nil {
		lists = &opLists{}
	}
	switch {
	case st.Kind == "":
		return nil, failure("no_operation", "no merge, rebase, cherry-pick or revert is in progress")
	case !managedOperation(st.Kind):
		return nil, failure("not_supported", article(st.Kind)+" is in progress; it is not managed here, use a terminal")
	case st.Kind != req.Kind:
		return nil, failure("stale_operation", article(st.Kind)+" is in progress, not "+article(req.Kind)+"; refresh")
	case st.HeadOid != req.ExpectedHead:
		return nil, failure("stale_head", "HEAD moved since the operation was read; refresh and review again")
	}
	verb := "abort"
	switch c.Kind {
	case protocol.GitKindOperationContinue:
		verb = "continue"
		switch {
		case st.StopReason != "":
			return nil, failure("not_supported", "the rebase stopped for an interactive step ("+st.StopReason+"); continue it in a terminal")
		case st.Step != req.ExpectedStep:
			return nil, failure("stale_operation", "the operation moved on since it was read; refresh")
		case st.UnmergedFingerprint != "":
			return nil, failure("conflicted", st.Can.Continue.Reason)
		case req.UnmergedFingerprint != st.UnmergedFingerprint:
			return nil, failure("stale_status", "the conflicts changed since they were read; refresh and review again")
		case strings.HasPrefix(st.StagedFingerprint, gitTruncatedFingerprint):
			return nil, failure("status_truncated", "too many staged changes to review here; continue in a terminal")
		case req.StagedFingerprint != st.StagedFingerprint:
			return nil, failure("stale_status", "the staged changes differ from what was reviewed; refresh and review what will be committed")
		case len(st.HiddenEntries) > 0:
			return nil, failure("not_supported", "entries marked assume-unchanged or skip-worktree may hide local changes ("+listPaths(st.HiddenEntries)+"); use a terminal")
		case !(req.MarkersFingerprint != "" && req.MarkersFingerprint == st.MarkersFingerprint) && !slices.Equal(sortedCopy(req.AcknowledgeMarkers), st.MarkerPaths):
			return nil, failure("markers_unacknowledged", "staged files still contain conflict markers: "+listPaths(st.MarkerPaths)+"; fix them, or confirm committing them as they are")
		case st.MarkersIncomplete && !req.AcknowledgeMarkersIncomplete:
			return nil, failure("markers_incomplete", "not every staged file could be checked for conflict markers; review them, then confirm continuing without a complete check")
		}
		if err := checkPending(st); err != nil {
			return nil, err
		}
		if err := checkIdentity(ctx, w); err != nil {
			return nil, err
		}
	case protocol.GitKindOperationSkip:
		verb = "skip"
		switch {
		case st.StopReason != "":
			return nil, failure("not_supported", "the rebase stopped for an interactive step ("+st.StopReason+"), not at a conflicting commit; use a terminal")
		case st.Current == nil:
			return nil, failure("not_stopped", "the rebase is not stopped at a commit")
		case st.Step != req.ExpectedStep || st.Current.Oid != req.SkipOid:
			return nil, failure("stale_operation", "the rebase moved on since it was read; refresh and confirm the commit to skip again")
		}
		if len(st.NestedOnSkip) > 0 {
			return nil, failure("not_supported", nestedReason(st.NestedOnSkip))
		}
		if err := checkDiscards(st, req, st.DiscardsOnSkip, st.DiscardsOnSkipIncomplete, "skip"); err != nil {
			return nil, err
		}
		if err := checkPending(st); err != nil {
			return nil, err
		}
		if err := checkIdentity(ctx, w); err != nil {
			return nil, err
		}
	default:
		if len(st.NestedOnAbort) > 0 {
			return nil, failure("not_supported", nestedReason(st.NestedOnAbort))
		}
		if len(st.AbortBlockedBy) > 0 {
			return nil, failure("abort_blocked", "unstaged changes to "+listPaths(st.AbortBlockedBy)+" would make Git refuse the abort; stage or discard them first")
		}
		if err := checkDiscards(st, req, st.DiscardsOnAbort, st.DiscardsOnAbortIncomplete, "abort"); err != nil {
			return nil, err
		}
		if st.AbortDropsCount > 0 && req.AcknowledgeDropped != st.AbortDropsFingerprint {
			return nil, failure("drops_unacknowledged", fmt.Sprintf("the abort removes %d commits the %s already made; confirm dropping them", st.AbortDropsCount, st.Kind))
		}
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	missing, missingFP := st.BackupMissingOnAbort, st.BackupMissingOnAbortFingerprint
	if c.Kind == protocol.GitKindOperationSkip {
		missing, missingFP = st.BackupMissingOnSkip, st.BackupMissingOnSkipFingerprint
	}
	if c.Kind != protocol.GitKindOperationContinue && len(missing) > 0 && req.AcknowledgeBackupMissing != missingFP {
		return nil, failure("backup_incomplete", "these files cannot be backed up before the "+verb+" (more than 1000 files or 64 MiB, or special files): "+listPaths(missing)+"; confirm running without backing them up, or use a terminal")
	}
	skipped := st.Current
	p := &gitPlan{journal: operationJournal(c, w.top, st)}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: st.Kind, HeadBefore: st.HeadOid}
		defer func() { res.Operation = op }()
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			op.Outcome = protocol.GitOutcomeUnchanged
			res = *refused
			return res
		}
		// Last look: saving documents or another process may have moved it.
		again, againLists, err := observeOperationLists(ctx, g, w.gitDir)
		switch {
		case err != nil:
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "unavailable", "the operation could not be read; nothing was changed", nil)
			return res
		case !sameStop(st, again) || (c.Kind == protocol.GitKindOperationContinue && again.UnmergedFingerprint != ""):
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "stale_operation", "the operation changed after review; nothing was changed", nil)
			return res
		case c.Kind == protocol.GitKindOperationContinue && (again.StagedFingerprint != st.StagedFingerprint || !slices.Equal(again.MarkerPaths, st.MarkerPaths) || again.MarkersIncomplete != st.MarkersIncomplete),
			c.Kind != protocol.GitKindOperationContinue && (again.WorktreeFingerprint != st.WorktreeFingerprint ||
				!slices.Equal(again.DiscardsOnAbort, st.DiscardsOnAbort) || !slices.Equal(again.DiscardsOnSkip, st.DiscardsOnSkip)):
			// Saved documents count: they were flushed before this look.
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "stale_status", "saving open documents or another program changed files after review; nothing was changed; review again", nil)
			return res
		case c.Kind == protocol.GitKindOperationAbort && len(again.NestedOnAbort) > 0,
			c.Kind == protocol.GitKindOperationSkip && len(again.NestedOnSkip) > 0:
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "not_supported", "a nested repository or untracked directory is now in the way; nothing was changed; resolve it in a terminal", nil)
			return res
		case again.AbortDropsFingerprint != st.AbortDropsFingerprint:
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "stale_operation", "the commits the abort would drop changed after review; nothing was changed", nil)
			return res
		case c.Kind != protocol.GitKindOperationAbort && checkPending(again) != nil:
			pe := checkPending(again).(*protocol.Error)
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, pe.Code, pe.Message+"; nothing was changed", nil)
			return res
		}
		var removed []string
		if c.Kind != protocol.GitKindOperationContinue {
			// A copy of everything the command overwrites, made after the
			// documents were saved and before Git runs.
			paths := lists.abort
			if c.Kind == protocol.GitKindOperationSkip {
				paths = lists.skip
			}
			if againLists != nil {
				paths = againLists.abort
				if c.Kind == protocol.GitKindOperationSkip {
					paths = againLists.skip
				}
			}
			backup, tokens, err := w.makeBackup(ctx, g, paths, verb+" of the "+st.Kind)
			switch {
			case err != nil:
				op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "backup_incomplete", "the files could not be backed up ("+err.Error()+"); nothing was changed", nil)
				return res
			case backup.Incomplete && pathsFingerprint(backup.Missing) != req.AcknowledgeBackupMissing:
				op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "backup_incomplete", "these files could not be backed up: "+listPaths(backup.Missing)+"; nothing was changed", nil)
				return res
			}
			op.Backup = backup
			// Untracked and ignored files in the way were listed,
			// acknowledged and backed up: remove them so Git neither
			// refuses the command nor overwrites them itself.
			remove := lists.abortRemove
			if againLists != nil {
				remove = againLists.abortRemove
			}
			if c.Kind == protocol.GitKindOperationSkip {
				remove = lists.skipRemove
				if againLists != nil {
					remove = againLists.skipRemove
				}
			}
			for _, rp := range remove {
				b, ok := tokens[rp]
				if !ok {
					continue // not backed up (acknowledged as missing): Git decides
				}
				// Unlinked only while it is exactly what the backup read.
				if err := removeUntracked(w.top, rp, b.token); err != nil {
					note := restoreRemoved(ctx, g, w, backup, tokens, removed)
					op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "stale_entry", "an untracked file in the way changed after it was backed up ("+rp+"); Git did not run"+note, nil)
					if strings.Contains(note, "could not") {
						op.Outcome, res.State = protocol.GitOutcomeUnknown, protocol.GitStateOutcomeUnknown
					}
					return res
				}
				removed = append(removed, rp)
			}
			// Whatever happens next, files moved aside that Git did not
			// replace are put back byte-exact from the backup.
			defer func() {
				if len(removed) == 0 {
					return
				}
				note := restoreRemoved(context.WithoutCancel(ctx), g, w, backup, tokens, removed)
				res.Message += note
				if strings.Contains(note, "could not") {
					res.State, op.Outcome = protocol.GitStateOutcomeUnknown, protocol.GitOutcomeUnknown
				}
			}()
		}
		args := append(append([]string{}, operationArgs...), st.Kind, "--"+verb)
		run := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, args...)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, err := w.observe(vctx, g)
		if err != nil {
			op.Outcome, res = protocol.GitOutcomeUnknown, gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the result could not be read; refresh", run.output)
			return res
		}
		if c.Kind == protocol.GitKindOperationAbort {
			op.HeadAfter, op.RerereResolved = after.HeadOid, nil
			switch {
			case after.Kind == "":
				op.Outcome = protocol.GitOutcomeAborted
				res = gitResult(protocol.GitStateSucceeded, "", "Aborted the "+st.Kind+"; HEAD is at "+shortOid(after.HeadOid), run.output)
				if run.err != nil {
					res.Code = "hook_failed"
				}
				written := lists.written
				if againLists != nil {
					written = againLists.written
				}
				if left := leftoverPaths(vctx, g, written); len(left) > 0 {
					res.Code, res.Paths = "abort_incomplete", left
					res.Message = "Aborted the " + st.Kind + ", but these paths still differ from what the abort restored: " + listPaths(left) + "; review them"
				}
			case sameStop(st, after) && run.err != nil:
				op.Outcome, res = protocol.GitOutcomeUnchanged, failedGit("git_failed", "Git did not abort", run.output)
				copied := after
				op.State = &copied
			default:
				op.Outcome, res = protocol.GitOutcomeUnknown, gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the abort did not finish cleanly; review the repository", run.output)
				copied := after
				op.State = &copied
			}
			res.Commit = after.HeadOid
			return res
		}
		operationOutcome(&res, op, st, after, run, verb)
		if c.Kind == protocol.GitKindOperationSkip && op.Outcome != protocol.GitOutcomeUnchanged {
			op.Skipped = skipped
		}
		if op.Outcome == protocol.GitOutcomeStoppedConflicts {
			_ = w.ensureConflictCopy(vctx, p, after, op) // best effort; conflict commands retry
		}
		return res
	}
	return p, nil
}

// integrateTargetPinned re-checks the target as requested: its ref (and
// for upstream, that it is still the upstream) still points at TargetOid.
func integrateTargetPinned(ctx context.Context, g *gitReader, req protocol.GitIntegrate) (label string, err error) {
	switch req.Source {
	case protocol.GitIntegrateCommit:
		ok, err := commitExists(ctx, g, req.TargetOid)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", failure("unknown_commit", "the target commit does not exist here; refresh and choose it again")
		}
		return shortOid(req.TargetOid), nil
	case protocol.GitIntegrateUpstream:
		switch up := upstreamRef(ctx, g); {
		case up == "":
			return "", failure("no_upstream", "the branch has no upstream")
		case up != req.TargetRef:
			return "", failure("stale_upstream", "the branch's upstream is not "+refShortName(req.TargetRef)+" any more; refresh")
		}
	}
	tip, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", req.TargetRef+"^{commit}")
	if err != nil && gitExitCode(err) != 1 {
		return "", failure("unavailable", "the target could not be resolved")
	}
	if err != nil || strings.TrimSpace(string(tip)) != req.TargetOid {
		return "", failure("stale_target", refShortName(req.TargetRef)+" moved or no longer exists since it was shown; refresh and review again")
	}
	return refShortName(req.TargetRef), nil
}

func mergeMessage(req protocol.GitIntegrate, label string) string {
	switch {
	case strings.HasPrefix(req.TargetRef, "refs/heads/"):
		return "Merge branch '" + label + "'"
	case strings.HasPrefix(req.TargetRef, "refs/remotes/"):
		return "Merge remote-tracking branch '" + label + "'"
	}
	return "Merge commit '" + req.TargetOid + "'"
}

// prepareIntegrate prepares git.merge and git.rebase.
func prepareIntegrate(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.Integrate
	rebase := c.Kind == protocol.GitKindRebase
	kind := protocol.GitOperationMerge
	if rebase {
		kind = protocol.GitOperationRebase
	}
	cur, err := readHead(ctx, g)
	if err != nil {
		return nil, err
	}
	switch {
	case cur.oid == "":
		return nil, failure("unborn", "the branch has no commits yet")
	case cur.branch == "":
		return nil, failure("detached", "HEAD is detached; switch to a branch first")
	}
	if err := checkHeadPins(cur, req.ExpectedBranch, req.ExpectedHead); err != nil {
		return nil, err
	}
	if err := refuseOperation(ctx, g, "starting a "+kind); err != nil {
		return nil, err
	}
	if err := checkTrackedClean(ctx, g); err != nil {
		return nil, err
	}
	label, err := integrateTargetPinned(ctx, g, req)
	if err != nil {
		return nil, err
	}
	if up, err := isAncestor(ctx, g, req.TargetOid, cur.oid); err != nil {
		return nil, err
	} else if up {
		return nil, failure("already_up_to_date", "HEAD already contains "+label)
	}
	if rebase {
		merges, replay, err := rebaseRange(ctx, g, req.TargetOid, cur.oid)
		switch {
		case err != nil:
			return nil, err
		case merges > 0:
			return nil, failure("range_has_merges", "the commits to rebase include merge commits; rebase them in a terminal")
		case replay != req.ExpectedReplayCount:
			return nil, failure("stale_range", strconv.Itoa(replay)+" commits would be replayed, not "+strconv.Itoa(req.ExpectedReplayCount)+"; refresh and review again")
		case replay > 0 && !req.AcknowledgePublished && leavesPublished(ctx, g, req.TargetOid, cur.oid):
			return nil, failure("published_commit", "commits to be rebased are already on a remote-tracking branch; rebasing rewrites published history")
		}
	}
	if err := checkWrittenPaths(ctx, g, cur.oid, req.TargetOid, rebase); err != nil {
		return nil, err
	}
	if err := checkIdentity(ctx, w); err != nil {
		return nil, err
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	if err := w.checkRefLocks("refs/heads/" + cur.branch); err != nil {
		return nil, err
	}
	v, err := gitVersion()
	if err != nil {
		return nil, failure("unavailable", "Git version could not be determined")
	}
	atLeast := func(major, minor int) bool { return v[0] > major || (v[0] == major && v[1] >= minor) }
	args := append([]string{}, operationArgs...)
	if rebase {
		args = append(args, "rebase", "--merge", "--no-rerere-autoupdate", "--no-fork-point", "--no-autosquash", "--no-autostash")
		if atLeast(2, 38) {
			args = append(args, "--no-update-refs")
		}
		args = append(args, req.TargetOid)
	} else {
		ff, err := isAncestor(ctx, g, cur.oid, req.TargetOid)
		if err != nil {
			return nil, err
		}
		if err := ffOnlyRefusal(mergeFFMode(ctx, g, cur.branch), ff); err != nil {
			return nil, err
		}
		// merge.ff and the branch's mergeOptions apply as in the CLI.
		args = append(args, "merge", "--no-overwrite-ignore", "--no-rerere-autoupdate", "--no-edit", "-m", mergeMessage(req, label))
		if atLeast(2, 27) {
			args = append(args, "--no-autostash")
		}
		args = append(args, req.TargetOid)
	}
	id := "op-" + c.ID
	subject := commitSubject(ctx, g, req.TargetOid)
	p := &gitPlan{}
	p.journal = func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		if res == nil {
			putGitOperation(s, protocol.GitOperationRecord{OperationID: id, Checkout: w.top, Kind: kind, State: protocol.GitOperationRunning,
				Branch: cur.branch, OrigHead: cur.oid, Target: protocol.GitOperationCommit{Oid: req.TargetOid, Label: label, Subject: subject},
				StartCommandID: c.ID, LastCommandID: c.ID, ThreadID: c.ThreadID, ProjectID: c.ProjectID, StartedAt: now, UpdatedAt: now})
			return nil
		}
		if res.Operation != nil {
			res.Operation.OperationID = id
		}
		rec := gitOperationFor(s, w.top)
		if rec == nil || rec.OperationID != id {
			return nil
		}
		if res.Operation != nil && res.Operation.Outcome == protocol.GitOutcomeNotStarted {
			removeGitOperation(s, id)
			if res.Operation.State != nil {
				annotateOperation(res.Operation.State, nil)
			}
			res.Operation.OperationID = ""
			return nil
		}
		applyOperationResult(rec, res, "", now)
		if res.Operation != nil && res.Operation.State != nil {
			annotateOperation(res.Operation.State, rec)
		}
		return nil
	}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: kind, HeadBefore: cur.oid}
		defer func() { res.Operation = op }()
		notStarted := func(code, message string) protocol.GitResult {
			op.Outcome = protocol.GitOutcomeNotStarted
			return gitResult(protocol.GitStateFailed, code, message+"; nothing was changed", nil)
		}
		if again, err := readHead(ctx, g); err != nil || checkHeadPins(again, req.ExpectedBranch, req.ExpectedHead) != nil {
			res = notStarted("stale_head", "HEAD moved after review")
			return res
		}
		if _, err := integrateTargetPinned(ctx, g, req); err != nil {
			res = notStarted("stale_target", "the target changed after review")
			return res
		}
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			op.Outcome, res = protocol.GitOutcomeNotStarted, *refused
			return res
		}
		// Saving open documents may have changed tracked files or created
		// files in the way; nothing may be stashed or overwritten.
		for _, check := range []func() error{
			func() error { return refuseOperation(ctx, g, "starting a "+kind) },
			func() error {
				if err := checkTrackedClean(ctx, g); err != nil {
					if pe, ok := err.(*protocol.Error); ok && pe.Code == "dirty_tree" {
						return failure("documents_changed_tree", "saving open documents changed tracked files, so the "+kind+" did not start; review, commit or discard those changes and start again")
					}
					return err
				}
				return nil
			},
			func() error { return checkWrittenPaths(ctx, g, cur.oid, req.TargetOid, rebase) },
		} {
			if err := check(); err != nil {
				pe, _ := err.(*protocol.Error)
				code, message := "unavailable", err.Error()
				if pe != nil {
					code, message = pe.Code, pe.Message
				}
				res = notStarted(code, message)
				return res
			}
		}
		before, err := snapshotRewrite(ctx, g, w.top, cur.oid, req.TargetOid)
		if err != nil {
			res = notStarted("unavailable", "the working tree could not be read")
			return res
		}
		pre := protocol.GitOperationState{HeadOid: cur.oid}
		run := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, args...)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, err := w.observe(vctx, g)
		if err != nil {
			op.Outcome, res = protocol.GitOutcomeUnknown, gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the result could not be read; refresh", run.output)
			return res
		}
		op.HeadAfter, res.Commit = after.HeadOid, after.HeadOid
		op.RerereResolved = rerereResolved(run.text())
		switch {
		case after.Kind == "" && after.HeadOid != cur.oid:
			op.Outcome = protocol.GitOutcomeCompleted
			verb := "Merged " + label + " into " + cur.branch
			if rebase {
				verb = "Rebased " + cur.branch + " onto " + label
			}
			res = gitResult(protocol.GitStateSucceeded, "", verb, run.output)
			if run.err != nil {
				res.Code, res.Message = "hook_failed", verb+", but Git reported a failure afterwards (a hook); review the result"
			}
		case after.Kind == "":
			// Nothing started: Git refused (would overwrite, a hook) or had
			// nothing to do.
			if before.changed(vctx, g) {
				op.Outcome = protocol.GitOutcomeUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "partial_change", "Git failed without starting the "+kind+" but files or the index changed; review status", run.output)
				wouldOverwrite(&res, run.text(), w.top)
				res.Code = "partial_change"
				break
			}
			op.Outcome = protocol.GitOutcomeNotStarted
			res = failedGit("git_failed", "Git did not start the "+kind+"; nothing was changed", run.output)
			if wouldOverwrite(&res, run.text(), w.top) {
				res.Message = "Git did not start the " + kind + ": these files would be overwritten; nothing was changed"
			} else if run.err == nil {
				res = gitResult(protocol.GitStateFailed, "already_up_to_date", "nothing to "+kind, run.output)
			}
		default:
			operationOutcome(&res, op, pre, after, run, "start the "+kind)
			if after.Kind != kind {
				op.Outcome = protocol.GitOutcomeUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "a different operation is now in progress; review it", run.output)
			}
			if op.Outcome == protocol.GitOutcomeStoppedConflicts {
				_ = w.ensureConflictCopy(vctx, p, after, op) // best effort; conflict commands retry
			}
			res.Commit = after.HeadOid
		}
		return res
	}
	return p, nil
}

func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

// checkDiscards refuses an abort or skip whose pins or acknowledgement do
// not match what it would reset.
func checkDiscards(st protocol.GitOperationState, req protocol.GitOperationWrite, discards []string, incomplete bool, verb string) error {
	switch {
	case incomplete:
		return failure("status_truncated", "too many changes to list what "+verb+" would reset; use a terminal")
	case req.WorktreeFingerprint != st.WorktreeFingerprint:
		return failure("stale_status", "the working tree changed since it was reviewed; refresh and review what "+verb+" would reset")
	case len(st.HiddenEntries) > 0:
		return failure("not_supported", "entries marked assume-unchanged or skip-worktree may hide local changes ("+listPaths(st.HiddenEntries)+"); use a terminal")
	case req.DiscardsFingerprint != "" && req.DiscardsFingerprint == pathsFingerprint(discards):
	case !slices.Equal(sortedCopy(req.AcknowledgeDiscard), discards):
		if len(discards) == 0 {
			return failure("stale_status", "nothing outside the operation would be reset any more; refresh and confirm again")
		}
		return failure("discards_unacknowledged", verb+" would also reset changes to "+listPaths(discards)+"; confirm resetting these files")
	}
	return nil
}

// checkPending refuses continuing or skipping while untracked files are in
// the way of the commits still to be applied, or those could not be checked.
func checkPending(st protocol.GitOperationState) error {
	switch {
	case len(st.NestedOnContinue) > 0:
		return failure("not_supported", nestedReason(st.NestedOnContinue))
	case len(st.ContinueUpdatesRefs) > 0:
		return failure("not_supported", "the rebase would also move "+listPaths(st.ContinueUpdatesRefs)+" (--update-refs); the application never moves other branches, so continue it in a terminal")
	case len(st.ContinueInTheWay) > 0:
		return failure("would_overwrite", "untracked or ignored files are in the way of the remaining commits: "+listPaths(st.ContinueInTheWay)+"; move them first")
	case st.ContinueUnchecked:
		return failure("not_supported", "the remaining commits could not be checked for files in the way; continue in a terminal")
	}
	return nil
}

// gitBackupsPerCheckout bounds Snapshot.GitBackups per repository.
const gitBackupsPerCheckout = 20

// gitBackupsMax bounds Snapshot.GitBackups overall.
const gitBackupsMax = 200

// checkoutKey is a byte-safe key for a toplevel (JSON would replace bytes
// that are not valid UTF-8 in the path itself).
func checkoutKey(top string) string { return base64.StdEncoding.EncodeToString([]byte(top)) }

// putGitBackup records a backup, keeping the newest per repository and
// overall.
func putGitBackup(s *protocol.Snapshot, b protocol.GitBackupEntry) {
	s.GitBackups = append(s.GitBackups, b)
	n := 0
	for i := len(s.GitBackups) - 1; i >= 0; i-- {
		if s.GitBackups[i].CheckoutKey != b.CheckoutKey {
			continue
		}
		if n++; n > gitBackupsPerCheckout {
			s.GitBackups = append(s.GitBackups[:i], s.GitBackups[i+1:]...)
		}
	}
	if extra := len(s.GitBackups) - gitBackupsMax; extra > 0 {
		s.GitBackups = s.GitBackups[extra:]
	}
}

// pruneGitBackups drops backups of repositories that no project or thread
// checkout overlaps any more.
func pruneGitBackups(s *protocol.Snapshot) {
	kept := s.GitBackups[:0]
	for _, b := range s.GitBackups {
		used := false
		for _, p := range s.Projects {
			used = used || pathsOverlap(p.Path, b.Checkout)
		}
		for _, t := range s.Threads {
			used = used || pathsOverlap(t.Checkout, b.Checkout)
		}
		if used {
			kept = append(kept, b)
		}
	}
	s.GitBackups = kept
	if len(s.GitBackups) == 0 {
		s.GitBackups = nil
	}
}

// leftoverPaths lists paths among written whose index or working tree
// still differs from HEAD after an abort that reported success.
func leftoverPaths(ctx context.Context, g *gitReader, written []string) []string {
	if len(written) == 0 {
		return nil
	}
	var left []string
	for len(written) > 0 {
		n := min(len(written), 500)
		out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(append(statusArgs("no"), "--"), written[:n]...)...)
		if err != nil || truncated {
			return append(left, written...)
		}
		var st protocol.GitStatus
		parseGitStatus(out, false, 0, &st)
		for _, e := range st.Entries {
			left = append(left, e.Path)
		}
		written = written[n:]
	}
	return sortedUnique(left)
}

// backupRecordedLocked reports whether oid is a recorded backup (or its
// index commit) of the repository at top.
func (e *engine) backupRecordedLocked(top, oid string) bool {
	for _, b := range e.snap.GitBackups {
		if b.CheckoutKey == checkoutKey(top) && (b.Oid == oid || b.IndexOid == oid) {
			return true
		}
	}
	return false
}

// describeOperationAfter says what an interrupted operation command left.
func describeOperationAfter(op *protocol.GitOperationResult) string {
	switch {
	case op == nil:
		return "refresh the operation to see what changed"
	case op.State == nil && op.HeadAfter != "":
		return "no operation is in progress now and HEAD is at " + shortOid(op.HeadAfter) + "; review the branch"
	case op.State == nil:
		return "refresh the operation to see what changed"
	}
	st := op.State
	msg := article(st.Kind) + " is still in progress"
	if st.Steps > 0 {
		msg += fmt.Sprintf(" at %d/%d", st.Step, st.Steps)
	}
	if n := len(st.Conflicts); n > 0 {
		msg += fmt.Sprintf(" with %d conflicted paths", n)
	}
	return msg + " and HEAD is at " + shortOid(st.HeadOid) + "; review it before continuing"
}

func nestedReason(paths []string) string {
	return "a nested repository or untracked directory is in the way at " + listPaths(paths) + "; resolve it in a terminal"
}

// restoreRemoved puts back, byte-exact with their permission bits, the
// untracked files an abort or skip moved aside that are still absent (Git
// did not write anything there). It returns a note for the result message.
func restoreRemoved(ctx context.Context, g *gitReader, w *gitWriter, backup *protocol.GitOperationBackup, tokens map[string]backedUp, removed []string) string {
	var restored, failed []string
	for _, p := range removed {
		tok := worktreeStat(w.top, p)
		if tokenKind(tok) != tokenAbsent {
			continue // Git wrote the path; the moved file stays in the backup
		}
		mode, oid, ok := copyEntry(ctx, g, backup.Oid, p)
		data, truncated, err := g.read(ctx, gitBackupBytesMax, "cat-file", "blob", oid)
		if !ok || err != nil || truncated || writeWorktreeAtomic(w, p, tok, true, mode, tokens[p].perm, data) != nil {
			failed = append(failed, p)
			continue
		}
		restored = append(restored, p)
	}
	note := ""
	if len(restored) > 0 {
		note += "; the untracked files moved aside were put back: " + listPaths(restored)
	}
	if len(failed) > 0 {
		note += "; these untracked files could not be put back and remain in backup " + shortOid(backup.Oid) + ": " + listPaths(failed)
	}
	return note
}
