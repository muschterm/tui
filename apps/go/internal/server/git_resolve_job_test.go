package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

var acpTarget = client.GitTarget{ProjectID: "project-acp"}

// jobSetup is an ACP engine whose project is a repository stopped in an
// application-started merge with a conflict in c.txt (the merge also adds
// d.txt) and a clean tracked a.txt.
func jobSetup(t *testing.T) (*engine, string, func(...string) string) {
	e, _, root, git := jobSetupFleet(t)
	return e, root, git
}

func jobSetupFleet(t *testing.T) (*engine, *fakeFleet, string, func(...string) string) {
	t.Helper()
	return jobSetupKind(t, "merge")
}

// jobSetupKind stops a merge of other into main, or a rebase of main onto
// other, at the conflict in c.txt.
func jobSetupKind(t *testing.T, kind string) (*engine, *fakeFleet, string, func(...string) string) {
	t.Helper()
	gitFixture(t) // isolated Git configuration
	e, fleet, checkout := acpEngine(t)
	e.baselineDir = t.TempDir()
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for i := range e.snap.Projects {
		if e.snap.Projects[i].ID == "project-acp" {
			e.snap.Projects[i].Path = root
		}
	}
	e.mu.Unlock()
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("init", "-q")
	git("config", "user.name", "W")
	git("config", "user.email", "w@example.invalid")
	writeFile(t, root, "c.txt", "base\n")
	writeFile(t, root, "a.txt", "a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeFile(t, root, "c.txt", "other\n")
	writeFile(t, root, "d.txt", "d\n")
	git("add", ".")
	git("commit", "-q", "-m", "other")
	git("checkout", "-q", "main")
	writeFile(t, root, "c.txt", "main\n")
	git("commit", "-q", "-am", "main")
	p := previewOf(t, root, kind, "refs/heads/other")
	st := mustStatus(t, root)
	if kind == "rebase" {
		mustGit(t, e, client.GitRebaseCommand("rebase", acpTarget, st, p, false), protocol.GitStateSucceeded)
	} else {
		mustGit(t, e, client.GitMergeCommand("merge", acpTarget, st, p), protocol.GitStateSucceeded)
	}
	return e, fleet, root, git
}

func startJob(t *testing.T, e *engine, root, id, instructions string) protocol.GitOperationState {
	t.Helper()
	st := operationOf(t, e, root)
	settings := fakeSettings()
	mustGit(t, e, client.GitResolveJobStartCommand(id, acpTarget, st, "claude", &settings, nil, instructions), protocol.GitStateSucceeded)
	return st
}

func reviewOf(t *testing.T, e *engine, root string) (protocol.GitOperationState, protocol.GitResolveReview) {
	t.Helper()
	waitFor(t, e, "review", func(s protocol.Snapshot) bool {
		return len(s.GitOperations) == 1 && s.GitOperations[0].State == protocol.GitOperationAgentReview
	})
	st, err := e.operationState(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Review == nil {
		t.Fatal("no review")
	}
	return st, *st.Review
}

func itemOf(t *testing.T, r protocol.GitResolveReview, path string) protocol.GitResolveItem {
	t.Helper()
	for _, it := range r.Items {
		if it.Path == path {
			return it
		}
	}
	t.Fatalf("no review item for %s", path)
	return protocol.GitResolveItem{}
}

func TestGitResolveJobEditsAndStopsForReview(t *testing.T) {
	e, fleet, root, git := jobSetupFleet(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n")
	s := e.current()
	var job *protocol.Thread
	for i := range s.Threads {
		if s.Threads[i].Job != nil {
			job = &s.Threads[i]
		}
	}
	if job == nil || job.Job.OperationID != "op-merge" || !slices.Equal(job.Job.Paths, []string{"c.txt"}) || s.GitOperations[0].JobThreadID != job.ID {
		t.Fatalf("job thread: %+v %+v", job, s.GitOperations)
	}
	if !strings.Contains(job.Queue[0].Text, "Do not run git add") && !strings.Contains(job.Activity[0].Text, "Do not run git add") {
		t.Log("prompt already dispatched")
	}
	_, review := reviewOf(t, e, root)
	item := itemOf(t, review, "c.txt")
	if !item.Changed || item.HasMarkers || item.Staged || !strings.Contains(item.Diff, "+resolved") || len(review.Violations) != 0 || review.State != protocol.GitReviewReady {
		t.Fatalf("review: %+v %+v", review, item)
	}
	if prompts, _, _, _ := fleet.last().snapshot(); len(prompts) == 0 || !strings.Contains(prompts[0], "git add") {
		t.Fatalf("scoped prompt not sent: %q", prompts)
	}
	if !unmergedIn(t, root, "c.txt") {
		t.Fatal("the job staged the resolution")
	}
	// Accept is pinned: an edit after the review is stale.
	writeFile(t, root, "c.txt", "changed after review\n")
	_, err := e.command(client.GitReviewAcceptCommand("accept-stale", acpTarget, item, false))
	wantGitCode(t, err, "stale_entry")
	writeFile(t, root, "c.txt", "resolved\n")
	_, review = reviewOf(t, e, root)
	mustGit(t, e, client.GitReviewAcceptCommand("accept", acpTarget, itemOf(t, review, "c.txt"), false), protocol.GitStateSucceeded)
	if unmergedIn(t, root, "c.txt") || git("show", ":c.txt") != "resolved" {
		t.Fatal("accept did not stage the reviewed content")
	}
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitOperationContinueCommand("cont", acpTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || recordOf(e).JobThreadID != "" {
		t.Fatalf("continue after review: %+v %+v", r.Git, recordOf(e))
	}
}

func TestGitResolveJobFlagsScopeViolations(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n\nFAKE-GIT add c.txt\nFAKE-WRITE a.txt touched\\n")
	_, review := reviewOf(t, e, root)
	if item := itemOf(t, review, "c.txt"); !item.Staged {
		t.Fatalf("staging not flagged: %+v", item)
	}
	joined := strings.Join(review.Violations, "; ")
	if !strings.Contains(joined, "staged") || !strings.Contains(joined, "a.txt") {
		t.Fatalf("violations: %v", review.Violations)
	}
	if readText(t, root, "a.txt") != "touched\n" {
		t.Fatal("a violation was repaired silently")
	}
}

func TestGitResolveJobRejectRestoresTheConflict(t *testing.T) {
	e, root, git := jobSetup(t)
	stages := git("ls-files", "-u", "-z", "--", "c.txt")
	working := readText(t, root, "c.txt")
	startJob(t, e, root, "job", "FAKE-WRITE c.txt agent attempt\\n\nFAKE-GIT add c.txt")
	_, review := reviewOf(t, e, root)
	mustGit(t, e, client.GitReviewRejectCommand("reject", acpTarget, itemOf(t, review, "c.txt")), protocol.GitStateSucceeded)
	if git("ls-files", "-u", "-z", "--", "c.txt") != stages || readText(t, root, "c.txt") != working {
		t.Fatal("reject is not byte-identical")
	}
}

func TestGitResolveJobCancelFollowupAndReservation(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "cancel me")
	waitFor(t, e, "job running", func(s protocol.Snapshot) bool {
		for _, th := range s.Threads {
			if th.Job != nil && th.State == "running" {
				return true
			}
		}
		return false
	})
	// Other threads still wait for the operation; the job is exempt.
	other := startACPThread(t, e, "other", "say pong")
	s := waitFor(t, e, "other waits", func(s protocol.Snapshot) bool { return threadOf(s, other).WriterWait != nil })
	if w := threadOf(s, other); w.State != "idle" {
		t.Fatalf("other thread ran during the job: %s", w.State)
	}
	st := operationOf(t, e, root)
	_, err := e.command(client.GitOperationContinueCommand("cont", acpTarget, st, false))
	wantGitCode(t, err, "job_running")
	_, err = e.command(client.GitOperationAbortCommand("abort", acpTarget, st, true, false))
	wantGitCode(t, err, "job_running")
	opID := recordOf(e).OperationID
	if _, err := e.command(client.GitResolveJobCancelCommand("cancel", acpTarget, opID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "interrupted", func(s protocol.Snapshot) bool {
		return s.GitOperations[0].State == protocol.GitOperationAgentInterrupted && !activeTurn(ptrThread(s, s.GitOperations[0].JobThreadID))
	})
	// The cancelled turn must end before a follow-up is accepted.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := e.command(client.GitResolveJobFollowupCommand("follow", acpTarget, opID, "FAKE-WRITE c.txt second try\\n"))
		if err == nil {
			break
		}
		wantGitCode(t, err, "job_running")
		if time.Now().After(deadline) {
			t.Fatal("follow-up never accepted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, review := reviewOf(t, e, root)
	if item := itemOf(t, review, "c.txt"); !strings.Contains(item.Diff, "+second try") {
		t.Fatalf("follow-up review: %+v", item)
	}
	if _, err := e.command(client.GitResolveJobEndCommand("end", acpTarget, opID)); err != nil {
		t.Fatal(err)
	}
	if rec := recordOf(e); rec.JobThreadID != "" || strings.HasPrefix(rec.State, "agent_") {
		t.Fatalf("ended job: %+v", rec)
	}
	if jobThreadOf(e.current()) != nil {
		t.Fatal("ending the job left its hidden thread behind")
	}
}

func ptrThread(s protocol.Snapshot, id string) *protocol.Thread {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i]
		}
	}
	return &protocol.Thread{}
}

func TestGitResolveJobRestartIsInterrupted(t *testing.T) {
	s := protocol.Snapshot{
		Threads:       []protocol.Thread{{ID: "job-x", State: "running", Job: &protocol.ThreadJob{Kind: protocol.ThreadJobConflictResolution, OperationID: "op"}}},
		GitOperations: []protocol.GitOperationRecord{{OperationID: "op", Kind: "merge", State: protocol.GitOperationAgentRunning, JobThreadID: "job-x", Checkout: t.TempDir()}},
	}
	recoverGitOperations(&s)
	if s.Threads[0].State != "interrupted" || !s.Threads[0].NeedsResume {
		t.Fatalf("job thread after restart: %+v", s.Threads[0])
	}
	if st := s.GitOperations[0].State; st != protocol.GitOperationAgentInterrupted && st != protocol.GitOperationEndedExternal {
		t.Fatalf("record after restart: %s", st)
	}
}

func TestGitResolveJobWithTheFixtureAgent(t *testing.T) {
	e, root, _ := conflictSetup(t)
	p := previewOf(t, root, "merge", "refs/heads/other")
	mustGit(t, e, client.GitMergeCommand("merge", gitTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	mustGit(t, e, client.GitResolveJobStartCommand("job", gitTarget, st, agent.FixtureID, nil, nil, ""), protocol.GitStateSucceeded)
	tickUntil(t, e, "fixture job reviewed", func(s protocol.Snapshot) bool {
		return s.GitOperations[0].State == protocol.GitOperationAgentReview
	})
	st = operationOf(t, e, root)
	if st.Review == nil || st.Review.State != protocol.GitReviewReady || itemOf(t, *st.Review, "c.txt").Changed {
		t.Fatalf("fixture review: %+v", st.Review)
	}
}

// S4 review 2026-09-25.

func jobThreadOf(s protocol.Snapshot) *protocol.Thread {
	for i := range s.Threads {
		if s.Threads[i].Job != nil {
			return &s.Threads[i]
		}
	}
	return nil
}

func TestGitResolveJobKeepsTheUsersPreJobEdit(t *testing.T) {
	e, root, _ := jobSetup(t)
	writeFile(t, root, "c.txt", "user partial resolution\n")
	startJob(t, e, root, "job", "FAKE-WRITE c.txt agent version\\n")
	_, review := reviewOf(t, e, root)
	item := itemOf(t, review, "c.txt")
	if !strings.Contains(item.Diff, "-user partial resolution") || !strings.Contains(item.Diff, "+agent version") {
		t.Fatalf("diff is not against the pre-job file:\n%s", item.Diff)
	}
	mustGit(t, e, client.GitReviewRejectCommand("reject", acpTarget, item), protocol.GitStateSucceeded)
	if readText(t, root, "c.txt") != "user partial resolution\n" || !unmergedIn(t, root, "c.txt") {
		t.Fatalf("reject lost the user's edit: %q", readText(t, root, "c.txt"))
	}
}

func TestGitResolveJobStagedContentMustBeReviewed(t *testing.T) {
	e, root, git := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt hidden staged content\\n\nFAKE-GIT add c.txt\nFAKE-WRITE c.txt shown content\\n")
	st, review := reviewOf(t, e, root)
	item := itemOf(t, review, "c.txt")
	if !item.Staged || item.IndexOid == "" || !strings.Contains(item.IndexDiff, "+hidden staged content") {
		t.Fatalf("staged content not shown: %+v", item)
	}
	_, err := e.command(client.GitOperationContinueCommand("cont-early", acpTarget, st, false))
	wantGitCode(t, err, "review_pending")
	cont := client.GitOperationContinueCommand("cont-unack", acpTarget, st, false)
	_, err = e.command(cont)
	wantGitCode(t, err, "review_pending") // agent-staged content no decision explains
	if st = operationOf(t, e, root); st.AgentChanges == nil || !hasChange(*st.AgentChanges, "c.txt") {
		t.Fatalf("agent changes: %+v", st.AgentChanges)
	}
	// keep_staged is a decision pinned to the reviewed index entry.
	mustGit(t, e, client.GitReviewAcceptCommand("keep", acpTarget, item, false), protocol.GitStateSucceeded)
	st, _ = reviewOf(t, e, root)
	if st.AgentChanges != nil && len(st.AgentChanges.Items) > 0 {
		t.Fatalf("keep_staged left unexplained changes: %+v", st.AgentChanges)
	}
	mustGit(t, e, client.GitOperationContinueCommand("cont", acpTarget, st, false), protocol.GitStateSucceeded)
	if got := git("show", "HEAD:c.txt"); got != "hidden staged content" {
		t.Fatalf("committed %q", got)
	}
}

func hasChange(c protocol.GitAgentChanges, path string) bool {
	for _, it := range c.Items {
		if it.Path == path {
			return true
		}
	}
	return false
}

// continueWithAck expects continue to be refused for exactly path, then to
// succeed once the reported changes are acknowledged.
func continueWithAck(t *testing.T, e *engine, root, path string) {
	t.Helper()
	st := operationOf(t, e, root)
	cont := client.GitOperationContinueCommand("cont-unack", acpTarget, st, false)
	_, err := e.command(cont)
	wantGitCode(t, err, "review_pending")
	st = operationOf(t, e, root)
	if st.AgentChanges == nil || !hasChange(*st.AgentChanges, path) {
		t.Fatalf("agent changes: %+v", st.AgentChanges)
	}
	stale := client.GitOperationContinueCommand("cont-stale", acpTarget, st, false)
	stale.Git.Operation.AcknowledgeAgentChanges = "stale"
	_, err = e.command(stale)
	wantGitCode(t, err, "review_pending")
	cont = client.GitOperationContinueCommand("cont", acpTarget, st, false)
	client.AcknowledgeAgentChanges(&cont, st.AgentChanges)
	mustGit(t, e, cont, protocol.GitStateSucceeded)
}

// Ported verification repros (2026-09-25 gate round).

// A review read during the cancel window must not hide what the agent
// stages before its turn actually stops.
func TestGitResolveJobCancelWindowStagingIsGated(t *testing.T) {
	e, fleet, root, git := jobSetupFleet(t)
	startJob(t, e, root, "job", "cancel me\nFAKE-WRITE c.txt resolved\\n")
	waitFor(t, e, "running", func(s protocol.Snapshot) bool { th := jobThreadOf(s); return th != nil && th.State == "running" })
	release := make(chan struct{})
	peer := fleet.last()
	peer.mu.Lock()
	peer.cancelRelease = release
	peer.mu.Unlock()
	if _, err := e.command(client.GitResolveJobCancelCommand("cancel", acpTarget, recordOf(e).OperationID)); err != nil {
		t.Fatal(err)
	}
	if st, err := e.operationState(context.Background(), root, false); err != nil || st.Review == nil || st.Review.State != protocol.GitReviewRunning {
		t.Fatalf("review during the cancel window: %v %+v", err, st.Review)
	}
	writeFile(t, root, "a.txt", "agent outside\n")
	git("add", "a.txt")
	close(release)
	waitFor(t, e, "stopped", func(s protocol.Snapshot) bool { th := jobThreadOf(s); return th != nil && !activeTurn(th) })
	waitFor(t, e, "interrupted", func(s protocol.Snapshot) bool {
		return s.GitOperations[0].State == protocol.GitOperationAgentInterrupted
	})
	st, err := e.operationState(context.Background(), root, true)
	if err != nil || st.Review == nil || st.Review.State == protocol.GitReviewRunning || !strings.Contains(strings.Join(st.Review.Violations, "\n"), "a.txt") {
		t.Fatalf("re-review after the stop missed a.txt: %v %+v", err, st.Review)
	}
	// The user resolves c.txt in a terminal; that staging is unexplained too.
	writeFile(t, root, "c.txt", "user resolved\n")
	git("add", "c.txt")
	continueWithAck(t, e, root, "a.txt")
	if got := git("show", "HEAD:a.txt"); got != "agent outside" {
		t.Fatalf("HEAD:a.txt = %q", got)
	}
}

// Ending the job does not drop the gate on agent-staged outside content.
func TestGitResolveJobEndKeepsTheGate(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n\nFAKE-WRITE a.txt agent outside\\n\nFAKE-GIT add a.txt")
	_, review := reviewOf(t, e, root)
	mustGit(t, e, client.GitReviewAcceptCommand("acc", acpTarget, itemOf(t, review, "c.txt"), false), protocol.GitStateSucceeded)
	if _, err := e.command(client.GitResolveJobEndCommand("end", acpTarget, recordOf(e).OperationID)); err != nil {
		t.Fatalf("end: %v", err)
	}
	continueWithAck(t, e, root, "a.txt")
}

// Deleting the job thread does not drop the gate either.
func TestGitResolveJobDeleteKeepsTheGate(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt agent content\\n\nFAKE-GIT add c.txt")
	reviewOf(t, e, root)
	jt := jobThreadOf(e.current())
	if _, err := e.command(protocol.Command{Version: 1, ID: "del", Kind: "thread.delete", ThreadID: jt.ID, Revision: jt.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	continueWithAck(t, e, root, "c.txt")
}

// The user finishing the operation in a terminal during review is external.
func TestGitResolveJobTerminalEndDuringReviewIsExternal(t *testing.T) {
	e, root, git := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt r\\n")
	reviewOf(t, e, root)
	git("add", "c.txt")
	git("commit", "-q", "-m", "user in terminal")
	operationOf(t, e, root)
	if rec := recordOf(e); rec.State != protocol.GitOperationEndedExternal {
		t.Fatalf("record after a terminal commit during review: %+v", rec)
	}
}

func TestGitResolveJobThreadDeletionDetaches(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "")
	reviewOf(t, e, root)
	jt := jobThreadOf(e.current())
	if _, err := e.command(protocol.Command{Version: 1, ID: "del", Kind: "thread.delete", ThreadID: jt.ID, Revision: jt.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	st := operationOf(t, e, root)
	if rec := recordOf(e); rec.JobThreadID != "" || strings.HasPrefix(rec.State, "agent_") {
		t.Fatalf("record after deleting the job thread: %+v", rec)
	}
	settings := fakeSettings()
	mustGit(t, e, client.GitResolveJobStartCommand("job2", acpTarget, st, "claude", &settings, nil, ""), protocol.GitStateSucceeded)
}

func TestGitResolveJobThreadRefusesOrdinaryCommands(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "")
	reviewOf(t, e, root)
	jt := jobThreadOf(e.current())
	settings := fakeSettings()
	for _, c := range []protocol.Command{
		{Version: 1, ID: "ps", Kind: "prompt.send", ThreadID: jt.ID, Text: "FAKE-WRITE a.txt outside\\n", Settings: &settings},
		{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: jt.ID},
	} {
		_, err := e.command(c)
		wantGitCode(t, err, "job_thread")
	}
	if readText(t, root, "a.txt") != "a\n" {
		t.Fatal("an ordinary command ran the job agent")
	}
}

func TestGitResolveJobAgentEndingTheOperationIsAttributed(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt r\\n\nFAKE-GIT add c.txt\nFAKE-GIT commit -q -m agent-did-it")
	waitFor(t, e, "ended by job", func(s protocol.Snapshot) bool {
		e.mu.Lock()
		e.syncJobsLocked()
		e.mu.Unlock()
		return len(s.GitOperations) == 1 && s.GitOperations[0].State == protocol.GitOperationEndedByJob
	})
	rec := recordOf(e)
	if rec.JobThreadID != "" || rec.Code != "ended_by_job" {
		t.Fatalf("record: %+v", rec)
	}
	_, err := e.command(client.GitResolveJobFollowupCommand("follow", acpTarget, rec.OperationID, "more"))
	wantGitCode(t, err, "no_job")
}

func TestGitResolveJobRejectAfterTheAgentRemovedTheFile(t *testing.T) {
	e, root, git := jobSetup(t)
	stages := git("ls-files", "-u", "-z", "--", "c.txt")
	working := readText(t, root, "c.txt")
	startJob(t, e, root, "job", "FAKE-GIT rm -q c.txt\nFAKE-WRITE newfile.txt created\\n")
	_, review := reviewOf(t, e, root)
	item := itemOf(t, review, "c.txt")
	if !item.Deleted || !item.Staged || !strings.Contains(strings.Join(review.Violations, ";"), "newfile.txt") {
		t.Fatalf("review: %+v %v", item, review.Violations)
	}
	mustGit(t, e, client.GitReviewRejectCommand("reject", acpTarget, item), protocol.GitStateSucceeded)
	if git("ls-files", "-u", "-z", "--", "c.txt") != stages || readText(t, root, "c.txt") != working {
		t.Fatal("reject after rm is not byte-identical")
	}
}

func TestGitResolveJobCancelWindowRefusesWrites(t *testing.T) {
	e, fleet, root, _ := jobSetupFleet(t)
	startJob(t, e, root, "job", "cancel me")
	waitFor(t, e, "running", func(s protocol.Snapshot) bool { th := jobThreadOf(s); return th != nil && th.State == "running" })
	release := make(chan struct{})
	peer := fleet.last()
	peer.mu.Lock()
	peer.cancelRelease = release
	peer.mu.Unlock()
	defer close(release)
	opID := recordOf(e).OperationID
	if _, err := e.command(client.GitResolveJobCancelCommand("cancel", acpTarget, opID)); err != nil {
		t.Fatal(err)
	}
	st := operationOf(t, e, root)
	_, err := e.command(client.GitOperationAbortCommand("abort", acpTarget, st, true, false))
	wantGitCode(t, err, "job_running")
	_, err = e.command(client.GitResolveJobEndCommand("end", acpTarget, opID))
	wantGitCode(t, err, "job_running")
}

func TestGitResolveJobReviewIsBoundedAndHonest(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-GIT add c.txt")
	st, _ := reviewOf(t, e, root)
	e.mu.Lock()
	in := e.jobInputLocked(root)
	e.mu.Unlock()
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := computeReview(ctx, g, st, in)
	for _, it := range r.Items {
		if !it.Unknown || it.Changed || it.Staged {
			t.Fatalf("an expired review claimed something: %+v", it)
		}
	}
	if !r.Incomplete {
		t.Fatal("expired review not marked incomplete")
	}
	// A large, completely different file is not diffed line by line.
	var a, b strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&a, "old %d\n", i)
		fmt.Fprintf(&b, "new %d\n", i)
	}
	start := time.Now()
	text, truncated := unifiedDiff([]byte(a.String()), []byte(b.String()))
	if !truncated || !strings.Contains(text, "too many differences") || time.Since(start) > 2*time.Second {
		t.Fatalf("large diff: %v %q", time.Since(start), text[:min(len(text), 200)])
	}
	if text, _ := unifiedDiff([]byte("a\nb\nc\n"), []byte("a\nx\nc\n")); !strings.Contains(text, "-b\n+x\n") {
		t.Fatalf("small diff:\n%s", text)
	}
}

func TestGitResolveJobReviewOfManyPathsIsBounded(t *testing.T) {
	gitFixture(t)
	e, _, checkout := acpEngine(t)
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for i := range e.snap.Projects {
		if e.snap.Projects[i].ID == "project-acp" {
			e.snap.Projects[i].Path = root
		}
	}
	e.mu.Unlock()
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("init", "-q")
	git("config", "user.name", "W")
	git("config", "user.email", "w@example.invalid")
	const n = 60
	body := func(tag string) string {
		var b strings.Builder
		for i := range 1500 {
			fmt.Fprintf(&b, "%s %d\n", tag, i)
		}
		return b.String()
	}
	name := func(i int) string { return fmt.Sprintf("f%02d.txt", i) }
	for _, branch := range []string{"base", "other", "main"} {
		switch branch {
		case "other":
			git("checkout", "-q", "-b", "other")
		case "main":
			git("checkout", "-q", "main")
		}
		for i := range n {
			writeFile(t, root, name(i), body(branch))
		}
		git("add", ".")
		git("commit", "-q", "-m", branch)
	}
	p := previewOf(t, root, "merge", "refs/heads/other")
	mustGit(t, e, client.GitMergeCommand("merge", acpTarget, mustStatus(t, root), p), protocol.GitStateSucceeded)
	var instr strings.Builder
	for i := range n {
		instr.WriteString("FAKE-WRITE " + name(i) + " x\\n\n")
	}
	startJob(t, e, root, "job", instr.String())
	start := time.Now()
	_, review := reviewOf(t, e, root)
	if len(review.Items) != n || time.Since(start) > 15*time.Second {
		t.Fatalf("review of %d paths: %d items in %v", n, len(review.Items), time.Since(start))
	}
	start = time.Now()
	if st := operationOf(t, e, root); st.Review == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cached review read took %v", time.Since(start))
	}
}

// Gate follow-up round (2026-09-25).

// An Incomplete set's fingerprint covers the whole index listing, also
// past the read limit.
func TestGitResolveJobIncompleteFingerprintCoversTheWholeIndex(t *testing.T) {
	gitFixture(t)
	dir := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "-q")
	a, b := git("a\n", "hash-object", "-w", "--stdin"), git("b\n", "hash-object", "-w", "--stdin")
	var sb strings.Builder
	for i := range 90000 { // well past gitStatusMaxBytes of listing
		fmt.Fprintf(&sb, "100644 %s\tdir/some/longish/path/file%06d.txt\n", a, i)
	}
	fmt.Fprintf(&sb, "100644 %s\tzzz_tail.txt\n", a)
	git(sb.String(), "update-index", "--index-info")
	ctx := context.Background()
	g, err := newGitReader(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := protocol.GitOperationRecord{State: protocol.GitOperationStoppedConflicts, JobBaseline: &protocol.GitJobBaseline{StopKey: "k"}}
	ac1 := agentChanges(ctx, g, rec, t.TempDir(), "k")
	git(fmt.Sprintf("100644 %s\tzzz_tail.txt\n", b), "update-index", "--index-info")
	ac2 := agentChanges(ctx, g, rec, t.TempDir(), "k")
	if !ac1.Incomplete || ac1.Reason == "" || ac1.Fingerprint == "" || ac1.Fingerprint == ac2.Fingerprint {
		t.Fatalf("incomplete fingerprints: %+v / %+v", ac1, ac2)
	}
	w := &gitWriter{recordLookup: func() *protocol.GitOperationRecord { return &rec }}
	stopped := protocol.GitOperationState{}
	rec.JobBaseline.StopKey = stopKey(stopped)
	ac2 = agentChanges(ctx, g, rec, "", stopKey(stopped))
	if err := refuseAgentChanges(ctx, g, w, stopped, ac1.Fingerprint); err == nil {
		t.Fatal("a stale acknowledgement passed after an index change past the read limit")
	}
	if err := refuseAgentChanges(ctx, g, w, stopped, ac2.Fingerprint); err != nil {
		t.Fatalf("the current acknowledgement: %v", err)
	}
}

// A baseline of another stop fails closed; a new job at the new stop
// takes a new baseline.
func TestGitResolveJobBaselineOfAnotherStopFailsClosed(t *testing.T) {
	e, root, _ := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n")
	reviewOf(t, e, root)
	if st := operationOf(t, e, root); st.AgentChanges == nil || st.AgentChanges.Incomplete {
		t.Fatalf("at the job's stop: %+v", st.AgentChanges)
	}
	e.mu.Lock()
	e.snap.GitOperations[0].JobBaseline.StopKey = "another-stop"
	e.mu.Unlock()
	st := operationOf(t, e, root)
	if st.AgentChanges == nil || !st.AgentChanges.Incomplete || !strings.Contains(st.AgentChanges.Reason, "stop") || st.AgentChanges.Fingerprint == "" {
		t.Fatalf("agent changes at another stop: %+v", st.AgentChanges)
	}
	// A new job at this stop replaces the baseline and its decisions.
	if _, err := e.command(client.GitResolveJobEndCommand("end", acpTarget, recordOf(e).OperationID)); err != nil {
		t.Fatal(err)
	}
	startJob(t, e, root, "job2", "")
	_, review := reviewOf(t, e, root)
	rec := recordOf(e)
	if rec.JobBaseline.StopKey == "another-stop" || len(rec.JobDecisions) != 0 {
		t.Fatalf("baseline after a job at the new stop: %+v %v", rec.JobBaseline, rec.JobDecisions)
	}
	mustGit(t, e, client.GitReviewAcceptCommand("acc", acpTarget, itemOf(t, review, "c.txt"), false), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitOperationContinueCommand("cont", acpTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
}

func TestGitResolveJobGateClearsOnlyWhenTheStopProvablyMoved(t *testing.T) {
	st := protocol.GitOperationState{Kind: "rebase", Step: 2}
	key := stopKey(protocol.GitOperationState{Kind: "rebase", Step: 1})
	for _, c := range []struct {
		op   protocol.GitOperationResult
		want bool
	}{
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeCompleted}, true},
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeAborted}, true},
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeStoppedConflicts, State: &st}, true},
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeStopped}, false}, // not observed
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeUnknown, State: &st}, false},
		{protocol.GitOperationResult{Outcome: protocol.GitOutcomeUnchanged}, false},
	} {
		if got := operationLeftStop(&c.op, key); got != c.want {
			t.Errorf("%s: %v, want %v", c.op.Outcome, got, c.want)
		}
	}
	same := protocol.GitOperationState{Kind: "rebase", Step: 1}
	if operationLeftStop(&protocol.GitOperationResult{Outcome: protocol.GitOutcomeStoppedConflicts, State: &same}, key) {
		t.Error("the same stop was taken for a move")
	}
}

