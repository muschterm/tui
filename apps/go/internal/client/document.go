package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Shared documents (see protocol/document.go). Lifecycle uses commands:
//
//	r, _ := c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindOpen,
//		ThreadID: t, Path: "src/main.go", ClientID: me})                  // r.TargetID is the document ID
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindEdit, TargetID: id, ClientID: me})
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindTakeEdit, TargetID: id, ClientID: me})
//	v, _ := c.DocumentVersions(ctx, id)                                   // review base/document/disk
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindResolve, TargetID: id, ClientID: me,
//		Text: protocol.DocumentResolveKeepDocument, Revision: status.DurableRev, DocumentDisk: v.DiskID})
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: protocol.DocumentKindClose, TargetID: id, ClientID: me})
//
// A failed open with code document_read_only means the file must be shown
// through the read-only Files view. Content, acknowledgements and status
// travel on a DocumentStream.
//
// Editing with a replica (github.com/reearth/ygo/crdt, pinned v1.50.0):
//
//  1. Create the replica with a fresh random client ID
//     (crdt.New(crdt.WithClientID(id))) and open the stream with that ID as
//     replica. The server binds the ID to your ClientID while a stream uses
//     it and releases the binding when the last such stream closes. Never
//     reuse an ID for a different replica.
//  2. Apply the first state event's Update (crdt.ApplyUpdateV1) and use the
//     root text protocol.DocumentTextName. Remember its Rev.
//  3. Edit in transactions tagged with a local origin; capture each
//     transaction's update with Doc.OnUpdate and send exactly that update
//     with a fresh op ID and the current EditGen (SendUpdate). Every new
//     character must be visible text in that update: a transaction that
//     inserts and deletes the same text, formatting, embeds and other roots
//     are refused. Normalize line endings before inserting: replace "\r\n"
//     and lone "\r" with "\n" (the server rejects a CR before an LF and
//     restores CRLF files itself). Keep each update, in order, until its ack
//     arrives; resending the same bytes is safe.
//  4. Apply every update event (other clients and server merges of disk
//     changes) with crdt.ApplyUpdateV1. Your own updates come back only as
//     acks, never as update events.
//  5. rejected with Resync true (invalid, origin, content, surrogate_split,
//     too_large, rejected, not_editor, stale_generation, resync_pending) is
//     the ONE recovery path for a diverged replica:
//     a. The rejected update was not applied, and the server applies no
//        later update from this stream (later ops get resync_pending). Every
//        earlier op is still answered: acks arrive once durable. Then the
//        server closes the stream normally (Events closes, Err is nil).
//     b. Keep the replica's text as a draft (Text of the old replica); do
//        not send anything more on this stream.
//     c. Discard the replica. Create a NEW replica with a NEW random client
//        ID and open a NEW stream (the old binding is released when the old
//        stream closes). Apply its state event.
//     d. Every acked op is in that state; unacknowledged edits are not.
//        Re-apply the difference between the draft and the new state's text
//        as fresh transactions on the new replica (for example a line or
//        character diff of the two texts), after showing it to the user when
//        it cannot be applied cleanly. Never drop the draft silently. Edits
//        that the server refused for their content (NUL, over 1 MiB, CR
//        before LF, split surrogates) must be corrected before re-applying.
//  6. rejected with reason unavailable (Resync false: storage failing, the
//     16 MiB pending bound, or the server stopping) means that update was
//     NOT applied and your replica is still valid. The server then refuses
//     every later update on this stream (also unavailable) until you resend
//     the refused op. Stop sending, wait for a status event whose State is
//     not failed (or for the stream to close, then reconnect as in 7), and
//     resend all unacknowledged updates in their original order starting
//     with the refused op, with their original op IDs and bytes.
//  7. After a connection loss (Events closed with an error, or a server
//     stop), open a new stream with the SAME replica ID, apply its state
//     event to the existing replica (or send Sync with the replica's state
//     vector) and resend unacknowledged updates in order. A replica ID
//     bound to another client fails with replica_conflict: recover as in 5.
//
// Offsets in the replica are UTF-16 code units; keep cursors on grapheme
// boundaries and never split a surrogate pair (the server rejects it).
// Undo is the replica's own (in-memory) UndoManager scoped to the local
// origin; it does not survive a restart or a replica rebuild.
//
// Quarantined documents (DocumentStatus.Quarantined) cannot be streamed or
// edited: open the file again for a new document, or delete the retained
// data with document.dismiss (ClientID, Text = protocol.DocumentDismissConfirm).

