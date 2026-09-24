package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Regression tests from the independent verification of the attachment and
// graphics integration.

func TestRemoveRefusedDuringSendCapture(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt")
	m.viewState().Attachments = append([]protocol.Attachment{{Kind: "artifact", Name: "img.png", ArtifactID: "img-id", MediaType: "image/png"}}, m.viewState().Attachments...)
	cmd := m.activate(action{Kind: "send"})
	if m.sendCapture == nil {
		t.Fatal("no capture")
	}
	for _, c := range []func() tea.Cmd{
		func() tea.Cmd {
			return m.activate(action{Kind: "attachment-remove", Index: 0, Value: "artifact:img-id"})
		},
		func() tea.Cmd { m.setFocus(chipPrefix + "0"); return m.key(tea.KeyPressMsg{Code: tea.KeyDelete}) },
	} {
		pump(t, m, c())
		if len(m.viewState().Attachments) != 2 || len(api.deleted) != 0 || !strings.Contains(m.notice.text, "captured for Send") {
			t.Fatalf("removal during capture: %v deleted %v notice %q", m.viewState().Attachments, api.deleted, m.notice.text)
		}
	}
	m.Update(cmd())
	if m.busy == nil || !slices.ContainsFunc(m.busy.Attachments, func(a protocol.Attachment) bool { return a.ArtifactID == "img-id" }) {
		t.Fatal("sent command lost the image")
	}
}

func TestRemoveSkipsDeleteForReferencedArtifacts(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	shared := protocol.Attachment{Kind: "artifact", Name: "img.png", ArtifactID: "shared-id", MediaType: "image/png"}
	m.viewState().Attachments = []protocol.Attachment{shared}
	// Pending/uncertain command (Retry) still references it.
	m.busy = &protocol.Command{Kind: "prompt.send", Attachments: []protocol.Attachment{shared}}
	pump(t, m, m.activate(action{Kind: "attachment-remove", Index: 0, Value: "artifact:shared-id"}))
	if len(m.viewState().Attachments) != 0 || len(api.deleted) != 0 {
		t.Fatalf("removed %v deleted %v", m.viewState().Attachments, api.deleted)
	}
	// Another draft still references it.
	m.busy = nil
	m.viewState().Attachments = []protocol.Attachment{shared}
	m.state.DraftThreads["other"] = &threadView{Attachments: []protocol.Attachment{shared}}
	pump(t, m, m.activate(action{Kind: "attachment-remove", Index: 0, Value: "artifact:shared-id"}))
	if len(api.deleted) != 0 {
		t.Fatal("deleted an artifact another draft uses")
	}
	delete(m.state.DraftThreads, "other")
	m.viewState().Attachments = []protocol.Attachment{shared}
	pump(t, m, m.activate(action{Kind: "attachment-remove", Index: 0, Value: "artifact:shared-id"}))
	if len(api.deleted) != 1 {
		t.Fatal("unreferenced staged artifact not deleted")
	}
}

func TestAcceptedSendRemovesExactlySentAttachments(t *testing.T) {
	m, _ := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt")
	cmd := m.activate(action{Kind: "send"})
	m.viewState().Attachments = append(m.viewState().Attachments, protocol.Attachment{Kind: "artifact", Name: "late.png", ArtifactID: "late-id", MediaType: "image/png"})
	m.Update(cmd())
	if m.busy == nil {
		t.Fatal("not dispatched: " + m.status)
	}
	c := *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "accepted"}, local: action{Kind: "send"}})
	v := m.viewState()
	if v.Draft != "" || m.prompt.Value() != "" || len(v.Attachments) != 1 || v.Attachments[0].ArtifactID != "late-id" {
		t.Fatalf("draft %q attachments %+v", v.Draft, v.Attachments)
	}
}

func TestPendingCommandAtCaptureEndShowsSendFailure(t *testing.T) {
	m, _ := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt")
	// Refused up front while a command is pending.
	m.busy = &protocol.Command{Kind: "thread.interrupt", ID: "other"}
	m.activate(action{Kind: "send"})
	if m.sendCapture != nil || len(m.menu) == 0 {
		t.Fatal("Send not refused while a command was pending")
	}
	m.menu, m.busy = nil, nil
	cmd := m.activate(action{Kind: "send"})
	m.busy = &protocol.Command{Kind: "thread.interrupt", ID: "other"}
	m.Update(cmd())
	if m.busy.ID != "other" || m.sendCapture != nil || len(m.menu) == 0 || !strings.Contains(m.status, "Not sent") {
		t.Fatalf("status %q menu %d", m.status, len(m.menu))
	}
	if v := m.viewState(); v.Draft != "please review" || v.Attachments[0].ArtifactID != "one.txt-id" {
		t.Fatalf("draft not kept for retry: %+v", v)
	}
}

