package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// hunksOf reads GET /v1/git/hunks's reply directly.
func hunksOf(t *testing.T, root, path, group string) protocol.GitHunks {
	t.Helper()
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := readPartialState(context.Background(), g, path, group)
	if err != nil {
		t.Fatalf("hunks %s %s: %v", path, group, err)
	}
	return st.view
}

func supportedHunks(t *testing.T, root, path, group string) protocol.GitHunks {
	t.Helper()
	h := hunksOf(t, root, path, group)
	if h.Unsupported != "" || h.Fingerprint == "" {
		t.Fatalf("%s %s unsupported: %+v", path, group, h)
	}
	return h
}

// lineIndex finds the selectable line of kind with text.
func lineIndex(t *testing.T, h protocol.GitHunks, kind, text string) int {
	t.Helper()
	for _, hk := range h.Hunks {
		for _, l := range hk.Lines {
			if l.Kind == kind && l.Text == text {
				return l.Index
			}
		}
	}
	t.Fatalf("no %s line %q in %+v", kind, text, h.Hunks)
	return -1
}

// worktreeFiles records every worktree file's bytes and mtime outside .git.
func worktreeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		b, _ := os.ReadFile(p)
		out[p] = fmt.Sprintf("%s|%d|%s", info.Mode(), info.ModTime().UnixNano(), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertWorktreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	if after := worktreeFiles(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("partial staging changed a worktree file")
	}
}

func partialCmd(id string, h protocol.GitHunks, hunks, lines []int) protocol.Command {
	return client.GitPartialCommand(id, gitTarget, h, hunks, lines)
}

