# Go sidebar and settings review — 2026-09-20

Implemented the updated [T3 sidebar direction](t3-code-design.md#updated-sidebarsettings-source-inspection--2026-09-20)
with this app's **Open/Closed** vocabulary. The user's final correction places
settings categories in the left sidebar and replaces the full main workspace
with the selected configuration. Back/close restores prior presentation; the
composer is hidden and cannot receive input while settings are open.

The later [project scope correction](go-project-settings-scope-2026-09-20.md)
separates project General overrides from app settings and narrows the Project
category to identity/removal. Its report supersedes the category contents shown
in the earlier captures below.

## Verified behavior

- Local thread-title search, project filtering, add/new icons, stable colored
  project badges, separate project-picker gears and keyboard access.
- Closed pinned above app settings, collapsed count and separate bounded expanded
  list; trailing Close/Trash remains directly left of the vertical ellipsis.
- Project/category settings, full workspace takeover, compact hamburger/F2
  categories/forms, bounded scrolling, Back/close, and persistent attention bell.
  Hidden workspace drafts, reading positions, pane/maximize state and fixture
  terminal/tab associations survive entry, category changes and exit.
- Server-owned revisioned app/project defaults; name/icon/color overrides;
  capability-gated controls for older servers; stale rename retains its draft.
- Confirmed project removal defaults to Cancel, rejects busy/stale targets,
  transactionally removes its threads/views/payloads, preserves checkout files,
  and prevents deleted creation retries from resurrecting records. Removed and
  re-added folders have distinct project identities, preventing stale confirmation
  authority from applying to a replacement project.
- Continue after restart defaults off. Backend tests exercise real server
  stop/start with opted-in fixture recovery, manual Stop exclusion, pending
  question/approval gating and a completed-turn/queued-turn boundary. Captured
  settings/context stay attached to queued continuation.

## Executed validation

From `apps/go`, `GOCACHE=/tmp/tui-go-build make check` passes: gofmt cleanliness,
`go vet ./...`, `go test -race ./...` and executable build. Loopback integration
checks were run with execution permission after the sandbox rejected binding
`127.0.0.1`. All server homes were temporary and isolated; the user's server was
not stopped or reconfigured. Python harness syntax and `git diff --check` pass.

OS-PTY interaction reports:

- [Navigation](go-sidebar-settings-captures/navigation-pty-report.json): 18 checks,
  including two-client drafts, lifecycle/SQLite cleanup and disk preservation.
- [Small screen](go-sidebar-settings-captures/small-screen-pty-report.json): 42
  checks at 40×22 and 47×22, covering compact columns, questions and restoration.
- [Settings](go-sidebar-settings-captures/settings-pty-report.json): 18 checks,
  including server-persisted settings, unsupported-worktree feedback, rename,
  project symbols, narrow settings and explicit permanent removal.

The settings PTY check exposed error text hidden behind hover help. Settings and
thread-creation failures now use the existing timed notice layer; a regression
asserts that unsupported-worktree feedback remains visible.

Deterministic Go View renders were visually reviewed using JetBrains Mono Nerd
Font Mono. They are renderer artifacts, **not native terminal screenshots**:

- [General, dark](go-sidebar-settings-captures/160x30-lightfalse-sidebar-general.png)
- [Project configuration, light](go-sidebar-settings-captures/160x30-lighttrue-sidebar-project.png)
- [Project picker](go-sidebar-settings-captures/160x30-lightfalse-sidebar-picker.png)
- [Project form at 47×22](go-sidebar-settings-captures/47x22-lightfalse-sidebar-project.png)

The directory retains PNG/SVG and compressed ANSI inputs. Reproduce captures with
`TUI_GO_CAPTURE_DIR=/tmp/tui-sidebar-captures go test ./internal/tui -run TestSidebarSettingsCaptures`
and `scripts/render-capture.py` with the chosen Nerd Font path.

## Limits and next slice

Worktree defaults persist but worktree provisioning is still unavailable; new
thread creation with that effective preference fails visibly instead of using
checkout silently. Keybindings are a reference, not a remapping UI. Project
symbols are bounded font glyphs/monograms, not loaded project image assets.

Server preferences apply to the single connected environment. Demo execution
and terminal records remain fixtures; real ACP/provider recovery, real embedded
shells and complete upstream child history are unverified. The OS PTY does not
establish native Ghostty/iTerm2, phone, SSH or tmux behavior. Full provider and
workspace integration remains the next implementation slice, with negotiated
capabilities and explicit recovery evidence before enabling those options.

See [settings behavior](../design/settings.md) and [ADR 0010](../adr/0010-environment-settings-and-recovery.md).
