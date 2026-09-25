package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestDocTypingIsAcknowledgedAndSaved(t *testing.T) {
	h := newDocHarness(t, "hello\nworld\n")
	s := h.session()
	if !slices.Contains(h.srv.kinds(), protocol.DocumentKindOpen) || s.replicaID == 0 {
		t.Fatal("open not sent")
	}
	h.edit()
	if !slices.Contains(h.srv.kinds(), protocol.DocumentKindEdit) {
		t.Fatalf("edit role not requested: %v", h.srv.kinds())
	}
	h.key(tea.KeyEnd, 0)
	h.typeText(" there")
	if got := h.srv.text(); got != "hello there\nworld\n" {
		t.Fatalf("server text %q", got)
	}
	if len(s.pending) != 0 {
		t.Fatalf("unacked: %v", opIDs(s.pending))
	}
	if _, text := h.m.docStatusLine(s); text != "Saved" {
		t.Fatalf("status %q", text)
	}
	if !strings.Contains(h.screen(), "hello there") {
		t.Fatalf("screen:\n%s", h.screen())
	}
	// Esc leaves edit mode; printable keys no longer insert.
	h.key(tea.KeyEscape, 0)
	h.typeText("w")
	if h.m.docEdit != "" || h.srv.text() != "hello there\nworld\n" || !h.currentWrap() {
		t.Fatalf("after Esc: editing %q text %q", h.m.docEdit, h.srv.text())
	}
}

func (h *docHarness) currentWrap() bool { return h.m.currentFilesView().buffer().wrap }

