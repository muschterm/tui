package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// Explicit managed worktrees (ADR 0024): the new-thread draft's Workspace
// row, recovery actions for an existing worktree thread and the project
// General Worktrees section. Previews and the draft's start observation are
// asynchronous reads; rendering never performs them.

const (
	workspaceCheckout = "checkout"
	workspaceWorktree = "worktree"
)

// worktreeAPI is the subset of the server client the worktree flows use;
// tests inject a fake.
type worktreeAPI interface {
	WorktreeRemoval(ctx context.Context, worktreeID string) (protocol.WorktreeRemoval, error)
	WorktreePrune(ctx context.Context, projectID string) (protocol.WorktreePrune, error)
}

var _ worktreeAPI = (*client.Client)(nil)

func (m *Model) worktreeClient() worktreeAPI {
	if m.worktreeReads != nil {
		return m.worktreeReads
	}
	if m.client != nil {
		return m.client
	}
	return nil
}

// draftStart is the project checkout's HEAD as last observed for a
// worktree draft: the start commit Send captures.
type draftStart struct {
	key, branch, oid, state, err string
	loading                      bool
	seq                          uint64
}

type draftStartMsg struct {
	key    string
	seq    uint64
	status protocol.GitStatus
	err    error
}

type worktreeRemovalMsg struct {
	seq     uint64
	preview protocol.WorktreeRemoval
	err     error
}

type worktreePruneMsg struct {
	seq       uint64
	projectID string
	preview   protocol.WorktreePrune
	err       error
	quiet     bool
}

// worktreeRetryMsg re-sends a worktree thread.start whose receipt was
// still running, under the same command identity.
type worktreeRetryMsg struct{ id string }

func (m *Model) workCtx() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// ---- Draft Workspace row ----

func (m *Model) draftWorkspaceOffered() bool {
	return m.creatingThread() && m.hasCapability("worktree-create")
}

// draftWorkspace is the draft's workspace choice: the explicit choice kept
// in the client-local draft, else the project's effective default.
func (m *Model) draftWorkspace() string {
	if !m.creatingThread() {
		return ""
	}
	v := m.viewState()
	if v.PendingStart != nil {
		return workspaceWorktree
	}
	if m.hasCapability("worktree-create") && v.Workspace != "" {
		return v.Workspace
	}
	p, _ := m.projectByID(m.state.DraftProjectID)
	if protocol.EffectiveWorkspaceDefault(m.snapshot.AppSettings, p) == workspaceWorktree {
		return workspaceWorktree
	}
	return workspaceCheckout
}

// checkoutRows is the height of the checkout context area below the
// composer: a worktree draft adds its Branch row.
func (m *Model) checkoutRows() int {
	if m.draftWorkspaceOffered() && (m.draftWorkspace() == workspaceWorktree || m.viewState().PendingStart != nil) {
		return 2
	}
	return 1
}

// branchNameProblem is a basic client-side check of a new branch name,
// following git-check-ref-format for a branch; the server stays
// authoritative.
func branchNameProblem(name string) string {
	switch {
	case name == "":
		return "enter a branch name"
	case name == "@" || name == "HEAD" || strings.HasPrefix(name, "refs/"):
		return "choose another name (not HEAD or refs/…)"
	case len(name) > 200:
		return "at most 200 bytes"
	case strings.HasPrefix(name, "-"):
		return "cannot start with -"
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//"):
		return "misplaced /"
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock"):
		return "cannot end with . or .lock"
	case strings.Contains(name, "..") || strings.Contains(name, "@{"):
		return "cannot contain .. or @{"
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return "no spaces or ~ ^ : ? * [ \\"
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return "a part cannot start with . or end with .lock"
		}
	}
	return ""
}

func (m *Model) draftWorkspaceBlocked() string {
	if !m.creatingThread() || m.draftWorkspace() != workspaceWorktree {
		return ""
	}
	if !m.hasCapability("worktree-create") {
		return "This project's workspace default is a new worktree, which this server cannot create. Choose Current checkout in project settings, or update the server."
	}
	branch := strings.TrimSpace(m.viewState().WorktreeBranch)
	if branch == "" {
		return "Name the new worktree's branch in the Workspace row below the prompt"
	}
	if problem := branchNameProblem(branch); problem != "" {
		return "Branch name: " + problem
	}
	if m.viewState().branchUsed(branch) {
		return "Branch " + branch + " may already exist from an earlier start; choose another name"
	}
	s := m.draftStart
	if s.key != m.draftStartKey() || s.loading {
		return "Reading the start commit; Send again in a moment"
	}
	if s.oid == "" {
		reason := "the project checkout has no commit"
		if s.err != "" {
			reason = s.err
		}
		return "Start commit unavailable: " + reason + ". Refresh it from the Workspace row."
	}
	return ""
}

// draftWorkspaceRequest is the explicit workspace a first Send captures, or
// nil when the server offers no choice. The checkout choice is explicit too,
// so a Worktree default never applies to a draft that chose Checkout.
func (m *Model) draftWorkspaceRequest() *protocol.WorkspaceRequest {
	if !m.draftWorkspaceOffered() {
		return nil
	}
	if m.draftWorkspace() == workspaceWorktree {
		return &protocol.WorkspaceRequest{Mode: workspaceWorktree, StartOid: m.draftStart.oid, Branch: strings.TrimSpace(m.viewState().WorktreeBranch)}
	}
	return &protocol.WorkspaceRequest{Mode: workspaceCheckout}
}

