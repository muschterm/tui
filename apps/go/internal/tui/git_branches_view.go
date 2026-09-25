package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_branches_view.go adds the read-only graph scope toggle, the BRANCHES
// section and the whole-group diff headings to the Git surface. Branch rows
// open a comparison against HEAD in the read-only viewer (git_viewer.go).
//
// Hook points for branch writes (switch/create): branch rows are gitRow
// values with kind "branch", key "git:branch:<full ref>" and the
// protocol.GitBranch in row.branch; gitBranchControls returns their
// reserved end-slot controls (none yet), painted like status-row controls.

// readGitBranches starts one branch-list read for the displayed target.
func (m *Model) readGitBranches(key string, target client.GitTarget, g *gitView) tea.Cmd {
	api := m.gitClient()
	if api == nil {
		return nil
	}
	m.gitSeq++
	gen := m.gitSeq
	g.branchGen, g.branchLoad = gen, true
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		b, err := api.GitBranches(deadline, target)
		return gitLogMsg{key: key, gen: gen, branches: &b, branchRead: true, err: err}
	}
}

func (m *Model) acceptGitBranches(msg gitLogMsg) {
	current, _ := m.gitTarget()
	g := m.gitViews[msg.key]
	if g == nil || g.branchGen != msg.gen {
		return // a newer read is in flight and owns branchLoad
	}
	g.branchLoad = false
	if msg.key != current {
		return
	}
	if msg.err != nil {
		g.branchErr = safe(singleLine(msg.err.Error()))
		return
	}
	g.branches, g.branchErr = msg.branches, ""
}

// gitBranchesAction handles the scope toggle, the BRANCHES disclosure and
// the whole-group headings; ok reports that a was one of them.
func (m *Model) gitBranchesAction(a action) (tea.Cmd, bool) {
	switch a.Kind {
	case "git-scope":
		key, _ := m.gitTarget()
		g := m.gitViews[key]
		if g == nil || !m.gitHistoryEnabled() {
			return nil, true
		}
		if !m.connected || m.gitClient() == nil {
			// Nothing is requested, so the selection does not change.
			return m.showNoticeAs(noticeUnavailable, "Connect to the server to read Git history"), true
		}
		if g.wantScope() == protocol.GitLogScopeAll {
			g.scope = protocol.GitLogScopeHead
		} else {
			g.scope = protocol.GitLogScopeAll
		}
		m.markDirty()
		return m.refreshGit(), true
	case "git-branches":
		key, target := m.gitTarget()
		g := m.gitViews[key]
		if g == nil || !m.gitHistoryEnabled() {
			return nil, true
		}
		g.branchOpen = !g.branchOpen
		m.markDirty()
		if !g.branchOpen || !m.connected || m.gitClient() == nil {
			return nil, true
		}
		return m.readGitBranches(key, target, g), true
	case "git-compare", "git-whole":
		return m.openGitViewer(a), true
	}
	return nil, false
}

// gitScopeBlock is the segmented HEAD / All branches choice under RECENT
// COMMITS.
func (m *Model) gitScopeBlock(g *gitView) surfaceBlock {
	// The selection is the displayed log's scope; a requested change shows
	// as pending (…) on its segment until that log arrives.
	all := g.shownScope() == protocol.GitLogScopeAll
	help := "Show all branches"
	if g.wantScope() == protocol.GitLogScopeAll {
		help = "Show HEAD and its upstream"
	}
	return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "scope", on: all, disabled: g.scopePending(), key: "git:scope", action: action{Kind: "git-scope"}, help: help + " · read-only"}}
}

// gitHistoryEnabled reports a server with the scope, branches and compare
// reads (capability git-history).
func (m *Model) gitHistoryEnabled() bool {
	return slices.Contains(m.snapshot.Capabilities, "git-history")
}

// gitSectionBlock is an activatable section heading: uppercase label,
// count at the right, full-row hover/focus feedback.
func gitSectionBlock(label, count, key, help string, a action, disclosure, open bool) surfaceBlock {
	return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{kind: "section", text: label, when: count, key: key, action: a, help: help, disclosure: disclosure, on: open}}
}

