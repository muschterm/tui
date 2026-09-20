package tui

import (
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"testing"
)

func TestActivitySummariesReportedStates(t *testing.T) {
	cases := []struct {
		name, turn           string
		resume               bool
		states               []string
		want                 string
		working, dismissible bool
	}{
		{"running", "running", false, []string{"running", "completed"}, "working", true, false},
		{"plan active", "running", false, []string{"active", "pending"}, "working", true, false},
		{"all completed", "completed", false, []string{"completed", "completed"}, "completed", false, true},
		{"children complete during turn", "running", false, []string{"completed"}, "completed", false, true},
		{"failed", "running", false, []string{"completed", "failed"}, "failed", false, false},
		{"parent failed", "failed", false, []string{"active"}, "failed", false, false},
		{"interrupted", "running", false, []string{"completed", "interrupted"}, "interrupted", false, false},
		{"parent interrupted", "interrupted", false, []string{"active"}, "interrupted", false, false},
		{"explicit resume required", "running", true, []string{"active"}, "interrupted", false, false},
		{"waiting", "waiting", false, []string{"active"}, "waiting", false, false},
		{"child waiting", "running", false, []string{"waiting"}, "waiting", false, false},
		{"pending", "running", false, []string{"pending"}, "pending", false, false},
		{"unknown", "running", false, []string{"unrecognized"}, "unknown", false, false},
		{"stale active after end", "completed", false, []string{"active"}, "paused", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			thread := protocol.Thread{ID: "thread", State: tc.turn, NeedsResume: tc.resume}
			for _, state := range tc.states {
				thread.Children = append(thread.Children, protocol.Child{State: state})
				thread.Plan = append(thread.Plan, protocol.PlanStep{State: state})
			}
			for _, s := range []activitySummary{agentSummary(thread), planSummary(thread)} {
				if s.State != tc.want || s.Working != tc.working || s.Dismissible != tc.dismissible {
					t.Fatalf("summary = %+v; want %s, working %v, dismissible %v", s, tc.want, tc.working, tc.dismissible)
				}
			}
		})
	}
}

func TestActivitySummaryLabelsAndEmpty(t *testing.T) {
	if got := agentSummary(protocol.Thread{}); got != (activitySummary{}) {
		t.Fatalf("empty agents: %+v", got)
	}
	if got := planSummary(protocol.Thread{}); got != (activitySummary{}) {
		t.Fatalf("empty plan: %+v", got)
	}
	thread := protocol.Thread{State: "running", Children: []protocol.Child{{State: "running"}, {State: "completed"}, {State: "running"}}, Plan: []protocol.PlanStep{{State: "completed"}, {State: "active"}}}
	if got := agentSummary(thread); got.Label != "Agents" || got.Total != 3 || got.Completed != 1 {
		t.Fatalf("agents: %+v", got)
	}
	if got := planSummary(thread); got.Label != "Plan 1/2" {
		t.Fatalf("plan: %+v", got)
	}
	for i := range thread.Children {
		thread.Children[i].State = "completed"
	}
	if got := agentSummary(thread); got.Label != "Agents" || !got.Dismissible {
		t.Fatalf("completed agents: %+v", got)
	}
}

func TestActivitySummaryIdentity(t *testing.T) {
	base := protocol.Thread{ID: "thread", State: "completed", Activity: []protocol.Activity{{ID: "prompt-1", Role: "user"}}, Children: []protocol.Child{{ID: "child-1", Name: "Review", State: "completed"}}, Plan: []protocol.PlanStep{{Title: "Review", State: "completed"}}}
	for name, summary := range map[string]func(protocol.Thread) activitySummary{"agents": agentSummary, "plan": planSummary} {
		t.Run(name, func(t *testing.T) {
			original := summary(base).Key
			other := base
			other.Tick++
			other.Activity = append(append([]protocol.Activity{}, base.Activity...), protocol.Activity{ID: "stream", Role: "agent", Text: "More streamed text"})
			if summary(other).Key != original {
				t.Fatal("unrelated tick/text changed identity")
			}
			other.Activity = append(other.Activity, protocol.Activity{ID: "prompt-2", Role: "user"})
			if summary(other).Key == original {
				t.Fatal("new accepted prompt did not change identity")
			}
			other = base
			other.ID = "other-thread"
			if summary(other).Key == original {
				t.Fatal("thread change did not change identity")
			}
			other = base
			other.Children = []protocol.Child{{ID: "child-2", Name: "Review", State: "completed"}}
			other.Plan = []protocol.PlanStep{{Title: "New review", State: "completed"}}
			if summary(other).Key == original {
				t.Fatal("new group/plan did not change identity")
			}
			other.Children[0].State = "running"
			other.Plan[0].State = "active"
			if summary(other).Dismissible {
				t.Fatal("reopened work remains dismissible")
			}
		})
	}
}

func TestActiveTurnRequiresRunningAndResume(t *testing.T) {
	for _, state := range []string{"completed", "failed", "interrupted", "idle", ""} {
		if activeTurn(protocol.Thread{State: state}) {
			t.Fatalf("%s treated as active", state)
		}
	}
	if activeTurn(protocol.Thread{State: "running", NeedsResume: true}) {
		t.Fatal("unresumed turn active")
	}
	if !activeTurn(protocol.Thread{State: "waiting"}) {
		t.Fatal("waiting turn inactive")
	}
	if activeTurn(protocol.Thread{State: "waiting", NeedsResume: true}) {
		t.Fatal("unresumed waiting turn active")
	}
	if !activeTurn(protocol.Thread{State: "running"}) {
		t.Fatal("running turn inactive")
	}
}
