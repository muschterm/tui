package tui

import (
	"context"
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

// GitSync routes fetch, pull and push through the same recorder and reply.
func (g *fakeGitWriter) GitSync(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error) {
	return g.GitWrite(ctx, cmd)
}

const refTip = "5ide000000000000000000000000000000000000"

func refModel(t *testing.T, width, height int) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := gitWriteModel(t, width, height)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-history", "git-refs")
	api.branch = protocol.GitBranches{Branches: []protocol.GitBranch{
		{Name: "feature/init", Ref: "refs/heads/feature/init", Head: true, Tip: api.status.HeadOid, Upstream: "origin/feature/init"},
		{Name: "side", Ref: "refs/heads/side", Tip: refTip},
		{Name: "origin/main", Ref: "refs/remotes/origin/main", Remote: true, Tip: strings.Repeat("e", 40)},
	}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-branches"}))
	return m, api
}

func refReply(api *fakeGitWriter, r protocol.GitResult) {
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		r := r
		r.Op = strings.TrimPrefix(cmd.Kind, "git.")
		return protocol.Receipt{ID: cmd.ID, State: r.State, Git: &r}, nil
	}
}

func lastSent(t *testing.T, api *fakeGitWriter) protocol.Command {
	t.Helper()
	s := api.sent()
	if len(s) == 0 {
		t.Fatal("nothing sent")
	}
	return s[len(s)-1]
}

func menuText(m *Model) string {
	var lines []string
	for _, item := range m.menu {
		lines = append(lines, item.Note+item.Label)
	}
	return strings.Join(lines, "\n")
}

func TestGitRefHeadingControlsOrderAndSlots(t *testing.T) {
	m, _ := refModel(t, 120, 60)
	f := m.measure()
	var xs []int
	for _, k := range []string{"git:sync:fetch", "git:sync:pull", "git:sync:push", "git-refresh"} {
		h, ok := findHit(f, k)
		if !ok || h.Rect.W != 2 || h.Slot.W != gitSlotWidth || h.Rect.X != h.Slot.X+1 {
			t.Fatalf("%s hit %+v", k, h)
		}
		xs = append(xs, h.Rect.X)
		if !strings.Contains(h.Label, "(") && k != "git-refresh" {
			t.Fatalf("%s help lacks its key: %q", k, h.Label)
		}
	}
	for i := 1; i < len(xs); i++ {
		if xs[i] != xs[i-1]+gitSlotWidth {
			t.Fatalf("slots not contiguous %v", xs)
		}
	}
	if h, _ := findHit(f, "git:sync:pull"); !strings.Contains(h.Label, "fast-forward only") {
		t.Fatalf("pull help %q", h.Label)
	}
}

func TestGitFetchKeyboardAndPointerParity(t *testing.T) {
	m, api := refModel(t, 120, 60)
	m.setFocus("git-refresh")
	gitKey(t, m, "f")
	m2, api2 := refModel(t, 120, 60)
	h, _ := findHit(m2.measure(), "git:sync:fetch")
	_, cmd := m2.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m2, cmd)
	a, b := lastSent(t, api), lastSent(t, api2)
	if a.Kind != protocol.GitKindFetch || a.Kind != b.Kind || fmt.Sprint(*a.Git.Sync) != fmt.Sprint(*b.Git.Sync) {
		t.Fatalf("keyboard %+v pointer %+v", a, b)
	}
	if !strings.Contains(m.notice.text, "Fetched") {
		t.Fatalf("notice %q", m.notice.text)
	}
}

