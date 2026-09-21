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
