package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_ref.go adds the ref and remote actions of ADR 0021 to the Git
// surface: fetch, fast-forward-only pull and push in the heading, switch and
// branch creation on branch rows, branch creation and soft reset on commit
// rows. Each is one durable command in the same per-target write slot as
// stage/commit (git_write.go), so Retry after a lost reply resends the same
// ID and the server never runs Git twice. A confirmation that the server
// asks for (leaves_commits, published_commit, not_ancestor) is a new command
// carrying the acknowledgement; nothing ran for the refused one.

// gitSyncAPI is the long-timeout half of the server client used for fetch,
// pull and push; tests inject a fake.
type gitSyncAPI interface {
	GitSync(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error)
}

var _ gitSyncAPI = (*client.Client)(nil)

// gitBranchNameKey is the focus key of the new-branch name input.
const gitBranchNameKey = "git:branch-name"

// gitRefUI is the client-local state of the ref and remote actions.
type gitRefUI struct {
	name  textarea.Model
	ready bool
	// create is the open new-branch editor; nameErr its validation line.
	create  *gitCreateDialog
	nameErr string
	// Dialog states, bound to what they showed; confirmations re-verify.
	carry *gitCarryDialog
	ack   *gitAckDialog
	reset *gitResetDialog
	push  *gitPushDialog
	// pending is an acknowledgement that arrived while another menu was
	// open; it opens once that menu closes, never replacing it.
	pending *gitAckDialog
}

// gitPushDialog confirms a push of the shown branch and HEAD.
type gitPushDialog struct {
	key    string
	target client.GitTarget
	status protocol.GitStatus
}

type gitCreateDialog struct {
	key      string
	target   client.GitTarget
	startOid string
	from     string // what the branch starts at, as shown (branch or short hash)
	origin   string // focus to restore on cancel
}

// gitCarryDialog asks before switching with changes: the status it showed
// supplies AcknowledgeCarry and the worktree fingerprint.
type gitCarryDialog struct {
	key    string
	target client.GitTarget
	status protocol.GitStatus
	// Existing-branch switch (branch, tip) or switch-create (name, start).
	branch, tip string
	name, start string
}

// gitAckDialog asks for an acknowledgement the server refused without:
// leaves_commits (count), published_commit or not_ancestor.
type gitAckDialog struct {
	key   string
	cmd   protocol.Command
	code  string
	count int
	label string
	undo  bool // resend keeps the undo marker
}

type gitResetDialog struct {
	key                 string
	target              client.GitTarget
	status              protocol.GitStatus
	oid, short, subject string
	count               int // commits leaving the branch, -1 when unknown
	branchLabel         string
}

type gitCancelMsg struct{ err error }

// gitRefsEnabled reports a server with the ref and remote actions (ADR 0021).
func (m *Model) gitRefsEnabled() bool {
	return m.gitWritesEnabled() && m.gitHistoryEnabled() && slices.Contains(m.snapshot.Capabilities, "git-refs")
}

// gitBranchInput returns the new-branch name input, creating it on first use.
func (m *Model) gitBranchInput() *textarea.Model {
	if !m.gitR.ready {
		m.gitR.name = newInput("Branch name")
		m.gitR.name.CharLimit = 255
		m.gitR.name.SetHeight(1)
		m.gitR.ready = true
	}
	return &m.gitR.name
}

// gitRefKind reports the ADR 0021 command kinds.
func gitRefKind(kind string) bool {
	if gitOperationKind(kind) || gitConflictKind(kind) || gitJobKind(kind) {
		return true
	}
	switch kind {
	case protocol.GitKindBranchCreate, protocol.GitKindSwitch, protocol.GitKindResetSoft,
		protocol.GitKindFetch, protocol.GitKindPull, protocol.GitKindPush:
		return true
	}
	return false
}

func gitSyncKind(kind string) bool {
	return kind == protocol.GitKindFetch || kind == protocol.GitKindPull || kind == protocol.GitKindPush
}

// gitLeaseKind reports the actions that hold the checkout writer lease and
// so wait while an agent turn holds it; fetch, push and branch creation run
// beside agent turns.
func gitLeaseKind(kind string) bool {
	return kind == protocol.GitKindSwitch || kind == protocol.GitKindResetSoft || kind == protocol.GitKindPull || gitOperationKind(kind) || gitConflictKind(kind)
}

// gitUpstreamRemote is the remote part of an upstream such as origin/main,
// for display only (fetch sends no remote and the server resolves it).
func gitUpstreamRemote(upstream string) string {
	remote, _, _ := strings.Cut(upstream, "/")
	return remote
}

func gitShort(oid string) string {
	return safe(singleLine(oid[:min(len(oid), 7)]))
}

// gitHeadLabel names what a HEAD-moving action affects.
func gitHeadLabel(s *protocol.GitStatus) string {
	if s != nil && s.Branch != "" {
		return safe(singleLine(s.Branch))
	}
	return "detached HEAD"
}

// gitRefBlock is why the action kind is unavailable now ("" when it can
// run). The server stays authoritative; this only explains and disables.
func (m *Model) gitRefBlock(key string, g *gitView, kind string) string {
	if !m.gitRefsEnabled() {
		return "Needs a newer server"
	}
	reason, _ := m.gitWriteBlock(key, g)
	if reason == gitLeaseCopy && !gitLeaseKind(kind) {
		reason = ""
	}
	if reason != "" {
		return reason
	}
	s := g.status
	switch kind {
	case protocol.GitKindFetch:
		if s.Upstream == "" {
			return "No upstream · nothing to fetch from"
		}
	case protocol.GitKindPull, protocol.GitKindPush:
		switch {
		case s.Workspace.State == "detached" || s.Branch == "":
			return gitRefCopy(kind, "detached")
		case s.Upstream == "":
			return gitRefCopy(kind, "no_upstream")
		case kind == protocol.GitKindPull && s.Operation != "":
			return gitErrorCopy("operation_in_progress")
		case kind == protocol.GitKindPush && s.Behind > 0:
			return gitRefCopy(kind, "behind_upstream")
		case kind == protocol.GitKindPush && s.HeadOid == "":
			return "Nothing to push on an unborn branch"
		}
	case protocol.GitKindMerge, protocol.GitKindRebase:
		switch {
		case s.Operation != "":
			return gitErrorCopy("operation_in_progress")
		case s.Workspace.State == "detached" || s.Branch == "":
			return gitRefCopy(kind, "detached")
		}
	case protocol.GitKindSwitch, protocol.GitKindResetSoft:
		if s.Operation != "" {
			return gitErrorCopy("operation_in_progress")
		}
		for _, e := range s.Entries {
			if e.Group == protocol.GitGroupConflicted {
				return gitErrorCopy("conflicted")
			}
		}
		if kind == protocol.GitKindResetSoft && s.HeadOid == "" {
			return gitRefCopy(kind, "nothing_to_reset")
		}
	}
	return ""
}

