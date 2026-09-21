package tui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

const (
	minTerminalWidth  = 40
	minTerminalHeight = 22
)

func (m *Model) terminalTooSmall() bool {
	return m.width < minTerminalWidth || m.height < minTerminalHeight
}

func lipColor(s string) color.Color { return lipgloss.Color(s) }
func (f *frame) put(r shell.Rect, text string) {
	if f.rows == nil || r.W <= 0 || r.H <= 0 {
		return
	}
	lines := strings.Split(text, "\n")
	for i := 0; i < r.H && i < len(lines); i++ {
		y := r.Y + i
		if y < 0 || y >= len(f.rows) {
			continue
		}
		line := fit(lines[i], r.W)
		f.rows[y] = compactSGR(ansi.Cut(f.rows[y], 0, r.X) + line + ansi.Cut(f.rows[y], r.X+r.W, ansi.StringWidth(f.rows[y])))
	}
}
func (f *frame) fill(r shell.Rect, p palette, bg string) {
	if f.rows != nil && r.W > 0 && r.H > 0 {
		f.put(r, strings.TrimSuffix(strings.Repeat(style(p.text, bg).Render(strings.Repeat(" ", r.W))+"\n", r.H), "\n"))
	}
}
func (f *frame) text(x, y, w int, s, fg, bg string) {
	if f.rows != nil && w > 0 {
		f.put(shell.Rect{X: x, Y: y, W: w, H: 1}, style(fg, bg).Render(fit(safe(s), w)))
	}
}
func (f *frame) button(m *Model, x, y, w int, label, key string, a action, fg, bg string) {
	f.styledButton(x, y, w, label, key, a, m.componentStyle(squareFill, m.controlState(false, key), fg, bg))
}

func (f *frame) selection(a, b [2]int) string {
	if a[1] > b[1] || a[1] == b[1] && a[0] > b[0] {
		a, b = b, a
	}
	var lines []string
	for y := max(0, a[1]); y <= b[1] && y < len(f.rows); y++ {
		start, end := 0, ansi.StringWidth(f.rows[y])
		if y == a[1] {
			start = max(0, a[0])
		}
		if y == b[1] {
			end = b[0] + 1
		}
		lines = append(lines, strings.TrimRight(ansi.Strip(ansi.Cut(f.rows[y], start, end)), " "))
	}
	return strings.Join(lines, "\n")
}
func (m *Model) compact() bool { return m.height < 28 }

func (m *Model) baseFooterHeight(w int) int {
	n := 3 + max(1, m.promptRows) + m.composerControlsHeight(w) + m.closedBannerHeight(w)
	if m.conversationVisible() {
		n += m.activityStripHeight(w) + m.queueHeight()
	}
	if len(m.viewState().Attachments) > 0 {
		n++
	}
	if m.viewState().ContextError != "" {
		n++
	}
	return n
}

func (m *Model) footerHeight() int {
	if m.settingsPage != "" {
		return 0
	}
	if !m.hasComposer() {
		return 0
	}
	w := max(1, m.state.Layout.Compute(m.width, m.height-1, 0).Center.W-2)
	if m.singleColumn() {
		w = max(1, m.width-2)
	}
	return m.baseFooterHeight(w) + m.requestHeight(w)
}

