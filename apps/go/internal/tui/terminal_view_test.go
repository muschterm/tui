package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// fakeTermConn is a scripted terminal stream. Writes arrive on the model's
// writer goroutine, so they are guarded and read with waitWrites.
type fakeTermConn struct {
	events chan protocol.TerminalEvent

	mu          sync.Mutex
	keys        []protocol.TerminalKey
	keyGens     []int64
	pastes      []string
	resizes     [][2]int
	scrollbacks [][2]int
	closed      bool
	closeOnce   sync.Once
}

func newFakeTermConn() *fakeTermConn {
	return &fakeTermConn{events: make(chan protocol.TerminalEvent, 64)}
}

func (c *fakeTermConn) Events() <-chan protocol.TerminalEvent { return c.events }
func (c *fakeTermConn) Err() error                            { return nil }
func (c *fakeTermConn) SendKey(gen int64, k protocol.TerminalKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys, c.keyGens = append(c.keys, k), append(c.keyGens, gen)
	return nil
}
func (c *fakeTermConn) Paste(_ int64, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pastes = append(c.pastes, text)
	return nil
}
func (c *fakeTermConn) Resize(_ int64, cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resizes = append(c.resizes, [2]int{cols, rows})
	return nil
}
func (c *fakeTermConn) RequestScrollback(from, n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scrollbacks = append(c.scrollbacks, [2]int{from, n})
	return nil
}
func (c *fakeTermConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.events) })
	return nil
}
func (c *fakeTermConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// termHarness is a model showing one live bottom terminal backed by fakes.
type termHarness struct {
	t     *testing.T
	m     *Model
	mu    sync.Mutex
	conns []*fakeTermConn
	msgs  chan tea.Msg
}

func terminalRecordFor(id string) protocol.Terminal {
	return protocol.Terminal{ID: id, ThreadID: "thread-shell", State: protocol.TerminalStateRunning, Controller: "test", ControlGen: 1, Revision: 1}
}

func newTermHarness(t *testing.T, records ...protocol.Terminal) *termHarness {
	t.Helper()
	h := &termHarness{t: t, m: testModel(), msgs: make(chan tea.Msg, 1024)}
	// Commands fail fast against an unreachable server instead of a nil client.
	h.m.client = client.New(protocol.Discovery{Version: protocol.Version, URL: "http://127.0.0.1:1", Token: "test"})
	h.m.width, h.m.height = 160, 45
	h.m.colorProfile = colorprofile.TrueColor
	if len(records) == 0 {
		records = []protocol.Terminal{terminalRecordFor("term-1")}
	}
	h.m.snapshot.Terminals = records
	h.m.termDial = func(context.Context, string, string) (terminalConn, error) {
		c := newFakeTermConn()
		h.mu.Lock()
		h.conns = append(h.conns, c)
		h.mu.Unlock()
		return c, nil
	}
	v := h.m.viewState()
	for _, r := range records {
		openTerminalTab(&v.Bottom, r.ID)
	}
	h.m.state.Layout.Bottom = true
	h.m.configureInputs()
	return h
}

func (h *termHarness) conn(i int) *fakeTermConn {
	h.t.Helper()
	waitFor(h.t, "stream dial", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.conns) > i })
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[i]
}

// launch runs cmd (and batches) in the background, collecting terminal
// messages; other messages (ticks, notices, redraws) are dropped.
func (h *termHarness) launch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				h.launch(c)
			}
		case termDialedMsg, termEventMsg, termClosedMsg, termRetryMsg, termResizeMsg:
			h.msgs <- msg
		}
	}()
}

// update applies msg and then pumps terminal messages until quiet.
func (h *termHarness) update(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.launch(cmd)
	h.pump()
}

func (h *termHarness) pump() {
	for {
		select {
		case msg := <-h.msgs:
			_, cmd := h.m.Update(msg)
			h.launch(cmd)
		case <-time.After(150 * time.Millisecond):
			return
		}
	}
}

// send delivers a server event on stream i and pumps.
func (h *termHarness) send(i int, ev protocol.TerminalEvent) {
	h.conn(i).events <- ev
	h.pump()
}

func textLine(s string) protocol.TerminalLine {
	return protocol.TerminalLine{{Text: s, Width: 1}}
}

func screenOf(cols, rows int, lines ...protocol.TerminalLine) *protocol.TerminalScreen {
	scr := &protocol.TerminalScreen{Seq: 1, Cols: cols, Rows: rows, Cursor: protocol.TerminalCursor{Visible: true}}
	for i := range rows {
		if i < len(lines) {
			scr.Lines = append(scr.Lines, lines[i])
		} else {
			scr.Lines = append(scr.Lines, textLine(strings.Repeat(" ", cols)))
		}
	}
	return scr
}

