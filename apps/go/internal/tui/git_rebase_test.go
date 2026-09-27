package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (g *fakeGit) GitRebasePlan(_ context.Context, t client.GitTarget, base, onto string) (protocol.GitRebasePlan, error) {
	g.record("rebase-plan:" + base + ":" + onto)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rbPlan, g.err
}

var (
	rb0 = strings.Repeat("0", 40) // base
	rb1 = strings.Repeat("1", 40)
	rb2 = strings.Repeat("2", 40)
	rb3 = strings.Repeat("3", 40)
	rb4 = strings.Repeat("4", 40) // HEAD
)

// rbModel is a Git surface on a four-commit linear branch with interactive
// rebase available.
func rbModel(t *testing.T) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := opModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	api.status.HeadOid = rb4
	api.status.Entries = nil
	commit := func(h, parent, subject string) protocol.GitCommit {
		c := protocol.GitCommit{Hash: h, Short: h[:7], Subject: subject}
		if parent != "" {
			c.Parents = []string{parent}
		}
		return c
	}
	api.log = protocol.GitLog{Workspace: protocol.WorkspaceInfo{State: "branch"}, Commits: []protocol.GitCommit{
		commit(rb4, rb3, "Four"), commit(rb3, rb2, "Three"), commit(rb2, rb1, "Two"), commit(rb1, rb0, "One"), commit(rb0, "", "Base"),
	}}
	api.log.Commits[0].Refs = []string{"HEAD", "refs/heads/feature/init"}
	api.rbPlan = protocol.GitRebasePlan{Branch: "feature/init", HeadOid: rb4, Base: rb0, BaseOid: rb0, BaseLabel: "0000000 Base", Fingerprint: "fp-1",
		Commits: []protocol.GitRebasePlanCommit{
			{Oid: rb1, Parents: []string{rb0}, Subject: "One", Body: "Body one", Message: "One\n\nBody one\n"}, {Oid: rb2, Parents: []string{rb1}, Subject: "Two", Message: "Two\n"},
			{Oid: rb3, Parents: []string{rb2}, Subject: "Three \x1b[31mred", Published: true, Message: "Three \x1b[31mred\n"}, {Oid: rb4, Parents: []string{rb3}, Subject: "Four", Message: "Four\n"},
		}, Published: true, UpdateRefs: []protocol.GitRebaseUpdateRef{{Ref: "refs/heads/stack", Oid: rb2}}}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	return m, api
}

func rbDraft(t *testing.T, m *Model) *gitRebaseDraft {
	t.Helper()
	key, _ := m.gitTarget()
	d := m.gitRebaseDraftFor(key)
	if d == nil {
		t.Fatal("no draft")
	}
	return d
}

