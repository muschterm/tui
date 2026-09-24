package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// A user message is a right-aligned tinted bubble ending at the prompt
// outline's right extent, one tinted padding row above and below, with its
// text left-aligned one cell inside the bubble's left edge.
func TestUserMessageRendersAsTintedBox(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {60, 32}, {48, 28}} {
		for _, light := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d-light%t", size[0], size[1], light)
			t.Run(name, func(t *testing.T) {
				m := testModel()
				m.state.Light = light
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.configureInputs()
				m.viewState().Scroll, m.viewState().Pinned = 0, false
				f := m.render()
				r, input := f.transcript, m.colors().input
				boxRight := r.X + r.W + 1
				lines := m.transcriptLines(m.thread(), r.W)
				first, last := -1, -1
				for i, line := range lines {
					if line.boxed {
						if first < 0 {
							first = i
						}
						last = i
					}
				}
				if first < 0 || last-first < 2 || lines[first].text != "" || lines[last].text != "" {
					t.Fatalf("user box lacks padding rows: first=%d last=%d", first, last)
				}
				boxW := lines[first].boxW
				x0 := boxRight - boxW
				if x0 <= r.X {
					t.Fatalf("bubble starts at %d, not right of the text column %d", x0, r.X)
				}
				for i := first; i <= last && i < r.H; i++ {
					if lines[i].boxW != boxW {
						t.Fatalf("row %d box width %d, want %d", i, lines[i].boxW, boxW)
					}
					// The jump-to-bottom overlay owns the last row while scrolled up.
					if i == r.H-1 && f.transcriptMax > 0 {
						continue
					}
					row := f.rows[r.Y+i]
					for x := x0; x < boxRight; x++ {
						if !cellHasBackground(row, x, input) {
							t.Fatalf("row %d cell %d is not tinted", i, x)
						}
					}
					for _, x := range []int{x0 - 1, boxRight} {
						if cellHasBackground(row, x, input) {
							t.Fatalf("row %d tint leaked to cell %d", i, x)
						}
					}
					if i == first || i == last {
						if got := ansi.Strip(ansi.Cut(row, x0, boxRight)); strings.TrimSpace(got) != "" {
							t.Fatalf("padding row %d is not blank: %q", i, got)
						}
					}
				}
				text := f.rows[r.Y+first+1]
				if got := ansi.Strip(ansi.Cut(text, x0, x0+6)); got != " Build" {
					t.Fatalf("text is not one cell inside the tint: %q", got)
				}

				// Repainting the same rows must not grow invisible styling.
				before := len(f.rows[r.Y+first+1])
				for range 20 {
					m.renderTranscript(&f, r)
				}
				if got := len(f.rows[r.Y+first+1]); got != before {
					t.Fatalf("repainting grew the styled row from %d to %d bytes", before, got)
				}

				want := "Build a"
				y := r.Y + first + 1
				selectTranscript(m, x0+1, y, x0+len(want), y)
				if m.selectedText != want {
					t.Fatalf("selection copied %q, want %q", m.selectedText, want)
				}
			})
		}
	}
}

