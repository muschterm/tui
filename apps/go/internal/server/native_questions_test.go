package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Exact single-question AskUserQuestion form emitted by the pinned adapter.
func nativeQuestionWire(session string) map[string]any {
	return map[string]any{
		"mode": "form", "sessionId": session, "toolCallId": "question-call", "message": "Which approach?",
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{
			"question_0": map[string]any{"type": "string", "title": "Approach", "oneOf": []any{
				map[string]any{"const": "Small", "title": "Small", "description": "A bounded change"},
				map[string]any{"const": "Broad", "title": "Broad", "description": "A larger change"},
			}},
			"question_0_custom": map[string]any{"type": "string", "title": "Other", "description": "Type your own answer, or add a note to the option you chose above (optional).", "_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"questionId": "question_0", "isCustomAnswer": true}}},
		}},
	}
}

func pendingNativeQuestion(t *testing.T, e *engine) (string, protocol.Request) {
	t.Helper()
	id := startACPThread(t, e, "start", "ask native question")
	s := waitFor(t, e, "native question", func(s protocol.Snapshot) bool {
		th := threadOf(s, id)
		return th.State == "waiting" && len(th.Requests) == 1
	})
	return id, threadOf(s, id).Requests[0]
}
func nativeAnswer(id string, r protocol.Request) protocol.Command {
	return protocol.Command{Version: 1, ID: "answer", Kind: "request.answer", ThreadID: id, TargetID: r.ID, Revision: r.Revision, QuestionAnswers: []protocol.Answer{{Choices: []string{"Small"}}}}
}

func TestNativeQuestionAcceptanceIsNotDelivery(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, r := pendingNativeQuestion(t, e)
	c := nativeAnswer(id, r)
	snapshot := e.current()
	if _, err := apply(&snapshot, c); err != nil {
		t.Fatal(err)
	}
	accepted := threadOf(snapshot, id).Requests[0]
	if accepted.State != "submitted" || accepted.Delivery != "acp-accepted" || accepted.SubmissionID != c.ID || accepted.SubmittedRevision != r.Revision || threadOf(snapshot, id).State != "waiting" {
		t.Fatalf("premature delivery: %+v", accepted)
	}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	thread := waitTurn(t, e, id, "prompt-start")
	accepted = thread.Requests[0]
	if accepted.State != "closed" || accepted.Delivery != "acp-unconfirmed" || !reflect.DeepEqual(accepted.QuestionAnswers, c.QuestionAnswers) || len(accepted.SourcePayload) == 0 {
		t.Fatalf("generic completion confirmed or lost answer: %+v", accepted)
	}
	f := fleet.last()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.elicitationForm {
		t.Fatal("initialize did not opt into form elicitation")
	}
	if !reflect.DeepEqual(f.questionResponse, map[string]any{"action": "accept", "content": map[string]any{"question_0": "Small"}}) {
		t.Fatalf("wrong response: %+v", f.questionResponse)
	}
}

