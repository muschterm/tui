package tui

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_conflict.go adds manual conflict resolution (ADR 0023 S3) to the
// operation panel's conflict rows: a viewer with Base/Ours/Theirs/Working/
// Saved tabs, Choose ours/theirs/base, Edit, Mark resolved, Resolve as
// deleted and Restore of saved copies. Every command pins the path's
// ConflictPin and WorktreeToken from the file state the user viewed: the
// row's GitConflict, checked against the working file read for the action
// (a mismatch refreshes instead of sending). Commands share the per-target
// write slot of git_ref.go (Retry reuses the ID).
//
// S4 hook points (agent resolution, being built separately):
// gitConflictAgentItems returns the row menu's "Resolve with agent…" items
// (none yet) and gitConflictReviewMode reports a row under agent review
// (never yet); both are consulted by the row menu and controls.

// gitConflictAPI reads one version of a conflicted path.
type gitConflictAPI interface {
	GitConflictFile(ctx context.Context, target client.GitTarget, path, version string) (protocol.GitConflictFile, error)
}

var _ gitConflictAPI = (*client.Client)(nil)

// gitConflictTabs are the viewer's versions, in order.
var gitConflictTabs = []string{protocol.GitConflictVersionBase, protocol.GitConflictVersionOurs, protocol.GitConflictVersionTheirs,
	protocol.GitConflictVersionWorking, protocol.GitConflictVersionSaved}

// gitConflictViewer is the open conflict viewer; it takes over the Git
// surface body like the review panel.
type gitConflictViewer struct {
	key      string
	target   client.GitTarget
	conflict protocol.GitConflict // the row as shown
	tab      string
	// item is the reviewed job item when opened from review mode; it adds
	// the Agent diff and Staged diff tabs.
	item    *protocol.GitResolveItem
	itemGen string
	files   map[string]*protocol.GitConflictFile
	errs    map[string]string
	loading map[string]bool
	// reqs is each version's latest read; older replies are dropped.
	reqs map[string]uint64
	// lines are each version's display lines, built once per read.
	lines  map[string][]gitViewerLine
	scroll int
}

// gitViewerLine is one display line of a version, capped in width.
type gitViewerLine struct {
	text   string
	marker bool
}

// gitViewerLineCells caps a painted content line.
const gitViewerLineCells = 4096

// gitConflictPrep is an action waiting for the working (and saved) file.
type gitConflictPrep struct {
	key      string
	target   client.GitTarget
	conflict protocol.GitConflict
	action   string // choose-ours|choose-theirs|choose-base|resolve|delete|restore-menu|restore
	copyID   string
	working  *protocol.GitConflictFile
	saved    *protocol.GitConflictFile
	seq      uint64
	// savedDone: the saved read finished (savedUnknown when no saved copy
	// holds the path, so Choose always confirms). resolved: the path is no
	// longer unmerged (restore only; pins come from the fresh read).
	savedDone, savedUnknown, resolved bool
}

// gitConflictDialog is a shown confirmation, bound to the reviewed file.
type gitConflictDialog struct {
	prep *gitConflictPrep
	cmd  protocol.Command
}

type gitConflictUI struct {
	viewer *gitConflictViewer
	prep   *gitConflictPrep
	dialog *gitConflictDialog
	seq    uint64
}

type gitConflictFileMsg struct {
	key, path, version string
	seq                uint64
	viewer             bool
	file               protocol.GitConflictFile
	err                error
}

// gitConflictsEnabled reports a server with manual conflict resolution.
func (m *Model) gitConflictsEnabled() bool {
	return m.gitOperationsEnabled() && slices.Contains(m.snapshot.Capabilities, "git-conflicts")
}

func (m *Model) gitConflictClient() gitConflictAPI {
	if a, ok := m.gitClient().(gitConflictAPI); ok {
		return a
	}
	return nil
}

func gitConflictKind(kind string) bool {
	return kind == protocol.GitKindConflictChoose || kind == protocol.GitKindConflictResolve || kind == protocol.GitKindConflictRestore
}

// gitConflictAgentItems offers "Resolve with agent…" for one path while no
// job is attached.
func (m *Model) gitConflictAgentItems(c protocol.GitConflict) []menuItem {
	g := m.currentGitView()
	if !m.gitJobsEnabled() || g == nil || g.oper == nil || g.oper.Review != nil {
		return nil
	}
	return []menuItem{{Label: "Resolve with agent…", Action: action{Kind: "git-job-open", ID: c.Path}}}
}

