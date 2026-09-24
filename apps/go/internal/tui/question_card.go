package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// The card includes both borders, the interior header, tabs and fixed actions.
const maxQuestionCardRows = 13

// Widths of the fixed compact actions on the card's last interior row.
const (
	questionSubmitWidth  = 10
	questionOptionsWidth = 12
	questionDeclineWidth = 11
	questionCancelWidth  = 10
	questionMoreWidth    = 9
)

// questionOption is one selectable answer of the active question, with Other
// last. Rows, focus traversal, digit keys and the Options… menu all use it.
// description is the supplied detail painted under the label, never in menus.
type questionOption struct {
	key, label, description string
	action                  action
	selected                bool
}

// questionLine is one painted body row: question text, one wrapped row of an
// option label, or one wrapped row of its muted description. glyph is set on
// an option's first row only; extent is the option label block's width from
// its glyph cell to its longest label row. Description rows carry no key: they
// are not part of the option's hover, focus or hit target.
type questionLine struct {
	text, key, glyph       string
	action                 action
	selected, bold, detail bool
	indent, extent         int
}

// requestLayout is the single measurement of a request card, shared by the
// footer height and by painting/hit testing. Rows are absolute; x and w are
// the content column, which ends before the body scrollbar column.
type requestLayout struct {
	card                        shell.Rect
	x, w                        int
	header, tabs, body, input   shell.Rect
	noticeY, actionsY           int
	noticeInline, optionsHidden bool
	lines                       []questionLine
	offset                      int
}

// questionIndex clamps a saved or stale page index to the request's questions.
func (m *Model) questionIndex(r protocol.Request) int {
	return min(max(0, m.viewState().QuestionIndex), max(0, len(r.Questions)-1))
}

func (m *Model) activeQuestion(r protocol.Request) (protocol.Question, int, bool) {
	if r.Kind == "approval" || len(r.Questions) == 0 {
		return protocol.Question{}, 0, false
	}
	i := m.questionIndex(r)
	return r.Questions[i], i, true
}

func (m *Model) questionMarker(kind string, selected bool) string {
	key := "radio"
	if kind == "multiple" {
		key = "checkbox"
	}
	if selected {
		key += "-on"
	}
	return m.icon(key)
}

func (m *Model) questionOptions(r protocol.Request) []questionOption {
	q, i, ok := m.activeQuestion(r)
	if !ok || protocol.QuestionKind(q) == "text" {
		return nil
	}
	d := m.questionDraft(r, i)
	// Malformed descriptions (not parallel to Options) are ignored, not guessed.
	described := protocol.ValidateQuestion(q) == nil && len(q.OptionDescriptions) == len(q.Options)
	options := make([]questionOption, 0, len(q.Options)+1)
	for j, option := range q.Options {
		description := ""
		if described {
			description = q.OptionDescriptions[j]
		}
		options = append(options, questionOption{fmt.Sprint("option:", j), option, description, action{Kind: "answer-choice", ID: r.ID, Value: option, Revision: r.Revision}, slices.Contains(d.Choices, option)})
	}
	if protocol.QuestionAllowsOther(q) {
		options = append(options, questionOption{"answer-other", "Other…", "", action{Kind: "answer-other", ID: r.ID, Revision: r.Revision}, d.Other})
	}
	return options
}

func questionOptionIndex(options []questionOption, key string) int {
	return slices.IndexFunc(options, func(o questionOption) bool { return o.key == key })
}

