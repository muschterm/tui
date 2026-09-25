package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

const (
	// docStreamBuffer bounds events queued for one connection; a connection
	// that falls this far behind is dropped (it reconnects and resyncs), so
	// a slow reader never delays the document or other observers.
	docStreamBuffer = 1024
	// docReadLimit admits one maximal update as base64 JSON.
	docReadLimit          = doc.MaxUpdate*4/3 + 4096
	maxDocStreamsPerDoc   = 16
	maxDocStreams         = 256
	docStreamWriteTimeout = 10 * time.Second
)

// docStream is one connection. out and kill are written and closed only by
// the actor goroutine; gone is actor-owned too.
type docStream struct {
	clientID string
	replica  uint64
	out      chan protocol.DocumentEvent
	kill     chan struct{}
	gone     bool
	// holdOp: after an unavailable rejection, only a resend of this op is
	// accepted next (later ones may depend on it).
	holdOp string
	// holdRetry is the RetryAfterMs repeated while holding (rate limit).
	holdRetry int
	// ending: a resync was required; no more updates are applied and the
	// stream closes once its earlier ops are answered.
	ending bool

	// peer identifies the stream in presence. presPending (latest per peer)
	// is filled by the actor and drained by the stream writer; presence
	// never occupies the update queue, so it cannot delay or drop updates.
	peer        string
	presMu      sync.Mutex
	presPending map[string]protocol.DocumentPeer
	presWake    chan struct{}
	// Actor-owned presence and rate-limit state.
	color     int
	presence  *protocol.DocumentPeer
	presAt    time.Time
	presDirty bool
	presSent  time.Time
	tokens    float64
	tokensAt  time.Time
	reqTokens float64
	reqAt     time.Time
}

// deliverPresence queues a peer's latest presence for this stream.
func (s *docStream) deliverPresence(p protocol.DocumentPeer) {
	s.presMu.Lock()
	s.presPending[p.Peer] = p
	s.presMu.Unlock()
	select {
	case s.presWake <- struct{}{}:
	default:
	}
}

func (s *docStream) takePresence() []protocol.DocumentPeer {
	s.presMu.Lock()
	defer s.presMu.Unlock()
	out := make([]protocol.DocumentPeer, 0, len(s.presPending))
	for _, p := range s.presPending {
		out = append(out, p)
	}
	clear(s.presPending)
	sort.Slice(out, func(i, j int) bool { return out[i].Peer < out[j].Peer })
	return out
}