// Measurement follows the same layout/control path as painting, but avoids
// styling, ANSI row composition and textarea rendering during input routing.
func (m *Model) measure() frame { return m.compose(false) }
func (m *Model) render() frame  { return m.compose(true) }
func (m *Model) compose(paint bool) frame {
	p := m.colors()
	f := frame{}
	if paint {
		f.rows = make([]string, m.height)
		blank := style(p.text, p.canvas).Render(strings.Repeat(" ", m.width))
		for i := range f.rows {
			f.rows[i] = blank
		}
	}
	footer := m.footerHeight()
	f.geom = m.workspaceGeometry(footer)
	g := f.geom
	if m.terminalTooSmall() {
		f.text(1, 1, max(1, m.width-2), fmt.Sprintf("tui-go · resize to at least %d × %d", minTerminalWidth, minTerminalHeight), p.gold, p.canvas)
		f.button(m, 1, 3, 12, "Commands", "commands", action{Kind: "commands"}, p.blue, p.canvas)
		f.text(1, 5, max(1, m.width-2), "Draft preserved · Ctrl+Q detaches", p.text, p.canvas)
		if len(m.menu) > 0 {
			m.renderMenu(&f)
		}
		return f
	}
	if m.settingsPage != "" {
		m.renderSettingsWorkspace(&f)
		return f
	}
	m.renderChrome(&f, g)
	for _, r := range []shell.Rect{g.LeftDivider, g.RightDivider} {
		for y := r.Y; y < r.Y+r.H; y++ {
			f.text(r.X, y, r.W, "│", p.line, p.canvas)
		}
	}
	if g.BottomDivider.W > 0 {
		f.text(g.BottomDivider.X, g.BottomDivider.Y, g.BottomDivider.W, strings.Repeat("─", g.BottomDivider.W), p.line, p.canvas)
	}
	if g.Left.W > 0 {
		m.renderNav(&f, g.Left)
	}
	if g.Right.W > 0 {
		m.renderSurface(&f, g.Right)
	}
	if g.Bottom.W > 0 {
		m.renderBottom(&f, g.Bottom)
	}
	r := g.Center
	fh := min(footer, r.H)
	transcript := shell.Rect{X: r.X + 2, Y: r.Y + 1, W: max(1, r.W-4), H: max(0, r.H-fh-1)}
	f.transcript = transcript
	if transcript.H > 0 {
		m.renderTranscript(&f, transcript)
	}
	if !m.hasComposer() && r.H > 0 {
		m.renderEmptyThreads(&f, r)
	} else if m.hasComposer() {
		m.renderFooter(&f, shell.Rect{X: r.X + 1, Y: r.Y + r.H - fh, W: max(1, r.W-2), H: fh})
	}
	status := m.status
	if status == "" {
		status = "Enter Send  ·  Shift+Enter Newline  ·  F4 Commands  ·  Ctrl+Q Detach"
	}
	for _, h := range f.hits {
		if (h.Key == m.hover && m.hover != "") || (m.hover == "" && h.Key == m.focus && m.focus != "prompt" && m.focus != "answer") {
			status = h.Label
			break
		}
	}
	if m.notice.text != "" {
		status = m.notice.text
	}
	if notice := m.steeringNotice(); notice != "" {
		status = notice
	}
	connection := "● connected"
	if !m.connected {
		connection = "○ disconnected · stale"
	}
	f.text(1, m.height-1, m.width-2, connection+"  ·  "+status, p.muted, p.nav)
	m.renderMentions(&f)
	if len(m.menu) > 0 {
		m.renderMenu(&f)
	}
	return f
}

type contentLine struct {
	rightAligned bool
	text, fg, bg string
	action       action
}

