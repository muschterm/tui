package term

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func convertColor(c color.Color) Color {
	switch v := c.(type) {
	case nil:
		return Color{}
	case ansi.BasicColor:
		return Color{Kind: ColorIndexed, Index: uint8(v)}
	case ansi.IndexedColor:
		return Color{Kind: ColorIndexed, Index: uint8(v)}
	default:
		r, g, b, _ := c.RGBA()
		return Color{Kind: ColorRGB, R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8)}
	}
}

func convertCell(c *uv.Cell) Cell {
	if c == nil {
		return Cell{Text: " ", Width: 1}
	}
	out := Cell{Text: c.Content, Width: c.Width, FG: convertColor(c.Style.Fg), BG: convertColor(c.Style.Bg)}
	if out.Width <= 0 || out.Text == "" {
		out.Text, out.Width = " ", 1
	}
	a := c.Style.Attrs
	if a&uv.AttrBold != 0 {
		out.Attrs |= AttrBold
	}
	if a&uv.AttrFaint != 0 {
		out.Attrs |= AttrDim
	}
	if a&uv.AttrItalic != 0 {
		out.Attrs |= AttrItalic
	}
	if a&uv.AttrReverse != 0 {
		out.Attrs |= AttrReverse
	}
	if a&uv.AttrStrikethrough != 0 {
		out.Attrs |= AttrStrike
	}
	if a&(uv.AttrBlink|uv.AttrRapidBlink) != 0 {
		out.Attrs |= AttrBlink
	}
	if c.Style.Underline != uv.UnderlineNone {
		out.Attrs |= AttrUnderline
	}
	return out
}

// convertLine copies n cells produced by at into a new row. Cells covered by
// a preceding wide character become continuation cells (Text "", Width 0);
// orphaned zero-width cells become blanks, so every row tiles its width.
func convertLine(n int, at func(x int) *uv.Cell) []Cell {
	row := make([]Cell, n)
	for x := 0; x < n; x++ {
		c := convertCell(at(x))
		row[x] = c
		for k := 1; k < c.Width && x+1 < n; k++ {
			x++
			row[x] = Cell{FG: c.FG, BG: c.BG, Attrs: c.Attrs}
		}
	}
	return row
}
