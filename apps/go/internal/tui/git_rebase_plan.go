package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_rebase_plan.go is the agent-planned rebase of ADR 0027 in the Git
// surface: a start form (agent, its settings, the user's instruction; the
// same conventions as the resolution job form), the planning jobs of the
// checkout with their proposal state and actions, the proposal details in
// the read-only viewer, and loading a proposed plan into the plan editor
// (git_rebase.go). Nothing runs from a proposal: the user starts the
// rebase from the editor through its ordinary confirmation.

// gitPlanInputKey is the focus key of the instruction input.
const gitPlanInputKey = "git:plan-input"

// gitPlanAPI reads proposals; tests inject a fake.
type gitPlanAPI interface {
	GitRebaseProposal(ctx context.Context, target client.GitTarget, jobID string) (protocol.GitRebaseProposal, error)
}

var _ gitPlanAPI = (*client.Client)(nil)

// gitPlanForm is the open start or revise form; it takes over the body.
type gitPlanForm struct {
	key    string
	target client.GitTarget
	revise bool
	// Start: base, onto and the plan read the user saw (fingerprint, may
	// be empty). Revise: the job and the proposal revision shown.
	base, onto, fingerprint string
	jobID                   string
	revision                int64
	agentID                 string
	settings                protocol.Settings
	// sentID and text are the command in flight and the instruction it
	// carried; err is the server's refusal, shown when the form returns.
	sentID, text, err string
}

type gitPlanUI struct {
	// forms are the open forms per Git target, kept across thread switches
	// like rebase drafts; inputFor is the form whose text the shared input
	// holds.
	forms    map[string]*gitPlanForm
	inputFor *gitPlanForm
	input    textarea.Model
	ready    bool
	seq      uint64
	// end is the job whose End confirmation is shown.
	end string
	// sent are forms whose command awaits the server's reply, by command.
	sent map[string]*gitPlanForm
}

// gitPlanInstructionMax is the server's instruction limit in bytes.
const gitPlanInstructionMax = 4 << 10

type gitPlanProposalMsg struct {
	seq      uint64
	key, job string
	open     bool
	p        protocol.GitRebaseProposal
	err      error
}

// gitRebaseAgentInfo marks a draft loaded from an agent's proposal.
type gitRebaseAgentInfo struct {
	job         string
	revision    int64
	fingerprint string
	updateRefs  bool
	rationale   string
	// built is the plan as loaded, to tell whether the user edited it.
	built []protocol.GitRebaseEntry
	// outdated is set once a refresh read another plan than the one the
	// agent planned against.
	outdated bool
}

// gitPlanEnabled reports a server with agent-planned rebases.
func (m *Model) gitPlanEnabled() bool {
	_, ok := m.gitClient().(gitPlanAPI)
	return ok && m.gitRebaseEnabled() && slices.Contains(m.snapshot.Capabilities, "git-rebase-agent-plan")
}

func (m *Model) gitPlanInput() *textarea.Model {
	if !m.gitPl.ready {
		m.gitPl.input = newInput("Instructions (optional)")
		m.gitPl.input.CharLimit = 4096
		m.gitPl.input.SetHeight(1)
		m.gitPl.ready = true
	}
	return &m.gitPl.input
}

// gitPlanJob reports a rebase planning job thread.
func gitPlanJob(t protocol.Thread) bool {
	return t.Job != nil && t.Job.Kind == protocol.ThreadJobRebasePlan
}

// gitPlanJobs are the planning jobs of the displayed checkout's repository.
func (m *Model) gitPlanJobs(g *gitView) []protocol.Thread {
	if g == nil || g.status == nil || g.status.Workspace.Path == "" {
		return nil
	}
	path := g.status.Workspace.Path
	root := m.gitRepoRoot(path)
	var out []protocol.Thread
	for _, t := range m.snapshot.Threads {
		if gitPlanJob(t) && (gitRepoOverlap(t.Checkout, root) || gitRepoOverlap(t.Checkout, path) || gitRepoOverlap(t.Job.Checkout, path)) {
			out = append(out, t)
		}
	}
	return out
}

