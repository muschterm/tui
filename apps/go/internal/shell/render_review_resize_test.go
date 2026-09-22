package shell

import "testing"

func TestResizeWithinKeepsEveryPaneEffective(t *testing.T) {
	s := NewState()
	s.Right, s.Bottom = true, true
	const w, h, footer = 120, 40, 9
	for _, d := range []Divider{LeftDivider, RightDivider, BottomDivider} {
		s.ResizeWithin(d, 500, w, h, footer)
		g := s.Compute(w, h, footer)
		if g.Left.W != s.LeftWidth || g.Right.W != s.RightWidth || g.Bottom.H != s.BottomHeight || g.Center.W < 36 || g.Center.H < footer+6 {
			t.Fatalf("divider %d overshoot: stored %#v effective %#v", d, s, g)
		}
		before := s
		s.ResizeWithin(d, -1, w, h, footer)
		if s == before {
			t.Fatalf("divider %d ignored the reverse step", d)
		}
		s.ResizeWithin(d, -500, w, h, footer)
	}
	if s.LeftWidth != 16 || s.RightWidth != 24 || s.BottomHeight != 4 {
		t.Fatalf("lower bounds: %#v", s)
	}
	// Collapsed or maximized panes have no divider; preferences stay put.
	s = NewState()
	s.Right, s.Bottom, s.RightWidth, s.BottomHeight = true, true, 50, 12
	before := s
	for _, d := range []Divider{RightDivider, BottomDivider} {
		s.ResizeWithin(d, 3, 70, 16, footer)
	}
	s.Maximized = true
	before.Maximized = true
	s.ResizeWithin(LeftDivider, 3, w, h, footer)
	if s != before {
		t.Fatalf("hidden pane resized: %#v -> %#v", before, s)
	}
}

func TestRevealIsForcedOnlyWhileTheHostCannotFit(t *testing.T) {
	s := NewState()
	var host Host
	s.Open(&host, "plan", "Plan")
	s.RevealRight()
	narrow := s.Compute(90, 30, 8)
	if !narrow.Maximized || !narrow.Forced || narrow.Right.W != 90 || narrow.Center.H != 8 || s.Maximized {
		t.Fatalf("reveal: %#v %#v", narrow, s)
	}
	wide := s.Compute(200, 50, 8)
	if wide.Maximized || wide.Forced || wide.Right.W != s.RightWidth || wide.Left.W == 0 {
		t.Fatalf("widening did not restore: %#v", wide)
	}
	s.Maximized = true
	if g := s.Compute(90, 30, 8); !g.Maximized || g.Forced {
		t.Fatalf("a preference is not forced: %#v", g)
	}
	s.Maximized = false
	s.ToggleRight(&host)
	s.ToggleRight(&host)
	if g := s.Compute(90, 30, 8); g.Right.W != 0 || g.Maximized {
		t.Fatalf("hiding did not end the reveal: %#v", g)
	}
}
