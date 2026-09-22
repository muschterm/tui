package server

import (
	"context"
	"encoding/json"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceInspection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "--initial-branch=workspace-test")
	info := inspectWorkspace(context.Background(), root)
	if info.State != "unborn" || info.Branch != "workspace-test" || info.Kind != "checkout" {
		t.Fatalf("unborn: %+v", info)
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath="+t.TempDir(), "commit", "--allow-empty", "-m", "Initial")
	info = inspectWorkspace(context.Background(), root)
	if info.State != "branch" || info.Branch != "workspace-test" {
		t.Fatalf("branch: %+v", info)
	}
	git("checkout", "--detach")
	info = inspectWorkspace(context.Background(), root)
	if info.State != "detached" || info.Revision == "" || info.Branch != "" {
		t.Fatalf("detached: %+v", info)
	}
	worktree := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "--detach", worktree)
	info = inspectWorkspace(context.Background(), worktree)
	if info.Kind != "worktree" || info.State != "detached" {
		t.Fatalf("worktree: %+v", info)
	}
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	info = inspectWorkspace(context.Background(), t.TempDir())
	if info.State != "non-git" {
		t.Fatalf("non-git: %+v", info)
	}
	info = inspectWorkspace(context.Background(), filepath.Join(root, "missing"))
	if info.State != "unavailable" || info.Error == "" {
		t.Fatalf("missing: %+v", info)
	}
	info = inspectWorkspace(context.Background(), "fixture://workspace")
	if info.Kind != "fixture" || info.Error != "" {
		t.Fatalf("fixture: %+v", info)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := inspectWorkspace(ctx, root); got.State != "unavailable" {
		t.Fatalf("cancelled: %+v", got)
	}
}

func TestWorkspaceHandlerTargets(t *testing.T) {
	e := &engine{snap: protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: "fixture://project"}}, Threads: []protocol.Thread{{ID: "t", Checkout: "fixture://thread"}}}}
	for _, tc := range []struct {
		query, path string
		status      int
	}{
		{"project_id=p", "fixture://project", 200}, {"thread_id=t", "fixture://thread", 200},
		{"project_id=missing", "", 404}, {"thread_id=missing", "", 404}, {"", "", 400}, {"project_id=p&thread_id=t", "", 400},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			e.workspace(w, httptest.NewRequest("GET", "/v1/workspace?"+tc.query, nil))
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				var got protocol.WorkspaceInfo
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Path != tc.path {
					t.Fatalf("path: %+v", got)
				}
			}
		})
	}
}
