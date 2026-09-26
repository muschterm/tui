package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_partial.go makes a single-file diff in the Git viewer selectable for
// hunk- and line-level stage/unstage (ADR 0025). The viewer fetches the
// addressable diff (GET /v1/git/hunks) beside the ordinary patch; while it
// carries a Fingerprint the body paints hunk rows instead of the raw patch.
// Cursor, selection and range are client-local; the command carries the
// exact line indices shown with the Fingerprint they came from, and the
// server refuses anything stale (stale_diff), after which the hunks are
// fetched again. Partial discard is out of scope and never offered.

// gitHunksAPI is the partial-staging read; tests inject a fake.
type gitHunksAPI interface {
	GitHunks(ctx context.Context, target client.GitTarget, path, group string) (protocol.GitHunks, error)
}

var _ gitHunksAPI = (*client.Client)(nil)

// gitPartialState is the viewer's client-local partial-staging state.
type gitPartialState struct {
	seq     uint64
	loading bool
	err     string
	hunks   *protocol.GitHunks
	rows    []gitPartialRow
	cursor  int
	// selected holds GitHunkLine.Index values of add/delete lines.
	selected map[int]bool
	// anchor is the range start row (-1 none); base is the selection the
	// range extends; ranging is the sticky `v` mode; drag a pointer range.
	anchor  int
	base    map[int]bool
	ranging bool
	drag    bool
	// keep is the cursor to restore approximately after a refetch.
	keep *gitPartialMark
	// pending is a refreshed status entry (new Pin) the viewer adopts only
	// after both the raw patch and the hunks read for it were accepted;
	// until then the viewer stays changed and refuses every write.
	pending             *protocol.GitStatusEntry
	pendDiff, pendHunks bool
}

// gitPartialRow is one body row: a hunk header (line -1), a hunk line, or
// the "\ No newline at end of file" marker after a line (eof).
type gitPartialRow struct {
	hunk, line int
	eof        bool
}

type gitPartialMark struct {
	row, hunk  int
	header     bool
	kind, text string
}

type gitHunksMsg struct {
	viewer, seq uint64
	hunks       protocol.GitHunks
	err         error
}

// gitPartialEnabled reports whether the server offers partial staging.
func (m *Model) gitPartialEnabled() bool {
	return slices.Contains(m.snapshot.Capabilities, "git-partial-stage") && m.gitHunker() != nil && m.gitWritesEnabled()
}

func (m *Model) gitHunker() gitHunksAPI {
	if h, ok := m.gitReads.(gitHunksAPI); ok {
		return h
	}
	if m.gitReads == nil && m.client != nil {
		return m.client
	}
	return nil
}

// gitPartialEligible reports whether an opened single-file viewer can offer
// partial staging: a pinned staged or unstaged entry.
func gitPartialEligible(g *gitViewerContent) bool {
	return g != nil && !g.commit && !g.whole && g.compare == "" && g.pinned &&
		(g.group == protocol.GitGroupUnstaged || g.group == protocol.GitGroupStaged)
}

// startGitPartial attaches partial state to a just-opened viewer and
// returns its first read.
func (m *Model) startGitPartial(vw *attachmentViewer) tea.Cmd {
	if vw == nil || !gitPartialEligible(vw.git) || !m.gitPartialEnabled() {
		return nil
	}
	vw.git.partial = &gitPartialState{anchor: -1}
	return m.loadGitHunks()
}

// loadGitHunks reads the viewer's hunks asynchronously; a newer read makes
// an older reply stale.
func (m *Model) loadGitHunks() tea.Cmd {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.partial == nil {
		return nil
	}
	api := m.gitHunker()
	if api == nil {
		return nil
	}
	g, ps := vw.git, vw.git.partial
	ps.seq++
	ps.loading = true
	seq, viewer := ps.seq, vw.loadID
	target, path, group := g.target, g.path, g.group
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		h, err := api.GitHunks(deadline, target, path, group)
		return gitHunksMsg{viewer: viewer, seq: seq, hunks: h, err: err}
	}
}

