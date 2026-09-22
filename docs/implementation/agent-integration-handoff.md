# Agent integration handoff — 2026-09-22

**Start here for the next agent.** This records the user's latest accepted
direction after reviewing the ACP prototype and the UCF direct-CLI code.
The original handoff was documentation-only; the continuation checkpoint below
records subsequent runtime work. Earlier delivery/recovery implementation and
validation remain part of the baseline: preserve them. This handoff takes precedence over the earlier live-validation handoff for
what to do next; earlier reports remain evidence of what ran.

## Latest accepted direction: own Go adapters — 2026-09-22

**Subsequent implementation:** start with the [built-in Go bridge checkpoint](go-adapter-checkpoint.md)
and [ADR 0016](../adr/0016-built-in-go-acp-bridges.md). The external-adapter-only
status below is historical; new evidence and remaining gaps are recorded there.

**This supersedes the earlier adapter-reuse recommendation and open ownership
decision below.** The user tried a new empty `test-tui-go` project and found
both agents unavailable. The running server reported that `claude-agent-acp`
and `codex-acp` were missing from PATH; the official Claude/Codex CLIs were
installed. The user rejected requiring separate adapter installations:
“I don't want extra stuff installed … I want to create our own in golang.”

The selected next implementation is **our own Go ACP adapters, shipped with
the Go application**, around the user's installed official runtimes:

- Claude: direct installed `claude -p` with its versioned JSONL/control host
  protocol. Do not require the npm ACP adapter or a separate Node/Agent SDK
  sidecar. The SDK types and existing adapter are reference evidence, not a
  runtime dependency for the new Go bridge.
- Codex: direct installed `codex app-server`, translated to ACP in Go. Do not
  substitute `codex exec` or require the npm Codex ACP adapter.
- Keep ACP v1 at the server-agent boundary, including native ACP agents. Keep
  one app-owned question/answer contract and distinct approvals. This is an
  adapter implementation change, not permission to put provider protocols in
  the TUI or bypass the common boundary.
- Deliver adapters with the application so the user does not install/manage
  extra translator executables. Exact package/process organization is for the
  next slice; internal subcommands of the same Go binary are a candidate, not
  yet a selected design. Existing Go build dependencies are distinct from
  separately installed end-user runtimes.
- Discover installed `claude`/`codex` automatically on the server's PATH;
  preserve validated explicit local overrides. Never download or fall back to
  bundled provider runtimes. Keep authentication/configuration with those
  official installations and preserve the personal-local-prototype scope.

**Status at the ownership decision (superseded by the checkpoint above):** no custom Go provider bridge existed yet. That
implementation still expected the external npm ACP adapters, so the user's
unavailable state was not fixed by the documentation change. No adapter package,
symlink or local tool directory was installed during troubleshooting; only
read-only diagnostics and package/source inspection ran before the user stopped
that approach. Do not resume the proposed installation. Earlier scratch test
dependencies remain historical evidence; do not remove or depend on them for
normal application execution. Preserve the user's project and running server.

### Next-agent implementation sequence

1. Read the accepted ADRs and **all current uncommitted implementation first**,
   especially `agent/runtime.go`, `agent/session.go`, server dispatch/recovery,
   approval/question handling, and the latest linked regression reports. Keep
   the durable-before-dispatch boundary, retained captures, exact option IDs,
   uncertain delivery, deduplication and no-replay migration fixes.
2. Use the completed versioned comparisons and UCF as read-only references.
   Adapter ownership is now decided; do not repeat the reuse-versus-own debate.
   Recheck the installed official CLI versions and required control schemas.
   Document the narrow Go bridge packaging/ownership choice before implementing.
3. Implement one small provider slice at a time behind ACP, then the second.
   Bound framing/output, preserve connection/session/turn/request identities,
   discover actual settings, and test exact permission/question responses,
   cancellation and process ownership. Do not silently weaken capability or
   permission behavior to make a basic prompt work.
4. Claude's native question/approval host requires actual correlated control
   responses; plain print output and UCF's turn-ending question blocks are not
   sufficient. Codex's bridge must preserve `turnId`, `itemId`, `isBlocking`
   and timeout semantics, and must never turn failed Steer into a new turn.
5. Define/version our adapter identity and question dialect explicitly. The
   current Claude gate accepts only third-party `0.80.0`; a new bridge must not
   impersonate that adapter or claim its live evidence. Reuse the normalized
   application schema/UI, not a second answer API. Do not promote callback/pipe
   writes or generic resolved/turn events into authoritative answer receipts.
