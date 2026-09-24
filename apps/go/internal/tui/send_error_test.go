package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRejectedSendOpensActionableDialogAndKeepsDraft(t *testing.T) {
	m := navigationModel()
	m.beginThreadDraft("alpha")
	m.Update(tea.PasteMsg{Content: "Keep this unsent prompt"})
	m.activate(action{Kind: "send"})
	if m.menuTitle != "Cannot send message" || m.busy != nil || m.prompt.Value() != "Keep this unsent prompt" {
		t.Fatal("invalid Send failed to preserve draft and open dialog")
	}
	if selected := m.menu[m.menuIndex]; selected.Action.Kind != "settings" || selected.Action.Value != "model" {
		t.Fatalf("no direct fix: %+v", selected)
	}
	if !strings.Contains(m.View().Content, "CANNOT SEND MESSAGE") {
		t.Fatal("dialog not rendered")
	}
	if dir := os.Getenv("TUI_GO_CAPTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "160x50-send-error.ansi"), []byte(m.View().Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.activate(action{Kind: "menu-select", Index: m.menuIndex})
	if !strings.Contains(m.menuTitle, "model") || m.busy != nil {
		t.Fatal("fix action did not open model chooser")
	}
	m.activate(action{Kind: "menu-close"})
	if m.prompt.Value() != "Keep this unsent prompt" || m.focus != "prompt" {
		t.Fatal("dismissal lost prompt or focus")
	}
}
