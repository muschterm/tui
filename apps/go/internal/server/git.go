package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Read-only Git surface (docs/design/git-client.md). Handlers resolve a
// project or thread to its checkout exactly like GET /v1/workspace, then run
// bounded Git reads outside the engine lock under one request deadline.
//
// Error codes: invalid (400), not_found (404), not_git and not_diffable (409),
// unavailable (503).

const (
	gitRequestBudget   = 10 * time.Second
	gitStatusMaxBytes  = 4 << 20
	gitStatusMaxItems  = 2000
	gitDiffMaxBytes    = 512 << 10
	gitLogDefaultLimit = 50
	gitLogMaxLimit     = 200
	gitLogMaxBytes     = 1 << 20
	gitStderrMaxBytes  = 8 << 10
)

// errGitNotRepository marks a target that is not a usable Git checkout.
var errGitNotRepository = errors.New("not a git checkout")

// gitReader runs read-only git commands in one checkout.
//
// Policy. Unlike inspectWorkspace, which needs only plumbing and ignores all
// user config, status/diff/log honor the user's SYSTEM and GLOBAL config (LFS
// and other clean filters, core.autocrlf, rename detection) so results match
// the user's own git. Repository-local and worktree config, however, may have
// been written by a sandboxed agent while this server runs unsandboxed, so no
// read may execute a command defined only there:
//   - every filter.<name> driver with any key in local/worktree scope
//     (including keys pulled in by a local include) is neutralized with
//     empty clean/smudge/process and required=false, which git treats as "no
//     filter" (verified: the configured command never runs, status is sane);
//     a driver defined purely in global/system scope still runs. The
//     overrides travel as GIT_CONFIG_COUNT/KEY_n/VALUE_n so a crafted
//     subsection name ("a=b", "x.process=cmd #") is taken verbatim and can
//     neither dodge nor inject configuration, as it could through -c. A name
//     that cannot be represented (NUL or newline) fails the read closed. A
//     single local key disables that driver even when it is defined globally
//     (e.g. `git lfs install --local` makes LFS files read raw here);
//   - global drivers still run and may read repository files (.lfsconfig);
//   - gitfile, alternates, replace refs and core.worktree redirection are
//     honored as Git configures them, so a checkout can present another
//     repository's data read-only; accepted for this slice;
//   - all reads run at the repository toplevel, because status paths are
//     toplevel-relative even when a project is registered at a subdirectory;
//   - fsmonitor, pager, external diff, textconv and signature verification
//     are disabled outright; hooks never run for these commands;
//   - status, diff and show use --ignore-submodules=dirty, so no git runs
//     inside submodules (whose local config is never scanned) (submodule commit changes are reported; dirty submodule
//     worktrees are not);
//   - GIT_NO_LAZY_FETCH=1 stops partial clones from contacting promisor
//     remotes; missing objects surface as unavailable. Nothing here fetches.
//
// Inherited GIT_* variables (GIT_DIR, GIT_INDEX_FILE, GIT_CONFIG_PARAMETERS,
// ...) are removed, optional index refreshes are suppressed so reads never
// take index.lock, stdin is empty, prompts are disabled and pathspecs are
// literal. git runs in its own process group, which is killed on output cap,
// deadline or cancellation so filter children are not orphaned.
type gitReader struct {
	dir string
	env []string
}

func newGitReader(ctx context.Context, dir string) (*gitReader, error) {
	g := &gitReader{dir: dir}
	top, truncated, err := g.read(ctx, 64<<10, "rev-parse", "--show-toplevel")
	if err != nil || truncated || len(top) < 2 {
		return nil, failure("unavailable", "Git toplevel could not be resolved")
	}
	g.dir = strings.TrimSuffix(string(top), "\n")
	out, truncated, err := g.read(ctx, 1<<20, "config", "--list", "--show-scope", "-z")
	if err != nil || truncated {
		return nil, failure("unavailable", "Git configuration could not be read")
	}
	seen := map[string]bool{}
	var names []string
	// -z records are "scope\0key\nvalue\0".
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		scope := fields[i]
		key, _, _ := strings.Cut(fields[i+1], "\n")
		if scope != "local" && scope != "worktree" {
			continue
		}
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "filter.") {
			continue
		}
		last := strings.LastIndexByte(key, '.')
		if last <= len("filter.") {
			continue
		}
		name := key[len("filter."):last]
		if strings.ContainsAny(name, "\x00\n") {
			return nil, failure("unavailable", "unsafe repository filter configuration")
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	g.env = gitFilterOverrides(names)
	return g, nil
}

