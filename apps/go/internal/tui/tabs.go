package tui

import (
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
	total := 0
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
	for first > 0 && used+slots[first-1].width <= available {
		first--
		used += slots[first].width
	}
	for last < len(slots) && used+slots[last].width <= available {
		used += slots[last].width
		last++
	}
	return slots[first:last], true
}

func tabEntry(tab shell.Surface) menuItem {
	return menuItem{tab.Title, action{Kind: "tab", ID: tab.ID, Value: tab.Kind}}
}

// The icon and name occupy disjoint hit areas. Only the icon slot closes;
// focusing it by keyboard exposes the same close affordance as hover.
func (f *frame) tab(m *Model, x, y, width int, title, kind, selectKey, closeKey string, selectAction, closeAction action, active bool, bg string) {
	p := m.colors()
	hovered := m.hover == selectKey || m.hover == closeKey
	closeVisible := hovered || m.focus == closeKey
	fg := p.muted
	if active || hovered || m.focus == selectKey || m.focus == closeKey {
		fg, bg = p.text, p.selected
	}
	icon := m.icon(kind)
	if closeVisible {
		icon = m.icon("close")
	}
	iconWidth := min(3, width)
	f.button(m, x, y, iconWidth, centered(icon, iconWidth), closeKey, closeAction, fg, bg)
	f.hits[len(f.hits)-1].Label = "Close " + title
	f.button(m, x+iconWidth, y, max(0, width-iconWidth), title, selectKey, selectAction, fg, bg)
}
