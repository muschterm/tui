// Package agent connects the server to user-owned ACP agent executables. It
// owns process launch, the ACP client side of the connection, and the pure
// translation between ACP payloads and the application protocol. It performs no
// snapshot persistence and knows nothing about the HTTP surface.
package agent

import (
	"errors"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// ErrExecutableMissing reports that a configured adapter is not installed. It
// is distinguished from other launch failures so the record can carry the
// documented install hint rather than a bare exec error.
var ErrExecutableMissing = errors.New("agent executable was not found on the server's PATH")

// InstallHint names the pinned, user-owned adapters. The application never
// installs or downloads them.
const InstallHint = "Install the adapter yourself, for example:\n  npm install -g @agentclientprotocol/claude-agent-acp@0.80.0 @agentclientprotocol/codex-acp@1.12.0\nThen set " + EnvClaudeCommand + " or " + EnvCodexCommand + " if it is not on the server's PATH."

// Kind values for protocol.Agent.Kind.
const (
	KindFixture = "fixture"
	KindACP     = "acp"
)

// FixtureID is the always-present synthetic agent. Older snapshots recorded it
// only by its display name.
const (
	FixtureID   = "fixture"
	FixtureName = "Fixture agent"
)

// Agent states recorded on protocol.Agent.State.
const (
	StateUnprobed        = "unprobed"
	StateProbing         = "probing"
	StateReady           = "ready"
	StateUnauthenticated = "unauthenticated"
	StateUnavailable     = "unavailable"
)

// Capability strings recorded on protocol.Agent.Capabilities. They are facts
// the agent reported during initialize, never assumptions from its name.
const (
	CapLoadSession    = "load-session"
	CapImagePrompt    = "image-prompt"
	CapEmbeddedPrompt = "embedded-context"
	CapSessionClose   = "session-close"
	CapAudioPrompt    = "audio-prompt"
)

// Environment overrides for the user-owned adapter executables. They are read
// once at server start; a settings form for agent commands is deferred.
const (
	EnvClaudeCommand = "TUI_GO_AGENT_CLAUDE_COMMAND"
	EnvCodexCommand  = "TUI_GO_AGENT_CODEX_COMMAND"
)

// Default adapter executables resolved on the server's PATH. The application
// never installs or downloads them.
const (
	DefaultClaudeCommand = "claude-agent-acp"
	DefaultCodexCommand  = "codex-acp"
)

// IsACP reports whether a thread's recorded agent identity uses a real ACP
// connection rather than the fixture.
func IsACP(agentID string) bool { return agentID != "" && agentID != FixtureID }

// Defaults returns the configured agent registry for a server start. getenv
// supplies the process environment; a nil value uses no overrides.
func Defaults(getenv func(string) string) []protocol.Agent {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	command := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	return []protocol.Agent{
		{ID: FixtureID, Name: FixtureName, Kind: KindFixture, State: StateReady, Detail: "Synthetic agent. No process, tool or provider is executed.", Revision: 1},
		{ID: "claude", Name: "Claude", Kind: KindACP, Command: command(EnvClaudeCommand, DefaultClaudeCommand), State: StateUnprobed, Detail: "Not probed yet. Probe to read this adapter's models and settings.", Revision: 1},
		{ID: "codex", Name: "Codex", Kind: KindACP, Command: command(EnvCodexCommand, DefaultCodexCommand), State: StateUnprobed, Detail: "Not probed yet. Probe to read this adapter's models and settings.", Revision: 1},
	}
}

// Ensure adds any missing default agents to an existing snapshot and refreshes
// the executables from the environment without discarding probe results.
func Ensure(s *protocol.Snapshot, getenv func(string) string) {
	for _, def := range Defaults(getenv) {
		found := false
		for i := range s.Agents {
			if s.Agents[i].ID != def.ID {
				continue
			}
			found = true
			existing := &s.Agents[i]
			existing.Name, existing.Kind = def.Name, def.Kind
			if existing.Kind == KindFixture {
				existing.State, existing.Detail = StateReady, def.Detail
				existing.Command, existing.Args = "", nil
				break
			}
			if existing.Command != def.Command {
				// A changed executable invalidates every probed fact.
				existing.Command = def.Command
				existing.State, existing.Detail = StateUnprobed, "Configured executable changed; probe again."
				existing.Version, existing.ProbedAt = "", ""
				existing.Options, existing.Fields, existing.Capabilities = nil, protocol.SettingFields{}, nil
			}
			if existing.State == StateProbing {
				// No probe survives a restart.
				existing.State, existing.Detail = StateUnprobed, "A probe was interrupted by a server restart; probe again."
			}
			if existing.Revision == 0 {
				existing.Revision = 1
			}
			existing.Revision++
			break
		}
		if !found {
			s.Agents = append(s.Agents, def)
		}
	}
}

// Find returns the configured agent with the given ID, or nil.
func Find(s *protocol.Snapshot, id string) *protocol.Agent {
	for i := range s.Agents {
		if s.Agents[i].ID == id {
			return &s.Agents[i]
		}
	}
	return nil
}
