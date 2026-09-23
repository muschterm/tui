# Agent and thread configuration

Implementation evidence (2026-09-19): the [first Go slice](go-slice.md) now exercises a fixture-driven subset of this contract. See the [validation report](../research/go-slice-validation-2026-09-19.md) for executed checks and limitations; provider/editor/real-terminal behavior below is not implied by fixture results.

Status: required by the user on 2026-09-19, refined on 2026-09-20. The Go Demo implements local configuration and first-Send behavior. Real agent integration and configuration enforcement remain **NOT RUN**.

## Creation flow

**New thread** opens or restores a client-local draft for the chosen project. Persist its prompt, attachments and configuration with that client's view; it is not an authoritative thread yet. Choose the agent connection, then explicitly choose a supported model and review its supported effort, permissions, context capacity and speed/service tier. Send stays unavailable until required choices are valid. The first valid Send creates the thread and accepts its captured initial prompt atomically; failed validation leaves only the draft. The agent remains assigned to the thread; a model is a setting of that agent, not a replacement agent.

Controls depend on the selected connection and, where relevant, model or account capabilities. `1m` versus `200k` context and `2x` speed are user examples, not universal options or default values. Preserve meaningful upstream labels and units. A speed tier is not a guarantee of measured throughput, and selected context capacity is distinct from current context occupancy in the [usage display](usage.md).

| Setting | Required presentation |
| --- | --- |
| Agent | Connection identity, readiness, authentication state when supplied, and capability availability |
| Model | Supported model identity and display name; selected/default value must be visible |
| Effort | Agent/model-supported effort options with their upstream meaning; do not force a universal low/medium/high scale |
| Permissions | Effective execution/approval policy and available choices; distinguish permission to act from the handling of individual approval requests |
| Context capacity | Selectable capacity when exposed; otherwise identify fixed/reported capacity or unavailable selection |
| Speed/service tier | Selectable provider tier when exposed, including any supplied tradeoff information; no inferred pricing or speed guarantees |

Changing the agent refreshes all dependent options; changing the model revalidates effort, context, and speed choices. Never carry an unsupported value silently to another connection. Loading, unavailable, unsupported, stale, rejected, and accepted configurations need distinct states. Supported dependent defaults may be filled after explicit model selection and shown for review, but must not conceal the effective choices. In the Go fixture, Demo Agent is preset and Reference model is the only selectable model; its supported defaults complete the selection. Codex and Claude models must not be invented as selectable fixture options.

## Persistent prompt configuration strip

The user requires these settings to remain available at the bottom of the prompt box, throughout a thread: agent, model, effort, permissions, and applicable context and speed selections. They must not exist only in the creation dialog or inspector. Put the strip inside the center workflow's composer; the optional bottom terminal remains a separate pane below that workflow.

While idle, show the editable selection for the next submission. During active work, including waiting, the settings row and its overflow controls are read-only and show the confirmed effective running configuration. Ordinary Send during work captures that effective configuration. Preserve the client's idle preference and restore it when work becomes idle; the active display must not overwrite that preference. Show matching values once, without “Running with selected settings.” Distinguish meaningful selected/running mismatches in details, and label disconnected last-confirmed values as stale. Never relabel a run before confirmation or imply that a queued item has started.

Each setting has mouse and keyboard access to supported controls or details. Abbreviated labels must retain enough meaning to distinguish model, effort, permissions, context, and speed tier. The 2026-09-20 review supersedes wrapping: progressively move fields into a vertical ellipsis menu directly after the remaining left-aligned fields as width decreases, retaining the model longest and restoring fields as space returns. Usage has a separate menu in the right-aligned group. The typing area and configuration/actions share one rounded outline, with controls on its bottom interior row (2026-09-22 refinement). Overflow items show their values and route to the same supported controls/details; differing selected/running values remain distinguishable. Preserve a compact prompt/configuration footer during ADE surface expansion. Unsupported optional selectors can show fixed, unavailable, or not-applicable values without inventing choices. This strip is distinct from the usage meter, which reports measurements rather than configuration.

