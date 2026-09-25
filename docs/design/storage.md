# Storage and recovery

Status: accepted application-home, SQLite, collaboration and explicit-resume direction; retention and implementation details remain open. Recorded **2026-09-19**. The [first Go slice](go-slice.md) now uses application-home SQLite for fixture state, command receipts and revisioned client views. Its persistence, migration backup and restart checks have run. Prompt attachment artifacts are implemented for the prototype ([ADR 0017](../adr/0017-attachment-artifacts.md)); collaborative buffers, tool-output artifacts and provider recovery remain unimplemented.

## Location and authority

All preserved application state belongs under a machine/user-level directory in the user's home, overridden through an application-specific environment variable. `~/.myapp/` and `MYAPP_HOME` are illustrative examples, not selected names or a required shared global directory. Each consuming application owns its identity and prefix; the Go slice selects `~/.tui-go`, `TUI_GO_HOME` and `state.sqlite`, while other applications choose their own names. Do not repurpose `HOME` or `CODEX_HOME`.

The resolved home and user identify a server/state namespace. An alternate configured home intentionally permits separate state and a separate server instance. Projects and threads belong within that namespace rather than requiring authoritative databases in every checkout. Workspace source files and agent/provider-owned external state are not relocated by this decision.

SQLite holds as much persisted application state as practical; auxiliary files are permitted when necessary. The server is the authoritative writer. TUIs and future clients read or mutate state through the common client/server contract, never by directly modifying database files. Native implementations share observable persistence behavior; a shared SQL schema or direct database interchange is not implied.

## Preserved state

Preserve projects, threads and provider/session identifiers; available conversation, plan, child-agent and tool history; turn outcomes; prompt queues; requests and resolutions; drafts and unsaved editor buffers; remembered layouts and preferences; and references to retained artifacts. Distinguish unavailable upstream content from content actually received. A summary cannot silently replace required inspectable output.

Remember thread view associations so switching back restores its open surfaces, prompt draft and reading position. Keep these associations distinct from workspace geometry preferences and from another client's current navigation. Stored terminal references identify the original session; they do not authorize restarting or redirecting it if that session is unavailable. See [thread restoration](layout.md#switching-threads-and-projects).

Simultaneous live file editing, prompt autosave and labelled peer cursors are accepted. Per-client undo must not erase peers' work; the collaboration algorithm remains unselected. Distinguish a durably preserved document revision from the revision actually saved on disk. Retain identity and source-version context for reconciliation. Presence and peer cursors are ephemeral participation information, not durable execution authority. Focus, scroll and selection remain client-local.

Clean external changes merge automatically. Overlapping edits, deletion or replacement preserve versions, pause autosave and require review. Recovered buffers must be revalidated against current disk contents before writing; database restoration is not authorization to overwrite changes made while the server was absent. Precise revision records and merge machinery remain implementation choices.

Record acknowledged operations and outcomes sufficiently to reconcile retries after disconnects. Clients must not blindly repeat prompts or Git operations when acknowledgments are lost. Cross-process effects can have uncertain outcomes; preserve and surface uncertainty for reconciliation, without a universal exactly-once guarantee. Snapshots and retained events may implement catch-up; full event sourcing is not required. Records, transaction boundaries and durability settings require design and failure tests.

