package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func settingsPanelModel(page, project string) *Model {
	m := navigationModel()
	m.openSidebarSettings(page, project)
	return m
}

func TestWorkspaceSegmentsHaveSeparateBandedHitsAndRevisionBoundActions(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := settingsPanelModel("general", "")
		m.snapshot.AppSettings.Revision = 4
		f := m.measure()
		checkout := controlHit(t, f, "sidebar-setting:workspace:checkout")
		worktree := controlHit(t, f, "sidebar-setting:workspace:worktree")
		if hasControl(f, "sidebar-setting:workspace") {
			t.Fatal("menu button shown beside segments")
		}
		if checkout.Rect.H != 3 || worktree.Rect.H != 3 || checkout.Rect.Y != worktree.Rect.Y ||
			checkout.Rect.X+checkout.Rect.W >= worktree.Rect.X || worktree.Rect.X+worktree.Rect.W != f.settingsBody.X+f.settingsBody.W {
			t.Fatal("segments are not separate equal bands spanning the row", checkout.Rect, worktree.Rect, f.settingsBody)
		}
		// Hover never selects or dispatches.
		m.hover = worktree.Key
		m.render()
		if m.busy != nil || m.snapshot.AppSettings.WorkspaceDefault == "worktree" {
			t.Fatal("hover acted")
		}
		// The selected segment writes nothing.
		m.activate(checkout.Action)
		if m.busy != nil {
			t.Fatal("selected segment wrote the current value")
		}
		if mouse {
			// Any row of the band activates, including the half-block edges.
			m.Update(tea.MouseClickMsg{X: worktree.Rect.X + 1, Y: worktree.Rect.Y + 2, Button: tea.MouseLeft})
		} else {
			m.setFocus(worktree.Key)
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		c := m.busy
		if c == nil || c.Kind != "settings.update" || c.Revision != 4 || c.AppSettings.WorkspaceDefault != "worktree" || c.ThreadID != "" {
			t.Fatal("segment lost action, value or revision", c)
		}
	}
}

func TestProjectWorkspaceSegmentsSaveRevisionBoundOverride(t *testing.T) {
	m := settingsPanelModel("general", "beta")
	p := &m.snapshot.Projects[1]
	p.Revision = 9
	f := m.measure()
	if !controlHit(t, f, "sidebar-setting:workspace:default").Rect.Contains(controlHit(t, f, "sidebar-setting:workspace:default").Rect.X, controlHit(t, f, "sidebar-setting:workspace:default").Rect.Y+2) {
		t.Fatal("band hit does not cover its edges")
	}
	m.activate(controlHit(t, f, "sidebar-setting:workspace:default").Action)
	if m.busy != nil {
		t.Fatal("selected inherited default wrote")
	}
	clickControl(m, controlHit(t, f, "sidebar-setting:workspace:worktree"))
	c := m.busy
	if c == nil || c.Kind != "project.update" || c.ProjectID != "beta" || c.Revision != 9 || c.ProjectSettings.WorkspaceDefault != "worktree" || c.AppSettings != nil {
		t.Fatal("project segment lost scope or revision", c)
	}
	if p.WorkspaceDefault != "" || !strings.Contains(strings.Join(m.settingsText(), "\n"), "Using app default: Current checkout.") {
		t.Fatal("segment applied optimistically or lost inherited note")
	}
}

func TestSettingsTabReachesEverySegmentAndToggle(t *testing.T) {
	for _, page := range []string{"general", "appearance"} {
		m := settingsPanelModel(page, "")
		m.setFocus("sidebar-settings")
		var seen []string
		for range 40 {
			m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
			seen = append(seen, m.focus)
		}
		want := []string{"sidebar-setting:workspace:checkout", "sidebar-setting:workspace:worktree", "sidebar-setting:restart"}
		if page == "appearance" {
			want = []string{"sidebar-setting:theme:dark", "sidebar-setting:theme:light", "sidebar-setting:icons:nerd", "sidebar-setting:icons:ascii"}
		}
		for _, key := range want {
			if !slices.Contains(seen, key) {
				t.Fatal("Tab skipped", key, seen)
			}
		}
	}
	m := settingsPanelModel("general", "")
	m.setFocus("sidebar-setting:restart")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.AppSettings == nil || !m.busy.AppSettings.ContinueAfterRestart {
		t.Fatal("toggle did not dispatch restart-toggle", m.busy)
	}
}

func TestAppearanceSegmentsSetValuesWithoutToggling(t *testing.T) {
	m := settingsPanelModel("appearance", "")
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:theme:light"))
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:theme:light"))
	if !m.state.Light {
		t.Fatal("light segment did not set light once")
	}
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:icons:ascii"))
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:icons:ascii"))
	if m.iconsSetting() != "ascii" || m.state.Icons != "ascii" {
		t.Fatal("ASCII segment did not set ASCII once")
	}
	m.activate(action{Kind: "project-settings", ID: "alpha"})
	m.activate(action{Kind: "theme-set", Value: "dark"})
	if !m.state.Light {
		t.Fatal("project scope changed the app theme")
	}
}

