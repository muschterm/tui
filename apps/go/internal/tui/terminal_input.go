package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Terminal input focus. A terminal pane is an ordinary focusable control
// ("terminal:<id>"); Enter or a click in its grid enters input focus, where
// every key goes to the shell except the reserved Ctrl+], which leaves.
// Outer-app shortcuts (F-keys, Ctrl+Q, Ctrl+C, Tab traversal) are suspended
// meanwhile. Mouse events are not forwarded to the child.

// terminalEscapeKey leaves terminal input focus. Ctrl+] is telnet's escape
// character and is unused by the app's own bindings.
const terminalEscapeKey = "ctrl+]"

// lockMods are modifier bits that never change what a key sends.
const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

var terminalKeyNames = map[rune]string{
	tea.KeyEnter: "enter", tea.KeyTab: "tab", tea.KeyBackspace: "backspace", tea.KeyEscape: "escape", tea.KeySpace: "space",
	tea.KeyUp: "up", tea.KeyDown: "down", tea.KeyLeft: "left", tea.KeyRight: "right",
	tea.KeyHome: "home", tea.KeyEnd: "end", tea.KeyPgUp: "pgup", tea.KeyPgDown: "pgdown",
	tea.KeyInsert: "insert", tea.KeyDelete: "delete",
	tea.KeyF1: "f1", tea.KeyF2: "f2", tea.KeyF3: "f3", tea.KeyF4: "f4", tea.KeyF5: "f5", tea.KeyF6: "f6",
	tea.KeyF7: "f7", tea.KeyF8: "f8", tea.KeyF9: "f9", tea.KeyF10: "f10", tea.KeyF11: "f11", tea.KeyF12: "f12",
	tea.KeyKp0: "kp0", tea.KeyKp1: "kp1", tea.KeyKp2: "kp2", tea.KeyKp3: "kp3", tea.KeyKp4: "kp4",
	tea.KeyKp5: "kp5", tea.KeyKp6: "kp6", tea.KeyKp7: "kp7", tea.KeyKp8: "kp8", tea.KeyKp9: "kp9",
	tea.KeyKpEnter: "kp-enter", tea.KeyKpPlus: "kp-plus", tea.KeyKpMinus: "kp-minus", tea.KeyKpMultiply: "kp-multiply",
	tea.KeyKpDivide: "kp-divide", tea.KeyKpDecimal: "kp-decimal", tea.KeyKpEqual: "kp-equal", tea.KeyKpComma: "kp-comma",
	tea.KeyKpUp: "up", tea.KeyKpDown: "down", tea.KeyKpLeft: "left", tea.KeyKpRight: "right",
	tea.KeyKpHome: "home", tea.KeyKpEnd: "end", tea.KeyKpPgUp: "pgup", tea.KeyKpPgDown: "pgdown",
	tea.KeyKpInsert: "insert", tea.KeyKpDelete: "delete",
}

// terminalKeyFor maps a key press to the semantic key the server encodes.
// Keys the protocol cannot express (Super/Hyper chords, F13+, media keys)
// report false and are not sent.
func terminalKeyFor(k tea.KeyPressMsg) (protocol.TerminalKey, bool) {
	key := k.Key()
	mod := key.Mod &^ lockMods
	if mod&(tea.ModSuper|tea.ModHyper) != 0 {
		return protocol.TerminalKey{}, false
	}
	var mods uint8
	if mod&tea.ModShift != 0 {
		mods |= protocol.TerminalModShift
	}
	if mod&tea.ModAlt != 0 {
		mods |= protocol.TerminalModAlt
	}
	if mod&tea.ModCtrl != 0 {
		mods |= protocol.TerminalModCtrl
	}
	if mod&tea.ModMeta != 0 {
		mods |= protocol.TerminalModMeta
	}
	if name, ok := terminalKeyNames[key.Code]; ok {
		return protocol.TerminalKey{Code: name, Mods: mods}, true
	}
	text := key.Text
	if len(text) > protocol.TerminalMaxKeyText || !printable(text) {
		return protocol.TerminalKey{}, false
	}
	if key.Code == tea.KeyExtended {
		// Several runes from one key, for example an input method commit.
		r, _ := utf8.DecodeRuneInString(text)
		if text == "" || !unicode.IsPrint(r) {
			return protocol.TerminalKey{}, false
		}
		return protocol.TerminalKey{Code: string(r), Text: text, Mods: mods &^ protocol.TerminalModShift}, true
	}
	if !unicode.IsPrint(key.Code) {
		return protocol.TerminalKey{}, false
	}
	return protocol.TerminalKey{Code: string(key.Code), Text: text, Mods: mods}, true
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

func isTerminalEscape(k tea.KeyPressMsg) bool {
	key := k.Key()
	return key.Code == ']' && key.Mod&^lockMods == tea.ModCtrl || k.String() == terminalEscapeKey
}

// terminalInputActive reports whether keys and pastes go to the focused
// terminal; stale focus is dropped.
func (m *Model) terminalInputActive() (*termView, bool) {
	if m.termFocus == "" {
		return nil, false
	}
	tv := m.terms[m.termFocus]
	rec, ok := m.liveTerminal(m.termFocus)
	if m.focus != "terminal:"+m.termFocus || len(m.menu) > 0 || m.viewer != nil || m.settingsPage != "" || m.terminalTooSmall() ||
		!ok || rec.State != protocol.TerminalStateRunning || !m.controls(tv) || tv.ended != nil {
		m.termFocus = ""
		return nil, false
	}
	return tv, true
}

// terminalKey routes a key press while a terminal has input focus.
func (m *Model) terminalKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	tv, ok := m.terminalInputActive()
	if !ok {
		return nil, false
	}
	if isTerminalEscape(k) {
		m.termFocus = ""
		return nil, true
	}
	tk, ok := terminalKeyFor(k)
	if !ok {
		return nil, true
	}
	// Typing returns a scrolled view to the live screen.
	tv.scroll, tv.history, tv.historyAsked = 0, nil, false
	gen := tv.control.Gen
	return m.termWrite(tv, func(c terminalConn) error { return c.SendKey(gen, tk) }), true
}