func TestDocStatusCopyTruthTable(t *testing.T) {
	m := testModel()
	m.clientID = "me"
	base := protocol.DocumentStatus{State: protocol.DocumentStateSaved, DurableRev: 5, SavedRev: 5, Editor: "me"}
	rep, err := newDocReplica(newReplicaID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.close()
	for _, tc := range []struct {
		name string
		mod  func(*docSession)
		want string
	}{
		{"saved", func(*docSession) {}, "Saved"},
		{"replica behind", func(s *docSession) { s.rev = 4 }, "Unsaved"},
		{"pending durable", func(s *docSession) { s.status.State, s.status.SavedRev = protocol.DocumentStatePending, 3 }, "Unsaved · 2 pending"},
		{"saved but unacked", func(s *docSession) { s.pending = []docOp{{id: "a"}} }, "Storing…"},
		{"unacked offline", func(s *docSession) { s.pending = []docOp{{id: "a"}, {id: "b"}}; s.connected = false }, "Not yet stored · retrying · 2 edits"},
		{"unacked backpressure", func(s *docSession) { s.pending = []docOp{{id: "a"}}; s.resending = true }, "Not yet stored · retrying · 1 edit"},
		{"offline", func(s *docSession) { s.connected = false }, "Reconnecting…"},
		{"saving", func(s *docSession) { s.status.State = protocol.DocumentStateSaving }, "Saving…"},
		{"failed", func(s *docSession) { s.status.State, s.status.Error = protocol.DocumentStateFailed, "disk full" }, "Failed · disk full · retrying"},
		{"conflict", func(s *docSession) { s.status.State, s.status.SavedRev = protocol.DocumentStatePausedConflict, -1 }, "Paused · changed on disk"},
		{"conflict beats pending", func(s *docSession) {
			s.status.State = protocol.DocumentStatePausedConflict
			s.pending = []docOp{{id: "a"}}
		}, "Paused · changed on disk"},
		{"deleted", func(s *docSession) { s.status.State = protocol.DocumentStateDeleted }, "Deleted on disk · autosave paused"},
		{"read-only", func(s *docSession) {
			s.status.State, s.status.Reason = protocol.DocumentStateReadOnly, "The file has other hard links"
		}, "Read-only · The file has other hard links"},
		{"reconciling", func(s *docSession) { s.status.State = protocol.DocumentStateReconciling }, "Checking the file…"},
	} {
		s := &docSession{status: base, rev: 5, connected: true, rep: rep}
		tc.mod(s)
		if _, got := m.docStatusLine(s); got != tc.want {
			t.Errorf("%s: %q want %q", tc.name, got, tc.want)
		}
		if tc.name != "saved" {
			if _, got := m.docStatusLine(s); got == "Saved" {
				t.Errorf("%s shows Saved", tc.name)
			}
		}
	}
}

func TestDocReconnectResendsSameOpsWithoutDuplicates(t *testing.T) {
	h := newDocHarness(t, "abc\n")
	s := h.session()
	h.edit()
	h.srv.hold = true
	h.key(tea.KeyEnd, 0)
	h.typeText("XY")
	ids := opIDs(s.pending)
	if len(ids) != 2 {
		t.Fatalf("pending %v", ids)
	}
	if _, text := h.m.docStatusLine(s); text == "Saved" {
		t.Fatal("unacked edits shown as Saved")
	}
	// The connection drops before anything is processed; the reconnect
	// resends the same ops, which the server acknowledges once each.
	replica := s.replicaID
	h.srv.drop()
	h.settle()
	time.Sleep(docRetryMin)
	h.settle()
	h.srv.release()
	h.settle()
	if got := h.srv.text(); got != "abcXY\n" {
		t.Fatalf("server text %q", got)
	}
	if len(s.pending) != 0 || s.replicaID != replica {
		t.Fatalf("pending %v replica %d→%d", opIDs(s.pending), replica, s.replicaID)
	}
	for _, id := range ids {
		if h.srv.applied[id] == 0 {
			t.Fatalf("op %s never acked (%v)", id, h.srv.applied)
		}
	}
	// A duplicate delivery (the old stream's request plus the resend)
	// changes nothing.
	h.srv.hold = true
	h.typeText("Z")
	h.srv.mu.Lock()
	dup := h.srv.held[0]
	h.srv.mu.Unlock()
	h.srv.release()
	h.settle()
	h.srv.mu.Lock()
	h.srv.updateLocked(dup.c, dup.op, dup.gen, dup.u)
	h.srv.mu.Unlock()
	h.settle()
	if got := h.srv.text(); got != "abcXYZ\n" || h.session().rep.txt.String() != got {
		t.Fatalf("duplicate applied: %q", got)
	}
}

func TestDocResyncReappliesDraftUnderNewReplica(t *testing.T) {
	h := newDocHarness(t, "one\ntwo\n")
	s := h.session()
	h.edit()
	old := s.replicaID
	h.srv.rejectOps = []string{protocol.DocumentRejectInvalid}
	h.typeText("A")
	time.Sleep(docRetryMin)
	h.settle()
	s = h.session()
	if s.replicaID == old || s.rep == nil {
		t.Fatalf("replica not rebuilt with a new ID (%d)", s.replicaID)
	}
	if got := h.srv.text(); got != "Aone\ntwo\n" || len(s.lost) != 0 || len(s.pending) != 0 {
		t.Fatalf("draft not re-applied: server %q lost %+v pending %d", got, s.lost, len(s.pending))
	}
	// A draft whose re-application is refused again is kept as a copy.
	h.edit()
	h.srv.mu.Lock()
	h.srv.rejectOps = []string{protocol.DocumentRejectInvalid, "rejected"}
	h.srv.mu.Unlock()
	h.typeText("B")
	time.Sleep(docRetryMin)
	h.settle()
	s = h.session()
	if len(s.lost) != 1 || !strings.Contains(s.lost[0].text, "ABone") || h.srv.text() != "Aone\ntwo\n" {
		t.Fatalf("second refusal: lost %+v server %q", s.lost, h.srv.text())
	}
	if !strings.Contains(h.screen(), "Edits not stored") {
		t.Fatalf("no recovery row:\n%s", h.screen())
	}
	var copied string
	h.m.clipboardWrite = func(s string) error { copied = s; return nil }
	h.click("doc-lost-copy")
	h.settle()
	if !strings.Contains(copied, "ABone") {
		t.Fatalf("copy %q", copied)
	}
}

func TestDocTakenOverKeepsUnstoredTextAsCopy(t *testing.T) {
	h := newDocHarness(t, "text\n")
	h.edit()
	h.srv.hold = true
	h.typeText("mine ")
	// Another client takes over before the held edits are processed.
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor, st.EditGen = "other", st.EditGen+1 })
	h.settle()
	if h.m.docEdit != "" || !strings.Contains(h.m.notice.text, "took over") {
		t.Fatalf("edit mode kept: %q", h.m.notice.text)
	}
	h.srv.release()
	h.settle()
	time.Sleep(docRetryMin)
	h.settle()
	s := h.session()
	if len(s.lost) != 1 || !strings.HasPrefix(s.lost[0].text, "mine ") || h.srv.text() != "text\n" {
		t.Fatalf("lost %+v server %q", s.lost, h.srv.text())
	}
	screen := h.screen()
	for _, want := range []string{"Edits not stored", "Simultaneous editing unavailable", "Take over"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	// Typing is refused while another client edits.
	h.m.setFocus("files-text")
	h.key(tea.KeyEnter, 0)
	if h.m.docEdit != "" || !strings.Contains(h.m.notice.text, "Take over") {
		t.Fatalf("entered edit mode under another editor: %q", h.m.notice.text)
	}
	// Take over asks first, then acquires the role and enters edit mode.
	h.click("doc-take")
	if len(h.m.menu) == 0 || !strings.Contains(h.m.menuTitle, "Take over") {
		t.Fatal("no confirmation")
	}
	h.choose("Take over")
	if h.m.docEdit == "" || !slices.Contains(h.srv.kinds(), protocol.DocumentKindTakeEdit) {
		t.Fatalf("take over: editing %q commands %v", h.m.docEdit, h.srv.kinds())
	}
	h.typeText("!")
	if h.srv.text() != "text!\n" {
		t.Fatalf("after take over %q", h.srv.text())
	}
}

