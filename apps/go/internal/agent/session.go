package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// AuthRequiredCode is the JSON-RPC code ACP reserves for "authentication
// required". The ADE never offers a login flow in this slice; it reports the
// adapter's own authentication methods instead.
const AuthRequiredCode = -32000

// stderrLimit bounds the retained adapter diagnostics reported on failure.
const stderrLimit = 8 << 10

// exitWait bounds how long ending a session waits for its child process to be
// reaped. Stdin close is followed by a kill after two seconds, so this leaves
// room for the documented slow adapter exit and for reaping the killed process.
const exitWait = 5 * time.Second

// Update is one session/update payload. Kind is the wire discriminator, Typed
// the SDK union and Raw the original object.
//
// Both are retained deliberately. acp-go-sdk v0.13.5 resolves the union by
// shape when the discriminator is unrecognised, so an update kind the SDK does
// not know decodes, without error, into whichever variant happens to match:
// {"sessionUpdate":"invented_kind","x":1} arrives as a session_info_update.
// Dispatching on Typed alone would therefore let a future kind masquerade as a
// known one. Raw and Kind are the authority; Typed is a convenience for the
// kinds the wire discriminator already confirmed.
type Update struct {
	Kind  string
	Typed acp.SessionUpdate
	Raw   json.RawMessage
}

// Handler receives everything the agent pushes at the client. Implementations
// must not block session updates for long; RequestPermission may block until
// the user answers or the context ends.
type Handler interface {
	SessionUpdate(ctx context.Context, sessionID string, u Update) error
	RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

// QuestionHandler opts a connection into the pinned Claude form dialect. Core
// ACP framing alone does not establish question execution or delivery semantics.
type QuestionHandler interface {
	ClaudeQuestions() bool
	CreateElicitation(context.Context, json.RawMessage) (map[string]any, error)
}

const ClaudeQuestionVersion = "@agentclientprotocol/claude-agent-acp 0.80.0"
const CapNativeQuestions = "native-questions"

// Options describe one agent process launch.
type Options struct {
	AgentID string
	Command string
	Args    []string
	Cwd     string
	Handler Handler
	Log     func(msg string, args ...any)
}

// Launcher starts an agent connection. The server holds one so tests can
// substitute an in-process agent without an executable.
type Launcher func(ctx context.Context, o Options) (*Session, error)

// Info records what the agent reported during initialize.
type Info struct {
	Version      string
	Capabilities []string
	AuthMethods  []string
	Caps         acp.AgentCapabilities
}

// Has reports whether the agent advertised the named capability.
func (i Info) Has(capability string) bool {
	for _, c := range i.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Session is one live agent connection: its child process, the ACP connection
// and at most one upstream session. It is safe for concurrent use.
type Session struct {
	conn   *acp.Connection
	stderr *ring
	stop   func()
	// pid is the child process identifier, or zero for an in-process
	// connection that owns no process.
	pid          int
	processID    func() int
	disconnected <-chan struct{}
	// exited closes when the child process has been reaped. It is nil for an
	// in-process connection, which owns no process.
	exited      <-chan struct{}
	log         func(string, ...any)
	runtimePath string

	handler Handler

	mu        sync.Mutex
	info      Info
	sessionID string
	closed    bool
}

// Start launches the configured executable with stdio pipes and wraps it in an
// ACP client connection. The process is killed when ctx ends or Close is called.
func Start(ctx context.Context, o Options) (*Session, error) {
	if strings.TrimSpace(o.Command) == "" {
		return nil, errors.New("no agent executable is configured")
	}
	if strings.HasPrefix(o.Command, "builtin:") {
		if o.Command != "builtin:"+o.AgentID || (o.AgentID != "claude" && o.AgentID != "codex") || len(o.Args) != 0 {
			return nil, errors.New("invalid built-in adapter selection or arguments")
		}
		environment, runtimePath, err := runtimeEnvironment(o.AgentID)
		if err != nil {
			return nil, err
		}
		if ctx == nil {
			ctx = context.Background()
		}
		buffer := newRing(stderrLimit)
		endpoint, err := acpbridge.Open(ctx, o.AgentID, runtimePath, o.Cwd, environment, buffer)
		if err != nil {
			return nil, err
		}
		logf := o.Log
		if logf == nil {
			logf = func(string, ...any) {}
		}
		s := connect(o.Handler, endpoint.Input, endpoint.Output, buffer, endpoint.Stop, logf)
		s.runtimePath, s.exited = runtimePath, endpoint.Exited
		s.processID = endpoint.ProcessID
		s.disconnected = endpoint.Disconnected
		logf("built-in ACP bridge opened", "agent", o.AgentID, "runtime", runtimePath, "version", acpbridge.Version)
		return s, nil
	}
	path, err := exec.LookPath(o.Command)
	if err != nil {
		return nil, fmt.Errorf("%w: %q (%v)", ErrExecutableMissing, o.Command, err)
	}
	environment, runtimePath, err := runtimeEnvironment(o.AgentID)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, o.Args...)
	cmd.Dir = o.Cwd
	cmd.Env = environment
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	buffer := newRing(stderrLimit)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("agent executable %q did not start: %w", o.Command, err)
	}
	go func() { _, _ = io.Copy(buffer, stderr) }()
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	logf("agent process started", "command", path, "pid", cmd.Process.Pid, "cwd", o.Cwd)
	if runtimePath != "" {
		logf("agent runtime selected", "agent", o.AgentID, "runtime", runtimePath)
	}
	var once sync.Once
	stopped := make(chan struct{})
	stop := func() {
		once.Do(func() {
			// Closing stdin lets a well-behaved adapter exit; the kill below
			// bounds one that does not.
			_ = stdin.Close()
			go func() {
				select {
				case <-stopped:
				case <-time.After(2 * time.Second):
					_ = cmd.Process.Kill()
				}
			}()
			go func() {
				err := cmd.Wait()
				close(stopped)
				logf("agent process exited", "command", path, "pid", cmd.Process.Pid, "error", err)
			}()
		})
	}
	session := connect(o.Handler, stdin, stdout, buffer, stop, logf)
	session.pid, session.exited = cmd.Process.Pid, stopped
	session.runtimePath = runtimePath
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				session.Kill()
			case <-session.conn.Done():
			}
		}()
	}
	return session, nil
}

