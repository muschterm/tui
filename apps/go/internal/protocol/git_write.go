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
	Integration     *GitIntegration `json:"integration,omitempty"`
	Push            *GitPushResult  `json:"push,omitempty"`
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
}

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
