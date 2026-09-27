package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Interactive rebase (ADR 0026; wire contract in protocol/git_rebase.go).
//
// GET /v1/git/rebase/plan reads the commits a rebase of the checked-out
// branch would rewrite with a fingerprint. git.rebase with
// GitIntegrate.Interactive validates the plan (protocol.ValidateRebasePlan
// against a fresh read with the same fingerprint), writes the pinned todo
// and stored messages to a per-operation file in the application home, and
// runs `git rebase -i` with the editor helpers of git_rebase_helper.go. The
// rest is the ADR 0023 operation machinery: the operation reserves the
// checkout, and git.operation_continue, _skip and _abort act on it, with the
// interactive additions here (edit and break stops, messages, commit
// failures, git.operation_commit).

const (
	gitRebaseMinMajor, gitRebaseMinMinor = 2, 38
	gitRebaseLogMax                      = 32 << 20
	gitRebaseBodyMax                     = 16 << 10
	gitRebaseRefsMax                     = 200
	gitRebaseTraceMax                    = 32 << 20
	gitRebaseDetailMax                   = 1024
	gitRebaseRoot                        = "root"
)

// gitRebaseInteractiveSupported: --update-refs (and so an explicit
// --no-update-refs) exists from Git 2.38; --empty and
// --reapply-cherry-picks are older.
func gitRebaseInteractiveSupported() bool {
	v, err := gitVersion()
	return err == nil && (v[0] > gitRebaseMinMajor || (v[0] == gitRebaseMinMajor && v[1] >= gitRebaseMinMinor))
}

// rebaseHelperExecutable is the program Git runs as the editor helpers: this
// binary (tests replace it through TestMain, which runs the helper mode).
var rebaseHelperExecutable = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// rebaseConfigArgs neutralise every configuration that changes how an
// interactive rebase generates or runs its todo, or edits messages, so the
// pinned plan runs as reviewed (ADR 0026). The user's identity, signing,
// hooks and merge drivers still apply.
var rebaseConfigArgs = []string{
	"-c", "rebase.autoSquash=false", "-c", "rebase.autoStash=false", "-c", "rebase.updateRefs=false",
	"-c", "rebase.missingCommitsCheck=error", "-c", "rebase.abbreviateCommands=false", "-c", "rebase.instructionFormat=%s",
	"-c", "rebase.rescheduleFailedExec=false", "-c", "rebase.rebaseMerges=false", "-c", "rebase.forkPoint=false",
	"-c", "rebase.backend=merge", "-c", "rebase.stat=false", "-c", "rerere.enabled=false",
	"-c", "commit.cleanup=verbatim", "-c", "commit.verbose=false",
	"-c", "advice.waitingForEditor=false",
}

// shellQuote quotes s for the POSIX shell Git runs editors with.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// rebaseOpDir is the per-operation directory in the application home.
func rebaseOpDir(root, operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return filepath.Join(root, hex.EncodeToString(sum[:16]))
}

// emptyTree is the empty tree's object ID in the hash of head.
func emptyTree(head string) string {
	if len(head) == 64 {
		return "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
	}
	return "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
}

// ---- Plan read ----

func (e *engine) gitRebasePlan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readRebasePlan(ctx, dir, q.Get("base"), q.Get("onto"))
	})
}

func validRebaseRevisions(base, onto string) error {
	if base != gitRebaseRoot && base != "upstream" && !validFullRef(base) && !gitFullHash.MatchString(base) {
		return failure("invalid", "base must be a full branch or remote-tracking ref, a full commit hash, root or upstream")
	}
	if onto != "" && !validFullRef(onto) && !gitFullHash.MatchString(onto) {
		return failure("invalid", "onto must be a full branch or remote-tracking ref, or a full commit hash")
	}
	return nil
}

func readRebasePlan(ctx context.Context, dir, base, onto string) (protocol.GitRebasePlan, error) {
	p := protocol.GitRebasePlan{Base: base, Onto: onto, Commits: []protocol.GitRebasePlanCommit{}}
	if err := validRebaseRevisions(base, onto); err != nil {
		return p, err
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return p, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return p, err
	}
	head, err := readHead(ctx, g)
	if err != nil {
		return p, err
	}
	return rebasePlanFor(ctx, g, head, base, onto)
}

// resolveRevision resolves a full ref or hash to a commit and names it.
func resolveRevision(ctx context.Context, g *gitReader, rev string) (oid, label string, err error) {
	ref, oid, err := integrateTarget(ctx, g, rev)
	if err != nil {
		return "", "", err
	}
	if ref != "" {
		return oid, refShortName(ref), nil
	}
	return oid, refLabel(ctx, g, oid), nil
}

