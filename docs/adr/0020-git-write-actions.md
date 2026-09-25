---
status: accepted (user decisions 2026-09-24)
---

# Git write actions: stage, unstage, discard and commit

The Go reference's Git surface was read-only. Its first writes are whole-file
stage, unstage and discard, and commit (including amend). Writes change user
work, can run repository-defined programs, compete with agent turns in the
same checkout and must not be repeated by a retry or a restart.

## Decision

- **CLI parity for writes, neutralization for reads.** A user-initiated write
  resolves Git configuration exactly as the user's own `git` would:
  repository-local filters (LFS clean on add), hooks, fsmonitor and signing
  (`commit.gpgSign`, `gpg.program`) all apply. Reads keep the policy in
  `apps/go/internal/server/git.go`, which never runs a command defined only in
  local config, because reads happen without a user decision. Writes still
  never start an editor (`GIT_EDITOR=:`, `core.editor`, `sequence.editor`) or
  pager, never prompt for credentials, never lazily fetch from a promisor
  remote, never start automatic gc or maintenance, and have empty stdin apart
  from the commit message. Inherited `GIT_*` variables are removed, as for
  reads, and so are `GPG_TTY` and `SSH_TTY`; Git starts in a new session with
  no controlling terminal, so a terminal pinentry cannot draw on a user's
  terminal while a graphical or agent-cached pinentry still works (otherwise
  signing fails within the budget). Because `GIT_NO_LAZY_FETCH` needs Git
  2.44, older Git refuses writes in a partial clone (`not_supported`).
- **Hooks run with a budget.** A write, including its hooks, has 5 minutes.
  Git leads its own session and process group; cancellation (server stop or
  the budget) sends SIGTERM so Git removes its own locks, then SIGKILL after
  2 s. Git's exit status decides the outcome even when a hook leaves a
  background job holding its output open; such leftovers in the group are
  then ended (SIGTERM, SIGKILL after 0.5 s). Combined stdout/stderr is
  returned verbatim, bounded to 64 KiB.
- **Durable commands with a two-phase journal.** `git.stage`, `git.unstage`,
  `git.discard` and `git.commit` use `POST /v1/command` with a typed
  `Command.Git` payload and a thread or project target. Refusals found before
  Git runs return a protocol error and record nothing. Otherwise the command and
  a `running` receipt are persisted, with a `running` `GitOp` in the snapshot,
  before Git starts; Git runs outside the engine lock; the final receipt then
  replaces the running one. A retry with the same ID waits for a running
  attempt and returns the stored receipt; Git never runs twice. A receipt
  still `running` at startup, and its `GitOp`, become `outcome_unknown` with
  code `interrupted`: the client refreshes status to learn what happened. If
  the final save fails, the outcome stays in memory, retries are answered
  from it, and the save is retried three times, on every later Git command
  and at shutdown. A panic while preparing or running is recorded as
  `internal_error` (before Git: refused; during: `outcome_unknown`) and the
  lease is released. If the stored receipt cannot be read, a retry is refused
  with `unknown_outcome_lookup` (whether it ran is unknown); a failed
  phase-one save is `storage` (Git did not run). Thread deletion and project
  removal are refused with
  `git_busy` while a Git write targeting them is in progress, and a
  `GitOp` is removed with the thread or project that it targeted.
- **Busy checkouts refuse; Git writes hold the lease.** While any thread holds
  an overlapping writer lease (a `running` or `waiting` turn, including one
  waiting on a question or approval, or an ACP claim in progress), every Git
  write is refused with `checkout_busy`, naming the thread. Git writes never
  queue. A running Git write holds the lease itself as a non-thread holder,
  so no agent turn starts there; queued turns wait with
  `WriterWait.HolderGitCommandID`. A second Git write in the same repository is
  refused with `git_busy`. Git writes compare lease keys with the repository
  toplevel by path overlap (either path equal to or inside the other), so a
  project registered at a subdirectory, or a thread whose checkout contains the
  repository, is the same checkout for Git writes.
