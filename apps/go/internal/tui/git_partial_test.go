package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// fakeGitPartial adds GitHunks to the write fake.
type fakeGitPartial struct {
	*fakeGitWriter
	hmu   sync.Mutex
	hunks map[string]protocol.GitHunks // by group
	reads int
}

func (g *fakeGitPartial) GitHunks(_ context.Context, _ client.GitTarget, path, group string) (protocol.GitHunks, error) {
	g.hmu.Lock()
	defer g.hmu.Unlock()
	g.reads++
	h := g.hunks[group]
	h.Path, h.Group = path, group
	return h, nil
}

func (g *fakeGitPartial) hunkReads() int {
	g.hmu.Lock()
	defer g.hmu.Unlock()
	return g.reads
}

// partialHunks has rows: 0 H0, 1 ctx, 2 -old, 3 +new, 4 ctx, 5 H1, 6 ctx,
// 7 +b1, 8 +hostile, 9 ctx (no newline), 10 eof marker.
func partialHunks() protocol.GitHunks {
	return protocol.GitHunks{Fingerprint: "fp-1", LineCount: 8, Mode: "100644", Hunks: []protocol.GitHunk{
		{Index: 0, Header: "@@ -1,3 +1,3 @@ func main()", OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3, Lines: []protocol.GitHunkLine{
			{Index: 0, Kind: "context", Text: "package main", OldLine: 1, NewLine: 1},
			{Index: 1, Kind: "delete", Text: "old", OldLine: 2},
			{Index: 2, Kind: "add", Text: "new", NewLine: 2},
			{Index: 3, Kind: "context", Text: "x", OldLine: 3, NewLine: 3},
		}},
		{Index: 1, Header: "@@ -10,2 +10,4 @@", OldStart: 10, OldLines: 2, NewStart: 10, NewLines: 4, Lines: []protocol.GitHunkLine{
			{Index: 4, Kind: "context", Text: "a", OldLine: 10, NewLine: 10},
			{Index: 5, Kind: "add", Text: "b1", NewLine: 11},
			{Index: 6, Kind: "add", Text: "\x1b[31mred\x1b]0;pwn\x07\u202e\r", NewLine: 12},
			{Index: 7, Kind: "context", Text: "d", OldLine: 11, NewLine: 13, NoNewline: true},
		}},
	}}
}

func partialStatus() protocol.GitStatus {
	s := writableStatus()
	s.Entries = append(s.Entries,
		protocol.GitStatusEntry{Path: "main.go", Index: ".", Worktree: "M", Group: protocol.GitGroupUnstaged, Pin: "p-main"},
		protocol.GitStatusEntry{Path: "lib.go", Index: "M", Worktree: ".", Group: protocol.GitGroupStaged, Pin: "p-lib"},
	)
	return s
}

func partialModel(t *testing.T, width, height int, group, path string) (*Model, *fakeGitPartial) {
	t.Helper()
	m, w := gitWriteModel(t, width, height)
	api := &fakeGitPartial{fakeGitWriter: w, hunks: map[string]protocol.GitHunks{
		protocol.GitGroupUnstaged: partialHunks(), protocol.GitGroupStaged: partialHunks(),
	}}
	w.fakeGit.status = partialStatus()
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-partial-stage")
	gitWriteSettle(t, m, m.refreshGit())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-open", Value: group, ID: path}))
	if !m.gitPartialActive() {
		t.Fatalf("partial not active: %+v", m.viewer.git.partial)
	}
	return m, api
}

func pkey(t *testing.T, m *Model, name string) {
	t.Helper()
	keys := map[string]tea.KeyPressMsg{
		"down": {Code: tea.KeyDown}, "up": {Code: tea.KeyUp},
		"shift+down": {Code: tea.KeyDown, Mod: tea.ModShift}, "shift+up": {Code: tea.KeyUp, Mod: tea.ModShift},
		"space": {Code: tea.KeySpace, Text: " "}, "tab": {Code: tea.KeyTab}, "esc": {Code: tea.KeyEscape},
		"enter": {Code: tea.KeyEnter}, "S": {Code: 's', Mod: tea.ModShift, Text: "S"},
	}
	k, ok := keys[name]
	if !ok {
		k = tea.KeyPressMsg{Code: []rune(name)[0], Text: name}
	}
	_, cmd := m.Update(k)
	gitWriteSettle(t, m, cmd)
}

