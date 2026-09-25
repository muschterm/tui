package tui

import (
	"fmt"
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// termPane records where a terminal grid was laid out in a frame.
type termPane struct {
	id   string
	grid shell.Rect
}

// surfaceBodyRect is the right host's body below its tab row: the detail
// area of every surface and the grid of a Terminal tab.
func surfaceBodyRect(r shell.Rect) shell.Rect {
	x, w, tabRows := r.X+1, r.W-2, 1
	if r.H < 8 {
		// A short host drops its margins.
		return shell.Rect{X: x + 1, Y: r.Y + tabRows + 1, W: max(1, w-2), H: max(0, r.H-tabRows-1)}
	}
	return shell.Rect{X: x + 1, Y: r.Y + 1 + tabRows + 2, W: max(1, w-2), H: max(0, r.H-tabRows-4)}
}

// bottomBodyRect is the bottom panel's body below its tab row.
func bottomBodyRect(r shell.Rect) shell.Rect {
	return shell.Rect{X: r.X + 2, Y: r.Y + 2, W: max(1, r.W-4), H: max(0, r.H-2)}
}

// terminalGridRect is the grid of a terminal shown in the bottom panel or
// right host r. The row above it is the terminal's status row; the column
// right of it stays free like other surfaces' scrollbar gutter.
func terminalGridRect(r shell.Rect, bottom bool) shell.Rect {
	if bottom {
		return bottomBodyRect(r)
	}
	return surfaceBodyRect(r)
}

// terminalTabTitle adds the terminal's sanitized title to its local name,
// for example "Terminal 1 · vim".
func (m *Model) terminalTabTitle(tab shell.Surface) string {
	if tab.Kind != "terminal" {
		return tab.Title
	}
	rec, ok := m.liveTerminal(tab.ID)
	if !ok {
		return tab.Title
	}
	name := rec.Title
	if tv := m.terms[tab.ID]; tv != nil && tv.screen != nil {
		name = tv.screen.Title
	}
	name = strings.TrimSpace(singleLine(name))
	if name == "" {
		return tab.Title
	}
	return tab.Title + " · " + ansi.Truncate(name, 24, "…")
}

func (m *Model) displayTabs(tabs []shell.Surface) []shell.Surface {
	out := make([]shell.Surface, len(tabs))
	for i, tab := range tabs {
		out[i] = tab
		out[i].Title = m.terminalTabTitle(tab)
	}
	return out
}

// terminalEndLabel describes why an ended terminal ended.
func terminalEndLabel(reason string, exit *protocol.TerminalExit) string {
	switch reason {
	case protocol.TerminalEndServerStopped:
		return "Ended · server stopped"
	case protocol.TerminalEndServerRestarted:
		return "Ended · server restarted"
	case protocol.TerminalEndThreadDeleted:
		return "Ended · thread deleted"
	case protocol.TerminalEndServerStoppedUnconfirmed:
		return "Server stopped · exit not confirmed"
	}
	switch {
	case exit == nil:
		return "Ended"
	case exit.Killed:
		return "Ended · killed"
	case exit.Signal != "":
		return "Ended · " + safe(exit.Signal)
	}
	return fmt.Sprintf("Ended · exit %d", exit.Code)
}

// terminalEnded returns the end reason and exit for an ended terminal.
func terminalEnded(rec protocol.Terminal, tv *termView) (string, *protocol.TerminalExit, bool) {
	if tv != nil && tv.ended != nil {
		return tv.ended.Reason, tv.ended.Exit, true
	}
	if rec.State == protocol.TerminalStateEnded {
		return rec.EndReason, rec.Exit, true
	}
	return "", nil, false
}

// renderTerminalPane paints a live terminal: its status row above grid and
// the latest screen inside grid.
func (m *Model) renderTerminalPane(f *frame, grid shell.Rect, rec protocol.Terminal) {
	if grid.W <= 0 || grid.H <= 0 {
		return
	}
	id := rec.ID
	tv := m.terms[id]
	f.terms = append(f.terms, termPane{id: id, grid: grid})
	label := "Terminal · click or Enter to type · wheel scrolls history"
	if reason, _, ended := terminalEnded(rec, tv); ended || rec.State != protocol.TerminalStateRunning {
		label = "Terminal not running · input unavailable"
		if reason == protocol.TerminalEndServerStoppedUnconfirmed && rec.Error != "" {
			label += " · " + singleLine(rec.Error)
		}
	} else if tv != nil && tv.hasControl && !m.controls(tv) {
		label = "Terminal controlled by another client · Take control to type"
	} else if m.termFocus == id {
		label = "Typing into the terminal · Ctrl+] leaves"
	}
	if tv != nil && tv.screen != nil && tv.screen.Degraded {
		label += " · Screen was too large to send in full; some characters or styles are simplified"
	}
	f.hits = append(f.hits, hit{Rect: grid, Action: action{Kind: "terminal-focus", ID: id}, Label: label, Key: "terminal:" + id})
	m.renderTerminalStatus(f, shell.Rect{X: grid.X, Y: grid.Y - 1, W: grid.W, H: 1}, grid, rec, tv)
	m.renderTerminalGrid(f, grid, rec, tv)
}

// renderTerminalStatus paints the one-row status line: lifecycle, observer
// and focus hints, a size note when the grid differs from the pane, and the
// Take control or Retry close action at its right.
func (m *Model) renderTerminalStatus(f *frame, r, grid shell.Rect, rec protocol.Terminal, tv *termView) {
	if r.Y < 0 || r.W <= 0 {
		return
	}
	p := m.colors()
	ink := p.muted
	var parts []string
	var act action
	actLabel := ""
	suffix, suffixInk := "", ink
	reason, exit, ended := terminalEnded(rec, tv)
	switch {
	case ended:
		parts = append(parts, terminalEndLabel(reason, exit))
		if exit != nil {
			switch {
			case exit.DescendantsRemaining > 0:
				suffix = fmt.Sprintf(" · %d background processes still running", exit.DescendantsRemaining)
				suffixInk = p.gold
			case exit.DescendantsUnknown && reason == protocol.TerminalEndClosed:
				suffix = " · background processes unknown"
				suffixInk = ink
			}
		}
	case rec.State == protocol.TerminalStateClosing:
		parts = append(parts, "Closing…")
	case rec.State == protocol.TerminalStateCloseUncertain:
		ink = p.gold
		note := "Close not confirmed"
		if rec.Error != "" {
			note += " · " + singleLine(rec.Error)
		}
		parts = append(parts, note)
		act, actLabel = action{Kind: "terminal-retry-close", ID: rec.ID}, "Retry close"
	case tv == nil || tv.conn == nil && tv.screen == nil:
		if tv != nil && tv.limited {
			parts = append(parts, "Too many terminal viewers · try again later")
		} else {
			parts = append(parts, "Connecting…")
		}
	case tv.conn == nil:
		if tv.limited {
			parts = append(parts, "Too many terminal viewers · try again later")
		} else {
			parts = append(parts, "Reconnecting…")
		}
	default:
		if tv.hasControl && !m.controls(tv) {
			parts = append(parts, "Observing")
			act, actLabel = action{Kind: "terminal-take-control", ID: rec.ID}, "Take control"
		}
		if m.termFocus == rec.ID {
			parts = append(parts, "Ctrl+] to leave")
		}
	}
	if tv != nil && tv.scroll > 0 {
		parts = append(parts, fmt.Sprintf("History · %d lines up · End returns", tv.scroll))
	}
	if tv != nil && tv.screen != nil && (tv.screen.Cols != grid.W || tv.screen.Rows != grid.H) {
		parts = append(parts, fmt.Sprintf("size %d×%d", tv.screen.Cols, tv.screen.Rows))
	}
	if tv != nil && tv.screen != nil && tv.screen.Degraded {
		parts = append(parts, "simplified")
	}
	f.text(r.X, r.Y, r.W, "", p.text, p.panel)
	textW := r.W
	if actLabel != "" {
		w := min(r.W, ansi.StringWidth(actLabel)+2)
		f.compactButton(m, r.X+r.W-w, r.Y, w, actLabel, "terminal-action:"+rec.ID, act, false, normalControl)
		textW = max(0, r.W-w-1)
	}
	msg := strings.Join(parts, "  ·  ")
	mainW := min(textW, ansi.StringWidth(msg))
	f.text(r.X, r.Y, mainW, msg, ink, p.panel)
	if suffix != "" {
		if remW := textW - mainW; remW > 0 {
			f.text(r.X+mainW, r.Y, remW, suffix, suffixInk, p.panel)
		}
	}
}

// terminalLines are the rows to show: the live screen, or while scrolled
// back, cached history followed by the top of the live screen.
func terminalLines(tv *termView, rows int) []protocol.TerminalLine {
	if tv == nil || tv.screen == nil {
		return nil
	}
	if tv.scroll <= 0 || tv.history == nil {
		return tv.screen.Lines
	}
	h := tv.history
	out := make([]protocol.TerminalLine, 0, rows)
	for i := range rows {
		idx := h.Total - tv.scroll + i
		switch {
		case idx < 0:
			out = append(out, nil)
		case idx < h.Total:
			if j := idx - h.From; j >= 0 && j < len(h.Lines) {
				out = append(out, h.Lines[j])
			} else {
				out = append(out, nil)
			}
		case idx-h.Total < len(tv.screen.Lines):
			out = append(out, tv.screen.Lines[idx-h.Total])
		}
	}
	return out
}

func (m *Model) renderTerminalGrid(f *frame, grid shell.Rect, rec protocol.Terminal, tv *termView) {
	p := m.colors()
	f.fill(grid, p, p.panel)
	_, _, ended := terminalEnded(rec, tv)
	if tv == nil || tv.screen == nil {
		if ended {
			f.text(grid.X, grid.Y, grid.W, "Screen not retained after the session ended", p.muted, p.panel)
		}
		return
	}
	if f.rows == nil {
		return
	}
	lines := terminalLines(tv, grid.H)
	cursorY, cursorX := -1, -1
	if m.termFocus == rec.ID && tv.scroll == 0 && tv.screen.Cursor.Visible && !ended {
		cursorY, cursorX = tv.screen.Cursor.Y, tv.screen.Cursor.X
	}
	for y := 0; y < grid.H && y < len(lines); y++ {
		cx := -1
		if y == cursorY {
			cx = cursorX
		}
		f.put(shell.Rect{X: grid.X, Y: grid.Y + y, W: grid.W, H: 1}, m.terminalRow(lines[y], grid.W, cx, ended))
	}
}

// terminalRow renders one grid row as exactly width cells. Graphemes are
// placed at the server's cell widths; a wide grapheme clipped by the right
// edge becomes blanks. cursor is the column painted in reverse video, or -1.
func (m *Model) terminalRow(line protocol.TerminalLine, width, cursor int, dim bool) string {
	var b strings.Builder
	col := 0
	for _, run := range line {
		if col >= width {
			break
		}
		w := max(1, min(2, run.Width))
		sgr := m.terminalSGR(run, dim, false)
		open := false
		for _, g := range run.Graphemes() {
			if col >= width {
				break
			}
			cell := terminalCellText(g, w)
			if col+w > width {
				cell = strings.Repeat(" ", width-col)
			}
			if cursor >= col && cursor < col+w {
				b.WriteString(m.terminalSGR(run, dim, true) + cell + ansi.ResetStyle)
				open = false
			} else {
				if !open {
					b.WriteString(sgr)
					open = true
				}
				b.WriteString(cell)
			}
			col += ansi.StringWidth(cell)
		}
		if open {
			b.WriteString(ansi.ResetStyle)
		}
	}
	if col < width {
		blank := protocol.TerminalRun{}
		if cursor >= col && cursor < width {
			b.WriteString(m.terminalSGR(blank, dim, false) + strings.Repeat(" ", cursor-col) + ansi.ResetStyle)
			b.WriteString(m.terminalSGR(blank, dim, true) + " " + ansi.ResetStyle)
			col = cursor + 1
		}
		b.WriteString(m.terminalSGR(blank, dim, false) + strings.Repeat(" ", width-col) + ansi.ResetStyle)
	}
	return b.String()
}

// terminalCellText makes one grapheme safe to paint in w cells. The server
// sends decoded cells only, but a control, format or invalid rune reaching
// here is replaced rather than written to the outer terminal, and a grapheme
// whose local width differs from the server's is padded or replaced so the
// row keeps its columns.
func terminalCellText(g string, w int) string {
	if g == "" {
		return strings.Repeat(" ", w)
	}
	for _, r := range g {
		if r == utf8.RuneError || r < 0x20 || r >= 0x7f && r < 0xa0 || unicode.Is(unicode.Bidi_Control, r) || r == ' ' || r == ' ' {
			return "�" + strings.Repeat(" ", w-1)
		}
	}
	switch lw := ansi.StringWidth(g); {
	case lw == w:
		return g
	case lw == 0:
		return strings.Repeat(" ", w)
	case lw < w:
		return g + strings.Repeat(" ", w-lw)
	}
	return "�" + strings.Repeat(" ", w-1)
}

// terminalColor resolves a cell colour; the default is the pane's own ink or
// background so terminal defaults follow the app theme. The renderer
// downsamples to the client's colour profile (256, 16 or none).
func terminalColor(c protocol.TerminalColor, fallback string) color.Color {
	if i, ok := c.Index(); ok {
		if i < 16 {
			return ansi.BasicColor(i)
		}
		return ansi.IndexedColor(i)
	}
	if r, g, b, ok := c.RGB(); ok {
		return color.RGBA{R: r, G: g, B: b, A: 0xff}
	}
	return lipColor(fallback)
}

// terminalSGR is the complete style for a run: explicit colours (reverse is
// applied here, so defaults swap correctly), bold, faint, italic, underline
// and strike. Blink is ignored. cursor inverts the cell; dim fades an ended
// terminal's final screen.
func (m *Model) terminalSGR(run protocol.TerminalRun, dim, cursor bool) string {
	p := m.colors()
	fg, bg := terminalColor(run.FG, p.text), terminalColor(run.BG, p.panel)
	if (run.Attrs&protocol.TerminalAttrReverse != 0) != cursor {
		fg, bg = bg, fg
	}
	s := ansi.Style{}.ForegroundColor(fg).BackgroundColor(bg)
	if run.Attrs&protocol.TerminalAttrBold != 0 {
		s = s.Bold()
	}
	if run.Attrs&protocol.TerminalAttrDim != 0 || dim {
		s = s.Faint()
	}
	if run.Attrs&protocol.TerminalAttrItalic != 0 {
		s = s.Italic(true)
	}
	if run.Attrs&protocol.TerminalAttrUnderline != 0 {
		s = s.Underline(true)
	}
	if run.Attrs&protocol.TerminalAttrStrike != 0 {
		s = s.Strikethrough(true)
	}
	return ansi.ResetStyle + s.String()
}
