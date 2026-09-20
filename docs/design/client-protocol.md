# Shared application client contract

Status: required behavior for three complete native server-and-TUI applications. Recorded **2026-09-19**. This specifies semantics, not endpoints, message schemas, protocol version numbers or an implementation. The [first Go slice](go-slice.md#application-protocol-v1) defines and tests a fixture-only application protocol; cross-language conformance and live integrations remain unverified.

## Boundary and compatibility

Go, Rust/Ratatui and React/Ink+Bun implementations share this application contract. Clients connect using authenticated loopback HTTP/WebSocket; remote access uses SSH forwarding. The application boundary is separate from upstream ACP v1 agent connections. [Server ownership](server.md), [storage](storage.md), [collaborative editing](editor.md) and [thread configuration](thread-configuration.md) define the authoritative product behavior.

Peers must establish compatible contract versions and available capabilities before issuing dependent operations. Unsupported, unavailable and temporarily stale capabilities must be distinguishable. Do not infer provider features from application-protocol compatibility or silently downgrade an action into different behavior. Exact negotiation messages, version policy and compatibility ranges remain implementation decisions. Unknown optional presentation data must not corrupt known state; unsupported required semantics need an explicit failure.

## Commands, acceptance and uncertain outcomes

State-changing commands need stable identity across retries and connection loss. The server must distinguish receipt, durable acceptance, execution and final outcome. A client must be able to reconcile whether a submission was accepted before retrying; an optimistic local card is not acknowledgment. Duplicate delivery of the same command must not create another queued prompt, edit or approval decision.

An accepted command can subsequently fail, be cancelled or require review. A lost acknowledgment can leave its outcome unknown to a client. Preserve that distinction and reconcile against authoritative state. Process and filesystem effects may remain uncertain after failures; this contract does not promise universal exactly-once external effects. Do not replay side effects merely to reconstruct history. Identity scope, deduplication retention and reconciliation mechanics require implementation design.

Prompt submission includes the [context content captured at Send](activity.md#prompt-context-attachments). Durable acceptance must bind that prompt to recoverable attachment snapshots; queue execution and retry must not substitute newer source content. Failed capture or transfer preserves the draft and prevents silent omission. Exact wire representations and artifact-transfer mechanisms remain implementation choices.

Queue editing, removal and reordering target stable prompt identities and revisions. The server arbitrates those commands against execution start and concurrent client changes. All clients reconcile the accepted content and order; losing or stale edits preserve unsent text and report the authoritative outcome. An accepted removal prevents that queued prompt from dispatching. Queue mutation does not bypass checkout eligibility or the explicit Resume gate after restart. See [queue behavior](server.md#scheduling-and-unattended-requests).

## State synchronization and recovery

Attachment must provide a coherent snapshot and ordered updates, including a handoff that cannot lose changes occurring during snapshot transfer. Stable resource identity prevents repeated deliveries from creating duplicate activity. Replay cursors or equivalent positions must identify recoverable progress; an expired cursor or detected gap requires explicit resynchronization rather than pretending the client is current. Snapshots plus retained updates can satisfy this requirement without full event sourcing.

Attaching to a live server catches up with ongoing work. After server restart, history and queues are restored, unfinished runs are marked interrupted, and saved execution waits for explicit **Resume**. Resumption revalidates upstream availability and effective settings. Catch-up itself must never dispatch queued work or restart an agent turn.

Slow clients require bounded buffering and backpressure. Recoverable gaps may trigger resynchronization; authoritative changes, accepted commands and required inspection content must not be silently discarded. Presence may be coalesced as transient data. Exact limits, overflow responses, replay retention and artifact transfer mechanisms remain unselected; unavailable retained content must be labelled.

## Shared state and client authority

The server owns execution, queues, pending requests, documents and persistence. Focus, scroll, selection and inspection position belong to the client. Remembered layout preferences do not mirror another client's current focus. Presence and labelled peer cursors convey participation, not durable authority.

Simultaneous file edits converge, with prompt autosave and per-client undo preserving others' work. Edit operations need document identity and revision context; stale revisions must be reconciled or rejected explicitly, never blindly applied to different text. The collaboration algorithm is unselected. Durable document revision and on-disk saved revision are distinct. Clean external changes merge automatically; overlap, deletion or replacement preserves versions, pauses autosave and requires review. Recovered buffers require disk revalidation before saving.

Each terminal has one input/resize controller; other clients inspect without injecting input or changing dimensions. Ownership changes must invalidate stale-controller actions. Shared approval/request identities similarly prevent stale clients from resolving an already-resolved request. Detailed authority-transfer and competing-action rules remain to be specified.

Agent settings must expose requested, pending and acknowledged effective values where they differ. A selected value does not relabel an active run. Unsupported options, rejection and disconnected stale state remain visible, including in the persistent prompt configuration strip.

## Question requests and answers

Expose the [question contract](questions.md) through server-owned request identity, revision, originating thread/turn/child and upstream connection scope, execution mode and delivery state. Keep question navigation and unsent drafts per client. Snapshot/update recovery includes pending, resolved, withdrawn and uncertain requests, with accepted answers distinct from drafts.

Answer submission is a request-specific command, separate from normal prompt queuing. Reconcile competing answers and lost acknowledgments; reject a stale revision or already-resolved request without discarding unsent text. Server acceptance and confirmed provider delivery are separate outcomes. Advertise blocking and continued-work asynchronous delivery separately; a responsive client does not prove the requesting agent continues work. After restart, require explicit Resume and request revalidation rather than replying to old RPC identifiers.

## Authentication and verification boundary

Loopback placement does not replace authentication. Discovery and credentials must select the intended application-home server; forwarding does not authorize other homes or users. The Go slice provisions a per-incarnation bearer token in private application-home discovery; remote and browser credential flows remain design work. A future browser client needs explicit Origin validation and CSRF protections appropriate to its authentication flow; these are security boundaries for future implementation, not authorization for direct network exposure.

[Go backend tests](../../apps/go/internal/server/server_test.go) cover duplicate commands, bounded slow-subscriber disconnection, initial snapshot catch-up, restart awaiting Resume, competing fixture approvals, fixture-configuration rejection and unauthorized connections. [View tests](../../apps/go/internal/storage/storage_test.go) cover competing and delayed saves through revision checks. Incompatible peers, unsupported live-provider capabilities, transport-level lost acknowledgments, injected snapshot/live races, concurrent document edits and own-undo, external conflicts and real terminal ownership remain unverified. This slice replaces full snapshots rather than exposing replay cursors. Protocol fixtures and failure injection must verify behavior across all three implementations before claiming interoperability.
