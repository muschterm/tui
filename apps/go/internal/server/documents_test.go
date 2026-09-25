//go:build unix

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
	"github.com/reearth/ygo/crdt"
)

// --- fake clock ---

type fakeTimer struct {
	at      time.Time
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_800_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) At(at time.Time) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: at, ch: make(chan time.Time, 1), stopped: make(chan struct{})}
	stop := func() { t.once.Do(func() { close(t.stopped) }) }
	if !at.After(c.now) {
		t.ch <- c.now
		return t.ch, stop
	}
	c.timers = append(c.timers, t)
	return t.ch, stop
}

// Advance moves time and delivers every due timer the actor still waits on,
// returning after each was received (or abandoned).
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due, kept []*fakeTimer
	for _, t := range c.timers {
		if !t.at.After(now) {
			due = append(due, t)
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()
	for _, t := range due {
		select {
		case t.ch <- now:
		case <-t.stopped:
		}
	}
}

// --- failing store ---

type flakyStore struct {
	*storage.Store
	fail atomic.Bool
}

func (s *flakyStore) CommitDocument(id string, c storage.DocumentCommit) error {
	if s.fail.Load() {
		return errors.New("injected storage failure")
	}
	return s.Store.CommitDocument(id, c)
}

// --- harness ---

type docHarness struct {
	t      *testing.T
	e      *engine
	root   string
	clock  *fakeClock
	store  *flakyStore
	client *client.Client
	dbPath string
	sent   map[string]int64
}

func newDocHarness(t *testing.T) *docHarness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	return startDocHarness(t, root, dbPath)
}

func startDocHarness(t *testing.T, root, dbPath string) *docHarness {
	t.Helper()
	st, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	snap, ok, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		snap = fixture.Initial()
		snap.Projects = append(snap.Projects, protocol.Project{ID: "project-docs", Name: "docs", Path: root, Revision: 1})
		if err := st.Save(snap, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	h := &docHarness{t: t, root: root, clock: newFakeClock(), store: &flakyStore{Store: st}, dbPath: dbPath, sent: map[string]int64{}}
	h.e = &engine{snap: snap, store: st, subscribers: map[chan protocol.Snapshot]bool{}}
	h.e.docs.store, h.e.docs.clock = h.store, h.clock
	if err := h.e.startDocuments(); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}/stream", h.e.documentStream)
	mux.HandleFunc("GET /v1/documents/{id}/versions", h.e.documentVersionsHandler)
	srv := httptest.NewServer(mux)
	h.client = client.New(protocol.Discovery{Version: protocol.Version, URL: srv.URL, Token: "test"})
	t.Cleanup(func() {
		srv.Close()
		h.e.stopDocuments()
		st.Close()
	})
	return h
}

func (h *docHarness) write(rel, content string, mode os.FileMode) {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *docHarness) read(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *docHarness) command(c protocol.Command) (protocol.Receipt, error) {
	c.Version = 1
	if c.ID == "" {
		c.ID = client.ID()
	}
	return h.e.command(c)
}

func (h *docHarness) open(rel, clientID string) string {
	h.t.Helper()
	r, err := h.command(protocol.Command{Kind: protocol.DocumentKindOpen, ProjectID: "project-docs", Path: rel, ClientID: clientID})
	if err != nil {
		h.t.Fatal(err)
	}
	return r.TargetID
}

func (h *docHarness) mustCommand(kind, id, clientID string) {
	h.t.Helper()
	if _, err := h.command(protocol.Command{Kind: kind, TargetID: id, ClientID: clientID}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *docHarness) status(id string) (protocol.DocumentStatus, bool) {
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	for _, d := range h.e.snap.Documents {
		if d.ID == id {
			return d, true
		}
	}
	return protocol.DocumentStatus{}, false
}

func (h *docHarness) waitStatus(id, what string, cond func(protocol.DocumentStatus) bool) protocol.DocumentStatus {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := h.status(id)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("document %s: %s not reached: %+v", id, what, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *docHarness) waitState(id, state string) protocol.DocumentStatus {
	h.t.Helper()
	return h.waitStatus(id, state, func(s protocol.DocumentStatus) bool { return s.State == state })
}

// onActor runs fn on the document's actor goroutine.
func (h *docHarness) onActor(id string, fn func(a *docActor)) {
	h.t.Helper()
	a := h.e.docActor(id)
	if a == nil {
		return
	}
	done := make(chan struct{})
	if a.post(docRunMsg{fn: func() { fn(a) }, done: done}) {
		select {
		case <-done:
		case <-a.quit:
		}
	}
}

// barrier returns after the actor processed everything queued before it
// (including a timer already received).
func (h *docHarness) barrier(id string) {
	h.t.Helper()
	h.onActor(id, func(*docActor) {})
}

// received waits until the actor has handled every update the test sent.
func (h *docHarness) received(id string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int64
		h.onActor(id, func(a *docActor) { n = a.updates })
		if n >= h.sent[id] {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("actor handled %d of %d updates", n, h.sent[id])
		}
		time.Sleep(time.Millisecond)
	}
}

type editor struct {
	t       *testing.T
	h       *docHarness
	id      string
	stream  *client.DocumentStream
	replica *crdt.Doc
	text    *crdt.YText
	origin  *struct{ _ byte }
	gen     int64
	seq     int
}

// connect opens a stream and applies its state to a fresh replica.
func (h *docHarness) connect(id, clientID string, replica uint64) *editor {
	h.t.Helper()
	s, err := h.client.OpenDocumentStream(context.Background(), id, clientID, replica)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { s.Close() })
	id64 := replica
	if id64 == 0 {
		id64 = 999_999
	}
	d := crdt.New(crdt.WithClientID(crdt.ClientID(id64)))
	ed := &editor{t: h.t, h: h, id: id, stream: s, replica: d, text: d.GetText(protocol.DocumentTextName), origin: &struct{ _ byte }{}}
	ev := ed.next("state", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventState })
	if err := crdt.ApplyUpdateV1(d, ev.Update, nil); err != nil {
		h.t.Fatal(err)
	}
	if ev.Status != nil {
		ed.gen = ev.Status.EditGen
	}
	return ed
}

