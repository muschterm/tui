package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func (m *Model) queueVisibleRows() int {
	limit := 2
	if m.compact() {
		limit = 1
	}
	return min(limit, len(m.thread().Queue))
}

func (m *Model) queueHeight() int {
	if rows := m.queueVisibleRows(); rows > 0 {
		return rows + 2 // Title in the top border, preview rows, bottom border.
	}
	return 0
}

func queueControlKey(key string) bool {
	return key == "queue" || strings.HasPrefix(key, "queue-row:") ||
		strings.HasPrefix(key, "steer:") ||
		strings.HasPrefix(key, "edit:") || strings.HasPrefix(key, "remove:") ||
		strings.HasPrefix(key, "up:") || strings.HasPrefix(key, "down:")
}

func (m *Model) renderQueue(f *frame, r shell.Rect) int {
	if r.H == 0 {
		return r.Y
	}
	p := m.colors()
	queue := m.thread().Queue
	state := componentState{Hovered: queueControlKey(m.hover), Focused: queueControlKey(m.focus)}
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, state, p.text, p.panel), p.canvas)
	x, w := r.X+2, max(1, r.W-4)
	header := fmt.Sprintf(" %d queued ", len(queue))
	if hidden := len(queue) - m.queueVisibleRows(); hidden > 0 {
		header = fmt.Sprintf(" %d queued · %d more… ", len(queue), hidden)
	}
	f.button(m, x, r.Y, min(w, ansi.StringWidth(header)), header, "queue", action{Kind: "queue"}, p.gold, p.panel)
	f.hits[len(f.hits)-1].Label = "Open all queued messages and controls"
	for i, q := range queue[:m.queueVisibleRows()] {
		y := r.Y + 1 + i
		// The preview and its actions share one row. Leave a gutter between
		// content and controls; previews never wrap into another message's row.
		editLabel, removeLabel, editWidth, removeWidth := "Edit", "Remove", 6, 8
		if w < 64 {
			editLabel, removeLabel, editWidth, removeWidth = m.icon("compose"), m.icon("trash"), 3, 3
			if m.plainIcons {
				editLabel = "e"
			}
		}
		controlsWidth := 7 + editWidth + removeWidth + 3 + 3 + 4
		textWidth := max(1, w-controlsWidth-1)
		preview := strings.Join(strings.Fields(safe(q.Text)), " ")
		if preview == "" {
			preview = "Context attachments"
		}
		f.button(m, x, y, textWidth, preview, "queue-row:"+q.ID, action{Kind: "queue"}, p.text, p.panel)
		bx := x + textWidth + 1
		role, help := primaryControl, "Steer this message into the active turn"
		if reason := m.steerBlocked(q); reason != "" {
			role, help = normalControl, "Steer unavailable: "+reason
		}
		f.compactButton(m, bx, y, 7, "Steer", "steer:"+q.ID, m.steerAction(q.ID), false, role)
		f.hits[len(f.hits)-1].Label = help
		bx += 8
		f.compactButton(m, bx, y, editWidth, editLabel, "edit:"+q.ID, action{Kind: "edit", ID: q.ID}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Edit queued message"
		bx += editWidth + 1
		f.compactButton(m, bx, y, removeWidth, removeLabel, "remove:"+q.ID, action{Kind: "remove", ID: q.ID}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Remove queued message"
		bx += removeWidth + 1
		for _, move := range []struct {
			x, delta   int
			key, label string
		}{{bx, -1, "up:", "↑"}, {bx + 4, 1, "down:", "↓"}} {
			key := move.key + q.ID
			if i+move.delta >= 0 && i+move.delta < len(queue) {
				f.compactButton(m, move.x, y, 3, move.label, key, action{Kind: "move", ID: q.ID, Index: move.delta}, false, normalControl)
				f.hits[len(f.hits)-1].Label = "Move queued message " + strings.TrimSuffix(move.key, ":")
			} else {
				// Keep boundary arrows in place, visibly disabled and without a hit.
				v := m.componentStyle(squareFill, componentState{Disabled: true}, p.text, p.input)
				f.compactControl(m, move.x, y, 3, move.label, v)
			}
		}
	}
	return r.Y + r.H
}
