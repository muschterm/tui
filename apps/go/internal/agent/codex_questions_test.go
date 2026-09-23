package agent

import (
	"encoding/json"
	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"strings"
	"testing"
)

func TestBuiltinCodexQuestionContract(t *testing.T) {
	source := json.RawMessage(`{"threadId":"session","turnId":"turn","itemId":"tool","isBlocking":true,"questions":[{"id":"choice-id","header":"Scope","question":"Which scope?","isOther":true,"options":[{"label":"Small","description":"A bounded change"},{"label":"Broad","description":"A large change"}]},{"id":"text-id","header":"Detail","question":"Anything else?","options":null}]}`)
	raw, _ := json.Marshal(map[string]any{"sessionId": "session", "toolCallId": "tool", "source": source, "_meta": map[string]any{"questionDialect": acpbridge.CodexQuestionDialect}})
	form, err := ParseBuiltinCodexQuestions("request", raw)
	if err != nil {
		t.Fatal(err)
	}
	if form.Request.Mode != "blocking" || len(form.Request.Questions) != 2 || !strings.Contains(form.Request.Questions[0].Text, "A bounded change") || form.Request.Questions[1].Kind != "text" || string(form.Request.SourcePayload) != string(raw) {
		t.Fatal("lost question semantics", form)
	}
	response, err := form.Response([]protocol.Answer{{Choices: []string{"Small"}}, {Text: "Extra detail"}})
	if err != nil {
		t.Fatal(err)
	}
	content := response["content"].(map[string]any)
	if content["question_0"] != "Small" || content["question_1_custom"] != "Extra detail" {
		t.Fatal(response)
	}
	for _, changed := range []string{
		strings.Replace(string(raw), `"sessionId":"session"`, `"sessionId":"other"`, 1),
		strings.Replace(string(raw), acpbridge.CodexQuestionDialect, "unknown", 1),
	} {
		if _, err := ParseBuiltinCodexQuestions("request", json.RawMessage(changed)); err == nil {
			t.Fatal("accepted mismatched source")
		}
	}
}
