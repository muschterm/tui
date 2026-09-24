package server

import (
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func ensureAppSettings(s *protocol.Snapshot) {
	if s.AppSettings.ProjectDirectory == "" {
		s.AppSettings.ProjectDirectory = "~"
	}
	if s.AppSettings.WorkspaceDefault == "" {
		s.AppSettings.WorkspaceDefault = "checkout"
	}
	if s.AppSettings.Revision == 0 {
		s.AppSettings.Revision = 1
	}
	for i := range s.Projects {
		if s.Projects[i].Revision == 0 {
			s.Projects[i].Revision = 1
		}
	}
	for _, capability := range []string{"app-settings", "new-thread-defaults", "project-settings", "restart-continuation", "path-completion", "workspace-file-context"} {
		if !slices.Contains(s.Capabilities, capability) {
			s.Capabilities = append(s.Capabilities, capability)
		}
	}
}

// Older clients omit the directory when changing existing settings. Preserve
// it; an explicit "~" resets the starting directory.
func nextProjectDirectory(s protocol.Snapshot, requested protocol.AppSettings) string {
	if requested.ProjectDirectory != "" {
		return requested.ProjectDirectory
	}
	if s.AppSettings.ProjectDirectory != "" {
		return s.AppSettings.ProjectDirectory
	}
	return "~"
}

func applySettingsResolved(s *protocol.Snapshot, c protocol.Command, resolved *resolvedPath) (string, error) {
	if c.Kind == "settings.update" {
		if c.Revision != s.AppSettings.Revision {
			return "", failure("stale_settings", "app settings changed; refresh before saving")
		}
		if c.AppSettings == nil || !slices.Contains([]string{"checkout", "worktree"}, c.AppSettings.WorkspaceDefault) {
			return "", failure("invalid", "choose current checkout or worktree")
		}
		next := *c.AppSettings
		if next.NewThreadDefaults == nil || reflect.DeepEqual(next.NewThreadDefaults, s.AppSettings.NewThreadDefaults) {
			next.NewThreadDefaults = s.AppSettings.NewThreadDefaults
		} else if next.NewThreadDefaults.AgentID == "" {
			if next.NewThreadDefaults.Settings != (protocol.Settings{}) {
				return "", failure("invalid", "reset new-thread defaults without settings")
			}
			next.NewThreadDefaults = nil
		} else {
			if next.NewThreadDefaults.AgentID != agent.FixtureID {
				record := agent.Find(s, next.NewThreadDefaults.AgentID)
				if record == nil {
					return "", failure("unsupported_agent", "new-thread default agent is no longer configured")
				}
				if record.Fields.Model == "" || next.NewThreadDefaults.Settings.Model == "" {
					return "", failure("unsupported_settings", "default agent must offer a selected model")
				}
			}
			if err := validateSettings(s, next.NewThreadDefaults.AgentID, next.NewThreadDefaults.Settings); err != nil {
				return "", err
			}
		}
		next.ProjectDirectory = nextProjectDirectory(*s, next)
		if next.ProjectDirectory != s.AppSettings.ProjectDirectory {
			path, err := resolved.result(next.ProjectDirectory)
			if err != nil {
				return "", err
			}
			if next.ProjectDirectory != "~" {
				next.ProjectDirectory = path
			}
		}
		next.Revision = s.AppSettings.Revision + 1
		s.AppSettings = next
		return "", nil
	}
	for i := range s.Projects {
		p := &s.Projects[i]
		if p.ID != c.ProjectID {
			continue
		}
		if c.Revision != p.Revision {
			return "", failure("stale_project", "project or its threads changed; refresh before saving")
		}
		if c.Kind == "project.remove" {
			if reason := protocol.ProjectRemoveBlocked(*s, p.ID); reason != "" {
				return "", failure("project_busy", reason)
			}
			removed := map[string]bool{}
			threads := s.Threads[:0]
			for _, t := range s.Threads {
				if t.ProjectID == p.ID {
					removed[t.ID] = true
				} else {
					threads = append(threads, t)
				}
			}
			s.Threads = threads
			terminals := s.Terminals[:0]
			for _, terminal := range s.Terminals {
				if !removed[terminal.ThreadID] {
					terminals = append(terminals, terminal)
				}
			}
			s.Terminals = terminals
			s.Projects = append(s.Projects[:i], s.Projects[i+1:]...)
			return "", nil
		}
		if c.ProjectSettings == nil {
			return "", failure("invalid", "project settings are required")
		}
		value := *c.ProjectSettings
		value.Name = strings.TrimSpace(value.Name)
		if value.Name == "" || len(value.Name) > 256 || strings.ContainsFunc(value.Name, unicode.IsControl) {
			return "", failure("invalid", "project name must contain 1–256 bytes without control characters")
		}
		if !slices.Contains([]string{"", "folder", "code", "terminal", "git", "star", "rocket"}, value.Icon) || !slices.Contains([]string{"", "purple", "blue", "green", "orange", "pink", "teal"}, value.Color) {
			return "", failure("invalid", "unsupported project icon or color")
		}
		if !slices.Contains([]string{"", "checkout", "worktree"}, value.WorkspaceDefault) {
			return "", failure("invalid", "choose inherit, current checkout or worktree")
		}
		p.Name, p.Icon, p.Color, p.WorkspaceDefault = value.Name, value.Icon, value.Color, value.WorkspaceDefault
		p.Revision++
		for j := range s.Threads {
			if s.Threads[j].ProjectID == p.ID {
				s.Threads[j].Project = p.Name
			}
		}
		return p.ID, nil
	}
	return "", failure("not_found", "project does not exist")
}

// Only known fixture turns active at shutdown/crash qualify. Old interrupted
// snapshots lack the marker, and manual Stop clears it. Requests always require
// explicit revalidation and answers, even when continuation is enabled.
func restartEligible(t *protocol.Thread) bool {
	// An ACP turn cannot be reattached after a restart: its process is gone and
	// no prompt is ever resent automatically.
	if t.Closed || agent.IsACP(t.AgentID) || t.Agent != "Fixture agent" || !validSettings(t.Effective) {
		return false
	}
	// A running thread can be between its completed turn and the next queued
	// dispatch. Preserve continuation across that durable tick boundary too.
	if fixtureTurnCompleted(t) && len(t.Queue) == 0 {
		return false
	}
	if !(t.State == "running" && !t.NeedsResume || t.State == "interrupted" && t.RestartEligible) {
		return false
	}
	for _, r := range t.Requests {
		if r.State == "pending" {
			return false
		}
	}
	for _, child := range t.Children {
		if child.State != "running" && child.State != "interrupted" && child.State != "completed" {
			return false
		}
	}
	return true
}

func recoverThreads(s *protocol.Snapshot) {
	for i := range s.Threads {
		t := &s.Threads[i]
		resume := s.AppSettings.ContinueAfterRestart && restartEligible(t)
		t.RestartEligible = false
		if agent.IsACP(t.AgentID) {
			recoverACPThread(t)
			gateQueuedIdle(t)
			continue
		}
		// Continuation eligibility (restartEligible) never covers an idle
		// thread, so its queued work is always gated.
		gateQueuedIdle(t)
		if t.State != "idle" {
			t.NeedsResume = true
			t.State = "interrupted"
		}
		for j := range t.Children {
			if t.Children[j].State == "running" {
				t.Children[j].State = "interrupted"
			}
		}
		for j := range t.Requests {
			if t.Requests[j].State == "pending" {
				t.Requests[j].Revision++
				t.Requests[j].Delivery = "revalidation-required"
			}
		}
		if resume {
			t.State, t.NeedsResume = "running", false
			for j := range t.Children {
				if t.Children[j].State == "interrupted" {
					t.Children[j].State = "running"
				}
			}
		}
	}
}

// recoverACPThread restores an ACP thread after a restart. Its agent process is
// gone, so a running or waiting turn becomes interrupted and needs an explicit
// Resume; the queue, captures and transcript are preserved untouched.
//
// Pending calls belonged to the dead process and become undeliverable. Accepted
// responses of the turn in flight retain their snapshots with uncertain
// receipt, never blind replay. A turn that had already ended recorded its own
// outcome; restart adds no uncertainty to it. A future approval requires a
// newly issued live call; Resume does not create one.
func recoverACPThread(t *protocol.Thread) {
	inFlight := t.State == "running" || t.State == "waiting"
	completed := make(map[string]bool)
	for _, activity := range t.Activity {
		if activity.Role == "user" && activity.Prompt != nil && activity.ID == activity.Prompt.ID && activity.TurnID == activity.ID && activity.State == "completed" {
			completed[activity.ID] = true
		}
	}
	// Earlier versions requeued failed in-flight prompts. Only remove a queue
	// copy when the retained user activity proves that exact capture crossed
	// dispatch; preserve unmatched/edited captures as queued work.
	dispatched := make(map[string]protocol.Prompt)
	for _, activity := range t.Activity {
		if activity.Role == "user" && activity.Prompt != nil && activity.TurnID == activity.Prompt.ID && (activity.State == "failed" || activity.State == "running" || activity.State == "interrupted") {
			dispatched[activity.Prompt.ID] = *activity.Prompt
		}
	}
	queue := t.Queue[:0]
	for _, prompt := range t.Queue {
		if captured, ok := dispatched[prompt.ID]; ok {
			t.QueueRevision++
			t.NeedsResume = true
			if reflect.DeepEqual(captured, prompt) {
				continue
			}
			// A user-edited legacy retry is new input. Preserve its capture,
			// but never overwrite the original history or reuse its turn ID.
			prompt.ID = "prompt-recovered-" + ID()
		}
		queue = append(queue, prompt)
	}
	t.Queue = queue
	if t.State == "failed" {
		t.NeedsResume = true
	}
	if t.State == "running" || t.State == "waiting" || t.State == "interrupted" {
		t.State, t.NeedsResume = "interrupted", true
	}
	for j := range t.Children {
		if t.Children[j].State == "running" {
			t.Children[j].State = "interrupted"
		}
	}
	for j := range t.Requests {
		r := &t.Requests[j]
		switch {
		case r.State == "submitted" || r.Delivery == "acp-accepted":
			// Accepted but never handed off: the turn was still live.
			r.State, r.Delivery = "closed", "acp-uncertain"
			r.Revision++
		case r.Delivery == "acp-delivered" || r.Delivery == "acp-unconfirmed":
			if inFlight && (r.TurnID == "" || r.TurnID == t.TurnID) {
				r.State, r.Delivery = "closed", "acp-uncertain"
				r.Revision++
			}
		case r.State == "pending":
			r.State, r.Delivery = "closed", "acp-undeliverable"
			r.Revision++
		case r.State == "closed" && r.Delivery == "acp-uncertain" && r.SubmissionID != "" && r.TurnID != "" && completed[r.TurnID]:
			// Repair records that earlier restarts downgraded after their turn
			// had completed normally. A failed turn marks its prompt failed and
			// an interrupted one interrupted, so only restart produced this
			// pair. Restore the prior truthful state; never mark it delivered.
			r.Delivery = "acp-unconfirmed"
			r.Revision++
		}
	}
	// Streamed activity that was mid-flight cannot continue in this process.
	for j := range t.Activity {
		if t.Activity[j].State == "running" {
			t.Activity[j].State = "interrupted"
		}
	}
}

// gateQueuedIdle requires explicit Resume for queued work that never started
// before a restart (for example a writer-lease waiter). Restart continuation
// applies only to eligible in-flight turns, never to idle threads.
func gateQueuedIdle(t *protocol.Thread) {
	if t.State == "idle" && len(t.Queue) > 0 && !t.Closed {
		t.NeedsResume = true
	}
}
