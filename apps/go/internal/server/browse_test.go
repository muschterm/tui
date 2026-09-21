package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"golang.org/x/sys/unix"
)

func TestBrowseCompletionAndConfinement(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"src", ".hidden", ".git"} {
		if err := os.Mkdir(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"readme.md", "src/main.go", ".hidden/config"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(root, "source")); err != nil {
		t.Fatal(err)
	}
	request := protocol.BrowseRequest{Scope: "files"}
	got, err := browsePaths(context.Background(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, entry := range got.Entries {
		paths = append(paths, entry.Path)
	}
	if !reflect.DeepEqual(paths, []string{"source/", "src/", "readme.md"}) {
		t.Fatal(paths)
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{{"src/", []string{"src/main.go"}}, {"src/ma", []string{"src/main.go"}}, {".", []string{".hidden/"}}, {".hidden/", []string{".hidden/config"}}, {"nothing", []string{}}} {
		request.Query = tc.query
		result, err := browsePaths(context.Background(), root, request)
		if err != nil {
			t.Fatal(tc.query, err)
		}
		paths := []string{}
		for _, e := range result.Entries {
			paths = append(paths, e.Path)
		}
		if !reflect.DeepEqual(paths, tc.want) {
			t.Fatalf("%s: %v", tc.query, paths)
		}
	}
	for _, query := range []string{"../", "src/../../", outside + "/", "escape/", ".git/", "~/", "src/\x1b"} {
		request.Query = query
		if _, err := browsePaths(context.Background(), root, request); err == nil {
			t.Fatal("unsafe path accepted", query)
		}
	}
	request = protocol.BrowseRequest{Scope: "projects"}
	got, err = browsePaths(context.Background(), root, request)
	canonical, _ := filepath.EvalSymlinks(root)
	if err != nil || got.Directory != canonical || got.Root != canonical {
		t.Fatal(got, err)
	}
	for _, entry := range got.Entries {
		if !entry.IsDir || !filepath.IsAbs(entry.Path) || !strings.HasSuffix(entry.Path, "/") {
			t.Fatal(entry)
		}
	}
	request.Query = filepath.Join(root, "escape") + "/"
	if got, err := browsePaths(context.Background(), root, request); err != nil || len(got.Entries) != 0 {
		t.Fatal("project absolute symlink directory", got, err)
	}
	request.Query = filepath.Join(root, "sr")
	got, err = browsePaths(context.Background(), root, request)
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "src" {
		t.Fatal(got, err)
	}
	request.Query = got.Entries[0].Path
	got, err = browsePaths(context.Background(), root, request)
	if err != nil || got.Directory != filepath.Join(canonical, "src") || len(got.Entries) != 0 {
		t.Fatal(got, err)
	}
}

func TestBrowseLimitsAndCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < browseLimit+10; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	req := protocol.BrowseRequest{Scope: "files"}
	got, err := browsePaths(context.Background(), root, req)
	if err != nil || !got.Truncated || len(got.Entries) != browseLimit {
		t.Fatal(len(got.Entries), got.Truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := browsePaths(ctx, root, req); err == nil {
		t.Fatal("cancel ignored")
	}
	req.Query = strings.Repeat("a", 4097)
	if _, err := browsePaths(context.Background(), root, req); err == nil {
		t.Fatal("unbounded query")
	}
}

func TestProjectDirectorySettingAndBrowseTransport(t *testing.T) {
	c, stop := startTestServer(t, t.TempDir())
	defer stop()
	ctx := context.Background()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.AppSettings.ProjectDirectory != "~" {
		t.Fatal(snap.AppSettings)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := snap.AppSettings
	settings.ProjectDirectory = root
	command := protocol.Command{Version: 1, ID: "directory-setting", Kind: "settings.update", Revision: settings.Revision, AppSettings: &settings}
	if _, err := c.Command(ctx, command); err != nil {
		t.Fatal(err)
	}
	got, err := c.Browse(ctx, protocol.BrowseRequest{Scope: "projects"})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "project" {
		t.Fatal(got, err)
	}
	command.ID = "stale-directory"
	if _, err := c.Command(ctx, command); err == nil {
		t.Fatal("stale accepted")
	}
	snap, err = c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command.ID = "bad-directory"
	command.Revision = snap.AppSettings.Revision
	settings.ProjectDirectory = filepath.Join(root, "missing")
	if _, err := c.Command(ctx, command); err == nil {
		t.Fatal("nonexistent directory accepted")
	}
	settings.ProjectDirectory = ""
	settings.ContinueAfterRestart = true
	command.ID = "legacy-settings-directory"
	if _, err := c.Command(ctx, command); err != nil {
		t.Fatal(err)
	}
	snap, err = c.Snapshot(ctx)
	canonical, canonicalErr := filepath.EvalSymlinks(root)
	if err != nil || canonicalErr != nil || snap.AppSettings.ProjectDirectory != canonical || !snap.AppSettings.ContinueAfterRestart {
		t.Fatal("legacy settings update replaced directory", snap.AppSettings, err, canonicalErr)
	}
	settings.ProjectDirectory = "~"
	command.Revision = snap.AppSettings.Revision
	command.ID = "reset-directory"
	if _, err := c.Command(ctx, command); err != nil {
		t.Fatal(err)
	}
	snap, err = c.Snapshot(ctx)
	if err != nil || snap.AppSettings.ProjectDirectory != "~" {
		t.Fatal(snap.AppSettings, err)
	}
}

func TestWorkspaceFileCaptureRetryAndRecovery(t *testing.T) {
	e := testEngine(t)
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("at send"), 0600); err != nil {
		t.Fatal(err)
	}
	e.snap.Projects[0].Path = root
	command := initialSend(e.snap)
	command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "file.txt"}}
	receipt, err := e.command(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	if again, err := e.command(command); err != nil || again != receipt {
		t.Fatal(again, err)
	}
	if command.Attachments[0].Content != "" || command.Attachments[0].Kind != "workspace-file" {
		t.Fatal("mutated request")
	}
	saved, _, err := e.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	recoverThreads(&saved)
	var found bool
	for _, thread := range saved.Threads {
		if thread.ID == receipt.TargetID {
			found = true
			a := thread.Activity[0].Prompt.Attachments[0]
			if a.Kind != "file" || a.Content != "at send" || a.Source != "file.txt" {
				t.Fatal(a)
			}
		}
	}
	if !found {
		t.Fatal("capture missing")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(command); err != nil {
		t.Fatal("retry reread missing file", err)
	}
}

func TestWorkspaceFileCaptureRejectsAtomically(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"good": []byte("text"), "large": []byte(strings.Repeat("x", 65537)), "binary": {0, 1}, "utf8": {255}} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"missing", "../secret", "escape/secret", "large", "binary", "utf8", ".", "fifo", filepath.Join(outside, "secret")} {
		t.Run(source, func(t *testing.T) {
			e := testEngine(t)
			e.snap.Projects[0].Path = root
			command := initialSend(e.snap)
			command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "good"}, {Kind: "workspace-file", Source: source}}
			before := clone(e.snap)
			if _, err := e.command(command); err == nil {
				t.Fatal("invalid capture accepted")
			} else if !strings.Contains(err.Error(), fmt.Sprintf("%q", source)) {
				t.Fatal("capture error did not identify source", err)
			}
			if !reflect.DeepEqual(before, e.snap) {
				t.Fatal("failed capture changed state")
			}
			if r, err := e.store.Lookup(command); err != nil || r != nil {
				t.Fatal("failed capture persisted", r, err)
			}
		})
	}
}

