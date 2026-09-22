package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func update(t *testing.T, payload string) Update {
	t.Helper()
	if !json.Valid([]byte(payload)) {
		t.Fatalf("invalid test payload: %s", payload)
	}
	return DecodeUpdate(json.RawMessage(payload))
}

func apply(t *testing.T, thread *protocol.Thread, payloads ...string) {
	t.Helper()
	for _, payload := range payloads {
		Normalize(thread, update(t, payload))
	}
}

func TestStreamedChunksCoalesceIntoOneActivityPerRole(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking "}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"aloud"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pon"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"g"}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"echoed prompt"}}`,
	)
	if len(thread.Activity) != 2 {
		t.Fatalf("expected one activity per role, got %d: %+v", len(thread.Activity), thread.Activity)
	}
	thought, reply := thread.Activity[0], thread.Activity[1]
	if thought.Role != "thought" || thought.Title != "Thinking" || thought.Text != "thinking aloud" {
		t.Fatalf("thought: %+v", thought)
	}
	if reply.Role != "agent" || reply.Text != "pong" || reply.TurnID != "turn-1" {
		t.Fatalf("reply: %+v", reply)
	}
}

func TestToolUpdatesNeverEraseEarlierValues(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread,
		`{"sessionUpdate":"tool_call","toolCallId":"c1","title":"Read file","kind":"read","status":"pending","locations":[{"path":"a.go"}],"rawInput":{"path":"a.go"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed","rawOutput":{"bytes":12}}`,
	)
	if len(thread.Activity) != 1 {
		t.Fatalf("tool call should upsert one row: %+v", thread.Activity)
	}
	row := thread.Activity[0]
	if row.Role != "tool" || row.State != "completed" || row.Title != "Read file" {
		t.Fatalf("tool row: %+v", row)
	}
	var detail struct {
		Kind      string          `json:"kind"`
		Locations []string        `json:"locations"`
		RawInput  json.RawMessage `json:"rawInput"`
		RawOutput json.RawMessage `json:"rawOutput"`
	}
	if err := json.Unmarshal([]byte(row.Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Kind != "read" || len(detail.Locations) != 1 || string(detail.RawInput) != `{"path":"a.go"}` {
		t.Fatalf("omitted fields erased earlier detail: %s", row.Detail)
	}
	if string(detail.RawOutput) != `{"bytes":12}` {
		t.Fatalf("raw output lost: %s", row.Detail)
	}
	if row.Text != "read · a.go" {
		t.Fatalf("summary: %q", row.Text)
	}
}

func TestMCPToolCallsKeepTheirOwnRole(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread,
		`{"sessionUpdate":"tool_call","toolCallId":"mcp__design__inspect","title":"mcp__design__inspect","status":"pending"}`)
	if thread.Activity[0].Role != "mcp" {
		t.Fatalf("mcp tool call: %+v", thread.Activity[0])
	}
}

func TestUnknownKindsAreRetained(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread,
		`{"sessionUpdate":"plan_update","planId":"p1","entries":[]}`,
		`{"sessionUpdate":"something_new","payload":{"keep":"this"}}`,
		`{"noDiscriminator":true}`,
	)
	if len(thread.Activity) != 3 {
		t.Fatalf("unknown kinds dropped: %+v", thread.Activity)
	}
	for _, expected := range []string{"plan_update", "something_new", "unknown"} {
		// Session notes carry the turn they arrived in, so a later turn adds
		// its own row instead of rewriting an earlier one.
		row := findActivity(thread, "update-"+expected+"-turn-1")
		if row == nil || !strings.Contains(row.Title, expected) || row.TurnID != "turn-1" {
			t.Fatalf("missing retained row for %q: %+v", expected, thread.Activity)
		}
	}
	if !strings.Contains(findActivity(thread, "update-something_new-turn-1").Detail, `"keep":"this"`) {
		t.Fatal("unknown payload not retained")
	}
}

