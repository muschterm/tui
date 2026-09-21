package server

import (
	"context"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"reflect"
	"strings"
	"testing"
)

func initialSend(s protocol.Snapshot) protocol.Command {
	settings := s.Threads[0].Selected
	settings.Effort = "high"
	return protocol.Command{Version: 1, ID: "first-send", Kind: "thread.start", ProjectID: s.Projects[0].ID, Agent: "Fixture agent", Text: "first captured prompt", Settings: &settings, Attachments: []protocol.Attachment{{Name: "capture", Content: "original"}}}
}
func TestInitialSendRejectsWithoutCreatingThread(t *testing.T) {
	for name, change := range map[string]func(*protocol.Command){
		"empty":                func(c *protocol.Command) { c.Text = " \n" },
		"long":                 func(c *protocol.Command) { c.Text = strings.Repeat("x", 16385) },
		"settings missing":     func(c *protocol.Command) { c.Settings = nil },
		"settings unsupported": func(c *protocol.Command) { c.Settings.Model = "other" },
		"agent missing":        func(c *protocol.Command) { c.Agent = "" },
		"agent unsupported":    func(c *protocol.Command) { c.Agent = "other" },
		"attachment":           func(c *protocol.Command) { c.Attachments[0].Content = strings.Repeat("x", 65537) },
		"missing project":      func(c *protocol.Command) { c.ProjectID = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			e := testEngine(t)
			c := initialSend(e.snap)
			change(&c)
			before := clone(e.snap)
			if _, err := e.command(c); err == nil {
				t.Fatal("invalid first send accepted")
			}
			if !reflect.DeepEqual(before, e.snap) {
				t.Fatal("invalid first send changed state")
			}
			stored, _, err := e.store.Load()
			if err != nil || !reflect.DeepEqual(stored, before) {
				t.Fatal("invalid first send changed storage", err)
			}
		})
	}
}
func TestInitialSendRestartRetryAndDeletion(t *testing.T) {
	home := t.TempDir()
	client, stop := startTestServer(t, home)
	ctx := context.Background()
	snap, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := initialSend(snap)
	receipt, err := client.Command(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TargetID != "thread-first-send" {
		t.Fatal(receipt)
	}
	stop()
	client, stop = startTestServer(t, home)
	defer stop()
	again, err := client.Command(ctx, c)
	if err != nil || again != receipt {
		t.Fatalf("retry: %+v %v", again, err)
	}
	snap, err = client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *protocol.Thread
	count := 0
	for i := range snap.Threads {
		if snap.Threads[i].ID == receipt.TargetID {
			found = &snap.Threads[i]
			count++
		}
	}
	if count != 1 || found == nil || !found.NeedsResume || found.TurnID != "prompt-first-send" {
		t.Fatalf("initial turn missing: %+v", found)
	}
	if found.Selected != *c.Settings || found.Effective != *c.Settings || len(found.Activity) == 0 || found.Activity[0].Prompt == nil {
		t.Fatalf("initial settings/capture lost: %+v", found)
	}
	p := found.Activity[0].Prompt
	if p.Text != c.Text || p.Settings != *c.Settings || !reflect.DeepEqual(p.Attachments, c.Attachments) {
		t.Fatalf("capture changed: %+v", p)
	}
	_, err = client.Command(ctx, protocol.Command{Version: 1, ID: "delete-first", Kind: "thread.delete", ThreadID: receipt.TargetID, Revision: found.LifecycleRevision})
	if err != nil {
		t.Fatal(err)
	}
	again, err = client.Command(ctx, c)
	if err != nil || again.State != "deleted" || again.TargetID != "" {
		t.Fatalf("deleted start resurrected: %+v %v", again, err)
	}
	c.Text = "changed identity"
	if _, err = client.Command(ctx, c); err == nil {
		t.Fatal("changed tombstoned command accepted")
	}
}
func TestReopenSendValidatesBeforeReopening(t *testing.T) {
	for name, change := range map[string]func(*protocol.Command){
		"stale":                func(c *protocol.Command) { c.Revision-- },
		"empty":                func(c *protocol.Command) { c.Text = "" },
		"missing settings":     func(c *protocol.Command) { c.Settings = nil },
		"unsupported settings": func(c *protocol.Command) { c.Settings.Model = "other" },
		"attachment":           func(c *protocol.Command) { c.Attachments[0].Content = strings.Repeat("x", 65537) },
	} {
		t.Run(name, func(t *testing.T) {
			e := testEngine(t)
			e.snap.Threads[0].Closed = true
			e.snap.Threads[0].LifecycleRevision = 2
			c := initialSend(e.snap)
			c.Kind, c.ThreadID, c.Revision = "prompt.reopen-send", e.snap.Threads[0].ID, 2
			change(&c)
			before := clone(e.snap)
			if _, err := e.command(c); err == nil {
				t.Fatal("invalid reopen accepted")
			}
			if !reflect.DeepEqual(e.snap, before) {
				t.Fatal("invalid reopen changed state")
			}
		})
	}
}
func TestReopenSendRetryAndConcurrentLifecycle(t *testing.T) {
	e := testEngine(t)
	thread := &e.snap.Threads[1]
	thread.Closed, thread.LifecycleRevision = true, 4
	c := initialSend(e.snap)
	c.Kind, c.ThreadID, c.Revision = "prompt.reopen-send", thread.ID, 4
	receipt, err := e.command(c)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TargetID != "prompt-first-send" || e.snap.Threads[1].Closed || e.snap.Threads[1].LifecycleRevision != 5 {
		t.Fatal("reopen/send not atomic")
	}
	before := clone(e.snap)
	again, err := e.command(c)
	if err != nil || again != receipt || !reflect.DeepEqual(before, e.snap) {
		t.Fatal("duplicate reopen/send changed state", err)
	}
	c.ID = "stale-second-send"
	if _, err = e.command(c); err == nil || !reflect.DeepEqual(before, e.snap) {
		t.Fatal("stale review against already open thread accepted")
	}
}