func rbActions(d *gitRebaseDraft) string {
	var out []string
	for _, e := range d.entries {
		s := e.Action
		if e.Commit != "" {
			s += ":" + e.Commit[:1]
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func rbType(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		gitWriteSettle(t, m, cmd)
	}
}

func TestGitRebaseCommitMenuPresets(t *testing.T) {
	m, api := rbModel(t)
	m.setFocus("git:commit:" + rb4)
	gitWriteSettle(t, m, m.openGitContextMenu("git:commit:"+rb4))
	text := menuText(m)
	for _, want := range []string{"Edit message of 4444444…", "Edit contents of 4444444…", "Squash 4444444 into parent…", "Fixup 4444444 into parent…",
		"Drop 4444444…", "Move 4444444 up · already the newest commit", "Move 4444444 down…", "Interactive rebase from 4444444…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("menu lacks %q:\n%s", want, text)
		}
	}
	m.closeContextMenu()
	m.menu = nil
	// Squash into parent reads the plan from the grandparent and applies the
	// preset, opening the combined message.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb2, Value: "squash:" + rb4}))
	if api.count("rebase-plan:"+rb2+":") != 1 {
		t.Fatalf("plan read %v", api.calls)
	}
	d := rbDraft(t, m)
	if rbActions(d) != "pick:1 pick:2 pick:3 squash:4" || m.focus != gitRebaseMsgKey {
		t.Fatalf("preset %s focus %s", rbActions(d), m.focus)
	}
	if v := m.gitRebaseMsg().Value(); !strings.Contains(v, "Three red") || !strings.Contains(v, "Four") || strings.Contains(v, "\x1b") {
		t.Fatalf("combined prefill %q", v)
	}
	if len(api.sent()) != 0 {
		t.Fatal("a preset must never act blind")
	}
}

func TestGitRebasePresetUnavailable(t *testing.T) {
	m, api := rbModel(t)
	api.log.Commits[1].Parents = []string{rb2, strings.Repeat("9", 40)} // rb3 is a merge
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	g := m.currentGitView()
	c, _ := g.commit(rb3)
	text := menuText(&Model{menu: m.gitRebaseMenuItems(g, c)})
	if !strings.Contains(text, "Drop 3333333 · merge commits are not rewritten here") {
		t.Fatalf("merge preset:\n%s", text)
	}
	c4, _ := g.commit(rb4)
	if text := menuText(&Model{menu: m.gitRebaseMenuItems(g, c4)}); !strings.Contains(text, "Squash 4444444 into parent · the parent is a merge commit") {
		t.Fatalf("merge parent:\n%s", text)
	}
	// Capability fallback.
	m.snapshot.Capabilities = []string{"git-writes", "git-history", "git-refs", "git-operations"}
	if text := menuText(&Model{menu: m.gitRebaseMenuItems(g, c4)}); text != "Interactive rebase needs a newer server" {
		t.Fatalf("fallback %q", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	if key, _ := m.gitTarget(); m.gitRebaseDraftFor(key) != nil {
		t.Fatal("editor opened without the capability")
	}
}

func TestGitRebaseEditorReorderSquashRewordAndStart(t *testing.T) {
	m, api := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	if !d.open || m.focus != gitRebaseRowKey(0) {
		t.Fatalf("editor focus %q", m.focus)
	}
	text := gitSurfaceText(m)
	// Newest first; untrusted subjects sanitized.
	if i4, i1 := strings.Index(text, "pick 4444444 Four"), strings.Index(text, "pick 1111111 One"); i4 < 0 || i1 < i4 || strings.Contains(text, "\x1b") {
		t.Fatalf("editor rows:\n%s", text)
	}
	// Move Two up (display) past Three.
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "K")
	if rbActions(d) != "pick:1 pick:3 pick:2 pick:4" || m.focus != gitRebaseRowKey(2) {
		t.Fatalf("move up %s focus %s", rbActions(d), m.focus)
	}
	// Squash Four into Two (the row below it), with a combined message.
	m.setFocus(gitRebaseRowKey(3))
	gitKey(t, m, "s")
	if d.entries[3].Action != protocol.GitRebaseSquash {
		t.Fatalf("squash %s", rbActions(d))
	}
	gitKey(t, m, "m")
	if m.focus != gitRebaseMsgKey || !strings.Contains(m.gitRebaseMsg().Value(), "Two\n\nFour") {
		t.Fatalf("combiner %q", m.gitRebaseMsg().Value())
	}
	m.gitRebaseMsg().SetValue("Two and four")
	gitKey(t, m, "enter")
	// Reword One.
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	if m.focus != gitRebaseMsgKey || m.gitRebaseMsg().Value() != "One\n\nBody one\n" {
		t.Fatalf("reword prefill %q", m.gitRebaseMsg().Value())
	}
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "  ")
	gitKey(t, m, "enter") // blank: refused, the editor stays
	if m.focus != gitRebaseMsgKey {
		t.Fatal("a blank reword message was accepted")
	}
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "First")
	gitKey(t, m, "enter")
	if d.entries[0].Message != "First" {
		t.Fatalf("reword %+v", d.entries[0])
	}
	built := d.build()
	if built[3].Message != "Two and four" || built[2].Message != "" || protocol.ValidateRebasePlan(d.plan, built) != nil {
		t.Fatalf("built %+v", built)
	}
	// Published needs its acknowledgement before starting.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if len(m.menu) != 0 {
		t.Fatalf("started without the published acknowledgement:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "update-refs"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	text = menuText(m)
	for _, want := range []string{"Rewrite feature/init at 4444444?", "Base 0000000 Base · onto 0000000 Base", "4 commits replayed", "  1 moved", "  1 reworded", "  1 squashed or fixed up",
		"Rewrites commits already on a remote", "Also moves stack"} {
		if !strings.Contains(text, want) {
			t.Fatalf("confirmation lacks %q:\n%s", want, text)
		}
	}
	if m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatal("Cancel must be focused first")
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	in := s.Git.Integrate
	if s.Kind != protocol.GitKindRebase || in.Interactive == nil || !in.AcknowledgePublished || !in.Interactive.UpdateRefs || in.Interactive.Fingerprint != "fp-1" ||
		in.ExpectedHead != rb4 || len(in.Interactive.Entries) != 4 || in.Interactive.Entries[3].Message != "Two and four" {
		t.Fatalf("sent %+v %+v", in, in.Interactive)
	}
	// Accepted: the draft ends and the status body returns.
	if key, _ := m.gitTarget(); m.gitRebaseDraftFor(key) != nil {
		t.Fatal("draft kept after the server accepted the start")
	}
}

func TestGitRebaseValidationInline(t *testing.T) {
	m, api := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "s") // squash the oldest: nothing below to meld into
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Cannot start · entry 1: squash needs a pick, reword, edit, squash or fixup directly before it") {
		t.Fatalf("validation:\n%s", text)
	}
	if line, _ := gitRowLine(t, m, gitRebaseRowKey(0)); !strings.Contains(line, "squash") || !strings.Contains(line, "!") {
		t.Fatalf("invalid row not marked: %q", line)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if len(m.menu) != 0 || len(api.sent()) != 0 {
		t.Fatal("an invalid plan must not reach the confirmation")
	}
	// fixup cycles through -C and -c; -c needs the chain message.
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "p")
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "p")
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "f")
	gitKey(t, m, "f")
	gitKey(t, m, "f")
	d := rbDraft(t, m)
	if e := d.entries[1]; e.Action != "fixup" || e.Fixup != "c" || d.build()[1].Message == "" {
		t.Fatalf("fixup -c %+v", d.build()[1])
	}
	// Break insert and removal.
	gitKey(t, m, "b")
	if rbActions(d) != "pick:1 fixup:2 break pick:3 pick:4" {
		t.Fatalf("break %s", rbActions(d))
	}
	gitKey(t, m, "d")
	if rbActions(d) != "pick:1 fixup:2 pick:3 pick:4" {
		t.Fatalf("unbreak %s", rbActions(d))
	}
	// Edit toggles contents -> amend -> pick.
	m.setFocus(gitRebaseRowKey(3))
	gitKey(t, m, "e")
	gitKey(t, m, "e")
	if e := d.entries[3]; e.Action != "edit" || e.EditMode != "amend" {
		t.Fatalf("amend %+v", e)
	}
}

