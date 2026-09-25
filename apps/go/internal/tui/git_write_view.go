package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// git_write_view.go paints the Git write controls inside the Git surface:
// reserved icon slots on status rows, the write status lines and the commit
// composer. Every part is a gitRow painted by paintGitRow, so the surface
// body's scrolling, wrapping and focus movement stay shared.

// gitSlotWidth is one reserved icon slot: padding cell, glyph, spill cell.
const gitSlotWidth = 3

// gitControl is one glyph-only icon control in a status row's slot.
type gitControl struct {
	glyph, key, help string
	action           action
	disabled         bool
}

// gitComposeRows bounds the commit message editor's height.
const gitComposeMinRows, gitComposeMaxRows = 2, 6

// gitEntryControls returns a status entry's two slot controls (left, right);
// nil slots stay reserved blank. Conflicted and submodule entries have none.
func (m *Model) gitEntryControls(e protocol.GitStatusEntry, block string) [2]*gitControl {
	var out [2]*gitControl
	if !gitEntryWritable(e) {
		return out
	}
	path := safe(singleLine(e.Path))
	suffix := ":" + e.Group + ":" + e.Path
	mk := func(kind, glyph, verb string) *gitControl {
		help := verb + " · " + path
		if block != "" {
			help = verb + " unavailable · " + block
		}
		return &gitControl{glyph: glyph, key: "git-act:" + strings.TrimPrefix(kind, "git-") + suffix, help: help,
			action: action{Kind: kind, Value: e.Group, ID: e.Path}, disabled: block != ""}
	}
	switch e.Group {
	case protocol.GitGroupStaged:
		out[1] = mk("git-unstage", m.icon("unstage"), "Unstage (u)")
	case protocol.GitGroupUnstaged:
		out[0] = mk("git-stage", m.icon("add"), "Stage (s)")
		out[1] = mk("git-discard", m.icon("discard"), "Discard changes… (d)")
	case protocol.GitGroupUntracked:
		out[0] = mk("git-stage", m.icon("add"), "Stage (s)")
		out[1] = mk("git-discard", m.icon("trash"), "Delete file… (d)")
	}
	return out
}

// gitWriteBlocks are the write status lines and the commit composer, shown
// between the checkout facts and the status groups.
func (m *Model) gitWriteBlocks(key string, g *gitView) []surfaceBlock {
	p := m.colors()
	var b []surfaceBlock
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	reason, progress := m.gitWriteBlock(key, g)
	switch {
	case progress:
		b = append(b, statusBlock(m, reason, "running", false))
	case reason == gitLeaseCopy:
		b = append(b, statusBlock(m, reason, "", false))
		b[len(b)-1].glyph, b[len(b)-1].ink = panelStatusMark(m, "waiting")
	}
	if st := m.gitWriteFor(key); st != nil && !st.running {
		switch {
		case st.transport != "":
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: st.transport, ink: p.red},
				button("Retry "+strings.TrimPrefix(st.cmd.Kind, "git.")+" · same command", m.icon("refresh"), "git:retry", action{Kind: "git-retry"}),
				button("Refresh status", m.icon("refresh"), "git:pending-refresh", action{Kind: "git-refresh"}),
				button("Dismiss after checking status", m.icon("close"), "git:dismiss", action{Kind: "git-dismiss"}))
		case st.failure != "":
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: st.failure, ink: p.red})
			if st.output != "" {
				b = append(b, button("View output", m.icon("terminal"), "git:output", action{Kind: "git-output"}))
			}
			if st.unknown {
				b = append(b, button("Refresh", m.icon("refresh"), "git:unknown-refresh", action{Kind: "git-refresh"}))
			}
		case st.warning != "":
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: st.warning, ink: p.gold})
			if st.output != "" {
				b = append(b, button("View output", m.icon("terminal"), "git:output", action{Kind: "git-output"}))
			}
		}
	}
	if len(b) > 0 {
		b = append(b, surfaceBlock{kind: surfaceGapBlock})
	}
	return append(b, m.gitComposeBlocks(key, g)...)
}

