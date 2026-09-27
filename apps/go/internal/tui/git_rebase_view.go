package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_rebase_view.go paints the interactive rebase editor, its message
// editor and the stop section of the operation panel (git_rebase.go).

// gitRebaseRow is one plan entry row of the editor.
type gitRebaseRow struct {
	index           int
	action          string // the action cell's words, e.g. "fixup -C"
	short, subject  string
	meld, isBreak   bool
	dropped         bool
	message         bool // carries a message the user chose
	invalid         bool
	published, stop bool
}

// gitRebaseActionWidth is the action cell (the longest word, "fixup -C",
// and a gap), so subjects line up.
const gitRebaseActionWidth = 9

// gitRebaseActionLabel is an entry's action cell text.
func gitRebaseActionLabel(e protocol.GitRebaseEntry) string {
	switch e.Action {
	case protocol.GitRebaseFixup:
		if e.Fixup != "" {
			return "fixup -" + e.Fixup
		}
	case protocol.GitRebaseEdit:
		if e.EditMode == protocol.GitRebaseEditAmend {
			return "amend"
		}
	}
	return e.Action
}

// gitRebaseEditorBlocks is the plan editor's body.
func (m *Model) gitRebaseEditorBlocks(d *gitRebaseDraft) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action, help string) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: help}}
	}
	pl := d.plan
	b := []surfaceBlock{{kind: surfaceHeadingBlock, label: "Interactive rebase", value: safe(singleLine(pl.Branch))},
		button("Back to status (Esc)", m.icon("back"), "git:rb:back", action{Kind: "git-rb-back"}, "Back to the Git status · the plan is kept")}
	if d.loaded {
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Branch", value: safe(singleLine(pl.Branch)) + " at " + gitShort(pl.HeadOid)},
			surfaceBlock{kind: surfacePairBlock, label: "Base", value: gitRebaseBaseLabel(pl)})
		if pl.Onto != "" || pl.Root {
			b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Onto", value: gitRebaseOntoLabel(pl)})
		}
	}
	if d.loading {
		b = append(b, statusBlock(m, "Reading the plan…", "pending", false))
	}
	if d.err != "" {
		b = append(b, text(d.err, p.red))
	}
	if m.gitRebaseStale(d) {
		b = append(b, text("The repository changed since the plan was read · Refresh plan keeps your arrangement", p.gold),
			button("Refresh plan", m.icon("refresh"), "git:rb:stale-refresh", action{Kind: "git-rb-refresh"}, "Read the plan again, keeping your edits"))
	}
	if d.notice != "" {
		b = append(b, text(d.notice, p.muted))
	}
	if !d.loaded {
		if !d.loading {
			b = append(b, gap, button("Retry reading the plan", m.icon("refresh"), "git:rb:retry", action{Kind: "git-rb-refresh"}, "Read the plan again"))
		}
		return append(b, gap, button("Discard plan", m.icon("trash"), "git:rb:discard", action{Kind: "git-rb-discard"}, "Discard the plan"))
	}
	if pl.Blocked != "" {
		msg := gitRefCopy(protocol.GitKindRebase, pl.Blocked)
		if s := safe(singleLine(pl.BlockedMessage)); s != "" {
			msg += " · " + s
		}
		b = append(b, text("Not possible now · "+msg, p.gold))
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Commits", value: strconv.Itoa(len(pl.Commits) - pl.MergeCount)},
		text("Newest first · runs bottom to top · squash and fixup meld into the row below", p.muted))
	bad := d.invalidEntry()
	published := map[string]bool{}
	for _, c := range pl.Commits {
		published[c.Oid] = c.Published
	}
	for i := len(d.entries) - 1; i >= 0; i-- {
		e := d.entries[i]
		row := &gitRebaseRow{index: i, action: gitRebaseActionLabel(e), invalid: i == bad}
		help := ""
		if e.Action == protocol.GitRebaseBreak {
			row.isBreak = true
			row.subject = "pause here · Continue resumes"
			help = "Break · d removes · Shift+↑/↓ move · drag the grip to reorder"
		} else {
			c, _ := d.commit(e.Commit)
			row.short, row.subject = gitShort(e.Commit), safe(singleLine(c.Subject))
			row.meld = gitRebaseChainMember(e)
			row.dropped = e.Action == protocol.GitRebaseDrop
			row.published = published[e.Commit]
			row.stop = e.Action == protocol.GitRebaseEdit
			kind := d.messageFor(i)
			row.message = kind == "reword" && e.Message != ""
			if kind == "chain" {
				start := gitRebaseChainStart(d.entries, i)
				_, row.message = d.chainMsg[gitRebaseChainKey(d.entries, start, gitRebaseChainEnd(d.entries, start))]
			}
			help = row.action + " " + row.short + " " + row.subject + " · Enter actions · p r e s f d · b break · m message · Shift+↑/↓ move"
		}
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbentry", key: gitRebaseRowKey(i),
			action: action{Kind: "git-rb-row", Index: i}, help: help, rb: row}})
	}
	for i := range d.entries {
		if d.rewordReplaced(i) {
			b = append(b, text("The reword of "+gitShort(d.entries[i].Commit)+" is replaced by its squash's written combined message · m on the chain edits it", p.gold))
		}
	}
	for _, c := range pl.Commits {
		if c.Merge {
			b = append(b, text("merge "+gitShort(c.Oid)+" "+safe(singleLine(c.Subject))+" · dropped", p.muted))
		}
	}
	if prob := m.gitRebaseProblem(d); prob != "" {
		b = append(b, text("Cannot start · "+prob, p.gold))
	}
	b = append(b, gap, surfaceBlock{kind: surfaceHeadingBlock, label: "Options"})
	refsHelp := "Move other branches that point into these commits"
	if len(pl.UpdateRefs) == 0 {
		refsHelp = "No other branch points into these commits"
	}
	b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbtoggle", text: "Update refs", key: "git:rb:update-refs",
		action: action{Kind: "git-rb-toggle", ID: "update-refs"}, help: refsHelp, on: d.updateRefs, disabled: len(pl.UpdateRefs) == 0}})
	if len(pl.UpdateRefs) > 0 {
		b = append(b, text("Branches: "+gitRebaseRefNames(pl.UpdateRefs), p.muted))
	}
	if n := len(pl.UpdateRefsUnsupported); n > 0 {
		var names []string
		for _, r := range pl.UpdateRefsUnsupported {
			names = append(names, safe(singleLine(gitRefLabel(r))))
		}
		if n > 6 {
			names = append(names[:6], "+"+strconv.Itoa(n-6))
		}
		b = append(b, text("Cannot be moved here: "+strings.Join(names, ", ")+" · with Update refs on the start is refused; off, they stay where they are", p.gold))
	}
	if pl.MergeCount > 0 {
		b = append(b, text(plural(pl.MergeCount, "merge commit")+" in the range: merge commits will be dropped and side commits linearised", p.gold),
			surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbtoggle", text: "Drop merges and linearise", key: "git:rb:merges",
				action: action{Kind: "git-rb-toggle", ID: "merges"}, help: "Acknowledge that merge commits are dropped", on: d.ackMerges}})
	}
	if pl.Published {
		b = append(b, text("Some commits are already on a remote · rewriting them diverges from it (never force-pushed here)", p.gold),
			surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbtoggle", text: "Rewrite published commits", key: "git:rb:published",
				action: action{Kind: "git-rb-toggle", ID: "published"}, help: "Acknowledge rewriting published commits", on: d.ackPublished}})
	}
	b = append(b, gap,
		button("Start rebase…", m.icon("check"), "git:rb:start", action{Kind: "git-rb-start"}, "Review and start the rebase"),
		button("Refresh plan", m.icon("refresh"), "git:rb:refresh", action{Kind: "git-rb-refresh"}, "Read the plan again, keeping your edits"),
		button("Discard plan…", m.icon("trash"), "git:rb:discard", action{Kind: "git-rb-discard"}, "Discard the plan"))
	return b
}

