package acpbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	claudeQuestionMaxJSON   = 64 << 10
	claudeQuestionMaxDepth  = 16
	claudeQuestionMaxText   = 4096
	claudeQuestionMaxHeader = 256
	claudeQuestionMaxLabel  = 512
	claudeQuestionMaxDetail = 2048
)

type claudeQuestionOption struct {
	Label       string
	Description string
	HasDesc     bool
}

// claudeQuestion is the bounded part of AskUserQuestion needed to validate an
// elicitation response and turn it back into Claude's updatedInput.
type claudeQuestion struct {
	Text        string
	Header      string
	Options     []claudeQuestionOption
	MultiSelect bool
}

// claudeQuestionForm converts the installed Claude CLI's AskUserQuestion input
// to the pinned form-elicitation wire shape. It accepts only the fields whose
// meaning the application can preserve.
func claudeQuestionForm(session, toolID, requestID string, rawControl, input json.RawMessage) (any, []claudeQuestion, error) {
	if _, err := claudeQuestionString(session, 512, false); err != nil {
		return nil, nil, fmt.Errorf("invalid Claude session id: %w", err)
	}
	if _, err := claudeQuestionString(toolID, 512, false); err != nil {
		return nil, nil, fmt.Errorf("invalid Claude tool id: %w", err)
	}
	if _, err := claudeQuestionString(requestID, 512, false); err != nil {
		return nil, nil, fmt.Errorf("invalid Claude request id: %w", err)
	}
	if _, err := claudeQuestionObjectAllowNull(rawControl); err != nil {
		return nil, nil, fmt.Errorf("invalid Claude control request: %w", err)
	}
	root, err := claudeQuestionObject(input)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid AskUserQuestion input: %w", err)
	}
	if err := claudeQuestionOnlyKeys(root, "questions"); err != nil {
		return nil, nil, err
	}
	items, ok := root["questions"].([]any)
	if !ok || len(items) < 1 || len(items) > 4 {
		return nil, nil, fmt.Errorf("expected 1–4 AskUserQuestion questions")
	}

	questions := make([]claudeQuestion, 0, len(items))
	seenQuestions := make(map[string]bool, len(items))
	properties := make(map[string]any, len(items)*2)
	for i, item := range items {
		field, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("question %d must be an object", i+1)
		}
		if err := claudeQuestionOnlyKeys(field, "question", "header", "options", "multiSelect"); err != nil {
			return nil, nil, fmt.Errorf("question %d: %w", i+1, err)
		}
		text, err := claudeQuestionString(field["question"], claudeQuestionMaxText, false)
		if err != nil {
			return nil, nil, fmt.Errorf("question %d text: %w", i+1, err)
		}
		if seenQuestions[text] {
			return nil, nil, fmt.Errorf("duplicate question text")
		}
		seenQuestions[text] = true
		header, err := claudeQuestionString(field["header"], claudeQuestionMaxHeader, false)
		if err != nil {
			return nil, nil, fmt.Errorf("question %d header: %w", i+1, err)
		}
		multiSelect, ok := field["multiSelect"].(bool)
		if !ok {
			return nil, nil, fmt.Errorf("question %d multiSelect must be boolean", i+1)
		}
		choices, ok := field["options"].([]any)
		if !ok || len(choices) < 2 || len(choices) > 4 {
			return nil, nil, fmt.Errorf("question %d expected 2–4 options", i+1)
		}

		q := claudeQuestion{Text: text, Header: header, MultiSelect: multiSelect}
		seenOptions := make(map[string]bool, len(choices))
		wireOptions := make([]any, 0, len(choices))
		for j, rawOption := range choices {
			option, ok := rawOption.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("question %d option %d must be an object", i+1, j+1)
			}
			if err := claudeQuestionOnlyKeys(option, "label", "description"); err != nil {
				return nil, nil, fmt.Errorf("question %d option %d: %w", i+1, j+1, err)
			}
			label, err := claudeQuestionString(option["label"], claudeQuestionMaxLabel, false)
			if err != nil {
				return nil, nil, fmt.Errorf("question %d option %d label: %w", i+1, j+1, err)
			}
			if seenOptions[label] {
				return nil, nil, fmt.Errorf("question %d has duplicate option labels", i+1)
			}
			seenOptions[label] = true
			o := claudeQuestionOption{Label: label}
			wireOption := map[string]any{"const": label, "title": label}
			if rawDescription, exists := option["description"]; exists {
				description, err := claudeQuestionString(rawDescription, claudeQuestionMaxDetail, false)
				if err != nil {
					return nil, nil, fmt.Errorf("question %d option %d description: %w", i+1, j+1, err)
				}
				o.Description, o.HasDesc = description, true
				wireOption["description"] = description
			}
			q.Options = append(q.Options, o)
			wireOptions = append(wireOptions, wireOption)
		}
		questions = append(questions, q)

		key := fmt.Sprintf("question_%d", i)
		fieldSchema := map[string]any{"title": header}
		if len(items) > 1 {
			fieldSchema["description"] = text
		}
		if multiSelect {
			fieldSchema["type"] = "array"
			fieldSchema["items"] = map[string]any{"anyOf": wireOptions}
		} else {
			fieldSchema["type"] = "string"
			fieldSchema["oneOf"] = wireOptions
		}
		properties[key] = fieldSchema
		customDescription := "Type your own answer, or add a note to the option you chose above (optional)."
		if multiSelect {
			customDescription = "Type your own answer to add to your selection above (optional)."
		}
		properties[key+"_custom"] = map[string]any{
			"type":        "string",
			"title":       "Other",
			"description": customDescription,
			"_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{
				"questionId": key, "isCustomAnswer": true,
			}},
		}
	}

	message := "Please answer the following questions."
	if len(questions) == 1 {
		message = questions[0].Text
	}
	return map[string]any{
		"mode":       "form",
		"sessionId":  session,
		"toolCallId": toolID,
		"message":    message,
		"requestedSchema": map[string]any{
			"type": "object", "properties": properties,
		},
		"_meta": map[string]any{
			"questionDialect": QuestionDialect,
			"requestId":       requestID,
			"source":          json.RawMessage(append([]byte(nil), rawControl...)),
		},
	}, questions, nil
}

