package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Managed worktrees (ADR 0024). thread.start with Workspace mode "worktree"
// creates a new branch at the chosen commit in a new worktree under the
// application home (<home>/worktrees/<project-id>/<branch-slug>-<cmd8>) and
// starts the thread there; Thread.Checkout is the worktree path joined with
// the project's path inside its repository, so every surface (files, Git,
// terminals, documents, the agent) follows it. Creation is journaled: the
// record is saved as creating with a running receipt before Git runs, and a
// worktree whose thread cannot be attached is kept (unattached), never
// deleted. The state of every record is detected from the file system (the
// worktree's .git file and Git's registration under the common directory);
// sending to a thread whose worktree is not present is refused
// (workspace_unavailable). worktree.relocate, worktree.forget,
// worktree.prune and worktree.remove are explicit recovery and cleanup
// commands; nothing removes a worktree automatically.

const (
	worktreeBudget      = 10 * time.Minute
	worktreeBranchMax   = 200
	worktreeIgnoredMax  = 20
	worktreeCheckPeriod = 2 * time.Second
)

// Test hooks around `git worktree add`; nil in the server.
var worktreeBeforeAdd, worktreeAfterAdd func(path string)

// ---- Validation and naming ----

func validateWorktreeStart(c protocol.Command) error {
	w := c.Workspace
	switch {
	case w == nil || w.Mode != "worktree":
		return failure("invalid", "not a worktree start")
	case c.Settings == nil:
		return failure("invalid", "starting a thread requires captured settings")
	case !gitFullHash.MatchString(w.StartOid):
		return failure("invalid", "choose the starting commit by its full object ID")
	case w.Branch == "" || len(w.Branch) > worktreeBranchMax || strings.ContainsAny(w.Branch, "\x00\n\r"):
		return failure("invalid", fmt.Sprintf("the new branch needs a name of 1–%d bytes", worktreeBranchMax))
	case strings.HasPrefix(w.Branch, "-") || strings.HasPrefix(w.Branch, "refs/") || w.Branch == "HEAD":
		return failure("invalid_branch", "that is not a valid new branch name")
	}
	return nil
}

// worktreeDirName is <branch-slug>-<cmd8>: the branch reduced to safe
// characters, and eight hex digits of the command ID's hash, so retries of
// one command name the same directory and different commands never share one.
func worktreeDirName(branch, commandID string) string {
	var b strings.Builder
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if slug == "" {
		slug = "worktree"
	}
	sum := sha256.Sum256([]byte(commandID))
	return slug + "-" + hex.EncodeToString(sum[:])[:8]
}

func worktreeByID(s *protocol.Snapshot, id string) *protocol.ManagedWorktree {
	for i := range s.Worktrees {
		if s.Worktrees[i].ID == id {
			return &s.Worktrees[i]
		}
	}
	return nil
}

func worktreeByCommand(s *protocol.Snapshot, commandID string) *protocol.ManagedWorktree {
	for i := range s.Worktrees {
		if s.Worktrees[i].CommandID == commandID {
			return &s.Worktrees[i]
		}
	}
	return nil
}

// worktreeThreads lists the threads created in worktree id.
// worktreeAt returns the ID of the managed worktree whose path is top.
func worktreeAt(s *protocol.Snapshot, top string) string {
	real := realPath(top)
	for _, rec := range s.Worktrees {
		if rec.State != protocol.WorktreeRemoved && (rec.Path == top || realPath(rec.Path) == real) {
			return rec.ID
		}
	}
	return ""
}

func worktreeThreads(s *protocol.Snapshot, id string) []*protocol.Thread {
	var out []*protocol.Thread
	for i := range s.Threads {
		if s.Threads[i].WorktreeID == id {
			out = append(out, &s.Threads[i])
		}
	}
	return out
}

func worktreeCheckout(rec protocol.ManagedWorktree, path string) string {
	if rec.RelPath == "" || rec.RelPath == "." {
		return path
	}
	return filepath.Join(path, filepath.FromSlash(rec.RelPath))
}

// ---- State detection ----

// gitdirLink reads a "gitdir: <path>" file (a worktree's .git, or the
// admin entry's gitdir), resolving a relative target against base.
func gitdirLink(file, base string) (string, bool) {
	b, err := os.ReadFile(file)
	if err != nil || len(b) > 4096 {
		return "", false
	}
	line := strings.TrimSpace(string(b))
	line = strings.TrimPrefix(line, "gitdir: ")
	if line == "" {
		return "", false
	}
	if !filepath.IsAbs(line) {
		line = filepath.Join(base, line)
	}
	return filepath.Clean(line), true
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}

// detectWorktree reads rec's state from the file system: its directory's
// .git file and Git's admin entry (CommonDir/worktrees/AdminName, whose
// gitdir file names the registered location). It never runs Git.
func detectWorktree(rec protocol.ManagedWorktree) (state, movedTo string) {
	switch rec.State {
	case protocol.WorktreeCreating, protocol.WorktreeRemoved:
		return rec.State, ""
	}
	admin := filepath.Join(rec.CommonDir, "worktrees", rec.AdminName)
	registered := ""
	if rec.AdminName != "" {
		if dotgit, ok := gitdirLink(filepath.Join(admin, "gitdir"), admin); ok {
			registered = filepath.Dir(dotgit)
		}
	}
	if info, err := os.Stat(rec.Path); err == nil && info.IsDir() {
		link, ok := gitdirLink(filepath.Join(rec.Path, ".git"), rec.Path)
		if ok && registered != "" && sameFile(link, admin) && sameFile(registered, rec.Path) {
			return protocol.WorktreePresent, ""
		}
		return protocol.WorktreeUnregistered, ""
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return protocol.WorktreeMissing, "" // unreadable counts as unavailable
	}
	if registered != "" && registered != rec.Path {
		if link, ok := gitdirLink(filepath.Join(registered, ".git"), registered); ok && sameFile(link, admin) {
			return protocol.WorktreeMoved, realPath(registered)
		}
	}
	return protocol.WorktreeMissing, ""
}

