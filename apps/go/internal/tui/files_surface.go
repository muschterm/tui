package tui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// files_surface.go is the read-only Files surface (docs/design/go-slice.md ›
// Files surface). It lists the displayed checkout through GET /v1/files/list
// and opens read-only buffers inside the single Files tab through
// GET /v1/files/read. Nothing here writes. Like the Git surface, every read
// is a tea.Cmd whose result carries its target key and generation and is
// dropped unless it still describes the displayed target. Tree expansion,
// open buffers and scroll positions are client-local per target, so
// switching threads restores them.

// filesPollInterval is how often a visible buffer checks its file on disk.
const filesPollInterval = 2 * time.Second

// filesMaxBuffers bounds open buffers per target view; opening another is
// refused rather than closing one the user did not choose.
const filesMaxBuffers = 20

// filesAPI is the subset of the server client used by the Files surface;
// tests inject a fake.
type filesAPI interface {
	FilesList(ctx context.Context, target client.GitTarget, dir, cursor string, hidden bool) (protocol.FileList, error)
	FilesRead(ctx context.Context, target client.GitTarget, path string) (protocol.FileRead, error)
	FilesStat(ctx context.Context, target client.GitTarget, path string) (protocol.FileStat, error)
}

var _ filesAPI = (*client.Client)(nil)

func (m *Model) filesClient() filesAPI {
	if m.filesReads != nil {
		return m.filesReads
	}
	if m.client != nil {
		return m.client
	}
	return nil
}

// filesDir is one directory's loaded pages.
type filesDir struct {
	entries   []protocol.FileEntry
	next      string
	loaded    bool
	loading   bool
	truncated bool
	skipped   int
	err       string
	gen       uint64
}

// fileBuffer is one open read-only buffer.
type fileBuffer struct {
	path    string
	read    *protocol.FileRead
	loading bool
	err     string
	gen     uint64
	// scroll is the first shown row, hscroll the first shown cell when not
	// wrapping.
	scroll, hscroll int
	wrap            bool
	// disk is "", "changed" or "deleted" from the stat poll; the content is
	// never replaced until Reload.
	disk        string
	statPending bool
	lines       []string // rendered source lines of read.Text
	// rows caches the laid-out text for rowsWrap and (wrapping) rowsRoom;
	// widest is the widest unwrapped line.
	rows     []filesTextRow
	rowsWrap bool
	rowsRoom int
	widest   int
	// Shared document (editor_session.go): doc is its ID once opened,
	// docOpen the open command in flight (kept for an identical Retry after
	// a lost reply), docErr a failed open, docRO the server's read-only
	// reason and docLost refused text kept after the document ended.
	doc     string
	docOpen *protocol.Command
	docErr  string
	docRO   string
	docLost []docLost
	// docGoneN counts consecutive disappearances of its document; reopening
	// stops after three until Retry.
	docGoneN int
	// docW and docH are the editor text area's last laid-out size.
	docW, docH int
}

// filesView is one target's client-local Files view.
type filesView struct {
	dirs     map[string]*filesDir
	expanded map[string]bool
	hidden   bool
	// cursor is the tree row with keyboard selection: an entry path, or
	// "more:<dir>" for a directory's Load more row.
	cursor  string
	scroll  int
	buffers []*fileBuffer
	// active is the shown buffer index, or -1 for the tree.
	active int
}

func (v *filesView) buffer() *fileBuffer {
	if v.active < 0 || v.active >= len(v.buffers) {
		return nil
	}
	return v.buffers[v.active]
}

func (v *filesView) dir(p string) *filesDir {
	d := v.dirs[p]
	if d == nil {
		d = &filesDir{}
		v.dirs[p] = d
	}
	return d
}

type filesListMsg struct {
	key, dir string
	gen      uint64
	more     bool
	list     protocol.FileList
	err      error
}

