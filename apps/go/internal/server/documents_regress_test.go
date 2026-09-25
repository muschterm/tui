//go:build unix

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// Regressions from the slice B verification (D1–D10).

func pausedConflict(t *testing.T, h *docHarness, rel string) (string, *editor, protocol.DocumentStatus) {
	t.Helper()
	h.write(rel, "one\ntwo\n", 0o644)
	id := h.open(rel, "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 12)
	ed.gen = 1
	op, _ := ed.insert(0, "ours ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.write(rel, "theirs one\ntwo\n", 0o644)
	h.clock.Advance(docPollInterval)
	return id, ed, h.waitState(id, protocol.DocumentStatePausedConflict)
}

// D1: keep_document never overwrites a disk version the client did not
// review, and versions stay until the resolved text is on disk.
func TestDocumentKeepDocumentRefusesUnreviewedDisk(t *testing.T) {
	h := newDocHarness(t)
	id, _, st := pausedConflict(t, h, "c.txt")
	reviewed, err := h.client.DocumentVersions(context.Background(), id)
	if err != nil || !strings.HasPrefix(reviewed.DiskID, "sha256:") {
		t.Fatalf("versions %+v %v", reviewed, err)
	}
	h.write("c.txt", "theirs one\ntwo\nIMPORTANT external line\n", 0o644)
	_, err = h.command(protocol.Command{Kind: protocol.DocumentKindResolve, TargetID: id, ClientID: "alice", Text: protocol.DocumentResolveKeepDocument, Revision: st.DurableRev, DocumentDisk: reviewed.DiskID})
	if err == nil || !strings.Contains(err.Error(), "stale_document") {
		t.Fatalf("resolve against an unreviewed disk: %v", err)
	}
	if h.read("c.txt") != "theirs one\ntwo\nIMPORTANT external line\n" {
		t.Fatal("unreviewed disk content overwritten")
	}
	fresh, err := h.client.DocumentVersions(context.Background(), id)
	if err != nil || fresh.DiskID == reviewed.DiskID || !strings.Contains(string(fresh.Disk), "IMPORTANT") {
		t.Fatalf("versions not refreshed: %+v %v", fresh, err)
	}
	if st, _ := h.status(id); st.State != protocol.DocumentStatePausedConflict {
		t.Fatalf("left the pause: %+v", st)
	}
	// Missing identity is refused too.
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindResolve, TargetID: id, ClientID: "alice", Text: protocol.DocumentResolveKeepDocument, Revision: st.DurableRev}); err == nil {
		t.Fatal("resolve without a reviewed disk version accepted")
	}
	// The save of a keep resolution is refused if the file changes again;
	// the document pauses again and keeps its versions.
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h.e.docs.beforeRename = func(string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindResolve, TargetID: id, ClientID: "alice", Text: protocol.DocumentResolveKeepDocument, Revision: st.DurableRev, DocumentDisk: fresh.DiskID}); err != nil {
		t.Fatal(err)
	}
	<-entered
	h.write("c.txt", "yet another\n", 0o644)
	close(release)
	st = h.waitState(id, protocol.DocumentStatePausedConflict)
	if !st.Versions || h.read("c.txt") != "yet another\n" {
		t.Fatalf("after a raced keep: %+v %q", st, h.read("c.txt"))
	}
	if v, err := h.client.DocumentVersions(context.Background(), id); err != nil || string(v.Disk) != "yet another\n" {
		t.Fatalf("versions %+v %v", v, err)
	}
}

// D3: resolve commits accepted updates before checking the revision.
func TestDocumentResolveCommitsPendingUpdatesFirst(t *testing.T) {
	h := newDocHarness(t)
	id, ed, st := pausedConflict(t, h, "c.txt")
	op, _ := ed.insert(0, "LATE ")
	h.received(id)
	if _, err := h.resolve(id, protocol.DocumentResolveUseDisk, st.DurableRev); err == nil || !strings.Contains(err.Error(), "stale_document") {
		t.Fatalf("use_disk over an unreviewed edit: %v", err)
	}
	ev := ed.next("ack", ackFor(op))
	var text string
	h.onActor(id, func(a *docActor) { text = a.d.Text() })
	if !strings.HasPrefix(text, "LATE ") || ev.Rev != st.DurableRev+1 {
		t.Fatalf("late edit lost: %q rev %d", text, ev.Rev)
	}
	// Reviewing again succeeds.
	if _, err := h.resolve(id, protocol.DocumentResolveUseDisk, ev.Rev); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, protocol.DocumentStateSaved)
}

