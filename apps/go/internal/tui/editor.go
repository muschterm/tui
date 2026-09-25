package tui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// editor.go is the cell-native text editor over a document session's
// replica (ADR 0008 component rules, ADR 0022 documents). The Files text
// pane is one focus stop ("files-text"); Enter or a click enters edit mode
// when this client may edit (acquiring the single editor role first), and
// Esc leaves it. While editing, printable keys, Enter and Tab insert text and
// the editor's own bindings below apply; F-keys, Ctrl+Q and the context-menu
// key keep their global meaning. Ctrl+Z is Undo while editing (Suspend stays
// available outside edit mode and in the Commands menu).
//
// Positions are line/byte pairs on grapheme boundaries; the replica is
// addressed in UTF-16 only at its boundary. Every edit is one replica
// transaction and one pending update; undo steps are bounded explicitly so
// one step never inserts and deletes the same text.

// docUndoGap starts a new undo step after this pause in typing.
const docUndoGap = time.Second

// docEditor is the cursor and selection of one document in this client.
type docEditor struct {
	cur, anchor edPos
	// goal is the display column vertical moves keep; -1 when unset.
	goal     int
	lastKind string
	lastAt   int
	lastTime time.Time
	dragging bool
	// now is injectable for tests.
	now func() time.Time
}

func (e *docEditor) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

func (e *docEditor) hasSel() bool { return e.cur != e.anchor }

func (e *docEditor) selRange() (edPos, edPos) {
	if e.anchor.less(e.cur) {
		return e.anchor, e.cur
	}
	return e.cur, e.anchor
}

// snap keeps p inside t on a grapheme boundary.
func (e *docEditor) snap(t *edText, p edPos) edPos {
	p = t.clamp(p)
	p.col = snapGrapheme(t.lines[p.line], p.col)
	return p
}

func (e *docEditor) clamp(t *edText) {
	e.cur, e.anchor = e.snap(t, e.cur), e.snap(t, e.anchor)
}

// selectedText returns the selected text.
func (e *docEditor) selectedText(t *edText) string {
	a, z := e.selRange()
	return t.slice(a, z)
}

// docInput returns the session and buffer receiving editor keys, if any.
// Transient overlays pause input; a changed buffer, focus or role ends edit
// mode.
func (m *Model) docInput() (*docSession, *fileBuffer, bool) {
	if m.docEdit == "" {
		return nil, nil, false
	}
	s := m.docs[m.docEdit]
	var b *fileBuffer
	if v := m.currentFilesView(); v != nil {
		b = v.buffer()
	}
	if s == nil || b == nil || b.doc != s.id || m.focus != "files-text" || !m.filesVisible() || !s.editable(m.clientID) && !s.recovering() {
		m.docEdit = ""
		return nil, nil, false
	}
	if len(m.menu) > 0 || m.viewer != nil || m.docReview != nil || m.settingsPage != "" || m.terminalTooSmall() || s.recovering() {
		return nil, nil, false
	}
	return s, b, true
}

// startDocEdit enters edit mode on the active buffer's document, acquiring
// the editor role first when nobody holds it.
func (m *Model) startDocEdit(b *fileBuffer, s *docSession) tea.Cmd {
	switch {
	case s == nil || s.rep == nil:
		return m.showNoticeAs(noticeUnavailable, "The document is still opening")
	case s.gone != "":
		return m.showNoticeAs(noticeUnavailable, "The document is closed · reopen the file")
	case s.status.State == protocol.DocumentStateReadOnly:
		return m.showNoticeAs(noticeUnavailable, "Read-only · "+docReason(s.status.Reason, "the file cannot be edited"))
	case s.status.Editor == m.clientID:
		m.docEdit = s.id
		m.docEnsureVisible(s, b)
		m.markDirty()
		return nil
	case s.otherEditor(m.clientID):
		return m.showNoticeAs(noticeUnavailable, protocol.DocumentSimultaneousUnavailable+" · another client is editing · Take over")
	case s.editing != nil:
		return nil
	}
	c := m.docCommand(protocol.DocumentKindEdit, s.id)
	s.editing, s.wantEdit = &c, true
	return m.sendDocCommand(c, "", "")
}