// next returns the first event matching, applying remote updates to the
// replica on the way.
func (ed *editor) next(what string, match func(protocol.DocumentEvent) bool) protocol.DocumentEvent {
	ed.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ed.stream.Events():
			if !ok {
				ed.t.Fatalf("stream ended waiting for %s: %v", what, ed.stream.Err())
			}
			if ev.Type == protocol.DocumentEventUpdate {
				if err := crdt.ApplyUpdateV1(ed.replica, ev.Update, nil); err != nil {
					ed.t.Fatal(err)
				}
			}
			if ev.Status != nil {
				ed.gen = ev.Status.EditGen
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			ed.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// ended waits for the stream to end normally, failing on any other event
// type than those allowed.
func (ed *editor) ended(what string) {
	ed.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-ed.stream.Events():
			if !ok {
				if err := ed.stream.Err(); err != nil {
					ed.t.Fatalf("%s: stream failed: %v", what, err)
				}
				return
			}
		case <-timeout:
			ed.t.Fatalf("%s: stream did not end", what)
		}
	}
}

// quiet asserts that no event matching arrives for d of real time.
func (ed *editor) quiet(d time.Duration, what string, match func(protocol.DocumentEvent) bool) {
	ed.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ed.stream.Events():
			if !ok {
				return
			}
			if ev.Type == protocol.DocumentEventUpdate {
				_ = crdt.ApplyUpdateV1(ed.replica, ev.Update, nil)
			}
			if match(ev) {
				ed.t.Fatalf("unexpected %s: %+v", what, ev)
			}
		case <-timeout:
			return
		}
	}
}

func (ed *editor) edit(fn func(*crdt.Transaction)) []byte {
	var out []byte
	unsub := ed.replica.OnUpdate(func(u []byte, o any) {
		if o == ed.origin {
			out = append([]byte(nil), u...)
		}
	})
	ed.replica.Transact(fn, ed.origin)
	unsub()
	return out
}

func (ed *editor) insert(at int, s string) (string, []byte) {
	u := ed.edit(func(txn *crdt.Transaction) { ed.text.Insert(txn, at, s, nil) })
	return ed.sendUpdate(u), u
}

func (ed *editor) delete(at, n int) string {
	u := ed.edit(func(txn *crdt.Transaction) { ed.text.Delete(txn, at, n) })
	return ed.sendUpdate(u)
}

func (ed *editor) sendUpdate(u []byte) string {
	ed.t.Helper()
	ed.seq++
	op := fmt.Sprintf("op-%d", ed.seq)
	if err := ed.stream.SendUpdate(op, ed.gen, u); err != nil {
		ed.t.Fatal(err)
	}
	ed.h.sent[ed.id]++
	return op
}

func ackFor(op string) func(protocol.DocumentEvent) bool {
	return func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventAck && ev.Op == op }
}

func rejectedFor(op string) func(protocol.DocumentEvent) bool {
	return func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventRejected && ev.Op == op }
}

// commit waits for every sent update to be handled, then advances the
// clock past the batch interval.
func (h *docHarness) commit(id string) {
	h.received(id)
	h.clock.Advance(docBatchInterval)
	h.barrier(id)
}

// resolve resolves with the currently retained disk version as reviewed.
func (h *docHarness) resolve(id, resolution string, rev int64) (protocol.Receipt, error) {
	h.t.Helper()
	disk := ""
	if v, err := h.client.DocumentVersions(context.Background(), id); err == nil {
		disk = v.DiskID
	}
	return h.command(protocol.Command{Kind: protocol.DocumentKindResolve, TargetID: id, ClientID: "alice", Text: resolution, Revision: rev, DocumentDisk: disk})
}

// --- tests ---

func TestDocumentTextNameMatchesLibrary(t *testing.T) {
	if doc.TextName != protocol.DocumentTextName {
		t.Fatal("protocol and doc text names differ")
	}
}

