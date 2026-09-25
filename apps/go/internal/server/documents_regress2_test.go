//go:build unix

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// Second verification round (C1–C3 and hardening).

func rewritePauses(h *docHarness, ids ...string) []int {
	out := make([]int, len(ids))
	for i, id := range ids {
		h.onActor(id, func(a *docActor) { out[i] = a.rewritePaused })
	}
	return out
}

// C1: a cancelled or failed begin releases only its own pauses; concurrent
// overlapping rewrites keep each other's.
func TestDocumentRewriteReleasesOnlyItsOwnPauses(t *testing.T) {
	h := newDocHarness(t)
	h.write("x.txt", "x\n", 0o644)
	h.write("y.txt", "y\n", 0o644)
	idx := h.open("x.txt", "alice")
	idy := h.open("y.txt", "alice")
	first, err := h.e.beginDocumentRewrite(context.Background(), h.root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		if i%2 == 0 {
			cancel()
		}
		release, _ := h.e.beginDocumentRewrite(ctx, h.root)
		cancel()
		release()
		release() // idempotent
		if p := rewritePauses(h, idx, idy); p[0] != 1 || p[1] != 1 {
			t.Fatalf("iteration %d: pauses %v; the first rewrite lost its pause", i, p)
		}
	}
	// Concurrent overlapping rewrites.
	done := make(chan func(), 8)
	for i := 0; i < 8; i++ {
		go func() {
			release, _ := h.e.beginDocumentRewrite(context.Background(), h.root)
			done <- release
		}()
	}
	var releases []func()
	for i := 0; i < 8; i++ {
		releases = append(releases, <-done)
	}
	if p := rewritePauses(h, idx, idy); p[0] != 9 || p[1] != 9 {
		t.Fatalf("concurrent pauses %v", p)
	}
	for _, r := range releases {
		r()
	}
	first()
	if p := rewritePauses(h, idx, idy); p[0] != 0 || p[1] != 0 {
		t.Fatalf("pauses after every release %v", p)
	}
}