// gitRebaseMessageBlocks is the message editor: a reword, a chain's
// combined message, or a stop's Continue or Commit message.
func (m *Model) gitRebaseMessageBlocks(e *gitRebaseMsgEdit, d *gitRebaseDraft) []surfaceBlock {
	p := m.colors()
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action, help string) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: help}}
	}
	heading, save := "Reword", "Save message (Enter)"
	var b []surfaceBlock
	switch e.purpose {
	case "reword", "chain":
		if e.purpose == "chain" {
			heading = "Combined message"
		}
		b = append(b, surfaceBlock{kind: surfaceHeadingBlock, label: heading, value: gitShort(e.commit)})
		if d != nil {
			if c, ok := d.commit(e.commit); ok {
				b = append(b, text(gitShort(c.Oid)+" "+safe(singleLine(c.Subject)), p.muted))
			}
			if e.purpose == "chain" {
				b = append(b, text("Message of the commit the chain makes · prefilled with its messages, joined by a blank line", p.muted))
			}
		}
	case "continue":
		heading, save = "Continue with message", "Continue… (Enter)"
		b = append(b, surfaceBlock{kind: surfaceHeadingBlock, label: heading, value: safe(singleLine(e.state.Branch))},
			text("The message for the commit this step makes or amends · empty keeps the stored or original message", p.muted))
	case "commit":
		heading, save = "Commit staged changes", "Commit… (Enter)"
		b = append(b, surfaceBlock{kind: surfaceHeadingBlock, label: heading, value: safe(singleLine(e.state.Branch))},
			text("A new commit from what is staged · the rebase stays stopped", p.muted))
	}
	if e.warn != "" {
		b = append(b, text(e.warn, p.gold))
	}
	a := m.gitRebaseMsg()
	width := m.workspaceGeometry(m.footerHeight()).Right.W - 7
	lines := min(12, max(3, inputRows(a.Value(), max(8, width))))
	help := "Message · Enter saves · " + m.newlineHint() + " newline · Esc cancels"
	b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbmsg", text: "top"}})
	for i := range lines {
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbmsg", line: i, lines: lines, key: gitRebaseMsgKey,
			action: action{Kind: "git-rb-msg"}, help: help, text: gitMessageLine(a.Value(), i)}})
	}
	b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "rbmsg", text: "bottom"}},
		button(save, m.icon("check"), "git:rb:msg-save", action{Kind: "git-rb-msg-save"}, save),
		button("Cancel (Esc)", m.icon("close"), "git:rb:msg-cancel", action{Kind: "git-rb-msg-cancel"}, "Cancel · the message is not changed"))
	if e.purpose == "reword" || e.purpose == "chain" {
		b = append(b, button("Reset to the original", m.icon("reopen"), "git:rb:msg-reset", action{Kind: "git-rb-msg-reset"}, "Replace the text with the original message"))
	}
	return b
}

