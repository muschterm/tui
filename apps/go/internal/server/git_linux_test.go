package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestGitUntrackedSymlinkSwapRace exchanges a real directory with a symlink
// to an outside directory while diffs run; outside content must never leak.
func TestGitUntrackedSymlinkSwapRace(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	outside := t.TempDir()
	writeFile(t, outside, "f.txt", "OUTSIDE\n")
	writeFile(t, root, "x/f.txt", "inside\n")
	if err := os.MkdirAll(filepath.Join(root, "hold"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "hold", "y")); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			_ = unix.Renameat2(unix.AT_FDCWD, filepath.Join(root, "x"), unix.AT_FDCWD, filepath.Join(root, "hold", "y"), unix.RENAME_EXCHANGE)
		}
	}()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		if d, err := readGitDiff(context.Background(), root, "x/f.txt", "untracked"); err == nil && strings.Contains(d.Text, "OUTSIDE") {
			stop.Store(true)
			t.Fatal("leaked content through a swapped symlink")
		}
		if f, err := readUntracked(root, "x/f.txt", 1024); err == nil && strings.Contains(string(f.data), "OUTSIDE") {
			stop.Store(true)
			t.Fatal("readUntracked followed a swapped symlink")
		}
	}
	stop.Store(true)
	<-done
}
