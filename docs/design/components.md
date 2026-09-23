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
