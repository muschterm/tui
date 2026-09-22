# Live ACP adapter probe

Recorded: **2026-09-22**. Unlike [the ACP protocol note](acp-protocol.md), this records **executed** behavior: two pinned ACP adapters were driven over stdio by a throwaway Go client built on the community Go SDK, and the JSON-RPC wire traffic was captured. Recorded streams are in [`acp-fixtures/`](acp-fixtures/).

Everything under "Observed" was read from the recordings. Everything under "Inference" is reasoning that the recordings do not by themselves establish.

**Implementation correction:** the original typed-SDK interpretation below was
superseded by the checkpoint's decoding tests. v0.13.5 decodes usage and session
info; its unknown-kind shape fallback is the reason the implementation dispatches
on the raw discriminator. Wire observations remain unchanged. See
[the checkpoint](../implementation/acp-checkpoint.md#backend-deviations-from-the-contract-above).

## Scope and limits

- One machine (macOS, Darwin 27.0.0, arm64), one operator account per provider, one probe run per adapter.
- One `session/new`, three prompts and one `session/cancel` per adapter. No load, no reconnect, no `session/load`, `session/resume`, `session/fork`, MCP servers, images, or subagents were exercised.
- The probe advertised **no** `fs` and **no** `terminal` client capabilities, so nothing here says how these adapters behave against a client that does.
- Both providers were already authenticated on this machine; no authentication flow was exercised. Model catalogues, effort levels and quota numbers are account- and date-specific and will differ elsewhere.
- Prompt wording, model selection and agent behavior are nondeterministic. Counts of update messages are one sample, not a contract.

## Versions

| Component | Version |
| --- | --- |
| Go SDK | `github.com/coder/acp-go-sdk` v0.13.5 |
| Go toolchain | go1.27.1 darwin/arm64 |
| Node | v26.9.0 |
| Claude adapter | `@agentclientprotocol/claude-agent-acp` 0.80.0 |
| — its agent dependency | `@anthropic-ai/claude-agent-sdk` 0.3.278 |
| Codex adapter | `@agentclientprotocol/codex-acp` 1.12.0 |
| — its agent dependency | `@openai/codex` 0.154.0 (installed beside the adapter) |
| Globally installed CLIs on PATH | `claude` 2.1.278, `codex` 0.155.1 |

**Not verified:** which `claude`/`codex` binary each adapter actually executed. Both a locally installed dependency and a global CLI were present; the probe did not trace child process execution. Treat the adapter's bundled dependency version and the global CLI version as two candidates, not as an established fact.

## What was run

The probe is archived at [`go-feasibility-probes/acp-adapter-probe.go`](go-feasibility-probes/acp-adapter-probe.go) (build-tagged `ignore`; it was executed from a throwaway module `require github.com/coder/acp-go-sdk v0.13.5`). Fixture packaging and redaction: [`go-feasibility-probes/acp-adapter-probe-fixtures.py`](go-feasibility-probes/acp-adapter-probe-fixtures.py).

```
# fresh, empty, git-initialised session cwd per adapter
git init -q <scratch>/work/<adapter>-cwd

acpprobe -bin <scratch>/adapters/node_modules/.bin/claude-agent-acp \
         -cwd <scratch>/work/claude-cwd \
         -out <scratch>/out/claude-agent-acp -askmode default

acpprobe -bin <scratch>/adapters/node_modules/.bin/codex-acp \
         -cwd <scratch>/work/codex-cwd \
         -out <scratch>/out/codex-acp -askmode read-only
```

Phase order in the `main` run: `initialize` → `session/new` → prompt "Reply with exactly the word pong and nothing else." → `session/set_config_option` (model) → `session/set_mode` to an approval-asking mode + a prompt that needs a file write → prompt "Count slowly from 1 to 50…" cancelled after 2 s → close stdin. A second, separate process ran only `initialize` with `protocolVersion: 99`.

The probe's client returned JSON-RPC `-32601` method-not-found for all `fs/*` and `terminal/*` methods and auto-selected the first `allow_once`-kind permission option. `CLAUDECODE`, `CLAUDE_CODE_*`, `AI_AGENT`, `CLAUDE_EFFORT` and `CLAUDE_PID` were stripped from the child environment; everything else was inherited.

## Observed: initialization

Both adapters returned `protocolVersion: 1` and completed `initialize` in well under a second (0.149 s claude, 0.110 s codex).

`claude-agent-acp` — [`acp-fixtures/claude-agent-acp/initialize.json`](acp-fixtures/claude-agent-acp/initialize.json):

- `agentInfo`: `{name: "@agentclientprotocol/claude-agent-acp", title: "Claude Agent", version: "0.80.0"}`.
- `agentCapabilities`: `loadSession: true`; `promptCapabilities: {image: true, embeddedContext: true}` (no `audio`); `mcpCapabilities: {http: true, sse: true}`; `auth: {logout: {}}`; `providers: {}`; `sessionCapabilities: {additionalDirectories, close, delete, fork, list, resume, subagents}` (each an empty object); `_meta.claudeCode.promptQueueing: true`; `_meta.authStatus: {}`.
- `authMethods: []`.
- Top-level `_meta` advertises three non-core dialects: `steering.supported: true`; `goal {version: 1, controlMethod: "_session/goal", actions: ["set","clear"]}`; `jetbrains.air {version: 1, capabilities: ["sessionFailure","agentFileChangeReport","nativeSubagentSessions","asyncTasks","recommendedValue"]}`.

`codex-acp` — [`acp-fixtures/codex-acp/initialize.json`](acp-fixtures/codex-acp/initialize.json):

- `agentInfo`: `{name: "@agentclientprotocol/codex-acp", title: "Codex", version: "1.12.0"}`.
- `agentCapabilities`: `loadSession: true`; `promptCapabilities: {embeddedContext: true, image: true}`; `mcpCapabilities: {acp: false, http: true, sse: false}`; same `sessionCapabilities` set as the Claude adapter including `subagents`; `_meta.authStatus: {}`.
- `authMethods`: **two** entries — `{id: "api-key", name: "API Key", _meta: {"api-key": {provider: "openai"}}}` and `{id: "chat-gpt", name: "ChatGPT"}`. The adapter advertised these despite the account already being authenticated; no `auth_required` error was ever returned.
- Top-level `_meta`: `steering.supported: true`; `goal {version: 1, controlMethod: "_session/goal", actions: ["set","pause","resume","clear"]}` (**four** actions, vs. two for Claude); the same `jetbrains.air` block.

**Observed:** the SDK's typed `InitializeResponse` round-trips faithfully — the decoded response and the raw wire result matched field for field, including `_meta`.

**Inference:** `_meta.steering.supported` is the per-adapter capability that [ADR 0009](../adr/0009-turn-bound-steering.md) requires before offering Steer. `goal.actions` differing between adapters is exactly the kind of per-dialect variation the specification says must be negotiated rather than assumed. Neither dialect was exercised.

## Observed: unsupported protocol version

**Neither adapter rejects `protocolVersion: 99`.** Both returned a normal successful `initialize` result with `protocolVersion: 1` and the same capability payload as a well-formed handshake, in 0.133 s / 0.140 s. No JSON-RPC error. See [`acp-fixtures/claude-agent-acp/bad-version.json`](acp-fixtures/claude-agent-acp/bad-version.json) and [`acp-fixtures/codex-acp/bad-version.json`](acp-fixtures/codex-acp/bad-version.json).

**Inference:** this matches the ACP v1 rule that the agent answers with the latest version it supports and the *client* must decide whether to disconnect. The implementation must therefore compare the returned `protocolVersion` against what it supports and fail closed itself; a successful `initialize` response is not evidence of version agreement.

## Observed: `session/new`

Both adapters accepted an empty `mcpServers: []` and a freshly `git init`-ed directory as `cwd`. Neither issued a *request* to the client during session creation; both pushed `_auth/status_update` notifications while it was in flight (claude twice, codex once). `claude-agent-acp` took 0.568 s, of which its stderr attributes 0.536 s to `phase=sdk-initialize`; `codex-acp` took 0.897 s.

Response shape (both): `sessionId`, `modes`, `configOptions`. `codex-acp` additionally returns a non-standard `models` key. Neither returned a top-level `_meta` on this response.

### Modes

| Adapter | `currentModeId` | `availableModes` (id → name, `_meta.kind`) |
| --- | --- | --- |
| claude | `auto` | `default` → Manual (`standard`), `acceptEdits` → Accept edits (`standard`), `plan` → Plan (`plan`), `auto` → Auto (`auto_review`), `bypassPermissions` → Bypass permissions (`full_access`) |
| codex | `agent` | `read-only` → Ask for approval (`standard`), `agent` → Approve for me (`auto_review`), `agent-full-access` → Full access (`full_access`) |

Both carry a private `_meta.kind` taxonomy (`standard` / `plan` / `auto_review` / `full_access`) that is *not* part of the mode id. The ids themselves are adapter-specific and share no vocabulary.

### Config options

Both advertise `type: "select"` options with `id`, `name`, `description`, `category`, `currentValue`, and an **ungrouped** `options` array whose entries use the key **`value`** (not `id`), plus `name` and optional `description`/`_meta`.

| Adapter | `id` | `category` | `currentValue` at session start | advertised values |
| --- | --- | --- | --- | --- |
| claude | `mode` | `mode` | `auto` | `default`, `acceptEdits`, `plan`, `auto`, `bypassPermissions` |
| claude | `model` | `model` | `fable[1m]` | `default`, `opus[1m]`, `fable[1m]`, `sonnet`, `haiku` |
| claude | `effort` | `thought_level` | `high` | `default`, `low`, `medium`, `high`, `xhigh`, `max` |
| codex | `mode` | `mode` | `agent` | `read-only`, `agent`, `agent-full-access` |
| codex | `collaboration_mode` | `collaboration_mode` | `default` | `default`, `plan` |
| codex | `model` | `model` | `gpt-6-astra` | `gpt-6-astra`, `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5` |
| codex | `reasoning_effort` | `thought_level` | `xhigh` | `low`, `medium`, `high`, `xhigh`, `max`, `ultra` |
| codex | `fast-mode` | `model_config` | `off` | `off`, `on` |

**Option ids are not portable.** `model` and the `mode`/`model`/`thought_level`/`model_config` *categories* are shared; `effort` vs `reasoning_effort`, `fast` vs `fast-mode`, and every value id differ. Codex exposes a `collaboration_mode` (plan) option that the Claude adapter folds into its `mode` option instead.

`codex-acp` also returns a separate `models.availableModels` list whose `modelId`s fuse model and effort (`gpt-6-astra[low]`, `gpt-6-astra[medium]`, `gpt-6-astra[high]`, `gpt-6-astra[xhigh]`, …) with display `name`/`description`. This overlaps the `model` + `reasoning_effort` selects and is a different axis decomposition.

**Inference:** `category` is the only cross-adapter handle for placing a control ("this is the model picker"); `id` and `value` must be treated as opaque per-adapter strings captured at Send. Nothing here says an id means what its name suggests — `fast-mode`, `max` and `ultra` were never exercised.

## Observed: `session/set_config_option`

Switching the `model` select succeeded on both adapters and was fast: claude 0.014 s, codex 0.002 s (round-trip, measured client-side). Both returned the **full** `configOptions` array with the new `currentValue` applied — not a delta. No `config_option_update` notification was emitted in either run; the response was the only acknowledgement.

Two further observations:

- **The option set itself changed on the Claude adapter.** `session/new` advertised three options (`mode`, `model`, `effort`). After switching `model` from `fable[1m]` to `opus[1m]`, the response carried **four**: a `fast` option (`category: model_config`, `currentValue: off`, values `on`/`off`) appeared that had not been advertised before. Codex returned the same five options it started with.
- **An invalid value is a `-32603` "Internal error", not a typed rejection.** During an earlier iteration of this probe a malformed (empty) value was sent for `mode` and `claude-agent-acp` answered `{"code": -32603, "message": "Internal error", "data": {"details": "Invalid value for config option mode: "}}`. The useful detail was in `data.details`. *This exchange is not in the fixtures* — it came from a probe iteration that was discarded and replaced, and it was not re-run.

The change took effect for subsequent turns: after switching codex to `gpt-5.6-sol`, the next `session/prompt` response reported `_meta.quota.model_usage[0].model: "gpt-5.6-sol"`.

See [`acp-fixtures/claude-agent-acp/set-config-option.json`](acp-fixtures/claude-agent-acp/set-config-option.json) and [`acp-fixtures/codex-acp/set-config-option.json`](acp-fixtures/codex-acp/set-config-option.json).

**Inference:** the client cannot cache a session's option catalogue across a `set_config_option`; it must re-render from every response, because the available options are model-dependent. Error `data.details` is the only user-presentable text for a rejected value, and it is free-form.

## Observed: session update kinds

Discriminators seen on the wire across both adapters, with the shapes recorded:

| `sessionUpdate` | Seen from | Shape observed |
| --- | --- | --- |
| `agent_message_chunk` | both | `{content: {type: "text", text: "..."}}`; text arrives split arbitrarily — the Claude adapter delivered "pong" as two chunks, `"p"` then `"ong"` |
| `agent_thought_chunk` | codex only | same `content` shape; 10 chunks during the tool turn. The Claude adapter emitted none in these runs |
| `tool_call` | both | `{toolCallId, title, kind, status, content: [], locations: [], rawInput, name}`; Claude adds `_meta.claudeCode.toolName`. The first `tool_call` can be a placeholder: Claude sent `title: "Preparing file…"`, `rawInput: {}`, `status: "pending"` |
| `tool_call_update` | both | `{toolCallId}` plus **only the changed fields**. `status` was frequently absent (`null`) on intermediate updates; `title`, `content`, `rawOutput` and `_meta` appear independently |
| `available_commands_update` | both | `{availableCommands: [{name, description, input}]}`; 76 then 73 entries (claude), 60 (codex). Codex entries can carry `_meta.commandAction`, e.g. `{kind: "setConfigOption", configId: "collaboration_mode", value: "plan", resetValue: "default", presentation: "state"}` |
| `usage_update` | both | **`{used: <int>, size: <int>}`** — claude `size: 1000000`, codex `size: 258400`. Emitted repeatedly during a turn, including after cancel |
| `session_info_update` | both | claude: `{title: "Pong reply", updatedAt: "2026-09-22T14:42:14.030Z"}` (an agent-generated thread title). codex: `{_meta: {codex: {threadStatus: {type: "active", activeFlags: []}}}}` and `{... {type: "idle"}}` |

**Not seen in any run:** `plan`, `current_mode_update`, `user_message_chunk`, or any `config_option_update`. `session/set_mode` returned `{}` with no accompanying `current_mode_update`.

**SDK gap:** `usage_update` and `session_info_update` do **not** decode into a typed variant of `acp.SessionUpdate` in coder/acp-go-sdk v0.13.5 — they arrive as an all-`nil` union. 4 of the 8 update notifications in the Claude pong turn and 4 of the 6 in the Codex pong turn were of these untyped kinds. The SDK does not error on them; it silently yields an empty `SessionUpdate`.

**Inference:** `usage_update`'s `used`/`size` is the direct feed for the context-occupancy part of [the usage display](../design/usage.md), and `size` differs per adapter and per model, so it must be read from the stream and not assumed. An implementation on this SDK version needs its own decoding path (or an SDK upgrade) for at least `usage_update` and `session_info_update`, or it will drop usage and title data entirely. `agent_message_chunk` arriving pre-split confirms the grapheme-aware incremental composition requirement.

Out-of-band notification: both adapters sent `_auth/status_update` — an undocumented extension method, not a `session/update` — carrying `{authStatus: {kind: "account", label, account: {email, plan, organization?}}}`. The Claude adapter sent it three times in one run, the Codex adapter once. The SDK's `ClientSideConnection` routed it through the extension path without error.

## Observed: an ordinary prompt turn

| | claude | codex |
| --- | --- | --- |
| Wall time for "Reply with exactly the word pong…" | 2.02 s | 5.12 s |
| `stopReason` | `end_turn` | `end_turn` |
| Reply text | `p` + `ong` | `pong` |
| `usage` on the response | `{inputTokens: 2, outputTokens: 4, cachedReadTokens: 20924, cachedWriteTokens: 0, totalTokens: 20930}` | `{inputTokens: 4205, outputTokens: 5, cachedReadTokens: 12928, thoughtTokens: 0, totalTokens: 17138}` |
| `_meta.quota` | present | present |
| Permission requests | none | none |
| `fs/*` or `terminal/*` calls into the client | none | none |

Both responses carry a `_meta.quota` block with `token_count` **and a per-model breakdown**: `model_usage: [{model, token_count{totalTokens, inputTokens, cachedInputTokens, cachedWriteTokens, outputTokens, reasoningOutputTokens}}]`. The Claude pong turn attributed tokens to **two** models in one turn — `claude-haiku-4-5-20251001` (915 tokens) alongside the selected `claude-fable-5-1` (20,930) — presumably an internal auxiliary call.

Note the field-name mismatch between the two places usage appears: the typed `usage` uses `cachedReadTokens`/`thoughtTokens`, while `_meta.quota.token_count` uses `cachedInputTokens`/`reasoningOutputTokens` for what appear to be the same quantities. Codex's typed `usage` omits `cachedWriteTokens` entirely.

**Inference:** a usage display must record which of these sources a number came from, because the two disagree on naming and coverage within a single response. The two-model breakdown in a single turn means "the model for this turn" is not a single value — the selected model and the models actually billed are different facts.

## Observed: a turn requiring permission

After `session/set_mode` to each adapter's approval-asking mode (claude `default` "Manual", codex `read-only` "Ask for approval"), the probe asked each to create `probe.txt` containing `hi`.

**`claude-agent-acp` issued exactly one `session/request_permission`** — the only inbound *request* either adapter made anywhere in this probe. From [`acp-fixtures/claude-agent-acp/prompt-tool.jsonl`](acp-fixtures/claude-agent-acp/prompt-tool.jsonl):

```json
{"toolCall":{"toolCallId":"toolu_…","name":"Write","status":"pending",
  "rawInput":{"file_path":"<SESSION_CWD>/probe.txt","content":"hi\n"},
  "title":"Write probe.txt","kind":"edit",
  "content":[{"type":"diff","path":"<SESSION_CWD>/probe.txt","oldText":null,"newText":"hi\n"}],
  "locations":[{"path":"<SESSION_CWD>/probe.txt"}]},
 "_meta":{"permission":{"version":1,"title":"Write probe.txt"}},
 "options":[{"optionId":"allow-once","name":"Yes","kind":"allow_once"},
            {"optionId":"allow-with-updates","name":"Yes, allow all edits during this session","kind":"allow_always"},
            {"optionId":"reject","name":"No","kind":"reject_once"}],
 "sessionId":"…"}
```

Three options, no `reject_always`. The request carries a ready-made `diff` content block and a `locations` array — the approval card has the file, the old/new text and a human title without any extra round-trip. The probe answered `{"outcome":{"outcome":"selected","optionId":"allow-once"}}`; the turn then completed with `end_turn` after 4.54 s, and the file was written by the agent itself (the probe advertises no `fs` capability and was never called).

Also observed: the permission request used JSON-RPC **`id: 0`**. The agent numbers its outbound requests in its own id space, overlapping the client's.

**`codex-acp` issued no permission request at all** in `read-only` mode. It wrote the file and additionally ran two shell commands (`xxd -g 1 probe.txt`, `truncate -s 2 probe.txt` — it was correcting the trailing newline), reported them as `tool_call` entries with `kind: "execute"` and `kind: "edit"`, and streamed the command output through `_meta.terminal_output_delta` / `_meta.terminal_exit` on `tool_call_update` rather than through the ACP `terminal/*` client methods (which the probe did not advertise). The turn took 30.3 s and ended `end_turn`, with 13 message chunks, 10 thought chunks and 3 tool calls.

**Inference (not established):** the plausible reading is that codex's `read-only` mode governs access *outside* the workspace and the network, matching its own description ("Always ask to edit external files and use the internet"), so an in-workspace write needs no approval. This probe did not test an out-of-workspace path, so the boundary is unverified. What *is* established is that **mode name parity does not imply approval parity**: the same instruction under each adapter's nominally most cautious mode produced one approval card and zero.

## Observed: cancellation

`session/cancel` was sent 2 s into a deliberately long prompt ("Count slowly from 1 to 50, one number per line, pausing between numbers").

| | claude | codex |
| --- | --- | --- |
| `session/cancel` sent at | +9.299 s | +38.432 s |
| Trailing `session/update` after cancel | `usage_update` at +9.303 s | `usage_update` at +38.446 s, `session_info_update` (`threadStatus: idle`) at +38.455 s |
| `session/prompt` response at | +9.303 s | +38.456 s |
| Cancel → response latency | **~4 ms** | **~24 ms** |
| `stopReason` | `cancelled` | `cancelled` |
| Response `usage` | all zeros, `_meta.quota.model_usage: []` | present |

Both honoured cancel promptly and both returned `stopReason: "cancelled"` on the *original* `session/prompt` response rather than erroring it. Trailing updates did arrive after the cancel notification and before the response, in both cases. No permission request was pending, so the cancelled-outcome path was not exercised.

See [`acp-fixtures/claude-agent-acp/prompt-cancel.jsonl`](acp-fixtures/claude-agent-acp/prompt-cancel.jsonl) and [`acp-fixtures/codex-acp/prompt-cancel.jsonl`](acp-fixtures/codex-acp/prompt-cancel.jsonl).

**Inference:** a Stop control can treat the prompt response as authoritative and does not need its own timeout in the common case, but it must still accept updates arriving between the notification and the response — the recordings show them. Latency here was measured on a turn that had produced no output yet; a turn mid-tool-call was not tested.

## Observed: process lifecycle

Closing the adapter's stdin terminated both processes cleanly with **exit code 0**, with no explicit shutdown method: `claude-agent-acp` in 0.025 s (0.005 s in the bad-version run), `codex-acp` in 2.017 s — the same 2.017 s in its bad-version run. Neither required `SIGTERM`. Both wrote diagnostics to stderr and kept stdout clean for protocol traffic — `claude-agent-acp`'s stderr carried structured session-creation phase timings (`[session/create] … phase=sdk-initialize durationMs=536 totalMs=562`), which is useful for surfacing why a new session is slow.

**Inference:** codex's consistent ~2 s stdin-close delay is long enough that a client shutting down several sessions needs to close them concurrently, and a user-visible "stopping" state should not assume sub-second exit. Whether this is a fixed drain timer was not determined.

## Fixtures

[`acp-fixtures/claude-agent-acp/`](acp-fixtures/claude-agent-acp/) and [`acp-fixtures/codex-acp/`](acp-fixtures/codex-acp/):

| File | Contents |
| --- | --- |
| `initialize.json` | the `initialize` request/response pair, raw |
| `new-session.json` | the `session/new` request/response pair, raw |
| `set-config-option.json` | the model switch: summary plus the raw request/response |
| `prompt-pong.jsonl` | every wire message of the simple prompt turn |
| `prompt-tool.jsonl` | the mode switch, the permission exchange and the tool turn |
| `prompt-cancel.jsonl` | the long prompt, the cancel notification and the trailing updates |
| `prompt-summaries.json` | decoded per-turn summaries (timing, stop reason, update counts, permission records) |
| `bad-version.json` | the `protocolVersion: 99` handshake and its wire log |
| `shutdown.json` | exit code, wait time and adapter stderr |
| `wire-full.jsonl` | the whole `main` run, phase-tagged, in order |

Each `wire-full.jsonl`/`*.jsonl` line is `{t, phase, dir, msg}`, where `t` is seconds since process start and `dir` is `send` (client → agent) or `recv` (agent → client).

**Redaction applied:** the operator's email → `<redacted-email>`; subscription `plan`/`label`/`organization` values inside `authStatus` → placeholders; the session working directory → `<SESSION_CWD>`; the scratch directory → `<SCRATCH>`; the home directory → `<HOME>`. `availableCommands` arrays were truncated to their first 5 entries with an explicit `_fixtureTruncated` marker, because the remainder listed this machine's locally installed commands and skills. The fixtures were grepped for `token`, `key`, `sk-`, `Bearer`, `secret`, `password` and `credential`; the only matches are token *counts* and Codex's `authMethods` entry literally named "API Key". No credential value was ever read, printed or stored.

## Open questions this probe did not answer

- Whether either adapter emits `plan` updates, and under what prompt. Neither did here.
- Whether `_meta.steering`, `_session/goal` or the `jetbrains.air` subagent dialect work as advertised; none were called.
- Codex's actual approval boundary (an out-of-workspace write, or a network access, was not attempted).
- Behavior when the client *does* advertise `fs` and `terminal` capabilities — in particular whether Codex then routes its `exec_command` output through `terminal/*` instead of `_meta.terminal_output_delta`.
- `session/load`, `session/resume` and `session/fork`, all advertised by both and all untested.
- Whether a cancel mid-tool-call is as prompt as a cancel mid-reasoning.
- Which concrete `claude`/`codex` binary each adapter executed.
