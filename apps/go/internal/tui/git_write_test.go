package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// fakeGitWriter adds GitWrite to the read fake. reply decides each answer;
// by default every write succeeds.
type fakeGitWriter struct {
	*fakeGit
	wmu   sync.Mutex
	cmds  []protocol.Command
	reply func(protocol.Command) (protocol.Receipt, error)
}

func (g *fakeGitWriter) GitWrite(_ context.Context, cmd protocol.Command) (protocol.Receipt, error) {
	g.wmu.Lock()
	g.cmds = append(g.cmds, cmd)
	reply := g.reply
	g.wmu.Unlock()
	if reply != nil {
		return reply(cmd)
	}
	return protocol.Receipt{ID: cmd.ID, State: protocol.GitStateSucceeded, Git: &protocol.GitResult{Op: strings.TrimPrefix(cmd.Kind, "git."), State: protocol.GitStateSucceeded, Commit: "abc1234def"}}, nil
}

func (g *fakeGitWriter) sent() []protocol.Command {
	g.wmu.Lock()
	defer g.wmu.Unlock()
	return append([]protocol.Command(nil), g.cmds...)
}

func writableStatus() protocol.GitStatus {
	return protocol.GitStatus{
		Workspace: protocol.WorkspaceInfo{Path: "/src/repo", Kind: "checkout", State: "branch", Branch: "feature/init"},
		Branch:    "feature/init", Upstream: "origin/feature/init",
		HeadOid: "c8dc889aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StagedFingerprint: "fp1",
		Identity: &protocol.GitIdentity{Name: "Dev Example", Email: "dev@example.com"},
		Entries: []protocol.GitStatusEntry{
			{Path: "added.go", Index: "A", Worktree: ".", Group: protocol.GitGroupStaged, Pin: "p-added"},
			{Path: "gone.go", Index: ".", Worktree: "D", Group: protocol.GitGroupUnstaged, Pin: "p-gone"},
			{Path: "docs/界面/very/long/nested/directory/structure/that/keeps/going/設計.md", Index: ".", Worktree: "M", Group: protocol.GitGroupUnstaged, Pin: "p-long"},
			{Path: "scratch.txt", Index: "?", Worktree: "?", Group: protocol.GitGroupUntracked, Pin: "p-scratch"},
		},
	}
}

func gitWriteModel(t *testing.T, width, height int) (*Model, *fakeGitWriter) {
	t.Helper()
	gitNow = func() time.Time { return gitTestNow }
	t.Cleanup(func() { gitNow = time.Now })
	api := &fakeGitWriter{fakeGit: &fakeGit{status: writableStatus(), log: representativeLog()}}
	m := testModel()
	m.connected = true
	m.gitReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-writes")
	m.snapshot.Threads[activeThreadIndex(m)].State = "idle"
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.openSurface("git", "")
	gitWriteSettle(t, m, nil)
	return m, api
}

// gitWriteSettle runs cmd and applies every Git read, write and head result.
func gitWriteSettle(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		_, cmd = m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	for range 6 {
		var next []tea.Cmd
		for _, msg := range pump(t, m, cmd).msgs {
			switch msg.(type) {
			case gitStatusMsg, gitLogMsg, gitViewerMsg, gitHunksMsg, gitWriteMsg, gitHeadMsg, gitOperationMsg, gitPreviewMsg, gitConflictFileMsg, gitReviewMsg, gitRebasePlanMsg, gitPlanProposalMsg:
				_, c := m.Update(msg)
				next = append(next, c)
			}
		}
		if len(next) == 0 {
			return
		}
		cmd = tea.Batch(next...)
	}
}

func gitKey(t *testing.T, m *Model, s string) {
	t.Helper()
	var k tea.KeyPressMsg
	switch s {
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "shift+enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	default:
		r := []rune(s)[0]
		k = tea.KeyPressMsg{Code: r, Text: s}
	}
	_, cmd := m.Update(k)
	gitWriteSettle(t, m, cmd)
}