func TestBrowseScanLimitAndSpecialDirectory(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < browseScanLimit+1; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := browsePaths(context.Background(), root, protocol.BrowseRequest{Scope: "files", Query: "no-match"})
	if err != nil || !got.Truncated || len(got.Entries) != 0 {
		t.Fatal(got, err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := browsePaths(context.Background(), root, protocol.BrowseRequest{Scope: "files", Query: "fifo/"}); err == nil {
		t.Fatal("FIFO directory accepted")
	}
}

func TestFileCaptureUsesThreadCheckoutAndPreservesQueue(t *testing.T) {
	e := testEngine(t)
	projectRoot, checkout := t.TempDir(), t.TempDir()
	for path, content := range map[string]string{filepath.Join(projectRoot, "file"): "wrong project root", filepath.Join(checkout, "file"): "thread checkout"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.snap.Projects[0].Path = projectRoot
	thread := &e.snap.Threads[0]
	thread.Checkout = checkout
	c := protocol.Command{Version: 1, ID: "capture-queued", Kind: "prompt.send", ThreadID: thread.ID, Text: "context", Attachments: []protocol.Attachment{{Kind: "workspace-file", Source: "file"}}}
	if _, err := e.command(c); err != nil {
		t.Fatal(err)
	}
	thread = &e.snap.Threads[0]
	if got := thread.Queue[len(thread.Queue)-1].Attachments[0].Content; got != "thread checkout" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ID = "capture-cancelled"
	before := clone(e.snap)
	if _, err := e.commandContext(ctx, c); err == nil {
		t.Fatal("cancel ignored")
	}
	if !reflect.DeepEqual(before, e.snap) {
		t.Fatal("cancel changed state")
	}
	thread.Closed = true
	thread.LifecycleRevision++
	c.ID = "capture-reopen"
	c.Kind = "prompt.reopen-send"
	c.Revision = thread.LifecycleRevision
	c.Settings = &thread.Selected
	if err := os.Remove(filepath.Join(checkout, "file")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(c); err == nil {
		t.Fatal("reopen accepted missing file")
	}
	if !e.snap.Threads[0].Closed {
		t.Fatal("failed capture reopened thread")
	}
}

func TestProjectBrowseRecoversMissingStartingFolder(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "removed")
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{root + "/", "~/"} {
		if _, err := browsePaths(context.Background(), missing, protocol.BrowseRequest{Scope: "projects", Query: query}); err != nil {
			t.Fatal(query, err)
		}
	}
	if _, err := browsePaths(context.Background(), missing, protocol.BrowseRequest{Scope: "projects"}); err == nil {
		t.Fatal("missing default directory silently changed")
	}
}

func TestConcurrentFileCaptureRetryCommitsOnce(t *testing.T) {
	e := testEngine(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("captured"), 0600); err != nil {
		t.Fatal(err)
	}
	e.snap.Projects[0].Path = root
	c := initialSend(e.snap)
	c.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "file"}}
	before := len(e.snap.Threads)
	var wg sync.WaitGroup
	receipts := make(chan protocol.Receipt, 12)
	errors := make(chan error, 12)
	for range 12 {
		wg.Go(func() { r, err := e.command(c); receipts <- r; errors <- err })
	}
	wg.Wait()
	close(receipts)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *protocol.Receipt
	for r := range receipts {
		if first == nil {
			first = &r
		} else if *first != r {
			t.Fatal("retry diverged", *first, r)
		}
	}
	if len(e.snap.Threads) != before+1 {
		t.Fatal("duplicate creation")
	}
}
