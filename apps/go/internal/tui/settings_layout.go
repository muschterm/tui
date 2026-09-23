package tui

import (
	"slices"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/shell"
)

// Settings is a temporary presentation, never a mutation of the conversation's
// pane geometry or terminal/surface ownership. Compact screens choose categories
// or the form; wide screens keep both visible.
func (m *Model) settingsGeometry() shell.Geometry {
	body := shell.Rect{Y: 1, W: m.width, H: max(0, m.height-2)}
	g := shell.Geometry{Center: body}
	if m.singleColumn() {
		if m.settingsNavigation {
			g.Left, g.Center = body, shell.Rect{}
		}
		return g
	}
	width := min(max(20, m.state.Layout.LeftWidth), min(36, m.width-32))
	g.Left = shell.Rect{Y: body.Y, W: width, H: body.H}
	g.Center = shell.Rect{X: width + 1, Y: body.Y, W: m.width - width - 1, H: body.H}
	return g
}

func (m *Model) settingsCategories() []string {
	if m.settingsProjectID != "" {
		return []string{"project", "general", "keybindings"}
	}
	return []string{"general", "agents", "appearance", "keybindings", "about"}
}

func (m *Model) settingsBreadcrumb() string {
	label := "Settings / " + title(m.settingsPage)
	if p, ok := m.projectByID(m.settingsProjectID); ok {
		label += " / " + singleLine(p.Name)
	}
	return label
}

func (m *Model) renderSidebarSettings(f *frame, r shell.Rect) {
	p := m.colors()
	f.fill(r, p, p.nav)
	x, width := r.X+2, max(1, r.W-4)
	visual := m.componentStyle(squareFill, componentState{}, p.text, p.nav)
	visual.bold = true
	f.componentText(x, r.Y+1, width, "Settings", visual)
	for i, page := range m.settingsCategories() {
		key := "settings-category:" + page
		label := title(page)
		f.styledButton(x, r.Y+3+i*2, width, label, key, action{Kind: "settings-page", Value: page}, m.componentStyle(squareFill, m.controlState(m.settingsPage == page, key), p.text, p.nav))
	}
	f.button(m, x, r.Y+r.H-2, width, m.icon("previous")+" Back", "settings-back", action{Kind: "settings-back"}, p.text, p.nav)
}

func (m *Model) renderSettingsContent(f *frame, r shell.Rect) {
	p := m.colors()
	width := min(68, max(1, r.W-6))
	x := r.X + min(3, max(1, (r.W-width)/2))
	name, rows := m.sidebarSettingsRows(width)
	if m.singleColumn() {
		if project, ok := m.projectByID(m.settingsProjectID); ok {
			name += " / " + singleLine(project.Name)
		}
	}
	visual := m.componentStyle(squareFill, componentState{}, p.text, p.canvas)
	visual.bold = true
	f.componentText(x, r.Y+1, width, name, visual)
	f.settingsBody = shell.Rect{X: x, Y: r.Y + 3, W: width, H: max(0, r.H-4)}
	f.settingsMax = max(0, len(rows)-f.settingsBody.H)
	offset := min(max(0, m.settingsScroll), f.settingsMax)
	f.hits = append(f.hits, hit{Rect: f.settingsBody, Action: action{}, Label: "Settings · wheel / arrows to scroll", Key: "sidebar-settings"})
	for i := 0; i < f.settingsBody.H && offset+i < len(rows); i++ {
		row, y := rows[offset+i], f.settingsBody.Y+i
		if row.key != "" {
			fg := p.blue
			if row.danger {
				fg = p.red
			}
			f.button(m, x, y, width, row.label, row.key, row.action, fg, p.input)
		} else {
			v := m.componentStyle(squareFill, componentState{}, p.muted, p.canvas)
			if row.heading {
				v.foreground, v.bold = p.text, true
			}
			f.componentText(x, y, width, row.label, v)
		}
	}
	f.scrollbar(m, shell.Rect{X: x + width + 1, Y: f.settingsBody.Y, W: 1, H: f.settingsBody.H}, "sidebar-settings", len(rows), f.settingsBody.H, offset, p.canvas)
}

func (m *Model) renderSettingsWorkspace(f *frame) {
	p := m.colors()
	g := f.geom
	f.fill(shell.Rect{W: m.width, H: 1}, p, p.nav)
	if m.singleColumn() {
		f.iconButton(m, 1, 0, 5, centered(m.icon("menu"), 5), "settings-nav", action{Kind: "settings-nav"}, p.text, p.nav)
		f.hits[len(f.hits)-1].Label = "Settings categories · F2"
	}
	attentionX := m.width - 5 - m.attentionWidth()
	f.text(7, 0, max(1, attentionX-8), m.settingsBreadcrumb(), p.text, p.nav)
	m.renderAttention(f, attentionX, p.nav)
	f.iconButton(m, m.width-4, 0, 3, centered(m.icon("close"), 3), "settings-close", action{Kind: "settings-back"}, p.muted, p.nav)
	f.hits[len(f.hits)-1].Label = "Return to workspace · Esc"
	if g.Left.W > 0 {
		m.renderSidebarSettings(f, g.Left)
		if g.Center.W > 0 {
			for y := g.Left.Y; y < g.Left.Y+g.Left.H; y++ {
				f.text(g.Left.W, y, 1, "│", p.line, p.canvas)
			}
		}
	}
	if g.Center.W > 0 {
		m.renderSettingsContent(f, g.Center)
	}
	status := "Tab Navigate · F6 Scroll settings · Esc Back"
	if m.singleColumn() {
		status = "F2 Categories · Esc Back"
	}
	for _, h := range f.hits {
		if h.Key == m.hover && m.hover != "" || m.hover == "" && h.Key == m.focus {
			status = h.Label
			break
		}
	}
	if m.notice.text != "" {
		status = m.notice.text
	}
	if !m.connected {
		status = "Disconnected · " + status
	}
	f.text(1, m.height-1, m.width-2, status, p.muted, p.nav)
	if len(m.menu) > 0 {
		m.renderMenu(f)
	}
}

func (m *Model) configureSettingsFocus(f frame) {
	if f.settingsBody.H > 0 {
		m.settingsScroll = min(max(0, m.settingsScroll), f.settingsMax)
	}
	if len(m.menu) > 0 {
		return
	}
	if !slices.ContainsFunc(f.hits, func(h hit) bool { return h.Key == m.focus }) {
		key := "sidebar-settings"
		if f.settingsBody.H == 0 {
			key = "settings-category:" + m.settingsPage
		}
		m.setFocus(key)
	}
}

func (m *Model) openSettingsCommands() {
	var items []menuItem
	for _, page := range m.settingsCategories() {
		items = append(items, menuItem{title(page), action{Kind: "settings-page", Value: page}})
	}
	items = append(items, menuItem{"Back to workspace", action{Kind: "settings-back"}}, menuItem{"Retry pending command", action{Kind: "retry"}}, menuItem{"Detach TUI", action{Kind: "quit"}})
	m.showMenu("Settings", items)
}

func settingsPaneKey(key string) bool {
	return slices.Contains([]string{"f3", "f5", "f7", "ctrl+s"}, key) || strings.HasPrefix(key, "alt+")
}
