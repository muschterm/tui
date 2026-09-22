package server

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// httpConnections releases unfinished input on shutdown while allowing handlers
// that have already read their requests to finish their writes. net/http's
// graceful Shutdown alone waits for new connections and stalled request bodies.
type httpConnections struct {
	mu       sync.Mutex
	states   map[net.Conn]http.ConnState
	draining bool
}

func (c *httpConnections) changed(conn net.Conn, state http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state == http.StateClosed || state == http.StateHijacked {
		delete(c.states, conn)
		return
	}
	if c.states == nil {
		c.states = make(map[net.Conn]http.ConnState)
	}
	c.states[conn] = state
	if c.draining {
		c.releaseInput(conn, state)
	}
}

func (c *httpConnections) drain() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.draining = true
	for conn, state := range c.states {
		c.releaseInput(conn, state)
	}
}

func (*httpConnections) releaseInput(conn net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew, http.StateIdle:
		_ = conn.Close()
	case http.StateActive:
		// A client that has not finished its body must not hold shutdown open.
		// Leave the write side available for the handler's result/receipt.
		_ = conn.SetReadDeadline(time.Now())
	}
}

func shutdownStage(stage string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return nil
}

// putView shares command admission's shutdown gate. The engine lock keeps a
// view write admitted before Stop ahead of the final save/storage close; a late
// body reader cannot begin a new write even if HTTP draining timed out.
func (e *engine) putView(id string, view protocol.View) (protocol.View, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping {
		return protocol.View{}, failure("stopping", "server is shutting down; view was not saved")
	}
	return e.store.PutView(id, view.Data, view.Revision)
}
