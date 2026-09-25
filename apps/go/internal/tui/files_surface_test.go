package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/server"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type fakeFiles struct {
	mu    sync.Mutex
	dirs  map[string][]protocol.FileEntry
	page  int
	reads map[string]protocol.FileRead
	stats map[string]protocol.FileStat
	calls []string
}

func (f *fakeFiles) record(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }

func (f *fakeFiles) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeFiles) FilesList(_ context.Context, t client.GitTarget, dir, cursor string, hidden bool) (protocol.FileList, error) {
	f.record(fmt.Sprintf("list:%s:%s:%s:%t", targetName(t), dir, cursor, hidden))
	f.mu.Lock()
	defer f.mu.Unlock()
	all, ok := f.dirs[dir]
	if !ok {
		return protocol.FileList{}, errors.New("not found")
	}
	var shown []protocol.FileEntry
	for _, e := range all {
		if hidden || !strings.HasPrefix(e.Name, ".") {
			shown = append(shown, e)
		}
	}
	start := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &start)
	}
	size := f.page
	if size == 0 {
		size = 500
	}
	end := min(len(shown), start+size)
	out := protocol.FileList{Dir: dir, Entries: shown[start:end]}
	if end < len(shown) {
		out.Next = fmt.Sprint(end)
	}
	return out, nil
}

func (f *fakeFiles) FilesRead(_ context.Context, t client.GitTarget, p string) (protocol.FileRead, error) {
	f.record("read:" + targetName(t) + ":" + p)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.reads[p]
	if !ok {
		return r, errors.New("not found")
	}
	return r, nil
}

func (f *fakeFiles) FilesStat(_ context.Context, t client.GitTarget, p string) (protocol.FileStat, error) {
	f.record("stat:" + targetName(t) + ":" + p)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats[p], nil
}

func (f *fakeFiles) setStat(p string, s protocol.FileStat) {
	f.mu.Lock()
	f.stats[p] = s
	f.mu.Unlock()
}

func dirEntry(name string) protocol.FileEntry {
	return protocol.FileEntry{Name: name, Kind: protocol.FileKindDir, Token: "d"}
}

func fileEntry(name string) protocol.FileEntry {
	return protocol.FileEntry{Name: name, Kind: protocol.FileKindFile, Token: "f"}
}

const longGoLine = "\treturn fmt.Sprintf(\"%s has a very long line that keeps going far past the width of any right host pane\", name)"

func representativeFiles() *fakeFiles {
	return &fakeFiles{
		dirs: map[string][]protocol.FileEntry{
			"":         {dirEntry("cmd"), dirEntry("internal"), fileEntry(".env"), fileEntry("README.md"), fileEntry("go.mod"), fileEntry("logo.png"), fileEntry("evil\x1b[31mname.txt")},
			"cmd":      {fileEntry("main.go")},
			"internal": {},
		},
		reads: map[string]protocol.FileRead{
			"cmd/main.go": {Path: "cmd/main.go", Kind: "text", Size: 180, Token: "t1", Encoding: "utf-8", Newline: "crlf",
				Text: "package main\r\n\r\nfunc name() string {\r\n" + longGoLine + "\r\n}\r\n"},
			"README.md":            {Path: "README.md", Kind: "text", Size: 30, Token: "t1", Encoding: "utf-8", Newline: "lf", Text: "# Title\n\nSome *markdown*.\n"},
			"logo.png":             {Path: "logo.png", Kind: "binary", Size: 2411724, Token: "t1"},
			"go.mod":               {Path: "go.mod", Kind: "text", Size: 3355443, Token: "t1", Encoding: "utf-8", Newline: "lf", Truncated: true, Text: "module x\n"},
			"evil\x1b[31mname.txt": {Path: "evil\x1b[31mname.txt", Kind: "text", Size: 20, Token: "t1", Encoding: "utf-8", Newline: "lf", Text: "a\x1b]0;pwned\x07b\x1b[2Jc\n"},
		},
		stats: map[string]protocol.FileStat{},
	}
}

