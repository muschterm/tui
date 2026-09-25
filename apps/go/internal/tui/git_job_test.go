package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (g *fakeGit) GitOperationRefreshReview(_ context.Context, t client.GitTarget) (protocol.GitOperationState, error) {
	g.record("review-refresh:" + targetName(t))
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.oper, g.err
}

const jobThreadID = "job-thread"

// jobModel is conflictModel with an attached job in state and a job thread.
func jobModel(t *testing.T, state string) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := conflictModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-jobs")
	st := api.oper
	st.OperationID = "op-1"
	st.Review = &protocol.GitResolveReview{JobThreadID: jobThreadID, State: state, Items: []protocol.GitResolveItem{
		{Path: cfPath, Changed: true, Diff: "--- a\n+++ b\n-old\n+new\n", ConflictPin: "pin-1", WorktreeToken: "tok-1", CopyID: "before-job"},
		{Path: "staged.go", Staged: true, IndexDiff: "+staged\n", ConflictPin: "pin-s", WorktreeToken: "tok-s", CopyID: "before-job"},
	}, Violations: []string{"HEAD moved"}}
	api.oper = st
	job := m.snapshot.Threads[0]
	job.ID, job.Title, job.Agent, job.State = jobThreadID, "Resolve conflicts", "Demo", "running"
	job.Job = &protocol.ThreadJob{Kind: protocol.ThreadJobConflictResolution, OperationID: "op-1", Checkout: "/src/repo"}
	job.NeedsResume = false
	job.Requests = []protocol.Request{
		{ID: "q", Kind: "question", State: "pending", Title: "Which side?", Revision: 2, Questions: []protocol.Question{{ID: "q1", Text: "Which side?", Options: []string{"Ours", "Theirs"}}}},
		{ID: "perm", Kind: "approval", State: "pending", Title: "Run tests", Revision: 3, Choices: []string{"Allow", "Deny"}},
	}
	job.Activity = []protocol.Activity{{Role: "agent", Text: "Looking at src/app.go"}}
	m.snapshot.Threads = append(m.snapshot.Threads, job)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	return m, api
}

func TestGitEditUsesToplevel(t *testing.T) {
	m, api := conflictModel(t)
	st := api.oper
	st.Toplevel = "/src/repo"
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if p, reason := gitConflictEditPath(gitOperationToplevel(m.currentGitView().oper), "/src/repo", cfPath); reason != "" || p != cfPath {
		t.Fatalf("edit path %q %q", p, reason)
	}
}

