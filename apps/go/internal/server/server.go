// Package server owns durable fixture execution independently of attached clients.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
	"golang.org/x/sys/unix"
)

// ID returns a fresh random identity for a server incarnation or record.
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type engine struct {
	mu          sync.Mutex
	snap        protocol.Snapshot
	store       *storage.Store
	subscribers map[chan protocol.Snapshot]bool
	stopping    bool
	// resolve replaces canonicalProjectPath in tests.
	resolve func(string) (string, error)
	// launch starts an agent connection; tests substitute an in-process agent.
	launch agent.Launcher
	log    *slog.Logger
	// runctx bounds every agent child process the server owns.
	runctx context.Context
	// runs holds one live agent connection per ACP thread, and probes the
	// agent IDs whose probe is in flight.
	runs    map[string]*acpRun
	probes  map[string]bool
	probeWG sync.WaitGroup
	// Streamed agent output persists and publishes at a bounded rate; commands
	// and turn outcomes flush immediately.
	dirty          bool
	flushScheduled bool
	lastFlush      time.Time
	flushErr       error
	// uploads and previews bound concurrent artifact uploads and file previews.
	uploads, previews chan struct{}
	// waitSince/waitSeq and claiming implement checkout writer coordination;
	// see writer.go. claiming maps an ACP thread to the checkout whose lease
	// its runner holds between claim and the end of that dispatch.
	waitSince map[string]uint64
	waitSeq   uint64
	claiming  map[string]string
	// terminals holds embedded terminal sessions; see terminal.go.
	terminals terminalState
	// git tracks running Git writes and their writer leases; see git_write.go.
	git gitWriteState
	// baselineDir keeps resolution jobs' index listings (git_resolve_job.go).
	baselineDir string
	// worktreeDir holds managed worktrees (worktrees.go); empty disables
	// worktree creation. worktreesCheckedAt throttles detection.
	worktreeDir string
	// worktreeAsync holds worktree creations running in the background
	// after their request returned (worktrees.go), by command ID.
	worktreeAsync      map[string]worktreeInflight
	worktreesCheckedAt time.Time
	// docs holds shared documents; see documents.go.
	docs documentState
}

func clone(s protocol.Snapshot) protocol.Snapshot {
	b, _ := json.Marshal(s)
	var n protocol.Snapshot
	_ = json.Unmarshal(b, &n)
	return n
}

// clientSnapshot exposes the normalized contract, not provider form payloads
// retained for recovery/diagnostics. This projection never mutates storage.
func clientSnapshot(s protocol.Snapshot) protocol.Snapshot {
	n := clone(s)
	for i := range n.Threads {
		for j := range n.Threads[i].Requests {
			n.Threads[i].Requests[j].SourcePayload = nil
		}
	}
	return n
}

func (e *engine) publish() {
	syncWorktreeCeilings(e.snap.Worktrees)
	for ch := range e.subscribers {
		select {
		case ch <- clientSnapshot(e.snap):
		default:
			close(ch)
			delete(e.subscribers, ch)
		}
	}
}

func newEngine(snap protocol.Snapshot, st *storage.Store) *engine {
	clearWriterWaits(&snap)
	return &engine{snap: snap, store: st, subscribers: map[chan protocol.Snapshot]bool{}, runs: map[string]*acpRun{}, probes: map[string]bool{},
		uploads: make(chan struct{}, uploadSlots), previews: make(chan struct{}, previewSlots)}
}

func (e *engine) command(c protocol.Command) (protocol.Receipt, error) {
	return e.commandContext(context.Background(), c)
}

