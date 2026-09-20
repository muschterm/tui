package server

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func projectIdentity(scope, value string) string {
	return fmt.Sprintf("project-%x", sha256.Sum256([]byte(scope+"\x00"+value)))
}

// ensureProjects migrates only existing threads. An intentionally empty snapshot
// stays empty; fixture checkouts retain their original values and are never
// interpreted as local filesystem paths.
func ensureProjects(s *protocol.Snapshot) {
	byName := map[string]int{}
	byID := map[string]int{}
	for i, p := range s.Projects {
		byID[p.ID] = i
		if _, exists := byName[p.Name]; !exists {
			byName[p.Name] = i
		}
	}
	for i := range s.Threads {
		t := &s.Threads[i]
		if _, exists := byID[t.ProjectID]; exists {
			continue
		}
		name := t.Project
		if name == "" {
			name = "Project"
		}
		index, exists := byName[name]
		if !exists {
			p := protocol.Project{ID: projectIdentity("legacy", name), Name: name, Path: t.Checkout}
			index = len(s.Projects)
			s.Projects = append(s.Projects, p)
			byName[name], byID[p.ID] = index, index
		}
		t.ProjectID = s.Projects[index].ID
	}
}

func canonicalProjectPath(input string) (string, error) {
	if input == "" || len(input) > 4096 || strings.ContainsRune(input, 0) {
		return "", failure("invalid", "project path must contain 1–4096 bytes")
	}
	path := input
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", failure("invalid", "cannot resolve server home directory")
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	} else if strings.HasPrefix(path, "~") {
		return "", failure("invalid", "use ~/ for the server home directory or an existing directory path")
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", failure("invalid", "cannot resolve project path: "+err.Error())
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", failure("invalid", "cannot access project directory: "+err.Error())
	}
	if len(path) > 4096 {
		return "", failure("invalid", "resolved project path exceeds 4096 bytes")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", failure("invalid", "cannot inspect project directory: "+err.Error())
	}
	if !info.IsDir() {
		return "", failure("invalid", "project path must be an existing directory")
	}
	dir, err := os.Open(path)
	if err != nil {
		return "", failure("invalid", "cannot read project directory: "+err.Error())
	}
	_, readErr := dir.Readdirnames(1)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF {
		return "", failure("invalid", "cannot read project directory: "+readErr.Error())
	}
	if closeErr != nil {
		return "", failure("invalid", "cannot close project directory: "+closeErr.Error())
	}
	return path, nil
}

func applyProject(s *protocol.Snapshot, c protocol.Command) (string, error) {
	switch c.Kind {
	case "project.add":
		path, err := canonicalProjectPath(c.Path)
		if err != nil {
			return "", err
		}
		for _, p := range s.Projects {
			if p.Path == path {
				return p.ID, nil
			}
		}
		if len(s.Projects) >= 128 {
			return "", failure("capacity", "at most 128 projects are supported")
		}
		p := protocol.Project{ID: projectIdentity("path", path), Name: filepath.Base(path), Path: path}
		s.Projects = append(s.Projects, p)
		return p.ID, nil
	case "thread.create":
		if len(s.Threads) >= 128 {
			return "", failure("capacity", "at most 128 threads are supported")
		}
		var project *protocol.Project
		for i := range s.Projects {
			if s.Projects[i].ID == c.ProjectID {
				project = &s.Projects[i]
				break
			}
		}
		if project == nil {
			return "", failure("not_found", "project does not exist")
		}
		title := strings.TrimSpace(c.Text)
		if title == "" {
			title = "New thread"
		}
		if len(title) > 256 {
			return "", failure("invalid", "thread title exceeds 256 bytes")
		}
		settings := protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
		if c.Settings != nil {
			if !validSettings(*c.Settings) {
				return "", failure("unsupported_settings", "only Demo fixture settings are available")
			}
			settings = *c.Settings
		}
		if c.ID == "" {
			return "", failure("invalid", "thread creation requires a command identity")
		}
		id := "thread-" + c.ID
		for _, t := range s.Threads {
			if t.ID == id {
				return "", failure("conflict", "thread identity already exists")
			}
		}
		s.Threads = append(s.Threads, protocol.Thread{ID: id, ProjectID: project.ID, Project: project.Name, Title: title, Checkout: project.Path, Agent: "Fixture agent", State: "idle", Selected: settings, Effective: settings, QueueRevision: 1})
		return id, nil
	default:
		return "", failure("invalid", "unsupported project command")
	}
}
