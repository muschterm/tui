# Go wheel-input backlog — 2026-09-19

The user reported that scrolling in Ghostty made the entire TUI appear frozen,
followed by delayed redraws, clicks and keys. Ctrl+Q eventually detached after a
delay. A local OS-PTY reproduction demonstrates the same input backlog; this is
not a claim that the fix has been interactively rechecked in Ghostty.

## Findings and correction

The pinned Bubble Tea 2.0.9 event loop calls `Update` and then `View` synchronously
for each input message. Its output frame limit does not limit application `View`
construction. Our mouse handler also constructed a complete styled frame for
hit testing. Wheel and hover bursts therefore rebuilt the screen twice per event,
including expensive ANSI row composition, before later keys could be processed.
The versioned source was inspected locally in `tea.go` (`eventLoop` and `render`)
and `tty.go` (`StreamEvents`). No dependency changes were needed.

Input routing now uses the same layout/control path without painting styled rows
or textarea contents. The runtime wrapper processes every input/state transition
immediately and schedules at most one pending redraw timer, combining visual
updates at up to 60 frames per second. `View` returns that prepared frame between
redraws. Clicks, keys, commands, saves and snapshots are not dropped or reordered
by this wrapper. It does not start a continuous redraw loop when idle.

A separate bug allowed wheel offsets to grow past the end of the content. Only
the visible offset was clamped, so reversing the wheel appeared inert until the
hidden overscroll was repaid. The saved view retained this debt, and keyboard End
introduced the same problem with an arbitrary large offset. Transcript, inspector
and request offsets now use actual bounds; End uses those bounds too. Further
scrolling at an edge does not dirty persistence. Restored offsets are normalized
after the real terminal dimensions arrive, preserving valid reading positions
that differ from the constructor's provisional viewport.

The save path was also reviewed: it already coalesces dirty state, permits one
background save at a time, and has no view-save-to-snapshot feedback loop. It was
not changed. Final detach still saves the latest underlying state, including
inputs processed since the most recent display frame. Server/schema/command
contracts remain unchanged.

## Reproduction and measurements

Environment: macOS, darwin/arm64, Apple M4 Max, Go 1.27.1. The harness starts its
own server and TUI under a private temporary home, injects 500 SGR wheel-down
events followed by F8 and Ctrl+Q at 160×50, and measures from the first input write
to alternate-screen exit. It also checks the persisted theme and server survival.
CPU measurements cover the complete TUI child lifetime, excluding the server.

| Measurement | Before | Final fix |
| --- | ---: | ---: |
| Wheel burst to alternate-screen exit | 1.937 s | 0.0251 s |
| Complete TUI child CPU | 2.268 s | 0.0797 s |
| F8 persisted and server survived detach | PASS | PASS |

These are individual local runs, not latency guarantees or a Ghostty benchmark.
An earlier fixed-build run measured 0.0090 s; scheduling and concurrent checks
affect the result. Raw streams and JSON reports are retained locally in
`/tmp/tui-scroll-baseline` and `/tmp/tui-scroll-final`.

Reproduce from `apps/go`:

```sh
make build
python3 scripts/scroll_benchmark.py --events 500 --output /tmp/tui-scroll-check
```

The input microbenchmark, using longer synthetic transcript/question/inspector
text, measured 63–65 µs per wheel event between rendered frames over three runs.
A complete wide frame still costs approximately 3.2–3.4 ms; the main improvement
is avoiding complete frame construction for every input.

## Regression checks

- `make check`: PASS (formatting, vet, race tests and build).
- Added tests for bounded/reversed wheel input, keyboard End, restored and resized
  bounds, content shrink, provisional versus actual viewport, matching measured
  and painted hit targets, immediate input state and one pending redraw timer.
- `go test ./internal/tui -run '^$' -bench 'Benchmark(ViewWide|WheelBurst)$' -benchmem -count=3`:
  PASS; measurements above.
- `python3 scripts/scroll_benchmark.py --binary bin/tui-go --events 500 --output /tmp/tui-scroll-final`:
  PASS; final measurements above.
- `python3 scripts/pty_smoke.py --artifacts /tmp/tui-scroll-smoke-verified`:
  PASS, 20 assertions including clicks, dragging, suspend/resume, separate drafts,
  detach and restart/Resume. An initial restricted run failed to reach startup;
  the complete run used approved PTY/loopback permissions.
- Native Ghostty interactive retest remains pending. The OS PTY exercises actual
  application input and output, not the GUI compositor or terminal rendering.

All checks use isolated application homes. The user's running server, drafts and
stored views were not used or stopped. Relaunching the TUI picks up the change;
no server restart or storage migration is required.
