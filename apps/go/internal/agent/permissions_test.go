package agent

import (
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestLegacyUnavailablePermissionUsesSupervisedBaseline(t *testing.T) {
	for _, tc := range []struct{ id, baseline string }{{"codex", "supervised"}, {"claude", "default"}} {
		t.Run(tc.id, func(t *testing.T) {
			a := protocol.Agent{ID: tc.id, Name: tc.id, Options: []protocol.ConfigOption{{ID: "mode", Current: tc.baseline, Values: []protocol.ConfigValue{{Value: tc.baseline}, {Value: "auto"}, {Value: "full"}}}}, Fields: protocol.SettingFields{Permissions: "mode"}}
			if err := ValidateSettings(a, protocol.Settings{Permissions: Unavailable}); err != nil {
				t.Fatalf("old captured setting should use supervised policy: %v", err)
			}
			assignments := Assignments(a, protocol.Settings{Permissions: Unavailable})
			if len(assignments) != 1 || assignments[0].Value != tc.baseline {
				t.Fatalf("legacy setting could inherit an earlier wider policy: %+v", assignments)
			}
			if err := ValidateSettings(a, protocol.Settings{Permissions: "unsupported"}); err == nil {
				t.Fatal("unsupported explicit permission mode accepted")
			}
		})
	}
}

func TestRestartClearsStaleACPOptionCatalogue(t *testing.T) {
	s := protocol.Snapshot{Threads: []protocol.Thread{{AgentID: "claude", Options: []protocol.ConfigOption{{ID: "model"}}}, {AgentID: FixtureID, Options: []protocol.ConfigOption{{ID: "fixture"}}}}}
	Ensure(&s, nil)
	if len(s.Threads[0].Options) != 0 {
		t.Fatalf("old ACP options survived restart: %+v", s.Threads[0].Options)
	}
	if len(s.Threads[1].Options) != 1 {
		t.Fatal("fixture options were cleared")
	}
}
