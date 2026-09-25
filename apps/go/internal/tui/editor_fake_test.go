package tui

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// fakeDocServer is an in-memory document server with the real validation
// (internal/doc) and the stream semantics of protocol/document.go: state on
// connect, acks after "commit", broadcasts to other streams, resync states
// after refusals, single editor with EditGen.
type fakeDocServer struct {
	mu       sync.Mutex
	d        *doc.Doc
	rev      int64
	status   protocol.DocumentStatus
	conns    []*fakeDocConn
	replicas map[uint64]string
	dials    []uint64
	// hold keeps update requests unprocessed until release.
	hold      bool
	held      []heldReq
	rejectOps []string // refuse the next updates with these reasons (resync)
	busyNext  bool     // refuse the next update as unavailable
	commands  []protocol.Command
	versions  protocol.DocumentVersions
	staleNext bool
	applied   map[string]int // op → times acked
}

type heldReq struct {
	c   *fakeDocConn
	op  string
	gen int64
	u   []byte
}

type fakeDocConn struct {
	srv      *fakeDocServer
	clientID string
	replica  uint64
	events   chan protocol.DocumentEvent
	once     sync.Once
	err      error
}

func newFakeDocServer(t *testing.T, text string) *fakeDocServer {
	t.Helper()
	d, _, err := doc.New(text)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeDocServer{d: d, replicas: map[uint64]string{}, applied: map[string]int{},
		status: protocol.DocumentStatus{ID: "doc-1", Path: "README.md", State: protocol.DocumentStateSaved, Newline: "lf", EditGen: 1}}
}

func (s *fakeDocServer) statusLocked() protocol.DocumentStatus {
	st := s.status
	st.DurableRev = s.rev
	if st.State == protocol.DocumentStateSaved {
		st.SavedRev = s.rev
	}
	return st
}

func (s *fakeDocServer) dial(_ context.Context, id, clientID string, replica uint64) (docConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "doc-1" {
		return nil, client.ErrDocumentNotFound
	}
	if bound := s.replicas[replica]; bound != "" && bound != clientID {
		return nil, &protocol.Error{Code: "replica_conflict", Message: "bound"}
	}
	s.replicas[replica] = clientID
	s.dials = append(s.dials, replica)
	c := &fakeDocConn{srv: s, clientID: clientID, replica: replica, events: make(chan protocol.DocumentEvent, 1024)}
	s.conns = append(s.conns, c)
	st := s.statusLocked()
	c.events <- protocol.DocumentEvent{Type: protocol.DocumentEventState, Rev: s.rev, Update: s.d.EncodeState(), Status: &st}
	return c, nil
}

func (c *fakeDocConn) Events() <-chan protocol.DocumentEvent { return c.events }
func (c *fakeDocConn) Err() error                            { return c.err }
func (c *fakeDocConn) Sync([]byte) error                     { return nil }
func (c *fakeDocConn) Close() error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()
	c.srv.dropLocked(c, nil)
	return nil
}

func (s *fakeDocServer) dropLocked(c *fakeDocConn, err error) {
	for i, x := range s.conns {
		if x == c {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			break
		}
	}
	c.once.Do(func() { c.err = err; close(c.events) })
}

// drop ends every stream as a network failure would; held requests are lost.
func (s *fakeDocServer) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = nil
	for len(s.conns) > 0 {
		s.dropLocked(s.conns[0], errors.New("connection lost"))
	}
}

func (c *fakeDocConn) SendUpdate(op string, gen int64, u []byte) error {
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connectedLocked(c) {
		return errors.New("closed")
	}
	if s.hold {
		s.held = append(s.held, heldReq{c, op, gen, u})
		return nil
	}
	s.updateLocked(c, op, gen, u)
	return nil
}

func (s *fakeDocServer) connectedLocked(c *fakeDocConn) bool {
	for _, x := range s.conns {
		if x == c {
			return true
		}
	}
	return false
}

func (s *fakeDocServer) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = false
	held := s.held
	s.held = nil
	for _, r := range held {
		if s.connectedLocked(r.c) {
			s.updateLocked(r.c, r.op, r.gen, r.u)
		}
	}
}

func (s *fakeDocServer) send(c *fakeDocConn, ev protocol.DocumentEvent) {
	select {
	case c.events <- ev:
	default:
	}
}

