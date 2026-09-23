# Thread context, subscription limits, and API cost

Status: required ADE feature, added by the user during round 3. This document defines expected behavior from agent-supplied data. No internet research, agent invocation, or provider-specific telemetry verification was performed for this addition.

## Availability and source

The usage display is always part of the ADE. It remains reachable with mouse and keyboard when sidebars or the bottom panel are hidden. A compact context gauge sits left of the composer paperclip and Stop/Send controls and opens detailed usage. Under the 2026-09-20 responsive footer correction, this control gains its own vertical ellipsis at the far left of the right-aligned footer group as cost details collapse; the gauge can also collapse into that ellipsis. It opens a usage summary with context occupancy, capacity, full percentage, billing mode, applicable limits and cost. It never shares the settings overflow menu. Missing measurements retain their unavailable/unknown states. Unknown context must not display fabricated utilization; the fixture has no measured context telemetry.

Populate measurements from the connected agent's stream, including telemetry forwarded by its adapter. Use standard ACP fields where the selected version supplies the required semantics, or an explicitly supported adapter extension where needed. This specification does not assert that ACP or every provider currently supplies all fields. Preserve supplied data through adapters rather than silently dropping it.

Do not fetch quota endpoints, scrape account dashboards, look up prices, or issue extra model requests to fill gaps. The feature being required does not make missing upstream data available: show an explicit unavailable state for context, and handle conditional quota/cost sections as described below.

## Measurements

| Measurement | Display and scope | Missing or inapplicable data |
| --- | --- | --- |
| Thread context | Current context occupancy and the effective capacity for the active agent/model; show tokens and a percentage when supplied or safely derivable | Show known parts independently; do not invent a denominator or use lifetime tokens as occupancy |
| Subscription quota | Every applicable supplied window, preserving its duration/name, used or remaining amount, units, reset information, and account/plan/model scope | Omit for known API billing; when subscription telemetry is unavailable, say so without inventing 5h/7d rows |
| Running API cost | Monetary amount attributable to this thread or a clearly labelled observed portion, with currency and reported/estimated status | Show unavailable when API billing is known but cost data is absent; never substitute zero or account-wide spend |

If billing mode is unknown, do not infer it from absent quota fields or the provider's name. Report billing/usage availability as unknown until supplied data or explicit connection configuration establishes the mode. Subscription quotas and a thread's context are separate measurements, even when both appear beside the same thread.

## Context correctness

Use current occupancy as reported for the active context. Cumulative input/output counters, streamed text length, and the visible transcript do not establish that occupancy. Calculate a percentage only from compatible current values, or display a provider-supplied percentage with its meaning intact. Partial data remains useful: capacity alone can be shown with usage unavailable, and the reverse also applies.

Compaction may reduce occupancy without reducing cumulative token consumption. A model change can change capacity; never pair a previous model's limit with the new model's usage. Keep the last compatible observation marked with its source and freshness while awaiting an update. Identify pre-compaction, pre-switch, or last-request observations when that is the only data available.

## Subscription windows

Five hours and seven days are examples, not a universal subscription schema. Show the supplied window names/durations and whether values mean used or remaining. Convert between used and remaining only when the units and total make that derivation valid. Do not infer absolute token allowances from a percentage.

Quotas may be shared across threads using an account, plan, or model pool. Label the reported scope; do not add identical account snapshots across threads or imply that the selected thread consumed the whole allowance. Keep distinct identities for distinct reported pools. If scope is not known, say so.

Show reset times/countdowns only when supplied. Reaching a countdown's end does not authorize the UI to restore an allowance locally; the previous observation remains stale until the agent reports new state. A stream that updates only during work is not a continuously refreshed account meter.

## Running cost

Prefer a reported cost with known thread/turn/request scope and currency. Preserve whether the source calls it an estimate or an actual reported amount. Do not relabel hypothetical subscription token value as an API charge.