// start connects the harness's terminal and delivers a screen sized to the
// pane plus control for controller.
func (h *termHarness) start(controller string, lines ...protocol.TerminalLine) (grid termPane) {
	h.t.Helper()
	h.update(tea.WindowSizeMsg{Width: h.m.width, Height: h.m.height})
	f := h.m.measure()
	if len(f.terms) != 1 {
		h.t.Fatalf("expected one terminal pane, got %+v", f.terms)
	}
	grid = f.terms[0]
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screenOf(grid.grid.W, grid.grid.H, lines...)})
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventControl, Control: &protocol.TerminalControl{Controller: controller, Gen: 1}})
	return grid
}

func (h *termHarness) screenText() string {
	f := h.m.render()
	return ansi.Strip(strings.Join(f.rows, "\n"))
}

func TestTerminalKeyMapping(t *testing.T) {
	for _, tc := range []struct {
		key  tea.KeyPressMsg
		want protocol.TerminalKey
	}{
		{tea.KeyPressMsg{Code: 'a', Text: "a"}, protocol.TerminalKey{Code: "a", Text: "a"}},
		{tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift, ShiftedCode: 'A'}, protocol.TerminalKey{Code: "a", Text: "A", Mods: protocol.TerminalModShift}},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, protocol.TerminalKey{Code: "c", Mods: protocol.TerminalModCtrl}},
		{tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}, protocol.TerminalKey{Code: "x", Mods: protocol.TerminalModAlt}},
		{tea.KeyPressMsg{Code: 'a', Text: "a", Mod: tea.ModCapsLock}, protocol.TerminalKey{Code: "a", Text: "a"}},
		{tea.KeyPressMsg{Code: tea.KeyUp}, protocol.TerminalKey{Code: "up"}},
		{tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}, protocol.TerminalKey{Code: "left", Mods: protocol.TerminalModCtrl}},
		{tea.KeyPressMsg{Code: tea.KeyF1}, protocol.TerminalKey{Code: "f1"}},
		{tea.KeyPressMsg{Code: tea.KeyF12, Mod: tea.ModShift}, protocol.TerminalKey{Code: "f12", Mods: protocol.TerminalModShift}},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, protocol.TerminalKey{Code: "enter"}},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, protocol.TerminalKey{Code: "tab", Mods: protocol.TerminalModShift}},
		{tea.KeyPressMsg{Code: tea.KeyBackspace}, protocol.TerminalKey{Code: "backspace"}},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, protocol.TerminalKey{Code: "escape"}},
		{tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, protocol.TerminalKey{Code: "space"}},
		{tea.KeyPressMsg{Code: tea.KeyPgDown}, protocol.TerminalKey{Code: "pgdown"}},
		{tea.KeyPressMsg{Code: tea.KeyDelete}, protocol.TerminalKey{Code: "delete"}},
		{tea.KeyPressMsg{Code: 'é', Text: "é"}, protocol.TerminalKey{Code: "é", Text: "é"}},
		{tea.KeyPressMsg{Code: tea.KeyExtended, Text: "日本"}, protocol.TerminalKey{Code: "日", Text: "日本"}},
	} {
		got, ok := terminalKeyFor(tc.key)
		if !ok || got != tc.want {
			t.Errorf("terminalKeyFor(%v) = %+v, %v; want %+v", tc.key, got, ok, tc.want)
		}
	}
	for _, k := range []tea.KeyPressMsg{
		{Code: 'c', Mod: tea.ModSuper},
		{Code: tea.KeyF13},
		{Code: tea.KeyMediaPlay},
		{Code: 'a', Text: "\x1b[2J"},
	} {
		if got, ok := terminalKeyFor(k); ok {
			t.Errorf("terminalKeyFor(%v) = %+v, want unsupported", k, got)
		}
	}
	if !isTerminalEscape(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}) || isTerminalEscape(tea.KeyPressMsg{Code: ']', Text: "]"}) {
		t.Fatal("Ctrl+] detection")
	}
}

