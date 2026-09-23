package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Retention bounds for streamed agent output. A turn that streams for a long
// time must not grow the snapshot without limit.
const (
	MaxActivityText = 256 << 10
	MaxActivityDeta = 64 << 10
	ActivityLimit   = 128
)

// Normalize applies one ACP session update to a thread. It performs no I/O, so
// recorded streams replay through it exactly as live ones do. Config updates
// recompute the option mapping from the newly reported catalogue.
//
// Dispatch is driven by the wire kind, never by whichever SDK union variant
// happened to match: v0.13.5 decodes an unrecognised kind into a known variant
// by shape (see Update). Every kind reaches the transcript; an unrecognised one
// is retained as a tool row titled with the kind rather than dropped.
func Normalize(t *protocol.Thread, u Update) {
	typed := u.Typed
	switch u.Kind {
	case "agent_message_chunk":
		if typed.AgentMessageChunk != nil {
			appendChunk(t, "agent-"+t.TurnID, "agent", "", text(typed.AgentMessageChunk.Content))
		}
	case "agent_thought_chunk":
		if typed.AgentThoughtChunk != nil {
			appendChunk(t, "thought-"+t.TurnID, "thought", "Thinking", text(typed.AgentThoughtChunk.Content))
		}
	case "user_message_chunk":
		// The agent echoes the prompt the server just sent; the captured user
		// activity already records it. Echoing it again would duplicate history.
	case "tool_call":
		if c := typed.ToolCall; c != nil {
			applyTool(t, string(c.ToolCallId), toolPatch{Title: &c.Title, Kind: stringOrNil(string(c.Kind)), Status: stringOrNil(string(c.Status)), Locations: c.Locations, Content: c.Content, RawInput: c.RawInput, RawOutput: c.RawOutput})
		}
	case "tool_call_update":
		if c := typed.ToolCallUpdate; c != nil {
			patch := toolPatch{Title: c.Title, Locations: c.Locations, Content: c.Content, RawInput: c.RawInput, RawOutput: c.RawOutput}
			if c.Kind != nil {
				patch.Kind = stringOrNil(string(*c.Kind))
			}
			if c.Status != nil {
				patch.Status = stringOrNil(string(*c.Status))
			}
			applyTool(t, string(c.ToolCallId), patch)
		}
	case "plan":
		if typed.Plan != nil {
			plan := make([]protocol.PlanStep, 0, len(typed.Plan.Entries))
			for _, entry := range typed.Plan.Entries {
				plan = append(plan, protocol.PlanStep{Title: label(entry.Content), State: planState(entry.Status)})
			}
			t.Plan = plan
		}
	case "config_option_update":
		if typed.ConfigOptionUpdate != nil {
			// The agent reports its complete option state, and switching one
			// option can add another, so the catalogue is replaced whole.
			t.Options, _ = MapOptions(typed.ConfigOptionUpdate.ConfigOptions)
			fields := Fields(t.Options)
			t.Effective = EffectiveSettings(fields, t.Options)
			note(t, "config-options", "Agent settings updated", summarizeOptions(t.Options))
		}
	case "current_mode_update":
		if typed.CurrentModeUpdate != nil {
			// A session mode is a separate identity from a config option value,
			// so it is reported rather than written into the effective settings.
			note(t, "current-mode", "Agent mode changed", label(string(typed.CurrentModeUpdate.CurrentModeId)))
		}
	case "available_commands_update":
		if typed.AvailableCommandsUpdate != nil {
			names := make([]string, 0, len(typed.AvailableCommandsUpdate.AvailableCommands))
			for _, command := range typed.AvailableCommandsUpdate.AvailableCommands {
				names = append(names, command.Name)
			}
			note(t, "available-commands", fmt.Sprintf("Agent commands available (%d)", len(names)), label(strings.Join(names, ", ")))
		}
	case "usage_update", "tui_usage_update":
		normalizeUsage(t, u)
	case "tui_question_delivery":
		// Delivery receipts are correlated by the server, never transcript rows.
	case "session_info_update":
		var info struct {
			Title *string `json:"title"`
		}
		if json.Unmarshal(u.Raw, &info) == nil && info.Title != nil && strings.TrimSpace(*info.Title) != "" {
			t.Title = label(strings.TrimSpace(*info.Title))
		}
	default:
		kind := u.Kind
		if kind == "" {
			kind = "unknown"
		}
		kind = label(kind)
		note(t, "update-"+kind, "Unrecognised agent update: "+kind, Truncate(Sanitize(string(u.Raw)), MaxActivityDeta))
	}
	TrimActivity(t)
}

// now is replaced in tests so replayed streams produce stable records.
var now = func() string { return time.Now().UTC().Format(time.RFC3339) }

func planState(status acp.PlanEntryStatus) string {
	switch status {
	case acp.PlanEntryStatusInProgress:
		return "active"
	case acp.PlanEntryStatusCompleted:
		return "completed"
	default:
		return "pending"
	}
}

