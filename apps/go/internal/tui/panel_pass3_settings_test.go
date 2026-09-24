package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestAgentsDefaultsFitThirtyRowsAsDenseFieldRows(t *testing.T) {
	for _, width := range []int{120, 72} {
		m := agentsDefaultsModel(width, 30, false)
		f := m.render()
		if f.settingsMax != 0 {
			t.Fatalf("%d: Agents page scrolls by %d rows at 30 rows", width, f.settingsMax)
		}
		for _, key := range []string{"agent", "model", "effort", "permissions", "context", "speed", "reset"} {
			h := controlHit(t, f, "sidebar-setting:"+key)
			if key != "reset" && (h.Rect.H != 1 || h.Rect.W != f.settingsBody.W) {
				t.Fatalf("%d: %s field hit %+v is not one full-width row", width, key, h.Rect)
			}
		}
		row := strings.TrimRight(ansi.Strip(f.rows[controlHit(t, f, "sidebar-setting:model").Rect.Y]), " ")
		if !strings.Contains(row, "Model") || !strings.Contains(row, "Sonnet") {
			t.Fatalf("%d: model pair missing: %q", width, row)
		}
	}
}

func TestAgentsFieldRowOpensItsMenuAndFixedValuesAreNotAccented(t *testing.T) {
	m := agentsDefaultsModel(120, 30, false)
	clickControl(m, controlHit(t, m.measure(), "sidebar-setting:model"))
	if len(m.menu) == 0 || m.busy != nil {
		t.Fatal("model field did not open its menu without writing")
	}
	m.menu = nil
	m.setFocus("sidebar-settings")
	f := m.render()
	p := m.colors()
	blue := style(p.blue, p.canvas).Render("x")
	blue = blue[:strings.Index(blue, "x")]
	if strings.Contains(f.rows[controlHit(t, f, "sidebar-setting:context").Rect.Y], blue) {
		t.Fatal("unselectable context value uses the actionable accent")
	}
	if !strings.Contains(f.rows[controlHit(t, f, "sidebar-setting:model").Rect.Y], blue) {
		t.Fatal("selectable model value lost its accent")
	}
}

func TestSettingsSidebarHeadingSelectionAndFocusMark(t *testing.T) {
	m := agentsDefaultsModel(120, 30, false)
	f := m.render()
	screen := frameText(f)
	if !strings.Contains(screen, "SETTINGS") || !strings.Contains(screen, "────") {
		t.Fatal("sidebar lost its panel heading and rule")
	}
	h := controlHit(t, f, "settings-category:about")
	before := ansi.Strip(f.rows[h.Rect.Y])
	m.setFocus("settings-category:about")
	after := ansi.Strip(m.render().rows[h.Rect.Y])
	at := strings.Index(after, "About")
	if ansi.StringWidth(after[:at]) != ansi.StringWidth(before[:strings.Index(before, "About")]) || !strings.Contains(after[:at], m.icon("focus")) {
		t.Fatalf("focus moved the label or lost its mark: %q -> %q", before, after)
	}
	clickControl(m, controlHit(t, f, "settings-category:appearance"))
	if m.settingsPage != "appearance" {
		t.Fatal("category click no longer changes page")
	}
}

func TestSettingsSidebarAndFieldsLimitedColorFallbacks(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ASCII} {
		m := agentsDefaultsModel(120, 30, false)
		m.colorProfile = profile
		m.state.Icons = "ascii"
		m.applyIcons()
		m.setFocus("sidebar-setting:model")
		f := m.render()
		cat := ansi.Strip(f.rows[controlHit(t, f, "settings-category:agents").Rect.Y])
		if !strings.Contains(cat, "[Agents") || !strings.Contains(cat, "]") {
			t.Fatalf("%v: selected category lost its bracket end cells: %q", profile, cat)
		}
		if other := ansi.Strip(f.rows[controlHit(t, f, "settings-category:about").Rect.Y]); strings.Contains(other, "[") {
			t.Fatalf("%v: unselected category bracketed: %q", profile, other)
		}
		row := ansi.Strip(f.rows[controlHit(t, f, "sidebar-setting:model").Rect.Y])
		if !strings.Contains(row, m.icon("focus")+" Model") || !strings.Contains(row, "Sonnet") {
			t.Fatalf("%v: focused field lost mark or pair: %q", profile, row)
		}
		if strings.ContainsAny(frameText(f), "▀▄") {
			t.Fatalf("%v: fallback painted half blocks", profile)
		}
	}
}

func TestSettingsSidebarAndFieldsDoNotGrowANSI(t *testing.T) {
	m := agentsDefaultsModel(120, 30, false)
	base := m.render().rows
	for _, h := range m.measure().hits {
		m.hover = h.Key
		m.setFocus(h.Key)
		m.render()
	}
	m.hover = ""
	m.setFocus("sidebar-settings")
	again := m.render().rows
	for i := range base {
		if len(again[i]) != len(base[i]) {
			t.Fatal("row styling grew after repeated painting", i, len(base[i]), len(again[i]))
		}
	}
}

func TestCompactSettingsCategoriesKeepHitsAndFocus(t *testing.T) {
	m := agentsDefaultsModel(47, 22, false)
	m.activate(action{Kind: "settings-nav"})
	f := m.render()
	for _, page := range m.settingsCategories() {
		controlHit(t, f, "settings-category:"+page)
	}
	controlHit(t, f, "settings-back")
	if m.focus != "settings-category:agents" {
		t.Fatal("compact categories lost focus on the current page", m.focus)
	}
	m.activate(action{Kind: "settings-nav"})
	controlHit(t, m.render(), "sidebar-setting:model")
}

// agentsDefaultsModel is an app Agents settings page with a saved Claude
// default, so every default field row is present.
func agentsDefaultsModel(width, height int, light bool) *Model {
	m := acpModel()
	m.state.Icons = "nerd"
	m.applyIcons()
	m.state.Light = light
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "new-thread-defaults", "app-settings")
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.openSidebarSettings("agents", "")
	m.activate(action{Kind: "app-thread-agent-set", ID: "claude", Revision: m.snapshot.AppSettings.Revision})
	if m.busy != nil {
		m.snapshot.AppSettings = *m.busy.AppSettings
		m.busy = nil
	}
	m.menu = nil
	m.setFocus("sidebar-settings")
	return m
}

// TestSettingsPass3Captures writes review captures of the settings category
// sidebar and the Agents page.
func TestSettingsPass3Captures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	type capture struct {
		name          string
		width, height int
		setup         func(*Model)
	}
	for _, light := range []bool{false, true} {
		for _, c := range []capture{
			{"agents", 160, 50, nil},
			{"agents", 120, 30, nil},
			{"agents", 72, 30, nil},
			{"agents-field-focus", 120, 30, func(m *Model) { m.setFocus("sidebar-setting:model") }},
			{"sidebar-hover", 120, 30, func(m *Model) { m.hover = "settings-category:appearance" }},
			{"sidebar-focus", 120, 30, func(m *Model) { m.setFocus("settings-category:about") }},
			{"compact-categories", 47, 22, func(m *Model) { m.activate(action{Kind: "settings-nav"}) }},
			{"compact-agents", 47, 22, nil},
		} {
			m := agentsDefaultsModel(c.width, c.height, light)
			if c.setup != nil {
				c.setup(m)
			}
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-light%t-%s.ansi", c.width, c.height, light, c.name)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
