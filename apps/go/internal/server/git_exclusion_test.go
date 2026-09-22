package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestGitDirectoryExcludedThroughAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git/config", ".git/hooks/pre-commit", "src/main.go", "notes"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("secret or text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"meta": ".git", "src/hooks": "../.git/hooks", "alias": "src"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	sources := []string{"meta/config", "meta/hooks/pre-commit", "src/hooks/pre-commit", ".GIT/config", "src/../.Git/config"}
	_, statErr := os.Stat(filepath.Join(root, ".GIT", "config"))
	for _, source := range sources {
		t.Run("capture "+source, func(t *testing.T) {
			e := testEngine(t)
			e.snap.Projects[0].Path = root
			command := initialSend(e.snap)
			command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: source}}
			before := clone(e.snap)
			if _, err := e.command(command); !isCode(err, "attachment") {
				t.Fatal("git metadata capture was not rejected:", err)
			}
			if !reflect.DeepEqual(before, e.snap) {
				t.Fatal("rejected capture changed state")
			}
		})
	}
	t.Run("case-insensitive volume", func(t *testing.T) {
		if statErr != nil {
			t.Skip("temp filesystem is case-sensitive")
		}
		opened, err := os.OpenRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		defer opened.Close()
		if !insideGit(opened, ".GIT/config") || !insideGit(opened, "SRC/../.giT") || insideGit(opened, "SRC/main.go") {
			t.Fatal("opened path was not compared with the checkout's .git")
		}
	})
	for _, query := range []string{"meta/", "meta/hooks/", "src/hooks/", ".GIT/", ".git/"} {
		if _, err := browsePaths(context.Background(), root, protocol.BrowseRequest{Scope: "files", Query: query}); !isCode(err, "invalid") {
			t.Fatalf("browse entered %q: %v", query, err)
		}
	}
	for query, want := range map[string][]string{"": {"alias/", "src/", "notes"}, ".": {}, "m": {}, "src/": {"src/main.go"}, "alias/": {"alias/main.go"}} {
		result, err := browsePaths(context.Background(), root, protocol.BrowseRequest{Scope: "files", Query: query})
		if err != nil {
			t.Fatal(query, err)
		}
		got := []string{}
		for _, entry := range result.Entries {
			got = append(got, entry.Path)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("browse %q listed %v, want %v", query, got, want)
		}
	}
	e := testEngine(t)
	e.snap.Projects[0].Path = root
	command := initialSend(e.snap)
	command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "alias/main.go"}, {Kind: "workspace-file", Source: "notes"}}
	if _, err := e.command(command); err != nil {
		t.Fatal("ordinary capture rejected:", err)
	}
}

func TestGitFileAndAbsentGitDoNotBlockCapture(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		root := t.TempDir()
		if worktree {
			if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(".git", filepath.Join(root, "pointer")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "notes"), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
		e := testEngine(t)
		e.snap.Projects[0].Path = root
		command := initialSend(e.snap)
		command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "notes"}}
		if _, err := e.command(command); err != nil {
			t.Fatal("ordinary capture rejected:", err)
		}
		if !worktree {
			continue
		}
		command = initialSend(e.snap)
		command.ID = "pointer"
		command.Attachments = []protocol.Attachment{{Kind: "workspace-file", Source: "pointer"}}
		if _, err := e.command(command); !isCode(err, "attachment") {
			t.Fatal("gitfile alias captured:", err)
		}
		result, err := browsePaths(context.Background(), root, protocol.BrowseRequest{Scope: "files"})
		if err != nil || len(result.Entries) != 1 || result.Entries[0].Path != "notes" {
			t.Fatal("gitfile alias listed:", result.Entries, err)
		}
	}
}