func TestGitSyncProgressAndCancel(t *testing.T) {
	m, api := refModel(t, 120, 60)
	cmd := m.activate(action{Kind: "git-fetch"})
	if cmd == nil {
		t.Fatal("no fetch cmd")
	}
	st := m.gitWriteFor(m.gitW.draftKey)
	if st == nil || !st.running {
		t.Fatal("fetch not running")
	}
	m.snapshot.GitOps = []protocol.GitOp{{Checkout: "/src/repo", CommandID: st.cmd.ID, Op: "fetch", State: protocol.GitStateRunning,
		Progress: &protocol.GitProgress{Phase: "Receiving objects", Percent: 42}, Cancellable: true}}
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Fetching origin… 42%") || !strings.Contains(text, "Receiving objects") {
		t.Fatalf("progress missing:\n%s", text)
	}
	h, ok := findHit(m.measure(), "git:cancel")
	if !ok {
		t.Fatal("no cancel control")
	}
	// Push is blocked while the fetch runs (one Git write per repository).
	if p, _ := findHit(m.measure(), "git:sync:push"); !strings.Contains(p.Label, "unavailable") {
		t.Fatalf("push not blocked: %q", p.Label)
	}
	_, c := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	for _, msg := range pump(t, m, c).msgs {
		if cm, ok := msg.(gitCancelMsg); ok {
			m.Update(cm)
		}
	}
	sent := lastSent(t, api)
	if sent.Kind != protocol.GitKindCancel || sent.Git.Cancel.CommandID != st.cmd.ID {
		t.Fatalf("cancel %+v", sent)
	}
	// A refused cancel explains itself.
	m.Update(gitCancelMsg{err: &protocol.Error{Code: "not_cancellable"}})
	if !strings.Contains(m.notice.text, "Past the cancellable phase") {
		t.Fatalf("notice %q", m.notice.text)
	}
	// The fetch's own reply: cancelled, failed.
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "cancelled", Fetch: &protocol.GitFetchResult{Remote: "origin", State: protocol.GitFetchCancelled}})
	m.snapshot.GitOps = nil
	gitWriteSettle(t, m, cmd)
	if !strings.Contains(gitSurfaceText(m), "Cancelled") {
		t.Fatalf("cancel result:\n%s", gitSurfaceText(m))
	}
}

func TestGitPullResultsCopy(t *testing.T) {
	for _, tc := range []struct {
		r    protocol.GitResult
		want []string
	}{
		{protocol.GitResult{State: protocol.GitStateSucceeded, Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "fast_forward", To: "abc1234ffff"}}, []string{"Fast-forwarded feature/init to abc1234"}},
		{protocol.GitResult{State: protocol.GitStateSucceeded, Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "up_to_date"}}, []string{"Up to date"}},
		{protocol.GitResult{State: protocol.GitStateSucceeded, Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "ahead"}}, []string{"Ahead of upstream by 2"}},
		{protocol.GitResult{State: protocol.GitStateFailed, Code: "would_overwrite", Paths: []string{"a.go"}, Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "failed"}}, []string{"Fetched origin, not integrated · Local files would be overwritten", "a.go"}},
		{protocol.GitResult{State: protocol.GitStateFailed, Code: "host_key_unknown", Fetch: &protocol.GitFetchResult{Remote: "origin", State: "failed"}}, []string{"Fetch failed · The remote's SSH host key is not trusted", gitCredentialAdvice}},
	} {
		m, api := refModel(t, 120, 60)
		api.status.Ahead = 2
		refReply(api, tc.r)
		gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
		text := gitSurfaceText(m)
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Fatalf("missing %q:\n%s", w, text)
			}
		}
		if s := lastSent(t, api); s.Kind != protocol.GitKindPull || s.Git.Sync.Upstream != "origin/feature/init" || s.Git.Sync.ExpectedBranch != "feature/init" {
			t.Fatalf("pull %+v", s.Git.Sync)
		}
	}
}

func TestGitPullDivergedCompare(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Ahead, api.status.Behind = 2, 3
	to := strings.Repeat("f", 40)
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "diverged", Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "diverged", To: to}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
	if text := gitSurfaceText(m); !strings.Contains(text, "Diverged: 2 ahead, 3 behind") || !strings.Contains(text, "Fetched, not integrated") {
		t.Fatalf("diverged:\n%s", text)
	}
	h, ok := findHit(m.measure(), "git:pull-compare")
	if !ok {
		t.Fatal("no compare")
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	if api.count("compare:") != 1 || !strings.Contains(strings.Join(api.calls, " "), ":"+to+":HEAD") {
		t.Fatalf("compare calls %v", api.calls)
	}
}

