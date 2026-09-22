package agent

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestCloseWaitsForTheChildProcess covers the live finding that server stop
// returned, and recorded success, while an adapter child was still running. The
// stub ignores stdin close, so Close must wait for the bounded kill and reap.
func TestCloseWaitsForTheChildProcess(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(fmt.Sprintf("killed=%v", killed), func(t *testing.T) {
			testCloseWaitsForChild(t, killed)
		})
	}
}

func testCloseWaitsForChild(t *testing.T, killed bool) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell available")
	}
	session, err := Start(context.Background(), Options{
		Command: "sh",
		// Hold stdin open and ignore termination requests other than KILL, so
		// the process only ends through the bounded kill path.
		Args: []string{"-c", "trap '' TERM INT; sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := session.pid
	if pid <= 0 {
		t.Fatal("no child process was recorded")
	}
	if killed {
		session.Kill()
	}
	start := time.Now()
	session.Close(context.Background())
	elapsed := time.Since(start)
	if elapsed > exitWait {
		t.Fatalf("Close exceeded its bounded wait: %s", elapsed)
	}
	// A reaped process no longer answers signal 0 from its own parent.
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("Close returned after %s while the adapter process was still running", elapsed)
	}
}