// gitConflictReviewMode reports a row that an attached job covers: its
// row controls then leave only the menu.
func (m *Model) gitConflictReviewMode(c protocol.GitConflict) bool {
	g := m.currentGitView()
	if g == nil || g.oper == nil || g.oper.Review == nil {
		return false
	}
	_, ok := gitReviewItem(g.oper, c.Path)
	return ok
}

// gitRowConflict finds a displayed conflict row by path.
func (m *Model) gitRowConflict(path string) (protocol.GitConflict, bool) {
	g := m.currentGitView()
	if g == nil || g.oper == nil {
		return protocol.GitConflict{}, false
	}
	for _, c := range g.oper.Conflicts {
		if c.Path == path {
			return c, true
		}
	}
	return protocol.GitConflict{}, false
}

func (m *Model) readConflictFile(key string, target client.GitTarget, path, version string, seq uint64, viewer bool) tea.Cmd {
	api := m.gitConflictClient()
	if api == nil {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		f, err := api.GitConflictFile(deadline, target, path, version)
		return gitConflictFileMsg{key: key, path: path, version: version, seq: seq, viewer: viewer, file: f, err: err}
	}
}

// gitConflictAction handles every conflict control; ok reports that a was
// one of them. a.ID is the path.
func (m *Model) gitConflictAction(a action) (tea.Cmd, bool) {
	if !strings.HasPrefix(a.Kind, "git-conflict-") {
		return nil, false
	}
	key, target := m.gitTarget()
	switch a.Kind {
	case "git-conflict-close":
		return m.closeGitConflictViewer(), true
	case "git-conflict-tab":
		v := m.gitCF.viewer
		if v == nil || v.key != key {
			return nil, true
		}
		v.tab, v.scroll = a.Value, 0
		m.markGitReviewViewed(v)
		m.markDirty()
		return m.loadViewerTab(v), true
	case "git-conflict-tabs":
		v := m.gitCF.viewer
		if v == nil {
			return nil, true
		}
		var items []menuItem
		for _, t := range v.tabs() {
			items = append(items, menuItem{Label: title(t), Action: action{Kind: "git-conflict-tab", Value: t}})
		}
		m.showMenu("Versions", items)
		return nil, true
	case "git-conflict-confirm":
		d := m.gitCF.dialog
		m.gitCF.dialog = nil
		if d == nil || d.prep.key != key {
			return nil, true
		}
		return m.sendConflict(d.prep, d.cmd), true
	case "git-conflict-menu":
		return m.openGitConflictMenu(a.ID), true
	}
	if !m.gitConflictsEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Conflict actions need a newer server"), true
	}
	c, ok := m.gitRowConflict(a.ID)
	resolved := false
	if !ok {
		if a.Kind != "git-conflict-restore-menu" && a.Kind != "git-conflict-restore" && a.Kind != "git-conflict-view" {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindConflictChoose, "not_conflicted")), m.refreshGit()), true
		}
		// Resolved since: its saved copies can still be viewed and restored.
		c, resolved = protocol.GitConflict{Path: a.ID}, true
	}
	g := m.gitViews[key]
	switch a.Kind {
	case "git-conflict-view":
		m.gitCF.viewer = newGitConflictViewer(key, target, c)
		m.viewState().DetailScroll = 0
		m.markDirty()
		return tea.Batch(m.setFocus("git:conflict-close"), m.loadViewerTab(m.gitCF.viewer)), true
	case "git-conflict-edit":
		return m.editConflict(c), true
	}
	if reason := m.gitRefBlock(key, g, protocol.GitKindConflictChoose); reason != "" && reason != gitErrorCopy("operation_in_progress") && reason != gitErrorCopy("conflicted") {
		return m.showNoticeAs(noticeUnavailable, reason), true
	}
	if c.Submodule {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindConflictChoose, "not_supported")), true
	}
	act := strings.TrimPrefix(a.Kind, "git-conflict-")
	switch act {
	case "choose-ours", "choose-theirs", "choose-base", "resolve", "delete", "restore-menu", "restore":
	default:
		return nil, true
	}
	m.gitCF.seq++
	if st := m.gitWriteFor(key); st != nil && (st.running || st.transport != "") {
		return m.showNoticeAs(noticeUnavailable, gitPendingCopy), true
	}
	p := &gitConflictPrep{key: key, target: target, conflict: c, action: act, copyID: a.Value, seq: m.gitCF.seq, resolved: resolved}
	m.gitCF.prep = p
	cmds := []tea.Cmd{m.readConflictFile(key, target, c.Path, protocol.GitConflictVersionWorking, p.seq, false)}
	if strings.HasPrefix(act, "choose-") {
		cmds = append(cmds, m.readConflictFile(key, target, c.Path, protocol.GitConflictVersionSaved, p.seq, false))
	}
	return tea.Batch(cmds...), true
}

