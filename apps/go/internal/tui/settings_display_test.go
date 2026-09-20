package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestSettingsPresentationPreservesProtocolValues(t *testing.T) {
	s := protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	items := composerSettings("Fixture agent", s)
	var labels []string
	for _, item := range items {
		labels = append(labels, item.label)
	}
	got := strings.Join(labels, " · ")
	if got != "Demo · Reference · Medium · Simulated" {
		t.Fatal(got)
	}
	if s.Model != "fixture-model" || s.Permissions != "fixture-only" {
		t.Fatal("presentation mutated protocol values")
	}
	for input, want := range map[string]string{"claude-opus-5": "Claude Opus 5", "claude-fable-5.1": "Claude Fable 5.1", "gpt-6-astra": "GPT-6-Astra", "gpt-5.6-sol": "GPT-5.6-Sol", "custom-model": "custom-model"} {
		if got := modelDisplayName(input); got != want {
			t.Errorf("%s: %q", input, got)
		}
	}
}
func TestSettingsKeepSuppliedSpeedContextAndReasoningIndependent(t *testing.T) {
	s := protocol.Settings{Model: "claude-opus-5", Effort: "high", Context: "1M", Speed: "fast", Permissions: "ask"}
	items := composerSettings("claude", s)
	want := []string{"Claude", "Claude Opus 5", "Fast", "High", "· 1M", "ask"}
	if len(items) != len(want) {
		t.Fatal(items)
	}
	for i, label := range want {
		if items[i].label != label {
			t.Fatal(items)
		}
	}
	if effortDisplayName("xhigh") == effortDisplayName("max") {
		t.Fatal("distinct effort values collapsed")
	}
}

func TestPrettyReasoningAndContextLabels(t *testing.T) {
	for value, want := range map[string]string{"low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra High", "max": "Max", "ultra": "Ultra", "ultracode": "Ultracode"} {
		if got := effortDisplayName(value); got != want {
			t.Fatalf("%s: %q", value, got)
		}
	}
	items := composerSettings("claude", protocol.Settings{Model: "claude-fable-5.1", Speed: "fast", Effort: "xhigh", Context: "200k", Permissions: "ask"})
	var labels []string
	for _, item := range items {
		labels = append(labels, item.label)
	}
	if got := strings.Join(labels, " "); got != "Claude Claude Fable 5.1 Fast Extra High · 200k ask" {
		t.Fatal(got)
	}
}
