package agent

import (
	"fmt"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Unavailable is the value a composer settings field carries when the connected
// agent exposes no option for it. It is a display fact, not a selection.
const Unavailable = "unavailable"

// Option bounds. An adapter's option list feeds the composer, so it is bounded
// like every other retained payload.
const (
	maxOptions = 64
	maxValues  = 128
	maxLabel   = 256
)

// MapOptions converts the agent's reported session config options into protocol
// records and the composer field mapping. Unknown categories and option types
// are retained for display rather than discarded.
func MapOptions(options []acp.SessionConfigOption) ([]protocol.ConfigOption, protocol.SettingFields) {
	var out []protocol.ConfigOption
	for _, option := range options {
		if len(out) >= maxOptions {
			break
		}
		switch {
		case option.Select != nil:
			s := option.Select
			mapped := protocol.ConfigOption{ID: string(s.Id), Name: label(s.Name), Type: "select", Current: string(s.CurrentValue)}
			if s.Category != nil {
				mapped.Category = label(string(*s.Category))
			}
			if s.Description != nil {
				mapped.Description = label(*s.Description)
			}
			for _, value := range flatten(s.Options) {
				if len(mapped.Values) >= maxValues {
					break
				}
				models, valid := optionModels(value.Meta)
				if !valid {
					continue
				}
				v := protocol.ConfigValue{Value: string(value.Value), Name: label(value.Name), Models: models}
				if value.Description != nil {
					v.Description = label(*value.Description)
				}
				mapped.Values = append(mapped.Values, v)
			}
			out = append(out, mapped)
		case option.Boolean != nil:
			b := option.Boolean
			mapped := protocol.ConfigOption{ID: string(b.Id), Name: label(b.Name), Type: "boolean", Current: fmt.Sprint(b.CurrentValue), Values: []protocol.ConfigValue{{Value: "true", Name: "On"}, {Value: "false", Name: "Off"}}}
			if b.Category != nil {
				mapped.Category = label(string(*b.Category))
			}
			if b.Description != nil {
				mapped.Description = label(*b.Description)
			}
			out = append(out, mapped)
		}
	}
	return out, Fields(out)
}

func flatten(options acp.SessionConfigSelectOptions) []acp.SessionConfigSelectOption {
	if options.Ungrouped != nil {
		return *options.Ungrouped
	}
	var out []acp.SessionConfigSelectOption
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			out = append(out, group.Options...)
		}
	}
	return out
}

func label(text string) string { return Truncate(Sanitize(text), maxLabel) }

func optionModels(meta map[string]any) ([]string, bool) {
	raw, ok := meta["tui-go.models"]
	if !ok {
		return nil, true
	}
	models := make([]string, 0)
	add := func(model string) {
		if model == "" || len(model) > maxLabel || len(models) >= maxValues {
			return
		}
		for _, existing := range models {
			if existing == model {
				return
			}
		}
		models = append(models, model)
	}
	switch values := raw.(type) {
	case []string:
		for _, model := range values {
			add(model)
		}
	case []any:
		for _, value := range values {
			if model, ok := value.(string); ok {
				add(model)
			}
		}
	}
	return models, len(models) != 0
}

// Fields maps composer settings fields onto option IDs. Option IDs and values
// are not portable between adapters (the probe recorded claude's
// mode/model/effort against codex's mode/collaboration_mode/model/
// reasoning_effort/fast-mode), so mapping keys on the agent's own category and
// treats every ID and value as opaque. An unmapped field stays empty rather
// than borrowing an unrelated option, and the option itself remains in Options
// for display.
func Fields(options []protocol.ConfigOption) protocol.SettingFields {
	var fields protocol.SettingFields
	assign := func(target *string, id string) {
		if *target == "" {
			*target = id
		}
	}
	collaboration := ""
	for _, option := range options {
		match := strings.ToLower(option.ID + " " + option.Name)
		switch option.Category {
		case "model":
			assign(&fields.Model, option.ID)
		case "thought_level":
			assign(&fields.Effort, option.ID)
		case "mode":
			assign(&fields.Permissions, option.ID)
		case "collaboration_mode":
			if collaboration == "" {
				collaboration = option.ID
			}
		case "model_config":
			// Two composer fields share this category, so the only available
			// discriminator is the agent's own wording.
			switch {
			case strings.Contains(match, "context"):
				assign(&fields.Context, option.ID)
			case strings.Contains(match, "fast"), strings.Contains(match, "speed"):
				assign(&fields.Speed, option.ID)
			}
		}
	}
	// An agent that reports only a collaboration mode still gets a permissions
	// control; one that reports both keeps the permission mode there and leaves
	// collaboration to the options list.
	assign(&fields.Permissions, collaboration)
	return fields
}

// Assignment is one captured setting applied through session/set_config_option.
type Assignment struct {
	Field, ConfigID, Value string
	Boolean                bool
}

