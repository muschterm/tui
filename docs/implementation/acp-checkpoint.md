# ACP agent slice checkpoint

**Continuation:** read [the latest agent-integration handoff](agent-integration-handoff.md)
before new work. The implementation below remains the current prototype; the
user reaffirmed ACP while reopening provider adapter selection and accepting a
unified question contract. Its old answer-delivery labels must not be treated as
general native-question support or confirmed upstream receipts.

Started 2026-09-22 after the user lifted the hardening hold on the Go slice.
This is implementation step 4's first vertical slice from
[the sequence](../design/implementation.md#vertical-slices): one pinned ACP
adapter path with initialization, capability negotiation, a real prompt,
confirmed settings, activity streaming, cancellation and reconnect without
resubmission. It follows [ADR 0002](../adr/0002-acp-agent-boundary.md),
[server ownership](../design/server.md) and
[thread configuration](../design/thread-configuration.md). Fixture behavior is
retained unchanged so existing reviews stay valid.

## Decisions taken for this slice

- **Go ACP library:** `github.com/coder/acp-go-sdk` v0.13.5 (community Go SDK,
  protocol version 1, generated stable types, JSON-RPC stdio connection). The
  upstream `zed-industries/agent-client-protocol` module contains no Go code.
  Cost: a generated 9.5k-line type surface that tracks upstream schema drift.
- **Adapters:** `@agentclientprotocol/claude-agent-acp` 0.80.0 (bundles Claude
  Agent SDK 0.3.278) and `@agentclientprotocol/codex-acp` 1.12.0 (bundles
  `@openai/codex` ^0.154.0), both Apache-2.0. They are **user-owned
  executables** resolved on the server's PATH (`claude-agent-acp`, `codex-acp`),
  not installed or downloaded by the application. Documented install:
  `npm install -g @agentclientprotocol/claude-agent-acp@0.80.0
  @agentclientprotocol/codex-acp@1.12.0`. `TUI_GO_AGENT_CLAUDE_COMMAND` and
  `TUI_GO_AGENT_CODEX_COMMAND` override the executable for the server process.
  A settings form for agent commands is deferred.
- **Authentication** uses whatever the adapter finds in the user's existing
  CLI login; the ADE never offers a login flow in this slice. An adapter that
  reports required authentication is shown as `unauthenticated` with its
  reported methods.
- **Client capabilities advertised:** none for `fs` or `terminal` (the agent's
  own direct access is unaffected); no boolean capability flag exists in the
  pinned schema. No subagent dialect is negotiated yet; child activity therefore arrives as
  ordinary tool calls and the Agents surface stays empty for ACP threads.
- **Server ownership:** agent processes are children of the background server,
  one per ACP thread session, started at first dispatch and ended with
  `session/close` when supported, else stdin close + kill. Server stop ends
  them. Restart cannot reattach a process: ACP threads that were running or
  waiting become `interrupted` and need explicit Resume. Resume clears the gate;
  the next queued dispatch starts a fresh process and uses `session/load` when the agent advertised it, otherwise
  `session/new` plus a visible notice that upstream context was not restored.
  No prompt is ever resent automatically.
- **Options:** `agent.probe` launches the adapter, runs `initialize`, creates a
  provisional session in the project's checkout (or the server's project
  starting folder when no project is given), records the session's config
  options, then closes it. Options are cached on the `Agent` record until the
  next probe. Model / effort / permissions / context / speed map from option
  categories `model`, `thought_level`, `mode` and `model_config` (ID or name
  containing `context` or `fast`/`speed`); unmapped options are kept for
  display as opaque agent defaults.
- **Dispatch:** before `session/prompt`, every mapped captured setting is
  applied with `session/set_config_option`; the response's full option state
  becomes `Thread.Effective`. A rejected or missing value fails the dispatch
  with `Thread.State = "failed"` and the prompt left at the head of the queue.
- **Streaming persistence:** session updates mutate the in-memory snapshot and
  are flushed to SQLite and subscribers at most ten times per second; commands
  and turn outcomes flush immediately.

## Wire contract additions (protocol v1, additive)

Types are in [types.go](../../apps/go/internal/protocol/types.go).