func numbered(n int, edit func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		line := fmt.Sprintf("line %d", i)
		if edit != nil {
			if s := edit(i); s != "" {
				line = s
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func TestGitPartialHunkStageAndUnstage(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", numbered(30, nil))
	writeFile(t, root, "other.txt", "other\n")
	git("add", ".")
	git("commit", "-m", "base")
	// Unrelated staged work in another file must survive.
	writeFile(t, root, "other.txt", "other staged\n")
	git("add", "other.txt")
	edited := numbered(30, func(i int) string {
		switch i {
		case 3:
			return "THREE"
		case 25:
			return "TWENTY-FIVE"
		}
		return ""
	})
	writeFile(t, root, "f.txt", edited)
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	if len(h.Hunks) != 2 || h.Mode != "100644" || h.ModeChanged {
		t.Fatalf("want two hunks: %+v", h)
	}
	before := worktreeFiles(t, root)
	r := mustGit(t, e, partialCmd("stage-h0", h, []int{0}, nil), protocol.GitStateSucceeded)
	if r.Git.Code != "" || !strings.Contains(r.Git.Message, "f.txt") {
		t.Fatalf("result: %+v", r.Git)
	}
	assertWorktreeUnchanged(t, root, before)
	want := numbered(30, func(i int) string {
		if i == 3 {
			return "THREE"
		}
		return ""
	})
	if got := git("show", ":f.txt") + "\n"; got != want {
		t.Fatalf("index:\n%s", got)
	}
	if git("show", ":other.txt") != "other staged" {
		t.Fatal("unrelated staged file lost")
	}
	// Unstage that hunk again from the staged diff.
	sh := supportedHunks(t, root, "f.txt", protocol.GitGroupStaged)
	if len(sh.Hunks) != 1 {
		t.Fatalf("staged hunks: %+v", sh)
	}
	mustGit(t, e, partialCmd("unstage-h0", sh, []int{0}, nil), protocol.GitStateSucceeded)
	assertWorktreeUnchanged(t, root, before)
	if git("diff", "--cached", "--name-only") != "other.txt" {
		t.Fatalf("f.txt still staged: %s", git("diff", "--cached", "--stat"))
	}
	if git("show", ":other.txt") != "other staged" {
		t.Fatal("unrelated staged file lost")
	}
}

func TestGitPartialLinesAndReplacementBlocks(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", "l1\nl2\nl3\nl4\nl5\nl6\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", "l1\nl2\nl3\nL4\nL5\nl6\nADDED\n")
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	before := worktreeFiles(t, root)
	// Replace l4 by L4 only: the kept l5 follows the selected new line.
	sel := []int{lineIndex(t, h, "delete", "l4"), lineIndex(t, h, "add", "L4")}
	mustGit(t, e, partialCmd("lines", h, nil, sel), protocol.GitStateSucceeded)
	if got := git("show", ":f.txt"); got != "l1\nl2\nl3\nL4\nl5\nl6" {
		t.Fatalf("index: %q", got)
	}
	// Stage the addition by line and a hunk-less remainder stays unstaged.
	h = supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	mustGit(t, e, partialCmd("lines-2", h, nil, []int{lineIndex(t, h, "add", "ADDED")}), protocol.GitStateSucceeded)
	if got := git("show", ":f.txt"); got != "l1\nl2\nl3\nL4\nl5\nl6\nADDED" {
		t.Fatalf("index: %q", got)
	}
	// Unstage just the restored-line half of the staged replacement:
	// selecting "-l4" (in the staged diff) restores l4 before the kept L4.
	sh := supportedHunks(t, root, "f.txt", protocol.GitGroupStaged)
	mustGit(t, e, partialCmd("unstage-line", sh, nil, []int{lineIndex(t, sh, "delete", "l4")}), protocol.GitStateSucceeded)
	if got := git("show", ":f.txt"); got != "l1\nl2\nl3\nl4\nL4\nl5\nl6\nADDED" {
		t.Fatalf("index: %q", got)
	}
	sh = supportedHunks(t, root, "f.txt", protocol.GitGroupStaged)
	mustGit(t, e, partialCmd("unstage-line-2", sh, nil, []int{lineIndex(t, sh, "add", "ADDED")}), protocol.GitStateSucceeded)
	if got := git("show", ":f.txt"); got != "l1\nl2\nl3\nl4\nL4\nl5\nl6" {
		t.Fatalf("index: %q", got)
	}
	assertWorktreeUnchanged(t, root, before)
}

func TestGitPartialPreservesStagedWorkInTheSameFile(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", numbered(40, nil))
	git("add", ".")
	git("commit", "-m", "base")
	// Line 2 is staged; then the worktree also changes lines 20 and 35.
	writeFile(t, root, "f.txt", numbered(40, func(i int) string {
		if i == 2 {
			return "STAGED"
		}
		return ""
	}))
	git("add", "f.txt")
	writeFile(t, root, "f.txt", numbered(40, func(i int) string {
		switch i {
		case 2:
			return "STAGED"
		case 20:
			return "TWENTY"
		case 35:
			return "THIRTY-FIVE"
		}
		return ""
	}))
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	if len(h.Hunks) != 2 {
		t.Fatalf("hunks: %+v", h.Hunks)
	}
	mustGit(t, e, partialCmd("stage-35", h, []int{1}, nil), protocol.GitStateSucceeded)
	want := numbered(40, func(i int) string {
		switch i {
		case 2:
			return "STAGED"
		case 35:
			return "THIRTY-FIVE"
		}
		return ""
	})
	if got := git("show", ":f.txt") + "\n"; got != want {
		t.Fatalf("index:\n%s", got)
	}
	// Unstaging the line-2 hunk keeps the staged line 35.
	sh := supportedHunks(t, root, "f.txt", protocol.GitGroupStaged)
	mustGit(t, e, partialCmd("unstage-2", sh, []int{0}, nil), protocol.GitStateSucceeded)
	want = numbered(40, func(i int) string {
		if i == 35 {
			return "THIRTY-FIVE"
		}
		return ""
	})
	if got := git("show", ":f.txt") + "\n"; got != want {
		t.Fatalf("index:\n%s", got)
	}
}

func TestGitPartialNoTrailingNewline(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", "a\nb")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", "A\nb\nc")
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	mustGit(t, e, partialCmd("nl", h, nil, []int{lineIndex(t, h, "add", "c")}), protocol.GitStateSucceeded)
	out, err := exec.Command("git", "-C", root, "cat-file", "blob", ":f.txt").Output()
	if err != nil || string(out) != "a\nb\nc" {
		t.Fatalf("index: %q %v", out, err)
	}
	h = supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	mustGit(t, e, partialCmd("nl-all", h, []int{0}, nil), protocol.GitStateSucceeded)
	out, _ = exec.Command("git", "-C", root, "cat-file", "blob", ":f.txt").Output()
	if string(out) != "A\nb\nc" {
		t.Fatalf("index: %q", out)
	}
}

func TestGitPartialLineEndingsAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(git func(...string) string, root string)
	}{
		{"autocrlf", func(git func(...string) string, root string) { git("config", "core.autocrlf", "true") }},
		{"eol-attribute", func(git func(...string) string, root string) {
			writeFile(t, root, ".gitattributes", "*.txt text eol=crlf\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, root, git := gitWriteSetup(t)
			tc.setup(git, root)
			writeFile(t, root, "f.txt", strings.ReplaceAll(numbered(20, nil), "\n", "\r\n"))
			git("add", ".")
			git("commit", "-m", "base")
			if got := git("cat-file", "blob", "HEAD:f.txt"); strings.Contains(got, "\r") {
				t.Fatal("setup: HEAD blob should be LF")
			}
			writeFile(t, root, "f.txt", strings.ReplaceAll(numbered(20, func(i int) string {
				switch i {
				case 2:
					return "TWO"
				case 18:
					return "EIGHTEEN"
				}
				return ""
			}), "\n", "\r\n"))
			h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
			if len(h.Hunks) != 2 || strings.Contains(h.Hunks[0].Lines[0].Text, "\r") {
				t.Fatalf("diff should be in index (LF) form: %+v", h.Hunks)
			}
			before := worktreeFiles(t, root)
			mustGit(t, e, partialCmd("crlf", h, []int{1}, nil), protocol.GitStateSucceeded)
			assertWorktreeUnchanged(t, root, before)
			out, _ := exec.Command("git", "-C", root, "cat-file", "blob", ":f.txt").Output()
			want := numbered(20, func(i int) string {
				if i == 18 {
					return "EIGHTEEN"
				}
				return ""
			})
			if string(out) != want {
				t.Fatalf("index: %q", out)
			}
		})
	}
	t.Run("latin1", func(t *testing.T) {
		e, root, git := gitWriteSetup(t)
		writeFile(t, root, "f.txt", "caf\xe9\nx\ny\nz\nw\nv\nu\nt\nna\xefve\n")
		git("add", ".")
		git("commit", "-m", "base")
		writeFile(t, root, "f.txt", "CAF\xc9\nx\ny\nz\nw\nv\nu\nt\nNA\xcfVE\n")
		h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
		// Text crosses JSON as U+FFFD; the selection is by index.
		var dels []int
		for _, l := range h.Hunks[len(h.Hunks)-1].Lines {
			if l.Kind != "context" {
				dels = append(dels, l.Index)
			}
		}
		mustGit(t, e, partialCmd("latin1", h, nil, dels), protocol.GitStateSucceeded)
		out, _ := exec.Command("git", "-C", root, "cat-file", "blob", ":f.txt").Output()
		if string(out) != "caf\xe9\nx\ny\nz\nw\nv\nu\nt\nNA\xcfVE\n" {
			t.Fatalf("index bytes: %q", out)
		}
	})
}

func TestGitPartialIntentToAddAndRename(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "old.txt", numbered(30, nil))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "ita.txt", "one\ntwo\nthree\n")
	git("add", "-N", "ita.txt")
	h := supportedHunks(t, root, "ita.txt", protocol.GitGroupUnstaged)
	mustGit(t, e, partialCmd("ita", h, nil, []int{lineIndex(t, h, "add", "two")}), protocol.GitStateSucceeded)
	if got := git("show", ":ita.txt"); got != "two" {
		t.Fatalf("index: %q", got)
	}
	if e := entryOf(t, root, "ita.txt", protocol.GitGroupStaged); e.Index != "A" {
		t.Fatalf("intent-to-add not replaced by a staged file: %+v", e)
	}
	// A staged rename keeps its rename while one hunk is unstaged (A15).
	git("mv", "old.txt", "new.txt")
	writeFile(t, root, "new.txt", numbered(30, func(i int) string {
		switch i {
		case 2:
			return "TWO"
		case 28:
			return "TWENTY-EIGHT"
		}
		return ""
	}))
	git("add", "new.txt")
	sh := supportedHunks(t, root, "new.txt", protocol.GitGroupStaged)
	if sh.OrigPath != "old.txt" || len(sh.Hunks) != 2 {
		t.Fatalf("rename hunks: %+v", sh)
	}
	mustGit(t, e, partialCmd("rename", sh, []int{0}, nil), protocol.GitStateSucceeded)
	if e := entryOf(t, root, "new.txt", protocol.GitGroupStaged); e.Index != "R" || e.OrigPath != "old.txt" {
		t.Fatalf("rename lost: %+v", e)
	}
	want := numbered(30, func(i int) string {
		if i == 28 {
			return "TWENTY-EIGHT"
		}
		return ""
	})
	if got := git("show", ":new.txt") + "\n"; got != want {
		t.Fatalf("index:\n%s", got)
	}
}

