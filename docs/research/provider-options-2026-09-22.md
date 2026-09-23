# Provider model, effort, and speed options — 2026-09-22

Scope: installed Claude Code 2.1.280 and Codex CLI/App Server 0.155.1, plus read-only T3 Code inspection. This records observed protocol behavior separately from account entitlement.

## Claude Code

The installed `claude -p` stream JSON `initialize` control returned five picker rows: `default`, `opus[1m]`, `claude-fable-5-1[1m]`, `sonnet`, and `haiku`. Each included a `resolvedModel`; the first four included `supportsEffort` and `supportedEffortLevels`, and only `default`/`opus[1m]` reported `supportsFastMode: true`. `fast_mode_state` was `off`, with `fast_mode_disabled_reason: sdk_opt_in_required`. The native `set_model` control acknowledged `claude-opus-5-5[1m]`, the reported exact resolved ID. `set_fast_mode` and `set_settings` control subtypes were rejected as unsupported. A probe of two other model IDs returned network errors, so it cannot establish those accounts' availability.

[Claude Code model configuration](https://code.claude.com/docs/en/model-config) explicitly supports full model IDs and pinning versions; it names Opus 5.5, Opus 5, Opus 4.8, Opus 4.7, Opus 4.6, Sonnet 4.6, and Fable 5. It also documents `ANTHROPIC_CUSTOM_MODEL_OPTION` and `modelPicker` for additional user-selected IDs. The read-only [T3 Code model manifest](/Users/muschterm/Developer/git/github.com/pingdotgg/t3code/apps/server/src/provider/model-manifest.json) records current and legacy IDs with minimum CLI versions where applicable. These sources establish identifiers and a native selection route; they do not establish this account's entitlement. The bridge therefore labels legacy rows and lets `set_model` return a native rejection rather than marking them discovered. Runtime-resolved exact IDs are exposed as pinned rows. Claude's dynamic effort is still unavailable in this bridge because a previous no-prompt `apply_flag_settings` probe acknowledged invalid effort and did not prove effective application.

T3 Code's [ClaudeTextGeneration.ts](/Users/muschterm/Developer/git/github.com/pingdotgg/t3code/apps/server/src/textGeneration/ClaudeTextGeneration.ts) supplies `fastMode` through process startup `--settings`, and its [ClaudeAdapter.ts](/Users/muschterm/Developer/git/github.com/pingdotgg/t3code/apps/server/src/provider/Layers/ClaudeAdapter.ts) scopes it to capable models. [Claude Code's Fast guide](https://code.claude.com/docs/en/fast-mode) requires that same launch setting for Fast in non-interactive `-p` sessions. A no-prompt probe confirmed launch `--settings '{"fastMode":true}'` reports `fast_mode_state:"on"`. A further no-prompt probe found a dynamic route in this CLI: `apply_flag_settings` with `settings:{"fastMode":true}` acknowledged, and a subsequent `initialize` readback reported `on`; the same sequence with `false` reported `off`. Using `flags` instead of `settings` was explicitly rejected. The bridge launches with the session-only opt-in, immediately resets to Standard with native readback before any prompt, applies Fast only for models whose native catalog reports `supportsFastMode`, requires state readback before acknowledging selection, and clears it when switching to a non-capable model. Repeating this experiment with `effortLevel` still supplied no effective effort field in initialize; generic ACKs are insufficient to expose that control.

## Codex App Server

The [official App Server model/list guide](https://learn.chatgpt.com/docs/app-server#list-models-modellist) says to use returned models and per-model effort options, since account/client availability differs. The installed `codex app-server generate-json-schema --experimental` schema exposes model `serviceTiers` and deprecated `additionalSpeedTiers`, `thread/settings/update.serviceTier`, and `turn/start.serviceTierForTurn`; `"default"` means standard speed for the turn. T3 Code's [CodexProvider.ts](/Users/muschterm/Developer/git/github.com/pingdotgg/t3code/apps/server/src/provider/Layers/CodexProvider.ts) maps the same per-model tiers and gives effort choices short labels. The Go bridge now derives short effort labels from native IDs, exposes speed only for models with reported tiers, validates both against the selected model, obtains a native settings acknowledgement, and binds the tier to each turn. The probe catalog carries model applicability for local draft menus, even before any session model change. A standalone no-prompt Codex `model/list` probe timed out during initialize in the sandbox; the later isolated application check supersedes that limitation: [Luna Fast live report](conversation-polish-codex-live-2026-09-22.json) records its reported `priority` tier, native acknowledgement and successful question/context flow. A model switch clears the prior tier even when the new model has no tier; older queued prompts with blank/unavailable speed apply explicit Standard only on a model with a speed selector.

## Telemetry hooks

Codex `thread/tokenUsage/updated` includes `threadId`, `turnId`, last request token breakdown, cumulative total, and optional `modelContextWindow` in the installed schema. The bridge gates it by both identities and forwards the last request observation, not cumulative total as context occupancy. Claude assistant `message.usage` has input and cache input categories; its result has per-model `modelUsage.contextWindow` and cumulative `total_cost_usd`. The bridge emits source/scope-tagged snapshots through the versioned `tui-go.usage.v1` extension. Cost is an estimate; it is never recomputed from a price table. Subscription quota is not supplied by these paths.

## Integrated account check

The isolated bridge launch on this account later reported Claude
`fast_mode_state:"off"`, `fast_mode_disabled_reason:"extra_usage_disabled"`,
while Opus still reported `supportsFastMode:true`. This supersedes any inference
that a no-prompt `on` readback established account entitlement. The UI retains
Standard and explains why Fast is unavailable; it does not enable extra usage.
The control remains selectable where native capability and account availability
both allow it, with readback required on every change.

Independent review found that filtering Auto by the previous model could reject
a queued supported target-model/Auto combination before changing models. The
bridge now keeps the union of Auto-capable models with explicit applicability;
the picker and dispatch each validate against the selected/captured model.

A later [Claude Standard live check](conversation-polish-claude-live-2026-09-22.json)
reported Fast as available in its catalog, illustrating that startup/account
availability can change. Standard native delivery, Q&A, cost telemetry and
recovery passed. The earlier extra-usage restriction remains evidence of a real
runtime gate, not a permanent account diagnosis; every Fast enable requires
native state readback without a reported disabled reason.
