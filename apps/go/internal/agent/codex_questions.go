package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func ParseBuiltinCodexQuestions(id string, raw json.RawMessage) (*QuestionForm, error) {
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("question exceeds bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	value, err := readQuestionJSONValue(decoder, 0, true)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing question data")
	}
	root, err := questionObject(value, "sessionId", "toolCallId", "source", "_meta")
	if err != nil {
		return nil, err
	}
	meta, err := questionObject(root["_meta"], "questionDialect")
	if err != nil || meta["questionDialect"] != acpbridge.CodexQuestionDialect {
		return nil, fmt.Errorf("unsupported question dialect")
	}
	session, err := questionString(root["sessionId"], 512, false)
	if err != nil {
		return nil, err
	}
	tool, err := questionString(root["toolCallId"], 512, false)
	if err != nil {
		return nil, err
	}
	source, err := json.Marshal(root["source"])
	if err != nil {
		return nil, err
	}
	req, err := acpbridge.DecodeCodexQuestions(source)
	if err != nil {
		return nil, err
	}
	if req.ItemID != tool || req.ThreadID != session {
		return nil, fmt.Errorf("question source identity mismatch")
	}
	// ToolRequestUserInputResponse (App Server 0.155.1–0.156.0) has only an
	// answers map and no decline or cancel result, so Actions stays empty.
	form := &QuestionForm{SessionID: session, ToolCallID: tool}
	form.Request = protocol.Request{ID: id, Kind: "question", Mode: "blocking", State: "pending", Revision: 1, Origin: "Agent", Title: "Agent questions", Delivery: "acp-pending", DeliveryRoute: "native-response", SourcePayload: append(json.RawMessage(nil), raw...)}
	if !req.IsBlocking {
		form.Request.Mode = "async"
	}
	for i, native := range req.Questions {
		q := protocol.Question{ID: fmt.Sprintf("question_%d", i), Label: native.Header, Text: native.Question, Kind: "text", Required: new(bool)}
		if len(native.Options) > 0 {
			q.Kind, q.AllowOther = "single", native.IsOther
		}
		described := false
		for _, option := range native.Options {
			q.Options = append(q.Options, option.Label)
			q.OptionDescriptions = append(q.OptionDescriptions, option.Description)
			described = described || option.Description != ""
		}
		if !described {
			q.OptionDescriptions = nil
		}
		form.Request.Questions = append(form.Request.Questions, q)
		form.questions = append(form.questions, q)
	}
	return form, nil
}
