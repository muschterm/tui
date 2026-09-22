package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
				if selected != both || focused.background != selected.background || focused.border != selected.border || !focused.bold || !focused.focused {
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
	// Selection keeps bold; focus replaces the leading bracket with the plain
	// mark and never underlines.
	if !strings.HasPrefix(ansi.Strip(text)[2:], m.icon("focus")) || !strings.Contains(ansi.Strip(text), "]") || !strings.Contains(text, "\x1b[1") || underlined(text) {
		t.Fatalf("monochrome selection/focus cues missing: %q", text)
	}
}

// underlined reports whether any SGR sequence in s enables underline (a bare
// parameter 4, not a component of a 38;2 or 48;2 color triplet).
func underlined(s string) bool {
	for _, seq := range regexp.MustCompile(`\x1b\[([0-9;]*)m`).FindAllStringSubmatch(s, -1) {
		params := strings.Split(seq[1], ";")
		for i := 0; i < len(params); i++ {
			switch params[i] {
			case "38", "48", "58":
				if i+1 < len(params) && params[i+1] == "2" {
					i += 4
				} else if i+1 < len(params) && params[i+1] == "5" {
					i += 2
				}
			case "4":
				return true
			}
		}
	}
	return false
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

func TestIconHoverLiftsInkAndFocusMarksLeadingCell(t *testing.T) {
	m := testModel()
	p := m.colors()
	if got := m.lift(p.muted); got != p.text {
		t.Fatal("muted did not lift to text", got)
	}
	if got := m.lift(p.blue); got == p.blue || got == p.text || parseHex(got) == nil {
		t.Fatal("accent did not lift toward text", got)
	}
	m.setFocus("thread-create")
	f := m.render()
	h := controlHit(t, f, "thread-create")
	if h.Rect.W != 2 {
		t.Fatal("icon target is not glyph plus spill cell", h.Rect)
	}
	cells := ansi.Cut(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W)
	rgb := parseHex(m.lift(p.blue))
	ink := fmt.Sprintf("38;2;%d;%d;%d", rgb[0], rgb[1], rgb[2])
	// Both the glyph and its spill cell carry the lifted bold ink; nothing is
	// underlined, and the mark sits in the padding cell before the glyph.
	for x := h.Rect.X; x < h.Rect.X+h.Rect.W; x++ {
		cell := ansi.Cut(f.rows[h.Rect.Y], x, x+1)
		if !strings.Contains(cell, ink) || !strings.Contains(cell, "\x1b[1;") {
			t.Fatalf("focused icon cell %d lacks lifted bold ink: %q", x, cell)
		}
	}
	if underlined(cells) {
		t.Fatalf("focused icon still underlines: %q", cells)
	}
	if got := ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X-1, h.Rect.X)); got != m.icon("focus") {
		t.Fatalf("focus mark missing before the glyph: %q", got)
	}
	if !h.slot().Contains(h.Rect.X-1, h.Rect.Y) {
		t.Fatal("focus mark left the control's reserved slot")
	}
	m.setFocus("")
	unfocused := m.render()
	if ansi.Strip(unfocused.rows[h.Rect.Y]) != strings.Replace(ansi.Strip(f.rows[h.Rect.Y]), m.icon("focus"), " ", 1) {
		t.Fatal("focus moved content on the row")
	}
}

// Filled controls and tabs replace their leading end cap with the mark and
// keep every other cell where it was; text rows use the blank gutter before
// them when their container leaves one.
func TestFocusMarkReplacesCapWithoutMovingControls(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.openSurface("files", "")
	files, _ := m.viewState().Host.Active()
	m.openSurface("plan", "")
	for _, key := range []string{"tab:" + files.ID, "close:" + files.ID} {
		m.setFocus("")
		rest := m.render()
		m.setFocus(key)
		f := m.render()
		h := controlHit(t, f, key)
		tab := controlHit(t, f, "tab:"+files.ID)
		capX := tab.Rect.X - 3 // leading end cap before the three-cell icon slot
		if got := ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], capX, capX+1)); got != m.icon("focus") {
			t.Fatalf("%s: cap cell %q is not the focus mark", key, got)
		}
		after := ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], capX+1, m.width))
		before := ansi.Strip(ansi.Cut(rest.rows[h.Rect.Y], capX+1, m.width))
		if key == "close:"+files.ID {
			before = strings.Replace(before, m.icon("files"), m.icon("close"), 1)
		}
		if after != before {
			t.Fatalf("%s: focus moved tab cells\n%q\n%q", key, before, after)
		}
		if underlined(ansi.Cut(f.rows[h.Rect.Y], tab.Rect.X-3, tab.Rect.X+tab.Rect.W)) {
			t.Fatalf("%s: focused tab still underlines", key)
		}
	}
	// A dialog row has the dialog's blank inset cell before it.
	m.showChooser()
	m.menuIndex = 1
	f := m.render()
	h := controlHit(t, f, "menu:1")
	if got := ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X-1, h.Rect.X)); got != m.icon("focus") {
		t.Fatalf("text row lacks the leading focus mark: %q", got)
	}
	if underlined(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W)) {
		t.Fatal("marked text row still underlines")
	}
}
