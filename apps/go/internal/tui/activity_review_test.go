package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func finishActivity(m *Model) {
	t := &m.snapshot.Threads[0]
	t.State = "idle"
	t.NeedsResume = false
	for i := range t.Plan {
		t.Plan[i].State = "completed"
	}
	for i := range t.Children {
		t.Children[i].State = "completed"
	}
	m.configureInputs()
}

func hasControl(f frame, key string) bool {
	for _, h := range f.hits {
		if h.Key == key {
			return true
		}
	}
	return false
}

func TestMessagesUseAlignmentWithoutRoleHeaders(t *testing.T) {
	m := testModel()
	items := []protocol.Activity{
		{ID: "user", Role: "user", Title: "You", Text: "Question"},
		{ID: "reply", Role: "agent", Title: "Agent", Text: "Answer", State: "completed"},
		{ID: "tool", Role: "tool", Title: "Read files", Text: "Useful result", State: "completed"},
	}
	lines := m.activityLines(items, 60)
	var all []string
	for _, line := range lines {
		all = append(all, line.text)
	}
	text := strings.Join(all, "\n")
	if strings.Contains(text, "You") || strings.Contains(text, "Agent") || !strings.Contains(text, "Read files  ·  completed") {
		t.Fatalf("unexpected message headers: %q", text)
	}
	if lines[0].bg != colors(false).input || lines[3].bg != colors(false).canvas {
		t.Fatal("message backgrounds no longer distinguish authors")
	}
}

func TestActivitySummariesFinishDismissAndRestorePerThread(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.prompt.SetValue("Keep this draft")
	m.viewState().Draft = m.prompt.Value()
	if !strings.Contains(m.View().Content, "Thinking…") || hasControl(m.measure(), "dismiss-agents") {
		t.Fatal("running state missing Thinking or enables dismissal")
	}
	m.activate(action{Kind: "dismiss-agents", ID: agentSummary(m.thread()).Key})
	if m.viewState().DismissedAgents != "" {
		t.Fatal("running work was dismissed")
	}
	finishActivity(m)
	f := m.render()
	if strings.Contains(m.View().Content, "Thinking…") || hasControl(f, "interrupt") {
		t.Fatal("completed turn still thinking or stoppable")
	}
	if !strings.Contains(m.View().Content, "Agents") || !strings.Contains(m.View().Content, "Plan 3/3") {
		t.Fatal("completed summaries disappeared")
	}
	agents := controlHit(t, f, "agents")
	m.Update(tea.MouseMotionMsg{X: agents.Rect.X + 1, Y: agents.Rect.Y})
	f = m.render()
	if !strings.Contains(ansi.Strip(f.rows[agents.Rect.Y]), m.icon("close")) {
		t.Fatal("hover did not expose completed summary close")
	}
	childrenBefore, _ := json.Marshal(m.thread().Children)
	clickControl(m, controlHit(t, f, "dismiss-agents"))
	clickControl(m, controlHit(t, m.measure(), "dismiss-plan"))
	if hasControl(m.measure(), "agents") || hasControl(m.measure(), "plan") || m.prompt.Value() != "Keep this draft" {
		t.Fatal("dismissal did not hide only summaries")
	}
	childrenAfter, _ := json.Marshal(m.thread().Children)
	if string(childrenBefore) != string(childrenAfter) {
		t.Fatal("dismissal deleted history")
	}
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if hasControl(restored.measure(), "agents") || hasControl(restored.measure(), "plan") {
		t.Fatal("completed dismissal did not survive reconnect")
	}
	id := restored.state.Active
	restored.activate(action{Kind: "thread", ID: restored.snapshot.Threads[1].ID})
	restored.activate(action{Kind: "thread", ID: id})
	if hasControl(restored.measure(), "agents") || restored.prompt.Value() != "Keep this draft" {
		t.Fatal("thread switching lost local dismissal or draft")
	}
	s := restored.snapshot
	s.Revision++
	s.Threads[0].Activity = append(s.Threads[0].Activity, protocol.Activity{ID: "next-turn", Role: "user", Text: "New work"})
	restored.Update(snapshotMsg(s))
	if !hasControl(restored.measure(), "agents") || !hasControl(restored.measure(), "plan") {
		t.Fatal("new turn remained hidden by old dismissal")
	}
}

func TestActivityAnimationUsesOneClockAndStops(t *testing.T) {
	m := testModel()
	before, _ := json.Marshal(m.state)
	if m.nextActivityTick() == nil || m.nextActivityTick() != nil {
		t.Fatal("animation schedules more than one clock")
	}
	initial := m.activityColor(agentSummary(m.thread()))
	m.Update(activityTick{})
	if m.activityColor(agentSummary(m.thread())) == initial {
		t.Fatal("working circle did not change brightness")
	}
	after, _ := json.Marshal(m.state)
	if string(before) != string(after) || m.dirty {
		t.Fatal("animation dirtied persisted view state")
	}
	finishActivity(m)
	_, cmd := m.Update(activityTick{})
	if cmd != nil || m.activityTickPending || m.activityColor(agentSummary(m.thread())) != colors(false).green {
		t.Fatal("completion failed to stop animation with solid green")
	}
	m.snapshot.Threads[0].State = "running"
	m.connected = false
	if m.nextActivityTick() != nil || strings.Contains(m.View().Content, "Thinking…") {
		t.Fatal("disconnected client pretends live thinking")
	}
}

