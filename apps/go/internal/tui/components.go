package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type componentVariant uint8

const (
	roundedOutline componentVariant = iota
	squareOutline
	squareFill
)

type componentRole uint8

const (
	normalControl componentRole = iota
	primaryControl
)

// Selection belongs to application state; hover and focus are independent inputs.
// A primary action is not a selected item. Disabled controls ignore interaction.
type componentState struct {
	Selected, Hovered, Focused, Disabled bool
	Role                                 componentRole
}

type componentVisual struct {
	foreground, background, border string
	bold, underline                bool
}

func (m *Model) controlState(selected bool, keys ...string) componentState {
	s := componentState{Selected: selected}
	for _, key := range keys {
		s.Hovered = s.Hovered || m.hover == key
		s.Focused = s.Focused || m.focus == key
	}
	return s
}

func (m *Model) hoverFill() string {
	switch m.colorProfile {
	case colorprofile.TrueColor:
		if m.state.Light {
			return "#d8dee9"
		}
		return "#353c4d"
	case colorprofile.ANSI256:
		if m.state.Light {
			return "252"
		}
		return "237"
	default:
		if m.state.Light {
			return "15"
		}
		return "8"
	}
}

// Resolve visual state without inspecting previously chosen colors. Selection
// wins over hover; focus adds a text cue without changing persistent selection.
func (m *Model) componentStyle(variant componentVariant, s componentState, fg, bg string) componentVisual {
	p := m.colors()
	v := componentVisual{foreground: fg, background: bg, border: p.line}
	if s.Disabled {
		v.foreground = p.muted
		return v
	}
	if s.Role == primaryControl {
		v.foreground = p.blue
	}
	if s.Hovered || s.Focused {
		v.border = p.muted
		if variant == squareFill {
			v.background = m.hoverFill()
		}
	}
	if s.Selected {
		v.border, v.bold = p.blue, true
		if variant == squareFill {
			v.background = p.selected
			v.foreground = p.blue
		}
	}
	// Neutral ANSI fills can coincide. Bold/underline preserve state identity,
	// and neutral foreground avoids terminal-defined blue-on-gray legibility loss.
	if m.colorProfile <= colorprofile.ANSI && variant == squareFill && (s.Selected || s.Hovered || s.Focused) {
		v.foreground = p.text
	}
	v.underline = s.Focused
	return v
}

func (f *frame) componentText(x, y, width int, label string, v componentVisual) {
	if f.rows != nil && width > 0 {
		f.put(shell.Rect{X: x, Y: y, W: width, H: 1}, style(v.foreground, v.background).Bold(v.bold).Underline(v.underline).Render(fit(safe(label), width)))
	}
}

func (f *frame) styledButton(x, y, width int, label, key string, a action, v componentVisual) {
	if width <= 0 {
		return
	}
	f.componentText(x, y, width, label, v)
	f.hits = append(f.hits, hit{shell.Rect{X: x, Y: y, W: width, H: 1}, a, label, key})
}

// The end cells are always reserved. Low-color/plain fallbacks add visible
// brackets without changing width, icon slots, or the one-row hit rectangle.
func (f *frame) compactControl(m *Model, x, y, width int, label string, v componentVisual) {
	if width < 3 {
		return
	}
	left, right := " ", " "
	if m.colorProfile <= colorprofile.ANSI || m.plainIcons {
		left, right = "[", "]"
	}
	f.componentText(x, y, width, left+fit(safe(label), width-2)+right, v)
}

func (f *frame) compactButton(m *Model, x, y, width int, label, key string, a action, selected bool, role componentRole) {
	if width < 3 {
		return
	}
	p := m.colors()
	s := m.controlState(selected, key)
	s.Role = role
	v := m.componentStyle(squareFill, s, p.text, p.input)
	f.compactControl(m, x, y, width, centered(ansi.Truncate(label, width-2, "…"), width-2), v)
	f.hits = append(f.hits, hit{shell.Rect{X: x, Y: y, W: width, H: 1}, a, label, key})
}

func componentBorder(variant componentVariant, plain bool) lipgloss.Border {
	if plain {
		return lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"}
	}
	if variant == squareOutline {
		return lipgloss.NormalBorder()
	}
	return lipgloss.RoundedBorder()
}

// Only interior cells receive the stable fill. Border/corner cells belong to
// the surrounding surface; interaction never adds a square background there.
func (f *frame) componentBox(m *Model, r shell.Rect, variant componentVariant, v componentVisual, outer string) {
	if r.W < 2 || r.H < 2 {
		return
	}
	b := componentBorder(variant, m.plainIcons)
	f.text(r.X, r.Y, r.W, b.TopLeft+strings.Repeat(b.Top, r.W-2)+b.TopRight, v.border, outer)
	for y := r.Y + 1; y < r.Y+r.H-1; y++ {
		f.text(r.X, y, 1, b.Left, v.border, outer)
		f.text(r.X+1, y, r.W-2, strings.Repeat(" ", r.W-2), v.foreground, v.background)
		f.text(r.X+r.W-1, y, 1, b.Right, v.border, outer)
	}
	f.text(r.X, r.Y+r.H-1, r.W, b.BottomLeft+strings.Repeat(b.Bottom, r.W-2)+b.BottomRight, v.border, outer)
}
