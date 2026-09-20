package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"
)

// inputBounds uses rune offsets because Bubbles positions use rune columns.
// Each offset is nevertheless an extended grapheme boundary, not a code point.
func inputBounds(value string) []int {
	bounds := []int{0}
	g := uniseg.NewGraphemes(value)
	n := 0
	for g.Next() {
		n += utf8.RuneCountInString(g.Str())
		bounds = append(bounds, n)
	}
	return bounds
}

func inputOffset(a *textarea.Model, p textarea.Position) int {
	lines := strings.Split(a.Value(), "\n")
	n := 0
	for row := 0; row < p.Row && row < len(lines); row++ {
		n += utf8.RuneCountInString(lines[row]) + 1
	}
	return n + p.Col
}

func inputCursor(a *textarea.Model) int {
	return inputOffset(a, textarea.Position{Row: a.Line(), Col: a.Column()})
}

func inputFloor(bounds []int, offset int) int {
	result := 0
	for _, n := range bounds {
		if n > offset {
			break
		}
		result = n
	}
	return result
}

func inputCeil(bounds []int, offset int) int {
	for _, n := range bounds {
		if n >= offset {
			return n
		}
	}
	return bounds[len(bounds)-1]
}

func inputSetCursor(a *textarea.Model, offset int) {
	lines := strings.Split(a.Value(), "\n")
	row := 0
	for row < len(lines)-1 && offset > utf8.RuneCountInString(lines[row]) {
		offset -= utf8.RuneCountInString(lines[row]) + 1
		row++
	}
	a.MoveToBegin()
	// CursorDown moves through visual wraps before advancing a logical row.
	for a.Line() < row {
		a.CursorDown()
	}
	a.SetCursorColumn(offset)
}

func inputKey(code rune, shift bool) tea.KeyPressMsg {
	k := tea.Key{Code: code}
	if shift {
		k.Mod = tea.ModShift
	}
	return tea.KeyPressMsg(k)
}

// Selection endpoints originating in upstream word/vertical/pointer movement
// can fall inside a cluster. Expand before replacement so no partial cluster
// survives deletion. Reconstruct only malformed ranges, retaining normal ones.
func normalizeInputSelection(a *textarea.Model) {
	start, end, selected := a.Selection()
	bounds := inputBounds(a.Value())
	if !selected {
		offset := inputCursor(a)
		if snapped := inputFloor(bounds, offset); snapped != offset {
			inputSetCursor(a, snapped)
		}
		return
	}
	lo, hi := inputOffset(a, start), inputOffset(a, end)
	floor, ceil := inputFloor(bounds, lo), inputCeil(bounds, hi)
	if lo == floor && hi == ceil {
		return
	}
	a.ClearSelection()
	inputSetCursor(a, floor)
	for inputCursor(a) < ceil {
		*a, _ = a.Update(inputKey(tea.KeyRight, true))
	}
}

// updateInput adapts the pinned rune-based textarea's basic editing operations.
// Keep sending all textarea messages here, including paste, so replacement
// normalizes selections formed through pointer and vertical/word navigation.
func updateInput(a *textarea.Model, msg tea.Msg) tea.Cmd {
	if !a.Focused() {
		var cmd tea.Cmd
		*a, cmd = a.Update(msg)
		return cmd
	}
	k, isKey := msg.(tea.KeyPressMsg)
	_, isPaste := msg.(tea.PasteMsg)
	if !isKey && !isPaste {
		var cmd tea.Cmd
		*a, cmd = a.Update(msg)
		return cmd
	}
	normalizeInputSelection(a)
	// Upstream truncates incoming text by rune count. Trim only at cluster
	// boundaries first, and never erase a selection when no cluster fits.
	if a.CharLimit > 0 {
		incoming := ""
		if isPaste {
			incoming = msg.(tea.PasteMsg).Content
		} else {
			incoming = k.Text
		}
		if incoming != "" {
			selected := a.SelectedText()
			budget := a.CharLimit - a.Length() + uniseg.StringWidth(selected) + strings.Count(selected, "\n")
			limit := inputFloor(inputBounds(incoming), max(0, budget))
			if limit == 0 {
				return nil
			}
			incoming = string([]rune(incoming)[:limit])
			if isPaste {
				msg = tea.PasteMsg{Content: incoming}
			} else {
				k.Text = incoming
				msg = k
			}
		}
	}
	if isKey {
		km := a.KeyMap
		back := key.Matches(k, km.DeleteCharacterBackward)
		forward := key.Matches(k, km.DeleteCharacterForward)
		if (back || forward) && !a.HasSelection() {
			direction := rune(tea.KeyRight)
			if back {
				direction = tea.KeyLeft
			}
			moveInputCluster(a, inputKey(direction, true))
			if !a.HasSelection() {
				return nil
			}
			var cmd tea.Cmd
			*a, cmd = a.Update(k)
			return cmd
		}
		if key.Matches(k, km.CharacterForward, km.CharacterBackward, km.SelectCharacterForward, km.SelectCharacterBackward) {
			return moveInputCluster(a, k)
		}
		// Rune transposition can break a ZWJ/combining sequence. Leave this
		// unadvertised convenience binding disabled until cluster transpose exists.
		if key.Matches(k, km.TransposeCharacterBackward) {
			return nil
		}
		if !a.HasSelection() && key.Matches(k, km.DeleteWordBackward, km.DeleteWordForward) {
			move := inputKey(tea.KeyRight, true)
			if key.Matches(k, km.DeleteWordBackward) {
				move = inputKey(tea.KeyLeft, true)
			}
			move.Mod |= tea.ModCtrl
			*a, _ = a.Update(move)
			normalizeInputSelection(a)
			if !a.HasSelection() {
				code := rune(tea.KeyDelete)
				if key.Matches(k, km.DeleteWordBackward) {
					code = tea.KeyBackspace
				}
				return updateInput(a, inputKey(code, false))
			}
		}
	}
	var cmd tea.Cmd
	*a, cmd = a.Update(msg)
	if isKey && key.Matches(k, a.KeyMap.WordForward, a.KeyMap.WordBackward,
		a.KeyMap.LineNext, a.KeyMap.LinePrevious, a.KeyMap.SelectWordForward,
		a.KeyMap.SelectWordBackward, a.KeyMap.SelectLineUp, a.KeyMap.SelectLineDown,
		a.KeyMap.PageUp, a.KeyMap.PageDown) {
		normalizeInputSelection(a)
	}
	return cmd
}

func moveInputCluster(a *textarea.Model, k tea.KeyPressMsg) tea.Cmd {
	bounds := inputBounds(a.Value())
	var cmd tea.Cmd
	for {
		before := inputCursor(a)
		*a, cmd = a.Update(k)
		after := inputCursor(a)
		if before == after || inputFloor(bounds, after) == after {
			return cmd
		}
	}
}
