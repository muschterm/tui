package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

var gitTarget = client.GitTarget{ProjectID: "p-git"}

// gitWriteSetup is an engine with a project and an idle fixture thread on a
// fresh repository whose local config supplies the committer identity.
func gitWriteSetup(t *testing.T) (*engine, string, func(...string) string) {
	t.Helper()
	root, git := gitFixture(t)
	git("init")
	git("config", "user.name", "Writer")
	git("config", "user.email", "writer@example.invalid")
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	e := testEngine(t)
	addGitThread(e, "p-git", "t-git", root)
	return e, root, git
}

func addGitThread(e *engine, projectID, threadID, dir string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	base := e.snap.Threads[0]
	if !strings.HasPrefix(threadID, "t-extra") {
		e.snap.Projects = append(e.snap.Projects, protocol.Project{ID: projectID, Name: projectID, Path: dir, Revision: 1})
	}
	e.snap.Threads = append(e.snap.Threads, protocol.Thread{ProjectID: projectID, ID: threadID, Title: threadID, Checkout: dir, Agent: base.Agent, State: "idle", Selected: base.Selected, Effective: base.Effective, QueueRevision: 1})
}

func entryOf(t *testing.T, root, path, group string) protocol.GitStatusEntry {
	t.Helper()
	s := mustStatus(t, root)
	e := hasEntry(s, path, group)
	if e == nil {
		t.Fatalf("no %s entry for %q in %+v", group, path, s.Entries)
	}
	if e.Pin == "" {
		t.Fatalf("entry %q has no pin", path)
	}
	return *e
}

func wantGitCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func mustGit(t *testing.T, e *engine, c protocol.Command, state string) protocol.Receipt {
	t.Helper()
	r, err := e.command(c)
	if err != nil {
		t.Fatalf("%s: %v", c.Kind, err)
	}
	if r.Git == nil || r.State != state || r.Git.State != state {
		t.Fatalf("%s: want %s, got %+v %+v", c.Kind, state, r, r.Git)
	}
	return r
}

func commitCmd(t *testing.T, root, id, message string, amend, ack bool) protocol.Command {
	return client.GitCommitCommand(id, gitTarget, mustStatus(t, root), message, amend, ack)
}

