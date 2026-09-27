package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Git writes (ADR 0020). Each is a durable command sent through
// POST /v1/command with Command.Git set and exactly one of Command.ThreadID or
// Command.ProjectID selecting the checkout, as the read endpoints do. The
// server resolves the checkout to its repository toplevel and:
//
//  1. refuses immediately, returning a protocol.Error and recording nothing,
//     when the request is invalid, the checkout is busy or a pin is stale
//     (the codes below);
//  2. otherwise records the command with a receipt in state running and
//     publishes a running GitOp before Git starts, runs Git outside the engine
//     lock, then replaces the receipt with its final state.
//
// The HTTP reply is the final Receipt, whose Git field carries the GitResult.
// A retry with the same command ID never runs Git again: it waits for a
// running attempt and then returns the stored receipt. A receipt that was
// still running when the server stopped is reported as outcome_unknown after
// restart; refresh status to see what happened.
//
// Writes run Git exactly like the user's CLI: repository-local config,
// filters (LFS clean on add), hooks and commit signing all apply. No editor,
// pager, credential prompt, lazy fetch or automatic maintenance runs, the
// only stdin is the commit message, and Git has no controlling terminal
// (GPG_TTY and SSH_TTY are removed), so terminal pinentry cannot prompt; a
// graphical or agent-cached pinentry still works. Hooks share a 5 minute budget; their
// combined output is returned bounded to 64 KiB.
//
// While any thread holds a writer lease on a checkout inside the repository
// (a running or waiting turn, including one waiting on a question), every Git
// write that takes the lease is refused with checkout_busy. While such a Git
// write runs it holds the lease itself, so no agent turn starts in that
// repository; queued turns wait and their WriterWait names the Git command.
// git.fetch, git.push and git.branch_create change neither the index nor the
// working tree and do not take the lease (ADR 0021): they run beside agent
// turns. Every Git write, lease or not, takes the repository's single Git
// slot, so a second Git write there is refused with git_busy.

// Git write command kinds.
const (
	GitKindStage   = "git.stage"
	GitKindUnstage = "git.unstage"
	GitKindDiscard = "git.discard"
	GitKindCommit  = "git.commit"

	// Ref and remote actions (ADR 0021); payloads in GitRefWrite, GitSync
	// and GitCancel below.
	GitKindBranchCreate = "git.branch_create"
	GitKindSwitch       = "git.switch"
	GitKindResetSoft    = "git.reset_soft"
	GitKindFetch        = "git.fetch"
	GitKindPull         = "git.pull"
	GitKindPush         = "git.push"
	GitKindCancel       = "git.cancel"

	// Merge and rebase (ADR 0023); payloads in GitIntegrate and
	// GitOperationWrite below.
	GitKindMerge             = "git.merge"
	GitKindRebase            = "git.rebase"
	GitKindOperationAbort    = "git.operation_abort"
	GitKindOperationContinue = "git.operation_continue"
	GitKindOperationSkip     = "git.operation_skip"

	// Manual conflict resolution (ADR 0023, S3); payload GitConflictWrite.
	GitKindConflictChoose  = "git.conflict_choose"
	GitKindConflictResolve = "git.conflict_resolve"
	GitKindConflictRestore = "git.conflict_restore"

	// Agent conflict resolution (ADR 0023, S4); payload GitResolveJob.
	GitKindResolveJobStart    = "git.resolve_job_start"
	GitKindResolveJobFollowup = "git.resolve_job_followup"
	GitKindResolveJobCancel   = "git.resolve_job_cancel"
	GitKindResolveJobEnd      = "git.resolve_job_end"
)

// Git write states, shared by Receipt.State, GitResult.State and GitOp.State.
const (
	GitStateRunning        = "running"
	GitStateSucceeded      = "succeeded"
	GitStateFailed         = "failed"
	GitStateOutcomeUnknown = "outcome_unknown"
)

// GitUnbornHead is the ExpectedHead value that pins an unborn branch.
const GitUnbornHead = "unborn"

// Error codes returned before anything runs (protocol.Error.Code):
//
//	invalid                request shape, path or group is not acceptable
//	not_found              target thread or project does not exist
//	not_git                the checkout is not a readable Git repository
//	checkout_busy          a thread holds the writer lease (message names it)
//	git_busy               another Git write is running in this repository;
//	                       also returned by thread.delete and project.remove
//	                       while a Git write targeting them is in progress
//	stale_entry            the path's status no longer matches its Pin
//	stale_head             HEAD differs from ExpectedHead
//	stale_status           the staged set differs from StagedFingerprint
//	nothing_staged         commit without staged changes (and without amend)
//	nothing_to_amend       amend on an unborn branch
//	empty_message          commit message is empty after whitespace cleanup
//	published_commit       amend of a commit reachable from a remote-tracking
//	                       ref without AcknowledgePublished
//	operation_in_progress  commit during merge, rebase, cherry-pick or revert
//	conflicted             the path (or, for commit, the index) is unmerged
//	not_supported          submodule, nested repository, directory, a path
//	                       below a file or symlink, or special file; another
//	                       index (or, for unstage, HEAD) entry that is an
//	                       ancestor or descendant of the path (file/directory
//	                       replacement); a renamed intent-to-add file; a
//	                       partial clone with Git older than 2.44
//	status_truncated       commit of a staged set that status did not list in
//	                       full (GitStatus.StagedTruncated); too many changes
//	                       to review here, commit from a terminal
//	internal_error         the server failed while preparing the change
//	storage                the change could not be recorded; Git did not run
//	unknown_outcome_lookup the stored receipt for this command ID could not be
//	                       read, so whether it already ran is unknown; do not
//	                       report that nothing ran, refresh status first
//	confirmation_required  discard without Confirmed
//	identity_missing       Git cannot determine the committer identity
//	index_locked           index.lock persisted through three retries
//	ref_locked             HEAD.lock persisted through three retries
//	stopping, unavailable  server shutting down, or Git could not be run
//
// Codes in a final GitResult: index_locked and ref_locked (a lock appeared
// after the checks), git_failed (Git refused; see Output), commit_failed
// (the commit did not happen, for example a pre-commit or commit-msg hook
// rejected it), cancelled (server stop or the 5 minute budget ended Git;
// state outcome_unknown), interrupted (server restarted while running),
// internal_error (the server failed while Git ran; outcome_unknown),
// stale_entry (the worktree changed between the checks and Git; nothing ran). A
// succeeded result may carry a warning code: staged_newer_content (the file
// changed between the check and `git add`; the newer bytes were staged) or
// hooks_changed_content (what was committed differs from the pinned staged
// set: hooks or another process changed the index during the commit).

// GitWrite is Command.Git.
//
//   - git.stage: exactly one Paths entry from the unstaged or untracked group.
//     A deletion is staged as a removal.
//   - git.unstage: exactly one staged entry; a staged rename unstages both
//     sides. Works on an unborn branch.
//   - git.stage and git.unstage with Partial set and no Paths stage or
//     unstage selected hunks and lines of one path (ADR 0025,
//     git_partial.go; capability git-partial-stage).
//   - git.discard: exactly one unstaged (restored from the index) or untracked
//     entry (deleted permanently: regular files and symlinks only, never a
//     symlink target, directory or nested repository). Requires Confirmed; the
//     Pin is the confirmation of what was shown. Discarding an entry with
//     GitStatusEntry.IntentToAdd empties the file, as `git restore` does; its
//     content was never stored by Git and cannot be recovered, so present it
//     as permanent like an untracked delete.
//   - git.commit: Message (1 byte to 64 KiB, `--cleanup=whitespace`, so `#`
//     lines are kept), ExpectedHead (GitStatus.HeadOid, or GitUnbornHead) and
//     StagedFingerprint (GitStatus.StagedFingerprint). Amend replaces the
//     pinned HEAD commit and is allowed with nothing staged; it needs
//     AcknowledgePublished when GitStatus.HeadOnUpstream is set.
//
// The ref and remote kinds carry exactly one typed sub-payload and none of
// the fields above: Ref for git.branch_create, git.switch and git.reset_soft,
// Sync for git.fetch, git.pull and git.push, Cancel for git.cancel.
type GitWrite struct {
	Paths                []GitPathPin `json:"paths,omitempty"`
	Message              string       `json:"message,omitempty"`
	Amend                bool         `json:"amend,omitempty"`
	ExpectedHead         string       `json:"expected_head,omitempty"`
	StagedFingerprint    string       `json:"staged_fingerprint,omitempty"`
	AcknowledgePublished bool         `json:"acknowledge_published,omitempty"`
	Confirmed            bool         `json:"confirmed,omitempty"`
	// Additive (ADR 0021).
	Ref    *GitRefWrite `json:"ref,omitempty"`
	Sync   *GitSync     `json:"sync,omitempty"`
	Cancel *GitCancel   `json:"cancel,omitempty"`
	// Additive (ADR 0023).
	Integrate *GitIntegrate      `json:"integrate,omitempty"`
	Operation *GitOperationWrite `json:"operation,omitempty"`
	// Additive (S3).
	Conflict *GitConflictWrite `json:"conflict,omitempty"`
	// Additive (S4).
	ResolveJob *GitResolveJob `json:"resolve_job,omitempty"`
	// Additive (ADR 0025): hunk/line selection for git.stage and
	// git.unstage (git_partial.go); Paths is then empty.
	Partial *GitPartial `json:"partial,omitempty"`
}

