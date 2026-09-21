package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestQueueSteerMouseAndKeyboardPreserveCapturedTargetAndDraft(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {48, 22}} {
		for _, keyboard := range []bool{false, true} {
			m := queueReviewModel(size[0], size[1])
			before := m.thread()
			h := controlHit(t, m.render(), "steer:queued-0")
			if h != controlHit(t, m.measure(), h.Key) {
				t.Fatal("steer paint and measurement disagree")
			}
			if keyboard {
				m.setFocus(h.Key)
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			} else {
				clickControl(m, h)
			}
			c := m.busy
			if c == nil || c.Kind != "queue.steer" || c.ThreadID != before.ID || c.TargetID != "queued-0" || c.ExpectedTurnID != before.TurnID || c.Revision != before.QueueRevision {
				t.Fatal("steer lost captured target", c)
			}
			if c.Text != "" || c.Settings != nil || c.Attachments != nil || m.prompt.Value() != "Keep my unsent draft" || len(m.thread().Queue) != 3 {
				t.Fatal("steer used composer data or removed the queue optimistically")
			}
			m.Update(commandMsg{command: *c, receipt: protocol.Receipt{State: "fixture-delivered"}, local: m.busyAction})
			if m.busy != nil || m.state.Pending != nil || m.prompt.Value() != "Keep my unsent draft" || !strings.Contains(m.notice.text, "steered") {
				t.Fatal("acceptance lost draft or feedback")
			}
		}
	}
}

func TestUnavailableSteerExplainsWithoutChangingWork(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Model)
	}{
		{"capability", func(m *Model) { m.snapshot.Capabilities = nil }},
		{"disconnected", func(m *Model) { m.connected = false }},
		{"resume", func(m *Model) { m.snapshot.Threads[0].NeedsResume = true }},
		{"idle", func(m *Model) { m.snapshot.Threads[0].State = "idle" }},
		{"settings", func(m *Model) { m.snapshot.Threads[0].Queue[0].Settings.Effort = "high" }},
		{"edit", func(m *Model) { m.activate(action{Kind: "edit", ID: "queued-0"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := queueReviewModel(160, 50)
			tc.change(m)
			draft := m.prompt.Value()
			before, _ := json.Marshal(m.snapshot)
			h := controlHit(t, m.render(), "steer:queued-0")
			m.hover = h.Key
			clickControl(m, h)
			after, _ := json.Marshal(m.snapshot)
			if m.busy != nil || string(before) != string(after) || m.prompt.Value() != draft || m.notice.text == "" {
				t.Fatal("unavailable steer changed work or omitted feedback")
			}
			if !strings.Contains(ansi.Strip(m.render().rows[m.height-1]), m.notice.text) {
				t.Fatal("hover help hid the unavailable explanation")
			}
			generation := m.notice.generation
			m.activate(h.Action)
			if m.notice.generation != generation {
				t.Fatal("repeated notice was not coalesced")
			}
			m.Update(noticeExpired(generation))
			if m.notice.text != "" || m.prompt.Value() != draft {
				t.Fatal("notice failed to expire independently of the draft")
			}
		})
	}
}

func TestSteerMenuKeepsOriginalTurnAndQueueRevision(t *testing.T) {
	m := queueReviewModel(160, 50)
	m.activate(action{Kind: "queue"})
	original := m.menu[0].Action
	if original.Kind != "steer" {
		t.Fatal("queue menu lacks Steer")
	}
	m.snapshot.Threads[0].TurnID = "replacement-turn"
	m.snapshot.Threads[0].QueueRevision++
	m.activate(action{Kind: "menu-select", Index: 0})
	if m.busy == nil || m.busy.ExpectedTurnID != original.Value || m.busy.Revision != original.Revision {
		t.Fatal("stale menu silently retargeted newer work")
	}
}

func TestSteerUncertainDeliveryPersistsIdentityAndVisibleRetry(t *testing.T) {
	m := queueReviewModel(160, 50)
	m.activate(m.steerAction("queued-0"))
	c, a := *m.busy, m.busyAction
	m.Update(commandMsg{command: c, local: a, err: errors.New("connection lost")})
	if m.busy == nil || m.state.Pending == nil || m.inFlight || m.steeringNotice() == "" || len(m.thread().Queue) != 3 {
		t.Fatal("uncertain steering lost its pending identity")
	}
	m.Update(noticeExpired(m.notice.generation))
	if !strings.Contains(ansi.Strip(m.render().rows[m.height-1]), "unconfirmed") {
		t.Fatal("uncertain delivery expired with the transient notice")
	}
	raw, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, raw)
	want, _ := json.Marshal(c)
	got, _ := json.Marshal(restored.busy)
	if string(got) != string(want) {
		t.Fatal("reconnect changed steering payload")
	}
	restored.activate(action{Kind: "retry"})
	if restored.busy.ID != c.ID || restored.busy.ExpectedTurnID != c.ExpectedTurnID {
		t.Fatal("retry created a new identity or target")
	}
	m.Update(commandMsg{command: c, local: a, err: &protocol.Error{Code: "stale_turn", Message: "active turn changed"}})
	if m.busy != nil || m.state.Pending != nil || len(m.thread().Queue) != 3 || m.prompt.Value() != "Keep my unsent draft" {
		t.Fatal("definite rejection discarded queue/draft or retained pending identity")
	}
}
