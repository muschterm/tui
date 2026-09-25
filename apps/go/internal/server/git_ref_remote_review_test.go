package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Regression tests for the 2026-09-24 verification of ADR 0021. Remotes are
// local bare repositories or fake SSH commands only.

func TestGitPushRefusesUpstreamWithDifferentName(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	git("checkout", "-q", "-b", "feat", "--track", "origin/main")
	local := r.localCommit(t, "feat.txt", "f\n")
	before := gitIn(t, r.bare, "rev-parse", "main")
	for _, mode := range []string{"", "simple", "current", "matching"} {
		if mode != "" {
			git("config", "push.default", mode)
		}
		_, err := e.command(client.GitPushCommand("push-mismatch-"+mode, gitTarget, mustStatus(t, root), ""))
		wantGitCode(t, err, "upstream_name_mismatch")
	}
	git("config", "push.default", "nothing")
	_, err := e.command(client.GitPushCommand("push-nothing", gitTarget, mustStatus(t, root), ""))
	wantGitCode(t, err, "not_supported")
	if gitIn(t, r.bare, "rev-parse", "main") != before {
		t.Fatal("a refused push changed the remote")
	}
	git("config", "push.default", "upstream")
	mustGit(t, e, client.GitPushCommand("push-upstream-mode", gitTarget, mustStatus(t, root), ""), protocol.GitStateSucceeded)
	if gitIn(t, r.bare, "rev-parse", "main") != local {
		t.Fatal("push.default=upstream did not push to the upstream")
	}
}

