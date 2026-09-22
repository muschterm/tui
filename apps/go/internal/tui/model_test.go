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
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func testModel() *Model { return New(nil, "test", fixture.Initial(), nil) }

func TestQuestionNavigationNeverSubmitsAndRemoteResolutionKeepsIdentity(t *testing.T) {
	m := testModel()
	m.prompt.SetValue("unsent prompt")
	m.viewState().Draft = m.prompt.Value()
	m.activate(action{Kind: "answer-choice", Value: "Keyboard flow"})
	m.activate(action{Kind: "answer-choice", Value: "Clarity"})
	m.activate(action{Kind: "question", Index: -1})
	request, _ := m.request()
	draft := m.questionDraft(request, 0)
	if len(draft.Choices) != 1 || draft.Choices[0] != "Keyboard flow" || m.busy != nil || m.prompt.Value() != "unsent prompt" {
		t.Fatal("navigation submitted or destroyed drafts")
	}
	s := fixture.Initial()
	first := s.Threads[0].Requests[0]
	next := first
	next.ID = "second-request"
	next.Revision = 2
	s.Threads[0].Requests = append(s.Threads[0].Requests, next)
	s.Revision = m.snapshot.Revision + 1
	m.Update(snapshotMsg(s))
	raw, _ := json.Marshal(s)
	s = protocol.Snapshot{}
	json.Unmarshal(raw, &s)
	s.Threads[0].Requests[0].State = "resolved"
	s.Revision++
	m.Update(snapshotMsg(s))
	if m.answer.Value() != "" {
		t.Fatal("resolved request draft leaked into next request")
	}
	if m.questionDraft(first, 0).Choices[0] != "Keyboard flow" {
		t.Fatal("old draft lost")
	}
}

func TestThreadRestorationAndSingletonInspectors(t *testing.T) {
	m := testModel()
	m.prompt.SetValue("Draft é 👩🏽‍💻")
	m.viewState().Draft = m.prompt.Value()
	m.activate(action{Kind: "open", Value: "plan"})
	m.activate(action{Kind: "open", Value: "plan"})
	scroll := min(9, m.measure().transcriptMax)
	m.viewState().Scroll = scroll
	if len(m.viewState().Host.Tabs) != 1 {
		t.Fatal("duplicate singleton")
	}
	m.activate(action{Kind: "thread", ID: "thread-review"})
	m.prompt.SetValue("other")
	m.activate(action{Kind: "thread", ID: "thread-shell"})
	if m.prompt.Value() != "Draft é 👩🏽‍💻" || m.viewState().Scroll != scroll || len(m.viewState().Host.Tabs) != 1 {
		t.Fatal("thread view lost")
	}
}

func TestQueueEditPersistsOriginalDraftAndRetainsTypingAfterAcceptance(t *testing.T) {
	m := testModel()
	m.prompt.SetValue("original draft")
	m.viewState().Draft = m.prompt.Value()
	q := m.thread().Queue[0]
	m.activate(action{Kind: "edit", ID: q.ID})
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if restored.state.Edit == nil || restored.state.Edit.OldDraft != "original draft" {
		t.Fatal("lost edit identity or original draft")
	}
	c := protocol.Command{ID: "edit-1", ThreadID: m.state.Active, Text: m.prompt.Value(), Settings: &q.Settings}
	m.busy = &c
	m.prompt.SetValue("newer edits")
	m.viewState().Draft = "newer edits"
	m.Update(commandMsg{command: c, local: action{Kind: "save-edit"}})
	if m.prompt.Value() != "newer edits" || m.state.Edit == nil {
		t.Fatal("accepted edit discarded later typing")
	}
	m.activate(action{Kind: "cancel-edit"})
	if m.prompt.Value() != "original draft" {
		t.Fatal("cancel lost original draft")
	}
}

func TestLateReceiptCannotRepeatEffectsOrChangeOtherThreadLayout(t *testing.T) {
	m := testModel()
	c := protocol.Command{ID: "terminal-1", ThreadID: m.state.Active}
	m.busy = &c
	m.activate(action{Kind: "thread", ID: "thread-review"})
	msg := commandMsg{command: c, receipt: protocol.Receipt{TargetID: "term-1"}, local: action{Kind: "terminal-open"}}
	m.Update(msg)
	m.Update(msg)
	if m.state.Layout.Right || len(m.state.Threads[c.ThreadID].Host.Tabs) != 1 {
		t.Fatal("late/duplicate receipt corrupted presentation")
	}
}

