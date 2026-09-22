package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// The top bar is each pane's first row: its cells take the pane beneath
// them, and the dividers continue through it. Left to right: navigation
// toggle, the application title while navigation is open, the project /
// thread breadcrumb left-justified in the center pane, the attention bell at
// that pane's right edge, then the pane controls at the far right; while the
// right host is visible those controls belong to its span, maximize first.
func (m *Model) renderChrome(f *frame, g shell.Geometry) {
	p := m.colors()
	f.fill(g.Top, p, p.canvas)
	if m.singleColumn() {
		f.fill(g.Top, p, p.nav)
		f.iconButton(m, 0, 0, 5, centered(m.icon("menu"), 5), "columns", action{Kind: "columns"}, p.blue, p.nav)
		f.hits[len(f.hits)-1].Label = "Choose column · F2"
		attX := m.width - m.attentionWidth() - 1
		f.text(6, 0, max(0, attX-7), columnName(m.viewState().CompactColumn), p.text, p.nav)
		m.renderAttention(f, attX, p.nav)
		return // Column selection replaces pane toggles and maximize.
	}
	rightVisible := g.Right.W > 0 && g.Right.H > 0
	leftVisible := g.Left.W > 0
	if leftVisible {
		f.fill(shell.Rect{X: g.Left.X, Y: 0, W: g.Left.W, H: 1}, p, p.nav)
	}
	if rightVisible {
		f.fill(shell.Rect{X: g.Right.X, Y: 0, W: g.Right.W, H: 1}, p, p.panel)
	}
	for _, r := range []shell.Rect{g.LeftDivider, g.RightDivider} {
		if r.W > 0 {
			f.text(r.X, 0, r.W, "│", p.line, p.canvas)
		}
	}
	// The toggle sits on the navigation while it is open and on the center
	// pane while it is hidden, where the breadcrumb then follows it.
	toggleBg := p.canvas
	if leftVisible {
		toggleBg = p.nav
	}
	f.iconButton(m, 0, 0, 5, m.paneIcon("left", leftVisible, 5), "left", action{Kind: "left"}, p.blue, toggleBg)
	f.hits[len(f.hits)-1].Label = paneHelp("navigation", leftVisible, "F2")
	crumbFrom := 6
	if leftVisible {
		m.renderAppTitle(f, 5, min(g.Left.W, g.Center.X)-1)
		crumbFrom = g.Center.X + 1
	}
	// A forced full-width host has nothing to restore to: no control, no gap.
	maximizable := rightVisible && !g.Forced
	paneWidth := 13
	if maximizable {
		paneWidth += 6
	}
	controlX := m.width - paneWidth
	// The bell stays with the center pane, one control pitch (six cells,
	// glyph to glyph) left of the first pane control while the right host is
	// hidden; a maximized host leaves no center span, so it packs there too.
	bellLimit, bellBg := controlX-2, p.canvas
	if rightVisible && !g.Maximized {
		bellLimit = min(bellLimit, g.Right.X-1)
	}
	if g.Maximized {
		bellBg = p.panel
	}
	attX := bellLimit - m.attentionWidth()
	m.renderBreadcrumb(f, crumbFrom, attX-1, g)
	m.renderAttention(f, attX, bellBg)
	controlBg := p.canvas
	if rightVisible {
		controlBg = p.panel
	}
	if maximizable {
		icon := "maximize"
		if g.Maximized {
			icon = "restore"
		}
		f.iconButton(m, controlX, 0, 6, slotIcon(m.icon(icon), 6), "maximize", action{Kind: "maximize"}, p.blue, controlBg)
		f.hits[len(f.hits)-1].Label = "Maximize / restore right panel · F7"
		controlX += 6
	}
	f.iconButton(m, controlX, 0, 6, m.paneIcon("bottom", g.Bottom.H > 0, 6), "bottom", action{Kind: "bottom"}, p.blue, controlBg)
	f.hits[len(f.hits)-1].Label = paneHelp("bottom panel", g.Bottom.H > 0, "F5")
	f.iconButton(m, controlX+6, 0, 7, m.paneIcon("right", rightVisible, 7), "right", action{Kind: "right"}, p.blue, controlBg)
	f.hits[len(f.hits)-1].Label = paneHelp("right panel", rightVisible, "F3")
}

