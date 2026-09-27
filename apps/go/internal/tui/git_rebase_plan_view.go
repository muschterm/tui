package tui

import (
	"strconv"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_rebase_plan_view.go paints the planning job form and the checkout's
// planning jobs (git_rebase_plan.go).

// gitPlanFormBlocks is the start or revise form; Cancel is focused first.
func (m *Model) gitPlanFormBlocks(f *gitPlanForm) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action, help string) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: help}}
	}
	heading, verb := "Plan rebase with agent", "Start planning"
	if f.revise {
		heading, verb = "Ask the agent to revise", "Ask again"
	}
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: heading},
		text("The agent reads the commits and proposes a plan. It never changes the checkout or runs the rebase; you review the plan in the editor and start it yourself.", p.muted),
		text("Built-in Claude and Codex bridges open the agent read-only (Claude plan mode, Codex read-only sandbox); for other agents only declined approvals and a checkout comparison after each turn apply.", p.muted),
		button("Cancel", m.icon("close"), "git:plan-cancel", action{Kind: "git-plan-cancel-form"}, "Cancel · nothing starts"), gap}
	if f.err != "" {
		b = append(b, text(f.err, p.red))
	}
	b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Base", value: gitPlanRev(f.base)})
	if f.onto != "" {
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Onto", value: gitPlanRev(f.onto)})
	}
	if f.revise {
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Revision", value: strconv.FormatInt(f.revision, 10)},
			text("The latest proposal's errors or checkout changes are sent with your instruction", p.muted))
	} else {
		b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Agent"})
		agents := m.gitPlanAgents()
		if len(agents) == 0 {
			b = append(b, text("No agent is available", p.gold))
		}
		for _, a := range agents {
			glyph := m.icon("radio")
			if a.ID == f.agentID {
				glyph = m.icon("radio-on")
			}
			b = append(b, button(safe(singleLine(a.Name)), glyph, "git:plan-agent:"+a.ID, action{Kind: "git-plan-agent", ID: a.ID}, "Use "+safe(singleLine(a.Name))))
		}
		if a, ok := m.agentByID(f.agentID); ok {
			set := reconcileJobSettings(a, f.settings)
			c := agentConfigFor(a).forModel(set.Model)
			for _, field := range []string{"model", "effort", "permissions"} {
				if _, has := c.option(field); has && a.Kind != "fixture" {
					b = append(b, button(title(field)+" · "+m.newThreadDefaultFieldValue(a, field, set), m.icon("settings"), "git:plan-setting:"+field,
						action{Kind: "git-plan-setting", ID: field}, "Choose the job's "+field))
					continue
				}
				b = append(b, surfaceBlock{kind: surfacePairBlock, label: title(field), value: "Agent default", ink: p.muted})
			}
			b = append(b, text("Built-in bridges replace the permissions with their read-only mode", p.muted))
		}
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Instructions"},
		surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "pinput", key: gitPlanInputKey, action: action{Kind: "git-plan-input"},
			help: "What the plan should do (optional) · Enter sends · Esc cancels", text: m.gitPlanInput().Value()}},
		gitPlanCounter(m), gap,
		button(verb, m.icon("rocket"), "git:plan-send", action{Kind: "git-plan-send"}, verb))
	return b
}

// gitPlanStateCopy names a proposal state.
func gitPlanStateCopy(state string) (string, string) {
	switch state {
	case protocol.GitProposalRunning:
		return "Planning…", "pending"
	case protocol.GitProposalProposed:
		return "Plan proposed", "succeeded"
	case protocol.GitProposalInvalid:
		return "Invalid plan", "failed"
	case protocol.GitProposalTainted:
		return "Checkout changed during the turn", "failed"
	case protocol.GitProposalFailed:
		return "Planning failed", "failed"
	case protocol.GitProposalCancelled:
		return "Planning cancelled", "blocked"
	case protocol.GitProposalStale:
		return "Stale · the branch changed", "stale"
	}
	return "Unknown state", "blocked"
}