func TestDocumentOpenEditSaveCloseAndEditorGate(t *testing.T) {
	h := newDocHarness(t)
	h.write("notes/a.txt", "hello\n", 0o640)
	before, _ := os.Stat(filepath.Join(h.root, "notes/a.txt"))
	id := h.open("notes/a.txt", "alice")
	if again := h.open("notes/a.txt", "bob"); again != id {
		t.Fatalf("second open created another document: %s vs %s", again, id)
	}
	st := h.waitState(id, protocol.DocumentStateSaved)
	if st.DurableRev != 0 || st.SavedRev != 0 || len(st.Openers) != 2 || st.Newline != "lf" {
		t.Fatalf("status after open: %+v", st)
	}
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindEdit, TargetID: id, ClientID: "bob"}); err == nil || !strings.Contains(err.Error(), "editor_busy") {
		t.Fatalf("second editor: %v", err)
	}
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindEdit, TargetID: id, ClientID: "mallory"}); err == nil || !strings.Contains(err.Error(), "not_open") {
		t.Fatalf("non-opener edit: %v", err)
	}
	alice := h.connect(id, "alice", 101)
	bob := h.connect(id, "bob", 202)
	alice.gen = h.waitStatus(id, "editor", func(s protocol.DocumentStatus) bool { return s.Editor == "alice" }).EditGen

	op, _ := alice.insert(5, ", world")
	// Not acknowledged before the batch commits.
	alice.quiet(150*time.Millisecond, "early ack", ackFor(op))
	h.commit(id)
	if ev := alice.next("ack", ackFor(op)); ev.Rev != 1 {
		t.Fatalf("ack rev %d", ev.Rev)
	}
	up := bob.next("update", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventUpdate })
	if up.Rev != 1 || up.Origin != "client" || up.Client != "alice" || bob.text.ToString() != "hello, world\n" {
		t.Fatalf("observer update %+v text %q", up, bob.text.ToString())
	}
	if st := h.waitState(id, protocol.DocumentStatePending); st.DurableRev != 1 || st.SavedRev != 0 {
		t.Fatalf("pending status %+v", st)
	}
	if h.read("notes/a.txt") != "hello\n" {
		t.Fatal("saved before the idle delay")
	}
	h.clock.Advance(docSaveIdle)
	st = h.waitState(id, protocol.DocumentStateSaved)
	if st.SavedRev != 1 || h.read("notes/a.txt") != "hello, world\n" {
		t.Fatalf("saved %+v %q", st, h.read("notes/a.txt"))
	}
	after, _ := os.Stat(filepath.Join(h.root, "notes/a.txt"))
	if after.Mode().Perm() != 0o640 || os.SameFile(before, after) {
		t.Fatalf("mode %v same inode %v: the write must replace the file atomically with its mode", after.Mode().Perm(), os.SameFile(before, after))
	}
	if matches, _ := filepath.Glob(filepath.Join(h.root, "notes", ".*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary files left: %v", matches)
	}

	// Bob is an observer: his updates are refused and his replica resynced.
	bob.gen = st.EditGen
	bop, _ := bob.insert(0, "x")
	rej := bob.next("rejected", rejectedFor(bop))
	if rej.Rejected.Reason != protocol.DocumentRejectNotEditor || rej.Rejected.Message != protocol.DocumentSimultaneousUnavailable || !rej.Rejected.Resync {
		t.Fatalf("observer rejection %+v", rej.Rejected)
	}
	// The stream ends; the client reconnects with a new replica.
	bob.ended("resync")
	bob = h.connect(id, "bob", 203)

	// Take-edit transfers the role; Alice's stale generation is refused.
	h.mustCommand(protocol.DocumentKindTakeEdit, id, "bob")
	st = h.waitStatus(id, "bob editor", func(s protocol.DocumentStatus) bool { return s.Editor == "bob" })
	if st.EditGen != 2 {
		t.Fatalf("edit gen %d", st.EditGen)
	}
	alice.gen = 1
	aop, _ := alice.insert(0, "y")
	if rej := alice.next("rejected", rejectedFor(aop)); rej.Rejected.Reason != protocol.DocumentRejectNotEditor {
		t.Fatalf("former editor: %+v", rej.Rejected)
	}

	// Closing by every opener unloads the saved document.
	h.mustCommand(protocol.DocumentKindClose, id, "alice")
	h.mustCommand(protocol.DocumentKindClose, id, "bob")
	h.waitStatus(id, "unloaded", func(s protocol.DocumentStatus) bool { return s.ID == "" })
	bob.next("closed", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventClosed })
	if records, _ := h.store.LoadDocuments(); len(records) != 0 {
		t.Fatalf("unloaded document left in storage: %d", len(records))
	}
	if _, err := h.client.OpenDocumentStream(context.Background(), id, "alice", 0); !errors.Is(err, client.ErrDocumentNotFound) {
		t.Fatalf("stream to unloaded document: %v", err)
	}
}

