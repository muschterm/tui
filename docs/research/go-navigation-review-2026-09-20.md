# Go project and thread navigation review — 2026-09-20

The user's latest requests add a T3-inspired project selector and folder-plus
entry point, Close/Reopen, a Closed group replacing Recents, hover check/trash
actions and an ellipsis with permanent Delete. The local T3 checkout was inspected
at `72c44a847c0a76f33b0d21f47548125b7032ec35`; [source findings](t3-code-design.md#project-selection-and-thread-lifecycle-inspection--2026-09-20)
remain separate from runtime evidence.

## Working behavior

Projects are server-owned records for existing readable directories. The picker
searches names and paths; All projects or a specific project filters only the
current client's navigation. The center retains its active thread and displays
its actual project in the breadcrumb. Add validates and canonicalizes a server
folder, selects the project and preserves input on error. It creates no folder,
Git changes or agent work. New thread creates an empty idle Demo thread.

Open thread hover/focus reveals a checkmark; Closed reveals trash. The label and
ellipsis have separate hit regions. Close preserves drafts/history/surfaces and
requires inactive work in this prototype. Reopen restores them without sending
or resuming. Delete has one Cancel-first confirmation, then removes the thread's
live state, owned terminal fixtures, command payloads and all saved thread views.
It preserves project files and other threads. Empty state survives restart.

The server rejects stale lifecycle revisions and projects stale view writes onto
live threads. Minimal creation/deletion fingerprints prevent retry resurrection.
A client reconciles only a verified deletion of its last acknowledged view,
preserving concurrent local edits to surviving drafts. Other edits made by a
process sharing its client name remain a save conflict. [ADR 0007](../adr/0007-thread-deletion-and-view-projection.md)
records the ownership and retry tradeoff. No dependency or toolchain changed.

## Executed validation

macOS darwin/arm64, pinned Go 1.27.1. All servers and PTYs used isolated temporary
homes; the existing user server and home were not stopped or modified.

| Check | Result |
| --- | --- |
| `GOCACHE=/tmp/tui-go-build make check` | PASS: gofmt check, vet, all race tests, native build |
| Lifecycle/storage tests | PASS: Close/reopen preservation and revisions, busy/closed-work rejection, scoped atomic deletion, stale views, rollback, creation/deletion retries, empty restart |
| Project tests | PASS: canonical and symlink deduplication, home expansion, invalid paths, file preservation, migration, capacity, empty thread/settings |
| UI/writer tests | PASS: local filters, input ownership, keyboard/pointer equivalence, errors retaining folder text, separate hover hit regions, Cancel-first confirmation, empty/closed focus, deleted-state reconciliation, surviving drafts, deletion-only CAS retry and real conflict rejection |
| `python3 scripts/pty_navigation.py --artifacts /tmp/tui-navigation-pty` | PASS: 18 OS-PTY checks, including both clients saving after Delete and read-only SQLite inspection |
| `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-navigation-regression-pty-2` | PASS: 28 existing OS-PTY checks for keyboard, mouse, resizing, suspend/resume, composer/questions, detach and server recovery |
| `python3 scripts/scroll_benchmark.py --events 500 --output /private/tmp/tui-navigation-scroll` | PASS: 50.3 ms to alternate-screen exit, 116.3 ms total TUI-child CPU; theme saved and server survived detach |
| Visual review | Dark/light, project picker, Closed hover, Delete confirmation and 48×22; [captures and reports](go-navigation-captures/README.md) |

The original PTY regression used a hardcoded command-menu height and clicked the
wrong row after new commands were appended. The harness now locates the rendered
theme control; its rerun passed. The new harness also corrected initial-save
timing, macOS canonical `/private/tmp` paths and omitted false `Closed` fields.
These were harness corrections. UI review separately fixed hidden project input
after closing its modal, mouse-submit error retention, active project breadcrumbs
and F6 traversal when no composer is visible.

The wheel result is one local sample, not a performance guarantee. Raster captures
do not establish native terminal compatibility. Ghostty, iTerm2, SSH and tmux were
not exercised in this review.

## Run and remaining scope

From the repository root, rebuild and restart an older server to load the added
`project-management` and `thread-lifecycle` capabilities:

```sh
cd apps/go
make build
./bin/tui-go server stop
./bin/tui-go --client desk
```

Use the same `TUI_GO_HOME` for both commands if overridden. Restart preserves
unfinished work and requires explicit Resume. The selector/folder-plus/New thread
controls are at the top of navigation; F4 exposes the same actions and thread
options. Close currently waits for active work, queues and pending requests to
be resolved. Permanent Delete removes live records, not historical SQLite journals,
external backups or earlier recovery exports, and never removes checkout files.

Project folder browsing, project deletion/configuration and real agent selection
remain future work. The folder input addresses the server filesystem. Demo
threads and terminal records are synthetic; real provider cancellation and process
cleanup require integration before equivalent deletion guarantees can be claimed.
A rare pending creation whose receipt was deleted can still produce a recoverable
view conflict because the client does not replay commands to probe their status.

The next implementation slice remains a pinned ACP adapter through initialization,
capability/settings confirmation, real prompt/activity, cancellation and reconnect
without resubmission. Collaborative editing, embedded terminal emulation, rich
clipboard/media intake, asynchronous provider questions and full child history
retain their separately recorded integration gaps.

## Follow-up: navigation status circles

The next user review replaces navigation thread icons with pulsing blue working,
yellow/orange attention, red error and solid green finished/idle circles. Errors
win over attention, then working. Pending asynchronous requests therefore remain
yellow even while other work continues. Reported thread/child failures are red;
old transcript error text does not establish a current failure. Unknown and
disconnected state stays neutral. Only finished/idle, closable rows replace the
circle with a Close check on hover/focus; Closed rows retain their trash hover.
Thread options use Nerd Fonts v3.4.0 cod-kebab_vertical (U+EB10), verified against
the previously retrieved glyph registry, with a colon fallback.

Visible background rows reuse the existing 120 ms animation clock. Visibility,
project filtering and scroll bounds stop offscreen rows from keeping it alive;
menu overlays and disconnect stop animation. Frames do not dirty saved views.
Tests cover state precedence, colors in both themes, hover action routing,
vertical options, background pulses, bounds and persistence invariance.

Validation: `GOCACHE=/tmp/tui-go-build make check` passed formatting, vet, all race
tests and the native build. `python3 scripts/pty_navigation.py --artifacts
/private/tmp/tui-thread-status-pty` passed 18 native PTY checks. The 500-event
wheel benchmark passed with 57.4 ms to alternate-screen exit and 139.9 ms total
TUI-child CPU. These are isolated synthetic runs and one timing sample. Dark/light
status frames and dim/bright pulse endpoints were visually inspected;
[captures and reports](go-thread-status-captures/README.md) record provenance.
No native Ghostty/iTerm2, remote terminal path or real provider API error was
exercised. The binary is built; relaunch the TUI to load these presentation changes.
The existing server can remain running.

A subsequent spacing review found the title's truncation mark touched the
vertical menu glyph. The title now reserves a trailing blank cell and the glyph
is centered in its existing three-cell right-edge button, leaving two blank
cells between them. Hit regions remain unchanged. `GOCACHE=/tmp/tui-go-build
make check` passed formatting, vet, race tests and native build after this edit;
the dark/light status captures were regenerated and visually reviewed.