// dispatchGitRef sends a ref or remote command: fetch, pull and push, and
// switch and soft reset (open documents are flushed first, then Git has up
// to 5 minutes), use the 16 minute GitSync call when the client offers it.
func (m *Model) dispatchGitRef(key string, cmd protocol.Command) tea.Cmd {
	if gitOperationKind(cmd.Kind) {
		return m.gitLongWrite(key, cmd)
	}
	w := m.gitWriter()
	api, ok := w.(gitSyncAPI)
	long := gitSyncKind(cmd.Kind) || cmd.Kind == protocol.GitKindSwitch || cmd.Kind == protocol.GitKindResetSoft
	if !long || !ok {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.markDirty()
	return func() tea.Msg {
		r, err := api.GitSync(ctx, cmd)
		return gitWriteMsg{key: key, cmd: cmd, receipt: r, err: err}
	}
}

// gitRefAction handles every ref and remote control; ok reports that a was
// one of them.
func (m *Model) gitRefAction(a action) (tea.Cmd, bool) {
	key, target := m.gitTarget()
	g := m.gitViews[key]
	switch a.Kind {
	case "git-branch-name":
		return m.setFocus(gitBranchNameKey), true
	case "git-branch-cancel":
		return m.closeGitCreate(), true
	case "git-row-menu":
		return m.openGitContextMenu(a.ID), true
	case "git-sync-cancel":
		return m.cancelGitSync(a.ID), true
	case "git-ack-open":
		if st := m.gitWriteFor(key); st != nil && st.ack != nil && len(m.menu) == 0 {
			m.showGitAckDialog(st.ack)
		}
		return nil, true
	case "git-review":
		return m.gitReview(key), true
	case "git-fetch", "git-pull", "git-push", "git-push-confirm", "git-switch", "git-switch-carry", "git-ack-confirm",
		"git-branch-new", "git-branch-create", "git-branch-create-switch", "git-reset", "git-reset-confirm", "git-reset-undo":
	default:
		return nil, false
	}
	if g == nil || g.status == nil {
		return m.showNoticeAs(noticeUnavailable, "Git status not loaded"), true
	}
	kind := map[string]string{
		"git-fetch": protocol.GitKindFetch, "git-pull": protocol.GitKindPull, "git-push": protocol.GitKindPush, "git-push-confirm": protocol.GitKindPush,
		"git-switch": protocol.GitKindSwitch, "git-switch-carry": protocol.GitKindSwitch,
		"git-branch-new": protocol.GitKindBranchCreate, "git-branch-create": protocol.GitKindBranchCreate,
		"git-branch-create-switch": protocol.GitKindSwitch,
		"git-reset":                protocol.GitKindResetSoft, "git-reset-confirm": protocol.GitKindResetSoft, "git-reset-undo": protocol.GitKindResetSoft,
	}[a.Kind]
	if a.Kind == "git-ack-confirm" && m.gitR.ack != nil {
		kind = m.gitR.ack.cmd.Kind
	}
	if reason := m.gitRefBlock(key, g, kind); reason != "" {
		m.gitR.carry, m.gitR.ack, m.gitR.reset, m.gitR.push = nil, nil, nil, nil
		return m.showNoticeAs(noticeUnavailable, reason), true
	}
	s := *g.status
	switch a.Kind {
	case "git-fetch":
		m.clearGitWarning(key)
		return m.sendGitWrite(key, client.GitFetchCommand(identity(), target, ""), gitUpstreamRemote(s.Upstream)), true
	case "git-pull":
		m.clearGitWarning(key)
		return m.sendGitWrite(key, client.GitPullCommand(identity(), target, s), s.Upstream), true
	case "git-push":
		m.openGitPush(key, target, s)
		return nil, true
	case "git-push-confirm":
		dlg := m.gitR.push
		m.gitR.push = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		if s.HeadOid != dlg.status.HeadOid || s.Branch != dlg.status.Branch || s.Upstream != dlg.status.Upstream {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "stale_head")), m.refreshGit()), true
		}
		m.clearGitWarning(key)
		return m.sendGitWrite(key, client.GitPushCommand(identity(), target, dlg.status, ""), dlg.status.Upstream), true
	case "git-switch":
		br, ok := m.gitBranchByRef(g, a.ID)
		switch {
		case !ok || br.Remote:
			return m.showNoticeAs(noticeUnavailable, "Only local branches can be switched to here · create a branch from it"), true
		case br.Head:
			return m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "already_on_branch")), true
		case br.WorktreePath != "":
			return m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "checked_out_elsewhere")), true
		}
		return m.startGitSwitch(&gitCarryDialog{key: key, target: target, status: s, branch: br.Name, tip: br.Tip}), true
	case "git-switch-carry":
		dlg := m.gitR.carry
		m.gitR.carry = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		if protocol.GitWorktreeFingerprint(s) != protocol.GitWorktreeFingerprint(dlg.status) || s.HeadOid != dlg.status.HeadOid || s.Branch != dlg.status.Branch {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "carry_unacknowledged")), m.refreshGit()), true
		}
		return m.sendGitSwitch(dlg, true), true
	case "git-ack-confirm":
		dlg := m.gitR.ack
		m.gitR.ack = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		cmd := gitCloneRef(dlg.cmd)
		switch dlg.code {
		case "leaves_commits":
			cmd = client.GitAcknowledgeLeaveCommits(cmd, dlg.count)
		case "published_commit":
			cmd.Git.Ref.AcknowledgePublished = true
		case "not_ancestor":
			cmd.Git.Ref.AcknowledgeNotAncestor = true
		}
		cmd.ID = identity()
		m.clearGitWarning(key)
		send := m.sendGitWrite(key, cmd, dlg.label)
		m.gitW.writes[key].undo = dlg.undo
		return send, true
	case "git-branch-new":
		origin := m.focus
		if !strings.HasPrefix(origin, "git:branch:") && !strings.HasPrefix(origin, "git:commit:") {
			origin = "git-refresh"
		}
		m.gitR.create = &gitCreateDialog{key: key, target: target, startOid: a.ID, from: safe(singleLine(a.Value)), origin: origin}
		m.gitR.nameErr = ""
		m.gitBranchInput().SetValue("")
		m.markDirty()
		return m.setFocus(gitBranchNameKey), true
	case "git-branch-create", "git-branch-create-switch":
		dlg := m.gitR.create
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		name := strings.TrimSpace(m.gitBranchInput().Value())
		if msg := gitBranchNameProblem(name); msg != "" {
			m.gitR.nameErr = msg
			m.markDirty()
			return nil, true
		}
		m.gitR.create, m.gitR.nameErr = nil, ""
		m.gitBranchInput().SetValue("")
		focus := m.setFocus("git-refresh")
		if a.Kind == "git-branch-create" {
			m.clearGitWarning(key)
			return tea.Batch(focus, m.sendGitWrite(key, client.GitBranchCreateCommand(identity(), target, name, dlg.startOid), name)), true
		}
		return tea.Batch(focus, m.startGitSwitch(&gitCarryDialog{key: key, target: target, status: s, name: name, start: dlg.startOid})), true
	case "git-reset":
		return m.openGitReset(key, target, g, a.ID), true
	case "git-reset-confirm":
		dlg := m.gitR.reset
		m.gitR.reset = nil
		if dlg == nil || dlg.key != key {
			return nil, true
		}
		if s.HeadOid != dlg.status.HeadOid || s.Branch != dlg.status.Branch {
			return tea.Batch(m.showNoticeAs(noticeUnavailable, gitRefCopy(kind, "stale_head")), m.refreshGit()), true
		}
		m.clearGitWarning(key)
		cmd := client.GitResetSoftCommand(identity(), target, dlg.status, dlg.oid, false, false)
		return m.sendGitWrite(key, cmd, dlg.branchLabel+" to "+dlg.short), true
	case "git-reset-undo":
		st := m.gitWriteFor(key)
		if st == nil || st.result == nil || st.undo {
			return nil, true
		}
		cmd, ok := client.GitUndoResetSoftCommand(identity(), target, *st.result)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "Nothing to undo"), true
		}
		label := gitHeadLabel(&s) + " to " + gitShort(st.result.Ref.PreviousHead)
		m.clearGitWarning(key)
		send := m.sendGitWrite(key, cmd, label)
		m.gitW.writes[key].undo = true
		return send, true
	}
	return nil, true
}

