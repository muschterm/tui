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
the published-amend warning), graph actions, multi-select, conflicted-path staging, submodule changes, branch switching,
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

### Merge and rebase — 2026-09-25

The server, protocol and Go client (not yet the TUI) read the operation in
progress and start, continue, skip and abort merges and rebases
([ADR 0023](../adr/0023-merge-rebase-operations.md); wire contract in
`apps/go/internal/protocol/git_write.go` and `git.go`, client helpers in
`apps/go/internal/client/git_operation.go`). `GET /v1/git/operation` reports
the kind, branch, target, rebase step i/N, the commit Git is stopped at and
every unmerged path with its stages from the index, labelled by role (during
a rebase, ours is the upstream plus the commits rebased so far). Operations
started in a terminal are shown and managed too. `GET
/v1/git/integrate/preview` reports fast-forward, replay count, merges in the
range and published commits before a start.

User decisions (2026-09-25): a merge or rebase needs a clean tracked tree and
never stashes, even with `merge.autoStash` or `rebase.autoStash`; rebase is
non-interactive only, never moves other branches (`--no-update-refs`), never
guesses a fork point and refuses ranges containing merge commits; Skip is
rebase only and names the dropped commit; agent resolution will run as a
job-kind thread (reserved `JobThreadID`). Untracked and ignored files in the
way are refused before anything runs. Rerere may replay a recorded
resolution into a file but never stages it.

While a merge, rebase, cherry-pick or revert is in progress in a checkout,
wherever it was started, agent turns there wait
(`WriterWait.HolderOperation`) until it ends; bisect does not reserve.
Abort, Continue and Skip pin what was reviewed, need confirmation, save and
pause open documents first and reconcile them afterwards; Continue may stop
again at the next conflict. Abort and Skip name every file outside the
conflicts whose changes they would reset and need that list acknowledged;
Continue pins the staged set and asks before committing staged conflict
markers. Interactive rebase stops (edit, exec, break) and entries marked
assume-unchanged or skip-worktree are left to the terminal, and `merge.ff`
applies as in the CLI (review 2026-09-25). Abort and Skip first back up
every file they overwrite into unreferenced Git objects that can be
restored with `git checkout <backup> -- <path>`, list untracked and
ignored files in their way, and never recurse into submodules; staged
conflict markers are read from the staged content itself (second review). Manual resolution (S3), agent resolution (S4)
and the TUI remain. Verified with real Git in temporary repositories in
`git_operation_test.go`, not in a terminal.

### Interactive rebase — 2026-09-26

The server, protocol and Go client (not yet the TUI) run interactive
rebases from a plan ([ADR 0026](../adr/0026-interactive-rebase.md); wire
contract in `apps/go/internal/protocol/git_rebase.go`, client helpers in
`apps/go/internal/client/git_rebase.go`, capability
`git-rebase-interactive`, Git 2.38 or newer). `GET /v1/git/rebase/plan`
reads the commits from a base (the upstream, a commit's parent, or the
root) to HEAD with their messages, authors, published and merge flags, the
branches `--update-refs` would move and a fingerprint; `git.rebase` with a
plan validates it with the shared `protocol.ValidateRebasePlan` (every
commit exactly once, squash and fixup after a commit, the messages each
step needs) against a fresh read. The server drives `git rebase -i` with
its own editor helpers, so the pinned plan and stored messages are what
Git runs. Stops use the ADR 0023 operation commands, now also at edit,
break and message stops of an application plan, with
`git.operation_commit` to split or insert commits while stopped; stop,
signing-failure, hook-rejection and helper-failure states are reported in
`GitOperationState.Interactive` and survive a server restart. Commits made
at stops are recorded; Abort lists them for acknowledgement and first keeps
the rebase's HEAD at `refs/tui-go/rebase-backup/<operation>`. Messages are
committed verbatim, so plan and original messages keep `#` lines, trailing
spaces and blank lines exactly. Verified with
real Git in temporary repositories in `git_rebase_test.go`, not in a
terminal.

### Partial staging — 2026-09-26

