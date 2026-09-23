package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func clearSSHEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"SSH_TTY", "SSH_CONNECTION", "SSH_CLIENT"} {
		t.Setenv(name, "")
	}
}

func TestLocalClipboardCommandWritesExactTextAndReportsResult(t *testing.T) {
	const want = "exact copied text\n漢字"
	var got string
	cmd, status := clipboardCommand(want, false, 7, func(text string) error {
		got = text
		return nil
	})
	if status != clipboardWritingStatus || cmd == nil {
		t.Fatalf("local command = (%v, %q)", cmd != nil, status)
	}
	msg, ok := cmd().(clipboardWriteMsg)
	if !ok || msg.generation != 7 || msg.text != want || msg.err != nil || got != want {
		t.Fatalf("local clipboard result = %#v, writer got %q", msg, got)
	}
}

func TestSSHClipboardCommandUsesOSC52WithoutLocalWrite(t *testing.T) {
	const want = "remote copy"
	called := false
	cmd, status := clipboardCommand(want, true, 1, func(string) error {
		called = true
		return nil
	})
	if cmd == nil || called || !strings.Contains(status, "OSC 52") || !strings.Contains(status, "cannot be confirmed") {
		t.Fatalf("SSH clipboard route = (%v, %q), local write called=%t", cmd != nil, status, called)
	}
	msg := cmd()
	if !strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg") || fmt.Sprint(msg) != want {
		t.Fatalf("SSH clipboard message = %T %v", msg, msg)
	}
}

func TestSSHEnvironmentRoutesCopyToTerminalClipboard(t *testing.T) {
	clearSSHEnvironment(t)
	t.Setenv("SSH_TTY", "/dev/pts/1")
	m := testModel()
	called := false
	m.clipboardWrite = func(string) error {
		called = true
		return nil
	}
	cmd := m.copyText("remote text")
	if cmd == nil || called || !strings.Contains(m.status, "cannot be confirmed") {
		t.Fatalf("SSH copy route = (%v, called=%t, status=%q)", cmd != nil, called, m.status)
	}
	if msg := cmd(); !strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg") || fmt.Sprint(msg) != "remote text" {
		t.Fatalf("SSH copy command = %T %v", msg, msg)
	}
}

func TestCopyShortcutsUseOSC52OverSSH(t *testing.T) {
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModCtrl | tea.ModShift, tea.ModSuper} {
		t.Run(fmt.Sprint(mod), func(t *testing.T) {
			clearSSHEnvironment(t)
			t.Setenv("SSH_TTY", "/dev/pts/1")
			m := wideModel(t, 154, 40, 60)
			r := m.measure().transcript
			selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
			if m.selectedText == "" {
				t.Fatal("mouse drag produced no transcript selection")
			}
			localWrite := false
			m.clipboardWrite = func(string) error {
				localWrite = true
				return nil
			}
			cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: mod})
			if cmd == nil || localWrite || !strings.Contains(m.status, "acceptance cannot be confirmed") {
				t.Fatalf("SSH copy route = (cmd=%t, local=%t, status=%q)", cmd != nil, localWrite, m.status)
			}
			msg := cmd()
			if !strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg") || fmt.Sprint(msg) != m.selectedText {
				t.Fatalf("SSH clipboard message = %T %v, want selected transcript text", msg, msg)
			}
		})
	}
}

func TestClipboardWriterFailureIsShownAndFallsBackToOSC52(t *testing.T) {
	m := testModel()
	m.clipboardGeneration = 3
	err := errors.New("pbcopy unavailable")
	cmd := m.acceptClipboardWrite(clipboardWriteMsg{generation: 3, text: "kept text", err: err})
	if !strings.Contains(m.status, err.Error()) || !strings.Contains(m.status, "acceptance cannot be confirmed") || cmd == nil {
		t.Fatalf("failure feedback = %q, fallback=%t", m.status, cmd != nil)
	}
	msg := cmd()
	if !strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg") || fmt.Sprint(msg) != "kept text" {
		t.Fatalf("fallback message = %T %v", msg, msg)
	}
}

func TestOlderClipboardResultCannotReplaceNewerFeedback(t *testing.T) {
	m := testModel()
	m.clipboardGeneration = 2
	m.status = "Copying newer text…"
	if cmd := m.acceptClipboardWrite(clipboardWriteMsg{generation: 1, text: "older", err: errors.New("old failure")}); cmd != nil {
		t.Fatal("stale failure emitted a clipboard fallback")
	}
	if m.status != "Copying newer text…" {
		t.Fatalf("stale clipboard result replaced status: %q", m.status)
	}
}

