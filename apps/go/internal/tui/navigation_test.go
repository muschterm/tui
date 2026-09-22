package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func navigationModel() *Model {
	m := testModel()
	m.snapshot.Projects = []protocol.Project{{ID: "alpha", Name: "Alpha", Path: "/work/alpha"}, {ID: "beta", Name: "Beta", Path: "/work/beta"}}
	for i := range m.snapshot.Threads {
		t := &m.snapshot.Threads[i]
		t.ProjectID = m.snapshot.Projects[i%2].ID
		t.Project = m.snapshot.Projects[i%2].Name
		t.LifecycleRevision = 3
	}
	m.state.Layout.Left = true
	m.state.RecentsCollapsed = false
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	return m
}

func TestNavigationProjectFilterIsLocalAndPreservesActiveWork(t *testing.T) {
	m := navigationModel()
	before, _ := json.Marshal(m.snapshot)
	active := m.state.Active
	m.prompt.SetValue("unfinished draft")
	m.viewState().Draft = m.prompt.Value()
	m.activate(action{Kind: "project-filter", ID: "beta"})
	after, _ := json.Marshal(m.snapshot)
	if m.state.Active != active || m.prompt.Value() != "unfinished draft" || string(before) != string(after) || m.busy != nil {
		t.Fatal("filter changed active work or server state")
	}
	f := m.render()
	if hasControl(f, "thread:"+active) || !hasControl(f, "thread:"+m.snapshot.Threads[1].ID) {
		t.Fatal("filter did not scope navigation")
	}
	if !strings.Contains(ansi.Strip(f.rows[0]), "Alpha") {
		t.Fatal("active center project context is misleading")
	}
	other := New(nil, "other", m.snapshot, nil)
	if other.state.ProjectFilter != "" {
		t.Fatal("local filter leaked to another client")
	}
	m.activate(action{Kind: "project-filter"})
	if !hasControl(m.measure(), "thread:"+active) {
		t.Fatal("All projects did not restore navigation")
	}
}

