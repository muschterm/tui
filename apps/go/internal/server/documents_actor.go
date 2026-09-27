package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// docActor owns one loaded document. Everything below the "run goroutine"
// marker is touched only by run (or, before run starts, by the goroutine
// that created the actor).
//
// Status (protocol.DocumentStatus.State) is derived:
//
//	paused_conflict | deleted | read_only   meta.Pause set (autosave off, versions kept)
//	reconciling                             comparing with the file (start, resume)
//	failed                                  storage or file write failing; retried with backoff
//	saving                                  a save job is writing
//	pending                                 DurableRev > SavedRev, or updates await commit
//	saved                                   SavedRev == DurableRev and nothing pending
//
// Transitions: an accepted update → pending (batched commit, then ack);
// idle 750 ms or 3 s after the first unsaved change → saving → saved (or
// failed → retry, or reconcile when the file changed); a poll or save that
// finds the file changed → clean merge (server update, save) or a pause;
// document.resolve leaves a pause.
type docActor struct {
	e              *engine
	id, root, path string
	clock          docClock
	store          documentStore
	inbox          chan any
	quit           chan struct{}
	stopOnce       sync.Once

	// gen guards renames: a save may rename only while gen equals the value
	// it started with (see bumpGen).
	gmu sync.Mutex
	gen uint64

	// run goroutine:
	d           *doc.Doc
	meta        docMeta
	baseline    []byte
	reconciling bool
	// storeFailing, saveFailed (writing) and examineFailed (reading the
	// file) with failErr describe the failed state.
	storeFailing, saveFailed, examineFailed bool
	failErr                                 string
	retryDelay                              time.Duration
	retryAt                                 time.Time
	batch                                   []docBatchEntry
	batchBytes                              int
	batchDue                                time.Time
	batchBaseline                           *docBaselineChange
	dirtySince, lastChange                  time.Time
	pollDue                                 time.Time
	job                                     *docJob
	streams                                 map[*docStream]struct{}
	// rewrite coordination (Git hooks).
	rewritePaused  int
	rewriteWaiters []chan error
	rewriteFlushed bool
	stopping       bool
	finalSaveDone  bool
	exited         bool
	lastStatus     *protocol.DocumentStatus
	// updates counts update requests handled (diagnostics and tests).
	updates int64
	// baselineUnpersisted: the in-memory baseline (and the meta describing
	// it) advanced but its record failed; every later metadata write carries
	// the baseline too, so the two are only ever stored together.
	baselineUnpersisted bool
	// restarted: loaded from storage and not yet compared with the file.
	restarted bool
	// stateBytes estimates the encoded CRDT state (-1: unknown).
	stateBytes int
	// tokenAt is when meta.Token was last recorded (wall clock). File
	// timestamps are coarse, so a same-size rewrite shortly after can keep
	// the token; until docRacyWindow has passed, polls reread the content.
	tokenAt time.Time
}

const docRacyWindow = 3 * time.Second

type docBatchEntry struct {
	data   []byte // nil for a duplicate that only needs an ack
	client string // "" for server-origin updates
	stream *docStream
	op     string
	rev    int64
}

// docBaselineChange is committed atomically with the batch it rides on.
type docBaselineChange struct {
	data, token   string
	saved         bool  // the file now holds the resulting text
	savedRev      int64 // with saved: the revision on disk (0 = the new DurableRev)
	clearVersions bool
	format        *fileFormat
}

type docJob struct {
	kind string // save or check
	gen  uint64
	rev  int64
	data []byte
}

type docJobResult struct {
	job       *docJob
	token     string
	obs       docObservation
	err       error
	unchanged bool
	renamed   bool
}

type docCommandMsg struct {
	c     protocol.Command
	reply chan error
}
type docJoinMsg struct {
	s     *docStream
	reply chan error
}
type docLeaveMsg struct{ s *docStream }
type docRequestMsg struct {
	s   *docStream
	req protocol.DocumentRequest
}
type docStopMsg struct{}

// docRunMsg runs fn on the actor goroutine (diagnostics and tests).
type docRunMsg struct {
	fn   func()
	done chan struct{}
}
type docRewriteMsg struct {
	begin bool
	reply chan error
}

func (e *engine) newDocActor(id, root, rel string, d *doc.Doc, meta docMeta, baseline []byte) *docActor {
	if meta.Replicas == nil {
		meta.Replicas = map[string]string{}
	}
	return &docActor{
		e: e, id: id, root: root, path: rel, clock: e.docClock(), store: e.docStore(),
		inbox: make(chan any, 256), quit: make(chan struct{}),
		d: d, meta: meta, baseline: baseline, streams: map[*docStream]struct{}{}, retryDelay: docRetryMin, tokenAt: time.Now(), stateBytes: -1,
	}
}

func (a *docActor) encodeMeta() []byte {
	b, _ := json.Marshal(a.meta)
	return b
}

// post delivers a message unless the actor has exited.
func (a *docActor) post(m any) bool {
	select {
	case a.inbox <- m:
		return true
	case <-a.quit:
		return false
	}
}

// command runs a document command on the actor and waits for its outcome.
func (a *docActor) command(ctx context.Context, c protocol.Command) error {
	reply := make(chan error, 1)
	select {
	case a.inbox <- docCommandMsg{c, reply}:
	case <-a.quit:
		return errActorStopped
	case <-ctx.Done():
		return failure("cancelled", "the request ended; retry with the same command ID")
	}
	select {
	case err := <-reply:
		return err
	case <-a.quit:
		return errActorStopped
	case <-ctx.Done():
		return failure("cancelled", "the request ended; retry with the same command ID")
	}
}

func (a *docActor) stop() {
	a.stopOnce.Do(func() { a.post(docStopMsg{}) })
}

// bumpGen invalidates every save that has not renamed yet. It waits for a
// rename in progress.
func (a *docActor) bumpGen() {
	a.gmu.Lock()
	a.gen++
	a.gmu.Unlock()
}

func (a *docActor) currentGen() uint64 {
	a.gmu.Lock()
	defer a.gmu.Unlock()
	return a.gen
}

func (a *docActor) guard(gen uint64) func(func() error) error {
	return func(rename func() error) error {
		if hook := a.e.docs.beforeRename; hook != nil {
			hook(a.id)
		}
		a.gmu.Lock()
		defer a.gmu.Unlock()
		if a.gen != gen {
			return errStaleSave
		}
		return rename()
	}
}

// --- run goroutine ---