// gitCloneRef copies a ref command deeply enough to change its
// acknowledgements.
func gitCloneRef(cmd protocol.Command) protocol.Command {
	if cmd.Git == nil || cmd.Git.Ref == nil {
		return cmd
	}
	w := *cmd.Git
	ref := *w.Ref
	w.Ref = &ref
	cmd.Git = &w
	return cmd
}

// gitBranchByRef finds a displayed branch row by its full ref.
func (m *Model) gitBranchByRef(g *gitView, ref string) (protocol.GitBranch, bool) {
	if g == nil || g.branches == nil {
		return protocol.GitBranch{}, false
	}
	for _, br := range g.branches.Branches {
		if br.Ref == ref {
			return br, true
		}
	}
	return protocol.GitBranch{}, false
}

// gitCommitByHash finds a displayed commit.
func (g *gitView) commit(hash string) (protocol.GitCommit, bool) {
	if g == nil || g.log == nil {
		return protocol.GitCommit{}, false
	}
	for _, c := range g.log.Commits {
		if c.Hash == hash {
			return c, true
		}
	}
	return protocol.GitCommit{}, false
}

// gitBranchNameProblem is a light client-side check; the server validates
// with Git's own rules.
func gitBranchNameProblem(name string) string {
	switch {
	case name == "":
		return "Enter a branch name"
	case strings.ContainsAny(name, " \t~^:?*[\\") || strings.Contains(name, ".."):
		return "Not a valid branch name · no spaces, .., ~ ^ : ? * [ or \\"
	case strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "."):
		return "Not a valid branch name"
	}
	return ""
}

// gitCarryCounts counts the shown entries by group.
func gitCarryCounts(s protocol.GitStatus) (staged, unstaged, untracked int) {
	for _, e := range s.Entries {
		switch e.Group {
		case protocol.GitGroupStaged:
			staged++
		case protocol.GitGroupUnstaged:
			unstaged++
		case protocol.GitGroupUntracked:
			untracked++
		}
	}
	return
}

// startGitSwitch sends a clean switch, or asks first when the shown status
// lists changes that Git would carry.
func (m *Model) startGitSwitch(dlg *gitCarryDialog) tea.Cmd {
	s := dlg.status
	if len(s.Entries) == 0 {
		return m.sendGitSwitch(dlg, false)
	}
	if fp := protocol.GitWorktreeFingerprint(s); fp == protocol.GitFingerprintTruncated || fp == protocol.GitFingerprintUnpinnable {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindSwitch, "status_truncated"))
	}
	m.gitR.carry = dlg
	name := dlg.branch
	if dlg.name != "" {
		name = dlg.name
	}
	name = safe(singleLine(name))
	staged, unstaged, untracked := gitCarryCounts(s)
	n := len(s.Entries)
	items := []menuItem{
		{Note: "Switch to " + name + " carrying " + plural(n, "change") + "?"},
		{Note: plural(staged, "staged") + " · " + plural(unstaged, "unstaged") + " · " + plural(untracked, "untracked")},
		{Note: "If one would be overwritten nothing changes"},
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Carry " + plural(n, "change") + " and switch", Action: action{Kind: "git-switch-carry"}},
	}
	m.showMenuFor("Switch branch · ", name, items)
	m.menuIndex = len(items) - 2
	return nil
}

// plural is "1 change", "3 changes"; words without a noun form ("staged")
// stay unchanged.
func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 && (word == "change" || word == "commit" || word == "file" || word == "item") {
		s += "s"
	}
	return s
}

