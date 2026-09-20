# Layout scope

Implementation evidence (2026-09-19): the [first Go slice](go-slice.md) now exercises a fixture-driven subset of this contract. See the [validation report](../research/go-slice-validation-2026-09-19.md) for executed checks and limitations; provider/editor/real-terminal behavior below is not implied by fixture results.

Status: overall arrangement accepted in Q7; later visual-review corrections supersede Q16's initially visible right sidebar. The controls and surface lifecycle below are accepted. Detailed dimensions and bindings remain prototype work; v3 styling is still a visual proposal. No runtime behavior has been verified. This diagram illustrates placement with all panes shown, not startup state.

```text
┌─────────────────┬───────────────────────────────────┬──────────────────────┐
│ LEFT NAVIGATION │ CENTER WORKSPACE                  │ RIGHT SIDEBAR        │
│                 │                                   │                      │
│ New             │ Primary application workflow      │ Active surface       │
│ Recent items    │                                   │                      │
│ Domain-specific │ ADE example: thread,              │ Examples:            │
│ navigation      │ activity, and prompt              │ Files or Git         │
│                 │                                   │                      │
│                 ├───────────────────────────────────┤                      │
│                 │ BOTTOM PANEL                      │                      │
│                 │ Terminal or another surface       │                      │
└─────────────────┴───────────────────────────────────┴──────────────────────┘
```

The bottom panel belongs to the center column. It does not span beneath the left or right sidebar. Its contents are extensible; “bottom panel” must not become a synonym for “terminal.”

The accepted direction includes hiding sidebars, resizing by mouse or keyboard, and expanding a surface for detailed work. The bottom panel follows the same intent, with a horizontal divider controlling its height. Arbitrary nested splits and docking are outside the initial scope.

Application content is illustrative. Projects and threads can populate the navigation in an ADE example, but a different application can supply different entities and a different central workflow.

## Persistent controls and navigation

Keep a show/hide-left icon at the application's top-left. At top-right, the controls are exactly, from left to right: maximize/restore the right host's active surface, show/hide the center-bottom panel, and show/hide the right host. This supersedes the earlier two-icon top-right arrangement. Maximize belongs in this application chrome, not the right surface header. Show maximize/restore only while the right host is actually visible, including a deliberately opened empty chooser; a host hidden by a toggle or responsive geometry has no maximize control. All have keyboard equivalents, with exact bindings deferred under Q24. Hiding a region must not hide its restoration control.

The ADE has no global content search. The 2026-09-20 review adds a searchable project selector and existing-folder Add project action; [project filtering](projects.md) stays local to each client. File search remains part of the file workflow. Closed replaces Recents and can collapse to its heading or hide entirely, with a discoverable restoration action even when hidden. Neither action deletes its items. [Close/Reopen and permanent Delete](threads.md) have separate row actions.

## Controls and composer refinement (2026-09-19)

The first implementation review requests Nerd Font control icons, assuming a patched font such as JetBrains Mono or Maple Mono. Use a consistent, verified icon set: the Go slice uses Codicons for shell controls and Nerd Font Font Awesome glyphs for the requested circular Send/Stop controls and paperclip. Navigation, bottom-panel and right-panel controls have distinct open/closed glyphs; maximize/restore also changes glyph. Keep descriptive hover/focus labels and keyboard paths. Fonts cannot be reliably detected through terminal protocols, so provide an explicit plain-symbol fallback.

Tabs show a surface-type icon followed by its name. Hovering the tab replaces that icon with a close glyph; keyboard focus on the icon reveals the same action. Clicking the name selects the tab; only the icon slot closes it. Show overflow only when some tabs are hidden, keep the active tab visible, and use one row per surface in the overflow menu with the same icon/close slot. Do not duplicate every surface into separate select/close entries.

Center close glyphs within their cell hit areas, including modal headers and tab
menus. Give the typing area a complete, clearly visible outline, with the
settings/actions row outside and below its bottom border. Retain the outlined
input's bounded growth and internal scrollbar. Keep the prompt text and footer
controls inside shared two-cell side
gutters, reducing those gutters only at tiny widths. Selected agent/model/settings
align left; usage, attachments, Stop and Send align right on the same footer row.
Keep this footer to one row. As its available width decreases, first compact
usage to its clickable gauge with a usage ellipsis, then progressively move
lower-priority settings into their own vertical ellipsis menu. Show the settings
ellipsis only when it contains hidden controls; restore those controls automatically as space returns. Keep Send,
active Stop and the model visible longest, truncating long model labels by cell
width before moving them into overflow. Place the settings ellipsis directly
after the remaining left-aligned settings; if all those fields collapse, it
stays at their left inset. Usage has a separate ellipsis at the far left of the
right-aligned group, before the visible gauge and attachment/Stop/Send controls.
If necessary the gauge also collapses into that usage ellipsis, which opens
full context occupancy, capacity, percentage, billing, limits and cost details.
Never mix usage into the settings menu or invent missing telemetry. Keep every
hidden value/action reachable by mouse and keyboard. Hidden recovery actions
or selected/running differences give the settings ellipsis an amber tint. An
open menu keeps its item order during resize and incoming activity, so a pending click/Enter cannot select a different item.
Keep the same layout when the right surface is maximized. This 2026-09-20 review
supersedes the earlier request to wrap settings; it changes presentation only,
never drafts, captured settings or execution.

