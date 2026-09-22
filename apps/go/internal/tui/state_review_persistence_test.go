package tui

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
)

// failingProbes drops view/snapshot reads while armed, so a lost PUT
// acknowledgment cannot be reconciled within the same save.
type failingProbes struct {
	next  *viewTransport
	armed bool
}

func (f *failingProbes) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.armed && r.Method == "GET" {
		return nil, errors.New("simulated probe failure")
	}
	return f.next.RoundTrip(r)
}

func stateReviewWriter(t *testing.T, initial []byte, live ...string) (*viewWriter, *viewTransport, *failingProbes) {
	w, f := testViewWriter(t, initial, live...)
	probes := &failingProbes{next: f}
	w.connection.Load().HTTP = &http.Client{Transport: probes}
	return w, f, probes
}

func TestStateReviewViewWriterRecoversFromLostAckAndFailedProbe(t *testing.T) {
	for _, pruned := range []bool{false, true} {
		initial := writerPayload(t, 1, "live", map[string]string{"live": "old"})
		w, f, probes := stateReviewWriter(t, initial, "live")
		first := writerPayload(t, 2, "live", map[string]string{"live": "first"})
		if pruned {
			first = writerPayload(t, 2, "live", map[string]string{"live": "first", "gone": "deleted elsewhere"})
		}
		f.loseNextAck, probes.armed = true, true
		if err := w.save(first); err == nil || w.revision != 1 {
			t.Fatal("unconfirmed save reported as acknowledged")
		}
		if f.view.Revision != 2 {
			t.Fatal("setup: first PUT should have committed")
		}
		probes.armed = false
		second := writerPayload(t, 3, "live", map[string]string{"live": "second"})
		if err := w.save(second); err != nil {
			t.Fatalf("pruned=%v: writer stayed stale after a lost acknowledgment: %v", pruned, err)
		}
		if w.revision != 3 || w.generation != 3 || !bytes.Equal(f.view.Data, second) || !bytes.Equal(w.acknowledged, second) || len(w.unconfirmed) != 0 {
			t.Fatalf("pruned=%v: recovery did not adopt the committed revision", pruned)
		}
		// Exactly one guarded retry, at the adopted revision.
		if len(f.puts) != 3 || f.puts[1].Revision != 1 || f.puts[2].Revision != 2 {
			t.Fatalf("unexpected writes: %+v", f.puts)
		}
		if err := w.save(writerPayload(t, 4, "live", map[string]string{"live": "third"})); err != nil || len(f.puts) != 4 {
			t.Fatal("later saves still failing")
		}
	}
}

func TestStateReviewViewWriterStillRejectsForeignWriteAfterLostAck(t *testing.T) {
	initial := writerPayload(t, 1, "live", map[string]string{"live": "old"})
	w, f, probes := stateReviewWriter(t, initial, "live")
	f.loseNextAck, probes.armed = true, true
	if err := w.save(writerPayload(t, 2, "live", map[string]string{"live": "first"})); err == nil {
		t.Fatal("unconfirmed save reported as acknowledged")
	}
	probes.armed = false
	foreign := writerPayload(t, 9, "live", map[string]string{"live": "other client's edit"})
	f.view.Data, f.view.Revision = foreign, f.view.Revision+1
	mine := writerPayload(t, 3, "live", map[string]string{"live": "my newer edit"})
	if err := w.save(mine); err == nil {
		t.Fatal("foreign write overwritten")
	}
	if !bytes.Equal(f.view.Data, foreign) || len(f.puts) != 2 || w.revision != 1 {
		t.Fatalf("conflict changed remote state or retried: %+v", f.puts)
	}
	if !bytes.Contains(mine, []byte("my newer edit")) {
		t.Fatal("local content lost")
	}
	// A definitive server rejection is never remembered as possibly committed.
	for _, sent := range w.unconfirmed {
		if bytes.Equal(sent, mine) {
			t.Fatal("rejected payload remembered as unconfirmed")
		}
	}
	if len(w.unconfirmed) > maxUnconfirmedViews {
		t.Fatal("unbounded unconfirmed payloads")
	}
}
