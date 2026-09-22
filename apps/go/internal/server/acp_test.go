package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// waitTurn waits for a dispatched ACP turn to finish. A newly started thread is
// idle with a queued prompt until the runner picks it up, so an idle state
// alone does not mean a turn has run.
func waitTurn(t *testing.T, e *engine, id, turnID string) protocol.Thread {
	t.Helper()
	s := waitFor(t, e, "turn "+turnID, func(s protocol.Snapshot) bool {
		thread := threadOf(s, id)
		return thread.TurnID == turnID && thread.State != "running" && thread.State != "waiting" && len(thread.Queue) == 0
	})
	return threadOf(s, id)
}

func startACPThread(t *testing.T, e *engine, id, text string) string {
	t.Helper()
	settings := fakeSettings()
	c := protocol.Command{Version: 1, ID: id, Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: text, Settings: &settings}
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	return receipt.TargetID
}

func TestProbeRecordsOptionsAndFields(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	record := agent.Find(&e.snap, "claude")
	record.State, record.Options, record.Fields = agent.StateUnprobed, nil, protocol.SettingFields{}
	if _, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "claude"}); err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "probe completion", func(s protocol.Snapshot) bool {
		return agent.Find(&s, "claude").State != agent.StateProbing
	})
	probed := agent.Find(&s, "claude")
	if probed.State != agent.StateReady {
		t.Fatalf("probe state %q: %s", probed.State, probed.Detail)
	}
	if probed.Version != "fake-acp 0.0.1" || probed.ProbedAt == "" {
		t.Fatalf("probe identity missing: %+v", probed)
	}
	want := protocol.SettingFields{Model: "model", Effort: "effort", Permissions: "mode"}
	if probed.Fields != want {
		t.Fatalf("fields %+v", probed.Fields)
	}
	if len(probed.Options) != 3 || probed.Options[0].Values[0].Value != "reference" {
		t.Fatalf("options %+v", probed.Options)
	}
	if !reflect.DeepEqual(probed.Capabilities, []string{agent.CapLoadSession, agent.CapEmbeddedPrompt, agent.CapSessionClose}) {
		t.Fatalf("capabilities %v", probed.Capabilities)
	}
	// The provisional session is closed and its process ended afterwards.
	if _, _, closes, sessions := fleet.last().snapshot(); closes != 1 || sessions != 1 {
		t.Fatalf("provisional session not closed: closes=%d sessions=%d", closes, sessions)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "probe-fixture", Kind: "agent.probe", TargetID: "fixture"}); err == nil {
		t.Fatal("fixture probe accepted")
	}
}

func TestProbeReportsUnauthenticatedAndUnavailable(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		e, fleet, _ := acpEngine(t)
		fleet.authRequired = true
		agent.Find(&e.snap, "claude").State = agent.StateUnprobed
		if _, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "claude"}); err != nil {
			t.Fatal(err)
		}
		s := waitFor(t, e, "probe completion", func(s protocol.Snapshot) bool {
			return agent.Find(&s, "claude").State != agent.StateProbing
		})
		probed := agent.Find(&s, "claude")
		if probed.State != agent.StateUnauthenticated || !strings.Contains(probed.Detail, "Adapter CLI login") {
			t.Fatalf("unauthenticated probe: %q %q", probed.State, probed.Detail)
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		e, _, _ := acpEngine(t)
		// Exercise the real launcher so a missing executable is a real PATH
		// lookup failure rather than a fake.
		e.launch = nil
		record := agent.Find(&e.snap, "codex")
		record.Command = "tui-go-agent-that-does-not-exist"
		if _, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "codex"}); err != nil {
			t.Fatal(err)
		}
		s := waitFor(t, e, "probe completion", func(s protocol.Snapshot) bool {
			return agent.Find(&s, "codex").State != agent.StateProbing
		})
		probed := agent.Find(&s, "codex")
		if probed.State != agent.StateUnavailable || !strings.Contains(probed.Detail, "PATH") {
			t.Fatalf("unavailable probe: %q %q", probed.State, probed.Detail)
		}
	})
}

