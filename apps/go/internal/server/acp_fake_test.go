package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// The fake agent speaks raw JSON-RPC rather than the SDK's typed agent side, so
// a test can emit payloads the SDK's own union does not model — notably
// usage_update — and assert that the client still records them.

// fakeOptionWire is the config option catalogue the fake agent advertises. It
// mirrors the shape the live probe recorded: a model select, a thought_level
// select and a mode select, with no context or speed option at all.
const fakeOptionWire = `[
  {"id":"model","name":"Model","category":"model","type":"select","currentValue":"reference",
   "options":[{"value":"reference","name":"Reference model"},{"value":"reference-fast","name":"Reference fast"}]},
  {"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":"medium",
   "options":[{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]},
  {"id":"mode","name":"Mode","category":"mode","type":"select","currentValue":"ask",
   "options":[{"value":"ask","name":"Ask"},{"value":"auto","name":"Auto"}]}
]`

func fakeOptions(t *testing.T, current map[string]string) []protocol.ConfigOption {
	t.Helper()
	var wire []acp.SessionConfigOption
	if err := json.Unmarshal([]byte(fakeOptionWire), &wire); err != nil {
		t.Fatal(err)
	}
	options, _ := agent.MapOptions(wire)
	for i := range options {
		if value, ok := current[options[i].ID]; ok {
			options[i].Current = value
		}
	}
	return options
}

func fakeSettings() protocol.Settings {
	return protocol.Settings{Model: "reference", Effort: "low", Permissions: "auto", Context: agent.Unavailable, Speed: agent.Unavailable}
}

// fakeAgent is a scripted ACP agent driven over an in-process pipe pair.
type fakeAgent struct {
	conn *acp.Connection
	stop func()

	mu               sync.Mutex
	options          map[string]string
	sessionID        string
	cancel           chan struct{}
	prompts          []string
	loads            int
	closes           int
	sessions         int
	authRequired     bool
	rejectValue      string
	stopped          bool
	cancelRelease    <-chan struct{}
	nativeQuestions  bool
	codexQuestions   bool
	nativeVersion    string
	elicitationForm  bool
	questionCancel   context.CancelFunc
	questionResponse map[string]any
	// questionBlocking, questionReceipt and questionFail shape the native
	// question turn: blocking Codex mode, a bridge delivery receipt after the
	// answer, and a failed prompt after the answer.
	questionBlocking, questionReceipt, questionFail bool
}

func (f *fakeAgent) optionWire() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var wire []any
	if err := json.Unmarshal([]byte(fakeOptionWire), &wire); err != nil {
		panic(err)
	}
	for _, entry := range wire {
		option := entry.(map[string]any)
		if current, ok := f.options[option["id"].(string)]; ok {
			option["currentValue"] = current
		}
	}
	return wire
}

func (f *fakeAgent) update(ctx context.Context, sessionID string, payload map[string]any) {
	_ = f.conn.SendNotification(ctx, acp.ClientMethodSessionUpdate, map[string]any{"sessionId": sessionID, "update": payload})
}

func chunk(text string) map[string]any {
	return map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
}