Use flat clickable values and one coherent settings action instead of placing a dropdown chevron and bordered selector around every field. Hover/focus and the settings action make editability discoverable; visible fields and overflow entries show their values without requiring hover. Agent brand artwork is optional decoration beside its name, sourced from verified official assets. Keep agent identity separate from provider and model identity, and use text when artwork is unavailable.

## Settings captured per prompt

When the user presses Send, capture that prompt's selected model, effort, permissions, and applicable context capacity and speed tier. A queued prompt retains those values until the user explicitly changes that queued item's settings. The thread's assigned agent remains fixed. When idle, changing the composer's selection affects future submissions; it does not rewrite queued items or relabel the active run.

Expose each queued item's recorded settings through its edit controls. While active work locks setting changes, an explicit queued text edit shows and preserves that queued item's recorded settings, instead of copying the running turn's settings. Explicit changes revalidate dependent options and use the same server revision/dispatch checks as other queue edits. Editing prompt text or reordering the queue leaves its settings and unchanged attachment captures intact. Preserve the accepted settings through reconnect and restart/Resume.

[Steer](activity.md#steering-a-queued-message) retains these recorded settings
while targeting the current turn. They must match its effective configuration,
or be explicitly validated and applied through a verified adapter capability.
Otherwise leave the message queued and explain the mismatch. Steering must not
silently use different settings, change the active model or start a new turn.

Before dispatch, apply and validate that item's recorded selection through the chosen agent's supported interface. Keep requested settings separate from acknowledged effective values. If a captured option is no longer available or cannot take effect, stop for resolution rather than silently substituting the composer's latest selection or an upstream default. Save the confirmed execution configuration with the resulting turn. Exact adapter mechanisms remain implementation work; a recorded selection is not proof of enforcement.

## Configuration authority

Use the agent's advertised configuration and supported adapter mappings. ACP is the boundary; a generic form can present native option groups without hardcoding Codex or Claude into the reusable shell. Stable protocol compatibility alone does not promise every listed control. Adapters may need documented extensions for missing settings; implement those only against a real supported upstream interface. [Configuration research](../research/acp-configuration.md) records stable generic options, dependent updates, conditional adapter controls, and limits of the current evidence.

Because options can arrive during upstream session setup, an idle provisional session before the first prompt is a proposed implementation path. Treat setup as real process/session work with cancellation and cleanup, not as a model request merely to populate a picker. Exact initialization and discard behavior needs adapter validation.

Keep requested configuration distinct from agent-acknowledged effective configuration. If an option is rejected or changes during initialization, show the result and require the user to resolve an incompatible selection before dispatching the prompt. If a provider exposes only an opaque default, label that limit rather than claiming a chosen value took effect. A connection that lacks required settings does not earn full integration-parity status merely by starting a session.

Persist the chosen agent identity, applicable non-secret configuration, and effective configuration associated with each run. Reattachment shows the same settings. Explicit resume after server restart rechecks availability; it must not silently replace a missing model, context tier, or permission mode. Authentication credentials require a separate storage and provider-setup design, not inclusion in transcript/configuration snapshots.

## Permissions and interaction

Permissions describe what the upstream harness can actually enforce. Label workspace/file access, command execution, network access, approval behavior, or other dimensions only where the provider supplies them; different agents need not share one permission enum. Do not equate a UI selection with a sandbox, and never advertise a restriction implemented only as prompt text as an enforcement guarantee. Internal mode IDs are not proof of effective permissions; inspect their mappings before classifying a run as read-only or exempting it from the writer queue.

Agent approvals and questions appear as structured actionable activity with enough context for a decision and inspectable full detail. Multiple clients share the same request identity; resolving it once invalidates stale controls elsewhere. No connected UI is not an approval. The selected permission policy remains active while detached, and pending requests wait when that policy requires user input. Repository instructions and agent policy can still restrict a requested action.

Pending [questions](questions.md) occupy a fixed area above the prompt, with question tabs and Back/Next navigation for batches. The prompt draft and configuration strip remain available. Answer submission targets the request; ordinary prompt submission retains its queue behavior. Native blocking requests hold their requesting run, while a verified asynchronous path allows it to continue and receive the answer later. A declared turn-ending fallback uses the same question UI and request-specific submission but may start a correlated upstream continuation turn; its settings and scheduling must be captured and validated before dispatch. None of these routes changes the selected permission policy or converts a question into an approval.

Use distinct [approval cards](activity.md#approval-cards) in the same area for permission requests. Show the action and its supplied targets, preserve the agent's supported choices and their scope, and open full details in Activity. Approval controls leave the prompt draft intact and never invent broader permission options.

“Resolve with agent” retains its explicit product scope: edit conflicted files and run checks, then stop for review before staging or continuing. A broad provider permission mode does not expand that workflow's authorization. The implementation must document what can be enforced through the chosen harness and detect unexpected repository changes; ACP negotiation alone cannot constrain arbitrary subprocess writes.

## Prototype and integration work

Local draft configuration, atomic first-send creation, idle composer selections and explicit editing of queued-item settings are accepted scope. App-level defaults persistence now initializes only fresh local drafts; see [App Agents settings](settings.md#new-thread-defaults). Existing drafts and queued captures remain independent. Mutating an already executing run and preset management remain later design details; do not silently switch the thread's agent or change an active run. Applying each queued item's recorded settings before execution needs adapter validation. Exact bindings are explicitly deferred to the interactive prototype.

Conformance cases must cover dependent-option invalidation, default visibility, unsupported controls, rejection after selection, effective-value persistence, detached approval requests, two clients answering one request, resume with removed options, and provider-specific permission limitations. Source research and live adapter evidence must stay separate. Real provider conformance has not been run; Demo and renderer checks do not establish it.

## Composer display refinement (2026-09-19)

Use clean connection and model names, without inventing capabilities from their spelling. Claude, Codex, Copilot and Grok are the user's display examples, and GPT-6-Astra illustrates model presentation; these are not a supported-provider inventory. Show optional Fast only when supplied and applicable. Effort display names such as Extra High, Max, Ultra or Ultracode must map to supplied options; Claude-style reasoning and context controls likewise require verified connection capabilities. Preserve upstream option identities behind display labels. Actual provider menus and integrations remain deferred.

The fixture footer uses Demo, Reference, Medium and Simulated as display labels for its synthetic selection; other selected fixture efforts retain their corresponding labels. These labels do not establish provider enforcement. Unknown context stays unknown. See [composer action placement](layout.md#controls-and-composer-refinement-2026-09-19).

The Go wire uses `thread.start` with project identity, explicit agent, captured settings, text and attachments. Its receipt names the created thread. `thread.create` remains a legacy API, unused by the new-thread UI. Real Codex/Claude support remains [implementation step 4](implementation.md#vertical-slices).

## Versioned models and model-scoped speed — 2026-09-22

Claude model choices show the runtime-resolved version beside moving aliases
and offer pinned resolved IDs. Explicit legacy entries come from documented
provider IDs and a versioned reference catalog; label them Legacy and retain
native selection errors. A listed legacy ID is not a claim of account access.
Do not invent model IDs, prices, capacities or capability parity from names.
Codex continues to use its account/runtime `model/list` catalog.

Effort picker rows use short names (Low, Medium, High and other reported
levels), with no paragraph embedded in the choice. Model-scoped effort/speed
values carry their applicability through the bridge and server. Selecting a
model preserves compatible idle preferences, resets incompatible values to an
offered choice or unavailable, and never edits queued prompt captures. Standard
speed remains a visible selectable control when the model also offers Fast;
responsive overflow retains access. Captured speed is applied and acknowledged
before dispatch, and native effective-state readback is required where a generic
control response does not prove that it applied.
