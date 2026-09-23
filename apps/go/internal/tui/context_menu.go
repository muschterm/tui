package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type contextMenuState struct {
	returnFocus string
}

func contextMenuKey(k tea.KeyPressMsg) bool {
	if k.Code == tea.KeyMenu {
		return true
	}
	if k.Keystroke() == "shift+f10" {
		return true
	}
	// Legacy VT reports Shift+F10 as F20. Modern Kitty keyboard input reports
	// F10 plus Shift, which Keystroke recognizes above.
	return k.Code == tea.KeyF20 && k.Mod == 0
}

func (m *Model) openContextMenuForFocus() tea.Cmd {
	if len(m.menu) != 0 || m.settingsPage != "" || m.terminalTooSmall() {
		return nil
	}
	switch m.focus {
	case "prompt":
		return m.openPromptContextMenu()
	case "transcript", "right-body":
		f := m.measure()
		if m.selectionLive(f) && m.selectedText != "" {
			return m.openSelectionContextMenu(m.focus)
		}
	}
	return nil
}

func (m *Model) openPromptContextMenu() tea.Cmd {
	f := m.measure()
	if len(m.menu) != 0 || m.settingsPage != "" || m.terminalTooSmall() || !m.hasComposer() || f.prompt.W <= 0 || f.prompt.H <= 0 {
		return nil
	}
	var cmd tea.Cmd
	if m.focus != "prompt" {
		cmd = m.setFocus("prompt")
	}
	items := make([]menuItem, 0, 2)
	normalizeInputSelection(&m.prompt)
	if m.prompt.HasSelection() && m.prompt.SelectedText() != "" {
		items = append(items, menuItem{"Copy", action{Kind: "context-copy", Value: m.prompt.SelectedText()}})
	}
	items = append(items, menuItem{"Paste", action{Kind: "context-paste"}})
	m.showMenu("Prompt", items)
	m.contextMenu = &contextMenuState{returnFocus: "prompt"}
	return cmd
}

func (m *Model) openSelectionContextMenu(focus string) tea.Cmd {
	if len(m.menu) != 0 || m.settingsPage != "" || m.terminalTooSmall() {
		return nil
	}
	if focus != "transcript" && focus != "right-body" {
		return nil
	}
	var cmd tea.Cmd
	if m.focus != focus {
		cmd = m.setFocus(focus)
	}
	m.showMenu("Selected text", []menuItem{{"Copy", action{Kind: "context-copy", Value: m.selectedText}}})
	m.contextMenu = &contextMenuState{returnFocus: focus}
	return cmd
}

func (m *Model) promptContextRect(f frame) shell.Rect {
	if f.prompt.W <= 0 || f.prompt.H <= 0 || f.geom.Center.W < 3 {
		return shell.Rect{}
	}
	w := f.geom.Center.W - 2
	return shell.Rect{X: f.geom.Center.X + 1, Y: f.prompt.Y - 1, W: w, H: f.prompt.H + m.composerControlsHeight(w) + 2}
}

func (m *Model) openContextMenuAtPointer(f frame, x, y int) tea.Cmd {
	if m.settingsPage != "" || m.terminalTooSmall() {
		return nil
	}
	if m.selectionContains(f, x, y) {
		focus := "transcript"
		if m.selectionRegion == f.detail {
			focus = "right-body"
		}
		return m.openSelectionContextMenu(focus)
	}
	if !m.promptContextRect(f).Contains(x, y) {
		return nil
	}
	// Right-click on an existing composer control keeps that control's context.
	// Blank outline, typing and footer space still open the prompt menu.
	for _, h := range f.hits {
		if h.Key != "prompt" && h.Rect.Contains(x, y) {
			return nil
		}
	}
	return m.openPromptContextMenu()
}

func (m *Model) selectionContains(f frame, x, y int) bool {
	if !m.selectionLive(f) || m.selectedText == "" {
		return false
	}
	for _, span := range selectionSpans(m.selectionStart, m.selectionEnd, m.selectionRegion, m.width, m.height-1) {
		if span.y == y && x >= span.start && x < span.end {
			return true
		}
	}
	return false
}

func (m *Model) closeContextMenu() {
	if m.contextMenu == nil {
		m.menu = nil
		return
	}
	state := m.contextMenu
	m.contextMenu = nil
	m.menu = nil
	m.projectMode = ""
	m.projectInput.Blur()
	if state.returnFocus != "" {
		m.setFocus(state.returnFocus)
	}
}

func (m *Model) selectContextMenuItem(index int) tea.Cmd {
	if m.contextMenu == nil || index < 0 || index >= len(m.menu) {
		return nil
	}
	a := m.menu[index].Action
	m.closeContextMenu()
	return m.activate(a)
}
