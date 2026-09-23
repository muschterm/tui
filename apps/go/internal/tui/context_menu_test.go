package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func contextMenuItemIndex(m *Model, label string) int {
	for i, item := range m.menu {
		if item.Label == label {
			return i
		}
	}
	return -1
}

func openPromptContextAt(t *testing.T, m *Model, x, y int) {
	t.Helper()
	m.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	if m.contextMenu == nil || m.menuTitle != "Prompt" {
		t.Fatalf("prompt context menu did not open: title=%q, context=%+v", m.menuTitle, m.contextMenu)
	}
}

func selectPromptRange(m *Model, start, end int) {
	m.prompt.BeginSelection(start, 0)
	m.prompt.ExtendSelection(end, 0)
	m.prompt.EndSelection()
}

func TestRightClickPromptContextPreservesSelectionCursorAndRestoresFocus(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	m.prompt.SetValue("alpha beta")
	selectPromptRange(m, 1, 5)
	selectionStart, selectionEnd, selected := m.prompt.Selection()
	cursor := inputCursor(&m.prompt)
	selectedText := m.prompt.SelectedText()
	f := m.measure()
	outer := m.promptContextRect(f)

	// The top-left composer outline cell is part of the prompt context target.
	openPromptContextAt(t, m, outer.X, outer.Y)
	if len(m.menu) != 2 || m.menu[0].Label != "Copy" || m.menu[1].Label != "Paste" {
		t.Fatalf("prompt menu items = %+v", m.menu)
	}
	if !selected || m.prompt.Value() != "alpha beta" || m.prompt.SelectedText() != selectedText || inputCursor(&m.prompt) != cursor {
		t.Fatal("opening context menu changed prompt selection, cursor, or draft")
	}
	gotStart, gotEnd, gotSelected := m.prompt.Selection()
	if !gotSelected || gotStart != selectionStart || gotEnd != selectionEnd {
		t.Fatalf("prompt selection changed: (%+v,%+v,%t), want (%+v,%+v,true)", gotStart, gotEnd, gotSelected, selectionStart, selectionEnd)
	}

	m.key(tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(m.menu) != 0 || m.contextMenu != nil || m.focus != "prompt" {
		t.Fatalf("Escape did not restore prompt focus: menu=%d context=%+v focus=%q", len(m.menu), m.contextMenu, m.focus)
	}
	if m.prompt.Value() != "alpha beta" || m.prompt.SelectedText() != selectedText || inputCursor(&m.prompt) != cursor {
		t.Fatal("closing context menu changed prompt selection, cursor, or draft")
	}
}

func TestPromptContextHasPasteWithoutSelectionAndPasteReplacesSelection(t *testing.T) {
	clearSSHEnvironment(t)
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	m.prompt.SetValue("abcdef")
	selectPromptRange(m, 1, 4)
	beforeCursor := inputCursor(&m.prompt)
	reads := 0
	m.clipboardRead = func() (string, error) {
		reads++
		return "X", nil
	}
	outer := m.promptContextRect(m.measure())
	openPromptContextAt(t, m, outer.X, outer.Y)
	if contextMenuItemIndex(m, "Paste") < 0 || contextMenuItemIndex(m, "Copy") < 0 {
		t.Fatalf("selected prompt menu missing Copy/Paste: %+v", m.menu)
	}
	if reads != 0 {
		t.Fatal("opening a context menu read the clipboard")
	}
	pasteIndex := contextMenuItemIndex(m, "Paste")
	cmd := m.activate(action{Kind: "menu-select", Index: pasteIndex})
	if cmd == nil || len(m.menu) != 0 || m.contextMenu != nil || m.focus != "prompt" {
		t.Fatalf("Paste did not close the menu and restore prompt focus: cmd=%t menu=%d context=%+v focus=%q", cmd != nil, len(m.menu), m.contextMenu, m.focus)
	}
	if reads != 0 || m.prompt.Value() != "abcdef" || inputCursor(&m.prompt) != beforeCursor || m.prompt.SelectedText() != "bcd" {
		t.Fatal("Paste changed prompt before the asynchronous clipboard read")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("Paste command returned no clipboard result")
	} else {
		m.Update(msg)
	}
	if reads != 1 || m.prompt.Value() != "aXef" || m.prompt.HasSelection() || m.focus != "prompt" {
		t.Fatalf("Paste result = %q, selection=%t, focus=%q, reads=%d", m.prompt.Value(), m.prompt.HasSelection(), m.focus, reads)
	}
}

func TestPromptContextWithoutSelectionOffersOnlyPaste(t *testing.T) {
	clearSSHEnvironment(t)
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	m.prompt.SetValue("draft")
	m.viewState().Draft = "draft"
	inputSetCursor(&m.prompt, 3)
	before := inputCursor(&m.prompt)
	m.clipboardRead = func() (string, error) { return "!", nil }
	outer := m.promptContextRect(m.measure())
	openPromptContextAt(t, m, outer.X, outer.Y)
	if len(m.menu) != 1 || m.menu[0].Label != "Paste" {
		t.Fatalf("prompt without selection should offer only Paste: %+v", m.menu)
	}
	if inputCursor(&m.prompt) != before || m.prompt.Value() != "draft" || m.prompt.HasSelection() {
		t.Fatal("opening Paste-only menu changed draft cursor or selection")
	}
	cmd := m.activate(action{Kind: "menu-select", Index: 0})
	if cmd == nil {
		t.Fatal("Paste action did not request clipboard text")
	}
	m.Update(cmd())
	if m.prompt.Value() != "dra!ft" || m.viewState().Draft != "dra!ft" || inputCursor(&m.prompt) != before+1 {
		t.Fatalf("Paste did not insert at the saved cursor: prompt=%q draft=%q cursor=%d", m.prompt.Value(), m.viewState().Draft, inputCursor(&m.prompt))
	}
}

func TestPromptContextRemainsAvailableInCompactNonConversationColumn(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
	m.prompt.SetValue("keep prompt available")
	m.selectColumn(shell.RightRegion)
	if m.conversationVisible() || m.measure().prompt.W <= 0 || m.measure().prompt.H <= 0 {
		t.Fatal("test setup did not show the persistent compact prompt outside Conversation")
	}
	outer := m.promptContextRect(m.measure())
	openPromptContextAt(t, m, outer.X, outer.Y)
	if len(m.menu) != 1 || m.menu[0].Label != "Paste" || m.focus != "prompt" {
		t.Fatalf("compact prompt context = items %+v, focus %q", m.menu, m.focus)
	}
}

func TestRightClickSelectedTranscriptOffersCopyAndPreservesSelection(t *testing.T) {
	clearSSHEnvironment(t)
	m := wideModel(t, 154, 40, 60)
	r := m.measure().transcript
	selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
	if m.selectedText == "" {
		t.Fatal("mouse drag produced no transcript selection")
	}
	selected := m.selectedText
	point := m.selectionStart
	m.mouse(tea.MouseClickMsg{X: point[0], Y: point[1], Button: tea.MouseRight})
	if m.contextMenu == nil || m.menuTitle != "Selected text" || len(m.menu) != 1 || m.menu[0].Label != "Copy" {
		t.Fatalf("selected transcript menu = title %q, items %+v", m.menuTitle, m.menu)
	}
	if m.selectedText != selected || !m.selectionLive(m.measure()) || m.focus != "transcript" {
		t.Fatal("right-click changed transcript selection or source focus")
	}
	var got string
	m.clipboardWrite = func(text string) error { got = text; return nil }
	if cmd := m.key(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatal("Enter did not activate Copy")
	} else if msg, ok := cmd().(clipboardWriteMsg); !ok || msg.text != selected || got != selected {
		t.Fatalf("copied selection = %#v, writer got %q, want %q", msg, got, selected)
	}
	if len(m.menu) != 0 || m.contextMenu != nil || m.focus != "transcript" || m.selectedText != selected {
		t.Fatal("Copy did not close the menu while restoring source selection/focus")
	}
}

func TestContextMenuKeyboardEquivalentOnPromptAndSelectedTranscript(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	if cmd := m.key(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift}); m.menuTitle != "Prompt" || len(m.menu) == 0 {
		t.Fatalf("Shift+F10 did not open prompt context menu: title=%q items=%+v cmd=%t", m.menuTitle, m.menu, cmd != nil)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focus != "prompt" || len(m.menu) != 0 {
		t.Fatal("Shift+F10 context menu did not restore prompt focus")
	}

	m = wideModel(t, 154, 40, 60)
	r := m.measure().transcript
	selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
	if cmd := m.key(tea.KeyPressMsg{Code: tea.KeyMenu}); m.menuTitle != "Selected text" || len(m.menu) != 1 || cmd != nil {
		t.Fatalf("Menu key did not open selected transcript context: title=%q items=%+v cmd=%t", m.menuTitle, m.menu, cmd != nil)
	}
}

func TestRightClickComposerControlDoesNotOpenPromptContext(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	f := m.measure()
	var send *hit
	for i := range f.hits {
		if f.hits[i].Key == "send" {
			send = &f.hits[i]
			break
		}
	}
	if send == nil {
		t.Fatal("Send control missing")
	}
	m.mouse(tea.MouseClickMsg{X: send.Rect.X, Y: send.Rect.Y, Button: tea.MouseRight})
	if len(m.menu) != 0 || m.contextMenu != nil || m.busy != nil {
		t.Fatal("right-clicking Send opened prompt menu or sent")
	}
}

func TestContextMenuVisualCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write context-menu captures")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	type capture struct {
		name  string
		w, h  int
		kind  string
		light bool
	}
	captures := []capture{
		{"prompt-selected", 160, 50, "prompt-selected", false},
		{"prompt-selected", 160, 50, "prompt-selected", true},
		{"prompt-empty", 160, 50, "prompt-empty", false},
		{"prompt-empty", 160, 50, "prompt-empty", true},
		{"transcript", 160, 50, "transcript", false},
		{"transcript", 160, 50, "transcript", true},
		{"prompt-compact", 60, 32, "prompt-selected", false},
	}
	for _, c := range captures {
		m := testModel()
		m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
		m.state.Light = c.light
		m.configureInputs()
		switch c.kind {
		case "prompt-selected":
			m.setFocus("prompt")
			m.prompt.SetValue("copy this selected phrase")
			selectPromptRange(m, 5, 9)
			outer := m.promptContextRect(m.measure())
			openPromptContextAt(t, m, outer.X, outer.Y)
		case "prompt-empty":
			m.setFocus("prompt")
			m.prompt.SetValue("paste at cursor")
			m.prompt.MoveToEnd()
			outer := m.promptContextRect(m.measure())
			openPromptContextAt(t, m, outer.X, outer.Y)
		case "transcript":
			r := m.measure().transcript
			selectTranscript(m, r.X+1, r.Y+1, r.X+16, r.Y+2)
			point := m.selectionStart
			m.mouse(tea.MouseClickMsg{X: point[0], Y: point[1], Button: tea.MouseRight})
		default:
			t.Fatalf("unknown capture %q", c.kind)
		}
		name := fmt.Sprintf("%dx%d-light%t-context-%s.ansi", c.w, c.h, c.light, c.name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(m.View().Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContextMenuLegacyShiftF10Alias(t *testing.T) {
	m := testModel()
	m.setFocus("prompt")
	if !contextMenuKey(tea.KeyPressMsg{Code: tea.KeyF20}) {
		t.Fatal("legacy Shift+F10 F20 representation was not recognized")
	}
	if !strings.Contains(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift}.Keystroke(), "shift+f10") {
		t.Fatal("pinned Bubble Tea key representation did not identify Shift+F10")
	}
}
