package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestCompactSGRPreservesTextPartialStylesAndOtherEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1b[31m\x1b[0m\x1b[34mBlue\x1b[0m", "\x1b[0m\x1b[34mBlue\x1b[0m"},
		{"\x1b[31m\x1b[m\x1b[1mBold", "\x1b[m\x1b[1mBold"},
		{"\x1b[1m\x1b[0;32mGreen", "\x1b[0;32mGreen"},
		{"\x1b[31m\x1b[1mBoth\x1b[22mRed", "\x1b[31m\x1b[1mBoth\x1b[22mRed"},
		{"\x1b[38;2;0;1;2m界é👩🏽‍💻", "\x1b[38;2;0;1;2m界é👩🏽‍💻"},
		{"\x1b[31m\x1b[2K\x1b[0mX", "\x1b[31m\x1b[2K\x1b[0mX"},
		{"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"},
	} {
		if got := compactSGR(tc.in); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestFramePaintingKeepsStyledRowsBounded(t *testing.T) {
	f := frame{rows: []string{strings.Repeat(" ", 160)}}
	p := colors(false)
	for i := 0; i < 200; i++ {
		x := i % 160
		f.put(shell.Rect{X: x, W: 1, H: 1}, style(p.blue, p.input).Render("x"))
		if len(f.rows[0]) > 160*100 {
			t.Fatal("repeated cell painting multiplied invisible styles")
		}
	}
	if ansi.Strip(f.rows[0]) != strings.Repeat("x", 160) {
		t.Fatal("style compaction changed cell content")
	}
}