// worktreeDetection is one record's detected state, with the state and
// path the detection started from.
type worktreeDetection struct {
	fromState, fromPath string
	state, movedTo      string
}

// refreshWorktreesIn applies detected states to s; a present worktree no
// thread uses is unattached. A detection is applied only while the record
// still has the state and path it was made from, so a result computed
// outside the lock never reverts a concurrent transition, and detection
// never writes creating or removed. It reports a change.
func refreshWorktreesIn(s *protocol.Snapshot, detected map[string]worktreeDetection) bool {
	changed := false
	for i := range s.Worktrees {
		rec := &s.Worktrees[i]
		d, ok := detected[rec.ID]
		if !ok || d.fromState != rec.State || d.fromPath != rec.Path {
			continue
		}
		state, moved := d.state, d.movedTo
		switch state {
		case protocol.WorktreeCreating, protocol.WorktreeRemoved:
			continue
		}
		switch rec.State {
		case protocol.WorktreeCreating, protocol.WorktreeRemoved:
			continue
		}
		if state == protocol.WorktreePresent && len(worktreeThreads(s, rec.ID)) == 0 {
			state = protocol.WorktreeUnattached
		}
		if rec.State != state || rec.MovedTo != moved {
			rec.State, rec.MovedTo = state, moved
			changed = true
		}
	}
	return changed
}

func detectAll(list []protocol.ManagedWorktree) map[string]worktreeDetection {
	out := make(map[string]worktreeDetection, len(list))
	for _, rec := range list {
		state, moved := detectWorktree(rec)
		out[rec.ID] = worktreeDetection{fromState: rec.State, fromPath: rec.Path, state: state, movedTo: moved}
	}
	return out
}

// refreshWorktreesLocked detects every record now (e.mu held; the checks
// are a few stats of local paths) and persists a change.
func (e *engine) refreshWorktreesLocked() {
	if len(e.snap.Worktrees) == 0 {
		return
	}
	next := clone(e.snap)
	if refreshWorktreesIn(&next, detectAll(next.Worktrees)) {
		next.Revision++
		e.snap = next
		e.flushLocked()
		e.rebalanceWritersAndFlushLocked()
	}
}

// checkWorktrees is the periodic detection: stats outside the lock,
// applied under it.
func (e *engine) checkWorktrees() {
	e.mu.Lock()
	if len(e.snap.Worktrees) == 0 || time.Since(e.worktreesCheckedAt) < worktreeCheckPeriod {
		e.mu.Unlock()
		return
	}
	e.worktreesCheckedAt = time.Now()
	list := slices.Clone(e.snap.Worktrees)
	e.mu.Unlock()
	detected := detectAll(list)
	e.mu.Lock()
	defer e.mu.Unlock()
	next := clone(e.snap)
	if refreshWorktreesIn(&next, detected) {
		next.Revision++
		e.snap = next
		e.flushLocked()
		// A worktree that is back lets its queued prompts proceed.
		e.rebalanceWritersAndFlushLocked()
	}
}

// worktreeUnavailable is the Send and dispatch guard: nil unless t works in
// a managed worktree that is not present (or no longer managed).
func worktreeUnavailable(s *protocol.Snapshot, t *protocol.Thread) error {
	if t == nil || t.WorktreeID == "" {
		return nil
	}
	rec := worktreeByID(s, t.WorktreeID)
	if rec == nil {
		return failure("workspace_unavailable", "this thread's worktree is no longer managed by the application; nothing was sent")
	}
	state, _ := detectWorktree(*rec)
	switch state {
	case protocol.WorktreePresent, protocol.WorktreeUnattached:
		return nil
	case protocol.WorktreeMoved:
		return failure("workspace_unavailable", "this thread's worktree was moved to "+realPath(rec.MovedTo)+"; relocate it before sending")
	}
	return failure("workspace_unavailable", "this thread's worktree is "+state+" ("+rec.Path+"); nothing was sent")
}

// threadCheckout is the checkout resolver for every thread-scoped surface
// (Files, documents, Git reads and writes, terminals, workspace info): a
// thread whose managed worktree is not present gets workspace_unavailable,
// never a path from which Git could discover an enclosing repository.
func threadCheckout(s *protocol.Snapshot, t *protocol.Thread) (string, error) {
	if err := worktreeUnavailable(s, t); err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			pe.Message = strings.Replace(pe.Message, "; nothing was sent", "", 1)
		}
		return "", err
	}
	return t.Checkout, nil
}

// worktreeCeilings mirrors the managed worktree records' paths (every
// state but a forgotten record). Git run at or below one of them gets the
// path's parent in GIT_CEILING_DIRECTORIES, so repository discovery from a
// worktree whose .git file is gone stops at the worktree instead of walking
// up into a repository that encloses it. Git run anywhere else is
// unaffected. The set is replaced from the snapshot on every publish.
var worktreeCeilings struct {
	sync.Mutex
	paths []string
	// real caches each recorded path's resolved form, so a publish only
	// resolves symlinks for a path it has not seen (a new or moved record).
	real map[string]string
}

// syncWorktreeCeilings replaces the set with list's paths (real and as
// recorded). Removed records get no ceiling.
func syncWorktreeCeilings(list []protocol.ManagedWorktree) {
	worktreeCeilings.Lock()
	cached := worktreeCeilings.real
	worktreeCeilings.Unlock()
	var paths []string
	real := make(map[string]string, len(list))
	for _, rec := range list {
		if rec.Path == "" || !filepath.IsAbs(rec.Path) || rec.State == protocol.WorktreeRemoved {
			continue
		}
		p := filepath.Clean(rec.Path)
		r, ok := cached[p]
		if !ok {
			r = realPath(p)
		}
		real[p] = r
		paths = append(paths, p)
		if r != p {
			paths = append(paths, r)
		}
	}
	worktreeCeilings.Lock()
	worktreeCeilings.paths, worktreeCeilings.real = paths, real
	worktreeCeilings.Unlock()
}

