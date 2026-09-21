package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func TestComponentSelectionFocusAndDisabledPrecedence(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.ASCII} {
		for _, light := range []bool{false, true} {
			m := testModel()
			m.colorProfile = profile
			m.state.Light = light
			p := m.colors()
			for _, variant := range []componentVariant{roundedOutline, squareOutline, squareFill} {
				rest := m.componentStyle(variant, componentState{}, p.text, p.input)
				hover := m.componentStyle(variant, componentState{Hovered: true}, p.text, p.input)
				selected := m.componentStyle(variant, componentState{Selected: true}, p.text, p.input)
				both := m.componentStyle(variant, componentState{Selected: true, Hovered: true}, p.text, p.input)
				focused := m.componentStyle(variant, componentState{Selected: true, Focused: true}, p.text, p.input)
				disabled := m.componentStyle(variant, componentState{Disabled: true}, p.text, p.input)
				disabledHover := m.componentStyle(variant, componentState{Disabled: true, Selected: true, Hovered: true, Focused: true}, p.text, p.input)
				if selected != both || focused.background != selected.background || focused.border != selected.border || !focused.bold || !focused.underline {
					t.Fatal("hover/focus overwrote persistent selection", profile, variant)
				}
				if hover.bold || disabled != disabledHover {
					t.Fatal("hover looked selected or enabled a disabled control")
				}
				if variant != squareFill && (rest.background != hover.background || rest.background != selected.background) {
					t.Fatal("outlined component changed its background")
				}
				if variant == squareFill && profile >= colorprofile.ANSI256 && (hover.background == selected.background || hover.background == rest.background) {
					t.Fatal("hover fill indistinguishable from rest/selection")
				}
			}
		}
	}
}

func TestPointerHoverDoesNotMoveSelectedTabOrDispatchWork(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.openSurface("files", "")
	files, _ := m.viewState().Host.Active()
	m.openSurface("plan", "")
	plan, _ := m.viewState().Host.Active()
	before := m.render()
	name := controlHit(t, before, "tab:"+files.ID)
	selected := controlHit(t, before, "tab:"+plan.ID)
	saved, _ := json.Marshal(m.state)
	m.dirty = false
	m.Update(tea.MouseMotionMsg{X: name.Rect.X, Y: name.Rect.Y})
	after := m.render()
	now, _ := json.Marshal(m.state)
	if !bytes.Equal(saved, now) || m.dirty || m.busy != nil {
		t.Fatal("hover changed persistent state or dispatched work")
	}
	if controlHit(t, after, name.Key).Rect != name.Rect || controlHit(t, after, selected.Key).Rect != selected.Rect {
		t.Fatal("hover changed hit geometry")
	}
	if !m.controlState(true, selected.Key).Selected || m.hover != name.Key {
		t.Fatal("neighbor hover lost the active selection")
	}
	clickControl(m, controlHit(t, after, name.Key))
	active, _ := m.viewState().Host.Active()
	if active.ID != files.ID || len(m.viewState().Host.Tabs) != 2 {
		t.Fatal("selecting a hovered tab closed it")
	}
	m.setFocus("tab:" + plan.ID)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	active, _ = m.viewState().Host.Active()
	if active.ID != plan.ID || len(m.viewState().Host.Tabs) != 2 {
		t.Fatal("keyboard activation no longer selects the tab")
	}
}

func TestCompactControlsKeepGeometryAndMonochromeCues(t *testing.T) {
	m := testModel()
	m.colorProfile = colorprofile.ASCII
	m.focus = "sample"
	m.hover = "neighbor"
	f := frame{rows: []string{strings.Repeat(" ", 24)}}
	f.compactButton(m, 2, 0, 18, "界é sample", "sample", action{Kind: "sample"}, true, normalControl)
	h := controlHit(t, f, "sample")
	if h.Rect != (shell.Rect{X: 2, W: 18, H: 1}) || ansi.StringWidth(f.rows[0]) != 24 {
		t.Fatal("compact control changed measured geometry")
	}
	var out bytes.Buffer
	writer := colorprofile.Writer{Forward: &out, Profile: colorprofile.ASCII}
	if _, err := writer.Write([]byte(f.rows[0])); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(ansi.Strip(text), "[") || !strings.Contains(ansi.Strip(text), "]") || !strings.Contains(text, "\x1b[1;4") {
		t.Fatalf("monochrome selection/focus cues missing: %q", text)
	}
}

func TestComponentStateCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	states := []struct {
		label string
		s     componentState
	}{
		{"Rest", componentState{}}, {"Hovered", componentState{Hovered: true}},
		{"Selected", componentState{Selected: true}}, {"Selected + focus", componentState{Selected: true, Focused: true}},
		{"Disabled + hover", componentState{Disabled: true, Hovered: true}},
	}
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI, colorprofile.ASCII} {
		for _, light := range []bool{false, true} {
			m := testModel()
			m.colorProfile = profile
			m.state.Light = light
			p := m.colors()
			f := frame{rows: make([]string, 20)}
			for i := range f.rows {
				f.rows[i] = strings.Repeat(" ", 160)
			}
			f.fill(shell.Rect{W: 160, H: 20}, p, p.canvas)
			f.text(2, 0, 155, "Actual component output · rounded outline / square outline / compact square fill", p.text, p.canvas)
			for col, state := range states {
				x := 2 + col*32
				f.text(x, 2, 30, state.label, p.text, p.canvas)
				for row, variant := range []componentVariant{roundedOutline, squareOutline, squareFill} {
					y := 4 + row*5
					v := m.componentStyle(variant, state.s, p.text, p.canvas)
					if variant == squareFill {
						v = m.componentStyle(variant, state.s, p.text, p.input)
						f.compactControl(m, x, y+1, 28, centered("Sample action", 26), v)
					} else {
						f.componentBox(m, shell.Rect{X: x, Y: y, W: 28, H: 3}, variant, v, p.canvas)
						f.componentText(x+1, y+1, 26, centered("Sample action", 26), v)
					}
				}
			}
			var out bytes.Buffer
			writer := colorprofile.Writer{Forward: &out, Profile: profile}
			if _, err := writer.Write([]byte(strings.Join(f.rows, "\n"))); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, fmt.Sprintf("160x20-light%t-components-%s.ansi", light, profile))
			if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
