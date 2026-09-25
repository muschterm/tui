package collabprobe

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

var factories = []Factory{Reearth, Deln0r}

func each(t *testing.T, fn func(t *testing.T, f Factory)) {
	for _, f := range factories {
		t.Run(f.Lib, func(t *testing.T) { fn(t, f) })
	}
}

func mustApply(t *testing.T, r Replica, u []byte) {
	t.Helper()
	if err := r.Apply(u); err != nil {
		t.Fatalf("%s apply: %v", r.Name(), err)
	}
}

// boundaries returns UTF-16 offsets that do not split a surrogate pair.
func boundaries(s string) []int {
	out := []int{0}
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
		out = append(out, n)
	}
	return out
}

var alphabet = []string{"a", "b", "c", "xyz", " ", "\n", "é", "😀", "é", "👩‍👩‍👧", "中"}

func randomOp(rng *rand.Rand, r Replica) []byte {
	b := boundaries(r.Text())
	if len(b) > 1 && rng.IntN(3) == 0 {
		i := rng.IntN(len(b) - 1)
		j := i + 1 + rng.IntN(min(4, len(b)-1-i))
		return r.Delete(b[i], b[j]-b[i])
	}
	return r.Insert(b[rng.IntN(len(b))], alphabet[rng.IntN(len(alphabet))])
}

// TestRandomizedConvergence: 3 replicas edit concurrently; updates are
// delivered in shuffled order, with duplicates, in random batches.
func TestRandomizedConvergence(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		for seed := uint64(1); seed <= 40; seed++ {
			rng := rand.New(rand.NewPCG(seed, 7))
			rs := []Replica{f.New(1), f.New(2), f.New(3)}
			inbox := make([][][]byte, 3) // pending updates per replica
			for step := 0; step < 150; step++ {
				k := rng.IntN(3)
				if rng.IntN(4) == 0 && len(inbox[k]) > 0 {
					// deliver a random subset, shuffled, possibly duplicated
					rng.Shuffle(len(inbox[k]), func(i, j int) { inbox[k][i], inbox[k][j] = inbox[k][j], inbox[k][i] })
					n := rng.IntN(len(inbox[k])) + 1
					for _, u := range inbox[k][:n] {
						mustApply(t, rs[k], u)
						if rng.IntN(5) == 0 {
							mustApply(t, rs[k], u) // duplicate
						}
					}
					inbox[k] = inbox[k][n:]
					continue
				}
				u := randomOp(rng, rs[k])
				for o := range rs {
					if o != k {
						inbox[o] = append(inbox[o], u)
					}
				}
			}
			for k := range rs {
				rng.Shuffle(len(inbox[k]), func(i, j int) { inbox[k][i], inbox[k][j] = inbox[k][j], inbox[k][i] })
				for _, u := range inbox[k] {
					mustApply(t, rs[k], u)
				}
			}
			a := rs[0].Text()
			for k, r := range rs[1:] {
				if got := r.Text(); got != a {
					t.Fatalf("seed %d: replica %d diverged:\n%q\n%q", seed, k+1, a, got)
				}
			}
			// Full-state exchange must be a no-op now.
			for _, r := range rs {
				for _, o := range rs {
					mustApply(t, r, o.Full())
				}
				if r.Text() != a {
					t.Fatalf("seed %d: full-state reapply changed text", seed)
				}
			}
		}
	})
}

// TestOutOfOrderAndDuplicate: a causally later update arriving first is
// held pending and integrated once its dependency arrives.
func TestOutOfOrderAndDuplicate(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		a, b := f.New(1), f.New(2)
		u1 := a.Insert(0, "hello")
		u2 := a.Insert(5, " world")
		u3 := a.Delete(0, 1)
		mustApply(t, b, u3)
		mustApply(t, b, u2)
		if b.Text() != "" {
			t.Logf("before dependency: %q", b.Text())
		}
		mustApply(t, b, u1)
		mustApply(t, b, u1)
		mustApply(t, b, u2)
		if b.Text() != "ello world" || a.Text() != b.Text() {
			t.Fatalf("got a=%q b=%q", a.Text(), b.Text())
		}
	})
}

