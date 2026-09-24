package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
	"github.com/rivo/uniseg"
)

// attachment_viewer.go is the centered, always read-only attachment viewer
// (docs/design/activity.md › Clipboard intake and read-only previews). It
// shows a copy of one attachment taken when it opened, so later draft or
// snapshot changes never alter what is on screen. Content is untrusted: it
// passes through safe() before painting, links are never fetched, and nothing
// is executed or handed to an editor.

// attachmentViewer is the open viewer's client-local state.
type attachmentViewer struct {
	att protocol.Attachment
	// draft marks an unsent composer attachment, whose content is captured
	// only at Send; the client never reads its source to preview it.
	draft bool
	// origin is the focus key restored when the viewer closes.
	origin            string
	preview, expanded bool
	scroll            int
	grab              int
	// Wrapped body lines are cached per width, mode and palette.
	cacheKey   string
	cacheLines []viewerLine
	// clean is the sanitized content split into source lines, computed once;
	// nothing else reads the full content per frame.
	clean []string
}

// sourceLines sanitizes the content once and splits it into source lines,
// dropping the empty line after a final newline.
func (vw *attachmentViewer) sourceLines() []string {
	if vw.clean == nil {
		lines := strings.Split(safe(vw.att.Content), "\n")
		if len(lines) > 1 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		vw.clean = lines
	}
	return vw.clean
}

// hardWrap splits a line into rows of at most width cells at grapheme
// boundaries, keeping every character (spaces included) and never splitting
// a wide grapheme, so rejoined rows equal the line.
func hardWrap(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	var rows []string
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(line)
	for g.Next() {
		c := g.Str()
		w := g.Width()
		if used > 0 && used+w > width {
			rows = append(rows, b.String())
			b.Reset()
			used = 0
		}
		b.WriteString(c)
		used += w
	}
	return append(rows, b.String())
}

type viewerLine struct {
	number string // raw mode's line number, empty on wrapped continuations
	text   string
	styled bool // renderer-generated SGR from markdownLines
	// wrap marks a raw-mode row that continues into the next row as a
	// soft-wrap of the same source line (hardWrap produced more than one
	// row for it, and this is not the last one).
	wrap bool
}

// viewerIcons are the viewer's own glyphs: Nerd Fonts v3.4.0 cod-eye and
// cod-file_media, with plain fallbacks.
var viewerIcons = map[string]struct{ glyph, plain string }{
	"eye":   {"", "o"},
	"image": {"", "I"},
}

func (m *Model) viewerIcon(name string) string {
	if i, ok := viewerIcons[name]; ok {
		if m.plainIcons {
			return i.plain
		}
		return i.glyph
	}
	return m.icon(name)
}

func (m *Model) attachmentKindIcon(kind string) string {
	switch kind {
	case "image":
		return m.viewerIcon("image")
	case "git-diff":
		return m.icon("git")
	case "terminal-output":
		return m.icon("terminal")
	}
	return m.icon("files")
}

func attachmentMarkdown(a protocol.Attachment) bool {
	for _, s := range []string{a.Name, a.Source} {
		s = strings.ToLower(strings.TrimSpace(s))
		if strings.HasSuffix(s, ".md") || strings.HasSuffix(s, ".markdown") {
			return true
		}
	}
	return false
}

func attachmentSize(n int) string {
	if n < 1024 {
		if n == 1 {
			return "1 byte"
		}
		return strconv.Itoa(n) + " bytes"
	}
	return fmt.Sprintf("%.1f KiB · %d bytes", float64(n)/1024, n)
}

// attachmentMeta is the one-line kind and size summary used by activity rows.
func attachmentMeta(a protocol.Attachment) string {
	return safe(singleLine(a.Kind)) + " · " + attachmentSize(len(a.Content))
}

