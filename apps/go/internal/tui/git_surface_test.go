package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type fakeGit struct {
	mu     sync.Mutex
	status protocol.GitStatus
	log    protocol.GitLog
	diff   protocol.GitDiff
	show   protocol.GitShow
	branch protocol.GitBranches
	cmp    protocol.GitCompare
	// oper and preview answer the merge/rebase reads (git_operation_test.go).
	oper    protocol.GitOperationState
	preview protocol.GitIntegratePreview
	// conflictFiles answers GitConflictFile by "path|version".
	conflictFiles map[string]protocol.GitConflictFile
	// rbPlan answers GitRebasePlan (git_rebase_test.go).
	rbPlan protocol.GitRebasePlan
	err    error
	calls  []string
}

func (g *fakeGit) record(call string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
}

func (g *fakeGit) count(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func targetName(t client.GitTarget) string { return t.ThreadID + t.ProjectID }

func (g *fakeGit) GitStatus(_ context.Context, t client.GitTarget) (protocol.GitStatus, error) {
	g.record("status:" + targetName(t))
	return g.status, g.err
}
func (g *fakeGit) GitDiff(_ context.Context, t client.GitTarget, path, group string) (protocol.GitDiff, error) {
	g.record("diff:" + targetName(t) + ":" + group + ":" + path)
	return g.diff, g.err
}
func (g *fakeGit) GitLog(_ context.Context, t client.GitTarget, limit int, scope string) (protocol.GitLog, error) {
	g.record("log:" + targetName(t))
	g.record("scope:" + scope)
	l := g.log
	l.Scope = scope
	if l.Scope == "" {
		l.Scope = protocol.GitLogScopeHead
	}
	return l, g.err
}
func (g *fakeGit) GitBranches(_ context.Context, t client.GitTarget) (protocol.GitBranches, error) {
	g.record("branches:" + targetName(t))
	return g.branch, g.err
}
func (g *fakeGit) GitCompare(_ context.Context, t client.GitTarget, base, head string) (protocol.GitCompare, error) {
	g.record("compare:" + targetName(t) + ":" + base + ":" + head)
	return g.cmp, g.err
}
func (g *fakeGit) GitShow(_ context.Context, t client.GitTarget, commit string) (protocol.GitShow, error) {
	g.record("show:" + targetName(t) + ":" + commit)
	return g.show, g.err
}

var gitTestNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func representativeStatus() protocol.GitStatus {
	return protocol.GitStatus{
		Workspace: protocol.WorkspaceInfo{Path: "/src/repo", Kind: "checkout", State: "branch", Branch: "feature/init"},
		Branch:    "feature/init", Upstream: "origin/feature/init", Ahead: 2, Behind: 1,
		Operation: "merge",
		Entries: []protocol.GitStatusEntry{
			{Path: "conflict.go", Index: "U", Worktree: "U", Group: protocol.GitGroupConflicted},
			{Path: "added.go", Index: "A", Worktree: ".", Group: protocol.GitGroupStaged},
			{Path: "new.go", OrigPath: "old.go", Index: "R", Worktree: ".", Group: protocol.GitGroupStaged},
			{Path: "gone.go", Index: ".", Worktree: "D", Group: protocol.GitGroupUnstaged},
			{Path: "docs/界面/very/long/nested/directory/structure/that/keeps/going/設計.md", Index: ".", Worktree: "M", Group: protocol.GitGroupUnstaged},
			{Path: "scratch.txt", Index: "?", Worktree: "?", Group: protocol.GitGroupUntracked},
		},
		Truncated: true,
	}
}

func representativeLog() protocol.GitLog {
	return protocol.GitLog{
		Workspace: protocol.WorkspaceInfo{State: "branch"},
		Commits: []protocol.GitCommit{
			{Hash: "c8dc889aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Short: "c8dc889", Subject: "Add PTY-backed terminal session package", Author: "Matthew", Time: gitTestNow.Add(-3 * time.Hour).Format(time.RFC3339), Refs: []string{"HEAD", "refs/heads/feature/init"}},
			{Hash: "9f65491bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Short: "9f65491", Subject: "Serialize write-capable turns per checkout", Time: gitTestNow.Add(-2 * 24 * time.Hour).Format(time.RFC3339), Refs: []string{"refs/tags/v0.1", "refs/remotes/origin/master"}},
		},
	}
}

// gitModel opens the Git surface on a connected model with a fake reader
// and applies the reads the surface starts.
func gitModel(t *testing.T, width, height int) (*Model, *fakeGit) {
	t.Helper()
	gitNow = func() time.Time { return gitTestNow }
	t.Cleanup(func() { gitNow = time.Now })
	api := &fakeGit{status: representativeStatus(), log: representativeLog()}
	m := testModel()
	m.connected = true
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-history")
	m.snapshot.Threads[activeThreadIndex(m)].State = "idle"
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.openSurface("git", "")
	gitSettle(t, m, nil)
	return m, api
}

// gitSettle runs cmd (or an idle update) and applies every Git result.
func gitSettle(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		_, cmd = m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	for range 4 {
		var next []tea.Cmd
		for _, msg := range pump(t, m, cmd).msgs {
			switch msg.(type) {
			case gitStatusMsg, gitLogMsg, gitViewerMsg:
				_, c := m.Update(msg)
				next = append(next, c)
			}
		}
		if len(next) == 0 {
			return
		}
		cmd = tea.Batch(next...)
	}
}

func gitSurfaceText(m *Model) string { return m.surfaceText(shell.Surface{Kind: "git"}) }

func TestGitSurfaceRendersGroupsBannerAndUpstream(t *testing.T) {
	m, _ := gitModel(t, 144, 40)
	text := gitSurfaceText(m)
	for _, want := range []string{
		"CONFLICTS 1", "STAGED 2", "CHANGES 2", "UNTRACKED 1",
		"Upstream: origin/feature/init · ↑2 ↓1",
		"Merge in progress", "Read-only here",
		"U conflict.go", "A added.go", "R old.go → new.go", "D gone.go", "? scratch.txt",
		"Showing first 6 changes",
		"RECENT COMMITS 2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	for _, absent := range []string{"Git integration", "Stage", "Commit changes", "Working tree clean"} {
		if strings.Contains(text, absent) {
			t.Fatalf("unexpected %q in\n%s", absent, text)
		}
	}
	// Semantic inks: added green, deleted and conflicted red, untracked muted.
	p := m.colors()
	inks := map[string]string{}
	for _, r := range gitRows(m, 60) {
		if r.git != nil && r.git.hash == "" {
			inks[r.git.path] = r.git.markInk
		}
	}
	if inks["added.go"] != p.green || inks["gone.go"] != p.red || inks["conflict.go"] != p.red || inks["scratch.txt"] != p.muted || inks["old.go → new.go"] != p.blue {
		t.Fatalf("status inks = %v", inks)
	}
}

func TestGitSurfaceTruncatesLongPathsFromTheStart(t *testing.T) {
	m, _ := gitModel(t, 144, 60)
	screen := screenText(m)
	if !strings.Contains(screen, "設計.md") || !strings.Contains(screen, "…") {
		t.Fatalf("long path lost its file name:\n%s", screen)
	}
	for i, row := range m.render().rows {
		if w := ansi.StringWidth(row); w != m.width {
			t.Fatalf("row %d is %d cells, want %d", i, w, m.width)
		}
	}
	// Narrow (phone-width) Surfaces column renders the same rows.
	m.Update(tea.WindowSizeMsg{Width: 44, Height: 40})
	m.selectColumn(shell.RightRegion)
	screen = screenText(m)
	if !strings.Contains(screen, "設計.md") || !strings.Contains(screen, "CONFLICTS") {
		t.Fatalf("compact column missing git rows:\n%s", screen)
	}
	for i, row := range m.render().rows {
		if w := ansi.StringWidth(row); w != m.width {
			t.Fatalf("compact row %d is %d cells, want %d", i, w, m.width)
		}
	}
}

func TestGitSurfaceStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status protocol.GitStatus
		want   string
	}{
		{"clean", protocol.GitStatus{Workspace: protocol.WorkspaceInfo{State: "branch", Branch: "main"}}, "Working tree clean"},
		{"non-git", protocol.GitStatus{Workspace: protocol.WorkspaceInfo{State: "non-git"}}, "No Git repository"},
		{"fixture", protocol.GitStatus{Workspace: protocol.WorkspaceInfo{State: "fixture"}}, "Demo checkout"},
		{"unavailable", protocol.GitStatus{Workspace: protocol.WorkspaceInfo{State: "unavailable", Error: "git not found"}}, "git not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, api := gitModel(t, 144, 40)
			api.status = tc.status
			api.log = protocol.GitLog{}
			gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
			text := gitSurfaceText(m)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %q:\n%s", tc.want, text)
			}
			if tc.name != "clean" && strings.Contains(text, "RECENT COMMITS") {
				t.Fatalf("%s showed a commit list:\n%s", tc.name, text)
			}
			if tc.name == "clean" && strings.Contains(text, "CHANGES") {
				t.Fatalf("clean tree showed an empty section:\n%s", text)
			}
		})
	}
}