func (m *Model) sendGitSwitch(dlg *gitCarryDialog, carry bool) tea.Cmd {
	m.clearGitWarning(dlg.key)
	if dlg.name != "" {
		cmd := client.GitSwitchCreateCommand(identity(), dlg.target, dlg.status, dlg.name, dlg.start, carry)
		return m.sendGitWrite(dlg.key, cmd, dlg.name)
	}
	cmd := client.GitSwitchCommand(identity(), dlg.target, dlg.status, dlg.branch, dlg.tip, carry)
	return m.sendGitWrite(dlg.key, cmd, dlg.branch)
}

// gitResetCount counts the commits a soft reset to oid leaves behind, from
// the loaded log: ancestors of HEAD that are not ancestors of oid. -1 when
// the log does not contain enough history to know.
func gitResetCount(g *gitView, head, oid string) int {
	if g.log == nil || g.log.Truncated {
		return -1
	}
	byHash := map[string]protocol.GitCommit{}
	for _, c := range g.log.Commits {
		byHash[c.Hash] = c
	}
	walk := func(start string) (map[string]bool, bool) {
		seen := map[string]bool{}
		stack := []string{start}
		for len(stack) > 0 {
			h := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[h] {
				continue
			}
			c, ok := byHash[h]
			if !ok {
				return nil, false
			}
			seen[h] = true
			stack = append(stack, c.Parents...)
		}
		return seen, true
	}
	from, ok1 := walk(head)
	to, ok2 := walk(oid)
	if !ok1 || !ok2 {
		return -1
	}
	n := 0
	for h := range from {
		if !to[h] {
			n++
		}
	}
	return n
}

// openGitReset opens the soft reset confirmation for a displayed commit.
func (m *Model) openGitReset(key string, target client.GitTarget, g *gitView, oid string) tea.Cmd {
	c, ok := g.commit(oid)
	if !ok {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "Commit changed since shown · refreshed"), m.refreshGit())
	}
	s := *g.status
	if c.Hash == s.HeadOid {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindResetSoft, "already_at_target"))
	}
	dlg := &gitResetDialog{key: key, target: target, status: s, oid: c.Hash, short: gitShort(c.Short), subject: safe(singleLine(c.Subject)),
		branchLabel: gitHeadLabel(&s), count: gitResetCount(g, s.HeadOid, c.Hash)}
	if !gitLogMatchesHead(g, s.HeadOid) {
		dlg.count = -1 // the log is from another HEAD: no number
	}
	m.gitR.reset = dlg
	leaving := "Commits after " + dlg.short + " leave " + dlg.branchLabel
	if dlg.count >= 0 {
		leaving = "About " + plural(dlg.count, "commit") + " leave " + dlg.branchLabel + " (from the loaded log)"
	}
	items := []menuItem{
		{Note: "Soft reset " + dlg.branchLabel + " to " + dlg.short + "?"},
		{Note: dlg.short + " " + dlg.subject},
		{Note: leaving},
		{Note: "Only the reflog keeps them unless another branch,"},
		{Note: "tag or remote contains them"},
		{Note: "Their changes stay staged; files are unchanged"},
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Soft reset " + dlg.branchLabel + " to " + dlg.short, Action: action{Kind: "git-reset-confirm"}},
	}
	m.showMenuFor("Soft reset · ", dlg.branchLabel, items)
	m.menuIndex = len(items) - 2
	return nil
}

// showGitAckDialog asks for the acknowledgement the server refused without.
func (m *Model) showGitAckDialog(dlg *gitAckDialog) {
	m.gitR.ack = dlg
	var items []menuItem
	title, verb := "Confirm · ", "Continue"
	switch dlg.code {
	case "leaves_commits":
		title, verb = "Leave commits · ", "Leave "+plural(dlg.count, "commit")+" and switch"
		items = []menuItem{{Note: "Leaving " + plural(dlg.count, "commit") + " reachable only from HEAD · they stay in the reflog"}}
	case "published_commit":
		title, verb = "Published commits · ", "Reset published commits"
		items = []menuItem{{Note: "These commits are on the remote"}, {Note: "They stay there; a later push needs a pull first"}}
	case "not_ancestor":
		title, verb = "Not an ancestor · ", "Reset to another history"
		items = []menuItem{{Note: "Target is not an ancestor of HEAD"}, {Note: "The branch moves to another history · reflog keeps it"}}
	}
	items = append(items, menuItem{Label: "Cancel", Action: action{Kind: "noop"}}, menuItem{Label: verb, Action: action{Kind: "git-ack-confirm"}})
	label := safe(singleLine(dlg.label))
	m.showMenuFor(title, label, items)
	m.menuIndex = len(items) - 2
}

// closeGitCreate closes the new-branch editor, keeping nothing.
func (m *Model) closeGitCreate() tea.Cmd {
	origin := "git-refresh"
	if m.gitR.create != nil && m.gitR.create.origin != "" {
		origin = m.gitR.create.origin
	}
	m.gitR.create, m.gitR.nameErr = nil, ""
	if m.gitR.ready {
		m.gitR.name.SetValue("")
	}
	m.markDirty()
	if m.focus == gitBranchNameKey || strings.HasPrefix(m.focus, "git:create-") {
		return m.setFocus(origin)
	}
	return nil
}

// cancelGitSync requests cancellation of a running fetch, pull or push.
func (m *Model) cancelGitSync(commandID string) tea.Cmd {
	api := m.gitWriter()
	if api == nil || commandID == "" {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := client.GitCancelCommand(identity(), commandID)
	return tea.Batch(m.showNoticeAs(noticeDone, "Cancel requested"), func() tea.Msg {
		_, err := api.GitWrite(ctx, cmd)
		return gitCancelMsg{err: err}
	})
}

func (m *Model) acceptGitCancel(msg gitCancelMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	var pe *protocol.Error
	if errors.As(msg.err, &pe) {
		return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindCancel, pe.Code))
	}
	return m.showNoticeAs(noticeError, "Cancel not delivered · "+safe(singleLine(msg.err.Error())))
}

