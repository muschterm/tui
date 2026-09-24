# Component construction and interaction states

Status: **accepted, 2026-09-20**. The user approved these placements and state
mappings for implementation. [ADR 0008](../adr/0008-cell-native-component-state.md)
records the rendering boundary; shades and dimensions belong in this rule.
Acceptance of the rule does not establish implementation or terminal validation.

## Choose shape and treatment for the available space

| Variant | Treatment | Use |
| --- | --- | --- |
| Rounded outline | Small rounded line corners; stable background | Prompt, thread cards, queue group, pending/answered question cards, approval card, dialogs; roomy buttons where justified |
| Square outline | Straight line corners; stable background | An isolated control/container needing a visible boundary, or a fallback when fill is unavailable |
| Square fill | Rectangular background, no line border | Question tabs, surface tabs, compact Back/Next/Submit/Options buttons and inline request selectors |

Rounded borderless fill is outside the baseline: neither pills nor stepped block
corners reproduce the requested small smooth curve. Square fills work naturally
with terminal cells. Reserve rounded outlines for grouping and square fills for
density; do not put an outline around every element simply for consistency.
Existing unboxed toolbar icons, composer settings and transcript activity can
remain unboxed when their grouping and hit targets are clear.

[Answered Q&A cards](questions.md#answered-questions-in-conversation-history) group
the original questions and accepted answers in one rounded container. Keep the
read-only content compact; use inline Expand/Collapse only for overflow, without
nested per-question boxes or submission controls.

Keep the queue count, visible message previews and their Steer/Edit/Remove/reorder
controls inside one rounded container. Align each preview with its actions and
inset them from the outline. Bound the visible rows; the header exposes hidden
items without mixing the queue into activity summaries or the pending request.

Use one row for question tabs and their compact actions. This gives content more
room than the earlier three-row controls inside the bounded question card. Surface
tabs likewise benefit from a one-row strip. Their enclosing region
already provides structure. Square outlined controls still consume border space;
choose square fill, rather than changing only the corner glyph, to save rows.

Treat shape and interaction state as separate inputs. Do not switch from square
to rounded, add a border, or change padding on hover/selection. Responsive variants
may change at a deliberate layout breakpoint; preserve focus, selection and drafts.
An optional quiet interior fill in outlined containers remains constant and inset
from corner cells. Outlined containers that hold controls (the pending request
card, the queue group) take the focused outline while keyboard focus is inside
them and never change it on hover; their inner controls carry their own state. Background colors themselves work; smooth rounded clipping is
the missing feature of this text renderer.

## State feedback

| State | Outlined variants | Square-filled variant | Additional cue |
| --- | --- | --- | --- |
| Rest | Quiet visible neutral border | Quiet neutral fill | Normal label |
| Hover, unselected | Stronger neutral border | Stronger neutral fill | Existing hover action may appear |
| Selected | Accent border | Accent-tinted fill with readable text | Bold label |
| Keyboard focus, unselected | Stronger neutral border | Stronger neutral fill | Focus mark in the leading cell |
| Selected and hovered | Keep selected treatment | Keep selected treatment | Keep bold label |
| Selected and keyboard-focused | Keep selected treatment | Keep selected treatment | Bold label plus the focus mark |

Glyph-only icon controls (pane toggles, the attention bell, sidebar header
actions, the settings gear, tab add/overflow, dialog close, composer paperclip,
Stop, Send and ellipses, and thread row status/quick/reopen/menu glyphs) are a
trial exception accepted on 2026-09-21: they have no fill in any state. Hover and
keyboard focus embolden the glyph and lift its ink toward text: muted becomes
text, accents move halfway toward text (brighter in dark themes, deeper in
light) in true color and become text in low-color profiles. Focus paints the
mark in the padding cell before the glyph; selection uses accent ink plus bold.
The glyph is centered in a reserved slot that keeps chrome spacing stable, but
only the glyph's cell and the one trailing cell it can draw into hover or
activate; leading padding is inert or, inside a tab, selects the tab. A tab's
glyph sits at the start of its three-cell slot, followed by its spill cell and
one gap cell, so tabs keep one end-cap cell of inset on each side (2026-09-22).

**Focus mark (2026-09-22).** Keyboard focus does not underline. It paints an
accent `•` (`>` in the plain-symbol fallback) in the one cell before the focused
control that the control does not use for content: a filled control's or tab's
leading end cap, an icon control's padding cell inside its reserved slot, or the
blank gutter a container leaves before a text row. No cell of the control itself
changes, so focusing never moves an icon or label. Only a control with no such
cell (a one-cell icon slot, or a text row flush against neighbouring content)
keeps the underline as its fallback cue.
Nerd Font symbol outlines are identical in Bold faces, so the ink lift, not the
weight, is the reliable cue there. Filled text controls and tabs keep the table
above. Pointer-shape changes (xterm/kitty `OSC 22`) are not used; they remain an
unnegotiated terminal capability.

Selection is persistent state, such as the active tab or thread. Hover is temporary
pointer presence; focus identifies the keyboard target. An action button does not
become selected merely because it was pressed. Selection takes precedence over
hover, and focus adds its own cue. Hovering B while A is selected must leave A
unmistakably selected. Primary-action emphasis is a role, not a false selected state.

Use semantic tokens for resting, interactive and selected border/fill, plus readable
foregrounds. Calibrate both themes and limited-color palettes; quiet does not mean
invisible. Avoid using ANSI dim as a substitute for a tested neutral color. Bold
selection and the leading focus mark remain useful when color distinctions collapse.
When background color is unavailable, compact square controls may use reserved
`[ Label ]` end cells. Keep dimensions and independent icon slots unchanged.
An explicit plain-symbol mode may use ASCII outlines. Never change width to add a
state indicator or steal space from a neighboring control.

Keep execution status separate: working blue, attention amber, completion green
and failure red remain on status indicators and meaningful messages. Interaction
styling must not overwrite them. Validation stays visible at its source. Disabled
actions must not gain an enabled hover treatment.

## Settings panel constructs — 2026-09-23

User-directed refinement ("do whatever works"), after reviewing an Omarchy
Quickshell panel. The Qt panel's mixed font sizes are not available: foot lacks
OSC 66 scaling and DECDHL, and the pinned Bubble Tea v2 renderer strips both (see
[text sizing research](../research/terminal-text-sizing-2026-09-23.md)). These
cell-native constructs approximate its polish instead.

- **Banded square fill.** A square fill may add a `▄` row above and a `▀` row below
  its label row, painted in the fill color over the canvas, so it reads as a
  two-cell-tall button. The band is part of the hit rectangle and changes color
  with state; its geometry never changes. Use it only with 256 or more colors and
  Nerd Font symbols, where neutral fills differ from the canvas. Otherwise use the
  single-row `[ Label ]` fallback; never paint solid blocks.
- **Segmented choice.** A small closed option set becomes equal-width square-fill
  segments across the form width with a two-cell gap. Each segment is its own hit
  rectangle, Tab stop and command; the current value takes the selected treatment,
  and activating it again writes nothing. The focus mark uses the gap cell before
  the segment. When any label does not fit, fall back to the single button that
  opens a menu.
- **Toggle row.** A boolean setting uses a full-row control: label at the left, an
  On/Off word and a short track with a knob at the right (neutral track with a
  muted knob at the left when off; accent track with a bright knob at the right when
  on). The word keeps the state readable without color; monochrome uses
  `[o   ]`/`[===o]`. The row takes square-fill hover/focus states.
- **Sections and pairs.** Form section headings are uppercase, bold and muted,
  separated by a full-width rule in the line color. Read-only label/value rows put a
  muted label at the left and a bright value flush right.

### Panel style across surfaces — 2026-09-23

The user then asked for the same language across the app. It applies to
content-reading and choosing areas, not to the compact controls governed above:

- **Right-host surfaces** (Plan, Agents, Activity, Usage, Files, Git, Terminal
  headers) paint structured rows: uppercase muted headings, rules between entries,
  status rows and label/value pairs. Pairs come only from explicit structured
  fields (protocol pairs the TUI composes, or `protocol.Activity.Tool`), never
  split from free-form `Key: value` text — see "Structured pairs only" below.
  Only short values sit right-aligned; longer values stack under a muted label.
  Terminal output stays raw, sanitized text.
- **Status glyphs** keep semantic ink: completed `✓` green, active/working `●`
  blue, pending `○` muted, failed `✕` red, interrupted/waiting/stale `!` amber,
  unknown/unavailable `?` neutral, and any other reported state a neutral `·`.
  Only completed states read as success. ASCII symbols use `+ * o x ! ? .`.
- **Empty host chooser** shows banded tiles in two or three columns, each its own
  hit rectangle and Tab stop, falling back to the list and then to the "Choose a
  surface…" button as space or color support shrinks.
- **Centered menus and dialogs** use an uppercase muted title (user-supplied names
  keep their case) with a rule beneath, and a muted hint row with a rule above.
  Pair rows are explicit per item for settings and usage facts; free-form text such
  as thread titles or option labels is never split. Destructive items use red ink;
  Cancel and informational items never do.
- **Navigation** labels its Closed section with the same uppercase heading, still
  dimmed, with the count only while collapsed.

This refinement did not band or restructure the composer and footer, question
card and tabs, queue card, thread cards, surface tabs, top chrome or activity
strip; the app-wide pass below adds only zero-row alignments to them.

### Panel style — app-wide pass — 2026-09-24

The user authorized further recommended restyling. These rules now apply:

- **Status vocabulary app-wide.** `panelStatusMark` glyphs and inks are the status
  language everywhere, including the conversation, activity strip, request cards,
  notices and other one-row-density areas. Circles remain only for active work and
  the all-completed Agents/Plan summaries (solid green `●`, per the activity
  rules). `✓` appears only for confirmed success; pending or failed delivery never
  shows it.
- **Zero-row alignment in one-row areas.** The composer, footer, question card,
  queue card, thread cards, surface tabs, top chrome and activity strip take marks,
  muted state words, uppercase static labels and explicit pairs only where they add
  no rows. If a mark would add a row, fall back to colored text without it.
  Uppercase applies to static labels (for example `ANSWER ANYTIME`,
  `APPROVAL REQUIRED`, `QUEUED n`, `ANSWERED`); user text keeps its case.
- **Actionable pair row.** A dense settings field is one row: muted label at the
  left, accent value flush right, whole-row hit rectangle with hover and focus
  feedback. Fixed (non-editable) values use muted value ink and no action.
- **Settings sidebar.** Uppercase heading and rule, single-row square-fill
  categories with a reserved focus cell, and a rule above Back. Brackets mark the
  selected category only in low-color or ASCII fallbacks.
- **Separator menu items.** Menus may contain non-selectable rule rows with no hit
  rectangle; keyboard navigation skips them.
- **Notice severity.** Each notice carries an explicit severity set by its caller:
  info `·`, unavailable `!`, error `✕`, done `✓`, active `●`. The mark is painted
  separately from the notice text; text is never parsed to infer severity.
- **Context lines.** The checkout line leads with a mark (Git identity icon for a
  branch, unborn or detached checkout; `○` loading; `·` non-Git; `?` unavailable or
  fixture), never green. The mark sits outside the line's hit rectangle.
- **Structured pairs only.** Activity inspectors show Kind, Status and Location
  pairs only from structured tool data (`protocol.Activity.Tool`); free-text detail
  is never split into pairs.
- **Empty states.** An uppercase heading, a rule and banded buttons, with
  `[Label]` fallbacks where bands are unsupported.
- **Menu position counter.** A menu's `n of N` counter counts selectable rows
  only, skipping separators. When any row in a menu has an icon, every row
  reserves that icon's slot so labels stay aligned, even the rows without one.
  The Commands menu groups its existing items with separator rows in their
  existing order; it does not add category headings.
- **Confirmation dialog titles.** Destructive confirmations use the
  `PREFIX · user` form (`DELETE THREAD · <title>`, `REMOVE PROJECT · <name>`):
  a static uppercase prefix followed by the user's text in its original case.
- **Footer overflow settings.** Hidden footer fields appear as explicit
  label/value pairs in the "More settings" menu, titled `MORE SETTINGS` and,
  while settings are read-only during active work, `MORE SETTINGS · READ-ONLY`
  (uppercased at render time like other menu titles; the stored title keeps
  its original case for the user-text form above).
- **`@`-mention popup.** The popup spans the composer outline's width (matching
  its inset) and sits directly above it, two rows above the prompt's top
  border. A selected entry uses the shared square-fill selection treatment
  plus the focus-mark gutter cell (`•`) reserved before the control, per the
  keyboard-focus rule above.
