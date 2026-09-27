package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/acpbridge"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// planSetup is an ACP engine whose project is a repository with base and
// commits A, "fixup! A" (B), C and D on main.
func planSetup(t *testing.T) (*engine, *fakeFleet, string, func(...string) string, map[string]string) {
	t.Helper()
	if !gitRebaseInteractiveSupported() {
		t.Skip("Git too old for interactive rebase")
	}
	gitFixture(t)
	e, fleet, checkout := acpEngine(t)
	e.baselineDir, e.rebaseDir = t.TempDir(), t.TempDir()
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
	writeFile(t, root, "base.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	oids := map[string]string{"base": git("rev-parse", "HEAD")}
	for _, c := range []struct{ name, file, subject string }{{"A", "a.txt", "A"}, {"B", "a.txt", "fixup! A"}, {"C", "c.txt", "C"}, {"D", "d.txt", "D"}} {
		writeFile(t, root, c.file, c.name+" content\n")
		git("add", ".")
		git("commit", "-q", "-m", c.subject, "-m", "body of "+c.name)
		oids[c.name] = git("rev-parse", "HEAD")
	}
	return e, fleet, root, git, oids
}

// planAnswer is a fake agent answer carrying wire in a tui-rebase-plan
// block, as FAKE-REPLY lines.
func planAnswer(t *testing.T, wire any) string {
	t.Helper()
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(b), `\n`, `\u000a`)
	return "FAKE-REPLY Here is the plan.\nFAKE-REPLY ```tui-rebase-plan\nFAKE-REPLY " + text + "\nFAKE-REPLY ```"
}

func goodWire(o map[string]string) protocol.GitRebaseProposalWire {
	return protocol.GitRebaseProposalWire{Version: 1, Rationale: "fold the fixup, clarify C", Entries: []protocol.GitRebaseEntry{
		{Action: "pick", Commit: o["A"][:9]},
		{Action: "fixup", Commit: o["B"]},
		{Action: "reword", Commit: o["C"], Message: "C, clarified\n\nWhy C exists."},
		{Action: "pick", Commit: o["D"]},
	}}
}

func startPlan(t *testing.T, e *engine, id, base, instructions string) string {
	t.Helper()
	settings := fakeSettings()
	mustGit(t, e, client.GitRebasePlanStartCommand(id, acpTarget, "claude", &settings, base, "", "", instructions), protocol.GitStateSucceeded)
	return "rebase-plan-" + id
}

func settledProposal(t *testing.T, e *engine, job string, revisionAbove int64) protocol.GitRebaseProposalSummary {
	t.Helper()
	s := waitFor(t, e, "proposal settled", func(s protocol.Snapshot) bool {
		th := threadOf(s, job)
		return th.Job != nil && th.Job.Proposal != nil && th.Job.Proposal.State != protocol.GitProposalRunning && th.Job.Proposal.Revision > revisionAbove
	})
	return *threadOf(s, job).Job.Proposal
}

func proposalOf(t *testing.T, e *engine, root, job string) protocol.GitRebaseProposal {
	t.Helper()
	p, err := e.readRebaseProposal(context.Background(), root, job)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRebasePlanJobProposesAValidPlan(t *testing.T) {
	e, fleet, root, git, o := planSetup(t)
	head := git("rev-parse", "HEAD")
	job := startPlan(t, e, "plan", o["base"], "Tidy this up.\n"+planAnswer(t, goodWire(o)))
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalProposed || sum.Entries != 4 || sum.Commits != 4 || sum.Branch != "main" || sum.Turns != 1 || sum.Concurrent {
		t.Fatalf("summary: %+v", sum)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0], o["C"]) || !strings.Contains(prompts[0], "tui-rebase-plan") || !strings.Contains(prompts[0], "+C content") ||
		!strings.Contains(prompts[0], "body of B") || !strings.Contains(prompts[0], "Tidy this up.") || !strings.Contains(prompts[0], "c.txt") {
		t.Fatalf("prompt: %q", prompts)
	}
	p := proposalOf(t, e, root, job)
	if p.State != protocol.GitProposalProposed || !client.ProposalLoadable(p) || len(p.Entries) != 4 || p.Entries[0].Commit != o["A"] || p.Rationale == "" ||
		p.Plan == nil || len(p.Plan.Commits) != 4 || p.CurrentFingerprint != sum.Fingerprint || p.Enforcement == "" || !strings.Contains(p.Raw, "Here is the plan.") || p.Base != o["base"] {
		t.Fatalf("proposal: %+v", p)
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("planning changed HEAD")
	}
	// The listing names the job; the user starts the plan as any other.
	list, err := e.listRebaseProposals(context.Background(), root)
	if err != nil || len(list) != 1 || list[0].JobThreadID != job || list[0].State != protocol.GitProposalProposed {
		t.Fatalf("list: %+v %v", list, err)
	}
	plan := planOf(t, root, p.Base, p.Onto)
	mustGit(t, e, client.GitRebaseInteractiveCommand("rebase", acpTarget, plan, p.Entries, client.RebaseOptions{UpdateRefs: p.UpdateRefs}), protocol.GitStateSucceeded)
	if got := git("log", "--format=%s", o["base"]+"..HEAD"); got != "D\nC, clarified\nA" {
		t.Fatalf("history after the proposal ran: %q", got)
	}
	// The branch moved: the proposal is stale now and cannot be revised.
	if p := proposalOf(t, e, root, job); p.State != protocol.GitProposalStale || client.ProposalLoadable(p) {
		t.Fatalf("stale: %s", p.State)
	}
	_, err = e.command(client.GitRebasePlanReviseCommand("revise", acpTarget, job, sum.Revision, "again"))
	wantGitCode(t, err, "stale_plan")
	// End removes the job.
	if _, err := e.command(client.GitRebasePlanEndCommand("end", acpTarget, job)); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(e.current(), job); th.ID != "" {
		t.Fatal("end kept the job thread")
	}
}

