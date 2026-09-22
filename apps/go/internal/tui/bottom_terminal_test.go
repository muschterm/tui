package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/fixture"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// accept delivers the pending command's receipt as the server would.
func accept(t *testing.T, m *Model, target string, local action) protocol.Command {
	t.Helper()
	if m.busy == nil {
		t.Fatal("no command pending")
	}
	c := *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{TargetID: target}, local: local})
	return c
}

func TestBottomPanelOpensTerminalTabsWithoutHeader(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.activate(action{Kind: "bottom"})
	if !m.state.Layout.Bottom || m.busy == nil || m.busy.Kind != "terminal.open" {
		t.Fatalf("showing the bottom panel did not request a terminal: busy=%+v", m.busy)
	}
	accept(t, m, "term-bottom-1", action{Kind: "terminal-open", Value: "bottom"})
	v := m.viewState()
	if len(v.Bottom.Tabs) != 1 || v.Bottom.Tabs[0].ID != "term-bottom-1" || v.Bottom.Tabs[0].Title != "Terminal 1" || v.Bottom.ActiveID != "term-bottom-1" {
		t.Fatalf("bottom tab not opened from the receipt: %+v", v.Bottom)
	}
	f := m.render()
	tab, add := controlHit(t, f, "bottom-tab:term-bottom-1"), controlHit(t, f, "bottom-new")
	row := ansi.Strip(f.rows[tab.Rect.Y])
	if add.Rect.Y != tab.Rect.Y || add.Rect.X <= tab.Rect.X+tab.Rect.W || !strings.Contains(row, "Terminal 1") {
		t.Fatalf("tab row lacks the tab and trailing add control: %q", row)
	}
	if strings.Contains(strings.Replace(row, "Terminal 1", "", 1), "Terminal") || hasControl(f, "bottom-close") {
		t.Fatalf("bottom panel still shows the old header or close button: %q", row)
	}
	// The fixture snapshot has no record of the accepted session yet, so the
	// body reports it honestly instead of inventing output.
	if f.bottomBody.H == 0 || f.bottomBody.Y != tab.Rect.Y+2 || !strings.Contains(ansi.Strip(f.rows[f.bottomBody.Y]), "Terminal session unavailable") {
		t.Fatal("terminal output body missing below the tab row")
	}

	// The add control opens another independently named session and selects it.
	m.activate(action{Kind: "bottom-new"})
	accept(t, m, "term-bottom-2", action{Kind: "terminal-open", Value: "bottom"})
	if len(v.Bottom.Tabs) != 2 || v.Bottom.Tabs[1].Title != "Terminal 2" || v.Bottom.ActiveID != "term-bottom-2" {
		t.Fatalf("second terminal not added: %+v", v.Bottom)
	}
	m.activate(action{Kind: "bottom-tab", ID: "term-bottom-1"})
	if v.Bottom.ActiveID != "term-bottom-1" {
		t.Fatal("selecting a bottom tab did not change the active session")
	}

	// Closing through a tab's icon slot ends only that session.
	f = m.render()
	clickControl(m, controlHit(t, f, "bottom-close:term-bottom-2"))
	if m.busy == nil || m.busy.Kind != "terminal.close" || m.busy.TargetID != "term-bottom-2" {
		t.Fatalf("tab close did not end its own session: %+v", m.busy)
	}
	accept(t, m, "term-bottom-2", action{Kind: "bottom-close", ID: "term-bottom-2"})
	if len(v.Bottom.Tabs) != 1 || v.Bottom.ActiveID != "term-bottom-1" || !m.state.Layout.Bottom {
		t.Fatalf("closing one tab disturbed the other or hid the panel: %+v", v.Bottom)
	}

	// The last close hides the panel; showing it again opens a fresh session.
	clickControl(m, controlHit(t, m.render(), "bottom-close:term-bottom-1"))
	accept(t, m, "term-bottom-1", action{Kind: "bottom-close", ID: "term-bottom-1"})
	if len(v.Bottom.Tabs) != 0 || m.state.Layout.Bottom {
		t.Fatalf("last close left the panel open: %+v", v.Bottom)
	}
	m.activate(action{Kind: "bottom"})
	if m.busy == nil || m.busy.Kind != "terminal.open" {
		t.Fatal("reshowing the panel did not open a fresh session")
	}
	accept(t, m, "term-bottom-3", action{Kind: "terminal-open", Value: "bottom"})
	if len(v.Bottom.Tabs) != 1 || v.Bottom.Tabs[0].Title != "Terminal 1" {
		t.Fatalf("fresh session not presented: %+v", v.Bottom)
	}
	// Hiding the panel preserves the session; showing it again opens nothing.
	m.activate(action{Kind: "bottom"})
	m.activate(action{Kind: "bottom"})
	if m.busy != nil || len(v.Bottom.Tabs) != 1 || !m.state.Layout.Bottom {
		t.Fatal("toggling a populated panel changed its sessions")
	}
}