// acceptGitHunks applies a hunk read to the viewer that asked for it. The
// selection clears; the cursor returns approximately to where it was.
func (m *Model) acceptGitHunks(msg gitHunksMsg) {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.partial == nil || vw.loadID != msg.viewer || vw.git.partial.seq != msg.seq {
		return
	}
	ps := vw.git.partial
	ps.loading = false
	m.markDirty()
	if msg.err != nil {
		// Nothing stale stays selectable: the old rows and selection go and
		// the viewer shows the raw patch with the error.
		ps.err = safe(singleLine(msg.err.Error()))
		ps.hunks, ps.rows, ps.keep = nil, nil, nil
		ps.selected, ps.base, ps.anchor, ps.ranging, ps.drag = nil, nil, -1, false, false
		m.gitPartialRereadFailed()
		return
	}
	ps.err = ""
	if ps.pending != nil {
		ps.pendHunks = true
		defer m.gitRepinDone()
	}
	h := msg.hunks
	ps.hunks = &h
	ps.rows = gitPartialRows(&h)
	ps.selected, ps.base, ps.anchor, ps.ranging, ps.drag = nil, nil, -1, false, false
	ps.cursor = ps.restore(ps.keep)
	ps.keep = nil
	vw.scroll = m.gitPartialScroll(ps.cursor, vw.scroll)
}

func gitPartialRows(h *protocol.GitHunks) []gitPartialRow {
	var rows []gitPartialRow
	for hi, hk := range h.Hunks {
		rows = append(rows, gitPartialRow{hunk: hi, line: -1})
		for li, ln := range hk.Lines {
			rows = append(rows, gitPartialRow{hunk: hi, line: li})
			if ln.NoNewline {
				rows = append(rows, gitPartialRow{hunk: hi, line: li, eof: true})
			}
		}
	}
	return rows
}

// gitPartialActive reports whether the viewer body shows selectable hunks.
func (m *Model) gitPartialActive() bool {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.partial == nil {
		return false
	}
	ps := vw.git.partial
	return ps.hunks != nil && ps.hunks.Fingerprint != "" && ps.hunks.Unsupported == "" && len(ps.rows) > 0
}

func (ps *gitPartialState) line(r gitPartialRow) *protocol.GitHunkLine {
	if r.line < 0 || r.eof {
		return nil
	}
	return &ps.hunks.Hunks[r.hunk].Lines[r.line]
}

// selectable reports an add/delete line row.
func (ps *gitPartialState) selectable(i int) bool {
	if i < 0 || i >= len(ps.rows) {
		return false
	}
	ln := ps.line(ps.rows[i])
	return ln != nil && (ln.Kind == protocol.GitHunkLineAdd || ln.Kind == protocol.GitHunkLineDelete)
}

// cursorable rows are hunk headers and selectable lines.
func (ps *gitPartialState) cursorable(i int) bool {
	return i >= 0 && i < len(ps.rows) && (ps.rows[i].line < 0 || ps.selectable(i))
}

// nearest is the cursorable row closest to i, preferring later rows.
func (ps *gitPartialState) nearest(i int) int {
	i = min(max(0, i), len(ps.rows)-1)
	for d := 0; d < len(ps.rows); d++ {
		if ps.cursorable(i + d) {
			return i + d
		}
		if ps.cursorable(i - d) {
			return i - d
		}
	}
	return 0
}

// step moves the cursor by n cursorable rows.
func (ps *gitPartialState) step(n int) {
	i := ps.cursor
	for n != 0 {
		d := 1
		if n < 0 {
			d = -1
		}
		j := i + d
		for j >= 0 && j < len(ps.rows) && !ps.cursorable(j) {
			j += d
		}
		if j < 0 || j >= len(ps.rows) {
			break
		}
		i = j
		n -= d
	}
	ps.cursor = i
}

func (ps *gitPartialState) mark() *gitPartialMark {
	if len(ps.rows) == 0 {
		return nil
	}
	r := ps.rows[ps.cursor]
	k := &gitPartialMark{row: ps.cursor, hunk: r.hunk, header: r.line < 0}
	if ln := ps.line(r); ln != nil {
		k.kind, k.text = ln.Kind, ln.Text
	}
	return k
}

