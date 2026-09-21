package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type transientNotice struct {
	text       string
	generation uint64
}

type noticeExpired uint64

// A short explanation takes precedence over hover help without moving focus.
// Uncertain command delivery is rendered separately and never expires here.
func (m *Model) showNotice(text string) tea.Cmd {
	text = safe(text)
	if m.notice.text == text {
		return nil
	}
	m.notice.text = text
	m.notice.generation++
	generation := m.notice.generation
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return noticeExpired(generation) })
}

func (m *Model) steeringNotice() string {
	if m.busy == nil || m.busy.Kind != "queue.steer" {
		return ""
	}
	if m.inFlight {
		return "Steering queued message…"
	}
	return "Steer delivery unconfirmed · F4 → Retry pending command"
}
