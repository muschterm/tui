package tui

import (
	"context"
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

const testHead = "0123456789abcdef0123456789abcdef01234567"

type fakeWorktrees struct {
	removal protocol.WorktreeRemoval
	prune   protocol.WorktreePrune
	calls   []string
}

func (f *fakeWorktrees) WorktreeRemoval(_ context.Context, id string) (protocol.WorktreeRemoval, error) {
	f.calls = append(f.calls, "removal:"+id)
	r := f.removal
	r.ID = id
	return r, nil
}

func (f *fakeWorktrees) WorktreePrune(_ context.Context, projectID string) (protocol.WorktreePrune, error) {
	f.calls = append(f.calls, "prune:"+projectID)
	return f.prune, nil
}

func worktreeModel(t *testing.T) (*Model, *fakeGit, *fakeWorktrees) {
	t.Helper()
	m := navigationModel()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "thread-start", "worktree-create", "worktree-manage", "project-settings")
	git := &fakeGit{status: protocol.GitStatus{Workspace: protocol.WorkspaceInfo{State: "branch", Branch: "main"}, Branch: "main", HeadOid: testHead}}
	wt := &fakeWorktrees{}
	m.gitReads, m.worktreeReads = git, wt
	m.connected = true
	return m, git, wt
}

// run executes a command and feeds its message (or a batch's messages) back.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			run(m, c)
		}
		return
	}
	switch msg.(type) {
	case draftStartMsg, worktreeRemovalMsg, worktreePruneMsg:
		m.Update(msg)
	}
}

func worktreeDraft(t *testing.T, m *Model) {
	t.Helper()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-model"})
	m.Update(tea.PasteMsg{Content: "Initial prompt"})
	run(m, m.activate(action{Kind: "worktree-draft-mode", Value: workspaceWorktree}))
	run(m, m.nextDraftStartInspection())
}

func screen(m *Model) string { return ansi.Strip(strings.Join(m.render().rows, "\n")) }

func TestDraftWorkspaceRowDefaultsFollowProjectAndPersist(t *testing.T) {
	m, _, _ := worktreeModel(t)
	m.snapshot.Projects[0].WorkspaceDefault = "worktree"
	m.beginThreadDraft("alpha")
	if m.draftWorkspace() != workspaceWorktree || m.checkoutRows() != 2 {
		t.Fatal("draft ignored the project's worktree default")
	}
	f := m.render()
	for _, key := range []string{"workspace-checkout", "workspace-worktree", "worktree-start", "worktree-branch"} {
		controlHit(t, f, key)
	}
	clickControl(m, controlHit(t, f, "workspace-checkout"))
	if m.draftWorkspace() != workspaceCheckout || m.viewState().Workspace != workspaceCheckout || m.checkoutRows() != 1 {
		t.Fatal("click did not choose checkout")
	}
	if hasControl(m.render(), "worktree-branch") {
		t.Fatal("branch field shown for checkout")
	}
	// Keyboard: focus the worktree segment and activate it with Enter.
	m.setFocus("workspace-worktree")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.draftWorkspace() != workspaceWorktree {
		t.Fatal("Enter on the focused segment did not choose worktree")
	}
	m.viewState().WorktreeBranch = "feature/x"
	m.beginThreadDraft("beta")
	if m.draftWorkspace() != workspaceCheckout || m.viewState().WorktreeBranch != "" {
		t.Fatal("draft choice leaked into another project")
	}
	m.beginThreadDraft("alpha")
	if m.viewState().WorktreeBranch != "feature/x" || m.draftWorkspace() != workspaceWorktree {
		t.Fatal("per-project draft lost its workspace")
	}
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if restored.viewState().WorktreeBranch != "feature/x" || restored.viewState().Workspace != workspaceWorktree {
		t.Fatal("workspace choice not saved with the client-local draft")
	}
}