func TestTerminalRowPaintsCellsAtServerWidths(t *testing.T) {
	m := testModel()
	m.colorProfile = colorprofile.TrueColor
	line := protocol.TerminalLine{
		{Text: "ab", Width: 1, FG: protocol.TerminalIndexed(1), Attrs: protocol.TerminalAttrBold},
		{Text: "界", Width: 2, FG: protocol.TerminalRGB(255, 0, 0)},
		{Text: "é", Width: 1, Cluster: true, Attrs: protocol.TerminalAttrUnderline},
		{Text: "👍🏽", Width: 2, Cluster: true},
		{Text: "\x1b[31mx", Width: 1},
		{Text: "z", Width: 1, BG: protocol.TerminalIndexed(200), Attrs: protocol.TerminalAttrReverse | protocol.TerminalAttrBlink},
	}
	row := m.terminalRow(line, 14, -1, false)
	if got := ansi.StringWidth(row); got != 14 {
		t.Fatalf("row is %d cells, want 14: %q", got, row)
	}
	// The child's ESC is replaced; the rest of its bytes are inert text.
	if plain := ansi.Strip(row); plain != "ab界e\u0301👍🏽�[31mxz" {
		t.Fatalf("row text %q", plain)
	}
	for _, want := range []string{"\x1b[31;", "38;2;255;0;0", ";4m", ";1m"} {
		if !strings.Contains(row, want) && !strings.Contains(row, strings.TrimSuffix(want, ";")) {
			t.Errorf("row lacks %q: %q", want, row)
		}
	}
	if strings.Contains(row, "\x1b[31mx") || strings.Contains(row, "[5m") || strings.Contains(row, ";5;") && !strings.Contains(row, "38;5;") {
		t.Fatalf("child escape or blink reached the row: %q", row)
	}
	// Reverse swaps the explicit colours: palette 200 becomes the foreground.
	if !strings.Contains(row, "38;5;200") {
		t.Fatalf("reverse did not swap colours: %q", row)
	}
	// A wide grapheme clipped by the edge becomes blanks.
	if got := ansi.Strip(m.terminalRow(protocol.TerminalLine{{Text: "a界", Width: 1}, {Text: "界", Width: 2}}, 2, -1, false)); ansi.StringWidth(got) != 2 {
		t.Fatalf("clipped row %q", got)
	}
	// The cursor cell is inverted, including past the last run.
	p := m.colors()
	text := terminalColor("", p.text)
	cursor := m.terminalRow(textLine("ab"), 5, 3, false)
	if ansi.StringWidth(cursor) != 5 || !strings.Contains(cursor, ansi.Style{}.BackgroundColor(text).String()[2:]) {
		t.Fatalf("cursor not painted: %q", cursor)
	}
}

func TestTerminalCellTextSanitizes(t *testing.T) {
	for in, want := range map[string]string{"\x1b": "�", "\u009b": "�", "\u202e": "�", "\x07": "�", "a": "a", "": " "} {
		if got := terminalCellText(in, 1); got != want {
			t.Errorf("terminalCellText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := terminalCellText("界", 1); ansi.StringWidth(got) != 1 {
		t.Fatalf("width-1 cell for a wide rune is %q", got)
	}
}

func TestTerminalStreamPaintsFocusesAndSendsInput(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", protocol.TerminalLine{{Text: "$ hello", Width: 1, FG: protocol.TerminalIndexed(2)}})
	if !strings.Contains(h.screenText(), "$ hello") {
		t.Fatal("screen not painted")
	}
	// Entering focus by clicking the grid; the status row shows the escape.
	h.update(tea.MouseClickMsg{X: pane.grid.X + 1, Y: pane.grid.Y + 1, Button: tea.MouseLeft})
	if h.m.termFocus != "term-1" || h.m.focus != "terminal:term-1" {
		t.Fatalf("click did not focus the terminal: %q %q", h.m.termFocus, h.m.focus)
	}
	if row := ansi.Strip(h.m.render().rows[pane.grid.Y-1]); !strings.Contains(row, "Ctrl+] to leave") {
		t.Fatalf("status row lacks the escape hint: %q", row)
	}
	// App shortcuts go to the shell: F2 does not toggle navigation.
	left := h.m.state.Layout.Left
	for _, k := range []tea.KeyPressMsg{{Code: 'l', Text: "l"}, {Code: 's', Text: "s"}, {Code: tea.KeyEnter}, {Code: 'c', Mod: tea.ModCtrl}, {Code: tea.KeyF2}, {Code: 'q', Mod: tea.ModCtrl}} {
		h.update(k)
	}
	if h.m.state.Layout.Left != left {
		t.Fatal("F2 reached the app while typing into the terminal")
	}
	c := h.conn(0)
	waitFor(t, "keys", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.keys) == 6 })
	c.mu.Lock()
	if c.keys[0].Code != "l" || c.keys[2].Code != "enter" || c.keys[3] != (protocol.TerminalKey{Code: "c", Mods: protocol.TerminalModCtrl}) || c.keys[4].Code != "f2" || c.keyGens[0] != 1 {
		t.Fatalf("keys %+v", c.keys)
	}
	c.mu.Unlock()
	h.update(tea.PasteMsg{Content: "echo pasted\n"})
	waitFor(t, "paste", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.pastes) == 1 && c.pastes[0] == "echo pasted\n"
	})
	if h.m.prompt.Value() != "" {
		t.Fatal("paste reached the prompt")
	}
	// Ctrl+] leaves; the next key is the app's again.
	h.update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if h.m.termFocus != "" {
		t.Fatal("Ctrl+] did not leave terminal focus")
	}
	h.update(tea.KeyPressMsg{Code: tea.KeyF2})
	if h.m.state.Layout.Left == left {
		t.Fatal("F2 did not reach the app after leaving")
	}
	c.mu.Lock()
	if len(c.keys) != 6 {
		t.Fatalf("keys after leaving reached the shell: %+v", c.keys)
	}
	c.mu.Unlock()
	// Enter on the focused pane re-enters; a click elsewhere leaves.
	h.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.m.termFocus != "term-1" {
		t.Fatal("Enter did not re-enter terminal focus")
	}
	f := h.m.measure()
	h.update(tea.MouseClickMsg{X: f.transcript.X, Y: f.transcript.Y, Button: tea.MouseLeft})
	if h.m.termFocus != "" {
		t.Fatal("clicking elsewhere did not leave terminal focus")
	}
}

