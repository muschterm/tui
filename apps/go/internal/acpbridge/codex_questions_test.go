package acpbridge

import (
	"context"
	"encoding/json"
	acp "github.com/coder/acp-go-sdk"
	"io"
	"strings"
	"testing"
	"time"
)

const codexQuestionFixture = `{"threadId":"thread","turnId":"turn","itemId":"item","isBlocking":true,"questions":[{"id":"native_id","header":"Scope","question":"Which scope?","isOther":true,"isSecret":false,"options":[{"label":"Small","description":"A bounded change"},{"label":"Broad","description":"A large change"}]}]}`

func TestCodexQuestionBoundsAndNativeAnswerIdentity(t *testing.T) {
	req, err := DecodeCodexQuestions(json.RawMessage(codexQuestionFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"action":"accept","content":{"question_0":"Small"}}`,
		`{"action":"accept","content":{"question_0_custom":"A custom answer"}}`,
		`{"action":"accept","content":{}}`,
	} {
		answer, err := codexQuestionAnswers(req, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := answer["native_id"]; !ok {
			t.Fatal("native question ID was lost")
		}
	}
	// ToolRequestUserInputResponse (0.155.1–0.156.0) is only an answers map:
	// decline and cancel have no native result and never become empty answers.
	for _, input := range []string{
		`{"action":"cancel"}`, `{"action":"decline"}`, `{"action":"accept","content":{"question_0":"invented"}}`,
		`{"action":"accept","content":{"question_0":"Small","question_0_custom":"extra"}}`,
		`{"action":"accept","content":{"question_1":"Small"}}`,
	} {
		if _, err := codexQuestionAnswers(req, json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, input := range []string{
		strings.Replace(codexQuestionFixture, `"isBlocking":true`, `"isBlocking":"false"`, 1),
		strings.Replace(codexQuestionFixture, `"isBlocking":true`, `"isBlocking":true,"autoResolutionMs":100`, 1),
		strings.Replace(codexQuestionFixture, `"isSecret":false`, `"isSecret":true`, 1),
		strings.Replace(codexQuestionFixture, `"isOther":true`, `"isOther":true,"isOther":false`, 1),
		strings.Replace(codexQuestionFixture, `"header":"Scope"`, `"header":"Scope","unknown":true`, 1),
	} {
		if _, err := DecodeCodexQuestions(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted incompatible form: %s", input)
		}
	}
}

func TestCodexQuestionNativeResponseGuardRejectsLateAnswer(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "completed"}[stale], func(t *testing.T) {
			bridgeRead, clientWrite := io.Pipe()
			clientRead, bridgeWrite := io.Pipe()
			t.Cleanup(func() { bridgeRead.Close(); clientWrite.Close(); clientRead.Close(); bridgeWrite.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			b := newCodex(&host{}).(*codexBridge)
			b.sessionID, b.threadID = "thread", "thread"
			b.active = &codexTurn{threadID: "thread", turnID: "turn", sessionID: "thread", started: make(chan struct{})}
			close(b.active.started)
			b.h.conn = acp.NewConnection(nil, bridgeWrite, bridgeRead)
			_ = acp.NewConnection(func(_ context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
				if method != "elicitation/create" {
					t.Errorf("unexpected method %s", method)
				}
				if !strings.Contains(string(raw), CodexQuestionDialect) {
					t.Error("dialect missing")
				}
				return map[string]any{"action": "accept", "content": map[string]any{"question_0": "Small"}}, nil
			}, clientWrite, clientRead)
			result, err := b.userInput(ctx, json.RawMessage(codexQuestionFixture))
			if err != nil {
				t.Fatal(err)
			}
			answer := result.(map[string]any)
			token := answer[codexGrantKey].(string)
			if stale {
				b.mu.Lock()
				b.active = nil
				b.mu.Unlock()
			}
			grant, ok := b.lockGrantForWrite(token)
			if ok {
				grant.turn.dispatchMu.Unlock()
				b.releaseGrant(token, grant)
			}
			if ok == stale {
				t.Fatalf("native answer dispatch allowed=%v stale=%v", ok, stale)
			}
		})
	}
}
