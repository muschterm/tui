package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// QuestionDeliveryReceipt returns the tool call named by a built-in bridge's
// pinned delivery receipt. The receipt is evidence for the server to correlate
// with its own request, turn and connection state; alone it resolves nothing.
func QuestionDeliveryReceipt(u Update) (string, bool) {
	if u.Kind != "tui_question_delivery" || len(u.Raw) > 4<<10 {
		return "", false
	}
	var wire struct {
		Dialect    string `json:"dialect"`
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(u.Raw, &wire) != nil || wire.Dialect != acpbridge.QuestionDeliveryDialect || wire.ToolCallID == "" || len(wire.ToolCallID) > 512 {
		return "", false
	}
	return wire.ToolCallID, true
}

// QuestionToolCallID returns the provider tool call a retained native question
// payload was presented for, or "" when the payload does not name one.
func QuestionToolCallID(source json.RawMessage) string {
	var wire struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(source, &wire) != nil {
		return ""
	}
	return wire.ToolCallID
}

// QuestionForm is the bounded AskUserQuestion form emitted by Claude ACP 0.80.0.
// It is not a general JSON Schema or MCP elicitation implementation.
type QuestionForm struct {
	Request               protocol.Request
	SessionID, ToolCallID string
	questions             []protocol.Question
}

// ParseClaudeQuestions rejects schemas whose meaning cannot be represented by
// the application, including previews, constraints and ambiguous wire labels.
func ParseClaudeQuestions(id string, raw json.RawMessage) (*QuestionForm, error) {
	if len(raw) > 64*1024 || !utf8.Valid(raw) {
		return nil, fmt.Errorf("elicitation exceeds bounds or has invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	value, err := readQuestionJSON(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing elicitation data")
	}
	root, err := questionObject(value, "mode", "sessionId", "toolCallId", "message", "requestedSchema")
	if err != nil {
		return nil, err
	}
	if root["mode"] != "form" {
		return nil, fmt.Errorf("unsupported elicitation mode")
	}
	session, err := questionString(root["sessionId"], 512, false)
	if err != nil {
		return nil, err
	}
	tool, err := questionString(root["toolCallId"], 512, false)
	if err != nil {
		return nil, err
	}
	message, err := questionString(root["message"], 4096, false)
	if err != nil {
		return nil, err
	}
	schema, err := questionObject(root["requestedSchema"], "type", "properties")
	if err != nil {
		return nil, err
	}
	if schema["type"] != "object" {
		return nil, fmt.Errorf("unsupported schema type")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing question properties")
	}
	count := 0
	for key := range props {
		if !strings.HasSuffix(key, "_custom") {
			count++
		}
	}
	if count < 1 || count > 4 {
		return nil, fmt.Errorf("expected 1–4 questions")
	}
	if count > 1 && message != "Please answer the following questions." {
		return nil, fmt.Errorf("unsupported multi-question message")
	}
	form := &QuestionForm{SessionID: session, ToolCallID: tool}
	// ACP form elicitation defines accept, decline and cancel responses; the
	// latter two carry no content.
	form.Request = protocol.Request{ID: id, Kind: "question", Mode: "blocking", State: "pending", Revision: 1, Origin: "Agent", Title: "Agent questions", Delivery: "acp-pending", DeliveryRoute: "native-response", Actions: []string{protocol.RequestActionDecline, protocol.RequestActionCancel}, SourcePayload: append(json.RawMessage(nil), raw...)}
	seenText := map[string]bool{}
	used := 0
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("question_%d", i)
		field, e := questionObject(props[key], "type", "title", "description", "oneOf", "items")
		if e != nil {
			return nil, e
		}
		used++
		q := protocol.Question{ID: key, Required: new(bool)}
		if title, exists := field["title"]; exists {
			q.Label, e = questionString(title, 256, false)
			if e != nil {
				return nil, e
			}
		}
		original := message
		if count == 1 {
			if _, exists := field["description"]; exists {
				return nil, fmt.Errorf("single question repeats its description")
			}
		} else {
			original, e = questionString(field["description"], 4096, false)
			if e != nil {
				return nil, e
			}
		}
		if seenText[original] {
			return nil, fmt.Errorf("duplicate original question text")
		}
		seenText[original] = true
		q.Text = original
		var options any
		switch field["type"] {
		case "string":
			if _, exists := field["items"]; exists {
				return nil, fmt.Errorf("unexpected single-choice items")
			}
			q.Kind = "single"
			options = field["oneOf"]
		case "array":
			if _, exists := field["oneOf"]; exists {
				return nil, fmt.Errorf("unexpected multiple-choice oneOf")
			}
			q.Kind = "multiple"
			items, e := questionObject(field["items"], "anyOf")
			if e != nil {
				return nil, e
			}
			options = items["anyOf"]
		default:
			return nil, fmt.Errorf("unsupported question type")
		}
		opts, ok := options.([]any)
		if !ok || len(opts) < 2 || len(opts) > 4 {
			return nil, fmt.Errorf("expected 2–4 options")
		}
		seen := map[string]bool{}
		for _, opt := range opts {
			option, e := questionObject(opt, "const", "title", "description")
			if e != nil {
				return nil, e
			}
			value, e := questionString(option["const"], 512, false)
			if e != nil {
				return nil, e
			}
			title, e := questionString(option["title"], 512, false)
			if e != nil {
				return nil, e
			}
			if value != title || seen[value] {
				return nil, fmt.Errorf("ambiguous option label")
			}
			seen[value] = true
			q.Options = append(q.Options, value)
			detail := ""
			if description, exists := option["description"]; exists {
				detail, e = questionString(description, 2048, false)
				if e != nil {
					return nil, e
				}
			}
			q.OptionDescriptions = append(q.OptionDescriptions, detail)
		}
		if !slices.ContainsFunc(q.OptionDescriptions, func(d string) bool { return d != "" }) {
			q.OptionDescriptions = nil
		}
		if custom, exists := props[key+"_custom"]; exists {
			used++
			companion, e := questionObject(custom, "type", "title", "description", "_meta")
			if e != nil {
				return nil, e
			}
			expected := "Type your own answer, or add a note to the option you chose above (optional)."
			if q.Kind == "multiple" {
				expected = "Type your own answer to add to your selection above (optional)."
			}
			if companion["type"] != "string" || companion["title"] != "Other" || companion["description"] != expected {
				return nil, fmt.Errorf("unsupported custom-answer field")
			}
			meta, e := questionObject(companion["_meta"], "_askUserQuestionCustomAnswer")
			if e != nil {
				return nil, e
			}
			marker, e := questionObject(meta["_askUserQuestionCustomAnswer"], "questionId", "isCustomAnswer")
			if e != nil {
				return nil, e
			}
			if marker["questionId"] != key || marker["isCustomAnswer"] != true {
				return nil, fmt.Errorf("invalid custom-answer correlation")
			}
			q.AllowOther = true
		}
		form.Request.Questions = append(form.Request.Questions, q)
		private := q
		private.Options = append([]string(nil), q.Options...)
		private.OptionDescriptions = append([]string(nil), q.OptionDescriptions...)
		private.Required = new(bool)
		form.questions = append(form.questions, private)
	}
	if used != len(props) {
		return nil, fmt.Errorf("unsupported question fields")
	}
	return form, nil
}