func (m *Model) draftStartKey() string {
	if !m.draftWorkspaceOffered() {
		return ""
	}
	// The selection generation makes every entry into the draft read again.
	return fmt.Sprintf("project:%s:%d", m.state.DraftProjectID, m.draftGen)
}

// nextDraftStartInspection reads the project checkout's HEAD once per
// draft project while New worktree is chosen; explicit refresh reads again.
func (m *Model) nextDraftStartInspection() tea.Cmd {
	key := m.draftStartKey()
	if key == "" || m.draftWorkspace() != workspaceWorktree || key == m.draftStart.key {
		return nil
	}
	api := m.gitClient()
	if api == nil || !m.connected {
		return nil
	}
	m.draftStartSeq++
	seq := m.draftStartSeq
	m.draftStart = draftStart{key: key, loading: true, seq: seq}
	ctx, projectID := m.workCtx(), m.state.DraftProjectID
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := api.GitStatus(deadline, client.GitTarget{ProjectID: projectID})
		return draftStartMsg{key: key, seq: seq, status: s, err: err}
	}
}

func (m *Model) acceptDraftStart(msg draftStartMsg) {
	if msg.key != m.draftStart.key || msg.seq != m.draftStart.seq {
		return
	}
	s := draftStart{key: msg.key, seq: msg.seq}
	if msg.err != nil {
		s.err = safe(singleLine(msg.err.Error()))
	} else {
		st := msg.status
		s.state, s.oid, s.branch = st.Workspace.State, st.HeadOid, st.Branch
		if s.branch == "" {
			s.branch = st.Workspace.Branch
		}
		switch st.Workspace.State {
		case "non-git":
			s.oid, s.err = "", "the project is not a Git checkout"
		case "unavailable", "":
			s.oid, s.err = "", "the checkout could not be read"
			if st.Workspace.Error != "" {
				s.err = safe(singleLine(st.Workspace.Error))
			}
		}
	}
	m.draftStart = s
	m.markDirty()
}

func shortOid(oid string) string { return oid[:min(7, len(oid))] }

// draftStartLabel is the read-only Start value: the observed branch and
// short commit the worktree will start from.
// room bounds the label: the branch is truncated first so the short commit
// stays visible.
func (m *Model) draftStartLabel(room int) (string, string) {
	s := m.draftStart
	switch {
	case s.key != m.draftStartKey() || s.loading:
		return "Start · reading…", "pending"
	case s.oid == "":
		return "Start unavailable", "unavailable"
	}
	branch := singleLine(s.branch)
	if branch == "" {
		branch = "detached"
	}
	tail := " @ " + shortOid(s.oid)
	prefix := "Start · "
	if room < ansi.StringWidth(prefix+branch+tail) {
		prefix = ""
	}
	branch = ansi.Truncate(branch, max(1, room-ansi.StringWidth(prefix+tail)), "…")
	return prefix + branch + tail, "branch"
}

func (m *Model) renderDraftWorkspace(f *frame, r shell.Rect) {
	p := m.colors()
	x, width := r.X+composerInset(r.W), max(1, r.W-2*composerInset(r.W))
	mode := m.draftWorkspace()
	checkout, worktree := "Checkout", "New worktree"
	if width < 44 {
		worktree = "Worktree"
	}
	cw, ww := ansi.StringWidth(checkout)+2, ansi.StringWidth(worktree)+2
	f.compactButton(m, x, r.Y, cw, checkout, "workspace-checkout", action{Kind: "worktree-draft-mode", Value: workspaceCheckout}, mode == workspaceCheckout, normalControl)
	f.hits[len(f.hits)-1].Label = "Start the thread in the project checkout"
	f.compactButton(m, x+cw, r.Y, ww, worktree, "workspace-worktree", action{Kind: "worktree-draft-mode", Value: workspaceWorktree}, mode == workspaceWorktree, normalControl)
	f.hits[len(f.hits)-1].Label = "Start the thread on a new branch in a new worktree"
	used := cw + ww + 1
	room := width - used
	if room < 4 {
		return
	}
	var value, state, key, label string
	valueInk := p.text
	if mode == workspaceWorktree {
		value, state = m.draftStartLabel(max(1, room-3))
		key, label = "worktree-start", "Observed project HEAD · open to refresh"
	} else {
		_, value = m.checkoutLabels()
		state = m.displayedCheckout().State
		key, label = "checkout-branch", value+" · Observed on selection; open to refresh"
	}
	glyph, glyphInk := m.icon("git"), p.muted
	if state != "branch" && state != "unborn" && state != "detached" {
		glyph, glyphInk = panelStatusMark(m, state)
		valueInk = p.muted
	}
	markWidth := ansi.StringWidth(glyph) + 1
	valueWidth := min(ansi.StringWidth(value), max(1, room-markWidth))
	rightX := x + width - valueWidth
	if rightX-markWidth >= x+used {
		f.text(rightX-markWidth, r.Y, markWidth, glyph+" ", glyphInk, p.canvas)
	}
	f.button(m, rightX, r.Y, valueWidth, value, key, action{Kind: "checkout-info"}, valueInk, p.canvas)
	f.hits[len(f.hits)-1].Label = label
	if mode != workspaceWorktree {
		return
	}
	// The Branch row: a label and the field that opens the name dialog, or
	// the pending start's progress and its actions.
	y := r.Y + 1
	if ps := m.viewState().PendingStart; ps != nil {
		m.renderPendingStart(f, x, y, width, ps)
		return
	}
	const branchLabel = "Branch "
	f.text(x, y, min(width, len(branchLabel)), branchLabel, p.muted, p.canvas)
	fieldX, fieldW := x+len(branchLabel), max(1, width-len(branchLabel))
	branch := strings.TrimSpace(m.viewState().WorktreeBranch)
	text, ink := singleLine(branch), p.text
	if branch == "" {
		text, ink = "Name the new branch…", p.muted
	} else if problem := branchNameProblem(branch); problem != "" {
		text, ink = text+" · "+problem, p.red
	} else if m.viewState().branchUsed(branch) {
		text, ink = text+" · may already exist; choose another", p.red
	}
	f.button(m, fieldX, y, fieldW, text, "worktree-branch", action{Kind: "worktree-branch"}, ink, p.canvas)
	f.hits[len(f.hits)-1].Label = "New branch for the worktree (required)"
}

