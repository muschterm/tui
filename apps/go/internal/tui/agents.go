package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	agentoptions "github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// fixtureAgentName is the legacy Agent value carried by threads created before
// agent identities existed. It still resolves to the fixture agent record.
const fixtureAgentName = "Fixture agent"

// settingFieldOrder is the composer's configurable fields in validation order.
// Each maps to at most one agent config option through Agent.Fields.
var settingFieldOrder = []string{"model", "effort", "permissions", "context", "speed"}

// acpAgents reports whether this server publishes agent records. Older servers
// keep the fixture-only composer rules unchanged.
func (m *Model) acpAgents() bool { return m.hasCapability("acp-agents") && len(m.snapshot.Agents) > 0 }

// agentByID resolves an agent record by ID, or by name for the legacy value.
func (m *Model) agentByID(value string) (protocol.Agent, bool) {
	if value == "" {
		return protocol.Agent{}, false
	}
	for _, a := range m.snapshot.Agents {
		if a.ID == value {
			return a, true
		}
	}
	for _, a := range m.snapshot.Agents {
		if strings.EqualFold(a.Name, value) {
			return a, true
		}
	}
	return protocol.Agent{}, false
}

// threadAgent resolves the agent a thread or creation draft is bound to.
func (m *Model) threadAgent(t protocol.Thread) (protocol.Agent, bool) {
	if a, ok := m.agentByID(t.AgentID); ok {
		return a, true
	}
	return m.agentByID(t.Agent)
}

// agentCommandID is the value thread.start carries: the agent ID when the
// record is known, otherwise the retained legacy value.
func (m *Model) agentCommandID(t protocol.Thread) string {
	if a, ok := m.threadAgent(t); ok {
		return a.ID
	}
	return t.Agent
}

// defaultAgent is the agent a new draft starts with: the fixture agent when it
// exists, else the first ready record, else the first configured one.
func (m *Model) defaultAgent() (protocol.Agent, bool) {
	if len(m.snapshot.Agents) == 0 {
		return protocol.Agent{}, false
	}
	for _, a := range m.snapshot.Agents {
		if a.Kind == "fixture" {
			return a, true
		}
	}
	for _, a := range m.snapshot.Agents {
		if a.State == "ready" {
			return a, true
		}
	}
	return m.snapshot.Agents[0], true
}

// newDraftView builds a per-project creation draft bound to the default agent.
func (m *Model) newDraftView() *threadView {
	if a, ok := m.defaultAgent(); ok {
		return &threadView{Agent: a.ID, Settings: agentConfigFor(a).defaults()}
	}
	return &threadView{Agent: fixtureAgentName, Settings: agentConfigFor(protocol.Agent{Kind: "fixture"}).defaults()}
}

// agentProjectID is the checkout a probe should use: the draft's destination
// project, otherwise the selected thread's project.
func (m *Model) agentProjectID() string {
	if m.creatingThread() {
		return m.state.DraftProjectID
	}
	return m.thread().ProjectID
}

// agentReadiness is the short state word shown beside an agent's name. It is
// the server's reported state, never inferred from the agent's name.
func agentReadiness(a protocol.Agent) string {
	if a.Kind == "fixture" {
		return "ready · fixture"
	}
	if a.State == "" {
		return "unknown"
	}
	return safe(a.State)
}

// agentUnready names why an agent cannot accept a prompt yet.
func agentUnready(a protocol.Agent) string {
	if a.Kind == "fixture" || a.State == "ready" {
		return ""
	}
	name := safe(a.Name)
	reason := ""
	switch a.State {
	case "unprobed", "":
		reason = name + " has no probed options · choose Probe first"
	case "probing":
		reason = "Probing " + name + " · wait for its options"
	case "unauthenticated":
		reason = name + " needs authentication in its own CLI first"
	case "unavailable":
		reason = name + " is unavailable"
	default:
		reason = name + " is " + safe(a.State)
	}
	if detail := strings.TrimSpace(singleLine(a.Detail)); detail != "" {
		reason += " · " + detail
	}
	return reason
}

