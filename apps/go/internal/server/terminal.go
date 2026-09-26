package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/term"
)

// Embedded terminal lifecycle (ADR 0019). Durable state (the Terminal records
// in the snapshot) changes only through journaled commands and the engine's
// own lifecycle commits; screen output, input and resize stay on the
// ephemeral stream (terminal_stream.go) and are never persisted.
//
// State machine of a record:
//
//	running ──terminal.close──▶ closing ──reaped──▶ ended (closed)
//	   │                          └─not confirmed in closeTimeout─▶ close_uncertain ──reaped──▶ ended (closed)
//	   ├──shell exits by itself──▶ ended (exited)
//	   └──server stop──▶ closing ──reaped──▶ ended (server_stopped)
//	                        └─not confirmed in terminalStopTimeout─▶ ended (server_stopped_unconfirmed; shutdown reports failure)
//	any live record found at server start ──▶ ended (server_restarted)
//
// Thread delete and project removal drop the records with their thread and
// close the sessions in the background (stream reason thread_deleted).
const (
	maxLiveTerminals  = 64
	maxEndedTerminals = 64
	// terminalCloseTimeout bounds waiting for a confirmed exit before the
	// record reports close_uncertain; the package escalates to SIGKILL after 2s.
	terminalCloseTimeout = 5 * time.Second
	// terminalStopTimeout bounds closing every session at server stop.
	terminalStopTimeout = 5 * time.Second
	// terminalFrameInterval caps screen frames per session (~30/s).
	terminalFrameInterval = time.Second / 30
	// terminalMetaInterval coalesces Cols/Rows/Title updates into the snapshot
	// so a resize drag does not become a revision storm.
	terminalMetaInterval = 250 * time.Millisecond
	defaultTerminalCols  = 80
	defaultTerminalRows  = 24
)

// terminalState is the engine's registry of live and recently ended sessions.
// It is guarded by engine.mu.
type terminalState struct {
	sessions map[string]*termSession
	// opening reserves capacity and command identity while a shell starts
	// without the engine lock; a retry with the same command ID waits on it.
	opening map[string]chan struct{}
	// shell overrides $SHELL and env the child environment (tests); home
	// locates the directory used for fixture checkouts.
	shell string
	env   []string
	home  func() (string, error)
	// streams counts stream connections per terminal (the "" key holds the
	// total); see terminal_stream.go.
	streams map[string]int
	// closed is set before the final shutdown save; later lifecycle commits
	// are dropped because storage is closing.
	closed bool
	// stop runs stopTerminals once; later calls return its result.
	stop    sync.Once
	stopErr error
}

func (e *engine) terminalsLocked() *terminalState {
	if e.terminals.sessions == nil {
		e.terminals.sessions = map[string]*termSession{}
		e.terminals.opening = map[string]chan struct{}{}
		e.terminals.streams = map[string]int{}
	}
	return &e.terminals
}

// termSession owns one term.Session and fans its screen out to streams.
// Lock order: engine.mu before termSession.mu.
type termSession struct {
	e  *engine
	id string

	mu         sync.Mutex
	sess       *term.Session // nil once ended; releases grid and scrollback
	controller string
	gen        int64
	running    bool   // accepts input and resize
	reason     string // end reason requested by close
	frame      []byte // latest encoded screen event
	frameSeq   uint64
	cols, rows int
	title      string
	metaTimer  bool
	ended      *protocol.TerminalEnded
	watchers   map[chan struct{}]struct{}

	closeOnce sync.Once
	finished  chan struct{} // closed after the ended record is committed
}

func terminalByID(s *protocol.Snapshot, id string) *protocol.Terminal {
	for i := range s.Terminals {
		if s.Terminals[i].ID == id {
			return &s.Terminals[i]
		}
	}
	return nil
}

func terminalLive(state string) bool {
	return state == protocol.TerminalStateRunning || state == protocol.TerminalStateClosing || state == protocol.TerminalStateCloseUncertain
}