// RuntimePath identifies the explicitly selected official CLI, when required.
func (s *Session) RuntimePath() string { return s.runtimePath }

// Connect wraps an already-established stdio pair. Tests use it to drive an
// in-process agent; Start uses it for a real child process.
func Connect(handler Handler, in io.Writer, out io.Reader, stop func(), log func(string, ...any)) *Session {
	if log == nil {
		log = func(string, ...any) {}
	}
	return connect(handler, in, out, newRing(stderrLimit), stop, log)
}

func connect(handler Handler, in io.Writer, out io.Reader, buffer *ring, stop func(), log func(string, ...any)) *Session {
	s := &Session{stderr: buffer, stop: stop, log: log, handler: handler}
	s.conn = acp.NewConnection(s.handle, in, out)
	return s
}

// handle dispatches inbound agent calls. Filesystem and terminal methods are
// refused explicitly, because the client advertised neither capability and an
// agent that calls them anyway must see the refusal rather than a silent
// success. The agent's own direct access is unaffected.
func (s *Session) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case "elicitation/create":
		h, ok := s.handler.(QuestionHandler)
		if !ok || !h.ClaudeQuestions() || !s.Info().Has(CapNativeQuestions) {
			return nil, acp.NewMethodNotFound(method)
		}
		response, err := h.CreateElicitation(ctx, params)
		if err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		return response, nil
	case acp.ClientMethodSessionUpdate:
		var notification struct {
			SessionId string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(params, &notification); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if s.handler == nil {
			return nil, nil
		}
		if err := s.handler.SessionUpdate(ctx, notification.SessionId, DecodeUpdate(notification.Update)); err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		return nil, nil
	case acp.ClientMethodSessionRequestPermission:
		var request acp.RequestPermissionRequest
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
		}
		if s.handler == nil {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
		}
		response, err := s.handler.RequestPermission(ctx, request)
		if err != nil {
			return nil, acp.NewInternalError(map[string]any{"error": err.Error()})
		}
		return response, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

// DecodeUpdate splits one session/update object into its wire kind, the SDK
// union where the SDK models it, and the untouched raw object.
func DecodeUpdate(raw json.RawMessage) Update {
	u := Update{Raw: raw}
	var discriminator struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &discriminator)
	u.Kind = discriminator.SessionUpdate
	_ = json.Unmarshal(raw, &u.Typed)
	return u
}

// Diagnostics returns the retained adapter stderr, trimmed to the ring bound.
func (s *Session) Diagnostics() string { return strings.TrimSpace(s.stderr.String()) }

