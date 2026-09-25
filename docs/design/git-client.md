# Git client and conflict resolution

Status: expanded scope accepted in Q6; corrected Q10 requires the selected agent to stop for review after resolving files, Q13 queues writers per checkout, and Q26 adds recorded turn comparisons. Exact operation inventory and detailed recovery policies remain under design. [Git operation research](../research/git-operations.md) supplies the underlying command semantics.

## Go prototype status — 2026-09-24

The Go reference has a read-only Git surface ([details](go-slice.md#read-only-git-surface--2026-09-24)):
server reads of status (grouped conflicted/staged/unstaged/untracked entries,
upstream ahead/behind from local refs, in-progress operation), bounded
per-entry diffs, the 50 most recent commits and bounded commit patches,
presented in the right host and the read-only viewer with refresh on
visibility, target change, turn end and explicit Refresh. Reads perform no Git
mutation and no network access. It also has topological history with
commit-graph lanes (HEAD plus upstream, or all branches), a branch list with
upstream ahead/behind, and read-only branch-versus-HEAD and
staged-versus-HEAD/unstaged whole diffs ([details](go-slice.md#git-history-branches-and-comparisons-read-only--2026-09-24)).

The server, protocol and Go client also implement whole-file stage, unstage
and discard, and commit including amend ([ADR 0020](../adr/0020-git-write-actions.md);
wire contract in `apps/go/internal/protocol/git_write.go`). They are durable
two-phase commands: refusals such as stale pins or a busy checkout record
nothing; otherwise a running receipt is journaled before Git starts and
becomes `outcome_unknown` if the server stops before the outcome is recorded.
Writes run with the user's full Git configuration (hooks, local filters,
signing) but never an editor, prompt, pager, lazy fetch or automatic
maintenance, and without a controlling terminal. They are refused while any
thread holds an overlapping checkout lease, and hold the lease themselves while
running. Stage and discard act only on regular files, symlinks or truly absent
paths (never a directory or a path below a file or symlink), and a commit is
refused when status could not list the whole staged set. The TUI does not expose
them yet; verified with real Git in `git_write_test.go`, not in a terminal.

Remaining: TUI actions for these writes (including discard confirmation and
the published-amend warning), graph actions, hunk/line staging,
multi-select, conflicted-path staging, submodule changes, branch switching,
soft reset, pull/fetch/push, rebase and conflict resolution (manual and
agent-assisted), context menus, turn comparisons, live-buffer coordination,
and real-terminal/PTY validation of the surface.

### Ref and remote actions — 2026-09-24

The server, protocol and Go client (not yet the TUI) also implement branch
creation, switching (including create-and-switch), soft reset with undo,
fetch, fast-forward-only pull, plain push to the upstream and cancel
([ADR 0021](../adr/0021-git-ref-and-remote-actions.md); wire contract in
`apps/go/internal/protocol/git_write.go`, client helpers in
`apps/go/internal/client/git_remote.go`). They use the same pinned,
journaled commands. Switch carries local changes only with an explicit
acknowledgement of the shown count and stops, naming the files, when Git
would overwrite one, including ignored files. Pull fetches, then
fast-forwards or reports up to date, ahead or diverged, with fetch and
integration results kept separate. Push never forces and is refused while
behind or when Git's `push.default` would not push to the upstream.
Credentials are non-interactive only, failures are classified, and
fetch, push and a pull's fetch can be cancelled. Fetch, push and branch
creation run beside agent turns; switch, reset and pull hold the checkout
lease. Switch and pull finish and pause open shared documents through the
document coordinator before Git rewrites files, and reconcile them after.
Verified with local bare repositories, a loopback HTTP server and fake SSH
commands in `git_ref_test.go` and `git_remote_test.go`, not against real
hosting services or in a terminal.

TUI implemented 2026-09-24: heading Fetch/Pull/Push controls with progress and
Cancel, branch-row Switch and context menus, commit-row Create branch and Soft
reset with Undo, and the carry, leave-commits and published confirmations
([Go slice](go-slice.md#git-ref-and-remote-actions-tui--2026-09-24)); fake-API
tests and render captures only, not yet a live server in a real terminal.

## Accepted scope

Provide status, attractive diffs, file/hunk/line staging, commits, history, branch switching, a commit graph, and right-click context menus. Include soft reset to a chosen commit, fast-forward-only pull by default, and rebase. Conflicts can be resolved manually or with a selected connected agent; when multiple agents are available, the user can choose one.

The surface must work with mouse and keyboard and expand for detailed review. Context menus are an interaction path, not the only way to discover or invoke operations. The reusable shell supplies menu/focus mechanics; Git supplies operation-specific actions and state.

## Coordinating Git with live buffers

For application-managed Git operations that rewrite workspace files, finish pending saves, pause disk autosave while the operation runs, then reconcile open buffers before autosave resumes. This applies to branch switches and file-changing pull/rebase steps, including continuation or abort where files change. The server coordinates the affected checkout with its writer queue and document-save pipeline.

Before dispatch, establish a consistent saved document/disk baseline and ensure earlier saves cannot complete after Git begins. A save failure or unresolved editor conflict preserves the edits and stops the operation for review. Do not silently stash, discard, stage or commit work to make it proceed. Preserve unrelated staged content.

While disk autosave is paused, any newly received collaborative edits remain recoverable in shared document state. After Git returns, reconcile those buffers with the actual resulting files using the baseline and the existing external-change rules. Clean reconciliation establishes the new baseline before resuming autosave. Overlap, deletion, replacement or reconciliation failure preserves versions and pauses the affected document for review; stale queued saves must not overwrite Git's result.

Run reconciliation after success, failure and cancellation; an unsuccessful operation does not establish unchanged files. Git conflicts also retain their explicit operation/review state and continue through the manual or agent-assisted resolver. Writes from that resolution workflow must be coordinated with the paused document state. The selected agent still stops before staging or continuation under Q10. Exact transaction boundaries and failure recovery need implementation tests; external shells and editors remain outside this coordinator.

## Commit graph and action targets

The selected commit, checked-out branch, and `HEAD` are separate state. Right-clicking a commit opens actions for that object. Opening or navigating a menu does not execute an action or silently switch branches.

Soft reset must identify both the target commit and the current branch/HEAD it will move. Present detached/unborn states accurately. The operation preserves index and working-tree content but changes the staged comparison against `HEAD`; the UI must refresh that comparison and must not silently restage files or substitute another reset mode.

Capture the selected object ID for the action and revalidate repository state before execution. A graph refresh or external branch movement must not silently redirect a prepared action. The complete menu beyond the explicitly requested operations—such as cherry-pick, revert, tags, mixed/hard reset, or force push—remains unselected.

## Pull and integration

The ordinary Pull action explicitly enforces fast-forward-only integration regardless of a conflicting inherited Git preference. It identifies the remote and upstream and distinguishes fetch results from integration results. If history diverges, fetching may succeed while integration stops; do not report that the whole operation left all state unchanged.

A failed fast-forward-only pull must not silently fall back to merge or rebase. Offer an explicit next action to integrate the selected upstream by merge or rebase, or leave the branch as it is. Manual or agent conflict resolution applies when that separately chosen operation encounters conflicts.

Missing upstream, authentication failure, transport failure, cancellation, and divergence need distinct states. Additional network features, background fetching, ordinary Push, and force-push behavior remain to be scoped. These product features do not authorize contributor remote operations in this repository.

## Rebase and manual conflicts

Show the operation type, source and target, current replayed commit when applicable, affected files, and progress. Keep Continue, Skip, and Abort distinct. Continue requires resolving and staging the appropriate conflicts; Skip omits a patch and must communicate that consequence. Do not imply that absence of conflict markers proves correctness.

The manual resolver needs base/side/result inspection, editable output, saving, and explicit resolution state. Label sides by their real branch/commit roles: during rebase, “ours” and “theirs” do not mean what users may expect from an ordinary merge. Layout details remain open, including which views remain visible at narrow widths.

Read conflict state from the repository's unmerged entries. Support or explicitly route missing sides, deletions, renames, and binary conflicts; text-marker scanning alone is insufficient. Repeated conflicts during rebase must return the same coherent workflow.

## Agent-assisted conflicts

The entry point offers a choice of connected agents. Show agent readiness and unavailable capabilities before dispatch. Give the selected agent the active operation, relevant commits/sides/base, conflicted paths, and the permitted scope of its job. Reuse the ADE's ACP boundary rather than putting provider-specific calls in the Git surface.

Show progress, resulting edits, checks, and a way to cancel or return to manual work. Keep unrelated staged changes and unsaved buffers intact. Detect repository or file changes that make a prepared resolution stale. An agent's completion message does not establish that all index conflicts are resolved or that the operation has finished.

**Q10 was corrected to stop for review:** selecting “Resolve with agent” authorizes the chosen agent to edit conflicted files and run relevant checks. It stops with reviewable results before staging resolutions or continuing the merge/rebase; the user then chooses whether to accept, revise manually, or ask for more work. Subsequent conflicts return to the same review workflow. The earlier automatic-continuation answer is superseded, not an additional accepted mode. If the agent cannot finish, preserve recoverable state and offer manual resolution. Do not silently skip patches, discard unrelated changes, or perform unrelated Git operations.

**Q9 accepted the existing checkout by default**, with worktrees available explicitly. The resolution job acts on the checkout/worktree containing the active Git operation, rather than silently moving the operation elsewhere. The checkout writer queue reserves that operation and its review for related work; competing threads wait. See [workspace behavior](workspaces.md). The job may use another available agent without switching the original thread's agent.

## Turn comparisons

Record a start/end comparison for each turn and label it **changes observed during this turn**. Capture the starting observation when the turn actually begins execution after queueing, and the end observation when work settles; preserve interrupted or incomplete coverage explicitly. These are proposed capture boundaries implementing the accepted start/end comparison and must be verified against the server lifecycle.

The view is historical and must not drift with later workspace edits. Show provider-reported edits where available with their source, separately from current working-tree, staged, and branch comparisons. External editors, collaborating clients, terminal commands, and provider subprocesses can contribute to observed changes; the comparison does not prove agent authorship. Record coverage limits, unavailable/binary content, and capture failures. Automatic rollback is deferred; displaying a comparison never resets files or creates a commit.

## Verification targets

- Graph actions affect the displayed target/current branch; stale state invalidates prepared actions.
- Soft reset preserves existing index/worktree content and updates the displayed staged comparison.
- A divergent ordinary Pull never silently merges or rebases.
- Manual conflicts and repeated rebase conflicts expose correct side identities and operation controls.
- Partial staging and resolution staging preserve unrelated staged work.
- Agent choice reaches the selected connection; failure/cancellation leaves reviewable, recoverable work.
- The selected agent edits and stops for review; staging and continuation wait for the user's subsequent action. Failure and missing capabilities remain explicit rather than silently skipping or discarding work.
- Context menus, keyboard equivalents, scroll/selection, and expanded views behave consistently across the agreed terminal matrix.

These are design acceptance targets, not passed tests. Dirty-work accommodation, explicit stashing, rollback/recovery, and the full command inventory will be finalized through the [interview](interview.md).