func TestProbeCoalescesWithOneAlreadyRunning(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	e.mu.Lock()
	agent.Find(&e.snap, "claude").State = agent.StateProbing
	e.probes["claude"] = true
	before := clone(e.snap)
	e.mu.Unlock()
	receipt, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "claude"})
	if err != nil || receipt.TargetID != "claude" {
		t.Fatalf("refresh during an in-flight probe: %+v %v", receipt, err)
	}
	if agent.Find(&e.snap, "claude").Revision != agent.Find(&before, "claude").Revision {
		t.Fatal("coalesced probe restarted the agent record")
	}
	if fleet.count() != 0 {
		t.Fatalf("coalesced probe launched a second process: %d", fleet.count())
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "probe-missing", Kind: "agent.probe", TargetID: "gemini"}); err == nil {
		t.Fatal("probe of an unconfigured agent accepted")
	}
}

func TestStartupProbesEveryConfiguredAgent(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	e.mu.Lock()
	for i := range e.snap.Agents {
		if e.snap.Agents[i].Kind == agent.KindACP {
			e.snap.Agents[i].State, e.snap.Agents[i].Options = agent.StateUnprobed, nil
		}
	}
	e.mu.Unlock()
	e.probeConfiguredAgents()
	s := waitFor(t, e, "startup probes", func(s protocol.Snapshot) bool {
		for _, a := range s.Agents {
			if a.Kind == agent.KindACP && a.State == agent.StateProbing {
				return false
			}
		}
		return true
	})
	for _, a := range s.Agents {
		if a.Kind != agent.KindACP {
			continue
		}
		if a.State != agent.StateReady || len(a.Options) != 3 {
			t.Fatalf("startup probe of %s: %q %s", a.ID, a.State, a.Detail)
		}
	}
	if fleet.count() != 2 {
		t.Fatalf("expected one process per configured agent, got %d", fleet.count())
	}
}

func TestMissingExecutableReportsTheInstallHint(t *testing.T) {
	e, _, _ := acpEngine(t)
	e.launch = nil
	e.mu.Lock()
	record := agent.Find(&e.snap, "claude")
	record.Command, record.State = "tui-go-agent-that-does-not-exist", agent.StateUnprobed
	e.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "claude"}); err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "probe completion", func(s protocol.Snapshot) bool {
		return agent.Find(&s, "claude").State != agent.StateProbing
	})
	probed := agent.Find(&s, "claude")
	if probed.State != agent.StateUnavailable || !strings.Contains(probed.Detail, "installed official claude or codex CLI") {
		t.Fatalf("install hint missing: %q %q", probed.State, probed.Detail)
	}
}

func TestThreadStartValidatesAgentAndSettings(t *testing.T) {
	for name, change := range map[string]func(*protocol.Command){
		"unknown agent":    func(c *protocol.Command) { c.Agent = "gemini" },
		"unknown value":    func(c *protocol.Command) { c.Settings.Model = "reference-turbo" },
		"display name":     func(c *protocol.Command) { c.Settings.Model = "Reference model" },
		"unmapped context": func(c *protocol.Command) { c.Settings.Context = "1m" },
		"unmapped speed":   func(c *protocol.Command) { c.Settings.Speed = "fast" },
		"fixture settings": func(c *protocol.Command) { c.Settings.Model = "fixture-model" },
	} {
		t.Run(name, func(t *testing.T) {
			e, _, _ := acpEngine(t)
			settings := fakeSettings()
			c := protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "hello", Settings: &settings}
			change(&c)
			before := clone(e.snap)
			if _, err := e.command(c); err == nil {
				t.Fatal("invalid ACP start accepted")
			}
			if !reflect.DeepEqual(before, e.snap) {
				t.Fatal("rejected start changed state")
			}
		})
	}
	t.Run("agent not ready", func(t *testing.T) {
		e, _, _ := acpEngine(t)
		agent.Find(&e.snap, "claude").State = agent.StateUnprobed
		settings := fakeSettings()
		if _, err := e.command(protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "hello", Settings: &settings}); err == nil {
			t.Fatal("unprobed agent accepted")
		}
	})
	t.Run("fixture keeps working", func(t *testing.T) {
		e, _, _ := acpEngine(t)
		settings := protocol.Settings{Model: "fixture-model", Effort: "high", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
		for _, name := range []string{"Fixture agent", "fixture"} {
			c := protocol.Command{Version: 1, ID: "start-" + name, Kind: "thread.start", ProjectID: e.snap.Projects[0].ID, Agent: name, Text: "hello", Settings: &settings}
			receipt, err := e.command(c)
			if err != nil {
				t.Fatal(name, err)
			}
			created := threadOf(e.current(), receipt.TargetID)
			if created.AgentID != "" || created.Agent != "Fixture agent" || created.State != "running" {
				t.Fatalf("fixture thread changed: %+v", created)
			}
		}
	})
}

