package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// worktreeSetup is a Git project p-git on main (a.txt, sub/s.txt) with a
// branch other (adds o.txt), and worktree storage in a temporary home.
func worktreeSetup(t *testing.T) (*engine, string, func(...string) string) {
	t.Helper()
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "a\n")
	writeFile(t, root, "sub/s.txt", "s\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("branch", "-M", "main")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "o.txt", "o\n")
	git("add", "o.txt")
	git("commit", "-q", "-m", "other")
	git("checkout", "-q", "main")
	e.worktreeDir = t.TempDir()
	return e, root, git
}

func worktreeStartCommand(e *engine, id, project, branch, oid string) protocol.Command {
	settings := e.current().Threads[0].Selected
	return client.WorktreeStartCommand(id, project, agent.FixtureName, "hello", settings, nil, oid, branch)
}

func mustStartWorktree(t *testing.T, e *engine, c protocol.Command) (protocol.Receipt, *protocol.Thread, protocol.ManagedWorktree) {
	t.Helper()
	r, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "accepted" {
		t.Fatalf("worktree start: %+v %+v", r, r.Error)
	}
	s := e.current()
	th := ptrThread(s, r.TargetID)
	rec := worktreeByID(&s, th.WorktreeID)
	if rec == nil {
		t.Fatalf("thread %s has no worktree record", th.ID)
	}
	return r, th, *rec
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	wantGitCode(t, err, code)
}

func TestWorktreeStartFromHeadAndABranch(t *testing.T) {
	e, root, git := worktreeSetup(t)
	// The main checkout has uncommitted and untracked work.
	writeFile(t, root, "a.txt", "edited\n")
	writeFile(t, root, "untracked.txt", "u\n")
	statusBefore, headBefore := git("status", "--porcelain"), git("rev-parse", "HEAD")
	head := headBefore
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-1", "p-git", "feat/a", head))
	if !strings.HasPrefix(rec.Path, realPath(e.worktreeDir)+"/p-git/feat-a-") || rec.State != protocol.WorktreePresent || rec.Branch != "feat/a" || rec.StartOid != head {
		t.Fatalf("record: %+v", rec)
	}
	if th.Checkout != rec.Path || th.WorktreeID != rec.ID {
		t.Fatalf("thread checkout %q, want %q", th.Checkout, rec.Path)
	}
	if git("rev-parse", "refs/heads/feat/a") != head || gitIn(t, rec.Path, "branch", "--show-current") != "feat/a" {
		t.Fatal("the worktree is not on the new branch at the start commit")
	}
	if git("status", "--porcelain") != statusBefore || git("rev-parse", "HEAD") != headBefore || git("branch", "--show-current") != "main" || readText(t, root, "a.txt") != "edited\n" {
		t.Fatal("the main checkout changed")
	}
	if readText(t, rec.Path, "a.txt") != "a\n" {
		t.Fatal("uncommitted work was copied into the worktree")
	}
	if _, err := os.Stat(filepath.Join(rec.Path, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked work was copied into the worktree")
	}
	other := git("rev-parse", "other")
	_, _, rec2 := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-2", "p-git", "feat/b", other))
	if readText(t, rec2.Path, "o.txt") != "o\n" || rec2.Path == rec.Path {
		t.Fatal("the second worktree does not hold the other branch's commit")
	}
}

func TestWorktreeStartRejections(t *testing.T) {
	e, _, git := worktreeSetup(t)
	head := git("rev-parse", "HEAD")
	for name, c := range map[string]struct {
		cmd  protocol.Command
		code string
	}{
		"existing branch": {worktreeStartCommand(e, "r1", "p-git", "other", head), "branch_exists"},
		"bad branch":      {worktreeStartCommand(e, "r2", "p-git", "a..b", head), "invalid_branch"},
		"dash branch":     {worktreeStartCommand(e, "r3", "p-git", "-x", head), "invalid_branch"},
		"unknown commit":  {worktreeStartCommand(e, "r4", "p-git", "feat/x", strings.Repeat("1", 40)), "invalid_start"},
		"short commit":    {worktreeStartCommand(e, "r5", "p-git", "feat/x", head[:12]), "invalid"},
		"not git":         {worktreeStartCommand(e, "r6", e.current().Projects[0].ID, "feat/x", head), "not_git"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.command(c.cmd)
			wantCode(t, err, c.code)
		})
	}
	bad := worktreeStartCommand(e, "r7", "p-git", "feat/x", head)
	bad.Workspace = &protocol.WorkspaceRequest{Mode: "checkout", Branch: "x"}
	_, err := e.command(bad)
	wantCode(t, err, "invalid")
	e.worktreeDir = ""
	_, err = e.command(worktreeStartCommand(e, "r8", "p-git", "feat/x", head))
	wantCode(t, err, "unsupported_workspace")
	if s := e.current(); len(s.Worktrees) != 0 || len(s.Threads) != 3 {
		t.Fatalf("a rejected start left state: %d worktrees, %d threads", len(s.Worktrees), len(s.Threads))
	}
	if git("branch", "--list", "feat/*") != "" {
		t.Fatal("a rejected start created a branch")
	}
}

func TestWorktreeExplicitCheckoutIgnoresTheWorktreeDefault(t *testing.T) {
	e, root, _ := worktreeSetup(t)
	e.mu.Lock()
	e.snap.AppSettings.WorkspaceDefault = "worktree"
	e.mu.Unlock()
	settings := e.current().Threads[0].Selected
	legacy := protocol.Command{Version: 1, ID: "legacy", Kind: "thread.start", ProjectID: "p-git", Agent: agent.FixtureName, Text: "x", Settings: &settings}
	_, err := e.command(legacy)
	wantCode(t, err, "unsupported_workspace")
	explicit := legacy
	explicit.ID, explicit.Workspace = "explicit", &protocol.WorkspaceRequest{Mode: "checkout"}
	r, err := e.command(explicit)
	if err != nil || ptrThread(e.current(), r.TargetID).Checkout != root {
		t.Fatalf("explicit checkout start: %v %+v", err, r)
	}
}

func TestWorktreeStartRetryIsIdempotent(t *testing.T) {
	e, _, git := worktreeSetup(t)
	c := worktreeStartCommand(e, "wt-retry", "p-git", "feat/r", git("rev-parse", "HEAD"))
	adds := 0
	worktreeBeforeAdd = func(string) { adds++ }
	t.Cleanup(func() { worktreeBeforeAdd = nil })
	first, _, _ := mustStartWorktree(t, e, c)
	again, err := e.command(c)
	if adds != 1 {
		t.Fatalf("git worktree add ran %d times", adds)
	}
	if err != nil || again.TargetID != first.TargetID || again.State != "accepted" {
		t.Fatalf("retry: %v %+v", err, again)
	}
	if s := e.current(); len(s.Worktrees) != 1 {
		t.Fatalf("retry created %d worktrees", len(s.Worktrees))
	}
}

