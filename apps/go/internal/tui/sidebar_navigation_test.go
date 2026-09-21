package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestSidebarHeaderAndPinnedClosedShelf(t *testing.T) {
	for _, width := range []int{40, 47, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := navigationModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
			if width < 80 {
				m.activate(action{Kind: "column", Index: int(shell.LeftRegion)})
			}
			m.snapshot.Threads = nil
			for i := 0; i < 20; i++ {
				m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: fmt.Sprint(i), Title: fmt.Sprintf("Thread %d", i), ProjectID: "alpha", State: "idle", Closed: i >= 10})
			}
			m.state.RecentsCollapsed = true
			m.configureInputs()
			f := m.render()
			search, filter, add, create := controlHit(t, f, "thread-search"), controlHit(t, f, "projects"), controlHit(t, f, "project-add"), controlHit(t, f, "thread-create")
			if search.Rect.Y != filter.Rect.Y || filter.Rect.Y != add.Rect.Y || add.Rect.Y != create.Rect.Y || search.Rect.X+search.Rect.W > filter.Rect.X {
				t.Fatal("header controls overlap or wrap")
			}
			if strings.Contains(ansi.Strip(f.rows[search.Rect.Y]), "PROJECTS") {
				t.Fatal("obsolete header retained")
			}
			collapsed, gear := controlHit(t, f, "recents"), controlHit(t, f, "app-settings")
			if collapsed.Rect.Y >= gear.Rect.Y || f.closedNavigation.H != 0 || !strings.Contains(ansi.Strip(f.rows[collapsed.Rect.Y]), "Closed (10)") {
				t.Fatal("collapsed shelf or count incorrect")
			}
			m.navScroll = f.navMax
			scrolled := m.render()
			if controlHit(t, scrolled, "recents").Rect != collapsed.Rect || controlHit(t, scrolled, "app-settings").Rect != gear.Rect {
				t.Fatal("scroll moved fixed footer")
			}
			m.state.RecentsCollapsed = false
			f = m.render()
			if f.navigation.H < 4 || f.closedNavigation.H <= 0 || f.navigation.Y+f.navigation.H > controlHit(t, f, "recents").Rect.Y || f.closedNavigation.Y+f.closedNavigation.H > gear.Rect.Y {
				t.Fatal("expanded shelf violates viewport bounds")
			}
			openRect, closedRect := f.navigation, f.closedNavigation
			m.closedScroll = f.closedMax
			f = m.render()
			if f.navigation != openRect || f.closedNavigation != closedRect || m.navScroll != scrolled.navMax {
				t.Fatal("independent scrolling changed geometry or open offset")
			}
			for _, h := range f.hits {
				if strings.HasPrefix(h.Key, "thread:") && !openRect.Contains(h.Rect.X, h.Rect.Y) && !closedRect.Contains(h.Rect.X, h.Rect.Y) {
					t.Fatal("thread hit outside list viewport", h)
				}
			}
		})
	}
}

func TestSidebarThreadSearchScopesBothSections(t *testing.T) {
	m := navigationModel()
	active := m.state.Active
	m.prompt.SetValue("preserved draft")
	m.snapshot.Threads = []protocol.Thread{
		{ID: "open-match", Title: "Fix Search", ProjectID: "alpha"},
		{ID: "closed-match", Title: "SEARCH regression", ProjectID: "alpha", Closed: true},
		{ID: "wrong-project", Title: "Search", ProjectID: "beta", Closed: true},
		{ID: "wrong-title", Title: "Other", ProjectID: "alpha"},
	}
	m.state.ThreadFilter = "  sEaRcH  "
	m.state.ProjectFilter = "alpha"
	open, closed := m.navigationSections()
	if len(open) != 4 || len(closed) != 4 || open[1].thread.ID != "open-match" || closed[1].thread.ID != "closed-match" {
		t.Fatal("search/filter did not scope both sections", open, closed)
	}
	if m.state.Active != active || m.prompt.Value() != "preserved draft" {
		t.Fatal("search changed active work")
	}
}

func TestProjectMonogramUsesBoundedFirstLastGraphemes(t *testing.T) {
	for name, want := range map[string]string{"TUI": "TI", "my project": "MT", "  abc  ": "AC", "e\u0301clair": "E\u0301R", "界面": "界", "": "??"} {
		got := projectMonogram(name)
		if got != want || ansi.StringWidth(got) != 2 {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

func TestClosedShelfUsesAvailableHeightAndPreservesOpenCard(t *testing.T) {
	m := navigationModel()
	r := shell.Rect{W: 40, H: 30}
	for _, openRows := range []int{1, 4, 49} {
		open, closed, heading := m.navigationLayout(r, openRows, 49)
		if open.H != min(4, openRows) || closed.H != navigationViewport(r).H-open.H {
			t.Fatal("expanded shelf wasted available space or hid open card", open, closed)
		}
		if closed.Y != heading+1 || closed.Y+closed.H != r.Y+r.H-3 {
			t.Fatal("closed shelf moved away from footer")
		}
	}
}

func TestProjectBadgeAutomaticAndExplicitColors(t *testing.T) {
	m := navigationModel()
	m.colorProfile = colorprofile.TrueColor
	colors := map[string]bool{}
	for _, name := range []string{"Alpha", "Beta", "Gamma", "TUI", "Examples", "Work"} {
		project := protocol.Project{Name: name}
		colors[projectBadgeColorName(project)] = true
	}
	if len(colors) < 2 {
		t.Fatal("automatic colors all identical")
	}
	project := protocol.Project{Name: "Alpha", Color: "purple"}
	if projectBadgeColorName(project) != "purple" {
		t.Fatal("explicit color lost")
	}
	dark := m.projectBadgeStyle(project)
	m.state.Light = true
	light := m.projectBadgeStyle(project)
	if dark.foreground == dark.background || light.foreground == light.background || dark == light {
		t.Fatal("badge failed to adapt contrasting colors to theme")
	}
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.NoTTY} {
		m.colorProfile = profile
		visual := m.projectBadgeStyle(project)
		if strings.Contains(visual.foreground, "#") || strings.Contains(visual.background, "#") || visual.foreground == visual.background || !visual.bold {
			t.Fatal("fallback badge lost readable bounded styling", visual)
		}
	}
}
