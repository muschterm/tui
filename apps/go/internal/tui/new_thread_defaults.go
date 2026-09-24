package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// newThreadDefaultAgentValue is the value half of the explicit Agent
// label/value pair on the Agents settings page.
func (m *Model) newThreadDefaultAgentValue() string {
	saved := m.snapshot.AppSettings.NewThreadDefaults
	if saved == nil || saved.AgentID == "" {
		return "Built-in default"
	}
	if a, ok := m.agentByID(saved.AgentID); ok {
		return safe(a.Name)
	}
	return "Unavailable (" + safe(saved.AgentID) + ")"
}

// newThreadDefaultFieldValue is the value half of one default field's explicit
// label/value pair; the label is the field's title.
func (m *Model) newThreadDefaultFieldValue(a protocol.Agent, field string, settings protocol.Settings) string {
	value := settingValue(settings, field)
	label := value
	if a.Kind == "fixture" {
		switch field {
		case "model":
			label = modelDisplayName(value)
		case "effort":
			label = effortDisplayName(value)
		case "permissions":
			label = permissionDisplayName(value)
		}
	} else if option, ok := agentConfigFor(a).forModel(settings.Model).option(field); ok {
		if name := optionValueName(option, value); name != "" {
			label = name
		} else {
			label = "Unavailable (" + value + ")"
		}
	} else if value == "" || value == "unavailable" {
		label = "Agent default · not selectable"
	}
	if label == "" {
		label = "Choose value"
	}
	return safe(label)
}

// newThreadDefaultFieldSelectable mirrors openNewThreadDefaultMenu: it reports
// whether the agent offers any value for field, so the settings row can show
// a fixed value in muted ink instead of the accent of an actionable value.
func newThreadDefaultFieldSelectable(a protocol.Agent, field string, settings protocol.Settings) bool {
	if a.Kind == "fixture" {
		return field == "effort"
	}
	option, mapped := agentConfigFor(a).forModel(settings.Model).option(field)
	return mapped && len(option.Values) > 0
}

func (m *Model) openNewThreadDefaultMenu(a action) tea.Cmd {
	if m.settingsProjectID != "" {
		return m.showNotice("New thread defaults are in app Agents settings")
	}
	if !m.hasCapability("new-thread-defaults") || !m.hasCapability("app-settings") {
		return m.settingsUnavailable("new-thread-defaults")
	}
	rev := m.snapshot.AppSettings.Revision
	if a.Kind == "app-thread-agent" {
		var items []menuItem
		for _, candidate := range m.snapshot.Agents {
			label := safe(candidate.Name) + " · " + agentReadiness(candidate)
			if candidate.Kind != "fixture" && (candidate.State != "ready" || len(candidate.Options) == 0) {
				label += " · probe before selecting"
				items = append(items, menuItem{Label: label, Action: action{Kind: "app-thread-agent-set", ID: candidate.ID, Revision: rev}})
				items = append(items, menuItem{Label: probeLabel(candidate), Action: action{Kind: "app-thread-agent-probe", ID: candidate.ID}})
				continue
			}
			items = append(items, menuItem{Label: label, Action: action{Kind: "app-thread-agent-set", ID: candidate.ID, Revision: rev}})
		}
		m.showMenu("Default agent for new threads", items)
		return nil
	}
	saved := m.snapshot.AppSettings.NewThreadDefaults
	if saved == nil {
		return m.showNotice("Choose a default agent first")
	}
	agent, ok := m.agentByID(saved.AgentID)
	if !ok {
		return m.showNoticeAs(noticeUnavailable, "Saved agent is no longer configured; choose another agent")
	}
	var items []menuItem
	if agent.Kind == "fixture" {
		if a.ID == "effort" {
			for _, value := range []string{"low", "medium", "high"} {
				items = append(items, menuItem{Label: effortDisplayName(value), Action: action{Kind: "app-thread-field-set", ID: a.ID, Value: value, Revision: rev}})
			}
		}
	} else if option, mapped := agentConfigFor(agent).forModel(saved.Settings.Model).option(a.ID); mapped {
		if a.ID == "speed" && option.Description != "" {
			items = append(items, menuItem{Label: safe(option.Description), Action: action{Kind: "noop"}})
		}
		for _, value := range option.Values {
			label := settingValueLabel(a.ID, value)
			if description := strings.TrimSpace(singleLine(value.Description)); description != "" && a.ID != "effort" {
				label += " · " + description
			}
			items = append(items, menuItem{Label: label, Action: action{Kind: "app-thread-field-set", ID: a.ID, Value: value.Value, Revision: rev}})
		}
	}
	if len(items) == 0 {
		return m.showNoticeAs(noticeUnavailable, "This agent offers no selectable "+a.ID+" value")
	}
	m.showMenu("Default "+a.ID+" for new threads", items)
	return nil
}

