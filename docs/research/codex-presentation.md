# Codex presentation research

Source-checked: **2026-09-19**, using the OpenAI Docs skill and official OpenAI documentation. No App Server, agent, adapter or UI was executed; runtime validation is **NOT RUN**.

The requested ADE experience includes visible plan steps, inspectable subagent work, and compact tool/MCP cards opening complete available details in the right sidebar. This is a presentation coverage target, not a claim that every provider, adapter or stored session exposes identical information.

## Native event and history map

The [App Server reference](https://learn.chatgpt.com/docs/app-server) documents:

| Surface | Evidence to consume |
| --- | --- |
| Lifecycle | `turn/started`, `turn/completed`; completed, interrupted or failed outcomes |
| Item reconciliation | `item/started`, ordered deltas, authoritative `item/completed` |
| Plan | `turn/plan/updated` step/status entries; separate proposed-plan text items |
| Delegation | `collabToolCall` with sender, receiver/new thread IDs and available prompt/status |
| Commands | Command, directory, streamed output and available exit/duration fields |
| MCP | Server/tool identity, arguments, status, optional result/error and app metadata |
| Changes | File-change items and aggregated `turn/diff/updated` |
| Other activity | Web searches, image views, review markers, compaction and available reasoning summaries |
| Interactivity | Approval requests, user-input requests, `serverRequest/resolved`, steering and interruption |

`thread/read` with `includeTurns` reads stored history without resuming/subscribing. Item/turn pagination and parent/ancestor thread filters are experimental. `experimentalApi` opt-in gates experimental fields/methods; unsupported stores can reject pagination. Final plan text can differ from concatenated deltas. Warnings and errors need separate handling. Preserve emitted reasoning only; its availability varies. The pinned App Server schema must resolve exact payloads, rather than copying this summary into handwritten wire types.

## Plans and subagents

**Presentation proposal:** distinguish an execution checklist from a proposed plan document. Show checklist progress compactly, retaining explanation and earlier revisions in the inspector when recorded. Do not infer step completion from surrounding prose or mark every step complete when a turn ends. A user should be able to identify unfinished work after interruption without reading the entire transcript.

Official subagent guidance describes delegated agent threads whose progress/results can be inspected in supported clients. Parent summaries intentionally omit noisy intermediate work; they are not substitutes for child-thread history. Parallel write-heavy work can also create conflicts. [Subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents).

**Presentation proposal:** a delegation card identifies the child, objective, current state and relationship to the parent. Activation opens a child timeline with its available messages and tool cards, plus a clear return path. Preserve relationships when the same child receives follow-up work. Never flatten child tools into the parent's chronology without identifying their source. If child history is unavailable, show that state and any available summary; do not manufacture a transcript or silently label a summary as complete history.

“Full available transcript” means all retrievable user-facing material supplied by the connection/store. It does not promise private internal reasoning, omitted provider data, lost deltas or unavailable attachments. Record whether the inspector is showing live, persisted, partial or unavailable content.

## Tool and MCP inspection

**Presentation proposal:** each card shows a meaningful action label, state and brief result. Mouse activation and keyboard activation open the same right-sidebar inspector. Details should retain supplied arguments, structured results, textual output, errors, files, attachments, timestamps/durations when available, and links back to the originating agent and turn. Keep long payloads searchable and copyable without expanding the main timeline; disclose UI truncation and offer access to retained content.

Codex's MCP documentation distinguishes configured local-host servers from hosted plugin tools, and exposes configurable tool-output budgets. Therefore a “complete details” UI cannot claim that an upstream result was never truncated. [MCP documentation](https://learn.chatgpt.com/docs/extend/mcp).

**Coverage proposal:** include ordinary commands, MCP calls, client-executed tools, file operations, searches, images and unknown future item types. Unknown content should remain inspectable through a safe generic view. Escape untrusted control sequences; displaying a tool result must never execute it, load an arbitrary remote resource automatically, or replay an action.

## Approvals, changes and recovery

Approval policy and sandbox policy are separate controls. Automatic reviews may approve, deny, abort or time out; not every action prompts the user. [Approvals and security](https://learn.chatgpt.com/docs/agent-approvals-security).

**Presentation proposal:** distinguish waiting for permission, waiting for an answer, running, interrupted, failed and completed. Scope pending controls to their originating work. Remove stale controls after resolution/cancellation while preserving the recorded decision. Show only actionable choices supplied by the integration, and prevent double submission. A timeout or disconnect must not appear as user approval.

Codex's review documentation distinguishes repository-wide staged/unstaged/branch views from last-turn review; repository changes can include edits made by the user or other processes. [Code review](https://learn.chatgpt.com/docs/code-review).

**Presentation proposal:** label change scope explicitly. Link tool-associated changes and turn-associated changes without treating either as an automatic undo guarantee. Preserve failed edits and cancellation markers. Keep stream-derived usage optional; unavailable values stay unavailable, with no new polling or quota subsystem implied here.

## Compatibility and acceptance

OpenAI labels experimental features unstable and subject to change/removal; beta is intended for broader testing. [Feature maturity](https://learn.chatgpt.com/docs/feature-maturity). **Proposal:** record provider, adapter, server/schema version and enabled features in diagnostics. Establish a per-integration coverage table before promising parity. Keep unavailable native features visible as limitations rather than guessing equivalent ACP events.

All acceptance checks are **NOT RUN**: revised plans; multiple children and follow-ups; inspection during streaming; full available tool results; missing history; reconnect/replay without duplicates; partial attachments; declined approvals; stale questions; cancellation races; failed edits; unknown items; large payloads; keyboard/mouse parity; and stable mode with experimental endpoints unavailable. Unresolved decisions include adapter fidelity, history retention, experimental opt-in, inspector navigation and limits for large artifacts.