func (a *docActor) run() {
	defer a.e.docs.wg.Done()
	defer close(a.quit)
	now := a.clock.Now()
	a.pollDue = now.Add(docPollInterval)
	if a.dirty() {
		a.dirtySince, a.lastChange = now, now
	}
	if a.meta.Keep != "" && a.meta.Pause == "" {
		// An accepted keep_document resolution saves promptly.
		a.dirtySince, a.lastChange = now.Add(-docSaveMax), now.Add(-docSaveIdle)
	}
	if a.reconciling {
		a.startCheck(true)
	}
	var stopDeadline <-chan time.Time
	for !a.exited {
		wake, cancel := a.timer()
		select {
		case m := <-a.inbox:
			a.handle(m)
		case <-wake:
			a.onTimer()
		case <-stopDeadline:
			a.e.logf("document did not finish its final save before shutdown", "id", a.id)
			a.exited = true
		}
		cancel()
		if a.stopping && stopDeadline == nil {
			t := time.NewTimer(docStopTimeout)
			defer t.Stop()
			stopDeadline = t.C
		}
		a.progress()
	}
}

// timer returns a channel for the earliest pending deadline.
func (a *docActor) timer() (<-chan time.Time, func()) {
	var next time.Time
	consider := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	consider(a.batchDue)
	consider(a.presenceDeadline())
	if t, ok := a.saveDeadline(); ok {
		consider(t)
	}
	if a.pollAllowed() {
		consider(a.pollDue)
	}
	if next.IsZero() {
		return nil, func() {}
	}
	return a.clock.At(next)
}

func (a *docActor) onTimer() {
	now := a.clock.Now()
	if t := a.presenceDeadline(); !t.IsZero() && !now.Before(t) {
		a.flushPresence(now)
	}
	if !a.batchDue.IsZero() && !now.Before(a.batchDue) {
		a.commitBatch()
	}
	if t, ok := a.saveDeadline(); ok && !now.Before(t) {
		a.startSave()
	}
	if a.pollAllowed() && !now.Before(a.pollDue) {
		a.startCheck(a.reconciling)
	}
	a.publishStatus()
}

func (a *docActor) dirty() bool { return a.meta.SavedRev < a.meta.DurableRev }

// writable reports whether autosave may write now (ignoring timing).
func (a *docActor) writable() bool {
	return a.rewritePaused == 0 && a.writableExceptRewrite()
}

func (a *docActor) writableExceptRewrite() bool {
	return !a.reconciling && a.meta.Pause == "" && a.job == nil && len(a.batch) == 0 && a.batchBaseline == nil && !a.storeFailing
}

func (a *docActor) saveDeadline() (time.Time, bool) {
	if !a.dirty() || !a.writable() || a.stopping {
		return time.Time{}, false
	}
	if a.saveFailed {
		return a.retryAt, true
	}
	due := a.lastChange.Add(docSaveIdle)
	if limit := a.dirtySince.Add(docSaveMax); limit.Before(due) {
		due = limit
	}
	return due, true
}

// pollAllowed covers the idle poll and, while reconciling, the retried
// full comparison.
func (a *docActor) pollAllowed() bool {
	return a.job == nil && a.meta.Pause == "" && a.rewritePaused == 0 && !a.stopping
}

func (a *docActor) handle(m any) {
	switch m := m.(type) {
	case docRequestMsg:
		a.request(m.s, m.req)
	case docJoinMsg:
		err := a.join(m.s)
		a.publishStatus()
		m.reply <- err
	case docLeaveMsg:
		if _, ok := a.streams[m.s]; ok {
			delete(a.streams, m.s)
			m.s.gone = true
		}
		a.releaseReplica(m.s)
		a.presenceGone(m.s)
	case docCommandMsg:
		// The status is published before the reply so a command's effect
		// is in the snapshot when its receipt is.
		err := a.commandLocal(m.c)
		a.publishStatus()
		m.reply <- err
	case docJobResult:
		a.jobDone(m)
	case docRewriteMsg:
		if m.begin {
			// The pause counts from receipt, so an end (always posted after
			// its begin) pairs with it even if the caller stopped waiting.
			a.rewritePaused++
			a.rewriteFlushed = false
			a.rewriteWaiters = append(a.rewriteWaiters, m.reply)
		} else {
			if a.rewritePaused > 0 {
				a.rewritePaused--
			}
			if a.rewritePaused == 0 && a.meta.Pause == "" {
				a.reconciling = true
				if a.job == nil {
					a.startCheck(true)
				}
			}
			m.reply <- nil
		}
	case docStopMsg:
		a.stopping = true
		a.commitBatch()
	case docRunMsg:
		m.fn()
		close(m.done)
	}
	a.publishStatus()
}

// progress finishes rewrite pauses and the shutdown sequence.
func (a *docActor) progress() {
	if len(a.rewriteWaiters) > 0 && a.job == nil {
		// Finish pending saves before the pause (Git/buffer coordination).
		// The flush save is the only one allowed while rewritePaused > 0.
		committed := a.commitBatch()
		if !a.rewriteFlushed && committed && a.dirty() && a.writableExceptRewrite() {
			a.rewriteFlushed = true
			a.startSave()
			a.publishStatus()
			return
		}
		var err error
		if !committed || a.storeFailing || a.meta.Pause == "" && a.dirty() {
			err = failure("document_unsaved", "a document in this checkout has edits that are not durable or not saved; review it before the operation")
		}
		a.bumpGen()
		for _, w := range a.rewriteWaiters {
			w <- err
		}
		a.rewriteWaiters = nil
		a.publishStatus()
	}
	if a.stopping && a.job == nil {
		if !a.finalSaveDone && a.dirty() && !a.reconciling && a.meta.Pause == "" && a.rewritePaused == 0 && len(a.batch) == 0 {
			a.finalSaveDone = true
			a.startSave()
			return
		}
		a.exited = true
	}
}

// --- streams ---

func (a *docActor) send(s *docStream, ev protocol.DocumentEvent) {
	if s.gone {
		return
	}
	select {
	case s.out <- ev:
	default:
		// A slow reader never blocks the document: drop it; it reconnects
		// and resynchronizes.
		s.gone = true
		delete(a.streams, s)
		close(s.kill)
		a.presenceGone(s)
	}
}

func (a *docActor) broadcast(ev protocol.DocumentEvent, except *docStream) {
	for s := range a.streams {
		if s != except {
			a.send(s, ev)
		}
	}
}

func (a *docActor) stateEvent() protocol.DocumentEvent {
	st := a.status()
	return protocol.DocumentEvent{Type: protocol.DocumentEventState, Rev: a.meta.DurableRev, Update: a.d.EncodeState(), Status: &st, ServerClient: a.d.ServerClientID()}
}

