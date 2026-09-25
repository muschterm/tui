package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Remote actions (ADR 0021): git.fetch, git.pull and git.push, plus the
// git.cancel request and the progress/stall machinery they share.
//
// Credentials are never prompted for (gitWriteEnv): helpers, ssh-agent and
// trusted host keys work as in the user's CLI, anything that would prompt
// fails and is classified (auth_required, host_key_unknown,
// agent_unavailable, transport). Git runs with --progress; progress lines
// are parsed into GitOp.Progress (published at most every 250 ms, never
// journaled) and kept out of the bounded output. A fetch that writes
// nothing for gitNetworkStall, or a push for gitPushStall, is ended
// (timeout).
//
// Pull never runs `git pull`: like it, it fetches exactly the upstream
// branch (FETCH_HEAD, updating the remote-tracking ref opportunistically),
// compares HEAD with the fetched commit and integrates only a fast forward with
// `git merge --ff-only --no-autostash --no-overwrite-ignore <tip>`, so
// inherited pull.rebase, pull.ff, merge.ff and merge.autostash settings
// cannot turn it into a merge, rebase or stash. Push runs `git push
// --porcelain <remote> refs/heads/<branch>:<upstream ref>`: never forced,
// no tags, pre-push hook included.

var (
	// gitNetworkStall ends a fetch that writes nothing this long.
	gitNetworkStall = 60 * time.Second
	// gitPushStall is longer: a pre-push hook may run tests silently.
	gitPushStall = 5 * time.Minute
	// gitProgressInterval throttles GitOp.Progress publication (≤ 4/s).
	gitProgressInterval = 250 * time.Millisecond
)

// gitRuntimeFor builds the runtime of a journaled command; the caller holds
// e.mu. Its rewrite coordinates with open documents (documentRewrite).
func (e *engine) gitRuntimeFor(id string, cancelCtx context.Context) *gitRuntime {
	var mu sync.Mutex
	var last time.Time
	updateOp := func(fn func(*protocol.GitOp)) {
		for i := range e.snap.GitOps {
			if op := &e.snap.GitOps[i]; op.CommandID == id && op.State == protocol.GitStateRunning {
				fn(op)
				// Published, never journaled: e.dirty stays as it is.
				e.snap.Revision++
				e.publish()
				return
			}
		}
	}
	return &gitRuntime{
		cancelCtx: cancelCtx,
		rewrite:   e.documentRewrite,
		progress: func(phase string, percent int) {
			mu.Lock()
			if time.Since(last) < gitProgressInterval {
				mu.Unlock()
				return
			}
			last = time.Now()
			mu.Unlock()
			e.mu.Lock()
			defer e.mu.Unlock()
			updateOp(func(op *protocol.GitOp) { op.Progress = &protocol.GitProgress{Phase: phase, Percent: percent} })
		},
		setCancellable: func(on bool) bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			st := e.git.userCancels[id]
			if st == nil || (!on && st.requested) {
				return false
			}
			if st.cancellable != on {
				st.cancellable = on
				updateOp(func(op *protocol.GitOp) { op.Cancellable = on })
			}
			return true
		},
	}
}

// gitCancel handles git.cancel. It is not journaled: cancelling is
// idempotent, and the cancelled command's own receipt records the outcome.
func (e *engine) gitCancel(c protocol.Command) (protocol.Receipt, error) {
	w := c.Git
	if w == nil || w.Cancel == nil || w.Cancel.CommandID == "" || w.Ref != nil || w.Sync != nil || len(w.Paths) != 0 || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished || w.Confirmed {
		return protocol.Receipt{}, failure("invalid", "git.cancel takes only the command ID to cancel")
	}
	id := w.Cancel.CommandID
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.gitLocked().userCancels[id]
	switch {
	case st == nil:
		return protocol.Receipt{}, failure("not_running", "that Git command is not running")
	case !st.cancellable:
		return protocol.Receipt{}, failure("not_cancellable", "that Git command can no longer be cancelled; wait for its result")
	}
	st.requested = true
	st.cancel()
	return protocol.Receipt{ID: c.ID, State: protocol.GitStateSucceeded, Revision: e.snap.Revision, TargetID: id,
		Git: &protocol.GitResult{Op: gitOpName(c.Kind), State: protocol.GitStateSucceeded, Message: "Cancellation requested; the command's own result reports what happened"}}, nil
}

