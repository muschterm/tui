package tui

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// graphCommits builds a log from "hash:parent,parent" tokens, newest first;
// a trailing * marks HEAD.
func graphCommits(spec string) []protocol.GitCommit {
	var out []protocol.GitCommit
	for _, tok := range strings.Fields(spec) {
		c := protocol.GitCommit{Parents: []string{}}
		if strings.HasSuffix(tok, "*") {
			tok = strings.TrimSuffix(tok, "*")
			c.Refs = []string{"HEAD"}
		}
		hash, parents, _ := strings.Cut(tok, ":")
		c.Hash, c.Short, c.Subject = hash, hash, hash
		if parents != "" {
			c.Parents = strings.Split(parents, ",")
		}
		out = append(out, c)
	}
	return out
}

func TestGitGraphGoldens(t *testing.T) {
	for _, tc := range []struct {
		name, spec  string
		width       int
		rich, plain string
	}{
		{"beyond", "m:d,z* d:y", 60,
			"◉─╮\n● ┆",
			"@-\\\n* :"},
		{"branchtips", "f:b g:b* b:a a", 60,
			"●\n│ ◉\n●─╯\n●",
			"*\n| @\n*-/\n*"},
		{"crisscross", "m1:a,b* m2:b,a a:r b:r r", 60,
			"◉─╮\n├─┼─●\n● │ │\n│ ●─╯\n●─╯",
			"@-\\\n+-+-*\n* | |\n| *-/\n*-/"},
		{"linear", "c:b* b:a a", 60,
			"◉\n●\n●",
			"@\n*\n*"},
		{"merge", "m:d,c* d:b c:b b:a a", 60,
			"◉─╮\n● │\n│ ●\n●─╯\n●",
			"@-\\\n* |\n| *\n*-/\n*"},
		{"octopus", "o:a,b,c* a:r b:r c:r r", 60,
			"◉─┬─╮\n● │ │\n│ ● │\n│ │ ●\n●─┴─╯",
			"@-+-\\\n* | |\n| * |\n| | *\n*-+-/"},
		{"overflow", "m:a,b,c,d,e,f,g,h* a:r b:r c:r d:r e:r f:r g:r h:r r", 30,
			"◉─┬─┬─┬─┬─›\n● │ │ │ │ ›\n│ ● │ │ │ ›\n│ │ ● │ │ ›\n│ │ │ ● │ ›\n│ │ │ │ ● ›\n│ │ │ │ │ ●\n│ │ │ │ │ ●\n│ │ │ │ │ ●\n●─┴─┴─┴─┴─›",
			"@-+-+-+-+->\n* | | | | >\n| * | | | >\n| | * | | >\n| | | * | >\n| | | | * >\n| | | | | *\n| | | | | *\n| | | | | *\n*-+-+-+-+->"},
		{"reuse", "x:w* w:v,t t:v v:u y:u u", 60,
			"◉\n●─╮\n│ ●\n●─╯\n│ ●\n●─╯",
			"@\n*-\\\n| *\n*-/\n| *\n*-/"},
	} {
		g := layoutGitGraph(graphCommits(tc.spec))
		if got := strings.Join(gitGraphText(g, tc.width, false), "\n"); got != tc.rich {
			t.Errorf("%s rich:\n%s\nwant\n%s", tc.name, got, tc.rich)
		}
		if got := strings.Join(gitGraphText(g, tc.width, true), "\n"); got != tc.plain {
			t.Errorf("%s plain:\n%s\nwant\n%s", tc.name, got, tc.plain)
		}
	}
}

// A new parent must not reuse a lane that ends in the same commit.
func TestGitGraphNewParentSkipsEndingLane(t *testing.T) {
	g := layoutGitGraph(graphCommits("a:x b:x x:p,q p q"))
	got := strings.Join(gitGraphText(g, 60, false), "\n")
	checkGraphInvariants(t, graphCommits("a:x b:x x:p,q p q"), g, 60)
	if want := "●\n│ ●\n●─┴─╮\n●   │\n    ●"; got != want {
		t.Fatalf("edge dropped:\n%s", got)
	}
}

