//go:build unix

package term

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
}

// testEnv isolates test shells from the developer's home: a temporary HOME,
// no history file and no startup files named by ENV or BASH_ENV.
func testEnv(t *testing.T) []string {
	t.Helper()
	return []string{"HOME=" + t.TempDir(), "HISTFILE=/dev/null", "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
}

// startScript runs script under /bin/sh -c and registers cleanup.
func startScript(t *testing.T, script string, mod func(*Config)) *Session {
	t.Helper()
	requireSh(t)
	cfg := Config{Shell: "/bin/sh", Dir: t.TempDir(), Env: testEnv(t), Cols: 40, Rows: 10, args: []string{"-c", script}}
	if mod != nil {
		mod(&cfg)
	}
	s, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

func rowText(row []Cell) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteString(c.Text)
	}
	return strings.TrimRight(b.String(), " ")
}

func screenText(scr Screen) string {
	lines := make([]string, len(scr.Lines))
	for i, r := range scr.Lines {
		lines[i] = rowText(r)
	}
	return strings.Join(lines, "\n")
}

// waitFor polls the snapshot until cond holds.
func waitFor(t *testing.T, s *Session, what string, cond func(Screen) bool) Screen {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		scr := s.Snapshot()
		if cond(scr) {
			return scr
		}
		select {
		case <-s.Changed():
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; screen:\n%s", what, screenText(scr))
		}
	}
}

func waitDone(t *testing.T, s *Session) ExitStatus {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session did not finish")
	}
	st, ok := s.ExitStatus()
	if !ok {
		t.Fatal("exit status unavailable after Done")
	}
	return st
}

func contains(sub string) func(Screen) bool {
	return func(scr Screen) bool { return strings.Contains(screenText(scr), sub) }
}

func TestPrintfAppearsInSnapshot(t *testing.T) {
	s := startScript(t, `printf 'hello world'; sleep 5`, nil)
	scr := waitFor(t, s, "hello", contains("hello world"))
	if scr.Cols != 40 || scr.Rows != 10 || len(scr.Lines) != 10 || len(scr.Lines[0]) != 40 {
		t.Fatalf("geometry %dx%d lines %d", scr.Cols, scr.Rows, len(scr.Lines))
	}
	if scr.Cursor.X != 11 || scr.Cursor.Y != 0 || !scr.Cursor.Visible {
		t.Fatalf("cursor %+v", scr.Cursor)
	}
}

func TestCursorPosition(t *testing.T) {
	s := startScript(t, `printf '\033[4;7Hx'; sleep 5`, nil)
	scr := waitFor(t, s, "x", contains("x"))
	if scr.Cursor.X != 7 || scr.Cursor.Y != 3 {
		t.Fatalf("cursor %+v", scr.Cursor)
	}
	if scr.Lines[3][6].Text != "x" {
		t.Fatalf("cell = %+v", scr.Lines[3][6])
	}
}

func TestWideCharacters(t *testing.T) {
	s := startScript(t, `printf 'a界b😀c'; sleep 5`, nil)
	scr := waitFor(t, s, "c", contains("c"))
	row := scr.Lines[0]
	want := []struct {
		text  string
		width int
	}{{"a", 1}, {"界", 2}, {"", 0}, {"b", 1}, {"😀", 2}, {"", 0}, {"c", 1}}
	for i, w := range want {
		if row[i].Text != w.text || row[i].Width != w.width {
			t.Fatalf("cell %d = %q/%d, want %q/%d", i, row[i].Text, row[i].Width, w.text, w.width)
		}
	}
	if scr.Cursor.X != 7 {
		t.Fatalf("cursor x = %d", scr.Cursor.X)
	}
}

func TestColorsAndAttributes(t *testing.T) {
	s := startScript(t, `printf '\033[1;31mA\033[0;3;4;7;9;2;5;38;5;200;48;2;1;2;3mB\033[0mC'; sleep 5`, nil)
	scr := waitFor(t, s, "C", contains("ABC"))
	a, b, c := scr.Lines[0][0], scr.Lines[0][1], scr.Lines[0][2]
	if a.Attrs != AttrBold || a.FG != (Color{Kind: ColorIndexed, Index: 1}) || a.BG.Kind != ColorDefault {
		t.Fatalf("A = %+v", a)
	}
	wantAttrs := AttrItalic | AttrUnderline | AttrReverse | AttrStrike | AttrDim | AttrBlink
	if b.Attrs != wantAttrs || b.FG != (Color{Kind: ColorIndexed, Index: 200}) || b.BG != (Color{Kind: ColorRGB, R: 1, G: 2, B: 3}) {
		t.Fatalf("B = %+v", b)
	}
	if c.Attrs != 0 || c.FG.Kind != ColorDefault {
		t.Fatalf("C = %+v", c)
	}
}