// gitPlanHere reports a job of the displayed checkout itself (not another
// worktree of the repository).
func gitPlanHere(t protocol.Thread, g *gitView) bool {
	if g == nil || g.status == nil {
		return false
	}
	clean := func(p string) string { return strings.TrimRight(p, "/") }
	return clean(t.Job.Checkout) == clean(g.status.Workspace.Path) || t.Job.Checkout == "" && clean(t.Checkout) == clean(g.status.Workspace.Path)
}

// openGitPlanForm opens the start form for base and onto.
func (m *Model) openGitPlanForm(base, onto, fingerprint string) tea.Cmd {
	key, target := m.gitTarget()
	if !m.gitPlanEnabled() {
		return m.showNoticeAs(noticeUnavailable, "Agent-planned rebase needs a newer server")
	}
	if base == "" {
		return nil
	}
	f := &gitPlanForm{key: key, target: target, base: base, onto: onto, fingerprint: fingerprint}
	m.gitPlanPickAgent(f)
	return m.showGitPlanForm(f)
}

func (m *Model) gitPlanPickAgent(f *gitPlanForm) {
	if d := m.snapshot.AppSettings.NewThreadDefaults; d != nil {
		f.agentID = d.AgentID
	}
	if a, ok := m.agentByID(f.agentID); !ok || a.Kind == "fixture" && len(m.gitPlanAgents()) > 1 {
		f.agentID = ""
		if agents := m.gitPlanAgents(); len(agents) > 0 {
			f.agentID = agents[0].ID
		}
	}
	f.settings = m.gitJobInitialSettings(f.agentID)
}

// gitPlanFormFor is key's open form, or nil.
func (m *Model) gitPlanFormFor(key string) *gitPlanForm { return m.gitPl.forms[key] }

// gitPlanSyncInput loads key's form text into the shared input, storing
// the previous owner's text first.
func (m *Model) gitPlanSyncInput(key string) {
	f := m.gitPl.forms[key]
	if f == nil || m.gitPl.inputFor == f {
		return
	}
	if o := m.gitPl.inputFor; o != nil {
		o.text = m.gitPlanInput().Value()
	}
	m.gitPl.inputFor = f
	m.gitPlanInput().SetValue(f.text)
}

func (m *Model) showGitPlanForm(f *gitPlanForm) tea.Cmd {
	if m.gitPl.forms == nil {
		m.gitPl.forms = map[string]*gitPlanForm{}
	}
	if o := m.gitPl.inputFor; o != nil {
		o.text = m.gitPlanInput().Value()
	}
	m.gitPl.forms[f.key] = f
	m.gitPl.inputFor = f
	m.gitPlanInput().SetValue(f.text)
	m.viewState().DetailScroll = 0
	m.markDirty()
	return m.setFocus("git:plan-cancel")
}

// gitPlanAgents are the agents a planning job may use (any available one;
// the server refuses agents that are not ACP).
func (m *Model) gitPlanAgents() []protocol.Agent { return m.gitJobAgents() }

// gitPlanSettings are the settings the start command carries (nil for the
// agent's own), reconciled as the form shows them.
func (m *Model) gitPlanSettings(f *gitPlanForm) *protocol.Settings {
	a, ok := m.agentByID(f.agentID)
	if !ok || a.Kind == "fixture" || len(a.Options) == 0 {
		return nil
	}
	s := reconcileJobSettings(a, f.settings)
	return &s
}

