package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func sizedModel(width, height int) *Model {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func within(inner, outer shell.Rect) bool {
	return inner.X >= outer.X && inner.Y >= outer.Y && inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
}

func overlaps(a, b shell.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// Every target belongs to one painted region, and two different activations
// never partially share cells. Scroll/selection bodies carry no activation of
// their own, so controls may sit on top of them.
func assertHitInvariants(t *testing.T, m *Model, context string) {
	t.Helper()
	f := m.measure()
	g := f.geom
	regions := []shell.Rect{g.Top, g.Left, g.Right, g.Center, g.Bottom}
	var leaves []hit
	for _, h := range f.hits {
		if h.Rect.W <= 0 || h.Rect.H <= 0 {
			t.Fatalf("%s: empty hit %#v", context, h)
		}
		owned := false
		for _, r := range regions {
			owned = owned || within(h.Rect, r)
		}
		if !owned {
			t.Fatalf("%s: hit outside every painted region: %#v\n%#v", context, h, g)
		}
		if h.Action.Kind != "" {
			leaves = append(leaves, h)
		}
	}
	for i, a := range leaves {
		for _, b := range leaves[i+1:] {
			// A later target may be layered wholly inside an earlier one (a card's
			// action slot); partial overlap means two controls disagree on a cell.
			if a.Action != b.Action && overlaps(a.Rect, b.Rect) && !within(b.Rect, a.Rect) {
				t.Fatalf("%s: %q and %q overlap: %#v %#v", context, a.Key, b.Key, a.Rect, b.Rect)
			}
		}
	}
	for _, r := range []shell.Rect{f.detail, f.transcript, f.bottomBody} {
		owned := r.W == 0 || r.H == 0
		for _, region := range regions {
			owned = owned || within(r, region)
		}
		if !owned {
			t.Fatalf("%s: body outside its region: %#v", context, r)
		}
	}
}

func TestReviewHitsStayInsideTheirRegionAcrossSizes(t *testing.T) {
	for width := 60; width <= 200; width += 11 {
		for height := 22; height <= 50; height += 4 {
			for _, mode := range []string{"plain", "surface", "maximized", "bottom"} {
				m := sizedModel(width, height)
				switch mode {
				case "surface":
					m.activate(action{Kind: "open", Value: "agents"})
					m.activate(action{Kind: "open", Value: "plan"})
				case "maximized":
					m.activate(action{Kind: "open", Value: "agents"})
					m.state.Layout.Maximized = true
				case "bottom":
					m.activate(action{Kind: "bottom"})
					m.activate(action{Kind: "right"})
				}
				m.menu = nil
				m.configureInputs()
				assertHitInvariants(t, m, fmt.Sprintf("%dx%d %s", width, height, mode))
			}
		}
	}
}

func TestReviewShortMaximizedSurfaceKeepsHitsInsideAndComposerVisible(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		content       bool
	}{{80, 22, false}, {120, 26, true}, {100, 30, true}} {
		m := sizedModel(tc.width, tc.height)
		before := len(m.viewState().Host.Tabs)
		m.activate(action{Kind: "open", Value: "agents"})
		m.configureInputs()
		if tc.width == 120 {
			m.state.Layout.Maximized = true
			m.configureInputs()
		}
		context := fmt.Sprintf("%dx%d", tc.width, tc.height)
		f := m.render()
		if !f.geom.Maximized || f.geom.Right.W != tc.width {
			t.Fatalf("%s: surface not full width: %#v", context, f.geom)
		}
		assertHitInvariants(t, m, context)
		for _, h := range f.hits {
			if strings.HasPrefix(h.Key, "tab:") || strings.HasPrefix(h.Key, "close:") || h.Key == "chooser" || h.Key == "tabs" || h.Key == "right-body" {
				if !within(h.Rect, f.geom.Right) {
					t.Fatalf("%s: surface hit outside the host: %#v", context, h)
				}
				if h.Key != "right-body" && h.Rect.W > 1 && strings.TrimSpace(ansi.Strip(cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W))) == "" {
					t.Fatalf("%s: unpainted hit %#v", context, h)
				}
			}
		}
		if tc.content && f.detail.H == 0 {
			t.Fatalf("%s: maximized surface has no content rows: %#v", context, f.geom.Right)
		}
		// Surface close and selection actions must remain confined to the host.
		for y := f.geom.Center.Y; y < m.height-1; y++ {
			for x := 0; x < m.width; x++ {
				for _, h := range f.hits {
					if h.Rect.Contains(x, y) && (h.Action.Kind == "close" || h.Action.Kind == "tab" || h.Action.Kind == "chooser") {
						t.Fatalf("%s: surface control %q reachable in the footer at %d,%d", context, h.Key, x, y)
					}
				}
			}
		}
		// The maximized surface has no conversation body; click an inert cell in
		// its center footer so a status/control target cannot close a host tab.
		m.Update(tea.MouseClickMsg{X: m.width - 1, Y: f.geom.Center.Y, Button: tea.MouseLeft})
		m.Update(tea.MouseReleaseMsg{X: m.width - 1, Y: f.geom.Center.Y, Button: tea.MouseLeft})
		if len(m.viewState().Host.Tabs) != before+1 {
			t.Fatalf("%s: conversation status changed the opened surface set", context)
		}
		for _, key := range []string{"prompt", "send"} {
			h := controlHit(t, f, key)
			if !within(h.Rect, f.geom.Center) || h.Rect.Y >= m.height-1 {
				t.Fatalf("%s: %s not visible: %#v", context, key, h)
			}
		}
		if _, ok := m.request(); ok && f.request.H == 0 {
			t.Fatalf("%s: pending request hidden", context)
		}
		if f.request.H > max(6, (m.height-2)*2/5) {
			t.Fatalf("%s: request card took %d rows", context, f.request.H)
		}
	}
	// The non-maximized budget is unchanged.
	m := sizedModel(80, 24)
	if got := m.measure().request.H; got <= (m.height-2)*2/5 {
		t.Fatalf("ordinary request budget was capped: %d rows", got)
	}
}