func (a *docActor) join(s *docStream) error {
	if a.stopping {
		return failure("stopping", "server is shutting down")
	}
	if !a.meta.hasOpener(s.clientID) {
		return failure("not_editor", "open the document (document.open) before connecting a stream")
	}
	if s.replica != 0 {
		key := strconv.FormatUint(s.replica, 10)
		if s.replica == a.d.ServerClientID() {
			return failure("replica_conflict", "the replica ID is the server's; choose another")
		}
		for other := range a.streams {
			if other.replica == s.replica {
				return failure("replica_conflict", "another stream is using this replica ID; if a previous connection is still closing, retry shortly")
			}
		}
		switch bound := a.meta.Replicas[key]; {
		case bound == s.clientID:
		case bound != "":
			return failure("replica_conflict", "the replica ID belongs to another client; choose another")
		case len(a.meta.Replicas) >= docMaxReplicas:
			return failure("capacity", "too many replicas for this document; reopen it")
		default:
			a.meta.Replicas[key] = s.clientID
			a.persistMeta(nil)
		}
	}
	// Joiners start from durable state: the replica must not contain
	// updates that could still be lost.
	if !a.commitBatch() {
		return failure("unavailable", "document storage is failing; retry")
	}
	a.streams[s] = struct{}{}
	a.send(s, a.stateEvent())
	a.joinPresence(s)
	return nil
}

// reject refuses a request. With resync the stream is sent a fresh state
// once everything accepted is durable (coalesced: one state per stream).
//
// With resync the stream's replica can no longer be used: the stream stops
// applying updates, answers every earlier op (ack once durable), then ends
// with a normal closure and releases its replica binding. The client
// reconnects with a new replica (client/document.go).
func (a *docActor) reject(s *docStream, op, reason, message string, resync bool) {
	a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventRejected, Op: op, Rejected: &protocol.DocumentRejected{Reason: reason, Message: message, Resync: resync}})
	if resync && !s.gone && !s.ending {
		s.ending = true
		a.endStreams()
	}
}

// endStreams closes ending streams whose ops are all answered.
func (a *docActor) endStreams() {
	for s := range a.streams {
		if !s.ending || s.gone {
			continue
		}
		waiting := false
		for _, entry := range a.batch {
			waiting = waiting || entry.stream == s
		}
		if waiting {
			continue
		}
		delete(a.streams, s)
		s.gone = true
		close(s.out)
		a.releaseReplica(s)
		a.presenceGone(s)
	}
}

// releaseReplica frees a closed stream's replica binding unless another
// connected stream uses the same replica.
func (a *docActor) releaseReplica(s *docStream) {
	if s.replica == 0 {
		return
	}
	for other := range a.streams {
		if other.replica == s.replica {
			return
		}
	}
	key := strconv.FormatUint(s.replica, 10)
	if _, ok := a.meta.Replicas[key]; ok {
		delete(a.meta.Replicas, key)
		_ = a.persistMeta(nil)
	}
}

func (a *docActor) request(s *docStream, req protocol.DocumentRequest) {
	if _, ok := a.streams[s]; !ok {
		return
	}
	switch req.Type {
	case protocol.DocumentRequestSync:
		if !a.commitBatch() {
			a.reject(s, "", protocol.DocumentRejectUnavailable, "document storage is failing; retry", false)
			return
		}
		diff, err := a.d.Diff(req.StateVector)
		if err != nil {
			a.reject(s, "", protocol.DocumentRejectInvalid, "invalid state vector", false)
			return
		}
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventSync, Rev: a.meta.DurableRev, Update: diff})
	case protocol.DocumentRequestUpdate:
		a.updates++
		a.update(s, req)
	case protocol.DocumentRequestPresence:
		a.presenceRequest(s, req)
	default:
		a.reject(s, req.Op, protocol.DocumentRejectInvalid, "unknown request type", false)
	}
}

func (a *docActor) update(s *docStream, req protocol.DocumentRequest) {
	switch {
	case s.ending:
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventRejected, Op: req.Op, Rejected: &protocol.DocumentRejected{
			Reason: protocol.DocumentRejectResyncPending, Message: "an earlier update required a resync; this stream is closing", Resync: true}})
		return
	case req.Op == "" || len(req.Op) > 128:
		a.reject(s, "", protocol.DocumentRejectInvalid, "an update needs a bounded op", true)
		return
	case !a.meta.hasOpener(s.clientID):
		// Every client with the document open may edit (slice C); Gen is
		// not checked. A client that closed the document stops here.
		a.reject(s, req.Op, protocol.DocumentRejectNotEditor, "open the document (document.open) before editing it", true)
		return
	case s.holdOp != "" && req.Op != s.holdOp:
		// After unavailable, later updates (which may depend on the refused
		// one) are refused too until the client resends from holdOp.
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventRejected, Op: req.Op, Rejected: &protocol.DocumentRejected{
			Reason: protocol.DocumentRejectUnavailable, Message: "an earlier update was refused; resend unacknowledged updates in order from op " + s.holdOp, RetryAfterMs: s.holdRetry}})
		return
	case a.stopping:
		a.hold(s, req.Op, 0, "server is shutting down; resend after reconnecting")
		return
	case a.batchBytes+len(req.Update) > docMaxPendingBytes && a.storeFailing:
		// Not applied; the client's replica is still valid and resends.
		a.hold(s, req.Op, 0, "document storage is failing; resend when the status is no longer failed")
		return
	case a.batchBytes+len(req.Update) > docMaxPendingBytes:
		// The shared batch is full because editors sent a lot within one
		// commit interval; it drains at the next commit.
		a.hold(s, req.Op, 200, "the document is busy with other edits; resend after the delay")
		return
	case !a.takeTokens(s, len(req.Update)):
		// A per-stream rate limit keeps one editor from monopolizing the
		// document; the replica stays valid and resends after the delay.
		a.hold(s, req.Op, 1000, "update rate limit exceeded")
		return
	}
	s.holdOp = ""
	if a.stateBytes < 0 || a.stateBytes+len(req.Update) > a.maxStateBytes() {
		a.stateBytes = len(a.d.EncodeState())
		if a.stateBytes+len(req.Update) > a.maxStateBytes() {
			a.reject(s, req.Op, doc.ReasonTooLarge, "the document's edit history is at its size limit", true)
			return
		}
	}
	replica := s.replica
	applied, err := a.d.ApplyClient(req.Update, func(c uint64) bool { return replica != 0 && c == replica }, docTextCheck(a.meta.Format))
	if err != nil {
		reason, message := protocol.DocumentRejectInvalid, err.Error()
		var de *doc.Error
		if errors.As(err, &de) {
			reason = de.Reason
			if de.Err != nil {
				message = de.Err.Error()
			}
		} else {
			a.e.logf("document update failed", "id", a.id, "error", err)
		}
		a.reject(s, req.Op, reason, message, true)
		return
	}
	entry := docBatchEntry{client: s.clientID, stream: s, op: req.Op}
	if applied.Changed {
		entry.data = req.Update
		a.batchBytes += len(req.Update)
		// An upper estimate (updates repeat the delete set); it is
		// recomputed exactly when it reaches the bound.
		a.stateBytes += len(req.Update)
	} else if len(a.batch) == 0 && a.batchBaseline == nil {
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventAck, Op: req.Op, Rev: a.meta.DurableRev})
		return
	}
	a.batch = append(a.batch, entry)
	if a.batchDue.IsZero() {
		a.batchDue = a.clock.Now().Add(docBatchInterval)
	}
}

