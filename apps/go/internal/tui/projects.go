package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) openProjectDialog(mode string) {
	m.showMenu("Projects", nil)
	m.projectMode = mode
	m.projectError = ""
	m.projectInput.SetValue("")
	m.projectInput.Placeholder = "Search projects…"
	if mode == "add" {
		m.menuTitle = "Add project"
		m.projectInput.Placeholder = "Type a folder name or path…"
	}
	if mode == "new-thread" {
		m.menuTitle = "New thread in project"
	}
	if mode == "project-root" {
		m.menuTitle = "Project starting folder"
		m.projectInput.Placeholder = "Type a folder name or path…"
		value := m.snapshot.AppSettings.ProjectDirectory
		if value == "" || value == "~" {
			value = "~/"
		}
		if !strings.HasSuffix(value, "/") {
			value += "/"
		}
		m.projectInput.SetValue(value)
		m.projectDirectoryRevision = m.snapshot.AppSettings.Revision
		m.projectDirectoryConflicted = false
	}
	if mode == "rename" {
		m.menuTitle = "Project name"
		m.projectInput.Placeholder = "Display name…"
	}
	if mode == "worktree-branch" {
		m.menuTitle = "New worktree branch"
		m.projectInput.Placeholder = "New branch name…"
	}
	m.projectInput.SetWidth(max(1, min(68, max(20, m.width-6))-4))
	m.projectInput.SetHeight(1)
	m.setFocus("project-input")
	m.projectInput.Focus()
	m.refreshProjectMenu()
}

func (m *Model) refreshProjectMenu() {
	if m.projectMode == "rename" {
		m.menu = []menuItem{{Label: "Save name", Action: action{Kind: "project-rename-submit"}}, {Label: "Cancel", Action: action{Kind: "project-cancel"}}}
		return
	}
	if m.projectMode == "worktree-branch" {
		m.menu = []menuItem{{Label: "Use branch name", Action: action{Kind: "worktree-branch-submit"}}, {Label: "Cancel", Action: action{Kind: "project-cancel"}}}
		return
	}
	if m.projectMode == "add" || m.projectMode == "project-root" {
		m.menu = m.folderMenu()
		m.menuIndex = min(max(0, m.menuIndex), len(m.menu)-1)
		return
	}
	query := strings.ToLower(strings.TrimSpace(m.projectInput.Value()))
	m.menu = nil
	if m.projectMode != "new-thread" && (query == "" || strings.Contains("all projects", query)) {
		m.menu = append(m.menu, menuItem{Label: "All projects", Action: action{Kind: "project-filter"}})
	}
	for _, p := range m.snapshot.Projects {
		if query == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Path), query) {
			a := action{Kind: "project-filter", ID: p.ID}
			if m.projectMode == "new-thread" {
				a = action{Kind: "thread-create", Value: p.ID}
			}
			m.menu = append(m.menu, menuItem{Label: p.Name + " · " + p.Path, Action: a})
		}
	}
	if len(m.menu) == 0 {
		m.menu = append(m.menu, menuItem{Label: "No matching projects", Action: action{Kind: "project-no-match"}})
	}
	kind := "project-add"
	if m.projectMode == "new-thread" {
		kind = "project-add-thread"
	}
	m.menu = append(m.menu, menuItem{Label: m.icon("project-add") + " Add project…", Action: action{Kind: kind}})
	m.menuIndex = min(max(0, m.menuIndex), len(m.menu)-1)
	m.menuOffset = 0
}

func (m *Model) projectKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.projectMode = ""
		m.menu = nil
		m.projectInput.Blur()
		return m.setFocus("prompt")
	case "tab", "shift+tab":
		if k.String() == "tab" && (m.projectMode == "add" || m.projectMode == "project-root") && m.menu[m.menuIndex].Action.Kind == "path-directory" {
			return m.activate(m.menu[m.menuIndex].Action)
		}
		if m.projectMode == "filter" {
			step := 1
			if k.String() == "shift+tab" {
				step = -1
			}
			if step > 0 && !m.projectGear && m.menu[m.menuIndex].Action.Kind == "project-filter" && m.menu[m.menuIndex].Action.ID != "" {
				m.projectGear = true
			} else if step < 0 && m.projectGear {
				m.projectGear = false
			} else {
				m.menuIndex = m.menuStep(m.menuIndex, step, true)
				item := m.menu[m.menuIndex]
				m.projectGear = step < 0 && item.Action.Kind == "project-filter" && item.Action.ID != ""
			}
			return nil
		}
		if k.String() == "tab" {
			m.menuIndex = m.menuStep(m.menuIndex, 1, true)
		} else {
			m.menuIndex = m.menuStep(m.menuIndex, -1, true)
		}
		return nil
	case "up":
		m.projectGear = false
		m.menuIndex = m.menuStep(m.menuIndex, -1, true)
		return nil
	case "down":
		m.projectGear = false
		m.menuIndex = m.menuStep(m.menuIndex, 1, true)
		return nil
	case "enter":
		if m.projectGear && m.projectMode == "filter" {
			return m.activate(action{Kind: "project-settings", ID: m.menu[m.menuIndex].Action.ID})
		}
		return m.activate(action{Kind: "menu-select", Index: m.menuIndex})
	case "shift+enter", "ctrl+j":
		return nil
	}
	m.projectGear = false
	cmd := updateInput(&m.projectInput, k)
	m.projectError = ""
	m.menuIndex = 0
	m.refreshProjectMenu()
	return cmd
}

