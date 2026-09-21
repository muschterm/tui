package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

const questionArrowWidth = 5

type questionTabSlot struct {
	index, width int
}

// Fit a contiguous window around the current question, with neighboring tabs
// where they fit. Widths reserve the answered mark so answering never shifts
// the header. The caller reserves both arrow slots, even at the ends.
func questionTabWindow(widths []int, active, available int) []questionTabSlot {
	if len(widths) == 0 || available < 3 {
		return nil
	}
	active = min(max(0, active), len(widths)-1)
	first, last := active, active+1
	used := min(widths[active], available)
	for {
		grew := false
		if first > 0 && used+1+widths[first-1] <= available {
			first--
			used += 1 + widths[first]
			grew = true
		}
		if last < len(widths) && used+1+widths[last] <= available {
			used += 1 + widths[last]
			last++
			grew = true
		}
		if !grew {
			break
		}
	}
	var slots []questionTabSlot
	for i := first; i < last; i++ {
		slots = append(slots, questionTabSlot{i, min(widths[i], available)})
	}
	return slots
}

func (m *Model) renderQuestionTabs(f *frame, r shell.Rect, req protocol.Request) {
	p := m.colors()
	active := m.viewState().QuestionIndex
	// Fixed empty slots prevent tab labels from occupying a hidden arrow's
	// position. One cell separates each arrow from the tab strip.
	x, end := r.X+questionArrowWidth+1, r.X+r.W-questionArrowWidth-1
	f.fill(r, p, p.panel)
	if active > 0 {
		f.compactButton(m, r.X, r.Y, questionArrowWidth, m.icon("previous"), "question-back", action{Kind: "question", Index: -1}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Previous question"
	}
	labels := make([]string, len(req.Questions))
	widths := make([]int, len(req.Questions))
	answered := make([]bool, len(req.Questions))
	total := max(0, len(req.Questions)-1) // Gaps between button containers.
	for i, q := range req.Questions {
		d := m.questionDraft(req, i)
		answer := draftAnswer(q, d)
		answered[i] = (len(answer.Choices) > 0 || strings.TrimSpace(answer.Text) != "") && validateDraft(q, d) == nil
		labels[i] = strings.Join(strings.Fields(safe(questionTabLabel(q, i, false))), " ")
		widths[i] = min(22, ansi.StringWidth(labels[i])+6) // Border, padding, check.
		total += widths[i]
	}
	overflow := total > end-x
	if overflow {
		end -= 4 // Three-cell overflow button and its leading gap.
	}
	for _, slot := range questionTabWindow(widths, active, max(0, end-x)) {
		mark := ""
		if answered[slot.index] {
			mark = " ✓"
		}
		label := ansi.Truncate(labels[slot.index], max(1, slot.width-4-ansi.StringWidth(mark)), "…") + mark
		key := fmt.Sprint("question-page:", slot.index)
		f.compactButton(m, x, r.Y, slot.width, label, key, action{Kind: "question-index", Index: slot.index}, slot.index == active, normalControl)
		f.hits[len(f.hits)-1].Label = fmt.Sprintf("Question %d of %d · %s%s", slot.index+1, len(labels), labels[slot.index], mark)
		x += slot.width + 1
	}
	if overflow {
		f.compactButton(m, end+1, r.Y, 3, m.icon("more-vertical"), "question-tabs", action{Kind: "question-tabs"}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "All questions"
	}
	if active+1 < len(req.Questions) {
		f.compactButton(m, r.X+r.W-questionArrowWidth, r.Y, questionArrowWidth, m.icon("next"), "question-next", action{Kind: "question", Index: 1}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Next question"
	}
}
