package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func executable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRuntimeSelectionReachesAdapter(t *testing.T) {
	for _, provider := range []struct{ id, key string }{{"claude", EnvClaudeRuntime}, {"codex", EnvCodexRuntime}} {
		for _, override := range []bool{false, true} {
			t.Run(provider.id+map[bool]string{false: "/PATH", true: "/override"}[override], func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, provider.id)
				executable(t, path)
				t.Setenv("PATH", dir)
				t.Setenv(provider.key, "")
				if override {
					path = filepath.Join(dir, "custom local CLI")
					executable(t, path)
					t.Setenv(provider.key, path)
				}
				marker := filepath.Join(dir, "observed")
				session, err := Start(context.Background(), Options{AgentID: provider.id, Command: "/bin/sh", Cwd: t.TempDir(),
					Args: []string{"-c", "printf '%s' \"$" + provider.key + "\" > \"$1\"", "adapter", marker}})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close(context.Background())
				select {
				case <-session.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("adapter did not finish")
				}
				got, err := os.ReadFile(marker)
				if err != nil || string(got) != path || session.RuntimePath() != path {
					t.Fatalf("adapter runtime = %q, wanted %q: %v", got, path, err)
				}
				wantParent := ""
				if override {
					wantParent = path
				}
				if os.Getenv(provider.key) != wantParent {
					t.Fatal("launch changed parent environment")
				}
			})
		}
	}
}

func TestMissingRuntimeNeverStartsAdapterOrFallsBack(t *testing.T) {
	for _, provider := range []struct{ id, key string }{{"claude", EnvClaudeRuntime}, {"codex", EnvCodexRuntime}} {
		for _, scenario := range []string{"missing", "bad override", "not executable"} {
			t.Run(provider.id+"/"+scenario, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("PATH", dir)
				t.Setenv(provider.key, "")
				if scenario == "bad override" {
					executable(t, filepath.Join(dir, provider.id))
					t.Setenv(provider.key, filepath.Join(dir, "missing-override"))
				} else if scenario == "not executable" {
					if err := os.WriteFile(filepath.Join(dir, provider.id), []byte("not executable"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				marker := filepath.Join(dir, "adapter-started")
				session, err := Start(context.Background(), Options{AgentID: provider.id, Command: "/bin/sh",
					Args: []string{"-c", ": > \"$1\"", "adapter", marker}})
				if session != nil || !errors.Is(err, ErrRuntimeMissing) || !strings.Contains(err.Error(), provider.key) {
					t.Fatalf("missing runtime did not fail clearly: %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("adapter started despite missing runtime")
				}
			})
		}
	}
}

func TestRelativeRuntimeOverrideIsResolvedBeforeCheckoutChange(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	executable(t, filepath.Join(dir, "custom-cli"))
	t.Setenv(EnvClaudeRuntime, "./custom-cli")
	environment, path, err := runtimeEnvironment("claude")
	if err != nil || path != filepath.Join(dir, "custom-cli") {
		t.Fatalf("relative resolution: %q %v", path, err)
	}
	count := 0
	for _, item := range environment {
		if strings.HasPrefix(item, EnvClaudeRuntime+"=") {
			count++
			if item != EnvClaudeRuntime+"="+path {
				t.Fatal("relative value survived in child environment")
			}
		}
	}
	if count != 1 {
		t.Fatal("override not replaced exactly once")
	}
}

func TestNativeACPDoesNotRequireProviderCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(EnvClaudeRuntime, "/nonexistent/claude")
	t.Setenv(EnvCodexRuntime, "/nonexistent/codex")
	_, path, err := runtimeEnvironment("native-acp")
	if path != "" || err != nil {
		t.Fatalf("native ACP required a provider runtime: %q %v", path, err)
	}
}