func TestDocumentAckOnlyAfterDurableCommit(t *testing.T) {
	h := newDocHarness(t)
	h.write("a.txt", "abc\n", 0o644)
	id := h.open("a.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 7)
	h.store.fail.Store(true)
	op, update := ed.insert(3, "d")
	h.commit(id)
	ed.quiet(200*time.Millisecond, "ack while storage fails", ackFor(op))
	st := h.waitState(id, protocol.DocumentStateFailed)
	if st.DurableRev != 0 || !strings.Contains(st.Error, "storage") {
		t.Fatalf("failed status %+v", st)
	}
	// The client keeps its op and may resend it; the duplicate is harmless.
	if err := ed.stream.SendUpdate(op, ed.gen, update); err != nil {
		t.Fatal(err)
	}
	h.sent[id]++
	h.received(id)
	h.store.fail.Store(false)
	h.clock.Advance(docRetryMax)
	ev := ed.next("ack", ackFor(op))
	if ev.Rev != 1 {
		t.Fatalf("ack rev %d", ev.Rev)
	}
	ed.next("ack of resend", ackFor(op))
	h.waitStatus(id, "durable", func(s protocol.DocumentStatus) bool {
		return s.DurableRev == 1 && s.State != protocol.DocumentStateFailed
	})
	h.clock.Advance(docSaveMax)
	h.waitState(id, protocol.DocumentStateSaved)
	if h.read("a.txt") != "abcd\n" {
		t.Fatalf("file %q", h.read("a.txt"))
	}
}

func TestDocumentValidationRejects(t *testing.T) {
	h := newDocHarness(t)
	h.write("u.txt", "a😀b\n", 0o644)
	id := h.open("u.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	replica := uint64(9)
	ed := h.connect(id, "alice", replica)
	h.waitStatus(id, "editor", func(s protocol.DocumentStatus) bool { return s.EditGen == 1 })
	ed.gen = 1
	cases := []struct {
		name    string
		reasons []string
		send    func() string
	}{
		{"surrogate split", []string{doc.ReasonSurrogate}, func() string { op, _ := ed.insert(2, "x"); return op }},
		{"NUL", []string{doc.ReasonRejected}, func() string { op, _ := ed.insert(0, "\x00"); return op }},
		{"over 1 MiB", []string{doc.ReasonRejected}, func() string {
			op, _ := ed.insert(0, strings.Repeat("x", protocol.DocumentMaxBytes))
			return op
		}},
		{"CR before LF", []string{doc.ReasonRejected}, func() string { op, _ := ed.insert(4, "\r"); return op }},
		{"invalid UTF-8", []string{doc.ReasonInvalid, doc.ReasonContent}, func() string {
			u := ed.edit(func(txn *crdt.Transaction) { ed.text.Insert(txn, 0, "é", nil) })
			i := strings.Index(string(u), "é")
			if i < 0 {
				t.Fatal("insert text not found in update")
			}
			u[i+1] = 0x28 // C3 28 is not UTF-8
			return ed.sendUpdate(u)
		}},
	}
	for _, tc := range cases {
		op := tc.send()
		rej := ed.next(tc.name, rejectedFor(op))
		ok := false
		for _, r := range tc.reasons {
			ok = ok || rej.Rejected.Reason == r
		}
		if !ok || !rej.Rejected.Resync {
			t.Fatalf("%s: %+v", tc.name, rej.Rejected)
		}
		// The stream ends; a new stream with a new replica starts from the
		// unchanged document.
		ed.ended(tc.name)
		replica++
		ed = h.connect(id, "alice", replica)
		ed.gen = 1
		if got := ed.text.ToString(); got != "a😀b\n" {
			t.Fatalf("%s changed the document: %q", tc.name, got)
		}
	}
	if st, _ := h.status(id); st.DurableRev != 0 {
		t.Fatalf("rejected updates became durable: %+v", st)
	}
	// Another client's replica ID cannot be claimed.
	h.open("u.txt", "bob")
	if _, err := h.client.OpenDocumentStream(context.Background(), id, "bob", replica); err == nil || !strings.Contains(err.Error(), "replica_conflict") {
		t.Fatalf("replica claim: %v", err)
	}
}

func TestDocumentAutosaveTiming(t *testing.T) {
	h := newDocHarness(t)
	h.write("t.txt", "", 0o600)
	id := h.open("t.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 5)
	ed.gen = 1
	op, _ := ed.insert(0, "a")
	h.commit(id)
	ed.next("ack", ackFor(op))
	// Idle save: 750 ms after the last change, not earlier.
	h.clock.Advance(docSaveIdle - time.Millisecond)
	h.barrier(id)
	if h.read("t.txt") != "" {
		t.Fatal("saved before 750 ms idle")
	}
	h.clock.Advance(time.Millisecond)
	h.waitState(id, protocol.DocumentStateSaved)
	if h.read("t.txt") != "a" {
		t.Fatalf("idle save %q", h.read("t.txt"))
	}
	// Continuous edits every 500 ms never leave 750 ms idle; the save comes
	// 3 s after the first unsaved change.
	for i := 0; i < 6; i++ {
		op, _ := ed.insert(ed.text.Len(), "b")
		h.commit(id)
		ed.next("ack", ackFor(op))
		h.clock.Advance(500*time.Millisecond - docBatchInterval)
		h.barrier(id)
		if h.read("t.txt") != "a" {
			t.Fatalf("saved %v after the first unsaved change", time.Duration(i+1)*500*time.Millisecond)
		}
	}
	op, _ = ed.insert(ed.text.Len(), "b")
	h.commit(id) // 3 s + 50 ms after the first unsaved change
	ed.next("ack", ackFor(op))
	h.waitStatus(id, "max-interval save", func(s protocol.DocumentStatus) bool {
		return s.State == protocol.DocumentStateSaved && s.SavedRev == s.DurableRev
	})
	if h.read("t.txt") != "abbbbbbb" {
		t.Fatalf("file %q", h.read("t.txt"))
	}
}

func TestDocumentOpenRefusesLinksAndUneditableFiles(t *testing.T) {
	h := newDocHarness(t)
	h.write("target.txt", "x\n", 0o644)
	if err := os.Symlink("target.txt", filepath.Join(h.root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	h.write("hard.txt", "x\n", 0o644)
	if err := os.Link(filepath.Join(h.root, "hard.txt"), filepath.Join(h.root, "hard2.txt")); err != nil {
		t.Fatal(err)
	}
	h.write("mixed.txt", "a\r\nb\n", 0o644)
	h.write("bin.dat", "a\x00b", 0o644)
	h.write("big.txt", strings.Repeat("x", protocol.DocumentMaxBytes+1), 0o644)
	h.write("bad.txt", "\xff\xfe", 0o644)
	for _, rel := range []string{"link.txt", "hard.txt", "mixed.txt", "bin.dat", "big.txt", "bad.txt"} {
		_, err := h.command(protocol.Command{Kind: protocol.DocumentKindOpen, ProjectID: "project-docs", Path: rel, ClientID: "alice"})
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != "document_read_only" {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	for _, rel := range []string{"../x", ".git/config", "/etc/passwd"} {
		if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindOpen, ProjectID: "project-docs", Path: rel, ClientID: "alice"}); err == nil {
			t.Fatalf("%s opened", rel)
		}
	}
	// CRLF and BOM are kept, and the document itself is LF.
	h.write("win.txt", "\xef\xbb\xbfone\r\ntwo\r\n", 0o644)
	id := h.open("win.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 3)
	ed.gen = 1
	if ed.text.ToString() != "one\ntwo\n" {
		t.Fatalf("document text %q", ed.text.ToString())
	}
	op, _ := ed.insert(4, "1.5\n")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateSaved)
	if got := h.read("win.txt"); got != "\xef\xbb\xbfone\r\n1.5\r\ntwo\r\n" {
		t.Fatalf("CRLF/BOM file %q", got)
	}
}

func TestDocumentExternalCleanChangeMergesAndSaves(t *testing.T) {
	h := newDocHarness(t)
	h.write("m.txt", "a\nb\nc\nd\ne\n", 0o644)
	id := h.open("m.txt", "alice")
	h.open("m.txt", "bob")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 11)
	obs := h.connect(id, "bob", 0)
	ed.gen = 1
	op, _ := ed.insert(0, "X")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.write("m.txt", "a\nb\nc\nd\nE\n", 0o644)
	h.clock.Advance(docPollInterval)
	merged := "Xa\nb\nc\nd\nE\n"
	obs.next("server update", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventUpdate && ev.Origin == "server"
	})
	ed.next("server update", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventUpdate && ev.Origin == "server"
	})
	if obs.text.ToString() != merged || ed.text.ToString() != merged {
		t.Fatalf("observer %q editor %q", obs.text.ToString(), ed.text.ToString())
	}
	h.barrier(id)
	h.clock.Advance(docSaveMax)
	h.waitStatus(id, "saved merge", func(s protocol.DocumentStatus) bool {
		return s.State == protocol.DocumentStateSaved && s.SavedRev == s.DurableRev
	})
	if h.read("m.txt") != merged {
		t.Fatalf("file %q", h.read("m.txt"))
	}
	// A clean external change to a document with no local edits is taken
	// as is and needs no write.
	h.write("m.txt", "Xa\nb\nC\nd\nE\n", 0o644)
	h.clock.Advance(docPollInterval)
	obs.next("second server update", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventUpdate })
	h.waitStatus(id, "saved", func(s protocol.DocumentStatus) bool {
		return s.State == protocol.DocumentStateSaved && s.DurableRev == 3
	})
}

func TestDocumentConflictPausesWithVersionsAndResolves(t *testing.T) {
	h := newDocHarness(t)
	h.write("c.txt", "one\ntwo\n", 0o644)
	id := h.open("c.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 12)
	ed.gen = 1
	op, _ := ed.insert(0, "ours ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.write("c.txt", "theirs one\ntwo\n", 0o644)
	h.clock.Advance(docPollInterval)
	st := h.waitState(id, protocol.DocumentStatePausedConflict)
	if !st.Versions || st.SavedRev >= st.DurableRev || st.Reason == "" {
		t.Fatalf("paused %+v", st)
	}
	v, err := h.client.DocumentVersions(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Base) != "one\ntwo\n" || string(v.Document) != "ours one\ntwo\n" || string(v.Disk) != "theirs one\ntwo\n" || v.DiskState != "present" {
		t.Fatalf("versions %+v", v)
	}
	// Edits continue durably while paused; nothing is written.
	op, _ = ed.insert(0, "more ")
	h.commit(id)
	ed.next("ack while paused", ackFor(op))
	h.clock.Advance(docSaveMax + docPollInterval)
	h.barrier(id)
	if h.read("c.txt") != "theirs one\ntwo\n" {
		t.Fatal("paused document wrote the file")
	}
	st, _ = h.status(id)
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev-1); err == nil || !strings.Contains(err.Error(), "stale_document") {
		t.Fatalf("stale resolve: %v", err)
	}
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(0)
	st = h.waitState(id, protocol.DocumentStateSaved)
	if h.read("c.txt") != "more ours one\ntwo\n" || st.Versions {
		t.Fatalf("keep_document: %q %+v", h.read("c.txt"), st)
	}

	// use_disk takes the file's text into the document.
	op, _ = ed.insert(0, "x")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.write("c.txt", "disk wins\n", 0o644)
	h.clock.Advance(docPollInterval)
	st = h.waitState(id, protocol.DocumentStatePausedConflict)
	if _, err := h.resolve(id, protocol.DocumentResolveUseDisk, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	ed.next("server update", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventUpdate && ev.Origin == "server"
	})
	h.waitState(id, protocol.DocumentStateSaved)
	if ed.text.ToString() != "disk wins\n" || h.read("c.txt") != "disk wins\n" {
		t.Fatalf("use_disk: %q %q", ed.text.ToString(), h.read("c.txt"))
	}
}

func TestDocumentDeletedOrReplacedPauses(t *testing.T) {
	h := newDocHarness(t)
	h.write("d.txt", "keep me\n", 0o600)
	id := h.open("d.txt", "alice")
	if err := os.Remove(filepath.Join(h.root, "d.txt")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docPollInterval)
	st := h.waitState(id, protocol.DocumentStateDeleted)
	v, err := h.client.DocumentVersions(context.Background(), id)
	if err != nil || v.DiskState != "absent" || string(v.Document) != "keep me\n" {
		t.Fatalf("versions %+v %v", v, err)
	}
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(0)
	h.waitState(id, protocol.DocumentStateSaved)
	info, err := os.Stat(filepath.Join(h.root, "d.txt"))
	if err != nil || info.Mode().Perm() != 0o600 || h.read("d.txt") != "keep me\n" {
		t.Fatalf("recreated %v %v", info, err)
	}
	// Replaced by a symlink: paused, and never written through.
	h.write("elsewhere.txt", "other\n", 0o644)
	if err := os.Remove(filepath.Join(h.root, "d.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere.txt", filepath.Join(h.root, "d.txt")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docPollInterval)
	st = h.waitState(id, protocol.DocumentStateDeleted)
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err == nil {
		t.Fatal("keep_document wrote through a symlink")
	}
	if h.read("elsewhere.txt") != "other\n" {
		t.Fatal("symlink target changed")
	}
	// Hard-linked meanwhile: read-only pause.
	if _, err := h.resolve(id, protocol.DocumentResolveDiscard, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(id, "discarded", func(s protocol.DocumentStatus) bool { return s.ID == "" })
	h.write("h.txt", "h\n", 0o644)
	hid := h.open("h.txt", "alice")
	if err := os.Link(filepath.Join(h.root, "h.txt"), filepath.Join(h.root, "h2.txt")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docPollInterval)
	h.waitState(hid, protocol.DocumentStateReadOnly)
}

func TestDocumentStaleSaveGenerationNeverWrites(t *testing.T) {
	h := newDocHarness(t)
	h.write("s.txt", "base\n", 0o644)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	h.e.docs.beforeRename = func(string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	t.Cleanup(func() {
		defer func() { _ = recover() }()
		close(release)
	})
	id := h.open("s.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 13)
	ed.gen = 1
	op, _ := ed.insert(0, "new ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveIdle)
	<-entered
	// A rewrite pause (or any invalidation) bumps the generation while the
	// save sits between its checks and the rename.
	h.e.docActor(id).bumpGen()
	release <- struct{}{}
	// The retry under the new generation reaches the same point; the stale
	// save must not have written anything.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no retry after the stale save")
	}
	if h.read("s.txt") != "base\n" {
		t.Fatal("a save with a stale generation wrote the file")
	}
	release <- struct{}{}
	h.waitState(id, protocol.DocumentStateSaved)
	if h.read("s.txt") != "new base\n" {
		t.Fatalf("file %q", h.read("s.txt"))
	}
}

func TestDocumentRewriteHooksFlushPauseAndReconcile(t *testing.T) {
	h := newDocHarness(t)
	h.write("g.txt", "one\ntwo\n", 0o644)
	id := h.open("g.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 14)
	ed.gen = 1
	op, _ := ed.insert(0, "1 ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	release, err := h.e.beginDocumentRewrite(context.Background(), h.root)
	if err != nil {
		t.Fatal(err)
	}
	if h.read("g.txt") != "1 one\ntwo\n" {
		t.Fatalf("pending save not finished before the rewrite: %q", h.read("g.txt"))
	}
	// Git rewrites the file; edits during the pause are kept, not written.
	h.write("g.txt", "1 one\ntwo\nthree\n", 0o644)
	op, _ = ed.insert(0, "0 ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveMax)
	h.barrier(id)
	if h.read("g.txt") != "1 one\ntwo\nthree\n" {
		t.Fatal("wrote during a rewrite pause")
	}
	release()
	ed.next("merged", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventUpdate && ev.Origin == "server"
	})
	h.clock.Advance(docSaveMax)
	h.waitStatus(id, "saved", func(s protocol.DocumentStatus) bool { return s.State == protocol.DocumentStateSaved })
	if h.read("g.txt") != "0 1 one\ntwo\nthree\n" {
		t.Fatalf("file %q", h.read("g.txt"))
	}
}

func TestDocumentRestartReconcilesThenResumes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("r.txt", "line\n", 0o644)
	id := h.open("r.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 15)
	ed.gen = 1
	for i := 0; i < 5; i++ {
		op, _ := ed.insert(0, fmt.Sprint(i))
		h.commit(id)
		ed.next("ack", ackFor(op))
	}
	want := ed.text.ToString()
	// "Crash" between commit and write: the first server never saves (its
	// clock stands still), and a second incarnation starts on the same
	// storage.
	h2 := startDocHarness(t, root, dbPath)
	st := h2.waitStatus(id, "resumed", func(s protocol.DocumentStatus) bool { return s.State == protocol.DocumentStatePending })
	if st.DurableRev != 5 || st.Editor != "alice" {
		t.Fatalf("restored %+v", st)
	}
	ed2 := h2.connect(id, "alice", 15)
	if ed2.text.ToString() != want {
		t.Fatalf("restored text %q want %q", ed2.text.ToString(), want)
	}
	h2.clock.Advance(docSaveMax)
	h2.waitState(id, protocol.DocumentStateSaved)
	if h2.read("r.txt") != want {
		t.Fatalf("file %q", h2.read("r.txt"))
	}
	// A restart after an overlapping external change pauses instead.
	h3root, _ := filepath.EvalSymlinks(t.TempDir())
	db3 := filepath.Join(t.TempDir(), "state.sqlite")
	h3 := startDocHarness(t, h3root, db3)
	h3.write("x.txt", "a\n", 0o644)
	xid := h3.open("x.txt", "alice")
	h3.mustCommand(protocol.DocumentKindEdit, xid, "alice")
	e3 := h3.connect(xid, "alice", 16)
	e3.gen = 1
	op, _ := e3.insert(0, "doc ")
	h3.commit(xid)
	e3.next("ack", ackFor(op))
	h3.write("x.txt", "disk a\n", 0o644)
	h4 := startDocHarness(t, h3root, db3)
	h4.waitState(xid, protocol.DocumentStatePausedConflict)
	if h4.read("x.txt") != "disk a\n" {
		t.Fatal("restart overwrote a diverged file")
	}
}

func TestDocumentWriteFailureRetriesWithoutLoss(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	h := newDocHarness(t)
	h.write("ro/w.txt", "w\n", 0o644)
	id := h.open("ro/w.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 17)
	ed.gen = 1
	dir := filepath.Join(h.root, "ro")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	op, _ := ed.insert(0, "w")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveIdle)
	st := h.waitState(id, protocol.DocumentStateFailed)
	if !strings.Contains(st.Error, "permission denied") || st.DurableRev != 1 {
		t.Fatalf("failed %+v", st)
	}
	// More edits are kept while failing.
	op, _ = ed.insert(0, "v")
	h.commit(id)
	ed.next("ack", ackFor(op))
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docRetryMax)
	h.waitState(id, protocol.DocumentStateSaved)
	if h.read("ro/w.txt") != "vww\n" {
		t.Fatalf("file %q", h.read("ro/w.txt"))
	}
	if !strings.Contains(st.Reason, "retried") {
		t.Fatalf("reason %q", st.Reason)
	}
}

func TestDocumentCompactionPreservesState(t *testing.T) {
	h := newDocHarness(t)
	h.write("k.txt", "", 0o644)
	id := h.open("k.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 18)
	ed.gen = 1
	var last string
	for i := 0; i < docCompactUpdates+10; i++ {
		if i%3 == 2 {
			last = ed.delete(0, 1)
		} else {
			last, _ = ed.insert(ed.text.Len(), "é")
		}
	}
	h.commit(id)
	ed.next("last ack", ackFor(last))
	records, err := h.store.LoadDocuments()
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	rec := records[0]
	if rec.SnapshotRev == 0 || len(rec.Updates) >= docCompactUpdates {
		t.Fatalf("not compacted: snapshot rev %d, %d updates", rec.SnapshotRev, len(rec.Updates))
	}
	updates := make([][]byte, len(rec.Updates))
	for i, u := range rec.Updates {
		updates[i] = u.Data
	}
	d, err := doc.Load(rec.Snapshot, updates)
	if err != nil || d.Text() != ed.text.ToString() {
		t.Fatalf("restored %q want %q (%v)", d.Text(), ed.text.ToString(), err)
	}
}

func TestDocumentStreamLimits(t *testing.T) {
	h := newDocHarness(t)
	h.write("l.txt", "l\n", 0o644)
	id := h.open("l.txt", "alice")
	ctx := context.Background()
	if _, err := h.client.OpenDocumentStream(ctx, "doc-missing", "alice", 0); !errors.Is(err, client.ErrDocumentNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := h.client.OpenDocumentStream(ctx, id, "", 0); err == nil {
		t.Fatal("stream without client_id")
	}
	var streams []*client.DocumentStream
	for i := 0; i < maxDocStreamsPerDoc; i++ {
		s, err := h.client.OpenDocumentStream(ctx, id, "alice", 0)
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, s)
	}
	if _, err := h.client.OpenDocumentStream(ctx, id, "alice", 0); !errors.Is(err, client.ErrDocumentStreamLimit) {
		t.Fatalf("over limit: %v", err)
	}
	for _, s := range streams {
		s.Close()
	}
	h.waitStatus(id, "streams released", func(protocol.DocumentStatus) bool {
		h.e.docs.mu.Lock()
		defer h.e.docs.mu.Unlock()
		return h.e.docs.streams[id] == 0
	})
}

func TestDocumentStreamRequiresAuthentication(t *testing.T) {
	home := t.TempDir()
	c, stop := startTestServer(t, home)
	defer stop()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := c.Command(ctx, protocol.Command{ID: client.ID(), Kind: "project.add", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindOpen, ProjectID: p.TargetID, Path: "f.txt", ClientID: "me"})
	if err != nil {
		t.Fatal(err)
	}
	u := strings.Replace(c.Discovery.URL, "http://", "ws://", 1) + "/v1/documents/" + r.TargetID + "/stream?client_id=me"
	_, resp, err := websocket.Dial(ctx, u, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stream: %v %v", resp, err)
	}
	s, err := c.OpenDocumentStream(ctx, r.TargetID, "me", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ev := <-s.Events()
	if ev.Type != protocol.DocumentEventState || ev.Status == nil || ev.Status.Path != "f.txt" {
		t.Fatalf("first event %+v", ev)
	}
	snap, err := c.Snapshot(ctx)
	if err != nil || len(snap.Documents) != 1 || !containsString(snap.Capabilities, "shared-documents") {
		t.Fatalf("snapshot documents %+v %v", snap.Documents, err)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestDocumentSlowObserverDoesNotBlock(t *testing.T) {
	h := newDocHarness(t)
	h.write("o.txt", "", 0o644)
	id := h.open("o.txt", "alice")
	h.open("o.txt", "bob")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 19)
	fast := h.connect(id, "bob", 0)
	ed.gen = 1
	// A raw observer that never reads.
	u := strings.Replace(h.client.Discovery.URL, "http://", "ws://", 1) + "/v1/documents/" + id + "/stream?client_id=slow"
	slow, _, err := websocket.Dial(context.Background(), u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.CloseNow()
	big := strings.Repeat("z", 16<<10)
	received := 0
	const rounds = 6
	for round := 0; round < rounds; round++ {
		var ops []string
		for i := 0; i < 300; i++ {
			if i%2 == 0 {
				op, _ := ed.insert(0, big)
				ops = append(ops, op)
			} else {
				ops = append(ops, ed.delete(0, len(big)))
			}
		}
		h.commit(id)
		for _, op := range ops {
			ed.next("ack", ackFor(op))
		}
		for i := 0; i < len(ops); i++ {
			fast.next("update", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventUpdate })
			received++
		}
	}
	if received != rounds*300 || fast.text.ToString() != "" {
		t.Fatalf("fast observer received %d, text %d bytes", received, len(fast.text.ToString()))
	}
	// The slow connection was dropped instead of holding anything up; once
	// it reads, it finds a policy-violation close.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		if _, _, err := slow.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("slow observer ended with %v", err)
			}
			break
		}
	}
}

func TestDocumentPauseSurvivesRestart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("p.txt", "p\n", 0o644)
	id := h.open("p.txt", "alice")
	if err := os.Remove(filepath.Join(root, "p.txt")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docPollInterval)
	h.waitState(id, protocol.DocumentStateDeleted)
	// The file returns; a restart must not resume on its own.
	h.write("p.txt", "p\n", 0o644)
	h2 := startDocHarness(t, root, dbPath)
	st := h2.waitState(id, protocol.DocumentStateDeleted)
	if !st.Versions {
		t.Fatalf("restored %+v", st)
	}
	h2.clock.Advance(docPollInterval)
	h2.barrier(id)
	if st, _ := h2.status(id); st.State != protocol.DocumentStateDeleted {
		t.Fatalf("pause lifted without resolution: %+v", st)
	}
}
