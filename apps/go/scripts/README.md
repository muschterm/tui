# OS-PTY harnesses and capture tools

These Python 3 scripts are validation tooling for the Go reference, not part of
the application or its Go module. `make check` does not run them; `make pty`
runs the seven harnesses in sequence against `bin/tui-go`, and each accepts
`--binary` and `--artifacts`. They drive the real binary through an OS
pseudo-terminal with an isolated `TUI_GO_HOME`, so no user server or home is
touched. They verify byte-level input and output handling, not GUI terminal,
SSH or tmux compatibility.

| Script | Purpose |
| --- | --- |
| `pty_smoke.py` | Shared `Terminal` helper plus the baseline lifecycle, input, mouse, resize, suspend/resume, multi-client, detach and restart/Resume checks |
| `pty_navigation.py` | Thread and project navigation, thread actions and deletion, including read-only SQLite inspection |
| `pty_small_screen.py` | Phone-width single-column layouts and the minimum size |
| `pty_sidebar_settings.py` | Settings takeover, scopes and project removal |
| `pty_steering.py` | Queued-message steering against the fixture turn |
| `pty_path_completion.py` | Project folder typeahead and inline `@` file mentions |
| `pty_colors.py` | Color negotiation with synthetic terminal replies |
| `pty_acp.py` | Opt-in real ACP prompt, menus and cancel/resume; requires live login and consumes account quota |
| `live_agent_recovery.py` | Opt-in live HTTP Claude native question / Codex prompt, exact answer retry, Stop/Resume and restart; selects an installed official runtime explicitly |
| `render-capture.py` | Rasterizes deterministic Go View captures to PNG for `docs/research/*-captures` (needs Pillow) |
| `scroll_benchmark.py` | Wheel-burst input latency measurement |

Everything except `render-capture.py` and `pty_acp.py` uses only the standard library. Dated
results and retained artifacts are recorded under `docs/research/`.

The live ACP harness is deliberately excluded from `PTY_HARNESSES` and `make
pty`. It skips unless `TUI_GO_LIVE_ACP=1`. Install the pinned adapters described
in [the Go slice](../../../docs/design/go-slice.md#acp-agents--2026-09-22)
on the server's `PATH` (or use its command overrides), and log in using the
adapters' own CLI setup. The harness never changes the caller's home or server;
its file-write permission probe targets only its newly created scratch project.

```sh
python3 -m venv /tmp/tui-acp-render-venv
/tmp/tui-acp-render-venv/bin/pip install pyte==0.8.2 Pillow==11.3.0
make build
TUI_GO_LIVE_ACP=1 /tmp/tui-acp-render-venv/bin/python scripts/pty_acp.py \
  --agent codex --exercise-controls --artifacts /tmp/tui-acp-codex
TUI_GO_LIVE_ACP=1 /tmp/tui-acp-render-venv/bin/python scripts/pty_acp.py \
  --agent claude --exercise-controls --artifacts /tmp/tui-acp-claude
TUI_GO_LIVE_ACP=1 /tmp/tui-acp-render-venv/bin/python scripts/pty_acp.py \
  --unavailable-only --artifacts /tmp/tui-acp-unavailable
```

Without `--exercise-controls`, a live run sends only the pong prompt. With it,
both agents exercise Stop/Resume; Claude also requests an explicit approval to
write `hello.txt`. A quota limit fails the run honestly; readiness only verifies
session initialization. `--unavailable-only` sets both adapter paths to missing
executables and sends no prompt. Captures inherit the caller's color environment
(including `NO_COLOR`). `.ansi` exports redact paths/emails; raw `.pty` and
`.screen.txt` artifacts remain private and must not be copied into documentation.
The ANSI files are pyte reconstructions, not native terminal screenshots; use
`render-capture.py` to produce PNG/SVG and compressed sources. See
[the retained evidence](../../../docs/research/go-acp-captures/README.md).

`live_agent_recovery.py` uses the standard library and is also excluded from
`make pty`. It requires `TUI_GO_LIVE_ACP=1`, an explicit adapter executable and
official runtime path. It inherits the existing official login/configuration,
creates a scratch application home and checkout, and uses an exec wrapper to
verify which runtime the adapter starts. It never collects credentials or
grants permissions. Claude asks a synthetic native question, accepts Blue via
`request.answer`, then cancels a second waiting question and rejects a late
answer. Codex runs pong and cancels a long reply; its questions remain disabled.
Both verify Resume/restart without queue replay and tracked process exit.

```sh
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent claude --adapter /absolute/path/to/claude-agent-acp \
  --runtime /absolute/path/to/claude --artifacts /tmp/tui-question-check
```

`report.json` contains selected evidence fields. Inspect it before retaining it
in documentation. `private-process-records.json` contains local process IDs for
cleanup diagnostics and stays private. The process check waits up to six seconds
after server-stop acknowledgment; it covers the tracked adapter/runtime PIDs,
not arbitrary tool descendants. HTTP evidence is separate from PTY/UI evidence.
