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

func TestThreadIndicatorPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		thread protocol.Thread
		want   threadIndicatorState
	}{
		{"running", protocol.Thread{State: "running"}, threadWorking},
		{"async question", protocol.Thread{State: "running", Requests: []protocol.Request{{State: "pending"}}}, threadAttention},
		{"waiting", protocol.Thread{State: "waiting"}, threadAttention},
		{"restart", protocol.Thread{State: "running", NeedsResume: true}, threadAttention},
		{"queued", protocol.Thread{State: "idle", Queue: []protocol.Prompt{{}}}, threadAttention},
		{"interrupted", protocol.Thread{State: "interrupted"}, threadAttention},
		{"failed over pending", protocol.Thread{State: "failed", Requests: []protocol.Request{{State: "pending"}}}, threadFailed},
		{"API error", protocol.Thread{State: "error"}, threadFailed},
		{"child error", protocol.Thread{State: "running", Children: []protocol.Child{{State: "error"}}}, threadFailed},
		{"child working", protocol.Thread{State: "idle", Children: []protocol.Child{{State: "running"}}}, threadWorking},
		{"idle", protocol.Thread{State: "idle"}, threadFinished},
		{"finished", protocol.Thread{State: "completed", Children: []protocol.Child{{State: "completed"}}}, threadFinished},
		{"unknown", protocol.Thread{State: "unknown"}, threadUnknown},
		{"unknown child", protocol.Thread{State: "idle", Children: []protocol.Child{{State: "unknown"}}}, threadUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadIndicator(tc.thread); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestThreadIndicatorHoverPreservesStatusUntilFinished(t *testing.T) {
	for _, state := range []string{"running", "waiting", "failed", "idle"} {
		for _, light := range []bool{false, true} {
			m := navigationModel()
			m.state.Light = light
			th := &m.snapshot.Threads[0]
			th.State, th.Queue, th.Requests, th.Children = state, nil, nil, nil
			id := th.ID
			m.hover = "thread-quick:" + id
			f := m.render()
			h := controlHit(t, f, m.hover)
			glyph, action := "●", "thread"
			if state == "idle" {
				glyph, action = m.icon("check"), "thread-close"
			}
			if h.Action.Kind != action || !strings.Contains(ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+2)), glyph) {
				t.Fatalf("%s: hover replaced status or routed wrong action", state)
			}
			p := colors(light)
			want := map[string]string{"waiting": p.gold, "failed": p.red, "idle": p.green}[state]
			if state != "running" && m.threadIndicatorColor(threadIndicator(*th)) != want {
				t.Fatalf("%s: wrong color", state)
			}
			if state != "idle" {
				clickControl(m, h)
				if m.busy != nil {
					t.Fatal("status click attempted to close unfinished thread")
				}
			}
			more := controlHit(t, f, "thread-menu:"+id)
			if !strings.Contains(ansi.Strip(ansi.Cut(f.rows[more.Rect.Y], more.Rect.X, more.Rect.X+3)), m.icon("more-vertical")) {
				t.Fatal("thread menu is not vertical")
			}
			m.connected = false
			if m.threadIndicatorColor(threadIndicator(*th)) != p.muted {
				t.Fatal("disconnected status claims live state")
			}
		}
	}
}

func TestThreadIndicatorsAnimateVisibleBackgroundWork(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads = []protocol.Thread{{ID: "selected", State: "idle", ProjectID: "alpha"}, {ID: "background", State: "running", ProjectID: "beta"}}
	m.state.Active = "selected"
	m.configureInputs()
	m.activityTickPending = false
	before, _ := json.Marshal(m.state)
	m.dirty = false
	if m.nextActivityTick() == nil || m.nextActivityTick() != nil {
		t.Fatal("background work did not reuse one clock")
	}
	initial := m.threadIndicatorColor(threadWorking)
	m.Update(activityTick{})
	after, _ := json.Marshal(m.state)
	if initial == m.threadIndicatorColor(threadWorking) || string(before) != string(after) || m.dirty {
		t.Fatal("pulse failed or wrote view state")
	}
	m.state.Layout.Left = false
	if m.activityAnimating() {
		t.Fatal("hidden navigation keeps clock running")
	}
	m.state.Layout.Left = true
	m.state.ProjectFilter = "alpha"
	if m.activityAnimating() {
		t.Fatal("filtered background thread keeps clock running")
	}
	m.state.ProjectFilter = ""
	m.snapshot.Threads[1].State = "idle"
	if m.activityAnimating() {
		t.Fatal("finished background work keeps clock running")
	}
	m.snapshot.Threads[1].State = "running"
	m.connected = false
	if m.activityAnimating() {
		t.Fatal("disconnected background work animates")
	}
	m.connected = true
	m.showMenu("Menu", []menuItem{{Label: "Test"}})
	if m.activityAnimating() {
		t.Fatal("obscured rows animate")
	}
	m.menu = nil
	for i := 0; i < 20; i++ {
		m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: fmt.Sprint(i), State: "idle"})
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 22})
	m.navScroll = 999
	if m.activityAnimating() {
		t.Fatal("offscreen working row keeps clock running")
	}
	// Bring it back into the visible navigation viewport.
	m.navScroll = 0
	if !m.activityAnimating() {
		t.Fatal("visible background row did not resume animation")
	}
}

func TestThreadIndicatorCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for status captures")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, phase := range []int{0, 6} {
			m := navigationModel()
			m.state.Light, m.activityPhase = light, phase
			m.snapshot.Threads = nil
			for _, state := range []string{"running", "waiting", "failed", "idle"} {
				m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: state, Title: map[string]string{"running": "Implement API client", "waiting": "Choose a database", "failed": "API request failed", "idle": "Review finished"}[state], State: state, Project: "Alpha", ProjectID: "alpha"})
			}
			m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: "closed", Title: "Previous review", State: "idle", Closed: true, Project: "Alpha", ProjectID: "alpha"})
			m.state.Active = "idle"
			m.hover = "thread:idle"
			m.configureInputs()
			name := fmt.Sprintf("160x50-light%t-thread-status-%d.ansi", light, phase)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