// gitCeilingEnv returns the GIT_CEILING_DIRECTORIES entry for Git run in
// dir, or nil when dir is not at or below a managed worktree.
func gitCeilingEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	dir = filepath.Clean(dir)
	real := realPath(dir)
	worktreeCeilings.Lock()
	var hits []string
	for _, p := range worktreeCeilings.paths {
		within := func(x string) bool { return x == p || strings.HasPrefix(x, strings.TrimSuffix(p, "/")+"/") }
		if within(dir) || within(real) {
			if parent := filepath.Dir(p); !slices.Contains(hits, parent) {
				hits = append(hits, parent)
			}
		}
	}
	worktreeCeilings.Unlock()
	if len(hits) == 0 {
		return nil
	}
	slices.Sort(hits)
	return []string{"GIT_CEILING_DIRECTORIES=" + strings.Join(hits, string(os.PathListSeparator))}
}

// reconcileWorktrees runs at startup: a record still creating belongs to a
// server that stopped while Git ran. A worktree Git finished is kept as
// unattached (a retry of the same command attaches its thread); a record
// with nothing on disk is dropped (a branch Git created is kept).
func reconcileWorktrees(s *protocol.Snapshot) {
	syncWorktreeCeilings(s.Worktrees)
	kept := s.Worktrees[:0]
	for _, rec := range s.Worktrees {
		if rec.State == protocol.WorktreeCreating {
			link, ok := gitdirLink(filepath.Join(rec.Path, ".git"), rec.Path)
			if !ok {
				if _, err := os.Lstat(rec.Path); err != nil {
					continue // Git never created it
				}
				link = ""
			}
			rec.State = protocol.WorktreeUnattached
			if link != "" {
				rec.AdminName = filepath.Base(link)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var err error
			if link == "" {
				err = errors.New("the directory is not a worktree")
			} else {
				_, err = verifyWorktree(ctx, &worktreePlan{path: rec.Path, branch: rec.Branch, oid: rec.StartOid})
			}
			cancel()
			if err != nil {
				rec.Unverified = true
				rec.Detail = "the server stopped while the worktree was being created and what Git left could not be verified (" + err.Error() + "); inspect it, then remove or forget it"
			} else {
				rec.Detail = "the server stopped while the worktree was being created; retry the same request to start its thread, or remove it"
			}
		}
		kept = append(kept, rec)
	}
	s.Worktrees = kept
	if len(s.Worktrees) == 0 {
		s.Worktrees = nil
	}
	refreshWorktreesIn(s, detectAll(s.Worktrees))
}

// ---- Creation ----

// worktreePlan is a validated creation, computed without the lock.
type worktreePlan struct {
	project     protocol.Project
	top, common string
	rel, path   string
	branch, oid string
	writer      *gitWriter
}

// worktreeStart handles thread.start with Workspace mode "worktree".
func (e *engine) worktreeStart(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	if err := validateWorktreeStart(c); err != nil {
		return protocol.Receipt{}, err
	}
	release, r, attach, err := e.reserveWorktreeCommand(ctx, c)
	if err != nil || r != nil {
		return deref(r), err
	}
	defer release()

	e.mu.Lock()
	captureState := clone(e.snap)
	e.mu.Unlock()
	captured, err := captureCommand(ctx, captureState, c, e.store)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if attach {
		return e.attachWorktree(c, captured)
	}

	e.mu.Lock()
	if e.worktreeDir == "" {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("unsupported_workspace", "this server has no worktree storage")
	}
	var project *protocol.Project
	for i := range e.snap.Projects {
		if e.snap.Projects[i].ID == c.ProjectID {
			p := e.snap.Projects[i]
			project = &p
		}
	}
	if project == nil {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("not_found", "project does not exist")
	}
	if err := e.dryRunStart(captured, captureState); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	}
	root := e.worktreeDir
	e.mu.Unlock()

	prepCtx, cancel := context.WithTimeout(ctx, gitWriteBudget)
	defer cancel()
	plan, err := prepareWorktree(prepCtx, *project, root, c)
	if err != nil {
		return protocol.Receipt{}, err
	}

	// Phase one: take the repository's Git slot and journal the record with
	// a running receipt before Git runs.
	e.mu.Lock()
	gs := e.gitLocked()
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	if err := e.dryRunStart(captured, captureState); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	}
	if p := projectByID(&e.snap, project.ID); p == nil || p.Path != project.Path {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("not_found", "the project changed while the request was prepared")
	}
	for id, h := range gs.holders {
		if h.common == plan.common || pathsOverlap(h.top, plan.path) {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("git_busy", "another Git change ("+id+") is running in this repository")
		}
	}
	gs.holders[c.ID] = gitHold{top: plan.path, common: plan.common, projectID: project.ID, refOp: true}
	next := clone(e.snap)
	rec := protocol.ManagedWorktree{ID: "worktree-" + ID(), ProjectID: project.ID, Path: plan.path, CommonDir: plan.common, RelPath: filepath.ToSlash(plan.rel),
		Branch: plan.branch, StartOid: plan.oid, CommandID: c.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339), State: protocol.WorktreeCreating}
	next.Worktrees = append(next.Worktrees, rec)
	next.Revision++
	running := protocol.Receipt{ID: c.ID, State: "running", Revision: next.Revision, TargetID: "thread-" + c.ID}
	if err := e.store.Save(next, &c, &running); err != nil {
		delete(gs.holders, c.ID)
		e.mu.Unlock()
		return protocol.Receipt{}, failure("storage", "the worktree could not be recorded, so Git did not run")
	}
	e.snap = next
	e.publish()
	parent := e.runctx
	if parent == nil {
		parent = context.Background()
	}
	gs.wg.Add(1)
	e.mu.Unlock()
	defer gs.wg.Done()

	runCtx, cancelRun := context.WithTimeout(parent, worktreeBudget)
	defer cancelRun()
	if worktreeBeforeAdd != nil {
		worktreeBeforeAdd(plan.path)
	}
	_, output, runErr := plan.writer.run(runCtx, nil, true, "worktree", "add", "-b", plan.branch, plan.path, plan.oid)
	admin, verifyErr := verifyWorktree(runCtx, plan)
	if worktreeAfterAdd != nil {
		worktreeAfterAdd(plan.path)
	}
	branchLeft := false
	if runErr != nil || verifyErr != nil {
		branchLeft = branchExists(runCtx, plan.writer, plan.branch)
	}

	// Phase two: attach the thread, or keep the worktree unattached.
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(gs.holders, c.ID)
	next = clone(e.snap)
	recp := worktreeByID(&next, rec.ID)
	var final protocol.Receipt
	switch {
	case runErr != nil || verifyErr != nil:
		msg := "Git could not create the worktree"
		if runErr != nil {
			if text := strings.TrimSpace(string(output.bytes())); text != "" {
				msg += ": " + gitSummary(text)
			}
		} else {
			msg += ": " + verifyErr.Error()
		}
		if branchLeft {
			msg += "; branch " + plan.branch + " exists and was kept"
		}
		if _, err := os.Lstat(plan.path); err != nil && recp != nil {
			// Nothing to keep: drop the record.
			next.Worktrees = slices.DeleteFunc(next.Worktrees, func(w protocol.ManagedWorktree) bool { return w.ID == rec.ID })
		} else if recp != nil {
			// Something was left behind: keep it for the user to inspect,
			// never attachable (ADR 0024).
			recp.AdminName, recp.Detail, recp.Unverified = admin, msg+"; the directory was kept for inspection", true
			recp.State = protocol.WorktreeUnattached
			refreshWorktreesIn(&next, detectAll([]protocol.ManagedWorktree{*recp}))
		}
		final = protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "worktree_failed", Message: msg}}
	case recp == nil:
		// The project was removed meanwhile; its records went with it.
		final = protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "not_found", Message: "the project was removed while its worktree was created; the directory " + plan.path + " was kept"}}
	default:
		recp.AdminName = admin
		recp.State = protocol.WorktreePresent
		final = e.attachLocked(&next, c, captured, recp)
	}
	next.Revision++
	final.Revision = next.Revision
	e.commitWorktreeReceiptLocked(next, c, final)
	return final, nil
}

