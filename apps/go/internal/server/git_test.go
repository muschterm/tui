package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	"golang.org/x/sys/unix"
)

// gitFixture isolates tests from the developer's global/system Git config.
func gitFixture(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	return root, func(args ...string) string { return gitIn(t, root, args...) }
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasEntry(s protocol.GitStatus, path, group string) *protocol.GitStatusEntry {
	for i, e := range s.Entries {
		if e.Path == path && e.Group == group {
			return &s.Entries[i]
		}
	}
	return nil
}

func mustStatus(t *testing.T, root string) protocol.GitStatus {
	t.Helper()
	s, err := readGitStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGitStatusGroupsAndDiffs(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	for _, name := range []string{"mod.txt", "staged.txt", "both.txt", "old.txt", "gone.txt", "clean.txt"} {
		writeFile(t, root, name, name+" line one\nline two\nline three\nline four\n")
	}
	writeFile(t, root, "bin.dat", "a\x00b")
	git("add", ".")
	git("commit", "-m", "Initial")
	if s := mustStatus(t, root); len(s.Entries) != 0 || s.Branch != "main" || s.Workspace.State != "branch" || s.Operation != "" {
		t.Fatalf("clean: %+v", s)
	}
	writeFile(t, root, "mod.txt", "changed\n")
	writeFile(t, root, "staged.txt", "staged change\n")
	git("add", "staged.txt")
	writeFile(t, root, "both.txt", "staged\n")
	git("add", "both.txt")
	writeFile(t, root, "both.txt", "staged\nthen unstaged\n")
	git("mv", "old.txt", "new name.txt")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "bin.dat", "c\x00d")
	odd := []string{"dir/nested/deep.txt", "with space.txt", "ünï ✓.txt", "new\nline.txt", "-p", "--output=x"}
	for _, name := range odd {
		writeFile(t, root, name, "untracked "+name+"\n")
	}
	s := mustStatus(t, root)
	for _, want := range []struct{ path, group string }{
		{"mod.txt", "unstaged"}, {"staged.txt", "staged"}, {"both.txt", "staged"}, {"both.txt", "unstaged"},
		{"new name.txt", "staged"}, {"gone.txt", "unstaged"}, {"bin.dat", "unstaged"},
	} {
		if hasEntry(s, want.path, want.group) == nil {
			t.Errorf("missing %s/%s in %+v", want.path, want.group, s.Entries)
		}
	}
	for _, name := range odd {
		if hasEntry(s, name, "untracked") == nil {
			t.Errorf("missing untracked %q", name)
		}
	}
	if e := hasEntry(s, "new name.txt", "staged"); e == nil || e.OrigPath != "old.txt" || e.Index != "R" {
		t.Fatalf("rename: %+v", e)
	}
	if e := hasEntry(s, "gone.txt", "unstaged"); e.Worktree != "D" {
		t.Fatalf("delete: %+v", e)
	}
	if hasEntry(s, "mod.txt", "staged") != nil || hasEntry(s, "staged.txt", "unstaged") != nil {
		t.Fatal("single-group change duplicated")
	}

	ctx := context.Background()
	diff := func(p, group string) protocol.GitDiff {
		t.Helper()
		d, err := readGitDiff(ctx, root, p, group)
		if err != nil {
			t.Fatalf("diff %q %s: %v", p, group, err)
		}
		if d.Bytes != len(d.Text) {
			t.Fatalf("bytes: %+v", d)
		}
		return d
	}
	if d := diff("both.txt", "staged"); !strings.Contains(d.Text, "+staged") || strings.Contains(d.Text, "then unstaged") {
		t.Fatalf("staged: %s", d.Text)
	}
	if d := diff("both.txt", "unstaged"); !strings.Contains(d.Text, "+then unstaged") {
		t.Fatalf("unstaged: %s", d.Text)
	}
	if d := diff("new name.txt", "staged"); !strings.Contains(d.Text, "rename from old.txt") {
		t.Fatalf("rename diff: %s", d.Text)
	}
	for _, name := range odd {
		if d := diff(name, "untracked"); !strings.Contains(d.Text, "+untracked "+strings.ReplaceAll(name, "\n", "\n+")) {
			t.Fatalf("untracked %q: %s", name, d.Text)
		}
	}
	if d := diff("bin.dat", "unstaged"); !d.Binary {
		t.Fatalf("binary: %+v", d)
	}
	if d := diff("gone.txt", "unstaged"); !strings.Contains(d.Text, "deleted file") {
		t.Fatalf("deleted: %s", d.Text)
	}

	writeFile(t, root, "big.txt", strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef\n", 40000))
	if d := diff("big.txt", "untracked"); !d.Truncated || d.Bytes > gitDiffMaxBytes || !strings.HasSuffix(d.Text, "\n") {
		t.Fatalf("truncation: truncated=%v bytes=%d", d.Truncated, d.Bytes)
	}

	writeFile(t, root, "../outside.txt", "secret\n")
	for _, bad := range []struct{ path, group, code string }{
		{"../outside.txt", "untracked", "invalid"}, {"/etc/passwd", "untracked", "invalid"}, {"dir/../mod.txt", "unstaged", "invalid"},
		{".git/config", "untracked", "invalid"}, {"dir/.GIT/x", "untracked", "invalid"}, {"", "unstaged", "invalid"}, {"./mod.txt", "unstaged", "invalid"},
		{"mod.txt", "bogus", "invalid"}, {"clean.txt", "unstaged", "not_found"}, {"mod.txt", "staged", "not_found"},
		{"missing.txt", "untracked", "not_found"}, {"dir", "untracked", "not_found"}, {"*.txt", "unstaged", "not_found"},
	} {
		_, err := readGitDiff(ctx, root, bad.path, bad.group)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != bad.code {
			t.Errorf("%q/%s: want %s, got %v", bad.path, bad.group, bad.code, err)
		}
	}
}

func TestGitConflictedMerge(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-m", "base")
	git("checkout", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-am", "other")
	git("checkout", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-am", "main")
	if err := exec.Command("git", "-C", root, "-c", "user.name=T", "-c", "user.email=t@e.invalid", "merge", "other").Run(); err == nil {
		t.Fatal("merge unexpectedly succeeded")
	}
	s := mustStatus(t, root)
	if s.Operation != "merge" || hasEntry(s, "c.txt", "conflicted") == nil {
		t.Fatalf("conflict: %+v", s)
	}
	d, err := readGitDiff(context.Background(), root, "c.txt", "conflicted")
	if err != nil || !strings.Contains(d.Text, "<<<<<<<") {
		t.Fatalf("conflicted diff: %v %+v", err, d)
	}
}

func TestGitWorkspaceStates(t *testing.T) {
	root, git := gitFixture(t)
	ctx := context.Background()
	s := mustStatus(t, root)
	if s.Workspace.State != "non-git" || len(s.Entries) != 0 {
		t.Fatalf("non-git: %+v", s)
	}
	if _, err := readGitDiff(ctx, root, "x", "untracked"); !errors.Is(err, errGitNotRepository) {
		t.Fatalf("non-git diff: %v", err)
	}
	if _, err := readGitShow(ctx, root, "abcd"); !errors.Is(err, errGitNotRepository) {
		t.Fatalf("non-git show: %v", err)
	}
	if s := mustStatus(t, "fixture://p"); s.Workspace.Kind != "fixture" || len(s.Entries) != 0 {
		t.Fatalf("fixture: %+v", s)
	}
	git("init")
	writeFile(t, root, "a.txt", "a\n")
	git("add", "a.txt")
	s = mustStatus(t, root)
	if s.Workspace.State != "unborn" || s.Branch != "main" || hasEntry(s, "a.txt", "staged") == nil {
		t.Fatalf("unborn: %+v", s)
	}
	if d, err := readGitDiff(ctx, root, "a.txt", "staged"); err != nil || !strings.Contains(d.Text, "+a") {
		t.Fatalf("unborn diff: %v %+v", err, d)
	}
	if l, err := readGitLog(ctx, root, 10); err != nil || len(l.Commits) != 0 || l.Workspace.State != "unborn" {
		t.Fatalf("unborn log: %v %+v", err, l)
	}
	git("commit", "-m", "one")
	git("checkout", "--detach")
	s = mustStatus(t, root)
	if s.Workspace.State != "detached" || s.Branch != "" {
		t.Fatalf("detached: %+v", s)
	}
	if l, err := readGitLog(ctx, root, 10); err != nil || len(l.Commits) != 1 {
		t.Fatalf("detached log: %v %+v", err, l)
	}
}

func TestGitLogAndShow(t *testing.T) {
	root, git := gitFixture(t)
	ctx := context.Background()
	git("init")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "first")
	git("checkout", "-b", "side")
	writeFile(t, root, "b.txt", "b\n")
	git("add", ".")
	git("commit", "-m", "side work")
	git("checkout", "main")
	writeFile(t, root, "c.txt", "c\n")
	git("add", ".")
	git("commit", "-m", "main work", "-m", "Body paragraph.")
	git("merge", "--no-ff", "-m", "merge side", "side")
	git("tag", "v1")
	l, err := readGitLog(ctx, root, 50)
	if err != nil || len(l.Commits) != 4 || l.Truncated {
		t.Fatalf("log: %v %+v", err, l)
	}
	head := l.Commits[0]
	if head.Subject != "merge side" || len(head.Parents) != 2 || !slices.Contains(head.Refs, "HEAD") || !slices.Contains(head.Refs, "refs/heads/main") || !slices.Contains(head.Refs, "refs/tags/v1") {
		t.Fatalf("merge commit: %+v", head)
	}
	if head.Author != "Test" || head.Email != "test@example.invalid" || len(head.Hash) < 40 || !strings.HasPrefix(head.Hash, head.Short) {
		t.Fatalf("metadata: %+v", head)
	}
	if _, err := time.Parse(time.RFC3339, head.Time); err != nil {
		t.Fatal(err)
	}
	if l, err := readGitLog(ctx, root, 2); err != nil || len(l.Commits) != 2 || !l.Truncated {
		t.Fatalf("limited: %v %+v", err, l)
	}
	var mainWork protocol.GitCommit
	for _, c := range l.Commits {
		if c.Subject == "main work" {
			mainWork = c
		}
	}
	show, err := readGitShow(ctx, root, mainWork.Short)
	if err != nil || show.Commit.Hash != mainWork.Hash || !strings.Contains(show.Commit.Body, "Body paragraph.") || !strings.Contains(show.Text, "c.txt | 1 +") || !strings.Contains(show.Text, "+c") {
		t.Fatalf("show: %v %+v", err, show)
	}
	for _, bad := range []struct{ commit, code string }{
		{"-p", "invalid"}, {"--output=x", "invalid"}, {"zzzz", "invalid"}, {"HEAD", "invalid"}, {"abc", "invalid"}, {"", "invalid"},
		{mainWork.Hash[:12] + "~1", "invalid"}, {strings.Repeat("0", 40), "not_found"},
	} {
		_, err := readGitShow(ctx, root, bad.commit)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != bad.code {
			t.Errorf("%q: want %s got %v", bad.commit, bad.code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("--output wrote a file")
	}
}

func TestGitAheadBehindLocalUpstream(t *testing.T) {
	base, git := gitFixture(t)
	bare := filepath.Join(base, "remote.git")
	git("init", "--bare", bare)
	clone := filepath.Join(base, "clone")
	gitIn(t, base, "clone", bare, clone)
	writeFile(t, clone, "a.txt", "a\n")
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-m", "one")
	gitIn(t, clone, "push", "origin", "main")
	gitIn(t, clone, "commit", "--allow-empty", "-m", "local")
	gitIn(t, clone, "update-ref", "refs/remotes/origin/main", "HEAD~1")
	other := gitIn(t, clone, "commit-tree", "-p", "HEAD~1", "-m", "remote", "HEAD^{tree}")
	gitIn(t, clone, "update-ref", "refs/remotes/origin/main", other)
	s := mustStatus(t, clone)
	if s.Upstream != "origin/main" || s.Ahead != 1 || s.Behind != 1 {
		t.Fatalf("ahead/behind: %+v", s)
	}
	gitIn(t, clone, "checkout", "-b", "nou")
	if s := mustStatus(t, clone); s.Upstream != "" || s.Ahead != 0 || s.Behind != 0 {
		t.Fatalf("no upstream: %+v", s)
	}
}

func TestGitStatusIsReadOnly(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "one")
	index := filepath.Join(root, ".git", "index")
	// Make the index stat-dirty so an ordinary status would want to refresh it.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		mustStatus(t, root)
		if _, err := readGitDiff(context.Background(), root, "a.txt", "unstaged"); err == nil {
			t.Fatal("stat-only change reported as diffable")
		}
		if _, err := readGitLog(context.Background(), root, 5); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("status rewrote the index")
	}
	if _, err := os.Stat(index + ".lock"); err == nil {
		t.Fatal("index.lock left behind")
	}
}

func TestGitHandlerTargets(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	writeFile(t, root, "a.txt", "a\n")
	e := &engine{snap: protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: root}}, Threads: []protocol.Thread{{ID: "t", Checkout: root}}}}
	for _, tc := range []struct {
		handler func(http.ResponseWriter, *http.Request)
		query   string
		status  int
	}{
		{e.gitStatus, "project_id=p", 200}, {e.gitStatus, "thread_id=t", 200}, {e.gitStatus, "", 400},
		{e.gitStatus, "project_id=p&thread_id=t", 400}, {e.gitStatus, "project_id=missing", 404},
		{e.gitDiff, "project_id=p&path=a.txt&group=untracked", 200}, {e.gitDiff, "project_id=p&path=../a&group=untracked", 400},
		{e.gitDiff, "project_id=p&path=b.txt&group=untracked", 404},
		{e.gitLog, "project_id=p&limit=0", 400}, {e.gitLog, "project_id=p&limit=999", 200},
		{e.gitShow, "project_id=p&commit=-p", 400},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, httptest.NewRequest("GET", "/v1/git/x?"+tc.query, nil))
		if w.Code != tc.status {
			t.Errorf("%s: status %d: %s", tc.query, w.Code, w.Body.String())
		}
		if !json.Valid(w.Body.Bytes()) {
			t.Errorf("%s: invalid JSON", tc.query)
		}
	}
}

