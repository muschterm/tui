package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// Capability state belongs to this terminal connection, never the saved client
// view. A server reconnect does not restart terminal negotiation.
type terminalColorProbe struct {
	noColor, skipProbe                      bool
	versionRequested, capabilitiesRequested bool
	confirmed                               bool
}

func (m *Model) terminalColorOptions(getenv func(string) string) []tea.ProgramOption {
	m.colorProbe = terminalColorProbe{
		noColor:   getenv("NO_COLOR") != "",
		skipProbe: getenv("TERM_PROGRAM") == "Apple_Terminal",
	}
	// Use neutral surfaces even in the initial cached frame before Bubble Tea
	// delivers its environment/terminfo/tmux detection result.
	m.colorProfile = colorprofile.ANSI
	options := []tea.ProgramOption{tea.WithFilter(m.filterColorReports)}
	if m.colorProbe.noColor {
		// colorprofile v0.4.3 recognizes boolean NO_COLOR values only. Honor
		// every nonempty value, including "0", without enabling later probes.
		m.colorProfile = colorprofile.ASCII
		options = append(options, tea.WithColorProfile(colorprofile.ASCII))
	}
	m.configureInputs()
	return options
}

func (m *Model) filterColorReports(_ tea.Model, msg tea.Msg) tea.Msg {
	msg = m.filterTerminalReply(msg, time.Now())
	// Bubble Tea upgrades its renderer before forwarding CapabilityMsg to us.
	// Only allow color replies to our own probes; unsolicited replies must not
	// override NO_COLOR or a conservative fallback.
	if report, ok := msg.(tea.CapabilityMsg); ok && (report.Content == "RGB" || report.Content == "Tc" || report.Content == "RGB=8") {
		if m.colorProbe.noColor || !m.colorProbe.capabilitiesRequested {
			return nil
		}
		// iTerm2 reports eight bits per component. Bubble Tea v2.0.9 only
		// recognizes the boolean form; normalize this verified numeric form.
		if report.Content == "RGB=8" {
			return tea.CapabilityMsg{Content: "RGB"}
		}
	}
	return msg
}

func (m *Model) updateColorProfile(profile colorprofile.Profile) tea.Cmd {
	if m.colorProbe.noColor {
		profile = colorprofile.ASCII
	}
	if m.colorProfile != profile {
		m.colorProfile = profile
		m.configureInputs()
		m.promptView.Refresh(&m.prompt, m.promptMetrics.Total)
		m.answerView.Refresh(&m.answer, m.answerMetrics.Total)
	}
	if (profile != colorprofile.ANSI && profile != colorprofile.ANSI256) || m.colorProbe.noColor || m.colorProbe.skipProbe || m.colorProbe.versionRequested {
		return nil
	}
	m.colorProbe.versionRequested = true
	return tea.RequestTerminalVersion
}

func (m *Model) probeTerminalColors(name string) tea.Cmd {
	if !m.colorProbe.versionRequested || m.colorProbe.capabilitiesRequested || m.colorProbe.noColor || m.colorProbe.skipProbe || (m.colorProfile != colorprofile.ANSI && m.colorProfile != colorprofile.ANSI256) {
		return nil
	}
	if !supportsColorQueries(name) {
		return nil
	}
	m.colorProbe.capabilitiesRequested = true
	// Separate requests are required: Bubble Tea recognizes affirmative boolean
	// RGB/Tc replies individually. Silence/negative replies leave the fallback;
	// there is no wait, timer, retry or tmux passthrough.
	return tea.Batch(tea.RequestCapability("RGB"), tea.RequestCapability("Tc"))
}

func supportsColorQueries(name string) bool {
	// Querying XTGETTCAP blindly can corrupt Apple Terminal output (documented
	// in Bubble Tea v2.0.9). An actual XTVERSION reply gates known DCS parsers;
	// it is NOT evidence of RGB support. Only the subsequent reply upgrades it.
	name = strings.ToLower(name)
	for _, prefix := range []string{"ghostty ", "kitty(", "iterm2 "} {
		if strings.HasPrefix(name, prefix) && strings.Trim(strings.TrimPrefix(name, prefix), " )") != "" {
			return true
		}
	}
	return false
}

// colorDiagnostic is the structured color diagnostic: the effective profile
// and an optional caveat about how it was established.
type colorDiagnostic struct{ profile, note string }

func (m *Model) colorDiagnosticParts() colorDiagnostic {
	if m.colorProbe.noColor {
		return colorDiagnostic{profile: "disabled by NO_COLOR"}
	}
	if m.colorProbe.confirmed {
		return colorDiagnostic{profile: "true color", note: "terminal capability confirmed"}
	}
	if m.colorProfile == colorprofile.TrueColor {
		return colorDiagnostic{profile: "true color", note: "startup detection"}
	}
	return colorDiagnostic{profile: m.colorProfile.String() + " fallback", note: "true color unconfirmed"}
}

func (m *Model) colorDiagnostics() string {
	d := m.colorDiagnosticParts()
	if d.note == "" {
		return "Colors: " + d.profile
	}
	return "Colors: " + d.profile + " · " + d.note
}
