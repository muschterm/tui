//go:build unix

package server

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// Simultaneous editors and presence (editor slice C).

// serverText reads the authoritative text.
func (h *docHarness) serverText(id string) string {
	var text string
	h.onActor(id, func(a *docActor) { text = a.d.Text() })
	return text
}

// syncTo applies events until the replica holds want.
func (ed *editor) syncTo(want string) {
	ed.t.Helper()
	timeout := time.After(10 * time.Second)
	for ed.text.ToString() != want {
		select {
		case ev, ok := <-ed.stream.Events():
			if !ok {
				ed.t.Fatalf("stream ended before converging: %v", ed.stream.Err())
			}
			if ev.Type == protocol.DocumentEventUpdate {
				if err := crdt.ApplyUpdateV1(ed.replica, ev.Update, nil); err != nil {
					ed.t.Fatal(err)
				}
			}
		case <-timeout:
			ed.t.Fatalf("replica %q never reached %q", ed.text.ToString(), want)
		}
	}
}

// drain applies whatever events are already queued, without waiting.
func (ed *editor) drain() {
	for {
		select {
		case ev, ok := <-ed.stream.Events():
			if !ok {
				return
			}
			if ev.Type == protocol.DocumentEventUpdate {
				if err := crdt.ApplyUpdateV1(ed.replica, ev.Update, nil); err != nil {
					ed.t.Fatal(err)
				}
			}
		default:
			return
		}
	}
}

func collabDoc(t *testing.T, h *docHarness, rel, content string, clients ...string) (string, []*editor) {
	t.Helper()
	h.write(rel, content, 0o644)
	var id string
	for _, c := range clients {
		id = h.open(rel, c)
	}
	var eds []*editor
	for i, c := range clients {
		eds = append(eds, h.connect(id, c, uint64(500+i)))
	}
	return id, eds
}

var collabAlphabet = []string{"a", "b", "é", "漢", "\n", " "}

func randomEdit(rng *rand.Rand, ed *editor) {
	n := ed.text.Len()
	if n > 0 && rng.Intn(3) == 0 {
		at := rng.Intn(n)
		ed.delete(at, min(n-at, 1+rng.Intn(3)))
		return
	}
	ed.insert(rng.Intn(n+1), collabAlphabet[rng.Intn(len(collabAlphabet))])
}

func TestDocumentConcurrentEditorsConverge(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		h := newDocHarness(t)
		clients := []string{"alice", "bob", "carol", "dave", "erin"}[:3+seed%3]
		id, eds := collabDoc(t, h, "c.txt", "start\n", clients...)
		rng := rand.New(rand.NewSource(seed))
		for round := 0; round < 120; round++ {
			ed := eds[rng.Intn(len(eds))]
			// Editors work on stale replicas: they drain remote updates only
			// sometimes, so their edits are genuinely concurrent.
			if rng.Intn(3) == 0 {
				ed.drain()
			}
			for k := 0; k < 1+rng.Intn(3); k++ {
				randomEdit(rng, ed)
			}
			if rng.Intn(5) == 0 {
				h.commit(id)
			}
		}
		h.commit(id)
		want := h.serverText(id)
		for _, ed := range eds {
			ed.syncTo(want)
		}
		if st, _ := h.status(id); st.DurableRev == 0 || !st.Collaborative {
			t.Fatalf("seed %d: status %+v", seed, st)
		}
		// Every editor's text also survives a restart of the stored log.
		records, err := h.store.LoadDocuments()
		if err != nil || len(records) != 1 {
			t.Fatal(records, err)
		}
		updates := make([][]byte, len(records[0].Updates))
		for i, u := range records[0].Updates {
			updates[i] = u.Data
		}
		d, err := doc.Load(records[0].Snapshot, updates)
		if err != nil || d.Text() != want {
			t.Fatalf("seed %d: restored %q want %q (%v)", seed, d.Text(), want, err)
		}
	}
}

func encodePos(ed *editor, index int) []byte {
	return crdt.EncodeRelativePosition(crdt.CreateRelativePositionFromIndex(ed.text, index, 0))
}

func nextPresence(t *testing.T, ed *editor, match func(protocol.DocumentPeer) bool) protocol.DocumentPeer {
	t.Helper()
	var found protocol.DocumentPeer
	ed.next("presence", func(ev protocol.DocumentEvent) bool {
		for _, p := range ev.Presence {
			if match(p) {
				found = p
				return true
			}
		}
		return false
	})
	return found
}

