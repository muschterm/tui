package tui

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/reearth/ygo/crdt"
	"github.com/rivo/uniseg"
)

// editor_text.go is the editor's line model of a shared document's text
// (ADR 0022): the text split at LF, with per-line UTF-16 lengths so CRDT
// offsets (UTF-16 code units) convert to line/byte positions without
// rescanning the document. Positions inside a line are UTF-8 byte offsets on
// grapheme boundaries; display columns come from walkCells, which also
// replaces control characters by visible symbols so document text never
// reaches the terminal as control sequences.

// edPos is a position: a line index and a byte offset into that line.
type edPos struct{ line, col int }

func (a edPos) less(b edPos) bool { return a.line < b.line || a.line == b.line && a.col < b.col }

// edText is the document text as lines.
type edText struct {
	lines []string
	u16   []int // UTF-16 length of each line, without its LF
	// prefix[i] is the UTF-16 offset of line i's start; entries below
	// prefixOK are valid.
	prefix   []int
	prefixOK int
	// width caches each line's display width (-1 unknown); rows caches its
	// wrapped row count at rowsRoom (0 unknown).
	width    []int
	rows     []int
	rowsRoom int
	size     int // UTF-8 bytes including LFs
}

func newEdText(s string) *edText {
	t := &edText{}
	t.set(s)
	return t
}

func (t *edText) set(s string) {
	t.lines = strings.Split(s, "\n")
	t.u16 = make([]int, len(t.lines))
	t.width = make([]int, len(t.lines))
	t.rows = make([]int, len(t.lines))
	for i, l := range t.lines {
		t.u16[i] = u16Len(l)
		t.width[i] = -1
	}
	t.prefix = make([]int, len(t.lines))
	t.prefixOK = 0
	t.size = len(s)
}

// String returns the whole text.
func (t *edText) String() string { return strings.Join(t.lines, "\n") }

// Len is the text's length in UTF-16 code units.
func (t *edText) Len() int {
	n := len(t.lines) - 1
	for _, u := range t.u16 {
		n += u
	}
	return n
}

func u16Len(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < 0x80 {
			n++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += max(1, utf16.RuneLen(r))
		i += size - 1
	}
	return n
}

// lineStart returns the UTF-16 offset of line i's first unit.
func (t *edText) lineStart(i int) int {
	for t.prefixOK <= i {
		j := t.prefixOK
		if j == 0 {
			t.prefix[0] = 0
		} else {
			t.prefix[j] = t.prefix[j-1] + t.u16[j-1] + 1
		}
		t.prefixOK++
	}
	return t.prefix[i]
}

// offset converts a position to a UTF-16 offset.
func (t *edText) offset(p edPos) int {
	p = t.clamp(p)
	return t.lineStart(p.line) + u16Len(t.lines[p.line][:p.col])
}

// posAt converts a UTF-16 offset to a position. An offset inside a
// surrogate pair lands on that character's start.
func (t *edText) posAt(u int) edPos {
	if u <= 0 {
		return edPos{}
	}
	// Extend the prefix only as far as needed, then binary search it.
	for t.prefixOK < len(t.lines) && (t.prefixOK == 0 || t.prefix[t.prefixOK-1] <= u) {
		t.lineStart(t.prefixOK)
	}
	line := max(0, sort.Search(t.prefixOK, func(i int) bool { return t.prefix[i] > u })-1)
	rest := u - t.prefix[line]
	s := t.lines[line]
	if rest >= t.u16[line] {
		return edPos{line, len(s)}
	}
	col := 0
	for rest > 0 && col < len(s) {
		r, size := utf8.DecodeRuneInString(s[col:])
		w := max(1, utf16.RuneLen(r))
		if w > rest {
			break
		}
		rest -= w
		col += size
	}
	return edPos{line, col}
}

// clamp keeps p inside the text and on a rune start.
func (t *edText) clamp(p edPos) edPos {
	p.line = min(max(0, p.line), len(t.lines)-1)
	s := t.lines[p.line]
	p.col = min(max(0, p.col), len(s))
	for p.col > 0 && p.col < len(s) && !utf8.RuneStart(s[p.col]) {
		p.col--
	}
	return p
}

// slice returns the text between positions a <= b.
func (t *edText) slice(a, b edPos) string {
	if a.line == b.line {
		return t.lines[a.line][a.col:b.col]
	}
	var out strings.Builder
	out.WriteString(t.lines[a.line][a.col:])
	for i := a.line + 1; i < b.line; i++ {
		out.WriteString("\n")
		out.WriteString(t.lines[i])
	}
	out.WriteString("\n")
	out.WriteString(t.lines[b.line][:b.col])
	return out.String()
}

