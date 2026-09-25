package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func commitFile(t *testing.T, root string, git func(...string) string, name, content, msg string) {
	t.Helper()
	writeFile(t, root, name, content)
	git("add", name)
	git("commit", "-m", msg)
}

// branchedRepo: main a-b-m(merge of side)-d, side c, origin clone tracking.
func branchedRepo(t *testing.T) (root string, git func(...string) string) {
	remote, rgit := gitFixture(t)
	rgit("init")
	commitFile(t, remote, rgit, "a.txt", "a\n", "a")
	commitFile(t, remote, rgit, "b.txt", "b\n", "b")
	root = t.TempDir()
	gitIn(t, filepath.Dir(root), "clone", "-q", remote, root)
	git = func(args ...string) string { return gitIn(t, root, args...) }
	git("checkout", "-q", "-b", "side")
	commitFile(t, root, git, "c.txt", "c\n", "side c")
	git("checkout", "-q", "main")
	commitFile(t, root, git, "d.txt", "d\n", "main d")
	git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	commitFile(t, remote, rgit, "r.txt", "r\n", "remote r")
	git("fetch", "-q", "origin")
	return root, git
}

func TestGitLogTopoOrderAndScope(t *testing.T) {
	root, git := branchedRepo(t)
	ctx := context.Background()
	git("branch", "lonely", "HEAD~1")
	git("checkout", "-q", "lonely")
	commitFile(t, root, git, "l.txt", "l\n", "lonely l")
	git("checkout", "-q", "main")
	l, err := readGitLogScope(ctx, root, 50, protocol.GitLogScopeHead)
	if err != nil || l.Upstream != "refs/remotes/origin/main" || l.Scope != "head" {
		t.Fatalf("head scope: %v %+v", err, l)
	}
	subjects := func(l protocol.GitLog) string {
		var s []string
		for _, c := range l.Commits {
			s = append(s, c.Subject)
		}
		return strings.Join(s, ",")
	}
	got := subjects(l)
	if strings.Contains(got, "lonely") || !strings.Contains(got, "remote r") {
		t.Fatalf("head scope subjects: %s", got)
	}
	// Topological: every commit precedes its parents.
	for _, log := range []protocol.GitLog{l} {
		seen := map[string]bool{}
		for _, c := range log.Commits {
			for _, p := range c.Parents {
				if seen[p] {
					t.Fatalf("parent %s before child %s", p, c.Hash)
				}
			}
			seen[c.Hash] = true
		}
	}
	all, err := readGitLogScope(ctx, root, 50, protocol.GitLogScopeAll)
	if err != nil || !strings.Contains(subjects(all), "lonely l") || !strings.Contains(subjects(all), "remote r") {
		t.Fatalf("all scope: %v %s", err, subjects(all))
	}
	// No upstream: head scope is HEAD only.
	git("checkout", "-q", "lonely")
	l, err = readGitLogScope(ctx, root, 50, protocol.GitLogScopeHead)
	if err != nil || l.Upstream != "" || strings.Contains(subjects(l), "remote r") {
		t.Fatalf("no upstream: %v %+v", err, l)
	}
}

func TestGitBranchesList(t *testing.T) {
	root, git := branchedRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-q", wt, "side")
	b, err := readGitBranches(context.Background(), root)
	if err != nil || b.Truncated {
		t.Fatalf("%v %+v", err, b)
	}
	byName := map[string]protocol.GitBranch{}
	var order []string
	for _, br := range b.Branches {
		byName[br.Name] = br
		order = append(order, br.Name)
		if br.Ref == "refs/remotes/origin/HEAD" {
			t.Fatal("symbolic remote HEAD listed")
		}
	}
	main := byName["main"]
	if !main.Head || main.Upstream != "origin/main" || main.Ahead != 3 || main.Behind != 1 || main.Remote || main.Ref != "refs/heads/main" || len(main.Tip) != 40 {
		t.Fatalf("main: %+v", main)
	}
	side := byName["side"]
	if side.Head || side.WorktreePath == "" || !strings.HasSuffix(side.WorktreePath, "wt") {
		t.Fatalf("side: %+v", side)
	}
	if r := byName["origin/main"]; !r.Remote || r.Ref != "refs/remotes/origin/main" {
		t.Fatalf("remote: %+v order %v", r, order)
	}
	if order[len(order)-1] != "origin/main" {
		t.Fatalf("locals first: %v", order)
	}
	// Gone upstream.
	git("branch", "-q", "tracked", "origin/main")
	git("branch", "-q", "--set-upstream-to=origin/main", "tracked")
	git("update-ref", "-d", "refs/remotes/origin/main")
	b, _ = readGitBranches(context.Background(), root)
	for _, br := range b.Branches {
		if br.Name == "tracked" && !br.UpstreamGone {
			t.Fatalf("gone: %+v", br)
		}
	}
}

