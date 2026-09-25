package tui

import (
	"errors"

	"github.com/reearth/ygo/crdt"
)

// editor_undo.go is this client's own undo/redo. ygo v1.50.0's UndoManager
// is not used: it merges steps incorrectly and, after an undo, later inserts
// can resolve stale positions and split surrogate pairs. Instead every local
// transaction records what it did as operations anchored in the CRDT:
//
//   - an insert is the clock range of the characters this replica created
//     (with the text, for character boundaries); undoing it deletes those of
//     them that still exist, so text others deleted meanwhile is skipped and
//     text others inserted is never touched;
//   - a delete is the removed text with anchors at its surviving neighbours
//     (relative positions) and the IDs of the removed characters; undoing it
//     inserts the text again there as new characters, and a redirect maps the
//     removed IDs of this replica's own characters to the new ones, so an
//     earlier insert step still finds its text after it was deleted and
//     restored (type, delete, undo, undo restores the original).
//
// Undoing a step runs the inverse of each operation in reverse order, each as
// an ordinary forward transaction (so each update is plain visible text and
// deletions), and records those as the step's redo; redo does the same in
// the other direction. Remote edits are never recorded.

// edIDLimit bounds how many removed characters' IDs a delete records; a
// larger deletion still undoes correctly but its restored text is not
// linked back to earlier insert steps.
const edIDLimit = 1024

// edHistory bounds the undo and redo stacks (steps).
const edHistory = 1000

// edResolveBudget bounds position lookups per undo step (each walks the
// document); a step needing more is refused rather than blocking input.
const edResolveBudget = 2048

var errUndoTooLarge = errors.New("undo step too large")

type edID struct {
	client uint64
	clock  uint64
}

// edIDSpan is n consecutive units starting at offset off (UTF-16) of an
// operation's text, held by consecutive clocks of one client.
type edIDSpan struct {
	off    int
	client uint64
	clock  uint64
	n      int
}

type edOp struct {
	ins bool
	// clock is an insert's first clock (this replica's client).
	clock uint64
	text  string
	// A delete's surviving neighbours and removed characters.
	left, right crdt.RelativePosition
	at          int
	ids         []edIDSpan
}

// edRedirect maps n removed own clocks from..from+n to restored clocks to.
type edRedirect struct {
	from, to uint64
	n        uint64
}

// ownClock returns the next clock of this replica's client.
func (r *docReplica) ownClock() uint64 { return r.doc.StateVector()[crdt.ClientID(r.id)] }

// idsAt records the IDs of UTF-16 units [at, at+n) (nil past edIDLimit).
func (r *docReplica) idsAt(at, n int) []edIDSpan {
	if n > edIDLimit {
		return nil
	}
	var out []edIDSpan
	for i := 0; i < n; i++ {
		rp := crdt.CreateRelativePositionFromIndex(r.text, at+i, 0)
		if rp.Item == nil {
			break
		}
		id := edID{uint64(rp.Item.Client), rp.Item.Clock}
		if k := len(out) - 1; k >= 0 && out[k].client == id.client && out[k].clock+uint64(out[k].n) == id.clock && out[k].off+out[k].n == i {
			out[k].n++
			continue
		}
		out = append(out, edIDSpan{off: i, client: id.client, clock: id.clock, n: 1})
	}
	return out
}

// doInsert inserts text at UTF-16 index at in one transaction and returns
// the operation.
func (r *docReplica) doInsert(at int, text string) (edOp, error) {
	clock := r.ownClock()
	r.doc.Transact(func(txn *crdt.Transaction) { r.text.Insert(txn, at, text, nil) }, r.origin)
	_, err := r.flush()
	return edOp{ins: true, clock: clock, text: text}, err
}

