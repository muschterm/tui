package tui

import (
	"context"
	"errors"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_write.go adds the Git write actions of ADR 0020 to the Git surface:
// per-entry stage, unstage and discard, and a commit composer with amend.
// Every write is one durable command sent as a tea.Cmd; the command is built
// once and kept, so Retry after a lost reply resends the same ID and the
// server never runs Git twice. Refusals (nothing recorded) clear the command:
// a corrected attempt is a new command. Writes are advisory-gated here and
// authoritatively refused by the server (checkout_busy, git_busy, stale_*).

// gitWriteAPI is the write half of the server client; tests inject a fake.
type gitWriteAPI interface {
	GitWrite(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error)
}

var _ gitWriteAPI = (*client.Client)(nil)

// gitMessageKey is the focus key of the commit message editor.
const gitMessageKey = "git:message"

// gitWriteUI is the client-local Git write state.
type gitWriteUI struct {
	ready   bool
	message textarea.Model
	// width is the message editor's last painted width, used to size its
	// rows before the next paint.
	width int
	// drafts are commit drafts per Git target key; draftKey is the target
	// whose draft is loaded into message.
	drafts   map[string]*gitDraft
	draftKey string
	// writes is the latest local write per target key.
	writes map[string]*gitWriteState
	// discard is the open discard confirmation.
	discard *gitDiscardDialog
	// ops remembers the last seen state of each snapshot GitOp, so another
	// client's finished write refreshes the displayed status once.
	ops map[string]string
}

type gitDraft struct {
	message string
	amend   bool
	// amendHead is the HEAD the amend (and any prefilled message or
	// published confirmation) was prepared for; headChanged reports that
	// status later showed another HEAD and amend was turned off.
	amendHead   string
	headChanged bool
}

type gitWriteState struct {
	cmd     protocol.Command
	label   string // success subject: the path or "commit"
	running bool
	// transport is set when no reply arrived; Retry resends cmd unchanged.
	transport string
	// failure is the red line at the source; unknown marks outcome_unknown.
	failure string
	unknown bool
	// warning is a succeeded write's warning, shown until the next action.
	warning string
	// output is Git's and the hooks' combined output (unsanitized; the
	// viewer sanitizes it).
	output    string
	truncated bool
	// result is a ref or remote action's final result (git_ref.go), shown
	// until the next action.
	result *protocol.GitResult
	// undo marks a soft reset that undid another; it offers no Undo.
	undo bool
	// ack is an acknowledgement the server asked for that is not shown yet;
	// review offers the flow again with fresh status.
	ack    *gitAckDialog
	review bool
}

type gitDiscardDialog struct {
	key     string
	target  client.GitTarget
	entry   protocol.GitStatusEntry
	changed bool
}

type gitWriteMsg struct {
	key     string
	cmd     protocol.Command
	receipt protocol.Receipt
	err     error
}

type gitHeadMsg struct {
	key, head, message string
	err                error
}

func (m *Model) gitWriter() gitWriteAPI {
	if w, ok := m.gitReads.(gitWriteAPI); ok {
		return w
	}
	if m.gitReads == nil && m.client != nil {
		return m.client
	}
	return nil
}

// gitMsg returns the commit message editor, creating it on first use.
func (m *Model) gitMsg() *textarea.Model {
	if !m.gitW.ready {
		m.gitW.message = newInput("Commit message")
		m.gitW.message.CharLimit = 16000
		m.gitW.ready = true
	}
	return &m.gitW.message
}

// gitWritesEnabled reports whether the server offers Git writes.
func (m *Model) gitWritesEnabled() bool {
	return slices.Contains(m.snapshot.Capabilities, "git-writes") && m.gitWriter() != nil
}

// syncGitDraft keeps the message editor on the displayed target's draft:
// the previous target's text is stored and the new one's loaded.
func (m *Model) syncGitDraft(key string) {
	if key == m.gitW.draftKey {
		return
	}
	a := m.gitMsg()
	if m.gitW.draftKey != "" {
		m.gitDraftFor(m.gitW.draftKey).message = a.Value()
	}
	m.gitW.draftKey = key
	a.SetValue(m.gitDraftFor(key).message)
}

func (m *Model) gitDraftFor(key string) *gitDraft {
	if m.gitW.drafts == nil {
		m.gitW.drafts = map[string]*gitDraft{}
	}
	d := m.gitW.drafts[key]
	if d == nil {
		d = &gitDraft{}
		m.gitW.drafts[key] = d
	}
	return d
}

func (m *Model) gitWriteFor(key string) *gitWriteState {
	return m.gitW.writes[key]
}

// gitRepoOverlap reports whether two checkout paths are in one repository
// tree: equal, or one inside the other.
func gitRepoOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// gitRepoRoot is the best client-side approximation of the displayed
// repository's toplevel: a snapshot GitOp's toplevel containing the
// checkout, else the checkout itself.
func (m *Model) gitRepoRoot(path string) string {
	root := path
	for _, op := range m.snapshot.GitOps {
		if gitRepoOverlap(op.Checkout, path) && len(op.Checkout) < len(root) {
			root = op.Checkout
		}
	}
	return root
}

// gitRunningOp is a running snapshot GitOp for the displayed repository.
func (m *Model) gitRunningOp(path string) *protocol.GitOp {
	for i, op := range m.snapshot.GitOps {
		if op.State == protocol.GitStateRunning && gitRepoOverlap(op.Checkout, path) {
			return &m.snapshot.GitOps[i]
		}
	}
	return nil
}

// gitLeaseHeld reports a thread turn holding (or queued behind) a writer
// lease in the displayed repository. The server remains authoritative.
func (m *Model) gitLeaseHeld(path string) bool {
	root := m.gitRepoRoot(path)
	for _, t := range m.snapshot.Threads {
		if t.Closed || !activeTurn(t) {
			continue
		}
		if gitRepoOverlap(t.Checkout, root) || gitRepoOverlap(t.Checkout, path) {
			return true
		}
	}
	return false
}

func gitProgressVerb(op string) string {
	switch op {
	case "stage", protocol.GitKindStage:
		return "Staging…"
	case "unstage", protocol.GitKindUnstage:
		return "Unstaging…"
	case "discard", protocol.GitKindDiscard:
		return "Discarding…"
	case "commit", protocol.GitKindCommit:
		return "Committing…"
	case "fetch", protocol.GitKindFetch:
		return "Fetching…"
	case "pull", protocol.GitKindPull:
		return "Pulling…"
	case "push", protocol.GitKindPush:
		return "Pushing…"
	case "switch", protocol.GitKindSwitch:
		return "Switching…"
	case "reset_soft", protocol.GitKindResetSoft:
		return "Resetting…"
	case "branch_create", protocol.GitKindBranchCreate:
		return "Creating branch…"
	}
	return "Git write running…"
}

// gitPendingCopy blocks new writes while an earlier write's outcome is
// unknown and its command is kept for Retry.
const gitPendingCopy = "Previous write has no reply · Retry or Refresh"

const gitLeaseCopy = "Agent turn running in this checkout · Git writes wait"

// gitWriteBlock is why writes are inert right now ("" when they can run),
// and whether that reason is progress rather than a wait.
func (m *Model) gitWriteBlock(key string, g *gitView) (string, bool) {
	if !m.connected {
		return "Connect to the server to change Git state", false
	}
	if g == nil || g.status == nil {
		return "Git status not loaded", false
	}
	switch g.status.Workspace.State {
	case "branch", "detached", "unborn":
	default:
		return "No Git repository", false
	}
	if st := m.gitWriteFor(key); st != nil && st.running {
		return gitProgressVerb(st.cmd.Kind), true
	}
	if st := m.gitWriteFor(key); st != nil && st.transport != "" {
		return gitPendingCopy, false
	}
	if op := m.gitRunningOp(g.status.Workspace.Path); op != nil {
		return gitProgressVerb(op.Op), true
	}
	if m.gitLeaseHeld(g.status.Workspace.Path) {
		return gitLeaseCopy, false
	}
	return "", false
}

// gitErrorCopy is the functional copy for every protocol/result code.
func gitErrorCopy(code string) string {
	switch code {
	case "invalid":
		return "Request not accepted · refresh and try again"
	case "not_found":
		return "Thread or project no longer exists"
	case "not_git":
		return "Not a readable Git repository"
	case "checkout_busy":
		return gitLeaseCopy
	case "git_busy":
		return "Another Git write is running in this repository"
	case "stale_entry":
		return "File changed since shown · status refreshed"
	case "stale_head":
		return "HEAD moved since shown · status refreshed"
	case "stale_status":
		return "Staged changes differ from shown · status refreshed"
	case "nothing_staged":
		return "Nothing staged to commit"
	case "nothing_to_amend":
		return "No commit to amend on an unborn branch"
	case "empty_message":
		return "Commit message is empty"
	case "published_commit":
		return "Last commit is already on the upstream · confirm to amend it"
	case "operation_in_progress":
		return "Finish or abort the merge, rebase, cherry-pick or revert first"
	case "conflicted":
		return "Resolve conflicts first"
	case "not_supported":
		return "Not supported for submodules, nested repositories, directories or special files"
	case "status_truncated":
		return "Too many staged changes to review here · commit from a terminal"
	case "unknown_outcome_lookup", "storage":
		return "Result unknown · refresh and check"
	case "internal_error":
		return "Server failed during the write · result unknown, refresh and check"
	case "confirmation_required":
		return "Discard needs confirmation"
	case "identity_missing":
		return "Git identity missing · set user.name/user.email"
	case "index_locked":
		return "Git index is locked by another process · try again"
	case "ref_locked":
		return "HEAD is locked by another process · try again"
	case "stopping":
		return "Server is stopping · no change made"
	case "unavailable":
		return "Git could not be run"
	case "git_failed":
		return "Git refused the change"
	case "commit_failed":
		return "Commit not made · a hook or Git rejected it"
	case "cancelled":
		return "Git was stopped before finishing · result unknown, refresh and check"
	case "interrupted":
		return "Server restarted during the write · result unknown, refresh and check"
	case "staged_newer_content":
		return "File changed while staging · the newer content was staged"
	case "hooks_changed_content":
		return "Committed content differs from what was staged · hooks or another process changed it"
	}
	return "Git write failed"
}

// gitStaleCode reports refusals that mean the shown status is out of date.
func gitStaleCode(code string) bool {
	switch code {
	case "stale_entry", "stale_head", "stale_status", "nothing_staged", "conflicted", "not_supported", "published_commit", "operation_in_progress", "identity_missing", "checkout_busy", "git_busy":
		return true
	}
	return false
}

// gitEntry finds the displayed status entry for group and path.
func (g *gitView) entry(group, path string) (protocol.GitStatusEntry, bool) {
	if g == nil || g.status == nil {
		return protocol.GitStatusEntry{}, false
	}
	for _, e := range g.status.Entries {
		if e.Group == group && e.Path == path {
			return e, true
		}
	}
	return protocol.GitStatusEntry{}, false
}

// gitEntryWritable reports whether an entry offers write controls: not
// conflicted, not a submodule and pinnable.
func gitEntryWritable(e protocol.GitStatusEntry) bool {
	return e.Group != protocol.GitGroupConflicted && !e.Submodule && e.Pin != ""
}

// sendGitWrite records cmd as the target's write and sends it.
func (m *Model) sendGitWrite(key string, cmd protocol.Command, label string) tea.Cmd {
	if m.gitW.writes == nil {
		m.gitW.writes = map[string]*gitWriteState{}
	}
	m.gitW.writes[key] = &gitWriteState{cmd: cmd, label: label, running: true}
	return m.dispatchGitWrite(key, cmd)
}

func (m *Model) dispatchGitWrite(key string, cmd protocol.Command) tea.Cmd {
	if c := m.dispatchGitRef(key, cmd); c != nil {
		return c
	}
	api := m.gitWriter()
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.markDirty()
	return func() tea.Msg {
		r, err := api.GitWrite(ctx, cmd)
		return gitWriteMsg{key: key, cmd: cmd, receipt: r, err: err}
	}
}

// gitWriteAction handles every Git write control.
func (m *Model) gitWriteAction(a action) tea.Cmd {
	key, target := m.gitTarget()
	g := m.gitViews[key]
	switch a.Kind {
	case "git-message":
		return nil
	case "git-discard-cancel":
		m.gitW.discard = nil
		return nil
	case "git-output":
		return m.openGitOutput(key)
	case "git-retry":
		st := m.gitWriteFor(key)
		if st == nil || st.transport == "" || st.running {
			return nil
		}
		st.transport, st.running = "", true
		return m.dispatchGitWrite(key, st.cmd)
	case "git-dismiss":
		delete(m.gitW.writes, key)
		return nil
	}
	if !m.gitWritesEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Git writes are not available on this server")
	}
	if reason, _ := m.gitWriteBlock(key, g); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	switch a.Kind {
	case "git-amend":
		d := m.gitDraftFor(key)
		if g.status.HeadOid == "" && !d.amend {
			return m.showNoticeAs(noticeUnavailable, gitErrorCopy("nothing_to_amend"))
		}
		d.amend = !d.amend
		d.amendHead, d.headChanged = "", false
		if d.amend {
			d.amendHead = g.status.HeadOid
		}
		m.clearGitWarning(key)
		if d.amend && strings.TrimSpace(m.gitMsg().Value()) == "" {
			return m.loadGitHeadMessage(key, target, g.status.HeadOid)
		}
		return nil
	case "git-commit-run":
		return m.commitGit(key, target, g, false)
	case "git-commit-ack":
		// The confirmation binds to the HEAD it was shown for.
		d := m.gitDraftFor(key)
		if !d.amend || a.Value == "" || a.Value != d.amendHead || g.status.HeadOid != d.amendHead {
			return m.showNoticeAs(noticeUnavailable, gitHeadChangedCopy)
		}
		return m.commitGit(key, target, g, true)
	case "git-discard-confirm":
		dlg := m.gitW.discard
		m.gitW.discard = nil
		if dlg == nil || dlg.key != key || dlg.changed {
			return m.showNoticeAs(noticeUnavailable, "File changed since shown · review")
		}
		if e, ok := g.entry(dlg.entry.Group, dlg.entry.Path); !ok || e.Pin != dlg.entry.Pin {
			return m.showNoticeAs(noticeUnavailable, "File changed since shown · review")
		}
		m.clearGitWarning(key)
		return m.sendGitWrite(key, client.GitDiscardCommand(identity(), dlg.target, dlg.entry), dlg.entry.Path)
	}
	e, ok := g.entry(a.Value, a.ID)
	if !ok || !gitEntryWritable(e) {
		return tea.Batch(m.showNoticeAs(noticeUnavailable, "File changed since shown · status refreshed"), m.refreshGit())
	}
	return m.gitEntryAction(key, target, a.Kind, e)
}

