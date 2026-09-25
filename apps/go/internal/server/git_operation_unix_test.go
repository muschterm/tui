//go:build unix

package server

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// A FIFO planted as a Git state file must not block a read under the engine
// lock.
func TestReadStateFileRefusesSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "MERGE_HEAD"), 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan string, 1)
	go func() { done <- readStateFile(dir, "MERGE_HEAD") }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("read %q from a FIFO", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

// An acknowledgement of a backup that cannot be complete names exactly the
// files the backup leaves out.
func TestGitBackupMissingFilesMustBeAcknowledgedExactly(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "Z", "tracked\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	git("rm", "-q", "Z")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	p := previewOf(t, root, "rebase", "refs/heads/other")
	mustGit(t, e, client.GitRebaseCommand("rb", gitTarget, mustStatus(t, root), p, false), protocol.GitStateSucceeded)
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "Z"), 0o600); err != nil {
		t.Skip(err)
	}
	st := operationOf(t, e, root)
	if !slices.Equal(st.BackupMissingOnAbort, []string{"Z"}) || !st.BackupIncomplete {
		t.Fatalf("missing: %v", st.BackupMissingOnAbort)
	}
	cmd := client.GitOperationAbortCommand("ab-yes-no", gitTarget, st, true, true)
	cmd.Git.Operation.AcknowledgeBackupMissing, cmd.Git.Operation.AcknowledgeBackupIncomplete = "", true
	_, err := e.command(cmd)
	wantGitCode(t, err, "backup_incomplete")
	mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, true), protocol.GitStateSucceeded)
}