func TestGitRebaseDragReorders(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	f := m.measure()
	var grip hit
	for _, h := range f.hits {
		if h.Action.Kind == "git-rb-grip" && h.Action.Index == 0 {
			grip = h
		}
	}
	target, ok := findHit(f, gitRebaseRowKey(3))
	if grip.Key == "" || !ok {
		t.Fatal("grip or target row missing")
	}
	m.Update(tea.MouseClickMsg{X: grip.Rect.X, Y: grip.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: target.Rect.X + 20, Y: target.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: target.Rect.X + 20, Y: target.Rect.Y, Button: tea.MouseLeft})
	d := rbDraft(t, m)
	if rbActions(d) != "pick:2 pick:3 pick:4 pick:1" || m.gitRB.dragging {
		t.Fatalf("drag %s dragging %v", rbActions(d), m.gitRB.dragging)
	}
}

func TestGitRebaseDraftKeptAndDiscardConfirmed(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "drop:" + rb2}))
	gitKey(t, m, "esc") // back to status keeps the draft
	d := rbDraft(t, m)
	if d.open || !strings.Contains(gitSurfaceText(m), "Rebase plan kept") {
		t.Fatalf("back:\n%s", gitSurfaceText(m))
	}
	// Hiding the surface and showing it again keeps it too.
	m.activate(action{Kind: "toggle-right"})
	m.activate(action{Kind: "toggle-right"})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-show"}))
	if !d.open || rbActions(rbDraft(t, m)) != "pick:1 drop:2 pick:3 pick:4" {
		t.Fatal("draft lost")
	}
	// Opening another plan while edited asks first.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	if !strings.Contains(menuText(m), "Discard it and open the new plan") || rbDraft(t, m) != d {
		t.Fatalf("replace:\n%s", menuText(m))
	}
	m.menu = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-discard"}))
	if !strings.Contains(menuText(m), "Discard this rebase plan?") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("discard:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-discard-confirm"}))
	if key, _ := m.gitTarget(); m.gitRebaseDraftFor(key) != nil {
		t.Fatal("discard kept the draft")
	}
}

func TestGitRebaseStaleRefreshKeepsEditsAndRefusal(t *testing.T) {
	m, api := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "drop:" + rb2}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	// The server refuses the start as stale: the edits stay.
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "stale_plan", Message: "HEAD \x1b[2Jmoved"}
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-confirm"}))
	d := rbDraft(t, m)
	if !d.open || !d.stale || !strings.Contains(d.err, "HEAD moved") || strings.Contains(d.err, "\x1b") || rbActions(d) != "pick:1 drop:2 pick:3 pick:4" {
		t.Fatalf("refusal open=%v stale=%v err=%q %s", d.open, d.stale, d.err, rbActions(d))
	}
	// Refresh: a new commit on top is picked, the drop is kept.
	rb5 := strings.Repeat("5", 40)
	api.rbPlan.Commits = append(api.rbPlan.Commits, protocol.GitRebasePlanCommit{Oid: rb5, Subject: "Five"})
	api.rbPlan.HeadOid, api.rbPlan.Fingerprint = rb5, "fp-2"
	api.status.HeadOid = rb5
	api.reply = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-refresh"}))
	if rbActions(d) != "pick:1 drop:2 pick:3 pick:4 pick:5" || d.stale || !strings.Contains(d.notice, "picked new 5555555") {
		t.Fatalf("refresh %s stale=%v notice %q", rbActions(d), d.stale, d.notice)
	}
}

func interactiveStop(stop string) protocol.GitOperationState {
	st := rebaseStop()
	st.Source, st.Conflicts, st.Branch = "application", nil, "feature/init"
	st.Can.Continue = protocol.GitOperationAction{Allowed: true}
	st.DiscardsOnAbort, st.BackupMissingOnAbort, st.DiscardsOnSkip = nil, nil, nil
	st.Interactive = &protocol.GitRebaseProgress{Plan: true, Stop: stop, Done: 2, Remaining: 3, Command: "edit", CommandOid: rb2, Staged: true,
		Failure: protocol.GitRebaseHookRejected, Hook: "pre-commit", Detail: "lint \x1b[31mfailed", UpdateRefs: []protocol.GitRebaseUpdateRef{{Ref: "refs/heads/stack"}}}
	return st
}