// The title starts a thread in the filtered project, or opens the project
// chooser when no project is selected. It is drawn on the navigation pane
// and truncates to the cells before limit.
func (m *Model) renderAppTitle(f *frame, x, limit int) {
	p := m.colors()
	title := fit(m.appTitle(), max(0, limit-x-1))
	title = strings.TrimRight(title, " ")
	w := ansi.StringWidth(title)
	if w == 0 {
		return
	}
	a, help := action{Kind: "thread-create", Value: m.state.ProjectFilter}, "New thread · choose a project"
	for _, project := range m.snapshot.Projects {
		if project.ID == m.state.ProjectFilter {
			help = "New thread in " + project.Name
		}
	}
	visual := m.iconStyle(m.controlState(false, "app-title"), p.text, p.nav)
	visual.bold = true // The title is always bold; hover lifts its ink.
	f.styledButton(x+1, 0, w, title, "app-title", a, visual)
	f.hits[len(f.hits)-1].Label = help
}

func (m *Model) appTitle() string { return "tui-go" }

// Project (icon and name, one hover target starting a thread there) then a
// separator and the thread title, left-justified at from on the center pane.
func (m *Model) renderBreadcrumb(f *frame, from, to int, g shell.Geometry) {
	p := m.colors()
	if !m.hasComposer() || to-from < 4 {
		return
	}
	t := m.thread()
	// The crumb leads with the same two-cell project badge as navigation and
	// the project picker: the chosen icon, else the colored monogram. A thread
	// whose project is no longer listed still gets a monogram of its name.
	project, ok := m.projectByID(t.ProjectID)
	if !ok {
		project = protocol.Project{Name: t.Project}
	}
	icon := "  " // badge cells, painted after the label
	available := to - from
	name, sep, title := safe(t.Project), "  /  ", safe(t.Title)
	// Nothing truncates while the whole crumb fits. Otherwise the project
	// keeps at least a third of the space and the title takes the rest.
	if over := ansi.StringWidth(icon+" "+name+sep+title) - available; over > 0 {
		nameLimit := max(available/3-2, ansi.StringWidth(name)-over)
		name = strings.TrimRight(fit(name, max(1, min(ansi.StringWidth(name), nameLimit))), " ")
		title = strings.TrimRight(fit(title, max(1, available-ansi.StringWidth(icon+" "+name+sep))), " ")
	}
	crumb := icon + " " + name
	if ansi.StringWidth(crumb)+ansi.StringWidth(sep) > available {
		crumb, sep, title = strings.TrimRight(fit(crumb, available), " "), "", ""
	}
	visual := m.iconStyle(m.controlState(false, "breadcrumb-project"), p.muted, p.canvas)
	f.styledButton(from, 0, ansi.StringWidth(crumb), crumb, "breadcrumb-project", action{Kind: "thread-create", Value: t.ProjectID}, visual)
	f.hits[len(f.hits)-1].Label = "New thread in " + t.Project
	m.renderProjectBadge(f, from, 0, project, p.canvas)
	f.text(from+ansi.StringWidth(crumb), 0, ansi.StringWidth(sep)+ansi.StringWidth(title), sep+title, p.text, p.canvas)
}

func (m *Model) attentionCount() int {
	attention := 0
	for _, t := range m.snapshot.Threads {
		for _, r := range t.Requests {
			if r.State == "pending" {
				attention++
			}
		}
		if t.State == "failed" {
			attention++
		}
	}
	return attention
}

func (m *Model) attentionWidth() int {
	width := ansi.StringWidth(m.icon("attention")) + 2
	if count := m.attentionCount(); count > 0 {
		width += len(strconv.Itoa(count)) + 3
	}
	return width
}

func (m *Model) renderAttention(f *frame, attX int, bg string) {
	p := m.colors()
	attention := m.attentionCount()
	attentionWidth := m.attentionWidth()
	bellWidth := ansi.StringWidth(m.icon("attention"))
	badge := ""
	if attention > 0 {
		badge = strconv.Itoa(attention)
	}
	fg := p.muted
	if attention > 0 {
		fg = p.gold
	}
	f.iconButton(m, attX, 0, attentionWidth, " "+m.icon("attention"), "attention", action{Kind: "attention"}, fg, bg)
	f.hits[len(f.hits)-1].Label = fmt.Sprintf("Open attention · %d pending items", attention)
	if badge != "" {
		f.text(attX+bellWidth+2, 0, ansi.StringWidth(badge)+2, " "+badge+" ", p.nav, p.gold)
	}
}
