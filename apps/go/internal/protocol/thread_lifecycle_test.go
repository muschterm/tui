package protocol

import "testing"

func TestThreadCloseBlocked(t *testing.T) {
	for _, thread := range []Thread{
		{State: "running"}, {State: "waiting"},
		{State: "idle", Queue: []Prompt{{ID: "queued"}}},
		{State: "interrupted", Requests: []Request{{State: "pending"}}},
		{State: "failed", Children: []Child{{State: "running"}}},
		{State: "idle", Children: []Child{{State: "waiting"}}},
	} {
		if ThreadCloseBlocked(thread) == "" {
			t.Fatalf("busy thread allowed: %+v", thread)
		}
	}
	for _, state := range []string{"idle", "failed", "interrupted"} {
		if reason := ThreadCloseBlocked(Thread{State: state, Children: []Child{{State: "completed"}}, Requests: []Request{{State: "resolved"}}}); reason != "" {
			t.Fatalf("%s: %s", state, reason)
		}
	}
}
