package server

import (
	"fmt"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func failure(code, message string) error { return &protocol.Error{Code: code, Message: message} }

func validSettings(s protocol.Settings) bool {
	return s.Model == "fixture-model" && (s.Effort == "low" || s.Effort == "medium" || s.Effort == "high") && s.Permissions == "fixture-only" && s.Context == "unavailable" && s.Speed == "standard"
}

// validateSettings checks captured settings against the agent that will run
// them. Settings are per-agent: the fixture has its fixed set, and an ACP agent
// accepts only reported option value IDs, using the live thread catalogue
// when available and the probe catalogue for a new session.
func validateSettings(s *protocol.Snapshot, agentID string, set protocol.Settings, live ...[]protocol.ConfigOption) error {
	if !agent.IsACP(agentID) {
		if !validSettings(set) {
			return failure("unsupported_settings", "fixture supports fixture-model, low/medium/high, fixture-only, unavailable context, standard speed")
		}
		return nil
	}
	a := agent.Find(s, agentID)
	if a == nil {
		return failure("unsupported_agent", "this thread's agent is no longer configured")
	}
	if a.State != agent.StateReady {
		return failure("agent_unavailable", a.Name+" is not ready; probe it before sending. "+a.Detail)
	}
	record := *a
	if len(live) > 0 && len(live[0]) > 0 {
		record.Options, record.Fields = live[0], agent.Fields(live[0])
	}
	if err := agent.ValidateSettings(record, set); err != nil {
		return failure("unsupported_settings", err.Error())
	}
	return nil
}

func apply(s *protocol.Snapshot, c protocol.Command) (string, error) {
	return applyResolved(s, c, nil)
}

// The engine holds its lock here and supplies paths it resolved beforehand.
func applyResolved(s *protocol.Snapshot, c protocol.Command, resolved *resolvedPath) (string, error) {
	if c.Kind == "thread.start" {
		return startThread(s, c)
	}
	if c.Kind == "settings.update" || c.Kind == "project.update" || c.Kind == "project.remove" {
		return applySettingsResolved(s, c, resolved)
	}
	if c.Kind == "project.add" || c.Kind == "thread.create" {
		return applyProjectResolved(s, c, resolved)
	}
	var t *protocol.Thread
	for i := range s.Threads {
		if s.Threads[i].ID == c.ThreadID {
			t = &s.Threads[i]
			break
		}
	}
	if t == nil {
		return "", failure("not_found", "thread does not exist")
	}
	// A job thread is driven by its job commands only (resolution jobs,
	// ADR 0023 S4; planning jobs, ADR 0027); Stop (thread.interrupt) stays
	// available.
	if t.Job != nil {
		switch {
		case c.Kind == "thread.delete" && activeTurn(t):
			return "", failure("job_running", "the job's agent is working; cancel it first")
		case c.Kind == "prompt.send", c.Kind == "prompt.reopen-send", c.Kind == "thread.resume", c.Kind == "thread.reopen", strings.HasPrefix(c.Kind, "queue."):
			return "", failure("job_thread", "this is a job's thread; use the job's commands")
		}
	}
	if c.Settings != nil {
		if err := validateSettings(s, t.AgentID, *c.Settings, t.Options); err != nil {
			return "", err
		}
	}
	switch c.Kind {
	case "thread.close", "thread.reopen", "thread.delete":
		if c.Revision != t.LifecycleRevision {
			return "", failure("stale_thread", "thread organization changed; refresh before trying again")
		}
		if c.Kind == "thread.delete" {
			for i := range s.Projects {
				if s.Projects[i].ID == t.ProjectID {
					s.Projects[i].Revision++
				}
			}
			for i := range s.Threads {
				if s.Threads[i].ID == c.ThreadID {
					s.Threads = append(s.Threads[:i], s.Threads[i+1:]...)
					break
				}
			}
			terminals := s.Terminals[:0]
			for _, terminal := range s.Terminals {
				if terminal.ThreadID != c.ThreadID {
					terminals = append(terminals, terminal)
				}
			}
			s.Terminals = terminals
			return "", nil
		}
		closed := c.Kind == "thread.close"
		if closed && !t.Closed {
			if reason := protocol.ThreadCloseBlocked(*t); reason != "" {
				return "", failure("thread_busy", reason)
			}
		}
		if t.Closed != closed {
			t.Closed = closed
			t.LifecycleRevision++
		}
		return t.ID, nil
	case "prompt.send", "prompt.reopen-send":
		if c.Kind == "prompt.reopen-send" {
			if c.Revision != t.LifecycleRevision {
				return "", failure("stale_thread", "thread organization changed; refresh before trying again")
			}
			if c.Settings == nil {
				return "", failure("invalid", "sending requires captured settings")
			}
		}
		if t.Closed && c.Kind != "prompt.reopen-send" {
			return "", failure("thread_closed", "reopen the thread before sending a prompt")
		}
		if strings.TrimSpace(c.Text) == "" || len(c.Text) > 16384 {
			return "", failure("invalid", "prompt must contain 1–16384 bytes")
		}
		if len(t.Queue) >= 32 {
			return "", failure("capacity", "prompt queue is full")
		}
		if len(c.Attachments) > 8 {
			return "", failure("capacity", "at most eight captured attachments")
		}
		for _, a := range c.Attachments {
			if len(a.Content) > 65536 {
				return "", failure("capacity", "attachment exceeds 64 KiB")
			}
		}
		if c.Kind == "prompt.reopen-send" && t.Closed {
			t.Closed = false
			t.LifecycleRevision++
		}
		set := t.Selected
		if c.Settings != nil {
			set = *c.Settings
		}
		id := "prompt-" + c.ID
		t.Queue = append(t.Queue, protocol.Prompt{ID: id, Text: c.Text, Revision: 1, Settings: set, Attachments: c.Attachments})
		t.QueueRevision++
		if t.Title == defaultThreadTitle {
			t.Title = titleFromPrompt(c.Text)
		}
		if agent.IsACP(t.AgentID) {
			// A pre-dispatch failure may retry unsent queue work on Send.
			// Post-dispatch failures retain the warning and explicit Resume gate.
			if t.State == "failed" && !t.NeedsResume {
				t.State, t.Error, t.StopReason = "idle", "", ""
			}
		}
		// An idle fixture thread starts its queue head in the engine, which
		// first acquires the checkout writer lease (startQueuedFixture).
		return id, nil
	case "queue.steer":
		if c.Revision != t.QueueRevision {
			return "", failure("stale_revision", "queue changed; retain your draft and refresh")
		}
		if c.ExpectedTurnID == "" || c.ExpectedTurnID != t.TurnID {
			return "", failure("stale_turn", "active turn changed; refresh before steering")
		}
		for i, p := range t.Queue {
			if p.ID != c.TargetID {
				continue
			}
			if reason := protocol.QueueSteerBlocked(s.Capabilities, *t, p); reason != "" {
				return "", failure("steer_unavailable", reason)
			}
			if fixtureTurnCompleted(t) {
				return "", failure("stale_turn", "active turn has already completed")
			}
			appendFixturePrompt(t, p)
			t.Queue = append(t.Queue[:i], t.Queue[i+1:]...)
			t.QueueRevision++
			trimFixtureActivity(t)
			return p.ID, nil
		}
		return "", failure("not_queued", "prompt is no longer queued")
	case "queue.edit", "queue.remove", "queue.reorder":
		if c.Revision != t.QueueRevision {
			return "", failure("stale_revision", "queue changed; retain your draft and refresh")
		}
		if c.Kind == "queue.reorder" {
			if len(c.Order) != len(t.Queue) {
				return "", failure("invalid", "order must include every queued prompt")
			}
			ordered := make([]protocol.Prompt, 0, len(t.Queue))
			seen := map[string]bool{}
			for _, id := range c.Order {
				found := false
				for _, p := range t.Queue {
					if p.ID == id && !seen[id] {
						ordered = append(ordered, p)
						seen[id] = true
						found = true
						break
					}
				}
				if !found {
					return "", failure("invalid", "order contains missing or duplicate prompt")
				}
			}
			t.Queue = ordered
			t.QueueRevision++
			return "", nil
		}
		for i := range t.Queue {
			if t.Queue[i].ID == c.TargetID {
				if c.Kind == "queue.remove" {
					t.Queue = append(t.Queue[:i], t.Queue[i+1:]...)
				} else {
					if strings.TrimSpace(c.Text) == "" || len(c.Text) > 16384 {
						return "", failure("invalid", "invalid prompt text")
					}
					t.Queue[i].Text = c.Text
					t.Queue[i].Revision++
					if c.Settings != nil {
						t.Queue[i].Settings = *c.Settings
					}
				}
				t.QueueRevision++
				return c.TargetID, nil
			}
		}
		return "", failure("not_queued", "prompt is no longer queued")
	case "request.answer":
		if t.NeedsResume {
			return "", failure("resume_required", "resume and revalidate this request first")
		}
		for i := range t.Requests {
			r := &t.Requests[i]
			if r.ID != c.TargetID {
				continue
			}
			if r.State != "pending" || r.Revision != c.Revision {
				return "", failure("stale_request", "request changed or was resolved elsewhere")
			}
			if agent.IsACP(t.AgentID) && (r.DeliveryRoute != "native-response" || (r.Kind != "approval" && (r.Kind != "question" || (t.AgentID != "claude" && t.AgentID != "codex") || len(r.SourcePayload) == 0))) {
				return "", failure("unsupported_request", "this agent request has no supported answer delivery route")
			}
			if c.RequestAction != "" {
				// A decline or cancel replaces the answer: it must be one the
				// upstream contract offered for this request, and it carries none.
				if r.Kind != "question" || c.ApprovalChoiceID != "" {
					return "", failure("invalid", "only a question request can be declined or cancelled")
				}
				if err := protocol.ValidateRequestAction(*r, c.RequestAction, c.QuestionAnswers, c.Answers); err != nil {
					return "", failure("unsupported_action", err.Error())
				}
				r.Action, r.QuestionAnswers, r.Answers = c.RequestAction, nil, nil
			} else if r.Kind == "approval" {
				choice, err := approvalChoice(*r, c)
				if err != nil {
					return "", err
				}
				r.Answers = []string{r.Choices[choice]}
				if len(r.ChoiceIDs) == len(r.Choices) {
					r.ApprovalChoiceID = r.ChoiceIDs[choice]
				}
			} else {
				if c.ApprovalChoiceID != "" {
					return "", failure("invalid", "an approval choice cannot answer a question")
				}
				answers, err := protocol.NormalizeQuestionAnswers(r.Questions, c.QuestionAnswers, c.Answers)
				if err != nil {
					return "", failure("invalid", err.Error())
				}
				r.QuestionAnswers, r.Answers = answers, c.Answers
				if agent.IsACP(t.AgentID) {
					// One authoritative answer representation; legacy input must
					// not double the native request's reserved storage budget.
					r.Answers = nil
				}
			}
			if agent.IsACP(t.AgentID) {
				r.State, r.Delivery = "submitted", "acp-accepted"
				r.SubmissionID, r.SubmittedRevision = c.ID, c.Revision
			} else {
				r.State, r.Delivery = "resolved", "fixture-confirmed"
			}
			r.Revision++
			if t.State == "waiting" && !agent.IsACP(t.AgentID) {
				t.State = "running"
				for _, pending := range t.Requests {
					if pending.State == "pending" && pending.Mode == "blocking" {
						t.State = "waiting"
					}
				}
			}
			if r.Kind == "question" {
				// The request record owns the accepted question and answer snapshot;
				// this transcript marker records where it belongs in conversation
				// chronology without copying or re-submitting the answer.
				activityID := fmt.Sprintf("question-answer:%s:%d", r.ID, c.Revision)
				present := false
				for _, activity := range t.Activity {
					present = present || activity.ID == activityID
				}
				if !present {
					turnID := r.TurnID
					if turnID == "" {
						turnID = t.TurnID
					}
					t.Activity = append(t.Activity, protocol.Activity{
						ID: activityID, Role: "question-answer", RequestID: r.ID, TurnID: turnID,
					})
				}
			}
			return r.ID, nil
		}
		return "", failure("not_found", "request missing")
	case "thread.resume":
		if agent.IsACP(t.AgentID) && (t.State == "running" || t.State == "waiting") {
			return "", failure("already_active", "this thread already has an active turn")
		}
		if t.Closed {
			return "", failure("thread_closed", "reopen the thread before resuming work")
		}
		if t.State == "idle" {
			// An idle thread has no turn to continue: Resume only clears the
			// gate and its queue competes for the checkout writer lease.
			t.NeedsResume, t.Error = false, ""
			return t.ID, nil
		}
		if agent.IsACP(t.AgentID) {
			// Resume never resends: it clears the gate and lets the dispatcher
			// start the queue head, restarting the process if it has ended.
			t.NeedsResume, t.State, t.Error = false, "idle", ""
			for i := range t.Requests {
				if t.Requests[i].State == "pending" || t.Requests[i].State == "submitted" {
					t.Requests[i].State, t.Requests[i].Delivery = "closed", "acp-undeliverable"
					t.Requests[i].Revision++
				}
			}
			return t.ID, nil
		}
		t.NeedsResume = false
		t.State = "running"
		for i := range t.Requests {
			if t.Requests[i].State == "pending" {
				t.Requests[i].Revision++
				t.Requests[i].Delivery = "fixture-revalidated"
				if t.Requests[i].Mode == "blocking" {
					t.State = "waiting"
				}
			}
		}
		for i := range t.Children {
			if t.Children[i].State == "interrupted" {
				t.Children[i].State = "running"
			}
		}
		return t.ID, nil
	case "thread.interrupt":
		// Stop interrupts an active turn. Accepting it for a thread that is not
		// working would mark finished work as interrupted and demand a Resume
		// that has nothing to resume.
		if t.State != "running" && t.State != "waiting" {
			return "", failure("not_active", "this thread has no active turn to interrupt")
		}
		t.RestartEligible = false
		t.State = "interrupted"
		t.NeedsResume = true
		for i := range t.Children {
			if t.Children[i].State == "running" {
				t.Children[i].State = "interrupted"
			}
		}
		return t.ID, nil
	case "terminal.open":
		// The engine starts shells (openTerminal); a pure transition cannot.
		return "", failure("terminal_unavailable", "terminals are opened by the server engine")
	case "terminal.close", "terminal.take-control":
		return applyTerminalCommand(s, t, c)
	default:
		return "", failure("unsupported_command", fmt.Sprintf("unsupported command %q", c.Kind))
	}
}
