---
status: accepted
---

# Create threads and accept their first prompt together

New thread is now a client-owned creation draft. Create the server thread and
accept the initial captured prompt/settings/attachments in one revisioned,
retryable transaction at Send, rather than creating an empty thread and sending a
second command. This prevents rejected or retried sends from producing orphan or
duplicate threads; deletion retains the creation retry fingerprint. Likewise,
sending to a reviewed Closed thread atomically checks its lifecycle revision,
reopens it and accepts the prompt, while explicit Reopen only changes lifecycle.
Local drafts and pending receipts survive reconnect without automatic submission.
