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
