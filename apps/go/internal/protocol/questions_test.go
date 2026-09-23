package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateAnswer(t *testing.T) {
	optional := false
	for _, tc := range []struct {
		name  string
		q     Question
		a     Answer
		valid bool
	}{
		{"single", Question{Kind: "single", Options: []string{"A", "B"}}, Answer{Choices: []string{"A"}}, true},
		{"multiple", Question{Kind: "multiple", Options: []string{"A", "B"}, AllowOther: true}, Answer{Choices: []string{"A", "B"}, Text: "C"}, true},
		{"unknown", Question{Options: []string{"A"}}, Answer{Choices: []string{"B"}}, false},
		{"duplicate", Question{Kind: "multiple", Options: []string{"A"}}, Answer{Choices: []string{"A", "A"}}, false},
		{"single multiple", Question{Options: []string{"A", "B"}}, Answer{Choices: []string{"A", "B"}}, false},
		{"single plus text", Question{Options: []string{"A"}}, Answer{Choices: []string{"A"}, Text: "B"}, false},
		{"text choice", Question{Kind: "text", Options: []string{"A"}}, Answer{Choices: []string{"A"}}, false},
		{"required", Question{}, Answer{}, false},
		{"optional", Question{Required: &optional}, Answer{}, true},
		{"whitespace", Question{}, Answer{Text: "  "}, false},
		{"text", Question{}, Answer{Text: "details"}, true},
		{"other disallowed", Question{Kind: "single", Options: []string{"A"}}, Answer{Text: "B"}, false},
		{"legacy other", Question{Options: []string{"A"}}, Answer{Text: "B"}, true},
		{"unknown kind", Question{Kind: "mystery"}, Answer{Text: "B"}, false},
		{"byte limit", Question{}, Answer{Text: strings.Repeat("é", 2049)}, false},
		{"aggregate limit", Question{Kind: "multiple", Options: []string{strings.Repeat("a", 4096)}, AllowOther: true}, Answer{Choices: []string{strings.Repeat("a", 4096)}, Text: "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateAnswer(tc.q, tc.a); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestNormalizeQuestionAnswers(t *testing.T) {
	qs := []Question{{Options: []string{"A"}}, {Options: []string{"B"}}, {}}
	got, err := NormalizeQuestionAnswers(qs, nil, []string{"A", "custom", "text"})
	if err != nil || got[0].Choices[0] != "A" || got[1].Text != "custom" || got[2].Text != "text" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := NormalizeQuestionAnswers(qs, got, []string{}); err == nil {
		t.Fatal("mixed input accepted")
	}
	if _, err := NormalizeQuestionAnswers(qs, got[:2], nil); err == nil {
		t.Fatal("missing answer accepted")
	}
	copied, err := NormalizeQuestionAnswers(qs, got, nil)
	if err != nil {
		t.Fatal(err)
	}
	got[0].Choices[0] = "changed"
	if copied[0].Choices[0] != "A" {
		t.Fatal("answer aliases command")
	}
}

// Storage uses marshaled commands for idempotency; adding question support must
// preserve the exact encoding of legacy submissions across application upgrades.
func TestLegacyCommandEncodingUnchanged(t *testing.T) {
	got, err := json.Marshal(Command{Version: 1, ID: "legacy", Kind: "request.answer", Answers: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Version":1,"ID":"legacy","Kind":"request.answer","ThreadID":"","TargetID":"","ClientID":"","Text":"","Revision":0,"Settings":null,"Attachments":null,"Order":null,"Answers":["A"]}`
	if string(got) != want {
		t.Fatalf("legacy command identity changed: %s", got)
	}
}

func TestOptionDescriptionsParallelOptions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		descriptions []string
		ok           bool
	}{
		{"absent", nil, true},
		{"parallel", []string{"First detail", ""}, true},
		{"short", []string{"First detail"}, false},
		{"long", []string{"a", "b", "c"}, false},
	} {
		q := Question{Kind: "single", Options: []string{"A", "B"}, OptionDescriptions: tc.descriptions}
		if err := ValidateQuestion(q); (err == nil) != tc.ok {
			t.Fatalf("%s: ValidateQuestion = %v", tc.name, err)
		}
		if err := ValidateAnswer(q, Answer{Choices: []string{"A"}}); (err == nil) != tc.ok {
			t.Fatalf("%s: ValidateAnswer = %v", tc.name, err)
		}
	}
}

func TestRequestActionNormalization(t *testing.T) {
	r := Request{Kind: "question", Actions: []string{RequestActionDecline, RequestActionCancel}, Questions: []Question{{Options: []string{"A"}}}}
	for _, action := range []string{RequestActionDecline, RequestActionCancel} {
		if err := ValidateRequestAction(r, action, nil, nil); err != nil {
			t.Fatalf("%s rejected: %v", action, err)
		}
		// Empty answer slices are equivalent to none; any answer is refused.
		if err := ValidateRequestAction(r, action, []Answer{}, []string{}); err != nil {
			t.Fatalf("%s with empty answers rejected: %v", action, err)
		}
		if err := ValidateRequestAction(r, action, []Answer{{Choices: []string{"A"}}}, nil); err == nil {
			t.Fatalf("%s carried structured answers", action)
		}
		if err := ValidateRequestAction(r, action, nil, []string{"A"}); err == nil {
			t.Fatalf("%s carried legacy answers", action)
		}
	}
	for name, tc := range map[string]struct {
		r      Request
		action string
	}{
		"accept":       {r, "accept"},
		"unknown":      {r, "retry"},
		"not offered":  {Request{Kind: "question", Actions: []string{RequestActionDecline}}, RequestActionCancel},
		"none offered": {Request{Kind: "question"}, RequestActionDecline},
		"duplicate":    {Request{Kind: "question", Actions: []string{RequestActionDecline, RequestActionDecline}}, RequestActionDecline},
		"approval":     {Request{Kind: "approval", Actions: []string{RequestActionDecline}}, RequestActionDecline},
	} {
		if err := ValidateRequestAction(tc.r, tc.action, nil, nil); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
