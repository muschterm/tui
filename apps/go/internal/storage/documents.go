package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// Shared documents (schema version 3, ADR 0022). Each document has one row in
// documents holding server-owned metadata (opaque JSON), the last agreed file
// bytes (baseline) and a compacted CRDT snapshot; accepted updates are
// appended to document_updates in revision order until the next compaction
// folds them into the snapshot. document_versions keeps the base, document
// and disk versions retained for a paused document.
const documentSchema = `
CREATE TABLE IF NOT EXISTS documents (
	id TEXT PRIMARY KEY,
	root TEXT NOT NULL,
	path TEXT NOT NULL,
	meta BLOB NOT NULL,
	baseline BLOB NOT NULL,
	snapshot BLOB NOT NULL,
	snapshot_rev INTEGER NOT NULL,
	UNIQUE(root, path)
);
CREATE TABLE IF NOT EXISTS document_updates (
	doc_id TEXT NOT NULL,
	rev INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY(doc_id, rev)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS document_versions (
	doc_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY(doc_id, kind)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS document_quarantine (
	id TEXT PRIMARY KEY,
	root TEXT NOT NULL,
	path TEXT NOT NULL,
	reason TEXT NOT NULL,
	meta BLOB NOT NULL,
	baseline BLOB NOT NULL,
	snapshot BLOB NOT NULL,
	snapshot_rev INTEGER NOT NULL
);`

// DocumentRecord is one stored document. Updates holds the log after
// SnapshotRev in order when loaded.
type DocumentRecord struct {
	ID, Root, Path string
	Meta           []byte
	Baseline       []byte
	Snapshot       []byte
	SnapshotRev    int64
	Updates        []DocumentUpdate
	// Damaged is set by LoadDocuments when the stored log is inconsistent;
	// such a record should be quarantined, not loaded.
	Damaged string
}

// QuarantinedDocument is a stored document set aside because it could not
// be loaded. Its rows (update log and versions under ID) are kept.
type QuarantinedDocument struct {
	ID, Root, Path, Reason string
}

// DocumentUpdate is one accepted CRDT update at revision Rev.
type DocumentUpdate struct {
	Rev  int64
	Data []byte
}

// DocumentCommit is written atomically by CommitDocument. Nil Baseline and
// Versions leave the stored values unchanged; a non-nil empty Versions map
// clears them.
type DocumentCommit struct {
	Updates  []DocumentUpdate
	Meta     []byte
	Baseline []byte
	Versions map[string][]byte
}

// ErrDocumentExists reports a second document for the same root and path.
var ErrDocumentExists = errors.New("storage: a document for this file already exists")

// ErrDocumentMissing reports a commit to a document that is not stored.
var ErrDocumentMissing = errors.New("storage: document is not stored")