// terminalDir chooses the shell's working directory. Fixture checkouts have
// no directory, so their terminals start in the server user's home and the
// record shows that; a real checkout must be an existing absolute directory.
func (e *engine) terminalDir(checkout string, home func() (string, error)) (string, error) {
	if checkout == "" || strings.HasPrefix(checkout, "fixture://") {
		dir, err := home()
		if err != nil || !filepath.IsAbs(dir) {
			return "", failure("terminal_unavailable", "no home directory is available for a fixture checkout terminal")
		}
		return dir, nil
	}
	if !filepath.IsAbs(checkout) {
		return "", failure("terminal_unavailable", "the thread checkout is not a local directory")
	}
	if fi, err := os.Stat(checkout); err != nil || !fi.IsDir() {
		return "", failure("terminal_unavailable", "the thread checkout directory is unavailable")
	}
	return checkout, nil
}

// openTerminal handles terminal.open. The shell starts without the engine
// lock; the command identity is reserved first so a concurrent retry waits
// and then finds the receipt, and a retry never starts a second process.
func (e *engine) openTerminal(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	e.mu.Lock()
	ts := e.terminalsLocked()
	for {
		wait, busy := ts.opening[c.ID]
		if !busy {
			break
		}
		e.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return protocol.Receipt{}, failure("cancelled", "the request ended before the terminal opened; retry with the same command ID")
		}
		e.mu.Lock()
	}
	if r, err := e.store.Lookup(c); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	} else if r != nil {
		e.mu.Unlock()
		return *r, nil
	}
	if err := e.admitTerminalLocked(c); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	}
	checkout := threadByID(&e.snap, c.ThreadID).Checkout
	shell, env, home := ts.shell, ts.env, ts.home
	if home == nil {
		home = os.UserHomeDir
	}
	reserved := make(chan struct{})
	ts.opening[c.ID] = reserved
	e.mu.Unlock()

	// Zero (or omitted) dimensions use the default size.
	cols, rows := defaultTerminalCols, defaultTerminalRows
	if c.TerminalSize != nil && c.TerminalSize.Cols > 0 {
		cols = c.TerminalSize.Cols
	}
	if c.TerminalSize != nil && c.TerminalSize.Rows > 0 {
		rows = c.TerminalSize.Rows
	}
	var sess *term.Session
	dir, err := e.terminalDir(checkout, home)
	if err == nil {
		sess, err = term.Start(ctx, term.Config{Shell: shell, Dir: dir, Env: env, Cols: cols, Rows: rows, Scrollback: term.DefaultScrollback})
		if err != nil && ctx.Err() != nil {
			err = failure("cancelled", "the request ended before the terminal opened; retry with the same command ID")
		} else if err != nil {
			e.logf("terminal start failed", "error", err)
			err = failure("terminal_unavailable", "the shell could not be started: "+err.Error())
		}
	}

	e.mu.Lock()
	delete(ts.opening, c.ID)
	close(reserved)
	if err == nil {
		// The thread may have been deleted, or the server stopped, meanwhile.
		err = e.admitTerminalLocked(c)
	}
	var r protocol.Receipt
	if err == nil {
		r, err = e.commitTerminalOpenLocked(c, sess, dir, shell)
	}
	e.mu.Unlock()
	if err != nil && sess != nil {
		// Never leave an unrecorded process behind; the lock is released so
		// a stubborn child cannot stall other commands.
		closeCtx, cancel := context.WithTimeout(context.Background(), terminalCloseTimeout)
		_ = sess.Close(closeCtx)
		cancel()
	}
	return r, err
}

