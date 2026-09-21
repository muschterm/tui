package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestSidebarAppActionsSeparatorPreservesShelfAndControls(t *testing.T) {
	for _, width := range []int{40, 160} {
		for _, empty := range []bool{false, true} {
			for _, collapsed := range []bool{false, true} {
				for _, plain := range []bool{false, true} {
					t.Run(fmt.Sprintf("width%d/empty%t/collapsed%t/plain%t", width, empty, collapsed, plain), func(t *testing.T) {
						m := navigationModel()
						m.plainIcons = plain
						m.state.RecentsCollapsed = collapsed
						if empty {
							m.snapshot.Threads = nil
							m.state.Active = ""
						} else {
							m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: "closed-footer", Title: "Closed footer", ProjectID: "alpha", Closed: true, State: "idle"})
						}
						m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
						if width < 80 {
							m.activate(action{Kind: "column", Index: int(shell.LeftRegion)})
						}
						m.configureInputs()
						f := m.render()
						gear, closed := controlHit(t, f, "app-settings"), controlHit(t, f, "recents")
						y := gear.Rect.Y - 1
						rule := "─"
						if plain {
							rule = "-"
						}
						if got := ansi.Strip(ansi.Cut(f.rows[y], closed.Rect.X, closed.Rect.X+closed.Rect.W)); got != strings.Repeat(rule, closed.Rect.W) {
							t.Fatalf("app action separator = %q", got)
						}
						if closed.Rect.Y >= y || f.closedNavigation.Y+f.closedNavigation.H > y || f.navigation.Y+f.navigation.H > closed.Rect.Y {
							t.Fatal("separator overlaps open or Closed content")
						}
						for _, h := range f.hits {
							if h.Rect.Contains(gear.Rect.X, y) {
								t.Fatalf("separator acquired interactive target: %s", h.Key)
							}
						}
						focus, active := m.focus, m.state.Active
						m.mouse(tea.MouseClickMsg{X: gear.Rect.X, Y: y, Button: tea.MouseLeft})
						if m.focus != focus || m.state.Active != active || m.busy != nil || m.settingsPage != "" || len(m.menu) != 0 {
							t.Fatal("clicking separator changed interaction state")
						}
						m.navScroll, m.closedScroll = f.navMax, f.closedMax
						if after := m.measure(); controlHit(t, after, "app-settings").Rect != gear.Rect || controlHit(t, after, "recents").Rect != closed.Rect {
							t.Fatal("scrolling shifted app actions or Closed heading")
						}
						clickControl(m, gear)
						if m.settingsPage == "" {
							t.Fatal("settings gear lost its click target")
						}
					})
				}
			}
		}
	}
}

func TestTightClosedShelfKeepsBothTitleRowsReachable(t *testing.T) {
	m := navigationModel()
	for _, available := range []int{4, 5, 6, 7} {
		r := shell.Rect{W: 47, H: available + 7}
		open, closed, heading := m.navigationLayout(r, 9, 9)
		if open.H < 2 || closed.H < 2 || open.H+closed.H != available {
			t.Fatalf("height %d lost a usable list: open %+v closed %+v", available, open, closed)
		}
		if available >= 6 && open.H < 4 {
			t.Fatal("full open card lost despite sufficient space")
		}
		if open.Y+open.H != heading || closed.Y != heading+1 || closed.Y+closed.H != r.Y+r.H-3 {
			t.Fatal("tight allocation moved footer or overlapped list geometry")
		}
	}
}
