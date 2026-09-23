package server

import (
	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"testing"
)

func codexNativeQuestionWire(session string) map[string]any {
	return map[string]any{"sessionId": session, "toolCallId": "question-call", "_meta": map[string]any{"questionDialect": acpbridge.CodexQuestionDialect}, "source": map[string]any{
		"threadId": session, "turnId": "native-turn", "itemId": "question-call", "isBlocking": false, "autoResolutionMs": nil,
		"questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Which scope?", "isOther": true, "options": []any{
			map[string]any{"label": "Small", "description": "A bounded change"}, map[string]any{"label": "Broad", "description": "A large change"},
		}}},
	}}
}

func TestCodexContinuedWorkQuestionPersistsAndAnswersWithoutPromptQueue(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	fleet.codexQuestions = true
	codex := agent.Find(&e.snap, "codex")
	codex.State, codex.Options = agent.StateReady, fakeOptions(t, nil)
	codex.Fields = agent.Fields(codex.Options)
	settings := fakeSettings()
	receipt, err := e.command(protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-acp", Agent: "codex", Settings: &settings, Text: "ask native question"})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.TargetID
	s := waitFor(t, e, "Codex question", func(s protocol.Snapshot) bool { return len(threadOf(s, id).Requests) == 1 })
	thread := threadOf(s, id)
	r := thread.Requests[0]
	if thread.State != "running" || r.Mode != "async" || r.DeliveryRoute != "native-response" {
		t.Fatalf("lost native execution mode: %+v", r)
	}
	c := nativeAnswer(id, r)
	receipt, err = e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	if retried, err := e.command(c); err != nil || retried != receipt {
		t.Fatal("answer retry did not deduplicate", err)
	}
	thread = waitTurn(t, e, id, "prompt-start")
	if thread.State != "idle" || len(thread.Queue) != 0 || thread.Requests[0].Delivery != "acp-unconfirmed" {
		t.Fatalf("incorrect outcome: %+v", thread)
	}
	f := fleet.last()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) != 1 || f.questionResponse["content"].(map[string]any)["question_0"] != "Small" {
		t.Fatal("answer was not a native response", f.questionResponse)
	}
}
