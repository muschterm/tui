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
// rebase, cherry-pick, revert, bisect or (since ADR 0023) am. Truncated
// reports that entries were capped.
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
	// Stages (additive, ADR 0023) lists a conflicted entry's index stages 1
	// (base), 2 (ours) and 3 (theirs) from porcelain v2's unmerged record;
	// ModeWorktree and WorktreeStat are then also set, and all of them are
	// part of Pin. Labels are filled only by GET /v1/git/operation.
	Stages []GitConflictStage `json:"stages,omitempty"`
}

// GitConflictStage is one index stage of an unmerged path. Present is false
// when that side has no entry (added or deleted on one side). Label names the
// side by its real role in the operation (see GitOperationState.Sides).
type GitConflictStage struct {
	Present bool   `json:"present"`
	Mode    string `json:"mode,omitempty"`
	Oid     string `json:"oid,omitempty"`
	Label   string `json:"label,omitempty"`
}

// Operation kinds reported by GitStatus.Operation and GitOperationState.Kind.
const (
	GitOperationMerge      = "merge"
	GitOperationRebase     = "rebase"
	GitOperationCherryPick = "cherry-pick"
	GitOperationRevert     = "revert"
	GitOperationBisect     = "bisect"
	GitOperationAm         = "am"
)

// Conflict kinds (GitConflict.Kind), named as porcelain status letters: which
// stages are present (1 base, 2 ours, 3 theirs).
//
//	UU  both modified (1, 2, 3)     AA  both added (2, 3)
//	UD  deleted by them (1, 2)      DU  deleted by us (1, 3)
//	AU  added by us (2)             UA  added by them (3)
//	DD  both deleted (1)
const (
	GitConflictBothModified  = "UU"
	GitConflictBothAdded     = "AA"
	GitConflictDeletedByThem = "UD"
	GitConflictDeletedByUs   = "DU"
	GitConflictAddedByUs     = "AU"
	GitConflictAddedByThem   = "UA"
	GitConflictBothDeleted   = "DD"
)

// Sources of GitOperationState.
const (
	GitOperationSourceApp      = "app"
	GitOperationSourceExternal = "external"
)

