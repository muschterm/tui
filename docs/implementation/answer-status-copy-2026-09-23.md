# Answer status and clipboard interaction — 2026-09-23

The user reported persistent “Delivery unconfirmed” on answered questions and
an ineffective Command+C shortcut in the TUI.

The server records accepted native answers before returning the response to the
bridge. `finishQuestion` records `acp-unconfirmed`: neither that return nor a
successful pipe write proves provider acceptance. The affected `test-tui-go`
thread uses Codex; its saved records were inspected read-only.

Codex's native request ledger observes `serverRequest/resolved`, but that event
also covers request cleanup. [Official App Server documentation](https://learn.chatgpt.com/docs/app-server#toolrequestuserinput)
describes resolution after an answer and cleanup at turn start, completion or
interruption. A locally completed response write cannot alone disambiguate a
cleanup event already queued on the other pipe. The ledger also records error
responses as answered for transport deduplication; that transport flag must not
be interpreted as successful answer delivery.

No reliable provider-acceptance receipt is exposed by this integration. The UI
therefore keeps the Submitted state and explicitly describes confirmation as
unavailable, using a neutral tone for this ordinary integration limitation.
Actual uncertain, cancelled and undeliverable records keep their distinct
statuses. Confirmed resolution alone remains Answered. No saved answer is
resubmitted or retroactively declared successful.

The copy handler previously accepted only Ctrl+C and Ctrl+Shift+C. Bubble Tea
v2.0.9 identifies Command as the Super modifier, so forwarded Super+C can use
the same selected-text copy route. Terminal applications may consume Command+C
before the TUI receives it; supporting a forwarded key does not override their
bindings. Ctrl+C with a TUI selection remains the fallback, and Ctrl+C without
selection still detaches. No terminal configuration is changed.

The user confirmed Ghostty on macOS and also requires Omarchy/Linux. The
installed Ghostty default binding is `super+c=copy_to_clipboard:mixed`, as read
with `ghostty +list-keybinds --default`. That binding consumes the key for
Ghostty's selection, which is separate from the TUI's highlighted selection.
[Ghostty documents Shift selection](https://ghostty.org/docs/vt/csi/xtshiftescape)
as the native-selection escape from application mouse reporting. Plain TUI
selection followed by Ctrl+C is independent of that native selection.

[Omarchy's unified clipboard](https://learn.omacom.io/2/the-omarchy-manual/105/universal-clipboard)
uses Super+C; the application accepts that key when forwarded, alongside
Ctrl+C and Ctrl+Shift+C. The selection hint is platform-neutral. The pinned
clipboard library uses `pbcopy` on macOS and supports `wl-copy`/`wl-paste` on
Wayland, with X11 utility fallbacks. SSH copying continues to use OSC 52.

The user additionally requested application context menus: Copy for selected
text, and Paste plus selection-dependent Copy in the prompt. Paste is an
explicit clipboard read into the existing cursor/selection, never an implicit
Send. Async clipboard results must remain bound to the original composer and
editing state so navigation or typing cannot redirect a pending paste.

The context menu also has Shift+F10/Menu keyboard access. Clipboard reads are
text-only, bounded to 1 MiB, and inserted through the composer's existing
selection and character-limit handling. SSH Paste offers the terminal's paste
shortcut instead of reading the remote host clipboard.

Validation:

- `make check` passed: vet, staticcheck, all race-enabled tests and the Go build.
- Linux amd64 cross-build passed with `CGO_ENABLED=0`.
- The new `apps/go/scripts/pty_clipboard.py` passed all 13 macOS OS-PTY checks,
  using isolated clipboard wrappers and scratch text, without accessing the
  system clipboard. The [retained report](../research/clipboard-context-captures/pty-report.json)
  covers explicit menu actions, exact Copy, cursor insertion and selection replacement.
- Visual review covered [selected transcript Copy](../research/clipboard-context-captures/160x50-lightfalse-context-transcript.png),
  [selected prompt Copy/Paste in light mode](../research/clipboard-context-captures/160x50-lighttrue-context-prompt-selected.png)
  and [compact prompt Copy/Paste](../research/clipboard-context-captures/60x32-lightfalse-context-prompt-compact.png).
  These are rendered ANSI captures, not native terminal screenshots.
- `git diff --check` passed.

Native GUI clipboard behavior remains unverified: computer-use tooling refused
access to Ghostty for safety reasons, and no Omarchy desktop is available in
this macOS environment. The Linux clipboard helper path is implemented but has
not been exercised on an Omarchy desktop.

## Follow-up: delivery evidence

Records answered before a restart were later shown as **delivery uncertain**,
because restart recovery downgraded completed-turn answers. That downgrade is
now limited to turns in flight, and affected records are repaired to
unconfirmed on load. Built-in bridges now provide a pinned delivery receipt.
A blocking answer with that receipt, followed by a normal end of the same turn,
is recorded as `resolved`/`acp-turn-confirmed`. See
[question delivery](question-delivery-2026-09-23.md).