func TestNativeQuestionCompetingClientsAndLostReceipt(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, r := pendingNativeQuestion(t, e)
	a, b := nativeAnswer(id, r), nativeAnswer(id, r)
	a.ID = "client-a"
	b.ID = "client-b"
	b.QuestionAnswers = []protocol.Answer{{Text: "Custom approach"}}
	commands := []protocol.Command{a, b}
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
		t.Fatalf("race: %+v", results)
	}
	thread := waitTurn(t, e, id, "prompt-start")
	if receipt, err := e.command(commands[winner]); err != nil || receipt != results[winner].receipt {
		t.Fatalf("retry: %+v %v", receipt, err)
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	accepted := threadOf(stored, id).Requests[0]
	if accepted.SubmissionID != commands[winner].ID || accepted.SubmittedRevision != r.Revision || !reflect.DeepEqual(accepted.QuestionAnswers, commands[winner].QuestionAnswers) || accepted.Delivery != "acp-unconfirmed" {
		t.Fatalf("durable answer: %+v", accepted)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if len(prompts) != 1 || len(thread.Requests) != 1 {
		t.Fatal("retry created new work")
	}
	f := fleet.last()
	f.mu.Lock()
	defer f.mu.Unlock()
	key, value := "question_0", "Small"
	if winner == 1 {
		key, value = "question_0_custom", "Custom approach"
	}
	if !reflect.DeepEqual(f.questionResponse, map[string]any{"action": "accept", "content": map[string]any{key: value}}) {
		t.Fatalf("wrong winner reached peer: %+v", f.questionResponse)
	}
}

func TestNativeQuestionWithdrawal(t *testing.T) {
	for _, action := range []string{"stop", "disconnect", "peer cancellation"} {
		t.Run(action, func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			fleet.nativeQuestions = true
			id, r := pendingNativeQuestion(t, e)
			switch action {
			case "stop":
				if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: id}); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				fleet.last().stop()
			case "peer cancellation":
				f := fleet.last()
				f.mu.Lock()
				cancel := f.questionCancel
				f.mu.Unlock()
				cancel()
			}
			s := waitFor(t, e, "withdrawn question", func(s protocol.Snapshot) bool { return threadOf(s, id).Requests[0].State == "closed" })
			got := threadOf(s, id).Requests[0]
			if len(got.QuestionAnswers) != 0 || got.SubmissionID != "" || (got.Delivery != "acp-cancelled" && got.Delivery != "acp-undeliverable") {
				t.Fatalf("withdrawal accepted answer: %+v", got)
			}
			if _, err := e.command(nativeAnswer(id, r)); err == nil {
				t.Fatal("late answer accepted")
			}
		})
	}
}

func TestNativeQuestionRequiresOriginalLiveCallback(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, request := pendingNativeQuestion(t, e)
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	r.mu.Lock()
	callback := r.questions[request.ID]
	delete(r.questions, request.ID)
	generation := r.generation
	r.mu.Unlock()
	if _, err := e.command(nativeAnswer(id, request)); err == nil {
		t.Fatal("missing callback accepted")
	}
	if got := threadOf(e.current(), id).Requests[0]; got.State != "pending" || got.SubmissionID != "" {
		t.Fatalf("rejected submission mutated question: %+v", got)
	}
	r.mu.Lock()
	r.questions[request.ID] = callback
	r.mu.Unlock()
	for _, stale := range []string{"generation", "session"} {
		h := &acpHandler{acpRun: r, generation: generation, questions: true}
		session := "session-fake"
		if stale == "generation" {
			h.generation = "previous"
		} else {
			session = "other-session"
		}
		raw, _ := json.Marshal(nativeQuestionWire(session))
		response, err := h.CreateElicitation(context.Background(), raw)
		if err != nil || response["action"] != "cancel" || len(threadOf(e.current(), id).Requests) != 1 {
			t.Fatalf("stale %s reused: %+v %v", stale, response, err)
		}
	}
}

type questionResponseDropper struct {
	io.Writer
	disconnect func()
}

