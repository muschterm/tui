# Go first-slice validation — 2026-09-19

The working tree now contains a runnable native Go server and TUI in `apps/go`. This report covers the first fixture-driven slice, not completion of the three reference applications or their live integrations. Existing staged and uncommitted repository work was preserved; no staging, commits, pulls or pushes were performed.

## Build and correctness evidence

Environment: macOS 27.0 (26A428), darwin/arm64, Apple M4 Max, Go 1.27.1. The module and `.go-version` record the toolchain; Make targets explicitly select `GOTOOLCHAIN=go1.27.1`. Dependency versions were resolved through the Go module proxy, inspected in their downloaded sources, and verified against module sums. The restricted environment required `GOCACHE=/tmp/tui-go-build` and approved loopback access for integration tests; neither is an application requirement outside that environment.

From `apps/go`:

| Command | Result and scope |
| --- | --- |
| `make check` | PASS: no `gofmt` differences, `go vet ./...`, `go test -race ./...`, native binary build |
| `go mod verify` | PASS: all downloaded modules verified |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/tui-go-linux-amd64 ./cmd/tui-go` | PASS: cross-build only; no Linux execution claim |
| `python3 scripts/pty_smoke.py --artifacts /tmp/tui-go-final-pty` | PASS: 20 assertions; see [PTY evidence](go-terminal-checks-2026-09-19.md) and the retained JSON report; OS PTY input/output checks, not GUI screenshots |
| `TUI_GO_CAPTURE_DIR=/tmp/tui-go-captures go test ./internal/tui -run TestLayoutKeepsComposerAndControls -count=1` | PASS: 20 combinations of size, theme and maximization; fixed controls remain inside the viewport |
| `go test ./internal/tui -run . -bench BenchmarkViewWide -benchtime=1s` | PASS; one wide-view microbenchmark below |
| `git diff --check` | PASS; local links checked in 45 Markdown files |

The tests cover command identity and changed-payload rejection, queue dispatch/revision races, immutable submitted settings/context, competing request answers, restart Resume gates, initial WebSocket snapshot handoff, bounded slow-client disconnection, authentication, same-home locking, and separate client views. SQLite checks cover stale/concurrent view writes, backup before legacy migration, future-schema rejection without changing the original database, and confirmed shutdown outcomes.

UI regression tests cover hidden/restored panes and singleton surfaces, thread drafts/reading positions, request changes during editing, retained original composer drafts while editing a queued prompt, typing after an accepted edit, duplicate/late command receipts, menu pointer activation, crowded footers, hostile terminal escapes and render purity. Unicode tests cover combining accents, ZWJ emoji, skin tones and flags for movement, selection, replacement, deletion, word deletion, newline joins and bounded bracketed paste.

## Practical and visual checks

[The PTY harness](../../apps/go/scripts/pty_smoke.py) runs actual app processes in isolated application homes and checks persisted state after injected keyboard/mouse protocol input. It exercises two clients, real shell job-control suspend/resume, resize/collapse/restore, alternate-screen/mouse cleanup, detach without stopping the server, and restart requiring Resume. Fixture terminal hide and close are distinct server transitions; no real child shell is implied.

[Dark, light and maximized captures](go-captures/README.md) were generated from actual Go `View` output and inspected against the approved direction. They retain text roles, richer colors, fixed pane controls and persistent composer/request/settings/usage areas. They are rasterized deterministic render output, not native terminal screenshots. Layout tests also cover 80×30, 60×24 and 48×22; smaller dimensions show a resize message and retain the draft. Compact presentation uses one queue row plus a queue menu and a one-row composer.

The Mac was locked during attempted native-app access. Live Ghostty 1.3.1 and iTerm2 3.5.14 GUI review was therefore **NOT RUN**. Actual clipboard delivery, GUI mouse selection and complex-grapheme hit positioning remain unverified. The PTY harness does not prove tmux/SSH transport, kitty keyboard negotiation or graphics. Required Linux terminal runtime checks and Windows best-effort checks remain outstanding.

The wide-view benchmark (160×50, Agents inspector open, default fixtures) measured **3.08 ms/op**, **1,883,312 B/op** and **10,082 allocations/op**, with 391 iterations and GOMAXPROCS 16. This is a single local rendering microbenchmark, not input-to-display latency, memory residency or an approved performance budget. End-to-end latency, streaming load and long-running storage growth still need measurement.

## Safety and scope limits

The server lifecycle, private application home, SQLite transactions, authenticated loopback transport and catch-up are real. The agent, plans, tools/MCP, child histories, question modes, approvals, terminal sessions and context sources are synthetic. Files and Git report integration unavailability; usage reports missing telemetry. No workspace file capture/write, Git mutation, live provider job, real embedded terminal or inferred quota/pricing data is hidden behind these fixtures.

Each Send records fixture context content at that moment and captures settings. Uncertain commands retain their identity and payload, including in the persisted view, for explicit Retry. Queue conflicts retain the edit; the command menu can rebase its queue revision after review. A final view-save failure exports a private JSON recovery file beneath the application home's `recovery/` directory and reports its path. That file is a recovery artifact, not authoritative server state; automatic recovery import is not yet implemented. Separate names should be used for simultaneous persistent clients; CAS rejects a competing write to the same named view.

The pinned Bubbles textarea uses rune-based internals. A narrow adapter preserves grapheme boundaries during editing; upstream pointer/wrapping geometry can still be approximate for complex emoji. Native asynchronous Ctrl+V and rune transposition are disabled; use the terminal's bracketed-paste shortcut. The terminal clipboard receives explicit copy requests, but delivery is not acknowledged. Shortcut remapping, broad editor selection semantics, rich image placement, production retention/backup tooling and remote attach configuration remain later work.

See [the feasibility report](go-feasibility-2026-09-19.md) for executed collaborative-text/undo, merge, VT emulator, Bun/Ink and Bun PTY probes. Live ACP configuration enforcement, complete child-history replay and continued-work asynchronous answer delivery remain integration gates.

## Next implementation slice

Add one pinned ACP adapter as a read-only activity vertical slice: initialization/capability negotiation, one real prompt, settings acknowledgment, streaming/history normalization, cancellation and reconnect without resubmission. Feed the resulting recorded streams through the existing inspectors and compare them with a credentialed live check. Keep unsupported async questions and unavailable child history explicit. Collaborative files and real PTYs follow their separate feasibility and safety gates; do not expand the Git surface into mutations before buffer coordination exists.
