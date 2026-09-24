package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func viewerTestModel(t *testing.T) *Model {
	t.Helper()
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	return m
}

func activeThreadIndex(m *Model) int {
	for i, th := range m.snapshot.Threads {
		if th.ID == m.state.Active {
			return i
		}
	}
	return -1
}

func menuIndexOf(m *Model, label string) int {
	return slices.IndexFunc(m.menu, func(it menuItem) bool { return it.Label == label })
}

func screenText(m *Model) string {
	return ansi.Strip(strings.Join(m.render().rows, "\n"))
}

func longText(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d of the captured file\n", i)
	}
	return b.String()
}

func TestViewerFromComposerMenuPreservesDraftAndRestoresFocus(t *testing.T) {
	m := viewerTestModel(t)
	m.prompt.SetValue("unsent é 👩🏽‍💻")
	m.viewState().Draft = m.prompt.Value()
	atts := []protocol.Attachment{{Kind: "file", Name: "notes.md", Source: "docs/notes.md"}, {Kind: "image", Name: "shot.png"}}
	m.viewState().Attachments = slices.Clone(atts)
	m.configureInputs()
	m.setFocus("attachments")
	m.activate(action{Kind: "attachments"})
	i := menuIndexOf(m, "View notes.md")
	if i < 0 || menuIndexOf(m, "Remove notes.md") < 0 {
		t.Fatalf("menu lacks View/Remove: %+v", m.menu)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyDown})
	m.menuIndex = i
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewer == nil || m.viewer.att.Name != "notes.md" || !m.viewer.draft {
		t.Fatal("viewer did not open on the draft attachment")
	}
	text := screenText(m)
	for _, want := range []string{"notes.md", "Captured when you send", "not captured yet", "docs/notes.md", "Esc Close"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// Typing, paste and removal shortcuts never reach the composer.
	for _, k := range []tea.KeyPressMsg{{Code: 'x', Text: "x"}, {Code: tea.KeyBackspace}, {Code: tea.KeyEnter}, {Code: tea.KeyF4}} {
		m.key(k)
	}
	m.Update(tea.PasteMsg{Content: "pasted"})
	if m.viewer == nil || len(m.menu) != 0 {
		t.Fatal("key closed viewer or opened a menu")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.viewer != nil {
		t.Fatal("Esc did not close")
	}
	if m.focus != "attachments" {
		t.Fatalf("focus %q, want attachments", m.focus)
	}
	if m.prompt.Value() != "unsent é 👩🏽‍💻" || !slices.Equal(m.viewState().Attachments, atts) {
		t.Fatal("draft or attachments changed")
	}
}

func TestViewerFromQueueMenuKeepsCopy(t *testing.T) {
	m := viewerTestModel(t)
	ti := activeThreadIndex(m)
	m.snapshot.Threads[ti].Queue = []protocol.Prompt{{ID: "q1", Text: "queued", Attachments: []protocol.Attachment{{Kind: "file", Name: "a.txt", Content: "hello\nworld"}}}}
	m.configureInputs()
	m.activate(action{Kind: "queue"})
	i := menuIndexOf(m, "View · a.txt")
	if i < 0 {
		t.Fatalf("queue menu lacks View: %+v", m.menu)
	}
	m.activate(action{Kind: "menu-select", Index: i})
	if m.viewer == nil || m.viewer.att.Content != "hello\nworld" {
		t.Fatal("viewer did not open the queued capture")
	}
	m.snapshot.Threads[ti].Queue[0].Attachments[0].Content = "changed"
	if !strings.Contains(screenText(m), "world") || strings.Contains(screenText(m), "changed") {
		t.Fatal("viewer followed a later snapshot change")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != "queue" && m.focus != "prompt" {
		t.Fatalf("unexpected focus %q", m.focus)
	}
}

func TestViewerFromActivityRow(t *testing.T) {
	m := viewerTestModel(t)
	ti := activeThreadIndex(m)
	m.snapshot.Threads[ti].Activity = append(m.snapshot.Threads[ti].Activity, protocol.Activity{
		ID: "act-view", Title: "Prompt accepted", State: "completed",
		Prompt: &protocol.Prompt{ID: "p", Attachments: []protocol.Attachment{{Kind: "file", Name: "README.md", Source: "README.md", Content: "# Title\n\nSome *text*."}}},
	})
	m.activate(action{Kind: "open", Value: "activity"})
	m.viewState().DetailID = "act-view"
	m.configureInputs()
	f := m.measure()
	key := "attachment:act-view:0"
	idx := slices.IndexFunc(f.hits, func(h hit) bool { return h.Key == key })
	if idx < 0 {
		t.Fatal("no activatable attachment row")
	}
	text := screenText(m)
	if !strings.Contains(text, "file · 21 bytes") || strings.Contains(text, "Some *text*") {
		t.Fatal("activity row should show metadata, not raw content\n" + text)
	}
	// Keyboard: focus the row and activate it.
	m.setFocus(key)
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewer == nil || m.viewer.origin != key {
		t.Fatal("Enter did not open the viewer from the activity row")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != key {
		t.Fatalf("focus %q, want %q", m.focus, key)
	}
	// Pointer: click the row.
	h := m.measure().hits[slices.IndexFunc(m.measure().hits, func(h hit) bool { return h.Key == key })]
	m.mouse(tea.MouseClickMsg{X: h.Rect.X + 2, Y: h.Rect.Y, Button: tea.MouseLeft})
	if m.viewer == nil {
		t.Fatal("click did not open the viewer")
	}
	// Outside click closes without reaching what is underneath.
	m.mouse(tea.MouseClickMsg{X: 0, Y: m.height - 1, Button: tea.MouseLeft})
	if m.viewer != nil {
		t.Fatal("outside click did not close")
	}
}

func openViewer(m *Model, a protocol.Attachment) {
	m.viewer = &attachmentViewer{att: a, origin: "prompt"}
	m.setFocus("viewer-body")
}

func TestViewerMarkdownToggleOnlyForMarkdown(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.md", Content: "# Heading\n\n**bold** text"})
	if !strings.Contains(screenText(m), "# Heading") {
		t.Fatal("markdown should default to raw")
	}
	m.key(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if !m.viewer.preview || strings.Contains(screenText(m), "# Heading") || !strings.Contains(screenText(m), "Raw") {
		t.Fatal("p did not switch to preview")
	}
	m.activate(action{Kind: "viewer-mode"})
	if m.viewer.preview {
		t.Fatal("Raw control did not return to raw")
	}
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "# not markdown"})
	m.key(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.viewer.preview || hasControl(m.measure(), "viewer-mode") {
		t.Fatal("non-Markdown offered a preview")
	}
}

func TestViewerExpandRestoreGeometry(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "x"})
	def := m.viewerRect()
	if def.W >= m.width || def.H >= m.height || def.X <= 0 || def.Y <= 0 {
		t.Fatalf("default not a bounded centered size: %+v", def)
	}
	m.key(tea.KeyPressMsg{Code: 'f', Text: "f"})
	exp := m.viewerRect()
	if exp.X != 0 || exp.Y != 1 || exp.W != m.width || exp.H != m.height-2 {
		t.Fatalf("expanded %+v", exp)
	}
	f := m.measure()
	if !hasControl(f, "viewer-close") || !hasControl(f, "viewer-expand") {
		t.Fatal("close/restore unreachable when expanded")
	}
	m.activate(action{Kind: "viewer-expand"})
	if m.viewerRect() != def {
		t.Fatal("restore did not return to default size")
	}
	// Tab reaches every header control and Enter activates it.
	seen := map[string]bool{}
	for range 4 {
		m.key(tea.KeyPressMsg{Code: tea.KeyTab})
		seen[m.focus] = true
	}
	if !seen["viewer-expand"] || !seen["viewer-close"] {
		t.Fatalf("tab order %v", seen)
	}
	m.setFocus("viewer-expand")
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.viewer.expanded {
		t.Fatal("Enter on expand did nothing")
	}
}

func TestViewerScrollBounds(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "long.txt", Content: longText(300)})
	m.key(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.viewer.scroll != 0 {
		t.Fatal("scrolled above top")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
	max := m.measure().viewerMax
	if max <= 0 || m.viewer.scroll != max {
		t.Fatalf("End %d, max %d", m.viewer.scroll, max)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m.mouse(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if m.viewer.scroll != max {
		t.Fatal("scrolled past end")
	}
	if !strings.Contains(screenText(m), "line 300 of") {
		t.Fatal("last line not visible at end")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyHome})
	m.mouse(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if m.viewer.scroll != 3 {
		t.Fatalf("wheel scroll %d", m.viewer.scroll)
	}
	if !hasControl(m.measure(), "scrollbar-viewer") {
		t.Fatal("no scrollbar for overflowing content")
	}
	// Resizing keeps a valid offset.
	m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	if f := m.measure(); min(m.viewer.scroll, f.viewerMax) < 0 {
		t.Fatal("invalid offset after resize")
	}
}

func TestViewerHonestStates(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "image", Name: "shot.png", Content: "\x89PNG..."})
	if s := screenText(m); !strings.Contains(s, "Image preview unavailable") || strings.Contains(s, "PNG...") {
		t.Fatal("image state")
	}
	openViewer(m, protocol.Attachment{Kind: "file", Name: "empty.txt"})
	if s := screenText(m); !strings.Contains(s, "Empty capture") || !strings.Contains(s, "0 bytes") {
		t.Fatal("empty capture state")
	}
	openViewer(m, protocol.Attachment{Kind: "file", Name: "big.txt", Content: strings.Repeat("a", 2048)})
	if !strings.Contains(screenText(m), "2.0 KiB · 2048 bytes") {
		t.Fatal("size pair")
	}
}

func TestViewerSanitizesContent(t *testing.T) {
	for _, preview := range []bool{false, true} {
		m := viewerTestModel(t)
		openViewer(m, protocol.Attachment{Kind: "file", Name: "evil.md", Content: "a\x1b]52;c;ZXZpbA==\x07b\x1b[31mred\x1b[0m \x1b]8;;http://x\x07link\x1b]8;;\x07\x07\x08"})
		m.viewer.preview = preview
		raw := strings.Join(m.render().rows, "\n")
		for _, bad := range []string{"\x1b]52", "\x1b]8", "\x07", "\x08", "\x1b[31m"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("preview=%t leaked %q", preview, bad)
			}
		}
		if !strings.Contains(ansi.Strip(raw), "abred") {
			t.Fatal("sanitized text missing")
		}
	}
}