// gitEntryAction runs a row action on exactly the entry the user saw: the
// surface's current row or the viewer's pinned entry.
func (m *Model) gitEntryAction(key string, target client.GitTarget, kind string, e protocol.GitStatusEntry) tea.Cmd {
	m.clearGitWarning(key)
	switch kind {
	case "git-stage":
		if e.Group != protocol.GitGroupUnstaged && e.Group != protocol.GitGroupUntracked {
			return m.showNoticeAs(noticeUnavailable, "Already staged")
		}
		return m.sendGitWrite(key, client.GitStageCommand(identity(), target, e), e.Path)
	case "git-unstage":
		if e.Group != protocol.GitGroupStaged {
			return m.showNoticeAs(noticeUnavailable, "Not staged")
		}
		return m.sendGitWrite(key, client.GitUnstageCommand(identity(), target, e), e.Path)
	case "git-discard":
		if e.Group != protocol.GitGroupUnstaged && e.Group != protocol.GitGroupUntracked {
			return m.showNoticeAs(noticeUnavailable, "Unstage first to discard staged changes")
		}
		m.gitW.discard = &gitDiscardDialog{key: key, target: target, entry: e}
		m.showGitDiscardDialog()
		return nil
	}
	return nil
}

const gitHeadChangedCopy = "HEAD changed · review amend"