// gitStallWatch cancels a command that has written nothing for stall.
type gitStallWatch struct {
	last  atomic.Int64
	fire  atomic.Bool
	done  chan struct{}
	close sync.Once
}

func newGitStallWatch(stall time.Duration, cancel context.CancelFunc) *gitStallWatch {
	w := &gitStallWatch{done: make(chan struct{})}
	w.touch()
	tick := max(stall/8, 5*time.Millisecond)
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-w.done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, w.last.Load())) >= stall {
					w.fire.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return w
}

func (w *gitStallWatch) touch() {
	if w != nil {
		w.last.Store(time.Now().UnixNano())
	}
}

func (w *gitStallWatch) fired() bool { return w.fire.Load() }

func (w *gitStallWatch) stop() { w.close.Do(func() { close(w.done) }) }

// gitTouchWriter records activity for the stall watch.
type gitTouchWriter struct {
	dst   io.Writer
	watch *gitStallWatch
}

func (t *gitTouchWriter) Write(p []byte) (int, error) {
	t.watch.touch()
	return t.dst.Write(p)
}

// gitProgressLines splits Git's stderr into progress updates (terminated by
// \r, dropped after parsing) and ordinary lines (kept in dst).
type gitProgressLines struct {
	dst      io.Writer
	progress func(phase string, percent int)
	watch    *gitStallWatch
	mu       sync.Mutex
	partial  []byte
}

const gitProgressLineMax = 4096

var gitProgressLine = regexp.MustCompile(`^(?:remote: )?([A-Za-z][A-Za-z ]*[A-Za-z]):\s+(?:(\d{1,3})%)?`)

func (l *gitProgressLines) Write(p []byte) (int, error) {
	l.watch.touch()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range p {
		switch b {
		case '\r':
			l.parse(l.partial)
			l.partial = l.partial[:0]
		case '\n':
			l.parse(l.partial)
			l.dst.Write(append(l.partial, '\n'))
			l.partial = l.partial[:0]
		default:
			if len(l.partial) >= gitProgressLineMax {
				l.dst.Write(l.partial)
				l.partial = l.partial[:0]
			}
			l.partial = append(l.partial, b)
		}
	}
	return len(p), nil
}

func (l *gitProgressLines) parse(line []byte) {
	if l.progress == nil {
		return
	}
	if m := gitProgressLine.FindSubmatch(line); m != nil && (len(m[2]) > 0 || bytes.HasSuffix(bytes.TrimSpace(line), []byte("done."))) {
		pct := -1
		if len(m[2]) > 0 {
			pct, _ = strconv.Atoi(string(m[2]))
		}
		l.progress(string(m[1]), min(pct, 100))
	}
}

func (l *gitProgressLines) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 {
		l.dst.Write(l.partial)
		l.partial = nil
	}
}

var gitRemoteName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)

// validRemoteName accepts remote names that cannot be mistaken for options
// or URLs.
func validRemoteName(name string) bool {
	return len(name) <= 255 && gitRemoteName.MatchString(name) && !strings.Contains(name, "..") && !strings.HasSuffix(name, "/")
}

// classifyRemoteFailure maps Git's untranslated output from a failed remote
// command to a code and advice. The empty code means none matched.
func classifyRemoteFailure(out string) (code, message string) {
	lower := strings.ToLower(out)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(lower, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("host key verification failed", "remote host identification has changed", "no matching host key", "host key is known for", "host key for"):
		return "host_key_unknown", "the SSH host key is not trusted here; fetch once in a terminal to review and accept it, then retry"
	case has("could not open a connection to your authentication agent", "error connecting to agent", "agent refused operation", "communication with agent failed", "sign_and_send_pubkey: signing failed"):
		return "agent_unavailable", "ssh-agent could not be used; start it or add your key (ssh-add), or fetch once in a terminal, then retry"
	case has("permission denied", "authentication failed", "could not read username", "could not read password", "terminal prompts disabled",
		"returned error: 401", "returned error: 403", "invalid username or password", "incorrect passphrase", "enter passphrase"):
		return "auth_required", "the remote needs credentials that no credential helper or ssh-agent supplied; fetch once in a terminal (or load your key into ssh-agent), then retry"
	}
	return "", ""
}

// remoteFailure is the code for a failed remote command.
func remoteFailure(run gitRunResult, cancelled bool, stall time.Duration) (code, message string) {
	switch {
	case run.stalled:
		return "timeout", "no output for " + stall.String() + " (a hook or the remote)"
	case cancelled:
		return "cancelled", "cancelled"
	}
	if code, message := classifyRemoteFailure(run.text()); code != "" {
		return code, message
	}
	return "transport", "the remote could not be reached or refused the request; see the output"
}