func TestDocUnavailableResendsInOrder(t *testing.T) {
	h := newDocHarness(t, "x\n")
	s := h.session()
	h.edit()
	h.srv.busyNext = true
	h.typeText("ab")
	time.Sleep(2 * docResendWait)
	h.settle()
	if got := h.srv.text(); got != "abx\n" || len(s.pending) != 0 || s.rep == nil {
		t.Fatalf("server %q pending %d", got, len(s.pending))
	}
}

func TestDocUndoRevertsOnlyOwnEdits(t *testing.T) {
	h := newDocHarness(t, "abc\n")
	s := h.session()
	h.edit()
	h.key(tea.KeyEnd, 0)
	h.typeText("XYZ")
	// A merged disk change arrives while the cursor is after XYZ.
	h.srv.serverEdit(t, doc.Edit{Start: 0, End: 0, Text: ">>"})
	h.settle()
	if got := s.rep.txt.String(); got != ">>abcXYZ\n" || s.ed.cur != (edPos{0, 8}) {
		t.Fatalf("remote: %q cursor %v", got, s.ed.cur)
	}
	h.key('z', tea.ModCtrl)
	if got := h.srv.text(); got != ">>abc\n" {
		t.Fatalf("undo: %q", got)
	}
	h.key('z', tea.ModCtrl)
	if !strings.Contains(h.m.notice.text, "Nothing to undo") || h.srv.text() != ">>abc\n" {
		t.Fatalf("undo went past own edits: %q %q", h.m.notice.text, h.srv.text())
	}
	h.key('y', tea.ModCtrl)
	if got := h.srv.text(); got != ">>abcXYZ\n" {
		t.Fatalf("redo: %q", got)
	}
	// Ctrl+Z outside edit mode is still Suspend, not Undo.
	h.key(tea.KeyEscape, 0)
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if cmd == nil || h.srv.text() != ">>abcXYZ\n" {
		t.Fatal("Ctrl+Z outside the editor changed the document")
	}
}