func TestTerminalCursorShownOnlyWhileTyping(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", textLine("$ "))
	h.m.terms["term-1"].screen.Cursor = protocol.TerminalCursor{X: 2, Y: 0, Visible: true}
	p := h.m.colors()
	inverted := ansi.Style{}.ForegroundColor(lipColor(p.panel)).BackgroundColor(lipColor(p.text)).String()
	inverted = inverted[2 : len(inverted)-1]
	if row := h.m.render().rows[pane.grid.Y]; strings.Contains(row, inverted) {
		t.Fatal("cursor shown without input focus")
	}
	h.m.activate(action{Kind: "terminal-focus", ID: "term-1"})
	if row := h.m.render().rows[pane.grid.Y]; !strings.Contains(row, inverted) {
		t.Fatalf("cursor not shown while typing: %q", row)
	}
}

func TestTerminalObserverTakesControlAndResizes(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("other-client", textLine("remote"))
	// Observers never resize, even when the grid differs.
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screenOf(80, 24, textLine("remote"))})
	h.pump()
	c := h.conn(0)
	c.mu.Lock()
	if len(c.resizes) != 0 {
		t.Fatalf("observer resized: %v", c.resizes)
	}
	c.mu.Unlock()
	row := ansi.Strip(h.m.render().rows[pane.grid.Y-1])
	if !strings.Contains(row, "Observing") || !strings.Contains(row, "Take control") || !strings.Contains(row, "size 80×24") {
		t.Fatalf("observer status row %q", row)
	}
	// Focus is refused with the explanation.
	h.m.activate(action{Kind: "terminal-focus", ID: "term-1"})
	if h.m.termFocus != "" || !strings.Contains(h.m.notice.text, "Another client controls this terminal") {
		t.Fatalf("observer focus not refused: %q", h.m.notice.text)
	}
	// Take control is keyboard reachable and sends the command.
	f := h.m.render()
	take := controlHit(t, f, "terminal-action:term-1")
	if take.Action.Kind != "terminal-take-control" {
		t.Fatalf("status action %+v", take.Action)
	}
	h.m.setFocus(take.Key)
	h.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.m.busy == nil || h.m.busy.Kind != "terminal.take-control" || h.m.busy.TargetID != "term-1" || h.m.busy.ClientID != "test" {
		t.Fatalf("take control command %+v", h.m.busy)
	}
	// Once control arrives, the controller resizes to its pane (debounced).
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventControl, Control: &protocol.TerminalControl{Controller: "test", Gen: 2}})
	waitFor(t, "resize", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.resizes) == 1 })
	c.mu.Lock()
	if c.resizes[0] != [2]int{pane.grid.W, pane.grid.H} {
		t.Fatalf("resize %v, want %dx%d", c.resizes, pane.grid.W, pane.grid.H)
	}
	c.mu.Unlock()
	// Several pane changes within the debounce send only the last size.
	h.m.state.Layout.BottomHeight++
	h.update(tea.WindowSizeMsg{Width: 158, Height: 45})
	h.m.state.Layout.BottomHeight++
	h.update(tea.WindowSizeMsg{Width: 156, Height: 45})
	final := h.m.measure().terms[0].grid
	waitFor(t, "second resize", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.resizes) >= 2 })
	h.pump()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last := c.resizes[len(c.resizes)-1]; last != [2]int{final.W, final.H} || len(c.resizes) > 3 {
		t.Fatalf("resizes %v, want last %dx%d", c.resizes, final.W, final.H)
	}
}

