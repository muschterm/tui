package server

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func approvalAdmissionWire(t *testing.T) acp.RequestPermissionRequest {
	t.Helper()
	var p acp.RequestPermissionRequest
	if err := json.Unmarshal([]byte(`{"sessionId":"session-fake","toolCall":{"toolCallId":"another-action"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeApprovalAdmissionRejectsWithoutMutation(t *testing.T) {
	for _, reason := range []string{"storage", "retained history", "snapshot capacity", "answer capacity"} {
		t.Run(reason, func(t *testing.T) {
			e, _, _ := acpEngine(t)
			id, _ := pendingApprovalThread(t, e, "ask permission")
			p := approvalAdmissionWire(t)
			// Streamed updates before the approval are persisted by a coalesced
			// flush timer that republishes the unchanged snapshot; wait for it
			// so the subscription below observes only the refused admission.
			quiescent := time.Now().Add(5 * time.Second)
			for e.mu.Lock(); e.dirty || e.flushScheduled; e.mu.Lock() {
				e.mu.Unlock()
				if time.Now().After(quiescent) {
					t.Fatal("pending coalesced flush did not complete")
				}
				time.Sleep(10 * time.Millisecond)
			}
			r := e.runs[id]
			thread := threadByID(&e.snap, id)
			switch reason {
			case "retained history":
				for len(thread.Requests) < 128 {
					thread.Requests = append(thread.Requests, protocol.Request{ID: fmt.Sprintf("history-%d", len(thread.Requests)), Kind: "approval", State: "closed"})
				}
			case "snapshot capacity":
				thread.Activity = append(thread.Activity, protocol.Activity{ID: "history", Text: strings.Repeat("x", snapshotLimit)})
			case "answer capacity":
				// An exact opaque option ID fits in the pending card but its accepted
				// duplicate needs additional space. JSON escaping magnifies this case.
				p.Options[0].OptionId = acp.PermissionOptionId(strings.Repeat("<", 16000))
				thread.Activity = append(thread.Activity, protocol.Activity{ID: "history"})
				remaining := snapshotLimit - projectedSize(e.snap) - 150000
				thread.Activity[len(thread.Activity)-1].Text = strings.Repeat("x", remaining)
			}
			before := clone(e.snap)
			updates := make(chan protocol.Snapshot, 1)
			e.subscribers[updates] = true
			e.mu.Unlock()
			defer func() { e.mu.Lock(); delete(e.subscribers, updates); e.mu.Unlock() }()
			r.mu.Lock()
			generation := r.generation
			callbacks := make(map[string]*pendingApproval, len(r.pending))
			for key, callback := range r.pending {
				callbacks[key] = callback
			}
			r.mu.Unlock()
			if reason == "storage" {
				if err := e.store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			h := &acpHandler{acpRun: r, generation: generation}
			response, err := h.RequestPermission(context.Background(), p)
			if err == nil || response.Outcome.Cancelled == nil {
				t.Fatalf("admission should cancel: %+v %v", response, err)
			}
			if !reflect.DeepEqual(before, e.current()) {
				t.Fatal("refused admission changed authoritative snapshot")
			}
			select {
			case <-updates:
				t.Fatal("refused admission published snapshot")
			default:
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if !reflect.DeepEqual(callbacks, r.pending) {
				t.Fatal("refused admission changed live callbacks")
			}
		})
	}
}

func TestNativeApprovalChoiceReservationCoversEscapedAcceptedState(t *testing.T) {
	before := approvalSnapshot()
	request := &before.Threads[0].Requests[0]
	request.ChoiceIDs = []string{strings.Repeat("<", 16000)}
	request.Choices = []string{strings.Repeat("<", 256)}
	after := clone(before)
	accepted := &after.Threads[0].Requests[0]
	accepted.State, accepted.Delivery = "submitted", "acp-accepted"
	accepted.Revision++
	accepted.SubmissionID = strings.Repeat("<", 128)
	accepted.SubmittedRevision = request.Revision
	accepted.ApprovalChoiceID = request.ChoiceIDs[0]
	accepted.Answers = []string{request.Choices[0]}
	pendingJSON, _ := json.Marshal(before)
	acceptedJSON, _ := json.Marshal(after)
	reserve := nativeApprovalAnswerReserve(*request)
	if growth := len(acceptedJSON) - len(pendingJSON); reserve < growth {
		t.Fatalf("reservation %d smaller than accepted growth %d", reserve, growth)
	}
	if projectedSize(before) < projectedSize(after) {
		t.Fatal("pending choice reservation did not cover accepted snapshot")
	}
}
