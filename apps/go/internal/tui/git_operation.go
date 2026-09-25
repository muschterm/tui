package tui

import (
	"context"
	"net/http"

	"github.com/charmbracelet/x/ansi"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_operation.go adds merge and rebase (ADR 0023) to the Git surface:
// entry points that read GET /v1/git/integrate/preview and confirm from it,
// the operation panel fed by GET /v1/git/operation, and the Continue, Skip
// and Abort confirmations that send exactly the fingerprints they showed.
// Commands share the per-target write slot, Retry and result handling of
// git_ref.go. The panel reads the operation on every refresh trigger (the
// same generation as status), so a stale reply is dropped with it.
//
// S3 hook points (per-file conflict actions: choose ours/theirs/base, mark
// resolved, restore, open in editor): conflict rows are gitRow kind
// "conflict" with key "git:conflict:<path>" and the protocol.GitConflict in
// row.conflict; gitConflictControls returns their reserved slot controls
// (none yet), painted like branch-row controls.

// gitOperationAPI reads the operation and previews; tests inject a fake.
type gitOperationAPI interface {
	GitOperation(ctx context.Context, target client.GitTarget) (protocol.GitOperationState, error)
	GitIntegratePreview(ctx context.Context, target client.GitTarget, kind, ref string) (protocol.GitIntegratePreview, error)
}

var _ gitOperationAPI = (*client.Client)(nil)

// gitOperationTimeout exceeds the server's 30 minute budget for merge,
// rebase, continue and skip, so the server's own outcome arrives first.
const gitOperationTimeout = 31 * time.Minute

type gitOperationMsg struct {
	key   string
	gen   uint64
	state protocol.GitOperationState
	err   error
}

type gitPreviewMsg struct {
	key     string
	seq     uint64
	preview protocol.GitIntegratePreview
	err     error
}

// gitIntegrateDialog confirms a merge or rebase from its preview.
type gitIntegrateDialog struct {
	key     string
	target  client.GitTarget
	status  protocol.GitStatus
	preview protocol.GitIntegratePreview
}

// gitOpDialog confirms Continue, Skip or Abort for the state it showed.
type gitOpDialog struct {
	key    string
	target client.GitTarget
	kind   string // protocol.GitKindOperation*
	state  protocol.GitOperationState
}

// gitOpUI is the client-local merge/rebase state.
type gitOpUI struct {
	previewSeq uint64
	// pendingPreview arrived while another menu was open.
	pendingPreview *gitIntegrateDialog
	integrate      *gitIntegrateDialog
	op             *gitOpDialog
	// review is the open scrollable review of op's acknowledged lists.
	review *gitReviewPanel
}

// gitOperationsEnabled reports a server with merge and rebase.
func (m *Model) gitOperationsEnabled() bool {
	return m.gitRefsEnabled() && slices.Contains(m.snapshot.Capabilities, "git-operations")
}

func (m *Model) gitOperationClient() gitOperationAPI {
	if a, ok := m.gitClient().(gitOperationAPI); ok {
		return a
	}
	return nil
}

// gitOperationKind reports the ADR 0023 command kinds.
func gitOperationKind(kind string) bool {
	switch kind {
	case protocol.GitKindMerge, protocol.GitKindRebase, protocol.GitKindOperationAbort,
		protocol.GitKindOperationContinue, protocol.GitKindOperationSkip:
		return true
	}
	return false
}

// readGitOperation reads the operation for the refresh generation gen.
func (m *Model) readGitOperation(key string, target client.GitTarget, gen uint64) tea.Cmd {
	api := m.gitOperationClient()
	if api == nil || !m.gitOperationsEnabled() {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := api.GitOperation(deadline, target)
		return gitOperationMsg{key: key, gen: gen, state: s, err: err}
	}
}

func (m *Model) acceptGitOperation(msg gitOperationMsg) tea.Cmd {
	g := m.acceptGitView(msg.key, msg.gen)
	if g == nil {
		return nil
	}
	if msg.err != nil {
		g.operErr = safe(singleLine(msg.err.Error()))
		return nil
	}
	s := msg.state
	g.oper, g.operErr = &s, ""
	// A shown confirmation whose state moved on loses its action.
	if d := m.gitO.op; d != nil && d.key == msg.key && !gitSameStop(d.state, s) {
		m.gitO.op = nil
		if m.gitO.review != nil {
			m.gitO.review = nil
			m.setFocus("git-refresh")
		} else if len(m.menu) > 0 {
			m.menu = nil
		}
		m.showNoticeAs(noticeUnavailable, gitRefCopy(d.kind, "stale_operation"))
	}
	return m.gitConflictViewerRefresh(msg.key, g)
}

// gitSameStop reports two reads of one operation at the same stop.
func gitSameStop(a, b protocol.GitOperationState) bool {
	return a.Kind == b.Kind && a.OperationID == b.OperationID && a.HeadOid == b.HeadOid && a.Step == b.Step &&
		a.WorktreeFingerprint == b.WorktreeFingerprint && a.StagedFingerprint == b.StagedFingerprint &&
		a.UnmergedFingerprint == b.UnmergedFingerprint &&
		a.DiscardsOnAbortFingerprint == b.DiscardsOnAbortFingerprint && a.DiscardsOnSkipFingerprint == b.DiscardsOnSkipFingerprint &&
		a.BackupMissingOnAbortFingerprint == b.BackupMissingOnAbortFingerprint && a.BackupMissingOnSkipFingerprint == b.BackupMissingOnSkipFingerprint &&
		a.AbortDropsFingerprint == b.AbortDropsFingerprint && a.MarkersFingerprint == b.MarkersFingerprint && a.MarkersIncomplete == b.MarkersIncomplete &&
		gitAgentChangesFingerprint(a) == gitAgentChangesFingerprint(b)
}

// gitAgentChangesFingerprint is the content gate's fingerprint ("" when
// there is nothing to acknowledge).
func gitAgentChangesFingerprint(s protocol.GitOperationState) string {
	if !gitAgentChangesPending(s) {
		return ""
	}
	return s.AgentChanges.Fingerprint + "/" + strconv.FormatBool(s.AgentChanges.Incomplete)
}

// gitAgentChangesPending reports index changes no decision explains, which
// Continue and Skip must show and acknowledge.
func gitAgentChangesPending(s protocol.GitOperationState) bool {
	return s.AgentChanges != nil && (len(s.AgentChanges.Items) > 0 || s.AgentChanges.Incomplete)
}

// gitLongWrite sends a merge, rebase, continue or skip with a client
// timeout beyond the server's 30 minute budget.
func (m *Model) gitLongWrite(key string, cmd protocol.Command) tea.Cmd {
	var send func(context.Context, protocol.Command) (protocol.Receipt, error)
	if c, ok := m.gitWriter().(*client.Client); ok && c.HTTP != nil {
		long := *c
		long.HTTP = &http.Client{Transport: c.HTTP.Transport, Timeout: gitOperationTimeout}
		send = long.Command
	} else if s, ok := m.gitWriter().(gitSyncAPI); ok {
		send = s.GitSync
	} else {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.markDirty()
	return func() tea.Msg {
		r, err := send(ctx, cmd)
		return gitWriteMsg{key: key, cmd: cmd, receipt: r, err: err}
	}
}

// gitOperationAction handles every merge/rebase control; ok reports that a
// was one of them.
func (m *Model) gitOperationAction(a action) (tea.Cmd, bool) {
	switch a.Kind {
	case "git-review-cancel":
		m.gitO.review, m.gitO.op = nil, nil
		m.markDirty()
		return m.setFocus("git-refresh"), true
	case "git-integrate", "git-integrate-confirm", "git-op-abort", "git-op-continue", "git-op-skip", "git-op-confirm":
	default:
		return nil, false
	}
	key, target := m.gitTarget()
	g := m.gitViews[key]
	if !m.gitOperationsEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Merge and rebase need a newer server"), true
	}
	if g == nil || g.status == nil {
		return m.showNoticeAs(noticeUnavailable, "Git status not loaded"), true
	}
	switch a.Kind {
	case "git-integrate":
		return m.startGitPreview(key, target, a.Value, a.ID), true
	case "git-integrate-confirm":
		dlg := m.gitO.integrate
		m.gitO.integrate = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		kind := protocol.GitKindMerge
		if dlg.preview.Kind == protocol.GitOperationRebase {
			kind = protocol.GitKindRebase
		}
		if reason := m.gitRefBlock(key, g, kind); reason != "" {
			return m.showNoticeAs(noticeUnavailable, reason), true
		}
		s := *g.status
		if s.HeadOid != dlg.status.HeadOid || s.Branch != dlg.status.Branch {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "stale_head")), m.refreshGit()), true
		}
		m.clearGitWarning(key)
		var cmd protocol.Command
		label := safe(singleLine(gitPreviewLabel(dlg.preview)))
		if kind == protocol.GitKindMerge {
			cmd = client.GitMergeCommand(identity(), dlg.target, dlg.status, dlg.preview)
		} else {
			cmd = client.GitRebaseCommand(identity(), dlg.target, dlg.status, dlg.preview, dlg.preview.Published)
		}
		return m.sendGitWrite(key, cmd, label), true
	case "git-op-abort", "git-op-continue", "git-op-skip":
		kind := map[string]string{"git-op-abort": protocol.GitKindOperationAbort, "git-op-continue": protocol.GitKindOperationContinue, "git-op-skip": protocol.GitKindOperationSkip}[a.Kind]
		if g.oper == nil || g.oper.Kind == "" {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "no_operation")), m.refreshGit()), true
		}
		if reason := m.gitOpBlock(key, g, kind); reason != "" {
			return m.showNoticeAs(noticeUnavailable, reason), true
		}
		m.openGitOpDialog(&gitOpDialog{key: key, target: target, kind: kind, state: *g.oper})
		return nil, true
	case "git-op-confirm":
		if r := m.gitO.review; r != nil {
			if !r.seen {
				return m.showNoticeAs(noticeUnavailable, "Scroll to review all "+plural(r.total, "item")), true
			}
			m.gitO.review = nil
			m.setFocus("git-refresh")
		}
		dlg := m.gitO.op
		m.gitO.op = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		if g.oper == nil || !gitSameStop(*g.oper, dlg.state) {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(dlg.kind, "stale_operation")), m.refreshGit()), true
		}
		if reason := m.gitOpBlock(key, g, dlg.kind); reason != "" {
			return m.showNoticeAs(noticeUnavailable, reason), true
		}
		st := dlg.state
		m.clearGitWarning(key)
		switch dlg.kind {
		case protocol.GitKindOperationAbort:
			cmd := client.GitOperationAbortCommand(identity(), dlg.target, st,
				len(st.DiscardsOnAbort) > 0 || len(st.BackupMissingOnAbort) > 0, st.AbortDropsCount > 0)
			return m.sendGitWrite(key, cmd, st.Kind), true
		case protocol.GitKindOperationContinue:
			cmd := client.GitOperationContinueCommand(identity(), dlg.target, st, len(st.MarkerPaths) > 0)
			if st.MarkersIncomplete {
				cmd.Git.Operation.AcknowledgeMarkersIncomplete = true
			}
			if gitAgentChangesPending(st) {
				client.AcknowledgeAgentChanges(&cmd, st.AgentChanges)
			}
			return m.sendGitWrite(key, cmd, st.Kind), true
		case protocol.GitKindOperationSkip:
			cmd, ok := client.GitOperationSkipCommand(identity(), dlg.target, st, len(st.DiscardsOnSkip) > 0 || len(st.BackupMissingOnSkip) > 0)
			if !ok {
				return m.showNoticeAs(noticeUnavailable, gitRefCopy(dlg.kind, "not_stopped")), true
			}
			return m.sendGitWrite(key, cmd, st.Kind), true
		}
	}
	return nil, true
}

