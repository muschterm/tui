package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// x/ansi Cut preserves escape sequences from discarded text. Composing both
// halves of a row repeatedly can therefore multiply invisible SGR sequences.
// Within an uninterrupted SGR run, a full reset supersedes every earlier style.
// Keep other escapes and all text verbatim; they are not styling instructions.
func compactSGR(text string) string {
	var out, pending strings.Builder
	out.Grow(len(text))
	state := byte(0)
	for len(text) > 0 {
		seq, _, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			out.WriteString(pending.String())
			out.WriteString(text)
			return out.String()
		}
		text, state = text[n:], next
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			if seq == "\x1b[m" || seq == "\x1b[0m" || strings.HasPrefix(seq, "\x1b[0;") {
				pending.Reset()
			}
			pending.WriteString(seq)
			continue
		}
		out.WriteString(pending.String())
		pending.Reset()
		out.WriteString(seq)
	}
	out.WriteString(pending.String())
	return out.String()
}

// cutCells returns exactly the cells [start,end) of a styled row. A cluster cut
// by either edge becomes spaces in its own style, so the piece is never wider
// or narrower than requested. Escapes are kept, as ansi.Cut keeps them, so
// styles and their resets carry into and out of the piece.
func cutCells(text string, start, end int) string {
	var out strings.Builder
	col, state := 0, byte(0)
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		text, state = text[n:], next
		switch {
		case width == 0:
			if strings.HasPrefix(seq, "\x1b") || col >= start && col < end {
				out.WriteString(seq)
			}
		case col >= start && col+width <= end:
			out.WriteString(seq)
		default:
			out.WriteString(strings.Repeat(" ", max(0, min(col+width, end)-max(col, start))))
		}
		col += width
	}
	return out.String()
}
