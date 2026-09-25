package collabprobe

import "github.com/rivo/uniseg"

// Cluster is one grapheme cluster with its offsets in each unit system.
type Cluster struct {
	Text           string
	Byte, U16, Col int // start offsets
	Width          int // terminal cells (uniseg estimate)
}

// Clusters maps a document string to grapheme clusters with UTF-8 byte,
// UTF-16 unit and display-column starts. A TUI editor keeps its cursor on
// cluster boundaries and converts to UTF-16 only at the CRDT boundary.
func Clusters(s string) []Cluster {
	var out []Cluster
	b, u, c := 0, 0, 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		t := g.Str()
		w := g.Width()
		out = append(out, Cluster{Text: t, Byte: b, U16: u, Col: c, Width: w})
		b += len(t)
		u += utf16Len(t)
		c += w
	}
	return out
}

// U16ToCluster returns the index of the cluster containing UTF-16 offset
// u, snapping a remote offset that lands inside a cluster (e.g. between
// a base letter and its combining mark) to that cluster's start.
func U16ToCluster(cs []Cluster, u int) int {
	lo, hi := 0, len(cs)
	for lo < hi {
		m := (lo + hi) / 2
		if cs[m].U16 <= u {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo - 1
}
