package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

const terminalReplyWindow = time.Second
const terminalReplyLimit = 512

type terminalReplyStarted uint64
type terminalReplyExpired uint64
type terminalReplyReplay []tea.Msg

// The pinned input reader expires even incomplete DCS strings after 50 ms.
// Reassemble only a recognized response to our probe. Keep original printable
// key events until ST proves they are response bytes; replay them on abort.
type terminalReplyFragments struct {
	serial   uint64
	buffer   string
	deadline time.Time
	keys     []tea.Msg
}

func (r *terminalReplyFragments) abort(next tea.Msg) tea.Msg {
	replay := terminalReplyReplay(r.keys)
	r.buffer, r.keys = "", nil
	if next != nil {
		replay = append(replay, next)
	}
	if len(replay) == 0 {
		return nil
	}
	return replay
}

func (m *Model) filterTerminalReply(msg tea.Msg, now time.Time) tea.Msg {
	r := &m.colorReply
	if expired, ok := msg.(terminalReplyExpired); ok {
		if uint64(expired) == r.serial && r.buffer != "" {
			return r.abort(nil)
		}
		return nil
	}
	if r.buffer != "" && !now.Before(r.deadline) {
		// Replay only user input through Model.Update. Framework messages
		// (Quit, capability replies, internal commands) must still reach
		// Bubble Tea's own event loop; the scheduled expiry flushes keys.
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseMsg, tea.PasteMsg, tea.PasteStartMsg, tea.FocusMsg, tea.BlurMsg, uv.UnknownEvent:
			return r.abort(msg)
		default:
			return msg
		}
	}
	if r.buffer == "" {
		if unknown, ok := msg.(uv.UnknownEvent); ok {
			s := string(unknown)
			version := m.colorProbe.versionRequested && strings.HasPrefix(s, "\x1bP>|")
			capability := m.colorProbe.capabilitiesRequested && (strings.HasPrefix(s, "\x1bP1+r") || strings.HasPrefix(s, "\x1bP0+r"))
			// An incomplete kitty graphics reply (APC ESC _ G …) to the probe.
			graphics := m.graphics.state == graphicsQuerying && strings.HasPrefix(s, "\x1b_G")
			if (version || capability || graphics) && len(s) < terminalReplyLimit {
				r.serial++
				r.buffer, r.deadline = s, now.Add(terminalReplyWindow)
				return terminalReplyStarted(r.serial)
			}
		}
		return msg
	}
	terminator := false
	switch event := msg.(type) {
	case tea.KeyPressMsg:
		// A standalone ST is decoded as Alt+backslash by this input reader.
		terminator = event.Code == '\\' && event.Mod == tea.ModAlt
		if !terminator {
			if event.Mod != 0 || event.Text == "" || strings.ContainsAny(event.Text, "\x1b\r\n") || len(r.buffer)+len(event.Text) > terminalReplyLimit {
				return r.abort(msg)
			}
			r.buffer += event.Text
			r.keys = append(r.keys, msg)
			return nil
		}
	case uv.UnknownEvent:
		terminator = string(event) == "\x1b\\" || string(event) == "\x9c"
		if !terminator {
			return r.abort(msg)
		}
	case tea.MouseMsg, tea.PasteMsg, tea.PasteStartMsg, tea.FocusMsg, tea.BlurMsg:
		return r.abort(msg)
	default:
		// Snapshot, animation and resize messages don't consume input bytes.
		return msg
	}
	if terminator {
		encoded := r.buffer + "\x1b\\"
		var decoder uv.EventDecoder
		n, decoded := decoder.Decode([]byte(encoded))
		if n == len(encoded) {
			switch event := decoded.(type) {
			case uv.CapabilityEvent:
				r.buffer, r.keys = "", nil
				return tea.CapabilityMsg(event)
			case uv.TerminalVersionEvent:
				r.buffer, r.keys = "", nil
				return tea.TerminalVersionMsg(event)
			case uv.KittyGraphicsEvent, uv.UnknownApcEvent:
				r.buffer, r.keys = "", nil
				return event
			}
			if strings.HasPrefix(encoded, "\x1bP0+r") {
				r.buffer, r.keys = "", nil
				return nil // A negative reply must not enter the composer.
			}
		}
	}
	return r.abort(msg)
}