// GitPathPin names one status entry exactly as GitStatus showed it.
type GitPathPin struct {
	Path  string `json:"path"`
	Group string `json:"group"`
	Pin   string `json:"pin"`
}

// GitResult is Receipt.Git: the outcome of one Git write. Commit is the new
// HEAD commit after git.commit. Output is Git's and the hooks' combined
// stdout/stderr, bounded to 64 KiB, verbatim and unsanitized.
type GitResult struct {
	Op              string `json:"op"`
	State           string `json:"state"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	Commit          string `json:"commit,omitempty"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	// Additive (ADR 0021). Paths lists the files Git named for
	// would_overwrite. Ref, Fetch, Integration and Push describe the parts of
	// a ref or remote action; a pull reports Fetch and Integration
	// separately, so "fetched, not integrated: diverged" is explicit.
	Paths []string `json:"paths,omitempty"`
	// PathsIncomplete reports that Paths may be wrong or partial: Git
	// prints the names verbatim, so a name containing a newline (or a list
	// that could not be matched to existing files) cannot be parsed
	// reliably. Show Output instead.
	PathsIncomplete bool            `json:"paths_incomplete,omitempty"`
	Ref             *GitRefResult   `json:"ref,omitempty"`
	Fetch           *GitFetchResult `json:"fetch,omitempty"`
	// Fetches is git.fetch with All: one entry per configured remote.
	Fetches     []GitFetchResult `json:"fetches,omitempty"`
	Integration *GitIntegration  `json:"integration,omitempty"`
	Push        *GitPushResult   `json:"push,omitempty"`
	// Operation (additive, ADR 0023) reports git.merge, git.rebase and the
	// git.operation_* commands.
	Operation *GitOperationResult `json:"operation,omitempty"`
}

// GitOp is the latest Git write per repository, published in
// Snapshot.GitOps so every client sees progress and outcomes. Checkout is the
// repository toplevel; ThreadID or ProjectID is the command's target. Output
// keeps only the last 8 KiB; the receipt holds up to 64 KiB. Times are
// RFC 3339.
type GitOp struct {
	Checkout   string   `json:"checkout"`
	CommandID  string   `json:"command_id"`
	Op         string   `json:"op"`
	State      string   `json:"state"`
	Code       string   `json:"code,omitempty"`
	Message    string   `json:"message,omitempty"`
	Commit     string   `json:"commit,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	ThreadID   string   `json:"thread_id,omitempty"`
	ProjectID  string   `json:"project_id,omitempty"`
	Output     string   `json:"output,omitempty"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at,omitempty"`
	// Additive (ADR 0021). Progress is the latest phase Git reported for a
	// running fetch, pull or push, published at most four times a second and
	// never journaled (it is dropped when the write ends and on restart).
	// Cancellable reports that git.cancel would currently be accepted.
	Progress    *GitProgress `json:"progress,omitempty"`
	Cancellable bool         `json:"cancellable,omitempty"`
}

// Ref and remote actions (ADR 0021) use the same durable two-phase flow as
// the writes above, with these differences:
//
//   - Lease: git.switch, git.reset_soft and git.pull hold the checkout writer
//     lease; git.branch_create, git.fetch and git.push do not (they never
//     touch the index or working tree) and so run beside agent turns. All of
//     them take a Git slot (git_busy): one Git write per worktree, and a
//     ref or remote action also excludes every Git write in the
//     repository's linked worktrees, which share its refs.
//   - Budgets: fetch and pull end Git after 60 s without any output, push
//     after 5 minutes (a pre-push hook may run silently) (code timeout,
//     "no output ... (a hook or the remote)"), and all three after 15
//     minutes in total; switch and reset have 5 minutes, branch creation
//     the ordinary budget.
//   - Credentials are never prompted for. Credential helpers, ssh-agent and
//     already-trusted host keys work as they do for the user's CLI, and so
//     do the user's GIT_SSH*, GIT_SSL_*, GIT_HTTP_*, GIT_PROXY_*,
//     GIT_CONFIG_GLOBAL/SYSTEM/NOSYSTEM, GIT_ALLOW_PROTOCOL,
//     GIT_PROTOCOL_FROM_USER, author/committer variables and proxy
//     variables. GIT_TERMINAL_PROMPT=0, GIT_ASKPASS is empty (which
//     also disables core.askPass and SSH_ASKPASS for Git), SSH_ASKPASS is
//     removed, SSH_ASKPASS_REQUIRE=never, GCM_INTERACTIVE=never, fetch,
//     pull and push run without DISPLAY or WAYLAND_DISPLAY, and Git has no
//     controlling terminal. A host key that is not yet trusted or a key that
//     needs a passphrase fails; fetch once in a terminal (or load the key
//     into ssh-agent) and retry. A signed push (push.gpgSign) that needs a
//     pinentry fails (signing_failed) unless gpg-agent can sign without one.
//   - Cancel: git.cancel ends a running fetch or push, or a pull while it is
//     still fetching (GitOp.Cancellable). git.cancel never ends a switch,
//     reset, branch creation or a pull's integration step once started (only
//     server stop or the budget can, as for the writes above).
//   - Files: every file-updating command runs with --no-overwrite-ignore and
//     without submodule recursion, never with --merge, --force, stash or
//     autostash.
//
// Additional refusal codes (protocol.Error.Code, nothing recorded):
//
//	branch_exists          git.branch_create or switch-create of a name that
//	                       already exists
//	unknown_commit         StartOid or TargetOid is not a commit here
//	stale_branch           the checked-out branch differs from ExpectedBranch
//	stale_head             HEAD differs from ExpectedHead
//	stale_target           the switch target's tip differs from TargetOid
//	stale_status           the working tree differs from WorktreeFingerprint
//	stale_upstream         the upstream differs from Upstream, or its
//	                       remote-tracking tip from ExpectedUpstreamOid
//	carry_unacknowledged   switching with changes whose count the user did
//	                       not acknowledge (AcknowledgeCarry != entry count)
//	status_truncated       switching with more changes than status lists
//	checked_out_elsewhere  the branch is checked out in another worktree
//	already_on_branch      switch to the branch that is already checked out
//	operation_in_progress  merge, rebase, cherry-pick, revert or bisect
//	conflicted             the index has unmerged paths
//	nothing_to_reset       soft reset on an unborn branch
//	already_at_target      soft reset to the current HEAD
//	published_commit       soft reset leaves commits that a remote-tracking
//	                       ref already contains, without AcknowledgePublished
//	not_ancestor           soft reset to a commit that is neither an
//	                       ancestor nor a descendant of HEAD, without
//	                       AcknowledgeNotAncestor
//	detached               pull or push with a detached HEAD
//	no_upstream            the branch has no upstream (or fetch without
//	                       Remote and no upstream)
//	upstream_gone          the upstream's remote-tracking ref does not exist
//	unknown_remote         Remote is not a configured remote
//	behind_upstream        push while the remote-tracking tip is not an
//	                       ancestor of HEAD (behind or diverged); pull first
//	not_running            git.cancel of a command that is not running
//	not_cancellable        git.cancel of a command past its cancellable phase
//	leaves_commits         switch away from a detached HEAD whose commits no
//	                       branch, tag or remote-tracking ref contains,
//	                       without AcknowledgeLeaveCommits equal to their
//	                       number; the message starts with that number
//	upstream_name_mismatch push of a branch whose upstream has another name
//	                       while push.default is simple (the default),
//	                       current or matching (Git would not push there);
//	                       allowed with push.default=upstream
//	ref_locked             also: a lock on the branch refs this action
//	                       updates, or on packed-refs
//	not_supported          a local (".") upstream, a push remote that differs
//	                       from the upstream's remote, push.default=nothing,
//	                       a mirror remote, a configured remote.<name>.push, a pull on an unborn branch, or
//	                       an unsafe remote or branch name
//
// Additional codes in a final GitResult:
//
//	would_overwrite   switch or pull integration refused by Git because
//	                  local changes or untracked (including ignored) files
//	                  would be overwritten or a directory would lose
//	                  untracked files; Paths lists them (PathsIncomplete
//	                  when they cannot be parsed reliably). Nothing moved.
//	partial_switch    outcome_unknown: HEAD did not move, but Git changed
//	                  files or the index before failing (for example a
//	                  lock appeared); review status. Paths may be set.
//	ref_locked        a fetch could not update a locked local ref
//	signing_failed    push.gpgSign could not sign; nothing was sent
//	document_unsaved  an open document could not be saved before a switch
//	                  or pull rewrote files; Git did not run
//	stale_status      (switch) saving open documents changed the working
//	                  tree after it was checked; Git did not run
//	leaves_commits    (switch) more detached commits than acknowledged
//	                  appeared after the check; Git did not run
//	auth_required     the remote wants credentials that no helper or agent
//	                  supplied (HTTP 401/403, "Permission denied")
//	host_key_unknown  SSH host key not trusted (or changed); fetch once in a
//	                  terminal to review and accept it
//	agent_unavailable ssh-agent could not be reached or refused to sign
//	transport         any other connection or remote failure
//	timeout           no output for 60 s (fetch, pull) or 5 minutes (push),
//	                  or the 15 minute budget ended
//	cancelled         git.cancel, or server stop
//	rejected          push refused: Push.Reason says why (non-fast-forward,
//	                  fetch first, remote hook, or "pre-push hook")
//	diverged          pull fetched but did not integrate: local and upstream
//	                  have both moved; merge or rebase explicitly
//	upstream_gone     the upstream branch no longer exists on the remote
//	stale_head        HEAD (for push, the branch) moved between the checks
//	                  and Git; nothing changed (a pull has still fetched)
//	hook_failed       warning on a succeeded switch: it happened, but the
//	                  post-checkout hook exited non-zero (see Output); Git
//	                  ignores post-merge's status, so pull never reports it
//	pushed_newer_head warning on a succeeded push: the branch moved between
//	                  the check and the push, so a newer commit than
//	                  ExpectedHead was pushed; Push.NewOid names it
//
// A fetch, or a pull still fetching, ended by git.cancel (cancelled) or by
// the stall (timeout) is failed: the branch and files are unchanged,
// though some remote-tracking refs may have been updated. A push ended that
// way, or failing in transport, is outcome_unknown: the remote may have
// accepted it; so is a push whose remote reported "remote failure" or no
// status. A pre-push hook refusal (rejected, Reason "pre-push hook") is
// claimed only when Git's own trace shows the pre-push hook (a hooks file
// or a hook.<name> entry) exiting non-zero. Server stop (cancelled) and the 15 minute budget (timeout)
// make any of them outcome_unknown, as for the writes above.