// gitPlanAction handles every planning control; ok reports that a was one.
func (m *Model) gitPlanAction(a action) (tea.Cmd, bool) {
	if !strings.HasPrefix(a.Kind, "git-plan-") {
		return nil, false
	}
	key, target := m.gitTarget()
	m.gitPlanSyncInput(key)
	f := m.gitPl.forms[key]
	switch a.Kind {
	case "git-plan-open":
		return m.openGitPlanForm(a.ID, a.Value, ""), true
	case "git-plan-from-editor":
		d := m.gitRebaseDraftFor(key)
		if d == nil {
			return nil, true
		}
		fp := ""
		if d.loaded && !m.gitRebaseStale(d) {
			fp = d.plan.Fingerprint
		}
		return m.openGitPlanForm(d.base, d.onto, fp), true
	case "git-plan-cancel-form":
		m.dropGitPlanForm(key)
		m.markDirty()
		return m.setFocus("git-refresh"), true
	case "git-plan-input":
		return m.setFocus(gitPlanInputKey), true
	case "git-plan-agent":
		if f != nil {
			f.agentID = a.ID
			f.settings = m.gitJobInitialSettings(a.ID)
			m.markDirty()
		}
		return nil, true
	case "git-plan-setting":
		if f == nil {
			return nil, true
		}
		ag, ok := m.agentByID(f.agentID)
		if !ok {
			return m.showNoticeAs(noticeUnavailable, "Choose an agent first"), true
		}
		f.settings = reconcileJobSettings(ag, f.settings)
		o, has := agentConfigFor(ag).forModel(f.settings.Model).option(a.ID)
		if !has {
			return m.showNoticeAs(noticeUnavailable, safe(ag.Name)+" offers no "+a.ID+" option"), true
		}
		m.showMenu("Planning job · "+a.ID, m.optionValueMenuItems(o, a.ID, settingValue(f.settings, a.ID), "git-plan-field"))
		return nil, true
	case "git-plan-field":
		if f == nil {
			return nil, true
		}
		if ag, ok := m.agentByID(f.agentID); ok {
			f.settings = reconcileJobSettings(ag, f.settings)
			if problem := agentConfigFor(ag).forModel(f.settings.Model).applySetting(&f.settings, a.ID, a.Value); problem != "" {
				return m.showNoticeAs(noticeUnavailable, problem), true
			}
		}
		m.markDirty()
		return nil, true
	case "git-plan-send":
		if f == nil || f.key != key {
			return nil, true
		}
		instr := strings.TrimSpace(m.gitPlanInput().Value())
		if len(instr) > gitPlanInstructionMax {
			return m.showNoticeAs(noticeUnavailable, "The instruction is over 4 KiB · shorten it"), true
		}
		var cmd protocol.Command
		label := "planning job"
		if f.revise {
			cmd = client.GitRebasePlanReviseCommand(identity(), target, f.jobID, f.revision, instr)
			label = "revision request"
		} else {
			if f.agentID == "" {
				return m.showNoticeAs(noticeUnavailable, "Choose an agent"), true
			}
			cmd = client.GitRebasePlanStartCommand(identity(), target, f.agentID, m.gitPlanSettings(f), f.base, f.onto, f.fingerprint, instr)
		}
		send := m.sendGitWrite(key, cmd, label)
		if st := m.gitW.writes[key]; st == nil || st.cmd.ID != cmd.ID {
			return send, true // another write is pending: the form stays
		}
		// The form hides while the command is in flight and returns with
		// the instruction and the reason if the server refuses it.
		f.sentID, f.text, f.err = cmd.ID, m.gitPlanInput().Value(), ""
		m.dropGitPlanForm(key)
		if m.gitPl.sent == nil {
			m.gitPl.sent = map[string]*gitPlanForm{}
		}
		m.gitPl.sent[cmd.ID] = f
		m.clearGitWarning(key)
		return tea.Batch(m.setFocus("git-refresh"), send), true
	case "git-plan-revise":
		t, ok := m.threadByID(a.ID)
		if !ok || !gitPlanJob(t) || t.Job.Proposal == nil {
			return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindRebasePlanRevise, "no_job")), true
		}
		return m.showGitPlanForm(&gitPlanForm{key: key, target: target, revise: true, jobID: t.ID, revision: t.Job.Proposal.Revision,
			base: t.Job.Base, onto: t.Job.Onto, agentID: t.AgentID}), true
	case "git-plan-replan":
		t, ok := m.threadByID(a.ID)
		if !ok || !gitPlanJob(t) {
			return nil, true
		}
		return m.openGitPlanForm(t.Job.Base, t.Job.Onto, ""), true
	case "git-plan-stop":
		return m.sendGitWrite(key, client.GitRebasePlanCancelCommand(identity(), target, a.ID), "stop"), true
	case "git-plan-end":
		m.gitPl.end = a.ID
		where := ""
		if t, ok := m.threadByID(a.ID); ok && gitPlanJob(t) {
			where = gitShortCheckout(t.Job.Checkout)
			if t.Job.Proposal != nil && t.Job.Proposal.Branch != "" {
				where += " · " + safe(singleLine(t.Job.Proposal.Branch))
			}
		}
		m.showMenu("End planning job", []menuItem{
			{Note: "End the planning job of " + where + "?"},
			{Note: "Its thread and proposal are deleted · nothing in Git changes"},
			{Label: "Cancel", Action: action{Kind: "noop"}},
			{Label: "End planning job", Action: action{Kind: "git-plan-end-confirm"}},
		})
		m.menuIndex = 2
		return nil, true
	case "git-plan-end-confirm":
		job := m.gitPl.end
		m.gitPl.end = ""
		if job == "" {
			return nil, true
		}
		return m.sendGitWrite(key, client.GitRebasePlanEndCommand(identity(), target, job), "planning job"), true
	case "git-plan-transcript":
		if t, ok := m.threadByID(a.ID); ok {
			return m.openGitJobTranscript(t), true
		}
		return nil, true
	case "git-plan-load", "git-plan-details":
		return m.readGitProposal(key, target, a.ID, a.Kind == "git-plan-load"), true
	}
	return nil, true
}

