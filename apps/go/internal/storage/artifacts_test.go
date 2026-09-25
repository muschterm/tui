package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func artifactStore(t *testing.T) (*Store, protocol.Snapshot, string) {
	t.Helper()
	s, snap := deletionStore(t)
	dir := filepath.Join(t.TempDir(), "artifacts")
	if err := s.UseArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	return s, snap, dir
}

func publish(t *testing.T, s *Store, id string, data []byte, created time.Time) {
	t.Helper()
	sum := sha256.Sum256(data)
	a := Artifact{ArtifactInfo: protocol.ArtifactInfo{ID: id, Name: "n", MediaType: "text/plain", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, CreatedAt: created}
	if err := s.PublishArtifact(a, data, 1<<20); err != nil {
		t.Fatal(err)
	}
}

func id32(c byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestPublicationIsAtomicAndSweepReconciles(t *testing.T) {
	s, _, dir := artifactStore(t)
	// A failed row insert after the rename leaves neither a row nor a file.
	beforeArtifactInsert = func() error { return errors.New("injected") }
	sum := sha256.Sum256([]byte("x"))
	failed := Artifact{ArtifactInfo: protocol.ArtifactInfo{ID: id32('a'), Name: "n", MediaType: "text/plain", SHA256: hex.EncodeToString(sum[:]), Size: 1}}
	err := s.PublishArtifact(failed, []byte("x"), 1<<20)
	beforeArtifactInsert = nil
	if err == nil {
		t.Fatal("injected failure published")
	}
	if _, ok, _ := s.Artifact(failed.ID); ok || exists(filepath.Join(dir, failed.ID)) {
		t.Fatal("failed publication left a reference or file")
	}
	// Quota is checked against staged bytes.
	publish(t, s, id32('b'), []byte("hello"), time.Now())
	if err := s.PublishArtifact(Artifact{ArtifactInfo: protocol.ArtifactInfo{ID: id32('c'), Size: 3}}, []byte("abc"), 6); err == nil {
		t.Fatal("staged quota ignored")
	}
	// Crash leftovers: a temporary file, a file without a row, a row without a file.
	for _, name := range []string{artifactTempPrefix + "123", id32('d')} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	publish(t, s, id32('e'), []byte("vanishing"), time.Now())
	if err := os.Remove(filepath.Join(dir, id32('e'))); err != nil {
		t.Fatal(err)
	}
	if result, _ := s.SweepArtifacts(time.Now(), 24*time.Hour); result != (SweepResult{Orphaned: 1, Unavailable: 1}) {
		t.Fatal("first sweep; a fresh temporary file of a running upload must stay", result)
	}
	if _, err := s.db.Exec("UPDATE artifacts SET unavailable=0"); err != nil {
		t.Fatal(err)
	}
	result, err := s.SweepArtifacts(time.Now().Add(time.Hour), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result != (SweepResult{Temporary: 1, Unavailable: 1}) {
		t.Fatalf("sweep %+v", result)
	}
	if exists(filepath.Join(dir, artifactTempPrefix+"123")) || exists(filepath.Join(dir, id32('d'))) || !exists(filepath.Join(dir, id32('b'))) {
		t.Fatal("sweep removed the wrong files")
	}
	if a, ok, _ := s.Artifact(id32('e')); !ok || !a.Unavailable {
		t.Fatal("missing file not marked unavailable", a)
	}
	if _, _, err := s.ReadArtifact(id32('e'), 1<<20); err == nil {
		t.Fatal("unavailable artifact read as content")
	}
	if _, data, err := s.ReadArtifact(id32('b'), 1<<20); err != nil || string(data) != "hello" {
		t.Fatal("published artifact unreadable", err)
	}
}

func TestStagedTTLExpires(t *testing.T) {
	s, _, dir := artifactStore(t)
	publish(t, s, id32('a'), []byte("old"), time.Now().Add(-8*24*time.Hour))
	publish(t, s, id32('b'), []byte("new"), time.Now())
	result, err := s.SweepArtifacts(time.Now(), 7*24*time.Hour)
	if err != nil || result.Expired != 1 {
		t.Fatal(result, err)
	}
	if _, ok, _ := s.Artifact(id32('a')); ok || exists(filepath.Join(dir, id32('a'))) {
		t.Fatal("expired artifact retained")
	}
	if _, ok, _ := s.Artifact(id32('b')); !ok {
		t.Fatal("fresh artifact expired")
	}
}

func TestBindingDeletionAndProjectRemoval(t *testing.T) {
	s, snap, dir := artifactStore(t)
	publish(t, s, id32('a'), []byte("gone"), time.Now().Add(-30*24*time.Hour))
	publish(t, s, id32('b'), []byte("live"), time.Now())
	send := protocol.Command{Version: 1, ID: "send", Kind: "prompt.send", ThreadID: "gone", Text: "t", Attachments: []protocol.Attachment{{Kind: "file", ArtifactID: id32('a')}}}
	if err := s.Save(snap, &send, &protocol.Receipt{ID: "send", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	// Accepted artifacts are no longer staged, so the TTL does not apply.
	if _, err := s.SweepArtifacts(time.Now(), 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := s.Artifact(id32('a')); !ok || a.State != "accepted" || a.ThreadID != "gone" || a.CommandID != "send" {
		t.Fatal("binding not recorded", a)
	}
	// Another thread cannot take an accepted artifact; nothing commits.
	steal := protocol.Command{Version: 1, ID: "steal", Kind: "prompt.send", ThreadID: "live", Text: "t", Attachments: []protocol.Attachment{{Kind: "file", ArtifactID: id32('a')}}}
	if err := s.Save(snap, &steal, &protocol.Receipt{ID: "steal", State: "accepted"}); err == nil {
		t.Fatal("artifact rebound to another thread")
	}
	if r, _ := s.Lookup(steal); r != nil {
		t.Fatal("rejected binding recorded a receipt")
	}
	// The same thread may reference it again without rebinding.
	again := protocol.Command{Version: 1, ID: "again", Kind: "prompt.send", ThreadID: "gone", Text: "t", Attachments: []protocol.Attachment{{Kind: "file", ArtifactID: id32('a')}}}
	if err := s.Save(snap, &again, &protocol.Receipt{ID: "again", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.Artifact(id32('a')); a.CommandID != "send" {
		t.Fatal("second reference rebound the artifact", a)
	}
	start := protocol.Command{Version: 1, ID: "start", Kind: "thread.start", Text: "t", Attachments: []protocol.Attachment{{Kind: "file", ArtifactID: id32('b')}}}
	if err := s.Save(snap, &start, &protocol.Receipt{ID: "start", State: "accepted", TargetID: "live"}); err != nil {
		t.Fatal(err)
	}
	next := snap
	next.Threads = next.Threads[1:]
	next.Terminals = next.Terminals[1:]
	del := protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "gone"}
	if err := s.Save(next, &del, &protocol.Receipt{ID: "delete", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Artifact(id32('a')); ok || exists(filepath.Join(dir, id32('a'))) {
		t.Fatal("deleted thread artifact retained")
	}
	if _, ok, _ := s.Artifact(id32('b')); !ok || !exists(filepath.Join(dir, id32('b'))) {
		t.Fatal("live thread artifact removed")
	}
	next.Projects = []protocol.Project{{ID: "p"}}
	next.Threads[0].ProjectID = "p"
	if err := s.Save(next, nil, nil); err != nil {
		t.Fatal(err)
	}
	removed := next
	removed.Projects, removed.Threads, removed.Terminals = nil, nil, nil
	remove := protocol.Command{Version: 1, ID: "remove", Kind: "project.remove", ProjectID: "p"}
	if err := s.Save(removed, &remove, &protocol.Receipt{ID: "remove", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Artifact(id32('b')); ok || exists(filepath.Join(dir, id32('b'))) {
		t.Fatal("removed project artifact retained")
	}
}

func TestMigrationFromVersionOneAddsArtifactsWithBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE state (id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL); CREATE TABLE commands (id TEXT PRIMARY KEY, command BLOB NOT NULL, receipt BLOB NOT NULL); CREATE TABLE views (id TEXT PRIMARY KEY, data BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 1); INSERT INTO state VALUES(1,'{"version":1,"revision":7,"threads":[{"ID":"kept","Queue":[{"ID":"p","Attachments":[{"Kind":"file","Name":"a","Source":"a","Content":"old"}]}]}]}'); PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, ok, err := s.Load()
	if err != nil || !ok || snap.Revision != 7 || snap.Threads[0].Queue[0].Attachments[0].Content != "old" {
		t.Fatal("v1 snapshot did not decode", snap, err)
	}
	var version, tables int
	s.db.QueryRow("PRAGMA user_version").Scan(&version)
	s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='artifacts'").Scan(&tables)
	if version != 3 || tables != 1 {
		t.Fatal("artifacts schema missing", version, tables)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "recovery-before-v3-*.sqlite"))
	if len(backups) != 1 {
		t.Fatal("no pre-migration backup", backups)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	backup.QueryRow("PRAGMA user_version").Scan(&version)
	backup.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='artifacts'").Scan(&tables)
	if version != 1 || tables != 0 {
		t.Fatal("backup taken after migration", version, tables)
	}
}

func TestUnavailableIsNotStickyAndDeleteIsStagedOnly(t *testing.T) {
	s, snap, dir := artifactStore(t)
	publish(t, s, id32('a'), []byte("bytes"), time.Now())
	path := filepath.Join(dir, id32('a'))
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenArtifact(id32('a')); err == nil {
		t.Fatal("missing file opened")
	}
	if a, _, _ := s.Artifact(id32('a')); !a.Unavailable {
		t.Fatal("missing file not flagged")
	}
	if err := os.Rename(path+".bak", path); err != nil {
		t.Fatal(err)
	}
	if _, data, err := s.ReadArtifact(id32('a'), 1<<20); err != nil || string(data) != "bytes" {
		t.Fatal("restored file still unavailable", err)
	}
	if a, _, _ := s.Artifact(id32('a')); a.Unavailable {
		t.Fatal("flag not cleared on open")
	}
	// The sweep also clears a flag once the file is back.
	if _, err := s.db.Exec("UPDATE artifacts SET unavailable=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepArtifacts(time.Now(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.Artifact(id32('a')); a.Unavailable {
		t.Fatal("sweep kept the flag")
	}
	publish(t, s, id32('b'), []byte("sent"), time.Now())
	send := protocol.Command{Version: 1, ID: "send", Kind: "prompt.send", ThreadID: "live", Text: "t", Attachments: []protocol.Attachment{{ArtifactID: id32('b')}}}
	if err := s.Save(snap, &send, &protocol.Receipt{ID: "send", State: "accepted"}); err != nil {
		t.Fatal(err)
	}
	var pe *protocol.Error
	if err := s.DeleteArtifact(id32('b')); !errors.As(err, &pe) || pe.Code != "accepted" {
		t.Fatal("accepted artifact deleted", err)
	}
	if err := s.DeleteArtifact(id32('a')); err != nil || exists(path) {
		t.Fatal("staged delete", err)
	}
	if err := s.DeleteArtifact(id32('a')); !errors.As(err, &pe) || pe.Code != "not_found" {
		t.Fatal("repeat delete", err)
	}
}
