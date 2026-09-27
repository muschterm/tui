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

**Amendment 2026-09-27 (read-only sessions).** A bridge can be opened
read-only (`acpbridge.OpenWith` with `OpenOptions.ReadOnly`, reached through
`agent.Options.ReadOnly`; other executables refuse it): its one session is
locked to the provider's read-only mode, Claude `plan` (launched with
`--permission-mode plan`, classifier review of planning commands off, no
bypass, and confirmed by Claude's initialize) or Codex `read-only` (App
Server `readOnly` sandbox with approval policy `never`), offers only that
Permissions value and refuses others; ordinary bridges never accept these
values. Only agent-planned rebase jobs use it
([ADR 0027](0027-agent-planned-rebase.md)).