// openAttachmentViewer resolves an attachment-view action. Value names the
// source: "draft" (Index into the composer's attachments), "queue" (ID is the
// queued prompt) or "activity" (ID is a retained activity, including child
// activity). The viewer keeps a copy of the resolved attachment.
func (m *Model) openAttachmentViewer(a action) tea.Cmd {
	var (
		list   []protocol.Attachment
		origin string
	)
	t := m.thread()
	source, name, checked := strings.Cut(a.Value, ":")
	switch source {
	case "draft":
		list, origin = m.viewState().Attachments, "attachments"
	case "queue":
		origin = "queue"
		for _, q := range t.Queue {
			if q.ID == a.ID {
				list = q.Attachments
			}
		}
	case "activity":
		origin = fmt.Sprintf("attachment:%s:%d", a.ID, a.Index)
		find := func(items []protocol.Activity) {
			for _, act := range items {
				if list == nil && act.ID == a.ID && act.Prompt != nil {
					list = act.Prompt.Attachments
				}
			}
		}
		find(t.Activity)
		for _, c := range t.Children {
			find(c.Activity)
		}
	}
	if a.Index < 0 || a.Index >= len(list) || checked && list[a.Index].Name != name {
		// Never open a different attachment that now sits at this index.
		return m.showNoticeAs(noticeUnavailable, "That attachment changed or is no longer available")
	}
	if m.focus != "" && !strings.HasPrefix(m.focus, "menu:") && !strings.HasPrefix(m.focus, "viewer-") {
		origin = m.focus
	}
	m.menu = nil
	m.projectMode = ""
	m.contextMenu = nil
	m.selecting, m.selectedText = false, ""
	m.hover = ""
	m.drag = shell.NoDivider
	m.scrollDrag = ""
	m.viewer = &attachmentViewer{att: list[a.Index], draft: source == "draft", origin: origin}
	return m.setFocus("viewer-body")
}

func (m *Model) closeAttachmentViewer() tea.Cmd {
	if m.viewer == nil {
		return nil
	}
	origin := m.viewer.origin
	m.viewer = nil
	m.hover = ""
	m.selecting, m.selectedText = false, ""
	if m.scrollDrag == "viewer" {
		m.scrollDrag = ""
	}
	cmd := m.setFocus(origin)
	m.configureInputs()
	return cmd
}

// viewerAction handles the viewer's own controls.
func (m *Model) viewerAction(a action) tea.Cmd {
	vw := m.viewer
	if vw == nil {
		return nil
	}
	switch a.Kind {
	case "viewer-close":
		return m.closeAttachmentViewer()
	case "viewer-mode":
		if !attachmentMarkdown(vw.att) || vw.att.Kind == "image" {
			return m.showNoticeAs(noticeUnavailable, "Preview is available for Markdown only")
		}
		vw.preview = !vw.preview
		vw.scroll = 0
	case "viewer-expand":
		vw.expanded = !vw.expanded
	}
	return nil
}

// viewerRect is the dialog's outlined rectangle: a bounded centered size, or
// the application area between the top chrome row and the status row.
func (m *Model) viewerRect() shell.Rect {
	if m.viewer != nil && m.viewer.expanded {
		y := min(1, max(0, m.height-2))
		return shell.Rect{X: 0, Y: y, W: max(1, m.width), H: max(1, m.height-1-y)}
	}
	w := min(m.width, min(96, max(20, m.width-8)))
	h := min(m.height, min(32, max(10, m.height-6)))
	return shell.Rect{X: max(0, (m.width-w)/2), Y: max(0, (m.height-h)/2), W: max(1, w), H: max(1, h)}
}

type viewerLayout struct {
	r                      shell.Rect
	x, w                   int // interior content column and width
	closeX, expandX, modeX int
	modeW                  int
	pairs                  [][2]string
	pairRows               int
	body                   shell.Rect // full body, including the line-number gutter
}