func TestMouseSelectionCopiesThroughInjectedWriter(t *testing.T) {
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModCtrl | tea.ModShift, tea.ModSuper} {
		t.Run(fmt.Sprint(mod), func(t *testing.T) {
			clearSSHEnvironment(t)
			m := wideModel(t, 154, 40, 60)
			r := m.measure().transcript
			selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
			if m.selectedText == "" {
				t.Fatal("mouse drag produced no transcript selection")
			}
			var got string
			m.clipboardWrite = func(text string) error { got = text; return nil }
			cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: mod})
			if cmd == nil {
				t.Fatal("copy shortcut did not copy the selection")
			}
			msg, ok := cmd().(clipboardWriteMsg)
			if !ok || msg.text != m.selectedText || got != m.selectedText {
				t.Fatalf("copied selection = %#v, writer got %q, want %q", msg, got, m.selectedText)
			}
			quit := m.key(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
			if _, ok := quit().(tea.QuitMsg); !ok {
				t.Fatal("Ctrl+Q must quit with a selection")
			}
		})
	}
}

func TestForwardedSuperCCopiesSelectedTranscriptAndShowsFallback(t *testing.T) {
	clearSSHEnvironment(t)
	m := wideModel(t, 154, 40, 60)
	r := m.measure().transcript
	selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
	if m.selectedText == "" {
		t.Fatal("mouse drag produced no transcript selection")
	}
	if !strings.Contains(m.notice.text, "Ctrl+C / Ctrl+Shift+C copies") {
		t.Fatalf("selection copy hint = %q", m.notice.text)
	}

	var got string
	m.clipboardWrite = func(text string) error { got = text; return nil }
	// Bubble Tea's String() may return printable Text without modifiers, so the
	// forwarded Command modifier must be checked through Keystroke().
	cmd := m.key(tea.KeyPressMsg{Code: 'c', Text: "c", Mod: tea.ModSuper})
	if cmd == nil {
		t.Fatal("forwarded Super+C did not copy the selection")
	}
	msg, ok := cmd().(clipboardWriteMsg)
	if !ok || msg.text != m.selectedText || got != m.selectedText {
		t.Fatalf("copied selection = %#v, writer got %q, want %q", msg, got, m.selectedText)
	}
}

func TestSuperCWithoutSelectionDoesNotCopyOrQuit(t *testing.T) {
	clearSSHEnvironment(t)
	m := testModel()
	m.setFocus("prompt")
	m.prompt.SetValue("draft")
	before := m.prompt.Value()
	called := false
	m.clipboardWrite = func(string) error {
		called = true
		return nil
	}
	cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
	if called || cmd != nil || m.prompt.Value() != before || m.status != "No text is selected" {
		t.Fatalf("Super+C without selection changed input or copied/quitted: called=%t cmd=%T draft=%q status=%q", called, cmd, m.prompt.Value(), m.status)
	}
}

func TestMetaCIsNotTreatedAsMacCommandCopy(t *testing.T) {
	clearSSHEnvironment(t)
	m := wideModel(t, 154, 40, 60)
	r := m.measure().transcript
	selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
	called := false
	m.clipboardWrite = func(string) error {
		called = true
		return nil
	}
	if cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModMeta}); called || cmd != nil {
		t.Fatalf("Meta+C was treated as Command+C: called=%t cmd=%T", called, cmd)
	}
}

func TestControlCWithoutSelectionStillQuits(t *testing.T) {
	m := testModel()
	cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+C did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C without selection must quit")
	}
}

func TestCopyTranscriptUsesTheSameClipboardPath(t *testing.T) {
	clearSSHEnvironment(t)
	m := testModel()
	const title, body = "Visible title", "Visible body"
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID == m.state.Active {
			m.snapshot.Threads[i].Activity = []protocol.Activity{{ID: "copy", Title: title, Text: body}}
		}
	}
	var got string
	m.clipboardWrite = func(text string) error {
		got = text
		return nil
	}
	cmd := m.activate(action{Kind: "copy-transcript"})
	if cmd == nil || m.status != clipboardWritingStatus {
		t.Fatalf("transcript copy command/status = (%v, %q)", cmd != nil, m.status)
	}
	msg, ok := cmd().(clipboardWriteMsg)
	want := title + "\n" + body + "\n\n"
	if !ok || msg.text != want || got != want {
		t.Fatalf("transcript clipboard content = %#v, writer got %q, want %q", msg, got, want)
	}
	_, _ = m.Update(msg)
	if m.status != clipboardCopiedStatus {
		t.Fatalf("successful write status = %q", m.status)
	}
}

func TestEmptyClipboardActionDoesNotStartAWrite(t *testing.T) {
	m := testModel()
	called := false
	m.clipboardWrite = func(string) error {
		called = true
		return nil
	}
	if cmd := m.copyText(""); cmd != nil || called || m.status != "Nothing to copy" {
		t.Fatalf("empty copy = (%v, called=%t, status=%q)", cmd != nil, called, m.status)
	}
}
