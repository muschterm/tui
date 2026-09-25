---
status: accepted (user decisions 2026-09-25)
---

# Merge and rebase operations

[ADR 0020](0020-git-write-actions.md) and [ADR 0021](0021-git-ref-and-remote-actions.md)
added pinned, journaled Git writes that finish in one step. A merge or
rebase can stop with conflicts and stay in progress across commands, server
restarts and terminals, and while it is in progress no agent turn may change
that checkout (docs/design/workspaces.md). The corrected Q10 contract also
requires later agent resolution to stop for review before anything is
staged or continued. This records the server, protocol and client slices
S1 (operation state, reservation, Abort/Continue/Skip) and S2 (starting a
merge or rebase). Manual resolution (S3), agent jobs (S4) and the TUI come
later.

## Decision

- **The repository is the truth.** `GET /v1/git/operation` reads the
  operation in progress from Git's state files (`MERGE_HEAD`, `MERGE_MSG`,
  `rebase-merge/*`, `rebase-apply/*`, `REBASE_HEAD`, `CHERRY_PICK_HEAD`,
  `REVERT_HEAD`, `BISECT_LOG`) and the unmerged index entries
  (`ls-files --unmerged`), never from conflict markers. It reports the
  branch, HEAD, original head, target, rebase step i/N, the commit Git is
  stopped at, each conflict's kind (UU, AA, UD, DU, AU, UA, DD), its three
  stages (mode, object ID, presence), binary and submodule flags, a
  fingerprint of all unmerged entries, and which of Continue, Skip and
  Abort are available with a reason. Stage sides are labelled by role on the
  server, because "ours" and "theirs" swap meaning during a rebase: merge
  ours is `HEAD · <branch>` and theirs `<target> <short>`; rebase ours is
  `<onto> + rebased so far (HEAD <short>)`, theirs
  `<short> <subject> from <branch>` and base `parent of <short>`. Status's
  conflicted entries also keep their stage modes and IDs (part of the pin).
  `git am` is reported as its own kind and, like bisect, is never managed.
- **Application records, reconciled.** `git.merge` and `git.rebase` record a
  `GitOperationRecord` (one per repository toplevel, in the snapshot) with
  the pinned branch, original head and target, the chosen target name and
  state `running`, `stopped_conflicts`, `ready`, `completed`, `aborted`,
  `ended_external` or `interrupted`, plus reserved `agent_running`,
  `agent_review`, `agent_interrupted` and `JobThreadID` for S4. A record
  matches the repository when kind and target (and for a rebase the
  original head) agree. Startup and every read reconcile it: no longer in
  progress, or a different operation, ends it as `ended_external`; a record
  whose command was running at a crash becomes `interrupted` and the next
  read resolves it. Operations started in a terminal are reported with
  `Source external` and accept the same commands.
- **The operation reserves the checkout.** While a merge, rebase,
  cherry-pick, revert or am is in progress in a checkout's worktree,
  wherever it was started, that checkout's writer lease is held by the
  operation: agent turns (fixture and ACP claims) do not start there and wait
  with `WriterWait.HolderOperation`. The holder is derived, like every other
  lease holder, from a stat of the markers in the worktree's Git directory,
  which is discovered without running Git and cached for two seconds, so it
  can be evaluated under the engine lock and ends when the operation ends.
  While any thread waits on an operation, waiters are re-evaluated once a
  second, so an abort in a terminal releases them. Bisect does not reserve;
  an am does. Paths are compared with symlinks resolved.
  A record's `JobThreadID` is exempt, so the S4 resolution job can work in
  the reserved checkout. A turn already running when an external operation
  starts is not interrupted.
- **Commands.** All five are journaled two-phase commands (ADR 0020) that
  hold the lease and a Git slot shared with linked worktrees, and bracket Git
  with the document coordinator (save and pause open documents, reconcile
  after; `document_unsaved` when a save fails). They run with the user's
  configuration (hooks, signing, merge drivers) but never an editor, and
  with `rerere.autoUpdate=false`: rerere may rewrite a file from a recorded
  resolution (reported in `RerereResolved`) but never stages it.
  - `git.merge`: `merge --no-autostash --no-overwrite-ignore
    --no-rerere-autoupdate --no-edit -m <Git's usual message> <target oid>`.
    `merge.ff` and `branch.<name>.mergeOptions` apply as in the CLI; the
    preview reports the effective setting (`MergeFF`) and exactly whether a
    fast forward happens, and a non-fast-forward merge under `only` is
    refused (`ff_only_configured`).
  - `git.rebase`: non-interactive only, `rebase --merge --no-autostash
    --no-update-refs --no-rerere-autoupdate --no-fork-point --no-autosquash
    <target oid>`; a range with merge commits is refused
    (`range_has_merges`, use a terminal); rewriting commits on a
    remote-tracking ref needs `AcknowledgePublished`; the replay count the
    user saw is pinned.
  - `git.operation_abort`, `git.operation_continue` and
    `git.operation_skip` pin kind, HEAD, step and (continue) the unmerged
    fingerprint or (skip) the commit being dropped, and all require
    `Confirmed`. Continue needs every conflict staged and may stop again at
    the next conflict; an empty resolution is reported as
    `nothing_to_commit`. Skip is rebase only.
