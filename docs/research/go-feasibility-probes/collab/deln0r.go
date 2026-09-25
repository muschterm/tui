package collabprobe

import (
	"github.com/Deln0r/ygo"
)

type delnOrigin struct{ client uint64 }

type delnReplica struct {
	doc  *ygo.Doc
	text *ygo.Text
	um   *ygo.UndoManager
	org  delnOrigin
}

// Deln0r is the factory for github.com/Deln0r/ygo.
var Deln0r = Factory{Lib: "Deln0r/ygo", New: func(c uint64) Replica {
	d := ygo.NewDocWithOptions(ygo.Options{ClientID: c})
	t := ygo.NewText(d, "t")
	o := delnOrigin{c}
	um := ygo.NewUndoManagerWithOptions(d, ygo.UndoManagerOptions{
		CaptureTimeout: -1, TrackedOrigins: map[any]struct{}{o: {}},
	}, t)
	return &delnReplica{doc: d, text: t, um: um, org: o}
}}

func (r *delnReplica) Name() string     { return "deln0r" }
func (r *delnReplica) ClientID() uint64 { return r.doc.ClientID() }

func (r *delnReplica) edit(fn func(*ygo.TransactionMut) error) []byte {
	sv := ygo.EncodeStateVector(r.doc)
	txn := r.doc.WriteTxn()
	txn.Origin = r.org
	err := fn(txn)
	txn.Commit()
	if err != nil {
		panic(err)
	}
	u, err := ygo.EncodeDiff(r.doc, sv)
	if err != nil {
		panic(err)
	}
	return u
}

func (r *delnReplica) Insert(i int, s string) []byte {
	return r.edit(func(t *ygo.TransactionMut) error { return r.text.Insert(t, uint64(i), s) })
}
func (r *delnReplica) Delete(i, n int) []byte {
	return r.edit(func(t *ygo.TransactionMut) error { return r.text.Delete(t, uint64(i), uint64(n)) })
}
func (r *delnReplica) Apply(u []byte) error { return ygo.ApplyUpdate(r.doc, u) }
func (r *delnReplica) Text() string {
	t := r.doc.ReadTxn()
	defer t.Close()
	return r.text.String()
}
func (r *delnReplica) Len() int {
	t := r.doc.ReadTxn()
	defer t.Close()
	return int(r.text.Length())
}
func (r *delnReplica) StateVector() []byte            { return ygo.EncodeStateVector(r.doc) }
func (r *delnReplica) Diff(sv []byte) ([]byte, error) { return ygo.EncodeDiff(r.doc, sv) }
func (r *delnReplica) Full() []byte                   { return ygo.EncodeStateAsUpdate(r.doc) }
func (r *delnReplica) Undo() bool                     { return r.um.Undo() }
func (r *delnReplica) StopCapturing()                 { r.um.StopCapturing() }
func (r *delnReplica) Anchor(i, assoc int) []byte {
	t := r.doc.ReadTxn()
	rp, err := ygo.CreateRelativePositionFromTypeIndex(r.text, uint64(i), int64(assoc))
	t.Close()
	if err != nil {
		panic(err)
	}
	b, err := ygo.EncodeRelativePosition(rp)
	if err != nil {
		panic(err)
	}
	return b
}
func (r *delnReplica) Resolve(b []byte) (int, bool) {
	rp, err := ygo.DecodeRelativePosition(b)
	if err != nil {
		return 0, false
	}
	a, ok := ygo.CreateAbsolutePositionFromRelativePosition(r.doc, rp)
	return int(a.Index), ok
}