func TestGitRoutesRequireAuthentication(t *testing.T) {
	home := t.TempDir()
	c, stop := startTestServer(t, home)
	defer stop()
	for _, route := range []string{"status", "diff", "log", "show"} {
		resp, err := http.Get(c.Discovery.URL + "/v1/git/" + route + "?project_id=x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%s unauthenticated: %d", route, resp.StatusCode)
		}
	}
	_, err := c.GitStatus(context.Background(), client.GitTarget{ProjectID: "missing"})
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "not_found" {
		t.Fatalf("client route: %v", err)
	}
}

func TestGitLocalFiltersNeutralizedGlobalHonored(t *testing.T) {
	root, git := gitFixture(t)
	marks := t.TempDir()
	global := filepath.Join(os.Getenv("HOME"), ".gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"glob\"]\n\tclean = \"sh -c 'touch "+marks+"/GLOBAL; cat'\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init")
	writeFile(t, root, ".gitattributes", "*.l filter=pwn\n*.g filter=glob\n")
	writeFile(t, root, "a.l", "a\n")
	writeFile(t, root, "a.g", "g\n")
	git("add", ".")
	git("commit", "-m", "one")
	include := filepath.Join(t.TempDir(), "inc.cfg")
	if err := os.WriteFile(include, []byte("[filter \"inc\"]\n\tprocess = \"sh -c 'touch "+marks+"/INCLUDE'\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("config", "filter.pwn.clean", "sh -c 'touch "+marks+"/LOCAL; cat'")
	git("config", "filter.pwn.process", "sh -c 'touch "+marks+"/LOCAL; exit 1'")
	git("config", "filter.pwn.required", "true")
	git("config", "include.path", include)
	writeFile(t, root, ".gitattributes", "*.l filter=pwn\n*.g filter=glob\n*.i filter=inc\n")
	writeFile(t, root, "a.l", "a\nchanged\n")
	writeFile(t, root, "a.g", "g\nchanged\n")
	writeFile(t, root, "b.i", "i\n")
	git("add", "b.i", ".gitattributes")
	_ = os.Remove(filepath.Join(marks, "GLOBAL"))
	_ = os.Remove(filepath.Join(marks, "INCLUDE"))
	s := mustStatus(t, root)
	if hasEntry(s, "a.l", "unstaged") == nil || hasEntry(s, "a.g", "unstaged") == nil {
		t.Fatalf("status: %+v", s)
	}
	if d, err := readGitDiff(context.Background(), root, "a.l", "unstaged"); err != nil || !strings.Contains(d.Text, "+changed") {
		t.Fatalf("diff: %v %+v", err, d)
	}
	if _, err := readGitDiff(context.Background(), root, "a.g", "unstaged"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LOCAL", "INCLUDE"} {
		if _, err := os.Stat(filepath.Join(marks, name)); err == nil {
			t.Fatalf("%s filter executed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(marks, "GLOBAL")); err != nil {
		t.Fatal("global filter was not honored")
	}
}

func TestGitPartialCloneNeverFetches(t *testing.T) {
	base, git := gitFixture(t)
	src := filepath.Join(base, "src")
	gitIn(t, base, "init", src)
	writeFile(t, src, "f.txt", "one\n")
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-m", "one")
	writeFile(t, src, "f.txt", "two\n")
	gitIn(t, src, "commit", "-am", "two")
	gitIn(t, src, "config", "uploadpack.allowFilter", "true")
	clone := filepath.Join(base, "clone")
	git("clone", "--filter=blob:none", "file://"+src, clone)
	marker := filepath.Join(base, "UPLOADPACK")
	gitIn(t, clone, "config", "remote.origin.uploadpack", "sh -c 'touch "+marker+"; git-upload-pack \"$@\"' --")
	packs := func() int {
		m, _ := filepath.Glob(filepath.Join(clone, ".git", "objects", "pack", "*.pack"))
		return len(m)
	}
	before := packs()
	first := gitIn(t, clone, "rev-parse", "HEAD~1")
	_, err := readGitShow(context.Background(), clone, first)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "unavailable" || !strings.Contains(pe.Message, "partial clone") {
		t.Fatalf("partial clone show: %v", err)
	}
	if _, err := os.Stat(marker); err == nil || packs() != before {
		t.Fatal("read contacted the promisor remote")
	}
}

func TestGitShowRejectsHexBranchAndKeepsBody(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	git("commit", "--allow-empty", "-m", "one")
	git("commit", "--allow-empty", "-m", "two\n\nbody \x1e with separator\x1e end")
	head := git("rev-parse", "HEAD")
	name := "deadbeef"
	if strings.HasPrefix(head, name) {
		name = "cafebabe"
	}
	git("branch", name, "HEAD~1")
	if _, err := readGitShow(context.Background(), root, name); err == nil || !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("hex branch accepted: %v", err)
	}
	s, err := readGitShow(context.Background(), root, strings.ToUpper(head[:10]))
	if err != nil || !strings.Contains(s.Commit.Body, "\x1e end") || s.Truncated {
		t.Fatalf("body: %v %q", err, s.Commit.Body)
	}
}

func TestGitParseCommitsKeepsCompleteRecords(t *testing.T) {
	rec := func(h string) string {
		return strings.Join([]string{h, h[:2], "", "A", "a@x", "1", "s", ""}, "\x00") + "\x00"
	}
	out := rec("aaaa") + rec("bbbb") + "cc"
	if got := parseGitCommits([]byte(out)); len(got) != 2 || got[1].Hash != "bbbb" {
		t.Fatalf("%+v", got)
	}
}

func TestGitUntrackedSpecialFiles(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	d, err := readGitDiff(context.Background(), root, "link", "untracked")
	if err != nil || !strings.Contains(d.Text, "new file mode 120000") || strings.Contains(d.Text, "+secret") || !strings.Contains(d.Text, "+"+outside) {
		t.Fatalf("symlink: %v %s", err, d.Text)
	}
	if _, err := readUntracked(root, "linkdir/secret.txt", 1024); err == nil {
		t.Fatal("followed a symlinked directory")
	}
	if _, err := readGitDiff(context.Background(), root, "linkdir/secret.txt", "untracked"); err == nil {
		t.Fatal("diff through symlinked directory")
	}
	if err := exec.Command("mkfifo", filepath.Join(root, "fifo")).Run(); err == nil {
		done := make(chan error, 1)
		go func() { _, err := readUntracked(root, "fifo", 1024); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("fifo accepted")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("fifo read blocked")
		}
	}
	writeFile(t, root, "nonl.txt", "a\nb")
	if d, err := readGitDiff(context.Background(), root, "nonl.txt", "untracked"); err != nil || !strings.Contains(d.Text, "@@ -0,0 +1,2 @@\n+a\n+b\n\\ No newline at end of file\n") {
		t.Fatalf("no newline: %v %q", err, d.Text)
	}
	writeFile(t, root, "empty.txt", "")
	if d, err := readGitDiff(context.Background(), root, "empty.txt", "untracked"); err != nil || strings.Contains(d.Text, "@@") {
		t.Fatalf("empty: %v %q", err, d.Text)
	}
	nested := filepath.Join(root, "sub")
	gitIn(t, root, "init", nested)
	gitIn(t, nested, "commit", "--allow-empty", "-m", "x")
	_, err = readGitDiff(context.Background(), root, "sub/", "untracked")
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "not_diffable" {
		t.Fatalf("nested repo: %v", err)
	}
}

func TestGitCraftedFilterNamesCannotBypassOrInject(t *testing.T) {
	root, git := gitFixture(t)
	marks := t.TempDir()
	script := filepath.Join(marks, "mk.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$0.ran.$1\"\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("init")
	names := []string{"a=b", "sp ace", "h#sh", "d.o.t", "x"}
	var attrs strings.Builder
	for i, name := range names {
		fmt.Fprintf(&attrs, "f%d filter=%s\n", i, name)
		writeFile(t, root, fmt.Sprintf("f%d", i), "one\n")
	}
	writeFile(t, root, ".gitattributes", attrs.String())
	git("add", ".")
	git("commit", "-m", "one")
	for i, name := range names[:4] {
		git("config", "filter."+name+".clean", fmt.Sprintf("%s BYPASS%d", script, i))
	}
	// Plain git never runs this; a naive -c override would synthesize
	// filter.x.process=<script> INJ.
	git("config", "filter.x.clean", "cat")
	git("config", "filter.x.process="+script+" INJ #.required", "false")
	for i := range names {
		writeFile(t, root, fmt.Sprintf("f%d", i), "one\ntwo\n")
	}
	mustStatus(t, root)
	for i := range names {
		if _, err := readGitDiff(context.Background(), root, fmt.Sprintf("f%d", i), "unstaged"); err != nil {
			t.Fatal(err)
		}
	}
	if ran, _ := filepath.Glob(script + ".ran.*"); len(ran) != 0 {
		t.Fatalf("filter command executed: %v", ran)
	}
}

func TestGitDiffDoesNotEnterSubmodules(t *testing.T) {
	base, _ := gitFixture(t)
	marker := filepath.Join(base, "SUBFILTER")
	sub := filepath.Join(base, "subsrc")
	gitIn(t, base, "init", sub)
	writeFile(t, sub, ".gitattributes", "* filter=s\n")
	writeFile(t, sub, "f", "x\n")
	gitIn(t, sub, "add", ".")
	gitIn(t, sub, "commit", "-m", "s")
	top := filepath.Join(base, "top")
	gitIn(t, base, "init", top)
	gitIn(t, top, "-c", "protocol.file.allow=always", "submodule", "add", sub, "sub")
	gitIn(t, top, "commit", "-m", "t")
	inner := filepath.Join(top, "sub")
	gitIn(t, inner, "commit", "--allow-empty", "-m", "moved")
	gitIn(t, inner, "config", "filter.s.clean", "sh -c 'touch "+marker+"; cat'")
	writeFile(t, inner, "f", "dirty\n")
	s := mustStatus(t, top)
	if e := hasEntry(s, "sub", "unstaged"); e == nil || !e.Submodule {
		t.Fatalf("submodule entry: %+v", s.Entries)
	}
	if d, err := readGitDiff(context.Background(), top, "sub", "unstaged"); err != nil || !strings.Contains(d.Text, "Subproject commit") {
		t.Fatalf("submodule diff: %v %+v", err, d)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran inside the submodule")
	}
}

func TestGitProjectAtRepositorySubdirectory(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	writeFile(t, root, "top.txt", "a\n")
	writeFile(t, root, "sub/in.txt", "b\n")
	git("add", ".")
	git("commit", "-m", "one")
	writeFile(t, root, "top.txt", "a\nchanged\n")
	writeFile(t, root, "sub/new.txt", "fresh\n")
	project := filepath.Join(root, "sub")
	s := mustStatus(t, project)
	if hasEntry(s, "top.txt", "unstaged") == nil || hasEntry(s, "sub/new.txt", "untracked") == nil {
		t.Fatalf("status: %+v", s.Entries)
	}
	if d, err := readGitDiff(context.Background(), project, "top.txt", "unstaged"); err != nil || !strings.Contains(d.Text, "+changed") {
		t.Fatalf("tracked: %v %+v", err, d)
	}
	if d, err := readGitDiff(context.Background(), project, "sub/new.txt", "untracked"); err != nil || !strings.Contains(d.Text, "+fresh") {
		t.Fatalf("untracked: %v %+v", err, d)
	}
}

func TestGitNoisyGlobalFilterStderr(t *testing.T) {
	root, git := gitFixture(t)
	global := filepath.Join(os.Getenv("HOME"), ".gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"noisy\"]\n\tclean = \"sh -c 'head -c 262144 /dev/zero | tr \\\\\\\\0 x >&2; cat'\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init")
	writeFile(t, root, ".gitattributes", "*.n filter=noisy\n")
	writeFile(t, root, "a.n", "a\n")
	git("add", ".")
	git("commit", "-m", "one")
	writeFile(t, root, "a.n", "a\nb\n")
	if d, err := readGitDiff(context.Background(), root, "a.n", "unstaged"); err != nil || !strings.Contains(d.Text, "+b") {
		t.Fatalf("noisy filter: %v %+v", err, d)
	}
}

func TestGitWalkParentFallback(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/f.txt", "x\n")
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFD)
	fd, err := walkParent(rootFD, "a/b")
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(fd, "f.txt", &st, 0); err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	for _, dir := range []string{"link/b", "link", "a/missing"} {
		if fd, err := walkParent(rootFD, dir); err == nil {
			unix.Close(fd)
			t.Fatalf("walked %s", dir)
		}
	}
}