// ---- Existing worktree threads ----

func (m *Model) worktreeByID(id string) (protocol.ManagedWorktree, bool) {
	for _, w := range m.snapshot.Worktrees {
		if w.ID == id {
			return w, true
		}
	}
	return protocol.ManagedWorktree{}, false
}

// threadWorktree is the managed worktree of the selected existing thread.
// ok reports a worktree thread; found whether its record is still listed.
func (m *Model) threadWorktree() (w protocol.ManagedWorktree, ok, found bool) {
	if m.creatingThread() || m.thread().WorktreeID == "" {
		return w, false, false
	}
	w, found = m.worktreeByID(m.thread().WorktreeID)
	return w, true, found
}

func worktreeStateLabel(w protocol.ManagedWorktree) string {
	label := map[string]string{
		protocol.WorktreeCreating:     "Creating",
		protocol.WorktreePresent:      "Present",
		protocol.WorktreeMissing:      "Missing",
		protocol.WorktreeMoved:        "Moved",
		protocol.WorktreeUnregistered: "Unregistered",
		protocol.WorktreeUnattached:   "Unattached",
		protocol.WorktreeRemoved:      "Removed",
	}[w.State]
	if label == "" {
		label = "Unknown state"
	}
	if w.Unverified {
		label += " · unverified"
	}
	return label
}

// worktreeMarkState maps a worktree state onto the panel status vocabulary.
func worktreeMarkState(state string) string {
	switch state {
	case protocol.WorktreeCreating:
		return "pending"
	case protocol.WorktreeMissing, protocol.WorktreeMoved, protocol.WorktreeUnregistered:
		return "blocked"
	case protocol.WorktreeUnattached, protocol.WorktreeRemoved:
		return ""
	}
	return "unavailable"
}

// worktreeActions reports the recovery and cleanup commands ADR 0024 allows
// in a state.
func worktreeActions(w protocol.ManagedWorktree) (relocate, forget, remove bool) {
	switch w.State {
	case protocol.WorktreeMoved:
		return true, true, false
	case protocol.WorktreeMissing, protocol.WorktreeUnregistered, protocol.WorktreeRemoved:
		return false, true, false
	case protocol.WorktreePresent, protocol.WorktreeUnattached:
		return false, false, true
	}
	return false, false, false
}

func (m *Model) worktreeThreadTitle(id string) string {
	for _, t := range m.snapshot.Threads {
		if t.WorktreeID == id {
			return singleLine(t.Title)
		}
	}
	return ""
}

// worktreeMenuItems lists a worktree's facts and applicable actions.
func (m *Model) worktreeMenuItems(w protocol.ManagedWorktree, withState bool) []menuItem {
	var items []menuItem
	if withState {
		items = append(items, menuItem{Note: "State: " + worktreeStateLabel(w)})
	}
	items = append(items, menuItem{Note: truncatePathLeft(w.Path, 60)})
	if w.State == protocol.WorktreeMoved && w.MovedTo != "" {
		items = append(items, menuItem{Note: "Git registers it at " + truncatePathLeft(w.MovedTo, 40)})
	}
	if w.StartOid != "" {
		items = append(items, menuItem{Note: "Started at " + shortOid(w.StartOid)})
	}
	if w.Detail != "" {
		items = append(items, menuItem{Note: singleLine(w.Detail)})
	}
	if w.Unverified {
		items = append(items, menuItem{Note: "Git's result could not be verified; inspect, then remove or forget it"})
	}
	if !m.hasCapability("worktree-manage") {
		return append(items, menuItem{Note: "Update this server to manage worktrees"})
	}
	relocate, forget, remove := worktreeActions(w)
	if relocate {
		items = append(items, menuItem{Label: "Relocate to where Git registers it", Action: action{Kind: "worktree-relocate", ID: w.ID}})
	}
	if forget {
		items = append(items, menuItem{Label: "Forget worktree record…", Action: action{Kind: "worktree-forget", ID: w.ID}})
	}
	if remove {
		items = append(items, menuItem{Label: "Remove worktree…", Action: action{Kind: "worktree-remove", ID: w.ID}})
	}
	return items
}

// ---- Actions ----

