package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Free-form server or tool text is never split into label/value pairs.
func TestActivityDetailFreeTextIsNeverAPair(t *testing.T) {
	m := testModel()
	a := protocol.Activity{ID: "t", Role: "tool", Title: "Run", State: "failed",
		Detail: "Note: something\nError: x\nResult: done\n{\"kind\":\"read\",\"status\":\"failed\"}"}
	for _, b := range activityBlocks(m, a) {
		if b.kind == surfacePairBlock {
			t.Fatalf("free text rendered as pair: %+v", b)
		}
	}
	got := detailBlocks(m, "Note: something\nError: x")
	if len(got) != 2 || got[0].kind != surfaceTextBlock || got[0].value != "Note: something" || got[1].value != "Error: x" {
		t.Fatalf("blocks = %+v", got)
	}
}

// TestActivityDetailCaptures writes Activity inspector review captures for a
// tool call, an MCP call and a failed tool in both themes.
func TestActivityDetailCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, id := range []string{"tool", "mcp-fixture", "failed-tool"} {
			m := testModel()
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			m.snapshot.Threads[0].Activity = append(m.snapshot.Threads[0].Activity, protocol.Activity{ID: "failed-tool", Role: "tool", Title: "Run go test", State: "failed", Text: "Tests failed",
				Detail: "Error: exit status 1\nNote: 2 packages failed\n\n--- FAIL: TestExample (0.01s)\n    example_test.go:12: got 1, want 2",
				Tool:   &protocol.ToolDetail{Kind: "execute", Status: "failed", Locations: []string{"apps/go"}, RawInput: `{"command":"go test ./..."}`, RawOutput: "--- FAIL: TestExample (0.01s)\n    example_test.go:12: got 1, want 2\nexit status 1"}})
			m.openSurface("activity", "")
			m.viewState().DetailID = id
			m.configureInputs()
			name := fmt.Sprintf("160x50-light%t-activity-%s.ansi", light, id)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Kind and Status pairs come only from the structured Tool payload; an old
// payload without Tool still renders its Detail as text.
func TestActivityToolPairsOnlyFromStructuredTool(t *testing.T) {
	m := testModel()
	old := protocol.Activity{ID: "t", Role: "tool", Title: "Run", State: "failed", Detail: `{"kind":"execute","status":"failed"}`}
	blocks := activityBlocks(m, old)
	text := false
	for _, b := range blocks {
		if b.kind == surfacePairBlock {
			t.Fatalf("old payload produced pair %+v", b)
		}
		text = text || (b.kind == surfaceTextBlock && strings.Contains(b.value, `"kind":"execute"`))
	}
	if !text {
		t.Fatalf("old detail not rendered as text: %+v", blocks)
	}
	withTool := old
	withTool.Tool = &protocol.ToolDetail{Kind: "execute", Status: "failed", RawInput: "{\"command\":\"x\x1b[31m\"}"}
	pairs := map[string]string{}
	for _, b := range activityBlocks(m, withTool) {
		if b.kind == surfacePairBlock {
			pairs[b.label] = b.value
		}
		if strings.Contains(b.value, `"kind":"execute"`) {
			t.Fatalf("Detail rendered alongside Tool: %+v", b)
		}
	}
	// The status row carries the state; no Status pair repeats it.
	if pairs["Kind"] != "execute" || len(pairs) != 1 {
		t.Fatalf("pairs = %v", pairs)
	}
	text2 := m.surfaceRows(activityBlocks(m, withTool), 60)
	for _, r := range text2 {
		if strings.Contains(r.text+r.value, "\x1b") {
			t.Fatalf("control sequence survived: %q", r.text)
		}
	}
}

// A settled row (interrupted by the server) keeps its stale agent-reported
// in_progress tool status only as a plain Reported pair: nothing in the
// inspector claims active work.
func TestActivityToolStatusFollowsSettledState(t *testing.T) {
	m := testModel()
	p := m.colors()
	a := protocol.Activity{ID: "t", Role: "tool", Title: "Run", State: "interrupted",
		Tool: &protocol.ToolDetail{Kind: "execute", Status: "in_progress", RawInput: `{"command":"x"}`}}
	active, _ := panelStatusMark(m, "running")
	pairs := map[string]surfaceBlock{}
	for _, b := range activityBlocks(m, a) {
		if b.ink == p.blue || strings.Contains(b.value+b.glyph, active) {
			t.Fatalf("active mark painted: %+v", b)
		}
		if b.kind == surfacePairBlock {
			pairs[b.label] = b
		}
	}
	if _, repeated := pairs["Status"]; repeated || pairs["Reported"].value != "in_progress" || pairs["Reported"].ink != p.muted {
		t.Fatalf("pairs = %+v", pairs)
	}
	for _, r := range m.surfaceRows(activityBlocks(m, a), 60) {
		if r.ink == p.blue || strings.Contains(r.text+r.value, active) {
			t.Fatalf("active mark in row: %+v", r)
		}
	}
	// Without an activity state the reported tool status is the effective
	// one: the status row shows none, so the Status pair carries it.
	a.State, a.Tool.Status = "", "failed"
	mark, _ := panelStatusMark(m, "failed")
	statusPair := false
	for _, b := range activityBlocks(m, a) {
		statusPair = statusPair || (b.label == "Status" && b.value == mark+" failed")
		if b.label == "Reported" {
			t.Fatalf("Reported pair without a differing state: %+v", b)
		}
	}
	if !statusPair {
		t.Fatal("reported status missing when the activity has no state")
	}
	// A matching reported status adds no Reported pair.
	a.State, a.Tool.Status = "running", "in_progress"
	for _, b := range activityBlocks(m, a) {
		if b.label == "Reported" {
			t.Fatalf("redundant Reported pair: %+v", b)
		}
	}
}