// claudeQuestionAnswer validates the single application answer against the
// original question set, then returns Claude's input with a correlated answers
// map. A cancel or decline is represented by nil so the caller can deny the
// tool request without manufacturing an answer.
func claudeQuestionAnswer(input json.RawMessage, questions []claudeQuestion, answer json.RawMessage) (json.RawMessage, error) {
	if len(questions) < 1 || len(questions) > 4 {
		return nil, fmt.Errorf("invalid Claude question set")
	}
	original, err := claudeQuestionObject(input)
	if err != nil {
		return nil, fmt.Errorf("invalid original AskUserQuestion input: %w", err)
	}
	if err := claudeQuestionOnlyKeys(original, "questions"); err != nil {
		return nil, fmt.Errorf("invalid original AskUserQuestion input: %w", err)
	}
	_, originalSet, err := claudeQuestionForm("session", "tool", "request", json.RawMessage(`{}`), input)
	if err != nil {
		return nil, fmt.Errorf("invalid original AskUserQuestion input: %w", err)
	}
	if !claudeQuestionSetsEqual(originalSet, questions) {
		return nil, fmt.Errorf("original AskUserQuestion does not match question set")
	}
	response, err := claudeQuestionObject(answer)
	if err != nil {
		return nil, fmt.Errorf("invalid elicitation response: %w", err)
	}
	if err := claudeQuestionOnlyKeys(response, "action", "content"); err != nil {
		return nil, fmt.Errorf("invalid elicitation response: %w", err)
	}
	action, ok := response["action"].(string)
	if !ok {
		return nil, fmt.Errorf("elicitation response action must be a string")
	}
	if action == "cancel" || action == "decline" {
		if content, exists := response["content"]; exists {
			object, ok := content.(map[string]any)
			if !ok || len(object) != 0 {
				return nil, fmt.Errorf("declined elicitation response has answer content")
			}
		}
		return nil, nil
	}
	if action != "accept" {
		return nil, fmt.Errorf("unsupported elicitation response action")
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("accepted elicitation response requires content")
	}
	if len(content) > len(questions)*2 {
		return nil, fmt.Errorf("too many elicitation answer fields")
	}

	answers := make(map[string]string, len(questions))
	allowed := make(map[string]bool, len(questions)*2)
	for i, q := range questions {
		if len(q.Options) < 2 || len(q.Options) > 4 || q.Text == "" {
			return nil, fmt.Errorf("invalid Claude question %d", i+1)
		}
		key := fmt.Sprintf("question_%d", i)
		allowed[key], allowed[key+"_custom"] = true, true
		choiceValue, hasChoice := content[key]
		customValue, hasCustom := content[key+"_custom"]
		if !hasChoice && !hasCustom {
			continue
		}
		custom := ""
		if hasCustom {
			var err error
			custom, err = claudeQuestionString(customValue, claudeQuestionMaxText, false)
			if err != nil {
				return nil, fmt.Errorf("question %d custom answer: %w", i+1, err)
			}
		}
		var selected []string
		if hasChoice {
			if q.MultiSelect {
				values, ok := choiceValue.([]any)
				if !ok || len(values) > len(q.Options) {
					return nil, fmt.Errorf("question %d requires a bounded list of choices", i+1)
				}
				selected = make([]string, 0, len(values))
				seen := make(map[string]bool, len(values))
				for _, value := range values {
					label, ok := value.(string)
					if !ok || !claudeQuestionHasOption(q, label) || seen[label] {
						return nil, fmt.Errorf("question %d has an invalid or repeated choice", i+1)
					}
					seen[label] = true
					selected = append(selected, label)
				}
			} else {
				label, ok := choiceValue.(string)
				if !ok || !claudeQuestionHasOption(q, label) {
					return nil, fmt.Errorf("question %d has an invalid choice", i+1)
				}
				selected = []string{label}
			}
		}
		if !q.MultiSelect && hasChoice && hasCustom {
			return nil, fmt.Errorf("question %d cannot combine a single choice and custom text", i+1)
		}
		if q.MultiSelect {
			switch {
			case len(selected) == 0 && custom != "":
				answers[q.Text] = custom
			case len(selected) > 0 && custom != "":
				answers[q.Text] = strings.Join(selected, ", ") + ", " + strconv.Quote(custom)
			case len(selected) > 0:
				answers[q.Text] = strings.Join(selected, ", ")
			}
		} else if len(selected) == 1 {
			answers[q.Text] = selected[0]
		} else if custom != "" {
			answers[q.Text] = custom
		}
	}
	for key := range content {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported elicitation answer field %q", key)
		}
	}

	// Re-parse the original object to preserve its question descriptions and
	// other exact source values in Claude's updatedInput.
	originalQuestions, ok := original["questions"].([]any)
	if !ok {
		return nil, fmt.Errorf("original AskUserQuestion does not match question set")
	}

	updated := map[string]any{"questions": originalQuestions, "answers": answers}
	raw, err := json.Marshal(updated)
	if err != nil {
		return nil, fmt.Errorf("encode updated AskUserQuestion input: %w", err)
	}
	if len(raw) > claudeQuestionMaxJSON {
		return nil, fmt.Errorf("updated AskUserQuestion input exceeds bounds")
	}
	return raw, nil
}

