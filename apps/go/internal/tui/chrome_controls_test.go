package tui

import (
	tea "charm.land/bubbletea/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func chromeFrame(m *Model, paint bool) frame {
	f := frame{}
	if paint {
		f.rows = []string{strings.Repeat(" ", m.width)}
	}
	m.renderChrome(&f, m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()))
	return f
}

func TestChromeAttentionBadgeAndColors(t *testing.T) {
	for _, light := range []bool{false, true} {
		m := testModel()
		m.state.Light = light
		for i := range m.snapshot.Threads {
			m.snapshot.Threads[i].Requests = nil
			m.snapshot.Threads[i].State = "idle"
		}
		f := chromeFrame(m, true)
		p := colors(light)
		if strings.Contains(ansi.Strip(f.rows[0]), "Attention") || strings.Contains(ansi.Strip(f.rows[0]), " 0 ") {
			t.Fatal("attention has redundant text/zero badge")
		}
		h := controlHit(t, f, "attention")
		if got := ansi.Cut(f.rows[0], h.Rect.X, h.Rect.X+1); !strings.Contains(got, style(p.muted, p.canvas).Render(m.icon("attention"))) {
			t.Fatalf("neutral bell color: %q", got)
		}
		m.snapshot.Threads[0].State = "failed"
		f = chromeFrame(m, true)
		h = controlHit(t, f, "attention")
		if !strings.Contains(f.rows[0], style(p.nav, p.gold).Render(" 1 ")) {
			t.Fatal("pending badge lacks distinct color")
		}
		if got := ansi.Cut(f.rows[0], h.Rect.X, h.Rect.X+1); !strings.Contains(got, style(p.gold, p.canvas).Render(m.icon("attention"))) {
			t.Fatalf("pending bell color: %q", got)
		}
	}
}

func TestChromeVisibleControlsPackRightAndMatchMeasurement(t *testing.T) {
	for _, width := range []int{40, 47, 48, 60, 80, 120} {
		for _, right := range []bool{false, true} {
			m := testModel()
			m.width = width
			m.state.Layout.Right = right
			f := chromeFrame(m, true)
			if !reflect.DeepEqual(f.hits, chromeFrame(m, false).hits) {
				t.Fatal("measure and paint disagree")
			}
			att := controlHit(t, f, "attention")
			if m.singleColumn() {
				if !hasControl(f, "columns") || hasControl(f, "maximize") || att.slot().X+att.slot().W != width-1 {
					t.Fatal("compact chrome lost its picker or right-aligned attention")
				}
			} else {
				first := controlHit(t, f, "bottom")
				if hasControl(f, "maximize") {
					first = controlHit(t, f, "maximize")
				}
				g := m.state.Layout.Compute(m.width, m.height-1, m.footerHeight())
				if g.Right.W > 0 && !g.Maximized {
					// The bell stays at the center pane's right edge; the pane
					// controls belong to the right host's span.
					if att.slot().X+att.slot().W+1 != g.Right.X || first.slot().X < g.Right.X {
						t.Fatalf("width %d: bell/controls not split at the right host: bell=%+v first=%+v right=%+v", width, att.slot(), first.slot(), g.Right)
					}
				} else if att.slot().X+att.slot().W+2 != first.slot().X {
					// Two slot cells make the bell-to-control pitch match the
					// six-cell pitch between the pane controls themselves.
					t.Fatal("unused maximize slot left a gap")
				}
				if g.Left.W > 0 {
					title := controlHit(t, f, "app-title")
					if title.Rect.X != 6 || !strings.HasPrefix(title.Label, "New thread") {
						t.Fatalf("app title not left of the breadcrumb: %+v", title)
					}
				} else if hasControl(f, "app-title") {
					t.Fatal("app title shown without navigation")
				}
				end := controlHit(t, f, "right")
				if end.slot().X+end.slot().W != width {
					t.Fatal("pane controls not right aligned")
				}
			}
			for _, h := range f.hits {
				if h.Rect.X < 0 || h.Rect.X+h.Rect.W > width {
					t.Fatalf("out of bounds: %+v", h)
				}
			}
		}
	}
}

