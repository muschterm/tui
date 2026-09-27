package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_ref_view.go paints the ref and remote actions (git_ref.go): the
// heading's Fetch, Pull, Push and Refresh icon controls, sync progress with
// Cancel, the new-branch editor, results, and the branch rows' reserved
// Switch and menu slots.

// gitWouldOverwriteShown bounds the listed would_overwrite paths.
const gitWouldOverwriteShown = 8

// gitHeadingBlock is the GIT heading with its glyph-only icon controls, in
// fixed order Fetch, Fetch & prune, Pull, Push, Refresh at the right.
func (m *Model) gitHeadingBlock(key string, g *gitView) surfaceBlock {
	row := &gitRow{kind: "heading", text: "Git"}
	tool := func(kind, glyph, k, verb, keyHint string) *gitControl {
		help := verb + " (" + keyHint + ")"
		disabled := false
		if g == nil || g.status == nil {
			help, disabled = verb+" unavailable · Git status not loaded", true
		} else if reason := m.gitRefBlock(key, g, kind); reason != "" {
			help, disabled = verb+" unavailable · "+reason, true
		}
		return &gitControl{glyph: glyph, key: k, help: help, action: action{Kind: "git-" + strings.TrimPrefix(kind, "git.")}, disabled: disabled}
	}
	remote, up := "", ""
	if g != nil && g.status != nil {
		remote, up = safe(singleLine(gitUpstreamRemote(g.status.Upstream))), safe(singleLine(g.status.Upstream))
	}
	fetchVerb := strings.TrimSpace("Fetch " + remote)
	if m.gitFetchAllEnabled() {
		fetchVerb = "Fetch all remotes"
	}
	fetch := tool(protocol.GitKindFetch, m.icon("fetch"), "git:sync:fetch", fetchVerb, "f")
	prune := tool(protocol.GitKindFetch, m.icon("fetch-prune"), "git:sync:fetch-prune", "Fetch & prune all remotes", "F")
	prune.action = action{Kind: "git-fetch-prune"}
	if !m.gitFetchAllEnabled() {
		prune.help, prune.disabled = "Fetch & prune unavailable · Needs a newer server", true
	}
	row.tools = []*gitControl{
		fetch,
		prune,
		tool(protocol.GitKindPull, m.icon("pull"), "git:sync:pull", strings.TrimSpace("Pull "+up)+" · fast-forward only", "p"),
		tool(protocol.GitKindPush, m.icon("push"), "git:sync:push", strings.TrimSpace("Push to "+up), "P"),
		{glyph: m.icon("refresh"), key: "git-refresh", help: "Refresh Git status · read-only", action: action{Kind: "git-refresh"}},
	}
	return surfaceBlock{kind: surfaceGitBlock, git: row}
}

// gitSyncVerb is a running fetch, pull or push's progress title.
func gitSyncVerb(st *gitWriteState) string {
	sync := st.cmd.Git.Sync
	switch st.cmd.Kind {
	case protocol.GitKindFetch:
		if st.label != "" {
			return "Fetching " + safe(singleLine(st.label)) + "…"
		}
		return "Fetching…"
	case protocol.GitKindPull:
		return "Pulling " + safe(singleLine(sync.Upstream)) + "…"
	case protocol.GitKindPush:
		return "Pushing " + safe(singleLine(sync.ExpectedBranch)) + "…"
	}
	return gitProgressVerb(st.cmd.Kind)
}

// gitProgressBlocks is the running write's line: progress percentage and
// phase from the snapshot's GitOp, and Cancel while it is cancellable.
func (m *Model) gitProgressBlocks(key string, g *gitView, reason string) []surfaceBlock {
	p := m.colors()
	label := reason
	var op *protocol.GitOp
	if st := m.gitWriteFor(key); st != nil && st.running {
		for i := range m.snapshot.GitOps {
			if m.snapshot.GitOps[i].CommandID == st.cmd.ID {
				op = &m.snapshot.GitOps[i]
			}
		}
		if gitSyncKind(st.cmd.Kind) {
			label = gitSyncVerb(st)
		}
	} else if g != nil && g.status != nil {
		op = m.gitRunningOp(g.status.Workspace.Path)
	}
	if op != nil && op.State != protocol.GitStateRunning {
		op = nil
	}
	if op != nil && op.Progress != nil && op.Progress.Percent >= 0 {
		label += " " + strconv.Itoa(min(100, op.Progress.Percent)) + "%"
	}
	b := []surfaceBlock{statusBlock(m, label, "running", false)}
	if op != nil && op.Progress != nil && op.Progress.Phase != "" {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: safe(singleLine(op.Progress.Phase)), ink: p.muted})
	}
	if op != nil && op.Cancellable {
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: "Cancel", mark: m.icon("close"), key: "git:cancel",
			action: action{Kind: "git-sync-cancel", ID: op.CommandID}, help: "Cancel " + strings.TrimSuffix(strings.ToLower(label), "…")}})
	}
	return b
}

