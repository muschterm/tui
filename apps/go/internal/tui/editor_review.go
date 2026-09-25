package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// editor_review.go is the conflict review of a paused document: a centered
// read-only dialog with the retained versions (GET /v1/documents/{id}/versions)
// as tabs — Changes (a line diff of the disk file against the document),
// Document, Disk and Base — and the three explicit resolutions. Each asks
// for confirmation naming its consequence and sends document.resolve bound
// to the reviewed DurableRev and disk version, so a document or file that
// changed after review is refused (stale_document) and refreshed rather than
// overwritten. Versions are untrusted text and paint through walkCells.

var docReviewTabs = []string{"Changes", "Document", "Disk", "Base"}

type docReview struct {
	id, path string
	origin   string
	seq      uint64
	loading  bool
	err      string
	versions *protocol.DocumentVersions
	// rev is the DurableRev the shown document text belongs to; mine is
	// that text.
	rev    int64
	mine   string
	tab    int
	scroll int
	// sending is a resolution without a reply yet; failed keeps one whose
	// reply was lost for an identical Retry.
	sending *protocol.Command
	failed  string
	// Wrapped body rows are cached per tab and width.
	cacheKey string
	cache    []docReviewRow
	bodyMax  int
}

type docReviewRow struct {
	number, text, ink string
}

type docVersionsMsg struct {
	id  string
	seq uint64
	v   protocol.DocumentVersions
	err error
}

// openDocReview opens the review for session s.
func (m *Model) openDocReview(s *docSession) tea.Cmd {
	if s == nil || s.rep == nil {
		return m.showNoticeAs(noticeUnavailable, "The document is still opening")
	}
	origin := m.focus
	if strings.HasPrefix(origin, "doc-review") || strings.HasPrefix(origin, "menu:") {
		origin = "files-text"
	}
	m.menu, m.contextMenu = nil, nil
	m.hover = ""
	m.docReview = &docReview{id: s.id, path: s.path, origin: origin}
	m.setFocus("doc-review-body")
	return m.fetchDocVersions()
}

func (m *Model) fetchDocVersions() tea.Cmd {
	rv := m.docReview
	api := m.docAPI()
	if rv == nil || api == nil {
		return nil
	}
	m.docSeq++
	rv.seq, rv.loading, rv.err, rv.cacheKey = m.docSeq, true, "", ""
	id, seq, ctx := rv.id, rv.seq, m.filesContext()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		v, err := api.DocumentVersions(deadline, id)
		return docVersionsMsg{id: id, seq: seq, v: v, err: err}
	}
}

func (m *Model) acceptDocVersions(msg docVersionsMsg) tea.Cmd {
	rv := m.docReview
	if rv == nil || rv.id != msg.id || rv.seq != msg.seq {
		return nil
	}
	rv.loading, rv.cacheKey = false, ""
	if msg.err != nil {
		rv.err = safe(singleLine(msg.err.Error()))
		return nil
	}
	s := m.docs[rv.id]
	if s == nil || s.rep == nil {
		rv.err = "The document is no longer open"
		return nil
	}
	v := msg.v
	rv.versions = &v
	rv.rev, rv.mine = s.status.DurableRev, s.rep.txt.String()
	return nil
}

func (m *Model) closeDocReview() tea.Cmd {
	rv := m.docReview
	if rv == nil {
		return nil
	}
	m.docReview = nil
	m.hover = ""
	origin := rv.origin
	if origin == "" {
		origin = "files-text"
	}
	return m.setFocus(origin)
}

// docReviewStale names why the review no longer matches the document.
func (m *Model) docReviewStale() string {
	rv := m.docReview
	s := m.docs[rv.id]
	switch {
	case s == nil || s.gone != "":
		return "The document is closed"
	case rv.versions == nil:
		return ""
	case s.rep == nil || !s.connected || s.rev < s.status.DurableRev:
		return "The document is still syncing"
	case s.status.State != protocol.DocumentStatePausedConflict && s.status.State != protocol.DocumentStateDeleted:
		return "Autosave is no longer paused"
	case s.status.DurableRev != rv.rev:
		return "The document changed since these versions were fetched"
	}
	return ""
}