// The baseline listing lives in the application home: Git garbage
// collection cannot remove it, a missing listing fails closed, and
// unreferenced listings are swept.
func TestGitResolveJobBaselineListingIsKeptInTheHome(t *testing.T) {
	e, root, git := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n")
	_, review := reviewOf(t, e, root)
	rec := recordOf(e)
	if rec.JobBaseline == nil || rec.JobBaseline.Incomplete || rec.JobBaseline.Listing == "" {
		t.Fatalf("baseline: %+v", rec.JobBaseline)
	}
	git("gc", "-q", "--prune=now")
	mustGit(t, e, client.GitReviewAcceptCommand("acc", acpTarget, itemOf(t, review, "c.txt"), false), protocol.GitStateSucceeded)
	if st := operationOf(t, e, root); st.AgentChanges == nil || st.AgentChanges.Incomplete {
		t.Fatalf("after gc: %+v", st.AgentChanges)
	}
	old := time.Now().Add(-2 * jobBaselineSweepAge)
	orphan := filepath.Join(e.baselineDir, strings.Repeat("0", 64))
	writeFile(t, e.baselineDir, filepath.Base(orphan), "x")
	listing := filepath.Join(e.baselineDir, rec.JobBaseline.Listing)
	for _, p := range []string{orphan, listing} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	e.sweepJobBaselines()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan listing kept: %v", err)
	}
	if _, err := os.Stat(listing); err != nil {
		t.Fatalf("referenced listing swept: %v", err)
	}
	if err := os.Remove(listing); err != nil {
		t.Fatal(err)
	}
	st := operationOf(t, e, root)
	if st.AgentChanges == nil || !st.AgentChanges.Incomplete || !strings.Contains(st.AgentChanges.Reason, "missing") {
		t.Fatalf("missing listing: %+v", st.AgentChanges)
	}
	continueWithAckOf(t, e, root)
}

