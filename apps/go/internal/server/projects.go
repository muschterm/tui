package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// acpAgentID records the agent identity only for real connections. Fixture
// threads keep an empty AgentID, exactly as every existing snapshot has it.
func acpAgentID(a protocol.Agent) string {
	if a.Kind == agent.KindACP {
		return a.ID
	}
	return ""
}

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

// resolvedPath carries a canonical path computed before the engine lock. A nil
// value resolves inline for callers that hold no lock.
type resolvedPath struct {
	input, path string
	err         error
	done        bool
}

func (r *resolvedPath) result(input string) (string, error) {
	if r == nil {
		return canonicalProjectPath(input)
	}
	if !r.done || r.input != input {
		return "", failure("stale_settings", "settings changed while resolving the folder; refresh before saving")
	}
	return r.path, r.err
}

func pathToResolve(s protocol.Snapshot, c protocol.Command) (string, bool) {
	switch {
	case c.Kind == "project.add":
		return c.Path, true
	case c.Kind == "settings.update" && c.AppSettings != nil:
		directory := nextProjectDirectory(s, *c.AppSettings)
		return directory, directory != s.AppSettings.ProjectDirectory && c.Revision == s.AppSettings.Revision
	}
	return "", false
}

const pathResolveTimeout = time.Second

// A blocked filesystem call cannot be cancelled; bound how many may linger.
var pathResolvers = make(chan struct{}, 8)

func (e *engine) resolvePath(parent context.Context, input string) *resolvedPath {
	resolve := e.resolve
	if resolve == nil {
		resolve = canonicalProjectPath
	}
	select {
	case pathResolvers <- struct{}{}:
	default:
		return &resolvedPath{input: input, done: true, err: failure("unavailable", "earlier folder checks have not responded; try again later")}
	}
	result := make(chan *resolvedPath, 1)
	go func() {
		defer func() { <-pathResolvers }()
		path, err := resolve(input)
		result <- &resolvedPath{input: input, path: path, err: err, done: true}
	}()
	ctx, cancel := context.WithTimeout(parent, pathResolveTimeout)
	defer cancel()
	select {
	case r := <-result:
		return r
	case <-ctx.Done():
		return &resolvedPath{input: input, done: true, err: failure("unavailable", "folder did not respond in time; nothing was changed")}
	}
}

func applyProject(s *protocol.Snapshot, c protocol.Command) (string, error) {
	return applyProjectResolved(s, c, nil)
}

func applyProjectResolved(s *protocol.Snapshot, c protocol.Command, resolved *resolvedPath) (string, error) {
	switch c.Kind {
	case "project.add":
		path, err := resolved.result(c.Path)
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
		// Registration identity must not survive removal: stale confirmations
		// must never apply to a later registration of the same directory.
		p := protocol.Project{ID: "project-" + ID(), Name: filepath.Base(path), Path: path, Revision: 1}
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
		if protocol.EffectiveWorkspaceDefault(s.AppSettings, *project) == "worktree" {
			return "", failure("unsupported_workspace", "worktree creation is unavailable in this server; select current checkout in settings")
		}
		title := strings.TrimSpace(c.Text)
		if title == "" {
			title = "New thread"
		}
		if len(title) > 256 {
			return "", failure("invalid", "thread title exceeds 256 bytes")
		}
		// thread.create keeps its historical default so existing clients that
		// create a thread without naming an agent still get the fixture.
		requested := c.Agent
		if requested == "" {
			requested = agent.FixtureID
		}
		chosen, err := resolveAgent(s, requested)
		if err != nil {
			return "", err
		}
		settings := protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
		if chosen.Kind == agent.KindACP {
			settings = agent.DefaultSettings(*chosen)
		}
		if c.Settings != nil {
			if err := validateSettings(s, acpAgentID(*chosen), *c.Settings); err != nil {
				return "", err
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
		project.Revision++
		s.Threads = append(s.Threads, protocol.Thread{ID: id, ProjectID: project.ID, Project: project.Name, Title: title, Checkout: project.Path, Agent: chosen.Name, AgentID: acpAgentID(*chosen), State: "idle", Selected: settings, Effective: settings, QueueRevision: 1})
		return id, nil
	default:
		return "", failure("invalid", "unsupported project command")
	}
}
