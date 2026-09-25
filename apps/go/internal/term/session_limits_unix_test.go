//go:build unix

package term

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"
)

// A flood of combining marks must not grow cells in the grid or the
// scrollback beyond MaxClusterBytes; ordinary clusters stay intact.
func TestCombiningFloodIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("é 👨‍👩‍👧‍👦 ok\r\n")
	for range 30 {
		for range 20 {
			b.WriteString("a" + strings.Repeat("́", 2000))
		}
		b.WriteString("\r\n")
	}
	b.WriteString("DONE\r\n")
	file := filepath.Join(t.TempDir(), "flood")
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	s := startScript(t, "cat "+file+"; sleep 30", func(c *Config) { c.Cols, c.Rows = 40, 5 })
	waitFor(t, s, "flood output", func(scr Screen) bool { return strings.Contains(screenText(scr), "DONE") })
	longest := map[string]int{}
	check := func(where string, content string) {
		longest[where] = max(longest[where], len(content))
	}
	s.mu.Lock()
	for y := range s.emu.Height() {
		for x := range s.emu.Width() {
			if c := s.emu.CellAt(x, y); c != nil {
				check("grid", c.Content)
			}
		}
	}
	sb := s.emu.Scrollback()
	first := append([]uv.Cell(nil), sb.Line(0)...)
	lines := sb.Len()
	for i := range lines {
		for _, c := range sb.Line(i) {
			check("scrollback", c.Content)
		}
	}
	s.mu.Unlock()
	if lines < 20 {
		t.Fatalf("only %d scrollback lines", lines)
	}
	if len(first) < 3 || first[0].Content != "é" || first[2].Content != "👨‍👩‍👧‍👦" {
		t.Fatalf("ordinary clusters changed: %q %q", first[0].Content, first[2].Content)
	}
	for _, row := range s.Snapshot().Lines {
		for _, c := range row {
			check("snapshot", c.Text)
		}
	}
	for where, n := range longest {
		if n > MaxClusterBytes {
			t.Fatalf("%s holds a %d-byte cluster", where, n)
		}
	}
	if got := capCluster("a" + strings.Repeat("́", 100)); len(got) != 127 || !strings.HasPrefix(got, "á") {
		t.Fatalf("capCluster kept %d bytes", len(got))
	}
}

// Input the child does not read times out as busy, reporting the bytes that
// did reach it, and the session keeps running.
func TestWriteToStalledChildIsBusy(t *testing.T) {
	s := startScript(t, "stty raw -echo; printf READY; sleep 40", nil)
	waitFor(t, s, "READY", func(scr Screen) bool { return strings.Contains(screenText(scr), "READY") })
	chunk := []byte(strings.Repeat("x", MaxInput))
	var busy *BusyError
	for range 16 {
		err := s.Write(chunk)
		if err == nil {
			continue
		}
		if !errors.As(err, &busy) || !errors.Is(err, ErrBusy) {
			t.Fatalf("stalled write: %v", err)
		}
		break
	}
	if busy == nil {
		t.Fatal("writes never stalled")
	}
	if busy.Total != MaxInput || busy.Written < 0 || busy.Written >= busy.Total {
		t.Fatalf("busy %+v", busy)
	}
	select {
	case <-s.Done():
		t.Fatal("session ended after a busy write")
	default:
	}
}

func processAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	f := strings.Fields(string(b[i+1:]))
	return len(f) > 0 && f[0] != "Z" && f[0] != "X"
}

// Close ends background jobs in their own process groups that ignore
// SIGHUP, and reports them.
func TestCloseKillsSessionDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("session enumeration is implemented for Linux")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := `set -m; sh -c 'trap "" HUP; exec sleep 7021' & echo $! > ` + pidFile + `; printf READY; wait`
	s := startScript(t, script, nil)
	waitFor(t, s, "READY", func(scr Screen) bool { return strings.Contains(screenText(scr), "READY") })
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	if pgid, _ := unix.Getpgid(pid); pgid == s.cmd.Process.Pid {
		t.Fatal("job control did not give the job its own process group")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ExitStatus()
	if st.Descendants < 1 || !st.DescendantsKilled || st.DescendantsRemaining != 0 || st.DescendantsUnknown {
		t.Fatalf("exit status %+v", st)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatal("HUP-ignoring background job survived Close")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A shell exiting by itself leaves its jobs alone but counts them.
func TestNaturalExitCountsDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("session enumeration is implemented for Linux")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	s := startScript(t, `set -m; sh -c 'trap "" HUP; exec sleep 7022' & echo $! > `+pidFile+`; exit 0`, nil)
	st := waitDone(t, s)
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 {
		defer unix.Kill(pid, unix.SIGKILL)
	}
	if st.Descendants < 1 || st.DescendantsKilled || st.DescendantsRemaining < 1 {
		t.Fatalf("exit status %+v", st)
	}
}
