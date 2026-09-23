package acpbridge

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type claudeQuestionFixture struct {
	Adapter string `json:"adapter"`
	Samples []struct {
		Name          string          `json:"name"`
		Request       json.RawMessage `json:"request"`
		Response      json.RawMessage `json:"response"`
		AdapterResult struct {
			UpdatedInput json.RawMessage `json:"updatedInput"`
		} `json:"adapterResult"`
	} `json:"samples"`
}

func TestClaudeQuestionPinnedAdapterFixtures(t *testing.T) {
	raw, err := os.ReadFile("../agent/testdata/claude-questions-0.80.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture claudeQuestionFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Adapter != "@agentclientprotocol/claude-agent-acp 0.80.0" || len(fixture.Samples) != 3 {
		t.Fatalf("unexpected fixture version or sample count: %q / %d", fixture.Adapter, len(fixture.Samples))
	}
	control := json.RawMessage(`{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"fixture-tool","decision_reason":null}`)
	for _, sample := range fixture.Samples {
		t.Run(sample.Name, func(t *testing.T) {
			input := fixtureInput(t, sample.AdapterResult.UpdatedInput)
			form, questions, err := claudeQuestionForm("fixture-session", "fixture-tool", "fixture-request", control, input)
			if err != nil {
				t.Fatal(err)
			}
			actualForm := fixtureValue(t, form)
			actualForm.(map[string]any)["_meta"] = nil
			delete(actualForm.(map[string]any), "_meta")
			expectedForm := fixtureValue(t, sample.Request)
			if !reflect.DeepEqual(actualForm, expectedForm) {
				t.Fatalf("form mismatch\n got: %#v\nwant: %#v", actualForm, expectedForm)
			}
			meta := fixtureValue(t, form).(map[string]any)["_meta"].(map[string]any)
			if meta["questionDialect"] != QuestionDialect || meta["requestId"] != "fixture-request" || !reflect.DeepEqual(meta["source"], fixtureValue(t, control)) {
				t.Fatalf("unexpected request metadata: %#v", meta)
			}

			updated, err := claudeQuestionAnswer(input, questions, sample.Response)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := fixtureValue(t, updated), fixtureValue(t, sample.AdapterResult.UpdatedInput); !reflect.DeepEqual(got, want) {
				t.Fatalf("updated input mismatch\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestClaudeQuestionInputBoundsAndAmbiguity(t *testing.T) {
	base := `{"questions":[{"question":"Which layout?","header":"Layout","options":[{"label":"Compact"},{"label":"Roomy"}],"multiSelect":false}]}`
	cases := map[string]string{
		"unknown meaningful field": strings.Replace(base, `"question":"Which layout?"`, `"preview":"sample","question":"Which layout?"`, 1),
		"root answers":             strings.TrimSuffix(base, "}") + `,"answers":{}}`,
		"missing multiselect":      strings.Replace(base, `,"multiSelect":false`, "", 1),
		"bad option count":         `{"questions":[{"question":"Q","header":"H","options":[{"label":"Only"}],"multiSelect":false}]}`,
		"duplicate option label":   `{"questions":[{"question":"Q","header":"H","options":[{"label":"Same"},{"label":"Same"}],"multiSelect":false}]}`,
		"invalid multiSelect":      `{"questions":[{"question":"Q","header":"H","options":[{"label":"A"},{"label":"B"}],"multiSelect":"false"}]}`,
		"duplicate JSON key":       strings.Replace(base, `"header":"Layout"`, `"header":"Layout","header":"Second"`, 1),
		"unsafe control":           strings.Replace(base, "Which layout?", "Which\\u001b layout?", 1),
		"oversized":                `{"questions":[{"question":"` + strings.Repeat("x", claudeQuestionMaxText+1) + `","header":"Layout","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`,
	}
	// Keep a distinct question-text duplicate case with two otherwise valid
	// question objects so it reaches the duplicate check.
	cases["duplicate question text"] = `{"questions":[{"question":"Same?","header":"One","options":[{"label":"A"},{"label":"B"}],"multiSelect":false},{"question":"Same?","header":"Two","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := claudeQuestionForm("session", "tool", "request", json.RawMessage(`{}`), json.RawMessage(input)); err == nil {
				t.Fatal("accepted invalid AskUserQuestion input")
			}
		})
	}
	if _, _, err := claudeQuestionForm("session", "tool", "request", json.RawMessage(`{"a":null,"a":null}`), json.RawMessage(base)); err == nil {
		t.Fatal("accepted duplicate control request keys")
	}
}

func TestClaudeQuestionResponseValidationAndCancellation(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Which layout?","header":"Layout","options":[{"label":"Compact"},{"label":"Roomy"}],"multiSelect":false}]}`)
	_, questions, err := claudeQuestionForm("session", "tool", "request", json.RawMessage(`{}`), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{`{"action":"cancel"}`, `{"action":"decline"}`, `{"action":"cancel","content":{}}`, `{"action":"decline","content":{}}`} {
		updated, err := claudeQuestionAnswer(input, questions, json.RawMessage(response))
		if err != nil || updated != nil {
			t.Fatalf("%s => %s, %v", response, updated, err)
		}
	}
	for name, response := range map[string]string{
		"unknown action":       `{"action":"retry","content":{}}`,
		"unknown answer key":   `{"action":"accept","content":{"question_0":"Compact","extra":"ignored"}}`,
		"unknown root field":   `{"action":"accept","content":{},"metadata":{}}`,
		"missing content":      `{"action":"accept"}`,
		"wrong choice":         `{"action":"accept","content":{"question_0":"Other"}}`,
		"wrong answer type":    `{"action":"accept","content":{"question_0":["Compact"]}}`,
		"single plus custom":   `{"action":"accept","content":{"question_0":"Compact","question_0_custom":"Note"}}`,
		"empty custom":         `{"action":"accept","content":{"question_0_custom":"  "}}`,
		"decline with answers": `{"action":"decline","content":{"question_0":"Compact"}}`,
		"duplicate JSON key":   `{"action":"accept","action":"cancel","content":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := claudeQuestionAnswer(input, questions, json.RawMessage(response)); err == nil {
				t.Fatalf("accepted invalid response %s", response)
			}
		})
	}
}

func TestClaudeQuestionAnswerRequiresMatchingInput(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Which layout?","header":"Layout","options":[{"label":"Compact"},{"label":"Roomy"}],"multiSelect":false}]}`)
	_, questions, err := claudeQuestionForm("session", "tool", "request", json.RawMessage(`{}`), input)
	if err != nil {
		t.Fatal(err)
	}
	changed := json.RawMessage(strings.Replace(string(input), "Compact", "Small", 1))
	if _, err := claudeQuestionAnswer(changed, questions, json.RawMessage(`{"action":"accept","content":{"question_0":"Compact"}}`)); err == nil {
		t.Fatal("paired response with a changed question set")
	}
}

func fixtureInput(t *testing.T, updated json.RawMessage) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(updated, &object); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]json.RawMessage{"questions": object["questions"]})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func fixtureValue(t *testing.T, raw any) any {
	t.Helper()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
