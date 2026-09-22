package server

import (
	"context"
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestDispatchCommitFailureKeepsQueueAndNeverOpensTurn(t *testing.T) {
	e, _, _ := acpEngine(t)
	prompt := protocol.Prompt{ID: "capture", Text: "write once", Revision: 1, Settings: fakeSettings()}
	e.snap.Threads = append(e.snap.Threads, protocol.Thread{ID: "candidate", AgentID: "claude", State: "idle", Queue: []protocol.Prompt{prompt}})
	r := &acpRun{e: e, threadID: "candidate", ctx: context.Background()}
	before := e.current()
	updates := make(chan protocol.Snapshot, 1)
	e.subscribers[updates] = true
	defer delete(e.subscribers, updates)
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	if r.startTurn(dispatchWork{prompt: prompt}, fakeSettings(), nil, nil) {
		t.Fatal("uncommitted prompt authorized provider I/O")
	}
	if !reflect.DeepEqual(before, e.current()) || r.turnDone != nil || r.cancelCh != nil || e.flushErr == nil {
		t.Fatal("failed persistence changed snapshot or live turn")
	}
	select {
	case <-updates:
		t.Fatal("uncommitted dispatch published")
	default:
	}
	if _, ok := r.claim(); ok {
		t.Fatal("failed storage allowed dispatcher to spin")
	}
}

func TestFailedDispatchedPromptIsNotReplayedBySendOrResume(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "boom please")
	waitFor(t, e, "failed turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
	settings := fakeSettings()
	if _, err := e.command(protocol.Command{Version: 1, ID: "followup", Kind: "prompt.send", ThreadID: id, Text: "new request", Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	thread := threadOf(e.current(), id)
	if thread.State != "failed" || thread.Error == "" || !thread.NeedsResume || len(thread.Queue) != 1 || thread.Queue[0].Text != "new request" {
		t.Fatal("Send reopened uncertain work or requeued original")
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: id}); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, id, "prompt-followup")
	prompts, _, _, _ := fleet.last().snapshot()
	if !reflect.DeepEqual(prompts, []string{"boom please", "new request"}) {
		t.Fatalf("replayed failed prompt: %q", prompts)
	}
	capture := activityOf(threadOf(e.current(), id), "prompt-start").Prompt
	if capture == nil || capture.Text != "boom please" || capture.Settings != settings {
		t.Fatal("failed captured prompt lost from history")
	}
}

func TestLongTurnRetainsDurableCaptureAtFailure(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "cancel me")
	waitFor(t, e, "running turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "running" })
	e.mu.Lock()
	thread := threadByID(&e.snap, id)
	capture := *activityOf(*thread, "prompt-start").Prompt
	for range 2 * agent.ActivityLimit {
		thread.Activity = append(thread.Activity, protocol.Activity{Role: "tool", Text: "long stream"})
		agent.TrimActivity(thread)
	}
	retained := activityOf(*thread, "prompt-start").Prompt
	if retained == nil || !reflect.DeepEqual(*retained, capture) || len(thread.Activity) > agent.ActivityLimit {
		e.mu.Unlock()
		t.Fatal("long stream evicted capture or exceeded bound")
	}
	e.mu.Unlock()
	fleet.last().stop()
	waitFor(t, e, "disconnected long turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
	saved, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	failed := threadOf(saved, id)
	retained = activityOf(failed, "prompt-start").Prompt
	if len(failed.Queue) != 0 || !failed.NeedsResume || retained == nil || !reflect.DeepEqual(*retained, capture) {
		t.Fatal("failure lost durable capture or requeued it")
	}
}

func TestLegacyDispatchedQueueCopyRecovery(t *testing.T) {
	original := protocol.Prompt{ID: "old", Text: "write once", Revision: 1, Settings: fakeSettings(), Attachments: []protocol.Attachment{{Content: "captured"}}}
	for _, edited := range []bool{false, true} {
		queueCopy := original
		if edited {
			queueCopy.Revision++
			queueCopy.Text = "explicitly edited recovery input"
		}
		thread := protocol.Thread{AgentID: "claude", State: "failed", QueueRevision: 7,
			Queue:    []protocol.Prompt{queueCopy, {ID: "never-sent", Text: "next"}},
			Activity: []protocol.Activity{{Role: "user", ID: original.ID, TurnID: original.ID, State: "failed", Prompt: &original}},
		}
		recoverACPThread(&thread)
		wantCount := 1
		if edited {
			wantCount = 2
		}
		if len(thread.Queue) != wantCount || !thread.NeedsResume || !reflect.DeepEqual(*thread.Activity[0].Prompt, original) {
			t.Fatalf("recovery lost or replayed capture (edited=%v): %+v", edited, thread)
		}
		if edited {
			if thread.Queue[0].ID == original.ID {
				t.Fatal("edited legacy prompt retained dispatched identity")
			}
			preserved := thread.Queue[0]
			preserved.ID = queueCopy.ID
			if !reflect.DeepEqual(preserved, queueCopy) {
				t.Fatal("reidentifying edited input changed its capture")
			}
		}
		before := clone(protocol.Snapshot{Threads: []protocol.Thread{thread}}).Threads[0]
		recoverACPThread(&thread)
		if !reflect.DeepEqual(before, thread) {
			t.Fatal("recovery was not idempotent")
		}
	}
}

func TestEditedLegacyRecoveryDispatchPreservesOriginalHistory(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "boom please")
	waitFor(t, e, "failed original turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
	e.mu.Lock()
	thread := threadByID(&e.snap, id)
	original := *activityOf(*thread, "prompt-start").Prompt
	edited := original
	edited.Text, edited.Revision = "new corrected input", original.Revision+1
	thread.Queue = []protocol.Prompt{edited}
	recoverACPThread(thread)
	recoveredID := thread.Queue[0].ID
	e.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume-edited", Kind: "thread.resume", ThreadID: id}); err != nil {
		t.Fatal(err)
	}
	finished := waitTurn(t, e, id, recoveredID)
	old := activityOf(finished, original.ID).Prompt
	newInput := activityOf(finished, recoveredID).Prompt
	if old == nil || !reflect.DeepEqual(*old, original) || newInput == nil || newInput.Text != edited.Text || finished.TurnID == original.ID {
		t.Fatal("edited legacy dispatch overwrote original history")
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if !reflect.DeepEqual(prompts, []string{"boom please", "new corrected input"}) {
		t.Fatalf("legacy recovery replayed work: %q", prompts)
	}
}
