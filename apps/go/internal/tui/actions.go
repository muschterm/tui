package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func (m *Model) showMenu(title string, items []menuItem) {
	m.projectMode = ""
	m.projectInput.Blur()
	m.menuTitle = title
	m.menu = items
	m.menuIndex = 0
	m.menuOffset = 0
}
func (m *Model) openCommands() {
	items := []menuItem{{"Navigation · show / hide (F2)", action{Kind: "left"}}, {"Right surfaces · show / hide (F3)", action{Kind: "right"}}, {"Bottom panel · show / hide (F5)", action{Kind: "bottom"}}, {"Maximize / restore surface (F7)", action{Kind: "maximize"}}, {"Switch dark / light theme (F8)", action{Kind: "theme"}}, {"Attention", action{Kind: "attention"}}, {"Resume selected thread", action{Kind: "resume"}}, {"Stop selected thread", action{Kind: "interrupt"}}, {"Send / save queued edit (Ctrl+S)", action{Kind: "send"}}, {"Retry pending command", action{Kind: "retry"}}, {"Rebase queued edit after conflict", action{Kind: "refresh-edit"}}, {"Closed · collapse / expand", action{Kind: "recents-collapse"}}, {"Closed · hide / restore", action{Kind: "recents-hide"}}, {"Navigation · grow", action{Kind: "resize-left", Index: 2}}, {"Navigation · shrink", action{Kind: "resize-left", Index: -2}}, {"Right · grow (Alt+Left)", action{Kind: "resize-right", Index: 2}}, {"Right · shrink (Alt+Right)", action{Kind: "resize-right", Index: -2}}, {"Bottom · grow (Alt+Up)", action{Kind: "resize-bottom", Index: 1}}, {"Bottom · shrink (Alt+Down)", action{Kind: "resize-bottom", Index: -1}}, {"Usage details", action{Kind: "usage"}}, {"Copy visible transcript", action{Kind: "copy-transcript"}}, {"Detach TUI (Ctrl+Q)", action{Kind: "quit"}}}
	for _, kind := range []string{"files", "git", "terminal", "agents", "plan", "activity"} {
		items = append(items, menuItem{"Open " + title(kind), action{Kind: "open", Value: kind}})
	}
	for _, t := range m.snapshot.Threads {
		items = append(items, menuItem{"Thread · " + t.Title, action{Kind: "thread", ID: t.ID}})
	}
	for _, tab := range m.viewState().Host.Tabs {
		items = append(items, tabEntry(tab))
	}
	items = append(items, menuItem{"Select / filter projects", action{Kind: "projects"}}, menuItem{"Add project", action{Kind: "project-add"}}, menuItem{"New thread", action{Kind: "thread-create"}}, menuItem{"Closed threads", action{Kind: "closed-threads"}}, menuItem{"Selected thread options", action{Kind: "thread-menu", ID: m.state.Active}})
	m.showMenu("Commands", items)
}
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
func (m *Model) openSurface(kind, id string) {
	v := m.viewState()
	m.state.Layout.Open(&v.Host, kind, title(kind))
	v.DetailID = id
	v.DetailScroll = 0
	if m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Right.W == 0 {
		m.state.Layout.Maximized = true
	}
	m.setFocus("right-body")
}