// TestOriginScopedUndo: A's undo removes only A's edits, including when
// interleaved with and adjacent to B's edits.
func TestOriginScopedUndo(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		a, b := f.New(1), f.New(2)
		sync2 := func(u []byte, to Replica) { mustApply(t, to, u) }
		sync2(a.Insert(0, "AAA"), b)
		a.StopCapturing()
		sync2(b.Insert(3, "bbb"), a) // AAAbbb
		sync2(a.Insert(6, "CC"), b)  // AAAbbbCC
		a.StopCapturing()
		sync2(b.Insert(3, "_"), a) // AAA_bbbCC  (B edits between A's)
		sync2(b.Delete(0, 1), a)   // B deletes one of A's chars: AA_bbbCC
		if a.Text() != "AA_bbbCC" {
			t.Fatalf("setup %q", a.Text())
		}
		if !a.Undo() {
			t.Fatal("undo 1 returned false")
		}
		if a.Text() != "AA_bbb" {
			t.Fatalf("after undo 1 %q", a.Text())
		}
		if !a.Undo() {
			t.Fatal("undo 2 returned false")
		}
		if a.Text() != "_bbb" {
			t.Fatalf("after undo 2 %q (B's text must survive)", a.Text())
		}
		if a.Undo() {
			t.Fatal("undo 3 should be empty (B's edits are not A's)")
		}
		u, _ := a.Diff(b.StateVector())
		mustApply(t, b, u)
		if b.Text() != a.Text() {
			t.Fatalf("peer after undo %q vs %q", b.Text(), a.Text())
		}
		// B can still undo its own edits independently.
		for b.Undo() {
		}
		u, _ = b.Diff(a.StateVector())
		mustApply(t, a, u)
		// Yjs semantics: B's undo of its delete restores A's character that
		// B had deleted, even though A already undid its own insert.
		if a.Text() != "A" || b.Text() != "A" {
			t.Fatalf("after B undo all: a=%q b=%q", a.Text(), b.Text())
		}
	})
}

// TestRelativePositions: cursors anchored by relative position follow
// remote inserts/deletes before them and stay put for edits after them.
func TestRelativePositions(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		srv, cli := f.New(1), f.New(2)
		mustApply(t, cli, srv.Insert(0, "hello world"))
		cur := cli.Anchor(6, 0)   // before 'w'
		end := cli.Anchor(11, -1) // end of text
		// Anchors are portable: resolve on server too.
		if i, ok := srv.Resolve(cur); !ok || i != 6 {
			t.Fatalf("server resolve %d %v", i, ok)
		}
		mustApply(t, cli, srv.Insert(0, "😀 "))   // +3 UTF-16
		mustApply(t, cli, srv.Delete(3, 5))      // remove "hello"
		mustApply(t, cli, srv.Insert(4, "big ")) // after cursor? " world" -> check
		txt := cli.Text()
		i, ok := cli.Resolve(cur)
		if !ok {
			t.Fatal("unresolved")
		}
		u := utf16.Encode([]rune(txt))
		if string(utf16.Decode(u[i:i+1])) != "w" {
			t.Fatalf("cursor at %d in %q no longer before 'w'", i, txt)
		}
		if e, _ := cli.Resolve(end); e != len(u) {
			t.Fatalf("end anchor %d want %d", e, len(u))
		}
		// Deleting the anchored char: cursor collapses to deletion point.
		mustApply(t, cli, srv.Delete(i, 1))
		if j, ok := cli.Resolve(cur); !ok || j != i {
			t.Fatalf("after anchored delete got %d,%v want %d", j, ok, i)
		}
		t.Logf("text=%q cursor=%d", cli.Text(), i)
	})
}

// TestPersistenceRestart: persist every incremental update (append log),
// restart from the log alone, and from a compacted snapshot.
func TestPersistenceRestart(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		rng := rand.New(rand.NewPCG(99, 1))
		a, b := f.New(1), f.New(2)
		var log [][]byte
		for i := 0; i < 300; i++ {
			r := a
			if i%3 == 0 {
				r = b
			}
			u := randomOp(rng, r)
			log = append(log, u)
			if r == a {
				mustApply(t, b, u)
			} else {
				mustApply(t, a, u)
			}
		}
		want := a.Text()
		restarted := f.New(1) // same client id as a (server restart)
		for _, u := range log {
			mustApply(t, restarted, u)
		}
		if restarted.Text() != want {
			t.Fatal("log replay mismatch")
		}
		if !bytes.Equal(restarted.StateVector(), a.StateVector()) {
			t.Fatal("state vector mismatch after replay")
		}
		snap := a.Full()
		r2 := f.New(1)
		mustApply(t, r2, snap)
		if r2.Text() != want {
			t.Fatal("snapshot restore mismatch")
		}
		// Restarted replica continues editing without clock collisions.
		u := r2.Insert(0, "Z")
		mustApply(t, b, u)
		if b.Text() != "Z"+want {
			t.Fatalf("post-restart edit: %q", b.Text()[:10])
		}
		t.Logf("log=%d updates, %d bytes; snapshot=%d bytes; text=%d bytes",
			len(log), total(log), len(snap), len(want))
	})
}

func total(bs [][]byte) (n int) {
	for _, b := range bs {
		n += len(b)
	}
	return
}