// rebasePlanFor reads the plan of rebasing head's branch from base onto
// onto (both as requested).
func rebasePlanFor(ctx context.Context, g *gitReader, head gitHead, base, onto string) (protocol.GitRebasePlan, error) {
	p := protocol.GitRebasePlan{Base: base, Onto: onto, Branch: head.branch, HeadOid: head.oid, Commits: []protocol.GitRebasePlanCommit{}}
	block := func(err error) {
		if pe, ok := err.(*protocol.Error); ok && pe != nil && p.Blocked == "" {
			p.Blocked, p.BlockedMessage = pe.Code, pe.Message
		}
	}
	if head.oid == "" {
		block(failure("unborn", "the branch has no commits yet"))
		return p, nil
	}
	var err error
	if base == "upstream" {
		// The branch's configured upstream, resolved here (local
		// upstreams included); the plan names its full ref.
		up := ""
		if head.branch != "" {
			up = upstreamRef(ctx, g)
		}
		if up == "" || !validFullRef(up) {
			if configured := configuredUpstream(ctx, g, head.branch); configured != "" {
				return p, failure("upstream_missing", "the branch's upstream "+configured+" does not exist here (fetch it, or set another upstream)")
			}
			return p, failure("no_upstream", "the checked-out branch has no upstream")
		}
		base, p.Base = up, up
	}
	if base == gitRebaseRoot {
		p.Root, p.BaseLabel = true, "root"
	} else if p.BaseOid, p.BaseLabel, err = resolveRevision(ctx, g, base); err != nil {
		return p, err
	}
	if validFullRef(base) && head.branch != "" && upstreamRef(ctx, g) == base {
		p.BaseIsUpstream = true
	}
	switch {
	case onto != "":
		if p.OntoOid, p.OntoLabel, err = resolveRevision(ctx, g, onto); err != nil {
			return p, err
		}
	case p.Root:
		p.OntoLabel = "a new root commit"
	default:
		p.OntoOid, p.OntoLabel = p.BaseOid, p.BaseLabel
	}
	if !gitRebaseInteractiveSupported() {
		block(failure("not_supported", fmt.Sprintf("interactive rebase needs Git %d.%d or newer", gitRebaseMinMajor, gitRebaseMinMinor)))
	}
	if head.branch == "" {
		block(failure("detached", "HEAD is detached; switch to a branch first"))
	}
	if err := refuseOperation(ctx, g, "starting a rebase"); err != nil {
		block(err)
	}
	if err := checkTrackedClean(ctx, g); err != nil {
		block(err)
	}
	rangeArgs := []string{head.oid}
	if !p.Root {
		rangeArgs = append(rangeArgs, "^"+p.BaseOid)
	}
	out, _, err := g.read(ctx, 4096, append(append([]string{"rev-list", "--count"}, rangeArgs...), "--")...)
	if err != nil {
		return p, failure("unavailable", "the commits to rebase could not be counted")
	}
	count, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	if count > protocol.GitRebasePlanCommitsMax {
		block(failure("too_many_commits", fmt.Sprintf("%d commits would be rewritten; plans are limited to %d, use a terminal", count, protocol.GitRebasePlanCommitsMax)))
		p.Fingerprint = rebaseFingerprint(p)
		return p, nil
	}
	if count > 0 {
		var unsupported []string
		if p.Commits, unsupported, err = rebaseCommits(ctx, g, rangeArgs); err != nil {
			return p, err
		}
		if err := fillRawMessages(ctx, g, p.Commits, unsupported); err != nil {
			return p, err
		}
		if len(unsupported) > 0 {
			short := make([]string, len(unsupported))
			for i, oid := range unsupported {
				short[i] = shortOid(oid)
			}
			block(failure("unsupported_message", "these commits have messages that are not UTF-8 (an encoding header or invalid bytes), which would be altered here: "+listPaths(short)+"; rebase them in a terminal"))
		}
	}
	nonMerge := 0
	for _, c := range p.Commits {
		if c.Merge {
			p.MergeCount++
		} else {
			nonMerge++
		}
	}
	if nonMerge == 0 {
		block(failure("empty_range", "there are no commits of "+cmpOr(head.branch, "HEAD")+" after "+p.BaseLabel+" to rebase"))
	}
	markPublished(ctx, g, rangeArgs, &p)
	p.UpdateRefs, p.UpdateRefsUnsupported = rebaseUpdateRefs(ctx, g, head.branch, p.Commits)
	if p.Blocked == "" && nonMerge > 0 {
		target := p.OntoOid
		if target == "" {
			target = emptyTree(head.oid)
		}
		block(checkRebaseWrittenPaths(ctx, g, head.oid, target, rangeArgs))
	}
	p.Fingerprint = rebaseFingerprint(p)
	return p, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// rebaseCommits lists the range oldest first, as Git's todo orders it.
// unsupported names commits whose message is not UTF-8 (an encoding
// header, or invalid bytes): JSON would alter them, so the plan is blocked.
func rebaseCommits(ctx context.Context, g *gitReader, rangeArgs []string) (commits []protocol.GitRebasePlanCommit, unsupported []string, err error) {
	args := append([]string{"-c", "i18n.logOutputEncoding=UTF-8", "log", "-z", "--reverse", "--topo-order", "--no-show-signature", "--no-color", "--format=%H%x01%P%x01%an%x01%ae%x01%aI%x01%e%x01%B"}, rangeArgs...)
	out, truncated, err := g.read(ctx, gitRebaseLogMax, append(args, "--")...)
	if err != nil {
		return nil, nil, failure("unavailable", "the commits to rebase could not be read")
	}
	if truncated {
		return nil, nil, failure("too_many_commits", "the commits to rebase are too large to list; use a terminal")
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(strings.TrimPrefix(rec, "\n"), "\x01", 7)
		if len(f) != 7 || !gitFullHash.MatchString(f[0]) {
			return nil, nil, failure("unavailable", "the commits to rebase could not be parsed")
		}
		c := protocol.GitRebasePlanCommit{Oid: f[0], Parents: strings.Fields(f[1]), AuthorName: boundedSubject(f[2]), AuthorEmail: boundedSubject(f[3]), AuthorDate: strings.TrimSpace(f[4])}
		for _, parent := range c.Parents {
			if !gitFullHash.MatchString(parent) {
				return nil, nil, failure("unavailable", "the commits to rebase could not be parsed")
			}
		}
		c.Merge = len(c.Parents) > 1
		if enc := strings.ToLower(strings.TrimSpace(f[5])); (enc != "" && enc != "utf-8" && enc != "utf8") || !utf8.ValidString(f[6]) {
			unsupported = append(unsupported, f[0])
		}
		msg := strings.ToValidUTF8(strings.TrimRight(f[6], "\n"), "�")
		subject, body, _ := strings.Cut(msg, "\n")
		c.Subject = boundedSubject(subject)
		body = strings.Trim(body, "\n")
		if len(body) > gitRebaseBodyMax {
			cut := gitRebaseBodyMax
			for cut > 0 && !utf8.RuneStart(body[cut]) {
				cut--
			}
			body, c.BodyTruncated = body[:cut], true
		}
		c.Body = body
		commits = append(commits, c)
	}
	return commits, unsupported, nil
}

// configuredUpstream names the upstream branch.<name>.remote and .merge
// configure, whether or not it exists ("" when none is configured).
func configuredUpstream(ctx context.Context, g *gitReader, branch string) string {
	if branch == "" {
		return ""
	}
	get := func(key string) string {
		out, _, err := g.read(ctx, 4096, "config", "--get", "branch."+branch+"."+key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	remote, merge := get("remote"), get("merge")
	if remote == "" || merge == "" {
		return ""
	}
	name := strings.TrimPrefix(merge, "refs/heads/")
	if remote == "." {
		return "refs/heads/" + name
	}
	return "refs/remotes/" + remote + "/" + name
}

// fillRawMessages sets each commit's Message to its raw message, byte for
// byte (`cat-file --batch`), cut at GitRebaseMessageMax on a rune boundary
// (MessageTruncated); commits with messages that are not UTF-8 are left
// empty (the plan is blocked for them).
func fillRawMessages(ctx context.Context, g *gitReader, commits []protocol.GitRebasePlanCommit, unsupported []string) error {
	if len(commits) == 0 {
		return nil
	}
	var in strings.Builder
	for _, c := range commits {
		in.WriteString(c.Oid + "\n")
	}
	out, truncated, err := g.readInput(ctx, gitRebaseLogMax, strings.NewReader(in.String()), "cat-file", "--batch")
	if err != nil || truncated {
		return failure("unavailable", "the commit messages could not be read")
	}
	rest := out
	for i := range commits {
		header, after, ok := bytes.Cut(rest, []byte("\n"))
		f := strings.Fields(string(header))
		if !ok || len(f) != 3 || f[0] != commits[i].Oid || f[1] != "commit" {
			return failure("unavailable", "the commit messages could not be parsed")
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size+1 > len(after) {
			return failure("unavailable", "the commit messages could not be parsed")
		}
		raw := string(after[:size])
		rest = after[size+1:]
		if slices.Contains(unsupported, commits[i].Oid) {
			continue
		}
		_, msg, _ := strings.Cut(raw, "\n\n")
		if len(msg) > protocol.GitRebaseMessageMax {
			cut := protocol.GitRebaseMessageMax
			for cut > 0 && !utf8.RuneStart(msg[cut]) {
				cut--
			}
			msg, commits[i].MessageTruncated = msg[:cut], true
		}
		commits[i].Message = msg
	}
	return nil
}

// markPublished marks commits reachable from a remote-tracking ref; when
// that cannot be read, every commit counts as published.
func markPublished(ctx context.Context, g *gitReader, rangeArgs []string, p *protocol.GitRebasePlan) {
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(append([]string{"rev-list"}, rangeArgs...), "--not", "--remotes", "--")...)
	unpublished := map[string]bool{}
	for _, oid := range strings.Fields(string(out)) {
		unpublished[oid] = true
	}
	for i := range p.Commits {
		c := &p.Commits[i]
		c.Published = err != nil || truncated || !unpublished[c.Oid]
		p.Published = p.Published || c.Published
	}
}

// rebaseUpdateRefs lists the local branches --update-refs would move: those
// pointing at a non-merge commit of the range, except the checked-out
// branch and branches checked out in another worktree (Git leaves those).
func rebaseUpdateRefs(ctx context.Context, g *gitReader, branch string, commits []protocol.GitRebasePlanCommit) (refs []protocol.GitRebaseUpdateRef, unsupported []string) {
	position := map[string]int{}
	for i, c := range commits {
		if !c.Merge {
			position[c.Oid] = i + 1
		}
	}
	if len(position) == 0 {
		return nil, nil
	}
	out, _, err := g.read(ctx, 4<<20, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/")
	if err != nil {
		return nil, nil
	}
	checkedOut := map[string]bool{"refs/heads/" + branch: true}
	if wt, _, err := g.read(ctx, 1<<20, "worktree", "list", "--porcelain", "-z"); err == nil {
		for _, field := range strings.Split(string(wt), "\x00") {
			if ref, ok := strings.CutPrefix(field, "branch "); ok {
				checkedOut[ref] = true
			}
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, ref, ok := strings.Cut(line, " ")
		if !ok || position[oid] == 0 || checkedOut[ref] {
			continue
		}
		if !validFullRef(ref) {
			unsupported = append(unsupported, strings.ToValidUTF8(ref, "�"))
			continue
		}
		refs = append(refs, protocol.GitRebaseUpdateRef{Ref: ref, Oid: oid})
	}
	slices.SortFunc(refs, func(a, b protocol.GitRebaseUpdateRef) int {
		if d := position[a.Oid] - position[b.Oid]; d != 0 {
			return d
		}
		return strings.Compare(a.Ref, b.Ref)
	})
	if len(refs) > gitRebaseRefsMax {
		refs = refs[:gitRebaseRefsMax]
	}
	return refs, unsupported
}

// rebaseFingerprint pins what a plan was read against.
func rebaseFingerprint(p protocol.GitRebasePlan) string {
	h := sha256.New()
	base := p.BaseOid
	if p.Root {
		base = gitRebaseRoot
	}
	fmt.Fprintf(h, "rebase-plan-v1\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00", p.Branch, p.HeadOid, base, p.OntoOid, len(p.Commits), p.Blocked)
	for _, c := range p.Commits {
		fmt.Fprintf(h, "%s %s\n", c.Oid, strings.Join(c.Parents, " "))
	}
	h.Write([]byte("refs\x00"))
	for _, r := range p.UpdateRefs {
		fmt.Fprintf(h, "%s %s\n", r.Ref, r.Oid)
	}
	for _, r := range p.UpdateRefsUnsupported {
		fmt.Fprintf(h, "unsupported %s\n", r)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---- Todo ----

// buildRebaseTodo turns validated entries into Git's todo: one line per
// entry with full hashes and, with update refs, `update-ref` lines after
// the chain that absorbs each branch's commit (so the branch moves to the
// commit that contains it; after a drop, to the commit before).
func buildRebaseTodo(plan protocol.GitRebasePlan, ir protocol.GitRebaseInteractive) (string, []rebaseLine, []string) {
	refsAt := map[string][]string{}
	var refNames []string
	if ir.UpdateRefs {
		for _, r := range plan.UpdateRefs {
			refsAt[r.Oid] = append(refsAt[r.Oid], r.Ref)
			refNames = append(refNames, r.Ref)
		}
	}
	after := make([][]string, len(ir.Entries))
	for i, e := range ir.Entries {
		if e.Commit == "" || len(refsAt[e.Commit]) == 0 {
			continue
		}
		end := i
		for end+1 < len(ir.Entries) && (ir.Entries[end+1].Action == protocol.GitRebaseSquash || ir.Entries[end+1].Action == protocol.GitRebaseFixup) {
			end++
		}
		after[end] = append(after[end], refsAt[e.Commit]...)
	}
	var b strings.Builder
	var lines []rebaseLine
	for i, e := range ir.Entries {
		cmd := e.Action
		if e.Action == protocol.GitRebaseFixup && e.Fixup != "" {
			cmd = "fixup -" + e.Fixup
		}
		line := rebaseLine{Command: cmd, Oid: e.Commit, Entry: i, Message: e.Message, HasMessage: e.Message != ""}
		if e.Action == protocol.GitRebaseEdit {
			line.EditMode = cmpOr(e.EditMode, protocol.GitRebaseEditReset)
		}
		lines = append(lines, line)
		b.WriteString(strings.TrimSpace(cmd + " " + e.Commit))
		b.WriteByte('\n')
		for _, ref := range after[i] {
			lines = append(lines, rebaseLine{Command: "update-ref", Oid: ref, Entry: -1})
			b.WriteString("update-ref " + ref + "\n")
		}
	}
	return b.String(), lines, refNames
}

// ---- Start ----

// prepareRebaseInteractive prepares git.rebase with an interactive plan.
func prepareRebaseInteractive(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.Integrate
	ir := *req.Interactive
	if !gitRebaseInteractiveSupported() {
		return nil, failure("not_supported", fmt.Sprintf("interactive rebase needs Git %d.%d or newer", gitRebaseMinMajor, gitRebaseMinMinor))
	}
	if w.rebaseDir == "" {
		return nil, failure("not_supported", "this server has no storage for rebase plans")
	}
	exe, err := rebaseHelperExecutable()
	if err != nil {
		return nil, failure("unavailable", "the rebase helper could not be located: "+err.Error())
	}
	cur, err := readHead(ctx, g)
	if err != nil {
		return nil, err
	}
	switch {
	case cur.oid == "":
		return nil, failure("unborn", "the branch has no commits yet")
	case cur.branch == "":
		return nil, failure("detached", "HEAD is detached; switch to a branch first")
	}
	if err := checkHeadPins(cur, req.ExpectedBranch, req.ExpectedHead); err != nil {
		return nil, err
	}
	plan, err := rebasePlanFor(ctx, g, cur, ir.Base, ir.Onto)
	if err != nil {
		return nil, err
	}
	if plan.Blocked != "" {
		return nil, failure(plan.Blocked, plan.BlockedMessage)
	}
	if plan.Fingerprint != ir.Fingerprint {
		return nil, failure("stale_plan", "the branch, its base or its commits changed since the plan was read; read it again and review the plan")
	}
	if perr := protocol.ValidateRebasePlan(plan, ir.Entries); perr != nil {
		return nil, perr
	}
	switch {
	case plan.MergeCount > 0 && !ir.AcknowledgeMerges:
		return nil, failure("merges_unacknowledged", fmt.Sprintf("the range contains %d merge commits; the rebase drops them and linearises their side commits, confirm that first", plan.MergeCount))
	case ir.UpdateRefs && len(plan.UpdateRefsUnsupported) > 0:
		return nil, failure("update_refs_unsupported", "these branches point into the range but their names cannot be handled here: "+listPaths(plan.UpdateRefsUnsupported)+"; start without moving other branches, or rename them")
	case plan.Published && !req.AcknowledgePublished:
		return nil, failure("published_commit", "commits to be rewritten are already on a remote-tracking branch; rebasing rewrites published history")
	}
	if err := checkIdentity(ctx, w); err != nil {
		return nil, err
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	refs := []string{"refs/heads/" + cur.branch}
	if ir.UpdateRefs {
		for _, r := range plan.UpdateRefs {
			refs = append(refs, r.Ref)
		}
	}
	if err := w.checkRefLocks(refs...); err != nil {
		return nil, err
	}
	todo, lines, refNames := buildRebaseTodo(plan, ir)
	var expected []string
	for _, pc := range plan.Commits {
		if !pc.Merge {
			expected = append(expected, pc.Oid)
		}
	}
	rec := &protocol.GitRebaseRecord{Fingerprint: plan.Fingerprint, Base: ir.Base, BaseOid: plan.BaseOid, Root: plan.Root, Entries: len(ir.Entries), MergesDropped: plan.MergeCount}
	for _, l := range lines {
		rec.Lines, rec.EditModes = append(rec.Lines, l.Entry), append(rec.EditModes, l.EditMode)
	}
	if ir.UpdateRefs {
		rec.UpdateRefs = slices.Clone(plan.UpdateRefs)
	}
	args := append(append(append([]string{}, operationArgs...), rebaseConfigArgs...), "rebase", "--interactive", "--no-autosquash", "--no-autostash",
		"--no-fork-point", "--reapply-cherry-picks", "--empty=stop", "--no-rerere-autoupdate")
	if ir.UpdateRefs {
		args = append(args, "--update-refs")
	} else {
		args = append(args, "--no-update-refs")
	}
	if plan.OntoOid != "" && (plan.Root || plan.OntoOid != plan.BaseOid) {
		args = append(args, "--onto", plan.OntoOid)
	}
	if plan.Root {
		args = append(args, "--root")
	} else {
		args = append(args, plan.BaseOid)
	}
	id := "op-" + c.ID
	target := protocol.GitOperationCommit{Oid: plan.OntoOid, Label: plan.OntoLabel}
	if plan.OntoOid != "" {
		target.Subject = commitSubject(ctx, g, plan.OntoOid)
	}
	dir := rebaseOpDir(w.rebaseDir, id)
	p := &gitPlan{}
	p.journal = func(s *protocol.Snapshot, res *protocol.GitResult, now string) error {
		if res == nil {
			putGitOperation(s, protocol.GitOperationRecord{OperationID: id, Checkout: w.top, Kind: protocol.GitOperationRebase, State: protocol.GitOperationRunning,
				Branch: cur.branch, OrigHead: cur.oid, Target: target, Interactive: rec,
				StartCommandID: c.ID, LastCommandID: c.ID, ThreadID: c.ThreadID, ProjectID: c.ProjectID, StartedAt: now, UpdatedAt: now})
			return nil
		}
		if res.Operation != nil {
			res.Operation.OperationID = id
		}
		r := gitOperationFor(s, w.top)
		if r == nil || r.OperationID != id {
			return nil
		}
		if res.Operation != nil && res.Operation.Outcome == protocol.GitOutcomeNotStarted {
			removeGitOperation(s, id)
			if res.Operation.State != nil {
				annotateOperation(res.Operation.State, nil)
			}
			res.Operation.OperationID = ""
			return nil
		}
		recordStopSnapshot(r, operationStateOf(res), false, false)
		if st := operationStateOf(res); st != nil && r.Target.Oid == "" && st.Target != nil && st.OrigHead == r.OrigHead {
			// Git created the new root a --root rebase replays onto.
			r.Target.Oid, r.Target.Subject = st.Target.Oid, st.Target.Subject
		}
		recordRebaseFailure(r, res)
		applyOperationResult(r, res, "", now)
		if st := operationStateOf(res); st != nil {
			annotateOperation(st, r)
		}
		return nil
	}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: protocol.GitOperationRebase, HeadBefore: cur.oid}
		defer func() { res.Operation = op }()
		notStarted := func(code, message string) protocol.GitResult {
			op.Outcome = protocol.GitOutcomeNotStarted
			return gitResult(protocol.GitStateFailed, code, message+"; nothing was changed", nil)
		}
		if again, err := readHead(ctx, g); err != nil || checkHeadPins(again, req.ExpectedBranch, req.ExpectedHead) != nil {
			res = notStarted("stale_head", "HEAD moved after review")
			return res
		}
		end, refused := beginWorktreeRewrite(ctx, p.runtime(ctx), w.top)
		defer end()
		if refused != nil {
			op.Outcome, res = protocol.GitOutcomeNotStarted, *refused
			return res
		}
		// Saving open documents may have changed tracked files; the plan is
		// read again after that, with every start check.
		again, err := rebasePlanFor(ctx, g, cur, ir.Base, ir.Onto)
		switch {
		case err != nil:
			res = notStarted("unavailable", "the plan could not be read again")
			return res
		case again.Blocked == "dirty_tree":
			res = notStarted("documents_changed_tree", "saving open documents changed tracked files, so the rebase did not start; review, commit or discard those changes and start again")
			return res
		case again.Blocked != "":
			res = notStarted(again.Blocked, again.BlockedMessage)
			return res
		case again.Fingerprint != plan.Fingerprint:
			res = notStarted("stale_plan", "the branch or its commits changed after review")
			return res
		}
		ontoTree := plan.OntoOid
		if ontoTree == "" {
			ontoTree = emptyTree(cur.oid)
		}
		before, err := snapshotRewrite(ctx, g, w.top, cur.oid, ontoTree)
		if err != nil {
			res = notStarted("unavailable", "the working tree could not be read")
			return res
		}
		r := &rebaseRun{dir: dir, exe: exe}
		if err := r.writeState(id, w.gitDir, expected, refNames, todo, lines); err != nil {
			res = notStarted("storage", "the plan could not be stored for Git's editor helpers ("+err.Error()+")")
			return res
		}
		run := r.runGit(ctx, w, args...)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, err := w.observe(vctx, g)
		if err != nil {
			op.Outcome, res = protocol.GitOutcomeUnknown, gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the result could not be read; refresh", run.output)
			return res
		}
		op.HeadAfter, res.Commit = after.HeadOid, after.HeadOid
		op.RerereResolved = rerereResolved(run.text())
		code, hook, detail := r.failure(run)
		_, doneErr := os.Lstat(filepath.Join(w.gitDir, "rebase-merge", "done"))
		switch {
		case after.Kind == "" && run.err == nil:
			op.Outcome = protocol.GitOutcomeCompleted
			res = gitResult(protocol.GitStateSucceeded, "", "Rebased "+cur.branch+" as planned", run.output)
			if after.HeadOid == cur.oid {
				res.Message = "The plan left " + cur.branch + " unchanged"
			}
			r.remove()
		case after.Kind == "":
			r.remove()
			if before.changed(vctx, g) {
				op.Outcome = protocol.GitOutcomeUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "partial_change", "Git failed without starting the rebase but files or the index changed; review status", run.output)
				break
			}
			op.Outcome = protocol.GitOutcomeNotStarted
			res = failedGit("git_failed", "Git did not start the rebase; nothing was changed", run.output)
			if code != "" {
				res.Code, res.Message = code, "Git did not start the rebase ("+failureText(code, hook, detail)+"); nothing was changed"
			} else if wouldOverwrite(&res, run.text(), w.top) {
				res.Message = "Git did not start the rebase: these files would be overwritten; nothing was changed"
			}
		case after.Kind != protocol.GitOperationRebase:
			op.Outcome = protocol.GitOutcomeUnknown
			res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "a different operation is now in progress; review it", run.output)
		case errors.Is(doneErr, os.ErrNotExist):
			// Git refused the todo after the editor (for example a check of
			// its own) and left its state behind without running a step:
			// the start is undone with Git's abort.
			abort := w.runWith(vctx, gitRunOpts{combined: true, cMessages: true}, append(append([]string{}, operationArgs...), "rebase", "--abort")...)
			back, err := w.observe(vctx, g)
			r.remove()
			if abort.err != nil || err != nil || back.Kind != "" || back.HeadOid != cur.oid || back.Branch != cur.branch {
				op.Outcome = protocol.GitOutcomeUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "todo_rejected", "Git rejected the plan and could not be returned to where it started; review the repository", run.output)
				if err == nil && back.Kind != "" {
					copied := back
					op.State = &copied
				}
				break
			}
			op.Outcome, op.HeadAfter, res.Commit = protocol.GitOutcomeNotStarted, back.HeadOid, back.HeadOid
			res = failedGit("todo_rejected", "Git rejected the plan; nothing was changed", run.output)
			if code != "" {
				res.Code = code
			}
		default:
			pre := protocol.GitOperationState{HeadOid: cur.oid}
			operationOutcome(&res, op, pre, after, run, "start the rebase")
			r.afterStop(vctx, g, w, &res, op, code, hook, detail)
			r.fillResultMessage(vctx, g, op.State)
			if op.Outcome == protocol.GitOutcomeStoppedConflicts {
				_ = w.ensureConflictCopy(vctx, p, *op.State, op) // best effort; conflict commands retry
			}
			res.Commit = op.HeadAfter
		}
		return res
	}
	return p, nil
}

// operationStateOf is the operation state a result carries, or nil.
func operationStateOf(res *protocol.GitResult) *protocol.GitOperationState {
	if res == nil || res.Operation == nil {
		return nil
	}
	return res.Operation.State
}

// recordRebaseFailure keeps the commit failure the latest command reported
// for the stop it left (none clears it); a result without an observed
// state (a refused command) leaves the record alone.
func recordRebaseFailure(rec *protocol.GitOperationRecord, res *protocol.GitResult) {
	st := operationStateOf(res)
	if rec.Interactive == nil || st == nil {
		return
	}
	rec.Interactive.Failure = nil
	if pi := st.Interactive; pi != nil && pi.Failure != "" && st.Kind == protocol.GitOperationRebase {
		rec.Interactive.Failure = &protocol.GitRebaseFailure{StopKey: stopKey(*st), Code: pi.Failure, Hook: pi.Hook, Detail: pi.Detail}
	}
}

// recordStopCommits keeps the commits a command made at a stop (at most
// gitRebaseRefsMax, oldest dropped first).
func recordStopCommits(rec *protocol.GitOperationRecord, res *protocol.GitResult) {
	if rec.Interactive == nil || res == nil || res.Operation == nil || len(res.Operation.StopCommits) == 0 {
		return
	}
	list := append(slices.Clone(rec.Interactive.StopCommits), res.Operation.StopCommits...)
	if extra := len(list) - gitRebaseRefsMax; extra > 0 {
		list = list[extra:]
	}
	rec.Interactive.StopCommits = list
}

func failureText(code, hook, detail string) string {
	switch code {
	case protocol.GitRebaseSigningFailed:
		return "a commit could not be signed (commit.gpgSign): " + detail
	case protocol.GitRebaseHookRejected:
		return "the " + hook + " hook refused"
	case protocol.GitRebaseHelperFailed:
		return "the editor helper failed: " + detail
	}
	return detail
}

// ---- Running Git for an interactive rebase ----

// rebaseRun is an application interactive rebase: its record, the
// per-operation directory and the helper state stored there.
type rebaseRun struct {
	rec     protocol.GitOperationRecord
	dir     string
	exe     string
	state   *rebaseState
	missing bool
}

// interactiveRun returns the application interactive rebase st is, nil for
// any other operation.
func (w *gitWriter) interactiveRun(st protocol.GitOperationState) *rebaseRun {
	rec := w.record()
	if rec == nil || rec.Interactive == nil || !gitOperationActive(rec.State) || !recordMatches(*rec, st) || w.rebaseDir == "" {
		return nil
	}
	r := &rebaseRun{rec: *rec, dir: rebaseOpDir(w.rebaseDir, rec.OperationID)}
	exe, err := rebaseHelperExecutable()
	state, serr := readRebaseState(filepath.Join(r.dir, rebaseStateName))
	if err != nil || serr != nil {
		r.missing = true
		return r
	}
	r.exe, r.state = exe, state
	return r
}

func (r *rebaseRun) writeState(id, gitDir string, expected, refs []string, todo string, lines []rebaseLine) error {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = real
	}
	r.state = &rebaseState{Version: rebaseStateVersion, OperationID: id, Token: hex.EncodeToString(token), GitDir: gitDir,
		Expected: expected, ExpectedRefs: refs, Todo: todo, Lines: lines}
	b, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.dir, rebaseStateName), b)
}

