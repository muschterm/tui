package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

const maxQuestionCardRows = 12

type questionLine struct {
	text, key string
	action    action
	selected  bool
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

func (m *Model) questionLines(r protocol.Request, width int) []questionLine {
	width = max(1, width)
	text := r.Title
	var q protocol.Question
	if r.Kind == "approval" {
		text += "\n" + r.Detail
	} else if len(r.Questions) > 0 {
		q = r.Questions[min(m.viewState().QuestionIndex, len(r.Questions)-1)]
		text = q.Text
	}
	var lines []questionLine
	for _, text := range strings.Split(ansi.Wrap(safe(text), width, ""), "\n") {
		lines = append(lines, questionLine{text: text})
	}
	if r.Kind == "approval" {
		return lines
	}
	d := m.questionDraft(r, m.viewState().QuestionIndex)
	add := func(label, key string, a action, selected bool) {
		marker := m.questionMarker(protocol.QuestionKind(q), selected)
		prefix := marker + " "
		for i, line := range strings.Split(ansi.Wrap(safe(label), max(1, width-ansi.StringWidth(prefix)), ""), "\n") {
			start := prefix
			if i > 0 {
				start = strings.Repeat(" ", ansi.StringWidth(prefix))
			}
			lines = append(lines, questionLine{start + line, key, a, selected})
		}
	}
	if protocol.QuestionKind(q) != "text" {
		for i, option := range q.Options {
			add(option, fmt.Sprint("option:", i), action{Kind: "answer-choice", Value: option}, slices.Contains(d.Choices, option))
		}
		if protocol.QuestionAllowsOther(q) {
			add("Other…", "answer-other", action{Kind: "answer-other"}, d.Other)
		}
	}
	return lines
}

func (m *Model) questionInputRows(r protocol.Request) int {
	if r.Kind == "approval" || len(r.Questions) == 0 {
		return 0
	}
	i := m.viewState().QuestionIndex
	if protocol.QuestionKind(r.Questions[i]) == "text" || m.questionDraft(r, i).Other {
		return min(3, max(1, m.answerMetrics.Total))
	}
	return 0
}

func (m *Model) requestHeight(width int) int {
	if !m.conversationVisible() {
		return 0
	}
	r, ok := m.request()
	if !ok {
		return 0
	}
	// Border/title, tabs, content, optional text input, actions, bottom border.
	fixed := 3
	if r.Kind != "approval" && len(r.Questions) > 1 {
		fixed++
	}
	fixed += m.requestNoticeRows(r)
	input := m.questionInputRows(r)
	desired := fixed + input + max(1, len(m.questionLines(r, width-4)))
	// Keep the composer and its controls intact. At short heights the content
	// viewport shrinks first; it always retains at least one scrollable row.
	floor := fixed + 1 + min(input, 1)
	budget := max(floor, m.height-3-m.baseFooterHeight(width))
	if m.surfaceFillsCenter() {
		// The surface gets the rows instead; the card scrolls within ~40% of
		// the body and keeps its decision controls.
		budget = min(budget, max(floor, (m.height-2)*2/5))
	}
	return min(desired, maxQuestionCardRows, budget)
}

func (m *Model) renderRequest(f *frame, r shell.Rect, req protocol.Request) int {
	p := m.colors()
	f.request = r
	state := componentState{Hovered: requestControlKey(m.hover), Focused: requestControlKey(m.focus)}
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, state, p.text, p.panel), p.canvas)
	x, y, w := r.X+2, r.Y, max(1, r.W-4)
	mode := "Question"
	if req.Mode == "async" {
		mode = "Answer anytime"
	} else if req.Mode == "blocking" {
		mode = "Waiting for answer"
	}
	if req.Kind == "approval" {
		mode = "Approval required"
	}
	if m.thread().NeedsResume {
		mode = "Awaiting Resume"
	}
	selectorWidth := 0
	if len(m.requests()) > 1 {
		selectorWidth = min(w/2, ansi.StringWidth(fmt.Sprint("Requests ", len(m.requests())))+4)
	}
	f.button(m, x, y, w-selectorWidth, " "+mode+" · "+agentDisplayName(req.Origin)+" ", "request-detail", action{Kind: "request-detail"}, p.gold, p.panel)
	if selectorWidth > 0 {
		f.compactButton(m, x+w-selectorWidth, y, selectorWidth, fmt.Sprint("Requests ", len(m.requests())), "request-select", action{Kind: "request-select"}, false, normalControl)
	}
	y++
	controlRows := 1
	if req.Kind != "approval" && len(req.Questions) > 1 {
		m.renderQuestionTabs(f, shell.Rect{X: x, Y: y, W: w, H: controlRows}, req)
		y += controlRows
	}
	noticeRows := m.requestNoticeRows(req)
	actionsY := r.Y + r.H - 1 - controlRows
	inputRows := min(m.questionInputRows(req), max(0, actionsY-y-noticeRows-1))
	body := shell.Rect{X: x, Y: y, W: w, H: max(1, actionsY-y-inputRows-noticeRows)}
	lines := m.questionLines(req, body.W)
	f.requestMax = max(0, len(lines)-body.H)
	offset := min(max(0, m.viewState().RequestScroll), f.requestMax)
	f.hits = append(f.hits, hit{Rect: body, Action: action{}, Label: "Question · arrows / wheel to scroll", Key: "request-body"})
	optionsHidden := false
	for i, line := range lines {
		if i < offset || i >= offset+body.H {
			optionsHidden = optionsHidden || line.action.Kind != ""
			continue
		}
		if line.action.Kind != "" {
			f.styledButton(body.X, body.Y+i-offset, body.W, line.text, line.key, line.action, m.componentStyle(squareFill, m.controlState(line.selected, line.key), p.text, p.panel))
		} else {
			f.text(body.X, body.Y+i-offset, body.W, line.text, p.text, p.panel)
		}
	}
	f.scrollbar(m, shell.Rect{X: r.X + r.W - 2, Y: body.Y, W: 1, H: body.H}, "request", len(lines), body.H, offset, p.panel)
	y = body.Y + body.H
	if inputRows > 0 {
		f.answer = shell.Rect{X: x, Y: y, W: w - 1, H: inputRows}
		if f.rows != nil {
			f.put(f.answer, style(p.text, p.input).Width(f.answer.W).Height(f.answer.H).Render(m.answerView.View(&m.answer)))
		}
		f.hits = append(f.hits, hit{Rect: f.answer, Action: action{}, Label: "Answer text · choose a question tab to review", Key: "answer"})
		scroll := m.answerView.Metrics(m.answerMetrics)
		f.scrollbar(m, shell.Rect{X: x + w - 1, Y: y, W: 1, H: inputRows}, "answer", scroll.Total, inputRows, scroll.Offset, p.input)
	}
	y = actionsY
	if noticeRows > 0 {
		message, problem := m.requestNotice(req)
		fg := p.blue
		if problem {
			fg = p.red
		}
		f.text(x, y-1, w, message, fg, p.panel)
	}
	if req.Kind == "approval" {
		total := max(0, len(req.Choices)-1)
		for _, choice := range req.Choices {
			total += ansi.StringWidth(choice) + 4
		}
		if total > w {
			f.compactButton(m, x, y, w, "Approval choices…", "approval-options", action{Kind: "approval-options"}, false, normalControl)
		} else {
			cx := x
			for i, choice := range req.Choices {
				size := ansi.StringWidth(choice) + 4
				f.compactButton(m, cx, y, size, choice, fmt.Sprint("approve:", i), m.approvalAction(req, i), false, normalControl)
				cx += size + 1
			}
		}
	} else {
		if optionsHidden {
			f.compactButton(m, x, y, min(12, w-11), "Options…", "answer-options", action{Kind: "answer-options"}, false, normalControl)
		}
		f.compactButton(m, x+w-10, y, 10, "Submit", "answer-submit", action{Kind: "answer-submit"}, false, primaryControl)
	}
	return r.Y + r.H
}

