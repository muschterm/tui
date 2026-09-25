package protocol

// Shared text documents (ADR 0022, editor slice B).
//
// A document is one server-owned, Yjs-compatible text (library
// github.com/reearth/ygo v1.50.0, root text named "t", indices in UTF-16 code
// units) for one file in a checkout. All clients that open the same file share
// one document. The server persists every accepted update before
// acknowledging it and autosaves the text to the file.
//
// Lifecycle commands travel through POST /v1/command and are journaled:
//
//   - document.open (ThreadID or ProjectID selecting the checkout, Path,
//     ClientID) opens or joins the document for Path (a clean relative
//     slash path, not in .git) and returns its ID as Receipt.TargetID. The
//     file must be editable (see DocumentMaxBytes); otherwise the command
//     fails with code "document_read_only" and a reason, and the client shows
//     the read-only Files view instead. ClientID is added to Openers.
//   - document.close (TargetID, ClientID) removes ClientID from Openers and
//     releases the editor role if it held it. Closing never discards edits:
//     a document with no openers keeps autosaving and is unloaded (removed
//     from the snapshot and storage) only once it is saved; a paused or
//     failed document stays listed until resolved.
//   - document.edit (TargetID, ClientID) makes ClientID the editor when no
//     client is (idempotent for the editor); otherwise it fails with
//     "editor_busy". Only one editor at a time (multi-editor collaboration
//     is slice C): other clients are observers and see Editor with the
//     reason DocumentSimultaneousUnavailable.
//   - document.take-edit (TargetID, ClientID) transfers the editor role
//     immediately and increments EditGen (idempotent for the editor). The
//     previous editor's later updates are rejected (stale_generation).
//   - document.resolve (TargetID, ClientID, Text = DocumentResolve*,
//     Revision = the DurableRev the client reviewed, DocumentDisk = the
//     DiskID of the DocumentVersions it reviewed) resolves a paused document.
//     Accepted updates are committed first; a changed DurableRev fails with
//     "stale_document". The server then re-reads the file: if it is no longer
//     the reviewed version, the versions are refreshed, the document stays
//     paused and the command fails with "stale_document" (fetch the
//     versions again). discard is refused for an unpaused document with
//     unsaved edits ("document_unsaved").
//
// Content travels on GET /v1/documents/{id}/stream?client_id=<ClientID>&replica=<n>
// (WebSocket, same authentication as /v1/events; JSON text messages). replica
// is the decimal Yjs client ID of the client's replica; it is bound to
// ClientID for the document's lifetime and may not be the server's. Observers
// may omit it. GET /v1/documents/{id}/versions returns DocumentVersions.

// Document command kinds.
const (
	DocumentKindOpen     = "document.open"
	DocumentKindClose    = "document.close"
	DocumentKindEdit     = "document.edit"
	DocumentKindTakeEdit = "document.take-edit"
	DocumentKindResolve  = "document.resolve"
	// DocumentKindDismiss (TargetID, ClientID, Text = DocumentDismissConfirm)
	// permanently deletes a quarantined document's retained storage.
	DocumentKindDismiss = "document.dismiss"
)

// DocumentDismissConfirm must be document.dismiss's Text: the retained,
// possibly unsaved edits are deleted.
const DocumentDismissConfirm = "delete_retained_edits"

// Document states (DocumentStatus.State).
const (
	// DocumentStatePending: durable revisions are ahead of the file and a save
	// is scheduled.
	DocumentStatePending = "pending"
	// DocumentStateSaving: a save of SavingRev is being written.
	DocumentStateSaving = "saving"
	// DocumentStateSaved: the file holds exactly revision SavedRev ==
	// DurableRev.
	DocumentStateSaved = "saved"
	// DocumentStatePausedConflict: the file changed in a way that overlaps
	// the document's edits (or its line endings/BOM changed). Autosave is
	// paused; base, document and disk versions are retained for review.
	DocumentStatePausedConflict = "paused_conflict"
	// DocumentStateDeleted: the file was deleted or replaced by something
	// that is not a regular file. Autosave is paused; versions are retained.
	DocumentStateDeleted = "deleted"
	// DocumentStateFailed: writing the file (or persisting the document)
	// failed; updates are kept and the save is retried with backoff. Error
	// holds the last failure.
	DocumentStateFailed = "failed"
	// DocumentStateReconciling: the document is being compared with the file
	// (after a server restart, a resume or a resolution); nothing is written
	// until that finishes.
	DocumentStateReconciling = "reconciling"
	// DocumentStateReadOnly: the file became unsuitable for editing (too
	// large, binary, invalid UTF-8, mixed newlines, symlinked or hard
	// linked). Autosave is paused; versions are retained.
	DocumentStateReadOnly = "read_only"
)

