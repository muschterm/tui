package tui

import "strings"

// newlineHint is the newline key the hints advertise. Without negotiated
// key disambiguation (kitty keyboard, as Bubble Tea reports it) or an
// observed Shift+Enter, Shift+Enter may arrive as plain Enter and send (for
// example tmux with its default extended-keys off), so only Ctrl+J, which
// always inserts a newline, is promised.
func (m *Model) newlineHint() string {
	if m.shiftEnter {
		return "Shift+Enter"
	}
	return "Ctrl+J"
}

// defaultStatus is the idle status line. The Ctrl+J form is padded at the
// end to the Shift+Enter form's width, so a later negotiation never shifts
// cells.
func (m *Model) defaultStatus() string {
	s := "Enter Send  ·  " + m.newlineHint() + " Newline  ·  F4 Commands  ·  Ctrl+Q Detach"
	return s + strings.Repeat(" ", len("Shift+Enter")-len(m.newlineHint()))
}
