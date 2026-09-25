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
type GitStatus struct {
	Workspace WorkspaceInfo    `json:"workspace"`
	Branch    string           `json:"branch,omitempty"`
	Upstream  string           `json:"upstream,omitempty"`
	Ahead     int              `json:"ahead,omitempty"`
	Behind    int              `json:"behind,omitempty"`
	Operation string           `json:"operation,omitempty"`
	Entries   []GitStatusEntry `json:"entries"`
	Truncated bool             `json:"truncated,omitempty"`
}

// GitStatusEntry is one file in one group. Index and Worktree are the
// porcelain v2 X and Y status letters ("." for unchanged, "?" for untracked).
// OrigPath is the rename/copy source for staged renames. Paths are relative to
// the checkout root with forward slashes.
type GitStatusEntry struct {
	Path      string `json:"path"`
	OrigPath  string `json:"orig_path,omitempty"`
	Index     string `json:"index"`
	Worktree  string `json:"worktree"`
	Group     string `json:"group"`
	Submodule bool   `json:"submodule,omitempty"`
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