func (e *engine) commandContext(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	if c.Version != protocol.Version {
		return protocol.Receipt{}, failure("version_mismatch", "protocol version 1 required")
	}
	if c.ID == "" || len(c.ID) > 128 {
		return protocol.Receipt{}, failure("invalid", "bounded command identity required")
	}
	if c.Kind == "terminal.open" {
		return e.openTerminal(ctx, c)
	}
	if c.Kind == "thread.start" && c.Workspace != nil && c.Workspace.Mode == "worktree" {
		return e.worktreeStart(ctx, c)
	}
	if c.Kind == "worktree.remove" || c.Kind == "worktree.prune" {
		return e.worktreeGitCommand(ctx, c)
	}
	if strings.HasPrefix(c.Kind, "git.") && !jobControlKind(c.Kind) {
		return e.gitWriteCommand(ctx, c)
	}
	if strings.HasPrefix(c.Kind, "document.") {
		return e.documentCommand(ctx, c)
	}
	e.mu.Lock()
	if r, err := e.store.Lookup(c); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	} else if r != nil {
		e.mu.Unlock()
		return *r, nil
	}
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	// A client-supplied path can sit on a hung mount; like file capture, it is
	// resolved without the lock and any competing retry is reconciled after.
	resolved := &resolvedPath{}
	if input, needed := pathToResolve(e.snap, c); needed {
		e.mu.Unlock()
		resolved = e.resolvePath(ctx, input)
		e.mu.Lock()
		if r, err := e.store.Lookup(c); err != nil {
			e.mu.Unlock()
			return protocol.Receipt{}, err
		} else if r != nil {
			e.mu.Unlock()
			return *r, nil
		}
		if e.stopping {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("stopping", "server is shutting down")
		}
	}
	captured := c
	if usesWorkspaceFiles(c) || usesArtifacts(c) {
		captureState := clone(e.snap)
		e.mu.Unlock()
		// Reading user files cannot stall unrelated commands, snapshots or ticks.
		// A competing retry may commit while this request reads; reconcile its
		// receipt before using these captures or reporting a read failure.
		var captureErr error
		captured, captureErr = captureCommand(ctx, captureState, c, e.store)
		e.mu.Lock()
		if r, err := e.store.Lookup(c); err != nil {
			e.mu.Unlock()
			return protocol.Receipt{}, err
		} else if r != nil {
			e.mu.Unlock()
			return *r, nil
		}
		if e.stopping {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("stopping", "server is shutting down")
		}
		if captureErr != nil {
			e.mu.Unlock()
			return protocol.Receipt{}, captureErr
		}
		if usesWorkspaceFiles(c) {
			before, errBefore := captureRoot(captureState, c)
			after, errAfter := captureRoot(e.snap, c)
			if errBefore != nil || errAfter != nil || before != after {
				e.mu.Unlock()
				return protocol.Receipt{}, failure("stale_workspace", "workspace changed while capturing context; review and send again")
			}
		}
		if err := validateArtifactDelivery(&e.snap, captured); err != nil {
			e.mu.Unlock()
			return protocol.Receipt{}, err
		}
	}
	defer e.mu.Unlock()
	if err := e.gitCommandGuardLocked(captured); err != nil {
		return protocol.Receipt{}, err
	}
	next := clone(e.snap)
	target, err := e.applyCommand(&next, captured, resolved)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if c.Kind == "thread.delete" || c.Kind == "project.remove" {
		pruneGitOps(&next)
	}
	next.Revision++
	// Commands that do not grow state stay available to an over-limit home.
	if size := projectedSize(next); size > snapshotLimit && size > projectedSize(e.snap)+capacitySlack {
		return protocol.Receipt{}, failure("capacity", "fixture snapshot exceeds 4 MiB; remove queued content before submitting")
	}
	r := protocol.Receipt{ID: c.ID, State: "accepted", Revision: next.Revision, TargetID: target}
	if c.Kind == "queue.steer" {
		r.State = "fixture-delivered"
	}
	if err = e.store.Save(next, &c, &r); err != nil {
		return protocol.Receipt{}, err
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	e.afterCommit(c, target)
	e.afterTerminalCommitLocked(c, target)
	return r, nil
}