func TestDraftWorkspaceCapabilityGating(t *testing.T) {
	m := navigationModel()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "thread-start")
	m.beginThreadDraft("alpha")
	if m.draftWorkspaceOffered() || hasControl(m.render(), "workspace-worktree") {
		t.Fatal("worktree choice offered without worktree-create")
	}
	m.activate(action{Kind: "setting-model"})
	m.Update(tea.PasteMsg{Content: "hi"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Workspace != nil {
		t.Fatal("old server received a workspace request")
	}
	m2 := navigationModel()
	m2.snapshot.Capabilities = append(m2.snapshot.Capabilities, "thread-start")
	m2.snapshot.AppSettings.WorkspaceDefault = "worktree"
	m2.beginThreadDraft("alpha")
	m2.activate(action{Kind: "setting-model"})
	m2.Update(tea.PasteMsg{Content: "hi"})
	m2.activate(action{Kind: "send"})
	if m2.busy != nil || !strings.Contains(m2.sendBlocked(), "cannot create") || m2.prompt.Value() != "hi" {
		t.Fatal("worktree default without capability was not refused honestly:", m2.sendBlocked())
	}
}

func TestDraftWorktreeSendPayloadAndRefusals(t *testing.T) {
	m, git, _ := worktreeModel(t)
	worktreeDraft(t, m)
	if git.count("status:alpha") != 1 {
		t.Fatal("start commit not observed", git.calls)
	}
	if !strings.Contains(screen(m), "Start · main @ 0123456") {
		t.Fatal("start not shown:\n" + screen(m))
	}
	m.activate(action{Kind: "send"})
	if m.busy != nil || !strings.Contains(m.status, "branch") || m.prompt.Value() != "Initial prompt" {
		t.Fatal("Send without branch was not refused inline, keeping the draft:", m.status)
	}
	m.activate(action{Kind: "menu-close"})
	// Invalid names are refused in the dialog and never saved.
	clickControl(m, controlHit(t, m.render(), "worktree-branch"))
	if m.projectMode != "worktree-branch" {
		t.Fatal("branch field did not open its dialog")
	}
	m.Update(tea.PasteMsg{Content: "bad name"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.projectError == "" || m.viewState().WorktreeBranch != "" {
		t.Fatal("invalid branch accepted")
	}
	m.projectInput.SetValue("feature/wt")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.projectMode != "" || m.viewState().WorktreeBranch != "feature/wt" {
		t.Fatal("valid branch not saved", m.projectError)
	}
	m.activate(action{Kind: "send"})
	c := m.busy
	if c == nil || c.Kind != "thread.start" || c.Workspace == nil || *c.Workspace != (protocol.WorkspaceRequest{Mode: "worktree", StartOid: testHead, Branch: "feature/wt"}) {
		t.Fatalf("wrong payload %+v", c)
	}
	// A failed receipt keeps the draft and the branch, and shows the reason.
	m.Update(commandMsg{command: *c, receipt: protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "worktree_failed", Message: "Git could not create the worktree"}}, local: action{Kind: "send"}})
	if m.busy != nil || m.prompt.Value() != "Initial prompt" || m.viewState().WorktreeBranch != "feature/wt" || m.menuTitle != "Cannot send message" {
		t.Fatal("failed receipt lost the draft or stayed busy")
	}
	m.activate(action{Kind: "menu-close"})
	// The failed start keeps its identity until the user discards it.
	m.activate(action{Kind: "send"})
	if m.busy != nil || m.viewState().PendingStart == nil || m.viewState().PendingStart.State != pendingFailed {
		t.Fatal("failed start silently replaced by a new command")
	}
	m.activate(action{Kind: "menu-close"})
	clickControl(m, controlHit(t, m.render(), "worktree-start-discard"))
	if m.viewState().PendingStart != nil || !strings.Contains(m.sendBlocked(), "may already exist") {
		t.Fatal("Discard did not release the start and guard the branch")
	}
	m.viewState().WorktreeBranch = "feature/wt-2"
	m.activate(action{Kind: "send"})
	c = m.busy
	// A running receipt releases the global command; the draft follows it.
	_, cmd := m.Update(commandMsg{command: *c, receipt: protocol.Receipt{ID: c.ID, State: "running"}, local: action{Kind: "send"}})
	ps := m.viewState().PendingStart
	if m.busy != nil || ps == nil || ps.Command.ID != c.ID || ps.State != pendingRunning || cmd == nil || !m.configurationLocked() {
		t.Fatal("running receipt not followed from the draft")
	}
	// A definitive rejection on a re-ask clears it, keeps the draft and
	// marks the branch as taken.
	m.Update(worktreeStartMsg{command: *c, err: &protocol.Error{Code: "branch_exists", Message: "branch exists"}})
	if m.viewState().PendingStart != nil || m.prompt.Value() != "Initial prompt" || !strings.Contains(m.sendBlocked(), "may already exist") {
		t.Fatal("rejection lost the draft or reused the branch", m.sendBlocked())
	}
	m.activate(action{Kind: "menu-close"})
	// Checkout is sent explicitly, so a worktree default never applies.
	m.activate(action{Kind: "worktree-draft-mode", Value: workspaceCheckout})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Workspace == nil || m.busy.Workspace.Mode != "checkout" || m.busy.Workspace.Branch != "" {
		t.Fatalf("checkout choice payload %+v", m.busy)
	}
}

func TestExistingThreadSendNeverCarriesWorkspace(t *testing.T) {
	m, _, _ := worktreeModel(t)
	m.snapshot.Threads[0].WorktreeID = "wt1"
	m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: protocol.WorktreeMissing}}
	m.selectThread(m.snapshot.Threads[0].ID)
	if m.draftWorkspaceOffered() || hasControl(m.render(), "workspace-worktree") {
		t.Fatal("existing thread offered a workspace choice")
	}
	m.prompt.SetValue("follow up")
	m.viewState().Draft = "follow up"
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Workspace != nil {
		t.Fatal("existing thread Send missing or carried a workspace", m.status)
	}
	{
		c := *m.busy
		m.Update(commandMsg{command: c, err: &protocol.Error{Code: "workspace_unavailable", Message: "worktree missing"}, local: action{Kind: "send"}})
		if m.prompt.Value() != "follow up" || !strings.Contains(m.status, "relocate or forget") {
			t.Fatal("workspace_unavailable lost the prompt or recovery pointer:", m.status)
		}
	}
}

