# File browser and lightweight editor

Status: lightweight editing accepted in Q5; Q25/Q28 add simultaneous live collaboration and autosave, and Q29 settles external-change handling. Recorded 2026-09-19. No editor or collaboration implementation has been run.

## Accepted scope

The file surface provides browsing, viewing, and raw-text editing, with selection, undo/redo, search, recoverable document state, and protection against overwriting external changes. Markdown supports rendered and raw views; editing occurs in the raw view. The user's later answers replace explicit Save as the normal workflow with prompt server-owned autosave and real-time document synchronization.

Files can be inspected in the right sidebar and expanded when more room is needed. Core operations must have keyboard and mouse paths. Hiding a pane or switching the surface must not silently discard unsaved work.

The richer visual direction includes inline image-file previews, alongside image attachments and verified agent marks elsewhere in the shell. User avatars are optional supplied/configured images; invented avatars were rejected. Use bounded placements and preserve filename/metadata in the fallback. Image formats, decoding, resource limits, and tested graphics backends remain implementation decisions.

## Shared documents and autosave

Two connected clients on the same machine must be able to view and edit the same file simultaneously. Edits appear live in both; labelled peer cursors show participation without moving the local cursor, selection, scroll, or focus. The contract leaves room for future cross-machine clients without making that deployment part of this example. Presence reflects an attached client, not necessarily a different human.

The server owns the shared document and its disk-save pipeline. Each edit has client and operation identity and a document version so reconnect/retry cannot duplicate typing or apply an edit against the wrong text. Clients converge after concurrent inserts, deletes, and reconnects. A whole-buffer last-write-wins replacement does not meet this requirement. The actual collaboration algorithm and its native libraries are implementation decisions requiring a focused feasibility slice across the three languages.

Undo/redo applies to the initiating client's edits while preserving other clients' work. Cursor anchors must remain meaningful through concurrent edits. Presence is ephemeral; a disconnected cursor must not linger as though its owner remains active. Exact presence expiry and visual markers will be tuned in the prototype.

Autosave is prompt, with its timing tuned through the prototype. Display pending local edits, synchronization, Saving, Saved, paused conflicts, and failures truthfully. Acknowledgment of a durable document revision is distinct from successful synchronization to the workspace file. **Saved** means the displayed document version is reflected on disk. A later pending revision cannot inherit an earlier Saved status. Permission errors, full storage, rename/delete races, or connection loss preserve recoverable work and show the affected state.

Closing or hiding a view does not discard shared edits or cancel server autosave. A server restart restores document state but must reconcile it with the current file before any disk overwrite. Pending local changes need an explicit reconnection/recovery outcome; the app must not imply offline collaborative editing is supported merely because it preserves an unsent edit. Explicit Save may later be offered to flush pending changes, but is not the normal workflow or a selected binding.

## External files and conflicts

Agent processes, the interactive terminal, and external editors can modify disk without participating in the collaborative document. Q29 requires automatically incorporating cleanly mergeable changes. When edits overlap or a file is deleted/replaced, preserve both versions, pause autosave for that file, and open a resolution view. A read-only viewer should see external updates promptly as well.

Reconciliation needs the last agreed disk content, current shared document, and newly observed external content. A file-watch notification alone does not establish a safe overwrite. Revalidate before writing, distinguish the server's own saves from external changes, and prevent save/watch feedback loops. Resolve conflicting content explicitly, then establish the new disk/document baseline before resuming autosave. Exact merge mechanics and supported rename cases are implementation work.

Checkout writer queues coordinate root agent jobs and Git operations; they do not prevent collaborative human edits or external writes. Saving during a turn can contribute to its recorded observed changes. Conflict review must keep unrelated work and document versions intact.

