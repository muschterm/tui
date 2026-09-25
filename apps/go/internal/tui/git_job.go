package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_job.go adds agent conflict resolution (ADR 0023 S4) to the operation
// panel: a start form (agent, the new-thread default settings for it, the
// paths and optional instructions), the job section (status, the job
// thread's pending requests, Stop, Follow up, End, a read-only transcript),
// review mode (per-path Accept and Reject pinned to the reviewed tokens,
// violations, Stale with Refresh) and the content gate on Continue and Skip
// (git_operation.go). Every command shares the per-target write slot.

// gitReviewAPI refreshes an attached job's review.
type gitReviewAPI interface {
	GitOperationRefreshReview(ctx context.Context, target client.GitTarget) (protocol.GitOperationState, error)
}

var _ gitReviewAPI = (*client.Client)(nil)

// gitJobInputKey is the focus key of the instructions / follow-up input.
const gitJobInputKey = "git:job-input"

// gitJobStart is the open start form; it takes over the Git surface body.
type gitJobStart struct {
	key      string
	target   client.GitTarget
	state    protocol.GitOperationState
	agentID  string
	paths    []string
	selected map[string]bool
}

// gitJobDialog is a shown job or review confirmation.
type gitJobDialog struct {
	key string
	cmd protocol.Command
	// label is the write's result label.
	label string
}

type gitJobUI struct {
	// viewed records, per path, the review generation whose diffs the
	// user has seen ("agent" and "staged" parts); Accept needs them.
	viewed map[string]map[string]string
	// reviewSeq orders review refresh replies; older ones are dropped.
	reviewSeq  uint64
	transcript string // job thread shown in the viewer, refreshed live
	input      textarea.Model
	ready      bool
	start      *gitJobStart
	followup   bool // the follow-up input is open
	dialog     *gitJobDialog
}

type gitReviewMsg struct {
	seq   uint64
	key   string
	state protocol.GitOperationState
	err   error
}

// gitJobsEnabled reports a server with resolution jobs.
func (m *Model) gitJobsEnabled() bool {
	return m.gitConflictsEnabled() && slices.Contains(m.snapshot.Capabilities, "git-jobs")
}

func gitJobKind(kind string) bool {
	switch kind {
	case protocol.GitKindResolveJobStart, protocol.GitKindResolveJobFollowup, protocol.GitKindResolveJobCancel, protocol.GitKindResolveJobEnd:
		return true
	}
	return false
}

func (m *Model) gitJobInput() *textarea.Model {
	if !m.gitJ.ready {
		m.gitJ.input = newInput("Instructions (optional)")
		m.gitJ.input.CharLimit = 4096
		m.gitJ.input.SetHeight(1)
		m.gitJ.ready = true
	}
	return &m.gitJ.input
}

// gitJobThread is the job thread of an attached review, if known.
func (m *Model) gitJobThread(o *protocol.GitOperationState) (protocol.Thread, bool) {
	if o == nil || o.Review == nil || o.Review.JobThreadID == "" {
		return protocol.Thread{}, false
	}
	return m.threadByID(o.Review.JobThreadID)
}

// gitJobDefaults are the settings a new job uses: the saved new-thread
// defaults when they are for this agent, else the agent's own (nil).
func (m *Model) gitJobDefaults(agentID string) *protocol.Settings {
	if d := m.snapshot.AppSettings.NewThreadDefaults; d != nil && d.AgentID == agentID {
		s := d.Settings
		return &s
	}
	return nil
}

// gitJobAgents are the agents a job may use.
func (m *Model) gitJobAgents() []protocol.Agent {
	var out []protocol.Agent
	for _, a := range m.snapshot.Agents {
		if a.State != "unavailable" && a.State != "missing" {
			out = append(out, a)
		}
	}
	return out
}