// gitOpBlock is why an operation command is unavailable now.
func (m *Model) gitOpBlock(key string, g *gitView, kind string) string {
	if reason := m.gitRefBlock(key, g, kind); reason != "" && reason != gitErrorCopy("operation_in_progress") && reason != gitErrorCopy("conflicted") {
		return reason
	}
	o := g.oper
	var can protocol.GitOperationAction
	switch kind {
	case protocol.GitKindOperationAbort:
		can = o.Can.Abort
	case protocol.GitKindOperationContinue:
		can = o.Can.Continue
	case protocol.GitKindOperationSkip:
		if o.Kind != protocol.GitOperationRebase {
			return "Skip is only for a rebase"
		}
		can = o.Can.Skip
	}
	if !can.Allowed {
		if r := safe(singleLine(can.Reason)); r != "" {
			return r
		}
		return "Not available now"
	}
	return ""
}

// startGitPreview reads the preview of a merge or rebase onto ref.
func (m *Model) startGitPreview(key string, target client.GitTarget, kind, ref string) tea.Cmd {
	api := m.gitOperationClient()
	if api == nil || ref == "" || (kind != protocol.GitOperationMerge && kind != protocol.GitOperationRebase) {
		return nil
	}
	m.gitO.previewSeq++
	seq := m.gitO.previewSeq
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return tea.Batch(m.showNoticeAs(noticeDone, "Checking "+kind+"…"), func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		p, err := api.GitIntegratePreview(deadline, target, kind, ref)
		return gitPreviewMsg{key: key, seq: seq, preview: p, err: err}
	})
}