func (m *Model) worktreeAction(a action) tea.Cmd {
	switch a.Kind {
	case "worktree-draft-mode":
		if !m.draftWorkspaceOffered() {
			return m.showNoticeAs(noticeUnavailable, "Update this server to choose a worktree")
		}
		if a.Value == m.draftWorkspace() {
			return nil // Re-selecting the current choice changes nothing.
		}
		if m.confirmReleaseSettledStart(a) {
			return nil
		}
		if m.configurationLocked() {
			return m.showNotice("The workspace is fixed while this Send is pending")
		}
		m.viewState().Workspace = a.Value
		m.markDirty()
		m.configureInputs()
	case "worktree-branch":
		if !m.draftWorkspaceOffered() {
			return nil
		}
		if m.confirmReleaseSettledStart(a) {
			return nil
		}
		if m.configurationLocked() {
			return m.showNotice("The branch is fixed while this Send is pending")
		}
		m.openProjectDialog("worktree-branch")
		m.projectInput.SetValue(m.viewState().WorktreeBranch)
		m.projectInput.CursorEnd()
	case "worktree-branch-submit":
		value := strings.TrimSpace(m.projectInput.Value())
		if problem := branchNameProblem(value); problem != "" {
			m.projectError = "Branch name: " + problem
			return nil
		}
		if m.creatingThread() {
			m.viewState().WorktreeBranch = value
		}
		m.menu, m.projectMode = nil, ""
		m.projectInput.Blur()
		m.markDirty()
		m.configureInputs()
		return m.setFocus("worktree-branch")
	case "worktree-start-retry":
		return m.retryPendingStart()
	case "worktree-start-discard":
		return m.discardPendingStart()
	case "worktree-start-release-confirm":
		if v, ps := m.pendingStartByID(a.ID); ps != nil {
			m.dropPendingStart(v)
		}
		next := action{Kind: "worktree-draft-mode", Value: a.Value}
		if a.Value == "" {
			next = action{Kind: "worktree-branch"}
		}
		return m.worktreeAction(next)
	case "worktree-start-stop":
		return m.confirmStopFollowing()
	case "worktree-start-stop-confirm":
		if v, ps := m.pendingStartByID(a.ID); ps != nil {
			m.dropPendingStart(v)
			return m.showNotice("Stopped following that start; branch " + singleLine(ps.Command.Workspace.Branch) + " is treated as used")
		}
		return nil
	case "worktree-start-inspect":
		m.openSidebarSettings("general", m.state.DraftProjectID)
		return nil
	case "worktree-menu":
		w, ok := m.worktreeByID(a.ID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "That worktree is no longer listed")
		}
		m.showMenuFor("Worktree · ", singleLine(w.Branch), append(m.worktreeMenuItems(w, true), menuItem{Label: "Close", Action: action{Kind: "noop"}}))
	case "worktree-relocate":
		w, ok := m.worktreeByID(a.ID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "That worktree is no longer listed")
		}
		items := []menuItem{
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "Relocate worktree record", Action: action{Kind: "worktree-relocate-confirm", ID: w.ID}},
			{Note: "Follows Git to " + truncatePathLeft(w.MovedTo, 48)},
			{Note: "The thread's agent session restarts in the new location"},
		}
		m.showMenuFor("Relocate worktree · ", singleLine(w.Branch), items)
	case "worktree-relocate-confirm":
		if !m.hasCapability("worktree-manage") {
			return m.settingsUnavailable("worktree-manage")
		}
		return m.command(protocol.Command{Kind: "worktree.relocate", Worktree: &protocol.WorktreeAction{ID: a.ID}}, a)
	case "worktree-forget":
		w, ok := m.worktreeByID(a.ID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "That worktree is no longer listed")
		}
		items := []menuItem{
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "Forget this worktree record", Action: action{Kind: "worktree-forget-confirm", ID: w.ID}},
			{Note: "Nothing on disk changes; branch " + singleLine(w.Branch) + " is kept"},
		}
		if title := m.worktreeThreadTitle(w.ID); title != "" {
			items = append(items, menuItem{Note: "Thread " + title + " keeps its history but cannot send"})
		}
		m.showMenuFor("Forget worktree · ", singleLine(w.Branch), items)
	case "worktree-forget-confirm":
		if !m.hasCapability("worktree-manage") {
			return m.settingsUnavailable("worktree-manage")
		}
		return m.command(protocol.Command{Kind: "worktree.forget", Worktree: &protocol.WorktreeAction{ID: a.ID}}, a)
	case "worktree-remove":
		return m.previewWorktreeRemoval(a.ID)
	case "worktree-remove-confirm":
		if !m.hasCapability("worktree-manage") {
			return m.settingsUnavailable("worktree-manage")
		}
		return m.command(protocol.Command{Kind: "worktree.remove", Worktree: &protocol.WorktreeAction{ID: a.ID, Confirm: a.Value}}, a)
	case "worktree-prune":
		return m.previewWorktreePrune(a.ID, false)
	case "worktree-prune-confirm":
		if !m.hasCapability("worktree-manage") {
			return m.settingsUnavailable("worktree-manage")
		}
		return m.command(protocol.Command{Kind: "worktree.prune", ProjectID: a.ID, Worktree: &protocol.WorktreeAction{Confirm: a.Value}}, a)
	}
	return nil
}

func (m *Model) previewWorktreeRemoval(id string) tea.Cmd {
	if !m.hasCapability("worktree-manage") {
		return m.settingsUnavailable("worktree-manage")
	}
	api := m.worktreeClient()
	if api == nil {
		return m.showNoticeAs(noticeUnavailable, "Connect to the server to remove a worktree")
	}
	m.worktreeSeq++
	seq, ctx := m.worktreeSeq, m.workCtx()
	m.status = "Checking what removal would delete…"
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		p, err := api.WorktreeRemoval(deadline, id)
		return worktreeRemovalMsg{seq: seq, preview: p, err: err}
	}
}

