package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestEmptyThreadBandedButtonsUseSharedBandHelpers verifies the empty-thread
// action buttons (navigation.go) still paint a three-row band and register a
// single hit spanning it, matching the geometry the shared panel.go band
// helpers (paintPanelBandEdge/registerPanelBandHit) produce for the chooser
// tiles and settings band buttons.
func TestEmptyThreadBandedButtonsUseSharedBandHelpers(t *testing.T) {
	m := testModel()
	m.snapshot.Threads = nil
	m.state.Active = ""
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	f := m.render()

	if !panelBandsSupported(m) {
		t.Fatal("rich test model should support bands")
	}
	for _, key := range []string{"empty-new", "empty-closed", "empty-project"} {
		h, ok := hitByKey(f, key)
		if !ok {
			t.Fatalf("missing hit for %s", key)
		}
		if h.Rect.H != 3 {
			t.Fatalf("%s: want a 3-row band hit, got H=%d", key, h.Rect.H)
		}
	}
	// Exactly one hit per action: registerPanelBandHit must not double-register
	// across the three painted band rows.
	counts := map[string]int{}
	for _, h := range f.hits {
		counts[h.Key]++
	}
	for _, key := range []string{"empty-new", "empty-closed", "empty-project"} {
		if counts[key] != 1 {
			t.Fatalf("%s: want exactly one hit, got %d", key, counts[key])
		}
	}
}

// TestEmptyThreadButtonsFallBackWithoutBands verifies the single-row
// `[ Label ]` fallback still paints and registers one hit per action when
// bands are unsupported (plain icons), unchanged by the band-helper reuse.
func TestEmptyThreadButtonsFallBackWithoutBands(t *testing.T) {
	m := testModel()
	m.snapshot.Threads = nil
	m.state.Active = ""
	m.plainIcons = true
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	f := m.render()

	if panelBandsSupported(m) {
		t.Fatal("plain-icon test model should not support bands")
	}
	for _, key := range []string{"empty-new", "empty-closed", "empty-project"} {
		h, ok := hitByKey(f, key)
		if !ok {
			t.Fatalf("missing hit for %s", key)
		}
		if h.Rect.H != 1 {
			t.Fatalf("%s: want a 1-row fallback hit, got H=%d", key, h.Rect.H)
		}
	}
}

// TestProjectFilterMenuSkipsSeparators verifies projectKey's up/down/tab
// arithmetic (projects.go) is separator-aware: it never lands the menu index
// on a rule row, matching the shared menuStep behavior used elsewhere.
func TestProjectFilterMenuSkipsSeparators(t *testing.T) {
	m := testModel()
	m.openProjectDialog("filter")
	m.menu = []menuItem{
		{Label: "All projects", Action: action{Kind: "project-filter"}},
		{Separator: true},
		{Label: "Alpha", Action: action{Kind: "project-filter", ID: "alpha"}},
		{Label: "Beta", Action: action{Kind: "project-filter", ID: "beta"}},
		{Separator: true},
		{Label: "Add project…", Action: action{Kind: "project-add"}},
	}
	assertNotSeparator := func(t *testing.T, label string) {
		t.Helper()
		if m.menuIndex < 0 || m.menuIndex >= len(m.menu) || m.menu[m.menuIndex].Separator {
			t.Fatalf("%s: menu index %d landed on a separator", label, m.menuIndex)
		}
	}

	// Down from "All projects" must skip the separator and land on "Alpha".
	m.menuIndex = 0
	m.key(tea.KeyPressMsg{Code: tea.KeyDown})
	assertNotSeparator(t, "down")
	if m.menu[m.menuIndex].Label != "Alpha" {
		t.Fatalf("down: want Alpha, got %q", m.menu[m.menuIndex].Label)
	}

	// Up from "Alpha" must skip the separator and land back on "All projects".
	m.key(tea.KeyPressMsg{Code: tea.KeyUp})
	assertNotSeparator(t, "up")
	if m.menu[m.menuIndex].Label != "All projects" {
		t.Fatalf("up: want All projects, got %q", m.menu[m.menuIndex].Label)
	}

	// Shift+Tab backward from "Add project…" (index 5) must skip the trailing
	// separator and land on "Beta"; starting here (rather than on a
	// project-filter row with an ID) avoids the unrelated gear-focus toggle
	// that "tab"/"shift+tab" also perform in filter mode.
	m.menuIndex = 5
	m.key(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	assertNotSeparator(t, "shift+tab")
	if m.menu[m.menuIndex].Label != "Beta" {
		t.Fatalf("shift+tab: want Beta, got %q", m.menu[m.menuIndex].Label)
	}
}

// TestProjectDialogMenuSkipsSeparators verifies the plain tab/shift+tab
// wrap-around arithmetic used outside filter mode (e.g. "new-thread") is also
// separator-aware.
func TestProjectDialogMenuSkipsSeparators(t *testing.T) {
	m := testModel()
	m.openProjectDialog("new-thread")
	m.menu = []menuItem{
		{Label: "Alpha", Action: action{Kind: "thread-create", Value: "alpha"}},
		{Separator: true},
		{Label: "Beta", Action: action{Kind: "thread-create", Value: "beta"}},
		{Label: "Add project…", Action: action{Kind: "project-add-thread"}},
	}
	m.menuIndex = 0
	m.key(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.menu[m.menuIndex].Separator {
		t.Fatalf("tab landed on a separator at index %d", m.menuIndex)
	}
	if m.menu[m.menuIndex].Label != "Beta" {
		t.Fatalf("tab: want Beta, got %q", m.menu[m.menuIndex].Label)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.menu[m.menuIndex].Separator {
		t.Fatalf("shift+tab landed on a separator at index %d", m.menuIndex)
	}
	if m.menu[m.menuIndex].Label != "Alpha" {
		t.Fatalf("shift+tab: want Alpha, got %q", m.menu[m.menuIndex].Label)
	}
}
