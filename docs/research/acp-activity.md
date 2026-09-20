# ACP activity and subagent inspectors

Date: 2026-09-19. Primary protocol documentation and maintained adapter source inspected; **not runtime verified**. Adapter `main` may exceed published releases. Pin and validate adapter, SDK, backend, and extension dialect together before implementation. Scope: one agent per top-level thread, clickable timeline cards opening the right sidebar inspector, and subagent presentation. Cross-agent handoffs remain deferred.

## Portable activity contract

ACP v1 provides a useful foundation for clean activity cards. [Tool calls](https://agentclientprotocol.com/protocol/v1/tool-calls) have identifiers, titles, kinds, status, optional raw inputs/outputs, content, and file locations. Subsequent updates identify the same call and change selected fields. Missing values must not erase earlier information; specifically, omitted/null raw input and output leave prior values unchanged. Content can include ordinary blocks, diffs, and terminal references. Use these fields for readable summaries and expandable details; unknown fields and unfamiliar tool kinds should not break rendering.

[Plans](https://agentclientprotocol.com/protocol/v1/agent-plan) are snapshots of entries with content, priority, and pending/in-progress/completed status. Each update replaces the whole current plan. It is not an append-only task log, and the documented entries have no stable task identifier. Preserve received snapshots for inspection; do not invent durable entry identity from array positions or turn arbitrary assistant prose into a structured plan.

[Session loading](https://agentclientprotocol.com/protocol/v1/session-setup) is capability-gated and replays conversation updates. This is distinct from resume without replay. Neither a successful connection nor core v1 support guarantees a separately discoverable child tree or every historical tool payload. MCP connection configuration also does not guarantee a complete tool catalog or consistent server identity in activity events. Render what the adapter supplies, with explicit unavailable/partial states.

## Subagents are a moving extension contract

The inspected adapters implement an earlier draft of [ACP subagents RFD #1992](https://github.com/agentclientprotocol/agent-client-protocol/pull/1992), still open when checked. Their bilateral negotiation uses `clientCapabilities.subagents` and `agentCapabilities.sessionCapabilities.subagents`, with an AIR `nativeSubagentSessions` metadata fallback. They emit `subagent_spawned` and `subagent_state_update`.

The proposal changed September 16 to a single upsert-style `subagent_update`, client-only capability gating, and no child-close capability. **Do not mix the latest proposal schema with an older adapter's wire format.** Isolate supported dialects in the provider boundary and normalize them into one internal child model. Unknown lifecycle outcomes must remain unknown/disconnected rather than becoming successful completion. Ordinary ACP clients receive legacy tool activity instead of a guaranteed child transcript.

## Current adapter coverage

| Surface | Codex ACP adapter | Claude Agent ACP adapter |
| --- | --- | --- |
| Structured plans | Maps backend step/status updates to v1 plan snapshots; separately supports Markdown plan updates when the client advertises `plan`. | Converts TodoWrite and confirmed Task operations into plan snapshots. |
| Tool/MCP details | MCP mapping preserves server, tool, arguments, result/error, and an MCP metadata marker. | Emits tool names, inputs, structured content, and tool-result updates; fidelity depends on available SDK records. |
| Child activity | Negotiated child-scoped messages, plans, tools, and interactions; subscribes recursively to discovered children. | Negotiated native child sessions, or legacy tool calls and optional flattened child text. |
| History | Documents child-tree reconstruction through session load. | Reconstructs children and routes persisted sidechain content through the replay converter. |
| Targeted child control | Child cancel/close not advertised. | Child cancel/close disabled. |

Codex's [event handler](https://github.com/agentclientprotocol/codex-acp/blob/main/src/CodexEventHandler.ts) separates checklist updates from Markdown plan content; unsupported Markdown-plan clients receive text. Its [MCP mapper](https://github.com/agentclientprotocol/codex-acp/blob/main/src/CodexToolCallMapper.ts) supplies structured call details. These are adapter translation findings, not claims about a native vendor ACP product.

The Codex [subagent documentation](https://github.com/agentclientprotocol/codex-acp/blob/main/docs/subagent-sessions.md) requires announcing a child before output, reports lifecycle on its immediate parent, and keeps permission controls visible through the root. Replay uses disconnected for unproven outcomes; documented live timeout/shutdown/notFound fallbacks use failed, a divergence to preserve as adapter-reported information. [Subscriptions](https://github.com/agentclientprotocol/codex-acp/blob/main/src/subagents/CodexSubagentSubscriptions.ts) suppress ordinary child output without negotiation while retaining root-attributed interactions.

Claude's [adapter source](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/acp-agent.ts) converts plans and tool blocks and reconstructs replay children using Agent/Task tool calls and `parent_tool_use_id`. Replay child IDs are synthetic, derived from the root and spawning tool-use ID. Persisted sidechain messages supply child content; lifecycle frames are not persisted, so tool results establish terminal states, with disconnected for missing results or malformed lineage. This establishes reconstruction of available records, not universal retrieval of complete child history. The [live router](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/native-subagents.ts) correlates task/tool identities and delays nested announcements until parentage is known. The [README](https://github.com/agentclientprotocol/claude-agent-acp) documents legacy flattened-transcript opt-ins.

## Inspector adaptation and acceptance targets

Recommend reusing these adapters and adding a small normalization boundary. Store provider provenance, parent/child links, received activity, and history availability separately from presentation. The full inspector means **all available child messages and tool history**, not a summary presented as a complete transcript. A summary-only provider should still produce an inspectable card explaining that limitation.

- Clicking or keyboard-opening a plan, tool, MCP, or subagent card selects its inspector without changing the top-level thread's agent.
- Tool refinements update one card; missing output remains distinguishable from an empty successful result. MCP identity and raw details appear only when supplied.
- Concurrent and nested children retain correct parentage, independent messages, plans, tools, and status. Late output cannot attach to an unrelated child.
- Reload reconstructs available history without duplicate cards; synthetic replay identities must not be assumed identical to live identities.
- Missing history, unknown outcomes, unsupported content, and legacy aggregate events remain visible. Never fabricate transcript entries from progress counters or summaries.
- Child permissions remain reachable from the root. Hide unsupported targeted cancellation; parent cancellation and child status are separate facts.

Validate these scenarios against pinned adapters using recorded protocol fixtures and eventual live acceptance runs. Source inspection alone does not establish full transcript parity between providers.
