package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// collabHarness is a harness whose server edits simultaneously.
func collabHarness(t *testing.T, text string) *docHarness {
	t.Helper()
	h := newDocHarness(t, text)
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Collaborative = true })
	h.settle()
	return h
}

// fakeClock drives presence timing in tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestDocCollaborativeEditsWithoutRoleGate(t *testing.T) {
	h := collabHarness(t, "shared\n")
	// Another client is the most recent claimant; editing is not gated.
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor = "other" })
	h.settle()
	screen := h.screen()
	if strings.Contains(screen, protocol.DocumentSimultaneousUnavailable) || strings.Contains(screen, "Take over") {
		t.Fatalf("single-editor gate shown:\n%s", screen)
	}
	h.edit()
	if slices := h.srv.kinds(); len(slices) > 0 && slices[len(slices)-1] == protocol.DocumentKindEdit {
		t.Fatal("collaborative edit still requested the role")
	}
	h.typeText("my ")
	if h.srv.text() != "my shared\n" {
		t.Fatalf("server %q", h.srv.text())
	}
}

func TestDocLegacyServerKeepsGate(t *testing.T) {
	h := newDocHarness(t, "text\n")
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor = "other" })
	h.settle()
	if !strings.Contains(h.screen(), "Take over") {
		t.Fatal("legacy gate missing")
	}
	h.m.setFocus("files-text")
	h.key(tea.KeyEnter, 0)
	if h.m.docEdit != "" {
		t.Fatal("legacy server let a non-editor type")
	}
}

func TestDocPresenceThrottleKeepaliveAndClear(t *testing.T) {
	h := collabHarness(t, "one\ntwo\nthree\n")
	clock := &fakeClock{t: time.Unix(1000, 0)}
	h.session().ed.now = clock.now
	h.edit()
	if n := h.srv.presenceCount(); n != 1 {
		t.Fatalf("entering edit mode sent %d presences", n)
	}
	// Moves within 100 ms are coalesced into one throttled send.
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyRight, 0)
	if n := h.srv.presenceCount(); n != 1 {
		t.Fatalf("throttle: %d presences", n)
	}
	clock.advance(150 * time.Millisecond)
	h.do(docPresenceMsg{id: h.session().id, gen: h.session().gen})
	if n := h.srv.presenceCount(); n != 2 {
		t.Fatalf("after the interval: %d presences", n)
	}
	// At rest nothing is sent until the keepalive.
	clock.advance(5 * time.Second)
	h.do(docPresenceMsg{id: h.session().id, gen: h.session().gen})
	if n := h.srv.presenceCount(); n != 2 {
		t.Fatalf("resting: %d presences", n)
	}
	clock.advance(6 * time.Second)
	h.do(docPresenceMsg{id: h.session().id, gen: h.session().gen, keepalive: true})
	if n := h.srv.presenceCount(); n != 3 {
		t.Fatalf("keepalive: %d presences", n)
	}
	// The sent positions resolve to this client's cursor.
	h.srv.mu.Lock()
	last := h.srv.presences[len(h.srv.presences)-1]
	h.srv.mu.Unlock()
	rp, err := crdt.DecodeRelativePosition(last.head)
	if err != nil {
		t.Fatal(err)
	}
	s := h.session()
	if abs, ok := crdt.ToAbsolutePosition(s.rep.doc, rp); !ok || abs.Index != s.rep.txt.offset(s.ed.cur) {
		t.Fatalf("head resolves to %v, cursor at %d", abs, s.rep.txt.offset(s.ed.cur))
	}
	// Leaving edit mode clears it.
	h.key(tea.KeyEscape, 0)
	h.srv.mu.Lock()
	last = h.srv.presences[len(h.srv.presences)-1]
	h.srv.mu.Unlock()
	if last.anchor != nil || last.head != nil {
		t.Fatal("presence not cleared on Esc")
	}
}

// peerAt sends a peer presence with a selection [a, h) of s's text.
func peerAt(t *testing.T, h *docHarness, peer, client string, color, a, head int) {
	t.Helper()
	s := h.session()
	enc := func(i int) []byte {
		return crdt.EncodeRelativePosition(crdt.CreateRelativePositionFromIndex(s.rep.text, i, 0))
	}
	h.srv.sendPeer(protocol.DocumentPeer{Peer: peer, Client: client, Color: color, Anchor: enc(a), Head: enc(head)})
	h.settle()
}

