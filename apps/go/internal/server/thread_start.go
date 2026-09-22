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
func startThread(s *protocol.Snapshot, c protocol.Command) (string, error) {
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
	id, err := applyProject(&next, create)
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