// acceptGitRef applies a ref or remote command's reply.
func (m *Model) acceptGitRef(msg gitWriteMsg, st *gitWriteState) tea.Cmd {
	current, _ := m.gitTarget()
	refresh := func() tea.Cmd {
		if msg.key != current {
			return nil
		}
		m.gitShown = current
		return m.refreshGit()
	}
	kind := msg.cmd.Kind
	var pe *protocol.Error
	if errors.As(msg.err, &pe) {
		if pe.Code == "unknown_outcome_lookup" || pe.Code == "storage" {
			st.transport = gitErrorCopy(pe.Code)
			return refresh()
		}
		// Refused before anything ran: the command is dropped.
		delete(m.gitW.writes, msg.key)
		if c, ok := m.acceptConflictRefusal(msg.key, msg.cmd, pe); ok && msg.key == current {
			return c
		}
		var ack *gitAckDialog
		if n, ok := client.GitLeaveCommitsCount(msg.err); ok {
			ack = &gitAckDialog{key: msg.key, cmd: msg.cmd, code: "leaves_commits", count: n, label: st.label, undo: st.undo}
		} else if kind == protocol.GitKindResetSoft && (pe.Code == "published_commit" || pe.Code == "not_ancestor") {
			ack = &gitAckDialog{key: msg.key, cmd: msg.cmd, code: pe.Code, label: st.label, undo: st.undo}
		}
		if ack != nil {
			// Keep a result block at the source with an explicit action, so
			// the acknowledgement is never a dead end; open the dialog now
			// only when it replaces nothing.
			m.gitW.writes[msg.key] = &gitWriteState{cmd: msg.cmd, label: st.label, failure: gitRefCopy(kind, ack.code), ack: ack, undo: st.undo}
			if msg.key == current && len(m.menu) == 0 {
				m.showGitAckDialog(ack)
				return nil
			}
			if msg.key == current {
				m.gitR.pending = ack
			}
			return m.showNoticeAs(noticeUnavailable, "Review required · Git")
		}
		copyText := gitRefCopy(kind, pe.Code)
		if gitOperationKind(kind) && (pe.Code == "would_overwrite" || pe.Code == "discards_unacknowledged") {
			if msg := safe(singleLine(pe.Message)); msg != "" {
				copyText += " · " + msg
			}
		}
		notice := m.showNoticeAs(noticeUnavailable, copyText)
		if pe.Code == "review_pending" {
			// Refresh and show the review the gate asks for.
			return tea.Batch(notice, refresh(), m.refreshGitReview(msg.key, client.GitTarget{ThreadID: msg.cmd.ThreadID, ProjectID: msg.cmd.ProjectID}))
		}
		if gitStaleCode(pe.Code) || strings.HasPrefix(pe.Code, "stale_") || pe.Code == "carry_unacknowledged" || pe.Code == "behind_upstream" {
			return tea.Batch(notice, refresh())
		}
		return notice
	}
	if msg.err != nil {
		st.transport = "No reply from the server · " + safe(singleLine(msg.err.Error()))
		return nil
	}
	r := msg.receipt.Git
	if r == nil {
		st.failure, st.unknown = "Result unknown · refresh and check", true
		return refresh()
	}
	st.result = r
	st.output, st.truncated = r.Output, r.OutputTruncated
	if r.Code == "leaves_commits" || r.Code == "published_commit" || r.Code == "not_ancestor" {
		// A final result naming an acknowledgement: offer review with
		// fresh status rather than a dead-end line.
		st.review = true
	}
	switch r.State {
	case protocol.GitStateSucceeded:
		if r.Code != "" {
			st.warning = gitRefCopy(kind, r.Code)
		}
		done := m.gitRefDoneCopy(st, r)
		if kind == protocol.GitKindBranchCreate && r.Code == "" {
			delete(m.gitW.writes, msg.key)
		}
		return tea.Batch(m.showNoticeAs(noticeDone, done), refresh())
	case protocol.GitStateFailed:
		st.failure = gitRefCopy(kind, r.Code)
		if r.Code == "" {
			st.failure = "Git refused the change"
		}
	default:
		st.unknown = true
		st.failure = "Result unknown · refresh and check"
		switch r.Code {
		case "partial_switch", "partial_change":
			st.failure = gitRefCopy(kind, r.Code)
		case "cancelled", "timeout", "transport", "git_failed":
			if gitOperationKind(kind) && (r.Code == "timeout" || r.Code == "git_failed") {
				st.failure = gitRefCopy(kind, "timeout")
				if r.Code == "git_failed" {
					st.failure = "Git failed · result unknown, refresh and check"
				}
				if msg := safe(singleLine(r.Message)); msg != "" {
					st.failure += " · " + msg
				}
			} else if kind == protocol.GitKindPush && r.Code != "git_failed" {
				st.failure = gitRefCopy(kind, r.Code) + " · the remote may have accepted it; refresh and check"
			}
		}
	}
	return refresh()
}

// gitRefDoneCopy is the success notice.
func (m *Model) gitRefDoneCopy(st *gitWriteState, r *protocol.GitResult) string {
	label := safe(singleLine(st.label))
	switch st.cmd.Kind {
	case protocol.GitKindBranchCreate:
		return "Created branch " + label + " at " + gitShort(st.cmd.Git.Ref.StartOid)
	case protocol.GitKindSwitch:
		if r.Ref != nil && r.Ref.Branch != "" {
			return "Switched to " + safe(singleLine(r.Ref.Branch))
		}
		return "Switched to " + label
	case protocol.GitKindResetSoft:
		return "Soft reset " + label
	}
	if gitOperationKind(st.cmd.Kind) {
		return gitOperationDoneCopy(st, r)
	}
	if gitConflictKind(st.cmd.Kind) {
		return gitConflictDone(st)
	}
	switch st.cmd.Kind {
	case protocol.GitKindResolveJobStart:
		return "Resolution job started"
	case protocol.GitKindResolveJobFollowup:
		return "Follow-up sent to the agent"
	case protocol.GitKindResolveJobCancel:
		return "Stop requested"
	case protocol.GitKindResolveJobEnd:
		return "Resolution job ended"
	}
	return m.gitSyncSummary(st, r)
}

