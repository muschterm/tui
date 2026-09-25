package tui

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

type palette struct{ canvas, nav, panel, input, text, muted, blue, violet, cyan, pink, gold, green, red, line, selected string }

// Low-color surfaces use explicit neutral indices. Quantizing the RGB theme
// can otherwise turn large navy backgrounds into saturated terminal blue.
func (m *Model) colors() palette {
	switch m.colorProfile {
	case colorprofile.TrueColor:
		return colors(m.state.Light)
	case colorprofile.ANSI256:
		if m.state.Light {
			return palette{"255", "253", "254", "253", "235", "240", "25", "90", "30", "125", "94", "28", "124", "248", "251"}
		}
		return palette{"234", "233", "235", "236", "253", "246", "111", "183", "116", "211", "222", "150", "210", "240", "238"}
	default:
		// ANSI entries are customizable by the terminal. Reserve its neutral
		// slots for surfaces; semantic accent colors occupy only small areas.
		if m.state.Light {
			return palette{"15", "7", "7", "7", "0", "0", "4", "5", "6", "5", "3", "2", "1", "8", "15"}
		}
		return palette{"0", "0", "0", "0", "7", "7", "12", "13", "14", "13", "11", "10", "9", "8", "8"}
	}
}

func colors(light bool) palette {
	if light {
		return palette{"#f4f5fa", "#e9edf5", "#eef0f7", "#e2e7f3", "#27314c", "#626d86", "#265fba", "#7941b2", "#147783", "#a03772", "#8b6212", "#3c7134", "#b3334d", "#c0c9dc", "#cedbf4"}
	}
	return palette{"#181c2c", "#101421", "#1e2435", "#242d43", "#d9e1f4", "#94a3c1", "#8ab7ff", "#c5a0ff", "#79dae3", "#f29ccd", "#efc86e", "#a6d986", "#ff829b", "#3b4762", "#2b3e60"}
}

func style(fg, bg string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg))
}

// Only our renderer may supply terminal escapes. Agent data, paths, and drafts
// pass through this boundary before becoming styled terminal text.
func safe(s string) string {
	return strings.ReplaceAll(safeKeepTabs(s), "\t", "    ")
}

// safeKeepTabs is safe without replacing tabs, for callers that expand them
// by column.
func safeKeepTabs(s string) string {
	s = ansi.Strip(strings.ToValidUTF8(s, "�"))
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		// Bidi_Control and the Zl/Zp separators can reorder or break a row.
		// ZWJ, ZWNJ and variation selectors stay so emoji sequences survive.
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// Center within the actual cell hit area, including multicolumn plain icons.
// Odd remaining padding puts the extra cell on the right.
func centered(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "")
	left := max(0, (width-ansi.StringWidth(s))/2)
	return fit(strings.Repeat(" ", left)+s, width)
}
