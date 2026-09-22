// Package shell contains frontend-local layout and surface state, independent of
// application content and side effects. Numeric defaults are prototype choices.
package shell

import "strconv"

// Region identifies a principal pane of the workspace.
type Region uint8

const (
	CenterRegion Region = iota
	LeftRegion
	RightRegion
	BottomRegion
)

// Divider identifies a draggable boundary between panes.
type Divider uint8

const (
	NoDivider Divider = iota
	LeftDivider
	RightDivider
	BottomDivider
)

// Surface is an opened tab in a Host.
type Surface struct {
	ID, Kind, Title string
}

// Host belongs to a view. Closing a tab returns its identity so the application
// can coordinate document preservation and terminal shutdown before removal.
type Host struct {
	Tabs     []Surface
	ActiveID string
	Chooser  bool
	NextID   uint64
}

// Open selects the existing tab of a singleton kind or appends a new tab;
// only terminal surfaces may repeat.
func (h *Host) Open(kind, title string) Surface {
	if kind != "terminal" {
		for _, tab := range h.Tabs {
			if tab.Kind == kind {
				h.Select(tab.ID)
				return tab
			}
		}
	}
	h.NextID++
	tab := Surface{ID: "surface-" + strconv.FormatUint(h.NextID, 10), Kind: kind, Title: title}
	h.Tabs = append(h.Tabs, tab)
	h.Select(tab.ID)
	return tab
}

// Select activates the tab with id and reports whether it exists.
func (h *Host) Select(id string) bool {
	for _, tab := range h.Tabs {
		if tab.ID == id {
			h.ActiveID, h.Chooser = id, false
			return true
		}
	}
	return false
}

// Active returns the selected tab, if any.
func (h Host) Active() (Surface, bool) {
	for _, tab := range h.Tabs {
		if tab.ID == h.ActiveID {
			return tab, true
		}
	}
	return Surface{}, false
}

// Close only removes presentation state. Call after the application accepts the
// close; this does not establish that an associated process has exited.
func (h *Host) Close(id string) (Surface, bool) {
	for i, tab := range h.Tabs {
		if tab.ID != id {
			continue
		}
		h.Tabs = append(h.Tabs[:i], h.Tabs[i+1:]...)
		if h.ActiveID == id {
			h.ActiveID = ""
			if len(h.Tabs) > 0 {
				h.ActiveID = h.Tabs[min(i, len(h.Tabs)-1)].ID
			}
		}
		h.Chooser = false
		return tab, true
	}
	return Surface{}, false
}

// State is the pane arrangement: visibility, sizes, focus and maximize.
type State struct {
	Left, Right, Bottom, Maximized      bool
	LeftWidth, RightWidth, BottomHeight int
	Focus                               Region
	beforeMax                           Region
	// reveal presents an explicitly opened right host full-width while it cannot
	// fit beside the center. It is transient and never a stored preference.
	reveal bool
}

// NewState returns the default arrangement with only navigation open.
func NewState() State {
	return State{Left: true, LeftWidth: 24, RightWidth: 42, BottomHeight: 10}
}

// ToggleLeft shows or hides navigation, moving focus off a hidden pane.
func (s *State) ToggleLeft() {
	s.Left = !s.Left
	if !s.Left && s.Focus == LeftRegion {
		s.Focus = CenterRegion
	}
}

// ToggleBottom shows or hides the bottom panel, moving focus off a hidden pane.
func (s *State) ToggleBottom() {
	s.Bottom = !s.Bottom
	if !s.Bottom && s.Focus == BottomRegion {
		s.Focus = CenterRegion
	}
}

// ToggleRight shows or hides the right host; showing an empty host opens its
// chooser, and hiding clears maximize.
func (s *State) ToggleRight(h *Host) {
	s.Right = !s.Right
	if s.Right {
		h.Chooser = len(h.Tabs) == 0
	} else {
		s.Maximized, s.reveal = false, false
		if s.Focus == RightRegion {
			s.Focus = CenterRegion
		}
	}
}

// Open opens a surface in h, shows the right host and focuses it.
func (s *State) Open(h *Host, kind, title string) Surface {
	tab := h.Open(kind, title)
	s.Right, s.Focus = true, RightRegion
	return tab
}

// Close removes a tab; closing the last tab hides the right host.
func (s *State) Close(h *Host, id string) (Surface, bool) {
	tab, ok := h.Close(id)
	if ok && len(h.Tabs) == 0 {
		s.Right, s.Maximized, s.reveal = false, false, false
		if s.Focus == RightRegion {
			s.Focus = CenterRegion
		}
	}
	return tab, ok
}

// RevealRight asks for the visible right host to be shown even when the
// viewport cannot fit it beside the center. Widening restores the arrangement.
func (s *State) RevealRight() { s.reveal = s.Right }

// ClearReveal drops a forced presentation when the displayed host changes.
func (s *State) ClearReveal() { s.reveal = false }

// ToggleMaximize expands the visible right host over the workspace or
// restores the previous arrangement and focus.
func (s *State) ToggleMaximize(_ *Host) {
	if !s.Right {
		return
	}
	s.reveal = false
	if s.Maximized {
		s.Maximized = false
		s.Focus = s.beforeMax
		if (s.Focus == LeftRegion && !s.Left) || (s.Focus == BottomRegion && !s.Bottom) {
			s.Focus = CenterRegion
		}
		return
	}
	s.beforeMax = s.Focus
	s.Maximized, s.Focus = true, RightRegion
}

// Resize adjusts remembered size in cells. Positive delta grows the pane;
// pointer handlers convert screen displacement to this same convention.
func (s *State) Resize(divider Divider, delta int) {
	switch divider {
	case LeftDivider:
		s.LeftWidth = max(16, s.LeftWidth+delta)
	case RightDivider:
		s.RightWidth = max(24, s.RightWidth+delta)
	case BottomDivider:
		s.BottomHeight = max(4, s.BottomHeight+delta)
	}
}

