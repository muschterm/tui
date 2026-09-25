package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// Embedded terminals (ADR 0019, phase 3). The server owns each shell and its
// emulator; this client only observes screens over one stream per terminal
// visible here and, while it is the controller, sends keys, pastes and
// resizes. Streams are opened and closed from Update through tea.Cmds and
// every message carries the stream generation it belongs to, so a late
// message from a replaced or closed stream is dropped. Update never blocks on
// stream I/O: reads run in commands, writes in a per-stream writer goroutine,
// and closing runs in the background.

const (
	terminalCapability  = "embedded-terminals"
	terminalQueue       = 256 // pending writes per stream before input is refused
	terminalResizeDelay = 100 * time.Millisecond
	terminalRetryMin    = 250 * time.Millisecond
	terminalRetryMax    = 5 * time.Second
)

// terminalConn is one terminal stream (client.TerminalStream in production).
type terminalConn interface {
	Events() <-chan protocol.TerminalEvent
	Err() error
	SendKey(gen int64, k protocol.TerminalKey) error
	Paste(gen int64, text string) error
	Resize(gen int64, cols, rows int) error
	RequestScrollback(from, n int) error
	Close() error
}

// terminalDialer opens a stream to terminal id as clientID; ctx bounds the
// stream's lifetime.
type terminalDialer func(ctx context.Context, id, clientID string) (terminalConn, error)

// termView is this client's presentation state for one server terminal. It is
// frontend-local and never saved: the last screen is kept while the stream is
// closed so re-showing the terminal is instant.
type termView struct {
	id      string
	gen     uint64 // generation of the current stream attempt
	conn    terminalConn
	writes  chan func(terminalConn) error
	dialing bool
	retry   bool // a reconnect is scheduled
	backoff time.Duration
	missing bool // the server has no such terminal
	limited bool // the server refused another stream (capacity)

	screen     *protocol.TerminalScreen
	control    protocol.TerminalControl
	hasControl bool
	ended      *protocol.TerminalEnded

	// resizeWant is the size last scheduled or sent as controller.
	resizeWant [2]int
	resizeSeq  uint64

	// scroll is how many lines the view is scrolled above the live screen;
	// history caches the last scrollback answer.
	scroll       int
	history      *protocol.TerminalScrollback
	historyAsked bool
}

type (
	termDialedMsg struct {
		id   string
		gen  uint64
		conn terminalConn
		err  error
	}
	termEventMsg struct {
		id  string
		gen uint64
		ev  protocol.TerminalEvent
	}
	termClosedMsg struct {
		id  string
		gen uint64
		err error
	}
	termRetryMsg struct {
		id  string
		gen uint64
	}
	termResizeMsg struct {
		id  string
		seq uint64
	}
)

func (m *Model) embeddedTerminals() bool {
	return slices.Contains(m.snapshot.Capabilities, terminalCapability)
}

func (m *Model) terminalRecord(id string) (protocol.Terminal, bool) {
	for _, t := range m.snapshot.Terminals {
		if t.ID == id {
			return t, true
		}
	}
	return protocol.Terminal{}, false
}

// liveTerminal reports a record this client renders as an embedded terminal:
// the server supports them and the record came from a real session (legacy
// fixture records have no control generation and keep their Output text).
func (m *Model) liveTerminal(id string) (protocol.Terminal, bool) {
	rec, ok := m.terminalRecord(id)
	return rec, ok && m.embeddedTerminals() && rec.ControlGen > 0
}