// questionLines wraps the question text and each option to the content width.
// Option labels hang under their first row, after the glyph and one gap.
func (m *Model) questionLines(r protocol.Request, width int) []questionLine {
	width = max(1, width)
	var lines []questionLine
	appendText := func(text string, bold bool) {
		for _, row := range strings.Split(ansi.Wrap(safe(text), width, ""), "\n") {
			lines = append(lines, questionLine{text: row, bold: bold})
		}
	}
	if r.Kind == "approval" {
		appendText(r.Title, true)
		if r.Detail != "" {
			appendText(r.Detail, false)
		}
		return lines
	}
	q, _, ok := m.activeQuestion(r)
	if !ok {
		appendText(r.Title, true)
		return lines
	}
	appendText(q.Text, true)
	for _, o := range m.questionOptions(r) {
		glyph := m.questionMarker(protocol.QuestionKind(q), o.selected)
		indent := ansi.StringWidth(glyph) + 1
		rows := strings.Split(ansi.Wrap(safe(o.label), max(1, width-indent), ""), "\n")
		extent := 0
		for _, row := range rows {
			extent = max(extent, indent+ansi.StringWidth(row))
		}
		for i, row := range rows {
			line := questionLine{text: row, key: o.key, action: o.action, selected: o.selected, indent: indent, extent: min(width, extent)}
			if i == 0 {
				line.glyph = glyph
			}
			lines = append(lines, line)
		}
		// The description hangs under the label column, muted and wrapped;
		// it scrolls with the body and counts in the card's row budget.
		if detail := strings.TrimSpace(o.description); detail != "" {
			for _, row := range strings.Split(ansi.Wrap(safe(detail), max(1, width-indent), ""), "\n") {
				lines = append(lines, questionLine{text: row, detail: true, indent: indent})
			}
		}
	}
	return lines
}

// The answer field is indented under Other's label, or under the question
// text for open-ended questions.
func (m *Model) questionInputIndent(r protocol.Request) int {
	q, _, ok := m.activeQuestion(r)
	if !ok || protocol.QuestionKind(q) == "text" {
		return 0
	}
	return ansi.StringWidth(m.questionMarker(protocol.QuestionKind(q), true)) + 1
}

func (m *Model) questionInputRows(r protocol.Request) int {
	q, i, ok := m.activeQuestion(r)
	if !ok {
		return 0
	}
	if protocol.QuestionKind(q) == "text" || m.questionDraft(r, i).Other {
		return min(3, max(1, m.answerMetrics.Total))
	}
	return 0
}

// submitBlocked reports that Submit cannot currently send; the card renders
// it disabled and requestCardNotice names the reason.
func (m *Model) submitBlocked() bool {
	return m.thread().NeedsResume || !m.connected || m.answerInFlight()
}

// answerInFlight is a request answer being delivered; other commands do not
// disable Submit (the command path reports its own pending state).
func (m *Model) answerInFlight() bool {
	return m.inFlight && m.busy != nil && m.busy.Kind == "request.answer"
}

// requestCardNotice is the card's feedback: the request's own progress,
// validation or server result first, then why a question's Submit is blocked.
func (m *Model) requestCardNotice(r protocol.Request) (string, bool) {
	if message, problem := m.requestNotice(r); message != "" || r.Kind == "approval" {
		return message, problem
	}
	if !m.connected {
		return "Disconnected · wait for the server to reconnect.", true
	}
	if m.answerInFlight() {
		return "Submitting another answer…", false
	}
	return "", false
}

func questionTabsShown(r protocol.Request) bool {
	return r.Kind != "approval" && len(r.Questions) > 1
}

