package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Ref actions (ADR 0021): git.branch_create, git.switch and git.reset_soft.
// They reuse the two-phase journaled flow of git_write.go. Every request pins
// what the user saw (the checked-out branch, HEAD, the target's tip and, for
// a switch, the whole working tree), prepare revalidates those pins with the
// read policy, and nothing is recorded when a pin is stale.
//
// Switch follows Git's own carry semantics with explicit confirmation: local
// changes that Git can carry are carried, a change Git would overwrite stops
// the switch before anything moves (would_overwrite), and ignored files are
// never overwritten (--no-overwrite-ignore). It never uses --merge,
// --discard-changes, --force, stash or autostash, and never recurses into
// submodules.

// gitKind is the per-kind run policy.
type gitKind struct {
	// lease: hold the checkout writer lease (refused while a thread holds
	// it). Every kind holds the repository's Git slot.
	lease bool
	// cancellable: git.cancel is accepted when the command starts.
	cancellable bool
	// network: a remote is contacted; a stall or the budget is a timeout.
	network bool
	budget  time.Duration
}

const (
	gitRefBudget     = 5 * time.Minute
	gitNetworkBudget = 15 * time.Minute
)

func gitKindPolicy(kind string) gitKind {
	switch kind {
	case protocol.GitKindBranchCreate:
		return gitKind{budget: gitWriteBudget}
	case protocol.GitKindSwitch, protocol.GitKindResetSoft:
		return gitKind{lease: true, budget: gitRefBudget}
	case protocol.GitKindFetch, protocol.GitKindPush:
		return gitKind{cancellable: true, network: true, budget: gitNetworkBudget}
	case protocol.GitKindPull:
		return gitKind{lease: true, cancellable: true, network: true, budget: gitNetworkBudget}
	}
	return gitKind{lease: true, budget: gitWriteBudget}
}

func gitRefOrSyncKind(kind string) bool {
	switch kind {
	case protocol.GitKindBranchCreate, protocol.GitKindSwitch, protocol.GitKindResetSoft,
		protocol.GitKindFetch, protocol.GitKindPull, protocol.GitKindPush:
		return true
	}
	return false
}

// validGitHead accepts a full hash, or "unborn" when allowUnborn.
func validGitHead(h string, allowUnborn bool) bool {
	return gitFullHash.MatchString(h) || (allowUnborn && h == protocol.GitUnbornHead)
}

