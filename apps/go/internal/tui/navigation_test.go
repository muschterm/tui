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
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
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
	if m.busy == nil || m.busy.Kind != "thread.reopen" || m.busy.ThreadID != id {
		t.Fatal("closed row did not reopen")
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
		more := controlHit(t, f, "thread-menu:"+id)
		if quick.Rect.X+quick.Rect.W > row.Rect.X || row.Rect.X+row.Rect.W > more.Rect.X {
			t.Fatal("quick/menu actions overlap thread label")
		}
		m.Update(tea.MouseMotionMsg{X: row.Rect.X, Y: row.Rect.Y})
		f = m.render()
		want := m.icon("check")
		kind := "thread-close"
		if closed {
			want = m.icon("trash")
			kind = "thread-delete"
		}
		painted := ansi.Strip(ansi.Cut(f.rows[quick.Rect.Y], quick.Rect.X, quick.Rect.X+quick.Rect.W))
		if !strings.Contains(painted, want) || quick.Action.Kind != kind {
			t.Fatal("hover did not expose lifecycle icon")
		}
		if !reflect.DeepEqual(f.hits, m.measure().hits) {
			t.Fatal("navigation hitboxes differ when measured")
		}
	}
}

func TestNavigationClosedHeaderAndEmptyDeletedReconciliation(t *testing.T) {
	m := navigationModel()
	m.snapshot.Threads[1].Closed = true
	text := ansi.Strip(strings.Join(m.render().rows, "\n"))
	if !strings.Contains(text, "CLOSED (1)") || strings.Contains(text, "RECENTS") {
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

func TestNavigationEmptyAndClosedHaveNoInvisiblePromptInput(t *testing.T) {
	for _, closed := range []bool{false, true} {
		m := navigationModel()
		m.prompt.SetValue("existing draft")
		if closed {
			m.snapshot.Threads[0].Closed = true
		} else {
			m.snapshot.Threads = nil
			m.state.Active = ""
		}
		m.setFocus("prompt")
		want := "empty-new"
		if closed {
			want = "transcript"
		}
		if m.focus != want {
			t.Fatal("focus targeted an unavailable composer", m.focus)
		}
		m.Update(tea.PasteMsg{Content: "should not insert"})
		if strings.Contains(m.prompt.Value(), "should not insert") {
			t.Fatal("hidden composer consumed paste")
		}
		m.activate(action{Kind: "send"})
		if m.busy != nil {
			t.Fatal("empty or closed thread sent prompt")
		}
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
	for _, scenario := range []string{"dark", "light", "filter", "closed", "delete", "narrow"} {
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