func gitRowLine(t *testing.T, m *Model, key string) (string, hit) {
	t.Helper()
	h, ok := findHit(m.measure(), key)
	if !ok {
		t.Fatalf("no hit %q", key)
	}
	return ansi.Strip(m.render().rows[h.Rect.Y]), h
}

func TestGitWriteRowSlotsStableHiddenShown(t *testing.T) {
	m, _ := gitWriteModel(t, 110, 60)
	key := "git:unstaged:docs/界面/very/long/nested/directory/structure/that/keeps/going/設計.md"
	hidden, h := gitRowLine(t, m, key)
	m.hover = key
	shown, _ := gitRowLine(t, m, key)
	cut := func(s string) string { return ansi.Cut(s, h.Rect.X, h.Rect.X+h.Rect.W-2*gitSlotWidth) }
	if cut(hidden) != cut(shown) {
		t.Fatalf("path geometry moved:\n%q\n%q", cut(hidden), cut(shown))
	}
	if strings.Contains(hidden, m.icon("discard")) || !strings.Contains(shown, m.icon("discard")) || !strings.Contains(shown, m.icon("add")) {
		t.Fatalf("controls not revealed on hover:\n%q\n%q", hidden, shown)
	}
	// Only the glyph and its spill cell are interactive, inside the slot.
	c, ok := findHit(m.measure(), "git-act:discard:unstaged:docs/界面/very/long/nested/directory/structure/that/keeps/going/設計.md")
	if !ok || c.Rect.W != 2 || c.Slot.W != gitSlotWidth || c.Rect.X != c.Slot.X+1 {
		t.Fatalf("discard hit %+v", c)
	}
	// Hovering the control keeps the controls and the row fill.
	m.hover = c.Key
	if still, _ := gitRowLine(t, m, key); !strings.Contains(still, m.icon("discard")) {
		t.Fatal("controls hid while hovering one")
	}
	// Staged rows reserve the same slots with Unstage at the right.
	m.hover = "git:staged:added.go"
	staged, sh := gitRowLine(t, m, "git:staged:added.go")
	if u, ok := findHit(m.measure(), "git-act:unstage:staged:added.go"); !ok || u.Slot.X != sh.Rect.X+sh.Rect.W-gitSlotWidth || !strings.Contains(staged, m.icon("unstage")) {
		t.Fatalf("unstage slot %+v in %q", u, staged)
	}
}

func TestGitWriteKeyboardAndPointerSendSameCommand(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	m.setFocus("git:unstaged:gone.go")
	gitKey(t, m, "s")
	m2, api2 := gitWriteModel(t, 120, 60)
	h, _ := findHit(m2.measure(), "git-act:stage:unstaged:gone.go")
	_, cmd := m2.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m2, cmd)
	a, b := api.sent(), api2.sent()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("sent %d and %d", len(a), len(b))
	}
	if a[0].Kind != protocol.GitKindStage || a[0].Kind != b[0].Kind || fmt.Sprint(a[0].Git) != fmt.Sprint(b[0].Git) || a[0].ThreadID != b[0].ThreadID {
		t.Fatalf("keyboard %+v vs pointer %+v", a[0], b[0])
	}
	if p := a[0].Git.Paths[0]; p.Path != "gone.go" || p.Group != "unstaged" || p.Pin != "p-gone" {
		t.Fatalf("pin %+v", p)
	}
	// u on a staged row unstages.
	m.setFocus("git:staged:added.go")
	gitKey(t, m, "u")
	if s := api.sent(); len(s) != 2 || s[1].Kind != protocol.GitKindUnstage || s[1].Git.Paths[0].Pin != "p-added" {
		t.Fatalf("unstage %+v", s)
	}
	if !strings.Contains(m.notice.text, "Unstaged added.go") {
		t.Fatalf("notice %q", m.notice.text)
	}
}