func (m *Model) terminalDialer() terminalDialer {
	if m.termDial != nil {
		return m.termDial
	}
	c := m.client
	if c == nil {
		return nil
	}
	return func(ctx context.Context, id, clientID string) (terminalConn, error) {
		s, err := c.OpenTerminalStream(ctx, id, clientID)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
}

func (m *Model) termView(id string) *termView {
	if m.terms == nil {
		m.terms = map[string]*termView{}
	}
	tv := m.terms[id]
	if tv == nil {
		tv = &termView{id: id}
		m.terms[id] = tv
	}
	return tv
}

// controls reports whether this client is tv's current controller.
func (m *Model) controls(tv *termView) bool {
	return tv != nil && tv.hasControl && tv.control.Controller == m.clientID
}

// syncTerminals keeps exactly the terminals visible in this client streaming,
// schedules controller resizes and drops input focus that no longer applies.
// It runs after every update.
func (m *Model) syncTerminals() tea.Cmd {
	if len(m.terms) == 0 && !m.mayShowTerminal() {
		return nil
	}
	visible := map[string]shell.Rect{}
	if m.embeddedTerminals() && m.connected {
		for _, pane := range m.measure().terms {
			visible[pane.id] = pane.grid
		}
	}
	var cmds []tea.Cmd
	for id, tv := range m.terms {
		if _, ok := m.terminalRecord(id); !ok {
			m.closeTermStream(tv)
			delete(m.terms, id)
			continue
		}
		if _, ok := visible[id]; !ok {
			m.closeTermStream(tv)
		}
	}
	for id, grid := range visible {
		tv := m.termView(id)
		if tv.conn == nil && !tv.dialing && !tv.retry && tv.ended == nil && !tv.missing {
			cmds = append(cmds, m.dialTerm(tv))
		}
		cmds = append(cmds, m.scheduleResize(tv, grid))
	}
	if m.termFocus != "" {
		_, shown := visible[m.termFocus]
		tv := m.terms[m.termFocus]
		rec, _ := m.terminalRecord(m.termFocus)
		if !shown || m.focus != "terminal:"+m.termFocus || !m.controls(tv) || tv.ended != nil || rec.State != protocol.TerminalStateRunning {
			m.termFocus = ""
		}
	}
	return tea.Batch(cmds...)
}

// mayShowTerminal is a cheap check that the active view has a terminal tab,
// so views without terminals never measure for them.
func (m *Model) mayShowTerminal() bool {
	if m.state.Active == "" && !m.creatingThread() {
		return false
	}
	v := m.viewState()
	if tab, ok := v.Host.Active(); ok && tab.Kind == "terminal" {
		return true
	}
	_, ok := v.Bottom.Active()
	return ok
}

func (m *Model) dialTerm(tv *termView) tea.Cmd {
	dial := m.terminalDialer()
	if dial == nil {
		return nil
	}
	m.termSeq++
	tv.gen, tv.dialing = m.termSeq, true
	id, gen, ctx, clientID := tv.id, tv.gen, m.ctx, m.clientID
	return func() tea.Msg {
		conn, err := dial(ctx, id, clientID)
		return termDialedMsg{id: id, gen: gen, conn: conn, err: err}
	}
}

// readTerm delivers the stream's next event, or its end.
func readTerm(id string, gen uint64, conn terminalConn) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-conn.Events()
		if !ok {
			return termClosedMsg{id: id, gen: gen, err: conn.Err()}
		}
		return termEventMsg{id: id, gen: gen, ev: ev}
	}
}

// closeTermStream closes tv's stream without affecting the session; later
// messages from it are stale.
func (m *Model) closeTermStream(tv *termView) {
	m.termSeq++
	tv.gen = m.termSeq
	tv.dialing, tv.retry, tv.backoff = false, false, 0
	tv.scroll, tv.history, tv.historyAsked = 0, nil, false
	tv.hasControl, tv.resizeWant = false, [2]int{}
	if tv.conn != nil {
		conn, writes := tv.conn, tv.writes
		tv.conn, tv.writes = nil, nil
		// A close handshake can wait on the peer; never block Update on it.
		// The writer drains its queue against the closed connection and stops.
		go func() {
			_ = conn.Close()
			close(writes)
		}()
	}
}

// closeTerminalStreams closes every stream, for detach and quit.
func (m *Model) closeTerminalStreams() {
	for _, tv := range m.terms {
		m.closeTermStream(tv)
	}
	m.termFocus = ""
}

func startTermWriter(conn terminalConn) chan func(terminalConn) error {
	writes := make(chan func(terminalConn) error, terminalQueue)
	go func() {
		// Write failures surface as the stream's own end, which reconnects.
		for write := range writes {
			_ = write(conn)
		}
	}()
	return writes
}

// termWrite queues one ordered write on tv's stream without blocking.
func (m *Model) termWrite(tv *termView, write func(terminalConn) error) tea.Cmd {
	if tv == nil || tv.writes == nil {
		return m.showNoticeAs(noticeUnavailable, "Terminal reconnecting · input not sent")
	}
	select {
	case tv.writes <- write:
		return nil
	default:
		return m.showNoticeAs(noticeError, "Terminal input backlog full · input not sent")
	}
}

