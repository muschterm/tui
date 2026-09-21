# Go project settings scope correction — 2026-09-20

The Project category now contains Name, Icon and confirmed Remove project only.
Existing badge color customization is inside Icon. Execution overrides belong to
the corresponding category for one named project: General currently provides the
workspace default, its effective inherited value and Use app default reset.
Project scope offers Project, General and Keybindings. App settings retains
General, Appearance, Keybindings and About, including server-wide restart
continuation. Project Keybindings explicitly reports unavailable overrides.

Breadcrumbs use `Settings / General / <project name>` for project configuration
and `Settings / General` for app configuration. There is no All projects settings
scope. Narrow form headings also identify the project when the breadcrumb clips.
Scope follows the project entry, independently of the active thread and navigation
filter. Existing takeover, Back, attention and workspace restoration remain.

This changes presentation and input routing only; existing revisioned
`project.update` / `settings.update` commands, persistence and recovery semantics
are unchanged. No new dependency, schema migration or architectural decision was
needed. See [the accepted behavior](../design/settings.md).

## Local T3 inspection

Inspected `/Users/muschterm/Developer/git/github.com/pingdotgg/t3code` read-only.
`apps/web/src/components/settings/SettingsBreadcrumb.tsx` separates category and
scope, but includes All projects. `apps/web/src/routes/settings.tsx` hides scope
for device-local Appearance. `useScopedSettings.ts` / `scopedSettings.ts` resolve
eligible project overrides and reset them to inheritance. T3 Keybindings writes
environment bindings (`KeybindingsSettings.tsx`, `packages/contracts/src/server.ts`)
without project identity; a project breadcrumb does not establish project-local
remapping. This correction follows the user's narrower category/scope requirements.

## Executed validation

Host: macOS 27.0 (26A428), arm64. The OS-PTY harness launches `/bin/bash` with
`TERM=xterm-256color`, using 160×50 and 47×22 grids and the rebuilt uncommitted
working tree. No native terminal emulator or remote connection participates.

From `apps/go`:

```sh
GOCACHE=/tmp/tui-go-build make check
python3 scripts/pty_sidebar_settings.py --artifacts /tmp/tui-project-settings-scope-pty
TUI_GO_CAPTURE_DIR=/tmp/tui-project-scope-captures GOCACHE=/tmp/tui-go-build go test ./internal/tui -run TestSidebarSettingsCaptures
```

- Formatting, vet, race tests and build passed after the final code change.
- Focused Go regressions cover pointer/keyboard scope routing, app-action exclusion,
  revision-bound project commands, retained identity fields, inheritance display,
  app/project separation, nested icon color, Unicode/long names and 40/47/160-column
  geometry. Existing takeover tests still cover drafts, panes and session associations.
- [OS-PTY report](go-project-settings-scope-captures/settings-pty-report.json):
  **26 checks passed**, including real isolated-server persistence, override reset,
  no app-setting mutation from project General, rename, icon/color, 47×22 category
  navigation, Back and confirmed removal preserving a checkout file.
- The sandboxed temporary server could not start; the OS-PTY harness passed with
  loopback execution permission. Its application home and checkout were under a
  disposable temporary directory. The user's server and data were untouched.
- Python harness syntax, local documentation link targets and `git diff --check`
  passed. Existing staged/uncommitted work was preserved; no commit/pull/push.

Go View captures were rendered with JetBrains Mono Nerd Font Mono and inspected
in dark/light themes. These are deterministic renderer artifacts, not screenshots
from a real terminal:

- [App General](go-project-settings-scope-captures/160x30-lightfalse-sidebar-general.png)
- [Project General](go-project-settings-scope-captures/160x30-lightfalse-sidebar-project-general.png)
- [Project identity/removal](go-project-settings-scope-captures/160x30-lighttrue-sidebar-project.png)
- [47×22 project General](go-project-settings-scope-captures/47x22-lightfalse-sidebar-project-general.png)
- [47×22 project Keybindings](go-project-settings-scope-captures/47x22-lighttrue-sidebar-project-keybindings.png)

## Limits and applying the change

The OS-PTY checks do not establish Ghostty, iTerm2, phone, SSH or tmux behavior.
Keybinding remapping/project overrides, worktree creation and real provider/editor/
embedded-terminal integrations remain unavailable. Restart continuation remains
verified only for eligible Demo fixture execution.

The executable is rebuilt. Detach and relaunch the TUI to load the correction.
No server restart is needed when the existing server already supports app/project
settings; the UI continues to explain unavailable settings on older servers.
