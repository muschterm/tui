package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestBuiltinDefaultMigrationInvalidatesExternalFacts(t *testing.T) {
	s := protocol.Snapshot{Agents: []protocol.Agent{{ID: "claude", Command: "claude-agent-acp", Args: []string{"old-argument"}, State: StateReady, Version: ClaudeQuestionVersion,
		Options: []protocol.ConfigOption{{ID: "model", Current: "old-model"}}, Capabilities: []string{"load-session"}}},
		Threads: []protocol.Thread{{ID: "kept", Agent: "claude", SessionID: "old-session", Queue: []protocol.Prompt{{ID: "unsent"}}}}}
	Ensure(&s, nil)
	a := Find(&s, "claude")
	if a.Command != DefaultClaudeCommand || a.State != StateUnprobed || a.Version != "" || len(a.Args) != 0 || len(a.Options) != 0 || len(a.Capabilities) != 0 {
		t.Fatalf("stale adapter facts retained: %+v", a)
	}
	if len(s.Threads) != 1 || s.Threads[0].SessionID != "old-session" || len(s.Threads[0].Queue) != 1 || s.Threads[0].Queue[0].ID != "unsent" {
		t.Fatal("agent migration changed user work")
	}
	if strings.Contains(InstallHint, "npm") {
		t.Fatal("built-in setup still requires another adapter")
	}
}

func TestBuiltinQuestionDialectPreservesSourceWithoutChangingLegacyParser(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-questions-0.80.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Samples []struct {
			Request map[string]any `json:"request"`
		} `json:"samples"`
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	request := fixtures.Samples[0].Request
	request["_meta"] = map[string]any{"questionDialect": acpbridge.QuestionDialect, "requestId": "native-request", "source": map[string]any{"subtype": "can_use_tool", "tool_name": "AskUserQuestion", "tool_use_id": request["toolCallId"], "input": map[string]any{"questions": []any{}}}}
	raw, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseClaudeQuestions("old", raw); err == nil {
		t.Fatal("new dialect silently passed legacy gate")
	}
	form, err := ParseBuiltinClaudeQuestions("new", raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(form.Request.SourcePayload) != string(raw) || form.Request.DeliveryRoute != "native-response" || form.Request.Mode != "blocking" {
		t.Fatal("question correlation/source or execution semantics lost")
	}
	request["_meta"].(map[string]any)["questionDialect"] = "unknown"
	raw, _ = json.Marshal(request)
	if _, err = ParseBuiltinClaudeQuestions("new", raw); err == nil {
		t.Fatal("unknown dialect accepted")
	}
}

func TestBuiltinRequiresInstalledRuntimeBeforeAnyLaunch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvClaudeRuntime, "")
	t.Setenv(EnvCodexRuntime, "")
	for _, id := range []string{"claude", "codex"} {
		s, err := Start(context.Background(), Options{AgentID: id, Command: "builtin:" + id, Cwd: t.TempDir()})
		if s != nil || !errors.Is(err, ErrRuntimeMissing) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