Enter sends from the prompt composer. Shift+Enter inserts a newline, with Ctrl+J as a fallback when the terminal cannot distinguish modified Enter. Pasting multiline content never submits it. Grow the composer with explicit and wrapped lines to a bounded height, then scroll without discarding text. Eight visible lines is the initial implementation choice within the user's suggested five-to-eight range; short windows reduce that cap to preserve requests, settings and usage. Answers still require their separate explicit Submit action.

Scrollable content shows a proportional scrollbar only when it overflows. Support wheel and keyboard scrolling, track paging and thumb dragging where cell geometry permits it. Input read-scrolling preserves the insertion cursor and draft; typing follows that cursor again. A one-cell viewport can show a position indicator but has no vertical thumb travel; wheel and keyboard remain available. Keep scroll positions bounded and preserve the input-burst performance correction.

Show Thinking while the reported turn is actively running; clear it when the turn completes, fails or is interrupted. Waiting uses a distinct status and does not claim hidden reasoning. Stop is the user-facing name for interrupt/cancel, available only during an active turn, including waiting for a question or approval; dispatching Stop is not confirmation that work ended. At the far right of the footer, Send uses a blue circle; while active, Stop uses a red circle directly to its left. Put the paperclip directly left of Stop, or Send when Stop is absent, with the context-usage gauge to the left of these controls. Keep keyboard paths and labels/fallbacks. Preserve this footer when the visible right host or empty chooser is maximized.

## Background attention

Questions, approval requests and failures in other threads appear as thread badges plus a clickable attention indicator in the top bar. Keep it available when the left navigation is hidden and while a surface is maximized. Explicit activation opens the relevant item; incoming events never steal focus or change the selected thread. This indicator is separate from the three ordered pane controls. Exact artwork and presentation for multiple items remain prototype details.

## Right sidebar surface lifecycle

The right sidebar hosts explicit dynamic tabs for opened surface instances rather than listing every available surface permanently. Every surface type uses the same tab `×` close control and top-right maximize/restore action, including when it is the only tab. An Add surface action opens a chooser. Files, Git, Terminal, Agents, Plan, and Activity are available surface types, not automatically opened entries. Selecting a tab reveals that instance while preserving the others' state.

Within one right host, every non-terminal surface type is a singleton: at most one Git, Files, Agents, Plan, Activity, and each custom non-terminal type. Agents, Plan, and Activity are separate types. Adding an already-open type focuses its existing tab and reveals the host rather than creating a duplicate. Multiple file buffers inside Files do not create multiple Files surface tabs; this cardinality rule concerns host tabs.

Terminal is the only repeatable surface type. Multiple independent terminal instances can be opened in the right host alongside the existing center-bottom terminal. Repeated New terminal actions create distinct instances, visibly distinguishable as, for example, Terminal 1 and Terminal 2; they must not silently reuse one shell. Numbered names illustrate identity, not a fixed instance limit.

| Action or state | Required result |
| --- | --- |
| No opened surfaces | Sidebar is hidden by default. |
| Explicitly show an empty sidebar | Show the Add surface chooser; emptiness must not immediately hide this deliberate opening. |
| Add a surface | Open it in the host and reveal the sidebar within available geometry. |
| Add an already-open non-terminal type | Focus its existing singleton tab and reveal the host without duplicating it. |
| New terminal again | Create another independently identified terminal instance and tab. |
| Close a tab | Close that surface instance through its own `×`; for a Terminal tab, also end its session. Preserve unrelated tabs and their state. |
| Hide an occupied sidebar | Preserve its opened surfaces, documents, and view state. |
| Show an occupied sidebar again | Restore the preserved surfaces and selected view. |
| Close the last surface | Hide the sidebar; closing is distinct from merely hiding its host. |

Closing a surface must respect the shared document's preservation and autosave contract; it is not an implicit discard. The implementation review settles icon-plus-name tabs with hover/focus close targets; singleton reuse, explicit tabs, and individual close controls remain unchanged. Child detail opens/reuses Agents, plan detail opens/reuses Plan, and tools/MCP open/reuse Activity, revealing the host without discarding documents or unrelated surfaces.