| Addition | Meaning |
| --- | --- |
| `Snapshot.Agents []Agent` | Configured connections. `fixture` agent ID `fixture`, name `Fixture agent`, always `ready`. ACP defaults: IDs `claude`, `codex`, names `Claude`, `Codex`. |
| `Agent.State` | `unprobed`, `probing`, `ready`, `unauthenticated`, `unavailable`; `Detail` explains. |
| `Agent.Options`, `Agent.Fields` | Config options from the last probe and the option IDs feeding each `Settings` field. `Settings` values are option **value IDs**. |
| `Agent.Capabilities` | Emitted from `initialize` only: `load-session`, `image-prompt`, `embedded-context`, `audio-prompt`, `session-close`. `boolean-options` is **not** emitted — ACP v1 has no client capability for it and the agent reports no such fact. |
| `Thread.AgentID`, `SessionID`, `StopReason`, `Error` | Agent identity, upstream session, last turn's stop reason (`end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`), last failure text. |
| `Thread.Options` | The live session's full current option catalogue, published after successful settings application and replaced whole on `config_option_update`; supersedes `Agent.Options` while the session exists. The probe showed the catalogue is dynamic (a model change added a `fast` option). |
| `Thread.Usage` | Agent-supplied `usage_update` telemetry (`Used`, `Size`, `Source`, `ReportedAt`); nil until reported. Feeds the context gauge and Usage inspector; nothing is computed or fetched. |
| `Thread.State = "failed"` | Turn or dispatch failed; `Error` set; Send is allowed and starts a new turn. |
| `Request.ChoiceIDs` | ACP permission option IDs parallel to `Choices` (names). New clients send `Command.ApprovalChoiceID` under `approval-choice-ids`; legacy labels must be unambiguous. Accepted requests retain `ApprovalChoiceID`, `SubmissionID`, `SubmittedRevision`, `TurnID`, and `DeliveryRoute`. See the [delivery continuation](request-delivery-checkpoint.md). |
| Command `agent.probe` | `TargetID` = agent ID, optional `ProjectID` for the cwd. Accepted receipt; result appears on the `Agent` record. |
| Command `thread.start` | `Agent` may now be any agent ID (legacy `Fixture agent` name still accepted for the fixture). |
| Command `thread.interrupt` | ACP: sends `session/cancel`; the turn ends with `cancelled`, thread `interrupted`, `NeedsResume` true, queue held. |
| Command `thread.resume` | ACP: clears `NeedsResume`; the next queued dispatch restarts the process if needed. An empty queue sends nothing. |
| Capabilities | `acp-agents`, `agent-probe`, `acp-permissions`, `acp-cancel`, `approval-choice-ids`. |

Activity mapping: `agent_message_chunk` text appends to one `Role: "agent"`
activity per turn; `agent_thought_chunk` appends to a `Role: "thought"` activity
(rendered muted, labelled Thinking); `tool_call`/`tool_call_update` upsert one
`Role: "tool"` (or `"mcp"` when the adapter marks MCP) activity keyed by the
tool call ID with `State` pending/running/completed/failed, `Title` from the
call title, `Text` a one-line summary and `Detail` the retained raw input,
output, locations and content; `plan` replaces `Thread.Plan` (in_progress →
`active`); `request_permission` adds a blocking `approval` request whose answer
resolves the pending ACP call; `config_option_update` refreshes the option catalogue and `Effective`.
`available_commands_update` and `current_mode_update` record bounded activity
notes; mode IDs are not treated as config-option values. Session notes use
turn-scoped identities so later turns do not rewrite earlier history. Unknown update kinds are retained as `Role: "tool"`
rows titled with the kind, never dropped silently.

## Live probe findings applied (2026-09-22)

[The live probe](../research/acp-live-probe-2026-09-22.md) ran both adapters
against real accounts through the coder SDK and recorded the streams under
[acp-fixtures](../research/acp-fixtures/). Consequences for this slice:

- The client dispatches on the raw `sessionUpdate` discriminator before
  mapping known kinds through SDK types. The initial probe interpretation of
  typed usage/title decoding was corrected during implementation: the pinned
  SDK decodes those kinds, but an unknown kind can match a known shape. Raw
  dispatch avoids misclassification and retains unknown kinds.
- Option IDs are adapter-specific (Claude: `mode`/`model`/`effort`; Codex:
  `mode`/`collaboration_mode`/`model`/`reasoning_effort`/`fast-mode`); only
  categories are shared, so `Fields` maps by category and values are opaque.
- Both adapters accept `protocolVersion: 99` and answer 1; the client fails
  closed on the negotiated version itself.
- Cancel is prompt (4–24 ms to the `cancelled` prompt response) with trailing
  updates before the response; both exit 0 on stdin close (Codex after ~2 s).
- A read-only prompt raised no permission request on either adapter. For a
  file write in each adapter's most cautious mode, Claude raised one request
  (`allow-once`/`allow-with-updates`/`reject` with a diff and locations) and
  Codex raised none and wrote the file. Mode names do not establish approval
  parity; the UI must not present a mode as a sandbox.
- Both advertise `_meta` steering support; ADR 0009's real-agent steering
  remains a later slice.

## Ownership

- Backend: `internal/agent` (new), `internal/server`, `internal/storage`,
  `internal/fixture`, `internal/protocol` additions.
- TUI: `internal/tui`, `internal/shell`.
- Evidence: `docs/research/acp-live-probe-2026-09-22.md`,
  `docs/research/acp-fixtures/`, this checkpoint, `go-slice.md`, ADR 0013.

## Backend deviations from the contract above

Recorded here because the TUI half consumes the same wire contract.

- **No `session.configOptions.boolean` client capability is advertised.** The
  stable ACP schema shipped in acp-go-sdk v0.13.5 gives `ClientCapabilities`
  only `fs` and `terminal`; there is no field for it. Boolean options are
  accepted unconditionally and mapped with explicit `"true"`/`"false"` value
  IDs, and `Agent.Capabilities` records only facts the agent itself reported.
