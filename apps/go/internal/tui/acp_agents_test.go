package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// acpAgentRecord is one probed ACP agent: two mapped option categories, one
// unmapped option, and no context or speed option at all.
func acpAgentRecord() protocol.Agent {
	return protocol.Agent{
		ID: "claude", Name: "Claude", Kind: "acp", State: "ready", Revision: 2,
		Options: []protocol.ConfigOption{
			{ID: "model", Name: "Model", Category: "model", Type: "select", Current: "sonnet", Values: []protocol.ConfigValue{
				{Value: "sonnet", Name: "Sonnet"},
				{Value: "opus", Name: "Opus", Description: "Highest capability"},
			}},
			{ID: "thought", Name: "Thinking", Category: "thought_level", Type: "select", Current: "medium", Values: []protocol.ConfigValue{
				{Value: "off", Name: "Off"},
				{Value: "medium", Name: "Medium"},
			}},
			{ID: "mode", Name: "Permission mode", Category: "mode", Type: "select", Current: "ask", Values: []protocol.ConfigValue{
				{Value: "ask", Name: "Ask every time"},
				{Value: "edits", Name: "Accept edits"},
			}},
			{ID: "verbose", Name: "Verbose logs", Category: "other", Type: "boolean", Current: "false", Values: []protocol.ConfigValue{
				{Value: "true", Name: "On"}, {Value: "false", Name: "Off"},
			}},
		},
		Fields:       protocol.SettingFields{Model: "model", Effort: "thought", Permissions: "mode"},
		Capabilities: []string{"load-session", "session-close"},
	}
}

// acpModel is a server that publishes agent records: the fixture agent, one
// probed ACP agent and one that reports required authentication.
func acpModel() *Model {
	m := navigationModel()
	m.state.Icons = "ascii"
	m.applyIcons()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "acp-agents", "agent-probe", "acp-permissions", "acp-cancel")
	m.snapshot.Agents = []protocol.Agent{
		{ID: "fixture", Name: "Fixture agent", Kind: "fixture", State: "ready"},
		acpAgentRecord(),
		{ID: "codex", Name: "Codex", Kind: "acp", State: "unauthenticated", Detail: "Sign in with the codex CLI first"},
	}
	return m
}

// menuCapture is the rendered menu box with styling removed: a deterministic
// capture of exactly what the menu paints.
func menuCapture(t *testing.T, m *Model) string {
	t.Helper()
	f := m.render()
	r := m.menuRect()
	var rows []string
	for y := r.Y; y < r.Y+r.H && y < len(f.rows); y++ {
		rows = append(rows, strings.TrimRight(ansi.Strip(cutCells(f.rows[y], r.X, r.X+r.W)), " "))
	}
	return strings.Join(rows, "\n")
}

func menuLabels(m *Model) []string {
	var labels []string
	for _, item := range m.menu {
		labels = append(labels, item.Label)
	}
	return labels
}

func TestAgentMenuListsReadinessAndOffersProbe(t *testing.T) {
	m := acpModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	m.activate(action{Kind: "settings", Value: "agent"})
	want := []string{
		"( ) Fixture agent · ready · fixture",
		"(*) Claude · ready",
		"( ) Codex · unauthenticated · Sign in with the codex CLI first",
		"Refresh options · Claude",
		"Agent defaults…",
	}
	got := menuLabels(m)
	if len(got) != len(want) {
		t.Fatalf("agent menu: %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent menu row %d: %q", i, got[i])
		}
	}
	// Every reported readiness reaches the menu with its own detail.
	for _, tc := range []struct{ state, detail, want string }{
		{"ready", "", "( ) Codex · ready"},
		{"unprobed", "", "( ) Codex · unprobed"},
		{"probing", "starting adapter", "( ) Codex · probing · starting adapter"},
		{"unavailable", "codex-acp not found on PATH", "( ) Codex · unavailable · codex-acp not found on PATH"},
		{"", "", "( ) Codex · unknown"},
	} {
		m.snapshot.Agents[2].State, m.snapshot.Agents[2].Detail = tc.state, tc.detail
		m.activate(action{Kind: "settings", Value: "agent"})
		if got := m.menu[2].Label; got != tc.want {
			t.Fatalf("%s: %q", tc.state, got)
		}
	}
}

