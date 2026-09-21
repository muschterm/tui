package tui

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func (m *Model) renderChrome(f *frame, g shell.Geometry) {
	p := m.colors()
	f.fill(g.Top, p, p.nav)
	if m.singleColumn() {
		f.button(m, 0, 0, 5, centered(m.icon("menu"), 5), "columns", action{Kind: "columns"}, p.blue, p.nav)
		f.hits[len(f.hits)-1].Label = "Choose column · F2"
	} else {
		f.button(m, 0, 0, 5, m.paneIcon("left", g.Left.W > 0), "left", action{Kind: "left"}, p.blue, p.nav)
		f.hits[len(f.hits)-1].Label = paneHelp("navigation", g.Left.W > 0, "F2")
	}
	rightVisible := g.Right.W > 0 && g.Right.H > 0
	paneWidth := 13
	if rightVisible {
		paneWidth += 6
	}
	if m.singleColumn() {
		paneWidth = 0
	}
	attentionWidth := m.attentionWidth()
	controlX := m.width - paneWidth
	attX := controlX - attentionWidth - 1
	breadcrumb := "tui-go"
	if t := m.thread(); m.hasComposer() {
		breadcrumb = t.Project + "  /  " + t.Title
	}
	if m.singleColumn() {
		breadcrumb = columnName(m.viewState().CompactColumn)
	}
	f.text(6, 0, max(0, attX-7), breadcrumb, p.text, p.nav)
	m.renderAttention(f, attX)
	if m.singleColumn() {
		return // Column selection replaces pane toggles and maximize.
	}
	if rightVisible {
		icon := "maximize"
		if m.state.Layout.Maximized {
			icon = "restore"
		}
		f.button(m, controlX, 0, 6, "  "+m.icon(icon)+"  ", "maximize", action{Kind: "maximize"}, p.blue, p.nav)
		f.hits[len(f.hits)-1].Label = "Maximize / restore right panel · F7"
		controlX += 6
	}
	f.button(m, controlX, 0, 6, m.paneIcon("bottom", g.Bottom.H > 0), "bottom", action{Kind: "bottom"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = paneHelp("bottom panel", g.Bottom.H > 0, "F5")
	f.button(m, controlX+6, 0, 7, m.paneIcon("right", rightVisible), "right", action{Kind: "right"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = paneHelp("right panel", rightVisible, "F3")
}

func (m *Model) attentionCount() int {
	attention := 0
	for _, t := range m.snapshot.Threads {
		for _, r := range t.Requests {
			if r.State == "pending" {
				attention++
			}
		}
		if t.State == "failed" {
			attention++
		}
	}
	return attention
}
func (m *Model) attentionWidth() int {
	width := ansi.StringWidth(m.icon("attention")) + 2
	if count := m.attentionCount(); count > 0 {
		width += len(strconv.Itoa(count)) + 3
	}
	return width
}
func (m *Model) renderAttention(f *frame, attX int) {
	p := m.colors()
	attention := m.attentionCount()
	attentionWidth := m.attentionWidth()
	bellWidth := ansi.StringWidth(m.icon("attention"))
	badge := ""
	if attention > 0 {
		badge = strconv.Itoa(attention)
	}
	fg := p.muted
	if attention > 0 {
		fg = p.gold
	}
	f.button(m, attX, 0, attentionWidth, " "+m.icon("attention"), "attention", action{Kind: "attention"}, fg, p.nav)
	f.hits[len(f.hits)-1].Label = fmt.Sprintf("Open attention · %d pending items", attention)
	bg := p.nav
	if m.hover == "attention" || m.focus == "attention" {
		bg = m.hoverFill()
	}
	f.text(attX+1, 0, bellWidth, m.icon("attention"), fg, bg)
	if badge != "" {
		f.text(attX+bellWidth+2, 0, ansi.StringWidth(badge)+2, " "+badge+" ", p.nav, p.gold)
	}
}
