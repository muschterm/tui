package tui

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
	"github.com/rivo/uniseg"
)

// files_view.go paints the Files surface: a lazily loaded tree, or the
// active read-only buffer with its strip. A buffer replaces the tree in
// every width; the strip's back control returns to it.

// filesTabWidth is the tab stop used to expand tabs in file text.
const filesTabWidth = 4

// filesRow is one tree row. id is the entry path, "more:<dir>" for a Load
// more row, or empty for a non-selectable status row.
type filesRow struct {
	id, kind, name string
	depth          int
	text           string // status rows: loading, empty, error or notes
	ink            string
}

// filesTreeRows flattens the expanded tree.
func (m *Model) filesTreeRows(v *filesView) []filesRow {
	p := m.colors()
	var rows []filesRow
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		d := v.dirs[dir]
		switch {
		case d == nil || !d.loaded && d.err == "":
			rows = append(rows, filesRow{depth: depth, text: "Loading…", ink: p.muted})
			return
		case !d.loaded:
			rows = append(rows, filesRow{depth: depth, text: "Unavailable · " + d.err, ink: p.red})
			return
		}
		for _, e := range d.entries {
			id := e.Name
			if dir != "" {
				id = dir + "/" + e.Name
			}
			if !filesAddressable(id) {
				// The server cannot address it (a backslash, for one).
				rows = append(rows, filesRow{depth: depth, text: safe(singleLine(e.Name)) + " · Unsupported file name", ink: p.muted})
				continue
			}
			rows = append(rows, filesRow{id: id, kind: e.Kind, name: safe(singleLine(e.Name)), depth: depth})
			if e.Kind == protocol.FileKindDir && v.expanded[id] {
				walk(id, depth+1)
			}
		}
		if len(d.entries) == 0 {
			text := "Empty folder"
			if dir == "" {
				text = "No files"
			}
			rows = append(rows, filesRow{depth: depth, text: text, ink: p.muted})
		}
		if d.err != "" {
			rows = append(rows, filesRow{depth: depth, text: "Refresh failed · " + d.err, ink: p.red})
		}
		if d.next != "" {
			text := fmt.Sprintf("Load more (%d shown)", len(d.entries))
			if d.loading {
				text = "Loading…"
			}
			rows = append(rows, filesRow{id: "more:" + dir, depth: depth, text: text})
		}
		if d.truncated && d.next == "" {
			rows = append(rows, filesRow{depth: depth, text: "Listing stops at 20000 entries", ink: p.muted})
		}
		if d.skipped > 0 {
			rows = append(rows, filesRow{depth: depth, text: fmt.Sprintf("%d names with invalid UTF-8 not shown", d.skipped), ink: p.muted})
		}
	}
	walk("", 0)
	return rows
}

// filesAddressable mirrors the server's path rule for listed names: no
// backslash, NUL or dot component, valid UTF-8 and at most 4096 bytes.
func filesAddressable(p string) bool {
	if len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// expandTabs expands tabs to the next multiple of width by cell column.
func expandTabs(line string, width int) string {
	if !strings.Contains(line, "\t") {
		return line
	}
	var b strings.Builder
	col := 0
	g := uniseg.NewGraphemes(line)
	for g.Next() {
		if g.Str() == "\t" {
			n := width - col%width
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteString(g.Str())
		col += g.Width()
	}
	return b.String()
}

// displaySourceLines turns untrusted text into display lines: split at LF
// (a CR before it is the CRLF ending, not content), tabs expanded to
// filesTabWidth by cell column, escape sequences and control characters
// removed. A final newline does not add an empty line.
func displaySourceLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		lines[i] = expandTabs(safeKeepTabs(strings.TrimSuffix(line, "\r")), filesTabWidth)
	}
	return lines
}

func (b *fileBuffer) sourceLines() []string {
	if b.lines == nil && b.read != nil {
		b.lines = displaySourceLines(b.read.Text)
	}
	return b.lines
}

// filesTextRow is one painted buffer row.
type filesTextRow struct {
	number string
	text   string
}

