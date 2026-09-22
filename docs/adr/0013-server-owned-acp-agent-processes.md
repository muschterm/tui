---
status: accepted
---

# Run ACP agents as server-owned child processes of user-installed adapters

2026-09-22 follow-up: [ADR 0014](0014-acp-boundary-official-agent-runtimes.md)
retains server ownership and ACP but reopens the exact provider adapter choice.
The packages below describe the existing prototype, not a final SDK/CLI or
adapter-ownership selection. Authentication inheritance observed in a run is
not by itself evidence of permitted subscription use in a distributed product.
[ADR 0015](0015-app-owned-question-contract.md) adds the app-owned question
contract; general native question delivery is not implemented by this slice.

The first real agent integration needs three decisions that ADR 0002 left
open: who installs the ACP executable, who owns its process, and what survives
a server restart.

The background server launches each agent as a child process over ACP stdio,
one process per thread session, using the community Go SDK
`github.com/coder/acp-go-sdk` (pinned, protocol version 1). The executables are
user-owned: the pinned `claude-agent-acp` and `codex-acp` npm packages are
resolved on the server's PATH or through an explicit environment override, and
the application never downloads, installs or updates them. Authentication is
whatever those adapters find in the user's existing CLI login; the ADE offers
no login flow and shows a reported authentication requirement as an honest
unauthenticated state. This keeps supply-chain and credential handling in the
user's hands at the cost of a manual install step and a visible "unavailable"
state when the executable is missing.

Process ownership follows the server-ownership decision: a TUI disconnect
changes nothing, server stop cancels the turn and ends every child, and a
server restart cannot reattach to a process it no longer has. Threads that were
running or waiting therefore become interrupted and require explicit Resume,
which clears the dispatch gate. The next queued prompt starts a fresh process
and uses `session/load` only when the agent advertised it; otherwise a new session is created and the thread shows that
upstream context was not restored. No prompt is ever resubmitted automatically,
and the fixture's opt-in restart continuation does not extend to ACP threads.

Configuration is discovered, not hardcoded: a probe creates a provisional
session to read the agent's config options, the composer offers only those
values, and every captured setting is applied with `session/set_config_option`
before the prompt so the acknowledged option state, not the request, is
recorded as the effective configuration. Client filesystem and terminal
capabilities are not advertised in this slice, so the agent's own direct access
governs file edits and the checkout writer queue remains future work. See the
[slice checkpoint](../implementation/acp-checkpoint.md) for the wire additions.