func (w questionResponseDropper) Write(data []byte) (int, error) {
	var m struct {
		Result *struct {
			Action string `json:"action"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &m) == nil && m.Result != nil && m.Result.Action == "accept" {
		w.disconnect()
		return 0, io.ErrClosedPipe
	}
	return w.Writer.Write(data)
}
func TestNativeQuestionWriteFailureIsUncertainAndNeverReplayed(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	fleet.dropQuestionResponse = true
	id, r := pendingNativeQuestion(t, e)
	c := nativeAnswer(id, r)
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "uncertain native response", func(s protocol.Snapshot) bool {
		th := threadOf(s, id)
		return th.State == "failed" && th.Requests[0].Delivery == "acp-uncertain"
	})
	got := threadOf(s, id).Requests[0]
	if got.State != "closed" || got.SubmissionID != c.ID || !reflect.DeepEqual(got.QuestionAnswers, c.QuestionAnswers) {
		t.Fatalf("lost accepted answer: %+v", got)
	}
	recovered, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	recoverThreads(&recovered)
	restarted := newEngine(recovered, e.store)
	if again, err := restarted.command(c); err != nil || again != receipt {
		t.Fatalf("restart retry: %+v %v", again, err)
	}
	if len(restarted.runs) != 0 || threadOf(restarted.current(), id).Requests[0].Delivery != "acp-uncertain" {
		t.Fatal("uncertain response replayed")
	}
}

func TestNativeQuestionRejectsUnpinnedPeer(t *testing.T) {
	for _, version := range []string{"unrelated", "0.80.1"} {
		t.Run(version, func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			if version != "unrelated" {
				fleet.nativeQuestions = true
				fleet.nativeVersion = version
			}
			id := startACPThread(t, e, "start", "ask native question")
			s := waitFor(t, e, "unsupported question failure", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
			if len(threadOf(s, id).Requests) != 0 {
				t.Fatal("unsupported peer created actionable questions")
			}
		})
	}
}

func TestNativeQuestionSourceRemainsPrivateAndDurable(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, request := pendingNativeQuestion(t, e)
	source := append(json.RawMessage(nil), request.SourcePayload...)
	snapshot := e.current()
	projected := clientSnapshot(snapshot)
	if len(threadOf(projected, id).Requests[0].SourcePayload) != 0 {
		t.Fatal("client projection exposed raw provider schema")
	}
	if !reflect.DeepEqual(threadOf(snapshot, id).Requests[0].SourcePayload, source) {
		t.Fatal("projection mutated authoritative source")
	}
	stored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(threadOf(stored, id).Requests[0].SourcePayload, source) {
		t.Fatal("durable source changed")
	}
	if len(threadOf(projected, id).Requests[0].Questions) != 1 {
		t.Fatal("projection lost normalized questions")
	}
}

func TestNativeQuestionRejectsUnsafeAnswerBeforeAcceptance(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, r := pendingNativeQuestion(t, e)
	c := nativeAnswer(id, r)
	c.QuestionAnswers = []protocol.Answer{{Text: "unsafe\x1b[31m"}}
	if _, err := e.command(c); err == nil {
		t.Fatal("unsafe native answer accepted")
	}
	got := threadOf(e.current(), id).Requests[0]
	if got.State != "pending" || got.SubmissionID != "" || len(got.QuestionAnswers) != 0 {
		t.Fatalf("rejection retained invalid answer: %+v", got)
	}
	c.QuestionAnswers = []protocol.Answer{{Text: "Safe custom answer"}}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, id, "prompt-start")
}

func TestNativeQuestionRejectsRepeatedToolAndMalformedForm(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, _ := pendingNativeQuestion(t, e)
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	r.mu.Lock()
	generation := r.generation
	r.mu.Unlock()
	h := &acpHandler{acpRun: r, generation: generation, questions: true}
	for _, malformed := range []bool{false, true} {
		wire := nativeQuestionWire("session-fake")
		if malformed {
			wire["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"unknown": map[string]any{"type": "string"}}}
		}
		raw, _ := json.Marshal(wire)
		if _, err := h.CreateElicitation(context.Background(), raw); err == nil {
			t.Fatalf("accepted repeated/malformed form (malformed=%v)", malformed)
		}
		if len(threadOf(e.current(), id).Requests) != 1 {
			t.Fatal("invalid form mutated requests")
		}
	}
}

func TestNativeQuestionFailedAdmissionDoesNotPublish(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, _ := pendingNativeQuestion(t, e)
	e.mu.Lock()
	r := e.runs[id]
	before := clone(e.snap)
	updates := make(chan protocol.Snapshot, 1)
	e.subscribers[updates] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.subscribers, updates); e.mu.Unlock() }()
	r.mu.Lock()
	generation := r.generation
	callbacks := len(r.questions)
	r.mu.Unlock()
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	h := &acpHandler{acpRun: r, generation: generation, questions: true}
	wire := nativeQuestionWire("session-fake")
	wire["toolCallId"] = "uncommittable-question"
	raw, _ := json.Marshal(wire)
	response, err := h.CreateElicitation(context.Background(), raw)
	if err == nil || response["action"] != "cancel" {
		t.Fatalf("failed storage admission: %+v %v", response, err)
	}
	if !reflect.DeepEqual(before, e.current()) {
		t.Fatal("failed admission changed authoritative snapshot")
	}
	select {
	case <-updates:
		t.Fatal("failed admission published a client snapshot")
	default:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.questions) != callbacks {
		t.Fatal("failed admission installed a live callback")
	}
}

func TestNativeQuestionReservationSurvivesQueuedWorkAndMaximumLegacyAnswer(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.nativeQuestions = true
	id, _ := pendingNativeQuestion(t, e)
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	r.mu.Lock()
	generation := r.generation
	r.mu.Unlock()
	h := &acpHandler{acpRun: r, generation: generation, questions: true}
	wire := nativeQuestionWire("session-fake")
	wire["toolCallId"], wire["message"] = "four-question-call", "Please answer the following questions."
	properties := map[string]any{}
	for i := range 4 {
		source := nativeQuestionWire("session-fake")["requestedSchema"].(map[string]any)["properties"].(map[string]any)
		key := fmt.Sprintf("question_%d", i)
		field := source["question_0"].(map[string]any)
		field["description"] = fmt.Sprintf("Question number %d?", i)
		custom := source["question_0_custom"].(map[string]any)
		custom["_meta"] = map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"questionId": key, "isCustomAnswer": true}}
		properties[key], properties[key+"_custom"] = field, custom
	}
	wire["requestedSchema"] = map[string]any{"type": "object", "properties": properties}
	raw, _ := json.Marshal(wire)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		response map[string]any
		err      error
	}
	done := make(chan result, 1)
	go func() { response, err := h.CreateElicitation(ctx, raw); done <- result{response, err} }()
	s := waitFor(t, e, "second pending native question", func(s protocol.Snapshot) bool { return len(threadOf(s, id).Requests) == 2 })
	question := threadOf(s, id).Requests[1]
	// Fill otherwise inert transcript history. Keep just enough unreserved room
	// for one competing queued prompt; both answer reservations must survive it.
	e.mu.Lock()
	thread := threadByID(&e.snap, id)
	thread.Activity = append(thread.Activity, protocol.Activity{ID: "retained-history", Text: ""})
	remaining := snapshotLimit - projectedSize(e.snap) - 12000
	if remaining < 0 {
		e.mu.Unlock()
		t.Fatal("unexpected fixture size")
	}
	thread.Activity[len(thread.Activity)-1].Text = strings.Repeat("x", remaining)
	e.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "small-competing-prompt", Kind: "prompt.send", ThreadID: id, Text: strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	// This would fit the encoded snapshot alone, but would consume pending
	// questions' reserved answer space if reservations were only admission-time.
	_, err := e.command(protocol.Command{Version: 1, ID: "large-competing-prompt", Kind: "prompt.send", ThreadID: id, Text: strings.Repeat("x", 16000)})
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "capacity" {
		t.Fatalf("competing work consumed reserved space: %v", err)
	}
	maxAnswer := strings.Repeat("<", 4096) // JSON expands each byte to six bytes.
	command := protocol.Command{Version: 1, ID: "maximum-legacy-answer", Kind: "request.answer", ThreadID: id, TargetID: question.ID, Revision: question.Revision, Answers: []string{maxAnswer, maxAnswer, maxAnswer, maxAnswer}}
	if _, err := e.command(command); err != nil {
		t.Fatalf("reserved maximum answer rejected: %v", err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.response["action"] != "accept" {
			t.Fatalf("maximum response failed: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("question callback did not resolve")
	}
	snapshot := e.current()
	accepted := threadOf(snapshot, id).Requests[1]
	if accepted.Delivery != "acp-unconfirmed" || len(accepted.QuestionAnswers) != 4 || len(accepted.Answers) != 0 {
		t.Fatalf("legacy answer duplicated or lost: state=%s delivery=%s structured=%d legacy=%d", accepted.State, accepted.Delivery, len(accepted.QuestionAnswers), len(accepted.Answers))
	}
	for _, answer := range accepted.QuestionAnswers {
		if answer.Text != maxAnswer {
			t.Fatal("maximum answer changed")
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if len(encoded) > snapshotLimit || projectedSize(snapshot) > snapshotLimit {
		t.Fatalf("accepted answer exceeded bound: encoded=%d projected=%d", len(encoded), projectedSize(snapshot))
	}
	if len(threadOf(snapshot, id).Queue) != 1 {
		t.Fatal("competing queue changed")
	}
}
