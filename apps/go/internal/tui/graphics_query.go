package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// graphicsOptOutEnv disables kitty graphics for this client when set to
// off, 0, false, no or none (case-insensitive). It never enables graphics:
// detection still decides. Named after TUI_GO_ICONS/TUI_GO_HOME.
const graphicsOptOutEnv = "TUI_GO_GRAPHICS"

// graphicsQueryTimeout backstops the DA1 terminator. Silence is unsupported.
const graphicsQueryTimeout = time.Second

// graphicsQueryImageID is the id used only by the a=q probe. a=q never stores
// the image, so it cannot collide with a registered placement id.
const graphicsQueryImageID = 31

type graphicsSupport uint8

const (
	graphicsUnknown graphicsSupport = iota // not yet queried (fallback shown)
	graphicsQuerying
	graphicsSupported
	graphicsUnavailable
)

// graphicsProbe is per-terminal-connection capability state, like
// terminalColorProbe. It never infers support from TERM or TERM_PROGRAM.
type graphicsProbe struct {
	state  graphicsSupport
	reason string // why graphics are unavailable (diagnostics)
	// Environment gates captured at startup.
	noColor, optOut, multiplexer bool
	seq                          int // invalidates stale timeouts
	cellW, cellH                 int // cell pixel size; 0 until reported
}

// graphicsProbeTimeoutMsg ends a query that never received DA1.
type graphicsProbeTimeoutMsg struct{ seq int }

// newGraphicsProbe captures environment gates. tmux (TMUX), GNU screen (STY)
// and herdr (HERDR_ENV=1) are unavailable: transmits need DCS passthrough and
// query replies are not reliably routed back, and this slice implements no
// passthrough wrapping. Placeholders themselves would survive as text.
func newGraphicsProbe(getenv func(string) string) graphicsProbe {
	p := graphicsProbe{
		noColor:     getenv("NO_COLOR") != "",
		multiplexer: getenv("TMUX") != "" || getenv("STY") != "" || strings.TrimSpace(getenv("HERDR_ENV")) == "1",
	}
	switch strings.ToLower(strings.TrimSpace(getenv(graphicsOptOutEnv))) {
	case "off", "0", "false", "no", "none":
		p.optOut = true
	}
	switch {
	case p.noColor:
		p.disable("disabled by NO_COLOR")
	case p.optOut:
		p.disable("disabled by " + graphicsOptOutEnv)
	case p.multiplexer:
		p.disable("unavailable inside a terminal multiplexer")
	}
	return p
}

func (p *graphicsProbe) disable(reason string) {
	p.state, p.reason = graphicsUnavailable, reason
}

// Supported reports whether placeholders may be rendered and images sent.
func (p *graphicsProbe) Supported() bool { return p.state == graphicsSupported }

// CellPixels returns the reported cell size, or 0,0 when unknown (callers
// then use the 1:2 aspect fallback in graphicsFit).
func (p *graphicsProbe) CellPixels() (w, h int) { return p.cellW, p.cellH }

// graphicsQueryAllowed mirrors supportsColorQueries: only an actual XTVERSION
// reply naming a parser known to handle APC may unlock the query. iTerm2 is
// excluded because it does not implement kitty graphics placeholders.
func graphicsQueryAllowed(terminalName string) bool {
	if !supportsColorQueries(terminalName) {
		return false
	}
	name := strings.ToLower(terminalName)
	return strings.HasPrefix(name, "kitty(") || strings.HasPrefix(name, "ghostty ")
}

// graphicsQuerySequence is the probe: a 1×1 RGB a=q query, a cell-size
// request (CSI 16 t) and DA1 as a deterministic terminator.
func graphicsQuerySequence() string {
	return ansi.KittyGraphics([]byte("AAAA"), "i="+strconv.Itoa(graphicsQueryImageID), "s=1", "v=1", "a=q", "t=d", "f=24") +
		"\x1b[16t" + ansi.RequestPrimaryDeviceAttributes
}

