package collabprobe

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// op is a replayable edit; indices are UTF-16 code units.
type op struct {
	Rep  int    `json:"rep"`
	Del  bool   `json:"del"`
	Idx  int    `json:"idx"`
	N    int    `json:"n,omitempty"`
	Str  string `json:"str,omitempty"`
	Sync bool   `json:"sync"` // deliver to the other replica immediately
}

// genOps drives a reference replica pair (reearth) with a seeded schedule
// that alternates sync and concurrent (buffered) edits.
func genOps(seed uint64, n int) []op {
	rng := rand.New(rand.NewPCG(seed, 1))
	rs := []Replica{Reearth.New(1), Reearth.New(2)}
	var pend [2][][]byte
	var ops []op
	for i := 0; i < n; i++ {
		k := i % 3 % 2
		b := boundaries(rs[k].Text())
		o := op{Rep: k, Sync: rng.IntN(4) != 0}
		if len(b) > 1 && rng.IntN(3) == 0 {
			x := rng.IntN(len(b) - 1)
			y := x + 1 + rng.IntN(min(4, len(b)-1-x))
			o.Del, o.Idx, o.N = true, b[x], b[y]-b[x]
		} else {
			o.Idx, o.Str = b[rng.IntN(len(b))], alphabet[rng.IntN(len(alphabet))]
		}
		ops = append(ops, o)
		pend[1-k] = append(pend[1-k], apply(rs[k], o))
		if o.Sync {
			for _, u := range pend[1-k] {
				rs[1-k].Apply(u)
			}
			pend[1-k] = nil
		}
	}
	return ops
}

func apply(r Replica, o op) []byte {
	if o.Del {
		return r.Delete(o.Idx, o.N)
	}
	return r.Insert(o.Idx, o.Str)
}

// replay runs an op list on a fresh pair of a library and returns the
// converged text.
func replay(t *testing.T, f Factory, ops []op) string {
	rs := []Replica{f.New(1), f.New(2)}
	var pend [2][][]byte
	for _, o := range ops {
		pend[1-o.Rep] = append(pend[1-o.Rep], apply(rs[o.Rep], o))
		if o.Sync {
			for _, u := range pend[1-o.Rep] {
				mustApply(t, rs[1-o.Rep], u)
			}
			pend[1-o.Rep] = nil
		}
	}
	for k := range pend {
		for _, u := range pend[k] {
			mustApply(t, rs[k], u)
		}
	}
	if rs[0].Text() != rs[1].Text() {
		t.Fatalf("%s pair diverged", f.Lib)
	}
	return rs[0].Text()
}

// TestDifferential replays identical seeded op lists on both Go libraries,
// flags lone-surrogate corruption, and writes the lists for interop.mjs to
// replay in Yjs.
func TestDifferential(t *testing.T) {
	type fixture struct {
		Seed  uint64            `json:"seed"`
		Ops   []op              `json:"ops"`
		Texts map[string]string `json:"texts"`
	}
	var all []fixture
	bad := map[string]int{}
	for seed := uint64(1); seed <= 30; seed++ {
		ops := genOps(seed, 200)
		fx := fixture{Seed: seed, Ops: ops, Texts: map[string]string{}}
		for _, f := range factories {
			txt := replay(t, f, ops)
			fx.Texts[f.New(1).Name()] = txt
			if strings.ContainsRune(txt, '�') {
				bad[f.Lib]++
			}
		}
		if fx.Texts["reearth"] != fx.Texts["deln0r"] {
			bad["mismatch"]++
		}
		all = append(all, fx)
	}
	b, _ := json.Marshal(all)
	write(t, "differential.json", b)
	t.Logf("seeds=30 ops/seed=200: U+FFFD corruption reearth=%d deln0r=%d; reearth!=deln0r in %d seeds",
		bad["reearth/ygo"], bad["Deln0r/ygo"], bad["mismatch"])
	// reearth is the op generator, so it is asserted only for corruption;
	// interop.mjs adjudicates both libraries against Yjs.
	if bad["reearth/ygo"] > 0 {
		t.Errorf("reearth corruption: %v", bad)
	}
	if bad["Deln0r/ygo"] > 0 || bad["mismatch"] > 0 {
		t.Logf("FINDING Deln0r/ygo differs from reearth/ygo: %v", bad)
	}
}
