// Package doc wraps github.com/reearth/ygo (pinned v1.50.0) as the server's
// shared plain-text document (ADR 0022). A Doc holds one Yjs-compatible text
// named TextName and never lets an unvalidated client update reach its
// authoritative replica:
//
//   - every client update is first applied to a shadow replica; only when the
//     result passes validation is it applied to the main replica, so a bad
//     update never becomes part of the state that is persisted, broadcast or
//     saved (a rejected update rebuilds the shadow from the main replica);
//   - text is addressed in Go strings (UTF-8 byte offsets) at this API and in
//     UTF-16 code units only at the CRDT boundary;
//   - server-origin edits (3-way merges of external disk changes) are made on
//     the main replica with the server's own client ID and returned as a V1
//     update for persistence and broadcast.
//
// A Doc is not safe for concurrent use; the server gives each document one
// owning goroutine. ygo's own locking is internal: nothing here calls a Doc
// accessor inside a transaction callback (that deadlocks in ygo v1.50.0).
package doc

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/reearth/ygo/crdt"
)

// TextName is the root text type every replica must use.
const TextName = "t"

// MaxUpdate bounds one encoded client update. ygo v1.50.0 embeds the whole
// delete set in every incremental update, so a keystroke update grows with
// deletion history; 4 MiB leaves room for a 1 MiB paste plus that overhead.
const MaxUpdate = 4 << 20

// MaxNewRanges bounds the new insert runs and the new delete ranges one
// update may add. Integration cost grows with them; an update beyond the
// bound is refused as too large before it is applied.
const MaxNewRanges = 4096

// Rejection reasons (Error.Reason).
const (
	// ReasonInvalid: the update does not decode, or it leaves integrations
	// pending (it depends on state the server does not have: resync first).
	ReasonInvalid = "invalid"
	// ReasonOrigin: the update adds items for a client ID other than the
	// ones allowed for the sender.
	ReasonOrigin = "origin"
	// ReasonContent: formatting, embeds, nested types or text outside the
	// shared text.
	ReasonContent = "content"
	// ReasonSurrogate: an edit boundary splits a UTF-16 surrogate pair.
	ReasonSurrogate = "surrogate_split"
	// ReasonTooLarge: the encoded update exceeds MaxUpdate.
	ReasonTooLarge = "too_large"
	// ReasonRejected: the caller's Check refused the resulting text.
	ReasonRejected = "rejected"
)

// Error is a validation failure. The main replica is unchanged.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "doc: " + e.Reason + ": " + e.Err.Error()
	}
	return "doc: " + e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

func reject(reason string, format string, args ...any) error {
	return &Error{Reason: reason, Err: fmt.Errorf(format, args...)}
}

// serverOrigin tags the server's own transactions. It is non-zero-sized so
// its pointer is a distinct origin (ygo compares origins with ==).
type serverOrigin struct{ _ byte }

// Doc is one shared text document.
type Doc struct {
	main, shadow         *crdt.Doc
	mainText, shadowText *crdt.YText
	text                 string
	origin               *serverOrigin
}

// Edit replaces Text[Start:End] (UTF-8 byte offsets into the current text)
// with Text.
type Edit struct {
	Start, End int
	Text       string
}

func newReplicas() *Doc {
	d := &Doc{main: crdt.New(), shadow: crdt.New(), origin: &serverOrigin{}}
	d.mainText = d.main.GetText(TextName)
	d.shadowText = d.shadow.GetText(TextName)
	return d
}

// New creates a document holding text, authored by a fresh server client
// ID. It returns the full state as a V1 update suitable as the persisted
// snapshot.
func New(text string) (*Doc, []byte, error) {
	if !utf8.ValidString(text) {
		return nil, nil, reject(ReasonContent, "text is not valid UTF-8")
	}
	d := newReplicas()
	if text != "" {
		if _, err := d.Replace([]Edit{{Start: 0, End: 0, Text: text}}); err != nil {
			return nil, nil, err
		}
	}
	return d, d.EncodeState(), nil
}

// Load rebuilds a document from a snapshot (a full-state update, or nil)
// followed by the update log in order. The server's client ID is fresh for
// every load, so clocks are never reused after a restart.
func Load(snapshot []byte, updates [][]byte) (*Doc, error) {
	d := newReplicas()
	all := updates
	if len(snapshot) > 0 {
		all = append([][]byte{snapshot}, updates...)
	}
	for i, u := range all {
		if err := safeApply(d.main, u, nil); err != nil {
			return nil, fmt.Errorf("doc: load update %d: %w", i, err)
		}
	}
	if p := d.main.PendingStats(); p.Items > 0 || p.DeleteRanges > 0 {
		return nil, errors.New("doc: stored updates leave integrations pending")
	}
	if err := d.resetShadow(); err != nil {
		return nil, err
	}
	d.text = d.mainText.ToString()
	if !utf8.ValidString(d.text) {
		return nil, errors.New("doc: stored text is not valid UTF-8")
	}
	return d, nil
}