type filesReadMsg struct {
	key, path string
	gen       uint64
	read      protocol.FileRead
	err       error
}

type filesStatMsg struct {
	key, path string
	gen       uint64
	stat      protocol.FileStat
	err       error
}

type filesTickMsg struct{}

// filesTarget is the displayed checkout, keyed like the Git surface.
func (m *Model) filesTarget() (string, client.GitTarget) { return m.gitTarget() }

// currentFilesView returns the displayed target's view, creating it.
func (m *Model) currentFilesView() *filesView {
	key, _ := m.filesTarget()
	if key == "" {
		return nil
	}
	if m.filesViews == nil {
		m.filesViews = map[string]*filesView{}
	}
	v := m.filesViews[key]
	if v == nil {
		v = &filesView{dirs: map[string]*filesDir{}, expanded: map[string]bool{}, active: -1}
		m.filesViews[key] = v
	}
	return v
}

// filesVisible reports whether the Files surface is on screen.
func (m *Model) filesVisible() bool {
	if !m.hasComposer() || m.settingsPage != "" || m.terminalTooSmall() {
		return false
	}
	v := m.viewState()
	active, ok := v.Host.Active()
	if !ok || v.Host.Chooser || active.Kind != "files" {
		return false
	}
	return m.workspaceGeometry(m.footerHeight()).Right.W > 0
}

func (m *Model) filesContext() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// nextFilesRefresh runs after every update: it loads what the displayed
// target is missing when the surface becomes visible or its target changes,
// and keeps the disk poll ticking only while a buffer is visible.
func (m *Model) nextFilesRefresh() tea.Cmd {
	key, _ := m.filesTarget()
	if key == "" || !m.filesVisible() {
		m.filesShown = ""
		return nil
	}
	pruned := m.pruneFilesViews()
	if !m.connected || m.filesClient() == nil || m.filesFixture() || !m.filesAvailable() {
		return pruned
	}
	cmds := []tea.Cmd{pruned}
	if key != m.filesShown {
		m.filesShown = key
		cmds = append(cmds, m.resumeFiles())
	}
	if v := m.currentFilesView(); v.buffer() != nil && !m.filesTicking {
		m.filesTicking = true
		cmds = append(cmds, tea.Tick(filesPollInterval, func(time.Time) tea.Msg { return filesTickMsg{} }))
	}
	return tea.Batch(cmds...)
}

// resumeFiles loads the root and expanded directories that have no result
// yet (a result for a target that was not displayed is dropped) and any
// buffer still waiting for its content.
func (m *Model) resumeFiles() tea.Cmd {
	v := m.currentFilesView()
	var cmds []tea.Cmd
	if d := v.dirs[""]; d == nil || !d.loaded {
		cmds = append(cmds, m.loadFilesDir("", false))
	}
	for dir, open := range v.expanded {
		if d := v.dirs[dir]; open && (d == nil || !d.loaded) {
			cmds = append(cmds, m.loadFilesDir(dir, false))
		}
	}
	for _, b := range v.buffers {
		if b.read == nil && b.err == "" || b.loading {
			cmds = append(cmds, m.readFilesBuffer(b))
		}
	}
	return tea.Batch(cmds...)
}

// loadFilesDir reads a directory's first page, or its next page with more.
func (m *Model) loadFilesDir(dir string, more bool) tea.Cmd {
	key, target := m.filesTarget()
	api := m.filesClient()
	v := m.currentFilesView()
	if api == nil || v == nil {
		return nil
	}
	d := v.dir(dir)
	cursor := ""
	if more {
		if d.next == "" {
			return nil
		}
		cursor = d.next
	}
	m.filesSeq++
	gen := m.filesSeq
	d.gen, d.loading = gen, true
	hidden := v.hidden
	ctx := m.filesContext()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		l, err := api.FilesList(deadline, target, dir, cursor, hidden)
		return filesListMsg{key: key, dir: dir, gen: gen, more: more, list: l, err: err}
	}
}