// requestLayout measures the card for req. A zero r.H chooses the height: the
// content viewport shrinks first at short heights and always keeps one row.
func (m *Model) requestLayout(req protocol.Request, r shell.Rect) requestLayout {
	l := requestLayout{card: r, x: r.X + 2, w: max(1, r.W-4), noticeY: -1}
	l.lines = m.questionLines(req, l.w)
	tabs := 0
	if questionTabsShown(req) {
		tabs = 1
	}
	message, _ := m.requestCardNotice(req)
	// A short notice shares the actions row, between Options… and the right
	// action group; approvals and longer notices take their own row above the
	// actions. The group is measured as if Options… were shown, so the choice
	// does not depend on the scroll position.
	right := questionActionsWidth(m.questionActionsPlan(req, l.w-questionOptionsWidth-1))
	l.noticeInline = message != "" && req.Kind != "approval" && ansi.StringWidth(message) <= l.w-right-questionOptionsWidth-2
	notice := 0
	if message != "" && !l.noticeInline {
		notice = 1
	}
	fixed := 4 + tabs + notice // Borders, header, actions.
	input := m.questionInputRows(req)
	if r.H <= 0 {
		desired := fixed + input + max(1, len(l.lines))
		floor := fixed + 1 + min(input, 1)
		budget := max(floor, m.height-3-m.baseFooterHeight(r.W))
		if m.surfaceFillsCenter() {
			// The surface gets the rows instead; the card scrolls within ~40% of
			// the body and keeps its decision controls.
			budget = min(budget, max(floor, (m.height-2)*2/5))
		}
		l.card.H = min(desired, maxQuestionCardRows, budget)
	}
	y := l.card.Y + 1
	l.header = shell.Rect{X: l.x, Y: y, W: l.w, H: 1}
	y++
	if tabs > 0 {
		l.tabs = shell.Rect{X: l.x, Y: y, W: l.w, H: 1}
		y++
	}
	l.actionsY = l.card.Y + l.card.H - 2
	end := l.actionsY
	if notice > 0 {
		l.noticeY = end - 1
		end--
	}
	inputRows := min(input, max(0, end-y-1))
	l.body = shell.Rect{X: l.x, Y: y, W: l.w, H: max(1, end-y-inputRows)}
	if inputRows > 0 {
		ix := l.x + m.questionInputIndent(req)
		// The field ends before the shared scrollbar column.
		l.input = shell.Rect{X: ix, Y: l.body.Y + l.body.H, W: max(1, l.card.X+l.card.W-2-ix), H: inputRows}
	}
	maxOffset := max(0, len(l.lines)-l.body.H)
	l.offset = min(max(0, m.viewState().RequestScroll), maxOffset)
	for i, line := range l.lines {
		if line.key != "" && (i < l.offset || i >= l.offset+l.body.H) {
			l.optionsHidden = true
		}
	}
	return l
}

func (m *Model) requestHeight(width int) int {
	if !m.conversationVisible() {
		return 0
	}
	r, ok := m.request()
	if !ok {
		return 0
	}
	return m.requestLayout(r, shell.Rect{W: width}).card.H
}

func (m *Model) requestMode(req protocol.Request) (string, bool) {
	switch {
	case m.thread().NeedsResume:
		return "Awaiting Resume", true
	case req.Kind == "approval":
		return "Approval required", true
	case req.Mode == "blocking":
		return "Waiting for answer", true
	case req.Mode == "async":
		return "Answer anytime", false
	}
	return "Question", false
}

func (m *Model) renderRequest(f *frame, r shell.Rect, req protocol.Request) int {
	p := m.colors()
	f.request = r
	l := m.requestLayout(req, r)
	// One uniform frame: the prompt's rest outline, or its focused outline
	// while keyboard focus is inside the card. Inner hover never recolors it.
	f.componentBox(m, r, roundedOutline, m.containerStyle(requestControlKey(m.focus), p.text, p.panel), p.canvas)
	m.renderRequestHeader(f, l, req)
	if l.tabs.H > 0 {
		m.renderQuestionTabs(f, l.tabs, req)
	}
	f.requestMax = max(0, len(l.lines)-l.body.H)
	f.hits = append(f.hits, hit{Rect: l.body, Action: action{}, Label: "Question · arrows / wheel to scroll", Key: "request-body"})
	for i := l.offset; i < len(l.lines) && i < l.offset+l.body.H; i++ {
		m.renderQuestionLine(f, l, l.lines[i], l.body.Y+i-l.offset)
	}
	scrollX := r.X + r.W - 2
	f.scrollbar(m, shell.Rect{X: scrollX, Y: l.body.Y, W: 1, H: l.body.H}, "request", len(l.lines), l.body.H, l.offset, p.panel)
	if l.input.H > 0 {
		f.answer = l.input
		if f.rows != nil {
			f.put(f.answer, style(p.text, p.input).Width(f.answer.W).Height(f.answer.H).Render(m.answerView.View(&m.answer)))
		}
		f.hits = append(f.hits, hit{Rect: f.answer, Action: action{}, Label: "Answer · Enter next/submit · Shift+Enter newline · Esc back", Key: "answer"})
		scroll := m.answerView.Metrics(m.answerMetrics)
		f.scrollbar(m, shell.Rect{X: scrollX, Y: l.input.Y, W: 1, H: l.input.H}, "answer", scroll.Total, l.input.H, scroll.Offset, p.panel)
	}
	m.renderRequestActions(f, l, req)
	return r.Y + r.H
}

