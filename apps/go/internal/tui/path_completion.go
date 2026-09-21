package tui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// Completion is transient client state. Each request is bound to its input and
// destination; changing either cancels I/O and invalidates late results.
type pathCompletion struct {
	key        string
	generation uint64
	loading    bool
	result     protocol.BrowseResult
	err        string
	cancel     context.CancelFunc
}
type pathQueryReady struct {
	key        string
	generation uint64
	request    protocol.BrowseRequest
}
type pathQueryResult struct {
	key        string
	generation uint64
	result     protocol.BrowseResult
	err        error
}
type promptMention struct {
	start, end int
	query, key string
}

func (m *Model) showContextError() {
	width := max(1, min(68, max(20, m.width-6))-5)
	var items []menuItem
	for _, line := range strings.Split(ansi.Wrap(safe(m.viewState().ContextError), width, ""), "\n") {
		items = append(items, menuItem{line, action{Kind: "context-error"}})
	}
	items = append(items, menuItem{"Manage attached files…", action{Kind: "attachments"}}, menuItem{"Close", action{Kind: "menu-close"}})
	m.showMenu("Send failed · draft preserved", items)
}

func (m *Model) activeMention() (promptMention, bool) {
	if m.settingsPage != "" || len(m.menu) > 0 || m.terminalTooSmall() || m.focus != "prompt" || !m.hasComposer() || m.prompt.HasSelection() {
		return promptMention{}, false
	}
	value := []rune(m.prompt.Value())
	end := min(len(value), inputCursor(&m.prompt))
	// Find an @ at a word boundary. Quoted paths allow spaces while traversing.
	for start := end - 1; start >= 0; start-- {
		if value[start] != '@' || start > 0 && !unicode.IsSpace(value[start-1]) {
			continue
		}
		tail := string(value[start+1 : end])
		if strings.HasPrefix(tail, "\"") {
			tail = strings.TrimPrefix(tail, "\"")
			if strings.ContainsAny(tail, "\"\n\r") {
				return promptMention{}, false
			}
		} else if strings.ContainsFunc(tail, unicode.IsSpace) || strings.ContainsRune(tail, '"') {
			return promptMention{}, false
		}
		target, _, _ := m.checkoutTarget()
		mention := promptMention{start: start, end: end, query: tail, key: fmt.Sprintf("%s:%d:%s", target, start, tail)}
		return mention, mention.key != m.mentionDismissed
	}
	return promptMention{}, false
}

func (m *Model) pathRequest() (string, protocol.BrowseRequest) {
	if m.projectMode == "add" || m.projectMode == "project-root" {
		req := protocol.BrowseRequest{Scope: "projects", Query: m.projectInput.Value()}
		return fmt.Sprintf("%s:%s:%s", m.projectMode, m.snapshot.AppSettings.ProjectDirectory, req.Query), req
	}
	if mention, ok := m.activeMention(); ok {
		_, projectID, threadID := m.checkoutTarget()
		return "mention:" + mention.key, protocol.BrowseRequest{Scope: "files", ProjectID: projectID, ThreadID: threadID, Query: mention.query}
	}
	return "", protocol.BrowseRequest{}
}

func (m *Model) nextPathQuery() tea.Cmd {
	key, req := m.pathRequest()
	if key == m.paths.key {
		return nil
	}
	if m.paths.cancel != nil {
		m.paths.cancel()
	}
	m.paths = pathCompletion{key: key, generation: m.paths.generation + 1}
	m.mentionIndex = 0
	if key == "" {
		return nil
	}
	switch {
	case !m.hasCapability("path-completion"):
		m.paths.err = "Update this server to browse folders and files"
	case req.Scope == "files" && !m.hasCapability("workspace-file-context"):
		m.paths.err = "Update this server to attach project files"
	case !m.connected:
		m.paths.err = "Disconnected · reconnect to browse"
	case m.state.Edit != nil && req.Scope == "files":
		m.paths.err = "Save or cancel the queued edit before attaching files"
	default:
		m.paths.loading = true
	}
	if req.Scope == "projects" {
		m.refreshProjectMenu()
	}
	if !m.paths.loading {
		return nil
	}
	pending := pathQueryReady{key: key, generation: m.paths.generation, request: req}
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return pending })
}

