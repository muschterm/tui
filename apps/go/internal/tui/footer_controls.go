package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type composerControl struct {
	x, y, width            int
	label, key, help, tone string
	action                 action
	icon                   bool // glyph-only: no fill, hit on the glyph cells
}

// Shared by prompt text and the settings/action rows. Collapse only for tiny
// widths so the gutter can never remove the last usable input/control cell.
func composerInset(width int) int { return min(2, max(0, (width-1)/2)) }

// Both measurement and painting use this layout. Narrowing the footer only
// changes presentation: hidden controls keep their original application action.
func (m *Model) composerLayout(width int) (visible, overflow []composerControl) {
	width = max(1, width)
	left := composerInset(width)
	right := width - left
	available := right - left
	t := m.thread()
	selected := m.composerSelection()
	control := func(label, key, help, tone string, a action) composerControl {
		return composerControl{width: ansi.StringWidth(label) + 2, label: label, key: key, help: help, tone: tone, action: a}
	}
	var settings, actions []composerControl
	for _, s := range m.composerSettingDisplays(t, selected) {
		label, help, tone := s.label, title(s.field)+": "+s.label, "setting"
		settings = append(settings, control(label, "settings:"+s.field, help, tone, action{Kind: "settings", Value: s.field}))
		if m.configurationLocked() {
			settings[len(settings)-1].tone = "muted"
			settings[len(settings)-1].help += " · read-only during active work"
		}
	}
	if m.state.Edit != nil {
		settings = append(settings,
			control("Save edit", "save-edit", "Save queued edit", "blue", action{Kind: "send"}),
			control("Cancel", "cancel-edit", "Cancel queued edit", "gold", action{Kind: "cancel-edit"}))
	}
	if m.busy != nil {
		settings = append(settings, control("Retry", "retry", "Retry pending command", "gold", action{Kind: "retry"}))
	}
	if t.NeedsResume {
		settings = append(settings, control("Resume", "resume", "Resume saved work", "gold", action{Kind: "resume"}))
	}
	gauge, usageHelp := m.usageGauge()
	actions = append(actions,
		control(gauge+"  Cost —", "usage", usageHelp, "muted", action{Kind: "usage-summary"}),
		control(m.icon("attach"), "attach", "Attach context", "blue", action{Kind: "attach"}))
	actions[1].icon = true
	if activeTurn(t) {
		actions = append(actions, control(m.icon("stop"), "interrupt", "Stop", "red", action{Kind: "interrupt"}))
		actions[len(actions)-1].icon = true
	}
	actions = append(actions, control(m.icon("send"), "send", "Send · Enter", "blue", action{Kind: "send"}))
	actions[len(actions)-1].icon = true
	if reason := m.sendBlocked(); reason != "" {
		actions[len(actions)-1].tone, actions[len(actions)-1].help = "muted", reason
	}
	more := control(m.icon("more-vertical"), "composer-more", "More settings", "muted", action{Kind: "composer-more"})
	more.icon = true
	hidden := map[string]bool{}
	needed := func() int {
		n, settingsWidth := 0, 0
		for _, c := range settings {
			if !hidden[c.key] {
				settingsWidth += c.width
			}
		}
		if len(hidden) > 0 {
			settingsWidth += more.width
		}
		if settingsWidth > 0 {
			n += settingsWidth + 1 // Separate the left and right groups.
		}
		for _, c := range actions {
			n += c.width
		}
		return n
	}
	compactUsage := func(keep bool) {
		// Usage owns its own overflow at the start of the right-hand group.
		// Keep the same key/action so keyboard focus survives compaction.
		actions[0].label, actions[0].icon = m.icon("more-vertical"), !keep
		if keep {
			actions[0].label += " " + gauge
		}
		actions[0].width = ansi.StringWidth(actions[0].label) + 2
	}
	if needed() > available {
		compactUsage(true)
	}
	// Remove whole controls in a stable order, keeping the model and active
	// Stop longest. Recovery and differing running settings tint overflow gold.
	for _, key := range []string{"settings:permissions", "settings:context", "settings:speed", "settings:effective", "save-edit", "settings:effort", "usage", "settings:agent", "cancel-edit", "retry", "resume"} {
		if needed() <= available {
			break
		}
		if key == "usage" {
			compactUsage(false)
			continue
		}
		for _, c := range settings {
			if c.key == key {
				hidden[key] = true
			}
		}
	}
	if needed() > available {
		for i := range settings {
			c := &settings[i]
			if c.key != "settings:model" {
				continue
			}
			room := c.width - (needed() - available)
			if room >= 5 {
				c.width = room
				c.label = ansi.Truncate(c.label, room-2, "…")
			} else {
				hidden[c.key] = true
			}
		}
	}
	x := left
	for _, c := range settings {
		if hidden[c.key] {
			overflow = append(overflow, c)
			if c.tone == "gold" {
				more.tone = "gold"
			}
		} else {
			c.x = x
			x += c.width
			visible = append(visible, c)
		}
	}
	// Settings overflow stays adjacent to the fields it expands, not attached
	// to the right-hand actions. Usage never enters this menu.
	if len(overflow) > 0 {
		more.x, more.label = x, centered(more.label, more.width)
		visible = append(visible, more)
	}
	x = right
	var rightControls []composerControl
	for i := len(actions) - 1; i >= 0; i-- {
		c := actions[i]
		c.width = min(c.width, x-left)
		if c.width <= 0 {
			continue // Below the supported terminal minimum, retain Send first.
		}
		x -= c.width
		c.x, c.label = x, centered(c.label, c.width)
		rightControls = append(rightControls, c)
	}
	leftControls := visible
	visible = nil
	for _, c := range leftControls {
		c.width = min(c.width, max(0, x-1-c.x))
		if c.width > 0 {
			visible = append(visible, c)
		}
	}
	for i := len(rightControls) - 1; i >= 0; i-- {
		visible = append(visible, rightControls[i])
	}
	return visible, overflow
}

