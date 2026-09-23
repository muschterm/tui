package tui

import (
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"strings"
	"testing"
)

func TestModelChangeFiltersSpeedAndEffortWithoutSending(t *testing.T) {
	m := acpModel()
	a := &m.snapshot.Agents[1]
	a.Fields.Speed = "speed"
	a.Options = append(a.Options, protocol.ConfigOption{ID: "speed", Name: "Speed", Current: "default", Values: []protocol.ConfigValue{
		{Value: "default", Name: "Standard", Models: []string{"opus"}},
		{Value: "fast", Name: "Fast", Models: []string{"opus"}},
	}})
	a.Options[1].Values = []protocol.ConfigValue{
		{Value: "low", Name: "Low", Description: strings.Repeat("description ", 30), Models: []string{"sonnet"}},
		{Value: "high", Name: "High", Description: "long description", Models: []string{"opus"}},
	}
	m.beginThreadDraft("alpha")
	m.chooseAgent("claude")
	m.prompt.SetValue("keep draft")
	m.chooseSetting("model", "opus")
	if m.composerSelection().Effort != "high" || m.composerSelection().Speed != "default" {
		t.Fatalf("opus defaults %+v", m.composerSelection())
	}
	m.chooseSetting("speed", "fast")
	m.openPromptSettings("effort")
	if labels := strings.Join(menuLabels(m), "\n"); !strings.Contains(labels, "High") || strings.Contains(labels, "description") || strings.Contains(labels, "Low") {
		t.Fatal(labels)
	}
	m.chooseSetting("model", "sonnet")
	if s := m.composerSelection(); s.Speed != "unavailable" || s.Effort != "low" {
		t.Fatalf("incompatible settings retained %+v", s)
	}
	c, _ := m.composerConfig()
	if _, ok := c.option("speed"); ok {
		t.Fatal("speed offered to incompatible model")
	}
	if m.busy != nil || m.prompt.Value() != "keep draft" {
		t.Fatal("model selection changed/sent prompt")
	}
}

func TestStandardSpeedRemainsSelectableInComposer(t *testing.T) {
	m := acpModel()
	a := &m.snapshot.Agents[1]
	a.Fields.Speed = "speed"
	a.Options = append(a.Options, protocol.ConfigOption{ID: "speed", Name: "Speed", Current: "default", Values: []protocol.ConfigValue{{Value: "default", Name: "Standard", Models: []string{"sonnet"}}, {Value: "fast", Name: "Fast", Models: []string{"sonnet"}}}})
	m.beginThreadDraft("alpha")
	m.chooseAgent("claude")
	found := false
	for _, c := range m.composerSettingDisplays(m.thread(), m.composerSelection()) {
		if c.field == "speed" {
			found = true
		}
	}
	if !found {
		t.Fatal("Standard speed hides the selector")
	}
}

func TestUnavailableFastReasonRemainsReachable(t *testing.T) {
	m := acpModel()
	a := &m.snapshot.Agents[1]
	a.Fields.Speed = "speed"
	a.Options = append(a.Options, protocol.ConfigOption{ID: "speed", Name: "Speed", Description: "Fast unavailable · extra usage disabled", Current: "standard", Values: []protocol.ConfigValue{{Value: "standard", Name: "Standard", Models: []string{"sonnet"}}}})
	m.beginThreadDraft("alpha")
	m.chooseAgent("claude")
	m.openPromptSettings("speed")
	if labels := strings.Join(menuLabels(m), "\n"); !strings.Contains(labels, "extra usage disabled") || !strings.Contains(labels, "Standard") {
		t.Fatal(labels)
	}
	found := false
	for _, c := range m.composerSettingDisplays(m.thread(), m.composerSelection()) {
		found = found || c.field == "speed"
	}
	if !found {
		t.Fatal("restricted speed control hidden")
	}
}