func deref(r *protocol.Receipt) protocol.Receipt {
	if r == nil {
		return protocol.Receipt{}
	}
	return *r
}

// reserveWorktreeCommand answers a retry from its receipt, or reserves the
// command ID. attach reports a retry whose worktree exists but whose thread
// was never attached.
func (e *engine) reserveWorktreeCommand(ctx context.Context, c protocol.Command) (release func(), r *protocol.Receipt, attach bool, err error) {
	e.mu.Lock()
	gs := e.gitLocked()
	for {
		wait, busy := gs.inflight[c.ID]
		if !busy {
			break
		}
		e.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, nil, false, ctx.Err()
		}
		e.mu.Lock()
	}
	defer e.mu.Unlock()
	e.persistUnsavedGitLocked()
	if u, ok := gs.unsaved[c.ID]; ok {
		if !sameCommand(u.c, c) {
			return nil, nil, false, failure("identity_conflict", "command ID was already used with different content")
		}
		return nil, &u.r, false, nil
	}
	stored, err := e.store.Lookup(c)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return nil, nil, false, err
		}
		return nil, nil, false, failure("unknown_outcome_lookup", "the server could not read whether this request already ran; refresh before retrying")
	}
	if e.stopping {
		return nil, nil, false, failure("stopping", "server is shutting down")
	}
	if stored != nil {
		if stored.State == "accepted" {
			return nil, stored, false, nil
		}
		e.refreshWorktreesLocked()
		rec := worktreeByCommand(&e.snap, c.ID)
		if rec == nil || rec.State != protocol.WorktreeUnattached || rec.Unverified {
			return nil, stored, false, nil
		}
		attach = true
	}
	done := make(chan struct{})
	gs.inflight[c.ID] = done
	return func() {
		e.mu.Lock()
		delete(gs.inflight, c.ID)
		close(done)
		e.mu.Unlock()
	}, nil, attach, nil
}

// attachWorktree retries attaching the thread to the worktree the same
// command created.
func (e *engine) attachWorktree(c, captured protocol.Command) (protocol.Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshWorktreesLocked()
	next := clone(e.snap)
	rec := worktreeByCommand(&next, c.ID)
	if rec == nil || rec.State != protocol.WorktreeUnattached || rec.Unverified {
		return protocol.Receipt{}, failure("workspace_unavailable", "the worktree this request created is no longer available to attach")
	}
	final := e.attachLocked(&next, c, captured, rec)
	if final.State != "accepted" {
		return protocol.Receipt{}, final.Error
	}
	next.Revision++
	final.Revision = next.Revision
	e.commitWorktreeReceiptLocked(next, c, final)
	return final, nil
}

// attachLocked starts the thread in rec on next. On failure rec stays
// unattached with the reason; the receipt says why.
func (e *engine) attachLocked(next *protocol.Snapshot, c, captured protocol.Command, rec *protocol.ManagedWorktree) protocol.Receipt {
	if err := validateArtifactDelivery(next, captured); err != nil {
		return failedAttach(c, rec, err)
	}
	id, err := startThreadIn(next, captured, threadPlace{checkout: worktreeCheckout(*rec, rec.Path), worktreeID: rec.ID, explicit: true})
	if err != nil {
		return failedAttach(c, rec, err)
	}
	// startThreadIn replaced next's slices; rec is found again.
	rec = worktreeByID(next, rec.ID)
	rec.State, rec.Detail = protocol.WorktreePresent, ""
	e.startQueuedFixture(next, threadByID(next, id))
	return protocol.Receipt{ID: c.ID, State: "accepted", TargetID: id}
}

func failedAttach(c protocol.Command, rec *protocol.ManagedWorktree, err error) protocol.Receipt {
	pe := &protocol.Error{Code: "internal_error", Message: err.Error()}
	errors.As(err, &pe)
	rec.State = protocol.WorktreeUnattached
	rec.Detail = "the worktree was created but its thread could not start (" + pe.Message + "); retry the request or remove the worktree"
	return protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: pe.Code, Message: rec.Detail}}
}

