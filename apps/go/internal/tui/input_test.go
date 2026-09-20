package tui

import (
	"testing"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func inputFixture(value string) textarea.Model {
	a := textarea.New()
	a.SetWidth(40)
	a.SetHeight(5)
	a.SetValue(value)
	a.Focus()
	return a
}

func TestInputClusterNavigationAndDeletion(t *testing.T) {
	for _, cluster := range []string{"e\u0301", "👩‍💻", "🇨🇭", "👍🏽"} {
		t.Run(cluster, func(t *testing.T) {
			a := inputFixture("x" + cluster + "y")
			a.SetCursorColumn(1)
			updateInput(&a, inputKey(tea.KeyRight, false))
			if a.Column() != 1+utf8.RuneCountInString(cluster) {
				t.Fatalf("right stopped inside cluster at %d", a.Column())
			}
			updateInput(&a, inputKey(tea.KeyLeft, false))
			if a.Column() != 1 {
				t.Fatalf("left stopped inside cluster at %d", a.Column())
			}
			updateInput(&a, inputKey(tea.KeyDelete, false))
			if a.Value() != "xy" {
				t.Fatalf("forward delete split cluster: %q", a.Value())
			}
			a.SetValue("x" + cluster + "y")
			a.SetCursorColumn(1 + utf8.RuneCountInString(cluster))
			updateInput(&a, inputKey(tea.KeyBackspace, false))
			if a.Value() != "xy" {
				t.Fatalf("backspace split cluster: %q", a.Value())
			}
		})
	}
}

func TestInputSelectionReplacement(t *testing.T) {
	for _, cluster := range []string{"e\u0301", "👩‍💻", "🇨🇭"} {
		a := inputFixture("x" + cluster + "y")
		a.SetCursorColumn(1)
		updateInput(&a, inputKey(tea.KeyRight, true))
		if a.SelectedText() != cluster {
			t.Fatalf("shift-right selected %q, want %q", a.SelectedText(), cluster)
		}
		updateInput(&a, tea.PasteMsg{Content: "Q"})
		if a.Value() != "xQy" {
			t.Fatalf("paste replacement: %q", a.Value())
		}
		a.SetValue("x" + cluster + "y")
		a.SetCursorColumn(1 + utf8.RuneCountInString(cluster))
		updateInput(&a, inputKey(tea.KeyLeft, true))
		if a.SelectedText() != cluster {
			t.Fatalf("shift-left selected %q, want %q", a.SelectedText(), cluster)
		}
		updateInput(&a, tea.KeyPressMsg{Code: 'z', Text: "z"})
		if a.Value() != "xzy" {
			t.Fatalf("key replacement: %q", a.Value())
		}
	}
}

func TestInputRepairsPartialUpstreamSelection(t *testing.T) {
	a := inputFixture("xe\u0301y")
	a.SetCursorColumn(1)
	// Simulate an upstream pointer/word selection ending between e and accent.
	a, _ = a.Update(inputKey(tea.KeyRight, true))
	updateInput(&a, tea.PasteMsg{Content: "Q"})
	if a.Value() != "xQy" {
		t.Fatalf("partial selection replacement left broken grapheme: %q", a.Value())
	}
}

func TestInputNewlineBoundaries(t *testing.T) {
	a := inputFixture("e\u0301\n🇨🇭")
	a.MoveToBegin()
	updateInput(&a, inputKey(tea.KeyDelete, false))
	if a.Value() != "\n🇨🇭" {
		t.Fatalf("cluster delete unexpectedly merged next line: %q", a.Value())
	}
	updateInput(&a, inputKey(tea.KeyDelete, false))
	if a.Value() != "🇨🇭" {
		t.Fatalf("newline delete: %q", a.Value())
	}
	a.SetValue("one\ne\u0301")
	updateInput(&a, inputKey(tea.KeyBackspace, false))
	if a.Value() != "one\n" {
		t.Fatalf("second-line cluster backspace: %q", a.Value())
	}
	updateInput(&a, inputKey(tea.KeyBackspace, false))
	if a.Value() != "one" {
		t.Fatalf("newline backspace: %q", a.Value())
	}
}

func TestInputUnfocusedDoesNotEdit(t *testing.T) {
	a := inputFixture("🇨🇭")
	a.Blur()
	updateInput(&a, inputKey(tea.KeyBackspace, false))
	if a.Value() != "🇨🇭" {
		t.Fatal("unfocused input changed")
	}
}

func TestInputPasteLimitDoesNotSplitOrErase(t *testing.T) {
	a := inputFixture("x")
	a.CharLimit = 3
	updateInput(&a, tea.PasteMsg{Content: "🇨🇭z"})
	if a.Value() != "x🇨🇭" {
		t.Fatalf("cluster-aware limit: %q", a.Value())
	}
	a.SetValue("ab")
	a.CharLimit = 2
	a.SetCursorColumn(1)
	updateInput(&a, inputKey(tea.KeyRight, true))
	updateInput(&a, tea.PasteMsg{Content: "👩‍💻"})
	if a.Value() != "ab" || a.SelectedText() != "b" {
		t.Fatalf("rejected insertion erased selection: value=%q selection=%q", a.Value(), a.SelectedText())
	}
}

func TestInputWordDeleteAndVerticalMovement(t *testing.T) {
	a := inputFixture("one 👩‍💻")
	updateInput(&a, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl})
	if a.Value() != "one " {
		t.Fatalf("word deletion: %q", a.Value())
	}
	a.SetValue("abc\ne\u0301x")
	a.MoveToBegin()
	a.SetCursorColumn(1)
	updateInput(&a, inputKey(tea.KeyDown, false))
	if inputFloor(inputBounds(a.Value()), inputCursor(&a)) != inputCursor(&a) {
		t.Fatalf("vertical movement stopped inside cluster: %d", inputCursor(&a))
	}
}
