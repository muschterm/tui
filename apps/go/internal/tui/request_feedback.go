package tui

import "github.com/muschterm/tui/apps/go/internal/protocol"

// Feedback belongs to the exact request revision, not the ordinary composer.
// It is transient UI state; pending command identities remain durably saved.
type requestFeedback struct {
	ID         string
	Revision   int64
	Text       string
	Validation bool
}

func (m *Model) setRequestFeedback(threadID, requestID string, revision int64, message string, validation bool) {
	if m.requestFeedback == nil {
		m.requestFeedback = map[string]requestFeedback{}
	}
	m.requestFeedback[threadID] = requestFeedback{requestID, revision, safe(message), validation}
}

func (m *Model) clearRequestFeedback(threadID, requestID string, revision int64, validationOnly bool) {
	f, ok := m.requestFeedback[threadID]
	if ok && f.ID == requestID && f.Revision == revision && (!validationOnly || f.Validation) {
		delete(m.requestFeedback, threadID)
	}
}

func (m *Model) requestNotice(r protocol.Request) (string, bool) {
	if c := m.busy; c != nil && c.Kind == "request.answer" && c.ThreadID == m.state.Active && c.TargetID == r.ID && c.Revision == r.Revision {
		if m.inFlight {
			switch c.RequestAction {
			case protocol.RequestActionDecline:
				return "Declining…", false
			case protocol.RequestActionCancel:
				return "Cancelling…", false
			}
			return "Submitting answer…", false
		}
		return "Delivery unconfirmed · F4 → Retry pending command", true
	}
	if m.thread().NeedsResume {
		return "Resume this thread first (F4 → Resume).", true
	}
	f, ok := m.requestFeedback[m.state.Active]
	if ok && f.ID == r.ID && f.Revision == r.Revision {
		return f.Text, true
	}
	return "", false
}