func writeHook(t *testing.T, root, name, script string) {
	t.Helper()
	p := filepath.Join(root, ".git", "hooks", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func notRecorded(t *testing.T, e *engine, c protocol.Command) {
	t.Helper()
	if r, err := e.store.Lookup(c); err != nil || r != nil {
		t.Fatalf("refused command was recorded: %+v %v", r, err)
	}
}

func TestGitWriteStageUnstageCommitCycle(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	for _, name := range []string{"mod.txt", "gone.txt", "old.txt"} {
		writeFile(t, root, name, name+"\n")
	}
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "mod.txt", "changed\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "new.txt", "fresh\n")
	git("mv", "old.txt", "renamed.txt")

	for i, p := range []struct{ path, group string }{{"mod.txt", "unstaged"}, {"gone.txt", "unstaged"}, {"new.txt", "untracked"}} {
		r := mustGit(t, e, client.GitStageCommand("stage-"+p.path, gitTarget, entryOf(t, root, p.path, p.group)), protocol.GitStateSucceeded)
		if r.Git.Code != "" || r.TargetID != root {
			t.Fatalf("stage %d: %+v", i, r.Git)
		}
	}
	s := mustStatus(t, root)
	for _, p := range []string{"mod.txt", "gone.txt", "new.txt", "renamed.txt"} {
		if hasEntry(s, p, "staged") == nil {
			t.Fatalf("%s not staged: %+v", p, s.Entries)
		}
	}
	if e := hasEntry(s, "gone.txt", "staged"); e.Index != "D" {
		t.Fatalf("deletion not staged: %+v", e)
	}
	// Unstaging the rename restores both sides.
	mustGit(t, e, client.GitUnstageCommand("unstage-rename", gitTarget, entryOf(t, root, "renamed.txt", "staged")), protocol.GitStateSucceeded)
	s = mustStatus(t, root)
	if hasEntry(s, "renamed.txt", "staged") != nil || hasEntry(s, "old.txt", "unstaged") == nil || hasEntry(s, "renamed.txt", "untracked") == nil {
		t.Fatalf("rename not fully unstaged: %+v", s.Entries)
	}
	r := mustGit(t, e, commitCmd(t, root, "commit-1", "Second\n\n# kept\n", false, false), protocol.GitStateSucceeded)
	if r.Git.Commit != git("rev-parse", "HEAD") || r.Git.Code != "" {
		t.Fatalf("commit: %+v", r.Git)
	}
	if msg := git("log", "-1", "--format=%B"); msg != "Second\n\n# kept" {
		t.Fatalf("message cleanup: %q", msg)
	}
	if files := git("show", "--name-status", "--format=", "HEAD"); !strings.Contains(files, "D\tgone.txt") || !strings.Contains(files, "A\tnew.txt") || !strings.Contains(files, "M\tmod.txt") {
		t.Fatalf("committed files: %s", files)
	}
	snap := e.current()
	if len(snap.GitOps) != 1 || snap.GitOps[0].CommandID != "commit-1" || snap.GitOps[0].State != protocol.GitStateSucceeded || snap.GitOps[0].Commit != r.Git.Commit || snap.GitOps[0].Checkout != root {
		t.Fatalf("git ops: %+v", snap.GitOps)
	}
	if len(e.git.holders) != 0 {
		t.Fatal("lease not released")
	}
}

func TestGitWriteUnbornCommitAndUnstage(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	writeFile(t, root, "b.txt", "b\n")
	git("add", ".")
	writeFile(t, root, "b.txt", "b edited after staging\n")
	st := mustStatus(t, root)
	if st.HeadOid != "" || st.StagedFingerprint == "" {
		t.Fatalf("unborn status: %+v", st)
	}
	mustGit(t, e, client.GitUnstageCommand("unstage-b", gitTarget, entryOf(t, root, "b.txt", "staged")), protocol.GitStateSucceeded)
	if s := mustStatus(t, root); hasEntry(s, "b.txt", "untracked") == nil || hasEntry(s, "a.txt", "staged") == nil {
		t.Fatalf("unborn unstage: %+v", s.Entries)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(b) != "b edited after staging\n" {
		t.Fatal("unstage touched the worktree")
	}
	_, err := e.command(commitCmd(t, root, "amend-unborn", "x", true, false))
	wantGitCode(t, err, "nothing_to_amend")
	r := mustGit(t, e, commitCmd(t, root, "first", "First", false, false), protocol.GitStateSucceeded)
	if r.Git.Commit != git("rev-parse", "HEAD") || git("show", "--name-only", "--format=", "HEAD") != "a.txt" {
		t.Fatalf("first commit: %+v", r.Git)
	}
}

func TestGitWriteStalePinsAreRefusedUnrecorded(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", "one\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", "two\n")
	entry := entryOf(t, root, "f.txt", "unstaged")
	time.Sleep(10 * time.Millisecond)
	writeFile(t, root, "f.txt", "three\n")
	stage := client.GitStageCommand("stale-stage", gitTarget, entry)
	_, err := e.command(stage)
	wantGitCode(t, err, "stale_entry")
	notRecorded(t, e, stage)
	discard := client.GitDiscardCommand("stale-discard", gitTarget, entry)
	_, err = e.command(discard)
	wantGitCode(t, err, "stale_entry")
	if b, _ := os.ReadFile(filepath.Join(root, "f.txt")); string(b) != "three\n" {
		t.Fatal("stale discard changed the file")
	}
	// Commit pins: a staged change after status, then HEAD moving.
	writeFile(t, root, "g.txt", "g\n")
	git("add", "g.txt")
	cmd := commitCmd(t, root, "stale-status", "msg", false, false)
	git("add", "f.txt")
	_, err = e.command(cmd)
	wantGitCode(t, err, "stale_status")
	cmd = commitCmd(t, root, "stale-head", "msg", false, false)
	git("commit", "-m", "elsewhere")
	_, err = e.command(cmd)
	wantGitCode(t, err, "stale_head")
	notRecorded(t, e, cmd)
	if _, err = e.command(commitCmd(t, root, "nothing", "msg", false, false)); err == nil {
		t.Fatal("empty commit accepted")
	}
	wantGitCode(t, err, "nothing_staged")
	writeFile(t, root, "h.txt", "h\n")
	git("add", "h.txt")
	_, err = e.command(commitCmd(t, root, "blank", " \n\t\n", false, false))
	wantGitCode(t, err, "empty_message")
}

func TestGitWriteStagedNewerContentAndLocalFilterParity(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "base.txt", "base\n")
	git("add", ".")
	git("commit", "-m", "base")
	// A repository-local clean filter runs on add, as it would for the CLI.
	git("config", "filter.up.clean", "tr a-z A-Z")
	writeFile(t, root, ".gitattributes", "*.up filter=up\n*.count filter=count\n")
	writeFile(t, root, "x.up", "lower\n")
	mustGit(t, e, client.GitStageCommand("stage-up", gitTarget, entryOf(t, root, "x.up", "untracked")), protocol.GitStateSucceeded)
	if got := git("cat-file", "-p", ":x.up"); got != "LOWER" {
		t.Fatalf("local clean filter did not run on add: %q", got)
	}
	// A filter whose output changes between the pre-hash and add stands in
	// for content edited during add: the newer bytes are staged and reported.
	counter := filepath.Join(t.TempDir(), "n")
	if err := os.WriteFile(counter, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("config", "filter.count.clean", "cat; n=$(cat "+counter+"); echo $n; echo $((n+1)) > "+counter)
	writeFile(t, root, "y.count", "data\n")
	r := mustGit(t, e, client.GitStageCommand("stage-count", gitTarget, entryOf(t, root, "y.count", "untracked")), protocol.GitStateSucceeded)
	if r.Git.Code != "staged_newer_content" {
		t.Fatalf("want staged_newer_content, got %+v", r.Git)
	}
}

func TestGitWriteAmend(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "original")
	old := git("rev-parse", "HEAD")
	// Message-only amend with nothing staged.
	r := mustGit(t, e, commitCmd(t, root, "amend-msg", "Reworded", true, false), protocol.GitStateSucceeded)
	if r.Git.Commit == old || git("log", "-1", "--format=%s") != "Reworded" || git("rev-list", "--count", "HEAD") != "1" {
		t.Fatalf("message amend: %+v", r.Git)
	}
	// Amend pinned to a HEAD that has since moved is refused.
	cmd := commitCmd(t, root, "amend-stale", "x", true, false)
	git("commit", "--allow-empty", "-m", "moved")
	_, err := e.command(cmd)
	wantGitCode(t, err, "stale_head")
	// Published commits need acknowledgement.
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	if s := mustStatus(t, root); !s.HeadOnUpstream {
		t.Fatal("status did not report HEAD on a remote-tracking ref")
	}
	_, err = e.command(commitCmd(t, root, "amend-published", "x", true, false))
	wantGitCode(t, err, "published_commit")
	writeFile(t, root, "b.txt", "b\n")
	mustGit(t, e, client.GitStageCommand("stage-b", gitTarget, entryOf(t, root, "b.txt", "untracked")), protocol.GitStateSucceeded)
	r = mustGit(t, e, commitCmd(t, root, "amend-ack", "moved plus b", true, true), protocol.GitStateSucceeded)
	if r.Git.Code != "" || git("show", "--name-only", "--format=", "HEAD") != "b.txt" || git("rev-list", "--count", "HEAD") != "2" {
		t.Fatalf("amend with content: %+v %s", r.Git, git("show", "--name-only", "--format=", "HEAD"))
	}
}

func TestGitWriteLocksAreReportedNeverRemoved(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "a.txt", "b\n")
	lock := filepath.Join(root, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := e.command(client.GitStageCommand("locked", gitTarget, entryOf(t, root, "a.txt", "unstaged")))
	wantGitCode(t, err, "index_locked")
	if time.Since(start) < 3*gitLockRetryDelay {
		t.Fatal("lock was not retried")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("index.lock was removed")
	}
	os.Remove(lock)
	git("add", "a.txt")
	head := filepath.Join(root, ".git", "HEAD.lock")
	if err := os.WriteFile(head, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = e.command(commitCmd(t, root, "head-locked", "x", false, false))
	wantGitCode(t, err, "ref_locked")
	if _, err := os.Stat(head); err != nil {
		t.Fatal("HEAD.lock was removed")
	}
}

func TestGitWriteHooks(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "a.txt", "changed\n")
	git("add", "a.txt")
	writeHook(t, root, "pre-commit", "echo 'lint failed: a.txt' >&2\nexit 1\n")
	r := mustGit(t, e, commitCmd(t, root, "rejected", "msg", false, false), protocol.GitStateFailed)
	if r.Git.Code != "commit_failed" || !strings.Contains(r.Git.Output, "lint failed: a.txt") || git("rev-list", "--count", "HEAD") != "1" {
		t.Fatalf("hook failure: %+v", r.Git)
	}
	// The recorded failure is returned again; Git does not rerun.
	if again, err := e.command(commitCmd(t, root, "rejected", "msg", false, false)); err != nil || again.Git.State != protocol.GitStateFailed {
		t.Fatalf("retry: %+v %v", again, err)
	}
	// commit-msg runs, and a pre-commit hook that stages more is reported.
	writeHook(t, root, "pre-commit", "echo extra > extra.txt && git add extra.txt\n")
	writeHook(t, root, "commit-msg", "echo 'Signed-off-by: hook' >> \"$1\"\n")
	r = mustGit(t, e, commitCmd(t, root, "restaged", "Change", false, false), protocol.GitStateSucceeded)
	if r.Git.Code != "hooks_changed_content" {
		t.Fatalf("want hooks_changed_content, got %+v", r.Git)
	}
	if msg := git("log", "-1", "--format=%B"); !strings.Contains(msg, "Signed-off-by: hook") {
		t.Fatalf("commit-msg hook did not run: %q", msg)
	}
}

func TestGitWriteHonorsLocalSigningProgram(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	sentinel := filepath.Join(t.TempDir(), "signed")
	program := filepath.Join(t.TempDir(), "fake-gpg")
	script := "#!/bin/sh\ncat >/dev/null\ntouch " + sentinel + "\nprintf '\\n[GNUPG:] SIG_CREATED D 1 8 00 0 X\\n' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nfake\\n-----END PGP SIGNATURE-----\\n'\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	git("config", "commit.gpgSign", "true")
	git("config", "gpg.program", program)
	r := mustGit(t, e, commitCmd(t, root, "signed", "Signed", false, false), protocol.GitStateSucceeded)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("local gpg.program not used: %+v", r.Git)
	}
	if raw := git("cat-file", "commit", "HEAD"); !strings.Contains(raw, "gpgsig -----BEGIN PGP SIGNATURE-----") {
		t.Fatalf("commit not signed: %s", raw)
	}
}

func TestGitWriteMergeAndConflictRefusals(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-m", "base")
	git("checkout", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-am", "other")
	git("checkout", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-am", "main")
	cmd := exec.Command("git", "-C", root, "-c", "core.hooksPath=/dev/null", "merge", "other")
	if err := cmd.Run(); err == nil {
		t.Fatal("merge unexpectedly clean")
	}
	s := mustStatus(t, root)
	conflict := hasEntry(s, "c.txt", "conflicted")
	if conflict == nil || s.Operation != "merge" {
		t.Fatalf("no conflict: %+v", s)
	}
	_, err := e.command(client.GitStageCommand("stage-conflict", gitTarget, *conflict))
	wantGitCode(t, err, "conflicted")
	// Staging an unrelated path during a merge is allowed; committing is not.
	writeFile(t, root, "side.txt", "side\n")
	mustGit(t, e, client.GitStageCommand("stage-side", gitTarget, entryOf(t, root, "side.txt", "untracked")), protocol.GitStateSucceeded)
	_, err = e.command(commitCmd(t, root, "merge-commit", "x", false, false))
	wantGitCode(t, err, "operation_in_progress")
}

func TestGitWriteSpecialPaths(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "keep.txt", "k\n")
	git("add", ".")
	git("commit", "-m", "base")
	names := []string{"-p", "--output=x", "with space.txt", "new\nline.txt", "ünï ✓.txt", "*glob?.txt", ":(top)magic.txt"}
	writeFile(t, root, "globXmatch.txt", "must stay untracked\n")
	for i, name := range names {
		writeFile(t, root, name, name+"\n")
		mustGit(t, e, client.GitStageCommand("special-"+string(rune('a'+i)), gitTarget, entryOf(t, root, name, "untracked")), protocol.GitStateSucceeded)
	}
	s := mustStatus(t, root)
	for _, name := range names {
		if hasEntry(s, name, "staged") == nil {
			t.Fatalf("%q not staged: %+v", name, s.Entries)
		}
	}
	if hasEntry(s, "globXmatch.txt", "untracked") == nil {
		t.Fatal("a pathspec glob matched another file")
	}
	mustGit(t, e, client.GitUnstageCommand("unstage-dash", gitTarget, entryOf(t, root, "-p", "staged")), protocol.GitStateSucceeded)
	_, err := e.command(client.GitStageCommand("bad", gitTarget, protocol.GitStatusEntry{Path: "../x", Group: "untracked", Pin: "x"}))
	wantGitCode(t, err, "invalid")
}

func TestGitWriteIdentityMissing(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	git("config", "--unset", "user.name")
	git("config", "--unset", "user.email")
	git("config", "user.useConfigOnly", "true")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	s := mustStatus(t, root)
	if s.Identity == nil || !s.Identity.Missing {
		t.Fatalf("identity: %+v", s.Identity)
	}
	_, err := e.command(commitCmd(t, root, "no-identity", "x", false, false))
	wantGitCode(t, err, "identity_missing")
	git("config", "user.name", "Local Name")
	git("config", "user.email", "local@example.invalid")
	if s := mustStatus(t, root); s.Identity.Missing || s.Identity.Email != "local@example.invalid" || s.Identity.EmailScope != "local" {
		t.Fatalf("identity: %+v", s.Identity)
	}
}

func TestGitWriteDuplicateCommandCommitsOnce(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	writeHook(t, root, "pre-commit", "sleep 0.3\n")
	cmd := commitCmd(t, root, "once", "Once", false, false)
	var wg sync.WaitGroup
	receipts := make([]protocol.Receipt, 3)
	for i := range receipts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.command(cmd)
			if err != nil {
				t.Error(err)
			}
			receipts[i] = r
		}()
	}
	wg.Wait()
	if git("rev-list", "--count", "HEAD") != "1" {
		t.Fatal("duplicate command committed twice")
	}
	for _, r := range receipts {
		if r.State != protocol.GitStateSucceeded || r.Git.Commit != receipts[0].Git.Commit {
			t.Fatalf("receipts differ: %+v", receipts)
		}
	}
	later, err := e.command(cmd)
	if err != nil || later.Git.Commit != receipts[0].Git.Commit {
		t.Fatalf("later retry: %+v %v", later, err)
	}
}

