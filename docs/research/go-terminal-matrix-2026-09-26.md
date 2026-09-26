# Go terminal compatibility matrix — 2026-09-26

Scope: implementation step 7 / the handoff item "real terminal/SSH/tmux checks of
terminals, editor, Git flows and kitty graphics" for the Go reference TUI. This
note records what was **verified on one Linux machine inside tmux**, what was
**not available**, and what remains **untested**. It is not a claim of general
tmux, SSH or emulator compatibility.

## Environment

| Item | Value |
| --- | --- |
| OS | Linux 7.2.5-3-omarchy (x86_64, Omarchy/Hyprland desktop) |
| Build | `go build -o <scratch>/tui-go ./cmd/tui-go` at commit `194f7cc` (working tree had uncommitted partial-staging files, untouched by the probes) |
| Toolchain | Go 1.27.1 linux/amd64; Bubble Tea v2.0.9, Bubbles v2.2.1, Lip Gloss v2.0.6, Ultraviolet v0.0.0-20260811164956-006e29f97886 |
| tmux | 3.7c, private servers only (`tmux -L tuiprobe-<random> -f /dev/null`), `default-terminal tmux-256color` |
| Outer terminal | a Python OS PTY running `tmux attach` with `TERM=xterm-256color`; the probe writes the bytes an outer emulator would send |
| Python / Git | Python 3.14.7 (stdlib only); git 2.55.0 |
| Installed emulators | **foot 1.28.0** only. kitty, Ghostty, Alacritty and WezTerm are **not installed** (`command -v` empty) |
| SSH | `sshd` binary present but `systemctl is-active sshd` = `inactive`; nothing listens on :22 |

Isolation follows `apps/go/scripts/harness_env.py`: every scenario gets a new
`/tmp/tui-tmux-*` app home and user `HOME`, `HISTFILE=/dev/null`, no
`ENV`/`BASH_ENV`, `GIT_CONFIG_GLOBAL` pointing at a temporary file with a
harness identity, and `CLAUDE_CODE_EXECUTABLE`/`CODEX_PATH` pointing at a
nonexistent path. The tmux pane starts in the temporary directory. The
developer's tmux server, `~/.config`, shell history and Git config were not
touched. Each scenario stops its server, kills its private tmux server and
removes its socket and home.

## Reproduction

```sh
cd apps/go && go build -o /tmp/tui-go ./cmd/tui-go
python3 docs/research/go-feasibility-probes/terminal-matrix/tmux_matrix.py \
  --binary /tmp/tui-go --artifacts /tmp/tui-tmux-art          # ~4 min, all scenarios
python3 docs/research/go-feasibility-probes/terminal-matrix/tmux_matrix.py \
  --binary /tmp/tui-go --artifacts /tmp/tui-tmux-art --only keyboard surfaces
python3 docs/research/go-feasibility-probes/terminal-matrix/pty_defect_repros.py --binary /tmp/tui-go
```

`tmux_matrix.py` drives input through the **attached tmux client**, so keys,
bracketed pastes, focus reports and SGR mouse events pass through tmux's own
input parser and forwarding rules. It records the application's output with
`pipe-pane -O`, grid state with `capture-pane -p [-e]`, and terminal modes with
`display-message` formats (`alternate_on`, `mouse_*_flag`, `cursor_flag`, …).
Artifacts per scenario: `<scenario>-*.txt`/`.ansi` captures, `<scenario>-pane-output.bin`
and `report.json`. Base tmux options unless noted: `focus-events on`,
`extended-keys on`, `mouse on`, `escape-time 10`. Note the tmux 3.7c defaults are
`extended-keys off`, `focus-events off`, `mouse off`, `allow-passthrough off`.

## Matrix (tmux 3.7c, final run)

