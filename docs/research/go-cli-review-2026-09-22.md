# Go slice CLI and standards review — 2026-09-22

The user asked for the Go reference to follow good Go standards, to be
CLI-first with help and completion, and for the Python and `.mjs` files mixed
into `apps/go` to be justified or moved. This records what was found, what
changed and how it was verified. Work is uncommitted on `feature/init`; no
remote Git operations; no user server or application home was touched.

## Findings

| Area | Finding | Outcome |
| --- | --- | --- |
| Command line | A hand-written `flag` parser in `cmd/tui-go/main.go`: one usage text, no per-command help, no completion, no version, exit 1 for everything, `TUI_GO_HOME` only as an environment variable, no tests. | Replaced by `internal/cli` on cobra 1.10.2 with `Main(ctx, args, stdout, stderr) int`; see below. |
| Package docs | `cmd/tui-go` and `client` had no package comment. | Added. |
| Exported identifiers | 86 exported types, functions and methods without doc comments across `client`, `lifecycle`, `protocol`, `shell`, `storage`, `fixture`, `server` and `tui`. | All documented. Remaining undocumented exports are interface methods on unexported types (`pacedModel`, `usageError`) and `protocol.Error.Error`, which are conventional. |
| Declaration spacing | 96 top-level declarations had no blank line before them (generated-looking layout). | Blank lines inserted; groups of aligned one-line functions kept together. |
| staticcheck 0.8.1 | Three numeric HTTP status literals (ST1013), two mergeable declarations (S1021), the unused `Model.navigationRows` (U1000) and fourteen invisible Unicode format characters in a test file (ST1018). | All fixed; the bidi test uses `\u` escapes so the cases are readable. |
| Tooling | staticcheck and govulncheck were run ad hoc and were not pinned. | Pinned as go.mod `tool` dependencies; `make lint` (vet + staticcheck) joins `make check`; `make vuln` is separate because it fetches the vulnerability database; `make pty` runs the seven OS-PTY harnesses. |
| Non-Go files | `internal/probe/` held a build-ignored Go probe and a Bun `.mjs` probe: one-off feasibility evidence from 2026-09-19 inside a Go package directory. `scripts/` holds nine Python harnesses and capture tools. | Probes moved to `docs/research/go-feasibility-probes/` beside the note that cites them; the module now contains only application code. The harnesses stay in `apps/go/scripts/` (a conventional non-package directory, not mixed with Go packages) with a README; they are the only coverage of `lifecycle`, `client` and `tui.Run` against a real PTY, and `make check` still does not run them. |
| govulncheck 1.8.0 | No vulnerabilities. | — |

T3 Code was inspected for a CLI precedent and has none; see
[T3 reference](t3-code-design.md#command-line-surface--inspected-2026-09-22).

## Command surface

Documented in [the slice guide](../design/go-slice.md#command-line-interface--2026-09-22)
and decided in [ADR 0012](../adr/0012-go-cli-framework.md). Behavior changes
beyond help and completion:

- `--home DIR` on every command overrides `TUI_GO_HOME`; the TUI passes it to
  the server it starts.
- `version` and `--version` print the linker-supplied version or the module
  version plus the Go-stamped VCS revision and `-dirty` marker; the nested module
  is stamped (`devel+276f685cc42c-dirty` on this tree).
- Usage errors exit 2 with `Run 'tui-go <command> --help' for usage.`; failures
  exit 1; both print `tui-go: <error>` on stderr.
- `server status`, `server stop` and `snapshot` distinguish "no server is running
  for HOME" (new `client.ErrNoServer`, wrapped from the missing discovery file)
  from a stale or shutting-down server. `lifecycle.Stop` uses the same sentinel
  instead of `os.IsNotExist`, which would not have matched a wrapped error.
- The TUI refuses to start when stdin or stdout is not a terminal instead of
  handing a pipeline to Bubble Tea.
- `server start`/`status` JSON is unchanged (`state`, `pid`, `url`, `home`,
  `instance_id`; never the token), so the harnesses and existing notes hold.

Dependencies added: cobra 1.10.2, pflag 1.0.9 and mousetrap 1.1.0 (Windows
only) at runtime; `honnef.co/go/tools` 0.8.1 and `golang.org/x/vuln` 1.8.0 as
tool dependencies, which pull `x/tools` 0.50.0, `x/mod`, `x/telemetry` and
`BurntSushi/toml` into `go.sum` but not into the binary. `charmbracelet/x/term`
became a direct dependency for the terminal check.

## Validation

From `apps/go` on Darwin 27.0.0 arm64, Go 1.27.1:

- `make check` (gofmt, `go vet`, staticcheck, `go test -race ./...`, build): PASS.
- New `internal/cli` tests: help contents, usage exit codes and hints for
  unknown commands, flags and client names, `version`/`--version`, the four
  completion scripts, `status`/`stop`/`snapshot` against an empty home, `probe`
  JSON, home resolution and client-name validation.
- Manual: `--help`, `server --help`, `completion zsh`, `bogus` (exit 2),
  non-terminal launch (exit 1 with hint), and an isolated-home
  `status` → `start` → `status` → `snapshot` → `stop` → `status` cycle.
- `make pty` with all seven harnesses against the cobra binary, bash,
  `TERM=xterm-256color`, isolated homes: `pty_smoke` 38, `pty_navigation` 28,
  `pty_small_screen` 42, `pty_sidebar_settings` 27, `pty_steering` 11,
  `pty_path_completion` 23 checks and `pty_colors` 11 cases: all PASS.
- `make vuln`: no vulnerabilities.

Not run: native Ghostty/iTerm2, SSH, tmux or phone-device checks; Windows
builds (the app already required Unix locks and process groups).

## Left open

- Server status categories and exit codes beyond 0/1/2 remain unselected in
  the [server design](../design/server.md#command-and-shutdown-semantics);
  `status` still fails rather than reporting a structured stopped state.
- `TUI_GO_ICONS` remains environment-only; a `--icons` flag would need
  `tui.Run` to take an options value.
- The PTY harnesses are Python and outside `make check`. A Go-native PTY
  harness (for example behind a build tag with a PTY dependency) would let
  `make check` catch the drift the 2026-09-21 review found, at the cost of
  rewriting about 1,800 lines; not started.
- `internal/tui/model.go` is 1,350 lines and `actions.go` 680; splitting them
  is a readability question, not a correctness one, and was left alone.
- `apps/go` builds only on Unix; no build constraint says so, so a Windows
  build fails on `unix.Flock` rather than with a clear message.
