package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
	"github.com/rivo/uniseg"
)

func (m *Model) navigationSections() (open, closed []navigationRow) {
	query := strings.ToLower(strings.TrimSpace(m.state.ThreadFilter))
	for _, t := range m.snapshot.Threads {
		if jobThread(t) {
			continue // job threads belong to the Git operation panel
		}
		if m.state.ProjectFilter != "" && t.ProjectID != m.state.ProjectFilter || query != "" && !strings.Contains(strings.ToLower(t.Title), query) {
			continue
		}
		// Closed cards keep one content row; state and project stay in help.
		if t.Closed {
			closed = append(closed, navigationRow{"card-top", t}, navigationRow{"thread", t}, navigationRow{"card-bottom", t}, navigationRow{kind: "gap"})
		} else {
			open = append(open, navigationRow{"card-top", t}, navigationRow{"thread", t}, navigationRow{"state", t}, navigationRow{"card-bottom", t}, navigationRow{kind: "gap"})
		}
	}
	if len(open) > 0 {
		open = open[:len(open)-1]
	} else {
		open = []navigationRow{{kind: "empty"}}
	}
	if len(closed) > 0 {
		closed = closed[:len(closed)-1]
	}
	return
}

func threadSearchRect(r shell.Rect) shell.Rect {
	return shell.Rect{X: r.X + 2, Y: r.Y + 1, W: max(1, r.W-14), H: 1}
}

func navigationViewport(r shell.Rect) shell.Rect {
	return shell.Rect{X: r.X + 1, Y: r.Y + 3, W: max(1, r.W-2), H: max(0, r.H-7)}
}

// Reserve a fixed footer for settings and a separately scrolling Closed shelf.
// Expansion leaves an open card intact when both lists can remain usable.
// At tight heights prefer visible title/actions in both lists to a full open
// card beside only the Closed card's top border. Empty lists need one row.
func (m *Model) navigationLayout(r shell.Rect, openRows, closedRows int) (open, closed shell.Rect, heading int) {
	open = navigationViewport(r)
	heading = r.Y + r.H - 4
	if !m.state.RecentsCollapsed && !m.state.RecentsHidden && closedRows > 0 {
		openReserve := min(4, openRows)
		closedMinimum := min(2, closedRows)
		if open.H >= min(2, openRows)+closedMinimum {
			openReserve = min(openReserve, open.H-closedMinimum)
		}
		h := min(closedRows, max(0, open.H-openReserve))
		closed = shell.Rect{X: open.X, Y: heading - h + 1, W: open.W, H: h}
		heading -= h
		open.H = max(0, heading-open.Y)
	}
	return
}

