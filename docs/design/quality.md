# Shared coding and verification contract

Recorded: 2026-09-19. Applies to all three complete reference applications: Go first, Rust/Ratatui, and React/Ink/Bun, each with its native server and TUI. This document lists acceptance targets, not a completed test report. See [the Go slice](go-slice.md) for implemented scope and [bounded feasibility evidence](../research/go-feasibility-2026-09-19.md) for executed probes; unrecorded conformance checks remain **NOT RUN**. This contract develops [AGENTS.md](../../AGENTS.md); detailed product behavior remains in the linked specifications. A checklist describes required evidence, not completed validation.

## Implementation discipline

Keep presentation state, domain transitions, and effects distinct. Rendering must not write files, mutate Git, launch processes, or submit agent work. Route keyboard and pointer activation through the same typed commands. Represent waiting, running, cancellation requested, completed, and failed outcomes explicitly; dispatching cancellation is not proof of cancellation.

The [server](server.md) owns authoritative execution and preserved state. Clients own live focus, hover, scroll, and selection; shared peer-cursor presence does not transfer focus ownership. Avoid coupling work to frontend lifetimes or reading terminal stdin from provider adapters. Keep ACP framing separate from PTY bytes; a terminal emulator belongs to its bounded surface. Provider translation must not leak into generic layout components.

Use explicit ownership and cancellation for asynchronous tasks, bounded queues/output, and observable error propagation. Separate recoverable failures from invariant violations. Preserve operation identities across retries. Reject stale completion events rather than applying them to whichever workspace is currently visible. Cache and virtualize expensive presentation work without making caches authoritative.

Use grapheme-aware editing and cell-aware layout. Sanitize untrusted content at its rendering boundary; subprocess terminal sequences require deliberate emulation, not unrestricted forwarding into the host. Keep themes semantic and centralized. Document a dependency's benefit, cost, maintenance, and distribution impact before adding it; pin reproducible toolchains and selected dependencies.

## Shared conformance cases

Maintain language-neutral scenarios with initial state, capabilities, user actions, injected failures, and observable outcomes. Each implementation runs equivalent cases through its own idiomatic test harness. Share fixtures and assertions about behavior, not private object structures. The following groups remain required targets; passing a bounded first-slice test does not complete a group:

| Area | Evidence to establish |
| --- | --- |
| Layout and input | Every visible action has pointer and keyboard activation; focus survives hide/expand/restore; menus cannot activate obscured content; pointer and keyboard resizing respect constraints. Narrow layouts preserve remembered preferences. |
| Unavailable terminal features | Explicit unsupported actions show readable, non-blocking, auto-dismissing notices without losing drafts, selection, attachments or focus. Exercise known absence, denied access, unanswered probes, and local/SSH/tmux differences. Repeated failures do not queue unbounded alerts; an old timeout cannot dismiss a newer notice. Fallback controls remain reachable, and unresolved save/capture/submission errors remain visible at their source after the notice expires. |
| Pane controls and surface lifecycle | Top-left navigation remains reachable. Top-right order is maximize/restore active right surface, bottom toggle, right toggle; maximize is absent from the surface header and from chrome when the right host is effectively hidden; a visible empty chooser can maximize. Only opened instances appear as tabs, each with `×`. Empty starts hidden; explicit empty show retains the chooser. Add opens, hide preserves, last-close hides. Closed has a pinned count/expand header and independent bounded scrolling without losing items. |
| Surface cardinality | Every type supports tab `×` and top-right maximize/restore. Git, Files, Agents, Plan, Activity, and each custom non-terminal type reuse their singleton tab. Child detail/history selects Agents, plans select Plan, tools/MCP select Activity, preserving documents. File buffers stay within Files. Only Terminal repeats. |
| Fixed activity and questions | Grouped Agents and Plan summaries stay above the usable composer outside transcript scroll. “Agents N” shows the total children through work, completion and hover: 12 of 13 completed still shows blue “Agents 13”, then solid green “Agents 13” when all complete. Working uses a pulsing blue circle; all-successful completion uses solid green and persists until hover/focus X dismissal. Unfinished, failed, waiting and interrupted groups cannot be dismissed or shown successful. Individual histories remain in Agents. Pending questions remain accessible in narrow/maximized views. |
| Phone column selection | Exercise 47×22 and the 40×22 Go minimum: hamburger/F2 selection, full-width navigation/surfaces/terminal, usable composer and Submit, attention returning to Conversation, draft/session preservation, and restoration of wide geometry. Resize below the minimum without hidden prompt submission. Check both themes and narrow/wide transitions; distinguish PTY evidence from actual phone/SSH behavior. |
| Compact activity overflow | At tight sizes, show compact Plan and Agents summaries and one scrollable question/approval card. Exercise many children, long requests, request switching, completion and dismissal. Individual targets remain correct; prompt, settings and usage stay visible, drafts survive and no overflow action submits a request. |
| Question delivery | One question at a time; navigation preserves typed answers/selections without submitting. Explicit Submit targets the request, not prompts. Blocking waits and supported async continuation/delivery are distinguished. Multi-client answers reconcile once; resolved controls invalidate. Defaults, navigation, timeout, and disconnect never auto-answer; unsupported provider modes are explicit. |
| Text and visuals | Combining marks, wide characters, emoji sequences, long paths, tabs, and multiline content preserve cursor/selection boundaries and alignment. Both themes remain readable with ordinary fonts and reduced color. |
| Approval cards and attention | Distinct approval cards above the prompt show the requested action, supplied targets and agent-supported choices; full detail opens Activity without approving. Prompt and question drafts survive request navigation. Other-thread questions, approvals and failures have badges and top-bar attention even with navigation hidden; incoming events never steal focus. |
| Surface continuity | Inspecting tools or child activity preserves file buffers and view state. Streaming updates do not unexpectedly move a user's reading position. Missing upstream detail is visibly unavailable. |
| Thread restoration | Switch between threads in the same and different checkouts; restore their opened surfaces, drafts and reading positions. Files/Git use the selected thread's checkout. Existing terminals preserve process identity and working directory without restart, redirection or controller transfer. Another client's navigation stays independent. |
| Activity identity and graphics | Ordinary user/agent roles remain recognizable from alignment and tint without repeated author headers; tool/MCP/subagent detail retains meaningful labels. Full available detail remains accessible; collapsed groups expose failures and pending requests. Rich mode supports verified brand marks and image previews without invented avatars. SVG-derived icons retain legibility at measured cell sizes; scroll/resize/overlays leave no stale placements. Fallback preserves hierarchy, labels and useful color. |
| Scheduling | Prompts and checkout writers queue separately; delegated children retain their parent's ownership; disconnect cannot release it; independent worktrees can progress. |
| Queue controls | Edit, remove and reorder waiting prompts with keyboard and pointer controls. Race those commands against dispatch, another client and reconnect; accepted changes take effect once, stale edits preserve text, removal prevents dispatch, and unchanged attachment captures remain intact. Queue changes do not resume saved work or bypass checkout eligibility. |
| Prompt context | Files, selected lines, images, Git diffs and terminal output are removable attachments before Send. Queued execution uses the exact content captured at submission despite later source changes. Failed capture/transfer preserves the draft; reconnect and restart/Resume do not replace snapshots with live reads. |
| Clipboard and attachment previews | Pasted images/files become removable context without Send. Changing the clipboard afterward does not replace staged content; late intake cannot attach to another thread. Supported thumbnails/files open a centered, read-only viewer with keyboard/pointer expand, restore and close. Markdown raw/preview uses the eyeball control and never enables editing. Accepted previews match stored captures; unsupported formats/capabilities remain explicit. Large/invalid images, resize, rapid preview changes and modal dismissal preserve responsiveness and remove obsolete image placements. |
| Multi-client terminal | Repeated New terminal creates independent right-side instances alongside the center-bottom terminal. Every terminal has its own single input/resize controller; clients cannot send input or resize without that instance's authority. Switching tabs, hiding, or maximizing/restoring neither starts/restarts shells nor transfers control. |
| Maximized surface | The visible right host's selected surface or empty chooser expands while app chrome and the ADE composer configuration/usage footer remain visible. Restore recovers preceding pane sizes/visibility and tab state. Terminal geometry changes obey controller authority and preserve process identity. |
| Agent configuration | Agent-dependent model/effort/permission/context/speed choices reconcile supported options and confirmed values. The prompt strip stays visible, distinguishes Selected/Running/stale states, becomes read-only with effective values during active/waiting work, uses progressive overflow at narrow widths, and offers keyboard/mouse access. |
| Queued settings | Submit with different captured settings, change idle composer preferences, reorder items and reconnect: each queued prompt retains its recorded selection. Explicit settings edits validate dependent options and race safely with dispatch. Unavailable/rejected captured options require resolution; execution records confirmed values without silently substituting defaults. |
| Collaborative files | Simultaneous edits converge across clients; peer cursors follow edits without taking focus; each client's undo preserves other clients' work. Autosave status distinguishes durable shared content from disk synchronization. |
| Observed turn changes | Recorded start/end comparisons remain stable after later edits and are labelled observed changes, without claiming authorship. Provider-reported edits remain separately identified; unavailable baselines are explicit. |

