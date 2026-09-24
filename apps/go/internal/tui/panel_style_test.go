package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func frameText(f frame) string { return ansi.Strip(strings.Join(f.rows, "\n")) }

func hitByKey(f frame, key string) (hit, bool) {
	for _, h := range f.hits {
		if h.Key == key {
			return h, true
		}
	}
	return hit{}, false
}

// emptyHostModel shows the right host with no surfaces, so it paints the
// chooser.
func emptyHostModel(w, h int) *Model {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.activate(action{Kind: "right"})
	m.configureInputs()
	return m
}

func TestChooserTilesHitAndActivate(t *testing.T) {
	m := emptyHostModel(160, 50)
	f := m.render()
	kinds := []string{"files", "git", "terminal", "agents", "plan", "activity"}
	var rects []shell.Rect
	for _, kind := range kinds {
		h, ok := hitByKey(f, "chooser:"+kind)
		if !ok {
			t.Fatalf("chooser tile %s missing: %s", kind, frameText(f))
		}
		if h.Rect.H != 3 || h.Action.Kind != "open" || h.Action.Value != kind {
			t.Fatalf("tile %s hit = %+v, want a three-row banded open target", kind, h)
		}
		rects = append(rects, h.Rect)
	}
	// A grid: several tiles share a row and none overlap.
	if rects[0].Y != rects[1].Y || rects[0].X >= rects[1].X {
		t.Fatalf("tiles are not laid out in columns: %+v", rects)
	}
	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			a, b := rects[i], rects[j]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Fatalf("tiles %d and %d overlap: %+v %+v", i, j, a, b)
			}
		}
	}
	if !strings.Contains(frameText(f), "ADD SURFACE") {
		t.Fatal("chooser heading missing")
	}
	// Tab reaches the tiles; focus marks the gap cell before the label row.
	for range 200 {
		if m.focus == "chooser:plan" {
			break
		}
		m.key(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if m.focus != "chooser:plan" {
		t.Fatal("Tab never reached the Plan tile")
	}
	f = m.render()
	plan, _ := hitByKey(f, "chooser:plan")
	if cell := ansi.Strip(cutCells(f.rows[plan.Rect.Y+1], plan.Rect.X-1, plan.Rect.X)); cell != m.icon("focus") {
		t.Fatalf("focus mark cell %q", cell)
	}
	// Clicking the lower band row of a tile opens that surface.
	git, _ := hitByKey(f, "chooser:git")
	m.clickAt(git.Rect.X+1, git.Rect.Y+2)
	if active, ok := m.viewState().Host.Active(); !ok || active.Kind != "git" {
		t.Fatalf("tile click did not open git: %+v", m.viewState().Host)
	}
}

// clickAt dispatches a left click and release at a cell.
func (m *Model) clickAt(x, y int) {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestChooserTilesFallBackToList(t *testing.T) {
	for name, setup := range map[string]func(*Model){
		"plain icons": func(m *Model) { m.plainIcons = true },
		"16 colors":   func(m *Model) { m.colorProfile = colorprofile.ANSI },
	} {
		m := emptyHostModel(160, 50)
		setup(m)
		f := m.render()
		h, ok := hitByKey(f, "chooser:files")
		if !ok || h.Rect.H != 1 {
			t.Fatalf("%s: chooser did not fall back to one-row list items: %+v", name, h)
		}
	}
	// A narrow host whose tiles cannot hold every label also keeps the list.
	m := emptyHostModel(160, 50)
	if cols := m.chooserTileColumns(shell.Rect{H: 40}, 20, []string{"x  Activity", "x  Terminal"}); cols != 0 {
		t.Fatalf("labels that do not fit chose %d columns", cols)
	}
	if cols := m.chooserTileColumns(shell.Rect{H: 40}, 70, []string{"x  Files", "x  Terminal", "x  Activity"}); cols != 3 {
		t.Fatalf("wide host chose %d columns, want 3", cols)
	}
	if cols := m.chooserTileColumns(shell.Rect{H: 40}, 40, []string{"x  Files", "x  Terminal", "x  Activity"}); cols != 2 {
		t.Fatalf("medium host chose %d columns, want 2", cols)
	}
	if cols := m.chooserTileColumns(shell.Rect{H: 8}, 70, []string{"a", "b", "c", "d", "e", "f"}); cols != 0 {
		t.Fatalf("short host chose %d columns", cols)
	}
}

func TestMenuHeadingRulesAndHitGeometry(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.openCommands()
	f := m.render()
	r := m.menuRect()
	if want := min(len(m.menu)+menuChromeRows, m.height-4); r.H != want {
		t.Fatalf("menu height %d, want %d", r.H, want)
	}
	rows := strings.Split(frameText(f), "\n")
	if !strings.Contains(rows[r.Y+1], "COMMANDS") {
		t.Fatalf("title row %q", rows[r.Y+1])
	}
	for _, y := range []int{r.Y + 2, r.Y + r.H - 3} {
		if !strings.Contains(rows[y], strings.Repeat("─", r.W-4)) {
			t.Fatalf("row %d is not a rule: %q", y, rows[y])
		}
	}
	visible := m.menuVisibleItems()
	for i := 0; i < visible; i++ {
		h, ok := hitByKey(f, fmt.Sprintf("menu:%d", i))
		if !ok || h.Rect.Y != r.Y+3+i || !r.Contains(h.Rect.X, h.Rect.Y) || !r.Contains(h.Rect.X+h.Rect.W-1, h.Rect.Y) {
			t.Fatalf("item %d hit %+v outside rows of %+v", i, h, r)
		}
	}
	if _, ok := hitByKey(f, fmt.Sprintf("menu:%d", visible)); ok {
		t.Fatal("an item beyond the visible rows has a hit")
	}
	// Selecting past the visible rows scrolls and keeps hits inside the list.
	m.menuIndex = len(m.menu) - 1
	f = m.render()
	last, ok := hitByKey(f, fmt.Sprintf("menu:%d", len(m.menu)-1))
	if !ok || last.Rect.Y != r.Y+3+visible-1 {
		t.Fatalf("last item hit %+v after scrolling", last)
	}
	if !strings.Contains(strings.Split(frameText(f), "\n")[r.Y+r.H-2], fmt.Sprintf("%d/%d", len(m.menu), len(m.menu))) {
		t.Fatal("menu counter missing on hint row")
	}
}

func TestUsageMenuRendersSelectablePairs(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 4250, Size: 10000, Source: "session/update"}
	m.openUsageSummary()
	f := m.render()
	rows := strings.Split(frameText(f), "\n")
	h, ok := hitByKey(f, "menu:0")
	if !ok {
		t.Fatal("usage item hit missing")
	}
	row := rows[h.Rect.Y]
	label, value := strings.Index(row, "Context used"), strings.Index(row, "4250 tokens")
	if label < 0 || value < 0 || strings.Contains(row, "Context used:") || value-label < 20 {
		t.Fatalf("usage row is not a pair: %q", row)
	}
	if h.Action.Kind != "menu-select" || h.Label != "Context used: 4250 tokens" {
		t.Fatalf("pair row lost its selectable action: %+v", h)
	}
	// A value too long for the row keeps the one-line label.
	if _, _, ok := menuPair(pairMenuItem("Source", strings.Repeat("x", 80), action{}), 60); ok {
		t.Fatal("an overflowing value became a truncated pair")
	}
}

// Free-form item text is never split into a pair, even when it reads
// "Label: value": thread titles, options and folder names keep their text.
func TestMenuNeverInfersPairsFromUserText(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, label := range []string{"Bug: crash on resize", "Thread · a: b"} {
		if _, _, ok := menuPair(menuItem{Label: label}, 60); ok {
			t.Fatalf("%q became a pair", label)
		}
	}
	m.showMenu("Closed threads", []menuItem{{Label: "Bug: crash on resize", Action: action{Kind: "thread", ID: "t"}}})
	f := m.render()
	h, ok := hitByKey(f, "menu:0")
	if !ok {
		t.Fatal("menu item hit missing")
	}
	if row := strings.Split(frameText(f), "\n")[h.Rect.Y]; !strings.Contains(row, "Bug: crash on resize") {
		t.Fatalf("thread title was split: %q", row)
	}
}

func TestSurfaceStructuredRowsAndScrollBounds(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.openSurface("activity", "")
	m.viewState().DetailID = "mcp-fixture"
	active, _ := m.viewState().Host.Active()
	rows := m.surfaceRows(m.surfaceBlocks(active), 60)
	pairs := map[string]string{}
	for _, r := range rows {
		if r.kind == surfacePairRow {
			pairs[r.text] = r.value
		}
	}
	if rows[0].kind != surfaceHeadingRow || rows[0].text != "Activity" {
		t.Fatalf("first row %+v, want the Activity heading", rows[0])
	}
	if pairs["Server"] != "design" || pairs["Tool"] != "inspect_surface" {
		t.Fatalf("short detail facts are not pairs: %+v", pairs)
	}
	// Result is too long for a pair: muted label above the wrapped value.
	for i, r := range rows {
		if r.kind == surfaceTextRow && r.text == "Result" {
			if r.ink != m.colors().muted || !strings.Contains(rows[i+1].text, "existing tab") {
				t.Fatalf("long value rows %+v %+v", r, rows[i+1])
			}
		}
	}
	// Plan: one status row per step, semantic ink and the state at the right.
	m.snapshot.Threads[0].Plan = []protocol.PlanStep{{Title: "Read", State: "completed"}, {Title: "Edit", State: "active"}, {Title: "Verify", State: "pending"}, {Title: "Ship", State: "failed"}, {Title: "Odd", State: "mystery"}}
	m.openSurface("plan", "")
	active, _ = m.viewState().Host.Active()
	p := m.colors()
	want := map[string]string{"Read": p.green, "Edit": p.blue, "Verify": p.muted, "Ship": p.red, "Odd": p.muted}
	seen := 0
	for _, r := range m.surfaceRows(m.surfaceBlocks(active), 60) {
		if r.kind == surfaceStatusRow {
			seen++
			if r.ink != want[r.text] || r.value == "" {
				t.Fatalf("step %q ink %s value %q", r.text, r.ink, r.value)
			}
			if r.text != "Read" && r.glyph == "✓" {
				t.Fatalf("%q implies success", r.text)
			}
		}
	}
	if seen != 5 {
		t.Fatalf("plan rendered %d status rows", seen)
	}
	// Scrolling is bounded by the painted rows.
	m.openSurface("activity", "")
	m.viewState().DetailID = ""
	f := m.render()
	active, _ = m.viewState().Host.Active()
	total := len(m.surfaceRows(m.surfaceBlocks(active), f.detail.W-1))
	if f.detailMax != max(0, total-f.detail.H) {
		t.Fatalf("detailMax %d, rows %d, height %d", f.detailMax, total, f.detail.H)
	}
	m.viewState().DetailScroll = 1 << 20
	f = m.render()
	if _, ok := hitByKey(f, "right-body"); !ok {
		t.Fatal("surface body hit missing")
	}
}

func TestClosedHeadingCountOnlyWhileCollapsed(t *testing.T) {
	m := navigationModel()
	m.state.RecentsCollapsed = true
	f := m.render()
	h, _ := hitByKey(f, "recents")
	row := strings.Split(frameText(f), "\n")[h.Rect.Y]
	if !strings.Contains(row, "CLOSED (") {
		t.Fatalf("collapsed heading %q", row)
	}
	m.state.RecentsCollapsed = false
	f = m.render()
	h, _ = hitByKey(f, "recents")
	row = strings.Split(frameText(f), "\n")[h.Rect.Y]
	if !strings.Contains(row, "CLOSED") || strings.Contains(row, "(") || h.Action.Kind != "recents-collapse" {
		t.Fatalf("expanded heading %q hit %+v", row, h)
	}
}

func TestPanelStyleNoANSIGrowth(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.openSurface("plan", "")
	m.openUsageSummary()
	first := strings.Join(m.render().rows, "\n")
	for range 20 {
		m.render()
	}
	if again := strings.Join(m.render().rows, "\n"); len(again) != len(first) {
		t.Fatalf("repeated painting grew the frame from %d to %d bytes", len(first), len(again))
	}
}

// TestPanelStyleCaptures writes review captures for the panel constructs
// outside settings.
func TestPanelStyleCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, scenario := range []string{"chooser", "activity-inspector", "usage-details", "agents", "terminal", "commands-menu", "usage-menu", "closed-collapsed"} {
			m := testModel()
			if scenario == "closed-collapsed" {
				m = navigationModel()
				m.state.RecentsCollapsed = true
			}
			m.state.Light = light
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			switch scenario {
			case "chooser":
				m = emptyHostModel(160, 50)
				m.state.Light = light
				m.hover = "chooser:git"
			case "activity-inspector":
				m.openSurface("activity", "")
				m.viewState().DetailID = "mcp-fixture"
			case "usage-details":
				m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 4250, Size: 10000, Source: "session/update", ReportedAt: "2026-09-22T14:10:00Z"}
				m.openSurface("activity", "")
				m.viewState().DetailID = "usage"
			case "agents":
				finishActivity(m)
				m.openSurface("agents", "")
			case "terminal":
				m.openSurface("terminal", "")
				active, _ := m.viewState().Host.Active()
				m.snapshot.Terminals = append(m.snapshot.Terminals, protocol.Terminal{ID: active.ID, ThreadID: m.state.Active, State: "running", Controller: "tui-go · this client",
					Output: "$ go test ./internal/tui\nok  \tgithub.com/muschterm/tui/apps/go/internal/tui\t15.2s\n$ git status --short\n M internal/tui/render.go\n?? internal/tui/surface_panel.go\n$ "})
			case "commands-menu":
				m.openCommands()
			case "usage-menu":
				m.snapshot.Threads[0].Usage = &protocol.Usage{Used: 4250, Size: 10000, Source: "session/update", ReportedAt: "2026-09-22T14:10:00Z"}
				m.openUsageSummary()
			}
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-light%t-panel-%s.ansi", m.width, m.height, light, scenario)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPanelStatusMarkNeverImpliesSuccessOrUncertaintyWrongly(t *testing.T) {
	m := testModel()
	p := m.colors()
	for state, want := range map[string]string{
		"completed": "✓", "active": "●", "pending": "○", "failed": "✕", "interrupted": "!",
		"unknown": "?", "unavailable": "?", "accepted": "·", "idle": "·", "ready": "·",
	} {
		glyph, ink := panelStatusMark(m, state)
		if glyph != want {
			t.Errorf("%s: glyph %q, want %q", state, glyph, want)
		}
		if want != "✓" && ink == p.green {
			t.Errorf("%s: uses success ink", state)
		}
	}
}

