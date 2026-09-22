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
	m.projectGear = false
	m.projectInput.Blur()
	m.menuTitle = title
	m.menu = items
	m.menuIndex = 0
	m.menuOffset = 0
}

func (m *Model) openCommands() {
	if m.terminalTooSmall() {
		// Nothing here submits, stops, deletes or changes queue/lifecycle state.
		m.showMenu("Commands", []menuItem{{"Detach TUI (Ctrl+Q)", action{Kind: "quit"}}, {"Suspend (Ctrl+Z)", action{Kind: "suspend"}}, {"Switch dark / light theme (F8)", action{Kind: "theme"}}})
		return
	}
	items := []menuItem{{"Navigation · show / hide (F2)", action{Kind: "left"}}, {"Right surfaces · show / hide (F3)", action{Kind: "right"}}, {"Bottom panel · show / hide (F5)", action{Kind: "bottom"}}, {"Maximize / restore surface (F7)", action{Kind: "maximize"}}, {"Switch dark / light theme (F8)", action{Kind: "theme"}}, {"Attention", action{Kind: "attention"}}, {"Resume selected thread", action{Kind: "resume"}}, {"Stop selected thread", action{Kind: "interrupt"}}, {"Send / save queued edit (Ctrl+S)", action{Kind: "send"}}, {"Retry pending command", action{Kind: "retry"}}, {"Rebase queued edit after conflict", action{Kind: "refresh-edit"}}, {"Closed · collapse / expand", action{Kind: "recents-collapse"}}, {"Closed · hide / restore", action{Kind: "recents-hide"}}, {"Navigation · grow", action{Kind: "resize-left", Index: 2}}, {"Navigation · shrink", action{Kind: "resize-left", Index: -2}}, {"Right · grow (Alt+Left)", action{Kind: "resize-right", Index: 2}}, {"Right · shrink (Alt+Right)", action{Kind: "resize-right", Index: -2}}, {"Bottom · grow (Alt+Up)", action{Kind: "resize-bottom", Index: 1}}, {"Bottom · shrink (Alt+Down)", action{Kind: "resize-bottom", Index: -1}}, {"Usage details", action{Kind: "usage"}}, {"Copy visible transcript", action{Kind: "copy-transcript"}}, {"Detach TUI (Ctrl+Q)", action{Kind: "quit"}}}
	for _, kind := range []string{"files", "git", "terminal", "agents", "plan", "activity"} {
		items = append(items, menuItem{"Open " + title(kind), action{Kind: "open", Value: kind}})
	}
	if m.singleColumn() {
		items[0] = menuItem{"Choose column (F2)", action{Kind: "columns"}}
		items[1] = menuItem{"Surfaces (F3)", action{Kind: "column", Index: int(shell.RightRegion)}}
		items[2] = menuItem{"Terminal (F5)", action{Kind: "column", Index: int(shell.BottomRegion)}}
		items[3] = menuItem{"Conversation", action{Kind: "column", Index: int(shell.CenterRegion)}}
	}
	for _, t := range m.snapshot.Threads {
		items = append(items, menuItem{"Thread · " + t.Title, action{Kind: "thread", ID: t.ID}})
	}
	for _, tab := range m.viewState().Host.Tabs {
		items = append(items, tabEntry(tab))
	}
	items = append(items, menuItem{"App settings", action{Kind: "app-settings"}}, menuItem{"Select / filter projects", action{Kind: "projects"}}, menuItem{"Add project", action{Kind: "project-add"}}, menuItem{"New thread", action{Kind: "thread-create"}}, menuItem{"Closed threads", action{Kind: "closed-threads"}}, menuItem{"Selected thread options", action{Kind: "thread-menu", ID: m.state.Active}})
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
	if m.singleColumn() {
		v.Host.Open(kind, title(kind))
		m.selectColumn(shell.RightRegion)
	} else {
		m.state.Layout.Open(&v.Host, kind, title(kind))
	}
	v.DetailID = id
	v.DetailScroll = 0
	if !m.singleColumn() && m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Right.W == 0 {
		m.state.Layout.RevealRight() // effective only while it cannot fit; not a preference
	}
	m.setFocus("right-body")
}

// hiddenWorkBlocked names why work-affecting actions are refused while their
// controls cannot be seen.
func (m *Model) hiddenWorkBlocked() string {
	if m.terminalTooSmall() {
		return fmt.Sprintf("Enlarge the terminal to at least %d×%d first", minTerminalWidth, minTerminalHeight)
	}
	if m.settingsPage != "" {
		return "Return to the workspace first"
	}
	return ""
}