func (m *Model) acceptTerminalMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case termDialedMsg:
		tv := m.terms[msg.id]
		if tv == nil || tv.gen != msg.gen || !tv.dialing {
			if msg.conn != nil {
				go msg.conn.Close()
			}
			return nil, true
		}
		tv.dialing = false
		if msg.err != nil {
			if errors.Is(msg.err, client.ErrTerminalNotFound) {
				tv.missing = true
				return nil, true
			}
			tv.limited = errors.Is(msg.err, client.ErrTerminalStreamLimit)
			return m.retryTerm(tv), true
		}
		tv.limited = false
		tv.conn, tv.writes = msg.conn, startTermWriter(msg.conn)
		return readTerm(tv.id, tv.gen, tv.conn), true
	case termEventMsg:
		tv := m.terms[msg.id]
		if tv == nil || tv.gen != msg.gen || tv.conn == nil {
			return nil, true
		}
		cmd := m.applyTerminalEvent(tv, msg.ev)
		return tea.Batch(cmd, readTerm(tv.id, tv.gen, tv.conn)), true
	case termClosedMsg:
		tv := m.terms[msg.id]
		if tv == nil || tv.gen != msg.gen {
			return nil, true
		}
		conn, writes := tv.conn, tv.writes
		tv.conn, tv.writes, tv.hasControl = nil, nil, false
		if writes != nil {
			go func() {
				_ = conn.Close()
				close(writes)
			}()
		}
		if m.termFocus == tv.id {
			m.termFocus = ""
		}
		if tv.ended != nil {
			return nil, true
		}
		return m.retryTerm(tv), true
	case termRetryMsg:
		if tv := m.terms[msg.id]; tv != nil && tv.gen == msg.gen && tv.retry {
			tv.retry = false
			// syncTerminals redials it if it is still visible.
		}
		return nil, true
	case termResizeMsg:
		tv := m.terms[msg.id]
		if tv == nil || tv.resizeSeq != msg.seq || !m.controls(tv) || tv.conn == nil {
			return nil, true
		}
		gen, want := tv.control.Gen, tv.resizeWant
		return m.termWrite(tv, func(c terminalConn) error { return c.Resize(gen, want[0], want[1]) }), true
	}
	return nil, false
}

// retryTerm schedules a reconnect with bounded exponential backoff.
func (m *Model) retryTerm(tv *termView) tea.Cmd {
	tv.backoff = min(terminalRetryMax, max(terminalRetryMin, tv.backoff*2))
	tv.retry = true
	id, gen := tv.id, tv.gen
	return tea.Tick(tv.backoff, func(time.Time) tea.Msg { return termRetryMsg{id: id, gen: gen} })
}

