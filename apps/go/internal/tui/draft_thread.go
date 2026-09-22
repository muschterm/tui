package tui

import (
	"slices"
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
		m.state.DraftThreads[projectID] = &threadView{Agent: "Fixture agent", Settings: protocol.Settings{Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}}
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
	if m.state.Edit != nil && m.state.Edit.ThreadID == m.state.Active {
		return m.viewState().Settings
	}
	if activeTurn(m.thread()) {
		return m.thread().Effective
	}
	return m.viewState().Settings
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
	s := m.composerSelection()
	if m.thread().Agent != "Fixture agent" || s.Model != "fixture-model" {
		return "Choose the Demo Reference model; Codex and Claude are not connected yet"
	}
	if !slices.Contains([]string{"low", "medium", "high"}, s.Effort) || s.Permissions != "fixture-only" || s.Context != "unavailable" || s.Speed != "standard" {
		return "Choose valid effort, permissions, context and speed settings"
	}
	if strings.TrimSpace(m.prompt.Value()) == "" {
		return "Write a prompt first"
	}
	return ""
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
	if v.Draft == c.Text {
		v.Draft = ""
		if sameAttachmentSources(v.Attachments, c.Attachments) {
			v.Attachments = nil
		}
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