// gitRefResultBlocks is a finished ref or remote action's outcome.
func (m *Model) gitRefResultBlocks(key string, g *gitView, st *gitWriteState) []surfaceBlock {
	p := m.colors()
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	mark := func(state, text string) surfaceBlock {
		glyph, ink := panelStatusMark(m, state)
		return surfaceBlock{kind: surfaceStatusBlock, label: text, glyph: glyph, ink: ink}
	}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	r := st.result
	var b []surfaceBlock
	code := ""
	if r != nil {
		code = r.Code
	}
	succeeded := r != nil && r.State == protocol.GitStateSucceeded
	if gitOperationKind(st.cmd.Kind) && st.transport == "" {
		return m.gitOperationResultBlocks(st)
	}
	if gitConflictKind(st.cmd.Kind) && r != nil && r.State == protocol.GitStateSucceeded {
		glyph, ink := panelStatusMark(m, "succeeded")
		b := []surfaceBlock{{kind: surfaceStatusBlock, label: gitConflictDone(st), glyph: glyph, ink: ink}}
		if st.warning != "" {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: st.warning, ink: m.colors().gold})
		}
		return append(b, m.gitConflictResultBlocks(st)...)
	}
	switch {
	case st.ack != nil:
		b = append(b, text(st.failure, p.gold),
			button("Review · "+gitAckVerb(st.ack), m.icon("attention"), "git:ack-open", action{Kind: "git-ack-open"}))
	case r != nil && st.cmd.Kind == protocol.GitKindPull && r.State == protocol.GitStateOutcomeUnknown && r.Integration != nil && r.Integration.State == protocol.GitIntegrationFastForward:
		b = append(b, text(safe(singleLine(st.cmd.Git.Sync.ExpectedBranch))+" may have moved to "+gitShort(r.Integration.To)+" · result unknown · refresh and check", p.gold))
	case r != nil && st.cmd.Kind == protocol.GitKindPull && r.State != protocol.GitStateSucceeded && r.Integration != nil && r.Integration.State == protocol.GitIntegrationFastForward:
		b = append(b, text("Fetched; the fast-forward of "+safe(singleLine(st.cmd.Git.Sync.ExpectedBranch))+" did not complete · "+st.failure, p.red))
	case succeeded && st.cmd.Kind == protocol.GitKindPush && r.Code == "pushed_newer_head" && r.Push != nil:
		b = append(b, mark("succeeded", "Pushed "+gitShort(r.Push.NewOid)+" (newer than shown) to "+safe(singleLine(st.cmd.Git.Sync.Upstream))))
	case succeeded && st.cmd.Kind == protocol.GitKindPull && r.Integration != nil && r.Integration.State == protocol.GitIntegrationAhead:
		line := "Ahead of upstream"
		if g != nil && g.status != nil && g.status.Ahead > 0 {
			line += " by " + strconv.Itoa(g.status.Ahead)
		}
		b = append(b, mark("succeeded", line))
	case succeeded:
		b = append(b, mark("succeeded", m.gitRefDoneCopy(st, r)))
		switch {
		case r.Ref != nil && st.cmd.Kind == protocol.GitKindSwitch && r.Ref.Carried > 0:
			b = append(b, text("Carried "+plural(r.Ref.Carried, "change"), p.muted))
		case r.Ref != nil && st.cmd.Kind == protocol.GitKindResetSoft && r.Ref.PreviousHead != "":
			b = append(b, text("Previous tip "+gitShort(r.Ref.PreviousHead), p.muted))
			if _, ok := m.gitUndoable(st); ok && !st.undo {
				b = append(b, button("Undo · soft reset back to "+gitShort(r.Ref.PreviousHead), m.icon("reopen"), "git:reset-undo", action{Kind: "git-reset-undo"}))
			}
		}
	case r != nil && st.cmd.Kind == protocol.GitKindPull && r.Fetch != nil && r.Fetch.State == protocol.GitFetchSucceeded && r.Integration != nil && r.Integration.State == protocol.GitIntegrationDiverged:
		line := "Diverged from upstream"
		if g != nil && g.status != nil && (g.status.Ahead > 0 || g.status.Behind > 0) {
			line = fmt.Sprintf("Diverged: %d ahead, %d behind", g.status.Ahead, g.status.Behind)
		}
		b = append(b, mark("blocked", line), text("Fetched, not integrated · merge or rebase explicitly", p.muted))
		up := safe(singleLine(st.cmd.Git.Sync.Upstream))
		base := r.Integration.To
		if base == "" {
			base = "refs/remotes/" + st.cmd.Git.Sync.Upstream
		}
		b = append(b, button("Compare with "+up, m.icon("git"), "git:pull-compare", action{Kind: "git-compare", ID: base, Value: up}))
		if m.gitOperationsEnabled() {
			// The full upstream ref from the log (HEAD@{upstream}), else
			// the fetched commit itself; never a guessed refs/remotes name.
			ref := r.Integration.To
			if g != nil && g.log != nil && strings.HasPrefix(g.log.Upstream, "refs/") {
				ref = g.log.Upstream
			}
			b = append(b, button("Merge "+up+"…", m.icon("git"), "git:pull-merge", action{Kind: "git-integrate", Value: protocol.GitOperationMerge, ID: ref}),
				button("Rebase onto "+up+"…", m.icon("git"), "git:pull-rebase", action{Kind: "git-integrate", Value: protocol.GitOperationRebase, ID: ref}))
		}
	case r != nil && st.cmd.Kind == protocol.GitKindPull && r.Fetch != nil && r.Fetch.State == protocol.GitFetchSucceeded:
		remote := safe(singleLine(r.Fetch.Remote))
		b = append(b, text("Fetched "+remote+", not integrated · "+st.failure, p.red))
	case r != nil && st.cmd.Kind == protocol.GitKindPush && r.Push != nil && r.Push.State == protocol.GitPushRejected:
		line := "Push rejected"
		if reason := safe(singleLine(r.Push.Reason)); reason != "" {
			line += " · " + reason
		}
		b = append(b, text(line, p.red))
		if strings.Contains(r.Push.Reason, "fetch first") || strings.Contains(r.Push.Reason, "non-fast-forward") {
			b = append(b, text("Pull first, then push again", p.muted))
		}
	default:
		failure := st.failure
		if failure == "" {
			failure = "Git refused the change"
		}
		if r != nil && r.Fetch != nil && r.Fetch.State != protocol.GitFetchSucceeded && st.cmd.Kind == protocol.GitKindPull {
			failure = "Fetch failed · " + failure
		}
		b = append(b, text(failure, p.red))
	}
	if st.review {
		verb := "Review and switch again"
		if st.cmd.Kind == protocol.GitKindResetSoft {
			verb = "Review and reset again"
		}
		b = append(b, button(verb, m.icon("refresh"), "git:review", action{Kind: "git-review"}))
	}
	if gitCredentialCode(code) || (r != nil && gitFetchCredentialFailure(r)) {
		b = append(b, text(gitCredentialAdvice, p.muted))
	}
	if r != nil && (code == "would_overwrite" || code == "partial_switch") && (len(r.Paths) > 0 || r.PathsIncomplete) {
		b = append(b, m.gitPathListBlocks(r)...)
		if code == "would_overwrite" {
			b = append(b, text("Commit or discard these changes first, then try again", p.muted))
		}
	}
	if st.warning != "" && succeeded && r.Code != "pushed_newer_head" {
		b = append(b, text(st.warning, p.gold))
	}
	if st.output != "" {
		b = append(b, button("View output", m.icon("terminal"), "git:output", action{Kind: "git-output"}))
	}
	if st.unknown {
		b = append(b, button("Refresh", m.icon("refresh"), "git:unknown-refresh", action{Kind: "git-refresh"}))
	}
	return b
}