// edEdit replaces UTF-16 range [at, at+del) with text.
type edEdit struct {
	at, del int
	text    string
}

// deltaEdits converts a text delta into edits addressed in the text before
// the delta, in ascending order.
func deltaEdits(delta []crdt.Delta) []edEdit {
	var out []edEdit
	pos := 0
	for _, d := range delta {
		switch d.Op {
		case crdt.DeltaOpRetain:
			pos += d.Retain
		case crdt.DeltaOpDelete:
			if n := len(out); n > 0 && out[n-1].at+out[n-1].del == pos {
				out[n-1].del += d.Delete
			} else {
				out = append(out, edEdit{at: pos, del: d.Delete})
			}
			pos += d.Delete
		case crdt.DeltaOpInsert:
			s, _ := d.Insert.(string)
			if n := len(out); n > 0 && out[n-1].at+out[n-1].del == pos {
				out[n-1].text += s
			} else {
				out = append(out, edEdit{at: pos, text: s})
			}
		}
	}
	return out
}

// apply applies edits (ascending, addressed in the text before them).
func (t *edText) apply(edits []edEdit) {
	for i := len(edits) - 1; i >= 0; i-- {
		t.replace(edits[i].at, edits[i].del, edits[i].text)
	}
}

// replace substitutes UTF-16 range [at, at+del) with text.
func (t *edText) replace(at, del int, text string) {
	a := t.posAt(at)
	b := a
	if del > 0 {
		b = t.posAt(at + del)
	}
	head := t.lines[a.line][:a.col]
	tail := t.lines[b.line][b.col:]
	removed := 0
	for i := a.line; i <= b.line; i++ {
		removed += len(t.lines[i]) + 1
	}
	parts := strings.Split(head+text+tail, "\n")
	u16s := make([]int, len(parts))
	widths := make([]int, len(parts))
	rows := make([]int, len(parts))
	added := 0
	for i, p := range parts {
		u16s[i], widths[i] = u16Len(p), -1
		added += len(p) + 1
	}
	t.lines = slices.Replace(t.lines, a.line, b.line+1, parts...)
	t.u16 = slices.Replace(t.u16, a.line, b.line+1, u16s...)
	t.width = slices.Replace(t.width, a.line, b.line+1, widths...)
	t.rows = slices.Replace(t.rows, a.line, b.line+1, rows...)
	if len(t.prefix) < len(t.lines) {
		t.prefix = append(t.prefix, make([]int, len(t.lines)-len(t.prefix))...)
	} else {
		t.prefix = t.prefix[:len(t.lines)]
	}
	t.prefixOK = min(t.prefixOK, a.line+1)
	t.size += added - removed
}

// ---- graphemes and cells ----

// edCell is one grapheme as painted: its byte range in the line, display
// column, width and the (sanitized) text painted for it.
type edCell struct {
	b, e   int
	col, w int
	text   string
	marked bool // a substituted control or invisible character
}

// walkCells visits line's graphemes in order with their display columns.
// Tabs expand to filesTabWidth stops; C0/C1 controls, DEL, bidi and line
// separator controls and zero-width clusters paint as visible symbols. The
// visit stops when fn returns false. Printable ASCII takes a fast path.
func walkCells(line string, fn func(c edCell) bool) {
	col, state := 0, -1
	for i := 0; i < len(line); {
		c := line[i]
		if c >= 0x20 && c < 0x7f && (i+1 == len(line) || line[i+1] < 0x80) {
			if !fn(edCell{b: i, e: i + 1, col: col, w: 1, text: line[i : i+1]}) {
				return
			}
			i++
			col++
			state = -1
			continue
		}
		cluster, _, width, next := uniseg.FirstGraphemeClusterInString(line[i:], state)
		state = next
		cell := edCell{b: i, e: i + len(cluster), col: col, w: width, text: cluster}
		switch r, _ := utf8.DecodeRuneInString(cluster); {
		case cluster == "\t":
			cell.w = filesTabWidth - col%filesTabWidth
			cell.text = strings.Repeat(" ", cell.w)
		case r < 0x20 && len(cluster) == 1:
			cell.w, cell.text, cell.marked = 1, string(rune(0x2400+r)), true
		case r == 0x7f:
			cell.w, cell.text, cell.marked = 1, "␡", true
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r) && utf8.RuneCountInString(cluster) == 1:
			cell.w, cell.text, cell.marked = 1, "\ufffd", true
		case width <= 0:
			cell.w, cell.text, cell.marked = 1, "\ufffd", true
		case width > 2:
			cell.w = 2
		}
		// Paint exactly the cells counted: a cluster the frame would measure
		// differently (or wider than two cells) becomes a replacement mark.
		if !cell.marked && cluster != "\t" && ansi.StringWidth(cell.text) != cell.w {
			cell.text, cell.marked = "\ufffd"+strings.Repeat(" ", cell.w-1), true
		}
		if !fn(cell) {
			return
		}
		i = cell.e
		col += cell.w
	}
}