func TestWorktreeThreadCheckoutRowAndRecoveryMenu(t *testing.T) {
	for _, tc := range []struct {
		state                    string
		relocate, forget, remove bool
	}{
		{protocol.WorktreePresent, false, false, true},
		{protocol.WorktreeMissing, false, true, false},
		{protocol.WorktreeMoved, true, true, false},
		{protocol.WorktreeUnregistered, false, true, false},
		{protocol.WorktreeRemoved, false, true, false},
		{protocol.WorktreeUnattached, false, false, true},
		{protocol.WorktreeCreating, false, false, false},
	} {
		m, _, _ := worktreeModel(t)
		id := m.snapshot.Threads[0].ID
		m.snapshot.Threads[0].WorktreeID = "wt1"
		m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: tc.state, Detail: "detail text", MovedTo: "/elsewhere"}}
		m.selectThread(id)
		left, right := m.checkoutLabels()
		if left != "Worktree · feature/a" {
			t.Fatal(tc.state, "left", left)
		}
		if tc.state != protocol.WorktreePresent && !strings.Contains(right, "detail text") {
			t.Fatal(tc.state, "state detail missing:", right)
		}
		clickControl(m, controlHit(t, m.render(), "checkout-info"))
		has := map[string]bool{}
		for _, item := range m.menu {
			has[item.Action.Kind] = true
		}
		if has["worktree-relocate"] != tc.relocate || has["worktree-forget"] != tc.forget || has["worktree-remove"] != tc.remove {
			t.Fatalf("%s: wrong actions %v", tc.state, has)
		}
		m.activate(action{Kind: "menu-close"})
		m.snapshot.Capabilities = []string{"thread-start", "workspace-info"}
		m.activate(action{Kind: "checkout-info"})
		for _, item := range m.menu {
			if strings.HasPrefix(item.Action.Kind, "worktree-") {
				t.Fatal("recovery offered without worktree-manage")
			}
		}
	}
}

