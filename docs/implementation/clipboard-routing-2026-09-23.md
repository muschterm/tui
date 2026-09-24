# Clipboard routing follow-up — 2026-09-23

The reported path is Omarchy terminal → SSH to macOS → herdr 0.9.1 → TUI.
The precise failing shortcut and outer terminal version remain unconfirmed.
Do not treat a host-local clipboard write as evidence that the viewing
machine's clipboard changed. No user clipboard contents or terminal
configuration are needed for the investigation.

The current client selects OSC 52 only from SSH environment variables.
A long-lived multiplexer can obscure the viewing client's environment;
the host-local macOS clipboard is then a different destination from Omarchy's.
This process had `HERDR_ENV=1` and `HERDR_PANE_ID` set, with all three SSH
markers unset; only marker presence was inspected. The user also confirmed
that herdr's own clipboard works in this topology.

The corrected route recognizes `HERDR_ENV=1` and sends Copy through OSC 52,
letting herdr choose the attached client's clipboard. Direct local use still
uses the OS helper. Paste inside herdr never reads the potentially remote
host's clipboard; it offers the outer terminal's Paste action instead.

The official herdr v0.9.1 source establishes the complete copy path:

- [`pane/terminal.rs`](https://github.com/herdrdev/herdr/blob/v0.9.1/src/pane/terminal.rs#L1421)
  extracts OSC 52 clipboard writes; [`pane.rs`](https://github.com/herdrdev/herdr/blob/v0.9.1/src/pane.rs#L2428)
  emits `AppEvent::ClipboardWrite`.
- [`server/headless/notifications.rs`](https://github.com/herdrdev/herdr/blob/v0.9.1/src/server/headless/notifications.rs#L347)
  sends `ServerMessage::Clipboard` to the foreground client.
- [`client/clipboard_forwarding.rs`](https://github.com/herdrdev/herdr/blob/v0.9.1/src/client/clipboard_forwarding.rs#L9)
  calls the helper in [`selection.rs`](https://github.com/herdrdev/herdr/blob/v0.9.1/src/selection.rs#L342).
  That helper uses the **client's** SSH environment: SSH clients emit OSC 52
  to stdout; local clients try native clipboard tools such as `pbcopy` first.

Thus both herdr and the TUI can run on macOS while their clipboard requests
reach Omarchy through the SSH-attached herdr client's stdout. Calling `pbcopy`
inside the pane bypasses this routing. With multiple attached herdr clients,
herdr chooses its foreground client; the TUI does not control that policy.
Neither layer acknowledges acceptance by the outer terminal.

The key handler also explicitly rejected Ctrl+V, despite the context menu
already having a guarded asynchronous clipboard reader. Forwarded Ctrl+V,
Ctrl+Shift+V, Super+V and Shift+Insert now use that reader. The widget's
independent paste command stays disabled: insertion must use the app's
grapheme-safe replacement and stale-target/duplicate-result guards.
Terminal-owned paste still arrives as bracketed text and does not read the
host clipboard. A forwarded shortcut over SSH keeps the terminal-paste
fallback instead of reading the remote host clipboard.

Validation:

- `GOCACHE=/tmp/tui-go-build-cache make check` passed formatting, vet,
  staticcheck, all race-enabled tests and build. Local-listener tests ran with
  sandbox permission. The first full run caught a missing dialect in the new
  Claude test fixture; the corrected full run passed.
- Focused Go clipboard/paste/herdr tests passed.
- The OS-PTY clipboard harness passed 23 checks in
  `/tmp/tui-clipboard-routing-final/report.json`, using scratch clipboard
  helpers. It covers native copy/paste, cursor and selection preservation,
  forwarded keyboard paste, OSC 52 output from a simulated herdr pane with no
  SSH markers, no host clipboard calls there, and incoming bracketed paste.
- The exact desktop → SSH → real herdr → TUI round trip has not been exercised
  here. No real clipboard contents or user terminal settings were changed.
