package protocol

// ThreadCloseBlocked reports work that must be resolved explicitly before Close.
// It never interrupts, resolves requests, or drains queued prompts.
func ThreadCloseBlocked(t Thread) string {
	if t.State == "running" || t.State == "waiting" {
		return "thread is still running or waiting; finish or interrupt it before closing"
	}
	if len(t.Queue) > 0 {
		return "thread has queued prompts; remove or complete them before closing"
	}
	for _, request := range t.Requests {
		if request.State == "pending" || request.State == "submitted" {
			return "thread has a pending request; resolve it before closing"
		}
	}
	for _, child := range t.Children {
		if child.State == "running" || child.State == "active" || child.State == "waiting" {
			return "thread has active child work; finish or interrupt it before closing"
		}
	}
	return ""
}