func (m *Model) readFilesBuffer(b *fileBuffer) tea.Cmd {
	key, target := m.filesTarget()
	api := m.filesClient()
	if api == nil {
		return nil
	}
	m.filesSeq++
	gen := m.filesSeq
	// A newer generation makes any stat in flight stale; its reply still
	// clears statPending, but polling never waits on it.
	b.gen, b.loading, b.statPending = gen, true, false
	p := b.path
	ctx := m.filesContext()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r, err := api.FilesRead(deadline, target, p)
		return filesReadMsg{key: key, path: p, gen: gen, read: r, err: err}
	}
}

// acceptFilesView returns the view a result belongs to. Keys include the
// checkout, so a result for a target that is no longer displayed still
// describes that target: it is applied there (never to the displayed one),
// which also clears its loading and poll state. A pruned view drops it.
func (m *Model) acceptFilesView(key string) *filesView { return m.filesViews[key] }

func (m *Model) acceptFilesList(msg filesListMsg) {
	v := m.acceptFilesView(msg.key)
	if v == nil {
		return
	}
	d := v.dirs[msg.dir]
	if d == nil || d.gen != msg.gen {
		return
	}
	d.loading = false
	if msg.err != nil {
		d.err = safe(singleLine(msg.err.Error()))
		return
	}
	if !msg.more {
		d.entries = nil
	}
	d.entries = append(d.entries, msg.list.Entries...)
	d.next, d.truncated, d.skipped, d.loaded, d.err = msg.list.Next, msg.list.Truncated, msg.list.Skipped, true, ""
	m.markDirty()
}

func (m *Model) acceptFilesRead(msg filesReadMsg) tea.Cmd {
	v := m.acceptFilesView(msg.key)
	if v == nil {
		return nil
	}
	for _, b := range v.buffers {
		if b.path != msg.path || b.gen != msg.gen {
			continue
		}
		b.loading = false
		if msg.err != nil {
			b.err = safe(singleLine(msg.err.Error()))
			return nil
		}
		r := msg.read
		b.read, b.err, b.disk = &r, "", ""
		b.lines, b.rows = nil, nil
		m.markDirty()
		// An editable text opens its shared document (read the target the
		// result belongs to, which need not be the displayed one).
		return m.maybeOpenDocument(msg.key, m.filesTargetFor(msg.key), b)
	}
	return nil
}

// filesTargetFor returns the Git target behind a view key.
func (m *Model) filesTargetFor(key string) client.GitTarget {
	if current, target := m.filesTarget(); current == key {
		return target
	}
	if rest, ok := strings.CutPrefix(key, "thread:"); ok {
		id, _, _ := strings.Cut(rest, ":")
		return client.GitTarget{ThreadID: id}
	}
	if rest, ok := strings.CutPrefix(key, "project:"); ok {
		id, _, _ := strings.Cut(rest, ":")
		return client.GitTarget{ProjectID: id}
	}
	return client.GitTarget{}
}

func (m *Model) acceptFilesStat(msg filesStatMsg) {
	v := m.acceptFilesView(msg.key)
	if v == nil {
		return
	}
	for _, b := range v.buffers {
		if b.path != msg.path {
			continue
		}
		// Any reply ends the poll in flight, even one a Reload made stale.
		b.statPending = false
		if b.gen != msg.gen || msg.err != nil || b.read == nil {
			continue
		}
		disk := ""
		switch {
		case msg.stat.Kind == protocol.FileKindAbsent:
			disk = "deleted"
		case msg.stat.Token != b.read.Token:
			disk = "changed"
		}
		if disk != b.disk {
			b.disk = disk
			m.markDirty()
		}
	}
}