func TestGitPartialRefusals(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "bin.dat", "a\x00b\n")
	writeFile(t, root, "lfs.txt", "pointer\n")
	writeFile(t, root, "enc.txt", "\xff\xfet\x00x\x00\n\x00")
	writeFile(t, root, "exec.sh", "echo\n")
	writeFile(t, root, "gone.txt", "gone\n")
	writeFile(t, root, "staged-gone.txt", "x\n")
	writeFile(t, root, "marked.txt", "m\n")
	writeFile(t, root, ".gitattributes", "lfs.txt filter=lfs\nenc.txt working-tree-encoding=UTF-16\nmarked.txt -diff\n")
	if err := os.Symlink("target-a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "bin.dat", "a\x00c\n")
	writeFile(t, root, "lfs.txt", "pointer 2\n")
	writeFile(t, root, "marked.txt", "m2\n")
	writeFile(t, root, "enc.txt", "\xff\xfey\x00\n\x00")
	os.Chmod(filepath.Join(root, "exec.sh"), 0o755)
	os.Remove(filepath.Join(root, "gone.txt"))
	git("rm", "-q", "staged-gone.txt")
	os.Remove(filepath.Join(root, "link"))
	os.Symlink("target-b", filepath.Join(root, "link"))
	for path, code := range map[string]string{
		"bin.dat": "binary", "lfs.txt": "filter", "exec.sh": "no_content", "gone.txt": "deleted",
		"link": "symlink", "marked.txt": "binary", "enc.txt": "encoding",
	} {
		h := hunksOf(t, root, path, protocol.GitGroupUnstaged)
		if h.Unsupported != code || h.Fingerprint != "" || h.Message == "" {
			t.Errorf("%s: want %s, got %+v", path, code, h)
		}
		c := partialCmd("refuse-"+path, protocol.GitHunks{Path: path, Group: protocol.GitGroupUnstaged, Fingerprint: "p1-x"}, []int{0}, nil)
		_, err := e.command(c)
		wantGitCode(t, err, "not_supported")
		notRecorded(t, e, c)
	}
	if h := hunksOf(t, root, "staged-gone.txt", protocol.GitGroupStaged); h.Unsupported != "deleted" {
		t.Errorf("staged deletion: %+v", h)
	}
	// Invalid shapes and selections.
	writeFile(t, root, "ok.txt", "1\n2\n3\n")
	git("add", "ok.txt")
	git("commit", "-q", "-m", "ok")
	writeFile(t, root, "ok.txt", "1\nTWO\n3\n")
	h := supportedHunks(t, root, "ok.txt", protocol.GitGroupUnstaged)
	ctxLine := lineIndex(t, h, "context", "1")
	add := lineIndex(t, h, "add", "TWO")
	for name, c := range map[string]protocol.Command{
		"context":    partialCmd("bad-1", h, nil, []int{ctxLine}),
		"range":      partialCmd("bad-2", h, nil, []int{99}),
		"hunk":       partialCmd("bad-3", h, []int{1}, nil),
		"duplicate":  partialCmd("bad-4", h, nil, []int{add, add}),
		"empty":      partialCmd("bad-5", h, nil, nil),
		"wrong kind": client.GitPartialCommand("bad-6", gitTarget, protocol.GitHunks{Path: "ok.txt", Group: protocol.GitGroupUntracked, Fingerprint: h.Fingerprint}, []int{0}, nil),
	} {
		_, err := e.command(c)
		wantGitCode(t, err, "invalid")
		notRecorded(t, e, c)
		_ = name
	}
	discard := partialCmd("discard", h, []int{0}, nil)
	discard.Kind, discard.Git.Confirmed = protocol.GitKindDiscard, true
	_, err := e.command(discard)
	wantGitCode(t, err, "not_supported")
	mixed := partialCmd("mixed", h, []int{0}, nil)
	mixed.Git.Paths = []protocol.GitPathPin{{Path: "ok.txt", Group: "unstaged", Pin: "x"}}
	_, err = e.command(mixed)
	wantGitCode(t, err, "invalid")
	// Checkout busy while a thread works there.
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "running"
	e.mu.Unlock()
	busy := partialCmd("busy", h, []int{0}, nil)
	_, err = e.command(busy)
	wantGitCode(t, err, "checkout_busy")
	notRecorded(t, e, busy)
	e.mu.Lock()
	threadByID(&e.snap, "t-git").State = "idle"
	e.mu.Unlock()
	mustGit(t, e, partialCmd("finally", h, []int{0}, nil), protocol.GitStateSucceeded)
}

