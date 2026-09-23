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
		// Rows stay exactly as wide as the terminal: clip to it, and let
		// cutCells turn any wide cluster split by an edge into styled spaces.
		width := ansi.StringWidth(f.rows[y])
		x0, x1 := max(0, r.X), min(width, r.X+r.W)
		if x0 >= x1 {
			continue
		}
		line := fit(lines[i], r.W)
		if x1-x0 < r.W {
			line = cutCells(line, x0-r.X, x1-r.X)
		}
		f.rows[y] = compactSGR(cutCells(f.rows[y], 0, x0) + line + cutCells(f.rows[y], x1, width))
	}
}

func (f *frame) fill(r shell.Rect, p palette, bg string) {
	if f.rows != nil && r.W > 0 && r.H > 0 {
		f.put(r, strings.TrimSuffix(strings.Repeat(style(p.text, bg).Render(strings.Repeat(" ", r.W))+"\n", r.H), "\n"))
	}
}

func (f *frame) text(x, y, w int, s, fg, bg string) {
	if f.rows != nil && w > 0 {
		f.put(shell.Rect{X: x, Y: y, W: w, H: 1}, style(fg, bg).Render(fit(singleLine(s), w)))
	}
}

func (f *frame) button(m *Model, x, y, w int, label, key string, a action, fg, bg string) {
	f.styledButton(x, y, w, label, key, a, m.componentStyle(squareFill, m.controlState(false, key), fg, bg))
}

type selectionSpan struct{ y, start, end int }

// Spans bound every selected row to the region it was taken from and to the
// frame, so neither the highlight nor the copy can reach neighbouring panes.
func selectionSpans(a, b [2]int, region shell.Rect, width, height int) []selectionSpan {
	if a[1] > b[1] || a[1] == b[1] && a[0] > b[0] {
		a, b = b, a
	}
	left, right := max(0, region.X), min(width, region.X+region.W)
	var spans []selectionSpan
	for y := max(0, region.Y, a[1]); y <= b[1] && y < min(height, region.Y+region.H); y++ {
		start, end := left, right
		if y == a[1] {
			start = max(left, a[0])
		}
		if y == b[1] {
			end = min(right, b[0]+1)
		}
		if start < end {
			spans = append(spans, selectionSpan{y, start, end})
		}
	}
	return spans
}

func (f *frame) selection(a, b [2]int, region shell.Rect) string {
	var lines []string
	for _, s := range selectionSpans(a, b, region, region.X+region.W, len(f.rows)) {
		lines = append(lines, strings.TrimRight(ansi.Strip(cutCells(f.rows[s.y], s.start, s.end)), " "))
	}
	return strings.Join(lines, "\n")
}

// A selection is screen coordinates plus copied text. It only means anything
// while everything that placed that text there is unchanged.
type selectionBasis struct {
	thread, surface, detailID        string
	width, height                    int
	scroll, lines, detailScroll, max int
	transcript, detail               shell.Rect
}

func (m *Model) selectionBasisFor(f frame) selectionBasis {
	v := m.viewState()
	return selectionBasis{
		thread: m.state.Active, surface: v.Host.ActiveID, detailID: v.DetailID,
		width: m.width, height: m.height,
		scroll: min(max(0, v.Scroll), f.transcriptMax), lines: f.transcriptMax,
		detailScroll: min(max(0, v.DetailScroll), f.detailMax), max: f.detailMax,
		transcript: f.transcript, detail: f.detail,
	}
}

func (m *Model) selectionLive(f frame) bool {
	return (m.selecting || m.selectedText != "") && m.settingsPage == "" && !m.terminalTooSmall() &&
		(m.selectionRegion == f.transcript || m.selectionRegion == f.detail) &&
		m.selectionBasis == m.selectionBasisFor(f)
}

func (m *Model) compact() bool { return m.height < 28 }