func (m *Model) acceptGitPreview(msg gitPreviewMsg) tea.Cmd {
	current, target := m.gitTarget()
	g := m.gitViews[msg.key]
	if msg.seq != m.gitO.previewSeq || msg.key != current || g == nil || g.status == nil {
		return nil // stale: a newer preview or another target
	}
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Preview failed · "+safe(singleLine(msg.err.Error())))
	}
	dlg := &gitIntegrateDialog{key: msg.key, target: target, status: *g.status, preview: msg.preview}
	if msg.preview.HeadOid != "" && msg.preview.HeadOid != g.status.HeadOid {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindMerge, "stale_head")), m.refreshGit())
	}
	if len(m.menu) > 0 {
		m.gitO.pendingPreview = dlg
		return m.showNoticeAs(noticeUnavailable, "Review required · Git")
	}
	m.showGitIntegrateDialog(dlg)
	return nil
}

// gitPreviewLabel names the preview's target: label and short hash.
func gitPreviewLabel(p protocol.GitIntegratePreview) string {
	label := p.TargetLabel
	if label == "" {
		label = gitRefLabel(p.TargetRef)
	}
	return strings.TrimSpace(label + " " + gitShort(p.TargetOid))
}

// showGitIntegrateDialog paints the merge or rebase confirmation.
func (m *Model) showGitIntegrateDialog(dlg *gitIntegrateDialog) {
	p := dlg.preview
	m.gitO.integrate = dlg
	branch := safe(singleLine(p.Branch))
	if branch == "" {
		branch = gitHeadLabel(&dlg.status)
	}
	target := safe(singleLine(gitPreviewLabel(p)))
	merge := p.Kind == protocol.GitOperationMerge
	var items []menuItem
	note := func(s string) { items = append(items, menuItem{Note: s}) }
	title, verb := "Merge · ", "Merge "+target+" into "+branch
	published := false
	if merge {
		note("Merge " + target + " into " + branch + "?")
	} else {
		title, verb = "Rebase · ", "Rebase "+branch+" onto "+target
		note("Rebase " + branch + " onto " + target + "?")
	}
	if s := safe(singleLine(p.TargetSubject)); s != "" {
		note(truncateCells(gitShort(p.TargetOid)+" "+s, 60))
	}
	ref := p.TargetRef
	if ref == "" {
		ref = p.TargetOid
	}
	source := safe(singleLine(p.Source))
	if source != "" {
		source += " "
	}
	note("Target: " + source + truncatePathLeft(safe(singleLine(ref)), 50))
	blocked := p.Blocked
	if blocked == "" && p.RangeHasMerges && !merge {
		blocked = "range_has_merges"
	}
	switch {
	case p.UpToDate:
		note("Already up to date · nothing to do")
		blocked = "already_up_to_date"
	case blocked != "":
		kind := protocol.GitKindMerge
		if !merge {
			kind = protocol.GitKindRebase
		}
		note("Not possible now · " + gitRefCopy(kind, blocked))
		if msg := safe(singleLine(p.BlockedMessage)); msg != "" {
			note(msg)
		}
	case merge && p.FastForward && p.MergeFF != protocol.GitMergeNoFF:
		note("Fast-forward: " + branch + " moves to " + gitShort(p.TargetOid))
	case merge:
		line := "Creates a merge commit"
		if p.FastForward && p.MergeFF == protocol.GitMergeNoFF {
			line += " (merge.ff=no-ff)"
		}
		note(line)
	default:
		note("Replays " + plural(p.ReplayCount, "commit") + " · Git may drop ones already upstream")
		if p.Published {
			note("These commits are on a remote · rebasing rewrites published history")
			verb, published = "Rebase published commits", true
		}
	}
	if blocked == "" && !p.UpToDate {
		note("Conflicts are not predicted · resolve them here or abort")
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}})
	if blocked == "" && !p.UpToDate {
		a := action{Kind: "git-integrate-confirm"}
		if published {
			a.Value = "published"
		}
		items = append(items, menuItem{Label: verb, Action: a})
	}
	m.showMenuFor(title, branch, items)
	m.menuIndex = slices.IndexFunc(items, func(i menuItem) bool { return i.Label == "Cancel" })
}