func TestGitPushResultsAndRefusals(t *testing.T) {
	m, api := refModel(t, 120, 60)
	refReply(api, protocol.GitResult{State: protocol.GitStateSucceeded, Push: &protocol.GitPushResult{State: "pushed"}})
	gitPushConfirmed(t, m)
	if !strings.Contains(gitSurfaceText(m), "Pushed feature/init → origin/feature/init") {
		t.Fatalf("pushed:\n%s", gitSurfaceText(m))
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "rejected", Push: &protocol.GitPushResult{State: "rejected", Reason: "fetch first\x1b[2J"}})
	gitPushConfirmed(t, m)
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Push rejected · fetch first") || !strings.Contains(text, "Pull first") || strings.Contains(text, "\x1b") {
		t.Fatalf("rejected:\n%q", text)
	}
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "upstream_name_mismatch"}
	}
	gitPushConfirmed(t, m)
	if !strings.Contains(m.notice.text, "push.default") {
		t.Fatalf("mismatch notice %q", m.notice.text)
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateOutcomeUnknown, Code: "transport"})
	gitPushConfirmed(t, m)
	if !strings.Contains(gitSurfaceText(m), "the remote may have accepted it") {
		t.Fatalf("unknown:\n%s", gitSurfaceText(m))
	}
	if _, ok := findHit(m.measure(), "git:unknown-refresh"); !ok {
		t.Fatal("no refresh for unknown outcome")
	}
	// Behind the upstream: Push is disabled with Pull first, nothing sent.
	api.status.Behind = 1
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	n := len(api.sent())
	gitPushConfirmed(t, m)
	if len(api.sent()) != n || !strings.Contains(m.notice.text, "Pull first") {
		t.Fatalf("push while behind: %q", m.notice.text)
	}
}

func TestGitLeaseGatesOnlyLeaseActions(t *testing.T) {
	m, api := refModel(t, 120, 60)
	other := 1 - activeThreadIndex(m)
	m.snapshot.Threads[other].Checkout = "/src/repo/sub"
	m.snapshot.Threads[other].State = "running"
	for _, kind := range []string{"git-pull"} {
		gitWriteSettle(t, m, m.activate(action{Kind: kind}))
		if len(api.sent()) != 0 || m.notice.text != gitLeaseCopy {
			t.Fatalf("%s during lease: %q %v", kind, m.notice.text, api.sent())
		}
	}
	if h, _ := findHit(m.measure(), "git-bact:switch:refs/heads/side"); !strings.Contains(h.Label, "unavailable") {
		t.Fatalf("switch not gated: %q", h.Label)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-fetch"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-branch-new", ID: refTip, Value: "side"}))
	m.gitBranchInput().SetValue("topic")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-branch-create"}))
	s := api.sent()
	if len(s) != 2 || s[0].Kind != protocol.GitKindFetch || s[1].Kind != protocol.GitKindBranchCreate {
		t.Fatalf("non-lease actions %+v", s)
	}
}

func TestGitSwitchCarryDialog(t *testing.T) {
	m, api := refModel(t, 120, 60)
	m.setFocus("git:branch:refs/heads/side")
	gitKey(t, m, "S")
	if len(api.sent()) != 0 {
		t.Fatal("switched without confirmation")
	}
	text := menuText(m)
	if !strings.Contains(text, "Switch to side carrying 4 changes?") || !strings.Contains(text, "1 staged · 2 unstaged · 1 untracked") {
		t.Fatalf("carry dialog:\n%s", text)
	}
	if m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("default focus %q", m.menu[m.menuIndex].Label)
	}
	// Cancel sends nothing.
	gitKey(t, m, "enter")
	if len(api.sent()) != 0 || len(m.menu) != 0 {
		t.Fatal("cancel sent or kept the dialog")
	}
	shown := *m.currentGitView().status
	m.setFocus("git:branch:refs/heads/side")
	gitKey(t, m, "S")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch-carry"}))
	s := lastSent(t, api)
	ref := s.Git.Ref
	if s.Kind != protocol.GitKindSwitch || ref.Branch != "side" || ref.TargetOid != refTip || ref.AcknowledgeCarry != 4 ||
		ref.WorktreeFingerprint != protocol.GitWorktreeFingerprint(shown) || ref.ExpectedBranch != "feature/init" {
		t.Fatalf("switch %+v", ref)
	}
}