// The composer is its rounded outline (two rows), one padding row above the
// typing area, the typing rows, the settings/actions rows and the checkout row.
func (m *Model) baseFooterHeight(w int) int {
	n := 4 + max(1, m.promptRows) + m.composerControlsHeight(w) + m.closedBannerHeight(w)
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
	if m.terminalTooSmall() {
		// No geometry: dividers, panes and scrollbars are unreachable here.
		f.text(1, 1, max(1, m.width-2), fmt.Sprintf("tui-go · resize to at least %d × %d", minTerminalWidth, minTerminalHeight), p.gold, p.canvas)
		f.button(m, 1, 3, 12, "Commands", "commands", action{Kind: "commands"}, p.blue, p.canvas)
		f.text(1, 5, max(1, m.width-2), "Draft preserved · Ctrl+Q detaches", p.text, p.canvas)
		if len(m.menu) > 0 {
			m.renderMenu(&f)
		}
		return f
	}
	footer := m.footerHeight()
	f.geom = m.workspaceGeometry(footer)
	g := f.geom
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
	outset           bool // painted across the prompt outline's extent, as answered question cards are
	boxed            bool // tinted full-width row of a user message box
	styled           bool // text contains only renderer-generated ANSI, after input sanitization
	text, fg, bg     string
	marker, markerFG string
	action           action
	// border is the outline ink of an outlined row's first and last cells,
	// painted on the canvas like componentBox's side cells; the interior
	// between them uses fg/bg. Border rows themselves carry the ink as fg.
	border string
}

func (m *Model) activityLines(items []protocol.Activity, w int) []contentLine {
	return m.activityLinesForThread(protocol.Thread{}, items, w)
}