func writeFileAtomic(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// remove deletes the per-operation directory once the rebase ended.
func (r *rebaseRun) remove() { _ = os.RemoveAll(r.dir) }

// env points Git's editors at the helper and hands it the state.
func (r *rebaseRun) env() []string {
	helper := shellQuote(r.exe) + " " + rebaseHelperCommand
	return []string{"GIT_SEQUENCE_EDITOR=" + helper + " sequence", "GIT_EDITOR=" + helper + " message",
		rebaseStateEnv + "=" + filepath.Join(r.dir, rebaseStateName), rebaseTokenEnv + "=" + r.state.Token}
}

func (r *rebaseRun) tracePath() string { return filepath.Join(r.dir, "trace2.json") }

// runGit runs one Git command of the rebase with the helpers, a fresh
// helper-error and trace2 file, untranslated messages and the whole output.
func (r *rebaseRun) runGit(ctx context.Context, w *gitWriter, args ...string) gitRunResult {
	_ = os.Remove(filepath.Join(r.dir, rebaseHelperError))
	_ = os.Remove(r.tracePath())
	return w.runWith(ctx, gitRunOpts{combined: true, cMessages: true, env: r.env(), trace2: r.tracePath()}, args...)
}

// runCommit runs `git commit` at a stop with args (after the fixed
// configuration), stdin as the message (when set) and extra environment
// (the original authorship).
func (r *rebaseRun) runCommit(ctx context.Context, w *gitWriter, args []string, stdin string, env []string) gitRunResult {
	_ = os.Remove(filepath.Join(r.dir, rebaseHelperError))
	_ = os.Remove(r.tracePath())
	o := gitRunOpts{combined: true, cMessages: true, env: append(r.env(), env...), trace2: r.tracePath()}
	if stdin != "" {
		if !strings.HasSuffix(stdin, "\n") {
			stdin += "\n"
		}
		o.stdin = strings.NewReader(stdin)
	}
	return w.runWith(ctx, o, append(append([]string{}, rebaseConfigArgs...), args...)...)
}

// failure classifies a failed commit from what the command left: the
// helper's error, a hook that exited non-zero (trace2, any process), or a
// signing failure in Git's output.
func (r *rebaseRun) failure(run gitRunResult) (code, hook, detail string) {
	if b, err := readBounded(filepath.Join(r.dir, rebaseHelperError), 64<<10); err == nil {
		return protocol.GitRebaseHelperFailed, "", boundedDetail(string(b))
	}
	if h := refusingHook(r.tracePath()); h != "" {
		return protocol.GitRebaseHookRejected, h, ""
	}
	if out := run.text(); run.err != nil && signingFailure(out) {
		return protocol.GitRebaseSigningFailed, "", boundedDetail(signingLine(out))
	}
	return "", "", ""
}

func boundedDetail(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "�")
	if len(s) > gitRebaseDetailMax {
		cut := gitRebaseDetailMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// signingFailure recognizes Git's failure to sign a commit (gpg, ssh or
// x509 signing): Git's "failed to write commit object" together with one of
// its own signing error lines, never a word in some other output line.
func signingFailure(out string) bool {
	return strings.Contains(out, "failed to write commit object") && signingLine(out) != ""
}

// signingLine is Git's signing error line in out, "" when there is none.
func signingLine(out string) string {
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
		l := strings.TrimSpace(line)
		// Progress ("Rebasing (1/2)") may precede a message on its line.
		if i := strings.Index(l, "error: "); i > 0 && strings.HasPrefix(l, "Rebasing (") {
			l = l[i:]
		}
		msg, ok := strings.CutPrefix(l, "error: ")
		if !ok {
			msg, ok = strings.CutPrefix(l, "fatal: ")
		}
		if !ok {
			continue
		}
		for _, p := range []string{"gpg failed to sign", "failed to sign the data", "Couldn't load public key", "Couldn't find key in agent", "ssh-keygen -Y sign", "failed to sign with", "gpgsm"} {
			if strings.HasPrefix(msg, p) || strings.Contains(msg, " "+p) {
				return msg
			}
		}
	}
	return ""
}

// refusingHooks are the hooks that can refuse a rebase or one of its
// commits.
var refusingHooks = map[string]bool{"pre-rebase": true, "pre-commit": true, "prepare-commit-msg": true, "commit-msg": true, "pre-merge-commit": true}

// refusingHook reads the trace2 events of a command and every Git process
// it started and returns the first refusing hook that exited non-zero.
func refusingHook(path string) string {
	b, err := readBounded(path, gitRebaseTraceMax)
	if err != nil {
		return ""
	}
	type event struct {
		Event      string `json:"event"`
		SID        string `json:"sid"`
		ChildID    *int   `json:"child_id"`
		ChildClass string `json:"child_class"`
		HookName   string `json:"hook_name"`
		Code       int    `json:"code"`
	}
	hooks := map[string]string{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"child_`)) {
			continue
		}
		var ev event
		if json.Unmarshal(line, &ev) != nil || ev.ChildID == nil {
			continue
		}
		key := ev.SID + "#" + strconv.Itoa(*ev.ChildID)
		switch ev.Event {
		case "child_start":
			if ev.ChildClass == "hook" && refusingHooks[ev.HookName] {
				hooks[key] = ev.HookName
			}
		case "child_exit":
			if name := hooks[key]; name != "" && ev.Code != 0 {
				return name
			}
		}
	}
	return ""
}

