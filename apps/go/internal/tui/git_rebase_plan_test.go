package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (g *fakeGit) GitRebaseProposal(_ context.Context, t client.GitTarget, job string) (protocol.GitRebaseProposal, error) {
	g.record("proposal:" + job)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rbProposal, g.err
}

// planModel is rbModel with agent-planned rebases and one ACP agent.
func planModel(t *testing.T) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := rbModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-agent-plan")
	return m, api
}

// addPlanJob adds a planning job thread for the checkout.
func addPlanJob(m *Model, s protocol.GitRebaseProposalSummary) {
	m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: "rebase-plan-1", Title: "Rebase plan", Checkout: "/src/repo", AgentID: "agent-x",
		Job: &protocol.ThreadJob{Kind: protocol.ThreadJobRebasePlan, Checkout: "/src/repo", Base: rb0, Proposal: &s}})
	m.markDirty()
}

func TestGitPlanStartFromCommitMenu(t *testing.T) {
	m, api := planModel(t)
	g := m.currentGitView()
	c, _ := g.commit(rb1)
	text := menuText(&Model{menu: m.gitRebaseMenuItems(g, c)})
	if !strings.Contains(text, "Plan rebase from 1111111 with agent…") {
		t.Fatalf("menu:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-open", ID: rb0}))
	body := gitSurfaceText(m)
	if !strings.Contains(body, "PLAN REBASE WITH AGENT") || !strings.Contains(body, "read-only") || m.focus != "git:plan-cancel" {
		t.Fatalf("form focus %s:\n%s", m.focus, body)
	}
	m.setFocus(gitPlanInputKey)
	rbType(t, m, "Squash the fixups")
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	j := s.Git.RebasePlan
	if s.Kind != protocol.GitKindRebasePlanStart || j.Base != rb0 || j.Instructions != "Squash the fixups" || j.AgentID == "" {
		t.Fatalf("start %+v", j)
	}
	// Capability fallback: no planning items.
	m.snapshot.Capabilities = []string{"git-writes", "git-history", "git-refs", "git-operations", "git-rebase-interactive"}
	if text := menuText(&Model{menu: m.gitRebaseMenuItems(g, c)}); strings.Contains(text, "with agent") {
		t.Fatalf("fallback:\n%s", text)
	}
}

