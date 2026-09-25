// Package collabprobe is throwaway feasibility evidence for choosing a Go
// collaborative-text library (see docs/research/go-collab-2026-09-24.md).
// It is not application code.
package collabprobe

// Replica is the minimal surface the probe needs from a Yjs-compatible
// library: one shared text named "t". All indices are in the library's
// native unit (UTF-16 code units for both candidates, verified by tests).
type Replica interface {
	Name() string
	ClientID() uint64
	// Insert/Delete perform a local edit tagged with the replica's local
	// undo origin and return the incremental V1 update it produced.
	Insert(idx int, s string) []byte
	Delete(idx, n int) []byte
	Apply(update []byte) error
	Text() string
	Len() int
	StateVector() []byte
	Diff(remoteSV []byte) ([]byte, error)
	Full() []byte
	// Undo reverts only this replica's own tracked edits.
	Undo() bool
	StopCapturing()
	Anchor(idx int, assoc int) []byte
	Resolve(anchor []byte) (int, bool)
}

// Factory creates a fresh replica with a fixed client id.
type Factory struct {
	Lib string
	New func(client uint64) Replica
}
