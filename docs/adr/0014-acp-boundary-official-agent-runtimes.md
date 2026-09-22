---
status: accepted
---

# Keep ACP at the agent boundary and use official agent runtimes

**Adapter ownership selected, 2026-09-22:** the user rejects separate adapter
installations and selects our own Go adapters delivered with the application.
Host installed `claude -p` directly through its JSONL/control protocol and
installed `codex app-server`, translating each to ACP. No npm/Node ACP adapter
or separate Agent SDK sidecar is required by the target design. This trades
upstream adapter reuse for owning translation, compatibility tests, permissions,
request correlation and process lifecycle, in exchange for the requested
installation experience. Exact Go packaging/process organization remains an
implementation decision; the [handoff](../implementation/agent-integration-handoff.md)
records the next slice. The current external-adapter prototype is preserved
until replacement, and its evidence does not establish Go bridge parity.

**Local installation requirement, confirmed 2026-09-22:** always use the user's
installed Claude/Codex CLI. Resolve `claude` / `codex` on the server's PATH and
pass the absolute executable to the ACP adapter for every probe and session
launch. Explicit local executable overrides remain available. Missing or invalid
executables make the integration unavailable; never fall back to an adapter's
bundled runtime or download another copy. The adapter translates protocols and
does not choose the provider installation. Native ACP agents remain direct.

Confirmed 2026-09-22: retain ACP v1 between the application server and agents,
with adapters for providers that do not implement it natively. Frontends use
the application's authenticated HTTP/WebSocket contract, not raw ACP; the
server owns persistence, queues, request identities and recovery. This reaffirms
[ADR 0002](0002-acp-agent-boundary.md) and
[ADR 0003](0003-background-server-attachable-clients.md).

Use the official local Claude Code runtime and official Codex App Server behind
that boundary. Claude's Agent SDK is an eligible implementation because it
runs a local Claude Code binary; compare it with direct `claude -p` integration
and verify that the intended authentication/use is permitted before selecting
it. SDK versus CLI is not a subscription-versus-API billing distinction.
Codex's selected backend is `codex app-server`, not `codex exec`.

The earlier evaluation compared existing ACP adapters with custom translation
and favored reuse for the prototype. The subsequent installation requirement
now justifies owning the Go adapters, using that versioned comparison as evidence.
The earlier open Claude-route and ownership question is resolved by the Go
adapter decision above. The pinned
packages in [ADR 0013](0013-server-owned-acp-agent-processes.md) remain the
current prototype, not a final selection established by this decision.

Provider-specific translation stays outside the generic UI. Preserve useful
capabilities through explicitly negotiated, versioned extensions, without
claiming native ACP support, universal feature parity or portable histories.
Keep authentication with the official runtime; do not introduce our own
subscription-token handling or silently switch subscription users to billed API
credentials. See the [evidence](../research/agent-integration-options-2026-09-22.md)
and [implementation handoff](../implementation/agent-integration-handoff.md).

## Evaluation checkpoint — 2026-09-22

The versioned [Claude](../research/claude-integration-matrix-2026-09-22.md) and
[Codex](../research/codex-integration-matrix-2026-09-22.md) comparisons inspect
installed releases, runtime selection and primary authentication guidance.
The delivery-correctness slice retains the existing adapters; no new bridge or
authentication route is selected. Claude's existing form-elicitation bridge
favors reuse; Codex's lost blocking/turn metadata and steering fallback require
focused fixes before exposure. Final adapter patch ownership and Claude product
eligibility remain open. This records implementation status, not a new accepted
product decision or a claim of verified native/async questions.

## Earlier personal prototype continuation — 2026-09-22

The following reuse choice is superseded by the subsequent Go adapter decision
above; its implementation and validation remain historical evidence.

The user selected personal local use with their existing official CLI login as
the next milestone. Retain the pinned existing SDK-backed Claude adapter and
Codex App Server adapter for the [dispatch/recovery slice](../implementation/agent-recovery-checkpoint.md):
native Claude form translation already works, while replacing both bridges
would add maintenance without resolving the application persistence defects.
Explicit installed-runtime selection and live native Claude answer behavior
are now recorded in the [evidence](../research/agent-recovery-live-2026-09-22.md).
This selects reuse for the personal prototype, leaving distributed-product
eligibility and future adapter patch ownership open. No authentication or
billing change follows; Codex questions/steering and true async input remain
unavailable until their respective contracts pass validation.