func TestGitSwitchCarryRefusedWhenStatusChanged(t *testing.T) {
	m, api := refModel(t, 120, 60)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	api.status.Entries = api.status.Entries[:2]
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch-carry"}))
	if len(api.sent()) != 0 || !strings.Contains(m.notice.text, "Changes differ from shown") {
		t.Fatalf("stale carry sent: %q", m.notice.text)
	}
}

func TestGitSwitchLeavesCommitsResend(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Entries = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	first := true
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		if first {
			first = false
			return protocol.Receipt{}, &protocol.Error{Code: "leaves_commits", Message: "3 commits are reachable only from HEAD"}
		}
		return protocol.Receipt{ID: cmd.ID, State: "succeeded", Git: &protocol.GitResult{Op: "switch", State: "succeeded", Ref: &protocol.GitRefResult{Branch: "side"}}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	if text := menuText(m); !strings.Contains(text, "Leaving 3 commits reachable only from HEAD · they stay in the reflog") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("leave dialog:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-ack-confirm"}))
	s := api.sent()
	if len(s) != 2 || s[1].Git.Ref.AcknowledgeLeaveCommits != 3 || s[1].ID == s[0].ID || s[0].Git.Ref.AcknowledgeLeaveCommits != 0 {
		t.Fatalf("resend %+v", s)
	}
	if !strings.Contains(gitSurfaceText(m), "Switched to side") {
		t.Fatalf("switch result:\n%s", gitSurfaceText(m))
	}
}

func TestGitSwitchWouldOverwriteAndPartial(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Entries = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	paths := []string{"x\x1b[31m.go"}
	for i := range 9 {
		paths = append(paths, fmt.Sprintf("p%d.go", i))
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "would_overwrite", Paths: paths})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Local files would be overwritten · nothing changed") || !strings.Contains(text, "and 2 others") || strings.Contains(text, "\x1b") || !strings.Contains(text, "p6.go") || strings.Contains(text, "p7.go") {
		t.Fatalf("would_overwrite:\n%q", text)
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "would_overwrite", Paths: []string{"a"}, PathsIncomplete: true})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	if !strings.Contains(gitSurfaceText(m), "and others") {
		t.Fatalf("incomplete:\n%s", gitSurfaceText(m))
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateOutcomeUnknown, Code: "partial_switch"})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	if !strings.Contains(gitSurfaceText(m), "Git changed files but did not switch; review status") {
		t.Fatalf("partial:\n%s", gitSurfaceText(m))
	}
}

func TestGitCreateBranchFromCommit(t *testing.T) {
	m, api := refModel(t, 120, 60)
	hash := representativeLog().Commits[1].Hash
	m.setFocus("git:commit:" + hash)
	gitKey(t, m, "b")
	if m.focus != gitBranchNameKey || !strings.Contains(gitSurfaceText(m), "NEW BRANCH from 9f65491") {
		t.Fatalf("editor focus %q:\n%s", m.focus, gitSurfaceText(m))
	}
	for _, r := range "bad name" {
		gitKey(t, m, string(r))
	}
	gitKey(t, m, "enter")
	if len(api.sent()) != 0 || !strings.Contains(gitSurfaceText(m), "Not a valid branch name") {
		t.Fatalf("invalid name accepted")
	}
	m.gitBranchInput().SetValue("topic")
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	if s.Kind != protocol.GitKindBranchCreate || s.Git.Ref.Name != "topic" || s.Git.Ref.StartOid != hash {
		t.Fatalf("create %+v", s.Git.Ref)
	}
	if !strings.Contains(m.notice.text, "Created branch topic at 9f65491") {
		t.Fatalf("notice %q", m.notice.text)
	}
	// Create and switch uses switch-create with the carry flow.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-branch-new", ID: hash, Value: "9f65491"}))
	m.gitBranchInput().SetValue("topic2")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-branch-create-switch"}))
	if !strings.Contains(menuText(m), "Switch to topic2 carrying 4 changes?") {
		t.Fatalf("carry dialog:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch-carry"}))
	if s := lastSent(t, api); s.Kind != protocol.GitKindSwitch || s.Git.Ref.Name != "topic2" || s.Git.Ref.StartOid != hash || s.Git.Ref.AcknowledgeCarry != 4 {
		t.Fatalf("switch-create %+v", s.Git.Ref)
	}
}

