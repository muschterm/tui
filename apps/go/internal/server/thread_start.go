package server

import "github.com/muschterm/tui/apps/go/internal/protocol"

// Construct both records on a candidate snapshot. Neither a rejected initial
// prompt nor a failed durable commit exposes an empty thread to other clients.
func startThread(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.Agent != "Fixture agent" {
		return "", failure("unsupported_agent", "only Demo fixture is available")
	}
	if c.Settings == nil {
		return "", failure("invalid", "starting a thread requires captured settings")
	}
	next := clone(*s)
	create := c
	create.Kind, create.Text = "thread.create", ""
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