// textRows lays out a buffer at room cells: one row per line, or wrapped
// rows when wrap is on. The layout is cached per content, wrap and (when
// wrapping) width, so frames and keys only slice it; reads reset it.
func (b *fileBuffer) textRows(room int) (rows []filesTextRow, widest int) {
	if b.rows != nil && b.rowsWrap == b.wrap && (!b.wrap || b.rowsRoom == room) {
		return b.rows, b.widest
	}
	lines := b.sourceLines()
	rows = make([]filesTextRow, 0, len(lines))
	widest = 0
	for i, line := range lines {
		n := strconv.Itoa(i + 1)
		if !b.wrap {
			rows = append(rows, filesTextRow{number: n, text: line})
			widest = max(widest, ansi.StringWidth(line))
			continue
		}
		for j, part := range hardWrap(line, max(1, room)) {
			if j > 0 {
				n = ""
			}
			rows = append(rows, filesTextRow{number: n, text: part})
		}
	}
	b.rows, b.rowsWrap, b.rowsRoom, b.widest = rows, b.wrap, room, widest
	return rows, widest
}

func (m *Model) filesTreeHeight() int { return m.measure().filesTree.H }
func (m *Model) filesTextHeight() int { return m.measure().filesText.H }

// revealFilesRow scrolls the tree so row index i is visible.
func (m *Model) revealFilesRow(v *filesView, i int) {
	h := m.filesTreeHeight()
	if h <= 0 {
		return
	}
	if i < v.scroll {
		v.scroll = i
	} else if i >= v.scroll+h {
		v.scroll = i - h + 1
	}
}

func (m *Model) clampFilesBuffer(b *fileBuffer) {
	f := m.measure()
	b.scroll = min(max(0, b.scroll), f.filesTextMax)
	b.hscroll = min(max(0, b.hscroll), f.filesTextHMax)
}

// filesFixture reports a demo checkout, which has no local files.
func (m *Model) filesFixture() bool { return strings.HasPrefix(m.thread().Checkout, "fixture://") }

// renderFilesSurface paints the surface into the host body r.
func (m *Model) renderFilesSurface(f *frame, r shell.Rect) {
	v := m.currentFilesView()
	if v == nil || r.W < 4 || r.H < 2 {
		return
	}
	if v.buffer() != nil {
		m.renderFilesBuffer(f, r, v)
		return
	}
	m.renderFilesTree(f, r, v)
}

func (m *Model) renderFilesTree(f *frame, r shell.Rect, v *filesView) {
	p := m.colors()
	x, y, w := r.X, r.Y, r.W
	bottom := r.Y + r.H
	panelSectionHeadingOn(f, m, x, y, w-4, "Files", p.panel)
	f.iconButton(m, x+w-3, y, 3, " "+m.icon("refresh"), "files-refresh", action{Kind: "files-refresh"}, p.muted, p.panel)
	f.hits[len(f.hits)-1].Label = "Refresh files · read-only"
	y++
	if r.H >= 8 {
		f.text(x, y, w, truncatePathLeft(safe(m.thread().Checkout), w), p.muted, p.panel)
		y++
	}
	if y < bottom {
		m.paintFilesToggle(f, x, y, w, "Hidden files", v.hidden)
		y++
	}
	if n := len(m.docOrphans); n > 0 && y < bottom {
		m.docNoticeRow(f, x, y, w, "failed", fmt.Sprintf("%d kept unsaved %s", n, docPlural(n, "text", "texts")),
			docButton{"Show", "doc-kept-orphans", action{Kind: "doc-kept", Value: "orphans"}})
		y++
	}
	if r.H >= 8 && y < bottom {
		panelRuleOn(f, m, x, y, w, p.panel)
		y++
	}
	body := shell.Rect{X: x, Y: y, W: w, H: max(0, bottom-y)}
	if body.H == 0 {
		return
	}
	// The whole body is one Tab stop; rows add their own hits over it.
	f.filesTree = body
	f.hits = append(f.hits, hit{Rect: body, Action: action{Kind: "files-tree-body"}, Label: "File tree · arrows move, Enter opens, Left/Right collapse/expand", Key: "files-tree"})
	switch {
	case m.filesFixture():
		glyph, ink := panelStatusMark(m, "unavailable")
		f.text(x, y, 2, glyph, ink, p.panel)
		f.text(x+2, y, w-2, "Fixture threads have no local files", p.muted, p.panel)
		return
	case m.connected && !m.filesAvailable():
		glyph, ink := panelStatusMark(m, "unavailable")
		f.text(x, y, 2, glyph, ink, p.panel)
		f.text(x+2, y, w-2, "Server does not offer file browsing", p.muted, p.panel)
		return
	case !m.connected || m.filesClient() == nil:
		glyph, ink := panelStatusMark(m, "disconnected")
		f.text(x, y, 2, glyph, ink, p.panel)
		f.text(x+2, y, w-2, "Connect to the server to list files", p.muted, p.panel)
		return
	}
	rows := m.filesTreeRows(v)
	f.filesTreeMax = max(0, len(rows)-body.H)
	offset := min(max(0, v.scroll), f.filesTreeMax)
	for i := 0; i < body.H && offset+i < len(rows); i++ {
		m.paintFilesRow(f, x, y+i, w-1, v, rows[offset+i])
	}
	f.scrollbar(m, shell.Rect{X: x + w - 1, Y: body.Y, W: 1, H: body.H}, "files-tree", len(rows), body.H, offset, p.panel)
}

