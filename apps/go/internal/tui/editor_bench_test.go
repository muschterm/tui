package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// benchDocModel shows a ~1 MiB document in edit mode with the cursor in its
// middle.
func benchDocModel(tb testing.TB) (*Model, *docSession, *fileBuffer) {
	tb.Helper()
	var text strings.Builder
	for i := 0; text.Len() < protocol.DocumentMaxBytes-200; i++ {
		fmt.Fprintf(&text, "\tline %6d: the quick brown fox jumps over the lazy dog, 漢字 ok\n", i)
	}
	d, state, err := doc.New(text.String())
	if err != nil {
		tb.Fatal(err)
	}
	_ = d
	m := testModel()
	m.connected = true
	m.filesReads = representativeFiles()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "files-read", docCapability)
	for i := range m.snapshot.Threads {
		m.snapshot.Threads[i].Checkout = "/src/repo-" + m.snapshot.Threads[i].ID
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	m.openSurface("files", "")
	m.activate(action{Kind: "maximize"})
	rep, err := newDocReplica(newReplicaID(), state)
	if err != nil {
		tb.Fatal(err)
	}
	s := &docSession{id: "doc-b", path: "big.txt", rep: rep, connected: true, nonce: "n", ed: docEditor{goal: -1},
		status: protocol.DocumentStatus{ID: "doc-b", State: protocol.DocumentStateSaved, Editor: m.clientID, Newline: "lf"}}
	m.docs = map[string]*docSession{s.id: s}
	v := m.currentFilesView()
	b := &fileBuffer{path: "big.txt", doc: s.id, read: &protocol.FileRead{Kind: "text"}}
	v.buffers, v.active = []*fileBuffer{b}, 0
	m.setFocus("files-text")
	m.docEdit = s.id
	mid := len(rep.txt.lines) / 2
	s.ed.cur, s.ed.anchor = edPos{mid, 10}, edPos{mid, 10}
	m.compose(true)
	m.docEnsureVisible(s, b)
	return m, s, b
}

// BenchmarkDocKeystroke1MiB types one character into a 1 MiB document and
// paints the whole frame, as one key press in the running TUI does.
func BenchmarkDocKeystroke1MiB(b *testing.B) {
	m, s, _ := benchDocModel(b)
	key := tea.KeyPressMsg{Code: 'x', Text: "x"}
	b.ResetTimer()
	for range b.N {
		m.docKey(key)
		m.compose(true)
		s.pending = s.pending[:0]
	}
}

// BenchmarkDocKeystroke1MiBWrapped is the same with soft wrap on.
func BenchmarkDocKeystroke1MiBWrapped(b *testing.B) {
	m, s, buf := benchDocModel(b)
	buf.wrap = true
	m.compose(true)
	key := tea.KeyPressMsg{Code: 'x', Text: "x"}
	b.ResetTimer()
	for range b.N {
		m.docKey(key)
		m.compose(true)
		s.pending = s.pending[:0]
	}
}

// TestDocKeystroke1MiBBudget keeps a keystroke plus a full repaint of a
// 1 MiB document under a generous bound (the target is ~5 ms; CI machines
// and -race vary).
func TestDocKeystroke1MiBBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing check")
	}
	m, s, _ := benchDocModel(t)
	key := tea.KeyPressMsg{Code: 'x', Text: "x"}
	for range 5 {
		m.docKey(key)
		m.compose(true)
	}
	const n = 50
	start := time.Now()
	for range n {
		m.docKey(key)
		m.compose(true)
		s.pending = s.pending[:0]
	}
	per := time.Since(start) / n
	t.Logf("keystroke + full frame: %v", per)
	if per > 25*time.Millisecond {
		t.Fatalf("keystroke took %v", per)
	}
	if got := s.rep.txt.String(); got != s.rep.text.ToString() {
		t.Fatal("line model diverged from the replica")
	}
}

// BenchmarkDocKeystroke1MiBEditor measures only the editor: the edit and
// painting the document pane.
func BenchmarkDocKeystroke1MiBEditor(b *testing.B) {
	m, s, buf := benchDocModel(b)
	key := tea.KeyPressMsg{Code: 'x', Text: "x"}
	full := m.compose(true)
	body := full.filesText
	rows := append([]string(nil), full.rows...)
	b.ResetTimer()
	for range b.N {
		m.docKey(key)
		f := frame{rows: append(rows[:0:0], rows...)}
		m.renderDocBody(&f, shell.Rect{X: body.X, Y: body.Y - 1, W: body.W, H: body.H + 1}, buf, s)
		s.pending = s.pending[:0]
	}
}
