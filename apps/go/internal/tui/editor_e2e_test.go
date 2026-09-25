package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/server"
)

// projectDocs sends document commands for the fixture thread's buffers to a
// real server project instead (the fixture threads are not the server's).
type projectDocs struct {
	c         *client.Client
	projectID string
}

func (p *projectDocs) Command(ctx context.Context, c protocol.Command) (protocol.Receipt, error) {
	if c.Kind == protocol.DocumentKindOpen {
		c.ThreadID, c.ProjectID = "", p.projectID
	}
	return p.c.Command(ctx, c)
}

func (p *projectDocs) DocumentVersions(ctx context.Context, id string) (protocol.DocumentVersions, error) {
	return p.c.DocumentVersions(ctx, id)
}

type realServer struct {
	c         *client.Client
	projectID string
	checkout  string
}

// startDocServer runs a real server in a temporary HOME with a temporary
// checkout registered as a project.
func startDocServer(t *testing.T, files map[string]string) *realServer {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TUI_GO_HOME", filepath.Join(home, "app"))
	// Agent runtimes must never start: the configured executables do not
	// exist, and stand-ins earlier on PATH record any launch.
	markers := filepath.Join(home, "launched")
	fakes := filepath.Join(home, "fakebin")
	for _, dir := range []string{markers, fakes} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"claude", "codex", "codex-acp", "claude-code-acp", "node", "npx"} {
		script := "#!/bin/sh\ntouch " + filepath.Join(markers, name) + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(fakes, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(agent.EnvClaudeRuntime, filepath.Join(home, "missing", "claude"))
	t.Setenv(agent.EnvCodexRuntime, filepath.Join(home, "missing", "codex"))
	t.Cleanup(func() {
		if launched, _ := os.ReadDir(markers); len(launched) > 0 {
			t.Errorf("agent runtimes were started: %v", launched)
		}
	})
	checkout := filepath.Join(home, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, filepath.Join(home, "app")) }()
	t.Cleanup(func() { cancel(); <-done })
	var c *client.Client
	for range 500 {
		if d, err := client.Discover(filepath.Join(home, "app")); err == nil {
			c = client.New(d)
			if _, err := c.Snapshot(ctx); err == nil {
				break
			}
			c = nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("server did not start")
	}
	snap, _ := c.Snapshot(ctx)
	found := false
	for _, cap := range snap.Capabilities {
		found = found || cap == docCapability
	}
	if !found {
		t.Skip("server does not offer shared documents on this platform")
	}
	r, err := c.Command(ctx, protocol.Command{Version: protocol.Version, ID: client.ID(), Kind: "project.add", Path: checkout})
	if err != nil {
		t.Fatal(err)
	}
	return &realServer{c: c, projectID: r.TargetID, checkout: checkout}
}

// realDocModel is a model of client clientID attached to rs.
func realDocModel(t *testing.T, rs *realServer, clientID string, width, height int) *docHarness {
	t.Helper()
	m := testModel()
	m.clientID = clientID
	m.connected = true
	m.ctx = context.Background()
	m.filesReads = &repoFiles{c: rs.c, target: client.GitTarget{ProjectID: rs.projectID}}
	m.docAPIs = &projectDocs{c: rs.c, projectID: rs.projectID}
	c := rs.c
	m.docDial = func(ctx context.Context, id, clientID string, replica uint64) (docConn, error) {
		s, err := c.OpenDocumentStream(ctx, id, clientID, replica)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	m.clipboardWrite = func(string) error { return nil }
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read", docCapability)
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].Checkout = rs.checkout
	}
	h := &docHarness{t: t, m: m, msgs: make(chan tea.Msg, 4096)}
	h.do(tea.WindowSizeMsg{Width: width, Height: height})
	m.openSurface("files", "")
	h.key(tea.KeyF7, 0)
	h.settle()
	return h
}

