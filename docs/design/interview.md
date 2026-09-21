# Design interview

Date started: 2026-09-19.

Naming clarification: `my-app`, `myapp`, `~/.myapp/`, and `MYAPP_HOME` are illustrative placeholders. Each reference or consuming application supplies its own executable name, home directory, and environment-variable prefix.

Terminology update: the user subsequently selected **thread** as the user-facing term. Earlier question text is retained as interview history; active specifications use thread. ACP `session/*` names and references to upstream sessions retain their protocol meaning.

Method: ask each currently answerable decision in a round, with a recommendation. Research facts independently; do not ask the user to supply facts that can be looked up. Record answers as they arrive, then advance to decisions that depend on them. The requested deliverable is documentation; no application implementation is underway.

## Settled from the initial request

- This session is design and documentation work.
- The language targets are Go, Rust, and Bun; Go uses Charmbracelet Bubble Tea.
- The illustrative UI has hideable left navigation, a central workspace, and a right sidebar for surfaces.
- File browsing/editing, rendered/raw Markdown, a Git surface, and mouse/keyboard pane resizing belong in the design.
- Reuse beyond an agent development environment is required.
- SSH and tmux usability are explicit goals; the exact compatibility contract remains open.

## Round 1: repository purpose and platforms

### Q1 — What does the repository ultimately deliver?

Recommendation: one shared design/behavior specification and three idiomatic reference apps; extract reusable libraries only when demonstrated reuse warrants them. Alternatives: three library/framework packages, or a cookbook with examples only.

Answer: **accepted** — shared specification and three idiomatic reference apps, with reusable libraries extracted only when justified. See [ADR 0001](../adr/0001-shared-specification-idiomatic-reference-apps.md).

### Q2 — Which operating systems are required?

Recommendation: macOS and Linux initially, including remote Linux over SSH and tmux. Treat Windows as a separately tracked target until verified. Alternative: equal macOS, Linux, and Windows support from the outset.

Answer: **accepted** — macOS and Linux first. The user specified iTerm2 and Ghostty on macOS, modern terminals on Linux, and Windows Terminal as best effort only. Windows compatibility must not block the project. Exact Linux test representatives and recorded tool versions remain to be selected.

## Round 2: protocols and example scope

### Q3 — What does “kitty protocol” require?

Recommendation: preserve every core workflow over normal keyboard/mouse input locally, over SSH, and through tmux; negotiate kitty keyboard enhancements. Defer optional inline images to a later surface capability. Alternatives: include optional inline images in the initial design, or require enhancements even at the expense of tmux compatibility.

Answer: **accepted with expanded image scope** — the user is good with everything added. Include the portable keyboard/mouse core, negotiated kitty keyboard enhancements, and optional inline graphics in the initial design. Graphics remain an enhancement; core workflows remain available through SSH/tmux without images. Exact image surfaces and content limits remain downstream details.

### Q4 — What does the first ADE example own?

Recommendation: native structured conversation, messages, turns, tool activity, and review; future headless-provider adapters. An embedded interactive terminal is an optional later surface. Alternatives: host existing agent terminals, or support both equally from the start.

Answer: **accepted and specified** — the ADE will use headless Codex and Claude through Agent Client Protocol (ACP). Connect directly to native ACP agents and use an ACP adapter for others. The user cited Copilot as a possible native example and wants additional agents to fit the same boundary. Research must verify actual provider paths, reuse opportunities, and capability differences; protocol compatibility must not be described as universal feature parity. See [the ADE design](ade.md) and [ADR 0002](../adr/0002-acp-agent-boundary.md).

### Q5 — How deep is file editing?

Recommendation: a useful lightweight editor with selection, undo/redo, search, explicit save, dirty buffers, external-change protection, and rendered/raw Markdown. Alternatives: full IDE ambitions, or viewing plus external-editor handoff.

Answer: **accepted** — lightweight editor scope is sufficient for now. See [the editor design](editor.md).

### Q6 — How capable is Git?

Recommendation: everyday local Git—status, diffs, file/hunk/line staging, commits, history, and branch switching. Advanced rebase, stash/worktree management, conflict resolution, and network workflows require later explicit scope decisions. Alternatives: review only, or a full Sublime Merge-style client. Sublime Merge is the proposed interpretation of the user's Sublime reference.

Answer: **accepted and expanded** — include everyday local Git, a commit graph with right-click actions such as soft reset, fast-forward-only pull by default, and rebase. Provide manual conflict resolution and an option to delegate resolution to a connected agent, with a chooser when several are available. A divergent fast-forward-only pull must not silently turn into merge/rebase; those are separately selected operations. Agent continuation authority, the complete graph-menu inventory, and remaining remote operations require downstream decisions. See [the Git client design](git-client.md).

### Q7 — How flexible is layout?

Recommendation: three panes with independently hideable sidebars, mouse/keyboard resizing, and an expanded surface for editing/review. Alternative: arbitrary nested splits and docking from the outset.

Answer: **accepted with an addition** — three panes with hide/resize/expand, plus a bottom panel inside the center pane for a terminal or another surface. This is a fixed arrangement with a vertically divided center column, not arbitrary nested splitting. See [layout scope](layout.md). Initial open/closed state, sizing, and focus details remain downstream questions.

## Round 3: stacks, concurrent work, resolution authority, and ACP behavior

### Q8 — Which Rust and Bun UI stacks?

Recommendation: Ratatui + Crossterm for Rust; OpenTUI + React for Bun. OpenTUI + Solid is a viable preference-driven alternative. Native OpenTUI assets add release-packaging work. The Go direction remains Bubble Tea and the Charm ecosystem.

Answer: **accepted** — Ratatui for Rust, with Crossterm as the recommended backend; TypeScript + React + public Ink for Bun, plus `Bun.Terminal` for interactive subprocesses. The user selected React/Ink after clarification that Claude Code has custom rendering changes and no supported standalone Anthropic renderer was located. The earlier OpenTUI recommendation is not selected. Bun compatibility, full mouse interactions, and graphics remain implementation validation work; the choice does not establish parity with Claude Code's private renderer.

### Q9 — How do simultaneous agent tasks share a project?

