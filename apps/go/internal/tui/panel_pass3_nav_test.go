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

// TestPanelPass3NavCaptures writes the closed banner, checkout context states,
// compact column picker and empty thread list for visual review.
func TestPanelPass3NavCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for panel pass 3 navigation captures")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	checkouts := map[string]protocol.WorkspaceInfo{
		"branch":   {Path: "/work/alpha", Kind: "checkout", State: "branch", Branch: "feature/panel-界面"},
		"detached": {Path: "/work/alpha", Kind: "checkout", State: "detached", Revision: "53f6390"},
		"nongit":   {Path: "/work/alpha", Kind: "checkout", State: "non-git"},
		"worktree": {Path: "/work/alpha-wt", Kind: "worktree", State: "unborn", Branch: "main"},
	}
	type scenario struct {
		name          string
		width, height int
		light, plain  bool
		setup         func(m *Model)
	}
	var scenarios []scenario
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{120, 32}, {48, 24}} {
			scenarios = append(scenarios,
				scenario{"closed", size[0], size[1], light, false, func(m *Model) {
					m.snapshot.Threads[0].Closed = true
					m.snapshot.Threads[0].State = "idle"
					m.snapshot.Threads[0].Queue, m.snapshot.Threads[0].Requests, m.snapshot.Threads[0].Children = nil, nil, nil
				}},
				scenario{"empty", size[0], size[1], light, false, func(m *Model) {
					m.snapshot.Threads = nil
					m.state.Active = ""
				}},
				scenario{"fixture", size[0], size[1], light, false, func(m *Model) {}},
				scenario{"loading", size[0], size[1], light, false, func(m *Model) {
					m.checkoutKey, _, _ = m.checkoutTarget()
					m.checkoutLoading = true
				}},
			)
			for name, info := range checkouts {
				info := info
				scenarios = append(scenarios, scenario{name, size[0], size[1], light, false, func(m *Model) {
					m.checkoutKey, _, _ = m.checkoutTarget()
					m.checkoutInfo = info
				}})
			}
		}
		scenarios = append(scenarios, scenario{"columns", 48, 24, light, false, func(m *Model) { m.openColumns() }})
	}
	scenarios = append(scenarios,
		scenario{"plain-closed", 120, 32, false, true, func(m *Model) { m.snapshot.Threads[0].Closed = true }},
		scenario{"plain-empty", 48, 24, false, true, func(m *Model) { m.snapshot.Threads, m.state.Active = nil, "" }},
		scenario{"plain-columns", 48, 24, false, true, func(m *Model) { m.openColumns() }},
	)
	for _, s := range scenarios {
		m := navigationModel()
		m.state.Light = s.light
		if s.plain {
			m.plainIcons = true
			m.colorProfile = colorprofile.NoTTY
		}
		m.Update(tea.WindowSizeMsg{Width: s.width, Height: s.height})
		s.setup(m)
		m.configureInputs()
		name := fmt.Sprintf("%dx%d-light%t-%s.ansi", s.width, s.height, s.light, s.name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func emptyThreadsModel(width, height int) *Model {
	m := navigationModel()
	m.snapshot.Threads, m.state.Active = nil, ""
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.configureInputs()
	return m
}

func TestEmptyThreadsBandedButtonsAndFallbacks(t *testing.T) {
	keys := []string{"empty-new", "empty-closed", "empty-project"}
	kinds := []string{"thread-create", "closed-threads", "project-add"}
	for _, tc := range []struct {
		name    string
		profile colorprofile.Profile
		plain   bool
		height  int
	}{
		{"banded", colorprofile.TrueColor, false, 32},
		{"ansi16", colorprofile.ANSI, false, 32},
		{"nocolor", colorprofile.NoTTY, true, 32},
		{"minimum", colorprofile.TrueColor, false, 22},
	} {
		m := emptyThreadsModel(120, tc.height)
		m.colorProfile, m.plainIcons = tc.profile, tc.plain
		f := m.render()
		text := ansi.Strip(strings.Join(f.rows, "\n"))
		if !strings.Contains(text, "NO THREAD SELECTED") || !strings.Contains(text, "Add project") {
			t.Fatal(tc.name, "heading or label missing")
		}
		banded := tc.profile >= colorprofile.ANSI256 && !tc.plain
		if banded != strings.Contains(text, "▄") {
			t.Fatal(tc.name, "band edges must need 256+ colors, symbols and room")
		}
		if tc.profile <= colorprofile.ANSI && !strings.Contains(text, "[Add project") {
			t.Fatal(tc.name, "low-color fallback lacks bracket end cells")
		}
		for i, key := range keys {
			h := controlHit(t, f, key)
			if h.Action.Kind != kinds[i] || (banded && h.Rect.H != 3) || (!banded && h.Rect.H != 1) {
				t.Fatal(tc.name, key, h)
			}
		}
	}
}

func TestCheckoutMarkDistinguishesStatesWithoutSuccess(t *testing.T) {
	m := navigationModel()
	p := m.colors()
	for _, tc := range []struct {
		state, plain string
		bright       bool
	}{
		{"branch", "G", true},
		{"detached", "G", true},
		{"non-git", ".", false},
		{"unavailable", "?", false},
	} {
		for _, plain := range []bool{false, true} {
			m.plainIcons = plain
			m.checkoutKey, _, _ = m.checkoutTarget()
			m.checkoutLoading = false
			m.checkoutInfo = protocol.WorkspaceInfo{Path: "/work/alpha", Kind: "checkout", State: tc.state, Branch: "main", Revision: "abc"}
			glyph, ink, value := m.checkoutMark()
			if (plain && glyph != tc.plain) || ink == p.green || (value == p.text) != tc.bright {
				t.Fatal(tc.state, plain, glyph, ink, value)
			}
		}
	}
	m.plainIcons = false
	m.checkoutLoading = true
	if glyph, _, value := m.checkoutMark(); glyph != "○" || value != p.muted {
		t.Fatal("loading is not pending", glyph)
	}
}

func TestClosedBannerAndCheckoutStayOneRowAndBounded(t *testing.T) {
	for _, width := range []int{120, 48} {
		m := navigationModel()
		m.snapshot.Threads[0].Closed = true
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m.checkoutKey, _, _ = m.checkoutTarget()
		m.checkoutInfo = protocol.WorkspaceInfo{Path: "/work/alpha", Kind: "checkout", State: "branch", Branch: "feature/界面-" + strings.Repeat("x", 80)}
		first := strings.Join(m.render().rows, "\n")
		for range 20 {
			m.render()
		}
		f := m.render()
		if again := strings.Join(f.rows, "\n"); len(again) != len(first) {
			t.Fatalf("%d: repeated painting grew the frame from %d to %d bytes", width, len(first), len(again))
		}
		for _, row := range f.rows {
			if w := ansi.StringWidth(row); w > width {
				t.Fatal(width, "row overflowed", w)
			}
		}
		text := ansi.Strip(strings.Join(f.rows, "\n"))
		if !strings.Contains(text, "This thread is closed") || !hasControl(f, "thread-reopen") {
			t.Fatal(width, "closed banner lost state or Reopen")
		}
		branch := controlHit(t, f, "checkout-branch")
		info := controlHit(t, f, "checkout-info")
		if branch.Rect.Y != info.Rect.Y || branch.Rect.X <= info.Rect.X+info.Rect.W {
			t.Fatal(width, "checkout context wrapped or overlapped", branch.Rect, info.Rect)
		}
	}
}
