package tui

import (
	"errors"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// TestNoticeSeverityUnavailable covers an explicit blocked-action call site:
// pasting is refused because the prompt isn't focused, and the notice text
// and m.status are unaffected by the severity change.
func TestNoticeSeverityUnavailable(t *testing.T) {
	m := testModel()
	m.configureInputs()
	m.setFocus("navigation") // Not "prompt", so clipboardPasteBlocked() fires.
	statusBefore := m.status
	m.pasteClipboard()
	if m.notice.severity != noticeUnavailable {
		t.Fatalf("severity = %v, want noticeUnavailable", m.notice.severity)
	}
	if m.status != statusBefore {
		t.Fatalf("m.status changed: %q -> %q", statusBefore, m.status)
	}
}

// TestNoticeSeverityError covers a failed operation: a clipboard read that
// returned an error.
func TestNoticeSeverityError(t *testing.T) {
	m := testModel()
	m.configureInputs()
	m.setFocus("prompt")
	target := m.clipboardTarget()
	generation := m.clipboardReadGeneration
	wantErr := errors.New("boom")
	m.acceptClipboardRead(clipboardReadMsg{generation: generation, target: target, err: wantErr})
	if m.notice.severity != noticeError {
		t.Fatalf("severity = %v, want noticeError", m.notice.severity)
	}
	if want := "Clipboard read failed: boom · use your terminal's paste shortcut"; m.notice.text != want {
		t.Fatalf("notice text = %q, want %q", m.notice.text, want)
	}
}

// TestNoticeSeverityDone covers a confirmed successful operation: an
// queue.steer receipt confirming delivery.
func TestNoticeSeverityDone(t *testing.T) {
	m := testModel()
	cmd := protocol.Command{ID: "cmd-1", Kind: "queue.steer", ThreadID: m.state.Active}
	m.busy = &cmd
	m.state.Pending = &cmd
	m.Update(commandMsg{command: cmd, receipt: protocol.Receipt{State: "fixture-delivered"}})
	if m.notice.severity != noticeDone {
		t.Fatalf("severity = %v, want noticeDone", m.notice.severity)
	}
	if want := "Message steered into the active turn"; m.notice.text != want {
		t.Fatalf("notice text = %q, want %q", m.notice.text, want)
	}
}

// An accepted but unconfirmed steer stays active and says so honestly.
func TestNoticeSteerAcceptedAwaitsDelivery(t *testing.T) {
	m := testModel()
	cmd := protocol.Command{ID: "cmd-1", Kind: "queue.steer", ThreadID: m.state.Active}
	m.busy = &cmd
	m.state.Pending = &cmd
	m.Update(commandMsg{command: cmd, receipt: protocol.Receipt{State: "accepted"}})
	if m.notice.severity != noticeActive {
		t.Fatalf("severity = %v, want noticeActive", m.notice.severity)
	}
	if want := "Steer accepted · awaiting delivery"; m.notice.text != want {
		t.Fatalf("notice text = %q, want %q", m.notice.text, want)
	}
}

// An empty receipt state leaves no dangling separator.
func TestReceiptEmptyStateHasNoSeparator(t *testing.T) {
	m := testModel()
	cmd := protocol.Command{ID: "cmd-1", Kind: "prompt.send", ThreadID: m.state.Active}
	m.busy = &cmd
	m.state.Pending = &cmd
	m.Update(commandMsg{command: cmd, receipt: protocol.Receipt{}})
	if m.status != "Accepted" {
		t.Fatalf("status = %q", m.status)
	}
}
