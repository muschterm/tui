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
		return m.showNotice("Settings are read-only during active work")
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
		items = []menuItem{{"Reference · Demo model", action{Kind: "setting-model"}}}
		if !m.acpAgents() {
			items = append(items,
				menuItem{"Codex · integration not connected", action{Kind: "provider-unavailable", Value: "Codex"}},
				menuItem{"Claude · integration not connected", action{Kind: "provider-unavailable", Value: "Claude"}},
			)
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
		items = append(items, menuItem{label, action{Kind: "setting-agent", Value: a.ID}})
	}
	if chosen.Kind != "fixture" {
		items = append(items, menuItem{probeLabel(chosen), action{Kind: "agent-probe", ID: chosen.ID}})
	}
	return append(items, menuItem{"Agent defaults…", action{Kind: "settings", Value: "details"}})
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
			return []menuItem{{"Agent default · " + safe(a.Name) + " supports only its fixed " + field, action{Kind: "noop"}}}
		}
		if agentNeedsProbe(a) {
			return []menuItem{{"No probed options · " + field + " uses the agent default", action{Kind: "noop"}}, {probeLabel(a), action{Kind: "agent-probe", ID: a.ID}}}
		}
		return []menuItem{{"Agent default · " + safe(a.Name) + " offers no " + field + " option", action{Kind: "noop"}}}
	}
	selected := settingValue(m.composerSelection(), field)
	var items []menuItem
	for _, v := range o.Values {
		label := m.questionMarker("single", v.Value == selected) + " " + settingValueLabel(field, v)
		if description := strings.TrimSpace(singleLine(v.Description)); description != "" {
			label += " · " + ansi.Truncate(description, maxSettingHelp, "…")
		}
		items = append(items, menuItem{label, action{Kind: "setting-field", ID: field, Value: v.Value}})
	}
	if len(items) == 0 {
		items = append(items, menuItem{"Agent default · " + safe(o.Name) + " offers no values", action{Kind: "noop"}})
	}
	return items
}

// agentDetailItems shows the mapped fields and every remaining agent option
// read-only, as opaque agent defaults.
func agentDetailItems(c agentConfig) []menuItem {
	a := c.agent
	items := []menuItem{{safe(a.Name) + " · " + agentReadiness(a), action{Kind: "noop"}}}
	for _, field := range settingFieldOrder {
		if o, ok := c.option(field); ok {
			items = append(items, menuItem{title(field) + ": " + safe(o.Name), action{Kind: "settings", Value: field}})
		}
	}
	for _, o := range c.unmapped() {
		current := optionValueName(o, o.Current)
		if current == "" {
			current = safe(o.Current)
		}
		items = append(items, menuItem{safe(o.Name) + ": " + current + " · agent default", action{Kind: "noop"}})
	}
	if len(a.Capabilities) > 0 {
		items = append(items, menuItem{"Capabilities: " + strings.Join(a.Capabilities, ", "), action{Kind: "noop"}})
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