// paintFilesToggle is a full-row panel toggle row.
func (m *Model) paintFilesToggle(f *frame, x, y, w int, label string, on bool) {
	p := m.colors()
	v := m.componentStyle(squareFill, m.controlState(false, "files-hidden"), p.text, p.panel)
	word := "Off"
	if on {
		word = "On "
	}
	right := len(word) + 1 + toggleTrackWidth
	marked := v.focused && f.blank(x-1, y)
	f.styledText(x, y, w, ansi.Truncate(label, max(0, w-right-1), "…"), v, v.focused && !marked)
	if right < w {
		wv := v
		if on {
			wv.foreground, wv.bold = p.blue, true
		} else {
			wv.foreground = p.muted
		}
		f.componentText(x+w-right, y, len(word)+1, word+" ", wv)
		f.put(shell.Rect{X: x + w - toggleTrackWidth, Y: y, W: toggleTrackWidth, H: 1}, m.toggleTrack(on))
	}
	if marked {
		f.focusMark(x-1, y, v, v.base)
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: w, H: 1}, Action: action{Kind: "files-hidden"}, Label: "Show hidden files", Key: "files-hidden"})
}

// filesEntryGlyphs returns a row's expansion caret and type icon.
func (m *Model) filesEntryGlyphs(kind string, open bool) (string, string) {
	caret := ""
	if kind == protocol.FileKindDir {
		caret = m.icon("tree-closed")
		if open {
			caret = m.icon("tree-open")
		}
	}
	icon := m.icon("file")
	switch kind {
	case protocol.FileKindDir:
		icon = m.icon("folder")
		if open {
			icon = m.icon("folder-open")
		}
	case protocol.FileKindSymlink:
		icon = m.icon("file-link")
	case protocol.FileKindOther:
		icon = m.icon("file-other")
	}
	return caret, icon
}

func (m *Model) paintFilesRow(f *frame, x, y, w int, v *filesView, r filesRow) {
	p := m.colors()
	indent := min(2*r.depth, max(0, w-6))
	if r.id == "" {
		f.text(x, y, w, "", p.text, p.panel)
		f.text(x+indent+2, y, max(0, w-indent-2), r.text, r.ink, p.panel)
		return
	}
	key := "files-row:" + r.id
	state := m.controlState(false, key)
	state.Focused = m.focus == "files-tree" && v.cursor == r.id
	cv := m.componentStyle(squareFill, state, p.text, p.panel)
	a := action{Kind: "files-row", ID: r.id, Value: r.kind}
	if strings.HasPrefix(r.id, "more:") {
		f.styledButton(x, y, w, strings.Repeat(" ", indent+2)+r.text, key, a, cv)
		f.hits[len(f.hits)-1].Label = "Load more entries"
		lv := cv
		lv.foreground = p.blue
		f.componentText(x+indent+2, y, max(0, w-indent-2), r.text, lv)
		return
	}
	f.styledButton(x, y, w, "", key, a, cv)
	label := "Open " + r.name
	if r.kind == protocol.FileKindDir {
		label = "Expand or collapse " + r.name
	}
	f.hits[len(f.hits)-1].Label = label
	caret, icon := m.filesEntryGlyphs(r.kind, v.expanded[r.id])
	cx := x + indent
	if caret != "" {
		f.text(cx, y, 1, caret, p.muted, cv.background)
	}
	cx += 2
	iw := ansi.StringWidth(icon)
	if iw > 0 && cx+iw+1 < x+w {
		ink := p.muted
		if r.kind == protocol.FileKindDir {
			ink = p.blue
		}
		f.text(cx, y, iw, icon, ink, cv.background)
		cx += iw + 1
	}
	name := r.name
	if m.plainIcons && r.kind == protocol.FileKindDir {
		name += "/"
	}
	f.componentText(cx, y, max(0, x+w-cx), name, cv)
}

