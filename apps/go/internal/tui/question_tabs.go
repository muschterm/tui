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

// questionTabPlan measures the tab row once for painting, hit testing and the
// header's overflow counter.
type questionTabPlan struct {
	slots          []questionTabSlot
	labels         []string
	answered       []bool
	x, end         int
	overflow       bool
	active, marker int
}

// Each tab is " <label> <mark> " with its end caps: the marker cell is always
// reserved, so answering never moves the label.
func (m *Model) questionTabPlan(req protocol.Request, r shell.Rect) questionTabPlan {
	plan := questionTabPlan{active: m.questionIndex(req), marker: max(1, ansi.StringWidth(answeredMark(m)))}
	// Fixed empty slots prevent tab labels from occupying a hidden arrow's
	// position. One cell separates each arrow from the tab strip.
	plan.x, plan.end = r.X+questionArrowWidth+1, r.X+r.W-questionArrowWidth-1
	plan.labels = make([]string, len(req.Questions))
	plan.answered = make([]bool, len(req.Questions))
	widths := make([]int, len(req.Questions))
	total := max(0, len(req.Questions)-1) // Gaps between button containers.
	for i, q := range req.Questions {
		d := m.questionDraft(req, i)
		answer := draftAnswer(q, d)
		plan.answered[i] = (len(answer.Choices) > 0 || strings.TrimSpace(answer.Text) != "") && validateDraft(q, d) == nil
		plan.labels[i] = strings.Join(strings.Fields(safe(questionTabLabel(q, i))), " ")
		widths[i] = min(22, ansi.StringWidth(plan.labels[i])+3+plan.marker) // End caps, gap, marker.
		total += widths[i]
	}
	plan.overflow = total > plan.end-plan.x
	if plan.overflow {
		plan.end -= 4 // Three-cell overflow button and its leading gap.
	}
	plan.slots = questionTabWindow(widths, plan.active, max(0, plan.end-plan.x))
	return plan
}

func (m *Model) renderQuestionTabs(f *frame, r shell.Rect, req protocol.Request) {
	p := m.colors()
	plan := m.questionTabPlan(req, r)
	f.fill(r, p, p.panel)
	if plan.active > 0 {
		f.compactButton(m, r.X, r.Y, questionArrowWidth, m.icon("previous"), "question-back", action{Kind: "question", Index: -1}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Previous question"
	}
	x := plan.x
	for _, slot := range plan.slots {
		mark, status := strings.Repeat(" ", plan.marker), ""
		if plan.answered[slot.index] {
			mark, status = fit(answeredMark(m), plan.marker), " · answered"
		}
		label := fit(ansi.Truncate(plan.labels[slot.index], max(1, slot.width-3-plan.marker), "…"), max(1, slot.width-3-plan.marker)) + " " + mark
		key := fmt.Sprint("question-page:", slot.index)
		v := m.questionTab(f, x, r.Y, slot.width, label, key, action{Kind: "question-index", Index: slot.index}, slot.index == plan.active)
		if plan.answered[slot.index] && slot.width >= 3+plan.marker {
			// Recolor only the reserved marker cell: the label never moves.
			// A local draft is not a confirmed answer: the mark keeps the tab's
			// own ink rather than success green.
			f.styledText(x+slot.width-1-plan.marker, r.Y, plan.marker, mark, componentVisual{foreground: v.foreground, background: v.background, bold: v.bold}, false)
		}
		f.hits[len(f.hits)-1].Label = fmt.Sprintf("Question %d of %d · %s%s", slot.index+1, len(plan.labels), plan.labels[slot.index], status)
		x += slot.width + 1
	}
	if plan.overflow {
		f.compactButton(m, plan.end+1, r.Y, 3, m.icon("more-vertical"), "question-tabs", action{Kind: "question-tabs"}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "All questions"
	}
	if plan.active+1 < len(req.Questions) {
		f.compactButton(m, r.X+r.W-questionArrowWidth, r.Y, questionArrowWidth, m.icon("next"), "question-next", action{Kind: "question", Index: 1}, false, normalControl)
		f.hits[len(f.hits)-1].Label = "Next question"
	}
}

// questionTab paints a left-aligned tab label (label, gap, marker cell) so
// its position is independent of the answered marker.
func (m *Model) questionTab(f *frame, x, y, width int, label, key string, a action, selected bool) componentVisual {
	if width < 3 {
		return componentVisual{}
	}
	p := m.colors()
	v := m.componentStyle(squareFill, m.controlState(selected, key), p.text, p.input)
	f.compactControl(m, x, y, width, fit(label, width-2), v)
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: a, Label: label, Key: key})
	return v
}

// answeredMark is the panel vocabulary's completed glyph (✓, ASCII "+"),
// painted in the tab's ink because a draft answer is not yet confirmed.
func answeredMark(m *Model) string {
	return statusGlyph(m, "answered")
}
