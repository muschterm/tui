package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func p1Scene(name string, light bool) *Model {
	var m *Model
	switch name {
	case "mention":
		m = mentionModel("Inspect @")
	default:
		m = testModel()
	}
	m.state.Light = light
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	switch name {
	case "commands":
		m.openCommands()
	case "commands-open":
		m.openCommands()
		for i, item := range m.menu {
			if item.Action.Kind == "open" {
				m.menuIndex = i
				break
			}
		}
	case "delete-thread":
		m.confirmThreadDelete(m.snapshot.Threads[0].ID)
	case "overflow-locked":
		m.snapshot.Threads[0].State = "running"
		m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
		m.openComposerOverflow()
	case "mention":
		pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Entries: []protocol.PathEntry{{Name: "Notes: Draft.md", Path: "Notes: Draft.md"}, {Name: "cmd", Path: "cmd", IsDir: true}, {Name: "README.md", Path: "README.md"}}})
	}
	m.configureInputs()
	return m
}

// TestPanelPass3P1Captures writes review captures for the final P1 fixes.
func TestPanelPass3P1Captures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, scene := range []string{"commands", "commands-open", "delete-thread", "overflow-locked", "mention"} {
			m := p1Scene(scene, light)
			name := fmt.Sprintf("%dx%d-light%t-p1-%s.ansi", m.width, m.height, light, scene)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The position counter counts selectable rows, never separators.
func TestMenuPositionSkipsSeparators(t *testing.T) {
	items := []menuItem{{Label: "a"}, {Separator: true}, {Label: "b"}, {Separator: true}, {Label: "c"}}
	if pos, total := menuPosition(items, 2); pos != 2 || total != 3 {
		t.Fatalf("got %d/%d, want 2/3", pos, total)
	}
	m := p1Scene("commands", false)
	sep := 0
	for _, item := range m.menu {
		if item.Separator {
			sep++
		}
	}
	if sep == 0 {
		t.Fatal("commands menu has no group separators")
	}
	want := fmt.Sprintf("1/%d", len(m.menu)-sep)
	if !strings.Contains(ansi.Strip(strings.Join(m.render().rows, "\n")), want) {
		t.Fatalf("counter %q missing", want)
	}
}

// Plain and icon rows of one menu start their labels in one column.
func TestCommandsMenuLabelsAlign(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		m := p1Scene("commands-open", false)
		if ascii {
			m.state.Icons = "ascii"
		}
		f := m.render()
		col := func(label string) int {
			for _, row := range f.rows {
				plain := ansi.Strip(row)
				if i := strings.Index(plain, label); i >= 0 {
					return ansi.StringWidth(plain[:i])
				}
			}
			t.Fatalf("%q not painted", label)
			return -1
		}
		m.menuIndex = 0
		f = m.render()
		plain := col("Navigation · show")
		m.menuIndex = len(m.menu) - 1
		for i, item := range m.menu {
			if item.Action.Kind == "open" {
				m.menuIndex = i
			}
		}
		f = m.render()
		if icon := col("Open Activity"); icon != plain {
			t.Fatalf("ascii %v: icon label at %d, plain at %d", ascii, icon, plain)
		}
	}
}

// Confirmation titles use the PREFIX · user form and keep the user's case.
func TestConfirmTitlesUseDotForm(t *testing.T) {
	m := p1Scene("delete-thread", false)
	title := m.menuTitleText()
	if !strings.HasPrefix(title, "DELETE THREAD · ") || !strings.HasSuffix(title, m.snapshot.Threads[0].Title) {
		t.Fatalf("title %q", title)
	}
}

// Locked settings in the overflow stay explicit pairs; the read-only note is
// stated once in the title.
func TestOverflowLockedSettingsArePairs(t *testing.T) {
	m := p1Scene("overflow-locked", false)
	if !strings.Contains(m.menuTitle, "read-only") {
		t.Fatalf("title %q", m.menuTitle)
	}
	pairs := 0
	for _, item := range m.menu {
		if strings.Contains(item.PairValue, "read-only") || strings.Contains(item.PairLabel, "read-only") {
			t.Fatalf("note inside pair %+v", item)
		}
		if item.Action.Kind == "settings" {
			if item.PairLabel == "" {
				t.Fatalf("setting row not a pair: %+v", item)
			}
			pairs++
		}
	}
	if pairs == 0 {
		t.Fatal("no setting pairs in overflow")
	}
}

// The selected mention row carries the gutter focus mark, and the popup's
// edges align with the composer outline below it.
func TestMentionPopupFocusMarkAndAlignment(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		m := p1Scene("mention", false)
		if ascii {
			m.state.Icons = "ascii"
		}
		f := m.render()
		r := m.mentionRect(f)
		h := controlHit(t, f, "mention:0")
		row := f.rows[h.Rect.Y]
		if got := ansi.Strip(cutCells(row, h.Rect.X-1, h.Rect.X)); got != m.icon("focus") {
			t.Fatalf("ascii %v: gutter %q, want focus mark %q: %q", ascii, got, m.icon("focus"), ansi.Strip(row))
		}
		if strings.Contains(row, "\x1b[4m") || strings.Contains(row, ";4m") {
			t.Fatalf("ascii %v: selected mention row still underlined", ascii)
		}
		below := f.rows[r.Y+r.H]
		left, right := ansi.Strip(cutCells(below, r.X, r.X+1)), ansi.Strip(cutCells(below, r.X+r.W-1, r.X+r.W))
		if left != "╭" || right != "╮" {
			t.Fatalf("ascii %v: popup edges %d..%d not over composer outline corners: %q", ascii, r.X, r.X+r.W-1, ansi.Strip(below))
		}
	}
}

func selectableMenuRows(items []menuItem) int {
	_, total := menuPosition(items, len(items)-1)
	return total
}