// filesModel opens the Files surface on a connected model with a real-path
// checkout and a fake reader, applying the reads it starts.
func filesModel(t *testing.T, width, height int) (*Model, *fakeFiles) {
	t.Helper()
	api := representativeFiles()
	m := testModel()
	m.connected = true
	m.filesReads = api
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read")
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].Checkout = "/src/repo-" + m.snapshot.Threads[i].ID
	}
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.openSurface("files", "")
	filesSettle(t, m, nil)
	return m, api
}

// filesSettle runs cmd (or an idle update) and applies every Files result
// except poll ticks.
func filesSettle(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		_, cmd = m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	for range 6 {
		var next []tea.Cmd
		for _, msg := range pump(t, m, cmd).msgs {
			switch msg.(type) {
			case filesListMsg, filesReadMsg, filesStatMsg:
				_, c := m.Update(msg)
				next = append(next, c)
			}
		}
		if len(next) == 0 {
			return
		}
		cmd = tea.Batch(next...)
	}
}

func filesKeyPress(t *testing.T, m *Model, code rune) {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: code})
	filesSettle(t, m, cmd)
}

func filesScreen(m *Model) string { return frameText(m.compose(true)) }

func clickHit(t *testing.T, m *Model, key string) {
	t.Helper()
	for _, h := range m.measure().hits {
		if h.Key == key {
			_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
			filesSettle(t, m, cmd)
			return
		}
	}
	t.Fatalf("no hit %q", key)
}

func TestFilesTreeRendersExpandsAndSanitizes(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	screen := filesScreen(m)
	for _, want := range []string{"FILES", "Hidden files", "cmd", "internal", "README.md", "go.mod", "evilname.txt"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, ".env") || strings.Contains(strings.Join(m.compose(true).rows, ""), "\x1b[31mname") {
		t.Fatal("hidden entry shown or raw escape painted")
	}
	clickHit(t, m, "files-row:cmd")
	if api.count("list:") != 2 || !strings.Contains(filesScreen(m), "main.go") {
		t.Fatalf("expanding cmd: calls %v\n%s", api.calls, filesScreen(m))
	}
	clickHit(t, m, "files-row:internal")
	if !strings.Contains(filesScreen(m), "Empty folder") {
		t.Fatal("empty folder note missing")
	}
	// Collapse keeps the listing; expanding again does not reread.
	clickHit(t, m, "files-row:cmd")
	clickHit(t, m, "files-row:cmd")
	if api.count(":cmd:") != 1 {
		t.Fatalf("re-expansion reread: %v", api.calls)
	}
}

func TestFilesKeyboardMatchesPointer(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	m.setFocus("files-tree")
	filesKeyPress(t, m, tea.KeyDown) // first row: cmd
	v := m.currentFilesView()
	if v.cursor != "cmd" {
		t.Fatalf("cursor %q", v.cursor)
	}
	filesKeyPress(t, m, tea.KeyRight)
	if !v.expanded["cmd"] || api.count(":cmd:") != 1 {
		t.Fatal("Right did not expand")
	}
	filesKeyPress(t, m, tea.KeyDown)
	if v.cursor != "cmd/main.go" {
		t.Fatalf("cursor %q", v.cursor)
	}
	filesKeyPress(t, m, tea.KeyLeft)
	if v.cursor != "cmd" {
		t.Fatalf("Left did not move to the parent: %q", v.cursor)
	}
	filesKeyPress(t, m, tea.KeyLeft)
	if v.expanded["cmd"] {
		t.Fatal("Left did not collapse")
	}
	filesKeyPress(t, m, tea.KeyEnd)
	if !strings.HasPrefix(v.cursor, "evil") {
		t.Fatalf("End: %q", v.cursor)
	}
	filesKeyPress(t, m, tea.KeyHome)
	filesKeyPress(t, m, tea.KeyEnter)
	if !v.expanded["cmd"] {
		t.Fatal("Enter did not toggle the directory")
	}
	filesKeyPress(t, m, tea.KeyDown)
	filesKeyPress(t, m, tea.KeyEnter)
	if b := v.buffer(); b == nil || b.path != "cmd/main.go" || b.read == nil || m.focus != "files-text" {
		t.Fatalf("Enter did not open the file: %+v focus %s", b, m.focus)
	}
	// Wheel scrolls the tree under the pointer.
	m.activate(action{Kind: "files-back"})
	api.mu.Lock()
	for i := range 80 {
		api.dirs[""] = append(api.dirs[""], fileEntry(fmt.Sprintf("z%02d", i)))
	}
	api.mu.Unlock()
	filesSettle(t, m, m.activate(action{Kind: "files-refresh"}))
	f := m.measure()
	m.Update(tea.MouseWheelMsg{X: f.filesTree.X + 2, Y: f.filesTree.Y + 1, Button: tea.MouseWheelDown})
	if v.scroll != 3 {
		t.Fatalf("wheel scroll %d", v.scroll)
	}
}