// ResizeWithin resizes a divider that is effective at this viewport and keeps the
// stored size inside what Compute can present there, so a drag can always be
// reversed. A collapsed or maximized pane has no divider and is left alone.
func (s *State) ResizeWithin(divider Divider, delta, width, height, footerHeight int) {
	g := s.Compute(width, height, footerHeight)
	switch divider {
	case LeftDivider:
		if g.LeftDivider.W > 0 {
			s.LeftWidth = max(16, min(g.Left.W+delta, g.Left.W+g.Center.W-36))
		}
	case RightDivider:
		if g.RightDivider.W > 0 {
			s.RightWidth = max(24, min(g.Right.W+delta, g.Right.W+g.Center.W-36))
		}
	case BottomDivider:
		if g.BottomDivider.W > 0 {
			footer := min(max(0, footerHeight), g.Center.H)
			s.BottomHeight = max(4, min(g.Bottom.H+delta, g.Bottom.H+g.Center.H-footer-6))
		}
	}
}

// Rect is a cell rectangle; zero W or H means absent.
type Rect struct{ X, Y, W, H int }

// Contains reports whether the cell (x, y) lies inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

// Geometry is the resolved pane and divider layout for one terminal size.
type Geometry struct {
	Top, Left, Center, Bottom, Right         Rect
	LeftDivider, RightDivider, BottomDivider Rect
	// Maximized is the effective full-width presentation. Forced means it comes
	// from a revealed host that cannot fit beside the center rather than from
	// the preference: there is nothing to restore and no preference to rewrite.
	Maximized, Forced bool
}

// The vertical dividers continue through the top bar row, so a drag may start
// there too; the top bar's own controls never occupy a divider column.
func (g Geometry) DividerAt(x, y int) Divider {
	if y >= g.Top.Y && y < g.Top.Y+g.Top.H {
		y = max(y, g.LeftDivider.Y, g.RightDivider.Y)
	}
	switch {
	case g.LeftDivider.Contains(x, y):
		return LeftDivider
	case g.RightDivider.Contains(x, y):
		return RightDivider
	case g.BottomDivider.Contains(x, y):
		return BottomDivider
	default:
		return NoDivider
	}
}

// EffectiveFocus resolves responsive collapse without modifying remembered state.
func (g Geometry) EffectiveFocus(focus Region) Region {
	r := g.Center
	switch focus {
	case LeftRegion:
		r = g.Left
	case RightRegion:
		r = g.Right
	case BottomRegion:
		r = g.Bottom
	}
	if r.W == 0 || r.H == 0 {
		return CenterRegion
	}
	return focus
}

// Compute reserves one top row. Center includes the caller's footer. In maximized
// mode Center is only the full-width footer and Right fills the rest of the body.
// Responsive collapse never changes preferences, allowing automatic restoration.
func (s State) Compute(width, height, footerHeight int) Geometry {
	w, h := max(0, width), max(0, height)
	top := min(1, h)
	body := h - top
	footer := min(max(0, footerHeight), body)
	g := Geometry{Top: Rect{0, 0, w, top}}
	if s.Right && (s.Maximized || s.reveal && !s.rightFits(w)) {
		g.Right = Rect{0, top, w, body - footer}
		g.Center = Rect{0, h - footer, w, footer}
		g.Maximized, g.Forced = true, !s.Maximized
		return g
	}
	lw, rw := 0, 0
	if s.Left {
		lw = max(16, s.LeftWidth)
	}
	if s.Right && s.rightFits(w) {
		rw = max(24, s.RightWidth)
	}
	// Retain a 36-column center; collapse right before left.
	if w < lw+boolCell(lw > 0)+36 {
		lw = 0
	}
	x, cw := 0, w
	if lw > 0 {
		g.Left = Rect{0, top, lw, body}
		g.LeftDivider = Rect{lw, top, 1, body}
		x, cw = lw+1, cw-lw-1
	}
	if rw > 0 {
		g.Right = Rect{w - rw, top, rw, body}
		g.RightDivider = Rect{w - rw - 1, top, 1, body}
		cw -= rw + 1
	}
	centerHeight := body
	if s.Bottom && body >= footer+6+4+1 {
		bh := min(max(4, s.BottomHeight), body-footer-6-1)
		centerHeight = body - bh - 1
		g.BottomDivider = Rect{x, top + centerHeight, cw, 1}
		g.Bottom = Rect{x, top + centerHeight + 1, cw, bh}
	}
	g.Center = Rect{x, top, cw, centerHeight}
	return g
}

func (s State) rightFits(width int) bool {
	lw := 0
	if s.Left {
		lw = max(16, s.LeftWidth)
	}
	rw := max(24, s.RightWidth)
	return width >= lw+boolCell(lw > 0)+rw+1+36
}

func boolCell(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ColumnGeometry shows one full-width region above the shared center footer.
// It never changes pane visibility, sizes or maximize preferences.
func ColumnGeometry(width, height, footerHeight int, region Region) Geometry {
	w, h := max(0, width), max(0, height)
	top := min(1, h)
	body := h - top
	footer := min(max(0, footerHeight), body)
	g := Geometry{Top: Rect{0, 0, w, top}, Center: Rect{0, h - footer, w, footer}}
	content := Rect{0, top, w, body - footer}
	switch region {
	case LeftRegion:
		g.Left = content
	case RightRegion:
		g.Right = content
	case BottomRegion:
		g.Bottom = content
	default:
		g.Center = Rect{0, top, w, body}
	}
	return g
}
