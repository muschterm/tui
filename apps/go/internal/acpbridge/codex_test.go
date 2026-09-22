package acpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	fakeCodexRole    = "TUI_GO_CODEX_TEST_ROLE"
	fakeCodexLog     = "TUI_GO_CODEX_TEST_LOG"
	fakeCodexBinary  = "TUI_GO_CODEX_TEST_BINARY"
	fakeCodexOutcome = "TUI_GO_CODEX_TEST_OUTCOME"
	fakeCodexRelease = "TUI_GO_CODEX_TEST_RELEASE"
	fakeCodexRuntime = "runtime"
)

type codexRuntimeEntry struct {
	Kind   string          `json:"kind"`
	Method string          `json:"method,omitempty"`
	ID     json.RawMessage `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type codexRuntimeMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func TestCodexRuntimeHelper(t *testing.T) {
	if os.Getenv(fakeCodexRole) == fakeCodexRuntime {
		os.Exit(runFakeCodexAppServer())
	}
}

func runFakeCodexAppServer() int {
	log := func(entry codexRuntimeEntry) {
		path := os.Getenv(fakeCodexLog)
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_ = json.NewEncoder(f).Encode(entry)
			_ = f.Close()
		}
	}
	write := func(value any) error {
		frame, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(frame, '\n'))
		return err
	}
	respond := func(id json.RawMessage, result any) error {
		frame, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return write(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(frame)})
	}
	notify := func(method string, params any) error {
		return write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}

	mode := os.Getenv(fakeCodexOutcome)
	settingsSeen := 0
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxFrame+1024)
	for scanner.Scan() {
		var message codexRuntimeMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		if message.Method == "" {
			log(codexRuntimeEntry{Kind: "response", ID: message.ID, Result: message.Result, Error: message.Error})
			if string(message.ID) == `"approval-1"` {
				if mode == "bad-approval" {
					return 0
				}
				if notify("turn/completed", map[string]any{"threadId": "thread-fake-1", "turn": map[string]any{"id": "turn-fake-1", "status": "completed"}}) != nil {
					return 1
				}
			}
			continue
		}
		log(codexRuntimeEntry{Kind: "request", Method: message.Method, ID: message.ID, Params: message.Params})
		switch message.Method {
		case "initialize":
			if respond(message.ID, map[string]any{"userAgent": "fake-codex", "platformFamily": "unix"}) != nil {
				return 1
			}
		case "model/list":
			if respond(message.ID, map[string]any{"data": []any{
				map[string]any{
					"id": "gpt-6-luna", "model": "gpt-6-luna", "displayName": "Codex Luna",
					"supportedReasoningEfforts": []any{
						map[string]any{"reasoningEffort": "low", "description": "Low"},
						map[string]any{"reasoningEffort": "medium", "description": "Medium"},
						map[string]any{"reasoningEffort": "high", "description": "High"},
					}, "defaultReasoningEffort": "medium", "inputModalities": []string{"text"},
				},
				map[string]any{"id": "fake-other", "model": "fake-other", "displayName": "Other model", "supportedReasoningEfforts": []any{}},
			}, "nextCursor": nil}) != nil {
				return 1
			}
		case "thread/start":
			if respond(message.ID, map[string]any{"thread": map[string]any{"id": "thread-fake-1"}, "cwd": os.Getenv("TUI_GO_CODEX_TEST_CWD"), "model": "", "reasoningEffort": nil}) != nil {
				return 1
			}
		case "thread/resume":
			if respond(message.ID, map[string]any{"thread": map[string]any{"id": "thread-fake-1"}, "cwd": os.Getenv("TUI_GO_CODEX_TEST_CWD"), "model": "gpt-6-luna", "reasoningEffort": "low"}) != nil {
				return 1
			}
		case "thread/settings/update":
			settingsSeen++
			if mode == "hold-settings" && settingsSeen >= 3 {
				release := os.Getenv(fakeCodexRelease)
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(release); err == nil {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if respond(message.ID, map[string]any{}) != nil {
				return 1
			}
		case "turn/start":
			if mode == "lost-start" {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(os.Getenv(fakeCodexRelease)); err == nil {
						return 0
					}
					time.Sleep(5 * time.Millisecond)
				}
				return 0
			}
			if mode == "completed" || mode == "failed" || mode == "completed-exit" {
				status := "completed"
				if mode == "failed" {
					status = "failed"
				}
				if notify("turn/started", map[string]any{"threadId": "thread-fake-1", "turn": map[string]any{"id": "turn-fake-1", "status": "inProgress"}}) != nil {
					return 1
				}
				_ = notify("item/agentMessage/delta", map[string]any{"threadId": "other-thread", "turnId": "turn-fake-1", "delta": "wrong-session"})
				_ = notify("item/agentMessage/delta", map[string]any{"threadId": "thread-fake-1", "turnId": "other-turn", "delta": "wrong-turn"})
				_ = notify("item/agentMessage/delta", map[string]any{"threadId": "thread-fake-1", "turnId": "turn-fake-1", "delta": "hello"})
				_ = notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": "thread-fake-1", "turnId": "turn-fake-1", "delta": "thinking"})
				terminal := map[string]any{"threadId": "thread-fake-1", "turn": map[string]any{"id": "turn-fake-1", "status": status}}
				if status == "failed" {
					terminal["turn"].(map[string]any)["error"] = map[string]any{"message": "fake turn failure"}
				}
				_ = notify("turn/completed", terminal)
				if respond(message.ID, map[string]any{"turn": map[string]any{"id": "turn-fake-1", "status": "inProgress"}}) != nil {
					return 1
				}
				if mode == "completed-exit" {
					return 0
				}
				continue
			}
			if respond(message.ID, map[string]any{"turn": map[string]any{"id": "turn-fake-1", "status": "inProgress"}}) != nil {
				return 1
			}
			if mode == "approval" || mode == "bad-approval" {
				decisions := []any{"accept", "acceptForSession", "decline", "cancel"}
				if mode == "bad-approval" {
					decisions = []any{"unknown-decision-shape"}
				}
				if write(map[string]any{"jsonrpc": "2.0", "id": "approval-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{
					"threadId": "thread-fake-1", "turnId": "turn-fake-1", "itemId": "item-fake-1", "approvalId": "approval-fake-1",
					"command": "printf safe", "cwd": os.Getenv("TUI_GO_CODEX_TEST_CWD"), "availableDecisions": decisions,
				}}) != nil {
					return 1
				}
			}
		case "turn/interrupt":
			// Terminal notification before the response exercises the SDK's
			// callback barrier. Holding a turn lock over the RPC deadlocks here.
			if notify("turn/completed", map[string]any{"threadId": "thread-fake-1", "turn": map[string]any{"id": "turn-fake-1", "status": "interrupted"}}) != nil {
				return 1
			}
			if respond(message.ID, map[string]any{}) != nil {
				return 1
			}
		default:
			if respond(message.ID, map[string]any{}) != nil {
				return 1
			}
		}
	}
	return 0
}

type codexFixture struct {
	endpoint *Endpoint
	conn     *acp.ClientSideConnection
	client   *claudeTestClient
	session  acp.SessionId
	cwd      string
	logPath  string
	options  []acp.SessionConfigOption
	release  string
}

func startCodexFixture(t *testing.T, outcome string) *codexFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake App Server shim is a Unix executable")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "checkout")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "app-server.jsonl")
	shim := filepath.Join(root, "codex")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec \"$"+fakeCodexBinary+"\" -test.run='^TestCodexRuntimeHelper$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "release-settings")
	env := append(os.Environ(), fakeCodexBinary+"="+os.Args[0], fakeCodexRole+"="+fakeCodexRuntime, fakeCodexLog+"="+logPath, fakeCodexOutcome+"="+outcome, "TUI_GO_CODEX_TEST_CWD="+cwd, fakeCodexRelease+"="+release)
	endpoint, err := Open(context.Background(), "codex", shim, cwd, env, io.Discard)
	if err != nil {
		t.Fatalf("Open Codex bridge: %v", err)
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
	return &codexFixture{endpoint: endpoint, conn: conn, client: client, session: created.SessionId, cwd: cwd, logPath: logPath, options: created.ConfigOptions, release: release}
}

func (f *codexFixture) selectModelAndEffort(t *testing.T) {
	t.Helper()
	if err := f.setOption("model", "gpt-6-luna"); err != nil {
		t.Fatalf("select discovered Codex Luna model: %v", err)
	}
	if err := f.setOption("effort", "low"); err != nil {
		t.Fatalf("select discovered low reasoning effort: %v", err)
	}
}

func (f *codexFixture) setOption(id, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := f.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: f.session, ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
	}})
	return err
}

func (f *codexFixture) prompt(ctx context.Context) (acp.PromptResponse, error) {
	return f.conn.Prompt(ctx, acp.PromptRequest{SessionId: f.session, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Type: "text", Text: "codex fake prompt"}}}})
}

func codexRuntimeLog(t *testing.T, path string) []codexRuntimeEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var entries []codexRuntimeEntry
	s := bufio.NewScanner(strings.NewReader(string(b)))
	for s.Scan() {
		var entry codexRuntimeEntry
		if err := json.Unmarshal(s.Bytes(), &entry); err != nil {
			t.Fatalf("parse fake App Server log: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func waitCodexRuntimeLog(t *testing.T, path string, predicate func([]codexRuntimeEntry) bool) []codexRuntimeEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries := codexRuntimeLog(t, path)
		if predicate(entries) {
			return entries
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries := codexRuntimeLog(t, path)
	t.Fatalf("timed out waiting for fake App Server; entries=%+v", entries)
	return nil
}

func codexRequestCount(entries []codexRuntimeEntry, method string) int {
	n := 0
	for _, entry := range entries {
		if entry.Kind == "request" && entry.Method == method {
			n++
		}
	}
	return n
}

func TestCodexDiscoversLunaLowAndAcknowledgesSettingsBeforePrompt(t *testing.T) {
	f := startCodexFixture(t, "completed")
	modelValues := modelOptionValues(t, f.options)
	if !stringInSlice(modelValues, "gpt-6-luna") {
		t.Fatalf("Codex model catalogue omitted Luna: %v", modelValues)
	}
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := f.prompt(ctx)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", response.StopReason)
	}
	entries := waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool {
		return codexRequestCount(entries, "turn/start") == 1
	})
	var startParams map[string]any
	var lastSettings map[string]any
	settingsIndex, startIndex := -1, -1
	for i, entry := range entries {
		if entry.Kind != "request" {
			continue
		}
		if entry.Method == "thread/settings/update" {
			_ = json.Unmarshal(entry.Params, &lastSettings)
			settingsIndex = i
		}
		if entry.Method == "turn/start" {
			_ = json.Unmarshal(entry.Params, &startParams)
			startIndex = i
		}
	}
	if settingsIndex < 0 || startIndex <= settingsIndex {
		t.Fatalf("settings were not acknowledged before turn/start: %+v", entries)
	}
	if lastSettings["model"] != "gpt-6-luna" || lastSettings["effort"] != "low" {
		t.Fatalf("effective settings update = %+v, want Luna / low", lastSettings)
	}
	if startParams["model"] != "gpt-6-luna" || startParams["effort"] != "low" {
		t.Fatalf("turn/start settings = %+v, want Luna / low", startParams)
	}
	var textChunks, thoughtChunks []string
	for len(f.client.updates) > 0 {
		update := <-f.client.updates
		if update.Update.AgentMessageChunk != nil && update.Update.AgentMessageChunk.Content.Text != nil {
			textChunks = append(textChunks, update.Update.AgentMessageChunk.Content.Text.Text)
		}
		if update.Update.AgentThoughtChunk != nil && update.Update.AgentThoughtChunk.Content.Text != nil {
			thoughtChunks = append(thoughtChunks, update.Update.AgentThoughtChunk.Content.Text.Text)
		}
	}
	if strings.Join(textChunks, "") != "hello" || strings.Join(thoughtChunks, "") != "thinking" {
		t.Fatalf("wrong-session/turn stream filtering failed: message=%q thought=%q", strings.Join(textChunks, ""), strings.Join(thoughtChunks, ""))
	}
}

func TestCodexTerminalFailureIsNotReportedAsCompletion(t *testing.T) {
	f := startCodexFixture(t, "failed")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if response, err := f.prompt(ctx); err == nil || response.StopReason == acp.StopReasonEndTurn {
		t.Fatalf("failed provider turn was reported as success: response=%+v err=%v", response, err)
	}
}

func TestCodexCancelIsSessionScopedAndInterruptsExactTurnOnce(t *testing.T) {
	f := startCodexFixture(t, "hold")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	promptDone := make(chan error, 1)
	go func() {
		response, err := f.prompt(ctx)
		if err == nil && response.StopReason != acp.StopReasonCancelled {
			err = errors.New("prompt did not return the cancelled stop reason")
		}
		promptDone <- err
	}()
	waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool { return codexRequestCount(entries, "turn/start") == 1 })
	if err := f.conn.Cancel(ctx, acp.CancelNotification{SessionId: "different-session"}); err != nil {
		t.Fatalf("wrong-session cancel: %v", err)
	}
	if err := f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session}); err != nil {
		t.Fatalf("active-session cancel: %v", err)
	}
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled prompt did not finish")
	}
	entries := waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool { return codexRequestCount(entries, "turn/interrupt") != 0 })
	if got := codexRequestCount(entries, "turn/interrupt"); got != 1 {
		t.Fatalf("provider received %d turn/interrupt requests, want exactly one", got)
	}
	for _, entry := range entries {
		if entry.Kind == "request" && entry.Method == "turn/interrupt" {
			var params map[string]string
			if err := json.Unmarshal(entry.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params["threadId"] != "thread-fake-1" || params["turnId"] != "turn-fake-1" {
				t.Fatalf("interrupt targeted wrong provider scope: %+v", params)
			}
		}
	}
}

func TestCodexCancelDuringSettingsAckPreventsTurnStart(t *testing.T) {
	f := startCodexFixture(t, "hold-settings")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	promptDone := make(chan error, 1)
	go func() {
		response, err := f.prompt(ctx)
		if err == nil && response.StopReason != acp.StopReasonCancelled {
			err = errors.New("prompt did not return the cancelled stop reason")
		}
		promptDone <- err
	}()
	waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool {
		return codexRequestCount(entries, "thread/settings/update") >= 3
	})
	if err := f.conn.Cancel(ctx, acp.CancelNotification{SessionId: f.session}); err != nil {
		t.Fatalf("cancel while settings are pending: %v", err)
	}
	if err := os.WriteFile(f.release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("prompt did not stop after settings acknowledgement")
	}
	if got := codexRequestCount(codexRuntimeLog(t, f.logPath), "turn/start"); got != 0 {
		t.Fatalf("turn/start was sent after cancellation during settings acknowledgement (%d calls)", got)
	}
}

func TestCodexNativeApprovalPreservesOnlyOfferedDecision(t *testing.T) {
	f := startCodexFixture(t, "approval")
	f.selectModelAndEffort(t)
	f.client.permission = func(_ context.Context, request acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
		if request.ToolCall.ToolCallId != "item-fake-1:approval-fake-1" {
			return acp.RequestPermissionResponse{}, errors.New("approval changed the native tool identity")
		}
		for _, option := range request.Options {
			if option.Name == "Allow for this session" {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: option.OptionId, Outcome: "selected"}}}, nil
			}
		}
		return acp.RequestPermissionResponse{}, errors.New("provider's session approval choice was not offered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.prompt(ctx); err != nil {
		t.Fatalf("prompt with native approval: %v", err)
	}
	entries := waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool {
		for _, entry := range entries {
			if entry.Kind == "response" && string(entry.ID) == `"approval-1"` {
				return true
			}
		}
		return false
	})
	var approvalResult map[string]any
	for _, entry := range entries {
		if entry.Kind == "response" && string(entry.ID) == `"approval-1"` {
			if err := json.Unmarshal(entry.Result, &approvalResult); err != nil {
				t.Fatalf("decode fake native approval response: %v; entry=%+v", err, entry)
			}
		}
	}
	if approvalResult["decision"] != "acceptForSession" {
		t.Fatalf("native approval decision = %+v, want exact offered acceptForSession", approvalResult)
	}
}

func TestCodexUnknownApprovalShapeFailsClosed(t *testing.T) {
	f := startCodexFixture(t, "bad-approval")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.prompt(ctx); err == nil {
		t.Fatal("unknown Codex approval decision shape did not fail the turn")
	}
	if got := f.client.permissionCalls.Load(); got != 0 {
		t.Fatalf("unknown approval reached the ordinary permission UI %d times", got)
	}
	entries := waitCodexRuntimeLog(t, f.logPath, func(entries []codexRuntimeEntry) bool {
		for _, entry := range entries {
			if entry.Kind == "response" && string(entry.ID) == `"approval-1"` && len(entry.Error) > 0 {
				return true
			}
		}
		return false
	})
	for _, entry := range entries {
		if entry.Kind == "response" && string(entry.ID) == `"approval-1"` && len(entry.Result) > 0 {
			t.Fatalf("unknown decision was answered with a fabricated approval: %s", entry.Result)
		}
	}
}

func TestCodexCompletionBeforeNativeExitWinsOverDisconnect(t *testing.T) {
	f := startCodexFixture(t, "completed-exit")
	f.selectModelAndEffort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := f.prompt(ctx)
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("queued completion lost to native process exit: response=%+v err=%v", response, err)
	}
}

func stringInSlice(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
