package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// Shared documents (ADR 0022, editor slice B server side). One docActor
// goroutine owns each loaded document (documents_actor.go): it applies
// validated client updates, commits them to SQLite in ~50 ms batches before
// acknowledging them, autosaves the durable text to the file and reconciles
// external changes. Streams live in documents_stream.go and disk access in
// documents_disk_*.go.
//
// Lock order: engine.mu and documentState.mu are never held while waiting
// for an actor; an actor takes engine.mu (status projection) and
// documentState.mu (registry) only briefly.
const (
	maxDocuments = 64
	// docBatchInterval coalesces accepted updates into one SQLite commit;
	// acknowledgements follow the commit.
	docBatchInterval = 50 * time.Millisecond
	// docSaveIdle and docSaveMax: save after this much idle time, and at
	// most this long after the first unsaved change while edits continue.
	docSaveIdle = 750 * time.Millisecond
	docSaveMax  = 3 * time.Second
	// docPollInterval detects external changes while a document is idle.
	docPollInterval = 2 * time.Second
	docRetryMin     = time.Second
	docRetryMax     = 30 * time.Second
	// Compaction folds the update log into a snapshot past either bound.
	docCompactUpdates = 256
	docCompactBytes   = 8 << 20
	// docMaxPendingBytes bounds accepted but uncommitted updates while
	// storage fails; beyond it updates are refused (unavailable).
	docMaxPendingBytes = 16 << 20
	// docMaxReplicas bounds replica bindings per document.
	docMaxReplicas = 256
	// docStopTimeout bounds the final commit and save at server stop.
	docStopTimeout = 5 * time.Second
	// docMaxStateBytes bounds a document's encoded CRDT state (checked at
	// compaction); beyond it the document pauses read-only.
	docMaxStateBytes = 40 << 20
)

// Disk observation kinds.
const (
	docDiskPresent     = "present"
	docDiskAbsent      = "absent"
	docDiskNotRegular  = "not_regular"
	docDiskLinked      = "linked"
	docDiskUnavailable = "unavailable"
)

var (
	errDocChanged = errors.New("file changed on disk")
	errStaleSave  = errors.New("save generation is stale")
)

// docObservation is one look at the file. Data is set for a regular file of
// an editable size.
type docObservation struct {
	Kind     string
	Token    string
	Mode     uint32
	TooLarge bool
	Data     []byte
	Err      error
}

// fileFormat is what the document text omits and the file keeps.
type fileFormat struct {
	BOM     bool
	Newline string // lf or crlf
}

const utf8BOM = "\xef\xbb\xbf"

// decodeDocFile converts editable file bytes to document text. reason is
// non-empty when the file cannot be edited; style is none when the file has
// no newline. With want (an open document's format) a leading BOM is
// stripped only when the document has one: a U+FEFF typed at the start of a
// BOM-less document is content, so its own saves round-trip.
func decodeDocFile(data []byte, want *fileFormat) (text string, f fileFormat, style, reason string) {
	if len(data) > protocol.DocumentMaxBytes {
		return "", f, "", "The file is larger than 1 MiB."
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", f, "", "The file contains NUL bytes."
	}
	if !utf8.Valid(data) {
		return "", f, "", "The file is not valid UTF-8."
	}
	s := string(data)
	if strings.HasPrefix(s, utf8BOM) && (want == nil || want.BOM) {
		f.BOM, s = true, s[len(utf8BOM):]
	}
	crlf := strings.Count(s, "\r\n")
	lf := strings.Count(s, "\n") - crlf
	switch {
	case crlf > 0 && lf > 0:
		return "", f, "", "The file mixes LF and CRLF line endings."
	case crlf > 0:
		f.Newline, style = "crlf", "crlf"
		s = strings.ReplaceAll(s, "\r\n", "\n")
	case lf > 0:
		f.Newline, style = "lf", "lf"
	default:
		f.Newline, style = "lf", "none"
	}
	return s, f, style, ""
}

func encodeDocFile(text string, f fileFormat) []byte {
	if f.Newline == "crlf" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if f.BOM {
		text = utf8BOM + text
	}
	return []byte(text)
}

