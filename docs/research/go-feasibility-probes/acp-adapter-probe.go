//go:build ignore

// Run explicitly from an isolated module with the versions in
// docs/research/acp-live-probe-2026-09-22.md:
//
//	module acpprobe; require github.com/coder/acp-go-sdk v0.13.5
//
// Command acpprobe is a throwaway evidence-gathering probe: it drives a
// pinned ACP adapter over stdio using github.com/coder/acp-go-sdk and records
// the raw JSON-RPC wire traffic plus typed summaries.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// ---------- wire recorder ----------

type wireLine struct {
	T     string          `json:"t"`
	Phase string          `json:"phase"`
	Dir   string          `json:"dir"` // "send" (client->agent) or "recv" (agent->client)
	Msg   json.RawMessage `json:"msg"`
	Raw   string          `json:"raw,omitempty"`
}

type recorder struct {
	mu    sync.Mutex
	phase string
	lines []wireLine
	start time.Time
}

func newRecorder() *recorder { return &recorder{phase: "startup", start: time.Now()} }

func (r *recorder) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
}

func (r *recorder) add(dir string, b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := wireLine{
		T:     fmt.Sprintf("+%.3fs", time.Since(r.start).Seconds()),
		Phase: r.phase,
		Dir:   dir,
	}
	trimmed := strings.TrimSpace(string(b))
	if json.Valid([]byte(trimmed)) {
		l.Msg = json.RawMessage(trimmed)
	} else {
		l.Raw = trimmed
	}
	r.lines = append(r.lines, l)
}

// recvJSON returns copies of all recv message bodies recorded so far.
func (r *recorder) recvJSON() []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []json.RawMessage
	for _, l := range r.lines {
		if l.Dir == "recv" && len(l.Msg) > 0 {
			out = append(out, l.Msg)
		}
	}
	return out
}

func (r *recorder) dump(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range r.lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return nil
}

// loggingWriter records every write (the SDK writes exactly one JSON line per Write).
type loggingWriter struct {
	w io.Writer
	r *recorder
}

func (lw loggingWriter) Write(p []byte) (int, error) {
	lw.r.add("send", p)
	return lw.w.Write(p)
}

// teeLines reads newline-delimited JSON from src, records each line, and
// forwards it verbatim to the returned reader.
func teeLines(src io.Reader, r *recorder) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			r.add("recv", line)
			if _, err := pw.Write(append(append([]byte{}, line...), '\n')); err != nil {
				break
			}
		}
		_ = pw.CloseWithError(io.EOF)
	}()
	return pr
}

// ---------- client implementation ----------

type probeClient struct {
	r            *recorder
	mu           sync.Mutex
	permRequests []map[string]any
	updateKinds  map[string]int
	unsupported  []string
}

var _ acp.Client = (*probeClient)(nil)

func newProbeClient(r *recorder) *probeClient {
	return &probeClient{r: r, updateKinds: map[string]int{}}
}

func (c *probeClient) noteUnsupported(method string) error {
	c.mu.Lock()
	c.unsupported = append(c.unsupported, method)
	c.mu.Unlock()
	return acp.NewMethodNotFound(method)
}

func (c *probeClient) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	kind := "unknown"
	switch u := n.Update; {
	case u.AgentMessageChunk != nil:
		kind = "agent_message_chunk"
	case u.AgentThoughtChunk != nil:
		kind = "agent_thought_chunk"
	case u.UserMessageChunk != nil:
		kind = "user_message_chunk"
	case u.ToolCall != nil:
		kind = "tool_call"
	case u.ToolCallUpdate != nil:
		kind = "tool_call_update"
	case u.Plan != nil:
		kind = "plan"
	case u.AvailableCommandsUpdate != nil:
		kind = "available_commands_update"
	case u.CurrentModeUpdate != nil:
		kind = "current_mode_update"
	}
	c.mu.Lock()
	c.updateKinds[kind]++
	c.mu.Unlock()
	return nil
}

func (c *probeClient) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	rec := map[string]any{"options": p.Options, "toolCall": p.ToolCall, "_meta": p.Meta}
	if len(p.Options) == 0 {
		c.mu.Lock()
		rec["selected"] = nil
		c.permRequests = append(c.permRequests, rec)
		c.mu.Unlock()
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
		}, nil
	}
	// Prefer an "allow once"-style option.
	pick := p.Options[0]
	for _, o := range p.Options {
		if string(o.Kind) == "allow_once" {
			pick = o
			break
		}
	}
	if string(pick.Kind) != "allow_once" {
		for _, o := range p.Options {
			if strings.HasPrefix(string(o.Kind), "allow") {
				pick = o
				break
			}
		}
	}
	c.mu.Lock()
	rec["selected"] = map[string]any{"optionId": pick.OptionId, "kind": pick.Kind, "name": pick.Name}
	c.permRequests = append(c.permRequests, rec)
	c.mu.Unlock()
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: pick.OptionId},
		},
	}, nil
}

