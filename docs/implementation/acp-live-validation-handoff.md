# ACP live validation — handoff (2026-09-22)

Written mid-task because the running agent reached its session limit. It records
what was executed live, what was found, what was already changed in the working
tree, and what is left. Nothing here is committed. Paths under the caller's
scratchpad are placeholders for whoever continues.

## Continuation status

**Next work:** [the agent-integration handoff](agent-integration-handoff.md)
records the later user decisions and implementation sequence. Start there rather
than resuming only the live tests below. Existing evidence remains valid within
its recorded versions and scope.

The handoff below is retained as the earlier run's historical record. The
[continuation report](../research/go-acp-2026-09-22.md) records completed merged
checks, additional fixes, live Codex PTY validation and retained captures.
Claude's current account limit blocks the remaining live TUI reply/permission
check. No commits or unrelated-server cleanup were performed.

## Environment used

- macOS Darwin 27.0.0 arm64, `GOTOOLCHAIN=go1.27.1`, `GOCACHE=/tmp/tui-go-build`.
- Node v26.9.0. Pinned adapters resolved from a scratchpad install prepended to
  the server's `PATH`: `claude-agent-acp` 0.80.0 (reported
  `@agentclientprotocol/claude-agent-acp 0.80.0`) and `codex-acp` 1.12.0
  (reported `@agentclientprotocol/codex-acp 1.12.0`).
- Isolated `TUI_GO_HOME` under the scratchpad. The user's real `~/.tui-go` and
  any running personal server were never touched.
- Binary: `apps/go/bin/tui-go`, rebuilt with `make build` before the run.
- Live account: the user's own logged-in `claude` and `codex` CLIs, one small
  authorized prompt per adapter plus a few cancel/permission probes.

## Step 1 — live server check over HTTP (done)

Driven with `curl`/`urllib` against `discovery.json`'s URL and token; the token
was never printed. Commands were posted to `POST /v1/command` with
`X-TUI-Protocol: 1`.

| Check | Outcome |
| --- | --- |
| `GET /v1/snapshot` lists `fixture`, `claude`, `codex` | pass |
| Startup probe moves both ACP agents to `ready` with `Options`/`Fields` | pass, within ~2 s of `server start` |
| `agent.probe` refresh and coalescing | pass — three probes fired together produced **one** adapter child process; `Revision` advanced probing→ready; `ProbedAt` refreshed |
| `project.add` for a fresh `git init` folder | pass |
| `thread.start` (Claude) with the agent's current option values | pass |
| Claude pong turn | pass |
| `thread.interrupt` during a long Claude turn | pass |
| `thread.resume` without resubmission | pass |
| Claude permission request → `request.answer` → file written | pass |
| Codex pong turn | pass |
| `server stop` / no surviving adapters / shutdown record | **fail** — see defect D3 |

Observed values worth keeping (home paths and identifiers elided):

- Claude `Fields`: `Model=model`, `Effort=effort`, `Permissions=mode`;
  `Capabilities`: `load-session, image-prompt, embedded-context, session-close`.
  Options: `mode` (default/acceptEdits/plan/auto/bypassPermissions), `model`,
  `effort`.
- Codex `Fields`: `Model=model`, `Effort=reasoning_effort`, `Permissions=mode`,
  `Speed=fast-mode`; same four capabilities. `collaboration_mode` stays unmapped
  and is retained for display, as the checkpoint says.
- Claude pong turn: `State=idle`, `StopReason=end_turn`,
  `Effective={Model:"fable[1m]", Effort:"high", Permissions:"default",
  Context:"unavailable", Speed:"unavailable"}`,
  `Usage={Used:20911, Size:1000000, Source:"usage_update"}`,
  `Thread.Options=[mode=default, model=fable[1m], effort=high]`, agent activity
  text exactly `pong`. Thread title became `Pong reply` from
  `session_info_update`. No thought rows on this turn.
- Codex pong turn: `StopReason=end_turn`,
  `Effective={Model:"gpt-6-astra", Effort:"xhigh", Permissions:"agent",
  Context:"unavailable", Speed:"off"}`,
  `Usage={Used:17021, Size:258400}`, agent text `pong`. No
  `session_info_update`, so the title fell back to the prompt text.
- Cancel: `StopReason=cancelled`, `State=interrupted`, `NeedsResume=true`,
  partial reply text retained, queue untouched. `thread.resume` returned the
  thread to `idle` with `NeedsResume=false` and **no** new activity entries
  (activity count unchanged, so nothing was resubmitted).
