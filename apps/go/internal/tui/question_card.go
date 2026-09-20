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
	budget := max(fixed+1+min(input, 1), m.height-3-m.baseFooterHeight(width))
	return min(desired, maxQuestionCardRows, budget)
}

func (m *Model) renderRequest(f *frame, r shell.Rect, req protocol.Request) int {
	p := m.colors()
	f.fill(r, p, p.panel)
	f.request = r
	f.text(r.X, r.Y, r.W, "┌"+strings.Repeat("─", max(0, r.W-2))+"┐", p.line, p.panel)
	for row := 1; row < r.H-1; row++ {
		f.text(r.X, r.Y+row, 1, "│", p.line, p.panel)
		f.text(r.X+r.W-1, r.Y+row, 1, "│", p.line, p.panel)
	}
	f.text(r.X, r.Y+r.H-1, r.W, "└"+strings.Repeat("─", max(0, r.W-2))+"┘", p.line, p.panel)
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
		selectorWidth = min(w/2, ansi.StringWidth(fmt.Sprint("Requests ", len(m.requests())))+2)
	}
	f.button(m, x, y, w-selectorWidth, " "+mode+" · "+agentDisplayName(req.Origin)+" ", "request-detail", action{Kind: "request-detail"}, p.gold, p.panel)
	if selectorWidth > 0 {
		f.button(m, x+w-selectorWidth, y, selectorWidth, fmt.Sprint("Requests ", len(m.requests())), "request-select", action{Kind: "request-select"}, p.blue, p.panel)
	}
	y++
	if req.Kind != "approval" && len(req.Questions) > 1 {
		m.renderQuestionTabs(f, shell.Rect{X: x, Y: y, W: w, H: 1}, req)
		y++
	}
	noticeRows := m.requestNoticeRows(req)
	inputRows := min(m.questionInputRows(req), max(0, r.Y+r.H-3-y-noticeRows))
	body := shell.Rect{X: x, Y: y, W: w, H: max(1, r.Y+r.H-2-y-inputRows-noticeRows)}
	lines := m.questionLines(req, body.W)
	f.requestMax = max(0, len(lines)-body.H)
	offset := min(max(0, m.viewState().RequestScroll), f.requestMax)
	f.hits = append(f.hits, hit{body, action{}, "Question · arrows / wheel to scroll", "request-body"})
	optionsHidden := false
	for i, line := range lines {
		if i < offset || i >= offset+body.H {
			optionsHidden = optionsHidden || line.action.Kind != ""
			continue
		}
		bg, fg := p.panel, p.text
		if line.selected {
			bg = p.selected
		}
		if line.action.Kind != "" {
			f.button(m, body.X, body.Y+i-offset, body.W, line.text, line.key, line.action, fg, bg)
		} else {
			f.text(body.X, body.Y+i-offset, body.W, line.text, fg, bg)
		}
	}
	f.scrollbar(m, shell.Rect{X: r.X + r.W - 2, Y: body.Y, W: 1, H: body.H}, "request", len(lines), body.H, offset, p.panel)
	y = body.Y + body.H
	if inputRows > 0 {
		f.answer = shell.Rect{X: x, Y: y, W: w - 1, H: inputRows}
		if f.rows != nil {
			f.put(f.answer, style(p.text, p.input).Width(f.answer.W).Height(f.answer.H).Render(m.answerView.View(&m.answer)))
		}
		f.hits = append(f.hits, hit{f.answer, action{}, "Answer text · choose a question tab to review", "answer"})
		scroll := m.answerView.Metrics(m.answerMetrics)
		f.scrollbar(m, shell.Rect{X: x + w - 1, Y: y, W: 1, H: inputRows}, "answer", scroll.Total, inputRows, scroll.Offset, p.input)
	}
	y = r.Y + r.H - 2
	if noticeRows > 0 {
		message, problem := m.requestNotice(req)
		fg := p.blue
		if problem {
			fg = p.red
		}
		f.text(x, y-1, w, message, fg, p.panel)
	}
	if req.Kind == "approval" {
		cx := x
		for i, choice := range req.Choices {
			size := ansi.StringWidth(choice) + 2
			if cx+size > x+w {
				f.button(m, x, y, w, "Approval choices…", "approval-options", action{Kind: "approval-options"}, p.blue, p.panel)
				break
			}
			f.button(m, cx, y, size, choice, fmt.Sprint("approve:", i), action{Kind: "approve", Value: choice}, p.blue, p.panel)
			cx += size
		}
	} else {
		if optionsHidden {
			f.button(m, x, y, min(11, w-9), "Options…", "answer-options", action{Kind: "answer-options"}, p.blue, p.panel)
		}
		f.button(m, x+w-8, y, 8, "Submit", "answer-submit", action{Kind: "answer-submit"}, p.blue, p.selected)
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

func (m *Model) renderQuestionTabs(f *frame, r shell.Rect, req protocol.Request) {
	p := m.colors()
	index := m.viewState().QuestionIndex
	x, end := r.X, r.X+r.W
	if index > 0 {
		f.button(m, x, r.Y, 6, "Back", "question-back", action{Kind: "question", Index: -1}, p.blue, p.panel)
		x += 6
	}
	if index+1 < len(req.Questions) {
		end -= 6
		f.button(m, end, r.Y, 6, "Next", "question-next", action{Kind: "question", Index: 1}, p.blue, p.panel)
	}
	labels := make([]string, len(req.Questions))
	total := 0
	for i, q := range req.Questions {
		d := m.questionDraft(req, i)
		answer := draftAnswer(q, d)
		labels[i] = questionTabLabel(q, i, (len(answer.Choices) > 0 || strings.TrimSpace(answer.Text) != "") && validateDraft(q, d) == nil)
		total += min(18, ansi.StringWidth(labels[i])+2)
	}
	start := 0
	if total > end-x {
		end -= 4
		// Keep the active tab visible; the overflow menu reaches every page.
		start = index
		f.button(m, end, r.Y, 4, " …", "question-tabs", action{Kind: "question-tabs"}, p.blue, p.panel)
	}
	for i := start; i < len(labels) && x < end; i++ {
		size := min(18, ansi.StringWidth(labels[i])+2)
		if x+size > end && i != index {
			break
		}
		size = min(size, end-x)
		bg := p.panel
		if i == index {
			bg = p.selected
		}
		f.button(m, x, r.Y, size, labels[i], fmt.Sprint("question-page:", i), action{Kind: "question-index", Index: i}, p.text, bg)
		x += size
	}
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
