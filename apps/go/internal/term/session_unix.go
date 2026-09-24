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
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	readChunk     = 4 << 10         // PTY bytes parsed per emulator lock
	replyQueue    = 64              // pending emulator replies; excess is dropped
	hangupGrace   = 2 * time.Second // SIGHUP to SIGKILL escalation
	drainGrace    = 200 * time.Millisecond
	writeTimeout  = 5 * time.Second
	fallbackShell = "/bin/sh"
)

// Session is one PTY-backed child process and its emulator. All methods are
// safe for concurrent use.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File

	mu        sync.Mutex // guards emu and the fields below
	emu       *vt.Emulator
	seq       uint64
	title     string
	cursorOn  bool
	exit      ExitStatus
	exited    bool
	reaped    bool
	killed    bool
	wmu       sync.Mutex // serializes PTY writes (input and replies)
	changed   chan struct{}
	replies   chan []byte
	done      chan struct{}
	closeOnce sync.Once
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
	}
	s.emu.SetScrollbackSize(sb)
	s.emu.SetCallbacks(vt.Callbacks{
		Title:            func(t string) { s.title = SanitizeTitle(t) },
		CursorVisibility: func(v bool) { s.cursorOn = v },
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
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			_, _ = s.emu.Write(buf[:n])
			s.seq++
			s.mu.Unlock()
			s.notify()
		}
		if err != nil {
			return
		}
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
		_ = s.writePTY(p) // failures are dropped: replies are advisory
	}
}

func (s *Session) writePTY(p []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.ptmx.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := s.ptmx.Write(p)
	return err
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
	st.Killed = s.killed
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
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	if err := s.writePTY(p); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return ErrClosed
		}
		return fmt.Errorf("term: write: %w", err)
	}
	return nil
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

// Close hangs up the child's process group, escalates to SIGKILL after a
// grace period, and returns once the child is reaped and goroutines stopped.
// Returning nil means confirmed exit; a ctx error means close was accepted
// (signals sent) but exit was not yet confirmed. Close is idempotent.
func (s *Session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		go s.terminate()
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) terminate() {
	pgid := s.cmd.Process.Pid // Setsid: the child leads its own group
	if !s.signalGroup(pgid, unix.SIGHUP, false) {
		return
	}
	t := time.NewTimer(hangupGrace)
	defer t.Stop()
	select {
	case <-s.done:
		return
	case <-t.C:
	}
	s.signalGroup(pgid, unix.SIGKILL, true)
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