func TestGitWriteNoControlsOnConflictAndSubmodule(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	api.status.Entries = append(api.status.Entries,
		protocol.GitStatusEntry{Path: "conflict.go", Index: "U", Worktree: "U", Group: protocol.GitGroupConflicted},
		protocol.GitStatusEntry{Path: "vendor/lib", Index: ".", Worktree: "M", Group: protocol.GitGroupUnstaged, Submodule: true, Pin: "p-sub"})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	f := m.measure()
	for _, h := range f.hits {
		if strings.HasPrefix(h.Key, "git-act:") && (strings.HasSuffix(h.Key, ":conflict.go") || strings.HasSuffix(h.Key, ":vendor/lib")) {
			t.Fatalf("control on %q", h.Key)
		}
	}
	m.setFocus("git:unstaged:vendor/lib")
	gitKey(t, m, "s")
	if len(api.sent()) != 0 {
		t.Fatal("submodule staged")
	}
}

func TestGitWriteDiscardDialog(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	m.setFocus("git:untracked:scratch.txt")
	gitKey(t, m, "d")
	if len(m.menu) == 0 || m.menu[0].Note != "Delete untracked file scratch.txt? This cannot be undone." {
		t.Fatalf("untracked dialog %+v", m.menu)
	}
	if m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("default focus %q", m.menu[m.menuIndex].Label)
	}
	if m.menu[2].Label != "Delete" || !menuItemDestructive(m.menu[2]) {
		t.Fatalf("destructive %+v", m.menu[2])
	}
	gitKey(t, m, "enter")
	if len(m.menu) != 0 || len(api.sent()) != 0 {
		t.Fatal("cancel sent or kept dialog")
	}
	// Tracked copy; the entry changes on refresh while the dialog is open.
	m.setFocus("git:unstaged:gone.go")
	gitKey(t, m, "d")
	if m.menu[0].Note != "Discard changes to gone.go?" || m.menu[2].Label != "Discard" {
		t.Fatalf("tracked dialog %+v", m.menu)
	}
	api.status.Entries[1].Pin = "p-gone-2"
	gitWriteSettle(t, m, m.refreshGit())
	if len(m.menu) != 3 || m.menu[2].Note != "File changed since shown · review" || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("changed dialog %+v", m.menu)
	}
	for _, item := range m.menu {
		if item.Action.Kind == "git-discard-confirm" {
			t.Fatal("destructive action still offered")
		}
	}
	m.menu = nil
	// Confirm sends the pin shown when the dialog opened.
	gitKey(t, m, "d")
	m.menuIndex = 2
	gitKey(t, m, "enter")
	s := api.sent()
	if len(s) != 1 || s[0].Kind != protocol.GitKindDiscard || !s[0].Git.Confirmed || s[0].Git.Paths[0].Pin != "p-gone-2" {
		t.Fatalf("discard %+v", s)
	}
}