func TestDispatchAppliesSettingsAndStreamsOneActivity(t *testing.T) {
	e, fleet, checkout := acpEngine(t)
	if err := os.WriteFile(filepath.Join(checkout, "notes.md"), []byte("captured content"), 0600); err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings()
	receipt, err := e.command(protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "say pong", Settings: &settings, Attachments: []protocol.Attachment{{Kind: "workspace-file", Source: "notes.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.TargetID
	thread := waitTurn(t, e, id, "prompt-start")
	if thread.AgentID != "claude" || thread.Agent != "Claude" || thread.SessionID != "session-fake" {
		t.Fatalf("agent identity: %+v", thread)
	}
	if thread.StopReason != "end_turn" || thread.Error != "" || len(thread.Queue) != 0 {
		t.Fatalf("turn outcome: %+v", thread)
	}
	// The agent's own reported option state becomes the effective settings.
	if thread.Effective != (protocol.Settings{Model: "reference", Effort: "low", Permissions: "auto", Context: agent.Unavailable, Speed: agent.Unavailable}) {
		t.Fatalf("effective settings: %+v", thread.Effective)
	}
	if len(thread.Options) != 3 {
		t.Fatalf("live option catalogue missing: %+v", thread.Options)
	}
	if user := activityOf(thread, thread.TurnID); user.State != "completed" || user.Prompt == nil || user.Prompt.Attachments[0].Content != "captured content" {
		t.Fatalf("user activity: %+v", user)
	}
	// Two streamed chunks coalesce into one agent activity.
	reply := activityOf(thread, "agent-"+thread.TurnID)
	if reply.Text != "pong" || reply.State != "completed" || reply.Role != "agent" {
		t.Fatalf("reply activity: %+v", reply)
	}
	if tool := activityOf(thread, "tool-call-echo"); tool.State != "completed" || tool.Title != "Echo" {
		t.Fatalf("tool activity: %+v", tool)
	}
	if len(thread.Plan) != 2 || thread.Plan[1].State != "active" {
		t.Fatalf("plan: %+v", thread.Plan)
	}
	if thread.Usage == nil || thread.Usage.Used != 42 || thread.Usage.Size != 1000 || thread.Usage.Source != "usage_update" {
		t.Fatalf("usage telemetry: %+v", thread.Usage)
	}
	if thread.Title != "Echoed thread" {
		t.Fatalf("session_info_update title ignored: %q", thread.Title)
	}
	if unknown := activityOf(thread, "update-invented_future_kind"); !strings.Contains(unknown.Detail, "retained") {
		t.Fatalf("unknown update dropped: %+v", unknown)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "say pong") || !strings.Contains(prompts[0], "resource:file://") {
		t.Fatalf("prompt blocks: %q", prompts)
	}
	// The fixture agent's synthetic tick must not touch an ACP thread.
	before := threadOf(e.current(), id)
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if after := threadOf(e.current(), id); after.Tick != before.Tick || after.State != before.State {
		t.Fatal("fixture tick advanced an ACP thread")
	}
}

func TestPermissionRequestResumesTheTurn(t *testing.T) {
	e, _, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "ask permission to write")
	s := waitFor(t, e, "approval card", func(s protocol.Snapshot) bool {
		thread := threadOf(s, id)
		return len(thread.Requests) == 1 && thread.State == "waiting"
	})
	request := threadOf(s, id).Requests[0]
	if request.Kind != "approval" || !reflect.DeepEqual(request.Choices, []string{"Allow once", "Reject"}) || !reflect.DeepEqual(request.ChoiceIDs, []string{"allow-once", "reject"}) {
		t.Fatalf("approval card: %+v", request)
	}
	if !strings.Contains(request.Detail, "notes.md") || !strings.Contains(request.Detail, "Diff for notes.md") {
		t.Fatalf("approval detail missing the proposed change: %q", request.Detail)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "answer", Kind: "request.answer", ThreadID: id, TargetID: request.ID, Revision: request.Revision, Answers: []string{"Allow once"}}); err != nil {
		t.Fatal(err)
	}
	thread := waitTurn(t, e, id, "prompt-start")
	if thread.Requests[0].State != "closed" || thread.Requests[0].Delivery != "acp-unconfirmed" {
		t.Fatalf("delivery: %+v", thread.Requests[0])
	}
	if reply := activityOf(thread, "agent-"+thread.TurnID); reply.Text != "decision allow-once" {
		t.Fatalf("answer not delivered to the agent: %+v", reply)
	}
	if tool := activityOf(thread, "tool-call-1"); tool.State != "completed" {
		t.Fatalf("tool call not settled: %+v", tool)
	}
}

func TestInterruptCancelsAndResumeDoesNotResend(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "cancel me please")
	waitFor(t, e, "streaming to start", func(s protocol.Snapshot) bool {
		return activityOf(threadOf(s, id), "agent-prompt-start").Text == "counting"
	})
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: id}); err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "cancelled turn", func(s protocol.Snapshot) bool {
		return threadOf(s, id).StopReason == "cancelled"
	})
	thread := threadOf(s, id)
	if thread.State != "interrupted" || !thread.NeedsResume {
		t.Fatalf("interrupt outcome: %+v", thread)
	}
	// Updates that arrived between the cancel notification and the response are
	// still recorded.
	if thread.Usage == nil || thread.Usage.Used != 12 {
		t.Fatalf("trailing update lost: %+v", thread.Usage)
	}
	// Queue a follow-up and resume: Resume must dispatch only the queued prompt.
	settings := fakeSettings()
	if _, err := e.command(protocol.Command{Version: 1, ID: "queued", Kind: "prompt.send", ThreadID: id, Text: "second prompt", Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: id}); err != nil {
		t.Fatal(err)
	}
	if resumed := waitTurn(t, e, id, "prompt-queued"); resumed.NeedsResume || len(resumed.Queue) != 0 {
		t.Fatalf("resume outcome: %+v", resumed)
	}
	prompts, _, _, _ := fleet.last().snapshot()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "second prompt") {
		t.Fatalf("resume resent or lost a prompt: %q", prompts)
	}
}

