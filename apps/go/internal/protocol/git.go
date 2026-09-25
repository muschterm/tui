package protocol

// Read-only Git observations served by GET /v1/git/{status,diff,log,show}.
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

// GitDiff is a bounded patch for one status entry. Bytes is len(Text) before
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

// GitLog lists commits reachable from HEAD, newest first. It is empty for an
// unborn branch or a non-Git workspace; Workspace explains which.
type GitLog struct {
	Workspace WorkspaceInfo `json:"workspace"`
	Commits   []GitCommit   `json:"commits"`
	Truncated bool          `json:"truncated,omitempty"`
}

// GitShow is one commit's metadata plus a bounded `--stat --patch` text.
type GitShow struct {
	Commit    GitCommit `json:"commit"`
	Text      string    `json:"text"`
	Binary    bool      `json:"binary,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Bytes     int       `json:"bytes"`
}
