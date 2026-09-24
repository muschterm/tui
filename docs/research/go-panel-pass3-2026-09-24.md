# Go panel style, third pass — 2026-09-24

The user authorized "any other restyling you recommend" after the
[app-wide pass](go-panel-style-2026-09-23.md). This note records what the third
pass changed in the Go TUI, the captures used to judge it and the checks run.
The accepted rules are in
[components › Panel style — app-wide pass](../design/components.md#panel-style--app-wide-pass--2026-09-24).
Captures are under [`go-panel-pass3-captures/`](go-panel-pass3-captures/), with
`before/` and `after/` pairs per area.

## Judgements per area

- **Settings** (`settings/`, real foot screenshots in `settings/foot/`). The
  sidebar now has a SETTINGS heading and rule, single-row square-fill categories
  with a reserved focus cell, and a rule above Back; brackets mark the selected
  category only in low-color/ASCII output. Settings › Agents uses actionable pair
  rows (muted label, accent value flush right, whole-row hover/focus, fixed values
  muted), an AGENT SETTINGS section and a banded reset button. The page fits 30
  rows without scrolling; that threshold is an implementation choice.
- **Dialogs** (`dialogs/`). The bottom terminal panel reuses the right-host
  terminal blocks (status row, Session/Controller pairs, rule) without adding
  rows. The `@` mention popup has a PROJECT FILES heading, rules and a hint; the
  rules drop below 7 rows. Add-project folder info rows are muted. The Go slice
  has no attachment viewer, so none was restyled.
- **Navigation and context** (`nav/`, dark and light at 120×32 and 48×24). The
  checkout line leads with a mark outside its hit rectangle: Git icon for branch,
  unborn and detached; `○` loading; `·` non-Git; `?` unavailable or fixture;
  never green. The closed banner keeps its state in text ink with a muted hint
  and a compact Reopen button (`[ ]` fallback). The empty thread list shows NO
  THREAD SELECTED, a rule and banded buttons sized to the longest label plus 4.
  The compact column picker already used `renderMenu` and is unchanged.
- **Activity detail** (`activity/`, including `after-structured/`). The
  `detailPair` regex is removed; free text never becomes pairs. New optional
  `protocol.Activity.Tool` (`ToolDetail`: kind, status, locations, content, raw
  input/output) is filled by `agent/normalize.go`, with the raw fields sharing
  one `MaxActivityDeta` budget; `rawInput`/`rawOutput` survive status-only
  updates. The inspector's Status pair shows the effective `Activity.State`; a
  differing `Tool.Status` appears as a muted, plain "Reported" pair instead of
  repainting the status mark. When `Tool` is present, the legacy `Detail` field
  is now a compact JSON summary (kind, status, locations, extra — no raw
  input/output/content), so older clients no longer see raw payloads through
  it; per-row retention is back to roughly the single `MaxActivityDeta` budget
  rather than double. A kept raw value restored from an earlier update (e.g. a
  retained `rawInput` alongside a new, separately-reported `rawOutput`) now
  shares the same `toolBudget` split as the current update's fields, so the
  combined raw size can no longer exceed the shared budget; `bound`/
  `boundEntries` strip a prior truncation marker before re-truncating, so a
  field never gains a doubled marker. The inspector shows Kind/Status/Location
  pairs only from `Tool`.
- **Summaries** (`summaries/`). The activity strip uses `panelStatusMark` for
  non-success states; circles remain for active and all-completed. The usage
  popup has explicit pairs and a separator above Usage details. The context gauge
  shows `Ctx ?` when unknown and turns amber at 80% and red at 95% (implementation
  thresholds) from supplied telemetry only. Terminal tab exit marks were skipped:
  the protocol carries no exit state.
- **Requests** (`requests/*.ans`, dark/light at 60 and 160 columns). Static mode
  labels are uppercase (ANSWER ANYTIME, WAITING FOR ANSWER, APPROVAL REQUIRED,
  AWAITING RESUME), with blocking still gold. A confirmed-submitted answered tab
  gets a green `✓` in the reserved cell; an unsubmitted draft answered mark uses
  plain tab ink instead, so a filled-but-not-submitted question never reads as
  successfully answered. Card notices lead with `!` or `●`, never `✓`. The queue
  legend is a muted QUEUED n. Deny stays neutral.
- **Conversation stream** (`stream/`). Tool/MCP rows lead with a status mark and
  kind icon and a muted state word. Thinking, Waiting and Connection lost have
  marks. The answered Q&A card is titled `✓ ANSWERED` with per-delivery marks
  (no `✓` unless confirmed); a failed, uncertain or cancelled-before-confirmation
  delivery word instead takes that state's mark ink (red for failed, amber for
  uncertain/cancelled), and falls back to a colored title without the mark if
  the mark would add a row. Notices carry explicit caller-set severity painted
  apart from their text; a steer notice reads "done" only on confirmed receipt
  (`delivered`/`fixture-delivered`), staying an active `●` otherwise, switching
  to error `✕` on a server rejection and unavailable `!` for a blocked action.
  Request-card notice width is measured including the mark glyph. Menus support
  non-selectable separator items.
