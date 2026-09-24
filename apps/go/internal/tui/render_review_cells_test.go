package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func wideModel(t *testing.T, width, height int, repeat ...int) *Model {
	t.Helper()
	m := testModel()
	wide := strings.Repeat("漢字テスト👩🏽‍💻 mixed 幅 ", append(repeat, 14)[0])
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID == m.state.Active {
			m.snapshot.Threads[i].Activity = []protocol.Activity{
				{ID: "u1", Role: "user", Text: wide},
				{ID: "a1", Role: "agent", Text: wide},
				{ID: "t1", Role: "tool", Title: "検索ツール", State: "completed", Text: wide},
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func assertRowWidths(t *testing.T, m *Model, context string) {
	t.Helper()
	rows := strings.Split(m.View().Content, "\n")
	if len(rows) != m.height {
		t.Fatalf("%s %dx%d: %d rows", context, m.width, m.height, len(rows))
	}
	for y, row := range rows {
		if w := ansi.StringWidth(row); w != m.width {
			t.Fatalf("%s %dx%d: row %d is %d cells: %q", context, m.width, m.height, y, w, ansi.Strip(row))
		}
	}
}

func TestReviewOverlaysKeepRowsCellAccurateOverWideText(t *testing.T) {
	for width := 60; width <= 140; width++ {
		// 27 rows leave the transcript more than the user box's padding row
		// beside the fixture's queue and interior-header question card.
		m := wideModel(t, width, 27)
		assertRowWidths(t, m, "transcript")

		m.openCommands()
		assertRowWidths(t, m, "menu")
		m.menu = nil

		m.setFocus("prompt")
		m.prompt.SetValue("see @")
		m.configureInputs()
		if m.mentionRect(m.measure()).H < 4 {
			t.Fatalf("%d: mention popup not shown", width)
		}
		assertRowWidths(t, m, "mentions")
		m.prompt.SetValue("")
		m.configureInputs()

		// Select from the transcript's first rows, not the end it opens at.
		m.viewState().Pinned, m.viewState().Scroll = false, 0
		f := m.measure()
		for dx := 0; dx < 2 && width%4 == 0; dx++ {
			m.Update(tea.MouseClickMsg{X: f.transcript.X + dx, Y: f.transcript.Y, Button: tea.MouseLeft})
			m.Update(tea.MouseMotionMsg{X: f.transcript.X + f.transcript.W/2 + dx, Y: f.transcript.Y + 3, Button: tea.MouseLeft})
			assertRowWidths(t, m, "selecting")
			m.Update(tea.MouseReleaseMsg{X: f.transcript.X + f.transcript.W/2 + dx, Y: f.transcript.Y + 3, Button: tea.MouseLeft})
			if m.selectedText == "" {
				t.Fatalf("%d: nothing selected", width)
			}
			assertRowWidths(t, m, "selected")
		}
	}
}

func TestReviewPutClipsAndPadsCutClusters(t *testing.T) {
	f := frame{rows: []string{style("#ffffff", "#000000").Render("漢字漢字漢字")}} // 12 cells
	f.put(shell.Rect{X: 3, Y: 0, W: 4, H: 1}, "abcd")
	// Each cut cluster becomes one pad cell inside its own style, before the reset.
	if got := ansi.Strip(f.rows[0]); got != "漢 abcd 漢字" || !strings.Contains(f.rows[0], "漢 \x1b[m") {
		t.Fatalf("cut clusters not padded in their own style: %q", f.rows[0])
	}
	f.put(shell.Rect{X: -2, Y: 0, W: 4, H: 1}, "wxyz")
	f.put(shell.Rect{X: 10, Y: 0, W: 6, H: 1}, "123456")
	f.put(shell.Rect{X: 40, Y: 0, W: 3, H: 1}, "no")
	if got := ansi.Strip(f.rows[0]); got != "yz abcd 漢12" || ansi.StringWidth(f.rows[0]) != 12 {
		t.Fatalf("clipping changed row: %q", got)
	}
}

func selectTranscript(m *Model, x0, y0, x1, y1 int) {
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
}

func TestReviewSelectionCopiesOnlyItsRegion(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	m.openSurface("plan", "")
	m.configureInputs()
	f := m.render()
	if f.geom.Left.W == 0 || f.geom.Right.W == 0 {
		t.Fatal("expected nav and right pane")
	}
	r := f.transcript
	selectTranscript(m, r.X+2, r.Y, r.X+5, r.Y+3)
	got := strings.Split(m.selectedText, "\n")
	if len(got) != 4 {
		t.Fatalf("rows: %q", got)
	}
	for i, line := range got {
		start, end := r.X, r.X+r.W
		if i == 0 {
			start = r.X + 2
		}
		if i == len(got)-1 {
			end = r.X + 6
		}
		want := strings.TrimRight(ansi.Strip(ansi.Cut(f.rows[r.Y+i], start, end)), " ")
		if line != want {
			t.Fatalf("row %d copied %q, want %q", i, line, want)
		}
	}
	if nav := strings.TrimSpace(ansi.Strip(ansi.Cut(f.rows[r.Y+1], 0, f.geom.Left.W))); nav != "" && strings.Contains(m.selectedText, nav) {
		t.Fatalf("copied navigation text %q", nav)
	}
	if strings.Contains(m.selectedText, "│") || strings.Contains(m.selectedText, "CURRENT PLAN") {
		t.Fatalf("copied outside the transcript: %q", m.selectedText)
	}
}

func copiesSelection(m *Model) bool {
	m.setFocus("transcript")
	cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	return cmd != nil && m.selectedText != ""
}

func TestReviewSelectionInvalidatedWhenBasisChanges(t *testing.T) {
	changes := map[string]func(m *Model){
		"resize": func(m *Model) { m.Update(tea.WindowSizeMsg{Width: 80, Height: 30}) },
		"scroll": func(m *Model) { f := m.measure(); m.scrollTo("transcript", f.transcriptMax-1, f) },
		"pane":   func(m *Model) { m.activate(action{Kind: "left"}) },
		"thread": func(m *Model) {
			for _, t := range m.snapshot.Threads {
				if t.ID != m.state.Active {
					m.selectThread(t.ID)
					return
				}
			}
		},
	}
	for name, change := range changes {
		m := wideModel(t, 154, 40, 60)
		f := m.measure()
		if f.transcriptMax < 2 {
			t.Fatal("fixture transcript does not scroll")
		}
		r := f.transcript
		selectTranscript(m, r.X+1, r.Y+1, r.X+r.W-1, r.Y+4)
		if !m.selectionLive(m.measure()) || m.selectedText == "" {
			t.Fatalf("%s: selection missing", name)
		}
		if !copiesSelection(m) {
			t.Fatalf("%s: live selection not copied", name)
		}
		selected := m.View().Content
		change(m)
		m.configureInputs()
		if m.selectionLive(m.measure()) {
			t.Fatalf("%s: stale selection still live", name)
		}
		assertRowWidths(t, m, name)
		if copiesSelection(m) {
			t.Fatalf("%s: stale selection copied", name)
		}
		// No highlight survives: the view equals one rendered without selection.
		stale := m.View().Content
		m.selectedText, m.selecting = "", false
		if stale != m.View().Content || stale == selected {
			t.Fatalf("%s: stale highlight painted", name)
		}
	}
}

func TestReviewSelectionHighlightIsClampedToFrame(t *testing.T) {
	m := wideModel(t, 80, 30)
	f := m.measure()
	m.selectedText, m.selectionRegion = "x", f.transcript
	m.selectionBasis = m.selectionBasisFor(f)
	m.selectionStart, m.selectionEnd = [2]int{-5, -3}, [2]int{400, 90}
	assertRowWidths(t, m, "clamped")
	spans := selectionSpans(m.selectionStart, m.selectionEnd, f.transcript, m.width, m.height)
	if len(spans) != f.transcript.H {
		t.Fatalf("spans %d for %d rows", len(spans), f.transcript.H)
	}
	for _, s := range spans {
		if s.start < f.transcript.X || s.end > f.transcript.X+f.transcript.W || !f.transcript.Contains(s.start, s.y) {
			t.Fatalf("span outside region: %#v", s)
		}
	}
}

func TestReviewSingleRowTextCollapsesNewlines(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].Title = "first line\nsecond\tline"
	}
	m.status = "status\nnext"
	for _, menu := range []bool{false, true} {
		if menu {
			m.showMenu("Pick\none", []menuItem{{Label: "item\nwith newline", Action: action{Kind: "theme"}}})
		}
		rows := m.render().rows
		if len(rows) != m.height {
			t.Fatalf("rows %d", len(rows))
		}
		for y, row := range rows {
			if strings.Contains(row, "\n") || ansi.StringWidth(row) != m.width {
				t.Fatalf("row %d broken: %q", y, ansi.Strip(row))
			}
		}
		text := ansi.Strip(strings.Join(rows, "\n"))
		if !menu && !strings.Contains(text, "first line second") {
			t.Fatal("title newline not collapsed to a space")
		}
		if !menu && !strings.Contains(text, "status next") {
			t.Fatal("status newline not collapsed")
		}
		if menu && (!strings.Contains(text, "item with newline") || !strings.Contains(text, "PICK ONE")) {
			t.Fatal("menu newline not collapsed")
		}
	}
	f := frame{rows: []string{strings.Repeat(" ", 20)}}
	f.text(0, 0, 20, "a\nb", "#ffffff", "#000000")
	f.compactControl(m, 0, 0, 0, "x", componentVisual{})
	if got := ansi.Strip(f.rows[0]); got != fit("a b", 20) {
		t.Fatalf("text kept newline: %q", got)
	}
	// Every cell of the row carries the style; no default-background gap.
	if plain := strings.TrimRight(f.rows[0], " "); plain != f.rows[0] {
		t.Fatalf("unstyled trailing padding: %q", f.rows[0])
	}
}
