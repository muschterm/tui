package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestSymbolSettingOverridesEnvironmentDefaultAndPersists(t *testing.T) {
	m := testModel()
	if m.iconsSetting() != "nerd" || m.plainIcons {
		t.Fatal("default is not Nerd Font")
	}
	m.setEnvIcons("ascii")
	if m.iconsSetting() != "ascii" || !m.plainIcons || m.state.Icons != "" {
		t.Fatal("environment default not applied while nothing is saved")
	}
	m.activate(action{Kind: "app-settings"})
	m.activate(action{Kind: "settings-page", Value: "appearance"})
	text := strings.Join(m.settingsText(), "\n")
	if !strings.Contains(text, "Symbols") || !strings.Contains(text, "ASCII") || !strings.Contains(text, "TUI_GO_ICONS") {
		t.Fatalf("appearance page lacks the symbol setting: %s", text)
	}
	m.activate(action{Kind: "icons"})
	if m.state.Icons != "nerd" || m.plainIcons || m.icon("close") != icons["close"].glyph || !m.dirty {
		t.Fatal("explicit Nerd Font choice did not override the environment default or mark the view dirty")
	}
	m.activate(action{Kind: "icons"})
	if m.state.Icons != "ascii" || !m.plainIcons || m.icon("close") != "x" {
		t.Fatal("second toggle did not select ASCII")
	}
	if strings.Contains(strings.Join(m.settingsText(), "\n"), "environment default.") {
		t.Fatal("saved choice still described as following the environment")
	}

	// The saved choice reloads without the environment variable.
	data, err := json.Marshal(m.state)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := New(nil, "test", fixture.Initial(), data)
	reloaded.setEnvIcons("")
	if reloaded.iconsSetting() != "ascii" || !reloaded.plainIcons {
		t.Fatal("saved ASCII choice lost on reload")
	}
	if componentBorder(roundedOutline, reloaded.plainIcons).TopLeft != "+" {
		t.Fatal("ASCII mode did not switch box borders")
	}
	_ = shell.NewState()
}

func TestProjectScopeCannotChangeSymbols(t *testing.T) {
	m := testModel()
	m.activate(action{Kind: "project-settings", ID: fixture.Initial().Projects[0].ID})
	before := m.state.Icons
	m.activate(action{Kind: "icons"})
	if m.state.Icons != before || m.notice.text == "" {
		t.Fatal("project scope changed the app symbol set or gave no notice")
	}
}