func TestChromeTitleAndBreadcrumbStartThreads(t *testing.T) {
	m := testModel()
	m.width, m.height = 120, 40
	m.state.Layout.Right = true
	for i := range m.snapshot.Threads {
		if m.snapshot.Threads[i].ID == m.state.Active {
			m.snapshot.Threads[i].Title = "Short title"
		}
	}
	f := m.render()
	g := m.state.Layout.Compute(m.width, m.height-1, m.footerHeight())
	crumb := controlHit(t, f, "breadcrumb-project")
	th := m.thread()
	if crumb.Action != (action{Kind: "thread-create", Value: th.ProjectID}) || crumb.Label != "New thread in "+th.Project {
		t.Fatalf("project crumb does not start a thread in its project: %+v", crumb)
	}
	row := ansi.Strip(f.rows[0])
	project, _ := m.projectByID(th.ProjectID)
	if !strings.Contains(row, projectMonogram(project.Name)+" "+th.Project+"  /  "+th.Title) || strings.Contains(row, m.icon("folder")) {
		t.Fatalf("breadcrumb missing the project badge, project, separator and title: %q", row)
	}
	badge := ansi.Cut(f.rows[0], crumb.Rect.X, crumb.Rect.X+2)
	if badge != ansi.Cut(m.render().rows[0], crumb.Rect.X, crumb.Rect.X+2) || !strings.Contains(badge, "\x1b[1;") {
		t.Fatalf("breadcrumb badge is not the bold project badge: %q", badge)
	}
	// Left-justified on the center pane, inset one cell.
	if want := g.Center.X + 1; crumb.Rect.X != want {
		t.Fatalf("breadcrumb x=%d want %d (center %+v)", crumb.Rect.X, want, g.Center)
	}
	m.state.Layout.Left = false
	f = m.render()
	if hasControl(f, "app-title") {
		t.Fatal("title shown while navigation is hidden")
	}
	if crumb = controlHit(t, f, "breadcrumb-project"); crumb.Rect.X != 6 {
		t.Fatalf("breadcrumb should follow the toggle when navigation is hidden: x=%d", crumb.Rect.X)
	}
	m.state.Layout.Left = true
	f = m.render()
	title := controlHit(t, f, "app-title")
	if title.Action != (action{Kind: "thread-create"}) || !strings.Contains(title.Label, "choose a project") {
		t.Fatalf("title without a filter should open the project chooser: %+v", title)
	}
	m.state.ProjectFilter = th.ProjectID
	title = controlHit(t, m.render(), "app-title")
	if title.Action != (action{Kind: "thread-create", Value: th.ProjectID}) || title.Label != "New thread in "+th.Project {
		t.Fatalf("title with a filter should start a thread there: %+v", title)
	}
	m.hover = "breadcrumb-project"
	f = m.render()
	cells := ansi.Cut(f.rows[0], crumb.Rect.X, crumb.Rect.X+crumb.Rect.W)
	if !strings.Contains(cells, "\x1b[1;") || strings.Contains(cells, "48;2;53;60;77") {
		t.Fatalf("project crumb hover is not bold without a fill: %q", cells)
	}
	clickControl(m, crumb)
	if !m.creatingThread() || m.state.DraftProjectID != th.ProjectID {
		t.Fatal("clicking the project crumb did not open a draft in that project")
	}
}

func TestDraftThreadKeepsPaneToggles(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	clickControl(m, controlHit(t, m.render(), "thread-create"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.creatingThread() {
		t.Fatal("draft not opened")
	}
	clickControl(m, controlHit(t, m.render(), "right"))
	if g := m.workspaceGeometry(m.footerHeight()); g.Right.W == 0 || !hasControl(m.render(), "chooser:files") {
		t.Fatalf("right host hidden on a draft thread: %+v", g.Right)
	}
	clickControl(m, controlHit(t, m.render(), "bottom"))
	if g := m.workspaceGeometry(m.footerHeight()); g.Bottom.H == 0 {
		t.Fatalf("bottom panel hidden on a draft thread: %+v", g.Bottom)
	}
	m.state.Active, m.state.DraftProjectID = "", ""
	if g := m.workspaceGeometry(m.footerHeight()); g.Right.W != 0 || g.Bottom.H != 0 {
		t.Fatal("empty state should hide the panes")
	}
}

func TestDividersContinueThroughTopBar(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.openSurface("files", "")
	f := m.render()
	g := f.geom
	for _, d := range []shell.Rect{g.LeftDivider, g.RightDivider} {
		if d.W == 0 {
			t.Fatal("expected both dividers")
		}
		if got := ansi.Strip(ansi.Cut(f.rows[0], d.X, d.X+1)); got != "│" {
			t.Fatalf("divider column %d not drawn through the top bar: %q", d.X, got)
		}
		if g.DividerAt(d.X, 0) == shell.NoDivider {
			t.Fatalf("top bar cell above divider %d is not draggable", d.X)
		}
		for _, h := range f.hits {
			if h.Rect.Y == 0 && h.Rect.Contains(d.X, 0) {
				t.Fatalf("control %q covers the divider column", h.Key)
			}
		}
	}
}
