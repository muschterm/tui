package server

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// noIndexDiff diffs two byte strings with the partial-staging options.
func noIndexDiff(t *testing.T, dir string, old, new []byte) []byte {
	t.Helper()
	a, b := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	if err := os.WriteFile(a, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, new, 0o644); err != nil {
		t.Fatal(err)
	}
	args := slices.Clone(gitPartialDiffOptions)
	i := slices.Index(args, "diff")
	args = slices.Insert(args, i+1, "--no-index")
	cmd := exec.Command("git", append(args, "--", a, b)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); err != nil && !(ok && exit.ExitCode() == 1) {
		t.Fatalf("git diff --no-index: %v", err)
	}
	return out
}

// oraclePatch renders the selection as an ordinary patch against the
// hunks' pre-image, independently of applySelection: in each change block
// every old line is removed, then the kept old lines before the first
// selected removal, the selected new lines and the remaining kept old lines
// are added in that order. git apply then produces the oracle result.
func oraclePatch(hunks []diffHunk, sel map[int]bool) []byte {
	var b bytes.Buffer
	b.WriteString("--- a/f\n+++ b/f\n")
	for _, h := range hunks {
		var body bytes.Buffer
		for i := 0; i < len(h.lines); {
			if h.lines[i].kind == ' ' {
				fmt.Fprintf(&body, " %s\n", h.lines[i].text)
				i++
				continue
			}
			var olds [][]byte
			var oldSel []bool
			var adds [][]byte
			for ; i < len(h.lines) && h.lines[i].kind != ' '; i++ {
				l := h.lines[i]
				if l.kind == '-' {
					fmt.Fprintf(&body, "-%s\n", l.text)
					olds, oldSel = append(olds, l.text), append(oldSel, sel[h.first+i])
				} else if sel[h.first+i] {
					adds = append(adds, l.text)
				}
			}
			cut := slices.Index(oldSel, true)
			if cut < 0 {
				cut = len(olds)
			}
			var lines [][]byte
			for k := 0; k < cut; k++ {
				lines = append(lines, olds[k])
			}
			lines = append(lines, adds...)
			for k := cut; k < len(olds); k++ {
				if !oldSel[k] {
					lines = append(lines, olds[k])
				}
			}
			for _, a := range lines {
				fmt.Fprintf(&body, "+%s\n", a)
			}
		}
		// --recount fixes the counts; the start is the pre-image's.
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldLines, h.oldStart, 1)
		b.Write(body.Bytes())
	}
	return b.Bytes()
}