func TestDocKeysSelectionAndGraphemes(t *testing.T) {
	h := newDocHarness(t, "e\u0301😀 word two\nline2\n")
	s := h.session()
	h.edit()
	// Right moves by grapheme; Backspace removes the whole emoji.
	h.key(tea.KeyRight, 0)
	if s.ed.cur.col != len("e\u0301") {
		t.Fatalf("cursor %v", s.ed.cur)
	}
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyBackspace, 0)
	if got := h.srv.text(); got != "e\u0301 word two\nline2\n" {
		t.Fatalf("backspace %q", got)
	}
	// Word moves and shift selection, then type over the selection.
	h.key(tea.KeyRight, tea.ModCtrl)
	h.key(tea.KeyRight, tea.ModCtrl|tea.ModShift)
	if got := s.ed.selectedText(s.rep.txt); got != " two" {
		t.Fatalf("selection %q", got)
	}
	h.typeText("!")
	if got := h.srv.text(); got != "e\u0301 word!\nline2\n" {
		t.Fatalf("replace %q", got)
	}
	// Delete at line end joins lines; Enter splits; Tab inserts a tab.
	h.key(tea.KeyDelete, 0)
	h.key(tea.KeyEnter, 0)
	h.key(tea.KeyTab, 0)
	if got := h.srv.text(); got != "e\u0301 word!\n\tline2\n" {
		t.Fatalf("join/split %q", got)
	}
	// Shift+Up then copy and cut; bracketed paste inserts with LF endings.
	var copied string
	h.m.clipboardWrite = func(s string) error { copied = s; return nil }
	h.key(tea.KeyHome, tea.ModCtrl)
	h.key(tea.KeyEnd, tea.ModShift)
	h.key('c', tea.ModCtrl)
	h.settle()
	if copied != "e\u0301 word!" {
		t.Fatalf("copied %q", copied)
	}
	h.key('x', tea.ModCtrl)
	h.do(tea.PasteMsg{Content: "p1\r\np2\x00\rp3"})
	if got := h.srv.text(); got != "p1\np2\np3\n\tline2\n" {
		t.Fatalf("cut/paste %q", got)
	}
	// Select all and delete; F-keys still reach the shell while editing.
	h.key('a', tea.ModCtrl)
	h.key(tea.KeyBackspace, 0)
	if h.srv.text() != "" {
		t.Fatalf("select all %q", h.srv.text())
	}
	left := h.m.state.Layout.Left
	h.key(tea.KeyF2, 0)
	if h.m.state.Layout.Left == left {
		t.Fatal("F2 swallowed by the editor")
	}
}

func TestDocVerticalMovesKeepGoalColumnAndWrap(t *testing.T) {
	h := newDocHarness(t, "0123456789\nab\n0123456789\n")
	s := h.session()
	h.edit()
	h.key(tea.KeyEnd, 0)
	h.key(tea.KeyDown, 0)
	h.key(tea.KeyDown, 0)
	if s.ed.cur != (edPos{2, 10}) {
		t.Fatalf("goal column lost: %v", s.ed.cur)
	}
	// Wrapping keeps the text, and Down moves by visual row.
	b := h.m.currentFilesView().buffer()
	h.key(tea.KeyEscape, 0)
	h.do(tea.KeyPressMsg{Code: 'w', Text: "w"})
	if !b.wrap {
		t.Fatal("w did not wrap outside edit mode")
	}
	long := strings.Repeat("x", 3*b.docW)
	h.edit()
	h.key(tea.KeyHome, tea.ModCtrl)
	h.do(tea.PasteMsg{Content: long + "\n"})
	h.key(tea.KeyHome, tea.ModCtrl)
	h.key(tea.KeyDown, 0)
	if s.ed.cur.line != 0 || s.ed.cur.col != b.docW {
		t.Fatalf("wrapped down: %v (room %d)", s.ed.cur, b.docW)
	}
}

func TestDocClickPlacesCursorAndDragSelects(t *testing.T) {
	h := newDocHarness(t, "alpha beta\ngamma\n")
	s := h.session()
	f := h.m.measure()
	r := f.docText
	h.do(tea.MouseClickMsg{X: r.X + 6, Y: r.Y, Button: tea.MouseLeft})
	if h.m.docEdit == "" || s.ed.cur != (edPos{0, 6}) {
		t.Fatalf("click: editing %q cursor %v", h.m.docEdit, s.ed.cur)
	}
	h.do(tea.MouseMotionMsg{X: r.X + 3, Y: r.Y + 1, Button: tea.MouseLeft})
	h.do(tea.MouseReleaseMsg{X: r.X + 3, Y: r.Y + 1, Button: tea.MouseLeft})
	if got := s.ed.selectedText(s.rep.txt); got != "beta\ngam" {
		t.Fatalf("drag selection %q", got)
	}
}

