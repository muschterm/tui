package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrRuntimeMissing distinguishes the official CLI from the ACP adapter.
var ErrRuntimeMissing = errors.New("local agent CLI unavailable")

// These adapter overrides are always populated for our Claude/Codex launches,
// preventing the adapters' default fallback to bundled provider runtimes.
const (
	EnvClaudeRuntime = "CLAUDE_CODE_EXECUTABLE"
	EnvCodexRuntime  = "CODEX_PATH"
)

// runtimeEnvironment selects an installed CLI before starting its adapter.
// Native ACP agents have no provider runtime to discover. Explicit overrides
// select another local executable; an invalid override never falls back.
func runtimeEnvironment(agentID string) (environment []string, runtimePath string, err error) {
	environment = os.Environ()
	var name, key string
	switch agentID {
	case "claude":
		name, key = "claude", EnvClaudeRuntime
	case "codex":
		name, key = "codex", EnvCodexRuntime
	default:
		return environment, "", nil
	}
	command := strings.TrimSpace(os.Getenv(key))
	if command == "" {
		command = name
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return nil, "", fmt.Errorf("%w: cannot execute %q; make %s available on the server's PATH or set %s to its installed executable. Bundled runtimes are not used", ErrRuntimeMissing, command, name, key)
	}
	// The adapter starts in the checkout, which may differ from the server's
	// working directory. Resolve relative explicit paths before crossing it.
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("%w: resolve %s executable: %v", ErrRuntimeMissing, name, err)
	}
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, key+"=") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, key+"="+path), path, nil
}
