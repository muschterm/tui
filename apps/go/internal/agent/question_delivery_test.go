package agent

import (
	"encoding/json"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestQuestionDeliveryReceiptIsPinnedAndNeverTranscript(t *testing.T) {
	receipt := func(dialect, tool string) Update {
		raw, _ := json.Marshal(map[string]any{"sessionUpdate": "tui_question_delivery", "dialect": dialect, "toolCallId": tool})
		return DecodeUpdate(raw)
	}
	if tool, ok := QuestionDeliveryReceipt(receipt(acpbridge.QuestionDeliveryDialect, "call")); !ok || tool != "call" {
		t.Fatalf("pinned receipt rejected: %q %v", tool, ok)
	}
	for _, u := range []Update{receipt("other", "call"), receipt(acpbridge.QuestionDeliveryDialect, ""), {Kind: "tool_call", Raw: json.RawMessage(`{"toolCallId":"call"}`)}} {
		if _, ok := QuestionDeliveryReceipt(u); ok {
			t.Fatalf("unpinned receipt accepted: %s", u.Raw)
		}
	}
	var thread protocol.Thread
	Normalize(&thread, receipt("other", "call"))
	if len(thread.Activity) != 0 {
		t.Fatalf("receipt became transcript: %+v", thread.Activity)
	}
	if QuestionToolCallID(json.RawMessage(`{"toolCallId":"call","source":{}}`)) != "call" || QuestionToolCallID(json.RawMessage(`[]`)) != "" {
		t.Fatal("tool call identity not read from retained payload")
	}
}
