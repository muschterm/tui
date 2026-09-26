package server

import (
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// resolveAgent maps a requested agent identity to a configured record. The
// fixture's display name is still accepted so existing clients keep working.
func resolveAgent(s *protocol.Snapshot, requested string) (*protocol.Agent, error) {
	if requested == "" {
		return nil, failure("unsupported_agent", "choose an agent for this thread")
	}
	if requested == agent.FixtureName {
		requested = agent.FixtureID
	}
	a := agent.Find(s, requested)
	if a == nil {
		if requested == agent.FixtureID {
			return &protocol.Agent{ID: agent.FixtureID, Name: agent.FixtureName, Kind: agent.KindFixture, State: agent.StateReady}, nil
		}
		return nil, failure("unsupported_agent", "agent is not configured on this server")
	}
	if a.Kind == agent.KindACP && a.State != agent.StateReady {
		return nil, failure("agent_unavailable", a.Name+" is not ready; probe it before starting a thread. "+a.Detail)
	}
	return a, nil
}

// Construct both records on a candidate snapshot. Neither a rejected initial
// prompt nor a failed durable commit exposes an empty thread to other clients.
// A worktree start (ADR 0024) runs Git and is handled by the engine
// (worktrees.go), which calls startThreadIn once the worktree exists.
func startThread(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.Workspace != nil {
		switch c.Workspace.Mode {
		case "checkout":
			if c.Workspace.StartOid != "" || c.Workspace.Branch != "" {
				return "", failure("invalid", "a checkout workspace takes no start commit or branch")
			}
			return startThreadIn(s, c, threadPlace{explicit: true})
		case "worktree":
			return "", failure("unsupported_workspace", "worktree creation needs the server's worktree support")
		default:
			return "", failure("invalid", "workspace mode must be checkout or worktree")
		}
	}
	return startThreadIn(s, c, threadPlace{})
}

// threadPlace is where a new thread works: the project's checkout unless
// checkout is set (a managed worktree). explicit skips the workspace default,
// which the client resolved.
type threadPlace struct {
	checkout, worktreeID string
	explicit             bool
}

func startThreadIn(s *protocol.Snapshot, c protocol.Command, place threadPlace) (string, error) {
	if _, err := resolveAgent(s, c.Agent); err != nil {
		return "", err
	}
	if c.Settings == nil {
		return "", failure("invalid", "starting a thread requires captured settings")
	}
	next := clone(*s)
	create := c
	create.Kind, create.Text = "thread.create", ""
	create.Agent = c.Agent
	id, err := createThread(&next, create, place)
	if err != nil {
		return "", err
	}
	send := c
	send.Kind, send.ThreadID = "prompt.send", id
	if _, err := apply(&next, send); err != nil {
		return "", err
	}
	*s = next
	return id, nil
}