// enterWantedEdit enters edit mode once a requested role is confirmed, if
// the document is still the focused buffer.
func (m *Model) enterWantedEdit(s *docSession) tea.Cmd {
	if !s.wantEdit || s.editing != nil || !s.editable(m.clientID) {
		return nil
	}
	s.wantEdit = false
	if v := m.currentFilesView(); v != nil && v.buffer() != nil && v.buffer().doc == s.id && m.focus == "files-text" {
		m.docEdit = s.id
		m.docEnsureVisible(s, v.buffer())
	}
	return nil
}

// takeOverDoc asks for confirmation before taking the editor role.
func (m *Model) takeOverDoc(s *docSession) {
	m.docConfirm("Take over editing · ", s.path, []string{
		"Another client is editing this file.",
		"Taking over refuses its edits not stored yet;",
		"that client keeps them as a copy to recover.",
	}, "", menuItem{Label: "Take over", Action: action{Kind: "doc-take-confirm", ID: s.id}})
}

func isFKey(code rune) bool { return code >= tea.KeyF1 && code <= tea.KeyF63 }

// docRecoveringInput reports that edit mode waits for a recovering
// document: keys are held back from the Files controls meanwhile.
func (m *Model) docRecoveringInput() bool {
	m.docInput()
	s := m.docs[m.docEdit]
	return s.recovering() && m.focus == "files-text" && len(m.menu) == 0 && m.viewer == nil && m.docReview == nil && m.settingsPage == ""
}

// docKey handles a key press in edit mode.
func (m *Model) docKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.docRecoveringInput() {
		switch key := k.Key(); {
		case isFKey(key.Code), key.Mod&tea.ModCtrl != 0 && key.Code == 'q', contextMenuKey(k):
			return nil, false
		case key.Code == tea.KeyEscape:
			m.docEdit = ""
			return nil, true
		}
		return m.showNoticeAs(noticeUnavailable, "Recovering your edits · typing resumes when the document is back"), true
	}
	s, b, ok := m.docInput()
	if !ok {
		return nil, false
	}
	key := k.Key()
	mod := key.Mod &^ lockMods
	shift, ctrl, alt, super := mod&tea.ModShift != 0, mod&tea.ModCtrl != 0, mod&tea.ModAlt != 0, mod&tea.ModSuper != 0
	code := key.Code
	switch {
	case isFKey(code), ctrl && code == 'q', contextMenuKey(k):
		return nil, false
	case code == tea.KeyEscape:
		m.docEdit = ""
		m.markDirty()
		return nil, true
	case ctrl && !shift && code == 'z':
		return m.docUndo(s, b, false), true
	case ctrl && (code == 'y' || shift && code == 'z'):
		return m.docUndo(s, b, true), true
	case (ctrl || super) && code == 'c':
		return m.docCopy(s, false), true
	case ctrl && code == 'x':
		return m.docCopy(s, true), true
	case (ctrl || super) && code == 'v', shift && code == tea.KeyInsert:
		return m.docPasteClipboard(s), true
	case ctrl && code == 'a':
		t := s.rep.txt
		s.ed.anchor, s.ed.cur = edPos{}, edPos{len(t.lines) - 1, len(t.lines[len(t.lines)-1])}
		m.markDirty()
		return nil, true
	case ctrl && code == 's':
		_, text := m.docStatusLine(s)
		return m.showNotice("Edits save automatically · " + text), true
	case code == tea.KeyEnter || code == tea.KeyKpEnter:
		return m.docInsert(s, b, "\n", "other"), true
	case code == tea.KeyTab && !shift && !ctrl && !alt:
		return m.docInsert(s, b, "\t", "other"), true
	case code == tea.KeyBackspace:
		return m.docDelete(s, b, false, ctrl || alt), true
	case code == tea.KeyDelete:
		return m.docDelete(s, b, true, ctrl || alt), true
	}
	if m.docMove(s, b, code, shift, ctrl || alt) {
		return nil, true
	}
	if text := key.Text; text != "" && !ctrl && !alt && !super && printable(text) {
		return m.docInsert(s, b, text, "type"), true
	}
	// Every other key is consumed so it never reaches controls behind the
	// editor.
	return nil, true
}