// afterStop finishes a start, continue or skip that left the rebase
// stopped: an edit entry in reset mode gets its soft reset, and a commit
// failure is reported with the stop.
func (r *rebaseRun) afterStop(ctx context.Context, g *gitReader, w *gitWriter, res *protocol.GitResult, op *protocol.GitOperationResult, code, hook, detail string) {
	st := op.State
	if st == nil || st.Kind != protocol.GitOperationRebase || st.Interactive == nil {
		return
	}
	pi := st.Interactive
	if st.UnmergedFingerprint == "" && st.StopReason == protocol.GitStopEdit && pi.Amend != "" && pi.Amend == st.HeadOid && r.state != nil &&
		pi.Done >= 1 && pi.Done <= len(r.state.Lines) && r.state.Lines[pi.Done-1].EditMode == protocol.GitRebaseEditReset && !pi.Staged {
		out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", st.HeadOid+"^1")
		parent := strings.TrimSpace(string(out))
		switch {
		case err != nil || !gitFullHash.MatchString(parent):
			res.Message += "; " + shortOid(st.HeadOid) + " has no parent to reset to, so it stays applied for amending"
		default:
			reset := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, "reset", "--soft", parent)
			after, err := w.observe(ctx, g)
			switch {
			case err != nil:
				op.Outcome = protocol.GitOutcomeUnknown
				*res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the rebase stopped to edit "+shortOid(st.HeadOid)+" but the result of resetting it could not be read; refresh", reset.output)
				return
			case reset.err != nil || after.HeadOid != parent:
				res.Message += "; " + shortOid(st.HeadOid) + " could not be reset into the index, so it stays applied for amending"
			default:
				res.Message = "The rebase stopped to edit " + shortOid(st.HeadOid) + "; its changes are staged to commit again or split"
			}
			if err == nil {
				copied := after
				op.State, st = &copied, &copied
				op.HeadAfter, res.Commit = after.HeadOid, after.HeadOid
			}
		}
	}
	if code != "" && st.Interactive != nil && st.UnmergedFingerprint == "" {
		st.Interactive.Failure, st.Interactive.Hook, st.Interactive.Detail = code, hook, detail
		res.Code = code
		res.Message = "The rebase stopped: " + failureText(code, hook, detail) + "; fix it and continue, or abort"
	}
}

// ---- Classification ----

// lineMode is the plan's edit mode of the todo line the rebase stopped at
// ("" when unknown).
func lineMode(st protocol.GitOperationState, rec *protocol.GitOperationRecord) string {
	if st.Interactive == nil || rec == nil || rec.Interactive == nil {
		return ""
	}
	if n := st.Interactive.Done; n >= 1 && n <= len(rec.Interactive.EditModes) {
		return rec.Interactive.EditModes[n-1]
	}
	return ""
}

// interactiveStopKind classifies an application interactive rebase stop from
// the repository and the plan's edit mode (protocol.GitRebaseStop*).
func interactiveStopKind(st protocol.GitOperationState, rec *protocol.GitOperationRecord) string {
	pi := st.Interactive
	if pi == nil {
		return protocol.GitRebaseStopUnknown
	}
	switch {
	case st.UnmergedFingerprint != "":
		return protocol.GitRebaseStopConflict
	case st.StopReason == protocol.GitStopEdit && pi.Amend != "":
		// HEAD still being the commit is an amend stop (amend mode, or a
		// reset that could not be made); after commits at the stop the
		// plan's mode decides.
		if pi.Amend == st.HeadOid || lineMode(st, rec) == protocol.GitRebaseEditAmend {
			return protocol.GitRebaseStopEditAmend
		}
		return protocol.GitRebaseStopEditReset
	case !pi.Staged && pi.CommandOid != "" && pi.Command != "break" && pi.Next == strings.TrimSpace(pi.Command+" "+pi.CommandOid):
		// The last line done is also the next to do: Git rescheduled it.
		return protocol.GitRebaseStopRescheduled
	case factsOf(st, rec).dirty && !pi.Staged && st.HeadOid != factsOf(st, rec).head && headParent(st) == factsOf(st, rec).head:
		// The resolution (or the staged step) was committed at the stop.
		return protocol.GitRebaseStopCommitted
	case factsOf(st, rec).empty && !pi.Staged:
		return protocol.GitRebaseStopEmpty
	case st.StopReason == protocol.GitStopBreak:
		return protocol.GitRebaseStopBreak
	case st.StopReason == protocol.GitStopInteractive && rebaseMessageCommand(pi.Command):
		return protocol.GitRebaseStopMessage
	case st.StopReason == protocol.GitStopExec:
		return protocol.GitRebaseStopUnknown
	case pi.Staged:
		return protocol.GitRebaseStopCommitFailed
	}
	return protocol.GitRebaseStopUnknown
}

// stepFacts are a stop's durable facts: from the snapshot recorded when the
// stop was first observed, or (late) as observed now.
type stepFacts struct {
	committed, empty, late bool
	preHead, head          string
	// dirty: the snapshot was taken with conflicts or staged changes.
	dirty bool
}

func factsOf(st protocol.GitOperationState, rec *protocol.GitOperationRecord) stepFacts {
	pi := st.Interactive
	if pi == nil {
		return stepFacts{late: true, head: st.HeadOid}
	}
	if rec != nil && rec.Interactive != nil {
		if s := rec.Interactive.Snapshot; s != nil && s.Done == pi.Done && s.StepOid == pi.CommandOid && s.Next == pi.Next {
			return stepFacts{committed: s.Committed, empty: s.Empty, late: s.Late, preHead: s.PreHead, head: s.Head, dirty: s.Conflict || s.Staged}
		}
	}
	return stepFacts{committed: pi.StepCommitted, empty: pi.StepEmpty, late: true, preHead: pi.PreHead, head: st.HeadOid}
}

// headParent is HEAD's first parent from the observed ancestry.
func headParent(st protocol.GitOperationState) string {
	if pi := st.Interactive; pi != nil && len(pi.HeadAncestry) > 1 && pi.HeadAncestry[0].Oid == st.HeadOid {
		return pi.HeadAncestry[1].Oid
	}
	return ""
}

