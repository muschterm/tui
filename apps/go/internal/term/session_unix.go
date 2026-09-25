//go:build unix

package term

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	readChunk     = 4 << 10         // PTY bytes parsed per emulator lock
	replyQueue    = 64              // pending emulator replies; excess is dropped
	hangupGrace   = 2 * time.Second // SIGHUP to SIGKILL escalation
	killGrace     = time.Second     // waiting for killed processes to vanish
	sessionPoll   = 50 * time.Millisecond
	drainGrace    = 200 * time.Millisecond
	heldFlush     = 20 * time.Millisecond // bound on holding a cluster split across reads
	writeTimeout  = 5 * time.Second
	fallbackShell = "/bin/sh"
)

// Session is one PTY-backed child process and its emulator. All methods are
// safe for concurrent use.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File

	mu       sync.Mutex // guards emu and the fields below
	emu      *vt.Emulator
	seq      uint64
	title    string
	cursorOn bool
	modes    inputModes // child-selected input modes; see keys.go
	exit     ExitStatus
	exited   bool
	reaped   bool
	killed   bool
	// closing is set by Close before the exit is finalized; finalizing is
	// set by wait when the shell exited by itself first. Exactly one wins.
	closing    bool
	finalizing bool
	closeStat  ExitStatus // descendant outcome recorded by terminate
	termDone   chan struct{}
	limiter    clusterLimiter // guarded by mu (used only by readLoop)
	wmu        sync.Mutex     // serializes PTY writes (input and replies)
	changed    chan struct{}
	replies    chan []byte
	done       chan struct{}
}

// Start launches the shell on a new PTY in its own session and process group.
// ctx bounds only the start itself; the session outlives it until Close or
// child exit.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shell, err := resolveShell(cfg.Shell, os.Getenv("SHELL"))
	if err != nil {
		return nil, err
	}
	if err := checkDir(cfg.Dir); err != nil {
		return nil, err
	}
	cols, rows := clamp(cfg.Cols, MinCols, MaxCols), clamp(cfg.Rows, MinRows, MaxRows)
	sb := cfg.Scrollback
	if sb <= 0 {
		sb = DefaultScrollback
	}
	sb = min(sb, MaxScrollback)
	env := cfg.Env
	if env == nil {
		env = os.Environ()
	}
	args := cfg.args
	if args == nil {
		args = []string{"-i"}
	}

	cmd := exec.Command(shell, args...)
	cmd.Dir = cfg.Dir
	cmd.Env = ChildEnv(env)
	// StartWithSize sets Setsid and Setctty: the child leads a new session
	// and process group with the PTY as its controlling terminal.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("term: start %s: %w", shell, err)
	}
	if ptmx, err = pollable(ptmx); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("term: start %s: %w", shell, err)
	}

	s := &Session{
		cmd:      cmd,
		ptmx:     ptmx,
		emu:      vt.NewEmulator(cols, rows),
		cursorOn: true,
		changed:  make(chan struct{}, 1),
		replies:  make(chan []byte, replyQueue),
		done:     make(chan struct{}),
		termDone: make(chan struct{}),
	}
	s.emu.SetScrollbackSize(sb)
	s.emu.SetCallbacks(vt.Callbacks{
		Title:            func(t string) { s.title = SanitizeTitle(t) },
		CursorVisibility: func(v bool) { s.cursorOn = v },
		EnableMode:       func(m ansi.Mode) { s.modes.set(m, true) },
		DisableMode:      func(m ansi.Mode) { s.modes.set(m, false) },
	})

	readerDone := make(chan struct{})
	drainDone := make(chan struct{})
	writerDone := make(chan struct{})
	go s.readLoop(readerDone)
	go s.drainReplies(drainDone)
	go s.writeReplies(writerDone)
	go s.wait(readerDone, drainDone, writerDone)
	return s, nil
}

// pollable replaces the PTY master with a non-blocking duplicate registered
// with the runtime poller. creack/pty leaves the master in blocking mode, where
// a write into a full input queue (a child not reading) blocks a thread
// forever and Close cannot interrupt reads or writes. Never call Fd on the
// result: it would switch the descriptor back to blocking mode.
func pollable(f *os.File) (*os.File, error) {
	defer f.Close()
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

func setSize(f *os.File, cols, rows int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) {
		ierr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	}); err != nil {
		return err
	}
	return ierr
}

func resolveShell(override, env string) (string, error) {
	if override != "" {
		if !isExecutable(override) {
			return "", fmt.Errorf("%w: %q", ErrInvalidShell, override)
		}
		return override, nil
	}
	if isExecutable(env) {
		return env, nil
	}
	return fallbackShell, nil
}

func isExecutable(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && unix.Access(p, unix.X_OK) == nil
}