// gitSyncSummary is the one-line outcome of a fetch, pull or push.
func (m *Model) gitSyncSummary(st *gitWriteState, r *protocol.GitResult) string {
	sync := st.cmd.Git.Sync
	switch st.cmd.Kind {
	case protocol.GitKindFetch:
		remote := safe(singleLine(st.label))
		if r.Fetch != nil && r.Fetch.Remote != "" {
			remote = safe(singleLine(r.Fetch.Remote))
		}
		return "Fetched " + remote
	case protocol.GitKindPull:
		branch := safe(singleLine(sync.ExpectedBranch))
		if r.Integration == nil {
			return "Pulled"
		}
		switch r.Integration.State {
		case protocol.GitIntegrationFastForward:
			return "Fast-forwarded " + branch + " to " + gitShort(r.Integration.To)
		case protocol.GitIntegrationUpToDate:
			return "Up to date"
		case protocol.GitIntegrationAhead:
			return "Ahead of upstream"
		}
		return "Pulled"
	case protocol.GitKindPush:
		up := safe(singleLine(sync.Upstream))
		if r.Push != nil && r.Push.State == protocol.GitPushUpToDate {
			return "Already pushed · " + up + " up to date"
		}
		return "Pushed " + safe(singleLine(sync.ExpectedBranch)) + " → " + up
	}
	return "Done"
}

// gitRefCopy is the functional copy for ADR 0021 codes, falling back to the
// write copy.
func gitRefCopy(kind, code string) string {
	switch code {
	case "branch_exists":
		return "A branch with that name already exists"
	case "unknown_commit":
		return "Commit not found here · refresh"
	case "stale_branch":
		return "Checked-out branch changed since shown · status refreshed"
	case "stale_head":
		return "HEAD moved since shown · nothing changed"
	case "stale_target":
		return "Branch tip moved since shown · refreshed"
	case "stale_upstream":
		return "Upstream moved since shown · refreshed"
	case "stale_status", "carry_unacknowledged":
		return "Changes differ from shown · review and switch again"
	case "status_truncated":
		return "Too many changes to review here · switch from a terminal"
	case "checked_out_elsewhere":
		return "Branch is checked out in another worktree"
	case "already_on_branch":
		return "Already on this branch"
	case "nothing_to_reset":
		return "Nothing to reset on an unborn branch"
	case "already_at_target":
		return "HEAD is already at this commit"
	case "published_commit":
		if kind == protocol.GitKindRebase {
			return "The rebase would rewrite published commits · review the preview again"
		}
		return "These commits are on the remote · review to reset anyway"
	case "not_ancestor":
		return "Target is not an ancestor of HEAD · review to reset anyway"
	case "detached":
		return "Detached HEAD · switch to a branch first"
	case "no_upstream":
		return "No upstream configured for this branch"
	case "upstream_gone":
		return "Upstream branch no longer exists on the remote"
	case "unknown_remote":
		return "Remote is not configured"
	case "behind_upstream":
		return "Behind upstream · Pull first"
	case "diverged":
		return "Diverged from upstream · merge or rebase explicitly"
	case "not_running":
		return "Nothing running to cancel"
	case "not_cancellable":
		return "Past the cancellable phase · wait for it to finish"
	case "leaves_commits":
		return "Switching leaves commits reachable only from HEAD · review to switch anyway"
	case "upstream_name_mismatch":
		return "Upstream has another name · push.default only pushes to a same-named branch; set push.default=upstream to push there"
	case "would_overwrite":
		return "Local files would be overwritten · nothing changed"
	case "partial_switch":
		return "Git changed files but did not switch; review status"
	case "auth_required":
		return "The remote needs credentials no helper or agent supplied"
	case "host_key_unknown":
		return "The remote's SSH host key is not trusted"
	case "agent_unavailable":
		return "ssh-agent is unavailable or refused to sign"
	case "signing_failed":
		return "Push signing failed · nothing was sent"
	case "transport":
		return "Could not reach the remote"
	case "timeout":
		if gitOperationKind(kind) {
			return "Git exceeded its time budget · result unknown, refresh and check"
		}
		return "Git stopped · no output for too long or the time budget ended"
	case "cancelled":
		if gitSyncKind(kind) {
			return "Cancelled"
		}
	case "rejected":
		return "Push rejected by the remote"
	case "document_unsaved":
		return "An open document could not be saved · Git did not run"
	case "hook_failed":
		if gitOperationKind(kind) {
			return "Finished, but a hook after it failed · see output"
		}
		return "Switched, but the post-checkout hook failed · see output"
	case "pushed_newer_head":
		return "Pushed a newer commit than shown · the branch moved"
	case "not_supported":
		if gitOperationKind(kind) {
			return "Not supported here · interactive stop, hidden index entries, nested repositories or too many paths; use a terminal"
		}
		if gitRefKind(kind) {
			return "Not supported here · local upstream, other push remote, push.default=nothing, mirror or unsafe name"
		}
	case "operation_in_progress":
		return "Finish or abort the merge, rebase, cherry-pick, revert or bisect first"
	case "job_running":
		return "The agent is working · stop it first"
	case "job_exists":
		return "A resolution job is already attached · end it first"
	case "no_job":
		return "No resolution job is attached · refreshed"
	case "job_thread":
		return "The job thread takes no ordinary commands"
	case "review_pending":
		return "The agent's staged changes need review · review them and try again"
	case "binary_unacknowledged":
		return "The file is binary · review it and confirm staging it as it is"
	case "unsaved_unacknowledged":
		return "The file cannot be copied first · confirm overwriting it without a copy"
	case "not_conflicted":
		return "The path is no longer unmerged · refreshed"
	case "no_saved_copy":
		return "No saved copy of this stop holds the path"
	case "dirty_tree":
		return "Commit or discard changes first · merge and rebase need a clean tracked tree (untracked files are fine)"
	case "unborn":
		return "No commits on this branch yet"
	case "already_up_to_date":
		return "Already up to date · nothing to do"
	case "stale_range":
		return "Commits to replay changed since shown · review again"
	case "range_has_merges":
		return "The rebase range contains merge commits · rebase in a terminal"
	case "ff_only_configured":
		return "merge.ff=only and this is not a fast forward · rebase instead or merge in a terminal"
	case "no_operation":
		return "No merge or rebase in progress · refreshed"
	case "stale_operation":
		return "The operation moved on since shown · refreshed, review again"
	case "not_stopped":
		return "The rebase is not stopped at a commit · nothing to skip"
	case "discards_unacknowledged":
		return "Files the command resets differ from shown · review again"
	case "markers_unacknowledged":
		return "Staged conflict markers differ from shown · review again"
	case "markers_incomplete":
		return "Not every staged file was checked for markers · review again"
	case "drops_unacknowledged":
		return "Commits the abort removes differ from shown · review again"
	case "backup_incomplete":
		return "Not every overwritten file can be backed up · review again; nothing changed"
	case "stopped_conflicts":
		return "Stopped with conflicts · resolve and stage them, then Continue"
	case "stopped":
		return "Stopped without conflicts · review, then Continue or Abort"
	case "nothing_to_commit":
		return "Nothing to commit for this step · Skip it (rebase) or finish in a terminal"
	case "partial_change":
		return "Git failed but files or the index changed · review status"
	case "abort_incomplete":
		return "Aborted, but some restored files still differ · review them"
	case "documents_changed_tree":
		return "Saving open documents changed tracked files · commit or discard them, then start again"
	}
	return gitErrorCopy(code)
}

