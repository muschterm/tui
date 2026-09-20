package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

type viewTransport struct {
	t           *testing.T
	view        protocol.View
	live        map[string]bool
	puts        []protocol.View
	paths       []string
	loseNextAck bool
}

func (f *viewTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	var payload any
	status := http.StatusOK
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/snapshot":
		s := protocol.Snapshot{Version: 1, InstanceID: "test"}
		for id := range f.live {
			s.Threads = append(s.Threads, protocol.Thread{ID: id})
		}
		payload = s
	case r.Method == "GET" && r.URL.Path == "/v1/views/desk":
		payload = f.view
	case r.Method == "PUT" && r.URL.Path == "/v1/views/desk":
		var incoming protocol.View
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			f.t.Fatal(err)
		}
		f.puts = append(f.puts, incoming)
		if incoming.Revision != f.view.Revision {
			status = http.StatusConflict
			payload = protocol.Error{Code: "stale_view", Message: "test view changed"}
		} else {
			projected, err := protocol.PruneThreadView(incoming.Data, f.live)
			if err != nil {
				f.t.Fatal(err)
			}
			f.view = protocol.View{Revision: f.view.Revision + 1, Data: projected}
			if f.loseNextAck {
				f.loseNextAck = false
				return nil, errors.New("simulated lost acknowledgment")
			}
			payload = f.view
		}
	default:
		f.t.Fatalf("unexpected request (commands must never be probed): %s %s", r.Method, r.URL.Path)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
}

func writerPayload(t *testing.T, generation int64, active string, drafts map[string]string) []byte {
	t.Helper()
	threads := map[string]any{}
	for id, draft := range drafts {
		threads[id] = map[string]string{"Draft": draft}
	}
	data, err := json.Marshal(map[string]any{"Generation": generation, "Active": active, "Threads": threads})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testViewWriter(t *testing.T, initial []byte, live ...string) (*viewWriter, *viewTransport) {
	t.Helper()
	f := &viewTransport{t: t, view: protocol.View{Revision: 1, Data: initial}, live: map[string]bool{}}
	for _, id := range live {
		f.live[id] = true
	}
	c := client.New(protocol.Discovery{URL: "http://127.0.0.1", InstanceID: "test"})
	c.HTTP = &http.Client{Transport: f}
	w := &viewWriter{id: "desk", revision: 1, generation: 1, acknowledged: bytes.Clone(initial)}
	w.connection.Store(c)
	return w, f
}

func (f *viewTransport) deleteThread(id string) {
	delete(f.live, id)
	projected, err := protocol.PruneThreadView(f.view.Data, f.live)
	if err != nil {
		f.t.Fatal(err)
	}
	f.view = protocol.View{Data: projected, Revision: f.view.Revision + 1}
}

func TestViewWriterRebasesOnlyDeletionAndKeepsCurrentDraft(t *testing.T) {
	initial := writerPayload(t, 1, "gone", map[string]string{"gone": "delete", "live": "old"})
	w, f := testViewWriter(t, initial, "gone", "live")
	f.deleteThread("gone")
	outgoing := writerPayload(t, 2, "gone", map[string]string{"gone": "stale deleted draft", "live": "new local draft"})
	if err := w.save(outgoing); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 2 || f.puts[0].Revision != 1 || f.puts[1].Revision != 2 {
		t.Fatalf("expected one guarded retry: %+v", f.puts)
	}
	if bytes.Contains(f.view.Data, []byte("gone")) || !bytes.Contains(f.view.Data, []byte("new local draft")) {
		t.Fatalf("wrong rebase: %s", f.view.Data)
	}
	if w.revision != 3 || w.generation != 2 || !bytes.Equal(w.acknowledged, f.view.Data) {
		t.Fatal("acknowledgment not advanced")
	}
}

func TestViewWriterRejectsGenuineConcurrentEdit(t *testing.T) {
	initial := writerPayload(t, 1, "gone", map[string]string{"gone": "delete", "live": "old"})
	w, f := testViewWriter(t, initial, "gone", "live")
	f.deleteThread("gone")
	f.view.Data = writerPayload(t, 2, "live", map[string]string{"live": "other client's edit"})
	f.view.Revision++
	remote := bytes.Clone(f.view.Data)
	outgoing := writerPayload(t, 3, "gone", map[string]string{"gone": "delete", "live": "my unsaved edit"})
	if err := w.save(outgoing); err == nil {
		t.Fatal("concurrent edit silently overwritten")
	}
	if len(f.puts) != 1 || !bytes.Equal(remote, f.view.Data) || w.generation != 1 || !bytes.Equal(w.acknowledged, initial) {
		t.Fatal("conflict changed remote or acknowledged state")
	}
	if !bytes.Contains(outgoing, []byte("my unsaved edit")) {
		t.Fatal("local recoverable payload lost")
	}
}

func TestViewWriterReconcilesLostAckOfPrunedOutgoing(t *testing.T) {
	initial := writerPayload(t, 1, "live", map[string]string{"live": "old"})
	w, f := testViewWriter(t, initial, "live")
	f.loseNextAck = true
	outgoing := writerPayload(t, 2, "gone", map[string]string{"gone": "stale", "live": "new"})
	if err := w.save(outgoing); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || w.revision != 2 || w.generation != 2 || bytes.Contains(w.acknowledged, []byte("gone")) {
		t.Fatal("lost ack caused retry or failed projection")
	}
}

func TestViewWriterRepeatedDeletionsAndOlderGeneration(t *testing.T) {
	initial := writerPayload(t, 1, "a", map[string]string{"a": "A", "b": "B", "c": "C"})
	w, f := testViewWriter(t, initial, "a", "b", "c")
	f.deleteThread("a")
	second := writerPayload(t, 2, "a", map[string]string{"a": "A", "b": "B2", "c": "C2"})
	if err := w.save(second); err != nil {
		t.Fatal(err)
	}
	f.deleteThread("b")
	third := writerPayload(t, 3, "b", map[string]string{"b": "B2", "c": "C3"})
	if err := w.save(third); err != nil {
		t.Fatal(err)
	}
	count := len(f.puts)
	if err := w.save(second); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != count || w.generation != 3 || !bytes.Contains(f.view.Data, []byte("C3")) {
		t.Fatal("older save displaced newer generation")
	}
}

func TestViewWriterUnknownCreationTombstoneStaysRecoverable(t *testing.T) {
	initial := []byte(`{"Generation":1,"Threads":{"live":{"Draft":"keep"}},"Pending":{"ID":"create","Kind":"thread.create","Text":"unsure"},"PendingAction":{"Kind":"thread-create"}}`)
	w, f := testViewWriter(t, initial, "live")
	pruned, err := protocol.PruneThreadViewCommands(initial, f.live, map[string]bool{"create": true})
	if err != nil {
		t.Fatal(err)
	}
	f.view = protocol.View{Revision: 2, Data: pruned}
	outgoing := bytes.Replace(initial, []byte(`"Generation":1`), []byte(`"Generation":2`), 1)
	if err = w.save(outgoing); err == nil {
		t.Fatal("unknown command status guessed")
	}
	if len(f.puts) != 1 || !strings.Contains(string(outgoing), "unsure") {
		t.Fatal("recovery payload lost")
	}
	for _, path := range f.paths {
		if strings.Contains(path, "command") {
			t.Fatal("pending command replayed")
		}
	}
}