func TestAgentMenuViewCapture(t *testing.T) {
	m := acpModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	m.activate(action{Kind: "settings", Value: "agent"})
	// ASCII symbols keep the capture independent of the reviewer's font.
	want := strings.Join([]string{
		"+------------------------------------------------------------------+",
		"| Settings · agent                                               x |",
		"|>( ) Fixture agent · ready · fixture                              |",
		"| (*) Claude · ready                                               |",
		"| ( ) Codex · unauthenticated · Sign in with the codex CLI first   |",
		"| Refresh options · Claude                                         |",
		"| Agent defaults…                                                  |",
		"| ↑ ↓  Enter  Esc  1/5                                             |",
		"+------------------------------------------------------------------+",
	}, "\n")
	if got := menuCapture(t, m); got != want {
		t.Fatalf("agent menu capture:\n%s", got)
	}
}

func TestOptionMenusComeFromAgentOptions(t *testing.T) {
	m := acpModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	m.activate(action{Kind: "settings", Value: "model"})
	if got := menuLabels(m); strings.Join(got, "|") != "(*) Sonnet|( ) Opus · Highest capability" {
		t.Fatalf("model menu: %q", got)
	}
	m.activate(action{Kind: "setting-field", ID: "model", Value: "opus"})
	m.activate(action{Kind: "settings", Value: "model"})
	if got := menuLabels(m); strings.Join(got, "|") != "( ) Sonnet|(*) Opus · Highest capability" {
		t.Fatalf("model selection unmarked: %q", got)
	}
	m.activate(action{Kind: "settings", Value: "effort"})
	if got := menuLabels(m); strings.Join(got, "|") != "( ) Off|(*) Medium" {
		t.Fatalf("effort menu: %q", got)
	}
	// A field with no option offers nothing: the agent's own default applies.
	m.activate(action{Kind: "settings", Value: "context"})
	if got := menuLabels(m); strings.Join(got, "|") != "Agent default · Claude offers no context option" {
		t.Fatalf("context menu invented choices: %q", got)
	}
	m.activate(action{Kind: "settings", Value: "details"})
	want := []string{"Claude · ready", "Model: Model", "Effort: Thinking", "Permissions: Permission mode", "Verbose logs: Off · agent default", "Capabilities: load-session, session-close"}
	if got := menuLabels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("details menu: %q", got)
	}
	if v := m.viewState().Settings; v.Model != "opus" || v.Effort != "medium" || v.Permissions != "ask" || v.Context != "" || v.Speed != "" {
		t.Fatalf("captured values are not option value IDs: %+v", v)
	}
}

func TestChangingAgentResetsDependentSettingsAndRetainsPerProject(t *testing.T) {
	m := acpModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	m.activate(action{Kind: "setting-field", ID: "effort", Value: "off"})
	if got := m.viewState().Settings; got.Model != "sonnet" || got.Effort != "off" {
		t.Fatalf("agent defaults not applied: %+v", got)
	}
	m.activate(action{Kind: "setting-agent", Value: "fixture"})
	if got := m.viewState().Settings; got != (protocol.Settings{Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}) {
		t.Fatalf("changing agent kept stale values: %+v", got)
	}
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	m.beginThreadDraft("beta")
	m.activate(action{Kind: "setting-agent", Value: "fixture"})
	m.beginThreadDraft("alpha")
	if m.viewState().Agent != "claude" || m.state.DraftThreads["beta"].Agent != "fixture" {
		t.Fatalf("draft agent not retained per project: %q / %q", m.viewState().Agent, m.state.DraftThreads["beta"].Agent)
	}
	// Changing the model keeps values the agent still offers.
	m.activate(action{Kind: "setting-field", ID: "model", Value: "opus"})
	if got := m.viewState().Settings; got.Effort != "medium" || got.Permissions != "ask" {
		t.Fatalf("model change lost valid values: %+v", got)
	}
	m.viewState().Settings.Effort = "withdrawn"
	m.activate(action{Kind: "setting-field", ID: "model", Value: "sonnet"})
	if m.viewState().Settings.Effort != "medium" {
		t.Fatalf("model change kept an unsupported effort: %+v", m.viewState().Settings)
	}
	m.Update(tea.PasteMsg{Content: "first prompt"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "thread.start" || m.busy.Agent != "claude" {
		t.Fatalf("thread.start did not carry the agent ID: %+v", m.busy)
	}
}

func TestUnprobedAgentOffersProbeCommand(t *testing.T) {
	m := acpModel()
	m.snapshot.Agents[1].State, m.snapshot.Agents[1].Options = "unprobed", nil
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "setting-agent", Value: "claude"})
	if got := menuLabels(m); strings.Join(got, "|") != "Probe · Claude|Keep Claude agent defaults" {
		t.Fatalf("no probe offered: %q", got)
	}
	m.activate(action{Kind: "agent-probe", ID: "claude"})
	if m.busy == nil || m.busy.Kind != "agent.probe" || m.busy.TargetID != "claude" || m.busy.ProjectID != "alpha" {
		t.Fatalf("probe command: %+v", m.busy)
	}
}

func TestSendBlockedUsesAgentOptionsAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Model)
		want  string
	}{
		{"ready", func(m *Model) { m.activate(action{Kind: "setting-agent", Value: "claude"}) }, ""},
		{"unauthenticated", func(m *Model) {
			m.activate(action{Kind: "setting-agent", Value: "codex"})
		}, "Codex needs authentication in its own CLI first · Sign in with the codex CLI first"},
		{"unprobed", func(m *Model) {
			m.snapshot.Agents[1].State = "unprobed"
			m.activate(action{Kind: "setting-agent", Value: "claude"})
		}, "Claude has no probed options · choose Probe first"},
		{"missing value", func(m *Model) {
			m.activate(action{Kind: "setting-agent", Value: "claude"})
			m.viewState().Settings.Model = ""
		}, "Choose a model for Claude"},
		{"withdrawn value", func(m *Model) {
			m.activate(action{Kind: "setting-agent", Value: "claude"})
			m.viewState().Settings.Permissions = "yolo"
		}, "yolo is no longer an offered permissions for Claude"},
		{"fixture rule kept", func(m *Model) {
			m.activate(action{Kind: "setting-agent", Value: "fixture"})
		}, "Choose the Demo Reference model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := acpModel()
			m.beginThreadDraft("alpha")
			tc.setup(m)
			m.Update(tea.PasteMsg{Content: "prompt"})
			if got := m.sendBlocked(); got != tc.want {
				t.Fatalf("sendBlocked = %q", got)
			}
		})
	}
	// A server without agent records keeps the previous fixture-only message.
	m := navigationModel()
	m.beginThreadDraft("alpha")
	m.Update(tea.PasteMsg{Content: "prompt"})
	if got := m.sendBlocked(); got != "Choose the Demo Reference model; Codex and Claude are not connected yet" {
		t.Fatalf("legacy server message changed: %q", got)
	}
}

func TestFailedThreadShowsErrorAndKeepsSend(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID, thread.State, thread.Error = "claude", "failed", "session/prompt rejected the selected model"
	thread.Selected, thread.Effective = protocol.Settings{Model: "sonnet", Effort: "medium", Permissions: "ask"}, protocol.Settings{}
	m.state.Threads[thread.ID].Settings = thread.Selected
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: "try again"})
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Failed · session/prompt rejected the selected model") {
		t.Fatalf("failure not shown above the composer:\n%s", view)
	}
	f := m.measure()
	if !hasControl(f, "send") || hasControl(f, "interrupt") {
		t.Fatal("failed thread lost Send or still offers Stop")
	}
	if got := m.sendBlocked(); got != "" {
		t.Fatalf("failed thread blocked Send: %q", got)
	}
	if threadIndicator(*thread) != threadFailed {
		t.Fatal("navigation indicator is not failed")
	}
}

