package acpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	fakeClaudeRole = "TUI_GO_CLAUDE_TEST_ROLE"
	fakeClaudeLog  = "TUI_GO_CLAUDE_TEST_LOG"
	fakeTestBinary = "TUI_GO_CLAUDE_TEST_BINARY"
)

// The installed-CLI substitute is the Go test executable itself. A tiny
// executable shim passes Claude's real argv through to this test-only helper,
// which speaks the same JSONL protocol as claude -p.
func TestClaudeRuntimeHelper(t *testing.T) {
	switch os.Getenv(fakeClaudeRole) {
	case "runtime":
		os.Exit(runFakeClaudeRuntime())
	case "descendant":
		os.Exit(runFakeClaudeDescendant())
	}
}

type fakeClaudeLogEntry struct {
	Kind      string          `json:"kind"`
	Args      []string        `json:"args,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
}

var fakeLogMu sync.Mutex

func logFakeClaude(entry fakeClaudeLogEntry) {
	path := os.Getenv(fakeClaudeLog)
	if path == "" {
		return
	}
	fakeLogMu.Lock()
	defer fakeLogMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_ = json.NewEncoder(f).Encode(entry)
	_ = f.Close()
}

func runFakeClaudeRuntime() int {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = append(args, os.Args[i+1:]...)
			break
		}
	}
	logFakeClaude(fakeClaudeLogEntry{Kind: "argv", Args: args})
	if marker := os.Getenv("TUI_GO_CLAUDE_TEST_DESCENDANT_MARKER"); marker != "" {
		cmd := exec.Command(os.Getenv(fakeTestBinary), "-test.run=^TestClaudeRuntimeHelper$")
		cmd.Env = environmentWith(os.Environ(), fakeClaudeRole+"=descendant", "TUI_GO_CLAUDE_TEST_HEARTBEAT="+marker)
		cmd.Stdin = nil
		if os.Getenv("TUI_GO_CLAUDE_TEST_INHERIT_STDIO") == "1" {
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		} else {
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		}
		if err := cmd.Start(); err == nil {
			logFakeClaude(fakeClaudeLogEntry{Kind: "descendant_started", Prompt: fmt.Sprint(cmd.Process.Pid)})
		}
	}

	var writeMu sync.Mutex
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = os.Stdout.Write(append(b, '\n'))
		return err
	}
	type inputMessage struct {
		Type      string          `json:"type"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
		Response  struct {
			RequestID string          `json:"request_id"`
			Subtype   string          `json:"subtype"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		UUID string `json:"uuid"`
	}
	var scanErr error
	fastMode := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" && strings.Contains(args[i+1], `"fastMode":true`) {
			fastMode = true
		}
	}
	var activeUserUUID string
	var activePrompt string
	writeResult := func(userUUID string, fields map[string]any) error {
		fields["type"] = "result"
		fields["user_message_uuid"] = userUUID
		fields["user_message_uuids"] = []string{userUUID}
		return write(fields)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxFrame+1024)
	for scanner.Scan() {
		var msg inputMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			logFakeClaude(fakeClaudeLogEntry{Kind: "invalid_host_json"})
			continue
		}
		switch msg.Type {
		case "control_request":
			var request struct {
				Subtype  string `json:"subtype"`
				Model    string `json:"model"`
				Mode     string `json:"mode"`
				Settings struct {
					FastMode bool `json:"fastMode"`
				} `json:"settings"`
			}
			_ = json.Unmarshal(msg.Request, &request)
			logFakeClaude(fakeClaudeLogEntry{Kind: "host_control", Subtype: request.Subtype, RequestID: msg.RequestID, Request: msg.Request})
			response := map[string]any{}
			subtype, message := "success", ""
			switch request.Subtype {
			case "initialize":
				response = map[string]any{
					"models": []any{
						map[string]any{"value": "claude-model-live-a", "resolvedModel": "claude-model-live-a-v1", "displayName": "Live model A", "supportsAutoMode": true, "supportsFastMode": true},
						map[string]any{"value": "claude-model-live-b", "displayName": "Live model B", "supportsAutoMode": false},
					},
					"current_permission_mode":   "default",
					"fast_mode_state":           "off",
					"fast_mode_disabled_reason": "sdk_opt_in_required",
				}
				if fastMode {
					response["fast_mode_state"] = "on"
					delete(response, "fast_mode_disabled_reason")
				}
			case "apply_flag_settings":
				if os.Getenv("TUI_GO_CLAUDE_TEST_FAST_ACK_ONLY") != "1" || !request.Settings.FastMode {
					fastMode = request.Settings.FastMode
				}
			case "set_model":
				if request.Model != "" && request.Model == os.Getenv("TUI_GO_CLAUDE_TEST_DELAY_MODEL") {
					logFakeClaude(fakeClaudeLogEntry{Kind: "model_ack_delayed", RequestID: msg.RequestID, Request: msg.Request})
					select {}
				}
				if request.Model == os.Getenv("TUI_GO_CLAUDE_TEST_REJECT_MODEL") {
					subtype, message = "error", "model selection rejected by fake runtime"
				}
			case "set_permission_mode":
				response = map[string]any{"mode": request.Mode}
				if forced := os.Getenv("TUI_GO_CLAUDE_TEST_ACK_MODE"); forced != "" {
					response = map[string]any{"mode": forced}
				}
				if request.Mode == os.Getenv("TUI_GO_CLAUDE_TEST_REJECT_MODE") {
					subtype, message = "error", "permission selection rejected by fake runtime"
				}
			case "interrupt":
				response = map[string]any{"interrupted": true}
			default:
				response = map[string]any{}
			}
			wire := map[string]any{"type": "control_response", "response": map[string]any{"request_id": msg.RequestID, "subtype": subtype, "response": response}}
			if message != "" {
				wire["response"].(map[string]any)["error"] = message
			}
			if err := write(wire); err != nil {
				return 1
			}
			if request.Subtype == "interrupt" {
				if activePrompt == "permission-cancel-failure" {
					_ = writeResult(activeUserUUID, map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": "error", "errors": []string{"failure unrelated to interruption"}})
				} else {
					_ = writeResult(activeUserUUID, map[string]any{"subtype": "success", "is_error": false, "terminal_reason": "aborted_streaming"})
				}
			}
		case "user":
			activeUserUUID = msg.UUID
			var parts []string
			for _, block := range msg.Message.Content {
				parts = append(parts, block.Text)
			}
			prompt := strings.Join(parts, "\n")
			activePrompt = prompt
			userUUID := activeUserUUID
			logFakeClaude(fakeClaudeLogEntry{Kind: "user", Prompt: prompt})
			switch prompt {
			case "permission-allow", "permission-deny", "permission-requires-interaction", "permission-withdraw", "permission-cancel", "permission-cancel-failure":
				interaction := prompt == "permission-requires-interaction"
				request := map[string]any{
					"subtype": "can_use_tool", "tool_name": "Bash",
					"input": map[string]any{"command": "printf safe"}, "tool_use_id": "tool-fake-1",
					"decision_reason":           "default_to_no",
					"permission_suggestions":    []any{map[string]any{"type": "addRules", "behavior": "allow", "destination": "session"}},
					"requires_user_interaction": interaction,
				}
				_ = write(map[string]any{"type": "control_request", "request_id": "permission-fake-1", "request": request})
				if prompt == "permission-withdraw" {
					_ = write(map[string]any{"type": "control_cancel_request", "request_id": "permission-fake-1"})
					logFakeClaude(fakeClaudeLogEntry{Kind: "withdraw", RequestID: "permission-fake-1"})
					time.AfterFunc(250*time.Millisecond, func() {
						_ = writeResult(userUUID, map[string]any{"subtype": "success", "is_error": false})
					})
				}
			case "failed":
				_ = writeResult(userUUID, map[string]any{"subtype": "error_during_execution", "is_error": true, "errors": []string{"fake failure"}})
			case "result-then-exit":
				_ = writeResult(userUUID, map[string]any{"subtype": "success", "is_error": false})
				return 0
			case "disconnect":
				return 0
			case "malformed":
				_, _ = io.WriteString(os.Stdout, "{malformed json\n")
				return 0
			case "oversized":
				_, _ = io.WriteString(os.Stdout, strings.Repeat("x", maxFrame+1)+"\n")
				return 0
			default:
				_ = writeResult(userUUID, map[string]any{"subtype": "success", "is_error": false})
			}
		case "control_response":
			logFakeClaude(fakeClaudeLogEntry{Kind: "provider_response", RequestID: msg.Response.RequestID, Subtype: msg.Response.Subtype, Response: msg.Response.Response})
			if msg.Response.RequestID == "permission-fake-1" && msg.Response.Subtype == "error" {
				_ = write(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{"permission rejected"}, "user_message_uuid": activeUserUUID})
			} else if msg.Response.RequestID == "permission-fake-1" {
				_ = writeResult(activeUserUUID, map[string]any{"subtype": "success", "is_error": false})
			}
		}
	}
	scanErr = scanner.Err()
	if scanErr != nil {
		logFakeClaude(fakeClaudeLogEntry{Kind: "host_input_scan_error", Prompt: scanErr.Error()})
	}
	return 0
}

func runFakeClaudeDescendant() int {
	path := os.Getenv("TUI_GO_CLAUDE_TEST_HEARTBEAT")
	var n uint64
	for {
		n++
		_ = os.WriteFile(path, []byte(fmt.Sprint(n)), 0600)
		time.Sleep(15 * time.Millisecond)
	}
}

type claudeTestClient struct {
	permissionCalls atomic.Int32
	permissions     chan acp.RequestPermissionRequest
	permission      func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
	updates         chan acp.SessionNotification
}

func newClaudeTestClient() *claudeTestClient {
	return &claudeTestClient{permissions: make(chan acp.RequestPermissionRequest, 8), updates: make(chan acp.SessionNotification, 32)}
}

func (c *claudeTestClient) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.permissionCalls.Add(1)
	select {
	case c.permissions <- p:
	default:
	}
	if c.permission != nil {
		return c.permission(ctx, p)
	}
	return testPermissionChoice("deny"), nil
}
func (c *claudeTestClient) SessionUpdate(_ context.Context, p acp.SessionNotification) error {
	select {
	case c.updates <- p:
	default:
	}
	return nil
}
func (*claudeTestClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errors.New("unexpected filesystem request")
}
func (*claudeTestClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("unexpected filesystem request")
}
func (*claudeTestClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errors.New("unexpected terminal request")
}
func (*claudeTestClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errors.New("unexpected terminal request")
}
func (*claudeTestClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errors.New("unexpected terminal request")
}
func (*claudeTestClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errors.New("unexpected terminal request")
}
func (*claudeTestClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errors.New("unexpected terminal request")
}

func testPermissionChoice(id string) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{
		OptionId: acp.PermissionOptionId(id), Outcome: "selected",
	}}}
}

type claudeFixture struct {
	endpoint *Endpoint
	conn     *acp.ClientSideConnection
	client   *claudeTestClient
	session  acp.SessionId
	cwd      string
	logPath  string
	options  []acp.SessionConfigOption
}

func startClaudeFixture(t *testing.T, extraEnv ...string) *claudeFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test CLI shim is a Unix executable")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "checkout")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "runtime.jsonl")
	shim := filepath.Join(root, "claude")
	script := "#!/bin/sh\nexec \"$" + fakeTestBinary + "\" -test.run='^TestClaudeRuntimeHelper$' -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), fakeTestBinary+"="+os.Args[0], fakeClaudeRole+"=runtime", fakeClaudeLog+"="+logPath)
	env = append(env, extraEnv...)
	endpoint, err := Open(context.Background(), "claude", shim, cwd, env, io.Discard)
	if err != nil {
		t.Fatalf("Open Claude bridge: %v", err)
	}
	t.Cleanup(endpoint.Stop)
	client := newClaudeTestClient()
	conn := acp.NewClientSideConnection(client, endpoint.Input, endpoint.Output)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	created, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return &claudeFixture{endpoint: endpoint, conn: conn, client: client, session: created.SessionId, cwd: cwd, logPath: logPath, options: created.ConfigOptions}
}

func (f *claudeFixture) setModel(t *testing.T, model string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: f.session, ConfigId: "model", Value: acp.SessionConfigValueId(model),
	}})
	return err
}

func (f *claudeFixture) setSpeed(t *testing.T, speed string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: f.session, ConfigId: "speed", Value: acp.SessionConfigValueId(speed),
	}})
	return err
}

func (f *claudeFixture) prompt(ctx context.Context, text string) (acp.PromptResponse, error) {
	return f.conn.Prompt(ctx, acp.PromptRequest{SessionId: f.session, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: text}}}})
}

func readFakeClaudeLog(t *testing.T, path string) []fakeClaudeLogEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var entries []fakeClaudeLogEntry
	s := bufio.NewScanner(strings.NewReader(string(b)))
	for s.Scan() {
		var entry fakeClaudeLogEntry
		if err := json.Unmarshal(s.Bytes(), &entry); err != nil {
			t.Fatalf("parse fake runtime log: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func waitFakeClaudeLog(t *testing.T, path string, predicate func([]fakeClaudeLogEntry) bool) []fakeClaudeLogEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries := readFakeClaudeLog(t, path)
		if predicate(entries) {
			return entries
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries := readFakeClaudeLog(t, path)
	t.Fatalf("timed out waiting for fake runtime log; entries=%+v", entries)
	return nil
}

func entryCount(entries []fakeClaudeLogEntry, kind string) int {
	n := 0
	for _, entry := range entries {
		if entry.Kind == kind {
			n++
		}
	}
	return n
}

func providerResponseCount(entries []fakeClaudeLogEntry, requestID string) int {
	n := 0
	for _, entry := range entries {
		if entry.Kind == "provider_response" && entry.RequestID == requestID {
			n++
		}
	}
	return n
}

func modelOptionValues(t *testing.T, options []acp.SessionConfigOption) []string {
	t.Helper()
	for _, option := range options {
		if option.Select == nil || option.Select.Id != "model" {
			continue
		}
		if option.Select.Options.Ungrouped == nil {
			t.Fatal("model options are not a flat discovered catalogue")
		}
		values := make([]string, 0, len(*option.Select.Options.Ungrouped))
		for _, choice := range *option.Select.Options.Ungrouped {
			values = append(values, string(choice.Value))
		}
		return values
	}
	t.Fatal("model configuration option missing")
	return nil
}

func TestClaudeBridgeRequiresDiscoveredAndAcknowledgedModel(t *testing.T) {
	f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_REJECT_MODEL=claude-model-live-a")
	values := modelOptionValues(t, f.options)
	if len(values) < 3 || values[0] != "claude-model-live-a" || values[1] != "claude-model-live-b" || !stringInSlice(values, "claude-opus-4-8") || !stringInSlice(values, "claude-model-live-a-v1") {
		t.Fatalf("model values omitted native aliases or documented legacy choices: %v", values)
	}
	if err := f.setModel(t, "invented-model"); err == nil {
		t.Fatal("accepted a model absent from the runtime catalogue")
	}
	if err := f.setModel(t, "claude-model-live-a"); err == nil {
		t.Fatal("accepted a model the runtime rejected")
	}
	if _, err := f.prompt(context.Background(), "must-not-run"); err == nil {
		t.Fatal("prompt ran without an acknowledged explicit model")
	}
	entries := readFakeClaudeLog(t, f.logPath)
	if got := entryCount(entries, "host_control"); got != 4 {
		t.Fatalf("runtime saw %d controls, want initialize, Standard reset/readback, and one valid set_model request", got)
	}
	if got := entryCount(entries, "user"); got != 0 {
		t.Fatalf("runtime received %d prompt(s) after a failed setting", got)
	}
}

func TestClaudeBridgeOffersPinnedAndDocumentedLegacyModelIDs(t *testing.T) {
	f := startClaudeFixture(t)
	values := modelOptionValues(t, f.options)
	for _, value := range []string{"claude-model-live-a-v1", "claude-opus-4-8", "claude-fable-5"} {
		if !stringInSlice(values, value) {
			t.Fatalf("missing pinned or legacy %q: %v", value, values)
		}
	}
	if err := f.setModel(t, "claude-model-live-a-v1"); err != nil {
		t.Fatalf("native bridge rejected runtime resolved model: %v", err)
	}
	if err := f.setModel(t, "claude-opus-4-8"); err != nil {
		t.Fatalf("native bridge rejected documented legacy model: %v", err)
	}
	entries := readFakeClaudeLog(t, f.logPath)
	var selected []string
	for _, entry := range entries {
		if entry.Subtype != "set_model" {
			continue
		}
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(entry.Request, &request)
		selected = append(selected, request.Model)
	}
	if got := strings.Join(selected, ","); got != "claude-model-live-a-v1,claude-opus-4-8" {
		t.Fatalf("native model controls = %s", got)
	}
}

func TestClaudeFastRequiresCapableModelAndNativeReadback(t *testing.T) {
	f := startClaudeFixture(t)
	var fastScope []any
	for _, option := range f.options {
		if option.Select == nil || option.Select.Id != "speed" || option.Select.Options.Ungrouped == nil {
			continue
		}
		for _, value := range *option.Select.Options.Ungrouped {
			if value.Value == "fast" {
				fastScope, _ = value.Meta["tui-go.models"].([]any)
			}
		}
	}
	if len(fastScope) == 0 {
		t.Fatal("Fast option has no model applicability metadata")
	}
	var hasCapable, hasIncapable bool
	for _, model := range fastScope {
		hasCapable = hasCapable || model == "claude-model-live-a"
		hasIncapable = hasIncapable || model == "claude-model-live-b"
	}
	if !hasCapable || hasIncapable {
		t.Fatalf("Fast scope = %v, want only capable models", fastScope)
	}
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.setSpeed(t, "fast"); err != nil {
		t.Fatalf("Fast selection was not confirmed: %v", err)
	}
	if err := f.setModel(t, "claude-model-live-b"); err != nil {
		t.Fatalf("switch to non-Fast model: %v", err)
	}
	if err := f.setSpeed(t, "fast"); err == nil {
		t.Fatal("accepted Fast on model without Fast capability")
	}
	entries := readFakeClaudeLog(t, f.logPath)
	var toggles []bool
	for _, entry := range entries {
		if entry.Subtype != "apply_flag_settings" {
			continue
		}
		var req struct {
			Settings struct {
				FastMode bool `json:"fastMode"`
			} `json:"settings"`
		}
		_ = json.Unmarshal(entry.Request, &req)
		toggles = append(toggles, req.Settings.FastMode)
	}
	if len(toggles) != 3 || toggles[0] || !toggles[1] || toggles[2] {
		t.Fatalf("native Fast transitions = %v, want startup Standard then on then off", toggles)
	}
}

func TestClaudeFastGenericACKDoesNotClaimEffectiveFast(t *testing.T) {
	f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_FAST_ACK_ONLY=1")
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.setSpeed(t, "fast"); err == nil {
		t.Fatal("accepted Fast when native state remained off")
	}
}

func TestClaudeBridgeNativePermissionIsOneShotAndDefaultsToDeny(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prompt   string
		choice   string
		behavior string
		updated  bool
	}{
		{name: "allow once", prompt: "permission-allow", choice: "allow-once", behavior: "allow", updated: true},
		{name: "deny", prompt: "permission-deny", choice: "deny", behavior: "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startClaudeFixture(t)
			if err := f.setModel(t, "claude-model-live-a"); err != nil {
				t.Fatalf("select discovered model: %v", err)
			}
			f.client.permission = func(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				if request.ToolCall.ToolCallId != "tool-fake-1" {
					t.Errorf("tool call ID = %q", request.ToolCall.ToolCallId)
				}
				if got, want := len(request.Options), 2; got != want {
					t.Errorf("permission option count = %d, want %d", got, want)
				} else {
					if request.Options[0].OptionId != "deny" || request.Options[0].Kind != acp.PermissionOptionKindRejectOnce {
						t.Errorf("first/default-to-no option = %+v, want one-shot deny", request.Options[0])
					}
					if request.Options[1].OptionId != "allow-once" || request.Options[1].Kind != acp.PermissionOptionKindAllowOnce {
						t.Errorf("second permission option = %+v, want allow once", request.Options[1])
					}
				}
				for _, option := range request.Options {
					if option.Kind == acp.PermissionOptionKindAllowAlways || option.Kind == acp.PermissionOptionKindRejectAlways {
						t.Errorf("persistent permission option exposed: %+v", option)
					}
				}
				return testPermissionChoice(tc.choice), nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			response, err := f.prompt(ctx, tc.prompt)
			if err != nil {
				t.Fatalf("prompt after permission: %v", err)
			}
			if response.StopReason != acp.StopReasonEndTurn {
				t.Fatalf("stop reason = %q, want end_turn", response.StopReason)
			}
			if got := f.client.permissionCalls.Load(); got != 1 {
				t.Fatalf("permission callback called %d times, want once", got)
			}
			entries := waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool {
				return providerResponseCount(entries, "permission-fake-1") == 1
			})
			var toolResponse struct {
				Behavior     string         `json:"behavior"`
				UpdatedInput map[string]any `json:"updatedInput"`
			}
			for _, entry := range entries {
				if entry.Kind == "provider_response" && entry.RequestID == "permission-fake-1" {
					if err := json.Unmarshal(entry.Response, &toolResponse); err != nil {
						t.Fatal(err)
					}
				}
			}
			if toolResponse.Behavior != tc.behavior {
				t.Fatalf("provider permission behavior = %q, want %q", toolResponse.Behavior, tc.behavior)
			}
			if tc.updated && toolResponse.UpdatedInput["command"] != "printf safe" {
				t.Fatalf("allow-once did not preserve the original input: %+v", toolResponse.UpdatedInput)
			}
			if !tc.updated && toolResponse.UpdatedInput != nil {
				t.Fatalf("deny unexpectedly supplied updated input: %+v", toolResponse.UpdatedInput)
			}
			if got := providerResponseCount(entries, "permission-fake-1"); got != 1 {
				t.Fatalf("provider received %d final permission responses, want exactly one", got)
			}
			args := findFakeEntry(entries, "argv")
			if args == nil || !containsPair(args.Args, "--permission-mode", "default") || !containsPair(args.Args, "--permission-prompt-tool", "stdio") {
				t.Fatalf("Claude startup did not use the default, permission-prompting mode: %+v", args)
			}
			for _, arg := range args.Args {
				if arg == "--dangerously-skip-permissions" || arg == "--permission-mode=bypassPermissions" {
					t.Fatalf("startup bypassed permission prompts with flag %q", arg)
				}
			}
		})
	}
}

func TestClaudeBridgeRejectsUnsupportedInteractiveApproval(t *testing.T) {
	f := startClaudeFixture(t)
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := f.prompt(ctx, "permission-requires-interaction"); err == nil {
		t.Fatal("interactive approval request was reported as a successful turn")
	}
	if got := f.client.permissionCalls.Load(); got != 0 {
		t.Fatalf("unsupported interactive approval reached the ordinary permission UI %d times", got)
	}
	entries := waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool {
		return providerResponseCount(entries, "permission-fake-1") == 1
	})
	var response fakeClaudeLogEntry
	for _, entry := range entries {
		if entry.Kind == "provider_response" && entry.RequestID == "permission-fake-1" {
			response = entry
		}
	}
	if response.Subtype != "error" {
		t.Fatalf("interactive provider request response subtype = %q, want error", response.Subtype)
	}
}

func TestClaudeBridgeCancelAndWithdrawInvalidatePendingPermission(t *testing.T) {
	t.Run("turn cancellation interrupts runtime", func(t *testing.T) {
		f := startClaudeFixture(t)
		if err := f.setModel(t, "claude-model-live-a"); err != nil {
			t.Fatal(err)
		}
		resolveAsCancelled := make(chan struct{})
		f.client.permission = func(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			<-resolveAsCancelled
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		type promptResult struct {
			response acp.PromptResponse
			err      error
		}
		promptDone := make(chan promptResult, 1)
		go func() {
			response, err := f.prompt(ctx, "permission-cancel")
			promptDone <- promptResult{response: response, err: err}
		}()
		select {
		case <-f.client.permissions:
		case <-ctx.Done():
			t.Fatal("permission request did not reach ACP client")
		}
		_ = f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session})
		waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool {
			for _, entry := range entries {
				if entry.Kind == "host_control" && entry.Subtype == "interrupt" {
					return true
				}
			}
			return false
		})
		close(resolveAsCancelled)
		select {
		case result := <-promptDone:
			if result.err != nil {
				t.Fatalf("cancelled turn errored instead of returning cancelled: %v", result.err)
			}
			if result.response.StopReason != acp.StopReasonCancelled {
				t.Fatalf("native abort result stop reason = %q, want cancelled", result.response.StopReason)
			}
		case <-ctx.Done():
			t.Fatal("cancelled prompt did not finish")
		}
		entries := waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool {
			for _, entry := range entries {
				if entry.Kind == "host_control" && entry.Subtype == "interrupt" {
					return true
				}
			}
			return false
		})
		if providerResponseCount(entries, "permission-fake-1") != 0 {
			t.Fatalf("canceled permission was answered to the provider: %+v", entries)
		}
	})

	t.Run("idle cancel cannot overtake a prompt blocked on settings", func(t *testing.T) {
		f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_DELAY_MODEL=claude-model-live-b")
		if err := f.setModel(t, "claude-model-live-a"); err != nil {
			t.Fatal(err)
		}
		settingCtx, settingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer settingCancel()
		settingDone := make(chan error, 1)
		go func() {
			_, err := f.conn.SetSessionConfigOption(settingCtx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: f.session, ConfigId: "model", Value: "claude-model-live-b",
			}})
			settingDone <- err
		}()
		waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool { return entryCount(entries, "model_ack_delayed") == 1 })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session}); err != nil {
			t.Fatalf("send idle cancel: %v", err)
		}
		promptDone := make(chan error, 1)
		go func() { _, err := f.prompt(ctx, "cancel-must-not-run"); promptDone <- err }()
		select {
		case err := <-settingDone:
			if err == nil {
				t.Fatal("pending model setting unexpectedly succeeded after idle cancellation retired the bridge")
			}
		case <-ctx.Done():
			t.Fatal("pending model setting did not unblock after idle cancellation")
		}
		select {
		case err := <-promptDone:
			if err == nil {
				t.Fatal("prompt blocked behind the setting was accepted after idle cancellation")
			}
		case <-ctx.Done():
			t.Fatal("prompt did not reject after idle cancellation")
		}
		if got := entryCount(readFakeClaudeLog(t, f.logPath), "user"); got != 0 {
			t.Fatalf("idle cancellation allowed a later prompt to reach Claude %d times", got)
		}
	})

	t.Run("interruption intent cannot hide an unrelated provider failure", func(t *testing.T) {
		f := startClaudeFixture(t)
		if err := f.setModel(t, "claude-model-live-a"); err != nil {
			t.Fatal(err)
		}
		resolveAsCancelled := make(chan struct{})
		f.client.permission = func(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			<-resolveAsCancelled
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		type promptResult struct {
			response acp.PromptResponse
			err      error
		}
		promptDone := make(chan promptResult, 1)
		go func() {
			response, err := f.prompt(ctx, "permission-cancel-failure")
			promptDone <- promptResult{response: response, err: err}
		}()
		select {
		case <-f.client.permissions:
		case <-ctx.Done():
			t.Fatal("permission request did not reach ACP client")
		}
		_ = f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session})
		waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool {
			for _, entry := range entries {
				if entry.Kind == "host_control" && entry.Subtype == "interrupt" {
					return true
				}
			}
			return false
		})
		close(resolveAsCancelled)
		select {
		case result := <-promptDone:
			if result.err == nil {
				t.Fatalf("unrelated provider failure was reported as successful cancellation (%q)", result.response.StopReason)
			}
		case <-ctx.Done():
			t.Fatal("failed interrupted turn did not finish")
		}
	})

	t.Run("provider withdraws and late UI choice is ignored", func(t *testing.T) {
		f := startClaudeFixture(t)
		if err := f.setModel(t, "claude-model-live-a"); err != nil {
			t.Fatal(err)
		}
		releaseChoice := make(chan struct{})
		choiceReturned := make(chan struct{})
		f.client.permission = func(_ context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
			<-releaseChoice
			close(choiceReturned)
			return testPermissionChoice("allow-once"), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		promptDone := make(chan error, 1)
		go func() { _, err := f.prompt(ctx, "permission-withdraw"); promptDone <- err }()
		select {
		case <-f.client.permissions:
		case <-ctx.Done():
			t.Fatal("permission request did not reach ACP client")
		}
		waitFakeClaudeLog(t, f.logPath, func(entries []fakeClaudeLogEntry) bool { return entryCount(entries, "withdraw") == 1 })
		time.Sleep(50 * time.Millisecond)
		close(releaseChoice)
		select {
		case <-choiceReturned:
		case <-ctx.Done():
			t.Fatal("test permission callback did not return")
		}
		select {
		case err := <-promptDone:
			if err != nil {
				t.Fatalf("runtime's terminal success was lost after withdrawal: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("prompt did not finish after runtime result")
		}
		time.Sleep(300 * time.Millisecond)
		entries := readFakeClaudeLog(t, f.logPath)
		if got := providerResponseCount(entries, "permission-fake-1"); got != 0 {
			t.Fatalf("late approval was sent after provider withdrew request: %+v", entries)
		}
	})
}

func TestClaudeBridgeNeverReportsFailedDisconnectedOrMalformedTurnsAsSuccess(t *testing.T) {
	for _, prompt := range []string{"failed", "disconnect", "malformed", "oversized"} {
		t.Run(prompt, func(t *testing.T) {
			f := startClaudeFixture(t)
			if err := f.setModel(t, "claude-model-live-a"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if response, err := f.prompt(ctx, prompt); err == nil {
				t.Fatalf("terminal failure returned successful stop reason %q", response.StopReason)
			}
		})
	}
}

func TestClaudeBridgeRetainsTerminalResultWrittenImmediatelyBeforeExit(t *testing.T) {
	f := startClaudeFixture(t)
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := f.prompt(ctx, "result-then-exit")
	if err != nil {
		t.Fatalf("terminal result was lost when the runtime exited immediately: %v", err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", response.StopReason)
	}
	select {
	case <-f.endpoint.Disconnected:
	case <-ctx.Done():
		t.Fatal("runtime process did not disconnect after terminal result")
	}
	f.endpoint.Stop()
	select {
	case <-f.endpoint.Exited:
	case <-ctx.Done():
		t.Fatal("runtime process was not reaped after Stop")
	}
}

func TestClaudeNativeExitClosesPipesRetainedByOwnedDescendant(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-heartbeat")
	f := startClaudeFixture(t,
		"TUI_GO_CLAUDE_TEST_DESCENDANT_MARKER="+marker,
		"TUI_GO_CLAUDE_TEST_INHERIT_STDIO=1",
	)
	waitHeartbeat(t, marker, "")
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	response, err := f.prompt(ctx, "result-then-exit")
	if err != nil {
		t.Fatalf("terminal result was lost when the native parent exited: %v", err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", response.StopReason)
	}
	select {
	case <-f.endpoint.Disconnected:
	case <-ctx.Done():
		t.Fatal("native exit did not become visible while an owned child held stdio open")
	}
	// The native parent has exited, but the child still owns inherited stdout
	// and stderr descriptors. The wait/drain timeout must still lead to owned
	// process-group cleanup without an explicit endpoint Stop.
	_ = waitHeartbeatStable(t, marker, 300*time.Millisecond)
	f.endpoint.Stop()
	select {
	case <-f.endpoint.Exited:
	case <-ctx.Done():
		t.Fatal("owned descendant cleanup did not complete")
	}
}

func TestClaudeBridgeRejectsOversizedPromptBeforeRuntimeDelivery(t *testing.T) {
	f := startClaudeFixture(t)
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	text := strings.Repeat("x", maxFrame)
	_, err := f.conn.Prompt(ctx, acp.PromptRequest{SessionId: f.session, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: text}}}})
	if err == nil {
		t.Fatal("oversized prompt was accepted by the Claude bridge")
	}
	if got := entryCount(readFakeClaudeLog(t, f.logPath), "user"); got != 0 {
		t.Fatalf("oversized prompt reached runtime %d times", got)
	}
}

func findFakeEntry(entries []fakeClaudeLogEntry, kind string) *fakeClaudeLogEntry {
	for i := range entries {
		if entries[i].Kind == kind {
			return &entries[i]
		}
	}
	return nil
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestClaudePermissionModesAreAcknowledgedBeforePrompt(t *testing.T) {
	for _, mode := range []string{"default", "acceptEdits", "auto", "bypassPermissions"} {
		t.Run(mode, func(t *testing.T) {
			f := startClaudeFixture(t)
			if err := f.setModel(t, "claude-model-live-a"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: acp.SessionConfigValueId(mode)}})
			if err != nil {
				t.Fatalf("select %s: %v", mode, err)
			}
			if got := selectedMode(response.ConfigOptions); got != mode {
				t.Fatalf("acknowledged mode = %q, want %q", got, mode)
			}
			if _, err := f.prompt(ctx, "safe"); err != nil {
				t.Fatal(err)
			}
			entries := readFakeClaudeLog(t, f.logPath)
			modeAck, prompt := -1, -1
			for i, entry := range entries {
				if entry.Kind == "host_control" && entry.Subtype == "set_permission_mode" {
					var request struct {
						Mode string `json:"mode"`
					}
					_ = json.Unmarshal(entry.Request, &request)
					if request.Mode != mode {
						t.Fatalf("native mode = %q, want %q", request.Mode, mode)
					}
					modeAck = i
				}
				if entry.Kind == "user" {
					prompt = i
				}
			}
			if modeAck < 0 || prompt <= modeAck {
				t.Fatalf("mode was not sent before prompt: %+v", entries)
			}
		})
	}
}

func TestClaudePermissionModeRejectsUnsupportedAndUnacknowledgedValues(t *testing.T) {
	f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_REJECT_MODE=auto")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, mode := range []string{"unknown", "manual", "auto"} {
		if _, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: acp.SessionConfigValueId(mode)}}); err == nil {
			t.Fatalf("%q was accepted", mode)
		}
	}
	if got := selectedMode(f.options); got != "default" {
		t.Fatalf("initial mode = %q", got)
	}
	if got := entryCount(readFakeClaudeLog(t, f.logPath), "user"); got != 0 {
		t.Fatalf("rejected configuration dispatched %d prompts", got)
	}
}

func TestClaudePermissionModeRequiresMatchingNativeAcknowledgment(t *testing.T) {
	f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_ACK_MODE=default")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: "auto"}}); err == nil {
		t.Fatal("mismatched native permission acknowledgment was accepted")
	}
}

func TestClaudeAutoPermissionFollowsModelCapability(t *testing.T) {
	f := startClaudeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	selected, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "model", Value: "claude-model-live-b"}})
	if err != nil {
		t.Fatal(err)
	}
	sawAuto := false
	for _, option := range selected.ConfigOptions {
		if option.Select == nil || option.Select.Id != "mode" || option.Select.Options.Ungrouped == nil {
			continue
		}
		for _, choice := range *option.Select.Options.Ungrouped {
			if choice.Value == "auto" {
				sawAuto = true
				encoded, _ := json.Marshal(choice.Meta["tui-go.models"])
				if strings.Contains(string(encoded), "claude-model-live-b") || !strings.Contains(string(encoded), "claude-model-live-a") {
					t.Fatalf("incorrect Auto model scope: %s", encoded)
				}
			}
		}
	}
	if !sawAuto {
		t.Fatal("target-model Auto removed from live catalogue")
	}
	if _, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: "auto"}}); err == nil {
		t.Fatal("Auto accepted for model that reports no Auto support")
	}
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: "auto"}}); err != nil {
		t.Fatal("supported model cannot regain Auto", err)
	}

}

func TestClaudeSwitchFromAutoToUnsupportedModelRestoresSupervised(t *testing.T) {
	f := startClaudeFixture(t)
	if err := f.setModel(t, "claude-model-live-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "mode", Value: "auto"}}); err != nil {
		t.Fatal(err)
	}
	selected, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: f.session, ConfigId: "model", Value: "claude-model-live-b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedMode(selected.ConfigOptions); got != "default" {
		t.Fatalf("permission mode after model switch = %q, want supervised default", got)
	}
}

func selectedMode(options []acp.SessionConfigOption) string {
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "mode" {
			return string(option.Select.CurrentValue)
		}
	}
	return ""
}