// plausibleBranchName is the syntactic gate before check-ref-format: no
// option-like or @{...} names (check-ref-format --branch would expand
// @{-1}), no control characters, bounded length.
func plausibleBranchName(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || strings.HasPrefix(name, "-") || strings.Contains(name, "@{") || name == "@" || name == "HEAD" {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validateGitRefSync checks a ref or sync command's shape.
func validateGitRefSync(kind string, w *protocol.GitWrite) error {
	if len(w.Paths) != 0 || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished || w.Confirmed || w.Cancel != nil {
		return failure("invalid", "only the ref or sync payload is accepted for "+kind)
	}
	switch kind {
	case protocol.GitKindBranchCreate, protocol.GitKindSwitch, protocol.GitKindResetSoft:
		r := w.Ref
		if r == nil || w.Sync != nil {
			return failure("invalid", kind+" carries its payload in Git.Ref")
		}
		if r.AcknowledgeCarry < 0 || r.AcknowledgeLeaveCommits < 0 || (r.ExpectedBranch != "" && !plausibleBranchName(r.ExpectedBranch)) {
			return failure("invalid", "invalid expected branch or carry count")
		}
		switch kind {
		case protocol.GitKindBranchCreate:
			if !plausibleBranchName(r.Name) || !gitFullHash.MatchString(r.StartOid) {
				return failure("invalid", "branch creation needs a valid name and a full start commit hash")
			}
			if r.Branch != "" || r.TargetOid != "" || r.ExpectedBranch != "" || r.ExpectedHead != "" || r.WorktreeFingerprint != "" || r.AcknowledgeCarry != 0 || r.AcknowledgeLeaveCommits != 0 || r.AcknowledgePublished || r.AcknowledgeNotAncestor {
				return failure("invalid", "branch creation takes only name and start_oid")
			}
		case protocol.GitKindSwitch:
			switch {
			case r.Branch != "" && r.Name == "":
				if !plausibleBranchName(r.Branch) || !gitFullHash.MatchString(r.TargetOid) || r.StartOid != "" {
					return failure("invalid", "switch needs the branch and its tip (target_oid) as shown")
				}
			case r.Name != "" && r.Branch == "":
				if !plausibleBranchName(r.Name) || !gitFullHash.MatchString(r.StartOid) || r.TargetOid != "" {
					return failure("invalid", "switch-create needs a valid name and a full start commit hash")
				}
			default:
				return failure("invalid", "switch takes either an existing branch or a new name")
			}
			if !validGitHead(r.ExpectedHead, true) || r.WorktreeFingerprint == "" {
				return failure("invalid", "switch needs expected_head and worktree_fingerprint from status")
			}
			if r.AcknowledgePublished || r.AcknowledgeNotAncestor {
				return failure("invalid", "reset acknowledgements do not apply to switch")
			}
		case protocol.GitKindResetSoft:
			if !gitFullHash.MatchString(r.TargetOid) || !validGitHead(r.ExpectedHead, true) {
				return failure("invalid", "soft reset needs a full target hash and expected_head")
			}
			if r.Name != "" || r.Branch != "" || r.StartOid != "" || r.WorktreeFingerprint != "" || r.AcknowledgeCarry != 0 || r.AcknowledgeLeaveCommits != 0 {
				return failure("invalid", "soft reset takes target_oid, the expected branch and head, and acknowledgements")
			}
		}
	default:
		s := w.Sync
		if s == nil || w.Ref != nil {
			return failure("invalid", kind+" carries its payload in Git.Sync")
		}
		switch kind {
		case protocol.GitKindFetch:
			if s.Remote != "" && !validRemoteName(s.Remote) {
				return failure("not_supported", "this remote name cannot be used here; fetch from a terminal")
			}
			if s.Upstream != "" || s.ExpectedBranch != "" || s.ExpectedHead != "" || s.ExpectedUpstreamOid != "" {
				return failure("invalid", "fetch takes only a remote")
			}
			if s.All && s.Remote != "" {
				return failure("invalid", "fetch takes either a remote or all remotes")
			}
			if s.Prune && !s.All {
				return failure("invalid", "prune applies to fetching all remotes")
			}
		default:
			if s.Remote != "" || s.All || s.Prune {
				return failure("invalid", "pull and push use the branch's configured upstream; remote is not accepted")
			}
			if s.Upstream == "" || !plausibleBranchName(s.ExpectedBranch) || !gitFullHash.MatchString(s.ExpectedHead) {
				return failure("invalid", kind+" needs the upstream, branch and head shown in status")
			}
			if s.ExpectedUpstreamOid != "" && (kind != protocol.GitKindPush || !gitFullHash.MatchString(s.ExpectedUpstreamOid)) {
				return failure("invalid", "expected_upstream_oid applies to push and must be a full hash")
			}
		}
	}
	return nil
}

// gitHead is the checked-out branch ("" when detached) and HEAD commit (""
// when unborn).
type gitHead struct{ branch, oid string }

func gitExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func readHead(ctx context.Context, g *gitReader) (gitHead, error) {
	var h gitHead
	out, _, err := g.read(ctx, 4096, "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		h.branch = strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/")
	case gitExitCode(err) != 1:
		return h, failure("unavailable", "HEAD could not be read")
	}
	out, _, err = g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	switch {
	case err == nil:
		h.oid = strings.TrimSpace(string(out))
	case gitExitCode(err) != 1:
		return h, failure("unavailable", "HEAD could not be read")
	}
	return h, nil
}

// checkHeadPins compares the current branch and HEAD with what was shown.
func checkHeadPins(cur gitHead, branch, head string) error {
	if cur.branch != branch {
		return failure("stale_branch", "the checked-out branch changed since status was read; refresh and review again")
	}
	got := cur.oid
	if got == "" {
		got = protocol.GitUnbornHead
	}
	if got != head {
		return failure("stale_head", "HEAD moved since status was read; refresh and review again")
	}
	return nil
}

// commitExists reports whether oid names a commit in this repository.
func commitExists(ctx context.Context, g *gitReader, oid string) (bool, error) {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", oid+"^{commit}")
	if err != nil {
		if gitExitCode(err) == 1 || gitExitCode(err) == 128 {
			return false, nil
		}
		return false, failure("unavailable", "the commit could not be looked up")
	}
	return strings.TrimSpace(string(out)) == oid, nil
}

// branchTip returns refs/heads/name's commit, "" when it does not exist.
func branchTip(ctx context.Context, g *gitReader, name string) (string, error) {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", "refs/heads/"+name+"^{commit}")
	if err != nil {
		if gitExitCode(err) == 1 {
			return "", nil
		}
		return "", failure("unavailable", "the branch could not be looked up")
	}
	return strings.TrimSpace(string(out)), nil
}

// checkBranchName asks Git whether name is a valid new branch name.
func checkBranchName(ctx context.Context, g *gitReader, name string) error {
	if !plausibleBranchName(name) {
		return failure("invalid", "invalid branch name")
	}
	out, _, err := g.read(ctx, 4096, "check-ref-format", "--branch", name)
	if err != nil || strings.TrimSpace(string(out)) != name {
		return failure("invalid", "invalid branch name")
	}
	return nil
}

// isAncestor reports whether a is an ancestor of (or equal to) b.
func isAncestor(ctx context.Context, g *gitReader, a, b string) (bool, error) {
	_, _, err := g.read(ctx, 4096, "merge-base", "--is-ancestor", a, b)
	switch {
	case err == nil:
		return true, nil
	case gitExitCode(err) == 1:
		return false, nil
	}
	return false, failure("unavailable", "commit ancestry could not be determined")
}

// hasUnmerged reports unmerged index entries.
func hasUnmerged(ctx context.Context, g *gitReader) (bool, error) {
	out, _, err := g.read(ctx, 4096, "ls-files", "--unmerged", "-z")
	if err != nil {
		return false, failure("unavailable", "the index could not be read")
	}
	return len(out) > 0, nil
}

// refuseOperation refuses during merge, rebase, cherry-pick, revert or bisect.
func refuseOperation(ctx context.Context, g *gitReader, what string) error {
	if op := g.operation(ctx); op != "" {
		return failure("operation_in_progress", "a "+op+" is in progress; finish or abort it before "+what)
	}
	return nil
}

// fullStatus reads status exactly as GET /v1/git/status does, so its entry
// pins and GitWorktreeFingerprint match what the client was shown.
func fullStatus(ctx context.Context, g *gitReader) (protocol.GitStatus, error) {
	st := protocol.GitStatus{Entries: []protocol.GitStatusEntry{}}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(statusArgs("all"), "--branch")...)
	if err != nil {
		return st, gitError(err)
	}
	parseGitStatus(out, truncated, gitStatusMaxItems, &st)
	annotateGitStatus(g.dir, &st)
	return st, nil
}

