package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func TestFragmentedTerminalRepliesAreNotPromptInput(t *testing.T) {
	for _, tc := range []struct {
		header, payload string
		want            tea.Msg
	}{
		{"\x1bP1+r", "5463", tea.CapabilityMsg{Content: "Tc"}},
		{"\x1bP1+r", "524742=38", tea.CapabilityMsg{Content: "RGB=8"}},
		{"\x1bP>|", "iTerm2 3.6.11", tea.TerminalVersionMsg{Name: "iTerm2 3.6.11"}},
		{"\x1bP0+r", "524742", nil},
	} {
		m := testModel()
		m.colorProbe.versionRequested, m.colorProbe.capabilitiesRequested = true, true
		m.prompt.SetValue("keep my draft")
		now := time.Now()
		if _, ok := m.filterTerminalReply(uv.UnknownEvent(tc.header), now).(terminalReplyStarted); !ok {
			t.Fatal("missing bounded reassembly timer")
		}
		deadline := m.colorReply.deadline
		for _, char := range tc.payload {
			if got := m.filterTerminalReply(tea.KeyPressMsg{Code: char, Text: string(char)}, now.Add(80*time.Millisecond)); got != nil {
				t.Fatal("fragment escaped to normal input:", got)
			}
		}
		if m.colorReply.deadline != deadline {
			t.Fatal("each fragment extended absolute deadline")
		}
		got := m.filterTerminalReply(tea.KeyPressMsg{Code: '\\', Mod: tea.ModAlt}, now.Add(90*time.Millisecond))
		if got != tc.want || m.colorReply.buffer != "" || m.prompt.Value() != "keep my draft" {
			t.Fatalf("%q: wrong completed response: %#v", tc.payload, got)
		}
		key := tea.KeyPressMsg{Code: 'x', Text: "x"}
		if m.filterTerminalReply(key, now.Add(100*time.Millisecond)) != key {
			t.Fatal("ordinary typing after response was consumed")
		}
	}
}

func TestIncompleteTerminalReplyReplaysTypingBeforeAbortAction(t *testing.T) {
	for _, abort := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl},
		tea.PasteMsg{Content: "paste"},
		tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 1},
		tea.FocusMsg{},
		uv.UnknownEvent("malformed"),
		tea.KeyPressMsg{Code: 'x', Text: strings.Repeat("x", terminalReplyLimit)},
	} {
		m := testModel()
		m.colorProbe.versionRequested = true
		now := time.Now()
		m.filterTerminalReply(uv.UnknownEvent("\x1bP>|"), now)
		keys := []tea.Msg{tea.KeyPressMsg{Code: 'a', Text: "a"}, tea.KeyPressMsg{Code: 'b', Text: "b"}}
		for _, key := range keys {
			m.filterTerminalReply(key, now)
		}
		result := m.filterTerminalReply(abort, now)
		want := terminalReplyReplay(append(keys, abort))
		if !reflect.DeepEqual(result, want) || m.colorReply.buffer != "" {
			t.Fatalf("abort %T lost/reordered original input: %#v", abort, result)
		}
	}
}

func TestTerminalReplyExpiryPreservesTextAndCannotExpireANewerReply(t *testing.T) {
	m := testModel()
	m.prompt.Focus()
	m.colorProbe.versionRequested = true
	now := time.Now()
	started := m.filterTerminalReply(uv.UnknownEvent("\x1bP>|"), now).(terminalReplyStarted)
	m.filterTerminalReply(tea.KeyPressMsg{Code: 'a', Text: "a"}, now)
	result := m.filterTerminalReply(terminalReplyExpired(started), now.Add(terminalReplyWindow))
	m.Update(result)
	if m.prompt.Value() != "a" || m.colorReply.buffer != "" {
		t.Fatal("expiry lost ordinary typing")
	}
	m.filterTerminalReply(uv.UnknownEvent("\x1bP>|"), now.Add(terminalReplyWindow))
	if result := m.filterTerminalReply(terminalReplyExpired(started), now.Add(terminalReplyWindow)); result != nil || m.colorReply.buffer == "" {
		t.Fatal("stale expiry reset a newer reply")
	}
	key := tea.KeyPressMsg{Code: 'b', Text: "b"}
	result = m.filterTerminalReply(key, now.Add(3*terminalReplyWindow))
	if !reflect.DeepEqual(result, terminalReplyReplay{key}) {
		t.Fatal("late input bypassed absolute deadline")
	}
}

func TestTerminalReplyDeadlineDoesNotSwallowFrameworkMessages(t *testing.T) {
	m := testModel()
	m.colorProbe.versionRequested, m.colorProbe.capabilitiesRequested = true, true
	now := time.Now()
	m.filterTerminalReply(uv.UnknownEvent("\x1bP>|"), now)
	for _, msg := range []tea.Msg{tea.QuitMsg{}, tea.InterruptMsg{}, tea.CapabilityMsg{Content: "RGB=8"}, tea.ColorProfileMsg{}} {
		if m.filterTerminalReply(msg, now.Add(2*terminalReplyWindow)) != msg {
			t.Fatalf("framework message %T bypassed its event loop", msg)
		}
	}
	if m.filterColorReports(m, tea.CapabilityMsg{Content: "RGB=8"}) != (tea.CapabilityMsg{Content: "RGB"}) {
		t.Fatal("pending fragment blocked capability normalization")
	}
}

func TestTerminalReassemblyDoesNotCaptureUnrelatedEscapeSequences(t *testing.T) {
	m := testModel()
	now := time.Now()
	for _, raw := range []string{"\x1bP>|", "\x1bP1+r"} {
		msg := uv.UnknownEvent(raw)
		if m.filterTerminalReply(msg, now) != msg {
			t.Fatal("unrequested reply was intercepted")
		}
	}
	m.colorProbe.versionRequested = true
	for _, raw := range []string{"\x1b", "\x1bP", "\x1bP>", "\x1bP1+r", "\x1b[unknown"} {
		msg := uv.UnknownEvent(raw)
		if m.filterTerminalReply(msg, now) != msg {
			t.Fatal("ambiguous/unrelated escape was intercepted")
		}
	}
}