func TestQuestionHistoryToggleHitboxStaysInsideItsCard(t *testing.T) {
	m := sizedModel(120, 40)
	options := make([]string, 16)
	for i := range options {
		options[i] = fmt.Sprintf("Option %02d", i+1)
	}
	request := protocol.Request{
		ID: "long-question-history", Kind: "question", State: "closed", Delivery: "acp-uncertain", SubmissionID: "answer-1",
		Questions:       []protocol.Question{{ID: "q", Text: "Choose the applicable options", Kind: "multiple", Options: options}},
		QuestionAnswers: []protocol.Answer{{Choices: []string{options[0], options[8]}}},
	}
	thread := &m.snapshot.Threads[0]
	thread.Requests = append(thread.Requests, request)
	thread.Activity = append(thread.Activity, protocol.Activity{ID: "question-answer:long", Role: "question-answer", RequestID: request.ID})
	m.viewState().Scroll = m.measure().transcriptMax
	f := m.render()
	var initial hit
	for _, h := range f.hits {
		if h.Key == "question-history:"+request.ID {
			initial = h
			break
		}
	}
	if initial.Key == "" || initial.Action.Kind != "question-history-toggle" {
		t.Fatal("compact question history has no explicit toggle target")
	}
	// The card is right-aligned at the prompt outline's extent, one cell beyond
	// the transcript column, and hugs the user box's 80% cap; the toggle is its
	// interior label, after the blank padding cell that holds the focus mark
	// and before the border.
	extent := f.transcript.W + 2
	cardW := 0
	for _, line := range m.transcriptLines(*thread, f.transcript.W) {
		if line.action.Kind == "question-history-toggle" {
			cardW = line.boxW
		}
	}
	if cap := max(min(extent, 24), extent*4/5); cardW < 5 || cardW > cap {
		t.Fatalf("card width %d, want at most the %d-cell cap", cardW, cap)
	}
	cardX := f.transcript.X + f.transcript.W + 1 - cardW
	if initial.Rect.X != cardX+2 || initial.Rect.W != cardW-4 || !within(initial.Rect, f.geom.Center) {
		t.Fatalf("toggle hitbox escaped its card: %#v transcript=%#v", initial.Rect, f.transcript)
	}
	initialRow := ansi.Strip(cutCells(f.rows[initial.Rect.Y], initial.Rect.X, initial.Rect.X+initial.Rect.W))
	if !strings.Contains(initialRow, "Expand") {
		t.Fatalf("expanded Q&A toggle is missing: %q", initialRow)
	}
	m.hover = initial.Key
	f = m.render()
	for _, h := range f.hits {
		if h.Key == initial.Key {
			if h.Rect != initial.Rect {
				t.Fatalf("hover moved the Q&A target: %#v -> %#v", initial.Rect, h.Rect)
			}
			hoveredRow := ansi.Strip(cutCells(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W))
			if hoveredRow != initialRow {
				t.Fatalf("hover changed the Q&A card geometry: %q -> %q", initialRow, hoveredRow)
			}
			return
		}
	}
	t.Fatal("hover removed the Q&A toggle target")
}

