package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func controlHit(t *testing.T, f frame, key string) hit {
	t.Helper()
	for _, h := range f.hits {
		if h.Key == key {
			return h
		}
	}
	t.Fatalf("missing control %q", key)
	return hit{}
}

func clickControl(m *Model, h hit) {
	m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
}

func TestTabOverflowRequiresHiddenTabsAndKeepsActiveVisible(t *testing.T) {
	tabs := []shell.Surface{{ID: "a", Title: "Files", Kind: "files"}, {ID: "b", Title: "Activity", Kind: "activity"}, {ID: "c", Title: "Plan", Kind: "plan"}}
	for _, tc := range []struct {
		tabs   []shell.Surface
		active string
		width  int
	}{{tabs, "a", 100}, {tabs, "c", 20}, {tabs, "b", 20}, {tabs[:1], "a", 6}} {
		slots, overflow := visibleTabs(tc.tabs, tc.active, tc.width)
		if overflow != (len(slots) < len(tc.tabs)) {
			t.Fatalf("overflow=%v with %d of %d tabs visible at width %d", overflow, len(slots), len(tc.tabs), tc.width)
		}
		found := false
		for _, slot := range slots {
			found = found || slot.tab.ID == tc.active
		}
		if !found {
			t.Fatal("active tab hidden")
		}
	}
}

func TestTabNameSelectsAndOnlyIconCloses(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.openSurface("files", "")
	files, _ := m.viewState().Host.Active()
	m.openSurface("plan", "")
	f := m.render()
	name, icon := controlHit(t, f, "tab:"+files.ID), controlHit(t, f, "close:"+files.ID)
	if name.Rect.Contains(icon.Rect.X, icon.Rect.Y) || icon.Rect.Contains(name.Rect.X, name.Rect.Y) {
		t.Fatal("tab icon and label overlap")
	}
	if !strings.Contains(ansi.Strip(f.rows[icon.Rect.Y]), m.icon("files")) {
		t.Fatal("resting tab omitted surface icon")
	}
	m.hover = name.Key
	f = m.render()
	iconText := ansi.Cut(ansi.Strip(f.rows[icon.Rect.Y]), icon.Rect.X, icon.Rect.X+icon.Rect.W)
	if iconText != " "+m.icon("close")+" " {
		t.Fatalf("hover did not replace icon with close: %q", iconText)
	}
	clickControl(m, name)
	active, _ := m.viewState().Host.Active()
	if len(m.viewState().Host.Tabs) != 2 || active.ID != files.ID {
		t.Fatal("clicking tab name must select without closing")
	}
	clickControl(m, controlHit(t, m.measure(), icon.Key))
	if len(m.viewState().Host.Tabs) != 1 || m.viewState().Host.Tabs[0].ID == files.ID {
		t.Fatal("icon did not close its tab")
	}
}

func TestMenuCloseIconIsCenteredAndClosesOnlyMenu(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m := testModel()
		m.plainIcons = plain
		m.openSurface("files", "")
		m.openCommands()
		m.hover = "menu-close"
		f := m.render()
		h := controlHit(t, f, "menu-close")
		label := ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W))
		if label != " "+m.icon("close")+" " {
			t.Fatalf("close glyph is not centered: %q", label)
		}
		clickControl(m, h)
		if len(m.menu) != 0 || len(m.viewState().Host.Tabs) != 1 {
			t.Fatal("centered close target changed its action")
		}
	}
}

func TestTabMenuListsOnceAndDeleteClosesTarget(t *testing.T) {
	m := testModel()
	m.openSurface("files", "")
	m.openSurface("plan", "")
	m.openSurface("activity", "")
	m.activate(action{Kind: "tabs"})
	if len(m.menu) != len(m.viewState().Host.Tabs) {
		t.Fatal("tab menu contains duplicate select/close rows")
	}
	seen := make(map[string]bool)
	for _, item := range m.menu {
		if item.Action.Kind != "tab" || seen[item.Action.ID] {
			t.Fatal("tab menu must have one selectable row per tab")
		}
		seen[item.Action.ID] = true
	}
	m.menuIndex = 1
	id := m.menu[1].Action.ID
	m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if len(m.viewState().Host.Tabs) != 2 {
		t.Fatal("Delete failed to close a single tab")
	}
	for _, tab := range m.viewState().Host.Tabs {
		if tab.ID == id {
			t.Fatal("Delete closed the wrong tab")
		}
	}
}