func TestGitPartialConflictedAndSkipWorktree(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "s.txt", "1\n2\n")
	git("add", ".")
	git("commit", "-m", "base")
	git("checkout", "-q", "-b", "side")
	writeFile(t, root, "c.txt", "side\n")
	git("commit", "-qam", "side")
	git("checkout", "-q", "-")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-qam", "main")
	exec.Command("git", "-C", root, "-c", "user.name=T", "-c", "user.email=t@e", "merge", "side").Run()
	if h := hunksOf(t, root, "c.txt", protocol.GitGroupUnstaged); h.Unsupported != "conflicted" {
		t.Fatalf("conflicted: %+v", h)
	}
	c := partialCmd("conflict", protocol.GitHunks{Path: "c.txt", Group: protocol.GitGroupUnstaged, Fingerprint: "p1-x"}, []int{0}, nil)
	_, err := e.command(c)
	wantGitCode(t, err, "conflicted")
	notRecorded(t, e, c)
	git("merge", "--abort")
	// assume-unchanged hides nothing from the index diff it would drop.
	git("update-index", "--assume-unchanged", "s.txt")
	writeFile(t, root, "s.txt", "1\nTWO\n")
	git("update-index", "--no-assume-unchanged", "s.txt")
	h := supportedHunks(t, root, "s.txt", protocol.GitGroupUnstaged)
	git("update-index", "--assume-unchanged", "s.txt")
	// Status hides the entry, so the selection is stale (and the tag check
	// would refuse it otherwise); the index keeps the committed content.
	_, err = e.command(partialCmd("assume", h, []int{0}, nil))
	if pe, ok := err.(*protocol.Error); !ok || (pe.Code != "stale_diff" && pe.Code != "not_supported") {
		t.Fatalf("assume-unchanged: %v", err)
	}
	if got := git("show", ":s.txt"); got != "1\n2" {
		t.Fatalf("index changed: %q", got)
	}
}