func TestDocumentPresenceDeliveryCoalescingAndRemoval(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "p.txt", "hello world\n", "alice", "bob", "carol")
	alice, bob, carol := eds[0], eds[1], eds[2]
	if err := alice.stream.SendPresence(encodePos(alice, 0), encodePos(alice, 5)); err != nil {
		t.Fatal(err)
	}
	p := nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Client == "alice" && !p.Removed })
	rp, err := crdt.DecodeRelativePosition(p.Head)
	if err != nil {
		t.Fatal(err)
	}
	if abs, ok := crdt.ToAbsolutePosition(bob.replica, rp); !ok || abs.Index != 5 || p.Replica != 500 {
		t.Fatalf("presence %+v resolves to %+v", p, abs)
	}
	aliceColor, alicePeer := p.Color, p.Peer
	// A burst is coalesced to the latest head.
	for i := 0; i < 30; i++ {
		if err := alice.stream.SendPresence(encodePos(alice, 0), encodePos(alice, i%12)); err != nil {
			t.Fatal(err)
		}
	}
	if err := alice.stream.SendPresence(encodePos(alice, 0), encodePos(alice, 7)); err != nil {
		t.Fatal(err)
	}
	seen := 0
	var last protocol.DocumentPeer
	found := false
	for tries := 0; !found && tries < 200; tries++ {
		h.clock.Advance(docPresenceEvery)
		deadline := time.After(20 * time.Millisecond)
	read:
		for {
			select {
			case ev := <-bob.stream.Events():
				for _, p := range ev.Presence {
					if p.Peer != alicePeer {
						continue
					}
					seen++
					rp, _ := crdt.DecodeRelativePosition(p.Head)
					abs, _ := crdt.ToAbsolutePosition(bob.replica, rp)
					if abs.Index == 7 {
						last, found = p, true
					}
				}
			case <-deadline:
				break read
			}
		}
	}
	if !found {
		t.Fatal("the latest presence never arrived")
	}
	if seen > 3 || last.Color != aliceColor {
		t.Fatalf("burst delivered %d presence entries (color %d vs %d)", seen, last.Color, aliceColor)
	}
	// Invalid presence is refused without affecting the stream.
	if err := alice.stream.SendPresence([]byte{0xff, 0xff}, encodePos(alice, 1)); err != nil {
		t.Fatal(err)
	}
	alice.next("invalid presence", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventRejected && ev.Rejected.Reason == protocol.DocumentRejectInvalid
	})
	// A new stream receives the live cursors; closing removes them.
	dave := h.connect(id, "alice", 601)
	nextPresence(t, dave, func(p protocol.DocumentPeer) bool { return p.Peer == alicePeer })
	alice.stream.Close()
	nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Peer == alicePeer && p.Removed })
	// A resting cursor expires after the timeout.
	if err := carol.stream.SendPresence(encodePos(carol, 1), encodePos(carol, 1)); err != nil {
		t.Fatal(err)
	}
	carolPeer := nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Client == "carol" && !p.Removed }).Peer
	h.clock.Advance(docPresenceTTL)
	nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Peer == carolPeer && p.Removed })
	// Presence is never persisted.
	records, _ := h.store.LoadDocuments()
	if strings.Contains(string(records[0].Meta), "presence") || strings.Contains(string(records[0].Meta), carolPeer) {
		t.Fatalf("presence persisted: %s", records[0].Meta)
	}
}

func TestDocumentSlowEditorDoesNotBlockOthers(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "s.txt", "", "alice", "bob", "slow")
	alice, bob, slow := eds[0], eds[1], eds[2]
	// The slow editor sends one edit and then never reads again.
	slow.insert(0, "slow ")
	big := strings.Repeat("z", 16<<10)
	for round := 0; round < 6; round++ {
		h.clock.Advance(2 * time.Second) // refill the request rate
		var ops []string
		for i := 0; i < 150; i++ {
			op, _ := alice.insert(0, big)
			ops = append(ops, op, alice.delete(0, len(big)))
		}
		h.commit(id)
		for _, op := range ops {
			alice.next("ack", ackFor(op))
		}
		bop, _ := bob.insert(bob.text.Len(), "b")
		h.commit(id)
		bob.next("bob ack", ackFor(bop))
	}
	want := h.serverText(id)
	alice.syncTo(want)
	bob.syncTo(want)
	if !strings.Contains(want, "slow ") || strings.Count(want, "b") != 6 {
		t.Fatalf("server text %q", want)
	}
}

