---
status: accepted (user decisions 2026-09-24; server slice B implemented 2026-09-24)
---

# Server-owned shared documents in the Go reference

[ADR 0005](0005-collaborative-documents-autosave.md) requires server-owned
shared documents with durable updates, prompt autosave and reconciliation of
external changes, leaving the algorithm open. The
[2026-09-24 probe](../research/go-collab-2026-09-24.md) compared Go
libraries. This records the Go choices for editor slice B (server side). The
TUI editor widget and simultaneous multi-editor collaboration (slice C) come
later.

## Decision

- **Library:** `github.com/reearth/ygo` pinned at v1.50.0 (Yjs-compatible, pure
  Go, MIT). It is wrapped by `apps/go/internal/doc`; nothing else in the server
  touches the CRDT directly. One root text named `t`; CRDT indices are UTF-16
  code units, and the wrapper's API uses Go strings and UTF-8 byte offsets.
- **Validation before acceptance.** Each client update is applied first to a
  shadow replica. It is refused, and the authoritative replica left unchanged,
  when it does not decode, leaves integrations pending, adds items for a Yjs
  client ID other than the sender's bound replica (or the server's), carries
  formatting, embeds or nested types, splits a surrogate pair (the library
  accepts that and produces U+FFFD), produces invalid UTF-8 or NUL, would make
  the file exceed 1 MiB, or adds a CR before an LF. The text the library reports
  through its delta must equal the replica's own text, and every new clock unit
  must be text that became visible in `t`. That second rule refuses other
  roots, maps, formatting marks and text inserted and deleted within one update,
  so the state cannot grow invisibly. An update adding more than 4096 new insert
  runs or new delete ranges is refused before it is applied, and so is one that
  would take the encoded state past 40 MiB (checked per update; clients read
  up to 64 MiB). A content rejection ends the stream once its earlier ops are
  answered; the client continues with a new replica on a new stream and
  re-applies its unacknowledged text as a draft (the single recovery contract
  in `client/document.go`). `unavailable` (storage failing, back-pressure,
  stopping) keeps the replica valid: later updates are held until the client
  resends from the refused op. Replica bindings are released when their last
  stream closes. States are only sent from durable state.
- **Durable acknowledgement.** Accepted updates are committed to SQLite in
  batches of about 50 ms (one transaction with the document metadata).
  Acknowledgements and broadcasts to other observers follow the commit, never
  precede it. A failed commit keeps the updates in memory, acknowledges nothing
  and retries with backoff; retrying an update is idempotent.
- **Storage:** schema version 3 (with the usual synced pre-migration backup)
  adds `documents` (metadata JSON, baseline file bytes, compacted snapshot),
  `document_updates` (the log after the snapshot) and `document_versions`
  (base, document and disk versions retained for a paused document). The log is
  compacted into a snapshot after 256 updates or 8 MiB. Documents are
  identified per incarnation (`doc-<random>`), keyed by canonical checkout and
  relative path, and shared by every client that opens the file.
- **Single editor until slice C.** `document.edit` grants the editor role when
  nobody holds it; `document.take-edit` transfers it immediately and increments
  `EditGen`, like terminal Take control. A client's replica bindings are
  released when it closes the document with no stream connected. Updates from other clients or with a
  stale generation are refused with "Simultaneous editing unavailable".
- **Editable files:** a regular, singly linked file of at most 1 MiB, valid
  UTF-8 without NUL, with consistent LF or CRLF line endings, and writable by
  its owner. Document text is always LF; the server restores CRLF and a UTF-8
  BOM on save. A U+FEFF typed at the start of a BOM-less document is content.
  Other files fail `document.open` with `document_read_only` and use the
  read-only Files view.
- **Autosave:** after 750 ms without a change, and at most 3 s after the first
  unsaved change while edits continue. Only durable text is written. A save
  reads and hashes the file first; only if it still matches the baseline is the
  text written to a temporary file in the same directory, fsynced, renamed over
  the file (mode preserved) and the directory fsynced. The token is rechecked
  immediately before the rename, which runs under a save-generation guard: a
  pause (conflict, Git rewrite) bumps the generation and no older save can
  rename afterwards.
- **External changes:** the file is polled every 2 s and checked before every
  save. Unchanged content resumes. A clean line-based three-way merge (baseline,
  document, file) is applied to the document as a server-origin CRDT update,
  broadcast, committed atomically with the new baseline, and then saved.
  Changes that overlap or touch, changed line endings or BOM, a merge that is
  too large to diff cheaply, deletion, replacement by a non-regular file or new
  hard links pause autosave (`paused_conflict`, `deleted` or `read_only`),
  retain all three versions and make `SavedRev` −1. Edits continue durably while
  paused, and a restart never lifts a pause.
