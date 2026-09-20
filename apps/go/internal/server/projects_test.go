package server

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestProjectAddCanonicalDirectoryDedupAndNoMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "My project")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "work.txt")
	if err := os.WriteFile(marker, []byte("unsaved work stays safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	s := protocol.Snapshot{}
	id, err := applyProject(&s, protocol.Command{Kind: "project.add", ID: "add-1", Path: path + "/../My project/."})
	if err != nil {
		t.Fatal(err)
	}
	second, err := applyProject(&s, protocol.Command{Kind: "project.add", ID: "add-2", Path: link})
	if err != nil || second != id || len(s.Projects) != 1 {
		t.Fatal("canonical alias duplicated project", second, err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Projects[0].Path != canonical || s.Projects[0].Name != "My project" || !strings.HasPrefix(id, "project-") {
		t.Fatalf("wrong project: %+v", s.Projects[0])
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "unsaved work stays safe" || len(entries) != 1 || len(s.Threads) != 0 {
		t.Fatal("project add changed filesystem or created work")
	}
}

func TestProjectAddRejectsInvalidAndExpandsServerHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "project")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(home, "missing"), file, strings.Repeat("a", 4097), "bad\x00path", "~another/project"} {
		s := protocol.Snapshot{}
		if _, err := applyProject(&s, protocol.Command{Kind: "project.add", Path: path}); err == nil || len(s.Projects) != 0 {
			t.Fatalf("invalid path accepted: %q", path)
		}
	}
	s := protocol.Snapshot{}
	if _, err := applyProject(&s, protocol.Command{Kind: "project.add", Path: "~/project"}); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Projects[0].Path != canonical {
		t.Fatal("home was not expanded on server")
	}
}

func TestEnsureProjectsMigratesLegacyLabelsAndKeepsCheckouts(t *testing.T) {
	s := protocol.Snapshot{Threads: []protocol.Thread{
		{ID: "one", Project: "Workspace", Checkout: "fixture://workspace"},
		{ID: "two", Project: "Workspace", Checkout: "fixture://review"},
		{ID: "three", Project: "Other", Checkout: "fixture://other"},
	}}
	ensureProjects(&s)
	if len(s.Projects) != 2 || s.Threads[0].ProjectID != s.Threads[1].ProjectID || s.Threads[0].ProjectID == s.Threads[2].ProjectID {
		t.Fatal("legacy grouping failed", s)
	}
	if s.Threads[0].Checkout != "fixture://workspace" || s.Threads[1].Checkout != "fixture://review" {
		t.Fatal("migration rewrote checkout")
	}
	before := append([]protocol.Project(nil), s.Projects...)
	ensureProjects(&s)
	if !reflect.DeepEqual(s.Projects, before) {
		t.Fatal("migration not idempotent")
	}
	empty := protocol.Snapshot{}
	ensureProjects(&empty)
	if len(empty.Threads) != 0 || len(empty.Projects) != 0 {
		t.Fatal("empty snapshot resurrected fixture projects")
	}
}

func TestProjectCreatesEmptyIdleThreadWithCapturedSettings(t *testing.T) {
	s := protocol.Snapshot{}
	id, err := applyProject(&s, protocol.Command{Kind: "project.add", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	settings := protocol.Settings{Model: "fixture-model", Effort: "high", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	threadID, err := applyProject(&s, protocol.Command{Kind: "thread.create", ID: "create-1", ProjectID: id, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Threads) != 1 {
		t.Fatal("thread not created")
	}
	th := s.Threads[0]
	if threadID != "thread-create-1" || th.ProjectID != id || th.Project != s.Projects[0].Name || th.Checkout != s.Projects[0].Path || th.Title != "New thread" || th.Agent != "Fixture agent" {
		t.Fatalf("wrong thread identity: %+v", th)
	}
	if th.State != "idle" || th.NeedsResume || len(th.Activity)+len(th.Plan)+len(th.Children)+len(th.Queue)+len(th.Requests) != 0 || th.Tick != 0 {
		t.Fatal("thread creation launched synthetic work", th)
	}
	settings.Effort = "low"
	if th.Selected.Effort != "high" || th.Effective.Effort != "high" {
		t.Fatal("creation settings not captured by value")
	}
	before := len(s.Threads)
	bad := protocol.Settings{Model: "real-provider"}
	if _, err := applyProject(&s, protocol.Command{Kind: "thread.create", ID: "bad", ProjectID: id, Settings: &bad}); err == nil || len(s.Threads) != before {
		t.Fatal("unsupported agent settings accepted")
	}
	if _, err := applyProject(&s, protocol.Command{Kind: "thread.create", ID: "missing", ProjectID: "missing"}); err == nil {
		t.Fatal("unknown project accepted")
	}
	if _, err := applyProject(&s, protocol.Command{Kind: "thread.create", ID: "title", ProjectID: id, Text: strings.Repeat("x", 257)}); err == nil {
		t.Fatal("unbounded title accepted")
	}
}

func TestProjectAndThreadCapacity(t *testing.T) {
	path, err := canonicalProjectPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := protocol.Snapshot{Projects: make([]protocol.Project, 128), Threads: make([]protocol.Thread, 128)}
	s.Projects[0] = protocol.Project{ID: "existing", Name: "Existing", Path: path}
	if id, err := applyProject(&s, protocol.Command{Kind: "project.add", Path: path}); err != nil || id != "existing" {
		t.Fatal("dedup failed at capacity")
	}
	if _, err := applyProject(&s, protocol.Command{Kind: "project.add", Path: t.TempDir()}); err == nil {
		t.Fatal("project capacity not enforced")
	}
	if _, err := applyProject(&s, protocol.Command{Kind: "thread.create", ID: "full", ProjectID: "existing"}); err == nil {
		t.Fatal("thread capacity not enforced")
	}
}
