package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func pressKey(m *Model, k tea.KeyPressMsg) { m.Update(k) }

func typeText(m *Model, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func noQuestionChoices(m *Model, req protocol.Request) bool {
	for i := range req.Questions {
		d := m.questionDraft(req, i)
		if len(d.Choices) > 0 || d.Other || d.Text != "" {
			return false
		}
	}
	return true
}

var sgrPattern = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// cellSGR returns the parameters of the SGR sequence in effect for the one
// visible cell at x: cutCells keeps the row's zero-width sequences.
func cellSGR(f frame, x, y int) string {
	cell := cutCells(f.rows[y], x, x+1)
	params := ""
	rest := cell
	for len(rest) > 0 {
		loc := sgrPattern.FindStringSubmatchIndex(rest)
		if loc == nil || loc[0] > 0 {
			break
		}
		params = rest[loc[2]:loc[3]]
		rest = rest[loc[1]:]
	}
	return params
}

// cellAttrs splits SGR parameters into attributes and 24-bit colors.
func cellAttrs(params string) (attrs []string, fg, bg string) {
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		if (parts[i] == "38" || parts[i] == "48") && i+4 < len(parts) && parts[i+1] == "2" {
			color := strings.Join(parts[i+2:i+5], ";")
			if parts[i] == "38" {
				fg = color
			} else {
				bg = color
			}
			i += 4
			continue
		}
		attrs = append(attrs, parts[i])
	}
	return attrs, fg, bg
}

func rgbParams(hex string) string {
	rgb := parseHex(hex)
	return fmt.Sprintf("%d;%d;%d", rgb[0], rgb[1], rgb[2])
}

// backgroundAt reports whether the painted cell uses the given true-color
// background.
func backgroundAt(f frame, x, y int, hex string) bool {
	_, _, bg := cellAttrs(cellSGR(f, x, y))
	return bg == rgbParams(hex)
}

func cellHasAttr(f frame, x, y int, attr string) bool {
	attrs, _, _ := cellAttrs(cellSGR(f, x, y))
	return slices.Contains(attrs, attr)
}