// gitBranchBlocks is the collapsed-by-default BRANCHES section.
func (m *Model) gitBranchBlocks(g *gitView) []surfaceBlock {
	p := m.colors()
	if !m.gitHistoryEnabled() {
		return []surfaceBlock{{kind: surfaceHeadingBlock, label: "Branches"}, {kind: surfaceGapBlock},
			{kind: surfaceTextBlock, value: "Branches and comparisons need a newer server", ink: p.muted}}
	}
	count := ""
	if g.branches != nil {
		count = strconv.Itoa(len(g.branches.Branches))
		if g.branches.Truncated || g.branches.Omitted > 0 {
			count += "+"
		}
	}
	b := []surfaceBlock{gitSectionBlock("Branches", count, "git:section:branches", "Show or hide branches", action{Kind: "git-branches"}, true, g.branchOpen)}
	if !g.branchOpen {
		return b
	}
	b = append(b, surfaceBlock{kind: surfaceGapBlock})
	switch {
	case g.branches == nil && g.branchErr != "":
		return append(b, statusBlock(m, "Branches", "failed", true), surfaceBlock{kind: surfaceTextBlock, value: g.branchErr, ink: p.muted})
	case g.branches == nil:
		return append(b, statusBlock(m, "Reading branches…", "pending", false))
	case g.branchErr != "":
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Showing the last result · " + g.branchErr, ink: p.muted})
	}
	if len(g.branches.Branches) == 0 {
		return append(b, surfaceBlock{kind: surfaceTextBlock, value: "No branches", ink: p.muted})
	}
	key, _ := m.gitTarget()
	block := ""
	refs := m.gitRefsEnabled() && g.status != nil
	if refs {
		block = m.gitRefBlock(key, g, protocol.GitKindSwitch)
	}
	for _, remote := range []bool{false, true} {
		for _, br := range g.branches.Branches {
			if br.Remote != remote {
				continue
			}
			name := safe(singleLine(br.Name))
			row := &gitRow{kind: "branch", branch: br, text: name, on: br.Head, key: "git:branch:" + br.Ref,
				action: action{Kind: "git-compare", ID: br.Ref, Value: br.Name},
				help:   "Compare " + name + " with HEAD · read-only"}
			row.when = gitBranchTrack(br, m.plainIcons)
			if refs {
				row.slots = true
				row.controls = m.gitBranchControls(br, block)
				if !br.Remote && !br.Head {
					row.help += " · S Switch"
				}
				row.help += " · b New branch"
			}
			b = append(b, surfaceBlock{kind: surfaceGitBlock, git: row})
		}
	}
	if g.branches.Truncated || g.branches.Omitted > 0 || g.branches.TrackingOmitted {
		b = append(b, surfaceBlock{kind: surfaceGapBlock})
	}
	if g.branches.Truncated {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: fmt.Sprintf("Showing the first %d refs", len(g.branches.Branches)), ink: p.muted})
	}
	if n := g.branches.Omitted; n > 0 {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: fmt.Sprintf("%d with unsupported names not shown", n), ink: p.muted})
	}
	if g.branches.TrackingOmitted {
		b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Ahead/behind omitted · repository too large to count in time", ink: p.muted})
	}
	return b
}

// gitBranchTrack is the compact upstream state: ↑2 ↓1, gone, or empty.
func gitBranchTrack(br protocol.GitBranch, plain bool) string {
	switch {
	case br.UpstreamGone:
		return "gone"
	case br.Ahead == 0 && br.Behind == 0:
		return ""
	}
	up, down := "↑", "↓"
	if plain {
		up, down = "+", "-"
	}
	var parts []string
	if br.Ahead > 0 {
		parts = append(parts, up+strconv.Itoa(br.Ahead))
	}
	if br.Behind > 0 {
		parts = append(parts, down+strconv.Itoa(br.Behind))
	}
	return strings.Join(parts, " ")
}

