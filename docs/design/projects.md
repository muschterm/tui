# Projects and navigation scope

Status: behavior selected on 2026-09-20; the Go reference is being extended with a server-owned project registry and local navigation controls. The reference still uses a synthetic Demo agent. Project selection does not establish a working provider integration or a collaborative editor.

## Choose the navigation scope

The left navigation offers **All projects** or one specific project. A searchable picker shows each project's name and server path so same-name folders are distinguishable; matching searches names and paths. The add-project control remains directly reachable beside the selector. Keyboard and pointer activation use the same actions, and search consumes its own text input.

The chosen filter belongs to the attached client's view. It changes which threads navigation lists, including the relevant closed threads; it does not switch the active center by itself. Selecting a thread explicitly opens it. Keep the active thread's actual project context in the center breadcrumb even when that thread is outside the current navigation filter. Empty filtered results must not imply that the active thread was moved, deleted or stopped.

Changing the filter never changes another client's navigation, sends a prompt, resumes saved work, restarts execution or changes a terminal's working directory. Thread restoration follows the [layout contract](layout.md#switching-threads-and-projects); checkout and worktree choices follow the [workspace contract](workspaces.md).

## Add an existing project folder

**Add project** opens a searchable folder-path picker, starting at the app General **Project starting folder** (the server user's home by default). Type a name prefix, browse matching directories, or enter an absolute or `~/` path. Up/Down selects a result; Enter/Tab browses it; an explicit Add action registers the chosen directory. Escape dismisses without changing the draft. The path names a directory on the server's filesystem, which can differ from the machine running the attached terminal. `~/` expands to the server user's home directory. The server resolves an absolute cleaned path and symlinks, confirms that it is an existing readable directory, and stores the canonical path with a stable project identity and a basename-derived display name.

Repeated additions of the same canonical folder, including symlink aliases, select the existing project identity. Errors retain the entered path and report the failure. Missing folders, regular files, unreadable directories and paths exceeding 4096 bytes are rejected. The current registry accepts at most 128 projects; an existing-project match still succeeds at capacity.

Adding a project records application state only. It does not create a folder, initialize or mutate Git, create a worktree, edit project files or start agent work. The project's default checkout is the selected existing folder. A newly added project starts with no threads.

## Create a thread in a project

Use a square-and-pencil compose icon at the right of the navigation header,
replacing the full-width `+ New thread` row. Hover/focus help says New thread;
Tab/Enter and the F4 New thread command remain available. The explicit plain-icon
fallback uses `+`. Keep Add project separate beside the project selector.

**New thread** always opens a searchable destination picker, including with a specific navigation filter or only one registered project. Search matches project names and paths. The picker includes Add project; completing that flow opens or restores the added project's draft. Selecting an existing destination does the same. Never infer the destination from the active thread or skip this choice because only one project exists. It opens or restores that client's per-project draft without creating a server thread or starting work. Draft prompt text, attachments and settings survive navigation and client-view persistence. Demo Agent is preset, but Reference model must be explicitly selected before its supported defaults become valid for Send. The first valid Send atomically creates the thread and accepts the initial captured prompt through `thread.start`; its initial title is **New thread**. The current reference accepts at most 128 authoritative threads. A rejected Send preserves the draft without creating an empty thread.

If the project is removed while a local draft exists, retain its content and show the missing-project state; sending cannot silently target another project or recreate the removed identity.

The Demo model and settings are fixture capabilities. Real agent choices remain unavailable until an integration supplies and enforces them; a project folder does not make a real provider available. The full intended creation flow remains governed by [thread configuration](thread-configuration.md). Existing thread prompts, requests, history and terminal sessions are unaffected by creating another thread.

The user-selected lifecycle actions are **Close**, **Reopen** and the separate destructive **Delete** action. Closed threads remain associated with their project and discoverable under **Closed**. Filtering never performs any lifecycle action. The [T3 source inspection](../research/t3-code-design.md#project-selection-and-thread-lifecycle-inspection--2026-09-20) records the reference and distinguishes its terminology from this project's choices.

## Persistence and current evidence

The server persists projects with authoritative application state. Existing saved threads that lack project identities are grouped by their legacy project labels into stable identities. Their individual checkout values are preserved, including synthetic `fixture://` paths. A migrated group's first checkout supplies its project path; this migration does not claim that a synthetic path is a real directory. An intentionally empty snapshot remains empty.

Focused Go tests cover canonical and symlink deduplication, home expansion, invalid-path rejection, existing-file preservation, legacy migration, an empty snapshot, captured settings, legacy empty idle creation and capacity limits. Those earlier checks describe `thread.create`, which remains a compatibility API; the new UI uses first-send creation. The command run for this slice was `GOCACHE=/tmp/tui-go-build-cache go test ./internal/server -run 'TestProject|TestEnsureProjects'`; it passed. This evidence concerns the server helpers. It does not verify the navigation UI, real agent behavior, terminal compatibility or full application integration. Broader validation belongs in the implementation review report.

The terminal folder picker performs bounded asynchronous server directory queries, with cancellation and stale-result isolation on edits, dismissal and navigation. Name-prefix matching is scoped to one directory; hidden entries appear only for an explicit dot prefix. Errors preserve the entered path. This is not an OS-native file-browser integration; real provider integration remains deferred. Project settings and confirmed removal are described below.

## Sidebar and project settings refinement — 2026-09-20

The updated navigation header contains a **thread-title search**, followed by
right-aligned Project filter, Add project and New thread icons. There is no
PROJECTS heading. Search and project selection compose and filter both Open and
Closed lists; neither changes the active conversation. Filters are local to each
client, persist with its view, and never consume prompt text.

The project picker retains name/path search and shows a project badge and name,
with a distinct settings gear at the right. Hover/focus help supplies the full
name/path. Tab visits each row and gear; Enter activates the focused target.
Selecting a filter shows its badge in the header. Automatic badges use first and
last non-space name graphemes (TUI → TI), safely bounded to two terminal cells,
and a stable name-derived accent. Explicit name, symbol and color overrides are
server-owned project settings. Rich-color badges use coordinated background/ink;
limited-color and plain-icon fallbacks remain usable. Project-supplied image
assets are a future opt-in extension, not implemented image loading.

Project settings selects Project in the category sidebar for that named project;
its form replaces the main workspace while Back stays at bottom-left. The form
exposes only Name, Icon and Remove project. Color customization is nested inside
Icon; no separate Color section or server-folder field is shown. Project scope
offers Project, General and Keybindings, with breadcrumb
`Settings / <category> / <project name>`. All projects remains a navigation filter
and never becomes a settings project scope.

Project General holds the workspace default, inheriting the app preference unless
overridden, and shows its effective value. Use app default removes the override.
Changes use revisioned `project.update` and affect future thread creation, never
existing checkouts, drafts or shell directories. Project Keybindings states that
app bindings are inherited and project overrides are unavailable. Appearance and
restart continuation are absent from project scope.
See [settings](settings.md) for scope, capabilities and the worktree limitation.

The user explicitly selected **Remove project and its threads after confirmation**.
The confirmation identifies the project and thread count, defaults to Cancel and
states that files on disk remain. Accepted removal permanently removes Open and
Closed threads, history, saved views, captured context and owned terminal records.
It never deletes the checkout or Git worktrees. Active/queued work, pending
requests or active children block removal until explicitly resolved. Compare the
project revision at execution, including thread-membership changes, so a stale
confirmation cannot cover newly created threads. Removing and adding the same
folder produces a new project identity. Old command retries cannot recreate it.

**Closed** stays above the bottom-left app-settings gear. Collapsed, show
`Closed (N)` with a separator and down indicator; expanded, omit the count and
show the up indicator. Expansion uses available height while retaining one open
card (or its empty state). Open and Closed have independent bounded scrolling.
Use modest bold title-case text. Keep the app's Close/Reopen terminology;
T3's Settle/Unsettle terminology is not adopted.

## Checkout and branch context

Below the composer controls, show the server-authoritative checkout path and branch context for the selected draft project or existing thread. Read `GET /v1/workspace?project_id=…` for a draft or `?thread_id=…` for an existing thread; refresh on selection and explicit refresh. Keep non-Git directories, detached HEAD, fixture paths and unavailable metadata distinct. This read-only context must not initialize Git, change branches, fetch, stage or otherwise mutate the checkout. A thread lookup uses that thread's actual checkout, not a navigation filter's project. Older servers without `workspace-info` show unavailable context until the user upgrades/restarts them.

## App-action row

The bottom-left app-action row has a horizontal separator above it, with the
settings gear below and the Closed shelf immediately above. Use the existing
spacer row so thread viewports and controls stay fixed. Keep the separator inert
and provide a plain `-` fallback; the row can accommodate future app actions
without adding a Usage action now.
