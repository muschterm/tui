package tui

import (
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// panel.go holds form-panel constructs shared across surfaces: settings is
// the first consumer, but any host may paint a section heading, rule,
// label/value pair, banded square-fill button/tile, segmented choice row or
// toggle switch through these helpers. See "Settings panel constructs" in
// docs/design/components.md.

// panelSectionHeading renders an uppercase, bold, muted section heading that
// separates groups of controls in a form panel.
func panelSectionHeading(f *frame, m *Model, x, y, width int, text string) {
	panelSectionHeadingOn(f, m, x, y, width, text, m.colors().canvas)
}

// panelSectionHeadingOn is panelSectionHeading over another host background,
// such as the right host's panel or a dialog's interior.
func panelSectionHeadingOn(f *frame, m *Model, x, y, width int, text, bg string) {
	p := m.colors()
	v := m.componentStyle(squareFill, componentState{}, p.muted, bg)
	v.bold = true
	f.componentText(x, y, width, strings.ToUpper(text), v)
}

// panelRule renders a full-width horizontal rule in the line color, separating
// sections of a form panel.
func panelRule(f *frame, m *Model, x, y, width int) {
	panelRuleOn(f, m, x, y, width, m.colors().canvas)
}

// panelRuleOn is panelRule over another host background.
func panelRuleOn(f *frame, m *Model, x, y, width int, bg string) {
	line := "─"
	if m.plainIcons {
		line = "-"
	}
	f.text(x, y, width, strings.Repeat(line, max(0, width)), m.colors().line, bg)
}

// panelPairRow renders a read-only label/value row: a muted label at the left
// and its bright value flush right; the value keeps at least half the row
// when both cannot fit.
func panelPairRow(f *frame, m *Model, x, y, width int, label, value string) {
	p := m.colors()
	panelPairRowStyled(f, x, y, width, label, value, p.muted, p.text, p.canvas)
}

// panelPairRowStyled is panelPairRow with explicit label, value and background
// inks, so a selectable row can paint its hover or selected fill underneath.
func panelPairRowStyled(f *frame, x, y, width int, label, value, labelInk, valueInk, bg string) {
	// The value keeps at least half the row, and more when the label is short.
	room := max(1, width/2, width-ansi.StringWidth(label)-2)
	rendered := ansi.Truncate(singleLine(value), room, "…")
	vw := ansi.StringWidth(rendered)
	f.text(x, y, width, "", valueInk, bg)
	f.text(x, y, max(0, width-vw-2), label, labelInk, bg)
	f.text(x+width-vw, y, vw, rendered, valueInk, bg)
}

const toggleTrackWidth = 6

// toggleTrack returns the switch cells: a track of background fills with a
// two-cell knob at the left (off) or right (on). Without colors the reserved
// end cells hold brackets and the knob is a glyph.
func (m *Model) toggleTrack(on bool) string {
	p := m.colors()
	if m.colorProfile < colorprofile.ANSI {
		if on {
			return "[===o]"
		}
		return "[o   ]"
	}
	track, knob := style(p.text, p.line), style(p.text, p.muted)
	if on {
		track, knob = style(p.text, p.blue), style(p.text, m.knobFill())
	}
	inner := strings.Repeat(" ", toggleTrackWidth-2)
	if on {
		return track.Render(inner) + knob.Render("  ")
	}
	return knob.Render("  ") + track.Render(inner)
}

// knobFill is the bright knob of an enabled switch, set against the accent.
// The dark theme's canvas is already dark, so a bright literal keeps full
// contrast there; the light theme's canvas is pale, so a bright literal knob
// nearly vanished against it. The light theme instead borrows its own
// canvas-contrasting text ink, which reads clearly against both the pale
// canvas and the accent track.
func (m *Model) knobFill() string {
	if m.state.Light {
		return m.colors().text
	}
	switch m.colorProfile {
	case colorprofile.TrueColor:
		return "#ffffff"
	case colorprofile.ANSI256:
		return "231"
	default:
		return "15"
	}
}

const panelSegmentGap = 2

// panelSegmentLayout splits width into n equal cells separated by a two-cell
// gap; the last cell takes any remainder so the group spans the full width.
func panelSegmentLayout(x, width, n int) (xs, ws []int) {
	w := (width - panelSegmentGap*(n-1)) / n
	for i := range n {
		xs = append(xs, x+i*(w+panelSegmentGap))
		ws = append(ws, w)
	}
	if n > 0 {
		ws[n-1] = x + width - xs[n-1]
	}
	return xs, ws
}

// panelSegmentsFit reports whether every label fits its equal-width cell, with
// at least one cell of inset, at the given layout width.
func panelSegmentsFit(width int, labels []string) bool {
	_, ws := panelSegmentLayout(0, width, len(labels))
	for i, label := range labels {
		if ws[i] < ansi.StringWidth(label)+2 {
			return false
		}
	}
	return true
}

// panelBandsSupported reports whether banded square fills may use half-block
// edges: they need separable neutral fills and the Unicode symbol set.
// Callers fall back to a single bracketed `[ Label ]` row when this is false.
func panelBandsSupported(m *Model) bool {
	return m.colorProfile >= colorprofile.ANSI256 && !m.plainIcons
}

// paintPanelBandEdge paints one banded half-block edge row (the "▄" row above
// or "▀" row below a label row) in the control's fill over the canvas, for a
// control spanning bandRows rows at band index band (-1 top edge, 0 label
// row, 1 bottom edge; bandRows == 1 means no banding). It reports whether it
// painted an edge, so callers skip label/content painting on non-edge rows.
func (f *frame) paintPanelBandEdge(x, y, width, band, bandRows int, v componentVisual) bool {
	if bandRows == 1 || band == 0 {
		return false
	}
	edge := "▄"
	if band == 1 {
		edge = "▀"
	}
	f.text(x, y, width, strings.Repeat(edge, width), v.background, v.base)
	return true
}

// registerPanelBandHit registers one hit rectangle for a banded control (once,
// on its first row) or one per row for an unbanded control, clipped to clip
// (the scrollable body the control is painted in). band and bandRows describe
// the control's band geometry as in paintPanelBandEdge.
func (f *frame) registerPanelBandHit(clip shell.Rect, y, band, bandRows int, first bool, h hit) {
	if !first && band != -1 && bandRows != 1 {
		return
	}
	top := y - (band + 1)
	if bandRows == 1 {
		top = y
	}
	y0, y1 := max(top, clip.Y), min(top+bandRows, clip.Y+clip.H)
	h.Rect.Y, h.Rect.H = y0, y1-y0
	f.hits = append(f.hits, h)
}