// recordStopSnapshot records st as the stop's snapshot when the record has
// none for this stop (late when taken on a read). refresh replaces a
// same-stop snapshot taken with conflicts or staged changes once a
// continue or skip left the index clean (Git moved on within the step).
// It reports whether the record changed.
func recordStopSnapshot(rec *protocol.GitOperationRecord, st *protocol.GitOperationState, late, refresh bool) bool {
	if rec == nil || rec.Interactive == nil || st == nil || st.Kind != protocol.GitOperationRebase || st.Interactive == nil {
		return false
	}
	pi := st.Interactive
	if s := rec.Interactive.Snapshot; s != nil && s.Done == pi.Done && s.StepOid == pi.CommandOid && s.Next == pi.Next {
		if late || !refresh || !(s.Conflict || s.Staged) || st.UnmergedFingerprint != "" || pi.Staged {
			return false
		}
	}
	rec.Interactive.Snapshot = &protocol.GitRebaseStopSnapshot{Done: pi.Done, StepOid: pi.CommandOid, Next: pi.Next, Head: st.HeadOid, PreHead: pi.PreHead,
		Committed: pi.StepCommitted, Empty: pi.StepEmpty, Conflict: st.UnmergedFingerprint != "", Staged: pi.Staged, Late: late}
	return true
}

// observeStepFacts reads, for a pick, reword or edit step stopped without
// conflicts or staged changes, whether Git had made the step's commit: its
// amend marker at HEAD, or HEAD carrying the step commit's authorship
// (name, email, date) while not being an earlier result of this rebase
// (onto, or a commit rewritten-list records). If not, PreHead is HEAD and
// the step became empty when every path its commit changes already has
// that content on HEAD. It also keeps HEAD's recent first-parent history.
func observeStepFacts(ctx context.Context, g *gitReader, gitDir string, st *protocol.GitOperationState) {
	pi := st.Interactive
	if pi == nil || st.Kind != protocol.GitOperationRebase || st.HeadOid == "" {
		return
	}
	pi.HeadAncestry = headAncestry(ctx, g, st.HeadOid)
	pi.PreHead = st.HeadOid
	if st.UnmergedFingerprint != "" || pi.Staged || !gitFullHash.MatchString(pi.CommandOid) {
		return
	}
	switch pi.Command {
	case "pick", "reword", "edit":
	default:
		return
	}
	dir := filepath.Join(gitDir, "rebase-merge")
	var committed bool
	switch {
	case st.HeadOid == pi.CommandOid || (pi.Amend != "" && pi.Amend == st.HeadOid):
		// The step's own commit (fast-forwarded) or Git's amend marker.
		committed = true
	case earlierResult(ctx, g, dir, st.HeadOid):
	default:
		// Git keeps the author on pick, reword and edit, and its commit
		// introduces exactly the step's change on top of the pre-step HEAD.
		headEnv, _, herr := commitAuthor(ctx, g, st.HeadOid)
		stepEnv, _, serr := commitAuthor(ctx, g, pi.CommandOid)
		parent := headParent(*st)
		committed = herr == nil && serr == nil && slices.Equal(headEnv, stepEnv) && parent != "" &&
			changeContained(ctx, g, pi.CommandOid, st.HeadOid) && !changeContained(ctx, g, pi.CommandOid, parent)
	}
	switch {
	case committed:
		pi.StepCommitted, pi.PreHead = true, headParent(*st)
	case changeContained(ctx, g, pi.CommandOid, st.HeadOid):
		pi.StepEmpty = true
		st.Current = &protocol.GitOperationCommit{Oid: pi.CommandOid, Label: shortOid(pi.CommandOid), Subject: commitSubject(ctx, g, pi.CommandOid)}
	}
}

// refineFacts corrects the observed facts of a new stop whose HEAD is a
// commit the server made at an earlier stop (StopCommits, or made by this
// command): that is an earlier result, never the step's commit.
func (r *rebaseRun) refineFacts(ctx context.Context, g *gitReader, st *protocol.GitOperationState, made []protocol.GitOperationCommit) {
	if st == nil || st.Interactive == nil || !st.Interactive.StepCommitted || st.HeadOid == st.Interactive.CommandOid {
		return
	}
	pi := st.Interactive
	if !stopCommitted(&r.rec, st.HeadOid) && !slices.ContainsFunc(made, func(c protocol.GitOperationCommit) bool { return c.Oid == st.HeadOid }) {
		return
	}
	pi.StepCommitted, pi.PreHead = false, st.HeadOid
	if changeContained(ctx, g, pi.CommandOid, st.HeadOid) {
		pi.StepEmpty = true
		st.Current = &protocol.GitOperationCommit{Oid: pi.CommandOid, Label: shortOid(pi.CommandOid), Subject: commitSubject(ctx, g, pi.CommandOid)}
	}
}

// fillStepMessage sets st.Interactive.StepMessage for an application
// interactive rebase stop (GET /v1/git/operation).
func (e *engine) fillStepMessage(ctx context.Context, st *protocol.GitOperationState, top string) {
	if st.Interactive == nil || !st.Interactive.Plan || st.OperationID == "" {
		return
	}
	e.mu.Lock()
	root := e.rebaseDir
	rec := gitOperationFor(&e.snap, top)
	var copied protocol.GitOperationRecord
	if rec != nil {
		copied = *rec
	}
	e.mu.Unlock()
	if root == "" || rec == nil || copied.OperationID != st.OperationID {
		return
	}
	r := &rebaseRun{rec: copied, dir: rebaseOpDir(root, copied.OperationID)}
	state, err := readRebaseState(filepath.Join(r.dir, rebaseStateName))
	if err != nil {
		return
	}
	r.state = state
	g, err := newGitReader(ctx, top)
	if err != nil {
		return
	}
	m := r.prefillMessage(ctx, g, *st)
	if len(m) > protocol.GitRebaseMessageMax {
		cut := protocol.GitRebaseMessageMax
		for cut > 0 && !utf8.RuneStart(m[cut]) {
			cut--
		}
		m, st.Interactive.StepMessageTruncated = m[:cut], true
	}
	st.Interactive.StepMessage = m
}

// applyEditStop presents a server-made edit stop (GitRebaseRecord.EditStop)
// as Git presents its own: the resolution commit as the amend marker, so
// every edit rule applies unchanged.
func applyEditStop(st *protocol.GitOperationState, rec *protocol.GitOperationRecord) {
	pi := st.Interactive
	if pi == nil || rec == nil || rec.Interactive == nil || st.UnmergedFingerprint != "" || pi.Amend != "" {
		return
	}
	if es := rec.Interactive.EditStop; es != nil && es.Done == pi.Done && es.StepOid == pi.CommandOid && es.Next == pi.Next {
		pi.Amend, pi.AmendParent, pi.ServerEdit = es.Commit, es.Parent, true
	}
}

// recordEditStop keeps (or clears) the server-made edit stop a command
// left.
func recordEditStop(rec *protocol.GitOperationRecord, res *protocol.GitResult) {
	st := operationStateOf(res)
	if rec.Interactive == nil || st == nil || st.Interactive == nil {
		return
	}
	pi := st.Interactive
	switch es := rec.Interactive.EditStop; {
	case pi.ServerEdit:
		rec.Interactive.EditStop = &protocol.GitRebaseEditStop{Done: pi.Done, StepOid: pi.CommandOid, Next: pi.Next, Commit: pi.Amend, Parent: pi.AmendParent}
	case es != nil && (es.Done != pi.Done || es.StepOid != pi.CommandOid || es.Next != pi.Next):
		rec.Interactive.EditStop = nil
	}
}

// stopForEdit honours an edit entry whose pick needed resolving: Git would
// commit the resolution on --continue and go on without stopping, so the
// server commits the staged resolution itself (the step commit's author
// and message, byte for byte, or the Continue's Message), does not run
// --continue, soft-resets it in reset mode, and presents the entry's edit
// stop. handled is false when this is not such a stop.
func (r *rebaseRun) stopForEdit(ctx context.Context, g *gitReader, w *gitWriter, st protocol.GitOperationState, req protocol.GitOperationWrite, op *protocol.GitOperationResult) (res protocol.GitResult, handled bool) {
	n, line := r.stepLine(st)
	if !editConflictContinued(st) || line == nil || line.Command != "edit" || line.Oid != st.Interactive.CommandOid {
		return res, false
	}
	args, stdin := []string{"commit", "--reuse-message=" + line.Oid, "--cleanup=verbatim"}, ""
	var env []string
	if req.Message != "" {
		author, _, err := commitAuthor(ctx, g, line.Oid)
		if err != nil {
			op.Outcome = protocol.GitOutcomeUnchanged
			return gitResult(protocol.GitStateFailed, "unavailable", "the commit being edited could not be read; nothing was changed", nil), true
		}
		args, stdin, env = []string{"commit", "--file=-", "--cleanup=verbatim"}, req.Message, author
	}
	commit := r.runCommit(ctx, w, args, stdin, env)
	head, herr := readHead(ctx, g)
	if commit.err != nil || herr != nil || head.oid == st.HeadOid {
		op.Outcome, res = protocol.GitOutcomeUnchanged, failedGit("commit_failed", "the resolution was not committed, so the rebase did not stop for editing", commit.output)
		if code, hook, detail := r.failure(commit); code != "" {
			res.Code, res.Message = code, "the resolution was not committed ("+failureText(code, hook, detail)+")"
		}
		return res, true
	}
	op.StopCommits = []protocol.GitOperationCommit{{Oid: head.oid, Label: shortOid(head.oid), Subject: commitSubject(ctx, g, head.oid)}}
	message := "The rebase stopped to edit " + shortOid(head.oid) + " as planned, with the resolution committed"
	if line.EditMode == protocol.GitRebaseEditReset {
		if reset := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, "reset", "--soft", st.HeadOid); reset.err != nil {
			message += "; it could not be reset into the index, so it stays applied for amending"
		} else {
			message = "The rebase stopped to edit " + shortOid(head.oid) + " as planned; its resolved changes are staged to commit again or split"
		}
	}
	after, err := w.observe(ctx, g)
	if err != nil {
		op.Outcome = protocol.GitOutcomeUnknown
		return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the edit stop could not be read; refresh", commit.output), true
	}
	if pi := after.Interactive; pi != nil && pi.Done == n {
		pi.Amend, pi.AmendParent, pi.ServerEdit = head.oid, st.HeadOid, true
	}
	copied := after
	op.State, op.HeadAfter, op.Outcome = &copied, after.HeadOid, protocol.GitOutcomeStopped
	res = gitResult(protocol.GitStateSucceeded, "stopped", message, commit.output)
	res.Commit = after.HeadOid
	return res, true
}

// fillResultMessage sets the stopped step's message on a command result.
func (r *rebaseRun) fillResultMessage(ctx context.Context, g *gitReader, st *protocol.GitOperationState) {
	if st == nil || st.Interactive == nil || st.Kind != protocol.GitOperationRebase || r.state == nil {
		return
	}
	m := r.prefillMessage(ctx, g, *st)
	if len(m) > protocol.GitRebaseMessageMax {
		cut := protocol.GitRebaseMessageMax
		for cut > 0 && !utf8.RuneStart(m[cut]) {
			cut--
		}
		m, st.Interactive.StepMessageTruncated = m[:cut], true
	}
	st.Interactive.StepMessage = m
}

// editConflictContinued reports a continue at an edit step whose pick had
// conflicts or failed: Git's own --continue would commit the resolution
// without stopping for editing, so stopForEdit makes the stop instead.
func editConflictContinued(st protocol.GitOperationState) bool {
	pi := st.Interactive
	return pi != nil && pi.Command == "edit" && pi.Amend == "" && pi.Staged
}

