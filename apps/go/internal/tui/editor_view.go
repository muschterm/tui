package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// editor_view.go lays out and paints a document buffer: the save-state line,
// state rows with their actions (take over, review, recover), and the text
// with line numbers, selection, cursor and scrollbar. Painting is per
// visible row from the line model, so a keystroke in a large document costs
// only the rows on screen plus cached per-line widths and row counts.

// docWrapRoom is the wrap width of b's text, or 0 when not wrapping.
func docWrapRoom(b *fileBuffer) int {
	if b != nil && b.wrap {
		return max(1, b.docW)
	}
	return 0
}

// totalRows counts visual rows (lines when room <= 0).
func (t *edText) totalRows(room int) int {
	if room <= 0 {
		return len(t.lines)
	}
	n := 0
	for i := range t.lines {
		n += t.rowCount(i, room)
	}
	return n
}

// rowLine returns the line holding visual row and the row within that line.
func (t *edText) rowLine(row, room int) (int, int) {
	if row < 0 {
		return 0, 0
	}
	if room <= 0 {
		return min(row, len(t.lines)-1), 0
	}
	for i := range t.lines {
		c := t.rowCount(i, room)
		if row < c {
			return i, row
		}
		row -= c
	}
	last := len(t.lines) - 1
	return last, t.rowCount(last, room) - 1
}

// rowStart is the visual row of line's first row.
func (t *edText) rowStart(line, room int) int {
	if room <= 0 {
		return line
	}
	n := 0
	for i := 0; i < line && i < len(t.lines); i++ {
		n += t.rowCount(i, room)
	}
	return n
}

// docCursorRow returns p's visual row and its column within that row.
func docCursorRow(t *edText, p edPos, room int) (int, int) {
	line := t.lines[p.line]
	x := colAt(line, p.col)
	if room <= 0 {
		return p.line, x
	}
	rows := lineRows(line, room)
	r := 0
	for i := range rows {
		if rows[i].b <= p.col {
			r = i
		}
	}
	return t.rowStart(p.line, room) + r, x - rows[r].col
}

// docRowPos returns the position at column x of visual row.
func docRowPos(t *edText, row, x, room int) edPos {
	line, within := t.rowLine(row, room)
	s := t.lines[line]
	if room <= 0 {
		return edPos{line, byteAtCol(s, max(0, x))}
	}
	rows := lineRows(s, room)
	within = min(within, len(rows)-1)
	b := byteAtCol(s, rows[within].col+max(0, x))
	if within+1 < len(rows) && b >= rows[within+1].b {
		b = graphemeBefore(s, rows[within+1].b)
	}
	return edPos{line, max(b, rows[within].b)}
}

// docEnsureVisible scrolls b so the cursor is on screen.
func (m *Model) docEnsureVisible(s *docSession, b *fileBuffer) {
	t := s.viewText()
	if b == nil || t == nil || b.docH <= 0 {
		return
	}
	room := docWrapRoom(b)
	row, x := docCursorRow(t, s.ed.cur, room)
	if row < b.scroll {
		b.scroll = row
	} else if row >= b.scroll+b.docH {
		b.scroll = row - b.docH + 1
	}
	if room <= 0 && b.docW > 2 {
		switch {
		case x < b.hscroll+boolCell(b.hscroll > 0):
			b.hscroll = max(0, x-b.docW/4)
		case x >= b.hscroll+b.docW-1:
			b.hscroll = x - b.docW + 2
		}
	}
}

func boolCell(b bool) int {
	if b {
		return 1
	}
	return 0
}

// docFormat describes the file's encoding and line endings.
func docFormat(st protocol.DocumentStatus) string {
	enc := "UTF-8"
	if st.BOM {
		enc += " with BOM"
	}
	switch st.Newline {
	case "crlf":
		return enc + " · CRLF"
	case "lf":
		return enc + " · LF"
	}
	return enc
}

type docButton struct {
	label, key string
	a          action
}

// docNoticeRow paints one state row: a status mark, the label and right
// aligned compact buttons.
func (m *Model) docNoticeRow(f *frame, x, y, w int, state, label string, buttons ...docButton) {
	p := m.colors()
	glyph, ink := panelStatusMark(m, state)
	f.text(x, y, w, "", p.text, p.panel)
	f.text(x, y, 2, glyph, ink, p.panel)
	right := x + w
	for i := len(buttons) - 1; i >= 0; i-- {
		bw := ansi.StringWidth(buttons[i].label) + 2
		if right-bw < x+12 {
			break
		}
		right -= bw
		f.compactButton(m, right, y, bw, buttons[i].label, buttons[i].key, buttons[i].a, false, 0)
		right--
	}
	f.text(x+2, y, max(0, right-x-3), label, p.text, p.panel)
}

