// Package fixture supplies explicitly synthetic agent data. It executes no tools.
package fixture

import (
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Initial returns the synthetic starting snapshot for a new application home.
func Initial() protocol.Snapshot {
	optional := false
	s := protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	thread := protocol.Thread{ProjectID: "project-fixture", ID: "thread-shell", TurnID: "intro", Project: "Terminal workspace", Title: "Build the workspace shell", Checkout: "fixture://workspace", Agent: agent.FixtureName, AgentID: "", State: "running", Selected: s, Effective: s, QueueRevision: 1,
		Activity: []protocol.Activity{{ID: "intro", Role: "user", Text: "Build a calm workspace with focused navigation and useful inspectors."}, {ID: "reply", Role: "agent", Title: "Fixture agent", Text: "I’m working through the shell layout and keeping activity available while you compose."}, {ID: "tool", Role: "tool", Title: "Inspect layout contract", State: "completed", Text: "Read 3 design documents", Detail: "Synthetic tool fixture.\nInputs: docs/design/layout.md, activity.md, questions.md\nResult: right surfaces are singleton except Terminal; pane hiding preserves sessions.\nNo filesystem tool was executed."}},
		Plan:     []protocol.PlanStep{{Title: "Inspect the interaction contract", State: "completed"}, {Title: "Build the workspace shell", State: "active"}, {Title: "Review narrow layouts", State: "pending"}},
		Children: []protocol.Child{{ID: "child-layout", ParentID: "thread-shell", Name: "Layout review", State: "running", Activity: []protocol.Activity{{ID: "child-message", Role: "agent", Text: "Checking navigation, split panes, and compact presentation. This history is synthetic."}}}},
		Requests: []protocol.Request{{ID: "question-async", Kind: "question", Mode: "async", State: "pending", Revision: 1, Title: "Choose the review focus", Origin: "Fixture agent", Detail: "Synthetic asynchronous request. Fixture ticks continue while awaiting an answer.", Questions: []protocol.Question{{ID: "focus", Label: "Focus", Kind: "single", AllowOther: true, Text: "Which area should receive the next review?", Options: []string{"Keyboard flow", "Compact layout", "Inspector detail"}}, {ID: "style", Label: "Priorities", Kind: "multiple", AllowOther: true, Text: "What should the review emphasize?", Options: []string{"Clarity", "Density"}}, {ID: "notes", Label: "Notes", Kind: "text", Required: &optional, Text: "Any additional review notes? (optional)"}}}}}
	second := protocol.Thread{ProjectID: "project-fixture", ID: "thread-review", TurnID: "thread-review-initial", Project: "Terminal workspace", Title: "Review keyboard flow", Checkout: "fixture://review", Agent: "Fixture agent", State: "waiting", Selected: s, Effective: s, QueueRevision: 1, Activity: []protocol.Activity{{ID: "review-intro", Role: "agent", Text: "The fixture is waiting for your explicit decision."}}, Requests: []protocol.Request{{ID: "approval-review", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Title: "Run the synthetic layout check", Detail: "Action: fixture check\nLocation: fixture://review\nNo subprocess or file write will occur.", Origin: "Fixture agent", Choices: []string{"Allow once", "Deny"}}}}
	thread.Queue = []protocol.Prompt{{ID: "prompt-initial", Text: "Review the compact layout after the shell", Revision: 1, Settings: s}}
	thread.Activity = append(thread.Activity, protocol.Activity{ID: "mcp-fixture", Role: "mcp", Title: "design / inspect_surface", State: "completed", Text: "Inspect singleton surface behavior", Detail: "Synthetic MCP fixture\nServer: design\nTool: inspect_surface\nArguments: {\"surface\":\"Plan\"}\nResult: existing tab focused. No MCP connection was made."})
	for _, name := range []string{"Keyboard review", "Inspector review", "Compact layout review"} {
		thread.Children = append(thread.Children, protocol.Child{ID: "child-" + name, ParentID: thread.ID, Name: name, State: "running", Activity: []protocol.Activity{{ID: "note", Role: "agent", Text: "Synthetic child history; reviewing the supplied scenario."}}})
	}
	thread.Children = append(thread.Children, protocol.Child{ID: "child-done", ParentID: thread.ID, Name: "Contract review", State: "completed", Activity: []protocol.Activity{{ID: "result", Role: "agent", Text: "Fixture contract review complete."}}})
	second.Requests = append(second.Requests, protocol.Request{ID: "question-blocking", Kind: "question", Mode: "blocking", State: "pending", Revision: 1, Title: "Confirm keyboard review scope", Origin: "Fixture agent", Actions: []string{protocol.RequestActionDecline, protocol.RequestActionCancel}, Questions: []protocol.Question{{ID: "scope", Text: "Which keyboard flow should be checked?", Options: []string{"Navigation", "Composer"}, OptionDescriptions: []string{"Pane focus, thread cards and the attention bell", ""}}}})
	// The fixture agent record is part of this synthetic starting state; real
	// ACP connections are added by the server from its own configuration.
	agents := []protocol.Agent{agent.Defaults(nil)[0]}
	return protocol.Snapshot{Agents: agents, AppSettings: protocol.AppSettings{Revision: 1, WorkspaceDefault: "checkout"}, Projects: []protocol.Project{{ID: "project-fixture", Name: "Terminal workspace", Path: "fixture://workspace", Revision: 1}}, Capabilities: []string{"fixture-agent", "fixture-steering", "fixture-terminal", "prompt-queue", "fixture-blocking-questions", "fixture-async-questions", "client-views", "thread-lifecycle", "thread-start", "closed-thread-send", "workspace-info", "project-management", "app-settings", "project-settings", "restart-continuation"}, Version: protocol.Version, Revision: 1, Threads: []protocol.Thread{thread, second}}
}