func (m *Model) showWorktreeRemoval(p protocol.WorktreeRemoval) {
	branch := singleLine(p.Branch)
	items := []menuItem{{Label: "Cancel", Action: action{Kind: "noop"}}}
	if len(p.Blockers) > 0 {
		items = append(items, menuItem{Note: "Cannot remove now:"})
		for _, b := range p.Blockers {
			items = append(items, menuItem{Note: "· " + singleLine(b)})
		}
	} else {
		items = append(items, menuItem{Label: "Remove worktree directory", Action: action{Kind: "worktree-remove-confirm", ID: p.ID, Value: p.Fingerprint}})
	}
	items = append(items, menuItem{Note: "Branch " + branch + " is kept"})
	switch {
	case p.Ignored > 0:
		items = append(items, menuItem{Note: fmt.Sprintf("Also deletes %d ignored %s:", p.Ignored, pluralWord(p.Ignored, "entry", "entries"))})
		for _, s := range p.IgnoredSample {
			items = append(items, menuItem{Note: "  " + truncatePathLeft(s, 56)})
		}
		if more := p.Ignored - len(p.IgnoredSample); more > 0 {
			items = append(items, menuItem{Note: fmt.Sprintf("  …and %d more", more)})
		}
	case !p.IgnoredIncomplete:
		items = append(items, menuItem{Note: "No ignored files are deleted"})
	}
	if p.IgnoredIncomplete {
		items = append(items, menuItem{Note: "Ignored-file count is incomplete; more may be deleted"})
	}
	items = append(items, menuItem{Note: truncatePathLeft(p.Path, 60)})
	m.showMenuFor("Remove worktree · ", branch, items)
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// previewWorktreePrune reads the prune preview. quiet reads only refresh
// the settings section's Prune control; explicit ones open the confirmation.
func (m *Model) previewWorktreePrune(projectID string, quiet bool) tea.Cmd {
	if !m.hasCapability("worktree-manage") {
		if quiet {
			return nil
		}
		return m.settingsUnavailable("worktree-manage")
	}
	api := m.worktreeClient()
	if api == nil {
		if quiet {
			return nil
		}
		return m.showNoticeAs(noticeUnavailable, "Connect to the server to prune worktrees")
	}
	// Quiet reads never advance the explicit sequence, so they cannot drop
	// an explicit preview the user is waiting for.
	var seq uint64
	if !quiet {
		m.worktreeSeq++
		seq = m.worktreeSeq
	}
	ctx := m.workCtx()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		p, err := api.WorktreePrune(deadline, projectID)
		return worktreePruneMsg{seq: seq, projectID: projectID, preview: p, err: err, quiet: quiet}
	}
}

func (m *Model) showWorktreePrune(projectID string, p protocol.WorktreePrune) {
	name := projectID
	if project, ok := m.projectByID(projectID); ok {
		name = project.Name
	}
	items := []menuItem{{Label: "Cancel", Action: action{Kind: "noop"}}}
	if len(p.Entries) == 0 {
		items = append(items, menuItem{Note: "Git lists no stale worktree registrations"})
	} else {
		items = append(items, menuItem{Label: fmt.Sprintf("Prune %d stale %s", len(p.Entries), pluralWord(len(p.Entries), "registration", "registrations")), Action: action{Kind: "worktree-prune-confirm", ID: projectID, Value: p.Fingerprint}})
		for _, e := range p.Entries {
			items = append(items, menuItem{Note: "· " + truncatePathLeft(e, 58)})
		}
		items = append(items, menuItem{Note: "Only Git's records are removed; no files or branches"})
	}
	m.showMenuFor("Prune worktrees · ", singleLine(name), items)
}

// nextPruneInspection keeps the project General Worktrees section's Prune
// control current: one quiet preview per opened project settings view.
func (m *Model) nextPruneInspection() tea.Cmd {
	if m.settingsPage != "general" || m.settingsProjectID == "" || m.pruneKey == m.settingsProjectID {
		return nil
	}
	m.pruneKey = m.settingsProjectID
	return m.previewWorktreePrune(m.settingsProjectID, true)
}

// acceptWorktreeMsg handles the worktree flows' asynchronous results.
func (m *Model) acceptWorktreeMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case draftStartMsg:
		m.acceptDraftStart(msg)
	case worktreeRemovalMsg:
		if msg.seq != m.worktreeSeq {
			return nil, true
		}
		m.status = ""
		if msg.err != nil {
			return m.showNoticeAs(noticeError, "Cannot preview removal: "+safe(singleLine(msg.err.Error()))), true
		}
		m.showWorktreeRemoval(msg.preview)
	case worktreePruneMsg:
		if msg.err == nil {
			if m.prunePreview == nil {
				m.prunePreview = map[string]protocol.WorktreePrune{}
			}
			m.prunePreview[msg.projectID] = msg.preview
		}
		if msg.quiet || msg.seq != m.worktreeSeq {
			return nil, true
		}
		if msg.err != nil {
			return m.showNoticeAs(noticeError, "Cannot list stale worktrees: "+safe(singleLine(msg.err.Error()))), true
		}
		m.showWorktreePrune(msg.projectID, msg.preview)
	case worktreeRetryMsg:
		if _, ps := m.pendingStartByID(msg.id); ps != nil && ps.State == pendingRunning {
			return m.dispatchStart(ps.Command), true
		}
		return nil, true
	case worktreeStartMsg:
		delete(m.startInFlight, msg.command.ID)
		if _, ps := m.pendingStartByID(msg.command.ID); ps == nil {
			return nil, true // Settled from the snapshot or discarded meanwhile.
		}
		return m.acceptWorktreeStart(msg.command, msg.receipt, msg.err), true
	default:
		return nil, false
	}
	m.markDirty()
	return nil, true
}