Recommendation: an isolated Git worktree per task by default, with an explicit option to use the existing checkout. Non-Git folders use the existing directory with one active writer. Alternatives: existing checkout by default with optional isolation, or one active task per project initially.

Answer: **accepted with a different default** — use the existing checkout by default, with Git worktrees as an option. Shared-checkout scheduling, optional worktree lifecycle, and management of unrelated writers remain downstream details. See [workspace behavior](workspaces.md).

### Q10 — How far may an agent take conflict resolution?

Recommendation: edit the conflicted files and run relevant checks, then present the result for user review before staging resolutions and continuing the operation. Alternatives: review by default with an explicit automatic mode, or let the selected agent finish the operation.

Answer: **accepted: stop for review**. This concerns the “Resolve with agent” action for conflicts stopping a Git merge or rebase. The user first selected automatic continuation, then explicitly corrected the answer to “Stop for review.” The latest answer supersedes the earlier one: the selected agent may edit the conflicted files and run relevant checks, but must stop before staging resolutions or continuing the operation so the user can review. Do not preserve automatic continuation as an accepted default or silently add it as another mode.

### Q11 — What is the ACP version and adapter policy?

Recommendation: stable ACP v1, existing maintained adapters, negotiated optional features, and generic ACP configuration for additional agents. Build a custom adapter only for a demonstrated gap. Alternatives: track the ACP v2 draft immediately, or leave the policy open.

Answer: **accepted** — ACP v1 is the baseline. Existing maintained adapters remain the recommended integration candidates; exact packages, versions, installation ownership, and extensions still require implementation-time selection. Native ACP agents connect directly.

### Q12 — What happens when switching agents in a thread?

Recommendation: start a linked thread with selected context transferred; do not imply that ACP transfers a provider's private session state. A separate conflict-resolution job may use a different agent without switching the original thread. Alternatives: one visible thread with agent changes per turn, or defer switching and choose the agent only when creating a thread. Wording updated to the user's chosen term; the decision itself is unchanged.

Answer: **accepted: one agent per thread; handoffs deferred**. Keep the initial experience simple: a thread is with one chosen agent. Agent switching, linked-thread handoffs, handoff documents, and a seamless cross-agent conversation are future discussion, including handoffs within the same provider or to another ACP agent. Do not build handoff behavior into the initial scope. Choosing an agent for a separate conflict-resolution job does not change the original thread's agent.

## Additional requirements during round 3: thread usage

The user added these requirements before answering Q8–Q12. Later answers are recorded above; this section preserves the added requirements.

- **Accepted terminology:** use thread, rather than chat/conversation/session, for the user-facing interaction entity. Upstream protocol identifiers retain their literal names.
- **Required display:** current thread context usage and capacity. Keep the usage UI available even if some measurements have not been reported.
- **Conditional subscription display:** show the windows the harness supplies, such as 5-hour and 7-day allowances, with their usage/remaining values and reset information when supplied. Do not hard-code those two windows for every provider.
- **API billing:** omit subscription quotas; show a running thread cost when enough applicable data is supplied.
- **Source boundary:** use information available in the agent stream or forwarded by its adapter. No browsing or independent quota/pricing lookup for this addition. Provider-specific telemetry support has not been newly verified.
- **Truthful absence:** unknown, unsupported, stale, and not-applicable are distinct from zero usage, an empty context, or zero cost.

See [usage display](usage.md) for the measurement contract, normalization rules, and verification scenarios. Exact placement and field mappings are downstream design details. Q12 subsequently selected one agent per thread and deferred switching/handoffs.

## Additional requirements after round 3: activity and implementation order

- **Accepted:** output structured plan steps cleanly.
- **Accepted:** show subagent runs and allow opening their available full transcript and tool history. Delegated child work does not change the original thread's chosen agent or introduce handoffs.
- **Accepted:** present tool and MCP activity as readable items; clicking opens available full details in the right sidebar. Include the corresponding keyboard path.
- **Research requested:** inspect Codex's available rich-client presentation capabilities and identify relevant gaps in ACP/adapter delivery. See [native Codex research](../research/codex-presentation.md), [ACP activity research](../research/acp-activity.md), and the [activity design](activity.md).
- **Implementation ordering delegated to the assistant:** choose the easiest language first. The assistant selected **Go first**, using the established Charm framework/components; Rust requires more application assembly, and the selected Bun/Ink stack adds runtime/input/rendering validation. See [implementation sequence](implementation.md).

## Round 4: shared work, lifecycle, turns, layout, and theme

### Q13 — May multiple threads write to the same checkout at once?

Recommendation: one active writing thread or conflict-resolution job per checkout, with others queued; independent worktrees may run concurrently. External editors and the terminal still require stale-change detection. Alternatives: explicitly allow concurrent writers, or allow them by default.

Answer: **accepted** — queue writers per checkout. One active writing thread or conflict-resolution job owns the checkout; independent worktrees can run concurrently. External editor/terminal changes still require stale-state detection. Delegated subagents belong to their owning run, rather than acquiring a competing slot behind it.

### Q14 — Should agents continue after the TUI exits?

Recommendation: the TUI owns agent processes initially, using tmux to keep the whole application alive across SSH disconnects. Alternative: a detached background service and reconnect support from the outset.

Answer: **accepted with a different architecture** — `my-app` ensures a background server is running and attaches the TUI. Quitting the TUI leaves the server and its work running. `my-app server start` starts the server without opening a TUI; `my-app server status` reports its state; `my-app server stop` explicitly stops it. A TUI can attach any time and catch up. The server must be designed to support another frontend, including a future web UI. This supersedes the recommendation that the TUI own agent processes. See [server design](server.md) and [ADR 0003](../adr/0003-background-server-attachable-clients.md).

### Q15 — What happens to a new prompt during an active turn?

Recommendation: a visible prompt queue plus explicit interruption. A turn is one submitted prompt and its resulting agent work through completion, failure, or cancellation, with tool/subagent activity beneath it. Alternatives: steer the active turn where supported, or disable submission until completion.

Answer: **accepted** — visible prompt queue and explicit interruption. A turn is one submitted prompt and its resulting agent work through completion, failure, or cancellation, with tool/subagent activity beneath it. Queue manipulation and post-interrupt advancement details remain to be specified.