// restore finds the cursor row for a mark taken before a refetch: the same
// line by kind and content nearest its old row, the same hunk ordinal for a
// header, else the nearest cursorable row.
func (ps *gitPartialState) restore(k *gitPartialMark) int {
	if len(ps.rows) == 0 {
		return 0
	}
	if k == nil {
		return ps.nearest(0)
	}
	if k.header {
		for i, r := range ps.rows {
			if r.line < 0 && r.hunk == min(k.hunk, len(ps.hunks.Hunks)-1) {
				return i
			}
		}
	}
	best := -1
	for i := range ps.rows {
		if ln := ps.line(ps.rows[i]); ln != nil && ps.selectable(i) && ln.Kind == k.kind && ln.Text == k.text {
			if best < 0 || abs(i-k.row) < abs(best-k.row) {
				best = i
			}
		}
	}
	if best >= 0 {
		return best
	}
	return ps.nearest(k.row)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// hunkLines are the selectable line indices of hunk hi.
func (ps *gitPartialState) hunkLines(hi int) []int {
	var out []int
	for _, ln := range ps.hunks.Hunks[hi].Lines {
		if ln.Kind == protocol.GitHunkLineAdd || ln.Kind == protocol.GitHunkLineDelete {
			out = append(out, ln.Index)
		}
	}
	return out
}

func (ps *gitPartialState) toggleLine(row int) {
	if !ps.selectable(row) {
		return
	}
	if ps.selected == nil {
		ps.selected = map[int]bool{}
	}
	idx := ps.line(ps.rows[row]).Index
	if ps.selected[idx] {
		delete(ps.selected, idx)
	} else {
		ps.selected[idx] = true
	}
}

// toggleHunk selects every change of the cursor's hunk, or clears them all
// when all are already selected.
func (ps *gitPartialState) toggleHunk() {
	if len(ps.rows) == 0 {
		return
	}
	lines := ps.hunkLines(ps.rows[ps.cursor].hunk)
	all := len(lines) > 0
	for _, i := range lines {
		all = all && ps.selected[i]
	}
	if ps.selected == nil {
		ps.selected = map[int]bool{}
	}
	for _, i := range lines {
		if all {
			delete(ps.selected, i)
		} else {
			ps.selected[i] = true
		}
	}
}

// startRange anchors a range at the cursor over the current selection.
func (ps *gitPartialState) startRange() {
	ps.anchor = ps.cursor
	ps.base = map[int]bool{}
	for k := range ps.selected {
		ps.base[k] = true
	}
	ps.extend()
}

// extend sets the selection to base plus every change between anchor and
// cursor.
func (ps *gitPartialState) extend() {
	if ps.anchor < 0 {
		return
	}
	sel := map[int]bool{}
	for k := range ps.base {
		sel[k] = true
	}
	lo, hi := min(ps.anchor, ps.cursor), max(ps.anchor, ps.cursor)
	for i := lo; i <= hi; i++ {
		if ps.selectable(i) {
			sel[ps.line(ps.rows[i]).Index] = true
		}
	}
	ps.selected = sel
}

func (ps *gitPartialState) endRange() {
	ps.anchor, ps.base, ps.ranging = -1, nil, false
}

// payload is the command selection: fully selected hunks by hunk index and
// the selected lines of partly selected hunks ascending, or the cursor's
// hunk when nothing is selected.
func (ps *gitPartialState) payload() (hunks, lines []int) {
	if len(ps.selected) > 0 {
		for hi, h := range ps.hunks.Hunks {
			all := ps.hunkLines(hi)
			var some []int
			for _, i := range all {
				if ps.selected[i] {
					some = append(some, i)
				}
			}
			switch {
			case len(some) == 0:
			case len(some) == len(all):
				hunks = append(hunks, h.Index)
			default:
				lines = append(lines, some...)
			}
		}
		slices.Sort(lines)
		return hunks, lines
	}
	if len(ps.rows) == 0 {
		return nil, nil
	}
	h := ps.hunks.Hunks[ps.rows[ps.cursor].hunk]
	return []int{h.Index}, nil
}

// gitPartialVerb is the group's action: stage from the unstaged diff,
// unstage from the staged one.
func gitPartialVerb(group string) (verb, key string) {
	if group == protocol.GitGroupStaged {
		return "Unstage", "u"
	}
	return "Stage", "s"
}

// gitPartialActionLabel names what s/u would do now.
func (ps *gitPartialState) actionLabel(group string) string {
	verb, _ := gitPartialVerb(group)
	n := len(ps.selected)
	switch {
	case n == 1:
		return verb + " 1 line"
	case n > 1:
		return verb + " " + strconv.Itoa(n) + " lines"
	}
	return verb + " hunk"
}

// gitPartialApply sends the selection (or the cursor's hunk, or hunk hi when
// hi >= 0) as one partial stage/unstage command.
func (m *Model) gitPartialApply(hi int) tea.Cmd {
	vw := m.viewer
	if !m.gitPartialActive() {
		return nil
	}
	g, ps := vw.git, vw.git.partial
	current, _ := m.gitTarget()
	if g.changed || current != g.key {
		return m.showNoticeAs(noticeUnavailable, gitViewerChangedCopy)
	}
	if ps.loading {
		return m.showNoticeAs(noticeUnavailable, "Diff reloading · try again")
	}
	if reason, _ := m.gitWriteBlock(g.key, m.gitViews[g.key]); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	var hunks, lines []int
	if hi >= 0 && hi < len(ps.hunks.Hunks) {
		hunks = []int{ps.hunks.Hunks[hi].Index}
	} else {
		hunks, lines = ps.payload()
	}
	if len(hunks)+len(lines) == 0 {
		return nil
	}
	count := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + one + "s"
	}
	var parts []string
	if len(hunks) > 0 {
		parts = append(parts, count(len(hunks), "hunk"))
	}
	if len(lines) > 0 {
		parts = append(parts, count(len(lines), "line"))
	}
	label := strings.Join(parts, " and ") + " in " + safe(singleLine(g.path))
	m.clearGitWarning(g.key)
	cmd := client.GitPartialCommand(identity(), g.target, *ps.hunks, hunks, lines)
	return m.sendGitWrite(g.key, cmd, label)
}