func (e *engine) commitTerminalOpenLocked(c protocol.Command, sess *term.Session, dir, shell string) (protocol.Receipt, error) {
	ts := e.terminalsLocked()
	scr := sess.Snapshot()
	record := protocol.Terminal{
		ID: "terminal-" + c.ID, ThreadID: c.ThreadID, State: protocol.TerminalStateRunning,
		Controller: c.ClientID, ControlGen: 1, Revision: 1,
		Dir: dir, Shell: resolvedShell(shell), Cols: scr.Cols, Rows: scr.Rows,
	}
	next := clone(e.snap)
	next.Terminals = append(next.Terminals, record)
	pruneEndedTerminals(&next)
	next.Revision++
	r := protocol.Receipt{ID: c.ID, State: "accepted", Revision: next.Revision, TargetID: record.ID}
	if err := e.store.Save(next, &c, &r); err != nil {
		return protocol.Receipt{}, err
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	s := &termSession{
		e: e, id: record.ID, sess: sess, controller: record.Controller, gen: record.ControlGen, running: true,
		cols: scr.Cols, rows: scr.Rows, watchers: map[chan struct{}]struct{}{}, finished: make(chan struct{}),
	}
	ts.sessions[record.ID] = s
	e.publish()
	e.afterCommit(c, record.ID)
	e.afterTerminalCommitLocked(c, record.ID)
	s.publishFrame(sess)
	go s.run(sess)
	return r, nil
}

// admitTerminalLocked checks what terminal.open needs under the lock. The
// caller's own reservation is already released when it rechecks.
func (e *engine) admitTerminalLocked(c protocol.Command) error {
	if e.stopping || e.terminals.closed {
		return failure("stopping", "server is shutting down")
	}
	if c.ClientID == "" || len(c.ClientID) > 128 {
		return failure("invalid", "terminal.open requires a bounded ClientID")
	}
	if c.TerminalSize != nil && (c.TerminalSize.Cols < 0 || c.TerminalSize.Rows < 0) {
		return failure("invalid", "terminal size must not be negative")
	}
	t := threadByID(&e.snap, c.ThreadID)
	if t == nil {
		return failure("not_found", "thread does not exist")
	}
	if _, err := threadCheckout(&e.snap, t); err != nil {
		return err
	}
	live := len(e.terminals.opening)
	for _, s := range e.terminals.sessions {
		select {
		case <-s.finished:
		default:
			live++
		}
	}
	if live >= maxLiveTerminals {
		return failure("capacity", "at most 64 terminals can run at once; close one first")
	}
	return nil
}

func resolvedShell(override string) string {
	if override != "" {
		return override
	}
	if sh := os.Getenv("SHELL"); filepath.IsAbs(sh) {
		if fi, err := os.Stat(sh); err == nil && fi.Mode().IsRegular() && fi.Mode()&0111 != 0 {
			return sh
		}
	}
	return "/bin/sh"
}

// pruneEndedTerminals keeps the newest maxEndedTerminals ended records.
func pruneEndedTerminals(s *protocol.Snapshot) {
	ended := 0
	for _, t := range s.Terminals {
		if !terminalLive(t.State) {
			ended++
		}
	}
	if ended <= maxEndedTerminals {
		return
	}
	drop := ended - maxEndedTerminals
	kept := s.Terminals[:0]
	for _, t := range s.Terminals {
		if drop > 0 && !terminalLive(t.State) {
			drop--
			continue
		}
		kept = append(kept, t)
	}
	s.Terminals = kept
}

// afterTerminalCommitLocked applies a committed command's effects to live
// sessions: close, control transfer, and closing sessions whose records a
// thread delete or project removal dropped.
func (e *engine) afterTerminalCommitLocked(c protocol.Command, target string) {
	ts := e.terminalsLocked()
	switch c.Kind {
	case "terminal.close":
		if s := ts.sessions[target]; s != nil {
			s.close(protocol.TerminalEndClosed)
		} else if rec := terminalByID(&e.snap, target); rec != nil && rec.State != protocol.TerminalStateEnded {
			// A record without a session (for example a legacy fixture) has
			// no process to wait for.
			rec.State, rec.EndReason = protocol.TerminalStateEnded, protocol.TerminalEndClosed
			rec.Revision++
			e.snap.Revision++
			e.flushLocked()
		}
	case "terminal.take-control":
		if s, rec := ts.sessions[target], terminalByID(&e.snap, target); s != nil && rec != nil {
			s.setControl(rec.Controller, rec.ControlGen)
		}
	}
	for id, s := range ts.sessions {
		if terminalByID(&e.snap, id) != nil {
			continue
		}
		select {
		case <-s.finished:
			delete(ts.sessions, id)
		default:
			s.close(protocol.TerminalEndThreadDeleted)
		}
	}
}

// applyTerminalCommand is the pure snapshot transition for terminal.close and
// terminal.take-control; t is the command's thread.
func applyTerminalCommand(s *protocol.Snapshot, t *protocol.Thread, c protocol.Command) (string, error) {
	rec := terminalByID(s, c.TargetID)
	if rec == nil || rec.ThreadID != t.ID {
		return "", failure("not_found", "terminal missing")
	}
	switch c.Kind {
	case "terminal.close":
		if rec.State == protocol.TerminalStateRunning {
			rec.State = protocol.TerminalStateClosing
			rec.Revision++
		}
		// closing, close_uncertain and ended: the close is already accepted.
		return rec.ID, nil
	case "terminal.take-control":
		if c.ClientID == "" || len(c.ClientID) > 128 {
			return "", failure("invalid", "taking control requires a bounded ClientID")
		}
		if rec.State != protocol.TerminalStateRunning {
			return "", failure("terminal_ended", "the terminal is no longer running")
		}
		if rec.Controller != c.ClientID {
			rec.Controller = c.ClientID
			rec.ControlGen++
			rec.Revision++
		}
		return rec.ID, nil
	}
	return "", failure("unsupported_command", "unsupported terminal command")
}

// endLoadedTerminals marks every live record from storage ended: no PTY
// survives a server restart.
func endLoadedTerminals(s *protocol.Snapshot) {
	for i := range s.Terminals {
		t := &s.Terminals[i]
		if t.State != protocol.TerminalStateEnded {
			t.State, t.EndReason = protocol.TerminalStateEnded, protocol.TerminalEndServerRestarted
			t.Revision++
		}
	}
	pruneEndedTerminals(s)
}

// endStoppedTerminals marks records still live at the final shutdown save.
// stopTerminals has already recorded every confirmed exit, so these are
// sessions whose exit was not confirmed; the record says so.
func endStoppedTerminals(s *protocol.Snapshot) {
	for i := range s.Terminals {
		t := &s.Terminals[i]
		if t.State != protocol.TerminalStateEnded {
			t.State, t.EndReason = protocol.TerminalStateEnded, protocol.TerminalEndServerStoppedUnconfirmed
			t.Error = "the server stopped before the shell's exit was confirmed; the process may still be running"
			t.Revision++
		}
	}
}

// stopTerminals closes every session at server stop and waits, bounded, for
// their ended records. Records move to closing first (take-control is
// refused from then on). It then refuses later lifecycle commits and reports
// sessions whose exit was not confirmed, so the shutdown outcome is honest.
func (e *engine) stopTerminals() error {
	e.terminals.stop.Do(func() { e.terminals.stopErr = e.stopTerminalsOnce() })
	return e.terminals.stopErr
}

func (e *engine) stopTerminalsOnce() error {
	e.mu.Lock()
	ts := e.terminalsLocked()
	sessions := make([]*termSession, 0, len(ts.sessions))
	for _, s := range ts.sessions {
		sessions = append(sessions, s)
	}
	changed := false
	for i := range e.snap.Terminals {
		if t := &e.snap.Terminals[i]; !ts.closed && t.State == protocol.TerminalStateRunning {
			t.State = protocol.TerminalStateClosing
			t.Revision++
			changed = true
		}
	}
	if changed {
		e.snap.Revision++
		e.flushLocked()
	}
	e.mu.Unlock()
	deadline := time.NewTimer(terminalStopTimeout)
	defer deadline.Stop()
	for _, s := range sessions {
		s.close(protocol.TerminalEndServerStopped)
	}
wait:
	for _, s := range sessions {
		select {
		case <-s.finished:
		case <-deadline.C:
			break wait
		}
	}
	// The records are the single source of truth: an exit committed before
	// closed is confirmed; anything still live is reported unconfirmed here
	// and recorded so by endStoppedTerminals.
	e.mu.Lock()
	ts.closed = true
	unconfirmed := 0
	for _, t := range e.snap.Terminals {
		if t.State != protocol.TerminalStateEnded {
			unconfirmed++
		}
	}
	e.mu.Unlock()
	if unconfirmed > 0 {
		e.logf("terminal sessions did not confirm exit before shutdown", "count", unconfirmed)
		return fmt.Errorf("%d terminal session(s) did not confirm exit within %v", unconfirmed, terminalStopTimeout)
	}
	return nil
}

// run publishes coalesced screen frames until the shell ends, then commits
// the ended record.
func (s *termSession) run(sess *term.Session) {
	var last time.Time
	for {
		select {
		case <-sess.Done():
			s.publishFrame(sess)
			st, _ := sess.ExitStatus()
			s.e.finishTerminal(s, st)
			return
		case <-sess.Changed():
		}
		if wait := terminalFrameInterval - time.Since(last); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-sess.Done():
				t.Stop()
			}
		}
		s.publishFrame(sess)
		last = time.Now()
	}
}