func TestRestartInterruptsACPWorkWithoutResubmission(t *testing.T) {
	e, _, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "cancel me please")
	waitFor(t, e, "running turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "running" })
	e.mu.Lock()
	restored := clone(e.snap)
	e.mu.Unlock()
	// A pending approval belonged to the dead process; recovery must close it.
	for i := range restored.Threads {
		if restored.Threads[i].ID == id {
			restored.Threads[i].Requests = append(restored.Threads[i].Requests, protocol.Request{ID: "approval-stale", Kind: "approval", Mode: "blocking", State: "pending", Revision: 1, Choices: []string{"Allow once"}, ChoiceIDs: []string{"allow-once"}})
		}
	}
	restored.AppSettings.ContinueAfterRestart = true
	recoverThreads(&restored)
	thread := threadOf(restored, id)
	if thread.State != "interrupted" || !thread.NeedsResume || thread.RestartEligible {
		t.Fatalf("ACP restart recovery: %+v", thread)
	}
	if len(thread.Queue) != 0 {
		t.Fatalf("restart resubmitted work: %+v", thread.Queue)
	}
	if thread.Requests[0].State != "closed" || thread.Requests[0].Delivery != "acp-undeliverable" {
		t.Fatalf("stale approval left waiting: %+v", thread.Requests[0])
	}
	if activityOf(thread, "agent-"+thread.TurnID).State == "running" {
		t.Fatal("streamed activity still claims to be running after restart")
	}
}

