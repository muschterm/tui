package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func encodedSize(t *testing.T, s protocol.Snapshot) int {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func isCode(err error, code string) bool {
	var pe *protocol.Error
	return errors.As(err, &pe) && pe.Code == code
}

func TestQueuedCapturesStayBoundedThroughDispatch(t *testing.T) {
	e := testEngine(t)
	// Escaped content is the worst case for any copy made at dispatch.
	content := strings.Repeat("\"", 60000)
	attachments := make([]protocol.Attachment, 8)
	for i := range attachments {
		attachments[i] = protocol.Attachment{Kind: "file", Name: fmt.Sprint("capture-", i), Content: content}
	}
	accepted := 0
	for i := range 32 {
		_, err := e.command(protocol.Command{Version: 1, ID: fmt.Sprint("large-", i), Kind: "prompt.send", ThreadID: "thread-shell", Text: "review", Attachments: attachments})
		if err != nil {
			if !isCode(err, "capacity") {
				t.Fatal(err)
			}
			break
		}
		accepted++
	}
	if accepted < 2 || accepted == 32 {
		t.Fatalf("scenario did not reach the snapshot bound: %d accepted", accepted)
	}
	for range (accepted + 2) * 2 {
		// Skip mid-turn ticks: one tick completes the turn, the next dispatches.
		if e.snap.Threads[0].Tick < fixtureTurnTicks-1 {
			e.snap.Threads[0].Tick = fixtureTurnTicks - 1
		}
		tickFixture(t, e, 1)
		if size := encodedSize(t, e.snap); size > snapshotLimit {
			t.Fatalf("dispatch grew snapshot to %d bytes", size)
		}
	}
	thread := e.snap.Threads[0]
	if len(thread.Queue) != 0 {
		t.Fatalf("%d prompts were not dispatched", len(thread.Queue))
	}
	captures := 0
	for _, a := range thread.Activity {
		if a.Prompt != nil && len(a.Prompt.Attachments) == 8 && a.Prompt.Attachments[7].Content == content {
			captures++
			if len(a.Detail) > 4096 || !strings.Contains(a.Detail, `"Size":60000`) {
				t.Fatalf("detail is not a compact summary: %d bytes", len(a.Detail))
			}
		}
	}
	if captures != accepted {
		t.Fatalf("retained %d of %d accepted captures", captures, accepted)
	}
	// A bounded snapshot still accepts a non-growing command against a thread
	// that is actually working, and refuses Stop for a finished turn rather
	// than demanding a Resume with nothing to resume.
	if _, err := e.command(protocol.Command{Version: 1, ID: "after-dispatch", Kind: "thread.interrupt", ThreadID: "thread-review"}); err != nil {
		t.Fatal("bounded snapshot rejected interrupt:", err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "idle-stop", Kind: "thread.interrupt", ThreadID: "thread-shell"}); !isCode(err, "not_active") {
		t.Fatal("Stop accepted for a thread with no active turn:", err)
	}
}

func TestOverLimitHomeAcceptsNonGrowingCommands(t *testing.T) {
	e := testEngine(t)
	queued, err := e.command(protocol.Command{Version: 1, ID: "queued", Kind: "prompt.send", ThreadID: "thread-shell", Text: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	// A legacy home can already exceed the bound.
	e.snap.Threads[1].Activity = append(e.snap.Threads[1].Activity, protocol.Activity{ID: "legacy", Role: "user", Detail: strings.Repeat("x", snapshotLimit)})
	if err := e.store.Save(e.snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	revision, queue := e.snap.Revision, len(e.snap.Threads[0].Queue)
	_, err = e.command(protocol.Command{Version: 1, ID: "grow", Kind: "prompt.send", ThreadID: "thread-shell", Text: "more"})
	if !isCode(err, "capacity") || e.snap.Revision != revision || len(e.snap.Threads[0].Queue) != queue {
		t.Fatal("growing prompt was not rejected atomically:", err)
	}
	if _, err = e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: "thread-shell"}); err != nil || e.snap.Threads[0].State != "interrupted" {
		t.Fatal("over-limit home rejected interrupt:", err)
	}
	if _, err = e.command(protocol.Command{Version: 1, ID: "remove", Kind: "queue.remove", ThreadID: "thread-shell", TargetID: queued.TargetID, Revision: e.snap.Threads[0].QueueRevision}); err != nil || len(e.snap.Threads[0].Queue) != queue-1 {
		t.Fatal("over-limit home rejected queue removal:", err)
	}
	settings := e.snap.AppSettings
	settings.ContinueAfterRestart = !settings.ContinueAfterRestart
	if _, err = e.command(protocol.Command{Version: 1, ID: "settings", Kind: "settings.update", Revision: settings.Revision, AppSettings: &settings}); err != nil {
		t.Fatal("over-limit home rejected settings:", err)
	}
	if _, err = e.command(protocol.Command{Version: 1, ID: "stop-review", Kind: "thread.interrupt", ThreadID: "thread-review"}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "thread-review", Revision: e.snap.Threads[1].LifecycleRevision}); err != nil || encodedSize(t, e.snap) > snapshotLimit {
		t.Fatal("over-limit home could not delete its largest thread:", err)
	}
}

func TestLegacyDuplicateDetailCompaction(t *testing.T) {
	p := protocol.Prompt{ID: "prompt-old", Text: "keep", Attachments: []protocol.Attachment{{Kind: "file", Name: "a", Content: "captured bytes"}}}
	duplicate, _ := json.Marshal(p)
	s := protocol.Snapshot{Threads: []protocol.Thread{{Activity: []protocol.Activity{{ID: "prompt-old", Prompt: &p, Detail: string(duplicate)}, {ID: "other", Prompt: &p, Detail: "hand-written"}, {ID: "bare", Detail: string(duplicate)}}}}}
	compactPromptDetails(&s)
	a := s.Threads[0].Activity
	if strings.Contains(a[0].Detail, "captured bytes") || a[0].Prompt.Attachments[0].Content != "captured bytes" {
		t.Fatal("duplicate detail not compacted or capture lost")
	}
	if a[1].Detail != "hand-written" || a[2].Detail != string(duplicate) {
		t.Fatal("compaction changed detail that is not a retained duplicate")
	}
}