// The header is a text control without fill inside the outline: hover and
// focus embolden and lift it, and focus marks the gutter cell before it.
func (m *Model) renderRequestHeader(f *frame, l requestLayout, req protocol.Request) {
	p := m.colors()
	y, right := l.header.Y, 0
	if pending := m.requests(); len(pending) > 1 {
		label := fmt.Sprint("Requests ", len(pending))
		width := min(l.w/2, ansi.StringWidth(label)+4)
		f.compactButton(m, l.x+l.w-width, y, width, label, "request-select", action{Kind: "request-select"}, false, normalControl)
		right = width + 1
	} else if l.tabs.H > 0 && m.questionTabPlan(req, l.tabs).overflow {
		counter := fmt.Sprintf("%d of %d", m.questionIndex(req)+1, len(req.Questions))
		cw := ansi.StringWidth(counter)
		if cw < l.w/2 {
			f.text(l.x+l.w-cw, y, cw, counter, p.muted, p.panel)
			right = cw + 1
		}
	}
	icon := m.icon("question")
	if req.Kind == "approval" {
		icon = m.icon("approval")
	}
	mode, blocking := m.requestMode(req)
	iconInk, modeInk := p.blue, p.muted
	if blocking {
		iconInk, modeInk = p.gold, p.gold
	}
	segments := []struct{ text, fg string }{{icon + " ", iconInk}}
	if origin := m.requestOriginLabel(req); origin != "" {
		segments = append(segments, struct{ text, fg string }{origin, p.text}, struct{ text, fg string }{" · ", p.muted})
	}
	segments = append(segments, struct{ text, fg string }{mode, modeInk})
	s := m.controlState(false, "request-detail")
	avail, x := max(1, l.w-right), l.x
	for _, seg := range segments {
		if x >= l.x+avail {
			break
		}
		text := seg.text
		if rest := l.x + avail - x; ansi.StringWidth(text) > rest {
			text = ansi.Truncate(text, rest, "…")
		}
		w := ansi.StringWidth(text)
		f.styledText(x, y, w, text, m.iconStyle(s, seg.fg, p.panel), false)
		x += w
	}
	if s.Focused {
		f.focusMark(l.x-1, y, m.iconStyle(s, p.text, p.panel), p.panel)
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: l.x, Y: y, W: max(1, x-l.x), H: 1}, Action: action{Kind: "request-detail"}, Label: "Request details · " + mode, Key: "request-detail"})
}

// Option rows keep the gutter cell for the focus mark, then glyph and label.
// Hover and focus fill only the glyph..label extent; selection is accent ink
// and bold, kept under hover.
func (m *Model) renderQuestionLine(f *frame, l requestLayout, line questionLine, y int) {
	p := m.colors()
	if line.detail {
		if w := l.w - line.indent; w > 0 {
			f.styledText(l.x+line.indent, y, w, line.text, componentVisual{foreground: p.muted, background: p.panel}, false)
		}
		return
	}
	if line.key == "" {
		f.styledText(l.x, y, l.w, line.text, componentVisual{foreground: p.text, background: p.panel, bold: line.bold}, false)
		return
	}
	s := m.controlState(line.selected, line.key)
	bg, glyphInk, labelInk := p.panel, p.muted, p.text
	if s.Hovered || s.Focused {
		bg, glyphInk = m.hoverFill(), p.text
	}
	if s.Selected {
		glyphInk = p.blue
	}
	extent := max(1, line.extent)
	f.fill(shell.Rect{X: l.x, Y: y, W: extent, H: 1}, p, bg)
	indent := line.indent
	if line.glyph != "" {
		f.styledText(l.x, y, ansi.StringWidth(line.glyph), line.glyph, componentVisual{foreground: glyphInk, background: bg, bold: s.Selected}, false)
	}
	if lw := extent - indent; lw > 0 {
		f.styledText(l.x+indent, y, lw, line.text, componentVisual{foreground: labelInk, background: bg, bold: s.Selected}, false)
	}
	if s.Focused {
		f.focusMark(l.x-1, y, componentVisual{mark: m.icon("focus"), markInk: p.blue}, p.panel)
	}
	label := line.text
	if line.glyph != "" {
		label = line.glyph + " " + line.text
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: l.x, Y: y, W: extent, H: 1}, Action: line.action, Label: label, Key: line.key})
}