// commitWorktreeReceiptLocked publishes next and records the final receipt,
// keeping it in memory for retries when storage fails (as Git writes do).
func (e *engine) commitWorktreeReceiptLocked(next protocol.Snapshot, c protocol.Command, r protocol.Receipt) {
	e.snap = next
	e.lastFlush = time.Now()
	e.publish()
	var err error
	for attempt := 0; attempt < gitSaveRetries; attempt++ {
		if err = e.store.SaveReceipt(e.snap, c, r); err == nil {
			break
		}
		e.mu.Unlock()
		time.Sleep(gitSaveRetryDelay)
		e.mu.Lock()
	}
	if err != nil {
		e.logf("worktree outcome not persisted", "command", c.ID, "error", err)
		e.gitLocked().unsaved[c.ID] = gitUnsaved{c: c, r: r}
		e.dirty = true
	}
	if r.State == "accepted" {
		e.afterCommit(c, r.TargetID)
	} else {
		e.rebalanceWritersAndFlushLocked()
	}
}

// dryRunStart validates the thread start on a copy (agent, settings,
// prompt, capacity, captured context) before anything is created.
func (e *engine) dryRunStart(captured protocol.Command, captureState protocol.Snapshot) error {
	if usesWorkspaceFiles(captured) {
		before, errBefore := captureRoot(captureState, captured)
		after, errAfter := captureRoot(e.snap, captured)
		if errBefore != nil || errAfter != nil || before != after {
			return failure("stale_workspace", "workspace changed while capturing context; review and send again")
		}
	}
	if err := validateArtifactDelivery(&e.snap, captured); err != nil {
		return err
	}
	trial := clone(e.snap)
	_, err := startThreadIn(&trial, captured, threadPlace{checkout: "/", explicit: true})
	return err
}

func projectByID(s *protocol.Snapshot, id string) *protocol.Project {
	for i := range s.Projects {
		if s.Projects[i].ID == id {
			return &s.Projects[i]
		}
	}
	return nil
}

// prepareWorktree validates the repository, branch, commit and target
// directory without the lock.
func prepareWorktree(ctx context.Context, project protocol.Project, root string, c protocol.Command) (*worktreePlan, error) {
	if strings.Contains(project.Path, "://") || !gitReadable(inspectWorkspace(ctx, project.Path)) {
		return nil, failure("not_git", "the project is not a readable Git checkout; worktrees need Git")
	}
	g, err := newGitReader(ctx, project.Path)
	if err != nil {
		return nil, err
	}
	w, err := newGitWriter(ctx, g)
	if err != nil {
		return nil, err
	}
	top := realPath(g.dir)
	rel, err := filepath.Rel(top, realPath(project.Path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, failure("not_supported", "the project folder is not inside its repository's working tree")
	}
	branch, oid := c.Workspace.Branch, c.Workspace.StartOid
	out, _, err := g.read(ctx, 4096, "check-ref-format", "--branch", branch)
	if err != nil || strings.TrimSpace(string(out)) != branch {
		return nil, failure("invalid_branch", "that is not a valid new branch name")
	}
	if branchExists(ctx, w, branch) {
		return nil, failure("branch_exists", "branch "+branch+" already exists; worktrees always start a new branch")
	}
	if kind, _, err := g.read(ctx, 64, "cat-file", "-t", oid); err != nil || strings.TrimSpace(string(kind)) != "commit" {
		return nil, failure("invalid_start", "the starting commit does not exist in this repository")
	}
	if rel != "." {
		if kind, _, err := g.read(ctx, 64, "cat-file", "-t", oid+":"+filepath.ToSlash(rel)); err != nil || strings.TrimSpace(string(kind)) != "tree" {
			return nil, failure("project_folder_absent", "the project folder "+filepath.ToSlash(rel)+" does not exist at the starting commit")
		}
	}
	parent := filepath.Join(root, c.ProjectID)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, failure("unavailable", "the worktree directory could not be prepared: "+err.Error())
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, failure("unavailable", "the worktree directory could not be resolved")
	}
	path := filepath.Join(parent, worktreeDirName(branch, c.ID))
	if pathsOverlap(path, top) || pathsOverlap(path, w.commonDir) {
		return nil, failure("not_supported", "worktrees are never created inside the repository; the application home is inside it")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, failure("path_exists", "the worktree directory "+path+" already exists")
	}
	return &worktreePlan{project: project, top: top, common: w.commonDir, rel: rel, path: path, branch: branch, oid: oid, writer: w}, nil
}

