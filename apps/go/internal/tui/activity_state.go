package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// activitySummary describes reported progress, never inferred model reasoning.
// Working is suitable for animation only while the client is connected.
type activitySummary struct {
	Key, Label, State              string
	Total, Completed, WorkingCount int
	Dismissible, Working           bool
}

func activeTurn(t protocol.Thread) bool {
	return (t.State == "running" || t.State == "waiting") && !t.NeedsResume
}

func agentSummary(t protocol.Thread) activitySummary {
	if len(t.Children) == 0 {
		return activitySummary{}
	}
	states := make([]string, 0, len(t.Children))
	identity := make([]string, 0, len(t.Children)*4)
	for _, child := range t.Children {
		states = append(states, child.State)
		identity = append(identity, child.ID, child.ParentID, child.Name, child.State)
	}
	s := summarizeActivity(t, states)
	s.Key = activityKey(t, "agents", identity)
	s.Label = "Agents"
	return s
}

func planSummary(t protocol.Thread) activitySummary {
	if len(t.Plan) == 0 {
		return activitySummary{}
	}
	states := make([]string, 0, len(t.Plan))
	identity := make([]string, 0, len(t.Plan)*2)
	for _, step := range t.Plan {
		states = append(states, step.State)
		identity = append(identity, step.Title, step.State)
	}
	s := summarizeActivity(t, states)
	s.Key = activityKey(t, "plan", identity)
	s.Label = fmt.Sprintf("Plan %d/%d", s.Completed, s.Total)
	if s.State != "completed" && s.State != "working" {
		s.Label += " · " + s.State
	}
	return s
}

func summarizeActivity(t protocol.Thread, states []string) activitySummary {
	s := activitySummary{Total: len(states)}
	failed, interrupted, waiting, unknown := false, false, false, false
	for _, state := range states {
		switch state {
		case "completed":
			s.Completed++
		case "running", "active", "in_progress":
			s.WorkingCount++
		case "failed", "error":
			failed = true
		case "interrupted", "cancelled", "canceled":
			interrupted = true
		case "waiting", "blocked":
			waiting = true
		case "pending", "queued":
		default:
			unknown = true
		}
	}
	switch {
	case s.Completed == s.Total && s.Total > 0:
		s.State, s.Dismissible = "completed", true
	case failed || t.State == "failed":
		s.State = "failed"
	case interrupted || t.NeedsResume || t.State == "interrupted" || t.State == "cancelled" || t.State == "canceled":
		s.State = "interrupted"
	case waiting || t.State == "waiting":
		s.State = "waiting"
	case unknown:
		s.State = "unknown"
	case activeTurn(t) && s.WorkingCount > 0:
		s.State, s.Working = "working", true
	case s.WorkingCount > 0:
		s.State = "paused"
	default:
		s.State = "pending"
	}
	return s
}

// activityKey excludes unrelated snapshot revisions, ticks and streaming text.
// The latest accepted user activity identifies a turn in this fixture protocol,
// which has no run ID. Callers also clear dismissal on observed unfinished work
// or a new active turn. Identical runs without either signal cannot be separated.
func activityKey(t protocol.Thread, kind string, identity []string) string {
	turn := ""
	for i := len(t.Activity) - 1; i >= 0; i-- {
		if t.Activity[i].Role == "user" {
			turn = t.Activity[i].ID
			break
		}
	}
	parts := append([]string{t.ID, turn, kind}, identity...)
	data, _ := json.Marshal(parts) // Strings always have a JSON representation.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