// paintGitRebaseRow paints a plan entry: grip, action cell, hash and
// subject, with full-row hover and focus fill.
func (m *Model) paintGitRebaseRow(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	e := r.rb
	s := m.controlState(m.gitRB.dragging && m.gitRB.drag == e.index, r.key)
	v := m.componentStyle(squareFill, s, p.text, p.panel)
	f.styledButton(x, y, width, "", r.key, r.action, v)
	f.hits[len(f.hits)-1].Label = r.help
	bg := v.background
	if width < 14 {
		return
	}
	cx, end := x+1, x+width-1
	grip := "⋮"
	if m.plainIcons {
		grip = ":"
	}
	f.text(cx, y, 1, grip, p.muted, bg)
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: cx, Y: y, W: 2, H: 1}, Action: action{Kind: "git-rb-grip", Index: e.index}, Label: "Drag to reorder · Shift+↑/↓ from the keyboard", Key: r.key})
	cx += 2
	ink := p.text
	switch r.rb.action {
	case protocol.GitRebaseReword:
		ink = p.blue
	case protocol.GitRebaseEdit, "amend", protocol.GitRebaseBreak:
		ink = p.gold
	case protocol.GitRebaseSquash, protocol.GitRebaseFixup, "fixup -C", "fixup -c":
		ink = p.violet
	case protocol.GitRebaseDrop:
		ink = p.red
	}
	aw := min(gitRebaseActionWidth, end-cx)
	f.componentText(cx, y, aw-1, e.action, componentVisual{foreground: ink, background: bg, bold: true})
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: cx, Y: y, W: aw - 1, H: 1}, Action: action{Kind: "git-rb-menu", Index: e.index}, Label: "Choose the action · Enter", Key: r.key})
	cx += aw
	if e.isBreak {
		f.componentText(cx, y, max(0, end-cx), e.subject, componentVisual{foreground: p.muted, background: bg})
		return
	}
	// Flags at the right: invalid, custom message, published.
	var flags []struct{ glyph, ink string }
	if e.invalid {
		flags = append(flags, struct{ glyph, ink string }{"!", p.red})
	}
	if e.message {
		glyph := "✎"
		if m.plainIcons {
			glyph = "m"
		}
		flags = append(flags, struct{ glyph, ink string }{glyph, p.blue})
	}
	if e.published {
		flags = append(flags, struct{ glyph, ink string }{"↑", p.gold})
	}
	if len(flags) > 0 && end-cx > 2*len(flags)+12 {
		for i := len(flags) - 1; i >= 0; i-- {
			f.text(end-1, y, 1, flags[i].glyph, flags[i].ink, bg)
			end -= 2
		}
	}
	if e.meld && end-cx > 12 {
		glyph := "↓"
		if m.plainIcons {
			glyph = "v"
		}
		f.text(cx, y, 1, glyph, p.violet, bg)
		cx += 2
	}
	if hw := ansi.StringWidth(e.short); end-cx > hw+10 {
		f.text(cx, y, hw, e.short, p.muted, bg)
		cx += hw + 1
	}
	fg := v.foreground
	if e.dropped {
		fg = p.muted
	}
	f.componentText(cx, y, max(0, end-cx), truncateCells(e.subject, end-cx), componentVisual{foreground: fg, background: bg, bold: v.bold})
}

