package tui

import (
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// paintLineAccents repaints a row's lead status mark and trailing suffix in
// their own inks over the row already painted at x with visual v, keeping its
// background, bold and underline. The lead is found after leading spaces; the
// tail is repainted only when the whole row fits, so a truncated row keeps a
// single ink rather than tinting part of an ellipsis.
func (f *frame) paintLineAccents(x, y, width int, line contentLine, v componentVisual, underline bool) {
	if f.rows == nil || width <= 0 {
		return
	}
	text := singleLine(line.text)
	if line.lead != "" && line.leadFG != "" {
		pad := ansi.StringWidth(text) - ansi.StringWidth(strings.TrimLeft(text, " "))
		if strings.HasPrefix(text[len(text)-len(strings.TrimLeft(text, " ")):], line.lead) {
			lw := ansi.StringWidth(line.lead)
			if pad+lw <= width {
				f.accent(x+pad, y, lw, line.lead, line.leadFG, v, underline)
			}
		}
	}
	if line.tail != "" && line.tailFG != "" && strings.HasSuffix(text, line.tail) {
		tw, total := ansi.StringWidth(line.tail), ansi.StringWidth(text)
		if total <= width {
			f.accent(x+total-tw, y, tw, line.tail, line.tailFG, v, underline)
			if sw := ansi.StringWidth(line.sep); line.sep != "" && line.sepFG != "" && strings.HasSuffix(text, line.sep+line.tail) {
				f.accent(x+total-tw-sw, y, sw, line.sep, line.sepFG, v, underline)
			}
		}
	}
}

func (f *frame) accent(x, y, width int, s, fg string, v componentVisual, underline bool) {
	f.put(shell.Rect{X: x, Y: y, W: width, H: 1}, style(fg, v.background).Bold(v.bold).Underline(underline).Render(s))
}

// attachmentsButton is the composer's aggregate attachment control. Rich
// color keeps its fill row with the label at x; ANSI, NoTTY and plain-icon
// fallbacks use the compact control's reserved end cells as [ ] brackets over
// the same span and one-row hit rectangle.
func (m *Model) attachmentsButton(f *frame, x, y, w int, label string) {
	p := m.colors()
	a := action{Kind: "attachments"}
	if m.colorProfile > colorprofile.ANSI && !m.plainIcons || w < 3 {
		f.button(m, x, y, w, label, "attachments", a, p.blue, p.input)
		return
	}
	v := m.componentStyle(squareFill, m.controlState(false, "attachments"), p.blue, p.input)
	f.compactControl(m, x, y, w, fit(singleLine(label), w-2), v)
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: w, H: 1}, Action: a, Label: label, Key: "attachments"})
}
