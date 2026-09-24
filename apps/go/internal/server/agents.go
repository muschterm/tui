package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// flushInterval bounds streamed persistence and publication to ten per second.
// Commands and turn outcomes flush immediately instead.
const flushInterval = 100 * time.Millisecond

// Bounded waits for an agent that may be unresponsive. None of them blocks a
// command, a snapshot read or another thread's work.
const (
	initializeTimeout = 30 * time.Second
	sessionTimeout    = 30 * time.Second
	cancelTimeout     = 5 * time.Second
	probeTimeout      = 60 * time.Second
	// Startup probes are shorter: readiness is a convenience, never a gate on
	// the server becoming usable.
	startupProbeTimeout = 30 * time.Second
)

// maxPendingApprovals bounds the approval cards one thread can accumulate.
const maxPendingApprovals = 16

func (e *engine) logf(msg string, args ...any) {
	if e.log != nil {
		e.log.Info(msg, args...)
	}
}

func (e *engine) launcher() agent.Launcher {
	if e.launch != nil {
		return e.launch
	}
	return agent.Start
}

func (e *engine) baseContext() context.Context {
	if e.runctx != nil {
		return e.runctx
	}
	return context.Background()
}

func threadByID(s *protocol.Snapshot, id string) *protocol.Thread {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i]
		}
	}
	return nil
}

// flushLocked persists and publishes the current snapshot. A storage failure is
// retained so the server loop can stop rather than keep streaming into a home
// that is no longer recording anything.
func (e *engine) flushLocked() {
	e.dirty, e.lastFlush = false, time.Now()
	if err := e.store.Save(e.snap, nil, nil); err != nil && e.flushErr == nil {
		e.flushErr = err
	}
	e.publish()
}

func (e *engine) maybeFlushLocked() {
	if !e.dirty {
		return
	}
	if elapsed := time.Since(e.lastFlush); elapsed >= flushInterval {
		e.flushLocked()
		return
	}
	if e.flushScheduled {
		return
	}
	e.flushScheduled = true
	time.AfterFunc(flushInterval-time.Since(e.lastFlush), func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.flushScheduled = false
		e.maybeFlushLocked()
	})
}

// streamMutate applies one streamed agent change to the authoritative snapshot
// and persists it at the coalesced rate.
func (e *engine) streamMutate(fn func(*protocol.Snapshot)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.snap)
	e.snap.Revision++
	e.dirty = true
	e.maybeFlushLocked()
}

// commitMutate applies a turn boundary or request change and persists it now.
func (e *engine) commitMutate(fn func(*protocol.Snapshot)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.snap)
	// Turn boundaries can end a writer lease; wake the next waiter.
	e.rebalanceWritersLocked()
	e.snap.Revision++
	e.flushLocked()
}

func (e *engine) mutateThread(id string, commit bool, fn func(*protocol.Thread)) {
	apply := func(s *protocol.Snapshot) {
		if t := threadByID(s, id); t != nil {
			fn(t)
		}
	}
	if commit {
		e.commitMutate(apply)
		return
	}
	e.streamMutate(apply)
}

// applyCommand routes the commands the engine itself owns and otherwise defers
// to the pure snapshot transition.
func (e *engine) applyCommand(next *protocol.Snapshot, c protocol.Command, resolved *resolvedPath) (string, error) {
	if c.Kind == "agent.probe" {
		return e.applyProbe(next, c)
	}
	if c.Kind == "thread.resume" {
		// A fixture Resume continues its interrupted turn in place, so it needs
		// the checkout writer lease now; it cannot wait in line.
		if t := threadByID(next, c.ThreadID); t != nil && !agent.IsACP(t.AgentID) && t.NeedsResume && !t.Closed && t.State != "idle" {
			// Refuse both an active holder and an earlier waiting candidate, so
			// Resume never jumps the line.
			if e.writerBlocked(next, t) {
				return "", failure("checkout_busy", "another thread is running on this checkout; resume after it finishes")
			}
		}
	}
	target, err := applyResolved(next, c, resolved)
	if err == nil && (c.Kind == "prompt.send" || c.Kind == "prompt.reopen-send" || c.Kind == "thread.start" || c.Kind == "thread.resume") {
		id := c.ThreadID
		if c.Kind == "thread.start" {
			id = target
		}
		e.startQueuedFixture(next, threadByID(next, id))
	}
	if err == nil && c.Kind == "request.answer" {
		if t := threadByID(next, c.ThreadID); t != nil && agent.IsACP(t.AgentID) {
			if r := e.runs[t.ID]; r == nil || (!r.liveApproval(t, c.TargetID) && !r.liveQuestion(t, c.TargetID)) {
				return "", failure("stale_request", "the answer no longer belongs to a live agent request")
			}
			if err := e.runs[t.ID].validateQuestionAnswer(t, c.TargetID); err != nil {
				return "", err
			}
		}
	}
	return target, err
}