func TestWorktreeRemoveConfirmationFlow(t *testing.T) {
	m, _, wt := worktreeModel(t)
	m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: protocol.WorktreePresent}}
	wt.removal = protocol.WorktreeRemoval{Path: "/home/wt", Branch: "feature/a", Blockers: []string{"thread Build is not Closed"}, Fingerprint: "f1"}
	run(m, m.activate(action{Kind: "worktree-remove", ID: "wt1"}))
	if m.menu[0].Label != "Cancel" {
		t.Fatal("Cancel is not first")
	}
	var notes []string
	for _, item := range m.menu {
		if item.Action.Kind == "worktree-remove-confirm" {
			t.Fatal("confirm offered despite blockers")
		}
		notes = append(notes, item.Note)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "thread Build is not Closed") || !strings.Contains(joined, "Branch feature/a is kept") {
		t.Fatal("blockers or branch note missing:\n" + joined)
	}
	wt.removal = protocol.WorktreeRemoval{Path: "/home/wt", Branch: "feature/a", Ignored: 3, IgnoredSample: []string{"node_modules/", ".env"}, IgnoredIncomplete: true, Fingerprint: "f2"}
	run(m, m.activate(action{Kind: "worktree-remove", ID: "wt1"}))
	var confirm action
	notes = nil
	for _, item := range m.menu {
		if item.Action.Kind == "worktree-remove-confirm" {
			confirm = item.Action
		}
		notes = append(notes, item.Note)
	}
	joined = strings.Join(notes, "\n")
	for _, want := range []string{"3 ignored entries", "node_modules/", "…and 1 more", "incomplete"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in\n%s", want, joined)
		}
	}
	if confirm.Value != "f2" {
		t.Fatal("confirm does not carry the fingerprint")
	}
	idx := -1
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-remove-confirm" {
			idx = i
		}
	}
	m.activate(action{Kind: "menu-select", Index: idx})
	c := m.busy
	if c == nil || c.Kind != "worktree.remove" || c.Worktree == nil || c.Worktree.ID != "wt1" || c.Worktree.Confirm != "f2" {
		t.Fatalf("wrong remove command %+v", c)
	}
	// A stale confirmation previews again instead of retrying.
	_, cmd := m.Update(commandMsg{command: *c, err: &protocol.Error{Code: "stale_confirmation", Message: "changed"}, local: confirm})
	before := len(wt.calls)
	run(m, cmd)
	if m.busy != nil || len(wt.calls) != before+1 || wt.calls[len(wt.calls)-1] != "removal:wt1" || m.menuTitle != "Remove worktree · feature/a" {
		t.Fatal("stale confirmation did not re-preview", wt.calls, m.menuTitle)
	}
}

func TestProjectWorktreesSettingsSectionAndPrune(t *testing.T) {
	m, _, wt := worktreeModel(t)
	m.snapshot.Threads[0].WorktreeID = "wt1"
	m.snapshot.Worktrees = []protocol.ManagedWorktree{
		{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: protocol.WorktreePresent},
		{ID: "wt2", ProjectID: "alpha", Branch: "feature/b", State: protocol.WorktreeMissing, Detail: "directory gone"},
		{ID: "wt3", ProjectID: "alpha", Branch: "old", State: protocol.WorktreeRemoved},
		{ID: "wt4", ProjectID: "beta", Branch: "other", State: protocol.WorktreePresent},
	}
	wt.prune = protocol.WorktreePrune{ProjectID: "alpha", Entries: []string{"/gone/one"}, Fingerprint: "p1"}
	m.openSidebarSettings("general", "alpha")
	run(m, m.nextPruneInspection())
	text := screen(m)
	for _, want := range []string{"WORKTREES", "feature/a", "feature/b", "directory gone", "Prune 1 stale registration"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "other") || strings.Contains(text, "unavailable in this build") {
		t.Fatal("other project's worktree or stale limitation shown")
	}
	f := m.render()
	clickControl(m, controlHit(t, f, "sidebar-setting:worktree:wt2"))
	if !strings.HasPrefix(m.menuTitle, "Worktree · feature/b") {
		t.Fatal("row did not open its menu", m.menuTitle)
	}
	var forget int = -1
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-forget" {
			forget = i
		}
	}
	m.activate(action{Kind: "menu-select", Index: forget})
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-forget-confirm" {
			m.activate(action{Kind: "menu-select", Index: i})
		}
	}
	if m.busy == nil || m.busy.Kind != "worktree.forget" || m.busy.Worktree.ID != "wt2" {
		t.Fatalf("forget not sent %+v", m.busy)
	}
	m.Update(commandMsg{command: *m.busy, receipt: protocol.Receipt{State: "accepted"}})
	if m.busy != nil || m.pruneKey != "" {
		t.Fatal("accepted worktree command not settled")
	}
	run(m, m.nextPruneInspection())
	f = m.render()
	m.setFocus("sidebar-setting:worktree-prune")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	run(m, cmd)
	var confirm int = -1
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-prune-confirm" {
			confirm = i
		}
	}
	if m.menu[0].Label != "Cancel" || confirm < 0 {
		t.Fatal("prune confirmation malformed", m.menu)
	}
	m.activate(action{Kind: "menu-select", Index: confirm})
	if m.busy == nil || m.busy.Kind != "worktree.prune" || m.busy.ProjectID != "alpha" || m.busy.Worktree.Confirm != "p1" {
		t.Fatalf("prune not sent %+v", m.busy)
	}
	_ = f
}

