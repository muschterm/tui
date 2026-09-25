package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_surface.go is the read-only Git surface (docs/design/go-slice.md ›
// Read-only Git surface). It observes the selected checkout through the
// server's GET /v1/git/* reads and never offers a mutation. Every read runs
// as a tea.Cmd; results carry their target key and generation and are
// dropped unless they still describe the displayed target. Loaded results
// stay per target so switching back paints immediately, then refreshes.

// gitLogLimit is how many recent commits the surface loads.
const gitLogLimit = 50

// gitAPI is the subset of the server client used by the Git surface;
// tests inject a fake.
type gitAPI interface {
	GitStatus(ctx context.Context, target client.GitTarget) (protocol.GitStatus, error)
	GitDiff(ctx context.Context, target client.GitTarget, path, group string) (protocol.GitDiff, error)
	GitLog(ctx context.Context, target client.GitTarget, limit int) (protocol.GitLog, error)
	GitShow(ctx context.Context, target client.GitTarget, commit string) (protocol.GitShow, error)
}

var _ gitAPI = (*client.Client)(nil)

func (m *Model) gitClient() gitAPI {
	if m.gitReads != nil {
		return m.gitReads
	}
	if m.client != nil {
		return m.client
	}
	return nil
}

// gitNow is the clock for relative commit times; tests pin it.
var gitNow = time.Now

// gitView is one target's client-local Git observation.
type gitView struct {
	status              *protocol.GitStatus
	log                 *protocol.GitLog
	statusErr, logErr   string
	gen                 uint64
	statusLoad, logLoad bool
}

func (g *gitView) loading() bool { return g.statusLoad || g.logLoad }

type gitStatusMsg struct {
	key    string
	gen    uint64
	status protocol.GitStatus
	err    error
}

type gitLogMsg struct {
	key string
	gen uint64
	log protocol.GitLog
	err error
}

// gitTarget is the displayed checkout: the draft's project or the active
// thread, keyed like the checkout inspection so a changed checkout is a new
// target.
func (m *Model) gitTarget() (string, client.GitTarget) {
	key, projectID, threadID := m.checkoutTarget()
	return key, client.GitTarget{ProjectID: projectID, ThreadID: threadID}
}

// gitVisible reports whether a Git surface is on screen: the right host's
// active tab (not its chooser) with a nonzero pane or compact column.
func (m *Model) gitVisible() bool {
	if !m.hasComposer() || m.settingsPage != "" || m.terminalTooSmall() {
		return false
	}
	v := m.viewState()
	active, ok := v.Host.Active()
	if !ok || v.Host.Chooser || active.Kind != "git" {
		return false
	}
	return m.workspaceGeometry(m.footerHeight()).Right.W > 0
}

// nextGitRefresh runs after every update. It reads the displayed target
// when the surface becomes visible, the target changes while visible, or
// the active thread's turn ends (running/waiting to anything else) while
// visible. There is no timer or polling.
func (m *Model) nextGitRefresh() tea.Cmd {
	key, _ := m.gitTarget()
	active := activeTurn(m.thread()) && !m.creatingThread()
	ended := m.gitTurnKey == key && m.gitTurnActive && !active
	m.gitTurnKey, m.gitTurnActive = key, active
	if key == "" || !m.gitVisible() {
		m.gitShown = ""
		return nil
	}
	if !m.connected || m.gitClient() == nil {
		// Not marked shown: the first connected update reads it.
		return nil
	}
	if key == m.gitShown && !ended {
		return nil
	}
	m.gitShown = key
	return m.refreshGit()
}

// refreshGit starts status and log reads for the displayed target, keeping
// its last result on screen until they return.
func (m *Model) refreshGit() tea.Cmd {
	key, target := m.gitTarget()
	api := m.gitClient()
	if key == "" || api == nil {
		return nil
	}
	if m.gitViews == nil {
		m.gitViews = map[string]*gitView{}
	}
	g := m.gitViews[key]
	if g == nil {
		g = &gitView{}
		m.gitViews[key] = g
	}
	m.gitSeq++
	gen := m.gitSeq
	g.gen, g.statusLoad, g.logLoad = gen, true, true
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status := func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := api.GitStatus(deadline, target)
		return gitStatusMsg{key: key, gen: gen, status: s, err: err}
	}
	log := func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		l, err := api.GitLog(deadline, target, gitLogLimit)
		return gitLogMsg{key: key, gen: gen, log: l, err: err}
	}
	return tea.Batch(status, log)
}