- Permission (Claude `mode=default`, its most cautious option): one blocking
  `approval` request, `Title="Write hello.txt"`, `Choices=["Yes","Yes, allow all
  edits during this session","No"]`, `ChoiceIDs=["allow-once",
  "allow-with-updates","reject"]`, `Delivery="acp-pending"`, detail carrying the
  tool call id, kind `edit`, location and a `+ hi` diff. Answering `Yes` with the
  request's `Revision` moved it to `resolved`/`acp-delivered`, the turn completed
  and `hello.txt` containing `hi` existed on disk. **`request.answer` requires
  `Revision`; omitting it returns HTTP 409.**
- Dynamic catalogue confirmed live: selecting `model=haiku` while `effort=high`
  was captured made the adapter reject `set_config_option` for `effort`. The
  thread went to `State=failed` with
  `Error='Agent rejected the captured settings: effort = "high": Internal
  error'` and the prompt stayed queued — the documented dispatch-failure
  contract, but a real usability trap worth recording.

## Defects found

- **D1 (fixed).** A cancelled turn's streamed `agent` row stayed
  `State: "running"` forever. Adapters emit chunks *after* the prompt response,
  and `appendChunk` unconditionally reset the row to `running`, re-opening an
  already settled entry. Observed live: the user row for the cancelled turn read
  `interrupted` while its agent row read `running`.
- **D2 (fixed).** `thread.interrupt` was accepted for a thread with no active
  turn, setting `State=interrupted` and `NeedsResume=true` while `StopReason`
  still said `end_turn`. Reproduced live by interrupting after a fast turn had
  already finished. The TUI already gates Stop with `activeTurn`; the server did
  not.
- **D3 (fixed).** `Session.Close` did not wait for the child process.
  `agent.Start`'s `stop` closure closed stdin and spawned goroutines for the 2 s
  kill and `cmd.Wait`, returning immediately. Consequences seen live: (a)
  `tui-go server stop` printed `Server stopped.`, recorded
  `{"Success":true}`, and exited while `codex-acp` was still running (it
  survived the server by ~5 s; `server.log` has no `agent process exited` line
  for it); (b) once, with a Claude turn in flight and a Codex session open, the
  shutdown record was `{"Success":false,"Error":"context deadline exceeded"}` and
  the CLI reported `server shutdown failed`. **(b) was not reproduced again** —
  it came from `httpServer.Shutdown`'s own 5 s budget, and the cause is still
  unproven. Record it honestly; do not claim it is fixed.
- **D4 (fixed).** `note()` used turn-independent ids (`current-mode`,
  `available-commands`, `config-options`, `update-<kind>`), so one row was shared
  by every turn and a later turn rewrote its `TurnID` while its transcript
  position stayed at the first turn. Live transcript showed `Agent mode changed`
  as row 1 carrying turn 3's id.
- **D5 (fixed, race only).** `probeConfiguredAgents` called `probeWG.Add(1)`
  after releasing the engine lock, so a concurrent shutdown could miss probes
  that were marked but not yet counted. Stopping during the startup probe left
  two adapter processes briefly alive with `server.log` showing no exits.
- **Not a defect, record it:** a `claude` grandchild process
  (`claude-agent-sdk-darwin-arm64/claude …`) outlives the adapter briefly after
  stop. The server owns the adapter, not its grandchildren.
- **Housekeeping observation:** 13 stray `tui-go server run` processes from
  earlier sessions were present on the machine. Not investigated, not killed.

## Changes already in the working tree (uncommitted, unverified by `make check`)

All under `apps/go`:

- `internal/agent/normalize.go` — `appendChunk` now takes its state from
  `streamState(t)` (new helper mapping thread `interrupted`/`failed`/`idle` to
  the matching activity state) and never resets a settled row to `running`.
  `note()` scopes its activity id to `id + "-" + t.TurnID`.
- `internal/agent/session.go` — new `exitWait = 5 * time.Second` constant;
  `Session` gained `pid int` and `exited <-chan struct{}`; `Start` records both;
  `Close` now calls the new bounded `awaitExit(ctx)` after `stop()`.
- `internal/server/agents.go` — `probeWG.Add(1)` moved inside the locked loop.
- `internal/server/commands.go` — `thread.interrupt` returns
  `failure("not_active", "this thread has no active turn to interrupt")` unless
  the thread is `running` or `waiting`.