func TestAggregateActivationRetainsFullChildAccess(t *testing.T) {
	m := testModel()
	clickControl(m, controlHit(t, m.measure(), "agents"))
	tab, _ := m.viewState().Host.Active()
	if tab.Kind != "agents" || m.viewState().DetailID != "" {
		t.Fatal("group did not open the whole Agents surface")
	}
	for _, child := range m.thread().Children {
		if !strings.Contains(m.surfaceText(tab), child.Name) {
			t.Fatal("group lost child access", child.Name)
		}
	}
}

func TestMaximizeShownOnlyForVisibleHostAndWorksEmpty(t *testing.T) {
	m := testModel()
	if hasControl(m.measure(), "maximize") {
		t.Fatal("hidden panel exposes maximize")
	}
	m.activate(action{Kind: "maximize"})
	if m.state.Layout.Maximized || m.state.Layout.Right {
		t.Fatal("hidden maximize revealed panel")
	}
	m.activate(action{Kind: "right"})
	if !hasControl(m.measure(), "maximize") || !m.viewState().Host.Chooser {
		t.Fatal("visible empty host cannot maximize")
	}
	clickControl(m, controlHit(t, m.measure(), "maximize"))
	if !m.state.Layout.Maximized || !m.viewState().Host.Chooser || m.measure().geom.Right.W != m.width {
		t.Fatal("empty chooser did not maximize")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	if m.state.Layout.Maximized || !m.viewState().Host.Chooser {
		t.Fatal("F7 did not restore empty chooser")
	}
}

func TestEmptyMaximizedChooserKeepsFooterReachableAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{48, 22}, {60, 24}, {80, 30}, {120, 40}} {
		m := testModel()
		m.state.Layout.Right, m.state.Layout.Maximized = true, true
		m.viewState().Host.Chooser = true
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		f := m.measure()
		for _, h := range f.hits {
			if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > m.width || h.Rect.Y+h.Rect.H > m.height-1 {
				t.Fatalf("%v: control outside workspace: %#v", size, h)
			}
			if (h.Key == "chooser" || strings.HasPrefix(h.Key, "chooser:")) && h.Rect.Y+h.Rect.H > f.geom.Right.Y+f.geom.Right.H {
				t.Fatalf("%v: chooser covers composer: %#v", size, h)
			}
		}
		if !hasControl(f, "send") || !hasControl(f, "maximize") {
			t.Fatal("lost composer or restore control", size)
		}
	}
}

func TestActivityReviewCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, scenario := range []string{"working", "finished", "empty-max", "narrow"} {
			m := testModel()
			m.state.Light = light
			m.activityPhase = 6
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			if scenario == "finished" {
				finishActivity(m)
				m.snapshot.Threads[0].Requests = nil
				m.snapshot.Threads[0].Queue = nil
				m.hover = "agents"
			}
			if scenario == "empty-max" {
				m.activate(action{Kind: "right"})
				m.activate(action{Kind: "maximize"})
			}
			if scenario == "narrow" {
				m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
			}
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-light%t-%s.ansi", m.width, m.height, light, scenario)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestActivityLeadingDismissSlotAndMeasurement(t *testing.T) {
	for _, width := range []int{48, 80, 120} {
		for _, key := range []string{"", "agents", "dismiss-agents", "plan", "dismiss-plan"} {
			m := testModel()
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			finishActivity(m)
			m.hover = key
			painted, measured := m.render(), m.measure()
			for _, kind := range []string{"agents", "plan"} {
				label := controlHit(t, painted, kind)
				close := controlHit(t, painted, "dismiss-"+kind)
				if close.Rect.W != 1 || close.Rect.X+1 != label.Rect.X || close.Rect.Y != label.Rect.Y {
					t.Fatalf("dismissal is not in leading icon slot: %+v %+v", close, label)
				}
				if label != controlHit(t, measured, kind) || close != controlHit(t, measured, "dismiss-"+kind) {
					t.Fatal("measure and paint disagree")
				}
				glyph := ansi.Strip(ansi.Cut(painted.rows[close.Rect.Y], close.Rect.X, close.Rect.X+1))
				want := "●"
				if key == kind || key == "dismiss-"+kind {
					want = m.icon("close")
				}
				if glyph != want {
					t.Fatalf("%s/%s: glyph %q want %q", kind, key, glyph, want)
				}
			}
		}
	}
}

func TestUnfinishedActivityNeverHasDismissTarget(t *testing.T) {
	for _, state := range []string{"running", "waiting", "failed", "interrupted", "pending", "unknown"} {
		m := testModel()
		m.snapshot.Threads[0].Children[0].State = state
		m.snapshot.Threads[0].Plan[0].State = state
		m.hover = "agents"
		f := m.render()
		if hasControl(f, "dismiss-agents") || hasControl(f, "dismiss-plan") {
			t.Fatal("unfinished circle dismissible", state)
		}
		if agentSummary(m.thread()).Label != "Agents" {
			t.Fatal("agents label encodes state")
		}
		if !strings.Contains(controlHit(t, f, "agents").Label, agentSummary(m.thread()).State) {
			t.Fatal("help lost reported status")
		}
	}
}
