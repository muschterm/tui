package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Read-only branch list, branch comparison and whole-group diffs. They use
// the same gitReader policy as git.go: nothing here writes refs, the index
// or the working tree, and nothing contacts a remote (ahead/behind and
// remote-tracking tips are whatever was last fetched).

const (
	gitBranchesMax      = 500
	gitCompareMaxCommit = 200
)

func (e *engine) gitBranches(w http.ResponseWriter, r *http.Request) {
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) { return readGitBranches(ctx, dir) })
}

func (e *engine) gitCompare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		return readGitCompare(ctx, dir, q.Get("base"), q.Get("head"))
	})
}

// for-each-ref fields, each NUL-terminated; git adds a newline after every
// record, which parsing strips from the next record's first field. Ref names
// cannot contain NUL or newline; a worktree path cannot contain NUL.
const (
	gitBranchFormat = "--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)%00%(upstream)%00%(upstream:short)%00%(upstream:track,nobracket)%00%(HEAD)%00%(worktreepath)%00"
	// gitBranchFormatNoTrack leaves the ahead/behind field empty, which
	// avoids a graph walk per branch on very large repositories.
	gitBranchFormatNoTrack = "--format=%(refname)%00%(refname:short)%00%(objectname)%00%(symref)%00%(upstream)%00%(upstream:short)%00%00%(HEAD)%00%(worktreepath)%00"
	gitBranchFields        = 9
	// gitBranchTrackBudget bounds the tracking attempt so a retry without
	// it still fits the request budget.
	gitBranchTrackBudget = gitRequestBudget / 2
)

func readGitBranches(ctx context.Context, dir string) (protocol.GitBranches, error) {
	out := protocol.GitBranches{Workspace: inspectWorkspace(ctx, dir), Branches: []protocol.GitBranch{}}
	if !gitReadable(out.Workspace) {
		return out, nil
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return out, err
	}
	args := func(format string) []string {
		return []string{"for-each-ref", "--count=" + strconv.Itoa(gitBranchesMax+1), format, "refs/heads/", "refs/remotes/"}
	}
	trackCtx, cancel := context.WithTimeout(ctx, gitBranchTrackBudget)
	raw, truncated, err := g.read(trackCtx, gitLogMaxBytes, args(gitBranchFormat)...)
	timedOut := trackCtx.Err() != nil && ctx.Err() == nil
	cancel()
	if err != nil && timedOut {
		out.TrackingOmitted = true
		raw, truncated, err = g.read(ctx, gitLogMaxBytes, args(gitBranchFormatNoTrack)...)
	}
	if err != nil {
		return out, gitError(err)
	}
	var records int
	out.Branches, records, out.Omitted = parseGitBranches(raw, gitBranchesMax)
	out.Truncated = truncated || records > gitBranchesMax
	return out, nil
}

// parseGitBranches keeps complete records among the first limit, returning
// the listed branches, the number of records read (listed, symbolic or
// omitted) and how many real refs were omitted for unsafe names.
// for-each-ref sorts by refname, so refs/heads/ precedes refs/remotes/.
func parseGitBranches(raw []byte, limit int) (branches []protocol.GitBranch, records, omitted int) {
	branches = []protocol.GitBranch{}
	tokens := strings.Split(string(raw), "\x00")
	tokens = tokens[:len(tokens)-1]
	for len(tokens) >= gitBranchFields {
		f := tokens[:gitBranchFields]
		tokens = tokens[gitBranchFields:]
		records++
		if records > limit {
			continue
		}
		ref := strings.TrimPrefix(f[0], "\n")
		if f[3] != "" {
			continue // symbolic, e.g. refs/remotes/origin/HEAD
		}
		if !validFullRef(ref) {
			omitted++
			continue
		}
		b := protocol.GitBranch{Name: f[1], Ref: ref, Remote: strings.HasPrefix(ref, "refs/remotes/"), Tip: f[2], Head: f[7] == "*", WorktreePath: f[8]}
		if f[4] != "" {
			b.Upstream = f[5]
			b.Ahead, b.Behind, b.UpstreamGone = parseGitTrack(f[6])
		}
		branches = append(branches, b)
	}
	return branches, records, omitted
}

