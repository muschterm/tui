package acpbridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const maxFrame = 8 << 20

type claudeModel struct {
	Value            string `json:"value"`
	ResolvedModel    string `json:"resolvedModel"`
	Name             string `json:"displayName"`
	Description      string `json:"description"`
	SupportsAutoMode bool   `json:"supportsAutoMode"`
	SupportsFastMode bool   `json:"supportsFastMode"`
}

// Claude Code accepts full model IDs through set_model even when its picker
// initialize list includes only aliases. These versions are documented by
// Claude Code and the T3 Code versioned catalog; account/provider policy can
// still reject one at selection or execution time. Keep this list separate
// from the native discovery result so the UI can label it honestly.
var claudeLegacyModels = []claudeModel{
	{Value: "claude-fable-5", Name: "Legacy · Fable 5"},
	{Value: "claude-opus-5", Name: "Legacy · Opus 5"},
	{Value: "claude-opus-4-8", Name: "Legacy · Opus 4.8"},
	{Value: "claude-opus-4-7", Name: "Legacy · Opus 4.7"},
	{Value: "claude-opus-4-6", Name: "Legacy · Opus 4.6"},
	{Value: "claude-opus-4-5", Name: "Legacy · Opus 4.5"},
	{Value: "claude-sonnet-4-6", Name: "Legacy · Sonnet 4.6"},
}

type controlResult struct {
	data json.RawMessage
	err  error
}
type claudeTurn struct {
	userID      string
	ctx         context.Context
	cancel      context.CancelFunc
	result      chan controlResult
	interrupted bool
	streamed    bool
}
type claude struct {
	h                              *host
	p                              *process
	mu                             sync.Mutex
	writeMu                        sync.Mutex
	setup                          sync.Mutex
	next                           uint64
	pending                        map[string]chan controlResult
	requests                       map[string]context.CancelFunc
	seen                           map[string]bool
	session                        string
	opened, initialized, questions bool
	models                         []claudeModel
	usage                          providerUsageState
	model                          string
	fast, fastAvailable            bool
	fastUnavailable                string
	permissionMode                 string
	turn                           *claudeTurn
	readDone                       chan struct{}
	retired                        bool
}