func claudeQuestionHasOption(q claudeQuestion, label string) bool {
	for _, option := range q.Options {
		if option.Label == label {
			return true
		}
	}
	return false
}

func claudeQuestionSetsEqual(a, b []claudeQuestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || a[i].Header != b[i].Header || a[i].MultiSelect != b[i].MultiSelect || len(a[i].Options) != len(b[i].Options) {
			return false
		}
		for j := range a[i].Options {
			if a[i].Options[j] != b[i].Options[j] {
				return false
			}
		}
	}
	return true
}

func claudeQuestionOnlyKeys(object map[string]any, allowed ...string) error {
	for key := range object {
		found := false
		for _, candidate := range allowed {
			found = found || key == candidate
		}
		if !found {
			return fmt.Errorf("unsupported question field %q", key)
		}
	}
	return nil
}

func claudeQuestionString(value any, limit int, empty bool) (string, error) {
	text, ok := value.(string)
	if !ok || len(text) > limit || (!empty && strings.TrimSpace(text) == "") || !utf8.ValidString(text) || strings.ContainsRune(text, '\ufffd') {
		return "", fmt.Errorf("invalid question string")
	}
	for _, r := range text {
		if r == '\r' || r == '\u200b' || (unicode.IsControl(r) && r != '\n' && r != '\t') {
			return "", fmt.Errorf("unsafe question string")
		}
	}
	return text, nil
}

func claudeQuestionObject(raw json.RawMessage) (map[string]any, error) {
	return claudeQuestionParseObject(raw, true)
}

// Control source metadata may contain null-valued optional protocol fields.
// It still gets duplicate-key, syntax, UTF-8 and nesting checks.
func claudeQuestionObjectAllowNull(raw json.RawMessage) (map[string]any, error) {
	return claudeQuestionParseObject(raw, false)
}

func claudeQuestionParseObject(raw json.RawMessage, rejectNull bool) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > claudeQuestionMaxJSON || !utf8.Valid(raw) {
		return nil, fmt.Errorf("question JSON exceeds bounds or has invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	value, err := claudeQuestionReadJSON(dec, 0, rejectNull)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing question JSON")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected question object")
	}
	return object, nil
}

// Token-level decoding rejects duplicate object keys before a later value can
// shadow the question, choice or answer that the user saw.
func claudeQuestionReadJSON(dec *json.Decoder, depth int, rejectNull bool) (any, error) {
	if depth > claudeQuestionMaxDepth {
		return nil, fmt.Errorf("question JSON nesting exceeds limit")
	}
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := map[string]any{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("invalid question JSON key")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate question JSON key")
				}
				field, err := claudeQuestionReadJSON(dec, depth+1, rejectNull)
				if err != nil {
					return nil, err
				}
				object[key] = field
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			array := []any{}
			for dec.More() {
				item, err := claudeQuestionReadJSON(dec, depth+1, rejectNull)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return array, nil
		}
	case nil:
		if rejectNull {
			return nil, fmt.Errorf("null question JSON value")
		}
	}
	if text, ok := token.(string); ok && strings.ContainsRune(text, '\ufffd') {
		return nil, fmt.Errorf("question JSON contains a replacement character")
	}
	return token, nil
}