func questionTabLabel(q protocol.Question, index int, answered bool) string {
	label := q.Label
	if label == "" {
		label = fmt.Sprint("Question ", index+1)
	} else {
		label = fmt.Sprint(index+1, " ", label)
	}
	if answered {
		label += " ✓"
	}
	return label
}

// Arrow focus traverses the complete choice list, including choices offscreen.
func (m *Model) focusQuestionOption(index int) {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	q := r.Questions[m.viewState().QuestionIndex]
	count := len(q.Options)
	if protocol.QuestionAllowsOther(q) {
		count++
	}
	if count == 0 {
		m.setFocus("request-body")
		return
	}
	index = min(max(0, index), count-1)
	key := fmt.Sprint("option:", index)
	if index == len(q.Options) {
		key = "answer-other"
	}
	f := m.measure()
	lines := m.questionLines(r, max(1, f.request.W-4))
	viewport := 1
	for _, h := range f.hits {
		if h.Key == "request-body" {
			viewport = h.Rect.H
		}
	}
	for i, line := range lines {
		if line.key != key {
			continue
		}
		offset := m.viewState().RequestScroll
		if i < offset {
			offset = i
		} else if i >= offset+viewport {
			offset = i - viewport + 1
		}
		m.scrollTo("request", offset, f)
		break
	}
	m.setFocus(key)
}

// A request owns its navigation, answer field and actions, not the prompt below.
func requestControlKey(key string) bool {
	for _, prefix := range []string{"request-", "question-", "answer", "option:", "approve:", "approval-"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return key == "scrollbar-request" || key == "scrollbar-answer"
}