func newClaude(h *host) backend {
	return &claude{h: h, session: randomID(), pending: map[string]chan controlResult{}, requests: map[string]context.CancelFunc{}, seen: map[string]bool{}, readDone: make(chan struct{})}
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (c *claude) Close() { c.h.mu.Lock(); p := c.h.process; c.h.mu.Unlock(); p.close() }

func (c *claude) Handle(ctx context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
	// Configuration and session setup are serialized; cancellation must be able
	// to pass a running Prompt and a pending permission callback.
	if method == "initialize" {
		return c.initialize(ctx, raw)
	}
	var req struct {
		SessionID  string            `json:"sessionId"`
		Cwd        string            `json:"cwd"`
		ConfigID   string            `json:"configId"`
		Value      string            `json:"value"`
		Prompt     []json.RawMessage `json:"prompt"`
		McpServers []json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalid(err)
	}
	c.mu.Lock()
	initialized, opened := c.initialized, c.opened
	c.mu.Unlock()
	if !initialized {
		return nil, invalid(errors.New("initialize first"))
	}
	if method == "session/new" {
		c.setup.Lock()
		defer c.setup.Unlock()
		if req.Cwd != c.h.cwd || len(req.McpServers) > 0 {
			return nil, invalid(errors.New("checkout mismatch or unsupported ACP MCP configuration"))
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.opened {
			return nil, invalid(errors.New("one session per bridge"))
		}
		c.opened = true
		return map[string]any{"sessionId": c.session, "configOptions": c.options()}, nil
	}
	if !opened || req.SessionID != c.session {
		return nil, invalid(errors.New("unknown session"))
	}
	switch method {
	case "session/set_config_option":
		c.setup.Lock()
		defer c.setup.Unlock()
		c.mu.Lock()
		active := c.turn != nil
		models := append([]claudeModel(nil), c.models...)
		models = append(models, claudeLegacyModels...)
		currentModel, currentMode, currentFast := c.model, c.permissionMode, c.fast
		c.mu.Unlock()
		if active {
			return nil, invalid(errors.New("settings are read-only during a turn"))
		}
		var control map[string]any
		modeReset := false
		switch req.ConfigID {
		case "model":
			found, supportsAuto := false, false
			for _, m := range models {
				if m.Value == req.Value || m.ResolvedModel != "" && m.ResolvedModel == req.Value {
					found, supportsAuto = true, m.SupportsAutoMode
				}
			}
			if !found {
				return nil, invalid(errors.New("model was not discovered"))
			}
			if currentMode == "auto" && !supportsAuto {
				confirmed, err := c.control(ctx, map[string]any{"subtype": "set_permission_mode", "mode": "default"})
				var state struct {
					Mode string `json:"mode"`
				}
				if err != nil || json.Unmarshal(confirmed, &state) != nil || state.Mode != "default" {
					c.Close()
					return nil, internal(errors.New("claude could not confirm supervised permissions before model change"))
				}
				modeReset = true
				currentMode = "default"
			}
			control = map[string]any{"subtype": "set_model", "model": req.Value}
		case "speed":
			if req.Value == "fast" && !c.fastAvailable || !claudeModelSupportsFast(models, currentModel) || req.Value != "standard" && req.Value != "fast" {
				return nil, invalid(errors.New("selected Claude model does not support the requested speed"))
			}
			control = map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"fastMode": req.Value == "fast"}}
		case "mode":
			if !claudePermissionMode(req.Value) {
				return nil, invalid(errors.New("unsupported permission mode"))
			}
			if req.Value == "auto" && !claudeModelSupportsAuto(models, currentModel) {
				return nil, invalid(errors.New("selected model does not support Claude Auto permissions"))
			}
			control = map[string]any{"subtype": "set_permission_mode", "mode": req.Value}
		default:
			return nil, invalid(errors.New("unsupported setting"))
		}
		ack, err := c.control(ctx, control)
		if err != nil {
			if modeReset {
				c.Close() // Native permissions changed; retire on uncertain model transition.
			}
			return nil, internal(err)
		}
		if req.ConfigID == "mode" {
			var confirmed struct {
				Mode string `json:"mode"`
			}
			if json.Unmarshal(ack, &confirmed) != nil || confirmed.Mode != req.Value {
				return nil, internal(errors.New("claude did not confirm the selected permission mode"))
			}
		}
		if req.ConfigID == "speed" {
			if err := c.confirmFast(ctx, req.Value == "fast"); err != nil {
				c.Close()
				return nil, internal(err)
			}
		}
		if req.ConfigID == "model" && currentFast && !claudeModelSupportsFast(models, req.Value) {
			if err := c.setFast(ctx, false); err != nil {
				c.Close()
				return nil, internal(fmt.Errorf("claude model changed but Fast could not be disabled: %w", err))
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if req.ConfigID == "model" {
			c.model = req.Value
			if modeReset {
				c.permissionMode = "default"
			}
		} else if req.ConfigID == "mode" {
			c.permissionMode = req.Value
		} else {
			c.fast = req.Value == "fast"
		}
		return map[string]any{"configOptions": c.options()}, nil
	case "session/prompt":
		return c.prompt(ctx, req.Prompt)
	case "session/cancel":
		c.writeMu.Lock()
		c.mu.Lock()
		t := c.turn
		if t != nil {
			t.interrupted = true
			t.cancel()
		} else {
			// Cancel may overtake the concurrently dispatched ACP Prompt handler.
			// Retire this connection instead of allowing that prompt to run later.
			c.retired = true
		}
		c.mu.Unlock()
		var id string
		var ch chan controlResult
		var err error
		if t != nil {
			id, ch, err = c.startControl(map[string]any{"subtype": "interrupt"})
		}
		c.writeMu.Unlock()
		if t == nil {
			c.Close()
			return nil, nil
		}
		if t != nil {
			cancelCtx, cancel := context.WithTimeout(c.h.ctx, 10*time.Second)
			defer cancel()
			if err == nil {
				_, err = c.awaitControl(cancelCtx, id, ch)
			}
			if err != nil {
				c.Close()
				return nil, internal(err)
			}
		}
		return nil, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (c *claude) initialize(ctx context.Context, raw json.RawMessage) (any, *acp.RequestError) {
	c.setup.Lock()
	defer c.setup.Unlock()
	if c.initialized {
		return nil, invalid(errors.New("already initialized"))
	}
	var req struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			Elicitation json.RawMessage `json:"elicitation"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(raw, &req) != nil || req.ProtocolVersion != 1 {
		return nil, invalid(errors.New("ACP v1 required"))
	}
	// Claude Code's non-interactive Fast contract requires opt-in at launch.
	// Reset it before exposing the session so no prompt inherits Fast silently.
	p, err := c.h.launch("-p", "--settings", `{"fastMode":true}`, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-prompt-tool", "stdio", "--allow-dangerously-skip-permissions", "--permission-mode", "default", "--session-id", c.session)
	if err != nil {
		return nil, internal(err)
	}
	c.p = p
	go c.read()
	r, err := c.control(ctx, map[string]any{"subtype": "initialize"})
	if err != nil {
		c.Close()
		return nil, internal(err)
	}
	var info struct {
		Models                 []claudeModel `json:"models"`
		Mode                   string        `json:"current_permission_mode"`
		FastModeState          string        `json:"fast_mode_state"`
		FastModeDisabledReason string        `json:"fast_mode_disabled_reason"`
	}
	if json.Unmarshal(r, &info) != nil || len(info.Models) == 0 || len(info.Models) > 128 || info.Mode != "default" {
		c.Close()
		return nil, internal(errors.New("unsupported Claude initialization/settings response"))
	}
	seen := map[string]bool{}
	for _, m := range info.Models {
		if m.Value == "" || m.Name == "" || seen[m.Value] {
			c.Close()
			return nil, internal(errors.New("invalid Claude model catalogue"))
		}
		seen[m.Value] = true
	}
	if info.FastModeState == "on" {
		if err := c.setFast(ctx, false); err != nil {
			c.Close()
			return nil, internal(fmt.Errorf("claude did not confirm Standard startup speed: %w", err))
		}
		info.FastModeState = "off"
	}
	c.mu.Lock()
	c.models = info.Models
	c.model = ""
	c.permissionMode = info.Mode
	c.fast = info.FastModeState == "on"
	c.fastAvailable = info.FastModeState == "on" || info.FastModeState == "off" && (info.FastModeDisabledReason == "" || info.FastModeDisabledReason == "sdk_opt_in_required")
	c.fastUnavailable = info.FastModeDisabledReason
	c.questions = len(req.ClientCapabilities.Elicitation) > 0
	c.initialized = true
	c.mu.Unlock()
	return map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "tui-go-claude", "version": Version}, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true}, "_meta": map[string]any{"questionDialect": QuestionDialect, "usageDialect": UsageDialect}}, "authMethods": []any{}}, nil
}

// options requires c.mu. No effort/speed/capacity setting is invented from a
// model name; unsupported controls remain unavailable in the common composer.
func (c *claude) options() []any {
	values := []any{}
	seen := make(map[string]bool, len(c.models)*2)
	for _, m := range c.models {
		name := m.Name
		if m.ResolvedModel != "" {
			name += " (" + m.ResolvedModel + ")"
		}
		values = append(values, map[string]any{"value": m.Value, "name": name})
		seen[m.Value] = true
	}
	for _, m := range claudeLegacyModels {
		if seen[m.Value] {
			continue
		}
		seen[m.Value] = true
		values = append(values, map[string]any{"value": m.Value, "name": m.Name, "description": "Documented full model ID; account/provider availability is verified by Claude when selected"})
	}
	for _, m := range c.models {
		if m.ResolvedModel == "" || seen[m.ResolvedModel] {
			continue
		}
		seen[m.ResolvedModel] = true
		values = append(values, map[string]any{"value": m.ResolvedModel, "name": m.ResolvedModel + " (pinned)"})
	}
	modeValues := []any{
		map[string]any{"value": "default", "name": "Supervised", "description": "Ask before tools that require permission"},
		map[string]any{"value": "acceptEdits", "name": "Auto-accept edits", "description": "Approve edits; ask for other actions"},
	}
	autoModels := make([]string, 0, len(c.models)*2)
	for _, model := range c.models {
		if model.SupportsAutoMode {
			autoModels = append(autoModels, model.Value)
			if model.ResolvedModel != "" {
				autoModels = append(autoModels, model.ResolvedModel)
			}
		}
	}
	// Catalogue describes captured target models, not only the previous turn.
	if len(autoModels) > 0 {
		modeValues = append(modeValues, map[string]any{"value": "auto", "name": "Auto", "description": "Claude automatically reviews permission decisions", "_meta": map[string]any{"tui-go.models": autoModels}})
	}
	modeValues = append(modeValues, map[string]any{"value": "bypassPermissions", "name": "Full access", "description": "Bypass Claude permission prompts"})
	options := []any{
		map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": c.model, "options": values},
		map[string]any{"id": "mode", "name": "Permissions", "category": "mode", "type": "select", "currentValue": c.permissionMode, "options": modeValues},
	}
	{
		fastModels := make([]string, 0, len(c.models)*2)
		seenFast := make(map[string]bool)
		for _, model := range c.models {
			if !model.SupportsFastMode {
				continue
			}
			for _, id := range []string{model.Value, model.ResolvedModel} {
				if id != "" && !seenFast[id] {
					seenFast[id] = true
					fastModels = append(fastModels, id)
				}
			}
		}
		if len(fastModels) != 0 {
			current := "standard"
			if c.fast {
				current = "fast"
			}
			choices := []any{map[string]any{"value": "standard", "name": "Standard", "_meta": map[string]any{"tui-go.models": fastModels}}}
			description := ""
			if c.fastAvailable {
				choices = append(choices, map[string]any{"value": "fast", "name": "Fast", "_meta": map[string]any{"tui-go.models": fastModels}})
			} else {
				reason := strings.ReplaceAll(c.fastUnavailable, "_", " ")
				if reason == "" {
					reason = "runtime did not report an available Fast control"
				}
				description = "Fast unavailable · " + reason
			}
			options = append(options, map[string]any{"id": "speed", "name": "Speed", "description": description, "category": "model_config", "type": "select", "currentValue": current, "options": choices})
		}
	}
	return options
}

func claudeModelSupportsAuto(models []claudeModel, value string) bool {
	for _, model := range models {
		if model.Value == value || model.ResolvedModel != "" && model.ResolvedModel == value {
			return model.SupportsAutoMode
		}
	}
	return false
}

func claudeModelSupportsFast(models []claudeModel, value string) bool {
	for _, model := range models {
		if model.Value == value || model.ResolvedModel != "" && model.ResolvedModel == value {
			return model.SupportsFastMode
		}
	}
	return false
}

// apply_flag_settings acknowledges accepted keys without echoing effective
// state. Re-read the native initialize state before telling the client Fast is
// active or disabled; a generic ACK alone is insufficient.
func (c *claude) confirmFast(ctx context.Context, enabled bool) error {
	raw, err := c.control(ctx, map[string]any{"subtype": "initialize"})
	if err != nil {
		return err
	}
	var state struct {
		FastModeState  string `json:"fast_mode_state"`
		DisabledReason string `json:"fast_mode_disabled_reason"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	want := "off"
	if enabled {
		want = "on"
	}
	if state.FastModeState != want {
		return fmt.Errorf("claude did not confirm Fast mode %s (reported %q)", want, state.FastModeState)
	}
	if enabled && state.DisabledReason != "" {
		return fmt.Errorf("claude Fast is unavailable: %s", state.DisabledReason)
	}
	return nil
}

func (c *claude) setFast(ctx context.Context, enabled bool) error {
	if _, err := c.control(ctx, map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"fastMode": enabled}}); err != nil {
		return err
	}
	if err := c.confirmFast(ctx, enabled); err != nil {
		return err
	}
	c.mu.Lock()
	c.fast = enabled
	c.mu.Unlock()
	return nil
}

func claudePermissionMode(value string) bool {
	switch value {
	case "default", "acceptEdits", "auto", "bypassPermissions":
		return true
	}
	return false
}

// write requires writeMu. No state mutex is held over pipe I/O.
func (c *claude) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errors.New("claude input exceeds frame bound")
	}
	if f, ok := c.p.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if err = f.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		defer func() { _ = f.SetWriteDeadline(time.Time{}) }()
	}
	_, err = c.p.stdin.Write(append(b, '\n'))
	return err
}
func (c *claude) control(ctx context.Context, req any) (json.RawMessage, error) {
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return nil, err
	}
	id, ch, err := c.startControl(req)
	c.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.awaitControl(ctx, id, ch)
}
func (c *claude) startControl(req any) (string, chan controlResult, error) {
	c.mu.Lock()
	c.next++
	id := fmt.Sprintf("host-%d", c.next)
	ch := make(chan controlResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return "", nil, err
	}
	return id, ch, nil
}
func (c *claude) awaitControl(ctx context.Context, id string, ch chan controlResult) (json.RawMessage, error) {
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	select {
	case r := <-ch:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.p.ctx.Done():
		select {
		case <-c.readDone:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		select {
		case r := <-ch:
			return r.data, r.err
		default:
		}
		return nil, errors.New("claude disconnected")
	}
}

func (c *claude) prompt(ctx context.Context, blocks []json.RawMessage) (any, *acp.RequestError) {
	c.setup.Lock()
	content, err := claudeContent(blocks)
	if err != nil {
		c.setup.Unlock()
		return nil, invalid(err)
	}
	c.writeMu.Lock()
	c.mu.Lock()
	if c.turn != nil || c.model == "" || c.retired || c.p.ctx.Err() != nil || ctx.Err() != nil {
		c.mu.Unlock()
		c.writeMu.Unlock()
		c.setup.Unlock()
		return nil, invalid(errors.New("turn active or no explicit model selected"))
	}
	tctx, cancel := context.WithCancel(c.p.ctx)
	t := &claudeTurn{userID: randomID(), ctx: tctx, cancel: cancel, result: make(chan controlResult, 1)}
	c.turn = t
	c.mu.Unlock()
	err = c.write(map[string]any{"type": "user", "session_id": c.session, "message": map[string]any{"role": "user", "content": content}, "parent_tool_use_id": nil, "uuid": t.userID})
	c.writeMu.Unlock()
	c.setup.Unlock()
	defer func() {
		c.mu.Lock()
		t.cancel()
		if c.turn == t {
			c.turn = nil
		}
		c.mu.Unlock()
	}()
	if err != nil {
		return nil, internal(err)
	}
	var result controlResult
	select {
	case result = <-t.result:
	case <-ctx.Done():
		c.Close()
		return nil, internal(ctx.Err())
	case <-c.p.ctx.Done():
		select {
		case <-c.readDone:
		case <-ctx.Done():
			return nil, internal(ctx.Err())
		case <-time.After(3 * time.Second):
		}
		select {
		case result = <-t.result:
		default:
			return nil, internal(errors.New("claude disconnected before a terminal result"))
		}
	}
	if result.err != nil {
		return nil, internal(result.err)
	}
	var r struct {
		Subtype        string   `json:"subtype"`
		IsError        bool     `json:"is_error"`
		Errors         []string `json:"errors"`
		TerminalReason string   `json:"terminal_reason"`
		UserID         string   `json:"user_message_uuid"`
		UserIDs        []string `json:"user_message_uuids"`
	}
	if json.Unmarshal(result.data, &r) != nil {
		return nil, internal(errors.New("malformed Claude result"))
	}
	if (r.UserID != "" && r.UserID != t.userID) || len(r.UserIDs) > 1 || (len(r.UserIDs) == 1 && r.UserIDs[0] != t.userID) || (r.Subtype == "success" && !r.IsError && r.UserID == "" && len(r.UserIDs) == 0) {
		c.Close()
		return nil, internal(errors.New("claude result belongs to a different or merged input; no replay"))
	}
	c.mu.Lock()
	interrupted := t.interrupted
	c.mu.Unlock()
	if interrupted && (r.UserID == t.userID || len(r.UserIDs) == 1 && r.UserIDs[0] == t.userID) && (r.TerminalReason == "aborted_streaming" || r.TerminalReason == "aborted_tools") {
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	if r.Subtype != "success" || r.IsError {
		return nil, internal(fmt.Errorf("claude turn failed (%s): %s", r.Subtype, strings.Join(r.Errors, "; ")))
	}
	return map[string]any{"stopReason": "end_turn"}, nil
}

func claudeContent(blocks []json.RawMessage) ([]any, error) {
	if len(blocks) == 0 {
		return nil, errors.New("empty prompt")
	}
	out := []any{}
	for _, raw := range blocks {
		var b struct{ Type, Text, Data, MimeType string }
		if json.Unmarshal(raw, &b) != nil {
			return nil, errors.New("invalid content")
		}
		switch b.Type {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		case "image":
			if b.Data == "" || !strings.HasPrefix(b.MimeType, "image/") {
				return nil, errors.New("unsupported image")
			}
			out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": b.MimeType, "data": b.Data}})
		default:
			return nil, errors.New("unsupported prompt block")
		}
	}
	return out, nil
}

func (c *claude) read() {
	s := bufio.NewScanner(c.p.stdout)
	s.Buffer(make([]byte, 64<<10), maxFrame)
	defer close(c.readDone)
	defer c.Close()
	for s.Scan() {
		var m struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Request   json.RawMessage `json:"request"`
			Response  struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			} `json:"response"`
			SessionID string `json:"session_id"`
		}
		line := append(json.RawMessage(nil), s.Bytes()...)
		if json.Unmarshal(line, &m) != nil {
			return
		}
		switch m.Type {
		case "control_response":
			c.mu.Lock()
			ch := c.pending[m.Response.RequestID]
			c.mu.Unlock()
			if ch != nil {
				r := controlResult{data: m.Response.Response}
				if m.Response.Subtype != "success" {
					r.err = fmt.Errorf("claude control rejected: %s", m.Response.Error)
				}
				select {
				case ch <- r:
				default:
				}
			}
		case "control_cancel_request":
			c.writeMu.Lock()
			c.mu.Lock()
			if cancel := c.requests[m.RequestID]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
			c.writeMu.Unlock()
		case "control_request":
			c.request(m.RequestID, m.Request)
		default:
			if m.SessionID != "" && m.SessionID != c.session {
				return
			}
			c.event(line)
		}
	}
}