// commitBatch persists every accepted update (and a pending baseline change)
// in one transaction, then acknowledges and broadcasts them. It reports
// whether nothing remains uncommitted.
func (a *docActor) commitBatch() bool {
	if len(a.batch) == 0 && a.batchBaseline == nil {
		return true
	}
	now := a.clock.Now()
	next := a.meta
	var updates []storage.DocumentUpdate
	for i := range a.batch {
		if a.batch[i].data != nil {
			next.DurableRev++
			a.batch[i].rev = next.DurableRev
			updates = append(updates, storage.DocumentUpdate{Rev: next.DurableRev, Data: a.batch[i].data})
			next.LogBytes += int64(len(a.batch[i].data))
			next.LogUpdates++
		}
	}
	commit := storage.DocumentCommit{Updates: updates}
	if a.baselineUnpersisted {
		commit.Baseline = a.baseline
	}
	if bc := a.batchBaseline; bc != nil {
		commit.Baseline = []byte(bc.data)
		next.SHA, next.Token = docSHA(commit.Baseline), bc.token
		next.Pending = nil
		a.tokenAt = time.Now()
		if bc.format != nil {
			next.Format = *bc.format
		}
		if bc.saved {
			next.SavedRev = next.DurableRev
			if bc.savedRev > 0 {
				next.SavedRev = bc.savedRev
			}
		}
		if bc.clearVersions {
			commit.Versions = map[string][]byte{}
			next.Versions = false
		}
	}
	b, _ := json.Marshal(next)
	commit.Meta = b
	if err := a.store.CommitDocument(a.id, commit); err != nil {
		for i := range a.batch {
			a.batch[i].rev = 0
		}
		a.storeFailing, a.failErr = true, "document storage failed: "+err.Error()
		a.batchDue = now.Add(a.backoff())
		a.e.logf("document commit failed", "id", a.id, "error", err)
		return false
	}
	a.meta = next
	if commit.Baseline != nil {
		a.baselineUnpersisted = false
	}
	if a.storeFailing {
		a.storeFailing = false
		if !a.saveFailed {
			a.failErr, a.retryDelay = "", docRetryMin
		}
	}
	if bc := a.batchBaseline; bc != nil {
		a.baseline = []byte(bc.data)
		if bc.saved && a.saveFailed {
			// The file holds our text: an earlier write failure is over.
			a.saveFailed, a.retryDelay = false, docRetryMin
			if !a.storeFailing && !a.examineFailed {
				a.failErr = ""
			}
		}
	}
	for _, entry := range a.batch {
		rev := entry.rev
		if rev == 0 {
			rev = a.meta.DurableRev
		}
		if entry.stream != nil {
			if _, ok := a.streams[entry.stream]; ok {
				a.send(entry.stream, protocol.DocumentEvent{Type: protocol.DocumentEventAck, Op: entry.op, Rev: rev})
			}
		}
		if entry.data != nil {
			origin := "client"
			if entry.client == "" {
				origin = "server"
			}
			a.broadcast(protocol.DocumentEvent{Type: protocol.DocumentEventUpdate, Rev: entry.rev, Update: entry.data, Origin: origin, Client: entry.client}, entry.stream)
		}
	}
	if len(updates) > 0 && a.dirty() {
		if a.dirtySince.IsZero() {
			a.dirtySince = now
		}
		a.lastChange = now
	}
	if !a.dirty() {
		a.dirtySince = time.Time{}
	}
	a.batch, a.batchBytes, a.batchDue, a.batchBaseline = nil, 0, time.Time{}, nil
	a.endStreams()
	a.maybeCompact()
	return true
}

func (a *docActor) backoff() time.Duration {
	d := a.retryDelay
	a.retryDelay = min(2*a.retryDelay, docRetryMax)
	return d
}

func (a *docActor) maybeCompact() {
	if a.meta.LogUpdates < docCompactUpdates && a.meta.LogBytes < docCompactBytes {
		return
	}
	state := a.d.EncodeState()
	a.stateBytes = len(state)
	if len(state) > a.maxStateBytes() && a.meta.Pause == "" {
		a.e.logf("document history exceeds the state bound", "id", a.id, "bytes", len(state))
		a.pause(protocol.DocumentStateReadOnly, "The document's edit history grew too large; save a copy and reopen the file.", docObservation{Kind: docDiskPresent, Data: nil})
	}
	if err := a.store.CompactDocument(a.id, state, a.meta.DurableRev); err != nil {
		a.e.logf("document compaction failed", "id", a.id, "error", err)
		return
	}
	a.meta.LogBytes, a.meta.LogUpdates = 0, 0
}

// persistMeta writes metadata (and versions when given) now; failures are
// logged and retried with the next commit, since the in-memory state is
// what governs writes.
func (a *docActor) persistMeta(versions map[string][]byte) error {
	commit := storage.DocumentCommit{Meta: a.encodeMeta(), Versions: versions}
	if a.baselineUnpersisted {
		commit.Baseline = a.baseline
	}
	err := a.store.CommitDocument(a.id, commit)
	if err != nil {
		a.e.logf("document metadata not persisted", "id", a.id, "error", err)
		return err
	}
	a.baselineUnpersisted = false
	return nil
}

// --- disk jobs ---

func (a *docActor) startSave() {
	if a.job != nil {
		return
	}
	data := encodeDocFile(a.d.Text(), a.meta.Format)
	// Record the pending write before the rename (see docMeta.Pending).
	// Earlier unconfirmed writes stay acceptable as the file's content.
	sha := docSHA(data)
	prev := append([]docPending(nil), a.meta.Pending...)
	a.meta.dropPending(sha)
	a.meta.Pending = append(a.meta.Pending, docPending{SHA: sha, Rev: a.meta.DurableRev})
	if n := len(a.meta.Pending); n > docMaxPending {
		a.meta.Pending = a.meta.Pending[n-docMaxPending:]
	}
	if err := a.persistMeta(nil); err != nil {
		a.meta.Pending = prev
		a.saveFailed, a.failErr = true, "saving failed: document storage failed: "+err.Error()
		a.retryAt = a.clock.Now().Add(a.backoff())
		return
	}
	job := &docJob{kind: "save", gen: a.currentGen(), rev: a.meta.DurableRev, data: data}
	a.job = job
	root, rel, mode := a.root, a.path, a.meta.Mode
	if mode == 0 {
		mode = 0o644
	}
	expect := []string{a.meta.SHA}
	for _, p := range prev {
		expect = append(expect, p.SHA)
	}
	if a.meta.Keep != "" {
		expect = nil
		if a.meta.Keep != docKeepAbsent {
			expect = []string{a.meta.Keep}
		}
	}
	guard := a.guard(job.gen)
	after := a.e.docs.afterRename
	id := a.id
	go func() {
		// Planning jobs learn that the checkout may change under them.
		a.e.noteDocumentWrite(root)
		w, err := writeDocFile(root, rel, data, mode, expect, guard)
		if err == nil && after != nil {
			err = after(id)
		}
		a.post(docJobResult{job: job, token: w.Token, obs: w.Obs, err: err, renamed: w.Renamed})
	}()
}