// gitComposeBlocks is the commit composer: a rounded outline around the
// message editor, the identity line, the Amend toggle, an optional
// published warning and the Commit action.
func (m *Model) gitComposeBlocks(key string, g *gitView) []surfaceBlock {
	p := m.colors()
	s := g.status
	d := m.gitDraftFor(key)
	row := func(r *gitRow) surfaceBlock { return surfaceBlock{kind: surfaceGitBlock, git: r} }
	a := m.gitMsg()
	width := m.gitW.width
	if width <= 0 {
		// Before the first paint: the right host less its gutter, scrollbar
		// and the composer's outline and padding.
		width = m.workspaceGeometry(m.footerHeight()).Right.W - 7
	}
	lines := min(gitComposeMaxRows, max(gitComposeMinRows, inputRows(a.Value(), max(8, width))))
	b := []surfaceBlock{row(&gitRow{compose: "top"})}
	for i := range lines {
		b = append(b, row(&gitRow{compose: "msg", line: i, lines: lines, key: gitMessageKey, action: action{Kind: "git-message"},
			help: "Commit message · Enter commits · Shift+Enter newline", text: gitMessageLine(a.Value(), i)}))
	}
	id := s.Identity
	switch {
	case id != nil && id.Missing:
		b = append(b, row(&gitRow{compose: "line", text: gitErrorCopy("identity_missing"), markInk: p.gold}))
	case id != nil && (id.Name != "" || id.Email != ""):
		text := safe(singleLine(id.Name))
		if id.Email != "" {
			text = strings.TrimSpace(text + " <" + safe(singleLine(id.Email)) + ">")
		}
		b = append(b, row(&gitRow{compose: "line", text: text, markInk: p.muted}))
	}
	if d.headChanged {
		b = append(b, row(&gitRow{compose: "line", text: gitHeadChangedCopy + " · amend turned off", markInk: p.gold}))
	}
	block, _ := m.gitWriteBlock(key, g)
	amendHelp := "Amend last commit"
	amendDisabled := block != "" || s.HeadOid == ""
	if s.HeadOid == "" {
		amendHelp += " unavailable · " + gitErrorCopy("nothing_to_amend")
	} else if block != "" {
		amendHelp += " unavailable · " + block
	}
	b = append(b, row(&gitRow{compose: "amend", text: "Amend last commit", key: "git:amend", action: action{Kind: "git-amend"},
		help: amendHelp, on: d.amend, disabled: amendDisabled}))
	if d.amend && s.HeadOnUpstream {
		text := "Last commit is already on the upstream"
		if s.HeadOnUpstreamUnknown {
			text = "Could not check whether the last commit is on the upstream"
		}
		b = append(b, row(&gitRow{compose: "line", text: text, markInk: p.gold}))
	}
	reason := m.gitCommitBlock(key, g)
	label := "Commit"
	if d.amend {
		label = "Amend"
	}
	help := label + " · Enter in the message"
	if reason != "" {
		help = label + " unavailable · " + reason
	}
	b = append(b, row(&gitRow{compose: "actions", text: label, key: "git:commit-btn", action: action{Kind: "git-commit-run"},
		help: help, disabled: reason != "", subject: reason}))
	return append(b, row(&gitRow{compose: "bottom"}))
}

// gitMessageLine is a message line for plain-text flattening.
func gitMessageLine(value string, i int) string {
	lines := strings.Split(value, "\n")
	if i < len(lines) {
		return safe(singleLine(lines[i]))
	}
	return ""
}

func (m *Model) gitMessageStyles(width int) {
	p := m.colors()
	s := textarea.Styles{}
	s.Focused = textarea.StyleState{Base: style(p.text, p.input), Text: style(p.text, p.input), Placeholder: style(p.muted, p.input), CursorLine: style(p.text, p.input), Selection: style(p.text, p.selected), EndOfBuffer: style(p.text, p.input).Width(width)}
	s.Blurred = s.Focused
	s.Cursor.Color = lipColor(p.blue)
	m.gitMsg().SetStyles(s)
}

// gitComposeFocused reports keyboard focus inside the composer.
func (m *Model) gitComposeFocused() bool {
	return m.focus == gitMessageKey || m.focus == "git:amend" || m.focus == "git:commit-btn"
}

