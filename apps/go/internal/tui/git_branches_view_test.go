package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func graphLog() protocol.GitLog {
	l := protocol.GitLog{Workspace: protocol.WorkspaceInfo{State: "branch"}, Commits: graphCommits("m:d,c* d:b c:b b:a a")}
	for i := range l.Commits {
		l.Commits[i].Time = gitTestNow.Format("2006-01-02T15:04:05Z07:00")
	}
	return l
}

func testBranches() protocol.GitBranches {
	return protocol.GitBranches{Branches: []protocol.GitBranch{
		{Name: "main", Ref: "refs/heads/main", Head: true, Upstream: "origin/main", Ahead: 2, Behind: 1},
		{Name: "side\x1b[31m", Ref: "refs/heads/side", WorktreePath: "/wt"},
		{Name: "old", Ref: "refs/heads/old", Upstream: "origin/old", UpstreamGone: true},
		{Name: "origin/main", Ref: "refs/remotes/origin/main", Remote: true},
	}}
}

func TestGitGraphRowsInSurface(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	api.log = graphLog()
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	text := gitSurfaceText(m)
	for _, want := range []string{"◉─╮ m", "● │ d", "│ ● c", "●─╯ b", "Scope: [HEAD] | All branches"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	screen := screenText(m)
	if !strings.Contains(screen, "◉─╮") || !strings.Contains(screen, "●─╯") {
		t.Fatalf("graph not painted:\n%s", screen)
	}
	m.plainIcons = true
	m.markDirty()
	if screen := screenText(m); !strings.Contains(screen, "@-\\") || !strings.Contains(screen, "*-/") {
		t.Fatalf("plain graph not painted:\n%s", screen)
	}
	for i, row := range m.render().rows {
		if w := ansi.StringWidth(row); w != m.width {
			t.Fatalf("row %d is %d cells", i, w)
		}
	}
}

func TestGitScopeToggle(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	gitSettle(t, m, m.activate(action{Kind: "git-scope"}))
	if api.count("scope:all") != 1 || m.currentGitView().logScope != protocol.GitLogScopeAll {
		t.Fatalf("scope all not read: %v", api.calls)
	}
	if !strings.Contains(gitSurfaceText(m), "Scope: HEAD | [All branches]") {
		t.Fatal("scope toggle not shown as all")
	}
	// Keyboard: focus the scope row and press enter.
	m.setFocus("git:scope")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gitSettle(t, m, cmd)
	if api.count("scope:head") != 2 || m.currentGitView().logScope != protocol.GitLogScopeHead {
		t.Fatalf("keyboard toggle: %v", api.calls)
	}
}

func TestGitBranchSectionAndCompare(t *testing.T) {
	m, api := gitModel(t, 144, 80)
	api.branch = testBranches()
	text := gitSurfaceText(m)
	if !strings.Contains(text, "+ BRANCHES") || strings.Contains(text, "origin/main ") || api.count("branches:") != 0 {
		t.Fatalf("branches should start collapsed and unread:\n%s", text)
	}
	gitSettle(t, m, m.activate(action{Kind: "git-branches"}))
	text = gitSurfaceText(m)
	for _, want := range []string{"- BRANCHES 4", "* main ↑2 ↓1", "old gone", "origin/main"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(screenText(m), "\x1b[31m") {
		t.Fatal("unsanitized branch name")
	}
	// A refresh re-reads the expanded list.
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if api.count("branches:") != 2 {
		t.Fatalf("refresh did not re-read branches: %v", api.calls)
	}
	// Row activation opens a read-only comparison against HEAD.
	api.cmp = protocol.GitCompare{Base: "refs/remotes/origin/main", Head: "HEAD", BaseOid: strings.Repeat("a", 40), HeadOid: strings.Repeat("b", 40), MergeBase: strings.Repeat("c", 40),
		Ahead: 1, Behind: 2, AheadCommits: []protocol.GitCommit{{Short: "bbbbbbb", Subject: "mine\x1b]0;x\x07"}}, BehindCommits: []protocol.GitCommit{{Short: "aaaaaaa", Subject: "theirs"}},
		Text: "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n", Bytes: 30, FetchedAt: "2026-09-24T10:00:00Z"}
	var row *gitRow
	for _, r := range gitRows(m, 60) {
		if r.git != nil && r.git.key == "git:branch:refs/remotes/origin/main" {
			row = r.git
		}
	}
	if row == nil {
		t.Fatal("remote branch row missing")
	}
	gitSettle(t, m, m.activate(row.action))
	if api.count("compare:thread-shell:refs/remotes/origin/main:HEAD") != 1 || m.viewer == nil {
		t.Fatalf("compare not read: %v", api.calls)
	}
	pairs := m.gitViewerPairs()
	joined := ""
	for _, p := range pairs {
		joined += p[0] + "=" + p[1] + ";"
	}
	for _, want := range []string{"Base=origin/main aaaaaaaaaaaa", "Ahead=1", "Behind=2", "Merge base=cccccccccccc", "Upstream as of="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing pair %q in %s", want, joined)
		}
	}
	screen := screenText(m)
	if !strings.Contains(screen, "Ahead · only in HEAD (1)") || !strings.Contains(screen, "theirs") || strings.Contains(screen, "\x07") {
		t.Fatalf("compare body:\n%s", screen)
	}
}

func TestGitWholeGroupDiff(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	api.diff = protocol.GitDiff{Group: "staged", Text: "diff --git a/a b/a\n", Bytes: 19}
	gitSettle(t, m, m.activate(action{Kind: "git-whole", Value: "staged"}))
	if api.count("diff:thread-shell:staged:") != 1 || m.viewer == nil || m.viewer.att.Name != "Staged vs HEAD" {
		t.Fatalf("whole staged diff: %v", api.calls)
	}
	// The whole view is not a pinned entry: write keys are refused.
	if m.viewer.git.pinned {
		t.Fatal("whole diff pinned for writes")
	}
}

func TestGitBranchStaleResultDropped(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	api.branch = testBranches()
	key, target := m.gitTarget()
	g := m.gitViews[key]
	g.branchOpen = true
	old := m.readGitBranches(key, target, g)
	fresh := m.readGitBranches(key, target, g)
	gitSettle(t, m, old)
	if g.branches != nil {
		t.Fatal("older branch read applied")
	}
	gitSettle(t, m, fresh)
	if g.branches == nil || len(g.branches.Branches) != 4 {
		t.Fatal("current branch read dropped")
	}
	m.Update(gitLogMsg{key: "other", gen: g.branchGen, branchRead: true, branches: &protocol.GitBranches{}})
	if len(g.branches.Branches) != 4 {
		t.Fatal("other target applied")
	}
}

func TestGitNoSpuriousPendingOnRefresh(t *testing.T) {
	m, _ := gitModel(t, 144, 60)
	// Start a refresh and inspect before results arrive.
	m.refreshGit()
	if strings.Contains(gitSurfaceText(m), "Reading commits…") || m.currentGitView().scopePending() {
		t.Fatalf("plain refresh shows scope pending:\n%s", gitSurfaceText(m))
	}
}

func TestGitScopeToggleOfflineAndFailure(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	m.connected = false
	m.activate(action{Kind: "git-scope"})
	if g := m.currentGitView(); g.wantScope() != protocol.GitLogScopeHead || !strings.Contains(gitSurfaceText(m), "[HEAD]") {
		t.Fatal("offline toggle changed the selection")
	}
	m.connected = true
	// Mid-flight: selection stays on the displayed scope, marked pending.
	cmd := m.activate(action{Kind: "git-scope"})
	if text := gitSurfaceText(m); !strings.Contains(text, "Scope: [HEAD] | All branches …") {
		t.Fatalf("pending not shown:\n%s", text)
	}
	api.err = errors.New("timed out")
	gitSettle(t, m, cmd)
	if g := m.currentGitView(); g.wantScope() != protocol.GitLogScopeHead || g.shownScope() != protocol.GitLogScopeHead || !strings.Contains(gitSurfaceText(m), "Scope: [HEAD] | All branches\n") {
		t.Fatalf("failed toggle did not revert:\n%s", gitSurfaceText(m))
	}
}

func TestGitBranchLoadClearedWhenDroppedForTarget(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	api.branch = testBranches()
	key, target := m.gitTarget()
	g := m.gitViews[key]
	cmd := m.readGitBranches(key, target, g)
	m.state.Active = "thread-review"
	gitSettle(t, m, cmd)
	if g.branchLoad || g.branches != nil {
		t.Fatalf("dropped result left loading or applied: %+v", g)
	}
}

func TestGitHistoryCapabilityGate(t *testing.T) {
	m, api := gitModel(t, 144, 60)
	m.snapshot.Capabilities = nil
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	text := gitSurfaceText(m)
	if strings.Contains(text, "Scope:") || !strings.Contains(text, "need a newer server") || strings.Contains(text, "+ BRANCHES") {
		t.Fatalf("ungated:\n%s", text)
	}
	if api.count("scope:all") != 0 || api.count("scope:head") != 1 || api.count("scope:") != 2 {
		t.Fatalf("scope sent to old server: %v", api.calls)
	}
	m.activate(action{Kind: "git-branches"})
	if api.count("branches:") != 0 {
		t.Fatal("branches read without capability")
	}
}