// prefillMessage is the raw message the stopped step would commit with:
// the plan's message, else the step's default (stepMessage).
func (r *rebaseRun) prefillMessage(ctx context.Context, g *gitReader, st protocol.GitOperationState) string {
	_, line := r.stepLine(st)
	if line == nil {
		return ""
	}
	if line.HasMessage {
		return line.Message
	}
	m, _ := r.stepMessage(ctx, g, st, "")
	return m
}

// earlierResult reports whether oid is a result from before the step: onto,
// an original commit of the branch (an ancestor of orig-head: fast-forwarded
// earlier picks, or the base), or a commit this rebase made for an earlier
// step (rewritten-list).
func earlierResult(ctx context.Context, g *gitReader, dir, oid string) bool {
	if stateOid(dir, "onto") == oid {
		return true
	}
	if orig := stateOid(dir, "orig-head"); orig != "" {
		if in, err := isAncestor(ctx, g, oid, orig); err == nil && in {
			return true
		}
	}
	b, err := readBounded(filepath.Join(dir, "rewritten-list"), rebaseTodoFileMax)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == oid {
			return true
		}
	}
	return false
}

// headAncestry is head's first-parent history, newest first (at most 64).
func headAncestry(ctx context.Context, g *gitReader, head string) []protocol.GitOperationCommit {
	out, _, err := g.read(ctx, 1<<20, "log", "--first-parent", "--no-show-signature", "-n", "64", "--format=%H%x00%s", head, "--")
	if err != nil {
		return nil
	}
	var list []protocol.GitOperationCommit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		oid, subject, ok := strings.Cut(line, "\x00")
		if ok && gitFullHash.MatchString(oid) {
			list = append(list, protocol.GitOperationCommit{Oid: oid, Label: shortOid(oid), Subject: boundedSubject(subject)})
		}
	}
	return list
}

func rebaseMessageCommand(cmd string) bool {
	switch cmd {
	case "reword", "squash", "fixup", "fixup -C", "fixup -c":
		return true
	}
	return false
}

// stopCommitted reports whether oid is a commit made at a stop.
func stopCommitted(rec *protocol.GitOperationRecord, oid string) bool {
	if rec == nil || rec.Interactive == nil {
		return false
	}
	for _, c := range rec.Interactive.StopCommits {
		if c.Oid == oid {
			return true
		}
	}
	return false
}

// committedAtStop reports whether commits were made at this edit stop:
// HEAD is neither the edited commit nor its parent.
func committedAtStop(st protocol.GitOperationState) bool {
	pi := st.Interactive
	return pi != nil && pi.Amend != "" && st.HeadOid != pi.Amend && st.HeadOid != pi.AmendParent
}

// fillAbortDrops lists the work an abort of an application interactive
// rebase would discard: the commits made at stops by the server, and any
// commit on HEAD beyond the stop's snapshot HEAD that the server did not
// make (made in a terminal at the stop, amended, or reset). The abort then
// needs them acknowledged (AcknowledgeDropped, as for a sequence abort).
func fillAbortDrops(st *protocol.GitOperationState, rec *protocol.GitOperationRecord) {
	if rec == nil || rec.Interactive == nil || st.Kind != protocol.GitOperationRebase {
		return
	}
	commits := slices.Clone(rec.Interactive.StopCommits)
	if f := factsOf(*st, rec); st.Interactive != nil && f.head != "" && st.HeadOid != f.head {
		found := false
		var outside []protocol.GitOperationCommit
		for _, c := range st.Interactive.HeadAncestry {
			if c.Oid == f.head {
				found = true
				break
			}
			if !stopCommitted(rec, c.Oid) {
				outside = append(outside, protocol.GitOperationCommit{Oid: c.Oid, Label: c.Label, Subject: c.Subject + " (made outside the application)"})
			}
		}
		if !found {
			// HEAD left the stop's history, or the history is longer than
			// what was walked: HEAD stands for it and the count is a lower
			// bound.
			st.AbortDropsAtLeast = true
			if len(outside) > 1 {
				outside = outside[:1]
			}
		}
		slices.Reverse(outside)
		commits = append(commits, outside...)
	}
	if len(commits) == 0 {
		return
	}
	st.AbortDropsCommits, st.AbortDropsCount, st.AbortDropsIncomplete = commits, len(commits), st.AbortDropsAtLeast
	st.AbortDropsFingerprint = droppedFingerprint(commits, len(commits))
	if st.AbortDropsAtLeast {
		st.AbortDropsFingerprint += "+"
	}
}

// annotateInteractive marks st as an application interactive rebase and
// classifies its stop; st.Source is already app.
func annotateInteractive(st *protocol.GitOperationState, rec *protocol.GitOperationRecord) {
	pi := st.Interactive
	if pi == nil || rec == nil || rec.Interactive == nil || st.Kind != protocol.GitOperationRebase {
		return
	}
	applyEditStop(st, rec)
	pi.Plan, pi.Entry = true, -1
	if n := pi.Done; n >= 1 && n <= len(rec.Interactive.Lines) {
		pi.Entry = rec.Interactive.Lines[n-1]
	}
	f := factsOf(*st, rec)
	pi.StepCommitted, pi.StepEmpty, pi.PreHead, pi.Late = f.committed, f.empty, f.preHead, f.late
	if f.empty && pi.Done >= 1 && gitFullHash.MatchString(pi.CommandOid) && (st.Current == nil || st.Current.Oid != pi.CommandOid) {
		st.Current = &protocol.GitOperationCommit{Oid: pi.CommandOid, Label: shortOid(pi.CommandOid)}
	}
	pi.Stop = interactiveStopKind(*st, rec)
	if f := rec.Interactive.Failure; f != nil && f.StopKey == stopKey(*st) && pi.Stop != protocol.GitRebaseStopConflict {
		pi.Failure, pi.Hook, pi.Detail = f.Code, f.Hook, f.Detail
	} else if pi.Failure == "" {
		pi.Hook, pi.Detail = "", ""
	}
	fillAbortDrops(st, rec)
	st.Can = interactiveActions(*st, rec)
}

// pinnedRefs filters the refs an application plan pinned out of
// ContinueUpdatesRefs: those it was allowed to move.
func pinnedRefs(st protocol.GitOperationState, rec *protocol.GitOperationRecord) protocol.GitOperationState {
	if rec == nil || rec.Interactive == nil || len(st.ContinueUpdatesRefs) == 0 {
		return st
	}
	pinned := map[string]bool{}
	for _, r := range rec.Interactive.UpdateRefs {
		pinned[r.Ref] = true
	}
	st.ContinueUpdatesRefs = slices.DeleteFunc(slices.Clone(st.ContinueUpdatesRefs), func(ref string) bool { return pinned[ref] })
	if len(st.ContinueUpdatesRefs) == 0 {
		st.ContinueUpdatesRefs = nil
	}
	return st
}

// continueRefusal is why Continue is not accepted at this stop of an
// application interactive rebase (nil when it is, as far as the stop
// goes).
func continueRefusal(st protocol.GitOperationState, rec *protocol.GitOperationRecord) error {
	pi := st.Interactive
	kind := interactiveStopKind(st, rec)
	switch {
	case kind == protocol.GitRebaseStopUnknown:
		return failure("not_supported", "the rebase stopped in a way the application does not handle; continue it in a terminal, or abort")
	case pi.Unstaged:
		return failure("unstaged_changes", "tracked files have unstaged changes, which Git refuses to continue with; stage or discard them first")
	case kind == protocol.GitRebaseStopBreak && pi.Staged:
		return failure("staged_changes", "changes are staged at a break; commit them, or unstage them, first")
	case kind == protocol.GitRebaseStopEditAmend && pi.Staged && st.HeadOid != pi.Amend:
		return failure("staged_changes", "commits were made at this stop and changes are still staged; commit them, or unstage them, first")
	case kind == protocol.GitRebaseStopEditReset && !pi.Staged && !committedAtStop(st):
		return failure("nothing_to_recommit", "nothing is staged and nothing was committed at this stop, so continuing would drop the commit; stage its changes to recommit them, or abort")
	}
	f := factsOf(st, rec)
	switch {
	case (kind == protocol.GitRebaseStopEmpty || kind == protocol.GitRebaseStopMessage) && f.late:
		return failure("stop_unobserved", "the server did not observe this stop when Git made it (it was restarted, or Git ran elsewhere), so it cannot tell whether the commit was already made; continue in a terminal, or abort")
	case kind == protocol.GitRebaseStopMessage && pi.Command == "reword" && !pi.Staged &&
		!(f.committed && (st.HeadOid == f.head || headParent(st) == f.preHead)):
		return failure("not_supported", "HEAD is not the commit being reworded any more; continue in a terminal, or abort")
	}
	return nil
}

// interactiveActions is operationActions for an application interactive
// rebase: Continue is available at edit, break and message stops too.
func interactiveActions(st protocol.GitOperationState, rec *protocol.GitOperationRecord) protocol.GitOperationActions {
	no := func(reason string) protocol.GitOperationAction { return protocol.GitOperationAction{Reason: reason} }
	tmp := pinnedRefs(st, rec)
	tmp.StopReason = ""
	a := operationActions(tmp)
	kind := interactiveStopKind(st, rec)
	switch f := factsOf(st, rec); {
	case kind == protocol.GitRebaseStopConflict, kind == protocol.GitRebaseStopCommitFailed:
	case kind == protocol.GitRebaseStopEmpty && f.late:
		a.Skip = no("the server did not observe this stop when Git made it; skip it in a terminal, or abort")
	case kind == protocol.GitRebaseStopEmpty && st.HeadOid != f.preHead:
		a.Skip = no("the empty commit was already kept at this stop; continue")
	case kind == protocol.GitRebaseStopEmpty:
	default:
		a.Skip = no("the rebase is stopped for " + strings.ReplaceAll(kind, "_", " ") + ", not at a commit it could not apply")
	}
	if a.Continue.Allowed {
		if err := continueRefusal(st, rec); err != nil {
			a.Continue = no(err.(*protocol.Error).Message)
		}
	}
	return a
}

// ---- Continue, skip and abort ----

// stepLine is the pinned todo line the rebase stopped at.
func (r *rebaseRun) stepLine(st protocol.GitOperationState) (int, *rebaseLine) {
	if st.Interactive == nil || r.state == nil {
		return 0, nil
	}
	n := st.Interactive.Done
	if n < 1 || n > len(r.state.Lines) {
		return n, nil
	}
	return n, &r.state.Lines[n-1]
}

// chainMessage returns the plan's message of the squash/fixup chain whose
// member is line n (1-based), when the chain carries one.
func (r *rebaseRun) chainMessage(n int) (string, bool) {
	j := n - 1
	for j+1 < len(r.state.Lines) && strings.HasPrefix(r.state.Lines[j+1].Command, "squash") || j+1 < len(r.state.Lines) && strings.HasPrefix(r.state.Lines[j+1].Command, "fixup") {
		j++
	}
	return r.state.Lines[j].Message, r.state.Lines[j].HasMessage
}