func TestFilesPaginationLoadMore(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	api.page = 2
	filesSettle(t, m, m.activate(action{Kind: "files-refresh"}))
	screen := filesScreen(m)
	if !strings.Contains(screen, "Load more (2 shown)") || strings.Contains(screen, "go.mod") {
		t.Fatalf("first page:\n%s", screen)
	}
	clickHit(t, m, "files-row:more:")
	if !strings.Contains(filesScreen(m), "Load more (4 shown)") || !strings.Contains(filesScreen(m), "go.mod") {
		t.Fatalf("second page:\n%s", filesScreen(m))
	}
	m.setFocus("files-tree")
	filesKeyPress(t, m, tea.KeyEnd)
	filesKeyPress(t, m, tea.KeyEnter)
	if strings.Contains(filesScreen(m), "Load more") || len(m.currentFilesView().dirs[""].entries) != 6 {
		t.Fatalf("last page:\n%s", filesScreen(m))
	}
}

func TestFilesHiddenToggle(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	clickHit(t, m, "files-hidden")
	if !strings.Contains(filesScreen(m), ".env") || api.count(":true") == 0 {
		t.Fatalf("hidden toggle: %v", api.calls)
	}
}

func TestFilesBufferClipWrapCRLFAndSanitize(t *testing.T) {
	m, _ := filesModel(t, 144, 40)
	filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
	screen := filesScreen(m)
	if !strings.Contains(screen, "180 B · UTF-8 · CRLF") || !strings.Contains(screen, "›") || !strings.Contains(screen, "    return fmt") {
		t.Fatalf("buffer:\n%s", screen)
	}
	if strings.Contains(strings.Join(m.compose(true).rows, ""), "\r") {
		t.Fatal("CR painted")
	}
	b := m.currentFilesView().buffer()
	m.setFocus("files-text")
	filesKeyPress(t, m, tea.KeyRight)
	if b.hscroll != 8 || !strings.Contains(filesScreen(m), "‹") {
		t.Fatalf("hscroll %d", b.hscroll)
	}
	m.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	screen = filesScreen(m)
	if !b.wrap || strings.Contains(screen, "›") || !strings.Contains(screen, "width of any") {
		t.Fatalf("wrap:\n%s", screen)
	}
	filesSettle(t, m, m.openFilesBuffer("evil\x1b[31mname.txt"))
	rows := strings.Join(m.compose(true).rows, "\n")
	if strings.Contains(rows, "pwned") || strings.Contains(rows, "\x1b]0") || strings.Contains(rows, "\x1b[2J") || !strings.Contains(frameText(m.compose(true)), "abc") {
		t.Fatalf("unsanitized content:\n%s", frameText(m.compose(true)))
	}
}

