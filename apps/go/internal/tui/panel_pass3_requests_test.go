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
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// The panel vocabulary pass must not change request or queue card geometry.
func TestPanelPass3RequestCardsKeepRowsAndMarkerCells(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m, req := questionReviewModel()
		m.plainIcons = plain
		before := m.render()
		tab := controlHit(t, before, "question-page:1")
		m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
		after := m.render()
		if after.request.H != before.request.H || controlHit(t, after, "question-page:1") != tab {
			t.Fatal("answering changed card rows or tab geometry")
		}
		first := controlHit(t, after, "question-page:0")
		want := "✓"
		if plain {
			want = "+"
		}
		row := ansi.Strip(cutCells(after.rows[first.Rect.Y], first.Rect.X, first.Rect.X+first.Rect.W))
		if !strings.Contains(row, want) || !strings.Contains(row, req.Questions[0].Label) {
			t.Fatalf("answered tab %q lacks %q", row, want)
		}
		if strings.Contains(ansi.Strip(strings.Join(after.rows, "\n")), strings.ToUpper(req.Questions[0].Text)) {
			t.Fatal("question text was uppercased")
		}
	}
	m := queueReviewModel(160, 50)
	if m.queueHeight() != 4 {
		t.Fatalf("queue height %d", m.queueHeight())
	}
	f := m.render()
	h := controlHit(t, f, "queue")
	if got := ansi.Strip(cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W)); !strings.Contains(got, "QUEUED 3") {
		t.Fatalf("queue legend %q", got)
	}
}

func TestPanelPass3RequestNoticeMarkFallbacks(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI, colorprofile.ASCII} {
			m := testModel()
			m.plainIcons, m.colorProfile = plain, profile
			if m.requestNoticeMark(true) != "!" {
				t.Fatal("problem notice is not the attention mark")
			}
			if got := m.requestNoticeMark(false); got == "✓" || got == "+" {
				t.Fatal("progress notice read as success")
			}
			if plain && answeredMark(m) != "+" || !plain && answeredMark(m) != "✓" {
				t.Fatal("answered mark fallback")
			}
		}
	}
}

// Writes visual review captures when TUI_GO_CAPTURE_DIR is set.
func TestPanelPass3RequestCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	approval := protocol.Request{ID: "perm", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Title: "Run make test", Origin: "Fixture agent", Choices: []string{"Allow once", "Deny"}}
	for _, light := range []bool{false, true} {
		for _, w := range []int{160, 60} {
			m, _ := questionReviewModel()
			m.state.Light = light
			m.chooseAnswer("Compact")
			m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
			writePass3Capture(t, dir, light, w, 40, "request-question", m)
			a := testModel()
			a.state.Light = light
			a.snapshot.Threads[0].Requests = []protocol.Request{approval}
			a.Update(tea.WindowSizeMsg{Width: w, Height: 40})
			writePass3Capture(t, dir, light, w, 40, "request-approval", a)
			q := queueReviewModel(w, 40)
			q.state.Light = light
			writePass3Capture(t, dir, light, w, 40, "request-queue", q)
		}
	}
}

func writePass3Capture(t *testing.T, dir string, light bool, w, h int, scene string, m *Model) {
	t.Helper()
	name := fmt.Sprintf("%dx%d-light%t-%s.ansi", w, h, light, scene)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
}

// A local draft answer is not confirmed: its tab mark keeps the tab's own ink,
// never success green.
func TestQuestionDraftMarkIsNotSuccessInk(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m, req := questionReviewModel()
		m.plainIcons = plain
		m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
		m.viewState().QuestionIndex = 1
		f := m.render()
		h := controlHit(t, f, "question-page:0")
		plan := m.questionTabPlan(req, shell.Rect{})
		x := h.Rect.X + h.Rect.W - 1 - plan.marker
		if got := ansi.Strip(cutCells(f.rows[h.Rect.Y], x, x+1)); got != answeredMark(m) {
			t.Fatalf("plain=%t: marker cell %q", plain, got)
		}
		if foregroundAt(f, x, h.Rect.Y, m.colors().green) {
			t.Fatalf("plain=%t: draft mark painted success green", plain)
		}
		if !foregroundAt(f, x-2, h.Rect.Y, m.colors().text) || !foregroundAt(f, x, h.Rect.Y, m.colors().text) {
			t.Fatalf("plain=%t: draft mark does not share the tab ink", plain)
		}
	}
}

// An inline notice is chosen only when it fits with its leading mark.
func TestRequestNoticeInlineMeasuresMark(t *testing.T) {
	m, req := questionReviewModel()
	m.connected = false
	message, problem := m.requestCardNotice(req)
	painted := ansi.StringWidth(m.requestNoticeMark(problem) + " " + message)
	sawInline, sawBlock := false, false
	for w := 20; w <= 160; w++ {
		l := m.requestLayout(req, shell.Rect{W: w})
		right := questionActionsWidth(m.questionActionsPlan(req, l.w-questionOptionsWidth-1))
		room := l.w - right - questionOptionsWidth - 2
		if l.noticeInline && painted > room {
			t.Fatalf("width %d: inline notice %d cells in %d", w, painted, room)
		}
		if !l.noticeInline && painted <= room {
			t.Fatalf("width %d: fitting notice moved to its own row", w)
		}
		sawInline = sawInline || l.noticeInline
		sawBlock = sawBlock || !l.noticeInline
	}
	if !sawInline || !sawBlock {
		t.Fatalf("boundary not exercised: inline=%t block=%t", sawInline, sawBlock)
	}
}
