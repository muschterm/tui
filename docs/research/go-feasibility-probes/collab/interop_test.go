package collabprobe

import (
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Deln0r/ygo"
	"github.com/reearth/ygo/crdt"
)

// TestWriteGoFixtures writes Go-produced state for interop.mjs.
func TestWriteGoFixtures(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		a, b := f.New(11), f.New(22)
		mustApply(t, b, a.Insert(0, "go 😀 text é 👩‍👩‍👧 end"))
		ua := a.Insert(3, "A中")
		ub := b.Insert(3, "B🎉")
		ub2 := b.Delete(0, 1)
		mustApply(t, a, ub)
		mustApply(t, a, ub2)
		mustApply(t, b, ua)
		if a.Text() != b.Text() {
			t.Fatal("diverged")
		}
		idx := strings.Index(a.Text(), "text")
		i16 := utf16Len(a.Text()[:idx])
		write(t, "go-"+f.New(1).Name()+".bin", a.Full())
		write(t, "go-"+f.New(1).Name()+".txt", []byte(a.Text()))
		write(t, "go-"+f.New(1).Name()+".relpos", a.Anchor(i16, 0))
		write(t, "go-"+f.New(1).Name()+".relidx", []byte(strconv.Itoa(i16)))
	})
}

func write(t *testing.T, n string, b []byte) {
	if err := os.WriteFile(filepath.Join("testdata", n), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, n string) []byte {
	b, err := os.ReadFile(filepath.Join("testdata", n))
	if err != nil {
		t.Skipf("fixture %s missing; run interop.mjs first: %v", n, err)
	}
	return b
}

func utf16Len(s string) (n int) {
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return
}

// TestApplyYjsFixtures applies Yjs 13.6.32-produced updates in Go.
func TestApplyYjsFixtures(t *testing.T) {
	each(t, func(t *testing.T, f Factory) {
		want := string(read(t, "js.txt"))
		r := f.New(1)
		mustApply(t, r, read(t, "js.bin"))
		if r.Text() != want {
			t.Fatalf("V1 text %q want %q", r.Text(), want)
		}
		idx, _ := strconv.Atoi(string(read(t, "js.relidx")))
		if got, ok := r.Resolve(read(t, "js.relpos")); !ok || got != idx {
			t.Fatalf("relpos %d,%v want %d", got, ok, idx)
		}
		// Stream applied in reverse order must converge once complete.
		var ups []string
		if err := json.Unmarshal(read(t, "js-stream.json"), &ups); err != nil {
			t.Fatal(err)
		}
		s := f.New(2)
		rng := rand.New(rand.NewPCG(3, 3))
		rng.Shuffle(len(ups), func(i, j int) { ups[i], ups[j] = ups[j], ups[i] })
		for _, u := range ups {
			b, _ := base64.StdEncoding.DecodeString(u)
			mustApply(t, s, b)
		}
		if s.Text() != string(read(t, "js-stream.txt")) {
			t.Fatalf("shuffled stream %q", s.Text())
		}
	})
	t.Run("V2", func(t *testing.T) {
		want := string(read(t, "js.txt"))
		d := crdt.New()
		if err := crdt.ApplyUpdateV2(d, read(t, "js.v2.bin"), nil); err != nil || d.GetText("t").ToString() != want {
			t.Errorf("reearth V2: %v %q", err, d.GetText("t").ToString())
		}
		d2 := ygo.NewDoc()
		tx := ygo.NewText(d2, "t")
		if err := ygo.ApplyUpdateV2(d2, read(t, "js.v2.bin")); err != nil {
			t.Fatal(err)
		}
		rt := d2.ReadTxn()
		got := tx.String()
		rt.Close()
		if got != want {
			t.Errorf("deln0r V2 %q", got)
		}
	})
}

// TestCrossLibrary exchanges updates between the two Go libraries.
func TestCrossLibrary(t *testing.T) {
	a, b := Reearth.New(1), Deln0r.New(2)
	rng := rand.New(rand.NewPCG(8, 8))
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			mustApply(t, b, randomOp(rng, a))
		} else {
			mustApply(t, a, randomOp(rng, b))
		}
	}
	if a.Text() != b.Text() {
		t.Fatalf("diverged")
	}
}
