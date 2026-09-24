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

func p2ApprovalModel(w, h int) *Model {
	m := testModel()
	m.snapshot.Threads[0].Requests = []protocol.Request{{ID: "perm", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Title: "Run make test", Origin: "Fixture agent", Choices: []string{"Allow once", "Deny"}}}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func p2LongQuestionModel(w, h int) *Model {
	m, req := questionReviewModel()
	req.Questions[0].Text = strings.Repeat("Which layout should the narrow phone column use for review? ", 4)
	m.snapshot.Threads[0].Requests = []protocol.Request{req}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// Card rows are one-row density: these heights were measured before the
// approval alignment and scrollbar-gap changes and must not move.
func TestPanelP2CardRowsUnchanged(t *testing.T) {
	for _, c := range []struct {
		name string
		m    *Model
		want int
	}{
		{"approval-160", p2ApprovalModel(160, 40), p2Rows["approval-160"]},
		{"approval-48", p2ApprovalModel(48, 22), p2Rows["approval-48"]},
		{"question-48", p2LongQuestionModel(48, 22), p2Rows["question-48"]},
		{"question-160", p2LongQuestionModel(160, 40), p2Rows["question-160"]},
	} {
		if got := c.m.render().request.H; got != c.want {
			t.Errorf("%s card rows = %d, want %d", c.name, got, c.want)
		}
	}
	if got, want := queueReviewModel(160, 40).queueHeight(), p2Rows["queue-160"]; got != want {
		t.Errorf("queue rows = %d, want %d", got, want)
	}
}

var p2Rows = map[string]int{"approval-160": 5, "approval-48": 5, "question-48": 11, "question-160": 10, "queue-160": 4}

// Approval actions end at the card's right content edge, in keyboard order,
// with hit rects matching the painted buttons.
func TestPanelP2ApprovalActionsRightAligned(t *testing.T) {
	for _, w := range []int{160, 60} {
		m := p2ApprovalModel(w, 40)
		f := m.render()
		var rects []hit
		for _, h := range f.hits {
			if strings.HasPrefix(h.Key, "approve:") {
				rects = append(rects, h)
			}
		}
		if len(rects) != 2 || rects[0].Key != "approve:0" || rects[1].Key != "approve:1" || rects[0].Rect.X >= rects[1].Rect.X {
			t.Fatalf("width %d: approval hits %+v", w, rects)
		}
		last := rects[1].Rect
		if right := f.request.X + f.request.W - 2; last.X+last.W != right {
			t.Fatalf("width %d: actions end at %d, want %d", w, last.X+last.W, right)
		}
		row := []rune(ansi.Strip(f.rows[last.Y]))
		if got := strings.TrimSpace(string(row[last.X:min(len(row), last.X+last.W)])); got != "Deny" {
			t.Fatalf("width %d: painted %q under Deny hit", w, got)
		}
	}
}

// With a scrollbar shown, question text keeps one blank cell before it.
func TestPanelP2QuestionScrollbarGap(t *testing.T) {
	m := p2LongQuestionModel(48, 22)
	f := m.render()
	req, _ := m.request()
	l := m.requestLayout(req, f.request)
	if len(l.lines) <= l.body.H {
		t.Fatalf("expected overflow: %d lines, %d rows", len(l.lines), l.body.H)
	}
	gap := l.x + l.w - 1
	for y := l.body.Y; y < l.body.Y+l.body.H; y++ {
		row := []rune(ansi.Strip(f.rows[y]))
		if gap < len(row) && row[gap] != ' ' {
			t.Fatalf("row %d touches the scrollbar: %q", y, string(row))
		}
	}
}

func TestPanelP2Captures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, s := range [][2]int{{160, 40}, {48, 22}} {
			for scene, m := range map[string]*Model{"p2-approval": p2ApprovalModel(s[0], s[1]), "p2-question": p2LongQuestionModel(s[0], s[1]), "p2-queue": queueReviewModel(s[0], s[1])} {
				m.state.Light = light
				name := fmt.Sprintf("%dx%d-light%t-%s.ansi", s[0], s[1], light, scene)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
