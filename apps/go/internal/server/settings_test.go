package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func TestSettingsPersistCASAndRetry(t *testing.T) {
	e := testEngine(t)
	if e.snap.AppSettings.ContinueAfterRestart || e.snap.AppSettings.WorkspaceDefault != "checkout" {
		t.Fatal("unsafe defaults")
	}
	before := clone(e.snap)
	c := protocol.Command{Version: 1, ID: "settings", Kind: "settings.update", Revision: e.snap.AppSettings.Revision, AppSettings: &protocol.AppSettings{WorkspaceDefault: "worktree", ContinueAfterRestart: true}}
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.command(c); err != nil || again != receipt {
		t.Fatal("retry changed result", err)
	}
	if !reflect.DeepEqual(before.Threads, e.snap.Threads) {
		t.Fatal("defaults changed existing threads")
	}
	c.ID = "stale"
	if _, err := e.command(c); err == nil {
		t.Fatal("stale settings accepted")
	}
	saved, _, err := e.store.Load()
	if err != nil || !reflect.DeepEqual(saved.AppSettings, e.snap.AppSettings) {
		t.Fatal("settings not durable", err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "new", Kind: "thread.create", ProjectID: e.snap.Projects[0].ID}); err == nil || !strings.Contains(err.Error(), "unsupported_workspace") {
		t.Fatal("worktree silently fell back", err)
	}
	p := e.snap.Projects[0]
	update := protocol.Command{Version: 1, ID: "override", Kind: "project.update", ProjectID: p.ID, Revision: p.Revision, ProjectSettings: &protocol.ProjectSettings{Name: "Renamed", Icon: "rocket", Color: "teal", WorkspaceDefault: "checkout"}}
	if _, err := e.command(update); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "new-checkout", Kind: "thread.create", ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if e.snap.Threads[0].Checkout != before.Threads[0].Checkout || e.snap.Threads[0].Project != "Renamed" || e.snap.Projects[0].Revision != p.Revision+2 {
		t.Fatal("project edit or membership revision invalid")
	}
	update.ID = "stale-project"
	if _, err := e.command(update); err == nil {
		t.Fatal("stale project write accepted")
	}
	for _, value := range []protocol.ProjectSettings{{Name: "bad\x1bname"}, {Name: "ok", Icon: "raw-image"}, {Name: "ok", Color: "#fff"}, {Name: "ok", WorkspaceDefault: "unknown"}} {
		update.ID += "x"
		update.Revision = e.snap.Projects[0].Revision
		update.ProjectSettings = &value
		if _, err := e.command(update); err == nil {
			t.Fatal("invalid project settings accepted", value)
		}
	}
}