// Resolutions (document.resolve Text).
const (
	// DocumentResolveKeepDocument writes the document over the reviewed file
	// (recreating a deleted file). Refused while the path is not a regular,
	// singly linked, owner-writable file (or absent). The retained versions
	// are cleared only once the save is confirmed; if the file changes again
	// first, the document pauses again.
	DocumentResolveKeepDocument = "keep_document"
	// DocumentResolveUseDisk replaces the document text with the editable
	// file content through a server edit; all observers receive it.
	DocumentResolveUseDisk = "use_disk"
	// DocumentResolveDiscard drops the document and its retained versions
	// without writing the file. Its ID becomes unknown.
	DocumentResolveDiscard = "discard"
)

// DocumentTextName is the Yjs root text every replica uses.
const DocumentTextName = "t"

// DocumentMaxBytes bounds an editable file (including a BOM and CRLF line
// endings) and every document text.
const DocumentMaxBytes = 1 << 20

// DocumentSimultaneousUnavailable is the observer reason while another client
// holds the editor role.
const DocumentSimultaneousUnavailable = "Simultaneous editing unavailable"

// DocumentStatus is a document's server-authoritative state (Snapshot.Documents).
// Durable and saved are distinct: DurableRev counts persisted updates;
// SavedRev is the revision whose text is on disk, and Saved means only
// SavedRev == DurableRev with State saved. The snapshot copy is coalesced
// (about 10 per second at most); stream status events are exact.
type DocumentStatus struct {
	ID string
	// Checkout is the canonical checkout directory and Path the relative file.
	Checkout, Path string
	State          string
	// Reason explains paused, read-only and failed states in user terms.
	Reason string `json:",omitempty"`
	// Error is the last low-level failure (failed state).
	Error                string `json:",omitempty"`
	DurableRev, SavedRev int64
	// Editor is the ClientID allowed to send updates, valid with EditGen.
	Editor  string `json:",omitempty"`
	EditGen int64
	Openers []string `json:",omitempty"`
	// Newline is lf or crlf and BOM whether the file starts with a UTF-8 BOM.
	// Document text is always LF; the server restores CRLF and the BOM on save.
	Newline string
	BOM     bool `json:",omitempty"`
	// Versions reports that base/document/disk versions are retained.
	Versions bool `json:",omitempty"`
	// Quarantined marks a stored document that could not be loaded (State
	// failed). Its stored edits are retained in storage but it cannot be
	// edited or streamed; open the file again for a new document, and
	// document.dismiss deletes the retained data.
	Quarantined bool `json:",omitempty"`
}

// DocumentVersions are the retained versions of a paused document, as file
// bytes (BOM and line endings included). Base is the last content the
// document and the file agreed on, Document the document when the pause
// began, Disk the file then (absent when deleted or not a regular file).
type DocumentVersions struct {
	ID       string
	Base     []byte
	Document []byte
	Disk     []byte `json:",omitempty"`
	// DiskState is present, absent or not_regular.
	DiskState string
	// DiskID identifies the disk version ("sha256:<hex>", "absent",
	// "not_regular:<token>" or "large:<token>"); document.resolve must echo
	// it as DocumentDisk.
	DiskID string
}

