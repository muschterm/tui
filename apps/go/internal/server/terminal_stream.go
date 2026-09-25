package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/term"
)

const (
	// terminalReadLimit admits one maximal input request: base64 (4/3) or
	// JSON-escaped control bytes (up to 6×) of TerminalMaxInput.
	terminalReadLimit = 6*protocol.TerminalMaxInput + 4096
	// Stream connections are bounded per terminal and in total; each holds
	// goroutines, a socket and frame copies.
	maxStreamsPerTerminal = 16
	maxStreams            = 256
	// terminalWriteTimeout drops a stream whose reader stopped reading; the
	// session and other streams never wait on it.
	terminalWriteTimeout = 10 * time.Second
)

// terminalStream serves GET /v1/terminals/{id}/stream?client_id=… (see
// protocol/terminal.go). Any authenticated client may observe; only the
// controller's input and resize are applied. The HTTP layer has already
// checked the bearer token, protocol header and browser origin.
func (e *engine) terminalStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" || len(clientID) > 128 {
		writeJSONError(w, http.StatusBadRequest, "invalid", "a bounded client_id query parameter is required")
		return
	}
	e.mu.Lock()
	var record protocol.Terminal
	rec := terminalByID(&e.snap, id)
	if rec != nil {
		record = *rec
	}
	ts := e.terminalsLocked()
	s := ts.sessions[id]
	admitted := rec != nil && ts.streams[id] < maxStreamsPerTerminal && ts.streams[""] < maxStreams
	if admitted {
		ts.streams[id]++
		ts.streams[""]++
	}
	e.mu.Unlock()
	if rec == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "terminal missing")
		return
	}
	if !admitted {
		writeJSONError(w, http.StatusTooManyRequests, "capacity", "too many terminal streams; close another view first")
		return
	}
	defer func() {
		e.mu.Lock()
		if ts.streams[id]--; ts.streams[id] <= 0 {
			delete(ts.streams, id)
		}
		ts.streams[""]--
		e.mu.Unlock()
	}()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(terminalReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if s == nil {
		// No session in this server incarnation: report the recorded end.
		reason := record.EndReason
		if reason == "" {
			reason = protocol.TerminalEndServerRestarted
		}
		if writeTerminalEvent(ctx, conn, protocol.TerminalEvent{Type: protocol.TerminalEventEnded, Ended: &protocol.TerminalEnded{Exit: record.Exit, Reason: reason}}) == nil {
			conn.Close(websocket.StatusNormalClosure, "terminal ended")
		}
		return
	}

	wake := s.subscribe()
	defer s.unsubscribe(wake)
	replies := make(chan protocol.TerminalEvent, 8)
	go func() {
		defer cancel()
		s.readRequests(ctx, conn, clientID, replies)
	}()
	var sentSeq uint64
	sentFrame := false
	sentControl := protocol.TerminalControl{Gen: -1}
	for {
		frame, seq, control, ended := s.view()
		if frame != nil && (!sentFrame || seq != sentSeq) {
			if writeTerminalFrame(ctx, conn, frame) != nil {
				return
			}
			sentFrame, sentSeq = true, seq
		}
		if ended != nil {
			if writeTerminalEvent(ctx, conn, protocol.TerminalEvent{Type: protocol.TerminalEventEnded, Ended: ended}) == nil {
				conn.Close(websocket.StatusNormalClosure, "terminal ended")
			}
			return
		}
		if control != sentControl {
			if writeTerminalEvent(ctx, conn, protocol.TerminalEvent{Type: protocol.TerminalEventControl, Control: &control}) != nil {
				return
			}
			sentControl = control
		}
		select {
		case <-ctx.Done():
			if e.baseContext().Err() != nil {
				conn.Close(websocket.StatusGoingAway, "server stopped")
			}
			return
		case <-wake:
		case ev := <-replies:
			if writeTerminalEvent(ctx, conn, ev) != nil {
				return
			}
		}
	}
}

