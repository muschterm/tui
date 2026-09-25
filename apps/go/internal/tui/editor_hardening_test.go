package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestDocNonTextPasteIsRefusedWithoutPanic(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CLIENT", "")
	h := newDocHarness(t, "hello\n")
	h.edit()
	h.m.clipboardRead = func() (string, error) { return "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", nil }
	h.do(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if h.srv.text() != "hello\n" || !strings.Contains(h.m.notice.text, "non-text data") {
		t.Fatalf("clipboard: server %q notice %q", h.srv.text(), h.m.notice.text)
	}
	h.do(tea.PasteMsg{Content: "x\xffy"})
	if h.srv.text() != "hello\n" || !strings.Contains(h.m.notice.text, "non-text data") || h.m.docEdit == "" {
		t.Fatalf("bracketed: server %q notice %q", h.srv.text(), h.m.notice.text)
	}
	// The replica refuses invalid text itself, and a library panic becomes
	// an error.
	s := h.session()
	if err := s.rep.edit(0, 0, "a\xff", false); err == nil {
		t.Fatal("replica accepted invalid UTF-8")
	}
	err := func() (err error) {
		defer s.rep.guard(&err)
		panic("invalid UTF-8")
	}()
	if err == nil {
		t.Fatal("panic not converted")
	}
}

func TestDocLocalFailureKeepsConcurrentEdits(t *testing.T) {
	h := newDocHarness(t, "alpha\nbeta\ngamma\ndelta\n")
	h.edit()
	h.typeText("MINE ")
	s := h.session()
	// A disk merge arrives on the server while the local replica fails.
	h.srv.serverEdit(t, doc.Edit{Start: len("MINE alpha\n"), End: len("MINE alpha\nbeta"), Text: "BETA-FROM-DISK"})
	base := s.rep.txt.String()
	intended := strings.Replace(base, "delta", "DELTA", 1)
	h.cmd(h.m.docLocalFailure(s, base, intended))
	time.Sleep(docRetryMin)
	h.settle()
	if got := h.srv.text(); got != "MINE alpha\nBETA-FROM-DISK\ngamma\nDELTA\n" {
		t.Fatalf("recovery: %q", got)
	}
	// An intended change on the line the disk changed is kept as a copy
	// instead of overwriting it.
	s = h.session()
	h.srv.serverEdit(t, doc.Edit{Start: 0, End: len("MINE alpha"), Text: "DISK alpha"})
	base = s.rep.txt.String()
	intended = strings.Replace(base, "MINE alpha", "MY alpha", 1)
	h.cmd(h.m.docLocalFailure(s, base, intended))
	time.Sleep(docRetryMin)
	h.settle()
	s = h.session()
	if got := h.srv.text(); !strings.HasPrefix(got, "DISK alpha\n") || len(s.lost) != 1 || !strings.Contains(s.lost[0].text, "MY alpha") {
		t.Fatalf("conflict: server %q lost %+v", got, s.lost)
	}
	// Repeated failures recover each time without looping.
	for i := range 3 {
		h.edit()
		h.typeText("x")
		s = h.session()
		txt := s.rep.txt.String()
		h.cmd(h.m.docLocalFailure(s, txt, txt+"!"))
		time.Sleep(docRetryMin)
		h.settle()
		if s = h.session(); s.rep == nil || s.recovering() || !strings.HasSuffix(h.srv.text(), "!") {
			t.Fatalf("failure %d: recovering %v server %q", i, s.recovering(), h.srv.text())
		}
	}
}

func closeFromServer(h *docHarness, reason string) {
	h.srv.mu.Lock()
	for _, c := range h.srv.conns {
		h.srv.send(c, protocol.DocumentEvent{Type: protocol.DocumentEventClosed, Reason: reason})
	}
	h.srv.mu.Unlock()
	h.settle()
}

// removeActiveThread switches away from the active thread and deletes it
// from the snapshot, as a thread.delete from another client would.
func removeActiveThread(h *docHarness) {
	active := h.m.state.Active
	var keep []protocol.Thread
	for _, th := range h.m.snapshot.Threads {
		if th.ID != active {
			keep = append(keep, th)
		}
	}
	h.m.selectThread(keep[0].ID)
	h.m.snapshot.Threads = keep
	h.settle()
}

func TestDocKeptCopiesOutliveViewsAndSessions(t *testing.T) {
	// A buffer's kept copy survives its view being pruned.
	h := newDocHarness(t, "one\n")
	h.edit()
	h.srv.hold = true
	h.typeText("UNSTORED ")
	closeFromServer(h, "The document was discarded")
	if n := len(h.m.currentFilesView().buffer().docLost); n != 1 {
		t.Fatalf("setup: %d copies", n)
	}
	removeActiveThread(h)
	h.m.openSurface("files", "")
	h.settle()
	h.cmd(h.m.nextFilesRefresh())
	if len(h.m.docOrphans) != 1 || h.m.docUnstored() != 1 || !strings.Contains(h.screen(), "1 kept unsaved text") {
		t.Fatalf("orphans %d unstored %d\n%s", len(h.m.docOrphans), h.m.docUnstored(), h.screen())
	}
	var copied string
	h.m.clipboardWrite = func(s string) error { copied = s; return nil }
	h.click("doc-kept-orphans")
	h.choose("Copy · README.md · 1 line · The document was discarded")
	if !strings.Contains(copied, "UNSTORED") {
		t.Fatalf("copy %q", copied)
	}

	// A session whose view is gone keeps its unstored text when it ends.
	h = newDocHarness(t, "one\n")
	h.edit()
	h.srv.hold = true
	h.typeText("PENDING ")
	removeActiveThread(h)
	h.m.openSurface("files", "")
	h.settle()
	h.cmd(h.m.nextFilesRefresh())
	closeFromServer(h, "The document was discarded")
	if len(h.m.docOrphans) != 1 || !strings.Contains(h.m.docOrphans[0].text, "PENDING") {
		t.Fatalf("orphaned session: %+v docs %d", h.m.docOrphans, len(h.m.docs))
	}
}

func TestDocKeptCopiesEvictionAndFileViewRows(t *testing.T) {
	h := newDocHarness(t, "one\n")
	b := h.m.currentFilesView().buffer()
	for i := range docMaxLost {
		h.m.keepLost(&b.docLost, docLost{text: string(rune('A' + i)), reason: "r"})
	}
	h.cmd(h.m.keepLost(&b.docLost, docLost{text: "new", reason: "r"}))
	if len(b.docLost) != docMaxLost || !strings.Contains(h.m.notice.text, "Oldest kept copy dropped") {
		t.Fatalf("eviction: %d %q", len(b.docLost), h.m.notice.text)
	}
	// The file view (no document) shows count, Copy, All… and Dismiss and
	// keeps Retry reachable.
	h.api.reads["plain.txt"] = protocol.FileRead{Path: "plain.txt", Kind: "text", Token: "t", Encoding: "utf-8", Newline: "lf", Text: "x\n"}
	h.cmd(h.m.openFilesBuffer("plain.txt"))
	pb := h.m.currentFilesView().buffer()
	pb.doc, pb.docOpen, pb.docErr = "", nil, "the document keeps closing on the server"
	pb.docLost = []docLost{{text: "one", reason: "r1"}, {text: "two", reason: "r2"}}
	screen := h.screen()
	for _, want := range []string{"Edits not stored · r2 (2 kept)", "Copy", "All…", "Dismiss", "Editing unavailable", "Retry"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	h.click("doc-kept")
	if len(h.m.menu) == 0 || h.m.menuTitle != "Kept texts" {
		t.Fatal("no list of copies")
	}
	h.choose("Dismiss · 1 line · r1")
	if len(pb.docLost) != 1 || pb.docLost[0].text != "two" {
		t.Fatalf("dismiss: %+v", pb.docLost)
	}
	// Closing asks and offers copying all kept texts.
	h.m.menu = nil
	h.click("files-buf-close:1")
	found := false
	for _, item := range h.m.menu {
		found = found || item.Label == "Copy all kept texts"
	}
	if !found {
		t.Fatal("close guard lacks Copy all")
	}
}

func TestDocDeleteConfirmationsMentionUnstoredText(t *testing.T) {
	h := newDocHarness(t, "one\n")
	h.edit()
	h.srv.hold = true
	h.typeText("x")
	h.key(tea.KeyEscape, 0)
	h.m.activate(action{Kind: "thread-delete", ID: h.m.state.Active})
	if !strings.Contains(h.menuNotes(), "unsaved document change") {
		t.Fatalf("thread delete: %q", h.menuNotes())
	}
}

func TestDocResolveConfirmRechecksPending(t *testing.T) {
	h := newDocHarness(t, "base\n")
	h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte("base\n"), Disk: []byte("disk\n"), DiskState: "present", DiskID: "sha256:1"}
	h.srv.setStatus(func(st *protocol.DocumentStatus) {
		st.State, st.Versions, st.SavedRev = protocol.DocumentStatePausedConflict, true, -1
	})
	h.settle()
	h.click("doc-review")
	h.click("doc-review-keep")
	// Edits become pending after the confirmation opened.
	h.session().pending = []docOp{{id: "late"}}
	h.choose("Keep mine")
	for _, c := range h.srv.commands {
		if c.Kind == protocol.DocumentKindResolve {
			t.Fatal("resolved with pending edits")
		}
	}
}

func TestReplicaUndoOfFragmentedPasteIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large document")
	}
	line := strings.Repeat("abcdefghij", 7) + "\n"
	d := serverDoc(t, strings.Repeat(line, 2000))
	r := replicaFrom(t, d)
	if err := r.edit(0, 0, strings.Repeat("😀", 50000), false); err != nil {
		t.Fatal(err)
	}
	sendAll(t, d, r)
	for i := range 2000 {
		u, err := d.Replace([]doc.Edit{{Start: i*4*20 + i, End: i*4*20 + i, Text: "Q"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.applyRemote(u, nil); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	_, err := r.undoStep(false)
	took := time.Since(start)
	sendAll(t, d, r)
	if took > 2*time.Second {
		t.Fatalf("undo blocked for %v", took)
	}
	if err != nil && !errors.Is(err, errUndoTooLarge) {
		t.Fatal(err)
	}
	if strings.Count(r.txt.String(), "Q") != 2000 {
		t.Fatal("remote text touched")
	}
	t.Logf("undo of a paste split 2000 times: %v (%v)", took, err)
}
