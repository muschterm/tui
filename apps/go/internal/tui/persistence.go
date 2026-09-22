package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// viewWriter serializes saves and ignores older generations even when Bubble
// Tea schedules effects in a different order. Server CAS guards delayed HTTP
// writes and two processes deliberately sharing a named view.
type viewWriter struct {
	mu                   sync.Mutex
	connection           atomic.Pointer[client.Client]
	id                   string
	revision, generation int64
	acknowledged         []byte
	// Payloads whose PUT outcome is unknown: no reply and no matching probe.
	unconfirmed [][]byte
}

const maxUnconfirmedViews = 3

func (w *viewWriter) save(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var state savedView
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Generation < w.generation {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c := w.connection.Load()
	result, err := c.PutView(ctx, w.id, data, w.revision)
	if err == nil {
		w.acknowledge(result, state.Generation)
		return nil
	}
	// A timed-out response may have committed. Reconcile content before a new
	// write; never blindly retry with a fresh revision over another client's data.
	probe, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	actual, e := c.LoadView(probe, w.id)
	if e == nil && bytes.Equal(actual.Data, data) {
		w.acknowledge(actual, state.Generation)
		return nil
	}
	if e == nil {
		// This is a read-only probe. Never replay a pending application command
		// to discover its status: it might still be live and execute new work.
		snapshot, snapshotErr := c.Snapshot(probe)
		var live map[string]bool
		if snapshotErr == nil {
			live = make(map[string]bool, len(snapshot.Threads))
			for _, thread := range snapshot.Threads {
				live[thread.ID] = true
			}
			outgoing, projectErr := protocol.PruneThreadView(data, live)
			if projectErr == nil && bytes.Equal(actual.Data, outgoing) {
				// The first PUT can have committed a server-pruned view before
				// its acknowledgment was lost.
				w.acknowledge(actual, state.Generation)
				return nil
			}
			previous, previousErr := protocol.PruneThreadView(w.acknowledged, live)
			if projectErr == nil && previousErr == nil &&
				actual.Revision > w.revision &&
				!bytes.Equal(previous, w.acknowledged) &&
				bytes.Equal(actual.Data, previous) {
				// The server changed only the deletion projection of exactly
				// what this writer last acknowledged. Keep all current local
				// edits to surviving threads, while excluding deleted data.
				result, retryErr := c.PutView(probe, w.id, outgoing, actual.Revision)
				if retryErr == nil {
					w.acknowledge(result, state.Generation)
					return nil
				}
				err = retryErr
			}
		}
		// An earlier PUT committed without an acknowledgment or probe. The
		// stored view is then this writer's own content (or its deletion
		// projection): the missing acknowledgment, not another client's write.
		for _, sent := range w.unconfirmed {
			matched := bytes.Equal(actual.Data, sent)
			if !matched && live != nil {
				projected, projectErr := protocol.PruneThreadView(sent, live)
				matched = projectErr == nil && bytes.Equal(actual.Data, projected)
			}
			if !matched {
				continue
			}
			w.revision, w.acknowledged, w.unconfirmed = actual.Revision, bytes.Clone(actual.Data), nil
			result, retryErr := c.PutView(probe, w.id, data, actual.Revision)
			if retryErr == nil {
				w.acknowledge(result, state.Generation)
				return nil
			}
			err = retryErr
			break
		}
	}
	var rejected *protocol.Error
	if !errors.As(err, &rejected) && !slices.ContainsFunc(w.unconfirmed, func(sent []byte) bool { return bytes.Equal(sent, data) }) {
		// No server verdict: this payload may have committed.
		w.unconfirmed = append(w.unconfirmed, bytes.Clone(data))
		if len(w.unconfirmed) > maxUnconfirmedViews {
			w.unconfirmed = w.unconfirmed[1:]
		}
	}
	// Creation-command tombstones need evidence beyond live thread IDs. If a
	// pending creation was purged, preserve/export the local state as a conflict
	// rather than guessing its execution status or replaying the command.
	return fmt.Errorf("view save not confirmed; use a different --client name for simultaneous clients: %w", err)
}

func (w *viewWriter) acknowledge(view protocol.View, generation int64) {
	w.revision = view.Revision
	w.generation = generation
	w.acknowledged = bytes.Clone(view.Data)
	w.unconfirmed = nil
}