func (m *Model) renderNav(f *frame, r shell.Rect) {
	p := m.colors()
	f.fill(r, p, p.nav)
	if m.settingsPage != "" {
		m.renderSidebarSettings(f, r)
		return
	}
	x, w := r.X+2, max(1, r.W-4)
	input := threadSearchRect(r)
	f.put(input, m.threadSearch.View())
	f.hits = append(f.hits, hit{Rect: input, Action: action{Kind: "focus", ID: "thread-search"}, Label: "Search thread titles", Key: "thread-search"})
	controls := x + w - 9
	f.iconButton(m, controls, r.Y+1, 3, centered(m.icon("folder"), 3), "projects", action{Kind: "projects"}, p.muted, p.nav)
	f.hits[len(f.hits)-1].Label = "Filter projects · All projects"
	for _, project := range m.snapshot.Projects {
		if project.ID == m.state.ProjectFilter {
			m.renderProjectBadge(f, controls, r.Y+1, project, p.nav)
			f.hits[len(f.hits)-1].Label = "Filter projects · " + project.Name
		}
	}
	f.iconButton(m, controls+3, r.Y+1, 3, centered(m.icon("project-add"), 3), "project-add", action{Kind: "project-add"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = "Add an existing project folder"
	f.iconButton(m, controls+6, r.Y+1, 3, centered(m.icon("compose"), 3), "thread-create", action{Kind: "thread-create"}, p.blue, p.nav)
	f.hits[len(f.hits)-1].Label = "New thread · also in Commands (F4)"
	open, closed := m.navigationSections()
	var heading int
	f.navigation, f.closedNavigation, heading = m.navigationLayout(r, len(open), len(closed))
	f.navMax = max(0, len(open)-f.navigation.H)
	f.closedMax = max(0, len(closed)-f.closedNavigation.H)
	m.renderNavigationRows(f, f.navigation, open, m.navScroll, "navigation")
	if f.closedNavigation.H > 0 {
		m.renderNavigationRows(f, f.closedNavigation, closed, m.closedScroll, "closed-navigation")
	}
	count := (len(closed) + 1) / 4
	// An uppercase, bold panel heading in the dimmed ink of closed rows;
	// the count shows only while collapsed.
	label := fmt.Sprintf("CLOSED (%d)", count)
	caret := m.icon("caret-down")
	a := action{Kind: "recents-collapse"}
	if !m.state.RecentsCollapsed && !m.state.RecentsHidden {
		label = "CLOSED"
		caret = m.icon("caret-up")
	}
	if m.state.RecentsHidden {
		label = "SHOW CLOSED"
		a.Kind = "recents-hide"
	}
	rule := "─"
	if m.plainIcons {
		rule = "-"
	}
	label += " " + strings.Repeat(rule, max(0, w-ansi.StringWidth(label)-ansi.StringWidth(caret)-2)) + " " + caret
	headingStyle := m.componentStyle(squareFill, m.controlState(false, "recents"), m.dimmed(), p.nav)
	headingStyle.bold = true
	f.styledButton(x, heading, w, label, "recents", a, headingStyle)
	// Use the existing gap above app actions; keep Closed and its viewport
	// fixed, and leave the separator outside all interactive hit rectangles.
	f.text(x, r.Y+r.H-3, w, strings.Repeat(rule, w), p.line, p.nav)
	f.iconButton(m, x, r.Y+r.H-2, 3, centered(m.icon("settings"), 3), "app-settings", action{Kind: "app-settings"}, p.muted, p.nav)
	f.hits[len(f.hits)-1].Label = "Settings"
}

func (m *Model) renderNavigationRows(f *frame, r shell.Rect, rows []navigationRow, scroll int, key string) {
	if r.H <= 0 {
		return
	}
	p := m.colors()
	f.hits = append(f.hits, hit{Rect: r, Action: action{}, Label: "Threads · wheel / arrows to scroll", Key: key})
	offset := min(max(0, scroll), max(0, len(rows)-r.H))
	x, w := r.X+1, max(1, r.W-2)
	for i := 0; i < r.H && offset+i < len(rows); i++ {
		row, y := rows[offset+i], r.Y+i
		card := shell.Rect{X: r.X, Y: y, W: r.W, H: 1}
		switch row.kind {
		case "card-top", "card-bottom":
			m.renderThreadCardBackground(f, card, row.thread, row.kind)
		case "thread":
			m.renderThreadCardBackground(f, card, row.thread, row.kind)
			m.renderThreadRow(f, shell.Rect{X: x, Y: y, W: w, H: 1}, row.thread)
		case "state":
			m.renderThreadCardBackground(f, card, row.thread, row.kind)
			label := row.thread.State
			if m.state.ProjectFilter == "" {
				label += " · " + row.thread.Project
			}
			f.text(x+2, y, max(1, w-3), label, p.muted, p.nav)
		case "empty":
			label := "No open threads"
			if strings.TrimSpace(m.state.ThreadFilter) != "" {
				label = "No matching open threads"
			}
			f.text(x, y, w, label, p.muted, p.nav)
		}
	}
	f.scrollbar(m, shell.Rect{X: r.X + r.W, Y: r.Y, W: 1, H: r.H}, key, len(rows), r.H, offset, p.nav)
}

// First and last non-space graphemes stay stable across multi-word names.
// Wide graphemes fit as one complete glyph; never split a displayed character.
func projectMonogram(name string) string {
	g := uniseg.NewGraphemes(strings.TrimSpace(safe(name)))
	var letters []string
	for g.Next() {
		s := g.Str()
		if strings.TrimFunc(s, unicode.IsSpace) != "" {
			letters = append(letters, s)
		}
	}
	if len(letters) == 0 {
		return "??"
	}
	first := strings.ToUpper(letters[0])
	last := strings.ToUpper(letters[len(letters)-1])
	if len(letters) == 1 {
		return centered(ansi.Truncate(first, 2, ""), 2)
	}
	return centered(ansi.Truncate(first+last, 2, ""), 2)
}

// Automatic colors are derived from the project name, so row order and filtering
// do not change identity. Explicit user choices always win.
func projectBadgeColorName(project protocol.Project) string {
	if project.Color != "" {
		return project.Color
	}
	names := [...]string{"purple", "blue", "green", "orange", "pink", "teal"}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(project.Name)))
	return names[h.Sum32()%uint32(len(names))]
}

// projectBadgeStyle resolves the badge over the surrounding background bg,
// which only shows through in the no-color profile.
func (m *Model) projectBadgeStyle(project protocol.Project, bg string) componentVisual {
	p := m.colors()
	color := projectBadgeColorName(project)
	// A small saturated ground and a related pale ink keep dark-mode badges
	// distinct without using the same bright solid fill as a selected control.
	dark := map[string][2]string{
		"purple": {"#f3c7ff", "#49305f"}, "blue": {"#c5e4ff", "#24466b"},
		"green": {"#d2f1c5", "#305433"}, "orange": {"#ffe0b5", "#674322"},
		"pink": {"#ffd0e6", "#65334f"}, "teal": {"#bff4ee", "#245552"},
	}
	light := map[string][2]string{
		"purple": {"#653277", "#eadbf4"}, "blue": {"#244f79", "#d5e7f8"},
		"green": {"#305e35", "#dcedd5"}, "orange": {"#75491f", "#f8e3c9"},
		"pink": {"#7b355c", "#f5dce9"}, "teal": {"#205c57", "#d3eeea"},
	}
	v := componentVisual{foreground: p.text, background: bg, bold: true}
	if m.colorProfile == colorprofile.TrueColor {
		pair := dark[color]
		if m.state.Light {
			pair = light[color]
		}
		if pair[0] != "" {
			v.foreground, v.background = pair[0], pair[1]
		}
	} else if m.colorProfile >= colorprofile.ANSI {
		// Neutral ink over a theme accent remains readable with terminal-defined
		// ANSI palettes; no-color keeps the monogram and bold identity intact.
		accents := map[string]string{"purple": p.violet, "blue": p.blue, "green": p.green, "orange": p.gold, "pink": p.pink, "teal": p.cyan}
		if accent := accents[color]; accent != "" {
			v.foreground, v.background = bg, accent
		}
	}
	return v
}

// The badge is the project's identity everywhere it appears: the chosen icon,
// else the colored first-and-last-letter monogram, in two cells.
func (m *Model) renderProjectBadge(f *frame, x, y int, project protocol.Project, bg string) {
	label := projectMonogram(project.Name)
	if project.Icon != "" {
		label = centered(m.icon(project.Icon), 2)
	}
	f.componentText(x, y, 2, label, m.projectBadgeStyle(project, bg))
}
