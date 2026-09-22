package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// The callback itself is the private upstream request identity: the SDK owns
// the JSON-RPC ID. It can only receive an answer on its original connection.
type pendingApproval struct {
	answer                        chan string
	ctx                           context.Context
	generation, sessionID, turnID string
}

// A fresh handler is bound to each launch, even when session/load restores the
// same upstream session name. An old callback cannot become live again.
type acpHandler struct {
	*acpRun
	generation string
	questions  bool
}

// liveApproval is called with the engine lock held; lock order is engine/run.
func (r *acpRun) liveApproval(t *protocol.Thread, requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending[requestID]
	if p == nil || p.ctx.Err() != nil || r.ctx.Err() != nil || r.interrupted || r.turnDone == nil || r.session == nil || p.generation != r.generation || p.turnID != t.TurnID || p.sessionID != r.session.SessionID() || t.NeedsResume || (t.State != "running" && t.State != "waiting") {
		return false
	}
	select {
	case <-r.session.Done():
		return false
	default:
		return true
	}
}

// RequestPermission registers and durably publishes a bounded approval before
// awaiting the exact supported choice. No timeout or disconnect grants consent.
func (h *acpHandler) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	r, e := h.acpRun, h.e
	cancelled := acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}
	request := agent.PermissionRequest("approval-"+ID(), p)
	if len(request.ChoiceIDs) == 0 {
		return cancelled, nil
	}
	seen := map[string]bool{}
	for _, id := range request.ChoiceIDs {
		if id == "" || seen[id] {
			return cancelled, fmt.Errorf("approval option identities must be nonempty and unique")
		}
		seen[id] = true
	}
	e.mu.Lock()
	t := threadByID(&e.snap, r.threadID)
	r.mu.Lock()
	if t == nil || e.stopping || r.session == nil || r.generation != h.generation || string(p.SessionId) != r.session.SessionID() || r.turnDone == nil || r.interrupted || t.NeedsResume || (t.State != "running" && t.State != "waiting") || ctx.Err() != nil || r.ctx.Err() != nil {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, nil
	}
	if len(r.pending)+len(r.questions) >= maxPendingApprovals {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, fmt.Errorf("too many unanswered permission requests on this thread")
	}
	// Retain history and reserve the eventual accepted choice before exposing
	// an actionable card. Failed persistence must not install a callback.
	next := clone(e.snap)
	nextThread := threadByID(&next, r.threadID)
	request.TurnID, request.Origin, request.DeliveryRoute = t.TurnID, t.Agent, "native-response"
	nextThread.Requests = append(nextThread.Requests, request)
	nextThread.State = "waiting"
	if len(nextThread.Requests) > 128 || projectedSize(next)+capacitySlack > snapshotLimit {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, fmt.Errorf("native approval history capacity reached")
	}
	next.Revision++
	if err := e.store.Save(next, nil, nil); err != nil {
		if e.flushErr == nil {
			e.flushErr = err
		}
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, err
	}
	answer := make(chan string, 1)
	if r.pending == nil {
		r.pending = make(map[string]*pendingApproval)
	}
	r.pending[request.ID] = &pendingApproval{answer: answer, ctx: ctx, generation: h.generation, sessionID: string(p.SessionId), turnID: t.TurnID}
	turnCancelled, turnDone, disconnected := r.cancelCh, r.turnDone, r.session.Done()
	r.mu.Unlock()
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	e.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, request.ID)
		r.mu.Unlock()
	}()

	select {
	case option, ok := <-answer:
		if !ok {
			return cancelled, r.finishApproval(request.ID, "", "acp-undeliverable")
		}
		if err := r.finishApproval(request.ID, option, "acp-unconfirmed"); err != nil {
			return cancelled, err
		}
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(option)}}}, nil
	case <-turnCancelled:
		return cancelled, r.finishApproval(request.ID, "", "acp-cancelled")
	case <-turnDone:
	case <-disconnected:
	case <-ctx.Done():
	case <-r.ctx.Done():
	}
	return cancelled, r.finishApproval(request.ID, "", "acp-undeliverable")
}

// finishApproval commits the last local delivery boundary before the handler
// returns. The SDK supplies no write/receipt callback: "unconfirmed" must never
// be treated as an acknowledgment, even after a later tool or turn completes.
func (r *acpRun) finishApproval(requestID, optionID, delivery string) error {
	e := r.e
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	t := threadByID(&next, r.threadID)
	if t == nil {
		return failure("stale_request", "the approval thread no longer exists")
	}
	var request *protocol.Request
	for i := range t.Requests {
		if t.Requests[i].ID == requestID {
			request = &t.Requests[i]
			break
		}
	}
	if request == nil || (request.State != "pending" && request.State != "submitted") {
		return failure("stale_request", "the approval is no longer live")
	}
	var deliveryErr error
	if optionID != "" && (!r.liveApproval(t, requestID) || request.State != "submitted" || request.ApprovalChoiceID != optionID) {
		delivery, deliveryErr = "acp-undeliverable", failure("stale_request", "the approval was cancelled before response handoff")
	}
	request.State, request.Delivery = "closed", delivery
	request.Revision++
	if t.State == "waiting" {
		t.State = "running"
		for _, pending := range t.Requests {
			if (pending.State == "pending" || pending.State == "submitted") && pending.Mode == "blocking" {
				t.State = "waiting"
			}
		}
	}
	next.Revision++
	if err := e.store.Save(next, nil, nil); err != nil {
		// Do not grant permission when the outcome cannot be retained.
		if e.flushErr == nil {
			e.flushErr = err
		}
		return err
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	return deliveryErr
}

// nativeApprovalAnswerReserve covers the largest exact choice retained on
// acceptance, plus escaped command identity (up to 128 bytes), revision and
// delivery metadata. IDs are opaque and must not be truncated for capacity.
func nativeApprovalAnswerReserve(request protocol.Request) int {
	largest := 0
	for i, id := range request.ChoiceIDs {
		encodedID, _ := json.Marshal(id)
		labelSize := 0
		if i < len(request.Choices) {
			label, _ := json.Marshal(request.Choices[i])
			labelSize = len(label)
		}
		largest = max(largest, len(encodedID)+labelSize)
	}
	return largest + 1024
}
