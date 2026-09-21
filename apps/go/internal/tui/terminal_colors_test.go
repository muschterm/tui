package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestColorNegotiationPreservesDraftsFocusAndReadingPosition(t *testing.T) {
	m, req := questionReviewModel()
	m.terminalColorOptions(func(string) string { return "" })
	m.prompt.SetValue(strings.Repeat("long draft line\n", 15))
	m.viewState().Draft = m.prompt.Value()
	m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
	m.configureInputs()
	m.promptView.ScrollTo(&m.prompt, m.promptMetrics.Total, 2)
	m.focus = "option:0"
	m.hover = "answer-submit"
	before, _ := json.Marshal(m.state)
	beforeGeometry := m.measure().geom
	generation := m.generation
	offset := m.promptView.offset
	_, cmd := m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.RequestTerminalVersion()) {
		t.Fatal("missing initial version query")
	}
	_, cmd = m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if cmd != nil {
		t.Fatal("repeated profile report retried probe")
	}
	_, cmd = m.Update(tea.TerminalVersionMsg{Name: "unverified-terminal 1.0"})
	if cmd != nil || m.colorProbe.capabilitiesRequested {
		t.Fatal("queried an unknown DCS parser")
	}
	_, cmd = m.Update(tea.TerminalVersionMsg{Name: "ghostty 1.2.0"})
	if cmd == nil || len(cmd().(tea.BatchMsg)) != 2 {
		t.Fatal("missing separate RGB/Tc queries")
	}
	_, cmd = m.Update(tea.TerminalVersionMsg{Name: "ghostty 1.2.0"})
	if cmd != nil || m.colorProfile != colorprofile.ANSI256 {
		t.Fatal("version alone upgraded color or retried capability queries")
	}
	// In a running tea.Program, the accepted report generates ColorProfileMsg.
	reply := tea.CapabilityMsg{Content: "Tc"}
	if m.filterColorReports(m, reply) == nil {
		t.Fatal("rejected an answer to the pending query")
	}
	m.Update(reply)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	after, _ := json.Marshal(m.state)
	if !bytes.Equal(before, after) || m.generation != generation || m.focus != "option:0" || m.hover != "answer-submit" || m.busy != nil || m.measure().geom != beforeGeometry || m.promptView.offset != offset {
		t.Fatal("capability negotiation changed work, focus or reading position")
	}
	if !strings.Contains(m.colorDiagnostics(), "capability confirmed") || !strings.Contains(m.promptView.View(&m.prompt), "38;2;") {
		t.Fatal("upgrade did not refresh diagnosis or cached input colors")
	}
	if cmd := m.updateColorProfile(colorprofile.TrueColor); cmd != nil {
		t.Fatal("successful upgrade restarted probing")
	}
}

func TestColorQueryResponderAndCapabilityValidation(t *testing.T) {
	for _, name := range []string{"ghostty 1.2.0", "kitty(0.43.0)", "iTerm2 3.6.11"} {
		m := testModel()
		m.updateColorProfile(colorprofile.ANSI256)
		if m.probeTerminalColors(name) == nil {
			t.Fatal("missing supported responder", name)
		}
		if m.filterColorReports(m, tea.CapabilityMsg{Content: "RGB=8"}) != (tea.CapabilityMsg{Content: "RGB"}) {
			t.Fatal("numeric true-color reply not normalized")
		}
		for _, content := range []string{"RGB=4", "RGB=", "RGB=garbage", "Co=256", "Tc=0"} {
			result := m.filterColorReports(m, tea.CapabilityMsg{Content: content})
			if result == (tea.CapabilityMsg{Content: "RGB"}) || result == (tea.CapabilityMsg{Content: "Tc"}) {
				t.Fatal("unverified reply upgraded renderer:", content)
			}
		}
	}
	for _, name := range []string{"", "ghostty ", "kitty()", "iTerm2 ", "Apple_Terminal 1", "tmux 3.5a", "xterm", "notghostty 1"} {
		if supportsColorQueries(name) {
			t.Fatal("unsupported responder accepted:", name)
		}
	}
}