func (m *Model) viewerPairs() [][2]string {
	a := m.viewer.att
	pairs := [][2]string{{"Kind", safe(singleLine(a.Kind))}}
	if src := safe(singleLine(a.Source)); src != "" && src != safe(singleLine(a.Name)) {
		pairs = append(pairs, [2]string{"Source", src})
	}
	if m.viewer.draft && a.Content == "" {
		pairs = append(pairs, [2]string{"Size", "not captured yet"})
	} else {
		pairs = append(pairs, [2]string{"Size", attachmentSize(len(a.Content))})
	}
	if attachmentMarkdown(a) && a.Kind != "image" {
		mode := "Raw"
		if m.viewer.preview {
			mode = "Preview"
		}
		pairs = append(pairs, [2]string{"Mode", mode})
	}
	return pairs
}

func (m *Model) viewerLayout() viewerLayout {
	r := m.viewerRect()
	l := viewerLayout{r: r, x: r.X + 2, w: max(0, r.W-4)}
	l.closeX = r.X + r.W - 4
	l.expandX = l.closeX - 3
	if attachmentMarkdown(m.viewer.att) && m.viewer.att.Kind != "image" {
		l.modeW = 5
		if m.plainIcons {
			l.modeW = 9
		}
	}
	l.modeX = l.expandX - l.modeW
	l.pairs = m.viewerPairs()
	// Interior rows: header, rule, pairs, rule, body, rule, hint.
	available := r.H - 2 - 5
	l.pairRows = min(len(l.pairs), max(0, available-3))
	bodyY := r.Y + 3 + l.pairRows + 1
	bh := max(0, available-l.pairRows)
	l.body = shell.Rect{X: l.x, Y: bodyY, W: max(0, r.W-5), H: bh}
	return l
}

// viewerState is a one-line honest state shown instead of content.
func (m *Model) viewerState() string {
	a := m.viewer.att
	switch {
	case a.Kind == "image":
		return "Image preview unavailable"
	case a.Content == "" && m.viewer.draft:
		return "Captured when you send"
	case a.Content == "":
		return "Empty capture"
	}
	return ""
}

func (m *Model) viewerLines(width int) []viewerLine {
	vw := m.viewer
	key := fmt.Sprintf("%d/%t/%t/%d/%t", width, vw.preview, m.state.Light, m.colorProfile, m.plainIcons)
	if vw.cacheKey == key && vw.cacheLines != nil {
		return vw.cacheLines
	}
	var out []viewerLine
	if vw.preview {
		for _, line := range markdownLines(vw.att.Content, max(1, width), m.colors()) {
			out = append(out, viewerLine{text: line, styled: true})
		}
	} else {
		source := vw.sourceLines()
		room := max(1, width-m.viewerGutter())
		for i, line := range source {
			parts := hardWrap(line, room)
			for j, part := range parts {
				n := ""
				if j == 0 {
					n = strconv.Itoa(i + 1)
				}
				out = append(out, viewerLine{number: n, text: part, wrap: j < len(parts)-1})
			}
		}
	}
	vw.cacheKey, vw.cacheLines = key, out
	return out
}

// viewerGutter is the raw mode's line-number column width, including its gap.
func (m *Model) viewerGutter() int {
	if m.viewer.preview || m.viewerState() != "" {
		return 0
	}
	return len(strconv.Itoa(len(m.viewer.sourceLines()))) + 2
}

func (m *Model) viewerBodyLines(l viewerLayout) []viewerLine {
	if m.viewerState() != "" || l.body.W <= 0 {
		return nil
	}
	return m.viewerLines(l.body.W)
}