// afterCommit launches the effects of a committed command. The engine lock is
// held, so effects only enqueue bounded work or start goroutines.
func (e *engine) afterCommit(c protocol.Command, target string) {
	for id, r := range e.runs {
		if threadByID(&e.snap, id) == nil {
			delete(e.runs, id)
			go r.stop()
		}
	}
	switch c.Kind {
	case "agent.probe":
		if a := agent.Find(&e.snap, target); a != nil && a.State == agent.StateProbing && !e.probes[target] {
			e.probes[target] = true
			cwd, err := probeDirectory(&e.snap, c)
			record := *a
			e.probeWG.Add(1)
			go e.probe(record, cwd, err)
		}
	case "thread.interrupt":
		if r := e.runs[c.ThreadID]; r != nil {
			go r.interrupt()
		}
	case "thread.delete":
		// The sweep above already stopped the run; nothing else to do.
	case "request.answer":
		e.resolveApproval(c)
	case "thread.start":
		e.ensureRunLocked(target)
	default:
		e.ensureRunLocked(c.ThreadID)
	}
	// Any committed command can end a lease or make a waiter eligible.
	e.rebalanceWritersAndFlushLocked()
}

// resolveApproval hands a committed answer to the blocked ACP permission call.
func (e *engine) resolveApproval(c protocol.Command) {
	r := e.runs[c.ThreadID]
	t := threadByID(&e.snap, c.ThreadID)
	if r == nil || t == nil || !agent.IsACP(t.AgentID) {
		return
	}
	for _, request := range t.Requests {
		if request.ID == c.TargetID && request.Kind == "question" && request.State == "submitted" && request.SubmissionID == c.ID {
			r.resolveQuestion(request.ID, c.ID)
			return
		}
		if request.ID == c.TargetID && request.Kind == "approval" && request.State == "submitted" && request.SubmissionID == c.ID {
			r.resolve(request.ID, request.ApprovalChoiceID)
			return
		}
	}
}

// ensureRunLocked starts the dispatch loop for a thread with queued work.
func (e *engine) ensureRunLocked(threadID string) {
	t := threadByID(&e.snap, threadID)
	if t == nil || !agent.IsACP(t.AgentID) || t.Closed || t.NeedsResume || len(t.Queue) == 0 || t.State != "idle" {
		return
	}
	r := e.runs[threadID]
	if r == nil {
		ctx, cancel := context.WithCancel(e.baseContext())
		r = &acpRun{e: e, threadID: threadID, ctx: ctx, cancel: cancel, pending: map[string]*pendingApproval{}}
		e.runs[threadID] = r
	}
	if r.busy {
		return
	}
	r.busy = true
	go r.loop()
}

// stopAgents cancels every owned agent connection and waits for its process to
// be closed or killed.
func (e *engine) stopAgents() {
	e.mu.Lock()
	runs := make([]*acpRun, 0, len(e.runs))
	for id, r := range e.runs {
		runs = append(runs, r)
		delete(e.runs, id)
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Add(1)
		go func(r *acpRun) { defer wg.Done(); r.stop() }(r)
	}
	wg.Wait()
	// Probe processes are children of the same run context; wait, bounded, for
	// them to record their outcome so no adapter outlives the server.
	done := make(chan struct{})
	go func() { e.probeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(cancelTimeout):
		e.logf("agent probes did not finish before shutdown")
	}
}