- **Approval and question actions.** Approval card actions are right-aligned
  in the same fixed action group as question Submit/queue actions
  (`questionActionsPlan`): Cancel, then Decline, then Submit, overflowing into
  a More… menu before Submit is ever displaced. The question body keeps one
  gap cell before its scrollbar column.
- **Queue legend.** The `QUEUED n` legend is bold, muted text that stays in
  the card's border row; moving it inside the card would add a row.
- **Status line.** The empty receipt state shows plain `Accepted`. An
  unconfirmed steer notice reads `Steer accepted · awaiting delivery` with the
  active `●` mark; once delivery is confirmed
  (`delivered`/`fixture-delivered`) it reads `Message steered into the active
  turn` with the done `✓` mark.
- **Right-host status rows.** The state word takes its mark's semantic ink
  app-wide (muted for unknown/neutral, and so on), instead of a separate
  fixed color. A Colors caveat is a note on the single `Colors` pair: it folds
  into `value · note` when `panelPairFits` allows the combined text, otherwise
  it continues on its own right-aligned muted line.
- **Activity tab title.** The Activity surface tab's label reads "Usage"
  while its usage detail is open, and "Activity" otherwise. This is a
  render-time title change only — it is still the same Activity surface kind,
  and it keeps the Activity glyph, since no dedicated usage icon exists yet.