// agentNeedsProbe reports an ACP agent whose options are missing or stale, so
// the composer can offer an explicit probe instead of inventing choices.
func agentNeedsProbe(a protocol.Agent) bool {
	if a.Kind == "fixture" || a.State == "probing" {
		return false
	}
	return a.State == "" || a.State == "unprobed" || len(a.Options) == 0
}

// agentConfig is the option catalogue in effect for one thread or draft. The
// catalogue is dynamic, so it is read from the snapshot on every use and never
// cached in client state.
type agentConfig struct {
	agent   protocol.Agent
	options []protocol.ConfigOption
}

// threadConfig prefers the live session's catalogue: a running session replaces
// it whole, and the probed agent catalogue is only the starting point.
func (m *Model) threadConfig(t protocol.Thread) (agentConfig, bool) {
	agent, ok := m.threadAgent(t)
	if !ok {
		return agentConfig{}, false
	}
	options := agent.Options
	if len(t.Options) > 0 {
		options = t.Options
		agent.Fields = agentoptions.Fields(options)
	}
	return agentConfig{agent: agent, options: options}, true
}

func (m *Model) composerConfig() (agentConfig, bool) { return m.threadConfig(m.thread()) }

// agentConfigFor is the probe-time catalogue, used before a session exists.
func agentConfigFor(a protocol.Agent) agentConfig { return agentConfig{agent: a, options: a.Options} }

// option returns the config option feeding one composer field. Option IDs are
// adapter-specific, so only Agent.Fields may resolve them.
func (c agentConfig) option(field string) (protocol.ConfigOption, bool) {
	id := ""
	switch field {
	case "model":
		id = c.agent.Fields.Model
	case "effort":
		id = c.agent.Fields.Effort
	case "permissions":
		id = c.agent.Fields.Permissions
	case "context":
		id = c.agent.Fields.Context
	case "speed":
		id = c.agent.Fields.Speed
	}
	if id == "" {
		return protocol.ConfigOption{}, false
	}
	for _, o := range c.options {
		if o.ID == id {
			return o, true
		}
	}
	return protocol.ConfigOption{}, false
}

// unmapped are the remaining options. They are displayed read-only as opaque
// agent defaults; nothing here is selectable.
func (c agentConfig) unmapped() []protocol.ConfigOption {
	mapped := map[string]bool{}
	for _, field := range settingFieldOrder {
		if o, ok := c.option(field); ok {
			mapped[o.ID] = true
		}
	}
	var rest []protocol.ConfigOption
	for _, o := range c.options {
		if !mapped[o.ID] {
			rest = append(rest, o)
		}
	}
	return rest
}

func optionValue(o protocol.ConfigOption, value string) (protocol.ConfigValue, bool) {
	for _, v := range o.Values {
		if v.Value == value {
			return v, true
		}
	}
	return protocol.ConfigValue{}, false
}

// optionValueName is the agent's own display name for a value, or "" when the
// value is unknown to this option.
func optionValueName(o protocol.ConfigOption, value string) string {
	if v, ok := optionValue(o, value); ok {
		return v.Name
	}
	return ""
}

func settingValue(s protocol.Settings, field string) string {
	switch field {
	case "model":
		return s.Model
	case "effort":
		return s.Effort
	case "permissions":
		return s.Permissions
	case "context":
		return s.Context
	case "speed":
		return s.Speed
	}
	return ""
}

func setSettingValue(s *protocol.Settings, field, value string) {
	switch field {
	case "model":
		s.Model = value
	case "effort":
		s.Effort = value
	case "permissions":
		s.Permissions = value
	case "context":
		s.Context = value
	case "speed":
		s.Speed = value
	}
}

