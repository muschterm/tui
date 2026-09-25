package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func conflictFileOf(t *testing.T, e *engine, root, path, version string) protocol.GitConflictFile {
	t.Helper()
	f, err := readConflictFileCopy(context.Background(), root, path, version, "", func(top, key string) []protocol.GitConflictCopy {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.conflictCopiesLocked(top, key)
	})
	if err != nil {
		t.Fatalf("%s %s: %v", path, version, err)
	}
	return f
}

func conflictOf(t *testing.T, st protocol.GitOperationState, path string) protocol.GitConflict {
	t.Helper()
	for _, c := range st.Conflicts {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no conflict for %q in %+v", path, st.Conflicts)
	return protocol.GitConflict{}
}

func unmergedIn(t *testing.T, root, path string) bool {
	t.Helper()
	return gitIn(t, root, "ls-files", "-u", "--", path) != ""
}

// shapesSetup starts a merge through the application with UU, AA, DU, UD,
// binary and symlink conflicts.
func shapesSetup(t *testing.T) (*engine, string, func(...string) string) {
	t.Helper()
	e, root, git := gitWriteSetup(t)
	for _, f := range []string{"uu.txt", "du.txt", "ud.txt"} {
		writeFile(t, root, f, f+" base\n")
	}
	writeFile(t, root, "bin.dat", "base\x00bin")
	if err := os.Symlink("base-target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "uu.txt", "other\n")
	writeFile(t, root, "du.txt", "other\n")
	git("rm", "-q", "ud.txt")
	writeFile(t, root, "aa.txt", "other\n")
	writeFile(t, root, "bin.dat", "other\x00bin")
	os.Remove(filepath.Join(root, "link"))
	os.Symlink("other-target", filepath.Join(root, "link"))
	git("add", "-A")
	git("commit", "-q", "-m", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "uu.txt", "main\n")
	git("rm", "-q", "du.txt")
	writeFile(t, root, "ud.txt", "main\n")
	writeFile(t, root, "aa.txt", "main\n")
	writeFile(t, root, "bin.dat", "main\x00bin")
	os.Remove(filepath.Join(root, "link"))
	os.Symlink("main-target", filepath.Join(root, "link"))
	git("add", "-A")
	git("commit", "-q", "-m", "main")
	p := previewOf(t, root, "merge", "refs/heads/other")
	r := mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	if r.Git.Operation.ConflictCopy == "" {
		t.Fatalf("no saved copy at the stop: %+v", r.Git.Operation)
	}
	return e, root, git
}

func TestGitConflictChooseEachShape(t *testing.T) {
	e, root, git := shapesSetup(t)
	choose := func(id, path, side string) {
		t.Helper()
		st := operationOf(t, e, root)
		mustGit(t, e, client.GitConflictChooseCommand(id, gitTarget, conflictOf(t, st, path), side), protocol.GitStateSucceeded)
		if !unmergedIn(t, root, path) {
			t.Fatalf("choose resolved %s", path)
		}
	}
	choose("uu", "uu.txt", protocol.GitConflictSideTheirs)
	if readText(t, root, "uu.txt") != "other\n" {
		t.Fatal("theirs not written")
	}
	choose("uu-base", "uu.txt", protocol.GitConflictSideBase)
	if readText(t, root, "uu.txt") != "uu.txt base\n" {
		t.Fatal("base not written")
	}
	choose("aa", "aa.txt", protocol.GitConflictSideOurs)
	if readText(t, root, "aa.txt") != "main\n" {
		t.Fatal("ours not written")
	}
	choose("du", "du.txt", protocol.GitConflictSideOurs) // ours deleted it
	if _, err := os.Lstat(filepath.Join(root, "du.txt")); !os.IsNotExist(err) {
		t.Fatal("choosing the deleting side kept the file")
	}
	choose("ud", "ud.txt", protocol.GitConflictSideTheirs)
	if _, err := os.Lstat(filepath.Join(root, "ud.txt")); !os.IsNotExist(err) {
		t.Fatal("choosing the deleting side kept the file")
	}
	choose("bin", "bin.dat", protocol.GitConflictSideTheirs)
	if readText(t, root, "bin.dat") != "other\x00bin" {
		t.Fatal("binary side not byte-identical")
	}
	choose("link", "link", protocol.GitConflictSideTheirs)
	if target, err := os.Readlink(filepath.Join(root, "link")); err != nil || target != "other-target" {
		t.Fatalf("symlink side: %q %v", target, err)
	}
	// Resolve everything; the record becomes ready.
	for _, path := range []string{"uu.txt", "aa.txt", "bin.dat", "link"} {
		f := conflictFileOf(t, e, root, path, protocol.GitConflictVersionWorking)
		if f.Binary {
			_, err := e.command(client.GitConflictResolveCommand("res-unack-"+path, gitTarget, f, protocol.GitConflictAsContent, false))
			wantGitCode(t, err, "binary_unacknowledged")
		}
		mustGit(t, e, client.GitConflictResolveCommand("res-"+path, gitTarget, f, protocol.GitConflictAsContent, f.Binary), protocol.GitStateSucceeded)
	}
	for _, path := range []string{"du.txt", "ud.txt"} {
		f := conflictFileOf(t, e, root, path, protocol.GitConflictVersionWorking)
		mustGit(t, e, client.GitConflictResolveCommand("res-"+path, gitTarget, f, protocol.GitConflictAsDeleted, false), protocol.GitStateSucceeded)
	}
	if out := git("ls-files", "-u"); out != "" {
		t.Fatalf("still unmerged: %s", out)
	}
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationReady {
		t.Fatalf("record: %+v", rec)
	}
	if st := operationOf(t, e, root); !st.Can.Continue.Allowed {
		t.Fatalf("continue after resolving: %+v", st.Can.Continue)
	}
}

func TestGitConflictSubmoduleIsRefused(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a", "a\n")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("update-index", "--add", "--cacheinfo", "160000,"+base+",sub")
	git("commit", "-q", "-m", "gitlink")
	one := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "other")
	git("update-index", "--cacheinfo", "160000,"+one+",sub")
	git("commit", "-q", "-m", "other")
	two := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("update-index", "--cacheinfo", "160000,"+two+",sub")
	git("commit", "-q", "-m", "main")
	gitTry(t, root, "merge", "other")
	st := operationOf(t, e, root)
	_, err := e.command(client.GitConflictChooseCommand("sub", gitTarget, conflictOf(t, st, "sub"), protocol.GitConflictSideOurs))
	wantGitCode(t, err, "not_supported")
}

func TestGitConflictResolveKeepsOtherStagedEntriesAndNeedsMarkersAcknowledged(t *testing.T) {
	e, root, git := conflictSetup(t)
	gitTry(t, root, "merge", "other")
	writeFile(t, root, "side.txt", "unrelated\n")
	git("add", "side.txt")
	sideEntry := git("ls-files", "-s", "--", "side.txt")
	f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	if !f.HasMarkers || !f.Unmerged || f.ConflictPin == "" {
		t.Fatalf("working: %+v", f)
	}
	cmd := client.GitConflictResolveCommand("res-unack", gitTarget, f, protocol.GitConflictAsContent, false)
	_, err := e.command(cmd)
	wantGitCode(t, err, "markers_unacknowledged")
	notRecorded(t, e, cmd)
	mustGit(t, e, client.GitConflictResolveCommand("res", gitTarget, f, protocol.GitConflictAsContent, true), protocol.GitStateSucceeded)
	if unmergedIn(t, root, "c.txt") || git("ls-files", "-s", "--", "side.txt") != sideEntry {
		t.Fatal("resolve touched another entry or left the path unmerged")
	}
	if got := git("show", ":d.txt"); got != "d" {
		t.Fatalf("merge's own staged entry changed: %q", got)
	}
}

func TestGitConflictRestoreIsByteIdentical(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "app"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			e, root, git := conflictSetup(t)
			if external {
				gitTry(t, root, "merge", "other")
			} else {
				p := previewOf(t, root, "merge", "refs/heads/other")
				mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
			}
			stages := git("ls-files", "-u", "-z", "--", "c.txt")
			working := readText(t, root, "c.txt")
			st := operationOf(t, e, root)
			mustGit(t, e, client.GitConflictChooseCommand("choose", gitTarget, conflictOf(t, st, "c.txt"), protocol.GitConflictSideTheirs), protocol.GitStateSucceeded)
			f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
			mustGit(t, e, client.GitConflictResolveCommand("resolve", gitTarget, f, protocol.GitConflictAsContent, false), protocol.GitStateSucceeded)
			saved := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionSaved)
			if string(saved.Content) != working || saved.CopyID == "" || saved.Unmerged {
				t.Fatalf("saved copy: %+v", saved)
			}
			if ours := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionOurs); string(ours.Content) != "main\n" {
				t.Fatalf("saved ours: %q", ours.Content)
			}
			mustGit(t, e, client.GitConflictRestoreCommand("restore", gitTarget, saved, saved.CopyID), protocol.GitStateSucceeded)
			if git("ls-files", "-u", "-z", "--", "c.txt") != stages || readText(t, root, "c.txt") != working {
				t.Fatal("restore is not byte-identical")
			}
			if !external {
				if rec := recordOf(e); rec.State != protocol.GitOperationStoppedConflicts {
					t.Fatalf("record after restore: %+v", rec)
				}
			}
		})
	}
}