// gitUpstream is a branch's configured upstream.
type gitUpstream struct {
	ref        string // refs/remotes/origin/main
	short      string // origin/main, as GitStatus.Upstream shows it
	remote     string // origin, "." for a local upstream
	remoteRef  string // refs/heads/main on the remote
	pushRemote string // where a plain `git push` would go
}

func readUpstream(ctx context.Context, g *gitReader, branch string) (gitUpstream, error) {
	var u gitUpstream
	out, truncated, err := g.read(ctx, 64<<10, "for-each-ref", "--format=%(refname)%00%(upstream)%00%(upstream:short)%00%(upstream:remotename)%00%(upstream:remoteref)%00%(push:remotename)", "refs/heads/"+branch)
	if err != nil || truncated {
		return u, failure("unavailable", "the branch's upstream could not be read")
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) == 6 && f[0] == "refs/heads/"+branch {
			u = gitUpstream{ref: f[1], short: f[2], remote: f[3], remoteRef: f[4], pushRemote: f[5]}
		}
	}
	return u, nil
}

// refOid resolves a ref to its commit, "" when it does not exist.
func refOid(ctx context.Context, g *gitReader, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if gitExitCode(err) == 1 {
			return "", nil
		}
		return "", failure("unavailable", "a ref could not be read")
	}
	return strings.TrimSpace(string(out)), nil
}

func remoteExists(ctx context.Context, g *gitReader, name string) (bool, error) {
	out, truncated, err := g.read(ctx, 1<<20, "remote")
	if err != nil || truncated {
		return false, failure("unavailable", "remotes could not be listed")
	}
	for _, r := range strings.Split(string(out), "\n") {
		if r == name {
			return true, nil
		}
	}
	return false, nil
}

// upstreamFor resolves the current branch's upstream for pull and push and
// checks the pins shared by both.
func upstreamFor(ctx context.Context, g *gitReader, s protocol.GitSync) (gitHead, gitUpstream, error) {
	cur, err := readHead(ctx, g)
	if err != nil {
		return cur, gitUpstream{}, err
	}
	if cur.branch == "" {
		return cur, gitUpstream{}, failure("detached", "HEAD is detached; switch to a branch first")
	}
	if cur.oid == "" {
		return cur, gitUpstream{}, failure("not_supported", "the branch has no commits yet; use a terminal")
	}
	if err := checkHeadPins(cur, s.ExpectedBranch, s.ExpectedHead); err != nil {
		return cur, gitUpstream{}, err
	}
	up, err := readUpstream(ctx, g, cur.branch)
	switch {
	case err != nil:
		return cur, up, err
	case up.ref == "" || up.remote == "":
		return cur, up, failure("no_upstream", cur.branch+" has no upstream; set one in a terminal (git branch --set-upstream-to)")
	case up.remote == ".":
		return cur, up, failure("not_supported", "the upstream is a local branch; use switch or a terminal")
	case !validRemoteName(up.remote) || !strings.HasPrefix(up.remoteRef, "refs/heads/"):
		return cur, up, failure("not_supported", "this upstream cannot be used here; use a terminal")
	case up.short != s.Upstream:
		return cur, up, failure("stale_upstream", "the upstream changed since status was read; refresh and review again")
	}
	if ok, err := remoteExists(ctx, g, up.remote); err != nil {
		return cur, up, err
	} else if !ok {
		return cur, up, failure("unknown_remote", "remote "+up.remote+" is not configured")
	}
	return cur, up, nil
}

// fetch runs one fetch of remote (and, when given, only the listed remote
// refs) with extra options under rt's cancellation.
func (w *gitWriter) fetch(rt *gitRuntime, options []string, remote string, refs ...string) gitRunResult {
	args := append(append([]string{"fetch", "--progress", "--recurse-submodules=no"}, options...), remote)
	return w.runWith(rt.cancelCtx, gitRunOpts{combined: true, stall: gitNetworkStall, progress: rt.progress, cMessages: true, network: true},
		append(args, refs...)...)
}

