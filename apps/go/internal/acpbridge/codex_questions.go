package acpbridge

import (
	"context"
	"encoding/json"
	"fmt"

	acp "github.com/coder/acp-go-sdk"
)

const CodexIdentity = "tui-go-codex " + codexVersion
const CodexQuestionDialect = "tui-go.codex-questions.v1"

// CodexQuestions pins the request_user_input shape from App Server 0.155.1.
// Blocking and continued-work forms retain their supplied execution mode.
type CodexQuestions struct {
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	ItemID     string          `json:"itemId"`
	IsBlocking bool            `json:"isBlocking"`
	Questions  []CodexQuestion `json:"questions"`
}

type CodexQuestion struct {
	ID       string                `json:"id"`
	Header   string                `json:"header"`
	Question string                `json:"question"`
	IsOther  bool                  `json:"isOther"`
	IsSecret bool                  `json:"isSecret"`
	Options  []CodexQuestionOption `json:"options"`
}

type CodexQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func DecodeCodexQuestions(raw json.RawMessage) (CodexQuestions, error) {
	var request CodexQuestions
	root, err := claudeQuestionObjectAllowNull(raw)
	if err != nil {
		return request, err
	}
	if err = claudeQuestionOnlyKeys(root, "threadId", "turnId", "itemId", "isBlocking", "autoResolutionMs", "questions"); err != nil {
		return request, err
	}
	if _, ok := root["isBlocking"].(bool); !ok || root["autoResolutionMs"] != nil {
		return request, fmt.Errorf("codex question requires an explicit mode and no automatic answer timeout")
	}
	for _, key := range []string{"threadId", "turnId", "itemId"} {
		if _, err = claudeQuestionString(root[key], 512, false); err != nil {
			return request, err
		}
	}
	items, ok := root["questions"].([]any)
	if !ok || len(items) < 1 || len(items) > 4 {
		return request, fmt.Errorf("expected 1–4 Codex questions")
	}
	seen := map[string]bool{}
	for _, item := range items {
		q, ok := item.(map[string]any)
		if !ok {
			return request, fmt.Errorf("invalid Codex question")
		}
		if err = claudeQuestionOnlyKeys(q, "id", "header", "question", "options", "isOther", "isSecret"); err != nil {
			return request, err
		}
		for _, key := range []string{"id", "header", "question"} {
			limit := 4096
			if key != "question" {
				limit = 256
			}
			if _, err = claudeQuestionString(q[key], limit, false); err != nil {
				return request, err
			}
		}
		id := q["id"].(string)
		if seen[id] {
			return request, fmt.Errorf("duplicate Codex question ID")
		}
		seen[id] = true
		for _, key := range []string{"isOther", "isSecret"} {
			if value, exists := q[key]; exists {
				if _, ok = value.(bool); !ok {
					return request, fmt.Errorf("invalid %s", key)
				}
			}
		}
		if q["isSecret"] == true {
			return request, fmt.Errorf("secret questions require a protected input surface")
		}
		if q["options"] == nil {
			continue
		}
		opts, ok := q["options"].([]any)
		if !ok || len(opts) < 1 || len(opts) > 12 {
			return request, fmt.Errorf("invalid Codex options")
		}
		labels := map[string]bool{}
		for _, option := range opts {
			o, ok := option.(map[string]any)
			if !ok {
				return request, fmt.Errorf("invalid Codex option")
			}
			if err = claudeQuestionOnlyKeys(o, "label", "description"); err != nil {
				return request, err
			}
			label, err := claudeQuestionString(o["label"], 512, false)
			if err != nil || labels[label] {
				return request, fmt.Errorf("invalid or duplicate option label")
			}
			labels[label] = true
			if _, err = claudeQuestionString(o["description"], 2048, true); err != nil {
				return request, err
			}
		}
	}
	err = json.Unmarshal(raw, &request)
	return request, err
}

func (b *codexBridge) userInput(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	req, err := DecodeCodexQuestions(raw)
	if err != nil {
		return nil, invalid(err)
	}
	if reqErr := b.waitForActive(ctx, req.ThreadID, req.TurnID); reqErr != nil {
		return nil, reqErr
	}
	questionCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.grantSequence++
	pendingID, epoch, session := b.grantSequence, b.approvalEpoch, b.sessionID
	b.pendingApprovals[pendingID] = cancel
	b.mu.Unlock()
	transferred := false
	defer func() {
		if !transferred {
			cancel()
			b.mu.Lock()
			delete(b.pendingApprovals, pendingID)
			b.mu.Unlock()
		}
	}()
	response, err := acp.SendRequest[json.RawMessage](b.h.conn, questionCtx, "elicitation/create", map[string]any{
		"sessionId": session, "toolCallId": req.ItemID, "source": json.RawMessage(raw),
		"_meta": map[string]any{"questionDialect": CodexQuestionDialect},
	})
	if err != nil {
		return nil, internal(err)
	}
	answers, err := codexQuestionAnswers(req, response)
	if err != nil {
		return nil, invalid(err)
	}
	guard, reqErr := b.guardApprovalChoice(ctx, questionCtx, cancel, pendingID, epoch, req.ThreadID, req.TurnID, &approvalChoice{})
	if reqErr != nil {
		return nil, reqErr
	}
	transferred = true
	return map[string]any{"answers": answers, codexGrantKey: guard.grantToken}, nil
}

func codexQuestionAnswers(req CodexQuestions, raw json.RawMessage) (map[string]any, error) {
	root, err := claudeQuestionObject(raw)
	if err != nil {
		return nil, err
	}
	if err = claudeQuestionOnlyKeys(root, "action", "content"); err != nil {
		return nil, err
	}
	if root["action"] != "accept" {
		return nil, fmt.Errorf("codex question was cancelled without submitting an answer")
	}
	content, ok := root["content"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing Codex answer content")
	}
	answers := map[string]any{}
	used := 0
	for i, q := range req.Questions {
		key := fmt.Sprintf("question_%d", i)
		values := []string{}
		if value, exists := content[key]; exists {
			label, err := claudeQuestionString(value, 4096, false)
			if err != nil {
				return nil, err
			}
			found := false
			for _, option := range q.Options {
				found = found || option.Label == label
			}
			if !found {
				return nil, fmt.Errorf("unknown Codex answer choice")
			}
			values = append(values, label)
			used++
		}
		if value, exists := content[key+"_custom"]; exists {
			if len(values) > 0 || len(q.Options) > 0 && !q.IsOther {
				return nil, fmt.Errorf("unsupported Codex custom answer")
			}
			text, err := claudeQuestionString(value, 4096, false)
			if err != nil {
				return nil, err
			}
			values = append(values, text)
			used++
		}
		answers[q.ID] = map[string]any{"answers": values}
	}
	if used != len(content) {
		return nil, fmt.Errorf("unknown Codex answer fields")
	}
	return answers, nil
}