- **Tool payload budget.** `structuredTool` folds any retained (`kept`) raw
  input/output into the same `toolBudget` split as the current update's
  fields, so kept and new payloads share one `MaxActivityDeta` budget instead
  of each being truncated to it independently; `bound`/`boundEntries` strip a
  prior truncation marker before re-truncating, so a field already marked
  truncated never gains a second marker. Content and location entries beyond
  the 64-entry cap (`capEntries`) keep the first 63 and end with a
  truncation-marker entry, so the cap holds and the loss stays visible, all
  within the shared budget.
- **Transcript tool/MCP rows.** The state word takes its status mark's ink,
  matching right-host status rows; the ` · ` separator before it stays muted.
- **Activity inspector, no repeated status.** The Status pair appears only
  when the activity itself carries no state; a plain muted "Reported" pair
  appears only when the agent-reported `Tool.Status` differs from the
  effective state. Neither repaints or duplicates the row's own status mark.
- **Right-host Terminal surface.** Opens with a `TERMINAL` heading and rule
  like other right-host surfaces. The bottom terminal panel is unchanged: it
  reuses the same blocks without a title row.
- **Git checkout facts are all pairs.** The checkout path is a long pair;
  Branch/HEAD/Revision are explicit pairs for their structured states;
  loading, unavailable and `Repository · not a Git checkout` are muted pairs.
  No status marks and no green ink appear on checkout facts.
