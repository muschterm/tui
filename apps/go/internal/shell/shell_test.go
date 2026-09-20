package shell

import (
	"encoding/json"
	"testing"
)

func TestRestoredHostKeepsUniqueIdentities(t *testing.T) {
	h := Host{}
	first := h.Open("terminal", "Terminal 1")
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var restored Host
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	second := restored.Open("terminal", "Terminal 2")
	if first.ID == second.ID {
		t.Fatal("restoration reused surface identity")
	}
	if len(restored.Tabs) != 2 || restored.ActiveID != second.ID {
		t.Fatal("restored host lost tabs")
	}
}

func TestSurfaceLifecycle(t *testing.T) {
	s, h := NewState(), Host{}
	if s.Right {
		t.Fatal("empty host starts visible")
	}
	s.ToggleRight(&h)
	if !s.Right || !h.Chooser {
		t.Fatal("show empty must open chooser")
	}
	files := s.Open(&h, "files", "Files")
	terminal := s.Open(&h, "terminal", "Terminal 1")
	other := s.Open(&h, "terminal", "Terminal 2")
	if terminal.ID == other.ID {
		t.Fatal("terminal identity reused")
	}
	s.ToggleRight(&h)
	if len(h.Tabs) != 3 {
		t.Fatal("hide removed surfaces")
	}
	again := s.Open(&h, "files", "Files")
	if again.ID != files.ID || len(h.Tabs) != 3 || !s.Right || h.ActiveID != files.ID {
		t.Fatal("singleton not restored")
	}
	s.ToggleMaximize(&h)
	if !s.Maximized {
		t.Fatal("maximize failed")
	}
	s.Close(&h, files.ID)
	if h.ActiveID != terminal.ID {
		t.Fatal("close did not select surviving neighbor")
	}
	s.Close(&h, terminal.ID)
	s.Close(&h, other.ID)
	if s.Right || s.Maximized || h.Chooser || s.Focus != CenterRegion {
		t.Fatal("last close did not hide/reset focus")
	}
	s.ToggleMaximize(&h)
	if s.Maximized {
		t.Fatal("hidden host maximize should be unavailable")
	}
	if _, ok := s.Close(&h, "missing"); ok {
		t.Fatal("unknown close accepted")
	}
}

func TestEmptyHostMaximizeRestoresChooserFocusAndGeometry(t *testing.T) {
	s, h := NewState(), Host{}
	s.ToggleRight(&h)
	s.Bottom, s.Focus = true, BottomRegion
	s.Resize(LeftDivider, 3)
	s.Resize(RightDivider, 5)
	s.Resize(BottomDivider, 2)
	before := s
	wide := s.Compute(160, 50, 8)
	s.ToggleMaximize(&h)
	if !s.Maximized || !s.Right || s.Focus != RightRegion || !h.Chooser || len(h.Tabs) != 0 {
		t.Fatalf("empty host did not maximize with its chooser preserved: state=%+v host=%+v", s, h)
	}
	for _, size := range []struct{ width, height int }{{160, 50}, {40, 15}} {
		g := s.Compute(size.width, size.height, 8)
		if g.Right != (Rect{0, 1, size.width, size.height - 9}) || g.Center != (Rect{0, size.height - 8, size.width, 8}) {
			t.Fatalf("maximized chooser/footer geometry at %+v: %+v", size, g)
		}
		if g.Left != (Rect{}) || g.Bottom != (Rect{}) || g.RightDivider != (Rect{}) {
			t.Fatalf("maximized chooser retained other panes or divider: %+v", g)
		}
	}
	s.ToggleMaximize(&h)
	if s.Maximized || !h.Chooser || s.Focus != before.Focus || s.Compute(160, 50, 8) != wide {
		t.Fatalf("restore lost chooser, focus or geometry: state=%+v host=%+v", s, h)
	}
	if s.LeftWidth != before.LeftWidth || s.RightWidth != before.RightWidth || s.BottomHeight != before.BottomHeight {
		t.Fatal("maximize changed saved geometry preferences")
	}
	s.ToggleMaximize(&h)
	s.ToggleRight(&h)
	if s.Right || s.Maximized || s.Focus != CenterRegion {
		t.Fatal("hiding maximized chooser did not clear maximization and focus")
	}
	s.ToggleRight(&h)
	if !h.Chooser || s.Maximized || s.Compute(160, 50, 8) != wide {
		t.Fatal("showing hidden chooser lost its normal geometry")
	}
}

func TestHiddenHostMaximizeDoesNotRevealSurfaces(t *testing.T) {
	for _, populated := range []bool{false, true} {
		s, h := NewState(), Host{}
		if populated {
			s.Open(&h, "files", "Files")
			s.ToggleRight(&h)
		}
		before := s
		active, chooser := h.ActiveID, h.Chooser
		s.ToggleMaximize(&h)
		if s != before || h.ActiveID != active || h.Chooser != chooser {
			t.Fatalf("maximize changed hidden host (populated=%t): state=%+v host=%+v", populated, s, h)
		}
	}
}

