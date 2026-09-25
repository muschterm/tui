package protocol

import (
	"strconv"
	"unicode/utf8"
)

// Embedded terminals (ADR 0019).
//
// Lifecycle commands travel through POST /v1/command and are journaled like
// every other command:
//
//   - terminal.open (ThreadID, ClientID, optional TerminalSize) starts the
//     user's shell in the thread's checkout and returns the new terminal ID as
//     Receipt.TargetID. The opener is the first controller (ControlGen 1).
//   - terminal.close (ThreadID, TargetID) ends the session: running becomes
//     closing, then ended once the process is reaped, or close_uncertain when
//     exit cannot be confirmed in time (it still becomes ended when it is).
//     Closing an ended terminal succeeds without change.
//   - terminal.take-control (ThreadID, TargetID, ClientID) makes ClientID the
//     controller and increments ControlGen; idempotent for the controller.
//
// Screen output, input and resize use the ephemeral stream
// GET /v1/terminals/{id}/stream?client_id=<ClientID> (WebSocket, same
// authentication as /v1/events). Each message is one JSON text frame. The
// server sends TerminalEvent values; the client sends TerminalRequest values.
// Nothing sent on the stream is persisted, logged or replayed.

// Terminal lifecycle states (Terminal.State).
const (
	TerminalStateRunning        = "running"
	TerminalStateClosing        = "closing"
	TerminalStateCloseUncertain = "close_uncertain"
	TerminalStateEnded          = "ended"
)

// Terminal end reasons (Terminal.EndReason and TerminalEnded.Reason).
const (
	TerminalEndExited          = "exited"           // the shell exited by itself
	TerminalEndClosed          = "closed"           // terminal.close
	TerminalEndThreadDeleted   = "thread_deleted"   // thread delete or project removal
	TerminalEndServerStopped   = "server_stopped"   // graceful server stop
	TerminalEndServerRestarted = "server_restarted" // found live in storage at start
	// TerminalEndServerStoppedUnconfirmed: the server stopped before the
	// shell's exit was confirmed; the process may have outlived it.
	TerminalEndServerStoppedUnconfirmed = "server_stopped_unconfirmed"
)

// TerminalMaxInput bounds the bytes of one input request. Client.SendInput
// splits larger input.
const TerminalMaxInput = 64 << 10

// TerminalMaxScrollback bounds the lines returned by one scrollback request.
const TerminalMaxScrollback = 500

// TerminalSize is a PTY size in cells.
type TerminalSize struct{ Cols, Rows int }

// TerminalExit describes how the shell ended. Code is -1 when a signal
// killed it; Killed reports that close escalated to SIGKILL.
type TerminalExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Killed bool   `json:"killed,omitempty"`
	// Descendants counts other processes found in the shell's session;
	// DescendantsKilled reports SIGKILL escalation for them on close, and
	// DescendantsRemaining those still present when the terminal ended (after
	// a natural exit they are left running). DescendantsUnknown means the
	// platform cannot enumerate the session.
	Descendants          int  `json:"descendants,omitempty"`
	DescendantsKilled    bool `json:"descendants_killed,omitempty"`
	DescendantsRemaining int  `json:"descendants_remaining,omitempty"`
	DescendantsUnknown   bool `json:"descendants_unknown,omitempty"`
}

// Stream event types (TerminalEvent.Type).
const (
	TerminalEventScreen     = "screen"
	TerminalEventControl    = "control"
	TerminalEventEnded      = "ended"
	TerminalEventRejected   = "rejected"
	TerminalEventScrollback = "scrollback"
)

// TerminalEvent is one server→client stream message; the field named by Type
// is set.
//
// On connect the server sends screen then control for a live terminal, or
// only ended (then closes normally) for an ended one. Afterwards screen is
// sent when the grid changes, at most about 30 per second per connection;
// intermediate frames are dropped for slow readers, so every screen is a
// complete replacement, never a diff. control is sent whenever the
// controller or generation changes. ended is sent once, after the final
// screen, and the server then closes the socket with a normal closure.
// rejected answers a refused input/resize/scrollback request, and scrollback
// answers a scrollback request.
type TerminalEvent struct {
	Type       string              `json:"type"`
	Screen     *TerminalScreen     `json:"screen,omitempty"`
	Control    *TerminalControl    `json:"control,omitempty"`
	Ended      *TerminalEnded      `json:"ended,omitempty"`
	Rejected   *TerminalRejected   `json:"rejected,omitempty"`
	Scrollback *TerminalScrollback `json:"scrollback,omitempty"`
}