func TestGitPartialStaleSelections(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", numbered(30, nil))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", numbered(30, func(i int) string {
		switch i {
		case 2:
			return "TWO"
		case 25:
			return "TWENTY-FIVE"
		}
		return ""
	}))
	// Worktree edit after the hunks were read.
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	time.Sleep(10 * time.Millisecond)
	writeFile(t, root, "f.txt", numbered(30, func(i int) string {
		switch i {
		case 2:
			return "TWO!"
		case 25:
			return "TWENTY-FIVE"
		}
		return ""
	}))
	c := partialCmd("stale-worktree", h, []int{0}, nil)
	_, err := e.command(c)
	wantGitCode(t, err, "stale_diff")
	notRecorded(t, e, c)
	// Index change after the hunks were read.
	h = supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	git("add", "f.txt")
	c = partialCmd("stale-index", h, []int{0}, nil)
	_, err = e.command(c)
	wantGitCode(t, err, "stale_diff")
	notRecorded(t, e, c)
	// A staged selection goes stale when HEAD's side changes.
	sh := supportedHunks(t, root, "f.txt", protocol.GitGroupStaged)
	git("commit", "-q", "-m", "moved")
	_, err = e.command(partialCmd("stale-head", sh, []int{0}, nil))
	if err == nil {
		t.Fatal("unstage after commit accepted")
	}
}