// startCheck compares the file with the baseline. force reads the content
// even when the stat token is unchanged.
func (a *docActor) startCheck(force bool) {
	if a.job != nil {
		return
	}
	job := &docJob{kind: "check"}
	a.job = job
	root, rel, token := a.root, a.path, a.meta.Token
	force = force || time.Since(a.tokenAt) < docRacyWindow
	go func() {
		if !force {
			if kind, t := statDocFile(root, rel); kind == docDiskPresent && t == token {
				a.post(docJobResult{job: job, unchanged: true})
				return
			}
		}
		a.post(docJobResult{job: job, obs: observeDocFile(root, rel)})
	}()
}

func (a *docActor) jobDone(r docJobResult) {
	if a.job != r.job {
		return
	}
	a.job = nil
	defer func() {
		// A reconciliation requested while this job ran starts now.
		if r.job.kind == "save" && a.reconciling && a.job == nil && a.meta.Pause == "" && a.rewritePaused == 0 && !a.stopping {
			a.startCheck(true)
		}
	}()
	now := a.clock.Now()
	switch r.job.kind {
	case "check":
		a.pollDue = now.Add(docPollInterval)
		if r.obs.Kind != docDiskUnavailable && a.examineFailed {
			a.examineFailed = false
			if !a.storeFailing && !a.saveFailed {
				a.failErr, a.retryDelay = "", docRetryMin
			}
		}
		if r.unchanged {
			return
		}
		a.reconcile(r.obs)
	case "save":
		switch {
		case r.err == nil:
			wasKeep := a.meta.Keep != ""
			a.meta.Keep = ""
			a.baseline = r.job.data
			a.meta.SHA, a.meta.Token, a.meta.SavedRev = docSHA(r.job.data), r.token, r.job.rev
			a.meta.Pending = nil
			a.tokenAt = time.Now()
			a.saveFailed, a.retryDelay = false, docRetryMin
			if !a.storeFailing {
				a.failErr = ""
			}
			var versions map[string][]byte
			if wasKeep || a.meta.Versions {
				a.meta.Versions = false
				versions = map[string][]byte{}
			}
			if err := a.store.CommitDocument(a.id, storage.DocumentCommit{Meta: a.encodeMeta(), Baseline: r.job.data, Versions: versions}); err != nil {
				// The stored meta still names the pending write, so a
				// restart adopts the file; meanwhile every later metadata
				// write carries the baseline (never one without the other).
				a.baselineUnpersisted = true
				if versions != nil {
					a.meta.Versions = true
				}
				a.e.logf("document save not recorded", "id", a.id, "error", err)
			}
			if a.dirty() {
				a.dirtySince, a.lastChange = now, now
			} else {
				a.dirtySince = time.Time{}
			}
			a.pollDue = now.Add(docPollInterval)
			a.maybeUnload()
		case errors.Is(r.err, errStaleSave):
			// A rewrite pause invalidated this save; nothing was written.
			a.meta.dropPending(docSHA(r.job.data))
			a.meta.Keep = ""
		case errors.Is(r.err, errDocChanged):
			// Refused before the rename.
			a.meta.dropPending(docSHA(r.job.data))
			a.meta.Keep = ""
			a.reconcile(r.obs)
		default:
			if !r.renamed {
				// Nothing replaced the file.
				a.meta.dropPending(docSHA(r.job.data))
			} else if a.meta.Keep != "" {
				// The resolution's content is on disk; later saves are
				// ordinary ones against it.
				a.meta.Keep = ""
			}
			a.saveFailed = true
			a.failErr = "saving failed: " + r.err.Error()
			a.retryAt = now.Add(a.backoff())
			a.e.logf("document save failed", "id", a.id, "error", r.err)
		}
	}
}

// reconcile compares an observation of the file with the baseline and the
// document: equal content resumes, a clean external change is merged into
// the document (and saved), anything else pauses autosave with versions.
func (a *docActor) reconcile(obs docObservation) {
	if !a.commitBatch() {
		// Nothing is written while storage fails; the next poll sees the
		// changed token (or rereads while reconciling) and tries again.
		a.pollDue = a.clock.Now().Add(a.retryDelay)
		return
	}
	if obs.Kind != docDiskUnavailable {
		defer func() { a.restarted = false }()
	}
	switch obs.Kind {
	case docDiskUnavailable:
		a.examineFailed, a.failErr = true, "the file could not be examined: "+formatDocError(obs.Err)
		a.pollDue = a.clock.Now().Add(a.backoff())
		return
	case docDiskAbsent:
		a.pause(protocol.DocumentStateDeleted, "The file was deleted on disk.", obs)
		return
	case docDiskNotRegular:
		a.pause(protocol.DocumentStateDeleted, "The file was replaced by something that is not a regular file.", obs)
		return
	case docDiskLinked:
		a.pause(protocol.DocumentStateReadOnly, "The file now has other hard links; editing it could break them.", obs)
		return
	}
	if obs.TooLarge {
		a.pause(protocol.DocumentStateReadOnly, "The file on disk is now larger than 1 MiB.", obs)
		return
	}
	if obs.Mode != 0 {
		a.meta.Mode = obs.Mode
	}
	if obs.Mode&0o200 == 0 {
		a.pause(protocol.DocumentStateReadOnly, "The file is no longer writable by its owner.", obs)
		return
	}
	ours := a.d.Text()
	diskSHA := docSHA(obs.Data)
	// The file holding exactly our own text needs no format checks.
	if bytes.Equal(obs.Data, encodeDocFile(ours, a.meta.Format)) {
		a.batchBaseline = &docBaselineChange{data: string(obs.Data), token: obs.Token, saved: true}
		a.commitBatch()
		a.reconciling = false
		return
	}
	// A recorded save that was not confirmed: the file holding its bytes is
	// that save; adopt it as the baseline and continue from there.
	if p, ok := a.meta.pendingFor(diskSHA); ok {
		a.batchBaseline = &docBaselineChange{data: string(obs.Data), token: obs.Token, saved: true, savedRev: p.Rev}
		if !a.commitBatch() {
			return
		}
		a.reconciling = false
		if a.dirty() {
			now := a.clock.Now()
			a.dirtySince, a.lastChange = now, now
		}
		return
	}
	if a.restarted && len(a.meta.Pending) > 0 && diskSHA != a.meta.SHA {
		a.pause(protocol.DocumentStatePausedConflict, "The server stopped during a save and the file changed since; review the versions.", obs)
		return
	}
	diskText, format, style, reason := decodeDocFile(obs.Data, &a.meta.Format)
	if reason != "" {
		a.pause(protocol.DocumentStateReadOnly, reason, obs)
		return
	}
	if format.BOM != a.meta.Format.BOM || style != "none" && format.Newline != a.meta.Format.Newline {
		a.pause(protocol.DocumentStatePausedConflict, "The file's line endings or byte-order mark changed on disk.", obs)
		return
	}
	base, _, _, _ := decodeDocFile(a.baseline, &a.meta.Format)
	switch {
	case diskText == ours:
		a.batchBaseline = &docBaselineChange{data: string(obs.Data), token: obs.Token, saved: true}
		a.commitBatch()
		a.reconciling = false
		return
	case diskSHA == a.meta.SHA:
		// Same content under a new token (touched, or rewritten identically).
		if a.meta.Token != obs.Token {
			a.meta.Token, a.tokenAt = obs.Token, time.Now()
		}
		a.reconciling = false
		if a.dirty() && a.dirtySince.IsZero() {
			now := a.clock.Now()
			a.dirtySince, a.lastChange = now, now
		}
		return
	}
	edits, merged, clean, err := doc.Merge3(base, ours, diskText)
	switch {
	case errors.Is(err, doc.ErrTooDifferent):
		a.pause(protocol.DocumentStatePausedConflict, "The file changed on disk too much to merge automatically.", obs)
		return
	case err != nil || !clean:
		a.pause(protocol.DocumentStatePausedConflict, "The file changed on disk where the document also changed.", obs)
		return
	case encodedDocSize(merged, a.meta.Format) > protocol.DocumentMaxBytes:
		a.pause(protocol.DocumentStatePausedConflict, "Merging the change on disk would exceed 1 MiB.", obs)
		return
	}
	a.applyServerEdits(edits, &docBaselineChange{data: string(obs.Data), token: obs.Token, saved: merged == diskText})
	a.reconciling = false
}