func (m *Model) activate(a action) tea.Cmd {
	if m.terminalTooSmall() && !slices.Contains([]string{"commands", "quit", "suspend", "theme", "menu-close", "menu-select", "scrollbar"}, a.Kind) {
		// A menu opened before the resize must not act on hidden content.
		return m.showNotice(m.hiddenWorkBlocked())
	}
	if requestScoped(a) && !m.requestBound(a) {
		m.status = "That request changed or was resolved · nothing sent"
		return m.showNotice(m.status)
	}
	if slices.Contains([]string{"answer-submit", "approve", "steer"}, a.Kind) {
		if reason := m.hiddenWorkBlocked(); reason != "" {
			return m.showNotice(reason)
		}
	}
	if handled, cmd := m.activateSidebarSettings(a); handled {
		m.configureInputs()
		return cmd
	}
	if m.singleColumn() && strings.HasPrefix(a.Kind, "resize-") {
		return m.showNotice("Pane resizing is available in the wider layout")
	}
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
			if item.Action.Kind == "project-submit" || item.Action.Kind == "project-rename-submit" || item.Action.Kind == "project-no-match" || strings.HasPrefix(item.Action.Kind, "path-") {
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
	case "suspend":
		return tea.Suspend
	case "columns":
		m.openColumns()
	case "column":
		m.selectColumn(shell.Region(a.Index))
	case "left":
		if m.singleColumn() {
			m.openColumns()
		} else {
			m.state.Layout.ToggleLeft()
		}
	case "right":
		if m.singleColumn() {
			m.selectColumn(shell.RightRegion)
			break
		}
		m.state.Layout.ToggleRight(&v.Host)
		if m.state.Layout.Right && m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Right.W == 0 {
			m.showChooser()
		}
	case "bottom":
		if m.singleColumn() {
			// Choosing the compact Terminal column only changes presentation;
			// its add control opens the first session.
			m.selectColumn(shell.BottomRegion)
			break
		}
		m.state.Layout.ToggleBottom()
		// Showing the wide panel is the explicit request for a terminal: an
		// empty panel opens its first session at once instead of a header
		// and button.
		if len(v.Bottom.Tabs) == 0 && m.bottomShown() {
			return m.command(protocol.Command{Kind: "terminal.open"}, action{Kind: "terminal-open", Value: "bottom"})
		}
	case "maximize":
		if m.singleColumn() {
			return nil
		}
		if g := m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()); g.Right.W == 0 || g.Right.H == 0 {
			return nil
		}
		if m.state.Layout.Compute(m.width, m.height-1, 0).Forced {
			return m.showNotice("Maximize needs room beside the conversation · F3 hides the right panel")
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
	case "icons":
		// Saved explicitly either way, so the choice survives a changed
		// environment default.
		if m.iconsSetting() == "ascii" {
			m.state.Icons = "nerd"
		} else {
			m.state.Icons = "ascii"
		}
		m.applyIcons()
	case "resize-left":
		m.resizePane(shell.LeftDivider, a.Index)
	case "resize-right":
		m.resizePane(shell.RightDivider, a.Index)
	case "resize-bottom":
		m.resizePane(shell.BottomDivider, a.Index)
	case "recents-hide":
		m.state.RecentsHidden = !m.state.RecentsHidden
	case "recents-collapse":
		m.state.RecentsHidden = false
		m.state.RecentsCollapsed = !m.state.RecentsCollapsed
	case "commands":
		if m.settingsPage != "" {
			m.openSettingsCommands()
		} else {
			m.openCommands()
		}
	case "chooser":
		m.showChooser()
	case "open":
		if a.Value == "terminal" {
			cmd = m.command(protocol.Command{Kind: "terminal.open"}, action{Kind: "terminal-open"})
			if cmd != nil {
				m.selectColumn(shell.RightRegion)
			}
			break
		}
		m.openSurface(a.Value, a.ID)
	case "tab":
		v.Host.Select(a.ID)
		if m.singleColumn() {
			m.selectColumn(shell.RightRegion)
		} else {
			m.state.Layout.Right = true
		}
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
		if m.singleColumn() && len(v.Host.Tabs) == 0 {
			m.selectColumn(shell.CenterRegion)
		}
	case "bottom-new":
		return m.command(protocol.Command{Kind: "terminal.open"}, action{Kind: "terminal-open", Value: "bottom"})
	case "bottom-tab":
		if v.Bottom.Select(a.ID) {
			v.BottomScroll = 0
		}
	case "bottom-tabs":
		var items []menuItem
		for _, tab := range v.Bottom.Tabs {
			items = append(items, menuItem{tab.Title, action{Kind: "bottom-tab", ID: tab.ID, Value: tab.Kind}})
		}
		m.showMenu("Bottom terminals", items)
	case "bottom-close":
		return m.command(protocol.Command{Kind: "terminal.close", TargetID: a.ID}, a)
	case "projects":
		m.openProjectDialog("filter")
	case "project-add":
		m.projectAddThread = false
		m.openProjectDialog("add")
	case "project-add-thread":
		m.projectAddThread = true
		m.openProjectDialog("add")
	case "path-directory":
		m.projectInput.SetValue(a.Value)
		m.projectError = ""
		m.menuIndex = 0
		m.refreshProjectMenu()
		return tea.Batch(m.setFocus("project-input"), m.nextPathQuery())
	case "path-project-root-save":
		return m.saveProjectDirectory(a.Value)
	case "path-project-root-home":
		m.projectInput.SetValue("~/")
		m.menuIndex = 0
		m.refreshProjectMenu()
		return tea.Batch(m.setFocus("project-input"), m.nextPathQuery())
	case "path-noop":
		return nil
	case "mention-select":
		return m.selectMention(a.Index)
	case "mention-background":
		return nil
	case "context-error":
		m.showContextError()
	case "mention-dismiss":
		m.dismissMention()
		return m.setFocus("prompt")
	case "project-filter":
		m.state.ProjectFilter = a.ID
		m.navScroll = 0
		m.closedScroll = 0
		m.menu = nil
		m.projectMode = ""
	case "project-cancel":
		m.menu = nil
		m.projectMode = ""
		return m.setFocus("prompt")
	case "project-no-match":
		return nil
	case "project-submit":
		path := strings.TrimSpace(m.projectInput.Value())
		if a.Value != "" {
			path = a.Value
		}
		if path == "" {
			m.status = "Enter an existing folder path"
			m.projectError = m.status
			return nil
		}
		m.projectError = ""
		if m.projectAddThread {
			a.Value = "new-thread"
		} else {
			a.Value = ""
		}
		return m.command(protocol.Command{Kind: "project.add", Path: path}, a)
	case "thread-create":
		if m.state.Edit != nil {
			m.status = "Save or cancel the queued edit first"
			return nil
		}
		if a.Value == "" {
			m.openProjectDialog("new-thread")
			break
		}
		m.projectMode, m.menu = "", nil
		m.beginThreadDraft(a.Value)
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
			// Reopen selects its thread; Close selects another when it is active.
			if m.state.Edit != nil && (a.Kind == "thread-reopen" || m.state.Active == a.ID || m.state.Edit.ThreadID == a.ID) {
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
				items = append(items, menuItem{t.Title, action{Kind: "thread", ID: t.ID}})
			}
		}
		if len(items) == 0 {
			m.status = "No closed threads in this project filter"
		} else {
			m.showMenu("Closed threads", items)
		}
	case "thread":
		if m.state.Edit != nil && a.ID != m.state.Edit.ThreadID {
			m.status = "Save or cancel this queued edit before switching threads"
			return nil
		}
		m.selectThread(a.ID)
		m.selectColumn(shell.CenterRegion)
	case "attention":
		if m.settingsPage != "" {
			m.closeSettings()
		}
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
					m.selectRequest(i)
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
			return m.dispatch(*m.busy, m.busyAction, true)
		}
		m.status = "No uncertain command to retry"
	case "send":
		if reason := m.sendBlocked(); reason != "" {
			return m.showNotice(reason)
		}
		text := strings.TrimSpace(m.prompt.Value())
		v.Draft = m.prompt.Value()
		settings := m.composerSelection()
		if m.state.Edit != nil {
			return m.command(protocol.Command{Kind: "queue.edit", Text: text, TargetID: m.state.Edit.ID, Revision: m.state.Edit.Revision, Settings: &settings}, action{Kind: "save-edit"})
		}
		captures := slices.Clone(v.Attachments)
		for i := range captures {
			if captures[i].Kind != "workspace-file" {
				captures[i].Content = fmt.Sprintf("Synthetic %s content captured at Send, fixture tick %d", captures[i].Kind, t.Tick)
			}
		}
		c := protocol.Command{Kind: "prompt.send", Text: v.Draft, Settings: &settings, Attachments: captures}
		if m.creatingThread() {
			c.Kind, c.ProjectID, c.Agent = "thread.start", m.state.DraftProjectID, m.agentCommandID(t)
		} else if t.Closed {
			c.Kind, c.Revision = "prompt.reopen-send", t.LifecycleRevision
		}
		return m.command(c, a)
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
		if e := m.state.Edit; e != nil {
			// Restore into the edited thread, never into whichever is active.
			if ev := m.state.Threads[e.ThreadID]; ev != nil {
				ev.Draft, ev.Settings = e.OldDraft, e.OldSettings
			}
			if m.state.Active == e.ThreadID && !m.creatingThread() {
				m.prompt.SetValue(e.OldDraft)
			}
			m.state.Edit = nil
		}
	case "remove":
		return m.command(protocol.Command{Kind: "queue.remove", TargetID: a.ID, Revision: t.QueueRevision}, a)
	case "steer":
		return m.steerQueued(a)
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
			items = append(items, menuItem{"Steer · " + safe(q.Text), m.steerAction(q.ID)})
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
	case "checkout-info":
		m.openCheckoutInfo()
		return nil
	case "checkout-refresh":
		m.checkoutKey = ""
		return m.nextCheckoutInspection()
	case "composer-more":
		m.openComposerOverflow()
		return nil
	case "settings":
		return m.openPromptSettings(a.Value)
	case "setting-model":
		if m.configurationLocked() {
			return m.showNotice("Settings are read-only during active work")
		}
		if v.Settings.Model != "fixture-model" {
			v.Settings = protocol.Settings{Model: "fixture-model", Effort: "medium", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}
		}
	case "provider-unavailable":
		return m.showNotice(a.Value + " integration is planned; only Demo is connected in this build")
	case "setting":
		if m.configurationLocked() {
			return m.showNotice("Settings are read-only during active work")
		}
		v.Settings.Effort = a.Value
	case "setting-agent":
		cmd = m.chooseAgent(a.Value)
	case "setting-field":
		cmd = m.chooseSetting(a.ID, a.Value)
	case "agent-probe":
		if agent, ok := m.agentByID(a.ID); ok && agent.Kind == "fixture" {
			return m.showNotice(agent.Name + " has fixed options; nothing to probe")
		}
		return m.command(protocol.Command{Kind: "agent.probe", TargetID: a.ID, ProjectID: m.agentProjectID()}, a)
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
			if len(v.Attachments) == 0 {
				v.ContextError = ""
			}
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
		m.selectRequest(m.requestIndex(m.requests()) + 1)
	case "request-select":
		var items []menuItem
		for _, r := range m.requests() {
			items = append(items, menuItem{agentDisplayName(r.Origin) + " · " + r.Title, action{Kind: "request-index", ID: r.ID}})
		}
		m.showMenu("Pending requests", items)
	case "request-index":
		for i, r := range m.requests() {
			if r.ID == a.ID {
				m.selectRequest(i)
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
				items = append(items, menuItem{questionTabLabel(q, i, false), action{Kind: "question-index", ID: r.ID, Index: i, Revision: r.Revision}})
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
				items = append(items, menuItem{mark + o, action{Kind: "answer-choice", ID: r.ID, Value: o, Revision: r.Revision}})
			}
			if protocol.QuestionAllowsOther(q) {
				items = append(items, menuItem{m.questionMarker(protocol.QuestionKind(q), d.Other) + " Other…", action{Kind: "answer-other", ID: r.ID, Revision: r.Revision}})
			}
			m.showMenu("Answer options", items)
		}
	case "approval-options":
		if r, ok := m.request(); ok && r.Kind == "approval" {
			var items []menuItem
			for i, choice := range r.Choices {
				items = append(items, menuItem{choice, m.approvalAction(r, i)})
			}
			m.showMenu("Approval choices", items)
		}
	case "answer-submit":
		return m.submitAnswers(a)
	case "approve":
		return m.submitApproval(a)
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

// bottomShown reports whether the wide center-bottom panel is actually
// presented for an authoritative thread: a draft has no thread to own a
// session, and a short height collapses the panel.
func (m *Model) bottomShown() bool {
	if m.state.Active == "" || m.creatingThread() || m.singleColumn() {
		return false
	}
	return m.state.Layout.Bottom && m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Bottom.H > 0
}