// defaults are the catalogue's current option values. Fields with no option
// stay empty: they are the agent's own defaults and are never selected here.
func (c agentConfig) defaults() protocol.Settings {
	if c.agent.Kind == "fixture" {
		return protocol.Settings{Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
	}
	var s protocol.Settings
	for _, field := range settingFieldOrder {
		if o, ok := c.option(field); ok {
			setSettingValue(&s, field, o.Current)
		}
	}
	return s
}

// blocked validates a selection against the catalogue in effect. Unmapped
// fields carry the agent's defaults and are not validated here.
func (c agentConfig) blocked(s protocol.Settings) string {
	for _, field := range settingFieldOrder {
		o, ok := c.option(field)
		if !ok {
			continue
		}
		value := settingValue(s, field)
		if value == "" {
			return "Choose a " + field + " for " + safe(c.agent.Name)
		}
		if _, ok := optionValue(o, value); !ok {
			return safe(value) + " is no longer an offered " + field + " for " + safe(c.agent.Name)
		}
	}
	return ""
}

// fixtureSettingsBlocked is the demo agent's unchanged fixed-option rule.
func fixtureSettingsBlocked(s protocol.Settings) string {
	if !slices.Contains([]string{"low", "medium", "high"}, s.Effort) || s.Permissions != "fixture-only" || s.Context != "unavailable" || s.Speed != "standard" {
		return "Choose valid effort, permissions, context and speed settings"
	}
	return ""
}

// chooseAgent binds a creation draft to one agent and resets its dependent
// settings to that agent's current option values. An authoritative thread keeps
// the agent it was created with.
func (m *Model) chooseAgent(id string) tea.Cmd {
	if m.configurationLocked() {
		return m.showNotice("Settings are read-only during active work")
	}
	agent, ok := m.agentByID(id)
	if !ok {
		return m.showNotice("That agent is no longer configured")
	}
	current, hasCurrent := m.threadAgent(m.thread())
	if !m.creatingThread() {
		if hasCurrent && current.ID == agent.ID {
			return m.probeOffer(agent)
		}
		name := "this thread's agent"
		if hasCurrent {
			name = safe(current.Name)
		}
		return m.showNotice("This thread keeps " + name + " · start a new thread to use another agent")
	}
	v := m.viewState()
	if !hasCurrent || current.ID != agent.ID {
		v.Agent, v.Settings = agent.ID, agentConfigFor(agent).defaults()
	}
	return m.probeOffer(agent)
}

// probeOffer asks for an explicit probe instead of guessing an unprobed or
// stale agent's options.
func (m *Model) probeOffer(a protocol.Agent) tea.Cmd {
	if !agentNeedsProbe(a) {
		return nil
	}
	m.showMenu("Settings · agent", []menuItem{
		{probeLabel(a), action{Kind: "agent-probe", ID: a.ID}},
		{"Keep " + safe(a.Name) + " agent defaults", action{Kind: "noop"}},
	})
	return nil
}

// chooseSetting captures one option value. Changing the model keeps the other
// values only while the agent still offers them.
func (m *Model) chooseSetting(field, value string) tea.Cmd {
	if m.configurationLocked() {
		return m.showNotice("Settings are read-only during active work")
	}
	c, ok := m.composerConfig()
	if !ok {
		return m.showNotice("Choose an agent first")
	}
	o, has := c.option(field)
	if !has {
		return m.showNotice(safe(c.agent.Name) + " offers no " + field + " option")
	}
	if _, ok := optionValue(o, value); !ok {
		return m.showNotice("That " + field + " is no longer offered · reprobe " + safe(c.agent.Name))
	}
	v := m.viewState()
	setSettingValue(&v.Settings, field, value)
	if field == "model" {
		for _, dependent := range settingFieldOrder[1:] {
			option, mapped := c.option(dependent)
			if !mapped {
				continue
			}
			if _, valid := optionValue(option, settingValue(v.Settings, dependent)); !valid {
				setSettingValue(&v.Settings, dependent, option.Current)
			}
		}
	}
	return nil
}

// deliveryConfirmed reports a server-confirmed answer delivery. Unknown values
// remain unconfirmed rather than assumed successful.
func deliveryConfirmed(delivery string) bool {
	switch delivery {
	case "fixture-confirmed", "confirmed":
		return true
	}
	return false
}
