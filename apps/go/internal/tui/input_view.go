package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"
)

// inputPresentation owns reading position independently of the insertion
// cursor. The native textarea remains authoritative for editing and selection.
// Reset on keyboard edits, paste, draft replacement, resize or style changes.
type inputPresentation struct {
	manual bool
	offset int
	rows   []string
}

// Reset returns to native scrolling that follows the insertion cursor.
func (p *inputPresentation) Reset() { *p = inputPresentation{} }

// ScrollTo moves the reading offset independently of the cursor when the
// content overflows the visible rows.
func (p *inputPresentation) ScrollTo(a *textarea.Model, total, offset int) {
	if total <= a.Height() {
		p.Reset()
		return
	}
	if !p.manual {
		p.manual = true
		p.Refresh(a, total)
	}
	p.offset = max(0, min(offset, len(p.rows)-a.Height()))
}

// Refresh updates cached styling after a pointer changes the selection, while
// keeping the independent reading offset. It never mutates the source input.
func (p *inputPresentation) Refresh(a *textarea.Model, total int) {
	if !p.manual {
		return
	}
	p.rows = strings.Split(fullInputView(a, total), "\n")
	p.offset = max(0, min(p.offset, len(p.rows)-a.Height()))
}

// Metrics substitutes the reading offset into the native scroll metrics.
func (p inputPresentation) Metrics(native inputScroll) inputScroll {
	if p.manual {
		native.Offset = p.offset
	}
	return native
}

// RelativeY converts a pointer row in the displayed slice to the native
// textarea's viewport coordinates. PositionAt accepts out-of-viewport rows.
func (p inputPresentation) RelativeY(a *textarea.Model, y int) int {
	if p.manual {
		return y + p.offset - a.ScrollYOffset()
	}
	return y
}

// View renders the native textarea or the cached rows at the reading offset.
func (p inputPresentation) View(a *textarea.Model) string {
	if !p.manual {
		return a.View()
	}
	end := min(len(p.rows), p.offset+a.Height())
	return strings.Join(p.rows[p.offset:end], "\n")
}

func fullInputView(source *textarea.Model, total int) string {
	probe := textarea.New()
	probe.Prompt = ""
	probe.ShowLineNumbers = false
	probe.CharLimit = 0
	probe.MaxHeight = 0
	probe.MaxWidth = 0
	probe.SetStyles(source.Styles())
	probe.SetWidth(source.Width())
	probe.SetHeight(max(1, total))
	probe.SetValue(source.Value())
	probe.SetVirtualCursor(false)
	probe.Focus()
	// The shell inputs have no padding, gutters or prompt. With all rows
	// visible, public cursor coordinates also address public selection APIs.
	point := func(pos textarea.Position) (int, int) {
		inputSetCursor(&probe, inputOffset(&probe, pos))
		c := probe.Cursor()
		// PositionAt in this pinned release sums individual rune widths,
		// unlike Cursor's grapheme width. Mirror that coordinate convention
		// when reconstructing endpoints after emoji/combining sequences.
		line := []rune(strings.Split(probe.Value(), "\n")[pos.Row])
		x := 0
		for _, r := range line[probe.LineInfo().StartColumn:pos.Col] {
			x += ansi.StringWidth(string(r))
		}
		return x, c.Y
	}
	if start, end, selected := source.Selection(); selected {
		sx, sy := point(start)
		ex, ey := point(end)
		probe.BeginSelection(sx, sy)
		probe.ExtendSelection(ex, ey)
		probe.EndSelection()
	}
	inputSetCursor(&probe, inputCursor(source))
	probe.SetVirtualCursor(source.VirtualCursor())
	if !source.Focused() {
		probe.Blur()
	}
	return probe.View()
}