// gitPathListBlocks lists would_overwrite or partial_switch paths,
// sanitized and bounded.
func (m *Model) gitPathListBlocks(r *protocol.GitResult) []surfaceBlock {
	p := m.colors()
	var b []surfaceBlock
	for i, path := range r.Paths {
		if i == gitWouldOverwriteShown {
			break
		}
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "  " + safe(singleLine(path)), ink: p.text})
	}
	switch rest := len(r.Paths) - gitWouldOverwriteShown; {
	case r.PathsIncomplete:
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "  and others · see output", ink: p.muted})
	case rest > 0:
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: fmt.Sprintf("  and %d others", rest), ink: p.muted})
	}
	return b
}

// gitUndoable reports that the displayed soft reset can still be undone:
// HEAD and the branch are where the reset left them.
func (m *Model) gitUndoable(st *gitWriteState) (string, bool) {
	r := st.result
	if r == nil || r.Ref == nil || r.Ref.PreviousHead == "" {
		return "", false
	}
	g := m.currentGitView()
	if g == nil || g.status == nil || g.status.HeadOid != r.Ref.Head || g.status.Branch != r.Ref.Branch {
		return "", false
	}
	return r.Ref.PreviousHead, true
}

// gitCreateBlocks is the open new-branch editor.
func (m *Model) gitCreateBlocks(key string) []surfaceBlock {
	dlg := m.gitR.create
	if dlg == nil || dlg.key != key {
		return nil
	}
	p := m.colors()
	button := func(label, k string, a action, help string) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: m.icon("add"), key: k, action: a, help: help}}
	}
	b := []surfaceBlock{
		{kind: surfaceHeadingBlock, label: "New branch", value: "from " + dlg.from},
		{kind: surfaceGitBlock, git: &gitRow{kind: "name", key: gitBranchNameKey, action: action{Kind: "git-branch-name"},
			help: "Branch name · Enter creates · Esc cancels", text: m.gitBranchInput().Value()}},
	}
	if m.gitR.nameErr != "" {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: m.gitR.nameErr, ink: p.gold})
	}
	b = append(b,
		button("Create branch", "git:create-create", action{Kind: "git-branch-create"}, "Create branch at "+dlg.from+" · Enter"),
		button("Create and switch", "git:create-switch", action{Kind: "git-branch-create-switch"}, "Create branch at "+dlg.from+" and switch to it"),
		surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: "Cancel", mark: m.icon("close"), key: "git:create-cancel", action: action{Kind: "git-branch-cancel"}, help: "Cancel new branch"}},
		surfaceBlock{kind: surfaceGapBlock})
	return b
}