// gitPlanJobBlocks lists the checkout's planning jobs with their actions.
func (m *Model) gitPlanJobBlocks(g *gitView) []surfaceBlock {
	jobs := m.gitPlanJobs(g)
	if len(jobs) == 0 || !m.gitPlanEnabled() {
		return nil
	}
	p := m.colors()
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: m.icon(glyph), key: k, action: a, help: label}}
	}
	b := []surfaceBlock{{kind: surfaceGapBlock}, {kind: surfaceHeadingBlock, label: "Rebase plans", value: strconv.Itoa(len(jobs))}}
	for _, t := range jobs {
		s := protocol.GitRebaseProposalSummary{State: protocol.GitProposalRunning}
		if t.Job.Proposal != nil {
			s = *t.Job.Proposal
		}
		state := s.State
		here := gitPlanHere(t, g)
		stale := here && state == protocol.GitProposalProposed && g.status != nil && s.HeadOid != "" && (g.status.HeadOid != s.HeadOid || g.status.Branch != s.Branch)
		if stale {
			state = protocol.GitProposalStale
		}
		label, mark := gitPlanStateCopy(state)
		glyph, ink := panelStatusMark(m, mark)
		agent := safe(singleLine(t.AgentID))
		if a, ok := m.agentByID(t.AgentID); ok {
			agent = safe(singleLine(a.Name))
		}
		b = append(b, surfaceBlock{kind: surfaceStatusBlock, label: label, value: agent, glyph: glyph, ink: ink})
		if !here {
			where := "Another checkout: " + gitShortCheckout(t.Job.Checkout)
			if s.Branch != "" {
				where += " · " + safe(singleLine(s.Branch))
			}
			b = append(b, text(where+" · open that checkout to act on it", p.gold))
		}
		info := "Base " + gitPlanRev(t.Job.Base) + onto(gitPlanRev(t.Job.Onto)) + " · " + plural(s.Commits, "commit")
		if s.Entries > 0 {
			info += " · " + plural(s.Entries, "entry")
		}
		info += " · turn " + strconv.Itoa(s.Turns) + "/" + strconv.Itoa(protocol.GitRebasePlanJobTurnsMax)
		b = append(b, text(info, p.muted))
		for i, e := range s.Errors {
			if i == 3 {
				b = append(b, text("and "+strconv.Itoa(len(s.Errors)-3)+" more · Details", p.muted))
				break
			}
			b = append(b, text(safe(singleLine(e)), p.red))
		}
		if r := safe(singleLine(s.Reason)); r != "" {
			b = append(b, text(r, p.gold))
		}
		if s.Concurrent {
			b = append(b, text("Other Git writes or agent turns ran in this checkout meanwhile", p.gold))
		}
		if stale {
			b = append(b, text("The branch moved since the plan was pinned · start a new plan", p.gold))
		}
		b = append(b, text(gitPlanEnforcementCopy(s), p.muted))
		id := t.ID
		if !here {
			b = append(b, button("Details", "activity", "git:plan-details:"+id, action{Kind: "git-plan-details", ID: id}),
				button("Transcript", "terminal", "git:plan-transcript:"+id, action{Kind: "git-plan-transcript", ID: id}))
			continue
		}
		switch state {
		case protocol.GitProposalRunning:
			b = append(b, button("Stop the agent", "stop", "git:plan-stop:"+id, action{Kind: "git-plan-stop", ID: id}))
		case protocol.GitProposalProposed:
			b = append(b, button("Open in the plan editor", "git", "git:plan-load:"+id, action{Kind: "git-plan-load", ID: id}))
		case protocol.GitProposalStale:
			b = append(b, button("Plan again…", "rocket", "git:plan-replan:"+id, action{Kind: "git-plan-replan", ID: id}))
		}
		if state == protocol.GitProposalInvalid || state == protocol.GitProposalTainted || state == protocol.GitProposalFailed || state == protocol.GitProposalCancelled {
			if s.Turns < protocol.GitRebasePlanJobTurnsMax {
				b = append(b, button("Ask the agent to fix…", "compose", "git:plan-revise:"+id, action{Kind: "git-plan-revise", ID: id}))
			} else {
				b = append(b, text("No turns left · plan again", p.muted),
					button("Plan again…", "rocket", "git:plan-replan:"+id, action{Kind: "git-plan-replan", ID: id}))
			}
		}
		b = append(b, button("Details", "activity", "git:plan-details:"+id, action{Kind: "git-plan-details", ID: id}),
			button("Transcript", "terminal", "git:plan-transcript:"+id, action{Kind: "git-plan-transcript", ID: id}))
		if state != protocol.GitProposalRunning {
			b = append(b, button("End planning job…", "trash", "git:plan-end:"+id, action{Kind: "git-plan-end", ID: id}))
		}
	}
	return b
}

// paintGitPlanInput paints the instruction input.
func (m *Model) paintGitPlanInput(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	cv := m.containerStyle(m.focus == r.key, p.text, p.input)
	border := componentBorder(squareOutline, m.plainIcons)
	if width < 6 {
		return
	}
	f.text(x, y, 1, border.Left, cv.border, p.panel)
	f.text(x+width-1, y, 1, border.Right, cv.border, p.panel)
	f.text(x+1, y, width-2, "", p.text, p.input)
	iw := width - 4
	a := m.gitPlanInput()
	m.gitMessageStylesFor(a, iw)
	if a.Width() != iw {
		a.SetWidth(iw)
	}
	if a.Height() != 1 {
		a.SetHeight(1)
	}
	if f.rows != nil {
		if lines := strings.Split(a.View(), "\n"); len(lines) > 0 {
			f.put(shell.Rect{X: x + 2, Y: y, W: iw, H: 1}, lines[0])
		}
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x + 1, Y: y, W: width - 2, H: 1}, Action: r.action, Label: r.help, Key: r.key})
}

// gitPlanRev shortens a full hash for display (Details keep it whole).
func gitPlanRev(r string) string {
	r = safe(singleLine(r))
	if len(r) >= 40 && strings.Trim(r, "0123456789abcdef") == "" {
		return gitShort(r)
	}
	return r
}

// gitPlanCounter shows the instruction's size against the server limit.
func gitPlanCounter(m *Model) surfaceBlock {
	p := m.colors()
	n := len(strings.TrimSpace(m.gitPlanInput().Value()))
	ink := p.muted
	if n > gitPlanInstructionMax {
		ink = p.red
	}
	return surfaceBlock{kind: surfaceTextBlock, value: strconv.Itoa(n) + " / " + strconv.Itoa(gitPlanInstructionMax) + " bytes", ink: ink}
}
