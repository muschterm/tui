package tui

import (
	"fmt"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type navigationRow struct {
	kind   string
	thread protocol.Thread
}

func (m *Model) navigationRows() []navigationRow {
	var open, closed []navigationRow
	for _, t := range m.snapshot.Threads {
		if m.state.ProjectFilter != "" && t.ProjectID != m.state.ProjectFilter {
			continue
		}
		rows := []navigationRow{{"thread", t}, {"state", t}}
		if t.Closed {
			closed = append(closed, rows...)
		} else {
			open = append(open, rows...)
		}
	}
	if len(open) == 0 {
		open = append(open, navigationRow{kind: "empty"})
	}
	open = append(open, navigationRow{kind: "gap"}, navigationRow{kind: "closed"})
	if !m.state.RecentsHidden && !m.state.RecentsCollapsed {
		open = append(open, closed...)
	}
	return open
}

func (m *Model) renderNav(f *frame, r shell.Rect) {
	p := m.colors()
	f.fill(r, p, p.nav)
	x, w := r.X+2, max(1, r.W-4)
	f.text(x, r.Y+1, w, "PROJECTS", p.muted, p.nav)
	label := "All projects"
	for _, project := range m.snapshot.Projects {
		if project.ID == m.state.ProjectFilter {
			label = project.Name
		}
	}
	f.button(m, x, r.Y+2, max(1, w-3), label, "projects", action{Kind: "projects"}, p.text, p.nav)
	f.button(m, x+w-3, r.Y+2, 3, m.icon("project-add"), "project-add", action{Kind: "project-add"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = "Add an existing project folder"
	f.button(m, x, r.Y+3, w, m.icon("add")+" New thread", "thread-create", action{Kind: "thread-create"}, p.blue, p.nav)
	f.navigation = shell.Rect{X: x, Y: r.Y + 5, W: w, H: max(0, r.H-10)}
	f.hits = append(f.hits, hit{f.navigation, action{}, "Threads · wheel / arrows to scroll", "navigation"})
	rows := m.navigationRows()
	f.navMax = max(0, len(rows)-f.navigation.H)
	offset := min(max(0, m.navScroll), f.navMax)
	for i := 0; i < f.navigation.H && offset+i < len(rows); i++ {
		row, y := rows[offset+i], f.navigation.Y+i
		switch row.kind {
		case "thread":
			m.renderThreadRow(f, shell.Rect{X: x, Y: y, W: w, H: 1}, row.thread)
		case "state":
			label := row.thread.State
			if m.state.ProjectFilter == "" {
				label += " · " + row.thread.Project
			}
			f.text(x+2, y, max(1, w-2), label, p.muted, p.nav)
		case "empty":
			f.text(x, y, w, "No open threads", p.muted, p.nav)
		case "closed":
			count := 0
			for _, t := range m.snapshot.Threads {
				if t.Closed && (m.state.ProjectFilter == "" || t.ProjectID == m.state.ProjectFilter) {
					count++
				}
			}
			label := fmt.Sprintf("CLOSED (%d)", count)
			a := action{Kind: "recents-collapse"}
			if m.state.RecentsHidden {
				label = "Show Closed"
				a.Kind = "recents-hide"
			}
			f.button(m, x, y, w, label, "recents", a, p.muted, p.nav)
		}
	}
	f.scrollbar(m, shell.Rect{X: x + w, Y: f.navigation.Y, W: 1, H: f.navigation.H}, "navigation", len(rows), f.navigation.H, offset, p.nav)
	f.button(m, x, r.Y+r.H-4, w, "Commands  F4", "commands", action{Kind: "commands"}, p.muted, p.nav)
	f.button(m, x, r.Y+r.H-2, w, m.icon("theme")+"  Theme  F8", "theme", action{Kind: "theme"}, p.violet, p.nav)
}

func (m *Model) renderThreadRow(f *frame, r shell.Rect, t protocol.Thread) {
	p := m.colors()
	key, quick, more := "thread:"+t.ID, "thread-quick:"+t.ID, "thread-menu:"+t.ID
	engaged := m.hover == key || m.hover == quick || m.hover == more || m.focus == key || m.focus == quick || m.focus == more
	bg := p.nav
	if t.ID == m.state.Active || engaged {
		bg = p.selected
	}
	a := action{Kind: "thread", ID: t.ID}
	if t.Closed {
		a.Kind = "thread-reopen"
	}
	labelWidth := max(1, r.W-5)
	// Keep a trailing title cell blank and center the menu glyph in its slot.
	// Long titles must not run their truncation mark into the vertical ellipsis.
	label := fit(safe(t.Title), max(1, labelWidth-1))
	f.button(m, r.X+2, r.Y, labelWidth, label, key, a, p.text, bg)
	f.hits[len(f.hits)-1].Label = t.Title + " · " + t.Project
	state := threadIndicator(t)
	glyph, kind, help, fg := "●", "thread", t.State, m.threadIndicatorColor(state)
	if m.plainIcons {
		glyph = "o"
	}
	if state == threadAttention {
		help = "Needs attention · " + t.State
	}
	if state == threadFailed {
		help = "Error reported"
	}
	if !m.connected {
		help = "Disconnected · last known " + t.State
	}
	if t.Closed {
		kind, help = "thread-delete", "Delete thread permanently"
		if engaged {
			glyph, fg = m.icon("trash"), p.red
		}
	} else if m.connected && state == threadFinished && protocol.ThreadCloseBlocked(t) == "" {
		kind, help = "thread-close", "Close thread"
		if engaged {
			glyph = m.icon("check")
		}
	}
	f.button(m, r.X, r.Y, 2, glyph, quick, action{Kind: kind, ID: t.ID}, fg, bg)
	f.hits[len(f.hits)-1].Label = help + " · " + t.Title
	// Generic button hover uses blue; preserve the status color on this slot.
	f.text(r.X, r.Y, 2, glyph, fg, bg)
	f.button(m, r.X+r.W-3, r.Y, 3, " "+m.icon("more-vertical")+" ", more, action{Kind: "thread-menu", ID: t.ID}, p.muted, bg)
	f.hits[len(f.hits)-1].Label = "Thread options · " + t.Title
}

func (m *Model) renderEmptyThreads(f *frame, r shell.Rect) {
	p := m.colors()
	x, y, w := r.X+2, r.Y+2, max(1, r.W-4)
	f.text(x, y, w, "No thread selected", p.text, p.canvas)
	f.button(m, x, y+2, w, "New thread", "empty-new", action{Kind: "thread-create"}, p.blue, p.canvas)
	f.button(m, x, y+4, w, "Reopen a closed thread", "empty-closed", action{Kind: "closed-threads"}, p.blue, p.canvas)
	f.button(m, x, y+6, w, "Add project", "empty-project", action{Kind: "project-add"}, p.blue, p.canvas)
}

func (m *Model) threadByID(id string) (protocol.Thread, bool) {
	for _, t := range m.snapshot.Threads {
		if t.ID == id {
			return t, true
		}
	}
	return protocol.Thread{}, false
}

func (m *Model) selectThread(id string) {
	_, ok := m.threadByID(id)
	if !ok && id != "" {
		return
	}
	if m.state.Active != "" {
		v := m.viewState()
		v.Draft = m.prompt.Value()
		v.RightVisible = m.state.Layout.Right
	}
	m.state.Active = id
	m.state.Layout.Right = id != "" && m.viewState().RightVisible
	m.state.Layout.Maximized = false
	if id == "" {
		m.prompt.SetValue("")
		m.answer.SetValue("")
	} else {
		m.loadDraft()
	}
	m.setFocus("prompt")
}

func (m *Model) nextOpenThread(exclude string) string {
	for _, t := range m.snapshot.Threads {
		if t.ID != exclude && !t.Closed && (m.state.ProjectFilter == "" || t.ProjectID == m.state.ProjectFilter) {
			return t.ID
		}
	}
	return ""
}

func (m *Model) reconcileThreadMembership() {
	live := map[string]bool{}
	for _, t := range m.snapshot.Threads {
		live[t.ID] = true
	}
	for id := range m.state.Threads {
		if !live[id] {
			delete(m.state.Threads, id)
			m.markDirty()
		}
	}
	for id := range m.requestFeedback {
		if !live[id] {
			delete(m.requestFeedback, id)
		}
	}
	if m.state.Edit != nil && !live[m.state.Edit.ThreadID] {
		m.state.Edit = nil
		m.markDirty()
	}
	if m.busy != nil && m.busy.ThreadID != "" && !live[m.busy.ThreadID] {
		m.busy = nil
		m.inFlight = false
		m.state.Pending = nil
		m.busyAction = action{}
		m.state.PendingAction = action{}
		m.markDirty()
	}
	if m.state.Active != "" && !live[m.state.Active] {
		// Do not save the deleted thread's input into its replacement.
		m.state.Active = ""
		m.prompt.SetValue("")
		m.selectThread(m.nextOpenThread(""))
		m.status = "Thread deleted"
		m.markDirty()
	}
	if m.pendingThreadSelection != "" {
		if _, ok := m.threadByID(m.pendingThreadSelection); ok {
			m.selectThread(m.pendingThreadSelection)
			m.pendingThreadSelection = ""
			m.markDirty()
		}
	}
	if m.pendingProjectSelection != "" {
		for _, p := range m.snapshot.Projects {
			if p.ID == m.pendingProjectSelection {
				m.state.ProjectFilter = p.ID
				m.pendingProjectSelection = ""
				m.markDirty()
				break
			}
		}
	}
	if len(m.menu) > 0 && strings.HasPrefix(m.menuTitle, "Delete thread") {
		for _, item := range m.menu {
			if item.Action.Kind == "thread-delete-confirm" && !live[item.Action.ID] {
				m.menu = nil
				break
			}
		}
	}
}
