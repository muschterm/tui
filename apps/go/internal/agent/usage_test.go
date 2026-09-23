package agent

import (
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"testing"
)

func TestUsageSnapshotsPreserveCostAndPartialContext(t *testing.T) {
	th := &protocol.Thread{SessionID: "s"}
	apply(t, th, `{"sessionUpdate":"tui_usage_update","dialect":"tui-go.usage.v1","used":120,"sessionID":"s","model":"m","source":"native","reportedAt":"2026-09-22T10:00:00Z","cost":{"amount":0.0123456789,"currency":"USD","estimated":true}}`)
	if th.Usage == nil || th.Usage.Size != 0 || th.Usage.Cost.Amount != "0.0123456789" || !th.Usage.Cost.Estimated {
		t.Fatalf("partial %+v", th.Usage)
	}
	apply(t, th, `{"sessionUpdate":"tui_usage_update","dialect":"tui-go.usage.v1","used":60,"size":1000,"sessionID":"s","model":"m","reportedAt":"2026-09-22T10:00:01Z"}`)
	if th.Usage.Used != 60 || th.Usage.Cost.Amount != "0.0123456789" {
		t.Fatalf("snapshot not replacement %+v", th.Usage)
	}
	apply(t, th, `{"sessionUpdate":"tui_usage_update","dialect":"tui-go.usage.v1","used":999,"size":1000,"sessionID":"s","reportedAt":"2026-09-22T10:00:00Z"}`)
	if th.Usage.Used != 60 {
		t.Fatal("stale event replaced fresh observation")
	}
	apply(t, th, `{"sessionUpdate":"tui_usage_update","dialect":"tui-go.usage.v1","sessionID":"s","cost":{"amount":0.02,"currency":"USD","estimated":true}}`)
	if th.Usage.Used != -1 || th.Usage.Cost.Amount != "0.02" {
		t.Fatalf("cost incorrectly added or occupancy fabricated %+v", th.Usage)
	}
}

func TestUsageRejectsInvalidOrForeignMeasurements(t *testing.T) {
	for _, raw := range []string{
		`{"sessionUpdate":"usage_update","used":-1,"size":100}`,
		`{"sessionUpdate":"usage_update","used":0,"size":0}`,
		`{"sessionUpdate":"tui_usage_update","dialect":"other","used":0,"size":100}`,
		`{"sessionUpdate":"tui_usage_update","dialect":"tui-go.usage.v1","sessionID":"foreign","used":0,"size":100}`,
		`{"sessionUpdate":"usage_update","used":0,"size":100,"cost":{"amount":-1,"currency":"USD"}}`,
	} {
		th := &protocol.Thread{SessionID: "s"}
		apply(t, th, raw)
		if th.Usage != nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestQuestionAnswerSeparatesSameTurnReplyChronology(t *testing.T) {
	th := &protocol.Thread{ID: "t", TurnID: "turn", State: "running"}
	apply(t, th, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Which color?"}}`)
	th.Activity = append(th.Activity, protocol.Activity{ID: "answer", Role: "question-answer", TurnID: "turn", RequestID: "question"})
	apply(t, th, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Blue"}}`, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":" selected"}}`)
	if len(th.Activity) != 3 || th.Activity[0].Text != "Which color?" || th.Activity[1].Role != "question-answer" || th.Activity[2].Text != "Blue selected" {
		t.Fatalf("reply crossed answer anchor %+v", th.Activity)
	}
}