// safeApply applies a V1 update and converts a decoder panic into an error.
func safeApply(target *crdt.Doc, update []byte, origin any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("update panicked: %v", r)
		}
	}()
	return crdt.ApplyUpdateV1(target, update, origin)
}

// resetShadow replaces the shadow replica with a copy of the main one.
func (d *Doc) resetShadow() error {
	shadow := crdt.New()
	if err := safeApply(shadow, d.EncodeState(), nil); err != nil {
		return fmt.Errorf("doc: rebuild shadow: %w", err)
	}
	d.shadow, d.shadowText = shadow, shadow.GetText(TextName)
	return nil
}

// Text returns the current text.
func (d *Doc) Text() string { return d.text }

// ServerClientID is the client ID of the server's own edits in this
// incarnation.
func (d *Doc) ServerClientID() uint64 { return uint64(d.main.ClientID()) }

// EncodeState returns the full state as a V1 update.
func (d *Doc) EncodeState() []byte { return crdt.EncodeStateAsUpdateV1(d.main, nil) }

// StateVector returns the encoded V1 state vector.
func (d *Doc) StateVector() []byte { return crdt.EncodeStateVectorV1(d.main) }

// Diff returns the V1 update a replica with the encoded state vector sv is
// missing.
func (d *Doc) Diff(sv []byte) ([]byte, error) {
	v, err := crdt.DecodeStateVectorV1(sv)
	if err != nil {
		return nil, reject(ReasonInvalid, "state vector: %v", err)
	}
	return crdt.EncodeStateAsUpdateV1(d.main, v), nil
}

// Check inspects a candidate text before an update is accepted; a non-nil
// error rejects the update with ReasonRejected (wrapping the error).
type Check func(old, next string) error

// Applied describes an accepted client update.
type Applied struct {
	// Changed is false when the update carried nothing new (a retry or a
	// duplicate); such an update need not be persisted.
	Changed bool
	// Clients lists the client IDs whose items the update added.
	Clients []uint64
}

// ApplyClient validates update on the shadow replica and, when it passes,
// applies it to the main replica. allowed reports whether the sender may
// author items with a client ID; the server's own ID is always refused.
// check (optional) vets the resulting text. On error the document is
// unchanged.
func (d *Doc) ApplyClient(update []byte, allowed func(uint64) bool, check Check) (Applied, error) {
	if len(update) > MaxUpdate {
		return Applied{}, reject(ReasonTooLarge, "update of %d bytes exceeds %d", len(update), MaxUpdate)
	}
	applied, next, err := d.trial(update, allowed, check)
	if err != nil {
		if resetErr := d.resetShadow(); resetErr != nil {
			return Applied{}, errors.Join(err, resetErr)
		}
		return Applied{}, err
	}
	if !applied.Changed {
		return applied, nil
	}
	if err := safeApply(d.main, update, "client"); err != nil {
		// The shadow accepted exactly these bytes against identical state,
		// so this indicates a library fault; the shadow is rebuilt and the
		// main text re-read so the two cannot silently diverge.
		d.text = d.mainText.ToString()
		return Applied{}, errors.Join(reject(ReasonInvalid, "main replica refused a validated update: %v", err), d.resetShadow())
	}
	d.text = next
	if got := d.mainText.Len(); got != utf16Len(next) {
		d.text = d.mainText.ToString()
		return applied, fmt.Errorf("doc: replicas diverged after a validated update (%d vs %d UTF-16 units)", got, utf16Len(next))
	}
	return applied, nil
}