// documentStream serves GET /v1/documents/{id}/stream?client_id=…&replica=…
// (see protocol/document.go). The HTTP layer has already checked the bearer
// token, protocol header and browser origin.
func (e *engine) documentStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	clientID := q.Get("client_id")
	if clientID == "" || len(clientID) > 128 {
		writeJSONError(w, http.StatusBadRequest, "invalid", "a bounded client_id query parameter is required")
		return
	}
	var replica uint64
	if v := q.Get("replica"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid", "replica must be a positive decimal Yjs client ID")
			return
		}
		replica = n
	}
	a := e.docActor(id)
	if a == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "document is not open")
		return
	}
	e.docs.mu.Lock()
	ds := e.docsLocked()
	admitted := !ds.closed && ds.streams[id] < maxDocStreamsPerDoc && ds.streams[""] < maxDocStreams
	if admitted {
		ds.streams[id]++
		ds.streams[""]++
	}
	e.docs.mu.Unlock()
	if !admitted {
		writeJSONError(w, http.StatusTooManyRequests, "capacity", "too many document streams; close another view first")
		return
	}
	defer func() {
		e.docs.mu.Lock()
		if ds.streams[id]--; ds.streams[id] <= 0 {
			delete(ds.streams, id)
		}
		ds.streams[""]--
		e.docs.mu.Unlock()
	}()
	s := &docStream{clientID: clientID, replica: replica, out: make(chan protocol.DocumentEvent, docStreamBuffer), kill: make(chan struct{}),
		peer: "peer-" + ID()[:12], presPending: map[string]protocol.DocumentPeer{}, presWake: make(chan struct{}, 1)}
	reply := make(chan error, 1)
	if !a.post(docJoinMsg{s, reply}) {
		writeJSONError(w, http.StatusNotFound, "not_found", "document is not open")
		return
	}
	var joinErr error
	select {
	case joinErr = <-reply:
	case <-a.quit:
		joinErr = failure("not_found", "document is not open")
	}
	if joinErr != nil {
		var pe *protocol.Error
		status := http.StatusConflict
		if errors.As(joinErr, &pe) && pe.Code == "not_found" {
			status = http.StatusNotFound
		} else if errors.As(joinErr, &pe) && pe.Code == "capacity" {
			status = http.StatusTooManyRequests
		}
		writeFailure(w, status, joinErr)
		return
	}
	defer a.post(docLeaveMsg{s})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(docReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var req protocol.DocumentRequest
			if typ != websocket.MessageText || json.Unmarshal(b, &req) != nil {
				req = protocol.DocumentRequest{Type: "invalid"}
			}
			select {
			case a.inbox <- docRequestMsg{s, req}:
			case <-a.quit:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	stateSent := false
	var presWake chan struct{}
	for {
		// Until the state is written, presence waits (the wake is re-armed
		// after the state).
		presWake = s.presWake
		if !stateSent {
			presWake = nil
		}
		select {
		case ev, ok := <-s.out:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "document closed")
				return
			}
			if writeDocEvent(ctx, conn, ev) != nil {
				return
			}
			if ev.Type == protocol.DocumentEventState && !stateSent {
				// Presence resolves against the replica, so it follows the
				// first state.
				stateSent = true
				select {
				case s.presWake <- struct{}{}:
				default:
				}
			}
		case <-presWake:
			// Updates queued before the presence are written first, so a
			// cursor never precedes the edit it refers to.
			for drained := false; !drained; {
				select {
				case ev, ok := <-s.out:
					if !ok {
						conn.Close(websocket.StatusNormalClosure, "document closed")
						return
					}
					if writeDocEvent(ctx, conn, ev) != nil {
						return
					}
				default:
					drained = true
				}
			}
			if peers := s.takePresence(); len(peers) > 0 {
				if writeDocEvent(ctx, conn, protocol.DocumentEvent{Type: protocol.DocumentEventPresence, Presence: peers}) != nil {
					return
				}
			}
		case <-s.kill:
			conn.Close(websocket.StatusPolicyViolation, "resync required: slow client")
			return
		case <-a.quit:
			// Deliver what the actor queued before it ended (a closed event
			// is followed by closing out).
			for {
				select {
				case ev, ok := <-s.out:
					if !ok {
						conn.Close(websocket.StatusNormalClosure, "document closed")
						return
					}
					if writeDocEvent(ctx, conn, ev) != nil {
						return
					}
					continue
				default:
				}
				break
			}
			conn.Close(websocket.StatusGoingAway, "server stopped")
			return
		case <-ctx.Done():
			if e.baseContext().Err() != nil {
				conn.Close(websocket.StatusGoingAway, "server stopped")
			}
			return
		}
	}
}

func writeDocEvent(ctx context.Context, conn *websocket.Conn, ev protocol.DocumentEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, docStreamWriteTimeout)
	defer cancel()
	err = conn.Write(writeCtx, websocket.MessageText, b)
	if errors.Is(err, context.DeadlineExceeded) {
		conn.Close(websocket.StatusPolicyViolation, "slow document stream")
	}
	return err
}

// documentVersionsHandler serves GET /v1/documents/{id}/versions.
func (e *engine) documentVersionsHandler(w http.ResponseWriter, r *http.Request) {
	v, err := e.documentVersions(r.PathValue("id"))
	if err != nil {
		var pe *protocol.Error
		status := http.StatusServiceUnavailable
		if errors.As(err, &pe) && pe.Code == "not_found" {
			status = http.StatusNotFound
		}
		writeFailure(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