func TestDocumentResyncOfOneEditorLeavesOthers(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "r.txt", "base\n", "alice", "bob", "carol")
	alice, bob, carol := eds[0], eds[1], eds[2]
	aop, _ := alice.insert(0, "A")
	cop, _ := carol.insert(0, "\x00")
	bop, _ := bob.insert(bob.text.Len(), "B")
	h.commit(id)
	if rej := carol.next("rejected", rejectedFor(cop)); !rej.Rejected.Resync {
		t.Fatalf("%+v", rej.Rejected)
	}
	carol.ended("resync")
	alice.next("ack", ackFor(aop))
	bob.next("ack", ackFor(bop))
	aop, _ = alice.insert(1, "a")
	h.commit(id)
	alice.next("ack", ackFor(aop))
	want := h.serverText(id)
	if want != "Aabase\nB" {
		t.Fatalf("text %q", want)
	}
	alice.syncTo(want)
	bob.syncTo(want)
	carol2 := h.connect(id, "carol", 777)
	if carol2.text.ToString() != want {
		t.Fatalf("carol reconnects to %q", carol2.text.ToString())
	}
}

func TestDocumentRestartWithConcurrentPendingUpdates(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	id, eds := collabDoc(t, h, "k.txt", "x\n", "alice", "bob")
	alice, bob := eds[0], eds[1]
	aop, _ := alice.insert(0, "a1 ")
	bop, _ := bob.insert(bob.text.Len(), "b1")
	h.commit(id)
	alice.next("ack", ackFor(aop))
	bob.next("ack", ackFor(bop))
	durable := h.serverText(id)
	// Concurrent updates accepted but not yet committed when the server
	// "crashes" (its clock never reaches the batch deadline).
	_, a2 := alice.insert(0, "a2 ")
	_, b2 := bob.insert(bob.text.Len(), " b2")
	h.received(id)
	h2 := startDocHarness(t, root, dbPath)
	h2.waitStatus(id, "loaded", func(s protocol.DocumentStatus) bool { return s.ID == id })
	if got := h2.serverText(id); got != durable {
		t.Fatalf("restart text %q want the durable %q", got, durable)
	}
	// Both clients reconnect with their replicas (step 7) and resend.
	for _, c := range []struct {
		ed      *editor
		client  string
		replica uint64
		update  []byte
	}{{alice, "alice", 500, a2}, {bob, "bob", 501, b2}} {
		s, err := h2.client.OpenDocumentStream(context.Background(), id, c.client, c.replica)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		c.ed.stream, c.ed.h, c.ed.id = s, h2, id
		c.ed.next("state", func(ev protocol.DocumentEvent) bool {
			if ev.Type == protocol.DocumentEventState {
				return crdt.ApplyUpdateV1(c.ed.replica, ev.Update, nil) == nil
			}
			return false
		})
		op := c.ed.sendUpdate(c.update)
		h2.commit(id)
		c.ed.next("ack after restart", ackFor(op))
	}
	want := h2.serverText(id)
	if !strings.Contains(want, "a2 ") || !strings.Contains(want, " b2") {
		t.Fatalf("resent updates missing: %q", want)
	}
	alice.syncTo(want)
	bob.syncTo(want)
}

func TestDocumentDiskMergeWhileEditorsType(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "m.txt", "one\ntwo\nthree\nfour\nfive\n", "alice", "bob")
	alice, bob := eds[0], eds[1]
	for i := 0; i < 5; i++ {
		alice.insert(0, "A")
		bob.insert(5+i, "B") // within "two"
	}
	h.commit(id)
	h.write("m.txt", "one\ntwo\nthree\nfour\nFIVE\n", 0o644)
	h.clock.Advance(docPollInterval)
	// More typing arrives while the merge is applied.
	for i := 0; i < 3; i++ {
		alice.insert(0, "a")
		bob.insert(5, fmt.Sprint(i))
	}
	h.commit(id)
	h.saveAll(id)
	want := h.serverText(id)
	alice.syncTo(want)
	bob.syncTo(want)
	if !strings.Contains(want, "FIVE") || !strings.HasPrefix(want, "aaaAAAAA") || h.read("m.txt") != want {
		t.Fatalf("merged %q file %q", want, h.read("m.txt"))
	}
}