func TestTerminalRejectedNoticesCoalesce(t *testing.T) {
	h := newTermHarness(t)
	h.start("test")
	rejected := protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: protocol.TerminalRejectNotController, For: protocol.TerminalRequestKey}}
	h.send(0, rejected)
	gen := h.m.notice.generation
	h.send(0, rejected)
	h.send(0, rejected)
	if h.m.notice.generation != gen || !strings.Contains(h.m.notice.text, "Take control") {
		t.Fatalf("rejections not coalesced: %d→%d %q", gen, h.m.notice.generation, h.m.notice.text)
	}
	// Losing control while typing leaves input focus.
	h.m.activate(action{Kind: "terminal-focus", ID: "term-1"})
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventControl, Control: &protocol.TerminalControl{Controller: "other", Gen: 2}})
	if h.m.termFocus != "" {
		t.Fatal("focus kept after losing control")
	}
}

func TestTerminalLifecycleRendering(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", textLine("final output"))
	status := func() string { return ansi.Strip(h.m.render().rows[pane.grid.Y-1]) }

	h.m.snapshot.Terminals[0].State = protocol.TerminalStateClosing
	if !strings.Contains(status(), "Closing…") {
		t.Fatalf("closing status %q", status())
	}
	h.m.snapshot.Terminals[0].State = protocol.TerminalStateCloseUncertain
	h.m.snapshot.Terminals[0].Error = "exit not yet confirmed"
	if s := status(); !strings.Contains(s, "Close not confirmed · exit not yet confirmed") || !strings.Contains(s, "Retry close") {
		t.Fatalf("uncertain status %q", s)
	}
	clickControl(h.m, controlHit(t, h.m.render(), "terminal-action:term-1"))
	if h.m.busy == nil || h.m.busy.Kind != "terminal.close" || h.m.busy.TargetID != "term-1" {
		t.Fatalf("retry close %+v", h.m.busy)
	}
	h.m.busy, h.m.state.Pending = nil, nil

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventEnded, Ended: &protocol.TerminalEnded{Reason: protocol.TerminalEndExited, Exit: &protocol.TerminalExit{Code: 3}}})
	h.conn(0).Close() // the server closes the stream after ended
	h.pump()
	h.m.snapshot.Terminals[0].State = protocol.TerminalStateEnded
	if s := status(); !strings.Contains(s, "Ended · exit 3") {
		t.Fatalf("ended status %q", s)
	}
	if row := h.m.render().rows[pane.grid.Y]; !strings.Contains(ansi.Strip(row), "final output") || !strings.Contains(row, ";2;") && !strings.Contains(row, "[2;") {
		t.Fatalf("ended screen not kept dimmed: %q", row)
	}
	h.m.activate(action{Kind: "terminal-focus", ID: "term-1"})
	if h.m.termFocus != "" || !strings.Contains(h.m.notice.text, "not running") {
		t.Fatal("ended terminal accepted input focus")
	}
	for _, tc := range []struct {
		reason string
		exit   *protocol.TerminalExit
		want   string
	}{
		{protocol.TerminalEndServerStopped, nil, "Ended · server stopped"},
		{protocol.TerminalEndServerRestarted, nil, "Ended · server restarted"},
		{protocol.TerminalEndClosed, &protocol.TerminalExit{Code: -1, Killed: true}, "Ended · killed"},
		{protocol.TerminalEndClosed, &protocol.TerminalExit{Code: -1, Signal: "SIGHUP"}, "Ended · SIGHUP"},
	} {
		if got := terminalEndLabel(tc.reason, tc.exit); got != tc.want {
			t.Errorf("terminalEndLabel(%s) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	// An ended record never reconnects after its stream closes.
	waitFor(t, "stream closed after ended", func() bool { return h.m.terms["term-1"].conn == nil })
	h.update(tea.WindowSizeMsg{Width: 160, Height: 45})
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.conns) != 1 {
		t.Fatalf("ended terminal redialed: %d streams", len(h.conns))
	}
}

func TestTerminalStreamsFollowVisibility(t *testing.T) {
	h := newTermHarness(t)
	h.start("test", textLine("cached screen"))
	tv := h.m.terms["term-1"]
	oldGen := tv.gen
	// Hiding the panel closes the stream but keeps the last screen.
	h.update(tea.KeyPressMsg{Code: tea.KeyF5})
	c := h.conn(0)
	waitFor(t, "stream closed on hide", c.isClosed)
	if tv.conn != nil || tv.screen == nil {
		t.Fatal("hidden terminal kept its stream or dropped its screen")
	}
	// A late event from the closed stream is stale.
	h.update(termEventMsg{id: "term-1", gen: oldGen, ev: protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screenOf(10, 2, textLine("stale"))}})
	if strings.Contains(ansi.Strip(tv.screen.Lines[0][0].Text), "stale") {
		t.Fatal("stale event applied")
	}
	// Showing it again shows the cached screen at once and redials.
	h.update(tea.KeyPressMsg{Code: tea.KeyF5})
	if !strings.Contains(h.screenText(), "cached screen") {
		t.Fatal("cached screen not shown on re-show")
	}
	h.conn(1)
	// A vanished record drops its view and stream.
	h.m.snapshot.Terminals = nil
	h.update(snapshotMsg(h.m.snapshot))
	if _, ok := h.m.terms["term-1"]; ok {
		t.Fatal("view of a vanished terminal kept")
	}
	waitFor(t, "stream closed on removal", h.conn(1).isClosed)
	// Quitting closes every stream.
	h2 := newTermHarness(t)
	h2.start("test")
	h2.m.quit()
	waitFor(t, "stream closed on quit", h2.conn(0).isClosed)
}