// probeDirectory resolves the working directory a provisional probe session
// uses: the named project's checkout, else the server's project starting
// folder, else the server home.
func probeDirectory(s *protocol.Snapshot, c protocol.Command) (string, error) {
	candidate := s.AppSettings.ProjectDirectory
	if c.ProjectID != "" {
		found := false
		for _, p := range s.Projects {
			if p.ID == c.ProjectID {
				candidate, found = p.Path, true
			}
		}
		if !found {
			return "", failure("not_found", "project does not exist")
		}
	}
	if candidate == "" || strings.Contains(candidate, "://") {
		candidate = "~"
	}
	expanded, err := expandHome(candidate)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", failure("invalid", "cannot resolve a working directory for the probe")
	}
	return abs, nil
}

func (e *engine) applyProbe(next *protocol.Snapshot, c protocol.Command) (string, error) {
	a := agent.Find(next, c.TargetID)
	if a == nil {
		return "", failure("not_found", "agent is not configured")
	}
	if a.Kind != agent.KindACP {
		return "", failure("unsupported_agent", "the fixture agent runs no process and has nothing to probe")
	}
	if _, err := probeDirectory(next, c); err != nil {
		return "", err
	}
	if e.probes[a.ID] || a.State == agent.StateProbing {
		// A refresh requested while the startup probe (or another client's
		// probe) is still running coalesces with it: the result lands on this
		// same record, so a second process would only duplicate work.
		return a.ID, nil
	}
	a.State = agent.StateProbing
	a.Detail = "Probing " + a.Command + " …"
	a.Revision++
	return a.ID, nil
}

// probeConfiguredAgents probes every configured ACP connection once at startup
// so readiness, models and settings are populated without a user action. It
// never blocks startup: each probe runs concurrently, is bounded, and is
// cancelled with the server's run context.
func (e *engine) probeConfiguredAgents() {
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		return
	}
	type pending struct {
		record protocol.Agent
		cwd    string
		err    error
	}
	var queued []pending
	for i := range e.snap.Agents {
		a := &e.snap.Agents[i]
		if a.Kind != agent.KindACP || e.probes[a.ID] || a.State == agent.StateProbing {
			continue
		}
		cwd, err := probeDirectory(&e.snap, protocol.Command{})
		e.probes[a.ID] = true
		a.State, a.Detail = agent.StateProbing, "Probing "+a.Command+" …"
		a.Revision++
		// The wait group is joined under the lock so a concurrent shutdown
		// cannot observe a probe that has been marked but not yet counted.
		e.probeWG.Add(1)
		queued = append(queued, pending{record: *a, cwd: cwd, err: err})
	}
	if len(queued) > 0 {
		e.snap.Revision++
		e.flushLocked()
	}
	e.mu.Unlock()
	for _, item := range queued {
		go func(item pending) {
			defer e.probeWG.Done()
			e.probeWithTimeout(item.record, item.cwd, item.err, startupProbeTimeout)
		}(item)
	}
}

// probe launches the adapter, negotiates the protocol, records the config
// options of a provisional session and then closes it. It runs without the
// engine lock: an unresponsive adapter cannot stall unrelated commands.
func (e *engine) probe(record protocol.Agent, cwd string, cwdErr error) {
	defer e.probeWG.Done()
	e.probeWithTimeout(record, cwd, cwdErr, probeTimeout)
}

