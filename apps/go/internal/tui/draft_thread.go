package tui

import (
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type startedDraft struct {
	ThreadID string
	Command  protocol.Command
	Revision int64
}

func (m *Model) creatingThread() bool { return m.state.DraftProjectID != "" }
func (m *Model) hasComposer() bool    { return m.creatingThread() || m.state.Active != "" }

func (m *Model) beginThreadDraft(projectID string) {
	if _, ok := m.projectByID(projectID); !ok {
		return
	}
	if m.state.Edit != nil {
		m.status = "Save or cancel the queued edit first"
		return
	}
	m.viewState().Draft = m.prompt.Value()
	if m.state.Active != "" {
		m.viewState().RightVisible = m.state.Layout.Right
	}
	m.state.Active, m.state.DraftProjectID = "", projectID
	if m.state.DraftThreads[projectID] == nil {
		m.state.DraftThreads[projectID] = m.newDraftView()
	}
	m.state.Layout.Right, m.state.Layout.Maximized = false, false
	m.state.Layout.ClearReveal()
	m.viewState().CompactColumn = shell.CenterRegion
	m.loadDraft()
	m.setFocus("prompt")
	m.markDirty()
}

func (m *Model) composerSelection() protocol.Settings {
	// An explicit queued edit retains that item's captured settings, even while
	// another prompt is running. Ordinary Send uses the locked running values.
	settings := m.viewState().Settings
	if (m.state.Edit == nil || m.state.Edit.ThreadID != m.state.Active) && activeTurn(m.thread()) {
		settings = m.thread().Effective
	}
	if c, ok := m.composerConfig(); ok && c.agent.Kind == "acp" {
		// A replaced catalogue may withdraw fields. Capture their unavailable
		// state without rewriting the saved preference or inventing a choice.
		for _, field := range settingFieldOrder {
			if option, offered := c.option(field); !offered {
				setSettingValue(&settings, field, "unavailable")
			} else if field == "speed" && (settings.Speed == "" || settings.Speed == "unavailable") && (c.agent.ID == "codex" || c.agent.ID == "claude") {
				baseline := "default"
				if c.agent.ID == "claude" {
					baseline = "standard"
				}
				if _, valid := optionValue(option, baseline); valid {
					settings.Speed = baseline
				}
			}
		}
	}
	return settings
}

func (m *Model) configurationLocked() bool {
	return activeTurn(m.thread()) ||
		m.busy != nil && m.busy.Kind == "thread.start" && m.busy.ProjectID == m.state.DraftProjectID ||
		m.state.StartedDraft != nil && m.state.StartedDraft.Command.ProjectID == m.state.DraftProjectID
}

func (m *Model) sendBlocked() string {
	// Never submit a prompt the user cannot see.
	if reason := m.hiddenWorkBlocked(); reason != "" {
		return reason
	}
	if !m.hasComposer() {
		return "Choose New thread first"
	}
	if m.busy != nil {
		return "A command is pending; use Retry to reconcile it"
	}
	if m.state.Edit != nil && m.state.Edit.ThreadID != m.state.Active {
		return "Finish the queued edit in its thread first"
	}
	for _, attachment := range m.viewState().Attachments {
		if attachment.Kind == "workspace-file" && !m.hasCapability("workspace-file-context") {
			return "Update this server to send attached project files"
		}
	}
	if m.creatingThread() {
		if m.state.StartedDraft != nil {
			return "Waiting for the created thread to appear"
		}
		if _, ok := m.projectByID(m.state.DraftProjectID); !ok {
			return "Project was removed; draft retained"
		}
		if !m.hasCapability("thread-start") {
			return "Update this server to create a thread at first Send"
		}
	}
	if m.thread().Closed && !m.hasCapability("closed-thread-send") {
		return "Update this server to send to a closed thread, or Reopen it first"
	}
	if reason := m.agentSendBlocked(m.composerSelection()); reason != "" {
		return reason
	}
	if strings.TrimSpace(m.prompt.Value()) == "" {
		return "Write a prompt first"
	}
	return ""
}

// agentSendBlocked validates the captured selection against the chosen agent's
// own options. Servers without agent records keep the fixture-only rule, and
// the fixture agent keeps its fixed options either way.
func (m *Model) agentSendBlocked(s protocol.Settings) string {
	t := m.thread()
	if !m.acpAgents() {
		// A server that lists agents without the capability still only runs the
		// fixture: accept its record as well as the legacy name.
		agent, known := m.threadAgent(t)
		if (t.Agent != fixtureAgentName && !(known && agent.Kind == "fixture")) || s.Model != "fixture-model" {
			return "Choose the Demo Reference model; Codex and Claude are not connected yet"
		}
		return fixtureSettingsBlocked(s)
	}
	c, ok := m.threadConfig(t)
	if !ok {
		return "Choose an agent for this thread"
	}
	if reason := agentUnready(c.agent); reason != "" {
		return reason
	}
	if c.agent.Kind == "fixture" {
		if s.Model != "fixture-model" {
			return "Choose the Demo Reference model"
		}
		return fixtureSettingsBlocked(s)
	}
	return c.blocked(s)
}

func (m *Model) reconcileStartedDraft() {
	accepted := m.state.StartedDraft
	if accepted == nil {
		return
	}
	if _, ok := m.threadByID(accepted.ThreadID); !ok {
		if m.snapshot.Revision >= accepted.Revision {
			m.state.StartedDraft = nil
			m.status = "Created thread was removed; local draft retained"
			m.markDirty()
		}
		return // Keep the receipt and draft through receipt-before-snapshot/relaunch.
	}
	c := accepted.Command
	v := m.state.DraftThreads[c.ProjectID]
	if v == nil {
		v = &threadView{Agent: c.Agent}
		if c.Settings != nil {
			v.Settings = *c.Settings
		}
	}
	removeSentAttachments(v, c.Attachments)
	if v.Draft == c.Text {
		v.Draft = ""
	}
	// A snapshot can expose the created thread before its receipt. Preserve a
	// view the user has already opened, including text typed there. If both
	// locations now hold input, keep the creation draft available via New thread.
	if target := m.state.Threads[accepted.ThreadID]; target != nil {
		if target.Draft == "" && len(target.Attachments) == 0 {
			target.Draft, target.Attachments = v.Draft, v.Attachments
			v.Draft, v.Attachments = "", nil
			if m.state.Active == accepted.ThreadID {
				m.prompt.SetValue(target.Draft)
			}
		}
		if v.Draft == "" && len(v.Attachments) == 0 {
			delete(m.state.DraftThreads, c.ProjectID)
		}
	} else {
		m.state.Threads[accepted.ThreadID] = v
		delete(m.state.DraftThreads, c.ProjectID)
	}
	m.state.StartedDraft = nil
	if m.state.DraftProjectID == c.ProjectID {
		// The transferred draft already includes any typing after Send.
		m.state.DraftProjectID = ""
		m.selectThread(accepted.ThreadID)
	}
	m.markDirty()
}