// filesTick polls the visible buffer's file; hidden surfaces send nothing.
func (m *Model) filesTick() tea.Cmd {
	m.filesTicking = false
	if !m.filesVisible() || !m.connected {
		return nil
	}
	key, target := m.filesTarget()
	v := m.currentFilesView()
	b := v.buffer()
	api := m.filesClient()
	if b == nil || b.read == nil || b.loading || b.statPending || api == nil || m.bufferDoc(b) != nil {
		// A document watches its own file on the server.
		return nil
	}
	b.statPending = true
	p, gen := b.path, b.gen
	ctx := m.filesContext()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		s, err := api.FilesStat(deadline, target, p)
		return filesStatMsg{key: key, path: p, gen: gen, stat: s, err: err}
	}
}

// openFilesBuffer shows path in a buffer, reusing an open one.
func (m *Model) openFilesBuffer(p string) tea.Cmd {
	v := m.currentFilesView()
	for i, b := range v.buffers {
		if b.path == p {
			v.active = i
			m.markDirty()
			return m.setFocus("files-text")
		}
	}
	if len(v.buffers) >= filesMaxBuffers {
		return m.showNoticeAs(noticeUnavailable, fmt.Sprintf("Close a file first · %d files are open", filesMaxBuffers))
	}
	b := &fileBuffer{path: p}
	v.buffers = append(v.buffers, b)
	v.active = len(v.buffers) - 1
	m.markDirty()
	return tea.Batch(m.readFilesBuffer(b), m.setFocus("files-text"))
}

func (m *Model) closeFilesBuffer(i int) tea.Cmd {
	v := m.currentFilesView()
	if i < 0 || i >= len(v.buffers) {
		return nil
	}
	if m.docCloseGuard(v.buffers[i]) {
		return nil
	}
	v.buffers = append(v.buffers[:i:i], v.buffers[i+1:]...)
	switch {
	case len(v.buffers) == 0:
		v.active = -1
		m.markDirty()
		return m.setFocus("files-tree")
	case v.active > i || v.active >= len(v.buffers):
		v.active--
	}
	m.markDirty()
	return nil
}

// toggleFilesDir expands or collapses dir, loading it on first expansion.
func (m *Model) toggleFilesDir(dir string) tea.Cmd {
	v := m.currentFilesView()
	v.expanded[dir] = !v.expanded[dir]
	m.markDirty()
	if d := v.dirs[dir]; v.expanded[dir] && (d == nil || !d.loaded && !d.loading) {
		return m.loadFilesDir(dir, false)
	}
	return nil
}

// reloadFiles rereads every loaded or expanded directory from its first page.
func (m *Model) reloadFiles() tea.Cmd {
	v := m.currentFilesView()
	cmds := []tea.Cmd{m.loadFilesDir("", false)}
	for dir, open := range v.expanded {
		if open {
			cmds = append(cmds, m.loadFilesDir(dir, false))
		}
	}
	for dir := range v.dirs {
		if dir != "" && !v.expanded[dir] {
			// Collapsed listings reload when expanded again.
			delete(v.dirs, dir)
		}
	}
	return tea.Batch(cmds...)
}