// gitJobAction handles every job and review control; ok reports that a was
// one of them.
func (m *Model) gitJobAction(a action) (tea.Cmd, bool) {
	if !strings.HasPrefix(a.Kind, "git-job-") {
		return nil, false
	}
	key, target := m.gitTarget()
	g := m.gitViews[key]
	if !m.gitJobsEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Agent resolution needs a newer server"), true
	}
	if g == nil || g.oper == nil || g.oper.Kind == "" {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindResolveJobStart, "no_operation")), m.refreshGit()), true
	}
	o := g.oper
	switch a.Kind {
	case "git-job-open":
		var paths []string
		for _, c := range o.Conflicts {
			if !c.Submodule {
				paths = append(paths, c.Path)
			}
		}
		if len(paths) == 0 {
			return m.showNoticeAs(noticeUnavailable, "No unmerged paths to resolve"), true
		}
		sel := map[string]bool{}
		for _, p := range paths {
			sel[p] = a.ID == "" || p == a.ID
		}
		agentID := ""
		if d := m.snapshot.AppSettings.NewThreadDefaults; d != nil {
			agentID = d.AgentID
		}
		if _, ok := m.agentByID(agentID); !ok {
			if agents := m.gitJobAgents(); len(agents) > 0 {
				agentID = agents[0].ID
			}
		}
		m.gitJ.start = &gitJobStart{key: key, target: target, state: *o, agentID: agentID, paths: paths, selected: sel}
		m.gitJobInput().SetValue("")
		m.gitJobInput().Placeholder = "Instructions (optional)"
		m.gitJ.followup = false
		m.viewState().DetailScroll = 0
		m.markDirty()
		return m.setFocus("git:job-cancel"), true
	case "git-job-cancel-form":
		m.gitJ.start, m.gitJ.followup = nil, false
		m.markDirty()
		return m.setFocus("git-refresh"), true
	case "git-job-agent":
		if s := m.gitJ.start; s != nil {
			s.agentID = a.ID
			m.markDirty()
		}
		return nil, true
	case "git-job-path":
		if s := m.gitJ.start; s != nil {
			s.selected[a.ID] = !s.selected[a.ID]
			m.markDirty()
		}
		return nil, true
	case "git-job-input":
		return m.setFocus(gitJobInputKey), true
	case "git-job-start":
		s := m.gitJ.start
		if s == nil || s.key != key {
			return nil, true
		}
		if s.agentID == "" {
			return m.showNoticeAs(noticeUnavailable, "Choose an agent"), true
		}
		var paths []string
		for _, p := range s.paths {
			if s.selected[p] {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			return m.showNoticeAs(noticeUnavailable, "Choose at least one path"), true
		}
		if len(paths) == len(s.paths) && !s.state.ConflictsTruncated && len(s.paths) == len(s.state.Conflicts) {
			paths = nil // all, and every conflict was listed
		}
		if o.OperationID != s.state.OperationID || o.HeadOid != s.state.HeadOid || o.UnmergedFingerprint != s.state.UnmergedFingerprint {
			m.gitJ.start = nil
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindResolveJobStart, "stale_operation")), m.refreshGit()), true
		}
		cmd := client.GitResolveJobStartCommand(identity(), target, s.state, s.agentID, m.gitJobDefaults(s.agentID), paths, strings.TrimSpace(m.gitJobInput().Value()))
		m.gitJ.start = nil
		m.gitJobInput().SetValue("")
		return tea.Batch(m.setFocus("git-refresh"), m.sendGitJob(key, g, cmd, "resolution job")), true
	case "git-job-stop":
		return m.sendGitJob(key, g, client.GitResolveJobCancelCommand(identity(), target, o.OperationID), "stop"), true
	case "git-job-followup":
		m.gitJ.followup = true
		m.gitJobInput().SetValue("")
		m.gitJobInput().Placeholder = "Follow-up for the agent"
		m.markDirty()
		return m.setFocus(gitJobInputKey), true
	case "git-job-followup-send":
		text := strings.TrimSpace(m.gitJobInput().Value())
		if text == "" {
			return m.showNoticeAs(noticeUnavailable, "Write a follow-up first"), true
		}
		m.gitJ.followup = false
		m.gitJobInput().SetValue("")
		return tea.Batch(m.setFocus("git-refresh"), m.sendGitJob(key, g, client.GitResolveJobFollowupCommand(identity(), target, o.OperationID, text), "follow-up")), true
	case "git-job-end":
		m.gitJ.dialog = &gitJobDialog{key: key, cmd: client.GitResolveJobEndCommand(identity(), target, o.OperationID), label: "end"}
		m.showMenuFor("End resolution job · ", safe(singleLine(o.Kind)), []menuItem{
			{Note: "End the job and remove its thread?"},
			{Note: "The agent's changes stay; Continue still asks you to review them"},
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "End job", Action: action{Kind: "git-job-confirm", Value: "destructive"}},
		})
		m.menuIndex = 2
		return nil, true
	case "git-job-confirm":
		d := m.gitJ.dialog
		m.gitJ.dialog = nil
		if d == nil || d.key != key {
			return nil, true
		}
		return m.sendGitJob(key, g, d.cmd, d.label), true
	case "git-job-accept", "git-job-reject":
		return m.openGitReviewDialog(key, target, g, a.Kind == "git-job-accept", a.ID), true
	case "git-job-refresh":
		return m.refreshGitReview(key, target), true
	case "git-job-answer":
		t, ok := m.gitJobThread(o)
		if !ok {
			return nil, true
		}
		return m.answerJobRequest(t.ID, a.ID), true
	case "git-job-transcript":
		t, ok := m.gitJobThread(o)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "The job thread is not available"), true
		}
		return m.openGitJobTranscript(t), true
	case "git-job-view":
		return m.openGitReviewViewer(key, target, o, a.ID), true
	}
	return nil, true
}