func TestTerminalReconnectsAfterUnexpectedEnd(t *testing.T) {
	h := newTermHarness(t)
	h.start("test")
	h.conn(0).Close()
	h.pump()
	if tv := h.m.terms["term-1"]; tv.conn != nil || !tv.retry {
		t.Fatalf("no reconnect scheduled: %+v", tv)
	}
	waitFor(t, "redial", func() bool {
		h.pump()
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.conns) == 2
	})
}

func TestTerminalHistoryScrolling(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", textLine("live"))
	h.update(tea.MouseWheelMsg{X: pane.grid.X, Y: pane.grid.Y, Button: tea.MouseWheelUp})
	c := h.conn(0)
	waitFor(t, "scrollback request", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.scrollbacks) == 1 })
	var lines []protocol.TerminalLine
	for i := range 10 {
		lines = append(lines, textLine("old-"+string(rune('0'+i))))
	}
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventScrollback, Scrollback: &protocol.TerminalScrollback{From: 0, Total: 10, Lines: lines}})
	text := h.screenText()
	if !strings.Contains(text, "old-9") || !strings.Contains(text, "History · 3 lines up") {
		t.Fatalf("history not shown:\n%s", text)
	}
	// Typing returns to the live screen.
	h.m.activate(action{Kind: "terminal-focus", ID: "term-1"})
	h.update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if h.m.terms["term-1"].scroll != 0 || strings.Contains(h.screenText(), "old-9") {
		t.Fatal("typing did not return to the live screen")
	}
	// Full-screen programs have no history to scroll.
	h.m.terms["term-1"].screen.Alt = true
	h.update(tea.MouseWheelMsg{X: pane.grid.X, Y: pane.grid.Y, Button: tea.MouseWheelUp})
	if h.m.terms["term-1"].scroll != 0 || !strings.Contains(h.m.notice.text, "full-screen") {
		t.Fatal("alternate screen scrolled")
	}
}

func TestTerminalOpenSendsClientAndSize(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.activate(action{Kind: "bottom"})
	if m.busy == nil || m.busy.Kind != "terminal.open" || m.busy.ClientID != "test" || m.busy.TerminalSize == nil {
		t.Fatalf("terminal.open %+v", m.busy)
	}
	size := *m.busy.TerminalSize
	accept(t, m, "term-new", action{Kind: "terminal-open", Value: "bottom"})
	m.snapshot.Terminals = []protocol.Terminal{terminalRecordFor("term-new")}
	f := m.measure()
	if len(f.terms) != 1 || f.terms[0].grid.W != size.Cols || f.terms[0].grid.H != size.Rows {
		t.Fatalf("open size %+v differs from the pane %+v", size, f.terms)
	}
	// The right host's prospective grid is used for a Terminal surface.
	m.activate(action{Kind: "open", Value: "terminal"})
	if m.busy == nil || m.busy.TerminalSize == nil {
		t.Fatalf("right terminal.open %+v", m.busy)
	}
	rsize := *m.busy.TerminalSize
	accept(t, m, "term-right", action{Kind: "terminal-open"})
	m.snapshot.Terminals = append(m.snapshot.Terminals, terminalRecordFor("term-right"))
	for _, pane := range m.measure().terms {
		if pane.id == "term-right" && (pane.grid.W != rsize.Cols || pane.grid.H != rsize.Rows) {
			t.Fatalf("right open size %+v differs from %+v", rsize, pane.grid)
		}
	}
}