func TestThoughtAndToolRowsRenderDistinctly(t *testing.T) {
	m := acpModel()
	p := m.colors()
	items := []protocol.Activity{
		{ID: "t1", Role: "thought", Text: "Weighing two approaches"},
		{ID: "c1", Role: "tool", Title: "Read files", Text: "3 files", State: "pending"},
		{ID: "c2", Role: "tool", Title: "Run tests", Text: "go test", State: "running"},
		{ID: "c3", Role: "tool", Title: "Patch", Text: "1 edit", State: "completed"},
		{ID: "c4", Role: "mcp", Title: "Lookup", Text: "timeout", State: "failed"},
	}
	var rows []string
	for _, line := range m.activityLines(items, 60) {
		if strings.TrimSpace(line.text) != "" {
			rows = append(rows, line.fg+"|"+line.text)
		}
	}
	want := []string{
		p.muted + "|Thinking",
		p.muted + "|Weighing two approaches",
		p.cyan + "|" + m.icon("tool") + "  Read files  ·  pending",
		p.text + "|3 files",
		p.cyan + "|" + m.icon("tool") + "  Run tests  ·  running",
		p.text + "|go test",
		p.cyan + "|" + m.icon("tool") + "  Patch  ·  completed",
		p.text + "|1 edit",
		p.cyan + "|" + m.icon("mcp") + "  Lookup  ·  failed",
		p.text + "|timeout",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("activity rows:\n%s", strings.Join(rows, "\n"))
	}
}

func TestStopReasonShownOnceAfterTheTurn(t *testing.T) {
	m := acpModel()
	thread := m.snapshot.Threads[0]
	thread.State, thread.NeedsResume = "idle", false
	for _, tc := range []struct{ reason, want string }{
		{"end_turn", ""},
		{"max_tokens", "Turn ended · the agent reached its output token limit"},
		{"max_turn_requests", "Turn ended · the agent reached its request limit for this turn"},
		{"refusal", "Turn ended · the agent refused this request"},
		{"cancelled", "Turn ended · cancelled"},
		{"weird_reason", "Turn ended · weird_reason"},
	} {
		thread.StopReason = tc.reason
		var got []string
		for _, line := range m.transcriptLines(thread, 70) {
			if strings.HasPrefix(line.text, "Turn ended") {
				got = append(got, line.text)
			}
		}
		if strings.Join(got, "|") != tc.want {
			t.Fatalf("%s: %q", tc.reason, got)
		}
	}
	running := m.snapshot.Threads[0]
	running.State, running.StopReason = "running", "max_tokens"
	for _, line := range m.transcriptLines(running, 70) {
		if strings.HasPrefix(line.text, "Turn ended") {
			t.Fatal("unfinished turn reported an outcome")
		}
	}
}

func TestApprovalCardShowsAgentChoices(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID, thread.State = "claude", "waiting"
	thread.Requests = []protocol.Request{{
		ID: "perm-1", Kind: "approval", Mode: "blocking", State: "pending", Revision: 4,
		Title: "Write src/main.go", Detail: "Tool: write · Location: /work/alpha/src/main.go", Origin: "claude",
		Choices: []string{"Reject", "Allow once", "Allow always"}, ChoiceIDs: []string{"reject", "allow", "allow_always"},
		Delivery: "acp-delivered",
	}}
	m.selectRequest(0)
	f := m.render()
	view := ansi.Strip(m.View().Content)
	for i, choice := range thread.Requests[0].Choices {
		h := controlHit(t, f, "approve:"+string(rune('0'+i)))
		if h.Rect.W < len(choice) {
			t.Fatalf("choice %q has no button", choice)
		}
	}
	if !strings.Contains(view, "Tool: write · Location: /work/alpha/src/main.go") {
		t.Fatalf("approval detail missing:\n%s", view)
	}
	if !strings.Contains(view, "Claude · Approval required") {
		t.Fatalf("approval origin missing:\n%s", view)
	}
	m.activate(action{Kind: "approve", ID: "perm-1", Value: "Allow once", Revision: 4})
	if m.busy == nil || m.busy.Kind != "request.answer" || m.busy.TargetID != "perm-1" || len(m.busy.Answers) != 1 || m.busy.Answers[0] != "Allow once" {
		t.Fatalf("approval answer: %+v", m.busy)
	}
	if deliveryConfirmed("acp-delivered") || !deliveryConfirmed("fixture-confirmed") || deliveryConfirmed("revalidation-required") {
		t.Fatal("delivery confirmation is not agent-agnostic")
	}
}