// sendGitJob sends a job or review command unless a write is in flight.
func (m *Model) sendGitJob(key string, g *gitView, cmd protocol.Command, label string) tea.Cmd {
	if reason := m.gitRefBlock(key, g, cmd.Kind); reason != "" && reason != gitErrorCopy("operation_in_progress") && reason != gitErrorCopy("conflicted") && reason != gitJobLeaseCopy {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	m.clearGitWarning(key)
	return m.sendGitWrite(key, cmd, label)
}

// gitReviewItem finds a review item by path.
func gitReviewItem(o *protocol.GitOperationState, path string) (protocol.GitResolveItem, bool) {
	if o == nil || o.Review == nil {
		return protocol.GitResolveItem{}, false
	}
	for _, it := range o.Review.Items {
		if it.Path == path {
			return it, true
		}
	}
	return protocol.GitResolveItem{}, false
}

// openGitReviewDialog confirms accepting or rejecting one reviewed path,
// pinned to the item as reviewed.
func (m *Model) openGitReviewDialog(key string, target client.GitTarget, g *gitView, accept bool, path string) tea.Cmd {
	o := g.oper
	it, ok := gitReviewItem(o, path)
	switch {
	case !ok:
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "The review changed since shown · refreshed"), m.refreshGit())
	case o.Review.Stale:
		return m.showNoticeAs(noticeUnavailable, "The review is out of date · Refresh review first")
	case o.Review.State == protocol.GitReviewRunning:
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindConflictResolve, "job_running"))
	case it.Unknown:
		return m.showNoticeAs(noticeUnavailable, "This path could not be compared · Refresh review or resolve it yourself")
	case it.Decision != "":
		return m.showNoticeAs(noticeUnavailable, "Already "+safe(singleLine(it.Decision))+" · refresh the review to decide again")
	case accept && !m.gitReviewViewed(o, it):
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "Review the agent's diff first"), m.openGitReviewViewer(key, target, o, path))
	}
	p := truncatePathLeft(safe(singleLine(path)), gitDialogPathWidth)
	var notes []string
	var cmd protocol.Command
	verb := ""
	if accept {
		ack := !it.Staged && !it.Deleted && (it.HasMarkers || it.Binary)
		cmd = client.GitReviewAcceptCommand(identity(), target, it, ack)
		verb = "Accept"
		switch {
		case it.Staged:
			notes = []string{"Accept the agent's staged " + p + "?", "Keeps exactly the staged entry whose diff you viewed"}
			if it.DiffTruncated {
				notes = append(notes, "Its diff was truncated · not all of it was shown")
			}
		case it.Deleted:
			notes = []string{"Accept the agent's deletion of " + p + "?", "Resolves the path as deleted"}
		default:
			notes = []string{"Accept the agent's " + p + "?", "Stages the working file as the resolution"}
		}
		if ack {
			notes = append(notes, "It still contains conflict markers or is binary · it is staged as it is")
			verb = "Stage as it is"
		}
	} else {
		cmd = client.GitReviewRejectCommand(identity(), target, it)
		verb = "Reject changes"
		what := gitReviewBadges(it)
		notes = []string{"Reject the agent's changes to " + p + "?", "Replaces the current file (" + what + ") with the copy made before the agent ran", "What it replaces is copied first · Restore brings it back"}
	}
	m.gitJ.dialog = &gitJobDialog{key: key, cmd: cmd, label: path}
	items := make([]menuItem, 0, len(notes)+2)
	for _, n := range notes {
		items = append(items, menuItem{Note: n})
	}
	confirm := action{Kind: "git-job-confirm"}
	if !accept || verb == "Stage as it is" {
		confirm.Value = "destructive"
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}}, menuItem{Label: verb, Action: confirm})
	title := "Accept · "
	if !accept {
		title = "Reject · "
	}
	m.showMenuFor(title, p, items)
	m.menuIndex = len(items) - 2
	return nil
}