// fetchOutcome fills Fetch from a finished fetch run and reports its code.
// A local ref lock is ref_locked, not a transport failure.
func fetchOutcome(ctx context.Context, g *gitReader, rt *gitRuntime, run gitRunResult, f *protocol.GitFetchResult) (code, message string) {
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
	defer cancel()
	if f.Upstream != "" && f.UpstreamAfter == "" {
		f.UpstreamAfter, _ = refOid(vctx, g, "refs/remotes/"+f.Upstream)
	}
	if run.err == nil {
		f.State = protocol.GitFetchSucceeded
		return "", ""
	}
	out := run.text()
	switch {
	case run.stalled || rt.cancelCtx.Err() != nil:
		code, message = remoteFailure(run, true, gitNetworkStall)
	case lockFailure(out) != "":
		code, message = lockFailure(out), "a local ref is locked by another Git process; retry when it finishes"
	case strings.Contains(out, "couldn't find remote ref"):
		code, message = "upstream_gone", "the upstream branch no longer exists on the remote"
	default:
		code, message = remoteFailure(run, false, gitNetworkStall)
	}
	f.State, f.Code = protocol.GitFetchFailed, code
	if code == "cancelled" {
		f.State = protocol.GitFetchCancelled
	}
	return code, message
}

func prepareFetch(ctx context.Context, g *gitReader, w *gitWriter, s protocol.GitSync) (*gitPlan, error) {
	remote := s.Remote
	var up gitUpstream
	cur, err := readHead(ctx, g)
	if err != nil {
		return nil, err
	}
	if cur.branch != "" {
		if up, err = readUpstream(ctx, g, cur.branch); err != nil {
			return nil, err
		}
	}
	if remote == "" {
		switch {
		case up.remote == "":
			return nil, failure("no_upstream", "no remote was chosen and the current branch has no upstream")
		case up.remote == ".":
			return nil, failure("not_supported", "the upstream is a local branch; there is nothing to fetch")
		case !validRemoteName(up.remote):
			return nil, failure("not_supported", "this remote name cannot be used here; fetch from a terminal")
		}
		remote = up.remote
	}
	if ok, err := remoteExists(ctx, g, remote); err != nil {
		return nil, err
	} else if !ok {
		return nil, failure("unknown_remote", "remote "+remote+" is not configured")
	}
	f := protocol.GitFetchResult{Remote: remote}
	if up.remote == remote && strings.HasPrefix(up.ref, "refs/remotes/") {
		f.Upstream = strings.TrimPrefix(up.ref, "refs/remotes/")
		if f.UpstreamBefore, err = refOid(ctx, g, up.ref); err != nil {
			return nil, err
		}
	}
	p := &gitPlan{}
	p.run = func(ctx context.Context) protocol.GitResult {
		rt := p.runtime(ctx)
		run := w.fetch(rt, nil, remote)
		fr := f
		code, message := fetchOutcome(ctx, g, rt, run, &fr)
		var res protocol.GitResult
		if code == "" {
			res = gitResult(protocol.GitStateSucceeded, "", "Fetched "+remote, run.output)
		} else {
			res = gitResult(protocol.GitStateFailed, code, "Fetch from "+remote+" failed: "+message+"; your branch and files are unchanged", run.output)
		}
		res.Fetch = &fr
		return res
	}
	return p, nil
}

// fetchedHead reads the oid of the for-merge FETCH_HEAD line for branch,
// which an explicit `git fetch --write-fetch-head <remote> <ref>` has just
// written.
func fetchedHead(ctx context.Context, g *gitReader, branch string) (string, error) {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return "", failure("unavailable", "FETCH_HEAD could not be located")
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.dir, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", failure("unavailable", "FETCH_HEAD could not be read")
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) == 3 && f[1] == "" && gitFullHash.MatchString(f[0]) && strings.HasPrefix(f[2], "branch '"+branch+"' of ") {
			return f[0], nil
		}
	}
	return "", failure("unavailable", "FETCH_HEAD did not name the fetched branch")
}

