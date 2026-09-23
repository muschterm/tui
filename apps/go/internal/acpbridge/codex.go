package acpbridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	codexName          = "Codex"
	codexVersion       = "0.1.0"
	maxModelPages      = 8
	maxModels          = 256
	maxEfforts         = 64
	maxBuffered        = 128
	maxBufferedSize    = 1 << 20
	providerTimeout    = 30 * time.Second
	nativeWriteTimeout = 5 * time.Second
	codexGrantKey      = "_tuiCodexApprovalGuard"
	codexStartKey      = "_tuiCodexStartGuard"
)

// codexBridge owns one installed Codex App Server process and maps its
// experimental v2 protocol onto the stable ACP v1 methods used by the app.
// It deliberately does not implement Codex auth, billing, MCP, or steering.
type codexBridge struct {
	h *host

	mu          sync.Mutex
	operationMu sync.Mutex
	initDone    chan struct{}
	initErr     error
	initialized bool
	closed      bool
	questions   bool
	requests    codexRequestLedger

	proc *process
	conn *acp.Connection

	models           []codexModel
	threadID         string
	sessionID        string
	cwd              string
	selectedModel    string
	selectedEffort   string
	selectedSpeed    string
	selectedMode     string
	retired          bool
	eventSignal      chan struct{}
	grantSequence    uint64
	approvalEpoch    uint64
	pendingApprovals map[uint64]context.CancelFunc
	grants           map[string]codexGrant
	active           *codexTurn
}

type codexTurn struct {
	callCtx        context.Context
	startToken     string
	startAttempted bool
	sessionID      string
	threadID       string
	turnID         string
	dispatchMu     sync.Mutex
	starting       bool
	draining       bool
	cancel         bool
	interruptSent  bool
	startErr       error
	started        chan struct{}
	startedOnce    sync.Once
	done           chan codexOutcome
	doneOnce       sync.Once
	pending        []codexEvent
	pendingBytes   int
	overflow       bool
	deliveryErr    error
}

type codexOutcome struct {
	stopReason acp.StopReason
	err        error
}

