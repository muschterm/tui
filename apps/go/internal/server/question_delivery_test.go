package server

import (
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// answeredCodexQuestion runs one built-in Codex question turn to completion.
func answeredCodexQuestion(t *testing.T, fleet func(*fakeFleet)) (protocol.Thread, protocol.Command) {
	t.Helper()
	e, f, _ := acpEngine(t)
	f.codexQuestions = true
	fleet(f)
	codex := agent.Find(&e.snap, "codex")
	codex.State, codex.Options = agent.StateReady, fakeOptions(t, nil)
	codex.Fields = agent.Fields(codex.Options)
	settings := fakeSettings()
	receipt, err := e.command(protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-acp", Agent: "codex", Settings: &settings, Text: "ask native question"})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.TargetID
	s := waitFor(t, e, "Codex question", func(s protocol.Snapshot) bool { return len(threadOf(s, id).Requests) == 1 })
	c := nativeAnswer(id, threadOf(s, id).Requests[0])
	if _, err = e.command(c); err != nil {
		t.Fatal(err)
	}
	return waitTurn(t, e, id, "prompt-start"), c
}

func TestBlockingAnswerReceiptThenEndTurnIsAnswered(t *testing.T) {
	thread, c := answeredCodexQuestion(t, func(f *fakeFleet) { f.questionBlocking, f.questionReceipt = true, true })
	r := thread.Requests[0]
	if thread.State != "idle" || r.Mode != "blocking" || r.State != "resolved" || r.Delivery != "acp-turn-confirmed" || r.SubmissionID != c.ID || !reflect.DeepEqual(r.QuestionAnswers, c.QuestionAnswers) {
		t.Fatalf("delivered blocking answer not settled: thread=%s request=%+v", thread.State, r)
	}
}

func TestBlockingAnswerReceiptThenTurnErrorIsUncertain(t *testing.T) {
	thread, c := answeredCodexQuestion(t, func(f *fakeFleet) { f.questionBlocking, f.questionReceipt, f.questionFail = true, true, true })
	r := thread.Requests[0]
	if thread.State != "failed" || r.State != "closed" || r.Delivery != "acp-uncertain" || r.SubmissionID != c.ID {
		t.Fatalf("failed turn confirmed an answer: thread=%s request=%+v", thread.State, r)
	}
}

func TestAnswerWithoutReceiptOrContinuedWorkIsNotConfirmed(t *testing.T) {
	for name, set := range map[string]func(*fakeFleet){
		// Turn completion alone: the bridge may have refused or dropped it.
		"blocking without receipt": func(f *fakeFleet) { f.questionBlocking = true },
		// A continued-work turn can finish while its question is still open.
		"async with receipt": func(f *fakeFleet) { f.questionReceipt = true },
	} {
		t.Run(name, func(t *testing.T) {
			thread, _ := answeredCodexQuestion(t, set)
			if r := thread.Requests[0]; thread.State != "idle" || r.State != "closed" || r.Delivery != "acp-unconfirmed" {
				t.Fatalf("answer confirmed without evidence: %+v", r)
			}
		})
	}
}

func TestDeliveryReceiptFromUnpinnedAdapterIsIgnored(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions, fleet.questionReceipt = true, true
	id, r := pendingNativeQuestion(t, e)
	if _, err := e.command(nativeAnswer(id, r)); err != nil {
		t.Fatal(err)
	}
	thread := waitTurn(t, e, id, "prompt-start")
	if got := thread.Requests[0]; got.State != "closed" || got.Delivery != "acp-unconfirmed" {
		t.Fatalf("external adapter receipt trusted: %+v", got)
	}
	for _, a := range thread.Activity {
		if a.Title == "tui_question_delivery" {
			t.Fatal("receipt leaked into the transcript")
		}
	}
}

// restartThread is an ACP thread whose question turn has the given outcome.
func restartThread(state, promptState, delivery string) protocol.Snapshot {
	prompt := protocol.Prompt{ID: "turn", Revision: 1, Text: "ask"}
	request := protocol.Request{ID: "question", Kind: "question", Mode: "blocking", State: "closed", Revision: 3, TurnID: "turn", DeliveryRoute: "native-response", Delivery: delivery, SubmissionID: "answer", SubmittedRevision: 1, QuestionAnswers: []protocol.Answer{{Text: "Small"}}}
	if delivery == "acp-turn-confirmed" {
		request.State = "resolved"
	}
	return protocol.Snapshot{Threads: []protocol.Thread{{ID: "thread", AgentID: "codex", State: state, TurnID: "turn", StopReason: "end_turn",
		Activity: []protocol.Activity{{ID: "turn", TurnID: "turn", Role: "user", Prompt: &prompt, State: promptState}},
		Requests: []protocol.Request{request},
	}}}
}

func TestRestartKeepsCompletedTurnDeliveryStatus(t *testing.T) {
	for _, delivery := range []string{"acp-unconfirmed", "acp-turn-confirmed", "acp-delivered"} {
		t.Run(delivery, func(t *testing.T) {
			s := restartThread("idle", "completed", delivery)
			before := s.Threads[0].Requests[0]
			recoverThreads(&s)
			if got := s.Threads[0].Requests[0]; !reflect.DeepEqual(got, before) || s.Threads[0].NeedsResume {
				t.Fatalf("restart rewrote a completed turn's answer: %+v", got)
			}
		})
	}
	// A turn that was cancelled before shutdown recorded its own outcome too.
	s := restartThread("interrupted", "interrupted", "acp-unconfirmed")
	recoverThreads(&s)
	if got := s.Threads[0].Requests[0]; got.Delivery != "acp-unconfirmed" || got.Revision != 3 {
		t.Fatalf("restart rewrote a cancelled turn's answer: %+v", got)
	}
}

func TestRestartDuringAnsweredTurnIsUncertain(t *testing.T) {
	for _, state := range []string{"running", "waiting"} {
		s := restartThread(state, "running", "acp-unconfirmed")
		recoverThreads(&s)
		got, thread := s.Threads[0].Requests[0], s.Threads[0]
		if got.State != "closed" || got.Delivery != "acp-uncertain" || got.Revision != 4 || got.SubmissionID != "answer" || !thread.NeedsResume || thread.State != "interrupted" {
			t.Fatalf("%s: in-flight answer not uncertain: %+v", state, got)
		}
		again := got
		recoverThreads(&s)
		if !reflect.DeepEqual(again, s.Threads[0].Requests[0]) {
			t.Fatal("repeated recovery changed an uncertain answer")
		}
	}
}

func TestRestartRepairsCompletedTurnDowngradedByEarlierRestart(t *testing.T) {
	s := restartThread("idle", "completed", "acp-uncertain")
	recoverThreads(&s)
	got := s.Threads[0].Requests[0]
	if got.State != "closed" || got.Delivery != "acp-unconfirmed" || got.Revision != 4 || got.SubmissionID != "answer" {
		t.Fatalf("legacy downgrade not repaired: %+v", got)
	}
	recoverThreads(&s)
	if s.Threads[0].Requests[0].Revision != 4 {
		t.Fatal("repair is not idempotent")
	}
	// Failed, interrupted and trimmed turns keep genuine uncertainty.
	for _, promptState := range []string{"failed", "interrupted", ""} {
		s := restartThread("idle", promptState, "acp-uncertain")
		if promptState == "" {
			s.Threads[0].Activity = nil
		}
		recoverThreads(&s)
		if got := s.Threads[0].Requests[0]; got.Delivery != "acp-uncertain" || got.Revision != 3 {
			t.Fatalf("%q: uncertain answer reclassified without evidence: %+v", promptState, got)
		}
	}
}