func TestGitPlanJobStatesAndRevise(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 3, State: protocol.GitProposalInvalid, Turns: 1, Commits: 4,
		Errors: []string{"entry 2: squash needs \x1b[31ma pick"}, ReadOnlyMode: true, Permissions: "plan", DeclinedApprovals: 2})
	text := gitSurfaceText(m)
	for _, want := range []string{"REBASE PLANS", "Invalid plan", "squash needs a pick", "read-only mode (plan)", "2 approvals declined", "Ask the agent to fix…", "End planning job…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "Open in the plan editor") {
		t.Fatalf("invalid offered for loading or unsafe:\n%q", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-revise", ID: "rebase-plan-1"}))
	m.setFocus(gitPlanInputKey)
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	if s.Kind != protocol.GitKindRebasePlanRevise || s.Git.RebasePlan.ExpectedRevision != 3 || s.Git.RebasePlan.JobID != "rebase-plan-1" {
		t.Fatalf("revise %+v", s.Git.RebasePlan)
	}
	// Planning jobs never hold the writer lease.
	m.snapshot.Threads[len(m.snapshot.Threads)-1].State = "running"
	if m.gitJobLeaseHeld("/src/repo") {
		t.Fatal("a planning job blocks writes")
	}
}

func TestGitPlanLoadProposal(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 2, State: protocol.GitProposalProposed, Turns: 1, Commits: 4, Entries: 4,
		Fingerprint: "fp-1", Branch: "feature/init", HeadOid: rb4})
	api.rbProposal = protocol.GitRebaseProposal{JobThreadID: "rebase-plan-1", Base: rb0, State: protocol.GitProposalProposed,
		Summary: protocol.GitRebaseProposalSummary{Revision: 2, Fingerprint: "fp-1"}, CurrentFingerprint: "fp-1", UpdateRefs: true, Rationale: "Tidy \x1b[2Jup",
		Entries: []protocol.GitRebaseEntry{{Action: "pick", Commit: rb1}, {Action: "squash", Commit: rb2, Message: "One\tand two\n"}, {Action: "pick", Commit: rb3}, {Action: "drop", Commit: rb4}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	d := rbDraft(t, m)
	if d.fromAgent == nil || !d.updateRefs || d.build()[1].Message != "One\tand two\n" || rbActions(d) != "pick:1 squash:2 pick:3 drop:4" {
		t.Fatalf("loaded %s %+v", rbActions(d), d.build())
	}
	if text := gitSurfaceText(m); !strings.Contains(text, "Agent's rationale: Tidy up") {
		t.Fatalf("rationale:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if !strings.Contains(menuText(m), "Plan proposed by the agent (revision 2) · unchanged") {
		t.Fatalf("confirm:\n%s", menuText(m))
	}
	m.menu = nil
	m.setFocus(gitRebaseRowKey(3))
	gitKey(t, m, "p")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if !strings.Contains(menuText(m), "· edited by you") {
		t.Fatalf("confirm after edit:\n%s", menuText(m))
	}
	if len(api.sent()) != 0 {
		t.Fatal("something ran from a proposal")
	}
}

func TestGitPlanStaleNeverLoaded(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 2, State: protocol.GitProposalProposed, Fingerprint: "old", Branch: "feature/init", HeadOid: rb3})
	if text := gitSurfaceText(m); !strings.Contains(text, "Stale · the branch changed") || !strings.Contains(text, "Plan again…") || strings.Contains(text, "Open in the plan editor") {
		t.Fatalf("stale job:\n%s", text)
	}
	api.rbProposal = protocol.GitRebaseProposal{Base: rb0, State: protocol.GitProposalStale, Summary: protocol.GitRebaseProposalSummary{Fingerprint: "old"},
		Entries: []protocol.GitRebaseEntry{{Action: "pick", Commit: rb1}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	if key, _ := m.gitTarget(); m.gitRebaseDraftFor(key) != nil {
		t.Fatal("stale proposal loaded")
	}
	// A proposal whose fingerprint differs from the fresh plan read.
	api.rbProposal.State, api.rbProposal.CurrentFingerprint = protocol.GitProposalProposed, ""
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	d := rbDraft(t, m)
	if d.fromAgent != nil || !strings.Contains(d.err, "proposal was not loaded") || rbActions(d) != "pick:1 pick:2 pick:3 pick:4" {
		t.Fatalf("mismatched fingerprint loaded: %s err %q", rbActions(d), d.err)
	}
}

func TestGitPlanDetailsSanitized(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 1, State: protocol.GitProposalTainted, Reason: "index changed"})
	api.rbProposal = protocol.GitRebaseProposal{Base: rb0, State: protocol.GitProposalTainted, Raw: "```tui-rebase-plan\n\x1b]0;x\x07{}\n```",
		Changes: []string{"a.txt modified"}, Enforcement: "unverified agent"}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-details", ID: "rebase-plan-1"}))
	if m.viewer == nil || !strings.Contains(m.viewer.att.Content, "a.txt modified") || !strings.Contains(m.viewer.att.Content, "Enforcement: unverified agent") ||
		strings.Contains(m.viewer.att.Content, "\x1b") {
		t.Fatalf("details %+v", m.viewer)
	}
}

func TestGitPlanVOtherWorktreeJobMislabelled(t *testing.T) {
	m, _ := planModel(t)
	g := m.currentGitView()
	t.Logf("path %q branch %q", g.status.Workspace.Path, g.status.Branch)
	wt := strings.TrimRight(g.status.Workspace.Path, "/") + "/.worktrees/b"
	s := protocol.GitRebaseProposalSummary{Revision: 2, State: protocol.GitProposalProposed, Turns: 1, Commits: 3, Entries: 3,
		Branch: "other-branch", HeadOid: strings.Repeat("7", 40), Fingerprint: "fp-other"}
	m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: "rebase-plan-wt", Title: "Rebase plan", Checkout: wt, AgentID: "agent-x",
		Job: &protocol.ThreadJob{Kind: protocol.ThreadJobRebasePlan, Checkout: wt, Base: rb0, Proposal: &s}})
	m.markDirty()
	text := gitSurfaceText(m)
	i := strings.Index(text, "REBASE PLANS")
	if i < 0 {
		t.Fatalf("not listed:\n%s", text)
	}
	t.Logf("%s", text[i:])
	if strings.Contains(text[i:], "Stale") || strings.Contains(text[i:], "Plan again") || strings.Contains(text[i:], "End planning job") ||
		!strings.Contains(text[i:], "Another checkout: .worktrees/b · other-branch") {
		t.Errorf("another worktree's job shown as this checkout's")
	}
}