func partialSel(m *Model) []int {
	var out []int
	for k, on := range m.viewer.git.partial.selected {
		if on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func lastPartial(t *testing.T, api *fakeGitPartial) protocol.Command {
	t.Helper()
	s := api.sent()
	if len(s) == 0 {
		t.Fatal("nothing sent")
	}
	return s[len(s)-1]
}

func TestGitPartialToggleHunkAndPayload(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	ps := m.viewer.git.partial
	if ps.cursor != 0 {
		t.Fatalf("cursor %d", ps.cursor)
	}
	pkey(t, m, "down") // skips the context row
	if ps.cursor != 2 {
		t.Fatalf("cursor %d, want 2", ps.cursor)
	}
	pkey(t, m, "space")
	pkey(t, m, "down")
	pkey(t, m, "space")
	if got := partialSel(m); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("sel %v", got)
	}
	pkey(t, m, "a") // all selected: clears the hunk
	if got := partialSel(m); len(got) != 0 {
		t.Fatalf("sel after hunk clear %v", got)
	}
	pkey(t, m, "a")
	pkey(t, m, "down") // next header
	pkey(t, m, "down") // +b1
	pkey(t, m, "space")
	if got := partialSel(m); !slices.Equal(got, []int{1, 2, 5}) {
		t.Fatalf("mixed sel %v", got)
	}
	if !strings.Contains(screenText(m), "Stage 3 lines") || !strings.Contains(screenText(m), "3 selected") {
		t.Fatalf("action row missing:\n%s", screenText(m))
	}
	pkey(t, m, "s")
	c := lastPartial(t, api)
	if c.Kind != protocol.GitKindStage || c.Git.Partial == nil || len(c.Git.Paths) != 0 {
		t.Fatalf("cmd %+v", c)
	}
	p := c.Git.Partial
	if p.Path != "main.go" || p.Group != "unstaged" || p.Fingerprint != "fp-1" || !slices.Equal(p.Lines, []int{5}) || !slices.Equal(p.Hunks, []int{0}) {
		t.Fatalf("partial %+v", p)
	}
	if !strings.Contains(m.notice.text, "Staged 1 hunk and 1 line in main.go") {
		t.Fatalf("notice %q", m.notice.text)
	}
}

func TestGitPartialRangeAndCursorHunk(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	ps := m.viewer.git.partial
	pkey(t, m, "down")
	pkey(t, m, "down") // row 3 +new
	pkey(t, m, "v")
	pkey(t, m, "down") // header 1
	pkey(t, m, "down") // +b1
	if got := partialSel(m); !slices.Equal(got, []int{2, 5}) {
		t.Fatalf("range %v", got)
	}
	pkey(t, m, "down")
	pkey(t, m, "up")
	if got := partialSel(m); !slices.Equal(got, []int{2, 5}) {
		t.Fatalf("shrunk range %v", got)
	}
	pkey(t, m, "v")
	pkey(t, m, "esc") // clears the selection, keeps the viewer
	if m.viewer == nil || len(partialSel(m)) != 0 {
		t.Fatal("esc did not clear the selection first")
	}
	pkey(t, m, "shift+down") // extends from +b1 to +hostile
	if got := partialSel(m); !slices.Equal(got, []int{5, 6}) {
		t.Fatalf("shift range %v", got)
	}
	pkey(t, m, "esc")
	pkey(t, m, "esc")
	if ps.cursor != 8 {
		t.Fatalf("cursor %d", ps.cursor)
	}
	// Nothing selected: s acts on the cursor's hunk.
	pkey(t, m, "s")
	p := lastPartial(t, api).Git.Partial
	if !slices.Equal(p.Hunks, []int{1}) || len(p.Lines) != 0 {
		t.Fatalf("hunk payload %+v", p)
	}
}

func TestGitPartialStagedGroupUnstages(t *testing.T) {
	m, api := partialModel(t, 120, 50, "staged", "lib.go")
	if !strings.Contains(screenText(m), "HEAD → index") {
		t.Fatalf("staged semantics not labelled:\n%s", screenText(m))
	}
	pkey(t, m, "s")
	if len(api.sent()) != 0 {
		t.Fatal("s sent from the staged diff")
	}
	pkey(t, m, "enter") // header 0: selects the hunk
	if got := partialSel(m); !slices.Equal(got, []int{1, 2}) || len(api.sent()) != 0 {
		t.Fatalf("enter on header %v", got)
	}
	pkey(t, m, "u")
	c := lastPartial(t, api)
	if c.Kind != protocol.GitKindUnstage || c.Git.Partial.Group != "staged" || !slices.Equal(c.Git.Partial.Hunks, []int{0}) || len(c.Git.Partial.Lines) != 0 {
		t.Fatalf("cmd %+v %+v", c, c.Git.Partial)
	}
	for _, s := range []string{"Select hunk"} {
		if !strings.Contains(screenText(m), s) {
			t.Fatalf("missing %q", s)
		}
	}
}

func TestGitPartialUnsupportedKeepsWholeFile(t *testing.T) {
	m, w := gitWriteModel(t, 120, 50)
	h := partialHunks()
	h.Fingerprint, h.Unsupported, h.Message = "", "filter", "filter attribute"
	api := &fakeGitPartial{fakeGitWriter: w, hunks: map[string]protocol.GitHunks{"unstaged": h}}
	w.fakeGit.status = partialStatus()
	w.fakeGit.diff = protocol.GitDiff{Text: "diff --git a/main.go b/main.go\n@@ -1 +1 @@\n-a\n+b\n", Bytes: 30}
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-partial-stage")
	gitWriteSettle(t, m, m.refreshGit())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "main.go"}))
	if m.gitPartialActive() {
		t.Fatal("unsupported path is selectable")
	}
	txt := screenText(m)
	if strings.Count(txt, "Whole file only · filter attribute") != 1 || !strings.Contains(txt, "+b") {
		t.Fatalf("unsupported rendering:\n%s", txt)
	}
	gitKey(t, m, "s")
	c := lastPartial(t, api)
	if c.Git.Partial != nil || len(c.Git.Paths) != 1 || c.Git.Paths[0].Pin != "p-main" {
		t.Fatalf("whole-file stage %+v", c.Git)
	}
}