func TestTerminalCapabilityGatingAndLegacyRecords(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.snapshot.Capabilities = []string{"fixture-agent", "fixture-terminal"}
	m.activate(action{Kind: "bottom-new"})
	if m.busy != nil || !strings.Contains(m.status, terminalCapability) {
		t.Fatalf("terminal.open sent without the capability: %+v %q", m.busy, m.status)
	}
	// A legacy fixture record keeps its recorded output and opens no stream.
	dialed := false
	m.termDial = func(context.Context, string, string) (terminalConn, error) {
		dialed = true
		return newFakeTermConn(), nil
	}
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, terminalCapability)
	m.snapshot.Terminals = []protocol.Terminal{{ID: "legacy", ThreadID: "thread-shell", State: "running", Controller: "fixture", Output: "$ fixture output"}}
	openTerminalTab(&m.viewState().Bottom, "legacy")
	m.state.Layout.Bottom = true
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	f := m.render()
	if len(f.terms) != 0 || !strings.Contains(ansi.Strip(strings.Join(f.rows, "\n")), "$ fixture output") || dialed {
		t.Fatal("legacy record not rendered as recorded output")
	}
}

func TestTerminalTabShowsTitle(t *testing.T) {
	h := newTermHarness(t)
	h.start("test")
	scr := screenOf(10, 2)
	scr.Title = "vim\x1b]0;evil\x07 main.go"
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: scr})
	f := h.m.render()
	tab := controlHit(t, f, "bottom-tab:term-1")
	row := f.rows[tab.Rect.Y]
	if plain := ansi.Strip(row); !strings.Contains(plain, "Terminal 1 · vim") || strings.Contains(row, "\x1b]0;") {
		t.Fatalf("tab title %q", plain)
	}
}

func TestTerminalColumnRendersLiveTerminal(t *testing.T) {
	h := newTermHarness(t)
	h.m.width, h.m.height = 47, 22
	h.m.selectColumn(3) // Terminal
	h.update(tea.WindowSizeMsg{Width: 47, Height: 22})
	f := h.m.measure()
	if len(f.terms) != 1 {
		t.Fatalf("compact Terminal column shows no live terminal: %+v", f.terms)
	}
	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventScreen, Screen: screenOf(f.terms[0].grid.W, f.terms[0].grid.H, textLine("compact shell"))})
	if !strings.Contains(h.screenText(), "compact shell") {
		t.Fatal("compact column did not paint the screen")
	}
}

// A right-host terminal opened while the host cannot fit beside the
// conversation is still presented, as other opened surfaces are.
func TestTerminalOpenRevealsNarrowRightHost(t *testing.T) {
	m := testModel()
	m.width, m.height = 100, 45
	m.activate(action{Kind: "open", Value: "terminal"})
	accept(t, m, "term-narrow", action{Kind: "terminal-open"})
	m.snapshot.Terminals = []protocol.Terminal{terminalRecordFor("term-narrow")}
	if f := m.measure(); len(f.terms) != 1 || f.terms[0].id != "term-narrow" {
		t.Fatalf("narrow right terminal not presented: %+v", f.terms)
	}
}

func TestTerminalBusyRejectionReportsBytesWithoutEnding(t *testing.T) {
	h := newTermHarness(t)
	h.start("test")
	tv := h.m.terms["term-1"]

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: protocol.TerminalRejectBusy, For: protocol.TerminalRequestKey, Written: 3}})
	if h.m.notice.text != "Terminal isn't reading input · 3 bytes sent" {
		t.Fatalf("busy notice %q", h.m.notice.text)
	}
	if tv.ended != nil {
		t.Fatal("busy rejection treated the terminal as ended")
	}

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: protocol.TerminalRejectBusy, For: protocol.TerminalRequestKey, Written: 0}})
	if h.m.notice.text != "Terminal isn't reading input" {
		t.Fatalf("busy notice with no bytes written %q", h.m.notice.text)
	}

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: protocol.TerminalRejectBusy, For: protocol.TerminalRequestPaste, Written: 12}})
	if h.m.notice.text != "Paste partly sent (12 bytes) · terminal isn't reading" {
		t.Fatalf("busy paste notice %q", h.m.notice.text)
	}

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventRejected, Rejected: &protocol.TerminalRejected{Reason: protocol.TerminalRejectFailed, For: protocol.TerminalRequestKey}})
	if h.m.notice.text != "Terminal input failed" {
		t.Fatalf("failed notice %q", h.m.notice.text)
	}
}