// Compact column selection is presentation only: the empty Terminal column
// shows its tab row and add control without opening a session.
func TestCompactTerminalColumnDoesNotOpenSessions(t *testing.T) {
	m := testModel()
	m.width, m.height = 47, 22
	m.activate(action{Kind: "bottom"})
	v := m.viewState()
	if m.busy != nil || len(v.Bottom.Tabs) != 0 || v.CompactColumn != shell.BottomRegion {
		t.Fatalf("selecting the Terminal column opened a session: busy=%+v", m.busy)
	}
	f := m.render()
	if !hasControl(f, "bottom-new") || !strings.Contains(strings.Join(f.rows, "\n"), "No terminal sessions") {
		t.Fatal("empty Terminal column lacks the add control or empty state")
	}
	clickControl(m, controlHit(t, f, "bottom-new"))
	if m.busy == nil || m.busy.Kind != "terminal.open" {
		t.Fatal("add control did not open a session")
	}
	accept(t, m, "term-compact-1", action{Kind: "terminal-open", Value: "bottom"})
	if len(v.Bottom.Tabs) != 1 || !hasControl(m.render(), "bottom-tab:term-compact-1") {
		t.Fatal("compact column did not present the new tab")
	}
}

func TestBottomTabOverflowAndOtherThreadReceipts(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 45
	m.state.Layout.Bottom = true
	v := m.viewState()
	for i := 1; i <= 6; i++ {
		openTerminalTab(&v.Bottom, "term-"+string(rune('0'+i)))
	}
	if v.Bottom.Tabs[5].Title != "Terminal 6" {
		t.Fatalf("numbering did not continue: %+v", v.Bottom.Tabs)
	}
	m.state.Layout.BottomHeight = 8
	f := m.render()
	// Narrow the panel by hiding nothing: every tab fits at this width, so no overflow control.
	if hasControl(f, "bottom-tabs") {
		t.Fatal("overflow shown while every tab fits")
	}
	m.width = 60
	m.state.Layout.Left, m.state.Layout.Right = false, false
	f = m.render()
	if !hasControl(f, "bottom-tabs") || !hasControl(f, "bottom-tab:"+v.Bottom.ActiveID) {
		t.Fatal("overflow control missing or active tab hidden at narrow width")
	}
	m.activate(action{Kind: "bottom-tabs"})
	if len(m.menu) != 6 || m.menu[0].Action.Kind != "bottom-tab" {
		t.Fatalf("overflow menu lists %d items of kind %q", len(m.menu), m.menu[0].Action.Kind)
	}

	// A receipt for another thread updates that thread's tabs, not the layout.
	other := "thread-review"
	c := protocol.Command{ID: "terminal-9", Kind: "terminal.open", ThreadID: other}
	m.state.Layout.Bottom = false
	m.busy = &c
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{TargetID: "term-other"}, local: action{Kind: "terminal-open", Value: "bottom"}})
	if len(m.state.Threads[other].Bottom.Tabs) != 1 || m.state.Layout.Bottom {
		t.Fatal("other thread's receipt changed the active layout or missed its own view")
	}
}

func TestLegacyBottomSessionBecomesTab(t *testing.T) {
	snapshot := fixture.Initial()
	saved := savedView{Layout: shell.NewState(), Active: snapshot.Threads[0].ID, Threads: map[string]*threadView{snapshot.Threads[0].ID: {BottomID: "old-session"}}}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, "test", snapshot, data)
	v := m.viewState()
	if len(v.Bottom.Tabs) != 1 || v.Bottom.Tabs[0].ID != "old-session" || v.Bottom.ActiveID != "old-session" || v.BottomID != "" {
		t.Fatalf("legacy bottom session not migrated: %+v", v.Bottom)
	}
}