func TestSendCaptureCancelledWithEsc(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt")
	cmd := m.activate(action{Kind: "send"})
	m.setFocus("prompt")
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.sendCapture != nil || !strings.Contains(m.notice.text, "Send cancelled") {
		t.Fatal("Esc did not cancel the capture")
	}
	// The abandoned capture's finished uploads are deleted, nothing is sent.
	_ = cmd
	_, next := m.Update(sendCaptureMsg{id: m.captureSeq, uploads: []captureUpload{{index: 0, info: protocol.ArtifactInfo{ID: "orphan"}}}})
	pump(t, m, next)
	if m.busy != nil || m.viewState().Draft != "please review" || !slices.Contains(api.deleted, "orphan") {
		t.Fatalf("busy %v deleted %v", m.busy, api.deleted)
	}
}

func TestReadLocalFileNeverBlocksOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readLocalFile(t.Context(), p, 1<<20); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		f, _ := os.OpenFile(p, os.O_WRONLY, 0)
		if f != nil {
			f.Close()
		}
		t.Fatal("readLocalFile blocked on a FIFO")
	}
	// A cancelled context fails the read instead of waiting.
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("x"), 0o600)
	if data, err := readLocalFile(t.Context(), f, 10); err != nil || string(data) != "x" {
		t.Fatal(data, err)
	}
}

func TestCollapsedStripIgnoresHiddenChipDelete(t *testing.T) {
	m, _ := stripModel(t, 40, stripAtts...)
	m.setFocus(chipPrefix + "1")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.focus = chipPrefix + "1" // even if focus lingered on a hidden chip
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(m.viewState().Attachments) != len(stripAtts) {
		t.Fatal("Backspace removed an attachment whose chip is hidden")
	}
}

func TestRemovalIdentityDistinguishesSameNames(t *testing.T) {
	m, _ := stripModel(t, 40,
		protocol.Attachment{Kind: "artifact", Name: "clipboard.png", ArtifactID: "a1", MediaType: "image/png"},
		protocol.Attachment{Kind: "artifact", Name: "clipboard.png", ArtifactID: "a2", MediaType: "image/png"})
	// A guard for a1 at index 1 (after a reorder) removes nothing.
	m.activate(action{Kind: "attachment-remove", Index: 1, Value: "artifact:a1"})
	if len(m.viewState().Attachments) != 2 {
		t.Fatal("removed a different attachment with the same name")
	}
	m.activate(action{Kind: "attachment-remove", Index: 1, Value: "artifact:a2"})
	if atts := m.viewState().Attachments; len(atts) != 1 || atts[0].ArtifactID != "a1" {
		t.Fatalf("attachments %v", atts)
	}
}

func TestPastedImageNamesAreUnique(t *testing.T) {
	m, _ := intakeModel(t, &fakeClipboard{types: []string{"image/png"}, data: map[string][]byte{"image/png": pngBytes(t, 2, 2)}})
	var names []string
	for range 2 {
		msg := runPaste(t, m).(clipboardImageMsg)
		names = append(names, msg.name)
	}
	if names[0] == names[1] {
		t.Fatalf("duplicate names %v", names)
	}
}

