package collabprobe

import "testing"

// TestIndexAfterRemoteDelete is the minimised divergence found by
// TestDifferential (seeded search + greedy op removal). Replica 1's local
// text is "xyz" and it inserts " " at UTF-16 index 1; Yjs and reearth give
// "x yz". The probe records, rather than fails on, the Deln0r result.
func TestIndexAfterRemoteDelete(t *testing.T) {
	ops := []op{
		{Rep: 0, Idx: 0, Str: "\n", Sync: true},
		{Rep: 1, Idx: 1, Str: "xyz", Sync: true},
		{Rep: 0, Del: true, Idx: 0, N: 1, Sync: true},
		{Rep: 1, Idx: 1, Str: " ", Sync: false},
	}
	each(t, func(t *testing.T, f Factory) {
		got := replay(t, f, ops)
		if got == "x yz" {
			return
		}
		if f.Lib == Reearth.Lib {
			t.Fatalf("got %q", got)
		}
		t.Logf("FINDING %s: got %q, want %q", f.Lib, got, "x yz")
	})
}
