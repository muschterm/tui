package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestProjectSettingsScopeIsIndependentOfThreadAndFilter(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := navigationModel()
		m.state.ProjectFilter = "alpha"
		before := settingsPreservedState(t, m)
		m.activate(action{Kind: "project-settings", ID: "beta"})
		if !slices.Equal(m.settingsCategories(), []string{"project", "general", "keybindings"}) {
			t.Fatal("project scope exposes app categories", m.settingsCategories())
		}
		f := m.measure()
		for _, key := range []string{"sidebar-setting:workspace", "sidebar-setting:color", "sidebar-setting:restart", "settings-category:appearance", "settings-category:about"} {
			if hasControl(f, key) {
				t.Fatal("Project category contains unrelated setting", key)
			}
		}
		for _, key := range []string{"name", "icon", "remove"} {
			controlHit(t, f, "sidebar-setting:"+key)
		}
		general := settingsCategoryHit(t, f, "general")
		if mouse {
			clickControl(m, general)
		} else {
			m.setFocus(general.Key)
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.settingsPage != "general" || !strings.Contains(ansi.Strip(m.render().rows[0]), "Settings / General / Beta") {
			t.Fatal("missing named project scope", m.settingsBreadcrumb())
		}
		workspace := controlHit(t, m.measure(), "sidebar-setting:workspace")
		if workspace.Action.Kind != "project-workspace" || workspace.Action.ID != "beta" || hasControl(m.measure(), "sidebar-setting:restart") {
			t.Fatal("project General controls target app or wrong project")
		}
		light := m.state.Light
		m.Update(tea.KeyPressMsg{Code: tea.KeyF8})
		m.activate(action{Kind: "settings-page", Value: "appearance"})
		m.activate(action{Kind: "restart-toggle"})
		m.activate(action{Kind: "app-workspace"})
		m.activate(action{Kind: "app-workspace-set", Value: "worktree"})
		if m.settingsPage != "general" || m.state.Light != light || m.busy != nil || len(m.menu) > 0 {
			t.Fatal("project scope allowed global configuration")
		}
		m.activate(action{Kind: "settings-page", Value: "keybindings"})
		text := strings.Join(m.settingsText(), "\n")
		if !strings.Contains(text, "Uses app keybindings") || !strings.Contains(text, "overrides are unavailable") || strings.Contains(text, "F7 / F8") {
			t.Fatal("project bindings imply available remapping", text)
		}
		m.activate(action{Kind: "settings-back"})
		if got := settingsPreservedState(t, m); got != before {
			t.Fatal("project settings changed workspace state")
		}
		m.activate(action{Kind: "app-settings"})
		if m.settingsBreadcrumb() != "Settings / General" || !slices.Equal(m.settingsCategories(), []string{"general", "appearance", "keybindings", "about"}) {
			t.Fatal("app settings retained project context")
		}
		controlHit(t, m.measure(), "sidebar-setting:restart")
		m.activate(action{Kind: "settings-page", Value: "appearance"})
		clickControl(m, controlHit(t, m.measure(), "sidebar-setting:theme"))
		if m.state.Light == light {
			t.Fatal("app appearance stopped working")
		}
	}
}

func TestProjectGeneralInheritsAndSavesOnlyRevisionBoundOverride(t *testing.T) {
	for _, value := range []string{"", "checkout", "worktree"} {
		for _, mouse := range []bool{false, true} {
			m := navigationModel()
			p := &m.snapshot.Projects[1]
			p.Revision, p.Icon, p.Color = 9, "rocket", "teal"
			m.openSidebarSettings("general", p.ID)
			if label := controlHit(t, m.measure(), "sidebar-setting:workspace").Label; label != "Use app default: Current checkout" {
				t.Fatal("missing inherited effective value", label)
			}
			m.snapshot.AppSettings.WorkspaceDefault = "worktree"
			if label := controlHit(t, m.measure(), "sidebar-setting:workspace").Label; label != "Use app default: Worktree" {
				t.Fatal("inherited effective value failed to follow app update", label)
			}
			p.WorkspaceDefault = "checkout"
			workspace := controlHit(t, m.measure(), "sidebar-setting:workspace")
			if workspace.Label != "Current checkout" || !strings.Contains(strings.Join(m.settingsText(), "\n"), "Project override. App default: Worktree.") {
				t.Fatal("explicit override does not distinguish app default")
			}
			if mouse {
				clickControl(m, workspace)
			} else {
				m.setFocus(workspace.Key)
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			index := slices.IndexFunc(m.menu, func(item menuItem) bool { return item.Action.Value == value })
			if index < 0 || m.menu[0].Label != "Use app default" {
				t.Fatal("override menu lacks explicit inheritance reset")
			}
			p.Revision++ // Concurrent project edit must not rebind the open menu.
			if mouse {
				clickControl(m, controlHit(t, m.measure(), "menu:"+strconv.Itoa(index)))
			} else {
				m.menuIndex = index
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			c := m.busy
			if c == nil || c.Kind != "project.update" || c.ProjectID != p.ID || c.Revision != 9 || c.AppSettings != nil || c.ThreadID != "" {
				t.Fatal("project General lost scope or revision", c)
			}
			want := protocol.ProjectSettings{Name: p.Name, Icon: "rocket", Color: "teal", WorkspaceDefault: value}
			if *c.ProjectSettings != want || p.WorkspaceDefault != "checkout" || m.snapshot.AppSettings.WorkspaceDefault != "worktree" {
				t.Fatal("override changed unrelated fields or optimistically applied")
			}
		}
	}
}

func TestProjectScopeBreadcrumbUnicodeAndCompactNavigation(t *testing.T) {
	for _, width := range []int{40, 47, 160} {
		m := navigationModel()
		m.snapshot.Projects[1].Name = "界面 e\u0301 " + strings.Repeat("long name ", 12)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 22})
		m.activate(action{Kind: "project-settings", ID: "beta"})
		if width < 60 {
			m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
		}
		clickControl(m, settingsCategoryHit(t, m.measure(), "general"))
		if !strings.HasSuffix(m.settingsBreadcrumb(), m.snapshot.Projects[1].Name) || m.settingsProjectID != "beta" {
			t.Fatal("compact category switch lost project identity")
		}
		f := m.render()
		if width < 60 && !strings.Contains(ansi.Strip(f.rows[2]), "General / 界面") {
			t.Fatal("compact form lost visible project identity")
		}
		for _, line := range f.rows {
			if ansi.StringWidth(line) != width {
				t.Fatal("breadcrumb overflowed viewport", width)
			}
		}
		controlHit(t, m.measure(), "sidebar-setting:workspace")
		controlHit(t, m.measure(), "settings-close")
	}
}

func TestProjectIconContainsColorCustomization(t *testing.T) {
	m := navigationModel()
	m.activate(action{Kind: "project-settings", ID: "beta"})
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:icon"))
	item := m.menu[len(m.menu)-1]
	if !strings.HasPrefix(item.Label, "Icon color:") || item.Action.Kind != "project-color" || item.Action.ID != "beta" {
		t.Fatal("icon color customization lost")
	}
	m.activate(action{Kind: "menu-select", Index: len(m.menu) - 1})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.ProjectID != "beta" || m.busy.ProjectSettings.Color != "purple" {
		t.Fatal("nested icon color could not be saved")
	}
}
