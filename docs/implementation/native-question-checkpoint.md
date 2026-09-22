# Bounded native-question continuation — 2026-09-22

**Subsequent continuation:** [dispatch/recovery](agent-recovery-checkpoint.md)
adds personal-use auth verification and [live native-question evidence](../research/agent-recovery-live-2026-09-22.md).
Its results supersede this checkpoint's earlier live-question-unknown statements,
while receipt, broader shape and integration gaps remain explicit.

This builds on [request delivery](request-delivery-checkpoint.md), preserving
all prior uncommitted work and the [shutdown fix](../research/go-shutdown-2026-09-22.md).
It is a small native blocking-question slice, not completion of agent integration.

## Dependency choice and assumptions

The user's preference for fewer runtime dependencies prompted the focused
[direct-Go comparison](../research/claude-integration-matrix-2026-09-22.md#follow-up-fewer-dependencies-with-a-direct-go-bridge).
A direct bridge can remove Node, JS ACP SDK, Anthropic SDK and Zod. Either route
can select the installed official Claude binary and avoid a duplicate native
installation. Both execute Claude Code; they do not automatically expose the
same host capabilities.

Retain the existing adapter for this slice. A direct bridge would additionally
own JSONL controls, native question/approval responses, exact permission update
scope/restrictions, model/settings discovery, cancellation receipts, recovery
and process cleanup. Pinned SDK types make that feasible but no Go host has
passed those checks. This is an implementation choice for the current slice,
not a final distribution decision or a rejection of dependency reduction.
No runtime override, authentication or billing configuration changed. Subscription
product eligibility and exact approval-restriction parity remain open research
items; this work neither reads credentials nor substitutes API-key billing.

## Implemented boundary

- ACP v1 remains server-to-agent. For the configured Claude route only, initialize
  advertises `elicitation.form: {}`. Handling is further gated to the reported
  `@agentclientprotocol/claude-agent-acp 0.80.0` identity. The connection records
  `claude-questions-0.80.0`; it is not a generic ACP feature claim. Provider/version
  mismatch fails closed. Probe-only connections do not advertise forms.
- The pinned JS adapter uses `elicitation/create`; the Go SDK v0.13.5 generated
  unstable union describes an older dialect, so this method is dispatched
  explicitly. URL completion is not an answer receipt. SDK `$/cancel_request`
  cancellation propagates to the original callback context.
- `agent/elicitation.go` accepts only the pinned AskUserQuestion schema:
  1–4 indexed questions, 2–4 choices, single/multiple selection and optional
  per-question Other companion. No required fields are invented. Single choice
  OR Other is supported; choice plus a separate note is unavailable. Multiple
  choices plus Other are additive. Explicit empty answers omit optional fields.
- Reject duplicate JSON keys, repeated original question text, duplicate wire
  labels, label/value mismatch, unknown fields/constraints, previews, general
  MCP/URL forms, unsafe text and oversized payloads. Questions retain stable
  indexed IDs; exact unique labels are the upstream option values. App IDs cannot
  recover distinctions the adapter lost. Descriptions appear with question text.
- Input is bounded to 64KiB and JSON depth 16, with additional string bounds.
  One thread shares a 16-pending-request limit with approvals. Native admission
  stops beyond 128 retained requests or projected snapshot capacity; history is
  never silently evicted. Each pending native request reserves 128KiB for its
  maximum escaped accepted answer. Native legacy input retains only normalized
  `QuestionAnswers`, avoiding duplicate snapshots.

## Lifecycle and frontend

The source form and normalized request are saved before publication. The source
is retained as `Request.SourcePayload` in existing snapshot storage, stripped
from HTTP/WebSocket projections, and never reused as a live routing handle.
Connection generation, upstream session, original callback, tool-call identity
and application turn gate submission/handoff. Duplicate tool requests in a turn
are rejected. The pinned form has no upstream turn/child ID: this establishes the
original callback and application turn at admission, not independently verified
upstream turn provenance or child attribution. No missing identity is fabricated.

`request.answer` remains the sole frontend answer action. It validates the
current revision and dialect before durable `submitted` / `acp-accepted`, then
hands the committed submission identity to the original callback. Response
preparation saves `closed` / `acp-unconfirmed`. Successful callback return, pipe
write, tool update and generic turn completion do not confirm receipt. Competing
clients and retries use the existing durable command deduplication.

RPC withdrawal, Stop, turn end and disconnect close unanswered requests without
acceptance. An accepted answer is retained even if cancellation wins the handoff
race; it is not resent. A failed write followed by disconnect becomes uncertain;
restart turns accepted/unconfirmed legacy states into `acp-uncertain`, preserves
the snapshot/receipt and never replays it. Resume cannot reattach an old callback.

The existing question card, explicit Submit, keyboard/mouse controls, local
answer drafts and composer work unchanged. Generic delivery descriptions now
say request/upstream rather than approval for shared outcomes. There is no
Answered Q&A history until authoritative resolution evidence exists. Separate
per-request Decline/Cancel controls are not added; Stop cancels the turn.

## Remaining work

Live Claude question round trips, received-answer confirmation and descendant
cleanup are not established by this slice. The versioned converter fixtures and
fake ACP peers are separate from terminal and provider evidence. See
[validation](../research/native-questions-2026-09-22.md).

Codex 1.12.0 forms/continued-work questions and steering remain disabled: blocking,
turn and timeout metadata loss and new-turn steering substitution still require
correction and tests. Claude strict same-turn steering remains unavailable too.
Generic/child forms, previews, broader answer shapes and true async delivery
remain open. Structured-text fallback still requires explicit queue/writer
scheduling, captured settings and Resume gates; no fallback is enabled.
