package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Fail precisely the approval response write, after durable response preparation.
// This exercises the SDK's lack of a response-write receipt, not a lost command
// receipt (which is covered separately).
type approvalResponseDropper struct {
	io.Writer
	disconnect func()
}

func (w approvalResponseDropper) Write(data []byte) (int, error) {
	var message struct {
		Result *struct {
			Outcome *struct {
				Outcome string `json:"outcome"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &message) == nil && message.Result != nil && message.Result.Outcome != nil && message.Result.Outcome.Outcome == "selected" {
		w.disconnect()
		return 0, io.ErrClosedPipe
	}
	return w.Writer.Write(data)
}

func TestApprovalResponseWriteFailureIsUncertainAndNeverReplayed(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.dropApprovalResponse = true
	id, request := pendingApprovalThread(t, e, "ask permission")
	c := protocol.Command{Version: 1, ID: "answer", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, ApprovalChoiceID: "allow-once"}
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "uncertain response after failed write", func(s protocol.Snapshot) bool {
		thread := threadOf(s, id)
		return thread.State == "failed" && thread.Requests[0].Delivery == "acp-uncertain"
	})
	accepted := threadOf(s, id).Requests[0]
	if accepted.State != "closed" || accepted.SubmissionID != c.ID || accepted.ApprovalChoiceID != c.ApprovalChoiceID || len(accepted.Answers) != 1 {
		t.Fatalf("failed write lost accepted snapshot: %+v", accepted)
	}
	// Simulate a server restart against the persisted command receipt. A retry
	// must return that receipt even though no corresponding live callback exists.
	recovered, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	recoverThreads(&recovered)
	restarted := newEngine(recovered, e.store)
	if again, err := restarted.command(c); err != nil || again != receipt {
		t.Fatalf("retry after restart: %+v %v", again, err)
	}
	if len(restarted.runs) != 0 || threadOf(restarted.current(), id).Requests[0].Delivery != "acp-uncertain" {
		t.Fatal("receipt recovery replayed an uncertain answer")
	}
}

func approvalSnapshot() protocol.Snapshot {
	return protocol.Snapshot{Threads: []protocol.Thread{{ID: "thread", AgentID: "claude", State: "waiting", TurnID: "turn", Requests: []protocol.Request{{
		ID: "approval", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1,
		TurnID: "turn", DeliveryRoute: "native-response", Choices: []string{"Allow", "Allow"}, ChoiceIDs: []string{"once", "always"},
	}}}}}
}

func pendingApprovalThread(t *testing.T, e *engine, text string) (string, protocol.Request) {
	t.Helper()
	id := startACPThread(t, e, "start", text)
	s := waitFor(t, e, "pending approval", func(s protocol.Snapshot) bool {
		thread := threadOf(s, id)
		return len(thread.Requests) == 1 && thread.State == "waiting"
	})
	return id, threadOf(s, id).Requests[0]
}

func TestApprovalCompetingClientsAndLostCommandReceipt(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id, request := pendingApprovalThread(t, e, "ask permission with duplicate labels")
	commands := []protocol.Command{
		{Version: 1, ID: "client-a", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, ApprovalChoiceID: "allow-once"},
		{Version: 1, ID: "client-b", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, ApprovalChoiceID: "reject"},
	}
	type result struct {
		receipt protocol.Receipt
		err     error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range commands {
		wg.Go(func() { results[i].receipt, results[i].err = e.command(commands[i]) })
	}
	wg.Wait()
	winner := 0
	if results[0].err != nil {
		winner = 1
	}
	if results[winner].err != nil || results[1-winner].err == nil {
		t.Fatalf("race results: %+v", results)
	}
	thread := waitTurn(t, e, id, "prompt-start")
	want := commands[winner]
	if got := activityOf(thread, "agent-"+thread.TurnID).Text; got != "decision "+want.ApprovalChoiceID {
		t.Fatalf("wrong permission scope reached peer: %q", got)
	}
	if receipt, err := e.command(want); err != nil || receipt != results[winner].receipt {
		t.Fatalf("lost receipt retry: %+v %v", receipt, err)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	accepted := threadOf(stored, id).Requests[0]
	if accepted.SubmissionID != want.ID || accepted.SubmittedRevision != request.Revision || accepted.ApprovalChoiceID != want.ApprovalChoiceID || accepted.Delivery != "acp-unconfirmed" || accepted.State != "closed" {
		t.Fatalf("durable answer: %+v", accepted)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if len(prompts) != 1 || len(thread.Requests) != 1 {
		t.Fatal("approval retry created additional work")
	}
}

func TestApprovalStopAndDisconnectDoNotGrant(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "disconnect"}[disconnect], func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			id, request := pendingApprovalThread(t, e, "ask permission")
			if disconnect {
				fleet.last().stop()
			} else if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: id}); err != nil {
				t.Fatal(err)
			}
			s := waitFor(t, e, "closed approval", func(s protocol.Snapshot) bool { return threadOf(s, id).Requests[0].State == "closed" })
			r := threadOf(s, id).Requests[0]
			if len(r.Answers) != 0 || r.SubmissionID != "" || r.ApprovalChoiceID != "" || (r.Delivery != "acp-cancelled" && r.Delivery != "acp-undeliverable") {
				t.Fatalf("cancellation granted consent: %+v", r)
			}
			if _, err := e.command(protocol.Command{Version: 1, ID: "late", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, ApprovalChoiceID: "allow-once"}); err == nil {
				t.Fatal("late answer accepted")
			}
		})
	}
}

func TestApprovalRequiresOriginalLiveCallback(t *testing.T) {
	e, _, _ := acpEngine(t)
	id, request := pendingApprovalThread(t, e, "ask permission")
	e.mu.Lock()
	r := e.runs[id]
	r.mu.Lock()
	callback := r.pending[request.ID]
	delete(r.pending, request.ID)
	r.mu.Unlock()
	e.mu.Unlock()
	c := protocol.Command{Version: 1, ID: "orphan-answer", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, ApprovalChoiceID: "allow-once"}
	if _, err := e.command(c); err == nil {
		t.Fatal("orphan request accepted")
	}
	if got := threadOf(e.current(), id).Requests[0]; got.State != "pending" || got.SubmissionID != "" {
		t.Fatalf("rejected answer changed state: %+v", got)
	}
	r.mu.Lock()
	r.pending[request.ID] = callback
	generation := r.generation
	r.mu.Unlock()
	var p acp.RequestPermissionRequest
	if err := json.Unmarshal([]byte(`{"sessionId":"session-fake","toolCall":{"toolCallId":"stale"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	for _, staleGeneration := range []bool{true, false} {
		h := &acpHandler{acpRun: r, generation: generation}
		if staleGeneration {
			h.generation = "previous-connection"
		} else {
			p.SessionId = "different-session"
		}
		response, err := h.RequestPermission(context.Background(), p)
		if err != nil || response.Outcome.Cancelled == nil || len(threadOf(e.current(), id).Requests) != 1 {
			t.Fatalf("stale callback reused: %+v %v", response, err)
		}
		update := agent.DecodeUpdate(json.RawMessage(`{"sessionUpdate":"session_info_update","title":"Stale update"}`))
		if err := h.SessionUpdate(context.Background(), string(p.SessionId), update); err != nil {
			t.Fatal(err)
		}
		if threadOf(e.current(), id).Title == "Stale update" {
			t.Fatal("stale connection or session update mutated the current thread")
		}
	}
}

func TestApprovalAcceptanceIsNotDelivery(t *testing.T) {
	s := approvalSnapshot()
	c := protocol.Command{ID: "submission", Kind: "request.answer", ThreadID: "thread", TargetID: "approval", Revision: 1, ApprovalChoiceID: "once"}
	if _, err := apply(&s, c); err != nil {
		t.Fatal(err)
	}
	r := s.Threads[0].Requests[0]
	if r.State != "submitted" || r.Delivery != "acp-accepted" || r.SubmissionID != c.ID || r.SubmittedRevision != 1 || r.ApprovalChoiceID != "once" || !reflect.DeepEqual(r.Answers, []string{"Allow"}) {
		t.Fatalf("accepted snapshot: %+v", r)
	}
	if s.Threads[0].State != "waiting" {
		t.Fatal("local acceptance resumed execution before handoff")
	}
	c.ID = "competing-client"
	if _, err := apply(&s, c); err == nil {
		t.Fatal("second submission accepted")
	}
}

func TestApprovalRejectsAmbiguousAndMixedAnswers(t *testing.T) {
	for _, c := range []protocol.Command{
		{Answers: []string{"Allow"}},
		{ApprovalChoiceID: "missing"},
		{ApprovalChoiceID: "once", Answers: []string{"Allow"}},
		{ApprovalChoiceID: "once", QuestionAnswers: []protocol.Answer{{Text: "Allow"}}},
	} {
		s := approvalSnapshot()
		c.Kind, c.ThreadID, c.TargetID, c.Revision = "request.answer", "thread", "approval", 1
		if _, err := apply(&s, c); err == nil {
			t.Fatalf("invalid response accepted: %+v", c)
		}
		if s.Threads[0].Requests[0].State != "pending" {
			t.Fatal("invalid response changed request")
		}
	}
}

func TestACPQuestionWithoutDeliveryRouteIsRejected(t *testing.T) {
	s := approvalSnapshot()
	r := &s.Threads[0].Requests[0]
	r.Kind, r.Questions = "question", []protocol.Question{{ID: "q", Kind: "text"}}
	c := protocol.Command{Kind: "request.answer", ThreadID: "thread", TargetID: "approval", Revision: 1, QuestionAnswers: []protocol.Answer{{Text: "answer"}}}
	_, err := apply(&s, c)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "unsupported_request" || r.State != "pending" || len(r.QuestionAnswers) != 0 {
		t.Fatalf("unsupported route: request=%+v error=%v", r, err)
	}
}

func TestACPDeliveryRecoveryNeverReplaysOrConfirms(t *testing.T) {
	for _, delivery := range []string{"acp-accepted", "acp-unconfirmed", "acp-delivered"} {
		t.Run(delivery, func(t *testing.T) {
			s := approvalSnapshot()
			r := &s.Threads[0].Requests[0]
			r.State, r.Delivery, r.SubmissionID = "submitted", delivery, "answer-command"
			r.Answers, r.ApprovalChoiceID = []string{"Allow"}, "once"
			recoverThreads(&s)
			r = &s.Threads[0].Requests[0]
			if r.State != "closed" || r.Delivery != "acp-uncertain" || r.SubmissionID != "answer-command" || r.ApprovalChoiceID != "once" || !reflect.DeepEqual(r.Answers, []string{"Allow"}) || !s.Threads[0].NeedsResume {
				t.Fatalf("recovery changed or confirmed answer: %+v", r)
			}
			before := *r
			recoverThreads(&s)
			if !reflect.DeepEqual(before, s.Threads[0].Requests[0]) {
				t.Fatal("repeated recovery changed terminal record")
			}
		})
	}
}