type codexModel struct {
	ID                     string   `json:"id"`
	Model                  string   `json:"model"`
	DisplayName            string   `json:"displayName"`
	Description            string   `json:"description"`
	IsDefault              bool     `json:"isDefault"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
	DefaultServiceTier     string   `json:"defaultServiceTier"`
	AdditionalSpeedTiers   []string `json:"additionalSpeedTiers"`
	ServiceTiers           []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"serviceTiers"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
	InputModalities []string `json:"inputModalities"`
}

type codexModelPage struct {
	Data       []codexModel `json:"data"`
	NextCursor *string      `json:"nextCursor"`
}

type codexThreadInfo struct {
	ID string `json:"id"`
}

type codexThreadStartResponse struct {
	Thread          codexThreadInfo `json:"thread"`
	Cwd             string          `json:"cwd"`
	Model           string          `json:"model"`
	ReasoningEffort *string         `json:"reasoningEffort"`
	ServiceTier     *string         `json:"serviceTier"`
}

type codexTurnInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexTurnStartResponse struct {
	Turn codexTurnInfo `json:"turn"`
}

type codexEvent struct {
	method   string
	threadID string
	turnID   string
	params   json.RawMessage
}

type approvalChoice struct {
	option     acp.PermissionOption
	raw        json.RawMessage
	name       string
	grantToken string
}

type codexGrant struct {
	turn      *codexTurn
	ctx       context.Context
	epoch     uint64
	pendingID uint64
	cancel    context.CancelFunc
}

func newCodex(h *host) backend {
	return &codexBridge{
		h: h, eventSignal: make(chan struct{}, 1),
		pendingApprovals: make(map[uint64]context.CancelFunc), grants: make(map[string]codexGrant),
	}
}

func (b *codexBridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	p, active := b.proc, b.active
	b.mu.Unlock()
	if active != nil {
		b.finish(active, codexOutcome{err: errors.New("codex App Server closed before the turn completed")})
	}
	if p != nil {
		p.close()
	}
}

func (b *codexBridge) Handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch method {
	case acp.AgentMethodInitialize:
		var req acp.InitializeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalid(err)
		}
		if err := req.Validate(); err != nil {
			return nil, invalid(err)
		}
		var extensions struct {
			ClientCapabilities struct {
				Elicitation json.RawMessage `json:"elicitation"`
			} `json:"clientCapabilities"`
		}
		_ = json.Unmarshal(params, &extensions)
		b.mu.Lock()
		b.questions = len(extensions.ClientCapabilities.Elicitation) > 0
		b.mu.Unlock()
		if err := b.ensureInitialized(ctx); err != nil {
			return nil, internal(err)
		}
		title := codexName
		return acp.InitializeResponse{
			ProtocolVersion: acp.ProtocolVersionNumber,
			AgentInfo:       &acp.Implementation{Name: "tui-go-codex", Title: &title, Version: codexVersion},
			AgentCapabilities: acp.AgentCapabilities{
				Meta:               map[string]any{"questionDialect": CodexQuestionDialect, "usageDialect": UsageDialect},
				LoadSession:        true,
				PromptCapabilities: acp.PromptCapabilities{Image: false, Audio: false, EmbeddedContext: false},
			},
			AuthMethods: []acp.AuthMethod{},
		}, nil
	case acp.AgentMethodSessionNew:
		return b.newSession(ctx, params)
	case acp.AgentMethodSessionLoad:
		return b.loadSession(ctx, params)
	case acp.AgentMethodSessionSetConfigOption:
		return b.setConfigOption(ctx, params)
	case acp.AgentMethodSessionPrompt:
		return b.prompt(ctx, params)
	case acp.AgentMethodSessionCancel:
		return nil, b.cancel(ctx, params)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (b *codexBridge) ensureInitialized(ctx context.Context) error {
	b.mu.Lock()
	if b.initialized {
		b.mu.Unlock()
		return nil
	}
	if b.initDone != nil {
		done := b.initDone
		b.mu.Unlock()
		select {
		case <-done:
			b.mu.Lock()
			err := b.initErr
			b.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if b.closed {
		b.mu.Unlock()
		return errors.New("codex bridge is closed")
	}
	b.initDone = make(chan struct{})
	done := b.initDone
	b.mu.Unlock()

	err := b.startProvider(ctx)
	b.mu.Lock()
	b.initErr = err
	b.initialized = err == nil
	close(done)
	b.mu.Unlock()
	return err
}

func (b *codexBridge) startProvider(ctx context.Context) error {
	args := []string{"app-server"}
	b.mu.Lock()
	questions := b.questions
	b.mu.Unlock()
	if questions {
		args = append(args, "--enable", "default_mode_request_user_input")
	}
	p, err := b.h.launch(args...)
	if err != nil {
		return fmt.Errorf("start installed Codex App Server: %w", err)
	}
	conn := acp.NewConnection(b.handleUpstream, newCodexNativeWriter(p.stdin, p, b), newCodexNativeReader(p.stdout, b))
	b.mu.Lock()
	b.proc, b.conn = p, conn
	b.mu.Unlock()
	go func() {
		<-conn.Done()
		b.failAfterUpstreamDrain()
	}()

	initCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	_, err = acp.SendRequest[json.RawMessage](conn, initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tui-go", "version": Version},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err != nil {
		return fmt.Errorf("initialize Codex App Server: %w", err)
	}
	models, err := b.discoverModels(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.models = models
	b.mu.Unlock()
	return nil
}

func (b *codexBridge) discoverModels(ctx context.Context) ([]codexModel, error) {
	var models []codexModel
	var cursor *string
	seenCursors := make(map[string]struct{})
	for pageNumber := 0; pageNumber < maxModelPages && len(models) < maxModels; pageNumber++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		page, err := acp.SendRequest[codexModelPage](b.upstream(), requestCtx, "model/list", params)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("discover Codex models: %w", err)
		}
		for _, model := range page.Data {
			if value := codexModelValue(model); value != "" && len(models) < maxModels {
				models = append(models, model)
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		if _, exists := seenCursors[*page.NextCursor]; exists {
			return nil, errors.New("codex model/list repeated a pagination cursor")
		}
		seenCursors[*page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	if len(models) == 0 {
		return nil, errors.New("codex model/list returned no selectable models")
	}
	return dedupeModels(models), nil
}

func (b *codexBridge) upstream() *acp.Connection {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn
}

func (b *codexBridge) newSession(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var req acp.NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	if err := req.Validate(); err != nil {
		return nil, invalid(err)
	}
	if err := b.validateWorkspace(req.Cwd, req.AdditionalDirectories, req.McpServers); err != nil {
		return nil, invalid(err)
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, internal(err)
	}
	b.operationMu.Lock()
	defer b.operationMu.Unlock()
	b.mu.Lock()
	if b.threadID != "" || b.active != nil {
		b.mu.Unlock()
		return nil, invalid(errors.New("codex bridge supports one session per connection"))
	}
	b.retired = false
	b.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	resp, err := acp.SendRequest[codexThreadStartResponse](b.upstream(), requestCtx, "thread/start", map[string]any{"cwd": req.Cwd})
	cancel()
	if err != nil {
		return nil, internal(fmt.Errorf("start Codex thread: %w", err))
	}
	if resp.Thread.ID == "" {
		return nil, internal(errors.New("codex thread/start omitted thread.id"))
	}
	if cwd := cleanAbsolute(resp.Cwd); cwd != "" && cwd != cleanAbsolute(req.Cwd) {
		return nil, internal(errors.New("codex started the thread in a different working directory"))
	}
	model, effort := b.effectiveSettings(resp.Model, resp.ReasoningEffort)
	if err := b.applySettings(ctx, resp.Thread.ID, "", "", "supervised"); err != nil {
		return nil, internal(fmt.Errorf("codex did not acknowledge supervised baseline: %w", err))
	}
	b.mu.Lock()
	b.threadID, b.sessionID, b.cwd = resp.Thread.ID, string(resp.Thread.ID), req.Cwd
	b.selectedModel, b.selectedEffort = model, effort
	b.selectedSpeed = b.modelSpeed(model, resp.ServiceTier)
	b.selectedMode = "supervised"
	options := b.configOptionsLocked()
	b.mu.Unlock()
	return acp.NewSessionResponse{SessionId: acp.SessionId(resp.Thread.ID), ConfigOptions: options}, nil
}

func (b *codexBridge) loadSession(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var req acp.LoadSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	if err := req.Validate(); err != nil {
		return nil, invalid(err)
	}
	if err := b.validateWorkspace(req.Cwd, req.AdditionalDirectories, req.McpServers); err != nil {
		return nil, invalid(err)
	}
	if len(req.SessionId) == 0 || len(req.SessionId) > 256 {
		return nil, invalid(errors.New("sessionId must contain between 1 and 256 bytes"))
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, internal(err)
	}
	b.operationMu.Lock()
	defer b.operationMu.Unlock()
	b.mu.Lock()
	if b.threadID != "" || b.active != nil {
		b.mu.Unlock()
		return nil, invalid(errors.New("codex bridge supports one session per connection"))
	}
	b.retired = false
	b.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	resp, err := acp.SendRequest[codexThreadStartResponse](b.upstream(), requestCtx, "thread/resume", map[string]any{
		"threadId": string(req.SessionId), "excludeTurns": true,
	})
	cancel()
	if err != nil {
		return nil, internal(fmt.Errorf("resume Codex thread: %w", err))
	}
	if resp.Thread.ID != string(req.SessionId) {
		return nil, internal(errors.New("codex thread/resume returned a different thread id"))
	}
	if cleanAbsolute(resp.Cwd) != cleanAbsolute(req.Cwd) {
		return nil, internal(errors.New("codex thread working directory does not match the requested checkout"))
	}
	model, effort := b.effectiveSettings(resp.Model, resp.ReasoningEffort)
	if err := b.applySettings(ctx, resp.Thread.ID, "", "", "supervised"); err != nil {
		return nil, internal(fmt.Errorf("codex did not acknowledge supervised baseline: %w", err))
	}
	b.mu.Lock()
	b.threadID, b.sessionID, b.cwd = resp.Thread.ID, string(req.SessionId), req.Cwd
	b.selectedModel, b.selectedEffort = model, effort
	b.selectedSpeed = b.modelSpeed(model, resp.ServiceTier)
	b.selectedMode = "supervised"
	options := b.configOptionsLocked()
	b.mu.Unlock()
	return acp.LoadSessionResponse{ConfigOptions: options}, nil
}

func (b *codexBridge) validateWorkspace(cwd string, additional []string, servers []acp.McpServer) error {
	if cleanAbsolute(cwd) == "" {
		return errors.New("cwd must be an absolute, clean path")
	}
	if cleanAbsolute(cwd) != cleanAbsolute(b.h.cwd) {
		return errors.New("codex session cwd must match the server-owned checkout")
	}
	if len(additional) != 0 {
		return errors.New("additionalDirectories are not supported by this Codex bridge")
	}
	if len(servers) != 0 {
		return errors.New("MCP server passthrough is not supported by this Codex bridge")
	}
	return nil
}

func cleanAbsolute(path string) string {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ""
	}
	return path
}

func (b *codexBridge) setConfigOption(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var req acp.SetSessionConfigOptionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	var sessionID acp.SessionId
	var configID acp.SessionConfigId
	var value acp.SessionConfigValueId
	if req.ValueId != nil {
		sessionID, configID, value = req.ValueId.SessionId, req.ValueId.ConfigId, req.ValueId.Value
	} else {
		return nil, invalid(errors.New("codex exposes only string model and effort settings"))
	}
	b.operationMu.Lock()
	defer b.operationMu.Unlock()
	b.mu.Lock()
	if string(sessionID) != b.sessionID || b.sessionID == "" {
		b.mu.Unlock()
		return nil, invalid(errors.New("sessionId does not match the active Codex session"))
	}
	if b.active != nil {
		b.mu.Unlock()
		return nil, invalid(errors.New("codex settings cannot change while a turn is active"))
	}
	model, effort, mode, speed := b.selectedModel, b.selectedEffort, b.selectedMode, b.selectedSpeed
	switch string(configID) {
	case "model":
		selected, ok := b.findModel(string(value))
		if !ok {
			b.mu.Unlock()
			return nil, invalid(fmt.Errorf("codex does not report model %q", value))
		}
		model = codexModelValue(selected)
		if !modelHasEffort(selected, effort) {
			effort = selected.DefaultReasoningEffort
			if !modelHasEffort(selected, effort) {
				effort = ""
			}
		}
		if !modelHasSpeed(selected, speed) {
			speed = ""
			if modelHasSpeed(selected, "default") {
				speed = "default"
			}
		}
	case "effort":
		selected, ok := b.findModel(model)
		if !ok || !modelHasEffort(selected, string(value)) {
			b.mu.Unlock()
			return nil, invalid(fmt.Errorf("codex model %q does not report reasoning effort %q", model, value))
		}
		effort = string(value)
	case "mode":
		if b.selectedMode == "" || !codexPermissionMode(string(value)) {
			b.mu.Unlock()
			return nil, invalid(fmt.Errorf("codex cannot enforce permission mode %q", value))
		}
		mode = string(value)
	case "speed":
		selected, ok := b.findModel(model)
		if !ok || !modelHasSpeed(selected, string(value)) {
			b.mu.Unlock()
			return nil, invalid(fmt.Errorf("codex model %q does not report speed tier %q", model, value))
		}
		speed = string(value)
	default:
		b.mu.Unlock()
		return nil, invalid(fmt.Errorf("codex cannot enforce configuration option %q", configID))
	}
	threadID := b.threadID
	b.mu.Unlock()
	if threadID == "" {
		return nil, invalid(errors.New("no Codex session is active"))
	}
	if err := b.applySettings(ctx, threadID, model, effort, mode, speed); err != nil {
		return nil, internal(fmt.Errorf("codex did not acknowledge selected settings: %w", err))
	}
	b.mu.Lock()
	b.selectedModel, b.selectedEffort, b.selectedMode, b.selectedSpeed = model, effort, mode, speed
	options := b.configOptionsLocked()
	b.mu.Unlock()
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (b *codexBridge) applySettings(ctx context.Context, threadID, model, effort, mode string, speed ...string) error {
	params := map[string]any{"threadId": threadID}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	if len(speed) != 0 {
		if speed[0] == "default" || speed[0] == "" {
			params["serviceTier"] = nil // Clear any previous thread override.
		} else {
			params["serviceTier"] = speed[0]
		}
	}
	if mode != "" {
		policy, sandbox, reviewer, err := b.permissionPolicy(mode)
		if err != nil {
			return err
		}
		params["approvalPolicy"], params["sandboxPolicy"], params["approvalsReviewer"] = policy, sandbox, reviewer
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	_, err := acp.SendRequest[json.RawMessage](b.upstream(), requestCtx, "thread/settings/update", params)
	return err
}

func codexPermissionMode(mode string) bool {
	switch mode {
	case "supervised", "auto", "full":
		return true
	}
	return false
}

func stringPtr(value string) *string { return &value }

// Values match the installed App Server v2 schema; these are effective runtime
// controls, not instruction text or an application-owned sandbox.
func (b *codexBridge) permissionPolicy(mode string) (any, any, string, error) {
	switch mode {
	case "supervised":
		return "on-request", map[string]any{"type": "workspaceWrite"}, "user", nil
	case "auto":
		return "on-request", map[string]any{"type": "workspaceWrite"}, "auto_review", nil
	case "full":
		return "never", map[string]any{"type": "dangerFullAccess"}, "user", nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported Codex permission mode %q", mode)
	}
}

func (b *codexBridge) effectiveSettings(model string, effort *string) (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	selected, ok := b.findModel(model)
	if !ok {
		return "", ""
	}
	selectedModel := codexModelValue(selected)
	selectedEffort := ""
	if effort != nil && modelHasEffort(selected, *effort) {
		selectedEffort = *effort
	}
	return selectedModel, selectedEffort
}

func (b *codexBridge) findModel(value string) (codexModel, bool) {
	for _, model := range b.models {
		if value == codexModelValue(model) || value == model.ID || value == model.Model {
			return model, true
		}
	}
	return codexModel{}, false
}

func codexModelValue(model codexModel) string {
	if model.Model != "" {
		return model.Model
	}
	return model.ID
}

func modelHasEffort(model codexModel, effort string) bool {
	if effort == "" {
		return false
	}
	for _, option := range model.SupportedReasoningEfforts {
		if option.ReasoningEffort == effort {
			return true
		}
	}
	return false
}

func modelHasSpeed(model codexModel, speed string) bool {
	if speed == "default" {
		return len(model.ServiceTiers) != 0 || len(model.AdditionalSpeedTiers) != 0
	}
	if speed == "" {
		return false
	}
	for _, tier := range model.ServiceTiers {
		if tier.ID == speed {
			return true
		}
	}
	if len(model.ServiceTiers) == 0 {
		for _, tier := range model.AdditionalSpeedTiers {
			if tier == speed {
				return true
			}
		}
	}
	return false
}

// modelSpeed preserves a reported thread tier only when the selected model
// advertises it. A missing or unsupported tier starts at explicit standard.
func (b *codexBridge) modelSpeed(model string, reported *string) string {
	selected, ok := b.findModel(model)
	if !ok || !modelHasSpeed(selected, "default") {
		return ""
	}
	if reported != nil && modelHasSpeed(selected, *reported) {
		return *reported
	}
	return "default"
}

func (b *codexBridge) configOptionsLocked() []acp.SessionConfigOption {
	modelValues := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(b.models))
	seenModels := make(map[string]struct{}, len(b.models))
	for _, model := range b.models {
		value := codexModelValue(model)
		if value == "" {
			continue
		}
		if _, exists := seenModels[value]; exists {
			continue
		}
		seenModels[value] = struct{}{}
		name := model.DisplayName
		if name == "" {
			name = value
		}
		modelValues = append(modelValues, acp.SessionConfigSelectOption{Name: name, Value: acp.SessionConfigValueId(value)})
	}
	modelCurrent := acp.SessionConfigValueId(b.selectedModel)
	modelCategory := acp.SessionConfigOptionCategoryModel
	options := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Category: &modelCategory, CurrentValue: modelCurrent,
		Options: acp.SessionConfigSelectOptions{Ungrouped: &modelValues}, Type: "select",
	}}}
	if b.selectedMode != "" {
		category := acp.SessionConfigOptionCategoryMode
		modeValues := acp.SessionConfigSelectOptionsUngrouped{
			{Name: "Supervised", Value: "supervised", Description: stringPtr("Ask the user before actions outside the writable workspace")},
			{Name: "Auto", Value: "auto", Description: stringPtr("Use Codex auto review for requests outside the writable workspace")},
			{Name: "Full access", Value: "full", Description: stringPtr("Run without approval prompts or Codex sandbox restrictions")},
		}
		options = append(options, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "mode", Name: "Permissions", Category: &category, CurrentValue: acp.SessionConfigValueId(b.selectedMode),
			Options: acp.SessionConfigSelectOptions{Ungrouped: &modeValues}, Type: "select",
		}})
	}
	effortValues := make(acp.SessionConfigSelectOptionsUngrouped, 0, maxEfforts)
	effortModels := make(map[string][]string)
	for _, model := range b.models {
		for _, effort := range model.SupportedReasoningEfforts {
			value := effort.ReasoningEffort
			if value == "" {
				continue
			}
			if _, exists := effortModels[value]; !exists && len(effortValues) < maxEfforts {
				effortValues = append(effortValues, acp.SessionConfigSelectOption{Name: effortLabel(value), Value: acp.SessionConfigValueId(value)})
			}
			if len(effortModels[value]) < maxModels {
				effortModels[value] = append(effortModels[value], codexModelValue(model))
			}
		}
	}
	for i := range effortValues {
		effortValues[i].Meta = map[string]any{"tui-go.models": effortModels[string(effortValues[i].Value)]}
	}
	if len(effortValues) != 0 {
		effortCurrent := acp.SessionConfigValueId(b.selectedEffort)
		category := acp.SessionConfigOptionCategoryThoughtLevel
		options = append(options, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "effort", Name: "Reasoning effort", Category: &category, CurrentValue: effortCurrent,
			Options: acp.SessionConfigSelectOptions{Ungrouped: &effortValues}, Type: "select",
		}})
	}
	values := acp.SessionConfigSelectOptionsUngrouped{}
	speedModels := make(map[string][]string)
	seen := make(map[string]bool)
	for _, model := range b.models {
		if !modelHasSpeed(model, "default") {
			continue
		}
		modelID := codexModelValue(model)
		speedModels["default"] = append(speedModels["default"], modelID)
		if !seen["default"] {
			seen["default"] = true
			values = append(values, acp.SessionConfigSelectOption{Name: "Standard", Value: "default"})
		}
		if len(model.ServiceTiers) != 0 {
			for _, tier := range model.ServiceTiers {
				if tier.ID == "" || tier.ID == "default" {
					continue
				}
				speedModels[tier.ID] = append(speedModels[tier.ID], modelID)
				if !seen[tier.ID] {
					seen[tier.ID] = true
					name := tier.Name
					if name == "" {
						name = effortLabel(tier.ID)
					}
					values = append(values, acp.SessionConfigSelectOption{Name: name, Value: acp.SessionConfigValueId(tier.ID)})
				}
			}
		} else {
			for _, tier := range model.AdditionalSpeedTiers {
				if tier == "" || tier == "default" {
					continue
				}
				speedModels[tier] = append(speedModels[tier], modelID)
				if !seen[tier] {
					seen[tier] = true
					values = append(values, acp.SessionConfigSelectOption{Name: effortLabel(tier), Value: acp.SessionConfigValueId(tier)})
				}
			}
		}
	}
	if len(values) != 0 {
		for i := range values {
			values[i].Meta = map[string]any{"tui-go.models": speedModels[string(values[i].Value)]}
		}
		category := acp.SessionConfigOptionCategory("model_config")
		options = append(options, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "speed", Name: "Speed", Category: &category, CurrentValue: acp.SessionConfigValueId(b.selectedSpeed),
			Options: acp.SessionConfigSelectOptions{Ungrouped: &values}, Type: "select",
		}})
	}
	return options
}

func effortLabel(value string) string {
	switch value {
	case "xhigh":
		return "Extra high"
	case "max":
		return "Max"
	default:
		if value == "" {
			return value
		}
		return strings.ToUpper(value[:1]) + value[1:]
	}
}

func (b *codexBridge) prompt(ctx context.Context, params json.RawMessage) (any, *acp.RequestError) {
	var req acp.PromptRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	if err := req.Validate(); err != nil {
		return nil, invalid(err)
	}
	input, err := codexPromptInput(req.Prompt)
	if err != nil {
		return nil, invalid(err)
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, internal(err)
	}
	b.operationMu.Lock()
	b.mu.Lock()
	if string(req.SessionId) != b.sessionID || b.sessionID == "" {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, invalid(errors.New("sessionId does not match the active Codex session"))
	}
	if b.retired {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, acp.NewRequestCancelled(map[string]any{"error": "Codex session was retired by a cancellation without an active turn"})
	}
	if b.active != nil {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, invalid(errors.New("codex already has an active turn"))
	}
	if b.selectedModel == "" {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, invalid(errors.New("select a model reported by Codex before sending a prompt"))
	}
	model, effort, mode, speed, threadID := b.selectedModel, b.selectedEffort, b.selectedMode, b.selectedSpeed, b.threadID
	if mode != "" && !codexPermissionMode(mode) {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, invalid(errors.New("selected Codex permission mode is no longer supported"))
	}
	selected, ok := b.findModel(model)
	if !ok || (len(selected.SupportedReasoningEfforts) != 0 && !modelHasEffort(selected, effort)) || (speed != "" && !modelHasSpeed(selected, speed)) {
		b.mu.Unlock()
		b.operationMu.Unlock()
		return nil, invalid(errors.New("selected Codex model or reasoning effort is no longer supported"))
	}
	b.grantSequence++
	a := &codexTurn{callCtx: ctx, startToken: fmt.Sprint(b.grantSequence), sessionID: string(req.SessionId), threadID: threadID, starting: true, started: make(chan struct{}), done: make(chan codexOutcome, 1)}
	b.active = a
	b.mu.Unlock()

	// The App Server must acknowledge the captured settings before any prompt
	// bytes are sent. Turn/start repeats the values to bind them to this prompt.
	if err := b.applySettings(ctx, threadID, model, effort, mode, speed); err != nil {
		b.finish(a, codexOutcome{err: fmt.Errorf("codex did not acknowledge prompt settings: %w", err)})
		b.operationMu.Unlock()
		return nil, internal(err)
	}
	b.mu.Lock()
	if b.active != a || a.cancel || ctx.Err() != nil {
		b.mu.Unlock()
		b.finish(a, codexOutcome{stopReason: acp.StopReasonCancelled})
		b.operationMu.Unlock()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	b.mu.Unlock()
	turnParams := map[string]any{
		"threadId":    threadID,
		"input":       input,
		"model":       model,
		codexStartKey: a.startToken,
	}
	if mode != "" {
		policy, sandbox, reviewer, err := b.permissionPolicy(mode)
		if err != nil {
			b.finish(a, codexOutcome{err: err})
			b.operationMu.Unlock()
			return nil, internal(err)
		}
		turnParams["approvalPolicy"], turnParams["sandboxPolicy"], turnParams["approvalsReviewer"] = policy, sandbox, reviewer
	}
	if effort != "" {
		turnParams["effort"] = effort
	}
	if speed == "" {
		turnParams["serviceTierForTurn"] = "default"
	} else {
		turnParams["serviceTierForTurn"] = speed
	}
	if req.MessageId != nil {
		turnParams["clientUserMessageId"] = *req.MessageId
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	resp, err := acp.SendRequest[codexTurnStartResponse](b.upstream(), requestCtx, "turn/start", turnParams)
	cancel()
	if err != nil {
		b.mu.Lock()
		cancelled := !a.startAttempted && (a.cancel || ctx.Err() != nil)
		b.mu.Unlock()
		if cancelled {
			b.finish(a, codexOutcome{stopReason: acp.StopReasonCancelled})
			b.operationMu.Unlock()
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		b.markStartUncertain(a, err)
		b.operationMu.Unlock()
		b.Close()
		return nil, internal(fmt.Errorf("codex turn/start outcome is uncertain: %w", err))
	}
	if resp.Turn.ID == "" {
		err = errors.New("codex turn/start omitted turn.id")
		b.markStartUncertain(a, err)
		b.operationMu.Unlock()
		return nil, internal(err)
	}
	b.startResolved(a, resp.Turn.ID)
	b.operationMu.Unlock()

	if shouldInterrupt := b.cancelRequested(a); shouldInterrupt {
		_ = b.interrupt(a)
	}
	select {
	case outcome := <-a.done:
		if outcome.err != nil {
			return nil, internal(outcome.err)
		}
		return acp.PromptResponse{StopReason: outcome.stopReason}, nil
	case <-ctx.Done():
		_ = b.interrupt(a)
		return nil, acp.NewRequestCancelled(map[string]any{"error": ctx.Err().Error()})
	case <-b.h.ctx.Done():
		return nil, internal(errors.New("codex bridge closed while waiting for turn completion"))
	}
}

func codexPromptInput(blocks []acp.ContentBlock) ([]map[string]string, error) {
	var text strings.Builder
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			text.WriteString(block.Text.Text)
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			text.WriteString(block.Resource.Resource.TextResourceContents.Text)
		case block.ResourceLink != nil:
			text.WriteString(block.ResourceLink.Uri)
		case block.Image != nil:
			return nil, errors.New("image prompts are unavailable through this Codex bridge")
		case block.Audio != nil:
			return nil, errors.New("audio prompts are unavailable through this Codex bridge")
		default:
			return nil, errors.New("unsupported ACP prompt content block")
		}
	}
	if text.Len() == 0 {
		return nil, errors.New("codex prompt must contain text")
	}
	return []map[string]string{{"type": "text", "text": text.String()}}, nil
}

func (b *codexBridge) startResolved(a *codexTurn, turnID string) {
	b.mu.Lock()
	if b.active != a {
		b.mu.Unlock()
		return
	}
	a.turnID = turnID
	a.starting = false
	a.draining = true
	a.startedOnce.Do(func() { close(a.started) })
	b.mu.Unlock()
	b.drain(a)
}

func (b *codexBridge) markStartUncertain(a *codexTurn, err error) {
	b.mu.Lock()
	if b.active == a {
		a.startErr = err
		a.starting = false
		a.startedOnce.Do(func() { close(a.started) })
	}
	b.mu.Unlock()
}

func (b *codexBridge) drain(a *codexTurn) {
	for {
		b.mu.Lock()
		if b.active != a {
			b.mu.Unlock()
			return
		}
		if len(a.pending) == 0 {
			a.draining = false
			b.mu.Unlock()
			return
		}
		batch := a.pending
		a.pending = nil
		a.pendingBytes = 0
		b.mu.Unlock()
		for _, event := range batch {
			if event.turnID != "" && event.turnID != a.turnID {
				continue
			}
			b.dispatchEvent(context.Background(), a, event)
		}
	}
}

func (b *codexBridge) cancel(ctx context.Context, params json.RawMessage) *acp.RequestError {
	var req acp.CancelNotification
	if err := json.Unmarshal(params, &req); err != nil {
		return invalid(err)
	}
	b.mu.Lock()
	a := b.active
	if string(req.SessionId) != b.sessionID || b.sessionID == "" {
		b.mu.Unlock()
		return nil
	}
	if a == nil {
		// A Cancel can overtake the Prompt request during session initialization.
		// Retire this ACP connection so a late prompt cannot start after Stop.
		b.retired = true
		b.mu.Unlock()
		b.Close()
		return nil
	}
	if string(req.SessionId) != a.sessionID {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	a.dispatchMu.Lock()
	b.mu.Lock()
	if b.active != a || string(req.SessionId) != a.sessionID {
		b.mu.Unlock()
		a.dispatchMu.Unlock()
		return nil
	}
	a.cancel = true
	canInterrupt := a.turnID != "" && !a.starting
	cancels := b.invalidateApprovalStateLocked(a)
	b.mu.Unlock()
	a.dispatchMu.Unlock()
	for _, cancelApproval := range cancels {
		cancelApproval()
	}
	if !canInterrupt {
		return nil
	}
	if err := b.interruptWithContext(ctx, a); err != nil {
		// A cancel notification has no response channel. Keep the turn active
		// until Codex reports its actual terminal outcome.
		return nil
	}
	return nil
}

func (b *codexBridge) cancelRequested(a *codexTurn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active == a && a.cancel && a.turnID != ""
}

func (b *codexBridge) interrupt(a *codexTurn) error {
	ctx, cancel := context.WithTimeout(b.h.ctx, 8*time.Second)
	defer cancel()
	return b.interruptWithContext(ctx, a)
}

func (b *codexBridge) interruptWithContext(ctx context.Context, a *codexTurn) error {
	b.mu.Lock()
	if b.active != a || a.turnID == "" || a.interruptSent {
		b.mu.Unlock()
		return nil
	}
	a.interruptSent = true
	threadID, turnID := a.threadID, a.turnID
	b.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := acp.SendRequest[json.RawMessage](b.upstream(), requestCtx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID})
	if err != nil {
		b.mu.Lock()
		if b.active == a {
			a.interruptSent = false
		}
		b.mu.Unlock()
	}
	return err
}

func (b *codexBridge) failActive(err error) {
	b.mu.Lock()
	a := b.active
	b.mu.Unlock()
	if a != nil {
		b.finish(a, codexOutcome{err: err})
	}
}

func (b *codexBridge) finish(a *codexTurn, outcome codexOutcome) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	b.mu.Lock()
	if b.active != a {
		b.mu.Unlock()
		return
	}
	b.active = nil
	cancels := b.invalidateApprovalStateLocked(a)
	a.startedOnce.Do(func() { close(a.started) })
	if a.overflow && outcome.err == nil {
		outcome.err = errors.New("codex event stream exceeded the bridge buffer; completion cannot be verified")
	}
	if a.deliveryErr != nil && outcome.err == nil {
		outcome.err = fmt.Errorf("codex output could not be delivered to the ACP client: %w", a.deliveryErr)
	}
	b.mu.Unlock()
	for _, cancelApproval := range cancels {
		cancelApproval()
	}
	a.doneOnce.Do(func() { a.done <- outcome; close(a.done) })
}

func (b *codexBridge) invalidateApprovalStateLocked(turn *codexTurn) []context.CancelFunc {
	seen := make(map[uint64]struct{})
	var cancels []context.CancelFunc
	for id, cancel := range b.pendingApprovals {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
		seen[id] = struct{}{}
		delete(b.pendingApprovals, id)
	}
	for token, grant := range b.grants {
		if turn == nil || grant.turn == turn {
			if _, ok := seen[grant.pendingID]; !ok && grant.cancel != nil {
				cancels = append(cancels, grant.cancel)
			}
			delete(b.grants, token)
		}
	}
	return cancels
}

func (b *codexBridge) handleUpstream(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "item/commandExecution/requestApproval":
		return b.commandApproval(ctx, method, params)
	case "item/fileChange/requestApproval":
		return b.fileApproval(ctx, method, params)
	case "item/permissions/requestApproval":
		return nil, acp.NewMethodNotFound(method)
	case "item/tool/requestUserInput":
		b.mu.Lock()
		questions := b.questions
		b.mu.Unlock()
		if !questions {
			return nil, acp.NewMethodNotFound(method)
		}
		return b.userInput(ctx, params)
	case "mcpServer/elicitation/request":
		return nil, acp.NewMethodNotFound(method)
	case "serverRequest/resolved":
		// Correlated at the native reader before SDK callback dispatch.
		return nil, nil
	case "turn/started", "turn/completed", "thread/tokenUsage/updated", "item/started", "item/completed",
		"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "turn/plan/updated", "error":
		select {
		case b.eventSignal <- struct{}{}:
		default:
		}
		b.acceptEvent(ctx, method, params)
		return nil, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

// A native-reader-correlated withdrawal retires this connection before an
// asynchronous SDK callback can register itself. Ordinary resolution after
// a response write is handled by the request ledger without retiring it.
func (b *codexBridge) retirePendingApprovals() {
	b.mu.Lock()
	a := b.active
	b.mu.Unlock()
	if a != nil {
		a.dispatchMu.Lock()
	}
	b.mu.Lock()
	b.retired = true
	b.approvalEpoch++
	cancels := b.invalidateApprovalStateLocked(nil)
	b.mu.Unlock()
	if a != nil {
		a.dispatchMu.Unlock()
	}
	for _, cancel := range cancels {
		cancel()
	}
	go b.Close()
}

// When the native process exits, ACP has already queued every complete frame it
// read. Notifications are dispatched on the SDK's ordered callback queue, so
// give that queue a short quiet window before treating the disconnect as a
// failed active turn. A queued turn/completed event remains authoritative.
func (b *codexBridge) failAfterUpstreamDrain() {
	quiet := time.NewTimer(100 * time.Millisecond)
	defer quiet.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-b.eventSignal:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(100 * time.Millisecond)
		case <-quiet.C:
			b.failActive(errors.New("codex App Server connection ended before the active turn completed"))
			return
		case <-deadline.C:
			b.failActive(errors.New("codex App Server notification drain exceeded its bound"))
			return
		}
	}
}

func (b *codexBridge) acceptEvent(ctx context.Context, method string, params json.RawMessage) {
	event, err := decodeCodexEvent(method, params)
	if err != nil {
		return
	}
	b.mu.Lock()
	a := b.active
	if a == nil || a.threadID != event.threadID {
		b.mu.Unlock()
		return
	}
	if a.turnID != "" && event.turnID != "" && event.turnID != a.turnID {
		b.mu.Unlock()
		return
	}
	if a.starting || a.draining {
		if len(a.pending) >= maxBuffered || a.pendingBytes+len(params) > maxBufferedSize {
			a.overflow = true
		} else {
			event.params = append(json.RawMessage(nil), params...)
			a.pending = append(a.pending, event)
			a.pendingBytes += len(params)
		}
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.dispatchEvent(ctx, a, event)
}

func decodeCodexEvent(method string, params json.RawMessage) (codexEvent, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		return codexEvent{}, err
	}
	var event codexEvent
	event.method, event.params = method, append(json.RawMessage(nil), params...)
	_ = json.Unmarshal(obj["threadId"], &event.threadID)
	_ = json.Unmarshal(obj["turnId"], &event.turnID)
	if event.turnID == "" && (method == "turn/started" || method == "turn/completed") {
		var turn codexTurnInfo
		if err := json.Unmarshal(obj["turn"], &turn); err != nil {
			return codexEvent{}, err
		}
		event.turnID = turn.ID
	}
	if event.threadID == "" || event.turnID == "" {
		return codexEvent{}, errors.New("codex event omitted thread or turn identity")
	}
	return event, nil
}

func (b *codexBridge) dispatchEvent(ctx context.Context, a *codexTurn, event codexEvent) {
	if event.turnID != a.turnID || event.threadID != a.threadID {
		return
	}
	switch event.method {
	case "thread/tokenUsage/updated":
		b.mu.Lock()
		model := b.selectedModel
		b.mu.Unlock()
		if update := codexUsageUpdate(event.params, model); update != nil {
			b.deliverUpdate(ctx, a, update)
		}
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(event.params, &p) == nil && p.Delta != "" {
			b.deliverUpdate(ctx, a, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": p.Delta}})
		}
	case "item/reasoning/summaryTextDelta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(event.params, &p) == nil && p.Delta != "" {
			b.deliverUpdate(ctx, a, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": p.Delta}})
		}
	case "item/started", "item/completed":
		b.dispatchItem(ctx, a, event.method, event.params)
	case "turn/completed":
		var p struct {
			Turn codexTurnInfo `json:"turn"`
		}
		if json.Unmarshal(event.params, &p) != nil {
			b.finish(a, codexOutcome{err: errors.New("malformed Codex turn/completed notification")})
			return
		}
		switch p.Turn.Status {
		case "completed":
			b.finish(a, codexOutcome{stopReason: acp.StopReasonEndTurn})
		case "interrupted":
			b.finish(a, codexOutcome{stopReason: acp.StopReasonCancelled})
		case "failed":
			message := "Codex turn failed"
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				message += ": " + p.Turn.Error.Message
			}
			b.finish(a, codexOutcome{err: errors.New(message)})
		default:
			b.finish(a, codexOutcome{err: fmt.Errorf("codex reported unknown terminal turn status %q", p.Turn.Status)})
		}
	case "error":
		var p struct {
			WillRetry bool `json:"willRetry"`
			Error     struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(event.params, &p) == nil && !p.WillRetry && p.Error.Message != "" {
			b.mu.Lock()
			if b.active == a && a.deliveryErr == nil {
				a.deliveryErr = errors.New(p.Error.Message)
			}
			b.mu.Unlock()
		}
	case "turn/started", "turn/plan/updated":
		// Turn identity and plans do not imply a user-visible result.
	}
}

func (b *codexBridge) deliverUpdate(ctx context.Context, a *codexTurn, update any) {
	if err := b.h.update(ctx, a.sessionID, update); err != nil {
		b.mu.Lock()
		if b.active == a && a.deliveryErr == nil {
			a.deliveryErr = err
		}
		b.mu.Unlock()
	}
}

func (b *codexBridge) dispatchItem(ctx context.Context, a *codexTurn, method string, params json.RawMessage) {
	var p struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Item) == 0 {
		return
	}
	itemID, _ := p.Item["id"].(string)
	itemType, _ := p.Item["type"].(string)
	if itemID == "" || itemType == "" || itemType == "userMessage" || itemType == "agentMessage" || itemType == "reasoning" || itemType == "plan" || itemType == "hookPrompt" {
		return
	}
	title, kind := codexToolTitle(itemType, p.Item)
	if method == "item/started" {
		b.deliverUpdate(ctx, a, map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": itemID, "title": title,
			"kind": kind, "status": "in_progress", "rawInput": p.Item,
		})
		return
	}
	status := codexItemStatus(p.Item)
	update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": itemID, "status": status, "rawOutput": p.Item}
	b.deliverUpdate(ctx, a, update)
}

func codexToolTitle(itemType string, item map[string]any) (string, string) {
	switch itemType {
	case "commandExecution":
		return "Run command", "execute"
	case "fileChange":
		return "Change files", "edit"
	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		return joinedTitle(server, tool, "MCP tool"), "other"
	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		return joinedTitle("Codex tool", tool, "Codex tool"), "other"
	case "webSearch":
		return "Search web", "fetch"
	case "collabAgentToolCall":
		return "Delegate to agent", "other"
	default:
		return "Codex " + itemType, "other"
	}
}

func joinedTitle(a, b, fallback string) string {
	if a == "" && b == "" {
		return fallback
	}
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "/" + b
}

func codexItemStatus(item map[string]any) string {
	if exitCode, ok := item["exitCode"].(float64); ok && exitCode != 0 {
		return "failed"
	}
	if status, ok := item["status"].(string); ok {
		switch strings.ToLower(status) {
		case "completed", "success", "succeeded", "applied", "done":
			return "completed"
		case "failed", "declined", "interrupted", "cancelled", "canceled", "error":
			return "failed"
		case "inprogress", "in_progress", "running", "pending":
			return "in_progress"
		default:
			return "failed"
		}
	}
	return "completed"
}

func (b *codexBridge) waitForActive(ctx context.Context, threadID, turnID string) *acp.RequestError {
	for {
		b.mu.Lock()
		a := b.active
		if a == nil || a.threadID != threadID {
			b.mu.Unlock()
			return invalid(errors.New("approval does not match the active Codex turn"))
		}
		if a.turnID != "" {
			match := a.turnID == turnID
			b.mu.Unlock()
			if !match {
				return invalid(errors.New("approval turnId does not match the active Codex turn"))
			}
			return nil
		}
		if !a.starting {
			b.mu.Unlock()
			return invalid(errors.New("codex turn identity is unavailable; refusing the approval"))
		}
		started := a.started
		b.mu.Unlock()
		select {
		case <-started:
		case <-ctx.Done():
			return acp.NewRequestCancelled(map[string]any{"error": ctx.Err().Error()})
		}
	}
}

func (b *codexBridge) commandApproval(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	var req struct {
		ThreadID           string            `json:"threadId"`
		TurnID             string            `json:"turnId"`
		ItemID             string            `json:"itemId"`
		ApprovalID         *string           `json:"approvalId"`
		Command            *string           `json:"command"`
		Cwd                *string           `json:"cwd"`
		Reason             *string           `json:"reason"`
		AvailableDecisions []json.RawMessage `json:"availableDecisions"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	if req.ThreadID == "" || req.TurnID == "" || req.ItemID == "" || req.AvailableDecisions == nil || len(req.AvailableDecisions) == 0 {
		return nil, invalid(errors.New("command approval omitted identity or availableDecisions; refusing it"))
	}
	if reqErr := b.waitForActive(ctx, req.ThreadID, req.TurnID); reqErr != nil {
		return nil, reqErr
	}
	choices, err := parseCommandChoices(req.AvailableDecisions)
	if err != nil {
		return nil, invalid(err)
	}
	toolID := approvalToolID(req.ItemID, req.ApprovalID)
	title := "Run command"
	if req.Command != nil && *req.Command != "" {
		title = "Run command"
	}
	_, choice, reqErr := b.askPermission(ctx, method, req.ThreadID, req.TurnID, req.ItemID, toolID, title, "execute", params, choices)
	if reqErr != nil {
		return nil, reqErr
	}
	if choice == nil {
		return nil, internal(errors.New("codex approval was cancelled without an offered decline or cancel choice"))
	}
	return map[string]any{"decision": json.RawMessage(choice.raw), codexGrantKey: choice.grantToken}, nil
}

