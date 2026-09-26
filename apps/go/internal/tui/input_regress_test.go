package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func typeInput(a *textarea.Model, s string) {
	for _, r := range s {
		updateInput(a, tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
}

func pressInput(a *textarea.Model, code rune, mod tea.KeyMod, n int) {
	for range n {
		updateInput(a, tea.KeyPressMsg(tea.Key{Code: code, Mod: mod}))
	}
}

// Regression: Backspace on an empty draft left an empty anchored selection at
// offset 0; later Backspaces then deleted from that stale anchor.
func TestInputBackspaceOnEmptyDoesNotCorruptLaterEdits(t *testing.T) {
	a := inputFixture("")
	pressInput(&a, tea.KeyBackspace, 0, 1)
	typeInput(&a, "ghijkl")
	pressInput(&a, tea.KeyBackspace, 0, 5)
	if got := a.Value(); got != "g" {
		t.Fatalf("after backspaces got %q, want %q", got, "g")
	}
	pressInput(&a, tea.KeyLeft, 0, 1)
	typeInput(&a, "X")
	if got := a.Value(); got != "Xg" {
		t.Fatalf("after Left+X got %q, want %q", got, "Xg")
	}
}

func TestInputBoundaryEditing(t *testing.T) {
	type key struct {
		code rune
		mod  tea.KeyMod
	}
	var (
		bs    = key{tea.KeyBackspace, 0}
		del   = key{tea.KeyDelete, 0}
		left  = key{tea.KeyLeft, 0}
		right = key{tea.KeyRight, 0}
		home  = key{tea.KeyHome, 0}
		end   = key{tea.KeyEnd, 0}
		wordB = key{'w', tea.ModCtrl}
		wordF = key{'d', tea.ModAlt}
	)
	boundary := []key{bs, del, left, right, home, end, wordB, wordF}
	for _, tc := range []struct {
		name  string
		value string
		start bool // cursor at start (else end)
	}{
		{"empty", "", true},
		{"single-start", "a", true},
		{"single-end", "a", false},
		{"wide-start", "👩‍💻", true},
		{"wide-end", "👩‍💻", false},
		{"multiline-start", "ab\ncd", true},
		{"multiline-end", "ab\ncd", false},
	} {
		for _, k := range boundary {
			t.Run(tc.name, func(t *testing.T) {
				a := inputFixture(tc.value)
				if tc.start {
					inputSetCursor(&a, 0)
				} else {
					a.MoveToEnd()
					a.CursorEnd()
				}
				// Repeat the boundary key: the first may edit, later ones must be no-ops.
				pressInput(&a, k.code, k.mod, 3)
				if a.HasSelection() {
					t.Fatalf("key %v left a selection", k)
				}
				base := a.Value()
				typeInput(&a, "ghijkl")
				pressInput(&a, tea.KeyBackspace, 0, 5)
				pressInput(&a, tea.KeyBackspace, 0, 1)
				if got := a.Value(); got != base {
					t.Fatalf("key %v: typed then erased, got %q want %q", k, got, base)
				}
			})
		}
	}
}

func TestInputPasteNormalizesCarriageReturns(t *testing.T) {
	for in, want := range map[string]string{
		"d1\rd2":     "d1\nd2",
		"d1\r\nd2":   "d1\nd2",
		"d1\nd2":     "d1\nd2",
		"a\r\r\nb\r": "a\n\nb\n",
	} {
		a := inputFixture("")
		updateInput(&a, tea.PasteMsg{Content: in})
		if got := a.Value(); got != want {
			t.Errorf("paste %q: got %q want %q", in, got, want)
		}
		if got := safe(normalizeInputNewlines(in)); got != want {
			t.Errorf("sanitised paste %q: got %q want %q", in, got, want)
		}
	}
	if got := singleLine(normalizeInputNewlines("a\rb")); got != "a b" {
		t.Errorf("single-line paste: got %q", got)
	}
}