func TestGitSoftResetPublishedAckAndUndo(t *testing.T) {
	m, api := refModel(t, 120, 60)
	log := representativeLog()
	log.Commits[0].Parents = []string{log.Commits[1].Hash}
	api.log = log
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	target := log.Commits[1].Hash
	m.setFocus("git:commit:" + target)
	gitKey(t, m, "r")
	text := menuText(m)
	if !strings.Contains(text, "Soft reset feature/init to 9f65491?\n9f65491 Serialize write-capable turns per checkout") ||
		!strings.Contains(text, "About 1 commit leave feature/init (from the loaded log)\nOnly the reflog keeps them unless another branch,\ntag or remote contains them") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("reset dialog:\n%s", text)
	}
	head := api.status.HeadOid
	calls := 0
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		calls++
		if calls == 1 {
			return protocol.Receipt{}, &protocol.Error{Code: "published_commit"}
		}
		return protocol.Receipt{ID: cmd.ID, State: "succeeded", Git: &protocol.GitResult{Op: "reset_soft", State: "succeeded",
			Ref: &protocol.GitRefResult{Branch: "feature/init", Head: cmd.Git.Ref.TargetOid, PreviousHead: head}}}, nil
	}
	api.status.HeadOid = target   // what the refresh after success shows
	m.menuIndex = len(m.menu) - 1 // the explicit Soft reset item
	gitKey(t, m, "enter")
	if !strings.Contains(menuText(m), "These commits are on the remote") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("published dialog:\n%s", menuText(m))
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	s := api.sent()
	if len(s) != 2 || !s[1].Git.Ref.AcknowledgePublished || s[0].Git.Ref.AcknowledgePublished || s[1].ID == s[0].ID || s[1].Git.Ref.ExpectedHead != head {
		t.Fatalf("reset sends %+v", s)
	}
	text = gitSurfaceText(m)
	if !strings.Contains(text, "Previous tip c8dc889") {
		t.Fatalf("reset result:\n%s", text)
	}
	m.viewState().DetailScroll = 0
	h, ok := findHit(m.measure(), "git:reset-undo")
	if !ok {
		t.Fatalf("no undo:\n%s", text)
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	u := lastSent(t, api).Git.Ref
	if u.TargetOid != head || u.ExpectedHead != target || u.ExpectedBranch != "feature/init" {
		t.Fatalf("undo %+v", u)
	}
}

func TestGitRefRetryReusesID(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, errors.New("connection reset")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
	if !strings.Contains(gitSurfaceText(m), "No reply from the server") {
		t.Fatalf("transport:\n%s", gitSurfaceText(m))
	}
	api.reply = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-retry"}))
	s := api.sent()
	if len(s) != 2 || s[0].ID != s[1].ID || s[1].Kind != protocol.GitKindPull {
		t.Fatalf("retry %+v", s)
	}
}