// gitIntentToAdd reports an intent-to-add entry (`git add -N`), whose
// discard deletes the new file's contents: the server's flag, or the
// porcelain letters (unstaged ".A") from an older server.
func gitIntentToAdd(e protocol.GitStatusEntry) bool {
	return e.Group == protocol.GitGroupUnstaged && (e.IntentToAdd || e.Index == "." && e.Worktree == "A")
}

// gitStatusArrived applies a fresh status to client-local write state: an
// amend prepared for another HEAD turns off, and an open viewer whose entry
// changed stops offering writes.
func (m *Model) gitStatusArrived(key string, g *gitView) {
	d := m.gitDraftFor(key)
	if d.amend && g.status.HeadOid != d.amendHead {
		d.amend, d.amendHead, d.headChanged = false, "", true
	}
	if vw := m.viewer; vw != nil && vw.git != nil && !vw.git.commit && vw.git.key == key && vw.git.pinned {
		if e, ok := g.entry(vw.git.group, vw.git.path); !ok || e.Pin != vw.git.entry.Pin {
			vw.git.changed = true
		}
	}
	m.recheckGitDiscard(key, g)
}

// clearGitWarning drops a finished write's persistent line before the next
// action; a running or retryable write stays.
func (m *Model) clearGitWarning(key string) {
	if st := m.gitWriteFor(key); st != nil && !st.running && st.transport == "" {
		delete(m.gitW.writes, key)
	}
}

