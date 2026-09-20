package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func scrollModel() *Model {
	m := testModel()
	m.width, m.height = 120, 40
	m.snapshot.Threads[0].Activity[0].Text = strings.Repeat("Transcript line\n", 80)
	m.snapshot.Threads[0].Requests[0].Questions[0].Text = strings.Repeat("Question line\n", 30)
	m.snapshot.Threads[0].Activity[0].Detail = strings.Repeat("Inspector line\n", 80)
	m.openSurface("activity", m.snapshot.Threads[0].Activity[0].ID)
	m.configureInputs()
	return m
}

func TestScrollEdgesDoNotAccumulateDebt(t *testing.T) {
	for _, area := range []string{"transcript", "detail", "request"} {
		t.Run(area, func(t *testing.T) {
			m := scrollModel()
			f, v := m.measure(), m.viewState()
			r, offset, limit := f.transcript, &v.Scroll, f.transcriptMax
			switch area {
			case "detail":
				r, offset, limit = f.detail, &v.DetailScroll, f.detailMax
			case "request":
				r, offset, limit = f.request, &v.RequestScroll, f.requestMax
			}
			if limit < 6 {
				t.Fatalf("fixture must scroll: %d", limit)
			}
			*offset = limit
			generation := m.state.Generation
			for range 100 {
				m.Update(tea.MouseWheelMsg{X: r.X, Y: r.Y, Button: tea.MouseWheelDown})
			}
			if *offset != limit || m.state.Generation != generation {
				t.Fatal("scrolling past bottom changed position or dirtied saved state")
			}
			m.Update(tea.MouseWheelMsg{X: r.X, Y: r.Y, Button: tea.MouseWheelUp})
			if *offset != limit-3 {
				t.Fatalf("reverse scroll did not move immediately: %d", *offset)
			}
			*offset = 100000 // Previously persisted overscroll must also recover.
			m.Update(tea.MouseWheelMsg{X: r.X, Y: r.Y, Button: tea.MouseWheelUp})
			if *offset != limit-3 {
				t.Fatal("legacy overscroll was not clamped before applying input")
			}
		})
	}
}

func TestScrollEndAndRestorationUseActualBounds(t *testing.T) {
	m := scrollModel()
	for _, focus := range []string{"transcript", "right-body"} {
		m.setFocus(focus)
		m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
		f, offset, limit := m.measure(), m.viewState().Scroll, m.measure().transcriptMax
		if focus == "right-body" {
			offset, limit = m.viewState().DetailScroll, f.detailMax
		}
		if offset != limit {
			t.Fatalf("End set %d, wanted %d", offset, limit)
		}
		m.key(tea.KeyPressMsg{Code: tea.KeyUp})
		offset = m.viewState().Scroll
		if focus == "right-body" {
			offset = m.viewState().DetailScroll
		}
		if offset != limit-1 {
			t.Fatal("Up after End did not move immediately")
		}
	}
	m.viewState().Scroll = 100000
	m.prompt.SetValue("preserved draft")
	m.viewState().Draft = m.prompt.Value()
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	restored.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if restored.viewState().Scroll != restored.measure().transcriptMax || restored.prompt.Value() != "preserved draft" {
		t.Fatal("restoration failed to repair bounds or preserved draft")
	}
	restored.Update(tea.WindowSizeMsg{Width: 160, Height: 100})
	if restored.viewState().Scroll > restored.measure().transcriptMax {
		t.Fatal("resize retained an out-of-range scroll offset")
	}
	s := restored.snapshot
	s.Revision++
	s.Threads[0].Activity = []protocol.Activity{{Text: "short"}}
	restored.Update(snapshotMsg(s))
	if restored.viewState().Scroll != 0 {
		t.Fatal("content shrink retained an out-of-range scroll offset")
	}
}

func TestRestorationWaitsForActualViewport(t *testing.T) {
	m := testModel()
	m.state.Layout.Left = false
	m.snapshot.Threads[0].Activity[0].Text = strings.Repeat("A long activity paragraph that wraps. ", 500)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	scroll := m.measure().transcriptMax
	m.viewState().Scroll = scroll
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if restored.measure().transcriptMax >= scroll {
		t.Fatal("fixture must have a shorter provisional scroll range")
	}
	if restored.viewState().Scroll != scroll {
		t.Fatal("provisional dimensions discarded the saved reading position")
	}
	restored.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	if restored.viewState().Scroll != scroll {
		t.Fatal("restoration changed the valid position at the actual size")
	}
}

func TestMeasurementMatchesPaintedControls(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}, {35, 12}} {
		for _, menu := range []bool{false, true} {
			m := scrollModel()
			m.width, m.height = size[0], size[1]
			if menu {
				m.openCommands()
			}
			measured, painted := m.measure(), m.render()
			painted.rows = nil
			if !reflect.DeepEqual(measured, painted) {
				t.Fatalf("hit testing diverged at %v menu=%v", size, menu)
			}
		}
	}
}

func TestPacedFramesKeepInputImmediateAndUseOneTimer(t *testing.T) {
	m := scrollModel()
	m.setFocus("prompt")
	p := pace(m)
	before := p.View().Content
	f := m.measure()
	for i := range 500 {
		_, cmd := p.Update(tea.MouseWheelMsg{X: f.transcript.X, Y: f.transcript.Y, Button: tea.MouseWheelDown})
		if (cmd != nil) != (i == 0) {
			t.Fatal("burst scheduled more than one redraw timer")
		}
		if p.View().Content != before {
			t.Fatal("frame was rebuilt for individual input")
		}
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyF8})
	p.Update(tea.PasteMsg{Content: "latest draft"})
	if !m.state.Light || m.prompt.Value() != "latest draft" {
		t.Fatal("input was delayed behind redraw")
	}
	p.Update(redrawMsg{})
	if p.pending || p.View().Content != m.View().Content {
		t.Fatal("redraw did not render latest state or left an idle timer")
	}
	_, cmd := p.Update(tea.MouseWheelMsg{X: f.transcript.X, Y: f.transcript.Y, Button: tea.MouseWheelUp})
	if cmd == nil || !p.pending {
		t.Fatal("input after idle did not schedule another redraw")
	}
}

func BenchmarkWheelBurst(b *testing.B) {
	m := scrollModel()
	p := pace(m)
	f := m.measure()
	event := tea.MouseWheelMsg{X: f.transcript.X, Y: f.transcript.Y, Button: tea.MouseWheelDown}
	p.Update(event) // Model an input burst between display frames.
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p.Update(event)
		p.View()
	}
}
