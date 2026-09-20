package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestShutdownRequiresMatchingSuccessfulOutcome(t *testing.T) {
	home := t.TempDir()
	if err := confirmedShutdown(home, "instance"); err == nil {
		t.Fatal("missing confirmation counted as success")
	}
	for _, outcome := range []protocol.ShutdownOutcome{{InstanceID: "other", Success: true}, {InstanceID: "instance", Error: "disk save failed"}, {InstanceID: "instance", Success: true}} {
		b, _ := json.Marshal(outcome)
		if err := os.WriteFile(filepath.Join(home, "shutdown-instance.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		err := confirmedShutdown(home, "instance")
		wantSuccess := outcome.Success && outcome.InstanceID == "instance"
		if (err == nil) != wantSuccess {
			t.Fatalf("outcome %+v error %v", outcome, err)
		}
	}
}