// doDelete deletes UTF-16 range [at, at+n) in one transaction and returns
// the operation.
func (r *docReplica) doDelete(at, n int, withIDs bool) (edOp, error) {
	t := r.txt
	op := edOp{text: t.slice(t.posAt(at), t.posAt(at+n)), at: at}
	op.left = crdt.CreateRelativePositionFromIndex(r.text, at, -1)
	op.right = crdt.CreateRelativePositionFromIndex(r.text, at+n, 0)
	if withIDs {
		op.ids = r.idsAt(at, n)
	}
	r.doc.Transact(func(txn *crdt.Transaction) { r.text.Delete(txn, at, n) }, r.origin)
	_, err := r.flush()
	return op, err
}

// follow maps an own clock through the redirects to the clock now holding
// its text, or reports that it no longer exists.
func (r *docReplica) follow(clock uint64, deleted *crdt.IDSet) (uint64, bool) {
	own := crdt.ClientID(r.id)
	for range len(r.redirects) + 1 {
		if !deleted.Has(own, clock) {
			return clock, true
		}
		moved := false
		for i := len(r.redirects) - 1; i >= 0; i-- {
			rd := r.redirects[i]
			if clock >= rd.from && clock < rd.from+rd.n {
				clock, moved = rd.to+(clock-rd.from), true
				break
			}
		}
		if !moved {
			return 0, false
		}
	}
	return 0, false
}

// indexOf resolves an existing character's current UTF-16 index.
func (r *docReplica) indexOf(client, clock uint64) (int, bool) {
	r.budget--
	abs, ok := crdt.ToAbsolutePosition(r.doc, crdt.RelativePosition{Item: &crdt.ID{Client: crdt.ClientID(client), Clock: clock}})
	return abs.Index, ok
}

// anchorIndex resolves a delete's insertion point: a surviving neighbour
// (following this replica's redirects), else the recorded index.
func (r *docReplica) anchorIndex(op edOp, deleted *crdt.IDSet) int {
	resolve := func(rp crdt.RelativePosition) (int, bool) {
		if rp.Item == nil {
			abs, ok := crdt.ToAbsolutePosition(r.doc, rp)
			return abs.Index, ok
		}
		id := *rp.Item
		if deleted.Has(id.Client, id.Clock) {
			if uint64(id.Client) != r.id {
				return 0, false
			}
			c, ok := r.follow(id.Clock, deleted)
			if !ok {
				return 0, false
			}
			id.Clock = c
		}
		abs, ok := crdt.ToAbsolutePosition(r.doc, crdt.RelativePosition{Item: &id, Assoc: rp.Assoc})
		return abs.Index, ok
	}
	i, ok := resolve(op.right)
	if !ok {
		i, ok = resolve(op.left)
	}
	if !ok {
		i = op.at
	}
	// Never split a character, whatever the anchor resolved to.
	i = min(max(0, i), r.text.Len())
	return r.txt.offset(r.txt.posAt(i))
}

// edRange is a UTF-16 range to delete with the own clocks it holds.
type edRange struct {
	at, n int
	clock uint64
}

// liveRanges returns the current ranges of an insert's characters that still
// exist (through redirects), splitting where other text lies between them.
func (r *docReplica) liveRanges(op edOp, deleted *crdt.IDSet) []edRange {
	// Unit boundaries of the text's runes, so no range splits a pair.
	var bounds []int
	u := 0
	for _, ru := range op.text {
		bounds = append(bounds, u)
		if ru >= 0x10000 {
			u += 2
		} else {
			u++
		}
	}
	bounds = append(bounds, u)
	// Group runes into runs of consecutive live clocks.
	type run struct {
		b0, b1 int // indices into bounds
		clock  uint64
	}
	var runs []run
	for i := 0; i+1 < len(bounds); i++ {
		c, ok := r.follow(op.clock+uint64(bounds[i]), deleted)
		if !ok {
			continue
		}
		if k := len(runs) - 1; k >= 0 && runs[k].b1 == i && runs[k].clock+uint64(bounds[i]-bounds[runs[k].b0]) == c {
			runs[k].b1 = i + 1
			continue
		}
		runs = append(runs, run{i, i + 1, c})
	}
	var out []edRange
	var split func(b0, b1 int, clock uint64)
	split = func(b0, b1 int, clock uint64) {
		if r.budget < 0 {
			return
		}
		n := bounds[b1] - bounds[b0]
		first, ok1 := r.indexOf(r.id, clock)
		lastClock := clock + uint64(n) - 1
		last, ok2 := r.indexOf(r.id, lastClock)
		if !ok1 || !ok2 {
			return
		}
		if last-first == n-1 {
			out = append(out, edRange{first, n, clock})
			return
		}
		if b1-b0 == 1 {
			return
		}
		mid := (b0 + b1) / 2
		split(b0, mid, clock)
		split(mid, b1, clock+uint64(bounds[mid]-bounds[b0]))
	}
	for _, rn := range runs {
		if r.budget < 0 {
			return nil
		}
		split(rn.b0, rn.b1, rn.clock)
	}
	if r.budget < 0 {
		return nil
	}
	return out
}

