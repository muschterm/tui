package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type navigationRow struct {
	kind   string
	thread protocol.Thread
}

func (m *Model) threadCardState(t protocol.Thread) componentState {
	return m.controlState(t.ID == m.state.Active, "thread:"+t.ID, "thread-status:"+t.ID, "thread-quick:"+t.ID, "thread-menu:"+t.ID, "thread-reopen:"+t.ID)
}

// A rounded surface contains title and metadata, with one blank row between
// threads. Its outline keeps the grouping visible in monochrome terminals.
// Register the whole row first so explicit status/menu hit areas take precedence.
func (m *Model) renderThreadCardBackground(f *frame, r shell.Rect, t protocol.Thread, part string) {
	p := m.colors()
	state := m.threadCardState(t)
	visual := m.componentStyle(roundedOutline, state, p.text, p.nav)
	bg := visual.background
	// All cells keep the sidebar background. Border and label styling carry
	// selection, hover and focus without changing the card silhouette.
	f.fill(r, p, p.nav)
	b := componentBorder(roundedOutline, m.plainIcons)
	edge, horizontal, left, right, ink := b.Left, b.Top, b.TopLeft, b.TopRight, visual.border
	if part == "card-bottom" {
		left, right = b.BottomLeft, b.BottomRight
	}
	if part == "card-top" || part == "card-bottom" {
		f.text(r.X, r.Y, r.W, left+strings.Repeat(horizontal, max(0, r.W-2))+right, ink, p.nav)
	} else {
		f.fill(shell.Rect{X: r.X + 1, Y: r.Y, W: max(0, r.W-2), H: 1}, p, bg)
		f.text(r.X, r.Y, 1, edge, ink, p.nav)
		f.text(r.X+r.W-1, r.Y, 1, edge, ink, p.nav)
	}
	a := action{Kind: "thread", ID: t.ID}
	f.hits = append(f.hits, hit{Rect: r, Action: a, Label: t.Title + " · " + t.Project, Key: "thread:" + t.ID})
}

func (m *Model) renderThreadRow(f *frame, r shell.Rect, t protocol.Thread) {
	p := m.colors()
	key, quick, more := "thread:"+t.ID, "thread-quick:"+t.ID, "thread-menu:"+t.ID
	interaction := m.threadCardState(t)
	ink := p.text
	if t.Closed {
		ink = m.dimmed()
	}
	visual := m.componentStyle(roundedOutline, interaction, ink, p.nav)
	bg, engaged := visual.background, interaction.Hovered || interaction.Focused
	a := action{Kind: "thread", ID: t.ID}
	labelWidth := max(1, r.W-8)
	// Reserve adjacent quick-action/menu slots, including when the quick action
	// is hidden. Keep a blank title gutter so hover never moves the title.
	label := fit(safe(t.Title), max(1, labelWidth-1))
	f.styledButton(r.X+2, r.Y, labelWidth, label, key, a, visual)
	f.hits[len(f.hits)-1].Label = t.Title + " · " + t.Project
	state := threadIndicator(t)
	glyph, help, fg := "●", t.State, m.threadIndicatorColor(state)
	if m.plainIcons {
		glyph = "o"
	}
	if state == threadAttention {
		help = "Needs attention · " + t.State
	}
	if state == threadFailed {
		help = "Error reported"
	}
	if state == threadCheckoutWaiting {
		help = "Waiting for checkout"
	}
	if !m.connected {
		help = "Disconnected · last known " + t.State
	}
	// Closed rows recede as a group; errors and attention keep their color.
	if t.Closed && (state == threadFinished || state == threadWorking) {
		fg = m.dimmed()
	}
	status := "thread-status:" + t.ID
	f.iconButton(m, r.X, r.Y, 2, glyph, status, a, fg, bg)
	f.hits[len(f.hits)-1].Label = help + " · " + t.Title
	kind, quickGlyph, quickHelp := "", "", ""
	if t.Closed {
		kind, quickGlyph, quickHelp, fg = "thread-delete", m.icon("trash"), "Delete thread permanently", p.red
	} else if m.connected && state == threadFinished && protocol.ThreadCloseBlocked(t) == "" {
		kind, quickGlyph, quickHelp = "thread-close", m.icon("check"), "Close thread"
	}
	if kind != "" {
		if !engaged {
			quickGlyph = ""
		}
		f.iconButton(m, r.X+r.W-6, r.Y, 3, centered(quickGlyph, 3), quick, action{Kind: kind, ID: t.ID}, fg, bg)
		f.hits[len(f.hits)-1].Label = quickHelp + " · " + t.Title
	}
	if t.Closed {
		// Reopen replaces the menu, whose only other item is the adjacent Delete.
		// Like trash, it appears only while the card is hovered or focused.
		reopen, glyph := "thread-reopen:"+t.ID, ""
		if engaged {
			glyph = m.icon("reopen")
		}
		f.iconButton(m, r.X+r.W-3, r.Y, 3, centered(glyph, 3), reopen, action{Kind: "thread-reopen", ID: t.ID}, p.muted, bg)
		f.hits[len(f.hits)-1].Label = "Reopen thread · " + t.Title
		return
	}
	f.iconButton(m, r.X+r.W-3, r.Y, 3, " "+m.icon("more-vertical")+" ", more, action{Kind: "thread-menu", ID: t.ID}, p.muted, bg)
	f.hits[len(f.hits)-1].Label = "Thread options · " + t.Title
}