// trial applies update to the shadow and validates the outcome. It returns
// the resulting text.
func (d *Doc) trial(update []byte, allowed func(uint64) bool, check Check) (Applied, string, error) {
	if err := d.checkRanges(update); err != nil {
		return Applied{}, "", err
	}
	before := d.shadow.StateVector()
	var deltas [][]crdt.Delta
	unobserve := d.shadowText.Observe(func(ev crdt.YTextEvent) {
		deltas = append(deltas, append([]crdt.Delta(nil), ev.Delta...))
	})
	err := safeApply(d.shadow, update, "trial")
	unobserve()
	if err != nil {
		return Applied{}, "", reject(ReasonInvalid, "decode: %v", err)
	}
	if p := d.shadow.PendingStats(); p.Items > 0 || p.DeleteRanges > 0 {
		return Applied{}, "", reject(ReasonInvalid, "update depends on state the server does not have; resynchronize")
	}
	var out Applied
	server := d.main.ClientID()
	var newUnits uint64
	for client, clock := range d.shadow.StateVector() {
		if clock <= before[client] {
			continue
		}
		newUnits += clock - before[client]
		if client == server || allowed == nil || !allowed(uint64(client)) {
			return Applied{}, "", reject(ReasonOrigin, "update adds items for client %d", uint64(client))
		}
		out.Clients = append(out.Clients, uint64(client))
	}
	next := d.text
	var insertedUnits uint64
	for _, delta := range deltas {
		if next, err = applyDelta(next, delta); err != nil {
			return Applied{}, "", err
		}
		for _, op := range delta {
			if text, ok := op.Insert.(string); ok && op.Op == crdt.DeltaOpInsert {
				insertedUnits += uint64(utf16Len(text))
			}
		}
	}
	// Every new clock unit must be text that became visible in the shared
	// text. Anything else (other roots, maps, formatting marks, nested
	// types, or text inserted and deleted within one update) would grow the
	// state invisibly and is refused; send each transaction's own update.
	if newUnits != insertedUnits {
		return Applied{}, "", reject(ReasonContent, "update adds %d units outside the visible shared text", newUnits-min(newUnits, insertedUnits))
	}
	// The delta is the library's account of the change; the replica's own
	// text is the ground truth. A mismatch (for example a split surrogate
	// pair turned into U+FFFD) is refused.
	actual := d.shadowText.ToString()
	if actual != next {
		return Applied{}, "", reject(ReasonSurrogate, "resulting text does not match the reported change")
	}
	out.Changed = len(out.Clients) > 0 || actual != d.text || len(deltas) > 0
	if !out.Changed {
		// Deletes of already-deleted items and duplicates change nothing.
		return out, next, nil
	}
	if !utf8.ValidString(next) {
		return Applied{}, "", reject(ReasonContent, "resulting text is not valid UTF-8")
	}
	if check != nil {
		if err := check(d.text, next); err != nil {
			return Applied{}, "", &Error{Reason: ReasonRejected, Err: err}
		}
	}
	return out, next, nil
}

// checkRanges refuses an update that would add more than MaxNewRanges insert
// runs or delete ranges. ygo updates repeat the sender's whole delete set,
// so only ranges the server has not yet deleted count.
func (d *Doc) checkRanges(update []byte) error {
	ids, err := crdt.ContentIDsFromUpdateV1(update)
	if err != nil {
		return reject(ReasonInvalid, "decode: %v", err)
	}
	count := func(set *crdt.IDSet) int {
		if set == nil {
			return 0
		}
		n := 0
		for _, c := range set.Clients() {
			n += len(set.Ranges(c))
		}
		return n
	}
	if count(ids.Inserts) > MaxNewRanges {
		sv := d.shadow.StateVector()
		known := crdt.NewIDSet()
		for c, clock := range sv {
			known.Add(c, 0, clock)
		}
		if count(crdt.ExcludeIDSet(ids.Inserts, known)) > MaxNewRanges {
			return reject(ReasonTooLarge, "update adds more than %d insert runs", MaxNewRanges)
		}
	}
	if count(ids.Deletes) > MaxNewRanges {
		if count(crdt.ExcludeIDSet(ids.Deletes, crdt.DeleteSetFromDoc(d.shadow))) > MaxNewRanges {
			return reject(ReasonTooLarge, "update deletes more than %d new ranges", MaxNewRanges)
		}
	}
	return nil
}

// applyDelta applies one text delta (UTF-16 counts) to s, refusing any
// boundary inside a surrogate pair and any non-text content.
func applyDelta(s string, delta []crdt.Delta) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	pos := 0 // byte offset into s
	advance := func(units int, keep bool) error {
		end, err := byteOffset(s, pos, units)
		if err != nil {
			return err
		}
		if keep {
			b.WriteString(s[pos:end])
		}
		pos = end
		return nil
	}
	for _, op := range delta {
		if len(op.Attributes) > 0 {
			return "", reject(ReasonContent, "formatting is not supported")
		}
		switch op.Op {
		case crdt.DeltaOpRetain:
			if err := advance(op.Retain, true); err != nil {
				return "", err
			}
		case crdt.DeltaOpDelete:
			if err := advance(op.Delete, false); err != nil {
				return "", err
			}
		case crdt.DeltaOpInsert:
			text, ok := op.Insert.(string)
			if !ok {
				return "", reject(ReasonContent, "embedded content is not supported")
			}
			b.WriteString(text)
		default:
			return "", reject(ReasonContent, "unknown delta operation")
		}
	}
	b.WriteString(s[pos:])
	return b.String(), nil
}

