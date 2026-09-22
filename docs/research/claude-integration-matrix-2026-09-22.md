# Claude runtime and adapter comparison — 2026-09-22

**Latest live continuation:** [personal-prototype evidence](agent-recovery-live-2026-09-22.md)
records official-login eligibility for the user-confirmed scope and native
question/Stop/restart results with explicitly selected Claude 2.1.280. Earlier
version/source-only statements below retain their original inspection scope.

Research recommendation, not an additional accepted ADR. Read with
[ADR 0014](../adr/0014-acp-boundary-official-agent-runtimes.md),
[ADR 0015](../adr/0015-app-owned-question-contract.md) and the
[implementation handoff](../implementation/agent-integration-handoff.md).

## Scope and evidence labels

Inspected the installed **0.80.0** npm distribution, its **0.3.278** Agent SDK
dependency and official documentation on 2026-09-22. Only executable resolution
and `--version` ran; no provider prompt, login, account inspection or credential
read was performed. No new live inference evidence is claimed. The earlier
[live probe](acp-live-probe-2026-09-22.md) and
[continuation report](go-acp-2026-09-22.md) remain the live evidence.

- **Documented:** official documentation or pinned SDK public type contract.
- **Implemented:** inspected executable distribution/source contains the route.
- **Fake-peer tested:** earlier repository synthetic transport checks, not provider behavior.
- **Live verified:** explicitly covered by an earlier recorded provider run.
- **Unsupported:** absent/disabled for the particular route described.
- **Unknown:** no sufficient evidence, including a proposed bridge not yet built.

These labels are cumulative rather than a single maturity score. An implemented
adapter feature is not necessarily implemented by our Go client.

## Versions and runtime selection

| Item | Observed evidence |
| --- | --- |
| Existing adapter | `@agentclientprotocol/claude-agent-acp` **0.80.0**, bin `dist/index.js` |
| Adapter dependencies | `@agentclientprotocol/sdk` **1.5.0**, `@anthropic-ai/claude-agent-sdk` **0.3.278**, `zod` **4.6.5** |
| SDK native package | `@anthropic-ai/claude-agent-sdk-darwin-arm64` **0.3.278**, optional dependency |
| Bundled executable | Imported adapter `claudeCliPath()` resolves that native package's `claude`; running the resolved executable with `--version` returned **2.1.278 (Claude Code)** |
| Installed executable | `~/.local/bin/claude --version` returned **2.1.278 (Claude Code)** |
| Explicit selection | Setting `CLAUDE_CODE_EXECUTABLE` to the installed executable in the resolver process returned that exact path; `createQuery` passes it as `pathToClaudeCodeExecutable` |

Reproducible source locations within the pinned npm package: `package.json`;
`dist/acp-agent.js:377` (`claudeCliPath`), `:6039` (environment), `:6059`
(settings sources), `:6098` (SDK executable option). The installed package was
found under the earlier probe's temporary `scratchpad/adapters/node_modules`;
this temporary installation is not a repository dependency or deployment path.

The resolver prefers the explicit environment override, otherwise the SDK's
platform-specific optional native package. It does **not** choose PATH's
`claude` by default. Missing optional dependencies produce an error with an
override/reinstallation hint. The resolution and version check establish today's
selected executable; they do not retrospectively identify the child executed
in earlier live traces, nor test an entire SDK query with the override.

The adapter passes `settingSources: ["user", "project", "local"]`, checkout
`cwd`, partial-message streaming, and inherited environment plus explicit
per-session/provider overrides to the SDK. A bridge must preserve deliberate
configuration loading and report routing/auth failures. Configuration presence
does not prove settings became effective. No private configuration values were
read during this inspection.

## Comparable capabilities