The server, protocol, Go client and Go TUI (the selectable diff viewer;
[go-slice binding](go-slice.md#git-partial-staging-tui--2026-09-26)) stage and unstage
selected hunks and lines of one file ([ADR 0025](../adr/0025-partial-staging.md);
wire contract in `apps/go/internal/protocol/git_partial.go`, client helpers
`Client.GitHunks` and `client.GitPartialCommand`, capability
`git-partial-stage`). `GET /v1/git/hunks` serves an addressable diff with
fixed diff options and a fingerprint; `git.stage`/`git.unstage` with a
`Partial` selection compute the new index blob from the pinned pre-image
and install it with `update-index`, never touching the worktree and
leaving every other staged change, in the same file or elsewhere, as it
was. Stale selections are refused (`stale_diff`) and never partly applied.
Paths reported unsupported keep only whole-file actions: conflicted,
submodule, symlink, binary, filter attribute, working-tree encoding,
deleted, type change, worktree rename of an intent-to-add file, not a
regular file, skip-worktree/assume-unchanged, no content (mode-only),
too large and unparsable diffs (`GitHunks.Unsupported` codes `conflicted`,
`submodule`, `symlink`, `binary`, `filter`, `encoding`, `deleted`,
`type_change`, `rename`, `not_regular`, `skip_worktree`, `no_content`,
`too_large`, `unparsable`); partial discard stays deferred (user decision
2026-09-26). Verified with real Git in temporary repositories in
`git_partial_test.go`, with the line-ordering rule checked against
`git apply` over random diffs in `git_partial_select_test.go`, not in a
terminal.

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

A failed fast-forward-only pull must not silently fall back to merge or rebase. Offer an explicit next action to integrate the selected upstream by rebase (manual interactive or agent-planned, see below), or leave the branch as it is. Manual or agent conflict resolution applies when that separately chosen operation encounters conflicts.

**User decisions — 2026-09-26 (history editing and fetch).** These supersede the earlier "non-interactive rebase only" decision (ADR 0023) and the merge option in the divergence follow-up above. Implementation status is noted per item and in the status sections above (interactive rebase: server, protocol and client only, 2026-09-26).

- **Pull integrates by fast forward only.** When Pull finds the branch diverged, the follow-up offers rebase onto the upstream or leaving the branch as it is; Pull never leads to a merge commit. The separate Merge action for other branches is unchanged.
- **Soft reset from a commit's context menu** is the user's manual squashing workflow (already built, ADR 0021): right-click a commit → Soft reset the current branch to it, keeping index and working tree.
- **Interactive rebase, two ways.** The user can edit the rebase plan manually or hand it to an agent. Manual scope: parity with mainstream Git clients, with Sublime Merge as the named reference (reorder, reword, edit/amend, squash, fixup, drop and the commit-menu shortcuts such clients offer); the exact list is to be confirmed against Sublime Merge's documentation before implementation. Agent mode: an agent job reads the commits and the user's instruction and proposes a plan (and reworded messages); the user reviews and may edit it in the same editor, and only then does the server run it. Conflicts use the existing manual or agent resolution, which stops for review before Continue.
- **Interactive rebase scope (confirmed 2026-09-26 after the [research](../research/go-interactive-rebase-2026-09-27.md)).** Parity target is everything Sublime Merge does for history editing, as the todo subset `pick`, `reword`, `edit`, `squash`, `fixup`, `fixup -C`, `fixup -c`, explicit `drop`, `break` and reordering. Sublime Merge's one-shot commit-menu items (edit message, edit contents, squash with parent or selection, fixup, drop, move up/down, rebase onto) are presets that open the same plan; there is no second server path. `exec` lines are not supported. Merge commits inside the rewritten range are flattened: the plan read flags them, starting needs an explicit acknowledgement, and the confirmation states that merges are dropped and their side commits linearised (never `--rebase-merges`). `--update-refs` is a per-plan toggle, off by default; the plan read lists the local branches that would move. Cherry-pick and revert are deferred. A clean tracked tree is still required and nothing is stashed; published commits still need an acknowledgement; the application never force-pushes.
- **Interactive rebase defaults (orchestrator, reversible; [ADR 0026](../adr/0026-interactive-rebase.md)).** `commit.gpgSign` is honoured, with a distinct signing-failed state; repository hooks run, and a refusing hook is reported as its own state; rerere is disabled during application rebases; Edit offers both a plain amend stop and Sublime Merge's "edit contents", which soft-resets the commit into the index at the stop so it can be re-committed or split (the default); rebase, sequence, editor, autosquash and autostash configuration is neutralised on the command line, while identity, signing, hooks and merge drivers follow the user's configuration.
- **Fetch and Fetch & prune** are two actions that fetch all remotes; Fetch & prune reports which remote-tracking refs it removed afterwards, without a confirmation (it deletes only remote-tracking refs). Implemented 2026-09-26 in the Go slice: plain Fetch never prunes, even when `fetch.prune` is configured, and neither action prunes tags; `remote.<name>.skipFetchAll` is honoured as by `git fetch --all`. Deleting only remote-tracking refs is enforced by checking each remote's configuration immediately before its fetch: a remote whose fetch refspecs write outside its own `refs/remotes/<name>/`, or whose namespace overlaps another remote's (compared case-insensitively), is refused and reported rather than fetched. A configuration change between that check and Git's own read remains a narrow race; any non-remote-tracking ref removed is still reported. Each remote is fetched and reported separately, so one failing remote does not hide the others' results or removed refs.

Missing upstream, authentication failure, transport failure, cancellation, and divergence need distinct states. Additional network features, background fetching, ordinary Push, and force-push behavior remain to be scoped. These product features do not authorize contributor remote operations in this repository.

## Rebase and manual conflicts

Rebase is interactive with a manual or agent-proposed plan (user decision 2026-09-26, under Pull and integration; server contract in [ADR 0026](../adr/0026-interactive-rebase.md)). Show the operation type, source and target, current replayed commit when applicable, affected files, and progress. Keep Continue, Skip, and Abort distinct. Continue requires resolving and staging the appropriate conflicts; Skip omits a patch and must communicate that consequence. Do not imply that absence of conflict markers proves correctness.

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