func TestGitPlanVAgentTextSanitized(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 1, State: protocol.GitProposalProposed, Turns: 1, Commits: 4, Fingerprint: "fp-1",
		Reason: "r\x1b]0;x\x07", Permissions: "plan\x1b[2J", Branch: "feature/init", HeadOid: rb4})
	api.rbProposal = protocol.GitRebaseProposal{JobThreadID: "rebase-plan-1", Base: rb0, State: protocol.GitProposalProposed,
		Summary:   protocol.GitRebaseProposalSummary{Revision: 1, State: "proposed\x1b[31m", Fingerprint: "fp-1", Branch: "b\x1b]8;;http://x\x07", HeadOid: rb4},
		Rationale: "why\x1b[2J\u202e", Raw: "raw \x1b]52;c;AAAA\x07 answer", Changes: []string{"a\x1b[1m"}, StopReason: "end\x1b[0m",
		Enforcement: "enf\x07", Entries: []protocol.GitRebaseEntry{{Action: "reword", Commit: rb1, Message: "One\x1b[31m red\n\n\tbody"}, {Action: "pick", Commit: rb2}, {Action: "pick", Commit: rb3}, {Action: "pick", Commit: rb4}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-details", ID: "rebase-plan-1"}))
	if m.viewer == nil {
		t.Fatal("no viewer")
	}
	c := m.viewer.att.Content
	if strings.ContainsAny(c, "\x1b\x07\u202e") {
		t.Errorf("unsanitized details: %q", c)
	}
	out := m.View().Content
	for _, bad := range []string{"\x1b]0;", "\x1b]52", "\x1b]8;;http", "\x1b[2J", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Errorf("rendered contains %q", bad)
		}
	}
	// Load: message byte-exact.
	m.viewer = nil
	m.setFocus("git-refresh")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	d := rbDraft(t, m)
	t.Logf("err %q notice %q", d.err, d.notice)
	if got := d.build()[0].Message; got != "One\x1b[31m red\n\n\tbody" {
		t.Errorf("loaded reword %q", got)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	t.Logf("confirm:\n%s", menuText(m))
	if len(api.sent()) != 0 {
		t.Errorf("sent without confirmation")
	}
}

func TestGitPlanVRefreshKeepsAgentProvenance(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 1, State: protocol.GitProposalProposed, Turns: 1, Commits: 4, Fingerprint: "fp-1", Branch: "feature/init", HeadOid: rb4})
	api.rbProposal = protocol.GitRebaseProposal{JobThreadID: "rebase-plan-1", Base: rb0, State: protocol.GitProposalProposed, UpdateRefs: true,
		Summary: protocol.GitRebaseProposalSummary{Revision: 1, State: "proposed", Fingerprint: "fp-1", Branch: "feature/init", HeadOid: rb4},
		Entries: []protocol.GitRebaseEntry{{Action: "pick", Commit: rb1}, {Action: "fixup", Commit: rb2}, {Action: "pick", Commit: rb3}, {Action: "pick", Commit: rb4}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	d := rbDraft(t, m)
	// The repository changes: another branch now points into the range.
	api.rbPlan.Fingerprint = "fp-2"
	api.rbPlan.UpdateRefs = append(api.rbPlan.UpdateRefs, protocol.GitRebaseUpdateRef{Ref: "refs/heads/newcomer", Oid: rb3})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	t.Logf("notice %q updateRefs=%v\n%s", d.notice, d.updateRefs, menuText(m))
	if strings.Contains(menuText(m), "unchanged") || !strings.Contains(menuText(m), "for an earlier state of the branch") {
		t.Errorf("after the plan changed the confirmation still reports the agent's plan unchanged, now also moving newcomer")
	}
}

func TestGitPlanVInstructionLostOnRefusal(t *testing.T) {
	m, api := planModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-open", ID: rb0}))
	m.setFocus(gitPlanInputKey)
	rbType(t, m, "Keep my long careful instruction")
	api.wmu.Lock()
	api.reply = func(c protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "invalid", Message: "instructions must be UTF-8 text up to 4 KiB"}
	}
	api.wmu.Unlock()
	gitKey(t, m, "enter")
	key, _ := m.gitTarget()
	f := m.gitPlanFormFor(key)
	if f == nil || m.gitPlanInput().Value() != "Keep my long careful instruction" || !strings.Contains(f.err, "4 KiB") {
		t.Errorf("instruction text discarded when the start is refused")
	}
	// The byte limit is enforced locally (runes are not bytes).
	m.gitPlanInput().SetValue(strings.Repeat("é", 2100))
	sent := len(api.sent())
	m.setFocus(gitPlanInputKey)
	gitKey(t, m, "enter")
	if len(api.sent()) != sent || !strings.Contains(gitSurfaceText(m), "4200 / 4096 bytes") {
		t.Errorf("over-long instruction sent or not counted")
	}
}

