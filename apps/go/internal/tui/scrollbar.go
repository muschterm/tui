package tui

import "github.com/muschterm/tui/apps/go/internal/shell"

// scrollbar measures rows, not bytes or logical lines. Callers supply the same
// wrapped row count and viewport height used to render their content.
type scrollbar struct {
	TrackRows, ThumbStart, ThumbRows int
	Viewport, Offset, MaxOffset      int
}

type scrollTarget struct {
	Rect shell.Rect
	Bar  scrollbar
}

func newScrollbar(total, viewport, offset, trackRows int) scrollbar {
	s := scrollbar{TrackRows: max(0, trackRows), Viewport: max(0, viewport)}
	s.MaxOffset = max(0, total-s.Viewport)
	s.Offset = min(max(0, offset), s.MaxOffset)
	if !s.Visible() {
		return s
	}
	s.ThumbRows = max(1, s.TrackRows*s.Viewport/total)
	// An overflowing viewport must retain at least one cell of travel whenever
	// the track has room for it, including almost-fitting content.
	if s.TrackRows > 1 {
		s.ThumbRows = min(s.ThumbRows, s.TrackRows-1)
	}
	travel := s.TrackRows - s.ThumbRows
	s.ThumbStart = (s.Offset*travel + s.MaxOffset/2) / s.MaxOffset
	return s
}

func (s scrollbar) Visible() bool {
	return s.TrackRows > 0 && s.Viewport > 0 && s.MaxOffset > 0
}

// PageAt returns an absolute content offset after clicking the track. Clicking
// the thumb leaves the position unchanged so the caller can begin dragging.
func (s scrollbar) PageAt(row int) int {
	if !s.Visible() {
		return s.Offset
	}
	if row < s.ThumbStart {
		return max(0, s.Offset-s.Viewport)
	}
	if row >= s.ThumbStart+s.ThumbRows {
		return min(s.MaxOffset, s.Offset+s.Viewport)
	}
	return s.Offset
}

// DragTo preserves the grabbed cell within the thumb. Both row and grab are
// track-relative cell positions; grab is pointer row minus ThumbStart at press.
// A one-cell track has no travel; keyboard and wheel scrolling remain usable.
func (s scrollbar) DragTo(row, grab int) int {
	travel := s.TrackRows - s.ThumbRows
	if !s.Visible() || travel <= 0 {
		return s.Offset
	}
	grab = min(max(0, grab), s.ThumbRows-1)
	position := min(max(0, row-grab), travel)
	return (position*s.MaxOffset + travel/2) / travel
}

// scrollbar paints one column at the right edge of r. Content must already
// reserve that column. Hits are emitted even in measurement-only frames, with
// the target identity and track-relative row needed by the input router.
func (f *frame) scrollbar(m *Model, r shell.Rect, target string, total, viewport, offset int, bg string) scrollbar {
	s := newScrollbar(total, viewport, offset, r.H)
	if r.W <= 0 || !s.Visible() {
		return s
	}
	if f.scrollbars == nil {
		f.scrollbars = make(map[string]scrollTarget)
	}
	f.scrollbars[target] = scrollTarget{Rect: shell.Rect{X: r.X + r.W - 1, Y: r.Y, W: 1, H: r.H}, Bar: s}
	p := m.colors()
	key := "scrollbar-" + target
	for row := 0; row < s.TrackRows; row++ {
		glyph, fg, part := "│", p.line, "track"
		if row >= s.ThumbStart && row < s.ThumbStart+s.ThumbRows {
			glyph, fg, part = "█", p.muted, "thumb"
			if m.hover == key || m.focus == key {
				fg = p.blue
			}
		}
		x, y := r.X+r.W-1, r.Y+row
		f.text(x, y, 1, glyph, fg, bg)
		f.hits = append(f.hits, hit{
			Rect:   shell.Rect{X: x, Y: y, W: 1, H: 1},
			Action: action{Kind: "scrollbar", ID: target, Value: part, Index: row},
			Label:  "Scroll " + target,
			Key:    key,
		})
	}
	return s
}