func branchExists(ctx context.Context, w *gitWriter, branch string) bool {
	_, _, err := w.run(ctx, nil, false, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// verifyWorktree checks what `git worktree add` left: the directory is a
// worktree on the new branch at the starting commit. It returns the admin
// entry's name.
func verifyWorktree(ctx context.Context, plan *worktreePlan) (string, error) {
	link, ok := gitdirLink(filepath.Join(plan.path, ".git"), plan.path)
	if !ok {
		return "", errors.New("the new directory is not a worktree")
	}
	admin := filepath.Base(link)
	if b, err := os.ReadFile(filepath.Join(link, "locked")); err == nil && strings.Contains(string(b), "initializing") {
		return admin, errors.New("git did not finish initializing the worktree")
	}
	g, err := newGitReader(ctx, plan.path)
	if err != nil {
		return admin, errors.New("the new worktree cannot be read")
	}
	head, _, err1 := g.read(ctx, 4096, "rev-parse", "HEAD")
	ref, _, err2 := g.read(ctx, 4096, "symbolic-ref", "HEAD")
	if err1 != nil || err2 != nil || strings.TrimSpace(string(head)) != plan.oid || strings.TrimSpace(string(ref)) != "refs/heads/"+plan.branch {
		return admin, errors.New("the new worktree is not on the new branch at the starting commit")
	}
	return admin, nil
}

// gitSummary keeps the last lines of Git's output.
func gitSummary(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.ToValidUTF8(strings.Join(lines, " "), "")
}

// ---- State commands: relocate and forget ----

// applyWorktreeState handles worktree.relocate and worktree.forget on next
// (e.mu held).
func (e *engine) applyWorktreeState(next *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.Worktree == nil || c.Worktree.ID == "" {
		return "", failure("invalid", "name the worktree")
	}
	rec := worktreeByID(next, c.Worktree.ID)
	if rec == nil {
		return "", failure("not_found", "the worktree is not managed by the application")
	}
	state, moved := detectWorktree(*rec)
	rec.State, rec.MovedTo = state, moved
	switch c.Kind {
	case "worktree.relocate":
		if state != protocol.WorktreeMoved {
			return "", failure("not_moved", "the worktree is "+state+"; only a worktree Git registers elsewhere can be relocated")
		}
		for _, t := range worktreeThreads(next, rec.ID) {
			if err := e.threadQuietLocked(next, t); err != nil {
				return "", err
			}
		}
		// Open documents and running Git changes still name the old path.
		for _, d := range next.Documents {
			if d.State != protocol.DocumentStateDeleted && pathsOverlap(d.Checkout, rec.Path) {
				return "", failure("documents_open", d.Path+" is open from the old location; close the worktree's documents first")
			}
		}
		for id, h := range e.gitLocked().holders {
			if pathsOverlap(h.top, rec.Path) || h.common == rec.CommonDir {
				return "", failure("git_busy", "a Git change ("+id+") is running in this repository; try again when it finishes")
			}
		}
		old := rec.Path
		rec.Path, rec.MovedTo, rec.State = moved, "", protocol.WorktreePresent
		for _, t := range worktreeThreads(next, rec.ID) {
			// The agent session's working directory was the old path. A
			// resolution job works at the worktree's top, others in the
			// project's folder inside it: keep each one's place.
			rel, err := filepath.Rel(old, t.Checkout)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				rel = filepath.FromSlash(rec.RelPath)
			}
			t.Checkout, t.SessionID = filepath.Join(moved, rel), ""
		}
		refreshWorktreesIn(next, detectAll([]protocol.ManagedWorktree{*rec}))
		return rec.ID, nil
	case "worktree.forget":
		switch state {
		case protocol.WorktreePresent, protocol.WorktreeUnattached, protocol.WorktreeCreating:
			return "", failure("worktree_present", "the worktree still exists; remove it instead, or forget it once it is missing")
		}
		id := rec.ID
		next.Worktrees = slices.DeleteFunc(next.Worktrees, func(w protocol.ManagedWorktree) bool { return w.ID == id })
		if len(next.Worktrees) == 0 {
			next.Worktrees = nil
		}
		return id, nil
	}
	return "", failure("invalid", "unsupported worktree command")
}

// threadQuietLocked refuses while t works or has a live terminal.
func (e *engine) threadQuietLocked(s *protocol.Snapshot, t *protocol.Thread) error {
	if activeTurn(t) || len(t.Queue) > 0 && t.State == "running" {
		return failure("thread_busy", "thread "+t.Title+" is working; stop it first")
	}
	if _, claiming := e.claiming[t.ID]; claiming {
		return failure("thread_busy", "thread "+t.Title+" is starting a turn; try again")
	}
	for _, term := range s.Terminals {
		if term.ThreadID == t.ID && term.State != protocol.TerminalStateEnded {
			return failure("terminals_open", "thread "+t.Title+" has open terminals; close them first")
		}
	}
	return nil
}

// ---- Removal and prune ----

// removalBlockersLocked lists why rec cannot be removed now (code: text).
func (e *engine) removalBlockersLocked(rec protocol.ManagedWorktree) []string {
	var out []string
	add := func(code, text string) { out = append(out, code+": "+text) }
	switch rec.State {
	case protocol.WorktreePresent, protocol.WorktreeUnattached:
	default:
		add("workspace_unavailable", "the worktree is "+rec.State+"; only a present worktree can be removed (forget or prune a missing one)")
		return out
	}
	for _, t := range worktreeThreads(&e.snap, rec.ID) {
		if !t.Closed {
			add("worktree_in_use", "thread "+t.Title+" is open; close it first")
		}
		for _, term := range e.snap.Terminals {
			if term.ThreadID == t.ID && term.State != protocol.TerminalStateEnded {
				add("terminals_open", "thread "+t.Title+" has open terminals")
				break
			}
		}
	}
	for _, term := range e.snap.Terminals {
		if term.State != protocol.TerminalStateEnded && term.Dir != "" && pathsOverlap(term.Dir, rec.Path) && !slices.ContainsFunc(worktreeThreads(&e.snap, rec.ID), func(t *protocol.Thread) bool { return t.ID == term.ThreadID }) {
			add("terminals_open", "a terminal started in the worktree is open")
		}
	}
	for _, d := range e.snap.Documents {
		if pathsOverlap(d.Checkout, rec.Path) {
			switch d.State {
			case protocol.DocumentStateSaved, protocol.DocumentStateReadOnly, protocol.DocumentStateDeleted:
			default:
				add("documents_unsaved", d.Path+" has changes not saved to disk")
			}
		}
	}
	for id, h := range e.gitLocked().holders {
		if pathsOverlap(h.top, rec.Path) || h.common == rec.CommonDir {
			add("git_busy", "a Git change ("+id+") is running in this repository")
		}
	}
	return out
}

// removalPreview reads the worktree with Git: HEAD, cleanliness, ignored
// entries and an operation in progress.
func removalPreview(ctx context.Context, rec protocol.ManagedWorktree, blockers []string) (out protocol.WorktreeRemoval) {
	out = protocol.WorktreeRemoval{ID: rec.ID, Path: rec.Path, Branch: rec.Branch, Blockers: blockers}
	if out.Blockers == nil {
		out.Blockers = []string{}
	}
	h := sha256.New()
	fmt.Fprintf(h, "worktree-removal-v1\x00%s\x00%s\x00%s\x00", rec.ID, rec.Path, rec.Branch)
	defer func() {
		fmt.Fprintf(h, "%s\x00%d\x00%v\x00%d", out.Head, out.Ignored, out.IgnoredIncomplete, len(out.Blockers))
		out.Fingerprint = hex.EncodeToString(h.Sum(nil))[:32]
	}()
	if len(blockers) > 0 && strings.HasPrefix(blockers[0], "workspace_unavailable") {
		return out
	}
	g, err := newGitReader(ctx, rec.Path)
	if err != nil {
		out.Blockers = append(out.Blockers, "workspace_unavailable: the worktree cannot be read with Git")
		return out
	}
	if head, _, err := g.read(ctx, 4096, "rev-parse", "HEAD"); err == nil {
		out.Head = strings.TrimSpace(string(head))
	}
	if dir, _, err := g.read(ctx, 4096, "rev-parse", "--absolute-git-dir"); err == nil {
		if op := gitDirOperation(strings.TrimSpace(string(dir))); op != "" {
			out.Blockers = append(out.Blockers, "operation_in_progress: a "+op+" is in progress in the worktree")
		}
	}
	status, truncated, err := g.read(ctx, gitStatusMaxBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		out.Blockers = append(out.Blockers, "workspace_unavailable: the worktree's status cannot be read")
		return out
	}
	out.IgnoredIncomplete = truncated
	dirty := 0
	for _, entry := range strings.Split(string(status), "\x00") {
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "!! "):
			out.Ignored++
			h.Write([]byte(entry + "\x00"))
			if len(out.IgnoredSample) < worktreeIgnoredMax {
				out.IgnoredSample = append(out.IgnoredSample, strings.ToValidUTF8(entry[3:], "�"))
			}
		case len(entry) > 3 && entry[2] == ' ':
			dirty++
		}
	}
	if dirty > 0 || truncated {
		out.Blockers = append(out.Blockers, fmt.Sprintf("worktree_dirty: the worktree has %d uncommitted or untracked changes; commit, stash or discard them first", dirty))
	}
	return out
}

