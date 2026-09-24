package tui

import "strings"

func (m *Model) menuStart(visible int) int {
	offset := min(max(0, m.menuOffset), max(0, len(m.menu)-visible))
	if m.menuIndex < offset {
		offset = m.menuIndex
	} else if m.menuIndex >= offset+visible {
		offset = m.menuIndex - visible + 1
	}
	return max(0, offset)
}

func (m *Model) scrollTo(target string, offset int, f frame) {
	v := m.viewState()
	var current *int
	limit := 0
	switch target {
	case "transcript":
		current, limit = &v.Scroll, f.transcriptMax
	case "detail":
		current, limit = &v.DetailScroll, f.detailMax
	case "request":
		current, limit = &v.RequestScroll, f.requestMax
	case "bottom":
		current, limit = &v.BottomScroll, f.bottomMax
	case "navigation":
		m.navScroll = min(f.navMax, max(0, offset))
	case "closed-navigation":
		m.closedScroll = min(f.closedMax, max(0, offset))
	case "sidebar-settings":
		m.settingsScroll = min(f.settingsMax, max(0, offset))
	case "menu":
		if bar, ok := f.scrollbars[target]; ok {
			m.menuOffset = min(bar.Bar.MaxOffset, max(0, offset))
			m.menuIndex = min(m.menuOffset+bar.Bar.Viewport-1, max(m.menuOffset, m.menuIndex))
			m.menuIndex = m.menuNearestSelectable(m.menuIndex)
		}
	case "prompt":
		m.promptView.ScrollTo(&m.prompt, m.promptMetrics.Total, offset)
	case "answer":
		m.answerView.ScrollTo(&m.answer, m.answerMetrics.Total, offset)
	}
	if current != nil {
		next := min(limit, max(0, offset))
		if next != *current {
			*current = next
			m.markDirty()
		}
		if target == "transcript" {
			// Reaching the end follows new output; any other offset holds the
			// reading position and counts activity received from here on.
			if v.Pinned && next != limit {
				v.SeenActivity = messageCount(m.thread())
			}
			v.Pinned = next == limit
		}
	}
}

func (m *Model) scrollFocus() string {
	if strings.HasPrefix(m.focus, "option:") || strings.HasPrefix(m.focus, "question-") || strings.HasPrefix(m.focus, "request-") || m.focus == "answer-other" {
		return "request"
	}
	switch m.focus {
	case "right-body":
		return "detail"
	case "bottom-body":
		return "bottom"
	case "navigation":
		return "navigation"
	case "closed-navigation", "sidebar-settings":
		return m.focus
	}
	if strings.HasPrefix(m.focus, "sidebar-setting:") {
		return "sidebar-settings"
	}
	if strings.HasPrefix(m.focus, "scrollbar-") {
		return strings.TrimPrefix(m.focus, "scrollbar-")
	}
	return "transcript"
}

func (m *Model) wheelTarget(f frame, x, y int) string {
	if len(m.menu) > 0 {
		return "menu"
	}
	for id, target := range f.scrollbars {
		if target.Rect.Contains(x, y) {
			return id
		}
	}
	switch {
	case f.prompt.Contains(x, y):
		return "prompt"
	case f.answer.Contains(x, y):
		return "answer"
	case f.request.Contains(x, y):
		return "request"
	case f.detail.Contains(x, y):
		return "detail"
	case f.transcript.Contains(x, y):
		return "transcript"
	case f.bottomBody.Contains(x, y):
		return "bottom"
	case f.navigation.Contains(x, y):
		return "navigation"
	case f.closedNavigation.Contains(x, y):
		return "closed-navigation"
	case f.settingsBody.Contains(x, y):
		return "sidebar-settings"
	}
	return ""
}