// continueWithAckOf is continueWithAck for an Incomplete set.
func continueWithAckOf(t *testing.T, e *engine, root string) {
	t.Helper()
	st := operationOf(t, e, root)
	_, err := e.command(client.GitOperationContinueCommand("cont-unack", acpTarget, st, false))
	wantGitCode(t, err, "review_pending")
	cont := client.GitOperationContinueCommand("cont", acpTarget, operationOf(t, e, root), false)
	client.AcknowledgeAgentChanges(&cont, st.AgentChanges)
	mustGit(t, e, cont, protocol.GitStateSucceeded)
}

// Staging a path through the application while the gate is active is the
// user's decision.
func TestGitResolveJobApplicationStagingIsADecision(t *testing.T) {
	e, root, git := jobSetup(t)
	startJob(t, e, root, "job", "FAKE-WRITE c.txt resolved\\n\nFAKE-WRITE a.txt agent outside\\n")
	_, review := reviewOf(t, e, root)
	mustGit(t, e, client.GitReviewAcceptCommand("acc", acpTarget, itemOf(t, review, "c.txt"), false), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitStageCommand("stage", acpTarget, entryOf(t, root, "a.txt", protocol.GitGroupUnstaged)), protocol.GitStateSucceeded)
	if rec := recordOf(e); rec.JobDecisions["a.txt"] == "" {
		t.Fatalf("no decision for the staged path: %v", rec.JobDecisions)
	}
	mustGit(t, e, client.GitOperationContinueCommand("cont", acpTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := git("show", "HEAD:a.txt"); got != "agent outside" {
		t.Fatalf("HEAD:a.txt = %q", got)
	}
}

// A decision whose index could not be read afterwards is not recorded.
func TestGitResolveJobUnreadDecisionIsNotRecorded(t *testing.T) {
	s := protocol.Snapshot{GitOperations: []protocol.GitOperationRecord{{OperationID: "op", Checkout: "/r", State: protocol.GitOperationAgentReview, JobBaseline: &protocol.GitJobBaseline{}}}}
	c := protocol.Command{Kind: protocol.GitKindConflictChoose, Git: &protocol.GitWrite{Conflict: &protocol.GitConflictWrite{Path: "c.txt"}}}
	res := &protocol.GitResult{State: protocol.GitStateSucceeded}
	recordJobDecision(&s, "/r", c, res, "", false)
	if _, ok := s.GitOperations[0].JobDecisions["c.txt"]; ok {
		t.Fatal("an unread decision was recorded")
	}
	recordJobDecision(&s, "/r", c, res, "", true)
	if d, ok := s.GitOperations[0].JobDecisions["c.txt"]; !ok || d != "" {
		t.Fatal("a read removal was not recorded")
	}
}

// The tick's job sync does not end a record while an application write
// holds the operation.
func TestGitResolveJobTickSkipsAHeldOperation(t *testing.T) {
	e, root, git := jobSetup(t)
	startJob(t, e, root, "job", "")
	reviewOf(t, e, root)
	e.mu.Lock()
	e.gitLocked().holders["held"] = gitHold{top: root}
	e.mu.Unlock()
	git("merge", "--abort")
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if rec := recordOf(e); !gitOperationActive(rec.State) {
		t.Fatalf("record ended while held: %+v", rec)
	}
	e.mu.Lock()
	delete(e.gitLocked().holders, "held")
	e.mu.Unlock()
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if rec := recordOf(e); rec.State != protocol.GitOperationEndedExternal {
		t.Fatalf("record after release: %+v", rec)
	}
}

// Skip resets the index, so it is not gated: the agent's staged content is
// discarded, never committed.
func TestGitResolveJobSkipIsNotGated(t *testing.T) {
	e, _, root, git := jobSetupKind(t, "rebase")
	startJob(t, e, root, "job", "FAKE-WRITE a.txt agent outside\\n\nFAKE-GIT add a.txt")
	reviewOf(t, e, root)
	st := operationOf(t, e, root)
	if st.AgentChanges == nil || !hasChange(*st.AgentChanges, "a.txt") {
		t.Fatalf("agent changes: %+v", st.AgentChanges)
	}
	skip, ok := client.GitOperationSkipCommand("skip", acpTarget, st, true)
	if !ok {
		t.Fatal("skip not offered")
	}
	mustGit(t, e, skip, protocol.GitStateSucceeded)
	if got := git("show", "HEAD:a.txt"); got != "a" {
		t.Fatalf("HEAD:a.txt = %q", got)
	}
}