// docMove moves the cursor for navigation keys; shift extends the selection.
func (m *Model) docMove(s *docSession, b *fileBuffer, code rune, shift, word bool) bool {
	t := s.rep.txt
	e := &s.ed
	cur := e.cur
	line := t.lines[cur.line]
	vertical := false
	switch code {
	case tea.KeyLeft, tea.KeyKpLeft:
		switch {
		case e.hasSel() && !shift:
			cur, _ = e.selRange()
		case word:
			if cur.col == 0 && cur.line > 0 {
				cur = edPos{cur.line - 1, len(t.lines[cur.line-1])}
			} else {
				cur.col = wordLeft(line, cur.col)
			}
		case cur.col > 0:
			cur.col = graphemeBefore(line, cur.col)
		case cur.line > 0:
			cur = edPos{cur.line - 1, len(t.lines[cur.line-1])}
		}
	case tea.KeyRight, tea.KeyKpRight:
		switch {
		case e.hasSel() && !shift:
			_, cur = e.selRange()
		case word:
			if cur.col == len(line) && cur.line < len(t.lines)-1 {
				cur = edPos{cur.line + 1, 0}
			} else {
				cur.col = wordRight(line, cur.col)
			}
		case cur.col < len(line):
			cur.col = graphemeAfter(line, cur.col)
		case cur.line < len(t.lines)-1:
			cur = edPos{cur.line + 1, 0}
		}
	case tea.KeyUp, tea.KeyKpUp, tea.KeyDown, tea.KeyKpDown, tea.KeyPgUp, tea.KeyKpPgUp, tea.KeyPgDown, tea.KeyKpPgDown:
		n := 1
		if code == tea.KeyPgUp || code == tea.KeyKpPgUp || code == tea.KeyPgDown || code == tea.KeyKpPgDown {
			n = max(1, b.docH-1)
		}
		if code == tea.KeyUp || code == tea.KeyKpUp || code == tea.KeyPgUp || code == tea.KeyKpPgUp {
			n = -n
		}
		cur = m.docVertical(s, b, n)
		vertical = true
		if code == tea.KeyPgUp || code == tea.KeyKpPgUp || code == tea.KeyPgDown || code == tea.KeyKpPgDown {
			b.scroll += n
		}
	case tea.KeyHome, tea.KeyKpHome:
		if word {
			cur = edPos{}
		} else {
			cur.col = 0
		}
	case tea.KeyEnd, tea.KeyKpEnd:
		if word {
			cur = edPos{len(t.lines) - 1, len(t.lines[len(t.lines)-1])}
		} else {
			cur.col = len(line)
		}
	default:
		return false
	}
	if !vertical {
		e.goal = -1
	}
	e.cur = cur
	if !shift {
		e.anchor = cur
	}
	e.lastKind = ""
	m.docEnsureVisible(s, b)
	m.markDirty()
	return true
}

// docVertical returns the position n visual rows from the cursor, keeping
// the goal column.
func (m *Model) docVertical(s *docSession, b *fileBuffer, n int) edPos {
	t := s.rep.txt
	e := &s.ed
	room := docWrapRoom(b)
	row, x := docCursorRow(t, e.cur, room)
	if e.goal < 0 {
		e.goal = x
	}
	target := row + n
	total := t.totalRows(room)
	switch {
	case target < 0:
		return edPos{}
	case target >= total:
		last := len(t.lines) - 1
		return edPos{last, len(t.lines[last])}
	}
	return docRowPos(t, target, e.goal, room)
}

// normalizeDocText makes inserted text what the server accepts: "\r\n" and
// lone "\r" become "\n", and NUL is removed.
func normalizeDocText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\x00", "")
}

