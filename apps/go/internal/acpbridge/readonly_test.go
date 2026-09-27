package acpbridge

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

func modeValues(t *testing.T, options []acp.SessionConfigOption) []string {
	t.Helper()
	var out []string
	for _, option := range options {
		if option.Select != nil && option.Select.Id == "mode" && option.Select.Options.Ungrouped != nil {
			for _, v := range *option.Select.Options.Ungrouped {
				out = append(out, string(v.Value))
			}
		}
	}
	return out
}

// A read-only Claude bridge launches in plan mode with classifier review of
// planning commands off and bypass not enabled, offers only plan, and
// refuses every other mode; an ordinary bridge never accepts plan.
func TestClaudeReadOnlyBridgeLocksPlanMode(t *testing.T) {
	f := startClaudeFixtureWith(t, OpenOptions{ReadOnly: true})
	if got := modeValues(t, f.options); len(got) != 1 || got[0] != "plan" || selectedMode(f.options) != "plan" {
		t.Fatalf("read-only catalogue: %v current %q", got, selectedMode(f.options))
	}
	args := findFakeEntry(readFakeClaudeLog(t, f.logPath), "argv")
	if args == nil || !containsPair(args.Args, "--permission-mode", "plan") || !containsPair(args.Args, "--permission-prompt-tool", "stdio") {
		t.Fatalf("read-only launch: %+v", args)
	}
	var settings map[string]any
	for i := 0; i+1 < len(args.Args); i++ {
		if args.Args[i] == "--settings" {
			if err := json.Unmarshal([]byte(args.Args[i+1]), &settings); err != nil {
				t.Fatal(err)
			}
		}
	}
	if settings["fastMode"] != true || settings["useAutoModeDuringPlan"] != false || settings["disableAutoMode"] != "disable" || settings["plansDirectory"] != nil {
		t.Fatalf("read-only settings: %v", settings)
	}
	for _, arg := range args.Args {
		if arg == "--allow-dangerously-skip-permissions" || arg == "--dangerously-skip-permissions" {
			t.Fatalf("read-only launch enabled bypass: %q", arg)
		}
	}
	for _, mode := range []string{"default", "acceptEdits", "auto", "bypassPermissions"} {
		if _, err := f.setField(t, "mode", mode); err == nil {
			t.Fatalf("read-only bridge accepted %s", mode)
		}
	}
	if _, err := f.setField(t, "mode", "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	}
	plain := startClaudeFixture(t)
	if _, err := plain.setField(t, "mode", "plan"); err == nil {
		t.Fatal("an ordinary bridge accepted plan")
	}
	for _, v := range modeValues(t, plain.options) {
		if v == "plan" {
			t.Fatal("an ordinary bridge offered plan")
		}
	}
}

// Claude must confirm plan mode at startup, or the bridge does not open.
func TestClaudeReadOnlyBridgeRequiresConfirmedPlanMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test CLI shim is a Unix executable")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "claude")
	script := "#!/bin/sh\nexec \"$" + fakeTestBinary + "\" -test.run='^TestClaudeRuntimeHelper$' -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), fakeTestBinary+"="+os.Args[0], fakeClaudeRole+"=runtime", "TUI_GO_CLAUDE_TEST_INIT_MODE=default")
	endpoint, err := OpenWith(context.Background(), "claude", shim, root, env, io.Discard, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(endpoint.Stop)
	conn := acp.NewClientSideConnection(newClaudeTestClient(), endpoint.Input, endpoint.Output)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err == nil {
		t.Fatal("a runtime that did not confirm plan mode was opened read-only")
	}
}

// A read-only Codex bridge narrows the thread to the readOnly sandbox with
// approval policy never before advertising it, sends that policy with every
// turn, and refuses every other mode; an ordinary bridge refuses read-only.
func TestCodexReadOnlyBridgeLocksReadOnlySandbox(t *testing.T) {
	f := startCodexFixtureWith(t, "completed", OpenOptions{ReadOnly: true})
	if got := modeValues(t, f.options); len(got) != 1 || got[0] != "read-only" || selectedMode(f.options) != "read-only" {
		t.Fatalf("read-only catalogue: %v", got)
	}
	for _, mode := range []string{"supervised", "auto", "full"} {
		if err := f.setOption("mode", mode); err == nil {
			t.Fatalf("read-only bridge accepted %s", mode)
		}
	}
	f.selectModelAndEffort(t)
	if err := f.setOption("mode", "read-only"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.prompt(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range codexRuntimeLog(t, f.logPath) {
		if entry.Kind != "request" || (entry.Method != "thread/settings/update" && entry.Method != "turn/start") {
			continue
		}
		var params map[string]any
		if err := json.Unmarshal(entry.Params, &params); err != nil {
			t.Fatal(err)
		}
		if _, has := params["sandboxPolicy"]; !has {
			continue
		}
		sandbox, _ := params["sandboxPolicy"].(map[string]any)
		if params["approvalPolicy"] != "never" || sandbox["type"] != "readOnly" || sandbox["networkAccess"] != false {
			t.Fatalf("%s policy = %+v", entry.Method, params)
		}
		seen[entry.Method]++
	}
	if seen["thread/settings/update"] < 2 || seen["turn/start"] != 1 {
		t.Fatalf("read-only policy not applied at open and per turn: %v", seen)
	}
	plain := startCodexFixture(t, "completed")
	if err := plain.setOption("mode", "read-only"); err == nil {
		t.Fatal("an ordinary bridge accepted read-only")
	}
}