// acceptGitView returns the view a result belongs to, or nil when the
// result is stale: another target is displayed or a newer read started.
func (m *Model) acceptGitView(key string, gen uint64) *gitView {
	current, _ := m.gitTarget()
	g := m.gitViews[key]
	if key != current || g == nil || g.gen != gen {
		return nil
	}
	return g
}

func (m *Model) acceptGitStatus(msg gitStatusMsg) {
	g := m.acceptGitView(msg.key, msg.gen)
	if g == nil {
		return
	}
	g.statusLoad = false
	if msg.err != nil {
		g.statusErr = safe(singleLine(msg.err.Error()))
		return
	}
	s := msg.status
	g.status, g.statusErr = &s, ""
}

func (m *Model) acceptGitLog(msg gitLogMsg) {
	g := m.acceptGitView(msg.key, msg.gen)
	if g == nil {
		return
	}
	g.logLoad = false
	if msg.err != nil {
		g.logErr = safe(singleLine(msg.err.Error()))
		return
	}
	l := msg.log
	g.log, g.logErr = &l, ""
}

// gitAction handles the surface's own controls.
func (m *Model) gitAction(a action) tea.Cmd {
	switch a.Kind {
	case "git-refresh":
		if !m.connected || m.gitClient() == nil {
			return m.showNoticeAs(noticeUnavailable, "Connect to the server to read Git status")
		}
		key, _ := m.gitTarget()
		m.gitShown = key
		return m.refreshGit()
	case "git-open", "git-commit":
		return m.openGitViewer(a)
	}
	return nil
}

// gitRow is one activatable Git surface row: a status entry (mark and path)
// or a commit (short hash, refs, subject and relative time).
type gitRow struct {
	mark, markInk string
	path          string
	hash, subject string
	refs          []string
	when          string
	help          string
	action        action
	key           string
}

var gitSections = []struct{ group, title, label string }{
	{protocol.GitGroupConflicted, "Conflicts", "Conflicted"},
	{protocol.GitGroupStaged, "Staged", "Staged"},
	{protocol.GitGroupUnstaged, "Changes", "Unstaged"},
	{protocol.GitGroupUntracked, "Untracked", "Untracked"},
}

// gitGroupLabel names a status group for titles and help.
func gitGroupLabel(group string) string {
	for _, s := range gitSections {
		if s.group == group {
			return s.label
		}
	}
	return title(group)
}

// gitEntryMark is the one-letter status and its semantic ink: added green,
// modified gold, deleted red, renamed/copied accent, conflicted red and
// untracked muted.
func (m *Model) gitEntryMark(e protocol.GitStatusEntry) (string, string) {
	p := m.colors()
	letter := e.Worktree
	switch e.Group {
	case protocol.GitGroupStaged:
		letter = e.Index
	case protocol.GitGroupConflicted:
		return "U", p.red
	case protocol.GitGroupUntracked:
		return "?", p.muted
	}
	switch letter {
	case "A":
		return "A", p.green
	case "M", "T":
		return letter, p.gold
	case "D":
		return "D", p.red
	case "R", "C":
		return letter, p.blue
	}
	letter = safe(singleLine(letter))
	if ansi.StringWidth(letter) != 1 {
		letter = "·"
	}
	return letter, p.muted
}

