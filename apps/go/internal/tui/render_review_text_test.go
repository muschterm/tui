package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReviewSafeDropsBidiControlsAndLineSeparators(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"LRE", "a\u202ab", "ab"}, {"RLE", "a\u202bb", "ab"}, {"PDF", "a\u202cb", "ab"},
		{"LRO", "a\u202db", "ab"}, {"RLO", "a\u202eb", "ab"},
		{"LRI", "a\u2066b", "ab"}, {"RLI", "a\u2067b", "ab"}, {"FSI", "a\u2068b", "ab"}, {"PDI", "a\u2069b", "ab"},
		{"LRM", "a\u200eb", "ab"}, {"RLM", "a\u200fb", "ab"}, {"ALM", "a\u061cb", "ab"},
		{"line separator", "a\u2028b", "ab"}, {"paragraph separator", "a\u2029b", "ab"},
		{"escape", "a\x1b[31mb\x1b[0m", "ab"}, {"C1", "a\u009bb", "ab"},
		{"newline kept", "a\nb", "a\nb"}, {"tab expanded", "a\tb", "a    b"},
		{"ZWJ emoji", "👩🏽\u200d💻", "👩🏽\u200d💻"}, {"ZWNJ", "می\u200cخواهم", "می\u200cخواهم"},
		{"variation selector", "❤️", "❤️"}, {"rtl text kept", "שלום", "שלום"},
	} {
		if got := safe(tc.in); got != tc.want {
			t.Errorf("%s: safe(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestReviewChromeControlsFitTheirSlotsInBothIconModes(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, width := range []int{47, 120, 200} {
			for _, maximized := range []bool{false, true} {
				m := testModel()
				m.plainIcons = plain
				m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				m.activate(action{Kind: "open", Value: "plan"})
				m.activate(action{Kind: "bottom"})
				m.state.Layout.Maximized = maximized
				m.menu = nil
				m.configureInputs()
				f := m.render()
				top := ansi.Strip(f.rows[0])
				// The crumb is confined to the center pane; beside an open right
				// host it only has room to avoid truncation at wide sizes.
				if strings.Contains(top, "…") && !strings.Contains(m.thread().Project+m.thread().Title, "…") && width >= 200 {
					t.Fatalf("plain=%v %d: truncated chrome: %q", plain, width, top)
				}
				for _, h := range f.hits {
					if h.Rect.Y != 0 {
						continue
					}
					cell := ansi.Strip(cutCells(f.rows[0], h.Rect.X, h.Rect.X+h.Rect.W))
					// The breadcrumb is text that truncates by design.
					if strings.Contains(cell, "…") && h.Key != "breadcrumb-project" || ansi.StringWidth(cell) != h.Rect.W {
						t.Fatalf("plain=%v %d: control %q overflows its %d-cell slot: %q", plain, width, h.Key, h.Rect.W, cell)
					}
				}
				for _, name := range []string{"left", "left-off", "right", "right-off", "bottom", "bottom-off", "maximize", "restore", "menu"} {
					for _, slot := range []int{5, 6, 7} {
						if got := centered(m.icon(name), slot); ansi.StringWidth(got) != slot || !strings.Contains(got, m.icon(name)) {
							t.Fatalf("plain=%v: %q does not fit %d cells: %q", plain, name, slot, got)
						}
					}
				}
			}
		}
	}
	m := testModel()
	m.plainIcons = true
	if got := m.paneIcon("left", true, 5); got != " L+  " {
		t.Fatalf("ascii left pane icon %q", got)
	}
}

func TestReviewDismissibleSummaryHasNoDeadLeadingCell(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID != m.state.Active {
			continue
		}
		for j := range m.snapshot.Threads[i].Children {
			m.snapshot.Threads[i].Children[j].State = "completed"
		}
	}
	if !agentSummary(m.thread()).Dismissible {
		t.Skip("fixture has no dismissible agent summary")
	}
	m.configureInputs()
	f := m.measure()
	dismiss := controlHit(t, f, "dismiss-agents")
	var label []hit
	for _, h := range f.hits {
		if h.Key == "agents" {
			label = append(label, h)
		}
	}
	at := func(x int) string {
		key := ""
		for _, h := range f.hits {
			if h.Rect.Contains(x, dismiss.Rect.Y) {
				key = h.Key
			}
		}
		return key
	}
	x := dismiss.Rect.X - 1
	if at(x) != "agents" || at(x+1) != "dismiss-agents" || at(x+2) != "agents" {
		t.Fatalf("summary cells: %q %q %q (%#v)", at(x), at(x+1), at(x+2), label)
	}
	for _, h := range label {
		if h.Rect.Contains(dismiss.Rect.X, dismiss.Rect.Y) {
			t.Fatal("label target covers the dismiss slot")
		}
	}
	before := len(m.viewState().Host.Tabs)
	m.Update(tea.MouseClickMsg{X: x, Y: dismiss.Rect.Y, Button: tea.MouseLeft})
	if len(m.viewState().Host.Tabs) != before+1 || m.viewState().DismissedAgents != "" {
		t.Fatal("leading cell did not open Agents")
	}
}

func TestReviewActivityRowKeysCannotCollide(t *testing.T) {
	m := wideModel(t, 120, 40)
	m.viewState().Scroll = 1 << 20 // the tool rows are last; rendering clamps
	seen := map[string]action{}
	for _, h := range m.measure().hits {
		if !strings.HasPrefix(h.Key, "activity:") {
			continue
		}
		if parts := strings.Split(h.Key, ":"); len(parts) != 3 || parts[1] != h.Action.ID {
			t.Fatalf("ambiguous activity key %q", h.Key)
		}
		if previous, ok := seen[h.Key]; ok && previous != h.Action {
			t.Fatalf("key %q names two actions", h.Key)
		}
		seen[h.Key] = h.Action
	}
	if len(seen) == 0 {
		t.Fatal("no activity rows")
	}
}
