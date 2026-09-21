package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func readyDraft(t *testing.T) (*Model, protocol.Command) {
	t.Helper()
	m := navigationModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-model"})
	m.Update(tea.PasteMsg{Content: "Initial prompt"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "thread.start" {
		t.Fatal("no first-Send command")
	}
	return m, *m.busy
}

func createdSnapshot(m *Model, c protocol.Command) (protocol.Snapshot, protocol.Receipt) {
	s := m.snapshot
	s.Revision++
	id := "thread-" + c.ID
	s.Threads = append(append([]protocol.Thread(nil), s.Threads...), protocol.Thread{ID: id, ProjectID: c.ProjectID, Project: "Alpha", Checkout: "/work/alpha", Agent: c.Agent, State: "running", Selected: *c.Settings, Effective: *c.Settings})
	return s, protocol.Receipt{TargetID: id, Revision: s.Revision, State: "accepted"}
}

func TestNewThreadDraftGatesSendAndRestoresPerProject(t *testing.T) {
	m := navigationModel()
	original, count := m.state.Active, len(m.snapshot.Threads)
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "Existing thread draft"})
	m.beginThreadDraft("alpha")
	if m.busy != nil || len(m.snapshot.Threads) != count || m.state.Threads[original].Draft != "Existing thread draft" {
		t.Fatal("New thread changed authoritative state or old draft")
	}
	m.Update(tea.PasteMsg{Content: "Unsent initial prompt"})
	m.activate(action{Kind: "send"})
	if m.busy != nil || !strings.Contains(m.sendBlocked(), "Choose") {
		t.Fatal("missing model allowed Send")
	}
	m.activate(action{Kind: "settings", Value: "model"})
	if len(m.menu) != 3 || !strings.Contains(m.menu[1].Label, "Codex") || !strings.Contains(m.menu[2].Label, "Claude") {
		t.Fatal("model availability unclear")
	}
	m.activate(action{Kind: "menu-close"})
	m.activate(action{Kind: "provider-unavailable", Value: "Claude"})
	if m.viewState().Settings.Model != "" {
		t.Fatal("unavailable provider selected")
	}
	m.activate(action{Kind: "setting-model"})
	m.activate(action{Kind: "setting", Value: "high"})
	m.activate(action{Kind: "setting-model"})
	if m.viewState().Settings.Effort != "high" {
		t.Fatal("reselecting model reset effort")
	}
	m.activate(action{Kind: "attach-kind", Value: "file"})
	m.beginThreadDraft("beta")
	m.Update(tea.PasteMsg{Content: "Beta draft"})
	m.beginThreadDraft("alpha")
	if m.prompt.Value() != "Unsent initial prompt" || m.composerSelection().Effort != "high" || len(m.viewState().Attachments) != 1 {
		t.Fatal("project draft lost")
	}
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if !restored.creatingThread() || restored.prompt.Value() != m.prompt.Value() || restored.busy != nil || restored.state.DraftThreads["beta"].Draft != "Beta draft" {
		t.Fatal("relaunch submitted or lost creation drafts")
	}
	restored.activate(action{Kind: "send"})
	c := restored.busy
	if c == nil || c.Kind != "thread.start" || c.ThreadID != "" || c.ProjectID != "alpha" || c.Settings.Effort != "high" || len(c.Attachments) != 1 || c.Attachments[0].Content == "" {
		t.Fatal("first Send did not capture valid configuration/context")
	}
}

func TestFirstSendReceiptSnapshotOrderPreservesNewTyping(t *testing.T) {
	for _, receiptFirst := range []bool{false, true} {
		for _, navigate := range []bool{false, true} {
			t.Run(fmt.Sprintf("receiptFirst=%v/navigate=%v", receiptFirst, navigate), func(t *testing.T) {
				m, c := readyDraft(t)
				m.Update(tea.PasteMsg{Content: " and later typing"})
				want := m.prompt.Value()
				if navigate {
					m.selectThread("thread-review")
				}
				s, r := createdSnapshot(m, c)
				receipt := commandMsg{command: c, receipt: r, local: action{Kind: "send"}}
				if receiptFirst {
					m.Update(receipt)
					data, _ := json.Marshal(m.state)
					m = New(nil, "test", m.snapshot, data)
					if m.state.StartedDraft == nil {
						t.Fatal("receipt lost before snapshot/relaunch")
					}
					m.Update(snapshotMsg(s))
				} else {
					m.Update(snapshotMsg(s))
					m.Update(receipt)
				}
				if m.state.StartedDraft != nil || m.state.Threads[r.TargetID].Draft != want {
					t.Fatal("accepted thread lost newer draft")
				}
				if navigate && m.state.Active != "thread-review" {
					t.Fatal("receipt stole navigation")
				}
				if !navigate && (m.state.Active != r.TargetID || m.prompt.Value() != want || m.creatingThread()) {
					t.Fatal("creation draft not transferred")
				}
				m.Update(receipt)
				if m.state.Threads[r.TargetID].Draft != want {
					t.Fatal("duplicate receipt repeated effects")
				}
			})
		}
	}
}