// worktreeCommandFailed reports a rejected worktree.* command. A stale
// confirmation previews again so the user reviews what changed.
func (m *Model) worktreeCommandFailed(c protocol.Command, err error) tea.Cmd {
	var e *protocol.Error
	if errors.As(err, &e) && e.Code == "stale_confirmation" && c.Worktree != nil {
		m.status = "The worktree changed since the preview; review it again"
		notice := m.showNoticeAs(noticeUnavailable, m.status)
		if c.Kind == "worktree.remove" {
			return tea.Batch(notice, m.previewWorktreeRemoval(c.Worktree.ID))
		}
		if c.Kind == "worktree.prune" {
			return tea.Batch(notice, m.previewWorktreePrune(c.ProjectID, false))
		}
	}
	return m.showNoticeAs(noticeError, "Worktree: "+m.status)
}

func worktreeDoneText(kind string) string {
	switch kind {
	case "worktree.remove":
		return "Worktree removed · branch kept"
	case "worktree.forget":
		return "Worktree record forgotten · files kept"
	case "worktree.relocate":
		return "Worktree relocated"
	case "worktree.prune":
		return "Stale worktree registrations pruned"
	}
	return "Accepted"
}

// ---- Following a worktree start ----

// Pending start states.
const (
	pendingRunning   = "running"   // Git may still be running; re-asked with backoff.
	pendingUnknown   = "unknown"   // The server lost track (outcome_unknown).
	pendingRetryable = "retryable" // Created but not attached; Retry attaches.
	pendingFailed    = "failed"    // Failed; kept until the user discards it.
)

// pendingStart is a worktree thread.start still followed under its command
// identity. It is kept with the per-project draft, so a restarted TUI
// resumes following it.
type pendingStart struct {
	Command protocol.Command
	State   string
	Detail  string `json:",omitempty"`
	// ThreadID is the thread the server named for this start, when known.
	ThreadID string `json:",omitempty"`
}

func (v *threadView) branchUsed(b string) bool { return b != "" && slices.Contains(v.UsedBranches, b) }

// markBranchUsed remembers a branch an unsettled start may have created;
// the set stays small.
func (v *threadView) markBranchUsed(b string) {
	if b == "" || v.branchUsed(b) {
		return
	}
	v.UsedBranches = append(v.UsedBranches, b)
	if len(v.UsedBranches) > 8 {
		v.UsedBranches = v.UsedBranches[len(v.UsedBranches)-8:]
	}
}

// dropPendingStart stops following the draft's start: the branch counts as
// used, the draft unlocks and keeps its prompt and branch.
func (m *Model) dropPendingStart(v *threadView) {
	if v.PendingStart == nil {
		return
	}
	v.markBranchUsed(v.PendingStart.Command.Workspace.Branch)
	delete(m.startAttempts, v.PendingStart.Command.ID)
	v.PendingStart = nil
	m.markDirty()
	m.configureInputs()
}

type worktreeStartMsg struct {
	command protocol.Command
	receipt protocol.Receipt
	err     error
}

func isWorktreeStart(c protocol.Command) bool {
	return c.Kind == "thread.start" && c.Workspace != nil && c.Workspace.Mode == workspaceWorktree
}

func (m *Model) pendingStartByID(id string) (*threadView, *pendingStart) {
	for _, v := range m.state.DraftThreads {
		if v != nil && v.PendingStart != nil && v.PendingStart.Command.ID == id {
			return v, v.PendingStart
		}
	}
	return nil, nil
}

func (m *Model) worktreeForCommand(id string) (protocol.ManagedWorktree, bool) {
	for _, w := range m.snapshot.Worktrees {
		if w.CommandID == id {
			return w, true
		}
	}
	return protocol.ManagedWorktree{}, false
}

func (m *Model) scheduleStartRetry(id string) tea.Cmd {
	if m.startAttempts == nil {
		m.startAttempts = map[string]int{}
	}
	n := m.startAttempts[id]
	m.startAttempts[id] = n + 1
	delay := min(5*time.Second, time.Second<<min(n, 3))
	return tea.Tick(delay, func(time.Time) tea.Msg { return worktreeRetryMsg{id: id} })
}

// dispatchStart re-asks a pending start under its own identity, outside
// the global pending command.
func (m *Model) dispatchStart(c protocol.Command) tea.Cmd {
	if m.startInFlight[c.ID] {
		return nil
	}
	if m.client == nil {
		// Not connected yet: keep the loop alive until a client exists.
		return m.scheduleStartRetry(c.ID)
	}
	if m.startInFlight == nil {
		m.startInFlight = map[string]bool{}
	}
	m.startInFlight[c.ID] = true
	connection, ctx := m.client, m.workCtx()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		r, err := connection.Command(deadline, c)
		return worktreeStartMsg{command: c, receipt: r, err: err}
	}
}

// resumePendingStarts follows running starts saved before a relaunch.
func (m *Model) resumePendingStarts() tea.Cmd {
	var cmds []tea.Cmd
	for _, v := range m.state.DraftThreads {
		if v != nil && v.PendingStart != nil && v.PendingStart.State == pendingRunning {
			cmds = append(cmds, m.scheduleStartRetry(v.PendingStart.Command.ID))
		}
	}
	return tea.Batch(cmds...)
}

