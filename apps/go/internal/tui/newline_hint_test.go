package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Shift+Enter is advertised only once it is distinguishable from Enter.
func TestNewlineHintFollowsKeyboardNegotiation(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.setFocus("prompt")
	m.status = ""
	txt := screenText(m)
	if strings.Contains(txt, "Shift+Enter") || !strings.Contains(txt, "Ctrl+J Newline") {
		t.Fatalf("unnegotiated hint:\n%s", txt)
	}
	before := ansi.StringWidth(m.defaultStatus())
	m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	if !strings.Contains(screenText(m), "Shift+Enter Newline") {
		t.Fatal("negotiated hint missing")
	}
	if after := ansi.StringWidth(m.defaultStatus()); after != before {
		t.Fatalf("status width changed %d -> %d", before, after)
	}
	// An observed Shift+Enter also proves it.
	m2 := testModel()
	m2.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if m2.newlineHint() != "Shift+Enter" {
		t.Fatal("observed Shift+Enter not recorded")
	}
	m3 := testModel()
	m3.Update(tea.KeyboardEnhancementsMsg{})
	if m3.newlineHint() != "Ctrl+J" {
		t.Fatal("zero flags advertised Shift+Enter")
	}
}