func TestGitRebaseStopRenderingAndCommit(t *testing.T) {
	m, api := opStopped(t, interactiveStop(protocol.GitRebaseStopEditReset))
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	text := gitSurfaceText(m)
	for _, want := range []string{"2 of 5 · edit 2222222", "Stopped to edit ccccccc", "pre-commit refused the commit", "lint failed",
		"Moves when finished: stack", "Continue with message…", "Commit staged…", "Skip commit…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stop lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "continue in a terminal") {
		t.Fatalf("stop text:\n%q", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-stop-commit"}))
	if m.focus != gitRebaseMsgKey || !strings.Contains(gitSurfaceText(m), "COMMIT STAGED CHANGES") && !strings.Contains(gitSurfaceText(m), "Commit staged changes") {
		t.Fatalf("commit editor focus %q:\n%s", m.focus, gitSurfaceText(m))
	}
	gitKey(t, m, "enter") // empty: refused
	if len(m.menu) != 0 {
		t.Fatal("empty commit message reached the confirmation")
	}
	rbType(t, m, "Part one")
	gitKey(t, m, "enter")
	if !strings.Contains(menuText(m), "Commit the staged changes on feature/init as a new commit?") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("commit confirm:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-commit-confirm"}))
	s := lastSent(t, api)
	o := s.Git.Operation
	if s.Kind != protocol.GitKindOperationCommit || o.Message != "Part one" || o.StagedFingerprint != "st-fp" || o.ExpectedStep != 2 || !o.Confirmed {
		t.Fatalf("operation_commit %+v", o)
	}
}