// D2: a save whose record fails must never let a later commit store the new
// hash without the new baseline; restart then merges against the right base.
func TestDocumentSaveRecordFailureKeepsBaselineConsistent(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("r.txt", "a\nb\n", 0o644)
	id := h.open("r.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 15)
	ed.gen = 1
	op, _ := ed.insert(2, "X\n")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.e.docs.beforeRename = func(string) { h.store.fail.Store(true) }
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateSaved)
	h.onActor(id, func(a *docActor) { a.e.docs.beforeRename = nil })
	h.store.fail.Store(false)
	if h.read("r.txt") != "a\nX\nb\n" {
		t.Fatalf("file %q", h.read("r.txt"))
	}
	op = ed.delete(2, 2)
	h.commit(id)
	ed.next("ack", ackFor(op))
	op, _ = ed.insert(ed.text.Len(), "c\n")
	h.commit(id)
	ed.next("ack", ackFor(op))
	// External change while the server is down.
	h.write("r.txt", "Z\na\nX\nb\n", 0o644)
	h2 := startDocHarness(t, root, dbPath)
	h2.waitStatus(id, "reconciled", func(s protocol.DocumentStatus) bool {
		return s.State != protocol.DocumentStateReconciling && s.State != ""
	})
	var text string
	h2.onActor(id, func(a *docActor) { text = a.d.Text() })
	if text != "Z\na\nb\nc\n" {
		t.Fatalf("restart merged against a stale base: %q", text)
	}
}

// D2: a crash between the rename and its record: the file holding the
// pending write is adopted; a file diverged from it pauses.
func TestDocumentCrashAfterRenameAdoptsPendingWrite(t *testing.T) {
	for _, external := range []bool{false, true} {
		root, _ := filepath.EvalSymlinks(t.TempDir())
		dbPath := filepath.Join(t.TempDir(), "state.sqlite")
		h := startDocHarness(t, root, dbPath)
		h.write("p.txt", "a\n", 0o644)
		id := h.open("p.txt", "alice")
		h.mustCommand(protocol.DocumentKindEdit, id, "alice")
		ed := h.connect(id, "alice", 16)
		ed.gen = 1
		op, _ := ed.insert(0, "b\n")
		h.commit(id)
		ed.next("ack", ackFor(op))
		// The record after the rename fails, and nothing later is stored.
		h.e.docs.beforeRename = func(string) { h.store.fail.Store(true) }
		h.clock.Advance(docSaveIdle)
		h.waitState(id, protocol.DocumentStateSaved)
		if external {
			h.write("p.txt", "b\na\nexternal\n", 0o644)
		}
		h2 := startDocHarness(t, root, dbPath)
		if !external {
			st := h2.waitState(id, protocol.DocumentStateSaved)
			if st.SavedRev != st.DurableRev || h2.read("p.txt") != "b\na\n" {
				t.Fatalf("adoption: %+v %q", st, h2.read("p.txt"))
			}
			continue
		}
		st := h2.waitState(id, protocol.DocumentStatePausedConflict)
		if h2.read("p.txt") != "b\na\nexternal\n" || !st.Versions {
			t.Fatalf("ambiguous base: %+v", st)
		}
		h.store.fail.Store(false)
	}
}

// D5: a caller that stops waiting during begin still pairs with its end.
func TestDocumentRewriteCancelledBeginPairsWithEnd(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	end, err := h.e.beginDocumentRewrite(ctx, h.root)
	if err == nil {
		t.Fatal("begin reported success while its save was still running")
	}
	end()
	close(release)
	h.waitState(id, protocol.DocumentStateSaved)
	var paused int
	h.onActor(id, func(a *docActor) { paused = a.rewritePaused })
	if paused != 0 {
		t.Fatalf("rewritePaused stuck at %d", paused)
	}
	op, _ = ed.insert(0, "more ")
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveMax)
	h.waitStatus(id, "saved again", func(s protocol.DocumentStatus) bool {
		return s.State == protocol.DocumentStateSaved && s.SavedRev == s.DurableRev && s.DurableRev == 2
	})
}

