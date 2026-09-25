package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// gitTry runs Git like gitIn but tolerates failure (a merge or rebase that
// stops with conflicts exits non-zero).
func gitTry(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "core.editor=:"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_EDITOR=:")
	_ = cmd.Run()
}

// conflictSetup is gitWriteSetup with c.txt changed on both main and other,
// and other carrying a second, clean commit (d.txt).
func conflictSetup(t *testing.T) (*engine, string, func(...string) string) {
	t.Helper()
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other c")
	writeFile(t, root, "d.txt", "d\n")
	git("add", "d.txt")
	git("commit", "-q", "-m", "other d")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main c")
	return e, root, git
}

// operationOf reads the operation as GET /v1/git/operation does.
func operationOf(t *testing.T, e *engine, root string) protocol.GitOperationState {
	t.Helper()
	st, err := e.operationState(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func previewOf(t *testing.T, root, kind, target string) protocol.GitIntegratePreview {
	t.Helper()
	p, err := readIntegratePreview(context.Background(), root, kind, target)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func recordOf(e *engine) *protocol.GitOperationRecord {
	s := e.current()
	if len(s.GitOperations) != 1 {
		return nil
	}
	return &s.GitOperations[0]
}

func TestGitOperationStateOfExternalOperations(t *testing.T) {
	e, root, git := conflictSetup(t)
	mainTip, otherTip := git("rev-parse", "main"), git("rev-parse", "other")
	otherC := git("rev-parse", "other~1")

	gitTry(t, root, "merge", "other")
	st := operationOf(t, e, root)
	if st.Kind != "merge" || st.Source != protocol.GitOperationSourceExternal || st.OperationID != "" || st.Branch != "main" || st.HeadOid != mainTip {
		t.Fatalf("merge state: %+v", st)
	}
	if st.Target == nil || st.Target.Oid != otherTip || st.Target.Label != "other" || st.Target.Subject != "other d" {
		t.Fatalf("merge target: %+v", st.Target)
	}
	if st.Sides.Ours != "HEAD · main" || st.Sides.Theirs != "other "+otherTip[:12] || st.Sides.Base != "common ancestor" {
		t.Fatalf("merge sides: %+v", st.Sides)
	}
	if len(st.Conflicts) != 1 || st.Conflicts[0].Path != "c.txt" || st.Conflicts[0].Kind != "UU" || st.UnmergedFingerprint == "" {
		t.Fatalf("merge conflicts: %+v", st.Conflicts)
	}
	for i, s := range st.Conflicts[0].Stages {
		if !s.Present || s.Mode != "100644" || !gitFullHash.MatchString(s.Oid) || s.Label == "" {
			t.Fatalf("stage %d: %+v", i+1, s)
		}
	}
	if st.Can.Continue.Allowed || st.Can.Skip.Allowed || !st.Can.Abort.Allowed || st.Can.Continue.Reason == "" || st.Can.Skip.Reason == "" {
		t.Fatalf("merge actions: %+v", st.Can)
	}
	status := mustStatus(t, root)
	if c := hasEntry(status, "c.txt", "conflicted"); c == nil || len(c.Stages) != 3 || c.Stages[1].Oid != st.Conflicts[0].Stages[1].Oid || c.Pin == "" || c.WorktreeStat == "" {
		t.Fatalf("status conflicted entry: %+v", c)
	}
	// An external operation is aborted with the same pins.
	r := mustGit(t, e, client.GitOperationAbortCommand("abort-merge", gitTarget, st, false, false), protocol.GitStateSucceeded)
	if r.Git.Operation == nil || r.Git.Operation.Outcome != protocol.GitOutcomeAborted || git("rev-parse", "HEAD") != mainTip || operationOf(t, e, root).Kind != "" {
		t.Fatalf("abort: %+v", r.Git)
	}

	git("checkout", "-q", "other")
	gitTry(t, root, "rebase", "main")
	st = operationOf(t, e, root)
	if st.Kind != "rebase" || st.Branch != "other" || st.Step != 1 || st.Steps != 2 || st.OrigHead != otherTip || st.Target == nil || st.Target.Oid != mainTip || st.Target.Label != "main" {
		t.Fatalf("rebase state: %+v", st)
	}
	if st.Current == nil || st.Current.Oid != otherC || st.Current.Subject != "other c" {
		t.Fatalf("rebase current: %+v", st.Current)
	}
	if st.Sides.Ours != "main + rebased so far (HEAD "+mainTip[:12]+")" || st.Sides.Theirs != otherC[:12]+" other c from other" || st.Sides.Base != "parent of "+otherC[:12] {
		t.Fatalf("rebase sides: %+v", st.Sides)
	}
	if !st.Can.Skip.Allowed || st.Can.Continue.Allowed {
		t.Fatalf("rebase actions: %+v", st.Can)
	}
	git("rebase", "--abort")

	git("checkout", "-q", "main")
	gitTry(t, root, "cherry-pick", otherC)
	st = operationOf(t, e, root)
	if st.Kind != "cherry-pick" || st.Current == nil || st.Current.Oid != otherC || st.Sides.Theirs != otherC[:12]+" other c" || st.Can.Skip.Allowed {
		t.Fatalf("cherry-pick state: %+v", st)
	}
	git("cherry-pick", "--abort")

	writeFile(t, root, "c.txt", "main again\n")
	git("commit", "-q", "-am", "main again")
	gitTry(t, root, "revert", "--no-edit", mainTip)
	st = operationOf(t, e, root)
	if st.Kind != "revert" || st.Current == nil || st.Current.Oid != mainTip || !strings.HasPrefix(st.Sides.Theirs, "parent of "+mainTip[:12]) {
		t.Fatalf("revert state: %+v", st)
	}
	git("revert", "--abort")
	if st := operationOf(t, e, root); st.Kind != "" || st.Can.Abort.Allowed || len(st.Conflicts) != 0 {
		t.Fatalf("idle state: %+v", st)
	}
}

func TestGitOperationConflictShapes(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	for _, f := range []string{"uu.txt", "du.txt", "ud.txt"} {
		writeFile(t, root, f, f+" base\n")
	}
	writeFile(t, root, "bin.dat", "base\x00bin")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("update-index", "--add", "--cacheinfo", "160000,"+base+",sub")
	git("commit", "-q", "-m", "gitlink")
	gitlink := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "uu.txt", "other\n")
	writeFile(t, root, "du.txt", "other\n")
	git("rm", "-q", "ud.txt")
	writeFile(t, root, "aa.txt", "other\n")
	writeFile(t, root, "bin.dat", "other\x00bin")
	git("add", ".")
	git("update-index", "--cacheinfo", "160000,"+gitlink+",sub")
	git("commit", "-q", "-m", "other")
	otherTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	writeFile(t, root, "uu.txt", "main\n")
	git("rm", "-q", "du.txt")
	writeFile(t, root, "ud.txt", "main\n")
	writeFile(t, root, "aa.txt", "main\n")
	writeFile(t, root, "bin.dat", "main\x00bin")
	git("add", ".")
	git("update-index", "--cacheinfo", "160000,"+otherTip+",sub")
	git("commit", "-q", "-m", "main")
	gitTry(t, root, "merge", "other")
	st := operationOf(t, e, root)
	kinds := map[string]protocol.GitConflict{}
	for _, c := range st.Conflicts {
		kinds[c.Path] = c
	}
	for path, want := range map[string]string{"uu.txt": "UU", "aa.txt": "AA", "du.txt": "DU", "ud.txt": "UD", "bin.dat": "UU", "sub": "UU"} {
		if kinds[path].Kind != want {
			t.Errorf("%s: kind %q, want %s (%+v)", path, kinds[path].Kind, want, kinds[path])
		}
	}
	if c := kinds["du.txt"]; c.Stages[1].Present || !c.Stages[0].Present || !c.Stages[2].Present {
		t.Errorf("deleted by us stages: %+v", c.Stages)
	}
	if c := kinds["aa.txt"]; c.Stages[0].Present {
		t.Errorf("both added has a base: %+v", c.Stages)
	}
	if !kinds["bin.dat"].Binary || kinds["uu.txt"].Binary {
		t.Errorf("binary detection: %+v %+v", kinds["bin.dat"], kinds["uu.txt"])
	}
	if !kinds["sub"].Submodule || kinds["sub"].Binary {
		t.Errorf("submodule: %+v", kinds["sub"])
	}
	status := mustStatus(t, root)
	if c := hasEntry(status, "du.txt", "conflicted"); c == nil || len(c.Stages) != 3 || c.Stages[1].Present || c.Index != "D" || c.Worktree != "U" {
		t.Errorf("status stages: %+v", c)
	}
}

func TestGitMergeStartContinueAndRecord(t *testing.T) {
	e, root, git := conflictSetup(t)
	// A fast forward completes without a merge commit.
	git("checkout", "-q", "-b", "behind", "main~1")
	ff := previewOf(t, root, "merge", "refs/heads/main")
	if !ff.FastForward || ff.UpToDate || ff.Source != protocol.GitIntegrateBranch || ff.TargetLabel != "main" || ff.Blocked != "" {
		t.Fatalf("ff preview: %+v", ff)
	}
	r := mustGit(t, e, client.GitMergeCommand("ff", gitTarget, mustStatus(t, root), ff), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("rev-parse", "HEAD") != git("rev-parse", "main") {
		t.Fatalf("ff merge: %+v %+v", r.Git, r.Git.Operation)
	}
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationCompleted || rec.OperationID != "op-ff" || rec.EndedAt == "" {
		t.Fatalf("ff record: %+v", rec)
	}
	git("checkout", "-q", "main")
	git("branch", "-D", "behind")

	p := previewOf(t, root, "merge", "refs/heads/other")
	if p.FastForward || p.UpToDate || p.MergeBase == "" {
		t.Fatalf("merge preview: %+v", p)
	}
	cmd := client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p)
	r = mustGit(t, e, cmd, protocol.GitStateSucceeded)
	op := r.Git.Operation
	if r.Git.Code != "stopped_conflicts" || op.Outcome != protocol.GitOutcomeStoppedConflicts || op.OperationID != "op-merge" || op.State == nil || op.State.Source != protocol.GitOperationSourceApp || len(op.State.Conflicts) != 1 {
		t.Fatalf("conflicting merge: %+v %+v", r.Git, op)
	}
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationStoppedConflicts || rec.Kind != "merge" || rec.Target.Label != "other" || rec.StartCommandID != "merge" {
		t.Fatalf("record: %+v", rec)
	}
	st := operationOf(t, e, root)
	if st.Source != protocol.GitOperationSourceApp || st.OperationID != "op-merge" || st.Record == nil {
		t.Fatalf("app state: %+v", st)
	}
	_, err := e.command(client.GitOperationContinueCommand("early", gitTarget, st, false))
	wantGitCode(t, err, "conflicted")
	notRecorded(t, e, client.GitOperationContinueCommand("early", gitTarget, st, false))
	// A second start is refused while the merge is in progress.
	_, err = e.command(client.GitMergeCommand("again", gitTarget, mustStatus(t, root), p))
	wantGitCode(t, err, "operation_in_progress")

	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	stale := st
	st = operationOf(t, e, root)
	if !st.Can.Continue.Allowed || st.UnmergedFingerprint != "" {
		t.Fatalf("resolved state: %+v", st.Can)
	}
	if rec := recordOf(e); rec.State != protocol.GitOperationReady {
		t.Fatalf("record not reconciled: %+v", rec)
	}
	_, err = e.command(client.GitOperationContinueCommand("stale", gitTarget, stale, false))
	wantGitCode(t, err, "stale_status")
	cont := client.GitOperationContinueCommand("continue", gitTarget, st, false)
	r = mustGit(t, e, cont, protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || r.Git.Operation.OperationID != "op-merge" {
		t.Fatalf("continue: %+v %+v", r.Git, r.Git.Operation)
	}
	head := git("rev-parse", "HEAD")
	if msg := git("log", "-1", "--format=%s"); msg != "Merge branch 'other'" || git("rev-parse", "HEAD^2") != git("rev-parse", "other") {
		t.Fatalf("merge commit: %q", msg)
	}
	if rec := recordOf(e); rec.State != protocol.GitOperationCompleted || rec.LastCommandID != "continue" {
		t.Fatalf("completed record: %+v", rec)
	}
	// A retry returns the recorded receipt without running Git again.
	again, err := e.command(cont)
	if err != nil || again.Git == nil || again.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("rev-parse", "HEAD") != head {
		t.Fatalf("retry: %+v %v", again, err)
	}
	dup, err := e.command(cmd)
	if err != nil || dup.Git.Code != "stopped_conflicts" || git("rev-parse", "HEAD") != head {
		t.Fatalf("start retry: %+v %v", dup, err)
	}
}

func TestGitRebaseStopContinueSkip(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "e.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	for _, step := range []struct{ file, text, msg string }{{"c.txt", "feature\n", "f1"}, {"d.txt", "d\n", "f2"}, {"e.txt", "feature\n", "f3"}} {
		writeFile(t, root, step.file, step.text)
		git("add", ".")
		git("commit", "-q", "-m", step.msg)
	}
	f3 := git("rev-parse", "HEAD")
	f1 := git("rev-parse", "HEAD~2")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	writeFile(t, root, "e.txt", "main\n")
	git("commit", "-q", "-am", "main")
	mainTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "feature")

	p := previewOf(t, root, "rebase", "refs/heads/main")
	if p.ReplayCount != 3 || p.RangeHasMerges || p.Published || p.Blocked != "" {
		t.Fatalf("rebase preview: %+v", p)
	}
	r := mustGit(t, e, client.GitRebaseCommand("rebase", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	st := r.Git.Operation.State
	if r.Git.Code != "stopped_conflicts" || st == nil || st.Step != 1 || st.Steps != 3 || st.Current.Oid != f1 || st.Source != protocol.GitOperationSourceApp {
		t.Fatalf("rebase stop: %+v %+v", r.Git, st)
	}
	if !strings.HasPrefix(st.Sides.Ours, "main + rebased so far") || !strings.HasSuffix(st.Sides.Theirs, "f1 from feature") {
		t.Fatalf("rebase sides: %+v", st.Sides)
	}
	writeFile(t, root, "c.txt", "merged\n")
	git("add", "c.txt")
	st2 := operationOf(t, e, root)
	r = mustGit(t, e, client.GitOperationContinueCommand("cont", gitTarget, st2, false), protocol.GitStateSucceeded)
	st = r.Git.Operation.State
	if r.Git.Code != "stopped_conflicts" || st == nil || st.Step != 3 || st.Current.Oid != f3 || len(st.Conflicts) != 1 || st.Conflicts[0].Path != "e.txt" {
		t.Fatalf("second stop: %+v %+v", r.Git, st)
	}
	if rec := recordOf(e); rec.State != protocol.GitOperationStoppedConflicts || rec.LastCommandID != "cont" {
		t.Fatalf("record at second stop: %+v", rec)
	}
	// Skip names the dropped commit; another commit is stale.
	wrong := *st
	wrong.Current = &protocol.GitOperationCommit{Oid: f1}
	bad, _ := client.GitOperationSkipCommand("skip-wrong", gitTarget, wrong, false)
	_, err := e.command(bad)
	wantGitCode(t, err, "stale_operation")
	skip, ok := client.GitOperationSkipCommand("skip", gitTarget, *st, false)
	if !ok {
		t.Fatal("skip not offered")
	}
	r = mustGit(t, e, skip, protocol.GitStateSucceeded)
	if op := r.Git.Operation; op.Outcome != protocol.GitOutcomeCompleted || op.Skipped == nil || op.Skipped.Oid != f3 {
		t.Fatalf("skip: %+v %+v", r.Git, op)
	}
	if n := git("rev-list", "--count", mainTip+"..feature"); n != "2" || readText(t, root, "e.txt") != "main\n" || git("branch", "--show-current") != "feature" {
		t.Fatalf("rebased branch: %s commits, e.txt %q", n, readText(t, root, "e.txt"))
	}
	if rec := recordOf(e); rec.State != protocol.GitOperationCompleted {
		t.Fatalf("record after skip: %+v", rec)
	}
}

func TestGitContinueWithNothingToCommit(t *testing.T) {
	e, root, git := conflictSetup(t)
	gitTry(t, root, "cherry-pick", git("rev-parse", "other~1"))
	writeFile(t, root, "c.txt", "main\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitOperationContinueCommand("empty", gitTarget, st, false), protocol.GitStateFailed)
	if r.Git.Code != "nothing_to_commit" || r.Git.Operation.Outcome != protocol.GitOutcomeUnchanged || operationOf(t, e, root).Kind != "cherry-pick" {
		t.Fatalf("empty continue: %+v %+v", r.Git, r.Git.Operation)
	}
	mustGit(t, e, client.GitOperationAbortCommand("abort", gitTarget, operationOf(t, e, root), false, false), protocol.GitStateSucceeded)
}

func TestGitIntegrateRefusals(t *testing.T) {
	e, root, git := conflictSetup(t)
	git("config", "rebase.autoStash", "true")
	git("config", "merge.autoStash", "true")
	mergeP := previewOf(t, root, "merge", "refs/heads/other")
	git("checkout", "-q", "other")
	rebaseP := previewOf(t, root, "rebase", "refs/heads/main")
	git("checkout", "-q", "main")

	try := func(id string, cmd protocol.Command, code string) {
		t.Helper()
		cmd.ID = id
		_, err := e.command(cmd)
		wantGitCode(t, err, code)
		notRecorded(t, e, cmd)
	}
	// Tracked changes are refused whatever autostash says; nothing is stashed.
	writeFile(t, root, "c.txt", "dirty\n")
	if p := previewOf(t, root, "merge", "refs/heads/other"); p.Blocked != "dirty_tree" {
		t.Fatalf("dirty preview: %+v", p)
	}
	try("dirty-merge", client.GitMergeCommand("", gitTarget, mustStatus(t, root), mergeP), "dirty_tree")
	git("add", "c.txt")
	try("staged-merge", client.GitMergeCommand("", gitTarget, mustStatus(t, root), mergeP), "dirty_tree")
	if out := git("stash", "list"); out != "" || readText(t, root, "c.txt") != "dirty\n" {
		t.Fatalf("stashed: %q", out)
	}
	git("reset", "-q", "--hard")
	writeFile(t, root, "untracked.txt", "fine\n")

	st := mustStatus(t, root)
	stale := st
	stale.HeadOid = git("rev-parse", "other")
	try("stale-head", client.GitMergeCommand("", gitTarget, stale, mergeP), "stale_head")
	up := previewOf(t, root, "merge", "refs/heads/main")
	if !up.UpToDate || up.Blocked != "already_up_to_date" {
		t.Fatalf("up to date preview: %+v", up)
	}
	try("uptodate", client.GitMergeCommand("", gitTarget, st, up), "already_up_to_date")
	commitTarget := mergeP
	commitTarget.Source, commitTarget.TargetRef = protocol.GitIntegrateCommit, ""
	commitTarget.TargetOid = strings.Repeat("0", 40)
	try("unknown", client.GitMergeCommand("", gitTarget, st, commitTarget), "unknown_commit")
	upstream := mergeP
	upstream.Source = protocol.GitIntegrateUpstream
	try("no-upstream", client.GitMergeCommand("", gitTarget, st, upstream), "no_upstream")

	// The target moved after the preview.
	git("checkout", "-q", "other")
	writeFile(t, root, "moved.txt", "m\n")
	git("add", "moved.txt")
	git("commit", "-q", "-m", "moved")
	stOther := mustStatus(t, root)
	git("checkout", "-q", "main")
	try("stale-target", client.GitMergeCommand("", gitTarget, mustStatus(t, root), mergeP), "stale_target")
	git("checkout", "-q", "other")
	try("stale-range", client.GitRebaseCommand("", gitTarget, stOther, rebaseP, false), "stale_range")

	// Published commits need acknowledgement.
	git("update-ref", "refs/remotes/origin/other", "HEAD")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	if !p.Published || p.ReplayCount != 3 {
		t.Fatalf("published preview: %+v", p)
	}
	try("published", client.GitRebaseCommand("", gitTarget, stOther, p, false), "published_commit")
	r := mustGit(t, e, client.GitRebaseCommand("published-ack", gitTarget, stOther, p, true), protocol.GitStateSucceeded)
	if r.Git.Code != "stopped_conflicts" || readText(t, root, "untracked.txt") != "fine\n" {
		t.Fatalf("acknowledged rebase: %+v", r.Git)
	}
	mustGit(t, e, client.GitOperationAbortCommand("published-abort", gitTarget, operationOf(t, e, root), false, false), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != stOther.HeadOid || git("branch", "--show-current") != "other" {
		t.Fatal("abort did not restore the branch")
	}

	// A merge commit in the range refuses the rebase.
	git("checkout", "-q", "-b", "merged", "main~1")
	writeFile(t, root, "m.txt", "m\n")
	git("add", "m.txt")
	git("commit", "-q", "-m", "side")
	git("merge", "-q", "--no-ff", "-m", "merge other", "other")
	mp := previewOf(t, root, "rebase", "refs/heads/main")
	if !mp.RangeHasMerges || mp.Blocked != "range_has_merges" {
		t.Fatalf("merges preview: %+v", mp)
	}
	try("merges", client.GitRebaseCommand("", gitTarget, mustStatus(t, root), mp, true), "range_has_merges")

	// Detached and unborn.
	git("checkout", "-q", "--detach", "main")
	det := previewOf(t, root, "merge", "refs/heads/other")
	if det.Blocked != "detached" {
		t.Fatalf("detached preview: %+v", det)
	}
	try("detached", client.GitMergeCommand("", gitTarget, mustStatus(t, root), det), "detached")
	git("checkout", "-q", "main")

	// An operation in progress refuses a start.
	gitTry(t, root, "merge", "other")
	try("in-progress", client.GitMergeCommand("", gitTarget, mustStatus(t, root), previewOf(t, root, "merge", "refs/heads/merged")), "operation_in_progress")
	git("merge", "--abort")

	// Shape checks.
	bad := client.GitMergeCommand("bad", gitTarget, mustStatus(t, root), mergeP)
	bad.Git.Integrate.AcknowledgePublished = true
	try("bad", bad, "invalid")
	abort := client.GitOperationAbortCommand("noconfirm", gitTarget, protocol.GitOperationState{Kind: "merge", HeadOid: git("rev-parse", "HEAD"), WorktreeFingerprint: "reviewed"}, false, false)
	abort.Git.Operation.Confirmed = false
	try("noconfirm", abort, "confirmation_required")
	_, err := e.command(client.GitOperationAbortCommand("none", gitTarget, protocol.GitOperationState{Kind: "merge", HeadOid: git("rev-parse", "HEAD"), WorktreeFingerprint: "reviewed"}, false, false))
	wantGitCode(t, err, "no_operation")
	skipMerge := client.GitOperationAbortCommand("skipmerge", gitTarget, protocol.GitOperationState{Kind: "merge", HeadOid: git("rev-parse", "HEAD"), WorktreeFingerprint: "reviewed"}, false, false)
	skipMerge.Kind, skipMerge.Git.Operation.SkipOid = protocol.GitKindOperationSkip, git("rev-parse", "HEAD")
	try("skipmerge", skipMerge, "invalid")

	unborn, _ := gitFixture(t)
	gitIn(t, unborn, "init", "-q")
	addGitThread(e, "p-unborn", "t-unborn", unborn)
	u := protocol.GitIntegrate{Source: protocol.GitIntegrateCommit, TargetOid: strings.Repeat("1", 40), ExpectedBranch: "main", ExpectedHead: strings.Repeat("2", 40)}
	_, err = e.command(protocol.Command{Version: 1, ID: "unborn", Kind: protocol.GitKindMerge, ProjectID: "p-unborn", Git: &protocol.GitWrite{Integrate: &u}})
	wantGitCode(t, err, "unborn")
}

func TestGitIntegrateNeverOverwritesUntrackedOrIgnoredFiles(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, ".gitignore", "*.log\n")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "build.log", "tracked on other\n")
	git("add", "-f", "build.log")
	git("commit", "-q", "-m", "add log")
	// A replayed commit adds and a later one removes x.log.
	git("checkout", "-q", "-b", "feature", "main")
	writeFile(t, root, "x.log", "replayed\n")
	git("add", "-f", "x.log")
	git("commit", "-q", "-m", "add x")
	git("rm", "-q", "x.log")
	git("commit", "-q", "-m", "drop x")
	git("checkout", "-q", "main")
	writeFile(t, root, "main.txt", "m\n")
	git("add", "main.txt")
	git("commit", "-q", "-m", "main")

	writeFile(t, root, "build.log", "precious ignored\n")
	p := previewOf(t, root, "merge", "refs/heads/other")
	_, err := e.command(client.GitMergeCommand("merge-ignored", gitTarget, mustStatus(t, root), p))
	wantGitCode(t, err, "would_overwrite")
	if !strings.Contains(err.Error(), "build.log") || readText(t, root, "build.log") != "precious ignored\n" {
		t.Fatalf("ignored file: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "build.log")); err != nil {
		t.Fatal(err)
	}
	git("checkout", "-q", "feature")
	writeFile(t, root, "x.log", "precious too\n")
	rp := previewOf(t, root, "rebase", "refs/heads/main")
	_, err = e.command(client.GitRebaseCommand("rebase-ignored", gitTarget, mustStatus(t, root), rp, false))
	wantGitCode(t, err, "would_overwrite")
	if readText(t, root, "x.log") != "precious too\n" || operationOf(t, e, root).Kind != "" {
		t.Fatalf("replayed ignored file overwritten")
	}
}

func TestGitRebaseIgnoresUpdateRefsAndRerereAutoUpdate(t *testing.T) {
	e, root, git := conflictSetup(t)
	git("config", "rebase.updateRefs", "true")
	git("config", "rerere.enabled", "true")
	git("config", "rerere.autoUpdate", "true")
	// A stacked branch in the rebased range stays where it is.
	git("checkout", "-q", "-b", "feature", "main~1")
	writeFile(t, root, "f1.txt", "1\n")
	git("add", ".")
	git("commit", "-q", "-m", "f1")
	stack := git("rev-parse", "HEAD")
	git("branch", "stack")
	writeFile(t, root, "f2.txt", "2\n")
	git("add", ".")
	git("commit", "-q", "-m", "f2")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	r := mustGit(t, e, client.GitRebaseCommand("stacked", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("rev-parse", "stack") != stack || git("rev-parse", "HEAD~2") != git("rev-parse", "main") {
		t.Fatalf("update-refs: %+v, stack %s", r.Git, git("rev-parse", "stack"))
	}

	// rerere replays a recorded resolution but never stages it.
	git("checkout", "-q", "main")
	gitTry(t, root, "merge", "other")
	writeFile(t, root, "c.txt", "recorded resolution\n")
	git("add", "c.txt")
	git("commit", "-q", "--no-edit")
	git("reset", "-q", "--hard", "HEAD~1")
	mp := previewOf(t, root, "merge", "refs/heads/other")
	r = mustGit(t, e, client.GitMergeCommand("rerere", gitTarget, mustStatus(t, root), mp), protocol.GitStateSucceeded)
	if r.Git.Code != "stopped_conflicts" || !slices.Equal(r.Git.Operation.RerereResolved, []string{"c.txt"}) || readText(t, root, "c.txt") != "recorded resolution\n" {
		t.Fatalf("rerere: %+v %+v", r.Git, r.Git.Operation)
	}
	if c := hasEntry(mustStatus(t, root), "c.txt", "conflicted"); c == nil {
		t.Fatal("rerere resolution was staged")
	}
}

func TestGitOperationReservesTheCheckout(t *testing.T) {
	e, root, git := conflictSetup(t)
	addGitThread(e, "p-sub", "t-sub", filepath.Join(root, "sub"))
	gitTry(t, root, "merge", "other")
	sendTo(t, e, "t-git", "during-merge")
	sendTo(t, e, "t-sub", "nested-during-merge")
	s := e.current()
	for _, id := range []string{"t-git", "t-sub"} {
		if th := threadOf(s, id); th.State != "idle" || th.WriterWait == nil || th.WriterWait.HolderOperation != "merge" || th.WriterWait.HolderThreadID != "" {
			t.Fatalf("%s started during a merge: %s %+v", id, th.State, th.WriterWait)
		}
	}
	// A reserved resolution job is exempt; bisect reserves nothing.
	e.mu.Lock()
	e.snap.GitOperations = []protocol.GitOperationRecord{{OperationID: "op-x", Checkout: root, Kind: "merge", State: protocol.GitOperationStoppedConflicts,
		Target: protocol.GitOperationCommit{Oid: git("rev-parse", "other")}, JobThreadID: "t-job"}}
	job, other := e.operationHolderLocked(&e.snap, root, "t-job"), e.operationHolderLocked(&e.snap, root, "t-git")
	e.snap.GitOperations = nil
	e.mu.Unlock()
	if job != "" || other != gitOpHolderPrefix+"merge:op-x" {
		t.Fatalf("job exemption: %q %q", job, other)
	}
	st := operationOf(t, e, root)
	mustGit(t, e, client.GitOperationAbortCommand("abort", gitTarget, st, false, false), protocol.GitStateSucceeded)
	s = e.current()
	if th := threadOf(s, "t-git"); th.State != "running" || th.WriterWait != nil {
		t.Fatalf("waiter after abort: %s %+v", th.State, th.WriterWait)
	}
	git("bisect", "start")
	e.mu.Lock()
	bisect := e.operationHolderLocked(&e.snap, root, "t-sub")
	e.mu.Unlock()
	if bisect != "" {
		t.Fatalf("bisect reserved the checkout: %q", bisect)
	}
}

func TestGitOperationReservesACPTurnsUntilItEnds(t *testing.T) {
	_, _ = gitFixture(t) // isolated Git configuration
	e, _, checkout := acpEngine(t)
	git := func(args ...string) string { return gitIn(t, checkout, args...) }
	git("init", "-q")
	writeFile(t, checkout, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, checkout, "c.txt", "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, checkout, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	git("checkout", "-q", "other")
	gitTry(t, checkout, "rebase", "main")
	a := startACPThread(t, e, "a", "say pong")
	s := waitFor(t, e, "operation wait", func(s protocol.Snapshot) bool { return threadOf(s, a).WriterWait != nil })
	if w := threadOf(s, a); w.State != "idle" || w.WriterWait.HolderOperation != "rebase" {
		t.Fatalf("ACP turn during a rebase: %s %+v", w.State, w.WriterWait)
	}
	// Ending the rebase in a terminal releases the waiter.
	git("rebase", "--abort")
	if done := waitTurn(t, e, a, "prompt-a"); done.WriterWait != nil {
		t.Fatalf("waiter after external abort: %+v", done.WriterWait)
	}
}

func TestGitOperationRecoveryAfterCrash(t *testing.T) {
	root, git := gitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	writeFile(t, root, "c.txt", "feature\n")
	git("commit", "-q", "-am", "feature")
	featureTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	mainTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "feature")
	// The server died after Git stopped the rebase but before phase two.
	gitTry(t, root, "rebase", "main")
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	snap := fixture.Initial()
	snap.Projects = append(snap.Projects, protocol.Project{ID: "p-git", Name: "repo", Path: root, Revision: 1})
	cmd := protocol.Command{Version: 1, ID: "crashed", Kind: protocol.GitKindRebase, ProjectID: "p-git", Git: &protocol.GitWrite{Integrate: &protocol.GitIntegrate{
		Source: protocol.GitIntegrateBranch, TargetRef: "refs/heads/main", TargetOid: mainTip, ExpectedBranch: "feature", ExpectedHead: featureTip, ExpectedReplayCount: 1}}}
	putGitOp(&snap, protocol.GitOp{Checkout: root, CommandID: cmd.ID, Op: "rebase", State: protocol.GitStateRunning, ProjectID: "p-git", StartedAt: "now"})
	putGitOperation(&snap, protocol.GitOperationRecord{OperationID: "op-crashed", Checkout: root, Kind: "rebase", State: protocol.GitOperationRunning, Branch: "feature",
		OrigHead: featureTip, Target: protocol.GitOperationCommit{Oid: mainTip, Label: "main"}, StartCommandID: cmd.ID, ProjectID: "p-git", StartedAt: "now"})
	running := protocol.Receipt{ID: cmd.ID, State: protocol.GitStateRunning, Revision: snap.Revision, Git: &protocol.GitResult{Op: "rebase", State: protocol.GitStateRunning}}
	if err := st.Save(snap, &cmd, &running); err != nil {
		t.Fatal(err)
	}
	st.Close()
	c, stop := startTestServer(t, home)
	defer stop()
	ctx := context.Background()
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.GitOperations) != 1 || s.GitOperations[0].State != protocol.GitOperationInterrupted {
		t.Fatalf("recovered record: %+v", s.GitOperations)
	}
	op, err := c.GitOperation(ctx, client.GitTarget{ProjectID: "p-git"})
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != "rebase" || op.Source != protocol.GitOperationSourceApp || op.OperationID != "op-crashed" || op.Record == nil || op.Record.State != protocol.GitOperationStoppedConflicts || op.Target.Label != "main" {
		t.Fatalf("reconciled state: %+v %+v", op, op.Record)
	}
	r, err := c.GitWrite(ctx, cmd)
	if err != nil || r.State != protocol.GitStateOutcomeUnknown || r.Git.Code != "interrupted" {
		t.Fatalf("retry after restart: %+v %v", r, err)
	}
	// Ending it in a terminal ends the record on the next read.
	git("rebase", "--abort")
	if op, err = c.GitOperation(ctx, client.GitTarget{ProjectID: "p-git"}); err != nil || op.Kind != "" {
		t.Fatalf("after external abort: %+v %v", op, err)
	}
	s, _ = c.Snapshot(ctx)
	if s.GitOperations[0].State != protocol.GitOperationEndedExternal || s.GitOperations[0].EndedAt == "" {
		t.Fatalf("ended record: %+v", s.GitOperations)
	}
}

func TestGitOperationWaitsForOpenDocuments(t *testing.T) {
	h, git := docGit(t)
	h.write("c.txt", "base\n", 0o644)
	h.write("u.txt", "u\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	h.write("c.txt", "other\n", 0o644)
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	h.write("c.txt", "main\n", 0o644)
	git("commit", "-q", "-am", "main")
	// Saving an unsaved edit first makes the tree dirty: nothing starts.
	unsavedEdit(h, "u.txt", 41)
	p := previewOf(t, h.root, "merge", "refs/heads/other")
	r := mustGit(t, h.e, client.GitMergeCommand("merge-dirty", gitTarget, mustStatus(t, h.root), p), protocol.GitStateFailed)
	if r.Git.Code != "documents_changed_tree" || r.Git.Operation.Outcome != protocol.GitOutcomeNotStarted || h.read("u.txt") != "UNSAVED u\n" || operationOf(t, h.e, h.root).Kind != "" {
		t.Fatalf("merge after saving documents: %+v", r.Git)
	}
	if len(h.e.current().GitOperations) != 0 {
		t.Fatalf("record kept for a merge that never started: %+v", h.e.current().GitOperations)
	}
	git("commit", "-q", "-am", "saved edit")
	mainTip := git("rev-parse", "HEAD")
	// Git abort waits for documents that cannot be saved.
	gitTry(t, h.root, "merge", "other")
	id := h.open("c.txt", "bob")
	h.mustCommand(protocol.DocumentKindEdit, id, "bob")
	ed := h.connect(id, "bob", 42)
	ed.gen = 1
	h.store.fail.Store(true)
	op, _ := ed.insert(0, "resolving ")
	h.commit(id)
	st := operationOf(t, h.e, h.root)
	r = mustGit(t, h.e, client.GitOperationAbortCommand("abort-blocked", gitTarget, st, false, false), protocol.GitStateFailed)
	if r.Git.Code != "document_unsaved" || operationOf(t, h.e, h.root).Kind != "merge" {
		t.Fatalf("abort with unsaved document: %+v", r.Git)
	}
	h.store.fail.Store(false)
	h.clock.Advance(docRetryMax)
	ed.next("ack", ackFor(op))
	// The saved edit changed the reviewed working tree: review again.
	r = mustGit(t, h.e, client.GitOperationAbortCommand("abort-stale", gitTarget, st, false, false), protocol.GitStateFailed)
	if r.Git.Code != "stale_status" || operationOf(t, h.e, h.root).Kind != "merge" {
		t.Fatalf("abort after a saved edit: %+v", r.Git)
	}
	r = mustGit(t, h.e, client.GitOperationAbortCommand("abort", gitTarget, operationOf(t, h.e, h.root), false, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeAborted || git("rev-parse", "HEAD") != mainTip || operationOf(t, h.e, h.root).Kind != "" {
		t.Fatalf("abort: %+v", r.Git)
	}
}

// Review 2026-09-25: user work outside the operation is never reset or
// committed unseen.

func TestGitSkipAndAbortNameUnrelatedChanges(t *testing.T) {
	e, root, git := conflictSetup(t)
	writeFile(t, root, "u.txt", "u\n")
	writeFile(t, root, "s.txt", "s\n")
	git("add", ".")
	git("commit", "-q", "-m", "u and s")
	git("checkout", "-q", "other")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	writeFile(t, root, "u.txt", "precious edit during the stop\n")
	st := operationOf(t, e, root)
	if !slices.Equal(st.DiscardsOnSkip, []string{"u.txt"}) || !slices.Equal(st.DiscardsOnAbort, []string{"u.txt"}) || st.WorktreeFingerprint == "" {
		t.Fatalf("discards: skip %v abort %v", st.DiscardsOnSkip, st.DiscardsOnAbort)
	}
	skip, _ := client.GitOperationSkipCommand("skip-unacknowledged", gitTarget, st, false)
	_, err := e.command(skip)
	wantGitCode(t, err, "discards_unacknowledged")
	if !strings.Contains(err.Error(), "u.txt") || readText(t, root, "u.txt") != "precious edit during the stop\n" {
		t.Fatalf("refusal: %v", err)
	}
	notRecorded(t, e, skip)
	abort := client.GitOperationAbortCommand("abort-unacknowledged", gitTarget, st, false, false)
	_, err = e.command(abort)
	wantGitCode(t, err, "discards_unacknowledged")
	// A change after review is stale even when acknowledged.
	writeFile(t, root, "u.txt", "edited again\n")
	skip, _ = client.GitOperationSkipCommand("skip-stale", gitTarget, st, true)
	_, err = e.command(skip)
	wantGitCode(t, err, "stale_status")
	st = operationOf(t, e, root)
	skip, _ = client.GitOperationSkipCommand("skip", gitTarget, st, true)
	r := mustGit(t, e, skip, protocol.GitStateSucceeded)
	if r.Git.Operation.Skipped == nil || readText(t, root, "u.txt") != "u\n" {
		t.Fatalf("acknowledged skip: %+v", r.Git)
	}

	// A merge abort keeps unstaged changes but resets staged ones.
	git("checkout", "-q", "-b", "side", "main")
	writeFile(t, root, "c.txt", "side\n")
	git("commit", "-q", "-am", "side")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main again\n")
	git("commit", "-q", "-am", "main again")
	gitTry(t, root, "merge", "side")
	writeFile(t, root, "u.txt", "unstaged\n")
	writeFile(t, root, "s.txt", "staged\n")
	git("add", "s.txt")
	st = operationOf(t, e, root)
	if st.Kind != "merge" || !slices.Equal(st.DiscardsOnAbort, []string{"s.txt"}) || st.DiscardsOnSkip != nil {
		t.Fatalf("merge discards: %+v", st.DiscardsOnAbort)
	}
	mustGit(t, e, client.GitOperationAbortCommand("merge-abort", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if readText(t, root, "u.txt") != "unstaged\n" || readText(t, root, "s.txt") != "s\n" {
		t.Fatalf("merge abort: u %q s %q", readText(t, root, "u.txt"), readText(t, root, "s.txt"))
	}
}

func TestGitInteractiveStopIsNotSkippedOrContinued(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	for i, name := range []string{"c", "f1", "f2"} {
		writeFile(t, root, name, name+"\n")
		git("add", ".")
		git("commit", "-q", "-m", []string{"base", "one", "two"}[i])
	}
	cmd := exec.Command("git", "-C", root, "-c", "core.hooksPath=/dev/null", "rebase", "-i", "HEAD~2")
	cmd.Env = append(os.Environ(), "GIT_SEQUENCE_EDITOR=sed -i 1s/^pick/edit/", "GIT_EDITOR=:")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	writeFile(t, root, "c", "work in progress while editing\n")
	head := git("rev-parse", "HEAD")
	st := operationOf(t, e, root)
	if st.StopReason != protocol.GitStopEdit || st.Can.Skip.Allowed || st.Can.Continue.Allowed || !strings.Contains(st.Can.Skip.Reason, "interactive") {
		t.Fatalf("edit stop: %s %+v", st.StopReason, st.Can)
	}
	if st.Current == nil || st.Current.Oid != head || !strings.HasPrefix(st.Current.Label, "stopped after applying "+head[:12]) {
		t.Fatalf("edit stop current: %+v", st.Current)
	}
	if _, ok := client.GitOperationSkipCommand("skip", gitTarget, st, true); ok {
		t.Fatal("skip offered at an edit stop")
	}
	forced := protocol.Command{Version: 1, ID: "forced-skip", Kind: protocol.GitKindOperationSkip, ProjectID: "p-git", Git: &protocol.GitWrite{Operation: &protocol.GitOperationWrite{
		Kind: "rebase", ExpectedHead: head, ExpectedStep: st.Step, SkipOid: head, WorktreeFingerprint: st.WorktreeFingerprint, AcknowledgeDiscard: st.DiscardsOnSkip, Confirmed: true}}}
	_, err := e.command(forced)
	wantGitCode(t, err, "not_supported")
	_, err = e.command(client.GitOperationContinueCommand("cont", gitTarget, st, true))
	wantGitCode(t, err, "not_supported")
	if readText(t, root, "c") != "work in progress while editing\n" || !slices.Equal(st.DiscardsOnAbort, []string{"c"}) {
		t.Fatalf("edit stop discards: %v", st.DiscardsOnAbort)
	}
}

func TestGitContinuePinsTheStagedSet(t *testing.T) {
	e, root, git := conflictSetup(t)
	writeFile(t, root, "u.txt", "u\n")
	git("add", "u.txt")
	git("commit", "-q", "-m", "u")
	git("checkout", "-q", "other")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	head := git("rev-parse", "HEAD")
	writeFile(t, root, "u.txt", "sneaky\n")
	git("add", "u.txt")
	cont := client.GitOperationContinueCommand("cont", gitTarget, st, false)
	_, err := e.command(cont)
	wantGitCode(t, err, "stale_status")
	notRecorded(t, e, cont)
	if git("rev-parse", "HEAD") != head {
		t.Fatal("continue committed an unreviewed staged change")
	}
}

func TestGitContinueNeedsConflictMarkersAcknowledged(t *testing.T) {
	e, root, git := conflictSetup(t)
	gitTry(t, root, "merge", "other")
	git("add", "c.txt") // markers staged as they are
	st := operationOf(t, e, root)
	if !slices.Equal(st.MarkerPaths, []string{"c.txt"}) || !st.Can.Continue.Allowed {
		t.Fatalf("markers: %v %+v", st.MarkerPaths, st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont-markers", gitTarget, st, false))
	wantGitCode(t, err, "markers_unacknowledged")
	r := mustGit(t, e, client.GitOperationContinueCommand("cont", gitTarget, st, true), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
		t.Fatalf("acknowledged continue: %+v", r.Git)
	}
}

func TestGitIntegrateRefusesHiddenLocalChanges(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "u", "u\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "u", "other\n")
	git("commit", "-q", "-am", "o")
	git("checkout", "-q", "main")
	writeFile(t, root, "m", "m\n")
	git("add", "m")
	git("commit", "-q", "-m", "m")
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		git("update-index", flag, "u")
		writeFile(t, root, "u", "LOCAL precious\n")
		p := previewOf(t, root, "merge", "refs/heads/other")
		if p.Blocked != "not_supported" {
			t.Fatalf("%s preview: %+v", flag, p)
		}
		_, err := e.command(client.GitMergeCommand("hidden"+flag, gitTarget, mustStatus(t, root), p))
		wantGitCode(t, err, "not_supported")
		if readText(t, root, "u") != "LOCAL precious\n" {
			t.Fatal("hidden local change overwritten")
		}
		git("update-index", "--no"+flag[1:], "u")
		git("checkout", "--", "u")
	}
}

func TestGitMergeReplacesAFileWithADirectory(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "x", "file\n")
	writeFile(t, root, "a", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	git("rm", "-q", "x")
	writeFile(t, root, "x/y", "dir\n")
	git("add", ".")
	git("commit", "-q", "-m", "x dir")
	git("checkout", "-q", "main")
	writeFile(t, root, "a", "main\n")
	git("commit", "-q", "-am", "main")
	p := previewOf(t, root, "merge", "refs/heads/other")
	if p.Blocked != "" {
		t.Fatalf("preview: %+v", p)
	}
	r := mustGit(t, e, client.GitMergeCommand("m", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || readText(t, root, "x/y") != "dir\n" {
		t.Fatalf("file to directory: %+v", r.Git)
	}
	// An untracked file blocking a path the target adds is still refused.
	git("checkout", "-q", "-b", "zdir")
	writeFile(t, root, "z/w", "w\n")
	git("add", ".")
	git("commit", "-q", "-m", "z dir")
	git("checkout", "-q", "main")
	writeFile(t, root, "z", "untracked\n")
	p = previewOf(t, root, "merge", "refs/heads/zdir")
	_, err := e.command(client.GitMergeCommand("blocked", gitTarget, mustStatus(t, root), p))
	wantGitCode(t, err, "would_overwrite")
	if readText(t, root, "z") != "untracked\n" {
		t.Fatal("untracked file replaced")
	}
}

func TestGitMergeFollowsMergeFF(t *testing.T) {
	e, root, git := conflictSetup(t)
	git("config", "merge.ff", "only")
	p := previewOf(t, root, "merge", "refs/heads/other")
	if p.MergeFF != protocol.GitMergeFFOnly || p.Blocked != "ff_only_configured" || p.FastForward {
		t.Fatalf("ff-only preview: %+v", p)
	}
	_, err := e.command(client.GitMergeCommand("ffonly", gitTarget, mustStatus(t, root), p))
	wantGitCode(t, err, "ff_only_configured")
	git("config", "merge.ff", "false")
	git("checkout", "-q", "-b", "behind", "main~1")
	p = previewOf(t, root, "merge", "refs/heads/main")
	if p.MergeFF != protocol.GitMergeNoFF || p.FastForward || p.Blocked != "" {
		t.Fatalf("no-ff preview: %+v", p)
	}
	mustGit(t, e, client.GitMergeCommand("noff", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD^2") != git("rev-parse", "main") {
		t.Fatal("merge.ff=false did not create a merge commit")
	}
	// The branch's mergeOptions override merge.ff, as for the CLI.
	git("config", "branch.behind.mergeOptions", "--ff")
	git("reset", "-q", "--hard", "main~1")
	if p = previewOf(t, root, "merge", "refs/heads/main"); p.MergeFF != protocol.GitMergeFF || !p.FastForward {
		t.Fatalf("mergeOptions preview: %+v", p)
	}
}

func TestGitOperationJobThreadIsWokenInReservedCheckout(t *testing.T) {
	e, root, git := conflictSetup(t)
	addGitThread(e, "p-git", "t-extra-job", root)
	sendTo(t, e, "t-git", "before-merge")
	if th := threadOf(e.current(), "t-git"); th.State != "running" {
		t.Fatalf("holder: %s", th.State)
	}
	gitTry(t, root, "merge", "other")
	e.mu.Lock()
	e.snap.GitOperations = []protocol.GitOperationRecord{{OperationID: "op-job", Checkout: root, Kind: "merge", State: protocol.GitOperationAgentRunning,
		Target: protocol.GitOperationCommit{Oid: git("rev-parse", "other")}, JobThreadID: "t-extra-job"}}
	e.mu.Unlock()
	sendTo(t, e, "t-extra-job", "job-prompt")
	if th := threadOf(e.current(), "t-extra-job"); th.State != "idle" || th.WriterWait == nil || th.WriterWait.HolderThreadID != "t-git" {
		t.Fatalf("job before release: %s %+v", th.State, th.WriterWait)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: "t-git"}); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(e.current(), "t-extra-job"); th.State != "running" || th.WriterWait != nil {
		t.Fatalf("job not woken in the reserved checkout: %s %+v", th.State, th.WriterWait)
	}
	addGitThread(e, "p-git", "t-extra-other", root)
	sendTo(t, e, "t-extra-other", "during-merge")
	if th := threadOf(e.current(), "t-extra-other"); th.State != "idle" || th.WriterWait == nil {
		t.Fatalf("other thread during the job: %s %+v", th.State, th.WriterWait)
	}
}

func TestGitOperationStaleReadDoesNotEndANewRecord(t *testing.T) {
	e, root, _ := conflictSetup(t)
	e.mu.Lock()
	seq := e.gitLocked().opSeq
	e.mu.Unlock()
	stale, top, err := readGitOperation(context.Background(), root)
	if err != nil || stale.Kind != "" {
		t.Fatalf("idle read: %+v %v", stale, err)
	}
	p := previewOf(t, root, "merge", "refs/heads/other")
	mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	e.attachOperation(&stale, top, seq)
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationStoppedConflicts {
		t.Fatalf("record ended by a read that predates it: %+v", rec)
	}
}

// Second review 2026-09-25.

func backupText(t *testing.T, e *engine, root, oid, path string) string {
	t.Helper()
	f, err := readBackupFile(context.Background(), root, oid, path, func(top, oid string) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.backupRecordedLocked(top, oid)
	})
	if err != nil {
		t.Fatalf("backup of %s: %v", path, err)
	}
	return string(f.Content)
}

func TestGitAbortListsAndBacksUpEditsOfTheOperationsOwnPaths(t *testing.T) {
	e, root, git := conflictSetup(t)
	refs := git("for-each-ref")
	gitTry(t, root, "merge", "other")
	if st := operationOf(t, e, root); st.DiscardsOnAbort != nil {
		t.Fatalf("the merge's own staged d.txt listed: %v", st.DiscardsOnAbort)
	}
	writeFile(t, root, "d.txt", "d\nUSER WORK\n")
	git("add", "d.txt")
	st := operationOf(t, e, root)
	if !slices.Equal(st.DiscardsOnAbort, []string{"d.txt"}) {
		t.Fatalf("own-path edit not listed: %v", st.DiscardsOnAbort)
	}
	_, err := e.command(client.GitOperationAbortCommand("ab-unack", gitTarget, st, false, false))
	wantGitCode(t, err, "discards_unacknowledged")
	r := mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, false), protocol.GitStateSucceeded)
	b := r.Git.Operation.Backup
	if b == nil || b.Incomplete || !slices.Contains(b.Paths, "d.txt") || !slices.Contains(b.Paths, "c.txt") {
		t.Fatalf("backup: %+v", b)
	}
	if got := backupText(t, e, root, b.Oid, "d.txt"); got != "d\nUSER WORK\n" {
		t.Fatalf("backed up d.txt %q", got)
	}
	if got := backupText(t, e, root, b.IndexOid, "d.txt"); got != "d\nUSER WORK\n" {
		t.Fatalf("backed up staged d.txt %q", got)
	}
	if !strings.Contains(backupText(t, e, root, b.Oid, "c.txt"), "<<<<<<<") {
		t.Fatal("conflicted file not backed up")
	}
	if git("for-each-ref") != refs {
		t.Fatal("the backup wrote a ref")
	}
	// The documented recovery works.
	git("checkout", b.Oid, "--", "d.txt")
	if readText(t, root, "d.txt") != "d\nUSER WORK\n" {
		t.Fatal("recovery from the backup failed")
	}
}

func TestGitRebaseAbortListsIgnoredFilesInTheWay(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "Z", "tracked\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	git("rm", "-q", "Z")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	p := previewOf(t, root, "rebase", "refs/heads/other")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "Z", "USER WORK\n")
	st := operationOf(t, e, root)
	if !slices.Equal(st.DiscardsOnAbort, []string{"Z"}) {
		t.Fatalf("ignored file in the way not listed: %v", st.DiscardsOnAbort)
	}
	r := mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if readText(t, root, "Z") != "tracked\n" || backupText(t, e, root, r.Git.Operation.Backup.Oid, "Z") != "USER WORK\n" {
		t.Fatalf("ignored Z: now %q", readText(t, root, "Z"))
	}
	if rec := recordOf(e); rec == nil || rec.Backup == nil || rec.Backup.Oid != r.Git.Operation.Backup.Oid {
		t.Fatalf("record backup: %+v", rec)
	}
}

func TestGitContinueRefusesInTheWayOfRemainingCommits(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, ".gitignore", "*.log\n")
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	writeFile(t, root, "c.txt", "feature\n")
	git("commit", "-q", "-am", "f1")
	writeFile(t, root, "later.log", "tracked later\n")
	git("add", "-f", "later.log")
	git("commit", "-q", "-m", "f2")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	git("checkout", "-q", "feature")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	writeFile(t, root, "later.log", "precious\n")
	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	if !slices.Equal(st.ContinueInTheWay, []string{"later.log"}) || st.Can.Continue.Allowed || st.Can.Skip.Allowed {
		t.Fatalf("in the way: %v %+v", st.ContinueInTheWay, st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont", gitTarget, st, false))
	wantGitCode(t, err, "would_overwrite")
	if readText(t, root, "later.log") != "precious\n" {
		t.Fatal("ignored file overwritten")
	}
}

func TestGitMarkerScanReadsStagedBlobs(t *testing.T) {
	e, root, git := conflictSetup(t)
	gitTry(t, root, "merge", "other")
	// Attributes do not hide markers; CRLF and a custom size are honoured.
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("c.txt -diff\nwide.txt conflict-marker-size=10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "c.txt", "<<<<<<< HEAD\r\nmain\r\n=======\r\nother\r\n>>>>>>> other\r\n")
	writeFile(t, root, "wide.txt", "<<<<<<< not a marker at size 10\n<<<<<<<<<< ours\n")
	writeFile(t, root, "narrow.txt", "<<<<<<<< eight is not seven\n")
	writeFile(t, root, "bin.dat", "\x00<<<<<<< HEAD\n")
	git("add", ".")
	st := operationOf(t, e, root)
	if !slices.Equal(st.MarkerPaths, []string{"c.txt", "wide.txt"}) || st.MarkersIncomplete {
		t.Fatalf("markers: %v incomplete=%v", st.MarkerPaths, st.MarkersIncomplete)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont", gitTarget, st, false))
	wantGitCode(t, err, "markers_unacknowledged")

	// A blob too large to scan makes the scan incomplete: never silent.
	writeFile(t, root, "big.txt", strings.Repeat("x", gitMarkerBlobMax+1))
	git("add", "big.txt")
	st = operationOf(t, e, root)
	if !st.MarkersIncomplete {
		t.Fatal("oversized blob scanned silently")
	}
	cmd := client.GitOperationContinueCommand("cont-incomplete", gitTarget, st, true)
	_, err = e.command(cmd)
	wantGitCode(t, err, "markers_incomplete")
	cmd.ID = "cont-acknowledged"
	cmd.Git.Operation.AcknowledgeMarkersIncomplete = true
	if r := mustGit(t, e, cmd, protocol.GitStateSucceeded); r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
		t.Fatalf("acknowledged continue: %+v", r.Git)
	}
}

func TestGitMergeReplacesADirectoryWithAFile(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "d/x", "x\n")
	writeFile(t, root, "a", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	git("rm", "-q", "-r", "d")
	writeFile(t, root, "d", "file\n")
	git("add", ".")
	git("commit", "-q", "-m", "d file")
	git("checkout", "-q", "main")
	writeFile(t, root, "a", "main\n")
	git("commit", "-q", "-am", "main")
	writeFile(t, root, "d/untracked", "mine\n")
	p := previewOf(t, root, "merge", "refs/heads/other")
	if p.Blocked != "would_overwrite" || !strings.Contains(p.BlockedMessage, "d/untracked") {
		t.Fatalf("untracked file in the directory: %+v", p)
	}
	if err := os.Remove(filepath.Join(root, "d", "untracked")); err != nil {
		t.Fatal(err)
	}
	p = previewOf(t, root, "merge", "refs/heads/other")
	if p.Blocked != "" {
		t.Fatalf("directory to file refused: %+v", p)
	}
	r := mustGit(t, e, client.GitMergeCommand("m", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || readText(t, root, "d") != "file\n" {
		t.Fatalf("directory to file: %+v", r.Git)
	}
}

func TestGitAbortNeverRecursesIntoSubmodules(t *testing.T) {
	e, root, git := conflictSetup(t)
	writeFile(t, os.Getenv("HOME"), ".gitconfig", "[protocol \"file\"]\n\tallow = always\n")
	subSrc := t.TempDir()
	gitIn(t, subSrc, "init", "-q")
	writeFile(t, subSrc, "s.txt", "one\n")
	gitIn(t, subSrc, "add", ".")
	gitIn(t, subSrc, "commit", "-q", "-m", "one")
	writeFile(t, subSrc, "s.txt", "two\n")
	gitIn(t, subSrc, "commit", "-q", "-am", "two")
	two := gitIn(t, subSrc, "rev-parse", "HEAD")
	git("checkout", "-q", "main~1")
	git("checkout", "-q", "-b", "base2")
	git("submodule", "add", "-q", subSrc, "sub")
	gitIn(t, filepath.Join(root, "sub"), "checkout", "-q", "HEAD~1")
	git("add", "sub")
	git("commit", "-q", "-m", "sub at one")
	git("checkout", "-q", "-b", "bump")
	gitIn(t, filepath.Join(root, "sub"), "checkout", "-q", two)
	writeFile(t, root, "c.txt", "bump\n")
	git("add", ".")
	git("commit", "-q", "-m", "bump sub")
	git("checkout", "-q", "base2")
	gitIn(t, filepath.Join(root, "sub"), "checkout", "-q", "HEAD~1")
	writeFile(t, root, "c.txt", "base2\n")
	git("commit", "-q", "-am", "base2 c")
	git("config", "submodule.recurse", "true")
	gitTry(t, root, "merge", "bump")
	writeFile(t, filepath.Join(root, "sub"), "s.txt", "DIRTY submodule work\n")
	st := operationOf(t, e, root)
	if st.Kind != "merge" {
		t.Fatalf("no merge: %+v", st)
	}
	mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if got := readText(t, filepath.Join(root, "sub"), "s.txt"); got != "DIRTY submodule work\n" {
		t.Fatalf("submodule worktree changed by abort: %q", got)
	}
}

func TestGitCherryPickOfAMergeListsWhatItCannotAttribute(t *testing.T) {
	e, root, git := conflictSetup(t)
	git("checkout", "-q", "-b", "m", "main~1")
	writeFile(t, root, "x.txt", "x\n")
	git("add", ".")
	git("commit", "-q", "-m", "x")
	git("merge", "-q", "--no-ff", "-m", "merge other", "other")
	merge := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	gitTry(t, root, "cherry-pick", "-m", "1", merge)
	st := operationOf(t, e, root)
	if st.Kind != "cherry-pick" || !slices.Contains(st.DiscardsOnAbort, "d.txt") {
		t.Fatalf("mainline unknown, so d.txt must be listed: %+v", st.DiscardsOnAbort)
	}
}

func TestGitOperationJobGoesAheadOfWaitersItHolds(t *testing.T) {
	e, root, git := conflictSetup(t)
	addGitThread(e, "p-git", "t-extra-a", root)
	addGitThread(e, "p-git", "t-extra-job", root)
	sendTo(t, e, "t-git", "before-merge")
	gitTry(t, root, "merge", "other")
	e.mu.Lock()
	e.snap.GitOperations = []protocol.GitOperationRecord{{OperationID: "op-job", Checkout: root, Kind: "merge", State: protocol.GitOperationAgentRunning,
		Target: protocol.GitOperationCommit{Oid: git("rev-parse", "other")}, JobThreadID: "t-extra-job"}}
	e.mu.Unlock()
	sendTo(t, e, "t-extra-a", "first-waiter")
	sendTo(t, e, "t-extra-job", "job")
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: "t-git"}); err != nil {
		t.Fatal(err)
	}
	s := e.current()
	if th := threadOf(s, "t-extra-job"); th.State != "running" {
		t.Fatalf("job behind an ordinary waiter: %s %+v", th.State, th.WriterWait)
	}
	if th := threadOf(s, "t-extra-a"); th.State != "idle" || th.WriterWait == nil {
		t.Fatalf("ordinary waiter: %s %+v", th.State, th.WriterWait)
	}
}

func TestGitOperationBudgetIsReportedAsTimeout(t *testing.T) {
	e, root, _ := conflictSetup(t)
	defer func(old time.Duration) { gitOperationBudget = old }(gitOperationBudget)
	gitOperationBudget = time.Second
	writeHook(t, root, "pre-merge-commit", "sleep 5\n")
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("checkout", "-q", "-b", "behind", "main~1")
	writeFile(t, root, "e.txt", "e\n")
	git("add", "e.txt")
	git("commit", "-q", "-m", "e")
	p := previewOf(t, root, "merge", "refs/heads/main")
	r := mustGit(t, e, client.GitMergeCommand("slow", gitTarget, mustStatus(t, root), p), protocol.GitStateOutcomeUnknown)
	if r.Git.Code != "timeout" || !strings.Contains(r.Git.Message, "budget") {
		t.Fatalf("budget: %+v", r.Git)
	}
}

func TestGitOperationBackupIsReadableThroughTheClient(t *testing.T) {
	root, git := gitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("config", "user.name", "W")
	git("config", "user.email", "w@example.invalid")
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	gitTry(t, root, "merge", "other")
	writeFile(t, root, "c.txt", "half resolved\n")
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	snap := fixture.Initial()
	snap.Projects = append(snap.Projects, protocol.Project{ID: "p-git", Name: "repo", Path: root, Revision: 1})
	if err := st.Save(snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	c, stop := startTestServer(t, home)
	defer stop()
	ctx := context.Background()
	target := client.GitTarget{ProjectID: "p-git"}
	op, err := c.GitOperation(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.GitWrite(ctx, client.GitOperationAbortCommand("abort", target, op, true, false))
	if err != nil || r.Git.Operation == nil || r.Git.Operation.Backup == nil {
		t.Fatalf("abort: %+v %v", r, err)
	}
	f, err := c.GitOperationBackupFile(ctx, target, r.Git.Operation.Backup.Oid, "c.txt")
	if err != nil || string(f.Content) != "half resolved\n" || f.Mode != "100644" {
		t.Fatalf("backup file: %+v %v", f, err)
	}
	if _, err := c.GitOperationBackupFile(ctx, target, r.Git.Operation.Backup.Oid, "../x"); err == nil {
		t.Fatal("invalid path accepted")
	}
}

// Third review 2026-09-25.

func TestGitSequenceAbortListsAndBacksUpFilesItRestores(t *testing.T) {
	for _, kind := range []string{"cherry-pick", "revert"} {
		for _, ignored := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s-ignored=%v", kind, ignored), func(t *testing.T) {
				e, root, git := gitWriteSetup(t)
				writeFile(t, root, "c.txt", "base\n")
				writeFile(t, root, "X", "tracked X\n")
				git("add", ".")
				git("commit", "-q", "-m", "base")
				restored := "X"
				if kind == "cherry-pick" {
					git("checkout", "-q", "-b", "other")
					git("rm", "-q", "X")
					git("commit", "-q", "-m", "rm X")
					writeFile(t, root, "c.txt", "other\n")
					git("commit", "-q", "-am", "other c")
					git("checkout", "-q", "main")
					writeFile(t, root, "c.txt", "main\n")
					git("commit", "-q", "-am", "main c")
					gitTry(t, root, "cherry-pick", "main..other")
				} else {
					restored = "Y"
					writeFile(t, root, "Y", "Y added\n")
					git("add", "Y")
					git("commit", "-q", "-m", "add Y")
					a := git("rev-parse", "HEAD")
					writeFile(t, root, "c.txt", "B\n")
					git("commit", "-q", "-am", "B")
					b := git("rev-parse", "HEAD")
					writeFile(t, root, "c.txt", "C\n")
					git("commit", "-q", "-am", "C")
					gitTry(t, root, "revert", "--no-edit", a, b)
				}
				start := strings.TrimSpace(readText(t, root, ".git/sequencer/head"))
				if ignored {
					if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(restored+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				writeFile(t, root, restored, "USER WORK\n")
				st := operationOf(t, e, root)
				if st.Kind != kind || st.OrigHead != start || len(st.AbortDropsCommits) != 1 || !slices.Contains(st.DiscardsOnAbort, restored) {
					t.Fatalf("sequence state: kind %s orig %s drops %+v discards %v", st.Kind, st.OrigHead, st.AbortDropsCommits, st.DiscardsOnAbort)
				}
				_, err := e.command(client.GitOperationAbortCommand("ab-unack", gitTarget, st, false, true))
				wantGitCode(t, err, "discards_unacknowledged")
				_, err = e.command(client.GitOperationAbortCommand("ab-undropped", gitTarget, st, true, false))
				wantGitCode(t, err, "drops_unacknowledged")
				r := mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, true), protocol.GitStateSucceeded)
				if git("rev-parse", "HEAD") != start || backupText(t, e, root, r.Git.Operation.Backup.Oid, restored) != "USER WORK\n" {
					t.Fatalf("abort: head %s, backup of %s missing", git("rev-parse", "HEAD"), restored)
				}
			})
		}
	}
}

func TestGitRebaseAbortAndSkipBackUpUntrackedFilesInAReplacedFile(t *testing.T) {
	for _, verb := range []string{"abort", "skip"} {
		t.Run(verb, func(t *testing.T) {
			e, root, git := conflictSetup(t)
			writeFile(t, root, "b.txt", "b\n")
			git("add", "b.txt")
			git("commit", "-q", "-m", "b")
			git("checkout", "-q", "other")
			p := previewOf(t, root, "rebase", "refs/heads/main")
			mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
			if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
				t.Fatal(err)
			}
			// Ignored, so Git would remove it without asking.
			if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeFile(t, root, "b.txt/work", "USER WORK\n")
			st := operationOf(t, e, root)
			list := st.DiscardsOnAbort
			if verb == "skip" {
				list = st.DiscardsOnSkip
			}
			if !slices.Equal(list, []string{"b.txt", "b.txt/work"}) || st.BackupIncomplete {
				t.Fatalf("%s discards: %v backup incomplete %v", verb, list, st.BackupIncomplete)
			}
			var cmd protocol.Command
			if verb == "skip" {
				cmd, _ = client.GitOperationSkipCommand("go", gitTarget, st, true)
			} else {
				cmd = client.GitOperationAbortCommand("go", gitTarget, st, true, false)
			}
			r := mustGit(t, e, cmd, protocol.GitStateSucceeded)
			if backupText(t, e, root, r.Git.Operation.Backup.Oid, "b.txt/work") != "USER WORK\n" {
				t.Fatal("directory content not backed up")
			}
		})
	}
}

func TestGitContinueStoppedByItsBudgetIsOutcomeUnknown(t *testing.T) {
	e, root, git := conflictSetup(t)
	defer func(old time.Duration) { gitOperationBudget = old }(gitOperationBudget)
	git("checkout", "-q", "other")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	writeHook(t, root, "post-rewrite", "sleep 5\n")
	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	gitOperationBudget = time.Second
	r := mustGit(t, e, client.GitOperationContinueCommand("cont", gitTarget, st, false), protocol.GitStateOutcomeUnknown)
	if r.Git.Code != "timeout" || r.Git.Operation.Outcome != protocol.GitOutcomeUnknown || !strings.Contains(r.Git.Message, "HEAD is at") {
		t.Fatalf("timed-out continue: %+v %+v", r.Git, r.Git.Operation)
	}
}

func TestGitBackupEndpointServesOnlyRecordedBackups(t *testing.T) {
	e, root, git := conflictSetup(t)
	_, err := readBackupFile(context.Background(), root, git("rev-parse", "HEAD"), "c.txt", func(top, oid string) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.backupRecordedLocked(top, oid)
	})
	wantGitCode(t, err, "not_found")
}

func TestGitAbortRefusesHiddenLocalChanges(t *testing.T) {
	e, root, git := conflictSetup(t)
	gitTry(t, root, "merge", "other")
	git("update-index", "--assume-unchanged", "d.txt")
	writeFile(t, root, "d.txt", "hidden edit\n")
	st := operationOf(t, e, root)
	if !slices.Equal(st.HiddenEntries, []string{"d.txt"}) || st.Can.Abort.Allowed {
		t.Fatalf("hidden: %v %+v", st.HiddenEntries, st.Can.Abort)
	}
	_, err := e.command(client.GitOperationAbortCommand("ab", gitTarget, st, true, false))
	wantGitCode(t, err, "not_supported")
	if readText(t, root, "d.txt") != "hidden edit\n" {
		t.Fatal("hidden edit overwritten")
	}
}

func TestGitDiscardAcknowledgementWorksForNonUTF8Paths(t *testing.T) {
	e, root, git := conflictSetup(t)
	bad := "bad\xff.txt"
	writeFile(t, root, bad, "b\n")
	git("add", ".")
	git("commit", "-q", "-m", "odd name")
	git("checkout", "-q", "other")
	p := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	writeFile(t, root, bad, "edited\n")
	st := operationOf(t, e, root)
	if !slices.Contains(st.DiscardsOnAbort, bad) || st.DiscardsOnAbortFingerprint == "" {
		t.Fatalf("discards: %q", st.DiscardsOnAbort)
	}
	// Through JSON the name is mangled; the fingerprint still acknowledges it.
	cmd := client.GitOperationAbortCommand("ab", gitTarget, st, true, false)
	b, _ := json.Marshal(cmd)
	var wire protocol.Command
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	r := mustGit(t, e, wire, protocol.GitStateSucceeded)
	if backupText(t, e, root, r.Git.Operation.Backup.Oid, bad) != "edited\n" {
		t.Fatal("odd name not backed up")
	}
}

// Fourth review 2026-09-25.

func nestedRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	writeFile(t, dir, "precious", "unpushed\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "n")
}

func TestGitOperationRefusesNestedRepositoriesInTheWay(t *testing.T) {
	for _, verb := range []string{"abort", "skip"} {
		for _, ignored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-ignored=%v", verb, ignored), func(t *testing.T) {
				e, root, git := conflictSetup(t)
				writeFile(t, root, "b.txt", "b\n")
				git("add", "b.txt")
				git("commit", "-q", "-m", "b")
				git("checkout", "-q", "other")
				p := previewOf(t, root, "rebase", "refs/heads/main")
				mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
				if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
					t.Fatal(err)
				}
				nestedRepo(t, filepath.Join(root, "b.txt", "nested"))
				if ignored {
					if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("nested/\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				st := operationOf(t, e, root)
				if !slices.Equal(st.NestedInTheWay, []string{"b.txt/nested/"}) || st.Can.Abort.Allowed || st.Can.Skip.Allowed {
					t.Fatalf("nested: %v %+v", st.NestedInTheWay, st.Can)
				}
				var cmd protocol.Command
				if verb == "skip" {
					cmd, _ = client.GitOperationSkipCommand("go", gitTarget, st, true)
				} else {
					cmd = client.GitOperationAbortCommand("go", gitTarget, st, true, true)
				}
				cmd.Git.Operation.AcknowledgeBackupIncomplete = true
				_, err := e.command(cmd)
				wantGitCode(t, err, "not_supported")
				if readText(t, filepath.Join(root, "b.txt", "nested"), "precious") != "unpushed\n" {
					t.Fatal("nested repository content removed")
				}
			})
		}
	}
}

func TestGitMergeAbortRefusesNestedRepositoryInAConflictDirectory(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "p/y", "y\n")
	git("add", ".")
	git("commit", "-q", "-m", "dir")
	git("checkout", "-q", "main")
	writeFile(t, root, "p", "file\n")
	git("add", ".")
	git("commit", "-q", "-m", "file")
	gitTry(t, root, "merge", "other")
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil && !os.IsExist(err) {
		t.Skip("merge left p as a file")
	}
	if fi, err := os.Lstat(filepath.Join(root, "p")); err != nil || !fi.IsDir() {
		t.Skip("merge left p as a file")
	}
	nestedRepo(t, filepath.Join(root, "p", "nested"))
	st := operationOf(t, e, root)
	if st.Kind != "merge" || len(st.NestedInTheWay) == 0 {
		t.Fatalf("nested in a file/directory conflict: %+v", st.NestedInTheWay)
	}
	_, err := e.command(client.GitOperationAbortCommand("go", gitTarget, st, true, true))
	wantGitCode(t, err, "not_supported")
	if readText(t, filepath.Join(root, "p", "nested"), "precious") != "unpushed\n" {
		t.Fatal("nested repository content removed")
	}
}

func TestGitSequenceAbortCountsDroppedCommitsExactly(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "seq.txt", "0\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	long := strings.Repeat("s", 100<<10)
	for i := range 205 {
		writeFile(t, root, "seq.txt", fmt.Sprintf("%d\n", i+1))
		subject := fmt.Sprintf("step %d", i)
		if i < 4 {
			subject = long + subject
		}
		git("commit", "-q", "-am", subject)
	}
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other c")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main c")
	gitTry(t, root, "cherry-pick", "main..other")
	st := operationOf(t, e, root)
	if st.AbortDropsCount != 205 || len(st.AbortDropsCommits) != 200 || !st.AbortDropsIncomplete || st.AbortDropsFingerprint == "" {
		t.Fatalf("dropped: count %d listed %d incomplete %v", st.AbortDropsCount, len(st.AbortDropsCommits), st.AbortDropsIncomplete)
	}
	if s := st.AbortDropsCommits[len(st.AbortDropsCommits)-1].Subject; len(s) > gitOperationSubjectMax+4 {
		t.Fatalf("unbounded subject: %d bytes", len(s))
	}
	_, err := e.command(client.GitOperationAbortCommand("ab-unack", gitTarget, st, true, false))
	wantGitCode(t, err, "drops_unacknowledged")
	start := strings.TrimSpace(readText(t, root, ".git/sequencer/head"))
	mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != start {
		t.Fatal("abort did not return to the sequence start")
	}
}

func TestGitContinueChecksFilesTheRemainingCommitsModify(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "p", "p\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	writeFile(t, root, "c.txt", "feature\n")
	git("commit", "-q", "-am", "f1")
	writeFile(t, root, "p", "p changed\n")
	git("commit", "-q", "-am", "f2 modifies p")
	git("checkout", "-q", "main")
	git("rm", "-q", "p")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main deletes p")
	git("checkout", "-q", "feature")
	pv := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), pv, false), protocol.GitStateSucceeded)
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "p", "ignored user file\n")
	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	if !slices.Equal(st.ContinueInTheWay, []string{"p"}) {
		t.Fatalf("modify/delete path not checked: %v", st.ContinueInTheWay)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont", gitTarget, st, false))
	wantGitCode(t, err, "would_overwrite")
}

func TestGitBackupsArePrunedWithTheirRepository(t *testing.T) {
	s := protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: "/repo"}}}
	putGitBackup(&s, protocol.GitBackupEntry{Checkout: "/repo", CheckoutKey: checkoutKey("/repo"), Oid: "a"})
	putGitBackup(&s, protocol.GitBackupEntry{Checkout: "/gone", CheckoutKey: checkoutKey("/gone"), Oid: "b"})
	pruneGitBackups(&s)
	if len(s.GitBackups) != 1 || s.GitBackups[0].Oid != "a" {
		t.Fatalf("pruned: %+v", s.GitBackups)
	}
	for i := range gitBackupsMax + 50 {
		key := fmt.Sprintf("/repo%d", i)
		putGitBackup(&s, protocol.GitBackupEntry{Checkout: key, CheckoutKey: checkoutKey(key), Oid: key})
	}
	if len(s.GitBackups) != gitBackupsMax {
		t.Fatalf("total bound: %d", len(s.GitBackups))
	}
}