func gitEntryPath(e protocol.GitStatusEntry) string {
	path := safe(singleLine(e.Path))
	if e.OrigPath != "" {
		path = safe(singleLine(e.OrigPath)) + " → " + path
	}
	return path
}

func gitEntryKey(e protocol.GitStatusEntry) string {
	return "git:" + e.Group + ":" + e.Path
}

// gitRefLabel shortens a full ref name for display.
func gitRefLabel(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ref
}

// gitRelativeTime is a compact age: now, 5m, 3h, 2d, 3w, 4mo or 2y.
func gitRelativeTime(stamp string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 14*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d/(7*24*time.Hour))) + "w"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d/(30*24*time.Hour))) + "mo"
	}
	return strconv.Itoa(int(d/(365*24*time.Hour))) + "y"
}

// gitSurfaceBlocks is the Git surface body.
func (m *Model) gitSurfaceBlocks() []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	rule := []surfaceBlock{gap, {kind: surfaceRuleBlock}, gap}
	heading := func(text, value string) surfaceBlock {
		return surfaceBlock{kind: surfaceHeadingBlock, label: text, value: value}
	}
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: "Git", glyph: m.icon("refresh"),
		action: action{Kind: "git-refresh"}, key: "git-refresh"}, gap}
	b = append(b, m.gitCheckoutBlocks()...)
	key, _ := m.gitTarget()
	g := m.gitViews[key]
	if g == nil {
		g = &gitView{}
	}
	s := g.status
	if s != nil && s.Upstream != "" {
		value := safe(singleLine(s.Upstream))
		note := "up to date"
		if s.Ahead > 0 || s.Behind > 0 {
			note = fmt.Sprintf("↑%d ↓%d", s.Ahead, s.Behind)
		}
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Upstream", value: value, note: note})
	} else if s != nil && s.Workspace.State == "branch" {
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Upstream", value: "none", ink: p.muted})
	}
	if s != nil && s.Operation != "" {
		glyph, ink := panelStatusMark(m, "blocked")
		b = append(b, gap, surfaceBlock{kind: surfaceStatusBlock, label: gitOperationTitle(s.Operation), glyph: glyph, ink: ink, bold: true},
			surfaceBlock{kind: surfaceTextBlock, value: "Read-only here · continue or abort it with Git", ink: p.muted})
	}
	// Loading, failure and stale markers.
	switch {
	case s == nil && g.statusErr != "":
		b = append(b, gap, statusBlock(m, "Git status", "failed", true),
			surfaceBlock{kind: surfaceTextBlock, value: g.statusErr, ink: p.muted})
		return b
	case s == nil && g.statusLoad:
		b = append(b, gap, statusBlock(m, "Reading Git status…", "pending", false))
		return b
	case s == nil:
		if !m.connected || m.gitClient() == nil {
			b = append(b, gap, statusBlock(m, "Git status", "disconnected", true))
		} else {
			b = append(b, gap, statusBlock(m, "Git status", "pending", false))
		}
		return b
	case g.statusErr != "":
		b = append(b, gap, statusBlock(m, "Showing the last result", "stale", false),
			surfaceBlock{kind: surfaceTextBlock, value: "Refresh failed · " + g.statusErr, ink: p.muted})
	case g.loading():
		b = append(b, gap, statusBlock(m, "Refreshing…", "pending", false))
	}
	switch s.Workspace.State {
	case "branch", "detached", "unborn":
	case "fixture":
		return append(b, gap, statusBlock(m, "Demo checkout", "no repository", true),
			surfaceBlock{kind: surfaceTextBlock, value: "Fixture threads have no Git data", ink: p.muted})
	case "non-git":
		return append(b, gap, statusBlock(m, "No Git repository", "", true))
	default:
		b = append(b, gap, statusBlock(m, "Git status", "unavailable", true))
		if e := safe(singleLine(s.Workspace.Error)); e != "" {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: e, ink: p.muted})
		}
		return b
	}
	groups := map[string][]protocol.GitStatusEntry{}
	for _, e := range s.Entries {
		groups[e.Group] = append(groups[e.Group], e)
	}
	b = append(b, rule...)
	if len(s.Entries) == 0 {
		b = append(b, statusBlock(m, "Working tree clean", "", false))
	}
	first := true
	for _, section := range gitSections {
		entries := groups[section.group]
		if len(entries) == 0 {
			continue
		}
		if !first {
			b = append(b, gap)
		}
		first = false
		b = append(b, heading(section.title, strconv.Itoa(len(entries))), gap)
		for _, e := range entries {
			mark, ink := m.gitEntryMark(e)
			path := gitEntryPath(e)
			b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{
				mark: mark, markInk: ink, path: path,
				help:   "Open diff · " + path + " · " + section.label,
				action: action{Kind: "git-open", Value: e.Group, ID: e.Path},
				key:    gitEntryKey(e),
			}})
		}
	}
	if s.Truncated {
		b = append(b, gap, surfaceBlock{kind: surfaceTextBlock, value: fmt.Sprintf("Showing first %d changes", len(s.Entries)), ink: p.muted})
	}
	b = append(b, rule...)
	b = append(b, m.gitCommitBlocks(g)...)
	return b
}

