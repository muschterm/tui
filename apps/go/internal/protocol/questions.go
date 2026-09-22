package protocol

import (
	"fmt"
	"strings"
)

// QuestionKind resolves the legacy shape while preserving unknown explicit kinds
// for validation rather than silently changing their meaning.
func QuestionKind(q Question) string {
	if q.Kind != "" {
		return q.Kind
	}
	if len(q.Options) != 0 {
		return "single"
	}
	return "text"
}

// QuestionRequired reports whether q needs an answer; unset means required.
func QuestionRequired(q Question) bool { return q.Required == nil || *q.Required }

// QuestionAllowsOther reports whether a choice question also accepts free text.
func QuestionAllowsOther(q Question) bool {
	return q.AllowOther || (q.Kind == "" && len(q.Options) != 0)
}

// ValidateAnswer validates one draft without mutating or submitting it. Choices
// contain exact supplied option values; Text carries open text or an Other answer.
// The combined answer is bounded to 4096 bytes, including all selected values.
func ValidateAnswer(q Question, a Answer) error {
	kind := QuestionKind(q)
	if kind != "single" && kind != "multiple" && kind != "text" {
		return fmt.Errorf("unsupported question kind %q", kind)
	}
	bytes := len(a.Text)
	seen := make(map[string]bool, len(a.Choices))
	for _, choice := range a.Choices {
		bytes += len(choice)
		if seen[choice] {
			return fmt.Errorf("duplicate choice")
		}
		seen[choice] = true
		found := false
		for _, option := range q.Options {
			found = found || choice == option
		}
		if !found {
			return fmt.Errorf("unknown choice")
		}
	}
	if bytes > 4096 {
		return fmt.Errorf("answer exceeds 4096 bytes")
	}
	hasText := strings.TrimSpace(a.Text) != ""
	if a.Text != "" && !hasText {
		return fmt.Errorf("text answer is blank")
	}
	switch kind {
	case "text":
		if len(a.Choices) != 0 {
			return fmt.Errorf("text question cannot receive choices")
		}
	case "single":
		if len(a.Choices) > 1 || (len(a.Choices) != 0 && hasText) {
			return fmt.Errorf("choose one option or supply Other text")
		}
		fallthrough
	case "multiple":
		if hasText && !QuestionAllowsOther(q) {
			return fmt.Errorf("this question does not allow Other text")
		}
	}
	if QuestionRequired(q) && len(a.Choices) == 0 && !hasText {
		return fmt.Errorf("answer is required")
	}
	return nil
}

// NormalizeQuestionAnswers validates an entire response in question order and
// returns owned answer data. A nil structured slice selects legacy string input.
// Legacy choice strings become single choices; other strings become Text, so
// older saved questions retain their free-text behavior. Inputs cannot be mixed.
func NormalizeQuestionAnswers(questions []Question, structured []Answer, legacy []string) ([]Answer, error) {
	if structured != nil && legacy != nil {
		return nil, fmt.Errorf("supply structured or legacy answers, not both")
	}
	if structured == nil {
		if len(legacy) != len(questions) {
			return nil, fmt.Errorf("answer every question")
		}
		structured = make([]Answer, len(questions))
		for i, q := range questions {
			structured[i].Text = legacy[i]
			if QuestionKind(q) != "text" {
				for _, option := range q.Options {
					if legacy[i] == option {
						structured[i] = Answer{Choices: []string{option}}
						break
					}
				}
			}
		}
	}
	if len(structured) != len(questions) {
		return nil, fmt.Errorf("answer every question")
	}
	result := make([]Answer, len(structured))
	for i, a := range structured {
		if err := ValidateAnswer(questions[i], a); err != nil {
			return nil, fmt.Errorf("question %d: %w", i+1, err)
		}
		result[i] = Answer{Choices: append([]string(nil), a.Choices...), Text: a.Text}
	}
	return result, nil
}
