# Codex adapter comparison — 2026-09-22

**Latest live continuation:** [personal-prototype evidence](agent-recovery-live-2026-09-22.md)
records successful explicit-runtime App Server 0.155.1 prompt, Stop/Resume and
restart checks using the existing login. This supersedes only the no-new-live-run
limits below; question metadata and steering defects remain unresolved.

Research recommendation, not a new adapter-ownership decision. Read with
[ADR 0014](../adr/0014-acp-boundary-official-agent-runtimes.md) and the
[handoff](../implementation/agent-integration-handoff.md). No provider prompts,
login changes, credential inspection, package installation or runtime changes
were performed. A later read-only authentication check is recorded below;
existing uncommitted work was preserved.

## Versioned evidence

Inspected the already installed npm package `@agentclientprotocol/codex-acp`
**1.12.0**, its shipped README, package manifest and readable bundled
`dist/index.js`. The latter's SHA-256 is
`f45a64dc3a994556ebdb688dc8d59b86945a9b2f940a3e3e545739dd265a7cc5`.
Source symbols and line numbers below refer to this exact bundle, not upstream
main. It lives beneath the earlier scratch install's
`adapters/node_modules/@agentclientprotocol/codex-acp`; the package identifies
[its source repository](https://github.com/agentclientprotocol/codex-acp).
The published package contains no separate test sources.

| Candidate | Exact runtime evidence and selection |
| --- | --- |
| Existing adapter | Manifest declares `@openai/codex: ^0.154.0`; installed dependency is **0.154.0**, confirmed by running its `bin/codex.js --version`. Pinning only the adapter does not lock that dependency across installs. |
| Explicit installed runtime | PATH resolves `~/.dotfiles.d/opt/codex/bin/codex`; `--version` reports **0.155.1**. A custom bridge can execute this resolved absolute path. No custom bridge exists here yet. |
| Adapter override | `startCodexConnection` (22098–22115) directly spawns nonempty `CODEX_PATH` with `app-server` on Darwin. Otherwise it resolves the dependency's JS launcher and runs it under the current Node executable. It does **not** default to PATH Codex. Windows uses a separate shell-launch branch, not validated here. |

The original comparison ran only version/help/schema commands. No App Server inference session was
started, so this is executable-selection **source evidence**, not a trace of
the binary in earlier live runs. Both exact runtimes generated experimental
JSON schemas into isolated scratch directories using
`app-server generate-json-schema --out <scratch> --experimental`.
Their `ToolRequestUserInputParams` both require `isBlocking`, `threadId`,
`turnId`, `itemId`, `questions`; `autoResolutionMs` is optional, defaults to null
and is deprecated in favor of `isBlocking`. Their
`ServerRequestResolvedNotification` requires `threadId` and `requestId`.
Schema generation passed; harmless sandbox PATH/cache warnings were emitted.

## Authentication and configuration

The adapter passes the inherited environment to App Server (22099). Runtime
help confirms normal Codex configuration loading; the adapter additionally
supports `CODEX_CONFIG`, `MODEL_PROVIDER` and explicit default-auth overrides
(35149–35170). Its config reads include project `cwd` and layers (28731), and
initialization opts into experimental App Server APIs (28202–28214).
Selecting an executable alone does not isolate its home, configuration or
credentials. Validate selected configuration and effective settings separately.

Official documentation distinguishes ChatGPT subscription authentication from
usage-based API-key authentication and documents shared cached CLI login.
App Server supports managed ChatGPT authentication and account-state discovery.
This supports retaining the user's official-runtime login; it does not justify
extracting credentials or silently switching billing. No account entitlement
or inference billing outcome was established by this comparison.
[Authentication](https://learn.chatgpt.com/docs/auth),
[App Server](https://learn.chatgpt.com/docs/app-server)

In the pinned adapter, `authenticateWithChatGpt` checks runtime account state;
API-key authentication is a separate explicit route using `CODEX_API_KEY`
before `OPENAI_API_KEY` (28219–28320). A custom bridge should leave login with
Codex and report the supplied mode without implementing token handling.
Do not enable adapter wire logging for credential-bearing traffic:
`attachLogs` records stdin/stdout and startup logging includes auth/config
objects (22117–22130, 35157–35164).

### Read-only authentication follow-up

Rechecked on 2026-09-22: the explicitly resolved PATH executable still reports
`codex-cli 0.155.1`. Its official `codex login status` returned exit code 0 and
reported **ChatGPT** authentication. The check retained only the recognized
mode and exit code; no credentials, account identifiers or plan details were
printed or stored. This is local login-state evidence, not a successful provider
turn or proof that a particular model/feature is entitled.

A separate owned `codex app-server` process exited before replying to
`initialize`, so the planned `account/read` with `refreshToken: false` was not
sent. A startup-only check returned exit code 1 with a permission-error marker.
The exact restricted resource was not diagnosed; this does not establish an
authentication failure. Both owned processes exited, and no thread was started.

Current official guidance documents subscription access through ChatGPT login
and separately billed API-key access, with workspace/role restrictions beyond
authentication. App Server documents Codex-managed ChatGPT auth separately from
host-managed external tokens. Retaining official-runtime login is technically
supported; this evidence does not authorize arbitrary public redistribution or
override workspace policy. No external-token implementation is needed here.
[Authentication](https://learn.chatgpt.com/docs/auth),
[App Server auth](https://learn.chatgpt.com/docs/app-server#auth-endpoints)

## Capability matrix

**D** documented; **I** implemented in inspected source/schema (schema alone
does not prove runtime behavior); **F** fake-peer tested in this repository's
earlier reports; **L** earlier live verification; **U** unavailable for the
current application path; **?** unverified. No new F/L tests were run here.
Custom means a proposed ACP bridge, not an implemented second integration.

| Scenario | Existing 1.12.0 adapter / current app | Custom bridge to 0.155.1 App Server |
| --- | --- | --- |
| Configuration / effective settings | D/I model, effort, mode and fast settings; F app normalization, earlier L discovery. `setConfigOption` mutates selected adapter state; do not treat that alone as running-value acknowledgment. | I upstream schema; bridge ?, must preserve discovered options and per-turn effective values. |
| Text, reported thoughts, tools, plan | D/I converters; L prompt/reply and Stop/Resume in earlier report; no blanket live parity. | I upstream event schema; bridge ?, requires bounded event translation. |
| Images / captured context | D/I adapter input conversion and modality guard; full app-provider path ?. | I image/local-image input schema; snapshot ownership still app responsibility; ?. |
| Native questions | I form elicitation translation; U app currently lacks that route. Question IDs retained, option labels are upstream values; arbitrary multiselect/duplicate-label distinction not established. | I experimental native request schema; bridge ?, can retain request/turn/item IDs and `isBlocking`. |
| Approvals | I command/file/permission/MCP mapping; F app permission plumbing. Earlier Claude approval evidence is not Codex live evidence. | I upstream approval schema; bridge ?, preserve offered decisions and scope. |
| Turn-ending Q&A fallback | U normalized fallback not implemented. | ? Requires bounded parsing, scheduling and correlated continuation; not supplied by transport. |
| Continued-work async questions | ? Adapter omits `isBlocking`; current app U. Async terminal tasks are a different feature. | I upstream `isBlocking` field; useful continued work plus later correlated answer still needs L evidence. |
| Stop / Resume | I; earlier L cancellation and Resume without prompt resubmission. Session-load/restart/multi-client parity ?. | D upstream interrupt/resume; bridge ?, preserve explicit Resume and no replay. |
| Same-turn steering | I `_session/steering`, but unsafe for our contract: can start a new turn. App U. | D/I `turn/steer` with expected turn identity; bridge ?, no new-turn substitution. |
| Child identity / history | D/I negotiated child sessions and history reconstruction, legacy tool fallback; app/live parity ?. | I upstream child events; bridge ?, needs subscription/replay and source/truncation handling. |
| Usage / quota | I `usage_update` emits last total tokens and model window; rate-limit snapshots retained and rendered in `/status`. Full typed quota/cost/freshness contract not established. | I upstream token/rate-limit schemas; bridge ?, retain units/source/scope rather than estimating cost. |
| Cleanup / reconnect | I stdin close then child termination after two seconds; earlier direct-child exit checks, one unexplained timeout; descendant/recovery parity ?. | ? Must own EOF, process exit, outstanding requests and connection generations. |

Earlier F/L evidence and its limits are recorded in
[continuation validation](go-acp-2026-09-22.md) and
[checkpoint](../implementation/acp-checkpoint.md). No provider-test result
is implied by schema generation or source inspection.

## Demonstrated contract gaps

1. **Question semantics are lost.** `handleUserInput` returns empty answers
   without negotiated `clientCapabilities.elicitation.form` (26319, 26648).
   `buildUserInputRequest` carries session/item and `autoResolutionMs`, but
   omits upstream `turnId` and `isBlocking` (26766–26833). `requestUserInputElicitation`
   waits indefinitely only for literal null; omitted `autoResolutionMs` becomes
   zero milliseconds (26666–26709). Both inspected schemas permit omission.
   That is a source/schema incompatibility requiring a focused regression test,
   not a claimed live failure.
2. **Resolution is not necessarily accepted-answer evidence.** The general
   event converter ignores `serverRequest/resolved` (24934). Elicitation
   handling uses it for URL completion at thread scope (26565–26577), not a
   complete native-question receipt contract. Official documentation says the
   same notification also accompanies cleanup after turn transitions or
   interruption. Correlate it with pending delivery and terminal outcomes;
   never render every such event as Answered.
   [Request lifecycle](https://learn.chatgpt.com/docs/app-server#toolrequestuserinput)
3. **Steer can become Send.** `performSteeringRequest` falls back to
   `startNewTurnFromSteering`, including a race where the active turn ends
   (33375–33444). A returned `startedNewTurn` outcome arrives after the side
   effect; frontend rejection is too late. Do not expose this method as our
   strict same-turn Steer. Official upstream `turn/steer` instead requires
   `expectedTurnId` and rejects absent active turns.
   [Steering](https://learn.chatgpt.com/docs/app-server#steer-an-active-turn)
4. **Permission labels cannot define scope.** Command decisions honor supplied
   `availableDecisions` (25670); absent values use compatibility defaults.
   File choices map to `accept`, `acceptForSession`, `cancel` (25812).
   Permission-profile choices include turn, turn with strict review, session,
   and refusal (25829). `allow_always` therefore does not universally mean a
   permanent grant. Preserve source decisions and metadata through the app.

## Recommendation and maintenance cost

Keep the pinned adapter as the prompt/approval baseline while implementing the
application request lifecycle. Prefer a focused upstream contribution or
maintained patch over immediately recreating its configuration, event,
permission, child-history and process translation. Reuse is conditional on
preserving request/turn/blocking metadata, exposing correlated withdrawal and
resolution, and providing strict steering without automatic new turns.
Pin the runtime dependency/override and test these behaviors before enabling
the affected features.

If those extensions cannot be made narrow and reviewable, a small independently
versioned ACP bridge to the official App Server is justified by the concrete
losses above. It must own JSON-RPC framing, schema compatibility, identity,
answer/approval correlation, cancellation, process cleanup and recovery;
existing adapter breadth is a real maintenance cost to replace. Neither path
already satisfies the full contract. Do not select custom ownership solely
because direct upstream schemas expose richer fields.

### Bounded next slice and evidence gates

Reinspection confirmed the same 1.12.0 bundle hash and both retained runtime
schemas above. Keep Codex native questions and strict Steer unavailable in the
application until their adapter boundary is repaired. A small patch should
preserve turn/request/item identity and `isBlocking`, treat omitted timeout as
unspecified rather than immediate expiration, and expose withdrawal distinctly
from accepted-answer confirmation. Test null/omitted/zero timeouts, cancellation,
late answers and lost response receipts with a fake upstream before a live
question run. A successful ACP callback or generic resolved notification still
cannot create an Answered card. Strict steering needs a separate regression
covering the turn-ending race with **zero** fallback `turn/start` calls.

Use an explicit `CODEX_PATH` and report the actual launched runtime in the live
harness; the override's source implementation is verified, but this follow-up
did not launch the adapter through that override. Repeat discovery and settings
checks for that exact pair. These are narrow reuse conditions, not selection of
a new bridge or a promise of asynchronous question support.

UCF was not reinspected beyond its root instructions in this follow-up: those
require connecting its MCP server before work, and neither that tool nor its
CLI was available in this harness. The earlier
[UCF evidence](agent-integration-options-2026-09-22.md#ucf-source-inspection)
remains explicitly earlier evidence. No UCF files were changed or copied.
