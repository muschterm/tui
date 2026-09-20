package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const redrawInterval = time.Second / 60

type redrawMsg struct{}

// Bubble Tea limits terminal output, but still calls View after every message.
// Keep input/state transitions immediate and coalesce only frame construction.
// At most one redraw timer is pending; idle clients do not run a render loop.
type pacedModel struct {
	model   *Model
	view    tea.View
	pending bool
}

func pace(m *Model) *pacedModel      { return &pacedModel{model: m, view: m.View()} }
func (m *pacedModel) Init() tea.Cmd  { return m.model.Init() }
func (m *pacedModel) View() tea.View { return m.view }

func (m *pacedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(redrawMsg); ok {
		m.pending = false
		m.view = m.model.View()
		return m, nil
	}
	_, cmd := m.model.Update(msg)
	if m.pending {
		return m, cmd
	}
	m.pending = true
	return m, tea.Batch(cmd, tea.Tick(redrawInterval, func(time.Time) tea.Msg { return redrawMsg{} }))
}