// publishFrame encodes the current screen once for every stream and signals
// them; slow streams skip to the latest frame.
func (s *termSession) publishFrame(sess *term.Session) {
	scr := sess.Snapshot()
	s.mu.Lock()
	if s.frame != nil && scr.Seq == s.frameSeq {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	b := encodeFrame(scr)
	s.mu.Lock()
	s.frame, s.frameSeq = b, scr.Seq
	metaChanged := scr.Cols != s.cols || scr.Rows != s.rows || scr.Title != s.title
	s.cols, s.rows, s.title = scr.Cols, scr.Rows, scr.Title
	schedule := metaChanged && !s.metaTimer
	if schedule {
		s.metaTimer = true
	}
	s.mu.Unlock()
	s.broadcast()
	if schedule {
		time.AfterFunc(terminalMetaInterval, s.flushMeta)
	}
}

// flushMeta copies the coalesced size and title into the snapshot at the
// streamed-output persistence rate.
func (s *termSession) flushMeta() {
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	s.mu.Lock()
	s.metaTimer = false
	cols, rows, title := s.cols, s.rows, s.title
	s.mu.Unlock()
	rec := terminalByID(&e.snap, s.id)
	if e.terminals.closed || rec == nil || (rec.Cols == cols && rec.Rows == rows && rec.Title == title) {
		return
	}
	rec.Cols, rec.Rows, rec.Title = cols, rows, title
	rec.Revision++
	e.snap.Revision++
	e.dirty = true
	e.maybeFlushLocked()
}

// finishTerminal commits the ended record once the process is reaped.
func (e *engine) finishTerminal(s *termSession, st term.ExitStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	exit := &protocol.TerminalExit{Code: st.Code, Signal: st.Signal, Killed: st.Killed,
		Descendants: st.Descendants, DescendantsKilled: st.DescendantsKilled,
		DescendantsRemaining: st.DescendantsRemaining, DescendantsUnknown: st.DescendantsUnknown}
	s.mu.Lock()
	reason := s.reason
	if reason == "" {
		reason = protocol.TerminalEndExited
		if e.stopping {
			reason = protocol.TerminalEndServerStopped
		}
	}
	s.ended = &protocol.TerminalEnded{Exit: exit, Reason: reason}
	s.running, s.sess = false, nil
	s.mu.Unlock()
	if rec := terminalByID(&e.snap, s.id); rec != nil && !e.terminals.closed {
		rec.State, rec.EndReason, rec.Exit, rec.Error = protocol.TerminalStateEnded, reason, exit, ""
		rec.Revision++
		pruneEndedTerminals(&e.snap)
		e.snap.Revision++
		e.flushLocked()
	}
	close(s.finished)
	s.broadcast()
	// Ended sessions keep only their final frame, and only while their record
	// is retained.
	ts := e.terminalsLocked()
	for id, other := range ts.sessions {
		select {
		case <-other.finished:
			if terminalByID(&e.snap, id) == nil {
				delete(ts.sessions, id)
			}
		default:
		}
	}
}

// close requests the session's end with reason (the first reason wins) and
// stops accepting input immediately.
func (s *termSession) close(reason string) {
	s.mu.Lock()
	if s.reason == "" && s.ended == nil {
		s.reason = reason
	}
	s.running = false
	sess := s.sess
	s.mu.Unlock()
	s.broadcast()
	if sess == nil {
		return
	}
	s.closeOnce.Do(func() { go s.awaitClose(sess) })
}

// awaitClose reports close_uncertain when exit is not confirmed in time; the
// record still becomes ended when run observes the reaped process.
func (s *termSession) awaitClose(sess *term.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), terminalCloseTimeout)
	defer cancel()
	if err := sess.Close(ctx); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return
	}
	e := s.e
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := terminalByID(&e.snap, s.id)
	if e.terminals.closed || rec == nil || rec.State != protocol.TerminalStateClosing {
		return
	}
	rec.State, rec.Error = protocol.TerminalStateCloseUncertain, "exit not yet confirmed; still waiting for the process to end"
	rec.Revision++
	e.snap.Revision++
	e.flushLocked()
}