// D6 and D9: begin refuses when edits are not durable; joins and resync
// states never carry non-durable updates.
func TestDocumentNonDurableEditsBlockRewriteAndState(t *testing.T) {
	h := newDocHarness(t)
	h.write("s.txt", "base\n", 0o644)
	id := h.open("s.txt", "alice")
	h.open("s.txt", "bob")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 13)
	ed.gen = 1
	h.store.fail.Store(true)
	op, _ := ed.insert(0, "new ")
	h.commit(id)
	release, err := h.e.beginDocumentRewrite(context.Background(), h.root)
	if err == nil || !strings.Contains(err.Error(), "document_unsaved") {
		t.Fatalf("begin with non-durable edits: %v", err)
	}
	release()
	if _, err := h.client.OpenDocumentStream(context.Background(), id, "bob", 0); err == nil {
		t.Fatal("joined while updates were not durable")
	}
	// A resync rejection: the stream stops applying updates but stays open
	// until its earlier op is answered (durably), then ends.
	bad, _ := ed.insert(0, "\x00")
	ed.next("rejected", rejectedFor(bad))
	later, _ := ed.insert(0, "later ")
	if rej := ed.next("later rejected", rejectedFor(later)); rej.Rejected.Reason != protocol.DocumentRejectResyncPending {
		t.Fatalf("later op: %+v", rej.Rejected)
	}
	ed.quiet(150*time.Millisecond, "ack while storage fails", ackFor(op))
	h.store.fail.Store(false)
	h.clock.Advance(docRetryMax)
	if ev := ed.next("ack", ackFor(op)); ev.Rev != 1 {
		t.Fatalf("ack rev %d", ev.Rev)
	}
	ed.ended("resync")
	fresh := h.connect(id, "alice", 14)
	if fresh.text.ToString() != "new base\n" {
		t.Fatalf("new replica text %q", fresh.text.ToString())
	}
}

// D7: a U+FEFF typed at the start of a BOM-less file round-trips.
func TestDocumentLeadingFEFFIsContent(t *testing.T) {
	h := newDocHarness(t)
	h.write("b.txt", "hello\n", 0o644)
	id := h.open("b.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 12)
	ed.gen = 1
	op, _ := ed.insert(0, string(rune(0xFEFF)))
	h.commit(id)
	ed.next("ack", ackFor(op))
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateSaved)
	now := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(h.root, "b.txt"), now, now); err != nil {
		t.Fatal(err)
	}
	h.onActor(id, func(a *docActor) { a.tokenAt = time.Time{} })
	h.clock.Advance(docPollInterval)
	h.waitStatus(id, "token refreshed", func(protocol.DocumentStatus) bool {
		var job bool
		h.onActor(id, func(a *docActor) { job = a.job != nil })
		return !job
	})
	h.barrier(id)
	if st, _ := h.status(id); st.State != protocol.DocumentStateSaved {
		t.Fatalf("touch after a leading U+FEFF: %+v", st)
	}
}

// D8: discard needs a paused or saved document and reports storage failure.
func TestDocumentDiscardRules(t *testing.T) {
	h := newDocHarness(t)
	h.write("d.txt", "d\n", 0o644)
	id := h.open("d.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 20)
	ed.gen = 1
	op, _ := ed.insert(0, "x")
	h.commit(id)
	ed.next("ack", ackFor(op))
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindResolve, TargetID: id, Text: protocol.DocumentResolveDiscard, Revision: 1}); err == nil {
		t.Fatal("discard without a ClientID accepted")
	}
	if _, err := h.resolve(id, protocol.DocumentResolveDiscard, 1); err == nil || !strings.Contains(err.Error(), "document_unsaved") {
		t.Fatalf("discard of unsaved edits: %v", err)
	}
	h.clock.Advance(docSaveIdle)
	h.waitState(id, protocol.DocumentStateSaved)
	failing := &deleteFailingStore{flakyStore: h.store}
	h.onActor(id, func(a *docActor) { a.store = failing })
	if _, err := h.resolve(id, protocol.DocumentResolveDiscard, 1); err == nil {
		t.Fatal("discard reported success although storage failed")
	}
	if _, ok := h.status(id); !ok {
		t.Fatal("document dropped although its storage remains")
	}
	h.onActor(id, func(a *docActor) { a.store = h.store })
	if _, err := h.resolve(id, protocol.DocumentResolveDiscard, 1); err != nil {
		t.Fatal(err)
	}
}