// terminalPaste sends an outer-terminal paste to the focused terminal.
func (m *Model) terminalPaste(text string) (tea.Cmd, bool) {
	tv, ok := m.terminalInputActive()
	if !ok {
		return nil, false
	}
	text = strings.ToValidUTF8(text, "�")
	if text == "" {
		return nil, true
	}
	tv.scroll, tv.history, tv.historyAsked = 0, nil, false
	gen := tv.control.Gen
	return m.termWrite(tv, func(c terminalConn) error { return c.Paste(gen, text) }), true
}

// focusTerminal gives id keyboard focus and, when this client controls the
// running terminal, input focus; otherwise it explains why typing is refused.
func (m *Model) focusTerminal(id string) tea.Cmd {
	rec, ok := m.liveTerminal(id)
	if !ok {
		return nil
	}
	m.setFocus("terminal:" + id)
	tv := m.terms[id]
	_, _, ended := terminalEnded(rec, tv)
	switch {
	case ended || rec.State != protocol.TerminalStateRunning:
		return m.showNoticeAs(noticeUnavailable, "Terminal is not running · input unavailable")
	case tv == nil || tv.conn == nil || !tv.hasControl:
		return m.showNoticeAs(noticeUnavailable, "Terminal is connecting · try again in a moment")
	case !m.controls(tv):
		return m.showNoticeAs(noticeUnavailable, "Another client controls this terminal · Take control")
	}
	m.termFocus = id
	tv.scroll, tv.history, tv.historyAsked = 0, nil, false
	return nil
}

func (m *Model) terminalAction(a action) tea.Cmd {
	rec, ok := m.liveTerminal(a.ID)
	if !ok {
		return nil
	}
	switch a.Kind {
	case "terminal-focus":
		return m.focusTerminal(a.ID)
	case "terminal-take-control":
		return m.command(protocol.Command{Kind: "terminal.take-control", ThreadID: rec.ThreadID, TargetID: rec.ID}, a)
	case "terminal-retry-close":
		return m.command(protocol.Command{Kind: "terminal.close", ThreadID: rec.ThreadID, TargetID: rec.ID}, a)
	}
	return nil
}

// terminalHistoryKey scrolls a focused terminal pane's history (not while
// typing into it): arrows by a line, PgUp/PgDn by a page, Home to the oldest
// retained line and End back to the live screen.
func (m *Model) terminalHistoryKey(s string) (tea.Cmd, bool) {
	id, ok := strings.CutPrefix(m.focus, "terminal:")
	if !ok || m.termFocus == id {
		return nil, false
	}
	tv := m.terms[id]
	page := 8
	if tv != nil && tv.screen != nil {
		page = max(1, tv.screen.Rows-1)
	}
	switch s {
	case "up":
		return m.terminalScroll(id, 1), true
	case "down":
		return m.terminalScroll(id, -1), true
	case "pgup":
		return m.terminalScroll(id, page), true
	case "pgdown":
		return m.terminalScroll(id, -page), true
	case "home":
		return m.terminalScroll(id, 1<<30), true
	case "end":
		return m.terminalScroll(id, -(1 << 30)), true
	}
	return nil, false
}

// terminalScroll moves the history view by delta lines (positive is older).
// Full-screen programs (the alternate screen) have no history to scroll.
func (m *Model) terminalScroll(id string, delta int) tea.Cmd {
	tv := m.terms[id]
	if tv == nil || tv.screen == nil || delta == 0 {
		return nil
	}
	if delta > 0 && tv.screen.Alt && tv.scroll == 0 {
		return m.showNoticeAs(noticeUnavailable, "Terminal history is unavailable while a full-screen program runs")
	}
	if delta > 0 && tv.conn == nil {
		return m.showNoticeAs(noticeUnavailable, "Terminal history needs a live connection")
	}
	limit := 1 << 30
	if tv.history != nil {
		limit = tv.history.Total
	}
	tv.scroll = max(0, min(limit, tv.scroll+delta))
	if tv.scroll == 0 {
		tv.history, tv.historyAsked = nil, false
		return nil
	}
	return m.requestHistory(tv)
}

// requestHistory asks for the scrollback window the view needs. One request
// is outstanding at a time; each asks for at most protocol.TerminalMaxScrollback
// lines ending past the view so continued scrolling finds lines cached.
func (m *Model) requestHistory(tv *termView) tea.Cmd {
	if tv.scroll == 0 || tv.historyAsked || tv.conn == nil {
		return nil
	}
	n := protocol.TerminalMaxScrollback
	from := -1
	if h := tv.history; h != nil {
		rows := 24
		if tv.screen != nil {
			rows = tv.screen.Rows
		}
		top := h.Total - tv.scroll
		if top >= h.From || h.From == 0 {
			return nil
		}
		from = max(0, top-(n-min(rows, n)))
	}
	tv.historyAsked = true
	return m.termWrite(tv, func(c terminalConn) error { return c.RequestScrollback(from, n) })
}