func TestGitPlanChainEditorSanitized(t *testing.T) {
	m, api := planModel(t)
	addPlanJob(m, protocol.GitRebaseProposalSummary{Revision: 1, State: protocol.GitProposalProposed, Fingerprint: "fp-1", Branch: "feature/init", HeadOid: rb4})
	evil := "Evil \u202e txt.exe\x1b]0;x\x07 end"
	api.rbProposal = protocol.GitRebaseProposal{Base: rb0, State: protocol.GitProposalProposed, CurrentFingerprint: "fp-1",
		Summary: protocol.GitRebaseProposalSummary{Revision: 1, Fingerprint: "fp-1"},
		Entries: []protocol.GitRebaseEntry{{Action: "reword", Commit: rb1, Message: evil}, {Action: "squash", Commit: rb2}, {Action: "pick", Commit: rb3}, {Action: "pick", Commit: rb4}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-load", ID: "rebase-plan-1"}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "m")
	if m.focus != gitRebaseMsgKey {
		t.Fatalf("chain editor not open: %s", m.focus)
	}
	out := m.View().Content
	if strings.Contains(out, "\u202e") || strings.Contains(out, "]0;x") || strings.Contains(m.gitRebaseMsg().Value(), "\u202e") {
		t.Fatalf("unsafe chain display %q", m.gitRebaseMsg().Value())
	}
	gitKey(t, m, "esc")
	if got := d.build()[1].Message; !strings.HasPrefix(got, evil) {
		t.Fatalf("raw chain message altered: %q", got)
	}
}

func TestGitPlanFormKeptAcrossThreadSwitch(t *testing.T) {
	m, api := planModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-plan-open", ID: rb0}))
	keyA, _ := m.gitTarget()
	m.setFocus(gitPlanInputKey)
	rbType(t, m, "Typed in A")
	cur := m.state.Active
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != cur && th.Job == nil {
			other = th.ID
		}
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: other}))
	if keyB, _ := m.gitTarget(); keyB == keyA {
		t.Skip("threads share a Git target")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: cur}))
	if f := m.gitPlanFormFor(keyA); f == nil || m.gitPlanInput().Value() != "Typed in A" {
		t.Fatalf("form or text lost across a thread switch: %q", m.gitPlanInput().Value())
	}
	// A refusal that arrives while another thread is shown returns to A's
	// form.
	api.wmu.Lock()
	api.reply = func(c protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "stale_plan", Message: "moved"}
	}
	api.wmu.Unlock()
	m.setFocus(gitPlanInputKey)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: other}))
	gitWriteSettle(t, m, cmd)
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: cur}))
	if f := m.gitPlanFormFor(keyA); f == nil || f.err == "" || m.gitPlanInput().Value() != "Typed in A" {
		t.Fatalf("refused form lost: %+v input %q", f, m.gitPlanInput().Value())
	}
}