func (e *engine) probeWithTimeout(record protocol.Agent, cwd string, cwdErr error, timeout time.Duration) {
	state, detail := agent.StateUnavailable, ""
	var version string
	var options []protocol.ConfigOption
	var fields protocol.SettingFields
	var capabilities []string
	defer func() {
		e.commitMutate(func(s *protocol.Snapshot) {
			a := agent.Find(s, record.ID)
			if a == nil {
				return
			}
			a.State, a.Detail, a.Version = state, agent.Truncate(detail, 4<<10), version
			a.ProbedAt = time.Now().UTC().Format(time.RFC3339)
			a.Options, a.Fields, a.Capabilities = options, fields, capabilities
			a.Revision++
		})
		e.mu.Lock()
		delete(e.probes, record.ID)
		e.mu.Unlock()
	}()
	if cwdErr != nil {
		detail = cwdErr.Error()
		return
	}
	ctx, cancel := context.WithTimeout(e.baseContext(), timeout)
	defer cancel()
	session, err := e.launcher()(ctx, agent.Options{AgentID: record.ID, Command: record.Command, Args: record.Args, Cwd: cwd, Log: e.logf})
	if err != nil {
		detail = agent.Message(err)
		if errors.Is(err, agent.ErrExecutableMissing) {
			detail += "\n\n" + agent.InstallHint
		}
		return
	}
	defer session.Close(context.Background())
	info, err := session.Initialize(ctx)
	if err != nil {
		detail = probeDetail(session, "initialize failed: "+agent.Message(err))
		if agent.AuthRequired(err) {
			state = agent.StateUnauthenticated
		}
		return
	}
	version, capabilities = info.Version, info.Capabilities
	_, probed, err := session.NewSession(ctx, cwd)
	if err != nil {
		detail = probeDetail(session, "session setup failed: "+agent.Message(err))
		if agent.AuthRequired(err) {
			state = agent.StateUnauthenticated
			detail = "Sign in with this adapter's own CLI. " + authDetail(info)
		}
		return
	}
	options = probed
	fields = agent.Fields(options)
	state = agent.StateReady
	detail = fmt.Sprintf("Probed %s in %s: %d settings reported.", record.Command, cwd, len(options))
	if path := session.RuntimePath(); path != "" {
		detail += " Local CLI: " + path
	}
}

func authDetail(info agent.Info) string {
	if len(info.AuthMethods) == 0 {
		return "The adapter reported no authentication methods."
	}
	return "Reported methods: " + strings.Join(info.AuthMethods, ", ") + "."
}

// probeDetail keeps the adapter's own diagnostics visible; a missing login or a
// broken install is usually only explained on its stderr.
func probeDetail(session *agent.Session, message string) string {
	if diagnostics := session.Diagnostics(); diagnostics != "" {
		return message + "\n\nAdapter output:\n" + diagnostics
	}
	return message
}

// acpRun is one thread's live agent connection and its dispatch loop.
type acpRun struct {
	e        *engine
	threadID string
	ctx      context.Context
	cancel   context.CancelFunc

	// busy is guarded by the engine lock; everything below by mu.
	busy bool

	mu        sync.Mutex
	session   *agent.Session
	info      agent.Info
	options   []protocol.ConfigOption
	pending   map[string]*pendingApproval
	questions map[string]*pendingQuestion
	// delivered maps answered question IDs to the turn in which this
	// connection's built-in bridge reported provider-side delivery.
	delivered   map[string]string
	generation  string
	interrupted bool
	cancelCh    chan struct{}
	turnDone    chan struct{}
}

type dispatchWork struct {
	prompt    protocol.Prompt
	record    protocol.Agent
	checkout  string
	sessionID string
}

func (r *acpRun) loop() {
	for {
		work, ok := r.claim()
		if !ok {
			return
		}
		r.dispatch(work)
		// The dispatch is over: the lease now follows the thread's state
		// alone, and the next claim competes fairly with waiters.
		r.e.mu.Lock()
		delete(r.e.claiming, r.threadID)
		r.e.rebalanceWritersAndFlushLocked()
		r.e.mu.Unlock()
	}
}

// claim takes the queue head of an idle thread, or ends the loop. A failed
// dispatch leaves the thread failed, so retries require an explicit Send or
// Resume rather than spinning.
func (r *acpRun) claim() (dispatchWork, bool) {
	e := r.e
	e.mu.Lock()
	defer e.mu.Unlock()
	t := threadByID(&e.snap, r.threadID)
	if t == nil || !agent.IsACP(t.AgentID) || t.Closed || t.NeedsResume || t.State != "idle" || len(t.Queue) == 0 || e.stopping || e.flushErr != nil {
		r.busy = false
		// Every claim that ends without a turn lets the next waiter proceed.
		e.rebalanceWritersAndFlushLocked()
		return dispatchWork{}, false
	}
	a := agent.Find(&e.snap, t.AgentID)
	if a == nil || a.State != agent.StateReady {
		r.busy = false
		name := t.Agent
		t.State, t.Error = "failed", name+" is not ready; probe the agent before sending."
		e.rebalanceWritersLocked()
		e.snap.Revision++
		e.flushLocked()
		return dispatchWork{}, false
	}
	if !e.acquireWriter(&e.snap, t) {
		// Another thread holds this checkout, or an earlier waiter is next.
		// The queue and idle state stay unchanged; a release wakes this runner.
		r.busy = false
		e.rebalanceWritersAndFlushLocked()
		return dispatchWork{}, false
	}
	if t.Checkout != "" {
		if e.claiming == nil {
			e.claiming = map[string]string{}
		}
		e.claiming[t.ID] = t.Checkout
	}
	return dispatchWork{prompt: t.Queue[0], record: *a, checkout: t.Checkout, sessionID: t.SessionID}, true
}

