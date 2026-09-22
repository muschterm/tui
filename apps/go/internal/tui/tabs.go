package tui

import (
	"fmt"
	"slices"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type tabSlot struct {
	tab   shell.Surface
	width int
}

// Keep a contiguous group containing the active tab; reserve overflow space
// only when complete tabs do not fit. Long names may be clipped within a tab.
func visibleTabs(tabs []shell.Surface, active string, width int) ([]tabSlot, bool) {
	if len(tabs) == 0 || width <= 0 {
		return nil, false
	}
	slots := make([]tabSlot, len(tabs))
	total := max(0, len(tabs)-1)
	for i, tab := range tabs {
		w := min(24, ansi.StringWidth(tab.Title)+5)
		slots[i] = tabSlot{tab, w}
		total += w
	}
	if total <= width {
		return slots, false
	}
	if len(slots) == 1 {
		slots[0].width = width
		return slots, false
	}
	available := max(1, width-4)
	i := max(0, slices.IndexFunc(tabs, func(t shell.Surface) bool { return t.ID == active }))
	slots[i].width = min(slots[i].width, available)
	used, first, last := slots[i].width, i, i+1
	for first > 0 && used+1+slots[first-1].width <= available {
		first--
		used += 1 + slots[first].width
	}
	for last < len(slots) && used+1+slots[last].width <= available {
		used += 1 + slots[last].width
		last++
	}
	return slots[first:last], true
}

func tabEntry(tab shell.Surface) menuItem {
	return menuItem{tab.Title, action{Kind: "tab", ID: tab.ID, Value: tab.Kind}}
}

// The icon and name occupy disjoint hit areas. Only the icon slot closes;
// focusing it by keyboard exposes the same close affordance as hover.
// Cells: end cap, glyph, its spill cell, one gap, the title, end cap. The
// padding matches T3's tight tab insets rather than centering the glyph.
func (f *frame) tab(m *Model, x, y, width int, title, kind, selectKey, closeKey string, selectAction, closeAction action, active bool) {
	p := m.colors()
	state := m.controlState(active, selectKey, closeKey)
	v := m.componentStyle(squareFill, state, p.text, p.input)
	closeVisible := state.Hovered || m.focus == closeKey
	icon := m.icon(kind)
	if closeVisible {
		icon = m.icon("close")
	}
	// Preserve the independent three-cell close slot and at least one title
	// cell. Tiny hosts can omit borders without losing either action.
	if width >= 6 {
		f.compactControl(m, x, y, width, fit(icon, 3)+fit(singleLine(title), width-5), v)
		// The glyph and the cell it can spill into close; the end caps and
		// the gap before the title select the tab.
		f.hits = append(f.hits,
			hit{Rect: shell.Rect{X: x + 1, Y: y, W: 2, H: 1}, Action: closeAction, Label: "Close " + title, Key: closeKey, Slot: shell.Rect{X: x + 1, Y: y, W: 3, H: 1}},
			hit{Rect: shell.Rect{X: x + 3, Y: y, W: width - 3, H: 1}, Action: selectAction, Label: title, Key: selectKey},
			hit{Rect: shell.Rect{X: x, Y: y, W: 1, H: 1}, Action: selectAction, Label: title, Key: selectKey},
		)
		return
	}
	iconWidth := min(3, width)
	f.styledButton(x, y, iconWidth, centered(icon, iconWidth), closeKey, closeAction, v)
	f.hits[len(f.hits)-1].Label = "Close " + title
	f.styledButton(x+iconWidth, y, max(0, width-iconWidth), title, selectKey, selectAction, v)
}

// Terminal tabs are numbered past the highest existing number in their host,
// so a reopened instance never repeats a live tab's name.
func nextTerminalTitle(tabs []shell.Surface) string {
	highest := 0
	for _, tab := range tabs {
		var n int
		if tab.Kind == "terminal" {
			if _, err := fmt.Sscanf(tab.Title, "Terminal %d", &n); err == nil {
				highest = max(highest, n)
			}
		}
	}
	return fmt.Sprintf("Terminal %d", highest+1)
}

// openTerminalTab adds and selects a tab for an accepted server terminal,
// keyed by the server's stable terminal identity.
func openTerminalTab(h *shell.Host, id string) {
	tab := h.Open("terminal", nextTerminalTitle(h.Tabs))
	for i := range h.Tabs {
		if h.Tabs[i].ID == tab.ID {
			h.Tabs[i].ID = id
		}
	}
	h.ActiveID = id
}

// Saved views from before bottom tabs recorded one session id. Present it as
// a tab so the surviving session stays reachable; nothing is opened or closed.
func (m *Model) migrateBottomSessions() {
	for _, views := range []map[string]*threadView{m.state.Threads, m.state.DraftThreads} {
		for _, v := range views {
			if v != nil && v.BottomID != "" {
				if len(v.Bottom.Tabs) == 0 {
					openTerminalTab(&v.Bottom, v.BottomID)
				}
				v.BottomID = ""
			}
		}
	}
}
