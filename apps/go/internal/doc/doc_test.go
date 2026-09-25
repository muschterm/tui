package doc

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/reearth/ygo/crdt"
)

// replica is a client-side replica as the TUI would hold one.
type replica struct {
	doc  *crdt.Doc
	text *crdt.YText
}

func newReplica(t testing.TB, id uint64, state []byte) *replica {
	t.Helper()
	d := crdt.New(crdt.WithClientID(crdt.ClientID(id)))
	if state != nil {
		if err := crdt.ApplyUpdateV1(d, state, nil); err != nil {
			t.Fatal(err)
		}
	}
	return &replica{doc: d, text: d.GetText(TextName)}
}

func (r *replica) edit(fn func(*crdt.Transaction)) []byte {
	var out []byte
	origin := &struct{ _ byte }{}
	unsub := r.doc.OnUpdate(func(u []byte, o any) {
		if o == origin {
			out = append([]byte(nil), u...)
		}
	})
	r.doc.Transact(fn, origin)
	unsub()
	return out
}

func (r *replica) insert(i int, s string) []byte {
	return r.edit(func(txn *crdt.Transaction) { r.text.Insert(txn, i, s, nil) })
}

func (r *replica) delete(i, n int) []byte {
	return r.edit(func(txn *crdt.Transaction) { r.text.Delete(txn, i, n) })
}

func only(id uint64) func(uint64) bool { return func(c uint64) bool { return c == id } }

func reason(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Reason
	}
	return ""
}

func TestDocClientUpdateAndRestart(t *testing.T) {
	d, snap, err := New("hello\nworld\n")
	if err != nil {
		t.Fatal(err)
	}
	c := newReplica(t, 7, snap)
	u1 := c.insert(5, ", there")
	u2 := c.delete(0, 1)
	u3 := c.insert(0, "H😀")
	var log [][]byte
	for _, u := range [][]byte{u1, u2, u3} {
		a, err := d.ApplyClient(u, only(7), nil)
		if err != nil || !a.Changed || len(a.Clients) > 1 {
			t.Fatalf("apply: %+v %v", a, err)
		}
		log = append(log, u)
	}
	want := "H😀ello, there\nworld\n"
	if d.Text() != want || c.text.ToString() != want {
		t.Fatalf("text %q replica %q", d.Text(), c.text.ToString())
	}
	// A duplicate (retry) changes nothing.
	if a, err := d.ApplyClient(u1, only(7), nil); err != nil || a.Changed {
		t.Fatalf("duplicate: %+v %v", a, err)
	}
	// Restart: snapshot plus log reproduces the text with a fresh server ID.
	r, err := Load(snap, log)
	if err != nil || r.Text() != want {
		t.Fatalf("load %q %v", r.Text(), err)
	}
	if r.ServerClientID() == d.ServerClientID() {
		t.Fatal("server client ID reused across loads")
	}
	// Compaction: the encoded state alone is equivalent.
	r2, err := Load(r.EncodeState(), nil)
	if err != nil || r2.Text() != want {
		t.Fatalf("compacted %q %v", r2.Text(), err)
	}
}

func TestDocRejectsWithoutChangingState(t *testing.T) {
	d, snap, _ := New("a😀b")
	before := d.Text()
	sv := d.StateVector()
	cases := []struct {
		name, want string
		update     func() []byte
		allowed    func(uint64) bool
		check      Check
	}{
		{"surrogate insert", ReasonSurrogate, func() []byte { return newReplica(t, 9, snap).insert(2, "x") }, only(9), nil},
		{"surrogate delete", ReasonSurrogate, func() []byte { return newReplica(t, 9, snap).delete(2, 1) }, only(9), nil},
		{"foreign origin", ReasonOrigin, func() []byte { return newReplica(t, 10, snap).insert(0, "x") }, only(9), nil},
		{"server origin", ReasonOrigin, func() []byte {
			return newReplica(t, d.ServerClientID(), snap).insert(0, "x")
		}, func(uint64) bool { return true }, nil},
		{"format", ReasonContent, func() []byte {
			r := newReplica(t, 9, snap)
			return r.edit(func(txn *crdt.Transaction) { r.text.Format(txn, 0, 1, crdt.Attributes{"bold": true}) })
		}, only(9), nil},
		{"embed", ReasonContent, func() []byte {
			r := newReplica(t, 9, snap)
			return r.edit(func(txn *crdt.Transaction) { r.text.InsertEmbed(txn, 0, map[string]any{"x": 1}, nil) })
		}, only(9), nil},
		{"pending", ReasonInvalid, func() []byte {
			r := newReplica(t, 9, snap)
			r.insert(0, "x")
			return r.insert(1, "y") // depends on the unsent "x"
		}, only(9), nil},
		{"garbage", ReasonInvalid, func() []byte { return []byte{0xff, 0xff, 0xff, 0x01} }, only(9), nil},
		{"check", ReasonRejected, func() []byte { return newReplica(t, 9, snap).insert(0, "\x00") }, only(9),
			func(_, next string) error {
				if strings.ContainsRune(next, 0) {
					return errors.New("NUL")
				}
				return nil
			}},
	}
	for _, tc := range cases {
		_, err := d.ApplyClient(tc.update(), tc.allowed, tc.check)
		if got := reason(err); got != tc.want {
			t.Errorf("%s: reason %q (%v), want %q", tc.name, got, err, tc.want)
		}
		if d.Text() != before || string(d.StateVector()) != string(sv) {
			t.Fatalf("%s: rejected update changed the document: %q", tc.name, d.Text())
		}
	}
	// A valid update still applies after the rejections (shadow rebuilt).
	if _, err := d.ApplyClient(newReplica(t, 11, snap).insert(3, "!"), only(11), nil); err != nil || d.Text() != "a😀!b" {
		t.Fatalf("after rejections: %q %v", d.Text(), err)
	}
}