func TestGitRebaseStopContinueWithMessage(t *testing.T) {
	m, api := opStopped(t, interactiveStop(protocol.GitRebaseStopMessage))
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	if strings.Contains(gitSurfaceText(m), "Commit staged…") {
		t.Fatal("Commit staged offered at a message stop")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-stop-continue"}))
	rbType(t, m, "Better")
	gitKey(t, m, "enter")
	if !strings.Contains(menuText(m), "with your message: Better") {
		t.Fatalf("continue confirm:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}))
	s := lastSent(t, api)
	if s.Kind != protocol.GitKindOperationContinue || s.Git.Operation.Message != "Better" {
		t.Fatalf("continue %+v", s.Git.Operation)
	}
}

func TestGitRebaseAgentPlanEntryPoint(t *testing.T) {
	m, api := rbModel(t)
	entries := []protocol.GitRebaseEntry{{Action: "pick", Commit: rb1}, {Action: "squash", Commit: rb2, Message: "One and two"},
		{Action: "reword", Commit: rb3, Message: "Three"}, {Action: "drop", Commit: rb4}}
	gitWriteSettle(t, m, m.openGitRebaseAgentPlan(rb0, "", entries))
	d := rbDraft(t, m)
	if got := d.build(); got[1].Message != "One and two" || got[2].Message != "Three" || protocol.ValidateRebasePlan(d.plan, got) != nil {
		t.Fatalf("agent plan %+v", got)
	}
	if !strings.Contains(d.notice, "nothing runs until you start it") || len(api.sent()) != 0 {
		t.Fatal("agent plan must wait for review")
	}
	// Upstream entry point: base is the upstream's full ref.
	m2, api2 := rbModel(t)
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-rb-upstream"}))
	if api2.count("rebase-plan:upstream:") != 1 {
		t.Fatalf("upstream base %v", api2.calls)
	}
}

func TestGitRebaseStopVariants(t *testing.T) {
	st := interactiveStop(protocol.GitRebaseStopEmpty)
	st.Interactive.Failure = ""
	m, _ := opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Keep empty commit…") || !strings.Contains(text, "Skip (drop) commit…") || strings.Contains(text, "Continue with message") {
		t.Fatalf("empty stop:\n%s", text)
	}
	// Inside a squash chain Continue takes no message.
	st = interactiveStop(protocol.GitRebaseStopConflict)
	st.Interactive.Command = "squash"
	if gitRebaseMessageApplies(st.Interactive) {
		t.Fatal("message offered inside a chain")
	}
	st.Interactive.Command = "pick"
	if !gitRebaseMessageApplies(st.Interactive) {
		t.Fatal("message not offered for a pick")
	}
	// Abort lists commits made at stops and reports the backup ref.
	st = interactiveStop(protocol.GitRebaseStopBreak)
	st.AbortDropsCommits = []protocol.GitOperationCommit{{Oid: rb3, Subject: "Split \x1b[2Jpart"}}
	st.AbortDropsCount, st.AbortDropsFingerprint = 1, "ad-fp"
	m, api := opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	if text := menuText(m); !strings.Contains(text, "made at stops (a backup ref keeps them)") || !strings.Contains(text, "3333333 Split part") {
		t.Fatalf("abort:\n%s", text)
	}
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, State: protocol.GitStateSucceeded, Git: &protocol.GitResult{State: protocol.GitStateSucceeded,
			Operation: &protocol.GitOperationResult{Kind: "rebase", Outcome: protocol.GitOutcomeAborted, BackupRef: "refs/tui-go/rebase-backup/op1"}}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
	if o := lastSent(t, api).Git.Operation; o.AcknowledgeDropped != "ad-fp" {
		t.Fatalf("abort ack %+v", o)
	}
	if text := gitSurfaceText(m); !strings.Contains(text, "git log refs/tui-go/rebase-backup/op1") {
		t.Fatalf("abort result:\n%s", text)
	}
}

func TestGitRebaseUpdateRefsUnsupported(t *testing.T) {
	m, api := rbModel(t)
	api.rbPlan.UpdateRefsUnsupported = []string{"refs/heads/bad"}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "update-refs"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Cannot be moved here: bad") || !strings.Contains(text, "Cannot start · Some branches cannot be moved here") {
		t.Fatalf("unsupported:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "update-refs"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if !strings.Contains(menuText(m), "Left in place (cannot be moved here): 1 branch") {
		t.Fatalf("confirm:\n%s", menuText(m))
	}
}

func TestGitRebasePlanReadRetry(t *testing.T) {
	m, api := rbModel(t)
	api.mu.Lock()
	api.err = errors.New("timeout")
	api.mu.Unlock()
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-onto", ID: rb0}))
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	if text := gitSurfaceText(m); !strings.Contains(text, "Retry reading the plan") {
		t.Fatalf("no retry:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-back"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-onto", ID: rb0}))
	if d := rbDraft(t, m); !d.loaded || d.err != "" || api.count("rebase-plan:"+rb0+":") != 2 {
		t.Fatalf("reopen did not re-read: loaded=%v err=%q", d.loaded, d.err)
	}
}

func TestGitRebaseUneditedRewordIsPick(t *testing.T) {
	m, api := rbModel(t)
	raw := "One line a\nOne line b\n\n\tBody\x07 with bell\r\n"
	api.rbPlan.Commits[0].Subject, api.rbPlan.Commits[0].Body, api.rbPlan.Commits[0].Message = "One line a", "One line b\n\n\tBody with bell", raw
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(0))
	for _, finish := range []string{"esc", "enter"} {
		gitKey(t, m, "r")
		if !strings.Contains(gitSurfaceText(m), "cannot keep") {
			t.Fatalf("no warning for unkeepable bytes:\n%s", gitSurfaceText(m))
		}
		if finish == "esc" {
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			gitWriteSettle(t, m, cmd)
		} else {
			gitKey(t, m, "enter")
		}
		if e := d.build()[0]; e.Action != "pick" || e.Message != "" || d.edited() {
			t.Fatalf("%s: unedited reword sent %+v edited=%v", finish, e, d.edited())
		}
	}
	// Squashing into it sends the exact original messages, joined.
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	if got := d.build()[1].Message; got != strings.TrimRight(raw, "\n")+"\n\nTwo" {
		t.Fatalf("combined %q", got)
	}
}

func TestGitRebaseMissingOriginalNeedsMessage(t *testing.T) {
	m, api := rbModel(t)
	api.rbPlan.Commits[0].Message = "" // older server
	api.rbPlan.Commits[1].MessageTruncated = true
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	if v := m.gitRebaseMsg().Value(); v != "" {
		t.Fatalf("missing original prefilled %q", v)
	}
	gitKey(t, m, "enter")
	if m.focus != gitRebaseMsgKey || !strings.Contains(gitSurfaceText(m), "write the full message") {
		t.Fatalf("empty message accepted, focus %s", m.focus)
	}
	gitKey(t, m, "esc")
	// A truncated original is never prefilled; the chain editor opens empty.
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	if p := m.gitRebaseProblem(d); !strings.Contains(p, "Write the combined message") {
		t.Fatalf("problem %q", p)
	}
	gitKey(t, m, "m")
	if v := m.gitRebaseMsg().Value(); v != "" {
		t.Fatalf("chain with a cut original prefilled %q", v)
	}
	rbType(t, m, "Mine")
	gitKey(t, m, "enter")
	if got := d.build()[1].Message; got != "Mine" {
		t.Fatalf("chain sent %q", got)
	}
	// Undoing and redoing the squash brings the written message back.
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "p")
	gitKey(t, m, "s")
	if got := d.build()[1].Message; got != "Mine" {
		t.Fatalf("written message lost: %q", got)
	}
}

func TestGitRebaseChainMessageNotReused(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	gitKey(t, m, "m")
	m.gitRebaseMsg().SetValue("One plus Two combined")
	gitKey(t, m, "enter")
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "p")
	if d.edited() {
		t.Fatal("edited after reverting the squash")
	}
	m.setFocus(gitRebaseRowKey(3))
	gitKey(t, m, "J")
	gitKey(t, m, "J")
	i := d.entryIndex(rb4)
	m.setFocus(gitRebaseRowKey(i))
	gitKey(t, m, "s")
	if msg := d.build()[i].Message; strings.Contains(msg, "combined") {
		t.Fatalf("stale combined message reused: %q", msg)
	}
}

func TestGitRebaseDragNeedsHeldButton(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	f := m.measure()
	var grip hit
	for _, h := range f.hits {
		if h.Action.Kind == "git-rb-grip" && h.Action.Index == 0 {
			grip = h
		}
	}
	target, _ := findHit(f, gitRebaseRowKey(3))
	m.Update(tea.MouseClickMsg{X: grip.Rect.X, Y: grip.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: target.Rect.X + 20, Y: target.Rect.Y})
	if d := rbDraft(t, m); rbActions(d) != "pick:1 pick:2 pick:3 pick:4" || m.gitRB.dragging {
		t.Fatalf("buttonless motion reordered: %s", rbActions(d))
	}
}

func TestGitRebaseRefreshKeepsChains(t *testing.T) {
	m, api := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(2))
	gitKey(t, m, "f") // Three fixes up Two
	// A new commit appears between Two and Three in the fresh read.
	rb5 := strings.Repeat("5", 40)
	c := api.rbPlan.Commits
	api.rbPlan.Commits = append([]protocol.GitRebasePlanCommit{c[0], c[1], {Oid: rb5, Subject: "Five", Message: "Five\n"}}, c[2:]...)
	api.rbPlan.Fingerprint = "fp-3"
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-refresh"}))
	if rbActions(d) != "pick:1 pick:2 fixup:3 pick:5 pick:4" {
		t.Fatalf("refresh split the chain: %s", rbActions(d))
	}
}