// branchInOtherWorktree reports whether refs/heads/name is checked out in a
// worktree other than top.
func branchInOtherWorktree(ctx context.Context, g *gitReader, name string) (bool, error) {
	out, truncated, err := g.read(ctx, 1<<20, "worktree", "list", "--porcelain", "-z")
	if err != nil || truncated {
		return false, failure("unavailable", "worktrees could not be listed")
	}
	path := ""
	for _, field := range strings.Split(string(out), "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			path = strings.TrimPrefix(field, "worktree ")
		case field == "branch refs/heads/"+name && path != g.dir:
			return true, nil
		}
	}
	return false, nil
}

// Worktree rewrites and live documents. beginWorktreeRewrite brackets every
// Git command that rewrites working-tree files (switch and pull
// integration): through the engine's document coordinator it finishes
// pending saves and pauses autosave for every open document in the
// repository's working tree, and the returned end reconciles those
// documents with the result and resumes autosave. end runs after success,
// failure and cancellation alike (docs/design/git-client.md, "Coordinating
// Git with live buffers"). When a document could not be saved first, Git
// does not run (document_unsaved). Documents are matched by path overlap
// with the toplevel, so a document opened through a project registered at a
// subdirectory, or through a symlinked path, is included. A document opened
// after begin is not paused.
func beginWorktreeRewrite(ctx context.Context, rt *gitRuntime, top string) (end func(), res *protocol.GitResult) {
	end, err := rt.rewrite(ctx, top)
	if err == nil {
		return end, nil
	}
	code, message := "unavailable", "open documents could not be prepared: "+err.Error()
	var pe *protocol.Error
	if errors.As(err, &pe) {
		code, message = pe.Code, pe.Message
	}
	r := gitResult(protocol.GitStateFailed, code, message+"; Git did not run", nil)
	return end, &r
}