// lineWidth is a line's display width.
func lineWidth(line string) int {
	w := 0
	walkCells(line, func(c edCell) bool { w = c.col + c.w; return true })
	return w
}

// widthOf returns line i's cached display width.
func (t *edText) widthOf(i int) int {
	if t.width[i] < 0 {
		t.width[i] = lineWidth(t.lines[i])
	}
	return t.width[i]
}

// edRow is one painted row of a line: its first byte and first column.
type edRow struct{ b, col int }

// lineRows splits a line into rows of at most room cells at grapheme
// boundaries (one row when room <= 0). A grapheme wider than the room keeps
// its own row.
func lineRows(line string, room int) []edRow {
	rows := []edRow{{0, 0}}
	if room <= 0 {
		return rows
	}
	used := 0
	walkCells(line, func(c edCell) bool {
		if used > 0 && used+c.w > room {
			rows = append(rows, edRow{c.b, c.col})
			used = 0
		}
		used += c.w
		return true
	})
	return rows
}

// rowCount returns line i's row count when wrapping at room.
func (t *edText) rowCount(i, room int) int {
	if t.rowsRoom != room {
		clear(t.rows)
		t.rowsRoom = room
	}
	if t.rows[i] == 0 {
		s := t.lines[i]
		if room > 0 && isPlainASCII(s) {
			t.rows[i] = max(1, (len(s)+room-1)/room)
		} else {
			t.rows[i] = len(lineRows(s, room))
		}
	}
	return t.rows[i]
}

func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// colAt is the display column of byte offset col in line.
func colAt(line string, col int) int {
	out := 0
	walkCells(line, func(c edCell) bool {
		if c.b >= col {
			return false
		}
		out = c.col + c.w
		return true
	})
	return out
}

// byteAtCol returns the grapheme boundary for display column x in line: the
// start of the cell covering x, or the line's end past it.
func byteAtCol(line string, x int) int {
	out := len(line)
	walkCells(line, func(c edCell) bool {
		if c.col+c.w > x {
			// Closer to the cell's end rounds forward, as pointers do.
			out = c.b
			if c.w > 1 && x-c.col >= (c.w+1)/2 {
				out = c.e
			}
			return false
		}
		return true
	})
	return out
}

// graphemeBefore returns the boundary before col in line.
func graphemeBefore(line string, col int) int {
	if col <= 0 {
		return 0
	}
	prev := 0
	walkCells(line[:col], func(c edCell) bool { prev = c.b; return true })
	return prev
}

// graphemeAfter returns the boundary after col in line.
func graphemeAfter(line string, col int) int {
	if col >= len(line) {
		return len(line)
	}
	next := len(line)
	walkCells(line[col:], func(c edCell) bool { next = col + c.e; return false })
	return next
}

// snapGrapheme moves col back to the start of the grapheme containing it.
func snapGrapheme(line string, col int) int {
	if col <= 0 {
		return 0
	}
	if col >= len(line) {
		return len(line)
	}
	out := col
	walkCells(line, func(c edCell) bool {
		if c.e > col {
			if c.b < col {
				out = c.b
			}
			return false
		}
		return true
	})
	return out
}

func wordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordLeft returns the start of the word before col (skipping spaces and
// punctuation first).
func wordLeft(line string, col int) int {
	i := col
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:i])
		if wordRune(r) {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:i])
		if !wordRune(r) {
			break
		}
		i -= n
	}
	return snapGrapheme(line, i)
}

// wordRight returns the end of the word after col.
func wordRight(line string, col int) int {
	i := col
	for i < len(line) {
		r, n := utf8.DecodeRuneInString(line[i:])
		if wordRune(r) {
			break
		}
		i += n
	}
	for i < len(line) {
		r, n := utf8.DecodeRuneInString(line[i:])
		if !wordRune(r) {
			break
		}
		i += n
	}
	if i < len(line) {
		return snapGrapheme(line, i)
	}
	return i
}