// docEncodedSize is the file size after replacing [a, z) with text.
func docEncodedSize(s *docSession, a, z edPos, text string) int {
	t := s.rep.txt
	removed := 0
	if a.line == z.line {
		removed = z.col - a.col
	} else {
		removed = len(t.lines[a.line]) - a.col + 1 + z.col
		for i := a.line + 1; i < z.line; i++ {
			removed += len(t.lines[i]) + 1
		}
	}
	size := t.size - removed + len(text)
	if s.status.Newline == "crlf" {
		size += len(t.lines) - 1 - (z.line - a.line) + strings.Count(text, "\n")
	}
	if s.status.BOM {
		size += 3
	}
	return size
}

// docReplace replaces [a, z) with text as one edit of the given kind.
func (m *Model) docReplace(s *docSession, b *fileBuffer, a, z edPos, text, kind string) tea.Cmd {
	if len(s.pending) >= docMaxPending || s.pendingBytes() >= docMaxPendingBytes {
		return m.showNoticeAs(noticeUnavailable, "Waiting for the server to store earlier edits · try again shortly")
	}
	if docEncodedSize(s, a, z, text) > protocol.DocumentMaxBytes {
		return m.showNoticeAs(noticeUnavailable, "Not inserted · the file would exceed 1 MiB")
	}
	t := s.rep.txt
	e := &s.ed
	at := t.offset(a)
	del := t.offset(z) - at
	if del == 0 && text == "" {
		return nil
	}
	now := e.clock()
	join := kind != "other" && kind == e.lastKind && at == e.lastAt && now.Sub(e.lastTime) <= docUndoGap
	// The lines before the edit: if the replica fails, the intended text is
	// rebuilt from them and kept as the recovery draft.
	before := &edText{lines: slices.Clone(t.lines)}
	if err := s.rep.edit(at, del, text, join); err != nil {
		last := len(before.lines) - 1
		end := edPos{last, len(before.lines[last])}
		intended := before.slice(edPos{}, a) + text + before.slice(z, end)
		return m.docLocalFailure(s, before.slice(edPos{}, end), intended)
	}
	t = s.rep.txt
	end := at + u16Len(text)
	e.cur = e.snap(t, t.posAt(end))
	e.anchor, e.goal = e.cur, -1
	e.lastKind, e.lastTime = kind, now
	e.lastAt = at
	if kind == "type" {
		e.lastAt = end
	}
	m.docEnsureVisible(s, b)
	return m.queueLocal(s)
}

// docInsert inserts text at the cursor, replacing a selection.
func (m *Model) docInsert(s *docSession, b *fileBuffer, text, kind string) tea.Cmd {
	if !utf8.ValidString(text) {
		return m.showNoticeAs(noticeUnavailable, "Not inserted · the text is not valid UTF-8")
	}
	text = normalizeDocText(text)
	a, z := s.ed.selRange()
	if s.ed.hasSel() || strings.Contains(text, "\n") {
		kind = "other"
	}
	if text == "" && a == z {
		return nil
	}
	return m.docReplace(s, b, a, z, text, kind)
}

// docDelete deletes the selection, or the grapheme (word) before or after
// the cursor, joining lines at their ends.
func (m *Model) docDelete(s *docSession, b *fileBuffer, forward, word bool) tea.Cmd {
	t := s.rep.txt
	e := &s.ed
	if e.hasSel() {
		a, z := e.selRange()
		return m.docReplace(s, b, a, z, "", "other")
	}
	a, z := e.cur, e.cur
	line := t.lines[e.cur.line]
	kind := "back"
	if forward {
		kind = "fwd"
		switch {
		case e.cur.col < len(line) && word:
			z.col = wordRight(line, e.cur.col)
		case e.cur.col < len(line):
			z.col = graphemeAfter(line, e.cur.col)
		case e.cur.line < len(t.lines)-1:
			z = edPos{e.cur.line + 1, 0}
		default:
			return nil
		}
	} else {
		switch {
		case e.cur.col > 0 && word:
			a.col = wordLeft(line, e.cur.col)
		case e.cur.col > 0:
			a.col = graphemeBefore(line, e.cur.col)
		case e.cur.line > 0:
			a = edPos{e.cur.line - 1, len(t.lines[e.cur.line-1])}
		default:
			return nil
		}
	}
	if word || a.line != z.line {
		kind = "other"
	}
	return m.docReplace(s, b, a, z, "", kind)
}