// gitReviewSection is one acknowledged list of a confirmation.
type gitReviewSection struct {
	heading string
	items   []string
	// path counts the section as one reviewed path (the gate's diffs).
	path bool
}

// gitReviewPanel is the scrollable review that replaces a confirmation
// dialog whose acknowledged lists do not fit: it takes over the Git surface
// body, shows the question, every heading and every item (wrapped, never
// cut), and offers the confirm only once the end of the lists was on screen.
type gitReviewPanel struct {
	key      string
	title    string
	question []string
	sections []gitReviewSection
	verb     string
	confirm  action
	total    int
	seen     bool
}

// gitReviewSimpleMax bounds the lists a menu dialog may show in full.
const gitReviewSimpleMax, gitDialogPathWidth = 6, 52

// gitOpReviewSections are the acknowledged lists of an operation command.
func gitOpReviewSections(kind string, st protocol.GitOperationState) []gitReviewSection {
	var out []gitReviewSection
	add := func(heading string, items []string) {
		if len(items) > 0 {
			out = append(out, gitReviewSection{heading: heading, items: items})
		}
	}
	switch kind {
	case protocol.GitKindOperationAbort:
		add("Also resets changes to:", st.DiscardsOnAbort)
		add("Not backed up (overwritten without a copy):", st.BackupMissingOnAbort)
		if st.AbortDropsCount > 0 {
			var commits []string
			for _, c := range st.AbortDropsCommits {
				commits = append(commits, gitShort(c.Oid)+" "+safe(singleLine(c.Subject)))
			}
			if st.AbortDropsIncomplete {
				commits = append(commits, "and "+strconv.Itoa(st.AbortDropsCount-len(st.AbortDropsCommits))+" more")
			}
			add("Removes "+plural(st.AbortDropsCount, "commit")+" the "+safe(singleLine(st.Kind))+" already made:", commits)
		}
	case protocol.GitKindOperationSkip:
		add("Also resets changes to:", st.DiscardsOnSkip)
		add("Not backed up (overwritten without a copy):", st.BackupMissingOnSkip)
	case protocol.GitKindOperationContinue:
		add("Staged files still contain conflict markers:", st.MarkerPaths)
	}
	if kind == protocol.GitKindOperationContinue && gitAgentChangesPending(st) {
		ac := st.AgentChanges
		if ac.Incomplete {
			reason := "Continuing commits whatever the index holds now"
			if r := safe(singleLine(ac.Reason)); r != "" {
				reason = r + " · " + reason
			}
			out = append(out, gitReviewSection{heading: "Changes could not all be compared:", items: []string{reason}, path: true})
		}
		for _, it := range ac.Items {
			out = append(out, gitAgentChangeSection(it))
		}
	}
	return out
}

