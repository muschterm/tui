package tui

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (m *Model) usageTelemetry() *protocol.Usage { return m.thread().Usage }

func (m *Model) compatibleUsage(u *protocol.Usage) bool {
	return u != nil && (u.Model == "" || u.Model == m.composerSelection().Model) && (u.SessionID == "" || u.SessionID == m.thread().SessionID)
}

func (m *Model) usageGauge() (string, string) {
	u := m.usageTelemetry()
	if !m.compatibleUsage(u) {
		return "Ctx ?", "Context usage unavailable for the selected model · open usage details"
	}
	label := usageGauge(&u.Used, &u.Size)
	help := "Context usage unavailable · open usage details"
	if u.Used >= 0 && u.Size > 0 {
		help = fmt.Sprintf("Context %s / %s tokens · %s · open usage details", formatTokens(u.Used), formatTokens(u.Size), safe(u.Scope))
	}
	return label, help
}

func (m *Model) usageCompactLabel() string {
	label, _ := m.usageGauge()
	if u := m.usageTelemetry(); u != nil && u.Cost != nil {
		label += " · " + compactCost(u.Cost)
	}
	return label
}

func compactCost(c *protocol.UsageCost) string {
	amount, ok := new(big.Rat).SetString(c.Amount)
	if !ok || amount.Sign() < 0 {
		return "Cost —"
	}
	prefix := c.Currency + " "
	if c.Currency == "USD" {
		prefix = "$"
	}
	if c.Estimated {
		prefix = "~" + prefix
	}
	if amount.Sign() > 0 && amount.Cmp(big.NewRat(1, 100)) < 0 {
		return prefix + "<0.01"
	}
	return prefix + amount.FloatString(2)
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
	if n >= 1_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000), ".0") + "k"
	}
	return fmt.Sprint(n)
}

// usageFact is one usage detail. Label is empty for free-form provenance
// notes, which never become label/value pairs.
type usageFact struct{ label, value string }

func (m *Model) usageLines() []string {
	var lines []string
	for _, f := range m.usageFacts() {
		if f.label == "" {
			lines = append(lines, f.value)
		} else {
			lines = append(lines, f.label+": "+f.value)
		}
	}
	return lines
}

func (m *Model) usageFacts() []usageFact {
	u := m.usageTelemetry()
	used, capacity, percentage := "unavailable", "unavailable", "unavailable"
	source := "no agent telemetry reported"
	var more []usageFact
	if u != nil {
		source = safe(u.Source)
		if source == "" {
			source = "unnamed agent source"
		}
		if m.compatibleUsage(u) {
			if u.Used >= 0 {
				used = fmt.Sprint(u.Used) + " tokens"
			}
			if u.Size > 0 {
				capacity = fmt.Sprint(u.Size) + " tokens"
			}
			if u.Used >= 0 && u.Size > 0 {
				percentage = fmt.Sprintf("%.1f%%", float64(u.Used)/float64(u.Size)*100)
			}
		} else {
			more = append(more, usageFact{"", "Context observation belongs to a previous model/session"})
		}
		if u.Model != "" {
			more = append(more, usageFact{"Model", safe(u.Model)})
		}
		if u.Scope != "" {
			more = append(more, usageFact{"Context scope", safe(u.Scope)})
		}
		if u.ReportedAt != "" {
			more = append(more, usageFact{"Observed", safe(u.ReportedAt)})
		}
	}
	lines := []usageFact{{"Context used", used}, {"Context capacity", capacity}, {"Context percentage", percentage}, {"Source", source}}
	lines = append(lines, more...)
	if u != nil && u.Cost != nil {
		kind := "Reported cost"
		if u.Cost.Estimated {
			kind = "Estimated cost"
		}
		lines = append(lines, usageFact{kind, safe(u.Cost.Currency) + " " + safe(u.Cost.Amount)}, usageFact{"Cost scope", safe(u.Cost.Scope)}, usageFact{"Cost observed", safe(u.Cost.ReportedAt)})
	} else {
		lines = append(lines, usageFact{"API cost", "unavailable"})
	}
	return append(lines, usageFact{"Billing mode", "unknown"}, usageFact{"Subscription limits", "unavailable"})
}

func (m *Model) openUsageSummary() {
	var items []menuItem
	// Facts are paired explicitly: a source or model name containing ": "
	// is never split into a pair.
	for _, f := range m.usageFacts() {
		if f.label != "" {
			items = append(items, pairMenuItem(f.label, f.value, action{Kind: "noop"}))
			continue
		}
		items = append(items, menuItem{Label: f.value, Action: action{Kind: "noop"}})
	}
	// A rule separates the facts from the one action row.
	m.showMenu("Usage", append(items, menuItem{Separator: true}, menuItem{Label: "Usage details", Action: action{Kind: "usage"}}))
}

// The compact percentage is clamped for layout; details preserve an over-capacity
// report exactly. Unknown remains distinct from a measured zero.
func usageGauge(used, capacity *int64) string {
	if used == nil || capacity == nil || *used < 0 || *capacity <= 0 {
		return "Ctx ?"
	}
	return fmt.Sprintf("Ctx %.0f%%", min(1.0, float64(*used)/float64(*capacity))*100)
}

// Context occupancy thresholds for the footer gauge ink. They apply only to
// compatible supplied telemetry; unknown occupancy stays muted.
const (
	usageWarnPercent     = 80
	usageCriticalPercent = 95
)

// usageTone is the footer tone for the context gauge: gold at high occupancy,
// red near capacity, otherwise muted.
func (m *Model) usageTone() string {
	u := m.usageTelemetry()
	if !m.compatibleUsage(u) || u.Used < 0 || u.Size <= 0 {
		return "muted"
	}
	switch pct := float64(u.Used) / float64(u.Size) * 100; {
	case pct >= usageCriticalPercent:
		return "red"
	case pct >= usageWarnPercent:
		return "gold"
	}
	return "muted"
}
