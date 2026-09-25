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

// settingsFieldValueRoom mirrors the value width a settingsField row leaves
// after its inset and label, matching renderSettingsField/panelPairRowStyled's
// own room formula so a value pre-truncated to this width is never truncated
// again from the wrong side at paint time.
func settingsFieldValueRoom(width int, label string) int {
	inner := max(1, width-2)
	return max(1, inner/2, inner-ansi.StringWidth(label)-2)
}

// truncatePathLeft shortens a long filesystem path from the left, keeping the
// tail (the most identifying part of a path) visible, unlike the pair
// construct's default right-truncation used for ordinary values.
func truncatePathLeft(value string, room int) string {
	value = singleLine(value)
	if room <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= room {
		return value
	}
	if room == 1 {
		return "…"
	}
	total := len([]rune(value))
	for n := 1; n <= total; n++ {
		out := ansi.TruncateLeft(value, n, "…")
		if ansi.StringWidth(out) <= room {
			return out
		}
	}
	return "…"
}

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
	return m.showNoticeAs(noticeUnavailable, "Update this server to use "+strings.ReplaceAll(capability, "-", " "))
}

func (m *Model) activateSidebarSettings(a action) (bool, tea.Cmd) {
	switch a.Kind {
	case "app-settings":
		m.openSidebarSettings("general", "")
	case "app-thread-agent", "app-thread-field":
		return true, m.openNewThreadDefaultMenu(a)
	case "app-thread-agent-set", "app-thread-field-set", "app-thread-reset":
		return true, m.saveNewThreadDefault(a)
	case "app-thread-agent-probe":
		if !m.hasCapability("agent-probe") {
			return true, m.settingsUnavailable("agent-probe")
		}
		return true, m.command(protocol.Command{Kind: "agent.probe", TargetID: a.ID}, a)
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
			{Label: "Current checkout", Action: action{Kind: "app-workspace-set", Value: "checkout", Revision: rev}},
			{Label: "Worktree · creation unavailable in this build", Action: action{Kind: "app-workspace-set", Value: "worktree", Revision: rev}},
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
				items = append(items, menuItem{Label: label, Action: action{Kind: "project-set-" + field, ID: p.ID, Value: value, Revision: p.Revision}})
			}
			if field == "icon" {
				color := title(p.Color)
				if color == "" {
					color = "Automatic"
				}
				items = append(items, menuItem{Label: "Icon color: " + color + "…", Action: action{Kind: "project-color", ID: p.ID}})
			}
			m.showMenu("Project "+field, items)
		}
	case "project-set-icon", "project-set-color", "project-set-workspace":
		if !m.hasCapability("project-settings") {
			return true, m.settingsUnavailable("project-settings")
		}
		return true, m.saveProjectSetting(a, strings.TrimPrefix(a.Kind, "project-set-"))
	case "project-remove":
		if !m.hasCapability("project-settings") {
			return true, m.settingsUnavailable("project-settings")
		}
		if p, ok := m.projectByID(a.ID); ok {
			if reason := protocol.ProjectRemoveBlocked(m.snapshot, p.ID); reason != "" {
				return true, m.showNoticeAs(noticeError, "Cannot remove project: "+reason)
			}
			count, jobs := 0, 0
			for _, t := range m.snapshot.Threads {
				if t.ProjectID == p.ID {
					count++
					if jobThread(t) {
						jobs++
					}
				}
			}
			label := fmt.Sprintf("Delete project and %d threads permanently", count)
			if jobs > 0 {
				label += fmt.Sprintf(" (including %d resolution job threads)", jobs)
			}
			// Cancel is deliberately first. Thread membership revisions bind this exact
			// confirmation to the project and count the user reviewed.
			m.showMenuFor("Remove project · ", p.Name, []menuItem{
				{Label: "Cancel", Action: action{Kind: "noop"}},
				{Label: label, Action: action{Kind: "project-remove-confirm", ID: p.ID, Revision: p.Revision}},
				{Note: "Files on disk will be kept"},
			})
			m.docDeleteNote(func(key string) bool {
				if strings.HasPrefix(key, "project:"+p.ID+":") {
					return true
				}
				for _, t := range m.snapshot.Threads {
					if t.ProjectID == p.ID && strings.HasPrefix(key, "thread:"+t.ID+":") {
						return true
					}
				}
				return false
			})
		}
	case "project-remove-confirm":
		return true, m.command(protocol.Command{Kind: "project.remove", ProjectID: a.ID, Revision: a.Revision}, a)
	case "theme", "icons":
		if m.settingsProjectID == "" {
			return false, nil
		}
		return true, m.showNotice("Appearance is available in app settings")
	case "theme-set", "icons-set":
		// Segments set a value; the existing toggles apply it only on change.
		current := m.iconsSetting()
		toggle := action{Kind: "icons"}
		if a.Kind == "theme-set" {
			current, toggle = "dark", action{Kind: "theme"}
			if m.state.Light {
				current = "light"
			}
		}
		if a.Value == current {
			return true, nil
		}
		return true, m.activate(toggle)
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
		return m.showNoticeAs(noticeUnavailable, "Project was removed")
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

