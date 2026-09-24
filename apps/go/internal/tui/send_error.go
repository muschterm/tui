package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A rejected Send needs a persistent, actionable explanation. Reuse the modal
// input boundary so dismissal neither submits nor alters the underlying draft.
func (m *Model) showSendError(reason string) tea.Cmd {
	if m.hiddenWorkBlocked() != "" {
		return m.showNotice(reason)
	}
	m.status = reason
	width := max(12, min(63, m.width-11))
	items := []menuItem{}
	for _, line := range strings.Split(ansi.Wrap(safe(reason), width, ""), "\n") {
		items = append(items, menuItem{Label: line, Action: action{Kind: "noop"}})
	}
	if !m.configurationLocked() && m.agentSendBlocked(m.composerSelection()) != "" {
		field := "agent"
		if c, ok := m.composerConfig(); ok && agentUnready(c.agent) == "" {
			field = "model"
			for _, candidate := range settingFieldOrder {
				if option, offered := c.option(candidate); offered {
					if _, valid := optionValue(option, settingValue(m.composerSelection(), candidate)); !valid {
						field = candidate
						break
					}
				}
			}
		}
		items = append(items, menuItem{Label: "Choose " + field, Action: action{Kind: "settings", Value: field}})
	}
	items = append(items, menuItem{Label: "Back to prompt", Action: action{Kind: "menu-close"}})
	m.showMenu("Cannot send message", items)
	m.menuIndex = len(items) - 1
	if len(items) > 1 && items[len(items)-2].Action.Kind == "settings" {
		m.menuIndex--
	}
	return nil
}
