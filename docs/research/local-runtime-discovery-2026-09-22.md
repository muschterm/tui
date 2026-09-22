# Installed CLI discovery — 2026-09-22

The user clarified that Claude/Codex must always use their local installation,
not an adapter-bundled runtime. [ADR 0014](../adr/0014-acp-boundary-official-agent-runtimes.md)
and the contributor instructions now record that requirement.

The server passes the configured provider identity on both startup/refresh
probes and thread-session launches. Before launching the adapter, `agent.Start`
resolves `claude` or `codex` with `exec.LookPath`, converts it to an absolute
path, and explicitly sets `CLAUDE_CODE_EXECUTABLE` or `CODEX_PATH` in the child
environment. Parent environment, authentication, configuration and billing are
unchanged. The existing optional overrides select a specific local executable;
missing/invalid overrides fail rather than trying another executable. There is
no runtime installation, download or bundled fallback. Native ACP agents do
not require either provider CLI.

This uses the exact override branches re-inspected in installed Claude ACP
0.80.0 (`claudeCliPath`, `dist/acp-agent.js`) and Codex ACP 1.12.0
(`startCodexConnection`, `dist/index.js:22098`). Those adapters still contain
bundled-runtime fallback code for other clients; our server always supplies
the resolved local path before starting them. Optional package dependencies
may still contain unused binaries; this change controls execution selection.

Tests exercise the actual process launcher with a stub adapter, observing the
override it receives in a different checkout. They cover PATH discovery for
both providers, explicit paths with spaces, invalid overrides despite a valid
PATH executable, absent/non-executable CLIs, relative-path resolution, unchanged
parent environment and unaffected native ACP connections. Missing runtimes
never start the adapter. Probe details and server logs expose the chosen path.

`GOCACHE=/tmp/tui-go-build STATICCHECK_CACHE=/tmp/tui-local-runtime-staticcheck make check`
passed: formatting, vet, pinned staticcheck, race suite and build. An initial
restricted server-test run could not bind loopback; the configured checks passed
with local socket access. No UI geometry changed, so the prior seven fixture
PTY results remain the terminal evidence; this change's fixture regressions
are covered by the full Go suite.

[Real startup-probe report](local-runtime-discovery-2026-09-22.json) uses an
isolated application home with **both runtime overrides unset**. The installed
Claude and Codex paths were discovered automatically; both adapters reported
ready and their probe details matched the executables found on PATH. The owned
test server stopped and both tracked adapter processes were reaped. No provider
prompt was sent. This is executable selection and session-initialization
evidence; earlier live prompt/question evidence remains separately recorded.

The server inherits PATH when it starts; changing a shell's PATH does not
change an existing server. Shell aliases are not executable discovery. Runtime
updates can change compatibility, which remains version-tested rather than
assumed. Adapter executable discovery is separate: the ACP adapters must still
be on PATH or explicitly configured. No personal server was stopped and no
existing uncommitted work was discarded.