func (e *engine) tick() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	changed := false
	for i := range next.Threads {
		t := &next.Threads[i]
		// ACP threads advance through their dispatch runner, not fixture ticks.
		if t.NeedsResume || t.State != "running" || agent.IsACP(t.AgentID) {
			continue
		}
		changed = true
		if t.Tick >= fixtureTurnTicks {
			if fixtureTurnCompleted(t) {
				// The finished turn releases the lease; the next queued prompt
				// reacquires it only when no earlier waiter is eligible.
				t.State = "idle"
				if len(t.Queue) == 0 || worktreeUnavailable(&next, t) != nil || !e.acquireWriter(&next, t) {
					continue
				}
				startFixturePrompt(t)
			} else {
				// Older snapshots used an unbounded tick counter. Finish their
				// preserved turn before starting any queued prompt.
				t.Tick = fixtureTurnTicks - 1
			}
		}
		t.Tick++
		if t.Tick == fixtureTurnTicks/2 {
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "completed"
				}
			}
			for j := range t.Plan {
				if t.Plan[j].State == "active" {
					t.Plan[j].State = "completed"
					if j+1 < len(t.Plan) {
						t.Plan[j+1].State = "active"
					}
					break
				}
			}
		}
		if t.Tick >= fixtureTurnTicks {
			for j := range t.Plan {
				t.Plan[j].State = "completed"
			}
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "completed"
				}
			}
			promptID := fixtureTurnID(t)
			for j := range t.Activity {
				if t.Activity[j].Role == "user" && (t.Activity[j].TurnID == promptID || t.Activity[j].ID == promptID) {
					t.Activity[j].State = "completed"
				}
			}
			result := protocol.Activity{ID: "result-" + promptID, TurnID: promptID, Role: "agent", Title: "Fixture agent", Text: "Synthetic review complete. Captured settings and attachments were preserved; no agent or tool was executed." + fixtureAttachmentEcho(t, promptID), State: "completed"}
			found := false
			for j := range t.Activity {
				if t.Activity[j].ID == result.ID {
					t.Activity[j] = result
					found = true
					break
				}
			}
			if !found {
				t.Activity = append(t.Activity, result)
			}
			if len(t.Queue) == 0 {
				t.State = "idle"
			}
		}
		trimFixtureActivity(t)
	}
	// A finished fixture resolution job moves its operation to review.
	if out := syncJobStatesLocked(&next, e.jobOpKindLocked); out.changed {
		e.afterJobSyncLocked(out)
		changed = true
	}
	if !changed {
		// Candidates are derived, so a free checkout is re-examined every tick
		// (for example queued threads restored at startup).
		e.rebalanceWritersAndFlushLocked()
		return nil
	}
	next.Revision++
	if err := e.store.Save(next, nil, nil); err != nil {
		return err
	}
	e.snap = next
	e.publish()
	e.rebalanceWritersAndFlushLocked()
	return nil
}