type settingsRowKind uint8

const (
	settingsText settingsRowKind = iota
	settingsHeading
	settingsRule
	settingsButton
	settingsSegments
	settingsToggle
	settingsPair
	settingsField
)

// A segment is one choice of a small closed option set. Each has its own key,
// hit rectangle and Tab stop; the selected one's action is a no-op so that
// activating the current value writes nothing.
type settingsSegment struct {
	label, key string
	action     action
	selected   bool
}

// settingsRow is one body row. A segmented band spans three rows (band -1, 0
// and 1: the upper half-block edge, the labels and the lower edge) when the
// palette can paint distinct neutral fills; otherwise it is one bracketed row.
type settingsRow struct {
	kind       settingsRowKind
	label, key string
	value      string
	action     action
	danger, on bool
	fixed      bool // A field whose value the agent offers no choice for.
	segments   []settingsSegment
	band       int
	bandRows   int
}

func (m *Model) sidebarSettingsRows(width int) (string, []settingsRow) {
	var rows []settingsRow
	wrap := func(kind settingsRowKind, text string) {
		for _, line := range strings.Split(ansi.Wrap(safe(text), width, ""), "\n") {
			rows = append(rows, settingsRow{kind: kind, label: line})
		}
	}
	paragraph := func(text string) { wrap(settingsText, text) }
	gap := func() { paragraph("") }
	rule := func() {
		if len(rows) > 0 {
			gap()
			rows = append(rows, settingsRow{kind: settingsRule})
			gap()
		}
	}
	section := func(text string) {
		rule()
		wrap(settingsHeading, text)
	}
	// band repeats a control row as its upper edge, label row and lower edge
	// when the palette supports banded fills.
	band := func(row settingsRow) {
		row.bandRows = 1
		if !panelBandsSupported(m) {
			rows = append(rows, row)
			return
		}
		row.bandRows = 3
		for part := -1; part <= 1; part++ {
			row.band = part
			rows = append(rows, row)
		}
	}
	button := func(label, key string, a action) {
		band(settingsRow{kind: settingsButton, label: label, key: "sidebar-setting:" + key, action: a})
	}
	// field is an actionable label/value pair: one dense row whose whole width
	// opens the value's menu. The pair is explicit, never split from text.
	field := func(label, value, key string, a action, fixed bool) {
		rows = append(rows, settingsRow{kind: settingsField, label: label, value: value, key: "sidebar-setting:" + key, action: a, fixed: fixed})
	}
	pair := func(label, value string) {
		rows = append(rows, settingsRow{kind: settingsPair, label: label, value: value})
	}
	// segments falls back to the single menu button when a label cannot fit.
	segments := func(key string, choices []settingsSegment, fallback func()) {
		labels := make([]string, len(choices))
		for i, c := range choices {
			labels[i] = c.label
		}
		if !panelSegmentsFit(width, labels) {
			fallback()
			return
		}
		for i := range choices {
			choices[i].key = "sidebar-setting:" + key + ":" + choices[i].key
			if choices[i].selected {
				choices[i].action = action{Kind: "noop"}
			}
		}
		band(settingsRow{kind: settingsSegments, label: strings.Join(labels, " | "), segments: choices})
	}
	toggle := func(label, key string, on bool, a action) {
		value := "Off"
		if on {
			value = "On"
		}
		rows = append(rows, settingsRow{kind: settingsToggle, label: label, value: value, on: on, key: "sidebar-setting:" + key, action: a})
	}
	name := "Settings"
	switch m.settingsPage {
	case "general":
		name = "General"
		section("Workspace default")
		if m.settingsProjectID != "" {
			p, ok := m.projectByID(m.settingsProjectID)
			if !ok {
				paragraph("Project was removed")
				break
			}
			var choices []settingsSegment
			for _, value := range []string{"", "checkout", "worktree"} {
				label, key := workspaceLabel(value), value
				if value == "" {
					label, key = "App default", "default"
				}
				choices = append(choices, settingsSegment{label: label, key: key, selected: p.WorkspaceDefault == value,
					action: action{Kind: "project-set-workspace", ID: p.ID, Value: value, Revision: p.Revision}})
			}
			segments("workspace", choices, func() {
				label := workspaceLabel(p.WorkspaceDefault)
				if p.WorkspaceDefault == "" {
					label = "Use app default: " + workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault)
				}
				button(label, "workspace", action{Kind: "project-workspace", ID: p.ID})
			})
			if p.WorkspaceDefault != "" {
				paragraph("Project override. App default: " + workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault) + ".")
			} else {
				paragraph("Using app default: " + workspaceLabel(m.snapshot.AppSettings.WorkspaceDefault) + ".")
			}
			paragraph("Applies to new threads in this project.")
			paragraph("Worktree creation is unavailable in this build.")
			if !m.hasCapability("project-settings") {
				gap()
				paragraph("Update this server to change project settings.")
			}
			break
		}
		rev, current := m.snapshot.AppSettings.Revision, m.snapshot.AppSettings.WorkspaceDefault
		var choices []settingsSegment
		for _, value := range []string{"checkout", "worktree"} {
			choices = append(choices, settingsSegment{label: workspaceLabel(value), key: value, selected: workspaceLabel(current) == workspaceLabel(value),
				action: action{Kind: "app-workspace-set", Value: value, Revision: rev}})
		}
		segments("workspace", choices, func() {
			button(workspaceLabel(current), "workspace", action{Kind: "app-workspace"})
		})
		paragraph("Used for new threads unless a project overrides it.")
		paragraph("Worktree creation is unavailable in this build.")
		section("Project starting folder")
		const directoryLabel = "Project starting folder"
		directory := m.snapshot.AppSettings.ProjectDirectory
		if directory == "" || directory == "~" {
			directory = "Home directory (~)"
		} else {
			directory = truncatePathLeft(directory, settingsFieldValueRoom(width, directoryLabel))
		}
		field(directoryLabel, directory, "project-directory", action{Kind: "app-project-directory"}, false)
		paragraph("Start browsing for projects here on the connected server.")
		rule()
		toggle("Continue threads after restart", "restart", m.snapshot.AppSettings.ContinueAfterRestart, action{Kind: "restart-toggle"})
		gap()
		paragraph("Resume eligible interrupted work after an update, crash or restart on this server.")
		paragraph("Demo execution only. Questions and approvals still need your answer.")
		if !m.hasCapability("restart-continuation") || !m.hasCapability("app-settings") {
			gap()
			paragraph("Update this server to change these settings.")
		}
	case "agents":
		name = "Agents"
		section("New thread defaults")
		// Fields are dense one-row pairs rather than banded buttons: six banded
		// fields took 18 rows and pushed the reset button below a 30-row screen.
		field("Agent", m.newThreadDefaultAgentValue(), "agent", action{Kind: "app-thread-agent"}, false)
		gap()
		paragraph("Used when a new project draft is first opened. Existing drafts and threads keep their selections.")
		if saved := m.snapshot.AppSettings.NewThreadDefaults; saved != nil && saved.AgentID != "" {
			if a, ok := m.agentByID(saved.AgentID); ok {
				if a.Kind != "fixture" && a.State != "ready" {
					paragraph("Saved agent is " + agentReadiness(a) + "; refresh its options before using this default.")
				}
				section("Agent settings")
				for _, name := range settingFieldOrder {
					field(title(name), m.newThreadDefaultFieldValue(a, name, saved.Settings), name, action{Kind: "app-thread-field", ID: name}, !newThreadDefaultFieldSelectable(a, name, saved.Settings))
				}
			} else {
				paragraph("Saved agent is no longer configured; choose another agent.")
			}
		}
		rule()
		button("Use built-in defaults", "reset", action{Kind: "app-thread-reset"})
		if !m.hasCapability("new-thread-defaults") {
			paragraph("Update this server to change new thread defaults.")
		}
	case "appearance":
		name = "Appearance"
		section("Theme")
		segments("theme", []settingsSegment{
			{label: "Dark", key: "dark", selected: !m.state.Light, action: action{Kind: "theme-set", Value: "dark"}},
			{label: "Light", key: "light", selected: m.state.Light, action: action{Kind: "theme-set", Value: "light"}},
		}, func() {
			value := "Dark"
			if m.state.Light {
				value = "Light"
			}
			button(value, "theme", action{Kind: "theme"})
		})
		paragraph("Saved for this client. F8 switches themes.")
		section("Symbols")
		icons := m.iconsSetting()
		segments("icons", []settingsSegment{
			{label: iconsLabel("nerd"), key: "nerd", selected: icons == "nerd", action: action{Kind: "icons-set", Value: "nerd"}},
			{label: iconsLabel("ascii"), key: "ascii", selected: icons == "ascii", action: action{Kind: "icons-set", Value: "ascii"}},
		}, func() {
			button(iconsLabel(icons), "icons", action{Kind: "icons"})
		})
		paragraph("Nerd Font uses patched-font control glyphs. Choose ASCII when they render as boxes or misaligned cells.")
		if m.state.Icons == "" {
			paragraph("Following the TUI_GO_ICONS environment default until a choice is saved here.")
		} else {
			paragraph("Saved for this client; TUI_GO_ICONS no longer applies.")
		}
	case "keybindings":
		name = "Keybindings"
		if m.settingsProjectID != "" {
			paragraph("Uses app keybindings.")
			paragraph("Project keybinding overrides are unavailable in this build.")
			break
		}
		for _, group := range []struct {
			title string
			pairs [][2]string
		}{
			{"Composer", [][2]string{{"Send / activate", "Enter"}, {"New prompt line", "Shift+Enter / Ctrl+J"}}},
			{"Navigation", [][2]string{{"Next / previous control", "Tab / Shift+Tab"}, {"Navigation / columns", "F2"}, {"Surfaces / terminal", "F3 / F5"}, {"Next content area", "F6"}}},
			{"Window", [][2]string{{"Commands", "F4"}, {"Maximize / theme", "F7 / F8"}, {"Close menu / focus prompt", "Esc"}, {"Detach; work continues", "Ctrl+Q"}}},
		} {
			section(group.title)
			for _, p := range group.pairs {
				pair(p[0], p[1])
			}
		}
	case "about":
		name = "About"
		section("Application")
		pair("Name", "tui-go")
		pair("Version", buildVersion())
		pair("Implementation", "Go reference app")
		gap()
		paragraph("Demo activity; agent, Git and embedded terminal integrations are incomplete.")
		section("Server")
		pair("Protocol", fmt.Sprint(m.snapshot.Version))
	case "project":
		name = "Project settings"
		p, ok := m.projectByID(m.settingsProjectID)
		if !ok {
			paragraph("Project was removed")
			break
		}
		section("Name")
		button(p.Name, "name", action{Kind: "project-name", ID: p.ID})
		section("Icon")
		icon := title(p.Icon)
		if icon == "" {
			icon = "Name initials"
		}
		field("Icon", icon, "icon", action{Kind: "project-icon", ID: p.ID}, false)
		section("Remove")
		button("Remove project…", "remove", action{Kind: "project-remove", ID: p.ID})
		for i := len(rows) - 1; i >= 0 && rows[i].key == "sidebar-setting:remove"; i-- {
			rows[i].danger = true
		}
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
