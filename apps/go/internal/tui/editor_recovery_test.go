package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// switchDials makes dials fail (1: network, 2: not found) until mode is 0.
func switchDials(h *docHarness, mode *atomic.Int32) {
	h.m.docDial = func(ctx context.Context, id, clientID string, replica uint64) (docConn, error) {
		switch mode.Load() {
		case 1:
			return nil, errors.New("dial tcp: connection refused")
		case 2:
			return nil, client.ErrDocumentNotFound
		}
		return h.srv.dial(ctx, id, clientID, replica)
	}
}

// refuseHeld processes held updates with the first refused for resync.
func refuseHeld(h *docHarness) {
	h.srv.mu.Lock()
	h.srv.hold = false
	held := h.srv.held
	h.srv.held = nil
	h.srv.rejectOps = []string{protocol.DocumentRejectInvalid}
	for _, r := range held {
		if h.srv.connectedLocked(r.c) {
			h.srv.updateLocked(r.c, r.op, r.gen, r.u)
		}
	}
	h.srv.mu.Unlock()
}

func TestDocRecoveryWindowKeepsDraftGuardsAndKeys(t *testing.T) {
	h := newDocHarness(t, "one\n")
	var mode atomic.Int32
	switchDials(h, &mode)
	h.edit()
	h.srv.hold = true
	h.typeText("DRAFT ")
	mode.Store(1)
	refuseHeld(h)
	h.settle()
	time.Sleep(docRetryMin)
	h.settle()
	s := h.session()
	if !s.recovering() || len(s.pending) != 0 || h.m.docUnstored() == 0 {
		t.Fatalf("recovering %v pending %d unstored %d", s.recovering(), len(s.pending), h.m.docUnstored())
	}
	if _, text := h.m.docStatusLine(s); text != "Recovering your edits…" || !strings.Contains(h.screen(), "DRAFT one") {
		t.Fatalf("status %q\n%s", text, h.screen())
	}
	// Keys stay with the editor: w and Backspace do not reach Files.
	b := h.m.currentFilesView().buffer()
	h.do(tea.KeyPressMsg{Code: 'w', Text: "w"})
	h.key(tea.KeyBackspace, 0)
	if b.wrap || h.m.currentFilesView().active != 0 || h.m.docEdit == "" {
		t.Fatalf("keys fell through: wrap %v active %d editing %q", b.wrap, h.m.currentFilesView().active, h.m.docEdit)
	}
	// Detaching and closing ask first; the session is never released.
	if !h.m.docQuitGuard() {
		t.Fatal("quit guard ignored the draft")
	}
	h.m.menu = nil
	h.click("files-buf-close:0")
	if len(h.m.menu) == 0 || len(h.m.currentFilesView().buffers) != 1 || len(h.m.docs) != 1 {
		t.Fatal("closed a buffer holding a draft")
	}
	h.key(tea.KeyEnter, 0) // Keep open
	// Once the server is reachable the draft is applied and editing resumes.
	mode.Store(0)
	for range 20 {
		if h.srv.text() == "DRAFT one\n" {
			break
		}
		time.Sleep(docRetryMin)
		h.settle()
	}
	if got := h.srv.text(); got != "DRAFT one\n" || h.session().recovering() {
		t.Fatalf("after recovery: server %q recovering %v", got, h.session().recovering())
	}
	h.edit()
	h.typeText("!")
	if !strings.Contains(h.srv.text(), "!") {
		t.Fatalf("typing after recovery: %q", h.srv.text())
	}
}

func TestDocRecoveryGoneKeepsDraftAndStopsReopening(t *testing.T) {
	h := newDocHarness(t, "one\n")
	var mode atomic.Int32
	switchDials(h, &mode)
	h.edit()
	h.srv.hold = true
	h.typeText("DRAFT ")
	mode.Store(2)
	refuseHeld(h)
	for range 12 {
		h.settle()
		time.Sleep(docRetryMin / 2)
	}
	b := h.m.currentFilesView().buffer()
	if len(b.docLost) == 0 || !strings.Contains(b.docLost[0].text, "DRAFT one") {
		t.Fatalf("draft lost: %+v", b.docLost)
	}
	if b.doc != "" || b.docErr == "" || !strings.Contains(h.screen(), "Edits not stored") {
		t.Fatalf("reopen loop not stopped: doc %q err %q\n%s", b.doc, b.docErr, h.screen())
	}
	if h.m.docUnstored() == 0 {
		t.Fatal("guards ignore kept copies")
	}
}

func TestDocLostCopiesSurviveReopenAndAccumulate(t *testing.T) {
	h := newDocHarness(t, "one\n")
	var once atomic.Int32
	h.m.docDial = func(ctx context.Context, id, clientID string, replica uint64) (docConn, error) {
		if once.CompareAndSwap(1, 2) {
			return nil, client.ErrDocumentNotFound
		}
		return h.srv.dial(ctx, id, clientID, replica)
	}
	h.edit()
	h.srv.hold = true
	h.typeText("FIRST ")
	once.Store(1)
	h.srv.drop()
	for range 6 {
		h.settle()
		time.Sleep(docRetryMin / 2)
	}
	b := h.m.currentFilesView().buffer()
	s := h.session()
	if s == nil || s.rep == nil || len(b.docLost) != 1 {
		t.Fatalf("not reopened with the copy: session %v lost %d", s != nil, len(b.docLost))
	}
	if !strings.Contains(h.screen(), "Edits not stored") {
		t.Fatalf("copy hidden after reopen:\n%s", h.screen())
	}
	// A second refused text is added, not overwritten.
	h.edit()
	h.srv.hold = true
	h.typeText("SECOND ")
	h.srv.setStatus(func(st *protocol.DocumentStatus) { st.Editor, st.EditGen = "other", st.EditGen+1 })
	h.srv.release()
	for range 4 {
		h.settle()
		time.Sleep(docRetryMin / 2)
	}
	s = h.session()
	if len(s.lost)+len(b.docLost) != 2 || !strings.Contains(h.screen(), "(2 kept)") {
		t.Fatalf("copies: session %d buffer %d\n%s", len(s.lost), len(b.docLost), h.screen())
	}
	var copied string
	h.m.clipboardWrite = func(s string) error { copied = s; return nil }
	h.click("doc-lost-copy")
	if !strings.Contains(copied, "SECOND") {
		t.Fatalf("copied %q", copied)
	}
	h.click("doc-lost-dismiss")
	if l, _ := h.m.newestLost(s, b); !strings.Contains(l.text, "FIRST") {
		t.Fatalf("dismiss removed the wrong copy: %q", l.text)
	}
}