func TestResizeReachesChild(t *testing.T) {
	s := startScript(t, `stty size; read x; stty size; sleep 5`, nil)
	waitFor(t, s, "initial size", contains("10 40"))
	if err := s.Resize(60, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	scr := waitFor(t, s, "new size", contains("20 60"))
	if scr.Cols != 60 || scr.Rows != 20 || len(scr.Lines) != 20 || len(scr.Lines[0]) != 60 {
		t.Fatalf("geometry %dx%d", scr.Cols, scr.Rows)
	}
	if err := s.Resize(0, 100000); err != nil {
		t.Fatal(err)
	}
	if scr := s.Snapshot(); scr.Cols != MinCols || scr.Rows != MaxRows {
		t.Fatalf("clamped geometry %dx%d", scr.Cols, scr.Rows)
	}
}

func TestAltScreenRestoresMain(t *testing.T) {
	s := startScript(t, `printf main; printf '\033[?1049h'; printf ALT; read x; printf '\033[?1049l'; sleep 5`, nil)
	scr := waitFor(t, s, "alt", contains("ALT"))
	if !scr.AltScreen || strings.Contains(screenText(scr), "main") {
		t.Fatalf("alt screen: %v\n%s", scr.AltScreen, screenText(scr))
	}
	_ = s.Write([]byte("\n"))
	scr = waitFor(t, s, "main", func(scr Screen) bool { return !scr.AltScreen })
	if txt := screenText(scr); !strings.Contains(txt, "main") || strings.Contains(txt, "ALT") {
		t.Fatalf("main screen not restored:\n%s", txt)
	}
}

func TestScrollbackBounded(t *testing.T) {
	s := startScript(t, `i=1; while [ $i -le 200 ]; do echo "line $i"; i=$((i+1)); done; sleep 5`,
		func(c *Config) { c.Scrollback = 50 })
	waitFor(t, s, "last line", contains("line 200"))
	if n := s.ScrollbackLen(); n != 50 {
		t.Fatalf("scrollback len = %d", n)
	}
	lines := s.ScrollbackLines(0, 1000)
	if len(lines) != 50 {
		t.Fatalf("lines = %d", len(lines))
	}
	// 200 lines with the last visible rows on screen: the history ends right
	// above the first visible line and is ordered oldest first.
	scr := s.Snapshot()
	first := rowText(scr.Lines[0])
	var firstN int
	if _, err := fmt.Sscanf(first, "line %d", &firstN); err != nil {
		t.Fatalf("first visible %q: %v", first, err)
	}
	for i, l := range lines {
		if got, want := rowText(l), "line "+strconv.Itoa(firstN-50+i); got != want {
			t.Fatalf("history[%d] = %q, want %q", i, got, want)
		}
	}
	if got := s.ScrollbackLines(45, 10); len(got) != 5 {
		t.Fatalf("tail slice = %d", len(got))
	}
	if got := s.ScrollbackLines(-3, 2); len(got) != 2 || rowText(got[0]) != rowText(lines[0]) {
		t.Fatalf("negative from not clamped")
	}
}

func TestTitleSanitized(t *testing.T) {
	s := startScript(t, `printf '\033]2;my\001ti\177tle\033\\'; printf done; sleep 5`, nil)
	scr := waitFor(t, s, "title", contains("done"))
	if scr.Title != "mytitle" {
		t.Fatalf("title = %q", scr.Title)
	}
	if got := SanitizeTitle(strings.Repeat("é", MaxTitle+10) + "\x9b"); len([]rune(got)) != MaxTitle {
		t.Fatalf("title length %d", len([]rune(got)))
	}
}

func TestDeviceStatusReportAnswered(t *testing.T) {
	// The child asks for the cursor position and reads the reply from its
	// own terminal input.
	s := startScript(t, `stty -echo -icanon; printf '\033[3;5H\033[6n'; r=$(dd bs=1 count=6 2>/dev/null); printf '\033[1;1Hgot:%s' "$(printf %s "$r" | tr -d '\033')"; sleep 5`, nil)
	waitFor(t, s, "reply", contains("got:[3;5R"))
}

func TestChildExitClosesDone(t *testing.T) {
	s := startScript(t, `exit 7`, nil)
	st := waitDone(t, s)
	if st.Code != 7 || st.Signal != "" || st.Killed {
		t.Fatalf("status %+v", st)
	}
	if err := s.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after exit: %v", err)
	}
	if err := s.Resize(10, 10); !errors.Is(err, ErrClosed) {
		t.Fatalf("resize after exit: %v", err)
	}
}

