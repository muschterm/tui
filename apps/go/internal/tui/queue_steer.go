package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (m *Model) steerAction(id string) action {
	t := m.thread()
	return action{Kind: "steer", ID: id, Value: t.TurnID, Revision: t.QueueRevision}
}

func (m *Model) steerBlocked(q protocol.Prompt) string {
	if !m.connected {
		return "Reconnect before steering; the message stays queued"
	}
	if m.state.Edit != nil && m.state.Edit.ID == q.ID {
		return "Save or cancel this queued edit before steering"
	}
	return protocol.QueueSteerBlocked(m.snapshot.Capabilities, m.thread(), q)
}

func (m *Model) steerQueued(a action) tea.Cmd {
	for _, q := range m.thread().Queue {
		if q.ID != a.ID {
			continue
		}
		if reason := m.steerBlocked(q); reason != "" {
			return m.showNotice("Steer: " + reason)
		}
		// Keep the turn/revision captured by the visible action or menu entry.
		// A delayed selection must not silently target newer work or queue edits.
		return m.command(protocol.Command{Kind: "queue.steer", TargetID: a.ID, Revision: a.Revision, ExpectedTurnID: a.Value}, a)
	}
	return m.showNotice("This message is no longer queued")
}