func TestRebasePlanJobInvalidAnswersAndFix(t *testing.T) {
	e, fleet, root, _, o := planSetup(t)
	// No block at all.
	job := startPlan(t, e, "plan", o["base"], "FAKE-REPLY I would squash B into A.")
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalInvalid || len(sum.Errors) != 1 || !strings.Contains(sum.Errors[0], "no ```tui-rebase-plan block") {
		t.Fatalf("summary: %+v", sum)
	}
	p := proposalOf(t, e, root, job)
	if p.State != protocol.GitProposalInvalid || p.Raw != "I would squash B into A." || client.ProposalLoadable(p) {
		t.Fatalf("invalid proposal: %+v", p)
	}
	// A revise must name the revision shown.
	_, err := e.command(client.GitRebasePlanReviseCommand("stale", acpTarget, job, sum.Revision-1, ""))
	wantGitCode(t, err, "stale_proposal")
	// Missing commit: the validator's error is kept and sent back.
	wire := goodWire(o)
	wire.Entries = wire.Entries[:3]
	mustGit(t, e, client.GitRebasePlanReviseCommand("fix1", acpTarget, job, sum.Revision, planAnswer(t, wire)), protocol.GitStateSucceeded)
	sum = settledProposal(t, e, job, sum.Revision+1)
	if sum.State != protocol.GitProposalInvalid || sum.Turns != 2 || !strings.Contains(strings.Join(sum.Errors, ";"), "missing from the plan") {
		t.Fatalf("summary: %+v", sum)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if last := prompts[len(prompts)-1]; !strings.Contains(last, "no ```tui-rebase-plan block") || !strings.Contains(last, "Answer again") {
		t.Fatalf("fix-up prompt: %q", last)
	}
	// The fix-up turn: the errors go back to the agent with the user's words.
	mustGit(t, e, client.GitRebasePlanReviseCommand("fix2", acpTarget, job, sum.Revision, "Keep D.\n"+planAnswer(t, goodWire(o))), protocol.GitStateSucceeded)
	sum = settledProposal(t, e, job, sum.Revision+1)
	prompts, _, _, _ = fleet.last().snapshot()
	if last := prompts[len(prompts)-1]; !strings.Contains(last, "missing from the plan") || !strings.Contains(last, "Keep D.") {
		t.Fatalf("fix-up prompt: %q", last)
	}
	if sum.State != protocol.GitProposalProposed || sum.Turns != 3 || len(sum.Errors) != 0 {
		t.Fatalf("fixed: %+v", sum)
	}
}

func TestRebasePlanJobRejectsExecAndOtherActions(t *testing.T) {
	e, _, _, _, o := planSetup(t)
	wire := goodWire(o)
	wire.Entries = append(wire.Entries, protocol.GitRebaseEntry{Action: "exec", Message: "make test"}, protocol.GitRebaseEntry{Action: "label", Commit: "x"})
	job := startPlan(t, e, "plan", o["base"], planAnswer(t, wire))
	sum := settledProposal(t, e, job, 0)
	joined := strings.Join(sum.Errors, ";")
	if sum.State != protocol.GitProposalInvalid || !strings.Contains(joined, "entry 5: exec is never allowed") || !strings.Contains(joined, `action "label" is not supported`) {
		t.Fatalf("exec: %+v", sum)
	}
}

func TestRebasePlanJobMergesAndPublished(t *testing.T) {
	e, _, root, git, o := planSetup(t)
	// Publish A..B, then merge a side branch into main and add E.
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	git("remote", "add", "origin", remote)
	git("push", "-q", "origin", o["B"]+":refs/heads/main")
	git("fetch", "-q", "origin")
	git("checkout", "-q", "-b", "side", o["base"])
	writeFile(t, root, "s.txt", "side\n")
	git("add", ".")
	git("commit", "-q", "-m", "S")
	side := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-edit", "side")
	merge := git("rev-parse", "HEAD")
	plan := planOf(t, root, o["base"], "")
	if plan.MergeCount != 1 || !plan.Published {
		t.Fatalf("setup: %+v", plan)
	}
	entries := func(extra ...protocol.GitRebaseEntry) protocol.GitRebaseProposalWire {
		w := protocol.GitRebaseProposalWire{Version: 1}
		for _, c := range plan.Commits {
			if !c.Merge {
				w.Entries = append(w.Entries, protocol.GitRebaseEntry{Action: "pick", Commit: c.Oid})
			}
		}
		w.Entries = append(w.Entries, extra...)
		return w
	}
	// Naming the merge commit is invalid; it is dropped by the rebase.
	job := startPlan(t, e, "plan", o["base"], planAnswer(t, entries(protocol.GitRebaseEntry{Action: "pick", Commit: merge})))
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalInvalid || !strings.Contains(strings.Join(sum.Errors, ";"), "merge commit") || sum.MergeCount != 1 || !sum.Published {
		t.Fatalf("merge named: %+v", sum)
	}
	job2 := startPlan(t, e, "plan2", o["base"], planAnswer(t, entries()))
	sum = settledProposal(t, e, job2, 0)
	p := proposalOf(t, e, root, job2)
	if sum.State != protocol.GitProposalProposed || p.Plan.MergeCount != 1 || !p.Plan.Published || len(p.Entries) != 5 {
		t.Fatalf("proposal: %+v %+v", sum, p.Entries)
	}
	// The agent cannot acknowledge: the start still needs the user's.
	fresh := planOf(t, root, p.Base, p.Onto)
	_, err := e.command(client.GitRebaseInteractiveCommand("rebase", acpTarget, fresh, p.Entries, client.RebaseOptions{AcknowledgePublished: true}))
	wantGitCode(t, err, "merges_unacknowledged")
	_ = side
}

func TestRebasePlanJobTaintedWhenTheCheckoutChanges(t *testing.T) {
	e, _, root, _, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], "FAKE-WRITE new.txt agent was here\n"+planAnswer(t, goodWire(o)))
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalTainted || !strings.Contains(sum.Reason, "new.txt") || sum.Entries != 0 {
		t.Fatalf("tainted: %+v", sum)
	}
	p := proposalOf(t, e, root, job)
	if p.State != protocol.GitProposalTainted || client.ProposalLoadable(p) || len(p.Changes) != 1 || len(p.Entries) != 4 {
		t.Fatalf("tainted proposal: %+v", p)
	}
	if readText(t, root, "new.txt") != "agent was here" {
		t.Fatal("the agent's change was repaired silently")
	}
	// A Git write by the agent (the index) taints too.
	job2 := startPlan(t, e, "plan2", o["base"], "FAKE-GIT add new.txt\n"+planAnswer(t, goodWire(o)))
	sum = settledProposal(t, e, job2, 0)
	if sum.State != protocol.GitProposalTainted || !strings.Contains(sum.Reason, "index") {
		t.Fatalf("index taint: %+v", sum)
	}
}