func encodedDocSize(text string, f fileFormat) int {
	n := len(text)
	if f.Newline == "crlf" {
		n += strings.Count(text, "\n")
	}
	if f.BOM {
		n += len(utf8BOM)
	}
	return n
}

// docTextCheck vets every accepted update's resulting text.
func docTextCheck(f fileFormat) doc.Check {
	return func(old, next string) error {
		if strings.IndexByte(next, 0) >= 0 {
			return errors.New("NUL characters are not allowed")
		}
		if encodedDocSize(next, f) > protocol.DocumentMaxBytes {
			return errors.New("the document would exceed 1 MiB")
		}
		// Line endings are the server's: document text uses LF only, so a
		// new CR before LF would change the file's line-ending style.
		if strings.Count(next, "\r\n") > strings.Count(old, "\r\n") {
			return errors.New("carriage return before line feed is not allowed; the server writes line endings")
		}
		return nil
	}
}

func docSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// docMeta is the server-owned JSON stored with each document.
type docMeta struct {
	DurableRev int64  `json:"durable_rev"`
	SavedRev   int64  `json:"saved_rev"`
	Pause      string `json:"pause,omitempty"` // paused_conflict, deleted, read_only
	Reason     string `json:"reason,omitempty"`
	// Token and SHA identify the file as last agreed (the baseline).
	Token   string     `json:"token"`
	SHA     string     `json:"sha"`
	Mode    uint32     `json:"mode"`
	Format  fileFormat `json:"format"`
	Openers []string   `json:"openers,omitempty"`
	Editor  string     `json:"editor,omitempty"`
	EditGen int64      `json:"edit_gen"`
	// Replicas binds replica (Yjs client) IDs to ClientIDs.
	Replicas map[string]string `json:"replicas,omitempty"`
	Versions bool              `json:"versions,omitempty"`
	// LogBytes approximates the update log size since the snapshot.
	LogBytes   int64 `json:"log_bytes,omitempty"`
	LogUpdates int64 `json:"log_updates,omitempty"`
	// Pending lists saves recorded before their rename and not yet
	// confirmed (at most docMaxPending, newest last). A file holding exactly
	// one of them is our own write: it is adopted as the baseline at that
	// revision and may be overwritten by the next save. After a restart, a
	// file that diverged from the baseline and every pending write pauses
	// for review (the true base is unknown).
	Pending []docPending `json:"pending,omitempty"`
	// Keep is the reviewed disk digest (or docKeepAbsent) an accepted
	// keep_document resolution may overwrite, until its save is confirmed.
	// It survives a restart: the save runs without reconciling first and
	// is refused (and the document pauses again) if the file changed.
	Keep string `json:"keep,omitempty"`
}

const docKeepAbsent = "absent"

type docPending struct {
	SHA string `json:"sha"`
	Rev int64  `json:"rev"`
}

const docMaxPending = 4

func (m docMeta) pendingFor(sha string) (docPending, bool) {
	for _, p := range m.Pending {
		if p.SHA == sha {
			return p, true
		}
	}
	return docPending{}, false
}

func (m *docMeta) dropPending(sha string) {
	kept := m.Pending[:0]
	for _, p := range m.Pending {
		if p.SHA != sha {
			kept = append(kept, p)
		}
	}
	m.Pending = kept
	if len(m.Pending) == 0 {
		m.Pending = nil
	}
}

// documentStore is the persistence the documents need (storage.Store);
// tests substitute failing stores.
type documentStore interface {
	CreateDocument(storage.DocumentRecord) error
	CommitDocument(string, storage.DocumentCommit) error
	CompactDocument(string, []byte, int64) error
	DocumentVersions(string) (map[string][]byte, error)
	DeleteDocument(string) error
	LoadDocuments() ([]storage.DocumentRecord, error)
	QuarantineDocument(id, reason string) error
	LoadQuarantined() ([]storage.QuarantinedDocument, error)
	DeleteQuarantined(id string) error
}

