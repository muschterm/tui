# Panel style handoff — 2026-09-23

**Start here** to continue restyling the Go reference app with the "panel" visual
language. The user asked for it after admiring an Omarchy Quickshell (Qt/QML, not
a TUI) WiFi panel: "do whatever works", then "apply this style to the rest of the
app too". Two commits on `feature/init` hold the work so far:

| Commit | Scope |
| --- | --- |
| `12b4b2d` Restyle settings as a control panel | Settings content, shared painters in `panel.go`, capture-renderer block drawing, text-sizing research |
| `d56fb9e` Apply the panel style across surfaces, menus and navigation | Right-host surfaces, chooser tiles, centered menus/dialogs, Closed heading |

## Read first

- `AGENTS.md` styling bullet (points at the panel constructs) and engineering priorities.
- [components.md › Settings panel constructs](../design/components.md#settings-panel-constructs--2026-09-23)
  and its "Panel style across surfaces" subsection: the rules this work follows.
- [Settings restyle note](../research/go-settings-panel-2026-09-23.md),
  [app-wide note](../research/go-panel-style-2026-09-23.md) and their `*-captures/`
  (including real foot screenshots under `foot/`).
- [Text sizing research](../research/terminal-text-sizing-2026-09-23.md): why
  there are no mixed font sizes.

## The visual language

Cell-native approximations of the Qt panel. There is one font size: foot 1.28
(the user's only installed terminal, their default) ignores OSC 66 `s=`/`n:d` and
DECDHL, and the pinned Bubble Tea v2/ultraviolet renderer strips both anyway.
Adding them would need renderer work and an ADR first; do not attempt it as
part of restyling.

- **Headings:** uppercase, bold, muted. Sections are separated by a full-width `─`
  rule in the line color.
- **Label/value pairs:** muted label at the left, bright value flush right. This
  applies only to short values: at most 32 cells and 40% of the width. Longer
  values stack, with a muted label row and the wrapped value below.
- **Banded square fill:** a `▄` row, the label row and a `▀` row, painted in the fill
  color over the canvas, so it reads as a padded button about two cells tall. The
  band belongs to the hit rect. It needs 256 or more colors and Nerd Font symbols;
  otherwise use the single-row `[ Label ]` fallback. Never paint solid blocks.
- **Segmented choice and tiles:** equal cells with a two-cell gap, via
  `panelSegmentLayout`. Each cell has its own hit rect and Tab stop. The focus mark
  `•` goes in the gap cell before it. Reselecting the current value writes nothing.
  If a label does not fit, fall back to a menu button or a list.
- **Toggle row:** the whole row is the control. The label is at the left; the
  On/Off word and a 6-cell track with a knob are at the right.
- **Status glyphs** (`panelStatusMark`):

  | Glyph | ASCII | Ink | States |
  | --- | --- | --- | --- |
  | `✓` | `+` | green | completed |
  | `●` | `*` | blue | active |
  | `○` | `o` | muted | pending |
  | `✕` | `x` | red | failed |
  | `!` | `!` | amber | interrupted, waiting |
  | `?` | `?` | neutral | unknown, unavailable |
  | `·` | `.` | neutral | anything else |

  Only completed states read as success.
- **Menus:**
  - The title is an uppercase static prefix; user text keeps its case. Use
    `showMenuFor(prefix, user, items)`.
  - A rule sits under the title, and a muted hint row with a rule above it closes
    the menu.
  - Pair rows are **explicit only**, via `pairMenuItem` or
    `menuItem{PairLabel, PairValue}`. Never infer pairs from free-form text such as
    thread titles, options or folder names.
  - Destructive items use red ink; Cancel and no-op items never do.

The shared API is in `apps/go/internal/tui/panel.go`:
- `panelSectionHeading`/`…On`, `panelRule`/`…On`, `panelPairRow`/`…Styled`,
  `panelPairFits`, `panelPairShort`;
- `panelSegmentLayout`, `panelSegmentsFit`, `panelBandsSupported`,
  `paintPanelBandEdge`, `registerPanelBandHit`;
- `toggleTrack`, `knobFill`, `panelStatusMark`.

Surfaces build typed blocks in `surface_panel.go` (`surfaceBlocks`/`surfaceRows`).

## Done

- **Settings** (app and project): headings and rules; segmented Workspace default,
  Theme and Symbols; the restart toggle; pairs on About and Keybindings; banded
  buttons.
- **Right-host surfaces:** Plan, Agents, Activity, Usage, Files, Git and Terminal
  headers. Terminal output stays raw, sanitized text.
- **Empty host chooser:** 2 or 3 columns of tiles, with list and button fallbacks.
- **Centered menus and dialogs**, including context menus, which use `renderMenu`.
- **Navigation:** the `CLOSED` heading, still dimmed, with the count only while
  collapsed.

## Deliberately unchanged (accepted one-row density rules)

The following are not restyled:
- composer and footer;
- question card and tabs;
- queue card;
- thread cards;
- surface tabs;
- top chrome;
- activity strip.

Banding them would take rows from the conversation. Changing them needs an
explicit user decision, which should be recorded in `components.md`.

## Candidates for the next pass (not yet reviewed)

Capture each area first and decide whether the style actually improves it:

1. **Settings category sidebar** (`settings_layout.go` `renderSidebarSettings`). Its
   selection fill and underline predate the new controls and look inconsistent
   beside them.
2. **Settings › Agents page:** banded buttons for every default field. It may
   scroll at 30 rows, so check its density.
3. **Bottom terminal panel** (`render.go` `renderBottom`): session header metadata.
4. **Attachment viewer** (read-only preview dialog, `clipboard.go`): header, and
   pair rows for source, size and type.
5. **Add project folder dialog and inline `@` mentions** (`path_completion.go`
   `renderMentions`, and `renderMenu`'s `projectMode` rows).
6. **Closed-thread banner and checkout context line** (`checkout_context.go`).
7. **Compact column picker** (`compact_columns.go`) and the empty thread list
   (`navigation.go` `renderEmptyThreads`).
8. **Activity detail pairs:** they are still inferred from server or tool text
   shaped like `Key: value` (the `detailPair` regex). Prefer explicit structure from
   the protocol when it exists.

## Open decisions (flag to the user; do not settle silently)

- Thresholds chosen by the implementation, not the user:
  - the pair short-value limit (32 cells and 40% of the width);
  - three chooser tile columns from a host width of 54.
- Whether the one-row-density areas above should adopt any of the panel style.
- Kitty-only enhancements (OSC 66 sizing, kitty graphics) need an ADR, per the
  text-sizing note's tier (b).

## Verification workflow that worked

1. **Implement** through a subagent that owns specific files. Paint with
   `panel.go`, and keep rendering pure, ANSI bounded and widths cell-aware.
2. **Automated checks:** in `apps/go`, run `make check` and `make pty`. Update PTY
   expectations only where text changed, and keep them equally strict.
3. **Deterministic captures:**
   ```sh
   TUI_GO_CAPTURE_DIR=<dir> go test ./internal/tui -run '<Capture tests>' -count=1
   python3 scripts/render-capture.py <dir> <out> --font /usr/share/fonts/TTF/JetBrainsMonoNerdFont-Regular.ttf
   ```
   `render-capture.py` needs Pillow; use a venv. Existing capture tests:
   `TestSettingsPanelCaptures`, `TestPanelStyleCaptures`, `TestPolishedViewCaptures`,
   `TestActivityReviewCaptures`, `TestContextMenuVisualCaptures` and
   `TestAgentMenuViewCapture`. Look at the PNGs and iterate; that visual judgement
   is the point of the work.
4. **Real terminal:** use `apps/go/scripts/foot_capture.py` (see its docstring).
   Build to a scratch path and use an isolated `TUI_GO_HOME`, then stop the server
   afterwards. Foot found real defects that the PNGs hid.
5. **Independent review** by the `verify` subagent. It found free-form text being
   split into pairs, uppercased user names, narrow-width truncation and focus loss
   across layout fallbacks. Then do a fix pass.

## Gotchas

- **Never send keystrokes to whichever window has focus.** The user may have their
  own `tui-go` running in foot, and Hyprland focus follows the mouse.
  `foot_capture.py` writes to its own child PTY for this reason.
- **Early foot captures showed a doubled scrollbar.** It came from a wrapper that
  ignored window resizes, not from the app. `foot_capture.py` forwards SIGWINCH.
- **`render-capture.py` drawing limits:** it draws block elements and `─`/`│` as
  cell geometry, but corner glyphs from the font. Re-rendered outlines can show
  joins that differ from real terminals.
- **Staticcheck (`make lint`) fails on unused helpers.** When splitting commits,
  keep each commit's `panel.go` free of functions only later work uses.
- **Commits:** imperative subject, short body, no co-author trailers or footers.
  Commit locally only; never push or pull. Commit only when the user asks.

## Not verified

- Light theme and 16-color output in a real terminal (unit tests cover the
  fallbacks).
- kitty, Ghostty, iTerm2, SSH and tmux.
- Hover and selection transitions on pair rows and tiles in the ANSI-growth test,
  which repaints a static state only.