// pairs lists the five composer fields against their mapped option IDs in a
// stable order so dispatch applies settings deterministically.
func pairs(fields protocol.SettingFields, settings protocol.Settings) []struct{ field, id, value string } {
	return []struct{ field, id, value string }{
		{"model", fields.Model, settings.Model},
		{"effort", fields.Effort, settings.Effort},
		{"permissions", fields.Permissions, settings.Permissions},
		{"context", fields.Context, settings.Context},
		{"speed", fields.Speed, settings.Speed},
	}
}

func option(options []protocol.ConfigOption, id string) *protocol.ConfigOption {
	for i := range options {
		if options[i].ID == id {
			return &options[i]
		}
	}
	return nil
}

// ValidateSettings checks captured settings against an agent's last probe. A
// mapped field must name one of that option's value IDs; an unmapped field must
// carry no selection. Names are never accepted in place of value IDs.
func ValidateSettings(a protocol.Agent, settings protocol.Settings) error {
	if len(a.Options) == 0 {
		return fmt.Errorf("%s has no probed settings; probe the agent before starting a thread", a.Name)
	}
	for _, pair := range pairs(a.Fields, settings) {
		if pair.id == "" {
			if pair.value != "" && pair.value != Unavailable {
				return fmt.Errorf("%s exposes no %s option; leave it unavailable", a.Name, pair.field)
			}
			continue
		}
		mapped := option(a.Options, pair.id)
		if mapped == nil {
			return fmt.Errorf("%s no longer reports the %s option; probe the agent again", a.Name, pair.field)
		}
		found := false
		applicable := false
		value := pair.value
		if pair.field == "permissions" && value == Unavailable {
			value = legacyPermissionBaseline(a.ID)
		}
		if pair.field == "speed" && (value == Unavailable || value == "") {
			value = legacySpeedBaseline(a.ID)
		}
		for _, choice := range mapped.Values {
			if len(choice.Models) != 0 {
				listed := false
				for _, model := range choice.Models {
					listed = listed || model == settings.Model
				}
				if !listed {
					continue
				}
			}
			applicable = true
			found = found || choice.Value == value
		}
		if !applicable && (pair.value == Unavailable || pair.value == "") {
			continue
		}
		if !found {
			return fmt.Errorf("%s does not offer %q for %s", a.Name, pair.value, pair.field)
		}
	}
	return nil
}

// Assignments returns the set_config_option calls for captured settings, in a
// stable order and without touching fields the agent does not expose.
func Assignments(a protocol.Agent, settings protocol.Settings) []Assignment {
	var out []Assignment
	for _, pair := range pairs(a.Fields, settings) {
		if pair.id == "" || pair.value == "" && pair.field != "speed" {
			continue
		}
		value := pair.value
		if pair.field == "permissions" && value == Unavailable {
			value = legacyPermissionBaseline(a.ID)
		}
		if pair.field == "speed" && (value == Unavailable || value == "") {
			value = legacySpeedBaseline(a.ID)
			mapped := option(a.Options, pair.id)
			found := false
			if mapped != nil {
				for _, choice := range mapped.Values {
					if choice.Value != value {
						continue
					}
					if len(choice.Models) == 0 {
						found = true
					}
					for _, model := range choice.Models {
						found = found || model == settings.Model
					}
				}
			}
			if !found {
				continue
			}
		}
		if value == "" || value == Unavailable {
			continue
		}
		mapped := option(a.Options, pair.id)
		out = append(out, Assignment{Field: pair.field, ConfigID: pair.id, Value: value, Boolean: mapped != nil && mapped.Type == "boolean"})
	}
	return out
}

// Old snapshots had no permission selector. Never let their unavailable value
// inherit a previous turn's explicit Full access on a reused provider session.
func legacyPermissionBaseline(agentID string) string {
	switch agentID {
	case "claude":
		return "default"
	case "codex":
		return "supervised"
	}
	return ""
}

// Old queued prompts may predate a newly discovered speed selector. Applying
// Standard explicitly prevents a previous Fast turn from leaking into them.
func legacySpeedBaseline(agentID string) string {
	switch agentID {
	case "claude":
		return "standard"
	case "codex":
		return "default"
	}
	return ""
}

// Defaults returns the agent's currently reported values as composer settings,
// so a new thread starts from what the agent itself selected.
func DefaultSettings(a protocol.Agent) protocol.Settings {
	return EffectiveSettings(a.Fields, a.Options)
}

// EffectiveSettings reads the agent-reported current value of each mapped
// option. An unmapped field reports unavailable rather than an invented value.
func EffectiveSettings(fields protocol.SettingFields, options []protocol.ConfigOption) protocol.Settings {
	value := func(id string) string {
		if id == "" {
			return Unavailable
		}
		if mapped := option(options, id); mapped != nil {
			return mapped.Current
		}
		return Unavailable
	}
	return protocol.Settings{Model: value(fields.Model), Effort: value(fields.Effort), Permissions: value(fields.Permissions), Context: value(fields.Context), Speed: value(fields.Speed)}
}
