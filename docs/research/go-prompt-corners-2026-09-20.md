# Prompt-style corners with contained fill — 2026-09-20

Historical experiments, superseded by the accepted [component rule](../design/components.md)
and [current Go component validation](go-components-2026-09-20.md). The first
section records the all-outlined pass; the [two-style split](#two-style-split)
records the subsequent stepped-fill experiment. Neither is the current treatment.

The user clarified that buttons/tabs should have the prompt outline's small
rounded corners and retain background fill inside that border. The previous
filled pills and thin curved end caps were implementation misinterpretations.
This pass uses the same box-drawing characters as the prompt: `╭─╮`, `│ │`, `╰─╯`.
Thread cards keep their dimensions and interior-only fill. Selected/hovered fills
do not color their border or corner cells with a square background.

At normal heights, question tabs/arrows/actions and surface tabs now occupy three
rows. Border cells use the surrounding background; interior cells carry the fill.
Terminal colors operate on whole cells, so this leaves the small gap between the
interior fill and the line within border cells. It does not claim arbitrary pixel
clipping or radius control. The font still determines each corner glyph's curve.

The implementation tradeoff is two extra rows per control strip. Question cards
retain their 12-row maximum and scroll their content sooner. Below 28 terminal
rows, controls use one-row rectangular fills to preserve content and the composer.
The inline Requests selector and surface overflow menu entries remain one row.
Plain icons and limited-color palettes share the same outline geometry. Surface
tab borders select; only the three-cell middle-row icon closes. These dimensions
are prototype choices awaiting the user's terminal review.

Validation on macOS darwin/arm64:

- `GOCACHE=/tmp/tui-go-build make check` in `apps/go` passed formatting, vet,
  race tests and build. Question tests cover one/three-row bounds, reserved arrow
  slots, overflowing Unicode labels, retained drafts, and mouse/keyboard navigation.
  Surface-tab tests include top, bottom and side border activation without closing.
- `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-prompt-corners-pty`
  passed all 38 checks, including question validation, radio/checkbox/free-text
  answers, explicit Submit, resize, detach and server recovery.
  [Retained report](go-prompt-corners-captures/pty-smoke-report.json).
- `python3 scripts/pty_navigation.py --artifacts /private/tmp/tui-prompt-corners-navigation-pty`
  passed all 18 checks, including metadata/border selection, Close/Reopen,
  draft preservation and existing confirmed Delete behavior across two clients.
  [Retained report](go-prompt-corners-captures/pty-navigation-report.json).
- Fresh Go View captures were generated with `TestPolishedViewCaptures`,
  `TestQuestionReviewCaptures`, `TestNavigationCaptures` and `TestColorReviewCaptures`.
  Visually inspected [dark](go-prompt-corners-captures/160x50-lightfalse-tabs.png),
  [light](go-prompt-corners-captures/160x50-lighttrue-tabs.png),
  [short](go-prompt-corners-captures/48x22-lightfalse-question-narrow.png) and
  [multiple-choice with text](go-prompt-corners-captures/160x50-lightfalse-question-multi-other.png)
  output rendered with JetBrainsMono Nerd Font Mono. Compressed ANSI/SVG sources
  are retained alongside the PNGs; these are render captures, not GUI screenshots.
- `git diff --check` passed. Reviewed this correction for data-loss and duplicate
  effects: it changes presentation and hit geometry, not persistence or dispatch.

No new dependencies or server changes. Native checks use isolated temporary
homes, not the user's running server. This is local PTY evidence, not a new
Omarchy/SSH/tmux compatibility claim. Provider and real embedded-terminal gaps
remain unchanged.

From the repository root, run `./apps/go/bin/tui-go` after detaching the old TUI.
The existing server can remain running. To rebuild: `make -C apps/go build`.

## Two-style split

The user clarified that both treatments are needed and explicitly confirmed:
thread cards and prompt use outline plus fill; buttons and tabs use background
fill without the line. The outer question/approval card keeps its outline.

The renderer now paints borderless controls with `▗▄▄▖` and `▝▀▀▘` edge rows
and a continuously filled text row. This is a small stepped corner on the cell
grid, not a smooth curve. It is an implementation approximation; smooth rounded
fill remains a visual integration gap, not something the user waived. The
[Unicode block-element chart](https://www.unicode.org/charts/PDF/U2580.pdf)
defines the half and quadrant shapes; it does not guarantee their appearance in
any particular font. Ordinary terminal style backgrounds remain cell based; the
[Lip Gloss API](https://github.com/charmbracelet/lipgloss)
provides separate foreground/background colors for border glyphs, not a pixel
clipping mask. No graphics protocol, image overlay or new dependency was added.

The fallback retains a visible outline when colors are absent or fill and outer
colors match. Short layouts and inline selectors retain their previous one-row
geometry. All existing icon slots, keyboard paths and edge hit targets remain.

Fresh [dark](go-prompt-corners-captures/two-styles/160x50-lightfalse-tabs.png),
[light](go-prompt-corners-captures/two-styles/160x50-lighttrue-tabs.png),
[short](go-prompt-corners-captures/two-styles/48x22-lightfalse-question-narrow.png) and
[16-color](go-prompt-corners-captures/two-styles/100x34-lightfalse-ANSI.png)
render captures were inspected. The fixed-cell PNG renderer uses font-drawn block
glyphs and shows seams at its chosen cell advance; these are not native Ghostty
screenshots or evidence of smooth corners. Computer-use access to Ghostty was
blocked by the tool's app safety restriction, so no GUI screenshot was obtained.
The local PTY harness remains usable and is distinct from that blocked GUI check.

`GOCACHE=/tmp/tui-go-build make check` passed formatting, vet, all race tests and
build. Existing tests were adjusted for the fill silhouette and compare text and
geometry across palette fallbacks. No new style-only test suite was introduced.
No persistence or command dispatch changed; this correction cannot resubmit work.

`python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-two-styles-pty` passed
all 38 checks with the new fill treatment; [report](go-prompt-corners-captures/two-styles/pty-smoke-report.json).
`git diff --check` passed. Relaunch `./apps/go/bin/tui-go` from the repository root;
the existing server can remain running. Smooth borderless corners and native
Ghostty visual review remain outstanding; the two-style mapping is implemented.
