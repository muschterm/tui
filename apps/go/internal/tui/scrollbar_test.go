package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestScrollbarGeometry(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		total, view, offset, track    int
		visible                       bool
		wantOffset, start, thumb, max int
	}{
		{"empty", 0, 5, 0, 5, false, 0, 0, 0, 0},
		{"fits", 5, 5, 8, 5, false, 0, 0, 0, 0},
		{"underfilled", 3, 5, -4, 5, false, 0, 0, 0, 0},
		{"hidden", 20, 0, 0, 5, false, 0, 0, 0, 20},
		{"zero track", 20, 5, 0, 0, false, 0, 0, 0, 15},
		{"top", 100, 20, -1, 10, true, 0, 0, 2, 80},
		{"middle", 100, 20, 40, 10, true, 40, 4, 2, 80},
		{"bottom", 100, 20, 900, 10, true, 80, 8, 2, 80},
		{"two rows", 30, 2, 28, 2, true, 28, 1, 1, 28},
		{"one row", 30, 1, 29, 1, true, 29, 0, 1, 29},
		{"almost fits", 11, 10, 1, 10, true, 1, 1, 9, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScrollbar(tc.total, tc.view, tc.offset, tc.track)
			if s.Visible() != tc.visible || s.Offset != tc.wantOffset || s.ThumbStart != tc.start || s.ThumbRows != tc.thumb || s.MaxOffset != tc.max {
				t.Fatalf("unexpected geometry: %+v", s)
			}
		})
	}
}

func TestScrollbarPagingAndDragging(t *testing.T) {
	s := newScrollbar(100, 20, 40, 10)
	if s.PageAt(0) != 20 || s.PageAt(9) != 60 || s.PageAt(4) != 40 || s.PageAt(5) != 40 {
		t.Fatal("track clicks must page and thumb clicks must preserve offset")
	}
	if s.DragTo(5, 1) != 40 || s.DragTo(-30, 1) != 0 || s.DragTo(30, 1) != 80 {
		t.Fatal("drag must preserve grab point and clamp endpoints")
	}
	if newScrollbar(100, 20, 5, 10).PageAt(-1) != 0 || newScrollbar(100, 20, 75, 10).PageAt(10) != 80 {
		t.Fatal("paging must clamp endpoints")
	}
	for track := 1; track <= 20; track++ {
		s = newScrollbar(107, 7, 35, track)
		last := -1
		for row := -1; row <= track+1; row++ {
			offset := s.DragTo(row, 0)
			if offset < last || offset < 0 || offset > s.MaxOffset {
				t.Fatalf("non-monotonic or unbounded drag at track %d row %d: %d", track, row, offset)
			}
			last = offset
		}
		if track > 1 && (s.DragTo(0, 0) != 0 || s.DragTo(track-1, 0) != 100) {
			t.Fatal("drag cannot reach content endpoints")
		}
	}
	s = newScrollbar(30, 1, 17, 1)
	if s.DragTo(20, 0) != 17 {
		t.Fatal("one-cell track cannot move its thumb")
	}
}

func TestScrollbarPaintAndMeasureAgree(t *testing.T) {
	m := testModel()
	r := shell.Rect{X: 1, Y: 1, W: 5, H: 4}
	paint := frame{rows: []string{"........", "........", "........", "........", "........", "........"}}
	measure := frame{}
	for _, f := range []*frame{&paint, &measure} {
		f.scrollbar(m, r, "request", 20, 4, 16, colors(false).canvas)
		if len(f.hits) != 4 || len(f.scrollbars) != 1 {
			t.Fatal("measurement must retain scroll geometry and pointer hits")
		}
		for i, h := range f.hits {
			if h.Rect.X != 5 || h.Rect.Y != i+1 || h.Rect.W != 1 || h.Key != "scrollbar-request" || h.Action.ID != "request" || h.Action.Index != i {
				t.Fatalf("incorrect scrollbar hit: %+v", h)
			}
		}
		if f.hits[3].Action.Value != "thumb" || f.hits[0].Action.Value != "track" {
			t.Fatal("pointer router cannot distinguish thumb from track")
		}
	}
	for i, row := range paint.rows {
		plain := ansi.Strip(row)
		if ansi.StringWidth(plain) != 8 || !strings.HasPrefix(plain, ".....") || !strings.HasSuffix(plain, "..") {
			t.Fatalf("scrollbar overwrote neighboring cells in row %d: %q", i, plain)
		}
	}
	for _, r := range []shell.Rect{{W: 0, H: 4}, {W: 1, H: 0}, {W: 1, H: 4}} {
		f := frame{}
		f.scrollbar(m, r, "fits", 4, 4, 0, colors(false).canvas)
		if len(f.hits) != 0 || len(f.scrollbars) != 0 {
			t.Fatal("invisible scrollbar retained pointer targets")
		}
	}
}
