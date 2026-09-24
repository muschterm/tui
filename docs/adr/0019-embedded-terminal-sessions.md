---
status: accepted (user decisions 2026-09-24; phase-2 wire details to be specified)
---

# Server-owned embedded terminal sessions with server-side emulation

The Terminal surface and centre-bottom panel need real shells. Several clients
may observe one session, sessions must outlive TUI exit, and child output is
untrusted: forwarding raw bytes would let a program drive the outer terminal
(title, clipboard, graphics, mode changes).

## Decision

- **Libraries:** `github.com/creack/pty` v1.1.24 owns the PTY and process
  start; `github.com/charmbracelet/x/vt` (pinned pseudo-version
  `v0.0.0-20260924144451-d676b019604b`) emulates. See the
  [research note](../research/go-terminal-2026-09-24.md).
- **Shell:** the server user's `$SHELL`, interactive non-login (`-i`), started in
  the requested directory; `/bin/sh` when `$SHELL` is unset, not absolute or not
  executable. The child leads a new session/process group with the PTY as its
  controlling terminal. `TERM=xterm-256color`, `COLORTERM=truecolor`; outer
  terminal/multiplexer identity (`TMUX`, `KITTY_*`, `TERM_PROGRAM`, …) is removed.
- **Emulation on the server:** one reader per session parses output into a
  bounded grid. Clients receive cell snapshots (grapheme text, width, colour,
  attributes, cursor, sanitized title), never child escape bytes. This is the
  untrusted-output boundary. Emulator query replies are written back to the PTY
  through a bounded queue that drops excess replies instead of blocking parsing.
- **Scrollback:** 10,000 main-screen lines per terminal, memory only. Sessions do
  not survive server stop or restart.
- **Close:** SIGHUP to the process group, SIGKILL after 2 s; accepted close and
  confirmed exit (reaped, reader stopped) are distinct outcomes.
- **Control (phase 2):** the opener controls input and resize; any client can
  explicitly Take control. Transfer is immediate, the previous controller becomes
  an observer and is notified, and input/resize from a non-controller is
  rejected as stale.
- **Input is ephemeral (phase 2):** keystrokes travel on a separate channel and
  are never journaled as durable application commands, so reconnect or recovery
  cannot replay them.

Phase 1 is the self-contained `apps/go/internal/term` package. The WebSocket
frames, controller identity/revision, snapshot/diff rate and TUI rendering are
to be specified with phase 2.

## Consequences

- Every client sees the same parsed state; late joiners and reconnects need only
  a snapshot plus history, not a byte replay.
- Emulation fidelity is bounded by pre-v1 x/vt (see research note limits);
  unsupported sequences are dropped, not forwarded.
- Memory per terminal is grid plus capped scrollback. x/vt's scrollback trims by
  shifting its slice, so heavy output costs CPU proportional to the cap.
- Closing is bounded for children that ignore SIGHUP. Descendants moved to other
  process groups (shell job control) receive the shell's own hangup handling but
  are not force-killed.
- Server restart ends all shells; this is intentional.

## Alternatives considered

- **Client-side emulation of raw bytes:** each client would need its own
  emulator and a byte replay to catch up, observers could diverge, and raw
  untrusted bytes would cross the protocol. Rejected.
- **Persisting transcripts/scrollback:** restart cannot restore a live process,
  and stored output could contain secrets. Rejected for now.
- **Login shell:** would rerun login profiles for every terminal; not requested.