| Area | Check | Result |
| --- | --- | --- |
| Startup | alternate screen on, any-motion SGR mouse (`?1003h ?1006h`), cursor hidden, title `tui-go` | PASS |
| Startup | requests `?2004h` bracketed paste, `?1004h` focus, `>4;2m` modifyOtherKeys, `>1u` kitty keyboard push + `?u` query, XTVERSION `>q` | OBSERVED (all sent) |
| Exit | Ctrl+Q → exit 0; tmux reports `alternate_on=0`, `cursor_flag=1`, all `mouse_*_flag=0`, keypad flags 0 | PASS |
| Exit | emits `>4m`, `<1u`, `?1049l`, `?25h`, `?2004l`, `?1004l`, `?1002l ?1003l ?1006l`, clears title | PASS |
| Exit | server keeps running after TUI exit (`tui-go server status`) | PASS |
| Resize | `resize-window` 160×45 → 60×22 (compact single column, composer and question card kept) → 35×12 (“resize to at least 40 ×…” notice, “Draft preserved”) → 100×30 → 160×45 (wide layout restored); no row wider than the window | PASS |
| Keyboard | Shift+Enter as CSI u `ESC[13;2u` (extended-keys on, format xterm and csi-u) → newline, not Send | PASS |
| Keyboard | Shift+Enter as `ESC[27;2;13~` (same two configs) → newline | PASS |
| Keyboard | Ctrl+J (LF) → newline, in all three configs | PASS |
| Keyboard | **tmux `extended-keys off` (the tmux default)**: both Shift+Enter encodings arrive as plain Enter and **send the prompt** | FAIL (tmux config limitation; see finding 3) |
| Focus | `ESC[I`/`ESC[O` forwarded by tmux with `focus-events on` do not leak into the draft | PASS |
| Paste | `paste-buffer -p -r` (bracketed, LF kept) → two draft lines, not sent | PASS |
| Paste | `paste-buffer -p` (bracketed, tmux default LF→CR) → **line break lost** (`first linesecond line`) | FAIL (app defect 2) |
| Mouse | tmux `mouse on`: left click on a thread card selects it (breadcrumb changes); wheel over transcript reaches the app, pane never enters copy-mode | PASS |
| Mouse | tmux `mouse off`: same click and wheel still reach the app (tmux forwards to a pane that requested mouse) | PASS |
| Color | `TERM=tmux-256color`, no `COLORTERM`: indexed 256 output, XTVERSION sent, tmux replies with its own name so no RGB/Tc query; first frame is 16-color until the profile arrives | PASS (256 as designed) |
| Color | outer `terminal-features xterm-256color:RGB` added: still 256 (the app never sees the outer terminal) | PASS (as designed) |
| Color | pane `COLORTERM=truecolor`: `38;2`/`48;2` output, no queries | PASS |
| Color | pane `NO_COLOR=1`: no SGR color at all | PASS |
| Graphics | inside tmux (`TMUX` set): no `ESC_G` APC emitted; the kitty probe is gated off (`graphics_query.go` multiplexer gate) | PASS (fallback) |
| Graphics | inside tmux with `TMUX`/`TMUX_PANE` unset: XTVERSION answered as tmux, so no `a=q` query and no `ESC_G`; text fallback | PASS (fallback) |
| Terminal surface | F5 in the fixture thread opens a real bash in the bottom panel; `echo hello-$((6*7)) $TERM` prints `hello-42 xterm-256color`; `printf` with `31m` and `38;2;1;2;3m` renders and the RGB SGR survives into the tmux grid | PASS |
| Terminal surface | Ctrl+] (0x1d via tmux) leaves terminal input; focus stays on the panel (“click or Enter to type”) and later keys do not reach the shell | PASS |
| Files editor | project registered via API; draft in that project; F3 → Files → `notes.txt`; typing appends ` typed-in-tmux`; autosave writes it to disk | PASS |
| Git | Git surface via the host tab-row add control; open `notes.txt` diff, `s` stages (`git diff --cached`), Esc, paste message, Enter commits (`git log -1` = `Commit from tmux`, harness identity) | PASS |

## Defects found (app, not tmux)

Ranked by risk to user work. Not fixed here, per the brief.

