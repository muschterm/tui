package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func paintedRow(m *Model, width int, row surfaceRow) string {
	f := &frame{rows: []string{strings.Repeat(" ", width)}}
	m.paintSurfaceRow(f, 0, 0, width, row)
	return f.rows[0]
}

// The color caveat folds into the Colors value as a muted suffix when it
// fits, else continues right-aligned below the value; NO_COLOR is one pair.
func TestColorNoteBelongsToColorsValue(t *testing.T) {
	m := testModel()
	m.colorProfile = colorprofile.ANSI256
	rows := m.surfaceRows(colorBlocks(m), 60)
	if len(rows) != 1 || rows[0].kind != surfacePairRow || rows[0].value != "ANSI256 fallback" || rows[0].note != "true color unconfirmed" {
		t.Fatalf("wide rows = %+v", rows)
	}
	painted := paintedRow(m, 60, rows[0])
	if !strings.HasSuffix(ansi.Strip(painted), "ANSI256 fallback · true color unconfirmed") {
		t.Fatalf("wide painted = %q", ansi.Strip(painted))
	}
	if !strings.Contains(painted, style(m.colors().muted, m.colors().panel).Render(" · true color unconfirmed")) {
		t.Fatalf("note not muted: %q", painted)
	}
	rows = m.surfaceRows(colorBlocks(m), 38)
	if len(rows) != 2 || rows[0].value != "ANSI256 fallback" || rows[0].note != "" ||
		rows[1].kind != surfacePairRow || rows[1].text != "" || rows[1].value != "true color unconfirmed" || rows[1].ink != m.colors().muted {
		t.Fatalf("narrow rows = %+v", rows)
	}
	if got := ansi.Strip(paintedRow(m, 38, rows[1])); got != strings.Repeat(" ", 38-22)+"true color unconfirmed" {
		t.Fatalf("continuation not right-aligned: %q", got)
	}
	m.colorProbe.noColor = true
	if rows = m.surfaceRows(colorBlocks(m), 60); len(rows) != 1 || rows[0].value != "disabled by NO_COLOR" || rows[0].note != "" {
		t.Fatalf("NO_COLOR rows = %+v", rows)
	}
}

// The inspector header's state word takes its status mark's ink; unmarked
// and neutral states stay muted.
func TestStatusHeaderStateUsesMarkInk(t *testing.T) {
	m := testModel()
	p := m.colors()
	for state, want := range map[string]string{"failed": "", "interrupted": "", "completed": "", "unknown": p.muted, "idle": p.muted} {
		b := statusBlock(m, "Run go test", state, true)
		if want == "" {
			want = b.ink
			if want == p.muted {
				t.Fatalf("%s: mark ink is muted", state)
			}
		}
		rows := m.surfaceRows([]surfaceBlock{b}, 60)
		if !strings.Contains(paintedRow(m, 60, rows[0]), style(want, p.panel).Render(state)) {
			t.Fatalf("%s: state word not in %q", state, want)
		}
	}
}