func TestAcpThreadKeepsSurfacesAndResumePath(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID, thread.SessionID, thread.Children = "claude", "sess-1", nil
	thread.Plan = []protocol.PlanStep{{Title: "Read", State: "completed"}, {Title: "Edit", State: "active"}, {Title: "Verify", State: "pending"}}
	if s := agentSummary(*thread); s.Label != "" {
		t.Fatal("empty children produced an Agents summary")
	}
	if got := planSummary(*thread).Label; got != "Plan 1/3" {
		t.Fatalf("plan summary: %q", got)
	}
	m.viewState().DetailID = ""
	for _, surface := range []struct{ kind, want string }{
		{"agents", "Child history unavailable"},
		{"plan", "✓ Read"},
		{"activity", "Inspect layout contract · completed"},
	} {
		m.openSurface(surface.kind, "")
		active, _ := m.viewState().Host.Active()
		if got := m.surfaceText(active); !strings.Contains(got, surface.want) {
			t.Fatalf("%s surface: %q", surface.kind, got)
		}
	}
	thread.State, thread.NeedsResume, thread.StopReason = "interrupted", true, "cancelled"
	m.configureInputs()
	f := m.measure()
	if !hasControl(f, "resume") {
		t.Fatal("interrupted ACP thread lost the Resume control")
	}
}

func TestLiveSessionOptionsSupersedeProbedCatalogue(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID, thread.State, thread.NeedsResume = "claude", "idle", false
	// The live catalogue replaces the probe's: a model value disappears and a
	// speed option the probe never saw becomes selectable.
	thread.Options = []protocol.ConfigOption{
		{ID: "model", Name: "Model", Category: "model", Type: "select", Current: "opus", Values: []protocol.ConfigValue{{Value: "opus", Name: "Opus"}}},
		{ID: "effort", Name: "Effort", Category: "thought_level", Type: "select", Current: "high", Values: []protocol.ConfigValue{{Value: "high", Name: "High"}}},
		{ID: "fast-mode", Name: "Fast mode", Category: "model_config", Type: "boolean", Current: "true", Values: []protocol.ConfigValue{{Value: "true", Name: "Fast"}, {Value: "false", Name: "Standard"}}},
	}
	v := m.state.Threads[thread.ID]
	v.Settings = protocol.Settings{Model: "opus", Effort: "high", Permissions: "ask", Speed: "true"}
	m.activate(action{Kind: "settings", Value: "model"})
	if got := menuLabels(m); strings.Join(got, "|") != "(*) Opus" {
		t.Fatalf("model menu ignored the live catalogue: %q", got)
	}
	m.activate(action{Kind: "settings", Value: "speed"})
	if got := menuLabels(m); strings.Join(got, "|") != "(*) Fast|( ) Standard" {
		t.Fatalf("speed menu ignored the live catalogue: %q", got)
	}
	// Permissions vanished from the live catalogue, so its retained value is no
	// longer validated and the field offers nothing.
	m.activate(action{Kind: "settings", Value: "permissions"})
	if got := menuLabels(m); strings.Join(got, "|") != "Agent default · Claude offers no permissions option" {
		t.Fatalf("permissions menu: %q", got)
	}
	m.activate(action{Kind: "menu-close"})
	m.prompt.SetValue("prompt")
	if got := m.sendBlocked(); got != "" {
		t.Fatalf("live catalogue rejected its own values: %q", got)
	}
	v.Settings.Model = "sonnet"
	if got := m.sendBlocked(); got != "sonnet is no longer an offered model for Claude" {
		t.Fatalf("withdrawn model accepted: %q", got)
	}
	if label := m.composerSettingDisplays(*thread, protocol.Settings{Model: "opus", Speed: "true"})[1].label; label != "Opus" {
		t.Fatalf("composer label ignored the live value name: %q", label)
	}
	v.Settings.Model = "opus"
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Settings == nil || m.busy.Settings.Permissions != "unavailable" || m.busy.Settings.Context != "unavailable" || m.busy.Settings.Speed != "true" {
		t.Fatalf("Send captured withdrawn fields or lost live choices: %+v", m.busy)
	}
	if v.Settings.Permissions != "ask" {
		t.Fatal("capture overwrote the saved preference")
	}
}

