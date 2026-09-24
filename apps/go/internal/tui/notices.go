package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type transientNotice struct {
	text       string
	severity   noticeSeverity
	generation uint64
}

// noticeSeverity picks the status mark painted before a notice. It never
// changes the notice text; info is the default.
type noticeSeverity int

const (
	noticeInfo        noticeSeverity = iota
	noticeUnavailable                // an explicit action cannot run here
	noticeError                      // an attempted operation failed
	noticeDone                       // an operation was confirmed
	noticeActive                     // an operation is in flight
)

// markState is the panelStatusMark state for a severity: unavailable is a
// gold "!", error a red "✕", done a green "✓", active a blue "●" and info a
// muted "·".
func (s noticeSeverity) markState() string {
	switch s {
	case noticeUnavailable:
		return "blocked"
	case noticeError:
		return "failed"
	case noticeDone:
		return "completed"
	case noticeActive:
		return "active"
	}
	return ""
}

type noticeExpired uint64

// A short explanation takes precedence over hover help without moving focus.
// Uncertain command delivery is rendered separately and never expires here.
func (m *Model) showNotice(text string) tea.Cmd {
	return m.showNoticeAs(noticeInfo, text)
}

// showNoticeAs is showNotice with an explicit severity. Repeating the same
// text coalesces: it keeps the running timer and only updates the severity.
func (m *Model) showNoticeAs(severity noticeSeverity, text string) tea.Cmd {
	text = safe(text)
	if m.notice.text == text {
		m.notice.severity = severity
		return nil
	}
	m.notice.text = text
	m.notice.severity = severity
	m.notice.generation++
	generation := m.notice.generation
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return noticeExpired(generation) })
}

func (m *Model) steeringNotice() string {
	text, _ := m.steeringNoticeSeverity()
	return text
}

// steeringNoticeSeverity is steeringNotice with its mark: in flight is active
// and unconfirmed delivery a warning, never a success.
func (m *Model) steeringNoticeSeverity() (string, noticeSeverity) {
	if m.busy == nil || m.busy.Kind != "queue.steer" {
		return "", noticeInfo
	}
	if m.inFlight {
		return "Steering queued message…", noticeActive
	}
	return "Steer delivery unconfirmed · F4 → Retry pending command", noticeUnavailable
}