func (m *Model) renderViewer(f *frame) {
	vw := m.viewer
	p := m.colors()
	l := m.viewerLayout()
	r := l.r
	m.renderModalBackdrop(f)
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, componentState{Focused: true}, p.text, p.input), p.canvas)
	f.hits = nil
	f.scrollbars = nil
	if r.W < 12 || r.H < 4 {
		// Too small for any content: keep only a reachable close control.
		if r.W >= 3 && r.H >= 1 {
			f.iconButton(m, r.X+max(0, r.W-3), r.Y, 3, centered(m.icon("close"), 3), "viewer-close", action{Kind: "viewer-close"}, p.muted, p.input)
		}
		return
	}
	lines := m.viewerBodyLines(l)
	body := l.body
	gutter := m.viewerGutter()
	maxOffset := max(0, len(lines)-body.H)
	offset := min(max(0, vw.scroll), maxOffset)
	if body.H > 0 && body.W > 0 {
		f.hits = append(f.hits, hit{Rect: body, Label: "Attachment · wheel / arrows to scroll · read-only", Key: "viewer-body"})
		f.viewerBody = shell.Rect{X: body.X + gutter, Y: body.Y, W: max(0, body.W-gutter), H: body.H}
		f.viewerMax = maxOffset
	}
	// Header: kind icon and name, then fixed control slots at the right.
	y := r.Y + 1
	titleW := max(0, l.modeX-1-l.x)
	if l.modeW == 0 {
		titleW = max(0, l.expandX-1-l.x)
	}
	icon := m.attachmentKindIcon(vw.att.Kind)
	iw := ansi.StringWidth(icon)
	name := safe(singleLine(vw.att.Name))
	if name == "" {
		name = "Attachment"
	}
	f.text(l.x, y, min(iw, titleW), icon, p.blue, p.input)
	f.componentText(l.x+iw+2, y, max(0, titleW-iw-2), name, componentVisual{foreground: p.text, background: p.input, bold: true})
	if l.modeW > 0 && l.modeX > l.x {
		key := "viewer-mode"
		if vw.preview {
			label := "Raw"
			v := m.componentStyle(squareFill, m.controlState(false, key), p.blue, p.input)
			f.styledButton(l.modeX+l.modeW-1-len(label), y, len(label), label, key, action{Kind: "viewer-mode"}, v)
			f.hits[len(f.hits)-1].Label = "Show raw Markdown · p"
		} else if m.plainIcons {
			label := "Preview"
			v := m.componentStyle(squareFill, m.controlState(false, key), p.blue, p.input)
			f.styledButton(l.modeX+l.modeW-1-len(label), y, len(label), label, key, action{Kind: "viewer-mode"}, v)
			f.hits[len(f.hits)-1].Label = "Preview Markdown · p"
		} else {
			f.iconButton(m, l.modeX, y, l.modeW, fit(strings.Repeat(" ", l.modeW-2)+m.viewerIcon("eye"), l.modeW), key, action{Kind: "viewer-mode"}, p.muted, p.input)
			f.hits[len(f.hits)-1].Label = "Preview Markdown · p"
		}
	}
	expand, expandLabel := "maximize", "Expand viewer · f"
	if vw.expanded {
		expand, expandLabel = "restore", "Restore viewer size · f"
	}
	if l.expandX > l.x {
		f.iconButton(m, l.expandX, y, 3, centered(m.icon(expand), 3), "viewer-expand", action{Kind: "viewer-expand"}, p.muted, p.input)
		f.hits[len(f.hits)-1].Label = expandLabel
	}
	f.iconButton(m, l.closeX, y, 3, centered(m.icon("close"), 3), "viewer-close", action{Kind: "viewer-close"}, p.muted, p.input)
	f.hits[len(f.hits)-1].Label = "Close viewer · Esc"
	panelRuleOn(f, m, l.x, y+1, l.w, p.input)
	for i := 0; i < l.pairRows; i++ {
		pair := l.pairs[i]
		value := pair[1]
		room := max(1, l.w-ansi.StringWidth(pair[0])-2)
		if pair[0] == "Source" {
			value = truncatePathLeft(value, room)
		}
		panelPairRowStyled(f, l.x, y+2+i, l.w, pair[0], value, p.muted, p.text, p.input)
	}
	panelRuleOn(f, m, l.x, body.Y-1, l.w, p.input)
	if m.focus == "viewer-body" && body.H > 0 && body.X > r.X {
		// The body has no reserved end cell: the mark takes the interior
		// blank cell before its first row.
		v := m.componentStyle(squareFill, componentState{Focused: true}, p.text, p.input)
		f.focusMark(body.X-1, body.Y, v, p.input)
	}
	if state := m.viewerState(); state != "" && body.H > 0 {
		f.text(body.X, body.Y, body.W, state, p.muted, p.input)
	}
	for i := 0; i < body.H && offset+i < len(lines); i++ {
		line := lines[offset+i]
		yy := body.Y + i
		if line.wrap {
			if f.wrapRows == nil {
				f.wrapRows = map[int]bool{}
			}
			f.wrapRows[yy] = true
		}
		if gutter > 0 {
			f.text(body.X, yy, gutter-2, fmt.Sprintf("%*s", gutter-2, line.number), p.muted, p.input)
		}
		tx, tw := body.X+gutter, max(0, body.W-gutter)
		if line.styled {
			// markdownLines sanitizes its source and decoded values; only its
			// own SGR reaches the frame.
			f.put(shell.Rect{X: tx, Y: yy, W: tw, H: 1}, onBackground(fit(line.text, tw), p.text, p.input))
		} else {
			f.text(tx, yy, tw, line.text, p.text, p.input)
		}
	}
	if body.H > 0 {
		f.scrollbar(m, shell.Rect{X: r.X + r.W - 2, Y: body.Y, W: 1, H: body.H}, "viewer", len(lines), body.H, offset, p.input)
	}
	panelRuleOn(f, m, l.x, r.Y+r.H-3, l.w, p.input)
	hint := ""
	if len(lines) > body.H {
		hint = "↑ ↓  PgUp PgDn  "
	}
	if l.modeW > 0 {
		if vw.preview {
			hint += "p Raw  "
		} else {
			hint += "p Preview  "
		}
	}
	if vw.expanded {
		hint += "f Restore  "
	} else {
		hint += "f Expand  "
	}
	hint += "Esc Close · read-only"
	f.text(l.x, r.Y+r.H-2, l.w, hint, p.muted, p.input)
}

