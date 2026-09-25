package collabprobe

import "github.com/reearth/ygo/crdt"

type localOrigin struct{ client uint64 }

type reearthReplica struct {
	doc  *crdt.Doc
	text *crdt.YText
	um   *crdt.UndoManager
	org  localOrigin
}

// Reearth is the factory for github.com/reearth/ygo.
var Reearth = Factory{Lib: "reearth/ygo", New: func(c uint64) Replica {
	d := crdt.New(crdt.WithClientID(crdt.ClientID(c)))
	t := d.GetText("t")
	o := localOrigin{c}
	um := crdt.NewUndoManager(d, []crdt.SharedType{t}, crdt.WithTrackedOrigins(o), crdt.WithCaptureTimeout(0))
	return &reearthReplica{doc: d, text: t, um: um, org: o}
}}

func (r *reearthReplica) Name() string     { return "reearth" }
func (r *reearthReplica) ClientID() uint64 { return uint64(r.doc.ClientID()) }

// edit captures the transaction's own update via OnUpdate (like Yjs's
// 'update' event), which carries only this transaction's deletes. A
// state-vector diff would carry the whole delete set every time.
func (r *reearthReplica) edit(fn func(*crdt.Transaction)) []byte {
	var out []byte
	unsub := r.doc.OnUpdate(func(u []byte, origin any) {
		if origin == r.org {
			out = append([]byte(nil), u...)
		}
	})
	r.doc.Transact(fn, r.org)
	unsub()
	return out
}

func (r *reearthReplica) Insert(i int, s string) []byte {
	return r.edit(func(t *crdt.Transaction) { r.text.Insert(t, i, s, nil) })
}
func (r *reearthReplica) Delete(i, n int) []byte {
	return r.edit(func(t *crdt.Transaction) { r.text.Delete(t, i, n) })
}
func (r *reearthReplica) Apply(u []byte) error { return crdt.ApplyUpdateV1(r.doc, u, "remote") }
func (r *reearthReplica) Text() string         { return r.text.ToString() }
func (r *reearthReplica) Len() int             { return r.text.Len() }
func (r *reearthReplica) StateVector() []byte  { return crdt.EncodeStateVectorV1(r.doc) }
func (r *reearthReplica) Diff(sv []byte) ([]byte, error) {
	v, err := crdt.DecodeStateVectorV1(sv)
	if err != nil {
		return nil, err
	}
	return crdt.EncodeStateAsUpdateV1(r.doc, v), nil
}
func (r *reearthReplica) Full() []byte   { return crdt.EncodeStateAsUpdateV1(r.doc, nil) }
func (r *reearthReplica) Undo() bool     { return r.um.Undo() }
func (r *reearthReplica) StopCapturing() { r.um.StopCapturing() }
func (r *reearthReplica) Anchor(i, assoc int) []byte {
	return crdt.EncodeRelativePosition(crdt.CreateRelativePositionFromIndex(r.text, i, assoc))
}
func (r *reearthReplica) Resolve(b []byte) (int, bool) {
	rp, err := crdt.DecodeRelativePosition(b)
	if err != nil {
		return 0, false
	}
	a, ok := crdt.ToAbsolutePosition(r.doc, rp)
	return a.Index, ok
}