// gitFilterOverrides neutralizes each named filter through the environment
// config channel, whose keys git takes verbatim.
func gitFilterOverrides(names []string) []string {
	var env []string
	n := 0
	for _, name := range names {
		for _, kv := range [][2]string{{"clean", ""}, {"smudge", ""}, {"process", ""}, {"required", "false"}} {
			env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=filter.%s.%s", n, name, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, kv[1]))
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
}

// read runs one command with output capped at limit bytes; reaching the cap
// kills git and reports truncated rather than an error.
func (g *gitReader) read(ctx context.Context, limit int, args ...string) (out []byte, truncated bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	base := []string{
		"--no-pager", "--no-optional-locks", "--literal-pathspecs", "-C", g.dir,
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "color.ui=false",
		"-c", "log.showSignature=false", "-c", "diff.external=", "-c", "core.bigFileThreshold=8m",
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "PAGER=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_PAGER=cat", "PAGER=cat")
	cmd.Env = append(cmd.Env, g.env...)
	configureGitProcess(cmd)
	w := &cappedOutput{limit: limit, full: cancel}
	stderr := &cappedOutput{limit: gitStderrMaxBytes, drain: true}
	cmd.Stdout, cmd.Stderr = w, stderr
	cmd.WaitDelay = 100 * time.Millisecond
	err = cmd.Run()
	if w.truncated() {
		return w.bytes(), true, nil
	}
	if ctx.Err() != nil {
		return nil, false, failure("unavailable", "Git read cancelled or timed out")
	}
	if err != nil {
		if msg := string(stderr.bytes()); strings.Contains(msg, "lazy fetching disabled") || strings.Contains(msg, "promisor remote") {
			return nil, false, failure("unavailable", "object not available locally (partial clone)")
		}
	}
	return w.bytes(), false, err
}

// cappedOutput keeps the first limit bytes and cancels git once more arrives.
type cappedOutput struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
	full  func()
	drain bool // keep the prefix, silently discard the rest (stderr)
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.over {
		if c.drain {
			return len(p), nil
		}
		return 0, errors.New("output cap reached")
	}
	if room := c.limit - c.buf.Len(); len(p) > room {
		c.buf.Write(p[:room])
		c.over = true
		if c.drain {
			return len(p), nil
		}
		c.full()
		return 0, errors.New("output cap reached")
	}
	return c.buf.Write(p)
}

func (c *cappedOutput) truncated() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.over }
func (c *cappedOutput) bytes() []byte   { c.mu.Lock(); defer c.mu.Unlock(); return c.buf.Bytes() }

// gitTarget resolves the request's project_id or thread_id to a checkout path.
func (e *engine) gitTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, threadID := r.URL.Query().Get("project_id"), r.URL.Query().Get("thread_id")
	if (projectID == "") == (threadID == "") {
		writeJSON(w, http.StatusBadRequest, failure("invalid", "select exactly one project or thread"))
		return "", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range e.snap.Threads {
		if threadID != "" && t.ID == threadID {
			return t.Checkout, true
		}
	}
	for _, p := range e.snap.Projects {
		if projectID != "" && p.ID == projectID {
			return p.Path, true
		}
	}
	writeJSON(w, http.StatusNotFound, failure("not_found", "workspace target not found"))
	return "", false
}

// gitReadable reports whether a workspace observation has a repository to read.
func gitReadable(info protocol.WorkspaceInfo) bool {
	return info.State == "branch" || info.State == "detached" || info.State == "unborn"
}