// docClock lets tests drive autosave timing.
type docClock interface {
	Now() time.Time
	// At returns a channel that fires once at t (at once when t has
	// passed), and a stop function. Deadlines are absolute so a timer set
	// after time moved on still fires.
	At(t time.Time) (<-chan time.Time, func())
}

type realDocClock struct{}

func (realDocClock) Now() time.Time { return time.Now() }
func (realDocClock) At(at time.Time) (<-chan time.Time, func()) {
	t := time.NewTimer(max(0, time.Until(at)))
	return t.C, func() { t.Stop() }
}

type docKey struct{ root, path string }

// documentState is the engine's document registry.
type documentState struct {
	mu       sync.Mutex
	byID     map[string]*docActor
	byKey    map[docKey]*docActor
	creating map[docKey]chan struct{}
	streams  map[string]int // per document, "" is the total
	closed   bool
	wg       sync.WaitGroup
	// commands reserves document command IDs in flight (guarded by
	// engine.mu), so a concurrent retry waits for the first attempt.
	commands map[string]chan struct{}
	// Test hooks: store overrides the engine's store, clock the real clock,
	// and beforeRename runs in a save just before its generation check.
	store        documentStore
	clock        docClock
	beforeRename func(id string)
	// afterRename (tests) turns a successful write into a failure reported
	// after the rename, as a failed directory fsync would.
	afterRename func(id string) error
	// maxState overrides docMaxStateBytes (tests).
	maxState int
}

func (e *engine) docsLocked() *documentState {
	ds := &e.docs
	if ds.byID == nil {
		ds.byID = map[string]*docActor{}
		ds.byKey = map[docKey]*docActor{}
		ds.creating = map[docKey]chan struct{}{}
		ds.streams = map[string]int{}
		ds.commands = map[string]chan struct{}{}
	}
	return ds
}

func (e *engine) docStore() documentStore {
	if e.docs.store != nil {
		return e.docs.store
	}
	return e.store
}

func (e *engine) docClock() docClock {
	if e.docs.clock != nil {
		return e.docs.clock
	}
	return realDocClock{}
}

func (e *engine) docActor(id string) *docActor {
	e.docs.mu.Lock()
	defer e.docs.mu.Unlock()
	if e.docs.byID == nil {
		return nil
	}
	return e.docs.byID[id]
}

// documentCommand journals document.* commands. The document work runs
// without the engine lock; the command identity is reserved first so a
// concurrent retry waits and then finds the receipt.
func (e *engine) documentCommand(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	e.mu.Lock()
	e.docs.mu.Lock()
	ds := e.docsLocked()
	e.docs.mu.Unlock()
	for {
		wait, busy := ds.commands[c.ID]
		if !busy {
			break
		}
		e.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return protocol.Receipt{}, failure("cancelled", "the request ended before the document command finished; retry with the same command ID")
		}
		e.mu.Lock()
	}
	if r, err := e.store.Lookup(c); err != nil {
		e.mu.Unlock()
		return protocol.Receipt{}, err
	} else if r != nil {
		e.mu.Unlock()
		return *r, nil
	}
	if e.stopping {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("stopping", "server is shutting down")
	}
	switch c.Kind {
	case protocol.DocumentKindOpen, protocol.DocumentKindClose, protocol.DocumentKindEdit, protocol.DocumentKindTakeEdit, protocol.DocumentKindResolve, protocol.DocumentKindDismiss:
	default:
		e.mu.Unlock()
		return protocol.Receipt{}, failure("unsupported_command", "unsupported document command")
	}
	if c.ClientID == "" || len(c.ClientID) > 128 {
		e.mu.Unlock()
		return protocol.Receipt{}, failure("invalid", c.Kind+" requires a bounded ClientID")
	}
	var root string
	if c.Kind == "document.open" {
		var err error
		if root, err = documentRoot(&e.snap, c); err != nil {
			e.mu.Unlock()
			return protocol.Receipt{}, err
		}
	}
	reserved := make(chan struct{})
	ds.commands[c.ID] = reserved
	e.mu.Unlock()

	var target string
	var err error
	if c.Kind == "document.open" {
		target, err = e.openDocument(ctx, root, c.Path, c.ClientID)
	} else if c.Kind == protocol.DocumentKindDismiss {
		target, err = c.TargetID, e.dismissQuarantined(c)
	} else if a := e.docActor(c.TargetID); a == nil {
		err = failure("not_found", "document is not open")
	} else {
		target = a.id
		err = a.command(ctx, c)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	delete(ds.commands, c.ID)
	close(reserved)
	if err != nil {
		return protocol.Receipt{}, err
	}
	next := clone(e.snap)
	next.Revision++
	r := protocol.Receipt{ID: c.ID, State: "accepted", Revision: next.Revision, TargetID: target}
	if err := e.store.Save(next, &c, &r); err != nil {
		return protocol.Receipt{}, err
	}
	e.snap = next
	e.lastFlush, e.dirty = time.Now(), false
	e.publish()
	return r, nil
}