func (r *acpRun) dispatch(w dispatchWork) {
	session, info, err := r.ensureSession(w)
	if err != nil {
		r.fail(w, "Agent connection failed", err)
		return
	}
	blocks, notices, err := agent.PromptBlocks(w.prompt, w.checkout, info, func(id string) ([]byte, error) {
		_, data, err := r.e.store.ReadArtifact(id, artifactLimit)
		return data, err
	})
	if err == nil {
		// Send acceptance already bounded the prompt; this is a backstop.
		if encoded, marshalErr := json.Marshal(blocks); marshalErr != nil || len(encoded) > acpPromptBudget {
			err = errors.Join(marshalErr, fmt.Errorf("prompt with attachments exceeds %d MiB", acpPromptBudget>>20))
		}
	}
	if err != nil {
		r.fail(w, "Attachment could not be sent", err)
		return
	}
	effective, options, err := r.applySettings(session, w)
	if err != nil {
		r.fail(w, "Agent rejected the captured settings", err)
		return
	}
	if !r.startTurn(w, effective, options, notices) {
		return
	}
	// Keep the request alive until the peer acknowledges cancellation or its
	// connection ends. Local context cancellation is not an ACP stop response.
	response, err := session.Prompt(r.ctx, blocks)
	r.finishTurn(w, response, err)
}

func (r *acpRun) ensureSession(w dispatchWork) (*agent.Session, agent.Info, error) {
	r.mu.Lock()
	var disconnected *agent.Session
	if r.session != nil {
		select {
		case <-r.session.Done():
			disconnected = r.session
			r.session, r.options = nil, nil
		default:
			session, info := r.session, r.info
			r.mu.Unlock()
			return session, info, nil
		}
	}
	r.mu.Unlock()
	if disconnected != nil {
		disconnected.Close(context.Background())
	}
	cwd := w.checkout
	if cwd == "" || strings.Contains(cwd, "://") {
		return nil, agent.Info{}, fmt.Errorf("this thread has no local checkout, so no agent session can be opened in it")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, agent.Info{}, err
	}
	generation := ID()
	r.mu.Lock()
	r.generation = generation
	r.mu.Unlock()
	handler := &acpHandler{acpRun: r, generation: generation, questions: w.record.ID == "claude" || w.record.Command == agent.DefaultCodexCommand}
	session, err := r.e.launcher()(r.ctx, agent.Options{AgentID: w.record.ID, Command: w.record.Command, Args: w.record.Args, Cwd: abs, Handler: handler, Log: r.e.logf})
	if err != nil {
		return nil, agent.Info{}, err
	}
	ctx, cancel := context.WithTimeout(r.ctx, initializeTimeout)
	info, err := session.Initialize(ctx)
	cancel()
	if err != nil {
		session.Kill()
		return nil, agent.Info{}, fmt.Errorf("%s: %s", agent.Message(err), session.Diagnostics())
	}
	ctx, cancel = context.WithTimeout(r.ctx, sessionTimeout)
	defer cancel()
	var options []protocol.ConfigOption
	restored := false
	if w.sessionID != "" && info.Has(agent.CapLoadSession) {
		if options, err = session.LoadSession(ctx, w.sessionID, abs); err == nil {
			restored = true
		} else {
			r.e.logf("agent session load failed; opening a new session", "thread", r.threadID, "error", err)
		}
	}
	if !restored {
		var id string
		if id, options, err = session.NewSession(ctx, abs); err != nil {
			session.Kill()
			return nil, agent.Info{}, fmt.Errorf("%s: %s", agent.Message(err), session.Diagnostics())
		}
		r.e.mutateThread(r.threadID, true, func(t *protocol.Thread) {
			if w.sessionID != "" {
				// An earlier session existed but could not be restored; say so
				// instead of letting the agent look like it remembers.
				t.Activity = append(t.Activity, protocol.Activity{ID: "session-reset-" + id, TurnID: t.TurnID, Role: "tool", Title: "Upstream context not restored", State: "completed", Text: "A new agent session was opened", Detail: "The previous session " + w.sessionID + " could not be loaded, so this agent starts without the earlier conversation. Nothing was resent."})
				agent.TrimActivity(t)
			}
			t.SessionID = id
		})
	}
	r.mu.Lock()
	r.session, r.info, r.options, r.generation = session, info, options, generation
	r.mu.Unlock()
	live := options
	r.e.mutateThread(r.threadID, true, func(t *protocol.Thread) { t.Options = live })
	return session, info, nil
}