func (m *Model) acceptThreadOperation(msg commandMsg) bool {
	if msg.receipt.State == "deleted" {
		m.status = "This thread was already deleted"
		m.markDirty()
		return true
	}
	switch msg.command.Kind {
	case "settings.update":
		if m.projectMode == "project-root" && msg.local.Kind == "path-project-root-save" && m.projectInput.Value() == msg.local.Value {
			m.projectMode, m.menu = "", nil
			m.projectInput.Blur()
			m.setFocus("sidebar-settings")
		} else if m.projectMode == "project-root" {
			m.projectDirectoryRevision = msg.command.Revision + 1
		}
		m.status = "App settings saved"
	case "project.update":
		if m.projectMode == "rename" && m.projectInput.Value() == msg.command.ProjectSettings.Name {
			m.projectMode, m.menu = "", nil
			m.projectInput.Blur()
			m.setFocus("sidebar-settings")
		} else if m.projectMode == "rename" {
			m.projectEditRevision = msg.command.Revision + 1
		}
		m.status = "Project settings saved"
	case "project.remove":
		if m.state.ProjectFilter == msg.command.ProjectID {
			m.state.ProjectFilter = ""
		}
		if m.settingsProjectID == msg.command.ProjectID {
			m.settingsPage, m.settingsProjectID = "", ""
		}
		m.reconcileThreadMembership()
		m.status = "Project and its threads removed · files kept"
	case "project.add":
		if m.projectMode == "add" {
			m.pendingProjectDraft = msg.local.Value == "new-thread"
			m.projectMode = ""
			m.menu = nil
			m.projectInput.Blur()
		}
		m.pendingProjectSelection = msg.receipt.TargetID
		m.reconcileThreadMembership()
		m.status = "Project added"
	case "thread.create":
		m.pendingThreadSelection = msg.receipt.TargetID
		m.state.ProjectFilter = msg.command.ProjectID
		m.reconcileThreadMembership()
		m.status = "Thread created · Demo"
	case "thread.start":
		m.state.StartedDraft = &startedDraft{ThreadID: msg.receipt.TargetID, Command: msg.command, Revision: msg.receipt.Revision}
		m.reconcileThreadMembership()
		m.status = "Initial prompt accepted"
	case "thread.close":
		if m.state.Active == msg.command.ThreadID {
			m.selectThread(m.nextOpenThread(msg.command.ThreadID))
		}
		m.status = "Thread moved to Closed"
	case "thread.reopen":
		m.pendingThreadSelection = msg.command.ThreadID
		m.reconcileThreadMembership()
		m.status = "Thread reopened"
	case "thread.delete":
		m.status = "Thread deleted permanently"
	default:
		return false
	}
	m.markDirty()
	m.configureInputs()
	return true
}

func (m *Model) threadMenu(id string) {
	t, ok := m.threadByID(id)
	if !ok {
		return
	}
	verb, kind := "Close", "thread-close"
	if t.Closed {
		verb, kind = "Reopen", "thread-reopen"
	}
	m.showMenuFor("", t.Title, []menuItem{{Label: verb, Action: action{Kind: kind, ID: id}}, {Label: "Delete permanently…", Action: action{Kind: "thread-delete", ID: id}}})
}

func (m *Model) confirmThreadDelete(id string) {
	t, ok := m.threadByID(id)
	if !ok {
		return
	}
	m.showMenuFor("Delete thread · ", t.Title, []menuItem{
		{Label: "Cancel", Action: action{Kind: "thread-delete-cancel"}},
		{Label: "Delete permanently · history, drafts and owned work", Action: action{Kind: "thread-delete-confirm", ID: id, Index: int(t.LifecycleRevision)}},
	})
	m.status = fmt.Sprintf("Delete %q? This cannot be undone.", t.Title)
}
