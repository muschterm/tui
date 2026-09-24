# Plans, tool activity, and subagent inspectors

Implementation evidence (2026-09-19): the [first Go slice](go-slice.md) now exercises a fixture-driven subset of this contract. See the [validation report](../research/go-slice-validation-2026-09-19.md) for executed checks and limitations; provider/editor/real-terminal behavior below is not implied by fixture results.

Status: required presentation scope added after round 3 and strengthened by the user's visual correction. Plan steps, subagent transcripts/tool history, tool calls, MCP calls, and right-sidebar details are required. Turn semantics are settled; exact tokens, retention, and adapter versions remain implementation work.

## Recognizable activity roles

User messages, agent replies, tool operations, and child work need distinct visual identities. Ordinary conversation messages use alignment, tint and spacing without repeated You/Agent headers, following the latest implementation review. A user message is a right-aligned tinted box ending at the prompt outline's right extent, hugging its content up to 80% of that width, with one padding cell each side and one tinted padding row above and below; agent replies and reported thinking stay left-aligned without a box and wrap at the mirrored 80% cap, so neither side runs the full column; tool rows stay left-aligned (2026-09-23, following the [T3 Code reference](../research/t3-code-design.md); supersedes the full-width box from earlier that day). Consecutive transcript blocks are separated by one blank row (2026-09-23). Preserve meaningful tool/MCP names and inspected-child identity. The [visual reference](visual-design.md) supplies the broader color and composition direction; its older author headers are superseded. Avoid repeated nested cards and invented avatars. Verified official agent artwork remains optional identity decoration outside repeated message headings.

Keep readable agent prose separate from inset operational rows. Compact related tool calls beneath their owning activity, preserving chronology and providing clear expansion/inspection controls. Distinguish the call type, target, current state, and supplied result information. Grouping must not imply a failed or waiting call completed because its siblings did.

User attachments and supported returned images have bounded thumbnail/preview areas with labels. Rich mode uses actual raster content and verified brand assets where available; user avatars are optional supplied/configured images. Fallback retains message alignment and role colors, control labels and attachment metadata; it does not restore repeated You/Agent headers. Keep conversation text selectable rather than rasterizing the whole transcript. Subagent transcripts and inspectors reuse the same message/tool visual language.

## Prompt context attachments

Prompts support removable context attachments: files, selected lines, images, Git diffs and terminal output. Show each attached item with enough source information to identify it and allow removal before submission. Adding an attachment does not send the prompt.

Capture the attached content when the user presses Send. An accepted queued prompt retains that exact content even if its source changes before execution. Preserve source identity and relevant revision/range or comparison scope alongside the captured content; a mutable path alone cannot satisfy the snapshot requirement. Agent tools may still inspect the live workspace during execution.

