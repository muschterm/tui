# Terminal capabilities research

Source-checked: **2026-09-19**. These are upstream documentation findings and proposals, not runtime verification. No application or terminal matrix has been executed.

## Target scope and pending decisions

Accepted platform direction: macOS and Linux are required. macOS targets iTerm2 and Ghostty. Modern Linux is required; Ghostty and kitty are recommended candidates, with the exact baseline pending. Windows Terminal is best effort and does not block release. No universal minimum terminal or tmux version has been established.

**Q3 subsequently accepted:** preserve core application workflows over SSH/tmux through portable input, add kitty keyboard enhancements when available, and include optional inline graphics in the initial design. Later visual corrections include image attachments, image/artifact previews, and verified identity marks, while rejecting invented user/agent avatars. Limits and exact backend support remain implementation work; bindings are explicitly deferred to the prototype, with selection semantics and clipboard checks still required. Research proposals beyond those settled requirements remain subject to the interview.

## Go baseline and capability distinctions

The current stable set is Bubble Tea **2.0.9**, Bubbles **2.2.1**, and Lip Gloss **2.0.6**, using `charm.land/.../v2` imports. These are candidate pins, not installed dependencies. Bubbles 2.2.1 requires Go 1.25.0 and depends on Bubble Tea 2.0.8 and Lip Gloss 2.0.5; the proposed patch upgrades fit that v2 dependency family. [Bubble Tea releases](https://github.com/charmbracelet/bubbletea/releases), [Bubbles manifest](https://raw.githubusercontent.com/charmbracelet/bubbles/v2.2.1/go.mod), [Lip Gloss releases](https://github.com/charmbracelet/lipgloss/releases).

Keep these features separate:

| Capability | Purpose and boundary |
| --- | --- |
| Styled cell rendering | Text, colors, borders and layout; sufficient for a polished shell |
| Mouse reporting | Clicks, wheel and dragging; independent of kitty keyboard/graphics |
| Kitty keyboard protocol | Disambiguates modified keys; optionally reports repeat/release |
| Kitty graphics protocol | Transfers and places raster images; independently negotiated |

The kitty specifications define [keyboard enhancements](https://sw.kovidgoyal.net/kitty/keyboard-protocol/) and [graphics](https://sw.kovidgoyal.net/kitty/graphics-protocol/) separately. [Ghostty documents both](https://ghostty.org/docs/features); [iTerm2 recommends application-negotiated kitty keyboard reporting](https://iterm2.com/documentation-preferences-profiles-keys.html). Neither establishes this project's tested behavior.

Bubble Tea v2 returns `tea.View`; `AltScreen` selects full-screen operation. `MouseModeCellMotion` provides click, release, wheel and held-button motion; `MouseModeAllMotion` additionally reports hover. Coordinates are zero-based cells. `WindowSizeMsg` reports resizing. Basic keyboard disambiguation is requested automatically; use `KeyboardEnhancementsMsg` to learn negotiated support and request additional flags only when needed. [Bubble Tea API](https://pkg.go.dev/charm.land/bubbletea/v2).

**Implementation proposal:** own layout geometry, hit testing, focus, and drag capture in the shell. Begin splitter capture on press, update on motion, and clear on release/cancellation. Test lost releases and resizing during capture. Record independent capabilities and offer diagnostics; terminal names and environment variables alone cannot prove support.

## SSH and tmux boundaries

Screen applications need a PTY; `ssh -t host command` requests one. SSH transport does not establish support in the outer terminal or intervening multiplexer. [OpenSSH manual](https://man.openbsd.org/ssh.1).

tmux documents xterm-style `extended-keys` and `extended-keys-format` values `csi-u` and `xterm`; CSI-u encoding alone does not prove the complete kitty protocol. Mouse bindings can intercept events; `send-keys -M` forwards them. `focus-events` and clipboard settings affect forwarding. `allow-passthrough` permits wrapped output sequences, not universal input-protocol compatibility. [tmux manual](https://man.openbsd.org/tmux.1).

The kitty keyboard follow-up [PR #5405](https://github.com/tmux/tmux/pull/5405) closed on August 18, 2026 without a merge shown; discussion deferred release-event support. Proposal code must not be treated as released support. Recheck concrete installed builds before setting a baseline.

For the accepted optional images, remote clients need direct, chunked transmission instead of remote filesystem paths. Unicode placeholders plus escape passthrough offer a tmux strategy, with additional layout and cleanup work. Probe graphics separately and retain a usable non-image representation. [Graphics specification](https://sw.kovidgoyal.net/kitty/graphics-protocol/).

## Shortcuts, selection and clipboard

Legacy Ctrl+Space aliases Ctrl+@ as NUL. macOS may intercept it for input-source switching; terminal mappings or a tmux prefix can also consume shortcuts. Provide configurable bindings and clickable access. Alternative keys remain undecided. [tmux modifier guide](https://github.com/tmux/tmux/wiki/Modifier-Keys), [Apple input-source shortcuts](https://support.apple.com/en-mt/guide/mac-help/-mchlp1406/mac).

Application selection and terminal-native selection compete for mouse gestures. Ghostty exposes mouse-reporting, Shift-capture, copy-on-select and clipboard policies; iTerm2 exposes mouse-reporting and paste-bracketing controls. Define the application's selection/copy actions and document how terminal selection remains accessible; do not assume one universal modifier gesture. Test denied clipboard access, local versus remote clipboard destinations, and bracketed paste. [Ghostty configuration](https://ghostty.org/docs/config/reference), [iTerm2 terminal preferences](https://iterm2.com/documentation-preferences-profiles-terminal.html).

## Verification matrix

| Target | Paths to exercise | Status |
| --- | --- | --- |
| macOS: iTerm2, Ghostty | Local, SSH PTY, local tmux, remote tmux | NOT RUN |
| Linux: terminal baseline pending | Same paths; Ghostty/kitty candidates | NOT RUN |
| Windows Terminal | Best-effort applicable paths | NOT RUN |

For each accepted combination, record exact versions/configuration and test keyboard-only and mouse workflows, splitter capture, scrolling, focus, Ctrl+Space and fallback, paste/IME, selection/copy, narrow sizes, Unicode widths, color degradation, suspend/resume and terminal restoration. Include unanswered capability probes and conflicting tmux bindings. Verify optional graphics separately. Automated event/layout tests complement real interactive checks; they cannot establish end-to-end terminal compatibility.