func TestGitSurfaceErrorKeepsLastResultAsStale(t *testing.T) {
	m, api := gitModel(t, 144, 40)
	api.err = errors.New("server unavailable")
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Showing the last result") || !strings.Contains(text, "added.go") {
		t.Fatalf("stale data not kept:\n%s", text)
	}
	key, _ := m.gitTarget()
	delete(m.gitViews, key)
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if text := gitSurfaceText(m); !strings.Contains(text, "server unavailable") || strings.Contains(text, "added.go") {
		t.Fatalf("error without data:\n%s", text)
	}
}

func TestGitCommitRowsShowRefsAndAge(t *testing.T) {
	m, _ := gitModel(t, 144, 60)
	text := gitSurfaceText(m)
	if !strings.Contains(text, "c8dc889 (HEAD, feature/init) Add PTY-backed terminal session package · 3h") ||
		!strings.Contains(text, "9f65491 (v0.1, origin/master) Serialize write-capable turns per checkout · 2d") {
		t.Fatalf("commit rows:\n%s", text)
	}
	screen := screenText(m)
	if strings.Contains(screen, "refs/") || !strings.Contains(screen, "HEAD") || !strings.Contains(screen, "3h") {
		t.Fatalf("rendered commit row:\n%s", screen)
	}
}