func (m *Model) runPathQuery(msg pathQueryReady) tea.Cmd {
	key, _ := m.pathRequest()
	if msg.key != key || msg.generation != m.paths.generation || m.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
	m.paths.cancel = cancel
	connection := m.client
	return func() tea.Msg {
		defer cancel()
		result, err := connection.Browse(ctx, msg.request)
		return pathQueryResult{key: msg.key, generation: msg.generation, result: result, err: err}
	}
}
func (m *Model) acceptPathQuery(msg pathQueryResult) {
	key, _ := m.pathRequest()
	if key != msg.key || msg.generation != m.paths.generation {
		return
	}
	m.paths.loading = false
	m.paths.result = msg.result
	m.paths.err = ""
	if msg.err != nil {
		m.paths.err = safe(msg.err.Error())
	}
	if m.projectMode == "add" || m.projectMode == "project-root" {
		m.refreshProjectMenu()
		// A typed prefix selects a matching child. Entering a directory restores
		// the explicit use/save action; completing a prefix never registers its parent.
		query := m.projectInput.Value()
		if query != "" && query != "~" && !strings.HasSuffix(query, "/") && len(msg.result.Entries) > 0 {
			for i, item := range m.menu {
				if item.Action.Kind == "path-directory" && item.Action.Value == msg.result.Entries[0].Path {
					m.menuIndex = i
					break
				}
			}
		} else if query != "" && query != "~" && !strings.HasSuffix(query, "/") && len(msg.result.Entries) == 0 {
			for i, item := range m.menu {
				if item.Action.Kind == "path-noop" {
					m.menuIndex = i
					break
				}
			}
		}
	}
}

func (m *Model) folderMenu() []menuItem {
	key, _ := m.pathRequest()
	if !m.hasCapability("path-completion") {
		// Older backends still support manual registration, with explicit guidance.
		m.projectError = "Update this server for folder suggestions"
		return []menuItem{{"Add folder as project", action{Kind: "project-submit"}}, {"Cancel", action{Kind: "project-cancel"}}}
	}
	current := m.paths.result.Directory
	var items []menuItem
	if key != m.paths.key || m.paths.loading {
		items = append(items, menuItem{"Looking for folders…", action{Kind: "path-noop"}})
	} else if m.paths.err != "" {
		items = append(items, menuItem{m.paths.err, action{Kind: "path-noop"}})
	} else {
		if current != "" {
			label, kind := "Add this folder", "project-submit"
			if m.projectMode == "project-root" {
				label, kind = "Save starting folder", "path-project-root-save"
			}
			query := m.projectInput.Value()
			if query == "" || query == "~" || strings.HasSuffix(query, "/") {
				items = append(items, menuItem{label, action{Kind: kind, Value: current}})
			}
			parent := path.Dir(strings.TrimSuffix(current, "/"))
			if current != "/" {
				items = append(items, menuItem{"../ · Parent folder", action{Kind: "path-directory", Value: strings.TrimSuffix(parent, "/") + "/"}})
			}
		}
		for _, entry := range m.paths.result.Entries {
			items = append(items, menuItem{entry.Name + "/", action{Kind: "path-directory", Value: entry.Path}})
		}
		if m.paths.result.Truncated {
			items = append(items, menuItem{"More matches · type to narrow", action{Kind: "path-noop"}})
		}
		if len(m.paths.result.Entries) == 0 {
			items = append(items, menuItem{"No matching subfolders", action{Kind: "path-noop"}})
		}
	}
	if m.projectMode == "project-root" {
		items = append(items, menuItem{"Use home directory…", action{Kind: "path-project-root-home"}})
	}
	return append(items, menuItem{"Cancel", action{Kind: "project-cancel"}})
}

func (m *Model) saveProjectDirectory(value string) tea.Cmd {
	if m.settingsProjectID != "" || !m.hasCapability("path-completion") {
		return m.settingsUnavailable("path-completion")
	}
	if m.projectDirectoryConflicted {
		m.projectDirectoryRevision = m.snapshot.AppSettings.Revision
		m.projectDirectoryConflicted = false
	}
	next := m.snapshot.AppSettings
	if m.projectInput.Value() == "~/" || m.projectInput.Value() == "~" {
		value = "~"
	}
	next.ProjectDirectory = value
	return m.command(protocol.Command{Kind: "settings.update", Revision: m.projectDirectoryRevision, AppSettings: &next}, action{Kind: "path-project-root-save", Value: m.projectInput.Value()})
}