// TestGitPartialIndexRaceBeforeUpdate is research B1: update-index has no
// compare-and-swap, so an index change after the checks must be caught by
// the last look immediately before it, leaving the other writer's change.
func TestGitPartialIndexRaceBeforeUpdate(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", numbered(30, nil))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", numbered(30, func(i int) string {
		switch i {
		case 2:
			return "TWO"
		case 25:
			return "TWENTY-FIVE"
		}
		return ""
	}))
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	gitPartialBeforeIndexHook = func(top string) { gitIn(t, top, "add", "f.txt") }
	defer func() { gitPartialBeforeIndexHook = nil }()
	r := mustGit(t, e, partialCmd("race", h, []int{0}, nil), protocol.GitStateFailed)
	if r.Git.Code != "stale_diff" {
		t.Fatalf("race: %+v", r.Git)
	}
	gitPartialBeforeIndexHook = nil
	if got := git("show", ":f.txt") + "\n"; !strings.Contains(got, "TWENTY-FIVE") {
		t.Fatal("the concurrent writer's staged content was overwritten")
	}
}

func TestGitPartialRetryRunsOnceAndHooks(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", numbered(30, nil))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", numbered(30, func(i int) string {
		if i == 5 {
			return "FIVE"
		}
		return ""
	}))
	log := filepath.Join(t.TempDir(), "hooks")
	for _, hook := range []string{"post-index-change", "pre-commit", "post-checkout"} {
		writeHook(t, root, hook, "echo "+hook+" >> "+log+"\n")
	}
	var mu sync.Mutex
	updates := 0
	gitWriteArgsHook = func(_ string, args []string) {
		if slices.Contains(args, "update-index") {
			mu.Lock()
			updates++
			mu.Unlock()
		}
	}
	defer func() { gitWriteArgsHook = nil }()
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	c := partialCmd("once", h, []int{0}, nil)
	var wg sync.WaitGroup
	receipts := make([]protocol.Receipt, 3)
	for i := range receipts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.command(c)
			if err != nil {
				t.Error(err)
			}
			receipts[i] = r
		}()
	}
	wg.Wait()
	later := mustGit(t, e, c, protocol.GitStateSucceeded)
	for _, r := range append(receipts, later) {
		if r.State != protocol.GitStateSucceeded || r.Revision != receipts[0].Revision {
			t.Fatalf("receipts differ: %+v", receipts)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if updates != 1 {
		t.Fatalf("update-index ran %d times", updates)
	}
	b, _ := os.ReadFile(log)
	if strings.TrimSpace(string(b)) != "post-index-change" {
		t.Fatalf("hooks run: %q", b)
	}
}

func TestGitPartialNeutralizesDiffConfig(t *testing.T) {
	_, root, git := gitWriteSetup(t)
	// Blank context lines are where diff.suppressBlankEmpty would differ.
	blank := func(s string) string {
		return strings.Replace(strings.Replace(s, "line 4\n", "\n", 1), "line 21\n", "\n", 1)
	}
	writeFile(t, root, "f.txt", blank(numbered(40, nil)))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", blank(numbered(40, func(i int) string {
		switch i {
		case 5, 20, 30:
			return fmt.Sprint("CHANGED ", i)
		}
		return ""
	})))
	plain := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	for _, kv := range [][2]string{
		{"diff.context", "0"}, {"diff.interHunkContext", "20"}, {"diff.algorithm", "patience"},
		{"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}, {"diff.suppressBlankEmpty", "true"},
		{"color.diff", "always"}, {"color.ui", "always"}, {"diff.renames", "copies"}, {"diff.indentHeuristic", "false"},
		{"core.abbrev", "7"},
	} {
		git("config", kv[0], kv[1])
	}
	configured := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	if !reflect.DeepEqual(plain, configured) {
		t.Fatalf("diff config changed the selectable diff:\n%+v\n%+v", plain, configured)
	}
	if len(plain.Hunks) != 3 {
		t.Fatalf("want 3 hunks at -U3: %+v", plain.Hunks)
	}
}