func (m *Model) activityLines(items []protocol.Activity, w int) []contentLine {
	p := m.colors()
	var lines []contentLine
	for _, a := range items {
		fg, bg := p.text, p.canvas
		name := a.Title
		if name == "" {
			name = title(a.Role)
		}
		message := a.Role == "user" || a.Role == "agent"
		if a.Role == "user" {
			fg = p.blue
			bg = p.input
		}
		if a.Role == "agent" {
			fg = p.violet
		}
		if a.Role == "tool" || a.Role == "mcp" {
			fg = p.cyan
		}
		header := name
		if a.Role == "tool" || a.Role == "mcp" {
			header = m.icon(a.Role) + "  " + header
		}
		if a.State != "" {
			header += "  ·  " + a.State
		}
		act := action{}
		if a.Role == "tool" || a.Role == "mcp" {
			act = action{Kind: "open", Value: "activity", ID: a.ID}
		}
		// Message ownership is expressed by alignment and background. Operational
		// rows retain their meaningful titles and lifecycle state.
		if !message {
			lines = append(lines, contentLine{text: header, fg: fg, bg: bg, action: act})
		}
		wrapWidth := w - 2
		if a.Role == "user" {
			wrapWidth -= min(12, w/6)
		}
		for _, line := range strings.Split(ansi.Wrap(safe(a.Text), max(1, wrapWidth), ""), "\n") {
			lines = append(lines, contentLine{text: line, fg: p.text, bg: bg, action: act, rightAligned: a.Role == "user"})
		}
		lines = append(lines, contentLine{fg: p.text, bg: p.canvas}, contentLine{fg: p.text, bg: p.canvas})
	}
	return lines
}
func (m *Model) renderTranscript(f *frame, r shell.Rect) {
	p := m.colors()
	f.hits = append(f.hits, hit{r, action{}, "Transcript · wheel / arrows to scroll", "transcript"})
	lines := m.activityLines(m.thread().Activity, r.W)
	f.transcriptMax = max(0, len(lines)-r.H)
	offset := min(max(0, m.viewState().Scroll), f.transcriptMax)
	for i := 0; i < r.H && offset+i < len(lines); i++ {
		line := lines[offset+i]
		if line.action.Kind != "" {
			f.button(m, r.X, r.Y+i, r.W, line.text, "activity:"+line.action.ID+fmt.Sprint(i), line.action, line.fg, line.bg)
		} else {
			inset := 0
			if line.rightAligned {
				inset = min(12, r.W/6)
			}
			f.text(r.X+inset, r.Y+i, r.W-inset, line.text, line.fg, line.bg)
		}
	}
	f.scrollbar(m, shell.Rect{X: r.X + r.W, Y: r.Y, W: 1, H: r.H}, "transcript", len(lines), r.H, offset, p.canvas)
}
func (m *Model) renderFooter(f *frame, r shell.Rect) {
	p := m.colors()
	v := m.viewState()
	x, w, y := r.X, r.W, r.Y
	if m.conversationVisible() {
		y = m.renderActivityStrip(f, shell.Rect{X: x, Y: y, W: w})
		y = m.renderQueue(f, shell.Rect{X: x, Y: y, W: w, H: m.queueHeight()})
		if req, ok := m.request(); ok {
			y = m.renderRequest(f, shell.Rect{X: x, Y: y, W: w, H: m.requestHeight(w)}, req)
		}
	}
	if len(v.Attachments) > 0 {
		f.button(m, x, y, w, fmt.Sprintf("%d context attachments · manage / remove", len(v.Attachments)), "attachments", action{Kind: "attachments"}, p.blue, p.input)
		y++
	}
	if v.ContextError != "" {
		f.button(m, x, y, w, "Send failed: "+v.ContextError, "context-error", action{Kind: "context-error"}, p.red, p.canvas)
		y++
	}
	y = m.renderClosedBanner(f, shell.Rect{X: x, Y: y, W: w})
	f.fill(shell.Rect{X: x, Y: y, W: w, H: max(0, r.Y+r.H-y)}, p, p.canvas)
	promptHeight := max(1, m.promptRows)
	promptStyle := m.componentStyle(roundedOutline, m.controlState(false, "prompt"), p.text, p.input)
	f.componentBox(m, shell.Rect{X: x, Y: y, W: w, H: promptHeight + 2}, roundedOutline, promptStyle, p.canvas)
	y++
	inset := composerInset(w)
	f.prompt = shell.Rect{X: x + inset, Y: y, W: max(1, w-2*inset), H: promptHeight}
	if f.rows != nil {
		f.put(f.prompt, style(p.text, p.input).Width(f.prompt.W).Height(f.prompt.H).Render(m.promptView.View(&m.prompt)))
	}
	f.hits = append(f.hits, hit{f.prompt, action{}, "Enter sends · Shift+Enter / Ctrl+J adds a line", "prompt"})
	promptScroll := m.promptView.Metrics(m.promptMetrics)
	f.scrollbar(m, shell.Rect{X: f.prompt.X + f.prompt.W, Y: f.prompt.Y, W: 1, H: f.prompt.H}, "prompt", promptScroll.Total, f.prompt.H, promptScroll.Offset, p.input)
	y += promptHeight
	y = m.renderComposerControls(f, r, y+1)
	m.renderCheckoutContext(f, shell.Rect{X: x, Y: y, W: w, H: 1})
}
func (m *Model) renderSurface(f *frame, r shell.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	p := m.colors()
	v := m.viewState()
	f.fill(r, p, p.panel)
	x, y, w := r.X+1, r.Y+1, r.W-2
	if v.Host.Chooser || len(v.Host.Tabs) == 0 {
		kinds := []string{"files", "git", "terminal", "agents", "plan", "activity"}
		if r.H < len(kinds)+3 {
			// Use the scrollable modal when the fixed composer leaves too few
			// rows for the empty host's chooser. Never draw across its footer.
			f.button(m, x, r.Y+min(1, r.H-1), w, "Choose a surface…", "chooser", action{Kind: "chooser"}, p.blue, p.panel)
			return
		}
		f.text(x, y, w, "Add surface", p.text, p.panel)
		spacing := 1
		if r.H >= len(kinds)*2+2 {
			spacing = 2
		}
		for i, kind := range kinds {
			f.button(m, x, y+2+i*spacing, w, m.icon(kind)+"  "+title(kind), "chooser:"+kind, action{Kind: "open", Value: kind}, p.blue, p.panel)
		}
		return
	}
	active, _ := v.Host.Active()
	cx := x
	tabRows := 1
	visible, overflow := visibleTabs(v.Host.Tabs, active.ID, w-4)
	for _, slot := range visible {
		tab := slot.tab
		f.tab(m, cx, y, slot.width, tab.Title, tab.Kind, "tab:"+tab.ID, "close:"+tab.ID,
			action{Kind: "tab", ID: tab.ID}, action{Kind: "close", ID: tab.ID}, tab.ID == active.ID)
		cx += slot.width + 1
	}
	if overflow {
		f.button(m, x+w-8, y+tabRows/2, 4, " "+m.icon("more"), "tabs", action{Kind: "tabs"}, p.muted, p.panel)
		f.hits[len(f.hits)-1].Label = "Hidden tabs · open or close a surface"
	}
	f.button(m, x+w-4, y+tabRows/2, 4, " "+m.icon("add"), "chooser", action{Kind: "chooser"}, p.blue, p.panel)
	f.detail = shell.Rect{X: x + 1, Y: y + tabRows + 2, W: max(1, w-2), H: max(0, r.H-tabRows-4)}
	f.hits = append(f.hits, hit{f.detail, action{}, "Surface · wheel / arrows to scroll", "right-body"})
	text := m.surfaceText(active)
	lines := strings.Split(ansi.Wrap(safe(text), f.detail.W, ""), "\n")
	f.detailMax = max(0, len(lines)-f.detail.H)
	offset := min(max(0, v.DetailScroll), f.detailMax)
	for i := 0; i < f.detail.H && offset+i < len(lines); i++ {
		fg := p.text
		if i == 0 {
			fg = p.violet
		}
		f.text(f.detail.X, f.detail.Y+i, f.detail.W, lines[offset+i], fg, p.panel)
	}
	f.scrollbar(m, shell.Rect{X: f.detail.X + f.detail.W, Y: f.detail.Y, W: 1, H: f.detail.H}, "detail", len(lines), f.detail.H, offset, p.panel)
}
func (m *Model) surfaceText(s shell.Surface) string {
	t := m.thread()
	v := m.viewState()
	var b strings.Builder
	switch s.Kind {
	case "plan":
		b.WriteString("CURRENT PLAN\n\n")
		for _, step := range t.Plan {
			mark := "○"
			if step.State == "completed" {
				mark = "✓"
			}
			if step.State == "active" {
				mark = "●"
			}
			fmt.Fprintf(&b, "%s %s\n  %s\n\n", mark, step.Title, step.State)
		}
	case "agents":
		for _, c := range t.Children {
			if v.DetailID != "" && c.ID != v.DetailID {
				continue
			}
			fmt.Fprintf(&b, "%s · %s\nParent: %s\n\n", c.Name, c.State, c.ParentID)
			for _, a := range c.Activity {
				fmt.Fprintf(&b, "%s\n%s\n%s\n\n", a.Title, a.Text, a.Detail)
			}
		}
		if b.Len() == 0 {
			b.WriteString("Child history unavailable")
		}
	case "activity":
		if v.DetailID == "usage" {
			return "USAGE\n\nContext occupancy: unavailable\nContext capacity: unavailable\nBilling mode: unknown\nSubscription windows: unavailable\nAPI cost: unavailable\n\nNo agent telemetry is connected.\n\nCapabilities\n" + strings.Join(m.snapshot.Capabilities, "\n") + "\n\n" + m.keyboard + "\n" + m.colorDiagnostics() + "\nGraphics: not probed; text fallback"
		}
		for _, a := range t.Activity {
			if v.DetailID != "" && a.ID != v.DetailID {
				continue
			}
			fmt.Fprintf(&b, "%s · %s\n%s\n\n%s\n\n", a.Title, a.State, a.Text, a.Detail)
		}
		for _, r := range t.Requests {
			if v.DetailID != "" && r.ID != v.DetailID {
				continue
			}
			fmt.Fprintf(&b, "%s\n%s\nState: %s\nDelivery: %s\n\n", r.Title, r.Detail, r.State, r.Delivery)
		}
		if b.Len() == 0 {
			b.WriteString("No retained activity for this selection")
		}
	case "terminal":
		return m.terminalText(s.ID)
	case "files":
		return "FILES\n\nCheckout: " + t.Checkout + "\n\nCollaborative editor unavailable\n\nFile writes are not enabled in this slice.\n\nPlanned validation\n• Concurrent edits and own-edit undo\n• Durable buffers versus disk saves\n• External-change reconciliation"
	case "git":
		return "GIT\n\nCheckout: " + t.Checkout + "\n\nGit integration unavailable\n\nWorking-tree, staged and branch diffs will remain separate from recorded turn changes."
	}
	return b.String()
}
func (m *Model) terminalText(id string) string {
	for _, t := range m.snapshot.Terminals {
		if t.ID == id {
			return fmt.Sprintf("Terminal · %s\n%s\nController: %s\n\n%s", t.State, t.ID, t.Controller, t.Output)
		}
	}
	return "Terminal session unavailable · open a new session explicitly"
}
func (m *Model) renderBottom(f *frame, r shell.Rect) {
	p := m.colors()
	f.fill(r, p, p.panel)
	id := m.viewState().BottomID
	f.text(r.X+1, r.Y, r.W-12, m.icon("terminal")+"  Terminal", p.cyan, p.panel)
	if id == "" {
		f.button(m, r.X+2, r.Y+2, r.W-4, m.icon("add")+" New terminal", "bottom-new", action{Kind: "bottom-new"}, p.blue, p.panel)
		return
	}
	f.button(m, r.X+r.W-10, r.Y, 9, centered("Close "+m.icon("close"), 9), "bottom-close", action{Kind: "bottom-close"}, p.muted, p.panel)
	f.bottomBody = shell.Rect{X: r.X + 2, Y: r.Y + 2, W: max(1, r.W-4), H: max(0, r.H-2)}
	lines := strings.Split(ansi.Wrap(safe(m.terminalText(id)), f.bottomBody.W, ""), "\n")
	f.bottomMax = max(0, len(lines)-f.bottomBody.H)
	offset := min(max(0, m.viewState().BottomScroll), f.bottomMax)
	for i := 0; i < f.bottomBody.H && offset+i < len(lines); i++ {
		f.text(f.bottomBody.X, f.bottomBody.Y+i, f.bottomBody.W, lines[offset+i], p.text, p.panel)
	}
	f.hits = append(f.hits, hit{f.bottomBody, action{}, "Terminal output · wheel / arrows to scroll", "bottom-body"})
	f.scrollbar(m, shell.Rect{X: f.bottomBody.X + f.bottomBody.W, Y: f.bottomBody.Y, W: 1, H: f.bottomBody.H}, "bottom", len(lines), f.bottomBody.H, offset, p.panel)
}
func (m *Model) renderMenu(f *frame) {
	p := m.colors()
	r := m.menuRect()
	w, h := r.W, r.H
	extra := 0
	if m.projectMode != "" {
		extra = 2
	}
	m.renderModalBackdrop(f)
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, componentState{Focused: true}, p.text, p.input), p.canvas)
	f.hits = nil
	f.scrollbars = nil
	f.text(r.X+2, r.Y+1, w-7, m.menuTitle, p.violet, p.input)
	f.button(m, r.X+w-4, r.Y+1, 3, centered(m.icon("close"), 3), "menu-close", action{Kind: "menu-close"}, p.muted, p.input)
	if extra > 0 {
		input := shell.Rect{X: r.X + 2, Y: r.Y + 2, W: w - 4, H: 1}
		if f.rows != nil {
			f.put(input, m.projectInput.View())
		}
		f.hits = append(f.hits, hit{input, action{}, "Project search / folder path", "project-input"})
		caption, ink := m.projectError, p.red
		if caption == "" && (m.projectMode == "add" || m.projectMode == "project-root") {
			caption, ink = m.paths.result.Directory, p.muted
		}
		f.text(r.X+2, r.Y+3, w-4, caption, ink, p.input)
	}
	visible := h - 4 - extra
	start := m.menuStart(visible)
	for i := 0; i < visible && start+i < len(m.menu); i++ {
		index := start + i
		item := m.menu[index]
		if m.projectMode == "filter" && item.Action.Kind == "project-filter" && item.Action.ID != "" {
			if project, ok := m.projectByID(item.Action.ID); ok {
				y, key := r.Y+2+extra+i, fmt.Sprintf("menu:%d", index)
				state := m.controlState(index == m.menuIndex && !m.projectGear, key)
				f.styledButton(r.X+2, y, w-8, "   "+project.Name, key, action{Kind: "menu-select", Index: index}, m.componentStyle(squareFill, state, p.text, p.input))
				f.hits[len(f.hits)-1].Label = project.Name + " · " + project.Path
				m.renderProjectBadge(f, r.X+2, y, project)
				key = "project-settings:" + project.ID
				state = m.controlState(index == m.menuIndex && m.projectGear, key)
				f.styledButton(r.X+w-6, y, 3, centered(m.icon("settings"), 3), key, action{Kind: "project-settings", ID: project.ID}, m.componentStyle(squareFill, state, p.muted, p.input))
				f.hits[len(f.hits)-1].Label = "Project settings · " + project.Name
			}
		} else if item.Action.Kind == "tab" {
			f.tab(m, r.X+2, r.Y+2+extra+i, w-5, item.Label, item.Action.Value,
				fmt.Sprintf("menu:%d", index), "menu-tab-close:"+item.Action.ID,
				action{Kind: "menu-select", Index: index}, action{Kind: "menu-tab-close", ID: item.Action.ID}, index == m.menuIndex)
		} else {
			label := item.Label
			if item.Action.Kind == "open" {
				label = m.icon(item.Action.Value) + "  " + label
			}
			key := fmt.Sprintf("menu:%d", index)
			state := m.controlState(index == m.menuIndex, key)
			state.Focused = state.Focused || index == m.menuIndex && m.focus != "project-input"
			f.styledButton(r.X+2, r.Y+2+extra+i, w-5, label, key, action{Kind: "menu-select", Index: index}, m.componentStyle(squareFill, state, p.text, p.input))
		}
	}
	f.scrollbar(m, shell.Rect{X: r.X + w - 2, Y: r.Y + 2 + extra, W: 1, H: visible}, "menu", len(m.menu), visible, start, p.input)
	help := fmt.Sprintf("↑ ↓  Enter  Esc  %d/%d", m.menuIndex+1, len(m.menu))
	if m.projectMode == "add" || m.projectMode == "project-root" {
		help = "↑ ↓  Tab browse  Enter choose  Esc"
	}
	f.text(r.X+2, r.Y+h-2, w-4, help, p.muted, p.input)
}
func (m *Model) View() tea.View {
	f := m.render()
	if m.settingsPage == "" && (m.selecting || m.selectedText != "") && len(m.menu) == 0 {
		a, b := m.selectionStart, m.selectionEnd
		if a[1] > b[1] || a[1] == b[1] && a[0] > b[0] {
			a, b = b, a
		}
		for y := max(0, a[1]); y <= b[1] && y < len(f.rows); y++ {
			start, end := m.selectionRegion.X, m.selectionRegion.X+m.selectionRegion.W
			if y == a[1] {
				start = a[0]
			}
			if y == b[1] {
				end = b[0] + 1
			}
			line := ansi.Cut(f.rows[y], start, end)
			highlight := style(m.colors().text, m.colors().selected).Render(ansi.Strip(line))
			f.put(shell.Rect{X: start, Y: y, W: end - start, H: 1}, highlight)
		}
	}
	v := tea.NewView(strings.Join(f.rows, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = "tui-go"
	// OSC default-color changes bypass the renderer's color downsampling.
	// Limited-color and NO_COLOR clients keep their terminal defaults; the
	// cell grid already paints the chosen fallback surfaces explicitly.
	if m.colorProfile == colorprofile.TrueColor {
		v.BackgroundColor = lipColor(m.colors().canvas)
		v.ForegroundColor = lipColor(m.colors().text)
	}
	v.ReportFocus = true
	return v
}