func (m *Model) mentionEntries() []protocol.PathEntry {
	key, _ := m.pathRequest()
	if key != m.paths.key || m.paths.loading || m.paths.err != "" {
		return nil
	}
	entries := m.paths.result.Entries
	if mention, ok := m.activeMention(); ok && strings.Contains(mention.query, "/") {
		dir := strings.TrimSuffix(m.paths.result.Directory, "/")
		if dir != "" && dir != "." && dir != m.paths.result.Root {
			parent := path.Dir(dir)
			if parent == "." {
				parent = ""
			} else {
				parent += "/"
			}
			entries = append(append([]protocol.PathEntry(nil), entries...), protocol.PathEntry{Name: "..", Path: parent, IsDir: true})
		}
	}
	return entries
}
func (m *Model) dismissMention() {
	if mention, ok := m.activeMention(); ok {
		m.mentionDismissed = mention.key
	}
}
func (m *Model) mentionKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	if _, ok := m.activeMention(); !ok {
		return false, nil
	}
	switch k.String() {
	case "esc":
		m.dismissMention()
		return true, nil
	case "down", "up", "shift+tab":
		n := len(m.mentionEntries())
		if n > 0 {
			delta := 1
			if k.String() != "down" {
				delta = -1
			}
			m.mentionIndex = (m.mentionIndex + delta + n) % n
		}
		return true, nil
	case "enter", "tab":
		return true, m.selectMention(m.mentionIndex)
	}
	return false, nil
}
func (m *Model) selectMention(index int) tea.Cmd {
	mention, ok := m.activeMention()
	entries := m.mentionEntries()
	if !ok || index < 0 || index >= len(entries) {
		return nil
	}
	entry := entries[index]
	if !entry.IsDir {
		if m.state.Edit != nil {
			return m.showNotice("Save or cancel the queued edit before attaching files")
		}
		duplicate := false
		for _, attachment := range m.viewState().Attachments {
			if attachment.Kind == "workspace-file" && attachment.Source == entry.Path {
				duplicate = true
			}
		}
		if !duplicate && len(m.viewState().Attachments) >= 8 {
			return m.showNotice("Attachment limit: 8")
		}
	}
	replacement := "@" + entry.Path
	if strings.ContainsAny(entry.Path, " \t") {
		replacement = "@\"" + entry.Path
		if !entry.IsDir {
			replacement += "\""
		}
	}
	if !entry.IsDir {
		replacement += " "
	}
	value := []rune(m.prompt.Value())
	next := string(value[:mention.start]) + replacement + string(value[mention.end:])
	if len([]rune(next)) > m.prompt.CharLimit {
		return m.showNotice("File reference exceeds the prompt limit")
	}
	m.prompt.SetValue(next)
	inputSetCursor(&m.prompt, mention.start+len([]rune(replacement)))
	m.promptView.Reset()
	if !entry.IsDir {
		duplicate := false
		for _, a := range m.viewState().Attachments {
			if a.Kind == "workspace-file" && a.Source == entry.Path {
				duplicate = true
			}
		}
		if !duplicate {
			m.viewState().Attachments = append(m.viewState().Attachments, protocol.Attachment{Kind: "workspace-file", Name: entry.Path, Source: entry.Path})
		}
	}
	m.viewState().Draft = m.prompt.Value()
	m.markDirty()
	m.configureInputs()
	return tea.Batch(m.setFocus("prompt"), m.nextPathQuery())
}

func (m *Model) mentionRect(f frame) shell.Rect {
	if _, ok := m.activeMention(); !ok || f.prompt.W == 0 {
		return shell.Rect{}
	}
	n := max(1, min(6, len(m.mentionEntries())))
	h := min(n+4, max(0, f.prompt.Y-1))
	return shell.Rect{X: f.prompt.X - 1, Y: f.prompt.Y - 1 - h, W: f.prompt.W + 2, H: h}
}
func (m *Model) renderMentions(f *frame) {
	r := m.mentionRect(*f)
	if r.H < 4 {
		return
	}
	p := m.colors()
	// Covered workspace controls must not remain clickable through the popup.
	filtered := f.hits[:0]
	for _, h := range f.hits {
		if h.Rect.X < r.X+r.W && h.Rect.X+h.Rect.W > r.X && h.Rect.Y < r.Y+r.H && h.Rect.Y+h.Rect.H > r.Y {
			continue
		}
		filtered = append(filtered, h)
	}
	f.hits = filtered
	// The whole popup, including padding, consumes pointer activation.
	f.hits = append(f.hits, hit{r, action{Kind: "mention-background"}, "Project files", "mention-background"})
	f.componentBox(m, r, roundedOutline, m.componentStyle(roundedOutline, componentState{Focused: true}, p.text, p.input), p.canvas)
	f.text(r.X+1, r.Y+1, r.W-5, "Project files · @", p.violet, p.input)
	f.button(m, r.X+r.W-4, r.Y+1, 3, centered(m.icon("close"), 3), "mention-dismiss", action{Kind: "mention-dismiss"}, p.muted, p.input)
	entries := m.mentionEntries()
	visible := r.H - 4
	start := max(0, min(m.mentionIndex-visible+1, len(entries)-visible))
	if len(entries) == 0 {
		text := "No matching files or folders"
		key, _ := m.pathRequest()
		if m.paths.loading || key != m.paths.key {
			text = "Looking for files…"
		} else if m.paths.err != "" {
			text = m.paths.err
		}
		f.text(r.X+1, r.Y+2, r.W-2, text, p.muted, p.input)
	}
	for i := 0; i < visible && start+i < len(entries); i++ {
		index := start + i
		entry := entries[index]
		label := entry.Name
		if entry.IsDir {
			label += "/"
		}
		key := fmt.Sprintf("mention:%d", index)
		state := m.controlState(index == m.mentionIndex, key)
		state.Focused = index == m.mentionIndex
		f.styledButton(r.X+1, r.Y+2+i, r.W-2, label, key, action{Kind: "mention-select", Index: index}, m.componentStyle(squareFill, state, p.text, p.input))
	}
	help := "↑ ↓  Tab/Enter choose · Esc close"
	if m.paths.result.Truncated {
		help = "More matches · type to narrow"
	}
	f.text(r.X+1, r.Y+r.H-2, r.W-2, help, p.muted, p.input)
}