func TestCloseHangsUp(t *testing.T) {
	s := startScript(t, `sleep 100`, nil)
	start := time.Now()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ExitStatus()
	if st.Signal != "SIGHUP" || st.Killed || time.Since(start) > hangupGrace {
		t.Fatalf("status %+v after %v", st, time.Since(start))
	}
	if err := s.Close(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestCloseKillsChildIgnoringHangup(t *testing.T) {
	s := startScript(t, `trap '' HUP; printf ready; sleep 100`, nil)
	waitFor(t, s, "ready", contains("ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := s.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("accepted close returned %v before escalation", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ExitStatus()
	if !st.Killed || st.Signal != "SIGKILL" || st.Code != -1 {
		t.Fatalf("status %+v", st)
	}
}

func TestFloodDoesNotBlockClose(t *testing.T) {
	s := startScript(t, `yes 'flood flood flood flood'`, func(c *Config) { c.Scrollback = 500 })
	waitFor(t, s, "flood", contains("flood"))
	time.Sleep(300 * time.Millisecond)
	if n := s.ScrollbackLen(); n > 500 {
		t.Fatalf("scrollback %d exceeds bound", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.ScrollbackLen(); n > 500 {
		t.Fatalf("scrollback %d exceeds bound", n)
	}
}

func TestChildEnvironment(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH"), "TERM=xterm-kitty", "TMUX=/tmp/x", "KITTY_WINDOW_ID=3", "KEEP=1"}
	s := startScript(t, `printf '%s|%s|%s|%s|%s' "$TERM" "$COLORTERM" "${TMUX-none}" "${KITTY_WINDOW_ID-none}" "$KEEP"; sleep 5`,
		func(c *Config) { c.Env = env; c.Cols = 80 })
	waitFor(t, s, "env", contains("xterm-256color|truecolor|none|none|1"))
}

func TestWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	s := startScript(t, `pwd; sleep 5`, func(c *Config) { c.Dir = dir; c.Cols = 200 })
	real, _ := filepath.EvalSymlinks(dir)
	waitFor(t, s, "pwd", func(scr Screen) bool {
		txt := screenText(scr)
		return strings.Contains(txt, dir) || strings.Contains(txt, real)
	})
}

func TestStartValidation(t *testing.T) {
	requireSh(t)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]Config{
		"relative dir":   {Shell: "/bin/sh", Dir: "rel"},
		"missing dir":    {Shell: "/bin/sh", Dir: filepath.Join(t.TempDir(), "missing")},
		"file dir":       {Shell: "/bin/sh", Dir: file},
		"relative shell": {Shell: "sh", Dir: t.TempDir()},
		"non-exec shell": {Shell: file, Dir: t.TempDir()},
	} {
		s, err := Start(context.Background(), cfg)
		if err == nil {
			_ = s.Close(context.Background())
			t.Fatalf("%s: expected error", name)
		}
		if !errors.Is(err, ErrInvalidDir) && !errors.Is(err, ErrInvalidShell) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestResolveShell(t *testing.T) {
	requireSh(t)
	for env, want := range map[string]string{"": "/bin/sh", "sh": "/bin/sh", "/nonexistent/zsh": "/bin/sh", "/bin/sh": "/bin/sh", "/": "/bin/sh"} {
		if got, err := resolveShell("", env); err != nil || got != want {
			t.Fatalf("SHELL=%q: %q %v", env, got, err)
		}
	}
}

func TestInputTooLarge(t *testing.T) {
	s := startScript(t, `sleep 5`, nil)
	if err := s.Write(make([]byte, MaxInput+1)); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoGoroutineLeaks(t *testing.T) {
	requireSh(t)
	before := runtime.NumGoroutine()
	for range 3 {
		s, err := Start(context.Background(), Config{Shell: "/bin/sh", Dir: t.TempDir(), Env: testEnv(t), args: []string{"-c", "printf '\033[6n'; sleep 100"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		s2, err := Start(context.Background(), Config{Shell: "/bin/sh", Dir: t.TempDir(), Env: testEnv(t), args: []string{"-c", "exit 0"}})
		if err != nil {
			t.Fatal(err)
		}
		<-s2.Done()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines %d > %d\n%s", runtime.NumGoroutine(), before, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnreadRepliesDoNotBlock(t *testing.T) {
	// The child floods queries without reading replies; the PTY input
	// queue fills, yet output keeps being parsed and Close completes.
	s := startScript(t, `stty -icanon -echo; i=0; while [ $i -lt 20000 ]; do printf '\033[6n'; i=$((i+1)); done; printf finished; sleep 100`, nil)
	waitFor(t, s, "finished", contains("finished"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// readKeyScript puts the PTY in raw mode, optionally enables modes, prints
// READY and then prints the hex of the next n input bytes as "got:<hex>".
func readKeyScript(modes string, n int) string {
	return `stty raw -echo; printf '` + modes + `READY'; r=$(dd bs=1 count=` + strconv.Itoa(n) + ` 2>/dev/null | od -An -tx1 | tr -d ' \n'); printf '\r\ngot:%s.' "$r"; sleep 5`
}

func sendAndRead(t *testing.T, modes string, n int, send func(*Session) error) string {
	t.Helper()
	if _, err := os.Stat("/usr/bin/od"); err != nil {
		if _, err := os.Stat("/bin/od"); err != nil {
			t.Skip("od unavailable")
		}
	}
	s := startScript(t, readKeyScript(modes, n), nil)
	waitFor(t, s, "READY", func(scr Screen) bool { return strings.Contains(screenText(scr), "READY") })
	if err := send(s); err != nil {
		t.Fatal(err)
	}
	scr := waitFor(t, s, "got", func(scr Screen) bool {
		return strings.Contains(screenText(scr), ".") && strings.Contains(screenText(scr), "got:")
	})
	text := screenText(scr)
	i := strings.Index(text, "got:")
	return strings.TrimSuffix(strings.Fields(text[i+4:])[0], ".")
}

func TestSendKeyFollowsCursorKeyMode(t *testing.T) {
	up := func(s *Session) error { return s.SendKey(Key{Code: "up"}) }
	if got := sendAndRead(t, `\033[?1h`, 3, up); got != "1b4f41" {
		t.Fatalf("DECCKM on: Up sent %s, want ESC O A", got)
	}
	if got := sendAndRead(t, ``, 3, up); got != "1b5b41" {
		t.Fatalf("DECCKM off: Up sent %s, want ESC [ A", got)
	}
	if got := sendAndRead(t, `\033[?1h\033[?1l`, 3, up); got != "1b5b41" {
		t.Fatalf("DECCKM reset: Up sent %s, want ESC [ A", got)
	}
}

func TestSendKeyControlAndAlt(t *testing.T) {
	if got := sendAndRead(t, ``, 1, func(s *Session) error { return s.SendKey(Key{Code: "c", Mods: ModCtrl}) }); got != "03" {
		t.Fatalf("Ctrl+C sent %s, want 03", got)
	}
	if got := sendAndRead(t, ``, 2, func(s *Session) error { return s.SendKey(Key{Code: "x", Text: "x", Mods: ModAlt}) }); got != "1b78" {
		t.Fatalf("Alt+X sent %s, want 1b78", got)
	}
	s := startScript(t, `sleep 5`, nil)
	if err := s.SendKey(Key{Code: "a", Text: "\x1b"}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("control text error = %v", err)
	}
}

func TestPasteBracketsOnlyWhenEnabled(t *testing.T) {
	paste := func(s *Session) error { return s.Paste("a\nb") }
	// ESC[200~ a CR b ESC[201~
	if got := sendAndRead(t, `\033[?2004h`, 15, paste); got != "1b5b3230307e610d621b5b3230317e" {
		t.Fatalf("bracketed paste sent %s", got)
	}
	if got := sendAndRead(t, ``, 3, paste); got != "610d62" {
		t.Fatalf("plain paste sent %s", got)
	}
	s := startScript(t, `sleep 5`, nil)
	if err := s.Paste(strings.Repeat("x", MaxInput+1)); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized paste error = %v", err)
	}
}
