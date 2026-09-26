package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A bracketed paste with bare CR line breaks (as some terminals send them)
// becomes newlines in the prompt and spaces in single-line inputs.
func TestPasteCarriageReturnNewlines(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.setFocus("prompt")
	m.prompt.SetValue("")
	m.Update(tea.PasteMsg{Content: "d1\rd2"})
	if got := m.prompt.Value(); got != "d1\nd2" {
		t.Fatalf("prompt %q", got)
	}
	m.setFocus("thread-search")
	m.threadSearch.SetValue("")
	m.Update(tea.PasteMsg{Content: "d1\rd2"})
	if got := m.threadSearch.Value(); got != "d1 d2" {
		t.Fatalf("thread search %q", got)
	}
}