func TestReviewBelowMinimumSizeNothingButCommandsIsReachable(t *testing.T) {
	m := sizedModel(120, 40)
	m.activate(action{Kind: "bottom"})
	m.activate(action{Kind: "right"})
	m.menu = nil
	before := m.state.Layout
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 15})
	f := m.measure()
	if f.geom != (shell.Geometry{}) || len(f.scrollbars) != 0 {
		t.Fatalf("geometry active below minimum: %#v", f.geom)
	}
	for _, h := range f.hits {
		if h.Key != "commands" {
			t.Fatalf("unexpected target below minimum: %#v", h)
		}
	}
	for _, x := range []int{before.LeftWidth, 120 - before.RightWidth - 1} {
		m.Update(tea.MouseClickMsg{X: x, Y: 8, Button: tea.MouseLeft})
		m.Update(tea.MouseMotionMsg{X: x + 20, Y: 8, Button: tea.MouseLeft})
		m.Update(tea.MouseReleaseMsg{X: x + 20, Y: 8, Button: tea.MouseLeft})
	}
	// A drag that began before the terminal shrank must not continue either.
	m.drag = shell.LeftDivider
	m.Update(tea.MouseMotionMsg{X: 60, Y: 8, Button: tea.MouseLeft})
	for _, kind := range []string{"resize-left", "resize-right", "resize-bottom"} {
		m.activate(action{Kind: kind, Index: 4})
	}
	if m.state.Layout != before {
		t.Fatalf("preferences changed below minimum: %#v -> %#v", before, m.state.Layout)
	}
}

