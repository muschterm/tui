package doc

import (
	"errors"
	"strings"
)

// Line-based three-way merge for reconciling external disk changes with the
// shared document. It is deliberately conservative: changes from both sides
// that overlap or touch (no unchanged base line between them) are a conflict
// unless both sides made the identical change, and inputs whose line diff is
// too large to compute cheaply are reported as not mergeable rather than
// merged approximately.

// MaxMergeDistance bounds the number of differing lines (after trimming the
// common prefix and suffix) one diff may contain.
const MaxMergeDistance = 2000

// ErrTooDifferent reports a diff beyond MaxMergeDistance.
var ErrTooDifferent = errors.New("doc: versions differ too much to merge automatically")

// hunk replaces base lines [oStart,oEnd) with side lines [xStart,xEnd).
type hunk struct{ oStart, oEnd, xStart, xEnd int }

// Merge3 merges the changes theirs made to base into ours. On success it
// returns the edits (byte offsets into ours, ascending) that turn ours into
// the merged text, and the merged text. clean is false when the changes
// conflict; err is ErrTooDifferent when a diff is too large.
func Merge3(base, ours, theirs string) (edits []Edit, merged string, clean bool, err error) {
	if theirs == base || theirs == ours {
		return nil, ours, true, nil
	}
	if ours == base {
		if theirs == ours {
			return nil, ours, true, nil
		}
		return []Edit{MinimalEdit(ours, theirs)}, theirs, true, nil
	}
	o, a, b := splitLines(base), splitLines(ours), splitLines(theirs)
	ids := map[string]int32{}
	intern := func(lines []string) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	oi, ai, bi := intern(o), intern(a), intern(b)
	ha, err := diffLines(oi, ai)
	if err != nil {
		return nil, "", false, err
	}
	hb, err := diffLines(oi, bi)
	if err != nil {
		return nil, "", false, err
	}
	// Byte offsets of each line start in ours.
	offsets := make([]int, len(a)+1)
	for i, l := range a {
		offsets[i+1] = offsets[i] + len(l)
	}
	var lineEdits []Edit
	ia, ib := 0, 0
	shiftA, shiftB := 0, 0 // (side length - base length) of hunks before the region
	for ia < len(ha) || ib < len(hb) {
		// Start a region at the earliest hunk and absorb every hunk that
		// overlaps or touches it.
		var lo, hi int
		if ib >= len(hb) || ia < len(ha) && ha[ia].oStart <= hb[ib].oStart {
			lo, hi = ha[ia].oStart, ha[ia].oEnd
		} else {
			lo, hi = hb[ib].oStart, hb[ib].oEnd
		}
		startA, startB := ia, ib
		for {
			grew := false
			for ia < len(ha) && ha[ia].oStart <= hi && ha[ia].oStart >= lo {
				hi = max(hi, ha[ia].oEnd)
				ia++
				grew = true
			}
			for ib < len(hb) && hb[ib].oStart <= hi && hb[ib].oStart >= lo {
				hi = max(hi, hb[ib].oEnd)
				ib++
				grew = true
			}
			if !grew {
				break
			}
		}
		endShiftA, endShiftB := shiftA, shiftB
		for _, h := range ha[startA:ia] {
			endShiftA += (h.xEnd - h.xStart) - (h.oEnd - h.oStart)
		}
		for _, h := range hb[startB:ib] {
			endShiftB += (h.xEnd - h.xStart) - (h.oEnd - h.oStart)
		}
		aStart, aEnd := lo+shiftA, hi+endShiftA
		bStart, bEnd := lo+shiftB, hi+endShiftB
		switch {
		case ib == startB:
			// Only ours changed here: keep it.
		case ia == startA:
			// Only theirs changed: ours equals base in this region.
			lineEdits = append(lineEdits, Edit{Start: offsets[aStart], End: offsets[aEnd], Text: strings.Join(b[bStart:bEnd], "")})
		default:
			if !equalLines(a[aStart:aEnd], b[bStart:bEnd]) {
				return nil, "", false, nil
			}
		}
		shiftA, shiftB = endShiftA, endShiftB
	}
	var sb strings.Builder
	last := 0
	for _, e := range lineEdits {
		sb.WriteString(ours[last:e.Start])
		sb.WriteString(e.Text)
		last = e.End
		// Narrow each line edit to the bytes that actually change so the
		// CRDT keeps as much of the existing text (and anchors) as it can.
		m := MinimalEdit(ours[e.Start:e.End], e.Text)
		if m.Start == m.End && m.Text == "" {
			continue
		}
		edits = append(edits, Edit{Start: e.Start + m.Start, End: e.Start + m.End, Text: m.Text})
	}
	sb.WriteString(ours[last:])
	return edits, sb.String(), true, nil
}

