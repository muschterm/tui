# Git operation semantics and interaction research

Date: 2026-09-19. Evidence is official Git documentation, versioned upstream source where noted, and Sublime Merge documentation. No repository operations or interactive product checks were performed. Safeguards and acceptance scenarios below are proposals for the parent design specification, not settled implementation choices.

The requested scope includes a commit graph, context-menu actions including soft reset, fast-forward-only pull by default, rebase, manual conflict resolution, and choosing a connected agent to assist with merging or conflicts. This research does not narrow that scope or add hard reset or force push.

## Graph targets and soft reset

Graph selection is presentation state; it must remain distinct from the checked-out branch and `HEAD`. Sublime Merge names source and destination branches in graph context-menu merge actions and exposes conflict resolution from unmerged files. These are useful precedents for explicit action targets. [Sublime Merge guide](https://www.sublimemerge.com/docs/getting_started)

`git reset --soft <commit>` moves `HEAD` to the target, moving the current branch tip when attached. Detached `HEAD` moves without moving a branch. The index contents and working-tree files remain unchanged. Consequently, the staged diff against the new `HEAD` changes: previously staged content is preserved, but the displayed patch can now include changes formerly committed. Unstaged differences remain between the same index and working tree. `ORIG_HEAD` records the previous tip when one exists. Soft reset is neither file-level unstaging nor a checkout of the selected branch. [git-reset](https://git-scm.com/docs/git-reset)

An unborn branch has no commit at its tip. [Git glossary](https://git-scm.com/docs/gitglossary#Documentation/gitglossary.txt-aiddefunbornaunborn) Source inspection of Git 2.54.0 shows that omitted/default `HEAD` on an unborn branch receives special handling without updating the ref; an explicit existing commit instead follows the normal ref-update path and can establish that branch. The source also rejects soft reset during a merge or with unmerged index entries. These are source-derived findings, requiring installed-version tests before support claims. [Versioned reset implementation](https://raw.githubusercontent.com/git/git/v2.54.0/builtin/reset.c)

Proposed menu wording: “Soft-reset current branch `<name>` to `<commit>`,” with detached/unborn-specific wording. Capture the target object ID when opening the action, then revalidate current `HEAD`, branch, and operation state before execution. Show the resulting staged comparison; do not silently restage files or substitute another reset mode.

## Pull and network behavior

Pull first fetches, then integrates into the current branch. `--ff-only` rejects divergent history rather than creating a merge commit. A rejected integration may therefore follow a successful fetch; remote-tracking information can already have changed. [git-pull](https://git-scm.com/docs/git-pull)

Proposed behavior implements the user's default explicitly rather than relying on inherited configuration: ordinary Pull uses fast-forward-only semantics, reports divergence, and offers separately chosen follow-up actions without automatically rebasing or merging. Surface the remote, upstream, fetch result, and integration result. Handle missing upstream, authentication failure, and cancellation distinctly. Automatic/background fetch policy remains open. Product network features do not authorize contributor pulls or pushes in this repository.

## Rebase and conflict state

Rebase replays commits onto another base, potentially replacing their identities. On conflict, resolve and stage affected paths before `--continue`; `--skip` omits the current patch; `--abort` restores the original branch/starting position. `--quit` differs: it leaves `HEAD`, index, and working tree in place. During a merge-backend rebase, “ours” means the rebased series starting at the upstream, while “theirs” means the working branch being replayed. Autostash can enable dirty-worktree rebases, but its final application can itself conflict. [git-rebase](https://git-scm.com/docs/git-rebase)

Proposed controls should name the replayed commit and actual side identities, not rely solely on “ours/theirs.” Keep Continue, Skip, and Abort separate; Skip needs a clear omitted-patch preview. Disabling Continue while unresolved entries remain is necessary but cannot establish semantic correctness.

For ordinary merge conflicts, the index can hold ancestor, `HEAD`, and incoming versions in stages 1, 2, and 3. [git-merge](https://git-scm.com/docs/git-merge) `git ls-files --unmerged --stage` exposes entries; not every conflict has all three. [git-ls-files](https://git-scm.com/docs/git-ls-files) A resolver must handle missing sides, deletions, renames, and binary conflicts rather than infer conflict state solely from text markers.

Sublime Merge presents ours, editable merged result, theirs, optional base, hunk-selection buttons, and explicit Save and stage. This suggests a reviewable result buffer with separate side labels and operation progress. [Sublime Merge conflict resolver](https://www.sublimemerge.com/docs/getting_started#resolving-merge-conflicts)

## Proposed safeguards and open decisions

- Recheck filesystem content, index entries, branch identity, and active operation before applying a prepared action. Preserve dirty editor buffers; detect external edits before saving or staging.
- Serialize application mutations per affected repository/worktree and coordinate connected agents. Application coordination cannot prevent unrelated processes from writing; detect stale state and stop rather than overwrite it.
- Preserve unrelated staged work and stage only reviewed resolutions. Decide how dirty work is accommodated before rebase, including explicit stash policy and restoration evidence.
- Let users select a connected agent and provide operation context, conflict versions, owned paths, and constraints. Decide whether it may only propose edits, save/stage resolutions, or continue the operation. Human review versus delegated continuation remains unresolved.

Candidate acceptance scenarios: soft reset preserves staged and unstaged bytes; detached/unborn targets use correct wording; stale graph actions stop; divergent pull fetches but never falls back; rebase labels remain correct across repeated conflicts; Skip and Abort have distinct outcomes; missing conflict stages render correctly; concurrent edits invalidate stale resolutions; agent failure preserves recoverable work and never silently continues.
