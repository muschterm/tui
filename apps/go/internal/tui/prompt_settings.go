package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// maxSettingHelp bounds an agent-supplied description inside a menu row.
const maxSettingHelp = 44

func (m *Model) openPromptSettings(field string) tea.Cmd {
	if m.configurationLocked() {
		return m.showNoticeAs(noticeUnavailable, "Settings are read-only during active work")
	}
	if field == "" {
		field = "model"
	}
	c, ok := m.composerConfig()
	if !m.acpAgents() || !ok || c.agent.Kind == "fixture" && field != "agent" {
		m.openFixtureSettings(field)
		return nil
	}
	switch field {
	case "agent":
		m.showMenu("Settings · agent", m.agentMenuItems(c.agent))
	case "model", "effort", "permissions", "context", "speed":
		m.showMenu("Settings · "+field, m.optionMenuItems(c, field))
	default:
		m.showMenu("Settings · agent defaults", agentDetailItems(c))
	}
	return nil
}

// openFixtureSettings keeps the fixture-only composer menus unchanged for
// the fixture agent, including when ACP agents are also configured.
func (m *Model) openFixtureSettings(field string) {
	var items []menuItem
	switch field {
	case "agent", "model":
		items = []menuItem{{Label: "Reference · Demo model", Action: action{Kind: "setting-model"}}}
		if !m.acpAgents() {
			items = append(items,
				menuItem{Label: "Codex · integration not connected", Action: action{Kind: "provider-unavailable", Value: "Codex"}},
				menuItem{Label: "Claude · integration not connected", Action: action{Kind: "provider-unavailable", Value: "Claude"}},
			)
		}
	case "effort":
		for _, value := range []string{"low", "medium", "high"} {
			items = append(items, menuItem{Label: effortDisplayName(value), Action: action{Kind: "setting", Value: value}})
		}
	default:
		s := m.composerSelection()
		items = []menuItem{pairMenuItem("Permissions", permissionDisplayName(s.Permissions), action{Kind: "noop"}), {Label: "Context: " + s.Context + " · Speed: " + s.Speed, Action: action{Kind: "noop"}}, {Label: "Demo supports only these fixed options", Action: action{Kind: "noop"}}}
	}
	m.showMenu("Settings · "+field, items)
}

// agentMenuItems lists every configured agent with its reported readiness and
// detail, then the explicit probe for the chosen ACP agent.
func (m *Model) agentMenuItems(chosen protocol.Agent) []menuItem {
	var items []menuItem
	for _, a := range m.snapshot.Agents {
		label := m.questionMarker("single", a.ID == chosen.ID) + " " + safe(a.Name) + " · " + agentReadiness(a)
		if detail := strings.TrimSpace(singleLine(a.Detail)); detail != "" {
			label += " · " + ansi.Truncate(detail, maxSettingHelp, "…")
		}
		items = append(items, menuItem{Label: label, Action: action{Kind: "setting-agent", Value: a.ID}})
	}
	if chosen.Kind != "fixture" {
		items = append(items, menuItem{Label: probeLabel(chosen), Action: action{Kind: "agent-probe", ID: chosen.ID}})
	}
	return append(items, menuItem{Label: "Agent defaults…", Action: action{Kind: "settings", Value: "details"}})
}

func probeLabel(a protocol.Agent) string {
	verb := "Refresh options"
	if agentNeedsProbe(a) {
		verb = "Probe"
	}
	return verb + " · " + safe(a.Name)
}

// optionMenuItems lists one option's offered values. A field with no option
// keeps the agent's own default and offers nothing to choose.
func (m *Model) optionMenuItems(c agentConfig, field string) []menuItem {
	a := c.agent
	o, ok := c.option(field)
	if !ok {
		if a.Kind == "fixture" {
			return []menuItem{{Label: "Agent default · " + safe(a.Name) + " supports only its fixed " + field, Action: action{Kind: "noop"}}}
		}
		if agentNeedsProbe(a) {
			return []menuItem{{Label: "No probed options · " + field + " uses the agent default", Action: action{Kind: "noop"}}, {Label: probeLabel(a), Action: action{Kind: "agent-probe", ID: a.ID}}}
		}
		return []menuItem{{Label: "Agent default · " + safe(a.Name) + " offers no " + field + " option", Action: action{Kind: "noop"}}}
	}
	selected := settingValue(m.composerSelection(), field)
	var items []menuItem
	if field == "speed" && o.Description != "" {
		items = append(items, menuItem{Label: safe(o.Description), Action: action{Kind: "noop"}})
	}
	for _, v := range o.Values {
		label := m.questionMarker("single", v.Value == selected) + " " + settingValueLabel(field, v)
		if description := strings.TrimSpace(singleLine(v.Description)); description != "" && field != "effort" {
			label += " · " + ansi.Truncate(description, maxSettingHelp, "…")
		}
		items = append(items, menuItem{Label: label, Action: action{Kind: "setting-field", ID: field, Value: v.Value}})
	}
	if len(items) == 0 {
		items = append(items, menuItem{Label: "Agent default · " + safe(o.Name) + " offers no values", Action: action{Kind: "noop"}})
	}
	return items
}

// agentDetailItems shows the mapped fields and every remaining agent option
// read-only, as opaque agent defaults.
func agentDetailItems(c agentConfig) []menuItem {
	a := c.agent
	items := []menuItem{{Label: safe(a.Name) + " · " + agentReadiness(a), Action: action{Kind: "noop"}}}
	for _, field := range settingFieldOrder {
		if o, ok := c.option(field); ok {
			items = append(items, pairMenuItem(title(field), safe(o.Name), action{Kind: "settings", Value: field}))
		}
	}
	for _, o := range c.unmapped() {
		current := optionValueName(o, o.Current)
		if current == "" {
			current = safe(o.Current)
		}
		items = append(items, pairMenuItem(safe(o.Name), current+" · agent default", action{Kind: "noop"}))
	}
	if len(a.Capabilities) > 0 {
		items = append(items, pairMenuItem("Capabilities", strings.Join(a.Capabilities, ", "), action{Kind: "noop"}))
	}
	return items
}

// settingValueLabel prefers the agent's own value name and falls back to the
// existing display tables, never to an invented one.
func settingValueLabel(field string, v protocol.ConfigValue) string {
	if name := strings.TrimSpace(v.Name); name != "" {
		return safe(name)
	}
	switch field {
	case "model":
		return modelDisplayName(v.Value)
	case "effort":
		return effortDisplayName(v.Value)
	case "permissions":
		return permissionDisplayName(v.Value)
	}
	return safe(v.Value)
}
