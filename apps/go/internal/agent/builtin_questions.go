package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
)

// ParseBuiltinClaudeQuestions keeps the app's existing bounded form and answer
// contract, while retaining our bridge's original control payload and identity.
// The third-party dialect and its fixtures remain separately pinned.
func ParseBuiltinClaudeQuestions(id string, raw json.RawMessage) (*QuestionForm, error) {
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("question exceeds bounds")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	v, err := readQuestionJSON(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing question data")
	}
	root, err := questionObject(v, "mode", "sessionId", "toolCallId", "message", "requestedSchema", "_meta")
	if err != nil {
		return nil, err
	}
	meta, err := questionObject(root["_meta"], "questionDialect", "requestId", "source")
	if err != nil {
		return nil, err
	}
	if meta["questionDialect"] != acpbridge.QuestionDialect {
		return nil, fmt.Errorf("unsupported question dialect")
	}
	if _, err = questionString(meta["requestId"], 512, false); err != nil {
		return nil, err
	}
	source, ok := meta["source"].(map[string]any)
	if !ok || source["subtype"] != "can_use_tool" || source["tool_name"] != "AskUserQuestion" || source["tool_use_id"] != root["toolCallId"] {
		return nil, fmt.Errorf("question source identity mismatch")
	}
	delete(root, "_meta")
	formRaw, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	form, err := ParseClaudeQuestions(id, formRaw)
	if err != nil {
		return nil, err
	}
	form.Request.SourcePayload = append(json.RawMessage(nil), raw...)
	return form, nil
}