// TestStateVectorReconnect: an offline client and server both edit; the
// reconnect exchanges SV-based diffs; replaying the same diffs twice
// (retry after lost ack) does not duplicate text.
func TestStateVectorReconnect(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		srv, cli := f.New(1), f.New(2)
		mustApply(t, cli, srv.Insert(0, "base text "))
		// Offline period.
		for i := 0; i < 20; i++ {
			srv.Insert(srv.Len(), fmt.Sprintf("s%d ", i))
			cli.Insert(0, fmt.Sprintf("c%d ", i))
		}
		toSrv, err := cli.Diff(srv.StateVector())
		if err != nil {
			t.Fatal(err)
		}
		toCli, err := srv.Diff(cli.StateVector())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			mustApply(t, srv, toSrv)
			mustApply(t, cli, toCli)
		}
		if srv.Text() != cli.Text() || strings.Count(srv.Text(), "c7 ") != 1 {
			t.Fatalf("reconnect mismatch/dup: %q", srv.Text())
		}
		empty, _ := srv.Diff(cli.StateVector())
		t.Logf("diff sizes toSrv=%d toCli=%d; post-sync diff=%d bytes", len(toSrv), len(toCli), len(empty))
	})
}

// TestUnicodeUnits establishes the index unit and a grapheme/cell mapping.
func TestUnicodeUnits(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		r := f.New(1)
		s := "a😀é👩‍👩‍👧中"
		r.Insert(0, s)
		want16 := len(utf16.Encode([]rune(s)))
		t.Logf("bytes=%d runes=%d utf16=%d Len()=%d", len(s), len([]rune(s)), want16, r.Len())
		if r.Len() != want16 {
			t.Fatalf("Len is not UTF-16 units")
		}
		// Insert after the emoji: UTF-16 index 3 (a=1, 😀=2).
		r.Insert(3, "|")
		if r.Text() != "a😀|é👩‍👩‍👧中" {
			t.Fatalf("index 3 insert: %q", r.Text())
		}
		r.Delete(3, 1)
		// Splitting a surrogate pair: record behaviour, do not require it.
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Logf("insert inside surrogate pair panicked: %v", p)
				}
			}()
			r.Insert(2, "X")
			t.Logf("insert inside surrogate pair -> %q (valid UTF-8: %v)", r.Text(), strings.ToValidUTF8(r.Text(), "?") == r.Text())
		}()
	})
}

// TestConcurrentAccess: server goroutines apply client updates while
// readers encode/diff; exercised under -race.
func TestConcurrentAccess(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		srv := f.New(1)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var ups [][]byte
		for c := uint64(2); c < 6; c++ {
			wg.Add(1)
			go func(c uint64) {
				defer wg.Done()
				cli := f.New(c)
				for i := 0; i < 100; i++ {
					u := cli.Insert(0, "x")
					if err := srv.Apply(u); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					ups = append(ups, u)
					mu.Unlock()
				}
			}(c)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = srv.Text()
				_, _ = srv.Diff(nil)
				_ = srv.StateVector()
			}
		}()
		wg.Wait()
		if srv.Len() != 400 {
			t.Fatalf("len %d", srv.Len())
		}
	})
}

// TestLargeDocument: 1 MiB document, then 10k small edits.
func TestLargeDocument(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	each(t, func(t *testing.T, f Factory) {
		line := strings.Repeat("abcdefghij", 7) + "\n" // 71 bytes
		var sb strings.Builder
		for sb.Len() < 1<<20 {
			sb.WriteString(line)
		}
		big := sb.String()
		srv, cli := f.New(1), f.New(2)
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		t0 := time.Now()
		mustApply(t, cli, srv.Insert(0, big))
		load := time.Since(t0)
		rng := rand.New(rand.NewPCG(5, 5))
		t0 = time.Now()
		var upBytes int
		for i := 0; i < 10000; i++ {
			var u []byte
			if i%4 == 3 {
				u = cli.Delete(rng.IntN(cli.Len()-2), 1)
			} else {
				u = cli.Insert(rng.IntN(cli.Len()), "q")
			}
			upBytes += len(u)
			mustApply(t, srv, u)
		}
		ops := time.Since(t0)
		lastIns := len(cli.Insert(0, "q"))
		lastDel := len(cli.Delete(0, 1))
		t0 = time.Now()
		full := srv.Full()
		enc := time.Since(t0)
		t0 = time.Now()
		re := f.New(3)
		mustApply(t, re, full)
		dec := time.Since(t0)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		if mustApply(t, re, cli.Full()); re.Text() != cli.Text() {
			t.Fatal("mismatch")
		}
		t.Logf("RESULT lib=%s load1MiB=%v 10kOps(local+remote apply)=%v perOp=%v incrUpdates=%dB lastInsertUpdate=%dB lastDeleteUpdate=%dB full=%dB encode=%v decode=%v heapLive=%.1fMiB (3 replicas)",
			f.Lib, load, ops, ops/10000, upBytes, lastIns, lastDel, len(full), enc, dec, float64(int64(m1.HeapAlloc)-int64(m0.HeapAlloc))/(1<<20))
		runtime.KeepAlive([]Replica{srv, cli, re})
	})
}