func TestFirstSendClearsOnlyAcceptedTextAndRetainsUncertainIdentity(t *testing.T) {
	m, c := readyDraft(t)
	m.Update(commandMsg{command: c, err: errors.New("connection lost")})
	if m.busy == nil || m.busy.ID != c.ID || m.prompt.Value() != c.Text {
		t.Fatal("uncertain Send lost retry identity/input")
	}
	data, _ := json.Marshal(m.state)
	m = New(nil, "test", m.snapshot, data)
	if m.busy == nil || m.busy.ID != c.ID || m.inFlight {
		t.Fatal("relaunch must retain identity without resubmitting")
	}
	s, r := createdSnapshot(m, c)
	m.Update(snapshotMsg(s))
	m.Update(commandMsg{command: c, receipt: r, local: action{Kind: "send"}})
	if m.prompt.Value() != "" || m.state.Threads[r.TargetID].Draft != "" || m.busy != nil {
		t.Fatal("accepted initial text not cleared")
	}
}

func TestReceiptPreservesAlreadyOpenedTargetAndOtherCreation(t *testing.T) {
	m, c := readyDraft(t)
	m.Update(tea.PasteMsg{Content: " later source text"})
	source := m.prompt.Value()
	s, r := createdSnapshot(m, c)
	m.Update(snapshotMsg(s))
	m.selectThread(r.TargetID)
	m.Update(tea.PasteMsg{Content: "new target text"})
	m.viewState().Host.Open("files", "Files")
	m.Update(commandMsg{command: c, receipt: r, local: action{Kind: "send"}})
	if m.prompt.Value() != "new target text" || m.viewState().Draft != "new target text" || len(m.viewState().Host.Tabs) != 1 || m.state.DraftThreads["alpha"].Draft != source {
		t.Fatal("late receipt overwrote newer target/source input")
	}

	m, c = readyDraft(t)
	s, r = createdSnapshot(m, c)
	m.Update(commandMsg{command: c, receipt: r, local: action{Kind: "send"}})
	m.activate(action{Kind: "setting", Value: "high"})
	if !m.configurationLocked() || m.viewState().Settings != *c.Settings {
		t.Fatal("acknowledged creation unlocked before its running state arrived")
	}
	m.beginThreadDraft("beta")
	m.activate(action{Kind: "setting-model"})
	m.Update(tea.PasteMsg{Content: "second project"})
	m.activate(action{Kind: "send"})
	if m.busy != nil || m.state.StartedDraft.ThreadID != r.TargetID {
		t.Fatal("second creation replaced unreconciled handoff")
	}
	m.Update(snapshotMsg(s))
	if m.state.DraftProjectID != "beta" || m.prompt.Value() != "second project" {
		t.Fatal("first receipt stole second draft")
	}
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.ProjectID != "beta" {
		t.Fatal("reconciled receipt still blocks creation")
	}
}