// gitCredentialCode reports failures the user fixes outside the app.
func gitCredentialCode(code string) bool {
	return code == "auth_required" || code == "host_key_unknown" || code == "agent_unavailable"
}

const gitCredentialAdvice = "Run `git fetch` once in a terminal to trust the host or unlock the key"

// gitRefKey handles the surface keys of the ref and remote actions while a
// Git row or control has focus: f fetch, p pull, P push, S switch, b new
// branch, r soft reset, y copy hash.
func (m *Model) gitRefKey(s string) (tea.Cmd, bool) {
	if cmd, ok := m.gitConflictKey(s); ok {
		return cmd, true
	}
	if cmd, ok := m.gitJobKey(s); ok {
		return cmd, true
	}
	if cmd, ok := m.gitViewerTabKey(s); ok {
		return cmd, true
	}
	if m.gitO.review != nil && strings.HasPrefix(m.focus, "git:review") {
		switch s {
		case "pgdown", "pgup", "home", "end", "up", "down":
			// Scrolling the review moves focus to the surface body so it
			// stays valid while the buttons scroll out of view.
			m.setFocus("right-body")
			return nil, false
		}
	}
	focus := m.focus
	if rest, ok := strings.CutPrefix(focus, "git-bact:"); ok {
		_, ref, _ := strings.Cut(rest, ":")
		focus = "git:branch:" + ref
	}
	scoped := focus == "git-refresh" || strings.HasPrefix(focus, "git:sync:") || strings.HasPrefix(focus, "git:branch:") || strings.HasPrefix(focus, "git:commit:")
	if !scoped || !m.gitRefsEnabled() {
		return nil, false
	}
	switch s {
	case "f":
		return m.activate(action{Kind: "git-fetch"}), true
	case "p":
		return m.activate(action{Kind: "git-pull"}), true
	case "P", "shift+p":
		return m.activate(action{Kind: "git-push"}), true
	}
	g := m.currentGitView()
	if ref, ok := strings.CutPrefix(focus, "git:branch:"); ok {
		br, found := m.gitBranchByRef(g, ref)
		if !found {
			return nil, false
		}
		switch s {
		case "S", "shift+s":
			return m.activate(action{Kind: "git-switch", ID: br.Ref}), true
		case "b":
			return m.activate(action{Kind: "git-branch-new", ID: br.Tip, Value: br.Name}), true
		}
		return nil, false
	}
	if hash, ok := strings.CutPrefix(focus, "git:commit:"); ok {
		c, found := g.commit(hash)
		if !found {
			return nil, false
		}
		switch s {
		case "b":
			return m.activate(action{Kind: "git-branch-new", ID: c.Hash, Value: c.Short}), true
		case "r":
			return m.activate(action{Kind: "git-reset", ID: c.Hash}), true
		case "y":
			return m.activate(action{Kind: "context-copy", Value: c.Hash}), true
		}
	}
	return nil, false
}

// openGitContextMenu opens the context menu of a branch or commit row. Every
// item names the commit and the branch or HEAD it affects.
func (m *Model) openGitContextMenu(focus string) tea.Cmd {
	if strings.HasPrefix(focus, "git:conflict:") || strings.HasPrefix(focus, "git-cact:") {
		return m.openGitConflictMenu(focus)
	}
	if rest, ok := strings.CutPrefix(focus, "git-bact:"); ok {
		_, ref, _ := strings.Cut(rest, ":")
		focus = "git:branch:" + ref
	}
	if len(m.menu) != 0 || !m.gitRefsEnabled() {
		return nil
	}
	g := m.currentGitView()
	if g == nil || g.status == nil {
		return nil
	}
	head := gitHeadLabel(g.status)
	var items []menuItem
	var title, user string
	if ref, ok := strings.CutPrefix(focus, "git:branch:"); ok {
		br, found := m.gitBranchByRef(g, ref)
		if !found {
			return nil
		}
		name := safe(singleLine(br.Name))
		title, user = "Branch · ", name
		if !br.Remote && !br.Head {
			items = append(items, menuItem{Label: "Switch to " + name + " (S)", Action: action{Kind: "git-switch", ID: br.Ref}})
		}
		if m.gitOperationsEnabled() && !br.Head && g.status.Branch != "" {
			cur := safe(singleLine(g.status.Branch))
			items = append(items,
				menuItem{Label: "Merge " + name + " into " + cur + "…", Action: action{Kind: "git-integrate", Value: protocol.GitOperationMerge, ID: br.Ref}},
				menuItem{Label: "Rebase " + cur + " onto " + name + "…", Action: action{Kind: "git-integrate", Value: protocol.GitOperationRebase, ID: br.Ref}})
		}
		items = append(items,
			menuItem{Label: "Create branch from " + name + " " + gitShort(br.Tip) + "… (b)", Action: action{Kind: "git-branch-new", ID: br.Tip, Value: br.Name}},
			menuItem{Label: "Compare " + name + " with HEAD (Enter)", Action: action{Kind: "git-compare", ID: br.Ref, Value: br.Name}})
	} else if hash, ok := strings.CutPrefix(focus, "git:commit:"); ok {
		c, found := g.commit(hash)
		if !found {
			return nil
		}
		short := gitShort(c.Short)
		title, user = "Commit · ", short
		items = append(items,
			menuItem{Label: "Create branch at " + short + "… (b)", Action: action{Kind: "git-branch-new", ID: c.Hash, Value: c.Short}})
		if c.Hash != g.status.HeadOid {
			items = append(items, menuItem{Label: "Soft reset " + head + " to " + short + "… (r)", Action: action{Kind: "git-reset", ID: c.Hash}})
		}
		if m.gitOperationsEnabled() && c.Hash != g.status.HeadOid && g.status.Branch != "" {
			cur := safe(singleLine(g.status.Branch))
			items = append(items,
				menuItem{Label: "Merge " + short + " into " + cur + "…", Action: action{Kind: "git-integrate", Value: protocol.GitOperationMerge, ID: c.Hash}},
				menuItem{Label: "Rebase " + cur + " onto " + short + "…", Action: action{Kind: "git-integrate", Value: protocol.GitOperationRebase, ID: c.Hash}})
		}
		items = append(items, menuItem{Label: "Copy hash " + short + " (y)", Action: action{Kind: "context-copy", Value: c.Hash}})
	} else {
		return nil
	}
	var cmd tea.Cmd
	if m.focus != focus {
		cmd = m.setFocus(focus)
	}
	m.showMenuFor(title, user, items)
	m.contextMenu = &contextMenuState{returnFocus: focus}
	return cmd
}