// gitAgentChangeSection is one gate item: its staged diff, and why it is
// not fully shown when it is not.
func gitAgentChangeSection(it protocol.GitResolveItem) gitReviewSection {
	heading := "Changed in the index since the agent started (not by your decisions): " + safe(singleLine(it.Path))
	diff := it.IndexDiff
	if diff == "" {
		diff = it.Diff
	}
	var lines []string
	shown := gitDiffLines(diff)
	for _, l := range shown {
		lines = append(lines, l.text)
	}
	if len(lines) == 0 {
		lines = []string{"(" + gitNoDiffReason(&it) + ")"}
	}
	if it.DiffTruncated {
		lines = append(lines, "(diff truncated by the server · not all of it is shown)")
	}
	if gitLinesCapped(shown) {
		lines = append(lines, "(display capped at 2000 lines, 4096 cells per line)")
	}
	return gitReviewSection{heading: heading, items: lines, path: true}
}

// gitAgentChangeHidden reports a gate item whose content is not fully shown.
func gitAgentChangeHidden(it protocol.GitResolveItem) bool {
	diff := it.IndexDiff
	if diff == "" {
		diff = it.Diff
	}
	return diff == "" || it.DiffTruncated || it.Unknown || it.Binary || gitLinesCapped(gitDiffLines(diff))
}

