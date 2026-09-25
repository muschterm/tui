package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_conflict_view.go paints the conflict viewer and the conflict rows'
// controls and menu (git_conflict.go).

// gitConflictViewerLines bounds the painted content lines.
const gitConflictViewerLines = 2000

// gitConflictControls are a conflict row's reserved slots: View, then the
// row menu. Rows under agent review (S4) keep only the menu.
func (m *Model) gitConflictControls(c protocol.GitConflict) [2]*gitControl {
	var out [2]*gitControl
	if !m.gitConflictsEnabled() {
		return out
	}
	path := safe(singleLine(c.Path))
	if !m.gitConflictReviewMode(c) {
		out[0] = &gitControl{glyph: m.icon("files"), key: "git-cact:view:" + c.Path, help: "View versions · " + path + " (Enter)",
			action: action{Kind: "git-conflict-view", ID: c.Path}}
	}
	out[1] = &gitControl{glyph: m.icon("more-vertical"), key: "git-cact:menu:" + c.Path, help: "Conflict actions · " + path + " (Shift+F10)",
		action: action{Kind: "git-conflict-menu", ID: "git:conflict:" + c.Path}}
	return out
}

// openGitConflictMenu opens a conflict row's menu. Every item names the
// path and side it writes.
func (m *Model) openGitConflictMenu(focus string) tea.Cmd {
	path, ok := strings.CutPrefix(focus, "git:conflict:")
	if rest, isSlot := strings.CutPrefix(focus, "git-cact:"); isSlot {
		_, path, ok = strings.Cut(rest, ":")
		focus = "git:conflict:" + path
	}
	if !ok || len(m.menu) != 0 || !m.gitConflictsEnabled() {
		return nil
	}
	c, found := m.gitRowConflict(path)
	if !found {
		return nil
	}
	p := safe(singleLine(path))
	a := func(kind string) action { return action{Kind: kind, ID: c.Path} }
	items := []menuItem{
		{Label: "View versions (Enter)", Action: a("git-conflict-view")},
		{Label: "Choose " + m.gitSideLabel("ours") + " (o)", Action: a("git-conflict-choose-ours")},
		{Label: "Choose " + m.gitSideLabel("theirs") + " (t)", Action: a("git-conflict-choose-theirs")},
		{Label: "Choose " + m.gitSideLabel("base"), Action: a("git-conflict-choose-base")},
	}
	if !c.Binary && !c.Submodule {
		items = append(items, menuItem{Label: "Edit in Files (e)", Action: a("git-conflict-edit")})
	}
	items = append(items,
		menuItem{Label: "Mark resolved (m)", Action: a("git-conflict-resolve")},
		menuItem{Label: "Resolve as deleted…", Action: a("git-conflict-delete")},
		menuItem{Label: "Restore a saved copy…", Action: a("git-conflict-restore-menu")})
	items = append(items, m.gitConflictAgentItems(c)...)
	var cmd tea.Cmd
	if m.focus != focus {
		cmd = m.setFocus(focus)
	}
	m.showMenuFor("Conflict · ", truncatePathLeft(p, gitDialogPathWidth), items)
	m.contextMenu = &contextMenuState{returnFocus: focus}
	return cmd
}

// gitConflictKey handles conflict-row keys: o/t choose, m mark resolved,
// e edit (Enter views through the row action).
func (m *Model) gitConflictKey(s string) (tea.Cmd, bool) {
	focus := m.focus
	if rest, ok := strings.CutPrefix(focus, "git-cact:"); ok {
		_, path, _ := strings.Cut(rest, ":")
		focus = "git:conflict:" + path
	}
	path, ok := strings.CutPrefix(focus, "git:conflict:")
	if !ok || !m.gitConflictsEnabled() {
		return nil, false
	}
	kind := map[string]string{"o": "git-conflict-choose-ours", "t": "git-conflict-choose-theirs", "m": "git-conflict-resolve", "e": "git-conflict-edit"}[s]
	if kind == "" {
		return nil, false
	}
	return m.activate(action{Kind: kind, ID: path}), true
}

// gitConflictViewerRefresh keeps an open viewer on the row's current pins:
// when the path's pins changed, its cached versions are reread.
func (m *Model) gitConflictViewerRefresh(key string, g *gitView) tea.Cmd {
	v := m.gitCF.viewer
	if v == nil || v.key != key {
		return nil
	}
	for _, c := range g.oper.Conflicts {
		if c.Path == v.conflict.Path {
			if c.ConflictPin != v.conflict.ConflictPin || c.WorktreeStat != v.conflict.WorktreeStat {
				v.conflict = c
				v.reset()
				return m.loadViewerTab(v)
			}
			return nil
		}
	}
	// Resolved or gone: the working version may still be read.
	v.reset()
	return m.loadViewerTab(v)
}