// TerminalScreen is the complete visible grid. Lines has exactly Rows
// entries. Seq increases with every emulator change and orders frames.
type TerminalScreen struct {
	Seq    uint64         `json:"seq"`
	Cols   int            `json:"cols"`
	Rows   int            `json:"rows"`
	Cursor TerminalCursor `json:"cursor"`
	Title  string         `json:"title,omitempty"`
	Alt    bool           `json:"alt,omitempty"`
	// Degraded reports that the frame exceeded the server's size budget and
	// was simplified (clusters reduced to their first rune, then styles
	// dropped) so it stays deliverable.
	Degraded bool           `json:"degraded,omitempty"`
	Lines    []TerminalLine `json:"lines"`
}

// TerminalCursor is the cursor cell in grid coordinates.
type TerminalCursor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Visible bool `json:"visible"`
}

// TerminalLine is one row as consecutive style runs from column 0. Visible
// rows cover every column: the runs' Cells() sum to the screen's cols, except
// that a wide grapheme clipped at the last column counts 2 and the client
// clips it. Scrollback rows omit trailing blank cells.
type TerminalLine []TerminalRun

// TerminalRun is consecutive graphemes sharing style and cell width. Wide
// characters' continuation cells are not transmitted: a Width 2 grapheme
// covers its own cell and the next.
//
// Placement is exact without client-side segmentation: when Cluster is false
// every rune of Text is one grapheme; when true Text is a single grapheme
// cluster of several runes (combining marks, emoji sequences). Use
// Graphemes.
type TerminalRun struct {
	Text    string        `json:"t"`
	Width   int           `json:"w"` // cells per grapheme: 1 or 2
	Cluster bool          `json:"c,omitempty"`
	FG      TerminalColor `json:"fg,omitempty"`
	BG      TerminalColor `json:"bg,omitempty"`
	Attrs   uint8         `json:"a,omitempty"` // TerminalAttr* bits
}

// Graphemes splits the run into the graphemes it places, each Width cells.
func (r TerminalRun) Graphemes() []string {
	if r.Cluster {
		return []string{r.Text}
	}
	out := make([]string, 0, len(r.Text))
	for i, c := range r.Text {
		out = append(out, r.Text[i:i+utf8.RuneLen(c)])
	}
	return out
}

// Cells is the number of cells the run covers.
func (r TerminalRun) Cells() int {
	if r.Cluster {
		return r.Width
	}
	return utf8.RuneCountInString(r.Text) * r.Width
}

// Terminal cell attribute bits (TerminalRun.Attrs).
const (
	TerminalAttrBold uint8 = 1 << iota
	TerminalAttrDim
	TerminalAttrItalic
	TerminalAttrUnderline
	TerminalAttrReverse
	TerminalAttrStrike
	TerminalAttrBlink
)

// TerminalColor encodes a cell colour: "" is the terminal default, decimal
// "0".."255" is a palette index (0-15 the basic/bright ANSI colours) and
// "#rrggbb" is direct colour.
type TerminalColor string

// TerminalIndexed returns the palette colour i.
func TerminalIndexed(i uint8) TerminalColor { return TerminalColor(strconv.Itoa(int(i))) }

// TerminalRGB returns a direct colour.
func TerminalRGB(r, g, b uint8) TerminalColor {
	const hex = "0123456789abcdef"
	return TerminalColor([]byte{'#', hex[r>>4], hex[r&15], hex[g>>4], hex[g&15], hex[b>>4], hex[b&15]})
}

// Index reports a palette colour.
func (c TerminalColor) Index() (uint8, bool) {
	if c == "" || c[0] == '#' {
		return 0, false
	}
	n, err := strconv.ParseUint(string(c), 10, 8)
	return uint8(n), err == nil
}