func TestGitRebaseNarrowAndHostileText(t *testing.T) {
	m, api := rbModel(t)
	api.rbPlan.Branch = "feat\x1b]0;pwn\x07/\u202eevil"
	api.rbPlan.Commits[1].Subject = "宽字符宽字符宽字符宽字符宽字符宽字符宽字符 👩‍👩‍👧 \x1b[2J"
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	for _, w := range []int{30, 40, 60} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		out := m.View().Content
		if strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\u202e") {
			t.Fatalf("unsafe output at width %d", w)
		}
	}
}

func TestGitRebaseStopCopy(t *testing.T) {
	st := interactiveStop(protocol.GitRebaseStopEmpty)
	st.Interactive.Failure, st.Interactive.StepCommitted, st.Interactive.Late = "", true, true
	m, _ := opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	text := gitSurfaceText(m)
	if strings.Contains(text, "Keep empty commit") || !strings.Contains(text, "already made") || !strings.Contains(text, "wasn't observed by the server") {
		t.Fatalf("committed empty stop:\n%s", text)
	}
	for stop, want := range map[string]string{protocol.GitRebaseStopBreak: "Resumes the rebase", protocol.GitRebaseStopMessage: "Retries the message step",
		protocol.GitRebaseStopEditReset: "original message and author"} {
		if got := gitRebaseContinueCopy(interactiveStop(stop)); !strings.Contains(got, want) {
			t.Fatalf("%s: %q", stop, got)
		}
	}
}

func TestGitRebaseStopMessagePrefill(t *testing.T) {
	st := interactiveStop(protocol.GitRebaseStopEditReset)
	st.Interactive.StepMessage, st.Interactive.Next = "Split\tme\n", "pick "+rb3
	m, api := opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	if !strings.Contains(gitSurfaceText(m), "Next: pick 3333333") {
		t.Fatalf("next:\n%s", gitSurfaceText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-stop-commit"}))
	if !strings.Contains(gitSurfaceText(m), "cannot keep") {
		t.Fatal("no warning for a tab")
	}
	gitKey(t, m, "enter") // unchanged: the exact step message is sent
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-commit-confirm"}))
	if got := lastSent(t, api).Git.Operation.Message; got != "Split\tme\n" {
		t.Fatalf("commit message %q", got)
	}
	st.Interactive.StepMessageTruncated = true
	m, _ = opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-stop-commit"}))
	if m.gitRebaseMsg().Value() != "" || !strings.Contains(gitSurfaceText(m), "longer than 64 KiB") {
		t.Fatalf("cut step message prefilled %q", m.gitRebaseMsg().Value())
	}
	gitKey(t, m, "enter")
	if len(m.menu) != 0 || m.focus != gitRebaseMsgKey {
		t.Fatal("an empty commit message was accepted")
	}
}

func TestGitRebaseServerEditAndBackupRefs(t *testing.T) {
	st := interactiveStop(protocol.GitRebaseStopEditReset)
	st.Interactive.ServerEdit, st.Interactive.Failure = true, ""
	m, _ := opStopped(t, st)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-rebase-interactive")
	if !strings.Contains(gitSurfaceText(m), "Stopped to edit after resolving the conflict") {
		t.Fatalf("server edit:\n%s", gitSurfaceText(m))
	}
	c := interactiveStop(protocol.GitRebaseStopConflict)
	if got := gitRebaseContinueCopy(c); !strings.Contains(got, "stops at this edit step") {
		t.Fatalf("conflict on edit: %q", got)
	}
	st = interactiveStop(protocol.GitRebaseStopBreak)
	st.Interactive.Failure = ""
	m, api := opStopped(t, st)
	var refs []string
	for i := range 7 {
		refs = append(refs, "refs/tui-go/rebase-backup/op1-"+strconv.Itoa(i))
	}
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{ID: cmd.ID, State: protocol.GitStateSucceeded, Git: &protocol.GitResult{State: protocol.GitStateSucceeded,
			Operation: &protocol.GitOperationResult{Kind: "rebase", Outcome: protocol.GitOutcomeAborted, BackupRef: "refs/tui-go/rebase-backup/op1", BackupRefs: refs}}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "op1-4") || strings.Contains(text, "op1-5") || !strings.Contains(text, "and 2 more") {
		t.Fatalf("backup refs:\n%s", text)
	}
}

func rbChain(t *testing.T) (*Model, *gitRebaseDraft) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s") // Two squashes into One
	gitKey(t, m, "m")
	m.gitRebaseMsg().SetValue("MY COMBINED")
	gitKey(t, m, "enter")
	if d.build()[1].Message != "MY COMBINED" {
		t.Fatalf("setup %q", d.build()[1].Message)
	}
	return m, d
}

// Moving an unrelated commit one step at a time past the chain.
func TestGitRebaseRMovePastChainLosesMessage(t *testing.T) {
	m, d := rbChain(t)
	m.setFocus(gitRebaseRowKey(3)) // Four
	gitKey(t, m, "J")
	gitKey(t, m, "J") // Four now between One and Two?
	t.Logf("mid %s", rbActions(d))
	gitKey(t, m, "J")
	t.Logf("end %s", rbActions(d))
	i := d.entryIndex(rb2)
	if got := d.build()[i].Message; got != "MY COMBINED" {
		t.Errorf("chain One+Two restored but its message is %q", got)
	}
}

// r then Esc on the squash member.
func TestGitRebaseRRewordCancelLosesMessage(t *testing.T) {
	m, d := rbChain(t)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "r")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	gitWriteSettle(t, m, cmd)
	t.Logf("after cancel %s", rbActions(d))
	if got := d.build()[1].Message; got != "MY COMBINED" {
		t.Errorf("cancelled reword dropped the combined message: %q", got)
	}
}