func gitFailure(w http.ResponseWriter, err error) {
	var pe *protocol.Error
	switch {
	case errors.Is(err, errGitNotRepository):
		writeJSON(w, http.StatusConflict, failure("not_git", "workspace is not a readable Git checkout"))
	case errors.As(err, &pe) && pe.Code == "invalid":
		writeJSON(w, http.StatusBadRequest, pe)
	case errors.As(err, &pe) && pe.Code == "not_found":
		writeJSON(w, http.StatusNotFound, pe)
	case errors.As(err, &pe) && pe.Code == "not_diffable":
		writeJSON(w, http.StatusConflict, pe)
	case errors.As(err, &pe):
		writeJSON(w, http.StatusServiceUnavailable, pe)
	default:
		writeJSON(w, http.StatusServiceUnavailable, failure("unavailable", "Git read failed"))
	}
}

// gitHandle applies the per-request deadline shared by every Git read.
func (e *engine) gitHandle(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, dir string) (any, error)) {
	dir, ok := e.gitTarget(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitRequestBudget)
	defer cancel()
	result, err := read(ctx, dir)
	if err != nil {
		gitFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (e *engine) gitStatus(w http.ResponseWriter, r *http.Request) {
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) { return readGitStatus(ctx, dir) })
}

func (e *engine) gitDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readGitDiff(ctx, dir, q.Get("path"), q.Get("group"))
	})
}

func (e *engine) gitLog(w http.ResponseWriter, r *http.Request) {
	limit := gitLogDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			gitFailure(w, failure("invalid", "limit must be a positive integer"))
			return
		}
		limit = min(n, gitLogMaxLimit)
	}
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) { return readGitLog(ctx, dir, limit) })
}

func (e *engine) gitShow(w http.ResponseWriter, r *http.Request) {
	commit := r.URL.Query().Get("commit")
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) { return readGitShow(ctx, dir, commit) })
}

func gitError(err error) error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return err
	}
	return failure("unavailable", "Git read failed")
}

// statusArgs is the shared porcelain status invocation.
func statusArgs(untracked string) []string {
	return []string{"status", "--porcelain=v2", "-z", "--untracked-files=" + untracked, "--find-renames", "--ignore-submodules=dirty"}
}

// readGitStatus returns status for a readable checkout, or only the workspace
// observation (with no entries) for fixture, non-Git and unavailable targets.
func readGitStatus(ctx context.Context, dir string) (protocol.GitStatus, error) {
	status := protocol.GitStatus{Workspace: inspectWorkspace(ctx, dir), Entries: []protocol.GitStatusEntry{}}
	if !gitReadable(status.Workspace) {
		return status, nil
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return status, err
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, append(statusArgs("all"), "--branch")...)
	if err != nil {
		return status, gitError(err)
	}
	parseGitStatus(out, truncated, gitStatusMaxItems, &status)
	status.Operation = g.operation(ctx)
	return status, nil
}

