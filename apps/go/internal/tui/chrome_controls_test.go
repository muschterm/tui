package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func chromeFrame(m *Model, paint bool) frame {
	f := frame{}
	if paint {
		f.rows = []string{strings.Repeat(" ", m.width)}
	}
	m.renderChrome(&f, m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()))
	return f
}

func TestChromeAttentionBadgeAndColors(t *testing.T) {
	for _, light := range []bool{false, true} {
		m := testModel()
		m.state.Light = light
		for i := range m.snapshot.Threads {
			m.snapshot.Threads[i].Requests = nil
			m.snapshot.Threads[i].State = "idle"
		}
		f := chromeFrame(m, true)
		p := colors(light)
		if strings.Contains(ansi.Strip(f.rows[0]), "Attention") || strings.Contains(ansi.Strip(f.rows[0]), " 0 ") {
			t.Fatal("attention has redundant text/zero badge")
		}
		h := controlHit(t, f, "attention")
		if got := ansi.Cut(f.rows[0], h.Rect.X+1, h.Rect.X+2); !strings.Contains(got, style(p.muted, p.nav).Render(m.icon("attention"))) {
			t.Fatalf("neutral bell color: %q", got)
		}
		m.snapshot.Threads[0].State = "failed"
		f = chromeFrame(m, true)
		h = controlHit(t, f, "attention")
		if !strings.Contains(f.rows[0], style(p.nav, p.gold).Render(" 1 ")) {
			t.Fatal("pending badge lacks distinct color")
		}
		if got := ansi.Cut(f.rows[0], h.Rect.X+1, h.Rect.X+2); !strings.Contains(got, style(p.gold, p.nav).Render(m.icon("attention"))) {
			t.Fatalf("pending bell color: %q", got)
		}
	}
}

func TestChromeVisibleControlsPackRightAndMatchMeasurement(t *testing.T) {
	for _, width := range []int{40, 47, 48, 60, 80, 120} {
		for _, right := range []bool{false, true} {
			m := testModel()
			m.width = width
			m.state.Layout.Right = right
			f := chromeFrame(m, true)
			if !reflect.DeepEqual(f.hits, chromeFrame(m, false).hits) {
				t.Fatal("measure and paint disagree")
			}
			att := controlHit(t, f, "attention")
			if m.singleColumn() {
				if !hasControl(f, "columns") || hasControl(f, "maximize") || att.Rect.X+att.Rect.W != width-1 {
					t.Fatal("compact chrome lost its picker or right-aligned attention")
				}
			} else {
				first := controlHit(t, f, "bottom")
				if hasControl(f, "maximize") {
					first = controlHit(t, f, "maximize")
				}
				if att.Rect.X+att.Rect.W+1 != first.Rect.X {
					t.Fatal("unused maximize slot left a gap")
				}
				end := controlHit(t, f, "right")
				if end.Rect.X+end.Rect.W != width {
					t.Fatal("pane controls not right aligned")
				}
			}
			for _, h := range f.hits {
				if h.Rect.X < 0 || h.Rect.X+h.Rect.W > width {
					t.Fatalf("out of bounds: %+v", h)
				}
			}
		}
	}
}
