package server

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func steerCommand(t protocol.Thread) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: "steer", Kind: "queue.steer", ThreadID: t.ID, TargetID: t.Queue[0].ID, Revision: t.QueueRevision, ExpectedTurnID: t.TurnID}
}

func TestQueueSteerPreservesTurnAndCapturedInput(t *testing.T) {
	for _, state := range []string{"running", "waiting"} {
		t.Run(state, func(t *testing.T) {
			e := testEngine(t)
			thread := &e.snap.Threads[0]
			thread.State, thread.Tick = state, 3
			thread.Selected.Effort = "high" // Composer changes are independent of captured/effective settings.
			if state == "waiting" {
				thread.Requests = append(thread.Requests, e.snap.Threads[1].Requests...)
			}
			thread.Queue[0].Attachments = []protocol.Attachment{{Kind: "file", Name: "source", Source: "/changed", Content: "accepted snapshot"}}
			before := clone(e.snap).Threads[0]
			c := steerCommand(before)
			r, err := e.command(c)
			if err != nil {
				t.Fatal(err)
			}
			after := e.snap.Threads[0]
			if r.State != "fixture-delivered" || r.TargetID != c.TargetID || len(after.Queue) != 0 || after.QueueRevision != before.QueueRevision+1 {
				t.Fatalf("invalid delivery: %+v", r)
			}
			a := after.Activity[len(after.Activity)-1]
			if a.Role != "user" || a.TurnID != before.TurnID || a.Text != before.Queue[0].Text || !reflect.DeepEqual(a.Prompt, &before.Queue[0]) {
				t.Fatalf("capture lost: %+v", a)
			}
			after.Activity, after.Queue, after.QueueRevision = before.Activity, before.Queue, before.QueueRevision
			if !reflect.DeepEqual(after, before) {
				t.Fatal("steering altered execution, settings, plan, children or requests")
			}
			stored, _, err := e.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			e.snap = stored
			again, err := e.command(c)
			if err != nil || again != r || len(e.snap.Threads[0].Activity) != len(before.Activity)+1 {
				t.Fatalf("durable retry duplicated delivery: %+v %v", again, err)
			}
		})
	}
}

func TestQueueSteerRejectionsPreserveQueue(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*engine, *protocol.Command)
	}{
		{"stale queue", func(e *engine, c *protocol.Command) { c.Revision-- }},
		{"stale turn", func(e *engine, c *protocol.Command) { c.ExpectedTurnID = "prior-turn" }},
		{"missing turn", func(e *engine, c *protocol.Command) { c.ExpectedTurnID = "" }},
		{"idle", func(e *engine, c *protocol.Command) { e.snap.Threads[0].State = "idle" }},
		{"resume required", func(e *engine, c *protocol.Command) { e.snap.Threads[0].NeedsResume = true }},
		{"closed", func(e *engine, c *protocol.Command) { e.snap.Threads[0].Closed = true }},
		{"agent", func(e *engine, c *protocol.Command) { e.snap.Threads[0].Agent = "other agent" }},
		{"capability", func(e *engine, c *protocol.Command) { e.snap.Capabilities = []string{"fixture-agent"} }},
		{"settings", func(e *engine, c *protocol.Command) { e.snap.Threads[0].Queue[0].Settings.Effort = "high" }},
		{"removed", func(e *engine, c *protocol.Command) { c.TargetID = "removed-prompt" }},
		{"already completed", func(e *engine, c *protocol.Command) { tickFixture(t, e, fixtureTurnTicks) }},
		{"already dispatched", func(e *engine, c *protocol.Command) { tickFixture(t, e, fixtureTurnTicks+1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			c := steerCommand(e.snap.Threads[0])
			tc.mutate(e, &c)
			before := clone(e.snap)
			if _, err := e.command(c); err == nil {
				t.Fatal("unsafe steering accepted")
			}
			if !reflect.DeepEqual(e.snap, before) {
				t.Fatal("rejected steering mutated state")
			}
		})
	}
}

func TestQueueSteerCompletionKeepsTurnIdentity(t *testing.T) {
	e := testEngine(t)
	before := e.snap.Threads[0].TurnID
	c := steerCommand(e.snap.Threads[0])
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "next", Kind: "prompt.send", ThreadID: c.ThreadID, Text: "next turn"}); err != nil {
		t.Fatal(err)
	}
	tickFixture(t, e, fixtureTurnTicks)
	th := e.snap.Threads[0]
	if th.TurnID != before || !fixtureTurnCompleted(&th) {
		t.Fatal("steering changed the completion identity")
	}
	count := 0
	for _, a := range th.Activity {
		if a.ID == "result-"+before {
			count++
		}
	}
	if count != 1 {
		t.Fatal("completion did not use original turn")
	}
	tickFixture(t, e, 1)
	if e.snap.Threads[0].TurnID != "prompt-next" || e.snap.Threads[0].Tick != 1 {
		t.Fatal("subsequent dispatch did not create fresh turn")
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "later", Kind: "prompt.send", ThreadID: c.ThreadID, Text: "later input"}); err != nil {
		t.Fatal(err)
	}
	stale := steerCommand(e.snap.Threads[0])
	stale.ID = "stale-turn"
	stale.ExpectedTurnID = before
	if _, err := e.command(stale); err == nil {
		t.Fatal("old turn accepted after later dispatch")
	}
}

