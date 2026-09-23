package acpbridge

import (
	"encoding/json"
	"math"
	"time"
)

const UsageDialect = "tui-go.usage.v1"

// providerUsageState holds one main-agent request observation, not accumulated
// turn/session tokens. Claude's assistant output count may be a placeholder, so
// only request input (including cache categories) represents this observation.
type providerUsageState struct {
	selected, actual string
	used, size       *int64
}

func usageUpdate(model, session, source, scope string, used, size *int64) map[string]any {
	m := map[string]any{"sessionUpdate": "tui_usage_update", "dialect": UsageDialect, "model": model, "sessionID": session, "source": source, "scope": scope, "reportedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	if used != nil {
		m["used"] = *used
	}
	if size != nil {
		m["size"] = *size
	}
	return m
}

func codexUsageUpdate(raw json.RawMessage, model string) map[string]any {
	var p struct {
		ThreadID   string `json:"threadId"`
		TokenUsage struct {
			Last struct {
				TotalTokens *int64 `json:"totalTokens"`
			} `json:"last"`
			ModelContextWindow *int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &p) != nil || p.TokenUsage.Last.TotalTokens == nil || *p.TokenUsage.Last.TotalTokens < 0 {
		return nil
	}
	size := p.TokenUsage.ModelContextWindow
	if size != nil && *size <= 0 {
		size = nil
	}
	return usageUpdate(model, p.ThreadID, "Codex thread/tokenUsage/updated", "last model request", p.TokenUsage.Last.TotalTokens, size)
}

func (c *claude) emitUsage(raw json.RawMessage) {
	c.mu.Lock()
	resolved := ""
	for _, model := range c.models {
		if model.Value == c.model {
			resolved = model.ResolvedModel
			break
		}
	}
	update := c.usage.update(raw, c.model, c.session, resolved)
	c.mu.Unlock()
	if update != nil {
		_ = c.h.update(c.h.ctx, c.session, update)
	}
}

func (s *providerUsageState) update(raw json.RawMessage, selected, session string, resolved ...string) map[string]any {
	var p struct {
		Type, Subtype string
		Message       struct {
			Model string
			Usage struct {
				Input       *int64 `json:"input_tokens"`
				CacheRead   *int64 `json:"cache_read_input_tokens"`
				CacheCreate *int64 `json:"cache_creation_input_tokens"`
			}
		}
		ModelUsage map[string]struct {
			ContextWindow  *int64 `json:"contextWindow"`
			CanonicalModel string `json:"canonicalModel"`
		} `json:"modelUsage"`
		TotalCost json.Number `json:"total_cost_usd"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if s.selected != selected {
		*s = providerUsageState{selected: selected}
	}
	// Compaction invalidates the previous request observation until another
	// assistant frame supplies a new input count.
	if p.Type == "system" && p.Subtype == "compact_boundary" {
		s.used = nil
		return usageUpdate(selected, session, "Claude compaction", "awaiting next request", nil, s.size)
	}
	if p.Type == "assistant" {
		if p.Message.Usage.Input == nil || *p.Message.Usage.Input < 0 {
			return nil
		}
		total := *p.Message.Usage.Input
		for _, n := range []*int64{p.Message.Usage.CacheRead, p.Message.Usage.CacheCreate} {
			if n != nil {
				if *n < 0 || *n > math.MaxInt64-total {
					return nil
				}
				total += *n
			}
		}
		if s.actual != p.Message.Model {
			s.size = nil
		}
		s.actual = p.Message.Model
		s.used = &total
		return usageUpdate(selected, session, "Claude assistant usage", "input at last model request", s.used, s.size)
	}
	if p.Type != "result" {
		return nil
	}
	// The assistant reports the canonical model while result keys may include
	// a context modifier (e.g. [1m]). Match supplied canonicalModel metadata,
	// preferring the selected/resolved key; never take the max across models.
	keys := []string{selected}
	if len(resolved) > 0 && resolved[0] != "" {
		keys = append(keys, resolved[0])
	}
	matched := false
	for _, key := range keys {
		if u, ok := p.ModelUsage[key]; ok && u.ContextWindow != nil && *u.ContextWindow > 0 && (key == s.actual || u.CanonicalModel == s.actual) {
			s.size = u.ContextWindow
			matched = true
			break
		}
	}
	if !matched {
		var capacity *int64
		matches := 0
		for key, u := range p.ModelUsage {
			if u.ContextWindow != nil && *u.ContextWindow > 0 && (key == s.actual || u.CanonicalModel == s.actual) {
				capacity = u.ContextWindow
				matches++
			}
		}
		if matches == 1 {
			s.size = capacity
		}
	}
	update := usageUpdate(selected, session, "Claude result", "input at last model request", s.used, s.size)
	if p.TotalCost != "" {
		value, err := p.TotalCost.Float64()
		if err == nil && value >= 0 && !math.IsInf(value, 0) {
			update["cost"] = map[string]any{"amount": p.TotalCost, "currency": "USD", "estimated": true}
		}
	}
	if s.used == nil && s.size == nil && update["cost"] == nil {
		return nil
	}
	return update
}
