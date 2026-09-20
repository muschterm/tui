# Existing checkouts and optional worktrees

Status: Q9 selected the existing checkout by default, with worktrees as an option; Q13 selected queued writers per checkout. Application-home SQLite persistence is selected; workspace identity and worktree lifecycle details remain open.

## Accepted behavior

Starting a thread uses the project's existing checkout unless the user explicitly selects a worktree. It must not silently change the checkout, create a branch, move uncommitted changes, or hide existing work in a different directory. The thread has one chosen agent; that choice does not imply an isolated filesystem.

The interface must make the active directory and Git branch/worktree clear wherever an operation depends on them. Files, Git and the headless agent use the selected thread's checkout; new terminals start in the selected execution location. Existing terminals retain their own shell sessions and working directories when the user switches threads or projects. Navigation must never redirect a running shell to make its directory match the newly selected thread. Preserve and identify terminal associations rather than silently substituting another session. See [thread restoration](layout.md#switching-threads-and-projects).

Worktrees are an explicit option for isolation. Their starting commit/branch, inclusion or exclusion of existing uncommitted work, naming, reuse, and deletion rules still need a contract. Do not infer that creating one copies the current checkout's unsaved or uncommitted content.

A conflict-resolution job operates where the interrupted merge/rebase lives. Choosing another agent for that job does not move the operation to a fresh worktree or change the originating thread's assigned agent. The selected agent edits and stops for review before staging/continuation under the corrected [Git contract](git-client.md).

## Writer coordination

The application server permits one active writing thread or conflict-resolution job per checkout and queues competing work. Different worktrees can run concurrently. The owning run includes its delegated subagents; making a child wait for the parent's writer slot would deadlock delegation. Ownership cannot be released merely because the parent printed a final message while known write-capable children remain active.

A thread's prompt queue is separate from this shared writer queue. A queued prompt must not start agent work until both the thread and its checkout are eligible. Read-only bypass needs an enforced execution policy; calling a prompt read-only does not make its tools incapable of writing. Internal permission-mode IDs are also insufficient; verify effective policy against the [configuration mapping](../research/acp-configuration.md).

While a Git operation remains conflicted or an agent-produced resolution awaits user review, competing writing runs must not alter that checkout. Preserve an explicit operation/review state until the user resolves, continues, or aborts it. Queue advancement, cancellation, and fairness need detailed behavior without weakening that exclusion.

Application coordination does not prevent collaborative human edits, unrelated editors, shells, or agents from changing files or Git state. Shared documents apply the accepted [autosave and external-change rules](editor.md). The eventual implementation must detect stale prepared edits/actions and preserve unsaved buffers and unrelated staged work. The bottom terminal also counts as a possible writer; a task-level queue alone does not make its filesystem exclusive.

Application-managed Git operations that rewrite files also coordinate with the document-save pipeline: finish pending saves, pause disk autosave during the operation, and reconcile buffers before resuming. A writer lease alone does not establish that earlier saves have finished or that open buffers match Git's result. Failures preserve work for review. See [Git/buffer coordination](git-client.md#coordinating-git-with-live-buffers).

Non-Git folders remain usable for generic applications and file/terminal work. Their workspace identity and persistence need specification; apply the same single-owner coordination to their write-capable work. Git worktrees are not available for them.

## Deferred details and verification

- Worktree creation/start-state controls and how a user sees the cost of isolation.
- Whether multiple threads may share one explicit worktree, and how that affects scheduling.
- Thread persistence and association with moved, missing, or externally deleted directories.
- Worktree cleanup after completion, including dirty files and live processes; no silent data deletion.
- Workspace identity across symlinks or nested repositories, and the scope of serialized Git mutations.
- Scenarios proving every surface and agent sees the selected checkout, existing work is preserved, and conflicting external edits invalidate stale actions.

These are documentation decisions and acceptance targets. No worktrees or application processes were created for this design.