// gitReviewFits reports lists short enough for the menu dialog: few items,
// each shown whole.
func gitReviewFits(sections []gitReviewSection) bool {
	n := 0
	for _, sec := range sections {
		for _, it := range sec.items {
			n++
			if ansi.StringWidth(safe(singleLine(it))) > gitDialogPathWidth {
				return false
			}
		}
	}
	return n <= gitReviewSimpleMax
}

// openGitOpDialog confirms Continue, Skip or Abort, naming every path and
// commit its fingerprints cover: in the menu dialog when the lists fit,
// otherwise in the scrollable review panel.
func (m *Model) openGitOpDialog(dlg *gitOpDialog) {
	m.gitO.op = dlg
	st := dlg.state
	kind := safe(singleLine(st.Kind))
	branch := truncateCells(safe(singleLine(st.Branch)), 24)
	if branch == "" {
		branch = "detached HEAD"
	}
	var question []string
	title, verb := "", ""
	confirm := action{Kind: "git-op-confirm", Value: dlg.kind}
	switch dlg.kind {
	case protocol.GitKindOperationAbort:
		title, verb = "Abort "+kind+" · ", "Abort the "+kind
		back := "its state before the " + kind
		if st.OrigHead != "" {
			back = gitShort(st.OrigHead)
		}
		question = append(question, "Abort the "+kind+" and return "+branch+" to "+back+"?", "Resolution work in conflicted files is discarded")
		if len(st.DiscardsOnAbort) > 0 {
			question = append(question, "Overwritten files are backed up first")
		}
	case protocol.GitKindOperationSkip:
		short, subject := "", ""
		if cur := st.Current; cur != nil {
			short, subject = gitShort(cur.Oid), safe(singleLine(cur.Subject))
		}
		title, verb = "Skip commit · ", "Skip "+short
		question = append(question, truncateCells("Skip "+short+" "+subject, 60)+"?", "Drops this commit from the rebased "+branch+" and continues")
		if gitAgentChangesPending(st) {
			question = append(question, "Skip discards the changes staged for this commit, including the agent's")
		}
	case protocol.GitKindOperationContinue:
		title, verb = "Continue "+kind+" · ", "Continue the "+kind
		question = append(question, "Continue the "+kind+" on "+branch+"?", "Commits what is staged now, with the "+kind+"'s own message")
		if len(st.MarkerPaths) > 0 {
			verb, confirm.ID = "Continue with conflict markers", "markers"
		}
		if st.MarkersIncomplete {
			question = append(question, "Not every staged file could be checked for markers")
		}
		if gitAgentChangesPending(st) {
			var hidden []string
			for _, it := range st.AgentChanges.Items {
				if gitAgentChangeHidden(it) {
					hidden = append(hidden, safe(singleLine(it.Path)))
				}
			}
			if st.AgentChanges.Incomplete {
				hidden = append(hidden, "changes that could not be compared")
			}
			if len(hidden) > 0 {
				verb = "Acknowledge, including content not shown"
				question = append(question, "Not fully shown: "+strings.Join(hidden, ", "))
			}
		}
	}
	sections := gitOpReviewSections(dlg.kind, st)
	if !gitReviewFits(sections) || (dlg.kind == protocol.GitKindOperationContinue && gitAgentChangesPending(st)) {
		total := 0
		for _, sec := range sections {
			if sec.path {
				total++
			} else {
				total += len(sec.items)
			}
		}
		m.gitO.review = &gitReviewPanel{key: dlg.key, title: strings.TrimSuffix(title, " · "), question: question, sections: sections,
			verb: verb, confirm: confirm, total: total}
		m.menu = nil
		m.viewState().DetailScroll = 0
		m.setFocus("git:review-cancel")
		m.markDirty()
		return
	}
	var items []menuItem
	for _, q := range question {
		items = append(items, menuItem{Note: q})
	}
	for _, sec := range sections {
		items = append(items, menuItem{Note: sec.heading})
		for _, it := range sec.items {
			items = append(items, menuItem{Note: "  " + truncatePathLeft(safe(singleLine(it)), gitDialogPathWidth)})
		}
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}}, menuItem{Label: verb, Action: confirm})
	m.showMenuFor(title, branch, items)
	m.menuIndex = len(items) - 2
}

