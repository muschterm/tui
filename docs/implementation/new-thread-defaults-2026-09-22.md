# New-thread defaults slice (2026-09-22)

App Agents stores one optional server-owned agent ID and captured configuration.
The server validates a new selection against its current probed catalogue and
revision. It preserves omitted defaults from older app-settings clients. Fresh
client-local drafts copy the saved values once; existing drafts, threads and
queued prompts retain their own settings. If a saved value is later withdrawn,
the draft keeps it and Send validation reports the mismatch.

Assumptions: one app-wide default is sufficient for this slice; per-project
overrides and named presets are unresolved. Unmapped ACP fields remain agent
defaults and cannot be set through this UI. The Agent catalogue must report a
ready state and selectable values before saving an ACP default. Saving a default
is preference storage, not proof that the provider applied it; prompt dispatch
still validates and records the effective configuration.