func TestRebasePlanJobCancelDoesNotHoldTheCheckout(t *testing.T) {
	e, _, root, git, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], "cancel me")
	waitFor(t, e, "planning", func(s protocol.Snapshot) bool { return threadOf(s, job).State == "running" })
	// Other threads and lease-taking Git writes proceed meanwhile.
	other := startACPThread(t, e, "other", "say pong")
	waitFor(t, e, "other thread done", func(s protocol.Snapshot) bool {
		th := threadOf(s, other)
		return th.State == "idle" && len(th.Queue) == 0 && th.TurnID != ""
	})
	writeFile(t, root, "e.txt", "e\n")
	mustGit(t, e, client.GitStageCommand("stage", acpTarget, entryOf(t, root, "e.txt", protocol.GitGroupUntracked)), protocol.GitStateSucceeded)
	if th := threadOf(e.current(), job); !th.Job.Proposal.Concurrent {
		t.Fatal("concurrent work not noted")
	}
	_, err := e.command(client.GitRebasePlanEndCommand("end-early", acpTarget, job))
	wantGitCode(t, err, "job_running")
	if _, err := e.command(client.GitRebasePlanCancelCommand("cancel", acpTarget, job)); err != nil {
		t.Fatal(err)
	}
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalCancelled {
		t.Fatalf("cancelled: %+v", sum)
	}
	_, err = e.command(client.GitRebasePlanCancelCommand("cancel2", acpTarget, job))
	wantGitCode(t, err, "not_active")
	// The job thread refuses ordinary commands.
	_, err = e.command(protocol.Command{Version: 1, ID: "send", Kind: "prompt.send", ThreadID: job, Text: "hi"})
	wantGitCode(t, err, "job_thread")
	_ = git
}

func TestRebasePlanJobDeclinesApprovals(t *testing.T) {
	e, _, root, _, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], "ask permission")
	sum := settledProposal(t, e, job, 0)
	th := threadOf(e.current(), job)
	if sum.DeclinedApprovals != 1 || len(th.Requests) != 0 || !strings.Contains(activityOf(th, "agent-"+sum.PromptID).Text, "decision reject") {
		t.Fatalf("approval: %+v %+v", sum, th.Activity)
	}
	if p := proposalOf(t, e, root, job); !strings.Contains(p.Enforcement, "Declined approvals: 1") {
		t.Fatalf("enforcement: %s", p.Enforcement)
	}
}