func TestWorktreeGitFailureKeepsNothing(t *testing.T) {
	e, _, git := worktreeSetup(t)
	common := filepath.Join(realPath(git("rev-parse", "--show-toplevel")), ".git")
	worktreeBeforeAdd = func(string) {
		// Another process holds the new branch's ref lock.
		_ = os.MkdirAll(filepath.Join(common, "refs/heads/feat"), 0o755)
		_ = os.WriteFile(filepath.Join(common, "refs/heads/feat/f.lock"), nil, 0o644)
	}
	t.Cleanup(func() { worktreeBeforeAdd = nil })
	r, err := e.command(worktreeStartCommand(e, "wt-fail", "p-git", "feat/f", git("rev-parse", "HEAD")))
	if err != nil || r.State != "failed" || r.Error == nil || r.Error.Code != "worktree_failed" {
		t.Fatalf("failed start: %v %+v", err, r)
	}
	if s := e.current(); len(s.Worktrees) != 0 || len(s.Threads) != 3 {
		t.Fatalf("a failed creation left %d records, %d threads", len(s.Worktrees), len(s.Threads))
	}
	// The retry reports the recorded failure.
	if again, err := e.command(worktreeStartCommand(e, "wt-fail", "p-git", "feat/f", git("rev-parse", "HEAD"))); err != nil || again.State != "failed" {
		t.Fatalf("retry of a failed start: %v %+v", err, again)
	}
}

func TestWorktreeUnattachedAfterAttachFailureAndRetryAttaches(t *testing.T) {
	e, _, git := worktreeSetup(t)
	worktreeAfterAdd = func(string) {
		// The project's thread capacity fills while Git runs.
		e.mu.Lock()
		for len(e.snap.Threads) < 128 {
			e.snap.Threads = append(e.snap.Threads, protocol.Thread{ID: "filler-" + ID(), ProjectID: "p-git", State: "idle"})
		}
		e.mu.Unlock()
	}
	t.Cleanup(func() { worktreeAfterAdd = nil })
	c := worktreeStartCommand(e, "wt-attach", "p-git", "feat/u", git("rev-parse", "HEAD"))
	r, err := e.command(c)
	if err != nil || r.State != "failed" || r.Error == nil || r.Error.Code != "capacity" {
		t.Fatalf("attach failure: %v %+v", err, r)
	}
	rec := e.current().Worktrees[0]
	if rec.State != protocol.WorktreeUnattached || rec.Detail == "" {
		t.Fatalf("record after an attach failure: %+v", rec)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatal("the created worktree was not kept")
	}
	worktreeAfterAdd = nil
	e.mu.Lock()
	e.snap.Threads = e.snap.Threads[:3]
	e.mu.Unlock()
	again, err := e.command(c)
	if err != nil || again.State != "accepted" {
		t.Fatalf("retry: %v %+v", err, again)
	}
	s := e.current()
	if th := ptrThread(s, again.TargetID); th.WorktreeID != rec.ID || th.Checkout != rec.Path || s.Worktrees[0].State != protocol.WorktreePresent {
		t.Fatalf("attached thread: %+v %+v", th, s.Worktrees[0])
	}
	// Once accepted, the stored receipt answers.
	if third, err := e.command(c); err != nil || third.TargetID != again.TargetID {
		t.Fatalf("second retry: %v %+v", err, third)
	}
}