// applyServerEdits applies server-origin edits and commits them with a
// baseline change.
func (a *docActor) applyServerEdits(edits []doc.Edit, bc *docBaselineChange) {
	update, err := a.d.Replace(edits)
	if err != nil {
		a.e.logf("server document edit failed", "id", a.id, "error", err)
		if update == nil {
			a.pause(protocol.DocumentStatePausedConflict, "The change on disk could not be applied to the document.", docObservation{Kind: docDiskPresent, Data: []byte(bc.data)})
			return
		}
	}
	if update != nil {
		a.batch = append(a.batch, docBatchEntry{data: update})
		a.batchBytes += len(update)
		a.stateBytes += len(update)
	}
	a.batchBaseline = bc
	if a.commitBatch() && !bc.saved {
		// Write the merged text promptly.
		now := a.clock.Now()
		a.dirtySince, a.lastChange = now.Add(-docSaveMax), now.Add(-docSaveIdle)
	}
}

// pause stops autosave and retains the base, document and disk versions.
func (a *docActor) pause(state, reason string, obs docObservation) {
	a.meta.Pause, a.meta.Reason = state, reason
	a.meta.Keep = ""
	// Whatever the file holds now, it is not the document's text.
	a.meta.SavedRev = -1
	a.reconciling, a.saveFailed = false, false
	if !a.storeFailing {
		a.failErr = ""
	}
	versions := map[string][]byte{
		"base":     a.baseline,
		"document": encodeDocFile(a.d.Text(), a.meta.Format),
	}
	diskState := "present"
	switch obs.Kind {
	case docDiskAbsent:
		diskState = "absent"
	case docDiskNotRegular:
		diskState = "not_regular"
	}
	versions["disk_state"] = []byte(diskState)
	versions["disk_id"] = []byte(docDiskID(obs))
	if obs.Data != nil {
		versions["disk"] = obs.Data
	}
	a.meta.Versions = true
	a.bumpGen()
	a.persistMeta(versions)
}

// docDiskID identifies an observed disk version for resolutions: the
// client must name the version it reviewed.
func docDiskID(obs docObservation) string {
	switch {
	case obs.Kind == docDiskAbsent:
		return "absent"
	case obs.Kind == docDiskNotRegular:
		return "not_regular:" + obs.Token
	case obs.TooLarge:
		return "large:" + obs.Token
	case obs.Data != nil:
		return "sha256:" + docSHA(obs.Data)
	}
	return "unavailable"
}

// --- commands ---

func (a *docActor) commandLocal(c protocol.Command) error {
	if a.stopping {
		return failure("stopping", "server is shutting down")
	}
	changed := false
	switch c.Kind {
	case protocol.DocumentKindOpen:
		if !a.meta.hasOpener(c.ClientID) {
			a.meta.Openers = append(a.meta.Openers, c.ClientID)
			changed = true
		}
	case protocol.DocumentKindClose:
		if a.meta.hasOpener(c.ClientID) {
			a.meta.removeOpener(c.ClientID)
			changed = true
		}
		if a.meta.Editor == c.ClientID {
			a.meta.Editor = ""
			changed = true
		}
		a.closePresence(c.ClientID)
		// Replica bindings of a client that closed and has no stream end:
		// its next open must use a fresh replica.
		connected := false
		for s := range a.streams {
			connected = connected || s.clientID == c.ClientID
		}
		if !connected {
			for k, v := range a.meta.Replicas {
				if v == c.ClientID {
					delete(a.meta.Replicas, k)
					changed = true
				}
			}
		}
	case protocol.DocumentKindEdit, protocol.DocumentKindTakeEdit:
		if !a.meta.hasOpener(c.ClientID) {
			return failure("not_open", "open the document before editing it")
		}
		// Compatibility (slice C): every opener may edit, so both commands
		// succeed. Editor records the most recent claimant for older clients;
		// EditGen no longer changes after the first claim and is not checked.
		if a.meta.Editor == c.ClientID {
			return nil
		}
		prev, prevGen := a.meta.Editor, a.meta.EditGen
		a.meta.Editor = c.ClientID
		if a.meta.EditGen == 0 {
			a.meta.EditGen = 1
		}
		if err := a.persistMeta(nil); err != nil {
			a.meta.Editor, a.meta.EditGen = prev, prevGen
			return failure("storage", "the editor change could not be stored")
		}
		return nil
	case protocol.DocumentKindResolve:
		return a.resolve(c)
	default:
		return failure("unsupported_command", "unsupported document command")
	}
	if changed {
		a.persistMeta(nil)
	}
	a.maybeUnload()
	return nil
}