// documentRewrite is gitRuntime.rewrite for the engine's shared documents.
func (e *engine) documentRewrite(ctx context.Context, top string) (func(), error) {
	roots := e.documentRootsIn(top)
	var errs []error
	var releases []func()
	for _, root := range roots {
		// Each release ends exactly the pauses its begin made, even when
		// the begin failed or was cancelled.
		release, err := e.beginDocumentRewrite(ctx, root)
		releases = append(releases, release)
		errs = append(errs, err)
	}
	end := func() {
		for _, release := range releases {
			release()
		}
	}
	return end, errors.Join(errs...)
}

// documentRootsIn lists the distinct document roots overlapping top.
func (e *engine) documentRootsIn(top string) []string {
	e.docs.mu.Lock()
	var roots []string
	for _, a := range e.docs.byID {
		if !slices.Contains(roots, a.root) {
			roots = append(roots, a.root)
		}
	}
	e.docs.mu.Unlock()
	return slices.DeleteFunc(roots, func(root string) bool {
		if pathsOverlap(root, top) {
			return false
		}
		real, err := filepath.EvalSymlinks(root)
		return err != nil || !pathsOverlap(real, top)
	})
}

// rewriteSnapshot pins what a file-rewriting command may change: the status
// fingerprint and the worktree token of every path that differs between the
// two commits (which Git would write, even where it is ignored).
type rewriteSnapshot struct {
	top      string
	fp       string
	entries  int
	tokens   map[string]string
	complete bool
}

const rewriteSnapshotMaxPaths = 20000

