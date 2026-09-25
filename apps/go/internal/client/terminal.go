package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Terminal lifecycle uses ordinary commands (see protocol/terminal.go):
//
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: "terminal.open", ThreadID: t, ClientID: me,
//		TerminalSize: &protocol.TerminalSize{Cols: w, Rows: h}})           // Receipt.TargetID is the terminal ID
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: "terminal.close", ThreadID: t, TargetID: id})
//	c.Command(ctx, protocol.Command{ID: client.ID(), Kind: "terminal.take-control", ThreadID: t, TargetID: id, ClientID: me})
//
// The durable record (protocol.Terminal in snapshots) carries State,
// Controller/ControlGen, Dir, Title, Cols/Rows and Exit. Screen contents,
// input and resize use a TerminalStream.

// ErrTerminalNotFound reports that the server has no such terminal record.
var ErrTerminalNotFound = errors.New("terminal not found")

// ErrTerminalStreamLimit reports that the server refused another stream
// (at most 16 per terminal and 256 in total).
var ErrTerminalStreamLimit = errors.New("too many terminal streams")

// terminalEventBuffer bounds events decoded ahead of the consumer. Screens
// are coalesced to the latest one (each is a complete grid), so only
// non-screen events count; when the buffer is full the stream stops reading
// and the server drops intermediate frames for this connection.
const terminalEventBuffer = 16

// TerminalStream is one observer/controller connection to a terminal.
//
// Events delivers server events in order, except that a screen not yet
// received by the consumer is replaced by a newer screen (never reordered
// around other events). It is closed when the stream
// ends: after an ended event (normal closure), on server stop, on a
// connection failure, when the dial context is cancelled, or after Close.
// Err then reports why (nil after ended or Close). Reconnecting is the
// caller's job: open a new stream; the server starts it with a full screen
// and the current control state, so nothing needs replaying.
//
// SendInput, Resize and RequestScrollback may be called from any goroutine,
// concurrently with each other and with reading Events. Each returns once
// the message is written to the socket; acceptance is implicit (the effect
// appears in later screen events) and refusal arrives as a rejected event.
// Send only with the Gen from the latest control event (or the snapshot's
// ControlGen) and only while this client is the controller; otherwise the
// server rejects the request.
type TerminalStream struct {
	conn   *websocket.Conn
	events chan protocol.TerminalEvent
	ctx    context.Context
	cancel context.CancelFunc

	mu  sync.Mutex
	err error
}

// OpenTerminalStream connects to terminal id as clientID (the same ClientID
// used in commands; it decides whether input is accepted). ctx bounds the
// whole stream's lifetime, not only the dial. A missing terminal is
// ErrTerminalNotFound; an ended terminal connects, delivers one ended event
// and closes.
func (c *Client) OpenTerminalStream(ctx context.Context, id, clientID string) (*TerminalStream, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.Discovery.Token)
	headers.Set("X-TUI-Protocol", "1")
	u := strings.Replace(c.Discovery.URL, "http://", "ws://", 1) + "/v1/terminals/" + url.PathEscape(id) + "/stream?client_id=" + url.QueryEscape(clientID)
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()
	conn, resp, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrTerminalNotFound, id)
		}
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return nil, ErrTerminalStreamLimit
		}
		return nil, err
	}
	// The server keeps every screen event within 8 MiB (degrading it if
	// necessary), well below this limit.
	conn.SetReadLimit(32 << 20)
	sctx, cancel := context.WithCancel(ctx)
	s := &TerminalStream{conn: conn, events: make(chan protocol.TerminalEvent), ctx: sctx, cancel: cancel}
	in := make(chan protocol.TerminalEvent)
	go s.read(in)
	go s.deliver(in)
	return s, nil
}

