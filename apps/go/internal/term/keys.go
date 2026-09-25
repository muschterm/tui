package term

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Semantic input. Only the emulator knows which input modes the child has
// enabled (DECCKM application cursor keys, DECKPAM application keypad,
// bracketed paste), so clients send keys and pastes and the session encodes
// them here with the modes x/vt reports through its EnableMode/DisableMode
// callbacks.
//
// x/vt's own Emulator.SendKey/Paste write into the emulator's reply pipe,
// which this package drains through a bounded, lossy queue meant for query
// replies; keystrokes sent that way could be dropped under back-pressure or
// reordered against raw input. The encoding below mirrors x/vt's key table
// (xterm conventions) and adds xterm's modifier parameters for cursor,
// editing and function keys, then writes through the same ordered PTY path as
// Write.

// KeyMods is a bitset of key modifiers.
type KeyMods uint8

// Key modifiers.
const (
	ModShift KeyMods = 1 << iota
	ModAlt
	ModCtrl
	ModMeta
)

// MaxKeyText bounds the text of one key event in bytes.
const MaxKeyText = 256

// Key is one key press. Code names a special key ("enter", "tab",
// "backspace", "escape", "space", "up", "down", "left", "right", "home",
// "end", "pgup", "pgdown", "insert", "delete", "f1".."f12") or is the
// key's own character (for example "a" or "]"). Text is the text the key
// produces (it may be several runes, for example from an input method);
// when empty and Code is a single character, Code is used.
type Key struct {
	Code string
	Text string
	Mods KeyMods
}

// ErrInvalidKey reports a key that cannot be encoded.
var ErrInvalidKey = errors.New("term: invalid key")

// inputModes are the child-selected input modes that change key encoding.
type inputModes struct {
	cursorKeys     bool // DECCKM (?1): application cursor keys
	keypad         bool // DECKPAM / DECNKM (?66): application keypad
	bracketedPaste bool // ?2004
}

// csiFinal maps cursor keys and Home/End to their final byte.
var csiFinal = map[string]byte{"up": 'A', "down": 'B', "right": 'C', "left": 'D', "home": 'H', "end": 'F'}

// tildeKeys maps editing and function keys to their CSI n ~ parameter.
var tildeKeys = map[string]int{
	"insert": 2, "delete": 3, "pgup": 5, "pgdown": 6,
	"f5": 15, "f6": 17, "f7": 18, "f8": 19, "f9": 20, "f10": 21, "f11": 23, "f12": 24,
}

// ss3Keys maps F1-F4 to their SS3 final byte.
var ss3Keys = map[string]byte{"f1": 'P', "f2": 'Q', "f3": 'R', "f4": 'S'}

// keypadKeys maps keypad keys to their numeric text and application final.
var keypadKeys = map[string][2]string{
	"kp0": {"0", "p"}, "kp1": {"1", "q"}, "kp2": {"2", "r"}, "kp3": {"3", "s"}, "kp4": {"4", "t"},
	"kp5": {"5", "u"}, "kp6": {"6", "v"}, "kp7": {"7", "w"}, "kp8": {"8", "x"}, "kp9": {"9", "y"},
	"kp-enter": {"\r", "M"}, "kp-plus": {"+", "k"}, "kp-minus": {"-", "m"}, "kp-multiply": {"*", "j"},
	"kp-divide": {"/", "o"}, "kp-decimal": {".", "n"}, "kp-equal": {"=", "X"}, "kp-comma": {",", "l"},
}

// xtermModifier is xterm's modifier parameter: 1 + shift(1) + alt(2) +
// ctrl(4) + meta(8).
func xtermModifier(m KeyMods) int {
	n := 1
	if m&ModShift != 0 {
		n++
	}
	if m&ModAlt != 0 {
		n += 2
	}
	if m&ModCtrl != 0 {
		n += 4
	}
	if m&ModMeta != 0 {
		n += 8
	}
	return n
}