func (m *Model) composerControls(width int) ([]composerControl, int) {
	visible, _ := m.composerLayout(width)
	return visible, 1
}

func (m *Model) composerControlsHeight(_ int) int { return 1 }

func (m *Model) openComposerOverflow() {
	// Snapshot the menu while it is open: resize and incoming activity must not
	// move the selected item under a pending click or Enter key.
	_, hidden := m.composerLayout(max(1, m.measure().geom.Center.W-2))
	var items []menuItem
	for _, c := range hidden {
		items = append(items, menuItem{c.help, c.action})
	}
	if len(items) > 0 {
		m.showMenu("More settings", items)
	}
}

// usageTelemetry is the selected thread's agent-supplied context measurement.
// A missing or incoherent report stays unavailable rather than being repaired.
func (m *Model) usageTelemetry() *protocol.Usage {
	u := m.thread().Usage
	if u == nil || u.Size <= 0 || u.Used < 0 {
		return nil
	}
	return u
}

// usageGauge renders the context gauge and its help from reported telemetry.
func (m *Model) usageGauge() (string, string) {
	u := m.usageTelemetry()
	if u == nil {
		return usageGauge(nil, nil), "Context usage / billing / cost · unavailable"
	}
	source := safe(u.Source)
	if source == "" {
		source = "the agent"
	}
	return usageGauge(&u.Used, &u.Size), fmt.Sprintf("Context %d of %d reported by %s · billing and cost unavailable", u.Used, u.Size, source)
}

// usageLines are the requested usage fields. Occupancy comes only from agent
// telemetry; percentages, money and quota windows are never derived here.
func (m *Model) usageLines() []string {
	u := m.usageTelemetry()
	used, capacity, percentage := "unavailable", "unavailable", "unavailable"
	source := "no agent telemetry reported"
	if u != nil {
		used, capacity = fmt.Sprint(u.Used), fmt.Sprint(u.Size)
		percentage = fmt.Sprintf("%.0f%%", min(1.0, float64(u.Used)/float64(u.Size))*100)
		source = safe(u.Source)
		if source == "" {
			source = "unnamed agent source"
		}
		if reported := safe(u.ReportedAt); reported != "" {
			source += " · reported " + reported
		}
	}
	return []string{
		"Context used: " + used,
		"Context capacity: " + capacity,
		"Context percentage: " + percentage,
		"Source: " + source,
		"Billing mode: unknown",
		"Subscription limits: unavailable",
		"API cost: unavailable",
	}
}

// Billing and quota telemetry is not supplied by this protocol. Keep every
// requested field explicit without deriving money or windows from transcripts.
func (m *Model) openUsageSummary() {
	var items []menuItem
	for _, line := range m.usageLines() {
		items = append(items, menuItem{line, action{Kind: "noop"}})
	}
	m.showMenu("Usage", append(items, menuItem{"Usage details", action{Kind: "usage"}}))
}

func (m *Model) renderComposerControls(f *frame, r shell.Rect, y int) int {
	controls, height := m.composerControls(r.W)
	p := m.colors()
	for _, c := range controls {
		fg := p.violet
		switch c.tone {
		case "blue":
			fg = p.blue
		case "red":
			fg = p.red
		case "gold":
			fg = p.gold
		case "muted":
			fg = p.muted
		}
		if c.icon {
			f.iconButton(m, r.X+c.x, y+c.y, c.width, centered(c.label, c.width), c.key, c.action, fg, p.canvas)
		} else {
			f.button(m, r.X+c.x, y+c.y, c.width, c.label, c.key, c.action, fg, p.canvas)
		}
		f.hits[len(f.hits)-1].Label = c.help
	}
	return y + height
}

// usageGauge accepts compatible occupancy/capacity measurements only. Unknown
// values remain distinct from a valid zero. No production telemetry is supplied
// by the current protocol; callers must not estimate occupancy from transcript.
func usageGauge(used, capacity *int64) string {
	if used == nil || capacity == nil || *used < 0 || *capacity <= 0 {
		return "[····] —"
	}
	ratio := min(1.0, float64(*used)/float64(*capacity))
	filled := int(math.Round(ratio * 4))
	return "[" + strings.Repeat("━", filled) + strings.Repeat("·", 4-filled) + "] " + fmt.Sprintf("%.0f%%", ratio*100)
}