func TestBranchNameProblem(t *testing.T) {
	for name, ok := range map[string]bool{"refs/heads/x": false, strings.Repeat("a", 201): false, "feature/x": true, "a.b-c_d": true, "": false, "a b": false, "-x": false, "a..b": false, "a/": false, "x.lock": false, "a/.b": false, "a~1": false, "@": false} {
		if (branchNameProblem(name) == "") != ok {
			t.Fatalf("%q: got %q", name, branchNameProblem(name))
		}
	}
}

func TestWorktreeCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{160, 80, 48} {
		for _, light := range []bool{false, true} {
			m, _, _ := worktreeModel(t)
			m.state.Light = light
			worktreeDraft(t, m)
			m.viewState().WorktreeBranch = "feature/worktree-review"
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			m.configureInputs()
			write := func(name string) {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("worktree-%s-%d-light%t.ansi", name, width, light)), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("draft")
			m.snapshot.Worktrees = []protocol.ManagedWorktree{
				{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: protocol.WorktreePresent, Path: "/home/user/.tui/worktrees/alpha/feature-a-1234abcd"},
				{ID: "wt2", ProjectID: "alpha", Branch: "feature/b", State: protocol.WorktreeMissing, Detail: "directory gone"},
			}
			m.snapshot.Threads[0].WorktreeID = "wt1"
			m.prunePreview = map[string]protocol.WorktreePrune{"alpha": {Entries: []string{"/gone"}}}
			m.pruneKey = "alpha"
			m.openSidebarSettings("general", "alpha")
			m.pruneKey = "alpha"
			write("settings")
		}
	}
}

func namedDraft(t *testing.T, m *Model) {
	t.Helper()
	worktreeDraft(t, m)
	m.viewState().WorktreeBranch = "feature/wt"
}

func TestWorktreeStartReobservedOnReentry(t *testing.T) {
	m, git, _ := worktreeModel(t)
	namedDraft(t, m)
	m.selectThread(m.snapshot.Threads[0].ID)
	git.status.HeadOid = strings.Repeat("f", 40)
	m.beginThreadDraft("alpha")
	run(m, m.nextDraftStartInspection())
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Workspace.StartOid != strings.Repeat("f", 40) {
		t.Fatal("Send used a stale start observation")
	}
	// A stale in-flight read never replaces a newer one.
	m2, _, _ := worktreeModel(t)
	namedDraft(t, m2)
	old := m2.draftStart
	m2.activate(action{Kind: "checkout-refresh"})
	m2.Update(draftStartMsg{key: old.key, seq: old.seq, status: protocol.GitStatus{HeadOid: strings.Repeat("e", 40), Workspace: protocol.WorkspaceInfo{State: "branch"}}})
	if m2.draftStart.oid == strings.Repeat("e", 40) {
		t.Fatal("stale read accepted")
	}
	// Disconnected: no read, Send refused.
	m3, _, _ := worktreeModel(t)
	m3.connected = false
	m3.beginThreadDraft("alpha")
	m3.activate(action{Kind: "worktree-draft-mode", Value: workspaceWorktree})
	if m3.nextDraftStartInspection() != nil {
		t.Fatal("read while disconnected")
	}
}

