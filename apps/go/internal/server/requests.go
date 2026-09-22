package server

import "github.com/muschterm/tui/apps/go/internal/protocol"

// approvalChoice accepts an opaque option identity or an unambiguous legacy
// label. Duplicate display labels never select a broader permission by accident.
func approvalChoice(r protocol.Request, c protocol.Command) (int, error) {
	if c.QuestionAnswers != nil || (c.ApprovalChoiceID != "" && c.Answers != nil) || (c.ApprovalChoiceID == "" && len(c.Answers) != 1) {
		return -1, failure("invalid", "choose one approval response")
	}
	if len(r.ChoiceIDs) != 0 && len(r.ChoiceIDs) != len(r.Choices) {
		return -1, failure("invalid", "approval option identities are incomplete")
	}
	found := -1
	for i, label := range r.Choices {
		match := c.ApprovalChoiceID == "" && label == c.Answers[0]
		if c.ApprovalChoiceID != "" {
			match = i < len(r.ChoiceIDs) && r.ChoiceIDs[i] == c.ApprovalChoiceID
		}
		if !match {
			continue
		}
		if found != -1 {
			return -1, failure("invalid", "approval choice is ambiguous; select its option identity")
		}
		found = i
	}
	if found == -1 {
		return -1, failure("invalid", "unsupported approval choice")
	}
	return found, nil
}