func TestWorktreeRestartReconcile(t *testing.T) {
	e, root, git := worktreeSetup(t)
	head := git("rev-parse", "HEAD")
	made := filepath.Join(realPath(e.worktreeDir), "made")
	git("worktree", "add", "-q", "-b", "feat/m", made, head)
	// A worktree Git left mid-initialization (its lock remains).
	initing := filepath.Join(realPath(e.worktreeDir), "initing")
	git("worktree", "add", "-q", "-b", "feat/i", initing, head)
	if err := os.WriteFile(filepath.Join(root, ".git", "worktrees", "initing", "locked"), []byte("initializing"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without a .git link, and a worktree on the wrong commit.
	bare := filepath.Join(realPath(e.worktreeDir), "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(realPath(e.worktreeDir), "wrong")
	git("worktree", "add", "-q", "-b", "feat/w", wrong, head)
	gitIn(t, wrong, "commit", "-q", "--allow-empty", "-m", "moved on")
	common := filepath.Join(root, ".git")
	creating := func(id, path, branch string) protocol.ManagedWorktree {
		return protocol.ManagedWorktree{ID: id, ProjectID: "p-git", Path: path, CommonDir: common, RelPath: ".", Branch: branch, StartOid: head, State: protocol.WorktreeCreating, CommandID: "c-" + id}
	}
	s := protocol.Snapshot{Worktrees: []protocol.ManagedWorktree{
		creating("w-made", made, "feat/m"),
		creating("w-none", filepath.Join(e.worktreeDir, "none"), "feat/n"),
		creating("w-init", initing, "feat/i"),
		creating("w-bare", bare, "feat/b"),
		creating("w-wrong", wrong, "feat/w"),
	}}
	reconcileWorktrees(&s)
	got := map[string]protocol.ManagedWorktree{}
	for _, w := range s.Worktrees {
		got[w.ID] = w
	}
	if _, ok := got["w-none"]; ok || len(got) != 4 {
		t.Fatalf("reconciled: %+v", s.Worktrees)
	}
	if w := got["w-made"]; w.State != protocol.WorktreeUnattached || w.AdminName != "made" || w.Unverified {
		t.Fatalf("verified record: %+v", w)
	}
	for _, id := range []string{"w-init", "w-bare", "w-wrong"} {
		if w := got[id]; !w.Unverified || w.State == protocol.WorktreeCreating || w.State == protocol.WorktreePresent {
			t.Fatalf("%s must be kept unverified: %+v", id, w)
		}
	}
}

func TestWorktreeRestartThenRetryAttaches(t *testing.T) {
	isolateAgentDiscovery(t)
	e, _, git := worktreeSetup(t)
	c := worktreeStartCommand(e, "wt-restart", "p-git", "feat/s", git("rev-parse", "HEAD"))
	// The first server "stops" after Git ran: its creation never reports.
	hold := make(chan struct{})
	worktreeAfterAdd = func(string) { <-hold }
	quickStartWait(t)
	t.Cleanup(func() {
		close(hold)
		e.gitLocked().wg.Wait()
		worktreeAfterAdd = nil
	})
	if r, err := e.command(c); err != nil || r.State != "running" {
		t.Fatalf("first attempt: %v %+v", err, r)
	}
	// What a restart does with the stored state and receipts.
	stored, _, err := e.store.Load()
	if err != nil || len(stored.Worktrees) != 1 || stored.Worktrees[0].State != protocol.WorktreeCreating {
		t.Fatalf("journaled record: %v %+v", err, stored.Worktrees)
	}
	if err := resolveInterruptedGitReceipts(e.store); err != nil {
		t.Fatal(err)
	}
	reconcileWorktrees(&stored)
	restarted := newEngine(stored, e.store)
	restarted.worktreeDir = e.worktreeDir
	if stored.Worktrees[0].State != protocol.WorktreeUnattached {
		t.Fatalf("after restart: %+v", stored.Worktrees[0])
	}
	r, err := restarted.command(c)
	if err != nil || r.State != "accepted" || ptrThread(restarted.current(), r.TargetID).WorktreeID != stored.Worktrees[0].ID {
		t.Fatalf("retry after restart: %v %+v", err, r)
	}
}

func TestWorktreeSurfacesResolveToTheWorktree(t *testing.T) {
	e, root, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-s", "p-git", "feat/s", git("rev-parse", "HEAD")))
	ctx := context.Background()
	if info := inspectWorkspace(ctx, th.Checkout); info.Kind != "worktree" || info.Branch != "feat/s" {
		t.Fatalf("workspace: %+v", info)
	}
	if dir, err := browseRoot(e.current(), protocol.BrowseRequest{Scope: "files", ThreadID: th.ID}); err != nil || dir != rec.Path {
		t.Fatalf("files root %q %v", dir, err)
	}
	settle(t, e, th.ID)
	// A Git write for the thread changes the worktree's index only.
	writeFile(t, rec.Path, "w.txt", "w\n")
	target := client.GitTarget{ThreadID: th.ID}
	mustGit(t, e, client.GitStageCommand("stage-w", target, entryOf(t, rec.Path, "w.txt", protocol.GitGroupUntracked)), protocol.GitStateSucceeded)
	if gitIn(t, rec.Path, "diff", "--cached", "--name-only") != "w.txt" || git("diff", "--cached", "--name-only") != "" {
		t.Fatal("the Git write did not target the worktree only")
	}
	if st := mustStatus(t, rec.Path); st.Workspace.Kind != "worktree" {
		t.Fatalf("status workspace: %+v", st.Workspace)
	}
	_ = root
}

func TestWorktreeTerminalStartsInTheWorktree(t *testing.T) {
	requireShell(t)
	e, _, git := worktreeSetup(t)
	home := t.TempDir()
	e.terminals.shell = "/bin/sh"
	e.terminals.env = []string{"HOME=" + home, "HISTFILE=/dev/null", "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	e.terminals.home = func() (string, error) { return home, nil }
	t.Cleanup(func() { _ = e.stopTerminals() })
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-t", "p-git", "feat/t", git("rev-parse", "HEAD")))
	r, err := e.command(protocol.Command{Version: 1, ID: "term", Kind: "terminal.open", ThreadID: th.ID, ClientID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range e.current().Terminals {
		if term.ID == r.TargetID && term.Dir != rec.Path {
			t.Fatalf("terminal dir %q, want %q", term.Dir, rec.Path)
		}
	}
	// A live terminal blocks removal once the thread is closed.
	closeThread(t, e, th.ID)
	removal, err := e.removalOf(context.Background(), rec.ID)
	if err != nil || !hasBlocker(removal, "terminals_open") {
		t.Fatalf("removal with a live terminal: %v %+v", err, removal)
	}
}

func TestWorktreeACPDispatchWorksInTheWorktree(t *testing.T) {
	gitFixture(t)
	e, _, checkout := acpEngine(t)
	root := realPath(checkout)
	e.mu.Lock()
	for i := range e.snap.Projects {
		if e.snap.Projects[i].ID == "project-acp" {
			e.snap.Projects[i].Path = root
		}
	}
	e.mu.Unlock()
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("init", "-q")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	e.worktreeDir = t.TempDir()
	settings := fakeSettings()
	c := client.WorktreeStartCommand("wt-acp", "project-acp", "claude", "FAKE-WRITE agent.txt from the agent\\n", settings, nil, git("rev-parse", "HEAD"), "agent/work")
	r, err := e.command(c)
	if err != nil || r.State != "accepted" {
		t.Fatalf("%v %+v", err, r)
	}
	th := ptrThread(e.current(), r.TargetID)
	waitFor(t, e, "turn", func(s protocol.Snapshot) bool {
		t := ptrThread(s, th.ID)
		return t.State == "idle" && len(t.Queue) == 0
	})
	if readText(t, th.Checkout, "agent.txt") != "from the agent\n" {
		t.Fatal("the agent did not work in the worktree")
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); !os.IsNotExist(err) {
		t.Fatal("the agent wrote into the main checkout")
	}
}

func TestWorktreeThreadsRunConcurrentlyAndRefOpsSerialize(t *testing.T) {
	e, root, git := worktreeSetup(t)
	head := git("rev-parse", "HEAD")
	_, a, _ := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-ca", "p-git", "feat/ca", head))
	_, b, _ := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-cb", "p-git", "feat/cb", head))
	s := e.current()
	if ta, tb := ptrThread(s, a.ID), ptrThread(s, b.ID); ta.State != "running" || tb.State != "running" {
		t.Fatalf("worktree threads did not run concurrently: %s %s", ta.State, tb.State)
	}
	// The main checkout's thread is not blocked by them.
	settings := s.Threads[0].Selected
	mainStart := protocol.Command{Version: 1, ID: "main", Kind: "thread.start", ProjectID: "p-git", Agent: agent.FixtureName, Text: "x", Settings: &settings, Workspace: &protocol.WorkspaceRequest{Mode: "checkout"}}
	r, err := e.command(mainStart)
	if err != nil || ptrThread(e.current(), r.TargetID).State != "running" {
		t.Fatalf("main checkout thread: %v", err)
	}
	// A creation holds the repository's Git slot: another ref change waits.
	var busy error
	worktreeBeforeAdd = func(string) {
		busy = func() error {
			_, err := e.command(client.GitBranchCreateCommand("branch-busy", gitTarget, "feat/z", head))
			return err
		}()
	}
	t.Cleanup(func() { worktreeBeforeAdd = nil })
	mustStartWorktree(t, e, worktreeStartCommand(e, "wt-cc", "p-git", "feat/cc", head))
	wantCode(t, busy, "git_busy")
	_ = root
}

// settle ticks the fixture turn of thread id to its end.
func settle(t *testing.T, e *engine, id string) {
	t.Helper()
	for range 200 {
		if ptrThread(e.current(), id).State != "running" {
			return
		}
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("the fixture turn did not end")
}

func closeThread(t *testing.T, e *engine, id string) {
	t.Helper()
	settle(t, e, id)
	th := ptrThread(e.current(), id)
	if _, err := e.command(protocol.Command{Version: 1, ID: "close-" + id, Kind: "thread.close", ThreadID: id, Revision: th.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
}

func hasBlocker(r protocol.WorktreeRemoval, code string) bool {
	for _, b := range r.Blockers {
		if strings.HasPrefix(b, code+":") {
			return true
		}
	}
	return false
}

func TestWorktreeMissingMovedUnregisteredAndRelocate(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-m", "p-git", "feat/m", git("rev-parse", "HEAD")))
	closeThread(t, e, th.ID)
	e.mu.Lock()
	e.snap.Threads = append([]protocol.Thread(nil), e.snap.Threads...)
	th2 := threadByID(&e.snap, th.ID)
	th2.SessionID = "old-session"
	e.mu.Unlock()
	reopen := func(id string) {
		th := ptrThread(e.current(), th.ID)
		if _, err := e.command(protocol.Command{Version: 1, ID: id, Kind: "thread.reopen", ThreadID: th.ID, Revision: th.LifecycleRevision}); err != nil {
			t.Fatal(err)
		}
	}
	reopen("reopen-1")
	send := func(id string) error {
		settings := ptrThread(e.current(), th.ID).Selected
		_, err := e.command(protocol.Command{Version: 1, ID: id, Kind: "prompt.send", ThreadID: th.ID, Text: "x", Settings: &settings})
		return err
	}
	stateOf := func() string {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.refreshWorktreesLocked()
		return worktreeByID(&e.snap, rec.ID).State
	}
	// Moved with Git: relocate follows it.
	moved := filepath.Join(filepath.Dir(rec.Path), "moved-here")
	git("worktree", "move", rec.Path, moved)
	if got := stateOf(); got != protocol.WorktreeMoved {
		t.Fatalf("after git worktree move: %s", got)
	}
	wantCode(t, send("send-moved"), "workspace_unavailable")
	if _, err := e.command(client.WorktreeRelocateCommand("relocate", rec.ID)); err != nil {
		t.Fatal(err)
	}
	s := e.current()
	if th := ptrThread(s, th.ID); th.Checkout != realPath(moved) || th.SessionID != "" || worktreeByID(&s, rec.ID).Path != realPath(moved) {
		t.Fatalf("after relocate: %+v", th)
	}
	if err := send("send-relocated"); err != nil {
		t.Fatalf("send after relocate: %v", err)
	}
	// Deleted outside the application: missing, Send refused.
	settle(t, e, th.ID)
	if err := os.RemoveAll(realPath(moved)); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(); got != protocol.WorktreeMissing {
		t.Fatalf("after deletion: %s", got)
	}
	wantCode(t, send("send-missing"), "workspace_unavailable")
	_, err := e.command(client.WorktreeRelocateCommand("relocate-missing", rec.ID))
	wantCode(t, err, "not_moved")
	// Recreated without Git's registration: unregistered.
	git("worktree", "prune")
	if err := os.MkdirAll(realPath(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realPath(moved), ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(); got != protocol.WorktreeUnregistered {
		t.Fatalf("recreated: %s", got)
	}
	// Forget drops the record; the thread stays and cannot send.
	if _, err := e.command(client.WorktreeForgetCommand("forget", rec.ID)); err != nil {
		t.Fatal(err)
	}
	if len(e.current().Worktrees) != 0 {
		t.Fatal("forget kept the record")
	}
	wantCode(t, send("send-forgotten"), "workspace_unavailable")
}

func TestWorktreePrune(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, _, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-p", "p-git", "feat/p", git("rev-parse", "HEAD")))
	if err := os.RemoveAll(rec.Path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	preview, _, err := e.pruneOf(ctx, "p-git")
	if err != nil || len(preview.Entries) != 1 || preview.Fingerprint == "" {
		t.Fatalf("prune preview: %v %+v", err, preview)
	}
	_, err = e.command(client.WorktreePruneCommand("prune-stale", "p-git", protocol.WorktreePrune{Fingerprint: "stale"}))
	wantCode(t, err, "stale_confirmation")
	if r, err := e.command(client.WorktreePruneCommand("prune", "p-git", preview)); err != nil || r.State != "accepted" {
		t.Fatalf("prune: %v %+v", err, r)
	}
	if strings.Contains(git("worktree", "list"), rec.Path) {
		t.Fatal("prune left the stale registration")
	}
	if git("rev-parse", "refs/heads/feat/p") == "" {
		t.Fatal("prune removed the branch")
	}
}

func TestWorktreeRemovalRefusalsAndCleanRemoval(t *testing.T) {
	e, root, git := worktreeSetup(t)
	head := git("rev-parse", "HEAD")
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-r", "p-git", "feat/r", head))
	ctx := context.Background()
	preview, err := e.removalOf(ctx, rec.ID)
	if err != nil || !hasBlocker(preview, "worktree_in_use") {
		t.Fatalf("open thread: %v %+v", err, preview)
	}
	_, err = e.command(client.WorktreeRemoveCommand("remove-open", preview))
	wantCode(t, err, "worktree_in_use")
	closeThread(t, e, th.ID)
	writeFile(t, rec.Path, "dirty.txt", "x\n")
	preview, _ = e.removalOf(ctx, rec.ID)
	if !hasBlocker(preview, "worktree_dirty") {
		t.Fatalf("dirty: %+v", preview)
	}
	if err := os.Remove(filepath.Join(rec.Path, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	// Ignored files are disclosed and removed with the directory.
	writeFile(t, rec.Path, ".gitignore", "build/\n")
	gitIn(t, rec.Path, "add", ".gitignore")
	gitIn(t, rec.Path, "commit", "-q", "-m", "ignore")
	writeFile(t, rec.Path, "build/out.bin", "b\n")
	preview, _ = e.removalOf(ctx, rec.ID)
	if len(preview.Blockers) != 0 || preview.Ignored != 1 || preview.IgnoredSample[0] != "build/" {
		t.Fatalf("clean preview: %+v", preview)
	}
	_, err = e.command(client.WorktreeRemoveCommand("remove-stale", protocol.WorktreeRemoval{ID: rec.ID, Fingerprint: "stale"}))
	wantCode(t, err, "stale_confirmation")
	r, err := e.command(client.WorktreeRemoveCommand("remove", preview))
	if err != nil || r.State != "accepted" {
		t.Fatalf("remove: %v %+v %+v", err, r, r.Error)
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Fatal("the worktree directory remains")
	}
	if git("rev-parse", "refs/heads/feat/r") == "" || strings.Contains(git("worktree", "list"), rec.Path) {
		t.Fatal("the branch was not kept, or Git still lists the worktree")
	}
	s := e.current()
	if worktreeByID(&s, rec.ID).State != protocol.WorktreeRemoved || ptrThread(s, th.ID).ID != th.ID {
		t.Fatal("the record or the thread is gone")
	}
	if env := gitCeilingEnv(rec.Path); env != nil {
		t.Fatalf("a removed worktree kept its ceiling: %v", env)
	}
	settings := ptrThread(s, th.ID).Selected
	lifecycle := ptrThread(s, th.ID).LifecycleRevision
	_, err = e.command(protocol.Command{Version: 1, ID: "send-removed", Kind: "prompt.reopen-send", ThreadID: th.ID, Revision: lifecycle, Text: "x", Settings: &settings})
	wantCode(t, err, "workspace_unavailable")
	_ = root
}

func TestWorktreeProjectRemovalKeepsDirectories(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-pr", "p-git", "feat/pr", git("rev-parse", "HEAD")))
	closeThread(t, e, th.ID)
	s := e.current()
	if list := protocol.ProjectWorktrees(s, "p-git"); len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("confirmation list: %+v", list)
	}
	p := projectByID(&s, "p-git")
	if _, err := e.command(protocol.Command{Version: 1, ID: "remove-project", Kind: "project.remove", ProjectID: "p-git", Revision: p.Revision}); err != nil {
		t.Fatal(err)
	}
	if len(e.current().Worktrees) != 0 {
		t.Fatal("project removal kept worktree records")
	}
	if _, err := os.Stat(filepath.Join(rec.Path, "a.txt")); err != nil {
		t.Fatal("project removal deleted the worktree directory")
	}
	if git("rev-parse", "refs/heads/feat/pr") == "" {
		t.Fatal("project removal deleted the branch")
	}
}

func TestWorktreeSubdirectoryProject(t *testing.T) {
	e, root, git := worktreeSetup(t)
	sub := filepath.Join(root, "sub")
	e.mu.Lock()
	e.snap.Projects = append(e.snap.Projects, protocol.Project{ID: "p-sub", Name: "sub", Path: sub, Revision: 1})
	e.mu.Unlock()
	head := git("rev-parse", "HEAD")
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-sub", "p-sub", "feat/sub", head))
	if rec.RelPath != "sub" || th.Checkout != filepath.Join(rec.Path, "sub") || readText(t, th.Checkout, "s.txt") != "s\n" {
		t.Fatalf("subdirectory mapping: %+v %q", rec, th.Checkout)
	}
	// A start commit without the project folder is refused.
	git("checkout", "-q", "--orphan", "empty")
	git("rm", "-rq", "--cached", ".")
	writeFile(t, root, "only.txt", "o\n")
	git("add", "only.txt")
	git("commit", "-q", "-m", "empty")
	_, err := e.command(worktreeStartCommand(e, "wt-sub2", "p-sub", "feat/sub2", git("rev-parse", "HEAD")))
	wantCode(t, err, "project_folder_absent")
}

func TestWorktreeGitWriteOnMissingWorktreeIsRefused(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-gm", "p-git", "feat/gm", git("rev-parse", "HEAD")))
	settle(t, e, th.ID)
	writeFile(t, rec.Path, "w.txt", "w\n")
	entry := entryOf(t, rec.Path, "w.txt", protocol.GitGroupUntracked)
	if err := os.RemoveAll(rec.Path); err != nil {
		t.Fatal(err)
	}
	r, err := e.command(client.GitStageCommand("stage-missing", client.GitTarget{ThreadID: th.ID}, entry))
	if err == nil && r.State == "accepted" {
		t.Fatalf("a Git write on a missing worktree was accepted: %+v", r)
	}
	// Nothing reached the project's own checkout.
	if git("diff", "--cached", "--name-only") != "" {
		t.Fatal("the refused write staged in the project checkout")
	}
}

func TestWorktreeCapabilitiesAdvertised(t *testing.T) {
	c, stop := startTestServer(t, t.TempDir())
	defer stop()
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"worktree-create", "worktree-manage"} {
		if !slices.Contains(snap.Capabilities, capability) {
			t.Fatalf("capability %q not advertised: %v", capability, snap.Capabilities)
		}
	}
}

// Port of the verifier's interleaving: a detection made before phase two
// must not revert the attached record to creating.
func TestWorktreeStaleDetectionNeverReverts(t *testing.T) {
	e, _, git := worktreeSetup(t)
	var detected map[string]worktreeDetection
	worktreeBeforeAdd = func(string) {
		e.mu.Lock()
		list := slices.Clone(e.snap.Worktrees)
		e.mu.Unlock()
		detected = detectAll(list)
	}
	t.Cleanup(func() { worktreeBeforeAdd = nil })
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-race", "p-git", "feat/race", git("rev-parse", "HEAD")))
	settle(t, e, th.ID)
	e.mu.Lock()
	next := clone(e.snap)
	if refreshWorktreesIn(&next, detected) {
		t.Error("a detection from the creating record was applied to the attached one")
	}
	e.snap = next
	e.worktreesCheckedAt = time.Time{}
	e.mu.Unlock()
	e.checkWorktrees()
	s := e.current()
	if got := worktreeByID(&s, rec.ID).State; got != protocol.WorktreePresent {
		t.Fatalf("state after the stale result: %s", got)
	}
	settings := ptrThread(s, th.ID).Selected
	if _, err := e.command(protocol.Command{Version: 1, ID: "send-after", Kind: "prompt.send", ThreadID: th.ID, Text: "x", Settings: &settings}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// A removed record is never revived by an older detection either.
	e.mu.Lock()
	stale := detectAll(e.snap.Worktrees)
	next = clone(e.snap)
	worktreeByID(&next, rec.ID).State = protocol.WorktreeRemoved
	refreshWorktreesIn(&next, stale)
	state := worktreeByID(&next, rec.ID).State
	e.mu.Unlock()
	if state != protocol.WorktreeRemoved {
		t.Fatalf("removed record became %s", state)
	}
}

// outerHomeSetup puts the application's worktree storage inside another
// repository, which Git discovery must never reach from a worktree.
func outerHomeSetup(t *testing.T, e *engine) string {
	outer := realPath(t.TempDir())
	gitIn(t, outer, "init", "-q")
	e.worktreeDir = filepath.Join(outer, "apphome", "worktrees")
	return outer
}

// Port of the verifier's walk-up: every thread-scoped resolver refuses an
// unavailable worktree, and Git never acts on the enclosing repository.
func TestWorktreeUnavailableNeverReachesAnEnclosingRepository(t *testing.T) {
	for _, mode := range []string{"unregistered", "moved", "forgotten"} {
		t.Run(mode, func(t *testing.T) {
			e, _, git := worktreeSetup(t)
			outer := outerHomeSetup(t, e)
			_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-walk", "p-git", "feat/walk", git("rev-parse", "HEAD")))
			settle(t, e, th.ID)
			writeFile(t, outer, "victim.txt", "v\n")
			entry := entryOf(t, outer, "victim.txt", protocol.GitGroupUntracked)
			switch mode {
			case "unregistered":
				if err := os.Remove(filepath.Join(rec.Path, ".git")); err != nil {
					t.Fatal(err)
				}
			case "moved":
				git("worktree", "move", rec.Path, filepath.Join(filepath.Dir(rec.Path), "elsewhere"))
				if err := os.MkdirAll(rec.Path, 0o755); err != nil { // an empty directory where it was
					t.Fatal(err)
				}
			case "forgotten":
				if err := os.Remove(filepath.Join(rec.Path, ".git")); err != nil {
					t.Fatal(err)
				}
				if _, err := e.command(client.WorktreeForgetCommand("forget", rec.ID)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := e.command(client.GitStageCommand("stage-walk", client.GitTarget{ThreadID: th.ID}, entry))
			wantCode(t, err, "workspace_unavailable")
			s := e.current()
			_, err = gitWriteTargetLocked(&s, protocol.Command{ThreadID: th.ID})
			wantCode(t, err, "workspace_unavailable")
			_, err = browseRoot(s, protocol.BrowseRequest{Scope: "files", ThreadID: th.ID})
			wantCode(t, err, "workspace_unavailable")
			_, err = documentRoot(&s, protocol.Command{ThreadID: th.ID, Path: "a.txt"})
			wantCode(t, err, "workspace_unavailable")
			_, err = e.command(protocol.Command{Version: 1, ID: "term-walk", Kind: "terminal.open", ThreadID: th.ID, ClientID: "alice"})
			wantCode(t, err, "workspace_unavailable")
			// The second layer: Git discovery from the stale path stops at
			// the worktree parent instead of finding the outer repository.
			// A forgotten record is no longer managed: only the resolver
			// protects its thread.
			if mode == "forgotten" {
				if staged := gitIn(t, outer, "diff", "--cached", "--name-only"); staged != "" {
					t.Fatalf("the enclosing repository was changed: %q", staged)
				}
				return
			}
			if _, err := newGitReader(context.Background(), rec.Path); err == nil {
				t.Fatal("Git discovery from the stale worktree path found a repository")
			}
			if info := inspectWorkspace(context.Background(), rec.Path); gitReadable(info) {
				t.Fatalf("workspace inspection found a repository: %+v", info)
			}
			if staged := gitIn(t, outer, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("the enclosing repository was changed: %q", staged)
			}
		})
	}
}

// A worktree that fails verification is kept for inspection and is never
// attached, even by a retry of the same command. The hook shows `git
// worktree add` runs post-checkout hooks.
func TestWorktreeUnverifiedIsNeverAttached(t *testing.T) {
	e, root, git := worktreeSetup(t)
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ngit commit -q --allow-empty -m hooked\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := worktreeStartCommand(e, "wt-unv", "p-git", "feat/unv", git("rev-parse", "HEAD"))
	r, err := e.command(c)
	if err != nil || r.State != "failed" || r.Error == nil || r.Error.Code != "worktree_failed" {
		t.Fatalf("start: %v %+v", err, r)
	}
	s := e.current()
	if len(s.Worktrees) != 1 || !s.Worktrees[0].Unverified || s.Worktrees[0].State == protocol.WorktreePresent {
		t.Fatalf("record: %+v", s.Worktrees)
	}
	threads := len(s.Threads)
	if again, err := e.command(c); err != nil || again.State != "failed" {
		t.Fatalf("retry: %v %+v", err, again)
	}
	if len(e.current().Threads) != threads {
		t.Fatal("a retry attached a thread to an unverified worktree")
	}
}

// Relocation is refused while a document or a Git change still uses the
// old path.
func TestWorktreeRelocateRefusals(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-rl", "p-git", "feat/rl", git("rev-parse", "HEAD")))
	settle(t, e, th.ID)
	git("worktree", "move", rec.Path, filepath.Join(filepath.Dir(rec.Path), "rl-moved"))
	e.mu.Lock()
	e.snap.Documents = append(e.snap.Documents, protocol.DocumentStatus{ID: "doc", Checkout: rec.Path, Path: "a.txt", State: protocol.DocumentStateSaved})
	e.mu.Unlock()
	_, err := e.command(client.WorktreeRelocateCommand("rl-doc", rec.ID))
	wantCode(t, err, "documents_open")
	e.mu.Lock()
	e.snap.Documents = nil
	e.gitLocked().holders["other"] = gitHold{top: rec.Path, common: rec.CommonDir}
	e.mu.Unlock()
	_, err = e.command(client.WorktreeRelocateCommand("rl-git", rec.ID))
	wantCode(t, err, "git_busy")
	e.mu.Lock()
	delete(e.gitLocked().holders, "other")
	e.mu.Unlock()
	if _, err := e.command(client.WorktreeRelocateCommand("rl-ok", rec.ID)); err != nil {
		t.Fatal(err)
	}
}

// Remove and prune never force, project removal is refused while a
// worktree removal runs, and prune compares a fresh listing taken while it
// holds the Git slot.
func TestWorktreeRemoveAndPruneArguments(t *testing.T) {
	e, _, git := worktreeSetup(t)
	head := git("rev-parse", "HEAD")
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-a", "p-git", "feat/a", head))
	closeThread(t, e, th.ID)
	var seen [][]string
	var projectRemoval error
	gitWriteArgsHook = func(_ string, args []string) {
		seen = append(seen, slices.Clone(args))
		if len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
			p := projectByID(&protocol.Snapshot{Projects: e.current().Projects}, "p-git")
			_, projectRemoval = e.command(protocol.Command{Version: 1, ID: "rm-project", Kind: "project.remove", ProjectID: "p-git", Revision: p.Revision})
		}
	}
	t.Cleanup(func() { gitWriteArgsHook = nil })
	preview, err := e.removalOf(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := e.command(client.WorktreeRemoveCommand("remove", preview)); err != nil || r.State != "accepted" {
		t.Fatalf("remove: %v %+v", err, r)
	}
	wantCode(t, projectRemoval, "git_busy")

	// Prune: two stale entries listed, one disappears before the in-slot
	// listing, so the confirmation is stale.
	_, _, r1 := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-b", "p-git", "feat/b", head))
	_, _, r2 := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-c", "p-git", "feat/c", head))
	for _, p := range []string{r1.Path, r2.Path} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	listing, _, err := e.pruneOf(context.Background(), "p-git")
	if err != nil || len(listing.Entries) != 2 {
		t.Fatalf("prune listing: %v %+v", err, listing)
	}
	dryRuns := 0
	gitWriteArgsHook = func(_ string, args []string) {
		seen = append(seen, slices.Clone(args))
		if slices.Contains(args, "--dry-run") {
			dryRuns++
			if dryRuns == 2 { // the listing taken inside the Git slot
				_ = os.MkdirAll(r1.Path, 0o755)
				_ = os.WriteFile(filepath.Join(r1.Path, ".git"), []byte("gitdir: "+filepath.Join(rec.CommonDir, "worktrees", r1.AdminName)+"\n"), 0o644)
			}
		}
	}
	_, err = e.command(client.WorktreePruneCommand("prune", "p-git", listing))
	wantCode(t, err, "stale_confirmation")
	for _, args := range seen {
		if len(args) > 0 && args[0] == "worktree" && (slices.Contains(args, "--force") || slices.Contains(args, "-f")) {
			t.Fatalf("worktree command forced: %v", args)
		}
	}
	if !slices.ContainsFunc(seen, func(a []string) bool { return slices.Equal(a, []string{"worktree", "remove", rec.Path}) }) {
		t.Fatalf("remove argv not observed: %v", seen)
	}
}

// A queued prompt in a thread whose worktree went away stays queued and
// runs after relocation.
func TestWorktreeQueuedPromptSurvivesUnavailability(t *testing.T) {
	e, _, git := worktreeSetup(t)
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-q", "p-git", "feat/q", git("rev-parse", "HEAD")))
	settings := ptrThread(e.current(), th.ID).Selected
	if _, err := e.command(protocol.Command{Version: 1, ID: "q-2", Kind: "prompt.send", ThreadID: th.ID, Text: "second", Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(rec.Path), "q-moved")
	git("worktree", "move", rec.Path, moved)
	e.mu.Lock()
	e.refreshWorktreesLocked()
	e.mu.Unlock()
	settle(t, e, th.ID)
	if got := ptrThread(e.current(), th.ID); len(got.Queue) != 1 || got.State != "idle" {
		t.Fatalf("after the first turn: state %s queue %d", got.State, len(got.Queue))
	}
	if _, err := e.command(client.WorktreeRelocateCommand("q-rel", rec.ID)); err != nil {
		t.Fatal(err)
	}
	settle(t, e, th.ID)
	if got := ptrThread(e.current(), th.ID); len(got.Queue) != 0 {
		t.Fatalf("the queued prompt did not run after relocation: %+v", got.Queue)
	}
}

// Port of the second verifier pass: the ceiling applies only at or below a
// managed worktree, so a relocation next to another repository's projects
// leaves them readable, and forgetting the record drops the ceiling.
func TestWorktreeCeilingOnlyCoversManagedWorktrees(t *testing.T) {
	e, _, git := worktreeSetup(t)
	base := realPath(t.TempDir())
	mono := filepath.Join(base, "mono")
	if err := os.MkdirAll(filepath.Join(mono, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mono, "init", "-q")
	writeFile(t, mono, "app/x.txt", "x\n")
	app := filepath.Join(mono, "app")
	_, th, rec := mustStartWorktree(t, e, worktreeStartCommand(e, "wt-ceil", "p-git", "feat/ceil", git("rev-parse", "HEAD")))
	settle(t, e, th.ID)
	moved := filepath.Join(mono, "wt")
	git("worktree", "move", rec.Path, moved)
	e.mu.Lock()
	e.refreshWorktreesLocked()
	e.mu.Unlock()
	if _, err := e.command(client.WorktreeRelocateCommand("rel", rec.ID)); err != nil {
		t.Fatal(err)
	}
	if env := gitCeilingEnv(app); env != nil {
		t.Fatalf("a sibling project got a ceiling: %v", env)
	}
	if info := inspectWorkspace(context.Background(), app); !gitReadable(info) {
		t.Fatalf("the sibling project lost Git: %+v", info)
	}
	if _, err := newGitReader(context.Background(), app); err != nil {
		t.Fatalf("the sibling project's reader: %v", err)
	}
	if env := gitCeilingEnv(filepath.Join(moved, "sub")); len(env) != 1 || env[0] != "GIT_CEILING_DIRECTORIES="+mono {
		t.Fatalf("ceiling inside the relocated worktree: %v", env)
	}
	// Forgetting the record drops its ceiling.
	if err := os.Remove(filepath.Join(moved, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(client.WorktreeForgetCommand("forget", rec.ID)); err != nil {
		t.Fatal(err)
	}
	if env := gitCeilingEnv(moved); env != nil {
		t.Fatalf("a forgotten worktree kept its ceiling: %v", env)
	}
}

// Port of the second verifier pass: an ACP claim that meets an unavailable
// worktree leaves the thread idle with its queue, and relocation runs it.
func TestWorktreeACPQueuedPromptRunsAfterRelocate(t *testing.T) {
	gitFixture(t)
	e, _, checkout := acpEngine(t)
	root := realPath(checkout)
	e.mu.Lock()
	for i := range e.snap.Projects {
		if e.snap.Projects[i].ID == "project-acp" {
			e.snap.Projects[i].Path = root
		}
	}
	e.mu.Unlock()
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("init", "-q")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	e.worktreeDir = t.TempDir()
	settings := fakeSettings()
	r, err := e.command(client.WorktreeStartCommand("wt-acp", "project-acp", "claude", "FAKE-WRITE one.txt 1\\n", settings, nil, git("rev-parse", "HEAD"), "agent/q"))
	if err != nil || r.State != "accepted" {
		t.Fatalf("%v %+v", err, r)
	}
	id := r.TargetID
	waitFor(t, e, "turn", func(s protocol.Snapshot) bool { t := ptrThread(s, id); return t.State == "idle" && len(t.Queue) == 0 })
	s := e.current()
	rec := *worktreeByID(&s, ptrThread(s, id).WorktreeID)
	moved := filepath.Join(filepath.Dir(rec.Path), "moved")
	git("worktree", "move", rec.Path, moved)
	e.mu.Lock()
	e.refreshWorktreesLocked()
	th := threadByID(&e.snap, id)
	th.Queue = append(th.Queue, protocol.Prompt{ID: "queued-x", Text: "FAKE-WRITE two.txt 2\\n", Revision: 1, Settings: th.Selected})
	e.ensureRunLocked(id) // the claim meets the moved worktree
	e.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	if got := ptrThread(e.current(), id); got.State != "idle" || got.Error != "" || len(got.Queue) != 1 {
		t.Fatalf("after the refused claim: state=%s err=%q queue=%d", got.State, got.Error, len(got.Queue))
	}
	if _, err := e.command(client.WorktreeRelocateCommand("rel", rec.ID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "queued turn", func(s protocol.Snapshot) bool { t := ptrThread(s, id); return t.State == "idle" && len(t.Queue) == 0 })
	if readText(t, moved, "two.txt") != "2\n" {
		t.Fatal("the queued prompt did not run in the relocated worktree")
	}
}

// A resolution job started in a managed worktree belongs to it: its
// dispatch is gated on the worktree and it blocks removal.
func TestWorktreeResolutionJobBelongsToTheWorktree(t *testing.T) {
	e, root, _ := jobSetup(t)
	e.mu.Lock()
	e.snap.Worktrees = append(e.snap.Worktrees, protocol.ManagedWorktree{ID: "wt-job", ProjectID: "project-acp", Path: root, CommonDir: filepath.Join(root, ".git"), RelPath: ".", State: protocol.WorktreePresent})
	e.mu.Unlock()
	startJob(t, e, root, "job", "")
	s := e.current()
	var job *protocol.Thread
	for i := range s.Threads {
		if s.Threads[i].Job != nil {
			job = &s.Threads[i]
		}
	}
	if job == nil || job.WorktreeID != "wt-job" {
		t.Fatalf("job thread: %+v", job)
	}
	// The record is not a registered linked worktree, so it is unavailable
	// and the job waits with its prompt instead of dispatching.
	time.Sleep(300 * time.Millisecond)
	if got := ptrThread(e.current(), job.ID); got.State != "idle" || len(got.Queue) != 1 {
		t.Fatalf("job dispatched in an unavailable worktree: state=%s queue=%d", got.State, len(got.Queue))
	}
	e.mu.Lock()
	rec := *worktreeByID(&e.snap, "wt-job")
	rec.State = protocol.WorktreePresent
	blockers := e.removalBlockersLocked(rec)
	e.mu.Unlock()
	if !slices.ContainsFunc(blockers, func(b string) bool { return strings.HasPrefix(b, "worktree_in_use") }) {
		t.Fatalf("the open job does not block removal: %v", blockers)
	}
}

func quickStartWait(t *testing.T) {
	t.Helper()
	old := worktreeStartWait
	worktreeStartWait = 150 * time.Millisecond
	t.Cleanup(func() { worktreeStartWait = old })
}

func slowCheckoutHook(t *testing.T, root, seconds string) {
	t.Helper()
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep "+seconds+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Creation is owned by the server: a slow checkout answers running at
// once, a retry answers running without blocking, a cancelled request does
// not stop Git, and the outcome is published and returned to the next retry.
func TestWorktreeStartIsServerOwned(t *testing.T) {
	e, root, git := worktreeSetup(t)
	quickStartWait(t)
	slowCheckoutHook(t, root, "1")
	adds := 0
	worktreeBeforeAdd = func(string) { adds++ }
	t.Cleanup(func() { worktreeBeforeAdd = nil })
	c := worktreeStartCommand(e, "wt-async", "p-git", "feat/async", git("rev-parse", "HEAD"))
	ctx, cancel := context.WithCancel(context.Background())
	began := time.Now()
	r, err := e.commandContext(ctx, c)
	cancel() // the client gives up; creation continues
	if err != nil || r.State != "running" || time.Since(began) > 900*time.Millisecond {
		t.Fatalf("first answer after %v: %v %+v", time.Since(began), err, r)
	}
	s := e.current()
	if rec := worktreeByCommand(&s, c.ID); rec == nil || rec.State != protocol.WorktreeCreating {
		t.Fatalf("the snapshot does not show the creation: %+v", s.Worktrees)
	}
	began = time.Now()
	again, err := e.command(c)
	if err != nil || again.State != "running" || time.Since(began) > 100*time.Millisecond {
		t.Fatalf("retry while running after %v: %v %+v", time.Since(began), err, again)
	}
	waitFor(t, e, "attached", func(s protocol.Snapshot) bool {
		rec := worktreeByCommand(&s, c.ID)
		return rec != nil && rec.State == protocol.WorktreePresent && len(worktreeThreads(&s, rec.ID)) == 1
	})
	final, err := e.command(c)
	if err != nil || final.State != "accepted" || ptrThread(e.current(), final.TargetID).WorktreeID == "" {
		t.Fatalf("final receipt: %v %+v", err, final)
	}
	if adds != 1 {
		t.Fatalf("git worktree add ran %d times", adds)
	}
}

// Server stop during a slow checkout cancels Git and leaves an honest,
// recoverable state: a failed receipt, and whatever Git left kept
// Unverified (or nothing).
func TestWorktreeServerStopMidCreation(t *testing.T) {
	e, root, git := worktreeSetup(t)
	quickStartWait(t)
	slowCheckoutHook(t, root, "30")
	c := worktreeStartCommand(e, "wt-stop", "p-git", "feat/stop", git("rev-parse", "HEAD"))
	if r, err := e.command(c); err != nil || r.State != "running" {
		t.Fatalf("start: %v %+v", err, r)
	}
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	began := time.Now()
	e.stopGitWrites()
	if time.Since(began) > 10*time.Second {
		t.Fatal("stop waited for the checkout")
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveInterruptedGitReceipts(e.store); err != nil {
		t.Fatal(err)
	}
	reconcileWorktrees(&stored)
	for _, w := range stored.Worktrees {
		if w.CommandID == c.ID && (w.State == protocol.WorktreeCreating || !w.Unverified) {
			t.Fatalf("after restart: %+v", w)
		}
	}
	restarted := newEngine(stored, e.store)
	restarted.worktreeDir = e.worktreeDir
	r, err := restarted.command(c)
	if err != nil || r.State != "failed" || r.Error == nil {
		t.Fatalf("retry after restart: %v %+v", err, r)
	}
	for _, th := range restarted.current().Threads {
		if th.WorktreeID != "" {
			t.Fatal("a thread was attached to an interrupted creation")
		}
	}
}

// Port of the async verifier: a stop mid-creation says so and still reports
// the branch Git left.
func TestWorktreeStopKeepsTheBranchMessage(t *testing.T) {
	e, root, git := worktreeSetup(t)
	quickStartWait(t)
	slowCheckoutHook(t, root, "30")
	c := worktreeStartCommand(e, "wt-stop4", "p-git", "feat/stop4", git("rev-parse", "HEAD"))
	if r, err := e.command(c); err != nil || r.State != "running" {
		t.Fatalf("start: %v %+v", err, r)
	}
	time.Sleep(300 * time.Millisecond) // Git has created the branch
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	e.stopGitWrites()
	r, err := e.store.Lookup(c)
	if err != nil || r == nil || r.Error == nil {
		t.Fatalf("stored receipt: %v %+v", err, r)
	}
	if !strings.Contains(r.Error.Message, "server stopped") || !strings.Contains(r.Error.Message, "feat/stop4 exists and was kept") {
		t.Fatalf("message: %s", r.Error.Message)
	}
}

// A panic during creation releases everything and fails honestly.
func TestWorktreeCreationPanicIsRecovered(t *testing.T) {
	e, _, git := worktreeSetup(t)
	worktreeAfterAdd = func(string) { panic("injected") }
	t.Cleanup(func() { worktreeAfterAdd = nil })
	c := worktreeStartCommand(e, "wt-panic", "p-git", "feat/panic", git("rev-parse", "HEAD"))
	r, err := e.command(c)
	if err != nil || r.State != "failed" || r.Error == nil || r.Error.Code != "worktree_failed" {
		t.Fatalf("receipt: %v %+v", err, r)
	}
	worktreeAfterAdd = nil
	s := e.current()
	if len(s.Worktrees) != 1 || !s.Worktrees[0].Unverified || s.Worktrees[0].State == protocol.WorktreeCreating {
		t.Fatalf("record: %+v", s.Worktrees)
	}
	e.mu.Lock()
	gs := e.gitLocked()
	leaked := len(gs.holders) + len(gs.cancels) + len(gs.inflight) + len(e.worktreeAsync)
	e.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("state left behind: %d entries", leaked)
	}
	gs.wg.Wait()
	if again, err := e.command(c); err != nil || again.State != "failed" {
		t.Fatalf("retry: %v %+v", err, again)
	}
	// Another creation in the repository proceeds.
	mustStartWorktree(t, e, worktreeStartCommand(e, "wt-after", "p-git", "feat/after", git("rev-parse", "HEAD")))
}

// A retry that arrived while the first request was still preparing answers
// running as soon as the creation is handed to the server.
func TestWorktreeRetryDuringPreparationIsWoken(t *testing.T) {
	e, root, git := worktreeSetup(t)
	quickStartWait(t)
	slowCheckoutHook(t, root, "2")
	entered, proceed := make(chan struct{}), make(chan struct{})
	worktreePrepareHook = func() {
		select {
		case <-entered:
		default:
			close(entered)
			<-proceed
		}
	}
	t.Cleanup(func() { worktreePrepareHook = nil })
	c := worktreeStartCommand(e, "wt-wake", "p-git", "feat/wake", git("rev-parse", "HEAD"))
	first := make(chan protocol.Receipt, 1)
	go func() { r, _ := e.command(c); first <- r }()
	<-entered
	second := make(chan protocol.Receipt, 1)
	go func() { r, _ := e.command(c); second <- r }()
	time.Sleep(100 * time.Millisecond) // the retry now waits on the reservation
	close(proceed)
	select {
	case r := <-second:
		if r.State != "running" {
			t.Fatalf("woken retry: %+v", r)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the retry was not woken at the handoff")
	}
	<-first
	waitFor(t, e, "attached", func(s protocol.Snapshot) bool {
		rec := worktreeByCommand(&s, c.ID)
		return rec != nil && rec.State == protocol.WorktreePresent
	})
	if n := len(e.current().Worktrees); n != 1 {
		t.Fatalf("%d records", n)
	}
}