// ctrlByte returns the C0 byte a Ctrl-chord of c produces, as xterm does.
func ctrlByte(c rune) (byte, bool) {
	switch {
	case c >= 'a' && c <= 'z':
		return byte(c - 'a' + 1), true
	case c >= 'A' && c <= 'Z':
		return byte(c - 'A' + 1), true
	}
	switch c {
	case '@', ' ', '2', '`':
		return 0x00, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '7', '/', '-':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}

// validText reports printable key text: valid UTF-8 without control or
// format characters other than the joiners emoji sequences need.
func validText(s string) bool {
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

// encodeKey returns the bytes a key press sends under the given modes.
func encodeKey(k Key, m inputModes) ([]byte, error) {
	if len(k.Code) == 0 || len(k.Code) > 32 || len(k.Text) > MaxKeyText || k.Mods > ModShift|ModAlt|ModCtrl|ModMeta || !validText(k.Text) {
		return nil, ErrInvalidKey
	}
	// Alt and Meta prefix ESC to plain and control keys (xterm metaSendsEscape).
	esc := ""
	if k.Mods&(ModAlt|ModMeta) != 0 {
		esc = "\x1b"
	}
	mods := k.Mods
	plain := mods == 0
	switch k.Code {
	case "enter":
		return []byte(esc + "\r"), nil
	case "tab":
		if mods&ModShift != 0 {
			return []byte(esc + "\x1b[Z"), nil
		}
		return []byte(esc + "\t"), nil
	case "backspace":
		if mods&ModCtrl != 0 {
			return []byte(esc + "\x08"), nil
		}
		return []byte(esc + "\x7f"), nil
	case "escape":
		return []byte(esc + "\x1b"), nil
	case "space":
		if mods&ModCtrl != 0 {
			return []byte(esc + "\x00"), nil
		}
		return []byte(esc + " "), nil
	}
	if final, ok := csiFinal[k.Code]; ok {
		if plain {
			if m.cursorKeys {
				return []byte{0x1b, 'O', final}, nil
			}
			return []byte{0x1b, '[', final}, nil
		}
		return []byte("\x1b[1;" + strconv.Itoa(xtermModifier(mods)) + string(final)), nil
	}
	if n, ok := tildeKeys[k.Code]; ok {
		if plain {
			return []byte("\x1b[" + strconv.Itoa(n) + "~"), nil
		}
		return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(xtermModifier(mods)) + "~"), nil
	}
	if final, ok := ss3Keys[k.Code]; ok {
		if plain {
			return []byte{0x1b, 'O', final}, nil
		}
		return []byte("\x1b[1;" + strconv.Itoa(xtermModifier(mods)) + string(final)), nil
	}
	if kp, ok := keypadKeys[k.Code]; ok {
		if m.keypad {
			return []byte(esc + "\x1bO" + kp[1]), nil
		}
		return []byte(esc + kp[0]), nil
	}
	// A character key: its own text, a Ctrl chord or both with an ESC prefix.
	code, size := utf8.DecodeRuneInString(k.Code)
	single := size == len(k.Code) && code != utf8.RuneError
	if strings.HasPrefix(k.Code, "f") && !single {
		return nil, ErrInvalidKey // unsupported function key such as f13
	}
	if mods&ModCtrl != 0 && single {
		if b, ok := ctrlByte(code); ok {
			return []byte(esc + string(rune(b))), nil
		}
	}
	text := k.Text
	if text == "" {
		if !single || !validText(k.Code) {
			return nil, ErrInvalidKey
		}
		text = k.Code
		if mods&ModShift != 0 {
			text = strings.ToUpper(text)
		}
	}
	return []byte(esc + text), nil
}

// bracketedPasteEnd ends a bracketed paste; pasted text must not contain it.
const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)

// encodePaste returns the bytes a paste sends. Line endings become CR, as
// terminals send pasted newlines. With bracketed paste the text is wrapped
// and every ESC inside it is dropped, so pasted text can neither end the
// bracket early nor carry escape sequences the child would treat as keys;
// without it, text is sent as typed.
func encodePaste(text string, m inputModes) []byte {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\r"), "\n", "\r")
	if !m.bracketedPaste {
		return []byte(text)
	}
	return []byte(bracketedPasteStart + strings.ReplaceAll(text, "\x1b", "") + bracketedPasteEnd)
}
