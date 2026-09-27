package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// TestMain runs the rebase editor helper when Git starts this test binary
// as its editor (rebaseHelperExecutable is the running executable).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == rebaseHelperCommand {
		if err := RunRebaseHelper(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "git-rebase-helper:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// rebaseSetup is a repository with base and commits A..E, each adding its
// own file (a.txt ...) with a body, authored by Test; the server commits as
// Writer.
func rebaseSetup(t *testing.T) (*engine, string, func(...string) string, map[string]string) {
	t.Helper()
	if !gitRebaseInteractiveSupported() {
		t.Skip("Git too old for interactive rebase")
	}
	e, root, git := gitWriteSetup(t)
	e.rebaseDir = t.TempDir()
	writeFile(t, root, "base.txt", "base\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	oids := map[string]string{"base": git("rev-parse", "HEAD")}
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		writeFile(t, root, strings.ToLower(n)+".txt", n+"\n")
		git("add", ".")
		git("commit", "-q", "-m", n, "-m", "body of "+n)
		oids[n] = git("rev-parse", "HEAD")
	}
	return e, root, git, oids
}

func planOf(t *testing.T, root, base, onto string) protocol.GitRebasePlan {
	t.Helper()
	p, err := readRebasePlan(context.Background(), root, base, onto)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func subjects(git func(...string) string, rng string) []string {
	return strings.Split(git("log", "--format=%s", rng), "\n")
}

func pick(oid string) protocol.GitRebaseEntry {
	return protocol.GitRebaseEntry{Action: protocol.GitRebasePick, Commit: oid}
}

func entry(action, oid, message string) protocol.GitRebaseEntry {
	return protocol.GitRebaseEntry{Action: action, Commit: oid, Message: message}
}

func rebaseCmd(id string, plan protocol.GitRebasePlan, entries []protocol.GitRebaseEntry, o client.RebaseOptions) protocol.Command {
	return client.GitRebaseInteractiveCommand(id, gitTarget, plan, entries, o)
}

func interactiveOf(t *testing.T, st protocol.GitOperationState) *protocol.GitRebaseProgress {
	t.Helper()
	if st.Interactive == nil || !st.Interactive.Plan || st.Source != protocol.GitOperationSourceApp {
		t.Fatalf("not an application interactive rebase: %+v", st)
	}
	return st.Interactive
}

func TestRebasePlanRead(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	_ = e
	p := planOf(t, root, o["base"], "")
	if p.Blocked != "" || p.Branch != "main" || p.HeadOid != o["E"] || p.BaseOid != o["base"] || p.OntoOid != o["base"] || p.Fingerprint == "" || len(p.Commits) != 5 {
		t.Fatalf("plan: %+v", p)
	}
	for i, n := range []string{"A", "B", "C", "D", "E"} {
		c := p.Commits[i]
		if c.Oid != o[n] || c.Subject != n || c.Body != "body of "+n || c.AuthorName != "Test" || c.Merge || c.Published || len(c.Parents) != 1 {
			t.Fatalf("commit %d: %+v", i, c)
		}
	}
	if p.Commits[0].Parents[0] != o["base"] || p.Published || p.MergeCount != 0 || len(p.UpdateRefs) != 0 {
		t.Fatalf("plan details: %+v", p)
	}
	// A branch at B would move with --update-refs; a branch checked out
	// elsewhere would not.
	git("branch", "stack", o["B"])
	git("worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "-b", "busy", o["C"])
	p2 := planOf(t, root, o["base"], "")
	if len(p2.UpdateRefs) != 1 || p2.UpdateRefs[0] != (protocol.GitRebaseUpdateRef{Ref: "refs/heads/stack", Oid: o["B"]}) || p2.Fingerprint == p.Fingerprint {
		t.Fatalf("update refs: %+v", p2.UpdateRefs)
	}
	// From a commit ("interactive rebase from here" at C: base is B) and
	// the root.
	from := planOf(t, root, o["B"], "")
	if len(from.Commits) != 3 || from.Commits[0].Oid != o["C"] {
		t.Fatalf("from here: %+v", from)
	}
	all := planOf(t, root, "root", "")
	if !all.Root || all.OntoOid != "" || len(all.Commits) != 6 || all.Commits[0].Subject != "base" || len(all.Commits[0].Parents) != 0 || all.Blocked != "" {
		t.Fatalf("root plan: %+v", all)
	}
	// Blocked states still list the commits.
	writeFile(t, root, "a.txt", "dirty\n")
	if d := planOf(t, root, o["base"], ""); d.Blocked != "dirty_tree" || len(d.Commits) != 5 {
		t.Fatalf("dirty plan: %+v", d)
	}
	git("checkout", "--", "a.txt")
	if d := planOf(t, root, o["E"], ""); d.Blocked != "empty_range" {
		t.Fatalf("empty plan: %+v", d)
	}
	if _, err := readRebasePlan(context.Background(), root, "HEAD~1", ""); err == nil {
		t.Fatal("a revision expression was accepted as base")
	}
	// Published: commits on a remote-tracking ref.
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	git("remote", "add", "origin", remote)
	git("push", "-q", "origin", o["B"]+":refs/heads/main")
	git("fetch", "-q", "origin")
	pub := planOf(t, root, o["base"], "")
	if !pub.Published || !pub.Commits[0].Published || !pub.Commits[1].Published || pub.Commits[2].Published {
		t.Fatalf("published: %+v", pub)
	}
	// The upstream as base.
	git("branch", "-u", "origin/main")
	up := planOf(t, root, "refs/remotes/origin/main", "")
	if !up.BaseIsUpstream || up.BaseLabel != "origin/main" || len(up.Commits) != 3 {
		t.Fatalf("upstream plan: %+v", up)
	}
}

func TestRebaseReorderSquashRewordAndFixups(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["base"], "")
	entries := []protocol.GitRebaseEntry{
		pick(o["A"]),
		entry(protocol.GitRebaseSquash, o["C"], "A and C\n\n# kept line"),
		entry(protocol.GitRebaseReword, o["B"], "B reworded"),
		pick(o["E"]),
		entry(protocol.GitRebaseFixup, o["D"], ""),
	}
	r := mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if r.Git.Operation == nil || r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
		t.Fatalf("rebase: %+v %+v", r.Git, r.Git.Operation)
	}
	if got := subjects(git, o["base"]+"..HEAD"); !slices.Equal(got, []string{"E", "B reworded", "A and C"}) {
		t.Fatalf("history: %q", got)
	}
	if msg := git("log", "-1", "--format=%B", "HEAD~2"); msg != "A and C\n\n# kept line" {
		t.Fatalf("squash message (# lines are kept): %q", msg)
	}
	if files := git("show", "--name-only", "--format=", "HEAD"); files != "d.txt\ne.txt" {
		t.Fatalf("fixup content: %q", files)
	}
	if git("symbolic-ref", "HEAD") != "refs/heads/main" || git("status", "--porcelain") != "" {
		t.Fatal("branch or tree after the rebase")
	}
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationCompleted || rec.Interactive == nil {
		t.Fatalf("record: %+v", rec)
	}
	if entries, _ := os.ReadDir(e.rebaseDir); len(entries) != 0 {
		t.Fatalf("plan files left behind: %v", entries)
	}

	// fixup -C takes the later commit's message, fixup -c the stored one.
	head := git("rev-parse", "HEAD")
	writeFile(t, root, "f.txt", "F\n")
	git("add", ".")
	git("commit", "-q", "-m", "F")
	writeFile(t, root, "g.txt", "G\n")
	git("add", ".")
	git("commit", "-q", "-m", "G message")
	writeFile(t, root, "h.txt", "H\n")
	git("add", ".")
	git("commit", "-q", "-m", "H")
	writeFile(t, root, "i.txt", "I\n")
	git("add", ".")
	git("commit", "-q", "-m", "I")
	f, g, h, i := git("rev-parse", "HEAD~3"), git("rev-parse", "HEAD~2"), git("rev-parse", "HEAD~1"), git("rev-parse", "HEAD")
	p = planOf(t, root, head, "")
	entries = []protocol.GitRebaseEntry{pick(f), {Action: protocol.GitRebaseFixup, Commit: g, Fixup: "C"}, pick(h), {Action: protocol.GitRebaseFixup, Commit: i, Fixup: "c", Message: "H and I"}}
	mustGit(t, e, rebaseCmd("rb2", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if got := subjects(git, head+"..HEAD"); !slices.Equal(got, []string{"H and I", "G message"}) {
		t.Fatalf("fixup history: %q", got)
	}

	// Drop removes a commit's change.
	p = planOf(t, root, head, "")
	top := git("rev-parse", "HEAD")
	mustGit(t, e, rebaseCmd("rb3", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseDrop, git("rev-parse", "HEAD~1"), ""), pick(top)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if _, err := os.Stat(filepath.Join(root, "g.txt")); !os.IsNotExist(err) {
		t.Fatalf("dropped commit's file remains: %v", err)
	}
	if got := subjects(git, head+"..HEAD"); !slices.Equal(got, []string{"H and I"}) {
		t.Fatalf("after drop: %q", got)
	}
}

func TestRebaseEditAmendAndContinue(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	entries := []protocol.GitRebaseEntry{{Action: protocol.GitRebaseEdit, Commit: o["D"], EditMode: protocol.GitRebaseEditAmend}, pick(o["E"])}
	r := mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStopped || pi.Stop != protocol.GitRebaseStopEditAmend || pi.Entry != 0 || st.HeadOid != o["D"] || !st.Can.Continue.Allowed || st.Can.Skip.Allowed {
		t.Fatalf("edit stop: %+v %+v %+v", r.Git, pi, st.Can)
	}
	writeFile(t, root, "d.txt", "D amended\n")
	// Unstaged changes block Continue until staged.
	st = operationOf(t, e, root)
	if st.Can.Continue.Allowed {
		t.Fatalf("continue with unstaged changes: %+v", st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("c0", gitTarget, st, false))
	wantGitCode(t, err, "unstaged_changes")
	git("add", "d.txt")
	st = operationOf(t, e, root)
	r = mustGit(t, e, client.GitOperationContinueMessageCommand("c1", gitTarget, st, false, "D amended"), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
		t.Fatalf("continue: %+v", r.Git)
	}
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"E", "D amended"}) {
		t.Fatalf("history: %q", got)
	}
	if git("show", "HEAD~1:d.txt") != "D amended" || git("log", "-1", "--format=%an", "HEAD~1") != "Test" {
		t.Fatal("amended content or author")
	}
}

func TestRebaseEditResetSplitAndBreak(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	// D gets a second file so it can be split.
	git("checkout", "-q", o["C"])
	writeFile(t, root, "d.txt", "D\n")
	writeFile(t, root, "d2.txt", "D2\n")
	git("add", ".")
	git("commit", "-q", "-m", "D", "-m", "body of D")
	d := git("rev-parse", "HEAD")
	git("cherry-pick", o["E"])
	git("branch", "-f", "main", "HEAD")
	git("checkout", "-q", "main")
	e2 := git("rev-parse", "HEAD")
	p := planOf(t, root, o["C"], "")
	entries := []protocol.GitRebaseEntry{entry(protocol.GitRebaseEdit, d, ""), {Action: protocol.GitRebaseBreak}, pick(e2)}
	r := mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopEditReset || st.HeadOid != o["C"] || !pi.Staged || pi.Amend == "" || !strings.Contains(r.Git.Message, "staged") {
		t.Fatalf("reset stop: %+v %+v", r.Git, pi)
	}
	// Split: commit d2.txt alone, then continue commits the rest with D's
	// message and author.
	git("restore", "--staged", "d.txt")
	st = operationOf(t, e, root)
	mustGit(t, e, client.GitOperationCommitCommand("split", gitTarget, st, "D part 2"), protocol.GitStateSucceeded)
	git("add", "d.txt")
	st = operationOf(t, e, root)
	if interactiveOf(t, st).Stop != protocol.GitRebaseStopEditReset {
		t.Fatalf("stop after a split commit: %+v", st.Interactive)
	}
	r = mustGit(t, e, client.GitOperationContinueCommand("c1", gitTarget, st, false), protocol.GitStateSucceeded)
	st = operationOf(t, e, root)
	pi = interactiveOf(t, st)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStopped || pi.Stop != protocol.GitRebaseStopBreak || pi.Entry != 1 || st.Can.Skip.Allowed {
		t.Fatalf("break stop: %+v %+v", r.Git, pi)
	}
	if git("log", "-1", "--format=%s|%an|%b", "HEAD") != "D|Test|body of D" || git("log", "-1", "--format=%s|%an", "HEAD~1") != "D part 2|Test" {
		t.Fatalf("split commits: %q", git("log", "--format=%s|%an|%b", "-2"))
	}
	// At the break: insert a commit, then continue.
	writeFile(t, root, "new.txt", "new\n")
	git("add", "new.txt")
	st = operationOf(t, e, root)
	if st.Can.Continue.Allowed {
		t.Fatal("continue allowed with staged changes at a break")
	}
	_, err := e.command(client.GitOperationContinueCommand("c2", gitTarget, st, false))
	wantGitCode(t, err, "staged_changes")
	mustGit(t, e, client.GitOperationCommitCommand("insert", gitTarget, st, "inserted"), protocol.GitStateSucceeded)
	if git("log", "-1", "--format=%an", "HEAD") != "Writer" {
		t.Fatal("a commit at a break is the user's")
	}
	st = operationOf(t, e, root)
	r = mustGit(t, e, client.GitOperationContinueCommand("c3", gitTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
		t.Fatalf("finish: %+v", r.Git)
	}
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"E", "inserted", "D", "D part 2"}) {
		t.Fatalf("history: %q", got)
	}
	// Committing outside a stop is refused.
	_, err = e.command(client.GitOperationCommitCommand("late", gitTarget, operationOf(t, e, root), "x"))
	if err == nil {
		t.Fatal("commit without a rebase in progress was accepted")
	}
}

func TestRebaseConflictThenAbort(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("commit", "-q", "-am", "A2")
	a2 := git("rev-parse", "HEAD")
	git("branch", "stack", o["B"])
	p := planOf(t, root, o["base"], "")
	// A2 before A conflicts.
	entries := []protocol.GitRebaseEntry{pick(a2), pick(o["A"]), pick(o["B"]), pick(o["C"]), pick(o["D"]), pick(o["E"])}
	r := mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{UpdateRefs: true}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStoppedConflicts || pi.Stop != protocol.GitRebaseStopConflict || !st.Can.Skip.Allowed || !st.Can.Abort.Allowed {
		t.Fatalf("conflict stop: %+v %+v %+v", r.Git, pi, st.Can)
	}
	if len(pi.UpdateRefs) != 1 || pi.UpdateRefs[0].Ref != "refs/heads/stack" {
		t.Fatalf("update refs in progress: %+v", pi.UpdateRefs)
	}
	r = mustGit(t, e, client.GitOperationAbortCommand("abort", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeAborted || r.Git.Code != "" || git("rev-parse", "HEAD") != a2 || git("symbolic-ref", "HEAD") != "refs/heads/main" || git("rev-parse", "stack") != o["B"] {
		t.Fatalf("abort: %+v", r.Git)
	}
	if rec := recordOf(e); rec == nil || rec.State != protocol.GitOperationAborted {
		t.Fatalf("record: %+v", rec)
	}
}

func TestRebaseConflictResolvedAtRewordKeepsItsMessage(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("commit", "-q", "-am", "A2")
	a2 := git("rev-parse", "HEAD")
	entries := []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseReword, a2, "A2 reworded")}
	// Rebase E and A2 onto base (which lacks a.txt): A2 conflicts
	// (modified here, absent there).
	p := planOf(t, root, o["D"], o["base"])
	r := mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStoppedConflicts || interactiveOf(t, st).Stop != protocol.GitRebaseStopConflict || interactiveOf(t, st).Command != "reword" {
		t.Fatalf("conflict at reword: %+v %+v", r.Git, st.Interactive)
	}
	if st.Current == nil || st.Current.Oid != a2 || !st.Can.Skip.Allowed || !strings.Contains(st.Sides.Theirs, "A2") {
		t.Fatalf("conflict at reword names the commit being applied: %+v %+v %+v", st.Current, st.Can, st.Sides)
	}
	writeFile(t, root, "a.txt", "resolved\n")
	git("add", "a.txt")
	st = operationOf(t, e, root)
	r = mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("log", "-1", "--format=%B", "HEAD") != "A2 reworded" {
		t.Fatalf("after continue: %+v %q", r.Git, git("log", "-1", "--format=%B"))
	}
	if got := subjects(git, o["base"]+"..HEAD"); !slices.Equal(got, []string{"A2 reworded", "E"}) {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseStartRefusals(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["base"], "")
	all := client.DefaultRebaseEntries(p)
	// Invalid plans.
	for _, tc := range []struct {
		entries []protocol.GitRebaseEntry
		code    string
	}{
		{all[1:], "invalid_plan"},
		{append(slices.Clone(all), pick(o["A"])), "invalid_plan"},
		{append([]protocol.GitRebaseEntry{entry(protocol.GitRebaseSquash, o["A"], "x")}, all[1:]...), "invalid_plan"},
		{append([]protocol.GitRebaseEntry{entry(protocol.GitRebaseReword, o["A"], "")}, all[1:]...), "message_required"},
		{append([]protocol.GitRebaseEntry{pick(o["A"]), entry(protocol.GitRebaseSquash, o["B"], "")}, all[2:]...), "message_required"},
		{append([]protocol.GitRebaseEntry{pick(o["base"])}, all...), "invalid_plan"},
	} {
		_, err := e.command(rebaseCmd("bad", p, tc.entries, client.RebaseOptions{}))
		wantGitCode(t, err, tc.code)
	}
	// A stale plan (a branch now points into the range).
	git("branch", "stack", o["C"])
	_, err := e.command(rebaseCmd("stale", p, all, client.RebaseOptions{}))
	wantGitCode(t, err, "stale_plan")
	// A dirty tree.
	p = planOf(t, root, o["base"], "")
	writeFile(t, root, "a.txt", "dirty\n")
	_, err = e.command(rebaseCmd("dirty", p, all, client.RebaseOptions{}))
	wantGitCode(t, err, "dirty_tree")
	git("checkout", "--", "a.txt")
	// Published commits need the acknowledgement.
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	git("remote", "add", "origin", remote)
	git("push", "-q", "origin", "main")
	git("fetch", "-q", "origin")
	p = planOf(t, root, o["base"], "")
	_, err = e.command(rebaseCmd("pub", p, all, client.RebaseOptions{}))
	wantGitCode(t, err, "published_commit")
	entries := slices.Clone(all)
	entries[4] = entry(protocol.GitRebaseReword, o["E"], "E published")
	mustGit(t, e, rebaseCmd("pub-ack", p, entries, client.RebaseOptions{AcknowledgePublished: true}), protocol.GitStateSucceeded)
	if git("log", "-1", "--format=%s") != "E published" || git("rev-parse", "origin/main") != o["E"] {
		t.Fatal("published rebase or remote ref")
	}
}

func TestRebaseMergesInRangeAreFlattenedWithAcknowledgement(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	git("checkout", "-q", "-b", "side", o["B"])
	writeFile(t, root, "s.txt", "S\n")
	git("add", ".")
	git("commit", "-q", "-m", "S")
	s := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	p := planOf(t, root, o["base"], "")
	if p.MergeCount != 1 || len(p.Commits) != 7 || p.Blocked != "" {
		t.Fatalf("plan with a merge: %+v", p)
	}
	entries := client.DefaultRebaseEntries(p)
	if len(entries) != 6 {
		t.Fatalf("default entries: %+v", entries)
	}
	_, err := e.command(rebaseCmd("merges", p, entries, client.RebaseOptions{}))
	wantGitCode(t, err, "merges_unacknowledged")
	merge := git("rev-parse", "HEAD")
	withMerge := append(slices.Clone(entries), pick(merge))
	_, err = e.command(rebaseCmd("merge-entry", p, withMerge, client.RebaseOptions{AcknowledgeMerges: true}))
	wantGitCode(t, err, "invalid_plan")
	// Reword the side commit so the linearised history is rewritten.
	for i := range entries {
		if entries[i].Commit == s {
			entries[i] = entry(protocol.GitRebaseReword, s, "S linear")
		}
	}
	r := mustGit(t, e, rebaseCmd("flatten", p, entries, client.RebaseOptions{AcknowledgeMerges: true}), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("rev-list", "--merges", "--count", o["base"]+"..HEAD") != "0" {
		t.Fatalf("flattened: %+v", r.Git)
	}
	if got := git("log", "--format=%s", o["base"]+"..HEAD"); !strings.Contains(got, "S linear") || strings.Contains(got, "merge side") {
		t.Fatalf("history: %q", got)
	}
	if rec := recordOf(e); rec == nil || rec.Interactive == nil || rec.Interactive.MergesDropped != 1 {
		t.Fatalf("record: %+v", rec)
	}
}

func TestRebaseUpdateRefsOnAndOff(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	git("branch", "stack", o["C"])
	p := planOf(t, root, o["base"], "")
	entries := client.DefaultRebaseEntries(p)
	entries[0] = entry(protocol.GitRebaseDrop, o["A"], "")
	mustGit(t, e, rebaseCmd("off", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if git("rev-parse", "stack") != o["C"] {
		t.Fatal("stack moved without update refs")
	}
	head := git("rev-parse", "HEAD")
	// On: stack follows C, and after a squash into C it names the result.
	p = planOf(t, root, o["base"], "")
	if len(p.UpdateRefs) != 0 {
		t.Fatalf("stack is not in the rewritten range any more: %+v", p.UpdateRefs)
	}
	git("branch", "-f", "stack", git("rev-parse", "HEAD~2")) // the rewritten C
	p = planOf(t, root, o["base"], "")
	if len(p.UpdateRefs) != 1 {
		t.Fatalf("update refs: %+v", p.UpdateRefs)
	}
	c, d := git("rev-parse", "HEAD~2"), git("rev-parse", "HEAD~1")
	entries = []protocol.GitRebaseEntry{entry(protocol.GitRebaseReword, git("rev-parse", "HEAD~3"), "B again"), pick(c), entry(protocol.GitRebaseSquash, d, "C and D"), pick(head)}
	mustGit(t, e, rebaseCmd("on", p, entries, client.RebaseOptions{UpdateRefs: true}), protocol.GitStateSucceeded)
	if git("log", "-1", "--format=%s", "stack") != "C and D" || git("rev-parse", "stack") != git("rev-parse", "HEAD~1") {
		t.Fatalf("stack after update refs: %s", git("log", "-1", "--format=%s", "stack"))
	}
}

func TestRebaseHookRejection(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeHook(t, root, "pre-rebase", "echo no >&2; exit 1\n")
	p := planOf(t, root, o["C"], "")
	r, err := e.command(rebaseCmd("pre", p, client.DefaultRebaseEntries(p), client.RebaseOptions{}))
	if err != nil || r.Git.Code != protocol.GitRebaseHookRejected || r.Git.Operation.Outcome != protocol.GitOutcomeNotStarted || git("rev-parse", "HEAD") != o["E"] {
		t.Fatalf("pre-rebase: %v %+v", err, r.Git)
	}
	if recordOf(e) != nil {
		t.Fatal("a refused start left a record")
	}
	os.Remove(filepath.Join(root, ".git", "hooks", "pre-rebase"))
	writeHook(t, root, "commit-msg", "echo rejected >&2; exit 1\n")
	p = planOf(t, root, o["C"], "")
	entries := []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseReword, o["D"], "D new")}
	r = mustGit(t, e, rebaseCmd("rw", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if r.Git.Code != protocol.GitRebaseHookRejected || pi.Stop != protocol.GitRebaseStopMessage || pi.Failure != protocol.GitRebaseHookRejected || pi.Hook != "commit-msg" || !st.Can.Continue.Allowed {
		t.Fatalf("commit-msg: %+v %+v", r.Git, pi)
	}
	// Retrying with the hook still rejecting fails again, then works once
	// the hook accepts.
	r = mustGit(t, e, client.GitOperationContinueCommand("c1", gitTarget, st, false), protocol.GitStateFailed)
	if r.Git.Code != protocol.GitRebaseHookRejected {
		t.Fatalf("retry: %+v", r.Git)
	}
	os.Remove(filepath.Join(root, ".git", "hooks", "commit-msg"))
	st = operationOf(t, e, root)
	r = mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("log", "-1", "--format=%B") != "D new" {
		t.Fatalf("after the hook accepted: %+v %q", r.Git, git("log", "-1", "--format=%B"))
	}
}

func TestRebaseSigningFailureAndRerereDisabled(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	git("config", "commit.gpgSign", "true")
	git("config", "gpg.program", "false")
	git("config", "rerere.enabled", "true")
	p := planOf(t, root, o["C"], "")
	entries := []protocol.GitRebaseEntry{pick(o["E"]), pick(o["D"])}
	r := mustGit(t, e, rebaseCmd("sign", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if r.Git.Code != protocol.GitRebaseSigningFailed || pi.Failure != protocol.GitRebaseSigningFailed || pi.Stop != protocol.GitRebaseStopCommitFailed || pi.Detail == "" {
		t.Fatalf("signing: %+v %+v", r.Git, pi)
	}
	mustGit(t, e, client.GitOperationAbortCommand("abort", gitTarget, st, true, false), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != o["E"] {
		t.Fatal("abort after a signing failure")
	}
	// rerere stays off for the application's rebase: a conflict records no
	// preimage.
	git("config", "--unset", "commit.gpgSign")
	writeFile(t, root, "a.txt", "A changed\n")
	git("commit", "-q", "-am", "A2")
	a2 := git("rev-parse", "HEAD")
	p = planOf(t, root, o["base"], "")
	entries = []protocol.GitRebaseEntry{pick(a2), pick(o["A"]), pick(o["B"]), pick(o["C"]), pick(o["D"]), pick(o["E"])}
	r = mustGit(t, e, rebaseCmd("rerere", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStoppedConflicts {
		t.Fatalf("conflict: %+v", r.Git)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, ".git", "rr-cache")); len(entries) != 0 {
		t.Fatalf("rerere recorded a preimage: %v", entries)
	}
}

func TestRebaseRestartRecoveryAndMissingPlan(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["B"], "")
	entries := []protocol.GitRebaseEntry{pick(o["C"]), {Action: protocol.GitRebaseEdit, Commit: o["D"], EditMode: protocol.GitRebaseEditAmend}, entry(protocol.GitRebaseReword, o["E"], "E after restart")}
	mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// A restart: the snapshot is recovered into a new engine.
	snap := e.current()
	recoverGitOps(&snap)
	e2 := testEngine(t)
	e2.snap, e2.rebaseDir = snap, e.rebaseDir
	st := operationOf(t, e2, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopEditAmend || pi.Entry != 1 || st.OperationID == "" || !st.Can.Continue.Allowed {
		t.Fatalf("after restart: %+v %+v", pi, st.Can)
	}
	r := mustGit(t, e2, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("log", "-1", "--format=%s") != "E after restart" {
		t.Fatalf("continue after restart: %+v", r.Git)
	}

	// Without its stored plan, a stop can only be aborted.
	p = planOf(t, root, o["B"], "")
	c, d := git("rev-parse", "HEAD~2"), git("rev-parse", "HEAD~1")
	entries = []protocol.GitRebaseEntry{pick(c), {Action: protocol.GitRebaseEdit, Commit: d, EditMode: protocol.GitRebaseEditAmend}, pick(git("rev-parse", "HEAD"))}
	mustGit(t, e2, rebaseCmd("rb2", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	os.RemoveAll(e2.rebaseDir)
	st = operationOf(t, e2, root)
	_, err := e2.command(client.GitOperationContinueCommand("c2", gitTarget, st, false))
	wantGitCode(t, err, "plan_missing")
	mustGit(t, e2, client.GitOperationAbortCommand("a2", gitTarget, st, true, false), protocol.GitStateSucceeded)
}

func TestRebaseRootAndOnto(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, "root", "")
	entries := client.DefaultRebaseEntries(p)
	entries[0] = entry(protocol.GitRebaseReword, o["base"], "new root")
	entries[1] = protocol.GitRebaseEntry{Action: protocol.GitRebaseEdit, Commit: o["A"], EditMode: protocol.GitRebaseEditAmend}
	mustGit(t, e, rebaseCmd("root", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if interactiveOf(t, st).Stop != protocol.GitRebaseStopEditAmend || st.OperationID == "" {
		t.Fatalf("root stop: %+v", st.Interactive)
	}
	if rec := recordOf(e); rec == nil || rec.Target.Oid == "" {
		t.Fatalf("root record learned no onto: %+v", rec)
	}
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if git("log", "--format=%s", "--reverse", "HEAD") != "new root\nA\nB\nC\nD\nE" || git("rev-list", "--max-parents=0", "HEAD") == o["base"] {
		t.Fatal("root rebase history")
	}
	// Onto another commit: move D and E onto A.
	head := git("rev-parse", "HEAD")
	p = planOf(t, root, git("rev-parse", "HEAD~2"), git("rev-parse", "HEAD~4"))
	if len(p.Commits) != 2 || p.OntoOid != git("rev-parse", "HEAD~4") {
		t.Fatalf("onto plan: %+v", p)
	}
	mustGit(t, e, rebaseCmd("onto", p, client.DefaultRebaseEntries(p), client.RebaseOptions{}), protocol.GitStateSucceeded)
	if git("log", "--format=%s", "--reverse", "HEAD") != "new root\nA\nD\nE" || git("rev-parse", "HEAD") == head {
		t.Fatalf("onto history: %q", git("log", "--format=%s", "HEAD"))
	}
}

func TestRebaseHelperRefusesStrayInvocations(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, "gitdir")
	if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o700); err != nil {
		t.Fatal(err)
	}
	oid := strings.Repeat("a", 40)
	st := rebaseState{Version: rebaseStateVersion, Token: "tok", GitDir: gitDir, Expected: []string{oid}, Todo: "reword " + oid + "\n",
		Lines: []rebaseLine{{Command: "reword", Oid: oid, Message: "stored", HasMessage: true}}}
	b, _ := json.Marshal(st)
	state := filepath.Join(dir, rebaseStateName)
	if err := os.WriteFile(state, b, 0o600); err != nil {
		t.Fatal(err)
	}
	todo := filepath.Join(gitDir, "rebase-merge", "git-rebase-todo")
	msg := filepath.Join(gitDir, "COMMIT_EDITMSG")
	t.Setenv(rebaseStateEnv, state)
	t.Setenv(rebaseTokenEnv, "wrong")
	if err := RunRebaseHelper([]string{"sequence", todo}); err == nil {
		t.Fatal("wrong token accepted")
	}
	t.Setenv(rebaseTokenEnv, "tok")
	os.WriteFile(todo, []byte("pick bbbbbbb # other\n"), 0o600)
	if err := RunRebaseHelper([]string{"sequence", todo}); err == nil || !strings.Contains(err.Error(), "not a pinned commit") {
		t.Fatalf("foreign commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rebaseHelperError)); err != nil {
		t.Fatal("helper error not recorded")
	}
	os.WriteFile(todo, []byte("pick aaaaaaa # ok\n# comment\n"), 0o600)
	if err := RunRebaseHelper([]string{"sequence", todo}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(todo); string(got) != st.Todo {
		t.Fatalf("todo not replaced: %q", got)
	}
	if err := RunRebaseHelper([]string{"sequence", filepath.Join(dir, "elsewhere")}); err == nil {
		t.Fatal("a file outside the Git directory was accepted")
	}
	os.WriteFile(filepath.Join(gitDir, "rebase-merge", "done"), []byte("reword "+oid+"\n"), 0o600)
	os.WriteFile(msg, []byte("old\n# comment\n"), 0o600)
	if err := RunRebaseHelper([]string{"message", msg}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(msg); string(got) != "stored\n" {
		t.Fatalf("message: %q", got)
	}
	os.WriteFile(filepath.Join(gitDir, "rebase-merge", "done"), []byte("pick "+oid+"\n"), 0o600)
	if err := RunRebaseHelper([]string{"message", msg}); err == nil {
		t.Fatal("a step that differs from the plan was accepted")
	}
	// Without a stored message the step fails; Git's prepared text is never
	// used.
	st.Lines[0].HasMessage, st.Lines[0].Message, st.Lines[0].Command = false, "", "pick"
	b, _ = json.Marshal(st)
	os.WriteFile(state, b, 0o600)
	os.WriteFile(filepath.Join(gitDir, "rebase-merge", "done"), []byte("pick "+oid+"\n"), 0o600)
	os.WriteFile(msg, []byte("old\n#1234 keep\n# Please enter the commit message\n"), 0o600)
	if err := RunRebaseHelper([]string{"message", msg}); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("fallback to Git's text: %v", err)
	}
	// A symlinked file is refused, not followed.
	target := filepath.Join(dir, "outside")
	os.WriteFile(target, []byte("x"), 0o600)
	os.Remove(msg)
	if err := os.Symlink(target, msg); err != nil {
		t.Fatal(err)
	}
	if err := RunRebaseHelper([]string{"message", msg}); err == nil {
		t.Fatal("a symlinked message file was accepted")
	}
	os.Remove(todo)
	os.Symlink(target, todo)
	if err := RunRebaseHelper([]string{"sequence", todo}); err == nil {
		t.Fatal("a symlinked todo was accepted")
	}
}

func TestRebaseSigningFailureIsRecognizedPrecisely(t *testing.T) {
	for out, want := range map[string]bool{
		"error: gpg failed to sign the data:\n(no gpg output)\nerror: failed to write commit object\n":             true,
		"error: Couldn't load public key /k: No such file or directory?\n\nerror: failed to write commit object\n": true,
		"lint: assign.go:3: sign mismatch\nerror: failed to write commit object\n":                                 false,
		"error: gpg failed to sign the data\n":                                                                     false,
		"hook: checking signatures of assign.go\nfatal: failed to write commit object\n":                           false,
	} {
		if got := signingFailure(out); got != want {
			t.Errorf("%q: got %v", out, got)
		}
	}
}

func TestRebaseHelperFailureStopsAndRecovers(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	entries := []protocol.GitRebaseEntry{{Action: protocol.GitRebaseEdit, Commit: o["D"], EditMode: protocol.GitRebaseEditAmend}, entry(protocol.GitRebaseReword, o["E"], "E new")}
	mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// The stored plan no longer matches what Git runs at the reword step.
	rec := recordOf(e)
	statePath := filepath.Join(rebaseOpDir(e.rebaseDir, rec.OperationID), rebaseStateName)
	original, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st rebaseState
	if err := json.Unmarshal(original, &st); err != nil {
		t.Fatal(err)
	}
	st.Lines[1].Command = "pick"
	tampered, _ := json.Marshal(st)
	os.WriteFile(statePath, tampered, 0o600)
	op := operationOf(t, e, root)
	r := mustGit(t, e, client.GitOperationContinueCommand("c1", gitTarget, op, false), protocol.GitStateSucceeded)
	op = operationOf(t, e, root)
	pi := interactiveOf(t, op)
	if r.Git.Code != protocol.GitRebaseHelperFailed || pi.Failure != protocol.GitRebaseHelperFailed || !strings.Contains(pi.Detail, "not the planned") || pi.Stop != protocol.GitRebaseStopMessage {
		t.Fatalf("helper failure: %+v %+v", r.Git, pi)
	}
	// Repaired, Continue finishes the reword with a new message.
	os.WriteFile(statePath, original, 0o600)
	r = mustGit(t, e, client.GitOperationContinueMessageCommand("c2", gitTarget, op, false, "E final"), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || git("log", "-1", "--format=%B") != "E final" {
		t.Fatalf("after repair: %+v %q", r.Git, git("log", "-1", "--format=%B"))
	}
}

// ---- Verify round 2026-09-26 ----

// commitWithHash commits a new file with a message carrying a `#` line.
func commitWithHash(t *testing.T, root string, git func(...string) string, file, msg string) string {
	t.Helper()
	writeFile(t, root, file, file+"\n")
	git("add", file)
	git("commit", "-q", "--cleanup=verbatim", "-m", msg)
	return git("rev-parse", "HEAD")
}

func messageOf(git func(...string) string, rev string) string {
	return git("log", "-1", "--format=%B", rev)
}

func TestRebaseKeepsHashLinesOnEveryFallback(t *testing.T) {
	// A resolved conflict at a pick keeps the commit's message exactly.
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("add", ".")
	git("commit", "-q", "--cleanup=verbatim", "-m", "A2\n\n#1234 closes the issue\n")
	a2 := git("rev-parse", "HEAD")
	want := messageOf(git, a2)
	p := planOf(t, root, o["D"], o["base"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), pick(a2)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "a.txt", "resolved\n")
	git("add", "a.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := messageOf(git, "HEAD"); got != want {
		t.Fatalf("resolved pick: want %q got %q", want, got)
	}

	// An amend stop continued with staged changes and no message.
	f := commitWithHash(t, root, git, "f.txt", "F\n\n#42 keep me\n")
	want = messageOf(git, f)
	p = planOf(t, root, git("rev-parse", "HEAD~1"), "")
	mustGit(t, e, rebaseCmd("amend", p, []protocol.GitRebaseEntry{{Action: protocol.GitRebaseEdit, Commit: f, EditMode: protocol.GitRebaseEditAmend}}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "f.txt", "F2\n")
	git("add", "f.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := messageOf(git, "HEAD"); got != want || git("show", "HEAD:f.txt") != "F2" {
		t.Fatalf("amend stop: want %q got %q", want, got)
	}

	// A reset stop continued without a message recommits it exactly.
	g := commitWithHash(t, root, git, "g.txt", "G\n\n#7 still here\n")
	want = messageOf(git, g)
	p = planOf(t, root, git("rev-parse", "HEAD~1"), "")
	mustGit(t, e, rebaseCmd("reset", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseEdit, g, "")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitOperationContinueCommand("c3", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := messageOf(git, "HEAD"); got != want || git("log", "-1", "--format=%an") != "Test" {
		t.Fatalf("reset stop: want %q got %q", want, got)
	}

	// A plain fixup resolved inside a fixup chain keeps the accumulated
	// message (the head commit's), `#` lines included.
	h := commitWithHash(t, root, git, "h.txt", "H\n\n#9 the head\n")
	want = messageOf(git, h)
	writeFile(t, root, "h.txt", "H fixed\n")
	git("commit", "-q", "-am", "fix H")
	fix := git("rev-parse", "HEAD")
	q := commitWithHash(t, root, git, "q.txt", "Q")
	base := git("rev-parse", "HEAD~3")
	// Onto the commit before H: fix H conflicts (h.txt does not exist).
	git("checkout", "-q", "-b", "other", base)
	writeFile(t, root, "h.txt", "other H\n")
	git("add", ".")
	git("commit", "-q", "-m", "other H")
	other := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	p = planOf(t, root, base, other)
	r := mustGit(t, e, rebaseCmd("fx", p, []protocol.GitRebaseEntry{pick(h), {Action: protocol.GitRebaseFixup, Commit: fix}, {Action: protocol.GitRebaseFixup, Commit: q}}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	sawFixup := false
	for r.Git.Operation.Outcome == protocol.GitOutcomeStoppedConflicts {
		writeFile(t, root, "h.txt", "merged\n")
		git("add", "h.txt")
		st := operationOf(t, e, root)
		// A message is refused inside a chain that keeps a commit's message.
		if st.Interactive.Command == "fixup" {
			sawFixup = true
			_, err := e.command(client.GitOperationContinueMessageCommand("cm-"+st.HeadOid, gitTarget, st, false, "MY MESSAGE"))
			wantGitCode(t, err, "invalid")
		}
		r = mustGit(t, e, client.GitOperationContinueCommand("cf-"+st.HeadOid, gitTarget, st, false), protocol.GitStateSucceeded)
	}
	if got := messageOf(git, "HEAD"); got != want || !sawFixup {
		t.Fatalf("fixup chain (conflict at the fixup %v): want %q got %q", sawFixup, want, got)
	}
}

func TestRebaseAbortReportsAndKeepsWorkMadeAtStops(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "new.txt", "precious\n")
	git("add", "new.txt")
	mustGit(t, e, client.GitOperationCommitCommand("ins", gitTarget, operationOf(t, e, root), "inserted"), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if st.AbortDropsCount != 1 || st.AbortDropsCommits[0].Subject != "inserted" || st.AbortDropsFingerprint == "" {
		t.Fatalf("abort drops: %+v", st.AbortDropsCommits)
	}
	_, err := e.command(client.GitOperationAbortCommand("ab", gitTarget, st, true, false))
	wantGitCode(t, err, "drops_unacknowledged")
	inserted := st.HeadOid
	r := mustGit(t, e, client.GitOperationAbortCommand("ab2", gitTarget, st, true, true), protocol.GitStateSucceeded)
	ref := r.Git.Operation.BackupRef
	if ref == "" || git("rev-parse", ref) != inserted || git("show", ref+":new.txt") != "precious" || git("rev-parse", "HEAD") != o["E"] || !strings.Contains(r.Git.Message, ref) {
		t.Fatalf("abort: %+v", r.Git)
	}

	// An amend at an amend stop is work too.
	p = planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb2", p, []protocol.GitRebaseEntry{{Action: protocol.GitRebaseEdit, Commit: o["D"], EditMode: protocol.GitRebaseEditAmend}, {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "d.txt", "D amended\n")
	git("add", "d.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	st = operationOf(t, e, root)
	if interactiveOf(t, st).Stop != protocol.GitRebaseStopBreak || st.AbortDropsCount != 1 || st.AbortDropsCommits[0].Subject != "D" {
		t.Fatalf("amended commit not reported: %+v %+v", st.Interactive, st.AbortDropsCommits)
	}
	mustGit(t, e, client.GitOperationAbortCommand("ab3", gitTarget, st, true, true), protocol.GitStateSucceeded)

	// A split at a reset stop, then Abort: acknowledged, restored.
	git("branch", "stack", o["D"])
	p = planOf(t, root, o["B"], "")
	mustGit(t, e, rebaseCmd("rb3", p, []protocol.GitRebaseEntry{pick(o["C"]), entry(protocol.GitRebaseEdit, o["D"], ""), pick(o["E"])}, client.RebaseOptions{UpdateRefs: true}), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitOperationCommitCommand("split", gitTarget, operationOf(t, e, root), "part"), protocol.GitStateSucceeded)
	st = operationOf(t, e, root)
	if st.AbortDropsCount != 1 {
		t.Fatalf("split not reported: %+v", st.AbortDropsCommits)
	}
	mustGit(t, e, client.GitOperationAbortCommand("ab4", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != o["E"] || git("symbolic-ref", "HEAD") != "refs/heads/main" || git("rev-parse", "stack") != o["D"] || git("status", "--porcelain") != "" {
		t.Fatal("not restored")
	}
}

func TestRebaseEditStopsNeverLoseOrDuplicateTheCommit(t *testing.T) {
	// Reset stop with everything unstaged: Continue would drop D.
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseEdit, o["D"], ""), pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	git("reset", "-q")
	st := operationOf(t, e, root)
	if st.Can.Continue.Allowed {
		t.Fatalf("continue allowed with nothing to recommit: %+v", st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("c", gitTarget, st, false))
	wantGitCode(t, err, "nothing_to_recommit")
	git("add", "d.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"E", "D"}) {
		t.Fatalf("history: %q", got)
	}

	// Amend stop with a commit made at it: staged leftovers are refused,
	// then Continue neither recommits nor duplicates D.
	p = planOf(t, root, o["C"], "")
	d, e2 := git("rev-parse", "HEAD~1"), git("rev-parse", "HEAD")
	mustGit(t, e, rebaseCmd("rb2", p, []protocol.GitRebaseEntry{{Action: protocol.GitRebaseEdit, Commit: d, EditMode: protocol.GitRebaseEditAmend}, pick(e2)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "x.txt", "x\n")
	git("add", "x.txt")
	mustGit(t, e, client.GitOperationCommitCommand("oc", gitTarget, operationOf(t, e, root), "extra"), protocol.GitStateSucceeded)
	writeFile(t, root, "y.txt", "y\n")
	git("add", "y.txt")
	st = operationOf(t, e, root)
	if interactiveOf(t, st).Stop != protocol.GitRebaseStopEditAmend || st.Can.Continue.Allowed {
		t.Fatalf("amend stop after a commit: %+v %+v", st.Interactive, st.Can)
	}
	_, err = e.command(client.GitOperationContinueCommand("c3", gitTarget, st, false))
	wantGitCode(t, err, "staged_changes")
	git("reset", "-q", "y.txt")
	os.Remove(filepath.Join(root, "y.txt"))
	mustGit(t, e, client.GitOperationContinueCommand("c4", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := git("log", "--format=%s|%an", o["C"]+"..HEAD"); got != "E|Test\nextra|Writer\nD|Test" {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseEmptyCommitContinueKeepsSkipDrops(t *testing.T) {
	for _, skip := range []bool{false, true} {
		e, root, git, o := rebaseSetup(t)
		git("checkout", "-q", "-b", "side", o["base"])
		writeFile(t, root, "a.txt", "A\n")
		git("add", ".")
		git("commit", "-q", "--cleanup=verbatim", "-m", "A dup\n\n#5 empty but kept\n")
		dup := git("rev-parse", "HEAD")
		want := messageOf(git, dup)
		writeFile(t, root, "z.txt", "Z\n")
		git("add", ".")
		git("commit", "-q", "-m", "Z")
		z := git("rev-parse", "HEAD")
		p := planOf(t, root, o["base"], o["E"])
		mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(dup), pick(z)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
		st := operationOf(t, e, root)
		if interactiveOf(t, st).Stop != protocol.GitRebaseStopEmpty || !st.Can.Continue.Allowed || !st.Can.Skip.Allowed {
			t.Fatalf("empty stop: %+v %+v", st.Interactive, st.Can)
		}
		if skip {
			cmd, ok := client.GitOperationSkipCommand("s", gitTarget, st, true)
			if !ok {
				t.Fatal("client refuses the skip")
			}
			mustGit(t, e, cmd, protocol.GitStateSucceeded)
			if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{"Z"}) {
				t.Fatalf("skip: %q", got)
			}
			continue
		}
		_, err := e.command(client.GitOperationContinueMessageCommand("cm", gitTarget, st, false, "other"))
		wantGitCode(t, err, "invalid")
		mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
		if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{"Z", "A dup"}) || messageOf(git, "HEAD~1") != want || git("log", "-1", "--format=%an", "HEAD~1") != "Test" {
			t.Fatalf("continue keeps: %q %q", got, messageOf(git, "HEAD~1"))
		}
	}
}

func TestRebaseRetriedContinueDoesNotCommitTwice(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	git("checkout", "-q", "-b", "side", o["base"])
	writeFile(t, root, "a.txt", "A\n")
	git("add", ".")
	git("commit", "-q", "-m", "A dup")
	dup := git("rev-parse", "HEAD")
	writeFile(t, root, "z.txt", "Z\n")
	git("add", ".")
	git("commit", "-q", "-m", "Z")
	z := git("rev-parse", "HEAD")
	p := planOf(t, root, o["base"], o["E"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(dup), pick(z)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// A pre-continue commit was made and recorded, then the continue failed
	// (simulated): the retry must not commit again.
	git("-c", "user.name=Test", "commit", "-q", "--allow-empty", "-C", dup)
	made := git("rev-parse", "HEAD")
	e.mu.Lock()
	e.snap.GitOperations[0].Interactive.StopCommits = []protocol.GitOperationCommit{{Oid: made, Subject: "A dup"}}
	e.mu.Unlock()
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{"Z", "A dup"}) {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseSkipAtInteractiveConflicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry func(a2 string) protocol.GitRebaseEntry
		after []string
	}{
		{"reword", func(a2 string) protocol.GitRebaseEntry { return entry(protocol.GitRebaseReword, a2, "A2 r") }, []string{"E"}},
		{"edit", func(a2 string) protocol.GitRebaseEntry { return entry(protocol.GitRebaseEdit, a2, "") }, []string{"E"}},
		{"final squash", func(a2 string) protocol.GitRebaseEntry { return entry(protocol.GitRebaseSquash, a2, "E and A2") }, []string{"E"}},
		{"fixup", func(a2 string) protocol.GitRebaseEntry {
			return protocol.GitRebaseEntry{Action: protocol.GitRebaseFixup, Commit: a2}
		}, []string{"E"}},
		{"fixup -c", func(a2 string) protocol.GitRebaseEntry {
			return protocol.GitRebaseEntry{Action: protocol.GitRebaseFixup, Commit: a2, Fixup: "c", Message: "E via c"}
		}, []string{"E"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, root, git, o := rebaseSetup(t)
			writeFile(t, root, "a.txt", "A changed\n")
			git("commit", "-q", "-am", "A2")
			a2 := git("rev-parse", "HEAD")
			p := planOf(t, root, o["D"], o["base"])
			mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), tc.entry(a2)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
			st := operationOf(t, e, root)
			if interactiveOf(t, st).Stop != protocol.GitRebaseStopConflict || st.Current == nil || st.Current.Oid != a2 {
				t.Fatalf("conflict: %+v %+v", st.Interactive, st.Current)
			}
			cmd, ok := client.GitOperationSkipCommand("s", gitTarget, st, true)
			if !ok || !st.Can.Skip.Allowed {
				t.Fatalf("skip unavailable: %v %+v", ok, st.Can.Skip)
			}
			r := mustGit(t, e, cmd, protocol.GitStateSucceeded)
			if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted {
				t.Fatalf("skip: %+v", r.Git)
			}
			if got := subjects(git, o["base"]+"..HEAD"); !slices.Equal(got, tc.after) {
				t.Fatalf("history: %q", got)
			}
			if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
				t.Fatal("the skipped commit's file is present")
			}
		})
	}
}

func TestRebaseUnsupportedUpdateRefAndExternalEndSweep(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	git("update-ref", "refs/heads/bad\xff", o["C"])
	p := planOf(t, root, o["B"], "")
	if len(p.UpdateRefsUnsupported) != 1 || len(p.UpdateRefs) != 0 {
		t.Fatalf("unsupported: %+v", p)
	}
	_, err := e.command(rebaseCmd("on", p, client.DefaultRebaseEntries(p), client.RebaseOptions{UpdateRefs: true}))
	wantGitCode(t, err, "update_refs_unsupported")
	git("update-ref", "-d", "refs/heads/bad\xff")

	// A rebase finished in a terminal: its plan files go on the next read.
	p = planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{{Action: protocol.GitRebaseBreak}, pick(o["D"]), pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	dir := rebaseOpDir(e.rebaseDir, recordOf(e).OperationID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	git("rebase", "--abort")
	if st := operationOf(t, e, root); st.Kind != "" || recordOf(e).State != protocol.GitOperationEndedExternal {
		t.Fatalf("after an external end: %+v", recordOf(e))
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("plan files kept after an external end: %v", err)
	}
}

func TestRebaseOperationCommitHookFailure(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeHook(t, root, "pre-commit", "exit 1\n")
	writeFile(t, root, "new.txt", "n\n")
	git("add", "new.txt")
	st := operationOf(t, e, root)
	r := mustGit(t, e, client.GitOperationCommitCommand("oc", gitTarget, st, "new"), protocol.GitStateFailed)
	after := operationOf(t, e, root)
	if r.Git.Code != protocol.GitRebaseHookRejected || after.HeadOid != st.HeadOid || interactiveOf(t, after).Hook != "pre-commit" || after.AbortDropsCount != 0 {
		t.Fatalf("pre-commit refusal: %+v %+v", r.Git, after.Interactive)
	}
}

func TestRebaseRewordRecoveryChecksTheCommit(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	t.Setenv("GIT_AUTHOR_DATE", "1700000000 +0000")
	writeFile(t, root, "x.txt", "X1\n")
	git("add", ".")
	git("commit", "-q", "-m", "wip")
	x1 := git("rev-parse", "HEAD")
	writeFile(t, root, "x.txt", "X2\n")
	git("add", ".")
	git("commit", "-q", "-m", "wip")
	x2 := git("rev-parse", "HEAD")
	writeHook(t, root, "commit-msg", "grep -q wip2 \"$1\" && exit 1; exit 0\n")
	p := planOf(t, root, o["E"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(x1), entry(protocol.GitRebaseReword, x2, "wip2")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// Pretend HEAD is the previous commit with the same author and message:
	// the amend must be refused, not applied to it.
	git("reset", "-q", "--soft", x1)
	git("reset", "-q", "--hard", x1)
	st := operationOf(t, e, root)
	if interactiveOf(t, st).Stop != protocol.GitRebaseStopMessage {
		t.Fatalf("stop: %+v", st.Interactive)
	}
	if st.Can.Continue.Allowed {
		t.Fatalf("continue offered with HEAD elsewhere: %+v", st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("c", gitTarget, st, false))
	wantGitCode(t, err, "not_supported")
	if git("rev-parse", "HEAD") != x1 {
		t.Fatal("reword recovery amended another commit")
	}
}

// ---- Second verify round: verbatim messages, empty reword/edit ----

// oddMessage has trailing spaces, doubled blank lines and a `#` line.
const oddMessage = "Subject  \n\n\nbody line   \n#1 hash\n\n\nlast\n"

func TestRebaseMessagesAreVerbatim(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	// A plan reword and a squash chain's message are stored exactly.
	p := planOf(t, root, o["B"], "")
	entries := []protocol.GitRebaseEntry{entry(protocol.GitRebaseReword, o["C"], oddMessage), pick(o["D"]), entry(protocol.GitRebaseSquash, o["E"], oddMessage)}
	mustGit(t, e, rebaseCmd("rb", p, entries, client.RebaseOptions{}), protocol.GitStateSucceeded)
	raw := func(rev string) string {
		m, err := commitMessage(context.Background(), mustReader(t, root), git("rev-parse", rev))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if raw("HEAD") != oddMessage || raw("HEAD~1") != oddMessage {
		t.Fatalf("plan messages: %q %q", raw("HEAD"), raw("HEAD~1"))
	}
	// Original messages survive a resolved conflict and a plain fixup chain.
	writeFile(t, root, "a.txt", "A changed\n")
	git("add", ".")
	git("commit", "-q", "--cleanup=verbatim", "-m", oddMessage)
	a2 := git("rev-parse", "HEAD")
	f := commitWithHash(t, root, git, "f.txt", oddMessage)
	writeFile(t, root, "f.txt", "F2\n")
	git("commit", "-q", "-am", "fix f")
	fix := git("rev-parse", "HEAD")
	p = planOf(t, root, git("rev-parse", "HEAD~3"), o["base"])
	r := mustGit(t, e, rebaseCmd("rb2", p, []protocol.GitRebaseEntry{pick(a2), pick(f), {Action: protocol.GitRebaseFixup, Commit: fix}}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	for r.Git.Operation.Outcome == protocol.GitOutcomeStoppedConflicts {
		writeFile(t, root, "a.txt", "resolved\n")
		git("add", "a.txt")
		st := operationOf(t, e, root)
		r = mustGit(t, e, client.GitOperationContinueCommand("c-"+st.HeadOid, gitTarget, st, false), protocol.GitStateSucceeded)
	}
	if raw("HEAD") != oddMessage || raw("HEAD~1") != oddMessage {
		t.Fatalf("original messages: %q %q", raw("HEAD"), raw("HEAD~1"))
	}
}

func mustReader(t *testing.T, root string) *gitReader {
	t.Helper()
	g, err := newGitReader(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRebaseRewordOrEditThatBecameEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		keep   bool
		want   string
	}{
		{"reword kept", protocol.GitRebaseReword, true, "reworded dup"},
		{"reword skipped", protocol.GitRebaseReword, false, ""},
		{"edit kept", protocol.GitRebaseEdit, true, "A dup"},
		{"edit skipped", protocol.GitRebaseEdit, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, root, git, o := rebaseSetup(t)
			git("checkout", "-q", "-b", "side", o["base"])
			writeFile(t, root, "a.txt", "A\n")
			git("add", ".")
			git("commit", "-q", "-m", "A dup")
			dup := git("rev-parse", "HEAD")
			p := planOf(t, root, o["base"], o["E"])
			en := protocol.GitRebaseEntry{Action: tc.action, Commit: dup}
			if tc.action == protocol.GitRebaseReword {
				en.Message = "reworded dup"
			}
			mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{en}, client.RebaseOptions{}), protocol.GitStateSucceeded)
			st := operationOf(t, e, root)
			pi := interactiveOf(t, st)
			if pi.Stop != protocol.GitRebaseStopEmpty || !pi.StepEmpty || st.Current == nil || st.Current.Oid != dup || !st.Can.Continue.Allowed || !st.Can.Skip.Allowed {
				t.Fatalf("empty %s stop: %+v %+v %+v", tc.action, pi, st.Current, st.Can)
			}
			if tc.keep {
				mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
				if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{tc.want}) || git("log", "-1", "--format=%an") != "Test" || git("diff", "--stat", "HEAD~1", "HEAD") != "" {
					t.Fatalf("kept: %q", got)
				}
				return
			}
			cmd, ok := client.GitOperationSkipCommand("s", gitTarget, st, true)
			if !ok {
				t.Fatal("client refuses the skip")
			}
			mustGit(t, e, cmd, protocol.GitStateSucceeded)
			if git("rev-parse", "HEAD") != o["E"] || operationOf(t, e, root).Kind != "" {
				t.Fatal("skipped: the empty commit remains or the rebase is still in progress")
			}
		})
	}
}

// ---- Third verify round: stop snapshots ----

// emptyStopSetup rebases a side branch (A dup, Z) onto E: A dup becomes
// empty at the first step.
func emptyStopSetup(t *testing.T, action string) (*engine, string, func(...string) string, map[string]string, string) {
	t.Helper()
	e, root, git, o := rebaseSetup(t)
	git("checkout", "-q", "-b", "side", o["base"])
	writeFile(t, root, "a.txt", "A\n")
	git("add", ".")
	git("commit", "-q", "-m", "A dup")
	dup := git("rev-parse", "HEAD")
	writeFile(t, root, "z.txt", "Z\n")
	git("add", ".")
	git("commit", "-q", "-m", "Z")
	z := git("rev-parse", "HEAD")
	p := planOf(t, root, o["base"], o["E"])
	first := protocol.GitRebaseEntry{Action: action, Commit: dup}
	if action == protocol.GitRebaseReword {
		first.Message = "reworded dup"
	}
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{first, pick(z)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	return e, root, git, o, dup
}

func TestRebaseRewordWhoseHooksChangeAndRejectTheMessage(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeHook(t, root, "prepare-commit-msg", "grep -q Ticket \"$1\" || printf '\\nTicket: X-1\\n' >> \"$1\"\n")
	writeHook(t, root, "commit-msg", "exit 1\n")
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseReword, o["D"], "D new")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopMessage || !pi.StepCommitted || pi.StepEmpty || pi.Late || st.Can.Skip.Allowed {
		t.Fatalf("stop: %+v skip %+v", pi, st.Can.Skip)
	}
	os.Remove(filepath.Join(root, ".git", "hooks", "commit-msg"))
	st = operationOf(t, e, root)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"D new", "E"}) {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseWorkMadeAtAStopIsNeverMadeAgain(t *testing.T) {
	// The empty commit kept in a terminal (or by an attempt the server
	// crashed after) is not kept twice, and Skip no longer applies.
	e, root, git, o, dup := emptyStopSetup(t, protocol.GitRebasePick)
	git("commit", "-q", "--allow-empty", "-C", dup)
	st := operationOf(t, e, root)
	if pi := interactiveOf(t, st); pi.Stop != protocol.GitRebaseStopEmpty || st.Can.Skip.Allowed || !st.Can.Continue.Allowed || st.AbortDropsCount != 1 {
		t.Fatalf("after a terminal commit: %+v %+v drops=%d", pi, st.Can, st.AbortDropsCount)
	}
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{"Z", "A dup"}) {
		t.Fatalf("empty kept once: %q", got)
	}

	// The reword amended already (in a terminal, or before a crash).
	e, root, git, o = rebaseSetup(t)
	writeHook(t, root, "commit-msg", "exit 1\n")
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseReword, o["D"], "D new")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	os.Remove(filepath.Join(root, ".git", "hooks", "commit-msg"))
	git("commit", "-q", "--amend", "-m", "D new")
	st = operationOf(t, e, root)
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"D new", "E"}) {
		t.Fatalf("reword applied once: %q", got)
	}
}

func TestRebaseStopNotObservedIsHandledConservatively(t *testing.T) {
	e, root, git, o, _ := emptyStopSetup(t, protocol.GitRebaseReword)
	// A crash between Git stopping and the journal: no snapshot.
	e.mu.Lock()
	e.snap.GitOperations[0].Interactive.Snapshot = nil
	e.mu.Unlock()
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if !pi.Late || st.Can.Continue.Allowed || st.Can.Skip.Allowed || !st.Can.Abort.Allowed {
		t.Fatalf("late stop: %+v %+v", pi, st.Can)
	}
	_, err := e.command(client.GitOperationContinueCommand("c", gitTarget, st, false))
	wantGitCode(t, err, "stop_unobserved")
	mustGit(t, e, client.GitOperationAbortCommand("a", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != git("rev-parse", "side") || operationOf(t, e, root).Kind != "" {
		t.Fatal("abort")
	}
	_ = o
}

func TestRebaseEmptyMessageCommitSurvivesAConflict(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("add", ".")
	git("commit", "-q", "--allow-empty-message", "-m", "")
	a2 := git("rev-parse", "HEAD")
	p := planOf(t, root, o["D"], o["base"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), pick(a2)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "a.txt", "res\n")
	git("add", "a.txt")
	st := operationOf(t, e, root)
	if !st.Can.Continue.Allowed {
		t.Fatalf("continue: %+v", st.Can)
	}
	r := mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if r.Git.Operation.Outcome != protocol.GitOutcomeCompleted || messageOf(git, "HEAD") != "" || git("show", "HEAD:a.txt") != "res" || git("log", "-1", "--format=%an") != "Test" {
		t.Fatalf("empty message: %+v %q", r.Git, messageOf(git, "HEAD"))
	}
}

func TestRebasePrepareCommitMsgAdditionsOnRewordsAreReplaced(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeHook(t, root, "prepare-commit-msg", "printf '\\nTicket: X-1\\n' >> \"$1\"\n")
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseReword, o["D"], "D new"), pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// The helper writes the whole message (ADR 0026, known limit).
	if got := messageOf(git, "HEAD~1"); got != "D new" {
		t.Fatalf("reword message: %q", got)
	}
}

func TestRebaseEmptyDetectionForLargeModeAndSubmoduleChanges(t *testing.T) {
	for _, kind := range []string{"large", "mode", "submodule"} {
		t.Run(kind, func(t *testing.T) {
			e, root, git, o := rebaseSetup(t)
			change := func() {
				switch kind {
				case "large":
					for i := range 1200 {
						writeFile(t, root, fmt.Sprintf("big/f%04d.txt", i), "x\n")
					}
					git("add", ".")
				case "mode":
					if err := os.Chmod(filepath.Join(root, "a.txt"), 0o755); err != nil {
						t.Fatal(err)
					}
					git("add", "a.txt")
				case "submodule":
					git("update-index", "--add", "--cacheinfo", "160000,"+o["A"]+",sub")
				}
			}
			git("checkout", "-q", "-b", "side", o["E"])
			change()
			git("commit", "-q", "-m", "dup change")
			dup := git("rev-parse", "HEAD")
			git("checkout", "-q", "main")
			change()
			git("commit", "-q", "-m", "upstream change")
			git("checkout", "-q", "side")
			if kind == "submodule" {
				// An uninitialised submodule is an empty directory.
				if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			p := planOf(t, root, o["E"], git("rev-parse", "main"))
			mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseReword, dup, "kept dup")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
			st := operationOf(t, e, root)
			if pi := interactiveOf(t, st); pi.Stop != protocol.GitRebaseStopEmpty || !st.Can.Continue.Allowed {
				t.Fatalf("%s: %+v %+v", kind, pi, st.Can)
			}
			mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
			if got := git("log", "-1", "--format=%s"); got != "kept dup" || git("diff", "--stat", "HEAD~1", "HEAD") != "" {
				t.Fatalf("%s kept: %q", kind, got)
			}
		})
	}
}

func TestRebasePlanBlocksNonUTF8Messages(t *testing.T) {
	_, root, git, o := rebaseSetup(t)
	writeFile(t, root, "l.txt", "l\n")
	git("add", ".")
	git("-c", "i18n.commitEncoding=ISO-8859-1", "commit", "-q", "-m", "caf\xe9")
	p := planOf(t, root, o["base"], "")
	if p.Blocked != "unsupported_message" || !strings.Contains(p.BlockedMessage, git("rev-parse", "--short=12", "HEAD")) {
		t.Fatalf("plan: %s %s", p.Blocked, p.BlockedMessage)
	}
}

func TestRebaseAbortListsCommitsMadeInATerminalAtABreak(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "t.txt", "t\n")
	git("add", "t.txt")
	git("commit", "-q", "-m", "from a terminal")
	st := operationOf(t, e, root)
	if st.AbortDropsCount != 1 || !strings.HasPrefix(st.AbortDropsCommits[0].Subject, "from a terminal") {
		t.Fatalf("drops: %+v", st.AbortDropsCommits)
	}
	r := mustGit(t, e, client.GitOperationAbortCommand("a", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if git("show", r.Git.Operation.BackupRef+":t.txt") != "t" {
		t.Fatal("backup ref")
	}
}

// ---- Fourth verify round ----

func TestRebaseCherryPickedDuplicateBecomesEmpty(t *testing.T) {
	for _, action := range []string{protocol.GitRebaseReword, protocol.GitRebasePick} {
		for _, keep := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s keep=%v", action, keep), func(t *testing.T) {
				e, root, git, o := rebaseSetup(t)
				writeFile(t, root, "x.txt", "X\n")
				git("add", ".")
				git("commit", "-q", "-m", "X")
				x := git("rev-parse", "HEAD")
				git("revert", "--no-edit", "HEAD")
				rv := git("rev-parse", "HEAD")
				git("cherry-pick", x)
				x2 := git("rev-parse", "HEAD")
				p := planOf(t, root, o["E"], "")
				second := protocol.GitRebaseEntry{Action: action, Commit: x2}
				if action == protocol.GitRebaseReword {
					second.Message = "X2 reworded"
				}
				mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(x), second, entry(protocol.GitRebaseDrop, rv, "")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
				st := operationOf(t, e, root)
				pi := interactiveOf(t, st)
				if pi.Stop != protocol.GitRebaseStopEmpty || pi.StepCommitted || !st.Can.Continue.Allowed || !st.Can.Skip.Allowed {
					t.Fatalf("duplicate: %+v %+v", pi, st.Can)
				}
				if keep {
					mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
					if got := len(subjects(git, o["E"]+"..HEAD")); got != 2 || git("diff", "--stat", "HEAD~1", "HEAD") != "" {
						t.Fatalf("kept: %q", subjects(git, o["E"]+"..HEAD"))
					}
					return
				}
				cmd, _ := client.GitOperationSkipCommand("s", gitTarget, st, true)
				mustGit(t, e, cmd, protocol.GitStateSucceeded)
				if got := subjects(git, o["E"]+"..HEAD"); !slices.Equal(got, []string{"X"}) {
					t.Fatalf("skipped: %q", got)
				}
			})
		}
	}
}

func TestRebaseFixedAuthorDateStillDetectsEmpty(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	t.Setenv("GIT_AUTHOR_DATE", "1700000000 +0000")
	// Upstream adds y.txt; the branch adds x.txt and then the same y.txt,
	// all with one author and date.
	git("checkout", "-q", "-b", "up", o["E"])
	writeFile(t, root, "y.txt", "Y\n")
	git("add", ".")
	git("commit", "-q", "-m", "Y upstream")
	git("checkout", "-q", "-b", "topic", o["E"])
	writeFile(t, root, "x.txt", "X\n")
	git("add", ".")
	git("commit", "-q", "-m", "X")
	x := git("rev-parse", "HEAD")
	writeFile(t, root, "y.txt", "Y\n")
	git("add", ".")
	git("commit", "-q", "-m", "Y")
	y := git("rev-parse", "HEAD")
	addGitThread(e, "p-git", "t-extra-topic", root)
	// A rewritten X, then Y empty.
	p := planOf(t, root, o["E"], git("rev-parse", "up"))
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(x), entry(protocol.GitRebaseReword, y, "Y again")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if pi := interactiveOf(t, operationOf(t, e, root)); pi.Stop != protocol.GitRebaseStopEmpty || pi.StepCommitted {
		t.Fatalf("after a rewritten step: %+v", pi)
	}
	mustGit(t, e, client.GitOperationAbortCommand("a", gitTarget, operationOf(t, e, root), true, true), protocol.GitStateSucceeded)

	// X recommitted by the server at a reset stop (same author and date),
	// then Y empty on top of it.
	p = planOf(t, root, o["E"], git("rev-parse", "up"))
	mustGit(t, e, rebaseCmd("rb2", p, []protocol.GitRebaseEntry{entry(protocol.GitRebaseEdit, x, ""), entry(protocol.GitRebaseReword, y, "Y again")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if pi := interactiveOf(t, st); pi.Stop != protocol.GitRebaseStopEmpty || pi.StepCommitted {
		t.Fatalf("after a server recommit: %+v", pi)
	}
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, git("rev-parse", "up")+"..HEAD"); !slices.Equal(got, []string{"Y again", "X"}) {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseConflictCommittedInATerminal(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("commit", "-q", "-am", "A2")
	a2 := git("rev-parse", "HEAD")
	p := planOf(t, root, o["D"], o["base"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), pick(a2)}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "a.txt", "res\n")
	git("add", "a.txt")
	git("commit", "-q", "--no-edit")
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopCommitted || !st.Can.Continue.Allowed || st.Can.Skip.Allowed || st.AbortDropsCount != 1 {
		t.Fatalf("committed outside: %+v %+v drops=%d", pi, st.Can, st.AbortDropsCount)
	}
	_, err := e.command(client.GitOperationContinueMessageCommand("cm", gitTarget, st, false, "no"))
	wantGitCode(t, err, "invalid")
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, o["base"]+"..HEAD"); !slices.Equal(got, []string{"A2", "E"}) || git("show", "HEAD:a.txt") != "res" {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseRestartBeforeAnyReadKeepsTheSnapshot(t *testing.T) {
	e, root, _, o := rebaseSetup(t)
	writeHook(t, root, "commit-msg", "grep -q 'D new' \"$1\" && exit 1; exit 0\n")
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseReword, o["D"], "D new")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	snap := e.current()
	recoverGitOps(&snap)
	e2 := testEngine(t)
	e2.snap, e2.rebaseDir = snap, e.rebaseDir
	st := operationOf(t, e2, root)
	if pi := interactiveOf(t, st); pi.Late || pi.Stop != protocol.GitRebaseStopMessage || !st.Can.Continue.Allowed || pi.StepMessage != "D new" {
		t.Fatalf("after restart: %+v %+v", pi, st.Can)
	}
}

func TestRebaseRescheduledPickAfterABreak(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	// An untracked file blocks E; Git reschedules it when continued in a
	// terminal.
	writeFile(t, root, "e.txt", "in the way\n")
	gitTry(t, root, "rebase", "--continue")
	st := operationOf(t, e, root)
	if st.Kind != protocol.GitOperationRebase || interactiveOf(t, st).Stop != protocol.GitRebaseStopRescheduled || st.Can.Continue.Allowed || st.Can.Skip.Allowed || len(st.ContinueInTheWay) == 0 {
		t.Fatalf("rescheduled: %+v %+v %v", st.Interactive, st.Can, st.ContinueInTheWay)
	}
	os.Remove(filepath.Join(root, "e.txt"))
	st = operationOf(t, e, root)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, st, false), protocol.GitStateSucceeded)
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"E", "D"}) {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseAbortCountIsALowerBoundWhenHistoryIsLeft(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	git("checkout", "-q", "--detach", o["A"])
	writeFile(t, root, "w.txt", "w\n")
	git("add", ".")
	git("commit", "-q", "-m", "elsewhere")
	st := operationOf(t, e, root)
	if !st.AbortDropsAtLeast || st.AbortDropsCount != 1 || !st.AbortDropsIncomplete {
		t.Fatalf("drops: %d at least %v %+v", st.AbortDropsCount, st.AbortDropsAtLeast, st.AbortDropsCommits)
	}
	_, err := e.command(client.GitOperationAbortCommand("a", gitTarget, st, true, false))
	wantGitCode(t, err, "drops_unacknowledged")
}

func TestRebasePlanCarriesRawMessagesAndUpstreamBase(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	_ = e
	f := commitWithHash(t, root, git, "f.txt", oddMessage)
	p := planOf(t, root, o["E"], "")
	if len(p.Commits) != 1 || p.Commits[0].Oid != f || p.Commits[0].Message != oddMessage || p.Commits[0].MessageTruncated {
		t.Fatalf("message: %+v", p.Commits)
	}
	if _, err := readRebasePlan(context.Background(), root, "upstream", ""); err == nil {
		t.Fatal("a branch without upstream was accepted")
	} else {
		wantGitCode(t, err, "no_upstream")
	}
	git("branch", "up", o["C"])
	git("branch", "-u", "up")
	up := planOf(t, root, "upstream", "")
	if up.Base != "refs/heads/up" || !up.BaseIsUpstream || up.BaseOid != o["C"] || len(up.Commits) != 3 {
		t.Fatalf("upstream plan: %+v", up)
	}
	mustGit(t, e, rebaseCmd("rb", up, client.DefaultRebaseEntries(up), client.RebaseOptions{}), protocol.GitStateSucceeded)
}

func TestRebaseStepMessagePrefill(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("add", ".")
	git("commit", "-q", "--cleanup=verbatim", "-m", oddMessage)
	a2 := git("rev-parse", "HEAD")
	p := planOf(t, root, o["D"], o["base"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), entry(protocol.GitRebaseEdit, a2, "")}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if pi := interactiveOf(t, st); pi.Stop != protocol.GitRebaseStopConflict || pi.StepMessage != oddMessage {
		t.Fatalf("prefill: %+v", pi)
	}
}

// ---- Server-made edit stops ----

// editConflictSetup stops at a conflict on the edit entry for A2 (a.txt
// changed, absent on base), after E.
func editConflictSetup(t *testing.T, mode string) (*engine, string, func(...string) string, map[string]string, string) {
	t.Helper()
	e, root, git, o := rebaseSetup(t)
	writeFile(t, root, "a.txt", "A changed\n")
	git("add", ".")
	git("commit", "-q", "--cleanup=verbatim", "-m", oddMessage)
	a2 := git("rev-parse", "HEAD")
	p := planOf(t, root, o["D"], o["base"])
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["E"]), {Action: protocol.GitRebaseEdit, Commit: a2, EditMode: mode}}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	if interactiveOf(t, operationOf(t, e, root)).Stop != protocol.GitRebaseStopConflict {
		t.Fatal("no conflict")
	}
	writeFile(t, root, "a.txt", "res\n")
	writeFile(t, root, "extra.txt", "extra\n")
	git("add", "a.txt", "extra.txt")
	return e, root, git, o, a2
}

func TestRebaseEditAfterConflictAmendMode(t *testing.T) {
	e, root, git, o, _ := editConflictSetup(t, protocol.GitRebaseEditAmend)
	r := mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if r.Git.Operation.Outcome != protocol.GitOutcomeStopped || pi.Stop != protocol.GitRebaseStopEditAmend || !pi.ServerEdit || !st.Can.Continue.Allowed || st.AbortDropsCount != 1 || r.Git.Operation.State.Interactive.StepMessage != oddMessage {
		t.Fatalf("edit stop: %+v %+v drops=%d", pi, st.Can, st.AbortDropsCount)
	}
	if raw, _ := commitMessage(context.Background(), mustReader(t, root), git("rev-parse", "HEAD")); raw != oddMessage || git("log", "-1", "--format=%an") != "Test" {
		t.Fatalf("resolution commit: %q", raw)
	}
	// Amend with another change, as planned.
	writeFile(t, root, "amend.txt", "amended\n")
	git("add", "amend.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := len(subjects(git, o["base"]+"..HEAD")); got != 2 || git("show", "HEAD:amend.txt") != "amended" || git("show", "HEAD:a.txt") != "res" || operationOf(t, e, root).Kind != "" {
		t.Fatalf("history: %q", subjects(git, o["base"]+"..HEAD"))
	}
	if raw, _ := commitMessage(context.Background(), mustReader(t, root), git("rev-parse", "HEAD")); raw != oddMessage {
		t.Fatalf("message after amend: %q", raw)
	}
}

func TestRebaseEditAfterConflictResetModeSplit(t *testing.T) {
	e, root, git, o, _ := editConflictSetup(t, protocol.GitRebaseEditReset)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopEditReset || !pi.ServerEdit || !pi.Staged || st.HeadOid != git("rev-parse", "HEAD") {
		t.Fatalf("reset stop: %+v", pi)
	}
	// Nothing staged and nothing committed would drop the commit.
	git("reset", "-q")
	_, err := e.command(client.GitOperationContinueCommand("c0", gitTarget, operationOf(t, e, root), false))
	wantGitCode(t, err, "nothing_to_recommit")
	// Split: extra.txt first, then the rest with the original message.
	git("add", "extra.txt")
	mustGit(t, e, client.GitOperationCommitCommand("split", gitTarget, operationOf(t, e, root), "extra part"), protocol.GitStateSucceeded)
	git("add", "a.txt")
	mustGit(t, e, client.GitOperationContinueCommand("c2", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	if got := subjects(git, o["base"]+"..HEAD"); !slices.Equal(got, []string{"Subject", "extra part", "E"}) {
		t.Fatalf("history: %q", got)
	}
	if raw, _ := commitMessage(context.Background(), mustReader(t, root), git("rev-parse", "HEAD")); raw != oddMessage {
		t.Fatalf("recommitted message: %q", raw)
	}
	if git("log", "-1", "--format=%an") != "Test" || git("log", "-1", "--format=%an", "HEAD~1") != "Test" {
		t.Fatal("authors")
	}
}

func TestRebaseEditAfterConflictSurvivesRestartAndAborts(t *testing.T) {
	e, root, git, _, a2 := editConflictSetup(t, protocol.GitRebaseEditAmend)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	resolution := git("rev-parse", "HEAD")
	snap := e.current()
	recoverGitOps(&snap)
	e2 := testEngine(t)
	e2.snap, e2.rebaseDir = snap, e.rebaseDir
	st := operationOf(t, e2, root)
	pi := interactiveOf(t, st)
	if pi.Stop != protocol.GitRebaseStopEditAmend || !pi.ServerEdit || pi.Late || st.AbortDropsCount != 1 || st.AbortDropsCommits[0].Oid != resolution {
		t.Fatalf("after restart: %+v drops=%+v", pi, st.AbortDropsCommits)
	}
	_, err := e2.command(client.GitOperationAbortCommand("a0", gitTarget, st, true, false))
	wantGitCode(t, err, "drops_unacknowledged")
	r := mustGit(t, e2, client.GitOperationAbortCommand("a", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if git("rev-parse", "HEAD") != a2 || git("rev-parse", r.Git.Operation.BackupRef) != resolution {
		t.Fatalf("abort: %+v", r.Git)
	}
}

// assertListedKept checks that every commit an abort listed is reachable
// from one of the refs its result names.
func assertListedKept(t *testing.T, git func(...string) string, listed []protocol.GitOperationCommit, op *protocol.GitOperationResult) {
	t.Helper()
	refs := append([]string{op.BackupRef}, op.BackupRefs...)
	for _, c := range listed {
		kept := false
		for _, ref := range refs {
			if ref != "" && strings.Contains(git("rev-list", ref), c.Oid) {
				kept = true
			}
		}
		if !kept {
			t.Fatalf("listed commit %s (%s) is not reachable from %v", c.Oid, c.Subject, refs)
		}
	}
}

func TestRebaseAbortKeepsEveryListedCommit(t *testing.T) {
	// Reset-mode edit after a conflict: the resolution commit is orphaned
	// by the soft reset; a split commit sits on HEAD.
	e, root, git, _, _ := editConflictSetup(t, protocol.GitRebaseEditReset)
	mustGit(t, e, client.GitOperationContinueCommand("c", gitTarget, operationOf(t, e, root), false), protocol.GitStateSucceeded)
	git("reset", "-q", "a.txt")
	mustGit(t, e, client.GitOperationCommitCommand("split", gitTarget, operationOf(t, e, root), "extra part"), protocol.GitStateSucceeded)
	st := operationOf(t, e, root)
	if st.AbortDropsCount != 2 {
		t.Fatalf("listed: %+v", st.AbortDropsCommits)
	}
	r := mustGit(t, e, client.GitOperationAbortCommand("a", gitTarget, st, true, true), protocol.GitStateSucceeded)
	if len(r.Git.Operation.BackupRefs) != 1 || !strings.Contains(r.Git.Message, r.Git.Operation.BackupRefs[0]) {
		t.Fatalf("refs: %+v %q", r.Git.Operation, r.Git.Message)
	}
	assertListedKept(t, git, st.AbortDropsCommits, r.Git.Operation)

	// A commit made at a break, then amended in a terminal: both listed,
	// the replaced one kept by its own ref.
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	writeFile(t, root, "n.txt", "n\n")
	git("add", "n.txt")
	mustGit(t, e, client.GitOperationCommitCommand("ins", gitTarget, operationOf(t, e, root), "inserted"), protocol.GitStateSucceeded)
	git("commit", "-q", "--amend", "-m", "inserted, amended")
	st = operationOf(t, e, root)
	if st.AbortDropsCount != 2 {
		t.Fatalf("listed: %+v", st.AbortDropsCommits)
	}
	r = mustGit(t, e, client.GitOperationAbortCommand("a2", gitTarget, st, true, true), protocol.GitStateSucceeded)
	assertListedKept(t, git, st.AbortDropsCommits, r.Git.Operation)
}

func TestRebaseTwoBreaksAreBreaks(t *testing.T) {
	e, root, git, o := rebaseSetup(t)
	p := planOf(t, root, o["C"], "")
	mustGit(t, e, rebaseCmd("rb", p, []protocol.GitRebaseEntry{pick(o["D"]), {Action: protocol.GitRebaseBreak}, {Action: protocol.GitRebaseBreak}, pick(o["E"])}, client.RebaseOptions{}), protocol.GitStateSucceeded)
	for i := range 2 {
		st := operationOf(t, e, root)
		if pi := interactiveOf(t, st); pi.Stop != protocol.GitRebaseStopBreak || !st.Can.Continue.Allowed {
			t.Fatalf("break %d: %+v %+v", i+1, pi, st.Can)
		}
		mustGit(t, e, client.GitOperationContinueCommand(fmt.Sprintf("c%d", i), gitTarget, st, false), protocol.GitStateSucceeded)
	}
	if got := subjects(git, o["C"]+"..HEAD"); !slices.Equal(got, []string{"E", "D"}) || operationOf(t, e, root).Kind != "" {
		t.Fatalf("history: %q", got)
	}
}

func TestRebaseUpstreamMissingIsNamed(t *testing.T) {
	_, root, git, o := rebaseSetup(t)
	// A remote upstream never fetched.
	git("config", "branch.main.remote", "origin")
	git("config", "branch.main.merge", "refs/heads/main")
	_, err := readRebasePlan(context.Background(), root, "upstream", "")
	wantGitCode(t, err, "upstream_missing")
	if !strings.Contains(err.Error(), "refs/remotes/origin/main") {
		t.Fatalf("message: %v", err)
	}
	// A local upstream that was deleted.
	git("branch", "gone", o["C"])
	git("branch", "-u", "gone")
	git("branch", "-D", "gone")
	_, err = readRebasePlan(context.Background(), root, "upstream", "")
	wantGitCode(t, err, "upstream_missing")
	if !strings.Contains(err.Error(), "refs/heads/gone") {
		t.Fatalf("message: %v", err)
	}
	git("config", "--unset", "branch.main.remote")
	git("config", "--unset", "branch.main.merge")
	_, err = readRebasePlan(context.Background(), root, "upstream", "")
	wantGitCode(t, err, "no_upstream")
}