// GitRefWrite is GitWrite.Ref.
//
//   - git.branch_create: Name (a valid new branch name) and StartOid (a full
//     commit hash, typically the commit selected in the graph). No tracking
//     is configured. Does not switch.
//   - git.switch: either Branch (an existing local branch) with TargetOid
//     (its tip as shown), or Name with StartOid to create a branch there and
//     switch to it. Always ExpectedBranch (GitStatus.Branch, empty when
//     detached), ExpectedHead (GitStatus.HeadOid or GitUnbornHead) and
//     WorktreeFingerprint (GitWorktreeFingerprint of the status shown). When
//     that status listed changes, AcknowledgeCarry must equal the number of
//     entries the user was shown and accepted carrying; Git semantics then
//     apply: what Git carries is carried, and when a change would be
//     overwritten nothing happens (would_overwrite).
//   - git.reset_soft: TargetOid (full hash), ExpectedBranch and ExpectedHead.
//     Moves HEAD (and the branch when attached) only; index and files are
//     unchanged. AcknowledgePublished is required when commits leaving the
//     branch are on a remote-tracking ref; AcknowledgeNotAncestor when the
//     target is neither an ancestor nor a descendant of HEAD. The result's
//     Ref.PreviousHead is the undo target: a soft reset back to it (a
//     descendant, so no acknowledgement) restores the branch exactly.
type GitRefWrite struct {
	Name                   string `json:"name,omitempty"`
	StartOid               string `json:"start_oid,omitempty"`
	Branch                 string `json:"branch,omitempty"`
	TargetOid              string `json:"target_oid,omitempty"`
	ExpectedBranch         string `json:"expected_branch,omitempty"`
	ExpectedHead           string `json:"expected_head,omitempty"`
	WorktreeFingerprint    string `json:"worktree_fingerprint,omitempty"`
	AcknowledgeCarry       int    `json:"acknowledge_carry,omitempty"`
	AcknowledgePublished   bool   `json:"acknowledge_published,omitempty"`
	AcknowledgeNotAncestor bool   `json:"acknowledge_not_ancestor,omitempty"`
	// AcknowledgeLeaveCommits (switch from a detached HEAD) is the number
	// of commits reachable only from HEAD that the user accepted leaving
	// behind; see leaves_commits.
	AcknowledgeLeaveCommits int `json:"acknowledge_leave_commits,omitempty"`
}

// GitSync is GitWrite.Sync.
//
//   - git.fetch: Remote, or empty for the current branch's upstream remote.
//     Fetches that remote's configured refspecs (fetch.prune applies as in
//     the CLI) without submodules.
//   - git.fetch with All (capability git-fetch-all): every configured remote
//     in `git remote` order, one `git fetch --prune|--no-prune
//     --no-prune-tags <remote>` each; Remote must be empty. Prune removes
//     remote-tracking refs whose branch is gone; without it
//     fetch.prune/remote.*.prune are overridden, and tags are never pruned
//     (fetch.pruneTags is overridden). Like `git fetch --all`, a remote with
//     remote.<name>.skipFetchAll is not fetched (state skipped). Checked
//     immediately before each remote's fetch, a remote whose fetch refspecs
//     have a destination outside refs/remotes/<name>/, or whose namespace
//     overlaps another remote's (case-insensitively), is refused (code
//     unsafe_refspec), so only remote-tracking refs are removed barring a
//     configuration change in the window before Git reads it (any other
//     removed ref is reported in RemovedOther). GitResult.Fetches lists every remote (a failed remote never
//     hides the others); Fetch is a copy of the upstream remote's entry,
//     whatever its state. State is succeeded when no remote failed or was
//     cancelled; otherwise failed with code cancelled, partial_fetch (some
//     remote succeeded), the failures' shared code when every attempted
//     remote failed with the same code, or fetch_failed.
//   - git.pull: ExpectedBranch, ExpectedHead and Upstream (GitStatus.Upstream)
//     as shown. Like `git pull`, fetches exactly the upstream branch
//     (FETCH_HEAD; the remote-tracking ref is updated when the remote's
//     refspecs map it), then compares HEAD with the fetched commit
//     (Fetch.UpstreamAfter): up_to_date and ahead succeed without change,
//     fast_forward runs `git merge --ff-only --no-autostash
//     --no-overwrite-ignore <tip>`, diverged stops (code diverged). Never
//     merges, rebases or runs `git pull`.
//   - git.push: ExpectedBranch, ExpectedHead and Upstream as shown, and
//     optionally ExpectedUpstreamOid (the remote-tracking tip the user saw,
//     for example GitFetchResult.UpstreamAfter). Runs `git push --porcelain
//     <remote> refs/heads/<branch>:<upstream ref>`: the current branch to its
//     configured upstream only, never forced, no tags; the pre-push hook
//     runs. As with the CLI's push.default, an upstream of another name is
//     used only with push.default=upstream. Refused while the local remote-tracking tip is not an ancestor
//     of HEAD; the remote itself still rejects a push that is not a fast
//     forward of what it has now (code rejected).
type GitSync struct {
	Remote              string `json:"remote,omitempty"`
	All                 bool   `json:"all,omitempty"`
	Prune               bool   `json:"prune,omitempty"`
	Upstream            string `json:"upstream,omitempty"`
	ExpectedBranch      string `json:"expected_branch,omitempty"`
	ExpectedHead        string `json:"expected_head,omitempty"`
	ExpectedUpstreamOid string `json:"expected_upstream_oid,omitempty"`
}

// GitCancel is GitWrite.Cancel: the ID of the running command to cancel.
// git.cancel is not journaled and needs no thread or project; its receipt
// (state succeeded) only confirms that cancellation was requested. The
// cancelled command's own receipt reports what happened.
type GitCancel struct {
	CommandID string `json:"command_id"`
}

