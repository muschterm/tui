# Go conversation, activity and composer review — 2026-09-19

The user's second interactive review changes message headers, working/completed
indicators, empty-host maximization and composer presentation. The accepted
behavior is recorded in [activity](../design/activity.md),
[layout](../design/layout.md), and [configuration](../design/thread-configuration.md).
These changes do not connect a real agent, clipboard, editor or terminal emulator.

## Implementation and decisions

- Ordinary user/agent messages omit repeated role/name headers. Their existing
  right-inset user tint and left-aligned replies distinguish them. Tool/MCP titles
  and states remain visible.
- A shared tinted strip contains Thinking/Waiting, Plan progress, and one Agents
  summary with a working count. Blue circles breathe on a single 120 ms clock;
  successful completion is solid green. Failures/interruption/waiting remain
  distinct. Animation stops while disconnected, obscured by a menu, below the
  minimum viewport, or idle; animation never writes persisted state.
- Completed Agents/Plan summaries have a hover/focus close slot. Dismissal is
  local to the named client's thread view, preserves history and drafts, and is
  checked against the current summary identity. New work reappears. The current
  fixture protocol has no run ID: identity uses accepted user activity plus group
  identities/states, with reset on observed new active work. A real integration
  needs source run identities before claiming arbitrary history/replay coverage.
- Stop uses the existing `thread.interrupt` contract and remains available during
  a running or waiting turn. Resume gating and queue safety are unchanged. Send
  stays at the right edge, preceded by optional Stop, paperclip and usage. The
  demo now displays `Demo`, `Reference`, `Medium`, and `Simulated`; raw IDs are
  preserved in submitted commands. Provider/model formatting examples are not
  advertised availability. Ultra and Ultracode remain distinct supplied values.
- Usage has a compact proportional-gauge renderer, but the live fixture supplies
  no measurements. Its neutral track/`—` and `Cost —` mean unavailable, never zero.
  The usage inspector retains billing/context/cost availability details. Selected
  and Running settings appear separately only during a real mismatch in the
  fixture state, and wrap using the same measurement and painting layout.
- The maximize control exists only for a visible right host. An empty chooser
  maximizes/restores while preserving composer access. A short empty host opens
  its chooser as a scrollable modal rather than painting over the footer.
- Synthetic turns now finish after eight server ticks, with children completing
  halfway and the plan finishing at the end. New idle prompts start a new bounded
  fixture turn; queued items retain captured settings/content. Completion stays
  idle across restart; unfinished work still needs explicit Resume. Each later
  turn creates a fresh synthetic child. The inspector retains 32 children, with
  older full child detail archived under the existing bounded activity history.
  History truncation reserves its own notice slot instead of overwriting a
  retained prompt's captured settings or attachments.
  This is fixture lifecycle evidence, not agent execution.

No dependencies or protocol version changed. The existing T3 Code reference at
`72c44a847c0a76f33b0d21f47548125b7032ec35` was inspected for its
[`ComposerPrimaryActions`](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/ComposerPrimaryActions.tsx)
circle controls. Nerd Fonts v3.4.0's `fa-circle_arrow_up`, `fa-stop_circle`, and
`fa-paperclip` supplement the existing Codicons; no artwork/font is bundled.

## Validation

Run from `apps/go` on macOS darwin/arm64 with pinned Go 1.27.1:

| Check | Result |
| --- | --- |
| `GOCACHE=/tmp/tui-go-build make check` | PASS: gofmt, vet, race tests and native build |
| Activity and controls regressions | PASS: headers/alignment, aggregate access, completed dismissal and restoration, new-work reappearance, bounded pulse clock, disconnected/finished state, exact footer geometry and visible/empty-host maximize |
| Settings and usage presentation | PASS: original protocol values preserved, distinct effort names, supplied speed/context, unknown versus zero gauge, proportional fill and clamping, selected/effective mismatch, recovery controls |
| Server fixture lifecycle | PASS: finite progress/completion, idle sends, preserved prompt captures through dispatch and history trimming, command retry deduplication, Stop/Resume continuity, old unbounded tick migration and idle restart |
| `python3 scripts/pty_smoke.py --artifacts /tmp/tui-activity-pty` | PASS: 22 native PTY checks covering input, mouse, resize, suspend/resume, multiple clients, submission, detach and restart/Resume |
| `python3 scripts/scroll_benchmark.py --events 500 --output /tmp/tui-activity-scroll` | PASS: 36.1 ms to alternate-screen exit during a live animation, 96.6 ms complete TUI-child CPU; theme saved and server survived |
| Visual review | Dark/light working, completed hover, maximized empty chooser and 48×22 captures; see [capture provenance](go-activity-captures/README.md) |

PTY runs use isolated temporary homes and servers. The user's existing server and
stored views were not stopped or altered. Timings are one local sample. These
checks do not establish native Ghostty/iTerm2, SSH/tmux, real provider reasoning
phases, real cancellation, graphics, or usage telemetry. Static raster captures
use their own font metrics and do not reproduce the user's terminal exactly.

To load the UI, build and reopen the TUI. To load the new demo lifecycle as well,
restart the existing background server with the new binary, then explicitly
Resume unfinished demo work. A new temporary `TUI_GO_HOME` gives an independent
fresh demo without touching an existing session.
