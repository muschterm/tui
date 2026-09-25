package term

import (
	"errors"
	"testing"
)

func TestEncodeKey(t *testing.T) {
	app := inputModes{cursorKeys: true, keypad: true}
	for _, tc := range []struct {
		name  string
		key   Key
		modes inputModes
		want  string
	}{
		{"letter", Key{Code: "a", Text: "a"}, inputModes{}, "a"},
		{"letter without text", Key{Code: "a"}, inputModes{}, "a"},
		{"shift letter", Key{Code: "a", Text: "A", Mods: ModShift}, inputModes{}, "A"},
		{"shift letter without text", Key{Code: "a", Mods: ModShift}, inputModes{}, "A"},
		{"unicode text", Key{Code: "é", Text: "é"}, inputModes{}, "é"},
		{"emoji cluster", Key{Code: "👍", Text: "👍🏽"}, inputModes{}, "👍🏽"},
		{"ctrl+c", Key{Code: "c", Mods: ModCtrl}, inputModes{}, "\x03"},
		{"ctrl+shift+c", Key{Code: "c", Mods: ModCtrl | ModShift}, inputModes{}, "\x03"},
		{"ctrl+]", Key{Code: "]", Mods: ModCtrl}, inputModes{}, "\x1d"},
		{"ctrl+space", Key{Code: "space", Mods: ModCtrl}, inputModes{}, "\x00"},
		{"ctrl+@", Key{Code: "@", Mods: ModCtrl}, inputModes{}, "\x00"},
		{"alt+x", Key{Code: "x", Text: "x", Mods: ModAlt}, inputModes{}, "\x1bx"},
		{"alt+ctrl+a", Key{Code: "a", Mods: ModAlt | ModCtrl}, inputModes{}, "\x1b\x01"},
		{"enter", Key{Code: "enter"}, inputModes{}, "\r"},
		{"alt+enter", Key{Code: "enter", Mods: ModAlt}, inputModes{}, "\x1b\r"},
		{"tab", Key{Code: "tab"}, inputModes{}, "\t"},
		{"shift+tab", Key{Code: "tab", Mods: ModShift}, inputModes{}, "\x1b[Z"},
		{"backspace", Key{Code: "backspace"}, inputModes{}, "\x7f"},
		{"ctrl+backspace", Key{Code: "backspace", Mods: ModCtrl}, inputModes{}, "\x08"},
		{"escape", Key{Code: "escape"}, inputModes{}, "\x1b"},
		{"space", Key{Code: "space", Text: " "}, inputModes{}, " "},
		{"up normal", Key{Code: "up"}, inputModes{}, "\x1b[A"},
		{"up application", Key{Code: "up"}, app, "\x1bOA"},
		{"left application", Key{Code: "left"}, app, "\x1bOD"},
		{"home", Key{Code: "home"}, inputModes{}, "\x1b[H"},
		{"end application", Key{Code: "end"}, app, "\x1bOF"},
		{"ctrl+left", Key{Code: "left", Mods: ModCtrl}, app, "\x1b[1;5D"},
		{"shift+up", Key{Code: "up", Mods: ModShift}, inputModes{}, "\x1b[1;2A"},
		{"alt+right", Key{Code: "right", Mods: ModAlt}, inputModes{}, "\x1b[1;3C"},
		{"delete", Key{Code: "delete"}, inputModes{}, "\x1b[3~"},
		{"insert", Key{Code: "insert"}, inputModes{}, "\x1b[2~"},
		{"pgup", Key{Code: "pgup"}, inputModes{}, "\x1b[5~"},
		{"ctrl+pgdown", Key{Code: "pgdown", Mods: ModCtrl}, inputModes{}, "\x1b[6;5~"},
		{"f1", Key{Code: "f1"}, inputModes{}, "\x1bOP"},
		{"shift+f1", Key{Code: "f1", Mods: ModShift}, inputModes{}, "\x1b[1;2P"},
		{"f5", Key{Code: "f5"}, inputModes{}, "\x1b[15~"},
		{"f12", Key{Code: "f12"}, inputModes{}, "\x1b[24~"},
		{"ctrl+f12", Key{Code: "f12", Mods: ModCtrl}, inputModes{}, "\x1b[24;5~"},
		{"keypad numeric", Key{Code: "kp5"}, inputModes{}, "5"},
		{"keypad application", Key{Code: "kp5"}, app, "\x1bOu"},
		{"keypad enter application", Key{Code: "kp-enter"}, app, "\x1bOM"},
	} {
		got, err := encodeKey(tc.key, tc.modes)
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: encodeKey(%+v) = %q, %v; want %q", tc.name, tc.key, got, err, tc.want)
		}
	}
}

func TestEncodeKeyRejectsInvalid(t *testing.T) {
	for _, k := range []Key{
		{},
		{Code: "f13"},
		{Code: "a", Text: "\x1b[31m"},
		{Code: "a", Text: "a\u202e"},
		{Code: "a", Text: string([]byte{0xff})},
		{Code: "a", Mods: 1 << 5},
		{Code: "unknown"},
		{Code: "\x01"},
		{Code: "a", Text: string(make([]byte, MaxKeyText+1))},
	} {
		if _, err := encodeKey(k, inputModes{}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("encodeKey(%+q) error = %v, want ErrInvalidKey", k, err)
		}
	}
}

func TestEncodePaste(t *testing.T) {
	if got := string(encodePaste("a\nb\r\nc", inputModes{})); got != "a\rb\rc" {
		t.Fatalf("plain paste = %q", got)
	}
	if got := string(encodePaste("x\x1b[201~y\n", inputModes{bracketedPaste: true})); got != "\x1b[200~x[201~y\r\x1b[201~" {
		t.Fatalf("bracketed paste = %q", got)
	}
}