func TestGitPartialStaleDiffRefetchesAndKeepsCursor(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	ps := m.viewer.git.partial
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "stale_diff", Message: "stale"}
	}
	for range 7 {
		pkey(t, m, "down")
	}
	if ps.cursor != 8 {
		t.Fatalf("cursor %d", ps.cursor)
	}
	pkey(t, m, "space")
	// The refetched diff has an extra hunk first; the hostile line moves.
	h := partialHunks()
	extra := protocol.GitHunk{Index: 0, Header: "@@ -0,0 +1 @@", Lines: []protocol.GitHunkLine{{Index: 0, Kind: "add", Text: "first"}}}
	for i := range h.Hunks {
		h.Hunks[i].Index++
		for j := range h.Hunks[i].Lines {
			h.Hunks[i].Lines[j].Index++
		}
	}
	h.Hunks = append([]protocol.GitHunk{extra}, h.Hunks...)
	h.Fingerprint = "fp-2"
	api.hmu.Lock()
	api.hunks["unstaged"] = h
	api.hmu.Unlock()
	before := api.hunkReads()
	pkey(t, m, "s")
	if api.hunkReads() <= before {
		t.Fatal("stale_diff did not refetch hunks")
	}
	if m.notice.text != gitErrorCopy("stale_diff") {
		t.Fatalf("notice %q", m.notice.text)
	}
	if len(partialSel(m)) != 0 || ps.hunks.Fingerprint != "fp-2" {
		t.Fatalf("selection kept %v", partialSel(m))
	}
	if ln := ps.line(ps.rows[ps.cursor]); ln == nil || ln.Index != 7 {
		t.Fatalf("cursor not preserved: row %d %+v", ps.cursor, ln)
	}
	// A retry with the new fingerprint sends it.
	api.reply = nil
	pkey(t, m, "space")
	pkey(t, m, "s")
	if p := lastPartial(t, api).Git.Partial; p.Fingerprint != "fp-2" || !slices.Equal(p.Lines, []int{7}) {
		t.Fatalf("retry %+v", p)
	}
}