func TestDocPeersRenderMoveWithTextAndNeverMoveOwnCursor(t *testing.T) {
	h := collabHarness(t, "alpha beta\ngamma\n")
	clock := &fakeClock{t: time.Unix(1000, 0)}
	h.session().ed.now = clock.now
	h.edit()
	h.key(tea.KeyDown, 0)
	s := h.session()
	own := s.ed.cur
	peerAt(t, h, "p9", "client-bob", 3, 0, 5) // selects "alpha"
	peers := h.m.livePeers(s)
	if len(peers) != 1 || !peers[0].selection || peers[0].a != (edPos{0, 0}) || peers[0].head != (edPos{0, 5}) || s.ed.cur != own {
		t.Fatalf("peers %+v own %v→%v", peers, own, s.ed.cur)
	}
	if !strings.Contains(h.screen(), "1 other editing") || !strings.Contains(h.screen(), "client") {
		t.Fatalf("no peers list:\n%s", h.screen())
	}
	// The peer's cursor cell is painted in its color and its selection
	// tinted.
	row := h.m.compose(true).rows[h.m.measure().docText.Y]
	for _, st := range []string{style(h.m.colors().panel, h.m.peerColor(3)).Render("x"), style(h.m.colors().text, h.m.peerTint(3)).Render("x")} {
		if sgr := st[:strings.Index(st, "x")]; !strings.Contains(row, sgr) {
			t.Fatalf("peer paint %q missing in %q", sgr, row)
		}
	}
	// Text inserted before the peer moves its cursor with the text.
	h.srv.serverEdit(t, doc.Edit{Start: 0, End: 0, Text: ">>"})
	h.settle()
	if peers = h.m.livePeers(s); peers[0].head != (edPos{0, 7}) || s.ed.cur.line != 1 {
		t.Fatalf("after remote insert: %+v own %v", peers, s.ed.cur)
	}
	// Removed drops it; a silent peer goes stale.
	h.srv.sendPeer(protocol.DocumentPeer{Peer: "p9", Removed: true})
	h.settle()
	if len(h.m.livePeers(s)) != 0 {
		t.Fatal("removed peer still shown")
	}
	peerAt(t, h, "p10", "client-eve", 5, 2, 2)
	clock.advance(docPeerStale + time.Second)
	if len(h.m.livePeers(s)) != 0 {
		t.Fatal("stale peer still shown")
	}
}

func TestDocRateLimitHonoursRetryAfter(t *testing.T) {
	h := collabHarness(t, "x\n")
	h.edit()
	h.srv.retryAfter = 300
	start := time.Now()
	h.typeText("a")
	s := h.session()
	if !s.resending || !s.rateLimited || len(s.pending) != 1 {
		t.Fatalf("not paused: resending %v limited %v pending %d", s.resending, s.rateLimited, len(s.pending))
	}
	// A status in between does not end the wait early.
	h.srv.setStatus(func(*protocol.DocumentStatus) {})
	h.settle()
	if h.srv.text() != "x\n" {
		t.Fatal("resent before RetryAfterMs")
	}
	for h.srv.text() != "ax\n" {
		if time.Since(start) > 3*time.Second {
			t.Fatal("never resent")
		}
		h.settle()
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatalf("resent after %v", time.Since(start))
	}
	// Another reason with RetryAfterMs follows the same hold-and-resend.
	h.srv.mu.Lock()
	h.srv.retryAfter, h.srv.retryReason = 200, "pending_full"
	h.srv.mu.Unlock()
	h.typeText("b")
	for h.srv.text() != "abx\n" {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("never resent after %q: %q", "pending_full", h.srv.text())
		}
		h.settle()
	}
}

