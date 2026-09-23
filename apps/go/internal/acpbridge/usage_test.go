package acpbridge

import (
	"encoding/json"
	"testing"
)

func TestCodexUsageUsesLastRequestNeverCumulativeTotal(t *testing.T) {
	u := codexUsageUpdate(json.RawMessage(`{"threadId":"t","tokenUsage":{"last":{"totalTokens":150},"total":{"totalTokens":99000},"modelContextWindow":1000}}`), "model")
	if u["used"] != int64(150) || u["size"] != int64(1000) || u["model"] != "model" {
		t.Fatalf("%+v", u)
	}
	partial := codexUsageUpdate(json.RawMessage(`{"threadId":"t","tokenUsage":{"last":{"totalTokens":0},"modelContextWindow":null}}`), "model")
	if partial["used"] != int64(0) || partial["size"] != nil {
		t.Fatalf("partial %+v", partial)
	}
}

func TestClaudeUsageCountsOnlyLatestMainRequestInputAndMatchingCapacity(t *testing.T) {
	var s providerUsageState
	u := s.update(json.RawMessage(`{"type":"assistant","message":{"model":"actual","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":999}}}`), "selected", "s")
	if u["used"] != int64(60) || u["size"] != nil {
		t.Fatalf("%+v", u)
	}
	u = s.update(json.RawMessage(`{"type":"result","usage":{"input_tokens":999999},"modelUsage":{"other":{"contextWindow":999999},"actual":{"contextWindow":1000}},"total_cost_usd":0.012345}`), "selected", "s")
	if u["used"] != int64(60) || u["size"] != int64(1000) {
		t.Fatalf("%+v", u)
	}
	if cost := u["cost"].(map[string]any); cost["amount"].(json.Number) != "0.012345" || cost["estimated"] != true {
		t.Fatal(cost)
	}
	u = s.update(json.RawMessage(`{"type":"system","subtype":"compact_boundary"}`), "selected", "s")
	if u["used"] != nil {
		t.Fatal("compaction retained stale request")
	}
	u = s.update(json.RawMessage(`{"type":"assistant","message":{"model":"new","usage":{"input_tokens":5}}}`), "new", "s")
	if u["used"] != int64(5) || u["size"] != nil {
		t.Fatal("model change retained prior capacity")
	}
}

func TestClaudeUnavailableFastKeepsExplainedStandardOption(t *testing.T) {
	c := &claude{models: []claudeModel{{Value: "opus", Name: "Opus", SupportsFastMode: true}}, model: "opus", permissionMode: "default", fastUnavailable: "extra_usage_disabled"}
	raw, _ := json.Marshal(c.options())
	var options []struct {
		ID, Description string
		Options         []struct{ Value string }
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range options {
		if o.ID == "speed" {
			found = true
			if o.Description != "Fast unavailable · extra usage disabled" || len(o.Options) != 1 || o.Options[0].Value != "standard" {
				t.Fatalf("unavailable Fast misrepresented %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("speed restriction was silently hidden")
	}
}

func TestClaudeCapacityUsesReportedCanonicalIdentityAndSelectedContext(t *testing.T) {
	var s providerUsageState
	s.update(json.RawMessage(`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2000}}}`), "default", "s")
	result := json.RawMessage(`{"type":"result","modelUsage":{"claude-opus-5-5":{"contextWindow":200000},"claude-opus-5-5[1m]":{"contextWindow":1000000,"canonicalModel":"claude-opus-5-5"},"other":{"contextWindow":2000000}}}`)
	u := s.update(result, "default", "s", "claude-opus-5-5[1m]")
	if u["size"] != int64(1000000) {
		t.Fatalf("wrong active capacity %+v", u)
	}
	var unknown providerUsageState
	unknown.update(json.RawMessage(`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2000}}}`), "unknown-alias", "s")
	if u := unknown.update(result, "unknown-alias", "s"); u["size"] != nil {
		t.Fatal("ambiguous capacity inferred", u)
	}
}