func (m *Model) readGitProposal(key string, target client.GitTarget, job string, open bool) tea.Cmd {
	api, ok := m.gitClient().(gitPlanAPI)
	if !ok {
		return nil
	}
	m.gitPl.seq++
	seq := m.gitPl.seq
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		p, err := api.GitRebaseProposal(deadline, target, job)
		return gitPlanProposalMsg{seq: seq, key: key, job: job, open: open, p: p, err: err}
	}
}

// acceptGitProposal opens the details, or loads a proposed plan into the
// editor. A stale, invalid or tainted proposal is never loaded.
func (m *Model) acceptGitProposal(msg gitPlanProposalMsg) tea.Cmd {
	if msg.seq != m.gitPl.seq {
		return nil
	}
	if current, _ := m.gitTarget(); current != msg.key {
		return nil
	}
	if msg.err != nil {
		var pe *protocol.Error
		if errors.As(msg.err, &pe) {
			return m.showNoticeAs(noticeUnavailable, gitRefCopy(protocol.GitKindRebasePlanStart, pe.Code))
		}
		return m.showNoticeAs(noticeError, "Could not read the proposal · "+safe(singleLine(msg.err.Error())))
	}
	p := msg.p
	if !msg.open {
		return m.openGitProposalDetails(p)
	}
	stale := p.State == protocol.GitProposalStale || p.CurrentFingerprint != "" && p.CurrentFingerprint != p.Summary.Fingerprint
	switch {
	case stale:
		return m.showNoticeAs(noticeUnavailable, "The branch changed since the agent planned · re-plan; the proposal was not loaded")
	case !client.ProposalLoadable(p):
		return m.showNoticeAs(noticeUnavailable, "Only a proposed plan can be opened · this one is "+safe(singleLine(p.State)))
	}
	m.gitRB.agentInfo = &gitRebaseAgentInfo{job: msg.job, revision: p.Summary.Revision, fingerprint: p.Summary.Fingerprint,
		updateRefs: p.UpdateRefs, rationale: p.Rationale}
	return m.openGitRebaseAgentPlan(p.Base, p.Onto, p.Entries)
}