func gitOperationTitle(op string) string {
	switch op {
	case "cherry-pick":
		return "Cherry-pick in progress"
	case "bisect":
		return "Bisect in progress"
	}
	return title(safe(singleLine(op))) + " in progress"
}

func (m *Model) gitCommitBlocks(g *gitView) []surfaceBlock {
	p := m.colors()
	l := g.log
	count := ""
	if l != nil {
		count = strconv.Itoa(len(l.Commits))
	}
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: "Recent commits", value: count}, {kind: surfaceGapBlock}}
	switch {
	case l == nil && g.logErr != "":
		return append(b, statusBlock(m, "Commits", "failed", true), surfaceBlock{kind: surfaceTextBlock, value: g.logErr, ink: p.muted})
	case l == nil:
		return append(b, statusBlock(m, "Reading commits…", "pending", false))
	case g.logErr != "":
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Showing the last result · " + g.logErr, ink: p.muted})
	}
	if len(l.Commits) == 0 {
		return append(b, surfaceBlock{kind: surfaceTextBlock, value: "No commits yet", ink: p.muted})
	}
	now := gitNow()
	for _, c := range l.Commits {
		var refs []string
		for _, r := range c.Refs {
			refs = append(refs, safe(singleLine(gitRefLabel(r))))
		}
		subject := safe(singleLine(c.Subject))
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{
			hash: safe(singleLine(c.Short)), subject: subject, refs: refs,
			when:   gitRelativeTime(c.Time, now),
			help:   "Open commit · " + safe(singleLine(c.Short)) + " " + subject,
			action: action{Kind: "git-commit", ID: c.Hash, Value: c.Short},
			key:    "git:commit:" + c.Hash,
		}})
	}
	if l.Truncated {
		b = append(b, surfaceBlock{kind: surfaceGapBlock}, surfaceBlock{kind: surfaceTextBlock, value: fmt.Sprintf("Showing the %d most recent commits", len(l.Commits)), ink: p.muted})
	}
	return b
}

// gitRowText is a row's plain text for surfaceText.
func gitRowText(r *gitRow) string {
	if r.hash != "" {
		line := r.hash
		if len(r.refs) > 0 {
			line += " (" + strings.Join(r.refs, ", ") + ")"
		}
		line += " " + r.subject
		if r.when != "" {
			line += " · " + r.when
		}
		return line
	}
	return r.mark + " " + r.path
}

