package agent

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestModelScopedOptionValidation(t *testing.T) {
	a := protocol.Agent{
		Name:   "Codex",
		Fields: protocol.SettingFields{Model: "model", Effort: "effort", Speed: "speed"},
		Options: []protocol.ConfigOption{
			{ID: "model", Values: []protocol.ConfigValue{{Value: "fast-model"}, {Value: "plain-model"}}},
			{ID: "effort", Values: []protocol.ConfigValue{{Value: "low", Models: []string{"fast-model"}}, {Value: "high", Models: []string{"plain-model"}}}},
			{ID: "speed", Values: []protocol.ConfigValue{{Value: "default", Models: []string{"fast-model"}}, {Value: "fast", Models: []string{"fast-model"}}}},
		},
	}
	good := protocol.Settings{Model: "fast-model", Effort: "low", Speed: "fast"}
	if err := ValidateSettings(a, good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSettings(a, protocol.Settings{Model: "plain-model", Effort: "high", Speed: Unavailable}); err != nil {
		t.Fatalf("plain model without speed must remain usable: %v", err)
	}
	for _, bad := range []protocol.Settings{
		{Model: "plain-model", Effort: "low", Speed: Unavailable},
		{Model: "plain-model", Effort: "high", Speed: "fast"},
		{Model: "fast-model", Effort: "low", Speed: Unavailable},
	} {
		if err := ValidateSettings(a, bad); err == nil {
			t.Fatalf("accepted invalid cross-model choice: %+v", bad)
		}
	}
}

func TestOptionModelsParsesBoundedMetadata(t *testing.T) {
	got, valid := optionModels(map[string]any{"tui-go.models": []any{"a", "a", 42, "b"}})
	if !valid || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("models = %v", got)
	}
	if _, valid := optionModels(map[string]any{"tui-go.models": []any{42}}); valid {
		t.Fatal("malformed model scope became universal")
	}
}

func TestMapOptionsRetainsModelScopeAndDropsMalformedScope(t *testing.T) {
	category := acp.SessionConfigOptionCategoryThoughtLevel
	values := acp.SessionConfigSelectOptionsUngrouped{
		{Name: "Low", Value: "low", Meta: map[string]any{"tui-go.models": []any{"model-a"}}},
		{Name: "Invalid", Value: "invalid", Meta: map[string]any{"tui-go.models": []any{42}}},
	}
	options, _ := MapOptions([]acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "effort", Name: "Effort", Category: &category, CurrentValue: "low", Type: "select",
		Options: acp.SessionConfigSelectOptions{Ungrouped: &values},
	}}})
	if len(options) != 1 || len(options[0].Values) != 1 || len(options[0].Values[0].Models) != 1 || options[0].Values[0].Models[0] != "model-a" {
		t.Fatalf("mapped model scope = %+v", options)
	}
}

func TestLegacyUnavailableSpeedAppliesStandardToCapableModel(t *testing.T) {
	a := protocol.Agent{ID: "codex", Name: "Codex", Fields: protocol.SettingFields{Model: "model", Speed: "speed"}, Options: []protocol.ConfigOption{
		{ID: "model", Values: []protocol.ConfigValue{{Value: "fast-model"}, {Value: "plain-model"}}},
		{ID: "speed", Values: []protocol.ConfigValue{{Value: "default", Models: []string{"fast-model"}}, {Value: "fast", Models: []string{"fast-model"}}}},
	}}
	for _, tc := range []struct {
		model string
		want  bool
	}{{"fast-model", true}, {"plain-model", false}} {
		for _, oldSpeed := range []string{Unavailable, ""} {
			settings := protocol.Settings{Model: tc.model, Speed: oldSpeed}
			if err := ValidateSettings(a, settings); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, assignment := range Assignments(a, settings) {
				found = found || assignment.Field == "speed" && assignment.Value == "default"
			}
			if found != tc.want {
				t.Fatalf("model %s old speed %q standard assignment = %v, want %v", tc.model, oldSpeed, found, tc.want)
			}
		}
	}
}