// Response preserves exact labels and custom text, omitting skipped optional
// fields. Single-choice notes alongside a pick are deliberately unsupported by
// the application contract; Other instead of a pick and additive multi-select
// Other are supported. This method performs no delivery or persistence.
func (f *QuestionForm) Response(answers []protocol.Answer) (map[string]any, error) {
	if f == nil || len(f.questions) == 0 {
		return nil, fmt.Errorf("missing question form")
	}
	normalized, err := protocol.NormalizeQuestionAnswers(f.questions, answers, nil)
	if err != nil {
		return nil, err
	}
	content := map[string]any{}
	for i, a := range normalized {
		q := f.questions[i]
		if a.Text != "" {
			if _, err := questionString(a.Text, 4096, false); err != nil {
				return nil, err
			}
			content[q.ID+"_custom"] = a.Text
		}
		if len(a.Choices) > 0 {
			if q.Kind == "single" {
				content[q.ID] = a.Choices[0]
			} else {
				content[q.ID] = append([]string(nil), a.Choices...)
			}
		}
	}
	return map[string]any{"action": "accept", "content": content}, nil
}

// ActionResponse is the elicitation result for a chosen decline or cancel:
// the action alone, with no content. The request must offer the action.
func (f *QuestionForm) ActionResponse(action string) (map[string]any, error) {
	if f == nil || len(f.questions) == 0 {
		return nil, fmt.Errorf("missing question form")
	}
	if err := protocol.ValidateRequestAction(f.Request, action, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"action": action}, nil
}

// Respond builds the response for a recorded request outcome: the chosen
// action when one is set, otherwise the accepted answers.
func (f *QuestionForm) Respond(action string, answers []protocol.Answer) (map[string]any, error) {
	if action != "" {
		return f.ActionResponse(action)
	}
	return f.Response(answers)
}

func questionObject(value any, allowed ...string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected question object")
	}
	for key := range object {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return nil, fmt.Errorf("unsupported question field %q", key)
		}
	}
	return object, nil
}

func questionString(value any, limit int, empty bool) (string, error) {
	text, ok := value.(string)
	if !ok || len(text) > limit || (!empty && strings.TrimSpace(text) == "") || Sanitize(text) != text || strings.ContainsRune(text, '\ufffd') {
		return "", fmt.Errorf("invalid or unsafe question string")
	}
	return text, nil
}

// The standard decoder accepts duplicate keys and replaces malformed surrogate
// strings. Reject those ambiguities before interpreting any actionable schema.
func readQuestionJSON(dec *json.Decoder, depth int) (any, error) {
	return readQuestionJSONValue(dec, depth, false)
}

func readQuestionJSONValue(dec *json.Decoder, depth int, allowNull bool) (any, error) {
	if depth > 16 {
		return nil, fmt.Errorf("question JSON nesting exceeds limit")
	}
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		switch t {
		case '{':
			result := map[string]any{}
			for dec.More() {
				keyToken, e := dec.Token()
				if e != nil {
					return nil, e
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON key")
				}
				if _, exists := result[key]; exists {
					return nil, fmt.Errorf("duplicate JSON key")
				}
				value, e := readQuestionJSONValue(dec, depth+1, allowNull)
				if e != nil {
					return nil, e
				}
				result[key] = value
			}
			if _, e := dec.Token(); e != nil {
				return nil, e
			}
			return result, nil
		case '[':
			result := []any{}
			for dec.More() {
				value, e := readQuestionJSONValue(dec, depth+1, allowNull)
				if e != nil {
					return nil, e
				}
				result = append(result, value)
			}
			if _, e := dec.Token(); e != nil {
				return nil, e
			}
			return result, nil
		}
	case nil:
		if allowNull {
			return nil, nil
		}
		return nil, fmt.Errorf("null question field")
	}
	return token, nil
}