func TestNewThreadIntakeNeverAttachesToLaterDraft(t *testing.T) {
	m := navigationModel()
	api := &fakeArtifacts{}
	m.artifacts = api
	m.beginThreadDraft("alpha")
	owner := m.currentOwner()
	m.intakes = map[uint64]draftOwner{9: owner}
	m.activate(action{Kind: "setting-model"})
	m.Update(tea.PasteMsg{Content: "Initial prompt"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "thread.start" {
		t.Fatal("no thread.start")
	}
	pump(t, m, m.acceptClipboardImage(clipboardImageMsg{id: 9, owner: owner, name: "x.png", info: protocol.ArtifactInfo{ID: "late-upload"}}))
	if v := m.state.DraftThreads["alpha"]; v != nil && len(v.Attachments) != 0 {
		t.Fatalf("upload attached to the consumed draft: %v", v.Attachments)
	}
	if !slices.Contains(api.deleted, "late-upload") || !strings.Contains(m.notice.text, "no longer exists") {
		t.Fatalf("deleted %v notice %q", api.deleted, m.notice.text)
	}
}

func TestViewerImageIgnoredAfterGraphicsDropped(t *testing.T) {
	m, _ := imageActivityModel(t, true)
	cmd := m.activate(action{Kind: "attachment-view", Value: "activity", ID: "pi", Index: 0})
	_, next := m.Update(drainBatch(cmd))
	m.updateColorProfile(colorprofile.ANSI)
	if out := pump(t, m, next); out.raw.Len() != 0 {
		t.Fatalf("kitty output after graphics were disabled: %q", out.raw.String())
	}
}

func TestViewerResizeCoalescesPreparation(t *testing.T) {
	m, _ := imageActivityModel(t, true)
	pump(t, m, m.activate(action{Kind: "attachment-view", Value: "activity", ID: "pi", Index: 0}))
	first := m.syncViewerImage() // no-op: same box
	if first != nil {
		t.Fatal("re-prepared for an unchanged box")
	}
	var cmds []tea.Cmd
	for _, w := range []int{130, 125, 110, 100} {
		m.width = w
		if c := m.syncViewerImage(); c != nil {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) != 1 {
		t.Fatalf("%d preparations in flight, want 1", len(cmds))
	}
	pump(t, m, cmds[0])
	if m.viewer.imgPreparing || m.viewer.imgBox != m.viewerImageBox() {
		t.Fatalf("latest box not prepared: %+v vs %+v", m.viewer.imgBox, m.viewerImageBox())
	}
}

func TestSplitKittyReplyNeverReachesPrompt(t *testing.T) {
	m := testModel()
	m.graphics.state = graphicsQuerying
	m.prompt.SetValue("keep my draft")
	now := time.Now()
	if _, ok := m.filterTerminalReply(uv.UnknownEvent("\x1b_Gi=3"), now).(terminalReplyStarted); !ok {
		t.Fatal("split APC reply not reassembled")
	}
	for _, char := range "1;OK" {
		if got := m.filterTerminalReply(tea.KeyPressMsg{Code: char, Text: string(char)}, now.Add(50*time.Millisecond)); got != nil {
			t.Fatal("fragment escaped to input:", got)
		}
	}
	got := m.filterTerminalReply(tea.KeyPressMsg{Code: '\\', Mod: tea.ModAlt}, now.Add(60*time.Millisecond))
	m.Update(got)
	if !m.graphics.Supported() || m.prompt.Value() != "keep my draft" {
		t.Fatalf("reply %#v supported %v", got, m.graphics.Supported())
	}
	// Without a pending probe, the same bytes are not captured.
	m = testModel()
	if _, ok := m.filterTerminalReply(uv.UnknownEvent("\x1b_Gi=3"), now).(terminalReplyStarted); ok {
		t.Fatal("unsolicited APC reassembled")
	}
}

func TestExitDeletesInFlightTransmitsAndStopsQueue(t *testing.T) {
	r := newGraphicsRegistry(0)
	enc := r.Show(graphicsPrepared{Key: "a", PNG: []byte("a"), Cols: 1, Rows: 1})() // encoded, not yet written
	seq := r.Close()
	if !strings.Contains(seq, "a=d,d=I,i=16") {
		t.Fatalf("in-flight id not deleted at exit: %q", seq)
	}
	if next, ok := r.Update(enc); !ok || next != nil {
		t.Fatal("transmit written after exit")
	}
	if r.Show(graphicsPrepared{Key: "b", PNG: []byte("b"), Cols: 1, Rows: 1}) != nil {
		t.Fatal("new transmit after exit")
	}
	// Released ids are still deleted again at exit.
	r = newGraphicsRegistry(0)
	pump(t, &Model{images: r}, r.Show(graphicsPrepared{Key: "a", PNG: []byte("a"), Cols: 1, Rows: 1}))
	r.Release("a")
	if !strings.Contains(r.Close(), "i="+strconv.Itoa(graphicsFirstID)) {
		t.Fatal("released id missing from exit deletes")
	}
}

func TestThumbnailCacheAvoidsRefetch(t *testing.T) {
	m, api := graphicsModel(t)
	api.fetch = map[string][]byte{"art-a": pngBytes(t, 40, 30)}
	api.fetchType = map[string]string{"art-a": "image/png"}
	m.viewState().Attachments = []protocol.Attachment{stripAtts[1]}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	pump(t, m, cmd)
	if len(api.fetched) != 1 {
		t.Fatalf("fetched %v", api.fetched)
	}
	saved := m.viewState().Attachments
	m.viewState().Attachments = nil
	_, cmd = m.Update(nil) // leave: kitty image released
	pump(t, m, cmd)
	m.viewState().Attachments = saved
	_, cmd = m.Update(nil)
	pump(t, m, cmd)
	if _, ok := m.imgs().Ready("thumb:art-a"); !ok || len(api.fetched) != 1 {
		t.Fatalf("returning refetched (%v) or not shown", api.fetched)
	}
	// Bounded: entries beyond the limit are evicted.
	var c thumbCache
	for i := range thumbCacheEntries + 4 {
		c.put(strconv.Itoa(i), graphicsPrepared{PNG: []byte("x")})
	}
	if len(c.entries) != thumbCacheEntries {
		t.Fatalf("cache holds %d", len(c.entries))
	}
}