// parseGitStatus parses `status --porcelain=v2 -z [--branch]`. When the
// output was truncated the final partial record is dropped. maxItems <= 0
// means unlimited.
func parseGitStatus(out []byte, truncated bool, maxItems int, status *protocol.GitStatus) {
	records := strings.Split(string(out), "\x00")
	if truncated {
		status.Truncated = true
	}
	// The final element is either empty (NUL terminator) or a partial record.
	records = records[:len(records)-1]
	add := func(e protocol.GitStatusEntry) bool {
		if maxItems > 0 && len(status.Entries) >= maxItems {
			status.Truncated = true
			return false
		}
		status.Entries = append(status.Entries, e)
		return true
	}
	for i := 0; i < len(records); i++ {
		rec := records[i]
		switch {
		case strings.HasPrefix(rec, "# branch.head "):
			if head := strings.TrimPrefix(rec, "# branch.head "); head != "(detached)" {
				status.Branch = head
			}
		case strings.HasPrefix(rec, "# branch.upstream "):
			status.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
		case strings.HasPrefix(rec, "# branch.ab "):
			var a, b int
			if f := strings.Fields(strings.TrimPrefix(rec, "# branch.ab ")); len(f) == 2 {
				a, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				b, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
			status.Ahead, status.Behind = a, b
		case strings.HasPrefix(rec, "1 "), strings.HasPrefix(rec, "2 "):
			n := 9
			if rec[0] == '2' {
				n = 10
			}
			f := strings.SplitN(rec, " ", n)
			if len(f) != n || len(f[1]) != 2 {
				continue
			}
			e := protocol.GitStatusEntry{Path: f[n-1], Index: f[1][:1], Worktree: f[1][1:], Submodule: strings.HasPrefix(f[2], "S")}
			orig := ""
			if rec[0] == '2' {
				if i+1 >= len(records) {
					continue
				}
				i++
				orig = records[i]
			}
			if e.Index != "." {
				staged := e
				staged.Group, staged.OrigPath = protocol.GitGroupStaged, orig
				if !add(staged) {
					return
				}
			}
			if e.Worktree != "." {
				e.Group = protocol.GitGroupUnstaged
				if !add(e) {
					return
				}
			}
		case strings.HasPrefix(rec, "u "):
			f := strings.SplitN(rec, " ", 11)
			if len(f) != 11 || len(f[1]) != 2 {
				continue
			}
			if !add(protocol.GitStatusEntry{Path: f[10], Index: f[1][:1], Worktree: f[1][1:], Group: protocol.GitGroupConflicted, Submodule: strings.HasPrefix(f[2], "S")}) {
				return
			}
		case strings.HasPrefix(rec, "? "):
			if !add(protocol.GitStatusEntry{Path: rec[2:], Index: "?", Worktree: "?", Group: protocol.GitGroupUntracked}) {
				return
			}
		}
	}
}

// operation detects an in-progress operation from per-worktree git-dir
// markers without running any command that could alter it.
func (g *gitReader) operation(ctx context.Context) string {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(string(out))
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(gitDir, name)); return err == nil }
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return "rebase"
	case exists("MERGE_HEAD"):
		return "merge"
	case exists("CHERRY_PICK_HEAD"):
		return "cherry-pick"
	case exists("REVERT_HEAD"):
		return "revert"
	case exists("BISECT_LOG"):
		return "bisect"
	}
	return ""
}

// validGitPath accepts only a clean relative slash path inside the checkout
// and outside repository metadata. It is a syntactic gate; the path must also
// currently appear in status for the requested group.
func validGitPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || filepath.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

var gitDiffPrefixes = []string{"--no-ext-diff", "--no-textconv", "--no-color", "--no-relative", "--find-renames", "--ignore-submodules=dirty", "--src-prefix=a/", "--dst-prefix=b/"}

// readGitDiff renders one status entry. The path must be present in the
// checkout's current status under the requested group, which prevents the
// endpoint from being used to read arbitrary files. An untracked directory
// entry (a nested repository, reported as "dir/") is not_diffable.
func readGitDiff(ctx context.Context, dir, p, group string) (protocol.GitDiff, error) {
	diff := protocol.GitDiff{Path: p, Group: group}
	switch group {
	case protocol.GitGroupStaged, protocol.GitGroupUnstaged, protocol.GitGroupUntracked, protocol.GitGroupConflicted:
	default:
		return diff, failure("invalid", "group must be staged, unstaged, untracked or conflicted")
	}
	nested := group == protocol.GitGroupUntracked && strings.HasSuffix(p, "/") && validGitPath(strings.TrimSuffix(p, "/"))
	if !nested && !validGitPath(p) {
		return diff, failure("invalid", "path must be a relative checkout path outside .git")
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return diff, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return diff, err
	}
	// Staged renames are only detected when both sides are in scope, so the
	// staged lookup reads unscoped status without untracked files and without
	// the entry cap; the byte cap still bounds it.
	args := statusArgs("all")
	if group == protocol.GitGroupStaged {
		args = statusArgs("no")
	} else {
		args = append(args, "--", strings.TrimSuffix(p, "/"))
	}
	out, truncated, err := g.read(ctx, gitStatusMaxBytes, args...)
	if err != nil {
		return diff, gitError(err)
	}
	var scoped protocol.GitStatus
	parseGitStatus(out, truncated, 0, &scoped)
	var entry *protocol.GitStatusEntry
	for i := range scoped.Entries {
		if scoped.Entries[i].Path == p && scoped.Entries[i].Group == group {
			entry = &scoped.Entries[i]
		}
	}
	switch {
	case entry == nil && truncated:
		return diff, failure("unavailable", "too many changes to locate this path")
	case entry == nil:
		return diff, failure("not_found", "path has no "+group+" change in current status")
	case nested:
		return diff, failure("not_diffable", "untracked nested repository cannot be diffed")
	case group == protocol.GitGroupUntracked:
		return untrackedDiff(g.dir, p)
	}
	var cmd []string
	switch group {
	case protocol.GitGroupStaged:
		cmd = append(append([]string{"diff", "--cached"}, gitDiffPrefixes...), "--", p)
		if entry.OrigPath != "" {
			cmd = append(cmd, entry.OrigPath)
		}
	default:
		cmd = append(append([]string{"diff"}, gitDiffPrefixes...), "--", p)
	}
	text, truncated, err := g.read(ctx, gitDiffMaxBytes, cmd...)
	if err != nil {
		return diff, gitError(err)
	}
	diff.Text, diff.Truncated, diff.Binary = boundedText(text, truncated), truncated, gitBinary(text)
	diff.Bytes = len(diff.Text)
	return diff, nil
}