func (s *termSession) setControl(controller string, gen int64) {
	s.mu.Lock()
	s.controller, s.gen = controller, gen
	s.mu.Unlock()
	s.broadcast()
}

func (s *termSession) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *termSession) unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	delete(s.watchers, ch)
	s.mu.Unlock()
}

// broadcast wakes every stream without blocking.
func (s *termSession) broadcast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// authorizeLocked returns the rejection reason for input or resize.
func (s *termSession) authorizeLocked(clientID string, gen int64) string {
	switch {
	case !s.running || s.sess == nil:
		return protocol.TerminalRejectEnded
	case clientID != s.controller:
		return protocol.TerminalRejectNotController
	case gen != s.gen:
		return protocol.TerminalRejectStaleGeneration
	}
	return ""
}

// input writes controller keystrokes to the PTY. The bytes are never logged
// or persisted.
func (s *termSession) input(clientID string, gen int64, data []byte) (string, int) {
	if len(data) > term.MaxInput {
		return protocol.TerminalRejectTooLarge, 0
	}
	return s.controlled(clientID, gen, func(sess *term.Session) error {
		if len(data) == 0 {
			return nil
		}
		return sess.Write(data)
	})
}

func (s *termSession) resize(clientID string, gen int64, cols, rows int) (string, int) {
	if cols <= 0 || rows <= 0 {
		return protocol.TerminalRejectInvalid, 0
	}
	return s.controlled(clientID, gen, func(sess *term.Session) error { return sess.Resize(cols, rows) })
}

// scrollback reads history lines; any observer may read.
func (s *termSession) scrollback(from, n int) protocol.TerminalEvent {
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil {
		return rejected(protocol.TerminalRejectEnded, protocol.TerminalRequestScrollback)
	}
	if n <= 0 || n > protocol.TerminalMaxScrollback {
		return rejected(protocol.TerminalRejectInvalid, protocol.TerminalRequestScrollback)
	}
	newest := from < 0
	total := sess.ScrollbackLen()
	if newest {
		from = max(0, total-n)
	}
	from = min(from, total)
	sb := encodeScrollback(sess.ScrollbackLines(from, n), from, total, newest)
	return protocol.TerminalEvent{Type: protocol.TerminalEventScrollback, Scrollback: &sb}
}

// view returns what a stream should send next.
func (s *termSession) view() (frame []byte, seq uint64, control protocol.TerminalControl, ended *protocol.TerminalEnded) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame, s.frameSeq, protocol.TerminalControl{Controller: s.controller, Gen: s.gen}, s.ended
}

func rejected(reason, request string) protocol.TerminalEvent {
	return protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: reason, For: request}}
}
