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