// Done closes when the peer disconnects.
func (s *Session) Done() <-chan struct{} {
	if s.disconnected != nil {
		return s.disconnected
	}
	return s.conn.Done()
}

// Info returns what initialize reported.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// SessionID returns the current upstream session identity, if any.
func (s *Session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Initialize negotiates protocol version 1 and records the agent's reported
// capabilities. The client advertises no filesystem or terminal support, so the
// agent uses its own direct access.
func (s *Session) Initialize(ctx context.Context) (Info, error) {
	capabilities := map[string]any{}
	h, questions := s.handler.(QuestionHandler)
	questions = questions && h.ClaudeQuestions()
	if questions {
		capabilities["elicitation"] = map[string]any{"form": map[string]any{}}
	}
	// The pinned JS adapter uses elicitation/create. The Go SDK's generated
	// unstable types describe an older dialect, so keep this negotiation explicit.
	resp, err := acp.SendRequest[acp.InitializeResponse](s.conn, ctx, acp.AgentMethodInitialize, map[string]any{
		"protocolVersion":    acp.ProtocolVersionNumber,
		"clientCapabilities": capabilities,
		"clientInfo":         &acp.Implementation{Name: "tui-go", Version: fmt.Sprintf("protocol-%d", protocol.Version)},
	})
	if err != nil {
		return Info{}, err
	}
	if s.processID != nil {
		s.mu.Lock()
		s.pid = s.processID()
		pid := s.pid
		s.mu.Unlock()
		s.log("agent process started", "command", s.runtimePath, "pid", pid, "bridge", "builtin")
	}
	if resp.ProtocolVersion != acp.ProtocolVersionNumber {
		return Info{}, fmt.Errorf("agent negotiated protocol version %d; this client implements version %d", resp.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	info := Info{Caps: resp.AgentCapabilities}
	if resp.AgentInfo != nil {
		info.Version = strings.TrimSpace(resp.AgentInfo.Name + " " + resp.AgentInfo.Version)
	}
	if questions && (info.Version == ClaudeQuestionVersion || (info.Version == acpbridge.ClaudeIdentity && resp.AgentCapabilities.Meta["questionDialect"] == acpbridge.QuestionDialect)) {
		info.Capabilities = append(info.Capabilities, CapNativeQuestions)
		if info.Version == acpbridge.ClaudeIdentity {
			info.Capabilities = append(info.Capabilities, acpbridge.QuestionDialect)
		} else {
			info.Capabilities = append(info.Capabilities, "claude-questions-0.80.0")
		}
	}
	if questions && info.Version == acpbridge.CodexIdentity && resp.AgentCapabilities.Meta["questionDialect"] == acpbridge.CodexQuestionDialect {
		info.Capabilities = append(info.Capabilities, CapNativeQuestions, acpbridge.CodexQuestionDialect)
	}
	if resp.AgentCapabilities.LoadSession {
		info.Capabilities = append(info.Capabilities, CapLoadSession)
	}
	if resp.AgentCapabilities.PromptCapabilities.Image {
		info.Capabilities = append(info.Capabilities, CapImagePrompt)
	}
	if resp.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		info.Capabilities = append(info.Capabilities, CapEmbeddedPrompt)
	}
	if resp.AgentCapabilities.PromptCapabilities.Audio {
		info.Capabilities = append(info.Capabilities, CapAudioPrompt)
	}
	if resp.AgentCapabilities.SessionCapabilities.Close != nil {
		info.Capabilities = append(info.Capabilities, CapSessionClose)
	}
	for _, method := range resp.AuthMethods {
		switch {
		case method.Agent != nil:
			info.AuthMethods = append(info.AuthMethods, method.Agent.Name)
		case method.EnvVar != nil:
			info.AuthMethods = append(info.AuthMethods, "environment variable")
		case method.Terminal != nil:
			info.AuthMethods = append(info.AuthMethods, "interactive terminal")
		}
	}
	s.mu.Lock()
	s.info = info
	s.mu.Unlock()
	return info, nil
}

// NewSession opens an upstream session in cwd and returns its config options.
func (s *Session) NewSession(ctx context.Context, cwd string) (string, []protocol.ConfigOption, error) {
	resp, err := acp.SendRequest[acp.NewSessionResponse](s.conn, ctx, acp.AgentMethodSessionNew, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	s.sessionID = string(resp.SessionId)
	s.mu.Unlock()
	options, _ := MapOptions(resp.ConfigOptions)
	return string(resp.SessionId), options, nil
}

// LoadSession restores an earlier upstream session. Only call it when the agent
// advertised load-session.
func (s *Session) LoadSession(ctx context.Context, id, cwd string) ([]protocol.ConfigOption, error) {
	resp, err := acp.SendRequest[acp.LoadSessionResponse](s.conn, ctx, acp.AgentMethodSessionLoad, acp.LoadSessionRequest{SessionId: acp.SessionId(id), Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
	options, _ := MapOptions(resp.ConfigOptions)
	return options, nil
}

// SetConfigOption applies one captured setting and returns the agent's reported
// full option state. A rejected value returns the agent's error unchanged.
func (s *Session) SetConfigOption(ctx context.Context, a Assignment) ([]protocol.ConfigOption, error) {
	id := acp.SessionId(s.SessionID())
	request := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: id, ConfigId: acp.SessionConfigId(a.ConfigID), Value: acp.SessionConfigValueId(a.Value)}}
	if a.Boolean {
		request = acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{SessionId: id, ConfigId: acp.SessionConfigId(a.ConfigID), Type: "boolean", Value: a.Value == "true"}}
	}
	resp, err := acp.SendRequest[acp.SetSessionConfigOptionResponse](s.conn, ctx, acp.AgentMethodSessionSetConfigOption, request)
	if err != nil {
		return nil, err
	}
	options, _ := MapOptions(resp.ConfigOptions)
	return options, nil
}

// Prompt runs one turn. It blocks until the agent reports a stop reason.
func (s *Session) Prompt(ctx context.Context, blocks []acp.ContentBlock) (acp.PromptResponse, error) {
	return acp.SendRequest[acp.PromptResponse](s.conn, ctx, acp.AgentMethodSessionPrompt, acp.PromptRequest{SessionId: acp.SessionId(s.SessionID()), Prompt: blocks})
}

// Cancel sends the session/cancel notification. The agent must still answer the
// in-flight prompt with the cancelled stop reason.
func (s *Session) Cancel(ctx context.Context) error {
	return s.conn.SendNotification(ctx, acp.AgentMethodSessionCancel, acp.CancelNotification{SessionId: acp.SessionId(s.SessionID())})
}

// Close ends the upstream session when the agent advertised session/close, then
// terminates the process. It is idempotent.
func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.awaitExit(ctx)
		return
	}
	s.closed = true
	id, closable := s.sessionID, s.info.Has(CapSessionClose)
	s.mu.Unlock()
	if id != "" && closable {
		closeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if _, err := acp.SendRequest[acp.CloseSessionResponse](s.conn, closeCtx, acp.AgentMethodSessionClose, acp.CloseSessionRequest{SessionId: acp.SessionId(id)}); err != nil {
			s.log("agent session close failed", "session", id, "error", err)
		}
		cancel()
	}
	if s.stop != nil {
		s.stop()
	}
	s.awaitExit(ctx)
}

