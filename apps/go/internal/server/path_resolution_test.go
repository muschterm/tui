package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestProjectAddDedupAndReceiptReplayThroughEngine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "project")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	e := testEngine(t)
	var calls atomic.Int32
	e.resolve = func(input string) (string, error) { calls.Add(1); return canonicalProjectPath(input) }
	add := protocol.Command{Version: 1, ID: "add", Kind: "project.add", Path: path}
	first, err := e.command(add)
	if err != nil {
		t.Fatal(err)
	}
	// A retry returns the accepted receipt even after the folder disappears.
	if err = os.Remove(filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	projects, revision := len(e.snap.Projects), e.snap.Revision
	replay, err := e.command(add)
	if err != nil || replay != first || calls.Load() != 1 || e.snap.Revision != revision {
		t.Fatal("retry re-resolved or changed the outcome", replay, calls.Load(), err)
	}
	if err = os.Rename(path+"-moved", path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(path, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	targets := make([]string, 8)
	for i := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			input := path
			if i%2 == 1 {
				input = filepath.Join(root, "alias")
			}
			r, err := e.command(protocol.Command{Version: 1, ID: "again-" + string(rune('a'+i)), Kind: "project.add", Path: input})
			if err != nil {
				t.Error(err)
			}
			targets[i] = r.TargetID
		}()
	}
	wg.Wait()
	for _, target := range targets {
		if target != first.TargetID {
			t.Fatal("concurrent add duplicated the project:", targets)
		}
	}
	if len(e.snap.Projects) != projects {
		t.Fatal("canonical path registered twice")
	}
	if _, err = e.command(protocol.Command{Version: 1, ID: "missing", Kind: "project.add", Path: filepath.Join(root, "missing")}); !isCode(err, "invalid") {
		t.Fatal("missing folder accepted:", err)
	}
}

func TestBlockedPathResolutionDoesNotHoldEngineLock(t *testing.T) {
	e := testEngine(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	e.resolve = func(string) (string, error) {
		entered <- struct{}{}
		<-release
		return "", failure("invalid", "released")
	}
	defer close(release)
	settings := e.snap.AppSettings
	settings.ProjectDirectory = "/hung/mount"
	blocked := []protocol.Command{
		{Version: 1, ID: "hung-add", Kind: "project.add", Path: "/hung/mount"},
		{Version: 1, ID: "hung-settings", Kind: "settings.update", Revision: settings.Revision, AppSettings: &settings},
	}
	errs := make(chan error, len(blocked))
	for _, c := range blocked {
		go func() {
			_, err := e.commandContext(context.Background(), c)
			errs <- err
		}()
	}
	for range blocked {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("resolver was not reached")
		}
	}
	free := make(chan error, 1)
	go func() {
		e.mu.Lock()
		_ = clone(e.snap)
		e.mu.Unlock()
		if err := e.tick(); err != nil {
			free <- err
			return
		}
		_, err := e.command(protocol.Command{Version: 1, ID: "unrelated", Kind: "thread.interrupt", ThreadID: "thread-shell"})
		free <- err
	}()
	select {
	case err := <-free:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked folder check stalled snapshots, ticks or unrelated commands")
	}
	projects, directory := len(e.snap.Projects), e.snap.AppSettings.ProjectDirectory
	for range blocked {
		select {
		case err := <-errs:
			if !isCode(err, "unavailable") {
				t.Fatal("hung folder check reported as:", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("hung folder check never timed out")
		}
	}
	if len(e.snap.Projects) != projects || e.snap.AppSettings.ProjectDirectory != directory {
		t.Fatal("timed-out folder check changed state")
	}
	for _, c := range blocked {
		if r, err := e.store.Lookup(c); err != nil || r != nil {
			t.Fatal("timed-out command persisted", r, err)
		}
	}
}
