package tui

import (
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Display names never change the values captured in a submitted prompt.
func agentDisplayName(value string) string {
	switch strings.ToLower(value) {
	case "fixture agent", "fixture-agent":
		return "Demo"
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "copilot":
		return "Copilot"
	case "grok":
		return "Grok"
	}
	return safe(value)
}

func modelDisplayName(value string) string {
	switch strings.ToLower(value) {
	case "":
		return "Choose model"
	case "fixture-model":
		return "Reference"
	case "claude-opus-5", "claude opus 5":
		return "Claude Opus 5"
	case "claude-fable-5.1", "claude fable 5.1":
		return "Claude Fable 5.1"
	case "gpt-6-astra":
		return "GPT-6-Astra"
	case "gpt-5.6-sol":
		return "GPT-5.6-Sol"
	}
	return safe(value)
}

func effortDisplayName(value string) string {
	switch strings.ToLower(value) {
	case "none":
		return "None"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "Extra High"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	case "ultracode":
		return "Ultracode"
	}
	return safe(value)
}

func permissionDisplayName(value string) string {
	if value == "fixture-only" {
		return "Simulated"
	}
	if value == "" {
		return "Permissions —"
	}
	return safe(value)
}

// composerSettingDisplays labels the composer fields with the chosen agent's
// own option value names, falling back to the display tables above. It never
// renames a value the agent did not report.
func (m *Model) composerSettingDisplays(t protocol.Thread, s protocol.Settings) []settingDisplay {
	c, ok := m.threadConfig(t)
	c = c.forModel(s.Model)
	name := t.Agent
	if ok {
		name = c.agent.Name
	}
	items := composerSettings(name, s)
	if !ok {
		return items
	}
	if o, offered := c.option("speed"); offered && len(o.Values) >= 1 && baselineSpeed(s.Speed) {
		label := optionValueName(o, s.Speed)
		if label == "" {
			label = "Speed"
		}
		items = append(items, settingDisplay{label, "speed"})
	}
	for i := range items {
		field := items[i].field
		if field == "agent" {
			continue
		}
		o, mapped := c.option(field)
		if !mapped {
			continue
		}
		label := optionValueName(o, settingValue(s, field))
		if label == "" {
			continue
		}
		if field == "context" {
			label = "· " + label
		}
		items[i].label = safe(label)
	}
	return items
}

// baselineSpeed reports a speed value that means the provider's ordinary
// tier. Claude reports "standard"; Codex reports "default" when no tier is
// chosen. composerSettings shows nothing for it, and composerSettingDisplays
// adds the mapped option once, so both branches never append a speed field.
func baselineSpeed(value string) bool {
	return value == "" || value == "standard" || value == "default"
}

func optionalSetting(value string) bool {
	return value != "" && value != "unavailable" && value != "unknown"
}

type settingDisplay struct{ label, field string }

func composerSettings(agent string, s protocol.Settings) []settingDisplay {
	result := []settingDisplay{{agentDisplayName(agent), "agent"}, {modelDisplayName(s.Model), "model"}}
	if optionalSetting(s.Speed) && !baselineSpeed(s.Speed) {
		label := safe(s.Speed)
		if strings.EqualFold(s.Speed, "fast") {
			label = "Fast"
		}
		result = append(result, settingDisplay{label, "speed"})
	}
	if optionalSetting(s.Effort) {
		result = append(result, settingDisplay{effortDisplayName(s.Effort), "effort"})
	}
	if optionalSetting(s.Context) {
		result = append(result, settingDisplay{"· " + safe(s.Context), "context"})
	}
	return append(result, settingDisplay{permissionDisplayName(s.Permissions), "permissions"})
}