// refreshGitReview asks the server to recompute the review.
func (m *Model) refreshGitReview(key string, target client.GitTarget) tea.Cmd {
	api, ok := m.gitClient().(gitReviewAPI)
	if !ok {
		return m.refreshGit()
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.gitJ.reviewSeq++
	seq := m.gitJ.reviewSeq
	return tea.Batch(m.showNoticeAs(noticeDone, "Refreshing the review…"), func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		s, err := api.GitOperationRefreshReview(deadline, target)
		return gitReviewMsg{seq: seq, key: key, state: s, err: err}
	})
}

func (m *Model) acceptGitReview(msg gitReviewMsg) tea.Cmd {
	current, _ := m.gitTarget()
	g := m.gitViews[msg.key]
	if msg.key != current || g == nil || msg.seq != m.gitJ.reviewSeq {
		return nil // another target, or an older refresh
	}
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Review refresh failed · "+safe(singleLine(msg.err.Error())))
	}
	s := msg.state
	g.oper = &s
	m.markDirty()
	return m.gitConflictViewerRefresh(msg.key, g)
}

// openGitReviewViewer opens the conflict viewer on a reviewed path with its
// Agent diff and Staged diff tabs.
func (m *Model) openGitReviewViewer(key string, target client.GitTarget, o *protocol.GitOperationState, path string) tea.Cmd {
	it, ok := gitReviewItem(o, path)
	if !ok {
		return nil
	}
	c, found := m.gitRowConflict(path)
	if !found {
		c = protocol.GitConflict{Path: path}
	}
	v := newGitConflictViewer(key, target, c)
	v.item, v.itemGen = &it, gitReviewGen(o, it)
	if it.Diff != "" {
		v.tab = gitViewerAgentDiff
	} else if it.IndexDiff != "" {
		v.tab = gitViewerStagedDiff
	}
	v.lines[gitViewerAgentDiff] = gitDiffLines(it.Diff)
	v.lines[gitViewerStagedDiff] = gitDiffLines(it.IndexDiff)
	m.gitCF.viewer = v
	m.markGitReviewViewed(v)
	m.viewState().DetailScroll = 0
	m.markDirty()
	return tea.Batch(m.setFocus("git:conflict-close"), m.loadViewerTab(v))
}