func TestDocPeerAheadOfItsEditStaysHidden(t *testing.T) {
	h := collabHarness(t, "abc\n")
	h.edit()
	s := h.session()
	// Another replica inserts "Z" and publishes its cursor on it before its
	// edit reaches this client.
	peer := crdt.New(crdt.WithClientID(4242))
	if err := crdt.ApplyUpdateV1(peer, crdt.EncodeStateAsUpdateV1(s.rep.doc, nil), nil); err != nil {
		t.Fatal(err)
	}
	text := peer.GetText(protocol.DocumentTextName)
	var update []byte
	unsub := peer.OnUpdate(func(u []byte, _ any) { update = append([]byte(nil), u...) })
	peer.Transact(func(txn *crdt.Transaction) { text.Insert(txn, 1, "Z", nil) })
	unsub()
	head := crdt.EncodeRelativePosition(crdt.CreateRelativePositionFromIndex(text, 1, 0))
	h.srv.sendPeer(protocol.DocumentPeer{Peer: "p7", Client: "client-z", Color: 2, Anchor: head, Head: head})
	h.settle()
	if n := len(h.m.livePeers(s)); n != 0 {
		t.Fatalf("unresolvable peer shown (%d)", n)
	}
	// The edit arrives: the cursor resolves.
	h.srv.mu.Lock()
	if _, err := h.srv.d.ApplyClient(update, func(c uint64) bool { return c == 4242 }, nil); err != nil {
		h.srv.mu.Unlock()
		t.Fatal(err)
	}
	h.srv.rev++
	for _, c := range h.srv.conns {
		h.srv.send(c, protocol.DocumentEvent{Type: protocol.DocumentEventUpdate, Rev: h.srv.rev, Update: update, Origin: "client", Client: "client-z"})
	}
	h.srv.mu.Unlock()
	h.settle()
	if peers := h.m.livePeers(s); len(peers) != 1 || peers[0].head != (edPos{0, 1}) || s.rep.txt.String() != "aZbc\n" {
		t.Fatalf("after the edit: %+v %q", peers, s.rep.txt.String())
	}
}

func TestDocUndoOwnOnlyWithConcurrentPeer(t *testing.T) {
	h := collabHarness(t, "line one\nline two\n")
	h.edit()
	h.key(tea.KeyEnd, 0)
	// Our typing run interleaves with a peer typing on the next line.
	for i, r := range "XYZ" {
		h.typeText(string(r))
		h.srv.serverEdit(t, doc.Edit{Start: len(h.srv.text()) - 1, End: len(h.srv.text()) - 1, Text: string(rune('a' + i))})
		h.settle()
	}
	if h.srv.text() != "line oneXYZ\nline twoabc\n" {
		t.Fatalf("setup %q", h.srv.text())
	}
	h.key('z', tea.ModCtrl)
	if h.srv.text() != "line one\nline twoabc\n" {
		t.Fatalf("undo: %q", h.srv.text())
	}
}

func TestDocReplicaConflictRetriesSameReplicaFirst(t *testing.T) {
	h := collabHarness(t, "x\n")
	s := h.session()
	replica := s.replicaID
	var conflicts atomic.Int32
	conflicts.Store(1)
	h.m.docDial = func(ctx context.Context, id, clientID string, r uint64) (docConn, error) {
		if conflicts.Add(-1) >= 0 {
			return nil, &protocol.Error{Code: "replica_conflict", Message: "still closing"}
		}
		return h.srv.dial(ctx, id, clientID, r)
	}
	h.srv.drop()
	for range 20 {
		if s.connected {
			break
		}
		time.Sleep(100 * time.Millisecond)
		h.settle()
	}
	if !s.connected || s.replicaID != replica || s.rep == nil {
		t.Fatalf("connected %v replica %d→%d", s.connected, replica, s.replicaID)
	}
	// A conflict that persists recovers with a new replica.
	conflicts.Store(2)
	h.srv.drop()
	for range 40 {
		if s.connected && s.replicaID != replica {
			break
		}
		time.Sleep(100 * time.Millisecond)
		h.settle()
	}
	if !s.connected || s.replicaID == replica {
		t.Fatalf("persistent conflict: connected %v replica %d", s.connected, s.replicaID)
	}
}

func TestDocRateLimitClearedOnReconnect(t *testing.T) {
	h := collabHarness(t, "x\n")
	h.edit()
	h.srv.mu.Lock()
	h.srv.retryAfter = 5000
	h.srv.mu.Unlock()
	h.typeText("a")
	s := h.session()
	if !s.rateLimited {
		t.Fatal("not rate limited")
	}
	h.srv.drop()
	for i := 0; i < 40 && !(s.connected && h.srv.text() == "ax\n"); i++ {
		time.Sleep(50 * time.Millisecond)
		h.settle()
	}
	if h.srv.text() != "ax\n" || s.rateLimited || s.resending {
		t.Fatalf("after reconnect: %q limited %v resending %v", h.srv.text(), s.rateLimited, s.resending)
	}
	// Rule 6 again: a plain unavailable waits for a status that is not failed.
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.State = protocol.DocumentStateFailed })
	h.settle()
	h.srv.mu.Lock()
	h.srv.busyNext = true
	h.srv.mu.Unlock()
	h.typeText("b")
	if h.srv.text() != "ax\n" || !s.resending {
		t.Fatalf("busy: %q resending %v", h.srv.text(), s.resending)
	}
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.State = protocol.DocumentStateSaved })
	h.settle()
	if h.srv.text() != "abx\n" {
		t.Fatalf("not resent on recovery: %q", h.srv.text())
	}
}

