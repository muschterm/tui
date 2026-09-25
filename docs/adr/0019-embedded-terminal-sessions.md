---
status: accepted (user decisions 2026-09-24; phase-2 wire details 2026-09-24)
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
- **Close:** SIGHUP to the shell's process group and to every other process
  group in its session (background jobs), SIGKILL to whatever remains after
  2 s; accepted close and confirmed exit (shell reaped, session empty or its
  survivors counted, reader stopped) are distinct outcomes. See Phase 3.
- **Control (phase 2):** the opener controls input and resize; any client can
  explicitly Take control. Transfer is immediate, the previous controller becomes
  an observer and is notified, and input/resize from a non-controller is
  rejected as stale.
- **Input is ephemeral (phase 2):** keystrokes travel on a separate channel and
  are never journaled as durable application commands, so reconnect or recovery
  cannot replay them.

Phase 1 is the self-contained `apps/go/internal/term` package. Phase 2 wires it
into the server, protocol and client as below; TUI rendering follows.

## Phase 2 wire details

Types and full field documentation live in `apps/go/internal/protocol/terminal.go`;
the Go client API is `apps/go/internal/client/terminal.go`.

- **Durable lifecycle** uses the journaled, deduplicated `POST /v1/command`:
  `terminal.open` (`ThreadID`, `ClientID`, optional `TerminalSize{Cols,Rows}`,
  default 80×24) returns the terminal ID as `Receipt.TargetID`; `terminal.close`
  (`ThreadID`, `TargetID`); `terminal.take-control` (`ThreadID`, `TargetID`,
  `ClientID`). The shell starts outside the engine lock with the command
  identity reserved, so a retried or concurrent `terminal.open` with the same ID
  never starts a second process. At most 64 terminals are live; the newest 64
  ended records are retained.
- **Working directory:** the thread checkout, which must be an existing absolute
  directory (`terminal_unavailable` otherwise). Fixture checkouts
  (`fixture://…`) have no directory, so their terminals start in the server
  user's home; the record's `Dir` shows the actual directory.
- **Record** (`protocol.Terminal`, additive): `State` `running` → `closing` →
  `ended`, or `closing` → `close_uncertain` (exit not confirmed within 5 s,
  `Error` set) → `ended` once reaped. A shell exiting by itself goes straight to
  `ended`. `EndReason` is `exited`, `closed`, `server_stopped` or
  `server_restarted`; `Exit{code,signal,killed}` is set on confirmed exit.
  `Controller` plus `ControlGen` (starting at 1, incremented per transfer)
  identify the controller. `Dir`, `Shell`, `Cols`, `Rows` and `Title` are
  informational; size and title are coalesced into the snapshot at most every
  250 ms (then the normal streamed-state flush) so resize drags do not produce a
  revision storm. Thread delete and project removal drop the records and close
  the sessions in the background. Server stop moves live records to `closing`,
  closes every session (bounded 5 s, SIGKILL fallback) and records
  `server_stopped`; a session whose exit is not confirmed in time is recorded as
  `server_stopped_unconfirmed` and the shutdown outcome fails with stage
  `terminals`. Records found live at start become `ended` with
  `server_restarted`. A zero `TerminalSize` dimension means the default.
- **Ephemeral stream:** `GET /v1/terminals/{id}/stream?client_id=…`, a WebSocket
  with the same bearer token, `X-TUI-Protocol: 1` and browser-origin rejection
  as `/v1/events`; unknown IDs are 404 before upgrade. JSON text messages carry
  a `type`. Server events: `screen` (complete grid: `seq`, `cols`, `rows`,
  `cursor{x,y,visible}`, sanitized `title`, `alt`, and per row a list of style
  runs `{t, w, c, fg, bg, a}`), `control{controller,gen}`, `ended{exit,reason}`
  followed by a normal close, `rejected{reason,for}` and
  `scrollback{from,total,lines,more,degraded}`. Client requests: `input{gen, data | data_b64}`
  (≤ 64 KiB), `resize{gen,cols,rows}` and `scrollback{from,n}` (n ≤ 500;
  negative `from` requests the newest lines). Runs never mix cell widths;
  continuation cells are omitted, and a run is either one grapheme per rune or
  (`c`) a single multi-rune cluster, so clients place graphemes without their
  own segmentation. Colours are `""` (default), `"0"`–`"255"` (palette) or
  `"#rrggbb"`.