// loadViewerTab reads the viewer's tab when not loaded.
func (m *Model) loadViewerTab(v *gitConflictViewer) tea.Cmd {
	if v.tab == gitViewerAgentDiff || v.tab == gitViewerStagedDiff || v.files[v.tab] != nil || v.loading[v.tab] {
		return nil
	}
	v.loading[v.tab] = true
	m.gitCF.seq++
	v.reqs[v.tab] = m.gitCF.seq
	return m.readConflictFile(v.key, v.target, v.conflict.Path, v.tab, m.gitCF.seq, true)
}

func newGitConflictViewer(key string, target client.GitTarget, c protocol.GitConflict) *gitConflictViewer {
	v := &gitConflictViewer{key: key, target: target, conflict: c, tab: protocol.GitConflictVersionWorking}
	v.reset()
	return v
}

// reset forgets every read; replies to earlier reads are then dropped.
func (v *gitConflictViewer) reset() {
	v.files, v.errs, v.loading = map[string]*protocol.GitConflictFile{}, map[string]string{}, map[string]bool{}
	v.reqs, v.lines = map[string]uint64{}, map[string][]gitViewerLine{}
	if v.item != nil {
		v.lines[gitViewerAgentDiff] = gitDiffLines(v.item.Diff)
		v.lines[gitViewerStagedDiff] = gitDiffLines(v.item.IndexDiff)
	}
}

// tabs are the viewer's versions: the review diffs first when present.
func (v *gitConflictViewer) tabs() []string {
	var out []string
	if v.item != nil && v.item.Diff != "" {
		out = append(out, gitViewerAgentDiff)
	}
	if v.item != nil && v.item.IndexDiff != "" {
		out = append(out, gitViewerStagedDiff)
	}
	return append(out, gitConflictTabs...)
}

// gitViewerLines splits a text version into capped display lines.
func gitViewerLines(f *protocol.GitConflictFile) []gitViewerLine {
	raw := strings.Split(strings.TrimRight(string(f.Content), "\n"), "\n")
	out := make([]gitViewerLine, 0, min(len(raw), gitConflictViewerLines))
	for i, line := range raw {
		if i == gitConflictViewerLines {
			break
		}
		line = strings.TrimRight(line, "\r")
		marker := gitMarkerLine(line)
		line = safe(line)
		if ansi.StringWidth(line) > gitViewerLineCells {
			line = ansi.Truncate(line, gitViewerLineCells, "…")
		}
		out = append(out, gitViewerLine{text: line, marker: marker})
	}
	return out
}

func (m *Model) acceptConflictFile(msg gitConflictFileMsg) tea.Cmd {
	current, _ := m.gitTarget()
	if msg.key != current {
		return nil
	}
	if msg.viewer {
		v := m.gitCF.viewer
		if v == nil || v.key != msg.key || v.conflict.Path != msg.path || v.reqs[msg.version] != msg.seq {
			return nil // an older read, or another viewer
		}
		delete(v.loading, msg.version)
		if msg.err != nil {
			v.errs[msg.version] = safe(singleLine(msg.err.Error()))
		} else {
			f := msg.file
			v.files[msg.version], v.errs[msg.version] = &f, ""
			if f.Present && !f.Binary && !f.Symlink {
				v.lines[msg.version] = gitViewerLines(&f)
			}
		}
		m.markDirty()
		return nil
	}
	p := m.gitCF.prep
	if p == nil || p.seq != msg.seq || p.conflict.Path != msg.path {
		return nil // stale
	}
	var pe *protocol.Error
	switch {
	case msg.err != nil && msg.version == protocol.GitConflictVersionSaved && errors.As(msg.err, &pe) && pe.Code == "not_found":
		// No saved copy holds the path: whether the working file holds
		// edits is unknown, so Choose always confirms.
		p.savedDone, p.savedUnknown = true, true
	case msg.err != nil:
		m.gitCF.prep = nil
		return m.showNoticeAs(noticeError, "Could not read "+safe(singleLine(msg.path))+" · "+safe(singleLine(msg.err.Error())))
	case msg.version == protocol.GitConflictVersionSaved:
		f := msg.file
		p.saved, p.savedDone = &f, true
	default:
		f := msg.file
		p.working = &f
	}
	if p.working == nil || strings.HasPrefix(p.action, "choose-") && !p.savedDone {
		return nil
	}
	m.gitCF.prep = nil
	// Pins come from the row the user acted on and, when the viewer shows
	// the path, from the working version it displays; a fresh read with
	// other pins means the file changed since shown.
	stale := !p.resolved && (p.working.ConflictPin != p.conflict.ConflictPin || p.working.WorktreeToken != p.conflict.WorktreeStat)
	if v := m.gitCF.viewer; v != nil && v.key == p.key && v.conflict.Path == p.conflict.Path {
		if shown := v.files[protocol.GitConflictVersionWorking]; shown != nil && shown.WorktreeToken != p.working.WorktreeToken ||
			shown == nil && v.loading[protocol.GitConflictVersionWorking] {
			stale = true
		}
	}
	if stale {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindConflictChoose, "stale_entry")), m.refreshGit())
	}
	return m.prepareConflict(p)
}