// gitPartialSelectHunk toggles hunk hi's selection (the header action).
func (m *Model) gitPartialSelectHunk(hi int) tea.Cmd {
	if !m.gitPartialActive() {
		return nil
	}
	ps := m.viewer.git.partial
	for i, r := range ps.rows {
		if r.line < 0 && r.hunk == hi {
			ps.cursor = i
			ps.endRange()
			ps.toggleHunk()
			m.markDirty()
		}
	}
	return nil
}

// gitViewerWritable reports a Git viewer whose entry offers writes, so it
// must not be labelled read-only.
func (m *Model) gitViewerWritable() bool {
	vw := m.viewer
	return vw != nil && vw.git != nil && vw.git.pinned && m.gitWritesEnabled()
}

// gitPartialAfterWrite refetches the viewer's hunks after any reply to a
// partial write for the path it shows (success, stale_diff or failure).
func (m *Model) gitPartialAfterWrite(msg gitWriteMsg) tea.Cmd {
	p := msg.cmd.Git
	vw := m.viewer
	if p == nil || p.Partial == nil || vw == nil || vw.git == nil || vw.git.partial == nil {
		return nil
	}
	if vw.git.key != msg.key || vw.git.path != p.Partial.Path || vw.git.group != p.Partial.Group {
		return nil
	}
	return m.refetchGitHunks()
}

func (m *Model) refetchGitHunks() tea.Cmd {
	ps := m.viewer.git.partial
	if ps.keep == nil {
		ps.keep = ps.mark()
	}
	return m.loadGitHunks()
}

// gitPartialStatus re-pins the viewer to a refreshed status entry of the
// same path and group and refetches its hunks; a vanished entry keeps the
// changed marking of gitStatusArrived.
func (m *Model) gitPartialStatus(key string) tea.Cmd {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.partial == nil || vw.git.key != key {
		return nil
	}
	g := vw.git
	ps := g.partial
	e, ok := m.gitViews[key].entry(g.group, g.path)
	if !ok || !gitEntryWritable(e) {
		// Vanished or no longer writable: late rereads must never adopt it.
		ps.pending = nil
		return nil
	}
	if e.Pin == g.entry.Pin && !g.changed || ps.pending != nil && ps.pending.Pin == e.Pin {
		return nil
	}
	// Adopt the new pin only once its patch and hunks are both on screen
	// (gitRepinDone); until then g.changed refuses every write.
	g.changed = true
	ps.pending, ps.pendDiff, ps.pendHunks = &e, false, false
	diff := m.reloadGitViewerDiff()
	return tea.Batch(diff, m.refetchGitHunks())
}