// applySettings applies each mapped captured setting before the prompt and
// records the agent's own reported option state as the effective settings.
//
// Validation uses the live session's catalogue where one exists: the probe
// recorded that switching claude's model adds a further option in the same
// response, so the probe-time list is a starting catalogue, not a contract.
func (r *acpRun) applySettings(session *agent.Session, w dispatchWork) (protocol.Settings, []protocol.ConfigOption, error) {
	r.mu.Lock()
	options := r.options
	r.mu.Unlock()
	record := w.record
	if len(options) > 0 {
		record.Options, record.Fields = options, agent.Fields(options)
	}
	if err := agent.ValidateSettings(record, w.prompt.Settings); err != nil {
		return protocol.Settings{}, nil, err
	}
	for _, assignment := range agent.Assignments(record, w.prompt.Settings) {
		ctx, cancel := context.WithTimeout(r.ctx, sessionTimeout)
		updated, err := session.SetConfigOption(ctx, assignment)
		cancel()
		if err != nil {
			return protocol.Settings{}, nil, fmt.Errorf("%s = %q: %s", assignment.Field, assignment.Value, agent.Message(err))
		}
		// The response carries the agent's complete option state, which can
		// differ in shape from the one before the call.
		options = updated
	}
	r.mu.Lock()
	r.options = options
	r.mu.Unlock()
	return agent.EffectiveSettings(agent.Fields(options), options), options, nil
}

// currentDispatch is checked under the engine lock after asynchronous setup.
func currentDispatch(t *protocol.Thread, w dispatchWork) bool {
	return t != nil && !t.Closed && !t.NeedsResume && t.State == "idle" && len(t.Queue) > 0 && t.Queue[0].ID == w.prompt.ID && t.Queue[0].Revision == w.prompt.Revision
}

// startTurn moves the captured prompt out of the queue into the transcript and
// opens the turn only if the captured queue head is still current. Commit this
// boundary before prompt I/O: after a crash, a possibly sent prompt must not
// still look like undispatched queue work.
func (r *acpRun) startTurn(w dispatchWork, effective protocol.Settings, options []protocol.ConfigOption, notices []string) bool {
	e := r.e
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	t := threadByID(&next, r.threadID)
	if !currentDispatch(t, w) || e.stopping || e.flushErr != nil || r.ctx.Err() != nil {
		return false
	}
	t.Queue = t.Queue[1:]
	t.QueueRevision++
	t.TurnID, t.State, t.StopReason, t.Error = w.prompt.ID, "running", "", ""
	t.Effective, t.Options = effective, options
	prompt := w.prompt
	entry := protocol.Activity{ID: prompt.ID, TurnID: prompt.ID, Prompt: &prompt, Role: "user", Text: prompt.Text, State: "running", Detail: promptSummary(prompt)}
	replaced := false
	for i := range t.Activity {
		if t.Activity[i].ID == entry.ID {
			t.Activity[i], replaced = entry, true
			break
		}
	}
	if !replaced {
		t.Activity = append(t.Activity, entry)
	}
	for i, notice := range notices {
		t.Activity = append(t.Activity, protocol.Activity{ID: fmt.Sprintf("notice-%s-%d", prompt.ID, i), TurnID: prompt.ID, Role: "tool", Title: "Attachment not sent", State: "failed", Text: notice, Detail: notice})
	}
	agent.TrimActivity(t)
	next.Revision++
	if err := e.store.Save(next, nil, nil); err != nil {
		e.flushErr = err
		return false
	}
	r.mu.Lock()
	r.interrupted, r.cancelCh, r.turnDone = false, make(chan struct{}), make(chan struct{})
	r.mu.Unlock()
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	return true
}

