package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// TestPanelR1Captures writes the consistency-round review captures: the
// transcript tool rows, the tool inspector, Terminal, Git, Agents and a
// menu with group separators.
func TestPanelR1Captures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"transcript", "inspector", "terminal", "git", "agents", "commands-menu"} {
		m := testModel()
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
		switch scenario {
		case "inspector":
			m.openSurface("activity", "")
			m.viewState().DetailID = "mcp-fixture"
		case "terminal":
			m.openSurface("terminal", "")
			active, _ := m.viewState().Host.Active()
			m.snapshot.Terminals = append(m.snapshot.Terminals, protocol.Terminal{ID: active.ID, ThreadID: m.state.Active, State: "running", Controller: "tui-go · this client", Output: "$ go test ./internal/tui\nok\n$ "})
		case "git":
			m.openSurface("git", "")
		case "agents":
			finishActivity(m)
			m.openSurface("agents", "")
		case "commands-menu":
			m.openCommands()
		}
		m.configureInputs()
		name := fmt.Sprintf("%dx%d-r1-%s.ansi", m.width, m.height, scenario)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// The right-host Terminal opens with its section heading like the other
// surfaces; the bottom panel reuses the body without it and gains no rows.
func TestTerminalHeadingOnlyInRightHost(t *testing.T) {
	m := testModel()
	m.snapshot.Terminals = append(m.snapshot.Terminals, protocol.Terminal{ID: "term-x", State: "running", Controller: "tui", Output: "$ "})
	host := m.surfaceBlocks(shell.Surface{Kind: "terminal", ID: "term-x"})
	if host[0].kind != surfaceHeadingBlock || host[0].label != "Terminal" || host[1].kind != surfaceGapBlock {
		t.Fatalf("host starts %+v", host[:2])
	}
	bottom := m.bottomTerminalBlocks("term-x")
	if len(bottom) != 5 || bottom[0].kind != surfaceStatusBlock {
		t.Fatalf("bottom panel blocks changed: %+v", bottom)
	}
	for _, b := range bottom {
		if b.kind == surfaceHeadingBlock {
			t.Fatalf("bottom panel gained a heading: %+v", b)
		}
	}
}

// Parent names the known thread by title and falls back to its ID.
func TestAgentsParentShowsThreadTitle(t *testing.T) {
	m := testModel()
	parent := func() string {
		for _, b := range m.surfaceBlocks(shell.Surface{Kind: "agents"}) {
			if b.label == "Parent" {
				return b.value
			}
		}
		return ""
	}
	th := m.thread()
	if len(th.Children) == 0 || th.Children[0].ParentID != th.ID || th.Title == "" {
		t.Fatalf("fixture changed: %+v", th.Children)
	}
	if got := parent(); got != th.Title {
		t.Fatalf("Parent = %q, want %q", got, th.Title)
	}
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID == m.state.Active {
			m.snapshot.Threads[i].Children[0].ParentID = "thread-gone"
		}
	}
	if got := parent(); got != "thread-gone" {
		t.Fatalf("unknown parent = %q", got)
	}
}

// Loading and unavailable checkout facts are muted Branch pairs, never a
// status row or success ink.
func TestGitCheckoutFactsArePairs(t *testing.T) {
	m := testModel()
	p := m.colors()
	for _, loading := range []bool{true, false} {
		m.checkoutLoading = loading
		m.checkoutInfo = protocol.WorkspaceInfo{Path: "/src/r", State: "error"}
		m.checkoutKey = ""
		blocks := m.gitCheckoutBlocks()
		last := blocks[len(blocks)-1]
		want := "unavailable"
		if loading {
			want = "loading…"
		}
		if last.kind != surfacePairBlock || last.label != "Branch" || last.value != want || last.ink != p.muted {
			t.Fatalf("loading=%t: %+v", loading, last)
		}
	}
}

// Menu group separators span the same cells as the heading and hint rules.
func TestMenuSeparatorMatchesRuleSpan(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.openCommands()
	r := m.menuRect()
	var spans []int
	for _, row := range m.render().rows[r.Y+1 : r.Y+r.H-1] {
		plain := ansi.Cut(ansi.Strip(row), r.X, r.X+r.W)
		if n := longestRun(plain, '─'); n > 10 {
			spans = append(spans, n)
		}
	}
	if len(spans) < 3 {
		t.Fatalf("expected heading, separator and hint rules, got %v", spans)
	}
	for _, n := range spans {
		if n != spans[0] {
			t.Fatalf("rule spans differ: %v", spans)
		}
	}
}

func longestRun(s string, r rune) int {
	best, run := 0, 0
	for _, c := range s {
		if c == r {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}