func TestWorktreeOutcomeUnknownIsNotAcceptance(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "outcome_unknown", TargetID: "thread-" + c.ID, Revision: 1}, local: action{Kind: "send"}})
	if m.state.StartedDraft != nil || strings.Contains(m.status, "accepted") || m.viewState().WorktreeBranch != "feature/wt" || m.prompt.Value() != "Initial prompt" {
		t.Fatal("outcome_unknown treated as acceptance:", m.status)
	}
	if !strings.Contains(m.sendBlocked(), "Worktrees") || !hasControl(m.render(), "worktree-start-inspect") {
		t.Fatal("unknown outcome does not point at Worktrees")
	}
	m.activate(action{Kind: "worktree-start-discard"})
	if m.viewState().PendingStart != nil || !strings.Contains(m.sendBlocked(), "may already exist") {
		t.Fatal("discard did not guard the branch")
	}
}

func TestWorktreeFailedAttachKeepsIdentity(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt9", ProjectID: "alpha", Branch: "feature/wt", CommandID: c.ID, State: protocol.WorktreeUnattached}}
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "capacity", Message: "the worktree was created but its thread could not start (x); retry the request or remove the worktree"}}, local: action{Kind: "send"}})
	ps := m.viewState().PendingStart
	if ps == nil || ps.State != pendingRetryable || ps.Command.ID != c.ID {
		t.Fatal("retryable failure lost its identity")
	}
	m.activate(action{Kind: "menu-close"})
	m.activate(action{Kind: "send"})
	if m.busy != nil || m.viewState().PendingStart.Command.ID != c.ID || m.viewState().PendingStart.State != pendingRunning {
		t.Fatal("Send did not re-ask under the same identity")
	}
	// Attachment seen in the snapshot settles it as accepted.
	s := m.snapshot
	s.Revision++
	s.Threads = append(append([]protocol.Thread(nil), s.Threads...), protocol.Thread{ID: "thread-new", ProjectID: "alpha", Project: "Alpha", WorktreeID: "wt9", Agent: c.Agent, State: "running", Selected: *c.Settings, Effective: *c.Settings})
	m.Update(snapshotMsg(s))
	if m.creatingThread() || m.state.Active != "thread-new" {
		t.Fatal("attached thread not followed", m.state.Active, m.status)
	}
}

func TestWorktreePendingStartLocksAndSurvivesRelaunch(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "running"}, local: action{Kind: "send"}})
	m.activate(action{Kind: "worktree-draft-mode", Value: workspaceCheckout})
	m.activate(action{Kind: "worktree-branch"})
	if m.draftWorkspace() != workspaceWorktree || m.projectMode == "worktree-branch" || m.viewState().WorktreeBranch != "feature/wt" {
		t.Fatal("pending start's draft was editable")
	}
	if !strings.Contains(screen(m), "Creating worktree feature/wt") {
		t.Fatal("progress not shown:\n" + screen(m))
	}
	// Other projects stay usable: global commands are not held.
	m.beginThreadDraft("beta")
	m.activate(action{Kind: "worktree-draft-mode", Value: workspaceWorktree})
	if m.viewState().Workspace != workspaceWorktree || m.busy != nil {
		t.Fatal("another draft was blocked")
	}
	data, _ := json.Marshal(m.state)
	restored := New(nil, "test", m.snapshot, data)
	if ps := restored.state.DraftThreads["alpha"].PendingStart; ps == nil || ps.Command.ID != c.ID || restored.resumePendingStarts() == nil {
		t.Fatal("pending start not resumed after relaunch")
	}
}