// documentRoot resolves the command's checkout directory and validates the
// path. The engine lock is held.
func documentRoot(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if (c.ThreadID == "") == (c.ProjectID == "") {
		return "", failure("invalid", "select exactly one project or thread")
	}
	if !validFilesPath(c.Path) {
		return "", failure("invalid", "path must be a relative checkout path outside .git")
	}
	root := ""
	if t := threadByID(s, c.ThreadID); c.ThreadID != "" && t != nil {
		checkout, err := threadCheckout(s, t)
		if err != nil {
			return "", err
		}
		root = checkout
	} else {
		for _, p := range s.Projects {
			if c.ProjectID != "" && p.ID == c.ProjectID {
				root = p.Path
			}
		}
		if root == "" {
			return "", failure("not_found", "workspace target not found")
		}
	}
	if strings.HasPrefix(root, "fixture://") || !strings.HasPrefix(root, "/") {
		return "", failure("unavailable", "this checkout has no local files")
	}
	return root, nil
}

// openDocument joins the loaded document for root/path or loads it from the
// file. A file that cannot be edited fails with document_read_only.
func (e *engine) openDocument(ctx context.Context, root, rel, clientID string) (string, error) {
	key := docKey{root, rel}
	for {
		e.docs.mu.Lock()
		ds := e.docsLocked()
		if ds.closed {
			e.docs.mu.Unlock()
			return "", failure("stopping", "server is shutting down")
		}
		if a := ds.byKey[key]; a != nil {
			e.docs.mu.Unlock()
			if err := a.command(ctx, protocol.Command{Kind: "document.open", ClientID: clientID}); err != nil {
				if errors.Is(err, errActorStopped) {
					continue // it unloaded meanwhile; load it again
				}
				return "", err
			}
			return a.id, nil
		}
		if wait, busy := ds.creating[key]; busy {
			e.docs.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", failure("cancelled", "the request ended before the document opened; retry with the same command ID")
			}
		}
		if len(ds.byID) >= maxDocuments {
			e.docs.mu.Unlock()
			return "", failure("capacity", "at most 64 documents can be open at once; close one first")
		}
		done := make(chan struct{})
		ds.creating[key] = done
		e.docs.mu.Unlock()
		a, err := e.createDocument(root, rel, clientID)
		e.docs.mu.Lock()
		delete(ds.creating, key)
		close(done)
		if err == nil {
			if ds.closed {
				err = failure("stopping", "server is shutting down")
			} else {
				ds.byID[a.id], ds.byKey[key] = a, a
				ds.wg.Add(1)
			}
		}
		e.docs.mu.Unlock()
		if err != nil {
			return "", err
		}
		a.publishStatus()
		go a.run()
		return a.id, nil
	}
}