func (f *fakeAgent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case acp.AgentMethodInitialize:
		var init struct {
			ClientCapabilities map[string]json.RawMessage `json:"clientCapabilities"`
		}
		_ = json.Unmarshal(params, &init)
		var elicitation map[string]any
		_ = json.Unmarshal(init.ClientCapabilities["elicitation"], &elicitation)
		f.mu.Lock()
		_, f.elicitationForm = elicitation["form"]
		name, version := "fake-acp", "0.0.1"
		if f.nativeQuestions {
			name, version = "@agentclientprotocol/claude-agent-acp", "0.80.0"
		}
		if f.nativeVersion != "" {
			version = f.nativeVersion
		}
		meta := map[string]any{}
		if f.codexQuestions {
			name, version = "tui-go-codex", acpbridge.Version
			meta["questionDialect"] = acpbridge.CodexQuestionDialect
		}
		f.mu.Unlock()
		return map[string]any{
			"protocolVersion": 1,
			"agentInfo":       map[string]any{"name": name, "version": version},
			"agentCapabilities": map[string]any{
				"_meta":               meta,
				"loadSession":         true,
				"promptCapabilities":  map[string]any{"embeddedContext": true},
				"sessionCapabilities": map[string]any{"close": map[string]any{}},
			},
			"authMethods": []any{map[string]any{"id": "cli", "name": "Adapter CLI login"}},
		}, nil
	case acp.AgentMethodSessionNew:
		f.mu.Lock()
		if f.authRequired {
			f.mu.Unlock()
			return nil, acp.NewAuthRequired(map[string]any{"error": "sign in with the adapter CLI"})
		}
		f.sessions++
		f.sessionID = "session-fake"
		id := f.sessionID
		f.mu.Unlock()
		return map[string]any{"sessionId": id, "configOptions": f.optionWire()}, nil
	case acp.AgentMethodSessionLoad:
		f.mu.Lock()
		f.loads++
		var request struct {
			SessionId string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &request)
		f.sessionID = request.SessionId
		f.mu.Unlock()
		return map[string]any{"configOptions": f.optionWire()}, nil
	case acp.AgentMethodSessionClose:
		f.mu.Lock()
		f.closes++
		f.mu.Unlock()
		return map[string]any{}, nil
	case acp.AgentMethodSessionSetConfigOption:
		var request struct {
			ConfigId string `json:"configId"`
			Value    string `json:"value"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		f.mu.Lock()
		reject := f.rejectValue != "" && request.Value == f.rejectValue
		if !reject {
			f.options[request.ConfigId] = request.Value
		}
		f.mu.Unlock()
		if reject {
			return nil, acp.NewInvalidParams(map[string]any{"error": "value " + request.Value + " is not selectable"})
		}
		return map[string]any{"configOptions": f.optionWire()}, nil
	case acp.AgentMethodSessionCancel:
		f.mu.Lock()
		if f.cancel != nil {
			select {
			case <-f.cancel:
			default:
				close(f.cancel)
			}
		}
		f.mu.Unlock()
		return nil, nil
	case acp.AgentMethodSessionPrompt:
		return f.prompt(ctx, params)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (f *fakeAgent) prompt(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var request struct {
		SessionId string `json:"sessionId"`
		Prompt    []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Resource *struct {
				Text string `json:"text"`
				Uri  string `json:"uri"`
			} `json:"resource"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	var parts []string
	for _, block := range request.Prompt {
		if block.Resource != nil {
			parts = append(parts, "resource:"+block.Resource.Uri+":"+block.Resource.Text)
			continue
		}
		parts = append(parts, block.Text)
	}
	text := strings.Join(parts, "\n")
	cancel := make(chan struct{})
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	f.cancel = cancel
	f.mu.Unlock()
	session := request.SessionId
	switch {
	case strings.Contains(text, "cancel me"):
		f.update(ctx, session, chunk("counting"))
		select {
		case <-cancel:
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		f.mu.Lock()
		release := f.cancelRelease
		f.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		// The recorded adapters emit trailing updates between the cancel
		// notification and the prompt response; the client must keep consuming.
		f.update(ctx, session, map[string]any{"sessionUpdate": "usage_update", "used": 12, "size": 200})
		return map[string]any{"stopReason": "cancelled"}, nil
	case strings.Contains(text, "ask native question"):
		questionCtx, questionCancel := context.WithCancel(ctx)
		defer questionCancel()
		f.mu.Lock()
		f.questionCancel = questionCancel
		f.mu.Unlock()
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "question-call", "title": "AskUserQuestion", "kind": "other", "status": "pending"})
		wire := nativeQuestionWire(session)
		if f.codexQuestions {
			wire = codexNativeQuestionWire(session)
			wire["source"].(map[string]any)["isBlocking"] = f.questionBlocking
		}
		response, err := acp.SendRequest[map[string]any](f.conn, questionCtx, "elicitation/create", wire)
		if err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		f.mu.Lock()
		f.questionResponse = response
		receipt, fail := f.questionReceipt, f.questionFail
		f.mu.Unlock()
		if receipt {
			f.update(ctx, session, map[string]any{"sessionUpdate": "tui_question_delivery", "dialect": acpbridge.QuestionDeliveryDialect, "toolCallId": "question-call", "evidence": "fake"})
		}
		if fail {
			return nil, acp.NewInternalError(map[string]any{"error": "the fake agent failed after the answer"})
		}
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "question-call", "status": "completed"})
		f.update(ctx, session, chunk("question answered"))
		return map[string]any{"stopReason": "end_turn"}, nil
	case strings.Contains(text, "ask permission"):
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-1", "title": "Write notes.md", "kind": "edit", "status": "pending", "locations": []any{map[string]any{"path": "notes.md"}}, "rawInput": map[string]any{"path": "notes.md"}})
		allowLabel, rejectLabel := "Allow once", "Reject"
		if strings.Contains(text, "duplicate labels") {
			allowLabel, rejectLabel = "Decision", "Decision"
		}
		response, err := acp.SendRequest[acp.RequestPermissionResponse](f.conn, ctx, acp.ClientMethodSessionRequestPermission, map[string]any{
			"sessionId": session,
			"toolCall":  map[string]any{"toolCallId": "call-1", "title": "Write notes.md", "kind": "edit", "status": "pending", "locations": []any{map[string]any{"path": "notes.md"}}, "content": []any{map[string]any{"type": "diff", "path": "notes.md", "newText": "hello"}}},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": allowLabel, "kind": "allow_once"},
				map[string]any{"optionId": "reject", "name": rejectLabel, "kind": "reject_once"},
			},
		})
		if err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		decision := "cancelled"
		if response.Outcome.Selected != nil {
			decision = string(response.Outcome.Selected.OptionId)
		}
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1", "status": "completed", "rawOutput": map[string]any{"decision": decision}})
		f.update(ctx, session, chunk("decision "+decision))
		return map[string]any{"stopReason": "end_turn"}, nil
	case strings.Contains(text, "boom"):
		return nil, acp.NewInternalError(map[string]any{"error": "the fake agent failed this turn"})
	default:
		f.update(ctx, session, chunk("pon"))
		f.update(ctx, session, chunk("g"))
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-echo", "title": "Echo", "kind": "read", "status": "pending", "rawInput": map[string]any{"text": text}})
		f.update(ctx, session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-echo", "status": "completed"})
		f.update(ctx, session, map[string]any{"sessionUpdate": "plan", "entries": []any{
			map[string]any{"content": "Read the prompt", "priority": "medium", "status": "completed"},
			map[string]any{"content": "Answer", "priority": "medium", "status": "in_progress"},
		}})
		f.update(ctx, session, map[string]any{"sessionUpdate": "usage_update", "used": 42, "size": 1000})
		f.update(ctx, session, map[string]any{"sessionUpdate": "session_info_update", "title": "Echoed thread"})
		f.update(ctx, session, map[string]any{"sessionUpdate": "invented_future_kind", "payload": "retained"})
		return map[string]any{"stopReason": "end_turn"}, nil
	}
}