// untrackedDiff reads an untracked file without following any symlink below
// the checkout root and synthesizes a new-file patch. A final-component
// symlink is shown as its target text with mode 120000, as git does; FIFOs,
// devices and sockets are not_diffable and never block.
func untrackedDiff(dir, p string) (protocol.GitDiff, error) {
	diff := protocol.GitDiff{Path: p, Group: protocol.GitGroupUntracked}
	file, err := readUntracked(dir, p, gitDiffMaxBytes)
	if err != nil {
		return diff, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode %s\n", p, p, file.mode)
	content := file.data
	if file.mode != "120000" {
		sniff := content[:min(len(content), 8<<10)]
		if bytes.IndexByte(sniff, 0) >= 0 {
			diff.Binary = true
			fmt.Fprintf(&b, "Binary files /dev/null and b/%s differ\n", p)
			diff.Text = b.String()
			diff.Bytes = len(diff.Text)
			return diff, nil
		}
	}
	if file.truncated {
		diff.Truncated = true
		if i := bytes.LastIndexByte(content, '\n'); i >= 0 {
			content = content[:i+1]
		}
	}
	if len(content) > 0 {
		lines := strings.SplitAfter(string(content), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		// Keep the whole patch within the diff cap, counting '+' prefixes.
		header := fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", p, len(lines))
		budget := gitDiffMaxBytes - b.Len() - len(header) - len("\n\\ No newline at end of file\n")
		var body strings.Builder
		kept := 0
		for _, line := range lines {
			if body.Len()+1+len(line) > budget {
				diff.Truncated = true
				break
			}
			body.WriteString("+" + line)
			kept++
		}
		fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n@@ -0,0 +1", p)
		if kept != 1 {
			fmt.Fprintf(&b, ",%d", kept)
		}
		b.WriteString(" @@\n")
		b.WriteString(body.String())
		if kept == len(lines) && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
	diff.Text = b.String()
	diff.Bytes = len(diff.Text)
	return diff, nil
}

// untrackedFile is a bounded read of one checkout file.
type untrackedFile struct {
	mode      string
	data      []byte
	truncated bool
}

// boundedText trims truncated output back to its last complete line so a cut
// never splits a UTF-8 sequence mid-line.
func boundedText(b []byte, truncated bool) string {
	if truncated {
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			b = b[:i+1]
		}
	}
	return string(b)
}

var gitBinaryLine = regexp.MustCompile(`(?m)^Binary files .* differ$`)

func gitBinary(b []byte) bool { return gitBinaryLine.Match(b) }

// Commit metadata fields, NUL-separated; with -z git also NUL-terminates each
// commit, so a record is exactly gitCommitFields tokens. None can contain NUL.
const (
	gitCommitFormat = "--format=%H%x00%h%x00%P%x00%an%x00%ae%x00%at%x00%s%x00%D"
	gitCommitFields = 8
)

// parseGitCommits parses `log -z` output; the token after the last NUL is
// partial or empty, so every earlier complete record is kept.
func parseGitCommits(out []byte) []protocol.GitCommit {
	commits := []protocol.GitCommit{}
	tokens := strings.Split(string(out), "\x00")
	tokens = tokens[:len(tokens)-1]
	for len(tokens) >= gitCommitFields {
		f := tokens[:gitCommitFields]
		tokens = tokens[gitCommitFields:]
		c := protocol.GitCommit{Hash: strings.TrimPrefix(f[0], "\n"), Short: f[1], Parents: strings.Fields(f[2]), Author: f[3], Email: f[4], Subject: f[6]}
		if c.Parents == nil {
			c.Parents = []string{}
		}
		if sec, err := strconv.ParseInt(f[5], 10, 64); err == nil {
			c.Time = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		for _, ref := range strings.Split(f[7], ", ") {
			ref = strings.TrimPrefix(ref, "tag: ")
			if head, rest, ok := strings.Cut(ref, " -> "); ok {
				c.Refs = append(c.Refs, head)
				ref = rest
			}
			if ref != "" {
				c.Refs = append(c.Refs, ref)
			}
		}
		commits = append(commits, c)
	}
	return commits
}

func readGitLog(ctx context.Context, dir string, limit int) (protocol.GitLog, error) {
	log := protocol.GitLog{Workspace: inspectWorkspace(ctx, dir), Commits: []protocol.GitCommit{}}
	if log.Workspace.State != "branch" && log.Workspace.State != "detached" {
		return log, nil
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return log, err
	}
	out, truncated, err := g.read(ctx, gitLogMaxBytes, "log", "-z", "--no-color", "--decorate=full", "--no-show-signature", "-n", strconv.Itoa(limit+1), gitCommitFormat, "HEAD", "--")
	if err != nil {
		return log, gitError(err)
	}
	commits := parseGitCommits(out)
	if len(commits) > limit || truncated {
		log.Truncated = true
		commits = commits[:min(len(commits), limit)]
	}
	log.Commits = commits
	return log, nil
}

var gitHashInput = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// readGitShow validates commit as a hex hash (never an option or revision
// expression) that resolves to a commit whose full hash starts with it (so a
// hex-named branch or tag cannot stand in), then renders its stat and patch.
func readGitShow(ctx context.Context, dir, commit string) (protocol.GitShow, error) {
	var show protocol.GitShow
	if !gitHashInput.MatchString(commit) {
		return show, failure("invalid", "commit must be a hexadecimal hash")
	}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return show, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return show, err
	}
	out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{commit}")
	full := strings.TrimSpace(string(out))
	if err != nil || !gitHashInput.MatchString(full) || !strings.HasPrefix(full, strings.ToLower(commit)) {
		if errors.As(err, new(*protocol.Error)) {
			return show, err
		}
		return show, failure("not_found", "commit does not resolve in this checkout")
	}
	meta, metaTruncated, err := g.read(ctx, gitLogMaxBytes, "log", "-z", "--no-color", "--decorate=full", "--no-show-signature", "-n", "1", gitCommitFormat, full, "--")
	if err != nil {
		return show, gitError(err)
	}
	if commits := parseGitCommits(meta); len(commits) == 1 {
		show.Commit = commits[0]
	}
	body, bodyTruncated, err := g.read(ctx, gitDiffMaxBytes, "log", "--no-color", "--no-show-signature", "-n", "1", "--format=%B", full, "--")
	if err != nil {
		return show, gitError(err)
	}
	show.Commit.Body = string(body)
	text, truncated, err := g.read(ctx, gitDiffMaxBytes, append(append([]string{"show"}, gitDiffPrefixes...), "--no-show-signature", "--stat", "--patch", "--format=", full, "--")...)
	if err != nil {
		return show, gitError(err)
	}
	show.Text, show.Binary = strings.TrimPrefix(boundedText(text, truncated), "\n"), gitBinary(text)
	show.Truncated = truncated || metaTruncated || bodyTruncated
	show.Bytes = len(show.Text)
	return show, nil
}