// snapshotRewrite records the state before Git rewrites files for a move
// from commit from ("" when unborn) to commit to.
func snapshotRewrite(ctx context.Context, g *gitReader, top, from, to string) (rewriteSnapshot, error) {
	s := rewriteSnapshot{top: top, tokens: map[string]string{}, complete: true}
	st, err := fullStatus(ctx, g)
	if err != nil {
		return s, err
	}
	s.fp, s.entries = protocol.GitWorktreeFingerprint(st), len(st.Entries)
	args := []string{"diff-tree", "-r", "-z", "--name-only", "--no-renames", from, to}
	if from == "" {
		args = []string{"ls-tree", "-r", "-z", "--name-only", "--full-tree", to}
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
	if err != nil || truncated {
		s.complete = false
		return s, nil
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if len(s.tokens) >= rewriteSnapshotMaxPaths {
			s.complete = false
			break
		}
		s.tokens[p] = worktreeStat(top, p)
	}
	return s, nil
}

// changed reports whether the working tree, index or any snapshotted path
// differs now; an incomplete snapshot always counts as changed.
func (s rewriteSnapshot) changed(ctx context.Context, g *gitReader) bool {
	if !s.complete {
		return true
	}
	st, err := fullStatus(ctx, g)
	if err != nil || protocol.GitWorktreeFingerprint(st) != s.fp {
		return true
	}
	for p, token := range s.tokens {
		if worktreeStat(s.top, p) != token {
			return true
		}
	}
	return false
}

// gitMessageTrailer reports lines Git prints after a path list.
func gitMessageTrailer(line string) bool {
	for _, p := range []string{"Please ", "Aborting", "error: ", "fatal: ", "hint: ", "Merge with strategy"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return line == ""
}

// overwrittenPaths extracts the paths Git lists (verbatim, one per
// tab-indented line) after "would be overwritten by", "would be removed by"
// or "would lose untracked files" in untranslated output. incomplete is set
// when the list cannot be trusted: a line that continues a name (a file
// name containing a newline) or a listed path that does not exist below
// top.
func overwrittenPaths(out, top string) (paths []string, incomplete bool) {
	in := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "would be overwritten by") || strings.Contains(line, "would be removed by") || strings.Contains(line, "would lose untracked files"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			p := strings.TrimPrefix(line, "\t")
			if len(paths) >= 200 {
				incomplete = true
				continue
			}
			paths = append(paths, p)
			if _, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p))); err != nil || p == "" || strings.HasPrefix(p, "/") {
				incomplete = true
			}
		case in && !gitMessageTrailer(line):
			incomplete = true
		default:
			in = false
		}
	}
	return paths, incomplete
}

// wouldOverwrite fills a would_overwrite result's paths.
func wouldOverwrite(res *protocol.GitResult, out, top string) bool {
	paths, incomplete := overwrittenPaths(out, top)
	if len(paths) == 0 && !incomplete {
		return false
	}
	res.Code, res.Paths, res.PathsIncomplete = "would_overwrite", paths, incomplete
	return true
}

func prepareBranchCreate(ctx context.Context, g *gitReader, w *gitWriter, r protocol.GitRefWrite) (*gitPlan, error) {
	if err := checkBranchName(ctx, g, r.Name); err != nil {
		return nil, err
	}
	if ok, err := commitExists(ctx, g, r.StartOid); err != nil {
		return nil, err
	} else if !ok {
		return nil, failure("unknown_commit", "the start commit does not exist here; refresh and choose it again")
	}
	if tip, err := branchTip(ctx, g, r.Name); err != nil {
		return nil, err
	} else if tip != "" {
		return nil, failure("branch_exists", "a branch named "+r.Name+" already exists")
	}
	if err := w.checkRefLocks("refs/heads/" + r.Name); err != nil {
		return nil, err
	}
	return &gitPlan{run: func(ctx context.Context) protocol.GitResult {
		_, output, err := w.run(ctx, nil, true, "branch", "--no-track", r.Name, r.StartOid)
		if err != nil {
			if strings.Contains(string(output.bytes()), "already exists") {
				return gitResult(protocol.GitStateFailed, "branch_exists", "a branch named "+r.Name+" already exists", output)
			}
			return failedGit("git_failed", "Git could not create the branch", output)
		}
		res := gitResult(protocol.GitStateSucceeded, "", "Created branch "+r.Name+" at "+r.StartOid[:12], output)
		res.Ref = &protocol.GitRefResult{Branch: r.Name, Head: r.StartOid}
		return res
	}}, nil
}

