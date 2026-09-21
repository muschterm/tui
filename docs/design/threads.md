# Close, reopen and delete threads

Status: the user's 2026-09-20 review chooses Close/Reopen, a Closed section, row hover actions and a separate permanent Delete action. The implementation uses T3 Code's inactive-only Close policy as a prototype assumption: running work, queued prompts, pending requests and active children prevent Close. Close never implicitly cancels work. This policy remains adjustable independently of the deletion contract.

## Open and Closed navigation

Group each thread's title and metadata inside one compact padded card with rounded corners, with a
blank row between adjacent threads. Use a quiet background for ordinary cards
and stronger whole-card feedback for selection, hover and keyboard focus. The outline retains grouping with limited color. Fill only interior content cells;
keep top/bottom borders and side edges on the sidebar background, including
selected and hovered cards, so the fill cannot form square exterior corners. Title, metadata and padding
select the thread for reading, including when Closed; the status quick action and vertical
ellipsis retain their specific hit areas. This translates the inspected T3 Code
row surface into terminal cells. The two content rows, top/bottom borders and separator trade some
list density for clearer grouping; scrolling preserves access to every thread.

Open threads appear above **Closed** within the selected [project filter](projects.md). Each open row has a status circle: pulsing blue for working, yellow/orange for attention (pending answers/approvals, waiting or recovery), red for reported thread or child failures (including API errors), and solid green for finished/idle work with no remaining work or requests. Errors take precedence over attention, then working. Historical transcript text does not establish a current error. Unknown or disconnected state stays neutral. Keep the leading status circle visible during hover and keyboard focus. A finished/idle, closable row reveals a Close checkmark in a reserved slot immediately left of the vertical ellipsis; a Closed row reveals a trash icon in that same slot. Center both action glyphs in their separate hit areas. Reserve the quick-action space even while hidden or unavailable so hovering does not change the title's width or truncation; retain a blank gutter after the title. The vertical ellipsis stays at the right edge and opens Close/Reopen and Delete permanently options. The circle and row label select an open or closed thread for reading; they never invoke Close or Delete. Quick actions, row selection and the ellipsis have separate hit regions and keyboard paths. Closed hover/focus still shows trash regardless of its former run status. Animation shares the existing bounded UI clock, includes visible background threads and does not persist animation frames; hidden/offscreen navigation rows alone do not keep the clock running. Explicit Close in the options menu retains the existing server eligibility checks.

Close preserves history, drafts, attachments, questions, opened surfaces and terminal sessions. Closing the selected thread opens another open thread in the current project filter, or shows an empty center with New thread, Reopen a closed thread and Add project actions. Reopen restores the saved thread view without sending a prompt or resuming interrupted execution. Selecting any Closed thread preserves its Closed state and opens it for reading with its composer available. Show “This thread is closed · Send a message to reopen” immediately above the composer, with Reopen at the far right; use two lines when compact. Explicit Reopen sends no prompt and does not Resume. A valid Send uses `prompt.reopen-send` to reopen and accept the captured message atomically. Compare the lifecycle revision even if another client has already reopened it; invalid text, unsupported settings, stale state or failed persistence must not reopen the thread. Resume still requires explicit Reopen first.

Closed stays pinned above Settings and can collapse or expand, with its count shown only while collapsed. Legacy hidden content keeps a Show Closed heading and F4 restoration path. Existing persisted `RecentsHidden` and `RecentsCollapsed` preferences are reused for compatibility; user-facing copy says Closed. Filtering, collapsing and hiding never close or delete anything. Thread lifecycle changes are server-owned and appear in all clients; each client's selected thread, filter and other navigation remain independent.

## Permanent deletion

The trash icon and ellipsis Delete entry open one confirmation naming the thread and its scope: history, drafts and owned work. Cancel is initially selected. Only explicit confirmation sends Delete; Escape or the close control cancels. A lifecycle revision guards against stale controls after another client closes or reopens the thread. The action is available for open and closed threads.

Delete removes the thread, its activity/plan/children, requests, queue, captured prompt data, terminal fixtures and saved per-thread view data from live application records. Other projects, threads and their drafts remain. It does not delete workspace files, project folders or Git worktrees. The current synthetic runner cannot advance a removed thread; real provider cancellation and process cleanup remain integration work and must not be claimed from these fixtures.

Deletion purges associated command payloads and projects every saved view onto remaining threads atomically. Later stale saves receive the same projection, preventing deleted drafts from reappearing. Empty application state stays empty after restart. Minimal creation/deletion retry fingerprints prevent lost acknowledgments from recreating deleted data; see [ADR 0007](../adr/0007-thread-deletion-and-view-projection.md). Existing external backups, migration backups and client recovery exports are outside this logical database deletion.

An attached client drops deleted thread state, cancels its local pending references and selects a remaining open thread or the empty center. A view writer can reconcile a server deletion against its last acknowledged view; it cannot overwrite unrelated edits by another process using the same client name. Unconfirmed final saves still export recoverable surviving content. A rare pending-creation tombstone without read-only client evidence remains a reported save conflict rather than replaying a creation command to probe it.

## Acceptance checks

- Close/reopen preserves content, drafts and terminal identity; busy Close reports a reason without changing work. Reading a Closed thread performs no lifecycle action. Explicit Reopen sends nothing; valid Send reopens and accepts exactly one prompt, while invalid/stale Send leaves state untouched.
- Hover icon actions and label selection have separate hit regions. Metadata and padding select for reading without reopening, closing or deleting. F4 and Tab/Enter reach all actions.
- Delete defaults to Cancel, rejects stale lifecycle revision and removes only the selected thread's data.
- Two clients observe deletion; surviving drafts remain editable and persist. Stale saved views and retried creation cannot resurrect the deleted thread.
- All threads can be removed, the empty app can restart, and Add project/New thread can start again without reseeding fixtures.

The [navigation review](../research/go-navigation-review-2026-09-20.md) records actual validation and remaining integration limits.