// Short user messages hug their content; long ones cap at 80% of the box
// extent and wrap inside it. Agent replies stay at the left without a tint.
func TestUserMessageBubbleWidthAndAgentAlignment(t *testing.T) {
	m := testModel()
	thread := &m.snapshot.Threads[0]
	long := strings.Repeat("x", 400) // unbroken, so wrapping fills the cap exactly
	thread.Activity = []protocol.Activity{
		{ID: "u1", Role: "user", Text: "Hi"},
		{ID: "a1", Role: "agent", Text: "Agent reply"},
		{ID: "u2", Role: "user", Text: long},
		{ID: "a2", Role: "agent", Text: strings.Repeat("y", 400)},
	}
	thread.Requests = nil
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.configureInputs()
	m.viewState().Scroll, m.viewState().Pinned = 0, false
	f := m.render()
	r, input := f.transcript, m.colors().input
	extent := r.W + 2
	maxBox := max(min(extent, 24), extent*4/5)
	lines := m.transcriptLines(*thread, r.W)
	var boxWidths []int
	agentRow := -1
	for i, line := range lines {
		if line.boxed && line.text == "" && (i == 0 || !lines[i-1].boxed) {
			boxWidths = append(boxWidths, line.boxW)
		}
		if strings.Contains(ansi.Strip(line.text), "Agent reply") {
			agentRow = i
		}
		if line.boxed && ansi.StringWidth(line.text) > line.boxW-2 {
			t.Fatalf("row %d text exceeds its box: %q", i, line.text)
		}
		// Agent text stops at the mirrored cap: as wide as the bubble's
		// interior, never the full column.
		if !line.boxed && strings.Contains(ansi.Strip(line.text), "yyyy") {
			width := ansi.StringWidth(ansi.Strip(line.text))
			full := i > 0 && !strings.Contains(ansi.Strip(lines[i-1].text), "yyyy") // first row of the reply
			if width > maxBox-2 || full && width != maxBox-2 {
				t.Fatalf("row %d agent text width %d, want the %d-cell mirrored cap", i, width, maxBox-2)
			}
		}
	}
	if len(boxWidths) != 2 || boxWidths[0] != len("Hi")+2 || boxWidths[1] != maxBox {
		t.Fatalf("box widths %v, want [4 %d]", boxWidths, maxBox)
	}
	wrapped := 0
	for _, line := range lines {
		if line.boxed && line.boxW == maxBox && line.text != "" {
			wrapped++
		}
	}
	if wrapped < 2 {
		t.Fatalf("long message did not wrap inside its box: %d rows", wrapped)
	}
	if agentRow < 0 || agentRow >= r.H {
		t.Fatalf("agent row %d not visible", agentRow)
	}
	row := f.rows[r.Y+agentRow]
	if got := ansi.Strip(ansi.Cut(row, r.X, r.X+11)); got != "Agent reply" {
		t.Fatalf("agent reply not at the left column: %q", got)
	}
	for x := r.X - 1; x <= r.X+r.W; x++ {
		if cellHasBackground(row, x, input) {
			t.Fatalf("agent row tinted at %d", x)
		}
	}
}

func TestUserMessageBoxDegradesAtNarrowWidths(t *testing.T) {
	m := testModel()
	items := []protocol.Activity{{ID: "u", Role: "user", Text: "A narrow message box"}}
	for w := 1; w < 6; w++ {
		lines := m.activityLines(items, w)
		if len(lines) < 5 || !lines[0].boxed || lines[0].text != "" {
			t.Fatalf("width %d: missing top padding row", w)
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line.text); got > w {
				t.Fatalf("width %d: line %d is %d cells", w, i, got)
			}
		}
	}
}