Exact bindings remain deferred to an interactive prototype. Turn rollback is deferred; observed comparisons do not authorize restoring files.

## Catch-up, persistence, and duplicate writes

For [queued-message steering](activity.md#steering-a-queued-message), verify that
pointer and keyboard select the same prompt and active turn. Acceptance must
preserve captures/settings, append input once, remove only that queued item and
leave questions, approvals and ongoing work intact. Exercise stale queue/turn,
completion versus steering, concurrent clients, definite rejection, lost receipts,
restart/Resume and incompatible settings. An uncertain delivery cannot also be
dispatched as a new prompt. Verify unsupported adapters explain unavailability
without Stop-and-Send fallback; fixtures do not establish real provider behavior.

Exercise snapshot/live-update handoff while a tool completes, a request resolves, and a prompt enters its queue. The returning client must see each logical item once with no missing completion. Retry delivery, reconnect repeatedly, expire a history cursor, and interrupt artifact publication. Verify unavailable history is reported rather than regenerated by execution. [Storage contract](storage.md)

Inject disconnects before acceptance, after durable acceptance but before acknowledgment, and after completion. Reconcile command identities and known outcomes to prevent repeated prompts, approvals, saves, staging, commits, or Git actions. An uncertain external outcome requires reconciliation, not blind retry; do not claim blanket exactly-once effects. Test competing startup attempts and configured-home isolation. Live reattachment only catches up. After server restart, interrupted work requires explicit Resume by default; explicitly opted-in continuation needs integration-specific eligibility tests, including manual Stop and pending-request exclusions; reconnecting or replaying history must not resume it automatically.

For collaborative buffers, test duplicated and reordered delivery, disconnected edits, stale revisions, and reconnect replay. Verify convergence without applying an accepted edit twice, and verify each client's undo preserves peer edits. Durable buffer acknowledgment is distinct from workspace-file synchronization: show Saved only when the relevant shared revision is synchronized to disk. Older autosave completions must not mark newer edits Saved.

## Files, Git, and lifecycle failures

Use fixtures with different staged, unstaged, and shared-buffer content. Verify autosave races, permissions failures, interrupted writes, and rendered/raw switching preserve work. Clean external changes merge automatically and converge to clients; overlapping edits, deletion, or replacement pause autosave, preserve versions, and require review. Test stale disk notifications and queued saves so neither bypasses that pause. [Editor contract](editor.md)

Verify graph selection cannot redirect a prepared action after refresh; soft reset preserves index/worktree bytes; fast-forward-only pull never falls back; partial staging preserves unrelated work; repeated rebase conflicts show correct side identities. Agent conflict resolution must stop before staging or continuation. Test cancellation with files already modified, competing external writers, and unresolved index stages. Do not treat cancellation as rollback. [Git contract](git-client.md)

Exercise Git/buffer coordination with in-flight saves, edits arriving during the autosave pause, and branch/pull/rebase success, failure, conflict and cancellation. A failed initial save prevents dispatch. Reconciliation establishes a new baseline before autosave resumes; overlapping versions stay recoverable for review. Delayed old saves cannot overwrite the resulting files, and unrelated staged work remains intact.

Test TUI exit independently of server shutdown, suspend/resume, process failure, and graceful server stop. Restore host terminal state and report surviving owned processes or incomplete shutdown honestly. Synthetic fixtures do not establish working ACP adapters or real PTY behavior. For [questions](questions.md), inject reconnects and competing client submissions while navigating a multi-question set; preserve answer drafts, reject stale resolved controls, and verify actual provider support before claiming async delivery.

Verify right Terminal tab close and the bottom terminal's explicit close end the targeted server session; panel toggles, tab switches and thread navigation preserve surviving sessions. Test busy terminals, two clients observing one session, stale input/resize after closure, failed or uncertain shutdown, repeated close delivery and reopening after termination. Tab removal must not count as confirmed process exit. New terminal creates a new session; showing a hidden pane reuses surviving ones. Preserve unrelated processes, filesystem changes and retained attachment snapshots. All checks remain **NOT RUN**.

## Terminal and visual evidence

Record OS, terminal/version, shell, locale, font, cell geometry, application revision, protocol capabilities, and exact local/SSH/tmux path. Cover iTerm2 and Ghostty on macOS plus selected modern Linux representatives; record tmux versions/configuration and remote OS. Windows Terminal is best effort and nonblocking. [Capability research](../research/terminal-capabilities.md)

Manually exercise click, drag, wheel, selection/copy, context menus, keyboard equivalents, live resize, narrow/short geometry, and focus recovery. Inspect dark/light captures and actual interaction. Test negotiated graphics and keyboard enhancements both available and unavailable, including SSH/tmux paths; core workflows must remain usable without images. A screenshot cannot establish input correctness, and one terminal cannot establish universal compatibility.

## Tooling and completion evidence

Configure and document each app's actual formatter, build, tests, and static checks: Go uses gofmt and idiomatic error handling; Rust uses rustfmt and Clippy with justified unsafe boundaries; Bun uses strict TypeScript and selected formatter/linter/type checker/test tooling. The first Go slice configures `make build`, `make check`, and `make test` in `apps/go`; the root Bun bootstrap does not provide application checks for the later Bun reference.

Measure input-to-render latency, resize responsiveness, streaming throughput, startup/attach time, memory, and retained-output growth under stated workloads. Record hardware, versions, distributions, and reproducible commands; numeric budgets remain unapproved. Investigate regressions against measured baselines.

Completion reports identify changed behavior, commands/results, manual evidence, skipped checks, and remaining gaps. Review diffs for data loss and duplicate effects. Documentation-only work checks links, consistency, and whitespace; it never claims runtime validation.

Verify Thinking/Waiting follows reported turn state, animations stop on disconnect or terminal outcomes, and Stop remains available during active waiting. Confirm Send/Stop/paperclip/gauge ordering, unknown context without invented fill, and frontend-local dismissal surviving unrelated ticks/text but reopening for changed or new work.

## Notification and structured-question review

Verify the [answered-history contract](questions.md#answered-questions-in-conversation-history):
exact accepted Q&A appears once at resolution, in a compact read-only transcript
card with overflow-only expansion. Check full text/copy access, keyboard/mouse,
themes, narrow layouts and preserved thread reading/expansion state. Reconnect,
duplicate events and competing clients must not duplicate or replace the accepted
answer with a local draft. Pending/uncertain/failed delivery is not Answered;
history interaction cannot resubmit a response or modify the composer.

Exercise zero/pending bell colors and badge counts, right packing when maximize
is absent, and dot-to-X dismissal without changing label activation. For questions,
check radio auto-advance without submission, checkbox toggles, Other text, optional
and required validation, top tab bounds and overflow-only controls. Verify fixed
Back/Next arrow slots stay empty at the ends, answered markers do not shift
buttons, the current question remains visible through narrow navigation, and
approval overflow leaves no hidden approval hit targets. Use both mouse
and keyboard, including native Space delivery. Verify narrow card/prompt geometry,
offscreen choice focus, independent scrolling, saved drafts, changed question
revision/schema isolation, stale competing responses and legacy command retries.
Record native PTY results separately from raster captures and provider evidence.

Navigation status checks cover working/attention/error/finished precedence,
including async questions during running work, errors in children, recovery and
unknown state. Hover and focus keep active/error/attention circles intact; only
finished, closable threads show Close. Check vertical options, Closed trash,
background-thread animation, visibility/filter/scroll bounds, disconnect and no
animation-driven persistence.

Thread-card checks cover icon-only creation through keyboard and mouse, coherent
title/metadata/padding activation, and distinct status/menu actions. Review
dark/light, limited-color and plain-icon grouping, long titles, scrolling and
pane bounds. Verify Close/Reopen preserves drafts across attached clients.

Verify the accepted [component states](components.md) in dark/light, limited-color
and monochrome modes: rest, hover, selected, focus and combined states. Selection
must survive hover on a neighbor, focus must be independently underlined, and
status icons must retain semantic colors. Outlined containers keep stable fills.
Tab edges select without closing; the separate icon remains a close action.
Verify single-row tabs/actions, reserved arrows, narrow overflow, keyboard paths
and focus/draft preservation on resize. Fallbacks must not change dimensions or
wrapping across color profiles. Bound styled row size under repeated painting and preserve text,
partial style changes and non-SGR escapes while removing superseded SGR runs.

### Sidebar settings acceptance

- Search and project filter compose without changing the active thread or prompt;
  each client restores its own filters. Header controls and fixed Closed/Settings
  remain reachable at 40×22 and 47×22 through the navigation column.
- Project-picker label and gear have disjoint hit areas and keyboard paths.
  The sidebar holds categories and the main form replaces all workspace panes
  and composer. Settings scroll independently; Back restores prior geometry,
  surfaces, sessions, reading positions and drafts. Hidden composer shortcuts
  cannot send; narrow layouts switch categories/form with hamburger/F2.
- App gear shows General/Appearance/Keybindings/About with `Settings / <category>`;
  a project gear shows only Project/General/Keybindings for one named project with
  `Settings / <category> / <name>`. No All projects settings scope exists. Verify
  Unicode and long-name breadcrumbs at wide and compact sizes, category changes,
  and Back restoration. F8 cannot change theme from project settings.
- Project contains only Name/Icon/Remove, with color nested inside Icon. Project
  General shows workspace inheritance and effective value; app-default updates
  propagate only to inherited projects, explicit overrides stay fixed, and Use
  app default resets inheritance. Writes bind the selected project and revision
  through `project.update`, without modifying app settings or another project.
  Restart continuation stays app-only. Project Keybindings honestly reports
  unavailable overrides and does not expose global controls as project edits.
- App/project changes use expected revisions; stale rename retains its text and
  explicit retry preserves other clients' changes. Old server capabilities give
  update guidance; unsupported worktree creation visibly fails with no new thread.
- Removal defaults to Cancel, binds the reviewed project/count revision, and
  atomically purges its threads/views/payloads while preserving checkout files.
  Busy work and stale targets fail; a removed/re-added folder cannot reuse old
  removal authority; old creation retries cannot resurrect deleted records.
- Restart continuation is off by default, recovers eligible fixture execution
  only when enabled, preserves queue captures at turn boundaries and never
  resumes manual Stop or answers a pending request. Real provider recovery
  requires new runtime evidence.

See [sidebar validation](../research/go-sidebar-settings-2026-09-20.md).

### Draft creation, Closed composer and workspace context acceptance

- New thread changes only client-local state. Navigate away and relaunch a named client: retain each project's text, attachments and selections. Demo Agent is preset; model selection is explicit and offers only supported Reference model. Invalid or incomplete selections cannot send or create an orphan thread.
- First valid Send atomically creates one thread and one captured prompt. Lost acknowledgments, reconnect and restart do not duplicate either; deleted creation retries cannot resurrect a thread. Preserve newly typed and unrelated drafts when accepted captures are deleted. Missing projects retain draft text with an explicit unavailable destination.
- While running or waiting, direct and overflow settings controls are read-only, show effective values and use those values for Send. Idle preferences survive and become editable again when idle; queued captures remain unchanged.
- Closed card, metadata and padding selection open history without Reopen. Verify the banner and separate far-right Reopen action at wide and compact sizes. Explicit Reopen sends nothing; valid Send atomically reopens and accepts one prompt. Empty, unsupported, stale and failed sends leave Closed state and drafts intact, including races with another client reopening.
- Workspace context reads the server's draft project or thread checkout, refreshes on selection/explicit refresh, and distinguishes branch, detached HEAD, non-Git, fixture and unavailable states. Check stale responses cannot replace another selected context. Reads never mutate Git or disk files.
- Older servers without the relevant capabilities show useful unavailability and upgrade guidance. Do not automatically restart user-owned servers or imply that rebuilding the client upgrades the running backend.

These are acceptance criteria, not a claim that interactive or compatibility checks have run. Record actual evidence separately.

Executed evidence for the latest creation/composer/modal/sidebar refinement is
recorded in the [Go review](../research/go-draft-composer-2026-09-20.md); it does not
establish real provider, terminal or remote-device compatibility.

### Project destination and file completion acceptance

- New thread always opens its searchable destination picker: zero, one or many
  projects, All projects and a specific filter. Add project remains reachable;
  registration opens/restores the chosen project's local draft, with no thread
  before first valid Send. Cancel preserves the previous draft and destination.
- Folder completion starts at app General's persisted server starting folder;
  verify home expansion, absolute paths, navigation, same-name projects, Unicode,
  spaces, hidden-prefix behavior, empty/error states and capacity. Registering or
  changing the setting never creates folders or mutates checkout files.
- Exercise inline `@` directory navigation and file selection through keyboard
  and pointer. Enter/Tab/Escape cannot leak into Send; changing queries or threads
  cannot apply stale results. Check bounded query/result work and narrow layouts
  at 40×22 and 47×22 with prompt/actions preserved.
- File selection inserts the relative mention and a removable attachment. Verify
  quoted spaces, UTF-8 regular-file limits, eight-item capacity, traversal/symlink
  escape, unreadable/missing/oversized files and draft preservation on rejection.
- Modify a file before Send and after acceptance: capture the former at Send,
  retain accepted bytes for duplicate commands, queued edits, steering and
  recovery. First-Send capture failure creates no orphan thread or partial prompt.
- Verify persisted revisioned starting-folder changes across clients/restart,
  stale-setting conflict preservation and explicit older-server upgrade guidance.
  Test only isolated server homes; never restart the user's backend implicitly.

These are acceptance targets. Record executed checks and remaining native-terminal
limits in the [project-path checkpoint](../implementation/project-path-checkpoint.md).