// detachedOnlyCommits counts commits reachable from HEAD but from no branch,
// tag or remote-tracking ref, nor from target (the commit being switched
// to): switching away from a detached HEAD leaves them reachable only
// through the reflog.
func detachedOnlyCommits(ctx context.Context, g *gitReader, target string) (int, error) {
	out, _, err := g.read(ctx, 4096, "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes", target)
	if err != nil {
		return 0, failure("unavailable", "commits on the detached HEAD could not be counted")
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func prepareSwitch(ctx context.Context, g *gitReader, w *gitWriter, r protocol.GitRefWrite) (*gitPlan, error) {
	cur, err := readHead(ctx, g)
	if err != nil {
		return nil, err
	}
	if err := checkHeadPins(cur, r.ExpectedBranch, r.ExpectedHead); err != nil {
		return nil, err
	}
	if err := refuseOperation(ctx, g, "switching branches"); err != nil {
		return nil, err
	}
	create := r.Name != ""
	target, targetOid := r.Branch, r.TargetOid
	if create {
		targetOid = r.StartOid
		target = r.Name
		if err := checkBranchName(ctx, g, r.Name); err != nil {
			return nil, err
		}
		if tip, err := branchTip(ctx, g, r.Name); err != nil {
			return nil, err
		} else if tip != "" {
			return nil, failure("branch_exists", "a branch named "+r.Name+" already exists")
		}
		if ok, err := commitExists(ctx, g, r.StartOid); err != nil {
			return nil, err
		} else if !ok {
			return nil, failure("unknown_commit", "the start commit does not exist here; refresh and choose it again")
		}
	} else {
		if r.Branch == cur.branch {
			return nil, failure("already_on_branch", r.Branch+" is already checked out")
		}
		tip, err := branchTip(ctx, g, r.Branch)
		if err != nil {
			return nil, err
		}
		if tip != r.TargetOid {
			return nil, failure("stale_target", "the branch moved or no longer exists since it was shown; refresh and review again")
		}
		if elsewhere, err := branchInOtherWorktree(ctx, g, r.Branch); err != nil {
			return nil, err
		} else if elsewhere {
			return nil, failure("checked_out_elsewhere", r.Branch+" is checked out in another worktree")
		}
	}
	if cur.branch == "" && cur.oid != "" {
		n, err := detachedOnlyCommits(ctx, g, targetOid)
		if err != nil {
			return nil, err
		}
		if n > 0 && r.AcknowledgeLeaveCommits != n {
			return nil, failure("leaves_commits", strconv.Itoa(n)+" commits are reachable only from this detached HEAD; switching away leaves them only in the reflog. Confirm, or create a branch first")
		}
	}
	st, err := fullStatus(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, e := range st.Entries {
		if e.Group == protocol.GitGroupConflicted {
			return nil, failure("conflicted", "the index has unmerged paths; resolve them before switching")
		}
	}
	fp := protocol.GitWorktreeFingerprint(st)
	switch {
	case fp == protocol.GitFingerprintTruncated:
		return nil, failure("status_truncated", "too many changes to review here; switch from a terminal")
	case fp == protocol.GitFingerprintUnpinnable:
		return nil, failure("not_supported", "a changed path cannot be represented here; switch from a terminal")
	case fp != r.WorktreeFingerprint:
		return nil, failure("stale_status", "the working tree changed since status was read; refresh and review again")
	case r.AcknowledgeCarry != len(st.Entries):
		return nil, failure("carry_unacknowledged", strconv.Itoa(len(st.Entries))+" changed paths would be carried to "+target+"; confirm carrying them")
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	// Git rewrites files before it creates or points HEAD at the branch; a
	// lock found only then leaves the files changed without a switch.
	locked := []string{"refs/heads/" + target}
	if cur.branch != "" {
		locked = append(locked, "refs/heads/"+cur.branch)
	}
	if err := w.checkRefLocks(locked...); err != nil {
		return nil, err
	}
	carried := len(st.Entries)
	p := &gitPlan{}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		rt := p.runtime(ctx)
		if again, err := readHead(ctx, g); err != nil || checkHeadPins(again, r.ExpectedBranch, r.ExpectedHead) != nil {
			return gitResult(protocol.GitStateFailed, "stale_head", "HEAD moved after review; nothing was changed", nil)
		}
		// Re-verify the target just before Git runs: older checkout would
		// otherwise guess a remote branch of the same name.
		if tip, err := branchTip(ctx, g, target); err != nil || (create && tip != "") || (!create && tip != r.TargetOid) {
			return gitResult(protocol.GitStateFailed, "stale_target", "the target branch changed after review; nothing was changed", nil)
		}
		end, refused := beginWorktreeRewrite(ctx, rt, w.top)
		defer end()
		if refused != nil {
			return *refused
		}
		// Saving open documents can change the working tree after it was
		// checked; the user must review what would then be carried.
		before, err := snapshotRewrite(ctx, g, w.top, cur.oid, targetOid)
		switch {
		case err != nil:
			return gitResult(protocol.GitStateFailed, "unavailable", "the working tree could not be read; nothing was changed", nil)
		case before.fp != fp || before.entries != carried:
			return gitResult(protocol.GitStateFailed, "stale_status", "saving open documents changed the working tree; refresh and review what would be carried", nil)
		}
		if cur.branch == "" && cur.oid != "" {
			if n, err := detachedOnlyCommits(ctx, g, targetOid); err != nil || n > r.AcknowledgeLeaveCommits {
				return gitResult(protocol.GitStateFailed, "leaves_commits", "more commits than acknowledged would be left behind; refresh and review again", nil)
			}
		}
		var args []string
		switch {
		case w.restore && create:
			args = []string{"switch", "--no-track", "--no-overwrite-ignore", "--no-recurse-submodules", "-c", r.Name, r.StartOid}
		case w.restore:
			args = []string{"switch", "--no-guess", "--no-overwrite-ignore", "--no-recurse-submodules", r.Branch}
		case create:
			args = []string{"checkout", "--no-track", "--no-overwrite-ignore", "--no-recurse-submodules", "-b", r.Name, r.StartOid}
		default:
			args = []string{"checkout", "--no-overwrite-ignore", "--no-recurse-submodules", r.Branch, "--"}
		}
		run := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true}, args...)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, headErr := readHead(vctx, g)
		ref := &protocol.GitRefResult{Branch: after.branch, PreviousBranch: cur.branch, Head: after.oid, PreviousHead: cur.oid, Carried: carried}
		defer func() { res.Ref = ref }()
		switched := headErr == nil && after.branch == target
		unchanged := headErr == nil && after.branch == cur.branch && after.oid == cur.oid
		out := run.text()
		switch {
		case switched && run.err == nil:
			return gitResult(protocol.GitStateSucceeded, "", "Switched to "+target, run.output)
		case switched:
			// post-checkout's exit status becomes Git's, but the switch happened.
			return gitResult(protocol.GitStateSucceeded, "hook_failed", "Switched to "+target+", but the post-checkout hook failed", run.output)
		case !unchanged:
			return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git did not end on "+target+" and HEAD changed; refresh status", run.output)
		}
		// HEAD did not move. Git may still have rewritten files (a lock
		// that appeared after the checks, a failing filter): compare the
		// working tree, index and every path Git would write, including
		// ignored ones, with what was checked.
		if before.changed(vctx, g) {
			res = gitResult(protocol.GitStateOutcomeUnknown, "partial_switch", "Git changed files but did not switch to "+target+"; review status before continuing", run.output)
			wouldOverwrite(&res, out, w.top)
			res.Code = "partial_switch"
			return res
		}
		res = gitResult(protocol.GitStateFailed, "git_failed", "Git could not switch to "+target+"; nothing was changed", run.output)
		switch {
		case wouldOverwrite(&res, out, w.top):
			res.Message = "Git did not switch: these files would be overwritten; nothing was changed"
		case strings.Contains(out, "already checked out") || strings.Contains(out, "already used by worktree"):
			res.Code, res.Message = "checked_out_elsewhere", target+" is checked out in another worktree"
		default:
			if lock := lockFailure(out); lock != "" {
				res.Code = lock
			}
		}
		return res
	}
	return p, nil
}

func prepareResetSoft(ctx context.Context, g *gitReader, w *gitWriter, r protocol.GitRefWrite) (*gitPlan, error) {
	cur, err := readHead(ctx, g)
	if err != nil {
		return nil, err
	}
	if err := checkHeadPins(cur, r.ExpectedBranch, r.ExpectedHead); err != nil {
		return nil, err
	}
	if cur.oid == "" {
		return nil, failure("nothing_to_reset", "the branch has no commits yet")
	}
	if r.TargetOid == cur.oid {
		return nil, failure("already_at_target", "HEAD is already at this commit")
	}
	if ok, err := commitExists(ctx, g, r.TargetOid); err != nil {
		return nil, err
	} else if !ok {
		return nil, failure("unknown_commit", "the target commit does not exist here; refresh and choose it again")
	}
	if err := refuseOperation(ctx, g, "resetting"); err != nil {
		return nil, err
	}
	if unmerged, err := hasUnmerged(ctx, g); err != nil {
		return nil, err
	} else if unmerged {
		return nil, failure("conflicted", "the index has unmerged paths; resolve them first")
	}
	ancestor, err := isAncestor(ctx, g, r.TargetOid, cur.oid)
	if err != nil {
		return nil, err
	}
	descendant, err := isAncestor(ctx, g, cur.oid, r.TargetOid)
	if err != nil {
		return nil, err
	}
	if !ancestor && !descendant && !r.AcknowledgeNotAncestor {
		return nil, failure("not_ancestor", "the target is not on this branch's history; the staged comparison will include unrelated changes")
	}
	if !descendant && !r.AcknowledgePublished && leavesPublished(ctx, g, r.TargetOid, cur.oid) {
		return nil, failure("published_commit", "commits leaving the branch are already on a remote-tracking branch; resetting rewrites published history")
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	if cur.branch != "" {
		if err := w.checkRefLocks("refs/heads/" + cur.branch); err != nil {
			return nil, err
		}
	}
	return &gitPlan{run: func(ctx context.Context) protocol.GitResult {
		if again, err := readHead(ctx, g); err != nil || checkHeadPins(again, r.ExpectedBranch, r.ExpectedHead) != nil {
			return gitResult(protocol.GitStateFailed, "stale_head", "HEAD moved after review; nothing was changed", nil)
		}
		_, output, runErr := w.run(ctx, nil, true, "reset", "--soft", r.TargetOid)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, headErr := readHead(vctx, g)
		ref := &protocol.GitRefResult{Branch: after.branch, PreviousBranch: cur.branch, Head: after.oid, PreviousHead: cur.oid}
		var res protocol.GitResult
		switch {
		case headErr == nil && after.oid == r.TargetOid && runErr == nil:
			res = gitResult(protocol.GitStateSucceeded, "", "Moved "+describeHead(cur.branch)+" to "+r.TargetOid[:12]+"; files and staged content are unchanged", output)
		case headErr == nil && after.oid == cur.oid:
			res = failedGit("git_failed", "Git did not reset", output)
		default:
			res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git's reset outcome is unclear; refresh status", output)
		}
		res.Commit, res.Ref = after.oid, ref
		return res
	}}, nil
}

func describeHead(branch string) string {
	if branch == "" {
		return "detached HEAD"
	}
	return branch
}

// leavesPublished reports whether any commit in target..head is reachable
// from a remote-tracking ref (local refs only). Unknown counts as published.
func leavesPublished(ctx context.Context, g *gitReader, target, head string) bool {
	count := func(args ...string) (int, bool) {
		out, _, err := g.read(ctx, 4096, append([]string{"rev-list", "--count", head, "^" + target}, args...)...)
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		return n, err == nil
	}
	all, ok := count()
	if !ok {
		return true
	}
	unpublished, ok := count("--not", "--remotes")
	return !ok || unpublished != all
}