func TestRebasePlanJobRefusals(t *testing.T) {
	e, _, root, git, o := planSetup(t)
	settings := fakeSettings()
	_, err := e.command(client.GitRebasePlanStartCommand("empty", acpTarget, "claude", &settings, "refs/heads/main", "", "", ""))
	wantGitCode(t, err, "empty_range")
	_, err = e.command(client.GitRebasePlanStartCommand("fp", acpTarget, "claude", &settings, o["base"], "", "0123", ""))
	wantGitCode(t, err, "stale_plan")
	_, err = e.command(client.GitRebasePlanStartCommand("fixture", acpTarget, agent.FixtureID, nil, o["base"], "", "", ""))
	wantGitCode(t, err, "not_supported")
	_, err = e.command(client.GitRebasePlanStartCommand("bad", acpTarget, "claude", nil, "HEAD~2", "", "", ""))
	wantGitCode(t, err, "invalid")
	git("checkout", "-q", "--detach")
	_, err = e.command(client.GitRebasePlanStartCommand("detached", acpTarget, "claude", nil, o["base"], "", "", ""))
	wantGitCode(t, err, "detached")
	git("checkout", "-q", "main")
	// A dirty tree can be cleaned before starting: planning is allowed and
	// the read reports what a start would say now.
	writeFile(t, root, "a.txt", "dirty\n")
	job := startPlan(t, e, "dirty", o["base"], planAnswer(t, goodWire(o)))
	settledProposal(t, e, job, 0)
	if p := proposalOf(t, e, root, job); p.State != protocol.GitProposalProposed || p.Blocked != "dirty_tree" {
		t.Fatalf("dirty: %s %s", p.State, p.Blocked)
	}
	if _, err := e.readRebaseProposal(context.Background(), root, "nope"); err == nil {
		t.Fatal("an unknown job was read")
	}
}

func TestRebasePlanJobTurnBound(t *testing.T) {
	th := &protocol.Thread{State: "idle", Job: &protocol.ThreadJob{Kind: protocol.ThreadJobRebasePlan, Checkout: "/r",
		Proposal: &protocol.GitRebaseProposalSummary{State: protocol.GitProposalInvalid, Revision: 3, Turns: protocol.GitRebasePlanJobTurnsMax}}}
	wantGitCode(t, checkPlanRevise(th, "/r", protocol.GitRebasePlanJob{ExpectedRevision: 3}), "too_many_turns")
	th.Job.Proposal.Turns = 1
	if err := checkPlanRevise(th, "/r", protocol.GitRebasePlanJob{ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	th.Job.Proposal.State = protocol.GitProposalRunning
	wantGitCode(t, checkPlanRevise(th, "/r", protocol.GitRebasePlanJob{ExpectedRevision: 3}), "job_running")
}

func TestRebasePlanJobRestart(t *testing.T) {
	e, _, root, _, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], planAnswer(t, goodWire(o)))
	sum := settledProposal(t, e, job, 0)
	// A stored proposal survives a restart.
	s := e.current()
	recoverThreads(&s)
	recoverGitOps(&s)
	e2 := newEngine(s, e.store)
	e2.baselineDir = e.baselineDir
	if p := proposalOf(t, e2, root, job); p.State != protocol.GitProposalProposed || len(p.Entries) != 4 || p.Summary.Revision != sum.Revision {
		t.Fatalf("after restart: %+v", p)
	}
	// A turn that ended before its answer was checked is checked at startup.
	s = e.current()
	pending := ptrThread(s, job)
	pending.Job.Proposal.State, pending.Job.Proposal.ResultID = protocol.GitProposalRunning, ""
	recoverGitOps(&s)
	e3 := newEngine(s, e.store)
	e3.baselineDir = e.baselineDir
	e3.evaluatePendingPlanJobs()
	if got := settledProposal(t, e3, job, 0); got.State != protocol.GitProposalProposed {
		t.Fatalf("pending check: %+v", got)
	}
	e3.planWG.Wait()
	// A turn running at the restart fails and needs an explicit revise.
	s = e.current()
	running := ptrThread(s, job)
	running.State = "running"
	running.Job.Proposal.State = protocol.GitProposalRunning
	running.Job.Proposal.PromptID = running.TurnID
	recoverThreads(&s)
	recoverGitOps(&s)
	if th := threadOf(s, job); th.Job.Proposal.State != protocol.GitProposalFailed || th.State != "interrupted" || !th.NeedsResume || !strings.Contains(th.Job.Proposal.Reason, "restarted") {
		t.Fatalf("running at restart: %+v %+v", th.State, th.Job.Proposal)
	}
}

func TestRebasePlanPermissionsPolicy(t *testing.T) {
	a := protocol.Agent{ID: "claude", Kind: agent.KindACP, Command: "builtin:claude", Version: acpbridge.ClaudeIdentity}
	if v, gated, ok := planPermissions(a); v != "plan" || !gated || !ok {
		t.Fatalf("claude: %q %t %t", v, gated, ok)
	}
	c := protocol.Agent{ID: "codex", Kind: agent.KindACP, Command: "builtin:codex", Version: acpbridge.CodexIdentity}
	if v, gated, ok := planPermissions(c); v != "read-only" || gated || !ok {
		t.Fatalf("codex: %q %t %t", v, gated, ok)
	}
	for _, unverified := range []protocol.Agent{
		{ID: "claude", Kind: agent.KindACP, Command: "builtin:claude", Version: "fake-acp 0.0.1"},
		{ID: "claude", Kind: agent.KindACP, Command: "/usr/bin/claude-acp", Version: acpbridge.ClaudeIdentity},
		{ID: "codex", Kind: agent.KindACP, Command: "builtin:codex", Version: acpbridge.ClaudeIdentity},
	} {
		if _, _, ok := planPermissions(unverified); ok {
			t.Fatalf("treated as verified: %+v", unverified)
		}
	}
}