func TestPaneIconsDistinguishOpenAndClosed(t *testing.T) {
	m := testModel()
	for _, plain := range []bool{false, true} {
		m.plainIcons = plain
		for _, pane := range []string{"left", "right", "bottom"} {
			if m.paneIcon(pane, true) == m.paneIcon(pane, false) || strings.Contains(m.paneIcon(pane, true), "?") {
				t.Fatalf("pane %s lacks distinct state icons (plain=%v)", pane, plain)
			}
		}
	}
}

func TestScrollbarPointerRoutesToOnlyItsSurface(t *testing.T) {
	for _, id := range []string{"transcript", "detail", "request"} {
		t.Run(id, func(t *testing.T) {
			m := scrollModel()
			f := m.measure()
			bar, ok := f.scrollbars[id]
			if !ok {
				t.Fatal("fixture must overflow")
			}
			m.Update(tea.MouseClickMsg{X: bar.Rect.X, Y: bar.Rect.Y + bar.Rect.H - 1, Button: tea.MouseLeft})
			f = m.measure()
			bar = f.scrollbars[id]
			if bar.Bar.Offset != min(bar.Bar.Viewport, bar.Bar.MaxOffset) {
				t.Fatalf("track click did not page: %+v", bar.Bar)
			}
			m.Update(tea.MouseClickMsg{X: bar.Rect.X, Y: bar.Rect.Y + bar.Bar.ThumbStart, Button: tea.MouseLeft})
			m.Update(tea.MouseMotionMsg{X: bar.Rect.X, Y: bar.Rect.Y + bar.Rect.H + 4, Button: tea.MouseLeft})
			m.Update(tea.MouseReleaseMsg{X: bar.Rect.X, Y: bar.Rect.Y + bar.Rect.H + 4, Button: tea.MouseLeft})
			if m.measure().scrollbars[id].Bar.Offset != bar.Bar.MaxOffset || m.scrollDrag != "" {
				t.Fatal("thumb drag did not reach bottom or release")
			}
			m.Update(tea.MouseWheelMsg{X: bar.Rect.X, Y: bar.Rect.Y, Button: tea.MouseWheelUp})
			f = m.measure()
			if f.scrollbars[id].Bar.Offset != max(0, bar.Bar.MaxOffset-3) {
				t.Fatal("wheel over scrollbar did not target its surface")
			}
			for _, other := range []string{"transcript", "detail", "request"} {
				if other != id && f.scrollbars[other].Bar.Offset != 0 {
					t.Fatalf("scrolling %s moved %s", id, other)
				}
			}
		})
	}
}

func TestModalScrollbarCannotRouteUnderlyingSurface(t *testing.T) {
	m := scrollModel()
	under := m.measure().scrollbars["transcript"]
	m.openCommands()
	m.height = 24 // Make the command menu overflow independently of content.
	f := m.measure()
	if len(f.scrollbars) != 1 || !f.scrollbars["menu"].Bar.Visible() {
		t.Fatal("modal retained underlying scrollbar targets")
	}
	m.Update(tea.MouseWheelMsg{X: under.Rect.X, Y: under.Rect.Y, Button: tea.MouseWheelDown})
	bar := m.measure().scrollbars["menu"]
	m.Update(tea.MouseClickMsg{X: bar.Rect.X, Y: bar.Rect.Y + bar.Rect.H - 1, Button: tea.MouseLeft})
	if m.viewState().Scroll != 0 || m.viewState().DetailScroll != 0 || m.viewState().RequestScroll != 0 {
		t.Fatal("modal scrolling moved content underneath")
	}
}

func TestKeyboardTabEscapesScrollbarRows(t *testing.T) {
	m := scrollModel()
	m.setFocus("scrollbar-transcript")
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus == "scrollbar-transcript" {
		t.Fatal("Tab trapped by repeated scrollbar row hits")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focus != "scrollbar-transcript" {
		t.Fatal("reverse Tab did not return to the scrollbar once")
	}
}
