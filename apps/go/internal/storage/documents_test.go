package storage

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestDocumentRecordsCommitCompactAndDelete(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec := DocumentRecord{ID: "doc-1", Root: "/r", Path: "a.txt", Meta: []byte(`{}`), Baseline: []byte("base"), Snapshot: []byte("snap")}
	if err := s.CreateDocument(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDocument(DocumentRecord{ID: "doc-2", Root: "/r", Path: "a.txt", Meta: []byte(`{}`), Snapshot: []byte{}}); !errors.Is(err, ErrDocumentExists) {
		t.Fatalf("duplicate file: %v", err)
	}
	if err := s.CommitDocument("doc-1", DocumentCommit{Updates: []DocumentUpdate{{1, []byte("u1")}, {2, []byte("u2")}}, Meta: []byte(`{"r":2}`),
		Versions: map[string][]byte{"base": []byte("b"), "disk": nil}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDocument("doc-1", DocumentCommit{Updates: []DocumentUpdate{{2, []byte("dup")}}, Meta: []byte(`{}`)}); err == nil {
		t.Fatal("duplicate revision accepted")
	}
	if err := s.CommitDocument("missing", DocumentCommit{Meta: []byte(`{}`)}); !errors.Is(err, ErrDocumentMissing) {
		t.Fatalf("missing document: %v", err)
	}
	v, err := s.DocumentVersions("doc-1")
	if _, ok := v["disk"]; err != nil || string(v["base"]) != "b" || !ok {
		t.Fatalf("versions %v %v", v, err)
	}
	if err := s.CommitDocument("doc-1", DocumentCommit{Updates: []DocumentUpdate{{3, []byte("u3")}}, Meta: []byte(`{"r":3}`), Baseline: []byte("new"), Versions: map[string][]byte{}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.DocumentVersions("doc-1"); len(v) != 0 {
		t.Fatal("versions not cleared")
	}
	if err := s.CompactDocument("doc-1", []byte("snap2"), 2); err != nil {
		t.Fatal(err)
	}
	docs, err := s.LoadDocuments()
	if err != nil || len(docs) != 1 {
		t.Fatal(docs, err)
	}
	d := docs[0]
	if string(d.Snapshot) != "snap2" || d.SnapshotRev != 2 || len(d.Updates) != 1 || d.Updates[0].Rev != 3 || string(d.Baseline) != "new" || string(d.Meta) != `{"r":3}` {
		t.Fatalf("loaded %+v", d)
	}
	// A gap in the log is reported, never silently skipped.
	if _, err := s.db.Exec("INSERT INTO document_updates(doc_id,rev,data) VALUES('doc-1',5,x'00')"); err != nil {
		t.Fatal(err)
	}
	if docs, err := s.LoadDocuments(); err != nil || len(docs) != 1 || docs[0].Damaged == "" {
		t.Fatalf("gap not reported: %v", err)
	}
	if err := s.QuarantineDocument("doc-1", "damaged"); err != nil {
		t.Fatal(err)
	}
	if docs, _ := s.LoadDocuments(); len(docs) != 0 {
		t.Fatal("quarantined document still loads")
	}
	if q, err := s.LoadQuarantined(); err != nil || len(q) != 1 || q[0].Path != "a.txt" || q[0].Reason != "damaged" {
		t.Fatalf("quarantine %+v %v", q, err)
	}
	// The file can be opened again as a new document.
	if err := s.CreateDocument(DocumentRecord{ID: "doc-3", Root: "/r", Path: "a.txt", Meta: []byte(`{}`), Snapshot: []byte{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDocument("doc-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM document_quarantine"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDocument("doc-1"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow("SELECT (SELECT count(*) FROM documents)+(SELECT count(*) FROM document_updates)+(SELECT count(*) FROM document_versions)").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left", n)
	}
}

func TestMigrationFromV2AddsDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE state (id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL); CREATE TABLE commands (id TEXT PRIMARY KEY, command BLOB NOT NULL, receipt BLOB NOT NULL); CREATE TABLE views (id TEXT PRIMARY KEY, data BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 1);` + artifactSchema + `PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version, tables int
	s.db.QueryRow("PRAGMA user_version").Scan(&version)
	s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('documents','document_updates','document_versions')").Scan(&tables)
	if version != 3 || tables != 3 {
		t.Fatalf("version %d tables %d", version, tables)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "recovery-before-v3-*.sqlite"))
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
}