// prepareConflict shows the action's confirmation, or sends it when none is
// needed.
func (m *Model) prepareConflict(p *gitConflictPrep) tea.Cmd {
	c, w := p.conflict, p.working
	path := truncatePathLeft(safe(singleLine(c.Path)), gitDialogPathWidth)
	switch p.action {
	case "choose-ours", "choose-theirs", "choose-base":
		side := strings.TrimPrefix(p.action, "choose-")
		cmd := client.GitConflictChooseCommand(identity(), p.target, c, side)
		if !p.savedUnknown && gitConflictSameContent(w, p.saved) {
			return m.sendConflict(p, cmd)
		}
		label := m.gitSideLabel(side)
		notes := []string{"Replace your edits in " + path + " with " + label + "?", "A copy is kept · Restore brings it back"}
		if p.savedUnknown {
			notes = []string{"Replace " + path + " with " + label + "?", "No saved copy of this stop holds it, so it may contain your edits", "A copy is kept when the file can be copied"}
		}
		return m.showConflictDialog(p, cmd, "Choose "+side+" · ", path, notes, "Replace with "+side, true)
	case "resolve":
		if !w.Present {
			return m.showNoticeAs(noticeUnavailable, "The file is absent · resolve it as deleted instead")
		}
		ack := w.HasMarkers || w.MarkersUnknown || w.Binary
		cmd := client.GitConflictResolveCommand(identity(), p.target, *w, protocol.GitConflictAsContent, ack)
		if !ack {
			return m.sendConflict(p, cmd)
		}
		note := path + " still contains conflict markers"
		switch {
		case w.Binary:
			note = path + " is binary · markers cannot be checked"
		case w.MarkersUnknown:
			note = path + " is too large to check for markers"
		}
		return m.showConflictDialog(p, cmd, "Mark resolved · ", path, []string{note, "Stage it as it is?"}, "Stage as it is", true)
	case "delete":
		cmd := client.GitConflictResolveCommand(identity(), p.target, *w, protocol.GitConflictAsDeleted, false)
		return m.showConflictDialog(p, cmd, "Resolve as deleted · ", path, []string{
			"Resolve " + path + " as deleted?",
			"Git records the deletion; the file itself stays on disk as untracked",
		}, "Resolve as deleted", true)
	case "restore-menu":
		return m.showRestoreMenu(p)
	case "restore":
		cmd := client.GitConflictRestoreCommand(identity(), p.target, *w, p.copyID)
		what := gitCopyLabel(w, p.copyID)
		notes := []string{"Replace " + path + " with the " + what + "?", "What it replaces is copied first · Restore brings it back"}
		if r := gitCopyReason(w, p.copyID); r == protocol.GitCopyAtStop || r == protocol.GitCopyBeforeFirstChange {
			notes = append(notes, "Its saved conflict stages make the path unmerged again")
		}
		return m.showConflictDialog(p, cmd, "Restore · ", path, notes, "Restore "+what, true)
	}
	return nil
}

