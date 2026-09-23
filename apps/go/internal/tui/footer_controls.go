package tui

import (
	"github.com/charmbracelet/x/ansi"
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
	actions = append(actions,
		control(m.usageCompactLabel(), "usage", "Open usage details", "muted", action{Kind: "usage-summary"}),
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
			actions[0].label += " " + m.usageCompactLabel()
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
			f.iconButton(m, r.X+c.x, y+c.y, c.width, centered(c.label, c.width), c.key, c.action, fg, p.input)
		} else {
			f.button(m, r.X+c.x, y+c.y, c.width, c.label, c.key, c.action, fg, p.input)
		}
		f.hits[len(f.hits)-1].Label = c.help
	}
	return y + height
}