func TestGitBranchRowSlotsStable(t *testing.T) {
	m, _ := refModel(t, 120, 60)
	key := "git:branch:refs/heads/side"
	hidden, h := gitRowLine(t, m, key)
	m.hover = key
	shown, _ := gitRowLine(t, m, key)
	cut := func(s string) string { return ansi.Cut(s, h.Rect.X, h.Rect.X+h.Rect.W-2*gitSlotWidth) }
	if cut(hidden) != cut(shown) || strings.Contains(hidden, m.icon("switch")) || !strings.Contains(shown, m.icon("switch")) {
		t.Fatalf("branch slots:\n%q\n%q", hidden, shown)
	}
	sw, ok := findHit(m.measure(), "git-bact:switch:refs/heads/side")
	if !ok || sw.Rect.W != 2 || sw.Slot.W != gitSlotWidth {
		t.Fatalf("switch hit %+v", sw)
	}
	// HEAD and remote rows have no Switch, only the menu slot.
	for _, ref := range []string{"refs/heads/feature/init", "refs/remotes/origin/main"} {
		if _, ok := findHit(m.measure(), "git-bact:switch:"+ref); ok {
			t.Fatalf("switch offered on %s", ref)
		}
		if _, ok := findHit(m.measure(), "git-bact:menu:"+ref); !ok {
			t.Fatalf("no menu on %s", ref)
		}
	}
}