// Kill terminates the process without attempting a protocol close.
func (s *Session) Kill() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.stop != nil {
		s.stop()
	}
}

// awaitExit blocks until the child process has been reaped, so a caller that
// ends a session — including server shutdown — can report that no adapter
// outlived it. The wait is bounded: stdin close is followed by a kill after two
// seconds, and exitWait leaves room for that plus reaping. A caller that
// supplies a shorter deadline gets its own bound.
func (s *Session) awaitExit(ctx context.Context) {
	if s.exited == nil {
		return
	}
	timer := time.NewTimer(exitWait)
	defer timer.Stop()
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	case <-s.exited:
	case <-done:
		s.log("agent process exit not confirmed before the caller's deadline")
	case <-timer.C:
		s.log("agent process did not exit within the bounded wait")
	}
}

// AuthRequired reports whether an agent error means the adapter's own CLI login
// is missing rather than a transport or protocol failure.
func AuthRequired(err error) bool {
	var re *acp.RequestError
	return errors.As(err, &re) && re.Code == AuthRequiredCode
}

// Message renders an agent error for Thread.Error and Agent.Detail without
// leaking control characters into the terminal.
func Message(err error) string {
	if err == nil {
		return ""
	}
	var re *acp.RequestError
	if errors.As(err, &re) && strings.TrimSpace(re.Message) != "" {
		if data, ok := re.Data.(map[string]any); ok {
			if detail, ok := data["error"].(string); ok && strings.TrimSpace(detail) != "" {
				return Sanitize(re.Message + ": " + detail)
			}
		}
		return Sanitize(re.Message)
	}
	return Sanitize(err.Error())
}
