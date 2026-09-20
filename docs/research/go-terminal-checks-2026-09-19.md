# Go reference terminal checks — 2026-09-19

This records OS pseudoterminal input/output checks of the first fixture reference. It is not a claim of Ghostty, iTerm2, SSH, tmux, Linux, or Windows compatibility. No live agent or real embedded shell is exercised: the right and bottom terminal surfaces currently hold explicit synthetic server sessions.

## Environment and reproduction

- macOS 27.0 build 26A428, arm64.
- Go 1.27.1 darwin/arm64; Python 3.9.6 standard library; Apple Bash 3.2.57 with job control.
- Bubble Tea v2.0.9, Bubbles v2.2.1, Lip Gloss v2.0.6, pinned by the Go module.
- OS PTY configured with `TERM=xterm-256color`; this is a harness input, not evidence of negotiated terminal capabilities.
- tmux 3.6b is installed, but this check does not enter tmux or interact with an existing tmux server.

From the repository root:

```sh
cd apps/go
make build
python3 scripts/pty_smoke.py
```

The script needs permission to create OS PTYs and bind a loopback server. In the restricted agent environment it was run through approved escalation. It creates a separate application home under `/tmp`, uses independent saved client identities, stops its own server, and removes its own application home. Raw byte streams and a JSON report remain in a printed `/tmp/tui-pty-evidence-*` directory. `--artifacts PATH` chooses a retained output directory; `--binary PATH` selects the executable. It never attaches to the user's existing application home. The harness uses an interactive shell so Ctrl+Z exercises real job-control suspension instead of an orphaned process group.

## Evidence and scope

The final functional run passed **20 assertions**, recorded in `/tmp/tui-go-final-pty/report.json`, with raw streams `pty-a.pty` and `pty-b.pty`. These temporary artifacts remain local and are not repository fixtures. It covered automatic server start, alternate-screen entry, function-key controls, persisted draft/theme/layout, SGR pointer toggle/menu activation/divider dragging, responsive resize restoration, actual suspension/resumption, independent simultaneous clients, terminal cleanup, surviving background work, restart recovery, and explicit Resume. F6 was sent during the draft-preservation sequence; the harness does not independently assert its focused-control identity.

The terminal-surface check creates a synthetic session through the command menu, verifies that F3 hiding leaves its server state unchanged, then closes it through its explicit command and verifies the selected session becomes ended. This establishes the fixture close/hide distinction, not real child-process exit or multiple real PTY isolation.

The automated assertions inspect authenticated server snapshots and saved frontend views, rather than assuming that emitted keystrokes succeeded. The size sequence is 160×50, 60×22, 35×12, then 160×50. Resize checks establish that remembered preferences survive collapse; they do not establish visual polish. The Ctrl+Z check requires the shell's stopped-job report, then sends `fg` and verifies a subsequent persisted UI change. Exit checks look for alternate-screen restoration and SGR mouse disable sequences; they do not prove an emulator's complete display state.

Two TUI clients share one isolated server while retaining separate drafts and layout/theme state. After one client detaches, the authoritative revision must continue advancing. After explicit server stop/start, revisions must remain stable with `NeedsResume` set until an explicit Resume action. The reference's generated background work is synthetic.

## Unverified paths

The Mac GUI was locked, so no live Ghostty or iTerm2 window was used and no unlock was requested. Actual GUI mouse selection, clipboard delivery, font/glyph appearance, pixel graphics, hover appearance, and rendering polish are **NOT VERIFIED** by this harness. It injects SGR mouse protocol bytes; it does not establish how a particular terminal produces them. It does not respond to kitty keyboard/graphics negotiation, verify kitty placements, test SSH forwarding, or test tmux transport. Linux and Windows checks remain **NOT RUN**. Real embedded-terminal emulation and child-process lifecycle remain separate implementation work.

PTY captures may contain terminal controls and should be inspected with an escape-aware viewer; do not replay arbitrary bytes to a production terminal.
