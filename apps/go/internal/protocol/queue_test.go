package protocol_test

import (
	"testing"

	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestQueueSteerAvailability(t *testing.T) {
	s := fixture.Initial()
	th := s.Threads[0]
	p := th.Queue[0]
	for _, state := range []string{"running", "waiting"} {
		th.State = state
		if reason := protocol.QueueSteerBlocked(s.Capabilities, th, p); reason != "" {
			t.Fatalf("%s: %s", state, reason)
		}
	}
	th.Activity = append(th.Activity, protocol.Activity{ID: "result-" + th.TurnID, State: "completed"})
	if protocol.QueueSteerBlocked(s.Capabilities, th, p) == "" {
		t.Fatal("completed turn exposes steering")
	}
}
