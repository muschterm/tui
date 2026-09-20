package server

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func TestThreadCloseReopenRevisionAndPreservation(t *testing.T) {
	e := testEngine(t)
	thread := &e.snap.Threads[0]
	thread.State = "idle"
	thread.Queue = nil
	thread.Requests = nil
	for i := range thread.Children {
		thread.Children[i].State = "completed"
	}
	e.snap.Terminals = []protocol.Terminal{{ID: "session", ThreadID: thread.ID, State: "running", Output: "keep"}}
	before := clone(e.snap)
	c := protocol.Command{Version: 1, ID: "close", Kind: "thread.close", ThreadID: thread.ID}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	closed := e.snap.Threads[0]
	if !closed.Closed || closed.LifecycleRevision != 1 {
		t.Fatalf("%+v", closed)
	}
	closed.Closed = false
	closed.LifecycleRevision = 0
	if !reflect.DeepEqual(closed, before.Threads[0]) || !reflect.DeepEqual(e.snap.Terminals, before.Terminals) {
		t.Fatal("close changed work or terminals")
	}
	c.ID = "stale"
	c.Kind = "thread.reopen"
	if _, err := e.command(c); err == nil {
		t.Fatal("stale lifecycle accepted")
	}
	c.ID = "already-closed"
	c.Kind = "thread.close"
	c.Revision = 1
	if _, err := e.command(c); err != nil || e.snap.Threads[0].LifecycleRevision != 1 {
		t.Fatal("same-state close changed lifecycle", err)
	}
	c.ID = "reopen"
	c.Kind = "thread.reopen"
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	if e.snap.Threads[0].Closed || e.snap.Threads[0].LifecycleRevision != 2 {
		t.Fatal("reopen not persisted")
	}
}

func TestCloseRejectsBusyWithoutSideEffects(t *testing.T) {
	e := testEngine(t)
	before := clone(e.snap)
	if _, err := e.command(protocol.Command{Version: 1, ID: "busy", Kind: "thread.close", ThreadID: "thread-shell"}); err == nil {
		t.Fatal("running close accepted")
	}
	if !reflect.DeepEqual(before, e.snap) {
		t.Fatal("rejection altered work")
	}
}

func TestClosedThreadRejectsNewWorkWithoutSideEffects(t *testing.T) {
	for _, kind := range []string{"prompt.send", "thread.resume"} {
		t.Run(kind, func(t *testing.T) {
			e := testEngine(t)
			e.snap.Threads[0].Closed = true
			before := clone(e.snap)
			if _, err := e.command(protocol.Command{Version: 1, ID: "closed-work", Kind: kind, ThreadID: e.snap.Threads[0].ID, Text: "do work"}); err == nil {
				t.Fatal("work started in a closed thread")
			}
			if !reflect.DeepEqual(before, e.snap) {
				t.Fatal("rejection altered closed thread")
			}
		})
	}
}

func TestDeleteRetryAndOldPromptCannotResurrect(t *testing.T) {
	e := testEngine(t)
	send := protocol.Command{Version: 1, ID: "old-prompt", Kind: "prompt.send", ThreadID: "thread-shell", Text: "captured"}
	if _, err := e.command(send); err != nil {
		t.Fatal(err)
	}
	e.snap.Terminals = []protocol.Terminal{{ID: "owned", ThreadID: "thread-shell", State: "running"}, {ID: "other", ThreadID: "thread-review", State: "running"}}
	del := protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "thread-shell", Revision: 0}
	got, err := e.command(del)
	if err != nil || got.TargetID != "" {
		t.Fatalf("%+v %v", got, err)
	}
	again, err := e.command(del)
	if err != nil || again != got {
		t.Fatal("delete lost-ack retry failed", err)
	}
	if len(e.snap.Threads) != 1 || e.snap.Threads[0].ID != "thread-review" || len(e.snap.Terminals) != 1 || e.snap.Terminals[0].ID != "other" {
		t.Fatal("wrong deletion scope")
	}
	if _, err = e.command(send); err == nil {
		t.Fatal("old prompt retried against deleted thread")
	}
	if err = e.tick(); err != nil {
		t.Fatal(err)
	}
	if len(e.snap.Threads) != 1 {
		t.Fatal("tick resurrected thread")
	}
}

func TestEmptySnapshotRestartDoesNotReseed(t *testing.T) {
	home := t.TempDir()
	// Start once to create the ordinary isolated home and authoritative schema.
	c, stop := startTestServer(t, home)
	ctx := context.Background()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range snap.Threads {
		if _, err = c.Command(ctx, protocol.Command{Version: 1, ID: "delete-" + thread.ID, Kind: "thread.delete", ThreadID: thread.ID, Revision: thread.LifecycleRevision}); err != nil {
			t.Fatal(err)
		}
	}
	stop()
	c, stop = startTestServer(t, home)
	defer stop()
	snap, err = c.Snapshot(ctx)
	if err != nil || len(snap.Threads) != 0 || len(snap.Terminals) != 0 {
		t.Fatalf("reseeded snapshot %+v %v", snap, err)
	}
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	persisted, exists, err := st.Load()
	if err != nil || !exists || len(persisted.Threads) != 0 {
		t.Fatal("empty snapshot not authoritative", err)
	}
}

func TestDeletedCreationRetryCannotRecreateThread(t *testing.T) {
	e := testEngine(t)
	projectPath := t.TempDir()
	add := protocol.Command{Version: 1, ID: "project-for-delete", Kind: "project.add", Path: projectPath}
	project, err := e.command(add)
	if err != nil {
		t.Fatal(err)
	}
	create := protocol.Command{Version: 1, ID: "create-before-delete", Kind: "thread.create", ProjectID: project.TargetID, Text: "private-created-title"}
	created, err := e.command(create)
	if err != nil {
		t.Fatal(err)
	}
	del := protocol.Command{Version: 1, ID: "delete-created", Kind: "thread.delete", ThreadID: created.TargetID}
	if _, err = e.command(del); err != nil {
		t.Fatal(err)
	}
	count := len(e.snap.Threads)
	retry, err := e.command(create)
	if err != nil || retry.State != "deleted" || retry.TargetID != "" || len(e.snap.Threads) != count {
		t.Fatalf("creation resurrected: %+v %v", retry, err)
	}
	create.Text = "different"
	if _, err = e.command(create); err == nil {
		t.Fatal("creation fingerprint identity conflict accepted")
	}
}
