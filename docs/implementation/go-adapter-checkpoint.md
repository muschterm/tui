# Built-in Go adapters — 2026-09-22

Two bounded Go provider slices continue the latest agent handoff. The initial checkout
was clean: the formerly uncommitted delivery/recovery work is already present.
Preserve that implementation and its historical fixtures/evidence.

## Packaging and scope

The application embeds Go ACP bridges in `internal/acpbridge`. The server's ACP
client exchanges serialized ACP v1 over in-memory pipes with each bridge. Each
bridge owns one installed official CLI child, started in the checkout. This
keeps provider translation outside the application protocol and UI while
shipping one binary, with no translator installation or Node/SDK sidecar.
Native/configured external ACP executables remain supported explicitly.

The existing server remains responsible for durable-before-dispatch admission,
question/approval acceptance, generation checks, uncertainty and no replay.
No provider write or turn completion constitutes an authoritative answer receipt.
No billing/authentication configuration is changed. No code is copied from UCF;
its transport and versioned comparison records are read-only references.

Initial slices: Claude JSONL control initialization, model discovery, prompt,
stream, native tool permissions and bounded AskUserQuestion translation; then
Codex App Server discovery, prompt, stream, native approvals and interruption.
Unsupported question dialects, settings, steering and fallback fail explicitly.
Codex question metadata must not be discarded to fit the blocking Claude form.

The registry now defaults to `builtin:claude` and `builtin:codex`. Switching from
an external adapter invalidates its cached options/capabilities and arguments,
preserving thread state, prompt captures and queues. Existing explicit external
command overrides still work; missing installed runtimes fail before startup.

The question adapter identifies itself as `tui-go-claude 0.1.0` and negotiates
`tui-go.claude-questions.v1`. Its bounded form uses the existing normalized app
question contract and retains the original native control payload privately.
It does not impersonate the third-party Claude adapter. Stop/withdrawal and
native answer writes share a dispatch boundary; a cancelled-before-dispatch
connection is retired instead of letting a later prompt execute.

Native process disconnect, decoded terminal outcome, ACP pipe teardown and
confirmed process reaping are distinct. Output framing, callbacks, writes and
shutdown are bounded. Unix cleanup targets only the owned process group;
Windows remains a direct-process best-effort path. No automatic prompt/answer
retry or receipt inference was added.

[Live and regression evidence](../research/go-built-in-adapters-2026-09-22.md)
records passing Haiku native questions and Luna-low prompt/cancel/restart runs,
plus fake-runtime race coverage. Full child history, true async questions,
structured-text fallback, writer scheduling and authoritative receipts remain
gaps. Live approval/file-write, image, session-load and multi-client provider
checks remain separate work; earlier adapter evidence is not carried forward.

Live-test constraint from the user: Codex Luna at low effort and Claude Haiku
only. Select their exact IDs from runtime discovery; if unavailable, stop the
live scenario without substituting another model. Use the existing login and
billing configuration. Fake-peer tests remain the default for edge cases.