- `internal/agent/normalize_test.go` — updated the unknown-kind assertions to the
  turn-scoped ids; added `TestTrailingChunksDoNotReopenASettledTurn` and
  `TestSessionNotesAreScopedToTheirTurn`.
- `internal/agent/session_process_test.go` (new) —
  `TestCloseWaitsForTheChildProcess` starts `sh -c "trap '' TERM INT; sleep 30"`,
  closes the session and asserts it returned within `exitWait` *and* that the pid
  is reaped (`syscall.Kill(pid, 0)` fails). Takes ~2 s.
- `internal/server/acp_fake_test.go` — `activityOf` also matches `id + "-"` so
  turn-scoped notes are found; `strings` import added.
- `internal/server/capacity_test.go` — the post-dispatch interrupt now targets
  the still-waiting `thread-review`, and a second assertion requires `not_active`
  for the finished `thread-shell`.

`go build ./...` passes, `gofmt` is clean, and `go test ./internal/...` passed
after the changes. **`make check` has not been re-run since the last edit** — run
it first.

## Remaining work

1. **Re-run `make check`** in `apps/go` (and `make build` before any further PTY
   work).
2. **Step 2 — TUI PTY check (not started).** Write `apps/go/scripts/pty_acp.py`
   in the style of `scripts/pty_steering.py` (reuse `pty_smoke.Terminal`; it
   takes `--binary` and `--artifacts`, uses an isolated `TUI_GO_HOME`, and drives
   the binary through an OS PTY). Requirements from the original brief:
   - 120×40, isolated home, adapters on `PATH`, a logged-in account.
   - New thread → choose Claude in the agent menu → choose a model → type the
     pong prompt → Send → wait for the reply. Capture a frame at each step and
     after completion.
   - Also capture the unavailable presentation by pointing
     `TUI_GO_AGENT_CODEX_COMMAND` at a nonexistent executable for one run.
   - Guard the whole harness behind `TUI_GO_LIVE_ACP=1` and skip otherwise.
     Because it needs real adapters and a live login, **keep it out of the
     Makefile `PTY_HARNESSES` list** and document the requirement in
     `scripts/README.md`.
   - Save captures under `docs/research/go-acp-captures/` with a `README.md`.
     Convention: see `docs/research/go-navigation-captures/README.md` and
     `go-activity-captures/README.md` — compressed `.ansi.gz` source plus
     `render-capture.py` PNG/SVG, a report JSON, and a README listing each frame
     with the exact regeneration commands.
   - Then look at the captures and report anything wrong: misaligned menus,
     missing labels, leftover fixture copy such as "Codex and Claude are not
     connected yet".
3. **Step 4 — documentation (not started).**
   - `docs/research/go-acp-2026-09-22.md`: the validation report. Versions,
     exact commands, outcomes, captures, defects fixed, and limits (single
     machine and account, no SSH or tmux, no `session/load` exercised live, no
     load or multi-client ACP testing).
   - Fill the **Validation** section of
     `docs/implementation/acp-checkpoint.md` with the live results, replacing
     the current "Not validated here" wording where it is now out of date and
     keeping the parts that remain true (MCP, images, subagents unexercised).
   - Add a dated "ACP agents — 2026-09-22" section to `docs/design/go-slice.md`
     plus a status-line update at the top, covering: install/`PATH`/env override
     instructions, probe and readiness, the agent menu, option-driven settings,
     dispatch and effective settings, streaming, cancel/resume semantics,
     permissions (including the Codex no-request finding from the earlier live
     probe), usage telemetry, restart behavior, the capabilities `acp-agents`,
     `agent-probe`, `acp-permissions`, `acp-cancel`, rebuild-and-restart-server
     instructions, and explicit non-claims (no fs or terminal client
     capabilities, no subagents, no `session/load` evidence, no steering to real
     agents).
   - One or two sentences each in `README.md`'s status sentence and
     `docs/design/implementation.md`'s status paragraph.
   - Check every relative link resolves.
4. **Do not commit** — that was the original instruction.

## Cautions for whoever continues

- Keep using an isolated `TUI_GO_HOME`; never stop or inspect the user's own
  server.
- Redact home directories, emails and the discovery token from every document.
- `request.answer` needs the request's current `Revision`.
- A long live prompt costs the user's own quota; prefer the shortest prompt that
  exercises the path.
- D3(b) — the `context deadline exceeded` shutdown record — is an honest
  single observation, not a reproduced defect. Say so.
