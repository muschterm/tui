package acpbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Native IDs are captured before SDK callbacks can race a withdrawal. This
// transport-only ledger never adds fields to provider question payloads.
type codexRequestLedger struct {
	mu       sync.Mutex
	requests map[string]codexNativeRequest
}
type codexNativeRequest struct {
	thread, turn, item, method string
	answered, withdrawn        bool
}

type codexNativeReader struct {
	scanner *bufio.Scanner
	bridge  *codexBridge
	pending []byte
}

func newCodexNativeReader(src io.Reader, bridge *codexBridge) io.Reader {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), maxFrame+1)
	return &codexNativeReader{scanner: scanner, bridge: bridge}
}
func (r *codexNativeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		frame := r.scanner.Bytes()
		if err := r.bridge.observeNativeRequest(frame); err != nil {
			return 0, err
		}
		r.pending = append(append([]byte(nil), frame...), '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func nativeRequestID(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 1024 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	switch value.(type) {
	case string, json.Number:
	default:
		return ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (b *codexBridge) observeNativeRequest(frame []byte) error {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(frame, &message) != nil {
		return errors.New("invalid Codex native frame")
	}
	if message.Method == "serverRequest/resolved" {
		var resolved struct {
			Thread string          `json:"threadId"`
			ID     json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(message.Params, &resolved) != nil {
			return errors.New("invalid Codex request resolution")
		}
		id := nativeRequestID(resolved.ID)
		b.requests.mu.Lock()
		request, exists := b.requests.requests[id]
		withdraw := exists && request.thread == resolved.Thread && !request.answered
		if exists && request.thread == resolved.Thread {
			if withdraw {
				request.withdrawn = true
				b.requests.requests[id] = request
			} else {
				delete(b.requests.requests, id)
			}
		}
		b.requests.mu.Unlock()
		if withdraw {
			b.retirePendingApprovals()
		}
		return nil
	}
	id := nativeRequestID(message.ID)
	if message.Method == "" || len(message.ID) == 0 {
		return nil
	}
	if id == "" || len(message.Method) > 256 {
		return errors.New("invalid Codex native request identity")
	}
	var identity struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
		Item   string `json:"itemId"`
	}
	if json.Unmarshal(message.Params, &identity) != nil || len(identity.Thread) > 512 || len(identity.Turn) > 512 || len(identity.Item) > 512 {
		return errors.New("invalid Codex native request")
	}
	b.requests.mu.Lock()
	defer b.requests.mu.Unlock()
	if b.requests.requests == nil {
		b.requests.requests = make(map[string]codexNativeRequest)
	}
	if _, exists := b.requests.requests[id]; exists {
		return errors.New("duplicate pending Codex request ID")
	}
	if len(b.requests.requests) >= 1024 {
		// Some runtime versions omit resolved after a response. Completed
		// entries may be evicted: unknown resolutions and response replays
		// are ignored, while live callbacks always retain their entry.
		for key, request := range b.requests.requests {
			if request.answered {
				delete(b.requests.requests, key)
			}
		}
		if len(b.requests.requests) >= 1024 {
			return errors.New("too many unresolved Codex native requests")
		}
	}
	b.requests.requests[id] = codexNativeRequest{thread: identity.Thread, turn: identity.Turn, item: identity.Item, method: message.Method}
	return nil
}

// Hold the ledger lock through the native write and its completion mark. The
// reader can then distinguish a normal resolution even if it arrives before
// the pipe Write returns. Write deadlines remain owned by codexNativeWriter.
func (b *codexBridge) writeNativeResponse(dst io.Writer, frame []byte) (int, error) {
	var message map[string]json.RawMessage
	if json.Unmarshal(frame, &message) != nil {
		return 0, errors.New("invalid Codex outgoing frame")
	}
	_, result := message["result"]
	_, failure := message["error"]
	if !result && !failure {
		return dst.Write(frame)
	}
	id := nativeRequestID(message["id"])
	b.requests.mu.Lock()
	defer b.requests.mu.Unlock()
	request, exists := b.requests.requests[id]
	if !exists || request.answered || request.withdrawn {
		return len(frame), nil
	}
	n, err := dst.Write(frame)
	if err == nil && n == len(frame) {
		request.answered = true
		b.requests.requests[id] = request
	}
	return n, err
}
