package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// artifactSchema is created by the version 2 migration. Bytes live in files
// named by id under the artifact directory; a row is the only reference.
const artifactSchema = `CREATE TABLE IF NOT EXISTS artifacts (
	id TEXT PRIMARY KEY,
	sha256 TEXT NOT NULL,
	size INTEGER NOT NULL,
	media_type TEXT NOT NULL,
	name TEXT NOT NULL,
	width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK(state IN ('staged','accepted')),
	thread_id TEXT NOT NULL DEFAULT '',
	command_id TEXT NOT NULL DEFAULT '',
	unavailable INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	accepted_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS artifacts_thread ON artifacts(thread_id);`

// Artifact is one stored artifact's metadata row.
type Artifact struct {
	protocol.ArtifactInfo
	ThreadID, CommandID   string
	CreatedAt, AcceptedAt time.Time
}

// SweepResult counts what one artifact sweep changed.
type SweepResult struct{ Temporary, Orphaned, Expired, Unavailable int }

// beforeArtifactInsert lets tests fail publication after the file is in place.
var beforeArtifactInsert func() error

const artifactTempPrefix = "tmp-"

// tempFileAge is how old an unlocked upload's temporary file must be before a
// sweep treats it as interrupted.
const tempFileAge = 10 * time.Minute

// UseArtifacts enables artifact storage in dir, creating it privately.
func (s *Store) UseArtifacts(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	s.artifactMu.Lock()
	s.artifactDir = dir
	s.artifactMu.Unlock()
	return nil
}

func validArtifactID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func artifactFailure(code, message string) error {
	return &protocol.Error{Code: code, Message: message}
}

// PublishArtifact durably writes data and then commits its staged row. The
// file is written to a temporary name and synced without the artifact lock;
// the lock covers only the quota re-check, rename, directory sync and insert,
// so a committed row never names a partial file and slow uploads do not block
// deletes or sweeps. A failure after the rename removes the file, and a crash
// leaves only files the sweep deletes. stagedLimit bounds staged bytes.
func (s *Store) PublishArtifact(a Artifact, data []byte, stagedLimit int64) error {
	s.artifactMu.Lock()
	dir := s.artifactDir
	s.artifactMu.Unlock()
	if dir == "" {
		return artifactFailure("unavailable", "artifact storage is not configured")
	}
	if !validArtifactID(a.ID) || int64(len(data)) != a.Size {
		return errors.New("invalid artifact record")
	}
	if err := s.checkStagedQuota(s.db, a.Size, stagedLimit); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, artifactTempPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	final := filepath.Join(dir, a.ID)
	// A sweep may have removed the temporary file while it was unlocked.
	if _, err := os.Lstat(tmpName); err != nil {
		return errors.New("artifact upload was interrupted; try again")
	}
	if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmpName)
		return errors.New("artifact identity already exists")
	}
	if err := s.checkStagedQuota(s.db, a.Size, stagedLimit); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = syncDir(dir); err == nil && beforeArtifactInsert != nil {
		err = beforeArtifactInsert()
	}
	if err == nil {
		err = s.insertArtifact(a, stagedLimit)
	}
	if err != nil {
		_ = os.Remove(final)
		return err
	}
	return nil
}

func (s *Store) checkStagedQuota(q interface {
	QueryRow(string, ...any) *sql.Row
}, size, stagedLimit int64) error {
	var staged int64
	if err := q.QueryRow("SELECT coalesce(sum(size),0) FROM artifacts WHERE state='staged'").Scan(&staged); err != nil {
		return err
	}
	if staged+size > stagedLimit {
		return artifactFailure("capacity", fmt.Sprintf("unsent attachments would exceed %d MiB; remove unsent attachments from drafts or send them (unsent attachments also expire after 7 days)", stagedLimit>>20))
	}
	return nil
}

