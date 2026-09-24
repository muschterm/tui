# Go panel style beyond Settings — 2026-09-23

User request (2026-09-23): "apply this style to the rest of the app too". This note
records how the Settings panel constructs (see "Settings panel constructs" in
[components](../design/components.md)) were extended to other Go TUI areas, the
checks that were run and the remaining limitations. It does not change the design
specification.

## What changed

- **Right-host surface content** (`internal/tui/surface_panel.go`). Surfaces are
  now built from width-independent blocks and wrapped into painted rows:
  uppercase muted section headings, rules between entries, label/value pairs for
  short facts (State, Delivery, Parent, Checkout, Session, Controller, Server,
  Tool, usage items and so on) and a muted label above wrapped values that do not
  fit a pair (Inputs, Result, attachment captures). Retained activity details
  become pairs for their `Key: value` lines. Plan shows one status row per step
  with the state as a muted value at the right. Status glyphs use semantic colors
  through `panelStatusMark`: completed green `✓`, active blue `●`, pending muted
  `○`, failed red `✕`, interrupted/waiting/stale gold `!`, and anything else a
  neutral `?`. ASCII icons use `+ * o x ! ?`. Terminal output stays raw,
  sanitized and wrapped under a status row and pairs. `surfaceText` now flattens
  the same blocks, so DetailID filtering and the plain-text contract are shared.
  Scrolling (`detailMax`, the `right-body` hit, wheel/arrow/F6) is unchanged,
  except that it now counts painted rows. One blank cell keeps right-aligned
  values off the scrollbar.
- **Empty right-host chooser.** Six banded square-fill tiles (icon and name), two
  columns, or three when the host is at least 54 cells wide. They use
  `panelSegmentLayout` and `paintPanelBandEdge`. Hit keys are still
  `chooser:<kind>` with `open` actions, each tile has one three-row hit, and the
  focus mark goes in the gap cell before the label row. The chooser falls back to
  the previous one-row list, now under an uppercase heading, when bands are
  unsupported (under 256 colors or ASCII icons), a label does not fit or the host
  is too short. The "Choose a surface…" button for short hosts is unchanged.
- **Centered menus and dialogs** (`renderMenu`, `menuRect`). The title is an
  uppercase, bold, muted heading with a rule beneath it. "Cannot send message"
  keeps red ink. A rule sits above the muted hint row. `menuChromeRows` (6),
  `menuExtraRows` and `menuVisibleItems` keep painting, `menuRect`, outside-click
  dismissal and resize offsets consistent. Pair rows are explicit opt-in:
  only items built with `pairMenuItem` (Usage summary, fixture Permissions,
  agent-defaults fields/Capabilities, More settings fields) become selectable
  pair rows, and only when the value is short (below). Free-form text such as
  thread titles, options and folder names is never split on `: `. Titles
  uppercase only their static prefix: `showMenuFor` keeps the user part
  (thread title, project or agent name) in its original case.
  Destructive delete/remove actions use red ink.
- **Navigation Closed heading.** `CLOSED (N)` while collapsed, `CLOSED` while
  expanded and `SHOW CLOSED` while hidden, in bold. It keeps the rule, the
  toggle hit and its pinned position.
- **Panel additions** (`panel.go`). Background-parameterized `panelSectionHeadingOn`,
  `panelRuleOn` and `panelPairRowStyled`, plus `panelPairFits` and
  `panelStatusMark`; existing signatures are unchanged. `panelPairRowStyled`
  (and therefore `panelPairRow`) now gives the value `max(width/2,
  width-label-2)` cells rather than exactly half the row, so it truncates only
  when the pair does not fit.

- **Review fixes (same day).** Pair values right-align only when short (≤ 40%
  of the width and ≤ 32 cells, `panelPairShort`); longer values stack under a
  muted label. A status row whose state cannot sit beside the title paints the
  title at its full wrap width and moves the state to a muted row below.
  Activity details keep blank lines (runs collapse to one). An untitled
  activity is named by its role (Prompt, Reply, Tool call…), never a bare glyph.

## Checks

- `make check` in `apps/go` (fmt-check, vet, staticcheck, `go test -race ./...`,
  build): passed.
- `make pty`: all seven harnesses passed (pty_smoke, pty_navigation,
  pty_small_screen, pty_sidebar_settings, pty_steering, pty_path_completion,
  pty_colors). `scripts/pty_clipboard.py` was also run on its own and passed.
  Expectations were updated only where they matched the old title/label text:
  uppercase menu titles, `CLOSED (`, `ADD SURFACE` and pair rows matched by
  regular expressions such as `Effort\s{2,}Medium`. The negative checks stay
  equally strict. `pty_navigation`'s `thread_row` also failed before this
  change, because thread titles are now derived from the first prompt; it now
  also accepts the derived "Navigation start" title.
- New unit tests (`internal/tui/panel_style_test.go`) cover chooser tile hits,
  grid geometry, Tab focus and its mark, click activation, fallback to the list
  (ASCII, 16 colors, labels that do not fit, short hosts), menu geometry with
  the heading and both rules, item hits and scrolling, the counter, usage pair
  rows that stay selectable, the no-truncation fallback, structured surface
  rows, status inks that never imply success, `detailMax` bounds, the Closed
  count shown only while collapsed and no ANSI growth across repeated renders.
- Visual review: every capture from TestPolishedViewCaptures,
  TestActivityReviewCaptures, TestContextMenuVisualCaptures,
  TestAgentMenuViewCapture and the new TestPanelStyleCaptures was rendered with
  `scripts/render-capture.py` and JetBrainsMono Nerd Font and inspected. A
  curated subset is in [go-panel-style-captures](go-panel-style-captures/), in
  both themes: tabs with Plan, the Activity inspector, the empty chooser, the
  commands menu, the usage menu and 48×22 narrow. These are rendered captures,
  not terminal screenshots. They were re-rendered after the review fixes.
- Real foot 1.28 review (Hyprland, JetBrainsMono Nerd Font): Activity and Usage
  surfaces, downscaled in [foot](go-panel-style-captures/foot/). The reported
  two-column scrollbar was a harness defect, not the app: the injector copied
  the window size once at start, before the tiler resized the foot window, and
  never forwarded SIGWINCH, so the app painted rows wider than the terminal
  and the extra cell wrapped, shifting following rows (right outlines were also
  missing in that capture). With the child PTY resized and SIGWINCH sent before
  each step, the scrollbar is a single column with a blank cell before it.

## Limitations

- Only Activity and Usage were reviewed in foot; kitty and other screens are
  unreviewed. Banding follows the same capability gate as Settings.
- `panelStatusMark` recognizes a fixed vocabulary of state words. Unknown
  provider states render as neutral `?`, never as success.
- The Closed heading keeps the dimmed ink of closed rows, which AGENTS.md
  requires ("the Closed heading is dimmed too"), rather than the panel's muted
  ink.
- Retained activity details still infer pairs from `Key: value` lines
  (`detailPair`, capitalised key ≤ 24 cells); this is server/tool text, not
  menu input, but a detail line of that shape will render as a pair.
- The bottom terminal panel and the 48×22 narrow Conversation column are
  unchanged. They are listed only as regression captures.