// Review-only viewer tabs, filled from the review item.
const gitViewerAgentDiff, gitViewerStagedDiff = "agent diff", "staged diff"

// gitDiffLines splits a unified diff into display lines; marker ink marks
// removed and added lines is left to the text; conflict markers are gold.
func gitDiffLines(diff string) []gitViewerLine {
	if diff == "" {
		return nil
	}
	return gitViewerLines(&protocol.GitConflictFile{Content: []byte(diff)})
}

// openGitJobTranscript shows the hidden job thread's conversation in the
// read-only viewer; the thread never joins navigation.
func (m *Model) openGitJobTranscript(t protocol.Thread) tea.Cmd {
	content := gitJobTranscriptText(t)
	m.gitJ.transcript = t.ID
	origin := m.focus
	if origin == "" || strings.HasPrefix(origin, "menu:") || strings.HasPrefix(origin, "viewer-") {
		origin = "right-body"
	}
	release := m.releaseViewerImage()
	m.menu, m.contextMenu, m.hover = nil, nil, ""
	m.viewer = &attachmentViewer{att: protocol.Attachment{Kind: "terminal-output", Name: "Agent transcript · " + safe(singleLine(t.Title)), Content: content}, origin: origin, loaded: true}
	return tea.Batch(release, m.setFocus("viewer-body"))
}

// gitJobTranscriptText is the job thread's activity as sanitized text.
func gitJobTranscriptText(t protocol.Thread) string {
	var b strings.Builder
	for _, a := range t.Activity {
		role := a.Role
		if a.Title != "" {
			role += " · " + a.Title
		}
		b.WriteString(strings.TrimSpace(role) + "\n")
		if a.Text != "" {
			b.WriteString(a.Text + "\n")
		}
		b.WriteString("\n")
	}
	for _, r := range t.Requests {
		if r.State == "pending" {
			b.WriteString("Pending · " + r.Title + "\n")
		}
	}
	if b.Len() == 0 {
		b.WriteString("No activity yet\n")
	}
	return safe(b.String())
}

// gitJobInputKeyPress edits the instructions or follow-up: Enter sends a
// follow-up (or starts the job from the form), Esc cancels.
func (m *Model) gitJobInputKeyPress(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		if m.gitJ.followup {
			return m.activate(action{Kind: "git-job-followup-send"})
		}
		return m.activate(action{Kind: "git-job-start"})
	case "esc":
		if m.gitJ.followup {
			m.gitJ.followup = false
			m.markDirty()
			return m.setFocus("git-refresh")
		}
		return m.activate(action{Kind: "git-job-cancel-form"})
	case "shift+enter", "ctrl+j", "tab", "shift+tab", "up", "down":
		return nil
	}
	c := updateInput(m.gitJobInput(), k)
	m.markDirty()
	return c
}

func (m *Model) gitJobInputPaste(content string) tea.Cmd {
	c := updateInput(m.gitJobInput(), tea.PasteMsg{Content: singleLine(safe(content))})
	m.markDirty()
	return c
}

// gitJobSettle drops job state whose target is not shown and dialog state
// whose menu is gone.
func (m *Model) gitJobSettle(key string) {
	if s := m.gitJ.start; s != nil && s.key != key {
		m.gitJ.start = nil
	}
	m.refreshGitTranscript()
	if m.jobReq != "" && m.requestThread().ID != m.jobReq {
		m.jobReq = "" // answered, or the job ended
	}
	if len(m.menu) == 0 {
		m.gitJ.dialog = nil
	}
}

// gitJobKey handles review rows: a accept, x reject (Enter views).
func (m *Model) gitJobKey(s string) (tea.Cmd, bool) {
	focus := m.focus
	if rest, ok := strings.CutPrefix(focus, "git-jact:"); ok {
		_, path, _ := strings.Cut(rest, ":")
		focus = "git:jitem:" + path
	}
	path, ok := strings.CutPrefix(focus, "git:jitem:")
	if !ok {
		return nil, false
	}
	switch s {
	case "a":
		return m.activate(action{Kind: "git-job-accept", ID: path}), true
	case "x":
		return m.activate(action{Kind: "git-job-reject", ID: path}), true
	}
	return nil, false
}

