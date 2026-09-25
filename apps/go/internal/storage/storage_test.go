package storage

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestViewCASRejectsLateAndCompetingSaves(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.LoadView("desk")
	if err != nil || v.Revision != 0 {
		t.Fatal("missing view should start at revision0", err)
	}
	first, err := s.PutView("desk", json.RawMessage(`{"draft":"first"}`), 0)
	if err != nil || first.Revision != 1 {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, body := range []string{`{"draft":"second"}`, `{"draft":"competing"}`} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			_, err := s.PutView("desk", json.RawMessage(body), first.Revision)
			results <- err
		}(body)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == "stale_view" {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	latest, err := s.LoadView("desk")
	if err != nil || latest.Revision != 2 {
		t.Fatal(err)
	}
	if _, err = s.PutView("desk", json.RawMessage(`{"draft":"late first"}`), 0); err == nil {
		t.Fatal("late initial save overwrote later draft")
	}
	if _, err = s.PutView("desk", json.RawMessage(`{"draft":"late second"}`), 1); err == nil {
		t.Fatal("late revision1 save overwrote later draft")
	}
	after, _ := s.LoadView("desk")
	if string(after.Data) != string(latest.Data) || after.Revision != latest.Revision {
		t.Fatal("rejected write changed view")
	}
}

func TestViewRevisionMigrationPreservesDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE views(id TEXT PRIMARY KEY,data BLOB NOT NULL); INSERT INTO views VALUES('desk','{"draft":"existing"}');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.LoadView("desk")
	if err != nil || v.Revision != 1 || string(v.Data) != `{"draft":"existing"}` {
		t.Fatalf("migration lost draft: %+v %v", v, err)
	}
	if _, err = s.PutView("desk", json.RawMessage(`{"draft":"next"}`), v.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestFutureSchemaUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=99; CREATE TABLE future(data TEXT); INSERT INTO future VALUES('preserve');"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("future schema accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("future schema modified")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "recovery-*.sqlite"))
	if len(backups) != 0 {
		t.Fatal("future schema backup/migration attempted")
	}
}

func TestMigrationBackupPreservesOriginalSchemaAndContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE views(id TEXT PRIMARY KEY,data BLOB NOT NULL); INSERT INTO views VALUES('desk','{"draft":"recover me"}');`); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "recovery-before-v2-*.sqlite"))
	if len(backups) != 1 {
		t.Fatalf("backup count %d", len(backups))
	}
	info, _ := os.Stat(backups[0])
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", info.Mode())
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var data string
	if err = backup.QueryRow("SELECT data FROM views WHERE id='desk'").Scan(&data); err != nil || data != `{"draft":"recover me"}` {
		t.Fatal("backup lost WAL contents", err, data)
	}
	var version, columns int
	backup.QueryRow("PRAGMA user_version").Scan(&version)
	backup.QueryRow("SELECT count(*) FROM pragma_table_info('views') WHERE name='revision'").Scan(&columns)
	if version != 0 || columns != 0 {
		t.Fatal("backup was taken after migration")
	}
	s.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 2 {
		t.Fatal("schema version not updated")
	}
}

func TestResolveRunningReceiptsUsesGitResultKeys(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := protocol.Command{Version: 1, ID: "g", Kind: protocol.GitKindCommit, Git: &protocol.GitWrite{Message: "m"}}
	r := protocol.Receipt{ID: "g", State: protocol.GitStateRunning, Git: &protocol.GitResult{Op: "commit", State: protocol.GitStateRunning}}
	if err := st.Save(protocol.Snapshot{Version: 1}, &c, &r); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ResolveRunningReceipts(protocol.GitStateOutcomeUnknown, "interrupted", "msg"); err != nil || n != 1 {
		t.Fatalf("resolve: %d %v", n, err)
	}
	var raw string
	if err := st.db.QueryRow("SELECT CAST(receipt AS TEXT) FROM commands WHERE id='g'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	g := generic["Git"].(map[string]any)
	if generic["State"] != "outcome_unknown" || g["state"] != "outcome_unknown" || g["code"] != "interrupted" || g["message"] != "msg" || g["State"] != nil || g["Code"] != nil {
		t.Fatalf("raw receipt: %s", raw)
	}
}
