package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func stateReviewEditing(t *testing.T) (*Model, protocol.Prompt) {
	t.Helper()
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.prompt.SetValue("A original draft")
	m.viewState().Draft = m.prompt.Value()
	m.state.Threads["thread-review"].Draft = "B draft"
	q := m.thread().Queue[0]
	m.activate(action{Kind: "edit", ID: q.ID})
	if m.state.Edit == nil {
		t.Fatal("edit did not start")
	}
	return m, q
}

func TestStateReviewQueuedEditBlocksEveryThreadSwitch(t *testing.T) {
	m, q := stateReviewEditing(t)
	m.Update(stateReviewSnapshot(t, m, func(s *protocol.Snapshot) {
		th := &s.Threads[1]
		th.Closed, th.State, th.Queue, th.Requests, th.Children = true, "idle", nil, nil, nil
	}))
	m.activate(action{Kind: "thread-reopen", ID: "thread-review"})
	if m.busy != nil || m.status != "Save or cancel the queued edit first" {
		t.Fatal("reopen of another thread allowed during a queued edit")
	}
	m.pendingThreadSelection = "thread-review"
	m.reconcileThreadMembership()
	m.selectThread("thread-review")
	m.pendingProjectSelection, m.pendingProjectDraft = m.thread().ProjectID, true
	m.reconcileThreadMembership()
	m.beginThreadDraft(m.thread().ProjectID)
	if m.state.Active != "thread-shell" || m.creatingThread() || m.prompt.Value() != q.Text || m.state.Edit == nil {
		t.Fatalf("switched away from the queued edit: active=%q draft=%q", m.state.Active, m.state.DraftProjectID)
	}
	m.activate(action{Kind: "cancel-edit"})
	m.selectThread("thread-review")
	if m.state.Active != "thread-review" || m.prompt.Value() != "B draft" || m.state.Threads["thread-shell"].Draft != "A original draft" {
		t.Fatal("drafts not restored after cancel")
	}
}

func TestStateReviewStaleEditOnAnotherThreadCannotDestroyDrafts(t *testing.T) {
	m, q := stateReviewEditing(t)
	// A view saved by an older version: the edit belongs to A while B is active.
	m.state.Active = "thread-review"
	data, _ := json.Marshal(m.state)
	for _, kind := range []string{"cancel-edit", "save-edit"} {
		restored := New(nil, "test", m.snapshot, data)
		if restored.prompt.Value() != "B draft" {
			t.Fatal("setup")
		}
		if kind == "cancel-edit" {
			restored.activate(action{Kind: kind})
		} else {
			c := protocol.Command{ID: "edit-1", Kind: "queue.edit", ThreadID: "thread-shell", TargetID: q.ID, Text: q.Text, Settings: &q.Settings}
			restored.busy = &c
			restored.Update(commandMsg{command: c, local: action{Kind: kind}})
		}
		if restored.state.Edit != nil || restored.prompt.Value() != "B draft" || restored.state.Threads["thread-review"].Draft != "B draft" || restored.state.Threads["thread-shell"].Draft != "A original draft" {
			t.Fatalf("%s: B=%q/%q A=%q", kind, restored.prompt.Value(), restored.state.Threads["thread-review"].Draft, restored.state.Threads["thread-shell"].Draft)
		}
	}
}

func TestStateReviewQueueEditSaveComparesTrimmedText(t *testing.T) {
	m, _ := stateReviewEditing(t)
	m.prompt.SetValue("edited text\n")
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "queue.edit" || m.busy.Text != "edited text" {
		t.Fatal("edit not saved")
	}
	m.Update(commandMsg{command: *m.busy, local: m.busyAction})
	if m.state.Edit != nil || m.prompt.Value() != "A original draft" || m.viewState().Draft != "A original draft" {
		t.Fatalf("accepted edit stayed in edit mode: %q", m.prompt.Value())
	}

	m, _ = stateReviewEditing(t)
	m.prompt.SetValue("edited text\n")
	m.activate(action{Kind: "send"})
	m.prompt.SetValue("edited text\nand more")
	m.viewState().Draft = m.prompt.Value()
	m.Update(commandMsg{command: *m.busy, local: m.busyAction})
	if m.state.Edit == nil || m.prompt.Value() != "edited text\nand more" {
		t.Fatal("newer text typed after Save was discarded")
	}
}

