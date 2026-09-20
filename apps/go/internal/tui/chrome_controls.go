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
	f.button(m, 0, 0, 5, m.paneIcon("left", g.Left.W > 0), "left", action{Kind: "left"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = paneHelp("navigation", g.Left.W > 0, "F2")
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
	rightVisible := g.Right.W > 0 && g.Right.H > 0
	paneWidth := 13
	if rightVisible {
		paneWidth += 6
	}
	badge := ""
	if attention > 0 {
		badge = strconv.Itoa(attention)
	}
	bellWidth := ansi.StringWidth(m.icon("attention"))
	attentionWidth := bellWidth + 2
	if badge != "" {
		attentionWidth += ansi.StringWidth(badge) + 3
	}
	controlX := m.width - paneWidth
	attX := controlX - attentionWidth - 1
	breadcrumb := "tui-go"
	if t := m.thread(); t.ID != "" {
		breadcrumb = t.Project + "  /  " + t.Title
	}
	f.text(6, 0, max(0, attX-7), breadcrumb, p.text, p.nav)
	fg := p.muted
	if attention > 0 {
		fg = p.gold
	}
	f.button(m, attX, 0, attentionWidth, " "+m.icon("attention"), "attention", action{Kind: "attention"}, fg, p.nav)
	f.hits[len(f.hits)-1].Label = fmt.Sprintf("Open attention · %d pending items", attention)
	bg := p.nav
	if m.hover == "attention" || m.focus == "attention" {
		bg = p.selected
	}
	f.text(attX+1, 0, bellWidth, m.icon("attention"), fg, bg)
	if badge != "" {
		f.text(attX+bellWidth+2, 0, ansi.StringWidth(badge)+2, " "+badge+" ", p.nav, p.gold)
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
