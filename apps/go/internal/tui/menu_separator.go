package tui

// selectable reports whether a menu row can receive keyboard/wheel focus and
// a hit rectangle. Separators and notes are informational rows only.
func (item menuItem) selectable() bool {
	return !item.Separator && item.Note == ""
}

// menuStep moves a menu index by d rows, skipping separator rows. With wrap
// it cycles past either end (keyboard); otherwise it clamps (wheel). If the
// move lands on or past only separators, the index is left unchanged.
func (m *Model) menuStep(index, d int, wrap bool) int {
	n := len(m.menu)
	if n == 0 || d == 0 {
		return index
	}
	next := index + d
	if wrap {
		next = ((next % n) + n) % n
	} else {
		next = min(n-1, max(0, next))
	}
	step := 1
	if d < 0 {
		step = -1
	}
	for range n {
		if m.menu[next].selectable() {
			return next
		}
		next += step
		if wrap {
			next = ((next % n) + n) % n
		} else if next < 0 || next >= n {
			return index
		}
	}
	return index
}

// menuNearestSelectable clamps index into range and, if it lands on a
// separator, returns the nearest selectable row by searching outward in both
// directions. It returns the clamped index unchanged if the menu holds only
// separators.
func (m *Model) menuNearestSelectable(index int) int {
	n := len(m.menu)
	if n == 0 {
		return index
	}
	index = min(n-1, max(0, index))
	if m.menu[index].selectable() {
		return index
	}
	for d := 1; d < n; d++ {
		if index-d >= 0 && m.menu[index-d].selectable() {
			return index - d
		}
		if index+d < n && m.menu[index+d].selectable() {
			return index + d
		}
	}
	return index
}
