# Sidebar and settings implementation checkpoint

Completed slice, 2026-09-20. Preserve all existing staged/uncommitted work; no Git pull/push.

## Latest accepted user requests

- Follow the newly updated local T3 Code sidebar, inspected at
  `/Users/muschterm/Developer/git/github.com/pingdotgg/t3code`, HEAD
  `1ba471a37cd6b0f18820795f4505206f4723a0e3` (clean checkout).
- Thread search field; at its right, left-to-right: project filter (folder or
  colored project badge), Add project (folder+), New thread (square/pen). Remove
  PROJECTS heading. Prefer restrained bold title-case labels.
- Project badge uses first and last project-name letters (TUI → TI) and a stable
  coordinated background/text palette. Allow project icon/name customization.
  Reusable consumers may eventually opt into a project-supplied icon; never load
  arbitrary assets during rendering or imply image support without evidence.
- Project picker lists names with right-aligned per-project settings gear.
  Project settings edit name/icon and remove project. The latest scope correction
  moves the workspace override to General for the named project.
- Bottom-left app settings gear; Theme moves there. LATEST CORRECTION: settings sidebar is category
  navigation; configuration fills the main area, hiding all workspace panes and
  composer. Back bottom-left restores workspace state. Groups: General first,
  appearance/theme, Keybindings, About/build version.
- General app workspace default: current checkout by default, optional worktree;
  project override inherits app default unless explicitly changed.
- Continue threads after restart: OFF by default; opt-in for supported selected
  server environments. Older servers need update first. Never misrepresent saved
  settings as enforced. Provider/runtime feature gaps remain explicit.
- IMPORTANT latest correction: retain Open/Closed terminology for this app.
  Do NOT rename it to Settled. Closed is pinned at bottom, collapsed count shown;
  expanded count hidden, rule/separator and up/down indicator, bounded list using
  available space. Preserve explicit Close/Reopen and confirmed permanent Delete.

## Progress

The [project/path checkpoint](project-path-checkpoint.md) adds an always-shown
New thread destination picker, folder typeahead from app General's starting
directory, and inline `@` file completion with real server capture at Send.
Its new capabilities require a user-controlled backend restart.

The subsequent [draft/composer checkpoint](draft-thread-checkpoint.md) adds
first-Send creation, active-only settings locking, closed-thread composition,
checkout context, modal dismissal/dimming and the app-action shelf separator.
Unlike the earlier presentation-only refinements, its server capabilities require
a user-controlled server restart.

### Targeted scope correction complete — 2026-09-20

- Project entry shows only Project, General and Keybindings, bound to one project.
  App entry retains General, Appearance, Keybindings and About, with no
  “All projects” settings scope. Navigation's All projects filter is unchanged.
- Project contains Name, Icon and Remove only; existing badge color customization
  stays inside Icon. The workspace override moves to project General with explicit
  inheritance. Breadcrumbs identify the project regardless of active thread/filter.
- Preserve the accepted server-wide restart setting in app General. Project
  Keybindings explicitly reports unavailable overrides; remapping remains outside
  this fix. No persistence or recovery contract change is required.
- Scope, revision binding, narrow geometry and restoration pass Go checks;
  isolated-home OS PTY passes 26 checks; dark/light renderer captures reviewed.
  Full `GOCACHE=/tmp/tui-go-build make check`, Python syntax, local links and diff
  whitespace pass. User server/data remain intact. Binary rebuilt; relaunch TUI
  only. No server contract/schema changes or new dependencies.
- [Scope correction evidence](../research/go-project-settings-scope-2026-09-20.md).

### Prompt height refinement — 2026-09-20

- User requested a two-line default typing area, comparable to the current
  Codex/T3 composer. Interpret this as two editable rows inside the existing
  outline, excluding borders and the settings/actions row.
- Keep content-driven growth, the eight-row upper bound, scrolling and short-window
  priority for requests/actions. Shared single-line inputs and question answers
  retain their existing sizing. The prompt's end-of-buffer styling now fills the
  second empty row before Bubbles v2.2.1 adds viewport padding; both rows keep the
  same interior background.
- `GOCACHE=/tmp/tui-go-build make check` passes (format, vet, race tests, build).
  Existing layout/composer captures were regenerated with
  `TUI_GO_CAPTURE_DIR=/tmp/tui-two-line-prompt`; renderer review passed at 160×50,
  47×22 and 40×22 in dark/light examples. These are renderer checks, not fresh
  OS-PTY or native terminal/device verification. `git diff --check` passes.
  Binary rebuilt; relaunch the TUI only. User server/data and other edits preserved.

### Earlier sidebar implementation

- Read current project/server/workspace docs and inspected relevant T3 files.
- User confirmed Remove project and its threads after confirmation; disk files stay.
- Backend settings/removal/recovery, new sidebar and settings UI, focused tests
  implemented. No new dependencies. Shared design, AGENTS and ADR 0010 updated.
- Final make check passes (format/vet/race/build) with isolated loopback permission.
- Navigation OS PTY: 18 checks; compact OS PTY: 42 checks; corrected settings OS
  PTY: 18 checks. Renderer captures reviewed in dark/light and 47×22. Python syntax,
  local documentation links and diff whitespace pass.
- Evidence: [sidebar review](../research/go-sidebar-settings-2026-09-20.md).
- Settings takeover tests pass for pane/host/terminal/draft/scroll preservation,
  narrow categories/forms, and hidden composer input protection.
- Backend review fixed stale confirmation ABA for removed/re-added paths and
  continuation at a completed-turn/queued-turn boundary. Prior tasks remain
  implemented and uncommitted; all unrelated work preserved.
- User reports 4% remaining weekly Codex usage and will apply their own reset.
  No reset credit authorized or consumed. Resume from these files if interrupted.

## Remaining integration work

Real worktree provisioning, ACP/provider recovery, live terminal sessions,
project image assets and keybinding remapping remain unavailable. Workspace
preferences are persisted but unsupported Worktree creation fails visibly.
Continuation is proven only for eligible Demo fixture work. No real user server
was stopped or reconfigured, and no commits/pulls/pushes were performed in this
slice. Use the build/run instructions in [Go slice](../design/go-slice.md).

## Tooling

From apps/go: `GOCACHE=/tmp/tui-go-build make check`.
Existing PTY harnesses: scripts/pty_navigation.py, pty_small_screen.py, pty_smoke.py.
Use isolated temporary homes. Loopback tests may need execution escalation.
Native iPhone/SSH/Ghostty/iTerm2 visual behavior is not verified by OS PTY checks.
