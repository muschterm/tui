package tui

import (
	"encoding/json"
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

func TestCompactColumnsPreserveWorkAndWideLayout(t *testing.T) {
	for _, width := range []int{40, 47, 53, 59} {
		for _, light := range []bool{false, true} {
			m, req := questionReviewModel()
			m.state.Light = light
			m.state.Layout.LeftWidth = 16 // Wide geometry must not size the compact footer.
			m.state.Layout.Right, m.state.Layout.Bottom = true, true
			m.viewState().Host.Open("files", "Files")
			m.prompt.SetValue(strings.Repeat("Keep this prompt line.\n", 9))
			m.viewState().Draft = m.prompt.Value()
			m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "keep.txt"}}
			m.chooseAnswer("Compact")
			m.toggleOther()
			m.answer.SetValue("Keep this answer\nand this line")
			m.storeAnswer(m.answer.Value())
			draft, answer, layout := m.prompt.Value(), m.answer.Value(), m.state.Layout
			before, _ := json.Marshal(m.snapshot)
			m.Update(tea.WindowSizeMsg{Width: width, Height: 22})
			for _, region := range []shell.Region{shell.LeftRegion, shell.RightRegion, shell.BottomRegion, shell.CenterRegion} {
				if light {
					m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
				} else {
					clickControl(m, controlHit(t, m.measure(), "columns"))
				}
				for range int(region) {
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				f := m.render()
				for _, key := range []string{"columns", "attention", "prompt", "send", "interrupt", "usage"} {
					controlHit(t, f, key)
				}
				if hasControl(f, "answer-submit") != (region == shell.CenterRegion) || hasControl(f, "maximize") {
					t.Fatal("compact column shows another column's controls", region)
				}
				for _, h := range f.hits {
					if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > width || h.Rect.Y+h.Rect.H > 21 {
						t.Fatalf("%d/%d: control outside viewport: %+v", width, region, h)
					}
				}
				for _, row := range f.rows {
					if ansi.StringWidth(row) != width {
						t.Fatal("row spills past terminal width", width, region)
					}
				}
				if region == shell.LeftRegion && f.navigation.H < 4 {
					t.Fatal("navigation cannot show a complete thread card")
				}
				if m.prompt.Value() != draft || m.answer.Value() != answer || m.state.Layout != layout || m.busy != nil {
					t.Fatal("column switch changed drafts, wide layout or execution")
				}
				if dir := os.Getenv("TUI_GO_CAPTURE_DIR"); dir != "" && width <= 47 {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					name := fmt.Sprintf("%dx22-light%t-column%d.ansi", width, light, region)
					if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(f.rows, "\n")), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
			m.setFocus("prompt")
			m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
			if m.focus != "right-body" {
				t.Fatal("F6 focused an invisible transcript", m.focus)
			}
			m.activate(action{Kind: "resize-right", Index: 2})
			if m.state.Layout != layout {
				t.Fatal("compact resize changed wide preferences")
			}
			data, _ := json.Marshal(m.state)
			restored := New(nil, "compact", m.snapshot, data)
			restored.Update(tea.WindowSizeMsg{Width: width, Height: 22})
			if restored.viewState().CompactColumn != shell.RightRegion || restored.prompt.Value() != draft {
				t.Fatal("saved compact view did not restore")
			}
			m.activate(action{Kind: "attention-item", ID: m.state.Active, Value: req.ID})
			if !hasControl(m.measure(), "answer-submit") || m.questionDraft(req, 1).Text != answer {
				t.Fatal("attention did not restore the pending question and its draft")
			}
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			if m.singleColumn() || m.state.Layout != layout || m.prompt.Value() != draft {
				t.Fatal("wide workspace did not restore")
			}
			after, _ := json.Marshal(m.snapshot)
			if string(before) != string(after) {
				t.Fatal("view selection changed server-owned work")
			}
		}
	}
}

func TestBelowMinimumPreservesDraftWithoutHiddenSubmission(t *testing.T) {
	m := testModel()
	m.prompt.SetValue("Do not send this")
	m.viewState().Draft = m.prompt.Value()
	for _, size := range [][2]int{{39, 22}, {47, 21}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if !strings.Contains(ansi.Strip(m.View().Content), "40 × 22") || m.nextActivityTick() != nil {
			t.Fatal("below-minimum fallback missing or still animating")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if m.busy != nil || m.prompt.Value() != "Do not send this" {
			t.Fatal("hidden editor submitted or changed draft")
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 47, Height: 22})
	controlHit(t, m.measure(), "send")
	if !m.activityAnimating() || !m.activityTickPending || m.prompt.Value() != "Do not send this" {
		t.Fatal("supported size did not restore work and animation")
	}
}

func TestCompactTerminalReceiptsDoNotStealColumnSelection(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 47, Height: 22})
	wide := m.state.Layout
	m.activate(action{Kind: "open", Value: "terminal"})
	if m.busy == nil || m.viewState().CompactColumn != shell.RightRegion {
		t.Fatal("opening a terminal did not reveal its column")
	}
	c := *m.busy
	m.activate(action{Kind: "column", Index: int(shell.LeftRegion)})
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{TargetID: "compact-terminal"}, local: action{Kind: "terminal-open"}})
	if m.viewState().CompactColumn != shell.LeftRegion || m.state.Layout != wide || len(m.viewState().Host.Tabs) != 1 {
		t.Fatal("delayed terminal receipt stole the column or wide layout")
	}
	m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
	m.activate(action{Kind: "close", ID: "compact-terminal"})
	c = *m.busy
	m.Update(commandMsg{command: c, local: action{Kind: "close"}})
	if m.viewState().CompactColumn != shell.CenterRegion || len(m.viewState().Host.Tabs) != 0 {
		t.Fatal("last terminal close did not return to Conversation")
	}
}
