# Go controls, composer and scrolling — 2026-09-19

The user's first implementation review requested clearer font icons and pane
states, T3-style tabs, Enter-to-send, a growing composer, and visible scrollbars.
The accepted behavior is recorded in [layout](../design/layout.md#controls-and-composer-refinement-2026-09-19).
No server lifecycle, agent integration or command-identity contract changed.

## Implementation

- Pane controls use distinct visible-open/closed Codicons, with maximize/restore
  variants, stable hit areas and descriptive focus/hover labels. Surface tabs,
  chooser, plan, children, tools, context, theme and send use the same font family.
  Nerd Font is the user-requested assumption; `TUI_GO_ICONS=ascii` is an explicit
  fallback. The program does not claim to detect the terminal's font.
- Tabs have disjoint icon and name targets. Hovering the tab or keyboard-focusing
  its icon reveals close. Name clicks select; icon-slot clicks close. The overflow
  control appears only for hidden tabs, keeps the active tab visible and lists
  each surface once. Delete closes the selected tab-menu row. Closing a Terminal
  still addresses its server session; hiding a pane preserves it.
- Enter submits the composer; Shift+Enter inserts a newline, with Ctrl+J as the
  legacy fallback and Ctrl+S retained for submission. Multiline paste never sends.
  The composer measures upstream textarea wrapping and grows from one to eight
  visible rows, with a smaller limit in short windows to retain fixed controls.
  This limit does not truncate the draft or prevent further newlines.
- Proportional scrollbars appear for overflowing transcript, inspector, request
  text, composer/answer, navigation, bottom output and menus. Tracks page and thumbs
  drag. One-row tracks have no vertical travel; wheel/keyboard remain available.
  Input read-scrolling uses independent presentation state, preserving insertion,
  selection and text. Editing returns to the insertion point. Background snapshots
  with unchanged input geometry do not reset that reading position.
- Mouse routing continues to measure without painting, and the paced frame wrapper
  remains in place. Scrollbar rows share one keyboard focus stop. Modal menus hide
  underlying hit targets, including their scrollbars.

The pinned Bubbles 2.2.1 source exposed two integration traps: `MaxHeight` can also
limit logical-line insertion, and shallow textarea copies share a viewport pointer.
The implementation keeps the draft limit separate from display height and uses
fresh inputs for measurement/manual-read rendering. Cached measurements and full
read views avoid rebuilding them for every wheel event. A manually scrolled input
uses a static cursor snapshot until editing or selection refreshes it.

## Sources and font evidence

The [recorded local T3 reference](t3-code-design.md) informed the controls and icon
swap. `PanelLayoutControls.tsx` and `RightPanelTabs.tsx` were reinspected locally.
The glyph registry follows the primary [Nerd Fonts 3.4.0 glyph map](https://github.com/ryanoasis/nerd-fonts/blob/v3.4.0/glyphnames.json),
using its Codicons family. Only code points are referenced; no font or vendor
artwork was added to the repository and no new Go dependency was introduced.

The selected pane/tab/control code points were checked against the installed
`JetBrainsMonoNerdFontMono-Regular.ttf` cmap and are present. PNG review uses that
font, reported by FreeType as `JetBrainsMono NFM / Regular`. This checks the local
font file, not the user's current Ghostty font configuration. Provider identities
remain separate from these interface symbols.

## Validation

Environment: macOS, darwin/arm64, Apple M4 Max, pinned Go 1.27.1.

| Check | Result |
| --- | --- |
| `GOCACHE=/tmp/tui-go-build make check` | PASS: gofmt, vet, race tests, native build |
| Added interaction tests | PASS: tab overflow/selection/closing, pane glyph variants, modal routing, scrollbar paging/dragging, keyboard focus, Enter/newlines, preserved multiline drafts, Unicode, resizing, input reading and snapshot continuity |
| Composer layout matrix | PASS: dark/light at 160×50, 120×40, 80×30, 60×24 and 48×22; controls within bounds, full draft retained |
| `python3 scripts/pty_smoke.py --artifacts /tmp/tui-controls-pty` | PASS: 22 assertions, including CSI-u Shift+Enter and Ctrl+J newline delivery, Enter submission, menu-based terminal close, mouse controls, suspend/resume, draft isolation and server lifecycle |
| `python3 scripts/scroll_benchmark.py --events 500 --output /tmp/tui-controls-scroll` | PASS: 26.9 ms to alternate-screen exit after 500 wheel events; 79.8 ms complete TUI-child CPU; theme saved and server survived |
| Visual review | Six deterministic captures inspected: dark/light tabs, overflow, single-row menu, eight-row composer and narrow layout |

Run these commands from `apps/go`. PTY runs use private temporary application homes
and approved loopback/process access. The user's server and stored views were not
used or stopped. The burst timing is one local sample, not a performance guarantee.

[Captures and regeneration](go-controls-captures/README.md) preserve the actual ANSI
source along with PNG/SVG. They are rasterized Go render output, not screenshots
of Ghostty. Native Ghostty/font/modified-key negotiation remains for interactive
review; the PTY checks establish parser/application behavior. Existing complex
emoji pointer-positioning limitations in the pinned textarea remain.

The binary is rebuilt. Relaunch the TUI with the same client identity to review the
changes; the running server can remain attached and needs no migration or restart.