- **Rate and back-pressure:** one encoder per session builds at most ~30 frames
  per second; each connection keeps only the latest frame, so slow readers skip
  frames and never block the PTY reader or other streams. A write blocked for
  10 s drops that connection. At most 16 streams per terminal and 256 in total
  (HTTP 429 `capacity`). The Go client also coalesces undelivered screens to
  the latest.
- **Control enforcement:** input and resize are applied only when the stream's
  `client_id` is the controller, `gen` equals the current `ControlGen` and the
  terminal is running; otherwise the server answers `rejected` with
  `not_controller`, `stale_generation`, `ended` (only when the session has
  ended or is closing), `busy` (the child did not read input within 5 s;
  `written` bytes were delivered, the rest dropped), `failed`, `too_large` or
  `invalid`.
  Every connected stream receives `control` on transfer. Any authenticated
  client may observe and read scrollback.
- **Input is never persisted or logged:** it exists only in stream messages and
  the PTY write.

## Phase 3: semantic input and hardening (2026-09-24)

- **Keys and paste are semantic requests** (`key{gen,key{code,text,mods}}`,
  `paste{gen,text}`) encoded on the server by our own encoder over x/vt's key
  tables, with DECCKM, keypad and bracketed-paste modes tracked through the
  emulator's mode callbacks. x/vt's own `SendKey`/`Paste` write into the same
  lossy, bounded reply pipe that drops excess emulator replies, so they cannot
  carry user input reliably. Raw `input` remains for byte-exact cases.
- **Terminal focus:** Ctrl+] leaves terminal input in the TUI; every other key
  goes to the controlled shell. Mouse events are not forwarded to the child.
- **Cluster cap:** one cell's grapheme cluster is at most 128 bytes. The
  session segments child output with the emulator's own grapheme segmenter
  (`ansi.FirstGraphemeCluster`) and, only for a cluster longer than the cap,
  keeps its first whole runes up to 128 bytes and drops the rest of that
  cluster before it reaches the grid or scrollback. When a read fills the
  buffer, the trailing cluster is held (at most 20 ms) and completed with the
  next read, so clusters are not split at read boundaries. Differential tests
  show identical cells and cursor to the unlimited emulator for a corpus of
  Thai, Devanagari, NFD Hangul, emoji ZWJ/skin-tone/kiss/tag/flag sequences,
  combining Latin and text interleaved with escape sequences, whole or split
  at any byte; floods of spacing marks, prepended letters, halfwidth sound
  marks, combining marks, variation selectors, regional indicators, tags, ZWJ
  chains, conjoining jamo and invalid bytes stay within the cap in grid and
  scrollback. Output cells are truncated the same way. Screens and scrollback
  answers are each kept within 8 MiB: screens degrade (clusters to one rune,
  then styles dropped, `degraded: true`); scrollback returns fewer lines
  (`more: true`, keeping the newest for a negative `from`) and degrades a line
  only if it alone exceeds the budget. Clusters longer than 128 bytes render
  truncated.
- **Busy input:** a child that stops reading makes input time out as `busy`
  with the delivered byte count, never `ended`.
- **Session-wide close:** on Linux the session is enumerated from
  `/proc/<pid>/stat` (session field). While any member lives the kernel does
  not reuse the session ID, so enumeration after the shell is reaped still
  finds only its processes. `Exit` reports `descendants`, `descendants_killed`
  and `descendants_remaining`. Other Unix platforms signal only the shell's
  process group and report `descendants_unknown`. A shell that exits by itself
  leaves its jobs running (as `nohup` would) and only counts them.

## Consequences

- Every client sees the same parsed state; late joiners and reconnects need only
  a snapshot plus history, not a byte replay.
- Emulation fidelity is bounded by pre-v1 x/vt (see research note limits);
  unsupported sequences are dropped, not forwarded.
- Memory per terminal is grid plus capped scrollback. x/vt's scrollback trims by
  shifting its slice, so heavy output costs CPU proportional to the cap.
- Closing is bounded for children that ignore SIGHUP. On Linux, background jobs
  in other process groups of the shell's session are hung up and then killed;
  processes that leave the session (their own `setsid`) are outside this
  guarantee, and other platforms report the outcome as unknown.
- Server restart ends all shells; this is intentional.

## Alternatives considered

- **Client-side emulation of raw bytes:** each client would need its own
  emulator and a byte replay to catch up, observers could diverge, and raw
  untrusted bytes would cross the protocol. Rejected.
- **Persisting transcripts/scrollback:** restart cannot restore a live process,
  and stored output could contain secrets. Rejected for now.
- **Login shell:** would rerun login profiles for every terminal; not requested.