func TestDispatchFailureKeepsThePromptQueued(t *testing.T) {
	t.Run("rejected setting", func(t *testing.T) {
		e, fleet, _ := acpEngine(t)
		fleet.rejectValue = "reference"
		id := startACPThread(t, e, "start", "say pong")
		s := waitFor(t, e, "failed dispatch", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
		thread := threadOf(s, id)
		if len(thread.Queue) != 1 || thread.Queue[0].ID != "prompt-start" {
			t.Fatalf("prompt not retained at the queue head: %+v", thread.Queue)
		}
		if !strings.Contains(thread.Error, "not selectable") {
			t.Fatalf("failure text: %q", thread.Error)
		}
		prompts, _, _, _ := fleet.last().snapshot()
		if len(prompts) != 0 {
			t.Fatalf("a prompt was sent despite the rejected setting: %q", prompts)
		}
		// A new Send clears the failure and retries the queue head.
		fleet.rejectValue = ""
		settings := fakeSettings()
		if _, err := e.command(protocol.Command{Version: 1, ID: "retry", Kind: "prompt.send", ThreadID: id, Text: "say pong again", Settings: &settings}); err != nil {
			t.Fatal(err)
		}
		waitTurn(t, e, id, "prompt-retry")
	})
	t.Run("failed turn", func(t *testing.T) {
		e, _, _ := acpEngine(t)
		id := startACPThread(t, e, "start", "boom please")
		s := waitFor(t, e, "failed turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "failed" })
		thread := threadOf(s, id)
		capture := activityOf(thread, "prompt-start").Prompt
		if len(thread.Queue) != 0 || capture == nil || capture.Text != "boom please" || !thread.NeedsResume {
			t.Fatalf("failed turn lost capture or queued a replay: %+v", thread)
		}
		if !strings.Contains(thread.Error, "the fake agent failed this turn") {
			t.Fatalf("failure text: %q", thread.Error)
		}
	})
}

func TestThreadDeleteEndsTheAgentSession(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "say pong")
	thread := waitTurn(t, e, id, "prompt-start")
	if _, err := e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: id, Revision: thread.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "session close", func(protocol.Snapshot) bool {
		_, _, closes, _ := fleet.last().snapshot()
		return closes == 1
	})
	e.mu.Lock()
	remaining := len(e.runs)
	e.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("run retained after delete: %d", remaining)
	}
}

func TestSteeringStaysUnavailableForACPThreads(t *testing.T) {
	e, _, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "cancel me please")
	waitFor(t, e, "running turn", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "running" })
	settings := fakeSettings()
	if _, err := e.command(protocol.Command{Version: 1, ID: "queued", Kind: "prompt.send", ThreadID: id, Text: "steer me", Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	thread := threadOf(e.current(), id)
	c := protocol.Command{Version: 1, ID: "steer", Kind: "queue.steer", ThreadID: id, TargetID: thread.Queue[0].ID, Revision: thread.QueueRevision, ExpectedTurnID: thread.TurnID}
	_, err := e.command(c)
	if err == nil || !strings.Contains(err.Error(), "Steering is unavailable for this agent") {
		t.Fatalf("ACP steering was not refused honestly: %v", err)
	}
}

func TestSessionLoadRestoresAfterProcessLoss(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "say pong")
	waitTurn(t, e, id, "prompt-start")
	// Drop the connection the way a dead adapter would.
	e.mu.Lock()
	run := e.runs[id]
	e.mu.Unlock()
	run.mu.Lock()
	session := run.session
	run.mu.Unlock()
	fleet.last().stop()
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("fake connection did not end")
	}
	settings := fakeSettings()
	if _, err := e.command(protocol.Command{Version: 1, ID: "again", Kind: "prompt.send", ThreadID: id, Text: "say pong again", Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, id, "prompt-again")
	if fleet.count() != 2 {
		t.Fatalf("expected a second connection, got %d", fleet.count())
	}
	if _, loads, _, sessions := fleet.last().snapshot(); loads != 1 || sessions != 0 {
		t.Fatalf("session/load was not used: loads=%d sessions=%d", loads, sessions)
	}
	if notice := activityOf(threadOf(e.current(), id), "session-reset-session-fake"); notice.ID != "" {
		t.Fatal("a restored session must not claim upstream context was lost")
	}
}