### Q16 — Which layout defaults?

Recommendation: left/right visible, bottom hidden; remember sizes and visibility per workspace. Collapse right then left at narrow widths and bottom at short heights. Activity selection reveals the right inspector. Expanded surfaces may use the full application and restore the prior arrangement. Alternatives: center-only startup, or require enough space to keep all panes visible.

Answer: **initially accepted; visible-right default later superseded** — the original answer selected left/right initially visible and bottom initially hidden, remembered workspace sizes/visibility, right-before-left narrow collapse, bottom collapse at short heights, activity inspection, and expansion/restore. Later visual-review corrections make the right sidebar an open-surface host: no opened surfaces means hidden, while explicitly showing an empty host displays its chooser. Left-visible and bottom-hidden defaults and responsive behavior remain. See the accepted correction below and [layout scope](layout.md). Numeric sizes and breakpoints remain visual-tuning details.

### Q17 — Which visual direction?

Recommendation: restrained desktop-style dark/light themes, muted pane backgrounds, sparse borders, one accent, clear focus/selection, and text labels with optional icons; no mandatory icon font. Alternatives: denser dark-only terminal styling, or visual mockups before choosing.

Answer: **initially accepted; later visual correction supersedes the one-accent limit** — dark/light themes, explicit focus/selection, labels, and no mandatory icon font remain. The user later rejected the first subdued mockups and requested richer color, icons/images, and clearer user/agent/tool separation using Omarchy, its plugins, and herdr as references. See [visual design](visual-design.md).

## Round 5: server scope, transport, recovery, and stopping

### Q18 — Does each language reference have a complete server and TUI?

Recommendation: complete Go, Rust, and Bun applications implementing the same client/server contract. Alternative: one Go server with three language-specific TUIs.

Answer: **accepted** — three complete apps, each with its own native server and TUI, implementing a shared client/server contract. Internal types and storage schemas need not be identical.

### Q19 — What does one server manage?

Recommendation: one server per user/application managing all projects and threads, with separate checkout writer queues. Alternative: one server per project.

Answer: **accepted** — one server per user/application manages all that application's projects and threads. Checkout writer queues remain separate. An explicitly overridden application home identifies an isolated state/server namespace.

### Q20 — How do clients connect initially?

Recommendation: authenticated local HTTP/WebSocket on loopback, with SSH forwarding for remote access and explicit future network exposure. Alternatives: Unix socket first with web support later, or authenticated network access from the outset.

Answer: **accepted** — authenticated loopback HTTP/WebSocket, with remote access through SSH forwarding. The application protocol is separate from the ACP boundary to upstream agents. Direct network exposure and a web frontend are later scope.

### Q21 — What survives a server restart?

Recommendation: persistent threads, received history/activity, queued prompts, drafts, and layouts; mark previously running work interrupted and require explicit resume rather than blindly rerunning commands/prompts. Alternatives: persist and automatically resume, or retain state only while the server stays alive.

Answer: **accepted storage direction; recovery authorization asked separately in Q27** — preserve machine/user-level state under the application's home directory, with an application-specific environment override and as much state as practical in SQLite. Necessary auxiliary files live under the same home. The user supplied `~/.myapp/` and `MYAPP_HOME` only as examples. See [storage](storage.md).

### Q22 — What does server stop do while busy?

Recommendation: gracefully cancel active work, record its latest state, shut down owned processes, and report completion; stopping is not rollback. Alternatives: refuse while busy unless forced, or wait for current work to finish.

Answer: **accepted** — gracefully cancel active work, record its latest state, stop owned processes, and report when shutdown completes. Modified workspace files remain; stopping is not rollback.

## Round 6: clients, input, files, change review, and recovery

### Q23 — May several clients attach simultaneously?

Recommendation: multiple clients share server state while focus, scroll, and selection stay local; each interactive terminal has one explicit input/resize controller. Alternative: only one interactive client.

Answer: **accepted** — multiple simultaneous clients, with one controller per terminal. Domain commands go through the server; one client's live focus does not move another client's focus.

### Q24 — Which command and pane bindings?

Recommendation: configurable Ctrl+Space prefix, F4 command-menu fallback, F6 pane focus cycle, and visible controls. Alternatives: command palette only, or choose bindings through a prototype.

Answer: **deferred by user to an interactive prototype** — no exact key bindings are selected. Keyboard completeness, remapping, discoverable fallback access, and equivalent mouse actions remain requirements.

### Q25 — Which editor opening and file-management conventions?

Recommendation: single-click preview, double-click pin, edits pin, Markdown renders the current buffer, explicit save, dirty-close Save/Discard/Cancel, and file search/create/rename/delete with external-change protection. Alternatives: browse/edit only or leave conventions open.

Answer: **expanded by the user** — edits and cursor presence must synchronize in real time across connected clients, initially two clients on the same machine with future cross-machine use in mind. The user requested real-time saving; Q28/Q29 settle simultaneous editing, autosave, and external changes. The remaining proposed preview/pinning and file-management conventions were not expressly accepted and remain prototype details.

### Q26 — What does a turn's Changes view mean?

Recommendation: recorded start/end comparison labelled changes observed during this turn, alongside provider-reported edits where available. Current/staged/branch views remain separate; external edits can contribute, and rollback is deferred. Alternatives: provider diffs only or current Git changes only.

Answer: **accepted** — recorded start/end turn comparison, clearly labelled changes observed during this turn, with available provider edits separately identified. Current/staged/branch diffs remain separate, external edits can contribute, and rollback is deferred.

### Q27 — What may start after a server restart?

Recommendation: restore history and queues, mark unfinished work interrupted, and require explicit Resume before saved work starts. Reattaching to a running server only catches up and does not pause work. Alternative: automatically start queued work while interrupted runs wait.

Answer: **accepted** — after server restart, restore preserved state, mark unfinished work interrupted, and wait for explicit Resume before starting saved work. Attaching to a running server only catches up; it does not interrupt or pause work.

## Round 7: live editing and external changes

### Q28 — May clients edit a file simultaneously?

