package tui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func stateReviewSnapshot(t *testing.T, m *Model, change func(*protocol.Snapshot)) snapshotMsg {
	t.Helper()
	raw, err := json.Marshal(m.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var s protocol.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.Revision++
	change(&s)
	return snapshotMsg(s)
}

func stateReviewRequests(t *testing.T, requests ...protocol.Request) *Model {
	t.Helper()
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) {
		s.Threads[0].Requests, s.Threads[0].Queue = requests, nil
	}))
	return m
}

func stateReviewApproval(id string) protocol.Request {
	return protocol.Request{ID: id, Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Title: id, Origin: "Fixture agent", Choices: []string{"Allow once", "Deny"}}
}

func stateReviewQuestion(id string) protocol.Request {
	return protocol.Request{ID: id, Kind: "question", Mode: "async", State: "pending", Revision: 1, Title: id, Origin: "Fixture agent", Questions: []protocol.Question{{ID: "q", Kind: "text", Text: id + "?"}}}
}

func TestStateReviewApprovalMenuStaysBoundToItsRequest(t *testing.T) {
	m := stateReviewRequests(t, stateReviewApproval("approval-a"), stateReviewApproval("approval-b"))
	m.activate(action{Kind: "approval-options"})
	if len(m.menu) != 2 || m.menu[0].Action.ID != "approval-a" || m.menu[0].Action.Revision != 1 {
		t.Fatalf("menu not bound to request identity: %+v", m.menu)
	}
	stale := m.menu[0].Action
	m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) { s.Threads[0].Requests[0].State = "resolved" }))
	if len(m.menu) != 0 {
		t.Fatal("request-scoped menu survived its request")
	}
	// A delayed pointer/keyboard activation of the old item must not reach B.
	m.activate(stale)
	if m.busy != nil {
		t.Fatalf("stale approval approved %s", m.busy.TargetID)
	}
	if r, _ := m.request(); r.ID != "approval-b" {
		t.Fatal("neighbor not selected")
	}
	m.activate(action{Kind: "approval-options"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.TargetID != "approval-b" || m.busy.Answers[0] != "Allow once" {
		t.Fatal("bound approval for the visible request was not sent")
	}
}

func TestStateReviewRequestMenusCloseOnRevisionChange(t *testing.T) {
	q := stateReviewQuestion("question-a")
	q.Questions[0] = protocol.Question{ID: "q", Kind: "single", Text: "Pick", Options: []string{"One", "Two"}}
	for _, kind := range []string{"answer-options", "question-tabs"} {
		m := stateReviewRequests(t, q)
		m.activate(action{Kind: kind})
		if len(m.menu) == 0 {
			t.Fatal(kind + " menu missing")
		}
		stale := m.menu[0].Action
		m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) { s.Threads[0].Requests[0].Revision = 2 }))
		if len(m.menu) != 0 {
			t.Fatal(kind + " menu survived a revision change")
		}
		m.activate(stale)
		current, _ := m.request()
		if m.busy != nil || len(m.questionDraft(current, 0).Choices) != 0 {
			t.Fatal("stale " + kind + " action changed the new revision")
		}
	}
}

func TestStateReviewTypingFollowsRequestIdentity(t *testing.T) {
	a, b, c := stateReviewQuestion("question-a"), stateReviewQuestion("question-b"), stateReviewQuestion("question-c")
	m := stateReviewRequests(t, a, b, c)
	m.activate(action{Kind: "request-index", ID: "question-b"})
	m.setFocus("answer")
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) { s.Threads[0].Requests[0].State = "resolved" }))
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if r, _ := m.request(); r.ID != "question-b" || m.focus != "answer" {
		t.Fatalf("selection moved to %s (focus %s)", r.ID, m.focus)
	}
	if m.questionDraft(b, 0).Text != "xy" || m.questionDraft(c, 0).Text != "" {
		t.Fatalf("typing redirected: b=%q c=%q", m.questionDraft(b, 0).Text, m.questionDraft(c, 0).Text)
	}
	// The selected request disappears: a neighbor shows, but typing cannot land in it.
	m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) { s.Threads[0].Requests[1].State = "resolved" }))
	m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	if r, _ := m.request(); r.ID != "question-c" || m.focus == "answer" || m.questionDraft(c, 0).Text != "" || m.busy != nil {
		t.Fatalf("neighbor received input or was submitted: focus=%s draft=%q", m.focus, m.questionDraft(c, 0).Text)
	}
	if m.questionDraft(b, 0).Text != "xy" {
		t.Fatal("resolved request draft lost")
	}
}

func TestStateReviewLegacySavedViewSelectsByIndex(t *testing.T) {
	m := stateReviewRequests(t, stateReviewQuestion("question-a"), stateReviewQuestion("question-b"))
	legacy := map[string]any{"Active": m.state.Active, "Threads": map[string]any{m.state.Active: map[string]any{"RequestIndex": 1, "Draft": "kept"}}}
	data, _ := json.Marshal(legacy)
	restored := New(nil, "test", m.snapshot, data)
	if r, _ := restored.request(); r.ID != "question-b" || restored.viewState().RequestID != "question-b" || restored.prompt.Value() != "kept" {
		t.Fatalf("legacy view not honored: %s", r.ID)
	}
	data, _ = json.Marshal(restored.state)
	if again := New(nil, "test", m.snapshot, data); again.viewState().RequestID != "question-b" {
		t.Fatal("identity not persisted")
	}
}