func TestServePublishesAgentsAndProbesThemOnStart(t *testing.T) {
	// The configured executables do not exist here, so the startup probe must
	// report unavailable with the install hint rather than hang or fail Serve.
	t.Setenv(agent.EnvClaudeCommand, "tui-go-agent-that-does-not-exist")
	t.Setenv(agent.EnvCodexCommand, "tui-go-agent-that-does-not-exist-either")
	home := t.TempDir()
	c, stop := startTestServer(t, home)
	defer stop()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	var snap protocol.Snapshot
	for {
		var err error
		snap, err = c.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		settled := len(snap.Agents) == 3
		for _, a := range snap.Agents {
			if a.State == agent.StateProbing {
				settled = false
			}
		}
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup probes did not settle: %+v", snap.Agents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, capability := range []string{"acp-agents", "agent-probe", "acp-permissions", "acp-cancel"} {
		if !slices.Contains(snap.Capabilities, capability) {
			t.Fatalf("capability %q not advertised: %v", capability, snap.Capabilities)
		}
	}
	for _, a := range snap.Agents {
		switch a.Kind {
		case agent.KindFixture:
			if a.State != agent.StateReady {
				t.Fatalf("fixture agent: %+v", a)
			}
		case agent.KindACP:
			if a.State != agent.StateUnavailable || !strings.Contains(a.Detail, agent.InstallHint) {
				t.Fatalf("startup probe of %s: %q %q", a.ID, a.State, a.Detail)
			}
			if !strings.HasPrefix(a.Command, "tui-go-agent-that-does-not-exist") {
				t.Fatalf("environment override ignored: %+v", a)
			}
		}
	}
}

// Session initialization can take seconds. Commands accepted while it runs must
// win over the stale queue capture held by the dispatcher.
func TestACPDispatchRechecksQueueAfterSessionSetup(t *testing.T) {
	for _, kind := range []string{"queue.remove", "queue.edit", "queue.reorder"} {
		t.Run(kind, func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			entered, release := make(chan struct{}), make(chan struct{})
			e.launch = func(ctx context.Context, o agent.Options) (*agent.Session, error) {
				session, err := fleet.launch(ctx, o)
				close(entered)
				<-release
				return session, err
			}
			id := startACPThread(t, e, "start", "original")
			<-entered
			settings := fakeSettings()
			if _, err := e.command(protocol.Command{Version: 1, ID: "second", Kind: "prompt.send", ThreadID: id, Text: "second", Settings: &settings}); err != nil {
				t.Fatal(err)
			}
			thread := threadOf(e.current(), id)
			c := protocol.Command{Version: 1, ID: "change", Kind: kind, ThreadID: id, TargetID: "prompt-start", Revision: thread.QueueRevision, Text: "edited", Order: []string{"prompt-second", "prompt-start"}}
			if _, err := e.command(c); err != nil {
				t.Fatal(err)
			}
			close(release)
			last := "prompt-second"
			want := []string{"second"}
			switch kind {
			case "queue.edit":
				want = []string{"edited", "second"}
			case "queue.reorder":
				last, want = "prompt-start", []string{"second", "original"}
			}
			waitTurn(t, e, id, last)
			got, _, _, _ := fleet.last().snapshot()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("dispatched stale queue capture: got %q, want %q", got, want)
			}
		})
	}
}

