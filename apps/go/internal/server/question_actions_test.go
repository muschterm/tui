package server

import (
	"errors"
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func nativeRequestAction(id string, r protocol.Request, action string) protocol.Command {
	return protocol.Command{Version: 1, ID: action + "-command", Kind: "request.answer", ThreadID: id, TargetID: r.ID, Revision: r.Revision, RequestAction: action}
}

// A chosen decline or cancel reaches the peer as the bare elicitation action,
// with the same acceptance, dedupe and unconfirmed delivery as an answer.
func TestNativeQuestionDeclineAndCancelReachPeerWithoutContent(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			fleet.nativeQuestions = true
			id, r := pendingNativeQuestion(t, e)
			if !reflect.DeepEqual(r.Actions, []string{"decline", "cancel"}) {
				t.Fatalf("offered actions: %+v", r.Actions)
			}
			c := nativeRequestAction(id, r, action)
			first, err := e.command(c)
			if err != nil {
				t.Fatal(err)
			}
			thread := waitTurn(t, e, id, "prompt-start")
			got := thread.Requests[0]
			if got.State != "closed" || got.Delivery != "acp-unconfirmed" || got.Action != action || got.SubmissionID != c.ID || len(got.QuestionAnswers) != 0 || len(got.Answers) != 0 {
				t.Fatalf("recorded %s: %+v", action, got)
			}
			if again, err := e.command(c); err != nil || again != first {
				t.Fatalf("duplicate submission: %+v %v", again, err)
			}
			if markers := questionHistoryMarkers(threadOf(e.current(), id), r.ID); len(markers) != 1 {
				t.Fatalf("history markers: %+v", markers)
			}
			stored, _, err := e.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if threadOf(stored, id).Requests[0].Action != action {
				t.Fatal("chosen action was not durable")
			}
			f := fleet.last()
			f.mu.Lock()
			defer f.mu.Unlock()
			if !reflect.DeepEqual(f.questionResponse, map[string]any{"action": action}) {
				t.Fatalf("peer received %+v", f.questionResponse)
			}
			if _, exists := f.questionResponse["content"]; exists {
				t.Fatal("decline carried content")
			}
		})
	}
}

func TestNativeQuestionActionGuards(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, r := pendingNativeQuestion(t, e)
	var pe *protocol.Error
	for name, c := range map[string]protocol.Command{
		"stale revision": func() protocol.Command { c := nativeRequestAction(id, r, "decline"); c.Revision++; return c }(),
		"unknown action": nativeRequestAction(id, r, "retry"),
		"with answers": func() protocol.Command {
			c := nativeRequestAction(id, r, "decline")
			c.QuestionAnswers = []protocol.Answer{{Choices: []string{"Small"}}}
			return c
		}(),
		"with legacy": func() protocol.Command {
			c := nativeRequestAction(id, r, "cancel")
			c.Answers = []string{"Small"}
			return c
		}(),
		"approval choice": func() protocol.Command {
			c := nativeRequestAction(id, r, "decline")
			c.ApprovalChoiceID = "x"
			return c
		}(),
	} {
		c.ID = "guard-" + name
		if _, err := e.command(c); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if got := threadOf(e.current(), id).Requests[0]; got.State != "pending" || got.Action != "" {
		t.Fatalf("rejected actions changed the request: %+v", got)
	}
	// A competing client's decline after an accepted answer is stale.
	if _, err := e.command(nativeAnswer(id, r)); err != nil {
		t.Fatal(err)
	}
	_, err := e.command(nativeRequestAction(id, r, "decline"))
	if !errors.As(err, &pe) || pe.Code != "stale_request" {
		t.Fatalf("late decline: %v", err)
	}
	waitTurn(t, e, id, "prompt-start")
}

// Codex's request_user_input response has no decline or cancel result, so its
// questions offer none and the server refuses one.
func TestCodexQuestionOffersNoDecline(t *testing.T) {
	e, f, _ := acpEngine(t)
	f.codexQuestions, f.questionBlocking = true, true
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
	r := threadOf(s, id).Requests[0]
	if len(r.Actions) != 0 {
		t.Fatalf("Codex question offered %v", r.Actions)
	}
	var pe *protocol.Error
	for _, action := range []string{"decline", "cancel"} {
		if _, err := e.command(nativeRequestAction(id, r, action)); !errors.As(err, &pe) || pe.Code != "unsupported_action" {
			t.Fatalf("Codex %s: %v", action, err)
		}
	}
	if _, err := e.command(nativeAnswer(id, r)); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, id, "prompt-start")
}

func TestFixtureQuestionDeclineResolvesLikeAnswer(t *testing.T) {
	e := testEngine(t)
	thread := threadOf(e.current(), "thread-review")
	var r protocol.Request
	for _, request := range thread.Requests {
		if request.ID == "question-blocking" {
			r = request
		}
	}
	if !reflect.DeepEqual(r.Actions, []string{"decline", "cancel"}) || len(r.Questions[0].OptionDescriptions) != len(r.Questions[0].Options) {
		t.Fatalf("fixture request: %+v", r)
	}
	c := protocol.Command{Version: 1, ID: "fixture-decline", Kind: "request.answer", ThreadID: "thread-review", TargetID: r.ID, Revision: r.Revision, RequestAction: "decline"}
	first, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.command(c); err != nil || again != first {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	thread = threadOf(e.current(), "thread-review")
	for _, request := range thread.Requests {
		if request.ID == r.ID && (request.State != "resolved" || request.Delivery != "fixture-confirmed" || request.Action != "decline" || len(request.QuestionAnswers) != 0) {
			t.Fatalf("fixture decline: %+v", request)
		}
	}
	if markers := questionHistoryMarkers(thread, r.ID); len(markers) != 1 {
		t.Fatalf("history markers: %+v", markers)
	}
	// The asynchronous fixture question offers no actions.
	async := protocol.Command{Version: 1, ID: "fixture-async-decline", Kind: "request.answer", ThreadID: "thread-shell", TargetID: "question-async", Revision: 1, RequestAction: "decline"}
	if _, err := e.command(async); err == nil {
		t.Fatal("decline accepted where none is offered")
	}
}