// createDocument reads the file and stores a new document for it.
func (e *engine) createDocument(root, rel, clientID string) (*docActor, error) {
	obs := observeDocFile(root, rel)
	switch obs.Kind {
	case docDiskPresent:
	case docDiskAbsent:
		return nil, failure("not_found", "file is no longer present")
	case docDiskNotRegular:
		return nil, failure("document_read_only", "The path is a symlink or not a regular file; it opens read-only.")
	case docDiskLinked:
		return nil, failure("document_read_only", "The file has other hard links; it opens read-only.")
	default:
		return nil, failure("unavailable", "the file could not be read")
	}
	if obs.TooLarge {
		return nil, failure("document_read_only", "The file is larger than 1 MiB; it opens read-only.")
	}
	text, format, _, reason := decodeDocFile(obs.Data, nil)
	if reason != "" {
		return nil, failure("document_read_only", reason+" It opens read-only.")
	}
	if obs.Mode&0o200 == 0 {
		return nil, failure("document_read_only", "The file is not writable by its owner; it opens read-only.")
	}
	d, snapshot, err := doc.New(text)
	if err != nil {
		return nil, failure("document_read_only", "The file cannot be represented as a shared document.")
	}
	meta := docMeta{Token: obs.Token, SHA: docSHA(obs.Data), Mode: obs.Mode, Format: format, Openers: []string{clientID}}
	a := e.newDocActor("doc-"+ID(), root, rel, d, meta, obs.Data)
	rec := storage.DocumentRecord{ID: a.id, Root: root, Path: rel, Meta: a.encodeMeta(), Baseline: obs.Data, Snapshot: snapshot}
	if err := e.docStore().CreateDocument(rec); err != nil {
		if errors.Is(err, storage.ErrDocumentExists) {
			return nil, failure("conflict", "the document is being opened concurrently; retry")
		}
		return nil, failure("storage", "the document could not be stored")
	}
	return a, nil
}

// startDocuments loads every stored document. They start reconciling: the
// file is compared before anything is written.
func (e *engine) startDocuments() error {
	records, err := e.docStore().LoadDocuments()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.snap.Documents = nil
	e.mu.Unlock()
	var actors []*docActor
	quarantine := func(rec storage.DocumentRecord, reason string) {
		e.logf("stored document quarantined", "id", rec.ID, "reason", reason)
		if err := e.docStore().QuarantineDocument(rec.ID, reason); err != nil {
			e.logf("stored document could not be quarantined", "id", rec.ID, "error", err)
		}
	}
	for _, rec := range records {
		if rec.Damaged != "" {
			quarantine(rec, rec.Damaged)
			continue
		}
		var meta docMeta
		if err := json.Unmarshal(rec.Meta, &meta); err != nil {
			quarantine(rec, "metadata unreadable: "+err.Error())
			continue
		}
		updates := make([][]byte, len(rec.Updates))
		for i, u := range rec.Updates {
			updates[i] = u.Data
		}
		d, err := doc.Load(rec.Snapshot, updates)
		if err != nil {
			quarantine(rec, err.Error())
			continue
		}
		if len(rec.Updates) > 0 {
			meta.DurableRev = max(meta.DurableRev, rec.Updates[len(rec.Updates)-1].Rev)
		}
		a := e.newDocActor(rec.ID, rec.Root, rec.Path, d, meta, rec.Baseline)
		// A paused document stays paused until it is resolved explicitly.
		a.reconciling = meta.Pause == "" && meta.Keep == ""
		a.restarted = true
		actors = append(actors, a)
	}
	// Quarantined documents are listed as failed; their rows stay stored
	// for recovery and the file can be opened again as a new document.
	if quarantined, err := e.docStore().LoadQuarantined(); err == nil {
		for _, q := range quarantined {
			e.setDocumentStatus(protocol.DocumentStatus{ID: q.ID, Checkout: q.Root, Path: q.Path, State: protocol.DocumentStateFailed, Quarantined: true,
				Reason: "Stored document damaged: it could not be loaded, and its stored edits (possibly unsaved) are retained in quarantine. Open the file again to edit it; dismiss deletes the retained edits.",
				Error:  q.Reason, SavedRev: -1}, false)
		}
	} else {
		e.logf("quarantined documents could not be listed", "error", err)
	}
	e.docs.mu.Lock()
	ds := e.docsLocked()
	for _, a := range actors {
		ds.byID[a.id], ds.byKey[docKey{a.root, a.path}] = a, a
		ds.wg.Add(1)
	}
	e.docs.mu.Unlock()
	started := time.Now()
	for _, a := range actors {
		a.publishStatus()
		// Temporary files from saves interrupted by a crash are removed;
		// anything written by this incarnation is newer than started.
		go cleanupDocTemps(a.root, a.path, started)
		go a.run()
	}
	return nil
}

