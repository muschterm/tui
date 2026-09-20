package tui

import "github.com/muschterm/tui/apps/go/internal/protocol"

type threadIndicatorState uint8

const (
	threadUnknown threadIndicatorState = iota
	threadWorking
	threadAttention
	threadFailed
	threadFinished
)

// Failures and then attention win over execution, keeping problems discoverable.
// Idle is the fixture's successful/quiescent state, including empty new threads.
func threadIndicator(t protocol.Thread) threadIndicatorState {
	if t.State == "failed" || t.State == "error" {
		return threadFailed
	}
	for _, c := range t.Children {
		if c.State == "failed" || c.State == "error" {
			return threadFailed
		}
	}
	if t.NeedsResume {
		return threadAttention
	}
	for _, r := range t.Requests {
		if r.State == "pending" {
			return threadAttention
		}
	}
	switch t.State {
	case "waiting", "blocked", "interrupted", "cancelled", "canceled", "paused":
		return threadAttention
	}
	working := t.State == "running" || t.State == "active" || t.State == "in_progress"
	pending, unknown := len(t.Queue) > 0, false
	for _, c := range t.Children {
		switch c.State {
		case "waiting", "blocked", "interrupted", "cancelled", "canceled", "paused":
			return threadAttention
		case "running", "active", "in_progress":
			working = true
		case "pending", "queued":
			pending = true
		case "completed":
		default:
			unknown = true
		}
	}
	if working {
		return threadWorking
	}
	if pending {
		return threadAttention
	}
	if !unknown && (t.State == "idle" || t.State == "completed" || t.State == "finished") {
		return threadFinished
	}
	return threadUnknown
}

func (m *Model) threadIndicatorColor(state threadIndicatorState) string {
	p := m.colors()
	if !m.connected {
		return p.muted
	}
	switch state {
	case threadWorking:
		return m.activityColor(activitySummary{Working: true})
	case threadAttention:
		return p.gold
	case threadFailed:
		return p.red
	case threadFinished:
		return p.green
	default:
		return p.muted
	}
}
