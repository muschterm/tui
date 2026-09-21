package storage

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestProjectRemovalRollbackAndPayloadPurge(t *testing.T) {
	st, snap := deletionStore(t)
	snap.Projects = []protocol.Project{{ID: "removed", Name: "private-project"}, {ID: "retained", Name: "keep"}}
	snap.Threads[0].ProjectID = "removed"
	snap.Threads[1].ProjectID = "retained"
	add := protocol.Command{Version: 1, ID: "add", Kind: "project.add", Path: "private-path"}
	if err := st.Save(snap, &add, &protocol.Receipt{ID: add.ID, State: "accepted", TargetID: "removed"}); err != nil {
		t.Fatal(err)
	}
	create := protocol.Command{Version: 1, ID: "create", Kind: "thread.create", ProjectID: "removed", Text: "private-title"}
	if err := st.Save(snap, &create, &protocol.Receipt{ID: create.ID, State: "accepted", TargetID: "gone"}); err != nil {
		t.Fatal(err)
	}
	// An invalid legacy view makes projection fail after receipt tombstones have
	// been staged; the transaction must roll all of those changes back.
	if _, err := st.db.Exec("INSERT INTO views(id,data,revision) VALUES('broken',?,1)", []byte(`{"Threads":42}`)); err != nil {
		t.Fatal(err)
	}
	next := snap
	next.Projects = next.Projects[1:]
	next.Threads = next.Threads[1:]
	next.Terminals = next.Terminals[1:]
	remove := protocol.Command{Version: 1, ID: "remove", Kind: "project.remove", ProjectID: "removed"}
	receipt := protocol.Receipt{ID: remove.ID, State: "accepted"}
	if err := st.Save(next, &remove, &receipt); err == nil {
		t.Fatal("invalid saved view silently discarded")
	}
	loaded, _, err := st.Load()
	if err != nil || !reflect.DeepEqual(loaded, snap) {
		t.Fatal("failed removal changed snapshot", err)
	}
	for _, c := range []protocol.Command{add, create} {
		r, err := st.Lookup(c)
		if err != nil || r == nil || r.State != "accepted" {
			t.Fatal("rollback retained tombstone", r, err)
		}
	}
	if r, err := st.Lookup(remove); err != nil || r != nil {
		t.Fatal("failed removal recorded receipt", err)
	}
	if _, err := st.db.Exec("DELETE FROM views WHERE id='broken'"); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(next, &remove, &receipt); err != nil {
		t.Fatal(err)
	}
	rows, err := st.db.Query("SELECT command,receipt FROM commands")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var command, receipt []byte
		if err := rows.Scan(&command, &receipt); err != nil {
			t.Fatal(err)
		}
		if json.Valid(command) {
			t.Fatal("project-owned command payload retained", string(command))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