func preparePull(ctx context.Context, g *gitReader, w *gitWriter, s protocol.GitSync) (*gitPlan, error) {
	cur, up, err := upstreamFor(ctx, g, s)
	if err != nil {
		return nil, err
	}
	if err := refuseOperation(ctx, g, "pulling"); err != nil {
		return nil, err
	}
	if unmerged, err := hasUnmerged(ctx, g); err != nil {
		return nil, err
	} else if unmerged {
		return nil, failure("conflicted", "the index has unmerged paths; resolve them first")
	}
	if err := w.checkLocks(true); err != nil {
		return nil, err
	}
	if err := w.checkRefLocks("refs/heads/" + cur.branch); err != nil {
		return nil, err
	}
	before, err := refOid(ctx, g, up.ref)
	if err != nil {
		return nil, err
	}
	p := &gitPlan{}
	p.run = func(ctx context.Context) (res protocol.GitResult) {
		rt := p.runtime(ctx)
		fr := protocol.GitFetchResult{Remote: up.remote, Upstream: strings.TrimPrefix(up.ref, "refs/remotes/"), UpstreamBefore: before}
		integ := &protocol.GitIntegration{State: protocol.GitIntegrationNotStarted, From: cur.oid}
		defer func() { res.Fetch, res.Integration = &fr, integ }()
		// Like `git pull`, fetch exactly the upstream branch: the fetched
		// commit is integrated even when the remote's fetch refspecs exclude
		// it, and the remote-tracking ref is updated opportunistically when
		// they map it.
		var options []string
		if w.writeFetchHead {
			// fetch.writeFetchHEAD=false would leave an older FETCH_HEAD.
			options = []string{"--write-fetch-head"}
		}
		run := w.fetch(rt, options, up.remote, up.remoteRef)
		upstream := ""
		if run.err == nil {
			var err error
			if upstream, err = fetchedHead(ctx, g, strings.TrimPrefix(up.remoteRef, "refs/heads/")); err != nil {
				fr.State = protocol.GitFetchFailed
				return gitResult(protocol.GitStateFailed, "unavailable", "Fetched, but the fetched commit could not be read; nothing was integrated", run.output)
			}
			fr.UpstreamAfter = upstream
		}
		if code, message := fetchOutcome(ctx, g, rt, run, &fr); code != "" {
			return gitResult(protocol.GitStateFailed, code, "Fetch from "+up.remote+" failed: "+message+"; nothing was integrated", run.output)
		}
		// Integration is never cancelled once it starts.
		if !rt.setCancellable(false) || rt.cancelCtx.Err() != nil {
			return gitResult(protocol.GitStateFailed, "cancelled", "Fetched "+up.remote+"; cancelled before integrating, so your branch and files are unchanged", run.output)
		}
		integ.To = upstream
		if again, err := readHead(ctx, g); err != nil || checkHeadPins(again, s.ExpectedBranch, s.ExpectedHead) != nil {
			return gitResult(protocol.GitStateFailed, "stale_head", "Fetched, but HEAD moved after review; nothing was integrated", run.output)
		}
		ahead, err1 := isAncestor(ctx, g, upstream, cur.oid)
		behind, err2 := isAncestor(ctx, g, cur.oid, upstream)
		switch {
		case err1 != nil || err2 != nil:
			integ.State = protocol.GitIntegrationFailed
			return gitResult(protocol.GitStateFailed, "unavailable", "Fetched, but history could not be compared; nothing was integrated", run.output)
		case upstream == cur.oid:
			integ.State = protocol.GitIntegrationUpToDate
			return gitResult(protocol.GitStateSucceeded, "", "Fetched; "+cur.branch+" is up to date with "+up.short, run.output)
		case ahead:
			integ.State = protocol.GitIntegrationAhead
			return gitResult(protocol.GitStateSucceeded, "", "Fetched; "+cur.branch+" is ahead of "+up.short+" and has nothing to integrate", run.output)
		case !behind:
			integ.State = protocol.GitIntegrationDiverged
			return gitResult(protocol.GitStateFailed, "diverged", "Fetched, not integrated: "+cur.branch+" and "+up.short+" have diverged; merge or rebase explicitly", run.output)
		}
		integ.State = protocol.GitIntegrationFailed
		if err := refuseOperation(ctx, g, "pulling"); err != nil {
			return gitResult(protocol.GitStateFailed, "operation_in_progress", "Fetched, but an operation started; nothing was integrated", run.output)
		}
		end, refused := beginWorktreeRewrite(ctx, rt, w.top)
		defer end()
		if refused != nil {
			refused.Output, refused.OutputTruncated = string(run.output.bytes()), run.output.truncated()
			return *refused
		}
		// Snapshot after open documents were saved, so their saves are not
		// mistaken for Git's changes.
		before, err := snapshotRewrite(ctx, g, w.top, cur.oid, upstream)
		if err != nil {
			return gitResult(protocol.GitStateFailed, "unavailable", "Fetched, but the working tree could not be read; nothing was integrated", run.output)
		}
		// The fetch succeeded, so the merge runs on the non-cancellable
		// context and only the budget or server stop can end it.
		merge := w.runWith(ctx, gitRunOpts{combined: true, cMessages: true},
			"-c", "submodule.recurse=false",
			"merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", "--no-edit", "--quiet", upstream)
		output := joinOutputs(run.output, merge.output)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		after, headErr := readHead(vctx, g)
		out := merge.text()
		switch {
		case headErr == nil && after.oid == upstream && after.branch == cur.branch && merge.err == nil:
			integ.State = protocol.GitIntegrationFastForward
			res = gitResult(protocol.GitStateSucceeded, "", "Fast-forwarded "+cur.branch+" to "+up.short, output)
			res.Commit = upstream
			return res
		case headErr == nil && after.oid == upstream && after.branch == cur.branch:
			// post-merge cannot fail a merge; something else did after the
			// branch moved.
			integ.State = protocol.GitIntegrationFastForward
			res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported a failure after fast-forwarding "+cur.branch+"; review status", output)
			res.Commit = upstream
			return res
		case headErr != nil || after.oid != cur.oid || after.branch != cur.branch:
			return gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git failed while fast-forwarding; refresh status to see what changed", output)
		}
		if before.changed(vctx, g) {
			res = gitResult(protocol.GitStateOutcomeUnknown, "partial_switch", "Git changed files but did not fast-forward "+cur.branch+"; review status before continuing", output)
			wouldOverwrite(&res, out, w.top)
			res.Code = "partial_switch"
			return res
		}
		res = failedGit("git_failed", "Fetched, but Git could not fast-forward "+cur.branch+"; nothing was changed", output)
		if wouldOverwrite(&res, out, w.top) {
			res.Message = "Fetched, not integrated: these files would be overwritten; nothing was changed"
		}
		return res
	}
	return p, nil
}