// docUndo reverts (or redoes) this client's own most recent edit step.
func (m *Model) docUndo(s *docSession, b *fileBuffer, redo bool) tea.Cmd {
	if len(s.pending) >= docMaxPending || s.pendingBytes() >= docMaxPendingBytes {
		return m.showNoticeAs(noticeUnavailable, "Waiting for the server to store earlier edits · try again shortly")
	}
	base := s.rep.txt.String()
	at, err := s.rep.undoStep(redo)
	if errors.Is(err, errUndoTooLarge) {
		// Anything it already changed is sent like any other edit.
		return tea.Batch(m.queueLocal(s), m.showNoticeAs(noticeUnavailable, "Undo too large here · that step was skipped · edit it manually"))
	}
	if err != nil {
		// What undo intended is unknown: keep the text before it.
		return m.docLocalFailure(s, base, base)
	}
	if at < 0 {
		if redo {
			return m.showNotice("Nothing to redo")
		}
		return m.showNotice("Nothing to undo · only your own edits here can be undone")
	}
	t := s.rep.txt
	e := &s.ed
	e.cur = e.snap(t, t.posAt(at))
	e.anchor, e.goal, e.lastKind = e.cur, -1, ""
	m.docEnsureVisible(s, b)
	return m.queueLocal(s)
}

// docCopy copies (or cuts) the selection.
func (m *Model) docCopy(s *docSession, cut bool) tea.Cmd {
	if !s.ed.hasSel() {
		return m.showNotice("No text is selected")
	}
	cmd := m.copyText(clipboardSafeText(s.ed.selectedText(s.rep.txt)))
	if !cut {
		return cmd
	}
	a, z := s.ed.selRange()
	var b *fileBuffer
	if v := m.currentFilesView(); v != nil {
		b = v.buffer()
	}
	return tea.Batch(cmd, m.docReplace(s, b, a, z, "", "other"))
}

// docLocalFailure recovers from a replica that failed during a local edit:
// the intended text becomes the draft, the replica is rebuilt from the
// server and the draft re-applied (docResync with local).
func (m *Model) docLocalFailure(s *docSession, base, intended string) tea.Cmd {
	s.rep.takeUpdates()
	cmd := m.docResync(s, "the editor lost track of the document", true)
	s.draftBase = base
	s.draftText, s.draftView, s.hasDraft = normalizeDocText(intended), nil, true
	return cmd
}

// docPasteMsg carries a clipboard read for the editor.
type docPasteMsg struct {
	gen  uint64
	id   string
	text string
	err  error
}

// docPasteClipboard reads the local clipboard for the editor. Remote
// sessions use the outer terminal's paste (bracketed paste), like the prompt.
func (m *Model) docPasteClipboard(s *docSession) tea.Cmd {
	if terminalClipboardSession() {
		if strings.TrimSpace(os.Getenv("HERDR_ENV")) == "1" {
			return m.showNoticeAs(noticeUnavailable, "Use your outer terminal's Paste (usually Ctrl+Shift+V)")
		}
		return m.showNoticeAs(noticeUnavailable, "Clipboard read unavailable over SSH · use your terminal's paste shortcut")
	}
	read := m.clipboardRead
	if read == nil {
		read = clipboard.ReadAll
	}
	m.docPasteGen++
	gen, id := m.docPasteGen, s.id
	return func() tea.Msg {
		text, err := read()
		return docPasteMsg{gen: gen, id: id, text: text, err: err}
	}
}