// Start sends the query once, after an XTVERSION reply, when every gate
// allows it. It returns nil (leaving the fallback) otherwise.
func (p *graphicsProbe) Start(terminalName string, profile colorprofile.Profile) tea.Cmd {
	if p.state != graphicsUnknown {
		return nil
	}
	if profile != colorprofile.ANSI256 && profile != colorprofile.TrueColor {
		// Downsampling below 256 colours would destroy the image id.
		p.disable("colour profile below 256 colours")
		return nil
	}
	if !graphicsQueryAllowed(terminalName) {
		p.disable("terminal not confirmed for graphics queries")
		return nil
	}
	p.state = graphicsQuerying
	p.seq++
	seq := p.seq
	return tea.Batch(tea.Raw(graphicsQuerySequence()), tea.Tick(graphicsQueryTimeout, func(time.Time) tea.Msg {
		return graphicsProbeTimeoutMsg{seq: seq}
	}))
}

// ProfileChanged disables graphics if the effective profile drops below
// ANSI256 (e.g. a later NO_COLOR-equivalent downgrade).
func (p *graphicsProbe) ProfileChanged(profile colorprofile.Profile) {
	if profile != colorprofile.ANSI256 && profile != colorprofile.TrueColor && p.state != graphicsUnavailable {
		p.disable("colour profile below 256 colours")
	}
}

// Update consumes probe replies. handled is true when msg belonged to the
// probe and should not reach other handlers. Replies are accepted only
// while querying (the cell size is also recorded afterwards, e.g. after a
// font change triggered by a later CSI 16 t from the integrator).
func (p *graphicsProbe) Update(msg tea.Msg) (handled bool) {
	switch msg := msg.(type) {
	case uv.CellSizeEvent:
		if msg.Width > 0 && msg.Height > 0 {
			p.cellW, p.cellH = msg.Width, msg.Height
		}
		return true
	case uv.KittyGraphicsEvent:
		// The pinned decoder parses complete ESC _ G replies into this event.
		return p.reply(msg.Options.ID, string(msg.Payload))
	case uv.UnknownApcEvent:
		id, message, ok := parseKittyGraphicsReply(string(msg))
		if !ok {
			return false
		}
		return p.reply(id, message)
	case uv.PrimaryDeviceAttributesEvent:
		if p.state != graphicsQuerying {
			return false
		}
		// DA1 arrived before any kitty reply: the terminal ignored a=q.
		p.disable("terminal did not answer the graphics query")
		return true
	case graphicsProbeTimeoutMsg:
		if msg.seq == p.seq && p.state == graphicsQuerying {
			p.disable("graphics query timed out")
		}
		return true
	}
	return false
}

// reply applies a kitty graphics reply for the probe id; other ids are not
// the probe's.
func (p *graphicsProbe) reply(id int, message string) bool {
	if id != graphicsQueryImageID {
		return false
	}
	if p.state == graphicsQuerying {
		if message == "OK" {
			p.state, p.reason = graphicsSupported, ""
		} else {
			p.disable("terminal rejected graphics query: " + safe(message))
		}
	}
	return true
}

// parseKittyGraphicsReply parses ESC _ G <keys> ; <message> ESC \ (the raw
// UnknownApcEvent form, including introducer and terminator).
func parseKittyGraphicsReply(s string) (id int, message string, ok bool) {
	switch {
	case strings.HasPrefix(s, "\x1b_"):
		s = s[2:]
	case strings.HasPrefix(s, "\u009f"):
		s = strings.TrimPrefix(s, "\u009f")
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "\x1b\\"), "\u009c")
	if !strings.HasPrefix(s, "G") {
		return 0, "", false
	}
	keys, message, found := strings.Cut(s[1:], ";")
	if !found {
		return 0, "", false
	}
	for kv := range strings.SplitSeq(keys, ",") {
		k, v, _ := strings.Cut(kv, "=")
		if k != "i" {
			continue
		}
		n := 0
		for _, r := range v {
			if r < '0' || r > '9' || n > 1<<24 {
				return 0, "", false
			}
			n = n*10 + int(r-'0')
		}
		id = n
	}
	return id, message, id > 0
}