// viewerFocusKeys is the viewer's Tab order.
func (m *Model) viewerFocusKeys() []string {
	keys := []string{"viewer-body"}
	for _, h := range m.measure().hits {
		if h.Key != "viewer-body" && strings.HasPrefix(h.Key, "viewer-") {
			keys = append(keys, h.Key)
		}
	}
	return keys
}

func (m *Model) viewerScrollBy(delta int, f frame) {
	m.viewer.scroll = min(f.viewerMax, max(0, min(f.viewerMax, max(0, m.viewer.scroll))+delta))
}

// viewerKey captures every key while the viewer is open, except the global
// detach/suspend and the copy shortcuts handled by the caller.
func (m *Model) viewerKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	s := k.String()
	switch s {
	case "ctrl+q", "ctrl+z", "ctrl+c", "ctrl+shift+c":
		return nil, false
	}
	if k.Keystroke() == "super+c" {
		return nil, false
	}
	if contextMenuKey(k) {
		// Let the caller's normal context-menu handling open the "Selected
		// text" menu for the focused viewer body over a live selection.
		return nil, false
	}
	switch s {
	case "esc":
		return m.closeAttachmentViewer(), true
	case "p":
		return m.viewerAction(action{Kind: "viewer-mode"}), true
	case "f":
		return m.viewerAction(action{Kind: "viewer-expand"}), true
	case "tab", "shift+tab":
		keys := m.viewerFocusKeys()
		i := -1
		for j, key := range keys {
			if key == m.focus {
				i = j
			}
		}
		step := 1
		if s == "shift+tab" {
			step = -1
			if i < 0 {
				i = 0
			}
		}
		return m.setFocus(keys[(i+step+len(keys))%len(keys)]), true
	case "enter", " ", "space":
		for _, h := range m.measure().hits {
			if h.Key == m.focus && h.Key != "viewer-body" && strings.HasPrefix(h.Key, "viewer-") {
				return m.activate(h.Action), true
			}
		}
		return nil, true
	case "up", "down", "pgup", "pgdown", "home", "end":
		f := m.measure()
		page := max(1, m.viewerLayout().body.H-1)
		switch s {
		case "up":
			m.viewerScrollBy(-1, f)
		case "down":
			m.viewerScrollBy(1, f)
		case "pgup":
			m.viewerScrollBy(-page, f)
		case "pgdown":
			m.viewerScrollBy(page, f)
		case "home":
			m.viewer.scroll = 0
		case "end":
			m.viewer.scroll = f.viewerMax
		}
		return nil, true
	}
	return nil, true
}

