package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestModalOutsideClickConsumesUnderlyingActions(t *testing.T) {
	for _, key := range []string{"send", "transcript", "thread:thread-review", "thread-delete"} {
		t.Run(key, func(t *testing.T) {
			m := testModel()
			m.width, m.height = 160, 50
			if key == "thread-delete" {
				m.snapshot.Threads[len(m.snapshot.Threads)-1].Closed = true
				m.state.RecentsCollapsed = false
			}
			m.prompt.SetValue("unsent é 👩🏽‍💻")
			active := m.state.Active
			var target hit
			found := false
			for _, h := range m.measure().hits {
				if h.Key == key || key == "thread:thread-review" && h.Action.Kind == "thread" && h.Action.ID == "thread-review" || key == "thread-delete" && h.Action.Kind == "thread-delete" {
					target, found = h, true
					break
				}
			}
			if !found {
				t.Fatalf("missing underlying target %s", key)
			}
			m.showMenu("Delete confirmation", []menuItem{{Label: "Cancel", Action: action{Kind: "menu-close"}}, {Label: "Delete", Action: action{Kind: "thread-delete-confirm", ID: active}}})
			if m.menuRect().Contains(target.Rect.X, target.Rect.Y) {
				t.Fatal("test target is inside modal")
			}
			m.mouse(tea.MouseClickMsg{X: target.Rect.X, Y: target.Rect.Y, Button: tea.MouseLeft})
			if len(m.menu) != 0 || m.busy != nil || m.state.Active != active || m.selecting || m.prompt.Value() != "unsent é 👩🏽‍💻" {
				t.Fatal("outside click did not dismiss safely")
			}
		})
	}
}

func TestModalInsideAndOtherPointerEventsDoNotDismiss(t *testing.T) {
	m := testModel()
	m.showMenu("Choose", []menuItem{{Label: "Keep open", Action: action{Kind: "noop"}}})
	r := m.menuRect()
	for _, point := range [][2]int{{r.X, r.Y}, {r.X + r.W - 1, r.Y + r.H - 1}, {r.X + 1, r.Y + 2}} {
		m.mouse(tea.MouseClickMsg{X: point[0], Y: point[1], Button: tea.MouseLeft})
		if len(m.menu) == 0 {
			t.Fatal("inside border or padding dismissed modal")
		}
	}
	m.mouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseRight})
	m.mouse(tea.MouseMotionMsg{X: 0, Y: 0})
	if len(m.menu) == 0 {
		t.Fatal("non-left click or motion dismissed modal")
	}
}

func TestModalDismissPreservesProjectInput(t *testing.T) {
	for _, mode := range []string{"add", "rename"} {
		m := testModel()
		m.showMenu("Project", []menuItem{{Label: "Apply", Action: action{Kind: "project-submit"}}})
		m.projectMode = mode
		m.projectInput.SetValue("unfinished project value")
		m.prompt.SetValue("draft")
		m.mouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
		if len(m.menu) != 0 || m.projectMode != "" || m.projectInput.Value() != "unfinished project value" || m.prompt.Value() != "draft" || m.busy != nil {
			t.Fatalf("%s dismissal lost input or submitted", mode)
		}
	}
}

func TestModalBackdropPreservesTextRolesAndModalContrast(t *testing.T) {
	for _, light := range []bool{false, true} {
		for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII} {
			t.Run(fmt.Sprintf("light%t/%s", light, profile), func(t *testing.T) {
				m := testModel()
				m.state.Light, m.colorProfile = light, profile
				p := m.colors()
				original := style(p.text, p.canvas).Render("context ") + style(p.red, p.canvas).Bold(true).Render("failure") + style(p.green, p.panel).Underline(true).Render("finished 界")
				f := frame{rows: []string{original}}
				m.renderModalBackdrop(&f)
				if ansi.Strip(f.rows[0]) != ansi.Strip(original) || ansi.StringWidth(f.rows[0]) != ansi.StringWidth(original) {
					t.Fatal("backdrop changed content or geometry")
				}
				if profile >= colorprofile.ANSI && f.rows[0] == original {
					t.Fatal("backdrop did not subdue colors")
				}
				if profile == colorprofile.ASCII && f.rows[0] != original {
					t.Fatal("monochrome fallback changed styling")
				}
				m.showMenu("Normal contrast", []menuItem{{Label: "Selected item", Action: action{Kind: "noop"}}})
				painted := m.render()
				var isolated frame
				isolated.rows = make([]string, m.height)
				for i := range isolated.rows {
					isolated.rows[i] = strings.Repeat(" ", m.width)
				}
				m.renderMenu(&isolated)
				r := m.menuRect()
				for y := r.Y + 1; y < r.Y+r.H-1; y++ {
					a := ansi.Cut(painted.rows[y], r.X+1, r.X+r.W-1)
					b := ansi.Cut(isolated.rows[y], r.X+1, r.X+r.W-1)
					// Cut can retain earlier SGR; compare the final visible run.
					if !strings.HasSuffix(a, b) && compactSGR(a) != compactSGR(b) {
						t.Fatalf("modal style differed from isolated paint at row %d", y)
					}
				}
				if again := m.render(); strings.Join(again.rows, "\n") != strings.Join(painted.rows, "\n") {
					t.Fatal("repainting accumulated backdrop styling")
				}
			})
		}
	}
}

func TestBackdropSGRRetainsAttributesAndDistinctRoleColors(t *testing.T) {
	for _, light := range []bool{false, true} {
		for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI} {
			m := testModel()
			m.state.Light, m.colorProfile = light, profile
			p := m.colors()
			f := frame{rows: []string{
				style(p.red, p.canvas).Bold(true).Underline(true).Render("role"),
				style(p.green, p.canvas).Bold(true).Underline(true).Render("role"),
			}}
			m.renderModalBackdrop(&f)
			if f.rows[0] == f.rows[1] {
				t.Fatalf("light%t/%s merged failure and completion colors", light, profile)
			}
			for _, row := range f.rows {
				if !strings.Contains(row, "\x1b[1;4;") {
					t.Fatalf("light%t/%s lost bold/underline: %q", light, profile, row)
				}
				if profile != colorprofile.TrueColor && (strings.Contains(row, "38;2;") || strings.Contains(row, "48;2;")) {
					t.Fatalf("light%t/%s introduced true color", light, profile)
				}
			}
		}
	}
}