- **Resolution:** `document.resolve` (`keep_document`, `use_disk` or `discard`)
  first commits accepted updates. It then requires the reviewed `DurableRev`
  and the reviewed disk version (`DocumentDisk` = `DocumentVersions.DiskID`,
  a SHA-256 or an absent/not-regular/large identity). The server rereads the
  file. If it is not the reviewed version, the versions are refreshed, the
  document stays paused and the command fails `stale_document`. `keep_document`
  saves only over that exact content; versions are cleared only when the save
  is confirmed, and a file that changes again first pauses the document again.
  `discard` needs a ClientID and is refused for an unpaused document with
  unsaved edits. A storage failure is reported as a failure.
- **Crash semantics:** before a save renames, its hash and revision are stored
  as a pending write (up to four unconfirmed ones). A save that fails before
  its rename drops its entry; one that fails after it keeps it, and the file
  is then recognised as our own write (adopted, or overwritten by the next
  save) rather than an external change. An accepted `keep_document` is stored
  too, so a restart performs that save instead of reconciling. The new baseline and metadata are only ever stored
  together; if the record after a successful rename fails, later metadata
  writes carry the baseline. On restart, stored documents load as
  `reconciling` and nothing is written before the file is compared: a file
  equal to the document is saved, a file holding exactly the pending write is
  adopted as the baseline at its revision, a file diverged after a restart
  from both the baseline and every pending write pauses (the true base is
  unknown), and
  otherwise the rules above apply. A crash between commit and write resumes
  and saves. Temporary files matching the save pattern and older than the
  server start are removed beside loaded documents.
- **Damaged storage:** a stored document that cannot be loaded (unreadable
  metadata, CRDT load failure, a gap in its update log) is moved to
  `document_quarantine` with its reason; its log and versions stay stored. It
  is listed as `failed` with `Quarantined` set, the file can be opened again as
  a new document, and `document.dismiss` with an explicit confirmation deletes
  the retained data. Serve does not fail. `keep_document` whose parent
  directory is gone pauses as deleted.
- **Closing:** `document.close` never discards edits. A document with no
  openers keeps autosaving and is unloaded (storage deleted) only once saved;
  paused or failed documents stay listed until resolved.
- **Git coordination:** `beginDocumentRewrite(ctx, root) (release func(), error)` finishes pending saves,
  pauses autosave and bumps the save generation for every document in the
  checkout; its `release` (always non-nil, idempotent, called even on error)
  ends exactly the pauses that call made and reconciles each document with the
  rewritten file. Roots match exactly. A pause counts from the moment the
  actor receives the request, so a cancelled begin still pairs with its
  release, and one rewrite never ends another's pause. Begin fails with `document_unsaved` when edits are not
  durable or not saved. The Git ref and remote operations call these through
  `documentRewrite` (ADR 0021).

Wire details and field documentation are in
`apps/go/internal/protocol/document.go`; the Go client and the replica recipe
for the TUI are in `apps/go/internal/client/document.go`.

## Consequences

- ygo embeds the whole delete set in every incremental update, so update size
  and log growth follow deletion history; compaction bounds the log, not the
  per-update size. A validated keystroke against a 1 MiB document costs about
  4 ms on the development machine (`BenchmarkApplyClient1MiB`, insert-only
  history); deletion-heavy histories cost more.
- Undo is the client replica's in-memory UndoManager; it does not survive a
  restart or a replica rebuild.
- Two replicas per loaded document (main and shadow) double CRDT memory.
- The final check-and-rename leaves a window of one `renameat` in which an
  external write can be replaced; everything before it is detected.
- Rename-based saves do not preserve extended attributes, ACLs or ownership
  other than the running user's; hard-linked files are never edited.
- Documents whose opener crashed keep their opener entry and stay loaded until
  that client closes them or they are discarded.
- Quarantined data is retained, not readable through the protocol; recovery
  from it is manual (SQLite) until a reader exists.
- The line merge is deliberately permissive compared with `git merge-file`
  on ambiguous alignments: a randomized comparison of 200,000 small histories
  found no lost line, but about 2% of clean merges were conflicts in Git.

## Alternatives considered

- **Deln0r/ygo:** diverged from Yjs in the probe's differential test. Rejected.
- **automerge-go:** cgo, inactive upstream, not Yjs-compatible. Rejected.
- **Whole-buffer last-write-wins:** violates the editor contract. Rejected.
- **`git merge-file` for merges:** needs temporary copies of user content and
  a Git executable for non-repository files; a small Go line diff3 is used
  instead, conservative about adjacent changes.