// showGitDiscardDialog paints the discard confirmation from its state:
// Cancel first and focused; the destructive action is replaced by a muted
// note once the entry changed since the dialog opened.
func (m *Model) showGitDiscardDialog() {
	dlg := m.gitW.discard
	if dlg == nil {
		return
	}
	path := safe(singleLine(dlg.entry.Path))
	question, verb, title := "Discard changes to "+path+"?", "Discard", "Discard changes · "
	if dlg.entry.Group == protocol.GitGroupUntracked {
		question, verb, title = "Delete untracked file "+path+"? This cannot be undone.", "Delete", "Delete file · "
	} else if gitIntentToAdd(dlg.entry) {
		question, title = "Discard new file "+path+"? Its contents cannot be recovered.", "Discard new file · "
	}
	items := []menuItem{{Note: question}, {Label: "Cancel", Action: action{Kind: "git-discard-cancel"}}}
	if dlg.changed {
		items = append(items, menuItem{Note: "File changed since shown · review"})
	} else {
		items = append(items, menuItem{Label: verb, Action: action{Kind: "git-discard-confirm", ID: dlg.entry.Path, Value: dlg.entry.Group}})
	}
	index := 1
	if len(m.menu) > 0 && m.menuTitleUser == path && m.menuIndex < len(items) && items[m.menuIndex].selectable() {
		index = m.menuIndex
	}
	m.showMenuFor(title, path, items)
	m.menuIndex = index
}

