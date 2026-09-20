package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func testEngine(t *testing.T) *engine {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := fixture.Initial()
	if err := st.Save(s, nil, nil); err != nil {
		t.Fatal(err)
	}
	return &engine{snap: s, store: st, subscribers: map[chan protocol.Snapshot]bool{}}
}
func TestDurableCommandIdentityAndQueueRevision(t *testing.T) {
	e := testEngine(t)
	c := protocol.Command{Version: 1, ID: "send", Kind: "prompt.send", ThreadID: "thread-shell", Text: "keep snapshot", Attachments: []protocol.Attachment{{Name: "file", Content: "captured content"}}}
	r, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.command(c)
	if err != nil || again != r || len(e.snap.Threads[0].Queue) != 2 {
		t.Fatalf("duplicate changed outcome: %#v %v", again, err)
	}
	c.Text = "different"
	if _, err = e.command(c); err == nil {
		t.Fatal("reused identity accepted")
	}
	q := e.snap.Threads[0].QueueRevision
	edit := protocol.Command{Version: 1, ID: "edit", Kind: "queue.edit", ThreadID: c.ThreadID, TargetID: r.TargetID, Revision: q, Text: "edited"}
	if _, err = e.command(edit); err != nil {
		t.Fatal(err)
	}
	edit.ID = "stale"
	if _, err = e.command(edit); err == nil {
		t.Fatal("stale queue edit accepted")
	}
	p := e.snap.Threads[0].Queue[1]
	if p.Attachments[0].Content != "captured content" {
		t.Fatal("edit changed attachment capture")
	}
	stored, _, err := e.store.Load()
	if err != nil || stored.Revision != e.snap.Revision {
		t.Fatal("state not durable", err)
	}
}
func TestCompetingAnswersAndResumeGate(t *testing.T) {
	e := testEngine(t)
	c := protocol.Command{Version: 1, ID: "answer", Kind: "request.answer", ThreadID: "thread-review", TargetID: "approval-review", Revision: 1, Answers: []string{"Allow once"}}
	e.snap.Threads[1].NeedsResume = true
	if _, err := e.command(c); err == nil {
		t.Fatal("restart request accepted before resume")
	}
	e.snap.Threads[1].NeedsResume = false
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	c.ID = "other-client"
	if _, err := e.command(c); err == nil {
		t.Fatal("second answer accepted")
	}
}
func TestSlowSubscriberIsExplicitlyDisconnected(t *testing.T) {
	e := testEngine(t)
	ch := make(chan protocol.Snapshot, 1)
	e.subscribers[ch] = true
	e.publish()
	e.publish()
	if len(e.subscribers) != 0 {
		t.Fatal("slow subscriber retained")
	}
	<-ch
	if _, ok := <-ch; ok {
		t.Fatal("overflow channel not closed")
	}
}
func startTestServer(t *testing.T, home string) (*client.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, home) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("server exited before readiness: %v", err)
		default:
		}
		d, err := client.Discover(home)
		if err == nil {
			c := client.New(d)
			if _, err = c.Snapshot(ctx); err == nil {
				return c, func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("server failed to start")
	return nil, nil
}
func TestAuthenticationCatchupRestartAndViews(t *testing.T) {
	home := t.TempDir()
	c, stop := startTestServer(t, home)
	ctx := context.Background()
	resp, err := http.Get(c.Discovery.URL + "/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthorized snapshot exposed")
	}
	if err := Serve(ctx, home); err == nil {
		t.Fatal("concurrent second server acquired home")
	}
	before, _ := c.Snapshot(ctx)
	time.Sleep(1100 * time.Millisecond)
	after, _ := c.Snapshot(ctx)
	if after.Threads[0].Tick <= before.Threads[0].Tick {
		t.Fatal("detached fixture did not advance")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	received := make(chan protocol.Snapshot, 1)
	go func() {
		_ = c.Watch(watchCtx, func(s protocol.Snapshot) {
			select {
			case received <- s:
			default:
			}
		})
	}()
	select {
	case got := <-received:
		if got.Revision < after.Revision {
			t.Fatal("initial watch snapshot stale")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch missing")
	}
	cancel()
	if err := c.SaveView(ctx, "a", json.RawMessage(`{"draft":"preserved"}`)); err != nil {
		t.Fatal(err)
	}
	viewVersion, err := c.LoadView(ctx, "a")
	if err != nil || viewVersion.Revision != 1 {
		t.Fatalf("view revision: %+v %v", viewVersion, err)
	}
	if _, err = c.PutView(ctx, "a", json.RawMessage(`{"draft":"late"}`), 0); err == nil {
		t.Fatal("HTTP view CAS accepted stale revision")
	}
	other, err := c.View(ctx, "b")
	if err != nil || string(other) != "{}" {
		t.Fatal("views shared client navigation")
	}
	stop()
	c, stop = startTestServer(t, home)
	defer stop()
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Threads[0].NeedsResume || s.Threads[0].State != "interrupted" {
		t.Fatal("restart resumed work")
	}
	tick := s.Threads[0].Tick
	time.Sleep(1100 * time.Millisecond)
	s, _ = c.Snapshot(ctx)
	if s.Threads[0].Tick != tick {
		t.Fatal("restart gate ticked")
	}
	view, _ := c.View(ctx, "a")
	if string(view) != `{"draft":"preserved"}` {
		t.Fatal("view not restored")
	}
}

func TestTerminalCloseAndPausedQueueSettings(t *testing.T) {
	e := testEngine(t)
	c := protocol.Command{Version: 1, ID: "terminal", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "owner"}
	r, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	closeCmd := protocol.Command{Version: 1, ID: "close", Kind: "terminal.close", ThreadID: c.ThreadID, TargetID: r.TargetID}
	if _, err = e.command(closeCmd); err != nil {
		t.Fatal(err)
	}
	if _, err = e.command(closeCmd); err != nil {
		t.Fatal(err)
	}
	if len(e.snap.Terminals) != 1 || e.snap.Terminals[0].State != "ended" {
		t.Fatal("close did not end existing session")
	}
	c.ID = "new-terminal"
	r2, err := e.command(c)
	if err != nil || r2.TargetID == r.TargetID {
		t.Fatal("reopen reused ended identity")
	}
	e.snap.Threads[0].NeedsResume = true
	e.snap.Threads[0].State = "interrupted"
	settings := e.snap.Threads[0].Selected
	settings.Effort = "high"
	send := protocol.Command{Version: 1, ID: "paused-send", Kind: "prompt.send", ThreadID: c.ThreadID, Text: "preserve selection", Settings: &settings}
	if _, err = e.command(send); err != nil {
		t.Fatal(err)
	}
	if err = e.tick(); err != nil {
		t.Fatal(err)
	}
	thread := e.snap.Threads[0]
	if !thread.NeedsResume || len(thread.Queue) != 2 || thread.Queue[1].Settings.Effort != "high" {
		t.Fatal("queue mutation resumed or lost selection")
	}
	settings.Model = "unsupported"
	send.ID = "bad-settings"
	if _, err = e.command(send); err == nil {
		t.Fatal("unsupported settings accepted")
	}
}

func TestStopConfirmsDurableShutdown(t *testing.T) {
	home := t.TempDir()
	_, cleanup := startTestServer(t, home)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lifecycle.Stop(ctx, home); err != nil {
		t.Fatal(err)
	}
}

func tickFixture(t *testing.T, e *engine, count int) {
	t.Helper()
	for range count {
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFiniteFixtureTurnsPreserveQueuedCaptures(t *testing.T) {
	e := testEngine(t)
	settings := e.snap.Threads[0].Selected
	settings.Effort = "high"
	c := protocol.Command{Version: 1, ID: "captured", Kind: "prompt.send", ThreadID: "thread-shell", Text: "review capture", Settings: &settings, Attachments: []protocol.Attachment{{Name: "file", Content: "accepted content"}}}
	r, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	tickFixture(t, e, fixtureTurnTicks-1)
	if e.snap.Threads[0].State != "running" || len(e.snap.Threads[0].Queue) != 2 {
		t.Fatal("active turn ended or dequeued the next prompt early")
	}
	tickFixture(t, e, 1)
	for _, step := range e.snap.Threads[0].Plan {
		if step.State != "completed" {
			t.Fatal("completed turn retained incomplete plan")
		}
	}
	for _, child := range e.snap.Threads[0].Children {
		if child.State != "completed" {
			t.Fatal("completed turn retained running child")
		}
	}
	tickFixture(t, e, fixtureTurnTicks+1)
	thread := e.snap.Threads[0]
	if thread.State != "running" || thread.Tick != 1 || thread.Effective != settings || len(thread.Queue) != 0 {
		t.Fatalf("captured prompt not dispatched separately: %+v", thread)
	}
	var captured protocol.Prompt
	for _, activity := range thread.Activity {
		if activity.ID == r.TargetID {
			if err := json.Unmarshal([]byte(activity.Detail), &captured); err != nil {
				t.Fatal(err)
			}
		}
	}
	if captured.Settings != settings || len(captured.Attachments) != 1 || captured.Attachments[0].Content != "accepted content" {
		t.Fatalf("dispatch lost accepted capture: %+v", captured)
	}
	tickFixture(t, e, fixtureTurnTicks-1)
	if e.snap.Threads[0].State != "idle" {
		t.Fatal("finished fixture did not become idle")
	}
	revision := e.snap.Revision
	tickFixture(t, e, 3)
	if e.snap.Revision != revision {
		t.Fatal("idle fixture continued ticking")
	}
	if again, err := e.command(c); err != nil || again != r {
		t.Fatalf("completed prompt retry changed receipt: %+v %v", again, err)
	}
	results := 0
	for _, activity := range e.snap.Threads[0].Activity {
		if activity.ID == "result-"+r.TargetID {
			results++
		}
	}
	if results != 1 || e.snap.Threads[0].State != "idle" {
		t.Fatal("retry executed a completed prompt again")
	}
	stored, _, err := e.store.Load()
	if err != nil || stored.Threads[0].State != "idle" || stored.Threads[0].Tick != fixtureTurnTicks {
		t.Fatal("completion not durable", err)
	}
}

func TestIdleSendAndInterruptedResumeContinueFiniteTurn(t *testing.T) {
	e := testEngine(t)
	tickFixture(t, e, fixtureTurnTicks*2)
	c := protocol.Command{Version: 1, ID: "idle-send", Kind: "prompt.send", ThreadID: "thread-shell", Text: "new review"}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	if e.snap.Threads[0].State != "running" || e.snap.Threads[0].Tick != 0 || len(e.snap.Threads[0].Queue) != 0 {
		t.Fatal("idle send did not start a fresh turn")
	}
	tickFixture(t, e, 2)
	if _, err := e.command(protocol.Command{Version: 1, ID: "interrupt", Kind: "thread.interrupt", ThreadID: c.ThreadID}); err != nil {
		t.Fatal(err)
	}
	c.ID, c.Text = "while-paused", "next review"
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	tickFixture(t, e, 10)
	if e.snap.Threads[0].Tick != 2 || !e.snap.Threads[0].NeedsResume || len(e.snap.Threads[0].Queue) != 1 {
		t.Fatal("sending while interrupted implicitly resumed work")
	}
	// Restore durable progress to exercise recovery without an in-memory counter.
	restored, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	e.snap = restored
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: c.ThreadID}); err != nil {
		t.Fatal(err)
	}
	tickFixture(t, e, fixtureTurnTicks-2)
	if e.snap.Threads[0].Tick != fixtureTurnTicks || len(e.snap.Threads[0].Queue) != 1 {
		t.Fatal("resume restarted or skipped preserved work")
	}
	tickFixture(t, e, fixtureTurnTicks)
	if e.snap.Threads[0].State != "idle" {
		t.Fatal("resumed queued work never completed")
	}
}

func TestLegacyUnboundedFixtureFinishesBeforeQueuedPrompt(t *testing.T) {
	e := testEngine(t)
	e.snap.Threads[0].Tick = 100
	tickFixture(t, e, 1)
	if !fixtureTurnCompleted(&e.snap.Threads[0]) || len(e.snap.Threads[0].Queue) != 1 {
		t.Fatal("legacy turn skipped completion before dispatch")
	}
	tickFixture(t, e, fixtureTurnTicks)
	if e.snap.Threads[0].State != "idle" {
		t.Fatal("legacy fixture never finished")
	}
}

func TestRestartKeepsCompletedFixtureIdle(t *testing.T) {
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	e := &engine{snap: fixture.Initial(), store: st, subscribers: map[chan protocol.Snapshot]bool{}}
	tickFixture(t, e, fixtureTurnTicks*2)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	c, stop := startTestServer(t, home)
	defer stop()
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Threads[0].State != "idle" || s.Threads[0].NeedsResume {
		t.Fatal("restart converted completed work to interrupted work")
	}
	if !s.Threads[1].NeedsResume {
		t.Fatal("restart did not gate unfinished work")
	}
}

func TestFixtureRetentionPreservesRetainedCaptures(t *testing.T) {
	thread := protocol.Thread{ID: "history"}
	capture := protocol.Prompt{ID: "accepted", Text: "keep context", Attachments: []protocol.Attachment{{Content: "captured bytes"}}}
	detail, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	for range 130 {
		thread.Activity = append(thread.Activity, protocol.Activity{Role: "user", Detail: string(detail)})
	}
	trimFixtureActivity(&thread)
	if len(thread.Activity) != 128 || thread.Activity[0].ID != "history-limit-history" {
		t.Fatal("retention did not reserve a separate notice slot")
	}
	for _, activity := range thread.Activity[1:] {
		if activity.Detail != string(detail) {
			t.Fatal("retention notice changed a retained capture")
		}
	}
	thread.Activity = append(thread.Activity, protocol.Activity{Role: "agent", Text: "new result"})
	trimFixtureActivity(&thread)
	notices := 0
	for _, activity := range thread.Activity {
		if activity.ID == "history-limit-history" {
			notices++
		}
	}
	if len(thread.Activity) != 128 || notices != 1 || thread.Activity[127].Text != "new result" {
		t.Fatal("subsequent retention duplicated its notice or removed newest output")
	}
}