// joinOutputs concatenates two bounded outputs into one bounded output.
func joinOutputs(a, b *cappedOutput) *cappedOutput {
	out := &cappedOutput{limit: gitWriteOutputMax, drain: true}
	for _, c := range []*cappedOutput{a, b} {
		if c != nil {
			out.Write(c.bytes())
			if c.truncated() {
				out.over = true
			}
		}
	}
	return out
}

// prePushHookRefused reads the trace2 events of one push and reports
// whether its pre-push hook (a hooks-directory file or a hook.<name> config
// entry) exited non-zero. Only the top-level git process is considered: its
// session ID has no parent part, since inherited GIT_TRACE2_PARENT_SID is
// removed.
func prePushHookRefused(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	type event struct {
		Event      string `json:"event"`
		SID        string `json:"sid"`
		ChildID    *int   `json:"child_id"`
		ChildClass string `json:"child_class"`
		HookName   string `json:"hook_name"`
		Code       int    `json:"code"`
	}
	hooks := map[int]bool{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var ev event
		if json.Unmarshal(line, &ev) != nil || ev.ChildID == nil || strings.Contains(ev.SID, "/") {
			continue
		}
		switch ev.Event {
		case "child_start":
			if ev.ChildClass == "hook" && ev.HookName == "pre-push" {
				hooks[*ev.ChildID] = true
			}
		case "child_exit":
			if hooks[*ev.ChildID] && ev.Code != 0 {
				return true
			}
		}
	}
	return false
}

// pushedOid resolves the new remote tip from a porcelain summary such as
// "1234abc..5678def", "" when it names none.
func pushedOid(ctx context.Context, g *gitReader, summary string) string {
	_, abbrev, ok := strings.Cut(summary, "..")
	abbrev = strings.TrimPrefix(abbrev, ".")
	if !ok || !gitHashInput.MatchString(abbrev) {
		return ""
	}
	oid, _ := refOid(ctx, g, abbrev)
	return oid
}

// pushLine is one ref line of `git push --porcelain`.
type pushLine struct{ flag, from, to, summary, reason string }

func parsePushPorcelain(stdout []byte, to string) (pushLine, bool) {
	for _, line := range strings.Split(string(stdout), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || len(f[0]) != 1 {
			continue
		}
		from, dst, _ := strings.Cut(f[1], ":")
		if dst != to {
			continue
		}
		l := pushLine{flag: f[0], from: from, to: dst, summary: f[2]}
		if open := strings.LastIndex(f[2], " ("); open >= 0 && strings.HasSuffix(f[2], ")") {
			l.summary, l.reason = f[2][:open], f[2][open+2:len(f[2])-1]
		}
		return l, true
	}
	return pushLine{}, false
}