// docKeptRow paints the kept-copies row of a buffer: the newest reason and
// count, Copy (newest), All… (every copy) when several, and Dismiss.
func (m *Model) docKeptRow(f *frame, x, y, w int, s *docSession, b *fileBuffer) {
	lost, _ := m.newestLost(s, b)
	id := ""
	n := len(b.docLost)
	if s != nil {
		id = s.id
		n += len(s.lost)
	}
	label := "Edits not stored · " + lost.reason
	buttons := []docButton{{"Copy", "doc-lost-copy", action{Kind: "doc-lost-copy", ID: id}}}
	if n > 1 {
		label += fmt.Sprintf(" (%d kept)", n)
		buttons = append(buttons, docButton{"All…", "doc-kept", action{Kind: "doc-kept", ID: id, Value: "buffer"}})
	}
	buttons = append(buttons, docButton{"Dismiss", "doc-lost-dismiss", action{Kind: "doc-lost-dismiss", ID: id}})
	m.docNoticeRow(f, x, y, w, "failed", label, buttons...)
}

// renderDocBody paints a document buffer below its tab strip.
func (m *Model) renderDocBody(f *frame, r shell.Rect, b *fileBuffer, s *docSession) {
	p := m.colors()
	x, y, w := r.X, r.Y, r.W
	bottom := r.Y + r.H
	editing := m.docEdit == s.id && m.focus == "files-text"
	// Save state, then the file format and the buffer's controls.
	controls := []struct{ icon, key, help string }{{"wrap", "files-wrap", "Wrap long lines · w (outside edit mode)"}, {"copy", "files-copy", "Copy document text"}}
	right := x + w
	for i := len(controls) - 1; i >= 0; i-- {
		c := controls[i]
		if right-3 < x+8 {
			break
		}
		right -= 3
		ink := p.muted
		if c.key == "files-wrap" && b.wrap {
			ink = p.blue
		}
		f.iconButton(m, right, y, 3, " "+m.icon(c.icon), c.key, action{Kind: c.key}, ink, p.panel)
		f.hits[len(f.hits)-1].Label = c.help
	}
	// A paused document offers Review on the same line.
	if st := s.status.State; st == protocol.DocumentStatePausedConflict || st == protocol.DocumentStateDeleted {
		if bw := len("Review") + 2; right-bw-1 > x+12 {
			right -= bw + 1
			f.compactButton(m, right, y, bw, "Review", "doc-review", action{Kind: "doc-review", ID: s.id}, false, 0)
			f.hits[len(f.hits)-1].Label = "Review the file on disk and your document, then choose which to keep"
		}
	}
	state, status := m.docStatusLine(s)
	glyph, ink := panelStatusMark(m, state)
	f.text(x, y, 2, glyph, ink, p.panel)
	room := max(0, right-x-3)
	f.text(x+2, y, room, status, p.text, p.panel)
	if sw := ansi.StringWidth(status); sw+3 < room {
		f.text(x+2+sw, y, room-sw, " · "+docFormat(s.status), p.muted, p.panel)
	}
	y++
	if _, ok := m.newestLost(s, b); ok && y < bottom {
		m.docKeptRow(f, x, y, w, s, b)
		y++
	}
	if s.recovering() && y < bottom {
		m.docNoticeRow(f, x, y, w, "active", "Recovering your edits · typing resumes when the document is back")
		y++
	}
	if q, ok := m.quarantinedDoc(b); ok && q.ID != s.id && y < bottom {
		m.docNoticeRow(f, x, y, w, "failed", "Earlier edits to this file could not be loaded",
			docButton{"Delete retained edits", "doc-dismiss", action{Kind: "doc-dismiss", ID: q.ID, Value: b.path}})
		y++
	}
	if s.otherEditor(m.clientID) && y < bottom {
		label := protocol.DocumentSimultaneousUnavailable + " · another client is editing"
		if ansi.StringWidth(label)+len("Take over")+6 > w {
			label = protocol.DocumentSimultaneousUnavailable
		}
		m.docNoticeRow(f, x, y, w, "blocked", label, docButton{"Take over", "doc-take", action{Kind: "doc-take", ID: s.id}})
		y++
	}
	body := shell.Rect{X: x, Y: y, W: w, H: max(0, bottom-y)}
	if body.H == 0 {
		return
	}
	t := s.viewText()
	gutter := len(strconv.Itoa(len(t.lines))) + 1
	text := shell.Rect{X: x + gutter, Y: body.Y, W: max(1, w-1-gutter), H: body.H}
	b.docW, b.docH = text.W, text.H
	wrapRoom := docWrapRoom(b)
	total := t.totalRows(wrapRoom)
	f.filesText, f.docText = body, text
	f.filesTextMax = max(0, total-body.H)
	if wrapRoom <= 0 {
		widest := 0
		for i := range t.lines {
			widest = max(widest, t.widthOf(i))
		}
		// One more cell than the widest line leaves room for the cursor.
		f.filesTextHMax = max(0, widest-text.W+1)
	}
	b.scroll = min(max(0, b.scroll), f.filesTextMax)
	b.hscroll = min(max(0, b.hscroll), f.filesTextHMax)
	label := "File text · Enter edits · arrows scroll · w wraps · Backspace returns to the tree"
	switch {
	case editing:
		label = "Editing · Esc stops · Ctrl+Z undo · Ctrl+Y redo · autosaves"
	case s.otherEditor(m.clientID):
		label = "File text · another client is editing · Take over to edit"
	case s.status.State == protocol.DocumentStateReadOnly:
		label = "File text · read-only · arrows scroll"
	}
	f.hits = append(f.hits, hit{Rect: body, Action: action{Kind: "files-text-body"}, Label: label, Key: "files-text"})
	if f.rows != nil {
		m.paintDocRows(f, text, gutter, b, s, editing)
	}
	f.scrollbar(m, shell.Rect{X: x + w - 1, Y: body.Y, W: 1, H: body.H}, "files-text", total, body.H, b.scroll, p.panel)
}

