package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestThreadSearchRoutesInputAndRestoresOnlyLocalView(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads[0].Title = "Build 界面"
	m.snapshot.Threads[1].Title = "Review"
	active := m.state.Active
	m.prompt.SetValue("keep prompt")
	clickControl(m, controlHit(t, m.measure(), "thread-search"))
	m.Update(tea.PasteMsg{Content: "Review"})
	if m.state.ThreadFilter != "Review" || m.state.Active != active || m.prompt.Value() != "keep prompt" || m.busy != nil {
		t.Fatal("search changed work")
	}
	f := m.measure()
	if hasControl(f, "thread:"+active) || !hasControl(f, "thread:"+m.snapshot.Threads[1].ID) {
		t.Fatal("search did not filter")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.focus != "navigation" || m.busy != nil {
		t.Fatal("search enter submitted work")
	}
	data, _ := json.Marshal(m.state)
	restored := New(nil, "restored", m.snapshot, data)
	if restored.threadSearch.Value() != "Review" {
		t.Fatal("filter did not restore")
	}
	if other := New(nil, "other", m.snapshot, nil); other.state.ThreadFilter != "" {
		t.Fatal("filter leaked")
	}
}

func TestProjectPickerSettingsHaveSeparateMouseAndKeyboardTargets(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := navigationModel()
		active := m.state.Active
		m.activate(action{Kind: "projects"})
		m.Update(tea.PasteMsg{Content: "beta"})
		f := m.measure()
		gear := controlHit(t, f, "project-settings:beta")
		row := controlHit(t, f, "menu:0")
		if row.Rect.X+row.Rect.W > gear.Rect.X {
			t.Fatal("gear overlaps project activation")
		}
		if mouse {
			clickControl(m, gear)
		} else {
			m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.settingsPage != "project" || m.settingsProjectID != "beta" || m.state.ProjectFilter != "" || m.state.Active != active || len(m.menu) > 0 {
			t.Fatal("gear filters/navigates work instead of settings")
		}
	}
}

func TestSettingsCommandsAreGlobalAndRevisionBound(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads = nil
	m.state.Active = ""
	m.activate(action{Kind: "app-settings"})
	m.activate(action{Kind: "settings-page", Value: "general"})
	m.activate(action{Kind: "app-workspace"})
	m.snapshot.AppSettings.Revision++ // A second client changes it while our menu is open.
	m.activate(action{Kind: "menu-select", Index: 1})
	if m.busy == nil || m.busy.Kind != "settings.update" || m.busy.ThreadID != "" || m.busy.AppSettings.WorkspaceDefault != "worktree" || m.busy.Revision != m.snapshot.AppSettings.Revision-1 {
		t.Fatal("settings lost global scope or expected revision")
	}
	if m.snapshot.AppSettings.WorkspaceDefault == "worktree" {
		t.Fatal("optimistic UI claims server accepted preference")
	}
}

func TestSettingsOldServerCannotPretendToAcceptChanges(t *testing.T) {
	m := navigationModel()
	m.snapshot.Capabilities = slices.DeleteFunc(m.snapshot.Capabilities, func(s string) bool {
		return s == "app-settings" || s == "project-settings" || s == "restart-continuation"
	})
	for _, a := range []action{{Kind: "restart-toggle"}, {Kind: "app-workspace"}, {Kind: "project-icon", ID: "alpha"}, {Kind: "project-remove", ID: "alpha"}} {
		m.activate(a)
		if m.busy != nil || len(m.menu) > 0 {
			t.Fatal("old server offered unsupported mutation", a)
		}
	}
	m.openSidebarSettings("general", "")
	if !strings.Contains(strings.Join(m.settingsText(), "\n"), "Update this server") {
		t.Fatal("missing unavailable guidance")
	}
}

func (m *Model) settingsText() []string {
	_, rows := m.sidebarSettingsRows(50)
	var text []string
	for _, r := range rows {
		text = append(text, r.label)
	}
	return text
}

func TestProjectRenameFailurePreservesNameAndRemovalIsExplicit(t *testing.T) {
	m := navigationModel()
	m.snapshot.Projects[0].Revision = 7
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].State = "idle"
		m.snapshot.Threads[i].Queue = nil
		m.snapshot.Threads[i].Requests = nil
		m.snapshot.Threads[i].Children = nil
	}
	m.activate(action{Kind: "project-settings", ID: "alpha"})
	m.activate(action{Kind: "project-name", ID: "alpha"})
	m.projectInput.SetValue("My renamed project")
	clickControl(m, controlHit(t, m.measure(), "menu:0"))
	if m.busy == nil || m.busy.ProjectSettings.Name != "My renamed project" || m.busy.Revision != 7 || m.projectMode != "rename" {
		t.Fatal("rename lost name/revision or hid editor")
	}
	c := *m.busy
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "stale_project", Message: "Project changed; retry"}})
	if m.projectInput.Value() != "My renamed project" || m.projectMode != "rename" || m.busy != nil {
		t.Fatal("rejection lost draft")
	}
	m.activate(action{Kind: "menu-close"})
	m.activate(action{Kind: "project-remove", ID: "alpha"})
	if m.busy != nil || m.menuIndex != 0 || m.menu[0].Label != "Cancel" || !strings.Contains(m.menu[1].Label, "1 threads") {
		t.Fatal("missing removal review")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil {
		t.Fatal("default Enter deleted project")
	}
	m.activate(action{Kind: "project-remove", ID: "alpha"})
	m.snapshot.Projects[0].Revision = 8
	clickControl(m, controlHit(t, m.measure(), "menu:1"))
	if m.busy == nil || m.busy.Kind != "project.remove" || m.busy.Revision != 7 {
		t.Fatal("confirmation silently rebound to new project revision")
	}
}