// gitReviewBlocks is the review panel's body.
func (m *Model) gitReviewBlocks(r *gitReviewPanel) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: "Review · " + r.title, value: plural(r.total, "item")}}
	for _, q := range r.question {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: q, ink: p.text})
	}
	b = append(b, button("Cancel", m.icon("close"), "git:review-cancel", action{Kind: "git-review-cancel"}), gap)
	for _, sec := range r.sections {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: sec.heading, ink: p.gold})
		for _, it := range sec.items {
			// Wrapped, never cut: two distinct paths never read the same.
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "  " + safe(singleLine(it)), ink: p.text})
		}
		b = append(b, gap)
	}
	b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "review-end", key: "", text: "End of the " + plural(r.total, "item")}})
	if r.seen {
		b = append(b, button(r.verb, m.icon("discard"), "git:review-confirm", r.confirm))
	} else {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Scroll to review all " + plural(r.total, "item"), ink: p.muted})
	}
	return append(b, button("Cancel", m.icon("close"), "git:review-cancel-end", action{Kind: "git-review-cancel"}))
}

// gitReviewSeen marks the review panel seen through once its end row is in
// the visible part of the surface body.
func (m *Model) gitReviewSeen() {
	r := m.gitO.review
	if r == nil || r.seen || !m.gitVisible() {
		return
	}
	f := m.measure()
	v := m.viewState()
	active, ok := v.Host.Active()
	if !ok || f.detail.H == 0 {
		return
	}
	rows := m.surfaceRows(m.surfaceBlocks(active), max(1, f.detail.W-1))
	scroll := min(max(0, v.DetailScroll), f.detailMax)
	for i, row := range rows {
		if row.git != nil && row.git.kind == "review-end" {
			if i >= scroll && i < scroll+f.detail.H {
				r.seen = true
				m.markDirty()
			}
			return
		}
	}
}

// gitOperationSettle opens a queued preview once no other menu is shown and
// drops confirmation state whose menu is gone.
func (m *Model) gitOperationSettle(key string) {
	if r := m.gitO.review; r != nil && r.key != key {
		m.gitO.review, m.gitO.op = nil, nil
	}
	m.gitReviewSeen()
	if v := m.gitCF.viewer; v != nil && v.key != key {
		m.gitCF.viewer = nil
	}
	if len(m.menu) != 0 {
		return
	}
	m.gitCF.dialog = nil
	m.gitO.integrate = nil
	if m.gitO.review == nil {
		m.gitO.op = nil
	}
	if p := m.gitO.pendingPreview; p != nil {
		m.gitO.pendingPreview = nil
		if p.key == key {
			m.showGitIntegrateDialog(p)
		}
	}
}

// gitShortCheckout names a checkout by its last two path components.
func gitShortCheckout(path string) string {
	path = strings.TrimRight(safe(singleLine(path)), "/")
	if path == "" {
		return "this checkout"
	}
	parts := strings.Split(path, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}