// docVersionText turns version bytes into document-like text: without a
// BOM, with LF line endings and valid UTF-8.
func docVersionText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "\ufffd")
	s = strings.TrimPrefix(s, "\ufeff")
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// docDisplayLine sanitizes one line for painting.
func docDisplayLine(line string) string {
	var b strings.Builder
	walkCells(line, func(c edCell) bool { b.WriteString(c.text); return true })
	return b.String()
}

func splitDocLines(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffLine struct {
	kind byte // ' ', '-', '+'
	text string
	a, b int // 1-based line numbers (0 when absent)
}

// lineDiff returns an edit script from a to b: common prefix and suffix,
// then an LCS of the middle when it is small enough, else the whole middle
// as removed then added.
func lineDiff(a, b []string) []diffLine {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []diffLine
	for i := 0; i < pre; i++ {
		out = append(out, diffLine{' ', a[i], i + 1, i + 1})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if n, k := len(ma), len(mb); n > 0 && k > 0 && n*k <= 4_000_000 {
		// lcs[i][j] is the LCS length of ma[i:] and mb[j:].
		lcs := make([][]int32, n+1)
		for i := range lcs {
			lcs[i] = make([]int32, k+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := k - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < k {
			switch {
			case i < n && j < k && ma[i] == mb[j]:
				out = append(out, diffLine{' ', ma[i], pre + i + 1, pre + j + 1})
				i++
				j++
			case i < n && (j == k || lcs[i+1][j] >= lcs[i][j+1]):
				out = append(out, diffLine{'-', ma[i], pre + i + 1, 0})
				i++
			default:
				out = append(out, diffLine{'+', mb[j], 0, pre + j + 1})
				j++
			}
		}
	} else {
		for i, l := range ma {
			out = append(out, diffLine{'-', l, pre + i + 1, 0})
		}
		for j, l := range mb {
			out = append(out, diffLine{'+', l, 0, pre + j + 1})
		}
	}
	for i := len(a) - suf; i < len(a); i++ {
		out = append(out, diffLine{' ', a[i], i + 1, len(b) - (len(a) - i) + 1})
	}
	return out
}

// docReviewRows lays out the current tab at width.
func (m *Model) docReviewRows(width int) []docReviewRow {
	rv := m.docReview
	p := m.colors()
	key := fmt.Sprintf("%d/%d/%t/%d", rv.tab, width, m.state.Light, m.colorProfile)
	if rv.cacheKey == key {
		return rv.cache
	}
	var rows []docReviewRow
	add := func(number, text, ink string) {
		room := max(1, width-6)
		for i, part := range lineRows(text, room) {
			end := len(text)
			parts := lineRows(text, room)
			if i+1 < len(parts) {
				end = parts[i+1].b
			}
			n := ""
			if i == 0 {
				n = number
			}
			rows = append(rows, docReviewRow{number: n, text: docDisplayLine(text[part.b:end]), ink: ink})
		}
	}
	v := rv.versions
	disk := ""
	if v != nil {
		disk = docVersionText(v.Disk)
	}
	plain := func(s string) {
		for i, l := range splitDocLines(s) {
			add(fmt.Sprint(i+1), l, p.text)
		}
	}
	switch {
	case v == nil:
	case rv.tab == 1:
		plain(rv.mine)
	case rv.tab == 2 && v.DiskState != "present":
		rows = append(rows, docReviewRow{text: docDiskStateCopy(v.DiskState), ink: p.muted})
	case rv.tab == 2:
		plain(disk)
	case rv.tab == 3:
		plain(docVersionText(v.Base))
	default:
		diff := lineDiff(splitDocLines(disk), splitDocLines(rv.mine))
		if v.DiskState != "present" {
			rows = append(rows, docReviewRow{text: docDiskStateCopy(v.DiskState), ink: p.muted})
		}
		changed := make([]bool, len(diff))
		for i, d := range diff {
			changed[i] = d.kind != ' '
		}
		const context = 3
		near := func(i int) bool {
			for j := max(0, i-context); j <= min(len(diff)-1, i+context); j++ {
				if changed[j] {
					return true
				}
			}
			return false
		}
		skipped := 0
		for i, d := range diff {
			if !near(i) {
				skipped++
				continue
			}
			if skipped > 0 {
				rows = append(rows, docReviewRow{text: fmt.Sprintf("⋯ %d unchanged %s", skipped, docPlural(skipped, "line", "lines")), ink: p.muted})
				skipped = 0
			}
			switch d.kind {
			case '-':
				add("-", d.text, p.red)
			case '+':
				add("+", d.text, p.green)
			default:
				add("", d.text, p.muted)
			}
		}
		if skipped > 0 && len(rows) > 0 {
			rows = append(rows, docReviewRow{text: fmt.Sprintf("⋯ %d unchanged %s", skipped, docPlural(skipped, "line", "lines")), ink: p.muted})
		}
		if len(rows) == 0 {
			rows = append(rows, docReviewRow{text: "The document and the file on disk have the same text", ink: p.muted})
		}
	}
	rv.cacheKey, rv.cache = key, rows
	return rows
}

func docDiskStateCopy(state string) string {
	switch state {
	case "absent":
		return "The file was deleted on disk"
	case "not_regular":
		return "The path on disk is no longer a regular file"
	}
	return "The file on disk cannot be shown"
}

// docReviewRect is the dialog's rectangle.
func (m *Model) docReviewRect() shell.Rect {
	w := min(m.width, min(110, max(24, m.width-6)))
	h := min(m.height-1, max(12, m.height-4))
	return shell.Rect{X: max(0, (m.width-w)/2), Y: max(0, (m.height-1-h)/2), W: max(1, w), H: max(1, h)}
}

// renderDocReview paints the review dialog over the workspace.
func (m *Model) renderDocReview(f *frame) {
	rv := m.docReview
	p := m.colors()
	r := m.docReviewRect()
	m.renderModalBackdrop(f)
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, componentState{Focused: true}, p.text, p.input), p.canvas)
	f.hits = nil
	f.scrollbars = nil
	x, w := r.X+2, max(0, r.W-4)
	if r.W < 20 || r.H < 10 {
		f.iconButton(m, r.X+max(0, r.W-3), r.Y, 3, centered(m.icon("close"), 3), "doc-review-close", action{Kind: "doc-review-close"}, p.muted, p.input)
		return
	}
	y := r.Y + 1
	title := "Review changes · "
	f.text(x, y, 2, m.icon("files"), p.blue, p.input)
	tw := ansi.StringWidth(title)
	f.componentText(x+3, y, tw, title, componentVisual{foreground: p.text, background: p.input, bold: true})
	f.text(x+3+tw, y, max(0, w-6-tw), truncatePathLeft(safe(singleLine(rv.path)), max(1, w-6-tw)), p.text, p.input)
	f.iconButton(m, r.X+r.W-4, y, 3, centered(m.icon("close"), 3), "doc-review-close", action{Kind: "doc-review-close"}, p.muted, p.input)
	f.hits[len(f.hits)-1].Label = "Close review · Esc"
	panelRuleOn(f, m, x, y+1, w, p.input)
	// State row.
	s := m.docs[rv.id]
	state, label := "stale", "The file changed on disk while you were editing · autosave paused"
	if s != nil && s.status.State == protocol.DocumentStateDeleted {
		state, label = "failed", "The file was deleted or replaced on disk · autosave paused"
	}
	var button *docButton
	switch stale := m.docReviewStale(); {
	case rv.loading:
		state, label = "pending", "Loading versions…"
	case rv.err != "":
		state, label = "failed", "Versions unavailable · "+rv.err
		button = &docButton{"Retry", "doc-review-refresh", action{Kind: "doc-review-refresh"}}
	case rv.sending != nil && rv.failed == "":
		state, label = "active", "Resolving…"
	case rv.failed != "":
		state, label = "failed", rv.failed
		button = &docButton{"Retry", "doc-resolve-retry", action{Kind: "doc-resolve-retry"}}
	case stale != "":
		state, label = "stale", stale
		button = &docButton{"Refresh", "doc-review-refresh", action{Kind: "doc-review-refresh"}}
	case s != nil && len(s.pending) > 0:
		state, label = "active", "Your latest edits are still being stored"
	}
	glyph, ink := panelStatusMark(m, state)
	f.text(x, y+2, 2, glyph, ink, p.input)
	lw := w - 2
	if button != nil {
		bw := ansi.StringWidth(button.label) + 2
		f.compactButton(m, x+w-bw, y+2, bw, button.label, button.key, button.a, false, 0)
		lw -= bw + 1
	}
	f.text(x+2, y+2, max(0, lw), label, p.text, p.input)
	// Tabs.
	tx := x
	for i, name := range docReviewTabs {
		tw := len(name) + 2
		if tx+tw > x+w {
			break
		}
		f.compactButton(m, tx, y+3, tw, name, fmt.Sprintf("doc-review-tab:%d", i), action{Kind: "doc-review-tab", Index: i}, i == rv.tab, 0)
		f.hits[len(f.hits)-1].Label = fmt.Sprintf("%s · %d", docReviewHelp(i), i+1)
		tx += tw + 1
	}
	panelRuleOn(f, m, x, y+4, w, p.input)
	body := shell.Rect{X: x, Y: y + 5, W: max(0, w-1), H: max(0, r.Y+r.H-3-(y+5))}
	rows := m.docReviewRows(body.W)
	rv.bodyMax = max(0, len(rows)-body.H)
	rv.scroll = min(max(0, rv.scroll), rv.bodyMax)
	f.hits = append(f.hits, hit{Rect: body, Label: "Versions · arrows scroll · 1–4 switch tabs · read-only", Key: "doc-review-body"})
	for i := 0; i < body.H && rv.scroll+i < len(rows); i++ {
		row := rows[rv.scroll+i]
		f.text(body.X, body.Y+i, 5, fmt.Sprintf("%4s", row.number), p.muted, p.input)
		f.text(body.X+6, body.Y+i, max(0, body.W-6), row.text, row.ink, p.input)
	}
	if m.focus == "doc-review-body" && body.H > 0 {
		v := m.componentStyle(squareFill, componentState{Focused: true}, p.text, p.input)
		f.focusMark(body.X-1, body.Y, v, p.input)
	}
	f.scrollbar(m, shell.Rect{X: r.X + r.W - 2, Y: body.Y, W: 1, H: body.H}, "doc-review", len(rows), body.H, rv.scroll, p.input)
	// Resolutions.
	ay := r.Y + r.H - 2
	panelRuleOn(f, m, x, ay-1, w, p.input)
	ax := x
	for _, c := range []struct{ label, key, value, help string }{
		{"Keep mine", "doc-review-keep", protocol.DocumentResolveKeepDocument, "Write the document over the file on disk"},
		{"Use disk", "doc-review-disk", protocol.DocumentResolveUseDisk, "Replace the document with the file on disk"},
		{"Discard", "doc-review-discard", protocol.DocumentResolveDiscard, "Drop the document without writing the file"},
	} {
		bw := len(c.label) + 2
		if ax+bw > x+w {
			break
		}
		f.compactButton(m, ax, ay, bw, c.label, c.key, action{Kind: "doc-resolve", Value: c.value}, false, 0)
		f.hits[len(f.hits)-1].Label = c.help
		ax += bw + 1
	}
	if hint := "Esc Close"; ax+ansi.StringWidth(hint)+2 <= x+w {
		f.text(x+w-ansi.StringWidth(hint), ay, ansi.StringWidth(hint), hint, p.muted, p.input)
	}
}

func docReviewHelp(tab int) string {
	switch tab {
	case 1:
		return "Your document now"
	case 2:
		return "The file on disk"
	case 3:
		return "The last version the document and the file agreed on"
	}
	return "Lines only on disk (−) and only in your document (+)"
}

// docReviewKey handles keys while the review is open (and no menu is).
func (m *Model) docReviewKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	rv := m.docReview
	s := k.String()
	switch s {
	case "ctrl+q", "ctrl+c", "ctrl+shift+c":
		return nil, false
	case "esc":
		return m.closeDocReview(), true
	case "1", "2", "3", "4":
		return m.docReviewAction(action{Kind: "doc-review-tab", Index: int(s[0] - '1')}), true
	case "left", "right":
		step := 1
		if s == "left" {
			step = -1
		}
		return m.docReviewAction(action{Kind: "doc-review-tab", Index: (rv.tab + step + len(docReviewTabs)) % len(docReviewTabs)}), true
	case "tab", "shift+tab":
		var keys []string
		for _, h := range m.measure().hits {
			if strings.HasPrefix(h.Key, "doc-review") || strings.HasPrefix(h.Key, "doc-resolve") {
				keys = append(keys, h.Key)
			}
		}
		i := -1
		for j, key := range keys {
			if key == m.focus {
				i = j
			}
		}
		step := 1
		if s == "shift+tab" {
			step = -1
			i = max(i, 0)
		}
		if len(keys) > 0 {
			return m.setFocus(keys[(i+step+len(keys))%len(keys)]), true
		}
		return nil, true
	case "enter", " ", "space":
		for _, h := range m.measure().hits {
			if h.Key == m.focus && h.Key != "doc-review-body" {
				return m.activate(h.Action), true
			}
		}
		return nil, true
	case "up", "down", "pgup", "pgdown", "home", "end":
		m.measure()
		page := max(1, m.docReviewRect().H-9)
		switch s {
		case "up":
			rv.scroll--
		case "down":
			rv.scroll++
		case "pgup":
			rv.scroll -= page
		case "pgdown":
			rv.scroll += page
		case "home":
			rv.scroll = 0
		case "end":
			rv.scroll = rv.bodyMax
		}
		rv.scroll = min(max(0, rv.scroll), rv.bodyMax)
		return nil, true
	}
	return nil, true
}

// docReviewMouse handles pointer input while the review is open.
func (m *Model) docReviewMouse(msg tea.MouseMsg, f frame) (tea.Cmd, bool) {
	rv := m.docReview
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		switch p.Button {
		case tea.MouseWheelUp:
			rv.scroll = max(0, rv.scroll-3)
		case tea.MouseWheelDown:
			rv.scroll = min(rv.bodyMax, rv.scroll+3)
		}
		return nil, true
	case tea.MouseClickMsg:
		if p.Button != tea.MouseLeft {
			return nil, true
		}
		if !m.docReviewRect().Contains(p.X, p.Y) {
			return m.closeDocReview(), true
		}
		for i := len(f.hits) - 1; i >= 0; i-- {
			h := f.hits[i]
			if !h.Rect.Contains(p.X, p.Y) {
				continue
			}
			if h.Action.Kind == "scrollbar" {
				if target, ok := f.scrollbars[h.Action.ID]; ok {
					rv.scroll = min(rv.bodyMax, max(0, target.Bar.PageAt(h.Action.Index)))
				}
				return nil, true
			}
			cmd := m.setFocus(h.Key)
			if h.Action.Kind == "" {
				return cmd, true
			}
			return tea.Batch(cmd, m.activate(h.Action)), true
		}
		return nil, true
	case tea.MouseMotionMsg:
		m.hover = ""
		for _, h := range f.hits {
			if h.Rect.Contains(p.X, p.Y) {
				m.hover = h.Key
			}
		}
		return nil, true
	}
	return nil, true
}