// GitOperationState is GET /v1/git/operation (ADR 0023): the merge, rebase,
// cherry-pick, revert or bisect in progress in the target checkout's
// worktree, read from Git's own state files and index without changing
// anything. Kind is empty when none is in progress (Conflicts is then
// empty); Workspace explains a non-Git target.
//
// Source is app when the operation is the one Snapshot.GitOperations
// records for this repository (OperationID and Record are then set) and
// external otherwise (started in a terminal or another tool); the operation
// commands work for both. Reading reconciles that record with the
// repository (see GitOperationRecord).
//
// Branch is the branch being operated on (for a rebase, the branch being
// rebased while HEAD is detached; empty when that is a detached HEAD).
// HeadOid is the current HEAD, which git.operation_* commands pin as
// ExpectedHead. OrigHead is where the branch was before the operation
// started. Target is the commit being merged in or rebased onto (merge:
// MERGE_HEAD; rebase: onto), with Label a branch name that points at it, or
// for an application record the name the user chose. Step and Steps are the
// rebase's position (1-based) and total; Step is the ExpectedStep pin
// (0 for other kinds). Current is the commit Git is stopped at: for a
// rebase the commit being replayed (the one Skip would drop), for a merge
// the merged commit with Subject from the first line of MERGE_MSG, for
// cherry-pick and revert the picked or reverted commit.
//
// Conflicts lists unmerged paths from the index (at most 500;
// ConflictsTruncated when more), never from conflict markers.
// UnmergedFingerprint digests every unmerged index entry and is empty when
// there are none; git.operation_continue sends it back. Sides names stages
// 1-3 by role, for example during a rebase ours is the upstream plus the
// commits rebased so far and theirs is the commit being replayed.
//
// Can reports which commands would be accepted now and, when not, Reason.
//
// Additive (2026-09-25 review): StopReason is set when a rebase stopped for
// an interactive step rather than a conflict (edit, exec, break or
// interactive for any other todo command such as reword, squash or fixup);
// Skip and Continue are then not offered (use a terminal) and Current is
// the commit applied before stopping. WorktreeFingerprint
// (GitWorktreeFingerprint of the tracked status: staged, unstaged and
// conflicted entries) is what git.operation_abort and _skip pin;
// StagedFingerprint (HEAD plus every staged change, as GitStatus) is what
// git.operation_continue pins, so a change staged after review is never
// committed unseen. DiscardsOnAbort and DiscardsOnSkip list every path
// outside the conflicted set whose current content that command would
// overwrite or remove and that is not exactly the operation's own result: a
// staged change is listed unless its index entry is exactly what the
// current step produced for a path only that step changed (the merged
// side, or the picked, reverted or replayed commit); an unstaged change is
// listed for a rebase, which resets hard (a merge, cherry-pick or revert
// abort uses `reset --merge`, which keeps unstaged changes or refuses); and
// an untracked or ignored file the command would write over, or remove to
// write a file, is listed too (for a rebase abort, files of the original
// branch; for a skip, files the remaining commits add). The confirmation
// must name these files; the command then carries
// them as AcknowledgeDiscard. DiscardsIncomplete reports that they could not
// be determined (too many changes); abort and skip are then refused.
// MarkerPaths lists staged files whose staged content still has a
// conflict-marker line (<, | or > repeated conflict-marker-size times, 7 by
// default, at a line start, followed by a space or the line end; CRLF
// allowed), read from the staged blobs themselves regardless of attributes;
// a blob with a NUL in its first 8 KiB is binary and not scanned.
// MarkersIncomplete reports that not every staged blob could be scanned
// (more than 5000 files, 8 MiB per file or 32 MiB in total, or a read
// failure); continue then needs AcknowledgeMarkersIncomplete.
//
// Additive (second review): ContinueInTheWay lists untracked or ignored
// files that the commits a rebase (or a cherry-pick or revert sequence)
// still has to apply would write over; continue and skip are refused
// (would_overwrite) until they are moved; ContinueUnchecked reports that
// those commits could not be checked (an apply-backend rebase or an
// unusual todo command), and continue and skip are then left to a terminal
// (not_supported). Operation commands never recurse
// into submodules (submodule.recurse=false), so submodule worktrees, dirty
// or not, are left as they are. BackupIncomplete reports that abort or skip could not back up
// every file it would overwrite (more than 1000 files or 64 MiB, or an
// unreadable file); they then need AcknowledgeBackupIncomplete.
type GitOperationState struct {
	Workspace           WorkspaceInfo       `json:"workspace"`
	Kind                string              `json:"kind,omitempty"`
	Source              string              `json:"source,omitempty"`
	OperationID         string              `json:"operation_id,omitempty"`
	Record              *GitOperationRecord `json:"record,omitempty"`
	Branch              string              `json:"branch,omitempty"`
	HeadOid             string              `json:"head_oid,omitempty"`
	OrigHead            string              `json:"orig_head,omitempty"`
	Target              *GitOperationCommit `json:"target,omitempty"`
	Step                int                 `json:"step,omitempty"`
	Steps               int                 `json:"steps,omitempty"`
	Current             *GitOperationCommit `json:"current,omitempty"`
	Conflicts           []GitConflict       `json:"conflicts"`
	ConflictsTruncated  bool                `json:"conflicts_truncated,omitempty"`
	UnmergedFingerprint string              `json:"unmerged_fingerprint,omitempty"`
	Sides               GitConflictSides    `json:"sides"`
	Can                 GitOperationActions `json:"can"`
	// Additive (2026-09-25 review).
	StopReason          string   `json:"stop_reason,omitempty"`
	WorktreeFingerprint string   `json:"worktree_fingerprint,omitempty"`
	StagedFingerprint   string   `json:"staged_fingerprint,omitempty"`
	DiscardsOnAbort     []string `json:"discards_on_abort,omitempty"`
	DiscardsOnSkip      []string `json:"discards_on_skip,omitempty"`
	DiscardsIncomplete  bool     `json:"discards_incomplete,omitempty"`
	MarkerPaths         []string `json:"marker_paths,omitempty"`
	MarkersIncomplete   bool     `json:"markers_incomplete,omitempty"`
	// Additive (second review).
	ContinueInTheWay  []string `json:"continue_in_the_way,omitempty"`
	ContinueUnchecked bool     `json:"continue_unchecked,omitempty"`
	BackupIncomplete  bool     `json:"backup_incomplete,omitempty"`
	// Additive (third review). DiscardsOnAbortFingerprint,
	// DiscardsOnSkipFingerprint and MarkersFingerprint digest those lists
	// exactly as the server holds them; commands acknowledge a list with its
	// fingerprint (GitOperationWrite.DiscardsFingerprint,
	// MarkersFingerprint), which also works for paths that are not valid
	// UTF-8 and so cannot round-trip through JSON. HiddenEntries lists index
	// entries marked assume-unchanged or skip-worktree on paths abort, skip
	// or continue would write; their local changes are invisible, so those
	// commands are refused (not_supported; use a terminal). AbortDropsCommits
	// lists the commits a cherry-pick or revert sequence has already made
	// and that an abort removes from the branch (OrigHead is then the
	// sequence's starting point; at most 200 are listed); the confirmation
	// must name them.
	DiscardsOnAbortFingerprint string               `json:"discards_on_abort_fingerprint,omitempty"`
	DiscardsOnSkipFingerprint  string               `json:"discards_on_skip_fingerprint,omitempty"`
	MarkersFingerprint         string               `json:"markers_fingerprint,omitempty"`
	HiddenEntries              []string             `json:"hidden_entries,omitempty"`
	AbortDropsCommits          []GitOperationCommit `json:"abort_drops_commits,omitempty"`
	// Additive (fourth review). AbortDropsCount is the exact number of
	// commits a sequence abort removes; AbortDropsIncomplete reports that
	// AbortDropsCommits does not list all of them. A sequence abort needs
	// AcknowledgeDropped equal to AbortDropsFingerprint (the count and the
	// listed commits), or it is refused (drops_unacknowledged).
	// NestedInTheWay lists nested repositories or unexpanded untracked
	// directories (trailing slash) that abort, skip or continue would
	// remove or write into; their content can be neither listed nor backed
	// up, so those commands are refused (not_supported; resolve in a
	// terminal).
	AbortDropsCount       int      `json:"abort_drops_count,omitempty"`
	AbortDropsIncomplete  bool     `json:"abort_drops_incomplete,omitempty"`
	AbortDropsFingerprint string   `json:"abort_drops_fingerprint,omitempty"`
	NestedInTheWay        []string `json:"nested_in_the_way,omitempty"`
	// Additive (fifth review). NestedOnAbort, NestedOnSkip and
	// NestedOnContinue split NestedInTheWay (now their union) by command, so
	// only the command that would remove the nested repository is refused;
	// submodules (gitlinks in HEAD, OrigHead or the index) are never listed,
	// since Git leaves their directories alone. DiscardsOnAbortIncomplete
	// and DiscardsOnSkipIncomplete split DiscardsIncomplete (now their
	// union) by command. BackupMissingOnAbort and BackupMissingOnSkip list
	// the files that command's backup would not hold (beyond 1000 files or
	// 64 MiB, or not a regular file or symlink); the command then needs
	// AcknowledgeBackupMissing equal to the list's fingerprint, and
	// BackupIncomplete reports that either list is non-empty.
	// ContinueUpdatesRefs lists the refs an external `--update-refs`
	// rebase would move when it continues; continue and skip are then left
	// to a terminal (not_supported), since the application never moves
	// other branches.
	NestedOnAbort                   []string `json:"nested_on_abort,omitempty"`
	NestedOnSkip                    []string `json:"nested_on_skip,omitempty"`
	NestedOnContinue                []string `json:"nested_on_continue,omitempty"`
	DiscardsOnAbortIncomplete       bool     `json:"discards_on_abort_incomplete,omitempty"`
	DiscardsOnSkipIncomplete        bool     `json:"discards_on_skip_incomplete,omitempty"`
	BackupMissingOnAbort            []string `json:"backup_missing_on_abort,omitempty"`
	BackupMissingOnSkip             []string `json:"backup_missing_on_skip,omitempty"`
	BackupMissingOnAbortFingerprint string   `json:"backup_missing_on_abort_fingerprint,omitempty"`
	BackupMissingOnSkipFingerprint  string   `json:"backup_missing_on_skip_fingerprint,omitempty"`
	ContinueUpdatesRefs             []string `json:"continue_updates_refs,omitempty"`
}