func checkDir(d string) error {
	if !filepath.IsAbs(d) {
		return fmt.Errorf("%w: %q", ErrInvalidDir, d)
	}
	fi, err := os.Stat(d)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", ErrInvalidDir, d)
	}
	return nil
}

func (s *Session) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// readLoop is the only PTY reader. It parses output under mu; emulator query
// replies go to an io.Pipe drained by drainReplies, so parsing never waits on
// the child.
func (s *Session) readLoop(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, readChunk)
	deadline := false
	release := func() {
		s.mu.Lock()
		s.limiter.flush(s.emu)
		s.seq++
		s.mu.Unlock()
		s.notify()
	}
	for {
		n, err := s.ptmx.Read(buf)
		held := false
		if n > 0 {
			s.mu.Lock()
			// A full read means more output is waiting: the limiter may hold
			// a trailing cluster so it is not split at the read boundary.
			s.limiter.write(s.emu, buf[:n], n == len(buf))
			held = s.limiter.held()
			s.seq++
			s.mu.Unlock()
			s.notify()
		}
		if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
			// No more output arrived: show the held cluster now.
			release()
			held = false
		} else if err != nil {
			release()
			return
		}
		if held {
			_ = s.ptmx.SetReadDeadline(time.Now().Add(heldFlush))
		} else if deadline {
			_ = s.ptmx.SetReadDeadline(time.Time{})
		}
		deadline = held
	}
}

// drainReplies copies emulator replies to a bounded queue without ever
// blocking; when the child is not reading its input, replies are dropped.
func (s *Session) drainReplies(done chan<- struct{}) {
	defer close(done)
	defer close(s.replies)
	buf := make([]byte, 1024)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 {
			select {
			case s.replies <- append([]byte(nil), buf[:n]...):
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) writeReplies(done chan<- struct{}) {
	defer close(done)
	for p := range s.replies {
		_, _ = s.writePTY(p) // failures are dropped: replies are advisory
	}
}

func (s *Session) writePTY(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.ptmx.SetWriteDeadline(time.Now().Add(writeTimeout))
	return s.ptmx.Write(p)
}

// wait reaps the child, then tears down the PTY and goroutines before Done.
func (s *Session) wait(readerDone, drainDone, writerDone <-chan struct{}) {
	_ = s.cmd.Wait() // the status is read from ProcessState
	s.mu.Lock()
	s.reaped = true
	s.mu.Unlock()
	st := exitStatus(s.cmd.ProcessState)
	// Let the reader collect final output; descendants still holding the
	// PTY slave must not keep the session alive.
	select {
	case <-readerDone:
	case <-time.After(drainGrace):
	}
	_ = s.ptmx.Close()
	<-readerDone
	// Closing the reply pipe ends drainReplies, which closes s.replies.
	if pw, ok := s.emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.CloseWithError(io.EOF)
	}
	<-drainDone
	<-writerDone
	s.mu.Lock()
	closing := s.closing
	s.finalizing = !closing
	s.mu.Unlock()
	if closing {
		// Close owns the session's other processes; the exit is confirmed
		// only once they are gone or accounted for.
		<-s.termDone
	} else {
		// A shell that exited by itself leaves its jobs alone (as nohup
		// would); they are only counted.
		n, ok := sessionMembers(s.cmd.Process.Pid)
		st.Descendants, st.DescendantsRemaining, st.DescendantsUnknown = len(n), len(n), !ok
	}
	s.mu.Lock()
	st.Killed = s.killed
	if closing {
		st.Descendants, st.DescendantsKilled = s.closeStat.Descendants, s.closeStat.DescendantsKilled
		st.DescendantsRemaining, st.DescendantsUnknown = s.closeStat.DescendantsRemaining, s.closeStat.DescendantsUnknown
	}
	s.exit, s.exited = st, true
	s.seq++
	s.mu.Unlock()
	close(s.done)
	s.notify()
}

func exitStatus(ps *os.ProcessState) ExitStatus {
	if ps == nil {
		return ExitStatus{Code: -1}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ExitStatus{Code: -1, Signal: unix.SignalName(ws.Signal())}
	}
	return ExitStatus{Code: ps.ExitCode()}
}

// Write sends input bytes to the child. Each call is limited to MaxInput.
func (s *Session) Write(p []byte) error {
	if len(p) > MaxInput {
		return ErrInputTooLarge
	}
	return s.write(p)
}

func (s *Session) write(p []byte) error {
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	n, err := s.writePTY(p)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrClosed):
		return ErrClosed
	case errors.Is(err, os.ErrDeadlineExceeded):
		return &BusyError{Written: n, Total: len(p)}
	}
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	return fmt.Errorf("term: write: %w", err)
}