// RGB reports a direct colour.
func (c TerminalColor) RGB() (r, g, b uint8, ok bool) {
	if len(c) != 7 || c[0] != '#' {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(string(c[1:]), 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(n >> 16), uint8(n >> 8), uint8(n), true
}

// TerminalControl names the client allowed to send input and resize, with
// the generation those requests must carry.
type TerminalControl struct {
	Controller string `json:"controller"`
	Gen        int64  `json:"gen"`
}

// TerminalEnded reports why the terminal ended. Exit is nil when the process
// outcome is unknown (for example after a server restart).
type TerminalEnded struct {
	Exit   *TerminalExit `json:"exit,omitempty"`
	Reason string        `json:"reason"`
}

// Rejection reasons (TerminalRejected.Reason).
const (
	TerminalRejectNotController   = "not_controller"   // another client controls the terminal
	TerminalRejectStaleGeneration = "stale_generation" // control changed since gen
	TerminalRejectEnded           = "ended"            // closing or ended
	TerminalRejectTooLarge        = "too_large"        // input above TerminalMaxInput
	TerminalRejectInvalid         = "invalid"          // malformed request
	// TerminalRejectBusy: the child did not read input before the write
	// deadline; Written bytes reached it and the rest were dropped. The
	// terminal is still running.
	TerminalRejectBusy = "busy"
	// TerminalRejectFailed: the PTY refused the request for another reason
	// while the terminal was still running.
	TerminalRejectFailed = "failed"
)

// TerminalRejected reports a refused request. For is the request type.
type TerminalRejected struct {
	Reason string `json:"reason"`
	For    string `json:"for"`
	// Written is the number of input bytes delivered before a busy rejection.
	Written int `json:"written,omitempty"`
}

// TerminalScrollback answers a scrollback request: Lines are history lines
// From, From+1, … (0 is the oldest retained line) of Total retained. History
// is capped (10,000 lines), so once full, indexes shift as output continues;
// request again with the new Total rather than caching by index.
//
// Each event stays within the server's 8 MiB frame budget. More reports that
// fewer lines were returned than requested to fit it: for a request from the
// newest lines (negative from) the oldest are omitted and From moves forward,
// otherwise the newest are omitted; request the rest separately. Degraded
// reports that a line alone exceeded the budget and was simplified as for
// screens.
type TerminalScrollback struct {
	From     int            `json:"from"`
	Total    int            `json:"total"`
	Lines    []TerminalLine `json:"lines"`
	More     bool           `json:"more,omitempty"`
	Degraded bool           `json:"degraded,omitempty"`
}

// Stream request types (TerminalRequest.Type).
const (
	TerminalRequestInput      = "input"
	TerminalRequestResize     = "resize"
	TerminalRequestScrollback = "scrollback"
	TerminalRequestKey        = "key"
	TerminalRequestPaste      = "paste"
)

// TerminalMaxKeyText bounds TerminalKey.Text in bytes.
const TerminalMaxKeyText = 256

// Key modifier bits (TerminalKey.Mods).
const (
	TerminalModShift uint8 = 1 << iota
	TerminalModAlt
	TerminalModCtrl
	TerminalModMeta
)

// TerminalKey is one semantic key press. The server encodes it for the
// child's current input modes (application cursor keys, application keypad),
// which only its emulator knows.
//
// Code names a special key — enter, tab, backspace, escape, space, up, down,
// left, right, home, end, pgup, pgdown, insert, delete, f1…f12, kp0…kp9,
// kp-enter, kp-plus, kp-minus, kp-multiply, kp-divide, kp-decimal,
// kp-equal, kp-comma — or is the key's own character ("a", "]"). Text is
// the printable text the key produces (possibly several runes, at most
// TerminalMaxKeyText bytes, no control characters); when empty and Code is
// one character, Code is used (upper-cased with Shift). Ctrl with a
// character sends its C0 byte; Alt and Meta prefix ESC; modified cursor,
// editing and function keys use xterm's modifier parameter.
type TerminalKey struct {
	Code string `json:"code"`
	Text string `json:"text,omitempty"`
	Mods uint8  `json:"mods,omitempty"`
}

// TerminalRequest is one client→server stream message.
//
//   - input: Gen plus either Data (UTF-8 text) or DataB64 (arbitrary bytes,
//     base64 in JSON), at most TerminalMaxInput bytes.
//   - resize: Gen, Cols, Rows (clamped by the server to 2..1000 × 1..500).
//   - scrollback: From and N (1..TerminalMaxScrollback). A negative From
//     requests the newest N lines.
//   - key: Gen and Key (see TerminalKey). Invalid keys are rejected invalid.
//   - paste: Gen and Text (UTF-8, at most TerminalMaxInput bytes; split
//     larger pastes). Newlines are sent as CR; when the child enabled
//     bracketed paste the text is wrapped in ESC[200~ … ESC[201~ with any ESC
//     inside it removed, otherwise it is sent as typed.
//
// Input, key, paste and resize are accepted only from the stream's client_id while it
// is the controller, with the current ControlGen, while the terminal is
// running; otherwise the server answers rejected. Accepted requests have no
// reply: the effect appears in later screen events.
type TerminalRequest struct {
	Type    string       `json:"type"`
	Gen     int64        `json:"gen,omitempty"`
	Data    string       `json:"data,omitempty"`
	DataB64 []byte       `json:"data_b64,omitempty"`
	Cols    int          `json:"cols,omitempty"`
	Rows    int          `json:"rows,omitempty"`
	From    int          `json:"from,omitempty"`
	N       int          `json:"n,omitempty"`
	Key     *TerminalKey `json:"key,omitempty"`
	Text    string       `json:"text,omitempty"`
}
