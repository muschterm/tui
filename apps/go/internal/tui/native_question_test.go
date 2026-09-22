package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// This uses the actual boundary normalizer, but no provider or terminal. The
// frontend receives only the projected app request and emits request.answer.
func TestNativeQuestionUsesExistingUIAndExplicitAnswerContract(t *testing.T) {
	form, err := agent.ParseClaudeQuestions("native-request", json.RawMessage(`{
		"mode":"form","sessionId":"private-session","toolCallId":"private-tool",
		"message":"Please answer the following questions.","requestedSchema":{"type":"object","properties":{
		"question_0":{"type":"string","title":"Layout","description":"Which layout?","oneOf":[{"const":"Compact","title":"Compact"},{"const":"Roomy","title":"Roomy"}]},
		"question_1":{"type":"array","title":"Features","description":"Which features?","items":{"anyOf":[{"const":"Files","title":"Files"},{"const":"Git","title":"Git"}]}}
	}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req := form.Request
	req.SourcePayload = nil // Server's public snapshot projection.
	m, _ := questionReviewModel()
	m.snapshot.Threads[0].AgentID = "claude"
	m.snapshot.Threads[0].Requests = []protocol.Request{req}
	m.snapshot.Threads[0].State = "waiting"
	m.loadAnswer()
	m.configureInputs()
	m.prompt.SetValue("unsent composer draft")
	m.viewState().Draft = m.prompt.Value()
	for _, size := range []tea.WindowSizeMsg{{Width: 160, Height: 50}, {Width: 48, Height: 20}, {Width: 100, Height: 16}} {
		m.Update(size)
		view := m.View().Content
		if strings.Contains(view, "private-session") || strings.Contains(view, "private-tool") || strings.Contains(view, "elicitation") {
			t.Fatal("provider routing leaked into the question UI")
		}
		if m.busy != nil || m.prompt.Value() != "unsent composer draft" {
			t.Fatal("resize submitted or lost the prompt")
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.chooseAnswer("Compact")
	m.chooseAnswer("Files")
	m.chooseAnswer("Git")
	if m.busy != nil {
		t.Fatal("selection submitted a native answer")
	}
	m.activate(action{Kind: "answer-submit"})
	want := []protocol.Answer{{Choices: []string{"Compact"}}, {Choices: []string{"Files", "Git"}}}
	if m.busy == nil || m.busy.Kind != "request.answer" || m.busy.TargetID != req.ID || m.busy.Revision != req.Revision || !reflect.DeepEqual(m.busy.QuestionAnswers, want) {
		t.Fatalf("native response escaped application contract: %+v", m.busy)
	}
	if m.prompt.Value() != "unsent composer draft" {
		t.Fatal("answer consumed composer")
	}
	for _, state := range []string{"acp-accepted", "acp-unconfirmed", "acp-uncertain", "acp-undeliverable"} {
		if deliveryConfirmed(state) || strings.Contains(requestDeliveryDescription(state), "approval") {
			t.Fatalf("misleading question delivery state: %s", state)
		}
	}
}
