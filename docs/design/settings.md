# App and project settings

Status: accepted sidebar refinement, 2026-09-20; Go implementation uses the current
server's settings capabilities. Other reference apps remain pending.

## Navigation and scope

A gear at the bottom-left opens the settings workspace. The left sidebar is
category navigation: General first, Appearance, Keybindings and About. This is app
scope, with breadcrumb `Settings / <category>`. A project's picker gear enters
only that named project, selects Project, and offers Project, General and
Keybindings. Its breadcrumb is `Settings / <category> / <project name>`; changing
category preserves that project identity. All projects is a navigation filter,
never a settings project scope. Project scope has no Appearance or About category,
and F8 cannot change the app theme while project settings is visible. The
selected category's configuration fills the main area, replacing the conversation,
right surfaces, bottom panel and composer. It does not render forms inside the
left category sidebar. This is the user's corrected layout decision.

Back stays at the bottom-left and exits settings; a top close control and Escape
also return to the workspace. Preserve the original pane visibility, maximize
state, surfaces, terminal associations, reading positions and drafts. Hidden
conversation controls cannot receive typing or Send shortcuts. Background work
continues. At widths below 60 cells, the settings hamburger/F2 switches between
category navigation and the selected form; choosing a category opens its form.
Back remains in the navigation view and the close control stays available on the
form. Compact form headings repeat the project name so a clipped breadcrumb
does not hide the scope. Settings content has independent bounded scrolling. The persistent
attention bell remains available; activating it explicitly returns to the
workspace attention list, while incoming events never change settings focus.

App General settings belong to the connected **server environment**, persist in its
SQLite state and are shared by clients attached to it. Each command compares the
settings revision; stale changes are rejected instead of replacing another
client's edits. A future environment selector must apply them only to explicitly
selected environments. This slice has one environment per connection and no
multi-environment selection UI. Theme remains a per-client appearance preference,
as does the **Symbols** choice added on 2026-09-22: Appearance offers Nerd Font
(default) or ASCII control glyphs, saved in the client view, for terminals whose
font renders the patched glyphs as boxes or misaligned cells. An unsaved choice
follows the `TUI_GO_ICONS` environment variable; once saved, the setting wins and
the variable is only a default. Project scope cannot change it.
App Keybindings documents current controls; rebinding is not implemented. Project
Keybindings explains that bindings are inherited from the app and project
overrides are unavailable; it does not display the global binding list as a
project override or offer editable remapping. About shows
a real module version or development revision/modified state from Go build info,
never an invented release number.

## Workspace default

The Project category contains only Name, Icon and Remove project. Badge color
customization belongs inside the Icon menu, with no separate Color section or
server-folder field. Execution preferences belong in General for the selected
scope.

The app defaults to Current checkout. Project General shows the inherited or
explicit workspace default and its effective value. Use app default clears the
override, so later app-default changes update the effective project choice. A
project can explicitly choose Current checkout or Worktree. These writes use the
existing revisioned `project.update` contract and never edit the app default. Resolve the effective choice when creating a thread;
never move an existing thread or reinterpret captured prompt settings. The Go
slice persists both choices but **cannot provision worktrees yet**. Menus and
settings explain the limitation, and creation with an effective Worktree default
fails explicitly. A project override of Current checkout remains usable. No Git
mutation is performed by saving preferences. Worktree start-state, lifecycle and
cleanup remain governed by the [workspace contract](workspaces.md).

## Project starting folder

App General exposes **Project starting folder**, a server-owned, revisioned,
persisted setting. Its default is the connected server user's home (`~`), not the
client's current directory. Add project starts browsing there while retaining
absolute paths, `~/` expansion and directory navigation. Saving validates an
existing readable directory; failures retain the entered value. Saving never
creates a directory, registers a project, changes an existing project or moves
thread checkouts. Other clients observe the saved preference through server
state. This setting is app-only and does not add a Project category field.

The path picker requires `path-completion`; an older server needs an explicit
update/restart before this control is available. Rebuilding or relaunching the
client alone cannot upgrade the running backend. Preserve drafts and show that
guidance instead of silently treating an unavailable preference as saved.

## Continue threads after restart

This setting is server/app-only and appears only in app General, never project
General. Off by default. When off, preserve interrupted execution/queues and require
explicit Resume after an update, crash or machine restart. Enabling it is explicit
permission for this environment to recover **eligible** previously active work;
changing the setting does not immediately resume an already stopped thread.
Attaching/reconnecting to a live server only catches up regardless of preference.

Eligibility must be verified by the execution integration. It cannot follow from
an agent name, shared protocol or a persisted settings label. The Go implementation
advertises support for its Demo fixture only: recover known active fixture turns
or their captured queued continuation, never manually stopped work, and never
answer a question or approve a tool. Pending requests remain gated and require
request revision revalidation. Ordinary Resume remains available. Server stop
still gracefully cancels owned work and exits; opted-in eligible fixture work can
continue on a later server start. No real provider side-effect replay, surviving
PTY or portable ACP history guarantee is implied.

Old servers lacking `app-settings`, `project-settings` or
`restart-continuation` capabilities show an update-required explanation and cannot
accept those changes. Update older servers first. Unknown future providers must
remain ineligible until their recovery contract is implemented and tested.

See [project settings/removal](projects.md#sidebar-and-project-settings-refinement--2026-09-20)
and [the persistence/recovery decision](../adr/0010-environment-settings-and-recovery.md).