func TestDocQuitGuardReplacesOpenMenu(t *testing.T) {
	h := newDocHarness(t, "one\n")
	h.edit()
	h.srv.hold = true
	h.typeText("x")
	h.key(tea.KeyEscape, 0)
	h.key(tea.KeyF4, 0)
	if len(h.m.menu) == 0 {
		t.Fatal("no menu")
	}
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Mod: tea.ModCtrl}, {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := h.m.Update(k)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Fatalf("%s quit with unstored edits", k.String())
			}
		}
		if h.m.menuTitle != "Edits not stored yet" {
			t.Fatalf("%s: menu %q", k.String(), h.m.menuTitle)
		}
	}
}

func TestDocUndoKeysChainAndStaleIndex(t *testing.T) {
	// Type a, Backspace, Ctrl+Z, Ctrl+Z restores the original.
	h := newDocHarness(t, "xy\n")
	h.edit()
	h.key(tea.KeyRight, 0)
	h.typeText("a")
	h.key(tea.KeyBackspace, 0)
	h.key('z', tea.ModCtrl)
	if h.srv.text() != "xay\n" {
		t.Fatalf("undo delete: %q", h.srv.text())
	}
	h.key('z', tea.ModCtrl)
	if h.srv.text() != "xy\n" {
		t.Fatalf("undo insert after restore: %q", h.srv.text())
	}
	// Undo, then typing lands at the cursor (ygo's UndoManager misplaced it).
	h = newDocHarness(t, "abXcd\n")
	h.edit()
	h.key(tea.KeyRight, 0)
	h.typeText("xy")
	h.key(tea.KeyLeft, 0)
	h.key(tea.KeyRight, 0)
	h.typeText("b")
	h.key('z', tea.ModCtrl)
	h.typeText("a")
	if got := h.srv.text(); got != "axyabXcd\n" || h.session().rep.txt.String() != got {
		t.Fatalf("typing after undo: %q", got)
	}
	// Astral text: replace an emoji, undo, type, undo, select across it.
	h = newDocHarness(t, "ab😀cd\n")
	h.edit()
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyRight, tea.ModShift)
	h.do(tea.PasteMsg{Content: "😀"})
	h.key('z', tea.ModCtrl)
	h.key(tea.KeyHome, 0)
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyRight, 0)
	h.typeText("a")
	h.key('z', tea.ModCtrl)
	h.key(tea.KeyHome, 0)
	h.key(tea.KeyRight, 0)
	h.key(tea.KeyRight, tea.ModShift)
	h.key(tea.KeyRight, tea.ModShift)
	h.typeText("a")
	s := h.session()
	if got := h.srv.text(); got != "aacd\n" || s.rep.txt.String() != got || s.rep.text.ToString() != got || len(s.lost) != 0 {
		t.Fatalf("astral: server %q model %q lost %d", got, s.rep.txt.String(), len(s.lost))
	}
}

func TestDocReviewRefusesWhileBehind(t *testing.T) {
	h := newDocHarness(t, "base\n")
	h.srv.versions = protocol.DocumentVersions{ID: "doc-1", Base: []byte("base\n"), Disk: []byte("disk\n"), DiskState: "present", DiskID: "sha256:1"}
	h.srv.setStatus(func(st *protocol.DocumentStatus) {
		st.State, st.Versions, st.SavedRev = protocol.DocumentStatePausedConflict, true, -1
	})
	h.settle()
	h.click("doc-review")
	// The replica falls behind the durable revision it reviewed.
	h.session().rev--
	h.click("doc-review-keep")
	if len(h.m.menu) != 0 || !strings.Contains(h.m.notice.text, "still syncing") {
		t.Fatalf("resolve offered while behind: %q", h.m.notice.text)
	}
}

func TestEdTextCellsMatchPaintedWidth(t *testing.T) {
	for _, s := range []string{"❤️", "1️⃣", "🇩🇪🇫🇷", "👨‍👩‍👧‍👦", "﷽", "á̂", "ｗｉｄｅ", "กำ", "각"} {
		col := 0
		walkCells(s, func(c edCell) bool {
			if c.col != col || ansi.StringWidth(c.text) != c.w || c.w < 1 || c.w > 2 {
				t.Fatalf("%q: cell %+v at column %d paints %d cells", s, c, col, ansi.StringWidth(c.text))
			}
			col += c.w
			return true
		})
		if col != lineWidth(s) {
			t.Fatalf("%q: width %d vs %d", s, col, lineWidth(s))
		}
	}
}
