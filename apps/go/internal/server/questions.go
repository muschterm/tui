package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

type pendingQuestion struct {
	pendingApproval // callback, connection generation, session and application turn
	form            *agent.QuestionForm
}

const nativeAnswerReserve = 128 << 10

func (h *acpHandler) ClaudeQuestions() bool { return h.questions }

// liveQuestion requires the original callback, never just a saved session name.
// The engine lock is held by callers; lock order is engine/run.
func (r *acpRun) liveQuestion(t *protocol.Thread, requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.questions[requestID]
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

// CreateElicitation supports only the pinned blocking AskUserQuestion form.
// The SDK owns RPC request IDs and propagates $/cancel_request into ctx.
func (h *acpHandler) CreateElicitation(ctx context.Context, raw json.RawMessage) (map[string]any, error) {
	cancelled := map[string]any{"action": "cancel"}
	if !h.questions {
		return cancelled, fmt.Errorf("native questions are unavailable on this connection")
	}
	form, err := agent.ParseClaudeQuestions("question-"+ID(), raw)
	if err != nil {
		return cancelled, err
	}
	r, e := h.acpRun, h.e
	e.mu.Lock()
	t := threadByID(&e.snap, r.threadID)
	r.mu.Lock()
	if t == nil || e.stopping || r.session == nil || !r.info.Has(agent.CapNativeQuestions) || r.generation != h.generation || form.SessionID != r.session.SessionID() || r.turnDone == nil || r.interrupted || t.NeedsResume || (t.State != "running" && t.State != "waiting") || ctx.Err() != nil || r.ctx.Err() != nil {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, nil
	}
	if len(r.questions)+len(r.pending) >= maxPendingApprovals {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, fmt.Errorf("too many unanswered agent requests on this thread")
	}
	for _, existing := range t.Requests {
		if existing.Kind != "question" || existing.TurnID != t.TurnID {
			continue
		}
		var source struct {
			ToolCallID string `json:"toolCallId"`
		}
		_ = json.Unmarshal(existing.SourcePayload, &source)
		if source.ToolCallID == form.ToolCallID {
			r.mu.Unlock()
			e.mu.Unlock()
			return cancelled, fmt.Errorf("question tool call already presented in this turn")
		}
	}
	// projectedSize reserves answer space for every pending native question.
	// Never evict accepted history to make a provider request actionable.
	next := clone(e.snap)
	nextThread := threadByID(&next, r.threadID)
	request := form.Request
	request.TurnID, request.Origin = t.TurnID, t.Agent
	nextThread.Requests = append(nextThread.Requests, request)
	nextThread.State = "waiting"
	if len(nextThread.Requests) > 128 || projectedSize(next) > snapshotLimit {
		r.mu.Unlock()
		e.mu.Unlock()
		return cancelled, fmt.Errorf("native question history capacity reached")
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
	if r.questions == nil {
		r.questions = make(map[string]*pendingQuestion)
	}
	r.questions[form.Request.ID] = &pendingQuestion{
		pendingApproval: pendingApproval{answer: answer, ctx: ctx, generation: h.generation, sessionID: form.SessionID, turnID: t.TurnID},
		form:            form,
	}
	turnCancelled, turnDone, disconnected := r.cancelCh, r.turnDone, r.session.Done()
	r.mu.Unlock()
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	e.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.questions, request.ID)
		r.mu.Unlock()
	}()
	select {
	case submissionID := <-answer:
		return r.finishQuestion(request.ID, submissionID, "acp-unconfirmed")
	case <-turnCancelled:
		return r.finishQuestion(request.ID, "", "acp-cancelled")
	case <-ctx.Done():
		// A correlated RPC cancellation is withdrawal, not answer confirmation.
		return r.finishQuestion(request.ID, "", "acp-undeliverable")
	case <-turnDone:
	case <-disconnected:
	case <-r.ctx.Done():
	}
	return r.finishQuestion(request.ID, "", "acp-undeliverable")
}

func (r *acpRun) resolveQuestion(requestID, submissionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.questions[requestID]; p != nil {
		select {
		case p.answer <- submissionID:
		default:
		}
	}
}

// validateQuestionAnswer runs the dialect's stricter encoding checks before
// durable command acceptance, so unsupported text never becomes submitted.
func (r *acpRun) validateQuestionAnswer(t *protocol.Thread, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.questions[requestID]
	if p == nil {
		return nil
	} // Approval validation is separate.
	for _, request := range t.Requests {
		if request.ID == requestID {
			if _, err := p.form.Response(request.QuestionAnswers); err != nil {
				return failure("invalid", err.Error())
			}
			return nil
		}
	}
	return failure("stale_request", "question is missing")
}

// finishQuestion persists the accepted snapshot before preparing a response.
// Neither this return, the SDK's pipe write, nor turn completion is a receipt.
func (r *acpRun) finishQuestion(requestID, submissionID, delivery string) (map[string]any, error) {
	cancelled := map[string]any{"action": "cancel"}
	e := r.e
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	t := threadByID(&next, r.threadID)
	if t == nil {
		return cancelled, failure("stale_request", "the question thread no longer exists")
	}
	var request *protocol.Request
	for i := range t.Requests {
		if t.Requests[i].ID == requestID {
			request = &t.Requests[i]
			break
		}
	}
	if request == nil || (request.State != "pending" && request.State != "submitted") {
		return cancelled, failure("stale_request", "the question is no longer live")
	}
	response := cancelled
	var deliveryErr error
	if submissionID != "" {
		if !r.liveQuestion(t, requestID) || request.State != "submitted" || request.SubmissionID != submissionID {
			delivery, deliveryErr = "acp-undeliverable", failure("stale_request", "the question was cancelled before response handoff")
		} else {
			r.mu.Lock()
			form := r.questions[requestID].form
			r.mu.Unlock()
			response, deliveryErr = form.Response(request.QuestionAnswers)
			if deliveryErr != nil {
				delivery, response = "acp-undeliverable", cancelled
			}
		}
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
		if e.flushErr == nil {
			e.flushErr = err
		}
		return cancelled, err
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	return response, deliveryErr
}