// DeleteArtifact removes a staged, unaccepted artifact: its row, then its
// file. Accepted artifacts are refused; unknown ids report not_found, so a
// repeated delete is harmless.
func (s *Store) DeleteArtifact(id string) error {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	a, ok, err := s.Artifact(id)
	if err != nil {
		return err
	}
	if !ok {
		return artifactFailure("not_found", "artifact is missing or expired")
	}
	if a.State != "staged" {
		return artifactFailure("accepted", "a sent attachment stays with its thread")
	}
	res, err := s.db.Exec("DELETE FROM artifacts WHERE id=? AND state='staged'", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return artifactFailure("accepted", "a sent attachment stays with its thread")
	}
	if s.artifactDir != "" {
		_ = os.Remove(filepath.Join(s.artifactDir, id))
	}
	return nil
}

func (s *Store) insertArtifact(a Artifact, stagedLimit int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkStagedQuota(tx, a.Size, stagedLimit); err != nil {
		return err
	}
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if _, err = tx.Exec("INSERT INTO artifacts(id,sha256,size,media_type,name,width,height,state,created_at) VALUES(?,?,?,?,?,?,?,'staged',?)", a.ID, a.SHA256, a.Size, a.MediaType, a.Name, a.Width, a.Height, created.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

const artifactColumns = "id,sha256,size,media_type,name,width,height,state,thread_id,command_id,unavailable,created_at,accepted_at"

func scanArtifact(row interface{ Scan(...any) error }) (Artifact, error) {
	var a Artifact
	var created, accepted int64
	err := row.Scan(&a.ID, &a.SHA256, &a.Size, &a.MediaType, &a.Name, &a.Width, &a.Height, &a.State, &a.ThreadID, &a.CommandID, &a.Unavailable, &created, &accepted)
	a.CreatedAt = time.Unix(created, 0)
	if accepted != 0 {
		a.AcceptedAt = time.Unix(accepted, 0)
	}
	return a, err
}

// Artifact returns the metadata for id and whether a row exists.
func (s *Store) Artifact(id string) (Artifact, bool, error) {
	a, err := scanArtifact(s.db.QueryRow("SELECT "+artifactColumns+" FROM artifacts WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	return a, err == nil, err
}

// OpenArtifact returns an artifact's metadata and an open file for its bytes.
// A row whose file is gone is marked unavailable, never served as empty.
func (s *Store) OpenArtifact(id string) (Artifact, *os.File, error) {
	a, ok, err := s.Artifact(id)
	if err != nil {
		return a, nil, err
	}
	if !ok {
		return a, nil, artifactFailure("not_found", "artifact is missing or expired")
	}
	s.artifactMu.Lock()
	dir := s.artifactDir
	s.artifactMu.Unlock()
	if dir == "" {
		return a, nil, artifactFailure("unavailable", "artifact storage is not configured")
	}
	f, err := os.Open(filepath.Join(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		_, _ = s.db.Exec("UPDATE artifacts SET unavailable=1 WHERE id=?", id)
		return a, nil, artifactFailure("unavailable", "artifact content is unavailable")
	}
	if err != nil {
		return a, nil, err
	}
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() || info.Size() != a.Size {
		f.Close()
		return a, nil, artifactFailure("unavailable", "artifact content does not match its record")
	}
	if a.Unavailable {
		// A restored file makes the record usable again; ReadArtifact still
		// verifies its digest.
		_, _ = s.db.Exec("UPDATE artifacts SET unavailable=0 WHERE id=?", id)
		a.Unavailable = false
	}
	return a, f, nil
}

// ReadArtifact returns an artifact's verified bytes when it is at most limit.
func (s *Store) ReadArtifact(id string, limit int64) (Artifact, []byte, error) {
	a, f, err := s.OpenArtifact(id)
	if err != nil {
		return a, nil, err
	}
	defer f.Close()
	if a.Size > limit {
		return a, nil, artifactFailure("capacity", "artifact exceeds the read bound")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return a, nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != a.Size || hex.EncodeToString(sum[:]) != a.SHA256 {
		return a, nil, artifactFailure("unavailable", "artifact content does not match its record")
	}
	return a, data, nil
}

// SweepArtifacts removes interrupted temporary files, files without rows and
// staged rows older than stagedTTL, and marks rows whose file is missing as
// unavailable.
func (s *Store) SweepArtifacts(now time.Time, stagedTTL time.Duration) (SweepResult, error) {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	var result SweepResult
	if s.artifactDir == "" {
		return result, nil
	}
	rows, err := s.db.Query("DELETE FROM artifacts WHERE state='staged' AND created_at < ? RETURNING id", now.Add(-stagedTTL).Unix())
	if err != nil {
		return result, err
	}
	expired, err := collectIDs(rows)
	if err != nil {
		return result, err
	}
	for _, id := range expired {
		_ = os.Remove(filepath.Join(s.artifactDir, id))
	}
	result.Expired = len(expired)
	rows, err = s.db.Query("SELECT id FROM artifacts")
	if err != nil {
		return result, err
	}
	ids, err := collectIDs(rows)
	if err != nil {
		return result, err
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	entries, err := os.ReadDir(s.artifactDir)
	if err != nil {
		return result, err
	}
	present := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, artifactTempPrefix):
			// Uploads write temporary files unlocked; only stale ones are
			// interrupted publications.
			if info, err := entry.Info(); err == nil && info.ModTime().After(now.Add(-tempFileAge)) {
				continue
			}
			if os.Remove(filepath.Join(s.artifactDir, name)) == nil {
				result.Temporary++
			}
		case !known[name]:
			if os.Remove(filepath.Join(s.artifactDir, name)) == nil {
				result.Orphaned++
			}
		default:
			present[name] = true
		}
	}
	for _, id := range ids {
		if present[id] {
			if _, err := s.db.Exec("UPDATE artifacts SET unavailable=0 WHERE id=? AND unavailable=1", id); err != nil {
				return result, err
			}
			continue
		}
		res, err := s.db.Exec("UPDATE artifacts SET unavailable=1 WHERE id=? AND unavailable=0", id)
		if err != nil {
			return result, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.Unavailable++
		}
	}
	return result, nil
}

func collectIDs(rows *sql.Rows) ([]string, error) {
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err := rows.Err()
	return ids, errors.Join(err, rows.Close())
}

// bindArtifacts accepts every artifact a Send command references for its
// thread inside the command's transaction. An artifact already accepted for
// the same thread stays bound to its first command; any other state rejects
// the whole command.
func bindArtifacts(tx *sql.Tx, c protocol.Command, threadID string, now time.Time) error {
	if c.Kind != "thread.start" && c.Kind != "prompt.send" && c.Kind != "prompt.reopen-send" {
		return nil
	}
	for _, a := range c.Attachments {
		if a.ArtifactID == "" {
			continue
		}
		res, err := tx.Exec(`UPDATE artifacts SET state='accepted', thread_id=?,
			command_id=CASE WHEN state='staged' THEN ? ELSE command_id END,
			accepted_at=CASE WHEN state='staged' THEN ? ELSE accepted_at END
			WHERE id=? AND unavailable=0 AND (state='staged' OR thread_id=?)`, threadID, c.ID, now.Unix(), a.ArtifactID, threadID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			name := a.Name
			if name == "" {
				name = a.ArtifactID
			}
			return errors.Join(err, artifactFailure("attachment", fmt.Sprintf("%q: attachment is missing, expired or unavailable; attach it again", name)))
		}
	}
	return nil
}

// deleteUnownedArtifacts removes accepted rows whose thread is gone from the
// snapshot being committed and returns their ids for file removal after commit.
func deleteUnownedArtifacts(tx *sql.Tx, live map[string]bool) ([]string, error) {
	rows, err := tx.Query("SELECT id,thread_id FROM artifacts WHERE state='accepted'")
	if err != nil {
		return nil, err
	}
	var gone []string
	for rows.Next() {
		var id, thread string
		if err := rows.Scan(&id, &thread); err != nil {
			rows.Close()
			return nil, err
		}
		if !live[thread] {
			gone = append(gone, id)
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, id := range gone {
		if _, err := tx.Exec("DELETE FROM artifacts WHERE id=?", id); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

func (s *Store) removeArtifactFiles(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	if s.artifactDir == "" {
		return
	}
	for _, id := range ids {
		_ = os.Remove(filepath.Join(s.artifactDir, id))
	}
}