func TestGitJobStartForm(t *testing.T) {
	m, api := conflictModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-jobs")
	st := api.oper
	st.Conflicts = append(st.Conflicts, protocol.GitConflict{Path: "b.go", Kind: "UU", ConflictPin: "p", WorktreeStat: "t"})
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if !strings.Contains(gitSurfaceText(m), "Resolve with agent…") {
		t.Fatalf("no start action:\n%s", gitSurfaceText(m))
	}
	// From a row menu, only that path is selected.
	m.setFocus("git:conflict:" + cfPath)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	gitWriteSettle(t, m, cmd)
	if !strings.Contains(menuText(m), "Resolve with agent…") {
		t.Fatalf("row menu:\n%s", menuText(m))
	}
	m.closeContextMenu()
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-open", ID: cfPath}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "RESOLVE WITH AGENT") || m.focus != "git:job-cancel" || m.gitJ.start == nil || m.gitJ.start.selected["b.go"] {
		t.Fatalf("form focus %q:\n%s", m.focus, text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-path", ID: "b.go"}))
	m.setFocus(gitJobInputKey)
	for _, r := range "keep ours" {
		gitKey(t, m, string(r))
	}
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	j := s.Git.ResolveJob
	if s.Kind != protocol.GitKindResolveJobStart || j.OperationID != st.OperationID || j.Paths != nil || j.Instructions != "keep ours" || j.AgentID == "" {
		t.Fatalf("start %+v", j)
	}
	// Cancel sends nothing.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-open"}))
	n := len(api.sent())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-cancel-form"}))
	if m.gitJ.start != nil || len(api.sent()) != n {
		t.Fatal("cancel")
	}
}

func TestGitJobRunningSection(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewRunning)
	text := gitSurfaceText(m)
	for _, want := range []string{"Demo working", "Answer · Which side?", "Approve or deny · Run tests", "Open agent transcript", "Stop agent", "End job…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "REVIEW") {
		t.Fatal("review shown while running")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-stop"}))
	if s := lastSent(t, api); s.Kind != protocol.GitKindResolveJobCancel || s.Git.ResolveJob.OperationID != "op-1" {
		t.Fatalf("stop %+v", s)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-transcript"}))
	if m.viewer == nil || !strings.Contains(m.viewer.att.Content, "Looking at src/app.go") || !strings.Contains(m.viewer.att.Content, "Pending · Which side?") {
		t.Fatal("transcript")
	}
	for _, r := range m.navigationRowsForTest() {
		if strings.Contains(r, jobThreadID) {
			t.Fatal("job thread in navigation")
		}
	}
}

func (m *Model) navigationRowsForTest() []string {
	open, closed := m.navigationSections()
	var out []string
	for _, r := range append(open, closed...) {
		out = append(out, fmt.Sprint(r))
	}
	return out
}

func TestGitJobReviewAcceptReject(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewReady)
	text := gitSurfaceText(m)
	for _, want := range []string{"Outside the job's scope", "HEAD moved", "REVIEW 2", "src/app.go · changed", "staged.go · staged", "Refresh review"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	// Accept first opens the diff; only after viewing it is Accept offered.
	m.setFocus("git:jitem:staged.go")
	gitKey(t, m, "a")
	if len(m.menu) != 0 || m.gitCF.viewer == nil || m.gitCF.viewer.tab != gitViewerStagedDiff || len(api.sent()) != 0 {
		t.Fatalf("accept without viewing: menu %d viewer %v", len(m.menu), m.gitCF.viewer != nil)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-close"}))
	m.setFocus("git:jitem:staged.go")
	gitKey(t, m, "a")
	if !strings.Contains(menuText(m), "Accept the agent's staged staged.go?") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("accept:\n%s", menuText(m))
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	c := lastSent(t, api).Git.Conflict
	if c.As != protocol.GitConflictAsKeepStaged || c.ConflictPin != "pin-s" || c.WorktreeToken != "tok-s" {
		t.Fatalf("accept %+v", c)
	}
	delete(m.gitW.writes, m.gitW.draftKey)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-reject", ID: cfPath}))
	if !strings.Contains(menuText(m), "Replaces the current file (changed) with the copy made before the agent ran") {
		t.Fatalf("reject:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.CopyID != "before-job" || c.WorktreeToken != "tok-1" {
		t.Fatalf("reject %+v", c)
	}
	// A stale review refuses decisions and offers refresh.
	m.menu = nil
	delete(m.gitW.writes, m.gitW.draftKey)
	st := api.oper
	st.Review.Stale = true
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	n := len(api.sent())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-accept", ID: cfPath}))
	if len(api.sent()) != n || len(m.menu) != 0 || !strings.Contains(gitSurfaceText(m), "changed since this review") {
		t.Fatal("stale review accepted")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-refresh"}))
	if api.count("review-refresh:") != 1 {
		t.Fatal("no review refresh")
	}
}

func TestGitJobFollowupAndEnd(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewInterrupted)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-followup"}))
	for _, r := range "try again" {
		gitKey(t, m, string(r))
	}
	gitKey(t, m, "enter")
	if s := lastSent(t, api); s.Kind != protocol.GitKindResolveJobFollowup || s.Git.ResolveJob.Prompt != "try again" {
		t.Fatalf("followup %+v", s.Git.ResolveJob)
	}
	delete(m.gitW.writes, m.gitW.draftKey)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-end"}))
	if m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatal("end default")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-confirm"}))
	if s := lastSent(t, api); s.Kind != protocol.GitKindResolveJobEnd {
		t.Fatalf("end %+v", s)
	}
}

func TestGitReviewViewerDiffTabs(t *testing.T) {
	m, _ := jobModel(t, protocol.GitReviewReady)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-view", ID: cfPath}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "[agent diff]") || !strings.Contains(text, "+new") {
		t.Fatalf("viewer:\n%s", text)
	}
}

func TestGitContinueAcknowledgesAgentChanges(t *testing.T) {
	m, api := conflictModel(t)
	st := api.oper
	st.Conflicts = nil
	st.Can.Continue = protocol.GitOperationAction{Allowed: true}
	st.AgentChanges = &protocol.GitAgentChanges{Fingerprint: "ac-fp", Items: []protocol.GitResolveItem{{Path: "x.go", Staged: true, IndexDiff: "+one\n+two\n"}}}
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
	if m.gitO.review == nil || !strings.Contains(gitSurfaceText(m), "(not by your decisions): x.go") || !strings.Contains(gitSurfaceText(m), "+two") {
		t.Fatalf("agent changes review:\n%s", gitSurfaceText(m))
	}
	for i := 0; i < 50 && !m.gitO.review.seen; i++ {
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		gitWriteSettle(t, m, cmd)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}))
	if o := lastSent(t, api).Git.Operation; o.AcknowledgeAgentChanges != "ac-fp" {
		t.Fatalf("continue %+v", o)
	}
	// review_pending refreshes the review.
	delete(m.gitW.writes, m.gitW.draftKey)
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, &protocol.Error{Code: "review_pending"}
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
	for i := 0; i < 50 && m.gitO.review != nil && !m.gitO.review.seen; i++ {
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		gitWriteSettle(t, m, cmd)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}))
	if api.count("review-refresh:") == 0 {
		t.Fatalf("review_pending: %q", m.notice.text)
	}
}

func TestGitJobCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"start", "running", "review", "continue"} {
			var m *Model
			switch name {
			case "start":
				m, _ = conflictModel(t)
				gitWriteSettle(t, m, m.activate(action{Kind: "git-job-open"}))
			case "running":
				m, _ = jobModel(t, protocol.GitReviewRunning)
				m.viewState().DetailScroll = 12
			case "review":
				m, _ = jobModel(t, protocol.GitReviewReady)
				m.viewState().DetailScroll = 14
			case "continue":
				var api *fakeGitWriter
				m, api = conflictModel(t)
				st := api.oper
				st.Conflicts = nil
				st.Can.Continue = protocol.GitOperationAction{Allowed: true}
				st.AgentChanges = &protocol.GitAgentChanges{Fingerprint: "ac-fp", Items: []protocol.GitResolveItem{{Path: "x.go", Staged: true, IndexDiff: "@@ -1 +1 @@\n-old line\n+new line\n"}}}
				api.oper = st
				gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
				gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
			}
			m.state.Light = light
			m.markDirty()
			file := filepath.Join(dir, fmt.Sprintf("144x70-git-job-%s-%s.ansi", name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGitJobRequestsAnsweredInline(t *testing.T) {
	m, _ := jobModel(t, protocol.GitReviewRunning)
	m.viewState().DetailScroll = 0
	h, ok := findHit(m.measure(), "git:job-req:perm")
	if !ok {
		t.Fatalf("no approval entry:\n%s", gitSurfaceText(m))
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	r, ok := m.request()
	if !ok || r.ID != "perm" || m.state.Active == jobThreadID {
		t.Fatalf("card shows %+v active %q", r, m.state.Active)
	}
	m.activate(m.approvalAction(r, 0))
	if m.busy == nil || m.busy.Kind != "request.answer" || m.busy.ThreadID != jobThreadID || m.busy.TargetID != "perm" || m.busy.Revision != 3 {
		t.Fatalf("approval command %+v", m.busy)
	}
	// The question, from the bell, answered through the question card.
	m2, _ := jobModel(t, protocol.GitReviewRunning)
	gitWriteSettle(t, m2, m2.activate(action{Kind: "attention-item", ID: jobThreadID, Value: "q"}))
	r2, ok := m2.request()
	if !ok || r2.ID != "q" {
		t.Fatalf("bell did not focus the job question: %+v", r2)
	}
	m2.chooseQuestionOption(0)
	m2.activate(action{Kind: "answer-submit"})
	if m2.busy == nil || m2.busy.ThreadID != jobThreadID || m2.busy.TargetID != "q" || len(m2.busy.QuestionAnswers)+len(m2.busy.Answers) != 1 {
		t.Fatalf("question command %+v", m2.busy)
	}
}

func TestGitJobKeysRespectDecisionAndReviewGeneration(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewReady)
	st := api.oper
	st.Review.Items[0].Decision = "accepted"
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	m.setFocus("git:jitem:" + cfPath)
	gitKey(t, m, "x")
	if len(m.menu) != 0 || !strings.Contains(m.notice.text, "Already accepted") {
		t.Fatalf("decided item: %q", m.notice.text)
	}
	// Viewing marks the current generation; a new review generation needs
	// viewing again, and the open viewer follows the new item.
	m2, api2 := jobModel(t, protocol.GitReviewReady)
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-job-view", ID: cfPath}))
	st2 := api2.oper
	st2.Review.Fingerprint = "gen-2"
	st2.Review.Items[0].Diff = "+newer\n"
	api2.oper = st2
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-refresh"}))
	if !strings.Contains(gitSurfaceText(m2), "+newer") {
		t.Fatalf("viewer did not follow the review:\n%s", gitSurfaceText(m2))
	}
	if !m2.gitReviewViewed(m2.currentGitView().oper, m2.currentGitView().oper.Review.Items[0]) {
		t.Fatal("displayed new generation not marked viewed")
	}
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-conflict-close"}))
	st2.Review.Fingerprint = "gen-3"
	api2.oper = st2
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-job-accept", ID: cfPath}))
	if len(m2.menu) != 0 || m2.gitCF.viewer == nil {
		t.Fatal("accept offered for an unviewed generation")
	}
}

func TestGitViewerTabKeysAndTruncation(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewReady)
	st := api.oper
	st.Review.Items[0].DiffTruncated = true
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-view", ID: cfPath}))
	if !strings.Contains(gitSurfaceText(m), "The server truncated this diff") {
		t.Fatalf("truncation:\n%s", gitSurfaceText(m))
	}
	start := m.gitCF.viewer.tab
	m.setFocus("git:conflict-close")
	gitKey(t, m, "]")
	if m.gitCF.viewer.tab == start {
		t.Fatal("] did not cycle")
	}
	gitKey(t, m, "[")
	if m.gitCF.viewer.tab != start {
		t.Fatal("[ did not cycle back")
	}
}

func TestGitContinueGateHonesty(t *testing.T) {
	m, api := conflictModel(t)
	st := api.oper
	st.Conflicts = nil
	st.Can.Continue = protocol.GitOperationAction{Allowed: true}
	st.Can.Skip = protocol.GitOperationAction{Allowed: true}
	st.Kind = "rebase"
	st.AgentChanges = &protocol.GitAgentChanges{Fingerprint: "ac", Incomplete: true, Reason: "time budget", Items: []protocol.GitResolveItem{
		{Path: "a.go", Staged: true, IndexDiff: "+a\n", DiffTruncated: true}, {Path: "b.bin", Staged: true, Binary: true}, {Path: "c.go", Staged: true, IndexDiff: "+c\n"}}}
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
	text := gitSurfaceText(m)
	for _, want := range []string{"Changed in the index since the agent started (not by your decisions): a.go", "diff truncated by the server", "No diff · binary content", "time budget", "Not fully shown: a.go, b.bin, changes that could not be compared", "4 items"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if m.gitO.review.verb != "Acknowledge, including content not shown" {
		t.Fatalf("verb %q", m.gitO.review.verb)
	}
	// Skip needs no acknowledgement and says it discards the staged changes.
	gitWriteSettle(t, m, m.activate(action{Kind: "git-review-cancel"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-skip"}))
	if m.gitO.review != nil && strings.Contains(gitSurfaceText(m), "Changed in the index") {
		t.Fatal("skip gated on agent changes")
	}
}

func TestGitJobStartExplicitPathsWhenTruncated(t *testing.T) {
	m, api := conflictModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-jobs")
	st := api.oper
	st.ConflictsTruncated = true
	st.Conflicts = append(st.Conflicts, protocol.GitConflict{Path: "vendor/lib", Kind: "UU", Submodule: true})
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-open"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Some conflicts are not listed") || !strings.Contains(text, "Submodule conflicts are left out") || strings.Contains(text, "vendor/lib") {
		t.Fatalf("form:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-start"}))
	if p := lastSent(t, api).Git.ResolveJob.Paths; len(p) != 1 || p[0] != cfPath {
		t.Fatalf("paths %v", p)
	}
}

func TestGitReviewRefreshDropsOlderReplies(t *testing.T) {
	m, api := jobModel(t, protocol.GitReviewReady)
	key, target := m.gitTarget()
	m.refreshGitReview(key, target)
	m.refreshGitReview(key, target)
	latest, old := api.oper, api.oper
	latestReview, oldReview := *latest.Review, *old.Review
	latestReview.Fingerprint, oldReview.Fingerprint = "latest", "old"
	latest.Review, old.Review = &latestReview, &oldReview
	m.Update(gitReviewMsg{seq: m.gitJ.reviewSeq, key: key, state: latest})
	m.Update(gitReviewMsg{seq: m.gitJ.reviewSeq - 1, key: key, state: old})
	if fp := m.currentGitView().oper.Review.Fingerprint; fp != "latest" {
		t.Fatalf("older refresh applied: %q", fp)
	}
}

func TestGitJobTranscriptLive(t *testing.T) {
	m, _ := jobModel(t, protocol.GitReviewRunning)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-job-transcript"}))
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID == jobThreadID {
			m.snapshot.Threads[i].Activity = append(m.snapshot.Threads[i].Activity, protocol.Activity{Role: "agent", Text: "Ran the tests"})
		}
	}
	gitWriteSettle(t, m, nil)
	if m.viewer == nil || !strings.Contains(m.viewer.att.Content, "Ran the tests") {
		t.Fatal("transcript not refreshed")
	}
}