// gitDialogOpen reports whether the menu on screen is the discard dialog.
func (m *Model) gitDialogOpen() bool {
	if m.gitW.discard == nil {
		return false
	}
	for _, item := range m.menu {
		if item.Action.Kind == "git-discard-cancel" {
			return true
		}
	}
	m.gitW.discard = nil
	return false
}

// recheckGitDiscard disables the dialog's destructive action once the entry
// it shows changed or disappeared.
func (m *Model) recheckGitDiscard(key string, g *gitView) {
	if !m.gitDialogOpen() || m.gitW.discard.key != key || m.gitW.discard.changed {
		return
	}
	d := m.gitW.discard
	if e, ok := g.entry(d.entry.Group, d.entry.Path); ok && e.Pin == d.entry.Pin {
		return
	}
	d.changed = true
	m.showGitDiscardDialog()
}

// commitGit builds and sends the commit command, asking first when an amend
// would rewrite a published commit.
func (m *Model) commitGit(key string, target client.GitTarget, g *gitView, acknowledged bool) tea.Cmd {
	d := m.gitDraftFor(key)
	message := m.gitMsg().Value()
	if reason := m.gitCommitBlock(key, g); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason)
	}
	if d.amend && g.status.HeadOnUpstream && !acknowledged {
		note := "The last commit is already on the upstream. Amending rewrites published history."
		if g.status.HeadOnUpstreamUnknown {
			note = "Could not check whether the last commit is on the upstream. Amending may rewrite published history."
		}
		m.showMenu("Amend published commit", []menuItem{
			{Note: note},
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "Amend published commit", Action: action{Kind: "git-commit-ack", Value: d.amendHead}},
		})
		m.menuIndex = 1
		return nil
	}
	m.clearGitWarning(key)
	status := *g.status
	if d.amend {
		// ExpectedHead is the HEAD the amend was prepared for; the server
		// refuses with stale_head if HEAD moved since.
		status.HeadOid = d.amendHead
	}
	cmd := client.GitCommitCommand(identity(), target, status, message, d.amend, acknowledged)
	return m.sendGitWrite(key, cmd, "commit")
}