func TestNavigationProjectPickerKeyboardMouseAndSearch(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := navigationModel()
		active := m.state.Active
		m.activate(action{Kind: "projects"})
		m.Update(tea.PasteMsg{Content: "/work/beta"})
		if m.projectInput.Value() != "/work/beta" || len(m.menu) != 2 || !strings.Contains(m.menu[0].Label, "Beta · /work/beta") {
			t.Fatal("picker did not search/display path")
		}
		if mouse {
			clickControl(m, controlHit(t, m.measure(), "menu:0"))
		} else {
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.state.ProjectFilter != "beta" || m.state.Active != active || m.projectMode != "" || len(m.menu) != 0 {
			t.Fatal("selection failed or changed active thread")
		}
	}
	m := navigationModel()
	m.activate(action{Kind: "project-add"})
	m.Update(tea.PasteMsg{Content: "~/My project"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.Kind != "project.add" || m.busy.Path != "~/My project" {
		t.Fatal("folder text not routed into explicit add command")
	}
}

func TestNavigationProjectModalCloseRestoresPromptInput(t *testing.T) {
	m := navigationModel()
	m.prompt.SetValue("draft")
	m.activate(action{Kind: "projects"})
	clickControl(m, controlHit(t, m.measure(), "menu-close"))
	if m.projectMode != "" || len(m.menu) != 0 {
		t.Fatal("closing project modal leaves hidden project input active")
	}
	m.setFocus("prompt")
	m.Update(tea.PasteMsg{Content: " continued"})
	if !strings.Contains(m.prompt.Value(), "continued") {
		t.Fatal("closed modal stole prompt paste")
	}
}

func TestNavigationLifecycleRoutesAndDeleteDefaultsToCancel(t *testing.T) {
	m := navigationModel()
	id := m.state.Active
	m.activate(action{Kind: "thread-close", ID: id})
	if m.busy != nil || m.status == "" {
		t.Fatal("active work was closable")
	}
	target := &m.snapshot.Threads[0]
	target.State = "idle"
	target.Queue = nil
	target.Requests = nil
	target.Children = nil
	m.activate(action{Kind: "thread-close", ID: id})
	if m.busy == nil || m.busy.Kind != "thread.close" || m.busy.ThreadID != id || m.busy.Revision != 3 {
		t.Fatal("close lost target/revision")
	}
	m = navigationModel()
	id = m.snapshot.Threads[1].ID
	m.snapshot.Threads[1].Closed = true
	clickControl(m, controlHit(t, m.measure(), "thread:"+id))
	if m.busy != nil || m.state.Active != id || !m.thread().Closed {
		t.Fatal("closed row must select without reopening")
	}
	m = navigationModel()
	id = m.state.Active
	m.activate(action{Kind: "thread-delete", ID: id})
	if m.busy != nil || m.menuIndex != 0 || m.menu[0].Action.Kind != "thread-delete-cancel" {
		t.Fatal("delete skipped confirmation or defaulted destructive")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil || len(m.menu) != 0 {
		t.Fatal("default Enter did not cancel deletion")
	}
	m.activate(action{Kind: "thread-delete", ID: id})
	clickControl(m, controlHit(t, m.measure(), "menu:1"))
	if m.busy == nil || m.busy.Kind != "thread.delete" || m.busy.ThreadID != id || m.busy.Revision != 3 {
		t.Fatal("confirmed delete lost target/revision")
	}
}

func TestNavigationHoverControlsHaveSeparateHitboxes(t *testing.T) {
	for _, closed := range []bool{false, true} {
		m := navigationModel()
		th := &m.snapshot.Threads[0]
		th.Closed = closed
		th.State = "idle"
		th.Queue = nil
		th.Children = nil
		th.Requests = nil
		m.state.RecentsCollapsed = false
		m.state.RecentsHidden = false
		id := th.ID
		f := m.render()
		row := controlHit(t, f, "thread:"+id)
		quick := controlHit(t, f, "thread-quick:"+id)
		trailing := "thread-menu:" + id
		if closed {
			trailing = "thread-reopen:" + id
		}
		more := controlHit(t, f, trailing)
		status := controlHit(t, f, "thread-status:"+id)
		// The full card is a selection fallback; the title's specific hit
		// area stays between the status and the trailing action buttons.
		for _, h := range f.hits {
			if h.Key == row.Key && h.Rect.Y == status.Rect.Y && h.Rect.X == status.slot().X+status.slot().W {
				row = h
			}
		}
		if row.Rect.X+row.Rect.W > quick.slot().X || quick.slot().X+quick.slot().W != more.slot().X {
			t.Fatal("quick/menu actions overlap thread label")
		}
		if closed && strings.TrimSpace(ansi.Strip(ansi.Cut(f.rows[more.Rect.Y], more.slot().X, more.slot().X+3))) != "" {
			t.Fatal("Reopen is visible without hover or focus")
		}
		m.Update(tea.MouseMotionMsg{X: row.Rect.X, Y: row.Rect.Y})
		f = m.render()
		want := m.icon("check")
		kind := "thread-close"
		if closed {
			want = m.icon("trash")
			kind = "thread-delete"
		}
		painted := ansi.Strip(ansi.Cut(f.rows[quick.Rect.Y], quick.slot().X, quick.slot().X+quick.slot().W))
		if painted != " "+want+" " || quick.Action.Kind != kind {
			t.Fatal("hover did not expose lifecycle icon")
		}
		if closed && (more.Action.Kind != "thread-reopen" || ansi.Strip(ansi.Cut(f.rows[more.Rect.Y], more.slot().X, more.slot().X+3)) != " "+m.icon("reopen")+" " || hasControl(f, "thread-menu:"+id)) {
			t.Fatal("closed row did not replace the menu with Reopen")
		}
		if got := ansi.Strip(ansi.Cut(f.rows[status.Rect.Y], status.Rect.X, status.Rect.X+status.Rect.W)); !strings.Contains(got, "●") {
			t.Fatal("hover replaced the leading status circle")
		}
		if !reflect.DeepEqual(f.hits, m.measure().hits) {
			t.Fatal("navigation hitboxes differ when measured")
		}
		clickControl(m, quick)
		if closed {
			if m.busy != nil || len(m.menu) == 0 || m.menu[m.menuIndex].Action.Kind != "thread-delete-cancel" {
				t.Fatal("trash bypassed the existing confirmation")
			}
		} else if m.busy == nil || m.busy.Kind != "thread.close" || m.busy.ThreadID != id {
			t.Fatal("checkmark did not close the intended thread")
		}
	}
}

func TestNavigationComposeIconKeyboardAndMouse(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, mouse := range []bool{false, true} {
			m := navigationModel()
			m.plainIcons = plain
			m.state.ProjectFilter = "alpha"
			m.prompt.SetValue("existing draft")
			f := m.render()
			h := controlHit(t, f, "thread-create")
			text := strings.TrimSpace(ansi.Strip(ansi.Cut(f.rows[h.Rect.Y], h.Rect.X, h.Rect.X+h.Rect.W)))
			if text != m.icon("compose") || !strings.Contains(h.Label, "New thread") {
				t.Fatal("compose control lost icon or descriptive help")
			}
			if mouse {
				clickControl(m, h)
			} else {
				m.setFocus(h.Key)
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if m.projectMode != "new-thread" || m.prompt.Value() != "existing draft" {
				t.Fatal("compose must offer a project destination without changing existing input")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.busy != nil || m.state.DraftProjectID != "alpha" || m.state.Active != "" || m.prompt.Value() != "" || m.state.Threads["thread-shell"].Draft != "existing draft" {
				t.Fatal("compose must open a local draft and preserve existing input")
			}
		}
	}
}

func TestThreadQuickActionSlotStaysBesideMenu(t *testing.T) {
	for _, width := range []int{16, 24, 47} {
		for _, closed := range []bool{false, true} {
			for _, plain := range []bool{false, true} {
				m := navigationModel()
				m.plainIcons = plain
				m.state.Layout.LeftWidth = width
				th := &m.snapshot.Threads[0]
				th.Closed, th.State, th.Title = closed, "idle", strings.Repeat("Long 界 title ", 5)
				th.Queue, th.Children, th.Requests = nil, nil, nil
				if width == 47 {
					m.Update(tea.WindowSizeMsg{Width: 47, Height: 22})
					m.activate(action{Kind: "column", Index: int(shell.LeftRegion)})
				}
				id := th.ID
				f := m.render()
				if !hasControl(f, "thread-quick:"+id) {
					t.Fatalf("missing quick: width=%d closed=%v plain=%v nav=%+v closedNav=%+v footer=%d", width, closed, plain, f.navigation, f.closedNavigation, m.footerHeight())
				}
				trailing := "thread-menu:" + id
				if closed {
					trailing = "thread-reopen:" + id
				}
				quick, more := controlHit(t, f, "thread-quick:"+id), controlHit(t, f, trailing)
				if quick.slot().W != 3 || quick.slot().X+quick.slot().W != more.slot().X {
					t.Fatal("quick action is not directly left of menu", width)
				}
				prefix := ansi.Strip(ansi.Cut(f.rows[quick.Rect.Y], 0, quick.slot().X))
				m.setFocus(quick.Key)
				f = m.render()
				if controlHit(t, f, quick.Key).Rect != quick.Rect || ansi.Strip(ansi.Cut(f.rows[quick.Rect.Y], 0, quick.slot().X)) != prefix {
					t.Fatal("focus shifted the title or action slot")
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if closed && (m.busy != nil || len(m.menu) == 0) || !closed && (m.busy == nil || m.busy.Kind != "thread.close") {
					t.Fatal("keyboard activation lost the lifecycle action")
				}
			}
		}
	}
}

func TestNavigationCardMetadataAndPaddingSelectWithoutLifecycleAction(t *testing.T) {
	for _, closed := range []bool{false, true} {
		for _, point := range []string{"leading edge", "trailing edge", "metadata", "metadata edge", "bottom border"} {
			t.Run(fmt.Sprintf("closed=%t/%s", closed, point), func(t *testing.T) {
				m := navigationModel()
				target := &m.snapshot.Threads[1]
				target.Closed = closed
				m.prompt.SetValue("keep this draft")
				original := m.state.Active
				f := m.render()
				card := controlHit(t, f, "thread:"+target.ID)
				x, y := card.Rect.X, card.Rect.Y
				// Closed cards have one content row and no metadata row.
				bottom := 3
				if closed {
					bottom = 2
					if strings.HasPrefix(point, "metadata") {
						t.Skip("closed cards omit the metadata row")
					}
				}
				switch point {
				case "trailing edge":
					x += card.Rect.W - 1
				case "metadata":
					x, y = x+4, y+2
				case "metadata edge":
					x, y = x+card.Rect.W-1, y+2
				case "bottom border":
					x, y = x+4, y+bottom
				}
				m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
				if m.state.Active != target.ID || m.busy != nil || m.state.Threads[original].Draft != "keep this draft" {
					t.Fatal("card did not select thread and preserve previous draft")
				}
			})
		}
	}
}

func TestNavigationClosedHeaderAndEmptyDeletedReconciliation(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads[1].Closed = true
	text := ansi.Strip(strings.Join(m.render().rows, "\n"))
	if !strings.Contains(text, "Closed") || strings.Contains(text, "RECENTS") {
		t.Fatal("Closed did not replace Recents")
	}
	deleted := m.state.Active
	m.prompt.SetValue("deleted private draft")
	m.viewState().Draft = m.prompt.Value()
	next := m.snapshot
	next.Revision++
	next.Threads = nil
	m.Update(snapshotMsg(next))
	if m.state.Active != "" || m.prompt.Value() != "" || m.state.Threads[deleted] != nil {
		t.Fatal("deleted active thread retained input or view")
	}
	f := m.measure()
	for _, key := range []string{"empty-new", "empty-closed", "empty-project"} {
		if !hasControl(f, key) {
			t.Fatal("empty state lost recovery action", key)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(m.render().rows, "\n")), "No open threads") {
		t.Fatal("empty navigation missing")
	}
}

func TestNavigationProjectAddFailureRetainsVisibleInput(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		m := navigationModel()
		m.activate(action{Kind: "project-add"})
		m.Update(tea.PasteMsg{Content: "/missing/project"})
		if mouse {
			clickControl(m, controlHit(t, m.measure(), "menu:0"))
		} else {
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		if m.busy == nil || m.projectMode != "add" || len(m.menu) == 0 {
			t.Fatal("submitting folder prematurely hid dialog")
		}
		command := *m.busy
		m.Update(commandMsg{command: command, err: &protocol.Error{Code: "invalid", Message: "directory missing"}})
		if m.projectInput.Value() != "/missing/project" || m.projectMode != "add" || m.busy != nil {
			t.Fatal("server rejection lost entered path or blocked retry")
		}
		if !strings.Contains(ansi.Strip(strings.Join(m.render().rows, "\n")), "directory missing") {
			t.Fatal("folder failure not visible inside modal")
		}
	}
}

func TestNavigationEmptyHasNoInvisiblePromptInput(t *testing.T) {
	m := navigationModel()
	m.prompt.SetValue("existing draft")
	m.snapshot.Threads = nil
	m.state.Active = ""
	m.setFocus("prompt")
	if m.focus != "empty-new" {
		t.Fatal("focus targeted an unavailable composer", m.focus)
	}
	m.Update(tea.PasteMsg{Content: "should not insert"})
	if strings.Contains(m.prompt.Value(), "should not insert") {
		t.Fatal("hidden composer consumed paste")
	}
	m.activate(action{Kind: "send"})
	if m.busy != nil {
		t.Fatal("empty workspace sent prompt")
	}
}

func TestNavigationDeletedActiveRestoresSurvivorWithoutDraftLeak(t *testing.T) {
	m := navigationModel()
	deleted, survivor := m.state.Active, m.snapshot.Threads[1].ID
	m.selectThread(survivor)
	m.prompt.SetValue("survivor draft")
	m.viewState().Draft = m.prompt.Value()
	m.selectThread(deleted)
	m.prompt.SetValue("deleted draft")
	m.viewState().Draft = m.prompt.Value()
	next := m.snapshot
	next.Revision++
	next.Threads = append([]protocol.Thread(nil), next.Threads[1:]...)
	m.Update(snapshotMsg(next))
	if m.state.Active != survivor || m.prompt.Value() != "survivor draft" || m.state.Threads[deleted] != nil {
		t.Fatal("deleted active draft leaked into surviving thread")
	}
}

func TestNavigationCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR for navigation captures")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"dark", "light", "filter", "closed", "delete", "narrow", "scroll", "plain", "ansi"} {
		m := navigationModel()
		m.snapshot.Threads[1].Closed = true
		m.snapshot.Threads[1].State = "idle"
		m.snapshot.Threads[1].Queue = nil
		m.snapshot.Threads[1].Requests = nil
		m.snapshot.Threads[1].Children = nil
		switch scenario {
		case "light":
			m.state.Light = true
		case "filter":
			m.activate(action{Kind: "projects"})
		case "closed":
			m.hover = "thread:" + m.snapshot.Threads[1].ID
		case "delete":
			m.activate(action{Kind: "thread-delete", ID: m.snapshot.Threads[1].ID})
		case "narrow":
			m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
		case "scroll":
			for i := 0; i < 12; i++ {
				m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: fmt.Sprint(i), Title: "Review 界面 é — long title", State: "idle", Project: "Alpha", ProjectID: "alpha"})
			}
			m.Update(tea.WindowSizeMsg{Width: 110, Height: 22})
			m.navScroll = 2
		case "plain":
			m.plainIcons = true
			m.colorProfile = colorprofile.NoTTY
		case "ansi":
			m.colorProfile = colorprofile.ANSI
		}
		m.configureInputs()
		name := fmt.Sprintf("%dx%d-light%t-navigation-%s.ansi", m.width, m.height, m.state.Light, scenario)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNavigationF6ReachesNavigationWithoutInvisibleComposer(t *testing.T) {
	for _, closed := range []bool{false, true} {
		m := navigationModel()
		if closed {
			m.snapshot.Threads[0].Closed = true
		} else {
			m.snapshot.Threads = nil
			m.state.Active = ""
		}
		m.configureInputs()
		m.setFocus("prompt")
		reached := false
		for i := 0; i < 8; i++ {
			m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
			if m.focus == "prompt" {
				t.Fatal("F6 targeted hidden composer")
			}
			if m.focus == "navigation" {
				reached = true
				break
			}
		}
		if !reached {
			t.Fatal("F6 never reached navigation")
		}
	}
}

func TestClosedRowsAreDimmedSingleLineCards(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads[0].Closed = true
	m.snapshot.Threads[1].Closed = true
	_, closed := m.navigationSections()
	if len(closed) != 7 || closed[1].kind != "thread" || closed[2].kind != "card-bottom" {
		t.Fatal("closed cards are not one content row", closed)
	}
	if m.dimmed() == m.colors().muted || m.dimmed() == m.colors().text {
		t.Fatal("dimmed ink does not recede below muted")
	}
}
