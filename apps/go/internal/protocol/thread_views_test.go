package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPruneThreadViewPreservesOtherData(t *testing.T) {
	input := json.RawMessage(`{"Active":"gone","Threads":{"gone":{"Draft":"secret"},"live": { "Draft":"retain", "future":9007199254740993 }},"Edit":{"ThreadID":"gone","OldDraft":"secret"},"Pending":{"ThreadID":"gone","Text":"secret"},"PendingAction":{"Kind":"send","Value":"secret"},"Future": { "x" : "<literal>" }}`)
	out, err := PruneThreadView(input, map[string]bool{"live": true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("secret")) || bytes.Contains(out, []byte("gone")) {
		t.Fatalf("deleted state retained: %s", out)
	}
	if !bytes.Contains(out, []byte(`{ "Draft":"retain", "future":9007199254740993 }`)) || !bytes.Contains(out, []byte(`{ "x" : "<literal>" }`)) {
		t.Fatalf("unrelated values changed: %s", out)
	}
	var v map[string]json.RawMessage
	json.Unmarshal(out, &v)
	if string(v["Active"]) != `""` {
		t.Fatalf("active not cleared: %s", out)
	}
	again, err := PruneThreadView(out, map[string]bool{"live": true})
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("projection is not stable", err)
	}
}

func TestPruneThreadViewUnchangedAndMalformed(t *testing.T) {
	input := json.RawMessage(`{ "Active":"live", "Threads":{"live":{"Draft":"keep"}},"Pending":{"ThreadID":"live"},"PendingAction":{"Kind":"send"},"unknown":[1,2] }`)
	out, err := PruneThreadView(input, map[string]bool{"live": true})
	if err != nil || !bytes.Equal(input, out) {
		t.Fatal("live view changed", err)
	}
	for _, input := range []string{`[]`, `null`, `{"Threads":[]}`, `{"Edit":"oops"}`, `{"Active":42}`} {
		if _, err := PruneThreadView([]byte(input), nil); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestPruneThreadViewCaseAndDuplicateFields(t *testing.T) {
	out, err := PruneThreadView([]byte(`{"active":"gone","threads":{"gone":{"Draft":"secret"}}}`), nil)
	if err != nil || bytes.Contains(out, []byte("gone")) || bytes.Contains(out, []byte("secret")) {
		t.Fatalf("%s %v", out, err)
	}
	for _, payload := range []string{`{"Threads":{"gone":{}},"Threads":{}}`, `{"threads":{},"Threads":{}}`} {
		if _, err := PruneThreadView([]byte(payload), nil); err == nil {
			t.Fatal("duplicate accepted")
		}
	}
}

func TestPruneStartedDraftDeletesOnlyAcceptedCapture(t *testing.T) {
	for _, draft := range []string{"accepted text", "new text"} {
		input := json.RawMessage(`{"StartedDraft":{"ThreadID":"gone","Command":{"ProjectID":"project","Text":"accepted text"}},"DraftProjectID":"project","DraftThreads":{"project":{"Draft":"` + draft + `"},"other":{"Draft":"preserve"}}}`)
		out, err := PruneThreadView(input, map[string]bool{"live": true})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("StartedDraft")) || bytes.Contains(out, []byte("accepted text")) || !bytes.Contains(out, []byte("preserve")) {
			t.Fatalf("bad accepted draft purge: %s", out)
		}
		if draft == "new text" && !bytes.Contains(out, []byte(draft)) {
			t.Fatalf("newly typed draft lost: %s", out)
		}
	}
	live := json.RawMessage(`{"StartedDraft":{"ThreadID":"live","Command":{"ProjectID":"project","Text":"accepted"}},"DraftThreads":{"project":{"Draft":"accepted"}}}`)
	out, err := PruneThreadView(live, map[string]bool{"live": true})
	if err != nil || !bytes.Equal(out, live) {
		t.Fatalf("live accepted draft changed: %s %v", out, err)
	}
}
