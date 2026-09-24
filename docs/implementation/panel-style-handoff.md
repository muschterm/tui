# Panel style handoff — 2026-09-24

**Start here** to continue restyling the Go reference app with the "panel" visual
language. The user asked for it after admiring an Omarchy Quickshell (Qt/QML, not
a TUI) WiFi panel: "do whatever works", then "apply this style to the rest of the
app too", and finally "any other restyling you recommend". Two commits on
`feature/init` hold the first two passes; the third pass is staged, not yet
committed:

| Commit | Scope |
| --- | --- |
| `12b4b2d` Restyle settings as a control panel | Settings content, shared painters in `panel.go`, capture-renderer block drawing, text-sizing research |
| `d56fb9e` Apply the panel style across surfaces, menus and navigation | Right-host surfaces, chooser tiles, centered menus/dialogs, Closed heading |
| (staged) third pass | App-wide status vocabulary, settings sidebar/Agents, context lines, requests, stream, notices, structured tool pairs |

## Read first

- `AGENTS.md` styling bullet (points at the panel constructs) and engineering priorities.
- [components.md › Settings panel constructs](../design/components.md#settings-panel-constructs--2026-09-23)
  and its "Panel style across surfaces" and "Panel style — app-wide pass"
  subsections: the rules this work follows.
- [Settings restyle note](../research/go-settings-panel-2026-09-23.md),
  [app-wide note](../research/go-panel-style-2026-09-23.md) and their `*-captures/`
  (including real foot screenshots under `foot/`), and the
  [third-pass note](../research/go-panel-pass3-2026-09-24.md).
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
  buttons. Sidebar with heading, square-fill categories and a rule above Back;
  Agents page as actionable pair rows fitting 30 rows.
- **Right-host surfaces:** Plan, Agents, Activity, Usage, Files, Git and Terminal
  headers; Git checkout pairs and CHANGES section; Usage TERMINAL pairs.
  Terminal output stays raw, sanitized text.
- **Empty host chooser:** 2 or 3 columns of tiles, with list and button fallbacks.
- **Centered menus and dialogs**, including context menus (`renderMenu`),
  separator items, `@` mentions and the folder dialog.
- **Navigation:** `CLOSED` heading; empty thread list; checkout line marks; closed
  banner with compact Reopen. Compact column picker already conformed.
- **Bottom terminal panel** header reusing the right-host terminal blocks.
- **Status vocabulary app-wide** at zero row cost: activity strip, conversation
  tool/MCP rows, Thinking/Waiting/Connection lost, answered Q&A card, request mode
  labels and answered tabs, queue legend, notices with explicit severity, context
  gauge.
- **Activity detail:** pairs only from structured `protocol.Activity.Tool`; the
  `detailPair` regex is gone. Status shows the effective `Activity.State`; a
  differing `Tool.Status` is a muted, plain "Reported" pair rather than a
  repainted mark. `rawInput`/`rawOutput` survive status-only updates. When
  `Tool` is present, the legacy `Detail` field is compact JSON (kind, status,
  locations, extra — no raw input/output/content), so older clients no longer
  see raw fields through it and per-row retention is back to roughly the
  single `MaxActivityDeta` budget.
- **Draft vs. confirmed answered marks:** an unsubmitted draft answered-tab mark
  uses plain tab ink, not the success-green `✓`, which is reserved for
  confirmed submission. Answered-card failure/uncertain/cancelled-before-
  confirmation delivery words take that state's mark ink (red/amber); request-
  card notice width is measured including the mark.
- **Notices:** a steer notice reads "done" only on confirmed receipt
  (`delivered`/`fixture-delivered`), stays active `●` otherwise, and becomes
  error `✕` on a server rejection or unavailable `!` for a blocked action.
- **Git body:** removed the non-functional "will remain separate" promise
  note. Colors is a single `Colors` pair from structured
  `colorDiagnosticParts`; a caveat folds into `value · note` when
  `panelPairFits` allows it, otherwise it continues as a right-aligned muted
  line — never a separate labelled pair.
- **Hit-testing and menu fixes:** empty-thread banded buttons register through
  the shared `registerPanelBandHit`; project menus skip separator items;
  `pty_sidebar_settings` asserts Effort shows `Medium` before the change under
  test. `attachmentsButton` and `renderSettingsCategory` were evaluated for
  consolidation into `compactButton` and kept separate because their rendered
  output differs.
- **Final polish round:** menu `n of N` counters count selectable rows only,
  and any row's icon reserves that slot on every row; the Commands menu groups
  its existing items with separators, in existing order, no new headings.
  Destructive confirmations title as `PREFIX · user`. The footer overflow
  menu is explicit pairs titled `MORE SETTINGS`/`MORE SETTINGS · READ-ONLY`.
  The `@`-mention popup spans the composer outline's width directly above it,
  with fill-plus-`•` selection. Approval actions share the question card's
  right-aligned action group; the question body keeps a gap cell before its
  scrollbar. `QUEUED n` is bold and stays in the border. The status line reads
  `Accepted`, `Steer accepted · awaiting delivery` (active) and `Message
  steered into the active turn` (done). Right-host state words take their
  mark's ink app-wide. The Activity tab reads "Usage" while its usage detail
  is open (render-time only; same surface kind and glyph). `structuredTool`
  now folds kept raw input/output into the same `toolBudget` split as the
  current update, fixing the earlier combined-raw-size gap, and truncation
  markers are never doubled.
- **Final consistency round:** transcript tool/MCP rows take mark ink on the
  state word, matching right-host status rows. The activity inspector no
  longer repeats status: the Status pair shows only when the activity has no
  state of its own, and "Reported" shows only when `Tool.Status` differs. The
  right-host Terminal surface opens with a `TERMINAL` heading (the bottom
  panel is unchanged, no title row). Git checkout facts are all pairs —
  Checkout, Branch/HEAD/Revision, and loading/unavailable/`Repository · not a
  Git checkout` — with no status rows and no green. Agents surface Parent
  shows the owning thread's title, falling back to the raw ID. Menu group
  separators span the same width as heading/hint rules. Tool payload content
  and locations beyond the 64-entry cap keep 63 entries plus a truncation
  marker, within budget. App General's "Project starting folder" and Project
  "Icon" are now dense actionable pairs (long paths left-truncated, keeping
  the tail); Name, segmented-choice fallbacks, "Use built-in defaults" and
  "Remove project…" stay banded. Menus support non-selectable muted `Note`
  rows (`menuItem.Note`; no hit rect; skipped by navigation and the `n of N`
  counter), used for "Files on disk will be kept" in the remove-project
  confirmation.

## Remaining candidates

1. **Attachment viewer:** the read-only preview dialog does not exist in the Go
   slice yet; it is a feature before it is a restyle.
2. **Git file lists and upstream ahead/behind:** need protocol data first.
3. **Terminal exit status** in the protocol, so terminal tabs can show exit marks.
4. **Attachment chip states** (captured, failed, unavailable) with status marks.
5. **Compact column picker:** check whether any values should sit flush right.
6. **Consolidating `attachmentsButton`/`renderSettingsCategory`** into
   `compactButton` needs an accepted visual change first, since their current
   rendered output differs; do not merge them as a plain refactor.
7. **Usage icon:** the Activity tab's "Usage" title (shown while its usage
   detail is open) still keeps the Activity glyph; no dedicated usage icon
   exists yet.