func TestWorktreeExplicitPruneNotDroppedByQuiet(t *testing.T) {
	m, _, wt := worktreeModel(t)
	wt.prune = protocol.WorktreePrune{ProjectID: "alpha", Entries: []string{"/gone"}, Fingerprint: "p1"}
	m.openSidebarSettings("general", "alpha")
	explicit := m.previewWorktreePrune("alpha", false)
	quiet := m.nextPruneInspection()
	run(m, quiet)
	run(m, explicit)
	found := false
	for _, it := range m.menu {
		found = found || it.Action.Kind == "worktree-prune-confirm"
	}
	if !found {
		t.Fatal("explicit prune preview dropped")
	}
}

func TestWorktreeRowsNarrowKeepOid(t *testing.T) {
	for _, w := range []int{40, 43, 44, 50} {
		m, _, _ := worktreeModel(t)
		m.draftStart.branch = "功能/很长的分支名称-with-a-very-long-suffix"
		namedDraft(t, m)
		m.draftStart.branch = "功能/很长的分支名称-with-a-very-long-suffix"
		m.viewState().WorktreeBranch = "功能/很长的分支名称-with-a-very-long-suffix-xxxxxxxxxxxxxxxxxxxxx"
		m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		f := m.render()
		for i, row := range f.rows {
			if ansi.StringWidth(row) > w {
				t.Fatalf("w=%d row %d width %d", w, i, ansi.StringWidth(row))
			}
		}
		if !strings.Contains(screen(m), "@ 0123456") {
			t.Fatalf("w=%d short oid hidden:\n%s", w, screen(m))
		}
	}
}

func TestWorktreeWaitLineAndRelocateConfirm(t *testing.T) {
	m, _, _ := worktreeModel(t)
	th := &m.snapshot.Threads[0]
	th.WorktreeID, th.State, th.Requests, th.Children = "wt1", "idle", nil, nil
	th.Queue = []protocol.Prompt{{ID: "q1", Text: "queued"}}
	m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt1", ProjectID: "alpha", Branch: "feature/a", State: protocol.WorktreeMoved, MovedTo: "/new"}}
	m.selectThread(th.ID)
	if !strings.Contains(screen(m), "Waiting for worktree · Moved") {
		t.Fatal("no waiting reason:\n" + screen(m))
	}
	m.activate(action{Kind: "worktree-relocate", ID: "wt1"})
	if m.busy != nil || m.menu[0].Label != "Cancel" {
		t.Fatal("relocate sent without confirmation")
	}
	m.activate(action{Kind: "menu-select", Index: 1})
	if m.busy == nil || m.busy.Kind != "worktree.relocate" {
		t.Fatal("relocate not sent after confirmation")
	}
	if n := worktreeDirectories(protocol.Snapshot{Worktrees: []protocol.ManagedWorktree{{ProjectID: "p", State: protocol.WorktreeMissing}, {ProjectID: "p", State: protocol.WorktreeRemoved}, {ProjectID: "p", State: protocol.WorktreePresent}}}, "p"); n != 1 {
		t.Fatal("directory count", n)
	}
}

func TestWorktreeFailedAttachBeforeSnapshot(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	msg := "the worktree was created but its thread could not start (thread capacity reached); retry the request or remove the worktree"
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "capacity", Message: msg}}, local: action{Kind: "send"}})
	if ps := m.viewState().PendingStart; ps == nil || ps.State != pendingFailed || ps.Command.ID != c.ID {
		t.Fatal("failed receipt dropped the command identity")
	}
	m.activate(action{Kind: "menu-close"})
	if !hasControl(m.render(), "worktree-start-inspect") {
		t.Fatal("failure does not link to Worktrees")
	}
	s := m.snapshot
	s.Revision++
	s.Worktrees = []protocol.ManagedWorktree{{ID: "wt9", ProjectID: "alpha", Branch: "feature/wt", CommandID: c.ID, State: protocol.WorktreeUnattached}}
	m.Update(snapshotMsg(s))
	if ps := m.viewState().PendingStart; ps == nil || ps.State != pendingRetryable {
		t.Fatal("late snapshot did not make the start retryable")
	}
	m.activate(action{Kind: "menu-close"})
	m.activate(action{Kind: "send"})
	if m.busy != nil || m.viewState().PendingStart.Command.ID != c.ID || m.viewState().PendingStart.State != pendingRunning {
		t.Fatal("Send did not retry under the same identity")
	}
	// Two threads in the worktree: the start settles on the one it named.
	m.viewState().PendingStart.ThreadID = "thread-own"
	s = m.snapshot
	s.Revision++
	s.Threads = append(append([]protocol.Thread(nil), s.Threads...),
		protocol.Thread{ID: "thread-job", ProjectID: "alpha", WorktreeID: "wt9", Job: &protocol.ThreadJob{}, Selected: *c.Settings, Effective: *c.Settings},
		protocol.Thread{ID: "thread-own", ProjectID: "alpha", WorktreeID: "wt9", Agent: c.Agent, State: "running", Selected: *c.Settings, Effective: *c.Settings})
	m.Update(snapshotMsg(s))
	if m.state.Active != "thread-own" {
		t.Fatal("settled on the wrong thread:", m.state.Active)
	}
}

