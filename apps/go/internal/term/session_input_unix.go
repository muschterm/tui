//go:build unix

package term

import "github.com/charmbracelet/x/ansi"

func (m *inputModes) set(mode ansi.Mode, on bool) {
	switch mode {
	case ansi.ModeCursorKeys:
		m.cursorKeys = on
	case ansi.ModeNumericKeypad:
		m.keypad = on
	case ansi.ModeBracketedPaste:
		m.bracketedPaste = on
	}
}

func (s *Session) inputModes() inputModes {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modes
}

// SendKey encodes one key press for the child's current input modes and
// writes it. Invalid keys report ErrInvalidKey.
func (s *Session) SendKey(k Key) error {
	b, err := encodeKey(k, s.inputModes())
	if err != nil {
		return err
	}
	return s.write(b)
}

// Paste sends text as a paste: bracketed when the child enabled bracketed
// paste mode, otherwise as typed. Text is limited to MaxInput bytes.
func (s *Session) Paste(text string) error {
	if len(text) > MaxInput {
		return ErrInputTooLarge
	}
	if text == "" {
		return nil
	}
	return s.write(encodePaste(text, s.inputModes()))
}
