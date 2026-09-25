package tui

import (
	"slices"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// git_graph.go lays out the commit graph beside RECENT COMMITS. Layout is a
// pure function of the loaded log, run once when the log arrives and cached
// in gitView; painting only clips and colors the cached cells.
//
// Encoding: exactly one terminal row per commit, so graph rows stay 1:1 with
// commit rows (and their focus keys). Each lane is two cells, a lane cell
// and a connector cell to its right. Connections that git log --graph draws
// on separate rows are drawn in the commit's own row instead: the commit's
// node, a horizontal run through connector cells (crossing untouched lanes
// with ┼), and an end glyph in each connected lane cell:
//
//	╯ ╰  a lane that expected this commit ends here, merging into it
//	╮    an extra parent opens a new lane to the right (continues below)
//	┤ ├  an extra parent joins a lane that already expects it
//	┬ ┴  as ╮ and ╯/╰ where the run continues to a farther lane
//
// A lane whose expected commit is not in the loaded list (a parent beyond
// the limit) is drawn dashed (┆) for its whole remaining length.

// Graph cell kinds; graphGlyph maps them to rich and plain glyphs.
const (
	graphBlank byte = iota
	graphNode
	graphHead
	graphVert
	graphDashed
	graphHoriz
	graphCross
	graphDownRight // ╭
	graphDownLeft  // ╮
	graphUpRight   // ╰
	graphUpLeft    // ╯
	graphTeeRight  // ├
	graphTeeLeft   // ┤
	graphTeeDown   // ┬ a new lane with the run continuing past it
	graphTeeUp     // ┴ an ending lane with the run continuing past it
)

// graphCell is one cell: its kind and a lane color index.
type graphCell struct {
	kind  byte
	color int
}

// gitGraph is the cached layout for one log.
type gitGraph struct {
	rows  [][]graphCell // two cells per lane, trailing blanks trimmed
	lanes int           // widest row, in lanes
	col   []int         // the commit's lane per row
}

// layoutGitGraph assigns lanes. lanes[i] is the hash lane i expects next
// ("" when free); a commit takes the leftmost lane expecting it, or the
// leftmost free lane. Other lanes expecting it end in it. Its first parent
// continues its lane; each further parent joins a lane already expecting
// it, or opens the first free lane to the right.
func layoutGitGraph(commits []protocol.GitCommit) *gitGraph {
	known := make(map[string]bool, len(commits))
	for _, c := range commits {
		known[c.Hash] = true
	}
	g := &gitGraph{}
	var lanes []string
	var colors []int
	next := 0
	open := func(i int, hash string) {
		for len(lanes) <= i {
			lanes, colors = append(lanes, ""), append(colors, 0)
		}
		lanes[i], colors[i] = hash, next
		next++
	}
	free := func(from int) int {
		for i := from; i < len(lanes); i++ {
			if lanes[i] == "" {
				return i
			}
		}
		return max(from, len(lanes))
	}
	for _, c := range commits {
		before := slices.Clone(lanes)
		beforeColors := slices.Clone(colors)
		col := slices.Index(lanes, c.Hash)
		if col < 0 {
			col = free(0)
			open(col, c.Hash)
			before = append(before, make([]string, max(0, len(lanes)-len(before)))...)
			beforeColors = append(beforeColors, make([]int, max(0, len(colors)-len(beforeColors)))...)
		}
		nodeColor := colors[col]
		type link struct {
			lane int
			kind byte
		}
		var links []link
		for j := range lanes {
			if j != col && lanes[j] == c.Hash {
				lanes[j] = ""
				kind := graphUpLeft
				if j < col {
					kind = graphUpRight
				}
				links = append(links, link{j, kind})
			}
		}
		if len(c.Parents) == 0 {
			lanes[col] = ""
		} else {
			lanes[col] = c.Parents[0]
		}
		for _, p := range c.Parents[min(1, len(c.Parents)):] {
			if p == c.Parents[0] {
				continue
			}
			if k := slices.Index(lanes, p); k >= 0 && k != col {
				kind := graphTeeLeft
				if k < col {
					kind = graphTeeRight
				}
				if !slices.ContainsFunc(links, func(l link) bool { return l.lane == k }) {
					links = append(links, link{k, kind})
				}
				continue
			}
			// A lane ending in this commit is freed above but still drawn
			// in this row; never reuse it for a new parent here.
			k := free(col + 1)
			for slices.ContainsFunc(links, func(l link) bool { return l.lane == k }) {
				k = free(k + 1)
			}
			open(k, p)
			links = append(links, link{k, graphDownLeft})
		}
		for len(before) < len(lanes) {
			before, beforeColors = append(before, ""), append(beforeColors, 0)
		}
		// Paint the row.
		n := len(lanes)
		row := make([]graphCell, 2*n)
		lo, hi := col, col
		isLink := map[int]byte{}
		for _, l := range links {
			lo, hi = min(lo, l.lane), max(hi, l.lane)
			isLink[l.lane] = l.kind
		}
		laneColor := func(i int) int {
			if i < len(before) && before[i] != "" {
				return beforeColors[i]
			}
			return colors[i]
		}
		// linkColor is the color of the nearest link beyond x, away from col.
		linkColor := func(x int) int {
			best, dist := nodeColor, -1
			for _, l := range links {
				if (l.lane > col) == (x > col) {
					d := l.lane - x
					if x < col {
						d = x - l.lane
					}
					if d >= 0 && (dist < 0 || d < dist) {
						best, dist = laneColor(l.lane), d
					}
				}
			}
			return best
		}
		for i := 0; i < n; i++ {
			through := before[i] != "" && lanes[i] != "" && before[i] == lanes[i] && i != col
			vert := graphVert
			if through && !known[lanes[i]] {
				vert = graphDashed
			}
			switch kind, ok := isLink[i]; {
			case i == col:
				row[2*i] = graphCell{graphNode, nodeColor}
				if slices.Contains(c.Refs, "HEAD") {
					row[2*i].kind = graphHead
				}
			case ok && i > lo && i < hi:
				// The run continues past this lane toward a farther link.
				switch kind {
				case graphDownLeft, graphDownRight:
					kind = graphTeeDown
				case graphUpLeft, graphUpRight:
					kind = graphTeeUp
				default:
					kind = graphCross
				}
				row[2*i] = graphCell{kind, laneColor(i)}
			case ok:
				row[2*i] = graphCell{kind, laneColor(i)}
			case through && i > lo && i < hi:
				row[2*i] = graphCell{graphCross, laneColor(i)}
			case through:
				row[2*i] = graphCell{vert, laneColor(i)}
			case i > lo && i < hi:
				row[2*i] = graphCell{graphHoriz, linkColor(i)}
			}
			if i >= lo && i < hi {
				x := i
				if i < col {
					x = i
				} else {
					x = i + 1
				}
				row[2*i+1] = graphCell{graphHoriz, linkColor(x)}
			}
		}
		// Trim free trailing lanes so later rows stay narrow.
		for len(lanes) > 0 && lanes[len(lanes)-1] == "" {
			lanes, colors = lanes[:len(lanes)-1], colors[:len(colors)-1]
		}
		for len(row) >= 2 && row[len(row)-1].kind == graphBlank && row[len(row)-2].kind == graphBlank {
			row = row[:len(row)-2]
		}
		g.rows = append(g.rows, row)
		g.col = append(g.col, col)
		g.lanes = max(g.lanes, (len(row)+1)/2)
	}
	return g
}

// gitGraphMaxLanes is how many lanes a surface width shows before the
// overflow marker.
func gitGraphMaxLanes(width int) int { return max(1, min(6, width/6)) }

// graphGlyph is a cell's glyph in the rich or plain icon set. Box drawing
// characters are East Asian Ambiguous width; terminals set to render them
// wide will misalign the graph (documented limit).
func graphGlyph(kind byte, plain bool) string {
	rich := [...]string{" ", "●", "◉", "│", "┆", "─", "┼", "╭", "╮", "╰", "╯", "├", "┤", "┬", "┴"}
	ascii := [...]string{" ", "*", "@", "|", ":", "-", "+", "/", "\\", "\\", "/", "+", "+", "+", "+"}
	if int(kind) >= len(rich) {
		return " "
	}
	if plain {
		return ascii[kind]
	}
	return rich[kind]
}

// gitGraphWidth is the cell width reserved for the graph at a surface width.
func gitGraphWidth(g *gitGraph, width int) int {
	if g == nil || g.lanes == 0 {
		return 0
	}
	max := gitGraphMaxLanes(width)
	if g.lanes > max {
		return 2*max + 1
	}
	return 2 * g.lanes
}

// gitGraphRow clips one row to the visible lanes. The returned cells are
// exactly gitGraphWidth wide; overflow is a muted › (or >), replaced by the
// node when the commit itself is in a hidden lane. overflow reports which
// cell, if any, is the overflow marker (-1 when none).
func gitGraphRow(g *gitGraph, row, width int) (cells []graphCell, overflow int) {
	w := gitGraphWidth(g, width)
	cells = make([]graphCell, w)
	overflow = -1
	if row < 0 || row >= len(g.rows) {
		return cells, overflow
	}
	src := g.rows[row]
	max := gitGraphMaxLanes(width)
	copy(cells, src[:min(len(src), 2*max)])
	if g.lanes > max {
		hidden := false
		for _, c := range src[min(len(src), 2*max):] {
			hidden = hidden || c.kind != graphBlank
		}
		if g.col[row] >= max {
			cells[w-1] = src[2*g.col[row]]
		} else if hidden {
			overflow = w - 1
		}
	}
	return cells, overflow
}

// gitGraphText renders a whole layout as plain text, one line per commit;
// used by golden tests and surfaceText.
func gitGraphText(g *gitGraph, width int, plain bool) []string {
	var out []string
	for i := range g.rows {
		cells, overflow := gitGraphRow(g, i, width)
		var b strings.Builder
		for j, c := range cells {
			if j == overflow {
				if plain {
					b.WriteString(">")
				} else {
					b.WriteString("›")
				}
				continue
			}
			b.WriteString(graphGlyph(c.kind, plain))
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// paintGitGraph paints a commit row's clipped graph cells at x.
func (m *Model) paintGitGraph(f *frame, x, y, width int, r *gitRow, bg string) {
	p := m.colors()
	inks := []string{p.blue, p.green, p.gold, p.violet, p.cyan, p.pink}
	cells, overflow := gitGraphRow(r.graph, r.graphRow, width)
	for i, c := range cells {
		if i == overflow {
			glyph := "›"
			if m.plainIcons {
				glyph = ">"
			}
			f.text(x+i, y, 1, glyph, p.muted, bg)
			continue
		}
		if c.kind == graphBlank {
			continue
		}
		f.text(x+i, y, 1, graphGlyph(c.kind, m.plainIcons), inks[c.color%len(inks)], bg)
	}
}