func TestUsageTelemetryDrivesGaugeAndSummary(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID = "claude"
	if got := m.usageLines()[0]; got != "Context used: unavailable" {
		t.Fatalf("missing telemetry invented a measurement: %q", got)
	}
	thread.Usage = &protocol.Usage{Used: 48000, Size: 200000, Source: "claude-agent-acp", ReportedAt: "2026-09-22T10:00:00Z"}
	want := []string{
		"Context used: 48000 tokens",
		"Context capacity: 200000 tokens",
		"Context percentage: 24.0%",
		"Source: claude-agent-acp",
		"Observed: 2026-09-22T10:00:00Z",
		"API cost: unavailable",
		"Billing mode: unknown",
		"Subscription limits: unavailable",
	}
	if got := m.usageLines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("usage summary: %q", got)
	}
	gauge, help := m.usageGauge()
	if gauge != "Ctx 24%" || !strings.Contains(help, "48k / 200k tokens") {
		t.Fatalf("usage gauge: %q / %q", gauge, help)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "24%") {
		t.Fatal("composer gauge does not show reported occupancy")
	}
	// An incoherent report stays unavailable instead of being repaired.
	thread.Usage = &protocol.Usage{Used: 10, Size: 0, Source: "claude-agent-acp"}
	if got := m.usageLines()[2]; got != "Context percentage: unavailable" {
		t.Fatalf("incoherent telemetry was repaired: %q", got)
	}
}

func TestApprovalCardScrollsLongDetailAndKeepsChoices(t *testing.T) {
	m := acpModel()
	thread := &m.snapshot.Threads[0]
	thread.AgentID, thread.State = "claude", "waiting"
	var diff []string
	for i := 0; i < 40; i++ {
		diff = append(diff, "+ added line of the proposed patch")
	}
	thread.Requests = []protocol.Request{{
		ID: "perm-2", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1,
		Title: "Edit src/main.go", Detail: strings.Join(diff, "\n"), Origin: "claude",
		// Names and IDs as recorded from the Claude adapter probe.
		Choices: []string{"Yes", "Yes, allow all edits during this session", "No"}, ChoiceIDs: []string{"allow-once", "allow-with-updates", "reject"},
	}}
	m.selectRequest(0)
	f := m.render()
	if f.requestMax <= 0 {
		t.Fatal("long approval detail does not scroll")
	}
	for i := range thread.Requests[0].Choices {
		controlHit(t, f, "approve:"+string(rune('0'+i)))
	}
	if !hasControl(f, "scrollbar-request") || !hasControl(f, "prompt") {
		t.Fatal("scrolling approval card hid the scrollbar or the composer")
	}
}

func TestFixtureModelRemainsSelectableWithACPAgents(t *testing.T) {
	m := acpModel()
	m.beginThreadDraft("alpha")
	m.activate(action{Kind: "settings", Value: "agent"})
	if len(m.menu) < 3 || m.menu[1].Action.Value != "claude" || m.menu[2].Action.Value != "codex" {
		t.Fatal("fixture draft lost the dynamic agent menu")
	}
	m.activate(action{Kind: "settings", Value: "model"})
	if len(m.menu) != 1 || m.menu[0].Label != "Reference · Demo model" || m.menu[0].Action.Kind != "setting-model" {
		t.Fatalf("fixture model is not selectable: %+v", m.menu)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewState().Settings.Model != "fixture-model" || len(m.menu) != 0 {
		t.Fatal("selecting Reference did not set the model and dismiss the menu")
	}
	m.activate(action{Kind: "settings", Value: "effort"})
	if len(m.menu) != 3 || m.menu[0].Action.Kind != "setting" {
		t.Fatalf("fixture effort choices unavailable: %+v", m.menu)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.PasteMsg{Content: "fixture prompt"})
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Kind != "thread.start" || m.busy.Agent != "fixture" || m.busy.Settings == nil || m.busy.Settings.Model != "fixture-model" || m.busy.Settings.Effort != "low" {
		t.Fatalf("fixture Send failed: %+v; reason %q", m.busy, m.sendBlocked())
	}
}