func TestReviewDividerDragIsClampedAndReversible(t *testing.T) {
	// 41 rows keep the stored bottom height beside the fixture question card,
	// whose header now takes an interior row.
	m := sizedModel(120, 41)
	m.activate(action{Kind: "open", Value: "plan"})
	m.activate(action{Kind: "bottom"})
	m.configureInputs()
	drag := func(x0, y0, x1, y1 int) {
		m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
		m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
		m.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	}
	visible := func(context string) shell.Geometry {
		t.Helper()
		g := m.measure().geom
		if g.Left.W != m.state.Layout.LeftWidth || g.Right.W != m.state.Layout.RightWidth || g.Bottom.H != m.state.Layout.BottomHeight ||
			g.LeftDivider.W == 0 || g.RightDivider.W == 0 || g.BottomDivider.W == 0 || g.Center.W < 36 {
			t.Fatalf("%s: stored %#v, effective %#v", context, m.state.Layout, g)
		}
		return g
	}
	g := visible("start")
	drag(g.LeftDivider.X, 5, 100, 5)
	g = visible("left overshoot")
	drag(g.LeftDivider.X, 5, g.LeftDivider.X-3, 5)
	if after := visible("left reverse"); after.Left.W != g.Left.W-3 {
		t.Fatalf("left divider ignored reverse drag: %d -> %d", g.Left.W, after.Left.W)
	}
	g = visible("before right")
	drag(g.RightDivider.X, 5, 2, 5)
	g = visible("right overshoot")
	drag(g.RightDivider.X, 5, g.RightDivider.X+3, 5)
	if after := visible("right reverse"); after.Right.W != g.Right.W-3 {
		t.Fatalf("right divider ignored reverse drag: %d -> %d", g.Right.W, after.Right.W)
	}
	g = visible("before bottom")
	drag(g.BottomDivider.X+2, g.BottomDivider.Y, g.BottomDivider.X+2, 0)
	g = visible("bottom overshoot")
	drag(g.BottomDivider.X+2, g.BottomDivider.Y, g.BottomDivider.X+2, g.BottomDivider.Y+2)
	if after := visible("bottom reverse"); after.Bottom.H != g.Bottom.H-2 {
		t.Fatalf("bottom divider ignored reverse drag: %d -> %d", g.Bottom.H, after.Bottom.H)
	}
	for _, kind := range []string{"resize-left", "resize-right", "resize-bottom"} {
		for i := 0; i < 80; i++ {
			m.activate(action{Kind: kind, Index: 2})
		}
		visible(kind)
	}
	// Responsive collapse still never rewrites the remembered sizes.
	stored := m.state.Layout
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m.activate(action{Kind: "resize-right", Index: 2})
	m.activate(action{Kind: "resize-bottom", Index: 2})
	if m.state.Layout != stored {
		t.Fatalf("collapsed panes were resized: %#v -> %#v", stored, m.state.Layout)
	}
}

func TestReviewForcedFullWidthNeverBecomesAPreference(t *testing.T) {
	for _, preferred := range []bool{false, true} {
		m := sizedModel(90, 30)
		m.state.Layout.Maximized = false
		m.activate(action{Kind: "open", Value: "plan"})
		m.configureInputs()
		if preferred {
			// A deliberate preference, set while the host fits.
			m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
			m.activate(action{Kind: "maximize"})
			m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
		}
		f := m.measure()
		if m.state.Layout.Maximized != preferred || !f.geom.Maximized || f.geom.Right.W != 90 || f.geom.Forced == preferred {
			t.Fatalf("preferred=%v: narrow presentation %#v, state %#v", preferred, f.geom, m.state.Layout)
		}
		if hasControl(f, "maximize") != preferred {
			t.Fatalf("preferred=%v: maximize control visibility", preferred)
		}
		if !preferred {
			// No reserved gap: the bell packs against the two remaining controls.
			if bell, bottom := controlHit(t, f, "attention"), controlHit(t, f, "bottom"); bell.slot().X+bell.slot().W+2 != bottom.slot().X {
				t.Fatalf("gap left for hidden maximize: %#v %#v", bell.Rect, bottom.Rect)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
			if m.state.Layout.Maximized || !m.measure().geom.Forced || m.notice.text == "" {
				t.Fatalf("F7 changed a forced presentation: %#v notice=%q", m.state.Layout, m.notice.text)
			}
		}
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
		f = m.measure()
		if m.state.Layout.Maximized != preferred || f.geom.Maximized != preferred || f.geom.Forced || !hasControl(f, "maximize") {
			t.Fatalf("preferred=%v: widening gave %#v, state %#v", preferred, f.geom, m.state.Layout)
		}
		if !preferred && (f.geom.Right.W != m.state.Layout.RightWidth || f.transcript.H == 0) {
			t.Fatalf("widening did not restore the side-by-side arrangement: %#v", f.geom)
		}
		m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
		if !preferred {
			// F3 is the way back to the conversation while forced.
			m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
			if f = m.measure(); f.geom.Maximized || f.transcript.H == 0 || m.state.Layout.Maximized {
				t.Fatalf("F3 did not return to the conversation: %#v", f.geom)
			}
		}
	}
	// Narrowing alone still collapses the right host before the left.
	m := sizedModel(200, 50)
	m.activate(action{Kind: "open", Value: "plan"})
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	if g := m.measure().geom; g.Right.W != 0 || g.Maximized || g.Left.W == 0 {
		t.Fatalf("narrowing did not collapse the right host: %#v", g)
	}
}