func TestGitPartialLargeFile(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	const n = 20000
	writeFile(t, root, "big.txt", numbered(n, nil))
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "big.txt", numbered(n, func(i int) string {
		if i%10 == 0 {
			return fmt.Sprint("CHANGED ", i)
		}
		return ""
	}))
	start := time.Now()
	h := supportedHunks(t, root, "big.txt", protocol.GitGroupUnstaged)
	var even []int
	for _, hk := range h.Hunks {
		if hk.Index%2 == 0 {
			even = append(even, hk.Index)
		}
	}
	mustGit(t, e, partialCmd("big", h, even, nil), protocol.GitStateSucceeded)
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("partial staging of %d hunks took %s", len(h.Hunks), d)
	}
	got := git("show", ":big.txt") + "\n"
	staged := map[int]bool{}
	for _, idx := range even {
		for _, l := range h.Hunks[idx].Lines {
			if l.Kind == "add" {
				staged[l.NewLine] = true
			}
		}
	}
	want := numbered(n, func(i int) string {
		if staged[i] {
			return fmt.Sprint("CHANGED ", i)
		}
		return ""
	})
	if got != want {
		t.Fatal("large file staged content differs")
	}
}

func TestGitHunksHandler(t *testing.T) {
	root, git := gitFixture(t)
	git("init")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "a.txt", "b\n")
	e := &engine{snap: protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: root}}}}
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"project_id=p&path=a.txt&group=unstaged", 200}, {"project_id=p&path=a.txt&group=staged", 404},
		{"project_id=p&path=a.txt&group=untracked", 400}, {"project_id=p&path=../a&group=unstaged", 400},
		{"project_id=p&path=b.txt&group=unstaged", 404},
	} {
		w := httptest.NewRecorder()
		e.gitHunks(w, httptest.NewRequest("GET", "/v1/git/hunks?"+tc.query, nil))
		if w.Code != tc.status {
			t.Errorf("%s: status %d: %s", tc.query, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var h protocol.GitHunks
			if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil || h.Fingerprint == "" || h.LineCount != 2 {
				t.Errorf("reply: %+v %v", h, err)
			}
		}
	}
}

