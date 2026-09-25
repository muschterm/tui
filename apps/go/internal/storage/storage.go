// Package storage persists snapshots and deduplication receipts in one transaction.
package storage

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	_ "modernc.org/sqlite"
)

// schemaVersion 2 adds artifact metadata; every upgrade first writes a
// synced pre-migration backup next to the database.
const schemaVersion = 2

// Store persists the authoritative snapshot, command receipts and client
// views in one SQLite database. Artifact bytes live under artifactDir once
// UseArtifacts has been called; artifactMu serializes their publication with
// sweeps so a sweep never sees a published file before its row.
type Store struct {
	db          *sql.DB
	artifactDir string
	artifactMu  sync.Mutex
}

// Open opens or creates the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > schemaVersion {
		db.Close()
		return nil, &protocol.Error{Code: "schema_version", Message: fmt.Sprintf("database schema is newer than supported version %d", schemaVersion)}
	}
	if version < schemaVersion {
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			db.Close()
			return nil, err
		}
		if tables > 0 {
			backup, createErr := os.CreateTemp(filepath.Dir(path), fmt.Sprintf("recovery-before-v%d-*.sqlite", schemaVersion))
			if createErr != nil {
				db.Close()
				return nil, createErr
			}
			backupPath := backup.Name()
			backup.Close()
			if _, err = db.Exec("VACUUM INTO ?", backupPath); err != nil {
				db.Close()
				return nil, err
			}
			backup, err = os.OpenFile(backupPath, os.O_RDWR, 0600)
			if err != nil {
				db.Close()
				return nil, err
			}
			err = backup.Sync()
			closeErr := backup.Close()
			if err = errors.Join(err, closeErr); err != nil {
				db.Close()
				return nil, err
			}
			directory, openErr := os.Open(filepath.Dir(path))
			if openErr != nil {
				db.Close()
				return nil, openErr
			}
			syncErr := directory.Sync()
			directory.Close()
			if syncErr != nil {
				db.Close()
				return nil, syncErr
			}
		}
		tx, beginErr := db.Begin()
		if beginErr != nil {
			db.Close()
			return nil, beginErr
		}
		_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL); CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, command BLOB NOT NULL, receipt BLOB NOT NULL); CREATE TABLE IF NOT EXISTS views (id TEXT PRIMARY KEY, data BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 1);`)
		if err == nil {
			var columns int
			err = tx.QueryRow("SELECT count(*) FROM pragma_table_info('views') WHERE name='revision'").Scan(&columns)
			if err == nil && columns == 0 {
				_, err = tx.Exec("ALTER TABLE views ADD COLUMN revision INTEGER NOT NULL DEFAULT 1")
			}
		}
		if err == nil {
			_, err = tx.Exec(artifactSchema)
		}
		if err == nil {
			_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion))
		}
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;"); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Load returns the stored snapshot and whether one exists.
func (s *Store) Load() (protocol.Snapshot, bool, error) {
	var b []byte
	err := s.db.QueryRow("SELECT data FROM state WHERE id=1").Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Snapshot{}, false, nil
	}
	if err != nil {
		return protocol.Snapshot{}, false, err
	}
	var snap protocol.Snapshot
	err = json.Unmarshal(b, &snap)
	return snap, true, err
}

// Lookup returns the receipt recorded for a command ID, or nil when the
// command has not been seen.
func (s *Store) Lookup(c protocol.Command) (*protocol.Receipt, error) {
	var cmd, b []byte
	err := s.db.QueryRow("SELECT command,receipt FROM commands WHERE id=?", c.ID).Scan(&cmd, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want, _ := json.Marshal(c)
	if !bytes.Equal(want, cmd) && !bytes.Equal(commandFingerprint(want), cmd) {
		return nil, &protocol.Error{Code: "identity_conflict", Message: "command ID was already used with different content"}
	}
	var r protocol.Receipt
	err = json.Unmarshal(b, &r)
	return &r, err
}

// Save stores the snapshot and, when given, the command with its receipt in
// one transaction so a retry cannot observe a partial outcome.
func (s *Store) Save(snap protocol.Snapshot, c *protocol.Command, r *protocol.Receipt) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c != nil && c.Kind == "project.remove" {
		if err = purgeProject(tx, snap, c.ProjectID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO state(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", b); err != nil {
		return err
	}
	var removed []string
	if c != nil && (c.Kind == "thread.delete" || c.Kind == "project.remove") {
		if removed, err = deleteUnownedArtifacts(tx, liveThreads(snap)); err != nil {
			return err
		}
	}
	if c != nil && r != nil {
		threadID := c.ThreadID
		if c.Kind == "thread.start" {
			threadID = r.TargetID
		}
		if err = bindArtifacts(tx, *c, threadID, time.Now()); err != nil {
			return err
		}
	}
	if c != nil {
		cb, _ := json.Marshal(c)
		storedReceipt := r
		if c.Kind == "thread.delete" {
			if err = purgeThread(tx, snap, c.ThreadID); err != nil {
				return err
			}
			cb = commandFingerprint(cb)
			minimal := *r
			minimal.TargetID = ""
			storedReceipt = &minimal
		}
		if c.Kind == "project.remove" {
			cb = commandFingerprint(cb)
		}
		rb, _ := json.Marshal(storedReceipt)
		if _, err = tx.Exec("INSERT INTO commands(id,command,receipt) VALUES(?,?,?)", c.ID, cb, rb); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Bytes go only after their rows are committed away; a crash in between
	// leaves files without rows, which the startup sweep removes.
	s.removeArtifactFiles(removed)
	return nil
}

// LoadView returns a client view; an unknown id yields an empty document at
// revision zero.
func (s *Store) LoadView(id string) (protocol.View, error) {
	var v protocol.View
	var data []byte
	err := s.db.QueryRow("SELECT data,revision FROM views WHERE id=?", id).Scan(&data, &v.Revision)
	v.Data = json.RawMessage(data)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.View{Data: json.RawMessage(`{}`)}, nil
	}
	return v, err
}

// PutView stores a client view when its current revision equals expected,
// rejecting invalid payloads and stale revisions.
func (s *Store) PutView(id string, b json.RawMessage, expected int64) (protocol.View, error) {
	if !json.Valid(b) || len(b) > 128*1024 || id == "" || len(id) > 128 || expected < 0 {
		return protocol.View{}, &protocol.Error{Code: "invalid_view", Message: "invalid view payload, client identity, or revision"}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return protocol.View{}, err
	}
	defer tx.Rollback()
	var state []byte
	err = tx.QueryRow("SELECT data FROM state WHERE id=1").Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.View{}, err
	}
	if err == nil {
		var snap protocol.Snapshot
		if err = json.Unmarshal(state, &snap); err != nil {
			return protocol.View{}, err
		}
		deletedCommands, loadErr := tombstonedCommands(tx)
		if loadErr != nil {
			return protocol.View{}, loadErr
		}
		b, err = protocol.PruneThreadViewCommands(b, liveThreads(snap), deletedCommands)
		if err != nil {
			return protocol.View{}, &protocol.Error{Code: "invalid_view", Message: err.Error()}
		}
	}
	var next int64
	if expected == 0 {
		err = tx.QueryRow("INSERT INTO views(id,data,revision) VALUES(?,?,1) ON CONFLICT(id) DO NOTHING RETURNING revision", id, []byte(b)).Scan(&next)
	} else {
		err = tx.QueryRow("UPDATE views SET data=?,revision=revision+1 WHERE id=? AND revision=? RETURNING revision", []byte(b), id, expected).Scan(&next)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.View{}, &protocol.Error{Code: "stale_view", Message: "client view changed; preserve the local draft and reconcile before saving"}
	}
	if err != nil {
		return protocol.View{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.View{}, err
	}
	return protocol.View{Data: b, Revision: next}, nil
}

// View returns a client view's data without its revision.
func (s *Store) View(id string) (json.RawMessage, error) {
	v, err := s.LoadView(id)
	return v.Data, err
}

// SaveView stores b against the view's current revision.
func (s *Store) SaveView(id string, b json.RawMessage) error {
	v, err := s.LoadView(id)
	if err != nil {
		return err
	}
	_, err = s.PutView(id, b, v.Revision)
	return err
}

// Fingerprints retain retry identity without retaining deleted thread payloads.
// This is logical deletion; SQLite journals/backups are not forensic erasure.
func commandFingerprint(encoded []byte) []byte {
	return fmt.Appendf(nil, "sha256:%x", sha256.Sum256(encoded))
}

func liveThreads(snap protocol.Snapshot) map[string]bool {
	live := make(map[string]bool, len(snap.Threads))
	for _, thread := range snap.Threads {
		live[thread.ID] = true
	}
	return live
}

func purgeThread(tx *sql.Tx, snap protocol.Snapshot, threadID string) error {
	live := liveThreads(snap)
	if threadID == "" || live[threadID] {
		return fmt.Errorf("deleted thread remains in snapshot")
	}
	for _, terminal := range snap.Terminals {
		if terminal.ThreadID == threadID {
			return fmt.Errorf("deleted thread terminal remains in snapshot")
		}
	}
	// A lost acknowledgment of thread.create or thread.start must not recreate a deleted
	// thread. Retain its identity/fingerprint, but erase its title and target.
	created, err := tx.Query("SELECT id,command,receipt FROM commands WHERE json_extract(CASE WHEN json_valid(command) THEN command ELSE '{}' END, '$.Kind') IN ('thread.create','thread.start') AND json_extract(receipt, '$.TargetID') = ?", threadID)
	if err != nil {
		return err
	}
	type tombstone struct {
		id               string
		command, receipt []byte
	}
	var tombstones []tombstone
	deletedCommands := map[string]bool{}
	for created.Next() {
		var id string
		var command, receipt []byte
		if err := created.Scan(&id, &command, &receipt); err != nil {
			created.Close()
			return err
		}
		var previous protocol.Receipt
		if err := json.Unmarshal(receipt, &previous); err != nil {
			created.Close()
			return err
		}
		minimal, _ := json.Marshal(protocol.Receipt{ID: id, State: "deleted", Revision: previous.Revision})
		tombstones = append(tombstones, tombstone{id, commandFingerprint(command), minimal})
		deletedCommands[id] = true
	}
	err = created.Err()
	closeErr := created.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	for _, item := range tombstones {
		if _, err := tx.Exec("UPDATE commands SET command=?,receipt=? WHERE id=?", item.command, item.receipt, item.id); err != nil {
			return err
		}
	}
	// Hash-only records are deliberately not JSON and have no scope data.
	if _, err := tx.Exec("DELETE FROM commands WHERE json_extract(CASE WHEN json_valid(command) THEN command ELSE '{}' END, '$.ThreadID') = ?", threadID); err != nil {
		return err
	}
	return pruneViews(tx, live, deletedCommands)
}

func pruneViews(tx *sql.Tx, live, deletedCommands map[string]bool) error {
	rows, err := tx.Query("SELECT id,data FROM views")
	if err != nil {
		return err
	}
	type projection struct {
		id   string
		data json.RawMessage
	}
	var changes []projection
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			rows.Close()
			return err
		}
		projected, projectErr := protocol.PruneThreadViewCommands(data, live, deletedCommands)
		if projectErr != nil {
			// A view this server cannot interpret must not veto authoritative
			// deletion; its bytes stay untouched for the owning client.
			continue
		}
		if !bytes.Equal(projected, data) {
			changes = append(changes, projection{id, projected})
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	for _, change := range changes {
		if _, err = tx.Exec("UPDATE views SET data=?,revision=revision+1 WHERE id=?", []byte(change.data), change.id); err != nil {
			return err
		}
	}
	return nil
}

func tombstonedCommands(tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.Query("SELECT id FROM commands WHERE json_extract(receipt, '$.State') = 'deleted'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// Project removal and all owned records are committed together with its receipt.
// Files at project.Path are deliberately outside this storage operation.
func purgeProject(tx *sql.Tx, snap protocol.Snapshot, projectID string) error {
	for _, p := range snap.Projects {
		if p.ID == projectID {
			return fmt.Errorf("removed project remains in snapshot")
		}
	}
	var raw []byte
	if err := tx.QueryRow("SELECT data FROM state WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	var previous protocol.Snapshot
	if err := json.Unmarshal(raw, &previous); err != nil {
		return err
	}
	for _, t := range previous.Threads {
		if t.ProjectID == projectID {
			if err := purgeThread(tx, snap, t.ID); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query("SELECT id,command,receipt FROM commands WHERE json_extract(CASE WHEN json_valid(command) THEN command ELSE '{}' END, '$.ProjectID') = ? OR (json_extract(CASE WHEN json_valid(command) THEN command ELSE '{}' END, '$.Kind') = 'project.add' AND json_extract(receipt, '$.TargetID') = ?)", projectID, projectID)
	if err != nil {
		return err
	}
	type record struct {
		id               string
		command, receipt []byte
	}
	var records []record
	for rows.Next() {
		var item record
		if err := rows.Scan(&item.id, &item.command, &item.receipt); err != nil {
			rows.Close()
			return err
		}
		records = append(records, item)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	for _, item := range records {
		var receipt protocol.Receipt
		if err := json.Unmarshal(item.receipt, &receipt); err != nil {
			return err
		}
		receipt.State, receipt.TargetID = "deleted", ""
		encoded, _ := json.Marshal(receipt)
		if _, err := tx.Exec("UPDATE commands SET command=?,receipt=? WHERE id=?", commandFingerprint(item.command), encoded, item.id); err != nil {
			return err
		}
	}
	deleted, err := tombstonedCommands(tx)
	if err != nil {
		return err
	}
	return pruneViews(tx, liveThreads(snap), deleted)
}

// SaveReceipt stores snap and replaces the receipt of the already recorded
// command c in one transaction. It is the second phase of a two-phase
// command (Save records the running receipt first). A command row removed
// meanwhile, for example by deleting its thread, is not recreated.
func (s *Store) SaveReceipt(snap protocol.Snapshot, c protocol.Command, r protocol.Receipt) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	cb, _ := json.Marshal(c)
	rb, _ := json.Marshal(r)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO state(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", b); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE commands SET receipt=? WHERE id=? AND command=?", rb, c.ID, cb); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveRunningReceipts rewrites every receipt still in state "running",
// left by a server that stopped between the phases of a two-phase command,
// to state, with code and message on its Git result (protocol.GitResult's
// lowercase JSON keys). It returns the count.
func (s *Store) ResolveRunningReceipts(state, code, message string) (int64, error) {
	res, err := s.db.Exec(`UPDATE commands SET receipt=CASE WHEN json_type(receipt,'$.Git')='object'
		THEN json_set(receipt,'$.State',?1,'$.Git.state',?1,'$.Git.code',?2,'$.Git.message',?3)
		ELSE json_set(receipt,'$.State',?1) END
		WHERE json_valid(receipt) AND json_extract(receipt,'$.State')='running'`, state, code, message)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
