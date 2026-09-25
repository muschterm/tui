package server

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/term"
)

// terminalFrameBudget bounds one encoded screen event. Cells are capped at
// term.MaxClusterBytes, but a large grid of individually styled cells can
// still be big; over budget the frame is degraded (clusters to their first
// rune, then styles dropped) so every client can always read it.
const terminalFrameBudget = 8 << 20

// encodeFrame encodes a screen event within terminalFrameBudget.
func encodeFrame(scr term.Screen) []byte {
	screen := encodeScreen(scr)
	b, _ := json.Marshal(protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screen})
	for level := 1; len(b) > terminalFrameBudget && level <= 2; level++ {
		degradeScreen(screen, level)
		b, _ = json.Marshal(protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screen})
	}
	return b
}

// encodeScrollback builds a scrollback answer within terminalFrameBudget,
// keeping the newest lines when newest is set and the oldest otherwise.
func encodeScrollback(rows [][]term.Cell, from, total int, newest bool) protocol.TerminalScrollback {
	const overhead = 256 // envelope and counters
	lines := encodeLines(rows)
	sizes := make([]int, len(lines))
	sb := protocol.TerminalScrollback{From: from, Total: total}
	for i := range lines {
		b, _ := json.Marshal(lines[i])
		for level := 1; len(b)+overhead > terminalFrameBudget && level <= 2; level++ {
			l := []protocol.TerminalLine{lines[i]}
			degradeLines(l, level)
			lines[i] = l[0]
			sb.Degraded = true
			b, _ = json.Marshal(lines[i])
		}
		sizes[i] = len(b) + 1
	}
	used, lo, hi := overhead, 0, len(lines)
	if newest {
		for lo = len(lines); lo > 0 && used+sizes[lo-1] <= terminalFrameBudget; lo-- {
			used += sizes[lo-1]
		}
	} else {
		for hi = 0; hi < len(lines) && used+sizes[hi] <= terminalFrameBudget; hi++ {
			used += sizes[hi]
		}
	}
	sb.More = lo > 0 || hi < len(lines)
	sb.From += lo
	sb.Lines = lines[lo:hi]
	return sb
}

// degradeScreen simplifies a screen in place: level 1 reduces clusters to
// their first rune, level 2 also drops colours and attributes and merges runs.
func degradeScreen(s *protocol.TerminalScreen, level int) {
	s.Degraded = true
	degradeLines(s.Lines, level)
}

func degradeLines(lines []protocol.TerminalLine, level int) {
	for i, line := range lines {
		out := protocol.TerminalLine{}
		var texts []*strings.Builder
		for _, run := range line {
			if run.Cluster {
				r, _ := utf8.DecodeRuneInString(run.Text)
				run.Text, run.Cluster = string(r), false
			}
			if level >= 2 {
				run.FG, run.BG, run.Attrs = "", "", 0
			}
			if n := len(out); n > 0 && out[n-1].Width == run.Width && out[n-1].FG == run.FG && out[n-1].BG == run.BG && out[n-1].Attrs == run.Attrs {
				texts[n-1].WriteString(run.Text)
				continue
			}
			out = append(out, run)
			b := &strings.Builder{}
			b.WriteString(run.Text)
			texts = append(texts, b)
		}
		for j := range out {
			out[j].Text = texts[j].String()
		}
		lines[i] = out
	}
}

// encodeScreen converts an emulator snapshot to the stream form. Only decoded
// cells cross this boundary; child bytes never reach clients.
func encodeScreen(s term.Screen) *protocol.TerminalScreen {
	out := &protocol.TerminalScreen{
		Seq: s.Seq, Cols: s.Cols, Rows: s.Rows, Title: s.Title, Alt: s.AltScreen,
		Cursor: protocol.TerminalCursor{X: s.Cursor.X, Y: s.Cursor.Y, Visible: s.Cursor.Visible},
		Lines:  make([]protocol.TerminalLine, len(s.Lines)),
	}
	for i, row := range s.Lines {
		out.Lines[i] = encodeLine(row)
	}
	return out
}

func encodeLines(rows [][]term.Cell) []protocol.TerminalLine {
	out := make([]protocol.TerminalLine, len(rows))
	for i, row := range rows {
		out[i] = encodeLine(row)
	}
	return out
}

// encodeLine groups cells into runs of equal style and width. Continuation
// cells are dropped (their wide grapheme covers them); a multi-rune grapheme
// gets its own Cluster run so clients place graphemes without segmenting.
func encodeLine(row []term.Cell) protocol.TerminalLine {
	line := protocol.TerminalLine{}
	var cur protocol.TerminalRun
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			cur.Text = text.String()
			line = append(line, cur)
			text.Reset()
		}
	}
	for _, c := range row {
		if c.Width <= 0 {
			continue
		}
		g := c.Text
		if g == "" {
			g = " "
		}
		run := protocol.TerminalRun{Width: min(c.Width, 2), FG: encodeColor(c.FG), BG: encodeColor(c.BG), Attrs: uint8(c.Attrs)}
		if utf8.RuneCountInString(g) != 1 {
			flush()
			run.Text, run.Cluster = g, true
			line = append(line, run)
			continue
		}
		if text.Len() == 0 || cur.Width != run.Width || cur.FG != run.FG || cur.BG != run.BG || cur.Attrs != run.Attrs {
			flush()
			cur = run
		}
		text.WriteString(g)
	}
	flush()
	return line
}

func encodeColor(c term.Color) protocol.TerminalColor {
	switch c.Kind {
	case term.ColorIndexed:
		return protocol.TerminalIndexed(c.Index)
	case term.ColorRGB:
		return protocol.TerminalRGB(c.R, c.G, c.B)
	}
	return ""
}