// invert runs the inverse of op and returns the operations it performed.
func (r *docReplica) invert(op edOp) ([]edOp, error) {
	deleted := crdt.DeleteSetFromDoc(r.doc)
	if !op.ins {
		at := r.anchorIndex(op, deleted)
		ins, err := r.doInsert(at, op.text)
		for _, s := range op.ids {
			if s.client == r.id {
				r.redirects = append(r.redirects, edRedirect{from: s.clock, to: ins.clock + uint64(s.off), n: uint64(s.n)})
				if len(r.redirects) > 4*edHistory {
					r.redirects = r.redirects[1:]
				}
			}
		}
		return []edOp{ins}, err
	}
	ranges := r.liveRanges(op, deleted)
	if r.budget < 0 {
		return nil, errUndoTooLarge
	}
	// Delete from the end so earlier indices stay valid.
	for i := 1; i < len(ranges); i++ {
		for j := i; j > 0 && ranges[j].at > ranges[j-1].at; j-- {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
		}
	}
	var out []edOp
	for _, rg := range ranges {
		d, err := r.doDelete(rg.at, rg.n, false)
		// The deleted characters are exactly this replica's clocks.
		d.ids = []edIDSpan{{off: 0, client: r.id, clock: rg.clock, n: rg.n}}
		out = append(out, d)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// record adds a local step (or extends the current one).
func (r *docReplica) record(ops []edOp, join bool) {
	if len(ops) == 0 {
		return
	}
	if join && len(r.undos) > 0 {
		r.undos[len(r.undos)-1] = append(r.undos[len(r.undos)-1], ops...)
	} else {
		r.undos = append(r.undos, ops)
		if len(r.undos) > edHistory {
			r.undos = r.undos[1:]
		}
	}
	r.redos = nil
}

// undoStep reverts this replica's most recent own step, or redoes the last
// undone one. It returns the UTF-16 offset after the change, or -1 when
// there was nothing to change.
func (r *docReplica) undoStep(redo bool) (_ int, err error) {
	defer r.guard(&err)
	from, to := &r.undos, &r.redos
	if redo {
		from, to = to, from
	}
	for len(*from) > 0 {
		step := (*from)[len(*from)-1]
		*from = (*from)[:len(*from)-1]
		var done []edOp
		r.budget = edResolveBudget
		for i := len(step) - 1; i >= 0; i-- {
			ops, err := r.invert(step[i])
			done = append(done, ops...)
			if errors.Is(err, errUndoTooLarge) && len(done) == 0 {
				// Nothing changed yet: the step is skipped.
				return -1, err
			}
			if err != nil {
				*to = append(*to, done)
				return -1, err
			}
		}
		if len(done) == 0 {
			// Everything it touched is gone: try the step before.
			continue
		}
		*to = append(*to, done)
		if len(*to) > edHistory {
			*to = (*to)[1:]
		}
		last := done[len(done)-1]
		if last.ins {
			i, _ := r.indexOf(r.id, last.clock)
			return i + u16Len(last.text), nil
		}
		return last.at, nil
	}
	return -1, nil
}

func (r *docReplica) canUndo(redo bool) bool {
	if redo {
		return len(r.redos) > 0
	}
	return len(r.undos) > 0
}
