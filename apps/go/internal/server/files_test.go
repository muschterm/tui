//go:build unix

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

func filesCode(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestFilesListOrderHiddenAndGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "b.txt", "b")
	writeFile(t, root, "a.txt", "a")
	writeFile(t, root, "Z/inner.txt", "z")
	writeFile(t, root, "a/x", "x")
	writeFile(t, root, ".env", "secret")
	writeFile(t, root, ".git/config", "")
	writeFile(t, root, ".GIT/x", "")
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l, err := listFiles(ctx, root, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Entries {
		got = append(got, e.Name+":"+e.Kind)
		if e.Token == "" {
			t.Errorf("%s has no token", e.Name)
		}
	}
	if want := "Z:dir a:dir a.txt:file b.txt:file link:symlink"; strings.Join(got, " ") != want {
		t.Fatalf("entries %q, want %q", strings.Join(got, " "), want)
	}
	l, _ = listFiles(ctx, root, "", "", true)
	names := ""
	for _, e := range l.Entries {
		names += e.Name + " "
	}
	if !strings.Contains(names, ".env") || strings.Contains(strings.ToLower(names), ".git ") {
		t.Fatalf("hidden listing %q", names)
	}
	for _, dir := range []string{".git", ".GIT", "a/../..", "../x", "/etc", "a/", "./a", "link"} {
		if _, err := listFiles(ctx, root, dir, "", true); err == nil {
			t.Errorf("listing %q succeeded", dir)
		}
	}
	if _, err := listFiles(ctx, root, "link", "", false); filesCode(err) != "not_directory" {
		t.Errorf("symlink dir: %v", err)
	}
	if _, err := listFiles(ctx, root, "a", "!!", false); filesCode(err) != "invalid" {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestFilesListPaginationStable(t *testing.T) {
	root := t.TempDir()
	for i := range 1203 {
		writeFile(t, root, fmt.Sprintf("f%04d", i), "")
	}
	for i := range 3 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	cursor, pages := "", 0
	for {
		l, err := listFiles(context.Background(), root, "", cursor, false)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(l.Entries) > protocol.FileListPage {
			t.Fatalf("page of %d", len(l.Entries))
		}
		for _, e := range l.Entries {
			all = append(all, e.Name)
		}
		if pages == 1 {
			// An entry added between pages sorts before the cursor and
			// does not shift later pages.
			writeFile(t, root, "f0000a", "")
		}
		if l.Next == "" {
			break
		}
		cursor = l.Next
	}
	if pages != 3 || len(all) != 1206 || all[0] != "d0" || all[3] != "f0000" || all[len(all)-1] != "f1202" {
		t.Fatalf("pages %d, %d entries, first %v last %s", pages, len(all), all[:4], all[len(all)-1])
	}
	seen := map[string]bool{}
	for _, n := range all {
		if seen[n] {
			t.Fatalf("duplicate %s", n)
		}
		seen[n] = true
	}
}

func TestFilesReadClassification(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "lf.txt", "a\nb\n")
	writeFile(t, root, "crlf.txt", "a\r\nb\r\n")
	writeFile(t, root, "mixed.txt", "a\r\nb\n")
	writeFile(t, root, "none.txt", "abc")
	writeFile(t, root, "bom.txt", "\xef\xbb\xbfhi\n")
	writeFile(t, root, "bin.dat", "ab\x00cd")
	writeFile(t, root, "noise.dat", strings.Repeat("\xff\xfe", 100))
	writeFile(t, root, "latin.txt", "caf\xe9 ok and plenty of ascii text around it\n")
	big := strings.Repeat("é", protocol.FileTextLimit/2) + "tail" // cut splits a rune
	writeFile(t, root, "big.txt", big)
	writeFile(t, root, "dir/esc.txt", "\x1b[31mred\n")
	ctx := context.Background()
	read := func(p string) protocol.FileRead {
		t.Helper()
		r, err := readFile(ctx, root, p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return r
	}
	for p, want := range map[string]string{"lf.txt": "lf", "crlf.txt": "crlf", "mixed.txt": "mixed", "none.txt": "none"} {
		if r := read(p); r.Kind != "text" || r.Newline != want || r.Encoding != "utf-8" || r.Sha256 == "" || r.Token == "" {
			t.Errorf("%s: %+v", p, r)
		}
	}
	if r := read("bom.txt"); !r.BOM || r.Text != "hi\n" {
		t.Errorf("bom: %+v", r)
	}
	for _, p := range []string{"bin.dat", "noise.dat"} {
		if r := read(p); r.Kind != "binary" || r.Text != "" {
			t.Errorf("%s: %+v", p, r)
		}
	}
	if r := read("latin.txt"); r.Kind != "text" || r.Encoding != "invalid" || !strings.Contains(r.Text, "caf�") {
		t.Errorf("latin: %+v", r)
	}
	r := read("big.txt")
	if r.Kind != "text" || !r.Truncated || r.Encoding != "utf-8" || len(r.Text) > protocol.FileTextLimit || r.Size != int64(len(big)) {
		t.Errorf("big: kind %s trunc %t enc %s len %d size %d", r.Kind, r.Truncated, r.Encoding, len(r.Text), r.Size)
	}
	if r := read("dir/esc.txt"); r.Text != "\x1b[31mred\n" {
		t.Errorf("content must be returned verbatim for the client to sanitize: %q", r.Text)
	}
}

func TestFilesReadTooLargeAndIrregular(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(protocol.FileHashLimit + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	writeFile(t, root, "secret/target.txt", "never")
	if err := os.Symlink("secret/target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secret", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := readFile(ctx, root, "huge.bin")
	if err != nil || r.Kind != "too_large" || r.Text != "" || r.Sha256 != "" || r.Size != protocol.FileHashLimit+1 {
		t.Fatalf("huge: %+v %v", r, err)
	}
	r, err = readFile(ctx, root, "link.txt")
	if err != nil || r.Kind != "not_regular" || r.LinkTarget != "secret/target.txt" || r.Text != "" {
		t.Fatalf("symlink: %+v %v", r, err)
	}
	if _, err := readFile(ctx, root, "linkdir/target.txt"); filesCode(err) != "not_found" {
		t.Fatalf("read through symlinked dir: %v", err)
	}
	done := make(chan protocol.FileRead, 1)
	go func() { r, _ := readFile(ctx, root, "pipe"); done <- r }()
	select {
	case r := <-done:
		if r.Kind != "not_regular" {
			t.Fatalf("fifo: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	for _, p := range []string{"", "../x", "/etc/passwd", ".git/config", "a/.Git/x", "a//b", "a\x00b"} {
		if _, err := readFile(ctx, root, p); filesCode(err) != "invalid" {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestFilesStat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "one")
	ctx := context.Background()
	s1, err := statFile(ctx, root, "a.txt")
	if err != nil || s1.Kind != "file" || s1.Token == "" {
		t.Fatalf("%+v %v", s1, err)
	}
	r, _ := readFile(ctx, root, "a.txt")
	if r.Token != s1.Token {
		t.Fatalf("read token %q differs from stat %q", r.Token, s1.Token)
	}
	writeFile(t, root, "a.txt", "two!")
	if s2, _ := statFile(ctx, root, "a.txt"); s2.Token == s1.Token {
		t.Fatal("token unchanged after a write")
	}
	os.Remove(filepath.Join(root, "a.txt"))
	for _, p := range []string{"a.txt", "gone/a.txt"} {
		if s, err := statFile(ctx, root, p); err != nil || s.Kind != "absent" {
			t.Fatalf("%s: %+v %v", p, s, err)
		}
	}
}

func TestFilesHandlers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "a\n")
	e := &engine{snap: protocol.Snapshot{
		Projects: []protocol.Project{{ID: "p", Path: root}, {ID: "fx", Path: "fixture://demo"}},
		Threads:  []protocol.Thread{{ID: "t", Checkout: root}},
	}}
	for _, tc := range []struct {
		handler func(http.ResponseWriter, *http.Request)
		query   string
		status  int
	}{
		{e.filesList, "project_id=p", 200}, {e.filesList, "thread_id=t&dir=", 200},
		{e.filesList, "", 400}, {e.filesList, "project_id=missing", 404},
		{e.filesList, "project_id=fx", 409}, {e.filesList, "project_id=p&dir=..", 400},
		{e.filesList, "project_id=p&dir=nope", 404}, {e.filesList, "project_id=p&dir=a.txt", 409},
		{e.filesRead, "project_id=p&path=a.txt", 200}, {e.filesRead, "project_id=p&path=b.txt", 404},
		{e.filesRead, "project_id=p&path=.git/HEAD", 400}, {e.filesRead, "project_id=fx&path=a.txt", 409},
		{e.filesStat, "project_id=p&path=b.txt", 200},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, httptest.NewRequest("GET", "/v1/files/x?"+tc.query, nil))
		if w.Code != tc.status {
			t.Errorf("%s: status %d: %s", tc.query, w.Code, w.Body.String())
		}
		if !json.Valid(w.Body.Bytes()) {
			t.Errorf("%s: invalid JSON", tc.query)
		}
		if tc.status == 409 && strings.Contains(tc.query, "fx") && !bytes.Contains(w.Body.Bytes(), []byte("unavailable")) {
			t.Errorf("fixture: %s", w.Body.String())
		}
	}
}

func TestFilesRoutesRequireAuthentication(t *testing.T) {
	c, stop := startTestServer(t, t.TempDir())
	defer stop()
	for _, route := range []string{"list", "read", "stat"} {
		resp, err := http.Get(c.Discovery.URL + "/v1/files/" + route + "?project_id=x&path=a")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", route, resp.StatusCode)
		}
	}
}

func TestFilesReadWaitsForASlot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "a")
	e := &engine{snap: protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: root}}}}
	for range cap(filesReadSlots) {
		filesReadSlots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	e.filesRead(w, httptest.NewRequest("GET", "/v1/files/read?project_id=p&path=a.txt", nil).WithContext(ctx))
	for range cap(filesReadSlots) {
		<-filesReadSlots
	}
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "busy") {
		t.Fatalf("busy: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	e.filesRead(w, httptest.NewRequest("GET", "/v1/files/read?project_id=p&path=a.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("after release: %d", w.Code)
	}
}
