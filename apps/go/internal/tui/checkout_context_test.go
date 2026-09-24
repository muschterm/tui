package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// TestClosedBannerReopenReservesBracketFallback checks that the closed-thread
// banner's Reopen control gets a visible boundary under plain icons, NO_COLOR
// and 16-color output via the shared compactControl bracket fallback, and
// that switching profiles never changes its hit-rect geometry.
func TestClosedBannerReopenReservesBracketFallback(t *testing.T) {
	rich := navigationModel()
	rich.snapshot.Threads[0].Closed = true
	rich.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	rich.colorProfile = colorprofile.TrueColor
	rich.plainIcons = false
	richFrame := rich.render()
	richHit := controlHit(t, richFrame, "thread-reopen")
	richText := ansi.Strip(cutCells(richFrame.rows[richHit.Rect.Y], richHit.Rect.X, richHit.Rect.X+richHit.Rect.W))
	if strings.ContainsAny(richText, "[]") {
		t.Fatal("rich Reopen control should not show bracket fallback:", richText)
	}
	if !strings.Contains(richText, "Reopen") {
		t.Fatal("rich Reopen control lost its label:", richText)
	}

	for _, tc := range []struct {
		name    string
		profile colorprofile.Profile
		plain   bool
	}{
		{"16 colors", colorprofile.ANSI, false},
		{"no color / no tty", colorprofile.NoTTY, false},
		{"plain icons", colorprofile.TrueColor, true},
	} {
		m := navigationModel()
		m.snapshot.Threads[0].Closed = true
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m.colorProfile = tc.profile
		m.plainIcons = tc.plain
		f := m.render()
		h := controlHit(t, f, "thread-reopen")
		if h.Rect != richHit.Rect {
			t.Fatalf("%s: Reopen geometry changed: got %+v, want %+v", tc.name, h.Rect, richHit.Rect)
		}
		text := ansi.Strip(cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W))
		if !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
			t.Fatalf("%s: Reopen lacks reserved bracket end cells: %q", tc.name, text)
		}
		if !strings.Contains(text, "Reopen") {
			t.Fatalf("%s: Reopen lost its label: %q", tc.name, text)
		}
	}
}