// saveAll advances the clock until everything durable is saved.
func (h *docHarness) saveAll(id string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.clock.Advance(docSaveMax)
		h.barrier(id)
		if st, _ := h.status(id); st.State == protocol.DocumentStateSaved && st.SavedRev == st.DurableRev {
			return
		}
		if time.Now().After(deadline) {
			st, _ := h.status(id)
			h.t.Fatalf("not saved: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// One editor exceeding its update rate is refused with RetryAfterMs while
// another editor is unaffected; resending after the delay succeeds.
func TestDocumentPerEditorRateLimit(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "l.txt", "", "alice", "bob")
	alice, bob := eds[0], eds[1]
	big := strings.Repeat("x", 900<<10)
	type sent struct {
		op     string
		update []byte
	}
	var all []sent
	for i := 0; i < 64; i++ {
		var u []byte
		if i%2 == 0 {
			u = alice.edit(func(txn *crdt.Transaction) { alice.text.Insert(txn, 0, big, nil) })
		} else {
			u = alice.edit(func(txn *crdt.Transaction) { alice.text.Delete(txn, 0, len(big)) })
		}
		all = append(all, sent{alice.sendUpdate(u), u})
		if i%4 == 3 {
			h.commit(id)
		}
	}
	bop, _ := bob.insert(0, "b")
	h.commit(id)
	bob.next("bob unaffected", ackFor(bop))
	rejected := map[string]bool{}
	last := all[len(all)-1].op
	for !rejected[last] {
		ev := alice.next("rejection", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventRejected })
		if ev.Rejected.Reason != protocol.DocumentRejectUnavailable || ev.Rejected.Resync || ev.Rejected.RetryAfterMs == 0 {
			t.Fatalf("%+v", ev.Rejected)
		}
		rejected[ev.Op] = true
	}
	refused := -1
	for i, x := range all {
		if rejected[x.op] && refused < 0 {
			refused = i
		}
	}
	h.clock.Advance(10 * time.Second)
	for _, x := range all[refused:] {
		if err := alice.stream.SendUpdate(x.op, 0, x.update); err != nil {
			t.Fatal(err)
		}
		h.sent[id]++
	}
	h.commit(id)
	for _, x := range all[refused:] {
		alice.next("ack "+x.op, ackFor(x.op))
	}
	if got := h.serverText(id); got != "b" {
		t.Fatalf("text %d bytes", len(got))
	}
}

// Presence needs an open document, stops when the client closes it, and a
// replica ID has one live stream.
func TestDocumentPresenceNeedsOpenerAndReplicaIsExclusive(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "o.txt", "hello\n", "alice", "bob")
	alice, bob := eds[0], eds[1]
	if _, err := h.client.OpenDocumentStream(context.Background(), id, "mallory", 0); err == nil || !strings.Contains(err.Error(), "not_editor") {
		t.Fatalf("non-opener stream: %v", err)
	}
	if _, err := h.client.OpenDocumentStream(context.Background(), id, "alice", 500); err == nil || !strings.Contains(err.Error(), "replica_conflict") {
		t.Fatalf("second stream on a live replica: %v", err)
	}
	if err := alice.stream.SendPresence(encodePos(alice, 0), encodePos(alice, 2)); err != nil {
		t.Fatal(err)
	}
	peer := nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Client == "alice" && !p.Removed }).Peer
	h.mustCommand(protocol.DocumentKindClose, id, "alice")
	nextPresence(t, bob, func(p protocol.DocumentPeer) bool { return p.Peer == peer && p.Removed })
	if err := alice.stream.SendPresence(encodePos(alice, 0), encodePos(alice, 3)); err != nil {
		t.Fatal(err)
	}
	alice.next("presence refused", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventRejected && ev.Rejected.Reason == protocol.DocumentRejectNotEditor && !ev.Rejected.Resync
	})
	h.clock.Advance(docPresenceEvery)
	bob.quiet(150*time.Millisecond, "closed client's presence", func(ev protocol.DocumentEvent) bool {
		for _, p := range ev.Presence {
			if p.Peer == peer && !p.Removed {
				return true
			}
		}
		return false
	})
}

