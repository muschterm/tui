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
	total := max(0, len(tabs)-1)
	for i, tab := range tabs {
		w := min(26, ansi.StringWidth(tab.Title)+7)
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
		f.compactControl(m, x, y, width, centered(icon, 3)+fit(safe(title), width-5), v)
		middle := y
		f.hits = append(f.hits,
			hit{shell.Rect{X: x + 1, Y: middle, W: 3, H: 1}, closeAction, "Close " + title, closeKey},
			hit{shell.Rect{X: x + 4, Y: middle, W: width - 5, H: 1}, selectAction, title, selectKey},
		)
		for _, capX := range []int{x, x + width - 1} {
			f.hits = append(f.hits, hit{shell.Rect{X: capX, Y: middle, W: 1, H: 1}, selectAction, title, selectKey})
		}

		return
	}
	iconWidth := min(3, width)
	f.styledButton(x, y, iconWidth, centered(icon, iconWidth), closeKey, closeAction, v)
	f.hits[len(f.hits)-1].Label = "Close " + title
	f.styledButton(x+iconWidth, y, max(0, width-iconWidth), title, selectKey, selectAction, v)
}