func (a *docActor) resolve(c protocol.Command) error {
	// Everything accepted so far must be durable and part of the revision
	// the client reviewed.
	if !a.commitBatch() {
		return failure("storage", "document storage is failing; retry")
	}
	if c.Revision != a.meta.DurableRev {
		return failure("stale_document", "the document changed; review it again before resolving")
	}
	if c.Text == protocol.DocumentResolveDiscard {
		if a.meta.Pause == "" && a.dirty() {
			return failure("document_unsaved", "the document has unsaved edits and is not paused; it cannot be discarded")
		}
		if err := a.unload(protocol.DocumentResolveDiscard); err != nil {
			return failure("storage", "the document could not be removed from storage")
		}
		return nil
	}
	if a.meta.Pause == "" {
		return failure("not_paused", "the document is not paused")
	}
	// A bounded local read; resolutions are rare and explicit.
	obs := observeDocFile(a.root, a.path)
	if obs.Kind == docDiskUnavailable {
		return failure("unavailable", "the file could not be examined")
	}
	if id := docDiskID(obs); id != c.DocumentDisk {
		// The file is not the version the client reviewed: record the new
		// one and stay paused.
		a.pause(a.meta.Pause, a.meta.Reason, obs)
		return failure("stale_document", "the file on disk changed since it was reviewed; review the versions again")
	}
	switch c.Text {
	case protocol.DocumentResolveKeepDocument:
		var expect string
		switch {
		case obs.Kind == docDiskAbsent:
		case obs.Kind == docDiskPresent && !obs.TooLarge:
			expect = docSHA(obs.Data)
			if obs.Mode != 0 {
				a.meta.Mode = obs.Mode
			}
		case obs.Kind == docDiskPresent:
			return failure("document_read_only", "the file on disk is larger than 1 MiB; it will not be overwritten")
		case obs.Kind == docDiskUnavailable:
			return failure("unavailable", "the file could not be examined")
		default:
			return failure("document_read_only", "the path is not a regular, singly linked file; it will not be overwritten")
		}
		if obs.Mode&0o200 == 0 && obs.Kind == docDiskPresent {
			return failure("document_read_only", "the file is not writable by its owner; it will not be overwritten")
		}
		// Versions stay until the save of the document is confirmed.
		a.meta.Keep = expect
		if expect == "" {
			a.meta.Keep = docKeepAbsent
		}
		a.meta.Pause, a.meta.Reason, a.meta.SavedRev = "", "", -1
		a.persistMeta(nil)
		now := a.clock.Now()
		a.dirtySince, a.lastChange = now.Add(-docSaveMax), now.Add(-docSaveIdle)
		return nil
	case protocol.DocumentResolveUseDisk:
		if obs.Kind != docDiskPresent || obs.TooLarge {
			return failure("document_read_only", "the file on disk cannot be used: it is missing, not a regular file or larger than 1 MiB")
		}
		text, format, style, reason := decodeDocFile(obs.Data, nil)
		if reason != "" {
			return failure("document_read_only", reason)
		}
		if style == "none" {
			format.Newline = a.meta.Format.Newline
		}
		if obs.Mode != 0 {
			a.meta.Mode = obs.Mode
		}
		a.meta.Pause, a.meta.Reason = "", ""
		a.applyServerEdits(doc.DiffEdits(a.d.Text(), text), &docBaselineChange{data: string(obs.Data), token: obs.Token, saved: true, clearVersions: true, format: &format})
		if a.storeFailing {
			return failure("storage", "document storage is failing; the resolution is retried")
		}
		return nil
	}
	return failure("invalid", "unknown resolution")
}

// maybeUnload drops a saved document that no client has open.
func (a *docActor) maybeUnload() {
	if len(a.meta.Openers) == 0 && a.meta.Pause == "" && !a.dirty() && len(a.batch) == 0 && a.batchBaseline == nil &&
		a.job == nil && !a.reconciling && !a.storeFailing && !a.saveFailed && !a.examineFailed && a.rewritePaused == 0 && len(a.rewriteWaiters) == 0 {
		_ = a.unload("closed")
	}
}

// unload deletes the document's storage and ends the actor.
func (a *docActor) unload(reason string) error {
	if err := a.store.DeleteDocument(a.id); err != nil {
		a.e.logf("document could not be removed from storage", "id", a.id, "error", err)
		return err
	}
	a.bumpGen()
	for s := range a.streams {
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventClosed, Reason: reason})
		if !s.gone {
			s.gone = true
			close(s.out)
		}
	}
	a.streams = nil
	a.e.unregisterDocument(a)
	a.exited = true
	return nil
}

func (a *docActor) status() protocol.DocumentStatus {
	st := protocol.DocumentStatus{
		ID: a.id, Checkout: a.root, Path: a.path, DurableRev: a.meta.DurableRev, SavedRev: a.meta.SavedRev,
		Editor: a.meta.Editor, EditGen: a.meta.EditGen, Openers: append([]string(nil), a.meta.Openers...),
		Newline: a.meta.Format.Newline, BOM: a.meta.Format.BOM, Versions: a.meta.Versions, Collaborative: true,
	}
	switch {
	case a.meta.Pause != "":
		st.State, st.Reason = a.meta.Pause, a.meta.Reason
	case a.reconciling:
		st.State = protocol.DocumentStateReconciling
	case a.storeFailing || a.saveFailed || a.examineFailed:
		st.State, st.Error = protocol.DocumentStateFailed, a.failErr
		st.Reason = "Saving failed; edits are kept and saving is retried."
		if a.storeFailing {
			st.Reason = "Document storage failed; edits are not yet durable and are retried."
		}
	case a.job != nil && a.job.kind == "save":
		st.State = protocol.DocumentStateSaving
	case a.dirty() || len(a.batch) > 0:
		st.State = protocol.DocumentStatePending
	default:
		st.State = protocol.DocumentStateSaved
	}
	return st
}

func (a *docActor) publishStatus() {
	if a.exited {
		return
	}
	st := a.status()
	if a.lastStatus != nil && statusEqual(*a.lastStatus, st) {
		return
	}
	a.lastStatus = &st
	a.e.setDocumentStatus(st, false)
	for s := range a.streams {
		copied := st
		a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventStatus, Status: &copied})
	}
}

func statusEqual(x, y protocol.DocumentStatus) bool {
	bx, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(bx) == string(by)
}

// --- Git coordination ---

// beginDocumentRewrite prepares every document in checkout root (exact
// match with the document's canonical checkout) for an application-managed
// Git operation that rewrites files: pending saves are finished, then
// autosave pauses and no earlier save can rename afterwards. It fails with
// document_unsaved when a document's edits are not durable or not saved
// (the operation should stop for review), or with ctx's error.
//
// release ends the pause of exactly the documents this call paused, once;
// it is never nil and must be called whether or not err is nil. A document
// counts as paused from the moment its actor received the request, so a
// cancelled begin still pairs with its release, and one rewrite's release
// never ends another's pause.
func (e *engine) beginDocumentRewrite(ctx context.Context, root string) (release func(), err error) {
	var paused []*docActor
	var once sync.Once
	release = func() {
		once.Do(func() {
			for _, a := range paused {
				reply := make(chan error, 1)
				if a.post(docRewriteMsg{reply: reply}) {
					select {
					case <-reply:
					case <-a.quit:
					}
				}
			}
		})
	}
	var replies []chan error
	var quits []chan struct{}
	for _, a := range e.documentsIn(root) {
		reply := make(chan error, 1)
		select {
		case a.inbox <- docRewriteMsg{begin: true, reply: reply}:
			paused = append(paused, a)
			replies = append(replies, reply)
			quits = append(quits, a.quit)
		case <-a.quit:
		case <-ctx.Done():
			return release, ctx.Err()
		}
	}
	var errs []error
	for i, reply := range replies {
		select {
		case err := <-reply:
			errs = append(errs, err)
		case <-quits[i]:
		case <-ctx.Done():
			return release, ctx.Err()
		}
	}
	return release, errors.Join(errs...)
}