Terminal processes remain server-owned. Switching tabs, hiding a host, or maximizing/restoring a surface must not start, restart, or terminate a shell. Each terminal independently has one explicit input/resize controller across connected clients; selecting another instance does not grant control of it.

## Terminal close and panel hide

The user rejected the proposal that a terminal tab's close button merely detaches its view. Follow the researched [Codex/T3 distinction](../research/terminal-lifecycle.md): closing a terminal ends its session, while hiding its containing panel preserves it.

| Control | Required result |
| --- | --- |
| Right Terminal tab × | End that terminal session and close its tab. Other terminal sessions remain intact. |
| Top-right right-panel toggle | Hide/show the host and preserve all its terminal sessions. |
| Top-right center-bottom toggle | Hide/show the bottom panel and preserve its terminal sessions. |
| Bottom terminal's own close control | End that terminal session. This is separate from the panel toggle. |

Close addresses the stable server terminal identity and stops its PTY/shell work; it is not a command sent to whichever terminal happens to be selected later. Other clients observing that session receive its ended state and cannot keep sending input or resize events. Unrelated terminals and agent runs continue. Closing a terminal does not roll back its filesystem changes.

Expose closing, ended and failed/uncertain shutdown honestly; removing a tab or receiving a request acknowledgment does not prove the process exited. Retain enough information to reconcile a failed close. A newly opened terminal starts a new session; showing a hidden panel reuses surviving sessions and must not silently resurrect a closed one. Explicitly opening the bottom terminal after its last session was closed creates a fresh session; replaying stale view state does not. Saved output and attachment captures follow storage retention independently of process lifetime.

Exact confirmation presentation, shutdown escalation and close authority across clients remain implementation/prototype details. The distinction between close and hide is settled, and both actions require keyboard and pointer access.

## Maximize and restore

The first top-right control expands the visible right host's active surface or empty chooser into the available application workspace. The empty chooser follows the same maximize/restore geometry. This applies to every surface type, including Git, Files, Agents, Plan, Activity, custom surfaces, and each Terminal instance. Preserve application chrome and the required ADE composer configuration and usage footer. Restore returns to the preceding pane arrangement and sizes without closing tabs or resetting surface state. Expansion changes presentation geometry; PTY resize still follows that terminal's controller authority. Exact keys and small-screen geometry remain prototype details.

The ADE requires a persistent [configuration strip](thread-configuration.md) at the bottom of the prompt box: agent, model, effort, permissions, and applicable context/speed settings, with selected versus active values identified. Keep a compact configuration footer during surface expansion. The optional bottom terminal is a separate pane below the center workflow and its composer.

The ADE also requires a [usage display](usage.md) available when sidebars or the bottom panel are hidden. A compact context gauge sits to the left of the composer action controls and opens full usage detail. Selected context capacity and measured context occupancy are different values. This does not require another pane.

## Fixed area above the prompt