// questionButton is a compact action with explicit state, so Submit can be
// disabled: muted, without hover fill or primary ink, but still focusable.
func (f *frame) questionButton(m *Model, x, y, width int, label, key string, a action, role componentRole, disabled bool) {
	if width < 3 {
		return
	}
	p := m.colors()
	s := m.controlState(false, key)
	s.Role, s.Disabled = role, disabled
	v := m.componentStyle(squareFill, s, p.text, p.input)
	v.focused = s.Focused
	f.compactControl(m, x, y, width, centered(ansi.Truncate(label, width-2, "…"), width-2), v)
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: a, Label: label, Key: key})
}

func (m *Model) renderRequestActions(f *frame, l requestLayout, req protocol.Request) {
	p := m.colors()
	x, y, w := l.x, l.actionsY, l.w
	message, problem := m.requestCardNotice(req)
	fg := p.blue
	if problem {
		fg = p.red
	}
	if message != "" && l.noticeY >= 0 {
		f.text(x, l.noticeY, w, message, fg, p.panel)
	}
	if req.Kind == "approval" {
		total := max(0, len(req.Choices)-1)
		for _, choice := range req.Choices {
			total += ansi.StringWidth(choice) + 4
		}
		if total > w {
			f.compactButton(m, x, y, w, "Approval choices…", "approval-options", action{Kind: "approval-options"}, false, normalControl)
			return
		}
		cx := x
		for i, choice := range req.Choices {
			size := ansi.StringWidth(choice) + 4
			f.compactButton(m, cx, y, size, choice, fmt.Sprint("approve:", i), m.approvalAction(req, i), false, normalControl)
			cx += size + 1
		}
		return
	}
	left := x
	if l.optionsHidden {
		size := min(questionOptionsWidth, w-questionSubmitWidth-1)
		f.compactButton(m, x, y, size, "Options…", "answer-options", action{Kind: "answer-options"}, false, normalControl)
		left = x + size + 1
	}
	buttons := m.questionActionsPlan(req, x+w-left)
	groupX := x + w - questionActionsWidth(buttons)
	if message != "" && l.noticeInline {
		// Right-aligned against the action group, truncated to the space between.
		room := max(0, groupX-1-left)
		text := ansi.Truncate(message, room, "…")
		tw := ansi.StringWidth(text)
		f.text(groupX-1-tw, y, tw, text, fg, p.panel)
	}
	// Decline, Cancel and the More… overflow share Submit's disabled rules;
	// they never touch the answer drafts.
	bx := groupX
	for _, b := range buttons {
		f.questionButton(m, bx, y, b.width, b.label, b.key, b.action, b.role, m.submitBlocked())
		bx += b.width + 1
	}
}

// questionActionButton is one control of the right-aligned action group.
type questionActionButton struct {
	label, key string
	action     action
	role       componentRole
	width      int
}

// questionActionsPlan lays out the right-aligned action group within room
// cells, left to right, ending with Submit. A request's offered Decline sits
// directly left of Submit; an offered Cancel sits left of Decline while the
// row has room, and otherwise moves, with Decline if even that cannot fit,
// into a More… menu. Submit is never displaced.
func (m *Model) questionActionsPlan(req protocol.Request, room int) []questionActionButton {
	submit := questionActionButton{"Submit", "answer-submit", action{Kind: "answer-submit"}, primaryControl, questionSubmitWidth}
	offered := questionOfferedActions(req)
	if len(offered) == 0 {
		return []questionActionButton{submit}
	}
	button := func(name string) questionActionButton {
		width := questionDeclineWidth
		if name == protocol.RequestActionCancel {
			width = questionCancelWidth
		}
		return questionActionButton{questionActionLabel(name), "answer-" + name, action{Kind: "answer-action", ID: req.ID, Revision: req.Revision, Value: name}, normalControl, width}
	}
	more := questionActionButton{"More…", "answer-actions", action{Kind: "answer-actions"}, normalControl, questionMoreWidth}
	// Cancel first (leftmost), then Decline, then Submit.
	all := []questionActionButton{}
	for _, name := range []string{protocol.RequestActionCancel, protocol.RequestActionDecline} {
		if slices.Contains(offered, name) {
			all = append(all, button(name))
		}
	}
	candidates := [][]questionActionButton{append(append([]questionActionButton{}, all...), submit)}
	if slices.Contains(offered, protocol.RequestActionDecline) && len(all) > 1 {
		candidates = append(candidates, []questionActionButton{more, button(protocol.RequestActionDecline), submit})
	}
	candidates = append(candidates, []questionActionButton{more, submit})
	for _, plan := range candidates {
		if questionActionsWidth(plan) <= room {
			return plan
		}
	}
	return []questionActionButton{submit}
}