// openGitContextMenuAt opens a branch or commit row's menu under the
// pointer; ok reports that the point was on such a row.
func (m *Model) openGitContextMenuAt(f frame, x, y int) (tea.Cmd, bool) {
	for i := len(f.hits) - 1; i >= 0; i-- {
		h := f.hits[i]
		if !h.Rect.Contains(x, y) {
			continue
		}
		if strings.HasPrefix(h.Key, "git:branch:") || strings.HasPrefix(h.Key, "git:commit:") || strings.HasPrefix(h.Key, "git:conflict:") || strings.HasPrefix(h.Key, "git-cact:") {
			return m.openGitContextMenu(h.Key), true
		}
		if ref, ok := strings.CutPrefix(h.Key, "git-bact:"); ok {
			_, ref, _ = strings.Cut(ref, ":")
			return m.openGitContextMenu("git:branch:" + ref), true
		}
	}
	return nil, false
}

// gitBranchNameKeyPress edits the new branch name: Enter creates, Escape
// cancels; newlines are never inserted.
func (m *Model) gitBranchNameKeyPress(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		return m.activate(action{Kind: "git-branch-create"})
	case "esc":
		return m.closeGitCreate()
	case "shift+enter", "ctrl+j", "tab", "shift+tab", "up", "down":
		return nil
	}
	c := updateInput(m.gitBranchInput(), k)
	m.gitR.nameErr = ""
	m.markDirty()
	return c
}

// gitBranchNamePaste inserts the first line of pasted text.
func (m *Model) gitBranchNamePaste(content string) tea.Cmd {
	c := updateInput(m.gitBranchInput(), tea.PasteMsg{Content: singleLine(safe(content))})
	m.markDirty()
	return c
}

// openGitPush confirms a push: the branch, its commit count and the
// upstream it goes to, with Cancel focused.
func (m *Model) openGitPush(key string, target client.GitTarget, s protocol.GitStatus) {
	m.gitR.push = &gitPushDialog{key: key, target: target, status: s}
	branch, up := safe(singleLine(s.Branch)), safe(singleLine(s.Upstream))
	items := []menuItem{
		{Note: "Push " + branch + " (" + plural(s.Ahead, "commit") + ") to " + up + "?"},
		{Note: "Never forced · no tags · the pre-push hook runs"},
		{Label: "Cancel", Action: action{Kind: "noop"}},
		{Label: "Push " + branch + " to " + up, Action: action{Kind: "git-push-confirm"}},
	}
	m.showMenuFor("Push · ", branch, items)
	m.menuIndex = len(items) - 2
}

// gitLogMatchesHead reports that the loaded log was read at the shown HEAD.
func gitLogMatchesHead(g *gitView, head string) bool {
	if g.log == nil || len(g.log.Commits) == 0 || head == "" {
		return false
	}
	for _, c := range g.log.Commits {
		if c.Hash == head {
			return slices.Contains(c.Refs, "HEAD")
		}
	}
	return false
}

// gitReview reopens a switch or reset flow whose acknowledgement could not
// be asked in place, from the fresh status.
func (m *Model) gitReview(key string) tea.Cmd {
	st := m.gitWriteFor(key)
	if st == nil || st.cmd.Git == nil || st.cmd.Git.Ref == nil {
		return nil
	}
	ref := *st.cmd.Git.Ref
	kind := st.cmd.Kind
	delete(m.gitW.writes, key)
	if kind == protocol.GitKindResetSoft {
		return m.activate(action{Kind: "git-reset", ID: ref.TargetOid})
	}
	if ref.Name != "" {
		_, target := m.gitTarget()
		g := m.gitViews[key]
		if g == nil || g.status == nil {
			return nil
		}
		return m.startGitSwitch(&gitCarryDialog{key: key, target: target, status: *g.status, name: ref.Name, start: ref.StartOid})
	}
	return m.activate(action{Kind: "git-switch", ID: "refs/heads/" + ref.Branch})
}

// gitDialogsSettle runs after every update: a queued acknowledgement opens
// once no other menu is shown, dialog state is dropped once its menu is
// gone, and the new-branch editor closes when its target is not shown.
func (m *Model) gitDialogsSettle(key string) {
	if len(m.menu) != 0 {
		return
	}
	m.gitR.carry, m.gitR.ack, m.gitR.reset, m.gitR.push = nil, nil, nil, nil
	if p := m.gitR.pending; p != nil {
		m.gitR.pending = nil
		if p.key == key {
			m.showGitAckDialog(p)
		}
	}
	if c := m.gitR.create; c != nil && c.key != key {
		m.gitR.create, m.gitR.nameErr = nil, ""
		if m.gitR.ready {
			m.gitR.name.SetValue("")
		}
		if m.focus == gitBranchNameKey || strings.HasPrefix(m.focus, "git:create-") {
			m.setFocus("git-refresh")
		}
	}
}