Recommendation: simultaneous editing with live edits and labelled peer cursors; server autosave with Saving/Saved/error states; undo affects the initiating client's edits without erasing peers' work. Alternative: one editor at a time with other clients watching.

Answer: **accepted** — simultaneous editing and autosave. This supersedes explicit Save as the normal editor workflow. The collaboration algorithm and exact timing remain implementation/prototype work.

### Q29 — How are changes outside the collaborative editor handled?

Recommendation: merge clean external changes automatically; on overlap, deletion, or replacement preserve both versions, pause autosave, and open resolution. Alternative: pause for review on every external change.

Answer: **accepted** — merge clean changes and review conflicts. Agent, terminal, and external-editor changes cannot be silently overwritten by a later autosave. See [editor behavior](editor.md).

## Additional requirements: configuration and the prompt box

- **Accepted:** thread creation chooses an agent first, then model, effort, permissions, and any agent-supported selectable context capacity and speed tier.
- **Accepted:** always show selected and actual running settings at the bottom of the prompt box, throughout the thread. Distinguish them when they differ, with mouse/keyboard access. The originally accepted narrow-layout wrapping is superseded by the 2026-09-20 responsive footer correction below.
- **Examples only:** `1m`/`200k` context and `2x` speed are not universal options. Show what the chosen agent supports and actually acknowledges.
- **Evidence boundary:** [ACP configuration research](../research/acp-configuration.md) documents generic options and current adapter mappings. Available controls are not universal guarantees, and runtime validation remains unperformed.

See [thread configuration](thread-configuration.md). The initial required flow is thread creation; changing settings mid-thread, exact presets, and the interactive bindings remain implementation/prototype details.

## Additional visual clarification

The user explicitly wants the TUI as close as practical to Codex's desktop app in visual polish, accepting square terminal-cell edges. This strengthens the existing dark/light direction: prioritize composition, spacing, hierarchy, calm backgrounds, and readable activity rather than assuming terminal limitations justify an unpolished interface. See [visual design](visual-design.md).

## Visual revision after the first mockups

The user rejected the initial renderings as insufficiently pretty, asking for more color, icons/images using kitty graphics, and clearer separation between user messages, agent responses, and tool use. Omarchy, herdr, and the Omarchy plugin gallery are explicit additional visual references. This supersedes the earlier one-accent constraint while retaining dark/light support, terminal usability, and the accepted shell arrangement.

Revision 2 proposed distinct role surfaces, icons and raster avatars, attachment/image-file previews, grouped tools, identifiable subagents, richer Git colors, and persistent prompt settings. Its invented agent/user avatars were subsequently rejected. Generating mockups did not establish approval or terminal compatibility.

## Accepted layout corrections for revision 3

The user corrected behavior and identity presentation after reviewing the mockups:

- Initially requested a persistent show/hide-left icon at top-left and center-bottom/right show/hide icons at top-right. The later tab/maximize correction below supersedes this two-icon top-right arrangement. Keyboard equivalents remain required; exact keys remain deferred.
- Remove global/sidebar search while retaining file search. Recents must be collapsible and hideable, with a way to restore it without deleting its entries.
- Treat the right sidebar as a host for opened surfaces only, with Add surface opening a chooser. No opened surfaces means hidden by default; explicitly showing an empty host displays the chooser. Hiding preserves surfaces, showing an occupied host restores them, and closing the last surface hides it.
- Activity details may open/reuse an inspector and reveal the host without discarding documents or other surfaces. This replaces Q16's unconditional initially visible right sidebar, not the existing document-preservation requirement.
- Make components cleaner and more compact, use consistent icons, and reduce repetitive `v`/`>` markers. Do not invent Codex or user avatars: retain name labels, use official agent SVG assets where appropriate and rasterize them for terminal images, and include an actual user avatar only if supplied.

These behavior corrections are accepted. Revision 3's exact colors, spacing, icon treatment, and rendered composition remain a visual proposal for review. No runtime or terminal compatibility is established by the mockup. See [layout](layout.md) and [visual design](visual-design.md).

The user additionally requested inspection of the local T3 Code app as a design reference. [The recorded source review](../research/t3-code-design.md) covers its stable pane toggles, opened-surface/empty-launcher transitions, compact activity, composer controls and SVG agent assets. This provides implementation evidence for the desired patterns without importing T3's unrelated features, search, bindings or compact-setting omissions. The revision 3 references incorporate its calmer hierarchy and inset user-message alignment. No app was launched or changed in that checkout.

In subsequent feedback, the user found the activity inspector icon confusingly similar to the right-sidebar toggle and rejected redundant explanatory UI copy, specifically “Running with selected settings.” Remove repeated inspector icons in favor of full-row activation with hover/focus feedback. Show matching settings once without a heading; retain labels when selected and running values differ or a status affects user action. Remove layout explanations and mockup disclaimers from inside the depicted UI; keep provenance in the surrounding documentation.

## Accepted dynamic tabs, terminals, and maximize correction

The user subsequently clarified that the right host needs explicit dynamic tabs for Git, Files, Terminal, inspectors, and other supported surfaces. Show only opened instances, each with its own `×` close control. Add surface remains. Repeated New terminal creates independent instances, for example Terminal 1 and Terminal 2, in addition to the existing center-bottom terminal. This settles tabs and multi-terminal support rather than leaving them as optional presentation ideas.

The top-right order is exactly: maximize/restore the right host's active surface, center-bottom show/hide, right show/hide. This supersedes the earlier two-icon rule and removes maximize from the surface header. Expansion retains application chrome and the required ADE composer configuration/usage footer; restoration returns to the previous layout. Exact bindings remain deferred.

Existing hide-versus-close, explicit-empty-chooser, and last-close-hidden rules remain. Switching tabs, hiding, or maximizing/restoring does not start/restart a shell or change server PTY ownership. Each terminal has its own single input/resize controller. The distinction between closing a live terminal tab and explicitly terminating its process must be documented before implementation; cancellation on hide is not implied. See [layout](layout.md) and [quality](quality.md). Updated tabbed, multi-terminal, and maximized illustrations remain visual proposals, not runtime evidence.

## Accepted surface cardinality clarification