// gitConflictSameContent reports a working file identical to the stop's
// saved content (nothing of the user's would be replaced).
func gitConflictSameContent(w, saved *protocol.GitConflictFile) bool {
	if w == nil || saved == nil {
		return false
	}
	if !w.Present && !saved.Present {
		return true
	}
	return w.Present == saved.Present && w.Oid != "" && w.Oid == saved.Oid && w.Mode == saved.Mode
}

// gitSideLabel names a side with the operation's own label.
func (m *Model) gitSideLabel(side string) string {
	g := m.currentGitView()
	label := ""
	if g != nil && g.oper != nil {
		label = map[string]string{"ours": g.oper.Sides.Ours, "theirs": g.oper.Sides.Theirs, "base": g.oper.Sides.Base}[side]
	}
	if l := safe(singleLine(label)); l != "" {
		return side + " (" + truncateCells(l, 30) + ")"
	}
	return side
}

// gitCopyLabel names a saved copy: its reason and time.
func gitCopyLabel(f *protocol.GitConflictFile, copyID string) string {
	reason, when := gitCopyReason(f, copyID), gitCopyTime(f, copyID)
	label := "saved copy"
	switch reason {
	case protocol.GitCopyAtStop:
		label = "original conflict"
	case protocol.GitCopyBeforeFirstChange:
		label = "copy before the first change"
	case protocol.GitCopyBeforeOverwrite:
		label = "copy before overwrite"
	case "before_job":
		label = "copy before the agent ran"
	}
	if age := gitRelativeTime(when, gitNow()); age != "" {
		label += " (" + age + ")"
	}
	return label
}

func (m *Model) showConflictDialog(p *gitConflictPrep, cmd protocol.Command, title, user string, notes []string, verb string, destructive bool) tea.Cmd {
	m.gitCF.dialog = &gitConflictDialog{prep: p, cmd: cmd}
	var items []menuItem
	for _, n := range notes {
		items = append(items, menuItem{Note: n})
	}
	confirm := action{Kind: "git-conflict-confirm"}
	if destructive {
		confirm.Value = "destructive"
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}}, menuItem{Label: verb, Action: confirm})
	m.showMenuFor(title, user, items)
	m.menuIndex = len(items) - 2
	return nil
}

// showRestoreMenu lists the path's saved copies.
func (m *Model) showRestoreMenu(p *gitConflictPrep) tea.Cmd {
	w := p.working
	ids := []string{}
	if w.CopyID != "" {
		ids = append(ids, w.CopyID)
	}
	for _, c := range w.Copies {
		if !slices.Contains(ids, c.CopyID) {
			ids = append(ids, c.CopyID)
		}
	}
	if len(ids) == 0 {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindConflictRestore, "no_saved_copy"))
	}
	labels := make([]string, len(ids))
	seen := map[string]int{}
	for i, id := range ids {
		labels[i] = gitCopyLabel(w, id)
		seen[labels[i]]++
	}
	var items []menuItem
	for i, id := range ids {
		label := labels[i]
		if seen[label] > 1 {
			// Relative ages collide: add the absolute time.
			if t, err := time.Parse(time.RFC3339, gitCopyTime(w, id)); err == nil {
				label += " · " + t.Local().Format("Jan 2 15:04:05")
			}
		}
		items = append(items, menuItem{Label: "Restore the " + label + "…", Action: action{Kind: "git-conflict-restore", ID: p.conflict.Path, Value: id}})
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}})
	m.showMenuFor("Restore · ", truncatePathLeft(safe(singleLine(p.conflict.Path)), gitDialogPathWidth), items)
	m.menuIndex = len(items) - 1
	return nil
}

func (m *Model) sendConflict(p *gitConflictPrep, cmd protocol.Command) tea.Cmd {
	// Re-check right before sending: a write may have started meanwhile.
	if g := m.gitViews[p.key]; g != nil {
		if reason := m.gitRefBlock(p.key, g, cmd.Kind); reason != "" && reason != gitErrorCopy("operation_in_progress") && reason != gitErrorCopy("conflicted") {
			return m.showNoticeAs(noticeUnavailable, reason)
		}
	}
	m.clearGitWarning(p.key)
	return m.sendGitWrite(p.key, cmd, p.conflict.Path)
}

