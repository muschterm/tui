package storage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func deletionStore(t *testing.T) (*Store, protocol.Snapshot) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	snap := protocol.Snapshot{Version: 1, Threads: []protocol.Thread{{ID: "gone", Title: "private-history"}, {ID: "live", Title: "keep"}}, Terminals: []protocol.Terminal{{ID: "owned", ThreadID: "gone", Output: "private-output"}, {ID: "other", ThreadID: "live"}}}
	if err = s.Save(snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	return s, snap
}

func TestDeletePurgesPayloadsAndProjectsStaleViews(t *testing.T) {
	s, snap := deletionStore(t)
	old := protocol.Command{Version: 1, ID: "old-send", Kind: "prompt.send", ThreadID: "gone", Text: "private-prompt"}
	other := protocol.Command{Version: 1, ID: "other-send", Kind: "prompt.send", ThreadID: "live", Text: "keep-command"}
	for _, cmd := range []protocol.Command{old, other} {
		if err := s.Save(snap, &cmd, &protocol.Receipt{ID: cmd.ID, State: "accepted", TargetID: cmd.ID}); err != nil {
			t.Fatal(err)
		}
	}
	create := protocol.Command{Version: 1, ID: "create-original", Kind: "thread.create", Text: "private-title"}
	if err := s.Save(snap, &create, &protocol.Receipt{ID: create.ID, State: "accepted", TargetID: "gone"}); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"Active":"gone","Threads":{"gone":{"Draft":"private-draft"},"live":{"Draft":"keep-draft"}},"Pending":{"ThreadID":"gone","Text":"private-pending"},"PendingAction":{"Kind":"send","Value":"private-action"},"Edit":{"ThreadID":"gone","OldDraft":"private-edit"},"Unknown":{"retain":true}}`)
	v, err := s.PutView("desk", payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	pendingCreation := json.RawMessage(`{"Pending":{"ID":"create-original","Kind":"thread.create","Text":"private-title"},"PendingAction":{"Kind":"thread-create","Value":"private-action"},"Unknown":"keep"}`)
	if _, err := s.PutView("creating", pendingCreation, 0); err != nil {
		t.Fatal(err)
	}
	untouched := json.RawMessage(`{ "Active":"live", "Threads":{"live":{"Draft":"keep-only"}} }`)
	if _, err = s.PutView("other", untouched, 0); err != nil {
		t.Fatal(err)
	}
	snap.Threads = snap.Threads[1:]
	snap.Terminals = snap.Terminals[1:]
	del := protocol.Command{Version: 1, ID: "delete-retry", Kind: "thread.delete", ThreadID: "gone"}
	receipt := protocol.Receipt{ID: del.ID, State: "accepted", Revision: 3}
	if err = s.Save(snap, &del, &receipt); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadView("desk")
	if err != nil || got.Revision != v.Revision+1 {
		t.Fatalf("%+v %v", got, err)
	}
	if bytes.Contains(got.Data, []byte("private")) || bytes.Contains(got.Data, []byte("gone")) || !bytes.Contains(got.Data, []byte("keep-draft")) {
		t.Fatalf("bad purge: %s", got.Data)
	}
	otherView, _ := s.LoadView("other")
	if otherView.Revision != 1 || !bytes.Equal(otherView.Data, untouched) {
		t.Fatal("unrelated view changed")
	}
	if _, err = s.PutView("desk", payload, v.Revision); err == nil {
		t.Fatal("stale view revision accepted")
	}
	// Even a new client or reconciled writer cannot restore the deleted keys.
	for _, id := range []string{"new-client", "desk"} {
		expected := int64(0)
		if id == "desk" {
			expected = got.Revision
		}
		saved, err := s.PutView(id, payload, expected)
		if err != nil || bytes.Contains(saved.Data, []byte("private")) || bytes.Contains(saved.Data, []byte("gone")) {
			t.Fatalf("stale data restored: %s %v", saved.Data, err)
		}
	}
	creationView, err := s.LoadView("creating")
	if err != nil || bytes.Contains(creationView.Data, []byte("private")) {
		t.Fatalf("creation draft retained: %s %v", creationView.Data, err)
	}
	creationView, err = s.PutView("creating", pendingCreation, creationView.Revision)
	if err != nil || bytes.Contains(creationView.Data, []byte("private")) {
		t.Fatalf("creation draft restored: %s %v", creationView.Data, err)
	}
	if result, err := s.Lookup(create); err != nil || result == nil || result.State != "deleted" || result.TargetID != "" {
		t.Fatal("thread creation retry identity lost", err)
	}
	if result, err := s.Lookup(old); err != nil || result != nil {
		t.Fatal("old command payload retained", err)
	}
	if result, err := s.Lookup(other); err != nil || result == nil {
		t.Fatal("unrelated command removed", err)
	}
	if result, err := s.Lookup(del); err != nil || result == nil || *result != receipt {
		t.Fatalf("delete retry failed: %+v %v", result, err)
	}
	conflict := del
	conflict.ThreadID = "live"
	if _, err = s.Lookup(conflict); err == nil {
		t.Fatal("delete fingerprint accepted changed payload")
	}
	rows, err := s.db.Query("SELECT command,receipt FROM commands")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cmd, receipt []byte
		if err = rows.Scan(&cmd, &receipt); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(cmd, []byte("gone")) || bytes.Contains(cmd, []byte("private")) || bytes.Contains(receipt, []byte("gone")) {
			t.Fatal("command records retain deleted data")
		}
		if strings.HasPrefix(string(cmd), "sha256:") && len(cmd) != len("sha256:")+64 {
			t.Fatal("bad fingerprint")
		}
	}
}

func TestDeleteFailureRollsBackEveryStore(t *testing.T) {
	s, snap := deletionStore(t)
	cmd := protocol.Command{Version: 1, ID: "send", Kind: "prompt.send", ThreadID: "gone", Text: "preserve-until-delete-accepted"}
	if err := s.Save(snap, &cmd, &protocol.Receipt{ID: cmd.ID}); err != nil {
		t.Fatal(err)
	}
	// A corrupt known view field cannot safely be projected; fail the whole delete.
	if _, err := s.db.Exec("INSERT INTO views(id,data,revision) VALUES('corrupt',?,1)", []byte(`{"Threads":[]}`)); err != nil {
		t.Fatal(err)
	}
	del := protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "gone"}
	next := snap
	next.Threads = next.Threads[1:]
	next.Terminals = next.Terminals[1:]
	if err := s.Save(next, &del, &protocol.Receipt{ID: del.ID}); err == nil {
		t.Fatal("malformed view deletion accepted")
	}
	stored, _, err := s.Load()
	if err != nil || len(stored.Threads) != 2 || len(stored.Terminals) != 2 {
		t.Fatal("snapshot partially deleted", err)
	}
	if got, err := s.Lookup(cmd); err != nil || got == nil {
		t.Fatal("command partially purged", err)
	}
	if got, err := s.Lookup(del); err != nil || got != nil {
		t.Fatal("failed deletion receipt persisted", err)
	}
}

func TestDeleteAndViewWriteSerialize(t *testing.T) {
	s, snap := deletionStore(t)
	payload := json.RawMessage(`{"Active":"gone","Threads":{"gone":{"Draft":"private-racing-draft"},"live":{"Draft":"keep"}}}`)
	initial, err := s.PutView("racing", payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	next := snap
	next.Threads = next.Threads[1:]
	next.Terminals = next.Terminals[1:]
	deletion := protocol.Command{Version: 1, ID: "racing-delete", Kind: "thread.delete", ThreadID: "gone"}
	start := make(chan struct{})
	deleted := make(chan error, 1)
	saved := make(chan error, 1)
	go func() {
		<-start
		deleted <- s.Save(next, &deletion, &protocol.Receipt{ID: deletion.ID, State: "accepted"})
	}()
	go func() { <-start; _, err := s.PutView("racing", payload, initial.Revision); saved <- err }()
	close(start)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		if pe, ok := err.(*protocol.Error); !ok || pe.Code != "stale_view" {
			t.Fatal(err)
		}
	}
	final, err := s.LoadView("racing")
	if err != nil || bytes.Contains(final.Data, []byte("private")) || bytes.Contains(final.Data, []byte("gone")) || !bytes.Contains(final.Data, []byte("keep")) {
		t.Fatalf("race resurrected data: %s %v", final.Data, err)
	}
}