// paintGitRebaseMsg paints the message editor's outline and text rows.
func (m *Model) paintGitRebaseMsg(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	bg := p.panel
	if width < 8 {
		return
	}
	v := m.containerStyle(m.focus == gitRebaseMsgKey, p.text, p.input)
	border := componentBorder(roundedOutline, m.plainIcons)
	if r.key == "" {
		if r.text == "top" {
			f.text(x, y, width, border.TopLeft+strings.Repeat(border.Top, width-2)+border.TopRight, v.border, bg)
		} else {
			f.text(x, y, width, border.BottomLeft+strings.Repeat(border.Bottom, width-2)+border.BottomRight, v.border, bg)
		}
		return
	}
	f.text(x, y, 1, border.Left, v.border, bg)
	f.text(x+width-1, y, 1, border.Right, v.border, bg)
	f.text(x+1, y, width-2, "", p.text, p.input)
	ix, iw := x+2, width-4
	a := m.gitRebaseMsg()
	m.gitMessageStylesFor(a, iw)
	if a.Width() != iw {
		a.SetWidth(iw)
	}
	if a.Height() != r.lines {
		a.SetHeight(r.lines)
	}
	if f.rows != nil {
		if lines := strings.Split(a.View(), "\n"); r.line < len(lines) {
			f.put(shell.Rect{X: ix, Y: y, W: iw, H: 1}, lines[r.line])
		}
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x + 1, Y: y, W: width - 2, H: 1}, Action: r.action, Label: r.help, Key: r.key})
}

// gitRebaseRowText is a rebase row's plain text for surfaceText.
func gitRebaseRowText(r *gitRow) string {
	switch r.kind {
	case "rbentry":
		e := r.rb
		if e.isBreak {
			return e.action + " · " + e.subject
		}
		line := e.action + " "
		if e.meld {
			line += "↓ "
		}
		return line + e.short + " " + e.subject
	case "rbmsg":
		if r.key == "" {
			return ""
		}
		return r.text
	case "rbtoggle":
		if r.on {
			return r.text + ": On"
		}
		return r.text + ": Off"
	}
	return ""
}

