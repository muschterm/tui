---
status: accepted
---

# Keep execution defaults server-owned and recovery explicitly opt-in

The user requested shared app/project workspace defaults and an off-by-default
Continue threads after restart setting. Storing these beside frontend geometry
would let two clients disagree about server startup and thread creation. Always
resuming saved queues would contradict the accepted recovery default and could
repeat external actions after an uncertain provider delivery.

Persist revisioned environment settings with authoritative server state. Projects
inherit the environment workspace default unless explicitly overridden. Resolve
that preference only at creation; reject unsupported worktree creation rather
than silently changing its meaning. Theme and navigation remain client-local.
Require advertised capabilities and reject stale settings writes.

Preserve explicit Resume as the default after restart. Permit opt-in continuation
only when the execution integration can establish a recoverable active turn or
queue. Keep manual Stop, pending questions/approvals and unknown integrations
ineligible. The initial implementation proves only fixture recovery; a future ACP
adapter needs its own delivery/reconciliation evidence. This adds eligibility
state and tests, but avoids treating persistence as proof that upstream side
effects are safe to replay. See the [settings behavior](../design/settings.md).

Confirmed project removal deletes its application-owned threads while keeping
checkout files. Use one transaction for snapshot, related view/payload cleanup
and command receipt. Retain minimal retry fingerprints to reject/reconcile old
commands without resurrecting deleted history. Re-registration gets a new project
identity, preventing old revision-bound confirmations from targeting a new
incarnation of the same folder. This identity/tombstone cost is accepted to make
multiple clients and lost acknowledgments safe.