func gitApplyOracle(t *testing.T, dir string, base, patch []byte) []byte {
	t.Helper()
	work := filepath.Join(dir, "apply")
	os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "f"), base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.patch"), patch, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "apply", "--recount", "--unidiff-zero", filepath.Join(dir, "p.patch"))
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply oracle: %v %s\n%s", err, out, patch)
	}
	got, err := os.ReadFile(filepath.Join(work, "f"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func randomText(r *rand.Rand, lines []string, noNL bool) []byte {
	s := strings.Join(lines, "\n")
	if len(lines) > 0 && !noNL {
		s += "\n"
	}
	return []byte(s)
}

func randomLineEdit(r *rand.Rand) (old, new []string) {
	alphabet := []string{"a", "b", "c", "d", "", "x y", "\tz"}
	n := r.IntN(14)
	for i := 0; i < n; i++ {
		line := alphabet[r.IntN(len(alphabet))] + fmt.Sprint(r.IntN(3))
		if r.IntN(6) == 0 {
			line = "" // blank lines (diff.suppressBlankEmpty is pinned off)
		}
		old = append(old, line)
	}
	for i := 0; i <= len(old); i++ {
		if r.IntN(4) == 0 {
			for k := r.IntN(3) + 1; k > 0; k-- {
				new = append(new, fmt.Sprintf("N%d", r.IntN(100)))
			}
		}
		if i == len(old) {
			break
		}
		switch r.IntN(5) {
		case 0: // delete
		case 1: // replace
			new = append(new, "R"+old[i])
		default:
			new = append(new, old[i])
		}
	}
	return old, new
}

func changeLines(hunks []diffHunk) []int {
	var out []int
	for _, h := range hunks {
		for j, l := range h.lines {
			if l.kind != ' ' {
				out = append(out, h.first+j)
			}
		}
	}
	return out
}

// TestPartialSelectionMatchesOracle compares applySelection with git apply
// of an independently built patch over random small diffs and random line
// selections, for a stage selection and for the complement an unstage
// applies to HEAD.
func TestPartialSelectionMatchesOracle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(20260926, 1))
	cases := 0
	for iter := 0; iter < 400; iter++ {
		o, n := randomLineEdit(r)
		old, new := randomText(r, o, false), randomText(r, n, false)
		diff := noIndexDiff(t, dir, old, new)
		hunks, err := parseHunks(diff)
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, diff)
		}
		changes := changeLines(hunks)
		if len(changes) == 0 {
			continue
		}
		cases++
		sel := map[int]bool{}
		for _, i := range changes {
			if r.IntN(2) == 0 {
				sel[i] = true
			}
		}
		complement := map[int]bool{}
		for _, i := range changes {
			complement[i] = !sel[i]
		}
		for _, dirn := range []struct {
			name string
			sel  map[int]bool
		}{{"stage", sel}, {"unstage", complement}} {
			got, err := applySelection(splitBlob(old), hunks, func(i int) bool { return dirn.sel[i] })
			if err != nil {
				t.Fatalf("%s: %v\n%s", dirn.name, err, diff)
			}
			want := gitApplyOracle(t, dir, old, oraclePatch(hunks, dirn.sel))
			if !bytes.Equal(joinBlob(got), want) {
				t.Fatalf("%s selection %v\nold %q\nnew %q\ngot  %q\nwant %q\n%s", dirn.name, sel, old, new, joinBlob(got), want, diff)
			}
		}
	}
	if cases < 200 {
		t.Fatalf("only %d non-trivial cases", cases)
	}
}

// TestPartialSelectionInvariants covers missing final newlines, where the
// oracle patch cannot be written simply: selecting everything yields the
// other side exactly, and selecting nothing yields the base.
func TestPartialSelectionInvariants(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(7, 26))
	for iter := 0; iter < 300; iter++ {
		o, n := randomLineEdit(r)
		old, new := randomText(r, o, r.IntN(2) == 0), randomText(r, n, r.IntN(2) == 0)
		diff := noIndexDiff(t, dir, old, new)
		hunks, err := parseHunks(diff)
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, diff)
		}
		all := func(int) bool { return true }
		none := func(int) bool { return false }
		for _, c := range []struct {
			base, want []byte
			hunks      []diffHunk
			sel        func(int) bool
		}{
			{old, new, hunks, all}, {old, old, hunks, none},
		} {
			got, err := applySelection(splitBlob(c.base), c.hunks, c.sel)
			if err != nil {
				t.Fatalf("%v\n%s", err, diff)
			}
			if !bytes.Equal(joinBlob(got), c.want) {
				t.Fatalf("got %q want %q\n%s", joinBlob(got), c.want, diff)
			}
		}
		// A random selection keeps a well-formed blob: only its last line
		// may lack a newline, and the line multiset is exactly base minus
		// selected deletions plus selected additions.
		changes := changeLines(hunks)
		sel := map[int]bool{}
		for _, i := range changes {
			sel[i] = r.IntN(2) == 0
		}
		got, err := applySelection(splitBlob(old), hunks, func(i int) bool { return sel[i] })
		if err != nil {
			t.Fatal(err)
		}
		count := map[string]int{}
		for _, l := range splitBlob(old) {
			count[string(l.text)]++
		}
		for _, h := range hunks {
			for j, l := range h.lines {
				if sel[h.first+j] && l.kind == '-' {
					count[string(l.text)]--
				}
				if sel[h.first+j] && l.kind == '+' {
					count[string(l.text)]++
				}
			}
		}
		for _, l := range splitBlob(joinBlob(got)) {
			count[string(l.text)]--
		}
		for k, v := range count {
			if v != 0 {
				t.Fatalf("line %q off by %d\n%s", k, v, diff)
			}
		}
	}
}

