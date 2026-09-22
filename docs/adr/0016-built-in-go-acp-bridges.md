---
status: accepted
---

# Ship Go provider bridges inside the application

Implement the ownership selected in [ADR 0014](0014-acp-boundary-official-agent-runtimes.md)
as internal Go bridges in the same application binary. Keep serialized ACP v1
over bounded in-memory pipes between the server's existing ACP client and each
bridge; each bridge directly owns an installed `claude -p` or `codex app-server`
child. This avoids another end-user executable or Node/SDK sidecar while keeping
provider protocols outside application persistence and frontend code. Explicit
external/native ACP executables remain supported.

Compared with internal subprocess subcommands, this reduces startup/process
layers but gives the bridge the server's failure domain. Bound provider frames,
callbacks and retained state; own cancellation and process-group teardown.
ACP framing does not imply isolation. Preserve the existing durable dispatch,
request-specific answer handling and uncertain/no-replay recovery boundary.
Our adapter identities and question dialect are versioned independently from
the historical third-party adapters; their live evidence is not inherited.
See the [implementation checkpoint](../implementation/go-adapter-checkpoint.md)
for actual coverage and gaps.