// docReviewAction handles the review's controls and confirmations.
func (m *Model) docReviewAction(a action) tea.Cmd {
	rv := m.docReview
	if rv == nil {
		return nil
	}
	s := m.docs[rv.id]
	switch a.Kind {
	case "doc-review-close":
		return m.closeDocReview()
	case "doc-review-tab":
		rv.tab = min(max(0, a.Index), len(docReviewTabs)-1)
		rv.scroll, rv.cacheKey = 0, ""
		return m.setFocus(fmt.Sprintf("doc-review-tab:%d", rv.tab))
	case "doc-review-refresh":
		return m.fetchDocVersions()
	case "doc-resolve-retry":
		if rv.sending == nil || rv.failed == "" {
			return nil
		}
		rv.failed = ""
		return m.sendDocCommand(*rv.sending, "", "")
	case "doc-resolve":
		switch reason := m.docReviewStale(); {
		case rv.loading || rv.versions == nil:
			return m.showNoticeAs(noticeUnavailable, "Wait for the versions to load")
		case rv.sending != nil:
			return m.showNoticeAs(noticeUnavailable, "A resolution is already being sent")
		case reason != "":
			return m.showNoticeAs(noticeUnavailable, reason+" · Refresh first")
		case s != nil && len(s.pending) > 0:
			return m.showNoticeAs(noticeUnavailable, "Wait until your latest edits are stored")
		}
		var notes []string
		var verb, title string
		switch a.Value {
		case protocol.DocumentResolveKeepDocument:
			title, verb = "Keep your version · ", "Keep mine"
			notes = []string{"Writes your document over the file on disk.", "The disk version under Disk is replaced."}
			if rv.versions.DiskState != "present" {
				notes[1] = "The file is created again from your document."
			}
		case protocol.DocumentResolveUseDisk:
			title, verb = "Use the file on disk · ", "Use disk"
			notes = []string{"Replaces the document with the file on disk", "for everyone viewing it. Your edits that are", "not on disk are lost and cannot be undone here."}
			if rv.versions.DiskState != "present" {
				return m.showNoticeAs(noticeUnavailable, "There is no file on disk to use · Keep mine or Discard")
			}
		case protocol.DocumentResolveDiscard:
			title, verb = "Discard the document · ", "Discard"
			notes = []string{"Closes the shared document without writing it.", "Edits that are not on disk are lost;", "the file stays exactly as it is on disk."}
		default:
			return nil
		}
		m.docConfirm(title, rv.path, notes, "", menuItem{Label: verb, Action: action{Kind: "doc-resolve-confirm", Value: a.Value, Revision: rv.rev, ID: rv.versions.DiskID}})
		return nil
	case "doc-resolve-confirm":
		if rv.versions == nil || a.Revision != rv.rev || a.ID != rv.versions.DiskID || m.docReviewStale() != "" {
			return m.showNoticeAs(noticeUnavailable, "The versions changed since you confirmed · review them again")
		}
		if s == nil || len(s.pending) > 0 || s.recovering() || rv.sending != nil {
			return m.showNoticeAs(noticeUnavailable, "Wait until your latest edits are stored · then confirm again")
		}
		c := m.docCommand(protocol.DocumentKindResolve, rv.id)
		c.Text, c.Revision, c.DocumentDisk = a.Value, rv.rev, rv.versions.DiskID
		rv.sending, rv.failed = &c, ""
		return m.sendDocCommand(c, "", "")
	}
	return nil
}