func TestViewerNarrowSizesDoNotPanic(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {10, 3}, {20, 5}, {39, 21}, {40, 22}, {47, 22}, {60, 24}} {
		for _, expanded := range []bool{false, true} {
			for _, name := range []string{"a.md", "a.png"} {
				m := testModel()
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				openViewer(m, protocol.Attachment{Kind: "file", Name: name, Source: "some/very/long/path/to/" + name, Content: longText(50)})
				m.viewer.expanded = expanded
				m.viewer.preview = name == "a.md"
				f := m.render()
				for i, row := range f.rows {
					if w := ansi.StringWidth(row); w != size[0] {
						t.Fatalf("%v row %d width %d", size, i, w)
					}
				}
				m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
				m.key(tea.KeyPressMsg{Code: tea.KeyTab})
				m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
				if m.viewer != nil {
					t.Fatal("Esc did not close at", size)
				}
			}
		}
	}
}

func TestViewerSelectionCopiesBodyText(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "alpha beta\ngamma"})
	f := m.measure()
	b := f.viewerBody
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseMotionMsg{X: b.X + 4, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseReleaseMsg{X: b.X + 4, Y: b.Y, Button: tea.MouseLeft})
	if m.selectedText != "alpha" {
		t.Fatalf("selected %q", m.selectedText)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.selectionLive(m.measure()) {
		t.Fatal("selection survived closing")
	}
}

func TestAttachmentViewerCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	md := "# Attachment viewer\n\nA **read-only** preview of `notes.md`.\n\n- first item\n- second item with a longer line that needs to wrap across the dialog width to show wrapping\n\n```go\nfunc main() {}\n```\n\n" + longText(40)
	for _, light := range []bool{false, true} {
		for _, scenario := range []string{"default", "expanded", "preview", "image", "draft", "activity"} {
			m := testModel()
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
			switch scenario {
			case "image":
				openViewer(m, protocol.Attachment{Kind: "image", Name: "screenshot.png", Source: "fixture://context/image", Content: "binary"})
			case "draft":
				m.viewer = &attachmentViewer{att: protocol.Attachment{Kind: "workspace-file", Name: "main.go", Source: "cmd/main.go"}, draft: true, origin: "prompt"}
				m.setFocus("viewer-close")
			case "activity":
				ti := activeThreadIndex(m)
				m.snapshot.Threads[ti].Activity = append(m.snapshot.Threads[ti].Activity, protocol.Activity{
					ID: "act-cap", Title: "Prompt accepted", State: "completed",
					Prompt: &protocol.Prompt{ID: "p", Attachments: []protocol.Attachment{{Kind: "file", Name: "notes.md", Source: "docs/notes.md", Content: md}, {Kind: "image", Name: "shot.png", Content: "x"}}},
				})
				m.activate(action{Kind: "open", Value: "activity"})
				m.viewState().DetailID = "act-cap"
				m.configureInputs()
				m.hover = "attachment:act-cap:0"
			default:
				openViewer(m, protocol.Attachment{Kind: "file", Name: "notes.md", Source: "docs/design/notes.md", Content: md})
				m.viewer.expanded = scenario == "expanded"
				m.viewer.preview = scenario == "preview"
				if scenario == "default" {
					m.hover = "viewer-mode"
				}
			}
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-viewer-%s-light%t.ansi", m.width, m.height, scenario, light)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestViewerGutterMatchesSanitizedLines(t *testing.T) {
	for _, content := range []string{"1\n2\n3\n4\n5\n6\n7\n8\n9\n\x07", "out\n\x1b[0m", strings.Repeat("x", 500) + "\n"} {
		m := viewerTestModel(t)
		openViewer(m, protocol.Attachment{Kind: "terminal-output", Name: "out", Content: content})
		want := len(fmt.Sprint(len(m.viewer.sourceLines()))) + 2
		if g := m.viewerGutter(); g != want {
			t.Fatalf("%q gutter %d want %d", content, g, want)
		}
		b := m.viewerLayout().body
		rows := m.render().rows
		for y := b.Y; y < b.Y+b.H; y++ {
			if strings.Contains(ansi.Strip(cutCells(rows[y], b.X, b.X+b.W)), "…") {
				t.Fatalf("%q: a row was truncated", content)
			}
		}
	}
}

func TestViewerRawWrapIsFaithful(t *testing.T) {
	line := "alpha beta  gamma 界界界 👩🏽‍💻 delta\tend " + strings.Repeat("word ", 40)
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: line})
	var b strings.Builder
	for _, l := range m.viewerLines(m.viewerLayout().body.W) {
		if ansi.StringWidth(l.text) > m.viewerLayout().body.W-m.viewerGutter() {
			t.Fatalf("row too wide: %q", l.text)
		}
		b.WriteString(l.text)
	}
	if b.String() != safe(line) {
		t.Fatalf("rejoined rows differ:\n%q\n%q", b.String(), safe(line))
	}
	for _, w := range []int{1, 2, 3} {
		if strings.Join(hardWrap("a界b👩🏽‍💻 c", w), "") != "a界b👩🏽‍💻 c" {
			t.Fatal("hardWrap lost characters at width", w)
		}
	}
}

