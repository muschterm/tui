package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/doc"
)

// runeOffsets returns the UTF-16 offset of every rune boundary of r's text.
func runeOffsets(r *docReplica) []int {
	out := []int{0}
	u := 0
	for _, ru := range r.txt.String() {
		u++
		if ru >= 0x10000 {
			u++
		}
		out = append(out, u)
	}
	return out
}

func mustStep(t *testing.T, d *doc.Doc, r *docReplica, what string) {
	t.Helper()
	for _, u := range r.takeUpdates() {
		if _, err := d.ApplyClient(u, func(c uint64) bool { return c == r.id }, nil); err != nil {
			t.Fatalf("%s: server refused: %v", what, err)
		}
	}
	got := r.text.ToString()
	if d.Text() != r.txt.String() || got != r.txt.String() || !utf8.ValidString(got) || strings.ContainsRune(got, '�') {
		t.Fatalf("%s: diverged: server %q model %q replica %q", what, d.Text(), r.txt.String(), got)
	}
}

func TestReplicaUndoReposPureAndChain(t *testing.T) {
	// Insert "xy"@1, insert "b"@3, undo, insert "a"@3 (ygo's UndoManager
	// put the "a" elsewhere).
	d := serverDoc(t, "abXcd")
	r := replicaFrom(t, d)
	for _, e := range []struct {
		at   int
		text string
	}{{1, "xy"}, {3, "b"}} {
		if err := r.edit(e.at, 0, e.text, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.undoStep(false); err != nil {
		t.Fatal(err)
	}
	if err := r.edit(3, 0, "a", false); err != nil {
		t.Fatal(err)
	}
	mustStep(t, d, r, "stale index")
	if got := r.txt.String(); got != "axyabXcd" {
		t.Fatalf("got %q", got)
	}
	// Type a, delete it, undo, undo: the restored "a" belongs to the first
	// step and goes too.
	d = serverDoc(t, "xy")
	r = replicaFrom(t, d)
	for _, f := range []func() error{
		func() error { return r.edit(1, 0, "a", false) },
		func() error { return r.edit(1, 1, "", false) },
		func() error { _, err := r.undoStep(false); return err },
		func() error { _, err := r.undoStep(false); return err },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
		mustStep(t, d, r, "chain")
	}
	if got := r.txt.String(); got != "xy" || r.canUndo(false) {
		t.Fatalf("chain: %q", got)
	}
	// Redo both steps back.
	for range 2 {
		if _, err := r.undoStep(true); err != nil {
			t.Fatal(err)
		}
		mustStep(t, d, r, "redo chain")
	}
	if got := r.txt.String(); got != "xy" {
		t.Fatalf("redo chain: %q", got)
	}
	// Astral characters: replace an emoji with an emoji, undo, type, undo,
	// then edit across the emoji.
	d = serverDoc(t, "ab😀cd\n")
	r = replicaFrom(t, d)
	steps := []func() error{
		func() error { return r.edit(2, 2, "😀", false) },
		func() error { _, err := r.undoStep(false); return err },
		func() error { return r.edit(2, 0, "a", false) },
		func() error { _, err := r.undoStep(false); return err },
		func() error { return r.edit(2, 0, "Q", false) },
		func() error { return r.edit(1, 4, "a", false) },
	}
	for i, f := range steps {
		if err := f(); err != nil {
			t.Fatalf("astral step %d: %v", i, err)
		}
		mustStep(t, d, r, fmt.Sprint("astral step ", i))
	}
	if got := r.txt.String(); got != "aacd\n" {
		t.Fatalf("astral: %q", got)
	}
}

// TestReplicaUndoOracleFuzz interleaves local edits, undo, redo and remote
// (server) edits with astral, CJK, combining and newline text. Every update
// must pass the server's validation, all three texts must agree and stay
// valid, remote text is never removed by undo or redo, and without remote
// edits undo and redo reproduce the exact earlier texts.
func TestReplicaUndoOracleFuzz(t *testing.T) {
	seeds := 1500
	if testing.Short() {
		seeds = 200
	}
	texts := []string{"a", "b", "😀", "👨‍👩‍👧", "漢字", "\n", "xy", "é", "\t"}
	steps, undos, redos, remotes := 0, 0, 0, 0
	for seed := int64(0); seed < int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		withRemote := seed%2 == 1
		d := serverDoc(t, "hello 😀 world\nline 漢 two\n")
		r := replicaFrom(t, d)
		type entry struct{ before, after string }
		var undoModel, redoModel []entry
		remoteS := 0
		var log []string
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nlog:\n%s", seed, fmt.Sprintf(format, args...), strings.Join(log, "\n"))
		}
		for step := 0; step < 50; step++ {
			steps++
			before := r.txt.String()
			switch k := rng.Intn(10); {
			case k < 5:
				offs := runeOffsets(r)
				i := rng.Intn(len(offs))
				j := min(len(offs)-1, i+rng.Intn(4))
				text := ""
				if rng.Intn(3) > 0 {
					text = texts[rng.Intn(len(texts))]
				}
				if offs[j] == offs[i] && text == "" {
					continue
				}
				// Local edits never remove remote text, so its count is exact.
				rs := []rune(before)
				if strings.Contains(string(rs[i:j]), "S") {
					continue
				}
				join := rng.Intn(3) == 0 && len(undoModel) > 0
				log = append(log, fmt.Sprintf("edit(%d,%d,%q,%v) on %q", offs[i], offs[j]-offs[i], text, join, before))
				if err := r.edit(offs[i], offs[j]-offs[i], text, join); err != nil {
					fail("edit: %v", err)
				}
				want := string(rs[:i]) + text + string(rs[j:])
				if got := r.txt.String(); got != want {
					fail("misplaced edit: want %q got %q", want, got)
				}
				if join {
					undoModel[len(undoModel)-1].after = want
				} else {
					undoModel = append(undoModel, entry{before, want})
				}
				redoModel = nil
			case k < 7:
				undos++
				log = append(log, "undo")
				if _, err := r.undoStep(false); err != nil {
					fail("undo: %v", err)
				}
				if n := len(undoModel); n > 0 {
					e := undoModel[n-1]
					undoModel = undoModel[:n-1]
					redoModel = append(redoModel, e)
					if !withRemote && r.txt.String() != e.before {
						fail("undo: want %q got %q", e.before, r.txt.String())
					}
				}
			case k < 9:
				redos++
				log = append(log, "redo")
				if _, err := r.undoStep(true); err != nil {
					fail("redo: %v", err)
				}
				if n := len(redoModel); n > 0 {
					e := redoModel[n-1]
					redoModel = redoModel[:n-1]
					undoModel = append(undoModel, e)
					if !withRemote && r.txt.String() != e.after {
						fail("redo: want %q got %q", e.after, r.txt.String())
					}
				}
			default:
				if !withRemote {
					continue
				}
				remotes++
				s := d.Text()
				var starts []int
				for i := range s {
					starts = append(starts, i)
				}
				starts = append(starts, len(s))
				at := starts[rng.Intn(len(starts))]
				edit := doc.Edit{Start: at, End: at, Text: "S"}
				if rng.Intn(3) == 0 {
					// A remote deletion of one character that is not remote text.
					if at < len(s) && s[at] != 'S' {
						_, n := utf8.DecodeRuneInString(s[at:])
						edit = doc.Edit{Start: at, End: at + n}
					}
				}
				if edit.Text == "S" {
					remoteS++
				}
				log = append(log, fmt.Sprintf("remote %+v on %q", edit, s))
				u, err := d.Replace([]doc.Edit{edit})
				if err != nil {
					fail("server edit: %v", err)
				}
				if _, err := r.applyRemote(u, nil); err != nil {
					fail("remote: %v", err)
				}
			}
			for _, u := range r.takeUpdates() {
				if _, err := d.ApplyClient(u, func(c uint64) bool { return c == r.id }, nil); err != nil {
					fail("server refused: %v", err)
				}
			}
			got := r.text.ToString()
			if d.Text() != r.txt.String() || got != r.txt.String() || !utf8.ValidString(got) || strings.ContainsRune(got, '�') {
				fail("diverged: server %q model %q replica %q", d.Text(), r.txt.String(), got)
			}
			if n := strings.Count(got, "S"); n != remoteS {
				fail("remote text touched: %d S, want %d in %q", n, remoteS, got)
			}
		}
	}
	t.Logf("%d seeds, %d steps (%d undo, %d redo, %d remote)", seeds, steps, undos, redos, remotes)
}