// TestGitPartialIntentToAddToggleBeforeUpdate: an empty file and an
// intent-to-add entry have identical `ls-files -s -v` records, so the last
// look must also compare the intent-to-add flag (review c).
func TestGitPartialIntentToAddToggleBeforeUpdate(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", "")
	writeFile(t, root, "g.txt", "g\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", "a\nb\n")
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	gitPartialBeforeIndexHook = func(top string) {
		gitIn(t, top, "rm", "--cached", "-q", "f.txt")
		gitIn(t, top, "add", "-N", "f.txt")
	}
	defer func() { gitPartialBeforeIndexHook = nil }()
	r := mustGit(t, e, partialCmd("ita-toggle", h, nil, []int{lineIndex(t, h, "add", "a")}), protocol.GitStateFailed)
	if r.Git.Code != "stale_diff" {
		t.Fatalf("toggle: %+v", r.Git)
	}
	gitPartialBeforeIndexHook = nil
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _, err := readIndexRecord(context.Background(), g, "f.txt"); err != nil || !strings.HasSuffix(string(rec), "ita=true") {
		t.Fatalf("the other writer's intent-to-add entry was replaced: %q %v", rec, err)
	}
}

// TestGitPartialCRLFInIndexWithAutocrlf: the index blob already has CRLF, so
// Git keeps CRLF under core.autocrlf=true; partial and whole-file staging
// must follow that rule instead of refusing or reporting newer content
// (review d).
func TestGitPartialCRLFInIndexWithAutocrlf(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	edit := func(i int) string {
		if i == 2 || i == 18 {
			return "CH"
		}
		return ""
	}
	writeFile(t, root, "f.txt", crlf(numbered(20, nil)))
	writeFile(t, root, "w.txt", crlf(numbered(20, nil)))
	git("add", ".")
	git("commit", "-m", "base")
	git("config", "core.autocrlf", "true")
	writeFile(t, root, "f.txt", crlf(numbered(20, edit)))
	writeFile(t, root, "w.txt", crlf(numbered(20, edit)))
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	before := worktreeFiles(t, root)
	mustGit(t, e, partialCmd("crlf-index", h, []int{0}, nil), protocol.GitStateSucceeded)
	assertWorktreeUnchanged(t, root, before)
	out, _ := exec.Command("git", "-C", root, "cat-file", "blob", ":f.txt").Output()
	want := crlf(numbered(20, func(i int) string {
		if i == 2 {
			return "CH"
		}
		return ""
	}))
	if string(out) != want {
		t.Fatalf("index %q", out)
	}
	// Whole-file stage of the same situation: no false staged_newer_content.
	r := mustGit(t, e, client.GitStageCommand("crlf-whole", gitTarget, entryOf(t, root, "w.txt", protocol.GitGroupUnstaged)), protocol.GitStateSucceeded)
	if r.Git.Code != "" {
		t.Fatalf("whole-file stage: %+v", r.Git)
	}
	if out, _ := exec.Command("git", "-C", root, "cat-file", "blob", ":w.txt").Output(); string(out) != crlf(numbered(20, edit)) {
		t.Fatalf("whole-file index %q", out)
	}
}

func TestGitPartialSelectionSizeAndCapability(t *testing.T) {
	e, root, git := gitWriteSetup(t)
	writeFile(t, root, "f.txt", "a\n")
	git("add", ".")
	git("commit", "-m", "base")
	writeFile(t, root, "f.txt", "b\n")
	h := supportedHunks(t, root, "f.txt", protocol.GitGroupUnstaged)
	lines := make([]int, gitPartialMaxSelection+1)
	for i := range lines {
		lines[i] = i
	}
	c := partialCmd("huge", h, nil, lines)
	_, err := e.command(c)
	wantGitCode(t, err, "too_large")
	notRecorded(t, e, c)
	srv, stop := startTestServer(t, t.TempDir())
	defer stop()
	snap, err := srv.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(snap.Capabilities, "git-partial-stage") != gitPartialSupported() || !gitPartialSupported() {
		t.Fatalf("capability does not follow the Git version (%v): %v", gitPartialSupported(), snap.Capabilities)
	}
}
