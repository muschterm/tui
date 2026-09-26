---
status: accepted (user decisions 2026-09-25)
---

# Explicit managed worktrees

[docs/design/workspaces.md](../design/workspaces.md) selected the project's
existing checkout by default (Q9) with worktrees as an explicit option, and
per-checkout writer queues (Q13). The worktree contract (location, start
state, sharing, recovery and cleanup) was left open. The user decided on
2026-09-25: worktrees live inside the application home, always on a new
branch, one thread per worktree, Remove only when every attached thread is
Closed, the branch is kept, and deletion of ignored files is disclosed. This
records the Go server, protocol and client slice; the Go TUI binding is in
[go-slice.md, Explicit worktrees (TUI) — 2026-09-26](../design/go-slice.md#explicit-worktrees-tui--2026-09-26).

## Decision

- **Opt-in at thread start.** `thread.start` accepts
  `Workspace{Mode: "worktree", StartOid, Branch}` behind capability
  `worktree-create`. Mode `checkout` (or no workspace) keeps the existing
  behavior, and an explicit checkout request ignores any worktree default.
  Nothing else creates a worktree; starting a thread never does so
  implicitly.
- **Always a new branch at a pinned commit.** The server runs
  `git worktree add -b <branch> <path> <start oid>` in the project's
  repository. The branch must be a valid, not yet existing name
  (`invalid_branch`, `branch_exists`); the start commit must resolve
  (`invalid_start`) and, for a project that is a subdirectory of its
  repository, contain that folder (`project_folder_absent`). Uncommitted
  work in the project checkout is never copied or moved. Like other Git
  writes, `git worktree add` runs with the user's configuration, so
  `post-checkout` hooks and smudge/LFS filters run in the new worktree.
- **Location.** `<home>/worktrees/<project-id>/<branch-slug>-<cmd8>`, where
  `cmd8` derives from the command ID. An existing path is refused
  (`path_exists`). `Thread.Checkout` is the worktree joined with the
  project's path inside the repository, so Files, Git, terminals, documents
  and the agent all follow it.
- **Records.** `Snapshot.Worktrees []ManagedWorktree{ID, ProjectID, Path,
  CommonDir, RelPath, AdminName, Branch, StartOid, CommandID, CreatedAt,
  State, Detail, MovedTo, Unverified}` and `Thread.WorktreeID`. One user thread
  per worktree. States: `creating`, `present`, `missing`, `moved`,
  `unregistered`, `unattached`, `removed`. State is detected from the file
  system (the worktree's `.git` file and Git's registration under the
  common directory), at startup, on read and every two seconds. Detection
  made outside the engine lock is applied only while the record still has
  the state and path it was detected from, and detection never writes
  `creating` or `removed`; commands own those transitions.
- **Two-phase, journaled creation.** Phase one takes the repository's Git
  slot (shared with linked worktrees and ref operations), saves the
  `creating` record with a `running` receipt, and only then runs Git; a
  storage failure means Git never runs. From then on the server owns the
  creation: Git, verification and attachment run in the background under
  the server's context (10-minute budget; server stop cancels it like a Git
  write), never the request's, so a client timeout or disconnect does not
  stop it. The request waits about two seconds for the outcome and
  otherwise answers with the `running` receipt; a retry of the same command
  ID while it runs answers `running` at once. The snapshot's record (found
  by `CommandID`) shows the progress, `creating` then `present` with its
  thread (or `unattached`), and the final receipt is persisted, published
  and returned to the next retry. Git then runs and the result is
  verified: a `.git` link, no remaining Git `initializing` lock, HEAD on the
  new branch at the start commit. The receipt fails (`worktree_failed`) when
  Git or verification fails; if no directory was left the record is
  dropped, otherwise it is kept `Unverified` for inspection and never
  attached, not even by a retry — the user removes or forgets it. A
  verified worktree whose thread cannot be attached (capacity, validation)
  keeps an `unattached` record, never deleted; retrying the same command ID
  attaches it without running Git again, and an accepted receipt is
  replayed. A worktree `thread.start` may return a non-error receipt with
  State `running` (Git still working, for example after a reconnect) or
  `failed`; clients must re-ask under the same command ID for the outcome
  rather than issue a new command. At startup a `creating` record is dropped only when its
  directory does not exist; otherwise it is verified as above and becomes
  `unattached` (retry attaches) or `Unverified`. A server stop mid-creation
  kills Git; the receipt fails saying the server stopped (and whether the
  new branch was kept), and anything left is kept `Unverified`, even when
  Git had already finished, since verification could not complete. A
  same-ID request that waited while the first was still preparing is
  answered `running` as soon as creation is handed to the server. A panic
  in the background creation is recovered: the Git slot is released, the
  receipt fails (`worktree_failed`) and anything left is kept `Unverified`.