// stopDocuments commits pending updates, attempts a final bounded save and
// stops every actor. Later commands and streams are refused.
func (e *engine) stopDocuments() {
	e.docs.mu.Lock()
	ds := e.docsLocked()
	ds.closed = true
	actors := make([]*docActor, 0, len(ds.byID))
	for _, a := range ds.byID {
		actors = append(actors, a)
	}
	e.docs.mu.Unlock()
	for _, a := range actors {
		a.stop()
	}
	done := make(chan struct{})
	go func() { ds.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(docStopTimeout + time.Second):
		e.logf("document actors did not stop in time")
	}
}

// setDocumentStatus replaces the snapshot projection of one document.
func (e *engine) setDocumentStatus(st protocol.DocumentStatus, remove bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	docs := e.snap.Documents[:0:0]
	found := false
	for _, d := range e.snap.Documents {
		if d.ID == st.ID {
			found = true
			if remove {
				continue
			}
			d = st
		}
		docs = append(docs, d)
	}
	if !found && !remove {
		docs = append(docs, st)
	}
	if !found && remove {
		return
	}
	e.snap.Documents = docs
	e.snap.Revision++
	e.dirty = true
	e.maybeFlushLocked()
}

func (e *engine) unregisterDocument(a *docActor) {
	e.docs.mu.Lock()
	if e.docs.byID[a.id] == a {
		delete(e.docs.byID, a.id)
		delete(e.docs.byKey, docKey{a.root, a.path})
	}
	e.docs.mu.Unlock()
	e.setDocumentStatus(protocol.DocumentStatus{ID: a.id}, true)
}

// documentVersions serves GET /v1/documents/{id}/versions.
func (e *engine) documentVersions(id string) (protocol.DocumentVersions, error) {
	a := e.docActor(id)
	if a == nil {
		return protocol.DocumentVersions{}, failure("not_found", "document is not open")
	}
	stored, err := e.docStore().DocumentVersions(id)
	if err != nil {
		return protocol.DocumentVersions{}, failure("storage", "versions could not be read")
	}
	if len(stored) == 0 {
		return protocol.DocumentVersions{}, failure("not_found", "no versions are retained for this document")
	}
	out := protocol.DocumentVersions{ID: id, Base: stored["base"], Document: stored["document"], Disk: stored["disk"], DiskState: string(stored["disk_state"]), DiskID: string(stored["disk_id"])}
	return out, nil
}

var errActorStopped = errors.New("document actor stopped")

func (m docMeta) hasOpener(client string) bool {
	for _, o := range m.Openers {
		if o == client {
			return true
		}
	}
	return false
}

func (m *docMeta) removeOpener(client string) {
	kept := m.Openers[:0]
	for _, o := range m.Openers {
		if o != client {
			kept = append(kept, o)
		}
	}
	m.Openers = kept
}

func formatDocError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}

// dismissQuarantined deletes a quarantined document's retained storage after
// explicit confirmation.
func (e *engine) dismissQuarantined(c protocol.Command) error {
	if c.Text != protocol.DocumentDismissConfirm {
		return failure("invalid", "dismissing deletes the retained edits; confirm with Text "+protocol.DocumentDismissConfirm)
	}
	quarantined, err := e.docStore().LoadQuarantined()
	if err != nil {
		return failure("storage", "quarantined documents could not be read")
	}
	found := false
	for _, q := range quarantined {
		found = found || q.ID == c.TargetID
	}
	if !found {
		return failure("not_found", "no quarantined document with this ID")
	}
	if err := e.docStore().DeleteQuarantined(c.TargetID); err != nil {
		return failure("storage", "the quarantined document could not be deleted")
	}
	e.setDocumentStatus(protocol.DocumentStatus{ID: c.TargetID}, true)
	return nil
}