// ErrDocumentNotFound reports that the server has no such loaded document;
// open it again with document.open (the old ID is gone).
var ErrDocumentNotFound = errors.New("document not found")

// ErrDocumentStreamLimit reports that the server refused another stream.
var ErrDocumentStreamLimit = errors.New("too many document streams")

// DocumentStream is one connection to a document.
//
// Events delivers server events in order and is never coalesced (updates
// depend on each other). If the consumer stops reading, the server
// eventually drops the connection (policy violation) and Err reports it;
// reconnect then. Events is closed when the stream ends: after a closed
// event, on server stop, on connection failure, when ctx ends, or after
// Close. SendUpdate and Sync may be called from any goroutine.
type DocumentStream struct {
	conn   *websocket.Conn
	events chan protocol.DocumentEvent
	ctx    context.Context
	cancel context.CancelFunc

	mu  sync.Mutex
	err error
}

// OpenDocumentStream connects to document id as clientID. replica is the
// Yjs client ID of the caller's replica, or 0 for an observer that never
// sends updates. ctx bounds the stream's lifetime. A replica ID bound to
// another client fails with a protocol.Error (replica_conflict).
func (c *Client) OpenDocumentStream(ctx context.Context, id, clientID string, replica uint64) (*DocumentStream, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.Discovery.Token)
	headers.Set("X-TUI-Protocol", "1")
	q := url.Values{"client_id": {clientID}}
	if replica != 0 {
		q.Set("replica", strconv.FormatUint(replica, 10))
	}
	u := strings.Replace(c.Discovery.URL, "http://", "ws://", 1) + "/v1/documents/" + url.PathEscape(id) + "/stream?" + q.Encode()
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	conn, resp, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("%w: %s", ErrDocumentNotFound, id)
			case http.StatusTooManyRequests:
				return nil, ErrDocumentStreamLimit
			case http.StatusConflict, http.StatusBadRequest:
				var pe protocol.Error
				if resp.Body != nil && json.NewDecoder(resp.Body).Decode(&pe) == nil && pe.Code != "" {
					return nil, &pe
				}
			}
		}
		return nil, err
	}
	// A full state of a 1 MiB document with a long delete history stays far
	// below this limit.
	conn.SetReadLimit(64 << 20)
	sctx, cancel := context.WithCancel(ctx)
	s := &DocumentStream{conn: conn, events: make(chan protocol.DocumentEvent, 16), ctx: sctx, cancel: cancel}
	go s.read()
	return s, nil
}

func (s *DocumentStream) read() {
	defer close(s.events)
	defer s.conn.CloseNow()
	for {
		_, b, err := s.conn.Read(s.ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure && s.ctx.Err() == nil {
				s.fail(err)
			}
			return
		}
		var ev protocol.DocumentEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			s.fail(fmt.Errorf("document stream: %w", err))
			return
		}
		select {
		case s.events <- ev:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *DocumentStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// Events returns the event channel (see DocumentStream).
func (s *DocumentStream) Events() <-chan protocol.DocumentEvent { return s.events }

// Err reports why the stream ended; valid after Events is closed.
func (s *DocumentStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// SendUpdate sends one replica update as the editor. op (at most 128 bytes)
// identifies it in the ack or rejected event; gen is the current EditGen.
func (s *DocumentStream) SendUpdate(op string, gen int64, update []byte) error {
	return s.send(protocol.DocumentRequest{Type: protocol.DocumentRequestUpdate, Op: op, Gen: gen, Update: update})
}

// Sync asks for the update a replica with the encoded state vector sv is
// missing; the answer is a sync event.
func (s *DocumentStream) Sync(sv []byte) error {
	return s.send(protocol.DocumentRequest{Type: protocol.DocumentRequestSync, StateVector: sv})
}

func (s *DocumentStream) send(req protocol.DocumentRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// Close ends the stream without closing the document (use document.close).
// It is idempotent.
func (s *DocumentStream) Close() error {
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
	s.cancel()
	return nil
}

// DocumentVersions fetches the base, document and disk versions retained
// for a paused document.
func (c *Client) DocumentVersions(ctx context.Context, id string) (protocol.DocumentVersions, error) {
	var out protocol.DocumentVersions
	err := c.request(ctx, http.MethodGet, "/v1/documents/"+url.PathEscape(id)+"/versions", nil, &out)
	return out, err
}