// requestThread is the thread whose pending requests the request cards show
// and answer: a resolution job thread the user chose to answer (jobReq)
// while it has pending requests, else the active thread.
func (m *Model) requestThread() protocol.Thread {
	if m.jobReq != "" {
		if t, ok := m.threadByID(m.jobReq); ok && jobThread(t) && slices.ContainsFunc(t.Requests, func(r protocol.Request) bool { return r.State == "pending" }) {
			return t
		}
	}
	return m.thread()
}

func (m *Model) requestThreadID() string {
	if t := m.requestThread(); t.ID != "" && m.jobReq != "" && t.ID == m.jobReq {
		return t.ID
	}
	return m.state.Active
}

// answerJobRequest shows a job thread's pending request in the request card
// (the same question and approval components, answering the job thread).
func (m *Model) answerJobRequest(threadID, requestID string) tea.Cmd {
	m.jobReq = threadID
	for i, r := range m.requests() {
		if r.ID == requestID {
			m.selectRequest(i)
		}
	}
	m.markDirty()
	return m.setFocus("request-body")
}

// gitReviewGen identifies one review generation of an item.
func gitReviewGen(o *protocol.GitOperationState, it protocol.GitResolveItem) string {
	fp := ""
	if o != nil && o.Review != nil {
		fp = o.Review.Fingerprint
	}
	return fp + "|" + it.ConflictPin + "|" + it.WorktreeToken + "|" + it.IndexOid
}

// gitReviewParts are the diffs an item must be viewed in before Accept.
func gitReviewParts(it protocol.GitResolveItem) []string {
	var parts []string
	if it.Diff != "" {
		parts = append(parts, gitViewerAgentDiff)
	}
	if it.Staged && it.IndexDiff != "" {
		parts = append(parts, gitViewerStagedDiff)
	}
	return parts
}

// markGitReviewViewed records the viewer's displayed diff tab for the
// current review generation.
func (m *Model) markGitReviewViewed(v *gitConflictViewer) {
	if v == nil || v.item == nil || (v.tab != gitViewerAgentDiff && v.tab != gitViewerStagedDiff) {
		return
	}
	g := m.gitViews[v.key]
	if g == nil || g.oper == nil {
		return
	}
	it, ok := gitReviewItem(g.oper, v.item.Path)
	if !ok {
		return
	}
	if m.gitJ.viewed == nil {
		m.gitJ.viewed = map[string]map[string]string{}
	}
	if m.gitJ.viewed[it.Path] == nil {
		m.gitJ.viewed[it.Path] = map[string]string{}
	}
	m.gitJ.viewed[it.Path][v.tab] = gitReviewGen(g.oper, it)
}

// gitReviewViewed reports that every diff of the item was viewed in the
// current review generation (an item without any diff needs nothing).
func (m *Model) gitReviewViewed(o *protocol.GitOperationState, it protocol.GitResolveItem) bool {
	gen := gitReviewGen(o, it)
	for _, part := range gitReviewParts(it) {
		if m.gitJ.viewed[it.Path][part] != gen {
			return false
		}
	}
	return true
}

// refreshGitTranscript keeps an open job transcript current.
func (m *Model) refreshGitTranscript() {
	if m.gitJ.transcript == "" || m.viewer == nil || !strings.HasPrefix(m.viewer.att.Name, "Agent transcript") {
		m.gitJ.transcript = ""
		return
	}
	t, ok := m.threadByID(m.gitJ.transcript)
	if !ok {
		return
	}
	if content := gitJobTranscriptText(t); content != m.viewer.att.Content {
		m.viewer.att.Content = content
		m.markDirty()
	}
}