// openGitProposalDetails shows a proposal in the read-only viewer:
// state, enforcement, errors, checkout changes, rationale, entries and the
// agent's raw answer, all sanitized.
func (m *Model) openGitProposalDetails(p protocol.GitRebaseProposal) tea.Cmd {
	var b strings.Builder
	line := func(s string) { b.WriteString(safe(s) + "\n") }
	s := p.Summary
	line("State: " + p.State + " · revision " + strconv.FormatInt(s.Revision, 10) + " · turn " + strconv.Itoa(s.Turns) + "/" + strconv.Itoa(protocol.GitRebasePlanJobTurnsMax))
	line("Base: " + p.Base + onto(p.Onto))
	line("Branch: " + s.Branch + " at " + gitShort(s.HeadOid) + " · " + plural(s.Commits, "commit"))
	if p.Enforcement != "" {
		line("Enforcement: " + p.Enforcement)
	}
	line(gitPlanEnforcementCopy(s))
	if p.StopReason != "" {
		line("Turn ended: " + p.StopReason)
	}
	if s.Reason != "" {
		line("Reason: " + s.Reason)
	}
	if p.CurrentError != "" {
		line("Current plan read failed: " + p.CurrentError)
	}
	if p.Blocked != "" {
		line("Now blocked: " + gitRefCopy(protocol.GitKindRebase, p.Blocked) + " " + p.BlockedMessage)
	}
	for _, e := range s.Errors {
		line("Error: " + e)
	}
	if len(p.Changes) > 0 {
		line("Checkout changes during the turn:")
		for _, c := range p.Changes {
			line("  " + c)
		}
	}
	if p.Rationale != "" {
		line("\nRationale:\n" + p.Rationale)
	}
	if len(p.Entries) > 0 {
		line("\nEntries (" + strconv.FormatBool(p.UpdateRefs) + " update refs):")
		for _, e := range p.Entries {
			l := "  " + e.Action
			if e.Fixup != "" {
				l += " -" + e.Fixup
			}
			if e.EditMode != "" {
				l += " (" + e.EditMode + ")"
			}
			if e.Commit != "" {
				l += " " + gitShort(e.Commit)
			}
			if e.Message != "" {
				first, _, _ := strings.Cut(e.Message, "\n")
				l += " · message: " + first
			}
			line(l)
		}
	}
	if p.Raw != "" {
		line("\nAgent answer:")
		line(p.Raw)
		if p.RawTruncated {
			line("(truncated)")
		}
	}
	origin := m.focus
	if origin == "" || strings.HasPrefix(origin, "menu:") || strings.HasPrefix(origin, "viewer-") {
		origin = "right-body"
	}
	release := m.releaseViewerImage()
	m.menu, m.contextMenu, m.hover = nil, nil, ""
	m.viewer = &attachmentViewer{att: protocol.Attachment{Kind: "terminal-output", Name: "Rebase proposal", Content: b.String()}, origin: origin, loaded: true}
	return tea.Batch(release, m.setFocus("viewer-body"))
}

func onto(o string) string {
	if o == "" {
		return ""
	}
	return " · onto " + o
}

// gitPlanEnforcementCopy says honestly what kept the agent from changing
// the checkout.
func gitPlanEnforcementCopy(s protocol.GitRebaseProposalSummary) string {
	var parts []string
	if s.ReadOnlyMode {
		mode := "read-only mode"
		if s.Permissions != "" {
			mode += " (" + safe(singleLine(s.Permissions)) + ")"
		}
		parts = append(parts, mode)
	} else {
		parts = append(parts, "no verified read-only mode")
	}
	if s.ApprovalGated {
		parts = append(parts, "commands need approval, declined")
	}
	if s.DeclinedApprovals > 0 {
		parts = append(parts, plural(s.DeclinedApprovals, "approval")+" declined")
	}
	parts = append(parts, "checkout compared after each turn")
	return "Kept read-only by: " + strings.Join(parts, " · ")
}