func TestGitCompare(t *testing.T) {
	root, git := branchedRepo(t)
	ctx := context.Background()
	c, err := readGitCompare(ctx, root, "refs/remotes/origin/main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if c.Ahead != 3 || c.Behind != 1 || len(c.AheadCommits) != 3 || len(c.BehindCommits) != 1 || c.BehindCommits[0].Subject != "remote r" || c.MergeBase == "" || c.FetchedAt == "" {
		t.Fatalf("compare: %+v", c)
	}
	// Merge-base diff: head's changes only, not the remote's r.txt.
	if !strings.Contains(c.Text, "+++ b/c.txt") || !strings.Contains(c.Text, "+++ b/d.txt") || strings.Contains(c.Text, "r.txt") {
		t.Fatalf("diff: %s", c.Text)
	}
	head := git("rev-parse", "HEAD")
	if c2, err := readGitCompare(ctx, root, "refs/heads/side", head); err != nil || c2.HeadOid != head || c2.Behind != 0 {
		t.Fatalf("hash compare: %v %+v", err, c2)
	}
	for _, bad := range []struct{ base, code string }{
		{"-p", "invalid"}, {"--output=x", "invalid"}, {"main", "invalid"}, {"HEAD~1", "invalid"}, {"refs/heads/main..HEAD", "invalid"},
		{"refs/heads/-x", "invalid"}, {"refs/tags/v1", "invalid"}, {head[:12], "invalid"}, {strings.ToUpper(head), "invalid"}, {"", "invalid"},
		{"refs/heads/main@{1}", "invalid"}, {"refs/heads/nope", "not_found"}, {strings.Repeat("0", 40), "not_found"},
	} {
		_, err := readGitCompare(ctx, root, bad.base, "HEAD")
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != bad.code {
			t.Errorf("%q: want %s got %v", bad.base, bad.code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("--output wrote a file")
	}
	// A hex-named branch does not stand in for a hash.
	git("branch", strings.Repeat("a", 40), "HEAD~1")
	if _, err := readGitCompare(ctx, root, strings.Repeat("a", 40), "HEAD"); err == nil {
		t.Fatal("hex branch resolved as hash")
	}
	// Unrelated histories: no merge base, no diff.
	git("checkout", "-q", "--orphan", "island")
	git("rm", "-rq", "--cached", ".")
	commitFile(t, root, git, "island.txt", "i\n", "island")
	c, err = readGitCompare(ctx, root, "refs/heads/main", "HEAD")
	if err != nil || c.MergeBase != "" || c.Text != "" || c.Ahead != 1 {
		t.Fatalf("unrelated: %v %+v", err, c)
	}
}

func TestGitCompareBounded(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	commitFile(t, root, git, "a.txt", "a\n", "base")
	git("checkout", "-q", "-b", "big")
	for i := 0; i < gitCompareMaxCommit+5; i++ {
		git("commit", "-q", "--allow-empty", "-m", "empty")
	}
	writeFile(t, root, "huge.txt", strings.Repeat("line of text\n", gitDiffMaxBytes/8))
	git("add", ".")
	git("commit", "-qm", "huge")
	c, err := readGitCompare(context.Background(), root, "refs/heads/main", "refs/heads/big")
	if err != nil || !c.CommitsTruncated || len(c.AheadCommits) != gitCompareMaxCommit || c.Ahead != gitCompareMaxCommit+6 || !c.Truncated || c.Bytes > gitDiffMaxBytes || !strings.HasSuffix(c.Text, "\n") {
		t.Fatalf("bounded: %v ahead=%d n=%d trunc=%v bytes=%d", err, c.Ahead, len(c.AheadCommits), c.Truncated, c.Bytes)
	}
}

func TestGitWholeDiff(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	commitFile(t, root, git, "a.txt", "a\n", "a")
	writeFile(t, root, "a.txt", "a2\n")
	writeFile(t, root, "s.txt", "s\n")
	git("add", "s.txt")
	ctx := context.Background()
	d, err := readGitDiff(ctx, root, "", protocol.GitGroupStaged)
	if err != nil || !strings.Contains(d.Text, "b/s.txt") || strings.Contains(d.Text, "a.txt") {
		t.Fatalf("staged: %v %+v", err, d)
	}
	d, err = readGitDiff(ctx, root, "", protocol.GitGroupUnstaged)
	if err != nil || !strings.Contains(d.Text, "+a2") || strings.Contains(d.Text, "s.txt") {
		t.Fatalf("unstaged: %v %+v", err, d)
	}
	if _, err := readGitDiff(ctx, root, "", protocol.GitGroupUntracked); err == nil {
		t.Fatal("untracked whole diff accepted")
	}
}

func TestValidFullRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"refs/heads/main": true, "refs/remotes/origin/feature/x": true, "refs/heads/a.b": true,
		"refs/heads/": false, "refs/heads/a..b": false, "refs/heads/.x": false, "refs/heads/x.lock": false,
		"refs/heads/a b": false, "refs/heads/a~1": false, "refs/heads/a^": false, "refs/heads/a:b": false,
		"refs/heads/a\x1b[31m": false, "refs/tags/v1": false, "heads/main": false, "refs/heads/a/-b": false,
		"refs/heads/feature/ümlaut": true, "refs/heads/fix{1}": true, "refs/heads/a;b": true, "refs/heads/-x": false,
		"refs/heads/a//b": false, "refs/heads/a/": false, "refs/heads/@": false, "refs/heads/a@{1}": false,
		"refs/heads/a\\b": false, "refs/heads/a?": false, "refs/heads/a*": false, "refs/heads/a[": false, "refs/heads/a\x7f": false,
	} {
		if validFullRef(ref) != want {
			t.Errorf("%q: want %v", ref, want)
		}
	}
}

