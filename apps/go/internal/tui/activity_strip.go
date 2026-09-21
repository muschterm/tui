package tui

import (
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type activityTick struct{}

// One bounded animation clock drives all visible indicators. It does not dirty
// persisted view state, and stops when no visible work is running.
func (m *Model) activityAnimating() bool {
	if m.settingsPage != "" {
		return false
	}
	if !m.connected || m.terminalTooSmall() || len(m.menu) > 0 {
		return false
	}
	t := m.thread()
	if m.conversationVisible() && activeTurn(t) && (t.State == "running" || agentSummary(t).Working || planSummary(t).Working) {
		return true
	}
	left := m.workspaceGeometry(m.footerHeight()).Left
	rows, closed := m.navigationSections()
	viewport, _, _ := m.navigationLayout(left, len(rows), len(closed))
	visible := viewport.H
	if left.W == 0 || visible == 0 || m.settingsPage != "" {
		return false
	}
	start := min(max(0, m.navScroll), max(0, len(rows)-visible))
	for _, row := range rows[start:min(len(rows), start+visible)] {
		if row.kind == "thread" && !row.thread.Closed && threadIndicator(row.thread) == threadWorking {
			return true
		}
	}
	return false
}

func (m *Model) nextActivityTick() tea.Cmd {
	if m.activityTickPending || !m.activityAnimating() {
		return nil
	}
	m.activityTickPending = true
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return activityTick{} })
}

func (m *Model) reconcileActivityDismissals(next protocol.Snapshot) {
	for _, t := range next.Threads {
		v := m.state.Threads[t.ID]
		if v == nil {
			continue
		}
		newTurn := false
		for _, old := range m.snapshot.Threads {
			if old.ID == t.ID {
				newTurn = !activeTurn(old) && activeTurn(t)
				break
			}
		}
		if v.DismissedAgents != "" && (!agentSummary(t).Dismissible || newTurn) {
			v.DismissedAgents = ""
			m.markDirty()
		}
		if v.DismissedPlan != "" && (!planSummary(t).Dismissible || newTurn) {
			v.DismissedPlan = ""
			m.markDirty()
		}
	}
}

type activityControl struct {
	kind        string
	x, y, width int
	summary     activitySummary
}

func (m *Model) activityControls(width int) ([]activityControl, int) {
	t, v := m.thread(), m.viewState()
	var controls []activityControl
	x, y := 0, 0
	add := func(kind string, s activitySummary) {
		if s.Label == "" {
			return
		}
		w := ansi.StringWidth(s.Label) + 4
		w = min(max(1, width), w)
		if x > 0 && x+w > width {
			y++
			x = 0
		}
		controls = append(controls, activityControl{kind, x, y, w, s})
		x += w + 1
	}
	if activeTurn(t) {
		s := activitySummary{Label: "Thinking…", State: "working", Working: t.State == "running"}
		if t.State == "waiting" {
			s.Label, s.State = "Waiting…", "waiting"
		}
		if !m.connected {
			s.Label, s.State, s.Working = "Connection lost", "unknown", false
		}
		add("thinking", s)
	}
	if s := planSummary(t); s.Key != v.DismissedPlan {
		add("plan", s)
	}
	if s := agentSummary(t); s.Key != v.DismissedAgents {
		add("agents", s)
	}
	if len(controls) == 0 {
		return nil, 0
	}
	return controls, y + 1
}

func (m *Model) activityStripHeight(width int) int {
	_, height := m.activityControls(width)
	return height
}

func (m *Model) activityColor(s activitySummary) string {
	p := m.colors()
	if s.State == "completed" {
		return p.green
	}
	if s.State == "failed" {
		return p.red
	}
	if s.Working && m.connected {
		if m.colorProfile != colorprofile.TrueColor {
			// Indexed palettes cannot interpolate RGB. Keep a visible blue
			// indicator throughout a stepped dim/bright pulse instead.
			if m.activityPhase < 6 {
				if m.colorProfile == colorprofile.ANSI256 {
					if m.state.Light {
						return "67"
					}
					return "68"
				}
				if m.state.Light {
					return "12"
				}
				return "4"
			}
			return p.blue
		}
		// A triangular brightness cycle avoids abrupt on/off blinking.
		phase := m.activityPhase
		if phase > 6 {
			phase = 12 - phase
		}
		a, _ := strconv.ParseUint(p.blue[1:], 16, 32)
		b, _ := strconv.ParseUint(p.panel[1:], 16, 32)
		var rgb uint64
		for _, shift := range []uint{16, 8, 0} {
			weight := 4 + phase
			channel := (int((a>>shift)&255)*weight + int((b>>shift)&255)*(10-weight)) / 10
			rgb |= uint64(channel) << shift
		}
		return fmt.Sprintf("#%06x", rgb)
	}
	if s.State == "waiting" || s.State == "interrupted" || s.State == "paused" {
		return p.gold
	}
	return p.muted
}

func (m *Model) renderActivityStrip(f *frame, r shell.Rect) int {
	controls, height := m.activityControls(r.W)
	p := m.colors()
	f.fill(shell.Rect{X: r.X, Y: r.Y, W: r.W, H: height}, p, p.panel)
	for _, c := range controls {
		x, y, w, s := r.X+c.x, r.Y+c.y, c.width, c.summary
		bg := p.panel
		closeKey := "dismiss-" + c.kind
		engaged := m.hover == c.kind || m.focus == c.kind || m.hover == closeKey || m.focus == closeKey
		if engaged && c.kind != "thinking" {
			bg = m.hoverFill()
		}
		if c.kind != "thinking" {
			// The label always inspects history. The leading icon is a separate
			// keyboard target only once successful completion permits dismissal.
			labelX, labelWidth := x, w
			label := "   " + s.Label
			if s.Dismissible {
				labelX, labelWidth, label = x+2, w-2, " "+s.Label
			}
			if labelWidth > 0 {
				f.button(m, labelX, y, labelWidth, label, c.kind, action{Kind: "open", Value: c.kind}, p.text, bg)
				f.hits[len(f.hits)-1].Label = fmt.Sprintf("Open %s · %s · %d/%d completed · %d working", title(c.kind), s.State, s.Completed, s.Total, s.WorkingCount)
			}
		} else {
			f.text(x, y, w, "   "+s.Label, p.text, bg)
		}
		circle := "●"
		if m.plainIcons {
			circle = "o"
		}
		if s.Dismissible && w > 1 {
			if engaged {
				circle = m.icon("close")
			}
			f.button(m, x+1, y, 1, centered(circle, 1), closeKey, action{Kind: closeKey, ID: s.Key}, m.activityColor(s), bg)
			f.hits[len(f.hits)-1].Label = "Dismiss completed " + c.kind + " summary · history is preserved"
			// Keep the completion color when the generic button adds focus styling.
			f.text(x+1, y, 1, circle, m.activityColor(s), bg)
		} else {
			f.text(x+1, y, min(1, w-1), circle, m.activityColor(s), bg)
		}
	}
	return r.Y + height
}
