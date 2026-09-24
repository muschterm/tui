package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func readyPasteModel(t *testing.T) *Model {
	t.Helper()
	clearSSHEnvironment(t)
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.setFocus("prompt")
	return m
}

func startClipboardRead(t *testing.T, m *Model) clipboardReadMsg {
	t.Helper()
	cmd := m.pasteClipboard()
	if cmd == nil {
		t.Fatal("explicit Paste returned no read command")
	}
	result := cmd()
	msg, ok := result.(clipboardReadMsg)
	if !ok {
		t.Fatalf("Paste command returned %T, want clipboardReadMsg", result)
	}
	return msg
}

func TestPasteInsertsAtCursorAndPersistsDraftWithoutSending(t *testing.T) {
	m := readyPasteModel(t)
	m.prompt.SetValue("Alpha Z")
	m.viewState().Draft = m.prompt.Value()
	inputSetCursor(&m.prompt, len([]rune("Alpha ")))
	m.clipboardRead = func() (string, error) { return "β\nsecond\x1b[31m", nil }

	msg := startClipboardRead(t, m)
	m.acceptClipboardRead(msg)
	const want = "Alpha β\nsecondZ"
	if m.prompt.Value() != want || m.viewState().Draft != want {
		t.Fatalf("pasted prompt/draft = %q / %q, want %q", m.prompt.Value(), m.viewState().Draft, want)
	}
	if inputCursor(&m.prompt) != len([]rune("Alpha β\nsecond")) {
		t.Fatalf("cursor after paste = %d", inputCursor(&m.prompt))
	}
	if m.busy != nil || !m.dirty || m.status != "Pasted from system clipboard" {
		t.Fatalf("Paste state = busy:%v dirty:%t status:%q", m.busy, m.dirty, m.status)
	}
	if strings.Contains(m.prompt.Value(), "\x1b") {
		t.Fatal("unsafe clipboard control sequence reached the prompt")
	}
}

func TestPasteReplacesUnicodeMultilineSelection(t *testing.T) {
	m := readyPasteModel(t)
	m.prompt.SetValue("A界B\nC🙂D")
	m.viewState().Draft = m.prompt.Value()
	m.prompt.BeginSelection(1, 0)
	m.prompt.ExtendSelection(1, 1)
	m.prompt.EndSelection()
	if got := m.prompt.SelectedText(); got != "界B\nC" {
		t.Fatalf("selection setup = %q", got)
	}
	m.clipboardRead = func() (string, error) { return "X\nY", nil }

	msg := startClipboardRead(t, m)
	m.acceptClipboardRead(msg)
	const want = "AX\nY🙂D"
	if m.prompt.Value() != want || m.viewState().Draft != want {
		t.Fatalf("selection paste prompt/draft = %q / %q, want %q", m.prompt.Value(), m.viewState().Draft, want)
	}
	if m.busy != nil {
		t.Fatal("pasting into a selection submitted the prompt")
	}
}

func TestPasteRejectsResultsForChangedTarget(t *testing.T) {
	for _, change := range []string{"edit", "cursor", "thread", "selection"} {
		t.Run(change, func(t *testing.T) {
			m := readyPasteModel(t)
			m.prompt.SetValue("original draft")
			inputSetCursor(&m.prompt, 4)
			m.viewState().Draft = m.prompt.Value()
			m.clipboardRead = func() (string, error) { return " clipboard", nil }
			msg := startClipboardRead(t, m)

			switch change {
			case "edit":
				m.prompt.SetValue("edited after read began")
			case "cursor":
				inputSetCursor(&m.prompt, 7)
			case "thread":
				m.state.Active = "another-thread"
			case "selection":
				m.prompt.SelectAll()
			}
			before := m.prompt.Value()
			m.acceptClipboardRead(msg)
			if m.prompt.Value() != before || !strings.Contains(m.notice.text, "prompt changed") || m.status != "" {
				t.Fatalf("stale result changed input or feedback: prompt=%q before=%q notice=%q status=%q", m.prompt.Value(), before, m.notice.text, m.status)
			}
		})
	}
}

func TestPasteReadFailurePreservesDraft(t *testing.T) {
	m := readyPasteModel(t)
	m.prompt.SetValue("unsent work")
	m.viewState().Draft = m.prompt.Value()
	m.clipboardRead = func() (string, error) { return "", errors.New("clipboard unavailable") }
	msg := startClipboardRead(t, m)
	m.acceptClipboardRead(msg)
	if m.prompt.Value() != "unsent work" || m.viewState().Draft != "unsent work" || m.busy != nil {
		t.Fatal("clipboard read failure changed or sent the prompt")
	}
	if !strings.Contains(m.notice.text, "clipboard unavailable") || !strings.Contains(m.notice.text, "terminal's paste shortcut") {
		t.Fatalf("clipboard failure notice = %q", m.notice.text)
	}
}

func TestPasteOverSSHNeverReadsRemoteClipboard(t *testing.T) {
	clearSSHEnvironment(t)
	m := readyPasteModel(t)
	t.Setenv("SSH_TTY", "/dev/pts/3")
	called := false
	m.clipboardRead = func() (string, error) {
		called = true
		return "remote clipboard", nil
	}
	cmd := m.pasteClipboard()
	if called || !strings.Contains(m.notice.text, "over SSH") || !strings.Contains(m.notice.text, "terminal's paste shortcut") {
		t.Fatalf("SSH Paste route = called:%t notice:%q", called, m.notice.text)
	}
	if cmd == nil {
		t.Fatal("SSH Paste should show its fallback notice")
	}
}

