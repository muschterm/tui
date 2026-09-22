# Official agent runtimes and UCF implementation evidence

Recorded 2026-09-22. This is documentation/source inspection, not a new live
provider validation. Decisions are in [ADR 0014](../adr/0014-acp-boundary-official-agent-runtimes.md)
and [ADR 0015](../adr/0015-app-owned-question-contract.md); the
[handoff](../implementation/agent-integration-handoff.md) describes remaining work.

## Components and ownership

| Component | Role |
| --- | --- |
| `github.com/coder/acp-go-sdk` v0.13.5 | Community Go ACP protocol library used by this server; not Claude's Agent SDK or an inference service |
| `@agentclientprotocol/claude-agent-acp` 0.80.0 | Current prototype's external ACP adapter; recorded dependency on Claude Agent SDK 0.3.278 |
| Claude Agent SDK | Anthropic's Python/TypeScript wrapper around a local Claude Code binary |
| `claude -p` | Official Claude Code noninteractive entry point; structured bidirectional streams are possible |
| `@agentclientprotocol/codex-acp` 1.12.0 | Current prototype's external ACP adapter, translating to Codex App Server |
| `codex app-server` | Official local Codex interface for bidirectional client integration; its protocol is not ACP |

Adapter package/version observations come from the earlier
[live probe](acp-live-probe-2026-09-22.md). Neither that probe nor this inspection
traced the precise provider binary executed by the adapters; bundled and PATH
versions must not be conflated. The local CLI help/version checks reported
Claude Code **2.1.278** and Codex CLI **0.155.1**. These are dated observations,
not minimum versions promised by our app. No official native ACP endpoint was
verified for either CLI in the documentation/help inspected.

## Claude: runtime, authentication and billing are separate

The Agent SDK runs Claude Code locally, normally using a bundled binary; a
specific executable can be selected. Direct `claude -p` also runs that local
agent. In both cases the runtime contacts the configured inference service.
Selecting the SDK does not itself mean selecting a separately hosted agent or
API-key billing. Anthropic documents CLI subprocess integration for languages
other than Python/TypeScript. [SDK overview](https://code.claude.com/docs/en/agent-sdk/overview),
[SDK installation/authentication](https://code.claude.com/docs/en/agent-sdk/quickstart)

The SDK supports API-key and supported cloud-provider authentication without a
Claude subscription. Anthropic's June 16 help article says its announced June
15 billing change was paused: SDK, print-mode and third-party usage still draw
from subscription limits. The superseded announcement retained below that
notice must not be quoted as current policy. This is a dated billing finding,
not permission for every integration model.
[Billing notice](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)

Anthropic's developer guidance restricts offering third-party Claude login or
intermediating users' subscription credentials. Its legal page also expressly
allows end users to sign into the unmodified Claude Code binary themselves,
subject to the stated product conditions. Verify how those rules apply to our
local, user-owned runtime integration before claiming subscription support.
Do not infer eligibility from a successful request or from choosing `-p` over
the SDK. If published guidance does not settle our case, record the uncertainty
and seek clarification rather than inventing permission or changing billing.
[Authentication/product guidance](https://code.claude.com/docs/en/legal-and-compliance)

## Claude: capability comparison to verify

`claude -p` supports structured output, partial streaming, session resume and
configuration. Without a permission host, unresolved permission requests are
denied. The documented MCP permission host and hooks provide integration routes;
the SDK supplies callbacks. SIGINT ends a turn, whereas SIGTERM may leave it
unfinished for continuation on resume. These differences matter to our Stop and
Resume semantics. [Headless documentation](https://code.claude.com/docs/en/headless)

The CLI exposes `--input-format stream-json`, `--output-format stream-json` and
`--permission-prompt-tool`. The MCP prompt-tool route has a documented restriction
on approving MCP tools marked as requiring user interaction. Streaming-input
support alone does not establish every SDK control operation or same-turn
steering. [CLI reference](https://code.claude.com/docs/en/cli-reference)

SDK callbacks explicitly address approvals and user input. Compare request
identity, cancellation, answer payloads and process lifetime against direct CLI
mechanisms, rather than assuming either complete parity or impossibility.
[SDK user input](https://code.claude.com/docs/en/agent-sdk/user-input),
[hooks](https://code.claude.com/docs/en/hooks)

## Codex: official App Server behind ACP

App Server supplies bidirectional JSON-RPC-style communication, thread/turn
lifecycle, interruption, steering, model discovery and interactive requests.
Use local stdio for the candidate integration. The documentation inspected
still labels the app-server command experimental; official ownership does not
imply an immutable interface. Generate/inspect schemas for the selected CLI
version and test the operations we use.
[Official OpenAI documentation](https://developers.openai.com/codex/app-server)

The existing `agentclientprotocol/codex-acp` adapter starts App Server and maps
requests/events to ACP. Its current README documents a compatible bundled Codex
dependency and `CODEX_PATH` for a chosen executable. Inspect the pinned release
before relying on current-main behavior. Reuse avoids owning all translation
and upstream churn, but coverage, settings, executable selection and extension
behavior still need contract tests.
[Adapter repository](https://github.com/agentclientprotocol/codex-acp)

## UCF source inspection

User-supplied reference checkout:
`~/Developer/git/github.com/muschterm/ucf`. Source and tests were read; no UCF
code was changed, tests run, or agent prompt sent. Its CLI was not on PATH and
no UCF MCP tool was available; the existing `bin/ucf context map` was used for
read-only orientation. Local references below are paths within that checkout,
not dependencies to add to this repository.

| Source | Finding |
| --- | --- |
| `server/runner/claude.go`: `claudeArgs`, `spawn`, `Send`, `Resume` | One persistent `claude -p` process with bidirectional JSONL, appended instructions/MCP configuration, image-bearing turns and explicit session resume |
| Same file: `Interrupt` | Writes a correlated `control_request` with `subtype: interrupt`; comments record an earlier live spike. Responses are not correlated/acknowledged by this translator, so this is not our complete cancellation contract |
| Same file: `claudeModes`, `Choices` | Only auto/full-access permission modes; static model/effort aliases. Does not implement human approval delivery for Claude |
| Same file: `claudeState.translate` | Converts tool/reasoning/message/usage events; some child progress is retained. All `result` frames currently map to completed, so do not copy that outcome mapping into our failure/cancel semantics |
| `server/server/sessions.go`: `questionConvention` | Instructs the agent to end its turn with fenced `ucf-question` JSON and receive the answer as a new user turn; explicitly avoids an open question tool call |
| `studio/web/src/lib/session.ts`: `parseQuestionBlock`, `composeAnswer` | Validates question JSON, degrades malformed blocks to prose, and formats selected labels as reply text |
| `studio/web/src/components/QuestionPanel.tsx`, `panes/Thread.tsx` | Renders options, routes the reply through normal message delivery; single-question radio selection can immediately send, unlike our explicit-Submit requirement |
| `server/runner/codexappsession.go`: `request` | Existing direct App Server client, but policy accepts certain approvals and answers native user-input requests with empty answers plus a transcript notice; not our interactive request contract |
| `server/runner/e2e_test.go`, `server/server/permission_test.go`, `studio/web/test/session.test.ts` | Fake-process transport and question-convention/parser coverage; test source inspection is not fresh live evidence |

UCF demonstrates an implemented direct-CLI approach and a useful structured-text
question fallback. It does not establish native Claude approval/question parity
or continued-work asynchronous answers. Reuse transport ideas after checking
current protocol support; do not copy its permission defaults, automatic
question submission, hardcoded catalogues, outcome handling or reply-as-ordinary-
message semantics into our application without reconciling our contracts.