func (f *fakeAgent) snapshot() (prompts []string, loads, closes, sessions int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...), f.loads, f.closes, f.sessions
}

// fakeFleet records every agent connection a test's engine opened.
type fakeFleet struct {
	mu                   sync.Mutex
	agents               []*fakeAgent
	authRequired         bool
	rejectValue          string
	launchErr            error
	dropApprovalResponse bool
	nativeQuestions      bool
	codexQuestions       bool
	nativeVersion        string
	dropQuestionResponse bool
	questionBlocking     bool
	questionReceipt      bool
	questionFail         bool
}

func (f *fakeFleet) launch(ctx context.Context, o agent.Options) (*agent.Session, error) {
	f.mu.Lock()
	if f.launchErr != nil {
		err := f.launchErr
		f.mu.Unlock()
		return nil, err
	}
	fake := &fakeAgent{options: map[string]string{}, authRequired: f.authRequired, rejectValue: f.rejectValue, nativeQuestions: f.nativeQuestions, codexQuestions: f.codexQuestions, nativeVersion: f.nativeVersion, questionBlocking: f.questionBlocking, questionReceipt: f.questionReceipt, questionFail: f.questionFail}
	dropApprovalResponse := f.dropApprovalResponse
	dropQuestionResponse := f.dropQuestionResponse
	f.agents = append(f.agents, fake)
	f.mu.Unlock()

	clientReader, agentWriter := io.Pipe()
	agentReader, clientWriter := io.Pipe()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			fake.mu.Lock()
			fake.stopped = true
			fake.mu.Unlock()
			_ = clientWriter.Close()
			_ = agentWriter.Close()
		})
	}
	fake.stop = stop
	fake.conn = acp.NewConnection(fake.handle, agentWriter, agentReader)
	if dropQuestionResponse {
		return agent.Connect(o.Handler, questionResponseDropper{Writer: clientWriter, disconnect: stop}, clientReader, stop, o.Log), nil
	}
	if dropApprovalResponse {
		return agent.Connect(o.Handler, approvalResponseDropper{Writer: clientWriter, disconnect: stop}, clientReader, stop, o.Log), nil
	}
	return agent.Connect(o.Handler, clientWriter, clientReader, stop, o.Log), nil
}

