package server

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/rivo/uniseg"
)

func TestTitleFromPrompt(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"heading mark", "# Fix the login bug", "Fix the login bug"},
		{"list mark", "- add retry logic", "add retry logic"},
		{"quote mark", "> investigate flaky test", "investigate flaky test"},
		{"multi-line takes first non-empty", "\n\n  Fix the login bug\n\nmore details here", "Fix the login bug"},
		{"whitespace collapse", "Fix   the\tlogin   bug", "Fix the login bug"},
		{
			"60 rune word boundary truncation",
			"this prompt line is intentionally long enough that it must be truncated at a clean word boundary rather than mid word",
			"this prompt line is intentionally long enough that it must",
		},
		{"trailing punctuation trimmed", "Fix the login bug!!!", "Fix the login bug"},
		{"empty text falls back", "", defaultThreadTitle},
		{"whitespace only falls back", "   \n\t  ", defaultThreadTitle},
		{"only markers falls back", "# - > ", defaultThreadTitle},
		{"wide unicode preserved", "修复登录页面上的一个重要问题", "修复登录页面上的一个重要问题"},
		{
			"joined emoji cluster is not split at the cap",
			strings.Repeat("x", 59) + "👨‍👩‍👧‍👦 tail",
			strings.Repeat("x", 59) + "👨‍👩‍👧‍👦",
		},
		{
			"combining marks stay with their base at the cap",
			strings.Repeat("y", 59) + "e\u0301\u0301z",
			strings.Repeat("y", 59) + "e\u0301\u0301",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := titleFromPrompt(c.text)
			if got != c.want {
				t.Fatalf("titleFromPrompt(%q) = %q, want %q", c.text, got, c.want)
			}
			if n := uniseg.GraphemeClusterCount(got); n > 60 {
				t.Fatalf("titleFromPrompt(%q) = %q, %d graphemes exceeds cap", c.text, got, n)
			}
		})
	}
}

func TestThreadStartDerivesTitleFromFirstPrompt(t *testing.T) {
	e := testEngine(t)
	c := initialSend(e.snap)
	c.Text = "Fix the login bug\n\ndetails about the failure and repro steps"
	r, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	var found *protocol.Thread
	for i := range e.snap.Threads {
		if e.snap.Threads[i].ID == r.TargetID {
			found = &e.snap.Threads[i]
		}
	}
	if found == nil {
		t.Fatal("started thread not found")
	}
	if found.Title != "Fix the login bug" {
		t.Fatalf("title = %q, want derived from first prompt line", found.Title)
	}
}

func TestPromptSendRenamesPlaceholderTitleOnlyOnce(t *testing.T) {
	e := testEngine(t)
	settings := protocol.Settings{Model: "fixture-model", Effort: "high", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	id, err := applyProject(&e.snap, protocol.Command{Kind: "thread.create", ID: "placeholder-1", ProjectID: e.snap.Projects[0].ID, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	findThread := func() *protocol.Thread {
		for i := range e.snap.Threads {
			if e.snap.Threads[i].ID == id {
				return &e.snap.Threads[i]
			}
		}
		return nil
	}
	if t0 := findThread(); t0 == nil || t0.Title != defaultThreadTitle {
		t.Fatalf("expected placeholder-titled thread: %+v", t0)
	}
	first := protocol.Command{Version: 1, ID: "first", Kind: "prompt.send", ThreadID: id, Text: "Add retry logic to the sync job"}
	if _, err := e.command(first); err != nil {
		t.Fatal(err)
	}
	if t0 := findThread(); t0.Title != "Add retry logic to the sync job" {
		t.Fatalf("first send did not derive title: %+v", t0.Title)
	}
	second := protocol.Command{Version: 1, ID: "second", Kind: "prompt.send", ThreadID: id, Text: "Different follow-up text entirely"}
	if _, err := e.command(second); err != nil {
		t.Fatal(err)
	}
	if t0 := findThread(); t0.Title != "Add retry logic to the sync job" {
		t.Fatalf("second send changed established title: %+v", t0.Title)
	}
}

func TestReopenSendRenamesPlaceholderTitledClosedThread(t *testing.T) {
	e := testEngine(t)
	settings := protocol.Settings{Model: "fixture-model", Effort: "high", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	id, err := applyProject(&e.snap, protocol.Command{Kind: "thread.create", ID: "placeholder-2", ProjectID: e.snap.Projects[0].ID, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	var t0 *protocol.Thread
	for i := range e.snap.Threads {
		if e.snap.Threads[i].ID == id {
			t0 = &e.snap.Threads[i]
		}
	}
	t0.Closed = true
	t0.LifecycleRevision = 1
	reopen := protocol.Command{Version: 1, ID: "reopen", Kind: "prompt.reopen-send", ThreadID: id, Revision: 1, Text: "Investigate the flaky checkout test", Settings: &settings}
	if _, err := e.command(reopen); err != nil {
		t.Fatal(err)
	}
	var after *protocol.Thread
	for i := range e.snap.Threads {
		if e.snap.Threads[i].ID == id {
			after = &e.snap.Threads[i]
		}
	}
	if after == nil || after.Title != "Investigate the flaky checkout test" || after.Closed {
		t.Fatalf("reopen-send did not derive title / reopen: %+v", after)
	}
	if strings.Contains(after.Title, defaultThreadTitle) {
		t.Fatal("title still placeholder")
	}
}