// fixtureAttachmentEcho lists the uploaded attachments a synthetic turn
// "received". Nothing inspects their bytes; it only echoes accepted metadata.
func fixtureAttachmentEcho(t *protocol.Thread, promptID string) string {
	var lines []string
	for _, a := range t.Activity {
		if a.Role != "user" || a.Prompt == nil || (a.TurnID != promptID && a.ID != promptID) {
			continue
		}
		for _, attachment := range a.Prompt.Attachments {
			if attachment.ArtifactID == "" {
				continue
			}
			line := fmt.Sprintf("%s (%s, %d bytes", attachment.Name, attachment.MediaType, attachment.Size)
			if attachment.Width > 0 {
				line += fmt.Sprintf(", %d×%d", attachment.Width, attachment.Height)
			}
			lines = append(lines, line+")")
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nSynthetic attachment echo (not inspected): " + strings.Join(lines, "; ")
}

// Fixture turns use durable per-turn progress so Resume continues saved work.
const fixtureTurnTicks = 8

func fixtureTurnCompleted(t *protocol.Thread) bool {
	for _, step := range t.Plan {
		if step.State != "completed" {
			return false
		}
	}
	for _, child := range t.Children {
		if child.State == "running" || child.State == "interrupted" {
			return false
		}
	}
	promptID := fixtureTurnID(t)
	for _, a := range t.Activity {
		if a.ID == "result-"+promptID && a.State == "completed" {
			return true
		}
	}
	return false
}

// Legacy snapshots have no identity; derive it from their latest original input
// before they can accept steering, and persist it with startup recovery.
func fixtureTurnID(t *protocol.Thread) string {
	if t.TurnID != "" {
		return t.TurnID
	}
	for i := len(t.Activity) - 1; i >= 0; i-- {
		if t.Activity[i].Role == "user" {
			return t.Activity[i].ID
		}
	}
	return t.ID + "-initial"
}

const snapshotLimit = 4 << 20

// Interrupt, resume and similar state changes re-encode a few longer words.
const capacitySlack = 256

// Dispatch adds a child, result, plan and archive record per queued prompt,
// each repeating the prompt identity.
const dispatchOverhead = 2048

// projectedSize is the encoded snapshot after every queued prompt dispatches,
// so ticks cannot push accepted work past the bound. Revision is excluded.
func projectedSize(s protocol.Snapshot) int {
	s.Revision = 0
	encoded, _ := json.Marshal(s)
	size := len(encoded)
	for i := range s.Threads {
		for _, request := range s.Threads[i].Requests {
			if request.Kind == "approval" && request.DeliveryRoute == "native-response" && request.State == "pending" {
				size += nativeApprovalAnswerReserve(request)
			}
			if request.Kind == "question" && request.DeliveryRoute == "native-response" && request.State == "pending" && len(request.SourcePayload) > 0 {
				size += nativeAnswerReserve
			}
		}
		for _, p := range s.Threads[i].Queue {
			queued, _ := json.Marshal(p)
			dispatched, _ := json.Marshal(fixturePromptActivity(&s.Threads[i], p))
			id, _ := json.Marshal(p.ID)
			size += max(0, len(dispatched)-len(queued)) + dispatchOverhead + 12*len(id)
		}
	}
	return size
}

// Activity.Prompt retains the complete capture; Detail only summarizes it.
func promptSummary(p protocol.Prompt) string {
	type attachment struct {
		Kind, Name, Source string
		Size               int
		ArtifactID         string `json:",omitempty"`
		MediaType          string `json:",omitempty"`
	}
	summary := struct {
		ID, Text    string
		Revision    int64
		Settings    protocol.Settings
		Attachments []attachment
	}{ID: p.ID, Text: p.Text, Revision: p.Revision, Settings: p.Settings}
	for _, a := range p.Attachments {
		summary.Attachments = append(summary.Attachments, attachment{a.Kind, a.Name, a.Source, len(a.Content), a.ArtifactID, a.MediaType})
	}
	b, _ := json.Marshal(summary)
	return string(b)
}

func fixturePromptActivity(t *protocol.Thread, p protocol.Prompt) protocol.Activity {
	return protocol.Activity{ID: p.ID, TurnID: t.TurnID, Prompt: &p, Role: "user", Text: p.Text, State: "running", Detail: promptSummary(p)}
}

func appendFixturePrompt(t *protocol.Thread, p protocol.Prompt) {
	t.Activity = append(t.Activity, fixturePromptActivity(t, p))
}

// Older homes stored the whole capture again in Detail. Compact only exact
// duplicates of the retained Prompt.
func compactPromptDetails(s *protocol.Snapshot) {
	for i := range s.Threads {
		for j := range s.Threads[i].Activity {
			a := &s.Threads[i].Activity[j]
			if a.Prompt == nil {
				continue
			}
			if duplicate, _ := json.Marshal(a.Prompt); a.Detail == string(duplicate) {
				a.Detail = promptSummary(*a.Prompt)
			}
		}
	}
}

func startFixturePrompt(t *protocol.Thread) {
	p := t.Queue[0]
	t.Queue = t.Queue[1:]
	t.QueueRevision++
	t.Effective = p.Settings
	t.State, t.Tick = "running", 0
	t.TurnID = p.ID
	// Keep the accepted capture after it leaves the editable queue. Activity
	// retention applies to this fixture history just as it does to other detail.
	appendFixturePrompt(t, p)
	t.Plan = []protocol.PlanStep{{Title: "Review the captured prompt", State: "active"}, {Title: "Complete the synthetic review", State: "pending"}}
	// Bound the fixture inspector while preserving older child detail in the
	// ordinary, explicitly retained activity history.
	if len(t.Children) >= 32 {
		child := t.Children[0]
		detail, _ := json.Marshal(child)
		t.Activity = append(t.Activity, protocol.Activity{ID: "archive-" + child.ID, Role: "agent", Title: child.Name, State: child.State, Text: "Earlier synthetic child history archived from the Agents inspector.", Detail: string(detail)})
		t.Children = t.Children[1:]
	}
	t.Children = append(t.Children, protocol.Child{ID: "child-" + p.ID, ParentID: t.ID, Name: "Prompt review", State: "running", Activity: []protocol.Activity{{ID: "review-" + p.ID, Role: "agent", Text: "Reviewing the captured prompt. This child and its history are synthetic."}}})
	trimFixtureActivity(t)
}

func trimFixtureActivity(t *protocol.Thread) {
	if len(t.Activity) <= 128 {
		return
	}
	// The notice owns a slot; never replace the detail of a retained capture.
	notice := protocol.Activity{ID: "history-limit-" + t.ID, Role: "tool", Title: "History retention", State: "completed", Text: "Earlier fixture activity was truncated by the 128-item retention limit."}
	t.Activity = append([]protocol.Activity{notice}, t.Activity[len(t.Activity)-127:]...)
}

// Serve runs the server for home until ctx ends or a stop is requested. It
// takes the home's exclusive lock, opens its database and publishes discovery.
func Serve(ctx context.Context, home string) error {
	abs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	home = abs
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err = os.Chmod(home, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(home, "server.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return failure("already_running", "application home is locked by another server")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	dbPath := filepath.Join(home, "state.sqlite")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	if err = os.Chmod(dbPath, 0600); err != nil {
		return err
	}
	st, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err = st.UseArtifacts(filepath.Join(home, "artifacts")); err != nil {
		return err
	}
	snap, exists, err := st.Load()
	if err != nil {
		return err
	}
	if exists && snap.Version != protocol.Version {
		return failure("version_mismatch", "stored snapshot version is unsupported; state was not modified")
	}
	if !exists {
		snap = fixture.Initial()
	} else {
		recoverThreads(&snap)
		demoteConcurrentWriters(&snap)
		compactPromptDetails(&snap)
		endLoadedTerminals(&snap)
		recoverGitOps(&snap)
		reconcileWorktrees(&snap)
		if err = resolveInterruptedGitReceipts(st); err != nil {
			return err
		}
		snap.Revision++
	}
	for i := range snap.Threads {
		t := &snap.Threads[i]
		if t.TurnID == "" {
			t.TurnID = fixtureTurnID(t)
		}
	}
	if !slices.Contains(snap.Capabilities, "fixture-steering") {
		snap.Capabilities = append(snap.Capabilities, "fixture-steering")
	}
	ensureProjects(&snap)
	ensureAppSettings(&snap)
	if !slices.Contains(snap.Capabilities, "project-management") {
		snap.Capabilities = append(snap.Capabilities, "project-management")
	}
	if !slices.Contains(snap.Capabilities, "thread-lifecycle") {
		snap.Capabilities = append(snap.Capabilities, "thread-lifecycle")
	}
	if filesSupported && !slices.Contains(snap.Capabilities, "files-read") {
		snap.Capabilities = append(snap.Capabilities, "files-read")
	}
	if docsSupported && !slices.Contains(snap.Capabilities, "shared-documents") {
		snap.Capabilities = append(snap.Capabilities, "shared-documents")
	}
	for _, capability := range []string{"thread-start", "closed-thread-send", "workspace-info", "embedded-terminals", "acp-agents", "agent-probe", "acp-permissions", "acp-cancel", "approval-choice-ids", "git-writes", "git-history", "git-refs", "git-operations", "git-conflicts", "git-jobs", "worktree-create", "worktree-manage"} {
		if !slices.Contains(snap.Capabilities, capability) {
			snap.Capabilities = append(snap.Capabilities, capability)
		}
	}
	// Partial staging depends on the installed Git (ADR 0025); a stored
	// capability from a newer Git is withdrawn.
	snap.Capabilities = slices.DeleteFunc(snap.Capabilities, func(c string) bool { return c == "git-partial-stage" })
	if gitPartialSupported() {
		snap.Capabilities = append(snap.Capabilities, "git-partial-stage")
	}
	// Agent definitions are server-owned: an existing home gains the configured
	// connections on start and a changed executable invalidates its probe.
	agent.Ensure(&snap, os.Getenv)
	snap.InstanceID = ID()
	if err = st.Save(snap, nil, nil); err != nil {
		return err
	}
	e := newEngine(snap, st)
	e.log = slog.Default()
	e.baselineDir = filepath.Join(home, "git-baselines")
	e.worktreeDir = filepath.Join(home, "worktrees")
	e.sweepJobBaselines()
	// Interrupted publications, orphaned files and expired staging are
	// reconciled before any client can reference an artifact.
	e.sweepArtifacts()
	// Stored documents load reconciling; nothing is written before each is
	// compared with its file.
	if err = e.startDocuments(); err != nil {
		return err
	}
	defer e.stopDocuments()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	discovery := protocol.Discovery{Version: protocol.Version, URL: "http://" + listener.Addr().String(), Token: ID() + ID(), InstanceID: snap.InstanceID, Home: home, PID: os.Getpid()}
	b, _ := json.Marshal(discovery)
	tmp := filepath.Join(home, "discovery.tmp")
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(home, "discovery.json")); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(home, "discovery.json"))
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	e.runctx = runctx
	e.mu.Unlock()
	// Agent readiness is server-pushed: every configured connection is probed
	// once now so models and settings are available without a user action. The
	// explicit agent.probe command remains the refresh path and coalesces with
	// a probe that is still running.
	go e.probeConfiguredAgents()
	// Every agent child process is killed before this function returns, even on
	// a failing path, so no adapter outlives the server that owns it.
	defer e.stopAgents()
	defer func() { _ = e.stopTerminals() }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspace", e.workspace)
	mux.HandleFunc("GET /v1/git/status", e.gitStatus)
	mux.HandleFunc("GET /v1/worktrees/removal", e.worktreeRemoval)
	mux.HandleFunc("GET /v1/worktrees/prune", e.worktreePruneRead)
	mux.HandleFunc("GET /v1/git/diff", e.gitDiff)
	mux.HandleFunc("GET /v1/git/hunks", e.gitHunks)
	mux.HandleFunc("GET /v1/git/log", e.gitLog)
	mux.HandleFunc("GET /v1/git/show", e.gitShow)
	mux.HandleFunc("GET /v1/git/branches", e.gitBranches)
	mux.HandleFunc("GET /v1/git/compare", e.gitCompare)
	mux.HandleFunc("GET /v1/git/operation", e.gitOperation)
	mux.HandleFunc("GET /v1/git/operation/backup", e.gitOperationBackup)
	mux.HandleFunc("GET /v1/git/conflict", e.gitConflict)
	mux.HandleFunc("GET /v1/git/integrate/preview", e.gitIntegratePreview)
	mux.HandleFunc("GET /v1/terminals/{id}/stream", e.terminalStream)
	mux.HandleFunc("GET /v1/documents/{id}/stream", e.documentStream)
	mux.HandleFunc("GET /v1/documents/{id}/versions", e.documentVersionsHandler)
	mux.HandleFunc("GET /v1/files/list", e.filesList)
	mux.HandleFunc("GET /v1/files/read", e.filesRead)
	mux.HandleFunc("GET /v1/files/stat", e.filesStat)
	mux.HandleFunc("GET /v1/browse", e.browse)
	mux.HandleFunc("GET /v1/preview", e.previewFile)
	mux.HandleFunc("POST /v1/artifacts", e.uploadArtifact)
	mux.HandleFunc("GET /v1/artifacts/{id}", e.getArtifact)
	mux.HandleFunc("DELETE /v1/artifacts/{id}", e.deleteArtifact)
	respond := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		s := clientSnapshot(e.snap)
		e.mu.Unlock()
		respond(w, s)
	})
	mux.HandleFunc("POST /v1/command", func(w http.ResponseWriter, r *http.Request) {
		var c protocol.Command
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 768*1024)).Decode(&c); err != nil {
			w.WriteHeader(400)
			respond(w, protocol.Error{Code: "invalid", Message: "invalid command JSON"})
			return
		}
		receipt, err := e.commandContext(r.Context(), c)
		if err != nil {
			w.WriteHeader(409)
			var pe *protocol.Error
			if errors.As(err, &pe) {
				respond(w, pe)
			} else {
				respond(w, protocol.Error{Code: "storage", Message: "command could not be persisted"})
			}
			return
		}
		respond(w, receipt)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		watchctx := conn.CloseRead(r.Context())
		ch := make(chan protocol.Snapshot, 8)
		e.mu.Lock()
		ch <- clientSnapshot(e.snap)
		e.subscribers[ch] = true
		e.mu.Unlock()
		defer func() { e.mu.Lock(); delete(e.subscribers, ch); e.mu.Unlock() }()
		for {
			select {
			case <-runctx.Done():
				conn.Close(websocket.StatusGoingAway, "server stopped")
				return
			case <-watchctx.Done():
				return
			case s, ok := <-ch:
				if !ok {
					conn.Close(websocket.StatusPolicyViolation, "resync required: slow client")
					return
				}
				b, _ := json.Marshal(s)
				writeCtx, cancel := context.WithTimeout(watchctx, 3*time.Second)
				err = conn.Write(writeCtx, websocket.MessageText, b)
				cancel()
				if err != nil {
					return
				}
			}
		}
	})
	mux.HandleFunc("GET /v1/views/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := st.LoadView(r.PathValue("id"))
		if err != nil {
			http.Error(w, "view unavailable", 500)
			return
		}
		respond(w, v)
	})
	mux.HandleFunc("PUT /v1/views/{id}", func(w http.ResponseWriter, r *http.Request) {
		var v protocol.View
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&v); err != nil {
			http.Error(w, "invalid view", 400)
			return
		}
		saved, err := e.putView(r.PathValue("id"), v)
		if err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				w.WriteHeader(http.StatusConflict)
				respond(w, pe)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				respond(w, protocol.Error{Code: "storage", Message: "view not persisted"})
			}
			return
		}
		respond(w, saved)
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.stopping = true
		e.mu.Unlock()
		respond(w, map[string]bool{"stopping": true})
		cancel()
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins unsupported", http.StatusForbidden)
			return
		}
		want := "Bearer " + discovery.Token
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-TUI-Protocol") != "1" {
			http.Error(w, "protocol version 1 required", http.StatusUpgradeRequired)
			return
		}
		mux.ServeHTTP(w, r)
	})
	connections := &httpConnections{}
	httpServer := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return runctx },
		ConnState:   connections.changed,
	}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	sweeper := time.NewTicker(artifactSweepInterval)
	defer sweeper.Stop()
	var runErr error