- **Unavailable workspaces are refused, not redirected.** For a thread
  whose worktree is not `present` (or whose record is gone), Send, Resume,
  Files/browse and context capture, documents, Git reads and writes
  (including merge/rebase and resolution jobs), and terminal open fail with
  `workspace_unavailable` from one resolver; workspace info reports the
  checkout as unavailable without running Git. Queued prompts stay queued
  and out of the writer line; the thread stays idle without an error (the
  worktree's state says why), and the writer rebalance after relocation or
  a detection that finds the worktree present again dispatches them. As a
  second layer, Git run at or below a managed worktree record's path gets
  that path's parent in `GIT_CEILING_DIRECTORIES`, so discovery from a
  worktree whose `.git` is gone cannot reach a repository enclosing it; the
  set is derived from the current records on every publish, so Git
  elsewhere (including other projects next to a relocated worktree) is
  unaffected, and a forgotten or removed record's ceiling goes with it.
  Each record's resolved path is cached, so a publish resolves symlinks
  only for a new or moved record.
- **Draft context comes from the project checkout.** The worktree does not
  exist while a new-thread draft is edited, so attachments and `@` file
  mentions of a worktree `thread.start` are captured from the project
  checkout at Send, while the agent then works in the new worktree at
  `StartOid`. The two can differ when the checkout has uncommitted changes
  or another commit checked out.
- **Resolution jobs.** A conflict-resolution job started in a managed
  worktree (its checkout is the worktree's top) gets that worktree's
  `WorktreeID`: its dispatch is gated like any worktree thread, relocation
  moves it (keeping its place relative to the worktree) and, while not
  Closed, it blocks removal (`worktree_in_use`). A worktree may therefore
  have its thread plus job threads attached.
- **Writer coordination.** A worktree is its own checkout: its canonical
  path is its writer lease key and does not overlap the project checkout,
  so threads in different worktrees run concurrently. Ref-changing Git
  work shares one slot per common directory.
- **Recovery and cleanup commands** (capability `worktree-manage`), all
  journaled with command identity:
  - `worktree.relocate`: follow a worktree Git reports as moved
    (`git worktree move`); updates the record and thread checkout and
    clears the agent session (its working directory changed). Refused
    unless the state is `moved` (`not_moved`), while an attached thread
    works or has terminals, while a document is open from the old path
    (`documents_open`) and while a Git change runs in the repository
    (`git_busy`).
  - `worktree.forget`: drop a record whose worktree is `missing`, `moved`,
    `unregistered` or `removed`; refused while it is `present`,
    `unattached` or `creating` (`worktree_present`). A directory that still
    exists (unregistered, or at a moved location) is left on disk. The
    thread stays and cannot send.
  - `worktree.prune`: `git worktree prune` for the project's repository,
    confirmed by the fingerprint of `GET /v1/worktrees/prune`
    (`stale_confirmation`, `nothing_to_prune`); the listing is taken again
    while the command holds the repository's Git slot and must match.
  - `worktree.remove`: `git worktree remove <path>`, never `--force`,
    confirmed by the fingerprint of `GET /v1/worktrees/removal`. Blockers:
    an attached thread not Closed (`worktree_in_use`), open terminals in
    the worktree (`terminals_open`), unsaved documents
    (`documents_unsaved`), a Git change running (`git_busy`), a merge,
    rebase or similar operation in progress (`operation_in_progress`), a
    dirty worktree (`worktree_dirty`) or a state other than
    present/unattached (`workspace_unavailable`). The preview discloses ignored entries (count
    plus up to 20 paths) that Git's remove deletes. The branch is kept; the
    record stays as `removed` so the Closed thread keeps its history. The
    running removal counts against its project, so project removal is
    refused (`git_busy`) until it ends.
- **Project removal keeps directories.** Removing a project deletes its
  worktree records with its threads; worktree directories and branches stay
  on disk. The project removal confirmation lists them
  (`protocol.ProjectWorktrees`).
- **Client.** `internal/client/worktrees.go` builds the start, relocate,
  forget, prune and remove commands and reads the removal and prune
  previews.

## Consequences

- A worktree costs a full working tree on disk under the application home,
  outside the project folder; users see it only through the application
  until they browse there.
- Worktree threads cannot share a worktree, and switching a thread between
  a worktree and the checkout is not possible.
- A missing or moved worktree strands its thread until relocated or
  forgotten; nothing is recreated automatically.

## Known limits

- **Outside programs.** Terminals, editors and Git in a shell can move,
  delete or dirty a worktree between the removal preview and Git; the
  fingerprint and Git's own refusal without `--force` narrow but do not
  close that window. Ignored files created after the preview are deleted
  without having been listed.
- **Detection.** State comes from stats of `.git` and the admin directory,
  checked every two seconds; a change is noticed at the next check, read or
  command. `GIT_DIR`, `core.worktree` and relocated common directories are
  not followed.
- **Crash during creation.** Startup keeps anything Git left as
  `Unverified`; there is no command to re-verify it, so the user removes or
  forgets it. Verification checks the branch, commit and Git's lock, not
  that every file was checked out.
- **Agent-run Git.** Git that an agent (or a terminal) runs inside its turn
  gets no ceiling from the application; a worktree whose `.git` disappears
  mid-turn can lead such Git to an enclosing repository.
- **Resolver coverage.** Agent turns already running when a worktree goes
  away are not interrupted, and a document already open in a worktree keeps
  its own save path; both are covered only by the next resolver call.
  A forgotten record's path gets no ceiling; its thread is protected by the
  resolver only. The ceiling set is process-wide, mirroring the one
  engine's records.
- **Branch and admin names.** The branch is kept after Remove and after a
  Git failure that created it (reported in the receipt); the user deletes
  it separately. Git's admin name is recorded but not validated against
  renames made outside the application.
- **Tests.** Behavior is verified by unit/integration tests with temporary
  repositories, not by a real-terminal or cross-platform matrix.

## Deferred

The TUI (draft Workspace row, recovery and remove actions), worktrees from
an existing branch, detached or shared worktrees, copying uncommitted work,
branch deletion on remove, worktrees outside the application home, and
non-Git isolation.

## Alternatives considered

- **Worktrees beside the repository (`../<repo>-<branch>`):** visible to
  users but writes into folders the user did not register; rejected by the
  user in favor of the application home.
- **Reusing an existing branch:** would risk two checkouts of one branch or
  surprising state; rejected in favor of always creating a new branch.
- **Deleting a worktree whose thread could not be attached:** simplest, but
  destroys a directory the user might already be using; kept as
  `unattached` instead.
- **`git worktree remove --force`:** would discard untracked and modified
  work; rejected.
