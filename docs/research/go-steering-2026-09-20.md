# Queued-message steering — 2026-09-20

The user requested a Steer button for queued messages and explicitly required
this in the overall design. The [shared behavior](../design/activity.md#steering-a-queued-message),
[glossary](../../CONTEXT.md), [server contract](../design/server.md#scheduling-and-unattended-requests)
and [ADR 0009](../adr/0009-turn-bound-steering.md) distinguish steering into an
active turn from ordinary queuing, Stop, Resume and beginning another turn.

## Implemented in the Go fixture

The queue card and full queue menu expose **Steer**. Pointer and Tab/Enter use the
same command. Narrow cards retain the text label and compact Edit/Remove to the
existing compose/trash glyphs (plain `e`/`x` fallback), with descriptive help.
Steering never reads the ordinary composer draft. A locally edited queue item
requires Save or Cancel first. Unavailable actions show a coalesced five-second
notice above hover help without moving focus. Unconfirmed delivery keeps a
persistent Retry status and the saved command identity.

`queue.steer` carries the target prompt, observed queue revision and expected
turn identity. The server checks the `fixture-steering` capability, fixture
agent, live turn, Resume gate and matching captured/effective settings. It then
appends one user input with its full `Prompt` capture, removes that queue item,
increments the queue revision and records `fixture-delivered` in the same SQLite
transaction. Tick, active-turn identity, effective settings, plan, children and
pending requests remain unchanged. Stable turn identity also prevents a steered
message from accidentally becoming the fixture's completion identity.

Retries reconcile through existing durable command receipts, including after
restart; a new competing command cannot steer the same queued revision twice.
Startup derives missing identities in older fixture snapshots and retains the
explicit Resume gate. Existing live servers need a deliberate restart of the
rebuilt binary before they advertise this new capability. No user server was
restarted during validation.

## Validation and scope

- `GOCACHE=/tmp/tui-go-build make check` in `apps/go` passed formatting, vet,
  all race tests and build. Loopback integration tests required sandbox
  escalation; the initial restricted attempt failed to bind temporary test
  ports, not an application assertion.
- Server tests verify captured settings/attachments, waiting requests, unchanged
  execution, concurrent clients, stale queue/turn/dispatch races, completed turns,
  unsupported capability, settings mismatch, storage failure, legacy identity
  recovery, Resume gating and accepted-command retry after a real server restart.
- TUI tests verify mouse/keyboard targets, narrow geometry, stable menu actions
  across turn/queue changes, preserved drafts, transient unavailable notices and
  durable unconfirmed delivery with same-identity Retry.
- Fresh [dark](go-steering-captures/160x50-lightfalse-queue.png),
  [light](go-steering-captures/160x50-lighttrue-queue.png),
  [narrow dark](go-steering-captures/48x22-lightfalse-queue.png) and
  [narrow light](go-steering-captures/48x22-lighttrue-queue.png) Go render captures
  retain compressed ANSI/SVG sources. These use JetBrainsMono Nerd Font Mono
  and are deterministic renders, not GUI screenshots.

`python3 scripts/pty_steering.py --artifacts /private/tmp/tui-steering-pty-final`
passed all 11 checks; [retained report](go-steering-captures/pty-report.json).
The local PTY check uses two TUI clients and a temporary home. Its harness was
corrected to avoid matching a message preview instead of the Steer button and
to bound old scroll-region sequences when replaying output after resize. These
were harness failures, not evidence of failed agent delivery.

This remains **synthetic delivery**. No Codex, Claude, other agent or ACP adapter
was invoked. No external provider acknowledgment, cancellation, permission,
mid-turn settings update or uncertain-delivery reservation was verified. The
fixture's atomic SQLite operation does not establish exactly-once behavior across
an external process boundary. Live integration must verify steering separately
from asynchronous questions, retain uncertain delivery against ordinary dispatch,
and negotiate support before enabling the feature. Ghostty/iTerm2 GUI and
Omarchy/SSH/tmux paths were not rerun for this change.

## Run

From the repository root, after detaching the previous TUI:

```sh
./apps/go/bin/tui-go server stop
./apps/go/bin/tui-go
```

The explicit server restart is needed only to replace an older running binary;
unfinished saved work waits for **F4 → Resume selected thread**. Select a queued
message's **Steer** button while its Demo turn is active. The next integration
slice must prove the shared steering contract against each selected ACP adapter.