// On a verified built-in bridge the job's agent is opened read-only and
// runs with the bridge's read-only permission value, whatever was selected.
func TestRebasePlanJobOpensVerifiedBridgesReadOnly(t *testing.T) {
	e, fleet, root, _, o := planSetup(t)
	e.mu.Lock()
	record := agent.Find(&e.snap, "claude")
	record.Command, record.Version = "builtin:claude", acpbridge.ClaudeIdentity
	e.mu.Unlock()
	job := startPlan(t, e, "plan", o["base"], planAnswer(t, goodWire(o)))
	sum := settledProposal(t, e, job, 0)
	th := threadOf(e.current(), job)
	if sum.State != protocol.GitProposalProposed || !sum.ReadOnlyMode || !sum.ApprovalGated || sum.Permissions != "plan" || th.Selected.Permissions != "plan" || th.Effective.Permissions != "plan" {
		t.Fatalf("read-only job: %+v %+v %+v", sum, th.Selected, th.Effective)
	}
	fleet.mu.Lock()
	launches := fleet.readOnlyLaunches
	fleet.mu.Unlock()
	if launches != 1 {
		t.Fatalf("read-only launches: %d", launches)
	}
	if p := proposalOf(t, e, root, job); !strings.Contains(p.Enforcement, "plan permission mode") {
		t.Fatalf("enforcement: %s", p.Enforcement)
	}
	// Ordinary threads of the same agent are not read-only.
	startACPThread(t, e, "ordinary", "say pong")
	waitFor(t, e, "ordinary launch", func(protocol.Snapshot) bool { return fleet.count() == 2 })
	fleet.mu.Lock()
	launches = fleet.readOnlyLaunches
	fleet.mu.Unlock()
	if launches != 1 {
		t.Fatalf("an ordinary thread was opened read-only")
	}
}