func (c *probeClient) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, c.noteUnsupported("fs/read_text_file")
}
func (c *probeClient) WriteTextFile(ctx context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, c.noteUnsupported("fs/write_text_file")
}
func (c *probeClient) CreateTerminal(ctx context.Context, p acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, c.noteUnsupported("terminal/create")
}
func (c *probeClient) KillTerminal(ctx context.Context, p acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, c.noteUnsupported("terminal/kill")
}
func (c *probeClient) TerminalOutput(ctx context.Context, p acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, c.noteUnsupported("terminal/output")
}
func (c *probeClient) ReleaseTerminal(ctx context.Context, p acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, c.noteUnsupported("terminal/release")
}
func (c *probeClient) WaitForTerminalExit(ctx context.Context, p acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, c.noteUnsupported("terminal/wait_for_exit")
}

// ---------- adapter process ----------

type adapter struct {
	cmd    *exec.Cmd
	conn   *acp.ClientSideConnection
	client *probeClient
	rec    *recorder
	stdin  io.WriteCloser
	stderr *strings.Builder
	mu     sync.Mutex
}

var stripEnvPrefixes = []string{
	"CLAUDECODE", "CLAUDE_CODE_", "AI_AGENT", "CLAUDE_EFFORT", "CLAUDE_PID",
}

func childEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		skip := false
		for _, p := range stripEnvPrefixes {
			if strings.HasPrefix(key, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

func startAdapter(bin string, args []string, cwd string) (*adapter, error) {
	rec := newRecorder()
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv()
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errBuf strings.Builder
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	a := &adapter{cmd: cmd, rec: rec, stdin: stdin, stderr: &errBuf}
	go func() {
		sc := bufio.NewScanner(errPipe)
		sc.Buffer(make([]byte, 0, 1<<16), 4<<20)
		for sc.Scan() {
			a.mu.Lock()
			errBuf.WriteString(sc.Text())
			errBuf.WriteString("\n")
			a.mu.Unlock()
		}
	}()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	a.client = newProbeClient(rec)
	a.conn = acp.NewClientSideConnection(a.client, loggingWriter{w: stdin, r: rec}, teeLines(stdout, rec))
	return a, nil
}

func (a *adapter) stderrText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stderr.String()
}

// closeAndWait closes stdin and reports how the process exited.
func (a *adapter) closeAndWait(timeout time.Duration) map[string]any {
	out := map[string]any{}
	t0 := time.Now()
	_ = a.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case err := <-done:
		out["exitedAfterStdinClose"] = true
		out["waitSeconds"] = round3(time.Since(t0).Seconds())
		out["exitCode"] = a.cmd.ProcessState.ExitCode()
		if err != nil {
			out["waitError"] = err.Error()
		}
	case <-time.After(timeout):
		out["exitedAfterStdinClose"] = false
		out["waitedSeconds"] = round3(timeout.Seconds())
		_ = a.cmd.Process.Kill()
		<-done
		out["killed"] = true
	}
	if s := a.stderrText(); s != "" {
		out["stderr"] = s
	}
	return out
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// ---------- helpers ----------

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func errInfo(err error) map[string]any {
	if err == nil {
		return nil
	}
	m := map[string]any{"error": err.Error()}
	var re *acp.RequestError
	if errors.As(err, &re) {
		m["jsonrpc"] = map[string]any{"code": re.Code, "message": re.Message, "data": re.Data}
	}
	return m
}

// findResult scans recorded recv lines for a JSON-RPC result containing key.
func findResult(rec *recorder, key string) json.RawMessage {
	for _, m := range rec.recvJSON() {
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(m, &env) != nil || len(env.Result) == 0 {
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(env.Result, &probe) != nil {
			continue
		}
		if _, ok := probe[key]; ok {
			return env.Result
		}
	}
	return nil
}

type selectOption struct {
	ID       string
	Current  string
	Name     string
	Category string
	Values   []string
}

// parseSelectOptions extracts select-type config options from a raw result blob.
func parseSelectOptions(raw json.RawMessage) []selectOption {
	var env struct {
		ConfigOptions []json.RawMessage `json:"configOptions"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	var out []selectOption
	for _, o := range env.ConfigOptions {
		var base struct {
			Type         string          `json:"type"`
			ID           string          `json:"id"`
			Name         string          `json:"name"`
			Category     string          `json:"category"`
			CurrentValue json.RawMessage `json:"currentValue"`
			Options      json.RawMessage `json:"options"`
		}
		if json.Unmarshal(o, &base) != nil || base.Type != "select" {
			continue
		}
		so := selectOption{ID: base.ID, Name: base.Name, Category: base.Category}
		_ = json.Unmarshal(base.CurrentValue, &so.Current)
		// options may be a flat list or a grouped list.
		var flat []struct {
			ID string `json:"value"`
		}
		if json.Unmarshal(base.Options, &flat) == nil && len(flat) > 0 {
			for _, v := range flat {
				so.Values = append(so.Values, v.ID)
			}
		} else {
			var grouped []struct {
				Options []struct {
					ID string `json:"value"`
				} `json:"options"`
			}
			if json.Unmarshal(base.Options, &grouped) == nil {
				for _, g := range grouped {
					for _, v := range g.Options {
						so.Values = append(so.Values, v.ID)
					}
				}
			}
		}
		out = append(out, so)
	}
	return out
}

// ---------- phases ----------

func runBadVersion(bin string, args []string, cwd, outDir string, version int) {
	a, err := startAdapter(bin, args, cwd)
	if err != nil {
		_ = writeJSON(filepath.Join(outDir, "bad-version.json"), map[string]any{"spawnError": err.Error()})
		return
	}
	a.rec.setPhase("bad-version")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	t0 := time.Now()
	resp, err := a.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(version),
		ClientInfo:      &acp.Implementation{Name: "tui-acp-probe", Version: "0.0.0"},
	})
	out := map[string]any{
		"requestedProtocolVersion": version,
		"elapsedSeconds":           round3(time.Since(t0).Seconds()),
	}
	if err != nil {
		out["result"] = "error"
		out["errorDetail"] = errInfo(err)
	} else {
		out["result"] = "ok"
		out["response"] = resp
	}
	out["shutdown"] = a.closeAndWait(10 * time.Second)
	_ = writeJSON(filepath.Join(outDir, "bad-version.json"), out)
	_ = a.rec.dump(filepath.Join(outDir, "bad-version.wire.jsonl"))
}

func runMain(bin string, args []string, cwd, outDir, askMode string) {
	a, err := startAdapter(bin, args, cwd)
	if err != nil {
		_ = writeJSON(filepath.Join(outDir, "spawn-error.json"), map[string]any{"spawnError": err.Error()})
		return
	}
	defer func() { _ = a.rec.dump(filepath.Join(outDir, "main.wire.jsonl")) }()

	ctx := context.Background()

	// --- initialize ---
	a.rec.setPhase("initialize")
	ictx, icancel := context.WithTimeout(ctx, 60*time.Second)
	t0 := time.Now()
	initResp, err := a.conn.Initialize(ictx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "tui-acp-probe", Version: "0.0.0"},
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
		},
	})
	icancel()
	initOut := map[string]any{"elapsedSeconds": round3(time.Since(t0).Seconds())}
	if err != nil {
		initOut["errorDetail"] = errInfo(err)
	} else {
		initOut["response"] = initResp
	}
	if raw := findResult(a.rec, "protocolVersion"); raw != nil {
		initOut["rawResult"] = raw
	}
	_ = writeJSON(filepath.Join(outDir, "initialize.json"), initOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize failed: %v\n", err)
		_ = writeJSON(filepath.Join(outDir, "shutdown.json"), a.closeAndWait(10*time.Second))
		return
	}

	// --- session/new ---
	a.rec.setPhase("session-new")
	nctx, ncancel := context.WithTimeout(ctx, 120*time.Second)
	t0 = time.Now()
	sess, err := a.conn.NewSession(nctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	ncancel()
	nsOut := map[string]any{"elapsedSeconds": round3(time.Since(t0).Seconds()), "cwd": cwd}
	if err != nil {
		nsOut["errorDetail"] = errInfo(err)
	} else {
		nsOut["response"] = sess
	}
	rawNS := findResult(a.rec, "sessionId")
	if rawNS != nil {
		nsOut["rawResult"] = rawNS
	}
	_ = writeJSON(filepath.Join(outDir, "new-session.json"), nsOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session/new failed: %v\n", err)
		_ = writeJSON(filepath.Join(outDir, "shutdown.json"), a.closeAndWait(10*time.Second))
		return
	}

	// --- prompt: pong ---
	a.rec.setPhase("prompt-pong")
	pctx, pcancel := context.WithTimeout(ctx, 180*time.Second)
	t0 = time.Now()
	presp, perr := a.conn.Prompt(pctx, acp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("Reply with exactly the word pong and nothing else.")},
	})
	pcancel()
	a.client.mu.Lock()
	kinds := map[string]int{}
	for k, v := range a.client.updateKinds {
		kinds[k] = v
	}
	perms := append([]map[string]any(nil), a.client.permRequests...)
	a.client.updateKinds = map[string]int{}
	a.client.permRequests = nil
	unsup := append([]string(nil), a.client.unsupported...)
	a.client.unsupported = nil
	a.client.mu.Unlock()
	pOut := map[string]any{
		"elapsedSeconds":      round3(time.Since(t0).Seconds()),
		"updateKindCounts":    kinds,
		"permissionRequests":  perms,
		"unsupportedCalledBy": unsup,
	}
	if perr != nil {
		pOut["errorDetail"] = errInfo(perr)
	} else {
		pOut["response"] = presp
	}
	if raw := findResult(a.rec, "stopReason"); raw != nil {
		pOut["rawResult"] = raw
	}
	_ = writeJSON(filepath.Join(outDir, "prompt-pong.json"), pOut)

	// --- session/set_config_option ---
	a.rec.setPhase("set-config-option")
	scOut := map[string]any{}
	opts := parseSelectOptions(rawNS)
	scOut["advertisedSelectOptions"] = opts
	var target *selectOption
	var newVal string
	pickFrom := func(pref func(selectOption) bool) {
		for i := range opts {
			o := opts[i]
			if target != nil || !pref(o) {
				continue
			}
			for _, v := range o.Values {
				if v != o.Current && v != "default" {
					target = &opts[i]
					newVal = v
					break
				}
			}
		}
	}
	pickFrom(func(o selectOption) bool { return o.ID == "model" || o.Category == "model" })
	pickFrom(func(o selectOption) bool { return o.ID == "effort" || o.Category == "thought_level" })
	pickFrom(func(o selectOption) bool { return true })
	if target == nil {
		scOut["skipped"] = "no select config option with an alternative value was advertised"
	} else {
		scOut["configId"] = target.ID
		scOut["previousValue"] = target.Current
		scOut["requestedValue"] = newVal
		sctx, sccancel := context.WithTimeout(ctx, 60*time.Second)
		t0 = time.Now()
		scResp, scErr := a.conn.SetSessionConfigOption(sctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				SessionId: sess.SessionId,
				ConfigId:  acp.SessionConfigId(target.ID),
				Value:     acp.SessionConfigValueId(newVal),
			},
		})
		sccancel()
		scOut["elapsedSeconds"] = round3(time.Since(t0).Seconds())
		if scErr != nil {
			scOut["errorDetail"] = errInfo(scErr)
		} else {
			scOut["responseOptionCount"] = len(scResp.ConfigOptions)
		}
		if raw := findResultInPhase(a.rec, "set-config-option", "configOptions"); raw != nil {
			scOut["rawResult"] = raw
		}
	}
	_ = writeJSON(filepath.Join(outDir, "set-config-option.json"), scOut)

	// --- set an "ask" mode, then run a prompt that needs a tool permission ---
	a.rec.setPhase("prompt-tool")
	toolOut := map[string]any{"askModeId": askMode}
	if askMode != "" {
		mctx, mcancel := context.WithTimeout(ctx, 60*time.Second)
		_, mErr := a.conn.SetSessionMode(mctx, acp.SetSessionModeRequest{
			SessionId: sess.SessionId,
			ModeId:    acp.SessionModeId(askMode),
		})
		mcancel()
		if mErr != nil {
			toolOut["setSessionModeError"] = errInfo(mErr)
		} else {
			toolOut["setSessionMode"] = "ok"
		}
	}
	a.client.mu.Lock()
	a.client.updateKinds = map[string]int{}
	a.client.permRequests = nil
	a.client.unsupported = nil
	a.client.mu.Unlock()
	tctx, tcancel := context.WithTimeout(ctx, 240*time.Second)
	t0 = time.Now()
	tresp, terr := a.conn.Prompt(tctx, acp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt: []acp.ContentBlock{acp.TextBlock(
			"Create a file named probe.txt in the current directory containing exactly the text hi. " +
				"Then stop and reply with the word done.")},
	})
	tcancel()
	a.client.mu.Lock()
	tkinds := map[string]int{}
	for k, v := range a.client.updateKinds {
		tkinds[k] = v
	}
	tperms := append([]map[string]any(nil), a.client.permRequests...)
	tunsup := append([]string(nil), a.client.unsupported...)
	a.client.updateKinds = map[string]int{}
	a.client.permRequests = nil
	a.client.unsupported = nil
	a.client.mu.Unlock()
	toolOut["elapsedSeconds"] = round3(time.Since(t0).Seconds())
	toolOut["updateKindCounts"] = tkinds
	toolOut["permissionRequests"] = tperms
	toolOut["unsupportedCalledByAgent"] = tunsup
	if terr != nil {
		toolOut["errorDetail"] = errInfo(terr)
	} else {
		toolOut["response"] = tresp
	}
	_ = writeJSON(filepath.Join(outDir, "prompt-tool.json"), toolOut)

	// --- prompt + cancel ---
	a.rec.setPhase("prompt-cancel")
	cctx, cccancel := context.WithTimeout(ctx, 180*time.Second)
	cancelSent := make(chan float64, 1)
	go func() {
		time.Sleep(2 * time.Second)
		ct := time.Now()
		_ = a.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: sess.SessionId})
		cancelSent <- round3(time.Since(ct).Seconds())
		_ = ct
	}()
	t0 = time.Now()
	cresp, cerr := a.conn.Prompt(cctx, acp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("Count slowly from 1 to 50, one number per line, pausing between numbers.")},
	})
	promptReturned := time.Since(t0)
	cccancel()
	<-cancelSent
	// Let trailing notifications land.
	time.Sleep(2 * time.Second)
	a.client.mu.Lock()
	ckinds := map[string]int{}
	for k, v := range a.client.updateKinds {
		ckinds[k] = v
	}
	cperms := append([]map[string]any(nil), a.client.permRequests...)
	a.client.mu.Unlock()
	cOut := map[string]any{
		"cancelSentAfterSeconds":     2.0,
		"promptReturnedAfterSeconds": round3(promptReturned.Seconds()),
		"updateKindCounts":           ckinds,
		"permissionRequests":         cperms,
	}
	if cerr != nil {
		cOut["errorDetail"] = errInfo(cerr)
	} else {
		cOut["response"] = cresp
	}
	_ = writeJSON(filepath.Join(outDir, "prompt-cancel.json"), cOut)

	// --- shutdown ---
	a.rec.setPhase("shutdown")
	_ = writeJSON(filepath.Join(outDir, "shutdown.json"), a.closeAndWait(15*time.Second))
}

func findResultInPhase(rec *recorder, phase, key string) json.RawMessage {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, l := range rec.lines {
		if l.Dir != "recv" || l.Phase != phase || len(l.Msg) == 0 {
			continue
		}
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(l.Msg, &env) != nil {
			continue
		}
		if len(env.Error) > 0 {
			return l.Msg
		}
		if len(env.Result) == 0 {
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(env.Result, &probe) != nil {
			continue
		}
		if _, ok := probe[key]; ok {
			return env.Result
		}
	}
	return nil
}

func main() {
	var (
		bin     = flag.String("bin", "", "adapter executable")
		argsCSV = flag.String("args", "", "comma-separated adapter args")
		cwd     = flag.String("cwd", "", "session cwd (absolute)")
		outDir  = flag.String("out", "", "output directory")
		phase   = flag.String("phase", "all", "all|main|badversion")
		badVer  = flag.Int("badversion", 99, "protocol version for the bad-version probe")
		askMode = flag.String("askmode", "", "session mode id that asks for approval")
	)
	flag.Parse()
	if *bin == "" || *cwd == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, "missing -bin/-cwd/-out")
		os.Exit(2)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var args []string
	if *argsCSV != "" {
		args = strings.Split(*argsCSV, ",")
	}
	if *phase == "all" || *phase == "main" {
		runMain(*bin, args, *cwd, *outDir, *askMode)
	}
	if *phase == "all" || *phase == "badversion" {
		runBadVersion(*bin, args, *cwd, *outDir, *badVer)
	}
	fmt.Println("done:", *outDir)
}
