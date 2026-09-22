package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestMalformedStoredViewDoesNotBlockThreadDelete(t *testing.T) {
	s, snap := deletionStore(t)
	// Accepted by earlier servers: DraftThreads was only checked for a dead StartedDraft.
	malformed := []byte(`{"StartedDraft":{"ThreadID":"live","Command":{}},"DraftThreads":5,"Threads":{"gone":{"Draft":"kept for its client"}}}`)
	if _, err := s.db.Exec("INSERT INTO views(id,data,revision) VALUES('malformed',?,1)", malformed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutView("valid", json.RawMessage(`{"Active":"gone","Threads":{"gone":{"Draft":"private"},"live":{"Draft":"keep"}}}`), 0); err != nil {
		t.Fatal(err)
	}
	next := snap
	next.Threads = next.Threads[1:]
	next.Terminals = next.Terminals[1:]
	del := protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "gone"}
	if err := s.Save(next, &del, &protocol.Receipt{ID: del.ID, State: "accepted"}); err != nil {
		t.Fatal("malformed view blocked thread deletion:", err)
	}
	if v, err := s.LoadView("malformed"); err != nil || !bytes.Equal(v.Data, malformed) || v.Revision != 1 {
		t.Fatal("malformed view changed", string(v.Data), v.Revision, err)
	}
	if v, err := s.LoadView("valid"); err != nil || string(v.Data) != `{"Active":"","Threads":{"live":{"Draft":"keep"}}}` || v.Revision != 2 {
		t.Fatal("valid view not pruned", string(v.Data), v.Revision, err)
	}
	if r, err := s.Lookup(del); err != nil || r == nil {
		t.Fatal("deletion receipt missing", err)
	}
}

func TestPutViewRejectsMalformedKnownFields(t *testing.T) {
	s, _ := deletionStore(t)
	for _, payload := range []string{
		`{"StartedDraft":{"ThreadID":"live","Command":{}},"DraftThreads":5}`,
		`{"DraftThreads":[]}`,
		`{"DraftThreads":{"project":7}}`,
		`{"DraftThreads":{"project":{"Draft":7}}}`,
		`{"DraftProjectID":7}`,
		`{"StartedDraft":"x"}`,
	} {
		_, err := s.PutView("desk", json.RawMessage(payload), 0)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != "invalid_view" {
			t.Fatalf("%s accepted: %v", payload, err)
		}
	}
	if v, err := s.LoadView("desk"); err != nil || v.Revision != 0 {
		t.Fatal("rejected view was stored", err)
	}
	valid := json.RawMessage(`{"DraftProjectID":"p","DraftThreads":{"p":{"Draft":"text","Attachments":null},"q":null},"StartedDraft":null,"Threads":{"live":{"Draft":"d"}},"Active":"live","Edit":null,"Pending":null,"PendingAction":{"Kind":"","ID":"","Value":"","Index":0,"Revision":0}}`)
	if v, err := s.PutView("desk", valid, 0); err != nil || !bytes.Equal(v.Data, valid) {
		t.Fatal("client-shaped view rejected or rewritten:", err)
	}
}