The user clarified that the same tab `×` and top-right maximize/restore must be available for every surface type. Within a right-panel host, Terminal is the only repeatable type. Each non-terminal type has at most one tab; adding an already-open type focuses it. The initial combined Activity-inspector interpretation is superseded by the separate Agents/Plan/Activity types below. Multiple file buffers inside Files do not imply multiple Files host tabs. This settles non-terminal reuse previously left to the prototype; appearance and exact bindings remain prototype work.

## Accepted fixed activity area and user questions

The user requires a fixed area immediately above the prompt and outside transcript scrolling, supplementing rather than replacing the usable composer. The original decision showed only actively running children individually and removed finished children; the later grouped-summary correction below supersedes those presentation choices. Retain the compact current plan; stale or unavailable status must not be labelled running. Agents and Plan are separate singleton right surfaces with corresponding detail and retained history; tools/MCP retain the singleton Activity surface. Only Terminal repeats.

Pending questions also appear above the composer. Multi-question sets use tabs or Back/Next, one question at a time, preserving typed answers/selections. Submit is explicit and separate from navigation; answers route to the request, not as new prompts. Blocking questions wait on the requesting run; supported asynchronous questions let it continue and receive a later answer. Use concise “Waiting for answer” versus “Answer anytime” only when meaningful.

Requests are durable and server-owned, with multi-client answer reconciliation. Navigation, defaults, timeout, or disconnect cannot auto-answer. Async delivery depends on provider capability; base ACP v1 is not a universal guarantee. Pending requests stay visible/accessibly actionable in narrow or maximized views. These behaviors are accepted; precise styling/tokens remain proposals and runtime validation is unperformed. See [activity](activity.md), [layout](layout.md), and [questions](questions.md).

## Research status

- [Go ecosystem and terminal capabilities](../research/terminal-capabilities.md): source research complete. No terminal execution checks performed.
- [Rust and Bun libraries](../research/language-stacks.md): directions selected in Q8; runtime verification remains pending. [Claude Code UI lineage](../research/claude-code-ui.md) and [Bun.Terminal](../research/bun-terminal.md) document the revised Bun direction.
- [Herdr and Git/editor precedents](../research/interaction-precedents.md): source research complete; proposed interactions pending decision.
- [Local T3 Code design](../research/t3-code-design.md): source and bundled-screenshot inspection complete at the recorded revision; live behavior remains untested.
- [ACP protocol](../research/acp-protocol.md) and [agent/adapter availability](../research/acp-agents.md): source research complete; no agents installed or executed. Stable v1, optional capabilities, existing adapter paths, and draft boundaries are documented.
- [Git operation semantics](../research/git-operations.md): source research complete; no repository mutations or remote Git operations performed.
- [Native Codex presentation](../research/codex-presentation.md) and [ACP activity](../research/acp-activity.md): source research complete; pin and validate adapter/extension dialects before claiming complete child history support.

## Gap review: background attention

After approving the latest visual direction, the user requested that remaining gap questions be asked **one at a time**. This overrides the skill's default of asking the whole available frontier together.

**Accepted:** when another thread needs an answer or approval, or fails, show thread badges plus a clickable attention indicator in the top bar. The indicator remains available when navigation is hidden. Explicit activation opens the relevant item; incoming events never steal focus. This does not add desktop notifications or settle the remaining gap-review recommendations. See [layout behavior](layout.md).

## Gap review: approval cards