// byteOffset advances units UTF-16 code units from byte offset pos in s and
// returns the new byte offset. Landing inside a supplementary character is
// a surrogate split.
func byteOffset(s string, pos, units int) (int, error) {
	if units < 0 {
		return 0, reject(ReasonInvalid, "negative delta length")
	}
	for units > 0 {
		if pos >= len(s) {
			return 0, reject(ReasonInvalid, "delta exceeds the text")
		}
		r, n := utf8.DecodeRuneInString(s[pos:])
		w := utf16.RuneLen(r)
		if w < 1 {
			w = 1
		}
		if w > units {
			return 0, reject(ReasonSurrogate, "an edit boundary splits a surrogate pair")
		}
		units -= w
		pos += n
	}
	return pos, nil
}

// Replace applies server-origin edits (byte offsets into the current text,
// ascending and non-overlapping; offsets must fall on rune boundaries) in
// one transaction and returns the resulting V1 update, or nil when the
// edits change nothing.
func (d *Doc) Replace(edits []Edit) ([]byte, error) {
	prev := 0
	for _, e := range edits {
		if e.Start < prev || e.End < e.Start || e.End > len(d.text) ||
			!utf8.ValidString(e.Text) || !runeBoundary(d.text, e.Start) || !runeBoundary(d.text, e.End) {
			return nil, fmt.Errorf("doc: invalid server edit [%d,%d) of %d bytes", e.Start, e.End, len(d.text))
		}
		prev = e.End
	}
	// Convert to UTF-16 indices against the text before the transaction.
	type unitEdit struct {
		at, n int
		text  string
	}
	units := make([]unitEdit, 0, len(edits))
	u, last := 0, 0
	for _, e := range edits {
		if e.Start == e.End && e.Text == "" {
			continue
		}
		u += utf16Len(d.text[last:e.Start])
		n := utf16Len(d.text[e.Start:e.End])
		units = append(units, unitEdit{at: u, n: n, text: e.Text})
		u += n
		last = e.End
	}
	if len(units) == 0 {
		return nil, nil
	}
	var b strings.Builder
	last = 0
	for _, e := range edits {
		b.WriteString(d.text[last:e.Start])
		b.WriteString(e.Text)
		last = e.End
	}
	b.WriteString(d.text[last:])
	next := b.String()

	var update []byte
	unsubscribe := d.main.OnUpdate(func(u []byte, origin any) {
		if origin == d.origin {
			update = append([]byte(nil), u...)
		}
	})
	text := d.mainText
	d.main.Transact(func(txn *crdt.Transaction) {
		// Descending order keeps earlier indices valid.
		for i := len(units) - 1; i >= 0; i-- {
			if units[i].n > 0 {
				text.Delete(txn, units[i].at, units[i].n)
			}
			if units[i].text != "" {
				text.Insert(txn, units[i].at, units[i].text, nil)
			}
		}
	}, d.origin)
	unsubscribe()
	if update == nil {
		return nil, errors.New("doc: server edit produced no update")
	}
	if err := safeApply(d.shadow, update, "server"); err != nil {
		return nil, errors.Join(fmt.Errorf("doc: shadow refused server update: %w", err), d.resetShadow())
	}
	d.text = d.mainText.ToString()
	if d.text != next {
		return update, errors.New("doc: server edit produced unexpected text")
	}
	return update, nil
}

// SetText replaces the whole text with next through a minimal server-origin
// edit (common prefix and suffix are kept). It returns nil when unchanged.
func (d *Doc) SetText(next string) ([]byte, error) {
	if next == d.text {
		return nil, nil
	}
	e := MinimalEdit(d.text, next)
	return d.Replace([]Edit{e})
}

// MinimalEdit returns the single edit turning old into next that keeps their
// longest common prefix and suffix, on rune boundaries.
func MinimalEdit(old, next string) Edit {
	p := 0
	for p < len(old) && p < len(next) && old[p] == next[p] {
		p++
	}
	for p > 0 && (p < len(old) && !utf8.RuneStart(old[p]) || p < len(next) && !utf8.RuneStart(next[p])) {
		p--
	}
	s := 0
	for s < len(old)-p && s < len(next)-p && old[len(old)-1-s] == next[len(next)-1-s] {
		s++
	}
	for s > 0 && (!utf8.RuneStart(old[len(old)-s]) || !utf8.RuneStart(next[len(next)-s])) {
		s--
	}
	return Edit{Start: p, End: len(old) - s, Text: next[p : len(next)-s]}
}

func runeBoundary(s string, i int) bool {
	return i == 0 || i == len(s) || i > 0 && i < len(s) && utf8.RuneStart(s[i])
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += max(1, utf16.RuneLen(r))
	}
	return n
}

// UTF16Len returns the length of s in UTF-16 code units (the CRDT index
// unit).
func UTF16Len(s string) int { return utf16Len(s) }
