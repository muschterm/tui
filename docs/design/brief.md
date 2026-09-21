# Design brief

Status: documented blueprint. This document records accepted requirements and identifies remaining prototype/implementation decisions. A first Go server/shell slice exists with synthetic activity; see [its current scope](go-slice.md). Provider, collaborative-editor, Git and real embedded-terminal integrations remain outstanding.

## Purpose

Make this repository a place where agents and developers can learn how to build a good-looking, pleasant terminal UI in Go, Rust, or Bun. Establish concrete coding and interaction standards with enough rationale and examples for future projects to follow them.

An agent development environment is the first example. It will connect to headless Codex and Claude using ACP directly or through adapters, and allow other ACP agents. The foundation must also work for applications whose concepts are not projects, threads, or agents. The first Go slice exercises presentation and application lifecycle without implementing those provider integrations.

## Requirements stated by the user

- Document the design thoroughly before implementing a particular application.
- Provide a Go implementation direction using Charmbracelet Bubble Tea and the kitty protocol; clarify which kitty protocols and what fallback behavior are intended.
- Research suitable, well-supported Rust and Bun implementation directions.
- Present a hideable left sidebar with a way to start something new, navigation, and recent items. Projects and their threads are illustrative content; the user originally called these chats and subsequently chose thread as the canonical term.
- Present a central working area that can host a prompt and activity, or another application's primary workflow.
- Present a right sidebar that hosts different surfaces.
- Include a file browser with viewing and editing. Markdown has rendered and raw views; the raw view supports edits.
- Include a visually polished Git surface inspired by desktop Git tools, with turn-related UI when the consuming application is an agent development environment.
- Make mouse interaction a first-class requirement, including resizing panes. Provide keyboard resizing as well; Ctrl+Space is an initial shortcut suggestion, not a finalized binding.
- Support use over SSH and through tmux, with a pleasant experience. Research and define what support can actually be promised.
- Research `herdr` as an interaction reference and Sublime's Git tooling as a visual/workflow reference.
- Replace the `AGENTS.md` TODOs with useful project-specific guidance.

## Decisions confirmed in the interview

