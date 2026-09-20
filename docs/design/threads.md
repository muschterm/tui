# Close, reopen and delete threads

Status: the user's 2026-09-20 review chooses Close/Reopen, a Closed section, row hover actions and a separate permanent Delete action. The implementation uses T3 Code's inactive-only Close policy as a prototype assumption: running work, queued prompts, pending requests and active children prevent Close. Close never implicitly cancels work. This policy remains adjustable independently of the deletion contract.

## Open and Closed navigation

Open threads appear above **Closed** within the selected [project filter](projects.md). Each open row has a status circle: pulsing blue for working, yellow/orange for attention (pending answers/approvals, waiting or recovery), red for reported thread or child failures (including API errors), and solid green for finished/idle work with no remaining work or requests. Errors take precedence over attention, then working. Historical transcript text does not establish a current error. Unknown or disconnected state stays neutral. Hover or keyboard focus replaces only a green, closable circle with the Close checkmark; active and attention circles keep their status on hover. Each row has a vertical ellipsis menu with Close or Reopen and Delete permanently. Keep that glyph centered in its fixed right-edge button, with two blank cells separating it from a full/truncated title; the gap remains inside the existing row/menu hit regions. Closed rows replace their leading icon with a trash icon on hover/focus; selecting the row label reopens it. Quick actions, row selection and the ellipsis have separate hit regions and keyboard paths. A non-closable status circle selects its thread. Closed hover/focus still shows the trash icon, independent of its former run status. Animation shares the existing bounded UI clock, includes visible background threads and does not persist animation frames; hidden/offscreen navigation rows alone do not keep the clock running. Explicit Close in the options menu retains the existing server eligibility checks.

Close preserves history, drafts, attachments, questions, opened surfaces and terminal sessions. Closing the selected thread opens another open thread in the current project filter, or shows an empty center with New thread, Reopen a closed thread and Add project actions. Reopen restores the saved thread view without sending a prompt or resuming interrupted execution. A remotely closed thread can remain selected for reading; it shows Reopen in place of its composer. New prompts and Resume require Reopen first.

Closed can collapse or hide, with Show Closed and F4 restoration paths. Existing persisted `RecentsHidden` and `RecentsCollapsed` preferences are reused for compatibility; user-facing copy says Closed. Filtering, collapsing and hiding never close or delete anything. Thread lifecycle changes are server-owned and appear in all clients; each client's selected thread, filter and other navigation remain independent.

## Permanent deletion

The trash icon and ellipsis Delete entry open one confirmation naming the thread and its scope: history, drafts and owned work. Cancel is initially selected. Only explicit confirmation sends Delete; Escape or the close control cancels. A lifecycle revision guards against stale controls after another client closes or reopens the thread. The action is available for open and closed threads.

Delete removes the thread, its activity/plan/children, requests, queue, captured prompt data, terminal fixtures and saved per-thread view data from live application records. Other projects, threads and their drafts remain. It does not delete workspace files, project folders or Git worktrees. The current synthetic runner cannot advance a removed thread; real provider cancellation and process cleanup remain integration work and must not be claimed from these fixtures.

Deletion purges associated command payloads and projects every saved view onto remaining threads atomically. Later stale saves receive the same projection, preventing deleted drafts from reappearing. Empty application state stays empty after restart. Minimal creation/deletion retry fingerprints prevent lost acknowledgments from recreating deleted data; see [ADR 0007](../adr/0007-thread-deletion-and-view-projection.md). Existing external backups, migration backups and client recovery exports are outside this logical database deletion.

An attached client drops deleted thread state, cancels its local pending references and selects a remaining open thread or the empty center. A view writer can reconcile a server deletion against its last acknowledged view; it cannot overwrite unrelated edits by another process using the same client name. Unconfirmed final saves still export recoverable surviving content. A rare pending-creation tombstone without read-only client evidence remains a reported save conflict rather than replaying a creation command to probe it.

## Acceptance checks

- Close/reopen preserves content, drafts and terminal identity; busy Close reports a reason without changing work.
- Hover icon actions and label selection have separate hit regions. F4 and Tab/Enter reach all actions.
- Delete defaults to Cancel, rejects stale lifecycle revision and removes only the selected thread's data.
- Two clients observe deletion; surviving drafts remain editable and persist. Stale saved views and retried creation cannot resurrect the deleted thread.
- All threads can be removed, the empty app can restart, and Add project/New thread can start again without reseeding fixtures.

The [navigation review](../research/go-navigation-review-2026-09-20.md) records actual validation and remaining integration limits.