func blockerError(blockers []string) error {
	code, text, _ := strings.Cut(blockers[0], ": ")
	msgs := make([]string, 0, len(blockers))
	for _, b := range blockers {
		_, t, _ := strings.Cut(b, ": ")
		msgs = append(msgs, t)
	}
	if len(msgs) > 1 {
		text = strings.Join(msgs, "; ")
	}
	return failure(code, text)
}

// worktreeRemoval is GET /v1/worktrees/removal?id=.
func (e *engine) worktreeRemoval(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitRequestBudget)
	defer cancel()
	out, err := e.removalOf(ctx, r.URL.Query().Get("id"))
	if err != nil {
		gitFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *engine) removalOf(ctx context.Context, id string) (protocol.WorktreeRemoval, error) {
	e.mu.Lock()
	e.refreshWorktreesLocked()
	rec := worktreeByID(&e.snap, id)
	if rec == nil {
		e.mu.Unlock()
		return protocol.WorktreeRemoval{}, failure("not_found", "the worktree is not managed by the application")
	}
	copied := *rec
	blockers := e.removalBlockersLocked(copied)
	e.mu.Unlock()
	return removalPreview(ctx, copied, blockers), nil
}

// worktreePruneRead is GET /v1/worktrees/prune?project_id=.
func (e *engine) worktreePruneRead(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitRequestBudget)
	defer cancel()
	out, _, err := e.pruneOf(ctx, r.URL.Query().Get("project_id"))
	if err != nil {
		gitFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (e *engine) pruneOf(ctx context.Context, projectID string) (protocol.WorktreePrune, *gitWriter, error) {
	e.mu.Lock()
	p := projectByID(&e.snap, projectID)
	var path string
	if p != nil {
		path = p.Path
	}
	e.mu.Unlock()
	if p == nil {
		return protocol.WorktreePrune{}, nil, failure("not_found", "project does not exist")
	}
	if strings.Contains(path, "://") || !gitReadable(inspectWorkspace(ctx, path)) {
		return protocol.WorktreePrune{}, nil, failure("not_git", "the project is not a readable Git checkout")
	}
	g, err := newGitReader(ctx, path)
	if err != nil {
		return protocol.WorktreePrune{}, nil, err
	}
	w, err := newGitWriter(ctx, g)
	if err != nil {
		return protocol.WorktreePrune{}, nil, err
	}
	out, err := pruneListing(ctx, w, projectID)
	if err != nil {
		return protocol.WorktreePrune{}, nil, err
	}
	return out, w, nil
}

// pruneListing is `git worktree prune --dry-run` and its fingerprint.
func pruneListing(ctx context.Context, w *gitWriter, projectID string) (protocol.WorktreePrune, error) {
	_, output, err := w.run(ctx, nil, true, "worktree", "prune", "--dry-run", "--verbose")
	if err != nil {
		return protocol.WorktreePrune{}, failure("unavailable", "Git could not list stale worktrees")
	}
	out := protocol.WorktreePrune{ProjectID: projectID, Entries: []string{}}
	h := sha256.New()
	h.Write([]byte("worktree-prune-v1\x00" + w.commonDir + "\x00"))
	for _, line := range strings.Split(string(output.bytes()), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out.Entries = append(out.Entries, strings.ToValidUTF8(line, "�"))
			h.Write([]byte(line + "\x00"))
		}
	}
	if len(out.Entries) > 0 {
		out.Fingerprint = hex.EncodeToString(h.Sum(nil))[:32]
	}
	return out, nil
}