// checkContinue refuses a continue the rebase cannot take at this stop.
func (r *rebaseRun) checkContinue(st protocol.GitOperationState, req protocol.GitOperationWrite) error {
	if r.missing {
		return failure("plan_missing", "the stored plan of this rebase is missing from the application home, so its messages cannot be supplied; continue it in a terminal, or abort")
	}
	if err := continueRefusal(st, &r.rec); err != nil {
		return err
	}
	if req.Message == "" {
		return nil
	}
	pi := st.Interactive
	kind := interactiveStopKind(st, &r.rec)
	_, line := r.stepLine(st)
	commits := false
	switch kind {
	case protocol.GitRebaseStopEditAmend:
		commits = pi.Staged && st.HeadOid == pi.Amend
	case protocol.GitRebaseStopEditReset:
		commits = pi.Staged
	case protocol.GitRebaseStopMessage, protocol.GitRebaseStopCommitFailed, protocol.GitRebaseStopConflict:
		commits = true
	}
	applies := line != nil && (line.Command == "pick" || line.Command == "reword" || line.Command == "edit" || line.HasMessage)
	if !commits || !applies {
		return failure("invalid", "a message does not apply at this stop: nothing is committed here with its own message (a break, an empty commit, a commit inside a squash or fixup chain, or nothing staged)")
	}
	return nil
}

// commitAuthor returns the environment that keeps a commit's authorship
// and its message (as log prints it).
func commitAuthor(ctx context.Context, g *gitReader, oid string) (env []string, message string, err error) {
	out, _, err := g.read(ctx, gitRebaseMessageRead, "log", "-1", "--no-show-signature", "--date=raw", "--format=%an%x00%ae%x00%ad%x00%B", oid, "--")
	f := strings.SplitN(string(out), "\x00", 4)
	if err != nil || len(f) != 4 {
		return nil, "", failure("unavailable", "the original commit could not be read")
	}
	return []string{"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2]}, strings.TrimRight(f[3], "\n"), nil
}

// changeContained reports whether every path commit changes (against its
// first parent, or all its files for a root commit; gitlinks and mode
// changes included) already has the commit's content on head, so picking
// it would change nothing.
func changeContained(ctx context.Context, g *gitReader, commit, head string) bool {
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, "diff-tree", "-r", "-z", "--raw", "--root", "--no-renames", "--no-abbrev", "--no-commit-id", commit, "--")
	if err != nil || truncated {
		return false
	}
	changes := parseRaw(out)
	if len(changes) == 0 {
		return false
	}
	have := map[string]string{}
	for i := 0; i < len(changes); i += 500 {
		batch := changes[i:min(i+500, len(changes))]
		args := []string{"ls-tree", "-z", "--full-tree", head, "--"}
		for _, c := range batch {
			args = append(args, c.path)
		}
		out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
		if err != nil || truncated {
			return false
		}
		for _, rec := range strings.Split(string(out), "\x00") {
			meta, p, ok := strings.Cut(rec, "\t")
			if f := strings.Fields(meta); ok && len(f) == 3 {
				have[p] = f[0] + " " + f[2]
			}
		}
	}
	for _, c := range changes {
		if absentSide(c.new) {
			if _, present := have[c.path]; present {
				return false
			}
		} else if have[c.path] != c.new {
			return false
		}
	}
	return true
}

// commitMessage is a commit's message exactly as stored.
func commitMessage(ctx context.Context, g *gitReader, oid string) (string, error) {
	out, truncated, err := g.read(ctx, gitRebaseMessageRead, "cat-file", "commit", oid)
	_, body, ok := strings.Cut(string(out), "\n\n")
	if err != nil || truncated || !ok {
		return "", failure("unavailable", "the message of "+shortOid(oid)+" could not be read")
	}
	return body, nil
}

const gitRebaseMessageRead = protocol.GitRebaseMessageMax + 64<<10

// preContinue makes the commit Git itself would not make (or not make
// faithfully) at this stop before `rebase --continue`: the rest of a commit
// reset for editing, the amend of an amend stop (message kept byte for
// byte), a kept empty commit, or the new message of a reword whose message
// step failed after its commit was made. Commits already made at this stop
// (a retry after a failed continue) are not made again. made lists what
// was committed; consumed reports that req.Message was used; failed stops
// the continue with that result.
func (r *rebaseRun) preContinue(ctx context.Context, g *gitReader, w *gitWriter, st protocol.GitOperationState, req protocol.GitOperationWrite) (made []protocol.GitOperationCommit, consumed bool, failed *protocol.GitResult, run *gitRunResult) {
	pi := st.Interactive
	if pi == nil {
		return nil, false, nil, nil
	}
	fail := func(code, message string) *protocol.GitResult {
		res := gitResult(protocol.GitStateFailed, code, message+"; nothing was changed", nil)
		return &res
	}
	kind := interactiveStopKind(st, &r.rec)
	var args []string
	stdin := ""
	var env []string
	switch {
	case kind == protocol.GitRebaseStopEditReset && pi.Staged:
		if req.Message == "" {
			args = []string{"commit", "--reuse-message=" + pi.Amend, "--cleanup=verbatim"}
			break
		}
		author, _, err := commitAuthor(ctx, g, pi.Amend)
		if err != nil {
			return nil, false, fail("unavailable", "the commit being edited could not be read"), nil
		}
		args, stdin, env = []string{"commit", "--file=-", "--cleanup=verbatim"}, req.Message, author
	case kind == protocol.GitRebaseStopEditAmend && pi.Staged && st.HeadOid == pi.Amend:
		args = []string{"commit", "--amend", "--no-edit", "--cleanup=verbatim"}
		if req.Message != "" {
			args, stdin = []string{"commit", "--amend", "--file=-", "--cleanup=verbatim"}, req.Message
		}
	case kind == protocol.GitRebaseStopEmpty && st.HeadOid == factsOf(st, &r.rec).preHead && gitFullHash.MatchString(pi.CommandOid):
		// Kept only while nothing was committed at the stop yet (a retry
		// after a crash, or a commit made in a terminal, is not repeated).
		args = []string{"commit", "--allow-empty", "--reuse-message=" + pi.CommandOid, "--cleanup=verbatim"}
		if _, line := r.stepLine(st); line != nil && line.HasMessage {
			// A reword that became empty keeps its planned message.
			author, _, err := commitAuthor(ctx, g, pi.CommandOid)
			if err != nil {
				return nil, false, fail("unavailable", "the commit being kept could not be read"), nil
			}
			args, stdin, env = []string{"commit", "--allow-empty", "--file=-", "--cleanup=verbatim"}, line.Message, author
		}
	case pi.Command == "reword" && !pi.Staged && kind == protocol.GitRebaseStopMessage:
		// Git made the commit (recorded in the stop's snapshot) before its
		// message step failed; the planned message is applied by amending
		// it, unless HEAD was amended since (by an earlier attempt, or in a
		// terminal), which counts as done.
		f := factsOf(st, &r.rec)
		switch {
		case f.late || !f.committed:
			return nil, false, fail("stop_unobserved", "the reworded commit's state is not known; continue in a terminal, or abort"), nil
		case st.HeadOid != f.head && headParent(st) == f.preHead:
			return nil, false, nil, nil
		case st.HeadOid != f.head:
			return nil, false, fail("not_supported", "HEAD is not the commit being reworded any more; continue in a terminal, or abort"), nil
		}
		message := req.Message
		if _, line := r.stepLine(st); message == "" && line != nil {
			message = line.Message
		}
		if message == "" {
			return nil, false, fail("message_required", "no message is stored for this reword; continue with a message"), nil
		}
		args, stdin = []string{"commit", "--amend", "--file=-", "--cleanup=verbatim"}, message
	case pi.Staged && req.Message == "" && (kind == protocol.GitRebaseStopCommitFailed || kind == protocol.GitRebaseStopConflict) && r.blankStepMessage(ctx, g, st):
		// Git's editor would get an empty message, which the helper
		// refuses; the commit keeps its (empty) message instead.
		_, line := r.stepLine(st)
		args = []string{"commit", "--allow-empty-message", "--reuse-message=" + line.Oid, "--cleanup=verbatim"}
	default:
		return nil, false, nil, nil
	}
	commit := r.runCommit(ctx, w, args, stdin, env)
	if commit.err == nil {
		if head, err := readHead(ctx, g); err == nil && head.oid != st.HeadOid {
			made = []protocol.GitOperationCommit{{Oid: head.oid, Label: shortOid(head.oid), Subject: commitSubject(ctx, g, head.oid)}}
		}
	}
	return made, req.Message != "" && stdin == req.Message, nil, &commit
}

// blankStepMessage reports a pick or edit step without a planned message
// whose commit has an empty (or blank) message.
func (r *rebaseRun) blankStepMessage(ctx context.Context, g *gitReader, st protocol.GitOperationState) bool {
	_, line := r.stepLine(st)
	if line == nil || line.HasMessage || (line.Command != "pick" && line.Command != "edit") || !gitFullHash.MatchString(line.Oid) {
		return false
	}
	m, err := commitMessage(ctx, g, line.Oid)
	return err == nil && strings.TrimSpace(m) == ""
}

// stepMessage is the message Git's editor gets for the stop's step when a
// continue or skip commits it: the Continue's Message, else the plan's
// (the helper reads it), else the message the step would have had without
// the stop: a squash or fixup chain's stored message, the accumulated
// message (HEAD's) for a plain fixup, and otherwise the step commit's own
// message, exactly as stored.
func (r *rebaseRun) stepMessage(ctx context.Context, g *gitReader, st protocol.GitOperationState, message string) (string, error) {
	n, line := r.stepLine(st)
	switch {
	case line == nil:
		return "", nil
	case message != "":
		return message, nil
	case line.HasMessage:
		return "", nil
	}
	if strings.HasPrefix(line.Command, "squash") || strings.HasPrefix(line.Command, "fixup") {
		if m, ok := r.chainMessage(n); ok {
			return m, nil
		}
	}
	switch {
	case line.Command == "fixup":
		return commitMessage(ctx, g, st.HeadOid)
	case gitFullHash.MatchString(line.Oid):
		return commitMessage(ctx, g, line.Oid)
	}
	return "", nil
}

// writeOverride stores the message of the step Git stopped at for the
// helper (none clears it).
func (r *rebaseRun) writeOverride(st protocol.GitOperationState, message string) error {
	_ = os.Remove(filepath.Join(r.dir, rebaseOverrideName))
	n, line := r.stepLine(st)
	if message == "" || line == nil {
		return nil
	}
	b, err := json.Marshal(rebaseOverride{Step: n, Oid: line.Oid, Message: message})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.dir, rebaseOverrideName), b)
}

// continueArgs are the arguments of `git rebase --continue|--skip`.
func continueArgs(verb string) []string {
	return append(append(append([]string{}, operationArgs...), rebaseConfigArgs...), "rebase", "--"+verb)
}

// backupRef is where an abort keeps the rebase's HEAD.
func (r *rebaseRun) backupRef() string {
	return "refs/tui-go/rebase-backup/" + filepath.Base(r.dir)
}