// gitCommitBlock is why Commit is disabled ("" when enabled).
func (m *Model) gitCommitBlock(key string, g *gitView) string {
	if reason, _ := m.gitWriteBlock(key, g); reason != "" {
		return reason
	}
	s := g.status
	d := m.gitDraftFor(key)
	switch {
	case d.amend && d.amendHead != s.HeadOid:
		return gitHeadChangedCopy
	case s.Operation != "":
		return gitErrorCopy("operation_in_progress")
	case s.Identity != nil && s.Identity.Missing:
		return gitErrorCopy("identity_missing")
	case s.StagedTruncated || s.StagedFingerprint == "":
		return gitErrorCopy("status_truncated")
	case slices.ContainsFunc(s.Entries, func(e protocol.GitStatusEntry) bool { return e.Group == protocol.GitGroupConflicted }):
		return gitErrorCopy("conflicted")
	case !d.amend && !slices.ContainsFunc(s.Entries, func(e protocol.GitStatusEntry) bool { return e.Group == protocol.GitGroupStaged }):
		return gitErrorCopy("nothing_staged")
	case strings.TrimSpace(m.gitMsg().Value()) == "":
		return "Write a commit message"
	}
	return ""
}

// loadGitHeadMessage prefills an empty amend draft with HEAD's message.
func (m *Model) loadGitHeadMessage(key string, target client.GitTarget, head string) tea.Cmd {
	api := m.gitClient()
	if api == nil || head == "" {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		show, err := api.GitShow(ctx, target, head)
		return gitHeadMsg{key: key, head: head, message: show.Commit.Body, err: err}
	}
}

func (m *Model) acceptGitHead(msg gitHeadMsg) tea.Cmd {
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Last commit message unavailable · "+safe(singleLine(msg.err.Error())))
	}
	current, _ := m.gitTarget()
	d := m.gitDraftFor(msg.key)
	if !d.amend || d.amendHead != msg.head {
		return nil
	}
	text := strings.TrimRight(safe(msg.message), "\n")
	if msg.key == current && msg.key == m.gitW.draftKey {
		if strings.TrimSpace(m.gitMsg().Value()) == "" {
			m.gitMsg().SetValue(text)
		}
	} else if strings.TrimSpace(d.message) == "" {
		d.message = text
	}
	m.markDirty()
	return nil
}

