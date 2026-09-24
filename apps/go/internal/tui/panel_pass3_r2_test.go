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

// TestProjectStartingFolderIsADenseFieldRow verifies App Settings › General's
// "Project starting folder" is the same actionable pair (settingsField) row
// used on the Agents page, not a banded button, and that clicking it still
// opens the folder picker.
func TestProjectStartingFolderIsADenseFieldRow(t *testing.T) {
	m := pathModel()
	m.openSidebarSettings("general", "")
	f := m.render()
	h := controlHit(t, f, "sidebar-setting:project-directory")
	if h.Rect.H != 1 || h.Rect.W != f.settingsBody.W {
		t.Fatalf("starting folder field hit %+v is not one dense full-width row", h.Rect)
	}
	row := strings.TrimRight(ansi.Strip(f.rows[h.Rect.Y]), " ")
	if !strings.Contains(row, "Project starting folder") || !strings.Contains(row, "/work") {
		t.Fatalf("starting folder pair row missing label/value: %q", row)
	}
	clickControl(m, h)
	if m.projectMode != "project-root" {
		t.Fatal("starting folder field did not open the folder picker")
	}
}

// TestProjectStartingFolderTruncatesFromTheLeft checks that a long starting
// folder path keeps its tail visible (left truncation with a leading "…")
// rather than losing the identifying suffix to right truncation.
func TestProjectStartingFolderTruncatesFromTheLeft(t *testing.T) {
	m := pathModel()
	long := "/very/deeply/nested/path/that/will/not/fit/in/the/settings/row/width/at/all/project-name-tail"
	m.snapshot.AppSettings.ProjectDirectory = long
	m.openSidebarSettings("general", "")
	f := m.render()
	h := controlHit(t, f, "sidebar-setting:project-directory")
	row := ansi.Strip(f.rows[h.Rect.Y])
	if !strings.Contains(row, "project-name-tail") {
		t.Fatalf("long path lost its identifying tail: %q", row)
	}
	if strings.Contains(row, "/very/deeply") {
		t.Fatalf("long path was not truncated: %q", row)
	}
	if !strings.Contains(row, "…") {
		t.Fatalf("truncated path has no ellipsis: %q", row)
	}
}

