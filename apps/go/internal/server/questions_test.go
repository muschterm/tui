package server

import (
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestStructuredQuestionAnswersDurableAndStale(t *testing.T) {
	e := testEngine(t)
	answers := []protocol.Answer{{Choices: []string{"Keyboard flow"}}, {Choices: []string{"Clarity", "Density"}, Text: "Accessibility"}, {}}
	c := protocol.Command{Version: 1, ID: "structured", Kind: "request.answer", ThreadID: "thread-shell", TargetID: "question-async", Revision: 1, QuestionAnswers: answers}
	c.QuestionAnswers[0].Choices = []string{"unknown"}
	if _, err := e.command(c); err == nil {
		t.Fatal("invalid choice accepted")
	}
	if e.snap.Threads[0].Requests[0].State != "pending" {
		t.Fatal("invalid response changed state")
	}
	c.QuestionAnswers[0].Choices = []string{"Keyboard flow"}
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.command(c)
	if err != nil || receipt != again {
		t.Fatalf("dedupe failed: %v", err)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := stored.Threads[0].Requests[0]
	if r.State != "resolved" || r.Delivery != "fixture-confirmed" || !reflect.DeepEqual(r.QuestionAnswers, answers) {
		t.Fatalf("stored response: %+v", r)
	}
	c.ID = "competitor"
	if _, err := e.command(c); err == nil {
		t.Fatal("stale answer accepted")
	}
}

func TestLegacyQuestionAnswerCompatibility(t *testing.T) {
	e := testEngine(t)
	e.snap.Threads[0].Requests[0].Questions = []protocol.Question{{ID: "legacy", Options: []string{"A"}}}
	c := protocol.Command{Version: 1, ID: "legacy", Kind: "request.answer", ThreadID: "thread-shell", TargetID: "question-async", Revision: 1, Answers: []string{"unlisted text"}}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	r := e.snap.Threads[0].Requests[0]
	if r.Answers[0] != "unlisted text" || r.QuestionAnswers[0].Text != "unlisted text" {
		t.Fatalf("%+v", r)
	}
}
