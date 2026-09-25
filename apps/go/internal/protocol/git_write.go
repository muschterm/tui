package protocol

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
// write is refused with checkout_busy. While a Git write runs it holds the
// lease itself, so no agent turn starts in that repository; queued turns wait
// and their WriterWait names the Git command.

// Git write command kinds.
const (
	GitKindStage   = "git.stage"
	GitKindUnstage = "git.unstage"
	GitKindDiscard = "git.discard"
	GitKindCommit  = "git.commit"
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
type GitWrite struct {
	Paths                []GitPathPin `json:"paths,omitempty"`
	Message              string       `json:"message,omitempty"`
	Amend                bool         `json:"amend,omitempty"`
	ExpectedHead         string       `json:"expected_head,omitempty"`
	StagedFingerprint    string       `json:"staged_fingerprint,omitempty"`
	AcknowledgePublished bool         `json:"acknowledge_published,omitempty"`
	Confirmed            bool         `json:"confirmed,omitempty"`
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
}
