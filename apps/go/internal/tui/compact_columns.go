package tui

import "github.com/muschterm/tui/apps/go/internal/shell"

// Column selection is per-thread presentation, independent of wide pane sizes
// and visibility. Resizing never rewrites the user's wider workspace.
func (m *Model) singleColumn() bool { return m.width < 60 }

func (m *Model) conversationVisible() bool {
	return !m.singleColumn() || m.viewState().CompactColumn == shell.CenterRegion
}

func (m *Model) workspaceGeometry(footer int) shell.Geometry {
	if m.settingsPage != "" {
		return m.settingsGeometry()
	}
	if m.singleColumn() {
		return shell.ColumnGeometry(m.width, m.height-1, footer, m.viewState().CompactColumn)
	}
	layout := m.state.Layout
	if m.state.Active == "" {
		layout.Bottom, layout.Right, layout.Maximized = false, false, false
	}
	return layout.Compute(m.width, m.height-1, footer)
}

func columnName(region shell.Region) string {
	switch region {
	case shell.LeftRegion:
		return "Projects & threads"
	case shell.RightRegion:
		return "Surfaces"
	case shell.BottomRegion:
		return "Terminal"
	default:
		return "Conversation"
	}
}

func (m *Model) openColumns() {
	var items []menuItem
	for _, region := range []shell.Region{shell.CenterRegion, shell.LeftRegion, shell.RightRegion, shell.BottomRegion} {
		if m.state.Active == "" && (region == shell.RightRegion || region == shell.BottomRegion) {
			continue
		}
		label := columnName(region)
		if region == m.viewState().CompactColumn {
			label += " · " + m.icon("check")
		}
		items = append(items, menuItem{label, action{Kind: "column", Index: int(region)}})
	}
	items = append(items, menuItem{"Commands", action{Kind: "commands"}})
	m.showMenu("Choose column", items)
}

func (m *Model) selectColumn(region shell.Region) {
	if region > shell.BottomRegion || !m.singleColumn() {
		return
	}
	m.viewState().CompactColumn = region
	key := "prompt"
	switch region {
	case shell.LeftRegion:
		key = "projects"
	case shell.RightRegion:
		key = "right-body"
	case shell.BottomRegion:
		key = "bottom-body"
	}
	m.setFocus(key)
}
