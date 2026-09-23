package tui

import (
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"strings"
	"testing"
)

func TestUsageCompactDisplayAndModelCompatibility(t *testing.T) {
	m := testModel()
	th := &m.snapshot.Threads[0]
	th.Usage = &protocol.Usage{Used: 42000, Size: 100000, Model: m.composerSelection().Model, Cost: &protocol.UsageCost{Amount: "0.00341", Currency: "USD", Estimated: true, Scope: "session cumulative"}}
	if got := m.usageCompactLabel(); got != "Ctx 42% · ~$<0.01" {
		t.Fatal(got)
	}
	lines := strings.Join(m.usageLines(), "\n")
	if !strings.Contains(lines, "42000 tokens") || !strings.Contains(lines, "Estimated cost: USD 0.00341") {
		t.Fatal(lines)
	}
	th.Usage.Model = "another model"
	if got := m.usageCompactLabel(); !strings.HasPrefix(got, "Ctx —") {
		t.Fatal("mixed models", got)
	}
	if !strings.Contains(strings.Join(m.usageLines(), "\n"), "previous model/session") {
		t.Fatal("missing context provenance")
	}
}

func TestUsageCompactMissingCostDoesNotShowFakePrice(t *testing.T) {
	m := testModel()
	if got := m.usageCompactLabel(); got != "Ctx —" {
		t.Fatal(got)
	}
	m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 0, Size: 100}
	if got := m.usageCompactLabel(); got != "Ctx 0%" {
		t.Fatal(got)
	}
}
