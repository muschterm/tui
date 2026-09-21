# Go creation drafts, composer and modal review

Recorded 2026-09-20. This review covers the current uncommitted Go implementation,
following the [project settings scope correction](go-project-settings-scope-2026-09-20.md).
Existing work was preserved; no commit, pull or push was performed. User-owned
server processes and application data were not changed by runtime checks.

## Implemented behavior

- New thread opens/restores a client-local project draft. Explicit supported model
  selection completes Demo defaults; valid first Send creates the authoritative
  thread and accepts its captured prompt in one retryable transaction. Invalid
  selections, unsupported worktree defaults and failed writes create no orphan.
- Model/settings controls are read-only during active work, including waiting,
  and while the initial Send awaits reconciliation. Idle selection remains
  editable. Ordinary active Send captures confirmed running settings; a queued
  text edit retains that item's recorded settings. Text typed after Send survives.
- Selecting Closed opens the conversation/composer without changing lifecycle.
  Its banner offers a separate Reopen; Send atomically reopens and accepts input.
- The prompt defaults to two editable rows. A separate row below its controls
  shows checkout name/kind and observed branch, with path details and explicit
  refresh. Server plumbing reads distinguish detached, unborn, non-Git, fixture
  and unavailable states; rendering performs no Git/process I/O.
- Centered dialogs dismiss on outside click, consuming that click without firing
  underlying actions. The background dims within color capabilities. This is not
  pixel blur; monochrome retains modal boundaries without dimming.
- A horizontal line separates the pinned Closed area from the bottom app-action
  shelf. Settings remains the only implemented shelf action. Compact layouts
  preserve both Open and Closed title/actions when full cards cannot fit.

[ADR 0011](../adr/0011-first-send-thread-creation.md) records atomic creation and
closed-thread submission. Relevant behavior is in [configuration](../design/thread-configuration.md),
[layout](../design/layout.md), [threads](../design/threads.md) and [components](../design/components.md).

## Local reference

Inspected `/Users/muschterm/Developer/git/github.com/pingdotgg/t3code` at
`1ba471a37cd6b0f18820795f4505206f4723a0e3`, without modifying it. Relevant source:
`useHandleNewThread.ts` local draft handling, `ChatView.tsx` initial Send and
closed-banner handling, server `ws.ts` creation rollback, orchestration
`decider.ts` lifecycle transition, and `BranchToolbar.tsx` checkout context.
The supplied screenshot informed banner/context placement. This application's
Open/Closed terminology and active-only settings lock follow the user's decisions,
not T3's labels or provider restrictions.

## Executed checks

`GOCACHE=/tmp/tui-go-build make check` passed from `apps/go`: gofmt check, vet,
all race-enabled tests and binary build. Server tests needed permission to bind
isolated localhost listeners after the sandbox denied bind; this was an environment
restriction, not a test assertion failure. No dependencies were added.

Regression coverage includes receipt/snapshot ordering, retry/relaunch without
resubmission, deletion tombstones, preserving newer target/source drafts, one
unreconciled creation at a time, active/idle controls, queued configuration,
closed lifecycle races, stale checkout observations, dark/light narrow geometry,
modal click-through prevention, color fallbacks and bounded ANSI composition.
The server metadata tests use temporary Git fixtures for branch/detached/unborn,
worktree and non-Git states; these are not interactive Git-client verification.

Isolated OS-PTY harnesses passed **96 checks** using temporary `TUI_GO_HOME`
directories. Each harness stops its own server in cleanup:

| Harness | Checks | Report |
| --- | ---: | --- |
| Navigation / first Send / closed composer | 27 | [JSON](go-draft-composer-captures/navigation-pty-report.json) |
| Sidebar / project settings / removal | 27 | [JSON](go-draft-composer-captures/settings-pty-report.json) |
| Small-screen interaction at 40×22 and 47×22 | 42 | [JSON](go-draft-composer-captures/small-screen-pty-report.json) |

Harness updates account for local creation drafts, truncated titles, input focus
and saved-view debounce. Python syntax and Git diff whitespace checks passed.

Renderer tests exercised draft/closed/modal states in both themes at 40, 47, 80
and 160 columns, checking two-row prompt geometry and bounded controls. Selected
ANSI frames were rendered with JetBrains Mono Nerd Font Mono and visually reviewed.
These are deterministic Go View captures, **not terminal screenshots**:

- [Wide dimmed model dialog and sidebar divider](go-draft-composer-captures/160x30-lightfalse-modal.png)
- [Wide light Closed composer](go-draft-composer-captures/160x30-lighttrue-closed.png)
- [40-column initial draft](go-draft-composer-captures/40x22-lightfalse-draft.png)
- [47-column Closed composer](go-draft-composer-captures/47x22-lightfalse-closed.png)

## Limits and loading the changes

No fresh Ghostty, iTerm2, SSH, tmux or physical-phone verification was performed.
Codex/Claude model discovery and execution remain implementation step 4; only
Demo / Reference is connected. Real worktree creation, collaborative editing and
embedded terminal integrations remain incomplete. Keybinding overrides remain
explicitly unavailable. Branch observations refresh on selection or request;
they are not a live subscription.

The binary is rebuilt. Detach and relaunch the TUI to load the presentation.
An already-running old server must also be explicitly stopped and restarted to
load `thread-start`, `closed-thread-send` and `workspace-info`. When ready, from
`apps/go`, run `./bin/tui-go server stop`, then `./bin/tui-go --client desk` (or
use the existing client name). Stop interrupts owned work; preserved unfinished
work requires Resume by default. No server restart was performed by this task.