// filesBufferMeta describes a buffer's content in one functional line.
func filesBufferMeta(b *fileBuffer) string {
	r := b.read
	if r == nil {
		return ""
	}
	switch r.Kind {
	case protocol.FileReadBinary:
		return "Binary file · " + filesSize(r.Size)
	case protocol.FileReadTooLarge:
		return "Too large to show · " + filesSize(r.Size)
	case protocol.FileReadNotRegular:
		if r.LinkTarget != "" {
			return "Symbolic link"
		}
		return "Not a regular file"
	}
	parts := []string{filesSize(r.Size)}
	if r.Encoding == "invalid" {
		parts = append(parts, "invalid UTF-8 shown as �")
	} else {
		enc := "UTF-8"
		if r.BOM {
			enc += " with BOM"
		}
		parts = append(parts, enc)
	}
	switch r.Newline {
	case "lf":
		parts = append(parts, "LF")
	case "crlf":
		parts = append(parts, "CRLF")
	case "mixed":
		parts = append(parts, "mixed line endings")
	}
	return strings.Join(parts, " · ")
}

func (m *Model) renderFilesBuffer(f *frame, r shell.Rect, v *filesView) {
	p := m.colors()
	b := v.buffer()
	x, y, w := r.X, r.Y, r.W
	bottom := r.Y + r.H
	// Strip: back to the tree, then one tab per open buffer.
	f.iconButton(m, x, y, 3, " "+m.icon("back"), "files-back", action{Kind: "files-back"}, p.muted, p.panel)
	f.hits[len(f.hits)-1].Label = "Back to the file tree"
	tabs := make([]shell.Surface, len(v.buffers))
	for i, buf := range v.buffers {
		tabs[i] = shell.Surface{ID: strconv.Itoa(i), Kind: "file", Title: safe(singleLine(path.Base(buf.path)))}
	}
	visible, overflow := visibleTabs(tabs, strconv.Itoa(v.active), w-4)
	if overflow {
		f.iconButton(m, x+w-3, y, 3, " "+m.icon("more"), "files-bufs", action{Kind: "files-bufs"}, p.muted, p.panel)
		f.hits[len(f.hits)-1].Label = "Open files · select one"
		visible, _ = visibleTabs(tabs, strconv.Itoa(v.active), w-8)
	}
	cx := x + 4
	for _, slot := range visible {
		i, _ := strconv.Atoi(slot.tab.ID)
		f.tab(m, cx, y, slot.width, slot.tab.Title, "file", "files-buf:"+slot.tab.ID, "files-buf-close:"+slot.tab.ID,
			action{Kind: "files-buf", Index: i}, action{Kind: "files-buf-close", Index: i}, i == v.active)
		cx += slot.width + 1
	}
	y++
	if y >= bottom {
		return
	}
	if doc := m.bufferDoc(b); doc != nil && doc.viewText() != nil {
		m.renderDocBody(f, shell.Rect{X: x, Y: y, W: w, H: bottom - y}, b, doc)
		return
	}
	// Metadata and the buffer's own controls.
	text := b.read != nil && b.read.Kind == protocol.FileReadText
	controls := []struct{ icon, key, help string }{{"refresh", "files-reload", "Reload from disk"}}
	if text {
		controls = append([]struct{ icon, key, help string }{
			{"wrap", "files-wrap", "Wrap long lines · w"}, {"copy", "files-copy", "Copy file text"}}, controls...)
	}
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
	// Text shows its format here; other kinds state theirs in the body, so
	// this line names the path instead.
	meta := safe(singleLine(b.path))
	if text {
		meta = filesBufferMeta(b)
	}
	f.text(x, y, max(0, right-x-1), meta, p.muted, p.panel)
	y++
	// Disk and truncation notices.
	notice := func(state, label, button string, a action) {
		if y >= bottom {
			return
		}
		glyph, ink := panelStatusMark(m, state)
		f.text(x, y, w, "", p.text, p.panel)
		f.text(x, y, 2, glyph, ink, p.panel)
		bw := 0
		if button != "" {
			bw = ansi.StringWidth(button) + 2
			f.compactButton(m, x+w-bw, y, bw, button, a.Kind, a, false, 0)
		}
		f.text(x+2, y, max(0, w-2-bw-1), label, p.text, p.panel)
		y++
	}
	reload := action{Kind: "files-reload"}
	switch {
	case b.disk == "deleted":
		notice("failed", "Deleted on disk", "", reload)
	case b.disk == "changed":
		notice("stale", "Changed on disk", "Reload", reload)
	}
	if b.read != nil && b.read.Truncated {
		notice("unavailable", fmt.Sprintf("Showing first %s of %s", filesSize(protocol.FileTextLimit), filesSize(b.read.Size)), "", reload)
	}
	if b.err != "" && b.read != nil {
		notice("failed", "Reload failed · "+b.err, "", reload)
	}
	// Shared-document states of a text buffer shown through the file view.
	if q, ok := m.quarantinedDoc(b); ok {
		notice("failed", "Earlier edits to this file could not be loaded", "Delete retained edits", action{Kind: "doc-dismiss", ID: q.ID, Value: b.path})
	}
	if len(b.docLost) > 0 && y < bottom {
		m.docKeptRow(f, x, y, w, nil, b)
		y++
	}
	switch {
	case b.docRO != "":
		notice("unavailable", "Read-only · "+b.docRO, "", reload)
	case b.docErr != "":
		notice("failed", "Editing unavailable · "+b.docErr, "Retry", action{Kind: "doc-retry"})
	}
	body := shell.Rect{X: x, Y: y, W: w, H: max(0, bottom-y)}
	if body.H == 0 {
		return
	}
	status := func(state, label string) {
		glyph, ink := panelStatusMark(m, state)
		f.text(x, y, 2, glyph, ink, p.panel)
		f.text(x+2, y, w-2, label, p.muted, p.panel)
	}
	switch {
	case b.read == nil && b.err != "":
		status("failed", "Unavailable · "+b.err)
		return
	case b.read == nil:
		status("pending", "Reading…")
		return
	case b.read.Kind == protocol.FileReadNotRegular && b.read.LinkTarget != "":
		f.text(x, y, w, "→ "+safe(singleLine(b.read.LinkTarget)), p.text, p.panel)
		return
	case !text:
		status("unavailable", filesBufferMeta(b))
		return
	case len(b.sourceLines()) == 0:
		f.text(x, y, w, "Empty file", p.muted, p.panel)
		return
	}
	gutter := len(strconv.Itoa(len(b.sourceLines()))) + 1
	room := max(1, w-1-gutter)
	rows, widest := b.textRows(room)
	f.filesText = body
	f.filesTextMax = max(0, len(rows)-body.H)
	if !b.wrap {
		// One more cell than the widest line leaves room for the end marker.
		f.filesTextHMax = max(0, widest-room+1)
	}
	offset := min(max(0, b.scroll), f.filesTextMax)
	hoff := min(max(0, b.hscroll), f.filesTextHMax)
	f.hits = append(f.hits, hit{Rect: body, Action: action{Kind: "files-text-body"}, Label: "File text · arrows scroll, w wraps, Backspace returns to the tree", Key: "files-text"})
	for i := 0; i < body.H && offset+i < len(rows); i++ {
		row := rows[offset+i]
		ry := body.Y + i
		f.text(x, ry, gutter, fmt.Sprintf("%*s", gutter-1, row.number), p.muted, p.panel)
		line := row.text
		lw := ansi.StringWidth(line)
		tx, tw := x+gutter, room
		if hoff > 0 {
			if lw > hoff {
				line = cutCells(line, hoff, lw)
			} else {
				line = ""
			}
		}
		clipped := ansi.StringWidth(line) > tw
		if hoff > 0 {
			f.text(tx, ry, 1, "‹", p.muted, p.panel)
			tx, tw = tx+1, tw-1
			line = cutCells(line, 1, max(1, ansi.StringWidth(line)))
		}
		if clipped {
			f.text(tx, ry, tw, cutCells(line, 0, max(0, tw-1)), p.text, p.panel)
			f.text(tx+tw-1, ry, 1, "›", p.muted, p.panel)
			continue
		}
		f.text(tx, ry, tw, line, p.text, p.panel)
	}
	f.scrollbar(m, shell.Rect{X: x + w - 1, Y: body.Y, W: 1, H: body.H}, "files-text", len(rows), body.H, offset, p.panel)
}