// gitBranchControls are a branch row's two reserved end slots: Switch for
// local branches other than HEAD, then the row's menu.
func (m *Model) gitBranchControls(br protocol.GitBranch, block string) [2]*gitControl {
	var out [2]*gitControl
	if !m.gitRefsEnabled() {
		return out
	}
	name := safe(singleLine(br.Name))
	if !br.Remote && !br.Head {
		help := "Switch to " + name + " (S)"
		switch {
		case br.WorktreePath != "":
			help = "Switch unavailable · " + gitRefCopy(protocol.GitKindSwitch, "checked_out_elsewhere")
		case block != "":
			help = "Switch unavailable · " + block
		}
		out[0] = &gitControl{glyph: m.icon("switch"), key: "git-bact:switch:" + br.Ref, help: help,
			action: action{Kind: "git-switch", ID: br.Ref}, disabled: block != "" || br.WorktreePath != ""}
	}
	out[1] = &gitControl{glyph: m.icon("more-vertical"), key: "git-bact:menu:" + br.Ref, help: "Branch actions · " + name + " (Shift+F10)",
		action: action{Kind: "git-row-menu", ID: "git:branch:" + br.Ref}}
	return out
}

// paintGitRefRow paints the heading and new-branch name rows.
func (m *Model) paintGitRefRow(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	bg := p.panel
	switch r.kind {
	case "heading":
		panelSectionHeadingOn(f, m, x, y, width, r.text, bg)
		sx := x + width
		for i := len(r.tools) - 1; i >= 0; i-- {
			sx -= gitSlotWidth
			if sx < x+ansi.StringWidth(r.text)+2 {
				break
			}
			m.paintGitSlot(f, sx, y, r.tools[i], bg, true)
		}
	case "review-end":
		f.text(x, y, width, truncateCells("— "+r.text+" —", width), p.muted, bg)
	case "name":
		focused := m.focus == r.key
		v := m.containerStyle(focused, p.text, p.input)
		border := componentBorder(squareOutline, m.plainIcons)
		if width < 6 {
			return
		}
		f.text(x, y, 1, border.Left, v.border, bg)
		f.text(x+width-1, y, 1, border.Right, v.border, bg)
		f.text(x+1, y, width-2, "", p.text, p.input)
		iw := width - 4
		a := m.gitBranchInput()
		m.gitNameStyles(iw)
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

func (m *Model) gitNameStyles(width int) {
	m.gitMessageStylesFor(m.gitBranchInput(), width)
}

// paintGitSlot paints one glyph-only icon control in its reserved
// three-cell slot: only the glyph and its spill cell are interactive, and
// the focus mark takes the leading padding cell.
func (m *Model) paintGitSlot(f *frame, sx, y int, c *gitControl, bg string, show bool) {
	p := m.colors()
	s := m.controlState(false, c.key)
	s.Disabled = c.disabled
	v := m.iconStyle(s, p.muted, bg)
	v.focused = s.Focused
	if show || s.Focused {
		f.styledText(sx+1, y, 2, c.glyph, v, false)
		if v.focused {
			f.focusMark(sx, y, v, bg)
		}
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: sx + 1, Y: y, W: 2, H: 1}, Action: c.action, Label: c.help, Key: c.key, Slot: shell.Rect{X: sx, Y: y, W: gitSlotWidth, H: 1}})
}

// gitAckVerb names what an acknowledgement review continues.
func gitAckVerb(a *gitAckDialog) string {
	if a.code == "leaves_commits" {
		return "leave " + plural(a.count, "commit") + " and switch"
	}
	return "reset anyway"
}