func TestDocPresenceWaitsForQueueAndNeverLosesClear(t *testing.T) {
	h := collabHarness(t, "one\ntwo\n")
	clock := &fakeClock{t: time.Unix(1000, 0)}
	s := h.session()
	s.ed.now = clock.now
	h.edit()
	sent := h.srv.presenceCount()
	// The writer queue is full: a move waits in the slot.
	writes := s.writes
	s.writes = make(chan func(docConn) error)
	h.key(tea.KeyDown, 0)
	clock.advance(200 * time.Millisecond)
	h.do(docPresenceMsg{id: s.id, gen: s.gen})
	if s.pres.slot == nil || s.pres.slot.head == nil {
		t.Fatal("move not kept waiting")
	}
	// Leaving edit mode replaces it with a clear.
	h.key(tea.KeyEscape, 0)
	if s.pres.slot == nil || s.pres.slot.head != nil {
		t.Fatal("clear not kept waiting")
	}
	// The queue drains: the clear goes out; the dropped move never does.
	s.writes = writes
	h.do(docPresenceMsg{id: s.id, gen: s.gen})
	h.srv.mu.Lock()
	all := h.srv.presences
	h.srv.mu.Unlock()
	if len(all) != sent+1 || all[len(all)-1].head != nil || s.pres.slot != nil || s.pres.shown {
		t.Fatalf("presences %d (was %d), last head %v", len(all), sent, all[len(all)-1].head)
	}
}

func TestDocLegacyServerSendsNoPresence(t *testing.T) {
	h := newDocHarness(t, "x\n")
	h.edit()
	h.key(tea.KeyRight, 0)
	if n := h.srv.presenceCount(); n != 0 {
		t.Fatalf("legacy server got %d presences", n)
	}
}

func TestDocNotEditorRedialEndsSessionKeepingDraft(t *testing.T) {
	h := collabHarness(t, "x\n")
	h.edit()
	h.srv.hold = true
	h.typeText("DRAFT")
	var dials atomic.Int32
	h.m.docDial = func(ctx context.Context, id, clientID string, r uint64) (docConn, error) {
		dials.Add(1)
		return nil, &protocol.Error{Code: protocol.DocumentRejectNotEditor, Message: "open the document first"}
	}
	h.srv.drop()
	for range 10 {
		time.Sleep(100 * time.Millisecond)
		h.settle()
	}
	b := h.m.currentFilesView().buffer()
	if n := dials.Load(); n > 4 {
		t.Fatalf("redial loop: %d dials", n)
	}
	kept := len(b.docLost) + len(h.m.docOrphans)
	if s := h.m.bufferDoc(b); s != nil {
		kept += len(s.lost)
	}
	if kept == 0 {
		t.Fatal("draft dropped")
	}
}

func TestDocTypingRunJoinsByUserMoves(t *testing.T) {
	h := collabHarness(t, "one\ntwo\n")
	h.edit()
	h.key(tea.KeyEnd, 0)
	h.typeText("a")
	// Moving away and back starts a new step even at the same place.
	h.key(tea.KeyLeft, 0)
	h.key(tea.KeyRight, 0)
	h.typeText("b")
	h.key('z', tea.ModCtrl)
	if h.srv.text() != "onea\ntwo\n" {
		t.Fatalf("undo after a move: %q", h.srv.text())
	}
	// A peer's insert at the cursor between keystrokes keeps one run.
	h.key(tea.KeyEnd, 0)
	h.typeText("x")
	s := h.session()
	at := len(s.rep.txt.lines[0])
	h.srv.serverEdit(t, doc.Edit{Start: at, End: at, Text: "S"})
	h.settle()
	h.typeText("y")
	h.key('z', tea.ModCtrl)
	if got := h.srv.text(); strings.ContainsAny(got, "xy") || !strings.Contains(got, "S") {
		t.Fatalf("run split by a peer insert: %q", got)
	}
}