func TestACPCancelWaitsForPeerResponse(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "cancel me")
	waitFor(t, e, "streaming", func(s protocol.Snapshot) bool {
		return activityOf(threadOf(s, id), "agent-prompt-start").Text == "counting"
	})
	peer := fleet.last()
	release := make(chan struct{})
	defer close(release)
	peer.mu.Lock()
	peer.cancelRelease = release
	peer.mu.Unlock()
	e.mu.Lock()
	run := e.runs[id]
	e.mu.Unlock()
	run.mu.Lock()
	done := run.turnDone
	run.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("local cancellation was treated as the peer's stop response")
	case <-time.After(100 * time.Millisecond):
	}
	if thread := threadOf(e.current(), id); thread.StopReason != "" {
		t.Fatalf("unconfirmed stop reason: %q", thread.StopReason)
	}
}

func TestACPResumeRejectsLiveApproval(t *testing.T) {
	e, _, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "ask permission")
	s := waitFor(t, e, "approval", func(s protocol.Snapshot) bool { return threadOf(s, id).State == "waiting" })
	before := threadOf(s, id)
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: id}); err == nil {
		t.Fatal("resume accepted for live approval")
	}
	after := threadOf(e.current(), id)
	if after.State != "waiting" || !reflect.DeepEqual(after.Requests, before.Requests) {
		t.Fatal("resume changed the live request")
	}
}

func TestOnDemandProbeIsJoinedAtShutdown(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	entered, release := make(chan struct{}), make(chan struct{})
	e.launch = func(ctx context.Context, o agent.Options) (*agent.Session, error) {
		session, err := fleet.launch(ctx, o)
		close(entered)
		<-release
		return session, err
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "probe", Kind: "agent.probe", TargetID: "claude"}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { e.stopAgents(); close(done) }()
	select {
	case <-done:
		close(release)
		t.Fatal("shutdown did not join the on-demand probe")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after the probe")
	}
}

func TestACPCommandsValidateLiveCatalogue(t *testing.T) {
	e, _, _ := acpEngine(t)
	id := startACPThread(t, e, "start", "pong")
	waitTurn(t, e, id, "prompt-start")
	snapshot := e.current()
	thread := threadByID(&snapshot, id)
	thread.Options = fakeOptions(t, nil)
	thread.Options[0].Values = append(thread.Options[0].Values, protocol.ConfigValue{Value: "live-model", Name: "Live model"})
	settings := fakeSettings()
	settings.Model = "live-model"
	if _, err := apply(&snapshot, protocol.Command{Version: 1, ID: "live", Kind: "prompt.send", ThreadID: id, Text: "live selection", Settings: &settings}); err != nil {
		t.Fatalf("live option rejected against stale probe: %v", err)
	}
	if threadByID(&snapshot, id).Queue[0].Settings.Model != "live-model" {
		t.Fatal("live selection not captured")
	}
}

func TestStaleACPSetupFailureDoesNotPoisonChangedQueue(t *testing.T) {
	for _, kind := range []string{"queue.remove", "queue.edit"} {
		t.Run(kind, func(t *testing.T) {
			e, fleet, _ := acpEngine(t)
			entered, release := make(chan struct{}), make(chan struct{})
			first := true
			e.launch = func(ctx context.Context, o agent.Options) (*agent.Session, error) {
				if first {
					first = false
					close(entered)
					<-release
					return nil, errors.New("stale setup failed")
				}
				return fleet.launch(ctx, o)
			}
			id := startACPThread(t, e, "start", "original")
			<-entered
			settings := fakeSettings()
			if _, err := e.command(protocol.Command{Version: 1, ID: "second", Kind: "prompt.send", ThreadID: id, Text: "second", Settings: &settings}); err != nil {
				t.Fatal(err)
			}
			thread := threadOf(e.current(), id)
			if _, err := e.command(protocol.Command{Version: 1, ID: "change", Kind: kind, ThreadID: id, TargetID: "prompt-start", Revision: thread.QueueRevision, Text: "edited"}); err != nil {
				t.Fatal(err)
			}
			close(release)
			thread = waitTurn(t, e, id, "prompt-second")
			if thread.Error != "" || thread.State != "idle" {
				t.Fatalf("stale failure poisoned replacement work: %+v", thread)
			}
			got, _, _, _ := fleet.last().snapshot()
			want := []string{"second"}
			if kind == "queue.edit" {
				want = []string{"edited", "second"}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