// acceptDocResolve applies a resolution's reply.
func (m *Model) acceptDocResolve(msg docCmdMsg, pe *protocol.Error) tea.Cmd {
	rv := m.docReview
	if rv == nil || rv.sending == nil || rv.sending.ID != msg.cmd.ID {
		return nil
	}
	switch {
	case pe != nil && pe.Code == "stale_document":
		rv.sending = nil
		return tea.Batch(m.fetchDocVersions(), m.showNoticeAs(noticeUnavailable, "The document or the file changed again · review the refreshed versions"))
	case pe != nil:
		rv.sending = nil
		return m.showNoticeAs(noticeError, "Not resolved · "+safe(singleLine(pe.Message)))
	case msg.err != nil:
		rv.failed = "No reply from the server · " + safe(singleLine(msg.err.Error()))
		return nil
	}
	path := safe(singleLine(rv.path))
	rv.sending = nil
	close := m.closeDocReview()
	switch msg.cmd.Text {
	case protocol.DocumentResolveKeepDocument:
		return tea.Batch(close, m.showNoticeAs(noticeDone, "Keeping your version of "+path+" · saving it to disk"))
	case protocol.DocumentResolveUseDisk:
		return tea.Batch(close, m.showNoticeAs(noticeDone, "Replaced the document with "+path+" from disk"))
	}
	if s := m.docs[msg.cmd.TargetID]; s != nil {
		return tea.Batch(close, m.docGone(s, "closed", "Discarded the document for "+path))
	}
	return close
}
