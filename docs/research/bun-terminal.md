# Bun terminal research

Source-checked: **2026-09-19**. Documentation research only; no runtime, subprocess, embedded terminal, or UI integration was tested.

The requested bottom-center terminal can use Bun's native PTY API for process I/O. The React/Ink presentation choice is researched separately; this document does not establish that stack or a terminal-emulation dependency.

## Documented API

`Bun.Terminal` and the `terminal` option for `Bun.spawn()` arrived in **Bun 1.3.5**, released December 17, 2025, initially for Linux/macOS. That establishes introduction, not a recommended production pin. [Release announcement](https://bun.com/blog/bun-v1.3.5).

The current [Spawn guide](https://bun.com/docs/runtime/child-process#terminal-pty-support) documents `openpty()` on Linux/macOS and Windows ConPTY. Some [Spawn API reference](https://bun.com/reference/bun/Spawn) text still says POSIX-only. Record the exact runtime tested before promising Windows support; Windows remains best effort for this project.

Use `Bun.spawn(command, { terminal: options })`, or construct `new Bun.Terminal(options)` and pass it as `terminal`. The child receives terminal-backed stdin/stdout/stderr; `proc.stdin`, `proc.stdout` and `proc.stderr` are `null`. Access `proc.terminal` instead. Initial `cols`/`rows` default to 80/24. `name` defaults to `xterm-256color`, but `TERM` must be set separately through spawn's `env`. [Spawn guide](https://bun.com/docs/runtime/child-process#terminal-pty-support).

`data(terminal, data)` receives `Uint8Array<ArrayBuffer>` bytes. `exit(terminal, exitCode, signal)` reports PTY EOF/read error; its code is PTY lifecycle status, **not** the subprocess exit code. Use `proc.exited` or spawn's `onExit` for process completion. [Terminal options](https://bun.com/reference/bun/TerminalOptions).

| API | Documented meaning |
| --- | --- |
| `write(string | BufferSource)` | Accepts all bytes; buffers anything not immediately flushed |
| `drain` callback | Signals buffered data has flushed |
| `resize(cols, rows)` | Updates terminal dimensions |
| `setRawMode(boolean)` | Controls terminal input processing |
| `ref()` / `unref()` | Controls whether the terminal keeps the event loop alive |
| `close()` / `closed` | Closes the terminal / reports closure |
| `await using` | Disposes a standalone terminal automatically |

Do not resend input based on `write()`'s return value: it reports accepted bytes. A standalone terminal can be reused across sequential spawns; its lifetime is separate from each subprocess. [Terminal class](https://bun.com/reference/bun/Terminal), [reuse documentation](https://bun.com/docs/runtime/child-process#reusable-terminal).

## Embedded-pane boundary

**Architectural inference:** a PTY transports terminal bytes; it does not supply the ADE's VT parser, screen grid, scrollback, selection or pane compositor. The guide's direct `process.stdout.write(data)` example hands child output to the outer terminal. That is unsuitable inside a multi-pane UI: child cursor movement, erases and alternate-screen sequences could affect the whole application.

**Proposal:** feed bytes into a persistent terminal emulator, then render its bounded grid inside the pane. Preserve parser state across chunks, including split UTF-8 and control sequences. Do not turn each callback into an independently decoded text row. Choose a documented emulation profile and advertise only capabilities it implements; `xterm-256color` is not proof of implementation.

The frontend owns the outer terminal, focus and global shortcuts. Under the subsequently accepted server architecture, the background server owns the child PTY/process and persistent emulation state; each terminal has one explicit input/resize controller while other clients observe. Route input to the child only when appropriate; translate mouse coordinates into pane coordinates and honor child mouse modes. Keep child alternate-screen buffers, cursor visibility, scrolling regions, bracketed paste and keyboard negotiation within the emulator. Terminal query replies belong back on the child's PTY, rather than leaking to the outer screen. Selection/copy versus child mouse capture needs an explicit interaction rule.

Use the controlling pane’s content dimensions, excluding borders, at creation and after its resizing; observing clients cannot resize the shared PTY. Define hidden/zero-size behavior before spawning. Keep PTY closure, child exit, retained output and pane removal as distinct state transitions; closing a pane must not silently imply an untested process-tree cleanup guarantee. Explicit server stop gracefully cancels and stops owned processes; hiding a pane or detaching a frontend leaves the server-owned process alive. Bound output/history retention and define controller transfer.

## ACP separation and acceptance

ACP agents/adapters use clean newline-delimited JSON-RPC stdin/stdout, with logs on stderr. **Do not attach this interactive PTY to ACP protocol stdio.** An ACP-requested command terminal may share process infrastructure only through a deliberate separate execution contract. [ACP transport](https://agentclientprotocol.com/protocol/v1/transports).

All checks are **NOT RUN**. Before accepting an implementation, test shell editing, full-screen child applications, rapid resize, alternate-screen restoration, cursor/query handling, mouse capture, paste, split Unicode chunks, large output, input duplication, process exit versus PTY EOF, and pane teardown. Confirm other panes remain unchanged by child escape sequences. Exercise macOS iTerm2/Ghostty and the chosen Linux terminals, directly and through SSH/tmux, recording Bun, emulator and UI versions.
