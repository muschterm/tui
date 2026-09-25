package protocol

// Read-only Git observations served by GET
// /v1/git/{status,diff,log,show,branches,compare}.
// Every endpoint selects exactly one target with project_id or thread_id,
// exactly like GET /v1/workspace, and resolves it to that checkout. None of
// them mutates the repository, index, refs or working tree, and none contacts
// a network remote. Text fields carry Git output verbatim; bytes that are not
// valid UTF-8 are replaced with U+FFFD by JSON encoding, and terminal control
// sequences are not removed, so presentation must sanitize before painting.

// Git status groups. A path with both staged and unstaged changes appears as
// two entries, one in each group.
const (
	GitGroupConflicted = "conflicted"
	GitGroupStaged     = "staged"
	GitGroupUnstaged   = "unstaged"
	GitGroupUntracked  = "untracked"
)

// GitStatus is the working-tree/index status of one checkout. Workspace uses
// the WorkspaceInfo vocabulary; Entries are populated only when
// Workspace.State is branch, detached or unborn. Upstream is empty when the
// branch has no configured upstream or it cannot be resolved; Ahead/Behind are
// meaningful only when Upstream is set and are computed from local refs
// (whatever was last fetched), never from the network. Operation is "", merge,
// rebase, cherry-pick, revert or bisect. Truncated reports that entries were
// capped.
//
// The remaining fields support Git writes (see git_write.go in this package):
// HeadOid is the full HEAD commit hash, empty when unborn. StagedFingerprint
// pins the complete staged set against HEAD and is what git.commit must send
// back. StagedTruncated reports that the staged set was not fully listed
// (entry cap or byte cap); the fingerprint then starts with "truncated" and
// git.commit refuses it with status_truncated. HeadOnUpstream reports that
// the HEAD commit is already reachable from the branch upstream or from any
// remote-tracking ref (local refs only, never the network), so amending it
// rewrites published history; it is also true when HeadOnUpstreamUnknown
// reports that reachability could not be determined. Identity is the
// committer identity Git would use.
type GitStatus struct {
	Workspace         WorkspaceInfo    `json:"workspace"`
	Branch            string           `json:"branch,omitempty"`
	Upstream          string           `json:"upstream,omitempty"`
	Ahead             int              `json:"ahead,omitempty"`
	Behind            int              `json:"behind,omitempty"`
	Operation         string           `json:"operation,omitempty"`
	Entries           []GitStatusEntry `json:"entries"`
	Truncated         bool             `json:"truncated,omitempty"`
	HeadOid           string           `json:"head_oid,omitempty"`
	StagedFingerprint string           `json:"staged_fingerprint,omitempty"`
	HeadOnUpstream    bool             `json:"head_on_upstream,omitempty"`
	// Additive (2026-09-24 review).
	HeadOnUpstreamUnknown bool         `json:"head_on_upstream_unknown,omitempty"`
	StagedTruncated       bool         `json:"staged_truncated,omitempty"`
	Identity              *GitIdentity `json:"identity,omitempty"`
}

// GitIdentity is the author/committer identity resolved by `git var`, with
// the config scope (system, global, local, worktree, command) that supplied
// user.name and user.email when they come from config. Missing reports that
// Git cannot form an identity (for example user.useConfigOnly without
// user.email); git.commit then fails with identity_missing.
type GitIdentity struct {
	Name       string `json:"name,omitempty"`
	Email      string `json:"email,omitempty"`
	NameScope  string `json:"name_scope,omitempty"`
	EmailScope string `json:"email_scope,omitempty"`
	Missing    bool   `json:"missing,omitempty"`
}

// GitStatusEntry is one file in one group. Index and Worktree are the
// porcelain v2 X and Y status letters ("." for unchanged, "?" for untracked).
// OrigPath is the rename/copy source for staged renames. Paths are relative to
// the checkout root with forward slashes.
//
// Write support: ModeHead/ModeIndex/ModeWorktree and HeadOid/IndexOid are the
// porcelain v2 mH/mI/mW and hH/hI fields (empty for untracked and conflicted
// entries). WorktreeStat is an opaque token for unstaged and untracked
// entries: the node type (reg, lnk, dir, other) plus lstat size, mtime,
// ctime, inode and mode; "absent" for a deleted file; "blocked" when a parent
// is a file or symlink; "unavailable" when it cannot be examined. Pin is an
// opaque digest of everything this entry shows (group, path, status letters,
// modes, object IDs and the worktree token); stage, unstage and discard
// commands send it back and the server refuses with stale_entry when the
// entry no longer matches. Pin is empty for an entry that cannot be acted on
// (a path that is not valid UTF-8 cannot round-trip through JSON).
type GitStatusEntry struct {
	Path         string `json:"path"`
	OrigPath     string `json:"orig_path,omitempty"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Group        string `json:"group"`
	Submodule    bool   `json:"submodule,omitempty"`
	ModeHead     string `json:"mode_head,omitempty"`
	ModeIndex    string `json:"mode_index,omitempty"`
	ModeWorktree string `json:"mode_worktree,omitempty"`
	HeadOid      string `json:"head_oid,omitempty"`
	IndexOid     string `json:"index_oid,omitempty"`
	WorktreeStat string `json:"worktree_stat,omitempty"`
	Pin          string `json:"pin,omitempty"`
	// IntentToAdd marks an unstaged entry added with `git add -N`: the index
	// holds no content, so discarding it empties the file and the content
	// cannot be recovered from Git.
	IntentToAdd bool `json:"intent_to_add,omitempty"`
}

// GitDiff is a bounded patch for one status entry, or, when requested with
// an empty path for the staged or unstaged group, the whole bounded diff of
// that group (staged against HEAD, or worktree against the index). Bytes is len(Text) before
// JSON encoding. Binary reports that Git described at least one side as binary.
type GitDiff struct {
	Path      string `json:"path"`
	Group     string `json:"group"`
	Text      string `json:"text"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Bytes     int    `json:"bytes"`
}

