package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// bottomTerminalModel shows the bottom panel with one recorded session.
func bottomTerminalModel(w, h int, light bool) *Model {
	m := testModel()
	m.state.Light = light
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.activate(action{Kind: "bottom"})
	m.Update(commandMsg{command: *m.busy, receipt: protocol.Receipt{TargetID: "term-bottom-1"}, local: action{Kind: "terminal-open", Value: "bottom"}})
	m.snapshot.Terminals = append(m.snapshot.Terminals, protocol.Terminal{ID: "term-bottom-1", ThreadID: m.state.Active, State: "running", Controller: "tui-go · this client",
		Output: "$ go test ./internal/tui\nok  \tgithub.com/muschterm/tui/apps/go/internal/tui\t15.2s\n$ git status --short\n M internal/tui/render.go\n$ "})
	m.configureInputs()
	return m
}

// TestPanelPass3DialogCaptures writes review captures for the bottom terminal
// panel, the add-project folder dialog and inline @ mentions.
func TestPanelPass3DialogCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{160, 45}, {47, 22}} {
		for _, light := range []bool{false, true} {
			for _, scene := range []string{"bottom", "folder", "mention"} {
				var m *Model
				switch scene {
				case "bottom":
					// Below the wide layout the terminal is a compact column,
					// so the narrow bottom capture uses the smallest wide size.
					w, h := size[0], size[1]
					if w < 100 {
						w, h = 100, 40
					}
					m = bottomTerminalModel(w, h, light)
				case "folder":
					m = mentionModel("Inspect")
					m.state.Light = light
					m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					m.activate(action{Kind: "project-add"})
					pathResults(m, protocol.BrowseResult{Directory: "/work", Entries: []protocol.PathEntry{{Name: "界 面", Path: "/work/界 面/", IsDir: true}, {Name: "MixedCase: repo", Path: "/work/MixedCase: repo/", IsDir: true}}})
				case "mention":
					m = mentionModel("Inspect @")
					m.state.Light = light
					m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Entries: []protocol.PathEntry{{Name: "src", Path: "src/", IsDir: true}, {Name: "README.md", Path: "README.md"}, {Name: "Notes: draft.md", Path: "Notes: draft.md"}}})
				}
				name := fmt.Sprintf("%dx%d-light%t-pass3-%s.ansi", m.width, m.height, light, scene)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// The bottom session body uses the right host's status and pair rows over a
// rule, with no gap rows, and keeps its fallbacks without colors or symbols.
func TestBottomTerminalMetadataPanelRows(t *testing.T) {
	for name, setup := range map[string]func(*Model){
		"default":     func(*Model) {},
		"plain icons": func(m *Model) { m.plainIcons = true },
		"16 colors":   func(m *Model) { m.colorProfile = colorprofile.ANSI },
		"no color":    func(m *Model) { m.colorProfile = colorprofile.NoTTY },
	} {
		m := bottomTerminalModel(160, 45, false)
		setup(m)
		f := m.render()
		b := f.bottomBody
		var rows []string
		for i := range 4 {
			rows = append(rows, strings.TrimRight(ansi.Strip(cutCells(f.rows[b.Y+i], b.X, b.X+b.W)), " "))
		}
		glyph := "●"
		if m.plainIcons {
			glyph = "*"
		}
		if !strings.HasPrefix(rows[0], glyph+" Terminal") || !strings.HasSuffix(rows[0], "running") {
			t.Fatalf("%s: status row = %q", name, rows[0])
		}
		if !strings.HasPrefix(rows[1], "Session") || !strings.HasSuffix(rows[1], "term-bottom-1") ||
			!strings.HasPrefix(rows[2], "Controller") || !strings.HasSuffix(rows[2], "tui-go · this client") {
			t.Fatalf("%s: pair rows = %q", name, rows[1:3])
		}
		if rows[3] == "" || strings.Trim(rows[3], "─-") != "" {
			t.Fatalf("%s: rule row = %q", name, rows[3])
		}
		if got := ansi.Strip(f.rows[b.Y+4]); !strings.Contains(got, "$ go test ./internal/tui") {
			t.Fatalf("%s: output does not follow the rule: %q", name, got)
		}
	}
}

// Terminal output and metadata stay sanitized text in the panel rows.
func TestBottomTerminalOutputStaysSanitized(t *testing.T) {
	m := bottomTerminalModel(160, 45, false)
	last := len(m.snapshot.Terminals) - 1
	m.snapshot.Terminals[last].Output = "safe\x1b]52;c;ZXZpbA==\x07 \x1b[31mred"
	m.snapshot.Terminals[last].Controller = "ctl\x1b[2J"
	joined := strings.Join(m.render().rows, "\n")
	if strings.Contains(joined, "\x1b]52") || strings.Contains(joined, "\x1b[2J") {
		t.Fatal("untrusted terminal text reached the frame as control sequences")
	}
}

// The mention popup uses a panel heading and rules; file names keep their
// case and are never split into pairs.
func TestMentionPopupPanelHeadingKeepsNames(t *testing.T) {
	m := mentionModel("Inspect @")
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Entries: []protocol.PathEntry{{Name: "Notes: Draft.md", Path: "Notes: Draft.md"}}})
	f := m.render()
	text := frameText(f)
	if !strings.Contains(text, "PROJECT FILES") || strings.Contains(text, "NOTES") || !strings.Contains(text, "Notes: Draft.md") {
		t.Fatalf("mention popup heading or names wrong:\n%s", text)
	}
	h := controlHit(t, f, "mention:0")
	if h.Rect.Y != m.mentionRect(f).Y+3 || !strings.Contains(ansi.Strip(f.rows[h.Rect.Y-1]), "───") {
		t.Fatalf("entries do not follow the heading rule: %+v", h.Rect)
	}
	// A popup too short for rules still shows its entry.
	short := mentionModel("Inspect @")
	short.Update(tea.WindowSizeMsg{Width: 47, Height: 13})
	pathResults(short, protocol.BrowseResult{Root: "/work/alpha", Entries: []protocol.PathEntry{{Name: "a.go", Path: "a.go"}}})
	sf := short.render()
	if r := short.mentionRect(sf); r.H >= 4 && !hasControl(sf, "mention:0") {
		t.Fatalf("short popup (%d rows) lost its entry", r.H)
	}
}

// Folder lookup status rows are painted in different (muted) ink from
// actionable rows such as Cancel.
func TestFolderDialogStatusRowsAreMuted(t *testing.T) {
	m := mentionModel("Inspect")
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	m.activate(action{Kind: "project-add"})
	pathResults(m, protocol.BrowseResult{Directory: "/work"})
	noop, cancel := -1, -1
	for i, item := range m.menu {
		switch item.Action.Kind {
		case "path-noop":
			noop = i
		case "project-cancel":
			cancel = i
		}
	}
	if noop < 0 || cancel < 0 {
		t.Fatalf("folder dialog rows missing: %+v", m.menu)
	}
	m.menuIndex = 0
	f := m.render()
	prefix := func(index int) string {
		h := controlHit(t, f, fmt.Sprintf("menu:%d", index))
		cell := cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+1)
		return cell
	}
	if prefix(noop) == prefix(cancel) {
		t.Fatalf("status row uses action ink: %q", prefix(noop))
	}
}