func TestGitWriteCommitComposer(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	text := gitSurfaceText(m)
	for _, want := range []string{"Dev Example <dev@example.com>", "Amend last commit: Off", "[Commit]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	m.setFocus(gitMessageKey)
	gitKey(t, m, "F")
	gitKey(t, m, "i")
	gitKey(t, m, "shift+enter")
	_, cmd := m.Update(tea.PasteMsg{Content: "body\nmore"})
	gitWriteSettle(t, m, cmd)
	if len(api.sent()) != 0 {
		t.Fatal("paste or newline committed")
	}
	if got := m.gitMsg().Value(); got != "Fi\nbody\nmore" {
		t.Fatalf("message %q", got)
	}
	gitKey(t, m, "enter")
	s := api.sent()
	if len(s) != 1 || s[0].Kind != protocol.GitKindCommit || s[0].Git.Message != "Fi\nbody\nmore" || s[0].Git.ExpectedHead != api.status.HeadOid || s[0].Git.StagedFingerprint != "fp1" || s[0].Git.Amend {
		t.Fatalf("commit %+v", s)
	}
	if m.gitMsg().Value() != "" || !strings.Contains(m.notice.text, "Committed abc1234 Fi") {
		t.Fatalf("after success: %q notice %q", m.gitMsg().Value(), m.notice.text)
	}
}

func TestGitWriteCommitDisabledStates(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	// Blank message: disabled, and hover paints nothing.
	h, _ := findHit(m.measure(), "git:commit-btn")
	before := m.render().rows[h.Rect.Y]
	m.hover = "git:commit-btn"
	if m.render().rows[h.Rect.Y] != before {
		t.Fatal("disabled Commit reacted to hover")
	}
	if !strings.Contains(h.Label, "Write a commit message") {
		t.Fatalf("help %q", h.Label)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
	if len(api.sent()) != 0 {
		t.Fatal("disabled Commit sent")
	}
	m.gitMsg().SetValue("msg")
	// Nothing staged.
	api.status.Entries = api.status.Entries[1:]
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if h, _ := findHit(m.measure(), "git:commit-btn"); !strings.Contains(h.Label, "Nothing staged") {
		t.Fatalf("help %q", h.Label)
	}
	// Operation in progress.
	api.status = writableStatus()
	api.status.Operation = "rebase"
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if h, _ := findHit(m.measure(), "git:commit-btn"); !strings.Contains(h.Label, "Finish or abort") {
		t.Fatalf("help %q", h.Label)
	}
	// Identity missing.
	api.status = writableStatus()
	api.status.Identity = &protocol.GitIdentity{Missing: true}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if !strings.Contains(gitSurfaceText(m), "Git identity missing · set user.name/user.email") {
		t.Fatal("identity warning missing")
	}
	if h, _ := findHit(m.measure(), "git:commit-btn"); !strings.Contains(h.Label, "identity missing") {
		t.Fatalf("help %q", h.Label)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
	if len(api.sent()) != 0 {
		t.Fatal("commit sent without identity")
	}
}

func TestGitWriteAmendPrefillAndPublishedAck(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	api.status.HeadOnUpstream = true
	api.status.Entries = api.status.Entries[1:] // amend needs nothing staged
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	api.show = protocol.GitShow{Commit: protocol.GitCommit{Hash: api.status.HeadOid, Subject: "Subject", Body: "Subject\n\nBody \x1b[31mline\n"}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
	if got := m.gitMsg().Value(); got != "Subject\n\nBody [31mline" && got != "Subject\n\nBody line" {
		t.Fatalf("prefill %q", got)
	}
	if strings.Contains(m.gitMsg().Value(), "\x1b") {
		t.Fatal("prefill kept an escape")
	}
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Amend last commit: On") || !strings.Contains(text, "Last commit is already on the upstream") || !strings.Contains(text, "[Amend]") {
		t.Fatalf("amend state:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
	if len(api.sent()) != 0 || len(m.menu) == 0 || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("published amend sent without ack or no dialog: %+v", m.menu)
	}
	m.menuIndex = 2
	gitKey(t, m, "enter")
	s := api.sent()
	if len(s) != 1 || !s[0].Git.Amend || !s[0].Git.AcknowledgePublished {
		t.Fatalf("ack commit %+v", s)
	}
	if m.gitDraftFor(m.gitW.draftKey).amend {
		t.Fatal("amend stayed on after success")
	}
}

func TestGitWriteBusyAndProgress(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	// A turn in a nested checkout of the repository holds the lease.
	other := 1 - activeThreadIndex(m)
	m.snapshot.Threads[other].Checkout = "/src/repo/sub"
	m.snapshot.Threads[other].State = "running"
	if !strings.Contains(gitSurfaceText(m), gitLeaseCopy) {
		t.Fatalf("lease state missing:\n%s", gitSurfaceText(m))
	}
	m.setFocus("git:unstaged:gone.go")
	gitKey(t, m, "s")
	if len(api.sent()) != 0 || m.notice.text != gitLeaseCopy {
		t.Fatalf("busy write sent; notice %q", m.notice.text)
	}
	if h, _ := findHit(m.measure(), "git-act:stage:unstaged:gone.go"); !strings.Contains(h.Label, gitLeaseCopy) {
		t.Fatalf("help %q", h.Label)
	}
	m.snapshot.Threads[other].State = "idle"
	// Another client's running write shows progress and disables writes.
	m.snapshot.GitOps = []protocol.GitOp{{Checkout: "/src", CommandID: "other", Op: "commit", State: protocol.GitStateRunning}}
	gitWriteSettle(t, m, nil)
	if !strings.Contains(gitSurfaceText(m), "Committing…") {
		t.Fatalf("progress missing:\n%s", gitSurfaceText(m))
	}
	gitKey(t, m, "s")
	if len(api.sent()) != 0 {
		t.Fatal("write sent during another write")
	}
	// Its completion refreshes status once.
	reads := api.count("status:")
	m.snapshot.GitOps[0].State = protocol.GitStateSucceeded
	gitWriteSettle(t, m, nil)
	if api.count("status:") != reads+1 || strings.Contains(gitSurfaceText(m), "Committing…") {
		t.Fatalf("finished op: reads %d→%d", reads, api.count("status:"))
	}
	// Our own running write shows its verb.
	block := make(chan struct{})
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		<-block
		return protocol.Receipt{}, errors.New("closed")
	}
	cmd := m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"})
	if !strings.Contains(gitSurfaceText(m), "Staging…") {
		t.Fatalf("own progress missing:\n%s", gitSurfaceText(m))
	}
	close(block)
	gitWriteSettle(t, m, cmd)
}

func TestGitWriteErrorCopy(t *testing.T) {
	codes := []string{"status_truncated", "internal_error", "invalid", "not_found", "not_git", "checkout_busy", "git_busy", "stale_entry", "stale_head", "stale_status", "nothing_staged", "nothing_to_amend", "empty_message", "published_commit", "operation_in_progress", "conflicted", "not_supported", "confirmation_required", "identity_missing", "index_locked", "ref_locked", "stopping", "unavailable", "git_failed", "commit_failed", "cancelled", "interrupted", "staged_newer_content", "hooks_changed_content"}
	seen := map[string]string{}
	for _, c := range codes {
		copy := gitErrorCopy(c)
		if copy == gitErrorCopy("unknown-code") || strings.Contains(copy, "_") {
			t.Fatalf("%s: generic or raw copy %q", c, copy)
		}
		if prev, dup := seen[copy]; dup {
			t.Fatalf("%s and %s share copy %q", c, prev, copy)
		}
		seen[copy] = c
	}
	// A stale refusal records nothing, shows its copy and rereads status.
	m, api := gitWriteModel(t, 120, 60)
	api.reply = func(protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "stale_entry", Message: "changed"}
	}
	reads := api.count("status:")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"}))
	if m.notice.text != gitErrorCopy("stale_entry") || api.count("status:") != reads+1 || m.gitWriteFor(m.gitW.draftKey) != nil {
		t.Fatalf("stale refusal: notice %q reads %d", m.notice.text, api.count("status:")-reads)
	}
	// A succeeded write with a warning keeps it until the next action.
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateSucceeded, Code: "staged_newer_content"}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"}))
	if !strings.Contains(gitSurfaceText(m), gitErrorCopy("staged_newer_content")) {
		t.Fatal("warning not persistent")
	}
	// outcome_unknown offers Refresh.
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateOutcomeUnknown}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"}))
	if text := gitSurfaceText(m); strings.Contains(text, gitErrorCopy("staged_newer_content")) || !strings.Contains(text, "Result unknown · refresh and check") || !strings.Contains(text, "Refresh") {
		t.Fatalf("unknown:\n%s", text)
	}
}