// Dialog titles uppercase only their static prefix; user names keep case.
func TestMenuTitleKeepsUserTextCase(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, tc := range []struct{ prefix, user, want string }{
		{"Delete thread: ", "Fix Bug: MixedCase", "DELETE THREAD: Fix Bug: MixedCase"},
		{"Remove project: ", "myProject", "REMOVE PROJECT: myProject"},
	} {
		m.showMenuFor(tc.prefix, tc.user, []menuItem{{Label: "Cancel", Action: action{Kind: "noop"}}})
		if got := m.menuTitleText(); got != tc.want {
			t.Fatalf("title = %q, want %q", got, tc.want)
		}
		if !strings.Contains(frameText(m.render()), tc.want) {
			t.Fatalf("painted title missing %q", tc.want)
		}
	}
	m.showMenu("Usage", nil)
	if got := m.menuTitleText(); got != "USAGE" {
		t.Fatalf("static title = %q", got)
	}
}

// A status row too narrow for its state keeps the full wrapped title width
// and moves the state to a muted row below instead of dropping it.
func TestNarrowStatusRowKeepsTitleAndState(t *testing.T) {
	m := testModel()
	for _, width := range []int{20, 30} {
		b := statusBlock(m, "A long reported title for the narrow row", "idle", true)
		// Force the narrow branch: the state cannot sit beside the title.
		b.value = strings.Repeat("s", width-6)
		rows := m.surfaceRows([]surfaceBlock{b}, width)
		last := rows[len(rows)-1]
		if last.text != b.value || last.ink != m.colors().muted {
			t.Fatalf("width %d: state not kept below: %+v", width, rows)
		}
		f := &frame{rows: make([]string, 1)}
		f.rows[0] = strings.Repeat(" ", width)
		m.paintSurfaceRow(f, 0, 0, width, rows[0])
		painted := strings.TrimRight(ansi.Strip(f.rows[0]), " ")
		if want := b.glyph + " " + rows[0].text; painted != want {
			t.Fatalf("width %d: title cut: %q want %q", width, painted, want)
		}
	}
}