The direct-CLI and thin-SDK columns describe candidate implementation work,
not new implementations in this repository. UCF evidence is the previously
recorded [read-only inspection](agent-integration-options-2026-09-22.md#ucf-source-inspection);
no UCF code was copied or modified for this comparison.

| Scenario | Pinned ACP adapter + SDK | Direct `claude -p` ACP bridge | Thin SDK ACP bridge |
| --- | --- | --- | --- |
| Settings discovery/effective values | **Implemented; live verified** model/config discovery and selected changes in earlier probe; not universal enforcement evidence | Flags **documented**; UCF's static aliases do not establish dynamic discovery. Our bridge **unknown** | `supportedModels`, `setModel`, `setPermissionMode` **documented** in pinned types; ACP mapping **unknown** |
| Text/thought/tool streaming | **Implemented; live verified** recorded text/tool output; synthetic normalization tests also exist | JSONL/partial output **documented**, UCF translation **implemented**; our bridge **unknown** | SDK stream types **documented**; custom translation **unknown** |
| Images and captured context | **Implemented** ACP image conversion; live images **unknown** | UCF image-bearing input **implemented**; end-to-end parity **unknown** | SDK message content **documented**; snapshot ownership remains app work |
| Native questions | **Implemented** `AskUserQuestion` to form elicitation; Go route was **unsupported** at this inspection checkpoint; live **unknown** | Native permission-host route **documented**, but complete question transport/correlation in proposed bridge **unknown**; UCF native questions **unsupported** | `canUseTool` + `updatedInput` **documented**; proposed ACP mapping **unknown** |
| Approvals | **Implemented; live verified** one earlier file approval/write. Exact option set depends on action and supplied updates | MCP permission host/hooks **documented**; UCF human approvals **unsupported**; our mapping **unknown** | Callback allow/deny and permission updates **documented**; our mapping **unknown** |
| Declared turn-ending question fallback | App parser/scheduling route **unsupported** at checkpoint | UCF convention **implemented**; app correlation, explicit Submit and scheduling **unknown** | Can send a later user turn; app fallback route **unknown** |
| True asynchronous questions | **Unsupported** for documented blocking `AskUserQuestion`; continued-work delivery **unknown** | UCF fallback **unsupported** as true async; another route **unknown** | Blocking callback **documented**; same-context useful-work continuation **unknown** |
| Stop / Resume | **Implemented**, earlier cancel evidence; live Claude load/reconnect **unknown** | CLI signals/resume **documented**; UCF interrupt control **implemented**, receipt correlation incomplete | SDK interrupt/resume **documented**; app recovery **unknown** |
| Same-turn steering | Private extension **implemented/advertised**; repository's exact upstream-turn semantics **unknown**, see below | Streaming stdin alone insufficient; our implementation **unknown** | `submitMessage`/stream controls exist; compliant app semantics **unknown** |
| Child identity/history | `native-subagents.js`, task routing and parent tool IDs **implemented**; complete live history **unknown** | UCF partial child translation **implemented**; parity **unknown** | Parent IDs/task events **documented**; complete history normalization **unknown** |
| Usage / quota | Usage updates and `_claude/rateLimit` **implemented**; earlier context/cost **live verified**, full quota parity **unknown** | Result usage **documented**, UCF translation **implemented**; full quota **unknown** | Usage/account/rate-limit types **documented**; mapping and applicability **unknown** |
| Process cleanup / reconnection | Go fake-peer lifecycle checks **fake-peer tested**; earlier direct adapter child reaping **live verified**; descendant cleanup and multi-client recovery **unknown** | Signal/process-tree behavior **documented**; proposed bridge recovery **unknown** | SDK close/interrupt **documented**; persistent app receipts/recovery **unknown** |

### Native question route already exists

`dist/acp-agent.js:5295` routes `AskUserQuestion` through
`handleAskUserQuestion` (`:5431`) when the client advertises
`elicitation.form`; otherwise the tool is explicitly disallowed (`:5975`).
`dist/elicitation.js` supplies indexed `question_N` properties, enum choices,
multiselect arrays, and per-question custom answer fields. Requests retain
`sessionId` and supplied `toolCallId`. Accept converts to SDK `updatedInput`;
decline skips with empty answers; cancel aborts the tool. A failed presentation
denies rather than synthesizing an answer. These are source facts, not a
successful live question test.

Two important normalization limits remain: enum values are option **labels**,
and the returned SDK answer map uses **question text** as keys. Duplicate labels
cannot identify distinct upstream options; repeated question text overwrites an
earlier answer. Application-generated IDs must not pretend to remove that
provider limitation. Reject ambiguous actionable forms without destroying the
original payload, or establish an explicitly tested alternative.

The official [user-input guide](https://code.claude.com/docs/en/agent-sdk/user-input)
documents a paused execution callback, 1–4 questions with 2–4 choices each,
and no `AskUserQuestion` in Agent-tool subagents. These restrictions do not
establish true async questions or child-question parity. A resolved callback is
also not proof that the application durably observed upstream completion.

### Approval and steering constraints

`dist/permissions/options/shared.js` defines `allow-once`, `allow-with-updates`
and `reject`; specialized filesystem/shell/skill/exit-plan branches generate
other action-specific options. Preserve each incoming ID, label, scope and
supplied permission update. Never fabricate a permanent grant from a once-only
choice. A future SDK bridge would need to own this policy-sensitive translation.

`dist/acp-agent.js:1697–1711` explains that urgent steering aborts the current
SDK generation cycle and runs another, holding the ACP turn until SDK idle.
While awaiting permission/elicitation it queues delivery with lower urgency.
Therefore `_meta.steering.supported` is not by itself evidence for our strict
same-upstream-turn requirement. Keep Steer unavailable until turn identity and
delivery semantics pass explicit checks.

## Authentication, startup and billing

The current [billing notice](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)
still says the proposed June 15 change is paused: SDK, print-mode and third-party
usage continue drawing from subscription limits. The old credit policy below
that notice is historical, not the active policy. Technical login inheritance
does not independently establish permitted product use.

Rechecked [legal/product guidance](https://code.claude.com/docs/en/legal-and-compliance)
distinguishes ordinary individual Claude Code/SDK use from product integration.
End users may sign into the unmodified binary themselves, including on a
platform. A product running it must accept Commercial Terms, preserve all built-in
authentication methods and avoid reselling/intermediating usage. Developers may
not collect subscription credentials or provide their own Claude login.
The [SDK quickstart](https://code.claude.com/docs/en/agent-sdk/quickstart)
separately directs third-party products to API/cloud authentication unless
approved to offer Claude login or plan limits.

**Eligibility conclusion:** the user's ordinary personal use of their existing
official CLI login is supported by this guidance and the billing notice; this
does not establish approval to market a distributed SDK product with subscription
access. Our inference is that a user-run local host is closer to the permitted
unmodified-runtime case, but the SDK product wording does not explicitly resolve
it. The open product question is whether distribution remains a user-installed
client controlling each user's runtime (no login, credential handling or resale),
or offers an SDK-powered service/subscription entitlement. Confirm that scope
and its applicability with Anthropic before advertising subscription eligibility.
No paid authentication alternative is selected here.

Both routes can retain the official runtime's login. A thin SDK bridge does not
need our own token storage. Neither `-p` nor the SDK independently changes the
billing agreement. This inspection did not fetch account/quota state.

The official [headless guide](https://code.claude.com/docs/en/headless) documents
configuration auto-loading without `--bare`; bare mode skips ordinary context
and subscription credentials. Therefore adopting bare mode for repeatability
would change this task's authentication/configuration behavior. The guide also
distinguishes SIGINT/SDK interrupt (end turn) from SIGTERM (unfinished turn that
resume can continue). Final-looking text or process exit must not substitute
for outcome reconciliation. The [CLI reference](https://code.claude.com/docs/en/cli-reference)
documents stream input/output and the permission-prompt-tool option; it does
not establish an implemented ACP bridge here.

## Recommendation and maintenance cost

**Prefer retaining pinned 0.80.0 for the next native-question slice.** Its existing
form route removes the main reason to rewrite Claude translation. Add bounded
Go elicitation handling and truthful request delivery/recovery state, negotiate
only supported shapes, and verify explicit executable selection in an isolated
end-to-end test. This is a research recommendation, not an accepted SDK/CLI
selection or authorization to change authentication.

Reuse costs include a Node sidecar and pinned SDK/native distribution, tracking
ACP elicitation/private metadata versions, and regression tests for adapter
upgrades. A thin SDK bridge avoids reimplementing SDK transport but still owns
permissions, settings, messages, child histories, usage and ACP lifecycle. A
direct Go bridge avoids Node but additionally owns JSONL controls, SDK-equivalent
correlation and upstream protocol churn; the inspected UCF path does not close
the approval/native-question gaps. Neither custom route is justified by a
demonstrated inability of 0.80.0 to carry ordinary native questions.

Validation for this documentation change: inspected pinned source and package
metadata; invoked resolver and both version checks; rechecked primary guidance;
checked local links and whitespace. No provider prompts, UCF changes, application
test run, commit, pull or push were performed by this research subtask.

## Follow-up: fewer dependencies with a direct Go bridge

**A Go ACP bridge around the installed official CLI is a credible dependency
reduction, but needs a permission/control host, not just a JSONL output parser.**
It removes the external Node runtime, JavaScript ACP SDK, Agent SDK and Zod from
this route. It can also avoid installing a second native Claude distribution;
the existing adapter already supports selecting the installed binary, so that
last saving does not require a rewrite. The Go ACP transport and official Claude
runtime remain. No package-size or deployment-size measurements were taken.

This follow-up re-read the pinned distributions above and ran installed
`claude --help`; it made no provider requests or account/configuration reads.
The following separates available building blocks from an implemented bridge:

| Concern | Evidence and work owned by a direct Go bridge |
| --- | --- |
| Native questions | SDK **0.3.278** `sdk.d.ts` exposes `SDKControlRequest`/`SDKControlResponse`, `can_use_tool`, tool/agent IDs and request cancellation. `AskUserQuestion` answers use `updatedInput`; the existing adapter already translates this into ACP forms. A Go host could implement this protocol, but its initialization, question delivery and cancellation are **unverified**. Plain print output and UCF's prompted question block do not provide this route. |
| Exact approvals | The same pinned types define allow/deny, optional updated input and permission updates, including rule/mode/directory changes and destinations `userSettings`, `projectSettings`, `localSettings`, `session`, `cliArg`. `allow-once`, `allow-with-updates`, `reject`, skill and exit-plan IDs are **adapter choices**, not raw CLI option IDs. A replacement must construct truthful choices from the supplied action and updates and retain scope; it cannot interpret ACP `allow_always` as universally permanent. |
| Approval restrictions | Pinned `SDKControlPermissionRequest` includes `suppress_always_allow_rule`, `default_to_no`, `matched_ask_rule`, `requires_user_interaction`, reason and blocked-path fields. A custom host must preserve these restrictions and sanitize supplied display text. Their presence in types does not prove that the current adapter preserves every field. Audit both paths before claiming full approval parity. |
| Streaming and settings | Installed help confirms stream input/output, partial messages, child-text forwarding, explicit model/effort, setting sources and resume flags. Pinned control types also describe model catalog/settings requests. Those are building blocks for discovery and acknowledgment, not evidence that arbitrary selected settings took effect. Help warns that invalid settings are silently ignored in print mode. Preserve checkout `cwd` and intentional configuration sources; do not use `--bare` as an equivalent startup. |
| Cancellation and recovery | Pinned types define request-ID withdrawal and capability-gated interrupt receipts (`still_queued`, `cancelled`). Initialization can report pending permission requests, but its comments explicitly warn that inherited prompts may be absent. Neither a successful pipe write nor an empty pending list establishes durable resolution or safe answer replay. The application still owns generation/turn correlation, uncertain delivery and explicit Resume. |

Pinned source anchors for reproduction: SDK `sdk.d.ts` lines 324–354
(initialization/recovery caveat), 2406–2454 (permission results and scopes),
3677–3683 (withdrawal), 4347–4369 (interrupt), 4494–4540 (permission request),
4685–4699 (control envelope), and adapter
`dist/permissions/options/shared.js` (option IDs and update validation).
These versioned type contracts make direct implementation more concrete than
reverse-engineering terminal output; they do not promise compatibility across
future CLI versions or verify an independent Go implementation.

The documented alternative is an MCP permission tool. However, the
[CLI reference](https://code.claude.com/docs/en/cli-reference) says it cannot
approve MCP tools requiring user interaction. Its existence does not establish
complete native-question parity. The documented SDK
[question callback](https://code.claude.com/docs/en/agent-sdk/user-input) already
returns answers through the permission callback; retaining its semantics in Go
requires implementing the host side and testing it. No true async-question or
same-turn steering claim follows.

Cleanup also has observable semantics: official
[headless documentation](https://code.claude.com/docs/en/headless) distinguishes
SIGINT ending a turn from SIGTERM leaving it unfinished for resume. SIGTERM
terminates running Bash process trees and runs SessionEnd hooks; SDK close first
ends input, which cancels a pending permission prompt. A Go replacement must
choose and test shutdown ordering, bound output draining, and reap owned
processes. This research did not verify descendant cleanup or resumed pending
requests for either route.

**Recommendation remains reuse for the next question slice.** If removing Node
is a product requirement, evaluate a narrowly scoped Go host using pinned
control fixtures before replacing the adapter. Its acceptance gate is native
question/approval round trips, exact scopes, withdrawn/stale responses, settings
acknowledgment, Stop with queued input, explicit recovery and descendant cleanup.
The saving is distribution dependencies; the cost is owning both ACP translation
and Claude host-protocol compatibility. No runtime selection or authentication
decision changes in this follow-up. Validation: source/help/documentation review
and whitespace checks only; no provider prompts, installs or process launches
beyond help.

## Subsequent implementation evidence

The [bounded Go native-question slice](../implementation/native-question-checkpoint.md)
now implements the app side of the pinned form route. Its source restrictions,
converter fixtures and fake-peer lifecycle tests do not change the earlier
unknown live-provider or authentication-eligibility findings. The existing
adapter remains selected for this slice; no runtime dependency was added or
removed. See [validation](native-questions-2026-09-22.md).

## Independent recheck after the native-question slice

Rechecked on 2026-09-22 without inference, login, account/credential reads,
installation, UCF mutation or copying code. The existing capability matrix
remains a comparison of the pinned adapter and two **candidate** bridges;
the native-question row's Go-client checkpoint is superseded by the linked
implementation above. The next smallest slice is a live native-question
round trip through that implementation with an explicitly selected runtime,
plus conservative delivery/recovery fixes revealed by that test. A new bridge
would duplicate working form translation before establishing a missing feature.

The discovered scratch installation remains present. Read `package.json` for
all five packages in the version table, imported `dist/acp-agent.js` in Node,
called `claudeCliPath()` with the override absent and then set to
`/Users/muschterm/.local/bin/claude`, and invoked only `--version` on each
returned binary. All versions and resolver results match the table. Source
inspection reconfirmed `settingSources` at line 6059 and executable handoff at
6098. This proves resolution and direct version invocation, not which binary
an SDK query actually launches; capture that child identity in the live check.

SHA-256 fingerprints of the installed files inspected (package-relative paths):

| File | SHA-256 |
| --- | --- |
| Adapter `dist/acp-agent.js` | `16cd14aa02584dcbeb4b1b141eab5977c4c1a2d7521680c6601cf51879ba3dad` |
| Adapter `dist/elicitation.js` | `99897e1fdc6f89f671b5b14adc972d04beceaf5cb9f15e571fd01d36bf720822` |
| SDK `sdk.d.ts` | `1995b3a75ca82c6b202424f07a50955f2c66a2ce4f0c8badbfc5275889dddfd2` |

Re-read `handleAskUserQuestion` and its allow/deny/cancel conversion, plus the
SDK control union, `updatedInput`, approval restrictions and interrupt receipt
capabilities. The direct-Go dependency saving remains credible; these are
versioned protocol building blocks, not a tested independent Go host or a
promise that arbitrary runtime versions interoperate.

UCF was clean at inspected commit
`f3d3d462451fb29dc1db380891813ba047185741`. Its root `LICENSE` is MIT,
Copyright 2026 Matthew Muschter; any later substantial copying must retain its
notice. Read its `AGENTS.md`; no UCF MCP tool was available, so used the existing
`bin/ucf context map`, `bin/ucf doctor`, and
`bin/ucf context fetch --context ucf-server 0007` for read-only orientation.
Doctor reported the snapshot current and one map-budget warning. No repair,
corpus work or pending UCF handoff was undertaken: this task authorizes source
inspection only. An attempted `context map --context ucf-server` was rejected
as an unknown flag; it supplied no evidence.

Re-read UCF `server/runner/claude.go:388` (`claudeArgs`), `:591` (`Interrupt`),
`:226` (all result frames become completed) and
`server/server/sessions.go:1094` (`questionConvention`). The prior transport
findings still hold. Its retrieved server decision describes a wider permission
vocabulary, but that is not proof that this Claude runner implements human
approval transport. Its question convention deliberately ends a turn and returns
a label in the next user turn; it cannot establish native or true asynchronous
questions for this application.

Validation: metadata/resolver/version checks, bounded source and official-doc
review, local-link and whitespace review. No application tests were needed for
this documentation-only addition. No provider result, billing mode, live
permission/question success or runtime cleanup was newly verified here.