// filesAction handles the surface's controls.
func (m *Model) filesAction(a action) tea.Cmd {
	v := m.currentFilesView()
	if v == nil {
		return nil
	}
	online := m.connected && m.filesClient() != nil
	switch a.Kind {
	case "files-refresh":
		if !online {
			return m.showNoticeAs(noticeUnavailable, "Connect to the server to list files")
		}
		return m.reloadFiles()
	case "files-hidden":
		if !online {
			return m.showNoticeAs(noticeUnavailable, "Connect to the server to list files")
		}
		v.hidden = !v.hidden
		v.cursor, v.scroll = "", 0
		m.markDirty()
		return m.reloadFiles()
	case "files-row":
		v.cursor = a.ID
		m.setFocus("files-tree")
		return m.activateFilesRow(a.ID, a.Value)
	case "files-tree-body":
		return m.setFocus("files-tree")
	case "files-text-body":
		return m.setFocus("files-text")
	case "files-back":
		v.active = -1
		m.markDirty()
		return m.setFocus("files-tree")
	case "files-buf":
		if a.Index >= 0 && a.Index < len(v.buffers) {
			v.active = a.Index
			m.markDirty()
		}
	case "files-buf-close":
		return m.closeFilesBuffer(a.Index)
	case "files-bufs":
		items := []menuItem{{Label: "Files", Action: action{Kind: "files-back"}}}
		for i, b := range v.buffers {
			items = append(items, menuItem{Label: safe(singleLine(b.path)), Action: action{Kind: "files-buf", Index: i}})
		}
		m.showMenu("Open files", items)
	case "files-reload":
		if b := v.buffer(); b != nil && online && m.bufferDoc(b) == nil {
			return m.readFilesBuffer(b)
		}
	case "files-wrap":
		if b := v.buffer(); b != nil {
			if s := m.bufferDoc(b); s != nil && s.rep != nil {
				// Keep the first visible line across the change.
				line, _ := s.rep.txt.rowLine(b.scroll, docWrapRoom(b))
				b.wrap, b.hscroll = !b.wrap, 0
				b.scroll = s.rep.txt.rowStart(line, docWrapRoom(b))
				m.markDirty()
				return nil
			}
			b.wrap, b.hscroll, b.scroll = !b.wrap, 0, 0
			m.markDirty()
		}
	case "files-copy":
		if b := v.buffer(); b != nil && m.bufferDoc(b) != nil && m.bufferDoc(b).rep != nil {
			return m.copyText(clipboardSafeText(m.bufferDoc(b).rep.txt.String()))
		}
		if b := v.buffer(); b != nil && b.read != nil && b.read.Kind == protocol.FileReadText {
			cmd := m.copyText(clipboardSafeText(b.read.Text))
			if b.read.Truncated {
				return tea.Batch(cmd, m.showNoticeAs(noticeInfo, "Copied the first "+filesSize(protocol.FileTextLimit)+" · the file is longer"))
			}
			return cmd
		}
	}
	return nil
}

// activateFilesRow toggles a directory, loads more entries or opens a file.
func (m *Model) activateFilesRow(id, kind string) tea.Cmd {
	if dir, ok := strings.CutPrefix(id, "more:"); ok {
		return m.loadFilesDir(dir, true)
	}
	if kind == protocol.FileKindDir {
		return m.toggleFilesDir(id)
	}
	if kind == "" {
		return nil
	}
	return m.openFilesBuffer(id)
}

