package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// TestSurfaceBodyCaptures writes right-host surface body review captures
// (Git, Files, Usage, Plan and Agents) in both themes at a wide and a narrow
// host width.
func TestSurfaceBodyCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{160, 50}, {100, 40}} {
		for _, light := range []bool{false, true} {
			for _, kind := range []string{"git", "files", "usage", "plan", "agents"} {
				m := testModel()
				m.state.Light = light
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				if kind == "usage" {
					m.openSurface("activity", "")
					m.viewState().DetailID = "usage"
				} else {
					m.openSurface(kind, "")
				}
				m.configureInputs()
				name := fmt.Sprintf("%dx%d-light%t-%s.ansi", size[0], size[1], light, kind)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func gitRows(m *Model, width int) []surfaceRow {
	return m.surfaceRows(m.surfaceBlocks(shell.Surface{Kind: "git"}), width)
}

// Git checkout states render as explicit pairs or neutral marks; a branch
// never reads as success and paths stay unsplit and untransformed.
func TestGitSurfaceCheckoutStates(t *testing.T) {
	m := testModel()
	done, _ := panelStatusMark(m, "completed")
	key, _, _ := m.checkoutTarget()
	for _, tc := range []struct {
		info protocol.WorkspaceInfo
		want map[string]string
	}{
		{protocol.WorkspaceInfo{Path: "/src/My Repo: x", Kind: "checkout", State: "branch", Branch: "main\x1b[31m"}, map[string]string{"Branch": "main"}},
		{protocol.WorkspaceInfo{Path: "/src/r", Kind: "checkout", State: "detached", Revision: "abc1234"}, map[string]string{"HEAD": "detached", "Revision": "abc1234"}},
		{protocol.WorkspaceInfo{Path: "/src/r", Kind: "checkout", State: "unborn", Branch: "main"}, map[string]string{"Branch": "main", "HEAD": "unborn"}},
		{protocol.WorkspaceInfo{Path: "/src/r", Kind: "checkout", State: "non-git"}, map[string]string{"Repository": "not a Git checkout"}},
	} {
		m.checkoutKey, m.checkoutLoading, m.checkoutInfo = key, false, tc.info
		pairs := map[string]string{}
		var text []string
		for _, r := range gitRows(m, 38) {
			if strings.Contains(r.text+r.value, "\x1b") {
				t.Fatalf("control sequence survived: %q", r.text+r.value)
			}
			if r.glyph == done {
				t.Fatalf("%s rendered a success mark: %+v", tc.info.State, r)
			}
			if r.kind == surfacePairRow {
				pairs[r.text] = r.value
			}
			text = append(text, r.text)
		}
		for k, v := range tc.want {
			if pairs[k] != v {
				t.Fatalf("%s: pairs = %v, want %s=%q", tc.info.State, pairs, k, v)
			}
		}
		if !strings.Contains(strings.Join(text, "\n"), tc.info.Path) {
			t.Fatalf("path %q not rendered verbatim: %q", tc.info.Path, text)
		}
		if tc.info.State == "non-git" && !strings.Contains(m.surfaceText(shell.Surface{Kind: "git"}), "Repository: not a Git checkout") {
			t.Fatalf("non-git state missing")
		}
	}
}

// Short terminal diagnostics sit flush right of their labels in a narrow host.
func TestUsageTerminalPairsInline(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		m := testModel()
		m.colorProbe.noColor = noColor
		m.openSurface("activity", "")
		m.viewState().DetailID = "usage"
		pairs := map[string]string{}
		active, _ := m.viewState().Host.Active()
		for _, r := range m.surfaceRows(m.surfaceBlocks(active), 38) {
			if r.kind == surfacePairRow {
				pairs[r.text] = r.value
			}
		}
		want := map[string]string{"Keyboard": "legacy keyboard", "Graphics": "text fallback", "Image support": "not probed"}
		if noColor {
			want["Colors"] = "disabled by NO_COLOR"
		} else {
			want["Colors"] = "true color"
		}
		for k, v := range want {
			if pairs[k] != v {
				t.Fatalf("noColor=%t pairs = %v, want %s=%q", noColor, pairs, k, v)
			}
		}
	}
}
