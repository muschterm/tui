# T3 Code design reference

Inspected **2026-09-19**, at the user's request, from the local checkout `/Users/muschterm/Developer/git/github.com/pingdotgg/t3code`. Recorded HEAD: `72c44a847c0a76f33b0d21f47548125b7032ec35`; origin identifies `pingdotgg/t3code`. This is source inspection plus visual review of the bundled marketing screenshot, **not a running-app check**. The screenshot can depict a different version from current source. No files in that checkout were changed and no code or brand assets were copied into this project.

The links below pin the inspected source revision so later agents can distinguish evidence from an evolving upstream app. The relevant behavior is translated into our [layout](../design/layout.md) and [visual design](../design/visual-design.md); T3's implementation conventions are not project instructions.

## Composition and controls

The bundled [marketing screenshot](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/marketing/public/updated-screenshot.webp) shows quiet navigation, a broad reading area, restrained changed-file detail, and a composer whose settings sit in its footer. Small file/provider marks supply identity without an avatar card for every line. The useful lesson is a hierarchy of emphasis: prose first, current work second, secondary controls quieter.

| Source-derived pattern | Evidence | Translation to this TUI |
| --- | --- | --- |
| Main sidebar trigger sits independently at top-left | [AppSidebarLayout.tsx:102](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/AppSidebarLayout.tsx#L102) | Keep the navigation toggle reachable when navigation is hidden. |
| Bottom/right controls keep the same top-right offsets through layout changes | [ChatView.tsx:6895](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/ChatView.tsx#L6895) | Keep controls stable across hide/show; their hit areas must not jump with pane content. |
| Matching Lucide panel icons, pressed states, labels and shortcut tooltips | [PanelLayoutControls.tsx:40](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/PanelLayoutControls.tsx#L40) | One control-icon family, visible active/focus state, keyboard and pointer access. Exact keys remain deferred. |
| Shared geometry tokens for sidebar, controls and titlebar | [index.css:88](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/index.css#L88) | Define common cell insets, icon bounds and hit sizes instead of styling each widget independently. |

## Opened surfaces, not a permanent inventory

T3's [right-panel store](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/rightPanelStore.ts#L123) defaults to hidden with no surfaces. Its [show/hide/toggle transitions](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/rightPanelStore.ts#L612) change visibility without discarding surfaces. Its [close transition](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/rightPanelStore.ts#L507) selects a nearby surviving surface and hides the panel if none remain.

[RightPanelTabs.tsx:889](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/RightPanelTabs.tsx#L889) supplies a compact plus menu beside opened tabs. The [empty launcher](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/RightPanelTabs.tsx#L241) supplies icons, names, descriptions and unavailable reasons; its empty-state placement is at line 932. This closely supports the user's requested host behavior. Explicitly opening an empty host must leave the chooser visible instead of immediately hiding because the surface count is zero.

The launcher's [keyboard routing](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/RightPanelTabs.tsx#L335) checks typing contexts and overlays. Adopt that input-ownership discipline: a chooser command must not consume text intended for the prompt, editor or terminal.

T3's surface inventory also includes Browser, Terminal, Diff, Pull Request and Agents, and its tab context menu offers bulk-close actions. Terminal and Agents are independently required by this project's user; inspecting T3 does not accept its remaining inventory or bulk-close behavior. T3 remembers this state per thread; our workspace preferences and frontend-local selection remain governed by our own contract. Its browser breakpoint/sheet treatment does not replace terminal-cell responsive rules.

Subsequent [terminal lifecycle tracing](terminal-lifecycle.md) follows tab close through T3's server to PTY termination, and distinguishes the bottom drawer's visibility toggle from its explicit terminal-close controls. Closing a terminal is not merely removing its view.

## Conversation and composer

The source presents [user messages](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/MessagesTimeline.tsx#L1029) with end alignment and a bounded tinted message area. [Tool-group summaries](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/MessagesTimeline.tsx#L1609) are compact clickable rows with a small icon and failure-aware labels. These support our inset user message, open agent prose and concise activity rows. We retain the richer semantic colors requested through Omarchy/herdr.

[ComposerControl.tsx:8](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/ComposerControl.tsx#L8) centralizes small ghost-button geometry and icon sizing. The [composer footer](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/ChatComposer.tsx#L3955) separates model/settings from primary send/stop actions. Its compact layout moves controls into an overflow menu. We take the consistent geometry and visual grouping, but keep our required selected/running values visible with wrapping; the user's requirement takes precedence over copying T3's overflow behavior. We also reduce repeated chevrons rather than inheriting every dropdown indicator.

## SVG identity assets

T3 uses custom SVG components for [Grok](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Icons.tsx#L205), [OpenAI and Claude](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Icons.tsx#L487), and [GitHub Copilot](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Icons.tsx#L669), alongside Lucide control icons. Its [provider mapping](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/providerIconUtils.ts#L5) maps Codex to its OpenAI component. [ProviderInstanceIcon.tsx](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/ProviderInstanceIcon.tsx#L17) adds optional instance/status indicators and an initials fallback.

This demonstrates the SVG asset approach the user proposed. It does not independently verify the artwork's brand provenance or imply that every mapping is the official product identity. Our registry records sources and separates agent, provider, model and artwork identities. For the TUI, SVG becomes cached PNG/pixels at measured cell dimensions; the terminal does not execute React SVG components. See [graphics and official asset sources](visual-enhancements.md).

Translate spacing, alignment, restrained chrome and purposeful identity. CSS blur, shadows, rounded geometry, browser dimensions and hover-only access are not terminal requirements. Review the resulting cell layout and graphics lifecycle in real terminals before claiming equivalent polish or behavior.

## Project selection and thread lifecycle inspection — 2026-09-20

Read the same local checkout again at `/Users/muschterm/Developer/git/github.com/pingdotgg/t3code`; `git rev-parse HEAD` still returned `72c44a847c0a76f33b0d21f47548125b7032ec35`. Inspected `apps/web/src/components/Sidebar.tsx` and `apps/web/src/hooks/useThreadActions.ts` directly. This records source behavior, not a running T3 application check. No checkout changes, remote Git operations or asset copying were performed.

The sidebar's project control combines an all-project scope with named project entries, keeps the chosen scope in local component state, and filters entries from the same state as its search input. The inspected search matches project labels; displaying and searching server paths is our own addition. See [scope and search construction](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L1955) and the [project combobox](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L3550).

A folder-plus button sits beside the project selector and opens the add-project command palette. The inspected code establishes that entry point, not the behavior of a native folder picker or validation of paths. See the [add-project callback](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L1820) and [folder-plus control](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L3658). Our [project behavior](../design/projects.md) translates this to a searchable terminal picker and an existing server-folder input. T3's project settings button does not establish an accepted project-settings feature here.

T3's settlement action first checks environment support, then refuses starting/running work and threads awaiting approval or user input. Its inverse dispatches an explicit user reopening intent. Those source checks support keeping active work discoverable, but do not establish this application's lifecycle semantics. See [settle and unsettle actions](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/hooks/useThreadActions.ts#L494). The user's subsequent choice here is **Close / Reopen**, with a **Closed** navigation group; T3's settlement name and automatic-settlement policies are not adopted.

T3's sidebar asks before deleting conversation history when its user-configurable deletion-confirmation preference is enabled. Its deletion hook can separately offer removal of an orphaned worktree. See [conditional history confirmation](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L3276) and [separate worktree-removal question](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/hooks/useThreadActions.ts#L334). Our requested Delete flow uses one explicit confirmation by default and preserves project folders, Git worktrees and files. Close remains reversible and preserves history; Delete is a distinct action. These are user-selected behavior requirements, not claims that T3 implements the same contract.

## Compose action and thread surfaces — 2026-09-20

Inspected the same pinned checkout's [New thread button](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L3503)
and [row surface classes](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/Sidebar.tsx#L1121),
and viewed the bundled marketing screenshot again. New thread uses Lucide
`SquarePenIcon` inside an icon-sized sidebar button with an accessible name and
tooltip. Thread rows share a rounded surface with active, selected and hover
backgrounds; ordinary rows may be transparent. The screenshot shows an older
sidebar arrangement and does not independently verify the current button.

The Go translation uses Nerd Fonts v3.4.0 `fa-pen_to_square` (U+F044, verified
against the pinned glyph metadata) with a `+` plain fallback. Its location is the
right end of the navigation heading. The user's request for stronger containment
adds an always-visible quiet background, shared title/metadata padding and one separator row. The subsequent rounded-corner
request adds a full rounded outline matching the prompt. These are our terminal adaptations, not
claims that T3 draws bordered cards or uses the same spacing. No T3 runtime was
started, no assets were copied and no remote Git operations were performed.

## Updated sidebar/settings source inspection — 2026-09-20

Re-inspected the user's updated local checkout at
`/Users/muschterm/Developer/git/github.com/pingdotgg/t3code`, clean HEAD
`1ba471a37cd6b0f18820795f4505206f4723a0e3`. No pull, push or source mutation. This
adds evidence to the earlier pinned review rather than replacing its history.

- `apps/web/src/components/sidebar/SidebarThreadHeader.tsx`: flexible Search
  input with project scope, folder-plus and square-pen actions at its right.
- `apps/web/src/components/Sidebar.tsx` around 4410–4570: project filter swaps
  folder icon for `ProjectFavicon`; picker rows expose a right-aligned gear.
  Its bottom settled shelf near 4890 uses a collapsed count and expanded title.
- `apps/web/src/projectIdentity.ts` and `components/ProjectFavicon.tsx`: stable
  name-derived monograms/color, explicit overrides and optional asset fallback.
  Our user's first/last-letter rule is intentionally simpler than T3's digit and
  multi-word heuristic; terminal cells replace DOM image rendering.
- `components/settings/ProjectSettingsPanel.tsx` around 295–361: confirmation
  includes project threads/history and says files remain on disk.
- `components/settings/SettingsSidebarNav.tsx`: grouped settings navigation;
  `SettingsPanels.tsx` around 2791 gates restart continuation by server support.

The user selected this direction while retaining **Open/Closed**, explicitly
approved confirmed project-plus-thread removal, and requested per-environment
restart continuation off by default. These are source observations and recorded
choices; they are not evidence that this terminal app supports T3's providers,
assets, worktrees or recovery integrations. See [settings](../design/settings.md).

## Narrow right panel, maximize and request height — 2026-09-21

Re-read the same local checkout, clean HEAD
`1ba471a37cd6b0f18820795f4505206f4723a0e3`, to inform two behavior choices in the
Go code-review fixes. Source reading only; no runtime check, pull, push, source
mutation or asset copying. Paths are under `apps/web/src`.

- `rightPanelLayout.ts:1` sets the inline/sheet breakpoint at `(max-width: 980px)`;
  `components/ChatView.tsx` around 1749 reads it through a media-query hook and
  renders either inline `RightPanelTabs` or a right-side modal sheet. The choice
  is derived at render time. No stored open/width state is rewritten because the
  viewport is narrow: `hooks/useResizableWidth.ts` clamps on read and persists
  only at drag end. Widening returns to side-by-side with the stored per-thread
  width. Dismissing the sheet does close the panel, which is a user action.
- The maximize control (`components/chat/PanelLayoutControls.tsx:102-133`) renders
  only in the inline layout. Its flag is unpersisted component state and is
  masked, not cleared, while the sheet presentation applies.
- A maximized panel collapses the chat column to zero width, hiding the timeline,
  composer and pending approval/question UI. Nothing restores it when a request
  arrives. **We do not adopt this:** our accepted contract keeps chrome, prompt,
  Send and the settings/usage footer visible while maximized.
- Height budgeting sits on the composer side only: `ComposerBanner.Scroll` and
  `ComposerBannerStack` cap pending request content at
  `min(24rem, 40dvh)` with internal scrolling; approval detail is `max-h-20`.
  The panel has no minimum height and the timeline yields.

Translation in the Go slice: a right host that cannot fit beside the conversation
is *presented* full width without changing the stored `Maximized` preference, and
the maximize/restore control is absent while that presentation is forced. While
a surface occupies the center, the pending request card is capped near 40% of the
body height (never above its existing 12-row maximum) so the surface keeps usable
rows. These are terminal-cell adaptations under [layout](../design/layout.md);
whether T3's sheet is truly modal at runtime was not determined.

## Archived rows and Unarchive — inspected 2026-09-21

Read-only inspection of the local checkout at `1ba471a37`.

- Sidebar thread rows are single-line truncated titles (`components/Sidebar.tsx`
  around 1479-1489) with muted ink that rises to foreground on hover/focus.
- Archived threads are listed in Settings, not the sidebar
  (`components/settings/SettingsPanels.tsx` around 3388-3458). Each row has a
  title, an “Archived … · Created …” description and an outline **Unarchive**
  button using lucide `ArchiveX`; Delete is in the context menu.

- Settled sidebar rows reveal an **Un-settle thread** action using lucide `Undo2`,
  a turning-left arrow (`components/Sidebar.tsx` around 1693-1711). The user
  identified this, not `ArchiveX`, as the re-open icon to follow.

Translation in the Go slice: Closed sidebar cards use one content row and dimmed
ink, and the trailing slot shows a Reopen icon instead of the ellipsis, using
Nerd Font `md-arrow_u_left_top` (U+F17B3) as the turning-left arrow matching
`Undo2`, revealed on hover/focus as T3's row action is. T3's
separate archive page and its description line are not adopted.

## Header breadcrumb and panel controls — inspected 2026-09-21

Read-only inspection at `1ba471a37`.

- `components/chat/ChatHeader.tsx` around 403-436: the breadcrumb leads with
  the project favicon and name as one button labelled “New thread in
  <project>” (`text-muted-foreground hover:text-foreground`), then a `/`
  separator, then the thread title.
- `components/chat/PanelLayoutControls.tsx`: bottom and right toggles use
  lucide `PanelBottomIcon`/`PanelRightIcon`; maximize/restore uses
  `Maximize2Icon`/`Minimize2Icon` (diagonal double arrows).
- `components/ChatView.tsx` around 9540-9585 and 10365-10375: the toggle
  cluster is one element that renders in the header while the right panel is
  closed and is passed to `RightPanelTabs` as `layoutControls` while it is open;
  the maximize control is shown only while the panel is open.

Translation in the Go slice: title and breadcrumb in the top bar, toggles over
the right host's top-bar span while it is visible, Nerd Font
`fa-up_right_and_down_left_from_center`/`fa-down_left_and_up_right_to_center`
for maximize/restore. T3 has no application-title control and no attention bell;
those remain this project's decisions.

## Terminal drawer tabs, tab insets and focus rings — inspected 2026-09-22

Read-only inspection of the same local checkout; `git rev-parse HEAD` returned
`1ba471a37cd6b0f18820795f4505206f4723a0e3` (2026-09-20), a newer HEAD than the
earlier sections record. No checkout changes, remote Git operations or asset
copying were performed.

- `components/ThreadTerminalDrawer.tsx` around 1421-1490 and 1640-1700: the
  drawer has no title row. Its chrome is an action cluster (split, `Plus` for a
  new terminal, `Trash2` to close the active terminal) and, once several
  terminals exist, a tab list where each row is `PanelTabCloseButton` (icon that
  swaps to `X` on hover/focus) plus the terminal label; the active row uses
  `bg-accent`. Around 1411 an empty drawer shows “No terminal sessions for this
  thread yet.” with a New terminal button rather than opening one.
- `components/RightPanelTabs.tsx` around 1152-1165: tabs are
  `h-6 max-w-36 rounded-md pr-2 pl-1.5 gap-0.5 text-xs`, i.e. roughly one
  character of inset on each side and the icon nearly touching the label.
- `components/ui/button.tsx`: keyboard focus is `focus-visible:ring-2
  ring-ring ring-offset-1` (or `outline-2`), a ring outside the element that
  never moves its content.

Translation in the Go slice: tab insets shrink to one cap cell each side with the
glyph left-aligned in its slot; the focus ring becomes an accent mark in the cell
before the control; the bottom panel becomes a terminal tab row with an add
control. The user's request that showing the wide panel opens a session at once
departs from T3's empty state; T3's split views and close-active-terminal action
are not adopted.

## Command-line surface — inspected 2026-09-22

HEAD `1ba471a37`, read-only. T3 Code is an Electron desktop application with a
web build; the checkout has no end-user CLI, `bin` entry, completion script or
`--help` surface (`apps/*` and `packages/*` expose only build and test scripts,
and the `effect-acp` package is a protocol library). Nothing there informs the
Go reference's command line, which follows Go CLI conventions instead
([ADR 0012](../adr/0012-go-cli-framework.md)).

## Questions, user messages and composer placeholder — inspected 2026-09-23

HEAD `1ba471a37`, read-only. Pending user-input prompts
(`apps/web/src/components/chat/ComposerPendingUserInputPanel.tsx`) dock
directly above the composer textarea inside the same surface as an attached
banner, not in the transcript. The header row shows a neutral icon slot, the
question header in muted text and, for several questions, an `n / total`
counter at the right. Questions are shown one at a time; single-select
auto-advances after a short delay and multi-select toggles in place. Options are
full-width rows with label and optional description: selection is a tinted
background plus a trailing check, and unselected rows show a `1`–`9` shortcut
key at the trailing edge. There is no separate Other field: the composer
textarea becomes the free-text answer and its placeholder changes to “Type your
own answer, or leave this blank to use the selected option”. There is no
dedicated Submit; the ordinary Send button commits the answer. Approval
requests (`ComposerPendingApprovalPanel.tsx`) use a warning-tinted variant with
inline Decline/Approve buttons and an overflow menu for extra options.

User messages (`MessagesTimeline.tsx:2089`) are right-aligned bubbles
(`max-w-[80%]`, `rounded-2xl`, `bg-message`, `p-3`) with a screen-reader-only
“You” heading and no avatar; assistant messages are plain full-width Markdown
with no bubble. The main composer placeholder (`ChatComposer.tsx:6876`) is “Ask
anything, @tag files/folders, $use skills, or / for commands”, with contextual
overrides for approvals, pending questions and disconnected states; the body
has top padding before the textarea.

Applied here: the Go card keeps this project's accepted tabs, radio/checkbox
rows, indented Other field and explicit Submit, and takes from T3 the neutral
in-container header with a page counter, selection feedback bounded to the row
content rather than the full width, digit shortcuts for options and a padded
composer with a single “Ask to do anything” placeholder. User messages become a
full-width tinted box aligned with the prompt outline rather than a
right-aligned bubble, at the user's request.

## Claude model naming — 2026-09-23 (HEAD `1ba471a37`, read-only)

T3 does not surface the Claude CLI picker names. `apps/server/src/provider/ClaudeModelCatalog.ts`
resolves rows from a bundled `model-manifest.json` whose Claude entries are
named by version ("Claude Opus 5", "Claude Sonnet 4.6", "Claude Haiku 4.5"),
keyed by canonical slug with alias lists; no Opus 5.5 entry existed at this
HEAD. This confirms version-specific row names as the expected presentation.
This project keeps native CLI discovery as its source and takes the versioned
identity from Claude's own description strings instead; see
[Claude effort and model rows](claude-effort-2026-09-23.md).