func (m *Model) renderEmptyThreads(f *frame, r shell.Rect) {
	p := m.colors()
	x, y, w := r.X+2, r.Y+2, max(1, r.W-4)
	panelSectionHeading(f, m, x, y, w, "No thread selected")
	panelRule(f, m, x, y+1, w)
	actions := []struct{ label, key, kind string }{
		{"New thread", "empty-new", "thread-create"},
		{"Reopen a closed thread", "empty-closed", "closed-threads"},
		{"Add project", "empty-project", "project-add"},
	}
	// Buttons share one width that fits the longest label, so they read as
	// buttons rather than full-width rows.
	longest := 0
	for _, a := range actions {
		longest = max(longest, ansi.StringWidth(a.label))
	}
	bw := min(w, longest+4)
	// Bands need three rows each plus a gap; the one-row `[ Label ]` fallback
	// keeps the original two-row pitch.
	banded := panelBandsSupported(m) && y+3+4*len(actions)-1 <= r.Y+r.H
	for i, a := range actions {
		act := action{Kind: a.kind}
		v := m.componentStyle(squareFill, m.controlState(false, a.key), p.blue, p.input)
		v.base = p.canvas
		if !banded {
			row := y + 3 + 2*i
			if row >= r.Y+r.H {
				return
			}
			f.compactControl(m, x, row, bw, a.label, v)
			f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: row, W: bw, H: 1}, Action: act, Label: a.label, Key: a.key})
			continue
		}
		top := y + 3 + 4*i
		for band := -1; band <= 1; band++ {
			row := top + band + 1
			if !f.paintPanelBandEdge(x, row, bw, band, 3, v) {
				f.componentText(x, row, bw, " "+a.label, v)
				if v.focused {
					f.focusMark(x-1, row, v, v.base)
				}
			}
			f.registerPanelBandHit(r, row, band, 3, false, hit{Rect: shell.Rect{X: x, W: bw}, Action: act, Label: a.label, Key: a.key})
		}
	}
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
	// The prompt holds the queued item's text; only its own thread may load it.
	if m.state.Edit != nil && id != "" && id != m.state.Edit.ThreadID {
		m.status = "Save or cancel the queued edit first"
		return
	}
	if m.hasComposer() {
		v := m.viewState()
		v.Draft = m.prompt.Value()
		v.RightVisible = m.state.Layout.Right
	}
	m.state.Active, m.state.DraftProjectID = id, ""
	m.jobReq = ""
	m.state.Layout.Right = id != "" && m.viewState().RightVisible
	m.state.Layout.Maximized = false
	m.state.Layout.ClearReveal()
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
		if t.ID != exclude && !t.Closed && !jobThread(t) && (m.state.ProjectFilter == "" || t.ProjectID == m.state.ProjectFilter) {
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
	m.reconcileStartedDraft()
	m.followPendingStarts()
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
		next := m.nextOpenThread("")
		if m.state.Edit != nil {
			next = m.state.Edit.ThreadID
		}
		m.selectThread(next)
		m.status = "Thread deleted"
		m.markDirty()
	}
	if m.pendingThreadSelection != "" {
		if _, ok := m.threadByID(m.pendingThreadSelection); ok {
			// Blocked during a queued edit; the thread stays reachable in navigation.
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
				if m.pendingProjectDraft {
					m.pendingProjectDraft = false
					m.beginThreadDraft(p.ID)
				}
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