// paintGitCompose paints one composer or write-status row.
func (m *Model) paintGitCompose(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	bg := p.panel
	if r.compose == "button" {
		v := m.componentStyle(squareFill, m.controlState(false, r.key), p.text, bg)
		f.styledButton(x, y, width, "", r.key, r.action, v)
		f.hits[len(f.hits)-1].Label = r.help
		gw := ansi.StringWidth(r.mark)
		f.text(x+1, y, min(gw, max(0, width-1)), r.mark, p.blue, v.background)
		f.componentText(x+gw+2, y, max(0, width-gw-2), r.text, v)
		return
	}
	if width < 8 {
		return
	}
	v := m.containerStyle(m.gitComposeFocused(), p.text, p.input)
	border := componentBorder(roundedOutline, m.plainIcons)
	switch r.compose {
	case "top":
		f.text(x, y, width, border.TopLeft+strings.Repeat(border.Top, width-2)+border.TopRight, v.border, bg)
		return
	case "bottom":
		f.text(x, y, width, border.BottomLeft+strings.Repeat(border.Bottom, width-2)+border.BottomRight, v.border, bg)
		return
	}
	f.text(x, y, 1, border.Left, v.border, bg)
	f.text(x+width-1, y, 1, border.Right, v.border, bg)
	f.text(x+1, y, width-2, "", p.text, p.input)
	ix, iw := x+2, width-4
	switch r.compose {
	case "msg":
		a := m.gitMsg()
		m.gitMessageStyles(iw)
		if a.Width() != iw {
			a.SetWidth(iw)
		}
		if a.Height() != r.lines {
			a.SetHeight(r.lines)
		}
		m.gitW.width = iw
		if f.rows != nil {
			if lines := strings.Split(a.View(), "\n"); r.line < len(lines) {
				f.put(shell.Rect{X: ix, Y: y, W: iw, H: 1}, lines[r.line])
			}
		}
		f.hits = append(f.hits, hit{Rect: shell.Rect{X: x + 1, Y: y, W: width - 2, H: 1}, Action: r.action, Label: r.help, Key: r.key})
	case "line":
		f.text(ix, y, iw, r.text, r.markInk, p.input)
	case "amend":
		m.paintGitToggle(f, ix, y, iw, r)
	case "actions":
		label := r.text
		bw := min(iw, ansi.StringWidth(label)+4)
		bx := ix + iw - bw
		if r.disabled && r.subject != "" && bx-ix > 8 {
			f.text(ix, y, bx-ix-1, ansi.Truncate(r.subject, bx-ix-1, "…"), p.muted, p.input)
		}
		f.questionButton(m, bx, y, bw, label, r.key, r.action, primaryControl, r.disabled)
		f.hits[len(f.hits)-1].Label = r.help
	}
}

// paintGitToggle is the panel toggle row: label at the left, the On/Off word
// and switch at the right; the whole row is one control. Disabled rows keep
// muted ink without hover fill.
func (m *Model) paintGitToggle(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	s := m.controlState(false, r.key)
	s.Disabled = r.disabled
	v := m.componentStyle(squareFill, s, p.text, p.input)
	v.focused = s.Focused
	word := "Off"
	if r.on {
		word = "On "
	}
	right := len(word) + 1 + toggleTrackWidth
	label := r.text
	if right < width {
		label = ansi.Truncate(label, max(0, width-right-1), "…")
	}
	marked := v.focused && f.blank(x-1, y)
	f.styledText(x, y, width, label, v, v.focused && !marked)
	if right < width {
		wv := v
		if r.on && !r.disabled {
			wv.foreground, wv.bold = p.blue, true
		} else {
			wv.foreground = p.muted
		}
		f.componentText(x+width-right, y, len(word)+1, word+" ", wv)
		f.put(shell.Rect{X: x + width - toggleTrackWidth, Y: y, W: toggleTrackWidth, H: 1}, m.toggleTrack(r.on))
	}
	if marked {
		f.focusMark(x-1, y, v, v.base)
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: r.action, Label: r.help, Key: r.key})
}

// gitRowShowsControls reports whether a status row's icon controls are
// revealed: while the row or one of its controls is hovered or focused.
func (m *Model) gitRowShowsControls(r *gitRow) bool {
	if m.hover == r.key || m.focus == r.key {
		return true
	}
	for _, c := range r.controls {
		if c != nil && (m.hover == c.key || m.focus == c.key) {
			return true
		}
	}
	return false
}

// paintGitControls paints a status row's two reserved slots at x. Only the
// glyph and its trailing spill cell are interactive; hidden glyphs keep
// their hit so focus can reveal them.
func (m *Model) paintGitControls(f *frame, x, y int, r *gitRow, bg string) {
	show := m.gitRowShowsControls(r)
	p := m.colors()
	for i, c := range r.controls {
		sx := x + i*gitSlotWidth
		if c == nil {
			continue
		}
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
}