// A cursor pointing at a new edit reaches peers only after that edit.
func TestDocumentPresenceFollowsItsUpdates(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "f.txt", "hello\n", "alice", "bob")
	alice, bob := eds[0], eds[1]
	alice.insert(5, " world")
	if err := alice.stream.SendPresence(encodePos(alice, 11), encodePos(alice, 11)); err != nil {
		t.Fatal(err)
	}
	h.received(id)
	h.barrier(id)
	bob.quiet(150*time.Millisecond, "presence before its update", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventPresence
	})
	h.clock.Advance(docBatchInterval)
	sawUpdate := false
	bob.next("presence", func(ev protocol.DocumentEvent) bool {
		if ev.Type == protocol.DocumentEventUpdate {
			sawUpdate = true
		}
		if ev.Type != protocol.DocumentEventPresence {
			return false
		}
		if !sawUpdate {
			t.Fatal("presence arrived before the update it refers to")
		}
		rp, _ := crdt.DecodeRelativePosition(ev.Presence[0].Head)
		abs, ok := crdt.ToAbsolutePosition(bob.replica, rp)
		if !ok || abs.Index != 11 {
			t.Fatalf("presence resolves to %+v %v", abs, ok)
		}
		return true
	})
}

// A full shared batch caused by editors' volume is refused as busy with a
// short retry, without charging the refused editor's rate limit.
func TestDocumentBusyBatchIsDistinctFromStorageFailure(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "b.txt", "", "alice", "bob")
	alice, bob := eds[0], eds[1]
	big := strings.Repeat("x", 900<<10)
	for i := 0; i < 16; i++ {
		alice.insert(0, big)
		alice.delete(0, len(big))
	}
	h.received(id)
	var bops []string
	var bupdates [][]byte
	for i := 0; i < 3; i++ {
		u := bob.edit(func(txn *crdt.Transaction) { bob.text.Insert(txn, 0, big, nil) })
		bops, bupdates = append(bops, bob.sendUpdate(u)), append(bupdates, u)
		u = bob.edit(func(txn *crdt.Transaction) { bob.text.Delete(txn, 0, len(big)) })
		bops, bupdates = append(bops, bob.sendUpdate(u)), append(bupdates, u)
	}
	h.received(id)
	rej := bob.next("busy", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventRejected })
	if rej.Rejected.Reason != protocol.DocumentRejectUnavailable || rej.Rejected.RetryAfterMs != 200 || !strings.Contains(rej.Rejected.Message, "busy") {
		t.Fatalf("%+v", rej.Rejected)
	}
	var tokens float64
	h.onActor(id, func(a *docActor) {
		for s := range a.streams {
			if s.clientID == "bob" {
				tokens = s.tokens
			}
		}
	})
	accepted := 0
	for i, op := range bops {
		if op == rej.Op {
			accepted = i
		}
	}
	var charged float64
	for _, u := range bupdates[:accepted] {
		charged += float64(len(u))
	}
	if tokens < docRateBurst-charged-1 {
		t.Fatalf("refused updates were charged: %.0f tokens left, %.0f charged", tokens, charged)
	}
	h.commit(id)
	for i, op := range bops[accepted:] {
		if err := bob.stream.SendUpdate(op, 0, bupdates[accepted+i]); err != nil {
			t.Fatal(err)
		}
		h.sent[id]++
	}
	h.commit(id)
	for _, op := range bops {
		bob.next("ack "+op, ackFor(op))
	}
}

// Many tiny updates hit the per-stream request rate.
func TestDocumentRequestRateLimit(t *testing.T) {
	h := newDocHarness(t)
	id, eds := collabDoc(t, h, "t.txt", "", "alice")
	alice := eds[0]
	var last string
	for i := 0; i < docRequestBurst+50; i++ {
		last, _ = alice.insert(0, "a")
	}
	h.received(id)
	h.commit(id)
	ev := alice.next("rate", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventRejected })
	if ev.Rejected.RetryAfterMs != 1000 || !strings.Contains(ev.Rejected.Message, "rate") {
		t.Fatalf("%+v", ev.Rejected)
	}
	_ = last
}