loop:
	for {
		select {
		case <-runctx.Done():
			break loop
		case err := <-errs:
			if !errors.Is(err, http.ErrServerClosed) {
				runErr = err
			}
			break loop
		case <-sweeper.C:
			e.sweepArtifacts()
		case <-ticker.C:
			e.mu.Lock()
			flushErr := e.flushErr
			if flushErr != nil {
				e.stopping = true
			}
			e.mu.Unlock()
			if flushErr != nil {
				runErr = fmt.Errorf("persist streamed agent output: %w", flushErr)
				break loop
			}
			if err := e.tick(); err != nil {
				runErr = fmt.Errorf("persist fixture tick: %w", err)
				break loop
			}
			e.checkWorktrees()
		}
	}
	cancel()
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	// Stop admission and release stalled readers immediately. Drain accepted
	// handlers before saving the final snapshot on a successful drain. A timed
	// out handler cannot later mutate storage: commands and views are gated.
	connections.drain()
	httpDone := make(chan error, 1)
	go func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		err := httpServer.Shutdown(stopCtx)
		if err != nil {
			// A timeout must not leave sockets alive against closed storage.
			_ = httpServer.Close()
		}
		httpDone <- err
	}()
	// Owned agent work is cancelled and its processes ended before the final
	// record is written, so the snapshot cannot claim work that no longer runs.
	// Terminal sessions close concurrently with agents; both are bounded.
	terminalsDone := make(chan struct{})
	var terminalsErr error
	go func() { terminalsErr = e.stopTerminals(); close(terminalsDone) }()
	e.stopAgents()
	e.stopGitWrites()
	e.stopDocuments()
	<-terminalsDone
	stopErr := <-httpDone
	e.mu.Lock()
	final := clone(e.snap)
	for i := range final.Threads {
		t := &final.Threads[i]
		t.RestartEligible = restartEligible(t)
		if agent.IsACP(t.AgentID) {
			t.RestartEligible = false
		}
		if t.State == "running" || t.State == "waiting" {
			t.State = "interrupted"
			t.NeedsResume = true
		}
		for j := range t.Children {
			if t.Children[j].State == "running" {
				t.Children[j].State = "interrupted"
			}
		}
	}
	endStoppedTerminals(&final)
	final.Revision++
	saveErr := st.Save(final, nil, nil)
	e.mu.Unlock()
	closeErr := st.Close()
	outcomeErr := errors.Join(runErr, shutdownStage("save final state", saveErr), shutdownStage("drain HTTP requests", stopErr), shutdownStage("terminals", terminalsErr), shutdownStage("close storage", closeErr))
	outcome := protocol.ShutdownOutcome{InstanceID: discovery.InstanceID, Success: outcomeErr == nil}
	if outcomeErr != nil {
		outcome.Error = outcomeErr.Error()
	}
	recordErr := writeShutdown(home, outcome)
	return errors.Join(outcomeErr, recordErr)
}

func writeShutdown(home string, outcome protocol.ShutdownOutcome) error {
	b, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, "shutdown-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(home, "shutdown-"+outcome.InstanceID+".json")); err != nil {
		return err
	}
	dir, err := os.Open(home)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
