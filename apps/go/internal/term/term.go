// Package term owns one PTY-backed shell session and a bounded server-side
// terminal emulator.
//
// Child output is untrusted. It is parsed only by the emulator inside this
// package; the API exposes decoded cells (grapheme text, width, colour and
// attributes) and a sanitized title, never raw child bytes, so renderers cannot
// forward child escape sequences to an outer terminal. Memory is bounded by the
// grid plus a fixed-size scrollback. Sessions live only in memory.
package term

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Limits and defaults.
const (
	MinCols, MaxCols  = 2, 1000
	MinRows, MaxRows  = 1, 500
	DefaultScrollback = 10000
	MaxScrollback     = 100000
	MaxInput          = 64 << 10 // bytes accepted by one Write call
	MaxTitle          = 256      // runes retained from an OSC title
	// MaxClusterBytes bounds one cell's grapheme cluster. It exceeds every
	// RGI emoji sequence and ordinary script cluster, so those are stored
	// exactly; a longer cluster keeps its first whole runes up to the bound
	// and the rest of it is dropped before it reaches the grid or scrollback
	// (see clusterLimiter). Output cells are truncated the same way.
	MaxClusterBytes = 128

	// TermName is the TERM value given to children. The emulator implements
	// the xterm control set it advertises (DA1 VT220-class replies, 256
	// colours, SGR truecolor, alternate screen, bracketed paste), and
	// xterm-256color is present in every supported system terminfo database.
	TermName = "xterm-256color"
)

// Errors reported by the package.
var (
	ErrUnavailable   = errors.New("term: embedded terminals are unavailable on this platform")
	ErrClosed        = errors.New("term: session has ended")
	ErrInputTooLarge = errors.New("term: input exceeds the per-call limit")
	ErrInvalidDir    = errors.New("term: working directory must be an existing absolute directory")
	ErrInvalidShell  = errors.New("term: shell must be an absolute path to an executable file")
	// ErrBusy reports input the child did not accept before the write
	// deadline (it is not reading). The session is still running; see
	// BusyError for how much was written.
	ErrBusy = errors.New("term: child is not reading input")
)

// BusyError is returned when a write times out. Written bytes reached the
// child; the rest were not sent. errors.Is(err, ErrBusy) reports true.
type BusyError struct{ Written, Total int }

func (e *BusyError) Error() string {
	return fmt.Sprintf("%v: %d of %d bytes written", ErrBusy, e.Written, e.Total)
}

// Is makes BusyError match ErrBusy.
func (e *BusyError) Is(target error) bool { return target == ErrBusy }

// Config describes a session to start.
type Config struct {
	// Shell overrides the shell. It must be an absolute executable path.
	// Empty uses $SHELL, falling back to /bin/sh when $SHELL is unset, not
	// absolute or not executable. The shell runs interactive, non-login (-i).
	Shell string
	// Dir is the absolute working directory; it must exist.
	Dir string
	// Env is the base environment; nil uses the server's environment. Outer
	// terminal identity variables are removed and TERM/COLORTERM are set; see
	// ChildEnv.
	Env []string
	// Cols and Rows are clamped to [MinCols,MaxCols] and [MinRows,MaxRows].
	Cols, Rows int
	// Scrollback is the retained main-screen history in lines; <=0 uses
	// DefaultScrollback, values above MaxScrollback are clamped.
	Scrollback int

	// args replaces the default shell arguments (tests only).
	args []string
}

// ColorKind distinguishes default, palette and direct colours.
type ColorKind uint8

// Colour kinds.
const (
	ColorDefault ColorKind = iota
	ColorIndexed
	ColorRGB
)

// Color is a cell colour. Index is meaningful for ColorIndexed (0-15 are the
// basic/bright ANSI colours); R, G, B for ColorRGB.
type Color struct {
	Kind    ColorKind
	Index   uint8
	R, G, B uint8
}

// Attrs is a bitset of cell attributes.
type Attrs uint8

// Cell attributes.
const (
	AttrBold Attrs = 1 << iota
	AttrDim
	AttrItalic
	AttrUnderline
	AttrReverse
	AttrStrike
	AttrBlink
)

// Cell is one terminal cell. Text is one grapheme cluster; a wide character
// occupies its first cell (Width 2) and the following continuation cell has
// Text "" and Width 0.
type Cell struct {
	Text   string
	Width  int
	FG, BG Color
	Attrs  Attrs
}

// Cursor is the cursor position in grid coordinates.
type Cursor struct {
	X, Y    int
	Visible bool
}

// Screen is a copied snapshot of the visible grid.
type Screen struct {
	Seq        uint64
	Cols, Rows int
	Cursor     Cursor
	Title      string
	AltScreen  bool
	Lines      [][]Cell
}

// ExitStatus describes how the child ended.
type ExitStatus struct {
	// Code is the exit code, or -1 when the child was killed by a signal.
	Code int
	// Signal names the terminating signal, if any.
	Signal string
	// Killed reports that Close escalated to SIGKILL.
	Killed bool
	// Descendants counts other processes found in the shell's session
	// (background jobs, including ones in their own process groups) when the
	// session ended. On Close they are hung up and, if still present after
	// the grace period, killed; DescendantsKilled reports that escalation.
	// DescendantsRemaining counts processes still present afterwards. After a
	// natural exit they are only counted, not signalled (like nohup).
	Descendants          int
	DescendantsKilled    bool
	DescendantsRemaining int
	// DescendantsUnknown reports that this platform cannot enumerate the
	// session, so only the shell's own process group was signalled and the
	// counts above are not meaningful.
	DescendantsUnknown bool
}

// SanitizeTitle removes control characters and caps the length.
func SanitizeTitle(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == unicode.ReplacementChar || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '‍' {
			continue
		}
		if n == MaxTitle {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// outerTerminalEnv lists variables describing the server's own terminal or
// multiplexer. A child inheriting them would misidentify its terminal (for
// example enabling kitty or tmux protocols the emulator does not implement).
var outerTerminalEnv = []string{
	"TERM", "COLORTERM", "TERMINFO", "TERM_PROGRAM", "TERM_PROGRAM_VERSION",
	"TERM_SESSION_ID", "LC_TERMINAL", "LC_TERMINAL_VERSION", "COLUMNS", "LINES",
	"TMUX", "TMUX_PANE", "STY", "WINDOW", "WINDOWID", "VTE_VERSION", "WT_SESSION",
	"ITERM_SESSION_ID", "ITERM_PROFILE", "KONSOLE_VERSION", "KONSOLE_DBUS_SESSION",
}

var outerTerminalPrefixes = []string{"KITTY_", "WEZTERM_", "ALACRITTY_", "GHOSTTY_", "ZELLIJ"}

// ChildEnv derives the child environment from base: outer terminal identity
// is removed, TERM is TermName and COLORTERM=truecolor (the emulator stores
// 24-bit SGR colours).
func ChildEnv(base []string) []string {
	out := make([]string, 0, len(base)+2)
next:
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, drop := range outerTerminalEnv {
			if k == drop {
				continue next
			}
		}
		for _, p := range outerTerminalPrefixes {
			if strings.HasPrefix(k, p) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return append(out, "TERM="+TermName, "COLORTERM=truecolor")
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