func TestGitAbortLeftoverPathsAreReported(t *testing.T) {
	_, root, git := conflictSetup(t)
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if left := leftoverPaths(context.Background(), g, []string{"c.txt"}); len(left) != 0 {
		t.Fatalf("clean: %v", left)
	}
	writeFile(t, root, "c.txt", "left behind\n")
	git("status")
	if left := leftoverPaths(context.Background(), g, []string{"c.txt", "d.txt"}); !slices.Equal(left, []string{"c.txt"}) {
		t.Fatalf("leftover: %v", left)
	}
}

// Fifth review 2026-09-25.

func TestGitContinueChecksPendingMergeCommitPicks(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "side")
	writeFile(t, root, "added_by_merge", "s\n")
	git("add", ".")
	git("commit", "-q", "-m", "side")
	git("checkout", "-q", "-b", "feat", "main")
	writeFile(t, root, "x", "x\n")
	git("add", ".")
	git("commit", "-q", "-m", "x")
	git("merge", "-q", "--no-ff", "side", "-m", "M")
	m := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "src", "main")
	writeFile(t, root, "c", "other\n")
	git("commit", "-q", "-am", "other c")
	c := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "tgt", "main")
	writeFile(t, root, "c", "tgt\n")
	git("commit", "-q", "-am", "tgt c")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("added_by_merge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "added_by_merge", "PRECIOUS\n")
	gitTry(t, root, "cherry-pick", "-m", "1", c, m)
	writeFile(t, root, "c", "resolved\n")
	git("add", "c")
	st := operationOf(t, e, root)
	if !slices.Equal(st.ContinueInTheWay, []string{"added_by_merge"}) || st.Can.Continue.Allowed {
		t.Fatalf("pending merge pick: %v unchecked=%v", st.ContinueInTheWay, st.ContinueUnchecked)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont", gitTarget, st, false))
	wantGitCode(t, err, "would_overwrite")
	if readText(t, root, "added_by_merge") != "PRECIOUS\n" {
		t.Fatal("ignored file overwritten")
	}
}

func TestGitAbortLeavesASubmoduleWorktreeAlone(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, os.Getenv("HOME"), ".gitconfig", "[protocol \"file\"]\n\tallow = always\n")
	sub := filepath.Join(t.TempDir(), "sub")
	nestedRepo(t, sub)
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	writeFile(t, root, "c.txt", "feature\n")
	git("commit", "-q", "-am", "f c")
	git("submodule", "add", "-q", sub, "sm")
	git("commit", "-q", "-m", "add sm")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main c")
	git("checkout", "-q", "feature")
	pv := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), pv, false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if len(st.NestedOnAbort) != 0 || !st.Can.Abort.Allowed {
		t.Fatalf("submodule treated as in the way: %v %+v", st.NestedOnAbort, st.Can.Abort)
	}
	mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if readText(t, filepath.Join(root, "sm"), "precious") != "unpushed\n" {
		t.Fatal("submodule worktree changed")
	}
}

