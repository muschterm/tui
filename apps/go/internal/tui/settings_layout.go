package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
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
		m.renderSettingsRow(f, rows[offset+i], x, f.settingsBody.Y+i, width, i == 0)
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
	if slices.ContainsFunc(f.hits, func(h hit) bool { return h.Key == m.focus }) {
		return
	}
	if key, ok := settingsFocusFallback(f.hits, m.focus); ok {
		m.setFocus(key)
		return
	}
	key := "sidebar-settings"
	if f.settingsBody.H == 0 {
		key = "settings-category:" + m.settingsPage
	}
	m.setFocus(key)
}

// settingsFocusFallback recovers keyboard focus across a resize that swaps a
// setting between its segmented-choice band and its single menu-button
// fallback. A segment key ("sidebar-setting:<setting>:<value>") that vanished
// falls back to that setting's button key; a button key that vanished
// recovers to the setting's currently selected segment, or its first segment
// when none is marked selected.
func settingsFocusFallback(hits []hit, focus string) (string, bool) {
	const prefix = "sidebar-setting:"
	if !strings.HasPrefix(focus, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(focus, prefix)
	setting := rest
	isSegment := false
	if i := strings.Index(rest, ":"); i >= 0 {
		setting, isSegment = rest[:i], true
	}
	buttonKey := prefix + setting
	if isSegment {
		if slices.ContainsFunc(hits, func(h hit) bool { return h.Key == buttonKey }) {
			return buttonKey, true
		}
		return "", false
	}
	segmentPrefix := buttonKey + ":"
	first := ""
	for _, h := range hits {
		if !strings.HasPrefix(h.Key, segmentPrefix) {
			continue
		}
		if first == "" {
			first = h.Key
		}
		if h.Action.Kind == "noop" {
			return h.Key, true
		}
	}
	if first != "" {
		return first, true
	}
	return "", false
}

func (m *Model) openSettingsCommands() {
	var items []menuItem
	for _, page := range m.settingsCategories() {
		items = append(items, menuItem{Label: title(page), Action: action{Kind: "settings-page", Value: page}})
	}
	items = append(items, menuItem{Label: "Back to workspace", Action: action{Kind: "settings-back"}}, menuItem{Label: "Retry pending command", Action: action{Kind: "retry"}}, menuItem{Label: "Detach TUI", Action: action{Kind: "quit"}})
	m.showMenu("Settings", items)
}

func settingsPaneKey(key string) bool {
	return slices.Contains([]string{"f3", "f5", "f7", "ctrl+s"}, key) || strings.HasPrefix(key, "alt+")
}

func (m *Model) renderSettingsRow(f *frame, row settingsRow, x, y, width int, first bool) {
	p := m.colors()
	switch row.kind {
	case settingsButton:
		fg := p.blue
		if row.danger {
			fg = p.red
		}
		m.renderSettingsButton(f, row, x, y, width, fg, first)
	case settingsHeading:
		panelSectionHeading(f, m, x, y, width, row.label)
	case settingsRule:
		panelRule(f, m, x, y, width)
	case settingsPair:
		panelPairRow(f, m, x, y, width, row.label, row.value)
	case settingsToggle:
		m.renderSettingsToggle(f, row, x, y, width)
	case settingsSegments:
		m.renderSettingsSegments(f, row, x, y, width, first)
	default:
		f.componentText(x, y, width, row.label, m.componentStyle(squareFill, componentState{}, p.muted, p.canvas))
	}
}

// The whole toggle row is one control: its label at the left, the On/Off word
// and switch at the right. Hover and focus fill the row like a square-fill
// control; the word keeps the state readable without color.
func (m *Model) renderSettingsToggle(f *frame, row settingsRow, x, y, width int) {
	p := m.colors()
	v := m.componentStyle(squareFill, m.controlState(false, row.key), p.text, p.canvas)
	marked := v.focused && f.blank(x-1, y)
	word := fit(row.value, 3)
	right := len(word) + 1 + toggleTrackWidth
	label := row.label
	if right < width {
		// Leave at least one blank cell before the word and switch so a long
		// label never overlaps them.
		label = ansi.Truncate(singleLine(label), max(0, width-right-1), "…")
	}
	f.styledText(x, y, width, label, v, v.focused && !marked)
	wordInk := p.muted
	if row.on {
		wordInk = p.blue
	}
	wv := v
	wv.foreground, wv.bold = wordInk, row.on
	if right < width {
		f.componentText(x+width-right, y, len(word)+1, word+" ", wv)
		f.put(shell.Rect{X: x + width - toggleTrackWidth, Y: y, W: toggleTrackWidth, H: 1}, m.toggleTrack(row.on))
	}
	if marked {
		f.focusMark(x-1, y, v, v.base)
	}
	f.hits = append(f.hits, hit{Rect: shell.Rect{X: x, Y: y, W: width, H: 1}, Action: row.action, Label: row.label + ": " + row.value, Key: row.key})
}

// A settings button is a square-fill field whose label is inset by one cell;
// with bands it reads as a two-cell-tall field, and its focus mark takes the
// blank cell before the label row.
func (m *Model) renderSettingsButton(f *frame, row settingsRow, x, y, width int, fg string, first bool) {
	p := m.colors()
	v := m.componentStyle(squareFill, m.controlState(false, row.key), fg, p.input)
	v.base = p.canvas
	if !f.paintPanelBandEdge(x, y, width, row.band, row.bandRows, v) {
		marked := v.focused && f.blank(x-1, y)
		f.styledText(x, y, width, " "+row.label, v, v.focused && !marked)
		if marked {
			f.focusMark(x-1, y, v, v.base)
		}
	}
	f.registerPanelBandHit(f.settingsBody, y, row.band, row.bandRows, first, hit{Rect: shell.Rect{X: x, W: width}, Action: row.action, Label: row.label, Key: row.key})
}

// Segments paint equal square fills across the row. With bands, a lower
// half-block row above and an upper half-block row below in each segment's
// fill make a three-row band read as a taller button; the hit rectangle covers
// the visible part of the whole band. The focus mark takes the blank cell
// before the focused segment on its label row.
func (m *Model) renderSettingsSegments(f *frame, row settingsRow, x, y, width int, first bool) {
	p := m.colors()
	xs, ws := panelSegmentLayout(x, width, len(row.segments))
	for i, s := range row.segments {
		v := m.componentStyle(squareFill, m.controlState(s.selected, s.key), p.text, p.input)
		v.base = p.canvas // Band edges and the focus mark sit on the canvas.
		if row.bandRows == 1 {
			f.compactControl(m, xs[i], y, ws[i], centered(ansi.Truncate(s.label, ws[i]-2, "…"), ws[i]-2), v)
		} else if !f.paintPanelBandEdge(xs[i], y, ws[i], row.band, row.bandRows, v) {
			f.componentText(xs[i], y, ws[i], centered(s.label, ws[i]), v)
			if v.focused {
				f.focusMark(xs[i]-1, y, v, v.base)
			}
		}
		f.registerPanelBandHit(f.settingsBody, y, row.band, row.bandRows, first, hit{Rect: shell.Rect{X: xs[i], W: ws[i]}, Action: s.action, Label: s.label, Key: s.key})
	}
}