func TestGitWriteHookOutputViewerSanitized(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateFailed, Code: "commit_failed",
			Output: "pre-commit: \x1b[31mlint failed\x1b[0m\x1b]0;pwned\x07\r\n\x1b]52;c;ZXZpbA==\x07done", OutputTruncated: true}}, nil
	}
	m.gitMsg().SetValue("msg")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, gitErrorCopy("commit_failed")) || !strings.Contains(text, "View output") {
		t.Fatalf("failure:\n%s", text)
	}
	if m.gitMsg().Value() != "msg" {
		t.Fatal("draft lost after failure")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-output"}))
	if m.viewer == nil || !strings.Contains(m.viewer.att.Name, "truncated") {
		t.Fatalf("viewer %+v", m.viewer)
	}
	c := m.viewer.att.Content
	if strings.ContainsAny(c, "\x1b\x07\r") || !strings.Contains(c, "lint failed") {
		t.Fatalf("unsanitized %q", c)
	}
	for _, row := range m.render().rows {
		if strings.Contains(row, "\x1b]") || strings.Contains(row, "\x07") {
			t.Fatalf("OSC reached the frame: %q", row)
		}
	}
}

func TestGitWriteRetryReusesCommandID(t *testing.T) {
	m, api := gitWriteModel(t, 120, 60)
	fail := true
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		if fail {
			return protocol.Receipt{}, errors.New("connection reset")
		}
		return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateSucceeded}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"}))
	if !strings.Contains(gitSurfaceText(m), "No reply from the server") {
		t.Fatalf("transport:\n%s", gitSurfaceText(m))
	}
	if _, ok := findHit(m.measure(), "git:retry"); !ok {
		t.Fatal("no Retry")
	}
	fail = false
	gitWriteSettle(t, m, m.activate(action{Kind: "git-retry"}))
	s := api.sent()
	if len(s) != 2 || s[0].ID != s[1].ID || fmt.Sprint(s[0]) != fmt.Sprint(s[1]) {
		t.Fatalf("retry changed the command: %+v", s)
	}
	if m.gitWriteFor(m.gitW.draftKey) != nil {
		t.Fatal("state kept after success")
	}
}