func equalLines(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// splitLines splits s after every "\n", keeping terminators; a final line
// without one is its own element.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines returns the hunks turning o into x (Myers O(ND) on the part
// between their common prefix and suffix).
func diffLines(o, x []int32) ([]hunk, error) {
	pre := 0
	for pre < len(o) && pre < len(x) && o[pre] == x[pre] {
		pre++
	}
	suf := 0
	for suf < len(o)-pre && suf < len(x)-pre && o[len(o)-1-suf] == x[len(x)-1-suf] {
		suf++
	}
	a, b := o[pre:len(o)-suf], x[pre:len(x)-suf]
	matches, err := myers(a, b)
	if err != nil {
		return nil, err
	}
	var out []hunk
	pa, pb := 0, 0
	emit := func(ea, eb int) {
		if ea > pa || eb > pb {
			out = append(out, hunk{oStart: pre + pa, oEnd: pre + ea, xStart: pre + pb, xEnd: pre + eb})
		}
	}
	for _, m := range matches {
		emit(m[0], m[1])
		pa, pb = m[0]+1, m[1]+1
	}
	emit(len(a), len(b))
	return out, nil
}

// myers returns the matched (a index, b index) pairs of a shortest edit
// script, ascending.
func myers(a, b []int32) ([][2]int, error) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil, nil
	}
	limit := min(n+m, MaxMergeDistance)
	off := limit + 1
	v := make([]int32, 2*limit+3)
	// trace[d] is v restricted to k in [-d, d] before step d.
	var trace [][]int32
	final := -1
	for d := 0; d <= limit && final < 0; d++ {
		snap := make([]int32, 2*d+1)
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[off+k-1] < v[off+k+1] {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				final = d
				break
			}
		}
	}
	if final < 0 {
		return nil, ErrTooDifferent
	}
	var rev [][2]int
	x, y := n, m
	for d := final; d >= 0; d-- {
		snap := trace[d]
		at := func(k int) int { return int(snap[k+d]) }
		k := x - y
		var prevK int
		if d == 0 {
			prevK = 0
		} else if k == -d || k != d && at(k-1) < at(k+1) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := 0
		if d > 0 {
			prevX = at(prevK)
		}
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, [2]int{x, y})
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// DiffEdits returns line-granular edits (narrowed to the changed bytes)
// turning old into next, so a server edit keeps unchanged lines and the
// anchors in them. It falls back to one minimal edit when the line diff is
// too large.
func DiffEdits(old, next string) []Edit {
	if old == next {
		return nil
	}
	o, n := splitLines(old), splitLines(next)
	ids := map[string]int32{}
	intern := func(lines []string) []int32 {
		out := make([]int32, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	hunks, err := diffLines(intern(o), intern(n))
	if err != nil {
		return []Edit{MinimalEdit(old, next)}
	}
	oOff := make([]int, len(o)+1)
	for i, l := range o {
		oOff[i+1] = oOff[i] + len(l)
	}
	var edits []Edit
	for _, h := range hunks {
		start, end := oOff[h.oStart], oOff[h.oEnd]
		text := strings.Join(n[h.xStart:h.xEnd], "")
		m := MinimalEdit(old[start:end], text)
		if m.Start == m.End && m.Text == "" {
			continue
		}
		edits = append(edits, Edit{Start: start + m.Start, End: start + m.End, Text: m.Text})
	}
	return edits
}