// until settles h until cond holds or the deadline passes.
func (h *docHarness) until(what string, d time.Duration, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s:\n%s", what, h.screen())
		}
		h.settle()
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDocEditsReachDiskThroughRealServer opens a file through a real server,
// types into it and waits for autosave to write it; a second client sees
// the edit live and is offered Take over; an overlapping external change
// pauses autosave and Review › Keep mine writes the document.
func TestDocEditsReachDiskThroughRealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a server")
	}
	rs := startDocServer(t, map[string]string{"notes.txt": "hello\nsecond line\n"})
	path := filepath.Join(rs.checkout, "notes.txt")
	h := realDocModel(t, rs, "client-a", 200, 44)
	h.cmd(h.m.openFilesBuffer("notes.txt"))
	h.until("document state", 5*time.Second, func() bool { s := h.session(); return s != nil && s.rep != nil })
	h.edit()
	h.typeText("big ")
	h.until("autosave", 10*time.Second, func() bool { return readFile(t, path) == "big hello\nsecond line\n" })
	h.until("Saved", 5*time.Second, func() bool { _, text := h.m.docStatusLine(h.session()); return text == "Saved" })

	// A second client joins the same document and edits at the same time;
	// both see each other's cursors, and each undoes only its own typing.
	o := realDocModel(t, rs, "client-b", 200, 44)
	o.cmd(o.m.openFilesBuffer("notes.txt"))
	o.until("second state", 5*time.Second, func() bool { s := o.session(); return s != nil && s.rep != nil })
	if o.session().id != h.session().id || !o.session().status.Collaborative {
		t.Fatalf("second client: same document %v collaborative %v", o.session().id == h.session().id, o.session().status.Collaborative)
	}
	h.typeText("!")
	o.until("live update", 5*time.Second, func() bool { return strings.Contains(o.session().rep.txt.String(), "big !hello") })
	o.edit()
	o.key(tea.KeyDown, 0)
	o.key(tea.KeyEnd, 0)
	for i, r := range "abc" {
		h.typeText(string(rune('A' + i)))
		o.typeText(string(r))
	}
	converged := func() bool {
		a, b := h.session().rep.txt.String(), o.session().rep.txt.String()
		return a == b && a == "big !ABChello\nsecond lineabc\n"
	}
	for deadline := time.Now().Add(10 * time.Second); !converged(); h.settle() {
		o.settle()
		if time.Now().After(deadline) {
			t.Fatalf("no convergence: a %q b %q", h.session().rep.txt.String(), o.session().rep.txt.String())
		}
	}
	h.until("peer cursor", 5*time.Second, func() bool {
		peers := h.m.livePeers(h.session())
		return len(peers) == 1 && peers[0].client == "client-b" && peers[0].head == (edPos{1, len("second lineabc")})
	})
	if !strings.Contains(h.screen(), "1 other editing") {
		t.Fatalf("no peers list:\n%s", h.screen())
	}
	// client-b undoes its own run; client-a's concurrent typing stays.
	o.key('z', tea.ModCtrl)
	h.until("undo own only", 10*time.Second, func() bool {
		return h.session().rep.txt.String() == "big !ABChello\nsecond line\n"
	})
	h.until("autosave 2", 10*time.Second, func() bool { return readFile(t, path) == "big !ABChello\nsecond line\n" })
	// Leaving edit mode clears client-b's cursor for client-a.
	o.key(tea.KeyEscape, 0)
	h.until("peer cleared", 5*time.Second, func() bool { return len(h.m.livePeers(h.session())) == 0 })

	// Overlapping edits: client-a types on line 2 while the file's line 2
	// changes on disk before autosave runs.
	h.key(tea.KeyDown, 0)
	h.key(tea.KeyEnd, 0)
	h.typeText(" mine")
	if err := os.WriteFile(path, []byte("big !ABChello\nsecond line theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.until("paused", 10*time.Second, func() bool {
		return h.session().status.State == protocol.DocumentStatePausedConflict
	})
	if !strings.Contains(h.screen(), "Paused · changed on disk") {
		t.Fatalf("no paused state:\n%s", h.screen())
	}
	h.click("doc-review")
	h.until("versions", 5*time.Second, func() bool { return h.m.docReview != nil && h.m.docReview.versions != nil })
	screen := h.screen()
	for _, want := range []string{"-  second line theirs", "+  second line mine"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("review missing %q:\n%s", want, screen)
		}
	}
	h.click("doc-review-keep")
	h.choose("Keep mine")
	h.until("resolved", 10*time.Second, func() bool { return readFile(t, path) == "big !ABChello\nsecond line mine\n" })
	h.until("saved after resolve", 10*time.Second, func() bool { _, text := h.m.docStatusLine(h.session()); return text == "Saved" })
	// The server probed its agents without starting any runtime.
	snap, err := rs.c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snap.Agents {
		if a.Kind == agent.KindACP && a.State == agent.StateReady {
			t.Fatalf("agent %s became ready: %s", a.ID, a.Detail)
		}
	}
}