func TestFilesBufferKindsMetadata(t *testing.T) {
	m, _ := filesModel(t, 144, 40)
	filesSettle(t, m, m.openFilesBuffer("logo.png"))
	if s := filesScreen(m); !strings.Contains(s, "Binary file · 2.3 MiB") {
		t.Fatalf("binary:\n%s", s)
	}
	filesSettle(t, m, m.openFilesBuffer("go.mod"))
	if s := filesScreen(m); !strings.Contains(s, "Showing first 1.0 MiB of 3.2 MiB") || !strings.Contains(s, "module x") {
		t.Fatalf("truncated:\n%s", s)
	}
	m.currentFilesView().buffers[0].read = &protocol.FileRead{Kind: "too_large", Size: 20 << 20}
	m.activate(action{Kind: "files-buf", Index: 0})
	if s := filesScreen(m); !strings.Contains(s, "Too large to show · 20.0 MiB") {
		t.Fatalf("too large:\n%s", s)
	}
}

func TestFilesBufferStripOpenCloseOverflow(t *testing.T) {
	m, api := filesModel(t, 100, 40)
	for i := range 8 {
		name := fmt.Sprintf("a-rather-long-file-name-%d.txt", i)
		api.reads[name] = protocol.FileRead{Path: name, Kind: "text", Token: "t", Text: "x"}
		filesSettle(t, m, m.openFilesBuffer(name))
	}
	v := m.currentFilesView()
	if len(v.buffers) != 8 || v.active != 7 {
		t.Fatalf("buffers %d active %d", len(v.buffers), v.active)
	}
	// Reopening an open file selects it instead of duplicating it.
	filesSettle(t, m, m.openFilesBuffer("a-rather-long-file-name-2.txt"))
	if len(v.buffers) != 8 || v.active != 2 || api.count("read::"+"a-rather-long-file-name-2.txt") > 1 {
		t.Fatal("duplicate buffer")
	}
	f := m.measure()
	var overflow bool
	for _, h := range f.hits {
		overflow = overflow || h.Key == "files-bufs"
	}
	if !overflow {
		t.Fatal("no overflow control")
	}
	m.activate(action{Kind: "files-bufs"})
	if len(m.menu) != 9 {
		t.Fatalf("overflow menu %d", len(m.menu))
	}
	m.menu = nil
	tabs := 0
	for _, s := range m.viewState().Host.Tabs {
		if s.Kind == "files" {
			tabs++
		}
	}
	if tabs != 1 {
		t.Fatalf("%d Files host tabs", tabs)
	}
	for len(v.buffers) > 0 {
		m.activate(action{Kind: "files-buf-close", Index: 0})
	}
	if v.active != -1 || !strings.Contains(filesScreen(m), "Hidden files") {
		t.Fatal("closing the last buffer did not return to the tree")
	}
}

func TestFilesStatPollOnlyWhileVisibleAndBanners(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
	if !m.filesTicking {
		t.Fatal("poll not scheduled")
	}
	api.setStat("cmd/main.go", protocol.FileStat{Kind: "file", Token: "t1"})
	filesSettle(t, m, m.filesTick())
	if api.count("stat:") != 1 || m.currentFilesView().buffer().disk != "" {
		t.Fatal("unchanged stat")
	}
	api.setStat("cmd/main.go", protocol.FileStat{Kind: "file", Token: "t2"})
	filesSettle(t, m, m.filesTick())
	s := filesScreen(m)
	if !strings.Contains(s, "Changed on disk") || !strings.Contains(s, "Reload") || !strings.Contains(s, "func name()") {
		t.Fatalf("changed banner:\n%s", s)
	}
	api.mu.Lock()
	r := api.reads["cmd/main.go"]
	r.Token, r.Text = "t2", "package main\n// new\n"
	api.reads["cmd/main.go"] = r
	api.mu.Unlock()
	filesSettle(t, m, m.activate(action{Kind: "files-reload"}))
	if s := filesScreen(m); strings.Contains(s, "Changed on disk") || !strings.Contains(s, "// new") {
		t.Fatalf("reload:\n%s", s)
	}
	api.setStat("cmd/main.go", protocol.FileStat{Kind: "absent"})
	filesSettle(t, m, m.filesTick())
	if !strings.Contains(filesScreen(m), "Deleted on disk") {
		t.Fatal("deleted banner")
	}
	// Hidden: a tick sends nothing and is not rescheduled.
	before := api.count("stat:")
	m.activate(action{Kind: "right"})
	if m.filesVisible() {
		t.Fatal("still visible")
	}
	if cmd := m.filesTick(); cmd != nil || m.nextFilesRefresh() != nil || m.filesTicking {
		t.Fatal("polling while hidden")
	}
	if api.count("stat:") != before {
		t.Fatal("stat while hidden")
	}
}