// readRequests applies client requests until the connection fails. Replies
// go through the single stream writer; a full reply queue back-pressures
// only this client.
func (s *termSession) readRequests(ctx context.Context, conn *websocket.Conn, clientID string, replies chan<- protocol.TerminalEvent) {
	reply := func(ev protocol.TerminalEvent) bool {
		select {
		case replies <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		typ, b, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req protocol.TerminalRequest
		if typ != websocket.MessageText || json.Unmarshal(b, &req) != nil {
			if !reply(rejected(protocol.TerminalRejectInvalid, "")) {
				return
			}
			continue
		}
		var reason string
		var written int
		switch req.Type {
		case protocol.TerminalRequestInput:
			data := []byte(req.Data)
			if len(req.DataB64) > 0 {
				if req.Data != "" {
					reason = protocol.TerminalRejectInvalid
					break
				}
				data = req.DataB64
			}
			reason, written = s.input(clientID, req.Gen, data)
		case protocol.TerminalRequestKey:
			reason, written = s.key(clientID, req.Gen, req.Key)
		case protocol.TerminalRequestPaste:
			reason, written = s.paste(clientID, req.Gen, req.Text)
		case protocol.TerminalRequestResize:
			reason, written = s.resize(clientID, req.Gen, req.Cols, req.Rows)
		case protocol.TerminalRequestScrollback:
			if !reply(s.scrollback(req.From, req.N)) {
				return
			}
			continue
		default:
			reason = protocol.TerminalRejectInvalid
		}
		// Request payloads are dropped here; nothing about input is logged.
		ev := rejected(reason, req.Type)
		ev.Rejected.Written = written
		if reason != "" && !reply(ev) {
			return
		}
	}
}

// key encodes one semantic key press for the child's current input modes
// (term.Session.SendKey) under the same controller checks as input.
func (s *termSession) key(clientID string, gen int64, k *protocol.TerminalKey) (string, int) {
	if k == nil || len(k.Text) > protocol.TerminalMaxKeyText {
		return protocol.TerminalRejectInvalid, 0
	}
	return s.controlled(clientID, gen, func(sess *term.Session) error {
		return sess.SendKey(term.Key{Code: k.Code, Text: k.Text, Mods: term.KeyMods(k.Mods)})
	})
}

// paste sends text as a paste, bracketed when the child enabled bracketed
// paste mode (term.Session.Paste).
func (s *termSession) paste(clientID string, gen int64, text string) (string, int) {
	if len(text) > protocol.TerminalMaxInput {
		return protocol.TerminalRejectTooLarge, 0
	}
	if !utf8.ValidString(text) {
		return protocol.TerminalRejectInvalid, 0
	}
	return s.controlled(clientID, gen, func(sess *term.Session) error { return sess.Paste(text) })
}

// controlled runs write when clientID controls the running session with gen,
// mapping errors to rejection reasons (with the bytes delivered before a
// busy timeout). ended is reported only when the session has ended. Nothing
// about the input is logged.
func (s *termSession) controlled(clientID string, gen int64, write func(*term.Session) error) (string, int) {
	s.mu.Lock()
	reason := s.authorizeLocked(clientID, gen)
	sess := s.sess
	s.mu.Unlock()
	if reason != "" {
		return reason, 0
	}
	err := write(sess)
	var busy *term.BusyError
	switch {
	case err == nil:
		return "", 0
	case errors.Is(err, term.ErrInvalidKey):
		return protocol.TerminalRejectInvalid, 0
	case errors.Is(err, term.ErrInputTooLarge):
		return protocol.TerminalRejectTooLarge, 0
	case errors.As(err, &busy):
		return protocol.TerminalRejectBusy, busy.Written
	case errors.Is(err, term.ErrClosed):
		return protocol.TerminalRejectEnded, 0
	}
	select {
	case <-sess.Done():
		return protocol.TerminalRejectEnded, 0
	default:
		return protocol.TerminalRejectFailed, 0
	}
}

func writeTerminalEvent(ctx context.Context, conn *websocket.Conn, ev protocol.TerminalEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return writeTerminalFrame(ctx, conn, b)
}

func writeTerminalFrame(ctx context.Context, conn *websocket.Conn, b []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, terminalWriteTimeout)
	defer cancel()
	err := conn.Write(writeCtx, websocket.MessageText, b)
	if errors.Is(err, context.DeadlineExceeded) {
		conn.Close(websocket.StatusPolicyViolation, "slow terminal stream")
	}
	return err
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.Error{Code: code, Message: message})
}