func TestGitPartialMouse(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	f := m.measure()
	b := f.viewerBody
	click := func(row int) {
		_, cmd := m.Update(tea.MouseClickMsg{X: b.X + 4, Y: b.Y + row, Button: tea.MouseLeft})
		gitWriteSettle(t, m, cmd)
	}
	click(2)
	if got := partialSel(m); !slices.Equal(got, []int{1}) {
		t.Fatalf("click sel %v", got)
	}
	click(2)
	if len(partialSel(m)) != 0 {
		t.Fatal("second click did not clear")
	}
	// Drag from -old to +b1 selects every change between.
	m.Update(tea.MouseClickMsg{X: b.X + 4, Y: b.Y + 2, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: b.X + 4, Y: b.Y + 7, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: b.X + 4, Y: b.Y + 7, Button: tea.MouseLeft})
	if got := partialSel(m); !slices.Equal(got, []int{1, 2, 5}) {
		t.Fatalf("drag sel %v", got)
	}
	if len(api.sent()) != 0 {
		t.Fatal("selection sent a write")
	}
	// The apply control is a hit and reachable by Tab.
	if _, ok := findHit(m.measure(), "viewer-partial-apply"); !ok {
		t.Fatal("no apply control")
	}
	for i := 0; i < 5 && m.focus != "viewer-partial-apply"; i++ {
		pkey(t, m, "tab")
	}
	if m.focus != "viewer-partial-apply" {
		t.Fatalf("tab focus %q", m.focus)
	}
	pkey(t, m, "enter")
	if p := lastPartial(t, api).Git.Partial; !slices.Equal(p.Lines, []int{5}) || !slices.Equal(p.Hunks, []int{0}) {
		t.Fatalf("apply %+v", p)
	}
	// Clicking a hunk header acts on that hunk.
	f = m.measure()
	b = f.viewerBody
	row := -1
	for i, r := range m.viewer.git.partial.rows {
		if r.line < 0 && r.hunk == 1 {
			row = i - m.viewer.scroll
		}
	}
	sent := len(api.sent())
	_, cmd := m.Update(tea.MouseClickMsg{X: b.X + 2, Y: b.Y + row, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	if got := partialSel(m); !slices.Equal(got, []int{5, 6}) || len(api.sent()) != sent {
		t.Fatalf("header click sel %v sent %d", got, len(api.sent()))
	}
	_, cmd = m.Update(tea.MouseClickMsg{X: b.X + 2, Y: b.Y + row, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	if got := partialSel(m); len(got) != 0 {
		t.Fatalf("second header click %v", got)
	}
}

func TestGitPartialSanitizesHostileText(t *testing.T) {
	m, _ := partialModel(t, 120, 50, "unstaged", "main.go")
	raw := strings.Join(m.render().rows, "\n")
	for _, bad := range []string{"\x1b[31mred", "\x1b]0;pwn", "\x07", "\u202e", "\r"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("render contains %q", bad)
		}
	}
	txt := screenText(m)
	if !strings.Contains(txt, "+␛red␍") || !strings.Contains(txt, `\ No newline at end of file`) {
		t.Fatalf("sanitized line or no-newline marker missing:\n%s", txt)
	}
}

func TestGitPartialNarrowAndStatusRepin(t *testing.T) {
	m, api := partialModel(t, 40, 24, "unstaged", "main.go")
	txt := screenText(m)
	if !strings.Contains(txt, "Stage hunk") {
		t.Fatalf("narrow action missing:\n%s", txt)
	}
	for _, row := range m.render().rows {
		if w := ansi.StringWidth(row); w > 40 {
			t.Fatalf("row wider than screen: %d", w)
		}
	}
	// A new pin for the same entry re-pins and refetches instead of
	// disabling the viewer.
	s := partialStatus()
	s.Entries[len(s.Entries)-2].Pin = "p-main-2"
	api.fakeGit.status = s
	before := api.hunkReads()
	gitWriteSettle(t, m, m.refreshGit())
	if m.viewer.git.changed || m.viewer.git.entry.Pin != "p-main-2" || api.hunkReads() <= before {
		t.Fatalf("not re-pinned: %+v", m.viewer.git.entry)
	}
}

func TestGitPartialIndexChangedWarning(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateSucceeded, Code: "index_changed"}}, nil
	}
	pkey(t, m, "s")
	key, _ := m.gitTarget()
	st := m.gitWriteFor(key)
	if st == nil || !strings.Contains(st.warning, "review the staged diff") || strings.Contains(st.warning, "failed") {
		t.Fatalf("warning %+v", st)
	}
}

func TestGitPartialCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"unstaged", "staged", "narrow"} {
			w, h, group, path := 120, 40, "unstaged", "main.go"
			if name == "staged" {
				group, path = "staged", "lib.go"
			}
			if name == "narrow" {
				w, h = 50, 30
			}
			m, _ := partialModel(t, w, h, group, path)
			m.state.Light = light
			pkey(t, m, "down")
			pkey(t, m, "space")
			pkey(t, m, "down")
			pkey(t, m, "down")
			if name == "narrow" {
				m.selectColumn(shell.RightRegion)
			}
			file := filepath.Join(dir, fmt.Sprintf("%dx%d-gitpartial-%s-%s.ansi", w, h, name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// settleOnly applies only the listed message kinds (by predicate).
func settleOnly(t *testing.T, m *Model, cmd tea.Cmd, keep func(tea.Msg) bool) (held []tea.Msg) {
	t.Helper()
	for range 6 {
		var next []tea.Cmd
		for _, msg := range pump(t, m, cmd).msgs {
			if keep(msg) {
				_, c := m.Update(msg)
				next = append(next, c)
			} else {
				held = append(held, msg)
			}
		}
		if len(next) == 0 {
			return held
		}
		cmd = tea.Batch(next...)
	}
	return held
}

func noHunks(msg tea.Msg) bool {
	switch msg.(type) {
	case gitStatusMsg, gitLogMsg, gitViewerMsg, gitWriteMsg, gitHeadMsg:
		return true
	}
	return false
}

func onlyStatus(msg tea.Msg) bool {
	switch msg.(type) {
	case gitStatusMsg, gitLogMsg:
		return true
	}
	return false
}

func unsupportedModel(t *testing.T, code string) (*Model, *fakeGitPartial) {
	t.Helper()
	m, w := gitWriteModel(t, 120, 50)
	h := partialHunks()
	h.Fingerprint, h.Unsupported = "", code
	api := &fakeGitPartial{fakeGitWriter: w, hunks: map[string]protocol.GitHunks{"unstaged": h}}
	w.fakeGit.status = partialStatus()
	w.fakeGit.diff = protocol.GitDiff{Text: "diff --git a/main.go b/main.go\n@@ -1 +1 @@\n-a\n+b\n", Bytes: 30}
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-partial-stage")
	gitWriteSettle(t, m, m.refreshGit())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "main.go"}))
	return m, api
}

func repinStatus(api *fakeGitPartial) {
	s := partialStatus()
	s.Entries[len(s.Entries)-2].Pin = "p-main-2"
	api.fakeGit.status = s
}

// s/u never switch scope while the first hunks load.
func TestGitPartialKeysWhileLoading(t *testing.T) {
	m, w := gitWriteModel(t, 120, 50)
	api := &fakeGitPartial{fakeGitWriter: w, hunks: map[string]protocol.GitHunks{"unstaged": partialHunks()}}
	w.fakeGit.status = partialStatus()
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-partial-stage")
	gitWriteSettle(t, m, m.refreshGit())
	settleOnly(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "main.go"}), noHunks)
	if m.gitPartialActive() {
		t.Fatal("active before hunks")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	settleOnly(t, m, cmd, noHunks)
	if len(api.sent()) != 0 || m.notice.text != "Loading diff…" {
		t.Fatalf("s while loading: sent %d notice %q", len(api.sent()), m.notice.text)
	}
	// S is whole-file in every state.
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModShift, Text: "S"})
	settleOnly(t, m, cmd, noHunks)
	if c := lastPartial(t, api); c.Git.Partial != nil || c.Git.Paths[0].Pin != "p-main" {
		t.Fatalf("S %+v", c.Git)
	}
}

// Whole-file actions act only on the pin whose content is displayed: a new
// pin is adopted after both the raw patch and the hunks were reread.
func TestGitPartialRepinWaitsForDisplayedContent(t *testing.T) {
	for _, tc := range []struct {
		name, key   string
		unsupported string
	}{{"S-active", "S", ""}, {"s-unsupported", "s", "binary"}, {"d-unsupported", "d", "too_large"}, {"d-active", "d", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var m *Model
			var api *fakeGitPartial
			if tc.unsupported == "" {
				m, api = partialModel(t, 120, 50, "unstaged", "main.go")
			} else {
				m, api = unsupportedModel(t, tc.unsupported)
			}
			diffs := api.fakeGit.count("diff:")
			repinStatus(api)
			held := settleOnly(t, m, m.refreshGit(), onlyStatus)
			if !m.viewer.git.changed || m.viewer.git.entry.Pin != "p-main" {
				t.Fatalf("re-pinned before display: %+v", m.viewer.git.entry)
			}
			k := tea.KeyPressMsg{Code: rune(strings.ToLower(tc.key)[0]), Text: tc.key}
			if tc.key == "S" {
				k.Mod = tea.ModShift
			}
			m.Update(k)
			if len(api.sent()) != 0 || m.gitW.discard != nil {
				t.Fatalf("acted on undisplayed pin: %+v %+v", api.sent(), m.gitW.discard)
			}
			// Deliver the held rereads: only now is the new content shown.
			for _, msg := range held {
				_, c := m.Update(msg)
				gitWriteSettle(t, m, c)
			}
			if api.fakeGit.count("diff:") <= diffs || m.viewer.git.changed || m.viewer.git.entry.Pin != "p-main-2" {
				t.Fatalf("not re-pinned after reload: changed=%v pin=%s", m.viewer.git.changed, m.viewer.git.entry.Pin)
			}
			_, cmd := m.Update(k)
			gitWriteSettle(t, m, cmd)
			switch tc.key {
			case "d":
				if m.gitW.discard == nil || m.gitW.discard.entry.Pin != "p-main-2" {
					t.Fatalf("discard %+v", m.gitW.discard)
				}
			default:
				if c := lastPartial(t, api); c.Git.Partial != nil || c.Git.Paths[0].Pin != "p-main-2" {
					t.Fatalf("whole-file %+v", c.Git)
				}
			}
		})
	}
}

