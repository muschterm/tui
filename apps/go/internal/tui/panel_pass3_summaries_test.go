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
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// summaryScenario prepares one activity/usage state for captures and geometry checks.
func summaryScenario(m *Model, name string) {
	th := &m.snapshot.Threads[0]
	model := m.composerSelection().Model
	switch name {
	case "finished":
		finishActivity(m)
		th.Requests, th.Queue = nil, nil
	case "failed-child":
		finishActivity(m)
		th.Requests, th.Queue = nil, nil
		if len(th.Children) > 0 {
			th.Children[0].State = "failed"
		}
		if len(th.Plan) > 0 {
			th.Plan[len(th.Plan)-1].State = "interrupted"
		}
	case "usage-high":
		th.Usage = &protocol.Usage{Used: 170000, Size: 200000, Model: model, Source: "agent: session/usage"}
	case "usage-critical":
		th.Usage = &protocol.Usage{Used: 196000, Size: 200000, Model: model}
	case "usage-popup":
		th.Usage = &protocol.Usage{Used: 42000, Size: 200000, Model: model, Source: "agent: session/usage"}
		m.openUsageSummary()
	}
	m.configureInputs()
}

var summaryScenarios = []string{"working", "finished", "failed-child", "usage-unknown", "usage-high", "usage-critical", "usage-popup"}

func TestPanelPass3SummaryCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{160, 44}, {48, 30}} {
			for _, scenario := range summaryScenarios {
				m := testModel()
				m.state.Light = light
				m.activityPhase = 6
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				summaryScenario(m, scenario)
				name := fmt.Sprintf("%dx%d-light%t-%s.ansi", m.width, m.height, light, scenario)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestActivityMarkStates(t *testing.T) {
	for _, plain := range []bool{false, true} {
		m := testModel()
		m.plainIcons = plain
		want := map[string][2]string{
			"completed":   {"●", "+"},
			"failed":      {"✕", "x"},
			"interrupted": {"!", "!"},
			"waiting":     {"!", "!"},
			"paused":      {"!", "!"},
			"unknown":     {"?", "?"},
			"pending":     {"○", "o"},
		}
		for state, glyphs := range want {
			got := m.activityMark(activitySummary{State: state})
			if exp := glyphs[map[bool]int{false: 0, true: 1}[plain]]; got != exp {
				t.Fatalf("plain=%t %s: %q want %q", plain, state, got, exp)
			}
		}
		working := activitySummary{State: "working", Working: true}
		if got := m.activityMark(working); got != map[bool]string{false: "●", true: "*"}[plain] {
			t.Fatalf("working mark %q", got)
		}
		m.connected = false
		if got := m.activityMark(working); got != "?" {
			t.Fatalf("disconnected working must be unknown, got %q", got)
		}
	}
}

// Non-success states never borrow the success ink, in any palette.
func TestActivityInkNeverImpliesSuccess(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI, colorprofile.NoTTY} {
		m := testModel()
		m.colorProfile = profile
		green := m.colors().green
		for _, state := range []string{"failed", "interrupted", "waiting", "paused", "unknown", "pending"} {
			if m.activityColor(activitySummary{State: state}) == green {
				t.Fatalf("%v %s uses success ink", profile, state)
			}
		}
		// The pulse stays within one colour value: no retained ANSI growth.
		for phase := 0; phase < 12; phase++ {
			m.activityPhase = phase
			if c := m.activityColor(activitySummary{State: "working", Working: true}); len(c) > 7 {
				t.Fatalf("unbounded pulse colour %q", c)
			}
		}
	}
}

func TestUsageToneThresholdsAndUnknownGauge(t *testing.T) {
	m := testModel()
	if tone := m.usageTone(); tone != "muted" {
		t.Fatal("no telemetry must stay muted", tone)
	}
	if label, _ := m.usageGauge(); label != "Ctx ?" {
		t.Fatal(label)
	}
	model := m.composerSelection().Model
	for used, want := range map[int64]string{0: "muted", 79: "muted", 80: "gold", 94: "gold", 95: "red", 150: "red"} {
		m.snapshot.Threads[0].Usage = &protocol.Usage{Used: used, Size: 100, Model: model}
		if tone := m.usageTone(); tone != want {
			t.Fatalf("%d%%: %s want %s", used, tone, want)
		}
	}
	// Telemetry for another model is not the selected model's measurement.
	m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 99, Size: 100, Model: "other"}
	if tone := m.usageTone(); tone != "muted" {
		t.Fatal("incompatible telemetry coloured", tone)
	}
}

func TestUsagePopupPairsAreExplicit(t *testing.T) {
	m := testModel()
	m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 1, Size: 10, Model: m.composerSelection().Model, Source: "agent: session/usage"}
	m.openUsageSummary()
	var source, details bool
	for _, item := range m.menu {
		if item.PairLabel == "Source" && item.PairValue == "agent: session/usage" {
			source = true
		}
		if item.Label == "Usage details" && item.PairLabel == "" {
			details = true
		}
	}
	if !source || !details {
		t.Fatalf("source pair %t, details action %t", source, details)
	}
}

// Marks and tones change ink and glyphs only: strip rows, control rectangles
// and footer control widths are identical across every scenario.
func TestSummaryGeometryUnchangedAcrossStates(t *testing.T) {
	for _, width := range []int{48, 80, 160} {
		var base map[string]string
		for _, scenario := range []string{"working", "usage-unknown", "usage-high", "usage-critical"} {
			m := testModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			summaryScenario(m, scenario)
			f := m.render()
			geometry := map[string]string{}
			for _, h := range f.hits {
				if h.Key == "usage" || h.Key == "send" || h.Key == "agents" || h.Key == "plan" {
					geometry[h.Key] = fmt.Sprint(h.Rect)
				}
			}
			geometry["strip"] = fmt.Sprint(m.activityStripHeight(m.measure().geom.Center.W))
			if base == nil {
				base = geometry
				continue
			}
			for k, v := range geometry {
				if k != "usage" && base[k] != v {
					t.Fatalf("width %d %s: %s moved %s -> %s", width, scenario, k, base[k], v)
				}
			}
		}
	}
	for _, plain := range []bool{false, true} {
		m := testModel()
		m.plainIcons = plain
		for _, state := range []string{"completed", "failed", "interrupted", "unknown", "pending", "paused"} {
			if w := ansi.StringWidth(m.activityMark(activitySummary{State: state})); w != 1 {
				t.Fatalf("%s mark width %d", state, w)
			}
		}
	}
}