func TestStateReviewFailedPreDispatchSaveIsNotUncertain(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "keep me"})
	m.activate(action{Kind: "attach-kind", Value: "file"})
	m.activate(action{Kind: "send"})
	if m.busy == nil {
		t.Fatal("send blocked: " + m.sendBlocked() + " / " + m.status)
	}
	c := *m.busy
	stale := fmt.Errorf("view save not confirmed: %w", &protocol.Error{Code: "stale_view", Message: "changed"})
	_, _ = m.Update(commandMsg{command: c, err: stale, local: m.busyAction, saveFailed: true})
	if m.busy != nil || m.inFlight || m.state.Pending != nil || m.prompt.Value() != "keep me" || len(m.viewState().Attachments) != 1 {
		t.Fatal("unsent command left pending or lost its draft")
	}
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.ID == c.ID {
		t.Fatal("actions still blocked after a command that never left")
	}

	// Sent without a receipt: identity is retained for explicit Retry, including
	// when the Retry's own view save fails.
	c = *m.busy
	m.Update(commandMsg{command: c, err: errors.New("timeout"), local: m.busyAction})
	m.Update(commandMsg{command: c, err: stale, local: m.busyAction, saveFailed: true, retry: true})
	if m.busy == nil || m.busy.ID != c.ID || m.state.Pending == nil || m.inFlight {
		t.Fatal("uncertain command identity lost")
	}
	m.activate(action{Kind: "remove", ID: "x"})
	if m.busy.ID != c.ID {
		t.Fatal("new command replaced the uncertain one")
	}
	// A wrapped server rejection is still a definitive answer.
	m.Update(commandMsg{command: c, err: fmt.Errorf("wrapped: %w", &protocol.Error{Code: "invalid", Message: "no"}), local: m.busyAction, retry: true})
	if m.busy != nil {
		t.Fatal("wrapped protocol rejection treated as uncertain")
	}
}

func TestStateReviewHiddenComposerNeverSubmits(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "hidden draft"})
	generation := m.generation
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	if len(m.menu) == 0 {
		t.Fatal("Commands unreachable below the supported size")
	}
	safeKinds := []string{"quit", "suspend", "theme"}
	for _, item := range m.menu {
		if !slices.Contains(safeKinds, item.Action.Kind) {
			t.Fatalf("unsafe command offered while too small: %+v", item)
		}
	}
	m.menu = nil
	for _, a := range []action{{Kind: "send"}, {Kind: "interrupt"}, {Kind: "resume"}, {Kind: "retry"}, {Kind: "answer-submit"}, {Kind: "approve", Value: "Allow once"}, {Kind: "remove", ID: m.thread().Queue[0].ID}, {Kind: "steer", ID: m.thread().Queue[0].ID}, {Kind: "thread-delete-confirm", ID: m.state.Active}, {Kind: "edit", ID: m.thread().Queue[0].ID}} {
		m.activate(a)
		if m.busy != nil || m.state.Edit != nil {
			t.Fatalf("%s acted below the supported size", a.Kind)
		}
	}
	m.Update(tea.PasteMsg{Content: " pasted"})
	if m.prompt.Value() != "hidden draft" || m.viewState().Draft != "hidden draft" || m.generation != generation {
		t.Fatal("paste changed an invisible prompt")
	}
	if m.sendBlocked() == "" {
		t.Fatal("sendBlocked allows an invisible prompt")
	}

	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	if m.sendBlocked() != "" {
		t.Fatal("send stayed blocked after enlarging: " + m.sendBlocked())
	}
	m.activate(action{Kind: "app-settings"})
	if m.settingsPage == "" {
		t.Fatal("settings did not open")
	}
	for _, a := range []action{{Kind: "send"}, {Kind: "answer-submit"}, {Kind: "approve", Value: "Allow once"}, {Kind: "steer", ID: m.thread().Queue[0].ID}} {
		m.activate(a)
		if m.busy != nil {
			t.Fatalf("%s submitted behind settings", a.Kind)
		}
	}
	if m.prompt.Value() != "hidden draft" {
		t.Fatal("draft lost")
	}
}

func TestStateReviewPasteRespectsModalGates(t *testing.T) {
	m := stateReviewRequests(t, stateReviewQuestion("question-a"))
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "draft"})
	for _, focus := range []string{"prompt", "answer"} {
		m.setFocus(focus)
		m.activate(action{Kind: "commands"})
		m.Update(tea.PasteMsg{Content: "behind menu"})
		r, _ := m.request()
		if m.prompt.Value() != "draft" || m.answer.Value() != "" || m.questionDraft(r, 0).Text != "" || m.busy != nil || len(m.menu) == 0 {
			t.Fatalf("paste edited %s behind a menu", focus)
		}
		m.menu = nil
	}
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: " more\n"})
	if m.prompt.Value() != "draft more\n" || m.busy != nil {
		t.Fatalf("ordinary paste broken or submitted: %q", m.prompt.Value())
	}
	m.activate(action{Kind: "project-add"})
	m.Update(tea.PasteMsg{Content: "/tmp/project"})
	if m.projectInput.Value() != "/tmp/project" {
		t.Fatal("project dialog paste regressed")
	}
}
