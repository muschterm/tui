package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

const (
	clipboardWritingStatus = "Copying to system clipboard…"
	clipboardCopiedStatus  = "Copied to system clipboard"
	clipboardOSC52Status   = "OSC 52 clipboard copy requested · terminal acceptance cannot be confirmed"
	clipboardPasteMaxBytes = 1 << 20
)

type clipboardWriteMsg struct {
	generation uint64
	text       string
	err        error
}

type clipboardPasteTarget struct {
	active, draftProjectID, value        string
	cursor, selectionStart, selectionEnd int
	hasSelection                         bool
}

type clipboardReadMsg struct {
	generation uint64
	target     clipboardPasteTarget
	text       string
	err        error
}

// remoteClipboardSession identifies an interactive SSH client. On SSH, OSC 52
// is the path back to the user's terminal clipboard; a host-local clipboard
// process would write on the remote machine instead.
func remoteClipboardSession() bool {
	for _, name := range []string{"SSH_TTY", "SSH_CONNECTION", "SSH_CLIENT"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func clipboardCommand(text string, remote bool, generation uint64, write func(string) error) (tea.Cmd, string) {
	if text == "" {
		return nil, "Nothing to copy"
	}
	if remote {
		return tea.SetClipboard(text), clipboardOSC52Status
	}
	if write == nil {
		write = clipboard.WriteAll
	}
	return func() tea.Msg {
		return clipboardWriteMsg{generation: generation, text: text, err: write(text)}
	}, clipboardWritingStatus
}

func (m *Model) copyText(text string) tea.Cmd {
	m.notice.text = ""
	m.notice.generation++
	if text == "" {
		m.status = "Nothing to copy"
		return nil
	}
	m.clipboardGeneration++
	cmd, status := clipboardCommand(text, remoteClipboardSession(), m.clipboardGeneration, m.clipboardWrite)
	m.status = status
	return cmd
}

// pasteClipboard reads only the local clipboard after an explicit Paste
// action. A running SSH client must use its terminal's paste shortcut because
// clipboard.ReadAll would inspect the remote host.
func (m *Model) pasteClipboard() tea.Cmd {
	m.notice.text = ""
	m.notice.generation++
	m.clipboardReadGeneration++
	generation := m.clipboardReadGeneration
	if reason := m.clipboardPasteBlocked(); reason != "" {
		m.status = ""
		return m.showNotice("Paste unavailable · " + reason)
	}
	if remoteClipboardSession() {
		m.status = ""
		return m.showNotice("Clipboard read unavailable over SSH · use your terminal's paste shortcut")
	}
	read := m.clipboardRead
	if read == nil {
		read = clipboard.ReadAll
	}
	target := m.clipboardTarget()
	m.status = "Reading system clipboard…"
	return func() tea.Msg {
		text, err := read()
		return clipboardReadMsg{generation: generation, target: target, text: text, err: err}
	}
}

func (m *Model) clipboardPasteBlocked() string {
	if reason := m.hiddenWorkBlocked(); reason != "" {
		return reason
	}
	if m.projectMode != "" {
		return "close the project dialog first"
	}
	if len(m.menu) > 0 {
		return "close the menu first"
	}
	if !m.hasComposer() {
		return "open a thread before pasting"
	}
	if m.focus != "prompt" {
		return "focus the prompt first"
	}
	f := m.measure()
	if f.prompt.W <= 0 || f.prompt.H <= 0 {
		return "the prompt is not visible"
	}
	return ""
}

func (m *Model) clipboardTarget() clipboardPasteTarget {
	start, end, selected := m.prompt.Selection()
	return clipboardPasteTarget{
		active: m.state.Active, draftProjectID: m.state.DraftProjectID,
		value: m.prompt.Value(), cursor: inputCursor(&m.prompt),
		selectionStart: inputOffset(&m.prompt, start), selectionEnd: inputOffset(&m.prompt, end),
		hasSelection: selected,
	}
}

func (m *Model) acceptClipboardRead(msg clipboardReadMsg) tea.Cmd {
	if msg.generation != m.clipboardReadGeneration {
		return nil
	}
	// Consume this identity before any result handling so a duplicate message
	// cannot insert the same clipboard contents twice.
	m.clipboardReadGeneration++
	if reason := m.clipboardPasteBlocked(); reason != "" {
		m.status = ""
		return m.showNotice("Paste cancelled · " + reason)
	}
	if current := m.clipboardTarget(); current != msg.target {
		m.status = ""
		return m.showNotice("Paste cancelled · the prompt changed while the clipboard was read")
	}
	if msg.err != nil {
		m.status = ""
		return m.showNotice("Clipboard read failed: " + safe(msg.err.Error()) + " · use your terminal's paste shortcut")
	}
	if len(msg.text) > clipboardPasteMaxBytes {
		m.status = ""
		return m.showNotice("Paste unavailable · clipboard text exceeds the 1 MiB limit")
	}
	content := safe(msg.text)
	if content == "" {
		m.status = ""
		return m.showNotice("Clipboard is empty")
	}
	before := m.prompt.Value()
	beforeCursor := inputCursor(&m.prompt)
	beforeStart, beforeEnd, beforeSelection := m.prompt.Selection()
	cmd := updateInput(&m.prompt, tea.PasteMsg{Content: content})
	if m.prompt.Value() == before && inputCursor(&m.prompt) == beforeCursor {
		start, end, selected := m.prompt.Selection()
		if selected == beforeSelection && (!selected || start == beforeStart && end == beforeEnd) {
			m.status = ""
			return m.showNotice("Paste made no changes · the prompt may be at its character limit")
		}
	}
	m.promptView.Reset()
	m.viewState().Draft = m.prompt.Value()
	m.markDirty()
	m.configureInputs()
	m.status = "Pasted from system clipboard"
	return cmd
}

func (m *Model) acceptClipboardWrite(msg clipboardWriteMsg) tea.Cmd {
	if msg.generation != m.clipboardGeneration {
		return nil
	}
	if msg.err == nil {
		m.status = clipboardCopiedStatus
		return nil
	}
	// The local writer gave us a real error. OSC 52 is still a useful fallback,
	// but Bubble Tea has no acknowledgement for it, so say so explicitly.
	m.status = "System clipboard write failed: " + safe(msg.err.Error()) + " · OSC 52 fallback requested; acceptance cannot be confirmed"
	return tea.SetClipboard(msg.text)
}
