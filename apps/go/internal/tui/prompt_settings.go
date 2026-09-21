package tui

import tea "charm.land/bubbletea/v2"

func (m *Model) openPromptSettings(field string) tea.Cmd {
	if m.configurationLocked() {
		return m.showNotice("Settings are read-only during active work")
	}
	if field == "" {
		field = "model"
	}
	var items []menuItem
	switch field {
	case "agent", "model":
		items = []menuItem{
			{"Reference · Demo model", action{Kind: "setting-model"}},
			{"Codex · integration not connected", action{Kind: "provider-unavailable", Value: "Codex"}},
			{"Claude · integration not connected", action{Kind: "provider-unavailable", Value: "Claude"}},
		}
	case "effort":
		for _, value := range []string{"low", "medium", "high"} {
			items = append(items, menuItem{effortDisplayName(value), action{Kind: "setting", Value: value}})
		}
	default:
		s := m.composerSelection()
		items = []menuItem{{"Permissions: " + permissionDisplayName(s.Permissions), action{Kind: "noop"}}, {"Context: " + s.Context + " · Speed: " + s.Speed, action{Kind: "noop"}}, {"Demo supports only these fixed options", action{Kind: "noop"}}}
	}
	m.showMenu("Settings · "+field, items)
	return nil
}