type failingHunks struct {
	*fakeGitPartial
	fail bool
}

func (e *failingHunks) GitHunks(ctx context.Context, tg client.GitTarget, path, group string) (protocol.GitHunks, error) {
	if e.fail {
		return protocol.GitHunks{}, fmt.Errorf("changed while it was read; try again")
	}
	return e.fakeGitPartial.GitHunks(ctx, tg, path, group)
}

func TestGitPartialRefetchErrorDisablesApply(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	e := &failingHunks{fakeGitPartial: api}
	m.gitReads = e
	pkey(t, m, "down")
	pkey(t, m, "space")
	e.fail = true
	pkey(t, m, "s")
	n := len(api.sent())
	if m.gitPartialActive() || len(partialSel(m)) != 0 || !strings.Contains(screenText(m), "Hunks unavailable") {
		t.Fatalf("stale hunks live after error:\n%s", screenText(m))
	}
	pkey(t, m, "s")
	if len(api.sent()) != n {
		t.Fatal("s sent after refetch error")
	}
}

func TestGitPartialEscFromApplyControl(t *testing.T) {
	m, _ := partialModel(t, 120, 50, "unstaged", "main.go")
	pkey(t, m, "down")
	pkey(t, m, "space")
	for i := 0; i < 5 && m.focus != "viewer-partial-apply"; i++ {
		pkey(t, m, "tab")
	}
	pkey(t, m, "esc")
	if m.viewer == nil || len(partialSel(m)) != 0 {
		t.Fatal("esc on the apply control did not clear the selection first")
	}
	pkey(t, m, "esc")
	if m.viewer != nil {
		t.Fatal("second esc did not close")
	}
}

func TestGitPartialTooLargeCopy(t *testing.T) {
	if got := gitErrorCopy("too_large"); !strings.Contains(got, "select whole hunks") {
		t.Fatal(got)
	}
}

func TestGitPartialHostileWidths(t *testing.T) {
	for _, w := range []int{40, 45, 57, 80} {
		m, api := partialModel(t, w, 24, "unstaged", "main.go")
		h := partialHunks()
		h.Hunks[0].Lines[1].Text = strings.Repeat("漢字", 20)
		h.Hunks[0].Lines[2].Text = "👩‍👩‍👧‍👦é\ufffd\x9b31m\u0085tab\there" + strings.Repeat("y", 500)
		h.Hunks[1].Lines[1].Text = "vis\x1b]52;c;SGVsbG8=\x07ible\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\ hidden\x1b]0;tail"
		h.Hunks[1].Header = "@@ -1 +1 @@ \x1b[2J\u202e" + strings.Repeat("漢字", 10)
		api.hmu.Lock()
		api.hunks["unstaged"] = h
		api.hmu.Unlock()
		gitWriteSettle(t, m, m.refetchGitHunks())
		for i, row := range m.render().rows {
			if sw := ansi.StringWidth(row); sw != w {
				t.Errorf("w=%d row %d width %d", w, i, sw)
			}
			for _, bad := range []string{"\x1b]52", "\x1b]8;;http", "\x1b[2J", "\u202e", "\u0085", "\u009b"} {
				if strings.Contains(row, bad) {
					t.Errorf("w=%d row %d contains %q", w, i, bad)
				}
			}
		}
		if txt := screenText(m); !strings.Contains(txt, "+␛") || !strings.Contains(txt, "…") {
			t.Errorf("w=%d missing hidden or truncation marker:\n%s", w, txt)
		}
	}
}
