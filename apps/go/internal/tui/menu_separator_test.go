package tui

import "testing"

func TestMenuNearestSelectableSkipsSeparators(t *testing.T) {
	m := testModel()
	m.menu = []menuItem{
		{Label: "a"},
		{Separator: true},
		{Separator: true},
		{Label: "b"},
		{Label: "c"},
	}
	cases := []struct {
		index int
		want  int
	}{
		{0, 0},
		{1, 0}, // nearer to index 0 than index 3
		{2, 3}, // nearer to index 3 than index 0
		{3, 3},
		{4, 4},
		{-1, 0},
		{10, 4},
	}
	for _, c := range cases {
		if got := m.menuNearestSelectable(c.index); got != c.want {
			t.Fatalf("menuNearestSelectable(%d) = %d, want %d", c.index, got, c.want)
		}
	}
}

func TestMenuNearestSelectableAllSeparatorsLeavesIndexUnchanged(t *testing.T) {
	m := testModel()
	m.menu = []menuItem{{Separator: true}, {Separator: true}}
	if got := m.menuNearestSelectable(1); got != 1 {
		t.Fatalf("menuNearestSelectable with only separators = %d, want 1 (unchanged)", got)
	}
}

// TestMenuScrollClampLandsOnSelectableItem covers the scrollbar-drag path:
// clamping menuIndex into the newly visible range must not strand it on a
// separator row.
func TestMenuScrollClampLandsOnSelectableItem(t *testing.T) {
	m := testModel()
	m.menu = []menuItem{
		{Label: "a"},
		{Label: "b"},
		{Separator: true},
		{Label: "d"},
		{Label: "e"},
	}
	m.menuIndex = 1
	m.menuOffset = 0
	f := frame{scrollbars: map[string]scrollTarget{
		"menu": {Bar: scrollbar{Viewport: 3, MaxOffset: 2}},
	}}
	// This clamps menuIndex onto index 2, the separator; index 1 ("b") and
	// index 3 ("d") are equally near, so the lower index wins the tie.
	m.scrollTo("menu", 2, f)
	if m.menu[m.menuIndex].Separator {
		t.Fatalf("menuIndex %d landed on a separator after scroll clamp", m.menuIndex)
	}
	if m.menuIndex != 1 {
		t.Fatalf("menuIndex = %d, want 1 (nearest selectable to the clamped separator)", m.menuIndex)
	}
}
