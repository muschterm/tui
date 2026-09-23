package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestAppNewThreadDefaultsOnlyInitializeNewDrafts(t *testing.T) {
	m := acpModel()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "new-thread-defaults")
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-model"})
	m.activate(action{Kind: "setting", Value: "high"})
	existing := *m.state.DraftThreads["alpha"]
	m.activate(action{Kind: "app-settings"})
	m.activate(action{Kind: "settings-page", Value: "agents"})
	if m.settingsPage != "agents" || !strings.Contains(strings.Join(m.settingsText(), "\n"), "New thread defaults") {
		t.Fatal("Agents category missing")
	}
	m.activate(action{Kind: "app-thread-agent"})
	m.activate(action{Kind: "app-thread-agent-set", ID: "claude", Revision: m.snapshot.AppSettings.Revision})
	if m.busy == nil || m.busy.Kind != "settings.update" || m.busy.AppSettings.NewThreadDefaults == nil || m.busy.AppSettings.NewThreadDefaults.AgentID != "claude" {
		t.Fatal("agent default did not use revisioned app settings")
	}
	m.snapshot.AppSettings = *m.busy.AppSettings
	m.snapshot.AppSettings.Revision++
	m.busy = nil
	m.activate(action{Kind: "app-thread-field", ID: "model"})
	m.activate(action{Kind: "app-thread-field-set", ID: "model", Value: "opus", Revision: m.snapshot.AppSettings.Revision})
	if m.busy == nil || m.busy.AppSettings.NewThreadDefaults.Settings.Model != "opus" {
		t.Fatal("model default not captured")
	}
	m.snapshot.AppSettings = *m.busy.AppSettings
	m.snapshot.AppSettings.Revision++
	m.busy = nil
	m.closeSettings()
	m.beginThreadDraft("beta")
	if got := m.state.DraftThreads["beta"]; got.Agent != "claude" || got.Settings.Model != "opus" || got.Settings.Effort != "medium" || got.Settings.Permissions != "ask" {
		t.Fatal("new draft missed saved default", got)
	}
	if got := m.state.DraftThreads["alpha"]; got.Agent != existing.Agent || got.Settings != existing.Settings {
		t.Fatal("existing draft changed", got)
	}
	m.snapshot.Agents[1].Options[0].Values = []protocol.ConfigValue{{Value: "sonnet", Name: "Sonnet"}}
	if got := m.newDraftView().Settings.Model; got != "opus" {
		t.Fatal("stale saved model silently replaced", got)
	}
	if reason := m.agentSendBlocked(m.composerSelection()); !strings.Contains(reason, "no longer an offered model") {
		t.Fatal("stale saved model not blocked", reason)
	}
}

func TestAppNewThreadDefaultsRequireServerCapabilityAndOfferedAgent(t *testing.T) {
	m := acpModel()
	m.activate(action{Kind: "app-settings"})
	m.activate(action{Kind: "settings-page", Value: "agents"})
	m.activate(action{Kind: "app-thread-agent"})
	if m.busy != nil || len(m.menu) != 0 {
		t.Fatal("old server offered default mutation")
	}
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "new-thread-defaults")
	m.activate(action{Kind: "app-thread-agent-set", ID: "codex", Revision: m.snapshot.AppSettings.Revision})
	if m.busy != nil {
		t.Fatal("unready agent became the default")
	}
	m.activate(action{Kind: "app-thread-agent-set", ID: "missing", Revision: m.snapshot.AppSettings.Revision})
	if m.busy != nil {
		t.Fatal("unknown agent became the default")
	}
}

func TestDefaultAgentWithoutCurrentModelRequiresExplicitModelBeforeSaving(t *testing.T) {
	m := acpModel()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "new-thread-defaults")
	m.snapshot.Agents[1].Options[0].Current = ""
	m.activate(action{Kind: "app-settings"})
	m.activate(action{Kind: "settings-page", Value: "agents"})
	m.activate(action{Kind: "app-thread-agent-set", ID: "claude", Revision: m.snapshot.AppSettings.Revision})
	if m.busy != nil || m.menuTitle != "Default model for Claude" || len(m.menu) == 0 {
		t.Fatal("missing explicit model chooser")
	}
	m.activate(action{Kind: "menu-select", Index: 1})
	if m.busy == nil || m.busy.AppSettings.NewThreadDefaults.Settings.Model != "opus" {
		t.Fatal("model choice did not save complete defaults")
	}
}
