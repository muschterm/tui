package acpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// receiptHost captures the session updates a bridge sends to its ACP client.
// A trailing sentinel update proves which receipts were (not) sent before it.
func receiptHost(t *testing.T) (*host, func() []string) {
	t.Helper()
	out, bridgeWrite := io.Pipe()
	in, _ := io.Pipe()
	h := &host{ctx: context.Background()}
	h.conn = acp.NewConnection(func(context.Context, string, json.RawMessage) (any, *acp.RequestError) { return nil, nil }, bridgeWrite, in)
	lines := make(chan json.RawMessage, 64)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			lines <- append(json.RawMessage(nil), scanner.Bytes()...)
		}
	}()
	return h, func() []string {
		if err := h.update(context.Background(), "sentinel", map[string]any{"sessionUpdate": "sentinel"}); err != nil {
			t.Fatal(err)
		}
		var receipts []string
		for line := range lines {
			var message struct {
				Params struct {
					SessionID string `json:"sessionId"`
					Update    struct {
						Kind       string `json:"sessionUpdate"`
						Dialect    string `json:"dialect"`
						ToolCallID string `json:"toolCallId"`
					} `json:"update"`
				} `json:"params"`
			}
			if err := json.Unmarshal(line, &message); err != nil {
				t.Fatal(err)
			}
			switch u := message.Params.Update; u.Kind {
			case "sentinel":
				return receipts
			case "tui_question_delivery":
				if u.Dialect != QuestionDeliveryDialect {
					t.Fatalf("unpinned receipt: %s", line)
				}
				receipts = append(receipts, message.Params.SessionID+"/"+u.ToolCallID)
			}
		}
		return receipts
	}
}

func TestCodexReceiptOnlyForWrittenUserInputAnswer(t *testing.T) {
	cases := []struct {
		name, method, response string
		withdraw               bool
		want                   int
	}{
		{"answer", "item/tool/requestUserInput", `{"id":7,"result":{"answers":{}}}`, false, 1},
		{"error response", "item/tool/requestUserInput", `{"id":7,"error":{"code":-32602,"message":"unsupported"}}`, false, 0},
		{"withdrawn before answer", "item/tool/requestUserInput", `{"id":7,"result":{"answers":{}}}`, true, 0},
		{"approval", "item/commandExecution/requestApproval", `{"id":7,"result":{"decision":"accept"}}`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, receipts := receiptHost(t)
			b := newCodex(h).(*codexBridge)
			if err := b.observeNativeRequest([]byte(`{"id":7,"method":"` + tc.method + `","params":{"threadId":"thread","turnId":"turn","itemId":"item"}}`)); err != nil {
				t.Fatal(err)
			}
			if tc.withdraw {
				_ = b.observeNativeRequest([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":7}}`))
			}
			var native bytes.Buffer
			for range 2 { // A duplicate response is neither written nor receipted twice.
				if _, err := b.writeNativeResponse(&native, []byte(tc.response)); err != nil {
					t.Fatal(err)
				}
			}
			got := receipts()
			if len(got) != tc.want || tc.want == 1 && got[0] != "thread/item" {
				t.Fatalf("receipts %v, want %d", got, tc.want)
			}
		})
	}
}

func TestClaudeReceiptRequiresSuccessfulToolResultForAnsweredQuestion(t *testing.T) {
	h, receipts := receiptHost(t)
	c := newClaude(h).(*claude)
	c.session = "session"
	turn := &claudeTurn{answered: map[string]bool{"asked": true, "failed": true}}
	c.turn = turn
	for _, result := range []string{
		`{"type":"tool_result","tool_use_id":"failed","is_error":true}`,
		`{"type":"tool_result","tool_use_id":"unanswered"}`,
		`{"type":"tool_result","tool_use_id":"asked"}`,
		`{"type":"tool_result","tool_use_id":"asked"}`,
	} {
		c.event(json.RawMessage(`{"type":"user","message":{"content":[` + result + `]}}`))
	}
	if got := receipts(); len(got) != 1 || got[0] != "session/asked" {
		t.Fatalf("receipts %v", got)
	}
	if len(turn.answered) != 0 {
		t.Fatalf("answered tools retained: %v", turn.answered)
	}
}

func TestAnsweredQuestionToolOnlyForAcceptedAskUserQuestion(t *testing.T) {
	ask := json.RawMessage(`{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-q"}`)
	bash := json.RawMessage(`{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-b"}`)
	allow := map[string]any{"behavior": "allow", "updatedInput": json.RawMessage(`{"answers":{}}`)}
	deny := map[string]any{"behavior": "deny", "message": "declined"}
	if got := answeredQuestionTool(ask, allow); got != "tool-q" {
		t.Fatalf("accepted answer not recognised: %q", got)
	}
	if answeredQuestionTool(ask, deny) != "" || answeredQuestionTool(bash, allow) != "" || answeredQuestionTool(ask, nil) != "" {
		t.Fatal("decline, approval or error treated as an answer")
	}
}