func TestFilesRestoreAcrossThreadSwitchAndDropStale(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	first := m.state.Active
	filesSettle(t, m, m.toggleFilesDir("cmd"))
	filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
	m.currentFilesView().buffer().scroll = 2
	firstKey, _ := m.filesTarget()
	// A read issued for the first thread must not land in the second.
	stale := m.loadFilesDir("internal", false)
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != first && th.State != "closed" {
			other = th.ID
			break
		}
	}
	m.activate(action{Kind: "thread", ID: other})
	m.openSurface("files", "")
	for _, msg := range pump(t, m, stale).msgs {
		m.Update(msg)
	}
	filesSettle(t, m, nil)
	key, _ := m.filesTarget()
	if key == firstKey {
		t.Fatal("thread switch kept the target")
	}
	// The late listing belongs to the first target: it lands there (so it
	// never stays Loading…) and never in the displayed one.
	if d := m.filesViews[firstKey].dirs["internal"]; d == nil || !d.loaded || d.loading {
		t.Fatal("late listing left its own target loading")
	}
	if d := m.currentFilesView().dirs["internal"]; d != nil {
		t.Fatal("late listing reached the displayed target")
	}
	if m.currentFilesView().buffer() != nil || !strings.Contains(filesScreen(m), "Hidden files") {
		t.Fatal("second thread shows the first thread's buffer")
	}
	m.activate(action{Kind: "thread", ID: first})
	filesSettle(t, m, nil)
	v := m.currentFilesView()
	if b := v.buffer(); b == nil || b.path != "cmd/main.go" || b.scroll != 2 || !v.expanded["cmd"] {
		t.Fatal("first thread's files view not restored")
	}
	_ = api
}

func TestFilesCompactColumn(t *testing.T) {
	m, _ := filesModel(t, 44, 22)
	m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
	filesSettle(t, m, nil)
	f := m.compose(true)
	for _, row := range f.rows {
		if ansi.StringWidth(row) != 44 {
			t.Fatal("row spills")
		}
	}
	if !strings.Contains(frameText(f), "README.md") {
		t.Fatalf("compact tree:\n%s", frameText(f))
	}
	filesSettle(t, m, m.openFilesBuffer("README.md"))
	f = m.compose(true)
	if !strings.Contains(frameText(f), "# Title") || strings.Contains(frameText(f), "Hidden files") {
		t.Fatalf("compact buffer:\n%s", frameText(f))
	}
	clickHit(t, m, "files-back")
	if !strings.Contains(filesScreen(m), "Hidden files") {
		t.Fatal("Back did not return to the tree")
	}
}

func TestFilesFixtureAndDisconnected(t *testing.T) {
	m := testModel()
	m.filesReads = representativeFiles()
	m.Update(tea.WindowSizeMsg{Width: 144, Height: 40})
	m.openSurface("files", "")
	if s := filesScreen(m); !strings.Contains(s, "Fixture threads have no local files") {
		t.Fatalf("fixture:\n%s", s)
	}
}