- **Session updates are decoded from the raw wire object, not from the SDK's
  typed union.** v0.13.5 resolves an unrecognised `sessionUpdate`
  discriminator by shape, so `{"sessionUpdate":"invented_kind","x":1}` decodes
  without error as a `session_info_update`. Dispatching on the union would let
  a future kind masquerade as a known one — and silently rename a thread. The
  client therefore uses its own `MethodHandler` and dispatches on the wire
  kind, keeping the union only for the kinds the discriminator confirmed.
  (`usage_update` and `session_info_update` themselves do decode correctly in
  this version; the hazard is the unknown-kind fallback.)
- **Option mapping keys on category only**, with one unavoidable exception:
  `model_config` feeds two composer fields, so Context and Speed are separated
  by the agent's own wording (`context`, `fast`/`speed`). `collaboration_mode`
  maps to Permissions only when no `mode` option exists. Unmapped options stay
  in `Options` for display.
- **`Agent.Detail` for a missing executable carries the documented install
  hint** (`npm install -g @agentclientprotocol/…`) so the unavailable state is
  actionable.
- **A pending approval does not survive a restart.** Its ACP call belonged to a
  process that no longer exists, so recovery marks it `State: "closed"` with
  `Delivery: "acp-undeliverable"` instead of leaving an approval card waiting
  for an answer nothing can read. `thread.resume` does the same for requests
  still pending when the process died. A future permission request requires a
  new live call; Resume alone does not replay the interrupted prompt or guarantee another request.
- **Approval acceptance is not delivery confirmation.** The continuation commits
  `submitted` / `acp-accepted` first, then `closed` / `acp-unconfirmed` before
  returning the callback response. There is no SDK receipt/write callback;
  subsequent tool/turn completion is not an answer receipt. Accepted answers and
  legacy `acp-delivered` records recover as `closed` / `acp-uncertain`, without
  replay. Unsupported ACP question submissions fail explicitly. Connection
  generation/session/turn checks protect approvals; updates from older
  connections or other sessions are ignored.
- **Corrected by the [dispatch/recovery continuation](agent-recovery-checkpoint.md):**
  setup/settings failures leave unsent input queued. A failed dispatched turn
  retains its capture in history, requires Resume and never requeues the
  original prompt. This prevents Send/Resume from repeating possible tool
  effects after an uncertain RPC outcome.
- **Startup probing was added** after the T3 comparison: every configured ACP
  connection is probed once, concurrently, bounded at 30 s, as soon as
  discovery is published. `agent.probe` remains the refresh path and now
  *coalesces* with an in-flight probe (accepted receipt, no second process)
  rather than being rejected.
- **`Thread.Options` and `Thread.Usage`** (added to the shared protocol during
  this slice) are published after a successful complete settings-application
  sequence and on `config_option_update`, and on `usage_update` respectively. Intermediate
  settings responses stay local; a later rejection fails dispatch, so partial
  upstream settings changes are not presented as an accepted effective selection.
  The live catalogue supersedes the probe-time one for validation, because switching a model can
  add an option in the same response.

## Latest continuation

[Request-delivery checkpoint](request-delivery-checkpoint.md) records the new
approval lifecycle, source/schema comparisons and fresh validation. It does not
enable native questions, fallback, asynchronous answers or Answered history.

## Earlier validation

Updated 2026-09-22 after merged-tree review. See the
[validation report](../research/go-acp-2026-09-22.md) and
[retained captures](../research/go-acp-captures/README.md) for exact commands,
results and limits.

- `GOCACHE=/tmp/tui-go-build make check` passes on Darwin 27.0.0 arm64,
  Go 1.27.1: formatting, vet, pinned staticcheck, race tests and build.
- Server/agent regressions cover readiness, probe coalescing, option validation,
  complete settings application, bounded stream replay, permission answer IDs,
  actual cancellation acknowledgment, no resend on Resume, stale dispatch after
  queue edits/removal, live Resume rejection, process-loss session loading,
  live option catalogues, and process reaping after Close/Kill.
- The earlier handoff records live HTTP pong turns for Claude and Codex,
  Claude cancel/resume and a permission-approved scratch file write. These are
  inherited observations, not repeated permission evidence from this run.
- The final 120×40 Codex OS-PTY run passes: New thread, agent/model menus,
  exact pong prompt/reply, usage, Stop and Resume without resubmission. Both
  live runs confirm all three directly owned adapter processes exited before
  the successful server-stop return (two probes and one thread process).
- Missing-executable menu capture passes. Claude initializes and exposes menus,
  but the current account limit rejects its prompt; the TUI retains one queued
  prompt and displays the failure. A successful Claude TUI reply and live
  permission-card activation remain blocked by quota.

The new PTY harness is opt-in (`TUI_GO_LIVE_ACP=1`) and excluded from `make pty`.
All seven existing fixture PTY harnesses pass; their reports are retained with
the evidence. No native GUI,
SSH/tmux, live `session/load`, multi-client ACP/load test, MCP, image or structured
subagent result is claimed. One earlier HTTP shutdown deadline failure remains
unreproduced and unexplained; successful later stops do not establish its cause.