func TestQuestionRepeatedEnterOnNavigationRecordsNoChoices(t *testing.T) {
	m, req := questionReviewModel()
	m.setFocus("question-next")
	for i := 0; i < 5; i++ {
		pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if i == 0 && m.focus != "question-next" {
			t.Fatalf("Next moved focus to %q", m.focus)
		}
	}
	if m.viewState().QuestionIndex != 2 || m.focus != "question-page:2" {
		t.Fatalf("Next at the end: page %d focus %q", m.viewState().QuestionIndex, m.focus)
	}
	if !noQuestionChoices(m, req) || m.busy != nil {
		t.Fatal("repeated Enter on Next recorded a choice or submitted")
	}
	m.setFocus("question-page:1")
	for i := 0; i < 3; i++ {
		pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if m.viewState().QuestionIndex != 1 || m.focus != "question-page:1" || !noQuestionChoices(m, req) || m.busy != nil {
		t.Fatalf("repeated Enter on a tab: page %d focus %q", m.viewState().QuestionIndex, m.focus)
	}
	m.setFocus("question-back")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewState().QuestionIndex != 0 || m.focus != "question-page:0" || !noQuestionChoices(m, req) {
		t.Fatalf("Back at the start: page %d focus %q", m.viewState().QuestionIndex, m.focus)
	}
}

func TestQuestionCtrlSInCardNeverSendsPrompt(t *testing.T) {
	ctrlS := tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	for _, focus := range []string{"answer", "option:0", "question-page:0", "request-detail", "answer-submit"} {
		m, req := questionReviewModel()
		m.prompt.SetValue("unsent prompt")
		m.viewState().Draft = m.prompt.Value()
		m.toggleOther()
		m.storeAnswer("Custom")
		m.loadAnswer()
		m.configureInputs()
		m.setFocus(focus)
		// Invalid: the required multiple-choice question is unanswered.
		pressKey(m, ctrlS)
		if m.busy != nil {
			t.Fatalf("%s: invalid Ctrl+S dispatched %+v", focus, m.busy)
		}
		if message, _ := m.requestNotice(req); !strings.Contains(message, "Question 2") {
			t.Fatalf("%s: invalid Ctrl+S has no notice: %q", focus, message)
		}
		m.saveQuestionDraft(req, 1, answerDraft{Choices: []string{"Git"}})
		m.setFocus(focus)
		pressKey(m, ctrlS)
		if m.busy == nil || m.busy.Kind != "request.answer" || m.prompt.Value() != "unsent prompt" {
			t.Fatalf("%s: Ctrl+S did not answer the request: %+v", focus, m.busy)
		}
	}
}

func TestQuestionAnswerFieldEnterNewlineAndEscape(t *testing.T) {
	m, req := questionReviewModel()
	m.toggleOther() // Question 1: Other focuses its field.
	if m.focus != "answer" {
		t.Fatalf("Other focus %q", m.focus)
	}
	typeText(m, "a")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	typeText(m, "b")
	pressKey(m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	typeText(m, "c")
	if got := m.questionDraft(req, 0).Text; got != "a\nb\nc" {
		t.Fatalf("newline keys: %q", got)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focus != "answer-other" || m.questionDraft(req, 0).Text != "a\nb\nc" || m.viewState().QuestionIndex != 0 {
		t.Fatalf("Esc: focus %q draft %q", m.focus, m.questionDraft(req, 0).Text)
	}
	m.setFocus("answer")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewState().QuestionIndex != 1 || m.busy != nil || m.questionDraft(req, 0).Text != "a\nb\nc" {
		t.Fatalf("Enter did not move to the next question: page %d", m.viewState().QuestionIndex)
	}
	// Last (open-ended) question: Enter submits through validation.
	m.selectQuestion(2)
	if m.focus != "answer" {
		t.Fatalf("open-ended focus %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil {
		t.Fatal("invalid Enter submitted")
	}
	if message, _ := m.requestNotice(req); !strings.Contains(message, "Question 2") {
		t.Fatalf("invalid Enter has no notice: %q", message)
	}
	m.saveQuestionDraft(req, 1, answerDraft{Choices: []string{"Files"}})
	m.selectQuestion(2)
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focus != "question-page:2" {
		t.Fatalf("Esc from open-ended field went to %q", m.focus)
	}
	m.setFocus("answer")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.Kind != "request.answer" {
		t.Fatal("valid Enter on the last question did not submit")
	}
}

func TestQuestionDigitKeysChooseOptions(t *testing.T) {
	m, req := questionReviewModel()
	m.setFocus("question-page:0")
	typeText(m, "2")
	if d := m.questionDraft(req, 0); len(d.Choices) != 1 || d.Choices[0] != "Roomy" {
		t.Fatalf("digit radio: %+v", d)
	}
	if m.viewState().QuestionIndex != 1 || m.focus != "question-page:1" {
		t.Fatalf("digit radio did not advance with the tab: page %d focus %q", m.viewState().QuestionIndex, m.focus)
	}
	typeText(m, "1")
	typeText(m, "3")
	if d := m.questionDraft(req, 1); len(d.Choices) != 2 || m.viewState().QuestionIndex != 1 {
		t.Fatalf("digit checkbox: %+v", d)
	}
	typeText(m, "1")
	if d := m.questionDraft(req, 1); len(d.Choices) != 1 || d.Choices[0] != "Terminal" {
		t.Fatalf("digit checkbox toggle off: %+v", d)
	}
	typeText(m, "9") // No ninth option.
	typeText(m, "4") // Other.
	if !m.questionDraft(req, 1).Other || m.focus != "answer" {
		t.Fatalf("digit Other: focus %q", m.focus)
	}
	typeText(m, "5")
	if d := m.questionDraft(req, 1); d.Text != "5" || len(d.Choices) != 1 {
		t.Fatalf("digit in the answer field chose an option: %+v", d)
	}
	m.setFocus("prompt")
	typeText(m, "1")
	if m.prompt.Value() != "1" || len(m.questionDraft(req, 1).Choices) != 1 {
		t.Fatal("digit in the composer chose an option")
	}
	// Radio Other by digit, then again: stays selected and focuses the field.
	m.selectQuestion(0)
	m.setFocus("option:0")
	typeText(m, "3")
	typeText(m, "3")
	if d := m.questionDraft(req, 0); !d.Other || len(d.Choices) != 0 || m.focus != "answer" || m.viewState().QuestionIndex != 0 {
		t.Fatalf("radio digit Other: %+v focus %q", d, m.focus)
	}
	if m.busy != nil {
		t.Fatal("digits submitted")
	}
}

func TestQuestionUpDownReachTabsFieldAndSubmit(t *testing.T) {
	m, req := questionReviewModel()
	m.selectQuestion(1)
	m.setFocus("option:0")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != "question-page:1" {
		t.Fatalf("Up from the first option: %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "option:0" {
		t.Fatalf("Down from the tab: %q", m.focus)
	}
	m.setFocus("answer-other")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "answer-submit" {
		t.Fatalf("Down past the last option without a field: %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != "answer-other" {
		t.Fatalf("Up from Submit: %q", m.focus)
	}
	m.saveQuestionDraft(req, 1, answerDraft{Other: true, Text: "x"})
	m.loadAnswer()
	m.configureInputs()
	m.setFocus("answer-other")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "answer" {
		t.Fatalf("Down past the last option with a field: %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.focus != "answer-submit" {
		t.Fatalf("Down from the field's last row: %q", m.focus)
	}
	// A single-question request has no tabs: Up reaches the header.
	single := req
	single.Questions = req.Questions[:1]
	m.snapshot.Threads[0].Requests = []protocol.Request{single}
	m.viewState().QuestionIndex = 0
	m.configureInputs()
	m.setFocus("option:0")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != "request-detail" || m.busy != nil {
		t.Fatalf("Up without tabs: %q", m.focus)
	}
}

func TestQuestionStaleIndexRendersLastQuestion(t *testing.T) {
	m, req := questionReviewModel()
	m.viewState().QuestionIndex = 99
	f := m.render()
	if !strings.Contains(ansi.Strip(strings.Join(f.rows, "\n")), req.Questions[2].Text) {
		t.Fatal("stale index did not render the last question")
	}
	m.activate(action{Kind: "answer-options"})
	m.activate(action{Kind: "question", Index: -1})
	m.storeAnswer("typed")
	m.setFocus("question-page:0")
	typeText(m, "1")
	m.viewState().QuestionIndex = -4
	m.render()
	m.focusQuestionOption(0)
}

func TestQuestionHeaderSitsInsideIntactOutline(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m, _ := questionReviewModel()
		m.plainIcons = plain
		m.setFocus("request-detail")
		f := m.render()
		r := f.request
		b := componentBorder(roundedOutline, plain)
		top := ansi.Strip(cutCells(f.rows[r.Y], r.X, r.X+r.W))
		if top != b.TopLeft+strings.Repeat(b.Top, r.W-2)+b.TopRight {
			t.Fatalf("top border broken: %q", top)
		}
		h := controlHit(t, f, "request-detail")
		if h.Rect.Y != r.Y+1 || h.Rect.X != r.X+2 || h.Rect.W >= r.W-4 {
			t.Fatalf("header hit %+v in card %+v", h.Rect, r)
		}
		header := ansi.Strip(cutCells(f.rows[r.Y+1], r.X+2, r.X+r.W-2))
		if !strings.HasPrefix(header, m.icon("question")+" "+agentDisplayName("Fixture agent")+" · Answer anytime") {
			t.Fatalf("header text %q", header)
		}
		if mark := ansi.Strip(cutCells(f.rows[r.Y+1], r.X+1, r.X+2)); mark != m.icon("focus") {
			t.Fatalf("focus mark %q", mark)
		}
		if cellHasAttr(f, r.X+4, r.Y+1, "4") {
			t.Fatal("focused header fell back to an underline")
		}
	}
}

func TestQuestionAnsweredMarkerDoesNotShiftLabel(t *testing.T) {
	m, req := questionReviewModel()
	before := m.render()
	h := controlHit(t, before, "question-page:0")
	label := func(f frame) string { return ansi.Strip(cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W)) }
	was := label(before)
	m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
	m.viewState().QuestionIndex = 0
	after := m.render()
	now := label(after)
	check := m.icon("check")
	if controlHit(t, after, "question-page:0").Rect != h.Rect {
		t.Fatal("answering resized the tab")
	}
	if !strings.HasSuffix(strings.TrimRight(now, " "), check) || strings.Contains(was, check) {
		t.Fatalf("marker: %q -> %q", was, now)
	}
	if strings.TrimRight(was, " ") != strings.TrimRight(strings.Replace(now, check, " ", 1), " ") {
		t.Fatalf("label moved: %q -> %q", was, now)
	}
}

func TestQuestionSelectedOptionFillIsBounded(t *testing.T) {
	m, req := questionReviewModel()
	m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
	m.hover = "option:0"
	f := m.render()
	h := controlHit(t, f, "option:0")
	want := ansi.StringWidth(m.icon("radio-on")) + 1 + ansi.StringWidth("Compact")
	if h.Rect.X != f.request.X+2 || h.Rect.W != want {
		t.Fatalf("option extent %+v, want x=%d w=%d", h.Rect, f.request.X+2, want)
	}
	if !backgroundAt(f, h.Rect.X, h.Rect.Y, m.hoverFill()) || !backgroundAt(f, h.Rect.X+h.Rect.W-1, h.Rect.Y, m.hoverFill()) {
		t.Fatal("hover fill missing inside the extent")
	}
	for x := h.Rect.X + h.Rect.W; x < f.request.X+f.request.W-1; x++ {
		if backgroundAt(f, x, h.Rect.Y, m.hoverFill()) || backgroundAt(f, x, h.Rect.Y, m.colors().selected) {
			t.Fatalf("fill leaked to x=%d", x)
		}
	}
	if !cellHasAttr(f, h.Rect.X+h.Rect.W-1, h.Rect.Y, "1") || !cellHasAttr(f, h.Rect.X, h.Rect.Y, "1") {
		t.Fatal("selected label lost bold under hover")
	}
	m.hover = ""
	m.Update(tea.MouseMotionMsg{X: h.Rect.X + h.Rect.W + 2, Y: h.Rect.Y})
	if m.hover == "option:0" {
		t.Fatal("pointer beyond the label hovered the option")
	}
	m.Update(tea.MouseMotionMsg{X: h.Rect.X + h.Rect.W - 1, Y: h.Rect.Y})
	if m.hover != "option:0" {
		t.Fatal("pointer on the label did not hover the option")
	}
}

func TestQuestionSubmitDisabledHasNoHoverFill(t *testing.T) {
	cells := func(m *Model) string {
		f := m.render()
		h := controlHit(t, f, "answer-submit")
		var b strings.Builder
		for x := h.Rect.X; x < h.Rect.X+h.Rect.W; x++ {
			b.WriteString(cellSGR(f, x, h.Rect.Y) + ansi.Strip(cutCells(f.rows[h.Rect.Y], x, x+1)) + "|")
		}
		return b.String()
	}
	enabled, _ := questionReviewModel()
	rest := cells(enabled)
	for _, block := range []string{"resume", "disconnected", "in-flight"} {
		m, req := questionReviewModel()
		switch block {
		case "resume":
			m.snapshot.Threads[0].NeedsResume = true
		case "disconnected":
			m.connected = false
		case "in-flight":
			m.busy = &protocol.Command{ID: "c", Kind: "request.answer", ThreadID: m.state.Active, TargetID: req.ID, Revision: req.Revision}
			m.inFlight = true
		}
		m.configureInputs()
		if !m.submitBlocked() {
			t.Fatalf("%s: Submit not blocked", block)
		}
		idle := cells(m)
		m.hover = "answer-submit"
		h := controlHit(t, m.render(), "answer-submit")
		if hovered := cells(m); hovered != idle || backgroundAt(m.render(), h.Rect.X+2, h.Rect.Y, m.hoverFill()) {
			t.Fatalf("%s: disabled Submit changed on hover", block)
		}
		if idle == rest {
			t.Fatalf("%s: disabled Submit looks enabled", block)
		}
		if message, _ := m.requestCardNotice(req); message == "" || !strings.Contains(ansi.Strip(strings.Join(m.render().rows, "\n")), message) {
			t.Fatalf("%s: no reason shown", block)
		}
	}
}

func TestQuestionDraftSurvivesRevisionBumpAndIsPrunedOnResolution(t *testing.T) {
	m, req := questionReviewModel()
	m.saveQuestionDraft(req, 0, answerDraft{Other: true, Text: "keep me"})
	m.loadAnswer()
	next := protocol.Snapshot{}
	raw, _ := json.Marshal(m.snapshot)
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	next.Revision++
	next.Threads[0].Requests[0].Revision++
	m.Update(snapshotMsg(next))
	bumped, _ := m.request()
	if d := m.questionDraft(bumped, 0); d.Text != "keep me" || !d.Other || m.answer.Value() != "keep me" || m.busy != nil {
		t.Fatalf("revision-only bump lost the draft: %+v", d)
	}
	// Legacy revision-bound drafts migrate once at load.
	legacy := m.state
	legacy.Threads = map[string]*threadView{m.state.Active: {QuestionDrafts: map[string][]answerDraft{legacyQuestionDraftKey(bumped): {{Choices: []string{"Roomy"}}}}}}
	data, _ := json.Marshal(legacy)
	restored := New(nil, "test", next, data)
	if d := restored.questionDraft(bumped, 0); len(d.Choices) != 1 || d.Choices[0] != "Roomy" {
		t.Fatalf("legacy draft not migrated: %+v", d)
	}
	next.Revision++
	next.Threads[0].Requests[0].State = "resolved"
	m.Update(snapshotMsg(next))
	for key := range m.viewState().QuestionDrafts {
		if strings.HasPrefix(key, req.ID+"#") {
			t.Fatal("resolved request draft was not pruned")
		}
	}
	if m.busy != nil {
		t.Fatal("resolution submitted a draft")
	}
}

func TestQuestionF6EntersAndLeavesAnswerField(t *testing.T) {
	m, req := questionReviewModel()
	m.selectQuestion(2)
	m.setFocus("prompt")
	reached := false
	for i := 0; i < 10 && !reached; i++ {
		pressKey(m, tea.KeyPressMsg{Code: tea.KeyF6})
		reached = m.focus == "answer"
	}
	if !reached {
		t.Fatal("F6 never reached the answer field")
	}
	typeText(m, "note")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyF6})
	if m.focus == "answer" || m.questionDraft(req, 2).Text != "note" || m.busy != nil {
		t.Fatalf("F6 out of the field: focus %q draft %q", m.focus, m.questionDraft(req, 2).Text)
	}
}

// foregroundAt reports whether the painted cell uses the given true-color
// foreground.
func foregroundAt(f frame, x, y int, hex string) bool {
	_, fg, _ := cellAttrs(cellSGR(f, x, y))
	return fg == rgbParams(hex)
}

// outlineCells lists every border cell of a rounded outline at r.
func outlineCells(r shell.Rect) [][2]int {
	var cells [][2]int
	for x := r.X; x < r.X+r.W; x++ {
		cells = append(cells, [2]int{x, r.Y}, [2]int{x, r.Y + r.H - 1})
	}
	for y := r.Y + 1; y < r.Y+r.H-1; y++ {
		cells = append(cells, [2]int{r.X, y}, [2]int{r.X + r.W - 1, y})
	}
	return cells
}

// assertUniformOutline requires one ink on every border cell, on the canvas.
func assertUniformOutline(t *testing.T, m *Model, f frame, r shell.Rect, ink string) {
	t.Helper()
	for _, c := range outlineCells(r) {
		if !foregroundAt(f, c[0], c[1], ink) || !backgroundAt(f, c[0], c[1], m.colors().canvas) {
			t.Fatalf("outline cell %v is %q, want ink %s on the canvas", c, cellSGR(f, c[0], c[1]), ink)
		}
	}
}

// A selected Other… is a selected choice: single-choice questions mark it
// with the same filled radio as a selected sibling, multiple-choice ones with
// the checked box, and never with the answered check.
func TestQuestionSelectedOtherUsesSiblingChoiceMarker(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m, req := questionReviewModel()
		m.plainIcons = plain
		for index, marker := range map[int]string{0: m.icon("radio-on"), 1: m.icon("checkbox-on")} {
			m.selectQuestion(index)
			m.saveQuestionDraft(req, index, answerDraft{Other: true, Text: "Custom"})
			m.configureInputs()
			f := m.render()
			other := controlHit(t, f, "answer-other")
			row := ansi.Strip(cutCells(f.rows[other.Rect.Y], other.Rect.X, other.Rect.X+other.Rect.W))
			if !strings.HasPrefix(row, marker+" Other…") || !strings.HasPrefix(other.Label, marker+" ") {
				t.Fatalf("plain=%t question %d: Other row %q, want marker %q", plain, index, row, marker)
			}
			if strings.Contains(row, m.icon("check")) && m.icon("check") != marker {
				t.Fatalf("plain=%t question %d: Other shows the answered check: %q", plain, index, row)
			}
		}
	}
	m := testModel()
	for _, glyph := range []string{"radio", "radio-on", "checkbox", "checkbox-on"} {
		if w := ansi.StringWidth(m.icon(glyph)); w != 1 {
			t.Fatalf("%s glyph is %d cells, want one", glyph, w)
		}
	}
}

// The card's frame is one colour on all four sides. Hovering or focusing an
// inner control does not recolor it; keyboard focus inside the card uses the
// prompt's focused ink, and focus outside leaves the rest outline.
func TestQuestionCardOutlineIsUniform(t *testing.T) {
	m, _ := questionReviewModel()
	p := m.colors()
	rest := m.containerStyle(false, p.text, p.panel).border
	focused := m.containerStyle(true, p.text, p.panel).border
	promptFocused := m.componentStyle(roundedOutline, componentState{Focused: true}, p.text, p.input).border
	if rest == focused || focused != promptFocused {
		t.Fatalf("frame inks rest=%s focused=%s prompt focused=%s", rest, focused, promptFocused)
	}
	m.setFocus("prompt")
	for _, hover := range []string{"", "option:0", "answer-submit", "request-detail", "question-page:1"} {
		m.hover = hover
		f := m.render()
		assertUniformOutline(t, m, f, f.request, rest)
	}
	for _, focus := range []string{"option:0", "answer-other", "answer-submit", "request-detail"} {
		m.hover = "option:1"
		m.setFocus(focus)
		f := m.render()
		assertUniformOutline(t, m, f, f.request, focused)
	}
}