// reloadGitViewerDiff rereads the viewer's raw patch under a new load ID.
func (m *Model) reloadGitViewerDiff() tea.Cmd {
	vw := m.viewer
	g := vw.git
	api := m.gitClient()
	if api == nil {
		return nil
	}
	m.viewerSeq++
	vw.loadID, vw.loading, vw.loadErr = m.viewerSeq, true, ""
	id, target, path, group := vw.loadID, g.target, g.path, g.group
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		diff, err := api.GitDiff(deadline, target, path, group)
		return gitViewerMsg{id: id, diff: &diff, err: err}
	}
}

// gitPartialDiffAccepted records that the raw patch for a pending pin is
// displayed.
func (m *Model) gitPartialDiffAccepted() {
	if vw := m.viewer; vw != nil && vw.git != nil && vw.git.partial != nil && vw.git.partial.pending != nil {
		vw.git.partial.pendDiff = true
		m.gitRepinDone()
	}
}

// gitPartialRereadFailed drops a pending re-pin whose patch or hunks read
// failed. The viewer stays changed; the next status showing that pin (or
// any other) starts the rereads again, so retries follow status arrivals
// only.
func (m *Model) gitPartialRereadFailed() {
	if vw := m.viewer; vw != nil && vw.git != nil && vw.git.partial != nil {
		vw.git.partial.pending = nil
	}
}

// gitRepinDone adopts the pending pin once both reads are displayed.
func (m *Model) gitRepinDone() {
	g := m.viewer.git
	ps := g.partial
	if ps.pending == nil || !ps.pendDiff || !ps.pendHunks {
		return
	}
	g.entry, g.changed = *ps.pending, false
	ps.pending = nil
}

// gitPartialUnsupportedCopy explains why a path keeps whole-file actions.
func gitPartialUnsupportedCopy(code string) string {
	reason := map[string]string{
		protocol.GitPartialUnsupportedConflicted: "conflicted file",
		protocol.GitPartialUnsupportedSubmodule:  "submodule",
		protocol.GitPartialUnsupportedSymlink:    "symbolic link",
		protocol.GitPartialUnsupportedBinary:     "binary file",
		protocol.GitPartialUnsupportedFilter:     "filter attribute (for example LFS)",
		protocol.GitPartialUnsupportedEncoding:   "working-tree encoding",
		protocol.GitPartialUnsupportedDeleted:    "deleted file",
		protocol.GitPartialUnsupportedTypeChange: "file type changed",
		protocol.GitPartialUnsupportedRename:     "renamed intent-to-add file",
		protocol.GitPartialUnsupportedNotRegular: "not a regular file",
		protocol.GitPartialUnsupportedSkip:       "skip-worktree or assume-unchanged",
		protocol.GitPartialUnsupportedNoContent:  "mode change only",
		protocol.GitPartialUnsupportedTooLarge:   "diff too large",
		protocol.GitPartialUnsupportedUnparsable: "diff could not be parsed",
	}[code]
	if reason == "" {
		reason = "not supported for this file"
	}
	return "Whole file only · " + reason
}

// gitPartialPairs are the viewer pairs describing partial staging.
func (m *Model) gitPartialPairs(g *gitViewerContent) [][2]string {
	ps := g.partial
	if ps == nil {
		return nil
	}
	var pairs [][2]string
	if g.group == protocol.GitGroupStaged {
		pairs = append(pairs, [2]string{"Compares", "HEAD → index · lines here are staged"})
	} else {
		pairs = append(pairs, [2]string{"Compares", "index → working tree · lines here are not staged"})
	}
	switch {
	case ps.err != "":
		pairs = append(pairs, [2]string{"Lines", "Hunks unavailable · " + ps.err})
	case ps.hunks == nil && ps.loading:
		pairs = append(pairs, [2]string{"Lines", "Loading hunks…"})
	case ps.hunks != nil && ps.hunks.Unsupported != "":
		pairs = append(pairs, [2]string{"Lines", gitPartialUnsupportedCopy(ps.hunks.Unsupported)})
	case ps.hunks != nil && ps.hunks.ModeChanged:
		pairs = append(pairs, [2]string{"Mode", "Mode change is not included · use whole-file actions"})
	}
	return pairs
}