func TestGitLargeUpstreamChangeStillAllowsAbortAndSkip(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	for i := range 1100 {
		writeFile(t, root, filepath.Join("many", fmt.Sprint(i)), "0\n")
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	writeFile(t, root, "c.txt", "feature\n")
	git("commit", "-q", "-am", "f c")
	git("checkout", "-q", "main")
	for i := range 1100 {
		writeFile(t, root, filepath.Join("many", fmt.Sprint(i)), "1\n")
	}
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main many")
	git("checkout", "-q", "feature")
	pv := previewOf(t, root, "rebase", "refs/heads/main")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), pv, false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if st.DiscardsOnAbortIncomplete || st.DiscardsOnSkipIncomplete || !st.Can.Skip.Allowed || !st.Can.Abort.Allowed {
		t.Fatalf("large upstream: abort %v skip %v %+v", st.DiscardsOnAbortIncomplete, st.DiscardsOnSkipIncomplete, st.Can)
	}
}

func TestGitContinueLeavesUpdateRefsRebasesToATerminal(t *testing.T) {
	e, root, git := conflictSetup(t)
	git("checkout", "-q", "other")
	git("branch", "stack", "HEAD~1")
	gitTry(t, root, "rebase", "--update-refs", "main")
	writeFile(t, root, "c.txt", "resolved\n")
	git("add", "c.txt")
	st := operationOf(t, e, root)
	if !slices.Equal(st.ContinueUpdatesRefs, []string{"refs/heads/stack"}) || st.Can.Continue.Allowed || !strings.Contains(st.Can.Continue.Reason, "refs/heads/stack") {
		t.Fatalf("update-refs: %v %+v", st.ContinueUpdatesRefs, st.Can.Continue)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont", gitTarget, st, false))
	wantGitCode(t, err, "not_supported")
}