// Resize changes the PTY and emulator size together; values are clamped.
func (s *Session) Resize(cols, rows int) error {
	cols, rows = clamp(cols, MinCols, MaxCols), clamp(rows, MinRows, MaxRows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return ErrClosed
	}
	if err := setSize(s.ptmx, cols, rows); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return ErrClosed
		}
		return fmt.Errorf("term: resize: %w", err)
	}
	s.emu.Resize(cols, rows)
	s.seq++
	s.notify()
	return nil
}

// Snapshot copies the visible grid.
func (s *Session) Snapshot() Screen {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.emu.Width(), s.emu.Height()
	pos := s.emu.CursorPosition()
	scr := Screen{
		Seq: s.seq, Cols: w, Rows: h, Title: s.title, AltScreen: s.emu.IsAltScreen(),
		Cursor: Cursor{X: pos.X, Y: pos.Y, Visible: s.cursorOn},
		Lines:  make([][]Cell, h),
	}
	for y := range h {
		scr.Lines[y] = convertLine(w, func(x int) *uv.Cell { return s.emu.CellAt(x, y) })
	}
	return scr
}

// ScrollbackLen reports retained main-screen history lines.
func (s *Session) ScrollbackLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emu.ScrollbackLen()
}

// ScrollbackLines copies up to n history lines starting at from (0 is the
// oldest). Rows omit trailing blank cells, so their lengths vary.
func (s *Session) ScrollbackLines(from, n int) [][]Cell {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.emu.Scrollback()
	total := sb.Len()
	from = clamp(from, 0, total)
	n = clamp(n, 0, total-from)
	out := make([][]Cell, n)
	for i := range n {
		line := sb.Line(from + i)
		out[i] = convertLine(len(line), func(x int) *uv.Cell { return &line[x] })
	}
	return out
}

// Changed delivers a coalesced notification after output, resize or exit.
func (s *Session) Changed() <-chan struct{} { return s.changed }

// Done closes once the child has been reaped and all session goroutines
// have stopped.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitStatus reports the exit status; ok is false until Done is closed.
func (s *Session) ExitStatus() (ExitStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit, s.exited
}

// Close ends the session: it hangs up the shell's process group and every
// other process group in its session (background jobs), escalates to SIGKILL
// after a grace period, and returns once the shell is reaped, the session's
// processes are gone or accounted for in ExitStatus, and goroutines stopped.
// Returning nil means confirmed exit; a ctx error means close was accepted
// (signals sent) but exit was not yet confirmed. Close is idempotent. After
// the shell has exited by itself Close only waits for Done.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closing && !s.finalizing {
		s.closing = true
		go s.terminate()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// terminate signals the shell's session and records the outcome for wait.
// The session ID is the shell's PID (Setsid); while any member lives the
// kernel does not reuse that number, so enumerating it after the shell is
// reaped still finds only this session's processes.
func (s *Session) terminate() {
	defer close(s.termDone)
	sid := s.cmd.Process.Pid
	var st ExitStatus
	s.signalGroup(sid, unix.SIGHUP, false)
	members, ok := sessionMembers(sid)
	st.DescendantsUnknown = !ok
	st.Descendants = len(members)
	signalMembers(members, unix.SIGHUP)
	if !s.awaitSession(sid, hangupGrace, &st) {
		s.signalGroup(sid, unix.SIGKILL, true)
		members, _ = sessionMembers(sid)
		signalMembers(members, unix.SIGKILL)
		st.DescendantsKilled = len(members) > 0
		s.awaitSession(sid, killGrace, &st)
	}
	members, _ = sessionMembers(sid)
	st.DescendantsRemaining = len(members)
	s.mu.Lock()
	s.closeStat = st
	s.mu.Unlock()
}

// awaitSession waits until the shell is reaped and no other session member
// remains, or d elapses.
func (s *Session) awaitSession(sid int, d time.Duration, st *ExitStatus) bool {
	deadline := time.Now().Add(d)
	for {
		s.mu.Lock()
		reaped := s.reaped
		s.mu.Unlock()
		members, _ := sessionMembers(sid)
		st.Descendants = max(st.Descendants, len(members))
		if reaped && len(members) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(sessionPoll)
	}
}

func signalMembers(members map[int]int, sig unix.Signal) {
	groups := map[int]bool{}
	for pid, pgid := range members {
		if pgid > 0 && !groups[pgid] {
			groups[pgid] = true
			_ = unix.Kill(-pgid, sig)
		}
		_ = unix.Kill(pid, sig)
	}
}

// signalGroup signals the child's process group unless the leader has been
// reaped (its id could then be reused). It reports whether it signalled.
func (s *Session) signalGroup(pgid int, sig unix.Signal, kill bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reaped {
		return false
	}
	if kill {
		s.killed = true
	}
	_ = unix.Kill(-pgid, sig)
	return true
}