// editConflict opens the file in the Files surface (shared documents when
// editable).
func (m *Model) editConflict(c protocol.GitConflict) tea.Cmd {
	if c.Submodule || c.Binary {
		return m.showNoticeAs(noticeUnavailable, "Not editable here · binary or submodule")
	}
	g := m.currentGitView()
	checkout := ""
	var o *protocol.GitOperationState
	if g != nil && g.status != nil {
		checkout, o = g.status.Workspace.Path, g.oper
	}
	path, reason := gitConflictEditPath(gitOperationToplevel(o), checkout, c.Path)
	if reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	m.gitCF.viewer = nil
	m.openSurface("files", "")
	return m.openFilesBuffer(path)
}

// acceptConflictRefusal handles an acknowledgement the server asked for.
func (m *Model) acceptConflictRefusal(key string, cmd protocol.Command, pe *protocol.Error) (tea.Cmd, bool) {
	if pe.Code != "unsaved_unacknowledged" || cmd.Git == nil || cmd.Git.Conflict == nil {
		return nil, false
	}
	next := cmd
	w := *cmd.Git.Conflict
	w.AcknowledgeUnsaved = w.WorktreeToken
	gw := *cmd.Git
	gw.Conflict = &w
	next.Git = &gw
	next.ID = identity()
	c, _ := m.gitRowConflict(w.Path)
	c.Path = w.Path
	p := &gitConflictPrep{key: key, conflict: c}
	path := truncatePathLeft(safe(singleLine(w.Path)), gitDialogPathWidth)
	return m.showConflictDialog(p, next, "Overwrite without a copy · ", path, []string{
		path + " cannot be copied first (larger than 64 MiB or not a regular file)",
		"Overwrite it without a copy?",
	}, "Overwrite without a copy", true), true
}

// gitConflictResultBlocks adds the Previous copy and evictions to a
// conflict command's result.
func (m *Model) gitConflictResultBlocks(st *gitWriteState) []surfaceBlock {
	p := m.colors()
	r := st.result
	if r == nil || r.Operation == nil {
		return nil
	}
	var b []surfaceBlock
	if prev := r.Operation.Previous; prev != nil && prev.CopyID != "" && st.cmd.Git.Conflict != nil {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Your previous content was saved", ink: p.muted},
			surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: "Restore previous content…", mark: m.icon("reopen"), key: "git:conflict-previous",
				action: action{Kind: "git-conflict-restore", ID: st.cmd.Git.Conflict.Path, Value: prev.CopyID}, help: "Restore the content this replaced"}})
	}
	if n := len(r.Operation.Evicted); n > 0 {
		line := "1 older saved copy of this stop was evicted to make room"
		if n > 1 {
			line = strconv.Itoa(n) + " older saved copies of this stop were evicted to make room"
		}
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: line, ink: p.gold})
	}
	return b
}

// gitConflictDone is the success copy.
func gitConflictDone(st *gitWriteState) string {
	w := st.cmd.Git.Conflict
	path := safe(singleLine(w.Path))
	switch st.cmd.Kind {
	case protocol.GitKindConflictChoose:
		return "Wrote " + safe(singleLine(w.Side)) + " into " + path + " · still unmerged"
	case protocol.GitKindConflictResolve:
		if w.As == protocol.GitConflictAsDeleted {
			return "Resolved " + path + " as deleted · the file stays untracked"
		}
		return "Marked " + path + " resolved"
	case protocol.GitKindConflictRestore:
		return "Restored " + path
	}
	return "Done"
}

// jobThread reports a job-kind thread (ADR 0023 S4): an agent resolving a
// Git operation's conflicts. Job threads belong under the Git operation
// panel, so navigation, search, counts, attention and Closed skip them.
func jobThread(t protocol.Thread) bool { return t.Job != nil }

const gitJobLeaseCopy = "Resolution job running · stop it first"

// gitJobLeaseHeld reports a running resolution job in the repository.
func (m *Model) gitJobLeaseHeld(path string) bool {
	root := m.gitRepoRoot(path)
	for _, t := range m.snapshot.Threads {
		if jobThread(t) && !t.Closed && activeTurn(t) && (gitRepoOverlap(t.Checkout, root) || gitRepoOverlap(t.Checkout, path)) {
			return true
		}
	}
	return false
}

// jobFallbackThread replaces an Active thread that is missing or a job
// thread: an open thread of the job's project, else the next open thread.
func (m *Model) jobFallbackThread(active string) string {
	if t, ok := m.threadByID(active); ok && jobThread(t) {
		for _, o := range m.snapshot.Threads {
			if !jobThread(o) && !o.Closed && o.ProjectID == t.ProjectID {
				return o.ID
			}
		}
	}
	return m.nextOpenThread("")
}