// GitRefResult reports a branch creation, switch or soft reset. Head and
// PreviousHead are full hashes (PreviousHead empty when it was unborn);
// Branch is the branch now checked out (or created), empty when detached.
// Carried is the number of status entries that were present when a switch
// started.
type GitRefResult struct {
	Branch         string `json:"branch,omitempty"`
	PreviousBranch string `json:"previous_branch,omitempty"`
	Head           string `json:"head,omitempty"`
	PreviousHead   string `json:"previous_head,omitempty"`
	Carried        int    `json:"carried,omitempty"`
}

// Fetch and integration states.
const (
	GitFetchSucceeded = "succeeded"
	GitFetchFailed    = "failed"
	GitFetchCancelled = "cancelled"
	// GitFetchSkipped: all-remotes fetch only, remote.<name>.skipFetchAll.
	GitFetchSkipped = "skipped"

	GitIntegrationUpToDate    = "up_to_date"
	GitIntegrationAhead       = "ahead"
	GitIntegrationFastForward = "fast_forward"
	GitIntegrationDiverged    = "diverged"
	GitIntegrationFailed      = "failed"
	GitIntegrationNotStarted  = "not_started"
)

// GitFetchResult is the fetch part of git.fetch and git.pull. Upstream is
// the current branch's upstream (for example origin/main) when it belongs to
// Remote; UpstreamBefore and UpstreamAfter are its remote-tracking tips
// around the fetch (empty when absent).
type GitFetchResult struct {
	Remote         string `json:"remote"`
	State          string `json:"state"`
	Code           string `json:"code,omitempty"`
	Upstream       string `json:"upstream,omitempty"`
	UpstreamBefore string `json:"upstream_before,omitempty"`
	UpstreamAfter  string `json:"upstream_after,omitempty"`
	// All-remotes fetch only. Message explains a failed, skipped or not
	// started remote. Pruned lists refs/remotes/ refs (short form,
	// origin/topic) that existed before the whole run, were removed during
	// this remote's fetch and are absent at its end, sorted and capped at
	// GitFetchPrunedMax; PrunedMore counts the rest.
	Message    string   `json:"message,omitempty"`
	Pruned     []string `json:"pruned,omitempty"`
	PrunedMore int      `json:"pruned_more,omitempty"`
	// RemovedOther lists full names of refs outside refs/remotes/ that
	// disappeared during this remote's fetch. Unsafe refspecs are refused,
	// so it is a safety net that should stay empty.
	RemovedOther []string `json:"removed_other,omitempty"`
}

// GitFetchPrunedMax bounds GitFetchResult.Pruned.
const GitFetchPrunedMax = 100

// GitIntegration is the integration part of git.pull. From is HEAD before;
// To is the upstream tip it was compared with (HEAD after a fast forward).
type GitIntegration struct {
	State string `json:"state"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// Push states.
const (
	GitPushPushed   = "pushed"
	GitPushUpToDate = "up_to_date"
	GitPushRejected = "rejected"
	GitPushUnknown  = "unknown"
)

// GitPushResult is the push part of git.push. Reason is Git's rejection
// reason, verbatim (for example "non-fast-forward", "fetch first" or a remote
// hook's message), or "pre-push hook" when the local hook declined. NewOid is
// the remote-tracking tip after the push.
type GitPushResult struct {
	Remote    string `json:"remote"`
	RemoteRef string `json:"remote_ref"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	OldOid    string `json:"old_oid,omitempty"`
	NewOid    string `json:"new_oid,omitempty"`
}

// GitProgress is GitOp.Progress: Git's latest progress phase (for example
// "Receiving objects") and its percentage, -1 when Git gave none.
type GitProgress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
}

// Worktree fingerprint markers.
const (
	GitFingerprintTruncated  = "truncated"
	GitFingerprintUnpinnable = "unpinnable"
)