// gitConflictViewerBlocks is the viewer's body.
func (m *Model) gitConflictViewerBlocks(v *gitConflictViewer) []surfaceBlock {
	p := m.colors()
	gap := surfaceBlock{kind: surfaceGapBlock}
	text := func(s, ink string) surfaceBlock { return surfaceBlock{kind: surfaceTextBlock, value: s, ink: ink} }
	button := func(label, glyph, k string, a action) surfaceBlock {
		return surfaceBlock{kind: surfaceGitBlock, git: &gitRow{compose: "button", text: label, mark: glyph, key: k, action: a, help: label}}
	}
	c := v.conflict
	b := []surfaceBlock{
		{kind: surfaceHeadingBlock, label: "Conflict", value: gitConflictBadge(c.Kind)},
		text(safe(singleLine(c.Path)), p.text),
		button("Close", m.icon("close"), "git:conflict-close", action{Kind: "git-conflict-close"}),
		{kind: surfaceGitBlock, git: &gitRow{kind: "ctabs", text: v.tab, key: "", help: "Versions"}},
	}
	if label := m.gitSideLabel(v.tab); v.tab != protocol.GitConflictVersionWorking && v.tab != protocol.GitConflictVersionSaved && label != v.tab {
		b = append(b, text(label, p.muted))
	}
	f := v.files[v.tab]
	switch {
	case v.loading[v.tab]:
		b = append(b, statusBlock(m, "Reading "+v.tab+"…", "pending", false))
	case v.errs[v.tab] != "":
		b = append(b, statusBlock(m, "Could not read "+v.tab, "failed", true), text(v.errs[v.tab], p.muted))
	case f == nil:
	case !f.Present:
		what := "Absent on this side (deleted)"
		switch f.Kind {
		case "directory":
			what = "A directory is in the way · resolve in a terminal"
		case "other":
			what = "A special file · not shown"
		}
		if v.tab == protocol.GitConflictVersionWorking && f.Kind == "absent" {
			what = "The file is absent in the working tree"
		}
		b = append(b, text(what, p.muted))
	case f.Symlink:
		b = append(b, text("Symbolic link → "+safe(singleLine(string(f.Content))), p.text))
	case f.Binary:
		b = append(b, text("Binary file · "+strconv.FormatInt(f.Size, 10)+" bytes · not shown", p.muted))
	default:
		if v.tab == protocol.GitConflictVersionWorking {
			switch {
			case f.HasMarkers:
				b = append(b, text("Contains conflict markers", p.gold))
			case f.MarkersUnknown:
				b = append(b, text("Too large to check for conflict markers", p.gold))
			}
		}
		lines := v.lines[v.tab]
		for _, line := range lines {
			ink := p.text
			if line.marker {
				ink = p.gold
			}
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: line.text, ink: ink})
		}
		if f.Truncated || len(lines) == gitConflictViewerLines {
			b = append(b, text("Showing the first part · the file is larger", p.muted))
		}
	}
	if f != nil && v.tab == protocol.GitConflictVersionSaved && len(f.Copies) > 0 {
		b = append(b, gap, text("Saved copies of this stop:", p.muted))
		for _, cp := range f.Copies {
			b = append(b, text("  "+gitCopyLabel(f, cp.CopyID), p.muted))
		}
	}
	b = append(b, gap)
	a := func(kind string) action { return action{Kind: kind, ID: c.Path} }
	b = append(b,
		button("Choose "+m.gitSideLabel("ours"), m.icon("check"), "git:conflict-ours", a("git-conflict-choose-ours")),
		button("Choose "+m.gitSideLabel("theirs"), m.icon("check"), "git:conflict-theirs", a("git-conflict-choose-theirs")),
		button("Choose "+m.gitSideLabel("base"), m.icon("check"), "git:conflict-base", a("git-conflict-choose-base")))
	if !c.Binary && !c.Submodule {
		b = append(b, button("Edit in Files", m.icon("files"), "git:conflict-edit", a("git-conflict-edit")))
	}
	b = append(b,
		button("Mark resolved", m.icon("add"), "git:conflict-resolve", a("git-conflict-resolve")),
		button("Resolve as deleted…", m.icon("trash"), "git:conflict-delete", a("git-conflict-delete")),
		button("Restore a saved copy…", m.icon("reopen"), "git:conflict-restore", a("git-conflict-restore-menu")))
	return b
}

// gitMarkerLine reports a conflict-marker line (7 repeated marker
// characters at the line start, then a space or the line end).
func gitMarkerLine(line string) bool {
	for _, mk := range []string{"<<<<<<<", "=======", ">>>>>>>", "|||||||"} {
		if rest, ok := strings.CutPrefix(line, mk); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\r') {
			return true
		}
	}
	return false
}

// paintGitConflictTabs paints the version tabs as square fills: the
// selected tab accent and bold.
func (m *Model) paintGitConflictTabs(f *frame, x, y, width int, r *gitRow) {
	p := m.colors()
	cx := x
	for _, tab := range gitConflictTabs {
		label := " " + title(tab) + " "
		w := ansi.StringWidth(label)
		if cx+w > x+width {
			break
		}
		key := "git:conflict-tab:" + tab
		s := m.controlState(r.text == tab, key)
		v := m.componentStyle(squareFill, s, p.text, p.panel)
		f.styledButton(cx, y, w, label, key, action{Kind: "git-conflict-tab", Value: tab}, v)
		f.hits[len(f.hits)-1].Label = "Show " + tab
		cx += w
	}
}
