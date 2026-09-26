# OS-PTY harnesses and capture tools

These Python 3 scripts are validation tooling for the Go reference, not part of
the application or its Go module. `make check` does not run them; `make pty`
runs the eight harnesses in sequence against `bin/tui-go`, and each accepts
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
| `pty_terminal.py` | Embedded terminal: F5 opens a real shell in the bottom panel, click enters input focus, typed `echo hello-$((6*7))` prints `hello-42`, Ctrl+] leaves, hide/show keeps the session, tab close ends it; the child shell gets a temporary `HOME` and `HISTFILE=/dev/null` |
| `pty_git.py` | Git surface against the real server: a temporary repository with a local bare remote registered as a project; stage from the diff viewer (`s`), commit from the message editor, merge a conflicting branch from its row menu, Choose theirs, Mark resolved, Continue (verified with `git`), then Fetch and fast-forward Pull; Git identity comes from the temporary HOME |
| `pty_worktree.py` | Explicit worktrees against the real server: a temporary repository registered as a project; the new-thread draft's Workspace row chooses New worktree, shows the observed start commit, names the branch and Sends; the thread attaches to the created worktree (verified with `git worktree list`); after closing the thread through the API, Remove from the checkout details previews then deletes the worktree while the branch is kept |
| `pty_clipboard.py` | Right-click Copy/Paste, forwarded paste shortcuts, cursor insertion and selected-range replacement through isolated clipboard utility stand-ins; never accesses the real OS clipboard |
| `pty_acp.py` | Opt-in real ACP prompt, menus and cancel/resume; requires live login and consumes account quota |
| `live_agent_recovery.py` | Opt-in live HTTP Claude native question / Codex prompt, exact answer retry, Stop/Resume and restart; selects an installed official runtime explicitly |
| `render-capture.py` | Rasterizes deterministic Go View captures to PNG for `docs/research/*-captures` (needs Pillow) |
| `foot_capture.py` | Runs the binary in a dedicated foot window (Hyprland + grim) with scripted keys and screenshots only that window; real-terminal visual evidence, not a PTY assertion harness |
| `scroll_benchmark.py` | Wheel-burst input latency measurement |

Everything except `render-capture.py` and `pty_acp.py` uses only the standard library. Dated
results and retained artifacts are recorded under `docs/research/`.

`pty_clipboard.py` is a standalone check, outside `make pty`. Run it with the
same `--binary` and `--artifacts` options. It checks application mouse/key input
and clipboard utility routing, not whether Ghostty or a Linux desktop forwards
its own copy shortcut. See the [clipboard/context-menu follow-up](../../../docs/implementation/answer-status-copy-2026-09-23.md).

The live ACP harness is deliberately excluded from `PTY_HARNESSES` and `make
pty`. It skips unless `TUI_GO_LIVE_ACP=1`. Use the shipped Go bridges with installed
official CLIs on the server's `PATH`, as described in
[the Go slice](../../../docs/design/go-slice.md#acp-agents--2026-09-22).
The official CLIs use their existing login. The harness never changes the caller's home or server;
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
`make pty`. It requires `TUI_GO_LIVE_ACP=1` and an official runtime path; omit
`--adapter` for the shipped Go bridge, or provide an explicit external ACP peer
for historical comparison. `--model` selects a discovered model ID. It inherits the existing official login/configuration,
creates a scratch application home and checkout, and uses an exec wrapper to
verify which runtime the adapter starts. It never collects credentials or
grants permissions. Claude asks a synthetic native question, accepts Blue via
`request.answer`, then cancels a second waiting question and rejects a late
answer. Codex runs pong and cancels a long reply by default; `--questions`
exercises native `request_user_input`, including its supplied continued-work
mode. `--permissions` selects a discovered mode for these harmless prompts.
Both verify Resume/restart without queue replay and tracked process exit.

```sh
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent claude \
  --runtime /absolute/path/to/claude --artifacts /tmp/tui-question-check
```

`report.json` contains selected evidence fields. Inspect it before retaining it
in documentation. `private-process-records.json` contains local process IDs for
cleanup diagnostics and stays private. The process check waits up to six seconds
after server-stop acknowledgment; it covers the tracked adapter/runtime PIDs,
not arbitrary tool descendants. HTTP evidence is separate from PTY/UI evidence.
