package server

import (
	"fmt"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func failure(code, message string) error { return &protocol.Error{Code: code, Message: message} }
func validSettings(s protocol.Settings) bool {
	return s.Model == "fixture-model" && (s.Effort == "low" || s.Effort == "medium" || s.Effort == "high") && s.Permissions == "fixture-only" && s.Context == "unavailable" && s.Speed == "standard"
}
func apply(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.Kind == "project.add" || c.Kind == "thread.create" {
		return applyProject(s, c)
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
	if c.Settings != nil && !validSettings(*c.Settings) {
		return "", failure("unsupported_settings", "fixture supports fixture-model, low/medium/high, fixture-only, unavailable context, standard speed")
	}
	switch c.Kind {
	case "thread.close", "thread.reopen", "thread.delete":
		if c.Revision != t.LifecycleRevision {
			return "", failure("stale_thread", "thread organization changed; refresh before trying again")
		}
		if c.Kind == "thread.delete" {
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
	case "prompt.send":
		if t.Closed {
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
		set := t.Selected
		if c.Settings != nil {
			set = *c.Settings
		}
		id := "prompt-" + c.ID
		t.Queue = append(t.Queue, protocol.Prompt{ID: id, Text: c.Text, Revision: 1, Settings: set, Attachments: c.Attachments})
		t.QueueRevision++
		if t.State == "idle" && !t.NeedsResume {
			startFixturePrompt(t)
		}
		return id, nil
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
			return "", failure("resume_required", "resume and revalidate this fixture request first")
		}
		for i := range t.Requests {
			r := &t.Requests[i]
			if r.ID != c.TargetID {
				continue
			}
			if r.State != "pending" || r.Revision != c.Revision {
				return "", failure("stale_request", "request changed or was resolved elsewhere")
			}
			if r.Kind == "approval" {
				if len(c.Answers) != 1 || c.QuestionAnswers != nil {
					return "", failure("invalid", "choose one approval response")
				}
				valid := false
				for _, v := range r.Choices {
					valid = valid || v == c.Answers[0]
				}
				if !valid {
					return "", failure("invalid", "unsupported approval choice")
				}
			} else {
				answers, err := protocol.NormalizeQuestionAnswers(r.Questions, c.QuestionAnswers, c.Answers)
				if err != nil {
					return "", failure("invalid", err.Error())
				}
				r.QuestionAnswers = answers
			}
			r.Answers = c.Answers
			r.State = "resolved"
			r.Delivery = "fixture-confirmed"
			r.Revision++
			if t.State == "waiting" {
				t.State = "running"
				for _, pending := range t.Requests {
					if pending.State == "pending" && pending.Mode == "blocking" {
						t.State = "waiting"
					}
				}
			}
			return r.ID, nil
		}
		return "", failure("not_found", "request missing")
	case "thread.resume":
		if t.Closed {
			return "", failure("thread_closed", "reopen the thread before resuming work")
		}
		if t.State == "idle" && !t.NeedsResume {
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
		t.State = "interrupted"
		t.NeedsResume = true
		for i := range t.Children {
			if t.Children[i].State == "running" {
				t.Children[i].State = "interrupted"
			}
		}
		return t.ID, nil
	case "terminal.open":
		if len(s.Terminals) >= 64 {
			return "", failure("capacity", "terminal fixture limit reached")
		}
		id := "terminal-" + c.ID
		s.Terminals = append(s.Terminals, protocol.Terminal{ID: id, ThreadID: t.ID, State: "running", Controller: c.ClientID, Revision: 1, Output: "Synthetic terminal session · no shell or PTY\n$ fixture status\nready"})
		return id, nil
	case "terminal.close":
		for i := range s.Terminals {
			v := &s.Terminals[i]
			if v.ID == c.TargetID && v.ThreadID == t.ID {
				v.State = "ended"
				v.Revision++
				return v.ID, nil
			}
		}
		return "", failure("not_found", "terminal missing")
	default:
		return "", failure("unsupported_command", fmt.Sprintf("unsupported command %q", c.Kind))
	}
}
