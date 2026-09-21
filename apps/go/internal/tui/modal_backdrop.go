package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// Painting and outside-click dismissal share the complete outlined rectangle,
// including its padding and border, rather than just the interactive items.
func (m *Model) menuRect() shell.Rect {
	w := min(68, max(20, m.width-6))
	extra := 0
	if m.projectMode != "" {
		extra = 2
	}
	h := min(len(m.menu)+4+extra, max(5+extra, m.height-4))
	return shell.Rect{X: max(0, (m.width-w)/2), Y: max(1, (m.height-h)/2), W: w, H: h}
}

func (m *Model) dismissMenuOutside(x, y int) (bool, tea.Cmd) {
	if len(m.menu) == 0 || m.menuRect().Contains(x, y) {
		return false, nil
	}
	// Use the close button's cancellation path. The caller consumes this click:
	// it must never reach the newly exposed Send, Delete, or selection targets.
	return true, m.activate(action{Kind: "menu-close"})
}

// A terminal cell grid cannot blur neighboring pixels. Instead, blend existing
// colors toward the canvas, preserving the text, role hues and style attributes.
// Paint the modal afterwards so its normal selection/focus contrast is retained.
func (m *Model) renderModalBackdrop(f *frame) {
	if f.rows == nil || m.colorProfile < colorprofile.ANSI {
		return
	}
	canvas := lipColor(m.colors().canvas)
	// A frame uses a small palette repeatedly; transform each SGR only once.
	cache := make(map[string]string)
	for i, row := range f.rows {
		var out strings.Builder
		out.Grow(len(row))
		state := byte(0)
		for len(row) > 0 {
			seq, _, n, next := ansi.DecodeSequence(row, state, nil)
			if n == 0 {
				out.WriteString(row)
				break
			}
			row, state = row[n:], next
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				if dimmed, ok := cache[seq]; ok {
					seq = dimmed
				} else {
					dimmed := backdropSGR(seq, canvas, m.state.Light)
					cache[seq] = dimmed
					seq = dimmed
				}
			}
			out.WriteString(seq)
		}
		f.rows[i] = out.String()
	}
}

func backdropBlend(c, canvas color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	cr, cg, cb, _ := canvas.RGBA()
	return color.RGBA{R: uint8((r*55 + cr*45) / 100 >> 8), G: uint8((g*55 + cg*45) / 100 >> 8), B: uint8((b*55 + cb*45) / 100 >> 8), A: 255}
}

func backdropSGR(seq string, canvas color.Color, light bool) string {
	parts := strings.Split(seq[2:len(seq)-1], ";")
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		if (n == 38 || n == 48) && i+1 < len(parts) {
			switch parts[i+1] {
			case "2":
				if i+4 < len(parts) {
					r, er := strconv.Atoi(parts[i+2])
					g, eg := strconv.Atoi(parts[i+3])
					b, eb := strconv.Atoi(parts[i+4])
					if er == nil && eg == nil && eb == nil && r >= 0 && r <= 255 && g >= 0 && g <= 255 && b >= 0 && b <= 255 {
						c := backdropBlend(color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, canvas)
						parts[i+2], parts[i+3], parts[i+4] = strconv.Itoa(int(c.R)), strconv.Itoa(int(c.G)), strconv.Itoa(int(c.B))
					}
					i += 4
				}
			case "5":
				if i+2 < len(parts) {
					index, err := strconv.Atoi(parts[i+2])
					if err == nil && index >= 0 && index <= 255 {
						c := backdropBlend(ansi.IndexedColor(index), canvas)
						parts[i+2] = strconv.Itoa(int(ansi.Convert256(c)))
					}
					i += 2
				}
			}
			continue
		}
		// Customizable 16-color palettes cannot be blended reliably. Keep their
		// neutral surfaces; use ordinary accents and a quiet neutral text slot.
		if n >= 91 && n <= 97 {
			parts[i] = strconv.Itoa(n - 60)
		} else if (!light && n == 37) || (light && n == 30) {
			parts[i] = "90"
		}
	}
	return fmt.Sprintf("\x1b[%sm", strings.Join(parts, ";"))
}
