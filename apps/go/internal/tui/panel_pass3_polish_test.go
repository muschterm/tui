package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// TestPanelPass3PolishCaptures writes the closed-thread banner's Reopen
// control across color profiles for visual review: rich truecolor (no
// visible change expected) plus the ANSI/plain-icon fallbacks that now gain
// reserved bracket end cells.
func TestPanelPass3PolishCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for panel pass 3 polish captures")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	scenarios := []struct {
		name    string
		profile colorprofile.Profile
		plain   bool
	}{
		{"truecolor", colorprofile.TrueColor, false},
		{"ansi16", colorprofile.ANSI, false},
		{"no-color", colorprofile.NoTTY, false},
		{"plain-icons", colorprofile.TrueColor, true},
	}
	for _, light := range []bool{false, true} {
		for _, sc := range scenarios {
			m := navigationModel()
			m.state.Light = light
			m.snapshot.Threads[0].Closed = true
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			m.colorProfile = sc.profile
			m.plainIcons = sc.plain
			name := fmt.Sprintf("120x30-light%t-closed-banner-%s.ansi", light, sc.name)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