// GitCommit is one commit. Time is the author time in RFC 3339. Refs are full
// decoration names such as HEAD, refs/heads/main or refs/tags/v1. Body is
// the full message and is only populated by GitShow.
type GitCommit struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Time    string   `json:"time"`
	Subject string   `json:"subject"`
	Refs    []string `json:"refs,omitempty"`
	Body    string   `json:"body,omitempty"`
}

// Commit log scopes for GET /v1/git/log?scope=.
const (
	// GitLogScopeHead lists HEAD plus its configured upstream, when any.
	GitLogScopeHead = "head"
	// GitLogScopeAll lists every local branch and remote-tracking ref plus HEAD.
	GitLogScopeAll = "all"
)

// GitLog lists commits in topological order (children before parents, with
// each line of history kept together), newest first within that order. It
// is empty for an unborn branch or a non-Git workspace; Workspace explains
// which. Scope echoes the effective scope. Upstream is the full upstream ref
// included by the head scope, or empty.
type GitLog struct {
	Workspace WorkspaceInfo `json:"workspace"`
	Scope     string        `json:"scope,omitempty"`
	Upstream  string        `json:"upstream,omitempty"`
	// UpstreamOmitted reports a configured upstream left out of the head
	// scope because its name is unsafe to pass to git.
	UpstreamOmitted bool        `json:"upstream_omitted,omitempty"`
	Commits         []GitCommit `json:"commits"`
	Truncated       bool        `json:"truncated,omitempty"`
}

// GitBranch is one local branch or remote-tracking ref from
// GET /v1/git/branches. Name is the short name (main, origin/main); Ref the
// full name (refs/heads/main), which is what GET /v1/git/compare accepts.
// Upstream is the configured upstream's short name; Ahead/Behind count
// against it from local refs only, and UpstreamGone reports a configured
// upstream whose ref no longer exists. Head marks the branch checked out
// here; WorktreePath is the checkout that has it checked out, if any.
type GitBranch struct {
	Name         string `json:"name"`
	Ref          string `json:"ref"`
	Remote       bool   `json:"remote,omitempty"`
	Tip          string `json:"tip"`
	Upstream     string `json:"upstream,omitempty"`
	UpstreamGone bool   `json:"upstream_gone,omitempty"`
	Ahead        int    `json:"ahead,omitempty"`
	Behind       int    `json:"behind,omitempty"`
	Head         bool   `json:"head,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
}

// GitBranches lists local branches (sorted by name) then remote-tracking
// refs; symbolic remote HEADs are not listed. Truncated reports the 500-ref
// cap (counted over every ref read, listed or not). Omitted counts real refs
// not listed because their names are unsafe to pass back to compare (e.g.
// a component starting with "-"). TrackingOmitted reports that upstream
// ahead/behind could not be computed within budget and was left empty.
type GitBranches struct {
	Workspace       WorkspaceInfo `json:"workspace"`
	Branches        []GitBranch   `json:"branches"`
	Truncated       bool          `json:"truncated,omitempty"`
	Omitted         int           `json:"omitted,omitempty"`
	TrackingOmitted bool          `json:"tracking_omitted,omitempty"`
}

// GitCompare compares Head against Base from GET /v1/git/compare. Base and
// Head echo the requested full ref names or hashes; BaseOid/HeadOid are
// what they resolved to. MergeBase is empty for unrelated histories, in
// which case there is no diff. Ahead counts commits in Head not in Base and
// Behind the reverse (exact); AheadCommits/BehindCommits list at most 200 of
// each, newest first, with CommitsTruncated set when either list is capped.
// Text is the bounded patch from the merge base to Head (git diff
// base...head). FetchedAt is the RFC 3339 mtime of FETCH_HEAD, i.e. when
// remote-tracking refs were last fetched, or empty when never fetched;
// nothing here contacts a remote.
type GitCompare struct {
	Base             string      `json:"base"`
	Head             string      `json:"head"`
	BaseOid          string      `json:"base_oid"`
	HeadOid          string      `json:"head_oid"`
	MergeBase        string      `json:"merge_base,omitempty"`
	Ahead            int         `json:"ahead"`
	Behind           int         `json:"behind"`
	AheadCommits     []GitCommit `json:"ahead_commits"`
	BehindCommits    []GitCommit `json:"behind_commits"`
	CommitsTruncated bool        `json:"commits_truncated,omitempty"`
	Text             string      `json:"text"`
	Binary           bool        `json:"binary,omitempty"`
	Truncated        bool        `json:"truncated,omitempty"`
	Bytes            int         `json:"bytes"`
	FetchedAt        string      `json:"fetched_at,omitempty"`
}

// GitShow is one commit's metadata plus a bounded `--stat --patch` text.
type GitShow struct {
	Commit    GitCommit `json:"commit"`
	Text      string    `json:"text"`
	Binary    bool      `json:"binary,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Bytes     int       `json:"bytes"`
}