// acceptWorktreeStart settles one answer about a worktree start: the first
// dispatch's or a re-ask's.
func (m *Model) acceptWorktreeStart(c protocol.Command, r protocol.Receipt, err error) tea.Cmd {
	v := m.state.DraftThreads[c.ProjectID]
	if v == nil {
		v = &threadView{Agent: c.Agent}
		if c.Settings != nil {
			v.Settings = *c.Settings
		}
		m.state.DraftThreads[c.ProjectID] = v
	}
	defer func() {
		m.markDirty()
		m.configureInputs()
	}()
	visible := m.state.DraftProjectID == c.ProjectID
	fail := func(text string) tea.Cmd {
		v.PendingStart = nil
		delete(m.startAttempts, c.ID)
		m.status = text
		if visible {
			return m.showSendError(text)
		}
		return m.showNoticeAs(noticeError, text)
	}
	var perr *protocol.Error
	switch {
	case err != nil && errors.As(err, &perr):
		if perr.Code == "branch_exists" {
			v.markBranchUsed(c.Workspace.Branch)
		}
		return fail("Thread not started: " + safe(singleLine(err.Error())))
	case err != nil || r.State == pendingRunning || r.State == "":
		// A transport failure is uncertain too: keep following the same ID.
		thread := r.TargetID
		if v.PendingStart != nil && thread == "" {
			thread = v.PendingStart.ThreadID
		}
		v.PendingStart = &pendingStart{Command: c, State: pendingRunning, ThreadID: thread}
		m.status = "Creating the worktree…"
		return m.scheduleStartRetry(c.ID)
	case r.State == "outcome_unknown":
		v.PendingStart = &pendingStart{Command: c, State: pendingUnknown, Detail: "The server restarted while creating the worktree; its outcome is unknown"}
		m.status = v.PendingStart.Detail + ". Inspect it in Project settings › General › Worktrees."
		return m.showNoticeAs(noticeUnavailable, m.status)
	case r.State == "failed":
		message := "the worktree was not created"
		if r.Error != nil {
			message = safe(singleLine(r.Error.Message))
		}
		// The identity is kept: the snapshot naming an unattached record
		// may arrive after this receipt, and then Retry attaches it.
		delete(m.startAttempts, c.ID)
		state := pendingFailed
		if m.startRetryable(c.ID) {
			state = pendingRetryable
		}
		v.PendingStart = &pendingStart{Command: c, State: state, Detail: message}
		m.status = "Thread not started: " + message
		if visible {
			return m.showSendError(m.pendingStartBlocked())
		}
		return m.showNoticeAs(noticeError, m.status)
	}
	return m.startAccepted(c, r.TargetID, r.Revision)
}

func (m *Model) startAccepted(c protocol.Command, threadID string, revision int64) tea.Cmd {
	v := m.state.DraftThreads[c.ProjectID]
	if m.state.StartedDraft != nil && m.state.StartedDraft.Command.ID != c.ID {
		// Another creation is still being settled; follow this one again later.
		if v != nil {
			v.PendingStart = &pendingStart{Command: c, State: pendingRunning}
		}
		return m.scheduleStartRetry(c.ID)
	}
	if v != nil {
		v.PendingStart, v.ContextError = nil, ""
		if v.WorktreeBranch == c.Workspace.Branch {
			v.WorktreeBranch = ""
		}
		// The name is now the thread's own branch; forget it as a risk.
		v.UsedBranches = slices.DeleteFunc(v.UsedBranches, func(b string) bool { return b == c.Workspace.Branch })
	}
	delete(m.startAttempts, c.ID)
	m.state.StartedDraft = &startedDraft{ThreadID: threadID, Command: c, Revision: revision}
	m.reconcileThreadMembership()
	m.status = "Initial prompt accepted"
	return nil
}

// followPendingStarts settles a start once the snapshot shows its thread
// attached to the worktree the command created.
func (m *Model) followPendingStarts() {
	for _, v := range m.state.DraftThreads {
		if v == nil || v.PendingStart == nil || m.state.StartedDraft != nil {
			continue
		}
		ps := v.PendingStart
		c := ps.Command
		w, ok := m.worktreeForCommand(c.ID)
		if !ok {
			continue
		}
		if ps.State == pendingFailed && m.startRetryable(c.ID) {
			ps.State = pendingRetryable
			m.markDirty()
		}
		// Settle on the start's own thread: the one the server named, else
		// an ordinary (non-job) thread in its worktree.
		var owner string
		for _, t := range m.snapshot.Threads {
			if t.WorktreeID != w.ID {
				continue
			}
			if ps.ThreadID != "" && t.ID == ps.ThreadID {
				owner = t.ID
				break
			}
			if owner == "" && ps.ThreadID == "" && !jobThread(t) {
				owner = t.ID
			}
		}
		if owner != "" {
			m.startAccepted(c, owner, m.snapshot.Revision)
			m.markDirty()
		}
	}
}

func (m *Model) pendingStartBlocked() string {
	ps := m.viewState().PendingStart
	if ps == nil {
		return ""
	}
	switch ps.State {
	case pendingFailed:
		return "Thread not started: " + ps.Detail + ". Inspect it in Project settings › General › Worktrees, or Discard to start again with another branch."
	case pendingUnknown:
		return ps.Detail + ". Inspect it in Project settings › General › Worktrees, then Discard to start again."
	case pendingRetryable:
		return "Worktree created but its thread did not start: " + ps.Detail + ". Retry attaches it."
	}
	return "Creating the worktree…"
}

// retryPendingStart re-asks the visible draft's start that needs the user
// to retry (created but not attached).
func (m *Model) retryPendingStart() tea.Cmd {
	if !m.creatingThread() {
		return nil
	}
	ps := m.viewState().PendingStart
	if ps == nil || ps.State != pendingRetryable {
		return nil
	}
	ps.State = pendingRunning
	m.status = "Attaching the worktree…"
	m.markDirty()
	return m.dispatchStart(ps.Command)
}