func TestGitContextMenusNameCommitAndBranch(t *testing.T) {
	m, api := refModel(t, 120, 60)
	hash := representativeLog().Commits[1].Hash
	m.setFocus("git:commit:" + hash)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	gitWriteSettle(t, m, cmd)
	text := menuText(m)
	for _, want := range []string{"Create branch at 9f65491… (b)", "Soft reset feature/init to 9f65491… (r)", "Copy hash 9f65491 (y)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	m.closeContextMenu()
	// The branch menu slot opens the branch menu with Switch.
	h, _ := findHit(m.measure(), "git-bact:menu:refs/heads/side")
	_, cmd = m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	text = menuText(m)
	for _, want := range []string{"Switch to side (S)", "Create branch from side 5ide000… (b)", "Compare side with HEAD"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if len(api.sent()) != 0 {
		t.Fatal("menu sent a command")
	}
}

// TestGitRefCaptures writes visual review captures of the ref and remote
// actions when TUI_GO_CAPTURE_DIR is set.
func TestGitRefCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"sync-progress", "diverged", "carry", "reset", "branch-row", "create"} {
			m, api := refModel(t, 144, 44)
			m.state.Light = light
			switch name {
			case "sync-progress":
				m.activate(action{Kind: "git-fetch"})
				st := m.gitWriteFor(m.gitW.draftKey)
				m.snapshot.GitOps = []protocol.GitOp{{Checkout: "/src/repo", CommandID: st.cmd.ID, Op: "fetch", State: protocol.GitStateRunning,
					Progress: &protocol.GitProgress{Phase: "Receiving objects", Percent: 42}, Cancellable: true}}
				m.hover = "git:sync:pull"
			case "diverged":
				api.status.Ahead, api.status.Behind = 2, 3
				refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "diverged", Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "diverged", To: strings.Repeat("f", 40)}})
				gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
			case "carry":
				gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
			case "reset":
				m.setFocus("git:commit:" + representativeLog().Commits[1].Hash)
				gitKey(t, m, "r")
			case "branch-row":
				m.viewState().DetailScroll = 1 << 20
				m.setFocus("git:branch:refs/heads/side")
				m.hover = "git:branch:refs/heads/side"
			case "create":
				m.setFocus("git:commit:" + representativeLog().Commits[1].Hash)
				gitKey(t, m, "b")
				m.gitBranchInput().SetValue("topic/new")
			}
			m.markDirty()
			file := filepath.Join(dir, fmt.Sprintf("144x44-git-ref-%s-%s.ansi", name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// gitPushConfirmed asks for a push and accepts the confirmation.
func gitPushConfirmed(t *testing.T, m *Model) {
	t.Helper()
	gitWriteSettle(t, m, m.activate(action{Kind: "git-push"}))
	if len(m.menu) == 0 {
		return // refused locally; the notice explains
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-push-confirm"}))
	m.menu = nil
}

func TestGitPushNeedsConfirmation(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Ahead = 2
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	m.setFocus("git-refresh")
	gitKey(t, m, "P")
	if len(api.sent()) != 0 || !strings.Contains(menuText(m), "Push feature/init (2 commits) to origin/feature/init?") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("push dialog:\n%s", menuText(m))
	}
	gitKey(t, m, "enter") // Cancel
	if len(api.sent()) != 0 || m.gitR.push != nil {
		t.Fatal("cancel pushed or kept state")
	}
	gitKey(t, m, "P")
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	if s := lastSent(t, api); s.Kind != protocol.GitKindPush {
		t.Fatalf("push %+v", s)
	}
}

func TestGitRemoteKeysScopedToHeadingAndHistoryRows(t *testing.T) {
	m, api := refModel(t, 120, 60)
	for _, f := range []string{"git:unstaged:gone.go", "git:commit-btn"} {
		m.setFocus(f)
		for _, k := range []string{"f", "p", "P"} {
			gitKey(t, m, k)
		}
	}
	if len(api.sent()) != 0 || len(m.menu) != 0 {
		t.Fatalf("remote keys fired off scope: %v", api.sent())
	}
	// A branch row's slot takes the row's keys and menu.
	m.setFocus("git-bact:switch:refs/heads/side")
	gitKey(t, m, "b")
	if m.gitR.create == nil || m.gitR.create.startOid != refTip {
		t.Fatal("b on a branch slot did not open the editor")
	}
	m.closeGitCreate()
	m.setFocus("git-bact:menu:refs/heads/side")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	gitWriteSettle(t, m, cmd)
	if !strings.Contains(menuText(m), "Switch to side (S)") {
		t.Fatalf("slot menu:\n%s", menuText(m))
	}
}

func TestGitPullUnknownAfterFastForward(t *testing.T) {
	m, api := refModel(t, 120, 60)
	to := strings.Repeat("a", 40)
	refReply(api, protocol.GitResult{State: protocol.GitStateOutcomeUnknown, Code: "git_failed",
		Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "fast_forward", To: to}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "feature/init may have moved to aaaaaaa · result unknown · refresh and check") || strings.Contains(text, "not integrated") {
		t.Fatalf("pull unknown:\n%s", text)
	}
}

func TestGitPushedNewerHeadNamesCommit(t *testing.T) {
	m, api := refModel(t, 120, 60)
	refReply(api, protocol.GitResult{State: protocol.GitStateSucceeded, Code: "pushed_newer_head",
		Push: &protocol.GitPushResult{State: "pushed", NewOid: strings.Repeat("b", 40)}})
	gitPushConfirmed(t, m)
	if !strings.Contains(gitSurfaceText(m), "Pushed bbbbbbb (newer than shown) to origin/feature/init") {
		t.Fatalf("newer head:\n%s", gitSurfaceText(m))
	}
}

func TestGitUndoOfUndoAsksAndIsNotOffered(t *testing.T) {
	m, api := refModel(t, 120, 60)
	log := representativeLog()
	log.Commits[0].Parents = []string{log.Commits[1].Hash}
	api.log = log
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	head := api.status.HeadOid
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		prev := api.status.HeadOid
		api.status.HeadOid = cmd.Git.Ref.TargetOid
		return protocol.Receipt{ID: cmd.ID, State: "succeeded", Git: &protocol.GitResult{Op: "reset_soft", State: "succeeded",
			Ref: &protocol.GitRefResult{Branch: "feature/init", Head: cmd.Git.Ref.TargetOid, PreviousHead: prev}}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-reset", ID: log.Commits[1].Hash}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-reset-confirm"}))
	m.menu = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-reset-undo"}))
	if api.status.HeadOid != head {
		t.Fatal("undo did not restore")
	}
	u := lastSent(t, api).Git.Ref
	if u.AcknowledgePublished || u.AcknowledgeNotAncestor {
		t.Fatalf("undo carried acknowledgements %+v", u)
	}
	if strings.Contains(gitSurfaceText(m), "Undo · soft reset back to") {
		t.Fatal("undo offered for an undo")
	}
	n := len(api.sent())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-reset-undo"}))
	if len(api.sent()) != n {
		t.Fatal("undo of undo sent")
	}
}