func TestColorProbingRespectsDisabledAndAlreadyDetectedProfiles(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.NoTTY, colorprofile.ASCII, colorprofile.TrueColor} {
		m := testModel()
		m.terminalColorOptions(func(string) string { return "" })
		if cmd := m.updateColorProfile(profile); cmd != nil || m.colorProbe.versionRequested {
			t.Fatal("unnecessary probe for", profile)
		}
	}
	for _, value := range []string{"1", "0", "false", "please"} {
		m := testModel()
		m.terminalColorOptions(func(key string) string {
			if key == "NO_COLOR" {
				return value
			}
			return ""
		})
		if cmd := m.updateColorProfile(colorprofile.TrueColor); cmd != nil || m.colorProfile != colorprofile.ASCII || m.View().BackgroundColor != nil || m.View().ForegroundColor != nil {
			t.Fatal("NO_COLOR was overridden:", value)
		}
		if m.filterColorReports(m, tea.CapabilityMsg{Content: "RGB"}) != nil {
			t.Fatal("unsolicited reply overrode NO_COLOR")
		}
	}
	m := testModel()
	m.terminalColorOptions(func(key string) string {
		if key == "TERM_PROGRAM" {
			return "Apple_Terminal"
		}
		return ""
	})
	if cmd := m.updateColorProfile(colorprofile.ANSI256); cmd != nil {
		t.Fatal("known Apple Terminal should retain detected fallback")
	}
	if m.filterColorReports(m, tea.CapabilityMsg{Content: "Tc"}) != nil {
		t.Fatal("unsolicited reply could upgrade renderer")
	}
	key := tea.KeyPressMsg{Code: 'a', Text: "a"}
	if m.filterColorReports(m, key) != key {
		t.Fatal("color filter interferes with normal input")
	}
}

func TestFallbackPalettesKeepLargeSurfacesNeutral(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ANSI256} {
		for _, light := range []bool{false, true} {
			m, _ := questionReviewModel()
			m.state.Light = light
			m.configureInputs()
			fullColorText := ansi.Strip(m.View().Content)
			m.colorProfile, m.state.Light = profile, light
			m.configureInputs()
			p := m.colors()
			for _, value := range []string{p.canvas, p.nav, p.panel, p.input, p.selected} {
				i, err := strconv.Atoi(value)
				if err != nil || (profile == colorprofile.ANSI256 && (i < 232 || i > 255)) || (profile == colorprofile.ANSI && i != 0 && i != 7 && i != 8 && i != 15) {
					t.Fatalf("%s light=%t: non-neutral surface %q", profile, light, value)
				}
			}
			for phase := 0; phase < 12; phase++ {
				m.activityPhase = phase
				if strings.HasPrefix(m.activityColor(activitySummary{Working: true}), "#") {
					t.Fatal("fallback pulse interpolates RGB")
				}
			}
			view := m.View()
			// Low-color compact controls use their reserved end cells for brackets.
			normalize := strings.NewReplacer("[", " ", "]", " ")
			if normalize.Replace(ansi.Strip(view.Content)) != normalize.Replace(fullColorText) {
				t.Fatal("palette changed transcript alignment, wrapping or visible content")
			}
			if strings.Contains(view.Content, ";2;") || view.BackgroundColor != nil || view.ForegroundColor != nil {
				t.Fatal("fallback emitted RGB or OSC default-color overrides")
			}
			if !strings.Contains(view.Content, "Submit") {
				t.Fatal("fallback lost question actions")
			}
		}
	}
}

func TestColorReviewCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for color visual artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI} {
		for _, light := range []bool{false, true} {
			m, _ := questionReviewModel()
			m.state.Light = light
			m.updateColorProfile(profile)
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
			m.focus = "option:0"
			m.hover = "answer-submit"
			m.prompt.SetValue("Keep this draft while detecting colors…")
			m.configureInputs()
			var output bytes.Buffer
			writer := colorprofile.Writer{Forward: &output, Profile: profile}
			if _, err := writer.Write([]byte(m.View().Content)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, fmt.Sprintf("%dx%d-light%t-%s.ansi", m.width, m.height, light, profile))
			if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