func (c *claude) event(raw json.RawMessage) {
	var m struct {
		Type  string `json:"type"`
		Event struct {
			Type  string                                `json:"type"`
			Delta struct{ Type, Text, Thinking string } `json:"delta"`
		} `json:"event"`
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
		Parent *string `json:"parent_tool_use_id"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	c.mu.Lock()
	t := c.turn
	c.mu.Unlock()
	if t == nil || m.Parent != nil {
		return
	}
	c.emitUsage(raw)
	if m.Type == "result" {
		c.writeMu.Lock()
		t.cancel()
		c.writeMu.Unlock()
		select {
		case t.result <- controlResult{data: raw}:
		default:
		}
		return
	}
	if m.Type == "stream_event" {
		d := m.Event.Delta
		if m.Event.Type == "content_block_delta" && (d.Type == "text_delta" || d.Type == "thinking_delta") {
			kind, text := "agent_message_chunk", d.Text
			if d.Type == "thinking_delta" {
				kind, text = "agent_thought_chunk", d.Thinking
			}
			c.mu.Lock()
			t.streamed = true
			c.mu.Unlock()
			_ = c.h.update(c.h.ctx, c.session, map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}})
		}
		return
	}
	if m.Type != "assistant" && m.Type != "user" {
		return
	}
	c.mu.Lock()
	streamed := t.streamed
	if m.Type == "assistant" {
		t.streamed = false
	}
	c.mu.Unlock()
	for _, rawBlock := range m.Message.Content {
		var b struct {
			Type, Text, Thinking, ID, Name string
			Input                          json.RawMessage
			ToolUseID                      string `json:"tool_use_id"`
			IsError                        bool   `json:"is_error"`
			Content                        json.RawMessage
		}
		_ = json.Unmarshal(rawBlock, &b)
		switch b.Type {
		case "text", "thinking":
			if m.Type == "assistant" && !streamed {
				kind, text := "agent_message_chunk", b.Text
				if b.Type == "thinking" {
					kind, text = "agent_thought_chunk", b.Thinking
				}
				_ = c.h.update(c.h.ctx, c.session, map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}})
			}
		case "tool_use":
			_ = c.h.update(c.h.ctx, c.session, map[string]any{"sessionUpdate": "tool_call", "toolCallId": b.ID, "title": b.Name, "status": "pending", "rawInput": b.Input})
		case "tool_result":
			status := "completed"
			if b.IsError {
				status = "failed"
			}
			_ = c.h.update(c.h.ctx, c.session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": b.ToolUseID, "status": status, "rawOutput": b.Content})
		}
	}
}

func (c *claude) request(id string, raw json.RawMessage) {
	c.mu.Lock()
	t := c.turn
	if id == "" || t == nil || t.ctx.Err() != nil || c.seen[id] || len(c.seen) >= 1024 || len(c.requests) >= 32 {
		c.mu.Unlock()
		go c.Close()
		return
	}
	ctx, cancel := context.WithCancel(t.ctx)
	c.requests[id] = cancel
	c.seen[id] = true
	c.mu.Unlock()
	go func() {
		defer func() { cancel(); c.mu.Lock(); delete(c.requests, id); c.mu.Unlock() }()
		response, err := c.permission(ctx, id, raw)
		c.writeMu.Lock()
		c.mu.Lock()
		live := c.turn == t && ctx.Err() == nil
		c.mu.Unlock()
		if !live {
			c.writeMu.Unlock()
			return
		}
		r := map[string]any{"subtype": "success", "request_id": id, "response": response}
		if err != nil {
			r = map[string]any{"subtype": "error", "request_id": id, "error": err.Error()}
		}
		err = c.write(map[string]any{"type": "control_response", "response": r})
		c.writeMu.Unlock()
		if err != nil {
			c.Close()
		}
	}()
}

func (c *claude) permission(ctx context.Context, id string, raw json.RawMessage) (any, error) {
	var r struct {
		Subtype     string          `json:"subtype"`
		Tool        string          `json:"tool_name"`
		Input       json.RawMessage `json:"input"`
		ToolID      string          `json:"tool_use_id"`
		AgentID     string          `json:"agent_id"`
		Interaction bool            `json:"requires_user_interaction"`
		Reason      string          `json:"decision_reason"`
		BlockedPath string          `json:"blocked_path"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &r) != nil || r.Subtype != "can_use_tool" || r.ToolID == "" {
		return nil, errors.New("unsupported Claude control request")
	}
	deny := map[string]any{"behavior": "deny", "message": "User declined or this interaction is unavailable"}
	if r.Tool == "AskUserQuestion" {
		if !c.questions || r.AgentID != "" {
			return nil, errors.New("native questions unavailable for this source")
		}
		form, questions, err := claudeQuestionForm(c.session, r.ToolID, id, raw, r.Input)
		if err != nil {
			return nil, err
		}
		answer, err := acp.SendRequest[json.RawMessage](c.h.conn, ctx, "elicitation/create", form)
		if err != nil {
			return nil, err
		}
		updated, err := claudeQuestionAnswer(r.Input, questions, answer)
		if err != nil {
			return nil, err
		}
		if updated == nil {
			return deny, nil
		}
		return map[string]any{"behavior": "allow", "updatedInput": updated}, nil
	}
	if r.Interaction {
		return nil, errors.New("tool requires an unsupported interactive approval surface")
	}
	// Only once/deny is exposed. Never apply permission_suggestions, broaden a
	// supplied ask rule, or persist grants. Deny is first even for default_to_no.
	title := r.Tool
	if r.Reason != "" {
		title += " — " + r.Reason
	}
	tool := map[string]any{"toolCallId": r.ToolID, "title": title, "status": "pending", "rawInput": json.RawMessage(raw)}
	if r.BlockedPath != "" {
		tool["locations"] = []any{map[string]any{"path": r.BlockedPath}}
	}
	response, err := acp.SendRequest[acp.RequestPermissionResponse](c.h.conn, ctx, "session/request_permission", map[string]any{"sessionId": c.session, "toolCall": tool, "options": []any{map[string]any{"optionId": "deny", "name": "Deny", "kind": "reject_once"}, map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"}}})
	if err != nil {
		return nil, err
	}
	if response.Outcome.Selected != nil && string(response.Outcome.Selected.OptionId) == "allow-once" {
		return map[string]any{"behavior": "allow", "updatedInput": r.Input}, nil
	}
	return deny, nil
}