// pushDefaultAllows mirrors the user's push.default for pushing branch to
// the upstream branch upstreamName on the upstream's remote: upstream (and
// its old name tracking) pushes to the upstream whatever its name; simple
// (the default), current and matching push only to a branch of the same
// name; nothing never pushes without an explicit refspec.
func pushDefaultAllows(ctx context.Context, g *gitReader, branch, upstreamName string) error {
	out, _, err := g.read(ctx, 4096, "config", "--get", "push.default")
	mode := strings.TrimSpace(string(out))
	if err != nil && gitExitCode(err) != 1 {
		return failure("unavailable", "push.default could not be read")
	}
	switch mode {
	case "upstream", "tracking":
		return nil
	case "nothing":
		return failure("not_supported", "push.default is nothing, so Git pushes nothing without an explicit destination; push from a terminal")
	case "", "simple", "current", "matching":
		if branch != upstreamName {
			return failure("upstream_name_mismatch", branch+" tracks a branch with a different name ("+upstreamName+"); with push.default="+cmp.Or(mode, "simple")+" Git would not push it there. Push from a terminal or set push.default=upstream")
		}
		return nil
	}
	return failure("not_supported", "unrecognized push.default "+mode+"; push from a terminal")
}

// pushSigningFailure reports a signed-push failure (push.gpgSign), which
// happens before anything is sent.
func pushSigningFailure(out string) bool {
	return strings.Contains(out, "failed to sign the push certificate") || strings.Contains(out, "gpg failed to sign") || strings.Contains(out, "does not support --signed push")
}

