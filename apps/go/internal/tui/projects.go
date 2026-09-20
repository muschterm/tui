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
		m.projectInput.Placeholder = "Existing folder path on the server…"
	}
	m.projectInput.SetWidth(max(1, min(68, max(20, m.width-6))-4))
	m.projectInput.SetHeight(1)
	m.setFocus("project-input")
	m.projectInput.Focus()
	m.refreshProjectMenu()
}

func (m *Model) refreshProjectMenu() {
	if m.projectMode == "add" {
		m.menu = []menuItem{{"Add folder as project", action{Kind: "project-submit"}}, {"Cancel", action{Kind: "project-cancel"}}}
		return
	}
	query := strings.ToLower(strings.TrimSpace(m.projectInput.Value()))
	m.menu = nil
	if query == "" || strings.Contains("all projects", query) {
		m.menu = append(m.menu, menuItem{"All projects", action{Kind: "project-filter"}})
	}
	for _, p := range m.snapshot.Projects {
		if query == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Path), query) {
			m.menu = append(m.menu, menuItem{p.Name + " · " + p.Path, action{Kind: "project-filter", ID: p.ID}})
		}
	}
	if len(m.menu) == 0 {
		m.menu = append(m.menu, menuItem{"No matching projects", action{Kind: "project-no-match"}})
	}
	m.menu = append(m.menu, menuItem{m.icon("project-add") + " Add project…", action{Kind: "project-add"}})
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
	case "up", "shift+tab":
		m.menuIndex = (m.menuIndex + len(m.menu) - 1) % len(m.menu)
		return nil
	case "down", "tab":
		m.menuIndex = (m.menuIndex + 1) % len(m.menu)
		return nil
	case "enter":
		return m.activate(action{Kind: "menu-select", Index: m.menuIndex})
	}
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
	case "project.add":
		if m.projectMode == "add" {
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
	m.showMenu(t.Title, []menuItem{{verb, action{Kind: kind, ID: id}}, {"Delete permanently…", action{Kind: "thread-delete", ID: id}}})
}

func (m *Model) confirmThreadDelete(id string) {
	t, ok := m.threadByID(id)
	if !ok {
		return
	}
	m.showMenu("Delete thread: "+t.Title, []menuItem{
		{"Cancel", action{Kind: "thread-delete-cancel"}},
		{"Delete permanently · history, drafts and owned work", action{Kind: "thread-delete-confirm", ID: id, Index: int(t.LifecycleRevision)}},
	})
	m.status = fmt.Sprintf("Delete %q? This cannot be undone.", t.Title)
}