func questionActionsWidth(buttons []questionActionButton) int {
	width := max(0, len(buttons)-1)
	for _, b := range buttons {
		width += b.width
	}
	return width
}

// questionOfferedActions returns the request's supported non-answer
// responses in a stable order; anything unknown is ignored.
func questionOfferedActions(req protocol.Request) []string {
	if req.Kind != "question" || protocol.ValidateRequestActions(req) != nil {
		return nil
	}
	var offered []string
	for _, name := range []string{protocol.RequestActionDecline, protocol.RequestActionCancel} {
		if slices.Contains(req.Actions, name) {
			offered = append(offered, name)
		}
	}
	return offered
}

// questionActionOutcome is the recorded outcome's name in history and detail.
func questionActionOutcome(name string) string {
	if name == protocol.RequestActionCancel {
		return "Cancelled"
	}
	return "Declined"
}

func questionActionLabel(name string) string {
	if name == protocol.RequestActionCancel {
		return "Cancel"
	}
	return "Decline"
}

func questionTabLabel(q protocol.Question, index int) string {
	if q.Label == "" {
		return fmt.Sprint("Question ", index+1)
	}
	return fmt.Sprint(index+1, " ", q.Label)
}

// Arrow focus traverses the complete choice list, including choices offscreen.
func (m *Model) focusQuestionOption(index int) {
	r, ok := m.request()
	if !ok {
		return
	}
	options := m.questionOptions(r)
	if len(options) == 0 {
		m.setFocus("request-body")
		return
	}
	key := options[min(max(0, index), len(options)-1)].key
	f := m.measure()
	l := m.requestLayout(r, f.request)
	for i, line := range l.lines {
		if line.key != key {
			continue
		}
		offset := m.viewState().RequestScroll
		if i < offset {
			offset = i
		} else if i >= offset+l.body.H {
			offset = i - l.body.H + 1
		}
		m.scrollTo("request", offset, f)
		break
	}
	m.setFocus(key)
}

// focusQuestionTop moves focus above the options: the active tab, or the
// header when a request has a single question.
func (m *Model) focusQuestionTop(r protocol.Request) {
	if questionTabsShown(r) {
		m.setFocus(fmt.Sprint("question-page:", m.questionIndex(r)))
		return
	}
	m.setFocus("request-detail")
}

// questionNavigationFocus reports focus on a card control that only navigates:
// the header, Requests selector, tabs and arrows. Navigation keeps it there.
func questionNavigationFocus(key string) bool {
	return key == "request-detail" || key == "request-select" || key == "question-back" || key == "question-next" || key == "question-tabs" || strings.HasPrefix(key, "question-page:")
}

