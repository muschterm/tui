//go:build unix

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/lifecycle"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
	"github.com/muschterm/tui/apps/go/internal/term"
)

func requireShell(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
}

// terminalEngine is an engine with /bin/sh terminals whose fixture checkouts
// start in a temporary home. dbDir holds its SQLite files.
func terminalEngine(t *testing.T) (e *engine, home, dbDir string) {
	t.Helper()
	requireShell(t)
	dbDir = t.TempDir()
	st, err := storage.Open(filepath.Join(dbDir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s := fixture.Initial()
	if err := st.Save(s, nil, nil); err != nil {
		t.Fatal(err)
	}
	e = &engine{snap: s, store: st, subscribers: map[chan protocol.Snapshot]bool{}}
	home = t.TempDir()
	e.terminals.shell = "/bin/sh"
	// Children never see the developer's home or history file.
	e.terminals.env = []string{"HOME=" + home, "HISTFILE=/dev/null", "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	e.terminals.home = func() (string, error) { return home, nil }
	t.Cleanup(func() {
		_ = e.stopTerminals()
		st.Close()
	})
	return e, home, dbDir
}

// streamClient serves the stream endpoint without the authentication wrapper
// (covered separately through Serve).
func streamClient(t *testing.T, e *engine) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/terminals/{id}/stream", e.terminalStream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return client.New(protocol.Discovery{Version: protocol.Version, URL: srv.URL, Token: "test"})
}

func openTerminal(t *testing.T, e *engine, id, clientID string) protocol.Terminal {
	t.Helper()
	r, err := e.command(protocol.Command{Version: 1, ID: id, Kind: "terminal.open", ThreadID: "thread-shell", ClientID: clientID, TerminalSize: &protocol.TerminalSize{Cols: 60, Rows: 12}})
	if err != nil {
		t.Fatal(err)
	}
	return terminalRecord(t, e, r.TargetID)
}

func terminalRecord(t *testing.T, e *engine, id string) protocol.Terminal {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := terminalByID(&e.snap, id)
	if rec == nil {
		t.Fatalf("terminal %s missing", id)
	}
	return *rec
}

func waitTerminal(t *testing.T, e *engine, id string, cond func(protocol.Terminal) bool) protocol.Terminal {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := terminalRecord(t, e, id)
		if cond(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal %s did not reach the expected state: %+v", id, rec)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openStream(t *testing.T, c *client.Client, id, clientID string) *client.TerminalStream {
	t.Helper()
	s, err := c.OpenTerminalStream(context.Background(), id, clientID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func screenText(s *protocol.TerminalScreen) string {
	lines := make([]string, len(s.Lines))
	for i, line := range s.Lines {
		var b strings.Builder
		for _, run := range line {
			b.WriteString(run.Text)
		}
		lines[i] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(lines, "\n")
}

// nextEvent returns the first event satisfying match, failing on timeout or
// stream end.
func nextEvent(t *testing.T, s *client.TerminalStream, what string, match func(protocol.TerminalEvent) bool) protocol.TerminalEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	var last *protocol.TerminalScreen
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("stream ended waiting for %s: %v", what, s.Err())
			}
			if ev.Screen != nil {
				last = ev.Screen
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			screen := ""
			if last != nil {
				screen = screenText(last)
			}
			t.Fatalf("timed out waiting for %s; last screen:\n%s", what, screen)
		}
	}
}

func screenContains(text string) func(protocol.TerminalEvent) bool {
	return func(ev protocol.TerminalEvent) bool {
		return ev.Screen != nil && strings.Contains(screenText(ev.Screen), text)
	}
}

func isType(typ string) func(protocol.TerminalEvent) bool {
	return func(ev protocol.TerminalEvent) bool { return ev.Type == typ }
}

func isRejected(reason, request string) func(protocol.TerminalEvent) bool {
	return func(ev protocol.TerminalEvent) bool {
		return ev.Rejected != nil && ev.Rejected.Reason == reason && ev.Rejected.For == request
	}
}

func isControl(controller string, gen int64) func(protocol.TerminalEvent) bool {
	return func(ev protocol.TerminalEvent) bool {
		return ev.Control != nil && ev.Control.Controller == controller && ev.Control.Gen == gen
	}
}

func TestTerminalControlInputResizeAndTakeControl(t *testing.T) {
	e, home, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open-1", "alice")
	if rec.State != protocol.TerminalStateRunning || rec.Dir != home || rec.Controller != "alice" || rec.ControlGen != 1 || rec.Cols != 60 || rec.Rows != 12 || rec.Shell != "/bin/sh" || rec.Output != "" {
		t.Fatalf("unexpected record %+v", rec)
	}
	alice := openStream(t, c, rec.ID, "alice")
	first := nextEvent(t, alice, "initial screen", isType(protocol.TerminalEventScreen))
	if first.Screen.Cols != 60 || first.Screen.Rows != 12 || len(first.Screen.Lines) != 12 {
		t.Fatalf("initial screen %dx%d with %d lines", first.Screen.Cols, first.Screen.Rows, len(first.Screen.Lines))
	}
	nextEvent(t, alice, "initial control", isControl("alice", 1))
	if err := alice.SendInput(1, []byte("printf 'mark-%s\\n' ok\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "controller output", screenContains("mark-ok"))

	bob := openStream(t, c, rec.ID, "bob")
	nextEvent(t, bob, "observer control", isControl("alice", 1))
	if err := bob.SendInput(1, []byte("echo intruder\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "observer rejection", isRejected(protocol.TerminalRejectNotController, protocol.TerminalRequestInput))
	if err := bob.Resize(1, 30, 5); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "observer resize rejection", isRejected(protocol.TerminalRejectNotController, protocol.TerminalRequestResize))

	take := protocol.Command{Version: 1, ID: "take-1", Kind: "terminal.take-control", ThreadID: "thread-shell", TargetID: rec.ID, ClientID: "bob"}
	if _, err := e.command(take); err != nil {
		t.Fatal(err)
	}
	if got := terminalRecord(t, e, rec.ID); got.Controller != "bob" || got.ControlGen != 2 {
		t.Fatalf("take-control not recorded: %+v", got)
	}
	nextEvent(t, alice, "control lost notice", isControl("bob", 2))
	nextEvent(t, bob, "control gained notice", isControl("bob", 2))
	if err := alice.SendInput(1, []byte("echo stale\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "previous controller rejection", isRejected(protocol.TerminalRejectNotController, protocol.TerminalRequestInput))
	if err := bob.SendInput(1, []byte("echo old-gen\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "stale generation", isRejected(protocol.TerminalRejectStaleGeneration, protocol.TerminalRequestInput))
	// Taking control again as the controller changes nothing.
	take.ID = "take-2"
	if _, err := e.command(take); err != nil || terminalRecord(t, e, rec.ID).ControlGen != 2 {
		t.Fatal("repeated take-control changed the generation", err)
	}

	if err := bob.Resize(2, 50, 10); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "resized screen", func(ev protocol.TerminalEvent) bool {
		return ev.Screen != nil && ev.Screen.Cols == 50 && ev.Screen.Rows == 10 && len(ev.Screen.Lines) == 10
	})
	if err := bob.SendInput(2, []byte("stty size\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "child sees new size", screenContains("10 50"))
	waitTerminal(t, e, rec.ID, func(r protocol.Terminal) bool { return r.Cols == 50 && r.Rows == 10 })

	// Any observer may read history.
	if err := bob.SendInput(2, []byte("i=0; while [ $i -lt 40 ]; do echo line-$i; i=$((i+1)); done\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "history output", screenContains("line-39"))
	if err := alice.RequestScrollback(-1, 5); err != nil {
		t.Fatal(err)
	}
	sb := nextEvent(t, alice, "scrollback", isType(protocol.TerminalEventScrollback)).Scrollback
	if sb.Total == 0 || len(sb.Lines) == 0 || sb.From != max(0, sb.Total-5) {
		t.Fatalf("scrollback %+v", sb)
	}
	if err := alice.RequestScrollback(0, protocol.TerminalMaxScrollback+1); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "oversized scrollback rejected", isRejected(protocol.TerminalRejectInvalid, protocol.TerminalRequestScrollback))
}

func TestTerminalOpenRetryStartsOneProcess(t *testing.T) {
	e, _, _ := terminalEngine(t)
	cmd := protocol.Command{Version: 1, ID: "same", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice"}
	var wg sync.WaitGroup
	targets := make([]string, 8)
	for i := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.command(cmd)
			if err != nil {
				t.Error(err)
				return
			}
			targets[i] = r.TargetID
		}()
	}
	wg.Wait()
	r, err := e.command(cmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target != r.TargetID {
			t.Fatalf("retries returned different terminals: %v", targets)
		}
	}
	e.mu.Lock()
	sessions, records := len(e.terminals.sessions), len(e.snap.Terminals)
	e.mu.Unlock()
	if sessions != 1 || records != 1 {
		t.Fatalf("retries started %d sessions and %d records", sessions, records)
	}
	// Default size applies without TerminalSize.
	if rec := terminalRecord(t, e, r.TargetID); rec.Cols != defaultTerminalCols || rec.Rows != defaultTerminalRows {
		t.Fatalf("default size %dx%d", rec.Cols, rec.Rows)
	}
}

func TestTerminalCloseEndsWithExitAndIsIdempotent(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	stream := openStream(t, c, rec.ID, "alice")
	nextEvent(t, stream, "control", isType(protocol.TerminalEventControl))
	closeCmd := protocol.Command{Version: 1, ID: "close", Kind: "terminal.close", ThreadID: "thread-shell", TargetID: rec.ID}
	if _, err := e.command(closeCmd); err != nil {
		t.Fatal(err)
	}
	if got := terminalRecord(t, e, rec.ID); got.State != protocol.TerminalStateClosing && got.State != protocol.TerminalStateEnded {
		t.Fatalf("close not accepted: %+v", got)
	}
	// Input after an accepted close is refused.
	_ = stream.SendInput(1, []byte("echo late\n"))
	ended := waitTerminal(t, e, rec.ID, func(r protocol.Terminal) bool { return r.State == protocol.TerminalStateEnded })
	if ended.EndReason != protocol.TerminalEndClosed || ended.Exit == nil {
		t.Fatalf("ended record %+v", ended)
	}
	ev := nextEvent(t, stream, "ended event", isType(protocol.TerminalEventEnded))
	if ev.Ended.Reason != protocol.TerminalEndClosed || ev.Ended.Exit == nil {
		t.Fatalf("ended event %+v", ev.Ended)
	}
	if _, ok := <-stream.Events(); ok {
		t.Fatal("stream continued after ended")
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ended stream reported %v", err)
	}
	closeCmd.ID = "close-again"
	if _, err := e.command(closeCmd); err != nil {
		t.Fatal("closing an ended terminal failed", err)
	}
	if got := terminalRecord(t, e, rec.ID); got.State != protocol.TerminalStateEnded || got.Revision != ended.Revision {
		t.Fatalf("idempotent close changed the record: %+v", got)
	}
	take := protocol.Command{Version: 1, ID: "take", Kind: "terminal.take-control", ThreadID: "thread-shell", TargetID: rec.ID, ClientID: "bob"}
	if _, err := e.command(take); err == nil {
		t.Fatal("took control of an ended terminal")
	}
	// A late observer receives only the recorded end.
	late := openStream(t, c, rec.ID, "carol")
	ev = nextEvent(t, late, "late ended", isType(protocol.TerminalEventEnded))
	if ev.Ended.Reason != protocol.TerminalEndClosed {
		t.Fatalf("late ended %+v", ev.Ended)
	}
	// A new open is a new session.
	if again := openTerminal(t, e, "open-again", "alice"); again.ID == rec.ID {
		t.Fatal("reopen reused the ended identity")
	}
}

func TestTerminalChildExitIsPublished(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	stream := openStream(t, c, rec.ID, "alice")
	nextEvent(t, stream, "control", isType(protocol.TerminalEventControl))
	if err := stream.SendInput(1, []byte("exit 3\n")); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, stream, "ended", isType(protocol.TerminalEventEnded))
	if ev.Ended.Reason != protocol.TerminalEndExited || ev.Ended.Exit == nil || ev.Ended.Exit.Code != 3 {
		t.Fatalf("ended event %+v", ev.Ended)
	}
	got := waitTerminal(t, e, rec.ID, func(r protocol.Terminal) bool { return r.State == protocol.TerminalStateEnded })
	if got.EndReason != protocol.TerminalEndExited || got.Exit == nil || got.Exit.Code != 3 {
		t.Fatalf("record %+v", got)
	}
}

func TestTerminalThreadDeleteEndsSessions(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	stream := openStream(t, c, rec.ID, "alice")
	nextEvent(t, stream, "control", isType(protocol.TerminalEventControl))
	e.mu.Lock()
	s := e.terminals.sessions[rec.ID]
	e.mu.Unlock()
	if _, err := e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "thread-shell"}); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, stream, "ended", isType(protocol.TerminalEventEnded))
	if ev.Ended.Reason != protocol.TerminalEndThreadDeleted {
		t.Fatalf("ended %+v", ev.Ended)
	}
	select {
	case <-s.finished:
	case <-time.After(10 * time.Second):
		t.Fatal("deleted thread's session did not end")
	}
	e.mu.Lock()
	remaining, records := len(e.terminals.sessions), len(e.snap.Terminals)
	e.mu.Unlock()
	if remaining != 0 || records != 0 {
		t.Fatalf("%d sessions and %d records remain", remaining, records)
	}
}

func TestTerminalLiveCapacity(t *testing.T) {
	e, _, _ := terminalEngine(t)
	e.mu.Lock()
	ts := e.terminalsLocked()
	for i := range maxLiveTerminals {
		ts.sessions[fmt.Sprint("fake-", i)] = &termSession{finished: make(chan struct{})}
	}
	e.mu.Unlock()
	_, err := e.command(protocol.Command{Version: 1, ID: "over", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice"})
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "capacity" {
		t.Fatalf("65th live terminal: %v", err)
	}
	e.mu.Lock()
	for id := range ts.sessions {
		delete(ts.sessions, id)
	}
	e.mu.Unlock()
}

func TestTerminalEndedRetentionAndLoad(t *testing.T) {
	var s protocol.Snapshot
	s.Terminals = append(s.Terminals, protocol.Terminal{ID: "live", State: protocol.TerminalStateRunning}, protocol.Terminal{ID: "closing", State: protocol.TerminalStateClosing})
	for i := range maxEndedTerminals + 3 {
		s.Terminals = append(s.Terminals, protocol.Terminal{ID: fmt.Sprint("ended-", i), State: protocol.TerminalStateEnded, EndReason: protocol.TerminalEndExited})
	}
	endLoadedTerminals(&s)
	if len(s.Terminals) != maxEndedTerminals {
		t.Fatalf("retained %d ended records", len(s.Terminals))
	}
	for _, rec := range s.Terminals {
		if rec.State != protocol.TerminalStateEnded {
			t.Fatalf("live record survived load: %+v", rec)
		}
		if strings.HasPrefix(rec.ID, "ended-") && rec.EndReason != protocol.TerminalEndExited || !strings.HasPrefix(rec.ID, "ended-") && rec.EndReason != protocol.TerminalEndServerRestarted {
			t.Fatalf("wrong end reason %+v", rec)
		}
	}
	if s.Terminals[len(s.Terminals)-1].ID != fmt.Sprint("ended-", maxEndedTerminals+2) {
		t.Fatal("newest ended record was pruned")
	}
}

func TestTerminalFloodDoesNotBlockOnSlowReader(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	// This observer never reads its events.
	openStream(t, c, rec.ID, "stalled")
	alice := openStream(t, c, rec.ID, "alice")
	nextEvent(t, alice, "control", isType(protocol.TerminalEventControl))
	if err := alice.SendInput(1, []byte("yes flood-line\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "flood output", screenContains("flood-line"))
	// Frames keep advancing for the reading stream.
	var seq uint64
	for range 5 {
		ev := nextEvent(t, alice, "advancing frame", func(ev protocol.TerminalEvent) bool { return ev.Screen != nil && ev.Screen.Seq > seq })
		seq = ev.Screen.Seq
	}
	start := time.Now()
	if _, err := e.command(protocol.Command{Version: 1, ID: "close", Kind: "terminal.close", ThreadID: "thread-shell", TargetID: rec.ID}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("close command waited on the session")
	}
	waitTerminal(t, e, rec.ID, func(r protocol.Terminal) bool { return r.State == protocol.TerminalStateEnded })
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("flooding terminal took %v to end", elapsed)
	}
	nextEvent(t, alice, "ended", isType(protocol.TerminalEventEnded))
}

func TestTerminalInputIsNeverPersisted(t *testing.T) {
	e, _, dbDir := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	alice := openStream(t, c, rec.ID, "alice")
	nextEvent(t, alice, "control", isType(protocol.TerminalEventControl))
	marker := "zq-private-" + ID()[:12]
	if err := alice.SendInput(1, []byte("printf '%s\\n' "+marker+"\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "marker echo", screenContains(marker))
	// Commit and flush more state so any leak would have been written.
	if _, err := e.command(protocol.Command{Version: 1, ID: "take", Kind: "terminal.take-control", ThreadID: "thread-shell", TargetID: rec.ID, ClientID: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "close", Kind: "terminal.close", ThreadID: "thread-shell", TargetID: rec.ID}); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, e, rec.ID, func(r protocol.Terminal) bool { return r.State == protocol.TerminalStateEnded })
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(dbDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(marker)) {
			t.Fatalf("terminal input persisted in %s", entry.Name())
		}
	}
}

func TestTerminalStreamAuthenticationAndServerLifecycle(t *testing.T) {
	requireShell(t)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HISTFILE", "/dev/null")
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	c, cleanup := startTestServer(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(snap.Capabilities, ","), "embedded-terminals") {
		t.Fatal("embedded-terminals capability missing")
	}
	r, err := c.Command(ctx, protocol.Command{ID: "open", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	wsURL := strings.Replace(c.Discovery.URL, "http://", "ws://", 1) + "/v1/terminals/" + r.TargetID + "/stream?client_id=alice"
	if _, resp, err := websocket.Dial(ctx, wsURL, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stream: %v", err)
	}
	if _, err := c.OpenTerminalStream(ctx, "terminal-missing", "alice"); !errors.Is(err, client.ErrTerminalNotFound) {
		t.Fatalf("unknown terminal: %v", err)
	}
	stream, err := c.OpenTerminalStream(ctx, r.TargetID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	nextEvent(t, stream, "control", isType(protocol.TerminalEventControl))
	if err := stream.SendInput(1, []byte("pwd\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, stream, "fixture checkout starts in home", screenContains(userHome))
	if err := lifecycle.Stop(ctx, home); err != nil {
		t.Fatal(err)
	}
	cleanup()
	for range stream.Events() {
	}

	st, err := storage.Open(filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Terminals) != 1 || stored.Terminals[0].State != protocol.TerminalStateEnded || stored.Terminals[0].EndReason != protocol.TerminalEndServerStopped {
		t.Fatalf("stopped terminal %+v", stored.Terminals)
	}
	// A record left live in storage (for example after a crash) ends on load.
	stored.Terminals[0].State, stored.Terminals[0].EndReason = protocol.TerminalStateRunning, ""
	if err := st.Save(stored, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	c, cleanup = startTestServer(t, home)
	defer cleanup()
	snap, err = c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Terminals[0]; got.State != protocol.TerminalStateEnded || got.EndReason != protocol.TerminalEndServerRestarted {
		t.Fatalf("restarted terminal %+v", got)
	}
	late, err := c.OpenTerminalStream(ctx, r.TargetID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	ev := nextEvent(t, late, "restarted ended", isType(protocol.TerminalEventEnded))
	if ev.Ended.Reason != protocol.TerminalEndServerRestarted {
		t.Fatalf("ended %+v", ev.Ended)
	}
}

func TestTerminalLineEncoding(t *testing.T) {
	red := term.Color{Kind: term.ColorIndexed, Index: 1}
	rgb := term.Color{Kind: term.ColorRGB, R: 0x12, G: 0xab, B: 0xff}
	row := []term.Cell{
		{Text: "a", Width: 1}, {Text: "b", Width: 1},
		{Text: "世", Width: 2}, {Width: 0},
		{Text: "é", Width: 1},
		{Text: "x", Width: 1, FG: red, Attrs: term.AttrBold | term.AttrUnderline},
		{Text: "y", Width: 1, BG: rgb},
		{Text: "", Width: 1},
	}
	line := encodeLine(row)
	want := []protocol.TerminalRun{
		{Text: "ab", Width: 1},
		{Text: "世", Width: 2},
		{Text: "é", Width: 1, Cluster: true},
		{Text: "x", Width: 1, FG: "1", Attrs: protocol.TerminalAttrBold | protocol.TerminalAttrUnderline},
		{Text: "y", Width: 1, BG: "#12abff"},
		{Text: " ", Width: 1},
	}
	if fmt.Sprint(line) != fmt.Sprint(protocol.TerminalLine(want)) {
		t.Fatalf("encoded %+v\nwant %+v", line, want)
	}
	cells := 0
	for _, run := range line {
		cells += run.Cells()
	}
	if cells != 8 {
		t.Fatalf("runs cover %d cells", cells)
	}
	if g := line[2].Graphemes(); len(g) != 1 || g[0] != "é" {
		t.Fatalf("cluster graphemes %q", g)
	}
	if i, ok := line[3].FG.Index(); !ok || i != 1 {
		t.Fatal("indexed colour")
	}
	if r, g, b, ok := line[4].BG.RGB(); !ok || r != 0x12 || g != 0xab || b != 0xff {
		t.Fatal("rgb colour")
	}
	for bit, want := range map[term.Attrs]uint8{term.AttrBold: protocol.TerminalAttrBold, term.AttrDim: protocol.TerminalAttrDim, term.AttrItalic: protocol.TerminalAttrItalic, term.AttrUnderline: protocol.TerminalAttrUnderline, term.AttrReverse: protocol.TerminalAttrReverse, term.AttrStrike: protocol.TerminalAttrStrike, term.AttrBlink: protocol.TerminalAttrBlink} {
		if uint8(bit) != want {
			t.Fatalf("attribute bit %d differs from protocol %d", bit, want)
		}
	}
	if protocol.TerminalMaxInput != term.MaxInput {
		t.Fatal("protocol input limit differs from the term package")
	}
}

func TestTerminalKeyAndPasteRequests(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open-keys", "alice")
	alice := openStream(t, c, rec.ID, "alice")
	nextEvent(t, alice, "initial control", isControl("alice", 1))
	// The child enables application cursor keys and bracketed paste, then
	// reports the hex of the bytes it reads.
	script := "stty raw -echo; printf '\\033[?1h\\033[?2004hREADY'; r=$(dd bs=1 count=21 2>/dev/null | od -An -tx1 | tr -d ' \\n'); printf '\\r\\ngot:%s.' \"$r\"\n"
	if err := alice.SendInput(1, []byte(script)); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "ready", screenContains("READY"))
	for _, k := range []protocol.TerminalKey{{Code: "up"}, {Code: "c", Mods: protocol.TerminalModCtrl}, {Code: "x", Text: "x", Mods: protocol.TerminalModAlt}} {
		if err := alice.SendKey(1, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := alice.Paste(1, "a\nb"); err != nil {
		t.Fatal(err)
	}
	// ESC O A, 03, ESC x, ESC[200~ a CR b ESC[201~
	nextEvent(t, alice, "encoded bytes", screenContains("got:1b4f41031b781b5b3230307e610d621b5b3230317e."))

	bob := openStream(t, c, rec.ID, "bob")
	nextEvent(t, bob, "observer control", isControl("alice", 1))
	if err := bob.SendKey(1, protocol.TerminalKey{Code: "a"}); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "observer key rejected", isRejected(protocol.TerminalRejectNotController, protocol.TerminalRequestKey))
	if err := bob.Paste(1, "x"); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, bob, "observer paste rejected", isRejected(protocol.TerminalRejectNotController, protocol.TerminalRequestPaste))
	if err := alice.SendKey(1, protocol.TerminalKey{Code: "a", Text: "\x1b[2J"}); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "control text rejected", isRejected(protocol.TerminalRejectInvalid, protocol.TerminalRequestKey))
	if err := alice.SendKey(2, protocol.TerminalKey{Code: "a"}); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "stale key rejected", isRejected(protocol.TerminalRejectStaleGeneration, protocol.TerminalRequestKey))
}

func TestTerminalStalledChildInputIsBusyNotEnded(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	rec := openTerminal(t, e, "open", "alice")
	alice := openStream(t, c, rec.ID, "alice")
	nextEvent(t, alice, "control", isType(protocol.TerminalEventControl))
	if err := alice.SendInput(1, []byte("stty raw -echo; printf READY; sleep 40\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "ready", screenContains("READY"))
	chunk := []byte(strings.Repeat("x", protocol.TerminalMaxInput))
	for range 4 {
		if err := alice.SendInput(1, chunk); err != nil {
			t.Fatal(err)
		}
	}
	ev := nextEvent(t, alice, "busy rejection", func(ev protocol.TerminalEvent) bool { return ev.Rejected != nil })
	if ev.Rejected.Reason != protocol.TerminalRejectBusy || ev.Rejected.For != protocol.TerminalRequestInput || ev.Rejected.Written >= protocol.TerminalMaxInput {
		t.Fatalf("rejection %+v", ev.Rejected)
	}
	if got := terminalRecord(t, e, rec.ID); got.State != protocol.TerminalStateRunning {
		t.Fatalf("busy terminal reported %+v", got)
	}
}

func TestTerminalCombiningFloodFramesStayBounded(t *testing.T) {
	e, home, _ := terminalEngine(t)
	c := streamClient(t, e)
	var b strings.Builder
	for range 30 {
		for range 60 {
			b.WriteString("a" + strings.Repeat("\u0301", 2000))
		}
		b.WriteString("\r\n")
	}
	if err := os.WriteFile(filepath.Join(home, "flood"), []byte(b.String()+"FLOOD-DONE\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := e.command(protocol.Command{Version: 1, ID: "open", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice", TerminalSize: &protocol.TerminalSize{Cols: 300, Rows: 80}})
	if err != nil {
		t.Fatal(err)
	}
	alice := openStream(t, c, r.TargetID, "alice")
	nextEvent(t, alice, "control", isType(protocol.TerminalEventControl))
	if err := alice.SendInput(1, []byte("cat flood\n")); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, alice, "flood shown", screenContains("FLOOD-DONE"))
	for _, line := range ev.Screen.Lines {
		for _, run := range line {
			if run.Cluster && len(run.Text) > term.MaxClusterBytes {
				t.Fatalf("frame carries a %d-byte cluster", len(run.Text))
			}
		}
	}
	if err := alice.RequestScrollback(-1, protocol.TerminalMaxScrollback); err != nil {
		t.Fatal(err)
	}
	sb := nextEvent(t, alice, "scrollback", isType(protocol.TerminalEventScrollback)).Scrollback
	for _, line := range sb.Lines {
		for _, run := range line {
			if run.Cluster && len(run.Text) > term.MaxClusterBytes {
				t.Fatalf("scrollback carries a %d-byte cluster", len(run.Text))
			}
		}
	}
}

func TestTerminalFrameBudgetDegrades(t *testing.T) {
	scr := term.Screen{Cols: term.MaxCols, Rows: term.MaxRows, Lines: make([][]term.Cell, term.MaxRows)}
	cluster := "a" + strings.Repeat("\u0301", 15)
	for y := range scr.Lines {
		row := make([]term.Cell, scr.Cols)
		for x := range row {
			row[x] = term.Cell{Text: cluster, Width: 1, FG: term.Color{Kind: term.ColorRGB, R: uint8(x), G: uint8(y), B: 7}, BG: term.Color{Kind: term.ColorRGB, R: uint8(y), B: uint8(x)}}
		}
		scr.Lines[y] = row
	}
	b := encodeFrame(scr)
	if len(b) > terminalFrameBudget {
		t.Fatalf("frame is %d bytes", len(b))
	}
	var ev protocol.TerminalEvent
	if err := json.Unmarshal(b, &ev); err != nil || ev.Screen == nil || !ev.Screen.Degraded || len(ev.Screen.Lines) != term.MaxRows {
		t.Fatalf("degraded frame %v", err)
	}
	cells := 0
	for _, run := range ev.Screen.Lines[0] {
		cells += run.Cells()
	}
	if cells != term.MaxCols {
		t.Fatalf("degraded row covers %d cells", cells)
	}
	small := encodeFrame(term.Screen{Cols: 2, Rows: 1, Lines: [][]term.Cell{{{Text: "é", Width: 1}, {Text: "x", Width: 1}}}})
	if bytes.Contains(small, []byte("degraded")) {
		t.Fatal("small frame degraded")
	}
}

func TestTerminalStopReportsUnconfirmedSessions(t *testing.T) {
	e, _, _ := terminalEngine(t)
	e.mu.Lock()
	e.snap.Terminals = append(e.snap.Terminals, protocol.Terminal{ID: "stuck", ThreadID: "thread-shell", State: protocol.TerminalStateRunning, Controller: "alice", ControlGen: 1})
	// A session whose process never confirms exit.
	e.terminalsLocked().sessions["stuck"] = &termSession{e: e, id: "stuck", running: true, finished: make(chan struct{})}
	e.mu.Unlock()
	errs := make(chan error, 1)
	go func() { errs <- e.stopTerminals() }()
	waitTerminal(t, e, "stuck", func(r protocol.Terminal) bool { return r.State == protocol.TerminalStateClosing })
	take := protocol.Command{Version: 1, ID: "take", Kind: "terminal.take-control", ThreadID: "thread-shell", TargetID: "stuck", ClientID: "bob"}
	if _, err := e.command(take); err == nil {
		t.Fatal("took control of a stopping terminal")
	}
	first := <-errs
	if first == nil {
		t.Fatal("unconfirmed session reported a clean stop")
	}
	start := time.Now()
	if again := e.stopTerminals(); again != first || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("second stop waited or disagreed: %v", again)
	}
	e.mu.Lock()
	final := clone(e.snap)
	delete(e.terminals.sessions, "stuck")
	e.mu.Unlock()
	endStoppedTerminals(&final)
	got := terminalByID(&final, "stuck")
	if got.State != protocol.TerminalStateEnded || got.EndReason != protocol.TerminalEndServerStoppedUnconfirmed || got.Error == "" {
		t.Fatalf("unconfirmed record %+v", got)
	}
}

func TestTerminalStreamLimitsAndDefaultSize(t *testing.T) {
	e, _, _ := terminalEngine(t)
	c := streamClient(t, e)
	r, err := e.command(protocol.Command{Version: 1, ID: "open", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice", TerminalSize: &protocol.TerminalSize{}})
	if err != nil {
		t.Fatal(err)
	}
	if rec := terminalRecord(t, e, r.TargetID); rec.Cols != defaultTerminalCols || rec.Rows != defaultTerminalRows {
		t.Fatalf("zero size opened %dx%d", rec.Cols, rec.Rows)
	}
	for i := range maxStreamsPerTerminal {
		s := openStream(t, c, r.TargetID, fmt.Sprint("viewer-", i))
		nextEvent(t, s, "control", isType(protocol.TerminalEventControl))
	}
	if _, err := c.OpenTerminalStream(context.Background(), r.TargetID, "one-too-many"); !errors.Is(err, client.ErrTerminalStreamLimit) {
		t.Fatalf("17th stream: %v", err)
	}
}

func TestTerminalScrollbackStaysWithinBudget(t *testing.T) {
	e, home, _ := terminalEngine(t)
	c := streamClient(t, e)
	cell := "\x1b[1;3;4;7;9;38;2;1;2;3;48;2;4;5;6ma" + strings.Repeat("́", 15)
	var b strings.Builder
	// ~95 KB per encoded line: 150 lines are well over the 8 MiB budget.
	for range 150 {
		b.WriteString(strings.Repeat(cell, 1000))
		b.WriteString("\x1b[0m\r\n")
	}
	b.WriteString("SB-DONE\r\n")
	if err := os.WriteFile(filepath.Join(home, "sb"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := e.command(protocol.Command{Version: 1, ID: "open", Kind: "terminal.open", ThreadID: "thread-shell", ClientID: "alice", TerminalSize: &protocol.TerminalSize{Cols: 1000, Rows: 5}})
	if err != nil {
		t.Fatal(err)
	}
	alice := openStream(t, c, r.TargetID, "alice")
	nextEvent(t, alice, "control", isType(protocol.TerminalEventControl))
	if err := alice.SendInput(1, []byte("cat sb\n")); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, alice, "done", screenContains("SB-DONE"))
	bob := openStream(t, c, r.TargetID, "bob")
	nextEvent(t, bob, "control", isType(protocol.TerminalEventControl))
	if err := bob.RequestScrollback(-1, 150); err != nil {
		t.Fatal(err)
	}
	sb := nextEvent(t, bob, "scrollback", isType(protocol.TerminalEventScrollback)).Scrollback
	encoded, _ := json.Marshal(protocol.TerminalEvent{Type: protocol.TerminalEventScrollback, Scrollback: sb})
	if len(encoded) > terminalFrameBudget || !sb.More || len(sb.Lines) == 0 || len(sb.Lines) >= 150 {
		t.Fatalf("scrollback of %d bytes, %d lines, more=%v", len(encoded), len(sb.Lines), sb.More)
	}
	if sb.From+len(sb.Lines) != sb.Total {
		t.Fatalf("newest lines not kept: from %d + %d lines, total %d", sb.From, len(sb.Lines), sb.Total)
	}
	// The stream stays usable afterwards.
	if err := bob.RequestScrollback(0, 1); err != nil {
		t.Fatal(err)
	}
	if first := nextEvent(t, bob, "oldest line", isType(protocol.TerminalEventScrollback)).Scrollback; first.From != 0 || len(first.Lines) != 1 || first.More {
		t.Fatalf("oldest line %+v", first)
	}
}

func TestTerminalScrollbackDegradesOversizedLine(t *testing.T) {
	row := make([]term.Cell, 300000)
	for i := range row {
		row[i] = term.Cell{Text: "á̂", Width: 1, FG: term.Color{Kind: term.ColorRGB, R: uint8(i), G: uint8(i >> 8)}}
	}
	sb := encodeScrollback([][]term.Cell{row, {{Text: "x", Width: 1}}}, 0, 2, false)
	encoded, _ := json.Marshal(protocol.TerminalEvent{Type: protocol.TerminalEventScrollback, Scrollback: &sb})
	if len(encoded) > terminalFrameBudget || !sb.Degraded || len(sb.Lines) != 2 || sb.More {
		t.Fatalf("scrollback of %d bytes degraded=%v lines=%d more=%v", len(encoded), sb.Degraded, len(sb.Lines), sb.More)
	}
}
