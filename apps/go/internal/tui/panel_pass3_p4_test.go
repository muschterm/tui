package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Usage renders inside the Activity singleton (surface_panel.go filters on
// DetailID=="usage"), but its tab must read "Usage" rather than the
// persisted "Activity" title, and revert once the user returns to the
// Activity list. The persisted shell.Surface.Title is never mutated.
func TestUsageActionTabTitleFollowsDetail(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.activate(action{Kind: "usage"})
	active, ok := m.viewState().Host.Active()
	if !ok || active.Kind != "activity" {
		t.Fatalf("usage action did not focus the activity singleton: %+v", active)
	}
	if m.viewState().DetailID != "usage" {
		t.Fatalf("DetailID = %q, want usage", m.viewState().DetailID)
	}
	if active.Title != title("activity") {
		t.Fatalf("persisted tab title changed: %q", active.Title)
	}

	f := m.render()
	h := controlHit(t, f, "tab:"+active.ID)
	if h.Label != "Usage" {
		t.Fatalf("tab label = %q, want Usage", h.Label)
	}
	row := ansi.Strip(f.rows[h.Rect.Y])
	if !strings.Contains(row, "Usage") || strings.Contains(row, "Activity") {
		t.Fatalf("tab row = %q, want Usage without Activity", row)
	}

	// Returning to the Activity list (re-selecting the surface) clears the
	// detail filter and restores the persisted title.
	m.activate(action{Kind: "open", Value: "activity"})
	if m.viewState().DetailID != "" {
		t.Fatalf("DetailID not cleared on return to list: %q", m.viewState().DetailID)
	}
	f = m.render()
	h = controlHit(t, f, "tab:"+active.ID)
	if h.Label != "Activity" {
		t.Fatalf("tab label after return to list = %q, want Activity", h.Label)
	}

	// Opening Activity again focuses the same singleton tab.
	if len(m.viewState().Host.Tabs) != 1 {
		t.Fatalf("opening Activity again created another tab: %+v", m.viewState().Host.Tabs)
	}
}

// The tab title follows the detail across a thread switch and restore: each
// thread's view keeps its own DetailID, so the surviving usage detail keeps
// reading "Usage" for its thread while a different thread's Activity tab
// reads "Activity".
func TestUsageTabTitleSurvivesThreadSwitch(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	first := m.snapshot.Threads[0].ID
	second := m.snapshot.Threads[1].ID

	m.activate(action{Kind: "thread", ID: first})
	m.activate(action{Kind: "usage"})
	active, _ := m.viewState().Host.Active()

	m.activate(action{Kind: "thread", ID: second})
	m.openSurface("activity", "")
	f := m.render()
	other, ok := m.viewState().Host.Active()
	if !ok || other.Kind != "activity" {
		t.Fatalf("second thread activity tab missing: %+v", other)
	}
	if h := controlHit(t, f, "tab:"+other.ID); h.Label != "Activity" {
		t.Fatalf("second thread tab label = %q, want Activity", h.Label)
	}

	m.activate(action{Kind: "thread", ID: first})
	f = m.render()
	if h := controlHit(t, f, "tab:"+active.ID); h.Label != "Usage" {
		t.Fatalf("restored tab label = %q, want Usage", h.Label)
	}
}

// The overflow "Opened surfaces" list shows each surface once, and the
// Activity entry follows the same detail-derived title as its tab.
func TestOverflowSurfaceListShowsUsageTitleOnce(t *testing.T) {
	m := testModel()
	m.openSurface("files", "")
	m.openSurface("plan", "")
	m.activate(action{Kind: "usage"})
	m.activate(action{Kind: "tabs"})

	if len(m.menu) != len(m.viewState().Host.Tabs) {
		t.Fatalf("overflow menu = %d items, want %d (one per surface)", len(m.menu), len(m.viewState().Host.Tabs))
	}
	seen := map[string]bool{}
	for _, item := range m.menu {
		if item.Action.Kind != "tab" || seen[item.Action.ID] {
			t.Fatalf("overflow menu must list each surface once: %+v", m.menu)
		}
		seen[item.Action.ID] = true
	}

	// The overflow list is rendered through the same f.tab() path as the
	// host's tab row, so its Activity row shows the derived title even
	// though the underlying menuItem still carries the persisted label.
	f := m.render()
	found := false
	for i, item := range m.menu {
		if item.Action.Value != "activity" {
			continue
		}
		h := controlHit(t, f, fmt.Sprintf("menu:%d", i))
		if h.Label != "Usage" {
			t.Fatalf("overflow row hit label = %q, want Usage", h.Label)
		}
		found = true
	}
	if !found {
		t.Fatal("activity row missing from overflow render")
	}
}

// TestTabTitleUsageCapture writes review captures of the right host's tab
// row while the Activity singleton shows its usage detail, for the p4
// tab-title-follows-detail fix (docs/research/go-panel-pass3-captures/final-p4).
func TestTabTitleUsageCapture(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		m := testModel()
		m.state.Light = light
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
		m.activate(action{Kind: "usage"})
		m.configureInputs()
		name := fmt.Sprintf("160x45-light%t-tab-usage.ansi", light)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