// keepListedCommits gives every commit an abort lists (AbortDropsCommits)
// that the rebase's HEAD does not reach a ref of its own,
// <backupRef>-<n>, so the abort loses none of them.
func (r *rebaseRun) keepListedCommits(ctx context.Context, g *gitReader, w *gitWriter, st protocol.GitOperationState) ([]string, error) {
	var refs []string
	for _, c := range st.AbortDropsCommits {
		if !gitFullHash.MatchString(c.Oid) {
			continue
		}
		if in, err := isAncestor(ctx, g, c.Oid, st.HeadOid); err == nil && in {
			continue
		}
		ref := fmt.Sprintf("%s-%d", r.backupRef(), len(refs)+1)
		if keep := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, "update-ref", "-m", "tui-go: interactive rebase aborted", ref, c.Oid); keep.err != nil {
			return refs, fmt.Errorf("commit %s could not be kept at %s", shortOid(c.Oid), ref)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// checkAbortRefs reports branches an abort left elsewhere than they were
// before the rebase started (Git's abort never moves them; this checks).
func (r *rebaseRun) checkAbortRefs(ctx context.Context, g *gitReader) []string {
	want := append([]protocol.GitRebaseUpdateRef{{Ref: "refs/heads/" + r.rec.Branch, Oid: r.rec.OrigHead}}, r.rec.Interactive.UpdateRefs...)
	var moved []string
	for _, ref := range want {
		if oid, err := refOid(ctx, g, ref.Ref); err != nil || oid != ref.Oid {
			moved = append(moved, refShortName(ref.Ref))
		}
	}
	return moved
}

// ---- Commit at a stop ----

// prepareOperationCommit prepares git.operation_commit: commit what is
// staged while an application interactive rebase is stopped for editing or
// at a break, staying stopped.
func prepareOperationCommit(ctx context.Context, g *gitReader, w *gitWriter, c protocol.Command) (*gitPlan, error) {
	req := *c.Git.Operation
	st, _, err := observeOperationLists(ctx, g, w.gitDir)
	if err != nil {
		return nil, err
	}
	r := w.interactiveRun(st)
	if r != nil {
		applyEditStop(&st, &r.rec)
	}
	switch {
	case st.Kind == "":
		return nil, failure("no_operation", "no rebase is in progress")
	case st.Kind != protocol.GitOperationRebase || r == nil:
		return nil, failure("not_supported", "commits are made here only while an application interactive rebase is stopped for editing or at a break")
	case r.missing:
		return nil, failure("plan_missing", "the stored plan of this rebase is missing from the application home; continue it in a terminal, or abort")
	case st.HeadOid != req.ExpectedHead:
		return nil, failure("stale_head", "HEAD moved since the operation was read; refresh and review again")
	case st.Step != req.ExpectedStep:
		return nil, failure("stale_operation", "the rebase moved on since it was read; refresh")
	}
	kind := interactiveStopKind(st, &r.rec)
	switch {
	case kind != protocol.GitRebaseStopEditAmend && kind != protocol.GitRebaseStopEditReset && kind != protocol.GitRebaseStopBreak:
		return nil, failure("not_supported", "commits are made only while the rebase is stopped for editing or at a break")
	case strings.HasPrefix(st.StagedFingerprint, gitTruncatedFingerprint):
		return nil, failure("status_truncated", "too many staged changes to review here; commit in a terminal")
	case req.StagedFingerprint != st.StagedFingerprint:
		return nil, failure("stale_status", "the staged changes differ from what was reviewed; refresh and review what will be committed")
	case !st.Interactive.Staged:
		return nil, failure("nothing_staged", "stage changes before committing")
	case len(st.HiddenEntries) > 0:
		return nil, failure("not_supported", "entries marked assume-unchanged or skip-worktree may hide local changes ("+listPaths(st.HiddenEntries)+"); use a terminal")
	}
	var author []string
	if kind == protocol.GitRebaseStopEditReset {
		if author, _, err = commitAuthor(ctx, g, st.Interactive.Amend); err != nil {
			return nil, err
		}
	}
	if err := checkIdentity(ctx, w); err != nil {
		return nil, err
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	p := &gitPlan{journal: operationJournal(c, w.top, st)}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		op := &protocol.GitOperationResult{Kind: st.Kind, HeadBefore: st.HeadOid}
		defer func() { res.Operation = op }()
		again, err := w.observe(ctx, g)
		switch {
		case err != nil:
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "unavailable", "the operation could not be read; nothing was changed", nil)
			return res
		case !sameStop(st, again) || again.StagedFingerprint != st.StagedFingerprint:
			op.Outcome, res = protocol.GitOutcomeUnchanged, gitResult(protocol.GitStateFailed, "stale_status", "the rebase or the staged changes changed after review; nothing was changed", nil)
			return res
		}
		run := r.runCommit(ctx, w, []string{"commit", "--file=-", "--cleanup=whitespace"}, req.Message, author)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, err := w.observe(vctx, g)
		if err != nil {
			op.Outcome, res = protocol.GitOutcomeUnknown, gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "the result could not be read; refresh", run.output)
			return res
		}
		copied := after
		op.State, op.HeadAfter, res.Commit = &copied, after.HeadOid, after.HeadOid
		switch {
		case after.HeadOid != st.HeadOid && run.err == nil:
			op.Outcome = protocol.GitOutcomeStopped
			op.StopCommits = []protocol.GitOperationCommit{{Oid: after.HeadOid, Label: shortOid(after.HeadOid), Subject: commitSubject(vctx, g, after.HeadOid)}}
			res = gitResult(protocol.GitStateSucceeded, "", "Committed "+shortOid(after.HeadOid)+"; the rebase is still stopped", run.output)
		case after.HeadOid != st.HeadOid:
			op.Outcome = protocol.GitOutcomeUnknown
			op.StopCommits = []protocol.GitOperationCommit{{Oid: after.HeadOid, Label: shortOid(after.HeadOid), Subject: commitSubject(vctx, g, after.HeadOid)}}
			res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported a failure after HEAD moved; review the new commit", run.output)
		default:
			op.Outcome = protocol.GitOutcomeUnchanged
			res = failedGit("commit_failed", "the commit was not created", run.output)
			if code, hook, detail := r.failure(run); code != "" && copied.Interactive != nil {
				copied.Interactive.Failure, copied.Interactive.Hook, copied.Interactive.Detail = code, hook, detail
				res.Code, res.Message = code, "the commit was not created: "+failureText(code, hook, detail)
			}
		}
		res.Commit = after.HeadOid
		return res
	}
	return p, nil
}

// ---- Storage ----

// sweepRebasePlans removes per-operation directories that no active
// interactive rebase record uses any more: at once for a record that has
// ended (here or outside the application), after a minute for unknown ones
// (a start that is just writing its plan has an active record already).
func (e *engine) sweepRebasePlans() {
	e.mu.Lock()
	root := e.rebaseDir
	keep, ended := map[string]bool{}, map[string]bool{}
	for _, rec := range e.snap.GitOperations {
		if rec.Interactive == nil {
			continue
		}
		name := filepath.Base(rebaseOpDir(root, rec.OperationID))
		if gitOperationActive(rec.State) {
			keep[name] = true
		} else {
			ended[name] = true
		}
	}
	e.mu.Unlock()
	if root == "" {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, de := range entries {
		if keep[de.Name()] {
			continue
		}
		if info, err := de.Info(); err == nil && (ended[de.Name()] || time.Since(info.ModTime()) > time.Minute) {
			_ = os.RemoveAll(filepath.Join(root, de.Name()))
		}
	}
}

// ---- Validation and observation ----

// validateRebaseInteractive checks git.rebase with an interactive plan
// before anything is read.
func validateRebaseInteractive(kind string, i *protocol.GitIntegrate) error {
	ir := i.Interactive
	switch {
	case kind != protocol.GitKindRebase:
		return failure("invalid", "only git.rebase takes an interactive plan")
	case i.Source != "" || i.TargetRef != "" || i.TargetOid != "" || i.ExpectedReplayCount != 0:
		return failure("invalid", "an interactive rebase names its base and onto in the plan, not a target")
	case (i.ExpectedBranch != "" && !plausibleBranchName(i.ExpectedBranch)) || !gitFullHash.MatchString(i.ExpectedHead):
		return failure("invalid", "git.rebase needs the branch and head shown in status")
	case ir.Fingerprint == "" || len(ir.Fingerprint) > 128:
		return failure("invalid", "fingerprint from the plan read is required")
	}
	if err := validRebaseRevisions(ir.Base, ir.Onto); err != nil {
		return err
	}
	if perr := protocol.ValidateRebaseEntryShapes(ir.Entries); perr != nil {
		return perr
	}
	return nil
}

// validateOperationCommit checks git.operation_commit's shape.
func validateOperationCommit(o *protocol.GitOperationWrite) error {
	switch {
	case o.Kind != protocol.GitOperationRebase:
		return failure("invalid", "commits are made only during a rebase")
	case !o.Confirmed:
		return failure("confirmation_required", "confirm this commit first")
	case !gitFullHash.MatchString(o.ExpectedHead) || o.ExpectedStep < 0:
		return failure("invalid", "git.operation_commit needs expected_head (and expected_step) from the operation state")
	case o.StagedFingerprint == "":
		return failure("invalid", "staged_fingerprint from the operation state is required")
	case o.WorktreeFingerprint != "" || o.UnmergedFingerprint != "" || o.SkipOid != "" || len(o.AcknowledgeDiscard) != 0 || len(o.AcknowledgeMarkers) != 0 ||
		o.DiscardsFingerprint != "" || o.MarkersFingerprint != "" || o.AcknowledgeDropped != "" || o.AcknowledgeBackupMissing != "" ||
		o.AcknowledgeMarkersIncomplete || o.AcknowledgeBackupIncomplete || o.AcknowledgeAgentChanges != "":
		return failure("invalid", "git.operation_commit pins the staged changes and carries a message only")
	case strings.TrimSpace(o.Message) == "":
		return failure("empty_message", "commit message is empty")
	case !protocol.ValidRebaseMessage(o.Message):
		return failure("invalid", "commit message must be UTF-8 text up to 64 KiB")
	}
	return nil
}

// rebaseProgress reads a merge-backend rebase's todo progress from its
// state directory (Staged and Unstaged are set by observeChanges).
func rebaseProgress(dir string) *protocol.GitRebaseProgress {
	pi := &protocol.GitRebaseProgress{Entry: -1}
	read := func(name string) string {
		b, err := readBounded(filepath.Join(dir, name), rebaseTodoFileMax)
		if err != nil {
			return ""
		}
		return string(b)
	}
	for _, line := range strings.Split(read("done"), "\n") {
		if cmd, arg, ok := todoCommand(line); ok {
			pi.Done++
			pi.Command, pi.CommandOid = cmd, ""
			if gitFullHash.MatchString(arg) {
				pi.CommandOid = arg
			}
		}
	}
	for _, line := range strings.Split(read("git-rebase-todo"), "\n") {
		if cmd, arg, ok := todoCommand(line); ok {
			if pi.Remaining == 0 {
				pi.Next = strings.TrimSpace(cmd + " " + arg)
			}
			pi.Remaining++
		}
	}
	pi.Amend = stateOid(dir, "amend")
	// update-refs holds three lines per branch: the ref, the commit it
	// pointed at, and the commit it will point at (or null).
	lines := strings.Split(strings.TrimSpace(read("update-refs")), "\n")
	for i := 0; i+2 < len(lines) && len(pi.UpdateRefs) < gitRebaseRefsMax; i += 3 {
		if validFullRef(lines[i]) && gitFullHash.MatchString(lines[i+1]) {
			pi.UpdateRefs = append(pi.UpdateRefs, protocol.GitRebaseUpdateRef{Ref: lines[i], Oid: lines[i+1]})
		}
	}
	return pi
}

// checkRebaseWrittenPaths is checkWrittenPaths for an interactive rebase:
// the move from head to onto (a tree for a new root) and every path a
// commit of the range (rangeArgs, a rev-list range) changes.
func checkRebaseWrittenPaths(ctx context.Context, g *gitReader, head, onto string, rangeArgs []string) error {
	return checkWrittenPathsRange(ctx, g, head, onto, append([]string{"--root"}, rangeArgs...))
}