func TestGitStaleResultDroppedOnTargetChange(t *testing.T) {
	m, api := gitModel(t, 144, 40)
	first := m.state.Active
	firstKey, _ := m.gitTarget()
	cmd := m.refreshGit()
	gen := m.gitViews[firstKey].gen
	// Switch threads before the read returns.
	m.state.Active = "thread-review"
	msgs := pump(t, m, cmd).msgs
	api.status.Branch = "changed"
	for _, msg := range msgs {
		m.Update(msg)
	}
	if g := m.gitViews[firstKey]; g.gen != gen || !g.loading() {
		t.Fatalf("result for %s applied after switching away: %+v", first, g)
	}
	// A result from an older generation for the current target is dropped too.
	key, _ := m.gitTarget()
	m.gitViews[key] = &gitView{gen: 99}
	m.Update(gitStatusMsg{key: key, gen: 98, status: protocol.GitStatus{Branch: "old"}})
	if m.gitViews[key].status != nil {
		t.Fatal("older generation applied")
	}
}

func TestGitRefreshTriggers(t *testing.T) {
	m, api := gitModel(t, 144, 40)
	if api.count("status:thread-shell") != 1 || api.count("log:thread-shell") != 1 {
		t.Fatalf("opening did not read once: %v", api.calls)
	}
	// Ordinary updates do not re-read.
	gitSettle(t, m, nil)
	if api.count("status:") != 1 {
		t.Fatalf("idle update re-read: %v", api.calls)
	}
	// Switching threads while visible reads the new target; switching back
	// renders the retained result immediately and refreshes.
	m.state.Active = "thread-review"
	m.viewState().Host = m.state.Threads["thread-shell"].Host
	gitSettle(t, m, nil)
	if api.count("status:thread-review") != 1 {
		t.Fatalf("switch did not read: %v", api.calls)
	}
	m.state.Active = "thread-shell"
	_, cmd := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	if !strings.Contains(gitSurfaceText(m), "added.go") {
		t.Fatal("retained result not shown immediately")
	}
	gitSettle(t, m, cmd)
	if api.count("status:thread-shell") != 2 {
		t.Fatalf("switch back did not refresh: %v", api.calls)
	}
	// Turn end (running → idle) while visible refreshes.
	ti := activeThreadIndex(m)
	m.snapshot.Threads[ti].State = "running"
	gitSettle(t, m, nil)
	m.snapshot.Threads[ti].State = "idle"
	gitSettle(t, m, nil)
	if api.count("status:thread-shell") != 3 {
		t.Fatalf("turn end did not refresh: %v", api.calls)
	}
	// Explicit Refresh.
	gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if api.count("status:thread-shell") != 4 {
		t.Fatalf("refresh did not read: %v", api.calls)
	}
	// Hidden: switching to another surface stops reads; showing it again reads.
	m.openSurface("plan", "")
	gitSettle(t, m, nil)
	m.snapshot.Threads[ti].State = "running"
	gitSettle(t, m, nil)
	m.snapshot.Threads[ti].State = "idle"
	gitSettle(t, m, nil)
	if api.count("status:thread-shell") != 4 {
		t.Fatalf("hidden surface read: %v", api.calls)
	}
	m.openSurface("git", "")
	gitSettle(t, m, nil)
	if api.count("status:thread-shell") != 5 {
		t.Fatalf("becoming visible did not read: %v", api.calls)
	}
}