A derived estimate is possible only if the agent supplies sufficient usage and matching pricing information, including applicable units and token categories. Label it as estimated. Do not use external price tables or assume input, output, cached input, and other categories have the same price. If the supplied data is insufficient, leave cost unavailable.

Distinguish incremental amounts from cumulative snapshots: replace a cumulative value rather than adding it again, and apply increments only once. Do not sum a thread total with the turn totals it already includes. Preserve monetary precision and currency. If observation began mid-thread, label a partial total such as “since connection” unless the stream establishes a complete total. Q12 fixes one agent per thread and defers switching/handoffs, so cross-provider thread totals are outside initial scope. Keep usage for a separate conflict-resolution job identifiable rather than silently merging it into the original agent's counters.

## Adapter and presentation contract

Each normalized measurement needs its meaning, value/units, source agent/connection, scope, observation time, and availability. Preserve model/context identity where relevant, quota-window identity, cost currency, and whether an event is a delta or snapshot. These are semantic requirements, not invented ACP wire fields or a selected storage schema.

Keep unknown, unsupported, stale, and not-applicable distinct. Only show zero after a valid zero measurement. On reconnect or history replay, avoid applying the same observation twice and do not mark a replayed value freshly measured. Use source identifiers/ordering where available; if semantics or ordering are insufficient to establish a reliable total, show incomplete coverage rather than guessing.

The compact display must remain legible in narrow terminals. Details need keyboard and pointer access; essential units, used/remaining labels, and unavailable states cannot rely on color or hover alone. Subscription-only and API-only presentations omit genuinely inapplicable sections while retaining a clear context indicator.

## Verification scenarios

- Complete, partial, and absent context telemetry; a zero usage value versus unknown.
- Compaction, model changes, and stale events without mixing incompatible occupancy/capacity.
- Subscription streams with two windows, one window, different durations, or unknown scope.
- Known API billing omits subscription quotas; unknown billing does not pretend to be API billing.
- A reset countdown expires without new telemetry; the UI does not invent replenishment.
- Cumulative cost snapshots, incremental costs, reconnect replay, and turn totals do not double-count.
- Reported versus estimated cost, missing prices/currency, and partial-thread coverage remain clearly labelled.
- Shared account quota events do not multiply when more than one thread is visible.
- Switching the visible thread or viewing a separate resolution job does not mix counters, account scopes, or currencies.
- Mouse/keyboard access and narrow layouts retain usage access, including through composer overflow, with sidebars hidden.
- Missing telemetry causes no additional network calls or pricing lookups.

These are acceptance scenarios for all three reference apps. Implementation, event mappings, and runtime validation are still pending.

## Go telemetry and compact display — 2026-09-22

The composer uses a compact `Ctx n%` control instead of a decorative four-cell
bar. Unknown context is `Ctx —`; no unavailable cost placeholder consumes the
control row. A cost appears only when supplied, with an estimate marker where
applicable. Details retain exact counts, decimal amounts, source, model, scope
and observation time. A model/session mismatch hides its old percentage rather
than pairing it with a new limit. Usage remains independently reachable by
keyboard and pointer when controls overflow.

The built-in bridges advertise `tui-go.usage.v1` and emit bounded
`tui_usage_update` snapshots through ACP. Missing context values stay absent.
Codex forwards `thread/tokenUsage/updated.last.totalTokens` with its reported
model context window, labelled as the last model request; cumulative `total`
is never used as occupancy. Claude uses the latest main-agent request's input,
cache-read and cache-creation tokens, labelled as input at the last request.
Its assistant output token field may be a placeholder and is excluded. Capacity
comes only from the matching actual model's result `modelUsage` entry;
compaction clears the old input observation until the next request.

Claude's `total_cost_usd` is an upstream-computed **estimate** and a cumulative
session snapshot. Replace it, never sum successive results. Preserve decimal
text and do not call it an authoritative bill or infer billing mode. Codex cost
and subscription limits stay unavailable when absent. No extra model requests,
quota APIs or price lookups populate these controls.