// Editing the chain start (pick -> edit) drops the combined message.
func TestGitRebaseREditStartLosesMessage(t *testing.T) {
	m, d := rbChain(t)
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "e")
	if got := d.build()[1].Message; got != "MY COMBINED" {
		t.Errorf("edit on chain start dropped the combined message: %q", got)
	}
}

// Drag of a row through the chain.
func TestGitRebaseRDragThroughChain(t *testing.T) {
	m, d := rbChain(t)
	gitKey(t, m, "esc")
	d.open = true
	f := m.measure()
	var grip hit
	for _, h := range f.hits {
		if h.Action.Kind == "git-rb-grip" && h.Action.Index == 3 {
			grip = h
		}
	}
	r1, _ := findHit(f, gitRebaseRowKey(1))
	r0, _ := findHit(f, gitRebaseRowKey(0))
	m.Update(tea.MouseClickMsg{X: grip.Rect.X, Y: grip.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: r1.Rect.X + 20, Y: r1.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: r0.Rect.X + 20, Y: r0.Rect.Y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: r0.Rect.X + 20, Y: r0.Rect.Y, Button: tea.MouseLeft})
	t.Logf("after drag %s", rbActions(d))
	i := d.entryIndex(rb2)
	if got := d.build()[i].Message; got != "MY COMBINED" {
		t.Errorf("drag through chain dropped message: %q", got)
	}
}

// Opening a message editor while another target's editor holds the textarea.
func TestGitRebaseROtherTargetEditCorrupted(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	other := &gitRebaseMsgEdit{key: "other-target", purpose: "reword", text: "OTHER TYPED"}
	if m.gitRB.edits == nil {
		m.gitRB.edits = map[string]*gitRebaseMsgEdit{}
	}
	m.gitRB.edits["other-target"] = other
	m.gitRB.msgFor = other
	m.gitRebaseMsg().SetValue("OTHER TYPED")
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	if other.text != "OTHER TYPED" {
		t.Errorf("other target's unsaved text overwritten: %q", other.text)
	}
}

// Exact raw bytes in an unedited chain; edited chain sent as shown.
func TestGitRebaseRChainExact(t *testing.T) {
	m, api := rbModel(t)
	api.rbPlan.Commits[0].Message = "One\r\n\n\tcode\x1b[1m\n"
	api.rbPlan.Commits[1].Message = "Two\n\n# hash line\n\n"
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	gitKey(t, m, "m")
	shown := m.gitRebaseMsg().Value()
	gitKey(t, m, "enter")
	t.Logf("shown %q sent %q", shown, d.build()[1].Message)
	if d.build()[1].Message != "One\r\n\n\tcode\x1b[1m\n\nTwo\n\n# hash line" {
		t.Errorf("unexpected")
	}
}