// CreateDocument stores a new document.
func (s *Store) CreateDocument(rec DocumentRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM documents WHERE root=? AND path=?", rec.Root, rec.Path).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrDocumentExists
	}
	if rec.Baseline == nil {
		rec.Baseline = []byte{}
	}
	if _, err = tx.Exec("INSERT INTO documents(id,root,path,meta,baseline,snapshot,snapshot_rev) VALUES(?,?,?,?,?,?,?)",
		rec.ID, rec.Root, rec.Path, rec.Meta, rec.Baseline, rec.Snapshot, rec.SnapshotRev); err != nil {
		return err
	}
	for _, u := range rec.Updates {
		if _, err = tx.Exec("INSERT INTO document_updates(doc_id,rev,data) VALUES(?,?,?)", rec.ID, u.Rev, u.Data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CommitDocument appends updates and replaces metadata (and optionally the
// baseline and retained versions) in one transaction.
func (s *Store) CommitDocument(id string, c DocumentCommit) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var res sql.Result
	if c.Baseline != nil {
		res, err = tx.Exec("UPDATE documents SET meta=?, baseline=? WHERE id=?", c.Meta, c.Baseline, id)
	} else {
		res, err = tx.Exec("UPDATE documents SET meta=? WHERE id=?", c.Meta, id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDocumentMissing
	}
	for _, u := range c.Updates {
		if _, err = tx.Exec("INSERT INTO document_updates(doc_id,rev,data) VALUES(?,?,?)", id, u.Rev, u.Data); err != nil {
			return err
		}
	}
	if c.Versions != nil {
		if _, err = tx.Exec("DELETE FROM document_versions WHERE doc_id=?", id); err != nil {
			return err
		}
		for kind, data := range c.Versions {
			if data == nil {
				data = []byte{}
			}
			if _, err = tx.Exec("INSERT INTO document_versions(doc_id,kind,data) VALUES(?,?,?)", id, kind, data); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// CompactDocument replaces the snapshot with one covering every update up to
// rev and drops those updates.
func (s *Store) CompactDocument(id string, snapshot []byte, rev int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE documents SET snapshot=?, snapshot_rev=? WHERE id=? AND snapshot_rev<=?", snapshot, rev, id, rev)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDocumentMissing
	}
	if _, err = tx.Exec("DELETE FROM document_updates WHERE doc_id=? AND rev<=?", id, rev); err != nil {
		return err
	}
	return tx.Commit()
}

// DocumentVersions returns the retained versions of a document.
func (s *Store) DocumentVersions(id string) (map[string][]byte, error) {
	rows, err := s.db.Query("SELECT kind,data FROM document_versions WHERE doc_id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var kind string
		var data []byte
		if err := rows.Scan(&kind, &data); err != nil {
			return nil, err
		}
		out[kind] = data
	}
	return out, rows.Err()
}

// DeleteDocument removes a document with its log and versions.
func (s *Store) DeleteDocument(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{"DELETE FROM document_updates WHERE doc_id=?", "DELETE FROM document_versions WHERE doc_id=?", "DELETE FROM documents WHERE id=?"} {
		if _, err = tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadDocuments returns every stored document with its update log.
func (s *Store) LoadDocuments() ([]DocumentRecord, error) {
	rows, err := s.db.Query("SELECT id,root,path,meta,baseline,snapshot,snapshot_rev FROM documents ORDER BY id")
	if err != nil {
		return nil, err
	}
	var out []DocumentRecord
	for rows.Next() {
		var r DocumentRecord
		if err := rows.Scan(&r.ID, &r.Root, &r.Path, &r.Meta, &r.Baseline, &r.Snapshot, &r.SnapshotRev); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	for i := range out {
		updates, err := s.db.Query("SELECT rev,data FROM document_updates WHERE doc_id=? AND rev>? ORDER BY rev", out[i].ID, out[i].SnapshotRev)
		if err != nil {
			return nil, err
		}
		for updates.Next() {
			var u DocumentUpdate
			if err := updates.Scan(&u.Rev, &u.Data); err != nil {
				updates.Close()
				return nil, err
			}
			out[i].Updates = append(out[i].Updates, u)
		}
		err = updates.Err()
		if closeErr := updates.Close(); err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		for j, u := range out[i].Updates {
			if want := out[i].SnapshotRev + int64(j) + 1; u.Rev != want {
				out[i].Damaged = fmt.Sprintf("the update log has a gap at revision %d", want)
				break
			}
		}
	}
	return out, nil
}

// QuarantineDocument moves a document that cannot be loaded out of the way:
// its row moves to document_quarantine with the reason, so the file can be
// opened again as a new document, while its log and versions stay stored
// under the old ID for recovery.
func (s *Store) QuarantineDocument(id, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO document_quarantine(id,root,path,reason,meta,baseline,snapshot,snapshot_rev)
		SELECT id,root,path,?,meta,baseline,snapshot,snapshot_rev FROM documents WHERE id=?`, reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDocumentMissing
	}
	if _, err = tx.Exec("DELETE FROM documents WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadQuarantined lists quarantined documents.
func (s *Store) LoadQuarantined() ([]QuarantinedDocument, error) {
	rows, err := s.db.Query("SELECT id,root,path,reason FROM document_quarantine ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuarantinedDocument
	for rows.Next() {
		var q QuarantinedDocument
		if err := rows.Scan(&q.ID, &q.Root, &q.Path, &q.Reason); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// DeleteQuarantined permanently removes a quarantined document with its
// retained log and versions.
func (s *Store) DeleteQuarantined(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM document_quarantine WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrDocumentMissing
	}
	for _, q := range []string{"DELETE FROM document_updates WHERE doc_id=?", "DELETE FROM document_versions WHERE doc_id=?"} {
		if _, err = tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