func (m *Model) saveNewThreadDefault(a action) tea.Cmd {
	if m.settingsProjectID != "" {
		return m.showNotice("New thread defaults are in app Agents settings")
	}
	if !m.hasCapability("new-thread-defaults") || !m.hasCapability("app-settings") {
		return m.settingsUnavailable("new-thread-defaults")
	}
	next := m.snapshot.AppSettings
	if a.Kind == "app-thread-reset" {
		next.NewThreadDefaults = &protocol.NewThreadDefaults{}
		a.Revision = next.Revision
	} else if a.Kind == "app-thread-agent-set" {
		candidate, ok := m.agentByID(a.ID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "That agent is no longer configured")
		}
		if candidate.Kind == "fixture" {
			next.NewThreadDefaults = &protocol.NewThreadDefaults{AgentID: candidate.ID, Settings: protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}}
		} else {
			if candidate.State != "ready" || len(candidate.Options) == 0 {
				return m.showNotice("Probe " + safe(candidate.Name) + " before selecting defaults")
			}
			next.NewThreadDefaults = &protocol.NewThreadDefaults{AgentID: candidate.ID, Settings: agentConfigFor(candidate).defaults()}
			if a.Value != "" {
				option, offered := agentConfigFor(candidate).option("model")
				if _, valid := optionValue(option, a.Value); !offered || !valid {
					return m.showNoticeAs(noticeUnavailable, "That model is no longer offered")
				}
				next.NewThreadDefaults.Settings.Model = a.Value
				agentConfigFor(candidate).reconcileModel(&next.NewThreadDefaults.Settings)
			}
			if next.NewThreadDefaults.Settings.Model == "" {
				var items []menuItem
				if option, offered := agentConfigFor(candidate).option("model"); offered {
					for _, value := range option.Values {
						items = append(items, menuItem{Label: settingValueLabel("model", value), Action: action{Kind: "app-thread-agent-set", ID: candidate.ID, Value: value.Value, Revision: a.Revision}})
					}
				}
				if len(items) == 0 {
					return m.showNotice("Probe this agent to discover its models")
				}
				m.showMenuFor("Default model for ", safe(candidate.Name), items)
				return nil
			}
		}
	} else {
		if next.NewThreadDefaults == nil {
			return m.showNotice("Choose a default agent first")
		}
		candidate, ok := m.agentByID(next.NewThreadDefaults.AgentID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "Saved agent is no longer configured; choose another agent")
		}
		if candidate.Kind == "fixture" {
			if a.ID != "effort" || a.Value != "low" && a.Value != "medium" && a.Value != "high" {
				return m.showNoticeAs(noticeUnavailable, "That fixture setting is unavailable")
			}
		} else {
			option, mapped := agentConfigFor(candidate).forModel(next.NewThreadDefaults.Settings.Model).option(a.ID)
			if !mapped {
				return m.showNoticeAs(noticeUnavailable, "That option is no longer offered")
			}
			if _, offered := optionValue(option, a.Value); !offered {
				return m.showNoticeAs(noticeUnavailable, "That value is no longer offered")
			}
		}
		copyDefaults := *next.NewThreadDefaults
		setSettingValue(&copyDefaults.Settings, a.ID, a.Value)
		if a.ID == "model" {
			agentConfigFor(candidate).reconcileModel(&copyDefaults.Settings)
		}
		next.NewThreadDefaults = &copyDefaults
	}
	return m.command(protocol.Command{Kind: "settings.update", Revision: a.Revision, AppSettings: &next}, a)
}