// gitPartialKeys is the viewer's Keys pair while hunks are selectable.
func gitPartialKeys(group string) string {
	verb, key := gitPartialVerb(group)
	whole := strings.ToUpper(key) + " whole file"
	if group == protocol.GitGroupUnstaged {
		whole += " · d Discard file"
	}
	return "Space line · a hunk · v range · " + key + " " + verb + " · " + whole
}

// gitPartialScroll keeps row visible in the viewer body.
func (m *Model) gitPartialScroll(row, scroll int) int {
	h := max(1, m.viewerLayout().body.H)
	if row < scroll {
		return row
	}
	if row >= scroll+h {
		return row - h + 1
	}
	return scroll
}

var gitPartialIcons = map[string]struct{ glyph, plain string }{
	"selected": {"●", "*"},
}

func (m *Model) gitPartialIcon(name string) string {
	i := gitPartialIcons[name]
	if m.plainIcons {
		return i.plain
	}
	return i.glyph
}

// gitPartialText sanitizes a diff line for painting: a CR becomes a visible
// marker, every other control, escape sequence and bidi control is removed
// by safe().
func gitPartialText(s string) string {
	return safe(strings.ReplaceAll(s, "\r", "␍"))
}

// gitPartialHidden reports whether sanitizing removed anything from a line
// (escape sequences, other controls, bidi controls), which the row then
// marks with a visible ␛ so no invisible bytes are staged unnoticed.
func gitPartialHidden(s string) bool {
	s = strings.ReplaceAll(s, "\r", "␍")
	return safe(s) != strings.ReplaceAll(s, "\t", "    ")
}

// renderGitPartial paints the selectable hunk rows into the viewer body.
// Column 0 is the cursor mark, column 1 the selection mark, column 2 the
// diff prefix; long lines truncate.
func (m *Model) renderGitPartial(f *frame, body shell.Rect) {
	vw := m.viewer
	ps := vw.git.partial
	p := m.colors()
	maxOffset := max(0, len(ps.rows)-body.H)
	offset := min(max(0, vw.scroll), maxOffset)
	f.viewerMax = maxOffset
	f.viewerBody = body
	focused := m.focus == "viewer-body"
	for i := 0; i < body.H && offset+i < len(ps.rows); i++ {
		ri := offset + i
		r := ps.rows[ri]
		y := body.Y + i
		cursor := ri == ps.cursor
		hkey := "gitp-hunk:" + strconv.Itoa(r.hunk)
		bg := p.input
		if r.line < 0 && (m.hover == hkey || cursor && focused) {
			bg = m.hoverFill()
		}
		if cursor {
			// The cursor mark is the body's focus mark: accent while the
			// body has keyboard focus, muted otherwise.
			ink := p.muted
			if focused {
				ink = p.blue
			}
			f.text(body.X, y, 1, m.icon("focus"), ink, bg)
		} else {
			f.text(body.X, y, 1, " ", p.text, bg)
		}
		tx, tw := body.X+1, max(0, body.W-1)
		if r.line < 0 {
			hk := ps.hunks.Hunks[r.hunk]
			label := "Select hunk"
			lw := len(label)
			head := gitPartialText(singleLine(hk.Header))
			if tw > lw+12 {
				f.componentText(tx, y, tw-lw-1, " "+head, componentVisual{foreground: p.blue, background: bg})
				f.componentText(tx+tw-lw-1, y, lw+1, " "+label, componentVisual{foreground: p.muted, background: bg, bold: cursor && focused || m.hover == hkey})
			} else {
				f.componentText(tx, y, tw, " "+head, componentVisual{foreground: p.blue, background: bg})
			}
			f.hits = append(f.hits, hit{Rect: shell.Rect{X: body.X, Y: y, W: body.W, H: 1}, Label: "Select or clear this hunk · a", Key: hkey, Action: action{Kind: "git-partial-hunk", Index: r.hunk}})
			continue
		}
		ln := ps.line(r)
		if r.eof {
			f.componentText(tx, y, tw, "  \\ No newline at end of file", componentVisual{foreground: p.muted, background: bg})
			continue
		}
		prefix, ink := " ", p.text
		switch ln.Kind {
		case protocol.GitHunkLineAdd:
			prefix, ink = "+", p.green
		case protocol.GitHunkLineDelete:
			prefix, ink = "-", p.red
		}
		sel := ps.selected[ln.Index]
		mark := " "
		if sel {
			mark = m.gitPartialIcon("selected")
		}
		f.text(tx, y, 1, mark, p.blue, bg)
		f.text(tx+1, y, min(1, max(0, tw-1)), prefix, ink, bg)
		rest, rw := tx+2, max(0, tw-2)
		if gitPartialHidden(ln.Text) && rw > 0 {
			// Sanitizing removed bytes from this line: mark it visibly.
			f.text(rest, y, 1, "␛", p.gold, bg)
			rest, rw = rest+1, rw-1
		}
		// fit() truncates at grapheme widths with a trailing "…".
		f.componentText(rest, y, rw, gitPartialText(ln.Text), componentVisual{foreground: ink, background: bg, bold: sel})
	}
	if body.H > 0 {
		r := m.viewerLayout().r
		f.scrollbar(m, shell.Rect{X: r.X + r.W - 2, Y: body.Y, W: 1, H: body.H}, "viewer", len(ps.rows), body.H, offset, p.input)
	}
}