func TestQueueSteerRestartMigrationAndResume(t *testing.T) {
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	snap := fixture.Initial()
	snap.Capabilities = []string{"fixture-agent"}
	snap.Threads[0].TurnID = ""
	if err = st.Save(snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	client, stop := startTestServer(t, home)
	defer func() { stop() }()
	ctx := context.Background()
	restored, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(restored.Capabilities, "fixture-steering") || restored.Threads[0].TurnID != "intro" || !restored.Threads[0].NeedsResume {
		t.Fatal("restart migration lost identity or resume gate")
	}
	c := steerCommand(restored.Threads[0])
	if _, err = client.Command(ctx, c); err == nil {
		t.Fatal("restart steered without resume")
	}
	if _, err = client.Command(ctx, protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: c.ThreadID}); err != nil {
		t.Fatal(err)
	}
	accepted, err := client.Command(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop = func() {}
	client, stop = startTestServer(t, home)
	beforeRetry, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeRetry.Threads[0].NeedsResume {
		t.Fatal("restart did not gate unfinished steered turn")
	}
	replay, err := client.Command(ctx, c)
	if err != nil || replay != accepted {
		t.Fatalf("restart lost accepted receipt: %+v %v", replay, err)
	}
	afterRetry, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRetry, afterRetry) {
		t.Fatal("restart retry resumed work or duplicated steering")
	}
	captures := 0
	for _, activity := range afterRetry.Threads[0].Activity {
		if activity.ID == c.TargetID && activity.Prompt != nil {
			captures++
		}
	}
	if captures != 1 {
		t.Fatalf("restart retained %d steering captures", captures)
	}
}

func TestQueueSteerStorageFailurePreservesQueue(t *testing.T) {
	e := testEngine(t)
	before := clone(e.snap)
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(steerCommand(before.Threads[0])); err == nil {
		t.Fatal("closed storage accepted steering")
	}
	if !reflect.DeepEqual(e.snap, before) {
		t.Fatal("failed durable delivery removed input")
	}
}

func TestConcurrentQueueSteerDeliversOnce(t *testing.T) {
	e := testEngine(t)
	before := clone(e.snap)
	first := steerCommand(before.Threads[0])
	second := first
	second.ID = "competing-steer"
	type outcome struct {
		command protocol.Command
		receipt protocol.Receipt
		err     error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, command := range []protocol.Command{first, second} {
		go func() {
			ready <- struct{}{}
			<-start
			receipt, err := e.command(command)
			results <- outcome{command, receipt, err}
		}()
	}
	<-ready
	<-ready
	close(start)
	accepted := 0
	var winner outcome
	for range 2 {
		result := <-results
		if result.err == nil {
			accepted++
			winner = result
		} else {
			var protocolErr *protocol.Error
			if !errors.As(result.err, &protocolErr) || protocolErr.Code != "stale_revision" {
				t.Fatalf("unexpected competing failure: %v", result.err)
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("%d concurrent steering commands accepted", accepted)
	}
	after := clone(e.snap)
	thread := after.Threads[0]
	if after.Revision != before.Revision+1 || thread.QueueRevision != before.Threads[0].QueueRevision+1 || len(thread.Queue) != 0 || len(thread.Activity) != len(before.Threads[0].Activity)+1 {
		t.Fatal("concurrent steering changed queue/history more than once")
	}
	capture := thread.Activity[len(thread.Activity)-1]
	if capture.ID != first.TargetID || capture.TurnID != first.ExpectedTurnID || !reflect.DeepEqual(capture.Prompt, &before.Threads[0].Queue[0]) {
		t.Fatal("winning capture lost queued input or turn identity")
	}
	replay, err := e.command(winner.command)
	if err != nil || replay != winner.receipt || !reflect.DeepEqual(e.snap, after) {
		t.Fatalf("winner retry changed delivery: %+v %v", replay, err)
	}
	durable, _, err := e.store.Load()
	if err != nil || !reflect.DeepEqual(durable, after) {
		t.Fatalf("concurrent outcome not durable: %v", err)
	}
}