// An overflowing transcript's scrollbar owns the gutter column right of the
// box extent: user boxes keep their tint and answered cards their border in
// the prompt outline's right column, and neither is painted over.
func TestTranscriptScrollbarLeavesBoxEdges(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {60, 32}, {48, 22}} {
		m := testModel()
		thread := &m.snapshot.Threads[0]
		request := protocol.Request{
			ID: "scroll-history", Kind: "question", State: "resolved", Delivery: "fixture-confirmed", SubmissionID: "answer-scroll",
			Questions:       []protocol.Question{{ID: "layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}}},
			QuestionAnswers: []protocol.Answer{{Choices: []string{"Compact"}}},
		}
		thread.Requests = []protocol.Request{request}
		thread.Activity = []protocol.Activity{{ID: "u0", Role: "user", Text: "First question"}, {ID: "question-answer:scroll:1", Role: "question-answer", RequestID: request.ID}}
		for i := range 12 {
			thread.Activity = append(thread.Activity, protocol.Activity{ID: fmt.Sprint("u", i+1), Role: "user", Text: fmt.Sprint("Follow-up message ", i+1)})
		}
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.configureInputs()
		m.viewState().Scroll, m.viewState().Pinned = 0, false
		f := m.render()
		bar, ok := f.scrollbars["transcript"]
		if !ok || !bar.Bar.Visible() {
			t.Fatalf("%v: transcript does not overflow", size)
		}
		c, r := f.geom.Center, f.transcript
		edge := r.X + r.W // The box's and prompt outline's right column.
		if bar.Rect.X != edge+1 || bar.Rect.X != c.X+c.W-1 {
			t.Fatalf("%v: scrollbar at x=%d, want the gutter %d beside box edge %d", size, bar.Rect.X, c.X+c.W-1, edge)
		}
		if prompt := f.prompt; ansi.Strip(cutCells(f.rows[prompt.Y], edge, edge+1)) != "│" {
			t.Fatalf("%v: prompt outline right edge is not at x=%d", size, edge)
		}
		lines := m.transcriptLines(*thread, r.W)
		boxed, bordered := 0, 0
		for i := 0; i < r.H && i < len(lines); i++ {
			row, line := f.rows[r.Y+i], lines[i]
			if cellHasBackground(row, bar.Rect.X, m.colors().input) {
				t.Fatalf("%v: row %d tinted the scrollbar gutter", size, i)
			}
			switch {
			case line.boxed:
				boxed++
				if !cellHasBackground(row, edge, m.colors().input) {
					t.Fatalf("%v: row %d lost the box tint at its right edge", size, i)
				}
			case line.outset:
				bordered++
				if got := ansi.Strip(cutCells(row, edge, edge+1)); !strings.ContainsAny(got, "│╮╯") {
					t.Fatalf("%v: row %d lost the card's right border: %q", size, i, got)
				}
			}
			if got := ansi.Strip(cutCells(row, bar.Rect.X, bar.Rect.X+1)); got != "│" && got != "█" {
				t.Fatalf("%v: row %d scrollbar cell %q", size, i, got)
			}
		}
		if boxed == 0 || bordered == 0 {
			t.Fatalf("%v: viewport showed boxed=%d bordered=%d rows", size, boxed, bordered)
		}
	}
}

// Beside the right pane divider the transcript's gutter scrollbar shows only
// its thumb, so its track and the divider never form a double rule; the
// blank track still pages when clicked.
func TestTranscriptScrollbarBesideDividerIsThumbOnly(t *testing.T) {
	m := testModel()
	thread := &m.snapshot.Threads[0]
	for i := range 12 {
		thread.Activity = append(thread.Activity, protocol.Activity{ID: fmt.Sprint("u", i+1), Role: "user", Text: fmt.Sprint("Follow-up message ", i+1)})
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.openSurface("activity", "")
	m.viewState().Scroll, m.viewState().Pinned = 0, false
	f := m.render()
	bar, ok := f.scrollbars["transcript"]
	d := f.geom.RightDivider
	if !ok || d.W == 0 || bar.Rect.X+1 != d.X || bar.Rect.X != f.transcript.X+f.transcript.W+1 {
		t.Fatalf("scrollbar %+v, divider %+v", bar.Rect, d)
	}
	thumb := 0
	for row := 0; row < bar.Rect.H; row++ {
		got := ansi.Strip(cutCells(f.rows[bar.Rect.Y+row], bar.Rect.X, bar.Rect.X+1))
		inThumb := row >= bar.Bar.ThumbStart && row < bar.Bar.ThumbStart+bar.Bar.ThumbRows
		if inThumb && got != "█" || !inThumb && got != " " {
			t.Fatalf("row %d: %q (thumb=%t)", row, got, inThumb)
		}
		if inThumb {
			thumb++
		}
	}
	if thumb == 0 || thumb == bar.Rect.H {
		t.Fatalf("thumb rows %d of %d", thumb, bar.Rect.H)
	}
	y := bar.Rect.Y + bar.Rect.H - 1
	m.Update(tea.MouseClickMsg{X: bar.Rect.X, Y: y, Button: tea.MouseLeft})
	if m.viewState().Scroll == 0 {
		t.Fatal("clicking the blank track did not page")
	}
}
