package collabprobe

import "testing"

func TestClusterMapping(t *testing.T) {
	s := "a😀é👩‍👩‍👧中b"
	cs := Clusters(s)
	for _, c := range cs {
		t.Logf("%q byte=%d u16=%d col=%d w=%d", c.Text, c.Byte, c.U16, c.Col, c.Width)
	}
	if len(cs) != 6 {
		t.Fatalf("clusters=%d", len(cs))
	}
	// Remote cursor between 'e' and U+0301 (u16=4) snaps to the 'e' cluster.
	if i := U16ToCluster(cs, 4); cs[i].Text != "é" {
		t.Fatalf("snap %q", cs[i].Text)
	}
	each(t, func(t *testing.T, f Factory) {
		r := f.New(1)
		r.Insert(0, s)
		// Insert at the start of the "中" cluster using its UTF-16 offset.
		r.Insert(cs[4].U16, "|")
		if r.Text() != "a😀é👩‍👩‍👧|中b" {
			t.Fatalf("%q", r.Text())
		}
	})
}