func (r *acpRun) finishTurn(w dispatchWork, response acp.PromptResponse, err error) {
	r.mu.Lock()
	interrupted := r.interrupted
	if r.turnDone != nil {
		close(r.turnDone)
		r.turnDone = nil
	}
	r.cancelCh = nil
	delivered := r.delivered
	r.delivered = nil
	r.mu.Unlock()
	cancelled := interrupted || response.StopReason == acp.StopReasonCancelled
	r.e.mutateThread(r.threadID, true, func(t *protocol.Thread) {
		for i := range t.Requests {
			request := &t.Requests[i]
			if request.TurnID != w.prompt.ID {
				continue
			}
			if request.State == "pending" || request.State == "submitted" {
				request.State, request.Delivery = "closed", "acp-undeliverable"
				if cancelled {
					request.Delivery = "acp-cancelled"
				}
				request.Revision++
			} else if err != nil && request.Delivery == "acp-unconfirmed" {
				request.Delivery = "acp-uncertain"
				request.Revision++
			} else if err == nil && !cancelled && t.AgentID == "claude" && r.info.Version == acpbridge.ClaudeIdentity && request.Delivery == "acp-unconfirmed" && request.Kind == "question" && request.Mode == "blocking" && request.Action == "" && delivered[request.ID] == w.prompt.ID {
				// Claude's receipt follows its successful tool result. With a
				// normal end of that blocking turn, the answer is confirmed.
				// Codex's receipt only follows a pipe write; App Server can still
				// discard that response, so its turn end cannot settle delivery.
				request.State, request.Delivery = "resolved", "acp-turn-confirmed"
				request.Revision++
			}
		}
		if cancelled {
			t.StopReason, t.State, t.NeedsResume = string(acp.StopReasonCancelled), "interrupted", true
			setActivityState(t, w.prompt.ID, "interrupted")
			closeStreams(t, w.prompt.ID, "interrupted")
			return
		}
		if err != nil {
			t.State, t.NeedsResume = "failed", true
			t.Error = "The turn failed; its prompt remains in history and will not be replayed. Review any effects before Resume. " + agent.Truncate(agent.Sanitize(agent.Message(err)), 3<<10)
			setActivityState(t, w.prompt.ID, "failed")
			closeStreams(t, w.prompt.ID, "failed")
			// The full capture already lives on the user activity. A failed RPC
			// may follow tool effects or an accepted answer; Send/Resume must not
			// turn it into a fresh execution of that original prompt.
			return
		}
		t.StopReason, t.State, t.Error = string(response.StopReason), "idle", ""
		setActivityState(t, w.prompt.ID, "completed")
		closeStreams(t, w.prompt.ID, "completed")
	})
	if err != nil {
		r.e.logf("agent turn failed", "thread", r.threadID, "error", err)
	}
}

func setActivityState(t *protocol.Thread, id, state string) {
	for i := range t.Activity {
		if t.Activity[i].ID == id {
			t.Activity[i].State = state
		}
	}
}

// closeStreams settles the streamed reply and reasoning activities of one turn.
func closeStreams(t *protocol.Thread, turnID, state string) {
	for i := range t.Activity {
		a := &t.Activity[i]
		if a.TurnID != turnID || a.State != "running" {
			continue
		}
		switch a.Role {
		case "agent", "thought":
			a.State = state
		case "tool", "mcp":
			if state != "completed" {
				a.State = state
			}
		}
	}
}