func TestNarrowWorkspaceFallsBackToMenuButton(t *testing.T) {
	m := navigationModel()
	m.Update(tea.WindowSizeMsg{Width: 47, Height: 22})
	m.openSidebarSettings("general", "beta")
	f := m.measure()
	if hasControl(f, "sidebar-setting:workspace:default") {
		t.Fatal("segments shown where labels cannot fit")
	}
	clickControl(m, controlHit(t, f, "sidebar-setting:workspace"))
	if len(m.menu) != 3 || m.menu[0].Label != "Use app default" {
		t.Fatal("fallback lost the menu", m.menu)
	}
}

func TestLimitedColorSettingsUseBracketedSingleRows(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ASCII} {
		m := settingsPanelModel("general", "")
		m.colorProfile = profile
		f := m.render()
		h := controlHit(t, f, "sidebar-setting:workspace:worktree")
		if h.Rect.H != 1 {
			t.Fatal("limited palette kept banded segments", profile)
		}
		screen := ansi.Strip(strings.Join(f.rows, "\n"))
		if strings.ContainsAny(screen, "▀▄") || !strings.Contains(screen, "[") || !strings.Contains(ansi.Strip(f.rows[h.Rect.Y]), "]") {
			t.Fatal("fallback painted blocks or lost end cells", profile)
		}
		toggle := controlHit(t, f, "sidebar-setting:restart")
		row := ansi.Strip(f.rows[toggle.Rect.Y])
		if profile == colorprofile.ASCII && !strings.Contains(row, "Off [o   ]") {
			t.Fatal("monochrome toggle lost its reserved-cell state", row)
		}
	}
	m := settingsPanelModel("general", "")
	m.state.Icons = "ascii"
	m.applyIcons()
	if controlHit(t, m.measure(), "sidebar-setting:workspace:worktree").Rect.H != 1 {
		t.Fatal("ASCII symbols kept half-block bands")
	}
}

func TestToggleShowsStateInWordAndTrack(t *testing.T) {
	m := settingsPanelModel("general", "")
	off := m.render()
	h := controlHit(t, off, "sidebar-setting:restart")
	if row := ansi.Strip(off.rows[h.Rect.Y]); !strings.Contains(row, "Continue threads after restart") || !strings.HasSuffix(strings.TrimRight(row[:strings.Index(row, "Off")+3], " "), "Off") {
		t.Fatal("off state missing", row)
	}
	m.snapshot.AppSettings.ContinueAfterRestart = true
	on := m.render()
	if row := ansi.Strip(on.rows[h.Rect.Y]); !strings.Contains(row, " On ") {
		t.Fatal("on state missing", row)
	}
	if on.rows[h.Rect.Y] == off.rows[h.Rect.Y] || !strings.Contains(m.toggleTrack(true), "48;2;138;183;255") {
		t.Fatal("on track does not use the accent fill")
	}
}

func TestToggleLabelTruncatesBeforeWordAndSwitchAtNarrowWidths(t *testing.T) {
	for _, width := range []int{minTerminalWidth, 44} {
		m := navigationModel()
		m.Update(tea.WindowSizeMsg{Width: width, Height: 60})
		m.openSidebarSettings("general", "")
		f := m.render()
		h := controlHit(t, f, "sidebar-setting:restart")
		row := ansi.Strip(f.rows[h.Rect.Y])
		idx := strings.Index(row, "Off")
		if idx <= 0 || row[idx-1] != ' ' {
			t.Fatalf("On/Off word lost its separating space at width %d: %q", width, row)
		}
		label := strings.TrimRight(row[:idx], " ")
		if !strings.HasSuffix(label, "…") {
			t.Fatalf("label was not ellipsis-truncated before the word and switch at width %d: %q", width, row)
		}
	}
}

func TestOnKnobContrastsWithLightCanvas(t *testing.T) {
	m := settingsPanelModel("general", "")
	m.state.Light = true
	p := m.colors()
	if m.knobFill() == p.canvas {
		t.Fatal("light theme on-knob matches the canvas color")
	}
}

func TestSettingsPanelPaintingDoesNotGrowANSI(t *testing.T) {
	for _, page := range []string{"general", "appearance", "about", "keybindings"} {
		m := settingsPanelModel(page, "")
		base := m.render().rows
		for _, h := range m.measure().hits {
			m.hover = h.Key
			m.setFocus(h.Key)
			m.render()
		}
		m.hover = ""
		m.setFocus("sidebar-settings")
		again := m.render().rows
		for i := range base {
			if len(again[i]) != len(base[i]) {
				t.Fatal("row styling grew after repeated painting", page, i, len(base[i]), len(again[i]))
			}
		}
	}
}