func TestRestartContinuationEligibility(t *testing.T) {
	base := fixture.Initial().Threads[0]
	base.Requests = nil
	for _, tc := range []struct {
		name          string
		enabled, want bool
		edit          func(*protocol.Thread)
	}{
		{"default off", false, false, func(*protocol.Thread) {}},
		{"crashed running fixture", true, true, func(*protocol.Thread) {}},
		{"graceful shutdown marker", true, true, func(t *protocol.Thread) { t.State = "interrupted"; t.NeedsResume = true; t.RestartEligible = true }},
		{"manual stop", true, false, func(t *protocol.Thread) { t.State = "interrupted"; t.NeedsResume = true }},
		{"idle", true, false, func(t *protocol.Thread) { t.State = "idle" }},
		{"closed", true, false, func(t *protocol.Thread) { t.Closed = true }},
		{"unknown agent", true, false, func(t *protocol.Thread) { t.Agent = "other" }},
		{"question", true, false, func(t *protocol.Thread) {
			t.Requests = []protocol.Request{{ID: "q", State: "pending", Kind: "question", Revision: 1}}
		}},
		{"approval", true, false, func(t *protocol.Thread) {
			t.Requests = []protocol.Request{{ID: "a", State: "pending", Kind: "approval", Revision: 1}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := clone(protocol.Snapshot{AppSettings: protocol.AppSettings{ContinueAfterRestart: tc.enabled}, Threads: []protocol.Thread{base}})
			tc.edit(&s.Threads[0])
			recoverThreads(&s)
			got := s.Threads[0]
			if (got.State == "running" && !got.NeedsResume) != tc.want {
				t.Fatalf("wrong recovery: %+v", got)
			}
			for _, r := range got.Requests {
				if r.State != "pending" || len(r.Answers) > 0 || len(r.QuestionAnswers) > 0 || r.Delivery != "revalidation-required" {
					t.Fatal("request implicitly answered", r)
				}
			}
		})
	}
	s := protocol.Snapshot{Threads: []protocol.Thread{base}}
	s.Threads[0].RestartEligible = true
	if _, err := apply(&s, protocol.Command{Kind: "thread.interrupt", ThreadID: base.ID}); err != nil || s.Threads[0].RestartEligible {
		t.Fatal("manual Stop retained restart eligibility", err)
	}
}

func TestProjectRemovalAtomicPurgeRetryAndFiles(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	add := protocol.Command{Version: 1, ID: "add-project", Kind: "project.add", Path: dir}
	receipt, err := e.command(add)
	if err != nil {
		t.Fatal(err)
	}
	projectID := receipt.TargetID
	create := protocol.Command{Version: 1, ID: "create-owned", Kind: "thread.create", ProjectID: projectID, Text: "private title"}
	created, err := e.command(create)
	if err != nil {
		t.Fatal(err)
	}
	view := json.RawMessage(`{"Active":"` + created.TargetID + `","Threads":{"` + created.TargetID + `":{"Draft":"private draft"},"thread-shell":{"Draft":"keep draft"}},"Pending":{"ID":"add-project","Kind":"project.add"}}`)
	if err := e.store.SaveView("client", view); err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, p := range e.snap.Projects {
		if p.ID == projectID {
			revision = p.Revision
		}
	}
	remove := protocol.Command{Version: 1, ID: "remove-project", Kind: "project.remove", ProjectID: projectID, Revision: revision - 1}
	if _, err := e.command(remove); err == nil {
		t.Fatal("stale membership accepted")
	}
	remove.Revision = revision
	if _, err := e.command(remove); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(remove); err != nil {
		t.Fatal("removal retry failed", err)
	}
	for _, c := range []protocol.Command{add, create} {
		r, err := e.command(c)
		if err != nil || r.State != "deleted" || r.TargetID != "" {
			t.Fatal("creation retry resurrected removed data", r, err)
		}
	}
	saved, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range saved.Projects {
		if p.ID == projectID {
			t.Fatal("project survived")
		}
	}
	for _, th := range saved.Threads {
		if th.ProjectID == projectID {
			t.Fatal("thread survived")
		}
	}
	got, err := e.store.View("client")
	if err != nil || strings.Contains(string(got), "private") || strings.Contains(string(got), "add-project") || !strings.Contains(string(got), "keep draft") {
		t.Fatal("view purge lost unrelated work or kept removed data", string(got), err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "keep" {
		t.Fatal("disk files changed", err)
	}
	busy := protocol.Command{Version: 1, ID: "busy-remove", Kind: "project.remove", ProjectID: e.snap.Projects[0].ID, Revision: e.snap.Projects[0].Revision}
	before := clone(e.snap)
	if _, err := e.command(busy); err == nil || !reflect.DeepEqual(before, e.snap) {
		t.Fatal("busy project removal changed work", err)
	}
}

func TestServerRestartContinuationOptInAndManualStop(t *testing.T) {
	for _, manuallyStopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "manually stopped"}[manuallyStopped], func(t *testing.T) {
			home := t.TempDir()
			st, err := storage.Open(filepath.Join(home, "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			snap := fixture.Initial()
			snap.AppSettings.ContinueAfterRestart = true
			snap.Threads[0].Requests = nil
			if err := st.Save(snap, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			client, stop := startTestServer(t, home)
			state, err := client.Snapshot(context.Background())
			if err != nil {
				stop()
				t.Fatal(err)
			}
			if state.Threads[0].State != "running" || state.Threads[0].NeedsResume {
				stop()
				t.Fatal("eligible crash recovery did not run")
			}
			if manuallyStopped {
				if _, err := client.Command(context.Background(), protocol.Command{Version: 1, ID: "manual-stop", Kind: "thread.interrupt", ThreadID: state.Threads[0].ID}); err != nil {
					stop()
					t.Fatal(err)
				}
			}
			stop()
			client, stop = startTestServer(t, home)
			defer stop()
			state, err = client.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (state.Threads[0].State == "running" && !state.Threads[0].NeedsResume) == manuallyStopped {
				t.Fatal("shutdown marker ignored manual stop or lost eligible continuation")
			}
			if !state.Threads[1].NeedsResume {
				t.Fatal("pending approval automatically resumed")
			}
			for _, r := range state.Threads[1].Requests {
				if r.State != "pending" || len(r.Answers) > 0 {
					t.Fatal("approval changed")
				}
			}
		})
	}
}

func TestProjectReregistrationRejectsOldConfirmations(t *testing.T) {
	e := testEngine(t)
	add := protocol.Command{Version: 1, ID: "register-original", Kind: "project.add", Path: t.TempDir()}
	original, err := e.command(add)
	if err != nil {
		t.Fatal(err)
	}
	create := protocol.Command{Version: 1, ID: "original-thread", Kind: "thread.create", ProjectID: original.TargetID}
	if _, err := e.command(create); err != nil {
		t.Fatal(err)
	}
	remove := protocol.Command{Version: 1, ID: "remove-original", Kind: "project.remove", ProjectID: original.TargetID, Revision: 2}
	if _, err := e.command(remove); err != nil {
		t.Fatal(err)
	}
	add.ID = "register-again"
	replacement, err := e.command(add)
	if err != nil || replacement.TargetID == original.TargetID {
		t.Fatal("registration reused removed identity", replacement, err)
	}
	create.ID, create.ProjectID = "replacement-thread", replacement.TargetID
	if _, err := e.command(create); err != nil {
		t.Fatal(err)
	}
	before := clone(e.snap)
	// Both registrations reached revision 2. An unsubmitted confirmation
	// from another client still belongs exclusively to the removed one.
	remove.ID = "old-confirmation"
	update := protocol.Command{Version: 1, ID: "old-edit", Kind: "project.update", ProjectID: original.TargetID, Revision: 2, ProjectSettings: &protocol.ProjectSettings{Name: "stale name"}}
	for _, c := range []protocol.Command{remove, update} {
		if _, err := e.command(c); err == nil || !reflect.DeepEqual(before, e.snap) {
			t.Fatal("stale command changed replacement registration", c.Kind, err)
		}
	}
	add.ID = "register-original"
	if r, err := e.command(add); err != nil || r.State != "deleted" {
		t.Fatal("original registration retry was not tombstoned", r, err)
	}
}

func TestRestartContinuationBetweenQueuedTurns(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "manual stop"}[stopped], func(t *testing.T) {
			e := testEngine(t)
			th := &e.snap.Threads[0]
			th.Requests = nil
			th.Queue = []protocol.Prompt{{ID: "queued-next", Text: "captured text", Settings: th.Effective}}
			for i := 0; i < fixtureTurnTicks && !fixtureTurnCompleted(&e.snap.Threads[0]); i++ {
				if err := e.tick(); err != nil {
					t.Fatal(err)
				}
			}
			if !fixtureTurnCompleted(&e.snap.Threads[0]) || e.snap.Threads[0].State != "running" {
				t.Fatal("fixture did not reach queued dispatch boundary")
			}
			if stopped {
				if _, err := e.command(protocol.Command{Version: 1, ID: "stop-boundary", Kind: "thread.interrupt", ThreadID: e.snap.Threads[0].ID}); err != nil {
					t.Fatal(err)
				}
			}
			e.snap.AppSettings.ContinueAfterRestart = true
			// Exercise graceful shutdown's marker and the same recovery used
			// after a crash, without requiring an HTTP listener.
			th = &e.snap.Threads[0]
			th.RestartEligible = restartEligible(th)
			th.State, th.NeedsResume = "interrupted", true
			recoverThreads(&e.snap)
			if err := e.tick(); err != nil {
				t.Fatal(err)
			}
			th = &e.snap.Threads[0]
			if stopped {
				if !th.NeedsResume || len(th.Queue) != 1 || th.TurnID == "queued-next" {
					t.Fatal("manual stop dispatched queued work", th)
				}
				return
			}
			if th.NeedsResume || len(th.Queue) != 0 || th.TurnID != "queued-next" {
				t.Fatal("opted-in continuation stranded queued work", th)
			}
			if got := th.Activity[len(th.Activity)-1].Prompt; got == nil || got.Text != "captured text" || got.Settings != th.Effective {
				t.Fatal("queued capture changed on continuation", got)
			}
		})
	}
}
