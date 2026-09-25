package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// refSetup is gitWriteSetup with two commits on main.
func refSetup(t *testing.T) (*engine, string, func(...string) string) {
	t.Helper()
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a1\n")
	writeFile(t, root, "shared.txt", "shared\n")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	writeFile(t, root, "a.txt", "a2\n")
	git("commit", "-q", "-am", "two")
	return e, root, git
}

func readText(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGitBranchCreateFromSelectedCommit(t *testing.T) {
	e, root, git := refSetup(t)
	first := git("rev-parse", "HEAD~1")
	head := git("rev-parse", "HEAD")
	cmd := client.GitBranchCreateCommand("create-feature", gitTarget, "feature/x", first)
	r := mustGit(t, e, cmd, protocol.GitStateSucceeded)
	if git("rev-parse", "refs/heads/feature/x") != first || git("rev-parse", "HEAD") != head || git("branch", "--show-current") != "main" {
		t.Fatal("branch not created at the selected commit, or HEAD moved")
	}
	if r.Git.Ref == nil || r.Git.Ref.Branch != "feature/x" || r.Git.Ref.Head != first {
		t.Fatalf("ref result: %+v", r.Git.Ref)
	}
	if out, err := gitRun(root, "config", "--get-regexp", `^branch\.feature/x\.`); err == nil {
		t.Fatalf("tracking configured: %q", out)
	}
	// A duplicate ID returns the recorded receipt without running again.
	again, err := e.command(cmd)
	if err != nil || again.Git.State != protocol.GitStateSucceeded || again.Revision != r.Revision {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	dup := client.GitBranchCreateCommand("create-again", gitTarget, "feature/x", head)
	_, err = e.command(dup)
	wantGitCode(t, err, "branch_exists")
	notRecorded(t, e, dup)
	for _, bad := range []string{"-f", "@{-1}", "a..b", "HEAD", "bad name", "x.lock"} {
		_, err := e.command(client.GitBranchCreateCommand("bad-"+bad, gitTarget, bad, head))
		wantGitCode(t, err, "invalid")
	}
	_, err = e.command(client.GitBranchCreateCommand("unknown", gitTarget, "nowhere", "0123456789012345678901234567890123456789"))
	wantGitCode(t, err, "unknown_commit")
	// Branch creation takes no lease: it runs while a thread works.
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "running"
	e.mu.Unlock()
	mustGit(t, e, client.GitBranchCreateCommand("create-busy", gitTarget, "during-turn", head), protocol.GitStateSucceeded)
}

func TestGitSwitchCarriesAcknowledgedChanges(t *testing.T) {
	e, root, git := refSetup(t)
	git("branch", "other")
	git("checkout", "-q", "other")
	writeFile(t, root, "a.txt", "on other\n")
	git("commit", "-q", "-am", "other")
	otherTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	mainTip := git("rev-parse", "HEAD")

	// A clean switch.
	st := mustStatus(t, root)
	r := mustGit(t, e, client.GitSwitchCommand("sw-clean", gitTarget, st, "other", otherTip, false), protocol.GitStateSucceeded)
	if git("branch", "--show-current") != "other" || r.Git.Ref.Branch != "other" || r.Git.Ref.PreviousBranch != "main" || r.Git.Ref.Head != otherTip || r.Git.Ref.PreviousHead != mainTip {
		t.Fatalf("clean switch: %+v", r.Git.Ref)
	}
	// Changes Git can carry need the count the user saw.
	writeFile(t, root, "shared.txt", "local edit\n")
	writeFile(t, root, "new.txt", "untracked\n")
	st = mustStatus(t, root)
	if len(st.Entries) != 2 {
		t.Fatalf("entries: %+v", st.Entries)
	}
	unacked := client.GitSwitchCommand("sw-unacked", gitTarget, st, "main", mainTip, false)
	_, err := e.command(unacked)
	wantGitCode(t, err, "carry_unacknowledged")
	notRecorded(t, e, unacked)
	wrongCount := client.GitSwitchCommand("sw-count", gitTarget, st, "main", mainTip, true)
	wrongCount.Git.Ref.AcknowledgeCarry = 1
	_, err = e.command(wrongCount)
	wantGitCode(t, err, "carry_unacknowledged")
	// The working tree changed after review.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, root, "new.txt", "untracked, edited\n")
	_, err = e.command(client.GitSwitchCommand("sw-stale", gitTarget, st, "main", mainTip, true))
	wantGitCode(t, err, "stale_status")
	st = mustStatus(t, root)
	r = mustGit(t, e, client.GitSwitchCommand("sw-carry", gitTarget, st, "main", mainTip, true), protocol.GitStateSucceeded)
	if git("branch", "--show-current") != "main" || r.Git.Ref.Carried != 2 || readText(t, root, "shared.txt") != "local edit\n" || readText(t, root, "new.txt") != "untracked, edited\n" {
		t.Fatalf("carry: %+v", r.Git.Ref)
	}
	// A change Git would overwrite stops the switch before anything moves.
	writeFile(t, root, "a.txt", "conflicting local edit\n")
	st = mustStatus(t, root)
	r = mustGit(t, e, client.GitSwitchCommand("sw-overwrite", gitTarget, st, "other", otherTip, true), protocol.GitStateFailed)
	if r.Git.Code != "would_overwrite" || !slices.Equal(r.Git.Paths, []string{"a.txt"}) || git("branch", "--show-current") != "main" || readText(t, root, "a.txt") != "conflicting local edit\n" {
		t.Fatalf("would overwrite: %+v", r.Git)
	}
	if out := git("stash", "list"); out != "" {
		t.Fatalf("stashed: %s", out)
	}
}

func TestGitSwitchNeverOverwritesIgnoredFiles(t *testing.T) {
	e, root, git := refSetup(t)
	git("checkout", "-q", "-b", "build")
	writeFile(t, root, "build.out", "tracked on build\n")
	git("add", "build.out")
	git("commit", "-q", "-m", "tracked build output")
	buildTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	writeFile(t, root, ".git/info/exclude", "build.out\n")
	writeFile(t, root, "build.out", "precious ignored output\n")
	st := mustStatus(t, root)
	if len(st.Entries) != 0 {
		t.Fatalf("ignored file listed: %+v", st.Entries)
	}
	r := mustGit(t, e, client.GitSwitchCommand("sw-ignored", gitTarget, st, "build", buildTip, false), protocol.GitStateFailed)
	if r.Git.Code != "would_overwrite" || !slices.Equal(r.Git.Paths, []string{"build.out"}) || readText(t, root, "build.out") != "precious ignored output\n" {
		t.Fatalf("ignored overwrite: %+v", r.Git)
	}
}

func TestGitSwitchRefusals(t *testing.T) {
	e, root, git := refSetup(t)
	head := git("rev-parse", "HEAD")
	git("branch", "side")
	st := mustStatus(t, root)
	_, err := e.command(client.GitSwitchCommand("sw-same", gitTarget, st, "main", head, false))
	wantGitCode(t, err, "already_on_branch")
	// The target branch moved after it was shown.
	_, err = e.command(client.GitSwitchCommand("sw-moved", gitTarget, st, "side", git("rev-parse", "HEAD~1"), false))
	wantGitCode(t, err, "stale_target")
	// HEAD moved after status.
	stale := st
	stale.HeadOid = git("rev-parse", "HEAD~1")
	_, err = e.command(client.GitSwitchCommand("sw-stale-head", gitTarget, stale, "side", head, false))
	wantGitCode(t, err, "stale_head")
	// Checked out in another worktree.
	wt := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-q", wt, "side")
	_, err = e.command(client.GitSwitchCommand("sw-elsewhere", gitTarget, st, "side", head, false))
	wantGitCode(t, err, "checked_out_elsewhere")
	git("worktree", "remove", "--force", wt)
	// Busy checkout: a switch takes the lease.
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "waiting"
	e.mu.Unlock()
	_, err = e.command(client.GitSwitchCommand("sw-busy", gitTarget, st, "side", head, false))
	wantGitCode(t, err, "checkout_busy")
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "idle"
	e.mu.Unlock()
	// During a merge, and with conflicted paths.
	git("checkout", "-q", "side")
	writeFile(t, root, "a.txt", "side\n")
	git("commit", "-q", "-am", "side")
	git("checkout", "-q", "main")
	writeFile(t, root, "a.txt", "main\n")
	git("commit", "-q", "-am", "main")
	if _, err := gitRun(root, "merge", "side"); err == nil {
		t.Fatal("merge did not conflict")
	}
	st = mustStatus(t, root)
	_, err = e.command(client.GitSwitchCommand("sw-merge", gitTarget, st, "side", git("rev-parse", "side"), true))
	wantGitCode(t, err, "operation_in_progress")
	_, err = e.command(client.GitResetSoftCommand("reset-merge", gitTarget, st, head, true, true))
	wantGitCode(t, err, "operation_in_progress")
}

func TestGitSwitchCreate(t *testing.T) {
	e, root, git := refSetup(t)
	first := git("rev-parse", "HEAD~1")
	writeFile(t, root, "new.txt", "carried\n")
	st := mustStatus(t, root)
	r := mustGit(t, e, client.GitSwitchCreateCommand("sw-create", gitTarget, st, "topic", first, true), protocol.GitStateSucceeded)
	if git("branch", "--show-current") != "topic" || git("rev-parse", "HEAD") != first || r.Git.Ref.Branch != "topic" || readText(t, root, "new.txt") != "carried\n" {
		t.Fatalf("switch-create: %+v", r.Git.Ref)
	}
	st = mustStatus(t, root)
	_, err := e.command(client.GitSwitchCreateCommand("sw-create-again", gitTarget, st, "main", first, true))
	wantGitCode(t, err, "branch_exists")
}

func TestGitResetSoftAndUndo(t *testing.T) {
	e, root, git := refSetup(t)
	writeFile(t, root, "c.txt", "c\n")
	git("add", "c.txt")
	git("commit", "-q", "-m", "three")
	top := git("rev-parse", "HEAD")
	first := git("rev-parse", "HEAD~2")
	writeFile(t, root, "staged.txt", "staged\n")
	git("add", "staged.txt")
	writeFile(t, root, "c.txt", "c unstaged edit\n")
	indexBefore := git("ls-files", "--stage")

	st := mustStatus(t, root)
	_, err := e.command(client.GitResetSoftCommand("reset-same", gitTarget, st, top, false, false))
	wantGitCode(t, err, "already_at_target")
	r := mustGit(t, e, client.GitResetSoftCommand("reset-1", gitTarget, st, first, false, false), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != first || git("branch", "--show-current") != "main" || r.Git.Ref.PreviousHead != top || r.Git.Ref.Head != first || r.Git.Commit != first {
		t.Fatalf("reset: %+v", r.Git.Ref)
	}
	if git("ls-files", "--stage") != indexBefore || readText(t, root, "c.txt") != "c unstaged edit\n" {
		t.Fatal("soft reset changed the index or files")
	}
	if git("rev-parse", "ORIG_HEAD") != top {
		t.Fatal("ORIG_HEAD not recorded")
	}
	// Formerly committed changes are now staged against the new HEAD.
	if s := mustStatus(t, root); hasEntry(s, "c.txt", "staged") == nil || hasEntry(s, "a.txt", "staged") == nil || hasEntry(s, "staged.txt", "staged") == nil {
		t.Fatalf("staged comparison: %+v", s.Entries)
	}
	undo, ok := client.GitUndoResetSoftCommand("undo-1", gitTarget, *r.Git)
	if !ok {
		t.Fatal("undo not offered")
	}
	mustGit(t, e, undo, protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != top || git("ls-files", "--stage") != indexBefore {
		t.Fatal("undo did not restore the branch")
	}
	if s := mustStatus(t, root); hasEntry(s, "c.txt", "staged") != nil || hasEntry(s, "staged.txt", "staged") == nil {
		t.Fatalf("after undo: %+v", s.Entries)
	}
	// A stale undo is refused.
	_, err = e.command(undo)
	if err != nil {
		t.Fatalf("retry of the same undo must return its receipt: %v", err)
	}
	undo.ID = "undo-again"
	_, err = e.command(undo)
	wantGitCode(t, err, "stale_head")
}

func TestGitResetSoftAcknowledgements(t *testing.T) {
	e, root, git := refSetup(t)
	head := git("rev-parse", "HEAD")
	first := git("rev-parse", "HEAD~1")
	// Publish HEAD through a remote-tracking ref (local refs only).
	git("update-ref", "refs/remotes/origin/main", head)
	st := mustStatus(t, root)
	_, err := e.command(client.GitResetSoftCommand("reset-pub", gitTarget, st, first, false, false))
	wantGitCode(t, err, "published_commit")
	mustGit(t, e, client.GitResetSoftCommand("reset-pub-ack", gitTarget, st, first, true, false), protocol.GitStateSucceeded)
	// Moving forward again (to a descendant) needs no acknowledgement.
	mustGit(t, e, client.GitResetSoftCommand("reset-forward", gitTarget, mustStatus(t, root), head, false, false), protocol.GitStateSucceeded)
	git("update-ref", "-d", "refs/remotes/origin/main")
	// A target outside the branch's history.
	git("checkout", "-q", "-b", "side", first)
	writeFile(t, root, "side.txt", "side\n")
	git("add", "side.txt")
	git("commit", "-q", "-m", "side")
	side := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	st = mustStatus(t, root)
	_, err = e.command(client.GitResetSoftCommand("reset-side", gitTarget, st, side, false, false))
	wantGitCode(t, err, "not_ancestor")
	mustGit(t, e, client.GitResetSoftCommand("reset-side-ack", gitTarget, st, side, false, true), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != side || git("branch", "--show-current") != "main" {
		t.Fatal("not-ancestor reset")
	}
}

func TestGitResetSoftUnbornAndValidation(t *testing.T) {
	e, root, _ := gitWriteSetup(t)
	st := mustStatus(t, root)
	_, err := e.command(client.GitResetSoftCommand("reset-unborn", gitTarget, st, "0123456789012345678901234567890123456789", false, false))
	wantGitCode(t, err, "nothing_to_reset")
	for name, w := range map[string]protocol.GitWrite{
		"no payload":    {},
		"both payloads": {Ref: &protocol.GitRefWrite{Name: "x", StartOid: "0123456789012345678901234567890123456789"}, Sync: &protocol.GitSync{}},
		"legacy field":  {Ref: &protocol.GitRefWrite{Name: "x", StartOid: "0123456789012345678901234567890123456789"}, Message: "m"},
		"short oid":     {Ref: &protocol.GitRefWrite{Name: "x", StartOid: "0123abc"}},
	} {
		_, err := e.command(protocol.Command{Version: protocol.Version, ID: "v-" + name, Kind: protocol.GitKindBranchCreate, ProjectID: "p-git", Git: &w})
		wantGitCode(t, err, "invalid")
	}
	_, err = e.command(protocol.Command{Version: protocol.Version, ID: "v-sync-on-stage", Kind: protocol.GitKindStage, ProjectID: "p-git",
		Git: &protocol.GitWrite{Paths: []protocol.GitPathPin{{Path: "a", Group: "untracked", Pin: "x"}}, Sync: &protocol.GitSync{}}})
	wantGitCode(t, err, "invalid")
}
