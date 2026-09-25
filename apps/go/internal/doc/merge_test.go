package doc

import (
	"math/rand"
	"strings"
	"testing"
)

func TestMerge3(t *testing.T) {
	base := "a\nb\nc\nd\ne\n"
	cases := []struct {
		name, ours, theirs, want string
		clean                    bool
	}{
		{"theirs only", base, "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", true},
		{"ours only", "A\nb\nc\nd\ne\n", base, "A\nb\nc\nd\ne\n", true},
		{"disjoint", "A\nb\nc\nd\ne\n", "a\nb\nc\nd\nE\n", "A\nb\nc\nd\nE\n", true},
		{"theirs insert", "A\nb\nc\nd\ne\n", "a\nb\nc\nx\nd\ne\n", "A\nb\nc\nx\nd\ne\n", true},
		{"theirs delete", "A\nb\nc\nd\ne\n", "a\nb\nc\ne\n", "A\nb\nc\ne\n", true},
		{"identical change", "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", "a\nB\nc\nd\ne\n", true},
		{"same line", "a\nX\nc\nd\ne\n", "a\nY\nc\nd\ne\n", "", false},
		{"adjacent lines", "a\nB\nc\nd\ne\n", "a\nb\nC\nd\ne\n", "", false},
		{"both insert same point", "a\nb\nX\nc\nd\ne\n", "a\nb\nY\nc\nd\ne\n", "", false},
		{"no final newline", "A\nb\nc\nd\ne", "a\nb\nc\nd\ne\nf", "", false},
		{"crlf-free unicode", "a\nb😀\nc\nd\ne\n", "a\nb\nc\nd\né\n", "a\nb😀\nc\nd\né\n", true},
	}
	for _, tc := range cases {
		edits, merged, clean, err := Merge3(base, tc.ours, tc.theirs)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if clean != tc.clean {
			t.Fatalf("%s: clean=%v", tc.name, clean)
		}
		if !clean {
			continue
		}
		if merged != tc.want {
			t.Fatalf("%s: merged %q want %q", tc.name, merged, tc.want)
		}
		if got := applyEdits(tc.ours, edits); got != merged {
			t.Fatalf("%s: edits give %q", tc.name, got)
		}
	}
}

func applyEdits(s string, edits []Edit) string {
	var b strings.Builder
	last := 0
	for _, e := range edits {
		b.WriteString(s[last:e.Start])
		b.WriteString(e.Text)
		last = e.End
	}
	return b.String() + s[last:]
}

// Randomized: ours changes only even lines, theirs only lines ≡ 3 mod 4
// with an unchanged line between every pair, so every merge is clean and
// must contain both sides' changes.
func TestMerge3Randomized(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 200; round++ {
		n := 8 + rng.Intn(40)
		var base, ours, theirs []string
		for i := 0; i < n; i++ {
			line := string(rune('a'+i%26)) + "\n"
			base = append(base, line)
			o, th := line, line
			if i%4 == 0 && rng.Intn(2) == 0 {
				o = "ours " + line
			}
			if i%4 == 2 && rng.Intn(2) == 0 {
				th = "theirs " + line
			}
			ours, theirs = append(ours, o), append(theirs, th)
		}
		var want []string
		for i := range base {
			switch {
			case ours[i] != base[i]:
				want = append(want, ours[i])
			default:
				want = append(want, theirs[i])
			}
		}
		b, o, th := strings.Join(base, ""), strings.Join(ours, ""), strings.Join(theirs, "")
		edits, merged, clean, err := Merge3(b, o, th)
		if err != nil || !clean || merged != strings.Join(want, "") || applyEdits(o, edits) != merged {
			t.Fatalf("round %d: clean=%v err=%v\n%q", round, clean, err, merged)
		}
	}
}

func TestMerge3TooDifferent(t *testing.T) {
	var base, ours, theirs strings.Builder
	for i := 0; i < 3*MaxMergeDistance; i++ {
		base.WriteString("x\n")
		ours.WriteString("x\n")
		if i%2 == 0 {
			theirs.WriteString("y\n")
		} else {
			theirs.WriteString("z\n")
		}
	}
	o := "changed\n" + ours.String()
	if _, _, _, err := Merge3(base.String(), o, theirs.String()); err != ErrTooDifferent {
		t.Fatalf("err %v", err)
	}
}

func TestDiffEdits(t *testing.T) {
	for _, tc := range [][2]string{{"a\nb\nc\n", "a\nB\nc\nd\n"}, {"", "x\n"}, {"x\n", ""}, {"a😀\nb\n", "a😁\nb\nc"}} {
		if got := applyEdits(tc[0], DiffEdits(tc[0], tc[1])); got != tc[1] {
			t.Fatalf("%q -> %q: got %q", tc[0], tc[1], got)
		}
	}
}