// openJobGitPanel shows the Git operation panel a job thread belongs to:
// an open thread of the job's project (unless the active one already
// shows that checkout), then the Git surface.
func (m *Model) openJobGitPanel(job protocol.Thread) tea.Cmd {
	var cmd tea.Cmd
	if cur := m.thread(); !gitRepoOverlap(cur.Checkout, job.Checkout) {
		for _, o := range m.snapshot.Threads {
			if !jobThread(o) && !o.Closed && o.ProjectID == job.ProjectID {
				cmd = m.activate(action{Kind: "thread", ID: o.ID})
				break
			}
		}
	}
	return tea.Batch(cmd, m.activate(action{Kind: "open", Value: "git"}))
}

// gitCopyReason and gitCopyTime describe one saved copy of f's path.
func gitCopyReason(f *protocol.GitConflictFile, copyID string) string {
	reason := ""
	if copyID == f.CopyID {
		reason = f.CopyReason
	}
	for _, c := range f.Copies {
		if c.CopyID == copyID && c.Reason != "" {
			reason = c.Reason
		}
	}
	return reason
}

func gitCopyTime(f *protocol.GitConflictFile, copyID string) string {
	when := ""
	if copyID == f.CopyID {
		when = f.CopyCreatedAt
	}
	for _, c := range f.Copies {
		if c.CopyID == copyID && c.CreatedAt != "" {
			when = c.CreatedAt
		}
	}
	return when
}

// gitOperationToplevel is the repository toplevel of the operation, when
// the server reports it (GitOperationState.Toplevel, S4 server); until then
// it is empty and Edit is unavailable.
func gitOperationToplevel(o *protocol.GitOperationState) string {
	if o == nil {
		return ""
	}
	return o.Toplevel
}

// gitConflictEditPath maps a toplevel-relative conflict path to the Files
// surface's checkout-relative path; reason explains when it cannot.
func gitConflictEditPath(toplevel, checkout, path string) (string, string) {
	if toplevel == "" || checkout == "" {
		return "", "Edit needs the repository location from a newer server"
	}
	rel, err := filepath.Rel(filepath.Clean(checkout), filepath.Join(toplevel, filepath.FromSlash(path)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "The file is outside this checkout · open it from the repository's checkout"
	}
	return filepath.ToSlash(rel), ""
}

// closeGitConflictViewer closes the viewer, returning focus to its row.
func (m *Model) closeGitConflictViewer() tea.Cmd {
	v := m.gitCF.viewer
	m.gitCF.viewer = nil
	m.markDirty()
	if v == nil {
		return nil
	}
	if _, ok := m.gitRowConflict(v.conflict.Path); ok {
		return m.setFocus("git:conflict:" + v.conflict.Path)
	}
	return m.setFocus("git-refresh")
}

// gitViewerTabKey cycles the viewer's tabs with [ and ].
func (m *Model) gitViewerTabKey(s string) (tea.Cmd, bool) {
	v := m.gitCF.viewer
	if v == nil || s != "[" && s != "]" || !(gitFocusKey(m.focus) || m.focus == "right-body") {
		return nil, false
	}
	tabs := v.tabs()
	i := slices.Index(tabs, v.tab)
	if s == "]" {
		i = (i + 1) % len(tabs)
	} else {
		i = (i - 1 + len(tabs)) % len(tabs)
	}
	return m.activate(action{Kind: "git-conflict-tab", Value: tabs[i]}), true
}

// gitNoDiffReason explains a review item without a diff.
func gitNoDiffReason(it *protocol.GitResolveItem) string {
	switch {
	case it == nil:
		return "No diff"
	case it.Unknown:
		return "No diff · the item could not be compared (error or time budget)"
	case it.Binary:
		return "No diff · binary content"
	case it.Deleted:
		return "No diff · the agent deleted the file"
	case !it.Changed && !it.Staged:
		return "No diff · unchanged since before the agent ran"
	}
	return "No diff was supplied"
}

// gitLinesCapped reports display lines cut by the viewer's caps.
func gitLinesCapped(lines []gitViewerLine) bool {
	if len(lines) >= gitConflictViewerLines {
		return true
	}
	for _, l := range lines {
		if strings.HasSuffix(l.text, "…") && ansi.StringWidth(l.text) >= gitViewerLineCells {
			return true
		}
	}
	return false
}