func TestGitRebaseRThreadSwitchCorrupts(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	keyA, _ := m.gitTarget()
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "TYPED IN A")
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != m.state.Active {
			other = th.ID
			break
		}
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: other}))
	keyB, _ := m.gitTarget()
	t.Logf("A=%s B=%s", keyA, keyB)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "reword:" + rb2}))
	t.Logf("B edit open: %v", m.gitRB.edits[keyB] != nil)
	if e := m.gitRB.edits[keyA]; e == nil || e.text != "TYPED IN A" {
		t.Errorf("thread A's unsaved message now %q", e.text)
	}
}

func TestGitRebaseRewordTextKept(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "Written")
	gitKey(t, m, "enter")
	gitKey(t, m, "p")
	if d.build()[0].Message != "" {
		t.Fatal("pick carries a message")
	}
	gitKey(t, m, "r")
	if v := m.gitRebaseMsg().Value(); v != "Written" {
		t.Fatalf("reword text not restored: %q", v)
	}
}

func TestGitRebaseStoredMessageKeptExactly(t *testing.T) {
	m, _ := rbModel(t)
	chain := "One and two\n\n\tindented\r\n"
	rew := "Three\n\n\tcode"
	entries := []protocol.GitRebaseEntry{{Action: "pick", Commit: rb1}, {Action: "squash", Commit: rb2, Message: chain},
		{Action: "reword", Commit: rb3, Message: rew}, {Action: "pick", Commit: rb4}}
	gitWriteSettle(t, m, m.openGitRebaseAgentPlan(rb0, "", entries))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "m")
	if !strings.Contains(gitSurfaceText(m), "cannot keep") {
		t.Fatal("no warning for a stored message the editor cannot show")
	}
	gitKey(t, m, "enter")
	m.setFocus(gitRebaseRowKey(2))
	gitKey(t, m, "m")
	gitKey(t, m, "enter")
	if b := d.build(); b[1].Message != chain || b[2].Message != rew || b[2].Action != "reword" {
		t.Fatalf("unchanged save altered stored messages: %+v", b)
	}
}

func TestGitRebaseRewordUnderWrittenChain(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	gitKey(t, m, "m")
	m.gitRebaseMsg().SetValue("MY COMBINED")
	gitKey(t, m, "enter")
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "A NEW")
	gitKey(t, m, "enter")
	if !d.rewordReplaced(0) || !strings.Contains(gitSurfaceText(m), "replaced by its squash's written combined message") {
		t.Fatalf("replaced reword not flagged:\n%s", gitSurfaceText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-toggle", ID: "published"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	text := menuText(m)
	if strings.Contains(text, "reworded") || !strings.Contains(text, "1 reword replaced by a written combined message") ||
		!strings.Contains(text, "Messages: 1 written, 0 combined by default") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("confirmation:\n%s", text)
	}
	// Without a written message the default combination uses the reword.
	m.menu = nil
	delete(d.chainMsg, gitRebaseChainKey(d.entries, 0, 1))
	if got := d.build()[1].Message; !strings.HasPrefix(got, "A NEW") {
		t.Fatalf("default combination ignores the reword: %q", got)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-start"}))
	if !strings.Contains(menuText(m), "Review combined messages…") {
		t.Fatalf("no review item:\n%s", menuText(m))
	}
}

func TestGitRebaseRestoredChainMessageVisible(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	d := rbDraft(t, m)
	m.setFocus(gitRebaseRowKey(1))
	gitKey(t, m, "s")
	gitKey(t, m, "m")
	m.gitRebaseMsg().SetValue("OLD WRITTEN")
	gitKey(t, m, "enter")
	gitKey(t, m, "p")
	if d.edited() {
		t.Fatal("edited after abandoning the chain")
	}
	gitKey(t, m, "s")
	if line, _ := gitRowLine(t, m, gitRebaseRowKey(1)); !strings.Contains(line, "✎") || d.build()[1].Message != "OLD WRITTEN" {
		t.Fatalf("restored message not shown: %q", line)
	}
}

// Stop message across thread switch.
func TestGitRebaseMessageAcrossThreadSwitch(t *testing.T) {
	m, _ := rbModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "from:" + rb1}))
	keyA, _ := m.gitTarget()
	m.setFocus(gitRebaseRowKey(0))
	gitKey(t, m, "r")
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "TYPED A")
	// hide surface & come back
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != m.state.Active {
			other = th.ID
		}
	}
	cur := m.state.Active
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: other}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-rb-open", ID: rb0, Value: "reword:" + rb2}))
	t.Logf("focus in B %s", m.focus)
	m.setFocus(gitRebaseMsgKey)
	m.gitRebaseMsg().SetValue("")
	rbType(t, m, "TYPED B")
	gitWriteSettle(t, m, m.activate(action{Kind: "thread", ID: cur}))
	t.Logf("focus after switch back %s", m.focus)
	m.setFocus(gitRebaseMsgKey)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	gitWriteSettle(t, m, cmd)
	if e := m.gitRB.edits[keyA]; e == nil || e.text != "TYPED Ax" {
		t.Errorf("A text %q (focus %s)", e.text, m.focus)
	}
	t.Logf("textarea %q", m.gitRebaseMsg().Value())
	gitKey(t, m, "enter")
	d := m.gitRebaseDraftFor(keyA)
	t.Logf("A entry0 %+v", d.entries[0])
}