6. Preserve fixtures and existing user work. Run `make check`, relevant PTY
   checks, and isolated live tests with only the application and installed
   provider CLIs available—no npm adapters on PATH, no external SDK sidecar,
   no automatic installation. Record actual executable versions, settings,
   prompt/streaming, questions/approvals, Stop/Resume, restart and cleanup limits.
   Remove the external-adapter requirement/install hints only when the new
   route actually replaces it. Unsupported functionality must remain explicit.

UCF remains read-only. Its turn-ending question convention is neither native
nor continued-work async delivery. Fallback still requires queue/writer
admission and correlation; true async support still needs live evidence.

## Earlier continuation checkpoint — 2026-09-22

**Local runtime follow-up:** the user requires their installed Claude/Codex
CLIs, never bundled defaults. Both probe and thread launches now discover the
CLI on the server's PATH and explicitly pass it to the adapter. Missing or
invalid local executables fail before adapter startup. Runtime-path environment
variables are optional overrides, not required setup. See
[discovery validation](../research/local-runtime-discovery-2026-09-22.md).

**Latest:** the [dispatch/recovery continuation](agent-recovery-checkpoint.md)
records the user's personal-local-prototype scope, rechecked auth/comparison,
selected adapter reuse, durable prompt/approval admission and failed-prompt
replay correction. [Fresh live evidence](../research/agent-recovery-live-2026-09-22.md)
now covers Claude native questions and Codex prompt/cancel/restart checks.
The paragraphs below preserve the earlier checkpoint; their no-new-live-run
and live-question-unknown statements are superseded only by that evidence.

The [approval delivery slice](request-delivery-checkpoint.md) now corrects the
optimistic `acp-delivered` behavior described below. `request.answer` durably
accepts exact option IDs, checks the live connection/session/turn callback,
and records response preparation without claiming upstream confirmation.
Restart retains submitted answers as uncertain and never replays them.
The TUI preserves fixture compatibility and no longer labels old ACP delivery
markers confirmed. The subsequent bounded [native-question slice](native-question-checkpoint.md)
adds pinned Claude forms; other ACP question dialects remain unsupported.

The versioned [Claude comparison](../research/claude-integration-matrix-2026-09-22.md)
and [Codex comparison](../research/codex-integration-matrix-2026-09-22.md)
are complete as source/schema research. They recommend retaining the existing
adapters for the baseline, with focused fixes before broader questions/steering.
Claude already translates `AskUserQuestion` to form elicitation; Codex's pinned
adapter loses blocking/turn metadata and can change Steer into a new turn.
Neither is evidence for true asynchronous questions. No new live provider
prompts ran. Final distribution/authentication eligibility and adapter patch
ownership remain open; do not silently change billing.

Current: bounded Claude 0.80.0 native elicitation uses the existing normalized
question UI and `request.answer`, with fake-peer and adapter-converter evidence.
Next: validate live provider questions and establish authoritative receipt evidence;
expand supported shapes only with explicit semantics and tests.
Codex metadata/timeout fixes and a receipt/withdrawal contract precede exposing
that path. Fallback still depends on explicit scheduling and writer admission.
This continuation supersedes the implementation-gap descriptions below where
noted; earlier live evidence remains historical.

## Read first