func TestGitEntryOpensDiffViewer(t *testing.T) {
	m, api := gitModel(t, 144, 40)
	api.diff = protocol.GitDiff{Path: "gone.go", Group: "unstaged", Bytes: 60,
		Text:      "diff --git a/gone.go b/gone.go\nindex 1..2 100644\n--- a/gone.go\n+++ b/gone.go\n@@ -1,2 +1,2 @@\n context\n-removed\x1b[2J\n--dashes removed\n+added \x1b]52;c;SGVsbG8=\x07tail\n",
		Truncated: true}
	m.setFocus("git:unstaged:gone.go")
	gitSettle(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "gone.go"}))
	if api.count("diff:thread-shell:unstaged:gone.go") != 1 {
		t.Fatalf("diff request: %v", api.calls)
	}
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.att.Name != "gone.go · Unstaged" {
		t.Fatalf("viewer = %+v", vw)
	}
	raw := strings.Join(m.render().rows, "\n")
	for _, bad := range []string{"\x1b]52", "\x1b[2J", "\x07"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("control sequence %q emitted raw", bad)
		}
	}
	screen := screenText(m)
	if !strings.Contains(screen, "Diff truncated at 512 KiB") || !strings.Contains(screen, "+added tail") {
		t.Fatalf("viewer:\n%s", screen)
	}
	p := m.colors()
	lines := m.viewerLines(80)
	inks := map[string]string{}
	for _, l := range lines {
		inks[l.text] = l.ink
	}
	if inks["-removed"] != p.red || inks["--dashes removed"] != p.red || inks["+added tail"] != p.green ||
		inks["@@ -1,2 +1,2 @@"] != p.blue || inks["--- a/gone.go"] != p.muted || inks[" context"] != "" {
		t.Fatalf("diff inks = %v", inks)
	}
	// Expand, then close returns focus to the originating row.
	m.activate(action{Kind: "viewer-expand"})
	if !m.viewer.expanded {
		t.Fatal("expand")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.viewer != nil || m.focus != "git:unstaged:gone.go" {
		t.Fatalf("close: viewer=%v focus=%q", m.viewer != nil, m.focus)
	}
}