// checkGraphInvariants verifies one layout: one node per row; every down
// stroke from a row meets an up stroke in the next row's same lane; the
// first parent continues the commit's lane; hidden cells show overflow;
// plain mode is ASCII-only.
func checkGraphInvariants(t *testing.T, commits []protocol.GitCommit, g *gitGraph, width int) {
	t.Helper()
	down := func(k byte) bool {
		switch k {
		case graphNode, graphHead:
			return true // checked separately via parents
		case graphVert, graphDashed, graphCross, graphDownLeft, graphDownRight, graphTeeRight, graphTeeLeft, graphTeeDown:
			return true
		}
		return false
	}
	up := func(k byte) bool {
		switch k {
		case graphNode, graphHead, graphVert, graphDashed, graphCross, graphUpLeft, graphUpRight, graphTeeRight, graphTeeLeft, graphTeeUp:
			return true
		}
		return false
	}
	cell := func(row, lane int) byte {
		if row >= len(g.rows) || 2*lane >= len(g.rows[row]) {
			return graphBlank
		}
		return g.rows[row][2*lane].kind
	}
	for i, row := range g.rows {
		nodes := 0
		for j := 0; j < len(row); j += 2 {
			if row[j].kind == graphNode || row[j].kind == graphHead {
				nodes++
			}
			if row[j+1].kind != graphBlank && row[j+1].kind != graphHoriz {
				t.Fatalf("row %d connector cell %d kind %d", i, j+1, row[j+1].kind)
			}
		}
		if nodes != 1 || cell(i, g.col[i]) != graphNode && cell(i, g.col[i]) != graphHead {
			t.Fatalf("row %d: %d nodes", i, nodes)
		}
		for lane := 0; 2*lane < len(row); lane++ {
			k := row[2*lane].kind
			isNode := lane == g.col[i]
			if i+1 < len(g.rows) && down(k) && !(isNode && len(commits[i].Parents) == 0) && !up(cell(i+1, lane)) {
				t.Fatalf("row %d lane %d: down stroke %d meets %d below", i, lane, k, cell(i+1, lane))
			}
			if i > 0 && up(k) && k != graphNode && k != graphHead && !down(cell(i-1, lane)) {
				t.Fatalf("row %d lane %d: up stroke %d has nothing above", i, lane, k)
			}
		}
		// First parent continues the lane: the next row in that lane is
		// either the parent itself or a stroke carrying on.
		if len(commits[i].Parents) > 0 && i+1 < len(g.rows) && !up(cell(i+1, g.col[i])) {
			t.Fatalf("row %d: first-parent lane broken", i)
		}
		cells, overflow := gitGraphRow(g, i, width)
		hidden := false
		max := gitGraphMaxLanes(width)
		for _, c := range row[min(len(row), 2*max):] {
			hidden = hidden || c.kind != graphBlank
		}
		if hidden && overflow < 0 && g.col[i] < max {
			t.Fatalf("row %d: hidden cells without overflow marker", i)
		}
		_ = cells
	}
	for _, line := range gitGraphText(g, width, true) {
		for _, r := range line {
			if r > 0x7e {
				t.Fatalf("plain graph has %q", r)
			}
		}
	}
}

func TestGitGraphRandomDAGs(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for n := 0; n < 1500; n++ {
		size := 2 + rng.IntN(30)
		commits := make([]protocol.GitCommit, size)
		for i := range commits {
			commits[i] = protocol.GitCommit{Hash: fmt.Sprintf("c%d", i), Parents: []string{}}
		}
		// Parents are always later rows (topological), or outside the list.
		for i := range commits {
			np := rng.IntN(4)
			if i == size-1 && np > 0 && rng.IntN(2) == 0 {
				np = 0
			}
			seen := map[string]bool{}
			for p := 0; p < np; p++ {
				var h string
				if i+1 < size && rng.IntN(5) > 0 {
					h = fmt.Sprintf("c%d", i+1+rng.IntN(size-i-1))
				} else {
					h = fmt.Sprintf("out%d", rng.IntN(3))
				}
				if !seen[h] {
					seen[h] = true
					commits[i].Parents = append(commits[i].Parents, h)
				}
			}
		}
		if rng.IntN(3) == 0 {
			commits[0].Refs = []string{"HEAD"}
		}
		g := layoutGitGraph(commits)
		for _, w := range []int{12, 30, 60} {
			checkGraphInvariants(t, commits, g, w)
		}
	}
}