func TestGitWriteDraftPersistsAcrossTargets(t *testing.T) {
	m, _ := gitWriteModel(t, 120, 60)
	m.gitMsg().SetValue("shell draft")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
	m.state.Active = "thread-review"
	m.viewState().Host = m.state.Threads["thread-shell"].Host
	gitWriteSettle(t, m, nil)
	if m.gitMsg().Value() != "" {
		t.Fatalf("draft leaked to another target: %q", m.gitMsg().Value())
	}
	m.gitMsg().SetValue("review draft")
	m.state.Active = "thread-shell"
	gitWriteSettle(t, m, nil)
	if m.gitMsg().Value() != "shell draft" || !m.gitDraftFor(m.gitW.draftKey).amend {
		t.Fatalf("draft not restored: %q", m.gitMsg().Value())
	}
	m.state.Active = "thread-review"
	gitWriteSettle(t, m, nil)
	if m.gitMsg().Value() != "review draft" {
		t.Fatalf("second draft %q", m.gitMsg().Value())
	}
}

func TestGitWritesHiddenWithoutCapability(t *testing.T) {
	m, _ := gitModel(t, 120, 60)
	for _, h := range m.measure().hits {
		if strings.HasPrefix(h.Key, "git-act:") || h.Key == gitMessageKey {
			t.Fatalf("write control %q without capability", h.Key)
		}
	}
}

func TestGitWriteCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"rows", "discard", "composer", "published", "failure", "narrow"} {
			w := 150
			if name == "narrow" {
				w = 60
			}
			m, api := gitWriteModel(t, w, 44)
			m.state.Light = light
			if name == "narrow" {
				m.selectColumn(shell.RightRegion)
			}
			switch name {
			case "rows":
				m.setFocus("git:unstaged:gone.go")
				m.hover = "git:untracked:scratch.txt"
			case "discard":
				m.setFocus("git:untracked:scratch.txt")
				gitKey(t, m, "d")
			case "composer", "narrow":
				m.setFocus(gitMessageKey)
				m.gitMsg().SetValue("Add Git write actions\n\nStage, unstage, discard and commit.")
			case "published":
				api.status.HeadOnUpstream = true
				gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
				m.gitMsg().SetValue("Fix typo")
				gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
				m.setFocus("git:amend")
			case "failure":
				api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
					return protocol.Receipt{ID: cmd.ID, Git: &protocol.GitResult{State: protocol.GitStateFailed, Code: "commit_failed", Output: "lint failed"}}, nil
				}
				m.gitMsg().SetValue("WIP")
				gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
				m.notice.text = ""
			}
			m.configureInputs()
			file := filepath.Join(dir, fmt.Sprintf("%dx44-gitwrite-%s-%s.ansi", w, name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGitWriteViewerUsesPinnedEntry(t *testing.T) {
	m, api := gitWriteModel(t, 110, 60)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "gone.go"}))
	if m.viewer == nil || !m.viewer.git.pinned || m.viewer.git.entry.Pin != "p-gone" {
		t.Fatalf("viewer not pinned: %+v", m.viewer)
	}
	// s from the viewer stages exactly the shown pin.
	gitKey(t, m, "s")
	if s := api.sent(); len(s) != 1 || s[0].Git.Paths[0].Pin != "p-gone" {
		t.Fatalf("stage %+v", s)
	}
	// A refresh showing another pin marks the viewer and disables s/d.
	s := writableStatus()
	s.Entries[1].Pin = "p-gone-NEWER-UNSEEN"
	api.fakeGit.status = s
	gitWriteSettle(t, m, m.refreshGit())
	if !m.viewer.git.changed || !strings.Contains(screenText(m), gitViewerChangedCopy) {
		t.Fatal("viewer not marked changed")
	}
	gitKey(t, m, "d")
	if m.gitW.discard != nil || len(m.menu) != 0 || m.notice.text != gitViewerChangedCopy {
		t.Fatalf("discard offered for unseen content: %+v %q", m.gitW.discard, m.notice.text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-discard-confirm"}))
	for _, c := range api.sent() {
		if c.Kind == protocol.GitKindDiscard {
			t.Fatalf("discard sent with pin %s", c.Git.Paths[0].Pin)
		}
	}
}

func TestGitWriteViewerDiscardDialogPinsViewerEntry(t *testing.T) {
	m, api := gitWriteModel(t, 110, 60)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "gone.go"}))
	gitKey(t, m, "d")
	if m.viewer != nil || m.gitW.discard == nil || m.gitW.discard.entry.Pin != "p-gone" {
		t.Fatalf("dialog %+v", m.gitW.discard)
	}
	api.status.Entries[1].Pin = "p-gone-2"
	gitWriteSettle(t, m, m.refreshGit())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-discard-confirm", Value: "unstaged", ID: "gone.go"}))
	if len(api.sent()) != 0 {
		t.Fatalf("discard sent after pin change: %+v", api.sent())
	}
}

