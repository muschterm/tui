package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (e *engine) workspace(w http.ResponseWriter, r *http.Request) {
	projectID, threadID := r.URL.Query().Get("project_id"), r.URL.Query().Get("thread_id")
	w.Header().Set("Content-Type", "application/json")
	if (projectID == "") == (threadID == "") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(failure("invalid", "select exactly one project or thread"))
		return
	}
	path, found := "", false
	var unavailable error
	e.mu.Lock()
	if threadID != "" {
		for _, t := range e.snap.Threads {
			if t.ID == threadID {
				path, found = t.Checkout, true
				_, unavailable = threadCheckout(&e.snap, &t)
				break
			}
		}
	} else {
		for _, p := range e.snap.Projects {
			if p.ID == projectID {
				path, found = p.Path, true
				break
			}
		}
	}
	e.mu.Unlock()
	if !found {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(failure("not_found", "workspace target not found"))
		return
	}
	if unavailable != nil {
		// A managed worktree that is not present is reported without
		// running Git there (ADR 0024).
		var pe *protocol.Error
		errors.As(unavailable, &pe)
		_ = json.NewEncoder(w).Encode(protocol.WorkspaceInfo{Path: path, Kind: "unavailable", State: "unavailable", Error: pe.Message})
		return
	}
	_ = json.NewEncoder(w).Encode(inspectWorkspace(r.Context(), path))
}

// Git runs outside the engine lock, without inherited repository/config overrides.
// Only plumbing reads are used; no hooks, filters, pager, shell or network runs.
func inspectWorkspace(parent context.Context, path string) protocol.WorkspaceInfo {
	info := protocol.WorkspaceInfo{Path: path, Kind: "unavailable", State: "unavailable"}
	if strings.HasPrefix(path, "fixture://") {
		info.Kind = "fixture"
		return info
	}
	if path == "" {
		info.Error = "Checkout path is unavailable"
		return info
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		info.Error = "Checkout directory is unavailable"
		return info
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", path}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		cmd.Env = append(cmd.Env, gitCeilingEnv(path)...)
		out := &workspaceOutput{}
		cmd.Stdout = out
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		return strings.TrimSpace(out.text.String()), err
	}
	inside, err := run("rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		if ctx.Err() != nil {
			info.Error = "Workspace inspection cancelled or timed out"
			return info
		}
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			info.Error = "Git inspection is unavailable"
			return info
		}
		info.Kind, info.State = "checkout", "non-git"
		return info
	}
	info.Kind = "checkout"
	gitDir, err1 := run("rev-parse", "--absolute-git-dir")
	commonDir, err2 := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err1 == nil && err2 == nil && filepath.Clean(gitDir) != filepath.Clean(commonDir) {
		info.Kind = "worktree"
	}
	branch, branchErr := run("symbolic-ref", "--quiet", "--short", "HEAD")
	revision, revisionErr := run("rev-parse", "--verify", "--short", "HEAD")
	if ctx.Err() != nil {
		info.State, info.Error = "unavailable", "Workspace inspection cancelled or timed out"
		return info
	}
	if branchErr == nil {
		info.Branch, info.State = branch, "branch"
		if revisionErr != nil {
			info.State = "unborn"
		}
	} else if revisionErr == nil {
		info.State, info.Revision = "detached", revision
	} else {
		info.Error = "Git HEAD is unavailable"
	}
	return info
}

// Bound output even for malformed repository metadata.
type workspaceOutput struct{ text strings.Builder }

// Write keeps the first 8 KiB and reports the rest as consumed.
func (b *workspaceOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 8192 - b.text.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.text.Write(p)
	}
	return n, nil
}