func TestGitWriteInterruptedBecomesOutcomeUnknown(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	snap := fixture.Initial()
	snap.Projects = append(snap.Projects, protocol.Project{ID: "p-git", Name: "repo", Path: root, Revision: 1})
	cmd := protocol.Command{Version: 1, ID: "crashed", Kind: protocol.GitKindCommit, ProjectID: "p-git",
		Git: &protocol.GitWrite{Message: "m", ExpectedHead: protocol.GitUnbornHead, StagedFingerprint: "f"}}
	// Phase one was recorded; the server died before phase two.
	putGitOp(&snap, protocol.GitOp{Checkout: root, CommandID: cmd.ID, Op: "commit", State: protocol.GitStateRunning, StartedAt: "now"})
	running := protocol.Receipt{ID: cmd.ID, State: protocol.GitStateRunning, Revision: snap.Revision, Git: &protocol.GitResult{Op: "commit", State: protocol.GitStateRunning}}
	if err := st.Save(snap, &cmd, &running); err != nil {
		t.Fatal(err)
	}
	st.Close()
	c, stop := startTestServer(t, home)
	defer stop()
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.GitOps) != 1 || s.GitOps[0].State != protocol.GitStateOutcomeUnknown || s.GitOps[0].Code != "interrupted" {
		t.Fatalf("recovered op: %+v", s.GitOps)
	}
	r, err := c.GitWrite(context.Background(), cmd)
	if err != nil || r.State != protocol.GitStateOutcomeUnknown || r.Git == nil || r.Git.State != protocol.GitStateOutcomeUnknown || r.Git.Code != "interrupted" {
		t.Fatalf("retry after restart: %+v %v", r, err)
	}
	if out, _ := exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", "HEAD").Output(); len(out) != 0 {
		t.Fatal("retry ran Git")
	}
}