// renderGitPartialHint paints the hint row: the selection count and keys at
// the left and the apply control at the right.
func (m *Model) renderGitPartialHint(f *frame, x, y, w int) {
	vw := m.viewer
	g, ps := vw.git, vw.git.partial
	p := m.colors()
	label := ps.actionLabel(g.group)
	_, key := gitPartialVerb(g.group)
	bw := len(label) + 2
	left := "No lines selected"
	if n := len(ps.selected); n > 0 {
		left = fmt.Sprintf("%d selected", n)
	}
	if ps.ranging {
		left += " · range"
	}
	left += " · Space a v " + key + " · Esc"
	if bw+2 >= w {
		bw = w
	} else {
		f.text(x, y, w-bw-1, left, p.muted, p.input)
	}
	k := "viewer-partial-apply"
	v := m.componentStyle(squareFill, m.controlState(false, k), p.blue, p.input)
	f.styledButton(x+w-bw, y, bw, " "+label+" ", k, action{Kind: "viewer-partial-apply"}, v)
	f.hits[len(f.hits)-1].Label = label + " · " + key
}

// gitPartialKey handles keys while the viewer shows selectable hunks.
// Movement and selection keys apply to the focused body; s/u/S/U anywhere
// in the viewer.
func (m *Model) gitPartialKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.partial == nil {
		return nil, false
	}
	g, ps := vw.git, vw.git.partial
	s := k.String()
	switch s {
	case "S", "shift+s", "U", "shift+u":
		// Whole-file actions in every state, on the pin of the content shown.
		return m.gitViewerWriteKey(strings.ToLower(strings.TrimPrefix(s, "shift+")))
	}
	if !m.gitPartialActive() {
		if s != "s" && s != "u" {
			return nil, false
		}
		switch {
		case ps.err != "":
			return m.showNoticeAs(noticeUnavailable, "Hunks unavailable · "+strings.ToUpper(s)+" acts on the whole file"), true
		case ps.hunks == nil:
			return m.showNoticeAs(noticeUnavailable, "Loading diff…"), true
		}
		return nil, false // Unsupported: s/u keep their whole-file meaning.
	}
	_, key := gitPartialVerb(g.group)
	switch s {
	case key:
		return m.gitPartialApply(-1), true
	case "s", "u":
		verb, _ := gitPartialVerb(g.group)
		return m.showNoticeAs(noticeUnavailable, "This diff offers "+key+" "+verb), true
	case "esc":
		switch {
		case ps.ranging || ps.anchor >= 0:
			ps.endRange()
		case len(ps.selected) > 0:
			ps.selected = nil
		default:
			return nil, false
		}
		m.markDirty()
		return nil, true
	}
	if m.focus != "viewer-body" {
		return nil, false
	}
	body := m.viewerLayout().body
	page := max(1, body.H-1)
	move := func(n int, extend bool) {
		if extend && ps.anchor < 0 {
			ps.startRange()
		}
		if !extend && !ps.ranging {
			ps.endRange()
		}
		ps.step(n)
		if ps.anchor >= 0 {
			ps.extend()
		}
		vw.scroll = m.gitPartialScroll(ps.cursor, vw.scroll)
	}
	switch s {
	case "up", "k":
		move(-1, false)
	case "down", "j":
		move(1, false)
	case "shift+up", "K":
		move(-1, true)
	case "shift+down", "J":
		move(1, true)
	case "pgup":
		move(-page, false)
	case "pgdown":
		move(page, false)
	case "home":
		move(-len(ps.rows), false)
	case "end":
		move(len(ps.rows), false)
	case "[", "]":
		d := 1
		if s == "[" {
			d = -1
		}
		ps.endRange()
		for i := ps.cursor + d; i >= 0 && i < len(ps.rows); i += d {
			if ps.rows[i].line < 0 {
				ps.cursor = i
				break
			}
		}
		vw.scroll = m.gitPartialScroll(ps.cursor, vw.scroll)
	case " ", "space":
		if ps.rows[ps.cursor].line < 0 {
			ps.toggleHunk()
		} else {
			ps.toggleLine(ps.cursor)
		}
		ps.endRange()
	case "a":
		ps.toggleHunk()
		ps.endRange()
	case "v":
		if ps.ranging {
			ps.endRange()
		} else {
			ps.startRange()
			ps.ranging = true
		}
	case "enter":
		if ps.rows[ps.cursor].line < 0 {
			ps.toggleHunk()
		} else {
			ps.toggleLine(ps.cursor)
		}
		ps.endRange()
	default:
		return nil, false
	}
	m.markDirty()
	return nil, true
}