- **Polish** (`polish/`). The composer attachment button has a `[ ]` fallback.
  `attachmentsButton` and `renderSettingsCategory` were considered for
  consolidation with the shared `compactButton` helper but kept separate: their
  rendered output differs, so merging them would be a visual change, not a
  refactor.
- **Surface bodies** (`bodies/`). Git shows Branch/HEAD/Revision pairs, a CHANGES
  section marked unavailable and the path stacked verbatim; the non-functional
  "will remain separate" promise note is removed since nothing in the slice
  enforces or shows that separation. Usage TERMINAL short values are
  flush-right pairs; in the right host a value of at most 20 cells stays
  inline even past the 40% rule. Colors renders a single `Colors` pair from the
  structured `colorDiagnosticParts` profile; a caveat, when the profile carries
  one, folds into `value · note` on the same row when `panelPairFits` allows
  the combined text, otherwise it continues as a right-aligned muted line
  below (no separate "Color source" label either way). Files, Plan and Agents
  needed no change. Git file lists and upstream ahead/behind need protocol
  data first.
- **Consolidation and hit-testing fixes.** Empty-thread banded buttons now
  register their hit rectangles through the shared `registerPanelBandHit`
  instead of a bespoke path. Project menus skip separator items when
  navigating and hit-testing. `pty_sidebar_settings` now asserts Effort shows
  `Medium` before the settings change under test, so the assertion actually
  exercises the intended before/after states.
- **Final polish round.** A menu's `n of N` position counter now counts
  selectable rows only, skipping separators; when any row carries an icon,
  every row reserves that icon's slot so labels align. The Commands menu
  groups its existing items with separator rows, in their existing order, with
  no new category headings. Destructive confirmation dialogs use the
  `PREFIX · user` title form (`DELETE THREAD · <title>`, `REMOVE PROJECT ·
  <name>`). The footer's hidden-settings overflow menu is explicit label/value
  pairs titled `MORE SETTINGS`, or `MORE SETTINGS · READ-ONLY` while settings
  are locked during active work. The `@`-mention popup now spans the
  composer outline's width and sits directly above it; a selected entry uses
  the shared square-fill treatment plus the reserved focus-mark gutter cell
  (`•`). Approval card actions share the same right-aligned action group as
  question Submit/queue actions; the question body keeps one gap cell before
  its scrollbar. The `QUEUED n` legend is bold, muted text and stays in the
  card's border row, since moving it inside would add a row. The status
  line's empty receipt state now reads `Accepted`; an unconfirmed steer notice
  reads `Steer accepted · awaiting delivery` (active `●`), and a confirmed one
  reads `Message steered into the active turn` (done `✓`). Right-host status
  rows now paint the state word in its mark's semantic ink app-wide, instead
  of a separate fixed color. The Activity surface tab's label reads "Usage"
  while its usage detail is open — a render-time title only, still the
  Activity surface kind, still the Activity glyph (no usage icon exists yet).
- **Final consistency round.** Transcript tool/MCP rows now paint the state
  word in its mark's ink, matching right-host status rows (the ` · `
  separator before it stays muted). The activity inspector no longer repeats
  status: the Status pair shows only when the activity has no state of its
  own, and the muted "Reported" pair shows only when `Tool.Status` differs
  from the effective state. The right-host Terminal surface now opens with a
  `TERMINAL` heading (the bottom terminal panel is unchanged, with no title
  row). Git checkout facts are all explicit pairs — Checkout as a long pair,
  Branch/HEAD/Revision pairs, and loading/unavailable/`Repository · not a Git
  checkout` as muted pairs — with no status rows and no green ink. The Agents
  surface's Parent field now shows the owning thread's title when known,
  falling back to the raw parent ID. Menu group separators span the same
  width as the heading and hint rules. Tool payload content/location entries
  beyond the 64-entry cap now keep 63 and end with a truncation-marker entry,
  within the shared budget, instead of silently dropping the overflow.
  Settings' App General "Project starting folder" and Project "Icon" are now
  dense actionable pair rows, with a long path left-truncated to keep its
  identifying tail; Name, segmented-choice fallbacks, "Use built-in defaults"
  and "Remove project…" stay banded. Menus gained a non-selectable, muted
  `Note` row (`menuItem.Note`, no hit rect, skipped by navigation and the
  `n of N` counter), used for "Files on disk will be kept" in the
  remove-project confirmation.

## Checks

- Each implementing agent ran `make check`, `make lint`, `make test` and
  `make pty` in `apps/go`; all passed.
- Integration run (2026-09-24, final merged tree, isolated): `make check`,
  `make lint`, `make test` and `make pty` (all 7 harnesses, 11 color
  profiles) pass. An intermittent `internal/server` failure appeared a few
  times only under concurrent load ("connection closed"); it never
  reproduced in isolation and its test was not identified.

## Not verified

- Real-terminal (foot) checks for anything other than Settings.
- Light theme and 16-color output in a real terminal.
- kitty, Ghostty, tmux and SSH.
