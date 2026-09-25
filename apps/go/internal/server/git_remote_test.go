package server

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Remote tests never contact a real remote: every remote is a local bare
// repository, a loopback HTTP server or a fake core.sshCommand, and the
// temporary global config allows only the file (and, where a test opts in,
// loopback http or fake ssh) transports.

type remoteRepo struct {
	e     *engine
	root  string
	git   func(...string) string
	bare  string
	other func(...string) string
	odir  string
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func remoteSetup(t *testing.T) remoteRepo {
	t.Helper()
	e, root, git := gitWriteSetup(t)
	writeFile(t, os.Getenv("HOME"), ".gitconfig", "[protocol]\n\tallow = never\n[protocol \"file\"]\n\tallow = always\n[init]\n\tdefaultBranch = main\n")
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "-q", "--bare", bare)
	writeFile(t, root, "a.txt", "a\n")
	writeFile(t, root, "b.txt", "b\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("remote", "add", "origin", bare)
	git("push", "-q", "-u", "origin", "main")
	odir := t.TempDir()
	gitIn(t, odir, "clone", "-q", bare, ".")
	return remoteRepo{e: e, root: root, git: git, bare: bare, other: func(args ...string) string { return gitIn(t, odir, args...) }, odir: odir}
}

// remoteCommit commits name=content in the other clone and pushes it.
func (r remoteRepo) remoteCommit(t *testing.T, name, content string) string {
	t.Helper()
	r.other("fetch", "-q", "origin")
	r.other("reset", "-q", "--hard", "origin/main")
	writeFile(t, r.odir, name, content)
	r.other("add", "--", name)
	r.other("commit", "-q", "-m", "remote "+name)
	r.other("push", "-q", "origin", "main")
	return r.other("rev-parse", "HEAD")
}

func (r remoteRepo) localCommit(t *testing.T, name, content string) string {
	t.Helper()
	writeFile(t, r.root, name, content)
	r.git("add", "--", name)
	r.git("commit", "-q", "-m", "local "+name)
	return r.git("rev-parse", "HEAD")
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGitFetchPullPushWithLocalBare(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	base := git("rev-parse", "HEAD")

	res := mustGit(t, e, client.GitPullCommand("pull-uptodate", gitTarget, mustStatus(t, root)), protocol.GitStateSucceeded)
	if res.Git.Fetch == nil || res.Git.Fetch.State != protocol.GitFetchSucceeded || res.Git.Integration == nil || res.Git.Integration.State != protocol.GitIntegrationUpToDate {
		t.Fatalf("up to date pull: %+v %+v", res.Git.Fetch, res.Git.Integration)
	}

	tip := r.remoteCommit(t, "remote.txt", "r1\n")
	res = mustGit(t, e, client.GitPullCommand("pull-ff", gitTarget, mustStatus(t, root)), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != tip || res.Git.Integration.State != protocol.GitIntegrationFastForward || res.Git.Fetch.UpstreamBefore != base || res.Git.Fetch.UpstreamAfter != tip || res.Git.Commit != tip {
		t.Fatalf("fast forward: %+v %+v", res.Git.Fetch, res.Git.Integration)
	}

	local := r.localCommit(t, "local.txt", "l1\n")
	res = mustGit(t, e, client.GitPullCommand("pull-ahead", gitTarget, mustStatus(t, root)), protocol.GitStateSucceeded)
	if res.Git.Integration.State != protocol.GitIntegrationAhead || git("rev-parse", "HEAD") != local {
		t.Fatalf("ahead: %+v", res.Git.Integration)
	}
	push := client.GitPushCommand("push-1", gitTarget, mustStatus(t, root), tip)
	res = mustGit(t, e, push, protocol.GitStateSucceeded)
	if gitIn(t, r.bare, "rev-parse", "main") != local || res.Git.Push == nil || res.Git.Push.State != protocol.GitPushPushed || res.Git.Push.NewOid != local || res.Git.Push.OldOid != tip || res.Git.Code != "" {
		t.Fatalf("push: %+v %+v", res.Git, res.Git.Push)
	}
	// The same command ID never pushes twice; it returns the receipt.
	again, err := e.command(push)
	if err != nil || again.Revision != res.Revision || again.Git.Push.State != protocol.GitPushPushed {
		t.Fatalf("duplicate push: %+v %v", again, err)
	}
	res = mustGit(t, e, client.GitPushCommand("push-uptodate", gitTarget, mustStatus(t, root), ""), protocol.GitStateSucceeded)
	if res.Git.Push.State != protocol.GitPushUpToDate {
		t.Fatalf("up to date push: %+v", res.Git.Push)
	}
	_, err = e.command(client.GitPushCommand("push-stale-up", gitTarget, mustStatus(t, root), tip))
	wantGitCode(t, err, "stale_upstream")

	// Fetch alone updates only the remote-tracking ref.
	tip2 := r.remoteCommit(t, "remote2.txt", "r2\n")
	res = mustGit(t, e, client.GitFetchCommand("fetch-1", gitTarget, ""), protocol.GitStateSucceeded)
	if res.Git.Fetch.Remote != "origin" || res.Git.Fetch.Upstream != "origin/main" || res.Git.Fetch.UpstreamBefore != local || res.Git.Fetch.UpstreamAfter != tip2 || git("rev-parse", "HEAD") != local {
		t.Fatalf("fetch: %+v", res.Git.Fetch)
	}
	_, err = e.command(client.GitFetchCommand("fetch-unknown", gitTarget, "nowhere"))
	wantGitCode(t, err, "unknown_remote")

	// Diverged: fetched, not integrated.
	local2 := r.localCommit(t, "local2.txt", "l2\n")
	tip3 := r.remoteCommit(t, "remote3.txt", "r3\n")
	res = mustGit(t, e, client.GitPullCommand("pull-diverged", gitTarget, mustStatus(t, root)), protocol.GitStateFailed)
	if res.Git.Code != "diverged" || res.Git.Fetch.State != protocol.GitFetchSucceeded || res.Git.Fetch.UpstreamAfter != tip3 || res.Git.Integration.State != protocol.GitIntegrationDiverged || git("rev-parse", "HEAD") != local2 {
		t.Fatalf("diverged: %+v %+v %+v", res.Git, res.Git.Fetch, res.Git.Integration)
	}
	if git("rev-list", "--merges", "--count", "HEAD") != "0" {
		t.Fatal("pull merged")
	}
	// Push refuses while the fetched upstream is not an ancestor.
	_, err = e.command(client.GitPushCommand("push-behind", gitTarget, mustStatus(t, root), ""))
	wantGitCode(t, err, "behind_upstream")

	// The remote moved without a local fetch: the remote rejects it.
	git("reset", "-q", "--hard", "origin/main")
	local3 := r.localCommit(t, "local3.txt", "l3\n")
	r.remoteCommit(t, "remote4.txt", "r4\n")
	res = mustGit(t, e, client.GitPushCommand("push-rejected", gitTarget, mustStatus(t, root), ""), protocol.GitStateFailed)
	if res.Git.Code != "rejected" || res.Git.Push.State != protocol.GitPushRejected || res.Git.Push.Reason == "" || gitIn(t, r.bare, "rev-parse", "main") == local3 {
		t.Fatalf("rejected: %+v %+v", res.Git, res.Git.Push)
	}
}

func TestGitPushPrePushHookAndLeaseFreeRuns(t *testing.T) {
	r := remoteSetup(t)
	e, root := r.e, r.root
	before := gitIn(t, r.bare, "rev-parse", "main")
	r.localCommit(t, "l.txt", "l\n")
	writeHook(t, root, "pre-push", "echo declined by policy >&2\nexit 1\n")
	res := mustGit(t, e, client.GitPushCommand("push-hook", gitTarget, mustStatus(t, root), ""), protocol.GitStateFailed)
	if res.Git.Code != "rejected" || res.Git.Push.Reason != "pre-push hook" || !strings.Contains(res.Git.Output, "declined by policy") || gitIn(t, r.bare, "rev-parse", "main") != before {
		t.Fatalf("pre-push: %+v %+v", res.Git, res.Git.Push)
	}
	os.Remove(filepath.Join(root, ".git", "hooks", "pre-push"))
	// Fetch and push do not take the checkout lease; pull does.
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "running"
	e.mu.Unlock()
	mustGit(t, e, client.GitFetchCommand("fetch-busy", gitTarget, "origin"), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitPushCommand("push-busy", gitTarget, mustStatus(t, root), ""), protocol.GitStateSucceeded)
	_, err := e.command(client.GitPullCommand("pull-busy", gitTarget, mustStatus(t, root)))
	wantGitCode(t, err, "checkout_busy")
	// Detached HEAD and missing upstream.
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "idle"
	e.mu.Unlock()
	r.git("checkout", "-q", "--detach")
	st := mustStatus(t, root)
	cmd := client.GitPullCommand("pull-detached", gitTarget, st)
	cmd.Git.Sync.ExpectedBranch, cmd.Git.Sync.Upstream = "main", "origin/main"
	_, err = e.command(cmd)
	wantGitCode(t, err, "detached")
	r.git("checkout", "-q", "-b", "lonely")
	st = mustStatus(t, root)
	cmd = client.GitPushCommand("push-noup", gitTarget, st, "")
	cmd.Git.Sync.Upstream = "origin/lonely"
	_, err = e.command(cmd)
	wantGitCode(t, err, "no_upstream")
}

func TestGitPullNeverAutostashesOrOverwrites(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	for _, kv := range [][2]string{{"merge.autostash", "true"}, {"rebase.autoStash", "true"}, {"pull.rebase", "true"}, {"merge.ff", "false"}} {
		git("config", kv[0], kv[1])
	}
	head := git("rev-parse", "HEAD")
	r.remoteCommit(t, "a.txt", "remote a\n")
	writeFile(t, root, "a.txt", "local a\n")
	res := mustGit(t, e, client.GitPullCommand("pull-overwrite", gitTarget, mustStatus(t, root)), protocol.GitStateFailed)
	if res.Git.Code != "would_overwrite" || !slices.Equal(res.Git.Paths, []string{"a.txt"}) || res.Git.Fetch.State != protocol.GitFetchSucceeded || res.Git.Integration.State != protocol.GitIntegrationFailed {
		t.Fatalf("overwrite: %+v %+v", res.Git, res.Git.Integration)
	}
	if git("rev-parse", "HEAD") != head || readText(t, root, "a.txt") != "local a\n" || git("stash", "list") != "" {
		t.Fatal("pull changed local work or stashed")
	}
	// An ignored file the upstream now tracks is never overwritten.
	git("checkout", "--", "a.txt")
	r.remoteCommit(t, "gen.out", "tracked upstream\n")
	writeFile(t, root, ".git/info/exclude", "gen.out\n")
	writeFile(t, root, "gen.out", "ignored local\n")
	res = mustGit(t, e, client.GitPullCommand("pull-ignored", gitTarget, mustStatus(t, root)), protocol.GitStateFailed)
	if res.Git.Code != "would_overwrite" || !slices.Equal(res.Git.Paths, []string{"gen.out"}) || readText(t, root, "gen.out") != "ignored local\n" {
		t.Fatalf("ignored: %+v", res.Git)
	}
	os.Remove(filepath.Join(root, "gen.out"))
	// Unrelated local changes are carried by a fast forward, with no merge
	// commit despite merge.ff=false.
	writeFile(t, root, "b.txt", "local b\n")
	res = mustGit(t, e, client.GitPullCommand("pull-carry", gitTarget, mustStatus(t, root)), protocol.GitStateSucceeded)
	if res.Git.Integration.State != protocol.GitIntegrationFastForward || git("rev-parse", "HEAD") != r.other("rev-parse", "HEAD") || readText(t, root, "b.txt") != "local b\n" || git("stash", "list") != "" {
		t.Fatalf("carry: %+v", res.Git.Integration)
	}
}

func TestGitRemoteCredentialFailuresNeverPrompt(t *testing.T) {
	r := remoteSetup(t)
	e, git := r.e, r.git
	marker := filepath.Join(t.TempDir(), "asked")
	askpass := script(t, "touch "+marker+"\necho secret\n")
	t.Setenv("GIT_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("DISPLAY", ":0")
	git("config", "core.askPass", askpass)
	git("config", "protocol.ssh.allow", "always")
	git("remote", "add", "sshremote", "ssh://git@example.invalid/repo.git")
	for i, c := range []struct{ stderr, code string }{
		{"git@example.invalid: Permission denied (publickey).", "auth_required"},
		{"Host key verification failed.", "host_key_unknown"},
		{"sign_and_send_pubkey: signing failed for ED25519 \"k\" from agent: agent refused operation", "agent_unavailable"},
		{"ssh: Could not resolve hostname example.invalid: Name or service not known", "transport"},
	} {
		git("config", "core.sshCommand", script(t, "echo '"+c.stderr+"' >&2\nexit 255\n"))
		res := mustGit(t, e, client.GitFetchCommand("ssh-"+c.code, gitTarget, "sshremote"), protocol.GitStateFailed)
		if res.Git.Code != c.code || res.Git.Fetch.State != protocol.GitFetchFailed || res.Git.Fetch.Code != c.code || !strings.Contains(res.Git.Output, c.stderr[:10]) {
			t.Fatalf("%d: %+v", i, res.Git)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	git("config", "protocol.http.allow", "always")
	git("remote", "add", "httpremote", srv.URL+"/repo.git")
	res := mustGit(t, e, client.GitFetchCommand("http-401", gitTarget, "httpremote"), protocol.GitStateFailed)
	if res.Git.Code != "auth_required" {
		t.Fatalf("401: %+v", res.Git)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("an askpass program ran")
	}
}

func TestGitFetchUsesCredentialHelperOverLoopbackHTTP(t *testing.T) {
	r := remoteSetup(t)
	e, git := r.e, r.git
	execPath, err := exec.Command("git", "--exec-path").Output()
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, statErr := os.Stat(backend); err != nil || statErr != nil {
		t.Skip("git-http-backend unavailable")
	}
	cgiHandler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(r.bare), "GIT_HTTP_EXPORT_ALL=1"}, InheritEnv: []string{"PATH", "HOME", "GIT_CONFIG_NOSYSTEM"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if u, p, ok := req.BasicAuth(); !ok || u != "user" || p != "pw" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		cgiHandler.ServeHTTP(w, req)
	}))
	defer srv.Close()
	creds := filepath.Join(t.TempDir(), "creds")
	if err := os.WriteFile(creds, []byte(strings.Replace(srv.URL, "http://", "http://user:pw@", 1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("config", "protocol.http.allow", "always")
	git("config", "credential.helper", "store --file="+creds)
	git("remote", "add", "http", srv.URL+"/"+filepath.Base(r.bare))
	tip := r.remoteCommit(t, "over-http.txt", "h\n")
	res := mustGit(t, e, client.GitFetchCommand("http-fetch", gitTarget, "http"), protocol.GitStateSucceeded)
	if got := git("rev-parse", "refs/remotes/http/main"); got != tip || res.Git.Fetch.State != protocol.GitFetchSucceeded {
		t.Fatalf("fetch over http: %s %+v", got, res.Git)
	}
}

func TestGitFetchStallTimeoutAndCancel(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	git("config", "protocol.ssh.allow", "always")
	git("config", "core.sshCommand", script(t, "exec sleep 30\n"))
	git("remote", "add", "slow", "ssh://git@example.invalid/repo.git")
	old := gitNetworkStall
	gitNetworkStall = 300 * time.Millisecond
	start := time.Now()
	res := mustGit(t, e, client.GitFetchCommand("fetch-stall", gitTarget, "slow"), protocol.GitStateFailed)
	gitNetworkStall = old
	if res.Git.Code != "timeout" || time.Since(start) > 10*time.Second {
		t.Fatalf("stall: %+v after %v", res.Git, time.Since(start))
	}

	done := make(chan protocol.Receipt, 1)
	go func() {
		rec, err := e.command(client.GitFetchCommand("fetch-cancel", gitTarget, "slow"))
		if err != nil {
			t.Error(err)
		}
		done <- rec
	}()
	waitFor(t, e, "cancellable fetch", func(s protocol.Snapshot) bool {
		return len(s.GitOps) > 0 && s.GitOps[len(s.GitOps)-1].CommandID == "fetch-cancel" && s.GitOps[len(s.GitOps)-1].State == protocol.GitStateRunning && s.GitOps[len(s.GitOps)-1].Cancellable
	})
	e.mu.Lock()
	holder := e.writerHolder(&e.snap, root, "")
	e.mu.Unlock()
	if holder != "" {
		t.Fatalf("fetch holds the checkout lease: %s", holder)
	}
	_, err := e.command(client.GitBranchCreateCommand("create-during-fetch", gitTarget, "x", git("rev-parse", "HEAD")))
	wantGitCode(t, err, "git_busy")
	_, err = e.command(client.GitCancelCommand("cancel-unknown", "nope"))
	wantGitCode(t, err, "not_running")
	cr, err := e.command(client.GitCancelCommand("cancel-1", "fetch-cancel"))
	if err != nil || cr.Git == nil || cr.Git.State != protocol.GitStateSucceeded {
		t.Fatalf("cancel: %+v %v", cr, err)
	}
	res = <-done
	if res.Git.State != protocol.GitStateFailed || res.Git.Code != "cancelled" || res.Git.Fetch.State != protocol.GitFetchCancelled {
		t.Fatalf("cancelled fetch: %+v %+v", res.Git, res.Git.Fetch)
	}
	_, err = e.command(client.GitCancelCommand("cancel-2", "fetch-cancel"))
	wantGitCode(t, err, "not_running")

	// A pull is cancellable while it fetches, and holds the lease.
	git("remote", "set-url", "origin", "ssh://git@example.invalid/repo.git")
	go func() {
		rec, err := e.command(client.GitPullCommand("pull-cancel", gitTarget, mustStatus(t, root)))
		if err != nil {
			t.Error(err)
		}
		done <- rec
	}()
	waitFor(t, e, "cancellable pull", func(s protocol.Snapshot) bool {
		if len(s.GitOps) == 0 {
			return false
		}
		op := s.GitOps[len(s.GitOps)-1]
		return op.CommandID == "pull-cancel" && op.State == protocol.GitStateRunning && op.Cancellable
	})
	e.mu.Lock()
	holder = e.writerHolder(&e.snap, root, "")
	e.mu.Unlock()
	if holder != gitHolderPrefix+"pull-cancel" {
		t.Fatalf("pull lease: %q", holder)
	}
	if _, err := e.command(client.GitCancelCommand("cancel-pull", "pull-cancel")); err != nil {
		t.Fatal(err)
	}
	res = <-done
	if res.Git.Code != "cancelled" || res.Git.State != protocol.GitStateFailed || res.Git.Integration.State != protocol.GitIntegrationNotStarted || res.Git.Fetch.State != protocol.GitFetchCancelled {
		t.Fatalf("cancelled pull: %+v %+v %+v", res.Git, res.Git.Fetch, res.Git.Integration)
	}
}

func TestGitSwitchIsNotCancellable(t *testing.T) {
	r := remoteSetup(t)
	e, root, git := r.e, r.root, r.git
	git("branch", "side")
	gate := filepath.Join(t.TempDir(), "gate")
	writeHook(t, root, "post-checkout", "while [ ! -e "+gate+" ]; do sleep 0.02; done\n")
	done := make(chan protocol.Receipt, 1)
	st := mustStatus(t, root)
	go func() {
		rec, err := e.command(client.GitSwitchCommand("sw-slow", gitTarget, st, "side", git("rev-parse", "side"), false))
		if err != nil {
			t.Error(err)
		}
		done <- rec
	}()
	waitFor(t, e, "switch running", func(s protocol.Snapshot) bool {
		if len(s.GitOps) == 0 {
			return false
		}
		op := s.GitOps[len(s.GitOps)-1]
		return op.CommandID == "sw-slow" && op.State == protocol.GitStateRunning
	})
	_, err := e.command(client.GitCancelCommand("cancel-switch", "sw-slow"))
	wantGitCode(t, err, "not_cancellable")
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Git.State != protocol.GitStateSucceeded || git("branch", "--show-current") != "side" {
		t.Fatalf("switch: %+v", res.Git)
	}
}

func TestGitFetchProgressPublishedNotJournaled(t *testing.T) {
	r := remoteSetup(t)
	e := r.e
	for i := range 40 {
		writeFile(t, r.odir, "many/"+strings.Repeat("f", i+1)+".txt", strings.Repeat("x", 100*i)+"\n")
	}
	r.other("add", ".")
	r.other("commit", "-q", "-m", "many")
	r.other("push", "-q", "origin", "main")
	old := gitProgressInterval
	gitProgressInterval = 0
	defer func() { gitProgressInterval = old }()
	ch := make(chan protocol.Snapshot, 1<<14)
	e.mu.Lock()
	e.subscribers[ch] = true
	e.mu.Unlock()
	res := mustGit(t, e, client.GitFetchCommand("fetch-progress", gitTarget, "origin"), protocol.GitStateSucceeded)
	e.mu.Lock()
	delete(e.subscribers, ch)
	e.mu.Unlock()
	seen := false
	for len(ch) > 0 {
		for _, op := range (<-ch).GitOps {
			if op.CommandID == "fetch-progress" && op.Progress != nil && op.Progress.Phase != "" {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("no progress was published")
	}
	if strings.Contains(res.Git.Output, "\r") {
		t.Fatalf("progress lines kept in output: %q", res.Git.Output)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []protocol.Snapshot{e.current(), stored} {
		for _, op := range s.GitOps {
			if op.Progress != nil || op.Cancellable {
				t.Fatalf("progress retained: %+v", op)
			}
		}
	}
	snap := protocol.Snapshot{GitOps: []protocol.GitOp{{CommandID: "x", State: protocol.GitStateRunning, Progress: &protocol.GitProgress{Phase: "p"}, Cancellable: true}}}
	recoverGitOps(&snap)
	if snap.GitOps[0].Progress != nil || snap.GitOps[0].Cancellable || snap.GitOps[0].State != protocol.GitStateOutcomeUnknown {
		t.Fatalf("recovered: %+v", snap.GitOps[0])
	}
}

func TestGitRemoteHelpers(t *testing.T) {
	top := t.TempDir()
	writeFile(t, top, "a.txt", "")
	writeFile(t, top, "sp\\é \"q\".txt", "")
	got, incomplete := overwrittenPaths("error: Your local changes to the following files would be overwritten by checkout:\n\ta.txt\n\tsp\\é \"q\".txt\nPlease commit\nAborting\n", top)
	if !slices.Equal(got, []string{"a.txt", "sp\\é \"q\".txt"}) || incomplete {
		t.Fatalf("paths: %q %v", got, incomplete)
	}
	// A name with a newline continues on an unindented line.
	if _, incomplete := overwrittenPaths("error: The following untracked working tree files would be overwritten by checkout:\n\tnl\nname\n\ta.txt\nPlease move\nAborting\n", top); !incomplete {
		t.Fatal("newline name not marked incomplete")
	}
	line, ok := parsePushPorcelain([]byte("To /x\n!\trefs/heads/main:refs/heads/main\t[rejected] (non-fast-forward)\nDone\n"), "refs/heads/main")
	if !ok || line.flag != "!" || line.reason != "non-fast-forward" {
		t.Fatalf("porcelain: %+v", line)
	}
	var phases []string
	out := &cappedOutput{limit: 1 << 10, drain: true}
	l := &gitProgressLines{dst: out, progress: func(p string, pct int) { phases = append(phases, p+":"+strings.TrimSpace(string(rune('0'+pct/10)))) }}
	l.Write([]byte("remote: Counting objects:  50% (1/2)\rremote: Counting objects: 100% (2/2), done.\nReceiving objects:  10% (1/10)\r"))
	l.flush()
	if len(phases) != 3 || phases[0] != "Counting objects:5" || !strings.HasPrefix(phases[2], "Receiving objects") || string(out.bytes()) != "remote: Counting objects: 100% (2/2), done.\n" {
		t.Fatalf("progress: %q %q", phases, out.bytes())
	}
	for _, name := range []string{"-x", "a b", "", "x..y", "--upload-pack=evil"} {
		if validRemoteName(name) {
			t.Fatalf("accepted remote %q", name)
		}
	}
}