// pushPackStarted reports whether Git's output shows that the pre-push hook
// passed and a pack was built or sent, or the connection broke afterwards.
func pushPackStarted(out string) bool {
	for _, s := range []string{"Enumerating objects", "Counting objects", "Compressing objects", "Writing objects", "Total ", "hung up", "unexpected disconnect", "remote end"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func preparePush(ctx context.Context, g *gitReader, w *gitWriter, s protocol.GitSync) (*gitPlan, error) {
	cur, up, err := upstreamFor(ctx, g, s)
	if err != nil {
		return nil, err
	}
	if up.pushRemote != "" && up.pushRemote != up.remote {
		return nil, failure("not_supported", "this branch pushes to "+up.pushRemote+", not its upstream's remote; push from a terminal")
	}
	if out, _, err := g.read(ctx, 4096, "config", "--bool", "--get", "remote."+up.remote+".mirror"); err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil, failure("not_supported", up.remote+" is a mirror remote; push from a terminal")
	}
	if out, _, err := g.read(ctx, 64<<10, "config", "--get-all", "remote."+up.remote+".push"); err == nil && len(bytes.TrimSpace(out)) > 0 {
		return nil, failure("not_supported", "remote."+up.remote+".push is configured; push from a terminal")
	}
	if err := pushDefaultAllows(ctx, g, cur.branch, strings.TrimPrefix(up.remoteRef, "refs/heads/")); err != nil {
		return nil, err
	}
	tracking, err := refOid(ctx, g, up.ref)
	switch {
	case err != nil:
		return nil, err
	case tracking == "":
		return nil, failure("upstream_gone", up.short+" does not exist locally; fetch to check the remote, or push from a terminal")
	case s.ExpectedUpstreamOid != "" && s.ExpectedUpstreamOid != tracking:
		return nil, failure("stale_upstream", up.short+" moved since it was shown; refresh and review again")
	}
	if ok, err := isAncestor(ctx, g, tracking, cur.oid); err != nil {
		return nil, err
	} else if !ok {
		return nil, failure("behind_upstream", cur.branch+" is behind or has diverged from "+up.short+"; pull (or merge/rebase) first")
	}
	p := &gitPlan{}
	p.run = func(ctx context.Context) protocol.GitResult {
		rt := p.runtime(ctx)
		push := &protocol.GitPushResult{Remote: up.remote, RemoteRef: up.remoteRef, OldOid: tracking}
		if tip, err := branchTip(ctx, g, cur.branch); err != nil || tip != cur.oid {
			r := gitResult(protocol.GitStateFailed, "stale_head", cur.branch+" moved after review; nothing was pushed", nil)
			push.State = protocol.GitPushRejected
			r.Push = push
			return r
		}
		trace := ""
		if dir, err := os.MkdirTemp("", "tui-git-push-"); err == nil {
			defer os.RemoveAll(dir)
			trace = filepath.Join(dir, "trace2.json")
		}
		run := w.runWith(rt.cancelCtx, gitRunOpts{stall: gitPushStall, progress: rt.progress, cMessages: true, network: true, trace2: trace},
			"push", "--porcelain", "--progress", "--no-follow-tags", "--recurse-submodules=no", up.remote, "refs/heads/"+cur.branch+":"+up.remoteRef)
		stderr := run.text()
		output := run.output
		output.Write(run.stdout)
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitRequestBudget)
		defer cancel()
		line, reported := parsePushPorcelain(run.stdout, up.remoteRef)
		remoteFailed := reported && line.flag == "!" && (strings.Contains(line.summary, "remote failure") || strings.Contains(line.reason, "failed to report status"))
		var res protocol.GitResult
		switch {
		case remoteFailed:
			push.State, push.Reason = protocol.GitPushUnknown, cmp.Or(line.reason, line.summary)
			res = gitResult(protocol.GitStateOutcomeUnknown, "transport", "The remote did not report whether it accepted the push; fetch to check", output)
		case reported && line.flag == "!":
			push.State, push.Reason = protocol.GitPushRejected, cmp.Or(line.reason, line.summary)
			res = gitResult(protocol.GitStateFailed, "rejected", "The remote rejected the push ("+push.Reason+"); nothing was changed there", output)
		case reported && line.flag == "=":
			push.State = protocol.GitPushUpToDate
			res = gitResult(protocol.GitStateSucceeded, "", up.short+" is already up to date", output)
		case reported && (line.flag == " " || line.flag == "*"):
			push.State = protocol.GitPushPushed
			res = gitResult(protocol.GitStateSucceeded, "", "Pushed "+cur.branch+" to "+up.short, output)
			// The remote-tracking ref is not updated when the fetch refspecs
			// exclude it, so the pushed commit comes from the status line;
			// a new branch ("[new branch]") falls back to the ref.
			push.NewOid = pushedOid(vctx, g, line.summary)
			if push.NewOid == "" {
				if tip, _ := refOid(vctx, g, up.ref); tip != tracking {
					push.NewOid = tip
				}
			}
			if push.NewOid != "" && push.NewOid != cur.oid {
				res.Code, res.Message = "pushed_newer_head", "Pushed "+cur.branch+", but it moved after review: "+push.NewOid+" was pushed instead of "+cur.oid
			}
		case run.stalled || rt.cancelCtx.Err() != nil:
			code, message := remoteFailure(run, true, gitPushStall)
			push.State = protocol.GitPushUnknown
			res = gitResult(protocol.GitStateOutcomeUnknown, code, "Push "+message+"; the remote may have accepted it, fetch to check", output)
		case run.err != nil && pushSigningFailure(stderr):
			push.State = protocol.GitPushRejected
			res = gitResult(protocol.GitStateFailed, "signing_failed", "The push could not be signed (push.gpgSign), so nothing was sent; push from a terminal to sign interactively", output)
		case run.err != nil && strings.Contains(stderr, "--mirror can't be combined"):
			push.State = protocol.GitPushRejected
			res = gitResult(protocol.GitStateFailed, "not_supported", up.remote+" is a mirror remote; nothing was sent", output)
		default:
			code, message := classifyRemoteFailure(stderr)
			switch {
			case run.err == nil:
				push.State = protocol.GitPushUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "git_failed", "Git reported success without a status for "+up.remoteRef+"; fetch to check", output)
			case prePushHookRefused(trace):
				// Git's trace shows the pre-push hook exited non-zero; Git
				// sends nothing after that.
				push.State, push.Reason = protocol.GitPushRejected, "pre-push hook"
				res = gitResult(protocol.GitStateFailed, "rejected", "The pre-push hook declined the push; nothing was sent", output)
			case code != "" && !pushPackStarted(stderr):
				push.State = protocol.GitPushRejected
				res = gitResult(protocol.GitStateFailed, code, "Push failed: "+message, output)
			default:
				push.State = protocol.GitPushUnknown
				res = gitResult(protocol.GitStateOutcomeUnknown, "transport", "The push failed without a status from the remote; it may or may not have the update, fetch to check", output)
			}
		}
		res.Push = push
		return res
	}
	return p, nil
}