func TestActiveSettingsLockAndQueuedEditPreserveCapturedSettings(t *testing.T) {
	m := testModel()
	m.setFocus("prompt")
	m.viewState().Settings.Effort = "high"
	effective := m.thread().Effective
	for _, a := range []action{{Kind: "settings", Value: "model"}, {Kind: "settings", Value: "effort"}, {Kind: "setting", Value: "low"}, {Kind: "setting-model"}} {
		m.activate(a)
		if len(m.menu) != 0 || m.viewState().Settings.Effort != "high" {
			t.Fatal("active settings changed")
		}
	}
	m.Update(tea.PasteMsg{Content: "queue with running values"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || *m.busy.Settings != effective {
		t.Fatal("ordinary active Send didn't capture effective values")
	}
	m.busy, m.state.Pending = nil, nil
	m.snapshot.Threads[0].Queue[0].Settings.Effort = "low"
	q := m.thread().Queue[0]
	m.activate(action{Kind: "edit", ID: q.ID})
	m.Update(tea.PasteMsg{Content: " amended"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "queue.edit" || *m.busy.Settings != q.Settings {
		t.Fatal("text edit rewrote captured queued settings")
	}
	c := *m.busy
	m.Update(commandMsg{command: c, local: action{Kind: "save-edit"}})
	if m.state.Edit != nil || m.viewState().Settings.Effort != "high" {
		t.Fatal("accepted queue edit failed to restore idle preference")
	}
	m.snapshot.Threads[0].State = "idle"
	m.activate(action{Kind: "settings", Value: "model"})
	if len(m.menu) != 3 {
		t.Fatal("idle model picker unavailable")
	}
	m.activate(action{Kind: "menu-close"})
	m.activate(action{Kind: "setting", Value: "low"})
	if m.viewState().Settings.Effort != "low" {
		t.Fatal("idle settings still locked")
	}
}

func TestClosedComposerReopenAndSendAreSeparate(t *testing.T) {
	for _, send := range []bool{false, true} {
		m := navigationModel()
		th := &m.snapshot.Threads[1]
		th.Closed, th.State = true, "idle"
		th.Queue, th.Requests, th.Children = nil, nil, nil
		m.selectThread(th.ID)
		m.Update(tea.PasteMsg{Content: "reopen message"})
		f := m.render()
		if !strings.Contains(ansi.Strip(strings.Join(f.rows, "\n")), "This thread is closed") || !hasControl(f, "prompt") || m.busy != nil {
			t.Fatal("closed view reopened or lacks composer/banner")
		}
		if send {
			m.activate(action{Kind: "send"})
		} else {
			clickControl(m, controlHit(t, f, "thread-reopen"))
		}
		want := "thread.reopen"
		if send {
			want = "prompt.reopen-send"
		}
		if m.busy == nil || m.busy.Kind != want || m.busy.Revision != th.LifecycleRevision {
			t.Fatal("lost explicit operation/revision")
		}
		if send && m.busy.Text != "reopen message" || !send && m.busy.Text != "" {
			t.Fatal("reopen and Send conflated")
		}
		c := *m.busy
		m.Update(commandMsg{command: c, err: &protocol.Error{Code: "stale_thread", Message: "Thread changed"}})
		if !m.thread().Closed || m.prompt.Value() != "reopen message" {
			t.Fatal("rejected reopen lost local state")
		}
	}
}

func TestDraftClosedCheckoutAndModalGeometryAndCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, width := range []int{40, 47, 80, 160} {
		for _, light := range []bool{false, true} {
			for _, mode := range []string{"draft", "closed", "modal"} {
				m := navigationModel()
				m.state.Light = light
				height := 30
				if width < 80 {
					height = 22
				}
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				if mode == "draft" {
					m.beginThreadDraft("alpha")
				} else {
					th := &m.snapshot.Threads[0]
					th.State, th.Closed = "idle", true
					th.Queue, th.Requests, th.Children = nil, nil, nil
				}
				m.configureInputs()
				key, _, _ := m.checkoutTarget()
				m.checkoutKey = key
				m.Update(checkoutMsg{key: key, info: protocol.WorkspaceInfo{Path: "/work/alpha", Kind: "checkout", State: "branch", Branch: "feature/界-long-branch"}})
				if mode == "modal" {
					m.activate(action{Kind: "settings", Value: "model"})
				}
				f := m.render()
				if m.promptRows != 2 {
					t.Fatal("default composer not two editable rows")
				}
				for y, row := range f.rows {
					if ansi.StringWidth(row) != width {
						t.Fatalf("%d/%s row%d overflow", width, mode, y)
					}
				}
				for _, h := range f.hits {
					if h.Rect.X < 0 || h.Rect.X+h.Rect.W > width || h.Rect.Y < 0 || h.Rect.Y+h.Rect.H > height-1 {
						t.Fatalf("%d/%s hit overflow: %+v", width, mode, h)
					}
				}
				if mode != "modal" {
					for _, key := range []string{"prompt", "send", "checkout-info", "checkout-branch"} {
						if !hasControl(f, key) {
							t.Fatal("missing", width, mode, key)
						}
					}
				}
				if dir != "" {
					name := fmt.Sprintf("%dx%d-light%t-%s.ansi", width, height, light, mode)
					if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(f.rows, "\n")), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestCheckoutIgnoresStaleSelectionAndShowsHonestStates(t *testing.T) {
	m := navigationModel()
	key, _, _ := m.checkoutTarget()
	m.checkoutKey = key
	m.Update(checkoutMsg{key: key, info: protocol.WorkspaceInfo{Path: "/work/alpha", Kind: "checkout", State: "branch", Branch: "main"}})
	_, right := m.checkoutLabels()
	if right != "main" {
		t.Fatal("branch observation not displayed")
	}
	m.selectThread("thread-review")
	m.Update(checkoutMsg{key: key, info: protocol.WorkspaceInfo{Path: "/work/alpha", Kind: "checkout", State: "branch", Branch: "stale"}})
	_, right = m.checkoutLabels()
	if right == "main" || right == "stale" {
		t.Fatal("old checkout shown for new selection")
	}
	for state, want := range map[string]string{"unborn": "main (unborn)", "detached": "Detached · 1234abc", "non-git": "Not a Git checkout", "unavailable": "Branch unavailable"} {
		key, _, _ = m.checkoutTarget()
		m.checkoutKey = key
		m.Update(checkoutMsg{key: key, info: protocol.WorkspaceInfo{Path: "/work/beta", Kind: "worktree", State: state, Branch: "main", Revision: "1234abc"}})
		left, right := m.checkoutLabels()
		if !strings.HasPrefix(left, "Worktree") || right != want {
			t.Fatal(state, left, right)
		}
	}
}