func TestViewerOpenStopsDrags(t *testing.T) {
	m := viewerTestModel(t)
	m.drag, m.scrollDrag = 1, "transcript"
	m.snapshot.Threads[activeThreadIndex(m)].Queue = []protocol.Prompt{{ID: "q1", Text: "x", Attachments: []protocol.Attachment{{Kind: "file", Name: "a.txt", Content: "c"}}}}
	m.activate(action{Kind: "attachment-view", Value: "queue:a.txt", ID: "q1"})
	if m.viewer == nil || m.scrollDrag != "" || m.drag == 1 {
		t.Fatal("drag survived opening the viewer")
	}
}

func TestViewerQueueIdentityAndLabels(t *testing.T) {
	m := viewerTestModel(t)
	ti := activeThreadIndex(m)
	m.snapshot.Threads[ti].Queue = []protocol.Prompt{
		{ID: "q1", Text: "first message", Attachments: []protocol.Attachment{{Kind: "file", Name: "a.txt", Content: "A"}}},
		{ID: "q2", Text: "second message", Attachments: []protocol.Attachment{{Kind: "file", Name: "a.txt", Content: "B"}}},
	}
	m.activate(action{Kind: "queue"})
	if menuIndexOf(m, "View · a.txt · first message") < 0 || menuIndexOf(m, "View · a.txt · second message") < 0 {
		t.Fatalf("ambiguous labels: %+v", m.menu)
	}
	i := menuIndexOf(m, "View · a.txt · second message")
	// The queued item changed before activation: refuse rather than open another.
	m.snapshot.Threads[ti].Queue[1].Attachments[0].Name = "other.txt"
	m.activate(action{Kind: "menu-select", Index: i})
	if m.viewer != nil {
		t.Fatal("opened a different attachment")
	}
}