// gitRebaseStopBlocks describes an application rebase's stop in the
// operation panel: progress, what the stop needs, a commit failure, and the
// branches that move when it finishes.
func (m *Model) gitRebaseStopBlocks(o *protocol.GitOperationState) []surfaceBlock {
	p := m.colors()
	in := o.Interactive
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	var b []surfaceBlock
	progress := strconv.Itoa(in.Done) + " of " + strconv.Itoa(in.Done+in.Remaining)
	if c := safe(singleLine(in.Command)); c != "" {
		progress += " · " + c
		if in.CommandOid != "" {
			progress += " " + gitShort(in.CommandOid)
		}
	}
	b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Plan", value: progress})
	if next := safe(singleLine(in.Next)); next != "" {
		b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Next", value: gitRebaseTodoLine(next)})
	}
	cur := ""
	if o.Current != nil {
		cur = gitShort(o.Current.Oid)
	} else if in.CommandOid != "" {
		cur = gitShort(in.CommandOid)
	}
	var stop string
	switch in.Stop {
	case protocol.GitRebaseStopConflict:
		stop = "Stopped with conflicts · resolve and stage them, then Continue (or Skip the commit)"
	case protocol.GitRebaseStopEditAmend:
		stop = "Stopped to amend " + cur + " · stage changes, then Continue amends it"
	case protocol.GitRebaseStopEditReset:
		stop = "Stopped to edit " + cur + " · its changes are staged: commit parts with Commit staged, or Continue to commit the rest"
	case protocol.GitRebaseStopBreak:
		stop = "Paused at a break · Commit staged inserts commits; Continue resumes"
	case protocol.GitRebaseStopMessage:
		stop = "A message step did not finish · Continue retries it, optionally with a new message"
	case protocol.GitRebaseStopCommitFailed:
		stop = "Git stopped before committing this step (its changes are staged) · Continue commits it"
	case protocol.GitRebaseStopCommitted:
		stop = "The step was committed outside the app · Continue goes on (Skip and a message do not apply)"
	case protocol.GitRebaseStopRescheduled:
		stop = "Git could not start the next step (for example an untracked file in the way) · clear the obstacle, then Continue retries it"
	case protocol.GitRebaseStopEmpty:
		stop = "The commit became empty · Continue keeps it as an empty commit, Skip drops it"
		if gitRebaseEmptyCommitted(o) {
			stop = "The commit became empty and was already made · Continue goes on"
		}
	default:
		stop = "Stopped without a recognised reason (for example a clean, non-empty pick) · Abort, or continue in a terminal"
	}
	if in.ServerEdit && (in.Stop == protocol.GitRebaseStopEditAmend || in.Stop == protocol.GitRebaseStopEditReset) {
		stop = "Stopped to edit after resolving the conflict · " + stop
	}
	b = append(b, text(stop, p.gold))
	if in.Late && (in.Stop == protocol.GitRebaseStopEmpty || in.Stop == protocol.GitRebaseStopMessage) {
		b = append(b, text(gitRefCopy(protocol.GitKindOperationContinue, "stop_unobserved"), p.gold))
	}
	detail := safe(singleLine(in.Detail))
	switch in.Failure {
	case protocol.GitRebaseSigningFailed:
		msg := "Signing failed · fix commit signing (commit.gpgSign), then Continue, or Abort"
		if detail != "" {
			msg += " · " + detail
		}
		b = append(b, text(msg, p.red))
	case protocol.GitRebaseHookRejected:
		hook := safe(singleLine(in.Hook))
		if hook == "" {
			hook = "A hook"
		}
		msg := hook + " refused the commit · fix it, then Continue retries"
		if detail != "" {
			msg += " · " + detail
		}
		b = append(b, text(msg, p.red))
	case protocol.GitRebaseHelperFailed:
		msg := "The server's editor helper failed · Continue retries"
		if detail != "" {
			msg += " · " + detail
		}
		b = append(b, text(msg, p.red))
	}
	if in.Stop == protocol.GitRebaseStopEditReset && in.AmendParent != "" && o.HeadOid != "" && o.HeadOid != in.AmendParent {
		b = append(b, text("Commits were made at this stop · Continue commits what is still staged", p.muted))
	}
	switch {
	case in.Staged && in.Unstaged:
		b = append(b, text("Staged and unstaged changes present", p.muted))
	case in.Staged:
		b = append(b, text("Staged changes present", p.muted))
	case in.Unstaged:
		b = append(b, text("Unstaged changes present · only staged changes are committed", p.muted))
	}
	if len(in.UpdateRefs) > 0 {
		b = append(b, text("Moves when finished: "+gitRebaseRefNames(in.UpdateRefs), p.muted))
	}
	return b
}