func TestRebaseProposalParsing(t *testing.T) {
	plan := protocol.GitRebasePlan{Commits: []protocol.GitRebasePlanCommit{
		{Oid: "aaaaaaa1" + strings.Repeat("0", 32)}, {Oid: "aaaaaaa2" + strings.Repeat("0", 32)}, {Oid: "bbbbbbb1" + strings.Repeat("0", 32)},
	}}
	block := func(body string) string { return "text\n```tui-rebase-plan\n" + body + "\n```\n" }
	for name, c := range map[string]struct{ raw, want string }{
		"two blocks": {block(`{"version":1}`) + block(`{"version":1}`), "exactly one"},
		"unclosed":   {"```tui-rebase-plan\n{}", "not closed"},
		"other lang": {"```json\n{\"version\":1}\n```", "no ```tui-rebase-plan block"},
		"unknown":    {block(`{"version":1,"entries":[],"extra":1}`), "unknown field"},
		"trailing":   {block(`{"version":1} {}`), "more than one JSON value"},
		"version":    {block(`{"version":2,"entries":[{"action":"pick","commit":"bbbbbbb1"}]}`), "version must be 1"},
		"ambiguous":  {block(`{"version":1,"entries":[{"action":"pick","commit":"aaaaaaa"}]}`), "ambiguous"},
		"short":      {block(`{"version":1,"entries":[{"action":"pick","commit":"bbb"}]}`), "not a commit hash"},
	} {
		if _, errs := parseProposal(c.raw, plan); len(errs) == 0 || !strings.Contains(strings.Join(errs, ";"), c.want) {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// A block inside another fence is not read; tildes work too.
	raw := "```text\n```tui-rebase-plan\n```\n~~~tui-rebase-plan\n" +
		`{"version":1,"entries":[{"action":"pick","commit":"aaaaaaa1"},{"action":"drop","commit":"aaaaaaa2"},{"action":"pick","commit":"bbbbbbb1"}]}` + "\n~~~"
	w, errs := parseProposal(raw, plan)
	if len(errs) != 0 || w.Entries[1].Commit != plan.Commits[1].Oid {
		t.Fatalf("parsed: %+v %v", w, errs)
	}
}

// Changes to what a later rebase runs (hooks, configuration) or to refs
// taint, as do edits hidden by skip-worktree.
func TestRebasePlanJobTaintsRepositoryChanges(t *testing.T) {
	for name, c := range map[string]struct{ act, want string }{
		"hook":     {"FAKE-WRITE .git/hooks/post-rewrite #!/bin/sh\\necho pwned", "hooks directory"},
		"config":   {"FAKE-GIT config core.fsmonitor /tmp/evil", "Git configuration"},
		"exclude":  {"FAKE-WRITE .git/info/exclude *.txt", "info/"},
		"ref":      {"FAKE-GIT branch sneaky", "refs"},
		"stash":    {"FAKE-WRITE a.txt stashed\nFAKE-GIT stash", "refs"},
		"skip":     {"FAKE-GIT update-index --skip-worktree d.txt\nFAKE-WRITE d.txt changed-by-agent", "skip-worktree"},
		"assume":   {"FAKE-GIT update-index --assume-unchanged c.txt", "skip-worktree or assume-unchanged"},
		"symbolic": {"FAKE-GIT symbolic-ref HEAD refs/heads/other", "HEAD"},
	} {
		t.Run(name, func(t *testing.T) {
			e, _, root, _, o := planSetup(t)
			job := startPlan(t, e, "plan", o["base"], c.act+"\n"+planAnswer(t, goodWire(o)))
			sum := settledProposal(t, e, job, 0)
			if sum.State != protocol.GitProposalTainted || !strings.Contains(sum.Reason, c.want) {
				t.Fatalf("%s: %s %q", name, sum.State, sum.Reason)
			}
			if client.ProposalLoadable(proposalOf(t, e, root, job)) {
				t.Fatal("a tainted proposal is loadable")
			}
		})
	}
}

// A second edit of an already changed file, a change among thousands of
// status entries and a submodule's working tree all taint.
func TestRebasePlanJobPinsTheCompleteStatus(t *testing.T) {
	e, _, root, git, o := planSetup(t)
	writeFile(t, root, "d.txt", "dirty before the job\n")
	for i := 0; i < 2100; i++ {
		writeFile(t, root, fmt.Sprintf("u/f%04d", i), "x")
	}
	time.Sleep(10 * time.Millisecond) // a distinct mtime for the second edit
	job := startPlan(t, e, "plan", o["base"], "FAKE-WRITE d.txt dirty again, by the agent\n"+planAnswer(t, goodWire(o)))
	if sum := settledProposal(t, e, job, 0); sum.State != protocol.GitProposalTainted || !strings.Contains(sum.Reason, "working tree") {
		t.Fatalf("re-edit: %s %q", sum.State, sum.Reason)
	}
	job2 := startPlan(t, e, "plan2", o["base"], "FAKE-WRITE zzz.txt agent\n"+planAnswer(t, goodWire(o)))
	if sum := settledProposal(t, e, job2, 0); sum.State != protocol.GitProposalTainted {
		t.Fatalf("many entries: %s %q", sum.State, sum.Reason)
	}
	// A submodule whose working tree the agent changes.
	sub := t.TempDir()
	gitIn(t, sub, "init", "-q")
	writeFile(t, sub, "s.txt", "s\n")
	gitIn(t, sub, "add", ".")
	gitIn(t, sub, "commit", "-q", "-m", "s")
	git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "mod")
	git("commit", "-q", "-m", "add submodule")
	git("clean", "-fdq")
	git("checkout", "-q", "--", "d.txt")
	job3 := startPlan(t, e, "plan3", o["base"], "FAKE-WRITE mod/s.txt changed\n"+planAnswer(t, goodWire(o)))
	if sum := settledProposal(t, e, job3, 0); sum.State != protocol.GitProposalTainted {
		t.Fatalf("submodule: %s %q", sum.State, sum.Reason)
	}
}

// Every dispatch failure ends the revision or leaves it cancellable.
func TestRebasePlanJobDispatchFailuresEndTheRevision(t *testing.T) {
	e, _, _, _, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], "FAKE-REPLY no block")
	sum := settledProposal(t, e, job, 0)
	// The agent is not ready.
	e.mu.Lock()
	agent.Find(&e.snap, "claude").State = agent.StateUnavailable
	e.mu.Unlock()
	mustGit(t, e, client.GitRebasePlanReviseCommand("rev1", acpTarget, job, sum.Revision, ""), protocol.GitStateSucceeded)
	sum = settledProposal(t, e, job, sum.Revision+1)
	if sum.State != protocol.GitProposalFailed || !strings.Contains(sum.Reason, "not ready") {
		t.Fatalf("not ready: %+v", sum)
	}
	e.mu.Lock()
	agent.Find(&e.snap, "claude").State = agent.StateReady
	ptrThread(e.snap, job).WorktreeID = "gone" // a managed worktree that no longer exists
	e.mu.Unlock()
	mustGit(t, e, client.GitRebasePlanReviseCommand("rev2", acpTarget, job, sum.Revision, ""), protocol.GitStateSucceeded)
	sum = settledProposal(t, e, job, sum.Revision+1)
	if sum.State != protocol.GitProposalFailed || !strings.Contains(sum.Reason, "worktree") {
		t.Fatalf("worktree: %+v", sum)
	}
	// Storage failing: the revision is never dispatched and stays cancellable.
	e.mu.Lock()
	ptrThread(e.snap, job).WorktreeID = ""
	e.flushErr = fmt.Errorf("disk full")
	e.mu.Unlock()
	mustGit(t, e, client.GitRebasePlanReviseCommand("rev3", acpTarget, job, sum.Revision, ""), protocol.GitStateSucceeded)
	e.mu.Lock()
	e.flushErr = nil
	e.mu.Unlock()
	if th := threadOf(e.current(), job); th.Job.Proposal.State != protocol.GitProposalRunning || len(th.Queue) != 1 {
		t.Fatalf("queued: %+v", th.Job.Proposal)
	}
	if _, err := e.command(client.GitRebasePlanCancelCommand("cancel", acpTarget, job)); err != nil {
		t.Fatal(err)
	}
	th := threadOf(e.current(), job)
	if th.Job.Proposal.State != protocol.GitProposalCancelled || len(th.Queue) != 0 {
		t.Fatalf("cancel before start: %+v %d", th.Job.Proposal, len(th.Queue))
	}
	if _, err := e.command(client.GitRebasePlanReviseCommand("rev4", acpTarget, job, th.Job.Proposal.Revision, "FAKE-REPLY again")); err != nil {
		t.Fatalf("revise after the cancel: %v", err)
	}
	settledProposal(t, e, job, th.Job.Proposal.Revision+1)
	e.planWG.Wait()
}