func TestDocCloseWithUnstoredEditsAsks(t *testing.T) {
	h := newDocHarness(t, "keep\n")
	h.edit()
	h.srv.hold = true
	h.typeText("new ")
	v := h.m.currentFilesView()
	h.click("files-buf-close:0")
	if len(v.buffers) != 1 || len(h.m.menu) == 0 || !strings.Contains(h.menuNotes(), "has not stored") {
		t.Fatalf("closed without asking: %d buffers", len(v.buffers))
	}
	// Keep open (the default).
	h.key(tea.KeyEnter, 0)
	if len(v.buffers) != 1 || len(h.session().pending) == 0 {
		t.Fatal("Keep open closed the file")
	}
	// Once stored, closing sends document.close without asking.
	h.srv.release()
	h.settle()
	h.click("files-buf-close:0")
	if len(v.buffers) != 0 || len(h.m.docs) != 0 || !slices.Contains(h.srv.kinds(), protocol.DocumentKindClose) {
		t.Fatalf("close: buffers %d docs %d commands %v", len(v.buffers), len(h.m.docs), h.srv.kinds())
	}
	if h.srv.text() != "new keep\n" {
		t.Fatalf("text %q", h.srv.text())
	}
}

func TestDocQuitGuardWithUnstoredEdits(t *testing.T) {
	h := newDocHarness(t, "q\n")
	h.edit()
	h.srv.hold = true
	h.typeText("a")
	h.key(tea.KeyEscape, 0)
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd != nil || len(h.m.menu) == 0 || h.m.menuTitle != "Edits not stored yet" {
		t.Fatal("detached with unstored edits")
	}
}

func TestDocReadOnlyAndOpenFailures(t *testing.T) {
	h := newDocHarness(t, "x\n")
	h.api.reads["a.lock"] = protocol.FileRead{Path: "a.lock", Kind: "text", Token: "t", Encoding: "utf-8", Newline: "lf", Text: "linked\n"}
	h.cmd(h.m.openFilesBuffer("a.lock"))
	b := h.m.currentFilesView().buffer()
	if b.doc != "" || !strings.Contains(b.docRO, "hard links") || !strings.Contains(h.screen(), "Read-only · The file has other hard links") {
		t.Fatalf("read-only: %+v\n%s", b.docRO, h.screen())
	}
	h.m.setFocus("files-text")
	h.key(tea.KeyEnter, 0)
	if h.m.docEdit != "" {
		t.Fatal("editing a read-only file")
	}
}

func TestDocReviewResolvesWithReviewedVersions(t *testing.T) {
	h := newDocHarness(t, "base\n")
	s := h.session()
	h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte("base\n"), Document: []byte("base\n"), Disk: []byte("disk\r\n"), DiskState: "present", DiskID: "sha256:abc"}
	h.srv.setStatus(func(st *protocol.DocumentStatus) {
		st.State, st.Versions, st.SavedRev = protocol.DocumentStatePausedConflict, true, -1
	})
	h.settle()
	if !strings.Contains(h.screen(), "Paused · changed on disk") {
		t.Fatalf("no paused row:\n%s", h.screen())
	}
	h.click("doc-review")
	h.settle()
	rv := h.m.docReview
	if rv == nil || rv.versions == nil {
		t.Fatalf("review not loaded: %+v %q", rv, h.m.notice.text)
	}
	screen := h.screen()
	for _, want := range []string{"Review changes", "Changes", "Keep mine", "Use disk", "Discard", "-  disk", "+  base"} {
		if !strings.Contains(strings.Join(strings.Fields(screen), " "), want) && !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	h.key('3', 0)
	if rv.tab != 2 || !strings.Contains(h.screen(), "disk") {
		t.Fatal("Disk tab")
	}
	// Keep mine asks, naming the consequence; Cancel is the default.
	h.click("doc-review-keep")
	if len(h.m.menu) == 0 || !strings.Contains(h.menuNotes(), "Writes your document over the file on disk") {
		t.Fatal("no confirmation")
	}
	h.key(tea.KeyEnter, 0)
	if slices.Contains(h.srv.kinds(), protocol.DocumentKindResolve) {
		t.Fatal("Cancel resolved")
	}
	// A stale reply refreshes the versions instead of closing.
	h.srv.staleNext = true
	h.click("doc-review-keep")
	h.choose("Keep mine")
	var sent protocol.Command
	for _, c := range h.srv.commands {
		if c.Kind == protocol.DocumentKindResolve {
			sent = c
		}
	}
	if sent.Text != protocol.DocumentResolveKeepDocument || sent.DocumentDisk != "sha256:abc" || sent.Revision != s.status.DurableRev || sent.ClientID != h.m.clientID {
		t.Fatalf("resolve %+v", sent)
	}
	if h.m.docReview == nil || !strings.Contains(h.m.notice.text, "changed again") {
		t.Fatalf("stale: %q", h.m.notice.text)
	}
	h.click("doc-review-disk")
	h.choose("Use disk")
	if h.m.docReview != nil || !strings.Contains(h.m.notice.text, "from disk") {
		t.Fatalf("use disk: %q", h.m.notice.text)
	}
}