// gitPartialRowAt maps a body cell to its row, or -1.
func (m *Model) gitPartialRowAt(f frame, x, y int) int {
	b := f.viewerBody
	if !b.Contains(x, y) {
		return -1
	}
	ps := m.viewer.git.partial
	row := min(max(0, m.viewer.scroll), f.viewerMax) + y - b.Y
	if row < 0 || row >= len(ps.rows) {
		return -1
	}
	return row
}

// gitPartialMouse handles pointer input over selectable hunks: a click on a
// line toggles it and starts a drag range, a click on a hunk header acts on
// the hunk.
func (m *Model) gitPartialMouse(msg tea.MouseMsg, f frame) (tea.Cmd, bool) {
	if !m.gitPartialActive() {
		return nil, false
	}
	ps := m.viewer.git.partial
	p := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if p.Button != tea.MouseLeft {
			return nil, false
		}
		row := m.gitPartialRowAt(f, p.X, p.Y)
		if row < 0 {
			return nil, false
		}
		focus := m.setFocus("viewer-body")
		ps.endRange()
		if r := ps.rows[row]; r.line < 0 {
			// A header click selects or clears the hunk, like `a`; applying
			// stays on s/u and the apply control.
			ps.cursor = row
			ps.toggleHunk()
			m.markDirty()
			return focus, true
		}
		if !ps.selectable(row) {
			return focus, true
		}
		ps.cursor = row
		ps.anchor = row
		ps.base = map[int]bool{}
		for k := range ps.selected {
			ps.base[k] = true
		}
		ps.toggleLine(row)
		if ps.selected[ps.line(ps.rows[row]).Index] {
			// A drag from a newly selected line extends a selection; from a
			// cleared line it only toggles.
			ps.drag = true
		} else {
			ps.anchor, ps.base = -1, nil
		}
		m.markDirty()
		return focus, true
	case tea.MouseMotionMsg:
		if !ps.drag {
			return nil, false
		}
		if row := m.gitPartialRowAt(f, p.X, p.Y); row >= 0 && row != ps.cursor {
			ps.cursor = ps.nearest(row)
			ps.extend()
			m.markDirty()
		}
		return nil, true
	case tea.MouseReleaseMsg:
		if ps.drag {
			ps.drag = false
			ps.anchor, ps.base = -1, nil
			m.markDirty()
		}
	}
	return nil, false
}