// paintDocRows paints the visible rows of the text.
func (m *Model) paintDocRows(f *frame, r shell.Rect, gutter int, b *fileBuffer, s *docSession, editing bool) {
	p := m.colors()
	t := s.viewText()
	e := &s.ed
	room := docWrapRoom(b)
	a, z := e.selRange()
	sel := e.hasSel()
	normal := style(p.text, p.panel)
	marked := style(p.muted, p.panel)
	selected := style(p.text, p.selected)
	cursor := style(p.text, p.panel).Reverse(true)
	line, within := t.rowLine(b.scroll, room)
	for i := 0; i < r.H && line < len(t.lines); i++ {
		y := r.Y + i
		src := t.lines[line]
		var rows []edRow
		if room > 0 {
			rows = lineRows(src, room)
		} else {
			rows = []edRow{{0, 0}}
		}
		n := ""
		if within == 0 {
			n = strconv.Itoa(line + 1)
		}
		f.text(r.X-gutter, y, gutter, fmt.Sprintf("%*s", gutter-1, n), p.muted, p.panel)
		start, end, startCol := rows[within].b, len(src), rows[within].col
		if within+1 < len(rows) {
			end = rows[within+1].b
		}
		left := startCol
		if room <= 0 {
			left = b.hscroll
		}
		var out strings.Builder
		var run strings.Builder
		var runStyle *lipgloss.Style
		flush := func() {
			if run.Len() > 0 && runStyle != nil {
				out.WriteString(runStyle.Render(run.String()))
			}
			run.Reset()
		}
		put := func(st *lipgloss.Style, text string) {
			if st != runStyle {
				flush()
				runStyle = st
			}
			run.WriteString(text)
		}
		inSel := func(pos edPos) bool { return sel && !pos.less(a) && pos.less(z) }
		col := left // next column to paint
		clippedRight := false
		walkCells(src, func(c edCell) bool {
			if c.b < start {
				return true
			}
			if c.b >= end {
				return false
			}
			if c.col+c.w <= left {
				return true
			}
			if c.col-left >= r.W {
				clippedRight = true
				return false
			}
			pos := edPos{line, c.b}
			st := &normal
			switch {
			case editing && pos == e.cur:
				st = &cursor
			case inSel(pos):
				st = &selected
			case c.marked:
				st = &marked
			}
			text := c.text
			if c.col < left || c.col+c.w-left > r.W {
				// A wide cell cut by an edge paints as spaces.
				visible := min(c.col+c.w, left+r.W) - max(c.col, left)
				text = strings.Repeat(" ", visible)
			}
			put(st, text)
			col = c.col + c.w
			return true
		})
		// The cursor or a selected line break after the row's text.
		lastRow := within == len(rows)-1
		used := max(0, col-left)
		if used < r.W {
			switch {
			case editing && e.cur == edPos{line, end} && (lastRow || end == len(src)):
				put(&cursor, " ")
				used++
			case lastRow && inSel(edPos{line, len(src)}) && line < len(t.lines)-1:
				put(&selected, " ")
				used++
			}
		}
		if used < r.W {
			put(&normal, strings.Repeat(" ", r.W-used))
		}
		flush()
		f.put(shell.Rect{X: r.X, Y: y, W: r.W, H: 1}, out.String())
		if room <= 0 {
			if b.hscroll > 0 && !(editing && e.cur.line == line && colAt(src, e.cur.col) == b.hscroll) {
				f.text(r.X, y, 1, "‹", p.muted, p.panel)
			}
			if clippedRight {
				f.text(r.X+r.W-1, y, 1, "›", p.muted, p.panel)
			}
		}
		if within++; within >= len(rows) {
			line, within = line+1, 0
		}
	}
}