type deleteFailingStore struct{ *flakyStore }

func (deleteFailingStore) DeleteDocument(string) error { return os.ErrPermission }

// D10: a damaged stored document is quarantined; the server starts and the
// file opens again as a new document.
func TestDocumentDamagedStorageIsQuarantined(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("q.txt", "q\n", 0o644)
	id := h.open("q.txt", "alice")
	h.mustCommand(protocol.DocumentKindEdit, id, "alice")
	ed := h.connect(id, "alice", 21)
	ed.gen = 1
	op, _ := ed.insert(0, "x")
	h.commit(id)
	ed.next("ack", ackFor(op))
	// Corrupt the log: a gap after revision 1.
	if err := h.store.CommitDocument(id, docCommitWithRev(t, h, id, 5)); err != nil {
		t.Fatal(err)
	}
	h2 := startDocHarness(t, root, dbPath)
	st, ok := h2.status(id)
	if !ok || st.State != protocol.DocumentStateFailed || !strings.Contains(st.Reason, "damaged") {
		t.Fatalf("quarantined status %+v %v", st, ok)
	}
	if h2.e.docActor(id) != nil {
		t.Fatal("damaged document loaded")
	}
	if fresh := h2.open("q.txt", "alice"); fresh == id {
		t.Fatal("file not reopenable as a new document")
	}
}

// Cheap hardening: owner-read-only files, orphaned temporary files and
// replica pruning.
func TestDocumentReadOnlyModeTempCleanupAndReplicaPruning(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	h := startDocHarness(t, root, dbPath)
	h.write("ro.txt", "r\n", 0o444)
	if _, err := h.command(protocol.Command{Kind: protocol.DocumentKindOpen, ProjectID: "project-docs", Path: "ro.txt", ClientID: "alice"}); err == nil || !strings.Contains(err.Error(), "document_read_only") {
		t.Fatalf("0444 file: %v", err)
	}
	h.write("t.txt", "t\n", 0o644)
	id := h.open("t.txt", "alice")
	h.open("t.txt", "bob")
	s, err := h.client.OpenDocumentStream(context.Background(), id, "alice", 33)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	h.waitStatus(id, "stream gone", func(protocol.DocumentStatus) bool {
		n := 0
		h.onActor(id, func(a *docActor) { n = len(a.streams) })
		return n == 0
	})
	h.mustCommand(protocol.DocumentKindClose, id, "alice")
	if s, err := h.client.OpenDocumentStream(context.Background(), id, "bob", 33); err != nil {
		t.Fatalf("replica of a closed client still bound: %v", err)
	} else {
		s.Close()
	}
	// Orphaned temporary files from before the start are removed; others stay.
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{".t.txt.tui-0123456789ab.tmp", ".tui-0123456789ab.tmp", ".t.txt.tui-zz.tmp", "keep.tmp"} {
		p := filepath.Join(root, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	h2 := startDocHarness(t, root, dbPath)
	h2.waitStatus(id, "loaded", func(s protocol.DocumentStatus) bool { return s.ID == id })
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, e1 := os.Stat(filepath.Join(root, ".t.txt.tui-0123456789ab.tmp"))
		_, e2 := os.Stat(filepath.Join(root, ".tui-0123456789ab.tmp"))
		if os.IsNotExist(e1) && os.IsNotExist(e2) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("orphaned temporary files not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, n := range []string{".t.txt.tui-zz.tmp", "keep.tmp"} {
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			t.Fatalf("%s removed: %v", n, err)
		}
	}
}

func docCommitWithRev(t *testing.T, h *docHarness, id string, rev int64) storage.DocumentCommit {
	t.Helper()
	var meta []byte
	h.onActor(id, func(a *docActor) { meta = a.encodeMeta() })
	return storage.DocumentCommit{Meta: meta, Updates: []storage.DocumentUpdate{{Rev: rev, Data: []byte{0}}}}
}