// viewerMouse handles pointer input while the viewer is open. Motion and
// release fall through to the shared handler (hover and text selection),
// except while dragging the viewer's scrollbar thumb.
func (m *Model) viewerMouse(msg tea.MouseMsg, f frame) (tea.Cmd, bool) {
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch p.Button {
		case tea.MouseWheelUp:
			m.viewerScrollBy(-3, f)
		case tea.MouseWheelDown:
			m.viewerScrollBy(3, f)
		}
		return nil, true
	case tea.MouseClickMsg:
		if p.Button == tea.MouseRight {
			if len(m.menu) == 0 && m.selectionContains(f, p.X, p.Y) {
				return m.openSelectionContextMenu("viewer-body"), true
			}
			return nil, true
		}
		if p.Button != tea.MouseLeft {
			return nil, true
		}
		if !m.viewerRect().Contains(p.X, p.Y) {
			// Consume the click: it must never reach controls underneath.
			return m.closeAttachmentViewer(), true
		}
		for i := len(f.hits) - 1; i >= 0; i-- {
			h := f.hits[i]
			if !h.Rect.Contains(p.X, p.Y) {
				continue
			}
			if h.Action.Kind == "scrollbar" {
				target := f.scrollbars[h.Action.ID]
				if h.Action.Value == "thumb" {
					m.scrollDrag = "viewer"
					m.viewer.grab = h.Action.Index - target.Bar.ThumbStart
				} else {
					m.viewer.scroll = min(f.viewerMax, max(0, target.Bar.PageAt(h.Action.Index)))
				}
				return nil, true
			}
			cmd := m.setFocus(h.Key)
			if h.Key == "viewer-body" {
				if f.viewerBody.Contains(p.X, p.Y) {
					m.selecting = true
					m.selectedText = ""
					m.selectionRegion = f.viewerBody
					m.selectionBasis = m.selectionBasisFor(f)
					m.selectionStart = [2]int{p.X, p.Y}
					m.selectionEnd = m.selectionStart
				}
				return cmd, true
			}
			return tea.Batch(cmd, m.activate(h.Action)), true
		}
		return nil, true
	case tea.MouseMotionMsg:
		if m.scrollDrag == "viewer" {
			if target, ok := f.scrollbars["viewer"]; ok {
				m.viewer.scroll = min(f.viewerMax, max(0, target.Bar.DragTo(p.Y-target.Rect.Y, m.viewer.grab)))
			}
			return nil, true
		}
	}
	return nil, false
}

// onBackground paints renderer-styled text over a dialog fill: every SGR
// reset inside it re-applies the base ink and fill, so spans after a styled
// segment keep the dialog background instead of the terminal default.
func onBackground(text, fg, bg string) string {
	sample := style(fg, bg).Render("x")
	i := strings.Index(sample, "x")
	if i <= 0 {
		return text
	}
	base := sample[:i]
	text = strings.ReplaceAll(text, "\x1b[0m", "\x1b[m")
	return base + strings.ReplaceAll(text, "\x1b[m", "\x1b[m"+base) + "\x1b[m"
}
