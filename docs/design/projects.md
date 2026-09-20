# Projects and navigation scope

Status: behavior selected on 2026-09-20; the Go reference is being extended with a server-owned project registry and local navigation controls. The reference still uses a synthetic Demo agent. Project selection does not establish a working provider integration or a collaborative editor.

## Choose the navigation scope

The left navigation offers **All projects** or one specific project. A searchable picker shows each project's name and server path so same-name folders are distinguishable; matching searches names and paths. The add-project control remains directly reachable beside the selector. Keyboard and pointer activation use the same actions, and search consumes its own text input.

The chosen filter belongs to the attached client's view. It changes which threads navigation lists, including the relevant closed threads; it does not switch the active center by itself. Selecting a thread explicitly opens it. Keep the active thread's actual project context in the center breadcrumb even when that thread is outside the current navigation filter. Empty filtered results must not imply that the active thread was moved, deleted or stopped.

Changing the filter never changes another client's navigation, sends a prompt, resumes saved work, restarts execution or changes a terminal's working directory. Thread restoration follows the [layout contract](layout.md#switching-threads-and-projects); checkout and worktree choices follow the [workspace contract](workspaces.md).

## Add an existing project folder

**Add project** opens a folder-path input. The path names a directory on the server's filesystem, which can differ from the machine running the attached terminal. `~/` expands to the server user's home directory. The server resolves an absolute cleaned path and symlinks, confirms that it is an existing readable directory, and stores the canonical path with a stable project identity and a basename-derived display name.

Repeated additions of the same canonical folder, including symlink aliases, select the existing project identity. Errors retain the entered path and report the failure. Missing folders, regular files, unreadable directories and paths exceeding 4096 bytes are rejected. The current registry accepts at most 128 projects; an existing-project match still succeeds at capacity.

Adding a project records application state only. It does not create a folder, initialize or mutate Git, create a worktree, edit project files or start agent work. The project's default checkout is the selected existing folder. A newly added project starts with no threads.

## Create a thread in a project

**New thread** targets an explicitly selected project. When viewing all projects, choose the destination rather than inferring it from an unrelated active thread. Creating a thread does not submit a prompt. The new Go reference thread is empty and idle, uses the project's server path, and captures the selected supported Demo settings. Its initial title is **New thread** when no title is supplied; titles are bounded to 256 bytes. The current reference accepts at most 128 threads.

The Demo model and settings are fixture capabilities. Real agent choices remain unavailable until an integration supplies and enforces them; a project folder does not make a real provider available. The full intended creation flow remains governed by [thread configuration](thread-configuration.md). Existing thread prompts, requests, history and terminal sessions are unaffected by creating another thread.

The user-selected lifecycle actions are **Close**, **Reopen** and the separate destructive **Delete** action. Closed threads remain associated with their project and discoverable under **Closed**. Filtering never performs any lifecycle action. The [T3 source inspection](../research/t3-code-design.md#project-selection-and-thread-lifecycle-inspection--2026-09-20) records the reference and distinguishes its terminology from this project's choices.

## Persistence and current evidence

The server persists projects with authoritative application state. Existing saved threads that lack project identities are grouped by their legacy project labels into stable identities. Their individual checkout values are preserved, including synthetic `fixture://` paths. A migrated group's first checkout supplies its project path; this migration does not claim that a synthetic path is a real directory. An intentionally empty snapshot remains empty.

Focused Go tests cover canonical and symlink deduplication, home expansion, invalid-path rejection, existing-file preservation, legacy migration, an empty snapshot, captured settings, empty idle creation and capacity limits. The command run for this slice was `GOCACHE=/tmp/tui-go-build-cache go test ./internal/server -run 'TestProject|TestEnsureProjects'`; it passed. This evidence concerns the server helpers. It does not verify the navigation UI, real agent behavior, terminal compatibility or full application integration. Broader validation belongs in the implementation review report.

Project deletion, project configuration, a directory-browsing or native folder picker, and real provider integration are deferred. The current folder input is not an OS file-browser integration.