func TestUsageAndTitleComeFromTheWireKind(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	// Recorded with acp-go-sdk v0.13.5: an unrecognised discriminator is
	// resolved by shape, so trusting the typed union would record an invented
	// kind as a title change. Kind-first dispatch is what prevents that.
	invented := update(t, `{"sessionUpdate":"invented_kind","x":1}`)
	if invented.Typed.SessionInfoUpdate == nil {
		t.Log("the SDK no longer mis-resolves unknown kinds; kind-first dispatch stays correct either way")
	}
	apply(t, thread,
		`{"sessionUpdate":"usage_update","used":17,"size":200000}`,
		`{"sessionUpdate":"session_info_update","title":"Rename the shell"}`,
	)
	if thread.Usage == nil || thread.Usage.Used != 17 || thread.Usage.Size != 200000 || thread.Usage.Source != "usage_update" || thread.Usage.ReportedAt == "" {
		t.Fatalf("usage: %+v", thread.Usage)
	}
	if thread.Title != "Rename the shell" {
		t.Fatalf("title: %q", thread.Title)
	}
	if len(thread.Activity) != 0 {
		t.Fatalf("telemetry should not add transcript rows: %+v", thread.Activity)
	}
}

func TestConfigOptionUpdateReplacesTheCatalogue(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1", Options: []protocol.ConfigOption{{ID: "stale"}}}
	apply(t, thread, `{"sessionUpdate":"config_option_update","configOptions":[
		{"id":"model","name":"Model","category":"model","type":"select","currentValue":"opus","options":[{"value":"opus","name":"Opus"}]},
		{"id":"fast","name":"Fast mode","category":"model_config","type":"boolean","currentValue":true}]}`)
	if len(thread.Options) != 2 || thread.Options[0].ID != "model" {
		t.Fatalf("catalogue not replaced whole: %+v", thread.Options)
	}
	if thread.Effective.Model != "opus" || thread.Effective.Effort != Unavailable || thread.Effective.Speed != "true" {
		t.Fatalf("effective: %+v", thread.Effective)
	}
	// A boolean option is selectable through explicit true/false values.
	if thread.Options[1].Type != "boolean" || thread.Options[1].Current != "true" || len(thread.Options[1].Values) != 2 {
		t.Fatalf("boolean option: %+v", thread.Options[1])
	}
}

func TestPlanAndRetentionBounds(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread, `{"sessionUpdate":"plan","entries":[
		{"content":"One","priority":"medium","status":"completed"},
		{"content":"Two","priority":"medium","status":"in_progress"},
		{"content":"Three","priority":"low","status":"pending"}]}`)
	if len(thread.Plan) != 3 || thread.Plan[1].State != "active" || thread.Plan[2].State != "pending" {
		t.Fatalf("plan: %+v", thread.Plan)
	}
	for i := 0; i < ActivityLimit+20; i++ {
		Normalize(thread, update(t, `{"sessionUpdate":"tool_call","toolCallId":"c`+itoa(i)+`","title":"T","status":"completed"}`))
	}
	if len(thread.Activity) != ActivityLimit {
		t.Fatalf("retention limit: %d", len(thread.Activity))
	}
	if thread.Activity[0].ID != "history-limit-t" {
		t.Fatalf("truncation is not recorded: %+v", thread.Activity[0])
	}
}