// deliver forwards decoded events, keeping at most one undelivered screen
// between other events.
func (s *TerminalStream) deliver(in <-chan protocol.TerminalEvent) {
	defer close(s.events)
	var queue []protocol.TerminalEvent
	for in != nil || len(queue) > 0 {
		var out chan<- protocol.TerminalEvent
		var head protocol.TerminalEvent
		if len(queue) > 0 {
			out, head = s.events, queue[0]
		}
		recv := in
		if len(queue) >= terminalEventBuffer {
			recv = nil
		}
		select {
		case ev, ok := <-recv:
			if !ok {
				in = nil
				continue
			}
			if n := len(queue); ev.Type == protocol.TerminalEventScreen && n > 0 && queue[n-1].Type == protocol.TerminalEventScreen {
				queue[n-1] = ev
			} else {
				queue = append(queue, ev)
			}
		case out <- head:
			queue = queue[1:]
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *TerminalStream) read(in chan<- protocol.TerminalEvent) {
	defer close(in)
	defer s.conn.CloseNow()
	for {
		_, b, err := s.conn.Read(s.ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure && s.ctx.Err() == nil {
				s.fail(err)
			}
			return
		}
		var ev protocol.TerminalEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			s.fail(fmt.Errorf("terminal stream: %w", err))
			return
		}
		select {
		case in <- ev:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *TerminalStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// Events returns the event channel (see TerminalStream).
func (s *TerminalStream) Events() <-chan protocol.TerminalEvent { return s.events }

// Err reports why the stream ended; valid after Events is closed.
func (s *TerminalStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// SendInput sends keystrokes or pasted bytes as the controller. Input
// larger than protocol.TerminalMaxInput is split into ordered chunks; valid
// UTF-8 travels as text, other bytes as base64.
func (s *TerminalStream) SendInput(gen int64, data []byte) error {
	for len(data) > 0 {
		n := min(len(data), protocol.TerminalMaxInput)
		// Keep a UTF-8 sequence in one chunk where possible.
		for k := 0; k < utf8.UTFMax && n < len(data) && n > 1 && !utf8.RuneStart(data[n]); k++ {
			n--
		}
		chunk := data[:n]
		data = data[n:]
		req := protocol.TerminalRequest{Type: protocol.TerminalRequestInput, Gen: gen}
		if utf8.Valid(chunk) {
			req.Data = string(chunk)
		} else {
			req.DataB64 = chunk
		}
		if err := s.send(req); err != nil {
			return err
		}
	}
	return nil
}

// SendKey sends one semantic key press as the controller; the server encodes
// it for the child's current input modes (see protocol.TerminalKey).
func (s *TerminalStream) SendKey(gen int64, k protocol.TerminalKey) error {
	return s.send(protocol.TerminalRequest{Type: protocol.TerminalRequestKey, Gen: gen, Key: &k})
}

// Paste sends text as a paste as the controller; the server brackets it when
// the child enabled bracketed paste. Text larger than
// protocol.TerminalMaxInput is split on rune boundaries into ordered pastes.
func (s *TerminalStream) Paste(gen int64, text string) error {
	for text != "" {
		n := min(len(text), protocol.TerminalMaxInput)
		for n < len(text) && n > 1 && !utf8.RuneStart(text[n]) {
			n--
		}
		if err := s.send(protocol.TerminalRequest{Type: protocol.TerminalRequestPaste, Gen: gen, Text: text[:n]}); err != nil {
			return err
		}
		text = text[n:]
	}
	return nil
}

// Resize asks the server to resize the PTY as the controller. Coalesce
// rapid resizes (for example while dragging) before calling.
func (s *TerminalStream) Resize(gen int64, cols, rows int) error {
	return s.send(protocol.TerminalRequest{Type: protocol.TerminalRequestResize, Gen: gen, Cols: cols, Rows: rows})
}

// RequestScrollback asks for n (1..protocol.TerminalMaxScrollback) history
// lines from index from (0 is the oldest; negative requests the newest n).
// Any observer may request; the answer is a scrollback event.
func (s *TerminalStream) RequestScrollback(from, n int) error {
	return s.send(protocol.TerminalRequest{Type: protocol.TerminalRequestScrollback, From: from, N: n})
}

func (s *TerminalStream) send(req protocol.TerminalRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, b)
}

// Close ends the stream without affecting the terminal session (use the
// terminal.close command to end the shell). Events is closed afterwards.
// Close is idempotent and safe to call after the stream ended.
func (s *TerminalStream) Close() error {
	_ = s.conn.Close(websocket.StatusNormalClosure, "")
	s.cancel()
	return nil
}
