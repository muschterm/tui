---
status: accepted
---

# Bind steering to the active turn and captured queued input

The user requested explicit Steer on queued messages and inclusion in the shared
design. Promoting a message to the front of the queue or stopping and resubmitting
would change that intent: steering adds input to the same active execution. A
thread identity alone is insufficient because its current turn can end before
the command arrives, and queued text or settings can change concurrently.

Bind steering to a queued prompt/revision and an expected turn identity. Preserve
the queued capture in accepted conversation history, and reconcile delivery and
queue removal as one outcome. Reject stale targets and incompatible settings.
Keep normal Send queuing, explicit Stop and restart Resume separate. These
identities become part of the durable application contract rather than being
derived from the latest visible user message. See the
[shared behavior](../design/activity.md#steering-a-queued-message).

The Go fixture can append the input, remove its queue entry and record its receipt
inside the existing SQLite transaction. Real agent delivery crosses a process
boundary and may be uncertain; it requires adapter capability verification and
retained reconciliation state before it can meet this contract. We accept that
integration cost rather than invent support or risk duplicate input. Stable ACP
framing is not evidence of a steering extension. No custom adapter or universal
exactly-once provider-delivery claim is introduced by this decision.
