# Context and cost telemetry — 2026-09-22

The previous UI rendered a placeholder bar and `Cost —`, while the two built-in
bridges emitted no usage. The server supported ACP `usage_update` from other
agents, but discarded its optional cost. This explains both missing native data
and the misleadingly complete-looking footer.

Primary evidence:

- Installed Codex 0.155.1 generated
  `v2/ThreadTokenUsageUpdatedNotification.json`: per-thread/turn `last` and
  `total` token breakdowns plus optional `modelContextWindow`. The
  [official App Server guide](https://learn.chatgpt.com/docs/app-server) documents
  the notification. The bridge uses the last request snapshot only.
- [Claude SDK cost/usage documentation](https://code.claude.com/docs/en/agent-sdk/cost-tracking)
  identifies assistant input/cache usage as per-step, warns that assistant
  output tokens may be placeholders, and describes `total_cost_usd` as a
  cumulative client-side estimate. Streaming result totals replace prior
  results; they are not increments. Result aggregate usage and mixed-model
  cumulative tokens cannot establish current context occupancy.
- The pinned ACP Go SDK v0.13.5 marks `UsageUpdate` unstable and supplies
  context plus optional cumulative session cost. The built-in extension
  `tui-go.usage.v1` adds explicit source/model/request scope and partial-data
  semantics rather than fabricating zero occupancy when only cost is known.

Normalization preserves absent vs zero, rejects negative/invalid measurements,
checks session identity, drops older timestamped reports, and retains a
session's last cost snapshot through context-only updates. Model switching
hides incompatible context in the frontend. Details show exact provider decimal
amounts; compact cost is rounded and marked as estimated where applicable.
There are no external pricing or quota requests.

Focused tests cover latest vs cumulative tokens, cache token categories, exact
capacity model matching, compaction invalidation, partial/zero/unknown values,
model changes, decimal cost replacement, stale events, and conversation
chronology across native answers. Live and visual results are recorded with the
conversation-polish implementation note after integration.

Live application checks passed for both providers: [Codex Fast](conversation-polish-codex-live-2026-09-22.json)
reported 20,464 / 258,400 tokens; [Claude Standard](conversation-polish-claude-live-2026-09-22.json)
reported 22,498 input tokens and an upstream estimated session cost of USD
0.098196. A separate bounded native observation reported assistant model
`claude-opus-5-5` and result key `claude-opus-5-5[1m]` with
`canonicalModel: claude-opus-5-5`, `contextWindow: 1000000`. This mismatch caused
the initial missing Claude capacity. Matching now uses that supplied canonical
identity plus selected/resolved model key, with tests rejecting ambiguous
capacities across the same canonical model. No capacity is inferred from the
text `[1m]`.
