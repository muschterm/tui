package protocol

import "slices"

// QueueSteerBlocked returns why a captured queued prompt cannot enter the active
// fixture turn. Revision and expected-turn race checks remain command concerns.
func QueueSteerBlocked(capabilities []string, thread Thread, prompt Prompt) string {
	if !slices.Contains(capabilities, "fixture-steering") || thread.Agent != "Fixture agent" {
		return "Steering is unavailable for this agent"
	}
	if thread.Closed {
		return "Reopen the thread before steering"
	}
	if thread.NeedsResume {
		return "Resume the thread before steering"
	}
	if (thread.State != "running" && thread.State != "waiting") || thread.TurnID == "" {
		return "Steering requires an active turn"
	}
	for _, activity := range thread.Activity {
		if activity.ID == "result-"+thread.TurnID && activity.State == "completed" {
			return "The active turn has completed"
		}
	}
	if prompt.Settings != thread.Effective {
		return "Queued settings differ from the active turn"
	}
	return ""
}