// GitOperationBackup is the copy the server makes immediately before an
// abort or skip overwrites anything (after open documents are saved): Oid is
// a commit whose tree holds the working-tree content of Paths (every file
// with staged, unstaged or conflicted changes, and every untracked or
// ignored file the command would overwrite or remove) at their own paths,
// and IndexOid its parent, whose tree holds their staged (stage 0) entries.
// The objects are written to the repository's object database without any
// ref, so Git's garbage collection may remove them after gc.pruneExpire
// (two weeks by default) and `git gc --prune=now` removes them at once.
// Untracked and ignored files (which may hold secrets) are copied into the
// object database too, as unreferenced blobs, until they are pruned. To recover a file: `git show <Oid>:<path> >
// <path>` (working-tree content), `git checkout <Oid> -- <path>` (also
// stages it) or `git checkout <IndexOid> -- <path>` (the staged version);
// GET /v1/git/operation/backup reads one file. Incomplete lists Missing
// paths that could not be copied (only when AcknowledgeBackupIncomplete
// allowed it).
type GitOperationBackup struct {
	Oid        string   `json:"oid"`
	IndexOid   string   `json:"index_oid"`
	CreatedAt  string   `json:"created_at"`
	Paths      []string `json:"paths,omitempty"`
	Incomplete bool     `json:"incomplete,omitempty"`
	Missing    []string `json:"missing,omitempty"`
}