func TestGitWriteLostReplyBlocksNewWrites(t *testing.T) {
	m, api := gitWriteModel(t, 110, 60)
	api.reply = func(protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, errors.New("connection reset")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
	m.gitMsg().SetValue("Amended message")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-ack", Value: m.gitDraftFor(m.gitW.draftKey).amendHead}))
	first := api.sent()
	if len(first) != 1 {
		t.Fatalf("first %+v", first)
	}
	// The server did amend; status now shows the new HEAD.
	s := writableStatus()
	s.HeadOid, s.StagedFingerprint = "dddddddaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "fp2"
	api.fakeGit.status = s
	gitWriteSettle(t, m, m.refreshGit())
	api.reply = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-ack", Value: first[0].Git.ExpectedHead}))
	m.setFocus(gitMessageKey)
	gitKey(t, m, "enter")
	m.setFocus("git:unstaged:gone.go")
	gitKey(t, m, "s")
	if len(api.sent()) != 1 {
		t.Fatalf("new command while the reply was lost: %+v", api.sent())
	}
	if m.notice.text != gitPendingCopy {
		t.Fatalf("notice %q", m.notice.text)
	}
	// Retry resends the same command; only then are writes free.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-retry"}))
	if s := api.sent(); len(s) != 2 || fmt.Sprint(s[0]) != fmt.Sprint(s[1]) {
		t.Fatalf("retry %+v", s)
	}
}

func TestGitWriteAmendBindsHead(t *testing.T) {
	m, api := gitWriteModel(t, 110, 60)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
	m.gitMsg().SetValue("Message of old HEAD c8dc889")
	s := writableStatus()
	s.HeadOid = "eeeeeeeaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	api.fakeGit.status = s
	gitWriteSettle(t, m, m.refreshGit())
	if m.gitDraftFor(m.gitW.draftKey).amend || !strings.Contains(gitSurfaceText(m), gitHeadChangedCopy) {
		t.Fatalf("amend kept across HEAD change:\n%s", gitSurfaceText(m))
	}
	// A stale published confirmation is refused.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-ack", Value: writableStatus().HeadOid}))
	for _, c := range api.sent() {
		if c.Git.Amend {
			t.Fatalf("amended new HEAD: %+v", c)
		}
	}
	// Re-enabling binds to the new HEAD and ExpectedHead follows it.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-amend"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-commit-run"}))
	sent := api.sent()
	if len(sent) == 0 || !sent[len(sent)-1].Git.Amend || sent[len(sent)-1].Git.ExpectedHead != s.HeadOid {
		t.Fatalf("rebound amend %+v", sent)
	}
	// A prefill for an old HEAD is ignored.
	d := m.gitDraftFor(m.gitW.draftKey)
	d.amend, d.amendHead = true, s.HeadOid
	m.gitMsg().SetValue("")
	m.Update(gitHeadMsg{key: m.gitW.draftKey, head: "old", message: "old message"})
	if m.gitMsg().Value() != "" {
		t.Fatal("stale prefill applied")
	}
}

func TestGitWriteIntentToAddDiscardCopy(t *testing.T) {
	m, api := gitWriteModel(t, 110, 60)
	api.status.Entries = append(api.status.Entries, protocol.GitStatusEntry{Path: "ita.go", Index: ".", Worktree: "A", Group: protocol.GitGroupUnstaged, Pin: "p-ita"}, protocol.GitStatusEntry{Path: "flag.go", Index: ".", Worktree: "M", IntentToAdd: true, Group: protocol.GitGroupUnstaged, Pin: "p-flag"})
	gitWriteSettle(t, m, m.refreshGit())
	m.setFocus("git:unstaged:ita.go")
	gitKey(t, m, "d")
	if len(m.menu) < 3 || m.menu[0].Note != "Discard new file ita.go? Its contents cannot be recovered." || !menuItemDestructive(m.menu[2]) {
		t.Fatalf("intent-to-add dialog %+v", m.menu)
	}
}

func TestGitWriteUnknownOutcomeOnRetryKeepsCommand(t *testing.T) {
	for _, code := range []string{"unknown_outcome_lookup", "storage"} {
		m, api := gitWriteModel(t, 110, 60)
		n := 0
		api.reply = func(protocol.Command) (protocol.Receipt, error) {
			n++
			if n == 1 {
				return protocol.Receipt{}, errors.New("timeout")
			}
			return protocol.Receipt{}, &protocol.Error{Code: code}
		}
		gitWriteSettle(t, m, m.activate(action{Kind: "git-stage", Value: "unstaged", ID: "gone.go"}))
		gitWriteSettle(t, m, m.activate(action{Kind: "git-retry"}))
		st := m.gitWriteFor(m.gitW.draftKey)
		if st == nil || st.transport != "Result unknown · refresh and check" || !strings.Contains(gitSurfaceText(m), "Result unknown · refresh and check") {
			t.Fatalf("%s: state %+v", code, st)
		}
		if _, ok := findHit(m.measure(), "git:retry"); !ok {
			t.Fatalf("%s: Retry gone", code)
		}
	}
}