// acceptGitWrite applies a write's reply to the target that sent it.
func (m *Model) acceptGitWrite(msg gitWriteMsg) tea.Cmd {
	st := m.gitWriteFor(msg.key)
	if st == nil || st.cmd.ID != msg.cmd.ID {
		return nil
	}
	st.running = false
	m.markDirty()
	if gitRefKind(msg.cmd.Kind) {
		return m.acceptGitRef(msg, st)
	}
	current, _ := m.gitTarget()
	refresh := func() tea.Cmd {
		if msg.key != current {
			return nil // Switching back rereads it.
		}
		m.gitShown = current
		return m.refreshGit()
	}
	var pe *protocol.Error
	if errors.As(msg.err, &pe) && (pe.Code == "unknown_outcome_lookup" || pe.Code == "storage") {
		// The server could not tell whether the command ran: keep it for
		// Retry and block new writes until the user resolves it.
		st.transport = gitErrorCopy(pe.Code)
		return refresh()
	}
	if errors.As(msg.err, &pe) {
		// Refused before anything ran: nothing is recorded, so the command
		// is dropped and a corrected attempt is a new one.
		delete(m.gitW.writes, msg.key)
		notice := m.showNoticeAs(noticeUnavailable, gitErrorCopy(pe.Code))
		if gitStaleCode(pe.Code) {
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
	st.output, st.truncated = r.Output, r.OutputTruncated
	switch r.State {
	case protocol.GitStateSucceeded:
		done := m.gitSuccessCopy(msg.key, st, r)
		if r.Code != "" {
			st.warning = gitErrorCopy(r.Code)
		} else {
			delete(m.gitW.writes, msg.key)
		}
		if msg.cmd.Kind == protocol.GitKindCommit {
			d := m.gitDraftFor(msg.key)
			d.message, d.amend, d.amendHead, d.headChanged = "", false, "", false
			if msg.key == m.gitW.draftKey {
				m.gitMsg().SetValue("")
			}
		}
		return tea.Batch(m.showNoticeAs(noticeDone, done), refresh())
	case protocol.GitStateFailed:
		st.failure = gitErrorCopy(r.Code)
		if r.Code == "" {
			st.failure = "Git write failed"
		}
	default:
		st.failure, st.unknown = "Result unknown · refresh and check", true
		if r.Code == "cancelled" || r.Code == "interrupted" || r.Code == "internal_error" {
			st.failure = gitErrorCopy(r.Code)
		}
	}
	return refresh()
}

func (m *Model) gitSuccessCopy(key string, st *gitWriteState, r *protocol.GitResult) string {
	path := safe(singleLine(st.label))
	switch st.cmd.Kind {
	case protocol.GitKindStage:
		return "Staged " + path
	case protocol.GitKindUnstage:
		return "Unstaged " + path
	case protocol.GitKindDiscard:
		if len(st.cmd.Git.Paths) == 1 && st.cmd.Git.Paths[0].Group == protocol.GitGroupUntracked {
			return "Deleted " + path
		}
		return "Discarded changes to " + path
	case protocol.GitKindCommit:
		short := safe(singleLine(r.Commit))
		if len(short) > 7 {
			short = short[:7]
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(st.cmd.Git.Message), "\n")
		verb := "Committed "
		if st.cmd.Git.Amend {
			verb = "Amended "
		}
		return strings.TrimSpace(verb + short + " " + safe(singleLine(subject)))
	}
	return "Done"
}

// openGitOutput shows the last write's bounded output in the read-only
// viewer; the viewer sanitizes it like any untrusted content.
func (m *Model) openGitOutput(key string) tea.Cmd {
	st := m.gitWriteFor(key)
	if st == nil || st.output == "" {
		return nil
	}
	name := "Git output · " + strings.TrimPrefix(st.cmd.Kind, "git.")
	if st.truncated {
		name += " · truncated at 64 KiB"
	}
	origin := m.focus
	if origin == "" || strings.HasPrefix(origin, "menu:") || strings.HasPrefix(origin, "viewer-") {
		origin = "right-body"
	}
	release := m.releaseViewerImage()
	m.menu, m.contextMenu, m.hover = nil, nil, ""
	m.viewer = &attachmentViewer{att: protocol.Attachment{Kind: "terminal-output", Name: name, Content: safe(st.output)}, origin: origin, loaded: true}
	return tea.Batch(release, m.setFocus("viewer-body"))
}

// gitOpsChanged refreshes the displayed status once when another client's
// write in this repository finishes.
func (m *Model) gitOpsChanged(key string) bool {
	if m.gitW.ops == nil {
		m.gitW.ops = map[string]string{}
	}
	g := m.gitViews[key]
	path := ""
	if g != nil && g.status != nil {
		path = g.status.Workspace.Path
	}
	changed := false
	for _, op := range m.snapshot.GitOps {
		seen := op.CommandID + "/" + op.State
		if m.gitW.ops[op.Checkout] == seen {
			continue
		}
		prev := m.gitW.ops[op.Checkout]
		m.gitW.ops[op.Checkout] = seen
		if prev != "" && op.State != protocol.GitStateRunning && gitRepoOverlap(op.Checkout, path) {
			changed = true
		}
	}
	return changed
}

// gitRowKey handles s/u/d on a focused status row or its controls.
func (m *Model) gitRowKey(s string) (tea.Cmd, bool) {
	kind := map[string]string{"s": "git-stage", "u": "git-unstage", "d": "git-discard"}[s]
	if kind == "" || !m.gitWritesEnabled() {
		return nil, false
	}
	group, path, ok := gitRowFocus(m.focus)
	if !ok {
		return nil, false
	}
	return m.activate(action{Kind: kind, Value: group, ID: path}), true
}

// gitRowFocus extracts a status row's group and path from its row key or
// one of its control keys.
func gitRowFocus(focus string) (string, string, bool) {
	rest, ok := strings.CutPrefix(focus, "git:")
	if !ok {
		rest, ok = strings.CutPrefix(focus, "git-act:")
		if !ok {
			return "", "", false
		}
		_, rest, ok = strings.Cut(rest, ":")
		if !ok {
			return "", "", false
		}
	}
	group, path, ok := strings.Cut(rest, ":")
	switch group {
	case protocol.GitGroupStaged, protocol.GitGroupUnstaged, protocol.GitGroupUntracked:
		return group, path, ok && path != ""
	}
	return "", "", false
}

// gitViewerWriteKey handles s/u/d in the diff viewer for the shown entry.
// Discard closes the viewer first so its confirmation takes the keys.
func (m *Model) gitViewerWriteKey(s string) (tea.Cmd, bool) {
	vw := m.viewer
	if vw == nil || vw.git == nil || vw.git.commit || !m.gitWritesEnabled() {
		return nil, false
	}
	kind := map[string]string{"s": "git-stage", "u": "git-unstage", "d": "git-discard"}[s]
	if kind == "" {
		return nil, false
	}
	vg := vw.git
	current, _ := m.gitTarget()
	switch {
	case !vg.pinned:
		return m.showNoticeAs(noticeUnavailable, "Not writable from here"), true
	case vg.changed || current != vg.key:
		return m.showNoticeAs(noticeUnavailable, gitViewerChangedCopy), true
	}
	if reason, _ := m.gitWriteBlock(vg.key, m.gitViews[vg.key]); reason != "" {
		return m.showNoticeAs(noticeUnavailable, reason), true
	}
	// The viewer's own pinned entry and target, never a refreshed row.
	var closing tea.Cmd
	if kind == "git-discard" {
		closing = m.closeAttachmentViewer()
	}
	return tea.Batch(closing, m.gitEntryAction(vg.key, vg.target, kind, vg.entry)), true
}

const gitViewerChangedCopy = "Changed since shown · reopen to review"

// gitViewerKeys is the viewer pair naming the write keys for the entry.
func gitViewerKeys(group string) string {
	switch group {
	case protocol.GitGroupStaged:
		return "u Unstage"
	case protocol.GitGroupUnstaged:
		return "s Stage · d Discard"
	case protocol.GitGroupUntracked:
		return "s Stage · d Delete"
	}
	return ""
}

// gitMessageKeyPress edits the commit message: Enter commits, Shift+Enter
// or Ctrl+J inserts a newline.
func (m *Model) gitMessageKeyPress(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "enter" {
		return m.activate(action{Kind: "git-commit-run"})
	}
	if s == "shift+enter" || s == "ctrl+j" {
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	c := updateInput(m.gitMsg(), k)
	m.markDirty()
	return c
}

// gitMessagePaste inserts pasted text into the message; it never commits.
func (m *Model) gitMessagePaste(content string) tea.Cmd {
	c := updateInput(m.gitMsg(), tea.PasteMsg{Content: safe(content)})
	m.markDirty()
	return c
}