// TestFilesCaptures writes ANSI captures when TUI_GO_CAPTURE_DIR is set.
func TestFilesCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("TUI_GO_CAPTURE_DIR not set")
	}
	for _, light := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			width, height int
			setup         func(*Model, *fakeFiles)
		}{
			{"tree", 144, 40, func(m *Model, _ *fakeFiles) {
				filesSettle(t, m, m.toggleFilesDir("cmd"))
				m.currentFilesView().cursor = "cmd/main.go"
				m.setFocus("files-tree")
			}},
			{"go", 144, 40, func(m *Model, _ *fakeFiles) { filesSettle(t, m, m.openFilesBuffer("cmd/main.go")) }},
			{"markdown", 144, 40, func(m *Model, _ *fakeFiles) { filesSettle(t, m, m.openFilesBuffer("README.md")) }},
			{"binary", 144, 40, func(m *Model, _ *fakeFiles) { filesSettle(t, m, m.openFilesBuffer("logo.png")) }},
			{"changed", 144, 40, func(m *Model, api *fakeFiles) {
				filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
				api.setStat("cmd/main.go", protocol.FileStat{Kind: "file", Token: "t9"})
				filesSettle(t, m, m.filesTick())
			}},
			{"narrow", 44, 22, func(m *Model, _ *fakeFiles) {
				m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
				filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
			}},
		} {
			m, api := filesModel(t, tc.width, tc.height)
			m.state.Light = light
			tc.setup(m, api)
			name := fmt.Sprintf("%dx%d-files-%s-light%t.ansi", tc.width, tc.height, tc.name, light)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.compose(true).rows, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// repoFiles reads this repository through a real server's Files routes,
// whatever target the model asks for; changed overrides stat tokens.
type repoFiles struct {
	c       *client.Client
	target  client.GitTarget
	changed bool
}

func (r *repoFiles) FilesList(ctx context.Context, _ client.GitTarget, dir, cursor string, hidden bool) (protocol.FileList, error) {
	return r.c.FilesList(ctx, r.target, dir, cursor, hidden)
}
func (r *repoFiles) FilesRead(ctx context.Context, _ client.GitTarget, p string) (protocol.FileRead, error) {
	return r.c.FilesRead(ctx, r.target, p)
}
func (r *repoFiles) FilesStat(ctx context.Context, _ client.GitTarget, p string) (protocol.FileStat, error) {
	s, err := r.c.FilesStat(ctx, r.target, p)
	if r.changed {
		s.Token += "-changed"
	}
	return s, err
}

// TestFilesRepositoryCaptures renders this repository through a real server
// in a temporary HOME when TUI_GO_CAPTURE_DIR is set.
func TestFilesRepositoryCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("TUI_GO_CAPTURE_DIR not set")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TUI_GO_HOME", filepath.Join(home, "app"))
	repo, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, filepath.Join(home, "app")) }()
	defer func() { cancel(); <-done }()
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
	receipt, err := c.Command(ctx, protocol.Command{Version: protocol.Version, ID: "add-repo", Kind: "project.add", Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	target := client.GitTarget{ProjectID: receipt.TargetID}
	for _, light := range []bool{false, true} {
		for _, tc := range []struct {
			name          string
			width, height int
			setup         func(*Model, *repoFiles)
		}{
			{"repo-tree", 144, 40, func(m *Model, _ *repoFiles) {
				filesSettle(t, m, m.toggleFilesDir("apps"))
				filesSettle(t, m, m.toggleFilesDir("apps/go"))
				m.currentFilesView().cursor = "apps/go/Makefile"
				m.setFocus("files-tree")
			}},
			{"repo-go", 144, 40, func(m *Model, _ *repoFiles) { filesSettle(t, m, m.openFilesBuffer("apps/go/internal/tui/actions.go")) }},
			{"repo-markdown", 144, 40, func(m *Model, _ *repoFiles) { filesSettle(t, m, m.openFilesBuffer("docs/design/editor.md")) }},
			{"repo-binary", 144, 40, func(m *Model, _ *repoFiles) {
				filesSettle(t, m, m.openFilesBuffer("docs/design/visuals/palette-study.png"))
			}},
			{"repo-changed", 144, 40, func(m *Model, api *repoFiles) {
				filesSettle(t, m, m.openFilesBuffer("README.md"))
				api.changed = true
				filesSettle(t, m, m.filesTick())
			}},
			{"repo-narrow", 44, 22, func(m *Model, _ *repoFiles) {
				m.activate(action{Kind: "column", Index: int(shell.RightRegion)})
				filesSettle(t, m, m.openFilesBuffer("apps/go/internal/tui/actions.go"))
			}},
		} {
			api := &repoFiles{c: c, target: target}
			m := testModel()
			m.connected = true
			m.filesReads = api
			m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read")
			for i := range m.snapshot.Threads {
				m.snapshot.Threads[i].Checkout = repo
			}
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			m.openSurface("files", "")
			filesSettle(t, m, nil)
			tc.setup(m, api)
			name := fmt.Sprintf("%dx%d-files-%s-light%t.ansi", tc.width, tc.height, tc.name, light)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.compose(true).rows, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestFilesStatPendingClearsOnReloadRaceAndThreadSwitch(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	filesSettle(t, m, m.openFilesBuffer("cmd/main.go"))
	b := m.currentFilesView().buffer()
	// A stat in flight when Reload starts a newer generation.
	stat := m.filesTick()
	if !b.statPending {
		t.Fatal("stat not pending")
	}
	filesSettle(t, m, m.activate(action{Kind: "files-reload"}))
	for _, msg := range pump(t, m, stat).msgs {
		m.Update(msg)
	}
	if b.statPending || m.filesTick() == nil {
		t.Fatal("polling stopped after a stat raced Reload")
	}
	// A stat in flight while the thread switches.
	b.statPending = false
	stat = m.filesTick()
	first := m.state.Active
	for _, th := range m.snapshot.Threads {
		if th.ID != first && th.State != "closed" {
			m.activate(action{Kind: "thread", ID: th.ID})
			break
		}
	}
	for _, msg := range pump(t, m, stat).msgs {
		m.Update(msg)
	}
	m.activate(action{Kind: "thread", ID: first})
	m.openSurface("files", "")
	filesSettle(t, m, nil)
	if b.statPending || m.filesTick() == nil {
		t.Fatal("polling stopped after a thread switch")
	}
	_ = api
}

func TestFilesTextRowCache(t *testing.T) {
	b := &fileBuffer{read: &protocol.FileRead{Kind: "text", Text: "short\n" + strings.Repeat("x", 100) + "\n"}}
	rows, widest := b.textRows(40)
	if len(rows) != 2 || widest != 100 {
		t.Fatalf("rows %d widest %d", len(rows), widest)
	}
	if again, _ := b.textRows(20); &again[0] != &rows[0] {
		t.Fatal("unwrapped layout recomputed for a width change")
	}
	b.wrap = true
	wrapped, _ := b.textRows(40)
	if len(wrapped) != 4 {
		t.Fatalf("wrap rows %d", len(wrapped))
	}
	if resized, _ := b.textRows(50); len(resized) != 3 {
		t.Fatalf("resize rows %d", len(resized))
	}
	// Reload resets the cache.
	m, _ := filesModel(t, 144, 40)
	filesSettle(t, m, m.openFilesBuffer("README.md"))
	buf := m.currentFilesView().buffer()
	m.compose(true)
	if buf.rows == nil {
		t.Fatal("no cache after paint")
	}
	filesSettle(t, m, m.activate(action{Kind: "files-reload"}))
	if buf.rows != nil && len(buf.rows) != 3 {
		t.Fatal("stale cache after reload")
	}
}

func BenchmarkFilesBufferFrame(b *testing.B) {
	line := strings.Repeat("abc def\t", 12)
	var text strings.Builder
	for text.Len() < protocol.FileTextLimit {
		text.WriteString(line + "\n")
	}
	// "small" is a one-line file: the whole-app frame cost the 1 MiB cases
	// are compared with.
	for _, tc := range []struct {
		name string
		wrap bool
		text string
	}{{"small", false, "x\n"}, {"1MiB", false, text.String()}, {"1MiB-wrap", true, text.String()}} {
		wrap := tc.wrap
		b.Run(tc.name, func(b *testing.B) {
			m := testModel()
			m.connected = true
			api := representativeFiles()
			api.reads["big.txt"] = protocol.FileRead{Path: "big.txt", Kind: "text", Token: "t", Text: tc.text}
			m.filesReads = api
			m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read")
			for i := range m.snapshot.Threads {
				m.snapshot.Threads[i].Checkout = "/src/repo"
			}
			m.Update(tea.WindowSizeMsg{Width: 144, Height: 40})
			m.openSurface("files", "")
			cmd := m.openFilesBuffer("big.txt")
			for _, msg := range pumpBench(cmd) {
				m.Update(msg)
			}
			m.currentFilesView().buffer().wrap = wrap
			m.compose(true)
			b.ResetTimer()
			for range b.N {
				m.currentFilesView().buffer().scroll += 3
				m.compose(true)
			}
		})
	}
}

// pumpBench runs a command tree synchronously and returns its messages.
func pumpBench(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case filesReadMsg, filesListMsg:
			out = append(out, msg)
		}
	}
	return out
}

func TestFilesBoundsCopyHiddenCapabilityAndNames(t *testing.T) {
	m, api := filesModel(t, 144, 40)
	for i := range filesMaxBuffers {
		name := fmt.Sprintf("f%d", i)
		api.reads[name] = protocol.FileRead{Path: name, Kind: "text", Token: "t", Text: "x"}
		filesSettle(t, m, m.openFilesBuffer(name))
	}
	m.openFilesBuffer("README.md")
	if len(m.currentFilesView().buffers) != filesMaxBuffers || !strings.Contains(m.notice.text, "Close a file first") {
		t.Fatalf("cap: %d %q", len(m.currentFilesView().buffers), m.notice.text)
	}
	if got := clipboardSafeText("a\tb\r\nc\x1b[31md\x1b]0;t\x07e\x1b[200~f\x1b[201~\u0085g\x00"); got != "a\tb\r\ncdefg" {
		t.Fatalf("clipboard %q", got)
	}
	// Hidden toggle while offline refuses and keeps the choice.
	m.connected = false
	m.activate(action{Kind: "files-hidden"})
	if m.currentFilesView().hidden {
		t.Fatal("hidden flipped while offline")
	}
	m.connected = true
	// Unsupported names are shown but not addressable.
	api.dirs[""] = append(api.dirs[""], fileEntry("back\\slash"))
	filesSettle(t, m, m.activate(action{Kind: "files-refresh"}))
	m.activate(action{Kind: "files-back"})
	if s := filesScreen(m); !strings.Contains(s, "back\\slash · Unsupported file name") {
		t.Fatalf("unsupported name:\n%s", s)
	}
	// Without the capability the surface says so and reads nothing.
	m2 := testModel()
	m2.connected = true
	api2 := representativeFiles()
	m2.filesReads = api2
	for i := range m2.snapshot.Threads {
		m2.snapshot.Threads[i].Checkout = "/src/repo"
	}
	m2.Update(tea.WindowSizeMsg{Width: 144, Height: 40})
	m2.openSurface("files", "")
	filesSettle(t, m2, nil)
	if !strings.Contains(filesScreen(m2), "Server does not offer file") || len(api2.calls) != 0 {
		t.Fatal("capability gate")
	}
}

func TestFilesPruneDeletedThreadViews(t *testing.T) {
	m, _ := filesModel(t, 144, 40)
	key, _ := m.filesTarget()
	m.filesViews["thread:gone:/x"] = &filesView{}
	m.filesViews[key+"-old-checkout"] = &filesView{}
	m.pruneFilesViews()
	if len(m.filesViews) != 1 || m.filesViews[key] == nil {
		t.Fatalf("views %v", len(m.filesViews))
	}
}