func (b *codexBridge) fileApproval(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	var req struct {
		ThreadID  string  `json:"threadId"`
		TurnID    string  `json:"turnId"`
		ItemID    string  `json:"itemId"`
		Reason    *string `json:"reason"`
		GrantRoot *string `json:"grantRoot"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalid(err)
	}
	if req.ThreadID == "" || req.TurnID == "" || req.ItemID == "" {
		return nil, invalid(errors.New("file approval omitted thread, turn, or item identity"))
	}
	if reqErr := b.waitForActive(ctx, req.ThreadID, req.TurnID); reqErr != nil {
		return nil, reqErr
	}
	choices := fixedApprovalChoices("accept", "acceptForSession", "decline", "cancel")
	_, choice, err := b.askPermission(ctx, method, req.ThreadID, req.TurnID, req.ItemID, req.ItemID, "Change files", "edit", params, choices)
	if err != nil {
		return nil, err
	}
	if choice == nil {
		return nil, internal(errors.New("codex file approval was cancelled without an offered cancel choice"))
	}
	var decision any
	if err := json.Unmarshal(choice.raw, &decision); err != nil {
		return nil, invalid(err)
	}
	return map[string]any{"decision": decision, codexGrantKey: choice.grantToken}, nil
}

func (b *codexBridge) askPermission(ctx context.Context, method, threadID, turnID, itemID, toolID, title, kind string, rawInput json.RawMessage, choices []approvalChoice) (acp.RequestPermissionResponse, *approvalChoice, *acp.RequestError) {
	if len(choices) == 0 {
		return acp.RequestPermissionResponse{}, nil, invalid(errors.New("codex approval offered no supported choices"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	permissionCtx, cancelPermission := context.WithCancel(ctx)
	b.mu.Lock()
	sessionID := b.sessionID
	b.grantSequence++
	pendingID := b.grantSequence
	epoch := b.approvalEpoch
	b.pendingApprovals[pendingID] = cancelPermission
	b.mu.Unlock()
	transferred := false
	defer func() {
		if transferred {
			return
		}
		cancelPermission()
		b.mu.Lock()
		delete(b.pendingApprovals, pendingID)
		b.mu.Unlock()
	}()
	options := make([]acp.PermissionOption, 0, len(choices))
	byID := make(map[acp.PermissionOptionId]*approvalChoice, len(choices))
	for i := range choices {
		choices[i].option.OptionId = acp.PermissionOptionId(fmt.Sprintf("codex:%d:%s", i, encodeChoice(choices[i].raw)))
		options = append(options, choices[i].option)
		byID[choices[i].option.OptionId] = &choices[i]
	}
	var input any
	if err := json.Unmarshal(rawInput, &input); err != nil {
		input = string(rawInput)
	}
	approvalID := ""
	var raw map[string]json.RawMessage
	if json.Unmarshal(rawInput, &raw) == nil {
		_ = json.Unmarshal(raw["approvalId"], &approvalID)
	}
	permissionReq := acp.RequestPermissionRequest{
		Meta:      map[string]any{"codex": map[string]any{"threadId": threadID, "turnId": turnID, "itemId": itemID, "approvalId": approvalID, "method": method}},
		SessionId: acp.SessionId(sessionID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(toolID), Title: &title,
			Kind: toolKind(kind), Status: toolStatus(), RawInput: input,
		},
		Options: options,
	}
	response, err := acp.SendRequest[acp.RequestPermissionResponse](b.h.conn, permissionCtx, acp.ClientMethodSessionRequestPermission, permissionReq)
	if err != nil {
		return acp.RequestPermissionResponse{}, nil, internal(fmt.Errorf("codex approval was not accepted by the ACP client: %w", err))
	}
	if err := response.Validate(); err != nil {
		return acp.RequestPermissionResponse{}, nil, invalid(err)
	}
	if response.Outcome.Cancelled != nil {
		for _, preferred := range []string{"cancel", "decline"} {
			for i := range choices {
				if choices[i].name == preferred {
					guarded, reqErr := b.guardApprovalChoice(ctx, permissionCtx, cancelPermission, pendingID, epoch, threadID, turnID, &choices[i])
					if reqErr != nil {
						return acp.RequestPermissionResponse{}, nil, reqErr
					}
					transferred = true
					return response, guarded, nil
				}
			}
		}
		return acp.RequestPermissionResponse{}, nil, acp.NewRequestCancelled(map[string]any{"error": "Codex approval was cancelled without an offered decline or cancel choice"})
	}
	if response.Outcome.Selected == nil {
		return acp.RequestPermissionResponse{}, nil, invalid(errors.New("ACP permission outcome has no selected choice"))
	}
	choice := byID[response.Outcome.Selected.OptionId]
	if choice == nil {
		return acp.RequestPermissionResponse{}, nil, invalid(errors.New("ACP selected an option not offered by Codex"))
	}
	guarded, reqErr := b.guardApprovalChoice(ctx, permissionCtx, cancelPermission, pendingID, epoch, threadID, turnID, choice)
	if reqErr != nil {
		return acp.RequestPermissionResponse{}, nil, reqErr
	}
	transferred = true
	return response, guarded, nil
}

func (b *codexBridge) guardApprovalChoice(ctx, permissionCtx context.Context, cancelPermission context.CancelFunc, pendingID, epoch uint64, threadID, turnID string, choice *approvalChoice) (*approvalChoice, *acp.RequestError) {
	b.mu.Lock()
	a := b.active
	b.mu.Unlock()
	if a == nil {
		return nil, acp.NewRequestCancelled(map[string]any{"error": "Codex turn completed before its approval could be delivered"})
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != a || b.closed || b.retired || a.cancel || a.threadID != threadID || a.turnID != turnID || b.approvalEpoch != epoch || ctx.Err() != nil || permissionCtx.Err() != nil {
		return nil, acp.NewRequestCancelled(map[string]any{"error": "Codex approval is stale or its turn is no longer active"})
	}
	b.grantSequence++
	token := fmt.Sprintf("%d", b.grantSequence)
	guarded := *choice
	guarded.grantToken = token
	b.grants[token] = codexGrant{turn: a, ctx: ctx, epoch: epoch, pendingID: pendingID, cancel: cancelPermission}
	return &guarded, nil
}

func approvalToolID(itemID string, approvalID *string) string {
	if approvalID != nil && *approvalID != "" {
		return itemID + ":" + *approvalID
	}
	return itemID
}

func parseCommandChoices(raw []json.RawMessage) ([]approvalChoice, error) {
	choices := make([]approvalChoice, 0, len(raw))
	seen := make(map[string]struct{})
	for _, decision := range raw {
		var value string
		if json.Unmarshal(decision, &value) == nil {
			var name string
			var kind acp.PermissionOptionKind
			switch value {
			case "accept":
				name, kind = "Allow once", acp.PermissionOptionKindAllowOnce
			case "acceptForSession":
				name, kind = "Allow for this session", acp.PermissionOptionKindAllowAlways
			case "decline":
				name, kind = "Decline", acp.PermissionOptionKindRejectOnce
			case "cancel":
				name, kind = "Decline and interrupt the turn", acp.PermissionOptionKindRejectOnce
			default:
				return nil, fmt.Errorf("codex offered unsupported command approval decision %q", value)
			}
			if _, ok := seen[string(decision)]; !ok {
				choices = append(choices, codexPermissionChoice(value, name, kind, decision))
				seen[string(decision)] = struct{}{}
			}
			continue
		}
		choice, err := parseCommandObjectDecision(decision)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[string(decision)]; !ok {
			choices = append(choices, choice)
			seen[string(decision)] = struct{}{}
		}
	}
	if len(choices) == 0 {
		return nil, errors.New("codex offered no supported command approval decisions")
	}
	return choices, nil
}

func parseCommandObjectDecision(raw json.RawMessage) (approvalChoice, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return approvalChoice{}, errors.New("codex offered an unknown command approval decision shape")
	}
	if amendment, ok := object["acceptWithExecpolicyAmendment"]; ok {
		var payload struct {
			Amendment []string `json:"execpolicy_amendment"`
		}
		if json.Unmarshal(amendment, &payload) != nil || len(payload.Amendment) == 0 {
			return approvalChoice{}, errors.New("codex command policy amendment has an unknown shape")
		}
		return codexPermissionChoice("acceptWithExecpolicyAmendment", "Allow and add a command rule", acp.PermissionOptionKindAllowAlways, raw), nil
	}
	if amendment, ok := object["applyNetworkPolicyAmendment"]; ok {
		var payload struct {
			Amendment struct {
				Action string `json:"action"`
				Host   string `json:"host"`
			} `json:"network_policy_amendment"`
		}
		if json.Unmarshal(amendment, &payload) != nil || payload.Amendment.Host == "" || (payload.Amendment.Action != "allow" && payload.Amendment.Action != "deny") {
			return approvalChoice{}, errors.New("codex network policy amendment has an unknown shape")
		}
		kind, action := acp.PermissionOptionKindAllowAlways, "Allow"
		if payload.Amendment.Action == "deny" {
			kind, action = acp.PermissionOptionKindRejectAlways, "Deny"
		}
		return codexPermissionChoice("applyNetworkPolicyAmendment", action+" host "+payload.Amendment.Host, kind, raw), nil
	}
	return approvalChoice{}, errors.New("codex offered an unknown command approval decision object")
}

func fixedApprovalChoices(values ...string) []approvalChoice {
	choices := make([]approvalChoice, 0, len(values))
	for _, value := range values {
		var kind acp.PermissionOptionKind
		var name string
		switch value {
		case "accept":
			name, kind = "Allow once", acp.PermissionOptionKindAllowOnce
		case "acceptForSession":
			name, kind = "Allow for this session", acp.PermissionOptionKindAllowAlways
		case "decline":
			name, kind = "Decline", acp.PermissionOptionKindRejectOnce
		case "cancel":
			name, kind = "Decline and interrupt the turn", acp.PermissionOptionKindRejectOnce
		}
		raw, _ := json.Marshal(value)
		choices = append(choices, codexPermissionChoice(value, name, kind, raw))
	}
	return choices
}

func codexPermissionChoice(id, name string, kind acp.PermissionOptionKind, raw json.RawMessage) approvalChoice {
	if raw == nil {
		raw, _ = json.Marshal(id)
	}
	return approvalChoice{
		option: acp.PermissionOption{Name: name, Kind: kind, Meta: map[string]any{"codexDecision": json.RawMessage(append([]byte(nil), raw...))}},
		raw:    append(json.RawMessage(nil), raw...), name: id,
	}
}

func encodeChoice(raw json.RawMessage) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func dedupeModels(models []codexModel) []codexModel {
	seen := make(map[string]struct{}, len(models))
	out := make([]codexModel, 0, len(models))
	for _, model := range models {
		value := codexModelValue(model)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, model)
	}
	return out
}

func toolKind(kind string) *acp.ToolKind {
	value := acp.ToolKind(kind)
	return &value
}

func toolStatus() *acp.ToolCallStatus {
	value := acp.ToolCallStatusInProgress
	return &value
}

type nativeWrite struct {
	frame []byte
	done  chan error
}

// codexNativeWriter serializes provider writes through a bounded queue. ACP's
// SDK has no write deadline, so a blocked native pipe must not hold an
// initialize, interrupt, or approval callback forever.
type codexNativeWriter struct {
	dst    io.WriteCloser
	proc   *process
	bridge *codexBridge
	queue  chan nativeWrite
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func newCodexNativeWriter(dst io.WriteCloser, proc *process, bridge *codexBridge) *codexNativeWriter {
	w := &codexNativeWriter{dst: dst, proc: proc, bridge: bridge, queue: make(chan nativeWrite, 32), done: make(chan struct{})}
	go w.run()
	return w
}

func (w *codexNativeWriter) Write(frame []byte) (int, error) {
	if len(frame) > maxFrame+1 {
		return 0, fmt.Errorf("codex App Server write exceeded %d bytes", maxFrame)
	}
	originalSize := len(frame)
	cleanStart, turn, err := w.bridge.lockStartForWrite(frame)
	if err != nil {
		return 0, err
	}
	if turn != nil {
		defer turn.dispatchMu.Unlock()
		frame = cleanStart
	}
	clean, token, guarded := w.bridge.guardNativeFrame(frame)
	if guarded && token == "" {
		return len(frame), nil
	}
	request := nativeWrite{frame: append([]byte(nil), clean...), done: make(chan error, 1)}
	if token != "" {
		grant, ok := w.bridge.lockGrantForWrite(token)
		if !ok {
			return len(frame), nil
		}
		defer grant.turn.dispatchMu.Unlock()
		if !w.write(request) {
			w.bridge.releaseGrant(token, grant)
			return 0, w.error()
		}
		w.bridge.releaseGrant(token, grant)
		return len(frame), nil
	}
	if !w.write(request) {
		return 0, w.error()
	}
	return originalSize, nil
}

// The SDK owns its write lock before invoking this method. Never acquire that
// SDK lock while holding dispatchMu: it protects only the actual native write,
// not the request's response wait. The marker binds delayed writes to this
// particular prompt and is removed before any bytes reach App Server.
func (b *codexBridge) lockStartForWrite(frame []byte) ([]byte, *codexTurn, error) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(frame), &message); err != nil {
		return frame, nil, err
	}
	var method string
	_ = json.Unmarshal(message["method"], &method)
	if method != "turn/start" {
		return frame, nil, nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(message["params"], &params); err != nil {
		return nil, nil, err
	}
	var token, threadID string
	_ = json.Unmarshal(params[codexStartKey], &token)
	_ = json.Unmarshal(params["threadId"], &threadID)
	b.mu.Lock()
	a := b.active
	b.mu.Unlock()
	if a == nil {
		return nil, nil, errors.New("codex prompt is no longer active")
	}
	a.dispatchMu.Lock()
	b.mu.Lock()
	valid := b.active == a && !b.closed && !b.retired && !a.cancel && a.starting && token != "" && token == a.startToken && threadID == a.threadID && a.callCtx != nil && a.callCtx.Err() == nil
	if valid {
		a.startAttempted = true
	}
	b.mu.Unlock()
	if !valid {
		a.dispatchMu.Unlock()
		return nil, nil, errors.New("codex prompt was cancelled before native dispatch")
	}
	delete(params, codexStartKey)
	message["params"], _ = json.Marshal(params)
	clean, err := json.Marshal(message)
	if err != nil {
		a.dispatchMu.Unlock()
		return nil, nil, err
	}
	if bytes.HasSuffix(frame, []byte("\n")) {
		clean = append(clean, '\n')
	}
	return clean, a, nil
}

func (w *codexNativeWriter) write(request nativeWrite) bool {
	timer := time.NewTimer(nativeWriteTimeout)
	defer timer.Stop()
	select {
	case w.queue <- request:
	case <-w.done:
		return false
	case <-w.proc.ctx.Done():
		w.fail(errors.New("codex App Server exited before its request was written"))
		return false
	case <-timer.C:
		w.fail(errors.New("codex App Server write queue remained blocked"))
		return false
	}
	select {
	case err := <-request.done:
		return err == nil
	case <-w.done:
		return false
	case <-w.proc.ctx.Done():
		w.fail(errors.New("codex App Server exited during a request write"))
		return false
	case <-timer.C:
		w.fail(errors.New("codex App Server request write timed out"))
		return false
	}
}

func (w *codexNativeWriter) run() {
	for {
		select {
		case <-w.done:
			return
		case <-w.proc.ctx.Done():
			w.fail(errors.New("codex App Server process exited"))
			return
		case request := <-w.queue:
			n, err := w.bridge.writeNativeResponse(w.dst, request.frame)
			if err == nil && n != len(request.frame) {
				err = io.ErrShortWrite
			}
			request.done <- err
			if err != nil {
				w.fail(fmt.Errorf("write to Codex App Server: %w", err))
				return
			}
		}
	}
}

func (w *codexNativeWriter) fail(err error) {
	w.once.Do(func() {
		w.mu.Lock()
		w.err = err
		w.mu.Unlock()
		close(w.done)
		go w.proc.close()
	})
}

func (w *codexNativeWriter) error() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	return errors.New("codex App Server writer is closed")
}

func (b *codexBridge) guardNativeFrame(frame []byte) ([]byte, string, bool) {
	var message map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(frame), &message) != nil {
		return frame, "", false
	}
	resultRaw, ok := message["result"]
	if !ok {
		return frame, "", false
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(resultRaw, &result) != nil {
		return frame, "", false
	}
	tokenRaw, guarded := result[codexGrantKey]
	if !guarded {
		return frame, "", false
	}
	var token string
	if json.Unmarshal(tokenRaw, &token) != nil || token == "" {
		return frame, "", true
	}
	delete(result, codexGrantKey)
	cleanResult, err := json.Marshal(result)
	if err != nil {
		return frame, "", true
	}
	message["result"] = cleanResult
	cleanFrame, err := json.Marshal(message)
	if err != nil {
		return frame, "", true
	}
	if bytes.HasSuffix(frame, []byte("\n")) {
		cleanFrame = append(cleanFrame, '\n')
	}
	return cleanFrame, token, true
}

func (b *codexBridge) lockGrantForWrite(token string) (codexGrant, bool) {
	b.mu.Lock()
	grant, ok := b.grants[token]
	b.mu.Unlock()
	if !ok || grant.turn == nil {
		return codexGrant{}, false
	}
	grant.turn.dispatchMu.Lock()
	b.mu.Lock()
	current, exists := b.grants[token]
	valid := exists && current.turn == grant.turn && b.active == grant.turn && !grant.turn.cancel && !b.closed && !b.retired && b.approvalEpoch == grant.epoch && grant.ctx != nil && grant.ctx.Err() == nil
	if !exists || current.turn != grant.turn {
		b.mu.Unlock()
		grant.turn.dispatchMu.Unlock()
		return codexGrant{}, false
	}
	delete(b.grants, token)
	if !valid {
		b.mu.Unlock()
		grant.turn.dispatchMu.Unlock()
		b.releaseGrant(token, current)
		return codexGrant{}, false
	}
	delete(b.pendingApprovals, current.pendingID)
	b.mu.Unlock()
	return current, true
}

func (b *codexBridge) releaseGrant(token string, grant codexGrant) {
	b.mu.Lock()
	delete(b.grants, token)
	delete(b.pendingApprovals, grant.pendingID)
	b.mu.Unlock()
	if grant.cancel != nil {
		grant.cancel()
	}
}