Accepted prompts retain the content captured from their explicit context attachments at Send, together with source identity and relevant revision/range or diff scope. Preserve those snapshots through queuing, reconnect and restart/Resume; a later read of the source is not an equivalent recovery. Apply the artifact readiness and acceptance guarantees below. See [prompt context](activity.md#prompt-context-attachments).

Steered prompts retain that same captured payload and their targeted turn identity.
Preserve pending/unconfirmed/accepted delivery state and its command identity so
neither retries nor normal queue dispatch duplicate the input. Accepted steering
leaves the waiting queue and remains in conversation history; restart does not
retarget it to a new turn. See [steering](activity.md#steering-a-queued-message).

Persist each prompt's selected model, effort, permissions and applicable context/speed settings captured at Send. Keep explicit queued-item revisions and the resulting turn's acknowledged effective configuration distinguishable. Reconnect and restart/Resume retain the accepted selection; current composer defaults do not replace it. See [per-prompt settings](thread-configuration.md#settings-captured-per-prompt).

## Artifacts and database evolution

Large tool outputs or binary artifacts may live outside SQLite when necessary, under the same application home. A database manifest can retain identities, metadata, hashes and readiness. These are proposed mechanisms, not a selected schema. Publication must make references usable atomically from the client's perspective: a supposedly complete result cannot point to a half-written artifact. Interrupted writes require recoverable cleanup and explicit unavailable states.

The Go prototype implements this for uploaded prompt attachments ([ADR 0017](../adr/0017-attachment-artifacts.md)). Bytes live in `<home>/artifacts/<id>`, written to a temporary name, synced and renamed before a schema-version-2 `artifacts` row (hash, size, sniffed media type, name, image dimensions, `staged`/`accepted` state, owning thread and command, timestamps, unavailable flag) commits; only a committed row is ever returned or referenced. A startup and periodic sweep removes temporary files and files without rows, marks rows whose file is missing unavailable, and expires staged artifacts after 7 days; clients may delete staged artifacts their drafts dropped, and a restored file clears the unavailable flag. Send binds staged artifacts to the thread in the command's transaction; thread deletion and project removal delete their rows in the same transaction and remove files after commit. Accepted artifacts have no age limit, and pre-migration backups do not copy the artifact directory.

Agent conflict-resolution jobs keep the index listing they are gated against in `<home>/git-baselines/<sha256>` ([ADR 0023](../adr/0023-merge-rebase-operations.md)): written to a temporary name, synced and renamed, verified by hash on read, and swept once no operation record references it and it is more than an hour old. A missing or damaged listing makes the gate fail closed. Shared-document state lives in SQLite (schema version 3, [ADR 0022](../adr/0022-shared-documents-go.md)); Git operation backups and conflict copies live as unreferenced objects in the repository itself, not in the home.

Version application storage and protocol expectations. Before migration, make a recoverable backup and validate the migration path; backup scope must include referenced auxiliary content where necessary. Reject or explain unsupported versions rather than silently interpreting them incorrectly. Backup naming, retention, migration tooling and downgrade policy remain unselected. Retention limits and storage budgets are open; cleanup must not silently violate the inspectable-history promise.

## Restart and recovery

Process handles, PTYs, sockets and in-memory provider objects are not resumable database state. Persisted identifiers and transcripts describe previous work; they do not prove a process is alive or a session resumable. Reconcile server identity, owned processes and provider capabilities before presenting recovered work as running.

After server restart, restore history and queues, mark unfinished runs interrupted, and require explicit **Resume** by default before continuing saved execution. The later [environment setting](settings.md#continue-threads-after-restart) allows opt-in continuation only for verified eligible integrations; manual Stop and pending requests remain gated. Never replay side-effecting tool calls, approvals or dispatch queued execution merely to reconstruct state. Resume may depend on provider support or require a new session; unsupported recovery must be visible. Attaching to an already-live server instead catches up with continuing work and does not introduce a restart Resume gate.

Graceful server stop saves state while cancelling work and stopping owned processes; workspace modifications remain. [Go tests](../../apps/go/internal/storage/storage_test.go) verify revisioned draft saves, legacy-schema migration with a pre-migration backup containing live WAL data, and rejection of newer schemas without changing database bytes; [artifact tests](../../apps/go/internal/storage/artifacts_test.go) cover interrupted publication, sweep reconciliation, staged expiry, Send binding, deletion and the version 1 to 2 migration backup. [Server tests](../../apps/go/internal/server/server_test.go) verify persisted views, restart awaiting Resume and durable fixture-command deduplication. Recovery of externally changed buffers, peer-safe undo, paused autosave on conflicts/deletion/replacement, transport-failure acknowledgment injection, failed-migration recovery workflows, unavailable provider sessions and configured-home isolation remain unverified.

Revisioned app execution defaults and project overrides share the server state transaction. Confirmed project removal purges thread-owned views and payloads atomically and retains minimal retry fingerprints; disk files are outside this deletion. See [ADR 0010](../adr/0010-environment-settings-and-recovery.md).