func (m *Model) activityLinesForThread(t protocol.Thread, items []protocol.Activity, w int) []contentLine {
	p := m.colors()
	var lines []contentLine
	for _, a := range items {
		if t.ID != "" {
			if history, handled := m.questionHistoryLines(t, a, w); handled {
				if len(history) > 0 {
					lines = append(lines, history...)
					lines = append(lines, contentLine{fg: p.text, bg: p.canvas}, contentLine{fg: p.text, bg: p.canvas})
				}
				continue
			}
		}
		fg, bg, body := p.text, p.canvas, p.text
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
		// Reported thinking is context for the answer, not the answer: it keeps
		// one muted label and muted text instead of a per-message header.
		if a.Role == "thought" {
			fg, body, name = p.muted, p.muted, "Thinking"
			if a.Title != "" && a.Title != name {
				name += "  ·  " + a.Title
			}
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
		// A user message is a tinted box: its text uses the full transcript
		// column and renderTranscript extends the tint one cell beyond it on
		// each side, with one tinted padding row above and below.
		user := a.Role == "user"
		wrapWidth := w - 2
		if user {
			wrapWidth = w
		}
		bodyLines := strings.Split(ansi.Wrap(safe(a.Text), max(1, wrapWidth), ""), "\n")
		formatted := a.Role == "agent"
		if formatted {
			bodyLines = markdownLines(a.Text, max(1, wrapWidth), p)
		}
		if user {
			lines = append(lines, contentLine{fg: body, bg: bg, boxed: true})
		}
		for _, line := range bodyLines {
			lines = append(lines, contentLine{text: line, fg: body, bg: bg, action: act, boxed: user, styled: formatted})
		}
		if user {
			lines = append(lines, contentLine{fg: body, bg: bg, boxed: true})
		}
		lines = append(lines, contentLine{fg: p.text, bg: p.canvas}, contentLine{fg: p.text, bg: p.canvas})
	}
	return lines
}

// transcriptLines are the thread's activity rows followed by the reported
// outcome of its last turn.
func (m *Model) transcriptLines(t protocol.Thread, w int) []contentLine {
	groups := m.questionHistoryFallbackGroups(t, w)
	fallbackByTurn := make(map[string][]questionHistoryFallbackGroup)
	var earlierFallback []questionHistoryFallbackGroup
	for _, group := range groups {
		if group.TurnID == "" {
			earlierFallback = append(earlierFallback, group)
		} else {
			fallbackByTurn[group.TurnID] = append(fallbackByTurn[group.TurnID], group)
		}
	}

	lines := make([]contentLine, 0, len(t.Activity)*4)
	p := m.colors()
	appendSpacer := func() {
		lines = append(lines, contentLine{fg: p.text, bg: p.canvas}, contentLine{fg: p.text, bg: p.canvas})
	}
	appendHistory := func(group questionHistoryFallbackGroup) {
		if len(group.Lines) == 0 {
			return
		}
		lines = append(lines, group.Lines...)
		appendSpacer()
	}
	if len(earlierFallback) > 0 {
		lines = append(lines, contentLine{text: "Earlier history", fg: p.muted, bg: p.canvas})
		appendSpacer()
	}
	for _, group := range earlierFallback {
		appendHistory(group)
	}
	lastActivityByTurn := make(map[string]int)
	for i, activity := range t.Activity {
		if activity.TurnID != "" {
			lastActivityByTurn[activity.TurnID] = i
		}
	}
	for i, activity := range t.Activity {
		lines = append(lines, m.activityLinesForThread(t, []protocol.Activity{activity}, w)...)
		if activity.TurnID == "" || lastActivityByTurn[activity.TurnID] != i {
			continue
		}
		for _, group := range fallbackByTurn[activity.TurnID] {
			appendHistory(group)
		}
	}
	if activeTurn(t) {
		status := "Thinking…"
		markerFG := m.activityColor(activitySummary{Working: true})
		if t.State == "waiting" {
			status, markerFG = "Waiting…", p.gold
		} else if !m.connected {
			status, markerFG = "Connection lost", p.muted
		}
		marker := "●"
		if m.plainIcons {
			marker = "o"
		}
		lines = append(lines, contentLine{text: status, fg: p.text, bg: p.canvas, marker: marker, markerFG: markerFG})
	}
	note := stopReasonNote(t)
	if note == "" {
		return lines
	}
	for _, line := range strings.Split(ansi.Wrap(safe(note), max(1, w-2), ""), "\n") {
		lines = append(lines, contentLine{text: line, fg: p.gold, bg: p.canvas})
	}
	return lines
}

// stopReasonNote names a finished turn that ended for any reason other than a
// normal completion. The reason is agent-reported and shown once, at the end of
// that turn; an unfinished turn has no outcome yet.
func stopReasonNote(t protocol.Thread) string {
	if activeTurn(t) {
		return ""
	}
	switch t.StopReason {
	case "", "end_turn":
		return ""
	case "max_tokens":
		return "Turn ended · the agent reached its output token limit"
	case "max_turn_requests":
		return "Turn ended · the agent reached its request limit for this turn"
	case "refusal":
		return "Turn ended · the agent refused this request"
	case "cancelled":
		return "Turn ended · cancelled"
	}
	return "Turn ended · " + safe(t.StopReason)
}

func (m *Model) renderTranscript(f *frame, r shell.Rect) {
	p := m.colors()
	f.hits = append(f.hits, hit{Rect: r, Action: action{}, Label: "Transcript · wheel / arrows to scroll", Key: "transcript"})
	lines := m.transcriptLines(m.thread(), r.W)
	f.transcriptMax = max(0, len(lines)-r.H)
	offset := min(max(0, m.viewState().Scroll), f.transcriptMax)
	// Boxed rows are tinted one cell beyond the text column on each side (the
	// prompt outline's extent), bounded by the center pane.
	boxLeft, boxRight := r.X-1, r.X+r.W+1
	if c := f.geom.Center; c.W > 0 {
		boxLeft, boxRight = max(boxLeft, c.X), min(boxRight, c.X+c.W)
	}
	for i := 0; i < r.H && offset+i < len(lines); i++ {
		line := lines[offset+i]
		x, width := r.X, r.W
		if line.outset && boxRight > boxLeft {
			x, width = boxLeft, boxRight-boxLeft
		}
		if line.boxed && boxRight > boxLeft {
			f.fill(shell.Rect{X: boxLeft, Y: r.Y + i, W: boxRight - boxLeft, H: 1}, p, line.bg)
		}
		if line.border != "" && width >= 2 {
			m.renderOutlinedRow(f, x, r.Y+i, width, line, i)
		} else if line.marker != "" {
			f.text(x, r.Y+i, 1, line.marker, line.markerFG, line.bg)
			f.text(x+2, r.Y+i, width-2, line.text, line.fg, line.bg)
		} else if line.action.Kind != "" {
			key := "activity:" + line.action.ID + ":" + fmt.Sprint(i)
			if line.action.Kind == "question-history-toggle" {
				key = "question-history:" + line.action.ID
			}
			f.button(m, x, r.Y+i, width, line.text, key, line.action, line.fg, line.bg)
		} else if line.styled {
			// Markdown source is sanitized before parsing and decoded text is
			// sanitized again by its renderer. Preserve only that trusted SGR
			// here; f.text intentionally strips ANSI from all ordinary strings.
			f.put(shell.Rect{X: x, Y: r.Y + i, W: width, H: 1}, style(line.fg, line.bg).Render(fit(line.text, width)))
		} else {
			f.text(x, r.Y+i, width, line.text, line.fg, line.bg)
		}
	}
	// The scrollbar owns the gutter column right of the box extent, so it never
	// replaces a user box's tint or a card's border and those right edges stay
	// aligned with the prompt outline. Only a pane too narrow for that gutter
	// falls back to the last column inside it. Beside the right pane divider
	// the track is blank, so the two never read as a double rule.
	bar, track := boxRight, "│"
	if c := f.geom.Center; c.W > 0 && bar >= c.X+c.W {
		bar = c.X + c.W - 1
	}
	if d := f.geom.RightDivider; d.W > 0 && d.X == bar+1 {
		track = " "
	}
	f.scrollbarTrack(m, shell.Rect{X: bar, Y: r.Y, W: 1, H: r.H}, "transcript", len(lines), r.H, offset, p.canvas, track)
}

// renderOutlinedRow paints one interior row of an outlined transcript card:
// uniform outline ink on the canvas in both side cells, the stable interior
// fill between them. An action row keeps one blank padding cell before its
// label, where keyboard focus paints its mark.
func (m *Model) renderOutlinedRow(f *frame, x, y, width int, line contentLine, index int) {
	p := m.colors()
	tw := ansi.StringWidth(line.text)
	f.text(x, y, 1, ansi.Cut(line.text, 0, 1), line.border, p.canvas)
	f.text(x+width-1, y, 1, ansi.Cut(line.text, tw-1, tw), line.border, p.canvas)
	inner := ansi.Cut(line.text, 1, tw-1)
	if line.action.Kind == "" || width < 5 {
		f.text(x+1, y, width-2, inner, line.fg, line.bg)
		return
	}
	f.text(x+1, y, width-2, "", line.fg, line.bg)
	key := "activity:" + line.action.ID + ":" + fmt.Sprint(index)
	if line.action.Kind == "question-history-toggle" {
		key = "question-history:" + line.action.ID
	}
	f.button(m, x+2, y, width-4, strings.TrimSpace(inner), key, line.action, line.fg, line.bg)
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
	controlsHeight := m.composerControlsHeight(w)
	// Top border, one tinted padding row, typing rows, controls, bottom border.
	composerHeight := promptHeight + controlsHeight + 3
	promptStyle := m.componentStyle(roundedOutline, m.controlState(false, "prompt"), p.text, p.input)
	f.componentBox(m, shell.Rect{X: x, Y: y, W: w, H: composerHeight}, roundedOutline, promptStyle, p.canvas)
	inset := composerInset(w)
	f.prompt = shell.Rect{X: x + inset, Y: y + 2, W: max(1, w-2*inset), H: promptHeight}
	if f.rows != nil {
		f.put(f.prompt, style(p.text, p.input).Width(f.prompt.W).Height(f.prompt.H).Render(m.promptView.View(&m.prompt)))
	}
	f.hits = append(f.hits, hit{Rect: f.prompt, Action: action{}, Label: "Enter sends · Shift+Enter / Ctrl+J adds a line", Key: "prompt"})
	promptScroll := m.promptView.Metrics(m.promptMetrics)
	f.scrollbar(m, shell.Rect{X: f.prompt.X + f.prompt.W, Y: f.prompt.Y, W: 1, H: f.prompt.H}, "prompt", promptScroll.Total, f.prompt.H, promptScroll.Offset, p.input)
	m.renderComposerControls(f, shell.Rect{X: x, W: w}, f.prompt.Y+f.prompt.H)
	m.renderCheckoutContext(f, shell.Rect{X: x, Y: y + composerHeight, W: w, H: 1})
}

func (m *Model) renderSurface(f *frame, r shell.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	p := m.colors()
	v := m.viewState()
	f.fill(r, p, p.panel)
	x, y, w := r.X+1, r.Y+1, r.W-2
	// A short host drops its margins; nothing is painted or hit outside r.
	tight := r.H < 8
	if tight {
		y = r.Y
	}
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
		f.iconButton(m, x+w-8, y+tabRows/2, 4, " "+m.icon("more"), "tabs", action{Kind: "tabs"}, p.muted, p.panel)
		f.hits[len(f.hits)-1].Label = "Hidden tabs · open or close a surface"
	}
	f.iconButton(m, x+w-4, y+tabRows/2, 4, " "+m.icon("add"), "chooser", action{Kind: "chooser"}, p.blue, p.panel)
	f.hits[len(f.hits)-1].Label = "Add surface"
	f.detail = shell.Rect{X: x + 1, Y: y + tabRows + 2, W: max(1, w-2), H: max(0, r.H-tabRows-4)}
	if tight {
		f.detail.Y, f.detail.H = y+tabRows+1, max(0, r.H-tabRows-1)
	}
	if f.detail.H == 0 {
		f.detail = shell.Rect{}
		return
	}
	f.hits = append(f.hits, hit{Rect: f.detail, Action: action{}, Label: "Surface · wheel / arrows to scroll", Key: "right-body"})
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
			return "USAGE\n\n" + strings.Join(m.usageLines(), "\n") + "\n\nCapabilities\n" + strings.Join(m.snapshot.Capabilities, "\n") + "\n\n" + m.keyboard + "\n" + m.colorDiagnostics() + "\nGraphics: not probed; text fallback"
		}
		for _, a := range t.Activity {
			if v.DetailID != "" && a.ID != v.DetailID {
				continue
			}
			fmt.Fprintf(&b, "%s · %s\n%s\n\n%s\n\n", a.Title, a.State, a.Text, activityDetail(a))
		}
		for _, r := range t.Requests {
			if v.DetailID != "" && r.ID != v.DetailID {
				continue
			}
			delivery := requestDeliveryDescription(r.Delivery)
			fmt.Fprintf(&b, "%s\n%s\nState: %s\nDelivery: %s\nChoices: %s\n", r.Title, r.Detail, r.State, delivery, strings.Join(r.Choices, ", "))
			if r.Action != "" {
				fmt.Fprintf(&b, "Response: %s · no answer sent\n", questionActionOutcome(r.Action))
			}
			b.WriteString("\n")
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

// The bottom panel is a tab row of this thread's terminal sessions with an
// add control, like the right host, and no title row: each tab's icon slot
// ends its own session, and the panel toggle only hides the row.
func (m *Model) renderBottom(f *frame, r shell.Rect) {
	p := m.colors()
	f.fill(r, p, p.panel)
	v := m.viewState()
	x, w := r.X+1, r.W-2
	active, ok := v.Bottom.Active()
	visible, overflow := visibleTabs(v.Bottom.Tabs, active.ID, w-4)
	cx := x
	for _, slot := range visible {
		tab := slot.tab
		f.tab(m, cx, r.Y, slot.width, tab.Title, tab.Kind, "bottom-tab:"+tab.ID, "bottom-close:"+tab.ID,
			action{Kind: "bottom-tab", ID: tab.ID}, action{Kind: "bottom-close", ID: tab.ID}, tab.ID == active.ID)
		cx += slot.width + 1
	}
	if overflow {
		f.iconButton(m, x+w-8, r.Y, 4, " "+m.icon("more"), "bottom-tabs", action{Kind: "bottom-tabs"}, p.muted, p.panel)
		f.hits[len(f.hits)-1].Label = "Hidden terminals · select one"
	}
	f.iconButton(m, x+w-4, r.Y, 4, " "+m.icon("add"), "bottom-new", action{Kind: "bottom-new"}, p.blue, p.panel)
	f.hits[len(f.hits)-1].Label = "New terminal"
	if !ok {
		f.text(r.X+2, r.Y+2, max(1, r.W-4), "No terminal sessions · "+m.icon("add")+" opens one", p.muted, p.panel)
		return
	}
	f.bottomBody = shell.Rect{X: r.X + 2, Y: r.Y + 2, W: max(1, r.W-4), H: max(0, r.H-2)}
	lines := strings.Split(ansi.Wrap(safe(m.terminalText(active.ID)), f.bottomBody.W, ""), "\n")
	f.bottomMax = max(0, len(lines)-f.bottomBody.H)
	offset := min(max(0, m.viewState().BottomScroll), f.bottomMax)
	for i := 0; i < f.bottomBody.H && offset+i < len(lines); i++ {
		f.text(f.bottomBody.X, f.bottomBody.Y+i, f.bottomBody.W, lines[offset+i], p.text, p.panel)
	}
	f.hits = append(f.hits, hit{Rect: f.bottomBody, Action: action{}, Label: "Terminal output · wheel / arrows to scroll", Key: "bottom-body"})
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
	titleInk := p.violet
	if m.menuTitle == "Cannot send message" {
		titleInk = p.red
	}
	f.text(r.X+2, r.Y+1, w-7, m.menuTitle, titleInk, p.input)
	f.iconButton(m, r.X+w-4, r.Y+1, 3, centered(m.icon("close"), 3), "menu-close", action{Kind: "menu-close"}, p.muted, p.input)
	if extra > 0 {
		input := shell.Rect{X: r.X + 2, Y: r.Y + 2, W: w - 4, H: 1}
		if f.rows != nil {
			f.put(input, m.projectInput.View())
		}
		f.hits = append(f.hits, hit{Rect: input, Action: action{}, Label: "Project search / folder path", Key: "project-input"})
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
				m.renderProjectBadge(f, r.X+2, y, project, p.input)
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

// View composes the frame and overlays the live text selection.
func (m *Model) View() tea.View {
	f := m.render()
	if m.selectionLive(f) && len(m.menu) == 0 {
		for _, s := range selectionSpans(m.selectionStart, m.selectionEnd, m.selectionRegion, m.width, len(f.rows)) {
			highlight := style(m.colors().text, m.colors().selected).Render(ansi.Strip(cutCells(f.rows[s.y], s.start, s.end)))
			f.put(shell.Rect{X: s.start, Y: s.y, W: s.end - s.start, H: 1}, highlight)
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

// activityDetail shows a dispatched prompt's retained capture; Detail holds
// only its summary.
func activityDetail(a protocol.Activity) string {
	if a.Prompt == nil || len(a.Prompt.Attachments) == 0 {
		return a.Detail
	}
	var b strings.Builder
	b.WriteString(a.Detail)
	for _, at := range a.Prompt.Attachments {
		name := at.Name
		if at.Source != "" && at.Source != name {
			name += " · " + at.Source
		}
		fmt.Fprintf(&b, "\n\n%s · %s · %d bytes captured\n%s", at.Kind, name, len(at.Content), at.Content)
	}
	return b.String()
}
