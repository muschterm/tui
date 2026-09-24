# Go embedded terminal libraries — 2026-09-24

Evidence for [ADR 0019](../adr/0019-embedded-terminal-sessions.md) and
`apps/go/internal/term` (phase 1). Checks ran on Linux (Omarchy, kernel 7.2,
Go 1.27.1). macOS was compiled (`GOOS=darwin go vet`) but not executed.

## Versions

| Module | Version | Source |
| --- | --- | --- |
| `github.com/charmbracelet/x/vt` | `v0.0.0-20260924144451-d676b019604b` | `go list -m ...@latest` via the Go module proxy on 2026-09-24; newer than the probed `c615ff2f7805` (2026-09-13) and builds/passes against the module's existing `ultraviolet` and `x/ansi` pins without upgrading them. Adds indirect `x/exp/ordered v0.1.0`. |
| `github.com/creack/pty` | `v1.1.24` | Latest tag in `go list -m -versions`. No transitive dependencies. |

x/vt is pre-v1 and unversioned; upgrades need the package tests rerun.

## Source findings (pinned versions)

- `vt.Emulator` writes query replies (DA1/DA2, DSR/CPR, DECRQM, OSC colour
  queries, in-band resize) synchronously to an internal `io.Pipe` inside
  `Write`. Parsing blocks until someone reads `Emulator.Read`. The package runs a
  dedicated drainer that never blocks (bounded queue of 64, excess dropped) and a
  separate writer to the PTY.
- `Emulator.Close` sets an unsynchronized `closed` field that `Read` checks, so
  it races with a concurrent reader. The package instead closes the pipe via
  `InputPipe().(*io.PipeWriter)`.
- Scrollback belongs to the main screen only; `Scrollback.Push` drops the oldest
  line with `slices.Delete(lines, 0, 1)` (O(cap) per line).
- OSC 0/2 titles containing `;` are ignored (`bytes.Split` expects two parts).
- The parser data buffer is 4 MiB per emulator.
- Wide-character continuation cells have width 0; `SGR 3x/9x` produce
  `ansi.BasicColor`, `38;5` `ansi.IndexedColor`, `38;2` an RGB colour.
- creack/pty's master fd stays in blocking mode (`Fd()` forces it). A write to a
  child that is not reading then blocks an OS thread indefinitely and
  `File.Close` cannot interrupt it — observed as a hung `Close` in
  `TestUnreadRepliesDoNotBlock` before the fix. The package duplicates the
  master into a non-blocking, poller-registered `os.File` and resizes with
  `TIOCSWINSZ` through `SyscallConn`, never calling `Fd()` again.

## Verified behaviour (real PTY, `/bin/sh`)

`go test -race -count=2 ./internal/term/` passes: output, cursor, wide
characters/emoji, SGR colours and attributes, resize seen by `stty size`,
alternate-screen restore, capped ordered scrollback, title sanitizing, CPR reply
read by the child, child exit code, SIGHUP close, SIGKILL escalation for a child
ignoring HUP, `yes` flood with bounded scrollback and prompt close, unread query
flood, environment filtering, directory/shell validation and goroutine cleanup.

## Not established

Interactive shells (`bash`/`zsh`/`fish -i`), full-screen programs (vim, htop,
less), mouse/bracketed paste/keyboard protocol negotiation, SSH/tmux, macOS
execution and long-running memory profiles.
