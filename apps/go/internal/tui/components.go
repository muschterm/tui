package tui

import (
	"fmt"
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
	// base is the surrounding background before any hover or selection fill;
	// a focus mark painted beside the control's fill uses it. mark and markInk
	// are the keyboard-focus glyph and its accent.
	base, mark, markInk string
	bold, focused       bool
}

func (m *Model) controlState(selected bool, keys ...string) componentState {
	s := componentState{Selected: selected}
	for _, key := range keys {
		s.Hovered = s.Hovered || m.hover == key
		s.Focused = s.Focused || m.focus == key
	}
	return s
}

// dimmed recedes below muted for Closed navigation while staying legible.
func (m *Model) dimmed() string {
	switch m.colorProfile {
	case colorprofile.TrueColor:
		if m.state.Light {
			return "#8790a7"
		}
		return "#67738f"
	case colorprofile.ANSI256:
		if m.state.Light {
			return "244"
		}
		return "242"
	default:
		return "8"
	}
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
	v := componentVisual{foreground: fg, background: bg, base: bg, border: p.line, mark: m.icon("focus"), markInk: p.blue}
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
	v.focused = s.Focused
	return v
}

// containerStyle resolves a rounded container's frame, such as the pending
// question card or an answered history card: the rest outline on all four
// sides, or the focused outline (the prompt's focused ink) while keyboard
// focus is inside the container. Pointer hover over the container or any of
// its controls never recolors the frame, and focus moving between its inner
// controls keeps it unchanged; those controls carry their own state cues.
func (m *Model) containerStyle(focusInside bool, fg, bg string) componentVisual {
	v := m.componentStyle(roundedOutline, componentState{Focused: focusInside}, fg, bg)
	v.focused = false // The frame has no focus mark; its focused control does.
	return v
}

// Icon controls have no fill. Hover and focus embolden the glyph and lift muted
// ink to text, as T3's muted-to-foreground row actions do; focus adds the
// leading mark. Bold alone is unreliable for Nerd Font glyphs, whose outlines
// are identical in the Bold face, so the ink lift carries the feedback there.
func (m *Model) iconStyle(s componentState, fg, bg string) componentVisual {
	p := m.colors()
	v := componentVisual{foreground: fg, background: bg, base: bg, mark: m.icon("focus"), markInk: p.blue}
	if s.Disabled {
		v.foreground = p.muted
		return v
	}
	if s.Hovered || s.Focused {
		v.bold = true
		v.foreground = m.lift(fg)
	}
	if s.Selected {
		v.foreground, v.bold = p.blue, true
	}
	v.focused = s.Focused
	return v
}

// lift moves hovered icon ink toward the text color: muted becomes text, and
// accent colors brighten (dark theme) or deepen (light theme) halfway while
// keeping their hue. Low-color profiles have no midpoint and use text.
func (m *Model) lift(fg string) string {
	p := m.colors()
	if fg == p.text || fg == p.muted || m.colorProfile != colorprofile.TrueColor {
		return p.text
	}
	a, b := parseHex(fg), parseHex(p.text)
	if a == nil || b == nil {
		return p.text
	}
	var out [3]int
	for i := range out {
		out[i] = (a[i] + b[i]) / 2
	}
	return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
}

func parseHex(s string) []int {
	if len(s) != 7 || s[0] != '#' {
		return nil
	}
	var out []int
	for i := 1; i < 7; i += 2 {
		var v int
		if _, err := fmt.Sscanf(s[i:i+2], "%02x", &v); err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

// iconButton paints a padded glyph label across its reserved slot, but only
// the glyph's own cells are interactive: the slot keeps chrome spacing stable
// while the leading padding neither hovers nor activates. One trailing cell
// joins the target because wide Nerd Font variants draw a one-cell glyph
// about half a cell past its advance, so that cell is visibly icon there.
func (f *frame) iconButton(m *Model, x, y, w int, label, key string, a action, fg, bg string) {
	if w <= 0 {
		return
	}
	label = fit(singleLine(label), w)
	glyph := strings.TrimSpace(label)
	lead := ansi.StringWidth(label[:strings.Index(label, glyph)])
	gw := ansi.StringWidth(glyph)
	slot := shell.Rect{X: x, Y: y, W: w, H: 1}
	f.text(x, y, w, label, fg, bg)
	if gw == 0 {
		// A hidden glyph keeps its key and slot so focus can still reveal it,
		// at the cells a centered one-cell glyph would occupy.
		lead, gw = (w-1)/2, 1
	}
	target := shell.Rect{X: x + lead, Y: y, W: min(gw+1, w-lead), H: 1}
	if glyph != "" {
		v := m.iconStyle(m.controlState(false, key), fg, bg)
		// The mark takes the padding cell before the glyph inside the slot. A
		// slot with no leading padding keeps the underline across the target.
		underline := v.focused && lead == 0
		f.styledText(target.X, y, target.W, glyph, v, underline)
		if v.focused && lead > 0 {
			f.focusMark(x+lead-1, y, v, bg)
		}
	}
	f.hits = append(f.hits, hit{Rect: target, Action: a, Label: glyph, Key: key, Slot: slot})
}

func (f *frame) componentText(x, y, width int, label string, v componentVisual) {
	f.styledText(x, y, width, label, v, false)
}

func (f *frame) styledText(x, y, width int, label string, v componentVisual, underline bool) {
	if f.rows != nil && width > 0 {
		f.put(shell.Rect{X: x, Y: y, W: width, H: 1}, style(v.foreground, v.background).Bold(v.bold).Underline(underline).Render(fit(singleLine(label), width)))
	}
}

// focusMark paints the keyboard-focus glyph into one cell the focused control
// does not use for content, so focusing never moves an icon or label.
func (f *frame) focusMark(x, y int, v componentVisual, bg string) {
	f.text(x, y, 1, v.mark, v.markInk, bg)
}

// blank reports whether a painted cell holds only a space, so a focus mark can
// take it without covering a neighbour's content. Nothing is blank while
// measuring: hits are unaffected and no mark is painted then anyway.
func (f *frame) blank(x, y int) bool {
	if f.rows == nil || y < 0 || y >= len(f.rows) || x < 0 || x >= ansi.StringWidth(f.rows[y]) {
		return false
	}
	return ansi.Strip(cutCells(f.rows[y], x, x+1)) == " "
}

// A text control has no reserved end cells: the mark takes the blank cell
// before it when its container leaves one, otherwise focus underlines the
// label as before.
func (f *frame) styledButton(x, y, width int, label, key string, a action, v componentVisual) {
	if width <= 0 {
		return
	}
	marked := v.focused && f.blank(x-1, y)
	f.styledText(x, y, width, label, v, v.focused && !marked)
	if marked {
		f.focusMark(x-1, y, v, v.base)
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: a, Label: label, Key: key})
}

// The end cells are always reserved. Low-color/plain fallbacks add visible
// brackets without changing width, icon slots, or the one-row hit rectangle.
// Keyboard focus replaces the leading end cell with the focus mark.
func (f *frame) compactControl(m *Model, x, y, width int, label string, v componentVisual) {
	if width < 3 {
		return
	}
	left, right := " ", " "
	if m.colorProfile <= colorprofile.ANSI || m.plainIcons {
		left, right = "[", "]"
	}
	f.componentText(x, y, width, left+fit(singleLine(label), width-2)+right, v)
	if v.focused {
		f.focusMark(x, y, v, v.background)
	}
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
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: a, Label: label, Key: key})
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