// discardPendingStart stops following a start whose outcome the user will
// resolve through project settings. The draft, prompt and branch stay; the
// branch is marked as possibly existing.
func (m *Model) discardPendingStart() tea.Cmd {
	if !m.creatingThread() {
		return nil
	}
	v := m.viewState()
	if v.PendingStart == nil || v.PendingStart.State == pendingRunning {
		return m.showNotice("The worktree is still being created; use Stop following")
	}
	m.dropPendingStart(v)
	return m.showNotice("Stopped following that worktree; manage it in Project settings › General › Worktrees")
}

func (m *Model) startRetryable(id string) bool {
	w, ok := m.worktreeForCommand(id)
	return ok && w.State == protocol.WorktreeUnattached && !w.Unverified
}

// confirmReleaseSettledStart asks before a workspace or branch edit
// abandons a kept (failed, unknown or unattached) start; it reports whether
// it opened the confirmation. The edit is applied after confirming.
func (m *Model) confirmReleaseSettledStart(a action) bool {
	ps := m.viewState().PendingStart
	if ps == nil || ps.State == pendingRunning {
		return false
	}
	branch := singleLine(ps.Command.Workspace.Branch)
	items := []menuItem{
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Leave it and change the workspace", Action: action{Kind: "worktree-start-release-confirm", ID: ps.Command.ID, Value: a.Value}},
	}
	if w, ok := m.worktreeForCommand(ps.Command.ID); ok {
		items = append(items, menuItem{Note: "Worktree " + truncatePathLeft(w.Path, 48) + " stays (" + worktreeStateLabel(w) + ")"})
		if ps.State == pendingRetryable {
			items = append(items, menuItem{Note: "Retry could still attach it; after this it can only be removed or forgotten"})
		}
	}
	items = append(items,
		menuItem{Note: "Branch " + branch + " is treated as used"},
		menuItem{Note: "Manage it in Project settings › General › Worktrees"})
	m.showMenuFor("Leave start · ", branch, items)
	return true
}

// confirmStopFollowing asks before abandoning a start still running (for
// example while the server is unreachable).
func (m *Model) confirmStopFollowing() tea.Cmd {
	if !m.creatingThread() {
		return nil
	}
	ps := m.viewState().PendingStart
	if ps == nil || ps.State != pendingRunning {
		return m.discardPendingStart()
	}
	branch := singleLine(ps.Command.Workspace.Branch)
	m.showMenuFor("Stop following · ", branch, []menuItem{
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Stop following this start", Action: action{Kind: "worktree-start-stop-confirm", ID: ps.Command.ID}},
		{Note: "The server may still create the worktree and its thread"},
		{Note: "Branch " + branch + " will be treated as used"},
		{Note: "Check Project settings › General › Worktrees later"},
	})
	return nil
}

func (m *Model) renderPendingStart(f *frame, x, y, width int, ps *pendingStart) {
	p := m.colors()
	branch := singleLine(ps.Command.Workspace.Branch)
	state, text := "active", "Creating worktree "+branch+"…"
	type button struct{ label, key, kind string }
	var buttons []button
	switch ps.State {
	case pendingUnknown:
		state, text = "unknown", "Outcome unknown · "+branch
		buttons = []button{{"Worktrees", "worktree-start-inspect", "worktree-start-inspect"}, {"Discard", "worktree-start-discard", "worktree-start-discard"}}
	case pendingRetryable:
		state, text = "failed", "Not attached · "+branch
		buttons = []button{{"Retry", "worktree-start-retry", "worktree-start-retry"}, {"Discard", "worktree-start-discard", "worktree-start-discard"}}
	case pendingFailed:
		state, text = "failed", "Not started · "+ps.Detail
		buttons = []button{{"Worktrees", "worktree-start-inspect", "worktree-start-inspect"}, {"Discard", "worktree-start-discard", "worktree-start-discard"}}
	default:
		buttons = []button{{"Stop following", "worktree-start-stop", "worktree-start-stop"}}
	}
	glyph, ink := panelStatusMark(m, state)
	used := 0
	for _, b := range buttons {
		used += ansi.StringWidth(b.label) + 3
	}
	markWidth := ansi.StringWidth(glyph) + 1
	f.text(x, y, min(width, markWidth), glyph+" ", ink, p.canvas)
	f.text(x+markWidth, y, max(0, width-markWidth-used), text, p.text, p.canvas)
	bx := x + width - used
	for _, b := range buttons {
		w := ansi.StringWidth(b.label) + 2
		if bx >= x+markWidth {
			f.compactButton(m, bx+1, y, w, b.label, b.key, action{Kind: b.kind}, false, normalControl)
		}
		bx += w + 1
	}
}

// worktreeWaitLine explains queued prompts held because the thread's
// worktree is unavailable; activating it opens the recovery details.
func (m *Model) worktreeWaitLine(t protocol.Thread, p palette) (contentLine, bool) {
	if t.WorktreeID == "" || len(t.Queue) == 0 {
		return contentLine{}, false
	}
	state := "Record unavailable"
	if w, ok := m.worktreeByID(t.WorktreeID); ok {
		if w.State == protocol.WorktreePresent {
			return contentLine{}, false
		}
		state = worktreeStateLabel(w)
	}
	return contentLine{text: "· Waiting for worktree · " + state, fg: p.text, bg: p.canvas, lead: "·", leadFG: p.gold, action: action{Kind: "checkout-info"}}, true
}