1. Repository `AGENTS.md`, [brief](../design/brief.md),
   [interview](../design/interview.md#agent-boundary-and-question-normalization--2026-09-22).
2. [ADR 0014: official runtimes behind ACP](../adr/0014-acp-boundary-official-agent-runtimes.md)
   and [ADR 0015: one question contract](../adr/0015-app-owned-question-contract.md).
3. [Integration evidence and UCF findings](../research/agent-integration-options-2026-09-22.md).
4. [Questions](../design/questions.md), [client protocol](../design/client-protocol.md),
   [thread settings](../design/thread-configuration.md),
   [activity](../design/activity.md), [workspace scheduling](../design/workspaces.md).
5. [Existing implementation checkpoint](acp-checkpoint.md),
   [continuation validation](../research/go-acp-2026-09-22.md), and only then
   [earlier live-run details](acp-live-validation-handoff.md).

## Accepted direction

- **ACP v1 remains the common server-to-agent boundary.** Native ACP agents
  connect directly; adapters translate other official agent interfaces. TUI
  and future web frontends use the application API. Do not move raw ACP or
  provider control messages into frontend code.
- **Claude:** our Go adapter hosts the installed official `claude -p` runtime
  directly. Earlier SDK-backed reuse was superseded by the no-extra-install
  requirement above. Preserve native question/approval control semantics.
- **Codex:** use official `codex app-server` behind ACP. `codex exec` was an
  initial suggestion, superseded by this choice. It is an official interface,
  but version/experimental-feature compatibility still needs validation.
- **Adapter ownership:** selected in-house Go implementations for both providers,
  delivered with the application. Existing adapters remain versioned reference
  evidence; separate npm/Node adapter installs are not the target setup.
- **Questions:** one app-owned schema, question card and answer action across
  frontends. Prefer native structured delivery; allow a declared structured-text
  fallback whose answer starts an upstream turn. The server/adapter chooses the
  route, never the user. Preserve waiting versus continuing-work semantics.
- **Approvals:** distinct authorization requests with original action, targets,
  scope and provider-supported choices. Never implement them as ordinary Q&A or
  replace unanswered approvals with automatic grants.
- **Authentication:** leave login with the official runtime. Preserve intended
  subscription use; do not collect/extract tokens, add our own subscription
  login, or silently select API-key billing. Record verified auth modes and
  unavailable states. Existing SDK/CLI calls alone do not establish permission
  to ship a particular subscription-backed integration.

## Current implementation and evidence

The Go server currently starts external `claude-agent-acp` and `codex-acp`
executables, with the pinned packages and overrides described in ADR 0013.
The community Coder Go ACP SDK is protocol plumbing, distinct from Anthropic's
Agent SDK. Relevant code:

| Location | Responsibility / gap |
| --- | --- |
| `apps/go/internal/agent/` | Process/ACP transport, normalization, settings, prompt capture; `session.go` gates pinned Claude elicitation; `elicitation.go` normalizes bounded AskUserQuestion forms |
| `apps/go/internal/server/agents.go`, `approvals.go`, `questions.go` | Probes, dispatch, cancellation, restoration and generation-bound ACP approvals/questions; neither callback return is confirmation |
| `apps/go/internal/server/commands.go` | Revisioned application commands; marks supported ACP approvals/questions submitted/`acp-accepted`; unsupported routes are rejected |
| `apps/go/internal/protocol/types.go` | Existing `Request`, `Question`, `Answer` and commands; retains normalized requests and accepted answers; source payload is stored privately, live callback/generation stays in server memory; child questions remain unavailable |
| `apps/go/internal/tui/agents.go`, `prompt_settings.go`, `render.go` | Dynamic agent/settings and presentation; preserve fixture behavior and generic rendering |
| `apps/go/internal/server/acp*_test.go`, `internal/agent/*_test.go`, `internal/tui/acp_agents_test.go` | Existing fake-peer/normalization/server/TUI coverage |
| `apps/go/scripts/pty_acp.py` | Opt-in live ACP PTY harness; not part of ordinary `make pty` |

**Preserve the delivery-state correction for every request type.** Supported
native questions share the approval acceptance/handoff lifecycle; unsupported
routes are rejected. A local durable answer or successful pipe write is not an
upstream resolution acknowledgment.
The old checkpoint documents this limit; do not build new Answered UI on the
literal `acp-delivered` name as if it were a verified receipt.

Earlier validation, preceding the native-question slice and its linked report:

- `make check` passed, including formatting, vet, staticcheck, race tests and build.
- Seven fixture PTY harnesses passed as detailed in the continuation report.
- Codex live PTY prompt, cancellation and Resume without prompt resubmission passed.
- Earlier HTTP runs recorded Claude/Codex replies and Claude approval/file write.
- Claude's later live TUI reply/approval check was blocked by its account limit.
  Readiness/model discovery is not a successful prompt. Do not assume the old
  reset time or quota condition is still current.
- No general native questions, true async answers, real child-history parity,
  real steering, MCP/image parity, live `session/load`, or multi-client provider
  recovery is established by those results. Answered Q&A history is not implemented.
- Per-checkout writer scheduling remains unimplemented. Successful isolated
  writes do not establish safe concurrent writers. One shutdown timeout's cause
  remains unproven; process exit checks covered direct adapter children.

## UCF is a reference, not a drop-in implementation

Inspect `~/Developer/git/github.com/muschterm/ucf/server` and the corresponding
`studio/web/src` under that checkout, following its own repository instructions.
It is outside this repository and should remain unchanged unless separately
requested. The [evidence note](../research/agent-integration-options-2026-09-22.md#ucf-source-inspection)
lists exact files/functions.

Its Claude runner supplies persistent JSONL transport, images, resume and a
control-message interrupt. Its question UI parses a prompted `ucf-question`
fence; the agent ends a turn and receives the answer in a later turn. It does
not implement native Claude human approvals. Its Codex App Server client also
has a deliberately different request policy. Preserve our explicit Submit,
native-first answers, provider-supported permissions, discovered settings,
failure/cancel outcomes and request-specific server authority. Do not introduce
a runtime dependency on the UCF checkout or assume code may be copied without
checking its license/provenance.

## Work sequence

**Historical sequence:** the new Go-adapter sequence at the top takes priority.
The lifecycle, question, scheduling and validation requirements below still
apply; earlier recommendations to retain or patch external npm adapters no
longer determine implementation ownership.

The [native-question checkpoint](native-question-checkpoint.md) supersedes the
original next-step items below for the bounded Claude route. The dependency
comparison was extended before implementation; the existing adapter is retained,
with no final direct-Go-versus-SDK distribution decision. Preserve the HTTP drain
fix, late-view-write admission gate and stage-specific stop diagnostics described
in [shutdown evidence](../research/go-shutdown-2026-09-22.md).

### 1. Establish the versioned capability and authentication matrix

Compare these candidates against the same scenarios:

| Provider | Candidates |
| --- | --- |
| Claude | Pinned existing ACP adapter using Agent SDK; our ACP bridge around official `claude -p`, informed by UCF; a thin SDK-backed bridge if the existing adapter has remediable gaps |
| Codex | Pinned existing `codex-acp`; our ACP bridge around official App Server, informed by UCF |

Record exact adapter/SDK/CLI versions and the executable actually launched.
Verify use of an explicitly selected installed runtime, startup/config loading,
auth inheritance and billing mode without logging credentials. Recheck primary
authentication/product guidance; separate technical success from permitted use.
If the intended SDK subscription use is still ambiguous, make that a concrete
open decision for the user, not a reason to silently adopt a billed alternative.

For each candidate, distinguish **documented**, **implemented**, **fake-peer
tested**, **live verified**, **unsupported**, and **unknown** for: configuration
discovery/effective settings; text/thought/tool streaming; images/context; native
questions; approvals and their exact choices; turn-ending question fallback;
true asynchronous questions; Stop/Resume; same-turn steering; child identities
and history; usage/quota telemetry; process cleanup and reconnection. Do not
equate transport support with all these features. Inspect the pinned release,
not just current upstream README claims.

Produce a short comparison and record the selected implementation with its
maintenance cost and verified limits. Do not make the UI depend on the result.

### 2. Specify and implement the normalized request lifecycle

Extend the existing application contract rather than introducing a second
frontend answer API. `request.answer` is the current command; the earlier
discussion's `answerQuestion(...)` was conceptual, not a new endpoint decision.

Settle exact wire fields/migrations and write focused lifecycle tests before
connecting new provider paths. At minimum the server needs:

- Stable request/question/option identities, revision, original payload and
  validated answer shapes. Labels alone must not ambiguously identify duplicate
  options. Frontend drafts remain separate from accepted answers.
- Originating thread, turn, child where supplied, upstream session/connection
  generation and pending request identity; private routing details stay server-side.
- Separate execution mode (waiting, continuing, turn ended, unknown) and delivery
  route (native response, declared adapter fallback). Use actual capability data.
- Durable submission identity and answer snapshot; accepted, sent, confirmed,
  uncertain, failed and terminal-without-answer outcomes as supported by evidence.
  No optimistic success or duplicate writes when a receipt is lost.
- Competing-client/revision checks, withdrawal/cancel behavior, and explicit
  Resume revalidation after restart. Never attach an old request ID to a new
  provider connection merely because the thread/session name matches.

Prefer native questions and expose them through negotiated ACP support or a
documented versioned extension. Do not claim ACP v1 alone covers every question
shape, child source or async mode. Preserve approvals as a distinct request kind.

### 3. Implement the declared fallback without weakening the contract

For providers/routes lacking usable native questions, a structured-text
convention can create a server-owned request after a completed turn. Define a
bounded schema/parser and injection/detection rules. Partial, malformed,
duplicate, quoted/example or tool-output blocks must not accidentally become
new actionable requests. Preserve readable text if parsing fails.

Submit still targets that request. Persist the original question and accepted
answer; the adapter may format and dispatch a correlated continuation turn.
Keep it distinct from an ordinary queued prompt in storage and history. Do not
automatically convert a failed/uncertain native answer into this path.

**Scheduling must be settled before enabling this route:** capture the intended
continuation settings, reconcile an already queued/new turn and any child
target, honor Stop/explicit Resume and checkout writer admission, and define
late-answer invalidation. The fallback cannot bypass scheduling, consume the
composer draft, duplicate a prompt, or silently resume interrupted work. If safe
correlation cannot be established, retain the answer and show why delivery is
unavailable. This route does not satisfy the true async-question requirement.

### 4. Connect the existing UI and answered history

Use one question renderer for all supported routes; hide transport details from
normal user flows. Keep observable waiting/continuing state, supplied choices,
multiselect/free text, explicit Submit, drafts and keyboard/mouse paths.
Retain separate approval cards with source-supported scope and choices.

Implement the specified compact Answered Q&A history only after authoritative
resolution evidence. A fallback's upstream continuation remains linked to the
original Q&A; do not add duplicate user-visible questions/answers. Pending,
uncertain, withdrawn and cancelled requests must not read as answered.

### 5. Validate the complete selected path and update status

Keep isolated test application homes/checkouts. Preserve earlier recordings;
add redacted fixtures and a dated report distinguishing live provider tests from
fixture/UI checks. At minimum cover:

1. Native and declared fallback questions use the same UI; answers reach the
   intended request/turn/child, not another queued prompt. Invalid and stale
   answers are rejected without losing drafts.
2. Single/multiple questions, choice/free text/multiselect, duplicate labels,
   explicit submission and bounded malformed-stream handling.
3. Approve/deny using exact supported options, cancellation while waiting, no
   implicit approval when disconnected, and no expanded permission scope.
4. Two clients racing, duplicate submission, lost acknowledgment, process death,
   disconnect/reconnect, restart and explicit Resume without blind replay.
5. Fallback answer versus queue dispatch/Stop/settings races, and confirmed
   chronology without duplicate Q&A entries.
6. Claimed async mode: useful work continues while the question is pending and a
   later answer reaches that same supported context. Otherwise label unavailable.
7. Real prompt, streaming, settings, Stop/Resume and owned-process shutdown for
   both selected providers; no success inferred from process start or final-looking text.
8. TUI keyboard/mouse, narrow/short layouts, retained prompt/request drafts and
   attention indicators; keep existing fixture regressions green.

From `apps/go`, run `make check` and applicable PTY checks after code changes.
`make check` already includes the build/race suite; do not rerun broad checks
without a new reason. Live harness details are in the earlier report and
`scripts/README.md`; live requests consume the configured account allowance.
No live requests were made by this documentation task.

Update ADR selection details, capability evidence, checkpoint, README and
`go-slice.md` to match what actually passed. Report unresolved gaps explicitly;
do not mark the whole ADE complete after a successful prompt.

## Workspace and process hygiene

Inspect `git status` and scoped instructions before edits. Do not reset, stash
away or overwrite the uncommitted ACP/TUI work. No commit, pull or push was
performed by this handoff task. Repository rules prohibit pull/push; do not
assume a new agent was asked to commit merely because it was asked to implement.

Earlier sessions reported personal servers using `~/.tui-go` and orphaned test
servers under `/private/tmp/tui-cap-home-*`. Their current existence/ownership
was not rechecked here. Do not kill processes by broad name/pattern or touch
personal storage. Start only isolated owned test servers, track their identities,
and clean up those runs. Temporary adapter/venv paths in old notes may no longer
exist; discover rather than assuming them. Keep credentials and private paths
out of retained wire fixtures, logs and captures.

## Suggested prompt for the next agent

> Continue from the latest Go-adapter decision in
> `docs/implementation/agent-integration-handoff.md`. Read the accepted ADRs and
> current uncommitted implementation first. Implement our own Go ACP adapters,
> shipped with the application, around installed `claude -p` and
> `codex app-server`. No separate npm/Node adapter or Agent SDK sidecar installs,
> bundled provider runtimes, downloads or billing changes. Keep ACP at the
> server-agent boundary and one app-owned question/answer contract. Preserve
> existing work, fixtures and durable delivery/recovery/no-replay guarantees.
> Use the versioned comparisons and UCF as read-only references. Implement
> small vertical slices, run appropriate checks, and record live evidence and
> remaining gaps. Never infer native/async questions from UCF's turn-ending
> convention or authoritative receipts from successful writes/turn completion.