// GitWorktreeFingerprint digests every entry of a status as shown (group,
// path and Pin), so git.switch can prove the working tree is still what the
// user reviewed. It is GitFingerprintTruncated when status did not list
// every entry, and GitFingerprintUnpinnable when an entry has no Pin; the
// server refuses a switch with changes in both cases. The server recomputes
// it from a fresh status with the same function.
func GitWorktreeFingerprint(st GitStatus) string {
	if st.Truncated {
		return GitFingerprintTruncated
	}
	lines := make([]string, 0, len(st.Entries))
	for _, e := range st.Entries {
		if e.Pin == "" {
			return GitFingerprintUnpinnable
		}
		lines = append(lines, e.Group+"\x00"+e.Path+"\x00"+e.Pin)
	}
	sort.Strings(lines)
	h := sha256.New()
	h.Write([]byte("worktree-v1\x00"))
	for _, l := range lines {
		h.Write([]byte(l + "\x00\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Merge and rebase (ADR 0023) use the same durable two-phase flow and hold
// the checkout writer lease (checkout_busy while a thread works there; agent
// turns wait while they run) and a Git slot that also excludes Git writes in
// linked worktrees (git_busy). Every command that rewrites files first saves
// and pauses open shared documents and reconciles them afterwards
// (document_unsaved when a document could not be saved; Git did not run).
// Git runs with the user's configuration (hooks, signing, merge drivers),
// never with an editor (the message Git prepared is kept), and with
// rerere.autoUpdate off, so a recorded resolution that rerere replays is
// written to the file but never staged (GitOperationResult.RerereResolved).
//
// Untracked and ignored files an abort or skip lists in its discards are
// backed up and, once acknowledged, moved aside so Git can proceed; any
// that Git did not replace are put back byte-exact from the backup
// afterwards (also when Git fails), and the result message says so.
//
// Every operation command runs with submodule.recurse=false, so no
// submodule worktree is changed. Before an abort or skip changes anything,
// the server backs up every file it would overwrite
// (GitOperationResult.Backup, GitOperationBackup).
//
// While a merge, rebase, cherry-pick, revert or am is in progress in a
// repository, whether started here or outside the application, agent turns
// in checkouts overlapping it do not start; they wait with
// WriterWait.HolderOperation (bisect does not block them). A running turn is
// not interrupted. Merge, rebase, continue and skip have a 30 minute budget
// (hooks included); abort has 5 minutes.
//
//   - git.merge, git.rebase: Integrate. Require an attached branch with a
//     clean tracked tree (untracked files are allowed and never overwritten;
//     staged or unstaged changes are refused, even when merge.autoStash or
//     rebase.autoStash is configured: nothing is ever stashed), and no
//     assume-unchanged or skip-worktree entry on a path the operation
//     writes. Merge runs `git merge --no-autostash --no-overwrite-ignore
//     --no-rerere-autoupdate --no-edit -m <message> <target oid>`; merge.ff
//     and branch.<name>.mergeOptions apply as in the CLI (see
//     GitIntegratePreview.MergeFF); the message is Git's usual
//     "Merge branch '<name>'", "Merge remote-tracking branch '<name>'" or
//     "Merge commit '<oid>'" (merge.log still appends a shortlog). Rebase
//     is non-interactive only: `git rebase --merge --no-autostash
//     --no-update-refs --no-rerere-autoupdate --no-fork-point
//     --no-autosquash <target oid>`, so no other branch moves and no fork
//     point guess changes the range; a range containing merge commits is
//     refused.
//   - git.operation_abort: returns to the state before the operation
//     (`git <kind> --abort`), discarding resolution work. Needs Confirmed and
//     the WorktreeFingerprint shown. When GitOperationState.DiscardsOnAbort
//     is not empty, those files' changes are reset too: the confirmation
//     must name them ("Abort the rebase and reset changes to: a.txt, b.txt")
//     and AcknowledgeDiscard must list exactly them.
//   - git.operation_continue: after every conflict is resolved and staged,
//     `git <kind> --continue` without an editor (a rebase keeps each
//     commit's message, a merge uses MERGE_MSG). It may stop again at the
//     next conflict. Needs Confirmed and the StagedFingerprint shown (what
//     will be committed), plus AcknowledgeMarkers listing exactly
//     GitOperationState.MarkerPaths when staged files still contain
//     conflict markers. Not offered at an interactive stop (StopReason).
//   - git.operation_skip: rebase only, stopped at a commit with conflicts:
//     drops that commit (SkipOid, which the confirmation must name) and
//     continues. Needs Confirmed, the WorktreeFingerprint shown and, as for
//     abort, AcknowledgeDiscard naming DiscardsOnSkip. Never offered to
//     agents or at an interactive stop.
//
// The operation commands work for operations started outside the
// application too (GitOperationState.Source external). Bisect is never
// managed here.
//
// Additional refusal codes (protocol.Error.Code, nothing recorded):
//
//	dirty_tree            staged or unstaged changes (or conflicts) exist;
//	                      commit or discard them first (untracked files are
//	                      fine)
//	detached              merge or rebase with a detached HEAD
//	unborn                merge or rebase on a branch without commits
//	already_up_to_date    the target is already contained in HEAD
//	stale_target          the target ref no longer points at TargetOid (or
//	                      does not exist)
//	stale_upstream        Source upstream, but the branch's upstream is not
//	                      TargetRef
//	no_upstream           Source upstream on a branch without one
//	unknown_commit        TargetOid is not a commit here
//	stale_range           the commits a rebase would replay differ from
//	                      ExpectedReplayCount
//	range_has_merges      the rebase range contains merge commits; use a
//	                      terminal
//	published_commit      rebase would rewrite a commit that is on a
//	                      remote-tracking ref, without AcknowledgePublished
//	would_overwrite       an untracked (including ignored) file is in the
//	                      way of a file the operation writes; the message
//	                      names up to 20 paths
//	operation_in_progress another operation (or bisect) is in progress
//	no_operation          git.operation_* while nothing is in progress
//	stale_operation       a different operation, step or stopped commit
//	                      than the request names (or OperationID does not
//	                      match the recorded operation)
//	conflicted            continue while unmerged paths remain
//	stale_status          continue with an UnmergedFingerprint that differs
//	                      from the current one
//	not_stopped           skip while the rebase is not stopped at a commit
//	confirmation_required abort, continue or skip without Confirmed
//	identity_missing      Git cannot determine the committer identity
//	not_supported         bisect or am; an interactive rebase stop (edit,
//	                      exec, break, reword, squash...); index entries
//	                      marked assume-unchanged or skip-worktree on a path
//	                      the merge or rebase writes (they may hide local
//	                      changes); or too many paths to check (use a
//	                      terminal)
//	ff_only_configured    merge.ff=only (or --ff-only in the branch's
//	                      mergeOptions) and the merge is not a fast forward
//	discards_unacknowledged abort or skip would reset changes outside the
//	                      conflicts (DiscardsOnAbort/DiscardsOnSkip) that
//	                      AcknowledgeDiscard does not list exactly; the
//	                      message names them
//	markers_unacknowledged continue with staged conflict markers
//	                      (MarkerPaths) that AcknowledgeMarkers does not list
//	                      exactly
//	abort_blocked         a merge, cherry-pick or revert abort while files it
//	                      resets have unstaged changes (AbortBlockedBy)
//	drops_unacknowledged  abort of a cherry-pick or revert sequence without
//	                      AcknowledgeDropped equal to AbortDropsFingerprint
//	markers_incomplete    continue when the marker scan was incomplete
//	                      (MarkersIncomplete) without
//	                      AcknowledgeMarkersIncomplete
//	backup_incomplete     abort or skip when not everything it overwrites
//	                      can be backed up (BackupIncomplete) without
//	                      AcknowledgeBackupIncomplete
//	would_overwrite       also: continue or skip while ContinueInTheWay is
//	                      not empty
//	status_truncated      abort or skip when the changes they would reset
//	                      could not be listed (DiscardsIncomplete)
//	stale_status          also: the working tree (abort, skip) or the staged
//	                      set (continue) differs from its fingerprint
//
// Codes in a final GitResult (see GitOperationResult.Outcome for what
// happened to the operation):
//
//	stopped_conflicts  succeeded: Git stopped with conflicts; Operation.State
//	                   lists them
//	stopped            succeeded: Git stopped without conflicts (for example
//	                   a hook refused the merge commit); review, then
//	                   Continue or Abort
//	hook_failed        succeeded: the operation finished but a hook after it
//	                   (post-rewrite, post-merge) exited non-zero
//	nothing_to_commit  failed: the resolution leaves nothing to commit, so
//	                   Git did not continue; Skip drops the commit (rebase),
//	                   or finish it in a terminal
//	would_overwrite    failed: Git refused before changing anything; Paths
//	                   lists the files
//	partial_change     outcome_unknown: Git failed without starting the
//	                   operation but files or the index changed
//	stale_head, stale_target, stale_operation, stale_status
//	                   failed: something changed between the checks and Git
//	                   (including open documents being saved); nothing ran
//	abort_incomplete   succeeded with a warning: the abort ended the
//	                   operation, but tracked paths it restores still differ
//	                   from what it restored (for example Git could not
//	                   remove a directory); Paths lists them
//	backup_incomplete  failed: abort or skip could not back up everything it
//	                   overwrites; nothing was changed
//	timeout            outcome_unknown: merge, rebase, continue or skip
//	                   exceeded its 30 minute budget (5 minutes for abort),
//	                   whatever Git had done by then; the message says what
//	                   the operation looks like now and whether Git left
//	                   index.lock behind
//	documents_changed_tree
//	                   failed (merge, rebase): saving open documents left
//	                   staged or unstaged changes to tracked files; commit
//	                   or discard them and start again; nothing ran
//	git_failed         failed (nothing changed) or outcome_unknown
//	document_unsaved   an open document could not be saved; Git did not run

// GitIntegrate is GitWrite.Integrate for git.merge and git.rebase. It pins
// what the user reviewed: the checked-out branch and HEAD (GitStatus.Branch
// and HeadOid), and the target commit TargetOid. Source is upstream (the
// branch's configured upstream, TargetRef its full name), branch (a local
// branch or remote-tracking ref, TargetRef its full name, as GitBranch.Ref)
// or commit (TargetRef empty). A ref's tip is re-checked immediately before
// Git runs and a moved ref is refused (stale_target); Git is always given
// TargetOid, never the name. Rebase also sends ExpectedReplayCount
// (GitIntegratePreview.ReplayCount) and AcknowledgePublished when the
// preview reported Published; merge sends neither.
type GitIntegrate struct {
	Source               string `json:"source"`
	TargetRef            string `json:"target_ref,omitempty"`
	TargetOid            string `json:"target_oid"`
	ExpectedBranch       string `json:"expected_branch"`
	ExpectedHead         string `json:"expected_head"`
	ExpectedReplayCount  int    `json:"expected_replay_count,omitempty"`
	AcknowledgePublished bool   `json:"acknowledge_published,omitempty"`
}

// GitOperationWrite is GitWrite.Operation for git.operation_abort,
// git.operation_continue and git.operation_skip, pinning the
// GitOperationState the user reviewed: Kind, ExpectedHead (HeadOid) and, for
// continue and skip, ExpectedStep (Step). Continue also sends
// UnmergedFingerprint (empty once every conflict is staged); skip sends
// SkipOid (Current.Oid, the commit being dropped). OperationID, when set,
// must be the recorded application operation. Confirmed is required for all
// three.
//
// Additive (2026-09-25 review): WorktreeFingerprint (abort, skip) and
// StagedFingerprint (continue) as the state showed them; AcknowledgeDiscard
// (abort, skip) listing exactly the state's DiscardsOnAbort or
// DiscardsOnSkip once the user accepted resetting them; AcknowledgeMarkers
// (continue) listing exactly MarkerPaths.
type GitOperationWrite struct {
	OperationID         string   `json:"operation_id,omitempty"`
	Kind                string   `json:"kind"`
	ExpectedHead        string   `json:"expected_head"`
	ExpectedStep        int      `json:"expected_step,omitempty"`
	UnmergedFingerprint string   `json:"unmerged_fingerprint,omitempty"`
	SkipOid             string   `json:"skip_oid,omitempty"`
	Confirmed           bool     `json:"confirmed,omitempty"`
	WorktreeFingerprint string   `json:"worktree_fingerprint,omitempty"`
	StagedFingerprint   string   `json:"staged_fingerprint,omitempty"`
	AcknowledgeDiscard  []string `json:"acknowledge_discard,omitempty"`
	AcknowledgeMarkers  []string `json:"acknowledge_markers,omitempty"`
	// Additive (second review): AcknowledgeMarkersIncomplete (continue)
	// accepts that the marker scan did not cover every staged file;
	// AcknowledgeBackupIncomplete (abort, skip) accepts running without a
	// complete backup (GitOperationState.BackupIncomplete).
	AcknowledgeMarkersIncomplete bool `json:"acknowledge_markers_incomplete,omitempty"`
	AcknowledgeBackupIncomplete  bool `json:"acknowledge_backup_incomplete,omitempty"`
	// Additive (third review): the fingerprint of the discard list (abort:
	// DiscardsOnAbortFingerprint, skip: DiscardsOnSkipFingerprint) or of
	// MarkerPaths (continue) the user accepted, as an alternative to listing
	// the paths in AcknowledgeDiscard or AcknowledgeMarkers.
	DiscardsFingerprint string `json:"discards_fingerprint,omitempty"`
	MarkersFingerprint  string `json:"markers_fingerprint,omitempty"`
	// AcknowledgeDropped (fourth review, abort of a cherry-pick or revert
	// sequence): GitOperationState.AbortDropsFingerprint once the user
	// accepted removing those commits.
	AcknowledgeDropped string `json:"acknowledge_dropped,omitempty"`
	// AcknowledgeBackupMissing (fifth review, abort and skip) is the
	// fingerprint of the backup's missing files the user accepted
	// (BackupMissingOnAbortFingerprint or BackupMissingOnSkipFingerprint);
	// the files the backup actually could not copy must match it, or
	// nothing runs (backup_incomplete). It supersedes
	// AcknowledgeBackupIncomplete, which alone no longer suffices.
	AcknowledgeBackupMissing string `json:"acknowledge_backup_missing,omitempty"`
	// AcknowledgeAgentChanges (S4 gate, continue) is
	// GitAgentChanges.Fingerprint of the unexplained index changes the user
	// reviewed and accepts committing.
	AcknowledgeAgentChanges string `json:"acknowledge_agent_changes,omitempty"`
}

// GitBackupEntry records one abort or skip backup in Snapshot.GitBackups
// (at most 20 per repository toplevel, Checkout), so GET
// /v1/git/operation/backup serves only recorded backups.
//
// CheckoutKey (fourth review) is the toplevel's bytes in standard base64,
// which matches even when the path is not valid UTF-8 (JSON would replace
// such bytes in Checkout).
type GitBackupEntry struct {
	Checkout    string `json:"checkout"`
	CheckoutKey string `json:"checkout_key,omitempty"`
	Oid         string `json:"oid"`
	IndexOid    string `json:"index_oid"`
	CommandID   string `json:"command_id"`
	CreatedAt   string `json:"created_at"`
}

// Operation outcomes (GitOperationResult.Outcome).
const (
	GitOutcomeCompleted        = "completed"
	GitOutcomeStoppedConflicts = "stopped_conflicts"
	GitOutcomeStopped          = "stopped"
	GitOutcomeAborted          = "aborted"
	GitOutcomeNotStarted       = "not_started"
	GitOutcomeUnchanged        = "unchanged"
	GitOutcomeUnknown          = "unknown"
)

// GitOperationResult is GitResult.Operation. Outcome: completed (the merge
// commit or rebased branch exists and nothing is in progress),
// stopped_conflicts or stopped (State is the operation now in progress),
// aborted, not_started (a merge or rebase that Git refused before starting),
// unchanged (an abort, continue or skip that did not change the operation)
// or unknown. HeadBefore and HeadAfter are full hashes. Skipped is the
// commit a skip dropped. RerereResolved lists paths Git's rerere rewrote
// from a recorded resolution; they are still unmerged and must be reviewed.
type GitOperationResult struct {
	OperationID    string              `json:"operation_id,omitempty"`
	Kind           string              `json:"kind"`
	Outcome        string              `json:"outcome"`
	HeadBefore     string              `json:"head_before,omitempty"`
	HeadAfter      string              `json:"head_after,omitempty"`
	State          *GitOperationState  `json:"state,omitempty"`
	Skipped        *GitOperationCommit `json:"skipped,omitempty"`
	RerereResolved []string            `json:"rerere_resolved,omitempty"`
	// Backup (additive) is the copy made before an abort or skip.
	Backup *GitOperationBackup `json:"backup,omitempty"`
	// ConflictCopy (additive, S3) is the saved copy of the conflicted files
	// made when this command left the operation stopped with conflicts (or
	// before a git.conflict_* command first changed them).
	ConflictCopy string `json:"conflict_copy,omitempty"`
	// Previous (additive, S3 review) is the copy of what a choose or
	// restore overwrote, when that differed from the saved state.
	Previous *GitConflictPrevious `json:"previous,omitempty"`
	// Evicted (additive) lists copies of this stop that recording a new
	// copy had to drop (only beyond 100 distinct copies of one path).
	Evicted []GitConflictCopyRef `json:"evicted,omitempty"`
}

// Git operation record states (GitOperationRecord.State).
const (
	// GitOperationRunning: a command of this operation is running.
	GitOperationRunning = "running"
	// GitOperationStoppedConflicts: stopped with unmerged paths.
	GitOperationStoppedConflicts = "stopped_conflicts"
	// GitOperationReady: stopped with no unmerged paths; Continue or Abort.
	GitOperationReady = "ready"
	// GitOperationCompleted, GitOperationAborted: ended by a command here.
	GitOperationCompleted = "completed"
	GitOperationAborted   = "aborted"
	// GitOperationEndedExternal: the repository no longer has this
	// operation in progress and no command here ended it (a terminal, or a
	// server stop while Git ran).
	GitOperationEndedExternal = "ended_external"
	// GitOperationInterrupted: the server stopped while a command of this
	// operation ran and the operation is still in progress; the next read
	// reconciles it.
	GitOperationInterrupted = "interrupted"

	// Reserved for agent-assisted resolution (not produced yet).
	GitOperationAgentRunning     = "agent_running"
	GitOperationAgentReview      = "agent_review"
	GitOperationAgentInterrupted = "agent_interrupted"

	// GitOperationEndedByJob: the operation ended while a resolution job
	// was attached (its agent continued, skipped or aborted it); the last
	// review is kept on the record.
	GitOperationEndedByJob = "ended_by_job"
)

// GitOperationRecord is one application-started merge or rebase, in
// Snapshot.GitOperations (the latest per repository toplevel, Checkout).
// It pins what was started: Kind, Branch, OrigHead (HEAD before) and Target
// (Oid, the name the user chose as Label, Subject). StartCommandID started
// it and LastCommandID is the latest git.* command applied to it; ThreadID
// or ProjectID is the start command's target. State is one of the
// GitOperation* states; Code and Message repeat the latest result. The
// server reconciles the record with the repository at startup and whenever
// GET /v1/git/operation reads it: an operation no longer in progress, or a
// different one, ends it as ended_external. JobThreadID is reserved for the
// agent resolution job that may work on the operation (not used yet).
// Times are RFC 3339.
type GitOperationRecord struct {
	OperationID    string             `json:"operation_id"`
	Checkout       string             `json:"checkout"`
	Kind           string             `json:"kind"`
	State          string             `json:"state"`
	Branch         string             `json:"branch,omitempty"`
	OrigHead       string             `json:"orig_head,omitempty"`
	Target         GitOperationCommit `json:"target"`
	StartCommandID string             `json:"start_command_id"`
	LastCommandID  string             `json:"last_command_id,omitempty"`
	ThreadID       string             `json:"thread_id,omitempty"`
	ProjectID      string             `json:"project_id,omitempty"`
	JobThreadID    string             `json:"job_thread_id,omitempty"`
	Code           string             `json:"code,omitempty"`
	Message        string             `json:"message,omitempty"`
	StartedAt      string             `json:"started_at"`
	UpdatedAt      string             `json:"updated_at,omitempty"`
	EndedAt        string             `json:"ended_at,omitempty"`
	// Backup (additive) is the latest backup made for this operation.
	Backup *GitOperationBackup `json:"backup,omitempty"`
	// Review (additive, S4) is the cached review of the attached (or, after
	// ended_by_job, the last) resolution job; ReviewKey identifies the
	// repository state it was computed for.
	Review    *GitResolveReview `json:"review,omitempty"`
	ReviewKey string            `json:"review_key,omitempty"`
	// JobBaseline and JobDecisions (additive, S4 gate) outlive the job:
	// the index as it was when the first resolution job of this stop
	// started, and, per path, the index entries a user decision (choose,
	// resolve, restore, or stage/unstage while the gate is active) left.
	// Continue is gated on them until the operation provably moves on
	// (GitOperationState.AgentChanges).
	JobBaseline  *GitJobBaseline   `json:"job_baseline,omitempty"`
	JobDecisions map[string]string `json:"job_decisions,omitempty"`
}

// GitJobBaseline is GitOperationRecord.JobBaseline: Listing is the SHA-256
// of `git ls-files --stage -z` at the job's start, kept in the application
// home (not the repository, so Git garbage collection cannot remove it);
// StopKey identifies the stop it was taken at. Incomplete when the index
// was too large or could not be recorded; a missing or altered listing, or
// a different stop, makes the gate fail closed (GitAgentChanges.Incomplete).
type GitJobBaseline struct {
	Listing    string `json:"listing,omitempty"`
	StopKey    string `json:"stop_key,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
	StartedAt  string `json:"started_at"`
}

// GitAgentChanges is GitOperationState.AgentChanges: every index entry that
// differs from the job baseline and that no user decision explains (the
// decision's recorded entries equal the current ones). Items show each
// path's current staged content against the baseline (IndexOid,
// IndexDiff). Continue is refused (review_pending) until the command
// carries Fingerprint as AcknowledgeAgentChanges; Incomplete means the
// comparison could not be made in full (Reason says why), which needs the
// same acknowledgement. Fingerprint then covers the whole current index
// listing, so an acknowledgement never extends to a later index. Skip is
// not gated: it resets the index and worktree to the stopped commit's
// parent state, so nothing staged is committed by it.
type GitAgentChanges struct {
	Items       []GitResolveItem `json:"items"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	Incomplete  bool             `json:"incomplete,omitempty"`
	Reason      string           `json:"reason,omitempty"`
}

// Manual conflict resolution (ADR 0023, S3). Three journaled commands act on
// one unmerged path of the merge, rebase, cherry-pick or revert in progress
// (started here or not). They hold the checkout lease like the other
// operation commands, run while the operation reserves the checkout (they
// are its own commands), are refused when nothing is in progress, and pin
// the path's index entries (ConflictPin, GitConflict.ConflictPin or
// GitConflictFile.ConflictPin) and its working-tree token (WorktreeToken,
// GitConflict.WorktreeStat or GitConflictFile.WorktreeToken). Each first
// saves and pauses open documents and reconciles them afterwards; an unsaved
// edit that saving writes makes the pinned token stale (nothing changes).
//
// Saved copies. When an operation stops with conflicts (after git.merge,
// git.rebase, continue or skip here, or before a git.conflict_* command
// first changes anything in an operation started elsewhere), the server
// saves every unmerged path as it is: stages 1-3 (mode and object) and the
// working-tree content (mode, symlink target, or absence). The copy is an
// unreferenced commit in the repository's object database (like
// GitOperationBackup; the same pruning caveats apply) named by
// GitOperationState.ConflictCopy and recorded in Snapshot.GitConflictCopies
// for that stop of that operation.
//
//   - git.conflict_choose: Side ours, theirs or base. Writes that side's
//     content to the working tree only (`git checkout-index --stage=N -f`,
//     so filters apply as in the CLI); when that side is absent (deleted),
//     removes the working-tree file. The path stays unmerged.
//   - git.conflict_resolve: As content stages the working-tree file (`git
//     add -- <path>`); As deleted resolves it as a deletion (`git rm
//     --cached -- <path>`, the file stays as untracked). No other path is
//     touched. When the file still has conflict-marker lines (or is too
//     large to check, over 8 MiB), AcknowledgeMarkers must equal the
//     WorktreeToken the user reviewed; a binary file, whose markers cannot
//     be checked, needs AcknowledgeBinary likewise. When no unmerged path
//     remains the operation becomes ready.
//   - git.conflict_restore: CopyID (any copy of the current stop holding
//     the path, GitConflictFile.Copies). Restores the saved working-tree
//     content atomically, with its permission bits, and the path's saved
//     index entries (the original copy's stages make it unmerged again),
//     whatever was resolved.
//
// Nothing a choose or restore overwrites is lost: immediately before it
// replaces or removes the working-tree file, a file that differs from the
// saved state is copied (a before_overwrite copy, GitOperationResult.Previous)
// and can be restored like any other copy. If the path's original copy was
// made in an earlier attempt of the stop or did not hold it, a
// before_first_change copy of that path is made first. A file the server
// cannot copy (larger than 64 MiB, or not a regular file or symlink) is
// overwritten only with AcknowledgeUnsaved (unsaved_unacknowledged). Copies
// are recorded durably before anything changes.
//
// Submodule (gitlink) conflicts and paths whose working tree is a
// directory are refused (not_supported). Refusal codes: invalid,
// no_operation, not_conflicted (choose or resolve of a path that is not
// unmerged), no_saved_copy (restore without a saved copy of the current
// stop), stale_entry (ConflictPin or WorktreeToken no longer match; also in
// the result when saving open documents changed the file),
// stale_operation, markers_unacknowledged, binary_unacknowledged,
// unsaved_unacknowledged, not_supported, unavailable.
// Choosing a side that deleted the file removes the working-tree file. A
// resolve whose file changed while Git staged it succeeds with warning
// staged_newer_content.
type GitConflictWrite struct {
	Path               string `json:"path"`
	Side               string `json:"side,omitempty"`
	As                 string `json:"as,omitempty"`
	ConflictPin        string `json:"conflict_pin"`
	WorktreeToken      string `json:"worktree_token"`
	AcknowledgeMarkers string `json:"acknowledge_markers,omitempty"`
	CopyID             string `json:"copy_id,omitempty"`
	// Additive (S3 review). AcknowledgeBinary (resolve as content of a file
	// with a NUL in its first 8 KiB, whose markers cannot be checked) and
	// AcknowledgeUnsaved (choose or restore that must overwrite a file the
	// server cannot copy first: larger than 64 MiB or not a regular file or
	// symlink) are the WorktreeToken the user reviewed.
	AcknowledgeBinary  string `json:"acknowledge_binary,omitempty"`
	AcknowledgeUnsaved string `json:"acknowledge_unsaved,omitempty"`
}

// Saved-copy reasons (GitConflictCopy.Reason).
const (
	GitCopyAtStop            = "at_stop"
	GitCopyBeforeFirstChange = "before_first_change"
	GitCopyBeforeOverwrite   = "before_overwrite"
	// GitCopyBeforeJob is every path of a resolution job, taken right
	// before its agent starts (S4).
	GitCopyBeforeJob = "before_job"
)

// GitConflictCopyRef names one saved copy of a path.
type GitConflictCopyRef struct {
	CopyID    string `json:"copy_id"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

// GitConflictPrevious is GitOperationResult.Previous: the copy of the
// working-tree content a choose or restore replaced (CopyID, restorable with
// git.conflict_restore; Oid is the saved blob).
type GitConflictPrevious struct {
	CopyID string `json:"copy_id"`
	Oid    string `json:"oid,omitempty"`
}

// Conflict sides and resolutions.
const (
	GitConflictSideOurs   = "ours"
	GitConflictSideTheirs = "theirs"
	GitConflictSideBase   = "base"

	GitConflictAsContent = "content"
	GitConflictAsDeleted = "deleted"
	// GitConflictAsKeepStaged (S4 review) accepts a path a resolution
	// job's agent already staged, exactly as reviewed (ConflictPin pins the
	// index entry); nothing is run.
	GitConflictAsKeepStaged = "keep_staged"
)

// Versions for GET /v1/git/conflict.
const (
	GitConflictVersionBase    = "base"
	GitConflictVersionOurs    = "ours"
	GitConflictVersionTheirs  = "theirs"
	GitConflictVersionWorking = "working"
	GitConflictVersionSaved   = "saved"
)

// GitConflictFile is GET /v1/git/conflict?path=&version=base|ours|theirs|
// working|saved (plus the target): one version of a path that is unmerged
// now or has a saved copy for the operation's current stop. base, ours and
// theirs are the index stages while the path is unmerged, else those of the
// saved copy; working is the file now (Oid: the blob ID of its content,
// computed without writing, for files up to 8 MiB); saved is the
// working-tree content
// the saved copy holds. Present is false for an absent side or file.
// Content is at most 1 MiB (Truncated beyond); Binary means a NUL in the
// first 8 KiB; Symlink content is the link target. ConflictPin and
// WorktreeToken are the path's current pins for git.conflict_* commands;
// HasMarkers (working) reports conflict-marker lines, MarkersUnknown that
// the file was too large to check. CopyID is the saved copy of the current
// stop, when there is one.
type GitConflictFile struct {
	Path           string `json:"path"`
	Version        string `json:"version"`
	Present        bool   `json:"present"`
	Mode           string `json:"mode,omitempty"`
	Oid            string `json:"oid,omitempty"`
	Size           int64  `json:"size"`
	Content        []byte `json:"content,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	Binary         bool   `json:"binary,omitempty"`
	Symlink        bool   `json:"symlink,omitempty"`
	Unmerged       bool   `json:"unmerged,omitempty"`
	ConflictPin    string `json:"conflict_pin,omitempty"`
	WorktreeToken  string `json:"worktree_token,omitempty"`
	HasMarkers     bool   `json:"has_markers,omitempty"`
	MarkersUnknown bool   `json:"markers_unknown,omitempty"`
	CopyID         string `json:"copy_id,omitempty"`
	// Additive (S3 review). Kind is the node: file, symlink, absent,
	// directory or other (Present is false for the last three). CopyReason
	// and CopyCreatedAt describe CopyID, the copy this path was first saved
	// in; Copies lists every copy of this stop that holds the path (the
	// original, then each before_overwrite copy), any of which
	// git.conflict_restore accepts; version saved with copy_id reads a
	// specific one.
	Kind          string               `json:"kind,omitempty"`
	CopyReason    string               `json:"copy_reason,omitempty"`
	CopyCreatedAt string               `json:"copy_created_at,omitempty"`
	Copies        []GitConflictCopyRef `json:"copies,omitempty"`
}

// GitConflictCopy records one saved copy in Snapshot.GitConflictCopies:
// StopKey identifies the stop of the
// operation it was made for, CopyID the commit; Missing lists paths whose
// working-tree content could not be saved (a directory, special file, or
// beyond 64 MiB).
//
// Additive (S3 review): Reason is at_stop (all conflicts, when the stop was
// first seen), before_first_change (one path the original copy did not
// hold) or before_overwrite (one path's content immediately before a choose
// or restore replaced it); Path names the path of a one-path copy and
// ContentOid the working-tree content it holds. before_overwrite copies are
// deduplicated by content per path and at most 100 are kept per path; a
// repository keeps 300 copies and all repositories 2000, evicting copies of
// other stops first (never the stop being written).
type GitConflictCopy struct {
	Reason      string   `json:"reason,omitempty"`
	Path        string   `json:"path,omitempty"`
	ContentOid  string   `json:"content_oid,omitempty"`
	Checkout    string   `json:"checkout"`
	CheckoutKey string   `json:"checkout_key"`
	StopKey     string   `json:"stop_key"`
	CopyID      string   `json:"copy_id"`
	CreatedAt   string   `json:"created_at"`
	Missing     []string `json:"missing,omitempty"`
}

// Agent conflict resolution (ADR 0023, S4; Q10). A resolution job is an
// agent turn in a job-kind thread (Thread.Job) that may edit the listed
// conflicted files and run checks, then stops for review: the server never
// stages, continues, skips or aborts for it. The job thread is exempt from
// the operation's checkout reservation; every other thread still waits.
//
//   - git.resolve_job_start (a journaled Git write, like the others):
//     OperationID (the recorded operation; empty adopts an operation started
//     elsewhere into a new record), AgentID and Settings (as thread.start),
//     Paths (unmerged paths; empty means all) and optional Instructions
//     (at most 4 KiB, appended to the prompt). Before the agent starts, every
//     path has a saved copy (the review's "before") and the server records
//     HEAD, the stop, the other unmerged paths and the status outside the
//     job's paths. The record becomes agent_running with JobThreadID.
//     Refused (job_exists) while a job is attached; end it first.
//   - git.resolve_job_followup (an ordinary command): OperationID and
//     Prompt; another turn in the same job thread (also after an interrupted
//     job or a server restart), record back to agent_running.
//   - git.resolve_job_cancel: OperationID; interrupts the running turn (as
//     thread.interrupt). The record becomes agent_interrupted.
//   - git.resolve_job_end: OperationID; closes the job thread and detaches
//     it; the record returns to stopped_conflicts or ready.
//
// When the job's turn ends the record becomes agent_review (agent_interrupted
// when it was cancelled, failed or the server restarted; jobs never restart
// by themselves). GitOperationState.Review compares every job path with its
// saved copy and flags scope violations. Accepting a path is
// git.conflict_resolve pinned to the reviewed ConflictPin and WorktreeToken;
// rejecting it is git.conflict_restore of the item's CopyID. While the job
// runs, git.conflict_* and git.operation_continue, _skip and _abort are
// refused (job_running: stop the agent first); in review they are allowed,
// and a successful continue, skip or abort ends the job.
//
// Review round (2026-09-25): the job start takes a before_job copy of every
// job path (working file and index, after open documents were saved); the
// review compares with it and reject restores it, so the user's own edits
// from before the job are never attributed to the agent or lost. A staged
// item shows its index content (IndexOid, IndexDiff) and is accepted with
// As keep_staged, pinned to the reviewed index entry. Continue is gated on
// content (GitAgentChanges): the first job of a stop records the whole
// index (GitOperationRecord.JobBaseline), each user choose, resolve,
// restore, stage or unstage records the entries it left (JobDecisions),
// and every index entry that differs from the baseline without a matching
// decision must be acknowledged by fingerprint (AcknowledgeAgentChanges)
// or the command is refused (review_pending). The gate outlives End and
// the job thread's deletion until the operation provably moves on (a
// completed or aborted operation, or a different stop); skip resets the
// index and is not gated. git.resolve_job_end removes the
// job thread; follow-up and end are refused while a cancelled turn is
// still stopping. A review is never cached while a turn is active, and a
// turn that ends (also after a cancel) is reviewed again. The job thread refuses ordinary commands (job_thread:
// prompt.send, prompt.reopen-send, thread.resume, thread.reopen, queue.*)
// and deletion while it works; deleting it otherwise detaches the job. If
// the operation ends while a job is attached (the agent continued, skipped
// or aborted it), the record becomes ended_by_job, keeps the last review
// and the job's turn is stopped. Codes: review_pending, job_thread.
type GitResolveJob struct {
	OperationID  string    `json:"operation_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	Settings     *Settings `json:"settings,omitempty"`
	Paths        []string  `json:"paths,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	Prompt       string    `json:"prompt,omitempty"`
}

// Review states (GitResolveReview.State).
const (
	GitReviewRunning     = "running"
	GitReviewReady       = "ready"
	GitReviewInterrupted = "interrupted"
)

// GitResolveReview is GitOperationState.Review while a job is attached.
// Violations name what the agent did outside its scope: HEAD moved, the
// operation's stop changed, other unmerged paths were resolved, files
// outside the job's paths changed, or job paths were staged (the agent ran
// git add or rm). They are reported, never repaired.
//
// Additive (S4 review): the review is computed when the job's turn ends and
// on an explicit refresh (GET /v1/git/operation?review=refresh), and cached
// on the record; Stale reports that the repository changed since
// (refresh to see it). TurnID is the job turn it reviews, StopReason how
// that turn ended (end_turn, max_tokens, refusal, cancelled...).
// Fingerprint digests the items and violations.
type GitResolveReview struct {
	JobThreadID string           `json:"job_thread_id"`
	State       string           `json:"state"`
	Items       []GitResolveItem `json:"items"`
	Violations  []string         `json:"violations,omitempty"`
	Incomplete  bool             `json:"incomplete,omitempty"`
	TurnID      string           `json:"turn_id,omitempty"`
	StopReason  string           `json:"stop_reason,omitempty"`
	Stale       bool             `json:"stale,omitempty"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	ComputedAt  string           `json:"computed_at,omitempty"`
}

// GitResolveItem reviews one job path: Changed (the working file differs
// from the saved copy), Staged (no longer unmerged: the agent staged or
// removed it), Deleted (the file is gone), HasMarkers (conflict-marker
// lines remain), Binary, and Diff, a bounded unified diff from the saved
// working file to the current one (DiffTruncated when cut). ConflictPin
// and WorktreeToken pin accept (git.conflict_resolve); CopyID is the saved
// copy reject restores (git.conflict_restore).
type GitResolveItem struct {
	Path          string `json:"path"`
	Changed       bool   `json:"changed,omitempty"`
	Staged        bool   `json:"staged,omitempty"`
	Deleted       bool   `json:"deleted,omitempty"`
	HasMarkers    bool   `json:"has_markers,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Diff          string `json:"diff,omitempty"`
	DiffTruncated bool   `json:"diff_truncated,omitempty"`
	ConflictPin   string `json:"conflict_pin,omitempty"`
	WorktreeToken string `json:"worktree_token,omitempty"`
	CopyID        string `json:"copy_id,omitempty"`
	// Additive (S4 review). Unknown: the item could not be compared (an
	// error or the review's time budget); nothing about it is claimed.
	// IndexOid and IndexDiff show a staged entry's content against the
	// pre-job file (accept it with As keep_staged). Decision is accepted or
	// rejected once the user decided.
	Unknown   bool   `json:"unknown,omitempty"`
	IndexOid  string `json:"index_oid,omitempty"`
	IndexDiff string `json:"index_diff,omitempty"`
	Decision  string `json:"decision,omitempty"`
}