- **Revalidate what was shown.** Status entries gain porcelain v2 modes and
  object IDs, an `lstat` token (size, mtime, ctime, inode, mode) for worktree
  sides and an opaque `Pin` over everything the entry shows for its own side.
  Status gains `HeadOid`, `StagedFingerprint` (HEAD plus the complete staged
  set), `HeadOnUpstream` and the resolved `Identity` with config scopes. The
  worktree token records the node type, so any type change is stale. When
  status did not list the whole staged set (`StagedTruncated`), the
  fingerprint is marked and commit is refused with `status_truncated`: too
  many changes to review here; commit from a terminal.
  Stage, unstage and discard send the entry's pin back; commit sends the
  expected HEAD (or `unborn`) and the staged fingerprint. Pins are recomputed
  with the read policy, so they compare against exactly what was displayed,
  and a mismatch is refused (`stale_entry`, `stale_head`, `stale_status`).
  - Stage hashes the pinned file as `git add` would (`hash-object --path`, from
    a descriptor opened without following symlinks and re-checked against the
    token), runs `git add`, and reports `staged_newer_content` when the staged
    object differs. Nothing is rolled back.
  - Unstage uses `restore --staged` (`reset HEAD` before Git 2.23; `rm
    --cached` when unborn) and unstages both sides of a staged rename.
  - Stage and discard of an unstaged path act only when its worktree node is
    a regular file, a symlink, or truly absent (every existing ancestor a
    real directory). A directory (which Git would add or delete
    recursively), a path below a file or symlink, or a special file is
    refused (`not_supported`); the node is checked again immediately before
    Git runs.
  - Discard requires `Confirmed`; the pin is the confirmation of what was
    shown. Unstaged tracked changes are restored from the index after a last
    token check. Discarding an intent-to-add entry empties the file, as
    `git restore` does; Git never stored its content, so this is as permanent
    as deleting an untracked file. Status marks such entries `IntentToAdd`
    and clients present the discard as permanent. A worktree rename of an
    intent-to-add file (`.R`) is refused (`not_supported`).
  - Stage, unstage and discard refuse (`not_supported`) when another index
    entry, or for unstage another HEAD entry, is an ancestor or descendant of
    the path: `git add`, `restore` and `rm` on one side of such a
    file/directory pair silently replace the other, losing staged-only
    content. The check runs before journaling and again just before Git. Untracked regular files and symlinks are unlinked beneath the
    toplevel without following symlinks; the link is removed, never its
    target. Directories, nested repositories, staged-only and conflicted
    entries are refused.
  - Commit uses `commit --file=- --cleanup=whitespace` (so `#` lines are kept),
    refuses empty messages, messages over 64 KiB, empty commits, unmerged
    indexes and merge, rebase, cherry-pick or revert in progress, and checks
    the author/committer identity first (`identity_missing`). Afterwards the
    commit's tree change against the old HEAD is compared with the pinned
    staged set; a difference is reported as `hooks_changed_content` on a
    successful result. The code keeps its name, but the cause may be a hook
    or another process changing the index during the commit, and the
    message says so.
- **Amend** is pinned to the current HEAD, is allowed with nothing staged, is
  refused on an unborn branch and requires `AcknowledgePublished` when HEAD is
  reachable from the upstream or any remote-tracking ref (local refs only).
  Status exposes `HeadOnUpstream` so the TUI can warn before the user confirms.
  When reachability cannot be determined, it counts as published
  (`HeadOnUpstreamUnknown` is also set).
- **Locks are never deleted.** An existing `index.lock` or `HEAD.lock` is
  retried three times at 200 ms, re-checking every pin each time, and then
  refused as `index_locked` or `ref_locked`. Git's own lock refusal during the
  write is reported with the same codes.
- **Paths** travel as literal pathspecs after `--`; names with spaces, leading
  dashes, glob or magic characters and newlines work. A path that is not valid
  UTF-8 cannot round-trip through JSON, so its entry has no pin and cannot be
  changed. Submodule gitlinks and untracked nested repositories are refused
  (`not_supported`).

The wire contract, including every code, is documented in
`apps/go/internal/protocol/git_write.go`; the Go client builds commands with
`client.GitStageCommand`, `GitUnstageCommand`, `GitDiscardCommand` and
`GitCommitCommand` and sends them with `Client.GitWrite`.

## Consequences

- Repository-local hooks, filters and signing programs run with the server
  user's privileges when the user asks for a write. This matches running
  `git` in that directory; a repository whose local config was written by an
  agent can therefore run that agent's program on the user's next write.
- Status costs a few more Git invocations (identity, upstream reachability)
  and one `lstat` per worktree entry.
- The lease prevents agent turns in this application from overlapping a Git
  write. It does not stop terminals, editors or other programs; pins narrow,
  but cannot close, the window between the last check and Git's own action.
- A Git write never waits behind agent work, so a user must retry after the
  holder finishes.
- Output is untrusted text. Clients must sanitize it before painting.
- At shutdown a hook that ignores SIGTERM is killed by SIGKILL on the Git
  process after 2 s, which can strand `index.lock` or `HEAD.lock`; the
  write's `GitOp` and receipt end `outcome_unknown`, and the user removes a
  stale lock by hand after checking no Git process runs. This is accepted
  rather than delaying shutdown further.
- Lease overlap compares paths byte-wise. On a case-insensitive filesystem,
  two spellings of one directory are not recognized as overlapping.
- Worktree safety checks and group cancellation are Unix-only. On other
  platforms path writes are refused (`unavailable`) because the worktree
  cannot be examined safely, and cancelling a commit could not end its hooks.

## Deferred

Hunk and line staging, multi-path selection, staging or resolving conflicted
paths, submodule operations, remote operations (fetch, pull, push), Git write
coordination with live editor buffers and autosave, and an explicit
client-initiated cancel of a running write.

## Alternatives considered

- **Neutralized config for writes, as for reads:** LFS and other filters
  would store raw content, hooks would silently not run and signing policy
  would be bypassed. Rejected by the user in favour of CLI parity.
- **Queueing Git writes behind agent turns:** a queued discard or commit could
  run long after the user reviewed a state that the agent then changed.
  Rejected; refuse and let the user retry.
- **Single-phase journaling after Git returns:** a crash between Git and the
  receipt would let a retry commit twice. Rejected for the two-phase record
  and `outcome_unknown`.