- Deliver one shared design/behavior specification and three idiomatic reference applications. Extract reusable libraries when demonstrated reuse warrants them; see [ADR 0001](../adr/0001-shared-specification-idiomatic-reference-apps.md).
- Require macOS and Linux. On macOS, target iTerm2 and Ghostty. On Linux, target modern terminal environments, with exact test representatives still to be chosen.
- Treat Windows Terminal as best effort. Windows compatibility does not block releases or drive the initial design.
- Use the three-column arrangement with hide/resize/expand behavior, plus a bottom panel inside the center column. The bottom panel may host a terminal or another surface; arbitrary nested splits and docking are outside the initial scope. See [layout scope](layout.md).
- Include portable keyboard/mouse workflows, negotiated kitty keyboard enhancements, and optional inline graphics in the initial design. Images must not become a prerequisite for core SSH/tmux workflows.
- Use stable ACP v1 as the ADE's agent boundary: native ACP connections where supported and adapters otherwise. Codex and Claude are required integration examples; provider differences must be explicit. See [ADE design](ade.md).
- Include the [lightweight editor](editor.md), including selection, undo/redo, search, recoverable buffers, external-change handling, and rendered/raw Markdown. The later Q25 answer adds real-time edit synchronization, autosave, and cursor presence across connected clients; it supersedes explicit Save as the proposed normal workflow.
- Expand the [Git client](git-client.md) to a commit graph with context-menu actions, soft reset, fast-forward-only pull by default, rebase, manual conflicts, and agent-assisted resolution with agent selection.
- Use **thread** for the user-facing interaction entity, with one chosen agent per thread. Agent switching and handoffs, including future handoff documents, are deferred. Upstream protocol sessions keep their native names.
- Make [usage visibility](usage.md) a required ADE feature: current thread context usage/capacity, applicable provider subscription windows such as 5 hours or 7 days, and running API cost when supplied. Omit subscription quotas for known API billing. Use only agent-supplied data; no internet lookup or separate quota/pricing requests are needed. Unavailable data must remain explicit.
- Use the existing checkout by default; offer worktrees as an option. Queue writing threads and jobs per checkout; optional worktree lifecycle and scheduling details remain to be specified. See [workspace behavior](workspaces.md).
- Let the chosen conflict-resolution agent edit files and run relevant checks, then stop for user review before staging resolutions or continuing the merge/rebase. This corrected Q10 answer supersedes the earlier automatic-continuation choice.
- Use Ratatui for Rust; TypeScript + React + public Ink for Bun, with `Bun.Terminal` for interactive subprocesses. The Bun direction follows Claude Code's publicly evidenced React/Ink lineage, without assuming its custom renderer is available as a reusable package.
- Present plan steps, subagent runs, tool calls, and MCP calls cleanly. Clicking an activity item opens available full details in the right sidebar; subagents expose their available complete transcript and tool history. See [activity presentation](activity.md).
- Implement the easiest reference first, as requested by the user: Go with Bubble Tea and the Charm ecosystem. The assistant selected Go based on its established renderer/components and the additional integration work in the other selected stacks. See [implementation sequence](implementation.md).
- Queue writing threads/jobs per checkout; independent worktrees can run concurrently. Keep this queue distinct from each thread's prompt queue.
- Each language reference includes a native background server and TUI, sharing a client/server contract. One server per user/application manages all projects and threads. Opening the app ensures it is running and attaches; explicit server start/status/stop commands manage it. TUI exit leaves work running; stop gracefully cancels owned work and processes. Reattached clients catch up. See [server design](server.md).
- Multiple clients may attach via authenticated loopback HTTP/WebSocket, with SSH forwarding for remote access. Focus/scroll/selection remain client-local; each interactive terminal has one input/resize controller. A web frontend is later scope.
- Persist machine/user-level application state under its own home directory, with an application-specific environment override and SQLite wherever practical. Necessary auxiliary files belong there too. `my-app`, `~/.myapp/`, and `MYAPP_HOME` are examples, not selected identities. See [storage](storage.md).
- Defer exact key bindings to an interactive prototype, retaining keyboard completeness, remapping, fallback access, and mouse equivalents.
- Record a start/end turn comparison labelled changes observed during this turn, alongside provider-reported edits where available. Keep current/staged/branch diffs distinct; automatic rollback is deferred.
- Queue prompts submitted during an active turn and provide explicit interruption. An initial prompt, accepted steering input and resulting agent work belong to the same turn.
- Offer explicit [Steer](activity.md#steering-a-queued-message) on queued messages
  to deliver one into the same active turn where the agent supports it. Preserve
  captured content/settings, active-turn identity and recoverable delivery; do not
  substitute interruption, reordering or a new turn. This extends the initial
  queue-only decision; ordinary Send still queues during active work.
- Default to a visible left region and hidden bottom panel; the later surface-host correction supersedes Q16's initially visible right sidebar. Show only opened right-side surfaces and provide Add surface. With none opened, hide the host; explicitly showing it empty opens the chooser. Hiding preserves surfaces; showing an occupied host restores them; closing the last hides it. Opening activity detail may open/reuse an inspector and reveal the host without discarding documents. See [layout scope](layout.md).
- Show explicit dynamic right-side tabs for opened surfaces. Every type gets tab `×` and top-right maximize/restore. Non-terminal types are singleton per host: Git, Files, Agents, Plan, Activity, and each custom type. Adding an existing type focuses it. Child detail/history uses Agents, plan detail/history uses Plan, and tools/MCP use Activity; file buffers remain inside Files. Only Terminal repeats: New terminal creates independent instances alongside the center-bottom terminal. Tab switching, hide/show, and maximize/restore preserve server-owned shells; each terminal has its own input/resize controller. Right Terminal tab close ends its session; the bottom terminal has a separate close action, while pane toggles preserve sessions. See [terminal lifecycle](layout.md#terminal-close-and-panel-hide).
- Keep show/hide-left at top-left. Top-right order is exactly maximize/restore the right host's active surface, center-bottom show/hide, right show/hide, superseding the earlier two-icon arrangement. Maximize appears only for an actually visible right host, including its empty chooser, and is no longer in the surface header; expanded views preserve app chrome and the ADE configuration/usage footer, then restore the prior layout. Keyboard equivalents remain required with exact keys deferred. Closed (formerly Recents) is pinned above app settings, collapsed with a count by default; expanded it has a separate bounded list. Provide Close/Reopen and separate confirmed permanent Delete. Omit global content search; retain file search and the subsequently requested local thread-title search, searchable project filter, Add project and New thread controls. Settings and project gears follow the [settings contract](settings.md). Remember workspace layout and collapse right then left when narrow and bottom when short.
- Use coordinated dark/light themes with richer role colors, consistent icons, attachments, and image previews. Prefer compact components and fewer repetitive `v`/`>` markers. Omit repeated author headers on ordinary conversation messages, distinguishing roles through alignment and tint; retain meaningful tool/MCP labels and official agent SVG assets where appropriate, rasterized for terminal image rendering; do not invent Codex or user avatars. An actual user avatar is optional only when provided. Preserve clear focus/selection and no mandatory icon-font dependency. See [visual design](visual-design.md). The user approved the v3 direction; exact terminal geometry and interactive tuning remain prototype work.
- Use Codex's desktop app as the visual target, approaching its hierarchy, spacing, and polish within terminal-cell constraints. The user explicitly accepts square edges.
- Support simultaneous live file edits and labelled peer cursors, prompt autosave, and own-edit undo. Automatically merge clean external changes; preserve versions and pause autosave for overlap, deletion, or replacement until review.
- After server restart, restore state and queues but require explicit Resume by default before saved execution starts. The later General setting permits opt-in continuation for verified eligible integrations only; manual Stop and pending requests remain gated. Reattaching to the still-running server simply catches up.
- At thread creation choose an agent, then its supported model, effort, permissions, selectable context capacity, and speed tier. Always display selected and actual running values at the bottom of the prompt box. See [thread configuration](thread-configuration.md).
- Keep a fixed area immediately above the prompt, outside transcript scrolling, with one Agents summary, a compact Plan summary and pending requests. Show “Agents N”, where N is the total number of children in the represented group, including completed children, with a pulsing blue circle for established active work. The count stays visible as children finish and after all have completed; it is not the remaining working count. Only when every child has successfully completed, use a solid green circle; retain the summary until explicitly dismissed. On hover or keyboard focus, the completed circle becomes an X in the same leading icon slot. Only that slot dismisses; the label still opens history. Keep the working count and full state in hover/focus help and the inspector. Never offer dismissal while work is unfinished. Activating the summary opens the singleton Agents surface, where individual children and full available history remain accessible. Plan uses completed/total progress (n/N), the same blue working and solid green all-completed states, and the same completed-only dismissal. Failed, interrupted, waiting, stale and unknown states stay distinct and never imply success. Dismissal belongs to the frontend thread view; new work or a changed group/plan reopens its summary, while unrelated stream updates do not. See [activity](activity.md).
- Question sets use top tabs and conditional Back/Next to show one question at a time, preserve typed answers/selections, and Submit explicitly to the request rather than as a prompt. Blocking requests wait; supported asynchronous requests let work continue and deliver later answers. Requests remain durable/server-owned and reconcile multi-client answers; navigation, defaults, timeout, and disconnect never auto-answer. Preserve pending-request access in narrow/maximized layouts. Capability support is conditional, not guaranteed by base ACP v1. See [questions](questions.md).

- Show thread badges and a persistent clickable top-bar attention indicator for questions, approvals and failures in other threads. It remains available with navigation hidden; explicit activation opens the relevant item, and incoming events never steal focus.

- Use distinct approval cards in the fixed area above the prompt. Show the requested action, affected files or working directory where supplied, and the agent's supported choices; full details open in Activity. Preserve the prompt draft and do not infer broader permission grants.

- Support removable prompt attachments for files, selected lines, images, Git diffs and terminal output. Capture their content at Send and retain it unchanged for queued execution despite later source edits. See [prompt context](activity.md#prompt-context-attachments).

- Restore each thread's open surfaces, prompt draft and reading position when switching back. Files/Git follow that thread's checkout; existing terminals retain their shell sessions and working directories without restart or redirection. Workspace geometry preferences remain distinct from thread view restoration.

- Coordinate Git operations that rewrite files with live buffers: finish pending saves, pause disk autosave while Git runs, and reconcile before resuming. Save/reconciliation failures preserve work and stop for review. See [Git/buffer coordination](git-client.md#coordinating-git-with-live-buffers).

- Allow editing, removing and reordering queued prompts until execution starts, with controls directly in the visible queue. Reconcile changes through the server and preserve unchanged attachment captures. See [queue controls](activity.md#prompt-queue-controls).

- Capture each prompt's model, effort, permissions and applicable context/speed settings at Send. Queued items retain them unless explicitly edited; composer changes affect future submissions. Validate the recorded selection before dispatch and preserve it through recovery. See [per-prompt settings](thread-configuration.md#settings-captured-per-prompt).

- In tight layouts, keep compact Agents and Plan summaries and one scrollable question or approval card. Preserve the prompt, settings, usage and pending-request drafts; individual child history remains accessible in Agents. See [compact layout](layout.md#fixed-area-above-the-prompt).

## Quality questions to turn into acceptance criteria

“Perfect coding standards,” “super pretty,” and “100% mouse support” need observable definitions. The specification must ultimately describe interaction coverage, layout behavior, visual tokens, safe editing, Git operation boundaries, responsiveness, terminal compatibility, and the checks that establish each claim.

Terminal rendering cannot be assumed to reproduce a desktop UI pixel for pixel. Research must distinguish character-cell design, optional graphics, enhanced keyboard events, and pointer protocols. Any reduced-capability experience needs an explicit contract.

## Current evidence

The repository contains project skills, agent instructions, and a Bun bootstrap. There are no existing TUI components, application tests, terminal fixtures, or established implementation packages to preserve. The working tree already contained staged user files at the start of this session; this design work must preserve them.

The previous instructions mentioned D2R game behavior. That unrelated sentence has been replaced with guidance about verifying terminal and library behavior.

## Prototype and implementation decisions

The [interview record](interview.md) distinguishes accepted behavior from historical recommendations. Core scope is documented; exact bindings are explicitly deferred to an interactive prototype. Numeric geometry, package/version pins, protocol messages, collaboration algorithm, retention limits, additional Git/file actions, and provider setup are implementation decisions to resolve in the relevant slice. Do not silently treat a deferred option as an accepted feature.