func TestHerdrWithoutSSHMarkersNeverPastesHostClipboard(t *testing.T) {
	m := readyPasteModel(t)
	t.Setenv("HERDR_ENV", "1")
	m.prompt.SetValue("draft")
	m.clipboardRead = func() (string, error) {
		t.Fatal("herdr paste must not read the application host's clipboard")
		return "", nil
	}
	cmd := m.pasteClipboard()
	if cmd == nil || !strings.Contains(m.notice.text, "outer terminal") || m.prompt.Value() != "draft" {
		t.Fatalf("herdr paste = notice:%q draft:%q", m.notice.text, m.prompt.Value())
	}
	// The viewing terminal delivers its clipboard as text, without an OS read
	// on the host, through the same safe input replacement path.
	m.Update(tea.PasteMsg{Content: "from viewer"})
	if !strings.Contains(m.prompt.Value(), "from viewer") || m.busy != nil {
		t.Fatalf("terminal paste failed or sent prompt: %q", m.prompt.Value())
	}
}

func TestPasteDoesNotReadWhenComposerIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Model)
	}{
		{name: "menu open", set: func(m *Model) { m.menu = make([]menuItem, 1) }},
		{name: "project dialog", set: func(m *Model) { m.projectMode = "add" }},
		{name: "settings", set: func(m *Model) { m.settingsPage = "General" }},
		{name: "terminal too small", set: func(m *Model) { m.width = minTerminalWidth - 1 }},
		{name: "no active composer", set: func(m *Model) { m.state.Active = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := readyPasteModel(t)
			called := false
			m.clipboardRead = func() (string, error) { called = true; return "text", nil }
			tc.set(m)
			cmd := m.pasteClipboard()
			if called || cmd == nil || m.status != "" {
				t.Fatalf("unavailable Paste called reader or lost notice: called=%t cmd=%t status=%q", called, cmd != nil, m.status)
			}
		})
	}
}

func TestPasteAtCharacterLimitDoesNotClaimSuccess(t *testing.T) {
	m := readyPasteModel(t)
	m.prompt.CharLimit = 4
	m.prompt.SetValue("full")
	m.viewState().Draft = "full"
	m.dirty = false
	m.generation = 12
	m.clipboardRead = func() (string, error) { return "more", nil }
	msg := startClipboardRead(t, m)
	m.acceptClipboardRead(msg)
	if m.prompt.Value() != "full" || m.viewState().Draft != "full" || m.dirty || m.generation != 12 {
		t.Fatalf("full prompt changed: value=%q draft=%q dirty=%t generation=%d", m.prompt.Value(), m.viewState().Draft, m.dirty, m.generation)
	}
	if m.status != "" || !strings.Contains(m.notice.text, "made no changes") {
		t.Fatalf("full prompt reported a paste: status=%q notice=%q", m.status, m.notice.text)
	}
}

func TestPasteSizeLimitAndDuplicateResult(t *testing.T) {
	t.Run("size limit", func(t *testing.T) {
		m := readyPasteModel(t)
		m.prompt.SetValue("keep")
		m.clipboardRead = func() (string, error) { return strings.Repeat("x", clipboardPasteMaxBytes+1), nil }
		msg := startClipboardRead(t, m)
		m.acceptClipboardRead(msg)
		if m.prompt.Value() != "keep" || !strings.Contains(m.notice.text, "1 MiB limit") {
			t.Fatalf("oversize paste = value:%q notice:%q", m.prompt.Value(), m.notice.text)
		}
	})

	t.Run("duplicate result", func(t *testing.T) {
		m := readyPasteModel(t)
		m.prompt.SetValue("draft")
		inputSetCursor(&m.prompt, len([]rune("draft")))
		m.clipboardRead = func() (string, error) { return "!", nil }
		msg := startClipboardRead(t, m)
		m.acceptClipboardRead(msg)
		m.acceptClipboardRead(msg)
		if m.prompt.Value() != "draft!" {
			t.Fatalf("duplicate result inserted more than once: %q", m.prompt.Value())
		}
	})
}

func TestExplicitClipboardActionClearsOlderSelectionNotice(t *testing.T) {
	clearSSHEnvironment(t)
	m := readyPasteModel(t)
	m.notice.text = "Ctrl+C copies selected text"
	m.clipboardWrite = func(string) error { return nil }
	m.copyText("copy")
	if m.notice.text != "" {
		t.Fatalf("copy left old notice visible: %q", m.notice.text)
	}
	m.notice.text = "Ctrl+C copies selected text"
	m.clipboardRead = func() (string, error) { return "paste", nil }
	m.pasteClipboard()
	if m.notice.text != "" {
		t.Fatalf("paste left old notice visible: %q", m.notice.text)
	}
}

func TestForwardedPasteShortcutsUseGuardedClipboardRead(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'v', Mod: tea.ModCtrl},
		{Code: 'v', Text: "V", Mod: tea.ModCtrl | tea.ModShift},
		{Code: 'v', Text: "v", Mod: tea.ModSuper},
		{Code: tea.KeyInsert, Mod: tea.ModShift},
	} {
		t.Run(key.Keystroke(), func(t *testing.T) {
			m := readyPasteModel(t)
			m.prompt.SetValue("replace")
			m.prompt.SelectAll()
			m.clipboardRead = func() (string, error) { return "pasted", nil }
			cmd := m.key(key)
			if cmd == nil {
				t.Fatal("paste shortcut did not read clipboard")
			}
			msg, ok := cmd().(clipboardReadMsg)
			if !ok {
				t.Fatal("paste shortcut bypassed guarded clipboard read")
			}
			m.acceptClipboardRead(msg)
			m.acceptClipboardRead(msg)
			if m.prompt.Value() != "pasted" || m.viewState().Draft != "pasted" || m.busy != nil {
				t.Fatalf("paste changed or submitted wrong content: %q", m.prompt.Value())
			}
		})
	}
}