func TestGitBranchNameEscCancels(t *testing.T) {
	m, api := refModel(t, 120, 60)
	row := "git:commit:" + representativeLog().Commits[1].Hash
	m.setFocus(row)
	gitKey(t, m, "b")
	for _, r := range "topic" {
		gitKey(t, m, string(r))
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	gitWriteSettle(t, m, cmd)
	if m.gitR.create != nil || m.focus != row || m.gitBranchInput().Value() != "" || strings.Contains(gitSurfaceText(m), "NEW BRANCH") || len(api.sent()) != 0 {
		t.Fatalf("esc: focus %q create %v", m.focus, m.gitR.create)
	}
}

func TestGitResetCountOmittedForOtherHead(t *testing.T) {
	m, api := refModel(t, 120, 60)
	log := representativeLog()
	log.Commits[0].Parents = []string{log.Commits[1].Hash}
	log.Commits[0].Refs = nil // log read at another HEAD
	api.log = log
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-reset", ID: log.Commits[1].Hash}))
	if text := menuText(m); strings.Contains(text, "About") || !strings.Contains(text, "Commits after 9f65491 leave feature/init") {
		t.Fatalf("reset copy:\n%s", text)
	}
}

func TestGitAckQueuedBehindOpenMenuAndDialogStateCleared(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Entries = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "leaves_commits", Message: "3 commits"}
	}
	cmd := m.activate(action{Kind: "git-switch", ID: "refs/heads/side"})
	m.openCommands()
	palette := menuText(m)
	gitWriteSettle(t, m, cmd)
	if menuText(m) != palette || m.notice.text != "Review required · Git" {
		t.Fatalf("palette replaced:\n%s", menuText(m))
	}
	if !strings.Contains(gitSurfaceText(m), "Review · leave 3 commits and switch") {
		t.Fatalf("no review action:\n%s", gitSurfaceText(m))
	}
	_, c := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	gitWriteSettle(t, m, c)
	if !strings.Contains(menuText(m), "Leaving 3 commits") {
		t.Fatalf("queued dialog did not open:\n%s", menuText(m))
	}
	// Cancelling drops the dialog state.
	_, c = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	gitWriteSettle(t, m, c)
	if len(m.menu) != 0 || m.gitR.ack != nil {
		t.Fatal("dialog state retained after cancel")
	}
}

func TestGitAckForHiddenTargetOffersReview(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Entries = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	key, _ := m.gitTarget()
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "leaves_commits", Message: "3 commits"}
	}
	cmd := m.activate(action{Kind: "git-switch", ID: "refs/heads/side"})
	active := m.state.Active
	m.state.Active = m.snapshot.Threads[1-activeThreadIndex(m)].ID
	gitWriteSettle(t, m, cmd)
	if len(m.menu) != 0 || m.gitWriteFor(key) == nil || m.gitWriteFor(key).ack == nil {
		t.Fatalf("hidden-target ack: menu %d", len(m.menu))
	}
	m.state.Active = active
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if !strings.Contains(gitSurfaceText(m), "Review · leave 3 commits and switch") {
		t.Fatalf("review missing:\n%s", gitSurfaceText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-ack-open"}))
	if !strings.Contains(menuText(m), "Leaving 3 commits") {
		t.Fatalf("review dialog:\n%s", menuText(m))
	}
}

func TestGitFinalAckResultOffersReview(t *testing.T) {
	m, api := refModel(t, 120, 60)
	api.status.Entries = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "leaves_commits"})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-switch", ID: "refs/heads/side"}))
	h, ok := findHit(m.measure(), "git:review")
	if !ok {
		t.Fatalf("no review:\n%s", gitSurfaceText(m))
	}
	n := len(api.sent())
	_, c := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, c)
	if len(api.sent()) != n+1 || lastSent(t, api).Git.Ref.Branch != "side" {
		t.Fatalf("review did not restart the switch: %d", len(api.sent()))
	}
}