// The answer is the final agent message segment: a block before a question
// (an example, say) does not count.
func TestRebasePlanJobAnswerAfterAQuestion(t *testing.T) {
	e, fleet, _, _, o := planSetup(t)
	fleet.nativeQuestions = true
	example := "FAKE-BEFORE For example:\nFAKE-BEFORE ```tui-rebase-plan\nFAKE-BEFORE {\"version\":1,\"entries\":[]}\nFAKE-BEFORE ```\n"
	job := startPlan(t, e, "plan", o["base"], "ask native question\n"+example+planAnswer(t, goodWire(o)))
	s := waitFor(t, e, "question", func(s protocol.Snapshot) bool {
		th := threadOf(s, job)
		return th.State == "waiting" && len(th.Requests) == 1
	})
	r := threadOf(s, job).Requests[0]
	if _, err := e.command(nativeAnswer(job, r)); err != nil {
		t.Fatal(err)
	}
	sum := settledProposal(t, e, job, 0)
	if sum.State != protocol.GitProposalProposed {
		t.Fatalf("after a question: %+v", sum)
	}
}

// encoding/json would take a case-mismatched or repeated key; the parser
// does not.
func TestRebaseProposalStrictKeys(t *testing.T) {
	plan := protocol.GitRebasePlan{Commits: []protocol.GitRebasePlanCommit{{Oid: "aaaaaaa1" + strings.Repeat("0", 32)}}}
	for name, body := range map[string]string{
		"case":            `{"VERSION":1,"entries":[{"action":"pick","commit":"aaaaaaa1"}]}`,
		"duplicate":       `{"version":1,"entries":[{"action":"exec"}],"entries":[{"action":"pick","commit":"aaaaaaa1"}]}`,
		"entry case":      `{"version":1,"entries":[{"Action":"pick","commit":"aaaaaaa1"}]}`,
		"entry duplicate": `{"version":1,"entries":[{"action":"drop","action":"pick","commit":"aaaaaaa1"}]}`,
		"not an object":   `[1]`,
	} {
		if _, errs := parseProposal("```tui-rebase-plan\n"+body+"\n```", plan); len(errs) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	if _, errs := parseProposal("```tui-rebase-plan\n"+`{"version":1,"entries":[{"action":"pick","commit":"aaaaaaa1"}]}`+"\n```", plan); len(errs) != 0 {
		t.Fatal(errs)
	}
}

// A truncated log read is marked, the prompt stays within its bound and
// repository text is fenced by a per-job boundary.
func TestRebasePlanPromptBoundsAndOmissions(t *testing.T) {
	_, _, root, _, o := planSetup(t)
	saved := planLogMax
	planLogMax = 300
	defer func() { planLogMax = saved }()
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	plan := planOf(t, root, o["base"], "")
	prompt, err := planPrompt(context.Background(), g, plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) > planPromptMax || !strings.Contains(prompt, "stopped at 8 MiB") || !strings.Contains(prompt, "BEGIN-REPOSITORY-DATA-") {
		t.Fatalf("prompt (%d bytes): %s", len(prompt), prompt)
	}
	begin := prompt[strings.Index(prompt, "BEGIN-REPOSITORY-DATA-")+len("BEGIN-REPOSITORY-DATA-"):]
	nonce := begin[:strings.IndexAny(begin, " \n")]
	if len(nonce) != 24 || !strings.Contains(prompt, "END-REPOSITORY-DATA-"+nonce) {
		t.Fatalf("boundary %q", nonce)
	}
	again, _ := planPrompt(context.Background(), g, plan, "")
	if strings.Contains(again, nonce) {
		t.Fatal("the boundary is not per prompt")
	}
}

// What Git and maintenance do by themselves does not taint; what a later
// rebase would run does.
func TestRebasePlanJobPinFalseTaintsAndHooks(t *testing.T) {
	for name, c := range map[string]struct {
		pre     func(root string, git func(...string) string, outside string)
		act     func(root, outside string) string
		tainted string
	}{
		"gc":     {act: func(string, string) string { return "FAKE-GIT gc --quiet" }},
		"status": {act: func(string, string) string { return "FAKE-GIT status" }},
		"reflogs": {act: func(string, string) string {
			return "FAKE-WRITE .git/FETCH_HEAD x\nFAKE-WRITE .git/ORIG_HEAD y\nFAKE-WRITE .git/logs/HEAD z"
		}},
		"pack-refs":       {act: func(string, string) string { return "FAKE-GIT pack-refs --all" }},
		"remote-tracking": {act: func(string, string) string { return "FAKE-GIT update-ref refs/remotes/origin/main HEAD" }},
		"prefetch":        {act: func(string, string) string { return "FAKE-GIT update-ref refs/prefetch/remotes/origin/main HEAD" }},
		"merge-msg":       {act: func(string, string) string { return "FAKE-WRITE .git/MERGE_MSG x" }},
		"hookspath-null": {pre: func(_ string, git func(...string) string, _ string) { git("config", "core.hooksPath", "/dev/null") },
			act: func(string, string) string { return "" }},
		"relative-hookspath": {pre: func(root string, git func(...string) string, _ string) {
			git("config", "core.hooksPath", ".githooks")
			writeFile(t, root, ".githooks/.keep", "")
			writeFile(t, root, ".git/info/exclude", ".githooks/\n")
		}, act: func(string, string) string { return "FAKE-WRITE .githooks/post-rewrite evil" }, tainted: "hooks directory"},
		"symlinked-hook": {pre: func(root string, _ func(...string) string, outside string) {
			writeFile(t, outside, "script.sh", "ok")
			if err := os.Symlink(filepath.Join(outside, "script.sh"), filepath.Join(root, ".git/hooks/post-rewrite")); err != nil {
				t.Fatal(err)
			}
		}, act: func(root, outside string) string {
			rel, _ := filepath.Rel(root, filepath.Join(outside, "script.sh"))
			return "FAKE-WRITE " + rel + " evil"
		}, tainted: "hooks directory"},
		"other-worktree": {pre: func(root string, git func(...string) string, _ string) {
			git("worktree", "add", "-q", "-b", "side", root+"-wt")
		}, act: func(root, _ string) string {
			return "FAKE-GIT -C " + root + "-wt commit -q --allow-empty -m other"
		}, tainted: "local branches"},
	} {
		t.Run(name, func(t *testing.T) {
			e, _, root, git, o := planSetup(t)
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".git/hooks"), 0o755); err != nil {
				t.Fatal(err)
			}
			if c.pre != nil {
				c.pre(root, git, outside)
			}
			job := startPlan(t, e, "plan", o["base"], c.act(root, outside)+"\n"+planAnswer(t, goodWire(o)))
			sum := settledProposal(t, e, job, 0)
			switch {
			case c.tainted == "" && sum.State != protocol.GitProposalProposed:
				t.Fatalf("false taint: %s %q", sum.State, sum.Reason)
			case c.tainted != "" && (sum.State != protocol.GitProposalTainted || !strings.Contains(sum.Reason, c.tainted)):
				t.Fatalf("not tainted: %s %q", sum.State, sum.Reason)
			case c.tainted != "" && (!strings.HasPrefix(sum.Reason, "the repository changed during the planning turn") || !strings.Contains(sum.Reason, "no other application work")):
				t.Fatalf("wording: %q", sum.Reason)
			}
		})
	}
}