func TestGitConflictStalePinsAndNoOperation(t *testing.T) {
	e, root, git := conflictSetup(t)
	_, err := readConflictFile(context.Background(), root, "c.txt", "working", func(string, string) *protocol.GitConflictCopy { return nil })
	wantGitCode(t, err, "no_operation")
	gitTry(t, root, "merge", "other")
	st := operationOf(t, e, root)
	conflict := conflictOf(t, st, "c.txt")
	writeFile(t, root, "c.txt", "edited after review\n")
	cmd := client.GitConflictChooseCommand("stale", gitTarget, conflict, protocol.GitConflictSideOurs)
	_, err = e.command(cmd)
	wantGitCode(t, err, "stale_entry")
	notRecorded(t, e, cmd)
	_, err = readConflictFile(context.Background(), root, "a.txt", "working", func(string, string) *protocol.GitConflictCopy { return nil })
	wantGitCode(t, err, "not_found")
	git("merge", "--abort")
	_, err = e.command(client.GitConflictChooseCommand("none", gitTarget, conflict, protocol.GitConflictSideOurs))
	wantGitCode(t, err, "no_operation")
}

func TestGitConflictResolveRefusesUnsavedDocumentEdits(t *testing.T) {
	h, git := docGit(t)
	h.write("c.txt", "base\n", 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	h.write("c.txt", "other\n", 0o644)
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	h.write("c.txt", "main\n", 0o644)
	git("commit", "-q", "-am", "main")
	gitTry(t, h.root, "merge", "other")
	h.write("c.txt", "resolved\n", 0o644)
	unsavedEdit(h, "c.txt", 51)
	f := conflictFileOf(t, h.e, h.root, "c.txt", protocol.GitConflictVersionWorking)
	r := mustGit(t, h.e, client.GitConflictResolveCommand("res", gitTarget, f, protocol.GitConflictAsContent, false), protocol.GitStateFailed)
	if r.Git.Code != "stale_entry" || !unmergedIn(t, h.root, "c.txt") {
		t.Fatalf("resolve with an unsaved edit: %+v", r.Git)
	}
}

func TestGitConflictSpecialPathNames(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	name := "we ird\nname -x.txt"
	writeFile(t, root, name, "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, name, "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, name, "main\n")
	git("commit", "-q", "-am", "main")
	p := previewOf(t, root, "merge", "refs/heads/other")
	mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	mustGit(t, e, client.GitConflictChooseCommand("choose", gitTarget, conflictOf(t, st, name), protocol.GitConflictSideTheirs), protocol.GitStateSucceeded)
	f := conflictFileOf(t, e, root, name, protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictResolveCommand("res", gitTarget, f, protocol.GitConflictAsContent, false), protocol.GitStateSucceeded)
	saved := conflictFileOf(t, e, root, name, protocol.GitConflictVersionSaved)
	mustGit(t, e, client.GitConflictRestoreCommand("restore", gitTarget, saved, saved.CopyID), protocol.GitStateSucceeded)
	if !unmergedIn(t, root, name) || readText(t, root, name) != string(saved.Content) {
		t.Fatal("special name not restored")
	}
}

// S3 review 2026-09-25: nothing a conflict command overwrites is lost.

func appMergeOther(t *testing.T, e *engine, root string) protocol.Receipt {
	t.Helper()
	p := previewOf(t, root, "merge", "refs/heads/other")
	return mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
}

func TestGitConflictChooseCopiesHandEditsFirst(t *testing.T) {
	e, root, _ := conflictSetup(t)
	appMergeOther(t, e, root)
	writeFile(t, root, "c.txt", "HAND-RESOLVED\n")
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitConflictChooseCommand("c", gitTarget, conflictOf(t, st, "c.txt"), protocol.GitConflictSideOurs), protocol.GitStateSucceeded)
	prev := r.Git.Operation.Previous
	if prev == nil || readText(t, root, "c.txt") != "main\n" {
		t.Fatalf("no copy of the hand edit: %+v", r.Git.Operation)
	}
	f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictRestoreCommand("back", gitTarget, f, prev.CopyID), protocol.GitStateSucceeded)
	if readText(t, root, "c.txt") != "HAND-RESOLVED\n" || !unmergedIn(t, root, "c.txt") {
		t.Fatal("hand edit not restored from its copy")
	}
	// An unchanged file is not copied again.
	st = operationOf(t, e, root)
	f = conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	// at_stop and the hand edit; the choose result the restore replaced is
	// the ours stage, recoverable from the index, so it is not copied.
	if len(f.Copies) != 2 {
		t.Fatalf("copies: %+v", f.Copies)
	}
	_ = st
}

