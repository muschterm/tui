package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_job_view.go paints the resolution job start form, the job section of
// the operation panel and review-mode rows (git_job.go).

// gitJobStartBlocks is the start form: agent, its settings, paths and
// instructions; Cancel is focused first.
func (m *Model) gitJobStartBlocks(s *gitJobStart) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	button := func(label, glyph, k string, a action, help string) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: help}}
	}
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: "Resolve with agent", value: safe(singleLine(s.state.Kind))},
		{kind: surfaceTextBlock, value: "The agent edits the chosen files and may run checks, then stops for your review. It never stages, continues or aborts.", ink: p.muted},
		button("Cancel", m.icon("close"), "git:job-cancel", action{Kind: "git-job-cancel-form"}, "Cancel · nothing starts"), gap,
		{kind: surfaceHeadingBlock, label: "Agent"}}
	agents := m.gitJobAgents()
	if len(agents) == 0 {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "No agent is available", ink: p.gold})
	}
	for _, a := range agents {
		glyph := m.icon("radio")
		if a.ID == s.agentID {
			glyph = m.icon("radio-on")
		}
		b = append(b, button(safe(singleLine(a.Name)), glyph, "git:job-agent:"+a.ID, action{Kind: "git-job-agent", ID: a.ID}, "Use "+safe(singleLine(a.Name))))
	}
	if a, ok := m.agentByID(s.agentID); ok {
		// Render the reconciliation Start sends, so what is shown is sent.
		set := reconcileJobSettings(a, s.settings)
		c := agentConfigFor(a).forModel(set.Model)
		for _, f := range []string{"model", "effort", "permissions"} {
			if _, has := c.option(f); has && a.Kind != "fixture" {
				b = append(b, button(title(f)+" · "+m.newThreadDefaultFieldValue(a, f, set), m.icon("settings"), "git:job-setting:"+f,
					action{Kind: "git-job-setting", ID: f}, "Choose the job's "+f))
				continue
			}
			b = append(b, surfaceBlock{kind: surfacePairBlock, label: title(f), value: "Agent default", ink: p.muted})
		}
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Paths", value: strconv.Itoa(len(s.paths))})
	if s.state.ConflictsTruncated {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Some conflicts are not listed · the job covers only the paths chosen here", ink: p.gold})
	}
	if len(s.paths) < len(s.state.Conflicts) {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Submodule conflicts are left out · resolve them in a terminal", ink: p.muted})
	}
	for _, path := range s.paths {
		glyph := m.icon("checkbox")
		if s.selected[path] {
			glyph = m.icon("checkbox-on")
		}
		b = append(b, button(truncatePathLeft(safe(singleLine(path)), 60), glyph, "git:job-path:"+path, action{Kind: "git-job-path", ID: path}, "Include or leave out "+safe(singleLine(path))))
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Instructions"},
		surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "jinput", key: gitJobInputKey, action: action{Kind: "git-job-input"},
			help: "Optional instructions · Enter starts · Esc cancels", text: m.gitJobInput().Value()}}, gap,
		button("Start resolution job", m.icon("rocket"), "git:job-start", action{Kind: "git-job-start"}, "Start the agent on the chosen paths"))
	return b
}