func TestPartialSelectionBlockOrderAndNoNewline(t *testing.T) {
	// "-l4 -l5 +L4 +L5" (research A2).
	base := splitBlob([]byte("l3\nl4\nl5\nl6\n"))
	h := []diffHunk{{oldStart: 1, oldLines: 4, newStart: 1, newLen: 4, lines: []diffLine{
		{kind: ' ', text: []byte("l3")}, {kind: '-', text: []byte("l4")}, {kind: '-', text: []byte("l5")},
		{kind: '+', text: []byte("L4")}, {kind: '+', text: []byte("L5")}, {kind: ' ', text: []byte("l6")},
	}}}
	for _, c := range []struct {
		sel  []int
		want string
	}{
		{[]int{1, 3}, "l3\nL4\nl5\nl6\n"},
		{[]int{3}, "l3\nl4\nl5\nL4\nl6\n"},
		{[]int{2, 4}, "l3\nl4\nL5\nl6\n"},
		{[]int{2, 3}, "l3\nl4\nL4\nl6\n"},
		{[]int{1}, "l3\nl5\nl6\n"},
		{[]int{1, 2, 3, 4}, "l3\nL4\nL5\nl6\n"},
	} {
		got, err := applySelection(base, h, func(i int) bool { return slices.Contains(c.sel, i) })
		if err != nil || string(joinBlob(got)) != c.want {
			t.Fatalf("sel %v: got %q (%v), want %q", c.sel, joinBlob(got), err, c.want)
		}
	}
	// "a\nb" (no final newline) -> "a\nb\nc" (no final newline): adding c
	// alone appends it and gives b a newline because it is no longer last.
	diff := []byte("--- a/f\n+++ b/f\n@@ -1,2 +1,3 @@\n a\n-b\n\\ No newline at end of file\n+b\n+c\n\\ No newline at end of file\n")
	hunks, err := parseHunks(diff)
	if err != nil {
		t.Fatal(err)
	}
	got, err := applySelection(splitBlob([]byte("a\nb")), hunks, func(i int) bool { return i == 3 })
	if err != nil || string(joinBlob(got)) != "a\nb\nc" {
		t.Fatalf("got %q %v", joinBlob(got), err)
	}
	got, err = applySelection(splitBlob([]byte("a\nb")), hunks, func(i int) bool { return i == 1 || i == 2 })
	if err != nil || string(joinBlob(got)) != "a\nb\n" {
		t.Fatalf("got %q %v", joinBlob(got), err)
	}
	// A diff that does not describe the base is refused.
	if _, err := applySelection(splitBlob([]byte("a\nX")), hunks, func(int) bool { return true }); err == nil {
		t.Fatal("mismatched base accepted")
	}
}

func TestParseHunksRejectsMalformed(t *testing.T) {
	for _, d := range []string{
		"@@ -1,2 +1,2 @@\n a\n",                                 // short
		"@@ -1 +1 @@\n-a\n+b\n c\n",                             // long (c starts no hunk)
		"@@ -1 +1 @@\n-a\n\\ x\n\\ y\n+b\n",                     // doubled marker
		"@@ -1 +1 @@\n-a\n+b",                                   // no final newline
		"diff --git a/x b/x\nBinary files a/x and b/x differ\n", // binary
		"@@ -3 +3 @@\n-a\n+b\n@@ -1 +1 @@\n-c\n+d\n",            // out of order
	} {
		if _, err := parseHunks([]byte(d)); err == nil {
			t.Fatalf("accepted %q", d)
		}
	}
}

// TestPartialSelectionNewlineOnlyPair documents review h: a line that only
// gained its final newline is a delete/add pair with equal text, and
// selecting one side of it is applied literally.
func TestPartialSelectionNewlineOnlyPair(t *testing.T) {
	// HEAD "a\nb" -> index "a\nb\nc"; unstaging only "-b" applies the
	// unselected "+b" and "+c" to HEAD, keeping the old b as well.
	diff := []byte("@@ -1,2 +1,3 @@\n a\n-b\n\\ No newline at end of file\n+b\n+c\n\\ No newline at end of file\n")
	hunks, err := parseHunks(diff)
	if err != nil {
		t.Fatal(err)
	}
	unstage := map[int]bool{1: true}
	got, err := applySelection(splitBlob([]byte("a\nb")), hunks, func(i int) bool { return !unstage[i] })
	if err != nil || string(joinBlob(got)) != "a\nb\nb\nc" {
		t.Fatalf("got %q %v", joinBlob(got), err)
	}
}