func TestGitPushTransportDeathAfterHookIsNotAHookRefusal(t *testing.T) {
	r := remoteSetup(t)
	e, root := r.e, r.root
	// The remote commits the update, then its receive-pack dies before
	// reporting status; a (passing) local pre-push hook exists.
	writeFile(t, r.bare, "hooks/reference-transaction", "#!/bin/sh\nif [ \"$1\" = committed ]; then kill -9 $PPID; fi\ncat >/dev/null\n")
	if err := os.Chmod(filepath.Join(r.bare, "hooks", "reference-transaction"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHook(t, root, "pre-push", "exit 0\n")
	local := r.localCommit(t, "l.txt", "l\n")
	res := mustGit(t, e, client.GitPushCommand("push-killed", gitTarget, mustStatus(t, root), ""), protocol.GitStateOutcomeUnknown)
	if res.Git.Code != "transport" || res.Git.Push.State != protocol.GitPushUnknown || res.Git.Push.Reason == "pre-push hook" {
		t.Fatalf("killed transport: %+v %+v", res.Git, res.Git.Push)
	}
	if gitIn(t, r.bare, "rev-parse", "main") != local {
		t.Fatal("repro did not reach the remote; the test proves nothing")
	}
}

func TestGitPushSigningAndMirrorFailClosed(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	r.localCommit(t, "s.txt", "s\n")
	before := gitIn(t, r.bare, "rev-parse", "main")
	gitIn(t, r.bare, "config", "receive.certNonceSeed", "seed")
	git("config", "push.gpgSign", "true")
	git("config", "gpg.program", "false")
	res := mustGit(t, e, client.GitPushCommand("push-signed", gitTarget, mustStatus(t, root), ""), protocol.GitStateFailed)
	if res.Git.Code != "signing_failed" || gitIn(t, r.bare, "rev-parse", "main") != before {
		t.Fatalf("signing: %+v", res.Git)
	}
	git("config", "--unset", "push.gpgSign")
	git("config", "remote.origin.mirror", "true")
	_, err := e.command(client.GitPushCommand("push-mirror", gitTarget, mustStatus(t, root), ""))
	wantGitCode(t, err, "not_supported")
}

func TestGitFetchLocalRefLockIsRefLocked(t *testing.T) {
	r := remoteSetup(t)
	r.remoteCommit(t, "x.txt", "x\n")
	writeFile(t, r.root, ".git/refs/remotes/origin/main.lock", "")
	res := mustGit(t, r.e, client.GitFetchCommand("fetch-locked", gitTarget, "origin"), protocol.GitStateFailed)
	if res.Git.Code != "ref_locked" || res.Git.Fetch.Code != "ref_locked" {
		t.Fatalf("locked fetch: %+v", res.Git)
	}
}

func TestGitPullFetchesUpstreamExcludedByRefspec(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	git("config", "--add", "remote.origin.fetch", "^refs/heads/main")
	tip := r.remoteCommit(t, "r.txt", "r\n")
	// A plain fetch of the remote skips main.
	if res := mustGit(t, e, client.GitFetchCommand("fetch-excluded", gitTarget, "origin"), protocol.GitStateSucceeded); res.Git.Fetch.UpstreamAfter == tip {
		t.Fatal("the repro's refspec did not exclude main")
	}
	res := mustGit(t, e, client.GitPullCommand("pull-excluded", gitTarget, mustStatus(t, root)), protocol.GitStateSucceeded)
	if res.Git.Integration.State != protocol.GitIntegrationFastForward || res.Git.Fetch.UpstreamAfter != tip || git("rev-parse", "HEAD") != tip {
		t.Fatalf("pull: %+v %+v", res.Git.Fetch, res.Git.Integration)
	}
}

func TestGitSwitchWouldOverwriteOddNames(t *testing.T) {
	e, root, git := refSetup(t)
	odd, nl := "we ird\\\"q\"é", "nl\nname"
	writeFile(t, root, odd, "1\n")
	writeFile(t, root, "d/f", "1\n")
	git("add", "-A")
	git("commit", "-q", "-m", "odd")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, odd, "2\n")
	git("rm", "-rq", "d")
	writeFile(t, root, "d", "file now\n")
	git("add", "-A")
	git("commit", "-q", "-m", "other")
	otherTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	writeFile(t, root, odd, "3\n")
	st := mustStatus(t, root)
	res := mustGit(t, e, client.GitSwitchCommand("sw-odd", gitTarget, st, "other", otherTip, true), protocol.GitStateFailed)
	if res.Git.Code != "would_overwrite" || !slices.Equal(res.Git.Paths, []string{odd}) || res.Git.PathsIncomplete {
		t.Fatalf("odd name: %q %+v", res.Git.Paths, res.Git)
	}
	// A directory that would lose untracked files.
	git("checkout", "--", odd)
	writeFile(t, root, "d/untracked", "u\n")
	st = mustStatus(t, root)
	res = mustGit(t, e, client.GitSwitchCommand("sw-dir", gitTarget, st, "other", otherTip, true), protocol.GitStateFailed)
	if res.Git.Code != "would_overwrite" || len(res.Git.Paths) == 0 {
		t.Fatalf("untracked dir: %+v", res.Git)
	}
	os.RemoveAll(filepath.Join(root, "d", "untracked"))
	// A name containing a newline cannot be parsed reliably.
	git("checkout", "-q", "other")
	writeFile(t, root, nl, "1\n")
	git("add", "-A")
	git("commit", "-q", "-m", "nl")
	nlTip := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	writeFile(t, root, nl, "untracked local\n")
	st = mustStatus(t, root)
	res = mustGit(t, e, client.GitSwitchCommand("sw-nl", gitTarget, st, "other", nlTip, true), protocol.GitStateFailed)
	if res.Git.Code != "would_overwrite" || !res.Git.PathsIncomplete {
		t.Fatalf("newline name: %q %+v", res.Git.Paths, res.Git)
	}
}

func TestGitSwitchPartialOutcomeIsNotClaimedClean(t *testing.T) {
	e, root, git := refSetup(t)
	git("checkout", "-q", "-b", "locky")
	writeFile(t, root, ".gitattributes", "*.lk filter=lk\n")
	writeFile(t, root, "x.lk", "payload\n")
	git("add", "-A")
	git("commit", "-q", "-m", "locky")
	locky := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	gitDir := filepath.Join(root, ".git")

	// A branch lock present before the request is refused up front.
	writeFile(t, root, ".git/refs/heads/topic.lock", "")
	st := mustStatus(t, root)
	_, err := e.command(client.GitSwitchCreateCommand("sw-prelocked", gitTarget, st, "topic", locky, false))
	wantGitCode(t, err, "ref_locked")
	os.Remove(filepath.Join(gitDir, "refs", "heads", "topic.lock"))

	// HEAD.lock appears while Git writes files (a smudge filter).
	git("config", "filter.lk.smudge", "touch "+gitDir+"/HEAD.lock; cat")
	st = mustStatus(t, root)
	res := mustGit(t, e, client.GitSwitchCommand("sw-headlock", gitTarget, st, "locky", locky, false), protocol.GitStateOutcomeUnknown)
	if res.Git.Code != "partial_switch" || git("branch", "--show-current") != "main" {
		t.Fatalf("HEAD.lock race: %+v", res.Git)
	}
	if _, err := os.Stat(filepath.Join(root, "x.lk")); err != nil {
		t.Fatal("repro did not change files; the test proves nothing")
	}
	os.Remove(filepath.Join(gitDir, "HEAD.lock"))
	git("reset", "-q", "--hard")
	git("clean", "-qfd")

	// The new branch's ref lock appears while Git writes files.
	git("config", "filter.lk.smudge", "touch "+gitDir+"/refs/heads/topic.lock; cat")
	st = mustStatus(t, root)
	res = mustGit(t, e, client.GitSwitchCreateCommand("sw-reflock", gitTarget, st, "topic", locky, false), protocol.GitStateOutcomeUnknown)
	if res.Git.Code != "partial_switch" {
		t.Fatalf("ref lock race: %+v", res.Git)
	}
}

func TestGitSwitchFromDetachedHeadNeedsLeaveCommitsAck(t *testing.T) {
	e, root, git := refSetup(t)
	mainTip := git("rev-parse", "main")
	git("checkout", "-q", "--detach")
	writeFile(t, root, "orphan.txt", "o\n")
	git("add", "orphan.txt")
	git("commit", "-q", "-m", "orphan 1")
	writeFile(t, root, "orphan.txt", "o2\n")
	git("commit", "-q", "-am", "orphan 2")
	st := mustStatus(t, root)
	cmd := client.GitSwitchCommand("sw-detached", gitTarget, st, "main", mainTip, false)
	_, err := e.command(cmd)
	wantGitCode(t, err, "leaves_commits")
	n, ok := client.GitLeaveCommitsCount(err)
	if !ok || n != 2 {
		t.Fatalf("count: %d %v (%v)", n, ok, err)
	}
	mustGit(t, e, client.GitAcknowledgeLeaveCommits(client.GitSwitchCommand("sw-detached-ack", gitTarget, st, "main", mainTip, false), n), protocol.GitStateSucceeded)
	if git("branch", "--show-current") != "main" {
		t.Fatal("not switched")
	}
}

func TestGitRefActionsSerializeAcrossLinkedWorktrees(t *testing.T) {
	r := remoteSetup(t)
	e, git := r.e, r.git
	wt := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-q", "-b", "wtbranch", wt)
	wt, _ = filepath.EvalSymlinks(wt)
	addGitThread(e, "p-wt", "t-wt", wt)
	git("config", "protocol.ssh.allow", "always")
	git("config", "core.sshCommand", script(t, "exec sleep 30\n"))
	git("remote", "add", "slow", "ssh://git@example.invalid/repo.git")
	done := make(chan protocol.Receipt, 1)
	go func() {
		rec, err := e.command(client.GitFetchCommand("fetch-slow", gitTarget, "slow"))
		if err != nil {
			t.Error(err)
		}
		done <- rec
	}()
	waitFor(t, e, "fetch running", func(s protocol.Snapshot) bool {
		return len(s.GitOps) > 0 && s.GitOps[len(s.GitOps)-1].CommandID == "fetch-slow" && s.GitOps[len(s.GitOps)-1].State == protocol.GitStateRunning
	})
	wtTarget := client.GitTarget{ProjectID: "p-wt"}
	_, err := e.command(client.GitBranchCreateCommand("create-in-wt", wtTarget, "fromwt", gitIn(t, wt, "rev-parse", "HEAD")))
	wantGitCode(t, err, "git_busy")
	if _, err := e.command(client.GitCancelCommand("cancel-slow", "fetch-slow")); err != nil {
		t.Fatal(err)
	}
	<-done
	mustGit(t, e, client.GitBranchCreateCommand("create-in-wt-2", wtTarget, "fromwt", gitIn(t, wt, "rev-parse", "HEAD")), protocol.GitStateSucceeded)
}

func TestGitNetworkEnvironmentKeepsCLIVariables(t *testing.T) {
	r := remoteSetup(t)
	e, git := r.e, r.git
	git("config", "protocol.ssh.allow", "always")
	git("remote", "add", "envremote", "ssh://git@example.invalid/repo.git")
	seen := filepath.Join(t.TempDir(), "env")
	// GIT_SSH_COMMAND is the user's own setting and must be honored;
	// GIT_DIR must not redirect the write; DISPLAY must not reach a
	// network command.
	t.Setenv("GIT_SSH_COMMAND", script(t, "echo \"display=$DISPLAY wayland=$WAYLAND_DISPLAY\" > "+seen+"\necho 'Permission denied (publickey).' >&2\nexit 255\n"))
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("DISPLAY", ":7")
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	res := mustGit(t, e, client.GitFetchCommand("fetch-env", gitTarget, "envremote"), protocol.GitStateFailed)
	if res.Git.Code != "auth_required" {
		t.Fatalf("env fetch: %+v", res.Git)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("GIT_SSH_COMMAND not honored: %v", err)
	}
	if strings.TrimSpace(string(b)) != "display= wayland=" {
		t.Fatalf("display leaked: %q", b)
	}
}

// Second verification (2026-09-24).

// docGit is a document harness whose root is a fresh repository with a
// project and thread for Git writes.
func docGit(t *testing.T) (*docHarness, func(...string) string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	h := newDocHarness(t)
	git := func(args ...string) string { return gitIn(t, h.root, args...) }
	git("init", "-q")
	git("config", "user.name", "W")
	git("config", "user.email", "w@example.invalid")
	addGitThread(h.e, "p-git", "t-git", h.root)
	return h, git
}

// unsavedEdit leaves an acknowledged but unsaved edit in rel.
func unsavedEdit(h *docHarness, rel string, replica uint64) {
	id := h.open(rel, "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", replica)
	ed.gen = 1
	op, _ := ed.insert(0, "UNSAVED ")
	h.commit(id)
	ed.next("ack", ackFor(op))
}

func TestGitSwitchRechecksCarryAfterSavingDocuments(t *testing.T) {
	h, git := docGit(t)
	h.write("a.txt", "one\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("branch", "other")
	unsavedEdit(h, "a.txt", 21)
	st := mustStatus(t, h.root)
	if len(st.Entries) != 0 || h.read("a.txt") != "one\n" {
		t.Fatal("edit saved early; the test proves nothing")
	}
	r := mustGit(t, h.e, client.GitSwitchCommand("sw", gitTarget, st, "other", git("rev-parse", "other"), false), protocol.GitStateFailed)
	if r.Git.Code != "stale_status" || git("branch", "--show-current") != "main" || h.read("a.txt") != "UNSAVED one\n" {
		t.Fatalf("switch after flush: %+v", r.Git)
	}
}

func TestGitPullSnapshotsAfterSavingDocuments(t *testing.T) {
	h, git := docGit(t)
	writeFile(t, os.Getenv("HOME"), ".gitconfig", "[protocol]\n\tallow = never\n[protocol \"file\"]\n\tallow = always\n[init]\n\tdefaultBranch = main\n")
	h.write("a.txt", "one\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("branch", "-M", "main")
	bare := filepath.Join(t.TempDir(), "o.git")
	gitIn(t, h.root, "init", "-q", "--bare", bare)
	git("remote", "add", "origin", bare)
	git("push", "-q", "-u", "origin", "main")
	odir := t.TempDir()
	gitIn(t, odir, "clone", "-q", bare, ".")
	writeFile(t, odir, "a.txt", "remote\n")
	gitIn(t, odir, "commit", "-qam", "r")
	gitIn(t, odir, "push", "-q", "origin", "main")
	unsavedEdit(h, "a.txt", 22)
	r := mustGit(t, h.e, client.GitPullCommand("pull", gitTarget, mustStatus(t, h.root)), protocol.GitStateFailed)
	if r.Git.Code != "would_overwrite" || !slices.Equal(r.Git.Paths, []string{"a.txt"}) || h.read("a.txt") != "UNSAVED one\n" {
		t.Fatalf("pull after flush: %+v", r.Git)
	}
}

func TestGitPushRefusesConfiguredPushRefspec(t *testing.T) {
	r := remoteSetup(t)
	r.git("config", "remote.origin.push", "refs/heads/main:refs/for/main")
	r.localCommit(t, "g.txt", "g\n")
	_, err := r.e.command(client.GitPushCommand("push-gerrit", gitTarget, mustStatus(t, r.root), ""))
	wantGitCode(t, err, "not_supported")
}

func TestGitPullIgnoresStaleFetchHead(t *testing.T) {
	r := remoteSetup(t)
	r.other("checkout", "-q", "-b", "side")
	writeFile(t, r.odir, "side.txt", "side\n")
	r.other("add", "side.txt")
	r.other("commit", "-q", "-m", "side")
	r.other("push", "-q", "origin", "side")
	r.other("checkout", "-q", "main")
	r.git("fetch", "-q", "origin", "side")
	r.git("config", "fetch.writeFetchHEAD", "false")
	head := r.git("rev-parse", "HEAD")
	res := mustGit(t, r.e, client.GitPullCommand("pull-stale", gitTarget, mustStatus(t, r.root)), protocol.GitStateSucceeded)
	if r.git("rev-parse", "HEAD") != head || res.Git.Integration.State != protocol.GitIntegrationUpToDate {
		t.Fatalf("stale FETCH_HEAD: %+v", res.Git.Integration)
	}
}

func TestGitPushOutcomesWithoutTrackingOrObjects(t *testing.T) {
	r := remoteSetup(t)
	// The fetch refspec excludes main, so the tracking ref cannot confirm.
	r.git("config", "--add", "remote.origin.fetch", "^refs/heads/main")
	local := r.localCommit(t, "p.txt", "p\n")
	res := mustGit(t, r.e, client.GitPushCommand("push-excluded", gitTarget, mustStatus(t, r.root), ""), protocol.GitStateSucceeded)
	if res.Git.Code != "" || res.Git.Push.NewOid != local {
		t.Fatalf("pushed: %+v %+v", res.Git, res.Git.Push)
	}
	// Objects already on the remote; it dies after committing the update.
	local = r.localCommit(t, "q.txt", "q\n")
	r.git("push", "-q", "origin", "HEAD:refs/heads/other")
	writeFile(t, r.bare, "hooks/reference-transaction", "#!/bin/sh\nif [ \"$1\" = committed ]; then kill -9 $PPID; fi\ncat >/dev/null\n")
	if err := os.Chmod(filepath.Join(r.bare, "hooks", "reference-transaction"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHook(t, r.root, "pre-push", "exit 0\n")
	res = mustGit(t, r.e, client.GitPushCommand("push-noobjects", gitTarget, mustStatus(t, r.root), ""), protocol.GitStateOutcomeUnknown)
	if gitIn(t, r.bare, "rev-parse", "main") != local || res.Git.Push.Reason == "pre-push hook" {
		t.Fatalf("killed: %+v", res.Git)
	}
}

func TestGitPushHookRefusalFromConfigAndLargeOutput(t *testing.T) {
	r := remoteSetup(t)
	before := gitIn(t, r.bare, "rev-parse", "main")
	r.localCommit(t, "h.txt", "h\n")
	// A config-defined hook (Git 2.46+) that prints more than the bounded
	// output before refusing.
	hook := script(t, "head -c 200000 /dev/zero | tr '\\0' x\necho\necho refused-by-policy >&2\nexit 1\n")
	r.git("config", "hook.policy.command", hook)
	r.git("config", "hook.policy.event", "pre-push")
	res := mustGit(t, r.e, client.GitPushCommand("push-config-hook", gitTarget, mustStatus(t, r.root), ""), protocol.GitStateFailed)
	if res.Git.Code != "rejected" || res.Git.Push.Reason != "pre-push hook" || gitIn(t, r.bare, "rev-parse", "main") != before {
		t.Skipf("config hooks unsupported by this Git or not traced: %+v", res.Git)
	}
}

func TestGitSwitchCreateAtDetachedHeadNeedsNoLeaveAck(t *testing.T) {
	e, root, git := refSetup(t)
	git("checkout", "-q", "--detach")
	writeFile(t, root, "o.txt", "o\n")
	git("add", "o.txt")
	git("commit", "-q", "-m", "detached work")
	head := git("rev-parse", "HEAD")
	st := mustStatus(t, root)
	mustGit(t, e, client.GitSwitchCreateCommand("sw-keep", gitTarget, st, "keep", head, false), protocol.GitStateSucceeded)
	if git("branch", "--show-current") != "keep" {
		t.Fatal("not switched")
	}
}

func TestGitKeepsRestrictiveProtocolEnvironment(t *testing.T) {
	r := remoteSetup(t)
	// The user's GIT_ALLOW_PROTOCOL forbids file:// and must still apply.
	t.Setenv("GIT_ALLOW_PROTOCOL", "https")
	res := mustGit(t, r.e, client.GitFetchCommand("fetch-restricted", gitTarget, "origin"), protocol.GitStateFailed)
	if !strings.Contains(res.Git.Output, "transport 'file' not allowed") {
		t.Fatalf("GIT_ALLOW_PROTOCOL ignored: %+v", res.Git)
	}
}

// docState reads an open document's text and rewrite pause count.
func docState(h *docHarness, id string) (text string, paused int) {
	h.onActor(id, func(a *docActor) { text, paused = a.d.Text(), a.rewritePaused })
	return text, paused
}

func TestGitDiscardCoordinatesWithOpenDocuments(t *testing.T) {
	h, git := docGit(t)
	h.write("a.txt", "committed\n", 0o644)
	h.write("b.txt", "committed b\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	h.write("a.txt", "local change\n", 0o644)
	h.write("b.txt", "local b\n", 0o644)

	// An unsaved edit on the discarded path is saved first, which makes the
	// reviewed pin stale: the discard refuses and the edit survives.
	entry := entryOf(t, h.root, "a.txt", "unstaged")
	id := h.open("a.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 41)
	ed.gen = 1
	op, _ := ed.insert(0, "UNSAVED ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	if h.read("a.txt") != "local change\n" {
		t.Fatal("edit saved early; the test proves nothing")
	}
	r := mustGit(t, h.e, client.GitDiscardCommand("discard-edited", gitTarget, entry), protocol.GitStateFailed)
	if r.Git.Code != "stale_entry" || h.read("a.txt") != "UNSAVED local change\n" {
		t.Fatalf("discard over unsaved edit: %+v file=%q", r.Git, h.read("a.txt"))
	}
	if _, paused := docState(h, id); paused != 0 {
		t.Fatalf("document left paused: %d", paused)
	}

	// A clean open document is reconciled with the restored file.
	idB := h.open("b.txt", "alice")
	if text, _ := docState(h, idB); text != "local b\n" {
		t.Fatalf("document text: %q", text)
	}
	mustGit(t, h.e, client.GitDiscardCommand("discard-clean", gitTarget, entryOf(t, h.root, "b.txt", "unstaged")), protocol.GitStateSucceeded)
	if h.read("b.txt") != "committed b\n" {
		t.Fatal("not restored")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		text, paused := docState(h, idB)
		if text == "committed b\n" && paused == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("document not reconciled: %q paused=%d", text, paused)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