// A request owns its navigation, answer field and actions, not the prompt below.
// Answered history cards in the transcript are not part of it.
func requestControlKey(key string) bool {
	if strings.HasPrefix(key, "question-history") {
		return false
	}
	for _, prefix := range []string{"request-", "question-", "answer", "option:", "approve:", "approval-"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return key == "scrollbar-request" || key == "scrollbar-answer"
}

// submitFocusedRequest is Ctrl+S inside the card: it answers a question through
// the validated Submit path. An approval needs an explicit choice.
func (m *Model) submitFocusedRequest() tea.Cmd {
	r, ok := m.request()
	if !ok {
		return nil
	}
	if r.Kind == "approval" {
		return m.showNotice("Choose an approval option · nothing sent")
	}
	return m.activate(action{Kind: "answer-submit"})
}

// answerFieldKey handles the answer field's own keys before global bindings:
// Enter moves to the next question or submits, Esc returns to the question's
// controls without clearing the draft, and Up/Down leave the field at its
// first/last row. Shift+Enter and Ctrl+J still insert newlines.
func (m *Model) answerFieldKey(s string) (bool, tea.Cmd) {
	r, ok := m.request()
	if !ok || r.Kind == "approval" {
		return false, nil
	}
	i := m.questionIndex(r)
	switch s {
	case "enter":
		if i+1 < len(r.Questions) {
			m.selectQuestion(i + 1)
			return true, nil
		}
		return true, m.activate(action{Kind: "answer-submit"})
	case "esc":
		if options := m.questionOptions(r); len(options) > 0 && options[len(options)-1].key == "answer-other" {
			m.focusQuestionOption(len(options) - 1)
		} else {
			m.focusQuestionTop(r)
		}
		return true, nil
	case "up":
		if m.answer.Line() == 0 && m.answer.LineInfo().RowOffset == 0 {
			if options := m.questionOptions(r); len(options) > 0 {
				m.focusQuestionOption(len(options) - 1)
			} else {
				m.focusQuestionTop(r)
			}
			return true, nil
		}
	case "down":
		info := m.answer.LineInfo()
		if m.answer.Line() == m.answer.LineCount()-1 && info.RowOffset >= info.Height-1 {
			m.setFocus("answer-submit")
			return true, nil
		}
	}
	return false, nil
}

// questionDigitFocus: digits choose options from the card's option rows, tabs
// and header, never from a text field or the composer.
func questionDigitFocus(key string) bool {
	return strings.HasPrefix(key, "option:") || key == "answer-other" || questionNavigationFocus(key) && key != "request-select"
}

// questionCardKey moves keyboard focus through a question card: Up/Down walk
// the header or tab, options, the answer field and Submit; digits 1–9 choose.
func (m *Model) questionCardKey(s string) (bool, tea.Cmd) {
	r, ok := m.request()
	if !ok || r.Kind == "approval" || !requestControlKey(m.focus) {
		return false, nil
	}
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && questionDigitFocus(m.focus) {
		m.chooseQuestionOption(int(s[0] - '1'))
		return true, nil
	}
	if s != "up" && s != "down" {
		return false, nil
	}
	options := m.questionOptions(r)
	answerVisible := m.measure().answer.W > 0
	down := s == "down"
	if i := questionOptionIndex(options, m.focus); i >= 0 {
		switch {
		case !down && i == 0:
			m.focusQuestionTop(r)
		case down && i == len(options)-1 && answerVisible:
			m.setFocus("answer")
		case down && i == len(options)-1:
			m.setFocus("answer-submit")
		case down:
			m.focusQuestionOption(i + 1)
		default:
			m.focusQuestionOption(i - 1)
		}
		return true, nil
	}
	if down && (m.focus == "request-detail" || strings.HasPrefix(m.focus, "question-page:") || m.focus == "question-back" || m.focus == "question-next") {
		if len(options) > 0 {
			m.focusQuestionOption(0)
		} else if answerVisible {
			m.setFocus("answer")
		} else {
			return false, nil
		}
		return true, nil
	}
	if !down && (m.focus == "answer-submit" || m.focus == "answer-decline" || m.focus == "answer-cancel" || m.focus == "answer-actions") {
		if answerVisible {
			m.setFocus("answer")
		} else if len(options) > 0 {
			m.focusQuestionOption(len(options) - 1)
		} else {
			return false, nil
		}
		return true, nil
	}
	if !down && strings.HasPrefix(m.focus, "question-page:") {
		m.setFocus("request-detail")
		return true, nil
	}
	return false, nil
}

// requestOriginLabel names a request's origin only when it is not the
// thread's own agent: a child run or another named source. The thread has one
// chosen agent, so repeating "Claude" or "Codex" on every card says nothing
// new; the raw origin stays in the request detail and Activity.
func (m *Model) requestOriginLabel(req protocol.Request) string {
	origin := strings.TrimSpace(safe(req.Origin))
	if origin == "" || strings.EqualFold(origin, "agent") {
		return ""
	}
	label := agentDisplayName(origin)
	if t := m.thread(); strings.EqualFold(origin, t.Agent) || strings.EqualFold(origin, t.AgentID) || strings.EqualFold(label, agentDisplayName(t.Agent)) {
		return ""
	}
	return label
}