1. **Backspace on an empty composer misplaces the cursor.** After the draft
   becomes empty, one extra Backspace, then typing `ghijkl` and five Backspaces
   leaves `l` instead of `g`; a following Left+`X` gives `Xl`. Subsequent edits
   land at the wrong position, silently corrupting what will be sent.
   Reproduced **without tmux** in a plain OS PTY (`pty_defect_repros.py`, output
   `1 backspace-on-empty: DEFECT ['l']`). A fresh draft without the extra
   Backspace behaves correctly (`['g']`). Suspected area:
   `apps/go/internal/tui/input.go` `updateInput` Backspace path
   (`moveInputCluster` with no selection on an empty textarea) — unconfirmed.
2. **Bracketed paste drops lone-CR line breaks.** `ESC[200~d1\rd2ESC[201~` lands
   as `d1d2`; the same with LF keeps two lines. Reproduced without tmux
   (`2 bracketed paste with CR: DEFECT ['d1d2']`). Through tmux this hits every
   default `paste-buffer -p` (tmux converts LF to CR unless `-r`), and
   emulators that send CR for pasted newlines would be affected the same way.
   Multi-line pasted text silently becomes one line. The prompt path passes
   through `safe`/`safeKeepTabs` (`theme.go`), which removes control runes other
   than `\n`/`\t`; the editor path already maps lone `\r` to `\n`
   (`editor.go:333`). Root cause in the prompt path is inferred, not confirmed.
3. **Shift+Enter sends under tmux's default `extended-keys off`.** tmux cannot
   forward the modifier, so the app receives Enter and submits. The footer still
   advertises “Shift+Enter Newline”. Ctrl+J works everywhere. This is a tmux
   configuration limit rather than an app bug, but it can send an unfinished
   prompt; the documented baseline should state the `extended-keys on`
   requirement or the app could detect the missing enhancement (it does not
   receive a kitty-keyboard reply from tmux) and advertise Ctrl+J instead.

## Not available / untested

| Item | Status |
| --- | --- |
| SSH (loopback or remote) | **Not available on this machine**: sshd inactive; not set up per brief. SSH forwarding, fragmented replies over SSH and remote color/graphics remain untested |
| kitty, Ghostty, Alacritty, WezTerm | **Not installed**; untested |
| foot 1.28.0 | Installed, but has no headless/remote-control mode; driving it needs a visible Wayland window (`apps/go/scripts/foot_capture.py`), which this run did not use. **Untested here** |
| Real GUI keyboard/mouse/paste/focus generation | Untested: the probe injects the bytes an emulator would send; it does not show any particular emulator sends them |
| tmux `allow-passthrough on` + kitty graphics | Untested; the app implements no passthrough wrapping and gates graphics off under `TMUX`, so no difference is expected |
| Kitty graphics placements (supported path) | Untested: no kitty-graphics emulator available |
| Suspend/resume (Ctrl+Z) inside tmux | Not exercised (covered only by the plain-PTY `pty_smoke.py`) |
| Pointer divider drag / pane resize inside tmux | Not exercised (click and wheel only) |
| tmux nested in tmux, GNU screen, herdr | Untested |
| Git merge/rebase/conflict, pull/fetch inside tmux | Not exercised here (plain-PTY `pty_git.py` only) |
| Multiple tmux clients of different sizes on one session | Untested |
| Visual polish, glyph rendering, font fallback | Not assessed: capture-pane shows cells, not pixels |

## Limitations

- The attached client's outer terminal is a PTY, not an emulator, so tmux's
  view of the outer terminal is only `TERM=xterm-256color` plus the configured
  `terminal-features`.
- Thread and file targets were found by searching captured text; the checks
  assert effects (breadcrumb text, disk contents, `git` state, tmux mode flags),
  not focus identity.
- The keyboard scenario with `extended-keys off` submits two fixture prompts in
  its isolated home; this is expected and harmless.
- One run, one machine. Timing-dependent behavior (e.g. split query replies) was
  not stressed.