func TestGitWriteRefusedWhileThreadHoldsCheckout(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	sub := filepath.Join(root, "sub")
	writeFile(t, root, "sub/b.txt", "b\n")
	addGitThread(e, "p-sub", "t-sub", sub)
	for _, state := range []string{"running", "waiting"} {
		for _, holder := range []string{"t-git", "t-sub"} {
			e.mu.Lock()
			threadByID(&e.snap, holder).State = state
			e.mu.Unlock()
			for _, target := range []client.GitTarget{gitTarget, {ProjectID: "p-sub"}} {
				_, err := e.command(client.GitStageCommand("busy", target, entryOf(t, root, "a.txt", "untracked")))
				wantGitCode(t, err, "checkout_busy")
				if !strings.Contains(err.Error(), holder) {
					t.Fatalf("holder not named: %v", err)
				}
			}
			e.mu.Lock()
			threadByID(&e.snap, holder).State = "idle"
			e.mu.Unlock()
		}
	}
	git("status")
	mustGit(t, e, client.GitStageCommand("free", gitTarget, entryOf(t, root, "a.txt", "untracked")), protocol.GitStateSucceeded)
}

func TestGitWriteHoldsLeaseAgainstAgentTurns(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	gate := filepath.Join(t.TempDir(), "go")
	writeHook(t, root, "pre-commit", "while [ ! -e "+gate+" ]; do sleep 0.02; done\n")
	addGitThread(e, "p-sub", "t-sub", filepath.Join(root, "sub"))
	cmd := commitCmd(t, root, "slow", "Slow", false, false)
	done := make(chan protocol.Receipt, 1)
	go func() {
		r, err := e.command(cmd)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	waitFor(t, e, "git op running", func(s protocol.Snapshot) bool {
		return len(s.GitOps) == 1 && s.GitOps[0].State == protocol.GitStateRunning
	})
	// A nested project's queued prompt waits for the Git write.
	sendTo(t, e, "t-sub", "blocked-prompt")
	s := e.current()
	if th := threadOf(s, "t-sub"); th.State != "idle" || th.WriterWait == nil || th.WriterWait.HolderGitCommandID != "slow" || th.WriterWait.HolderThreadID != "" {
		t.Fatalf("turn started during Git write: %s %+v", th.State, th.WriterWait)
	}
	// A second Git write in the same repository is refused.
	_, err := e.command(commitCmd(t, root, "second", "x", false, false))
	wantGitCode(t, err, "git_busy")
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.State != protocol.GitStateSucceeded {
		t.Fatalf("slow commit: %+v", r.Git)
	}
	s = e.current()
	if th := threadOf(s, "t-sub"); th.State != "running" || th.WriterWait != nil {
		t.Fatalf("waiter not started after Git write: %s %+v", th.State, th.WriterWait)
	}
}

func TestGitWriteDiscard(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "t.txt", "committed\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "t.txt", "committed\nedit\n")
	entry := entryOf(t, root, "t.txt", "unstaged")
	unconfirmed := client.GitDiscardCommand("unconfirmed", gitTarget, entry)
	unconfirmed.Git.Confirmed = false
	_, err := e.command(unconfirmed)
	wantGitCode(t, err, "confirmation_required")
	mustGit(t, e, client.GitDiscardCommand("discard-t", gitTarget, entry), protocol.GitStateSucceeded)
	if b, _ := os.ReadFile(filepath.Join(root, "t.txt")); string(b) != "committed\n" {
		t.Fatalf("not restored: %q", b)
	}
	// An untracked symlink is removed, never its target.
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	mustGit(t, e, client.GitDiscardCommand("discard-link", gitTarget, entryOf(t, root, "link", "untracked")), protocol.GitStateSucceeded)
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatal("symlink not removed")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "keep" {
		t.Fatal("symlink target touched")
	}
	// A file replaced by a directory after review is stale, and nested
	// repositories are never deleted.
	writeFile(t, root, "u.txt", "u\n")
	u := entryOf(t, root, "u.txt", "untracked")
	os.Remove(filepath.Join(root, "u.txt"))
	os.Mkdir(filepath.Join(root, "u.txt"), 0o755)
	writeFile(t, root, "u.txt/inner", "x\n")
	_, err = e.command(client.GitDiscardCommand("discard-dir", gitTarget, u))
	wantGitCode(t, err, "stale_entry")
	if _, err := os.Stat(filepath.Join(root, "u.txt", "inner")); err != nil {
		t.Fatal("directory contents touched")
	}
	gitIn(t, root, "init", "nested")
	writeFile(t, root, "nested/n.txt", "n\n")
	_, err = e.command(client.GitDiscardCommand("discard-nested", gitTarget, entryOf(t, root, "nested/", "untracked")))
	wantGitCode(t, err, "not_supported")
	// Staged entries cannot be discarded.
	writeFile(t, root, "s.txt", "s\n")
	git("add", "s.txt")
	_, err = e.command(client.GitDiscardCommand("discard-staged", gitTarget, entryOf(t, root, "s.txt", "staged")))
	wantGitCode(t, err, "invalid")
}

func TestGitWriteCancelledCommitLeavesNoLock(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	started := filepath.Join(t.TempDir(), "started")
	writeHook(t, root, "pre-commit", "touch "+started+"\nsleep 60\n")
	done := make(chan protocol.Receipt, 1)
	go func() {
		r, err := e.command(commitCmd(t, root, "cancelled", "x", false, false))
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	e.stopGitWrites()
	r := <-done
	if r.State != protocol.GitStateOutcomeUnknown || r.Git.Code != "cancelled" || time.Since(start) > 4*time.Second {
		t.Fatalf("cancelled commit: %+v after %v", r.Git, time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatal("cancellation stranded index.lock")
	}
	if s := e.current(); s.GitOps[0].State != protocol.GitStateOutcomeUnknown || len(e.git.holders) != 0 {
		t.Fatalf("op after cancel: %+v", s.GitOps)
	}
}

func TestGitWriteCancelledAddReleasesIndexLock(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	dir := t.TempDir()
	// The first run (the server's pre-hash) returns; the second, inside
	// `git add` while it holds index.lock, blocks until cancelled.
	git("config", "filter.slow.clean", "cat; if [ -e "+dir+"/seen ]; then touch "+dir+"/started; sleep 60; fi; touch "+dir+"/seen")
	writeFile(t, root, ".gitattributes", "*.slow filter=slow\n")
	writeFile(t, root, "f.slow", "x\n")
	done := make(chan protocol.Receipt, 1)
	go func() {
		r, err := e.command(client.GitStageCommand("slow-add", gitTarget, entryOf(t, root, "f.slow", "untracked")))
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filter never started inside add")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index.lock")); err != nil {
		t.Fatal("add did not hold index.lock; the test proves nothing")
	}
	e.stopGitWrites()
	if r := <-done; r.State != protocol.GitStateOutcomeUnknown || r.Git.Code != "cancelled" {
		t.Fatalf("cancelled add: %+v", r.Git)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatal("cancellation stranded index.lock")
	}
}

func TestGitWriteDiscardNeverDeletesAReplacementDirectory(t *testing.T) {
	for _, nested := range []bool{false, true} {
		e, root, git := gitWriteSetup(t)
		writeFile(t, root, "a", "hi\n")
		writeFile(t, root, ".gitignore", "a/*.env\n")
		git("add", ".")
		git("commit", "-m", "base")
		os.Remove(filepath.Join(root, "a"))
		writeFile(t, root, "a/prod.env", "SECRET\n")
		writeFile(t, root, "a/notes.txt", "precious\n")
		if nested {
			gitIn(t, root, "init", "a")
		}
		entry := entryOf(t, root, "a", "unstaged")
		_, err := e.command(client.GitDiscardCommand("discard-dir", gitTarget, entry))
		wantGitCode(t, err, "not_supported")
		_, err = e.command(client.GitStageCommand("stage-dir", gitTarget, entry))
		wantGitCode(t, err, "not_supported")
		for _, name := range []string{"a/prod.env", "a/notes.txt"} {
			if _, err := os.Stat(filepath.Join(root, name)); err != nil {
				t.Fatalf("nested=%v: %s lost: %v", nested, name, err)
			}
		}
	}
}

func TestGitWriteDiscardNeverReplacesABlockingParent(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "d/f", "hi\n")
	writeFile(t, root, "s/f", "hi\n")
	git("add", ".")
	git("commit", "-m", "base")
	os.RemoveAll(filepath.Join(root, "d"))
	writeFile(t, root, "d", "precious file d\n")
	os.RemoveAll(filepath.Join(root, "s"))
	outside := t.TempDir()
	writeFile(t, outside, "f", "outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "s")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"d/f", "s/f"} {
		entry := entryOf(t, root, p, "unstaged")
		if entry.WorktreeStat != "blocked" {
			t.Fatalf("%s token %q", p, entry.WorktreeStat)
		}
		_, err := e.command(client.GitDiscardCommand("discard-"+p, gitTarget, entry))
		wantGitCode(t, err, "not_supported")
	}
	if b, err := os.ReadFile(filepath.Join(root, "d")); err != nil || string(b) != "precious file d\n" {
		t.Fatalf("file d lost: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "f")); string(b) != "outside\n" {
		t.Fatal("symlink target touched")
	}
	if fi, err := os.Lstat(filepath.Join(root, "s")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink s replaced")
	}
}

func TestGitWriteTypeChangeAfterStatusIsStale(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a", "hi\n")
	git("add", ".")
	git("commit", "-m", "base")
	os.Remove(filepath.Join(root, "a"))
	entry := entryOf(t, root, "a", "unstaged")
	writeFile(t, root, "a/x", "x\n")
	_, err := e.command(client.GitStageCommand("stage-became-dir", gitTarget, entry))
	wantGitCode(t, err, "stale_entry")
	_, err = e.command(client.GitDiscardCommand("discard-became-dir", gitTarget, entry))
	wantGitCode(t, err, "stale_entry")
	if _, err := os.Stat(filepath.Join(root, "a", "x")); err != nil {
		t.Fatal("directory contents lost")
	}
	if strings.Contains(git("ls-files"), "a/x") {
		t.Fatal("directory contents staged")
	}
}

func TestGitWriteHookBackgroundJobDoesNotFailCommit(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	marker := filepath.Join(t.TempDir(), "survived")
	writeHook(t, root, "post-commit", "(sleep 4; touch "+marker+") &\n")
	r := mustGit(t, e, commitCmd(t, root, "bg", "msg", false, false), protocol.GitStateSucceeded)
	if r.Git.Code != "" || r.Git.Commit != git("rev-parse", "HEAD") {
		t.Fatalf("commit with background hook: %+v", r.Git)
	}
	time.Sleep(4500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook background job outlived the write")
	}
}

func TestGitWriteOpsPurgedWithTheirOwnerAndDeletionWaits(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	gate := filepath.Join(t.TempDir(), "go")
	writeHook(t, root, "pre-commit", "while [ ! -e "+gate+" ]; do sleep 0.02; done\n")
	byThread := client.GitCommitCommand("by-thread", client.GitTarget{ThreadID: "t-git"}, mustStatus(t, root), "m", false, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := e.command(byThread); err != nil {
			t.Error(err)
		}
	}()
	waitFor(t, e, "git op running", func(s protocol.Snapshot) bool { return len(s.GitOps) == 1 })
	th := threadOf(e.current(), "t-git")
	_, err := e.command(protocol.Command{Version: 1, ID: "del-busy", Kind: "thread.delete", ThreadID: "t-git", Revision: th.LifecycleRevision})
	wantGitCode(t, err, "git_busy")
	_, err = e.command(protocol.Command{Version: 1, ID: "remove-busy", Kind: "project.remove", ProjectID: "p-git"})
	wantGitCode(t, err, "git_busy")
	os.WriteFile(gate, nil, 0o644)
	<-done
	if _, err := e.command(protocol.Command{Version: 1, ID: "del", Kind: "thread.delete", ThreadID: "t-git", Revision: th.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	if ops := e.current().GitOps; len(ops) != 0 {
		t.Fatalf("thread-owned op kept: %+v", ops)
	}
	// A project-targeted record goes with its project.
	writeFile(t, root, "b.txt", "b\n")
	mustGit(t, e, client.GitStageCommand("by-project", gitTarget, entryOf(t, root, "b.txt", "untracked")), protocol.GitStateSucceeded)
	if len(e.current().GitOps) != 1 {
		t.Fatal("project op missing")
	}
	e.mu.Lock()
	var rev int64
	for _, p := range e.snap.Projects {
		if p.ID == "p-git" {
			rev = p.Revision
		}
	}
	e.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "remove", Kind: "project.remove", ProjectID: "p-git", Revision: rev}); err != nil {
		t.Fatal(err)
	}
	if ops := e.current().GitOps; len(ops) != 0 {
		t.Fatalf("project-owned op kept: %+v", ops)
	}
}

func TestGitWriteTruncatedStagedSetRefusesCommit(t *testing.T) {
	var st protocol.GitStatus
	out := []byte("# branch.oid " + strings.Repeat("a", 40) + "\x00" +
		"1 M. N... 100644 100644 100644 " + strings.Repeat("1", 40) + " " + strings.Repeat("2", 40) + " a\x00" +
		"1 M. N... 100644 100644 100644 " + strings.Repeat("1", 40) + " " + strings.Repeat("3", 40) + " b\x00")
	parseGitStatus(out, false, 1, &st)
	if !st.StagedTruncated || !strings.HasPrefix(st.StagedFingerprint, "truncated") {
		t.Fatalf("truncated staged set: %+v", st)
	}
	var full protocol.GitStatus
	parseGitStatus(out, false, 0, &full)
	if full.StagedTruncated || strings.HasPrefix(full.StagedFingerprint, "truncated") {
		t.Fatalf("full staged set: %+v", full)
	}
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	cmd := commitCmd(t, root, "truncated", "m", false, false)
	cmd.Git.StagedFingerprint = st.StagedFingerprint
	_, err := e.command(cmd)
	wantGitCode(t, err, "status_truncated")
}

func TestGitWriteUnknownPublicationFailsClosed(t *testing.T) {
	_, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "base")
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if published, unknown := g.headOnUpstream(context.Background(), false); published || unknown {
		t.Fatalf("unpublished HEAD: %v %v", published, unknown)
	}
	// A read that cannot complete is unknown, never "not published".
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, unknown := g.headOnUpstream(ctx, true); !unknown {
		t.Fatal("failed reachability check reported as known")
	}
}

func TestGitWriteOldGitRefusesPartialClone(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("config", "remote.origin.url", "/nonexistent")
	git("config", "remote.origin.promisor", "true")
	saved := gitVersion
	gitVersion = func() ([2]int, error) { return [2]int{2, 43}, nil }
	defer func() { gitVersion = saved }()
	_, err := e.command(client.GitStageCommand("old-partial", gitTarget, entryOf(t, root, "a.txt", "untracked")))
	wantGitCode(t, err, "not_supported")
	gitVersion = func() ([2]int, error) { return [2]int{2, 44}, nil }
	mustGit(t, e, client.GitStageCommand("new-partial", gitTarget, entryOf(t, root, "a.txt", "untracked")), protocol.GitStateSucceeded)
}

func TestGitWritePanicReleasesTheLease(t *testing.T) {
	r := safeRun(context.Background(), &gitPlan{run: func(context.Context) protocol.GitResult { panic("boom") }})
	if r.State != protocol.GitStateOutcomeUnknown || r.Code != "internal_error" {
		t.Fatalf("panic result: %+v", r)
	}
	if _, err := safePrepare(context.Background(), nil, nil, protocol.Command{Kind: protocol.GitKindStage, Git: &protocol.GitWrite{}}); err == nil {
		t.Fatal("prepare panic not converted")
	}
}

func TestGitWriteOutcomeKeptWhenFinalSaveFails(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	gate := filepath.Join(t.TempDir(), "go")
	writeHook(t, root, "pre-commit", "while [ ! -e "+gate+" ]; do sleep 0.02; done\n")
	cmd := commitCmd(t, root, "unsaved", "m", false, false)
	done := make(chan protocol.Receipt, 1)
	go func() {
		r, err := e.command(cmd)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	waitFor(t, e, "git op running", func(s protocol.Snapshot) bool { return len(s.GitOps) == 1 })
	e.store.Close() // every later save fails
	os.WriteFile(gate, nil, 0o644)
	r := <-done
	if r.State != protocol.GitStateSucceeded || r.Git.Commit != git("rev-parse", "HEAD") {
		t.Fatalf("final outcome: %+v", r.Git)
	}
	again, err := e.command(cmd)
	if err != nil || again.State != protocol.GitStateSucceeded || again.Git.Commit != r.Git.Commit {
		t.Fatalf("retry after failed save: %+v %v", again, err)
	}
	if git("rev-list", "--count", "HEAD") != "1" {
		t.Fatal("retry committed again")
	}
}

func TestGitWriteFileDirectoryReplacementNeverDropsStagedContent(t *testing.T) {
	// Untracked file a would replace staged-only a/b.
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a/b", "head\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "a/b", "STAGED-ONLY\n")
	git("add", "a/b")
	before := git("ls-files", "-s")
	os.RemoveAll(filepath.Join(root, "a"))
	writeFile(t, root, "a", "file now\n")
	_, err := e.command(client.GitStageCommand("stage-a", gitTarget, entryOf(t, root, "a", "untracked")))
	wantGitCode(t, err, "not_supported")
	if git("ls-files", "-s") != before {
		t.Fatal("index changed")
	}

	// Untracked a/x would replace staged-only file a.
	e, root, git = gitWriteSetup(t)
	writeFile(t, root, "a", "head\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "a", "STAGED-ONLY\n")
	git("add", "a")
	before = git("ls-files", "-s")
	os.Remove(filepath.Join(root, "a"))
	writeFile(t, root, "a/x", "x\n")
	_, err = e.command(client.GitStageCommand("stage-ax", gitTarget, entryOf(t, root, "a/x", "untracked")))
	wantGitCode(t, err, "not_supported")
	if git("ls-files", "-s") != before {
		t.Fatal("index changed")
	}

	// Unstaging file a would also restore HEAD's a/b.
	e, root, git = gitWriteSetup(t)
	writeFile(t, root, "a/b", "head\n")
	git("add", ".")
	git("commit", "-m", "base")
	os.RemoveAll(filepath.Join(root, "a"))
	writeFile(t, root, "a", "file now\n")
	git("add", "-A")
	before = git("ls-files", "-s")
	_, err = e.command(client.GitUnstageCommand("unstage-a", gitTarget, entryOf(t, root, "a", "staged")))
	wantGitCode(t, err, "not_supported")
	if git("ls-files", "-s") != before {
		t.Fatal("index changed")
	}
}

func TestGitWriteIntentToAdd(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "old.txt", "one\ntwo\nthree\nfour\nfive\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "ita.txt", "PRECIOUS\n")
	git("add", "-N", "ita.txt")
	entry := entryOf(t, root, "ita.txt", "unstaged")
	if !entry.IntentToAdd {
		t.Fatalf("intent-to-add not flagged: %+v", entry)
	}
	// Confirmed discard follows Git: the file is emptied.
	mustGit(t, e, client.GitDiscardCommand("discard-ita", gitTarget, entry), protocol.GitStateSucceeded)
	// A worktree rename of an intent-to-add file is refused, not stale forever.
	os.Rename(filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt"))
	writeFile(t, root, "new.txt", "one\ntwo\nthree\nfour\nfive\nUSERWORK\n")
	git("add", "-N", "new.txt")
	renamed := entryOf(t, root, "new.txt", "unstaged")
	if renamed.Worktree != "R" || !renamed.IntentToAdd {
		t.Skipf("git did not pair the intent-to-add rename: %+v", renamed)
	}
	_, err := e.command(client.GitDiscardCommand("discard-ita-rename", gitTarget, renamed))
	wantGitCode(t, err, "not_supported")
	if b, _ := os.ReadFile(filepath.Join(root, "new.txt")); !strings.Contains(string(b), "USERWORK") {
		t.Fatal("renamed intent-to-add file changed")
	}
}

func TestGitWriteStopMarksUnfinishedOps(t *testing.T) {
	e := testEngine(t)
	e.snap.GitOps = []protocol.GitOp{{Checkout: "/x", CommandID: "c", State: protocol.GitStateRunning}}
	e.stopGitWrites()
	if op := e.current().GitOps[0]; op.State != protocol.GitStateOutcomeUnknown || op.Code != "interrupted" {
		t.Fatalf("op after stop: %+v", op)
	}
}