// Stream message types.
const (
	// Server → client.
	DocumentEventState    = "state"    // full state: first message, and after resync
	DocumentEventUpdate   = "update"   // a durable update from another client or the server
	DocumentEventAck      = "ack"      // the sender's update op is durable at Rev
	DocumentEventRejected = "rejected" // the op (or request) was refused
	DocumentEventSync     = "sync"     // answer to a sync request
	DocumentEventStatus   = "status"   // DocumentStatus changed
	DocumentEventClosed   = "closed"   // the document was discarded; the socket closes
	// Client → server.
	DocumentRequestUpdate = "update"
	DocumentRequestSync   = "sync"
)

// Rejection reasons (DocumentRejected.Reason). Content reasons come from the
// document layer: invalid, origin, content, surrogate_split, too_large and
// rejected (NUL, size, CRLF), with Message saying which. content also
// covers anything that is not visible text in the shared text: other roots,
// maps, formatting and text inserted and deleted within one update, so send
// each transaction's own update. too_large also covers more than 4096 new
// insert runs or new delete ranges in one update.
const (
	DocumentRejectNotEditor       = "not_editor"
	DocumentRejectStaleGeneration = "stale_generation"
	DocumentRejectUnavailable     = "unavailable" // storage failing; retry later
	DocumentRejectInvalid         = "invalid"
	// DocumentRejectResyncPending: an earlier update on this stream needed a
	// resync; this later update was not applied.
	DocumentRejectResyncPending = "resync_pending"
)

// DocumentEvent is one server→client stream message.
//
// On connect the server sends state (the whole document as one V1 update at
// Rev, plus Status). Every later update event carries the next Rev, so a
// replica that applied state and every update is at the server's DurableRev.
// Updates are durable before they are sent. An ack answers each update op
// once it is durable (duplicates are acked too). A rejected update was not
// applied. With Resync the stream stops applying updates, answers earlier
// ops, then closes normally; the client continues with a new replica on a
// new stream (see the recovery contract in client/document.go). Without
// Resync (unavailable) the replica stays valid and the client resends from
// the refused op. A connection that falls behind is closed (policy
// violation) and must reconnect.
type DocumentEvent struct {
	Type string `json:"type"`
	// Rev is the durable revision reached by state, update, ack and sync.
	Rev int64 `json:"rev,omitempty"`
	// Update is a V1 Yjs update (base64 in JSON).
	Update []byte `json:"update,omitempty"`
	// Origin of an update event: "client" (Client is its ClientID) or
	// "server" (merge of an external change or a resolution).
	Origin string `json:"origin,omitempty"`
	Client string `json:"client,omitempty"`
	// Op echoes the client op of an ack or rejected.
	Op       string            `json:"op,omitempty"`
	Rejected *DocumentRejected `json:"rejected,omitempty"`
	Status   *DocumentStatus   `json:"status,omitempty"`
	// ServerClient is the server replica's Yjs client ID (state).
	ServerClient uint64 `json:"server_client,omitempty"`
	// Reason explains closed.
	Reason string `json:"reason,omitempty"`
}

// DocumentRejected explains a refused request.
type DocumentRejected struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	// Resync: this stream's replica can no longer be used. The server stops
	// applying updates from the stream, acks earlier ops once durable and
	// closes the stream normally; reconnect with a NEW replica ID and
	// re-apply unacknowledged edits as a draft (client/document.go).
	Resync bool `json:"resync,omitempty"`
}

// DocumentRequest is one client→server stream message.
//
// update sends one V1 update produced by the client's replica (tagged with
// the replica's client ID) with a client-chosen Op (≤128 bytes) and the
// current EditGen. Retrying the same bytes is safe: a duplicate changes
// nothing and is acked. sync sends the replica's encoded state vector; the
// answer is a sync event with the missing update (use it after a reconnect
// when the replica may still be valid, then resend unacknowledged ops).
type DocumentRequest struct {
	Type        string `json:"type"`
	Op          string `json:"op,omitempty"`
	Gen         int64  `json:"gen,omitempty"`
	Update      []byte `json:"update,omitempty"`
	StateVector []byte `json:"state_vector,omitempty"`
}