func (e *engine) documentsIn(root string) []*docActor {
	e.docs.mu.Lock()
	defer e.docs.mu.Unlock()
	var out []*docActor
	for _, a := range e.docs.byID {
		if a.root == root {
			out = append(out, a)
		}
	}
	return out
}

func (a *docActor) maxStateBytes() int {
	if a.e.docs.maxState > 0 {
		return a.e.docs.maxState
	}
	return docMaxStateBytes
}

// --- presence ---

// Per-stream update rate limit: docRateBytes per second with a burst of
// docRateBurst. Presence is limited by coalescing, not by this bucket.
const (
	docRateBytes     = 4 << 20
	docRateBurst     = 16 << 20
	docPresenceEvery = protocol.DocumentPresenceInterval * time.Millisecond
	docPresenceTTL   = protocol.DocumentPresenceTimeout * time.Second
)

func (a *docActor) takeTokens(s *docStream, n int) bool {
	if !a.takeRequest(s) {
		return false
	}
	now := a.clock.Now()
	if s.tokensAt.IsZero() {
		s.tokens, s.tokensAt = docRateBurst, now
	}
	s.tokens = min(docRateBurst, s.tokens+now.Sub(s.tokensAt).Seconds()*docRateBytes)
	s.tokensAt = now
	if float64(n) > s.tokens {
		return false
	}
	s.tokens -= float64(n)
	return true
}

// presenceRequest records a stream's cursor; it is forwarded coalesced.
func (a *docActor) presenceRequest(s *docStream, req protocol.DocumentRequest) {
	if !a.meta.hasOpener(s.clientID) {
		a.clearPresence(s)
		a.reject(s, "", protocol.DocumentRejectNotEditor, "open the document before publishing presence", false)
		return
	}
	if !a.takeRequest(s) {
		// Presence beyond the request rate is dropped (latest wins anyway).
		return
	}
	if len(req.Anchor) == 0 && len(req.Head) == 0 {
		a.clearPresence(s)
		return
	}
	if len(req.Anchor) > protocol.DocumentMaxPresenceBytes || len(req.Head) > protocol.DocumentMaxPresenceBytes ||
		!doc.ValidPosition(req.Anchor) || !doc.ValidPosition(req.Head) {
		a.reject(s, "", protocol.DocumentRejectInvalid, "presence needs anchor and head RelativePosition encodings of at most 256 bytes", false)
		return
	}
	s.presence = &protocol.DocumentPeer{Peer: s.peer, Client: s.clientID, Replica: s.replica, Color: s.color,
		Anchor: append([]byte(nil), req.Anchor...), Head: append([]byte(nil), req.Head...)}
	s.presAt, s.presDirty = a.clock.Now(), true
}

func (a *docActor) clearPresence(s *docStream) {
	if s.presence == nil {
		return
	}
	s.presence, s.presDirty = nil, false
	removed := protocol.DocumentPeer{Peer: s.peer, Color: s.color, Removed: true}
	for other := range a.streams {
		if other != s {
			other.deliverPresence(removed)
		}
	}
}

// presenceGone removes a closed stream's cursor from the others.
func (a *docActor) presenceGone(s *docStream) {
	a.clearPresence(s)
}

// joinPresence assigns the stream's color and sends it the live cursors.
func (a *docActor) joinPresence(s *docStream) {
	used := map[int]bool{}
	for other := range a.streams {
		if other != s {
			used[other.color] = true
		}
	}
	for s.color = 0; used[s.color] && s.color < 7; s.color++ {
	}
	for other := range a.streams {
		if other != s && other.presence != nil {
			s.deliverPresence(*other.presence)
		}
	}
}

// presenceDeadline is the next coalesced flush or expiry, or zero.
func (a *docActor) presenceDeadline() time.Time {
	var next time.Time
	for s := range a.streams {
		if s.presence == nil {
			continue
		}
		t := s.presAt.Add(docPresenceTTL)
		if s.presDirty && !a.hasPending(s) {
			// A cursor is forwarded only after the peer's own accepted
			// updates are committed and broadcast, so it never points at an
			// edit the receivers have not seen.
			t = s.presSent.Add(docPresenceEvery)
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}

// flushPresence forwards changed cursors and expires stale ones.
func (a *docActor) flushPresence(now time.Time) {
	for s := range a.streams {
		switch {
		case s.presence == nil:
		case !now.Before(s.presAt.Add(docPresenceTTL)):
			a.clearPresence(s)
		case s.presDirty && !a.hasPending(s) && !now.Before(s.presSent.Add(docPresenceEvery)):
			s.presDirty, s.presSent = false, now
			for other := range a.streams {
				if other != s {
					other.deliverPresence(*s.presence)
				}
			}
		}
	}
}

// hold refuses an update as unavailable without a resync: this op and every
// later one on the stream are refused until the client resends from op.
func (a *docActor) hold(s *docStream, op string, retryMs int, message string) {
	if s.holdOp == "" {
		s.holdOp = op
	}
	s.holdRetry = retryMs
	a.send(s, protocol.DocumentEvent{Type: protocol.DocumentEventRejected, Op: op, Rejected: &protocol.DocumentRejected{
		Reason: protocol.DocumentRejectUnavailable, Message: message + "; resend unacknowledged updates in order from op " + s.holdOp, RetryAfterMs: retryMs}})
}

// Per-stream request rate (updates and presence together): docRequestRate
// per second with a burst of docRequestBurst.
const (
	docRequestRate  = 200
	docRequestBurst = 400
)

func (a *docActor) takeRequest(s *docStream) bool {
	now := a.clock.Now()
	if s.reqAt.IsZero() {
		s.reqTokens, s.reqAt = docRequestBurst, now
	}
	s.reqTokens = min(docRequestBurst, s.reqTokens+now.Sub(s.reqAt).Seconds()*docRequestRate)
	s.reqAt = now
	if s.reqTokens < 1 {
		return false
	}
	s.reqTokens--
	return true
}

// hasPending reports accepted updates of s that are not yet committed.
func (a *docActor) hasPending(s *docStream) bool {
	for _, e := range a.batch {
		if e.stream == s {
			return true
		}
	}
	return false
}

// closePresence removes the presence of a client that closed the document.
func (a *docActor) closePresence(client string) {
	for s := range a.streams {
		if s.clientID == client {
			a.clearPresence(s)
		}
	}
}
