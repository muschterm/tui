---
status: accepted
---

# Build the Go reference as a CLI with cobra

The user asked for the reference to be CLI-first: a clean command line with
help and completion, not a TUI launcher with ad hoc arguments. The hand-written
`flag` parser gave one usage text, no per-command help, no completion, a single
exit code and no version. T3 Code has no end-user CLI to learn from.

Use cobra 1.10.2 for the `tui-go` command surface. It supplies subcommands,
per-command help, bash/zsh/fish/PowerShell completion and POSIX flags through
pflag, and its conventions are the ones Go users already know from `gh`,
`kubectl` and `hugo`. The cost is two small runtime dependencies (pflag, and
mousetrap on Windows only) and a framework whose idioms shape every future
command. We accept that over maintaining bespoke completion scripts.

The command surface lives in `internal/cli`, where `Main` returns an exit code
so it is tested without a process: 0 success, 1 failure, 2 usage error. Static
analysis is pinned as go.mod `tool` dependencies (staticcheck, govulncheck) so
`make lint` and `make vuln` are reproducible without adding build dependencies.
Non-Go validation tooling stays outside Go package directories.

This decision covers the Go reference only. Rust and Bun choose their own
idiomatic CLI tooling against the same observable behavior: lifecycle
commands, help, completion, version, exit codes and JSON status. Server status
categories and richer exit codes remain open in the
[server design](../design/server.md#command-and-shutdown-semantics).