**Accepted:** permission approvals use the same area above the prompt as questions, with a distinct approval card. Show the requested action, affected files or working directory, and choices supported by the requesting agent, such as Allow once or Deny. Full details open in Activity. Responding leaves the normal prompt draft intact. The user accepted the recommendation; remaining gap questions continue one at a time. See [approval behavior](activity.md#approval-cards).

## Gap review: prompt context attachments

**Accepted:** prompts support removable files, selected lines, images, Git diffs and terminal-output attachments. Capture their content when the user presses Send. A queued prompt keeps that exact attached content even if the underlying source changes before execution. The user answered yes to this recommendation. This does not settle queued-prompt editing or configuration capture; remaining gap questions continue one at a time. See [prompt context](activity.md#prompt-context-attachments).

## Gap review: thread and project switching

**Accepted:** switching threads restores each thread's open surfaces, prompt draft and reading position. Files and Git follow that thread's checkout. Existing terminals retain their own shell sessions and working directories; switching threads never restarts or redirects them. The user answered yes to the recommendation. This settles view restoration while preserving frontend-local navigation and the separate terminal controller contract. Terminal-tab close was the next gap question and is settled by the correction below. See [thread restoration](layout.md#switching-threads-and-projects).

## Gap review: terminal close versus hide

**User correction:** right-panel Terminal tab × should end that terminal session, following Codex/T3-style behavior. The center-bottom panel toggle only hides and preserves its terminal; the bottom terminal must also have an explicit session-close action. This rejects the assistant's detach-only close recommendation. Hiding the right host likewise preserves its sessions. Close affects that server session, while ordinary navigation continues to preserve surviving sessions. Source inspection is recorded in [terminal lifecycle research](../research/terminal-lifecycle.md); the [layout contract](layout.md#terminal-close-and-panel-hide) now settles this previously open behavior.

## Gap review: Git operations and autosave

**Accepted:** before Git operations that rewrite files, finish pending saves, pause disk autosave during the operation, then reconcile open buffers before autosave resumes. If saving or reconciliation fails, preserve the edits and stop for review. The user answered yes to the recommendation. This adds coordination to the existing writer queue and external-change contract; it does not authorize automatic stashing, staging, commits or discarded edits. See [Git/buffer coordination](git-client.md#coordinating-git-with-live-buffers).

## Gap review: queued-prompt controls

**Accepted:** queued prompts can be edited, removed and reordered until they start running. These controls appear directly in the visible prompt queue. The user answered yes to the recommendation. This settles queue manipulation left open in Q15; settings capture and post-interrupt advancement remain separate decisions. See [queue controls](activity.md#prompt-queue-controls) and [server scheduling](server.md#scheduling-and-unattended-requests).

## Gap review: settings captured for queued prompts

**Accepted:** each queued prompt retains the model, effort, permissions, context size and speed selected at Send unless the user explicitly changes that queued item's settings. Changing composer settings affects future submissions; already queued prompts keep their recorded settings. The user answered yes to the recommendation. This settles configuration capture separately from attachment capture and queue controls. See [per-prompt settings](thread-configuration.md#settings-captured-per-prompt).

## Gap review: crowded and small layouts

**Historical accepted choice, superseded by the grouped-summary correction below:** use one compact plan row, a running-agent row with a More menu, and one scrollable question or approval card at a time. Keep the prompt, settings and usage visible. The user answered yes to the recommendation. Numeric geometry, minimum dimensions and exact focus/input behavior remain assigned to the interactive prototype. See [compact layout](layout.md#fixed-area-above-the-prompt).

The eight areas raised in this gap review now have recorded behavior decisions; queue controls and settings capture were asked separately. The user requested one question at a time, and terminal close was corrected to end the session. The remaining work below is scoped to prototype or implementation decisions and verification, not additional silently accepted product scope.

## Implementation and prototype follow-ups

Q1–Q29 have answers, including the user’s explicit deferral of bindings to an interactive prototype. Do not reopen settled scope merely because implementation details remain. The following is the proposed staging of remaining work, not a claim that the user selected every possible feature or numerical default.

| Area | Follow-up before the relevant implementation slice |
| --- | --- |
| Packages and distribution | Choose directories, pinned toolchains/libraries/adapters, packaging, and executable/home names; validate Bun/Ink and embedded-terminal compatibility. |
| Terminal prototype | Verify exact local/SSH/tmux versions and capabilities; choose bindings with the user, numeric geometry, focus/modal/selection behavior, and reduced-capability presentation. |
| Files and collaboration | Choose and validate the shared-document algorithm, per-client undo, revision protocol, autosave timing, opening/tab conventions, file-management inventory, encodings, and content limits. |
| Git and worktrees | Specify optional worktree creation/cleanup, additional graph/remote actions, dirty-work handling, review capture coverage, and detailed cancellation/recovery. Accepted ff-only Pull and stop-for-review resolution do not change. |
| Agent configuration | Pin capabilities and adapter dialects, define installation/authentication, and validate effective model/effort/permission/context/speed controls, including per-prompt settings captured at Send and explicitly edited queued items. Mutating an executing run remains separate scope. |
| Server and protocol | Define wire schemas/versioning, authenticated discovery, revision and request reconciliation, terminal-controller transfer, queue fairness, and shutdown failure reporting. Core lifecycle and transport are settled. |
| Storage | Define schema/migrations, artifact publication, retention/budgets, backup/recovery tooling, and workspace identity; preserve explicit Resume and conflict review semantics. |
| Quality | Configure actual language tooling, shared fixtures, live provider checks, measured performance budgets, and visual/interaction evidence. No runtime result is implied by the specification. |

## Documentation status

The accepted answers define the documented blueprint. Remaining implementation choices are labelled and must be resolved in the corresponding slice. Research recommendations are not automatically accepted decisions, and this record does not authorize starting application implementation or claim it has begun.

## First implementation review: controls, composer and scrolling

**User-requested corrections (2026-09-19):** replace unfamiliar control symbols with recognizable font icons, assuming a good Nerd Font; distinguish open/closed pane states; use T3/Codex-style icon-plus-name tabs whose icon becomes the sole close target on hover; show the tab overflow control only for hidden tabs and one row per surface. Enter sends from the prompt composer, Shift+Enter creates a newline, and the input grows to a bounded height (the user suggested five or eight lines) before scrolling. Scrollable regions need scrollbars. The implementation selects an eight-line maximum with a smaller cap in short windows, plus Ctrl+J and explicit plain-font fallbacks. These concrete corrections supersede the initial prototype's Enter/newline binding and always-visible Tabs control; they do not reopen the interview or change surface/session lifecycle.

## First implementation review: pasted context and previews

**User-requested behavior (2026-09-19):** paste copied content, especially images and files, into the composer as context attachments. Show a thumbnail where possible. Clicking an item opens a centered floating preview with expand and restore. File previews must always be view-only, never editable. Markdown has raw and rendered preview modes, with an eyeball icon inspired by T3 Code. This extends the existing captured-context requirement; it does not replace the collaborative Files editor with a read-only editor. When asked separately, the user confirmed: ordinary text goes into the prompt; images and copied files become attachments. See [the attachment specification](activity.md#clipboard-intake-and-read-only-previews).

The user initially questioned small icons in the assistant's static captures, then withdrew that concern after running the CLI and reporting that it looked excellent. Preserve the live icon treatment. Deterministic captures have fixed raster font/cell metrics and are not evidence that the native terminal needs a font-size change.

## First implementation review: graceful capability failures

**Accepted (2026-09-19):** terminal-dependent actions that are unavailable should fail gracefully with a brief alert that disappears, explaining that the action cannot work in the current environment. This applies to any unsupported terminal feature, including image/clipboard limitations through SSH and tmux, rather than only the two named macOS terminals. Use non-blocking notices, preserve user work and provide a useful fallback where possible. See [unavailable terminal features](layout.md#unavailable-terminal-features). This is an accepted interaction requirement; detection and runtime behavior still require implementation and validation.


## First implementation review: grouped activity and composer status

**Accepted correction (2026-09-19):** replace individual running-child chips and their More menu with one “Agents working N” summary and a pulsing blue circle. Once every child successfully completes, retain “Agents finished” with a solid green circle until the user explicitly dismisses it using an X visible on hover or keyboard focus. Never dismiss unfinished work. Activation opens Agents with individual histories intact. Plan uses n/N progress with analogous working, all-completed and completed-only dismissal behavior. Both summaries share a tinted band and consistent text colors; failure, interruption and waiting must not imply success. This supersedes the earlier running-only removal and compact-overflow choices.

Ordinary conversation messages omit repetitive You/Agent headings, using alignment and tint instead; meaningful tool/MCP/detail labels remain. Show Thinking for reported running turns, Waiting distinctly, and clear running status on terminal outcomes without inferring private reasoning. Stop is the UI label for interrupt/cancel and remains available during active waiting. Send is blue and far right; active Stop is red directly left, then paperclip and usage gauge to their left. Maximize appears only when the right host is actually visible; a visible empty chooser uses the same expanded geometry and preserved composer footer.

Clean agent/model names, optional Fast, pretty effort names and Claude reasoning/context are capability-driven display requirements, not authorization to add integrations or invent supported settings. The fixture uses Demo / Reference / Medium / Simulated labels and unknown context. See [configuration](thread-configuration.md), [activity](activity.md) and [layout](layout.md).

## Interactive polish: notifications, activity and questions

**Accepted correction (2026-09-20):** Attention is a bell with a numbered badge,
neutral with no badge at zero and yellow/orange for pending items. Pack it beside
the visible right-side controls; hidden maximize leaves no gap. Recents is a
navigation section heading. The Agents label is always simply Agents; the dot
and animation communicate state. Completed Agents and Plan replace the leading
green circle with X on hover/focus, with only that icon dismissing and the text
still opening details. These corrections supersede the working/finished labels
and separate close slot from the previous review.

Questions use an outlined, height-bounded card with top question tabs, vertical
radio/checkbox options, supported Other/free-fill and open-ended text. A radio
selection advances if another question exists, never submits. Checkbox toggles
stay on the page; Other focuses its text before advancing. Back/Next exist only
for real adjacent questions and do not wrap. Overflow controls appear only when
needed. The multiple-request selector is distinct from question navigation and
hidden for a single request. Explicit Submit, preserved prompt drafts and
server-owned request identity/revision continue to apply. See
[question behavior](questions.md).

## Interactive review: thread lifecycle and projects — 2026-09-20

The user requests Close/Reopen for retained threads, renames Recents to Closed,
adds an ellipsis menu with permanent Delete and asks for hover check/trash actions.
Delete fully removes the thread from live database records. T3 Code's
settle/unsettle design is a reference; user-facing terms here remain Close/Reopen.
The user also requests better Add project and project selection/filtering, with
T3 Code as the reference. This supersedes the earlier omission of sidebar search
only for the project selector's name/path search; global content search stays absent.

The prototype uses existing server-folder path entry, a per-client All projects
or single-project filter and empty Demo thread creation. Folder browsing/native
pickers and real provider selection remain later work. A single Cancel-first
delete confirmation and inactive-only Close are implementation defaults; the latter
was raised as an optional question and implemented as the stated T3-style assumption.
See [projects](projects.md) and [thread lifecycle](threads.md) for behavior,
[the pinned T3 source inspection](../research/t3-code-design.md#project-selection-and-thread-lifecycle-inspection--2026-09-20)
for evidence, and [ADR 0007](../adr/0007-thread-deletion-and-view-projection.md)
for deletion/retry tradeoffs.

### Navigation thread status refinement — 2026-09-20

The user requests pulsing blue circles for working threads, yellow/orange when
something needs attention and green when finished. Only finished thread hover
reveals the Close check; Closed hover continues to reveal trash. Thread options
use a vertical ellipsis like T3 Code. A follow-up explicitly adds red circles for
issues such as API errors. Errors take precedence over attention, then working.
The current fixture's idle state includes finished work and empty quiescent threads;
unknown/disconnected state stays neutral. Reuse the existing animation clock for
visible background threads, with no persisted animation updates.

## Responsive footer correction (2026-09-20)

The user requests progressive removal of controls into a vertical ellipsis as
space narrows. This supersedes wrapping the configuration strip: keep selected
settings left and Send/actions right on one inset row. Hidden settings and
usage stay accessible in the overflow menu and return as space widens. Preserve
the existing centered close targets. See [composer behavior](layout.md#controls-and-composer-refinement-2026-09-19)
and the [implementation review](../research/go-footer-overflow-2026-09-20.md).

## Composer grouping and outline correction (2026-09-20)

The user clarifies that settings overflow belongs directly beside the remaining
left-aligned fields. Usage needs its own ellipsis at the far left of the
right-aligned group, opening full context percentage, cost and related details.
Stop and Send retain highest priority. The user also requests an outlined typing
area with a less subtle separation from the settings/actions below it. These
are presentation corrections; measurements still require supplied telemetry.
See [composer behavior](layout.md#controls-and-composer-refinement-2026-09-19).

## Question navigation buttons (2026-09-20)

The user requests filled arrow icons for Back/Next, reserving their slots when
hidden so question IDs cannot fill them. Question tabs should look like buttons,
using a distinct background or container. When a batch exceeds the available
width, the current question must stay visible and Back/Next must reveal the
adjacent questions. Apply similar treatment to other controls where it improves
recognition. This refinement retains explicit Submit and preserved drafts; see
[question behavior](questions.md#placement-and-navigation).

## Compose icon and contained thread rows (2026-09-20)

The user requests replacing `+ New thread` with an icon following T3 Code and
making sidebar threads feel contained rather than like separate text lines.
Use its square-and-pencil compose treatment and grouped row surfaces, translated
to a compact padded terminal card containing title and metadata. Preserve the
accepted status circles, hover actions and vertical options. See [thread cards](threads.md#open-and-closed-navigation)
and [New thread](projects.md#create-a-thread-in-a-project).

The user subsequently asks for more rounded corners, citing the prompt box, and
extends the request to buttons and tabs where appropriate. Thread cards and the
question/approval container use rounded outlines. Question navigation/actions
and surface tabs use compact, single-row pills; pane-toggle chrome stays unboxed.
Round caps must preserve the separate close icon, reserved arrow slots and
keyboard paths. Limited-color/plain layouts keep equal-width readable fallbacks.

The next correction asks for less rounding, using the prompt border as the
reference, and for thread background colors to stay inside their borders. Replace
the solid pill silhouette with thin outlined ends on compact one-row controls.
Thread cards retain their prompt-style corners but fill only their interior;
hover/selected backgrounds must not paint square border cells.

The user rejects the outlined-end result. That was an implementation
misinterpretation, not an accepted design decision. Restore the preceding filled
buttons/tabs and retain the requested interior-only thread background fix. The
request for a smaller visual corner radius remains unresolved; do not claim the
outlined ends satisfied it or reintroduce them as an accepted specification.

The user's latest clarification requires the prompt outline's small rounded
corners with background fill contained inside the border. This supersedes both
cap experiments above: the thin-end substitution was an implementation error,
not a user decision. The Go implementation now uses actual top/side/bottom borders
for normal-height buttons and tabs. Three rows per control strip, with a one-row
rectangular fallback below 28 terminal rows, is an implementation tradeoff rather
than an explicitly requested height. Thread-card dimensions remain unchanged;
only interior cells receive their fill. See the [current visual specification](visual-design.md#pane-controls-and-surface-chrome).

The latest clarification explicitly asks for two types: an outlined and filled
surface, and a filled surface with the same shape but no visible border line.
The user confirms the proposed split: thread cards and the prompt use the outlined
style; buttons and tabs use the background-only style. Their small rounded
silhouette remains the intended appearance. The current text implementation uses
stepped half/quadrant-block corners for the latter; the user has not approved that
approximation as equivalent to a smooth curve. The outer question/approval card
remains outlined as a routine hierarchy choice.

The user next suggests returning to borders, using different shades for rest,
hover and selection, and asks for a reusable component-building rule and possibly
an ADR once the approach is settled. The recommendation is stable backgrounds,
neutral rest/hover borders, an accent selected border, and independent text cues
for selection and keyboard focus. This remains a proposal, not a confirmed change
to the preceding split. See [components](components.md) and
[proposed ADR 0008](../adr/0008-cell-native-component-state.md).

The user clarifies that the border-shade approach applies to rounded components.
Square components may be outlined or borderless with a background, with the latter
preferred for a tighter fit; question tabs are suggested as a candidate. The
recommendation is rounded outlines for grouping/input cards and square fills for
question tabs, surface tabs and compact actions. Square outlines remain available
when a compact element needs an explicit boundary. Placement and exact state
mapping remain under review in the component proposal; no runtime change has been
made by this documentation pass.


## Accepted component construction, 2026-09-20

The user approved implementing the [component rule](components.md) and
[ADR 0008](../adr/0008-cell-native-component-state.md). This settles the preceding
review and supersedes the earlier pill, curved-end and stepped-corner treatments.
Prompt, thread, request and dialog containers use rounded outlines with stable
interior backgrounds. Question tabs, surface tabs and compact actions use
single-row square fills; square outlines remain available. Rest is neutral,
hover stronger neutral, selection accent plus bold, and keyboard focus separately
underlined. Selection remains visible when a neighboring control is hovered;
semantic status icons retain their colors. The implementation and its evidence
are separate from this acceptance record.

The user subsequently requests a container around queued messages: the queue
header, previews and Edit/Remove/reorder controls currently blend together and
into surrounding content. Apply the accepted rounded-container rule to the queue
group, with inset, aligned rows and bounded access to additional queued items.
This changes grouping, not queue execution, settings captures or attachment
preservation.

## Queued-message steering, 2026-09-20

The user requests a **Steer** button on queued messages so a selected message
can join the current conversation immediately instead of waiting for another
turn, and explicitly requires this in the overall design. Ordinary Send continues
to queue during active work. Steer targets the same active turn, preserving the
queued payload and settings, without implicit Stop/Resume, reordering or a new
turn. It is conditional on verified adapter support and must have an honest
unavailable state. The [shared steering contract](activity.md#steering-a-queued-message)
defines race, acknowledgment and recovery behavior for all three native apps.

## Answered questions in conversation history, 2026-09-20

The user asks whether answered questions are displayed nicely in the conversation.
Inspection confirms that Go currently saves the answers and removes the pending
form, but does not render the Q&A in its transcript. The user approves adding the
proposed compact, read-only **Answered** card to the design: show each original
question with its submitted answer, and allow expansion of longer content.
The [shared history contract](questions.md#answered-questions-in-conversation-history)
records presentation, accepted-response identity and recovery requirements.
Implementation remains outstanding; this follow-up changes documentation only.

## Agents summary total, 2026-09-20

The user clarifies that the compact summary must read **circle · Agents N**,
where N is the total children in the represented group, including completed
children. With 13 children and 12 completed, the circle still pulses blue and the
label stays **Agents 13**; after the last completes, the circle becomes solid
green and the label remains **Agents 13**. This adds the count to the earlier
“just Agents” label decision; it does not restore working/finished wording.
Existing exceptional-state colors and completed-only icon dismissal still apply.

## Phone-sized terminal and column selection, 2026-09-20

The user reports an iPhone terminal at **47×22** and requests support below the
former 48-column minimum. The user proposes choosing the visible column with a
hamburger menu and asks for a recommendation. The implementation uses one column
at a time below 60 columns and validates a 40×22 minimum. The prompt and actions
remain available; Conversation owns questions, queued messages and activity,
while the bell reveals pending questions from another column. Column selection
preserves work and restores the wider pane arrangement after a resize. See the
[small-screen contract](layout.md#single-column-layouts-on-small-screens).

## Thread lifecycle action placement, 2026-09-20

The user moves the Close checkmark and Closed-thread trash action directly left
of each row's vertical ellipsis. This supersedes the earlier leading-circle swap.
Keep the status circle on the left and reserve the trailing action slot so titles
do not shift on hover/focus. Close eligibility and explicit Delete confirmation
remain unchanged. See [thread presentation](threads.md#open-and-closed-navigation).

### Sidebar/settings refinement — 2026-09-20

User approved the updated local T3 reference's simplified search/filter/add/new
header, colored project badges, per-project gear/settings, bottom app settings
with General/Appearance/Keybindings/About and bottom Back navigation. Keep this
app's **Open/Closed** terminology, not Settled. Closed stays at the bottom with
count only while collapsed. App workspace default is Current checkout, project
overrides are supported, and Continue threads after restart is off by default.

Asked whether removing a project should also delete its threads while preserving
disk files. User: “Remove project and its threads after confirmation.” This is
now the accepted removal contract. No real worktree/protocol support is inferred
from storing defaults; see [settings](settings.md) and [projects](projects.md).

The user then clarified the settings layout: **the left sidebar is category
navigation, and the center/right panels are fully replaced by the selected
category's configuration**, as in T3. The Project category appears when entering
through a project's gear. The composer and bottom workspace are hidden too,
with their state preserved for Back. This supersedes the intermediate
implementation that placed the settings form inside the left sidebar.
