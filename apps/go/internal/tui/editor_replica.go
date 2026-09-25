package tui

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// editor_replica.go wraps this client's replica of a shared document
// (github.com/reearth/ygo, the server's pinned version; see
// client/document.go for the recipe). The replica holds the root text
// protocol.DocumentTextName. Every change to it, local or remote, arrives
// through the text's observer as a delta that keeps the editor's line model
// (edText) in step, so the whole text is never re-read per keystroke.
//
// Local edits and this replica's undo/redo (editor_undo.go) run in
// transactions whose origin is the replica's own token; their updates are
// collected for sending. Each local transaction is one update, so the server
// can validate it (text inserted and deleted within one update is refused).

// edOrigin tags local transactions; it is not zero-sized, so every
// allocation is a distinct origin (ygo compares origins with ==).
type edOrigin struct{ _ byte }

// remoteOrigin tags updates applied from the server.
type remoteOrigin struct{ _ byte }

var errReplicaDiverged = errors.New("replica diverged from its text model")

type docReplica struct {
	id     uint64
	doc    *crdt.Doc
	text   *crdt.YText
	origin *edOrigin
	remote *remoteOrigin
	txt    *edText
	// deltas collects observer deltas until flush; outbox collects this
	// replica's own updates until taken.
	deltas    [][]crdt.Delta
	outbox    [][]byte
	unobserve func()
	unupdate  func()
	// Own undo history (editor_undo.go).
	undos, redos [][]edOp
	redirects    []edRedirect
	budget       int
}

// newReplicaID returns a random non-zero Yjs client ID (32 bits, as Yjs
// clients use).
func newReplicaID() uint64 {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		if id := uint64(binary.BigEndian.Uint32(b[:])); id != 0 {
			return id
		}
	}
}

// newDocReplica builds a replica with client ID id from a full state update.
func newDocReplica(id uint64, state []byte) (*docReplica, error) {
	r := &docReplica{id: id, origin: &edOrigin{}, remote: &remoteOrigin{}}
	r.doc = crdt.New(crdt.WithClientID(crdt.ClientID(id)))
	r.text = r.doc.GetText(protocol.DocumentTextName)
	if len(state) > 0 {
		if err := safeApplyUpdate(r.doc, state, r.remote); err != nil {
			return nil, err
		}
	}
	if p := r.doc.PendingStats(); p.Items > 0 || p.DeleteRanges > 0 {
		return nil, errors.New("document state is incomplete")
	}
	r.txt = newEdText(r.text.ToString())
	r.unobserve = r.text.Observe(func(ev crdt.YTextEvent) {
		r.deltas = append(r.deltas, append([]crdt.Delta(nil), ev.Delta...))
	})
	r.unupdate = r.doc.OnUpdate(func(u []byte, origin any) {
		if origin == any(r.origin) {
			r.outbox = append(r.outbox, append([]byte(nil), u...))
		}
	})
	return r, nil
}

// guard turns a panic inside the CRDT library into an error, so a failing
// replica takes the recovery path instead of ending the program.
func (r *docReplica) guard(err *error) {
	if p := recover(); p != nil {
		r.deltas = r.deltas[:0]
		*err = fmt.Errorf("replica failed: %v", p)
	}
}

// close releases the replica's subscriptions.
func (r *docReplica) close() {
	if r == nil {
		return
	}
	r.unobserve()
	r.unupdate()
}

func safeApplyUpdate(d *crdt.Doc, update []byte, origin any) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("update panicked: %v", p)
		}
	}()
	return crdt.ApplyUpdateV1(d, update, origin)
}

// flush applies the collected deltas to the line model and returns the
// UTF-16 offset just after the last change (-1 when nothing changed).
func (r *docReplica) flush() (int, error) {
	last := -1
	for _, delta := range r.deltas {
		edits := deltaEdits(delta)
		shift := 0
		for _, e := range edits {
			last = e.at + shift + u16Len(e.text)
			shift += u16Len(e.text) - e.del
		}
		r.txt.apply(edits)
	}
	r.deltas = r.deltas[:0]
	if r.txt.Len() != r.text.Len() {
		// Never keep painting a text that differs from the replica.
		r.txt = newEdText(r.text.ToString())
		return last, errReplicaDiverged
	}
	return last, nil
}

// takeUpdates returns and clears this replica's pending own updates.
func (r *docReplica) takeUpdates() [][]byte {
	out := r.outbox
	r.outbox = nil
	return out
}

// edit replaces UTF-16 range [at, at+del) with text: a delete and an
// insert, each its own transaction and queued update. join adds them to the
// current undo step instead of starting a new one.
func (r *docReplica) edit(at, del int, text string, join bool) (err error) {
	if !utf8.ValidString(text) {
		return errors.New("text is not valid UTF-8")
	}
	defer r.guard(&err)
	var ops []edOp
	if del > 0 {
		var op edOp
		op, err = r.doDelete(at, del, true)
		ops = append(ops, op)
	}
	if err == nil && text != "" {
		var op edOp
		op, err = r.doInsert(at, text)
		ops = append(ops, op)
	}
	r.record(ops, join)
	return err
}

// applyRemote applies a server update, keeping each offset in keep (UTF-16)
// anchored to the text around it through relative positions, and returns
// them remapped.
func (r *docReplica) applyRemote(update []byte, keep []int) (_ []int, err error) {
	defer r.guard(&err)
	anchors := make([]crdt.RelativePosition, len(keep))
	for i, u := range keep {
		anchors[i] = crdt.CreateRelativePositionFromIndex(r.text, min(max(0, u), r.text.Len()), 0)
	}
	if err := safeApplyUpdate(r.doc, update, r.remote); err != nil {
		r.deltas = r.deltas[:0]
		return keep, err
	}
	if p := r.doc.PendingStats(); p.Items > 0 || p.DeleteRanges > 0 {
		_, _ = r.flush()
		return keep, errors.New("update depends on state this replica does not have")
	}
	if _, err := r.flush(); err != nil {
		return keep, err
	}
	out := make([]int, len(keep))
	for i, rp := range anchors {
		out[i] = keep[i]
		if abs, ok := crdt.ToAbsolutePosition(r.doc, rp); ok {
			out[i] = abs.Index
		}
		out[i] = min(max(0, out[i]), r.text.Len())
	}
	return out, nil
}
