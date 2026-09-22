package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func settingsCategoryHit(t *testing.T, f frame, page string) hit {
	t.Helper()
	for _, h := range f.hits {
		if h.Action.Kind == "settings-page" && h.Action.Value == page {
			return h
		}
	}
	t.Fatalf("settings category %q is unavailable", page)
	return hit{}
}

func assertSettingsTakeover(t *testing.T, m *Model) frame {
	t.Helper()
	f := m.render()
	for _, key := range []string{"prompt", "transcript", "send", "stop", "interrupt", "attachment", "thread-search", "surface-add", "terminal-new"} {
		if hasControl(f, key) {
			t.Errorf("workspace control %q remains interactive behind settings", key)
		}
	}
	for name, r := range map[string]shell.Rect{"prompt": f.prompt, "answer": f.answer, "transcript": f.transcript, "detail": f.detail, "bottom": f.bottomBody, "requests": f.request} {
		if r.W > 0 && r.H > 0 {
			t.Errorf("workspace %s remains visible in settings: %+v", name, r)
		}
	}
	return f
}

func settingsPreservedState(t *testing.T, m *Model) string {
	t.Helper()
	view := m.state
	view.Generation = 0 // Persistence sequencing is independent of workspace content.
	data, err := json.Marshal(struct {
		View           savedView
		Prompt, Answer string
		Nav, Closed    int
	}{view, m.prompt.Value(), m.answer.Value(), m.navScroll, m.closedScroll})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSettingsTakeoverWideCategoriesAndFullContent(t *testing.T) {
	for _, project := range []bool{false, true} {
		t.Run(fmt.Sprint(project), func(t *testing.T) {
			m := navigationModel()
			if project {
				m.activate(action{Kind: "project-settings", ID: "alpha"})
			} else {
				m.activate(action{Kind: "app-settings"})
			}
			want := "general"
			if project {
				want = "project"
			}
			if m.settingsPage != want {
				t.Fatal("wrong initial settings page", m.settingsPage)
			}
			f := assertSettingsTakeover(t, m)
			if f.geom.Center.W < 80 || f.settingsBody.X < 20 || f.geom.Center.X+f.geom.Center.W != m.width {
				t.Fatal("configuration did not replace central and right workspace", f.settingsBody)
			}
			pages := []string{"general", "appearance", "keybindings", "about"}
			if project {
				pages = []string{"project", "general", "keybindings"}
			}
			for _, page := range pages {
				h := settingsCategoryHit(t, f, page)
				if h.Rect.X+h.Rect.W > f.settingsBody.X {
					t.Fatal("category leaked into main settings form", h)
				}
				clickControl(m, h)
				f = assertSettingsTakeover(t, m)
				if m.settingsPage != page {
					t.Fatal("category did not select form", page, m.settingsPage)
				}
				if project && m.settingsProjectID != "alpha" {
					t.Fatal("switching category lost project settings context")
				}
				h = settingsCategoryHit(t, f, page)
				if !strings.Contains(ansi.Strip(f.rows[h.Rect.Y]), h.Label) {
					t.Fatal("selected category missing from sidebar")
				}
			}
			if !project {
				for _, h := range f.hits {
					if h.Action.Kind == "settings-page" && h.Action.Value == "project" {
						t.Fatal("app settings unexpectedly offered project category")
					}
				}
			}
			back := controlHit(t, f, "settings-back")
			if back.Rect.X > 5 || back.Rect.Y < m.height-5 {
				t.Fatal("Back is not anchored at bottom left", back)
			}
			clickControl(m, back)
			if m.settingsPage != "" {
				t.Fatal("Back did not exit settings")
			}
		})
	}
}

func TestSettingsTakeoverPreservesWorkspaceAndActiveWork(t *testing.T) {
	for _, maximized := range []bool{false, true} {
		m := navigationModel()
		m.state.Layout.Left = false
		m.state.Layout.Right, m.state.Layout.Bottom = true, true
		m.viewState().RightVisible = true
		m.viewState().Host.Open("files", "Files")
		m.viewState().Host.Open("terminal", "Existing shell")
		openTerminalTab(&m.viewState().Bottom, "existing-bottom-session")
		m.state.Layout.Maximized = maximized
		m.prompt.SetValue("Unsent prompt survives settings")
		m.viewState().Draft = m.prompt.Value()
		m.viewState().CompactColumn = shell.RightRegion
		m.configureInputs()
		f := m.measure()
		m.viewState().Scroll = min(2, f.transcriptMax)
		m.viewState().DetailScroll = min(2, f.detailMax)
		m.viewState().RequestScroll = min(1, f.requestMax)
		before := settingsPreservedState(t, m)
		snapshot, _ := json.Marshal(m.snapshot)
		m.activate(action{Kind: "project-settings", ID: "alpha"})
		for _, page := range []string{"general", "keybindings", "project"} {
			m.activate(action{Kind: "settings-page", Value: page})
			assertSettingsTakeover(t, m)
			if got := settingsPreservedState(t, m); got != before {
				t.Fatalf("settings category %s mutated workspace\nbefore %s\nafter %s", page, before, got)
			}
		}
		m.activate(action{Kind: "settings-back"})
		if m.settingsPage != "" || settingsPreservedState(t, m) != before {
			t.Fatal("exiting settings failed to restore workspace")
		}
		after, _ := json.Marshal(m.snapshot)
		if string(snapshot) != string(after) || m.busy != nil {
			t.Fatal("viewing settings altered execution")
		}
	}
}

func TestSettingsTakeoverCompactNavigationAndResize(t *testing.T) {
	for _, width := range []int{40, 47} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := navigationModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: 22})
			m.selectColumn(shell.RightRegion)
			m.prompt.SetValue("compact unsent draft")
			m.viewState().Draft = m.prompt.Value()
			before := settingsPreservedState(t, m)
			m.activate(action{Kind: "app-settings"})
			f := assertSettingsTakeover(t, m)
			if f.settingsBody.H == 0 {
				t.Fatal("compact settings did not open form")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
			f = assertSettingsTakeover(t, m)
			category := settingsCategoryHit(t, f, "appearance")
			clickControl(m, category)
			f = assertSettingsTakeover(t, m)
			if m.settingsPage != "appearance" || f.settingsBody.H == 0 {
				t.Fatal("compact category did not open its form")
			}
			m.Update(tea.WindowSizeMsg{Width: 35, Height: 12})
			m.Update(tea.WindowSizeMsg{Width: width, Height: 22})
			assertSettingsTakeover(t, m)
			if settingsPreservedState(t, m) != before {
				t.Fatal("compact settings or resize changed stored workspace")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
			clickControl(m, controlHit(t, m.measure(), "settings-back"))
			if m.settingsPage != "" || settingsPreservedState(t, m) != before {
				t.Fatal("compact Back failed to restore workspace")
			}
			m.activate(action{Kind: "app-settings"})
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.settingsPage != "" || settingsPreservedState(t, m) != before {
				t.Fatal("compact Escape failed to restore workspace")
			}
			m.activate(action{Kind: "app-settings"})
			clickControl(m, controlHit(t, m.measure(), "settings-close"))
			if m.settingsPage != "" || settingsPreservedState(t, m) != before {
				t.Fatal("compact close control failed to restore workspace")
			}
		})
	}
}

func TestSettingsTakeoverHiddenComposerCannotReceiveInput(t *testing.T) {
	for _, width := range []int{40, 47, 160} {
		m := navigationModel()
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m.prompt.SetValue("Never submit this hidden draft")
		m.viewState().Draft = m.prompt.Value()
		m.activate(action{Kind: "app-settings"})
		m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
		if m.focus == "prompt" {
			t.Fatal("settings content cycle exposed hidden composer")
		}
		m.Update(tea.PasteMsg{Content: "unrelated paste"})
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if m.busy != nil || m.prompt.Value() != "Never submit this hidden draft" || m.viewState().Draft != m.prompt.Value() {
			t.Fatal("settings input reached hidden composer", width)
		}
	}
}
