package doc

import (
	"strings"
	"testing"
)

// BenchmarkApplyClient1MiB measures one validated keystroke update against
// a document near the 1 MiB limit.
func BenchmarkApplyClient1MiB(b *testing.B) {
	line := strings.Repeat("x", 70) + "\n"
	text := strings.Repeat(line, (1<<20)/len(line)-1)
	d, snap, err := New(text)
	if err != nil {
		b.Fatal(err)
	}
	c := newReplica(b, 77, snap)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := c.insert((i*7919)%c.text.Len(), "y")
		if _, err := d.ApplyClient(u, only(77), nil); err != nil {
			b.Fatal(err)
		}
	}
}