func (s *fakeDocServer) rejectLocked(c *fakeDocConn, op, reason string, resync bool) {
	s.send(c, protocol.DocumentEvent{Type: protocol.DocumentEventRejected, Op: op, Rejected: &protocol.DocumentRejected{Reason: reason, Resync: resync}})
	if resync {
		// The stream applies nothing more and closes normally.
		s.dropLocked(c, nil)
	}
}

func (s *fakeDocServer) updateLocked(c *fakeDocConn, op string, gen int64, u []byte) {
	switch {
	case len(s.rejectOps) > 0:
		reason := s.rejectOps[0]
		s.rejectOps = s.rejectOps[1:]
		s.rejectLocked(c, op, reason, true)
		return
	case s.busyNext:
		s.busyNext = false
		s.rejectLocked(c, op, protocol.DocumentRejectUnavailable, false)
		return
	case c.clientID != s.status.Editor:
		s.rejectLocked(c, op, protocol.DocumentRejectNotEditor, true)
		return
	case gen != s.status.EditGen:
		s.rejectLocked(c, op, protocol.DocumentRejectStaleGeneration, true)
		return
	}
	replica := c.replica
	applied, err := s.d.ApplyClient(u, func(id uint64) bool { return id == replica }, nil)
	if err != nil {
		reason := protocol.DocumentRejectInvalid
		var de *doc.Error
		if errors.As(err, &de) {
			reason = de.Reason
		}
		s.rejectLocked(c, op, reason, true)
		return
	}
	s.applied[op]++
	if applied.Changed {
		s.rev++
		for _, o := range s.conns {
			if o != c {
				s.send(o, protocol.DocumentEvent{Type: protocol.DocumentEventUpdate, Rev: s.rev, Update: u, Origin: "client", Client: c.clientID})
			}
		}
	}
	s.send(c, protocol.DocumentEvent{Type: protocol.DocumentEventAck, Op: op, Rev: s.rev})
}

// serverEdit applies a server-origin edit (a merged disk change) and
// broadcasts it.
func (s *fakeDocServer) serverEdit(t *testing.T, e doc.Edit) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.d.Replace([]doc.Edit{e})
	if err != nil {
		t.Fatal(err)
	}
	s.rev++
	for _, o := range s.conns {
		s.send(o, protocol.DocumentEvent{Type: protocol.DocumentEventUpdate, Rev: s.rev, Update: u, Origin: "server"})
	}
}

// setStatus changes the status and broadcasts it.
func (s *fakeDocServer) setStatus(f func(*protocol.DocumentStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.status)
	st := s.statusLocked()
	for _, o := range s.conns {
		s.send(o, protocol.DocumentEvent{Type: protocol.DocumentEventStatus, Status: &st})
	}
}

func (s *fakeDocServer) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Text()
}

func (s *fakeDocServer) Command(_ context.Context, c protocol.Command) (protocol.Receipt, error) {
	s.mu.Lock()
	s.commands = append(s.commands, c)
	var err error
	target := c.TargetID
	switch c.Kind {
	case protocol.DocumentKindOpen:
		if strings.HasSuffix(c.Path, ".lock") {
			err = &protocol.Error{Code: "document_read_only", Message: "The file has other hard links; it opens read-only."}
		}
		target = "doc-1"
	case protocol.DocumentKindEdit:
		switch s.status.Editor {
		case "", c.ClientID:
			s.status.Editor = c.ClientID
		default:
			err = &protocol.Error{Code: "editor_busy", Message: "busy"}
		}
	case protocol.DocumentKindTakeEdit:
		if s.status.Editor != c.ClientID {
			s.status.Editor = c.ClientID
			s.status.EditGen++
		}
	case protocol.DocumentKindResolve:
		if s.staleNext {
			s.staleNext = false
			err = &protocol.Error{Code: "stale_document", Message: "changed"}
		}
	}
	s.mu.Unlock()
	if err != nil {
		return protocol.Receipt{}, err
	}
	if c.Kind == protocol.DocumentKindEdit || c.Kind == protocol.DocumentKindTakeEdit {
		s.setStatus(func(*protocol.DocumentStatus) {})
	}
	return protocol.Receipt{ID: c.ID, State: "accepted", TargetID: target}, nil
}

func (s *fakeDocServer) DocumentVersions(context.Context, string) (protocol.DocumentVersions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.versions, nil
}

func (s *fakeDocServer) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.commands {
		out = append(out, c.Kind)
	}
	return out
}