// paintGitRow paints one activatable row with full-row square-fill hover and
// focus feedback; the focus mark takes the blank cell before the row.
func (m *Model) paintGitRow(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	v := m.componentStyle(squareFill, m.controlState(false, r.key), p.text, p.panel)
	f.styledButton(x, y, width, "", r.key, r.action, v)
	f.hits[len(f.hits)-1].Label = r.help
	bg := v.background
	if width < 4 {
		return
	}
	cx, end := x+1, x+width-1
	if r.hash == "" {
		f.text(cx, y, 1, r.mark, r.markInk, bg)
		room := end - (cx + 2)
		f.componentText(cx+2, y, max(0, room), truncatePathLeft(r.path, room), v)
		return
	}
	// Commit: hash, subject, refs, then the age right-aligned when it fits.
	hw := min(ansi.StringWidth(r.hash), end-cx)
	f.text(cx, y, hw, r.hash, p.muted, bg)
	cx += hw + 1
	if r.when != "" && end-cx >= 24 {
		ww := ansi.StringWidth(r.when)
		f.text(end-ww, y, ww, r.when, p.muted, bg)
		end -= ww + 2
	}
	room := end - cx
	if room <= 0 {
		return
	}
	// Refs take what the subject leaves, keeping at least half the room (or
	// its whole width, if shorter) legible for the subject.
	budget := room - min(ansi.StringWidth(r.subject), max(16, room/2))
	var refs []string
	used := 0
	for i, ref := range r.refs {
		label := ref
		w := ansi.StringWidth(label) + 1
		if used+w > budget {
			refs = append(refs, "+"+strconv.Itoa(len(r.refs)-i))
			break
		}
		refs = append(refs, label)
		used += w
	}
	subjectRoom := room - refsWidth(refs)
	sw := min(ansi.StringWidth(r.subject), max(0, subjectRoom))
	f.componentText(cx, y, sw, r.subject, v)
	cx += sw + 1
	for _, ref := range refs {
		w := ansi.StringWidth(ref)
		if cx+w > end {
			break
		}
		ink := p.green
		switch {
		case ref == "HEAD":
			ink = p.blue
		case strings.HasPrefix(ref, "+"):
			ink = p.muted
		}
		f.componentText(cx, y, w, ref, componentVisual{foreground: ink, background: bg, bold: ref == "HEAD"})
		cx += w + 1
	}
}

func refsWidth(refs []string) int {
	w := 0
	for _, r := range refs {
		w += ansi.StringWidth(r) + 1
	}
	return w
}

// moveGitFocus moves keyboard focus between the Git surface's activatable
// rows and keeps the focused row inside the scrolled body.
func (m *Model) moveGitFocus(delta int) tea.Cmd {
	f := m.measure()
	v := m.viewState()
	active, ok := v.Host.Active()
	if !ok || active.Kind != "git" || f.detail.H == 0 {
		return nil
	}
	rows := m.surfaceRows(m.surfaceBlocks(active), max(1, f.detail.W-1))
	var index []int
	current := -1
	for i, r := range rows {
		if r.git != nil {
			if r.git.key == m.focus {
				current = len(index)
			}
			index = append(index, i)
		}
	}
	if len(index) == 0 {
		return nil
	}
	next := current + delta
	if current < 0 {
		next = 0
	}
	next = min(len(index)-1, max(0, next))
	row := index[next]
	scroll := min(max(0, v.DetailScroll), f.detailMax)
	if row < scroll {
		scroll = row
	} else if row >= scroll+f.detail.H {
		scroll = row - f.detail.H + 1
	}
	v.DetailScroll = scroll
	m.markDirty()
	return m.setFocus(rows[row].git.key)
}

// gitFocusKey reports a Git row focus key.
func gitFocusKey(key string) bool { return strings.HasPrefix(key, "git:") }

// Used by tests and surfaceText to find the displayed view.
func (m *Model) currentGitView() *gitView {
	key, _ := m.gitTarget()
	return m.gitViews[key]
}