func TestDocServerEditsReachReplicas(t *testing.T) {
	d, snap, _ := New("one\ntwo\nthree\n")
	c := newReplica(t, 5, snap)
	// The client edits line one while the server applies a disk change to
	// line three; both converge.
	cu := c.insert(0, "ONE ")
	if _, err := d.ApplyClient(cu, only(5), nil); err != nil {
		t.Fatal(err)
	}
	text := d.Text()
	i := strings.Index(text, "three")
	su, err := d.Replace([]Edit{{Start: i, End: i + 5, Text: "3️⃣"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := crdt.ApplyUpdateV1(c.doc, su, nil); err != nil {
		t.Fatal(err)
	}
	if d.Text() != "ONE one\ntwo\n3️⃣\n" || c.text.ToString() != d.Text() {
		t.Fatalf("server %q client %q", d.Text(), c.text.ToString())
	}
	// Sync by state vector: a stale replica catches up exactly.
	stale := newReplica(t, 6, snap)
	diff, err := d.Diff(crdt.EncodeStateVectorV1(stale.doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := crdt.ApplyUpdateV1(stale.doc, diff, nil); err != nil || stale.text.ToString() != d.Text() {
		t.Fatalf("diff sync %q %v", stale.text.ToString(), err)
	}
	if u, err := d.SetText(d.Text()); err != nil || u != nil {
		t.Fatal("no-op SetText produced an update")
	}
	if _, err := d.SetText("é"); err != nil || d.Text() != "é" {
		t.Fatalf("SetText %q %v", d.Text(), err)
	}
}

func TestDocRandomizedAgainstReplica(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	d, snap, _ := New("start😀\n")
	c := newReplica(t, 42, snap)
	alphabet := []string{"a", "é", "😀", "\n", "é", "漢"}
	for step := 0; step < 300; step++ {
		var u []byte
		n := c.text.Len()
		if n > 0 && rng.Intn(3) == 0 {
			// Delete whole runes only: pick positions on rune boundaries.
			at, l := runeRange(c.text.ToString(), rng)
			u = c.delete(at, l)
		} else {
			at, _ := runeRange(c.text.ToString(), rng)
			u = c.insert(at, alphabet[rng.Intn(len(alphabet))])
		}
		if _, err := d.ApplyClient(u, only(42), nil); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if step%50 == 0 {
			edit := MinimalEdit(d.Text(), d.Text()+fmt.Sprint(step))
			su, err := d.Replace([]Edit{edit})
			if err != nil {
				t.Fatal(err)
			}
			if err := crdt.ApplyUpdateV1(c.doc, su, nil); err != nil {
				t.Fatal(err)
			}
		}
		if d.Text() != c.text.ToString() {
			t.Fatalf("step %d: diverged %q vs %q", step, d.Text(), c.text.ToString())
		}
	}
}

// runeRange returns a UTF-16 position on a rune boundary and a length
// covering at most two whole runes after it.
func runeRange(s string, rng *rand.Rand) (int, int) {
	var starts []int
	u := 0
	for _, r := range s {
		starts = append(starts, u)
		u += UTF16Len(string(r))
	}
	starts = append(starts, u)
	i := rng.Intn(len(starts))
	j := min(len(starts)-1, i+1+rng.Intn(2))
	return starts[i], starts[j] - starts[i]
}

func TestMinimalEditRuneBoundaries(t *testing.T) {
	for _, tc := range [][2]string{{"aé", "aè"}, {"😀x", "😁x"}, {"", "x"}, {"x", ""}, {"abc", "abc"}, {"日本", "日木"}} {
		e := MinimalEdit(tc[0], tc[1])
		got := tc[0][:e.Start] + e.Text + tc[0][e.End:]
		if got != tc[1] || !runeBoundary(tc[0], e.Start) || !runeBoundary(tc[0], e.End) {
			t.Fatalf("%q -> %q: %+v", tc[0], tc[1], e)
		}
	}
}

func TestDocRejectsContentOutsideSharedText(t *testing.T) {
	d, snap, _ := New("hello\n")
	sv := string(d.StateVector())
	big := strings.Repeat("x", 1<<20)
	c := newReplica(t, 7, snap)
	m := c.doc.GetMap("m")
	u := c.edit(func(txn *crdt.Transaction) { m.Set(txn, "k", big) })
	if _, err := d.ApplyClient(u, only(7), nil); reason(err) != ReasonContent {
		t.Fatalf("map: %v", err)
	}
	c2 := newReplica(t, 8, snap)
	other := c2.doc.GetText("other")
	u2 := c2.edit(func(txn *crdt.Transaction) { other.Insert(txn, 0, big, nil) })
	if _, err := d.ApplyClient(u2, only(8), nil); reason(err) != ReasonContent {
		t.Fatalf("other text: %v", err)
	}
	// Text inserted and deleted within one update is invisible too.
	c3 := newReplica(t, 9, snap)
	u3 := c3.edit(func(txn *crdt.Transaction) {
		c3.text.Insert(txn, 0, "gone", nil)
		c3.text.Delete(txn, 0, 4)
	})
	if _, err := d.ApplyClient(u3, only(9), nil); reason(err) != ReasonContent {
		t.Fatalf("insert+delete: %v", err)
	}
	if string(d.StateVector()) != sv || len(d.EncodeState()) > 1024 || d.Text() != "hello\n" {
		t.Fatal("refused content reached the document")
	}
}

func TestDocBoundsNewDeleteRanges(t *testing.T) {
	d, snap, _ := New("")
	c := newReplica(t, 7, snap)
	if _, err := d.ApplyClient(c.insert(0, strings.Repeat("ab", 6000)), only(7), nil); err != nil {
		t.Fatal(err)
	}
	var last []byte
	for i := 0; i < MaxNewRanges+10; i++ {
		last = c.delete(i, 1) // every other character: separate ranges
	}
	if _, err := d.ApplyClient(last, only(7), nil); reason(err) != ReasonTooLarge {
		t.Fatalf("mass delete: %v", err)
	}
	// Repeating an already applied delete set does not count.
	c2 := newReplica(t, 8, d.EncodeState())
	var u []byte
	for i := 0; i < 100; i++ {
		u = c2.delete(i, 1)
	}
	if _, err := d.ApplyClient(u, only(8), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ApplyClient(c2.insert(0, "z"), only(8), nil); err != nil {
		t.Fatal(err)
	}
}

// Ordinary typing with corrections, then select-all delete and replace, is
// never refused by the content or range rules.
func TestDocAcceptsTypingCorrectionsAndSelectAll(t *testing.T) {
	d, snap, _ := New("")
	c := newReplica(t, 77, snap)
	pos := 0
	for i := 0; i < 600; i++ {
		if _, err := d.ApplyClient(c.insert(pos, "ab"), only(77), nil); err != nil {
			t.Fatal(i, err)
		}
		if _, err := d.ApplyClient(c.delete(pos+1, 1), only(77), nil); err != nil {
			t.Fatal(i, err)
		}
		pos++
	}
	if _, err := d.ApplyClient(c.delete(0, c.text.Len()), only(77), nil); err != nil {
		t.Fatal(err)
	}
	u := c.edit(func(txn *crdt.Transaction) {
		c.text.Delete(txn, 0, c.text.Len())
		c.text.Insert(txn, 0, "replacement", nil)
	})
	if _, err := d.ApplyClient(u, only(77), nil); err != nil || d.Text() != "replacement" {
		t.Fatalf("%q %v", d.Text(), err)
	}
	// Insert/delete cycles of large text leave a small state (GC).
	big := strings.Repeat("abcdefgh\n", 20000)
	for i := 0; i < 5; i++ {
		if _, err := d.ApplyClient(c.insert(0, big), only(77), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.ApplyClient(c.delete(0, len(big)), only(77), nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(d.EncodeState()); n > 64<<10 {
		t.Fatalf("state %d bytes after cycles", n)
	}
}
