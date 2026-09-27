// Package acpbridge translates installed official runtimes to ACP. It has no
// application storage or frontend dependencies. Provider controls never cross
// the ACP boundary except through explicitly versioned extensions.
package acpbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const Version = "0.1.0"
const ClaudeIdentity = "tui-go-claude " + Version
const QuestionDialect = "tui-go.claude-questions.v1"

// QuestionDeliveryDialect pins the built-in bridges' answer-delivery receipt.
// A receipt reports only what the bridge observed at its provider boundary; the
// application still decides whether that evidence settles a request.
const QuestionDeliveryDialect = "tui-go.question-delivery.v1"

type backend interface {
	Handle(context.Context, string, json.RawMessage) (any, *acp.RequestError)
	Close()
}

type host struct {
	conn       *acp.Connection
	path, cwd  string
	env        []string
	stderr     io.Writer
	ctx        context.Context
	mu         sync.Mutex
	process    *process
	nativeDone chan struct{}
	nativeOnce sync.Once
	// readOnly locks the session to the provider's read-only mode (see
	// OpenOptions).
	readOnly bool
}

// OpenOptions select how a bridge runs. ReadOnly locks its one session to
// the provider's most restrictive documented mode for work that must not
// change the checkout (agent-planned rebases, ADR 0027): Claude's plan
// permission mode with classifier review of planning commands off, and
// Codex's readOnly sandbox with approval policy never. The session then
// offers only that value as its Permissions option and refuses others.
type OpenOptions struct {
	ReadOnly bool
}

// Endpoint carries serialized ACP over pipes inside the application binary.
// Stop closes both ACP directions, cancels callbacks and reaps the owned CLI.
type Endpoint struct {
	Input        io.WriteCloser
	Output       io.ReadCloser
	Stop         func()
	Exited       <-chan struct{}
	Disconnected <-chan struct{}
	ProcessID    func() int
}

func Open(ctx context.Context, provider, path, cwd string, env []string, stderr io.Writer) (*Endpoint, error) {
	return OpenWith(ctx, provider, path, cwd, env, stderr, OpenOptions{})
}

// OpenWith is Open with options.
func OpenWith(ctx context.Context, provider, path, cwd string, env []string, stderr io.Writer, o OpenOptions) (*Endpoint, error) {
	if provider != "claude" && provider != "codex" {
		return nil, errors.New("unknown built-in provider")
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &host{path: path, cwd: cwd, env: env, stderr: stderr, ctx: ctx, nativeDone: make(chan struct{}), readOnly: o.ReadOnly}
	var b backend
	if provider == "claude" {
		b = newClaude(h)
	} else {
		b = newCodex(h)
	}
	appRead, bridgeWrite := io.Pipe()
	bridgeRead, appWrite := io.Pipe()
	h.conn = acp.NewConnection(b.Handle, bridgeWrite, bridgeRead)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = appWrite.Close()
			_ = bridgeWrite.Close()
			_ = bridgeRead.Close()
			_ = appRead.Close()
			b.Close()
			h.disconnected()
			h.mu.Lock()
			p := h.process
			h.mu.Unlock()
			if p == nil {
				close(done)
			} else {
				go func() { <-p.done; close(done) }()
			}
		})
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-h.conn.Done():
		}
		stop()
	}()
	return &Endpoint{Input: appWrite, Output: appRead, Stop: stop, Exited: done, Disconnected: h.nativeDone, ProcessID: func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.process == nil {
			return 0
		}
		return h.process.cmd.Process.Pid
	}}, nil
}

func (h *host) disconnected() {
	h.nativeOnce.Do(func() {
		if h.nativeDone != nil {
			close(h.nativeDone)
		}
	})
}

func (h *host) update(ctx context.Context, sessionID string, update any) error {
	return h.conn.SendNotification(ctx, "session/update", map[string]any{"sessionId": sessionID, "update": update})
}

// questionDelivered reports that a client-accepted answer crossed the provider
// boundary for toolCallID. It is sent on the ordered ACP stream, so a caller
// that emits it before the provider can end the turn orders it before the
// prompt response.
func (h *host) questionDelivered(sessionID, toolCallID, evidence string) error {
	if h.conn == nil || sessionID == "" || toolCallID == "" {
		return errors.New("question delivery receipt has no ACP destination")
	}
	return h.update(h.ctx, sessionID, map[string]any{"sessionUpdate": "tui_question_delivery", "dialect": QuestionDeliveryDialect, "toolCallId": toolCallID, "evidence": evidence})
}

func (h *host) launch(args ...string) (*process, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return nil, h.ctx.Err()
	}
	if h.process != nil {
		return nil, errors.New("provider process already launched")
	}
	cmd := exec.Command(h.path, args...)
	cmd.Dir, cmd.Env, cmd.Stderr = h.cwd, h.env, h.stderr
	// os/exec copies stderr into the bounded diagnostic sink. A descendant
	// holding that inherited pipe must not prevent Wait from reporting reaping.
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Use our own pipe so Wait cannot close stdout before the decoder drains it.
	out, writer, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	cmd.Stdout = writer
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		_ = writer.Close()
		return nil, err
	}
	_ = writer.Close()
	pctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	p := &process{stdin: in, stdout: out, done: done, ctx: pctx, cancel: cancel, cmd: cmd}
	h.process = p
	go func() {
		_ = cmd.Wait()
		cancel()
		close(done)
		h.disconnected()
		// Keep stdout and ACP open while decoders drain final frames. An exited
		// runtime's descendants cannot hold the pipe indefinitely.
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-h.ctx.Done():
		case <-timer.C:
		}
		p.close()
	}()
	go func() { <-h.ctx.Done(); p.close() }()
	return p, nil
}

type process struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   <-chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	once   sync.Once
}

func (p *process) close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.cancel()
		_ = p.stdin.Close()
		// Terminate this application's process group only. No name-based kills.
		terminateProcess(p.cmd, false)
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
		// A runtime may exit before its owned tool descendants do.
		terminateProcess(p.cmd, true)
		_ = p.stdout.Close()
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
	})
}

func invalid(err error) *acp.RequestError {
	return acp.NewInvalidParams(map[string]any{"error": err.Error()})
}
func internal(err error) *acp.RequestError {
	return acp.NewInternalError(map[string]any{"error": err.Error()})
}