func TestGitBinaryDiffAndCommitViewer(t *testing.T) {
	m, api := gitModel(t, 144, 40)
	api.diff = protocol.GitDiff{Path: "img.png", Group: "staged", Binary: true, Text: "Binary files a/img.png and b/img.png differ\n"}
	gitSettle(t, m, m.activate(action{Kind: "git-open", Value: "staged", ID: "img.png"}))
	if !strings.Contains(screenText(m), "Binary file; no text diff") {
		t.Fatalf("binary:\n%s", screenText(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	c := representativeLog().Commits[0]
	c.Body = "Add PTY-backed terminal session package\n\nBody paragraph.\n"
	c.Email = "m@example.com"
	api.show = protocol.GitShow{Commit: c, Text: " term.go | 2 +-\n\ndiff --git a/term.go b/term.go\n@@ -1 +1 @@\n-a\n+b\n"}
	gitSettle(t, m, m.activate(action{Kind: "git-commit", ID: c.Hash, Value: c.Short}))
	if api.count("show:thread-shell:"+c.Hash) != 1 {
		t.Fatalf("show request: %v", api.calls)
	}
	screen := screenText(m)
	for _, want := range []string{"c8dc889 Add PTY-backed", "Matthew <m@example.com>", c.Hash, "HEAD, feature/init", "Body paragraph.", "+b"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	if m.viewerMarkdown() {
		t.Fatal("git content offered Markdown preview")
	}
}

func TestGitKeyboardFocusAndHover(t *testing.T) {
	m, _ := gitModel(t, 144, 60)
	f := m.measure()
	if h, ok := findHit(f, "git-refresh"); !ok || h.Label != "Refresh Git status · read-only" {
		t.Fatalf("refresh control missing: %+v", h)
	}
	if _, ok := findHit(f, "git:conflicted:conflict.go"); !ok {
		t.Fatal("entry row not activatable")
	}
	m.setFocus("git:conflicted:conflict.go")
	// The STAGED heading (whole staged diff) is a focus stop between rows.
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "git:section:staged" {
		t.Fatalf("down moved focus to %q", m.focus)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "git:staged:added.go" {
		t.Fatalf("down moved focus to %q", m.focus)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != "git:conflicted:conflict.go" {
		t.Fatalf("up moved focus to %q", m.focus)
	}
	// The focus mark takes the blank cell before the focused row.
	h, _ := findHit(m.measure(), m.focus)
	row := ansi.Strip(m.render().rows[h.Rect.Y])
	if cell := ansi.Cut(row, h.Rect.X-1, h.Rect.X); cell != m.icon("focus") {
		t.Fatalf("focus mark cell = %q in %q", cell, row)
	}
	// Hover changes the row's fill without moving text.
	plain := m.render().rows[h.Rect.Y]
	m.hover = "git:staged:added.go"
	h2, _ := findHit(m.measure(), m.hover)
	if m.render().rows[h2.Rect.Y] == plain && h2.Rect.Y == h.Rect.Y {
		t.Fatal("hover had no effect")
	}
	m.hover = ""
	// Enter opens the focused row.
	gitSettle(t, m, m.handleKeyForTest(tea.KeyPressMsg{Code: tea.KeyEnter}))
	if m.viewer == nil || m.viewer.git == nil || m.viewer.git.path != "conflict.go" {
		t.Fatal("enter did not open the focused row")
	}
}

func (m *Model) handleKeyForTest(k tea.KeyPressMsg) tea.Cmd {
	_, cmd := m.Update(k)
	return cmd
}

func TestGitSurfaceCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// TUI_GO_GIT_JSON optionally supplies real server reads ({"status",
	// "log", "diff", "show"}) captured from an actual checkout.
	var real struct {
		Status *protocol.GitStatus `json:"status"`
		Log    *protocol.GitLog    `json:"log"`
		Diff   *protocol.GitDiff   `json:"diff"`
		Show   *protocol.GitShow   `json:"show"`
	}
	if path := os.Getenv("TUI_GO_GIT_JSON"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &real); err != nil {
			t.Fatal(err)
		}
		gitTestNow = time.Now()
	}
	for _, light := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			w, h int
		}{{"wide", 144, 40}, {"narrow", 44, 40}, {"diff", 144, 40}, {"commit", 144, 40}, {"compact-diff", 44, 30}, {"commits", 144, 40}, {"graph", 144, 40}, {"graph-plain", 144, 40}, {"graph-narrow", 44, 40}, {"compare", 144, 40}} {
			m, api := gitModel(t, tc.w, tc.h)
			if real.Status != nil {
				api.status, api.log = *real.Status, *real.Log
				gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
			}
			m.state.Light = light
			if tc.name == "narrow" || tc.name == "compact-diff" {
				m.selectColumn(shell.RightRegion)
			}
			if rows := gitRows(m, 40); len(rows) > 0 {
				for _, r := range rows {
					if r.git != nil && r.git.hash == "" {
						m.setFocus(r.git.key)
						break
					}
				}
			}
			switch tc.name {
			case "commits":
				m.viewState().DetailScroll = 1 << 20
			case "graph", "graph-plain", "graph-narrow":
				if real.Log == nil {
					api.log = protocol.GitLog{Workspace: protocol.WorkspaceInfo{State: "branch"}, Commits: graphCommits("m:d,c* d:o c:b o:a,b,x b:q x:q a:r q:r y:z r:z z")}
					for i := range api.log.Commits {
						api.log.Commits[i].Time = gitTestNow.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339)
						api.log.Commits[i].Subject = "Commit " + api.log.Commits[i].Hash
					}
					api.log.Commits[0].Refs = []string{"HEAD", "refs/heads/main"}
				}
				api.branch = protocol.GitBranches{Branches: []protocol.GitBranch{{Name: "main", Ref: "refs/heads/main", Head: true, Upstream: "origin/main", Ahead: 1}, {Name: "side", Ref: "refs/heads/side"}, {Name: "origin/main", Ref: "refs/remotes/origin/main", Remote: true}}}
				gitSettle(t, m, m.activate(action{Kind: "git-refresh"}))
				gitSettle(t, m, m.activate(action{Kind: "git-branches"}))
				m.plainIcons = tc.name == "graph-plain"
				if tc.name == "graph-narrow" {
					m.selectColumn(shell.RightRegion)
				}
				m.viewState().DetailScroll = 1 << 20
				m.markDirty()
			case "compare":
				api.cmp = protocol.GitCompare{Base: "refs/remotes/origin/main", Head: "HEAD", BaseOid: strings.Repeat("a", 40), HeadOid: strings.Repeat("b", 40), MergeBase: strings.Repeat("c", 40), Ahead: 1, Behind: 1,
					AheadCommits: []protocol.GitCommit{{Short: "bbbbbbb", Subject: "Local work"}}, BehindCommits: []protocol.GitCommit{{Short: "aaaaaaa", Subject: "Upstream work"}},
					Text: "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n", Bytes: 60, FetchedAt: gitTestNow.Format(time.RFC3339)}
				gitSettle(t, m, m.activate(action{Kind: "git-compare", ID: "refs/remotes/origin/main", Value: "origin/main"}))
			case "diff", "compact-diff":
				api.diff = protocol.GitDiff{Path: "gone.go", Group: "unstaged", Bytes: 90, Text: "diff --git a/gone.go b/gone.go\nindex 1..2 100644\n--- a/gone.go\n+++ b/gone.go\n@@ -1,3 +1,3 @@\n context\n-removed\n+added\n"}
				if real.Diff != nil {
					api.diff = *real.Diff
				}
				gitSettle(t, m, m.activate(action{Kind: "git-open", Value: api.diff.Group, ID: api.diff.Path}))
			case "commit":
				c := representativeLog().Commits[0]
				api.show = protocol.GitShow{Commit: c, Text: " term.go | 2 +-\n\ndiff --git a/term.go b/term.go\n@@ -1 +1 @@\n-a\n+b\n"}
				if real.Show != nil {
					api.show = *real.Show
					c = real.Show.Commit
				}
				gitSettle(t, m, m.activate(action{Kind: "git-commit", ID: c.Hash, Value: c.Short}))
			}
			name := filepath.Join(dir, fmt.Sprintf("%dx%d-git-%s-%s.ansi", tc.w, tc.h, tc.name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(name, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
