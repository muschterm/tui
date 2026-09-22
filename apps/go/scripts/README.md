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
| `render-capture.py` | Rasterizes deterministic Go View captures to PNG for `docs/research/*-captures` (needs Pillow) |
| `scroll_benchmark.py` | Wheel-burst input latency measurement |

Everything except `render-capture.py` uses only the standard library. Dated
results and retained artifacts are recorded under `docs/research/`.