// gitPlanInputKeyPress edits the instruction: Enter sends, Esc cancels.
func (m *Model) gitPlanInputKeyPress(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		return m.activate(action{Kind: "git-plan-send"})
	case "esc":
		return m.activate(action{Kind: "git-plan-cancel-form"})
	case "shift+enter", "ctrl+j", "tab", "shift+tab", "up", "down":
		return nil
	}
	key, _ := m.gitTarget()
	m.gitPlanSyncInput(key)
	c := updateInput(m.gitPlanInput(), k)
	if f := m.gitPl.forms[key]; f != nil && m.gitPl.inputFor == f {
		f.text = m.gitPlanInput().Value()
	}
	m.markDirty()
	return c
}

func (m *Model) gitPlanInputPaste(content string) tea.Cmd {
	key, _ := m.gitTarget()
	m.gitPlanSyncInput(key)
	c := updateInput(m.gitPlanInput(), tea.PasteMsg{Content: singleLine(safe(content))})
	if f := m.gitPl.forms[key]; f != nil && m.gitPl.inputFor == f {
		f.text = m.gitPlanInput().Value()
	}
	m.markDirty()
	return c
}

// dropGitPlanForm closes key's form.
func (m *Model) dropGitPlanForm(key string) {
	if f := m.gitPl.forms[key]; f != nil {
		delete(m.gitPl.forms, key)
		if m.gitPl.inputFor == f {
			m.gitPl.inputFor = nil
			m.gitPlanInput().SetValue("")
		}
	}
}

// gitPlanReply returns a refused start or revision to its form (on its
// own target, whichever is shown), with the instruction and the reason.
// When another form is open there meanwhile, that form shows the refusal
// and the earlier instruction, so neither is lost.
func (m *Model) gitPlanReply(msg gitWriteMsg) {
	f := m.gitPl.sent[msg.cmd.ID]
	if f == nil {
		return
	}
	reason := ""
	var pe *protocol.Error
	switch {
	case errors.As(msg.err, &pe):
		reason = gitRefCopy(msg.cmd.Kind, pe.Code)
		if s := safe(singleLine(pe.Message)); s != "" {
			reason += " · " + s
		}
	case msg.err != nil:
		return // no reply: Retry resends the same command
	case msg.receipt.Git != nil && msg.receipt.Git.State == protocol.GitStateFailed:
		reason = gitRefCopy(msg.cmd.Kind, msg.receipt.Git.Code)
		if s := safe(singleLine(msg.receipt.Git.Message)); s != "" {
			reason += " · " + s
		}
	}
	delete(m.gitPl.sent, msg.cmd.ID)
	if reason == "" {
		return
	}
	if o := m.gitPl.forms[f.key]; o != nil {
		o.err = "An earlier request was refused: " + reason
		if t := strings.TrimSpace(f.text); t != "" {
			o.err += " · its instruction: " + safe(singleLine(t))
		}
	} else {
		f.sentID, f.err = "", reason
		if m.gitPl.forms == nil {
			m.gitPl.forms = map[string]*gitPlanForm{}
		}
		m.gitPl.forms[f.key] = f
	}
	m.markDirty()
}

// gitPlanSettle loads the shown target's form into the input, leaves the
// input when that target has none, and drops dialog state whose menu is
// gone. Forms of other targets are kept.
func (m *Model) gitPlanSettle(key string) {
	m.gitPlanSyncInput(key)
	if m.gitPl.forms[key] == nil && m.focus == gitPlanInputKey {
		m.setFocus("git-refresh")
	}
	if len(m.menu) == 0 {
		m.gitPl.end = ""
	}
}