func (m *Model) activate(a action) tea.Cmd {
	v := m.viewState()
	t := m.thread()
	var cmd tea.Cmd
	switch a.Kind {
	case "scrollbar":
		f := m.measure()
		if target, ok := f.scrollbars[a.ID]; ok {
			m.scrollTo(a.ID, target.Bar.PageAt(a.Index), f)
		}
		return nil
	case "menu-close":
		m.menu = nil
		m.projectMode = ""
		return m.setFocus("prompt")
	case "menu-tab-close":
		m.menu = nil
		return m.activate(action{Kind: "close", ID: a.ID})
	case "menu-select":
		if a.Index >= 0 && a.Index < len(m.menu) {
			item := m.menu[a.Index]
			if item.Action.Kind == "project-submit" || item.Action.Kind == "project-no-match" {
				return m.activate(item.Action)
			}
			m.menu = nil
			m.projectMode = ""
			m.projectInput.Blur()
			return m.activate(item.Action)
		}
		return nil
	case "quit":
		return tea.Quit
	case "left":
		m.state.Layout.ToggleLeft()
	case "right":
		m.state.Layout.ToggleRight(&v.Host)
		if m.state.Layout.Right && m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Right.W == 0 {
			m.showChooser()
		}
	case "bottom":
		m.state.Layout.ToggleBottom()
	case "maximize":
		if g := m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()); g.Right.W == 0 || g.Right.H == 0 {
			return nil
		}
		m.state.Layout.ToggleMaximize(&v.Host)
	case "dismiss-agents":
		if s := agentSummary(t); s.Dismissible && s.Key == a.ID {
			v.DismissedAgents = s.Key
		}
	case "dismiss-plan":
		if s := planSummary(t); s.Dismissible && s.Key == a.ID {
			v.DismissedPlan = s.Key
		}
	case "theme":
		m.state.Light = !m.state.Light
	case "resize-left":
		m.state.Layout.Resize(shell.LeftDivider, a.Index)
	case "resize-right":
		m.state.Layout.Resize(shell.RightDivider, a.Index)
	case "resize-bottom":
		m.state.Layout.Resize(shell.BottomDivider, a.Index)
	case "recents-hide":
		m.state.RecentsHidden = !m.state.RecentsHidden
	case "recents-collapse":
		m.state.RecentsCollapsed = !m.state.RecentsCollapsed
	case "commands":
		m.openCommands()
	case "chooser":
		m.showChooser()
	case "open":
		if a.Value == "terminal" {
			return m.command(protocol.Command{Kind: "terminal.open"}, action{Kind: "terminal-open"})
		}
		m.openSurface(a.Value, a.ID)
	case "tab":
		v.Host.Select(a.ID)
		m.state.Layout.Right = true
		v.DetailScroll = 0
	case "tabs":
		var items []menuItem
		for _, tab := range v.Host.Tabs {
			items = append(items, tabEntry(tab))
		}
		m.showMenu("Opened surfaces", items)
	case "close":
		for _, tab := range v.Host.Tabs {
			if tab.ID == a.ID && tab.Kind == "terminal" {
				return m.command(protocol.Command{Kind: "terminal.close", TargetID: a.ID}, a)
			}
		}
		m.state.Layout.Close(&v.Host, a.ID)
	case "bottom-new":
		return m.command(protocol.Command{Kind: "terminal.open"}, action{Kind: "terminal-open", Value: "bottom"})
	case "bottom-close":
		return m.command(protocol.Command{Kind: "terminal.close", TargetID: v.BottomID}, a)
	case "projects":
		m.openProjectDialog("filter")
	case "project-add":
		m.openProjectDialog("add")
	case "project-filter":
		m.state.ProjectFilter = a.ID
		m.navScroll = 0
		m.menu = nil
		m.projectMode = ""
	case "project-cancel":
		m.menu = nil
		m.projectMode = ""
	case "project-no-match":
		return nil
	case "project-submit":
		path := strings.TrimSpace(m.projectInput.Value())
		if path == "" {
			m.status = "Enter an existing folder path"
			m.projectError = m.status
			return nil
		}
		m.projectError = ""
		return m.command(protocol.Command{Kind: "project.add", Path: path}, a)
	case "thread-create":
		if m.state.Edit != nil {
			m.status = "Save or cancel the queued edit first"
			return nil
		}
		project := a.Value
		if project == "" {
			project = m.state.ProjectFilter
		}
		if project == "" && len(m.snapshot.Projects) == 1 {
			project = m.snapshot.Projects[0].ID
		}
		if project == "" {
			var items []menuItem
			for _, p := range m.snapshot.Projects {
				items = append(items, menuItem{p.Name + " · " + p.Path, action{Kind: "thread-create", Value: p.ID}})
			}
			items = append(items, menuItem{"Add project…", action{Kind: "project-add"}})
			m.showMenu("New thread in project", items)
			break
		}
		return m.command(protocol.Command{Kind: "thread.create", ProjectID: project}, a)
	case "thread-menu":
		m.threadMenu(a.ID)
	case "thread-delete":
		m.confirmThreadDelete(a.ID)
	case "thread-delete-cancel":
		m.menu = nil
	case "thread-delete-confirm":
		m.menu = nil
		return m.command(protocol.Command{Kind: "thread.delete", ThreadID: a.ID, Revision: int64(a.Index)}, a)
	case "thread-close", "thread-reopen":
		if target, ok := m.threadByID(a.ID); ok {
			if m.state.Edit != nil && m.state.Active == a.ID {
				m.status = "Save or cancel the queued edit first"
				return nil
			}
			kind := "thread.reopen"
			if a.Kind == "thread-close" {
				if reason := protocol.ThreadCloseBlocked(target); reason != "" {
					m.status = reason
					return nil
				}
				kind = "thread.close"
			}
			return m.command(protocol.Command{Kind: kind, ThreadID: a.ID, Revision: target.LifecycleRevision}, a)
		}
	case "closed-threads":
		var items []menuItem
		for _, t := range m.snapshot.Threads {
			if t.Closed && (m.state.ProjectFilter == "" || t.ProjectID == m.state.ProjectFilter) {
				items = append(items, menuItem{t.Title, action{Kind: "thread-reopen", ID: t.ID}})
			}
		}
		if len(items) == 0 {
			m.status = "No closed threads in this project filter"
		} else {
			m.showMenu("Reopen closed thread", items)
		}
	case "thread":
		if m.state.Edit != nil {
			m.status = "Save or cancel this queued edit before switching threads"
			return nil
		}
		if target, ok := m.threadByID(a.ID); ok && target.Closed {
			return m.activate(action{Kind: "thread-reopen", ID: a.ID})
		}
		m.selectThread(a.ID)
	case "attention":
		var items []menuItem
		for _, thread := range m.snapshot.Threads {
			for _, r := range thread.Requests {
				if r.State == "pending" {
					items = append(items, menuItem{thread.Title + " · " + r.Title, action{Kind: "attention-item", ID: thread.ID, Value: r.ID}})
				}
			}
			if thread.State == "failed" {
				items = append(items, menuItem{thread.Title + " · failed", action{Kind: "thread", ID: thread.ID}})
			}
		}
		if len(items) == 0 {
			m.status = "No pending attention"
		} else {
			m.showMenu("Attention", items)
		}
	case "attention-item":
		cmd = m.activate(action{Kind: "thread", ID: a.ID})
		if m.state.Active == a.ID {
			for i, r := range m.requests() {
				if r.ID == a.Value {
					m.viewState().RequestIndex = i
					m.viewState().QuestionIndex = 0
					m.loadAnswer()
				}
			}
		}
	case "resume":
		return m.command(protocol.Command{Kind: "thread.resume"}, a)
	case "interrupt":
		if !activeTurn(t) {
			return nil
		}
		return m.command(protocol.Command{Kind: "thread.interrupt"}, a)
	case "retry":
		if m.inFlight {
			m.status = "Command in flight"
			return nil
		}
		if m.busy != nil {
			return m.dispatch(*m.busy, m.busyAction)
		}
		m.status = "No uncertain command to retry"
	case "send":
		if m.state.Active == "" || t.Closed {
			m.status = "Open a thread before sending a prompt"
			return nil
		}
		text := strings.TrimSpace(m.prompt.Value())
		if text == "" {
			m.status = "Write a prompt first"
			return nil
		}
		v.Draft = m.prompt.Value()
		settings := v.Settings
		if m.state.Edit != nil {
			return m.command(protocol.Command{Kind: "queue.edit", Text: text, TargetID: m.state.Edit.ID, Revision: m.state.Edit.Revision, Settings: &settings}, action{Kind: "save-edit"})
		}
		captures := slices.Clone(v.Attachments)
		for i := range captures {
			captures[i].Content = fmt.Sprintf("Synthetic %s content captured at Send, fixture tick %d", captures[i].Kind, t.Tick)
		}
		return m.command(protocol.Command{Kind: "prompt.send", Text: v.Draft, Settings: &settings, Attachments: captures}, a)
	case "edit":
		if m.state.Edit != nil {
			m.status = "Finish the current queued edit first"
			return nil
		}
		for _, q := range t.Queue {
			if q.ID == a.ID {
				m.state.Edit = &editState{q.ID, t.ID, m.prompt.Value(), v.Settings, t.QueueRevision}
				m.prompt.SetValue(q.Text)
				v.Draft = q.Text
				v.Settings = q.Settings
				cmd = m.setFocus("prompt")
			}
		}
	case "refresh-edit":
		if m.state.Edit != nil {
			for _, q := range t.Queue {
				if q.ID == m.state.Edit.ID {
					m.state.Edit.Revision = t.QueueRevision
					m.status = "Edit rebased onto current queue revision; review and save explicitly"
				}
			}
		}
	case "cancel-edit":
		if m.state.Edit != nil {
			m.prompt.SetValue(m.state.Edit.OldDraft)
			v.Draft = m.state.Edit.OldDraft
			v.Settings = m.state.Edit.OldSettings
			m.state.Edit = nil
		}
	case "remove":
		return m.command(protocol.Command{Kind: "queue.remove", TargetID: a.ID, Revision: t.QueueRevision}, a)
	case "move":
		order := make([]string, len(t.Queue))
		i := -1
		for j, q := range t.Queue {
			order[j] = q.ID
			if q.ID == a.ID {
				i = j
			}
		}
		if i >= 0 && i+a.Index >= 0 && i+a.Index < len(order) {
			order[i], order[i+a.Index] = order[i+a.Index], order[i]
			return m.command(protocol.Command{Kind: "queue.reorder", Order: order, Revision: t.QueueRevision}, a)
		}
	case "queue":
		var items []menuItem
		for _, q := range t.Queue {
			for _, op := range []string{"edit", "remove", "up", "down"} {
				act := action{Kind: op, ID: q.ID}
				if op == "up" {
					act.Kind = "move"
					act.Index = -1
				}
				if op == "down" {
					act.Kind = "move"
					act.Index = 1
				}
				items = append(items, menuItem{title(op) + " · " + safe(q.Text), act})
			}
		}
		m.showMenu("Prompt queue", items)
	case "usage-summary":
		m.openUsageSummary()
		return nil
	case "composer-more":
		m.openComposerOverflow()
		return nil
	case "settings":
		field := a.Value
		if field == "" {
			field = "effort"
		}
		var items []menuItem
		if field == "effort" {
			for _, value := range []string{"low", "medium", "high"} {
				items = append(items, menuItem{effortDisplayName(value), action{Kind: "setting", Value: value}})
			}
		} else {
			selected := v.Settings
			if field == "effective" {
				selected = t.Effective
			}
			items = append(items, menuItem{agentDisplayName(t.Agent) + " · " + modelDisplayName(selected.Model), action{Kind: "noop"}}, menuItem{"Reasoning: " + effortDisplayName(selected.Effort), action{Kind: "noop"}}, menuItem{"Permissions: " + permissionDisplayName(selected.Permissions), action{Kind: "noop"}}, menuItem{"Context: " + selected.Context + " · Speed: " + selected.Speed, action{Kind: "noop"}}, menuItem{"Demo connection · no provider is connected", action{Kind: "noop"}}, menuItem{"Change effort", action{Kind: "settings", Value: "effort"}})
		}
		m.showMenu("Settings · "+field, items)
	case "setting":
		v.Settings.Effort = a.Value
	case "attach":
		if m.state.Edit != nil {
			m.status = "Queued attachment captures are preserved; finish this edit first"
			return nil
		}
		var items []menuItem
		for _, kind := range []string{"file", "lines", "image", "git-diff", "terminal-output"} {
			items = append(items, menuItem{title(kind) + " context", action{Kind: "attach-kind", Value: kind}})
		}
		m.showMenu("Context", items)
	case "attach-kind":
		if len(v.Attachments) >= 8 {
			m.status = "Attachment limit: 8"
			return nil
		}
		v.Attachments = append(v.Attachments, protocol.Attachment{Kind: a.Value, Name: a.Value + " sample", Source: "fixture://context/" + a.Value, Content: ""})
	case "attachment-remove":
		if a.Index < len(v.Attachments) {
			v.Attachments = slices.Delete(v.Attachments, a.Index, a.Index+1)
		}
	case "attachments":
		if m.state.Edit != nil {
			m.status = "Queued attachment captures are unchanged"
			return nil
		}
		var items []menuItem
		for i, x := range v.Attachments {
			items = append(items, menuItem{"Remove " + x.Name, action{Kind: "attachment-remove", Index: i}})
		}
		m.showMenu("Attached context", items)
	case "children":
		var items []menuItem
		for _, c := range t.Children {
			if c.State == "running" {
				items = append(items, menuItem{c.Name, action{Kind: "open", Value: "agents", ID: c.ID}})
			}
		}
		m.showMenu("Running agents", items)
	case "request-next":
		v.RequestIndex++
		v.QuestionIndex, v.RequestScroll = 0, 0
		m.loadAnswer()
	case "request-select":
		var items []menuItem
		for _, r := range m.requests() {
			items = append(items, menuItem{agentDisplayName(r.Origin) + " · " + r.Title, action{Kind: "request-index", ID: r.ID}})
		}
		m.showMenu("Pending requests", items)
	case "request-index":
		for i, r := range m.requests() {
			if r.ID == a.ID {
				v.RequestIndex, v.QuestionIndex, v.RequestScroll = i, 0, 0
				m.loadAnswer()
			}
		}
	case "question":
		m.selectQuestion(v.QuestionIndex + a.Index)
	case "question-index":
		m.selectQuestion(a.Index)
	case "question-tabs":
		if r, ok := m.request(); ok {
			var items []menuItem
			for i, q := range r.Questions {
				items = append(items, menuItem{questionTabLabel(q, i, false), action{Kind: "question-index", Index: i}})
			}
			m.showMenu("Questions", items)
		}
	case "answer-choice":
		m.chooseAnswer(a.Value)
	case "answer-other":
		m.toggleOther()
	case "answer-options":
		if r, ok := m.request(); ok && len(r.Questions) > 0 {
			q := r.Questions[v.QuestionIndex]
			d := m.questionDraft(r, v.QuestionIndex)
			var items []menuItem
			for _, o := range q.Options {
				mark := m.questionMarker(protocol.QuestionKind(q), slices.Contains(d.Choices, o)) + " "
				items = append(items, menuItem{mark + o, action{Kind: "answer-choice", Value: o}})
			}
			if protocol.QuestionAllowsOther(q) {
				items = append(items, menuItem{m.questionMarker(protocol.QuestionKind(q), d.Other) + " Other…", action{Kind: "answer-other"}})
			}
			m.showMenu("Answer options", items)
		}
	case "approval-options":
		if r, ok := m.request(); ok && r.Kind == "approval" {
			var items []menuItem
			for _, choice := range r.Choices {
				items = append(items, menuItem{choice, action{Kind: "approve", Value: choice}})
			}
			m.showMenu("Approval choices", items)
		}
	case "answer-submit":
		return m.submitAnswers(a)
	case "approve":
		if r, ok := m.request(); ok {
			return m.command(protocol.Command{Kind: "request.answer", TargetID: r.ID, Revision: r.Revision, Answers: []string{a.Value}}, a)
		}
	case "request-detail":
		if r, ok := m.request(); ok {
			m.openSurface("activity", r.ID)
		}
	case "usage":
		m.openSurface("activity", "usage")
	case "copy-transcript":
		var b strings.Builder
		for _, a := range t.Activity {
			fmt.Fprintf(&b, "%s\n%s\n\n", safe(a.Title), safe(a.Text))
		}
		m.status = "Transcript sent to terminal clipboard"
		return tea.SetClipboard(b.String())
	}
	m.viewState().RightVisible = m.state.Layout.Right
	m.markDirty()
	m.configureInputs()
	return cmd
}
func (m *Model) showChooser() {
	var items []menuItem
	for _, kind := range []string{"files", "git", "terminal", "agents", "plan", "activity"} {
		items = append(items, menuItem{title(kind), action{Kind: "open", Value: kind}})
	}
	m.showMenu("Add surface", items)
}