// parseGitTrack parses %(upstream:track,nobracket): "", "gone", "ahead 2",
// "behind 1" or "ahead 2, behind 1".
func parseGitTrack(track string) (ahead, behind int, gone bool) {
	if track == "gone" {
		return 0, 0, true
	}
	for _, part := range strings.Split(track, ", ") {
		word, num, ok := strings.Cut(part, " ")
		n, err := strconv.Atoi(num)
		if !ok || err != nil {
			continue
		}
		switch word {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return ahead, behind, false
}

var gitCompareHash = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// validFullRef accepts refs/heads/... and refs/remotes/... names under
// git's check-ref-format rules (no control characters, space, ~ ^ : ? * [
// or backslash; no "..", "@{", "//"; no component starting with "." or
// ending in ".lock"; not ending in "/" or "."), plus one rule of our own:
// no component may start with "-", so a name can never read as an option.
// Non-ASCII names such as feature/ümlaut are valid.
func validFullRef(ref string) bool {
	rest, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		if rest, ok = strings.CutPrefix(ref, "refs/remotes/"); !ok {
			return false
		}
	}
	if rest == "" || rest == "@" || !utf8.ValidString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "-") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// validCompareRev accepts only HEAD, full branch/remote ref names and full
// lowercase hashes.
func validCompareRev(rev string) bool {
	return rev == "HEAD" || validFullRef(rev) || gitCompareHash.MatchString(rev)
}

// resolveCompareRev resolves a validated rev to a commit id. A full ref must
// exist under exactly that name and a hash must resolve to itself, so a
// hex-named branch cannot stand in for a hash.
func (g *gitReader) resolveCompareRev(ctx context.Context, rev string) (string, error) {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	oid := strings.TrimSpace(string(out))
	if err != nil || !gitCompareHash.MatchString(oid) || (gitCompareHash.MatchString(rev) && oid != rev) {
		if errors.As(err, new(*protocol.Error)) {
			return "", err
		}
		return "", failure("not_found", "revision does not resolve to a commit in this checkout")
	}
	if strings.HasPrefix(rev, "refs/") {
		if _, _, err := g.read(ctx, 4096, "show-ref", "--verify", "--quiet", "--end-of-options", rev); err != nil {
			return "", failure("not_found", "ref does not exist in this checkout")
		}
	}
	return oid, nil
}

func readGitCompare(ctx context.Context, dir, base, head string) (protocol.GitCompare, error) {
	c := protocol.GitCompare{Base: base, Head: head, AheadCommits: []protocol.GitCommit{}, BehindCommits: []protocol.GitCommit{}}
	if !validCompareRev(base) || !validCompareRev(head) {
		return c, failure("invalid", "base and head must be HEAD, a full refs/heads/ or refs/remotes/ name, or a full commit hash")
	}
	info := inspectWorkspace(ctx, dir)
	if info.State != "branch" && info.State != "detached" {
		if gitReadable(info) {
			return c, failure("not_found", "no commits to compare")
		}
		return c, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return c, err
	}
	if c.BaseOid, err = g.resolveCompareRev(ctx, base); err != nil {
		return c, err
	}
	if c.HeadOid, err = g.resolveCompareRev(ctx, head); err != nil {
		return c, err
	}
	counts, _, err := g.read(ctx, 4096, "rev-list", "--left-right", "--count", c.BaseOid+"..."+c.HeadOid, "--")
	if err != nil {
		return c, gitError(err)
	}
	if f := strings.Fields(string(counts)); len(f) == 2 {
		c.Behind, _ = strconv.Atoi(f[0])
		c.Ahead, _ = strconv.Atoi(f[1])
	}
	list := func(from, to string) ([]protocol.GitCommit, bool, error) {
		out, truncated, err := g.read(ctx, gitLogMaxBytes, "log", "-z", "--no-color", "--topo-order", "--decorate=full", "--no-show-signature", "-n", strconv.Itoa(gitCompareMaxCommit+1), gitCommitFormat, from+".."+to, "--")
		if err != nil {
			return nil, false, gitError(err)
		}
		commits := parseGitCommits(out)
		if len(commits) > gitCompareMaxCommit || truncated {
			return commits[:min(len(commits), gitCompareMaxCommit)], true, nil
		}
		return commits, false, nil
	}
	var capA, capB bool
	if c.AheadCommits, capA, err = list(c.BaseOid, c.HeadOid); err != nil {
		return c, err
	}
	if c.BehindCommits, capB, err = list(c.HeadOid, c.BaseOid); err != nil {
		return c, err
	}
	c.CommitsTruncated = capA || capB
	// merge-base exits 1 with no output for unrelated histories.
	if mb, _, err := g.read(ctx, 4096, "merge-base", "--end-of-options", c.BaseOid, c.HeadOid); err == nil && gitCompareHash.MatchString(strings.TrimSpace(string(mb))) {
		c.MergeBase = strings.TrimSpace(string(mb))
	} else if errors.As(err, new(*protocol.Error)) {
		return c, err
	}
	if c.MergeBase != "" {
		text, truncated, err := g.read(ctx, gitDiffMaxBytes, append(append([]string{"diff"}, gitDiffPrefixes...), "--end-of-options", c.MergeBase, c.HeadOid, "--")...)
		if err != nil {
			return c, gitError(err)
		}
		c.Text, c.Truncated, c.Binary = boundedText(text, truncated), truncated, gitBinary(text)
		c.Bytes = len(c.Text)
	}
	c.FetchedAt = g.fetchedAt(ctx)
	return c, nil
}

// fetchedAt is the FETCH_HEAD modification time, which git rewrites on
// every fetch; empty when the repository was never fetched.
func (g *gitReader) fetchedAt(ctx context.Context) string {
	out, _, err := g.read(ctx, 4096, "rev-parse", "--git-path", "FETCH_HEAD")
	p := strings.TrimSpace(string(out))
	if err != nil || p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.dir, p)
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}

// readGitWholeDiff is the bounded diff of a whole group: staged against
// HEAD (or the empty tree when unborn) or the worktree against the index.
// Untracked files are not part of either.
func readGitWholeDiff(ctx context.Context, dir, group string) (protocol.GitDiff, error) {
	diff := protocol.GitDiff{Group: group}
	if !gitReadable(inspectWorkspace(ctx, dir)) {
		return diff, errGitNotRepository
	}
	g, err := newGitReader(ctx, dir)
	if err != nil {
		return diff, err
	}
	cmd := []string{"diff"}
	if group == protocol.GitGroupStaged {
		cmd = append(cmd, "--cached")
	}
	cmd = append(append(cmd, gitDiffPrefixes...), "--")
	text, truncated, err := g.read(ctx, gitDiffMaxBytes, cmd...)
	if err != nil {
		return diff, gitError(err)
	}
	diff.Text, diff.Truncated, diff.Binary = boundedText(text, truncated), truncated, gitBinary(text)
	diff.Bytes = len(diff.Text)
	return diff, nil
}