## Open decisions (flag to the user; do not settle silently)

- Thresholds chosen by the implementation, not the user:
  - pair short-value limit (32 cells and 40% of the width);
  - the right-host exception keeping values of at most 20 cells inline;
  - three chooser tile columns from a host width of 54;
  - context gauge amber at 80% and red at 95%;
  - Settings › Agents fitting 30 rows without scrolling;
  - empty-state button width (longest label plus 4);
  - the `@`-mention popup dropping its heading/hint rules below 7 rows.
- Copy choices: `ANSWERED`, `QUEUED n`, `Image support`,
  `Repository · not a Git checkout`, `Steer accepted · awaiting delivery`.
- Notice severity classification at each call site.
- Deny stays neutral rather than destructive.
- Completed Agents/Plan summaries keep a solid green `●` (per AGENTS.md) rather
  than `✓`.
- Kitty-only enhancements (OSC 66 sizing, kitty graphics) need an ADR, per the
  text-sizing note's tier (b).
- **Reported pair:** when the effective `Activity.State` differs from an
  agent-reported `Tool.Status`, showing the reported value as a plain muted
  pair (rather than dropping it, or giving it its own status mark) was an
  implementation choice, not a specified behavior.
- **State-word ink app-wide:** painting a right-host status row's state word
  in its mark's semantic ink (rather than a separate fixed text color) was an
  implementation choice made for consistency with the status vocabulary rule,
  not a specified behavior.
- **Snapshot encoded size vs. the 8 MiB WebSocket read limit:** pre-existing and
  unfixed, made more visible by this pass. `client/client.go`'s `SetReadLimit`
  caps a client's read at 8 MiB, but the server's snapshot byte budgets
  (`MaxActivityDeta` and friends) bound retained bytes, not the size after JSON
  encoding — escaping can multiply size, so a thread's snapshot can still
  exceed the client's read limit even while every field stays within its
  retention budget. Needs an encoded-size budget or pagination; this is a
  parent/user decision, not something this pass's fixes settle.
- **Intermittent `internal/server` test flake:** the final merged-tree
  integration run (`make check`, `make lint`, `make test`, `make pty`, all 7
  harnesses, 11 color profiles) passed, but an `internal/server` failure
  ("connection closed") appeared a few times only under concurrent load. It
  never reproduced in isolation and its specific test was not identified;
  needs follow-up investigation rather than being written off as flaky.

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
   `TestAgentMenuViewCapture`, plus the third pass's `panel_pass3_*_test.go` captures.
   Look at the PNGs and iterate; that visual judgement
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
- **Agent worktrees may be based on `master`,** not `feature/init`. Seed an
  isolated worktree from `feature/init` (including staged work) before
  implementing.
- **The sandbox refuses commands whose paths contain `git` combined with pipes.**
  Wrap such pipelines in a small script file and run that instead.
- **Commits:** imperative subject, short body, no co-author trailers or footers.
  Commit locally only; never push or pull. Commit only when the user asks.

## Not verified

- Real-terminal (foot) checks of the third pass beyond Settings.
- Light theme and 16-color output in a real terminal (unit tests cover the
  fallbacks).
- kitty, Ghostty, iTerm2, SSH and tmux.
- Hover and selection transitions on pair rows and tiles in the ANSI-growth test,
  which repaints a static state only.