// gitJobBlocks is the operation panel's job section: the attached job's
// status and controls, then review mode.
func (m *Model) gitJobBlocks(o *protocol.GitOperationState) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	r := o.Review
	if r == nil {
		if len(o.Conflicts) == 0 || !m.gitJobsEnabled() {
			return nil
		}
		return []surfaceBlock{gap, button("Resolve with agent…", m.icon("agents"), "git:job-open", action{Kind: "git-job-open"})}
	}
	b := []surfaceBlock{gap, {kind: surfaceHeadingBlock, label: "Resolution job"}}
	t, known := m.gitJobThread(o)
	switch r.State {
	case protocol.GitReviewRunning:
		state := "Thinking"
		if known && t.State == "waiting" {
			state = "Waiting"
		}
		label := "Agent working"
		if known {
			label = safe(singleLine(t.Agent)) + " working"
		}
		b = append(b, statusBlock(m, strings.TrimSpace(label), strings.ToLower(state), false))
	case protocol.GitReviewInterrupted:
		b = append(b, statusBlock(m, "Agent stopped before finishing", "interrupted", false))
	default:
		b = append(b, statusBlock(m, "Agent finished · review its changes", "ready", false))
	}
	if known {
		if v := m.gitJobRecordedSettings(t); v != "" {
			b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Settings", value: v})
		}
		for _, req := range t.Requests {
			if req.State == "pending" {
				label := "Answer · "
				if req.Kind == "approval" {
					label = "Approve or deny · "
				}
				b = append(b, button(label+safe(singleLine(req.Title)), m.icon("attention"), "git:job-req:"+req.ID, action{Kind: "git-job-answer", ID: req.ID}))
			}
		}
		b = append(b, button("Open agent transcript", m.icon("thread"), "git:job-transcript", action{Kind: "git-job-transcript"}))
	}
	if r.State == protocol.GitReviewRunning {
		b = append(b, button("Stop agent", m.icon("stop"), "git:job-stop", action{Kind: "git-job-stop"}))
	} else if m.gitJ.followup {
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "jinput", key: gitJobInputKey, action: action{Kind: "git-job-input"},
			help: "Follow-up · Enter sends · Esc cancels", text: m.gitJobInput().Value()}},
			button("Send follow-up", m.icon("send"), "git:job-followup-send", action{Kind: "git-job-followup-send"}))
	} else {
		b = append(b, button("Follow up…", m.icon("send"), "git:job-followup", action{Kind: "git-job-followup"}))
	}
	b = append(b, button("End job…", m.icon("close"), "git:job-end", action{Kind: "git-job-end"}))
	if r.State == protocol.GitReviewRunning {
		return b
	}
	// Review mode.
	if r.Stale {
		b = append(b, gap, text("The repository changed since this review · refresh before deciding", p.gold))
	}
	b = append(b, button("Refresh review", m.icon("refresh"), "git:job-refresh", action{Kind: "git-job-refresh"}))
	if len(r.Violations) > 0 {
		b = append(b, gap, text("Outside the job's scope (reported, not repaired):", p.red))
		for _, v := range r.Violations {
			b = append(b, text("  "+safe(singleLine(v)), p.red))
		}
	}
	if r.Incomplete {
		b = append(b, text("The review is incomplete · some paths could not be compared", p.gold))
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Review", value: strconv.Itoa(len(r.Items))})
	for _, it := range r.Items {
		path := safe(singleLine(it.Path))
		row := &gitRow{kind: "jitem", text: path, key: "git:jitem:" + it.Path, action: action{Kind: "git-job-view", ID: it.Path},
			help: "View the agent's changes · " + path + " · a accept · x reject", when: gitReviewBadges(it), subject: safe(singleLine(it.Decision))}
		row.slots = true
		if it.Decision == "" && !r.Stale {
			row.controls = [2]*gitControl{
				{glyph: m.icon("check"), key: "git-jact:accept:" + it.Path, help: "Accept · " + path + " (a)", action: action{Kind: "git-job-accept", ID: it.Path}},
				{glyph: m.icon("reopen"), key: "git-jact:reject:" + it.Path, help: "Reject · restore the copy before the agent ran (x)", action: action{Kind: "git-job-reject", ID: it.Path}},
			}
		}
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: row})
	}
	return b
}

// gitReviewBadges are an item's compact facts.
func gitReviewBadges(it protocol.GitResolveItem) string {
	var f []string
	switch {
	case it.Unknown:
		f = append(f, "unknown")
	case !it.Changed && !it.Staged && !it.Deleted:
		f = append(f, "unchanged")
	}
	if it.Changed {
		f = append(f, "changed")
	}
	if it.Staged {
		f = append(f, "staged")
	}
	if it.Deleted {
		f = append(f, "deleted")
	}
	if it.HasMarkers {
		f = append(f, "markers")
	}
	if it.Binary {
		f = append(f, "binary")
	}
	return strings.Join(f, " ")
}

// paintGitJobRow paints review item rows and the job input.
func (m *Model) paintGitJobRow(f *frame, x, y, width int, r *gitRow, v componentVisual) {
	p := m.colors()
	bg := v.background
	switch r.kind {
	case "jitem":
		cx, end := x+1, x+width-1
		end -= 2 * gitSlotWidth
		m.paintGitControls(f, end+1, y, r, bg)
		end--
		state := r.subject
		if state == "" {
			state = r.when
		}
		ink := p.muted
		switch {
		case r.subject == "accepted":
			ink = p.green
		case r.subject == "rejected":
			ink = p.gold
		case strings.Contains(r.when, "markers") || strings.Contains(r.when, "unknown"):
			ink = p.gold
		}
		if sw := ansi.StringWidth(state); sw > 0 && sw+8 < end-cx {
			f.text(end-sw, y, sw, state, ink, bg)
			end -= sw + 2
		}
		f.componentText(cx, y, max(0, end-cx), truncatePathLeft(r.text, end-cx), v)
	case "jinput":
		focused := m.focus == r.key
		cv := m.containerStyle(focused, p.text, p.input)
		border := componentBorder(squareOutline, m.plainIcons)
		if width < 6 {
			return
		}
		f.text(x, y, 1, border.Left, cv.border, p.panel)
		f.text(x+width-1, y, 1, border.Right, cv.border, p.panel)
		f.text(x+1, y, width-2, "", p.text, p.input)
		iw := width - 4
		a := m.gitJobInput()
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
}

// gitJobRecordedSettings summarizes the job thread's settings once: the
// effective (acknowledged) value where reported, else the selected one, with
// a mismatch labelled.
func (m *Model) gitJobRecordedSettings(t protocol.Thread) string {
	a, _ := m.threadAgent(t)
	var parts []string
	for _, f := range []string{"model", "effort", "permissions"} {
		sel, eff := settingValue(t.Selected, f), settingValue(t.Effective, f)
		if eff == "unavailable" {
			eff = ""
		}
		if sel == "unavailable" {
			sel = ""
		}
		switch {
		case eff != "":
			label := m.newThreadDefaultFieldValue(a, f, t.Effective)
			if sel != "" && sel != eff {
				label += " (selected " + m.newThreadDefaultFieldValue(a, f, t.Selected) + ")"
			}
			parts = append(parts, label)
		case sel != "":
			parts = append(parts, m.newThreadDefaultFieldValue(a, f, t.Selected))
		}
	}
	return strings.Join(parts, " · ")
}