func (f *fakeFleet) last() *fakeAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agents) == 0 {
		return nil
	}
	return f.agents[len(f.agents)-1]
}

func (f *fakeFleet) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.agents)
}

// acpEngine builds an engine whose "claude" agent is already probed ready and
// whose connections are in-process fakes.
func acpEngine(t *testing.T) (*engine, *fakeFleet, string) {
	t.Helper()
	st, err := storage.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	snap := fixture.Initial()
	agent.Ensure(&snap, func(string) string { return "" })
	checkout := t.TempDir()
	snap.Projects = append(snap.Projects, protocol.Project{ID: "project-acp", Name: "ACP", Path: checkout, Revision: 1})
	record := agent.Find(&snap, "claude")
	record.State, record.Detail = agent.StateReady, "probed by the test harness"
	record.Options = fakeOptions(t, nil)
	record.Fields = agent.Fields(record.Options)
	record.Capabilities = []string{agent.CapLoadSession, agent.CapEmbeddedPrompt, agent.CapSessionClose}
	if err := st.Save(snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	e := newEngine(snap, st)
	fleet := &fakeFleet{}
	e.launch = fleet.launch
	t.Cleanup(e.stopAgents)
	return e, fleet, checkout
}

// current returns the authoritative snapshot.
func (e *engine) current() protocol.Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return clone(e.snap)
}

// waitFor polls the authoritative snapshot until cond holds. Agent work runs on
// its own goroutines, so tests observe outcomes rather than timing.
func waitFor(t *testing.T, e *engine, what string, cond func(protocol.Snapshot) bool) protocol.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		s := clone(e.snap)
		e.mu.Unlock()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func threadOf(s protocol.Snapshot, id string) protocol.Thread {
	for _, t := range s.Threads {
		if t.ID == id {
			return t
		}
	}
	return protocol.Thread{}
}

// activityOf finds one activity by identity. Session notes are scoped to the
// turn they arrived in, so an exact identity or that identity plus its turn
// suffix both match.
func activityOf(t protocol.Thread, id string) protocol.Activity {
	for _, a := range t.Activity {
		if a.ID == id || strings.HasPrefix(a.ID, id+"-") {
			return a
		}
	}
	return protocol.Activity{}
}