// Application Git writes without the lease, also in another worktree of
// the repository, are noted as concurrent work.
func TestRebasePlanJobConcurrentByRepository(t *testing.T) {
	e, _, root, git, o := planSetup(t)
	wt := root + "-wt"
	git("worktree", "add", "-q", "-b", "side", wt)
	t.Cleanup(func() { os.RemoveAll(wt) })
	addGitThread(e, "p-wt", "t-wt", wt)
	e.mu.Lock()
	same := e.sameRepositoryLocked(root, wt)
	e.mu.Unlock()
	if !same {
		t.Fatal("worktrees of one repository are not matched")
	}
	job := startPlan(t, e, "plan", o["base"], "cancel me")
	waitFor(t, e, "planning", func(s protocol.Snapshot) bool { return threadOf(s, job).State == "running" })
	mustGit(t, e, client.GitBranchCreateCommand("branch", client.GitTarget{ProjectID: "p-wt"}, "fromwt", gitIn(t, wt, "rev-parse", "HEAD")), protocol.GitStateSucceeded)
	if !threadOf(e.current(), job).Job.Proposal.Concurrent {
		t.Fatal("a branch created in another worktree was not noted")
	}
	if _, err := e.command(client.GitRebasePlanCancelCommand("cancel", acpTarget, job)); err != nil {
		t.Fatal(err)
	}
	settledProposal(t, e, job, 0)
}

// A cancel between the turn's end and its check (the finished turn not yet
// evaluated) must not relabel the finished turn.
func TestRebasePlanJobCancelAfterTheTurnEnded(t *testing.T) {
	e, _, _, _, o := planSetup(t)
	job := startPlan(t, e, "plan", o["base"], planAnswer(t, goodWire(o)))
	sum := settledProposal(t, e, job, 0)
	e.mu.Lock()
	th := ptrThread(e.snap, job)
	th.Job.Proposal.State = protocol.GitProposalRunning // the window: turn ended, check not begun
	e.mu.Unlock()
	if th.TurnID != sum.PromptID {
		t.Fatalf("setup: %s %s", th.TurnID, sum.PromptID)
	}
	_, err := e.command(client.GitRebasePlanCancelCommand("cancel", acpTarget, job))
	wantGitCode(t, err, "job_running")
	if got := threadOf(e.current(), job).Job.Proposal.State; got != protocol.GitProposalRunning {
		t.Fatalf("relabelled: %s", got)
	}
}
