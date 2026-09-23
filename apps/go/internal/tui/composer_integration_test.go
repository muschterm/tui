package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
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
	for _, size := range [][2]int{{160, 50}, {120, 40}, {80, 30}, {60, 24}, {48, 22}, {40, 22}} {
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

func TestPromptOutlineContainsTypingAndControls(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}, {40, 22}} {
		m := testModel()
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.setFocus("prompt")
		m.Update(tea.PasteMsg{Content: strings.Repeat("Keep my draft\n", 12)})
		f := m.render()
		send := controlHit(t, f, "send")
		if send.Rect.Y != f.prompt.Y+f.prompt.H {
			t.Fatal("footer is not inside the prompt outline")
		}
		bottom := f.prompt.Y + f.prompt.H + m.composerControlsHeight(f.geom.Center.W-2)
		for _, edge := range []struct {
			row         int
			left, right string
		}{
			{f.prompt.Y - 2, "╭", "╮"}, {bottom, "╰", "╯"},
		} {
			line := ansi.Strip(f.rows[edge.row])
			if !strings.Contains(line, edge.left) || !strings.Contains(line, edge.right) {
				t.Fatal("incomplete prompt outline", line)
			}
		}
		// One blank prompt-tinted row separates the top border from typing.
		outlineX, outlineW := f.geom.Center.X+1, f.geom.Center.W-2
		padding := f.rows[f.prompt.Y-1]
		if got := ansi.Strip(ansi.Cut(padding, outlineX+1, outlineX+outlineW-1)); strings.TrimSpace(got) != "" {
			t.Fatalf("%v: prompt padding row is not blank: %q", size, got)
		}
		for x := outlineX + 1; x < outlineX+outlineW-1; x++ {
			if !cellHasBackground(padding, x, m.colors().input) {
				t.Fatalf("%v: prompt padding cell %d lacks the prompt background: %q", size, x, ansi.Cut(padding, x, x+1))
			}
		}
		if send.Rect.Y >= bottom {
			t.Fatal("footer escaped the prompt outline")
		}
		bar, ok := f.scrollbars["prompt"]
		if !ok || bar.Rect.X >= f.geom.Center.X+f.geom.Center.W-2 {
			t.Fatal("prompt scrollbar overlaps outer border")
		}
	}
}

func TestPromptPlaceholderRendersForThreadsAndDrafts(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.setFocus("prompt")
	m.configureInputs()
	f := m.render()
	if got := ansi.Strip(f.rows[f.prompt.Y]); !strings.Contains(got, "Ask to do anything") || strings.Contains(got, "…") {
		t.Fatalf("existing thread placeholder: %q", got)
	}
	m.beginThreadDraft(m.snapshot.Projects[0].ID)
	m.configureInputs()
	if !m.creatingThread() {
		t.Fatal("expected a new-thread draft")
	}
	f = m.render()
	if got := ansi.Strip(f.rows[f.prompt.Y]); !strings.Contains(got, "Ask to do anything") {
		t.Fatalf("new-thread placeholder: %q", got)
	}
}

// cellHasBackground inspects only the SGR sequences that precede the cell's
// character; ansi.Cut may also carry the next cell's opening sequence.
func cellHasBackground(row string, x int, hex string) bool {
	rgb := parseHex(hex)
	if rgb == nil {
		return false
	}
	cell := ansi.Cut(row, x, x+1)
	head := cell
	for i := 0; i < len(cell); {
		if cell[i] != 0x1b {
			head = cell[:i]
			break
		}
		end := strings.IndexByte(cell[i:], 'm')
		if end < 0 {
			break
		}
		i += end + 1
	}
	last := strings.LastIndex(head, "48;")
	return last >= 0 && strings.HasPrefix(head[last:], fmt.Sprintf("48;2;%d;%d;%d", rgb[0], rgb[1], rgb[2]))
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
		for _, variant := range []string{"tabs", "composer", "overflow", "menu", "narrow", "maximized", "footer-compact", "footer-menu", "footer-minimal", "footer-usage", "conversation-wide", "conversation-compact"} {
			m := testModel()
			m.setFocus("prompt")
			m.state.Light = light
			size := [2]int{160, 50}
			if variant == "conversation-compact" {
				size = [2]int{60, 32}
			}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			conversationCapture := strings.HasPrefix(variant, "conversation-")
			if !conversationCapture {
				m.openSurface("files", "")
				m.openSurface("plan", "")
			}
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
			if conversationCapture {
				thread := &m.snapshot.Threads[0]
				thread.Usage = &protocol.Usage{
					Used: 4250, Size: 10000, Source: "session/update", ReportedAt: "2026-09-22T14:10:00Z",
					Model: "fixture-model", Cost: &protocol.UsageCost{
						Amount: "0.03", Currency: "USD", Source: "fixture", ReportedAt: "2026-09-22T14:10:00Z", Scope: "turn", Estimated: true,
					},
				}
				request := protocol.Request{
					ID: "answered-review-focus", Kind: "question", State: "resolved", Delivery: "fixture-confirmed",
					SubmissionID: "fixture-answer-1", Origin: "Demo Agent",
					Questions:       []protocol.Question{{ID: "review-focus", Label: "Focus", Text: "Which part should the review emphasize?", Kind: "single", Options: []string{"Keyboard navigation", "Compact layout", "Surface workflow"}}},
					QuestionAnswers: []protocol.Answer{{Choices: []string{"Compact layout"}}},
				}
				thread.Requests = append(thread.Requests, request)
				thread.Activity = append(thread.Activity, protocol.Activity{
					ID: "question-answer:answered-review-focus", Role: "question-answer", RequestID: request.ID, TurnID: "intro",
				})
				m.configureInputs()
				m.viewState().Scroll = m.measure().transcriptMax
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