func TestProjectRemovalReconcilesOtherClientFilterAndSettings(t *testing.T) {
	m := navigationModel()
	m.state.ProjectFilter = "alpha"
	m.activate(action{Kind: "project-settings", ID: "alpha"})
	m.activate(action{Kind: "project-name", ID: "alpha"})
	next := m.snapshot
	next.Revision++
	next.Projects = append([]protocol.Project(nil), next.Projects[1:]...)
	next.Threads = append([]protocol.Thread(nil), next.Threads[1:]...)
	m.Update(snapshotMsg(next))
	if m.state.ProjectFilter != "" || m.settingsPage != "" || m.projectMode != "" || len(m.menu) > 0 || m.state.Active == "thread-shell" {
		t.Fatal("removed project retained stale local targets")
	}
}

func TestSettingsNarrowScrollAndBackRemainReachable(t *testing.T) {
	for _, width := range []int{40, 47, 160} {
		for _, scope := range []struct{ page, project string }{{"general", ""}, {"appearance", ""}, {"keybindings", ""}, {"about", ""}, {"project", "alpha"}, {"general", "alpha"}, {"keybindings", "alpha"}} {
			page := scope.page
			m := navigationModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: 22})
			previous := m.viewState().CompactColumn
			m.openSidebarSettings(page, scope.project)
			m.configureInputs()
			f := m.render()
			if hasControl(f, "thread-search") || hasControl(f, "prompt") || !hasControl(f, "settings-close") {
				t.Fatal("settings didn't replace workspace")
			}
			close := controlHit(t, f, "settings-close")
			for _, h := range f.hits {
				if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > width || h.Rect.Y+h.Rect.H > 21 {
					t.Fatal("control outside viewport", width, page, h)
				}
			}
			for _, row := range f.rows {
				if ansi.StringWidth(row) != width {
					t.Fatal("width overflow", width, page)
				}
			}
			m.setFocus("sidebar-settings")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
			f = m.render()
			if m.settingsScroll != f.settingsMax || controlHit(t, f, "settings-close").Rect != close.Rect {
				t.Fatal("settings scroll/close failed", width, page)
			}
			if width < 60 {
				m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
			}
			clickControl(m, controlHit(t, m.measure(), "settings-back"))
			if m.settingsPage != "" || m.viewState().CompactColumn != previous {
				t.Fatal("back failed to restore workspace")
			}
		}
	}
}

func TestSidebarSettingsCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set capture directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{47, 160} {
		for _, light := range []bool{false, true} {
			for _, page := range []string{"navigation", "picker", "general", "project", "project-general", "project-keybindings", "keybindings"} {
				m := navigationModel()
				m.state.Light = light
				m.state.ProjectFilter = "alpha"
				m.snapshot.Threads[1].Closed = true
				height := 30
				if width == 47 {
					height = 22
				}
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				if width == 47 {
					m.selectColumn(shell.LeftRegion)
				}
				if page == "picker" {
					m.activate(action{Kind: "projects"})
				} else if strings.HasPrefix(page, "project-") {
					m.openSidebarSettings(strings.TrimPrefix(page, "project-"), "alpha")
				} else if page == "project" {
					m.openSidebarSettings(page, "alpha")
				} else if page != "navigation" {
					m.openSidebarSettings(page, "")
				}
				m.configureInputs()
				path := filepath.Join(dir, fmt.Sprintf("%dx%d-light%t-sidebar-%s.ansi", width, height, light, page))
				if err := os.WriteFile(path, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestWorkspaceFailureRemainsVisibleOverControlHelp(t *testing.T) {
	m := navigationModel()
	m.state.ProjectFilter = "alpha"
	clickControl(m, controlHit(t, m.measure(), "thread-create"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.activate(action{Kind: "setting-model"})
	m.prompt.SetValue("first prompt")
	m.activate(action{Kind: "send"})
	if m.busy == nil {
		t.Fatal("missing first Send command")
	}
	c := *m.busy
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "unsupported_workspace", Message: "Worktree creation unavailable; choose Current checkout"}})
	last := ansi.Strip(m.render().rows[m.height-1])
	if !strings.Contains(last, "Worktree creation unavailable") || m.busy != nil {
		t.Fatal("failure hidden behind focus help", last)
	}
}

func TestRenameConflictRetryKeepsDraftAndLatestUnrelatedSettings(t *testing.T) {
	m := navigationModel()
	m.snapshot.Projects[0].Revision = 4
	m.activate(action{Kind: "project-name", ID: "alpha"})
	m.projectInput.SetValue("My name")
	m.activate(action{Kind: "project-rename-submit"})
	c := *m.busy
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "stale_project", Message: "stale"}})
	m.snapshot.Projects[0].Revision = 5
	m.snapshot.Projects[0].Color = "green"
	m.activate(action{Kind: "project-rename-submit"})
	if m.busy == nil || m.busy.Revision != 5 || m.busy.ProjectSettings.Name != "My name" || m.busy.ProjectSettings.Color != "green" {
		t.Fatal("explicit retry lost draft or overwrote another field")
	}
}

func TestSettingsAttentionAndCompactContentCycle(t *testing.T) {
	m := navigationModel()
	m.Update(tea.WindowSizeMsg{Width: 47, Height: 22})
	m.activate(action{Kind: "app-settings"})
	controlHit(t, m.measure(), "attention")
	m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if m.focus != "settings-category:general" {
		t.Fatal("F6 did not remain in visible categories")
	}
	m.activate(action{Kind: "attention"})
	if m.settingsPage != "" || m.menuTitle != "Attention" {
		t.Fatal("explicit attention did not leave settings to review work")
	}
}