// worktreeGitCommand runs worktree.remove and worktree.prune: previewed,
// confirmed by fingerprint, holding the repository's Git slot while Git
// runs, and recorded with their receipt.
func (e *engine) worktreeGitCommand(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	if c.Worktree == nil || c.Worktree.Confirm == "" {
		return protocol.Receipt{}, failure("confirmation_required", "confirm the previewed change first")
	}
	release, r, _, err := e.reserveWorktreeCommand(ctx, c)
	if err != nil || r != nil {
		return deref(r), err
	}
	defer release()
	prepCtx, cancel := context.WithTimeout(ctx, gitWriteBudget)
	defer cancel()

	var (
		w      *gitWriter
		top    string
		common string
		args   []string
		recID  string
	)
	switch c.Kind {
	case "worktree.remove":
		if c.Worktree.ID == "" {
			return protocol.Receipt{}, failure("invalid", "name the worktree")
		}
		preview, err := e.removalOf(prepCtx, c.Worktree.ID)
		if err != nil {
			return protocol.Receipt{}, err
		}
		if preview.Fingerprint != c.Worktree.Confirm {
			return protocol.Receipt{}, failure("stale_confirmation", "the worktree changed since the removal was previewed; review it again")
		}
		if len(preview.Blockers) > 0 {
			return protocol.Receipt{}, blockerError(preview.Blockers)
		}
		// Git runs from the project's repository, not from inside the
		// worktree it deletes; a missing project falls back to the worktree.
		from := preview.Path
		e.mu.Lock()
		if rec := worktreeByID(&e.snap, preview.ID); rec != nil {
			if p := projectByID(&e.snap, rec.ProjectID); p != nil && !strings.Contains(p.Path, "://") {
				from = p.Path
			}
		}
		e.mu.Unlock()
		g, err := newGitReader(prepCtx, from)
		if err != nil {
			return protocol.Receipt{}, err
		}
		if w, err = newGitWriter(prepCtx, g); err != nil {
			return protocol.Receipt{}, err
		}
		if w.commonDir != realPath(e.worktreeCommon(preview.ID)) {
			return protocol.Receipt{}, failure("not_supported", "the project's repository is not the worktree's repository")
		}
		recID, top, common = preview.ID, preview.Path, w.commonDir
		args = []string{"worktree", "remove", preview.Path}
	case "worktree.prune":
		preview, pw, err := e.pruneOf(prepCtx, c.ProjectID)
		if err != nil {
			return protocol.Receipt{}, err
		}
		if len(preview.Entries) == 0 {
			return protocol.Receipt{}, failure("nothing_to_prune", "Git lists no stale worktrees")
		}
		if preview.Fingerprint != c.Worktree.Confirm {
			return protocol.Receipt{}, failure("stale_confirmation", "the stale worktrees changed since they were listed; review them again")
		}
		w, top, common = pw, pw.top, pw.commonDir
		args = []string{"worktree", "prune", "--verbose"}
	default:
		return protocol.Receipt{}, failure("invalid", "unsupported worktree command")
	}

	e.mu.Lock()
	gs := e.gitLocked()
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	if recID != "" {
		rec := worktreeByID(&e.snap, recID)
		if rec == nil {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("not_found", "the worktree is not managed by the application")
		}
		if blockers := e.removalBlockersLocked(*rec); len(blockers) > 0 {
			e.mu.Unlock()
			return protocol.Receipt{}, blockerError(blockers)
		}
	}
	for id, h := range gs.holders {
		if h.common == common || pathsOverlap(h.top, top) {
			e.mu.Unlock()
			return protocol.Receipt{}, failure("git_busy", "another Git change ("+id+") is running in this repository")
		}
	}
	projectID := c.ProjectID
	if recID != "" {
		// Project removal is refused while its worktree is being removed.
		if rec := worktreeByID(&e.snap, recID); rec != nil {
			projectID = rec.ProjectID
		}
	}
	gs.holders[c.ID] = gitHold{top: top, common: common, projectID: projectID, lease: recID != "", refOp: true}
	e.rebalanceWritersAndFlushLocked()
	gs.wg.Add(1)
	e.mu.Unlock()
	defer gs.wg.Done()

	runCtx, cancelRun := context.WithTimeout(context.WithoutCancel(ctx), gitWriteBudget)
	defer cancelRun()
	if c.Kind == "worktree.prune" {
		// The confirmed listing is compared with a fresh one taken while
		// this command holds the repository's Git slot.
		if fresh, err := pruneListing(runCtx, w, c.ProjectID); err != nil || fresh.Fingerprint != c.Worktree.Confirm {
			e.mu.Lock()
			delete(gs.holders, c.ID)
			e.rebalanceWritersAndFlushLocked()
			e.mu.Unlock()
			if err != nil {
				return protocol.Receipt{}, err
			}
			return protocol.Receipt{}, failure("stale_confirmation", "the stale worktrees changed since they were listed; review them again")
		}
	}
	_, output, runErr := w.run(runCtx, nil, true, args...)

	e.mu.Lock()
	defer e.mu.Unlock()
	delete(gs.holders, c.ID)
	next := clone(e.snap)
	var final protocol.Receipt
	if runErr != nil {
		msg := "Git did not complete the change"
		if text := strings.TrimSpace(string(output.bytes())); text != "" {
			msg += ": " + gitSummary(text)
		}
		final = protocol.Receipt{ID: c.ID, State: "failed", Error: &protocol.Error{Code: "worktree_failed", Message: msg}}
	} else {
		final = protocol.Receipt{ID: c.ID, State: "accepted", TargetID: recID}
		if rec := worktreeByID(&next, recID); rec != nil {
			rec.State, rec.MovedTo, rec.Detail = protocol.WorktreeRemoved, "", ""
		}
	}
	refreshWorktreesIn(&next, detectAll(next.Worktrees))
	next.Revision++
	final.Revision = next.Revision
	if err := e.store.Save(next, &c, &final); err != nil {
		e.logf("worktree outcome not persisted", "command", c.ID, "error", err)
		e.gitLocked().unsaved[c.ID] = gitUnsaved{c: c, r: final}
		e.dirty = true
	}
	e.snap = next
	e.lastFlush = time.Now()
	e.publish()
	e.rebalanceWritersAndFlushLocked()
	return final, nil
}

func (e *engine) worktreeCommon(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec := worktreeByID(&e.snap, id); rec != nil {
		return rec.CommonDir
	}
	return ""
}