Permission requests use distinct approval cards in this same area, showing the action, supplied targets and supported decisions, with full detail in Activity. They coexist with questions through request navigation and preserve the normal prompt draft. See [approval cards](activity.md#approval-cards).

Keep a fixed area immediately above the prompt, outside transcript scrolling, with one Agents summary, a compact Plan summary and pending requests. Show “Agents” with a pulsing blue circle for established active work. Only when every child has successfully completed, use a solid green circle; retain the summary until explicitly dismissed. On hover or keyboard focus, the completed circle becomes an X in the same leading icon slot. Only that slot dismisses; the label still opens history. Keep the working count and full state in hover/focus help and the inspector. Never offer dismissal while work is unfinished. Activating the summary opens the singleton Agents surface, where individual children and full available history remain accessible. Plan uses completed/total progress (n/N), the same blue working and solid green all-completed states, and the same completed-only dismissal. Failed, interrupted, waiting, stale and unknown states stay distinct and never imply success. Dismissal belongs to the frontend thread view; new work or a changed group/plan reopens its summary, while unrelated stream updates do not. Tools/MCP continue to open Activity. See [activity](activity.md).

Question sets show one question at a time via top tabs and conditional Back/Next, preserve typed answers/selections, and have an explicit Submit separate from navigation. Answers target the request rather than the prompt queue. Keep pending requests visible and accessible in narrow and maximized layouts, alongside preserved ADE configuration/usage access. Compact tokens and exact arrangement remain prototype work. Blocking versus asynchronous delivery is capability-dependent and specified in [questions](questions.md).

Existing file buffers and each surface's view state survive inspection; viewing a subagent does not change the thread's assigned agent or execution location.

When space is tight, use compact Agents and Plan summaries and one scrollable question or approval card at a time. Keep the prompt visible and settings/usage reachable through the responsive footer. Individual children remain accessible in Agents. Omit absent or explicitly dismissed activity instead of reserving empty rows.

Bound the request card's height and scroll its content independently. Keep request selection, answer navigation and the explicit submission/decision controls reachable without replacing the composer. Selecting another pending request preserves drafts. Apply the existing right/left and bottom-pane collapse rules before allowing fixed activity to crowd out the prompt. Numeric breakpoints, row budgets and minimum dimensions remain prototype decisions.

## Unavailable terminal features

User-requested behavior, 2026-09-19: when an attempted action cannot work in the current terminal/session, show a brief, non-blocking notice that automatically disappears. For example, “Image paste isn't available in this session” or “Image preview unavailable; showing file details.” Name the unavailable action and include a useful fallback when one exists. Do not require acknowledgment, steal focus, move the reading position or alter the prompt draft, attachments or selection. Use readable text and a styled status area; the notice itself must not depend on graphics or a special font. Exact placement and dismissal timing remain prototype choices.

Apply this across optional terminal features, including clipboard and graphics paths through SSH/tmux. Distinguish known unsupported capabilities, denied access, timeout/unknown support and ordinary operation failures; do not blame the terminal when the cause is unconfirmed. Capability checks must be bounded and must not block input/rendering. Quietly use an available presentation fallback until an explicit action needs explanation. Coalesce repeated identical failures so clicks or repeated events cannot build an alert backlog. An old dismissal timer must not erase a newer notice.

The notice's disappearance does not resolve an underlying failed save, attachment capture or blocked submission: preserve the relevant error state and affected work until it is resolved. If the terminal consumes a gesture without delivering any event, the app cannot detect that attempt; provide an explicit application action for capability-sensitive operations so it can reliably report a result. This is the shared behavior contract, not a claim that every capability or notice path is implemented in the first Go slice.

## Switching threads and projects

Switching threads restores the destination thread's open surfaces, prompt draft and reading position. Returning restores the prior thread's view instead of replacing its draft or jumping to the latest output. Files and Git follow the selected thread's checkout. Keep each thread's surface associations distinct while applying the singleton rules within the displayed host.

Existing terminals retain their server-owned shell sessions and working directories. Thread or project navigation must not issue a directory change, restart a shell, substitute a different terminal, or transfer its input/resize controller. Restoring a terminal view attaches to that same session where available; an exited or unavailable session remains explicitly identified. Server restart still requires the separate recovery contract.

Workspace layout preferences supply sizes and responsive behavior; opened surfaces and reading/draft restoration belong to the thread view. Navigation in one frontend does not move another frontend's focus or selected thread. Switching threads preserves surviving terminals; explicit close ends the selected terminal under the separate lifecycle above.

## Accepted responsive defaults

The left region begins visible and the bottom panel begins hidden. A fresh workspace with no opened right-side surfaces keeps the right sidebar hidden; the surface lifecycle above replaces Q16's unconditional visible-right default. Remember workspace sizes and visibility preferences, and restore opened surfaces with each thread's view as described above. At narrow widths, collapse the right sidebar before the left. At short heights, collapse the bottom panel. Expansion and restoration retain the persistent region controls and ADE configuration/usage footer as described above.

Keep remembered user preferences separate from effective layout constraints. Temporarily hiding a sidebar because the terminal is narrow must not overwrite the user's preferred visible state or stored width. Likewise, opening an inspector should reveal it within the available geometry rather than corrupting minimum pane sizes. Exact tiny-terminal presentation remains a visual-tuning detail.

Client focus, scroll position, and active selection are frontend-local. Multiple simultaneous clients are accepted; persisted layout preferences do not make them mirror one another's focus or use incompatible dimensions. Peer file cursors are presence indicators, not changes to local focus. See [server design](server.md) for shared state and terminal control.

## Decisions still needed

- Initial numeric sizes, minimum dimensions, breakpoints, and tiny-terminal presentation.
- Exact expansion/restore bindings and layout-preference storage across frontend types; the top-right location and order are settled.
- Focus traversal and return, command access, shortcut defaults/remapping, and pointer selection semantics. Q24 explicitly defers exact bindings to an interactive prototype; Ctrl+Space, F4, and F6 are unselected suggestions.
- Tab appearance, overflow, and file-buffer preview/pinning. Non-terminal singleton reuse, repeatable terminals, universal tab `×` and maximize/restore, open-only listing, empty chooser, last-close hiding, and document preservation are settled.
- Initial shell choice, child-mouse versus selection behavior, terminal-controller transfer/close authority, and shutdown/confirmation details. Terminal close ends the session; panel toggles preserve it. Terminals can occupy the right host and center-bottom panel, while the bottom panel remains usable for other surfaces.

These choices will be resolved through the [interview](interview.md), rather than inferred from the schematic.