func TestShownHostWithoutSelectionCanMaximize(t *testing.T) {
	s, h := NewState(), Host{}
	s.Open(&h, "files", "Files")
	h.ActiveID, h.Chooser = "", true
	s.ToggleMaximize(&h)
	if !s.Maximized || !h.Chooser || h.ActiveID != "" {
		t.Fatal("maximize required a selection or changed the chooser")
	}
}

func TestResponsiveRestoreAndMaximize(t *testing.T) {
	s := NewState()
	s.Right, s.Bottom = true, true
	wide := s.Compute(160, 50, 8)
	if wide.Left.W == 0 || wide.Right.W == 0 || wide.Bottom.H == 0 {
		t.Fatal("wide layout omitted panes")
	}
	if wide.Bottom.X != wide.Center.X || wide.Bottom.W != wide.Center.W {
		t.Fatal("bottom escapes center column")
	}
	narrow := s.Compute(80, 50, 8)
	if narrow.Right.W != 0 || narrow.Left.W == 0 {
		t.Fatal("must collapse right first")
	}
	tiny := s.Compute(40, 15, 8)
	if tiny.Left.W != 0 || tiny.Right.W != 0 || tiny.Bottom.H != 0 {
		t.Fatal("narrow/short collapse failed")
	}
	if s.Compute(160, 50, 8) != wide {
		t.Fatal("responsive collapse changed preferences")
	}
	s.Maximized = true
	g := s.Compute(160, 50, 8)
	if g.Right != (Rect{0, 1, 160, 41}) || g.Center != (Rect{0, 42, 160, 8}) {
		t.Fatalf("maximized geometry: %+v", g)
	}
	if g.EffectiveFocus(LeftRegion) != CenterRegion {
		t.Fatal("focus retained in hidden pane")
	}
}

func TestGeometryBoundedAndNonOverlapping(t *testing.T) {
	for w := 0; w <= 180; w += 3 {
		for h := 0; h <= 60; h += 2 {
			for mode := 0; mode < 16; mode++ {
				s := NewState()
				s.Left, s.Right, s.Bottom, s.Maximized = mode&1 != 0, mode&2 != 0, mode&4 != 0, mode&8 != 0
				g := s.Compute(w, h, 8)
				rs := []Rect{g.Top, g.Left, g.Center, g.Bottom, g.Right, g.LeftDivider, g.RightDivider, g.BottomDivider}
				for i, r := range rs {
					if r.X < 0 || r.Y < 0 || r.W < 0 || r.H < 0 || r.X+r.W > w || r.Y+r.H > h {
						t.Fatalf("out of bounds %dx%d: %+v", w, h, r)
					}
					for _, other := range rs[:i] {
						if r.W > 0 && r.H > 0 && other.W > 0 && other.H > 0 && r.X < other.X+other.W && other.X < r.X+r.W && r.Y < other.Y+other.H && other.Y < r.Y+r.H {
							t.Fatalf("overlap: %+v %+v", r, other)
						}
					}
				}
			}
		}
	}
}

func TestDividerResizeAndFocus(t *testing.T) {
	s := NewState()
	s.Right, s.Bottom = true, true
	g := s.Compute(160, 50, 8)
	for _, item := range []struct {
		divider Divider
		rect    Rect
	}{{LeftDivider, g.LeftDivider}, {RightDivider, g.RightDivider}, {BottomDivider, g.BottomDivider}} {
		if g.DividerAt(item.rect.X, item.rect.Y) != item.divider {
			t.Fatal("divider not hittable")
		}
	}
	s.Resize(LeftDivider, 3)
	s.Resize(RightDivider, -4)
	s.Resize(BottomDivider, 2)
	if s.LeftWidth != 27 || s.RightWidth != 38 || s.BottomHeight != 12 {
		t.Fatal("resize failed")
	}
	s.Resize(LeftDivider, -100)
	s.Resize(RightDivider, -100)
	s.Resize(BottomDivider, -100)
	if s.LeftWidth != 16 || s.RightWidth != 24 || s.BottomHeight != 4 {
		t.Fatal("minimum violated")
	}
	s.Focus = LeftRegion
	s.ToggleLeft()
	if s.Focus != CenterRegion {
		t.Fatal("focus remained hidden")
	}
	s.Focus = BottomRegion
	h := Host{}
	h.Open("files", "Files")
	s.ToggleMaximize(&h)
	s.ToggleMaximize(&h)
	if s.Focus != BottomRegion {
		t.Fatal("restore lost prior focus")
	}
}
