package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func markdownPlain(lines []string) string {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	return strings.Join(plain, "\n")
}

func TestMarkdownLinesRendersCommonInlineMarkdown(t *testing.T) {
	got := markdownLines("# Status\n\nUse **bold**, *italic*, ~~old~~, `x < y` and [guide](https://example.test).\n\nFish &amp; chips; \\*literal\\*.", 80, colors(false))
	plain := markdownPlain(got)
	for _, want := range []string{"Status", "bold", "italic", "old", "x < y", "guide <https://example.test>", "Fish & chips", "*literal*"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Markdown output %q does not contain %q", plain, want)
		}
	}
}

func TestMarkdownLinesRendersBlocksListsQuotesCodeAndTables(t *testing.T) {
	source := "- first item\n- second item\n- [x] checked\n- [ ] open\n\n1. ordered\n2. list\n\n> quoted text\n\n```go\nif a < b {\n  return \"ok\"\n}\n```\n\n| Name | Count |\n| --- | ---: |\n| team | 2 |"
	got := markdownLines(source, 32, colors(false))
	plain := markdownPlain(got)
	for _, want := range []string{"- first item", "- second item", "- [x] checked", "- [ ] open", "1. ordered", "2. list", "> quoted text", "if a < b {", `return "ok"`, "Name | Count", "team | 2"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Markdown output %q does not contain %q", plain, want)
		}
	}
	for i, line := range got {
		if width := ansi.StringWidth(line); width > 32 {
			t.Errorf("line %d has width %d, want at most 32: %q", i, width, line)
		}
	}
}

func TestMarkdownLinesSanitizesSourceAndDecodedEntities(t *testing.T) {
	source := "safe \x1b[2J text and &#27;[2J\n\n```text\n\x1b[31mraw code\x1b[0m\n```"
	got := strings.Join(markdownLines(source, 80, colors(false)), "\n")
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[31m") {
		t.Fatalf("untrusted escape sequence reached rendered Markdown: %q", got)
	}
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "safe  text and [2J") || !strings.Contains(plain, "raw code") {
		t.Fatalf("sanitization changed surrounding text: %q", plain)
	}
}

func TestMarkdownWrappingRepeatsAndClosesSGRPerRow(t *testing.T) {
	lines := wrapMarkdownText("\x1b[1mbold words here\x1b[0m", 5)
	if len(lines) != 3 {
		t.Fatalf("wrapped rows = %#v, want three", lines)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "\x1b[1m") || !strings.HasSuffix(line, "\x1b[0m") {
			t.Errorf("row %d does not reopen and close bold SGR: %q", i, line)
		}
	}
}

func TestMarkdownLinesEmptyAndNarrowWidths(t *testing.T) {
	if got := markdownLines("", 20, colors(false)); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty Markdown = %#v, want one empty row", got)
	}
	for _, width := range []int{1, 2, 4} {
		for i, line := range markdownLines("> quote\n\n- item", width, colors(false)) {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d has display width %d: %q", width, i, got, line)
			}
		}
	}
}