func TestWorktreeServerGoneForever(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	m.Update(commandMsg{command: c, err: errors.New("connection refused"), local: action{Kind: "send"}})
	for range 10 {
		m.Update(worktreeStartMsg{command: c, err: errors.New("connection refused")})
	}
	ps := m.viewState().PendingStart
	if ps == nil || ps.State != pendingRunning || !m.configurationLocked() {
		t.Fatal("unreachable server should keep following")
	}
	m.activate(action{Kind: "worktree-start-discard"})
	if m.viewState().PendingStart == nil {
		t.Fatal("Discard released a running start without confirmation")
	}
	clickControl(m, controlHit(t, m.render(), "worktree-start-stop"))
	if m.menu[0].Label != "Cancel" || m.viewState().PendingStart == nil {
		t.Fatal("Stop following lacks a Cancel-first confirmation")
	}
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-start-stop-confirm" {
			m.activate(action{Kind: "menu-select", Index: i})
		}
	}
	if m.viewState().PendingStart != nil || m.configurationLocked() || !strings.Contains(m.sendBlocked(), "may already exist") {
		t.Fatal("Stop following did not unlock the draft and guard the branch", m.sendBlocked())
	}
	// The retry loop survives a missing client instead of dying.
	if m.dispatchStart(c) == nil {
		t.Fatal("nil client ended the retry loop")
	}
}

func TestWorktreeSegmentKeepsRetryableStart(t *testing.T) {
	m, _, _ := worktreeModel(t)
	namedDraft(t, m)
	m.activate(action{Kind: "send"})
	c := *m.busy
	m.snapshot.Worktrees = []protocol.ManagedWorktree{{ID: "wt9", ProjectID: "alpha", Branch: "feature/wt", CommandID: c.ID, State: protocol.WorktreeUnattached, Path: "/home/wt9"}}
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "capacity", Message: "not attached"}}, local: action{Kind: "send"}})
	m.activate(action{Kind: "menu-close"})
	clickControl(m, controlHit(t, m.render(), "workspace-worktree"))
	if ps := m.viewState().PendingStart; ps == nil || ps.State != pendingRetryable || m.menu != nil {
		t.Fatal("re-selecting New worktree changed the kept start")
	}
	clickControl(m, controlHit(t, m.render(), "workspace-checkout"))
	if m.viewState().PendingStart == nil || m.menu[0].Label != "Cancel" || m.draftWorkspace() != workspaceWorktree {
		t.Fatal("mode change dropped the kept start without a Cancel-first confirmation")
	}
	m.activate(action{Kind: "menu-select", Index: 0})
	if m.viewState().PendingStart == nil {
		t.Fatal("Cancel released the start")
	}
	m.activate(action{Kind: "worktree-draft-mode", Value: workspaceCheckout})
	for i, item := range m.menu {
		if item.Action.Kind == "worktree-start-release-confirm" {
			m.activate(action{Kind: "menu-select", Index: i})
			break
		}
	}
	if m.viewState().PendingStart != nil || m.draftWorkspace() != workspaceCheckout || !m.viewState().branchUsed("feature/wt") {
		t.Fatal("confirmed change did not release the start, apply the choice and guard the branch")
	}
}
