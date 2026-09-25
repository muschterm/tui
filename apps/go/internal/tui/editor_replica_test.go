package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/reearth/ygo/crdt"
)

// serverDoc is the server's validating document wrapper; every update a
// replica produces must pass its checks.
func serverDoc(t *testing.T, text string) *doc.Doc {
	t.Helper()
	d, _, err := doc.New(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func replicaFrom(t *testing.T, d *doc.Doc) *docReplica {
	t.Helper()
	r, err := newDocReplica(newReplicaID(), d.EncodeState())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.close)
	return r
}

// sendAll applies the replica's pending updates to the server document,
// as the server would validate them.
func sendAll(t *testing.T, d *doc.Doc, r *docReplica) {
	t.Helper()
	for _, u := range r.takeUpdates() {
		if _, err := d.ApplyClient(u, func(c uint64) bool { return c == r.id }, nil); err != nil {
			t.Fatalf("server refused update: %v", err)
		}
	}
}

func TestEdTextUnicodeOffsets(t *testing.T) {
	// Emoji (surrogate pair), CJK (wide), combining mark and a ZWJ family.
	s := "a😀b\n漢字\ne\u0301x\n👨\u200d👩\u200d👧z"
	tx := newEdText(s)
	if tx.Len() != u16Len(s) || tx.String() != s {
		t.Fatalf("len %d want %d", tx.Len(), u16Len(s))
	}
	for u := 0; u <= tx.Len(); u++ {
		p := tx.posAt(u)
		if back := tx.offset(p); back > u || u-back > 1 {
			t.Fatalf("u %d → %v → %d", u, p, back)
		}
	}
	// Inside the surrogate pair lands on the emoji's start.
	if p := tx.posAt(2); p != (edPos{0, 1}) {
		t.Fatalf("surrogate split: %v", p)
	}
	if p := tx.posAt(3); p != (edPos{0, 5}) {
		t.Fatalf("after emoji: %v", p)
	}
	// Columns: 漢字 is four cells; e+combining is one grapheme.
	if w := lineWidth("漢字"); w != 4 {
		t.Fatalf("CJK width %d", w)
	}
	if got := graphemeAfter("e\u0301x", 0); got != 3 {
		t.Fatalf("combining grapheme end %d", got)
	}
	if got := graphemeBefore("👨\u200d👩\u200d👧z", len("👨\u200d👩\u200d👧")); got != 0 {
		t.Fatalf("zwj grapheme start %d", got)
	}
	if got := snapGrapheme("e\u0301x", 1); got != 0 {
		t.Fatalf("snap inside cluster %d", got)
	}
	if got := byteAtCol("漢字", 3); got != len("漢字") {
		// Column 3 is the right half of 字: rounds to its end.
		t.Fatalf("byteAtCol %d", got)
	}
}

func TestEdTextSanitizesCells(t *testing.T) {
	var shown strings.Builder
	walkCells("a\x1b[31m\tb\x7f\u202e\u200bc\x00", func(c edCell) bool { shown.WriteString(c.text); return true })
	got := shown.String()
	if strings.ContainsAny(got, "\x1b\x7f\u202e\u200b\x00") {
		t.Fatalf("raw control painted: %q", got)
	}
	if !strings.Contains(got, "␛[31m") || !strings.Contains(got, "␡") || !strings.Contains(got, "␀") {
		t.Fatalf("controls not visible: %q", got)
	}
	// "a" + "␛[31m" is six cells; the tab stops at column 8.
	if w := lineWidth("a\x1b[31m\tb"); w != 9 {
		t.Fatalf("tab width %d", w)
	}
}

func TestEdTextDeltaAppliesAcrossLines(t *testing.T) {
	tx := newEdText("one\ntwo\nthree")
	tx.apply(deltaEdits([]crdt.Delta{{Op: crdt.DeltaOpRetain, Retain: 2}, {Op: crdt.DeltaOpDelete, Delete: 4}, {Op: crdt.DeltaOpInsert, Insert: "X\nY"}, {Op: crdt.DeltaOpRetain, Retain: 3}, {Op: crdt.DeltaOpInsert, Insert: "!"}}))
	if got := tx.String(); got != "onX\nYo\nt!hree" {
		t.Fatalf("got %q", got)
	}
	if tx.Len() != u16Len(tx.String()) {
		t.Fatal("u16 cache wrong")
	}
}

func TestReplicaEditsPassServerValidation(t *testing.T) {
	d := serverDoc(t, "hello\nworld\n")
	r := replicaFrom(t, d)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.edit(5, 0, " 😀", false))
	must(r.edit(r.txt.offset(edPos{1, 0}), 0, "漢", true))
	must(r.edit(0, 1, "H", false)) // replace in one transaction
	must(r.edit(2, 3, "", false))  // delete
	sendAll(t, d, r)
	if d.Text() != r.txt.String() || r.txt.String() != "He 😀\n漢world\n" {
		t.Fatalf("server %q replica %q", d.Text(), r.txt.String())
	}
	// Undo and redo updates are accepted too.
	if _, err := r.undoStep(false); err != nil {
		t.Fatal(err)
	}
	sendAll(t, d, r)
	if _, err := r.undoStep(true); err != nil {
		t.Fatal(err)
	}
	sendAll(t, d, r)
	if d.Text() != r.txt.String() {
		t.Fatalf("server %q replica %q", d.Text(), r.txt.String())
	}
}

