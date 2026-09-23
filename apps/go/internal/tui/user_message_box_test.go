package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// A user message is a tinted box with the prompt outline's horizontal extent,
// one tinted padding row above and below, and text in the transcript column.
func TestUserMessageRendersAsTintedBox(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {60, 32}, {48, 28}} {
		for _, light := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d-light%t", size[0], size[1], light)
			t.Run(name, func(t *testing.T) {
				m := testModel()
				m.state.Light = light
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m.configureInputs()
				m.viewState().Scroll = 0
				f := m.render()
				r, c, input := f.transcript, f.geom.Center, m.colors().input
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
				if last >= r.H {
					t.Fatalf("user box does not fit the %d-row viewport", r.H)
				}
				for i := first; i <= last; i++ {
					row := f.rows[r.Y+i]
					for x := c.X + 1; x <= c.X+c.W-2; x++ {
						if !cellHasBackground(row, x, input) {
							t.Fatalf("row %d cell %d is not tinted: %q", i, x, ansi.Cut(row, x, x+1))
						}
					}
					for _, x := range []int{c.X, c.X + c.W - 1} {
						if cellHasBackground(row, x, input) {
							t.Fatalf("row %d tint leaked to pane edge cell %d", i, x)
						}
					}
					if i == first || i == last {
						if got := ansi.Strip(ansi.Cut(row, c.X+1, c.X+c.W-1)); strings.Trim(got, " │█") != "" {
							t.Fatalf("padding row %d is not blank: %q", i, got)
						}
					}
				}
				after := f.rows[r.Y+last+1]
				if cellHasBackground(after, r.X, input) {
					t.Fatal("tint continued past the bottom padding row")
				}
				text := f.rows[r.Y+first+1]
				if got := ansi.Strip(ansi.Cut(text, r.X-1, r.X+5)); got != " Build" {
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

				want := "Build a calm"
				y := r.Y + first + 1
				selectTranscript(m, r.X, y, r.X+len(want)-1, y)
				if m.selectedText != want {
					t.Fatalf("selection copied %q, want %q", m.selectedText, want)
				}
			})
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
		m.viewState().Scroll = 0
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
	m.viewState().Scroll = 0
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
