package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func questionReviewModel() (*Model, protocol.Request) {
	m := testModel()
	optional := false
	req := protocol.Request{ID: "review-request", Revision: 7, Kind: "question", Mode: "async", State: "pending", Origin: "Fixture agent", Questions: []protocol.Question{
		{ID: "layout", Label: "Layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}, AllowOther: true},
		{ID: "features", Label: "Features", Text: "Which features?", Kind: "multiple", Options: []string{"Files", "Git", "Terminal"}, AllowOther: true},
		{ID: "notes", Label: "Notes", Text: "Anything else?", Kind: "text", Required: &optional},
	}}
	m.snapshot.Threads[0].Requests = []protocol.Request{req}
	m.snapshot.Threads[0].Queue = nil
	m.viewState().RequestIndex = 0
	m.viewState().QuestionIndex = 0
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.loadAnswer()
	m.configureInputs()
	return m, req
}

func TestQuestionReviewChoicesAdvanceLocallyAndSubmitExplicitly(t *testing.T) {
	m, req := questionReviewModel()
	m.prompt.SetValue("preserve my prompt")
	m.viewState().Draft = m.prompt.Value()
	m.chooseAnswer("Compact")
	if m.viewState().QuestionIndex != 1 || m.busy != nil || m.prompt.Value() != "preserve my prompt" {
		t.Fatal("single choice failed to advance locally")
	}
	m.chooseAnswer("Files")
	m.chooseAnswer("Git")
	m.chooseAnswer("Files")
	if m.viewState().QuestionIndex != 1 || !reflect.DeepEqual(m.questionDraft(req, 1).Choices, []string{"Git"}) {
		t.Fatal("multi choice did not toggle in place")
	}
	m.toggleOther()
	if m.focus != "answer" || !m.questionDraft(req, 1).Other {
		t.Fatal("Other did not focus its editor")
	}
	m.answer.SetValue("Custom surface")
	m.storeAnswer(m.answer.Value())
	m.selectQuestion(0)
	m.selectQuestion(1)
	if m.answer.Value() != "Custom surface" || !m.questionDraft(req, 1).Other {
		t.Fatal("navigation lost Other draft")
	}
	m.selectQuestion(2)
	if err := validateDraft(req.Questions[2], m.questionDraft(req, 2)); err != nil {
		t.Fatal("optional text required an answer", err)
	}
	if m.busy != nil {
		t.Fatal("navigation submitted answers")
	}
	m.activate(action{Kind: "answer-submit"})
	if m.busy == nil {
		t.Fatal("explicit submit did not create command", m.status)
	}
	c := m.busy
	want := []protocol.Answer{{Choices: []string{"Compact"}}, {Choices: []string{"Git"}, Text: "Custom surface"}, {}}
	if c.Kind != "request.answer" || c.TargetID != req.ID || c.Revision != req.Revision || !reflect.DeepEqual(c.QuestionAnswers, want) || c.Answers != nil {
		t.Fatalf("wrong submission: %+v", c)
	}
	if m.prompt.Value() != "preserve my prompt" {
		t.Fatal("submit changed prompt")
	}
}

func TestQuestionReviewOtherValidationAndSingleReplacement(t *testing.T) {
	m, req := questionReviewModel()
	m.toggleOther()
	if validateDraft(req.Questions[0], m.questionDraft(req, 0)) == nil {
		t.Fatal("checked empty Other accepted")
	}
	m.storeAnswer("Alternative")
	m.chooseAnswer("Roomy")
	d := m.questionDraft(req, 0)
	if d.Other || !reflect.DeepEqual(d.Choices, []string{"Roomy"}) || draftAnswer(req.Questions[0], d).Text != "" {
		t.Fatal("single option retained submitted Other", d)
	}
	m.selectQuestion(0)
	m.toggleOther()
	if m.answer.Value() != "Alternative" || len(m.questionDraft(req, 0).Choices) != 0 {
		t.Fatal("Other recovery lost text or retained single selection")
	}
}

func TestQuestionReviewDraftPersistenceAndRevisionIsolation(t *testing.T) {
	m, req := questionReviewModel()
	m.saveQuestionDraft(req, 0, answerDraft{Other: true, Text: "old unresolved draft"})
	raw, err := json.Marshal(m.state)
	if err != nil {
		t.Fatal(err)
	}
	restored := New(nil, "test", m.snapshot, raw)
	if restored.questionDraft(req, 0).Text != "old unresolved draft" || restored.busy != nil {
		t.Fatal("restore lost draft or submitted it")
	}
	changed := req
	changed.Revision++
	if got := restored.questionDraft(changed, 0); got.Text != "" || got.Other {
		t.Fatal("revision inherited old answer")
	}
	changed = req
	changed.Questions = append([]protocol.Question(nil), req.Questions...)
	changed.Questions[0].Text = "A changed question"
	if restored.questionDraft(changed, 0).Text != "" {
		t.Fatal("schema inherited old answer")
	}
	restored.saveQuestionDraft(changed, 0, answerDraft{Choices: []string{"Compact"}})
	if restored.questionDraft(req, 0).Text != "old unresolved draft" {
		t.Fatal("schema update deleted unresolved old draft")
	}
}

func TestQuestionReviewTabsProgressAndConditionalControls(t *testing.T) {
	m, _ := questionReviewModel()
	f := m.render()
	if hasControl(f, "question-back") || !hasControl(f, "question-next") || hasControl(f, "request-select") || hasControl(f, "answer-options") {
		t.Fatal("unexpected first-page controls")
	}
	for i := 0; i < 3; i++ {
		h := controlHit(t, f, fmt.Sprint("question-page:", i))
		if h.Rect.Y != f.request.Y+1 {
			t.Fatal("question tabs are not at top")
		}
	}
	m.chooseAnswer("Compact")
	f = m.render()
	tab := controlHit(t, f, "question-page:0")
	if !strings.Contains(tab.Label, "✓") {
		t.Fatal("answered tab missing progress")
	}
	m.selectQuestion(2)
	f = m.render()
	if !hasControl(f, "question-back") || hasControl(f, "question-next") {
		t.Fatal("unexpected last-page controls")
	}
	if f.answer.H == 0 {
		t.Fatal("text question missing editor")
	}
	other := m.snapshot.Threads[0].Requests[0]
	other.ID = "second"
	m.snapshot.Threads[0].Requests = append(m.snapshot.Threads[0].Requests, other)
	if !hasControl(m.measure(), "request-select") {
		t.Fatal("multiple pending requests lack selector")
	}
}

func TestQuestionReviewScrollAndNarrowGeometry(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}} {
		m, _ := questionReviewModel()
		q := &m.snapshot.Threads[0].Requests[0].Questions[0]
		q.Options = nil
		for i := 0; i < 25; i++ {
			q.Options = append(q.Options, fmt.Sprintf("Option %02d with a longer label", i))
		}
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		f := m.render()
		if f.request.H > 12 || f.request.H < 4 || f.requestMax == 0 || !hasControl(f, "answer-options") {
			t.Fatalf("%v: missing bounded scrolling card: %+v", size, f.request)
		}
		for _, h := range f.hits {
			if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > size[0] || h.Rect.Y+h.Rect.H > size[1]-1 {
				t.Fatalf("%v: control out of bounds: %+v", size, h)
			}
		}
		m.focusQuestionOption(24)
		if m.focus != "option:24" || m.viewState().RequestScroll == 0 || !hasControl(m.measure(), "option:24") {
			t.Fatal("offscreen option not reachable", size)
		}
		m.focusQuestionOption(0)
		if !hasControl(m.measure(), "option:0") {
			t.Fatal("first option not reachable again")
		}
	}
}

func TestQuestionReviewCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for question visual artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"radio", "light-radio", "multi-other", "narrow", "invalid-submit", "light-error", "narrow-error"} {
		m, req := questionReviewModel()
		if scenario == "light-radio" || scenario == "light-error" {
			m.state.Light = true
			m.configureInputs()
		}
		if scenario == "multi-other" {
			m.selectQuestion(1)
			m.chooseAnswer("Files")
			m.chooseAnswer("Git")
			m.toggleOther()
			m.answer.SetValue("Custom surface")
			m.storeAnswer(m.answer.Value())
			m.configureInputs()
		}
		if scenario == "narrow" || scenario == "narrow-error" {
			m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
		}
		if scenario == "invalid-submit" || scenario == "narrow-error" {
			m.hover = "answer-submit"
			m.submitAnswers(action{Kind: "answer-submit"})
		}
		if scenario == "light-error" {
			m.setRequestFeedback(m.state.Active, req.ID, req.Revision, "Server rejected answer · retry after correcting it", false)
			m.hover = "answer-submit"
			m.configureInputs()
		}
		f := m.render()
		if !strings.Contains(ansi.Strip(strings.Join(f.rows, "\n")), "Submit") {
			t.Fatal("capture lost submit")
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%dx%d-light%t-question-%s.ansi", m.width, m.height, m.state.Light, scenario)), []byte(strings.Join(f.rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuestionReviewChoiceMarkersAndMeasurement(t *testing.T) {
	m, req := questionReviewModel()
	for _, multiple := range []bool{false, true} {
		index := 0
		if multiple {
			index = 1
		}
		m.selectQuestion(index)
		m.saveQuestionDraft(req, index, answerDraft{Choices: []string{req.Questions[index].Options[0]}})
		painted, measured := m.render(), m.measure()
		if painted.request != measured.request || painted.answer != measured.answer || !reflect.DeepEqual(painted.hits, measured.hits) {
			t.Fatal("question measurement and painting disagree")
		}
		selected, empty := m.icon("radio-on"), m.icon("radio")
		if multiple {
			selected, empty = m.icon("checkbox-on"), m.icon("checkbox")
		}
		if !strings.HasPrefix(controlHit(t, painted, "option:0").Label, selected) || !strings.HasPrefix(controlHit(t, painted, "option:1").Label, empty) {
			t.Fatal("selection marker does not reflect question kind")
		}
	}
}

func TestQuestionReviewKeyboardChoicesScrollWithoutMovingTranscript(t *testing.T) {
	m, _ := questionReviewModel()
	q := &m.snapshot.Threads[0].Requests[0].Questions[1]
	q.Options = nil
	for i := 0; i < 25; i++ {
		q.Options = append(q.Options, fmt.Sprintf("Feature %02d", i))
	}
	req := m.snapshot.Threads[0].Requests[0]
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.selectQuestion(1)
	before := m.viewState().Scroll
	for i := 0; i < 24; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	f := m.measure()
	if m.focus != "option:24" || !hasControl(f, "option:24") || m.viewState().RequestScroll == 0 {
		t.Fatal("Down failed to reveal offscreen choice")
	}
	if m.viewState().Scroll != before {
		t.Fatal("choice arrows moved transcript")
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.viewState().QuestionIndex != 1 || !reflect.DeepEqual(m.questionDraft(req, 1).Choices, []string{"Feature 24"}) || m.busy != nil {
		t.Fatalf("Space failed to toggle multiple selection locally: focus=%q page=%d draft=%+v key=%q", m.focus, m.viewState().QuestionIndex, m.questionDraft(req, 1), (tea.KeyPressMsg{Code: ' ', Text: " "}).String())
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if len(m.questionDraft(req, 1).Choices) != 0 {
		t.Fatal("Space did not deselect option")
	}
	for i := 0; i < 24; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if m.focus != "option:0" || !hasControl(m.measure(), "option:0") || m.viewState().Scroll != before {
		t.Fatal("Up failed to restore first choice independently of transcript")
	}
	h := controlHit(t, m.measure(), "option:1")
	m.Update(tea.MouseMotionMsg{X: h.Rect.X + h.Rect.W - 1, Y: h.Rect.Y})
	if m.hover != "option:1" {
		t.Fatal("choice row hover does not cover full width")
	}
	clickControl(m, h)
	if !reflect.DeepEqual(m.questionDraft(req, 1).Choices, []string{"Feature 01"}) || m.viewState().QuestionIndex != 1 || m.viewState().Scroll != before {
		t.Fatal("full-row activation altered navigation or transcript")
	}
}

func TestQuestionReviewChangedSchemaDoesNotReuseVisibleOtherText(t *testing.T) {
	m, old := questionReviewModel()
	m.toggleOther()
	m.Update(tea.PasteMsg{Content: "answer to the original wording"})
	m.viewState().RequestScroll = 2
	next := protocol.Snapshot{}
	raw, _ := json.Marshal(m.snapshot)
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	// Defend against a source changing the schema without advancing its own
	// request revision. The enclosing snapshot still has a newer revision.
	next.Revision++
	next.Threads[0].Requests[0].Questions[0].Text = "A different question"
	m.Update(snapshotMsg(next))
	if m.answer.Value() != "" || m.viewState().RequestScroll != 0 || m.busy != nil {
		t.Fatal("changed question reused visible text or submitted a draft")
	}
	if m.questionDraft(old, 0).Text != "answer to the original wording" {
		t.Fatal("changed question discarded its previous draft")
	}
}