func TestMenuPointerUsesSameActionAndBlocksUnderlyingContent(t *testing.T) {
	m := testModel()
	m.openCommands()
	f := m.render()
	for _, h := range f.hits {
		if h.Key == "menu:0" {
			m.mouse(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
			if m.state.Layout.Left {
				t.Fatal("menu pointer action ignored")
			}
			return
		}
	}
	t.Fatal("no first menu item")
}

func TestRenderingDoesNotMutateStateAndRemovesTerminalEscapes(t *testing.T) {
	m := testModel()
	m.snapshot.Threads[0].Activity[0].Text = "hello\x1b[2J\x1b]52;c;YWJj\a world\u202e"
	before, _ := json.Marshal(m.state)
	view := m.View()
	after, _ := json.Marshal(m.state)
	if string(before) != string(after) {
		t.Fatal("render mutated state")
	}
	if strings.Contains(view.Content, "\x1b[2J") || strings.Contains(view.Content, "52;c;") || strings.Contains(view.Content, "\u202e") {
		t.Fatal("untrusted terminal control escaped boundary")
	}
}

func TestCrowdedFooterRemainsReachable(t *testing.T) {
	m := testModel()
	m.width, m.height = 48, 22
	m.viewState().Settings.Effort = "high"
	m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "context"}}
	m.snapshot.Threads[0].Queue = append(m.snapshot.Threads[0].Queue, m.snapshot.Threads[0].Queue[0])
	m.configureInputs()
	f := m.render()
	for _, h := range f.hits {
		if h.Rect.Y+h.Rect.H > m.height-1 {
			t.Errorf("clipped control: %s y%d h%d", h.Key, h.Rect.Y, h.Rect.H)
		}
	}
}

func TestPromptCapturesSettingsAndContextAtSend(t *testing.T) {
	m := testModel()
	m.snapshot.Threads[0].State = "idle"
	m.prompt.SetValue("keep this submission")
	m.activate(action{Kind: "attach-kind", Value: "file"})
	m.snapshot.Threads[0].Tick = 17
	m.viewState().Settings.Effort = "high"
	m.activate(action{Kind: "send"})
	captured := *m.busy
	m.viewState().Settings.Effort = "low"
	m.snapshot.Threads[0].Tick = 25
	if captured.Settings.Effort != "high" || !strings.Contains(captured.Attachments[0].Content, "tick 17") {
		t.Fatal("capture did not bind Send state")
	}
	m.activate(action{Kind: "attach-kind", Value: "image"})
	m.Update(commandMsg{command: captured, local: action{Kind: "send"}})
	if len(m.viewState().Attachments) != 2 {
		t.Fatal("receipt discarded newly added attachment")
	}
}

func BenchmarkViewWide(b *testing.B) {
	m := testModel()
	m.width, m.height = 160, 50
	m.openSurface("agents", "child-layout")
	m.configureInputs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

func TestLayoutKeepsComposerAndControls(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {120, 40}, {80, 30}, {60, 24}, {48, 22}, {47, 22}, {40, 22}} {
		for _, light := range []bool{false, true} {
			for _, expanded := range []bool{false, true} {
				m := testModel()
				m.width, m.height = size[0], size[1]
				m.state.Light = light
				if expanded {
					m.openSurface("agents", "child-layout")
					m.state.Layout.Maximized = true
				}
				m.configureInputs()
				f := m.render()
				name := fmt.Sprintf("%dx%d-light%t-max%t", size[0], size[1], light, expanded)
				if len(f.rows) != size[1] {
					t.Errorf("%s rows %d", name, len(f.rows))
				}
				for i, line := range f.rows {
					if ansi.StringWidth(line) != size[0] {
						t.Errorf("%s row%d width%d", name, i, ansi.StringWidth(line))
					}
				}
				found := map[string]bool{}
				for _, h := range f.hits {
					found[h.Key] = true
					if h.Rect.Y < 0 || h.Rect.Y+h.Rect.H > size[1]-1 || h.Rect.X < 0 || h.Rect.X+h.Rect.W > size[0] {
						t.Errorf("%s hit outside workspace: %+v", name, h)
					}
				}
				if found["maximize"] != (!m.singleColumn() && f.geom.Right.W > 0 && f.geom.Right.H > 0) {
					t.Errorf("%s maximize visibility does not match right panel", name)
				}
				keys := []string{"attention", "prompt", "send"}
				if m.singleColumn() {
					keys = append(keys, "columns")
				} else {
					keys = append(keys, "left", "right", "bottom")
				}
				if m.conversationVisible() {
					keys = append(keys, "answer-submit")
				}
				for _, k := range keys {
					if !found[k] {
						t.Errorf("%s missing %s", name, k)
					}
				}
				if !found["usage"] {
					t.Errorf("%s missing usage control", name)
				}
				if path := os.Getenv("TUI_GO_CAPTURE_DIR"); path != "" {
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, name+".ansi"), []byte(strings.Join(f.rows, "\n")), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}