If capture, transfer or supported-input validation fails, preserve the draft and identify the affected attachment rather than silently dropping it or dispatching an incomplete prompt. Durable acceptance includes recoverable attachment content under the application home, through SQLite or retained artifacts. Reconnect and explicit restart/Resume reuse the accepted capture instead of rereading its source. Exact pickers, limits, formats and transport remain implementation details. Queued-prompt editing is accepted below; [settings are captured per prompt at Send](thread-configuration.md#settings-captured-per-prompt) and change only through explicit editing of that queued item's settings.

### Inline file mentions

Typing `@` in the composer opens inline directory/file completion rooted in the
draft project's directory or the existing thread's actual checkout. Match name
prefixes within the current directory; reveal hidden entries only for an explicit
dot prefix. Up/Down navigates results. Enter/Tab browses a directory or selects a
file; Escape dismisses. These completion keys never send a prompt. Preserve
surrounding text, Unicode editing and the draft through browsing and dismissal.

Selecting a file inserts `@relative-path` (quoted when it contains spaces) and
adds a removable source-backed file attachment. Typing path-like prose alone does
not attach it. The Go slice supports regular UTF-8 files up to 64 KiB each and at
most eight attachments. It does not recursively attach directories. Completion
queries are bounded asynchronous reads; cancel superseded work and reject stale
results after edits, dismissal, project/thread changes or restored drafts.

The server validates the source against the selected project/thread checkout and
captures content at Send. Reject traversal or symlink escape outside that root,
unreadable/missing files, unsupported content and size/count overflow without
creating a thread, dispatching partial context or discarding the draft. Capture
errors remain visible beside the composer with details and attachment
removal; they clear on successful Send or removal of the last attachment. Accepted
retries, queue edits, steering and recovery retain the accepted bytes; they never
reread a changed source. Completion is gated by `path-completion`, and real file
capture by `workspace-file-context`. Unsupported servers show upgrade guidance.
These capabilities do not establish provider delivery, clipboard support or an
editable Files integration.

## Clipboard intake and read-only previews

User-requested refinement, 2026-09-23: right-clicking selected conversation or
read-only surface text offers Copy. Right-clicking the prompt offers Paste and,
when prompt text is selected, Copy. Preserve the existing cursor and selection
when opening the menu. Paste inserts at that cursor or replaces the selected
range; it never submits. Provide keyboard access to the same menu and preserve
focus/drafts when dismissing it. Clipboard reads happen only after explicit
Paste and must not apply to a changed thread, draft, cursor or selection while
the read is pending. Keep the behavior available on macOS and Linux, including
Omarchy; terminal-consumed shortcuts and unsupported clipboard access need an
honest fallback rather than assumed success.

Forwarded paste shortcuts use the same guarded local reader as the prompt
menu. Over SSH or inside herdr, copy uses the terminal clipboard route and
Paste comes from the viewing terminal. Herdr panes may retain no SSH markers
even when their client is remote; do not read or write the application host's
clipboard based solely on their absence.

User-requested refinement, 2026-09-19: support pasting images and copied files into the composer as removable context, with thumbnails where possible. A thumbnail or file chip opens a centered floating viewer. Its expand/restore control switches between the default centered size and the available application area, keeping close and restore reachable. This expands the viewer; it does not guarantee one image pixel per screen pixel. Keep image aspect ratio. Use the same viewer for supported attachment previews from history.

The viewer is always read-only, including raw Markdown. It is distinct from the editable Files surface. Markdown supports raw text and rendered preview, with an eyeball icon for preview and an explicit Raw action/label. Give preview, mode switching, expand/restore and close both pointer and keyboard paths. Escape closes and returns focus to the originating control; viewing must preserve the composer draft, selection, attachments and the underlying reading position. Long text remains selectable and scrollable, with the normal scrollbar behavior. Unsupported formats retain useful name/type/size information and an honest unavailable-preview state; viewing must never execute file contents or launch an editor.

Stage ephemeral clipboard bytes when Paste is explicitly invoked, so copying something else afterward cannot replace an attachment. Send commits the staged content to the existing durable capture contract; it must not reread the current system clipboard. A source-backed file still follows capture-at-Send semantics: identify source changes since an earlier preview and make the changed content reviewable. Queued and historical previews use the accepted capture rather than the current workspace file. Async intake results belong to the originating thread/draft and must not appear in another thread after navigation.

Clipboard intake, preview support and agent input support are separate capabilities. Do not equate a successful text paste or image display with binary clipboard access, or a successful preview with the agent accepting that format. Use negotiated terminal support or a verified local clipboard adapter; provide an explicit attachment path when the clipboard cannot supply the requested data. Keep graphics optional and bounded, with metadata fallback. Fetching, decoding, thumbnail generation and artifact publication run outside rendering and must not reintroduce input backlog.

The user confirmed that ordinary text pastes into the prompt, while images and copied files become attachments. Do not turn arbitrary path-like prose into a file attachment just because it resembles a path; actual copied-file intake has file identity. Pasting does not submit either text or attachments. Image/file intake and the viewer are not implemented by the first slice's synthetic attachment menu. See the [feasibility and integration notes](../research/clipboard-previews-2026-09-19.md).

## Prompt queue controls

Group the queue count, message previews and Steer/Edit/Remove/reorder controls in one
rounded container with a stable interior background, following the
[component rule](components.md). Align each preview and its actions on one row
with a gutter between them and an inset from the border. Keep the queue visually
separate from activity summaries, requests and the prompt. Bound visible items
in short layouts and show the hidden count in the queue header; activating that
header opens all queued items and their controls. First/last reorder arrows keep
their slots but are disabled when no move exists. Truncating a preview never
changes its saved text or attachments.

Show edit, remove and reorder controls directly in the visible per-thread prompt queue, with pointer and keyboard access. These actions remain available until a prompt starts running. Editing queued text or changing order must not silently reread unchanged attachments. Removing a waiting prompt does not interrupt current work; reordering does not bypass checkout scheduling. Once execution begins, report that state instead of applying a stale queue edit to the running turn. Preserve unsent edits if dispatch or another client wins a race. See [server scheduling](server.md#scheduling-and-unattended-requests).

### Steering a queued message

**Accepted, 2026-09-20:** expose **Steer** on each visible queued message and in
the full queue controls, with the same mouse and keyboard command. Ordinary
Send during active work still queues. Explicit Steer delivers that selected
message into the **same active turn**, where it becomes conversation input and
no longer waits for a later turn. It does not interrupt/restart the agent,
start a competing turn, merely move the message to the front, or bypass the
checkout writer lease. Keep Steer labelled in compact layouts; other queue
actions may use familiar icons with descriptive focus/hover help.

Steering requires a live active turn and a verified capability on its agent
connection/adapter. A waiting turn may accept steering only when its integration
supports it; steering never answers a pending question or grants approval.
No active turn, disconnection, unsupported delivery, restart awaiting Resume or
an incompatible configuration produces an explanation and preserves the queued
message. Do not implement a silent Stop-and-Send fallback.

Bind the action to the queued prompt identity/revision and the active turn the
user targeted. Reject a race with editing, removal, dispatch, turn completion or
replacement. Use the queued message's saved text and attachment captures, never
the current composer draft or reread sources. Its captured settings remain part
of the record. They must be compatible with the running turn: unless an adapter
can explicitly validate/apply an allowed change within that turn, explain a
mismatch and leave the message queued for editing or later execution. Never
silently discard a requested model/effort/permission/context/speed choice.

Preserve unsaved queued edits; require Save or Cancel before steering that item.
Show delivery pending/unconfirmed separately from confirmed acceptance. Remove
the item from the waiting queue only when delivery is confirmed; an uncertain
outcome must be reserved against ordinary queue dispatch while reconciled.
Retain accepted input with its target turn and full captured content. A lost
receipt, reconnect, retry or restart must not inject it twice or move it to a
new turn. Definite rejection leaves it queued. Resume remains explicit after a
server restart, and retained delivery outcomes must be reconciled first.

This behavior is shared by all three reference apps. The initial Go implementation
demonstrates fixture delivery only; ACP framing alone does not prove steering.
Each real adapter must verify active-turn targeting, content support, settings,
acknowledgment and recovery semantics. Steering also does not establish the
separate asynchronous-question contract. See [ADR 0009](../adr/0009-turn-bound-steering.md).

## Thread activity and right-sidebar details

The center workflow presents readable messages and concise structured activity. Use dedicated presentations for plans, tool/MCP calls, and subagent runs instead of dumping raw protocol messages into the transcript. Preserve chronology and relationships without making every event a new row.

Activation opens or reuses the corresponding singleton right-side surface: Agents for child runs/history, Plan for the current plan and retained revisions, and Activity for tools/MCP and other operational detail. These are separate types, each with its own tab close control and global maximize/restore support; only Terminal repeats. Keyboard activation reaches the same view and reveals a hidden host. Inspectors show full available detail with scrolling, selection, and copying. Content-local search and internal navigation remain prototype details; global/sidebar search is omitted. Changing inspection must not execute a tool, switch branches or agents, discard documents, or move the working directory.

Pending questions, approvals and failures in other threads must remain discoverable through thread badges and the persistent top-bar attention indicator. Activating an attention item opens its originating thread and relevant request or failure; receiving the event alone does not navigate or steal focus. The fixed area below describes the selected thread, not the application's only access to pending attention. See [background attention](layout.md#background-attention).

## Fixed area above the prompt

Keep a fixed area immediately above the prompt, outside transcript scrolling, with one Agents summary, a compact Plan summary and pending requests. Show “Agents N”, where N is the total number of children in the represented group, including completed children, with a pulsing blue circle for established active work. The count stays visible as children finish and after all have completed; it is not the remaining working count. Only when every child has successfully completed, use a solid green circle; retain the summary until explicitly dismissed. On hover or keyboard focus, the completed circle becomes an X in the same leading icon slot. Only that slot dismisses; the label still opens history. Keep the working count and full state in hover/focus help and the inspector. Never offer dismissal while work is unfinished. Activating the summary opens the singleton Agents surface, where individual children and full available history remain accessible. Plan uses completed/total progress (n/N), the same blue working and solid green all-completed states, and the same completed-only dismissal. Failed, interrupted, waiting, stale and unknown states stay distinct and never imply success. Dismissal belongs to the frontend thread view; new work or a changed group/plan reopens its summary, while unrelated stream updates do not.

The grouped summary supersedes the former running-only individual chips and More menu. Completed summaries remain until explicitly dismissed; dismissal never deletes history, cancels work or resolves pending requests. Child questions remain accessible independently of summary visibility. Use one shared tinted band with consistent text colors; circles convey working, success and exceptional states; hover/focus help retains full state and working counts. The label remains “Agents N” in every state, including while the completed circle becomes X; do not append “working” or “finished”. For 13 children with 12 completed and one running, show a pulsing blue circle and “Agents 13”; when the last completes, show a solid green circle and the same “Agents 13”. Animation stops on disconnection or a terminal turn state.

In the [single-column phone layout](layout.md#single-column-layouts-on-small-screens), the fixed activity area belongs to Conversation; explicitly selecting another column hides it while preserving its state and attention access. In constrained space, retain compact Agents and Plan summaries and one independently scrollable question or approval card. Preserve the prompt, settings and usage. Individual child selection lives in Agents; no separate running-child More menu is required. See [compact-layout rules](layout.md#fixed-area-above-the-prompt).

Pending question sets show one question at a time with top tabs and conditional Back/Next navigation. Preserve typed answers and selections across navigation; explicit Submit is separate. Keep the composer usable and route answers to the application request identity, never through ordinary frontend prompt submission. A declared turn-ending adapter fallback may deliver that answer in a correlated upstream continuation turn; see [the normalized question contract](questions.md#one-application-contract-multiple-delivery-mechanisms). Show concise “Waiting for answer” when waiting, or “Answer anytime” for verified continuing work. A native blocking request keeps its requesting turn open; the turn-ending fallback does not. Asynchronous work continues and receives the submitted answer later through a supported provider path.

Requests are durable and server-owned. Reconcile answers across clients, invalidate resolved controls, and never answer through navigation, defaults, elapsed time, or disconnect. Provider capability is conditional: base ACP v1 does not universally establish asynchronous questions. Unsupported modes need an honest state rather than simulated delivery. See [questions](questions.md) for the request contract.

Confirmed question responses remain visible in the conversation as compact,
read-only [Answered Q&A cards](questions.md#answered-questions-in-conversation-history).
Pair the original questions with accepted answers, expand long content in place,
and preserve response chronology without duplicating history on reconnect. These
cards scroll with the transcript; the fixed area remains for pending requests.

The compact view can show a summary or bounded output tail. The inspector must expose the retained source detail; it must not simply repeat the summary under a “full output” label. If the provider truncated or omitted content, state that limitation and distinguish it from an empty result. Do not recreate missing content from summaries or counters.

[Native Codex presentation research](../research/codex-presentation.md) inventories its available event/history surfaces; [ACP activity research](../research/acp-activity.md) identifies how adapters preserve them and where optional extensions are needed. The application normalizes those differences at the integration boundary rather than making every surface speak a vendor protocol.

## Approval cards

Permission approvals share the fixed request area above the prompt, using a distinct approval card rather than a question form. Show the requested action, affected files or working directory where supplied, and the actual choices supported by that agent. Name the requesting origin only when it is not the thread's own agent, such as a child run; the thread has one chosen agent, so cards do not repeat "Claude" or "Codex" (2026-09-23). The raw origin stays in the request detail and Activity. For example, Allow once and Deny are illustrative choices, not universal options. Preserve supplied scope and duration; do not invent a session-wide grant or silently broaden a one-time approval.

Opening full details selects the singleton Activity surface. Inspecting details does not resolve the request. The prompt, its draft, settings and usage stay available. Questions and approvals remain separately identified when sharing request navigation; changing the selected request preserves question drafts and never activates an approval choice.

Explicit approval targets the server-owned request and its current payload. Shared resolution invalidates stale controls on other clients; disconnect, defaults, navigation and inspection never grant permission. Apply the existing request reconciliation rules without treating an approval as an ordinary prompt. Other threads' pending approvals remain reachable through background attention.

## Plans

Show the agent's reported steps and their pending, active, or completed state, with priority where supplied. Keep the current plan visible as a coherent checklist. ACP v1 plan updates replace the entire plan; treat them as snapshots rather than repeatedly appending duplicate tasks. Do not invent stable step IDs where the source supplies only an ordered list. [ACP activity research](../research/acp-activity.md)

Keep prose describing a proposed plan distinct from the structured execution checklist. An approval or mode transition must come from a supported interaction, not from guessing at the prose. The compact current plan stays above the prompt; opening Plan shows the full received plan and retained revisions. Receiving a final message does not authorize the UI to mark every step complete.

## Tools and MCP

One logical call has one activity item whose state and details develop as source updates arrive. Its compact form should identify the operation, meaningful target, current state, and concise result or error. Preserve waiting-for-permission, running, completed, failed, and cancelled/unknown distinctions where supplied; do not claim a tool ran merely because a request appeared.

The inspector shows supplied arguments, structured content, output, error information, file locations, and other relevant metadata. Present readable formatting first with a way to inspect raw supplied structure. File locations can open the file surface at the reported location; diffs can open their comparison. Never execute output content as a side effect of rendering it.

MCP calls identify their server and tool when supplied. A generic tool-call title is not enough to infer an MCP server, and configuring an MCP connection does not establish that a particular call used it. Preserve output content blocks and source metadata through the adapter when supported. Unknown tools still get a usable generic inspector.

## Subagents

Show a distinct entry for each subagent run, with parentage, purpose/name, lifecycle state, and progress that the source actually supplies. Allow inspecting nested runs as well as siblings. A click opens the child's available full transcript and tool activity in Agents; it does not hand the original thread to that child. The fixed grouped summary supplements this retained history.

A collapsed group can summarize several children to keep the main transcript calm. Activating it exposes the individual entries and transcript actions. Keep failures, waiting approvals and pending child interactions discoverable in the summary; do not collapse them into a misleading generic success count.

The child view uses the same message, plan, and tool presentations as the parent. Show navigation back to its parent and a clear indication of which run is being inspected. Loading or following child output must preserve the user's position in the parent thread. Child histories may be retrieved through a supported agent connection or reconstructed from received events; the source and coverage must remain known.

The Codex reference integration must validate child transcript/tool-history access, not just a row saying “subagent running.” If the chosen adapter loses backend child data, record the adapter gap before claiming this feature complete. Generic ACP agents that expose only aggregate activity must show a partial/unavailable-history state. Core ACP v1 alone does not establish this feature: the researched adapters use negotiated, evolving subagent extensions. Pin the adapter, SDK, backend, and supported dialect together. [Extension and adapter evidence](../research/acp-activity.md)

Closing a child inspector does not cancel work. Show targeted cancellation or other child controls only when the negotiated interface supports them; do not relabel parent cancellation as child cancellation. Pending child interactions must remain discoverable from the active thread even when its inspector is closed.

## Streaming and state

Route events by their actual thread/session, parent/child, turn, and item identities. A child cannot borrow another child's transcript because titles match. Plan snapshots, incremental tool updates, and completed-item replacements have different merge rules. Preserve existing fields when an update omits them and reconcile final payloads without duplicating streamed content.

History replay and reconnect must not create duplicate cards or pretend replayed state is live. Preserve uncertainty when a child's final state cannot be established. Opening a stored child transcript should not resume the agent merely to read its history.

Keep input, pane resizing, and selection responsive during large streams. The UI should follow live output only while the user chooses to follow it; scrolling back or inspecting another item must not cause repeated jumps. The conversation follows the end while the user is at the end; otherwise it shows a jump-to-bottom control with the count of user/agent messages received since scrolling away. Application-home SQLite with auxiliary files is selected; exact retention limits and artifact boundaries remain open, but any truncation must be explicit and its relationship to “full available detail” documented.

## Presentation coverage to verify

| Category | Expected presentation |
| --- | --- |
| Messages | Formatted text/Markdown/code and supported attachments; stable streaming updates |
| Plans | Fixed compact current plan; singleton Plan with full detail/history; distinct prose proposals |
| Tool calls | Readable call summary, state, full supplied arguments/results/errors |
| MCP calls | Tool details plus supplied MCP server/tool identity and structured content |
| Commands | Live output preview, full retained output, reported completion/error status |
| File changes | Affected paths and supplied diffs with an explicit comparison scope |
| Subagents | Grouped working/completed summary with explicit completed-only dismissal; singleton Agents with parent/child navigation, lifecycle, and available full transcript/tool history |
| Permissions and questions | Pending questions above composer; one question at a time, preserved drafts, explicit Submit; meaningful blocking/async state and capability-aware delivery |
| Search, media, and review | Readable summaries plus supplied content, attachments, links, and review markers |
| Compaction and reasoning summaries | Distinct notices or inspectable source-supplied summaries; no inferred hidden content |
| Turn lifecycle | Completion, failure, interruption, and pending work shown distinctly |
| Usage | Context, subscription windows, and API cost under the stream-only [usage contract](usage.md) |

This is a presentation inventory, not a claim of universal provider support. Native Codex findings and adapter mappings determine which fields can actually be presented. Test snapshots, streaming refinements, errors, cancellation, nested/concurrent children, replay, unavailable history, and mouse/keyboard inspector access using shared fixtures and later live adapter checks.

Approval/user-input controls must track the actual request lifecycle, including resolution elsewhere, cancellation, and disconnect. Prevent duplicate submissions and remove stale active controls while retaining the recorded result. Unknown future item types get a safe generic detail view rather than disappearing from the thread.