// gitRebaseStopActions are the extra stop actions of an application
// rebase: Continue with a message and Commit staged.
func (m *Model) gitRebaseStopActions(o *protocol.GitOperationState) []surfaceBlock {
	p := m.colors()
	in := o.Interactive
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: m.icon(glyph), key: k, action: a, help: label}}
	}
	var b []surfaceBlock
	if o.Can.Continue.Allowed && gitRebaseMessageApplies(in) {
		b = append(b, button("Continue with message…", "compose", "git:rb-stop-continue", action{Kind: "git-rb-stop-continue"}))
	}
	switch in.Stop {
	case protocol.GitRebaseStopEditAmend, protocol.GitRebaseStopEditReset, protocol.GitRebaseStopBreak:
		if reason := gitRebaseCommitBlock(o); reason != "" {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Commit staged unavailable · " + reason, ink: p.muted})
		} else {
			b = append(b, button("Commit staged…", "add", "git:rb-stop-commit", action{Kind: "git-rb-stop-commit"}))
		}
	}
	return b
}

// gitRebaseDraftBlocks is the status body's line for a kept, hidden draft.
func (m *Model) gitRebaseDraftBlocks(d *gitRebaseDraft) []surfaceBlock {
	p := m.colors()
	label := "Rebase plan kept"
	if d.sentID != "" {
		label = "Starting the rebase…"
	}
	b := []surfaceBlock{{kind: surfaceGapBlock}, {kind: surfaceTextBlock, value: label, ink: p.muted}}
	if d.err != "" {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: d.err, ink: p.gold})
	}
	if d.sentID == "" {
		b = append(b, surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: "Open rebase plan", mark: m.icon("git"),
			key: "git:rb:show", action: action{Kind: "git-rb-show"}, help: "Open the kept interactive rebase plan"}})
	}
	return b
}

// gitRebaseEmptyCommitted reports an empty stop whose commit was already
// made: Skip is refused there and Continue only goes on.
func gitRebaseEmptyCommitted(o *protocol.GitOperationState) bool {
	in := o.Interactive
	return in != nil && in.Stop == protocol.GitRebaseStopEmpty && (in.StepCommitted || in.PreHead != "" && o.HeadOid != "" && o.HeadOid != in.PreHead)
}

// gitRebaseContinueCopy says what Continue does at an application
// rebase's stop.
func gitRebaseContinueCopy(o protocol.GitOperationState) string {
	in := o.Interactive
	switch in.Stop {
	case protocol.GitRebaseStopEmpty:
		if gitRebaseEmptyCommitted(&o) {
			return "Goes on · the commit was already made"
		}
		return "Keeps the empty commit and goes on"
	case protocol.GitRebaseStopBreak:
		return "Resumes the rebase after the break"
	case protocol.GitRebaseStopCommitted:
		return "Goes on · the step was already committed"
	case protocol.GitRebaseStopRescheduled:
		return "Retries the step Git rescheduled"
	case protocol.GitRebaseStopEditReset:
		return "Commits what is still staged with the original message and author, then goes on"
	case protocol.GitRebaseStopEditAmend:
		if in.Staged {
			return "Amends the stopped commit with what is staged, then goes on"
		}
		return "Goes on without amending"
	case protocol.GitRebaseStopMessage:
		return "Retries the message step with the plan's message"
	case protocol.GitRebaseStopCommitFailed:
		return "Commits this step from what is staged, then goes on"
	}
	if in.Stop == protocol.GitRebaseStopConflict {
		if word, _, _ := strings.Cut(strings.TrimSpace(in.Command), " "); word == "edit" || word == "e" {
			return "Commits the resolution, then stops at this edit step for editing"
		}
	}
	return "Commits what is staged now, then goes on"
}

// gitRebaseTodoLine shortens a todo line's full hash for display.
func gitRebaseTodoLine(line string) string {
	words := strings.Fields(line)
	for i, w := range words {
		if len(w) >= 40 && strings.Trim(w, "0123456789abcdef") == "" {
			words[i] = gitShort(w)
		}
	}
	return strings.Join(words, " ")
}