// C2: a save that failed before its rename does not leave a pending write
// behind, so a later clean external change merges instead of pausing.
func TestDocumentFailedSaveBeforeRenameMergesLaterChange(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	h := newDocHarness(t)
	h.write("d/g.txt", "one\ntwo\nthree\n", 0o644)
	id := h.open("d/g.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 22)
	ed.gen = 1
	op, _ := ed.insert(0, "ours ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	dir := filepath.Join(h.root, "d")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateFailed)
	var pending int
	h.onActor(id, func(a *docActor) { pending = len(a.meta.Pending) })
	if pending != 0 {
		t.Fatalf("pending writes after a failure before the rename: %d", pending)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("one\ntwo\nthree changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.onActor(id, func(a *docActor) { a.retryAt = a.clock.Now().Add(time.Hour) })
	h.clock.Advance(docPollInterval)
	ed.next("merge", func(ev protocol.DocumentEvent) bool {
		return ev.Type == protocol.DocumentEventUpdate && ev.Origin == "server"
	})
	if st, _ := h.status(id); st.State == protocol.DocumentStatePausedConflict {
		t.Fatalf("false pause: %+v", st)
	}
	if ed.text.ToString() != "ours one\ntwo\nthree changed\n" {
		t.Fatalf("merged %q", ed.text.ToString())
	}
}

// C3: a rename that succeeded but reported an error afterwards leaves the
// file as our own write; the next save overwrites it without a conflict.
func TestDocumentRenamedButFailedSaveIsOwnWrite(t *testing.T) {
	h := newDocHarness(t)
	h.write("a.txt", "a\n", 0o644)
	id := h.open("a.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 21)
	ed.gen = 1
	op, _ := ed.insert(2, "X")
	h.commit(id)
	ed.next("ack", ackFor(op))
	failed := false
	h.e.docs.afterRename = func(string) error {
		if !failed {
			failed = true
			return errors.New("emulated directory fsync failure")
		}
		return nil
	}
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateFailed)
	if h.read("a.txt") != "a\nX" {
		t.Fatalf("file %q", h.read("a.txt"))
	}
	op, _ = ed.insert(3, "Y\n")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docRetryMax)
	st := h.waitState(id, protocol.DocumentStateSaved)
	if h.read("a.txt") != "a\nXY\n" || st.SavedRev != st.DurableRev {
		t.Fatalf("after retry: %+v %q", st, h.read("a.txt"))
	}
	// And a poll in between (instead of a save) adopts it as well.
	h2 := newDocHarness(t)
	h2.write("b.txt", "b\n", 0o644)
	id2 := h2.open("b.txt", "alice")
	h2.mustCommand(protocol.DocumentKindEdit, id2, "alice")
	e2 := h2.connect(id2, "alice", 22)
	e2.gen = 1
	op, _ = e2.insert(0, "Z")
	h2.commit(id2)
	e2.next("ack", ackFor(op))
	once := false
	h2.e.docs.afterRename = func(string) error {
		if !once {
			once = true
			return errors.New("emulated fstat failure")
		}
		return nil
	}
	h2.clock.Advance(docSaveIdle)
	h2.waitState(id2, protocol.DocumentStateFailed)
	h2.onActor(id2, func(a *docActor) { a.retryAt = a.clock.Now().Add(time.Hour) })
	h2.clock.Advance(docPollInterval)
	h2.waitState(id2, protocol.DocumentStateSaved)
}

// Replica bindings are released when streams close, so reconnecting with new
// replicas never exhausts the per-document bound.
func TestDocumentReplicaBindingsReleasedOnStreamClose(t *testing.T) {
	h := newDocHarness(t)
	h.write("r.txt", "r\n", 0o644)
	id := h.open("r.txt", "alice")
	for i := 0; i < docMaxReplicas+20; i++ {
		s, err := h.client.OpenDocumentStream(context.Background(), id, "alice", uint64(1000+i))
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		<-s.Events()
		s.Close()
		h.waitStatus(id, "released", func(protocol.DocumentStatus) bool {
			n := 0
			h.onActor(id, func(a *docActor) { n = len(a.meta.Replicas) + len(a.streams) })
			return n == 0
		})
	}
}

// After unavailable, later updates are refused without a resync until the
// client resends from the refused op; the replica stays valid.
func TestDocumentUnavailableHoldsWithoutResync(t *testing.T) {
	h := newDocHarness(t)
	h.write("u.txt", "", 0o644)
	id := h.open("u.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 30)
	ed.gen = 1
	h.store.fail.Store(true)
	big := strings.Repeat("x", 900<<10)
	type sent struct {
		op     string
		update []byte
	}
	var all []sent
	for i := 0; i < 40; i++ {
		var u []byte
		if i%2 == 0 {
			u = ed.edit(func(txn *crdt.Transaction) { ed.text.Insert(txn, 0, big, nil) })
		} else {
			u = ed.edit(func(txn *crdt.Transaction) { ed.text.Delete(txn, 0, len(big)) })
		}
		all = append(all, sent{ed.sendUpdate(u), u})
	}
	h.received(id)
	rejected := map[string]bool{}
	last := all[len(all)-1].op
	for !rejected[last] {
		ev := ed.next("rejection", func(ev protocol.DocumentEvent) bool { return ev.Type == protocol.DocumentEventRejected })
		if ev.Rejected.Reason != protocol.DocumentRejectUnavailable || ev.Rejected.Resync {
			t.Fatalf("op %s: %+v", ev.Op, ev.Rejected)
		}
		rejected[ev.Op] = true
	}
	refused := -1
	for i, x := range all {
		if rejected[x.op] && refused < 0 {
			refused = i
		}
		if refused >= 0 && !rejected[x.op] {
			t.Fatalf("op %s after the refused one was applied", x.op)
		}
	}
	if refused <= 0 {
		t.Fatalf("refused index %d", refused)
	}
	h.store.fail.Store(false)
	h.clock.Advance(docRetryMax)
	h.barrier(id)
	// Resend in order from the refused op; all are acknowledged.
	for _, x := range all[refused:] {
		if err := ed.stream.SendUpdate(x.op, ed.gen, x.update); err != nil {
			t.Fatal(err)
		}
		h.sent[id]++
	}
	h.commit(id)
	for _, x := range all[refused:] {
		ed.next("ack "+x.op, ackFor(x.op))
	}
	var text string
	h.onActor(id, func(a *docActor) { text = a.d.Text() })
	if text != ed.text.ToString() || text != "" {
		t.Fatalf("server %d bytes, replica %d bytes", len(text), len(ed.text.ToString()))
	}
}

// Quarantined documents are flagged and can be dismissed only with the
// explicit confirmation.
func TestDocumentQuarantineDismiss(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("q.txt", "q\n", 0o644)
	id := h.open("q.txt", "alice")
	if err := h.store.CommitDocument(id, docCommitWithRev(t, h, id, 7)); err != nil {
		t.Fatal(err)
	}
	h2 := startDocHarness(t, root, dbPath)
	st, _ := h2.status(id)
	if !st.Quarantined || !strings.Contains(st.Reason, "retained in quarantine") {
		t.Fatalf("status %+v", st)
	}
	if _, err := h2.command(protocol.Command{Kind: protocol.DocumentKindDismiss, TargetID: id, ClientID: "alice"}); err == nil {
		t.Fatal("dismiss without confirmation accepted")
	}
	if _, err := h2.command(protocol.Command{Kind: protocol.DocumentKindDismiss, TargetID: id, ClientID: "alice", Text: protocol.DocumentDismissConfirm}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h2.status(id); ok {
		t.Fatal("dismissed document still listed")
	}
	if q, _ := h2.store.LoadQuarantined(); len(q) != 0 {
		t.Fatalf("quarantine rows left: %v", q)
	}
}

// keep_document with the parent directory gone pauses (deleted) instead of
// retrying forever; an accepted keep survives a restart.
func TestDocumentKeepDocumentParentGoneAndRestart(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("sub/k.txt", "k\n", 0o644)
	id := h.open("sub/k.txt", "alice")
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(docPollInterval)
	st := h.waitState(id, protocol.DocumentStateDeleted)
	// keep_document is accepted (the reviewed version is "absent"), but the
	// save finds no parent directory and pauses again.
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, protocol.DocumentStateDeleted)

	// A keep accepted just before a restart is honoured afterwards.
	h.write("sub/k.txt", "theirs\n", 0o644)
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err == nil || !strings.Contains(err.Error(), "stale_document") {
		t.Fatalf("keep against a changed file: %v", err)
	}
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	h.e.docs.beforeRename = func(string) { <-block } // the first server never renames
	if _, err := h.resolve(id, protocol.DocumentResolveKeepDocument, st.DurableRev); err != nil {
		t.Fatal(err)
	}
	h2 := startDocHarness(t, root, dbPath)
	h2.waitState(id, protocol.DocumentStateSaved)
	if h2.read("sub/k.txt") != "k\n" {
		t.Fatalf("file %q", h2.read("sub/k.txt"))
	}
}

// The encoded state bound is enforced per update.
func TestDocumentStateBoundPerUpdate(t *testing.T) {
	h := newDocHarness(t)
	h.e.docs.maxState = 64 << 10
	h.write("s.txt", "", 0o644)
	id := h.open("s.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 40)
	ed.gen = 1
	op, _ := ed.insert(0, strings.Repeat("y", 70<<10))
	rej := ed.next("rejected", rejectedFor(op))
	if rej.Rejected.Reason != "too_large" || !rej.Rejected.Resync {
		t.Fatalf("%+v", rej.Rejected)
	}
}
