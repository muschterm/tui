package server

import (
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func questionHistoryMarkers(thread protocol.Thread, requestID string) []protocol.Activity {
	var markers []protocol.Activity
	for _, activity := range thread.Activity {
		if activity.Role == "question-answer" && activity.RequestID == requestID {
			markers = append(markers, activity)
		}
	}
	return markers
}

func TestFixtureQuestionAnswerPersistsOneChronologyMarker(t *testing.T) {
	e := testEngine(t)
	answers := []protocol.Answer{{Choices: []string{"Keyboard flow"}}, {Choices: []string{"Clarity", "Density"}}, {Text: "Keep the preview compact"}}
	command := protocol.Command{
		Version: 1, ID: "question-history-fixture-answer", Kind: "request.answer",
		ThreadID: "thread-shell", TargetID: "question-async", Revision: 1,
		QuestionAnswers: answers,
	}
	first, err := e.command(command)
	if err != nil {
		t.Fatal(err)
	}
	thread := threadOf(e.current(), command.ThreadID)
	request := thread.Requests[0]
	markers := questionHistoryMarkers(thread, request.ID)
	if request.State != "resolved" || request.Delivery != "fixture-confirmed" || !reflect.DeepEqual(request.QuestionAnswers, answers) {
		t.Fatalf("fixture answer was not durably accepted: %+v", request)
	}
	if len(markers) != 1 || markers[0].ID != "question-answer:question-async:1" || markers[0].TurnID != thread.TurnID {
		t.Fatalf("chronology marker: %+v", markers)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(questionHistoryMarkers(threadOf(stored, command.ThreadID), request.ID), markers) {
		t.Fatal("answer chronology marker was not persisted with the request")
	}
	again, err := e.command(command)
	if err != nil || again != first {
		t.Fatalf("duplicate submission did not return its original receipt: %+v %v", again, err)
	}
	if got := questionHistoryMarkers(threadOf(e.current(), command.ThreadID), request.ID); len(got) != 1 {
		t.Fatalf("idempotent retry duplicated the history card anchor: %+v", got)
	}
}

func TestNativeQuestionHistoryPersistsBeforeDeliveryConfirmation(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	threadID, request := pendingNativeQuestion(t, e)
	command := nativeAnswer(threadID, request)
	command.ID = "question-history-native-answer"
	first, err := e.command(command)
	if err != nil {
		t.Fatal(err)
	}
	thread := waitTurn(t, e, threadID, "prompt-start")
	accepted := thread.Requests[0]
	if accepted.State != "closed" || accepted.Delivery != "acp-unconfirmed" || !reflect.DeepEqual(accepted.QuestionAnswers, command.QuestionAnswers) {
		t.Fatalf("unexpected native answer state: %+v", accepted)
	}
	markers := questionHistoryMarkers(thread, request.ID)
	if len(markers) != 1 || markers[0].RequestID != request.ID || markers[0].TurnID != accepted.TurnID {
		t.Fatalf("native answer history marker: %+v", markers)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := questionHistoryMarkers(threadOf(stored, threadID), request.ID); len(got) != 1 {
		t.Fatalf("unconfirmed answer lost its durable history anchor: %+v", got)
	}
	again, err := e.command(command)
	if err != nil || again != first {
		t.Fatalf("retry changed the accepted response: %+v %v", again, err)
	}
	if got := questionHistoryMarkers(threadOf(e.current(), threadID), request.ID); len(got) != 1 {
		t.Fatalf("native retry duplicated answer history: %+v", got)
	}
}