func TestGitConflictRestoreCopiesTheCurrentResolutionFirst(t *testing.T) {
	e, root, _ := conflictSetup(t)
	appMergeOther(t, e, root)
	writeFile(t, root, "c.txt", "RESOLVED-BY-HAND\n")
	f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictResolveCommand("r", gitTarget, f, protocol.GitConflictAsContent, false), protocol.GitStateSucceeded)
	writeFile(t, root, "c.txt", "FURTHER-EDIT\n")
	saved := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionSaved)
	r := mustGit(t, e, client.GitConflictRestoreCommand("rs", gitTarget, saved, saved.CopyID), protocol.GitStateSucceeded)
	prev := r.Git.Operation.Previous
	if prev == nil {
		t.Fatal("the restore did not copy what it replaced")
	}
	now := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictRestoreCommand("undo", gitTarget, now, prev.CopyID), protocol.GitStateSucceeded)
	if readText(t, root, "c.txt") != "FURTHER-EDIT\n" || unmergedIn(t, root, "c.txt") || gitIn(t, root, "show", ":c.txt") != "RESOLVED-BY-HAND" {
		t.Fatal("the replaced resolution (file and staged entry) was not restored")
	}
}

func TestGitConflictCopiesBelongToOneAttempt(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "a.txt", "base\n")
	writeFile(t, root, "b.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "a.txt", "other\n")
	writeFile(t, root, "b.txt", "other\n")
	git("commit", "-q", "-am", "o")
	git("checkout", "-q", "main")
	writeFile(t, root, "a.txt", "main\n")
	writeFile(t, root, "b.txt", "main\n")
	git("commit", "-q", "-am", "m")
	writeFile(t, root, ".gitattributes", "b.txt merge=union\n")
	gitTry(t, root, "merge", "other")
	st := operationOf(t, e, root)
	first := mustGit(t, e, client.GitConflictChooseCommand("c1", gitTarget, conflictOf(t, st, "a.txt"), protocol.GitConflictSideOurs), protocol.GitStateSucceeded)
	git("merge", "--abort")
	if err := os.Remove(filepath.Join(root, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	gitTry(t, root, "merge", "other")
	orig := readText(t, root, "b.txt")
	st = operationOf(t, e, root)
	second := mustGit(t, e, client.GitConflictChooseCommand("c2", gitTarget, conflictOf(t, st, "b.txt"), protocol.GitConflictSideOurs), protocol.GitStateSucceeded)
	if second.Git.Operation.ConflictCopy == "" || second.Git.Operation.ConflictCopy == first.Git.Operation.ConflictCopy {
		t.Fatalf("second attempt reused the first attempt's copy: %q %q", first.Git.Operation.ConflictCopy, second.Git.Operation.ConflictCopy)
	}
	if saved := conflictFileOf(t, e, root, "b.txt", protocol.GitConflictVersionSaved); string(saved.Content) != orig || saved.CopyReason != protocol.GitCopyAtStop {
		t.Fatalf("saved b.txt %q (%s), want %q", saved.Content, saved.CopyReason, orig)
	}
}

func TestGitConflictResolveDeletedThenAbortKeepsTheFile(t *testing.T) {
	e, root, git := conflictSetup(t)
	appMergeOther(t, e, root)
	writeFile(t, root, "c.txt", "USER-RESOLUTION\n")
	f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictResolveCommand("rd", gitTarget, f, protocol.GitConflictAsDeleted, false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if !slices.Contains(st.DiscardsOnAbort, "c.txt") {
		t.Fatalf("untracked leftover not listed: %v", st.DiscardsOnAbort)
	}
	r := mustGit(t, e, client.GitOperationAbortCommand("ab", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if readText(t, root, "c.txt") != "main\n" || backupText(t, e, root, r.Git.Operation.Backup.Oid, "c.txt") != "USER-RESOLUTION\n" {
		t.Fatalf("abort after resolving as deleted: %+v", r.Git)
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("abort left changes")
	}
}

func TestGitConflictUnsavedFileNeedsAcknowledgement(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 65 MiB file")
	}
	e, root, git := gitWriteSetup(t)
	big := strings.Repeat("x", 65<<20)
	writeFile(t, root, "big.txt", "base\n"+big)
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "big.txt", "other\n"+big)
	git("commit", "-q", "-am", "o")
	git("checkout", "-q", "main")
	writeFile(t, root, "big.txt", "main\n"+big)
	git("commit", "-q", "-am", "m")
	appMergeOther(t, e, root)
	writeFile(t, root, "big.txt", "HANDEDIT\n"+big)
	st := operationOf(t, e, root)
	cmd := client.GitConflictChooseCommand("c", gitTarget, conflictOf(t, st, "big.txt"), protocol.GitConflictSideTheirs)
	_, err := e.command(cmd)
	wantGitCode(t, err, "unsaved_unacknowledged")
	cmd.ID, cmd.Git.Conflict.AcknowledgeUnsaved = "c-ack", cmd.Git.Conflict.WorktreeToken
	mustGit(t, e, cmd, protocol.GitStateSucceeded)
}

func TestGitConflictRestoreKeepsPermissionsAndParents(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	name := "dir/sub/c.sh"
	writeFile(t, root, name, "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, name, "other\n")
	git("commit", "-q", "-am", "o")
	git("checkout", "-q", "main")
	writeFile(t, root, name, "main\n")
	git("commit", "-q", "-am", "m")
	appMergeOther(t, e, root)
	// A hand edit with its own permission bits, replaced by a choose.
	writeFile(t, root, name, "hand edit\n")
	if err := os.Chmod(filepath.Join(root, name), 0o750); err != nil {
		t.Fatal(err)
	}
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitConflictChooseCommand("c", gitTarget, conflictOf(t, st, name), protocol.GitConflictSideTheirs), protocol.GitStateSucceeded)
	prev := r.Git.Operation.Previous
	if prev == nil {
		t.Fatal("hand edit not copied")
	}
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	f := conflictFileOf(t, e, root, name, protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictRestoreCommand("rs", gitTarget, f, prev.CopyID), protocol.GitStateSucceeded)
	fi, err := os.Stat(filepath.Join(root, name))
	if err != nil || fi.Mode().Perm() != 0o750 || readText(t, root, name) != "hand edit\n" || !unmergedIn(t, root, name) {
		t.Fatalf("restore: mode %v err %v", fi.Mode(), err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "dir", "sub", ".tui-restore-*")); len(matches) != 0 {
		t.Fatalf("temporary files left: %v", matches)
	}
}

func TestGitConflictCommandsAtRebaseAndCherryPickStops(t *testing.T) {
	for _, kind := range []string{"rebase", "cherry-pick"} {
		t.Run(kind, func(t *testing.T) {
			e, root, git := conflictSetup(t)
			if kind == "rebase" {
				git("checkout", "-q", "other")
				gitTry(t, root, "rebase", "main")
			} else {
				gitTry(t, root, "cherry-pick", git("rev-parse", "other~1"))
			}
			st := operationOf(t, e, root)
			if st.Kind != kind {
				t.Fatalf("no %s: %+v", kind, st)
			}
			mustGit(t, e, client.GitConflictChooseCommand("c", gitTarget, conflictOf(t, st, "c.txt"), protocol.GitConflictSideTheirs), protocol.GitStateSucceeded)
			if theirs := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionTheirs); readText(t, root, "c.txt") != string(theirs.Content) {
				t.Fatal("theirs not written")
			}
			f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
			mustGit(t, e, client.GitConflictResolveCommand("r", gitTarget, f, protocol.GitConflictAsContent, false), protocol.GitStateSucceeded)
			if st := operationOf(t, e, root); !st.Can.Continue.Allowed {
				t.Fatalf("continue after resolving: %+v", st.Can.Continue)
			}
		})
	}
}

func TestGitConflictReadableThroughTheHTTPRoute(t *testing.T) {
	root, git := gitFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	writeFile(t, root, "c.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	gitTry(t, root, "merge", "other")
	home := t.TempDir()
	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	snap := fixture.Initial()
	snap.Projects = append(snap.Projects, protocol.Project{ID: "p-git", Name: "repo", Path: root, Revision: 1})
	if err := st.Save(snap, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	c, stop := startTestServer(t, home)
	defer stop()
	target := client.GitTarget{ProjectID: "p-git"}
	ours, err := c.GitConflictFile(context.Background(), target, "c.txt", protocol.GitConflictVersionOurs)
	if err != nil || string(ours.Content) != "main\n" || !ours.Unmerged || ours.Kind != "file" {
		t.Fatalf("ours: %+v %v", ours, err)
	}
	working, err := c.GitConflictFile(context.Background(), target, "c.txt", protocol.GitConflictVersionWorking)
	if err != nil || !working.HasMarkers || working.WorktreeToken == "" {
		t.Fatalf("working: %+v %v", working, err)
	}
	if _, err := c.GitConflictFile(context.Background(), target, "missing.txt", protocol.GitConflictVersionWorking); err == nil {
		t.Fatal("a path that is not conflicted was served")
	}
}

func TestGitConflictCommandRemovesAnInterruptedRestoreTemp(t *testing.T) {
	e, root, _ := conflictSetup(t)
	appMergeOther(t, e, root)
	orphan := filepath.Join(root, ".tui-restore-123")
	writeFile(t, root, ".tui-restore-123", "half written\n")
	if err := os.WriteFile(filepath.Join(root, ".git", gitRestorePendingState), []byte(orphan+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := operationOf(t, e, root)
	mustGit(t, e, client.GitConflictChooseCommand("c", gitTarget, conflictOf(t, st, "c.txt"), protocol.GitConflictSideOurs), protocol.GitStateSucceeded)
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatal("interrupted restore temporary file left behind")
	}
}

// S3 targeted check 2026-09-25.

func TestGitAbortPutsBackMovedFilesWhenGitRefuses(t *testing.T) {
	e, root, _ := conflictSetup(t)
	appMergeOther(t, e, root)
	writeFile(t, root, "c.txt", "USER-RESOLUTION\n")
	if err := os.Chmod(filepath.Join(root, "c.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	f := conflictFileOf(t, e, root, "c.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictResolveCommand("rd", gitTarget, f, protocol.GitConflictAsDeleted, false), protocol.GitStateSucceeded)
	// An unstaged edit of a file the merge staged makes Git refuse the
	// abort; the precheck names it and nothing moves.
	writeFile(t, root, "d.txt", "dirty edit of a cleanly merged file\n")
	st := operationOf(t, e, root)
	if !slices.Equal(st.AbortBlockedBy, []string{"d.txt"}) || st.Can.Abort.Allowed {
		t.Fatalf("blocked: %v %+v", st.AbortBlockedBy, st.Can.Abort)
	}
	cmd := client.GitOperationAbortCommand("ab", gitTarget, st, true, false)
	_, err := e.command(cmd)
	wantGitCode(t, err, "abort_blocked")
	notRecorded(t, e, cmd)
	writeFile(t, root, "d.txt", "d\n")
	// Git fails after the untracked c.txt was moved aside (it cannot take
	// index.lock): c.txt comes back byte-exact with its permissions.
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	st = operationOf(t, e, root)
	gitDir := filepath.Join(root, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	r, err := e.command(client.GitOperationAbortCommand("ab-fails", gitTarget, st, true, false))
	os.Chmod(gitDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	fi, serr := os.Stat(filepath.Join(root, "c.txt"))
	if r.Git.State != protocol.GitStateFailed || !strings.Contains(r.Git.Message, "put back") || serr != nil || fi.Mode().Perm() != 0o640 || readText(t, root, "c.txt") != "USER-RESOLUTION\n" {
		t.Fatalf("after a failing abort: %+v %v %v", r.Git, fi, serr)
	}
}

func TestGitConflictHandEditCopySurvivesManyChoices(t *testing.T) {
	e, root, _ := shapesSetup(t)
	writeFile(t, root, "uu.txt", "MY PRECIOUS EDIT\n")
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitConflictChooseCommand("uu", gitTarget, conflictOf(t, st, "uu.txt"), protocol.GitConflictSideTheirs), protocol.GitStateSucceeded)
	prev := r.Git.Operation.Previous
	if prev == nil {
		t.Fatal("no copy of the hand edit")
	}
	for i := range 22 {
		side := protocol.GitConflictSideOurs
		if i%2 == 1 {
			side = protocol.GitConflictSideTheirs
		}
		st := operationOf(t, e, root)
		r := mustGit(t, e, client.GitConflictChooseCommand(fmt.Sprintf("aa%d", i), gitTarget, conflictOf(t, st, "aa.txt"), side), protocol.GitStateSucceeded)
		if len(r.Git.Operation.Evicted) != 0 {
			t.Fatalf("evicted: %+v", r.Git.Operation.Evicted)
		}
	}
	e.mu.Lock()
	copies := len(e.snap.GitConflictCopies)
	e.mu.Unlock()
	if copies > 3 {
		t.Fatalf("identical content copied again: %d copies", copies)
	}
	f := conflictFileOf(t, e, root, "uu.txt", protocol.GitConflictVersionWorking)
	mustGit(t, e, client.GitConflictRestoreCommand("back", gitTarget, f, prev.CopyID), protocol.GitStateSucceeded)
	if readText(t, root, "uu.txt") != "MY PRECIOUS EDIT\n" {
		t.Fatal("hand edit lost")
	}
}

func TestGitConflictCopyRetention(t *testing.T) {
	s := protocol.Snapshot{}
	key := checkoutKey("/repo")
	put := func(stop, reason, path, content string) []protocol.GitConflictCopy {
		return putConflictCopy(&s, protocol.GitConflictCopy{Checkout: "/repo", CheckoutKey: key, StopKey: stop, Reason: reason, Path: path, ContentOid: content, CopyID: stop + path + content})
	}
	put("old", protocol.GitCopyAtStop, "", "")
	put("now", protocol.GitCopyAtStop, "", "")
	put("now", protocol.GitCopyBeforeOverwrite, "a", "x")
	if len(put("now", protocol.GitCopyBeforeOverwrite, "a", "x")) != 0 || len(s.GitConflictCopies) != 3 {
		t.Fatalf("duplicate content appended: %d", len(s.GitConflictCopies))
	}
	var evicted []protocol.GitConflictCopy
	for i := range gitConflictCopies {
		evicted = append(evicted, put("now", protocol.GitCopyBeforeOverwrite, fmt.Sprint(i%5), fmt.Sprint(i))...)
	}
	for _, c := range evicted {
		if c.StopKey == "now" && c.Reason == protocol.GitCopyAtStop {
			t.Fatal("the current stop's at_stop copy was evicted")
		}
	}
	found := false
	for _, c := range s.GitConflictCopies {
		found = found || (c.StopKey == "now" && c.Reason == protocol.GitCopyAtStop)
	}
	if !found {
		t.Fatal("current stop lost its at_stop copy")
	}
}