func text(block acp.ContentBlock) string {
	switch {
	case block.Text != nil:
		return block.Text.Text
	case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
		return block.Resource.Resource.TextResourceContents.Text
	case block.ResourceLink != nil:
		return block.ResourceLink.Uri
	case block.Image != nil:
		return "[image content omitted]"
	case block.Audio != nil:
		return "[audio content omitted]"
	}
	return ""
}

func stringOrNil(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func find(t *protocol.Thread, id string) *protocol.Activity {
	for i := range t.Activity {
		if t.Activity[i].ID == id {
			return &t.Activity[i]
		}
	}
	return nil
}

func upsert(t *protocol.Thread, a protocol.Activity) *protocol.Activity {
	if existing := find(t, a.ID); existing != nil {
		return existing
	}
	t.Activity = append(t.Activity, a)
	return &t.Activity[len(t.Activity)-1]
}

// appendChunk accumulates streamed text into one activity per turn and role, so
// a long reply is one transcript entry rather than hundreds of fragments.
func appendChunk(t *protocol.Thread, id, role, title, chunk string) {
	if chunk == "" {
		return
	}
	// A submitted question answer separates the conversation into segments.
	// Continuing the same native turn must not append its reply above the Q&A.
	for i := len(t.Activity) - 1; i >= 0; i-- {
		anchor := t.Activity[i]
		if anchor.Role == "question-answer" && anchor.TurnID == t.TurnID {
			id += ":after:" + anchor.ID
			break
		}
	}
	// Adapters emit trailing chunks after a turn has already been cancelled or
	// failed. The text is retained, but a settled entry keeps its outcome and a
	// late first chunk opens in the turn's outcome: retained output must never
	// make a finished turn claim it is still running.
	a := upsert(t, protocol.Activity{ID: id, TurnID: t.TurnID, Role: role, Title: title, State: streamState(t)})
	a.Text = Truncate(a.Text+Sanitize(chunk), MaxActivityText)
	if a.State == "" {
		a.State = streamState(t)
	}
}

// streamState is the state a streamed entry takes from the turn it belongs to.
func streamState(t *protocol.Thread) string {
	switch t.State {
	case "interrupted":
		return "interrupted"
	case "failed":
		return "failed"
	case "idle":
		return "completed"
	}
	return "running"
}

// note records one session-level agent notification. The identity is scoped to
// the turn it arrived in so a later turn adds its own row instead of rewriting
// an earlier entry's outcome and position in the transcript.
func note(t *protocol.Thread, id, title, detail string) {
	a := upsert(t, protocol.Activity{ID: id + "-" + t.TurnID, TurnID: t.TurnID, Role: "tool", State: "completed"})
	a.Title, a.Text, a.State = label(title), label(title), "completed"
	a.Detail = Truncate(detail, MaxActivityDeta)
}

// toolPatch carries only the fields one update actually supplied. A nil field
// leaves the retained value untouched: an omitted rawInput or rawOutput must
// never erase what an earlier update reported.
type toolPatch struct {
	Title, Kind, Status *string
	Locations           []acp.ToolCallLocation
	Content             []acp.ToolCallContent
	RawInput, RawOutput any
}

// toolDetail is the retained inspector payload for one tool call.
type toolDetail struct {
	Kind      string            `json:"kind,omitempty"`
	Status    string            `json:"status,omitempty"`
	Locations []string          `json:"locations,omitempty"`
	Content   []string          `json:"content,omitempty"`
	RawInput  json.RawMessage   `json:"rawInput,omitempty"`
	RawOutput json.RawMessage   `json:"rawOutput,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

func toolState(status string) string {
	switch status {
	case "in_progress":
		return "running"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "":
		return ""
	default:
		return "pending"
	}
}

// mcpCall reports whether an adapter marked this tool call as an MCP tool. ACP
// v1 has no dedicated flag, so only an explicit _meta marker or the widely used
// "mcp__server__tool" naming counts; nothing is inferred from the kind alone.
func mcpCall(id string, title *string) bool {
	if strings.HasPrefix(id, "mcp__") {
		return true
	}
	return title != nil && strings.HasPrefix(*title, "mcp__")
}

func applyTool(t *protocol.Thread, id string, patch toolPatch) {
	if id == "" {
		return
	}
	role := "tool"
	if mcpCall(id, patch.Title) {
		role = "mcp"
	}
	a := upsert(t, protocol.Activity{ID: "tool-" + id, TurnID: t.TurnID, Role: role, Title: label(id), State: "pending"})
	a.Role = role
	var detail toolDetail
	if a.Detail != "" {
		_ = json.Unmarshal([]byte(a.Detail), &detail)
	}
	if patch.Title != nil && *patch.Title != "" {
		a.Title = label(*patch.Title)
	}
	if patch.Kind != nil {
		detail.Kind = label(*patch.Kind)
	}
	if patch.Status != nil {
		if state := toolState(*patch.Status); state != "" {
			a.State, detail.Status = state, label(*patch.Status)
		}
	}
	if len(patch.Locations) > 0 {
		detail.Locations = detail.Locations[:0]
		for _, location := range patch.Locations {
			detail.Locations = append(detail.Locations, label(location.Path))
		}
	}
	if len(patch.Content) > 0 {
		detail.Content = detail.Content[:0]
		for _, content := range patch.Content {
			detail.Content = append(detail.Content, label(describeContent(content)))
		}
	}
	if patch.RawInput != nil {
		detail.RawInput = encode(patch.RawInput)
	}
	if patch.RawOutput != nil {
		detail.RawOutput = encode(patch.RawOutput)
	}
	a.Text = summarizeTool(a.Title, detail)
	encoded, err := json.Marshal(detail)
	if err != nil {
		encoded = []byte(`{"error":"tool detail could not be encoded"}`)
	}
	a.Detail = Truncate(string(encoded), MaxActivityDeta)
}

func encode(value any) json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil || len(b) > MaxActivityDeta {
		summary, _ := json.Marshal(Truncate(Sanitize(fmt.Sprint(value)), MaxActivityDeta))
		return summary
	}
	return json.RawMessage(b)
}

func describeContent(content acp.ToolCallContent) string {
	switch {
	case content.Content != nil:
		return text(content.Content.Content)
	case content.Diff != nil:
		return "diff " + content.Diff.Path
	case content.Terminal != nil:
		return "terminal " + string(content.Terminal.TerminalId)
	}
	return ""
}

// summarizeTool keeps ordinary activity to one compact line; the retained
// detail stays available in the inspector.
func summarizeTool(title string, detail toolDetail) string {
	parts := make([]string, 0, 3)
	if detail.Kind != "" {
		parts = append(parts, detail.Kind)
	}
	if len(detail.Locations) > 0 {
		parts = append(parts, strings.Join(detail.Locations, ", "))
	} else if len(detail.Content) > 0 {
		if line := firstLine(detail.Content[0]); line != "" {
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		return title
	}
	return label(strings.Join(parts, " · "))
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

func summarizeOptions(options []protocol.ConfigOption) string {
	lines := make([]string, 0, len(options))
	for _, option := range options {
		lines = append(lines, option.Name+": "+option.Current)
	}
	return label(strings.Join(lines, "\n"))
}

// TrimActivity applies the shared 128-item retention limit and records that it
// truncated history rather than silently losing it.
func TrimActivity(t *protocol.Thread) {
	if len(t.Activity) <= ActivityLimit {
		return
	}
	notice := protocol.Activity{ID: "history-limit-" + t.ID, Role: "tool", Title: "History retention", State: "completed", Text: "Earlier activity was truncated by the 128-item retention limit."}
	// The current turn's input is the durable recovery capture, not disposable
	// stream output. Keep it through long tool streams and terminal failure.
	cut := len(t.Activity) - (ActivityLimit - 1)
	for _, entry := range t.Activity[:cut] {
		if entry.Role == "user" && entry.Prompt != nil && entry.ID == t.TurnID {
			t.Activity = append([]protocol.Activity{notice, entry}, t.Activity[len(t.Activity)-(ActivityLimit-2):]...)
			return
		}
	}
	t.Activity = append([]protocol.Activity{notice}, t.Activity[len(t.Activity)-(ActivityLimit-1):]...)
}

// PermissionRequest builds the approval card for one ACP permission request.
// ChoiceIDs stay parallel to Choices so an answer resolves the agent's own
// option identity rather than a display name.
func PermissionRequest(id string, p acp.RequestPermissionRequest) protocol.Request {
	request := protocol.Request{ID: id, Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Origin: "Agent", Delivery: "acp-pending"}
	title := "Approve agent action"
	if p.ToolCall.Title != nil && *p.ToolCall.Title != "" {
		title = *p.ToolCall.Title
	}
	request.Title = label(title)
	lines := []string{"Tool call: " + string(p.ToolCall.ToolCallId)}
	if p.ToolCall.Kind != nil {
		lines = append(lines, "Kind: "+string(*p.ToolCall.Kind))
	}
	for _, location := range p.ToolCall.Locations {
		lines = append(lines, "Location: "+location.Path)
	}
	// The recorded claude adapter supplies the proposed change as a diff content
	// block; an approval card that hid it would ask for consent to an unseen
	// edit.
	for _, content := range p.ToolCall.Content {
		if content.Diff != nil {
			lines = append(lines, "Diff for "+content.Diff.Path+":")
			if content.Diff.OldText != nil {
				lines = append(lines, "- "+strings.ReplaceAll(*content.Diff.OldText, "\n", "\n- "))
			}
			lines = append(lines, "+ "+strings.ReplaceAll(content.Diff.NewText, "\n", "\n+ "))
			continue
		}
		if described := describeContent(content); described != "" {
			lines = append(lines, described)
		}
	}
	if p.ToolCall.RawInput != nil {
		lines = append(lines, "Input: "+string(encode(p.ToolCall.RawInput)))
	}
	request.Detail = Truncate(Sanitize(strings.Join(lines, "\n")), MaxActivityDeta)
	for _, option := range p.Options {
		request.Choices = append(request.Choices, label(option.Name))
		request.ChoiceIDs = append(request.ChoiceIDs, string(option.OptionId))
	}
	return request
}