// fail records a dispatch failure before the turn started. The prompt stays at
// the head of the queue and no activity claims work that did not happen.
func (r *acpRun) fail(w dispatchWork, title string, err error) {
	message := agent.Truncate(agent.Sanitize(title+": "+err.Error()), 4<<10)
	r.e.mutateThread(r.threadID, true, func(t *protocol.Thread) {
		if !currentDispatch(t, w) {
			return
		}
		t.State, t.Error = "failed", message
		t.Activity = append(t.Activity, protocol.Activity{ID: "dispatch-" + w.prompt.ID + "-" + ID(), TurnID: t.TurnID, Role: "tool", Title: title, State: "failed", Text: "The prompt stays queued; nothing was sent.", Detail: message})
		agent.TrimActivity(t)
	})
	r.e.logf("agent dispatch failed", "thread", r.threadID, "error", err)
	r.clearSession()
}

func (r *acpRun) clearSession() {
	r.mu.Lock()
	session := r.session
	r.session, r.options = nil, nil
	r.mu.Unlock()
	if session != nil {
		session.Kill()
	}
}

// interrupt sends session/cancel and waits, bounded, for the agent to end the
// turn with the cancelled stop reason before killing the process.
func (r *acpRun) interrupt() {
	r.mu.Lock()
	r.interrupted = true
	if r.cancelCh != nil {
		close(r.cancelCh)
		r.cancelCh = nil
	}
	session, done := r.session, r.turnDone
	r.mu.Unlock()
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, cancelTimeout)
	defer cancel()
	if err := session.Cancel(ctx); err != nil {
		r.e.logf("agent cancel notification failed", "thread", r.threadID, "error", err)
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(cancelTimeout):
		r.e.logf("agent did not end the cancelled turn in time; ending its process", "thread", r.threadID)
		r.clearSession()
	}
}

// resolve hands a committed answer to the blocked permission call.
func (r *acpRun) resolve(requestID, optionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pending := r.pending[requestID]; pending != nil {
		// One durable command can hand off once. The buffered channel avoids
		// waiting for a provider callback while the engine lock is held.
		select {
		case pending.answer <- optionID:
		default:
		}
	}
}

func (r *acpRun) stop() {
	r.mu.Lock()
	session := r.session
	r.session = nil
	for id, pending := range r.pending {
		delete(r.pending, id)
		close(pending.answer)
	}
	if r.cancelCh != nil {
		close(r.cancelCh)
		r.cancelCh = nil
	}
	r.mu.Unlock()
	if session != nil {
		session.Close(context.Background())
	}
	r.cancel()
}

// SessionUpdate streams one agent update into the snapshot.
func (h *acpHandler) SessionUpdate(_ context.Context, sessionID string, u agent.Update) error {
	r := h.acpRun
	if toolCallID, ok := agent.QuestionDeliveryReceipt(u); ok {
		h.questionDelivered(sessionID, toolCallID)
		return nil
	}
	r.e.streamMutate(func(s *protocol.Snapshot) {
		t := threadByID(s, r.threadID)
		if t == nil {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if h.generation != r.generation || sessionID == "" || sessionID != t.SessionID {
			return
		}
		agent.Normalize(t, u)
		if u.Kind == "config_option_update" {
			r.options = t.Options
		}
	})
	return nil
}

// questionDelivered records a built-in bridge's delivery receipt for an
// answer this connection handed off in the current turn. It changes no
// durable state: finishTurn decides whether the turn's outcome settles it,
// and a restart before then leaves the answer uncertain.
func (h *acpHandler) questionDelivered(sessionID, toolCallID string) {
	h.mu.Lock()
	version := h.info.Version
	h.mu.Unlock()
	if version != acpbridge.ClaudeIdentity && version != acpbridge.CodexIdentity {
		return // Only our pinned bridges define this receipt.
	}
	r, e := h.acpRun, h.e
	e.mu.Lock()
	defer e.mu.Unlock()
	t := threadByID(&e.snap, r.threadID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if t == nil || h.generation != r.generation || sessionID == "" || sessionID != t.SessionID || r.turnDone == nil {
		return
	}
	for _, request := range t.Requests {
		if request.Kind == "question" && request.TurnID == t.TurnID && request.State == "closed" && request.Delivery == "acp-unconfirmed" && request.Action == "" && request.DeliveryRoute == "native-response" && agent.QuestionToolCallID(request.SourcePayload) == toolCallID {
			if r.delivered == nil {
				r.delivered = make(map[string]string)
			}
			r.delivered[request.ID] = t.TurnID
		}
	}
}