- **Agents surface Parent.** Shows the owning thread's title when the parent
  thread is known locally, falling back to the raw parent ID otherwise.
- **Menu group separators.** A separator rule spans the same width as the
  menu's heading and hint rules.
- **Settings dense pairs.** App General's "Project starting folder" and
  Project "Icon" are dense actionable pair rows, with a long path
  left-truncated (keeping its identifying tail) rather than wrapped. Name,
  segmented-choice button fallbacks, "Use built-in defaults" and "Remove
  project…" keep the banded-button treatment.
- **Menu Note rows.** Menus support a non-selectable, muted `Note` row
  (`menuItem.Note`) with no hit rectangle, skipped by keyboard/wheel
  navigation and by the `n of N` position counter — for example "Files on
  disk will be kept" in the remove-project confirmation.

## Building components

### Centered menus and dialogs

An outside left-click dismisses an open centered menu or dialog through its close
action. Consume that click: it must not activate, resize, select or submit anything
in the newly exposed workspace. The full outline and interior padding count as
inside the dialog. Pointer motion, scrolling and other mouse buttons do not
dismiss it. Keep the existing explicit close and Escape paths, preserve the prompt
draft, and do not turn dismissal into confirmation of a destructive action.

Subdue the existing background while the centered dialog retains normal readable
selection and focus styling. A terminal grid has no pixel blur; reduce color
contrast toward the theme canvas instead, preserving text geometry and semantic
role hues. Use bounded per-frame styling without accumulating ANSI sequences.
Limited palettes use subdued available colors; monochrome retains the outline
and modal input boundary without claiming a blur effect. This treatment applies
to centered overlays, not ordinary docked surfaces or pending question cards.

### Construction boundary

Pass variant, semantic role, selected, hovered, focused and enabled state explicitly
into a shared visual resolver. Never infer selection by comparing background
colors. Each language may use idiomatic types; share these observable rules rather
than implementation APIs. Rendering remains pure, with no storage/network/process
operations. Ordinary palette or radius choices do not each require their own ADR.

Resolve state once, then use one measured rectangle for painting and hit testing.
Edges activate the same primary command as the label. Preserve distinct secondary
slots: a tab's type icon swaps to X on hover/focus, and only that glyph cell closes it.
Thread status/Close/trash/menu actions retain their lifecycle rules and targets.
Hover alone must neither select nor dispatch an action.

Use terminal-cell and grapheme-aware measurement, fixed icon slots, and bounded
ANSI composition. Styling must not trigger layout changes or accumulate invisible
escape sequences. Question arrow slots remain reserved when hidden, the current
tab stays visible, and overflow exposes only genuinely hidden content. Question
cards stay bounded, preserving the prompt, drafts and explicit Submit.

## Acceptance checks

- Review all variants at rest, hover, selected, focused and combined states in both
  themes, limited colors and monochrome. Hover must not look like selection.
- Check selected tabs outside keyboard focus, primary action buttons, inputs and
  disabled controls. Preserve semantic status colors and existing icon swaps.
- Verify unchanged hit rectangles, label/icon separation, keyboard activation,
  reserved arrow slots, narrow overflow, and focus/draft restoration after resize.
- Inspect the real terminal as well as deterministic captures. Record terminal and
  connection details; one local result does not establish SSH/tmux parity.

Record implementation checks separately from this accepted specification. Earlier
build, PTY and screenshot results do not validate the new state combinations.

The Go implementation's [component validation](../research/go-components-2026-09-20.md)
records fresh state matrices, application captures and local PTY checks. Other
language implementations and untested terminal/connection combinations remain
outside that evidence.