func TestTruncatePathLeftKeepsTail(t *testing.T) {
	if got := truncatePathLeft("/short", 20); got != "/short" {
		t.Fatalf("short path was altered: %q", got)
	}
	long := "/home/user/projects/deeply/nested/example-repo"
	got := truncatePathLeft(long, 20)
	if ansi.StringWidth(got) > 20 {
		t.Fatalf("result exceeds room: %q (%d)", got, ansi.StringWidth(got))
	}
	if !strings.HasSuffix(got, "example-repo") {
		t.Fatalf("tail was not preserved: %q", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("truncated result missing leading ellipsis: %q", got)
	}
	if got := truncatePathLeft("anything", 0); got != "" {
		t.Fatalf("zero room should be empty, got %q", got)
	}
}

// TestProjectIconIsADenseFieldRow checks the Project settings page's Icon
// picker uses the same settingsField construct as the starting folder and
// Agents defaults, since it is also a true value-picker (a menu of a fixed
// icon set), unlike Name (a free-text rename dialog) or Remove (a real
// destructive action), which stay banded buttons.
func TestProjectIconIsADenseFieldRow(t *testing.T) {
	m := navigationModel()
	m.activate(action{Kind: "project-settings", ID: "alpha"})
	f := m.render()
	h := controlHit(t, f, "sidebar-setting:icon")
	if h.Rect.H != 1 || h.Rect.W != f.settingsBody.W {
		t.Fatalf("project icon field hit %+v is not one dense full-width row", h.Rect)
	}
	row := strings.TrimRight(ansi.Strip(f.rows[h.Rect.Y]), " ")
	if !strings.Contains(row, "Icon") {
		t.Fatalf("icon pair row missing its label: %q", row)
	}
	clickControl(m, h)
	if len(m.menu) == 0 || m.busy != nil {
		t.Fatal("icon field did not open its menu without writing")
	}
	// Name and Remove stay banded buttons: Name opens a free-text rename
	// dialog rather than choosing among enumerated values, and Remove is a
	// destructive action, not a value.
	nameHit := controlHit(t, f, "sidebar-setting:name")
	if nameHit.Rect.H == 1 {
		t.Fatal("project Name should remain a banded button, not a dense field")
	}
	removeHit := controlHit(t, f, "sidebar-setting:remove")
	if removeHit.Rect.H == 1 {
		t.Fatal("project Remove should remain a banded button, not a dense field")
	}
}

// TestRemoveProjectDiskNoteIsNotSelectable is the sidebar_settings.go /
// menu_separator.go / render.go side of the remove-project confirmation
// menu: "Files on disk will be kept" must be a muted, non-clickable note that
// keyboard/wheel navigation skips and that never counts toward the n/N
// position, unlike the two real choices (Cancel, Delete).
func TestRemoveProjectDiskNoteIsNotSelectable(t *testing.T) {
	m := navigationModel()
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].State = "idle"
		m.snapshot.Threads[i].Queue = nil
		m.snapshot.Threads[i].Requests = nil
		m.snapshot.Threads[i].Children = nil
	}
	m.activate(action{Kind: "project-settings", ID: "alpha"})
	m.activate(action{Kind: "project-remove", ID: "alpha"})
	if len(m.menu) != 3 {
		t.Fatalf("expected Cancel, Delete confirm and a note, got %d items", len(m.menu))
	}
	note := m.menu[2]
	if note.Note == "" || note.Separator {
		t.Fatalf("third item is not a note row: %+v", note)
	}
	if note.Action.Kind != "" {
		t.Fatalf("note row must not carry an action: %+v", note.Action)
	}

	// The note has no hit rectangle.
	f := m.render()
	for _, h := range f.hits {
		if h.Key == "menu:2" {
			t.Fatalf("note row must have no hit rectangle: %+v", h)
		}
	}

	// It is not counted in n/N: two real choices, so total must be 2.
	_, total := menuPosition(m.menu, m.menuIndex)
	if total != 2 {
		t.Fatalf("note row counted toward n/N: total=%d", total)
	}

	// Keyboard down/up from Cancel (index 0) skips straight to Delete (index
	// 1) and never lands on the note (index 2).
	if next := m.menuStep(0, 1, true); next != 1 {
		t.Fatalf("menuStep landed on %d, want 1 (Delete)", next)
	}
	if next := m.menuStep(1, 1, true); next != 0 {
		t.Fatalf("menuStep from Delete wrapped to %d, want 0 (Cancel), skipping the note", next)
	}
	if got := m.menuNearestSelectable(2); got == 2 {
		t.Fatal("menuNearestSelectable resolved to the non-selectable note row")
	}
}

// TestSettingsPass3R2Captures writes before/after review captures for the
// converted App General starting folder field, the Project Icon field and
// the remove-project note row.
func TestSettingsPass3R2Captures(t *testing.T) {
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
			{"general-directory-field", 160, 30, nil},
			{"general-directory-field", 120, 30, nil},
			{"general-directory-field", 72, 30, nil},
			{"general-directory-field", 47, 22, nil},
			{"project-icon-field", 120, 30, func(m *Model) { m.activate(action{Kind: "project-settings", ID: "alpha"}) }},
			{"remove-project-note", 120, 30, func(m *Model) {
				for i := range m.snapshot.Threads {
					m.snapshot.Threads[i].State = "idle"
					m.snapshot.Threads[i].Queue = nil
					m.snapshot.Threads[i].Requests = nil
					m.snapshot.Threads[i].Children = nil
				}
				m.activate(action{Kind: "project-settings", ID: "alpha"})
				m.activate(action{Kind: "project-remove", ID: "alpha"})
			}},
		} {
			m := navigationModel()
			m.state.Icons = "nerd"
			m.applyIcons()
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: c.width, Height: c.height})
			m.snapshot.AppSettings.ProjectDirectory = "/home/muschterm/Developer/git/github.com/muschterm/tui/apps/go"
			if strings.HasPrefix(c.name, "general") {
				m.openSidebarSettings("general", "")
			}
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