// Detail blank lines survive, collapsed to one.
func TestDetailBlocksKeepBlankLines(t *testing.T) {
	m := testModel()
	got := detailBlocks(m, "\nline one\n\n\n\nline two\n\n")
	if len(got) != 3 || got[1].value != "" || got[0].value != "line one" || got[2].value != "line two" {
		t.Fatalf("blocks = %+v", got)
	}
}

// An untitled activity is named by its role, never a bare glyph.
func TestUntitledActivityGetsRoleTitle(t *testing.T) {
	m := testModel()
	b := activityBlocks(m, protocol.Activity{Role: "user", State: "completed", Text: "Do it"})
	if b[0].label != "Prompt" {
		t.Fatalf("title = %q", b[0].label)
	}
}

// Long pair values stack under their label rather than right-align.
func TestLongPairValueStacks(t *testing.T) {
	m := testModel()
	rows := m.surfaceRows([]surfaceBlock{
		{kind: surfacePairBlock, label: "Inputs", value: "docs/design/layout.md, activity.md, questions.md"},
		{kind: surfacePairBlock, label: "State", value: "idle"},
	}, 90)
	if len(rows) != 3 || rows[0].kind != surfaceTextRow || rows[0].text != "Inputs" || rows[2].kind != surfacePairRow {
		t.Fatalf("rows = %+v", rows)
	}
}