func TestDocRemoteUpdateKeepsCursorAndView(t *testing.T) {
	h := newDocHarness(t, strings.Repeat("line\n", 80))
	s := h.session()
	h.edit()
	for range 50 {
		h.key(tea.KeyDown, 0)
	}
	b := h.m.currentFilesView().buffer()
	top := b.scroll
	h.srv.serverEdit(t, doc.Edit{Start: 0, End: 0, Text: "new\nlines\n"})
	h.settle()
	if s.ed.cur != (edPos{52, 0}) || b.scroll != top+2 {
		t.Fatalf("cursor %v scroll %d→%d", s.ed.cur, top, b.scroll)
	}
}

func TestDocRenderSanitizesControls(t *testing.T) {
	h := newDocHarness(t, "a\x1b]0;pwned\x07b\x1b[2Jc\u202eZ\n")
	h.edit()
	rows := h.m.compose(true).rows
	joined := strings.Join(rows, "\n")
	for _, bad := range []string{"\x1b]0;", "\x07", "\x1b[2J", "\u202e"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("raw %q painted", bad)
		}
	}
	plain := ansi.Strip(joined)
	if !strings.Contains(plain, "a␛]0;pwned␇b␛[2Jc�Z") {
		t.Fatalf("controls not shown as symbols:\n%s", plain)
	}
}

func TestDocTextStaysOneStopAndKeepsTreeKeys(t *testing.T) {
	h := newDocHarness(t, "x\n")
	h.m.setFocus("files-text")
	h.key(tea.KeyBackspace, 0) // outside edit mode: back to the tree
	if h.m.currentFilesView().active != -1 {
		t.Fatal("Backspace outside edit mode did not return to the tree")
	}
}

func TestDocQuarantinedEditsOfferConfirmedDismiss(t *testing.T) {
	h := newDocHarness(t, "x\n")
	h.m.snapshot.Documents = append(h.m.snapshot.Documents, protocol.DocumentStatus{ID: "doc-old", Checkout: h.m.thread().Checkout,
		Path: "README.md", State: protocol.DocumentStateFailed, Reason: "Stored document damaged", Quarantined: true})
	if !strings.Contains(h.screen(), "Earlier edits to this file could not be loaded") {
		t.Fatalf("no quarantine row:\n%s", h.screen())
	}
	h.click("doc-dismiss")
	if len(h.m.menu) == 0 || !strings.Contains(h.menuNotes(), "cannot be undone") {
		t.Fatal("dismiss without confirmation")
	}
	h.key(tea.KeyEnter, 0) // Cancel is the default
	if slices.Contains(h.srv.kinds(), protocol.DocumentKindDismiss) {
		t.Fatal("Cancel dismissed")
	}
	h.click("doc-dismiss")
	h.choose("Delete retained edits")
	var sent protocol.Command
	for _, c := range h.srv.commands {
		if c.Kind == protocol.DocumentKindDismiss {
			sent = c
		}
	}
	if sent.TargetID != "doc-old" || sent.Text != protocol.DocumentDismissConfirm || sent.ClientID != h.m.clientID {
		t.Fatalf("dismiss %+v", sent)
	}
}
