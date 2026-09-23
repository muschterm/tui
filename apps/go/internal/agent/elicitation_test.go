package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func claudeQuestionFixture(multiple, custom bool) map[string]any {
	opts := []any{map[string]any{"const": "First", "title": "First", "description": "First detail"}, map[string]any{"const": "Second", "title": "Second"}}
	field := map[string]any{"type": "string", "title": "Destination", "oneOf": opts}
	desc := "Type your own answer, or add a note to the option you chose above (optional)."
	if multiple {
		field = map[string]any{"type": "array", "title": "Destination", "items": map[string]any{"anyOf": opts}}
		desc = "Type your own answer to add to your selection above (optional)."
	}
	props := map[string]any{"question_0": field}
	if custom {
		props["question_0_custom"] = map[string]any{"type": "string", "title": "Other", "description": desc, "_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"questionId": "question_0", "isCustomAnswer": true}}}
	}
	return map[string]any{"mode": "form", "sessionId": "session-1", "toolCallId": "tool-1", "message": "Where should it go?", "requestedSchema": map[string]any{"type": "object", "properties": props}}
}
func claudeQuestionRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func claudeProperties(r map[string]any) map[string]any {
	return r["requestedSchema"].(map[string]any)["properties"].(map[string]any)
}

func TestClaudeQuestionsRoundTrip(t *testing.T) {
	for _, multi := range []bool{false, true} {
		raw := claudeQuestionRaw(t, claudeQuestionFixture(multi, true))
		form, err := ParseClaudeQuestions("req", raw)
		if err != nil {
			t.Fatal(err)
		}
		if form.SessionID != "session-1" || form.ToolCallID != "tool-1" || form.Request.Mode != "blocking" || form.Request.DeliveryRoute != "native-response" || string(form.Request.SourcePayload) != string(raw) {
			t.Fatalf("bad form: %+v", form)
		}
		q := form.Request.Questions[0]
		if q.ID != "question_0" || protocol.QuestionRequired(q) || !q.AllowOther || q.Text != "Where should it go?" || !reflect.DeepEqual(q.OptionDescriptions, []string{"First detail", ""}) || !reflect.DeepEqual(form.Request.Actions, []string{"decline", "cancel"}) {
			t.Fatalf("question: %+v", q)
		}
		answer := protocol.Answer{Choices: []string{"Second"}}
		var expected any = "Second"
		if multi {
			answer = protocol.Answer{Choices: []string{"Second", "First"}, Text: "Third, fourth"}
			expected = []string{"Second", "First"}
		}
		response, err := form.Response([]protocol.Answer{answer})
		if err != nil {
			t.Fatal(err)
		}
		content := response["content"].(map[string]any)
		if response["action"] != "accept" || !reflect.DeepEqual(content["question_0"], expected) {
			t.Fatalf("response: %+v", response)
		}
		if multi && content["question_0_custom"] != "Third, fourth" {
			t.Fatalf("custom: %+v", content)
		}
		for _, a := range []protocol.Answer{{Text: "Own answer"}, {}} {
			response, err = form.Response([]protocol.Answer{a})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{}
			if a.Text != "" {
				want["question_0_custom"] = a.Text
			}
			if !reflect.DeepEqual(response["content"], want) {
				t.Fatalf("response: %+v", response)
			}
		}
		raw[0] = 'x'
		if form.Request.SourcePayload[0] != '{' {
			t.Fatal("raw alias")
		}
	}
}

func TestClaudeQuestionsMultipleQuestions(t *testing.T) {
	root := claudeQuestionFixture(false, false)
	props := claudeProperties(root)
	props["question_0"].(map[string]any)["description"] = "First question?"
	second := claudeProperties(claudeQuestionFixture(true, false))["question_0"].(map[string]any)
	second["description"] = "Second question?"
	props["question_1"] = second
	root["message"] = "Please answer the following questions."
	form, err := ParseClaudeQuestions("id", claudeQuestionRaw(t, root))
	if err != nil {
		t.Fatal(err)
	}
	response, err := form.Response([]protocol.Answer{{}, {Choices: []string{"First"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response["content"], map[string]any{"question_1": []string{"First"}}) {
		t.Fatalf("response: %+v", response)
	}
	second["description"] = "First question?"
	if _, err = ParseClaudeQuestions("id", claudeQuestionRaw(t, root)); err == nil {
		t.Fatal("duplicate question accepted")
	}
}

func TestClaudeQuestionsRejectUnsupported(t *testing.T) {
	cases := map[string]func(map[string]any){
		"url":           func(r map[string]any) { r["mode"] = "url" },
		"missing tool":  func(r map[string]any) { delete(r, "toolCallId") },
		"empty session": func(r map[string]any) { r["sessionId"] = "" },
		"unknown root":  func(r map[string]any) { r["url"] = "https://example.test" },
		"general form": func(r map[string]any) {
			p := claudeProperties(r)
			p["choice"] = p["question_0"]
			delete(p, "question_0")
		},
		"required":   func(r map[string]any) { r["requestedSchema"].(map[string]any)["required"] = []any{"question_0"} },
		"constraint": func(r map[string]any) { claudeProperties(r)["question_0"].(map[string]any)["minLength"] = 1 },
		"duplicate option": func(r map[string]any) {
			f := claudeProperties(r)["question_0"].(map[string]any)
			f["oneOf"].([]any)[1] = f["oneOf"].([]any)[0]
		},
		"different title": func(r map[string]any) {
			claudeProperties(r)["question_0"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)["title"] = "Different"
		},
		"preview": func(r map[string]any) {
			claudeProperties(r)["question_0"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)["_meta"] = map[string]any{"preview": "code"}
		},
		"few options": func(r map[string]any) {
			f := claudeProperties(r)["question_0"].(map[string]any)
			f["oneOf"] = f["oneOf"].([]any)[:1]
		},
		"many options": func(r map[string]any) {
			f := claudeProperties(r)["question_0"].(map[string]any)
			f["oneOf"] = append(f["oneOf"].([]any), nil, nil, nil)
		},
		"null":          func(r map[string]any) { r["message"] = nil },
		"unsafe":        func(r map[string]any) { r["message"] = "Question\x1b[31m" },
		"long":          func(r map[string]any) { r["message"] = strings.Repeat("x", 4097) },
		"orphan custom": func(r map[string]any) { p := claudeProperties(r); p["question_1_custom"] = p["question_0_custom"] },
		"wrong custom": func(r map[string]any) {
			claudeProperties(r)["question_0_custom"].(map[string]any)["_meta"].(map[string]any)["_askUserQuestionCustomAnswer"].(map[string]any)["questionId"] = "question_1"
		},
		"single description": func(r map[string]any) {
			claudeProperties(r)["question_0"].(map[string]any)["description"] = "Another question"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := claudeQuestionFixture(false, true)
			mutate(r)
			if _, err := ParseClaudeQuestions("id", claudeQuestionRaw(t, r)); err == nil {
				t.Fatal("accepted unsupported form")
			}
		})
	}
	raw := string(claudeQuestionRaw(t, claudeQuestionFixture(false, true)))
	for name, bad := range map[string]string{
		"duplicate root":   strings.Replace(raw, `"mode":"form"`, `"mode":"url","mode":"form"`, 1),
		"duplicate nested": strings.Replace(raw, `"const":"First"`, `"const":"First","const":"Second"`, 1),
		"malformed":        raw[:len(raw)-1], "trailing": raw + `{}`, "oversized": strings.Repeat(" ", 65537),
		"bad surrogate": strings.Replace(raw, "Where should it go?", `\ud800`, 1),
		"invalid UTF8":  strings.Replace(raw, "Where should it go?", string([]byte{0xff}), 1),
		"deep":          strings.Repeat("[", 18) + strings.Repeat("]", 18),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseClaudeQuestions("id", json.RawMessage(bad)); err == nil {
				t.Fatal("accepted malformed form")
			}
		})
	}
}

func TestClaudeQuestionResponseRejectsInvalidAnswers(t *testing.T) {
	for _, custom := range []bool{false, true} {
		form, err := ParseClaudeQuestions("id", claudeQuestionRaw(t, claudeQuestionFixture(false, custom)))
		if err != nil {
			t.Fatal(err)
		}
		answers := [][]protocol.Answer{nil, {{Choices: []string{"Unknown"}}}, {{Choices: []string{"First", "Second"}}}, {{Choices: []string{"First", "First"}}}, {{Choices: []string{"First"}, Text: "note"}}, {{Text: strings.Repeat("x", 4097)}}, {{Text: "unsafe\rtext"}}}
		if !custom {
			answers = append(answers, []protocol.Answer{{Text: "Other"}})
		}
		for _, answer := range answers {
			if _, err = form.Response(answer); err == nil {
				t.Fatalf("accepted %+v", answer)
			}
		}
		form.Request.Questions[0].Options[0] = "Unknown"
		if _, err = form.Response([]protocol.Answer{{Choices: []string{"Unknown"}}}); err == nil {
			t.Fatal("public mutation changed wire contract")
		}
	}
}

func TestClaudeQuestionsExactWireValues(t *testing.T) {
	root := claudeQuestionFixture(true, false)
	field := claudeProperties(root)["question_0"].(map[string]any)
	opts := field["items"].(map[string]any)["anyOf"].([]any)
	values := []string{`Redis, not Memcached`, `"SQLite" 日本語`}
	for i, value := range values {
		option := opts[i].(map[string]any)
		option["const"] = value
		option["title"] = value
	}
	form, err := ParseClaudeQuestions("id", claudeQuestionRaw(t, root))
	if err != nil {
		t.Fatal(err)
	}
	response, err := form.Response([]protocol.Answer{{Choices: values}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response["content"], map[string]any{"question_0": values}) {
		t.Fatalf("wire values changed: %+v", response)
	}
}

func TestClaudeQuestionsQuestionCountBounds(t *testing.T) {
	for _, count := range []int{0, 4, 5} {
		root := claudeQuestionFixture(false, false)
		root["message"] = "Please answer the following questions."
		props := claudeProperties(root)
		delete(props, "question_0")
		for i := 0; i < count; i++ {
			field := claudeProperties(claudeQuestionFixture(false, false))["question_0"].(map[string]any)
			key := fmt.Sprintf("question_%d", i)
			field["description"] = key + "?"
			props[key] = field
		}
		_, err := ParseClaudeQuestions("id", claudeQuestionRaw(t, root))
		if (err == nil) != (count == 4) {
			t.Fatalf("count %d: %v", count, err)
		}
	}
}

func TestClaudeQuestionDeclineAndCancelCarryNoContent(t *testing.T) {
	form, err := ParseClaudeQuestions("req", claudeQuestionRaw(t, claudeQuestionFixture(false, true)))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"decline", "cancel"} {
		response, err := form.Respond(action, nil)
		if err != nil || !reflect.DeepEqual(response, map[string]any{"action": action}) {
			t.Fatalf("%s => %+v, %v", action, response, err)
		}
	}
	if _, err := form.ActionResponse("accept"); err == nil {
		t.Fatal("accept became an answerless action")
	}
	if _, err := form.ActionResponse("retry"); err == nil {
		t.Fatal("unknown action accepted")
	}
	form.Request.Actions = []string{"decline"}
	if _, err := form.ActionResponse("cancel"); err == nil {
		t.Fatal("cancel sent although only decline was offered")
	}
	// Options without any description stay without the parallel slice.
	plain := claudeQuestionFixture(false, false)
	plain["requestedSchema"].(map[string]any)["properties"].(map[string]any)["question_0"].(map[string]any)["oneOf"] = []any{map[string]any{"const": "A", "title": "A"}, map[string]any{"const": "B", "title": "B"}}
	form, err = ParseClaudeQuestions("req", claudeQuestionRaw(t, plain))
	if err != nil || form.Request.Questions[0].OptionDescriptions != nil {
		t.Fatalf("undescribed options gained descriptions: %+v %v", form, err)
	}
}