func TestReplicaUndoKeepsRemoteEdits(t *testing.T) {
	d := serverDoc(t, "abc")
	r := replicaFrom(t, d)
	other := replicaFrom(t, d)
	if err := r.edit(3, 0, "XYZ", false); err != nil {
		t.Fatal(err)
	}
	sendAll(t, d, r)
	for _, u := range r.takeUpdates() {
		_ = u
	}
	// Another client edits between our edit and our undo.
	_, _ = other.applyRemote(d.EncodeState(), nil)
	if err := other.edit(0, 0, ">", false); err != nil {
		t.Fatal(err)
	}
	for _, u := range other.takeUpdates() {
		if _, err := d.ApplyClient(u, func(c uint64) bool { return c == other.id }, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := r.applyRemote(u, nil); err != nil {
			t.Fatal(err)
		}
	}
	if r.txt.String() != ">abcXYZ" {
		t.Fatalf("got %q", r.txt.String())
	}
	if _, err := r.undoStep(false); err != nil {
		t.Fatal(err)
	}
	sendAll(t, d, r)
	if r.txt.String() != ">abc" || d.Text() != ">abc" {
		t.Fatalf("undo removed remote work: replica %q server %q", r.txt.String(), d.Text())
	}
	if r.canUndo(false) {
		t.Fatal("remote edit became undoable")
	}
}

func TestReplicaRemoteKeepsCursor(t *testing.T) {
	d := serverDoc(t, "line one\nline two\n")
	r := replicaFrom(t, d)
	cursor := r.txt.offset(edPos{1, 5}) // before "two"
	// The server merges an external change above and inside the line.
	u1, err := d.Replace([]doc.Edit{{Start: 0, End: 0, Text: "new 😀 first\n"}})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := r.applyRemote(u1, []int{cursor})
	if err != nil {
		t.Fatal(err)
	}
	if p := r.txt.posAt(keep[0]); p != (edPos{2, 5}) {
		t.Fatalf("cursor moved to %v", p)
	}
	u2, err := d.Replace([]doc.Edit{{Start: len("new 😀 first\nline "), End: len("new 😀 first\nline "), Text: "漢"}})
	if err != nil {
		t.Fatal(err)
	}
	keep, err = r.applyRemote(u2, keep)
	if err != nil {
		t.Fatal(err)
	}
	if r.txt.String() != d.Text() {
		t.Fatalf("replica %q server %q", r.txt.String(), d.Text())
	}
	// Text inserted at the cursor lands before it (the cursor sticks to "t").
	if p := r.txt.posAt(keep[0]); r.txt.lines[p.line][p.col:] != "two" {
		t.Fatalf("cursor at %v in %q", p, r.txt.lines[p.line])
	}
}