Application-managed Git operations that rewrite files use the accepted [save/pause/reconcile sequence](git-client.md#coordinating-git-with-live-buffers). Finish pending saves before dispatch, pause disk autosave during the operation, and reconcile shared buffers with resulting files before resuming. Retain edits received during the pause; prevent old saves from running against the new checkout contents. Save or reconciliation failures preserve work and require review. This coordinates disk writes without treating a hidden editor or an acknowledged document revision as permission to overwrite files.

## Behavior to specify before implementation

- Preview versus pinned tabs and opening several files. Single-click preview and double-click/edit-to-pin remain proposals: the user answered Q25 by expanding collaboration scope, not by accepting every proposed convention.
- Which file operations accompany browsing: create, rename, move, duplicate, and delete are not yet accepted as a complete inventory.
- Directory expansion, hidden/ignored files, repository badges, symlinks, and paths outside the active workspace.
- Search scope and replace behavior; selection, clipboard, line navigation, indentation, and newline/encoding policy.
- Rendered Markdown should follow the shared document as the proposed default, with its disk-sync state identified; exact rendering and opening conventions remain prototype work.
- Read-only treatment of binary, unsupported-encoding, and oversized files; configurable limits rather than loading arbitrary content into memory.
- The collaboration algorithm, document-size limits, reconnection protocol, and concurrent undo implementation; exact autosave timing and presence presentation.
- How direct agent edits and ACP client-mediated reads/writes interact with dirty buffers.
- A shared conflict-result buffer contract used by the Git resolver.

## Quality requirements

Keep source versions recoverable when a save fails; never overwrite overlapping external edits without resolution. Preserve encoding/newline choices once those policies are settled. Use grapheme-aware selection and terminal-cell layout. Keep large directory traversal, syntax work, merging, and preview rendering out of the input loop.

Acceptance scenarios cover concurrent insertion/deletion, peer cursor movement, own-edit undo, replay/reconnect without duplicate edits, autosave/reopen, hidden buffers, external clean merge and overlapping conflicts, delete/replace, failed writes, Unicode, narrow layouts, and keyboard/mouse equivalence. No editor is implemented or tested yet. [Interaction research](../research/interaction-precedents.md) provides examples without establishing this app's behavior.

## Go prototype status

The Go reference app has a read-only Files surface: a lazily loaded tree and read-only buffers inside the single Files tab, with disk-change notices and explicit Reload ([details](go-slice.md#files-surface-read-only--2026-09-24)). It settles only read-only presentation of binary, invalid-UTF-8, oversized and non-regular files for the prototype. Editing, autosave, collaboration and the remaining behavior above stay pending the collaboration library decision and its feasibility probe.

Editor slice B (server side, 2026-09-24) implements server-owned shared documents over `github.com/reearth/ygo` v1.50.0 ([ADR 0022](../adr/0022-shared-documents-go.md)): `document.open`/`close`/`edit`/`take-edit`/`resolve` commands, a per-document WebSocket stream, validated client updates acknowledged only after their SQLite commit, a single editor at a time ("Simultaneous editing unavailable" for others until slice C), autosave after 750 ms idle and at most every 3 s through an atomic same-directory rename, clean three-way merges of external changes, and paused conflict, deletion and read-only states that retain base, document and disk versions until an explicit resolution. Editable files are regular, singly linked, at most 1 MiB, valid UTF-8 without NUL and with consistent line endings (BOM and CRLF are restored on save). The TUI editor (2026-09-25) edits Files buffers through these documents: a cell-native editor over a client ygo replica with grapheme-aware movement and selection, own-edit undo/redo (own inverse operations; ygo's UndoManager is not used), truthful save states (Saved only when the file holds the durable revision and nothing is unacknowledged), Take over for the single editor role, reconnect/resync recovery that resends or re-applies unacknowledged edits and never drops them silently, and a conflict review with confirmed Keep mine / Use disk / Discard ([details](go-slice.md#editing-shared-documents-tui--2026-09-25)). Peer cursors and simultaneous editors remain slice C; the editor is covered by automated tests, a real-server end-to-end test and rendered captures, not yet by an interactive review.