func TestViewerContextMenuRightClickCopyAndEsc(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "alpha beta"})
	f := m.measure()
	b := f.viewerBody
	// Without a selection, right-click is a no-op.
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseRight})
	if len(m.menu) != 0 || m.contextMenu != nil {
		t.Fatal("right-click without a selection opened a menu")
	}
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseMotionMsg{X: b.X + 4, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseReleaseMsg{X: b.X + 4, Y: b.Y, Button: tea.MouseLeft})
	if m.selectedText != "alpha" {
		t.Fatalf("selection %q", m.selectedText)
	}
	// Right-click on the selection opens Copy, layered over the still-open
	// viewer, and preserves the selection.
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseRight})
	if len(m.menu) == 0 || m.contextMenu == nil {
		t.Fatal("right-click on the selection did not open the context menu")
	}
	if m.viewer == nil {
		t.Fatal("opening the context menu closed the viewer")
	}
	if m.selectedText != "alpha" {
		t.Fatal("selection lost when the menu opened")
	}
	text := screenText(m)
	if !strings.Contains(text, "Copy") {
		t.Fatal("menu missing Copy")
	}
	// Esc dismisses the menu back to the viewer, not the viewer itself.
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(m.menu) != 0 || m.contextMenu != nil {
		t.Fatal("Esc did not dismiss the context menu")
	}
	if m.viewer == nil {
		t.Fatal("Esc closed the viewer instead of the menu")
	}
	if m.focus != "viewer-body" {
		t.Fatalf("focus %q, want viewer-body after Esc", m.focus)
	}
	if m.selectedText != "alpha" {
		t.Fatal("selection lost after dismissing the menu")
	}
	// Reopen through the keyboard (Shift+F10) and activate Copy.
	m.key(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	if len(m.menu) == 0 || m.contextMenu == nil {
		t.Fatal("Shift+F10 did not open the context menu in the viewer")
	}
	i := menuIndexOf(m, "Copy")
	if i < 0 {
		t.Fatal("no Copy item")
	}
	m.activate(action{Kind: "menu-select", Index: i})
	if len(m.menu) != 0 || m.contextMenu != nil {
		t.Fatal("Copy did not close the menu")
	}
	if m.viewer == nil || m.focus != "viewer-body" {
		t.Fatal("Copy did not return focus to the viewer body")
	}
}

func TestViewerRawWrapSelectionIsSourceExact(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "x"})
	room := m.viewerLayout().body.W - m.viewerGutter()
	// A line that fills exactly one wrapped row (real trailing spaces at the
	// wrap point) before continuing onto a shorter final row.
	line := strings.Repeat("a", room-3) + "   END"
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: line})
	lines := m.viewerLines(m.viewerLayout().body.W)
	if len(lines) != 2 || !lines[0].wrap || lines[1].wrap {
		t.Fatalf("expected one wrapped continuation, got %+v", lines)
	}
	f := m.measure()
	b := f.viewerBody
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseMotionMsg{X: b.X + b.W - 1, Y: b.Y + 1, Button: tea.MouseLeft})
	m.mouse(tea.MouseReleaseMsg{X: b.X + b.W - 1, Y: b.Y + 1, Button: tea.MouseLeft})
	if m.selectedText != line {
		t.Fatalf("selected %q\nwant   %q", m.selectedText, line)
	}
}

func TestViewerHardNewlineSelectionKeepsNewline(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "alpha\nbeta"})
	f := m.measure()
	b := f.viewerBody
	m.mouse(tea.MouseClickMsg{X: b.X, Y: b.Y, Button: tea.MouseLeft})
	m.mouse(tea.MouseMotionMsg{X: b.X + 3, Y: b.Y + 1, Button: tea.MouseLeft})
	m.mouse(tea.MouseReleaseMsg{X: b.X + 3, Y: b.Y + 1, Button: tea.MouseLeft})
	if m.selectedText != "alpha\nbeta" {
		t.Fatalf("selected %q", m.selectedText)
	}
}

func TestViewerBodyFocusMark(t *testing.T) {
	m := viewerTestModel(t)
	openViewer(m, protocol.Attachment{Kind: "file", Name: "a.txt", Content: "text"})
	b := m.viewerLayout().body
	row := ansi.Strip(m.render().rows[b.Y])
	if cutCells(row, b.X-1, b.X) != m.icon("focus") {
		t.Fatalf("no focus mark before the body: %q", row)
	}
}