func TestTerminalEndedShowsUnconfirmedAndDescendants(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", textLine("final output"))
	status := func() string { return ansi.Strip(h.m.render().rows[pane.grid.Y-1]) }

	h.send(0, protocol.TerminalEvent{Type: protocol.TerminalEventEnded, Ended: &protocol.TerminalEnded{
		Reason: protocol.TerminalEndServerStoppedUnconfirmed,
		Exit:   &protocol.TerminalExit{DescendantsRemaining: 2},
	}})
	h.conn(0).Close()
	h.pump()
	h.m.snapshot.Terminals[0].State = protocol.TerminalStateEnded
	h.m.snapshot.Terminals[0].EndReason = protocol.TerminalEndServerStoppedUnconfirmed
	h.m.snapshot.Terminals[0].Error = "the server stopped before the shell's exit was confirmed; the process may still be running"
	s := status()
	if !strings.Contains(s, "Server stopped · exit not confirmed") {
		t.Fatalf("unconfirmed end status %q", s)
	}
	if !strings.Contains(s, "2 background processes still running") {
		t.Fatalf("descendants remaining not shown %q", s)
	}
	f := h.m.render()
	term := controlHit(t, f, "terminal:term-1")
	if !strings.Contains(term.Label, "the server stopped before the shell's exit was confirmed") {
		t.Fatalf("Error not surfaced in hover help: %q", term.Label)
	}

	// Closed with unknown descendants: the suffix names it unknown.
	if got := terminalEndLabel(protocol.TerminalEndClosed, &protocol.TerminalExit{DescendantsUnknown: true}); got != "Ended · exit 0" {
		t.Fatalf("terminalEndLabel closed = %q", got)
	}
	// The stream already closed above; apply the follow-up ended event
	// directly rather than sending on the now-closed fake connection.
	h.m.applyTerminalEvent(h.m.terms["term-1"], protocol.TerminalEvent{Type: protocol.TerminalEventEnded, Ended: &protocol.TerminalEnded{
		Reason: protocol.TerminalEndClosed,
		Exit:   &protocol.TerminalExit{DescendantsUnknown: true},
	}})
	h.m.snapshot.Terminals[0].EndReason = protocol.TerminalEndClosed
	if s := status(); !strings.Contains(s, "background processes unknown") {
		t.Fatalf("unknown descendants not shown %q", s)
	}
}

func TestTerminalDegradedScreenShowsSimplifiedMarker(t *testing.T) {
	h := newTermHarness(t)
	pane := h.start("test", textLine("live"))
	h.m.terms["term-1"].screen.Degraded = true
	status := ansi.Strip(h.m.render().rows[pane.grid.Y-1])
	if !strings.Contains(status, "simplified") {
		t.Fatalf("degraded marker not shown %q", status)
	}
	f := h.m.render()
	term := controlHit(t, f, "terminal:term-1")
	if !strings.Contains(term.Label, "simplified") {
		t.Fatalf("degraded hover help missing: %q", term.Label)
	}
}

func TestTerminalStreamLimitShowsPaneNoticeAndBacksOff(t *testing.T) {
	h := newTermHarness(t)
	h.m.termDial = func(context.Context, string, string) (terminalConn, error) {
		return nil, client.ErrTerminalStreamLimit
	}
	h.update(tea.WindowSizeMsg{Width: h.m.width, Height: h.m.height})
	f := h.m.measure()
	if len(f.terms) != 1 {
		t.Fatalf("expected one terminal pane, got %+v", f.terms)
	}
	pane := f.terms[0]
	tv := h.m.terms["term-1"]
	waitFor(t, "stream limit recorded", func() bool { return tv.limited })
	status := ansi.Strip(h.m.render().rows[pane.grid.Y-1])
	if !strings.Contains(status, "Too many terminal viewers · try again later") {
		t.Fatalf("stream limit status %q", status)
	}
	if !tv.retry || tv.backoff <= 0 {
		t.Fatalf("stream limit did not back off: retry=%v backoff=%v", tv.retry, tv.backoff)
	}
}
