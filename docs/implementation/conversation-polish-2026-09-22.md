# Conversation and provider controls — 2026-09-22

The user's latest request supersedes the earlier composer placement: put typing,
model/settings and actions inside one prompt outline. Running/Waiting belongs
to conversation content. Answer history must preserve the question, selected
answer and alternatives, including clearly labelled unconfirmed delivery.

Parallel ownership: question history and acceptance anchors; composer geometry
and status placement; provider option discovery/control. Parent integrates
model-scoped UI selections, native usage normalization, documentation and checks.
The prior uncommitted UI fixes remain part of the working tree.

Assumptions: no replay of personal threads, no personal-server restart, no
external quota/price fetching. Native cost estimates remain estimates; current
context uses only latest-request observations, never accumulated token totals.
Legacy Claude entries are explicit catalog choices, not proof of account access.
Versioned official/runtime evidence and native acceptance must remain visible in
the provider research note. Model changes preserve compatible settings and reset
inapplicable selections before capture; queued prompts retain their settings.

The Claude Fast control was verified through native state readback; its runtime
still gates account availability. An isolated live startup reported
`extra_usage_disabled`; when unavailable, Standard stays visible with the reason.
Claude effort remained unavailable here because no effective-state readback had
been found; the `get_settings` readback was verified and used on
[2026-09-23](../research/claude-effort-2026-09-23.md).

Integration review corrected the live Auto catalog to retain model applicability
for a captured target model even when the previous model lacks Auto. Native
model changes clear incompatible Fast/Auto settings before dispatch, and failures
stop before any prompt is sent. Agent text after a question answer is segmented
below its durable history anchor instead of merging into an earlier response.

Validation: full `make check` passed after the account-restriction UI
refinement (formatting, vet, staticcheck, race tests and build). All seven OS-PTY harnesses passed
at `/tmp/tui-polish-pty-final`, including 40×22 and 47×22.

The isolated [Codex live report](../research/conversation-polish-codex-live-2026-09-22.json)
verified Luna Fast (native `priority` tier), Auto, native question/answer,
persisted context telemetry, one answer anchor across duplicate retries/restart,
and owned process cleanup. Native delivery remains unconfirmed where the runtime
has no authoritative resolution receipt.

Deterministic Go View captures (not terminal screenshots) were rendered with
JetBrains Mono Nerd Font and visually inspected in dark/light wide/compact
layouts: [wide dark](../research/conversation-polish-captures/160x50-lightfalse-conversation-wide.png),
[compact dark](../research/conversation-polish-captures/60x32-lightfalse-conversation-compact.png),
[wide light](../research/conversation-polish-captures/160x50-lighttrue-conversation-wide.png),
[compact light](../research/conversation-polish-captures/60x32-lighttrue-conversation-compact.png).

The isolated [Claude live report](../research/conversation-polish-claude-live-2026-09-22.json)
verified pinned Opus 5.5, Standard, native questions/history, cumulative estimated
cost, input usage and recovery. Claude Fast availability varied across startup
observations: one returned `extra_usage_disabled`, while a later catalog offered
Fast. Treat the runtime result as authoritative for that connection; reject a
change if native readback says it is disabled. No account settings were enabled.

A bounded direct usage probe identified why the initial Claude live report lacked
capacity: assistant.model is canonical, while result.modelUsage keys include
`[1m]` and supply `canonicalModel`. The bridge now matches the supplied selected /
resolved key and canonical metadata. It does not guess by maximum capacity or
combine different models. A regression uses the observed shape, including
ambiguous same-canonical-model capacities.

Final formatting, vet, staticcheck, build and affected bridge/agent race tests
passed after the canonical-capacity mapping and disabled-Fast readback guard.
`git diff --check` passed. The personal server was not restarted.
