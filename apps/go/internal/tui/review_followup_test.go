package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func TestActivityDetailShowsRetainedCapture(t *testing.T) {
	p := protocol.Prompt{ID: "p1", Text: "read it", Attachments: []protocol.Attachment{{Kind: "workspace-file", Name: "a.txt", Source: "dir/a.txt", Content: "captured bytes"}}}
	got := activityDetail(protocol.Activity{Detail: `{"ID":"p1"}`, Prompt: &p})
	for _, want := range []string{`{"ID":"p1"}`, "dir/a.txt", "14 bytes captured", "captured bytes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail %q lacks %q", got, want)
		}
	}
	if got := activityDetail(protocol.Activity{Detail: "plain"}); got != "plain" {
		t.Fatalf("detail without a prompt changed: %q", got)
	}
}

func TestThreadSwitchDropsForcedFullWidth(t *testing.T) {
	m := sizedModel(90, 30)
	m.state.Layout.Maximized = false
	m.activate(action{Kind: "open", Value: "plan"})
	if !m.measure().geom.Forced {
		t.Fatal("expected a forced presentation before switching")
	}
	first := m.state.Active
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != first && !th.Closed {
			other = th.ID
			break
		}
	}
	if other == "" {
		t.Skip("fixture has one open thread")
	}
	m.selectThread(other)
	m.selectThread(first)
	if g := m.measure().geom; g.Forced || g.Maximized {
		t.Fatalf("returning to the thread kept a forced presentation: %#v", g)
	}
}
