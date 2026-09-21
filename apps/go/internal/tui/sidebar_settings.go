package tui

import (
	"fmt"
	"runtime/debug"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func singleLine(s string) string { return strings.ReplaceAll(safe(s), "\n", " ") }

func (m *Model) updateThreadSearch(msg tea.Msg) tea.Cmd {
	cmd := updateInput(&m.threadSearch, msg)
	m.state.ThreadFilter = singleLine(m.threadSearch.Value())
	m.navScroll, m.closedScroll = 0, 0
	m.markDirty()
	return cmd
}

func (m *Model) projectByID(id string) (protocol.Project, bool) {
	for _, p := range m.snapshot.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return protocol.Project{}, false
}

func (m *Model) openSidebarSettings(page, projectID string) {
	if m.settingsPage == "" {
		m.settingsReturnFocus = m.focus
	}
	if page == "settings" {
		page = "general"
	}
	m.menu, m.projectMode = nil, ""
	m.projectInput.Blur()
	m.settingsPage, m.settingsProjectID, m.settingsScroll = page, projectID, 0
	if !slices.Contains(m.settingsCategories(), page) {
		m.settingsPage = "general"
	}
	m.settingsNavigation = false
	m.setFocus("sidebar-settings")
}

func (m *Model) closeSettings() {
	m.settingsPage, m.settingsProjectID = "", ""
	m.settingsNavigation = false
	m.menu, m.projectMode = nil, ""
	focus := m.settingsReturnFocus
	if focus == "" {
		focus = "prompt"
	}
	m.setFocus(focus)
}

func (m *Model) hasCapability(name string) bool {
	return slices.Contains(m.snapshot.Capabilities, name)
}

func (m *Model) settingsUnavailable(capability string) tea.Cmd {
	return m.showNotice("Update this server to use " + strings.ReplaceAll(capability, "-", " "))
}

func (m *Model) activateSidebarSettings(a action) (bool, tea.Cmd) {
	switch a.Kind {
	case "app-settings":
		m.openSidebarSettings("general", "")
	case "app-project-directory":
		if m.settingsProjectID != "" {
			return true, m.showNotice("Project starting folder is in app General settings")
		}
		if !m.hasCapability("path-completion") {
			return true, m.settingsUnavailable("path-completion")
		}
		m.openProjectDialog("project-root")
		return true, m.nextPathQuery()
	case "settings-page":
		if slices.Contains(m.settingsCategories(), a.Value) {
			m.openSidebarSettings(a.Value, m.settingsProjectID)
		}
	case "settings-nav":
		m.settingsNavigation = !m.settingsNavigation
		if m.settingsNavigation {
			m.setFocus("settings-category:" + m.settingsPage)
		} else {
			m.setFocus("sidebar-settings")
		}
	case "settings-back":
		m.closeSettings()
	case "project-settings":
		if _, ok := m.projectByID(a.ID); ok {
			m.openSidebarSettings("project", a.ID)
		}
	case "app-workspace":
		if m.settingsProjectID != "" {
			return true, m.showNotice("Workspace defaults for the app are in app settings")
		}
		if !m.hasCapability("app-settings") {
			return true, m.settingsUnavailable("app-settings")
		}
		rev := m.snapshot.AppSettings.Revision
		m.showMenu("Workspace default", []menuItem{
			{"Current checkout", action{Kind: "app-workspace-set", Value: "checkout", Revision: rev}},
			{"Worktree · creation unavailable in this build", action{Kind: "app-workspace-set", Value: "worktree", Revision: rev}},
		})
	case "app-workspace-set", "restart-toggle":
		if m.settingsProjectID != "" {
			return true, m.showNotice("App settings are available from the workspace settings gear")
		}
		if !m.hasCapability("app-settings") {
			return true, m.settingsUnavailable("app-settings")
		}
		next := m.snapshot.AppSettings
		if a.Kind == "restart-toggle" {
			if !m.hasCapability("restart-continuation") {
				return true, m.settingsUnavailable("restart-continuation")
			}
			next.ContinueAfterRestart = !next.ContinueAfterRestart
			a.Revision = next.Revision
		} else {
			next.WorkspaceDefault = a.Value
		}
		return true, m.command(protocol.Command{Kind: "settings.update", Revision: a.Revision, AppSettings: &next}, a)
	case "project-name":
		if !m.hasCapability("project-settings") {
			return true, m.settingsUnavailable("project-settings")
		}
		if p, ok := m.projectByID(a.ID); ok {
			m.openProjectDialog("rename")
			m.settingsProjectID = p.ID
			m.projectEditRevision = p.Revision
			m.projectRenameConflicted = false
			m.projectInput.SetValue(p.Name)
		}
	case "project-rename-submit":
		if m.projectRenameConflicted {
			if p, ok := m.projectByID(m.settingsProjectID); ok {
				m.projectEditRevision = p.Revision
			}
			m.projectRenameConflicted = false
		}
		return true, m.saveProjectSetting(action{ID: m.settingsProjectID, Value: strings.TrimSpace(m.projectInput.Value()), Revision: m.projectEditRevision}, "name")
	case "project-icon", "project-color", "project-workspace":
		if !m.hasCapability("project-settings") {
			return true, m.settingsUnavailable("project-settings")
		}
		if p, ok := m.projectByID(a.ID); ok {
			field := strings.TrimPrefix(a.Kind, "project-")
			values := []string{"", "folder", "code", "terminal", "git", "star", "rocket"}
			if field == "color" {
				values = []string{"", "purple", "blue", "green", "orange", "pink", "teal"}
			}
			if field == "workspace" {
				values = []string{"", "checkout", "worktree"}
			}
			var items []menuItem
			for _, value := range values {
				label := title(value)
				if value == "" {
					label = "Automatic"
				}
				if field == "icon" && value == "" {
					label = "Name initials"
				}
				if field == "workspace" {
					label = workspaceLabel(value)
					if value == "" {
						label = "Use app default"
					}
					if value == "worktree" {
						label += " · creation unavailable"
					}
				}
				if field == "icon" && value != "" {
					label = m.icon(value) + "  " + label
				}
				items = append(items, menuItem{label, action{Kind: "project-set-" + field, ID: p.ID, Value: value, Revision: p.Revision}})
			}
			if field == "icon" {
				color := title(p.Color)
				if color == "" {
					color = "Automatic"
				}
				items = append(items, menuItem{"Icon color: " + color + "…", action{Kind: "project-color", ID: p.ID}})
			}
			m.showMenu("Project "+field, items)
		}
	case "project-set-icon", "project-set-color", "project-set-workspace":
		return true, m.saveProjectSetting(a, strings.TrimPrefix(a.Kind, "project-set-"))
	case "project-remove":
		if !m.hasCapability("project-settings") {
			return true, m.settingsUnavailable("project-settings")
		}
		if p, ok := m.projectByID(a.ID); ok {
			if reason := protocol.ProjectRemoveBlocked(m.snapshot, p.ID); reason != "" {
				return true, m.showNotice("Cannot remove project: " + reason)
			}
			count := 0
			for _, t := range m.snapshot.Threads {
				if t.ProjectID == p.ID {
					count++
				}
			}
			// Cancel is deliberately first. Thread membership revisions bind this exact
			// confirmation to the project and count the user reviewed.
			m.showMenu("Remove project: "+p.Name, []menuItem{
				{"Cancel", action{Kind: "noop"}},
				{fmt.Sprintf("Delete project and %d threads permanently", count), action{Kind: "project-remove-confirm", ID: p.ID, Revision: p.Revision}},
				{"Files on disk will be kept", action{Kind: "noop"}},
			})
		}
	case "project-remove-confirm":
		return true, m.command(protocol.Command{Kind: "project.remove", ProjectID: a.ID, Revision: a.Revision}, a)
	case "theme":
		if m.settingsProjectID == "" {
			return false, nil
		}
		return true, m.showNotice("Appearance is available in app settings")
	default:
		return false, nil
	}
	return true, nil
}

func workspaceLabel(value string) string {
	if value == "worktree" {
		return "Worktree"
	}
	return "Current checkout"
}

func (m *Model) saveProjectSetting(a action, field string) tea.Cmd {
	p, ok := m.projectByID(a.ID)
	if !ok {
		return m.showNotice("Project was removed")
	}
	settings := protocol.ProjectSettings{Name: p.Name, Icon: p.Icon, Color: p.Color, WorkspaceDefault: p.WorkspaceDefault}
	switch field {
	case "name":
		if a.Value == "" {
			m.projectError = "Enter a project name"
			return nil
		}
		settings.Name = a.Value
	case "icon":
		settings.Icon = a.Value
	case "color":
		settings.Color = a.Value
	case "workspace":
		settings.WorkspaceDefault = a.Value
	}
	return m.command(protocol.Command{Kind: "project.update", ProjectID: p.ID, Revision: a.Revision, ProjectSettings: &settings}, a)
}

type settingsRow struct {
	label, key      string
	action          action
	heading, danger bool
}

func (m *Model) sidebarSettingsRows(width int) (string, []settingsRow) {
	var rows []settingsRow
	heading := func(text string) {
		for _, line := range strings.Split(ansi.Wrap(safe(text), width, ""), "\n") {
			rows = append(rows, settingsRow{label: line, heading: true})
		}
	}
	paragraph := func(text string) {
		for _, line := range strings.Split(ansi.Wrap(safe(text), width, ""), "\n") {
			rows = append(rows, settingsRow{label: line})
		}
	}
	button := func(label, key string, a action) {
		rows = append(rows, settingsRow{label: label, key: "sidebar-setting:" + key, action: a})
	}
	gap := func() { paragraph("") }
	name := "Settings"
	switch m.settingsPage {
	case "general":
		name = "General"
		heading("Workspace default")
		if m.settingsProjectID != "" {
			p, ok := m.projectByID(m.settingsProjectID)
			if !ok {
				paragraph("Project was removed")
				break
			}
			label := workspaceLabel(p.WorkspaceDefault)
			if p.WorkspaceDefault == "" {
				label = "Use app default: " + workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault)
			}
			button(label, "workspace", action{Kind: "project-workspace", ID: p.ID})
			if p.WorkspaceDefault != "" {
				paragraph("Project override. App default: " + workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault) + ".")
			}
			paragraph("Applies to new threads in this project.")
			if protocol.EffectiveWorkspaceDefault(m.snapshot.AppSettings, p) == "worktree" {
				paragraph("Worktree creation is unavailable in this build.")
			}
			if !m.hasCapability("project-settings") {
				gap()
				paragraph("Update this server to change project settings.")
			}
			break
		}
		button(workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault), "workspace", action{Kind: "app-workspace"})
		paragraph("Used for new threads unless a project overrides it.")
		if m.snapshot.AppSettings.WorkspaceDefault == "worktree" {
			paragraph("Worktree creation is unavailable in this build.")
		}
		gap()
		heading("Project starting folder")
		directory := m.snapshot.AppSettings.ProjectDirectory
		if directory == "" || directory == "~" {
			directory = "Home directory (~)"
		}
		button(directory, "project-directory", action{Kind: "app-project-directory"})
		paragraph("Start browsing for projects here on the connected server.")
		gap()
		heading("Continue threads after restart")
		value := "Off"
		if m.snapshot.AppSettings.ContinueAfterRestart {
			value = "On"
		}
		button(value, "restart", action{Kind: "restart-toggle"})
		paragraph("Resume eligible interrupted work after an update, crash or restart on this server.")
		paragraph("Demo execution only. Questions and approvals still need your answer.")
		if !m.hasCapability("restart-continuation") || !m.hasCapability("app-settings") {
			gap()
			paragraph("Update this server to change these settings.")
		}
	case "appearance":
		name = "Appearance"
		heading("Theme")
		value := "Dark"
		if m.state.Light {
			value = "Light"
		}
		button(value, "theme", action{Kind: "theme"})
		paragraph("Saved for this client. F8 switches themes.")
	case "keybindings":
		name = "Keybindings"
		if m.settingsProjectID != "" {
			paragraph("Uses app keybindings.")
			paragraph("Project keybinding overrides are unavailable in this build.")
			break
		}
		for _, pair := range [][2]string{{"Enter", "Send / activate"}, {"Shift+Enter / Ctrl+J", "New prompt line"}, {"Tab / Shift+Tab", "Next / previous control"}, {"F2", "Navigation / columns"}, {"F3 / F5", "Surfaces / terminal"}, {"F4", "Commands"}, {"F6", "Next content area"}, {"F7 / F8", "Maximize / theme"}, {"Esc", "Close menu / focus prompt"}, {"Ctrl+Q", "Detach; work continues"}} {
			heading(pair[0])
			paragraph(pair[1])
			gap()
		}
	case "about":
		name = "About"
		heading("tui-go")
		paragraph(buildVersion())
		gap()
		paragraph("Go reference app")
		paragraph("Demo activity; agent, Git and embedded terminal integrations are incomplete.")
		gap()
		heading("Server protocol")
		paragraph(fmt.Sprint(m.snapshot.Version))
	case "project":
		name = "Project settings"
		p, ok := m.projectByID(m.settingsProjectID)
		if !ok {
			paragraph("Project was removed")
			break
		}
		heading("Name")
		button(p.Name, "name", action{Kind: "project-name", ID: p.ID})
		heading("Icon")
		icon := title(p.Icon)
		if icon == "" {
			icon = "Name initials"
		}
		button(icon, "icon", action{Kind: "project-icon", ID: p.ID})
		gap()
		button("Remove project…", "remove", action{Kind: "project-remove", ID: p.ID})
		rows[len(rows)-1].danger = true
		paragraph("Deletes its threads after confirmation; keeps files on disk.")
		if !m.hasCapability("project-settings") {
			gap()
			paragraph("Update this server to edit projects.")
		}
	}
	return name, rows
}

// No release number is invented for local development binaries. Go stamps the
// module version or the actual checkout revision in its build info.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "Development build"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	revision, modified := "", false
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "Development build"
	}
	revision = revision[:min(10, len(revision))]
	if modified {
		revision += " · modified"
	}
	return "Development · " + revision
}

func (m *Model) reconcileProjects() {
	if m.state.ProjectFilter != "" {
		if _, ok := m.projectByID(m.state.ProjectFilter); !ok {
			m.state.ProjectFilter = ""
			m.markDirty()
		}
	}
	if m.settingsProjectID != "" {
		if _, ok := m.projectByID(m.settingsProjectID); !ok {
			m.closeSettings()
			if m.projectMode == "rename" {
				m.projectMode, m.menu = "", nil
				m.projectInput.Blur()
			}
		}
	}
	if m.projectMode == "filter" || m.projectMode == "new-thread" {
		m.refreshProjectMenu()
	}
	if len(m.menu) > 0 {
		for _, item := range m.menu {
			if item.Action.Kind == "project-remove-confirm" {
				if _, ok := m.projectByID(item.Action.ID); !ok {
					m.menu = nil
					break
				}
			}
		}
	}
}