func TestControlSequencesAreNeverRetained(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1"}
	apply(t, thread,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"before\u001b[31mred\u0007\r\nafter"}}`)
	text := thread.Activity[0].Text
	if strings.ContainsAny(text, "\x1b\a\r") {
		t.Fatalf("agent output kept terminal control sequences: %q", text)
	}
	if text != "before[31mred\nafter" {
		t.Fatalf("sanitized text: %q", text)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func findActivity(t *protocol.Thread, id string) *protocol.Activity {
	for i := range t.Activity {
		if t.Activity[i].ID == id {
			return &t.Activity[i]
		}
	}
	return nil
}

// TestReplayRecordedAdapterStreams runs the recorded live-probe traffic through
// the normalizer. It is evidence that the mapping handles what two real
// adapters actually sent, not proof that it handles every adapter.
func TestReplayRecordedAdapterStreams(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "docs", "research", "acp-fixtures")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("recorded streams unavailable: %v", err)
	}
	replayed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "wire-full.jsonl")
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			defer file.Close()
			thread := &protocol.Thread{ID: "replay", TurnID: "turn-1"}
			kinds := map[string]int{}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
			for scanner.Scan() {
				var record struct {
					Dir string `json:"dir"`
					Msg struct {
						Method string `json:"method"`
						Params struct {
							Update json.RawMessage `json:"update"`
						} `json:"params"`
					} `json:"msg"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
					t.Fatal(err)
				}
				// Non-session notifications such as _auth/status_update are not
				// session updates and must be ignored without incident.
				if record.Dir != "recv" || record.Msg.Method != acp.ClientMethodSessionUpdate {
					continue
				}
				u := DecodeUpdate(record.Msg.Params.Update)
				kinds[u.Kind]++
				Normalize(thread, u)
				replayed++
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if len(kinds) == 0 {
				t.Fatal("no session updates were replayed")
			}
			// Every recorded kind must be accounted for: either it produced a
			// recognised effect, or it was retained as an unknown row.
			for kind := range kinds {
				if findActivity(thread, "update-"+kind) != nil {
					t.Logf("kind %q retained as an unrecognised row", kind)
				}
			}
			if kinds["usage_update"] > 0 && thread.Usage == nil {
				t.Fatal("recorded usage_update produced no telemetry")
			}
			if kinds["session_info_update"] > 0 && thread.Title == "" {
				t.Fatal("recorded session_info_update produced no title")
			}
			if kinds["agent_message_chunk"] > 0 {
				if reply := findActivity(thread, "agent-turn-1"); reply == nil || reply.Text == "" {
					t.Fatal("recorded agent_message_chunk produced no reply activity")
				}
			}
			for _, a := range thread.Activity {
				if strings.ContainsAny(a.Text+a.Title+a.Detail, "\x1b\a\r") {
					t.Fatalf("recorded stream leaked control sequences into %q", a.ID)
				}
				if len(a.Text) > MaxActivityText || len(a.Detail) > MaxActivityDeta {
					t.Fatalf("retention bound exceeded on %q", a.ID)
				}
			}
			if len(thread.Activity) > ActivityLimit {
				t.Fatalf("activity limit exceeded: %d", len(thread.Activity))
			}
			t.Logf("%s: replayed %d updates into %d activities; kinds=%v", entry.Name(), sum(kinds), len(thread.Activity), kinds)
		})
	}
	if replayed == 0 {
		t.Skip("no recorded streams found")
	}
}

func sum(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// TestTrailingChunksDoNotReopenASettledTurn covers the live finding that both
// adapters emit chunks after the prompt response has already ended the turn. The
// text is retained, but a cancelled reply must not go back to claiming it runs.
func TestTrailingChunksDoNotReopenASettledTurn(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1", State: "running"}
	apply(t, thread,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"partial"}}`,
	)
	// The turn is cancelled: the server settles the thread and its streams.
	thread.State, thread.StopReason = "interrupted", "cancelled"
	for i := range thread.Activity {
		thread.Activity[i].State = "interrupted"
	}
	apply(t, thread,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":" tail"}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"late thought"}}`,
	)
	reply := findActivity(thread, "agent-turn-1")
	if reply == nil || reply.Text != "partial tail" {
		t.Fatalf("trailing text lost: %+v", thread.Activity)
	}
	if reply.State != "interrupted" {
		t.Fatalf("trailing chunk reopened a cancelled reply: %q", reply.State)
	}
	thought := findActivity(thread, "thought-turn-1")
	if thought == nil || thought.State != "interrupted" {
		t.Fatalf("late reasoning claims running work: %+v", thought)
	}
}

// TestSessionNotesAreScopedToTheirTurn covers the live finding that one shared
// note row was rewritten by a later turn, moving that turn's event to the top of
// the transcript and relabelling the earlier entry.
func TestSessionNotesAreScopedToTheirTurn(t *testing.T) {
	thread := &protocol.Thread{ID: "t", TurnID: "turn-1", State: "running"}
	apply(t, thread,
		`{"sessionUpdate":"current_mode_update","currentModeId":"default"}`,
	)
	thread.TurnID = "turn-2"
	apply(t, thread,
		`{"sessionUpdate":"current_mode_update","currentModeId":"acceptEdits"}`,
	)
	notes := make([]protocol.Activity, 0, 2)
	for _, a := range thread.Activity {
		if strings.HasPrefix(a.ID, "current-mode-") {
			notes = append(notes, a)
		}
	}
	if len(notes) != 2 {
		t.Fatalf("a later turn rewrote an earlier note: %+v", thread.Activity)
	}
	if notes[0].TurnID != "turn-1" || notes[1].TurnID != "turn-2" {
		t.Fatalf("note chronology lost: %+v", notes)
	}
}