// filesKey handles keys while the tree or a buffer's text has focus.
func (m *Model) filesKey(s string) (tea.Cmd, bool) {
	if m.focus != "files-tree" && m.focus != "files-text" || !m.filesVisible() {
		return nil, false
	}
	v := m.currentFilesView()
	if m.focus == "files-text" {
		return m.filesTextKey(v, s)
	}
	rows := m.filesTreeRows(v)
	var ids []int
	cur := -1
	for i, r := range rows {
		if r.id != "" {
			if r.id == v.cursor {
				cur = len(ids)
			}
			ids = append(ids, i)
		}
	}
	if len(ids) == 0 {
		return nil, s == "up" || s == "down"
	}
	page := max(1, m.filesTreeHeight()-1)
	move := func(n int) {
		if cur < 0 {
			n = 0
		} else {
			n = min(len(ids)-1, max(0, n))
		}
		v.cursor = rows[ids[n]].id
		m.revealFilesRow(v, ids[n])
		m.markDirty()
	}
	row := func() filesRow {
		if cur < 0 {
			return filesRow{}
		}
		return rows[ids[cur]]
	}
	switch s {
	case "up":
		move(cur - 1)
	case "down":
		move(cur + 1)
	case "pgup":
		move(cur - page)
	case "pgdown":
		move(cur + page)
	case "home":
		move(0)
	case "end":
		move(len(ids) - 1)
	case "right":
		r := row()
		if r.kind == protocol.FileKindDir && !v.expanded[r.id] {
			return m.toggleFilesDir(r.id), true
		}
		if r.kind == protocol.FileKindDir {
			move(cur + 1)
		}
	case "left":
		r := row()
		if r.kind == protocol.FileKindDir && v.expanded[r.id] {
			return m.toggleFilesDir(r.id), true
		}
		parent := path.Dir(strings.TrimPrefix(r.id, "more:"))
		if strings.HasPrefix(r.id, "more:") {
			parent = strings.TrimPrefix(r.id, "more:")
		}
		for i, id := range ids {
			if parent != "." && rows[id].id == parent {
				move(i)
			}
		}
	case "enter", " ", "space":
		r := row()
		if r.id == "" {
			return nil, true
		}
		return m.activateFilesRow(r.id, r.kind), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) filesTextKey(v *filesView, s string) (tea.Cmd, bool) {
	b := v.buffer()
	if b == nil {
		return nil, false
	}
	h := max(1, m.filesTextHeight())
	if doc := m.bufferDoc(b); doc != nil && doc.rep != nil && s == "enter" {
		return m.startDocEdit(b, doc), true
	}
	switch s {
	case "up":
		b.scroll--
	case "down":
		b.scroll++
	case "pgup":
		b.scroll -= h - 1
	case "pgdown":
		b.scroll += h - 1
	case "home":
		b.scroll, b.hscroll = 0, 0
	case "end":
		b.scroll = 1 << 30
	case "left":
		b.hscroll = max(0, b.hscroll-8)
	case "right":
		if !b.wrap {
			b.hscroll += 8
		}
	case "w":
		return m.filesAction(action{Kind: "files-wrap"}), true
	case "backspace":
		return m.filesAction(action{Kind: "files-back"}), true
	default:
		return nil, false
	}
	m.clampFilesBuffer(b)
	m.markDirty()
	return nil, true
}

// filesWheel scrolls the tree or text under the pointer.
func (m *Model) filesWheel(f frame, x, y, d int) bool {
	v := m.currentFilesView()
	switch {
	case v == nil:
		return false
	case f.filesTree.Contains(x, y):
		v.scroll = min(max(0, v.scroll+d), f.filesTreeMax)
	case f.filesText.Contains(x, y):
		if b := v.buffer(); b != nil {
			b.scroll += d
			m.clampFilesBuffer(b)
		}
	default:
		return false
	}
	m.markDirty()
	return true
}

func filesSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

// filesAvailable reports whether the server offers file browsing.
func (m *Model) filesAvailable() bool { return m.hasCapability("files-read") }

// pruneFilesViews drops views whose thread or project is gone or whose
// checkout changed (the key names the checkout it was built for).
func (m *Model) pruneFilesViews() tea.Cmd {
	if len(m.filesViews) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	live := map[string]bool{}
	for _, t := range m.snapshot.Threads {
		live["thread:"+t.ID+":"+t.Checkout] = true
	}
	current, _ := m.filesTarget()
	live[current] = true
	for key := range m.filesViews {
		if live[key] {
			continue
		}
		if rest, ok := strings.CutPrefix(key, "project:"); ok {
			id, _, _ := strings.Cut(rest, ":")
			if _, found := m.projectByID(id); found {
				continue
			}
		}
		// Unstored document text outlives the view.
		cmds = append(cmds, m.orphanView(m.filesViews[key]))
		delete(m.filesViews, key)
	}
	return tea.Batch(cmds...)
}

// clipboardSafeText keeps the text, tabs and line endings of untrusted file
// content and removes escape sequences (including bracketed-paste markers)
// and every other C0/C1 control before it reaches the clipboard.
func clipboardSafeText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\x1b[200~", ""), "\x1b[201~", "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(strings.ToValidUTF8(text, "\uFFFD")))
}