func (m *Model) applyTerminalEvent(tv *termView, ev protocol.TerminalEvent) tea.Cmd {
	switch ev.Type {
	case protocol.TerminalEventScreen:
		if ev.Screen != nil {
			tv.screen = ev.Screen
			tv.backoff = 0
		}
	case protocol.TerminalEventControl:
		if ev.Control == nil {
			return nil
		}
		lost := m.controls(tv) && ev.Control.Controller != m.clientID
		tv.control, tv.hasControl = *ev.Control, true
		// A new controller sizes the terminal to its own pane.
		tv.resizeWant = [2]int{}
		if lost {
			if m.termFocus == tv.id {
				m.termFocus = ""
			}
			return m.showNoticeAs(noticeUnavailable, "Another client took control of this terminal · Take control")
		}
	case protocol.TerminalEventEnded:
		if ev.Ended != nil {
			tv.ended = ev.Ended
		} else {
			tv.ended = &protocol.TerminalEnded{}
		}
		if m.termFocus == tv.id {
			m.termFocus = ""
		}
	case protocol.TerminalEventRejected:
		if ev.Rejected == nil || ev.Rejected.For == protocol.TerminalRequestResize {
			// A resize raced a control change; the next control event resizes.
			return nil
		}
		switch ev.Rejected.Reason {
		case protocol.TerminalRejectNotController, protocol.TerminalRejectStaleGeneration:
			return m.showNoticeAs(noticeUnavailable, "Another client controls this terminal · Take control")
		case protocol.TerminalRejectEnded:
			// Not proof the session ended: the record and ended event say so.
			return m.showNoticeAs(noticeUnavailable, "Terminal did not accept input")
		case protocol.TerminalRejectTooLarge:
			return m.showNoticeAs(noticeError, "Terminal input too large · not sent")
		case protocol.TerminalRejectBusy:
			// The terminal is still running; a busy rejection is never ended.
			if ev.Rejected.For == protocol.TerminalRequestPaste {
				if ev.Rejected.Written == 0 {
					return m.showNoticeAs(noticeUnavailable, "Paste not sent · terminal isn't reading")
				}
				return m.showNoticeAs(noticeUnavailable, fmt.Sprintf("Paste partly sent (%d bytes) · terminal isn't reading", ev.Rejected.Written))
			}
			msg := "Terminal isn't reading input"
			if ev.Rejected.Written > 0 {
				msg += fmt.Sprintf(" · %d bytes sent", ev.Rejected.Written)
			}
			return m.showNoticeAs(noticeUnavailable, msg)
		case protocol.TerminalRejectFailed:
			return m.showNoticeAs(noticeError, "Terminal input failed")
		default:
			if ev.Rejected.For == protocol.TerminalRequestScrollback {
				tv.historyAsked = false
				return nil
			}
			return m.showNoticeAs(noticeError, "Terminal rejected that input")
		}
	case protocol.TerminalEventScrollback:
		if ev.Scrollback != nil {
			// Output since the last answer adds history below the view; keep
			// the same lines in view.
			if old := tv.history; old != nil && ev.Scrollback.Total > old.Total {
				tv.scroll += ev.Scrollback.Total - old.Total
			}
			tv.history, tv.historyAsked = ev.Scrollback, false
			if tv.history.Total == 0 {
				tv.scroll = 0
				return m.showNoticeAs(noticeInfo, "No terminal history yet")
			}
			tv.scroll = min(tv.scroll, tv.history.Total)
			return m.requestHistory(tv)
		}
	}
	return nil
}

// scheduleResize debounces a controller resize to the pane's grid size.
// Observers never resize.
func (m *Model) scheduleResize(tv *termView, grid shell.Rect) tea.Cmd {
	rec, _ := m.terminalRecord(tv.id)
	if tv.conn == nil || tv.screen == nil || !m.controls(tv) || tv.ended != nil || rec.State != protocol.TerminalStateRunning || grid.W < 2 || grid.H < 1 {
		return nil
	}
	want := [2]int{grid.W, grid.H}
	if want == [2]int{tv.screen.Cols, tv.screen.Rows} || want == tv.resizeWant {
		return nil
	}
	tv.resizeWant = want
	tv.resizeSeq++
	id, seq := tv.id, tv.resizeSeq
	return tea.Tick(terminalResizeDelay, func(time.Time) tea.Msg { return termResizeMsg{id: id, seq: seq} })
}

// terminalOpenSize is the grid the new terminal's pane will have, so the
// shell starts at the size it is shown at. Nil lets the server default.
func (m *Model) terminalOpenSize(bottom bool) *protocol.TerminalSize {
	footer := m.footerHeight()
	var r shell.Rect
	if m.singleColumn() {
		region := shell.RightRegion
		if bottom {
			region = shell.BottomRegion
		}
		g := shell.ColumnGeometry(m.width, m.height-1, footer, region)
		r = g.Right
		if bottom {
			r = g.Bottom
		}
	} else {
		layout := m.state.Layout
		if bottom {
			layout.Bottom = true
			r = layout.Compute(m.width, m.height-1, footer).Bottom
		} else {
			layout.Right = true
			r = layout.Compute(m.width, m.height-1, footer).Right
		}
	}
	grid := terminalGridRect(r, bottom)
	if grid.W < 2 || grid.H < 1 {
		return nil
	}
	return &protocol.TerminalSize{Cols: grid.W, Rows: grid.H}
}

// openTerminal requests a new session for the bottom panel or right host.
func (m *Model) openTerminal(bottom bool) tea.Cmd {
	local := action{Kind: "terminal-open"}
	if bottom {
		local.Value = "bottom"
	}
	return m.command(protocol.Command{Kind: "terminal.open", TerminalSize: m.terminalOpenSize(bottom)}, local)
}
