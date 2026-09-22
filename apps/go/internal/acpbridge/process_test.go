package acpbridge

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeStopKillsOwnedDescendantsOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group ownership is verified on Unix")
	}

	root := t.TempDir()
	ownedHeartbeat := root + "/owned-heartbeat"
	f := startClaudeFixture(t, "TUI_GO_CLAUDE_TEST_DESCENDANT_MARKER="+ownedHeartbeat)
	waitHeartbeat(t, ownedHeartbeat, "")

	otherHeartbeat := root + "/other-heartbeat"
	other := exec.Command(os.Args[0], "-test.run=^TestClaudeRuntimeHelper$")
	other.Env = environmentWith(os.Environ(), fakeClaudeRole+"=descendant", "TUI_GO_CLAUDE_TEST_HEARTBEAT="+otherHeartbeat)
	other.Stdin = nil
	other.Stdout, other.Stderr = io.Discard, io.Discard
	if err := other.Start(); err != nil {
		t.Fatalf("start unrelated process: %v", err)
	}
	t.Cleanup(func() {
		if other.Process != nil {
			_ = other.Process.Kill()
			_, _ = other.Process.Wait()
		}
	})
	waitHeartbeat(t, otherHeartbeat, "")

	// Wait until both helpers have written again so a stale timestamp or a
	// delayed first write cannot be mistaken for process liveness.
	_ = waitHeartbeatChange(t, ownedHeartbeat, readHeartbeat(t, ownedHeartbeat))
	otherBefore := waitHeartbeatChange(t, otherHeartbeat, readHeartbeat(t, otherHeartbeat))
	f.endpoint.Stop()

	_ = waitHeartbeatStable(t, ownedHeartbeat, 250*time.Millisecond)
	if got := waitHeartbeatChange(t, otherHeartbeat, otherBefore); got == otherBefore {
		t.Fatalf("Stop affected an unrelated process: heartbeat remained %q", got)
	}
}

func environmentWith(env []string, replacements ...string) []string {
	keys := make(map[string]struct{}, len(replacements))
	for _, replacement := range replacements {
		if at := strings.IndexByte(replacement, '='); at >= 0 {
			keys[replacement[:at]] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(env)+len(replacements))
	for _, entry := range env {
		at := strings.IndexByte(entry, '=')
		if at >= 0 {
			if _, replace := keys[entry[:at]]; replace {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, replacements...)
}

func readHeartbeat(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper heartbeat %q: %v", path, err)
	}
	return string(b)
}

func waitHeartbeat(t *testing.T, path, previous string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && string(b) != previous && string(b) != "" {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("heartbeat %q did not advance from %q", path, previous)
	return ""
}

func waitHeartbeatChange(t *testing.T, path, previous string) string {
	t.Helper()
	return waitHeartbeat(t, path, previous)
}

func waitHeartbeatStable(t *testing.T, path string, stableFor time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	last, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper heartbeat %q: %v", path, err)
	}
	lastValue := string(last)
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read helper heartbeat %q: %v", path, err)
		}
		if string(current) != lastValue {
			lastValue = string(current)
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= stableFor {
			return lastValue
		}
	}
	t.Fatalf("heartbeat %q kept changing; last value %q", path, lastValue)
	return ""
}