// docHarness runs a model's commands concurrently and feeds their messages
// back, like the Bubble Tea runtime.
type docHarness struct {
	t    *testing.T
	m    *Model
	srv  *fakeDocServer
	api  *fakeFiles
	msgs chan tea.Msg
}

func (h *docHarness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if msg == nil {
			return
		}
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := range v.Len() {
				h.run(v.Index(i).Interface().(tea.Cmd))
			}
			return
		}
		switch msg.(type) {
		case filesTickMsg, activityTick, saveTick, noticeExpired:
			return // Pollers and timers are driven explicitly.
		}
		h.msgs <- msg
	}()
}

// settle applies messages until none arrive for a short while.
func (h *docHarness) settle() {
	h.t.Helper()
	for {
		select {
		case msg := <-h.msgs:
			_, cmd := h.m.Update(msg)
			h.run(cmd)
		case <-time.After(60 * time.Millisecond):
			return
		}
	}
}

// do applies one message (a key, a click) and settles.
func (h *docHarness) do(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
	h.settle()
}

func (h *docHarness) cmd(cmd tea.Cmd) {
	h.t.Helper()
	h.run(cmd)
	h.settle()
}

func (h *docHarness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		if r == '\n' {
			h.do(tea.KeyPressMsg{Code: tea.KeyEnter})
			continue
		}
		h.do(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (h *docHarness) key(code rune, mod tea.KeyMod) {
	h.t.Helper()
	h.do(tea.KeyPressMsg{Code: code, Mod: mod})
}

func (h *docHarness) session() *docSession {
	b := h.m.currentFilesView().buffer()
	return h.m.bufferDoc(b)
}

// click activates the control with key through a pointer click.
func (h *docHarness) click(key string) {
	h.t.Helper()
	for _, hit := range h.m.measure().hits {
		if hit.Key == key {
			h.do(tea.MouseClickMsg{X: hit.Rect.X, Y: hit.Rect.Y, Button: tea.MouseLeft})
			return
		}
	}
	h.t.Fatalf("no hit %q:\n%s", key, h.screen())
}

func (h *docHarness) screen() string { return frameText(h.m.compose(true)) }

// newDocHarness opens README.md as a shared document on a fake server.
func newDocHarness(t *testing.T, text string) *docHarness {
	t.Helper()
	docResendWait = 100 * time.Millisecond
	srv := newFakeDocServer(t, text)
	api := representativeFiles()
	api.reads["README.md"] = protocol.FileRead{Path: "README.md", Kind: "text", Size: int64(len(text)), Token: "t1", Encoding: "utf-8", Newline: "lf", Text: text}
	m := testModel()
	m.connected = true
	m.filesReads = api
	m.docAPIs, m.docDial = srv, srv.dial
	m.clipboardWrite = func(string) error { return nil }
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read", docCapability)
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].Checkout = "/src/repo-" + m.snapshot.Threads[i].ID
	}
	h := &docHarness{t: t, m: m, srv: srv, api: api, msgs: make(chan tea.Msg, 4096)}
	h.do(tea.WindowSizeMsg{Width: 200, Height: 44})
	m.openSurface("files", "")
	h.key(tea.KeyF7, 0) // maximize the right surface
	h.settle()
	h.cmd(m.openFilesBuffer("README.md"))
	if s := h.session(); s == nil || s.rep == nil {
		t.Fatalf("document did not open: %v", srv.kinds())
	}
	return h
}

// edit enters edit mode with Enter (unless already editing).
func (h *docHarness) edit() {
	h.t.Helper()
	if h.m.docEdit != "" {
		return
	}
	h.m.setFocus("files-text")
	h.key(tea.KeyEnter, 0)
	if h.m.docEdit == "" {
		h.t.Fatalf("not editing: %s", h.m.notice.text)
	}
}

func opIDs(ops []docOp) []string {
	var out []string
	for _, o := range ops {
		out = append(out, o.id)
	}
	return out
}

var _ = strconv.Itoa

// choose activates the menu item labelled label.
func (h *docHarness) choose(label string) {
	h.t.Helper()
	for i, item := range h.m.menu {
		if item.Label == label {
			h.m.menuIndex = i
			h.key(tea.KeyEnter, 0)
			return
		}
	}
	h.t.Fatalf("no menu item %q", label)
}

// menuNotes joins the menu's note rows.
func (h *docHarness) menuNotes() string {
	var notes []string
	for _, item := range h.m.menu {
		if item.Note != "" {
			notes = append(notes, item.Note)
		}
	}
	return strings.Join(notes, " ")
}