func (m *Model) acceptDocPaste(msg docPasteMsg) tea.Cmd {
	if msg.gen != m.docPasteGen {
		return nil
	}
	m.docPasteGen++
	s, b, ok := m.docInput()
	if !ok || s.id != msg.id {
		return m.showNotice("Paste cancelled · the editor is no longer active")
	}
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Clipboard read failed: "+safe(msg.err.Error())+" · use your terminal's paste shortcut")
	}
	if len(msg.text) > protocol.DocumentMaxBytes {
		return m.showNoticeAs(noticeUnavailable, "Paste unavailable · clipboard text exceeds 1 MiB")
	}
	if msg.text == "" {
		return m.showNotice("Clipboard is empty")
	}
	if !utf8.ValidString(msg.text) {
		return m.showNoticeAs(noticeUnavailable, "Clipboard holds non-text data · nothing pasted")
	}
	return m.docInsert(s, b, msg.text, "other")
}

// docPaste inserts a bracketed paste while editing.
func (m *Model) docPaste(text string) (tea.Cmd, bool) {
	if m.docRecoveringInput() {
		return m.showNoticeAs(noticeUnavailable, "Paste not inserted · recovering your edits"), true
	}
	s, b, ok := m.docInput()
	if !ok {
		return nil, false
	}
	if len(text) > protocol.DocumentMaxBytes {
		return m.showNoticeAs(noticeUnavailable, "Paste unavailable · text exceeds 1 MiB"), true
	}
	if !utf8.ValidString(text) {
		return m.showNoticeAs(noticeUnavailable, "Paste holds non-text data · nothing pasted"), true
	}
	return m.docInsert(s, b, text, "other"), true
}

// docMouse handles pointer input over a document's text: click places the
// cursor (entering edit mode when possible), drag selects.
func (m *Model) docMouse(msg tea.MouseMsg, f frame) (tea.Cmd, bool) {
	if len(m.menu) > 0 || m.viewer != nil || m.docReview != nil || f.docText.W <= 0 {
		return nil, false
	}
	v := m.currentFilesView()
	if v == nil {
		return nil, false
	}
	b := v.buffer()
	s := m.bufferDoc(b)
	if s == nil || s.rep == nil {
		return nil, false
	}
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if p.Button != tea.MouseLeft || !f.docText.Contains(p.X, p.Y) {
			return nil, false
		}
		m.termFocus = ""
		pos := m.docPosAt(s, b, f.docText, p.X, p.Y)
		s.ed.cur, s.ed.goal, s.ed.lastKind = pos, -1, ""
		if p.Mod&tea.ModShift == 0 {
			s.ed.anchor = pos
		}
		s.ed.dragging = true
		m.markDirty()
		focus := m.setFocus("files-text")
		if m.docEdit == s.id {
			return focus, true
		}
		if s.editable(m.clientID) || s.status.Editor == "" && s.status.State != protocol.DocumentStateReadOnly {
			return tea.Batch(focus, m.startDocEdit(b, s)), true
		}
		return focus, true
	case tea.MouseMotionMsg:
		if !s.ed.dragging || p.Button != tea.MouseLeft {
			return nil, false
		}
		y := min(max(p.Y, f.docText.Y-1), f.docText.Y+f.docText.H)
		switch {
		case y < f.docText.Y:
			b.scroll = max(0, b.scroll-1)
			y = f.docText.Y
		case y >= f.docText.Y+f.docText.H:
			b.scroll++
			y = f.docText.Y + f.docText.H - 1
		}
		x := min(max(p.X, f.docText.X), f.docText.X+f.docText.W-1)
		s.ed.cur = m.docPosAt(s, b, f.docText, x, y)
		m.clampFilesBuffer(b)
		m.markDirty()
		return nil, true
	case tea.MouseReleaseMsg:
		if !s.ed.dragging {
			return nil, false
		}
		s.ed.dragging = false
		return nil, false
	}
	return nil, false
}

// docPosAt maps a cell in the text rectangle to a position.
func (m *Model) docPosAt(s *docSession, b *fileBuffer, r shell.Rect, x, y int) edPos {
	t := s.rep.txt
	room := docWrapRoom(b)
	row := b.scroll + (y - r.Y)
	if row >= t.totalRows(room) {
		last := len(t.lines) - 1
		return edPos{last, len(t.lines[last])}
	}
	col := x - r.X
	if room <= 0 {
		col += b.hscroll
	}
	return docRowPos(t, row, col, room)
}
