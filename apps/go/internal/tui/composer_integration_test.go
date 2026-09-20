package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestComposerEnterSendsAndModifiedEnterGrows(t *testing.T) {
	m := testModel()
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "first"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m.Update(tea.PasteMsg{Content: "second"})
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m.Update(tea.PasteMsg{Content: "third"})
	if m.prompt.Value() != "first\nsecond\nthird" || m.promptRows != 3 || m.busy != nil {
		t.Fatal("newlines failed, did not grow or submitted prematurely")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.Kind != "prompt.send" || m.busy.Text != "first\nsecond\nthird" {
		t.Fatal("Enter did not capture exactly the visible draft")
	}
	if m.prompt.Value() != m.busy.Text {
		t.Fatal("draft changed before command acceptance")
	}
}

func TestComposerCapKeepsFooterAndCompleteDraft(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {120, 40}, {80, 30}, {60, 24}, {48, 22}} {
		for _, light := range []bool{false, true} {
			m := testModel()
			m.setFocus("prompt")
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			value := strings.Repeat("A complete draft line\n", 14) + "last line"
			m.Update(tea.PasteMsg{Content: value})
			f := m.render()
			if m.prompt.Value() != value || f.prompt.H > 8 || f.prompt.H < 1 {
				t.Fatalf("%v: draft truncated or invalid visible height %d", size, f.prompt.H)
			}
			if size[1] >= 40 && f.prompt.H != 8 {
				t.Fatalf("wide viewport did not grow to eight rows: %d", f.prompt.H)
			}
			if _, ok := f.scrollbars["prompt"]; !ok {
				t.Fatal("long draft missing scrollbar")
			}
			for _, h := range f.hits {
				if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > m.width || h.Rect.Y+h.Rect.H > m.height-1 {
					t.Errorf("%v: control clipped: %s %+v", size, h.Key, h.Rect)
				}
			}
			for i, line := range f.rows {
				if ansi.StringWidth(line) != m.width {
					t.Errorf("%v row %d has wrong cell width", size, i)
				}
			}
		}
	}
}

func TestPromptOutlineSeparatesTypingFromControls(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}} {
		m := testModel()
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.setFocus("prompt")
		m.Update(tea.PasteMsg{Content: strings.Repeat("Keep my draft\n", 12)})
		f := m.render()
		send := controlHit(t, f, "send")
		if send.Rect.Y != f.prompt.Y+f.prompt.H+1 {
			t.Fatal("footer is not below the prompt outline")
		}
		for _, edge := range []struct {
			row         int
			left, right string
		}{
			{f.prompt.Y - 1, "╭", "╮"}, {f.prompt.Y + f.prompt.H, "╰", "╯"},
		} {
			line := ansi.Strip(f.rows[edge.row])
			if !strings.Contains(line, edge.left) || !strings.Contains(line, edge.right) {
				t.Fatal("incomplete prompt outline", line)
			}
		}
		bar, ok := f.scrollbars["prompt"]
		if !ok || bar.Rect.X >= f.geom.Center.X+f.geom.Center.W-2 {
			t.Fatal("prompt scrollbar overlaps outer border")
		}
	}
}

func TestComposerManualScrollSurvivesSnapshotsAndKeepsInsertion(t *testing.T) {
	m := testModel()
	m.setFocus("prompt")
	value := strings.Repeat("line\n", 14) + "end"
	m.Update(tea.PasteMsg{Content: value})
	f := m.measure()
	before := inputCursor(&m.prompt)
	m.Update(tea.MouseWheelMsg{X: f.prompt.X, Y: f.prompt.Y, Button: tea.MouseWheelUp})
	offset := m.promptView.Metrics(m.promptMetrics).Offset
	if offset >= m.promptMetrics.Offset || inputCursor(&m.prompt) != before {
		t.Fatal("wheel did not independently scroll the composer")
	}
	s := m.snapshot
	s.Revision++
	m.Update(snapshotMsg(s))
	if m.promptView.Metrics(m.promptMetrics).Offset != offset {
		t.Fatal("background snapshot stole the reading position")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.prompt.Value() != value+"x" || m.promptView.manual {
		t.Fatal("typing after reading moved the insertion or failed to follow it")
	}
}

func TestPolishedViewCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, variant := range []string{"tabs", "composer", "overflow", "menu", "narrow", "maximized", "footer-compact", "footer-menu", "footer-minimal", "footer-usage"} {
			m := testModel()
			m.setFocus("prompt")
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			m.openSurface("files", "")
			m.openSurface("plan", "")
			if variant == "tabs" {
				m.hover = "tab:" + m.viewState().Host.Tabs[0].ID
			}
			if variant == "maximized" {
				m.state.Layout.Maximized = true
			}
			if variant == "overflow" || variant == "menu" {
				m.openSurface("git", "")
				m.openSurface("agents", "")
				m.openSurface("activity", "")
			}
			m.setFocus("prompt")
			if variant == "composer" || variant == "narrow" {
				m.Update(tea.PasteMsg{Content: strings.Repeat("Review the layout and preserve every draft line.\n", 11) + "Keep the composer visible while I read."})
			}
			if variant == "menu" {
				m.activate(action{Kind: "tabs"})
			}
			if variant == "narrow" {
				m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
			}
			if strings.HasPrefix(variant, "footer-") {
				m.Update(tea.WindowSizeMsg{Width: 76, Height: 28})
				if variant == "footer-menu" {
					m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
					m.activate(action{Kind: "composer-more"})
				}
				if variant == "footer-usage" {
					m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
					m.activate(action{Kind: "usage-summary"})
				}
				if variant == "footer-minimal" {
					m.viewState().Settings.Model = "A very long model name"
					m.snapshot.Threads[0].Effective = m.viewState().Settings
				}
			}
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-light%t-%s.ansi", m.width, m.height, light, variant)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(m.View().Content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