- **Nothing outside the operation is reset or committed unseen** (review
  2026-09-25). A rebase's abort and skip reset the working tree hard, and a
  merge, cherry-pick or revert abort (`reset --merge`) resets staged
  changes. The operation state therefore lists `DiscardsOnAbort` and
  `DiscardsOnSkip`: tracked paths outside the conflicted set whose changes
  the command would reset, excluding changes the operation itself staged
  for the current step (the paths the merged side or the current commit
  changes). The confirmation names them and the command must carry them
  exactly as `AcknowledgeDiscard` (`discards_unacknowledged` otherwise);
  abort and skip also pin `WorktreeFingerprint`, recomputed after open
  documents are saved, so an edit saved or made after review is
  `stale_status`. Continue pins `StagedFingerprint` (HEAD plus every staged
  change), so a change staged by anyone after review is never committed,
  and requires `AcknowledgeMarkers` for staged files that still contain
  conflict-marker lines (bounded as described below).
- **Exact discard lists and a backup** (second review 2026-09-25). The
  discard lists no longer exempt the step's paths wholesale: a staged entry
  is the operation's own only when HEAD still has the step's old side and
  the index holds exactly its new side (merge base to MERGE_HEAD, parent to
  commit, or commit to parent for a revert; several merge bases or a merge
  picked without a known mainline make nothing own). Untracked and ignored
  files that abort (the original branch's files), skip or the remaining
  commits would write over are listed too; continue and skip are refused
  (`would_overwrite`) while such files are in the way of the remaining
  commits. Immediately before an abort or skip runs (after documents are
  saved, inside the lease) the server copies every dirty tracked file and
  every listed untracked file into two unreferenced commits (working tree,
  and staged entries as its parent) in the repository's object database;
  `GitOperationResult.Backup` and the record name them, `GET
  /v1/git/operation/backup` reads one file, and `git checkout <oid> --
  <path>` restores it. No ref is written, so garbage collection may prune
  the objects after `gc.pruneExpire`. When the copy cannot be complete
  (1000 files, 64 MiB, unreadable), the command needs
  `AcknowledgeBackupIncomplete` (`backup_incomplete`). Untracked and
  ignored files, which may hold secrets, are copied into the object
  database as unreferenced blobs too, and `git gc --prune=now` deletes a
  backup at once. The backup endpoint serves only object IDs recorded in
  `Snapshot.GitBackups` for that repository (20 per repository). This replaces the
  SQLite table the review suggested: the storage package is outside this
  slice, and Git objects restore with ordinary Git commands.
- **Third review.** A cherry-pick or revert sequence's abort returns to
  `sequencer/head`: `OrigHead` names it, `AbortDropsCommits` lists the
  commits already made that the abort removes, and files it restores are
  checked for untracked and ignored files in the way (Git itself refuses
  untracked ones; ignored ones are listed and backed up). A rebase's hard
  reset also lists and backs up untracked files inside a tracked path that
  became a directory. Acknowledgements use fingerprints of the lists
  (`DiscardsFingerprint`, `MarkersFingerprint`), so names that are not
  valid UTF-8 can be acknowledged. Assume-unchanged or skip-worktree
  entries on paths abort, skip or continue writes refuse them
  (`HiddenEntries`). Any operation command stopped by its budget is
  `outcome_unknown`/`timeout`, describing the state it left.
- **Fourth review.** A nested repository or unexpanded untracked directory
  in the way of abort, skip or continue (including one inside a directory
  that replaced a tracked file or in a file/directory conflict) refuses the
  command (`NestedInTheWay`, `not_supported`): its content can be neither
  listed nor backed up. A sequence abort reports the exact number of
  commits it drops (`AbortDropsCount`, `AbortDropsIncomplete` when the
  list of at most 200 is shorter) and needs `AcknowledgeDropped`
  (`drops_unacknowledged`). After an abort that Git reports as successful,
  the paths it rewrote are compared with HEAD; any that still differ are
  reported (`abort_incomplete`). The pending check also covers files the
  remaining commits modify, since a modify/delete conflict writes them.
  Backups are keyed by the toplevel's bytes (base64), pruned when no project
  or thread checkout overlaps their repository, and bounded to 200.
- **Fifth review.** Pending commits are compared with their parent
  explicitly (`diff-tree --stdin --root`; a merge commit against the
  sequencer's mainline, or unchecked without one), so pending merge picks
  and root commits are covered whatever the log configuration. Nested
  repositories and incomplete discard lists are tracked per command
  (`NestedOnAbort`/`OnSkip`/`OnContinue`, `DiscardsOnAbortIncomplete`/
  `OnSkipIncomplete`), and submodule directories are never counted as in
  the way. An incomplete backup is acknowledged by the fingerprint of the
  exact files it leaves out (`AcknowledgeBackupMissing`), not a yes/no. An
  external `--update-refs` rebase lists the refs it would move
  (`ContinueUpdatesRefs`) and continue and skip are left to a terminal: the
  application never moves other branches.
- **Markers are read from the staged blobs** (`cat-file --batch`, bounded
  to 5000 files, 8 MiB each and 32 MiB in total), not through `git grep`,
  so attributes cannot hide them; `conflict-marker-size` and CRLF are
  honoured and a NUL in the first 8 KiB marks a binary blob. An incomplete
  scan needs `AcknowledgeMarkersIncomplete` (`markers_incomplete`).
- **Submodules are never recursed into**: every operation command runs with
  `submodule.recurse=false`, so a dirty submodule worktree survives an
  abort. Dirty submodules are not detected, because that would run Git
  inside them, which the read policy forbids.
- **Interactive stops are left to the terminal.** A merge-backend rebase
  stopped for edit, exec, break or another non-pick todo command (the last
  `done` command, or Git's `amend` marker) reports `StopReason`; Continue
  and Skip are refused (`not_supported`), because the application cannot
  reproduce the terminal's amend and message workflow faithfully, and
  `Current` is the commit applied before stopping. Abort still works, with
  the discard rules above.
- **Hidden local changes refuse a start.** An index entry marked
  assume-unchanged or skip-worktree on a path a merge or rebase writes may
  hide a local change that status cannot see, so the start is refused
  (`not_supported`). A path blocked only by a tracked file that the
  operation itself replaces with a directory is not an obstruction.
- **Budgets.** Merge, rebase, continue and skip have 30 minutes including
  hooks; abort has 5. Exceeding it is `timeout` (`outcome_unknown`), naming
  a leftover `index.lock`. A resolution job thread also goes ahead of the
  ordinary waiters its operation holds back.
- **Clean start, nothing stashed** (user decision). A merge or rebase needs a
  clean tracked tree; untracked files are allowed. Autostash is never used,
  even when `merge.autoStash` or `rebase.autoStash` is configured. Because a
  rebase cannot refuse to overwrite ignored files, the server refuses
  (`would_overwrite`, naming the paths) when an untracked or ignored file is
  in the way of a file the target adds, or a replayed commit adds, and
  checks again after saving open documents. Git is always given the pinned
  object ID; a ref that moved is refused (`stale_target`) rather than
  merged at its newer tip.
- **Outcomes.** A result carries `GitResult.Operation`: `completed`,
  `stopped_conflicts` or `stopped` (with the operation state), `aborted`,
  `not_started`, `unchanged` or `unknown`. Git failing without starting,
  but with files or the index changed, is `partial_change`
  (`outcome_unknown`), found by comparing status and every path the target
  changes before and after, as a switch does.
- **Client.** `client.GitOperation`, `GitIntegratePreview`,
  `GitMergeCommand`, `GitRebaseCommand`, `GitOperationAbortCommand`,
  `GitOperationContinueCommand` and `GitOperationSkipCommand`; capability
  `git-operations`.

## Consequences

- While a conflict waits for the user, every agent turn in that checkout
  waits too, including the thread that started the merge. Other worktrees
  are unaffected.
- The reservation cannot stop terminals, editors or other programs; the
  pins narrow but cannot close the window between the last check and Git.
- The holder derivation stats up to five files per waiting thread under the
  engine lock and discovers Git directories by walking parents; it ignores
  `GIT_DIR`, `core.worktree` and ceiling directories, so a checkout that only
  such settings make a repository is not reserved.
- An operation started outside the application with rerere autoupdate
  recorded in its state still stages what rerere resolves when continued.
- `-m` replaces Git's generated message with the same text without
  " into <branch>"; `merge.log` still appends.
- The discard lists count changes the operation staged for the current
  step as its own; a user's own staged change to one of those paths is
  covered only by the general Abort or Skip confirmation.
- Conflict listing is bounded (500 paths) and binary detection runs one
  bounded blob read per side for the first 64 conflicts.

## Manual conflict resolution (S3)

- **Saved copies.** When an operation stops with conflicts (after a merge,
  rebase, continue or skip here, or before the first `git.conflict_*`
  command changes an operation started elsewhere), every unmerged path is
  saved: stages 1-3 with their modes and objects, and the working-tree
  content (mode, symlink target, or absence), in an unreferenced commit
  (`s1/`, `s2/`, `s3/`, `w/` subtrees and a manifest blob). Git objects
  were chosen over a SQLite table for the same reasons as the abort
  backups: the stages are already objects, the copy restores with
  ordinary Git commands, and no storage schema is added. The copy is
  recorded in `Snapshot.GitConflictCopies` by repository and stop (kind,
  target, original head, current commit, step, HEAD and attempt), pruned
  with their repository (retention below).
- **Reading.** `GET /v1/git/conflict?path&version=base|ours|theirs|working|saved`
  serves a path that is unmerged now, or has a saved copy for the current
  stop, bounded to 1 MiB, with the path's current pins (`ConflictPin` over
  its index entries, `WorktreeToken`) and, for the working file, whether it
  still has conflict markers.
- **Commands.** `git.conflict_choose` writes one side into the working tree
  only (`checkout-index --stage=N -f`, so filters apply; a deleting side
  removes the file); the path stays unmerged. `git.conflict_resolve` stages
  the working-tree file (`add -- <path>`) or resolves it as deleted (`rm
  --cached -- <path>`), touching no other path; a file that still has
  conflict markers, or is too large to check, needs `AcknowledgeMarkers`
  equal to the reviewed working-tree token. The operation becomes ready
  when no unmerged path remains. `git.conflict_restore` puts a saved copy
  back: the working-tree content atomically (temporary file or symlink
  renamed over it) and the stages through `update-index --index-info`, so
  `ls-files -u` and the file are byte-identical to the saved state. All
  three are journaled, hold the lease, pin the path, save and pause open
  documents first (a saved unsaved edit makes the pin stale: nothing
  changes), run while the operation reserves the checkout, and are refused
  when nothing is in progress. Submodule conflicts and paths that are
  directories in the working tree are refused. S4 agent jobs will use the
  same commands to accept or reject a proposed resolution.
- **S3 review: nothing is overwritten without a copy.** Before a choose or
  restore replaces or removes a file, the file's current content is copied
  (a `before_overwrite` copy, `GitOperationResult.Previous`, restorable with
  `git.conflict_restore`) unless it equals the saved state or the content
  about to be written; a path the stop's copy does not hold gets a
  `before_first_change` copy first. Copies are recorded durably before
  anything changes. Stop keys include the attempt (the inode and
  modification time of the file the operation's start created), so a
  retried operation never reuses an earlier attempt's copies. A file the
  server cannot copy (over 64 MiB, special) is overwritten only with
  `AcknowledgeUnsaved`; staging a binary file needs `AcknowledgeBinary`.
  Restore keeps the saved permission bits and removes a temporary file an
  interrupted restore left (its name is kept in the Git directory while it
  exists). An abort after resolving a path as deleted lists the untracked
  file left behind, backs it up and moves it aside once acknowledged, so
  the abort proceeds instead of failing. Files moved aside are unlinked
  only while they still match the token the backup read, and any that Git
  did not replace are put back from the backup (content and permission
  bits) whatever Git's outcome; the result says so, and a file that cannot
  be put back makes the outcome unknown. A merge, cherry-pick or revert
  abort that Git would refuse because a file it resets has unstaged
  changes is refused first (`AbortBlockedBy`, `abort_blocked`). A restore
  trusts the copy's manifest for whether the saved file existed, so a
  failed read never deletes the working file.

## Known limits

Accepted residual risks from the five review rounds (2026-09-25):

- **Outside programs.** Pins and the lease narrow but cannot close the
  window between the last check and Git: terminals, editors and other
  programs can still change files, the index or refs. A turn already
  running when an operation starts outside the application is not
  interrupted.
- **Reservation derivation.** The holder is a stat of operation markers in
  a Git directory discovered by walking parents; `GIT_DIR`,
  `core.worktree` and ceiling directories are ignored, so a checkout that
  only such settings make a repository is not reserved. An operation ended
  in a terminal releases waiters within about a second. Up to five stats
  run per waiting thread under the engine lock.
- **Backups.** They are unreferenced objects: `gc` prunes them after
  `gc.pruneExpire` and `git gc --prune=now` at once. They copy ignored and
  untracked files, which may hold secrets, into `.git/objects`. They are
  bounded to 1000 files and 64 MiB; anything beyond, special files and
  files unreadable at run time are left out (acknowledged by exact
  fingerprint; a file that becomes unreadable only at run time refuses the
  command, naming it, and the next read shows it). Directories of nested
  repositories are never backed up; the command is refused instead.
  Backup records are keyed by repository and bounded (20 each, 200 in
  total) and are pruned when no project or thread uses the repository.
- **Discard attribution.** A staged entry is the operation's own only for a
  one-sided change of the current step; with several merge bases, or a
  merge commit picked without a known mainline (a single `cherry-pick -m`
  leaves no sequencer options), everything staged is listed. A user's own
  staged edit identical to the step's result counts as the operation's.
  Untracked files that are not ignored are listed although Git itself
  refuses to overwrite them.
- **Submodules.** Operation commands never recurse into submodules, so
  submodule worktrees are never changed, but dirty submodules are not
  detected (that would run Git inside them). A nested repository at a path
  that is a gitlink in HEAD, the original head or the index is treated as
  a submodule.
- **Left to a terminal.** Interactive stops (edit, exec, break, reword,
  squash), apply-backend rebases, todo commands other than pick-like,
  revert and update-ref, pending merge commits without a known mainline,
  `--update-refs` rebases, bisect and `git am`, nested repositories in the
  way, and assume-unchanged or skip-worktree entries on written paths.
  An external rebase whose state asks for rerere autoupdate still stages
  what rerere resolves when continued.
- **Bounds.** Conflicts are listed up to 500, with binary detection for the
  first 64. The marker scan covers 5000 staged files, 8 MiB each and
  32 MiB in total (beyond that, continuing needs
  `AcknowledgeMarkersIncomplete`), skips blobs with a NUL in the first
  8 KiB, and looks for `<`, `|` and `>` markers only. Dropped commits are
  listed up to 200 (the count is exact). Pre-scans refuse beyond 20000
  written paths or 1000 untracked candidates.
- **Verification.** `abort_incomplete` compares the paths an abort wrote
  with HEAD after Git reports success; this is unit-tested but a partly
  failing Git abort has not been reproduced end to end. A command refused
  after its 30 minute budget ended is also reported as `timeout`.
- **Platforms.** Worktree checks are Unix-only; elsewhere these commands
  are effectively refused, and the server package does not currently
  build on Windows for unrelated reasons.
- **Conflict copies.** Saved copies have the same pruning and secrecy
  caveats as backups; working-tree files beyond 64 MiB in total, special
  files and directories are not saved (`Missing`) and cannot be restored
  here; overwriting such a file needs `AcknowledgeUnsaved`. Retention:
  `before_overwrite` copies are deduplicated by content per path (and not
  made when the content equals an index stage, the original copy or the
  content being written), at most 100 per path; a repository keeps 300
  copies and all repositories 2000, evicting other stops' copies first,
  oldest first, and never the stop being written (a copy of another
  repository's still-active stop can be evicted beyond 2000). A copy of
  the current stop that has to be evicted is named in
  `GitOperationResult.Evicted`. The attempt identity relies on file inode and modification time
  (not available on every platform), so an operation restarted within
  the same timestamp resolution on a reused inode would share copies. A restore creates missing parent directories but refuses symlinked
  or non-directory parents; the parent check and the rename are not atomic
  against a concurrent program replacing a parent directory.
- **Merge message.** `-m` reproduces Git's usual message without
  " into <branch>"; `merge.log` still appends.

## Deferred

Resolution commands (S3: staging, choosing a side, deleting), agent
resolution jobs (S4), the TUI, interactive rebase, rebasing merges,
`--onto`, octopus merges, cherry-pick and revert as started commands,
skip for cherry-pick and revert, `git am` and bisect management, and
predicting conflicts before a start.

## Alternatives considered

- **Autostash or stashing dirty work:** rejected by the user; the final
  stash application can itself conflict and hide user work.
- **Persisting the reservation as a record-only lease:** would miss
  operations started in a terminal and could strand a lease after one ended
  there. Rejected for derivation from the repository.
- **Merging by ref name and reporting a newer merged tip:** the pinned object
  ID is exact and a moved ref is simply refused.
