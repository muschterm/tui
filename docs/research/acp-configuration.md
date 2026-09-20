# ACP configuration for thread creation

Date: 2026-09-19. Official ACP v1 documentation and maintained adapter source checked; **NOT RUN**. No agents, packages, or model requests executed. Adapter `main` observations are not release guarantees. Pin reviewed adapter/backend/SDK versions and validate their actual option payloads before implementation.

The accepted flow chooses an agent first, then its supported model, effort, permissions, and any offered context-capacity or speed controls. This is feasible through generic ACP configuration, but no protocol category guarantees every agent exposes a corresponding setting.

## Stable protocol mechanisms

[Session Config Options](https://agentclientprotocol.com/protocol/v1/session-config-options) are the preferred v1 mechanism. Options arrive during session setup with IDs, labels, current values, and selectable values. Categories include `model`, `thought_level`, `mode`, and `model_config`; categories guide presentation and are not required for correctness. Preserve unknown categories and option IDs rather than using a fixed provider-independent enum.

`session/set_config_option` changes one option. Its response supplies the entire configuration state, including dependent changes; `config_option_update` notifications also carry complete state. Replace the displayed option set and reconcile selections. A model change can invalidate effort or other choices. Submit only advertised values and retain server-confirmed values when a request fails. Although v1 permits configuration during generation, exact effect timing still needs adapter validation.

Select controls are the baseline. Boolean controls require `clientCapabilities.session.configOptions.boolean: {}`. Unknown option types retain the agent's default rather than becoming guessed controls. Boolean support is [stabilized](https://agentclientprotocol.com/announcements/boolean-config-option-stabilized). The stable [model-config category](https://agentclientprotocol.com/announcements/model-config-category-stabilized) explicitly accommodates context size and speed/quality trade-offs, but defines neither universal context sizes nor speed tiers.

Legacy [session modes](https://agentclientprotocol.com/protocol/v1/session-modes) use `modes`, `session/set_mode`, and `current_mode_update`. Prefer config options when both exist; avoid duplicate permission selectors. Modes can affect prompts, tool availability, and permission behavior. For model selection, use the stable generic configuration path; do not assume a standalone model-selection method is a portable v1 requirement. The [v1 schema](https://agentclientprotocol.com/protocol/v1/schema) is the contract to pin.

## Maintained adapter mappings

| Setting | Codex adapter source | Claude adapter source |
| --- | --- | --- |
| Model/effort | Builds model choices from available model records and effort choices from their supported efforts. | Builds model choices from the available list; effort appears only when the selected model reports supported levels. |
| Permissions | Exposes approval/sandbox presets through a mode option. | Exposes permission modes, with model-dependent reconciliation and conditional availability. |
| Speed | Fast-mode option depends on model support; boolean or select presentation. | Fast-mode option appears only when the model supports it; boolean or select presentation. |
| Context | No independent 200k/1m selector established by this review. | Model identities may encode context variants; no independent universal capacity selector established. |

Codex's [model mapper](https://github.com/agentclientprotocol/codex-acp/blob/main/src/ModelConfigOption.ts) uses model-specific effort options. Its [fast-mode mapper](https://github.com/agentclientprotocol/codex-acp/blob/main/src/FastModeConfig.ts) maps an enabled supported option to the fast service tier; the inspected description says 1.5x, not 2x. Preserve provider-supplied labels as descriptions, not measured throughput guarantees. [Mode presets](https://github.com/agentclientprotocol/codex-acp/blob/main/src/AgentMode.ts) bundle policies; notably, an internal ID named `read-only` maps to a workspace-write preset in the inspected source. IDs must never substitute for reading the effective policy.

Claude's [effort builder](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/session-effort.ts) omits unsupported effort controls. Its [configuration builder](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/acp-agent.ts) conditionally adds fast mode. [Model mapping](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/session-model.ts) preserves selectable identifiers and handles context variants; do not manufacture a capacity setting by altering a model-name suffix. [Mode handling](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/session-mode.ts) can reconcile an unsupported automatic mode after a model change. Recommended-value metadata used by these adapters is an opt-in extension, distinct from standard current values.

## Application adaptation

The background server owns discovery and ACP configuration; each TUI displays the shared server state. Since portable options arrive after session creation, a practical design is a provisional, idle agent session for configuration before the first prompt. Cancelling creation needs explicit provisional-session cleanup. This is a proposed adaptation, not an ACP promise of side-effect-free startup or an atomic create-with-all-settings request.

Distinguish requested, confirmed, unavailable, and changed-by-agent values across all attached clients. Context occupancy/capacity reports are measurements, not proof that capacity is selectable. A 1m/200k control or 2x speed choice appears only when supported by the selected integration. Permission presets remain provider-specific and do not establish a universal filesystem sandbox or replace checkout writer coordination.

Acceptance tests should cover dependent option removal, rejected changes, unknown types/categories, absent controls, simultaneous-client changes, cancellation before first prompt, and restart followed by explicit Resume without silently restoring incompatible saved choices. Confirm effective configuration before starting queued work; do not send a test model request merely to populate the picker.