func TestGitBranchesUnicodeHeadAndOmitted(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	commitFile(t, root, git, "a.txt", "a\n", "a")
	git("checkout", "-q", "-b", "feature/ümlaut")
	git("branch", "fix{1}")
	git("branch", "a;b")
	// A name git accepts but we never pass back: a component starting "-".
	git("update-ref", "refs/heads/x/-y", "HEAD")
	b, err := readGitBranches(context.Background(), root)
	if err != nil || b.Omitted != 1 || b.Truncated {
		t.Fatalf("%v %+v", err, b)
	}
	names := map[string]protocol.GitBranch{}
	for _, br := range b.Branches {
		names[br.Name] = br
	}
	if !names["feature/ümlaut"].Head || names["fix{1}"].Ref == "" || names["a;b"].Ref == "" {
		t.Fatalf("names: %+v", b.Branches)
	}
	if c, err := readGitCompare(context.Background(), root, "refs/heads/feature/ümlaut", "HEAD"); err != nil || c.Ahead != 0 {
		t.Fatalf("compare unicode: %v %+v", err, c)
	}
	l, err := readGitLogScope(context.Background(), root, 10, protocol.GitLogScopeHead)
	if err != nil || len(l.Commits) != 1 {
		t.Fatalf("log on unicode head: %v %+v", err, l)
	}
	// An upstream with an unsafe name is reported, not silently dropped.
	git("update-ref", "refs/remotes/o/-u", "HEAD")
	git("config", "branch.feature/ümlaut.remote", ".")
	git("config", "branch.feature/ümlaut.merge", "refs/heads/x/-y")
	l, err = readGitLogScope(context.Background(), root, 10, protocol.GitLogScopeHead)
	if err != nil || l.Upstream != "" || !l.UpstreamOmitted {
		t.Fatalf("unsafe upstream: %v %+v", err, l)
	}
}

func TestGitBranchesTruncationCountsSkippedRefs(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	commitFile(t, root, git, "a.txt", "a\n", "a")
	head := git("rev-parse", "HEAD")
	var refs strings.Builder
	// main + 500 locals, origin/HEAD (symref) and origin/main: 503 records.
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&refs, "create refs/heads/b%03d %s\n", i, head)
	}
	fmt.Fprintf(&refs, "create refs/remotes/origin/main %s\n", head)
	cmd := exec.Command("git", "-C", root, "update-ref", "--stdin")
	cmd.Stdin = strings.NewReader(refs.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	b, err := readGitBranches(context.Background(), root)
	if err != nil || !b.Truncated || len(b.Branches) != 500 {
		t.Fatalf("502 refs: %v truncated=%v n=%d", err, b.Truncated, len(b.Branches))
	}
	del := exec.Command("git", "-C", root, "update-ref", "--stdin")
	var d strings.Builder
	for i := 497; i < 500; i++ { // leaves main + b000..b496
		fmt.Fprintf(&d, "delete refs/heads/b%03d\n", i)
	}
	del.Stdin = strings.NewReader(d.String())
	if out, err := del.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// main + 497 locals + origin/HEAD + origin/main = 500 records.
	b, err = readGitBranches(context.Background(), root)
	if err != nil || b.Truncated || len(b.Branches) != 499 {
		t.Fatalf("500 records: %v truncated=%v n=%d", err, b.Truncated, len(b.Branches))
	}
}

func TestGitHistoryHandlers(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	commitFile(t, root, git, "a.txt", "a\n", "a")
	e := &engine{snap: protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: root}}}}
	for _, tc := range []struct {
		handler func(http.ResponseWriter, *http.Request)
		query   string
		status  int
		want    string
	}{
		{e.gitBranches, "project_id=p", 200, `"ref":"refs/heads/main"`},
		{e.gitBranches, "", 400, "invalid"},
		{e.gitCompare, "project_id=p&base=refs/heads/main&head=HEAD", 200, `"ahead":0`},
		{e.gitCompare, "project_id=p&base=main&head=HEAD", 400, "invalid"},
		{e.gitCompare, "project_id=p&base=--output=x&head=HEAD", 400, "invalid"},
		{e.gitCompare, "project_id=p&base=refs/heads/nope&head=HEAD", 404, "not_found"},
		{e.gitLog, "project_id=p&scope=all", 200, `"scope":"all"`},
		{e.gitLog, "project_id=p", 200, `"scope":"head"`},
		{e.gitLog, "project_id=p&scope=everything", 400, "invalid"},
		{e.gitDiff, "project_id=p&group=staged", 200, `"group":"staged"`},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, httptest.NewRequest("GET", "/v1/git/x?"+tc.query, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
	}
}