// GitBackupFile is GET /v1/git/operation/backup?oid=<backup Oid or
// IndexOid>&path=<path> (plus the target): one file of a backup, read-only.
// Only backups the server recorded for that repository are served
// (Snapshot.GitBackups); any other object ID is not_found.
// Content is at most 1 MiB (Truncated beyond); Mode is the Git file mode.
type GitBackupFile struct {
	Oid       string `json:"oid"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Size      int64  `json:"size"`
	Content   []byte `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Interactive stop reasons (GitOperationState.StopReason).
const (
	GitStopEdit        = "edit"
	GitStopExec        = "exec"
	GitStopBreak       = "break"
	GitStopInteractive = "interactive"
)

// GitOperationCommit identifies one commit of an operation. Oid is the full
// hash; Subject is untrusted text (sanitize before painting).
type GitOperationCommit struct {
	Oid     string `json:"oid"`
	Label   string `json:"label,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// GitConflictSides labels the index stages of this operation: Base (1),
// Ours (2) and Theirs (3).
type GitConflictSides struct {
	Base   string `json:"base,omitempty"`
	Ours   string `json:"ours,omitempty"`
	Theirs string `json:"theirs,omitempty"`
}

// GitConflict is one unmerged path. Stages are base, ours and theirs in that
// order, labelled as in GitOperationState.Sides. Binary reports that a side's
// content looks binary (a NUL in its first 8000 bytes, as Git decides) or the
// path's diff attribute is unset; it is checked for the first 64 conflicts
// only (BinaryUnknown beyond). Submodule reports a gitlink (mode 160000) and
// Symlink a symbolic link on any side. WorktreeStat is the worktree token
// (as GitStatusEntry.WorktreeStat).
type GitConflict struct {
	Path          string              `json:"path"`
	Kind          string              `json:"kind"`
	Stages        [3]GitConflictStage `json:"stages"`
	Binary        bool                `json:"binary,omitempty"`
	BinaryUnknown bool                `json:"binary_unknown,omitempty"`
	Submodule     bool                `json:"submodule,omitempty"`
	Symlink       bool                `json:"symlink,omitempty"`
	WorktreeStat  string              `json:"worktree_stat,omitempty"`
}

// GitOperationActions says whether each command is accepted now.
type GitOperationActions struct {
	Continue GitOperationAction `json:"continue"`
	Skip     GitOperationAction `json:"skip"`
	Abort    GitOperationAction `json:"abort"`
}

// GitOperationAction is one command's availability; Reason explains a
// refusal in words a user can act on.
type GitOperationAction struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Integration sources (GitIntegrate.Source).
const (
	GitIntegrateUpstream = "upstream"
	GitIntegrateBranch   = "branch"
	GitIntegrateCommit   = "commit"
)

// GitIntegratePreview is GET /v1/git/integrate/preview?kind=merge|rebase&
// target=<full ref or full hash> (ADR 0023): what git.merge or git.rebase
// would do from the current state, computed from local refs without changing
// anything. Target is a full ref (refs/heads/..., refs/remotes/...) or a full
// commit hash; Source is upstream when it is the branch's upstream.
//
// UpToDate: the target is already contained in HEAD (nothing to do).
// FastForward (merge): the merge will move the branch without a merge
// commit: HEAD is an ancestor of the target and the effective fast-forward
// setting (merge.ff, then branch.<name>.mergeOptions, as `git merge` reads
// them; MergeFF is ff, no-ff or only) allows it. With only, a merge that is
// not a fast forward is blocked (ff_only_configured); with no-ff a merge
// commit is created even when a fast forward is possible. ReplayCount (rebase): commits in
// target..HEAD, sent back as ExpectedReplayCount; Git may drop commits whose
// change is already upstream. RangeHasMerges: that range contains merge
// commits, which a rebase here refuses (range_has_merges; use a terminal).
// Published: a commit to be replayed is on a remote-tracking ref (unknown
// counts as published), so rebasing rewrites published history and needs
// AcknowledgePublished. Conflicts are not predicted. Blocked is the refusal
// code a start would return now (for example dirty_tree, detached,
// operation_in_progress), with BlockedMessage.
type GitIntegratePreview struct {
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	Branch         string `json:"branch,omitempty"`
	HeadOid        string `json:"head_oid,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	TargetOid      string `json:"target_oid,omitempty"`
	TargetLabel    string `json:"target_label,omitempty"`
	TargetSubject  string `json:"target_subject,omitempty"`
	MergeBase      string `json:"merge_base,omitempty"`
	UpToDate       bool   `json:"up_to_date,omitempty"`
	FastForward    bool   `json:"fast_forward,omitempty"`
	ReplayCount    int    `json:"replay_count,omitempty"`
	RangeHasMerges bool   `json:"range_has_merges,omitempty"`
	Published      bool   `json:"published,omitempty"`
	Blocked        string `json:"blocked,omitempty"`
	BlockedMessage string `json:"blocked_message,omitempty"`
	// Additive (2026-09-25 review).
	MergeFF string `json:"merge_ff,omitempty"`
}

// Effective fast-forward settings (GitIntegratePreview.MergeFF).
const (
	GitMergeFF     = "ff"
	GitMergeNoFF   = "no-ff"
	GitMergeFFOnly = "only"
)

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