// gitExtraRowText is surfaceText for the rows this file adds.
func gitExtraRowText(r *gitRow) string {
	switch r.kind {
	case "scope":
		pending := ""
		if r.disabled {
			pending = " …"
		}
		if r.on {
			return "Scope: HEAD | [All branches]" + pending
		}
		return "Scope: [HEAD] | All branches" + pending
	case "section":
		mark := ""
		if r.disclosure {
			mark = "+ "
			if r.on {
				mark = "- "
			}
		}
		return strings.TrimSpace(mark + strings.ToUpper(r.text) + " " + r.when)
	case "heading":
		return "GIT"
	case "name":
		return "Branch name: " + r.text
	case "branch":
		line := r.text
		if r.on {
			line = "* " + line
		}
		if r.when != "" {
			line += " " + r.when
		}
		return line
	}
	return ""
}

// paintGitExtraRow paints scope, section and branch rows.
func (m *Model) paintGitExtraRow(f *frame, x, y, width int, r *gitRow) {
	if r.kind == "heading" || r.kind == "name" {
		m.paintGitRefRow(f, x, y, width, r)
		return
	}
	p := m.colors()
	state := m.controlState(false, r.key)
	for _, c := range r.controls {
		// Hovering a row's own control keeps the row's hover fill.
		state.Hovered = state.Hovered || c != nil && m.hover == c.key
	}
	v := m.componentStyle(squareFill, state, p.text, p.panel)
	f.styledButton(x, y, width, "", r.key, r.action, v)
	f.hits[len(f.hits)-1].Label = r.help
	bg := v.background
	if width < 4 {
		return
	}
	cx, end := x+1, x+width-1
	switch r.kind {
	case "scope":
		// Two segments; the selected one is accent and bold.
		label := "Scope"
		f.text(cx, y, min(len(label), end-cx), label, p.muted, bg)
		cx += len(label) + 2
		for _, seg := range []struct {
			text string
			on   bool
		}{{"HEAD", !r.on}, {"All branches", r.on}} {
			w := ansi.StringWidth(seg.text)
			if cx+w > end {
				break
			}
			ink := p.muted
			if seg.on {
				ink = p.blue
			}
			f.componentText(cx, y, w, seg.text, componentVisual{foreground: ink, background: bg, bold: seg.on})
			cx += w + 2
		}
		if r.disabled && cx < end {
			f.text(cx, y, 1, "…", p.muted, bg)
		}
	case "section":
		if r.disclosure {
			glyph := "▸"
			if r.on {
				glyph = "▾"
			}
			if m.plainIcons {
				glyph = "+"
				if r.on {
					glyph = "-"
				}
			}
			f.text(cx, y, 1, glyph, p.muted, bg)
			cx += 2
		}
		label := strings.ToUpper(r.text)
		vw := ansi.StringWidth(r.when)
		room := end - cx
		if vw > 0 && vw+2 < room {
			f.text(end-vw, y, vw, r.when, p.muted, bg)
			room -= vw + 2
		}
		f.componentText(cx, y, max(0, min(room, ansi.StringWidth(label))), label, componentVisual{foreground: p.muted, background: bg, bold: v.bold})
	case "branch":
		mark, ink := " ", p.muted
		if r.on {
			mark, ink = "●", p.blue
			if m.plainIcons {
				mark = "*"
			}
		}
		f.text(cx, y, 1, mark, ink, bg)
		cx += 2
		if r.branch.WorktreePath != "" && !r.on {
			f.text(cx, y, 1, "+", p.muted, bg)
		}
		cx += 2
		if r.slots && end-cx > 2*gitSlotWidth+8 {
			// Reserved slots: the name and tracking never move when the
			// controls appear.
			end -= 2 * gitSlotWidth
			m.paintGitControls(f, end+1, y, r, bg)
			end--
		}
		tw := ansi.StringWidth(r.when)
		room := end - cx
		if tw > 0 && tw+2 < room {
			tink := p.muted
			if r.branch.UpstreamGone {
				tink = p.gold
			}
			f.text(end-tw, y, tw, r.when, tink, bg)
			room -= tw + 2
		}
		f.componentText(cx, y, max(0, room), truncateCells(r.text, room), componentVisual{foreground: v.foreground, background: bg, bold: v.bold || r.on})
	}
}

// truncateCells cuts s to width cells with an ellipsis.
func truncateCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}
