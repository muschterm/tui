// Package tui binds the reusable shell to the fixture ADE and server commands.
// All I/O happens in commands or Run, never in View.
package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

type threadView struct {
	Agent         string
	CompactColumn shell.Region
	Host          shell.Host
	RightVisible  bool
	Draft         string
	ContextError  string
	Settings      protocol.Settings
	Attachments   []protocol.Attachment
	// DraftID identifies one New-thread draft instance; it changes when that
	// draft is sent, so late results never attach to a later draft.
	DraftID                                                          string `json:",omitempty"`
	Scroll, DetailScroll, RequestIndex, QuestionIndex, RequestScroll int
	BottomScroll                                                     int
	DetailID                                                         string
	// Bottom holds this thread's center-bottom terminal tabs. BottomID is the
	// pre-tab single session, read once to migrate saved views.
	Bottom                         shell.Host
	BottomID                       string `json:",omitempty"`
	RequestID                      string
	DismissedAgents, DismissedPlan string
	Answers                        map[string][]string
	QuestionDrafts                 map[string][]answerDraft
	QuestionHistoryExpanded        map[string]bool `json:"QuestionHistoryExpanded,omitempty"`
	// Pinned follows the transcript end; SeenActivity counts the conversation
	// messages (user/agent rows) already shown when the user scrolled away.
	Pinned       bool `json:",omitempty"`
	SeenActivity int  `json:",omitempty"`
	// Workspace and WorktreeBranch are a New-thread draft's explicit
	// workspace choice ("" follows the project's effective default) and the
	// new worktree's branch name (worktrees.go).
	Workspace      string `json:",omitempty"`
	WorktreeBranch string `json:",omitempty"`
	// UsedBranches are branches earlier failed or abandoned starts may have
	// left behind; Send refuses to reuse them silently.
	UsedBranches []string `json:",omitempty"`
	// PendingStart is a worktree thread.start still being followed under
	// its command identity (worktrees.go).
	PendingStart *pendingStart `json:",omitempty"`
}

type savedView struct {
	DraftProjectID                         string
	DraftThreads                           map[string]*threadView
	StartedDraft                           *startedDraft
	Generation                             int64
	Layout                                 shell.State
	Active                                 string
	ProjectFilter                          string
	ThreadFilter                           string
	Light, RecentsHidden, RecentsCollapsed bool
	// Icons is this client's saved symbol set: "nerd", "ascii", or empty to
	// follow the TUI_GO_ICONS environment default.
	Icons         string `json:",omitempty"`
	Threads       map[string]*threadView
	Edit          *editState
	Pending       *protocol.Command
	PendingAction action
}

type action struct {
	Kind, ID, Value  string
	ApprovalChoiceID string
	Index            int
	Revision         int64
}

type menuItem struct {
	Label  string
	Action action
	// PairLabel and PairValue opt a settings or usage fact into a label/value
	// pair row. Free-form labels are never split, since user text such as a
	// thread title may contain ": ".
	PairLabel, PairValue string
	// Separator is a non-selectable rule row: it has no hit rectangle and
	// keyboard and wheel navigation skip it.
	Separator bool
	// Note is a non-selectable muted informational row: it has no hit
	// rectangle, is skipped by keyboard/wheel navigation, and does not count
	// toward the n/N position shown in the menu footer.
	Note string
}

// pairMenuItem is a label/value fact row; Label keeps "Label: value" for
// hit help and the plain one-line fallback.
func pairMenuItem(label, value string, a action) menuItem {
	return menuItem{Label: label + ": " + value, Action: a, PairLabel: label, PairValue: value}
}

type hit struct {
	Rect       shell.Rect
	Action     action
	Label, Key string
	// Slot is the reserved cells an icon control is centered in; Rect is only
	// the interactive glyph. Layout checks use the slot, input uses Rect.
	Slot shell.Rect
}

func (h hit) slot() shell.Rect {
	if h.Slot.W > 0 {
		return h.Slot
	}
	return h.Rect
}

type frame struct {
	rows                                        []string
	hits                                        []hit
	geom                                        shell.Geometry
	scrollbars                                  map[string]scrollTarget
	transcriptMax, detailMax, requestMax        int
	bottomMax, navMax                           int
	closedMax, settingsMax                      int
	prompt, answer, transcript, detail, request shell.Rect
	bottomBody, navigation                      shell.Rect
	closedNavigation, settingsBody              shell.Rect
	// viewerBody is the attachment viewer's selectable text area.
	viewerBody shell.Rect
	viewerMax  int
	// wrapRows marks, by absolute screen row, a raw-mode viewer row that is a
	// soft-wrap continuation of the same source line as the row below it: the
	// shared selection joins such a pair without a newline and keeps the
	// wrapped row's trailing spaces instead of trimming them.
	wrapRows map[int]bool
	// composer is the prompt outline, including any attachment strip.
	composer shell.Rect
	// terms are the embedded terminal grids laid out in this frame.
	terms []termPane
	// Files surface tree and buffer text areas and their scroll limits.
	filesTree, filesText                      shell.Rect
	filesTreeMax, filesTextMax, filesTextHMax int
	// docText is an editable document's text cells (editor_view.go).
	docText shell.Rect
}

type snapshotMsg protocol.Snapshot
type connectionMsg struct {
	client *client.Client
	err    error
}

type commandMsg struct {
	command protocol.Command
	receipt protocol.Receipt
	err     error
	local   action
	// saveFailed: the pre-dispatch view save failed, so this attempt never left.
	saveFailed, retry bool
}

type saveMsg struct {
	err        error
	generation int
}

type saveTick struct{}
type editState struct {
	ID, ThreadID, OldDraft string
	OldSettings            protocol.Settings
	Revision               int64
}

// Model is the Bubble Tea model for one attached client: the server snapshot,
// client-local view state, focus, input widgets and pending effects.
type Model struct {
	paths                      pathCompletion
	mentionDismissed           string
	mentionIndex               int
	projectAddThread           bool
	pendingProjectDraft        bool
	projectDirectoryRevision   int64
	projectDirectoryConflicted bool
	checkoutKey                string
	checkoutInfo               protocol.WorkspaceInfo
	checkoutLoading            bool
	// Worktree flows (worktrees.go).
	draftStart                           draftStart
	draftStartSeq                        uint64
	draftGen                             uint64
	worktreeReads                        worktreeAPI
	worktreeSeq                          uint64
	pruneKey                             string
	startAttempts                        map[string]int
	startInFlight                        map[string]bool
	prunePreview                         map[string]protocol.WorktreePrune
	emptyView                            threadView
	projectMode                          string
	projectError                         string
	requestFeedback                      map[string]requestFeedback
	projectInput                         textarea.Model
	threadSearch                         textarea.Model
	projectGear                          bool
	projectEditRevision                  int64
	projectRenameConflicted              bool
	settingsPage, settingsProjectID      string
	settingsNavigation                   bool
	settingsReturnFocus                  string
	closedScroll, settingsScroll         int
	pendingThreadSelection               string
	pendingProjectSelection              string
	state                                savedView
	snapshot                             protocol.Snapshot
	client                               *client.Client
	ctx                                  context.Context
	clientID                             string
	width, height                        int
	sizeKnown                            bool
	plainIcons                           bool
	envIcons                             string // TUI_GO_ICONS at startup; the default when no symbol set is saved
	colorProfile                         colorprofile.Profile
	colorProbe                           terminalColorProbe
	colorReply                           terminalReplyFragments
	inputLight                           bool
	prompt, answer                       textarea.Model
	promptRows                           int
	promptMetrics, answerMetrics         inputScroll
	promptLayoutValue, answerLayoutValue string
	promptLayoutWidth, answerLayoutWidth int
	promptView, answerView               inputPresentation
	scrollDrag                           string
	scrollGrab                           int
	menuOffset, navScroll                int
	focus, hover                         string
	menu                                 []menuItem
	menuTitle                            string
	// menuTitleUser is the user-supplied tail of menuTitle (a thread or
	// project name) that keeps its case; only the static prefix is uppercased.
	menuTitleUser string
	menuIndex     int
	contextMenu   *contextMenuState
	// viewer is the open read-only attachment viewer, if any.
	viewer                       *attachmentViewer
	status                       string
	notice                       transientNotice
	connected                    bool
	busy                         *protocol.Command
	busyAction                   action
	inFlight                     bool
	writer                       *viewWriter
	dirty, saving                bool
	generation                   int
	drag                         shell.Divider
	lastX, lastY                 int
	selecting                    bool
	selectionStart, selectionEnd [2]int
	selectedText                 string
	selectionRegion              shell.Rect
	selectionBasis               selectionBasis
	clipboardGeneration          uint64
	// clipboardWrite is injectable for tests; production leaves it nil so the
	// pinned OS clipboard adapter is used by clipboardCommand.
	clipboardWrite          func(string) error
	clipboardRead           func() (string, error)
	clipboardReadGeneration uint64
	// clipboardSource and artifacts are injectable for tests; production
	// uses wl-paste (when available) and the server client.
	clipboardSource clipboardSource
	artifacts       artifactAPI
	intakes         map[uint64]draftOwner
	intakeSeq       uint64
	sendCapture     *sendCapture
	captureSeq      uint64
	viewerSeq       uint64
	// Read-only Git surface (git_surface.go): per-target observations, the
	// read generation, the target last shown and the last observed turn.
	gitReads      gitAPI
	gitViews      map[string]*gitView
	gitSeq        uint64
	gitShown      string
	gitTurnKey    string
	gitTurnActive bool
	gitW          gitWriteUI
	gitR          gitRefUI
	gitO          gitOpUI
	gitCF         gitConflictUI
	gitJ          gitJobUI
	// jobReq is a resolution job thread whose pending requests the request
	// cards show (git_job.go); the active thread is unchanged.
	jobReq string
	// Read-only Files surface (files_surface.go): per-target views, the read
	// generation, the target last shown and whether the disk poll is ticking.
	filesReads   filesAPI
	filesViews   map[string]*filesView
	filesSeq     uint64
	filesShown   string
	filesTicking bool
	// Kitty graphics (graphics_model.go): per-connection capability, owned
	// image ids and the composer's thumbnail loads.
	graphics            graphicsProbe
	images              *graphicsRegistry
	thumbs              map[string]*thumbnail
	thumbCache          thumbCache
	graphicsSeq         uint64
	keyboard            string
	activityPhase       int
	activityTickPending bool
	// Embedded terminals (terminal_session.go): per-terminal stream and
	// screen state, the stream generation counter, the terminal receiving
	// keys (empty when none) and an injectable dialer for tests.
	terms     map[string]*termView
	termSeq   uint64
	termFocus string
	termDial  terminalDialer
	// Shared documents (editor_session.go): sessions by document ID, the
	// stream/read generation counter, the session in edit mode, the open
	// conflict review, the editor's clipboard read generation and
	// injectable API and dialer for tests.
	docs        map[string]*docSession
	docSeq      uint64
	docEdit     string
	docReview   *docReview
	docPasteGen uint64
	// docOrphans are kept copies whose file view or session is gone.
	docOrphans []docLost
	docAPIs    documentAPI
	docDial    docDialer
}

func newInput(placeholder string) textarea.Model {
	a := textarea.New()
	a.Placeholder = placeholder
	a.Prompt = ""
	a.ShowLineNumbers = false
	a.CharLimit = 8000
	a.MaxHeight = 0
	a.MaxWidth = 0
	a.KeyMap.Paste.SetEnabled(false)
	a.SetHeight(2)
	a.SetWidth(50)
	a.SetVirtualCursor(true)
	return a
}

// New builds a model for client id from the current snapshot and that
// client's stored view document.
func New(c *client.Client, id string, snapshot protocol.Snapshot, data []byte) *Model {
	m := &Model{client: c, clientID: id, snapshot: snapshot, ctx: context.Background(), width: 120, height: 40, connected: true, colorProfile: colorprofile.TrueColor, focus: "prompt", keyboard: "legacy keyboard", prompt: newInput(promptPlaceholder), answer: newInput("Type an answer…")}
	m.state = savedView{Layout: shell.NewState(), Threads: map[string]*threadView{}, RecentsCollapsed: true}
	m.images = newGraphicsRegistry(os.Getpid() ^ time.Now().Nanosecond())
	m.projectInput = newInput("Project name or path…")
	m.threadSearch = newInput("Search")
	m.threadSearch.SetHeight(1)
	m.threadSearch.CharLimit = 256
	if len(data) > 0 {
		_ = json.Unmarshal(data, &m.state)
	}
	if m.state.Threads == nil {
		m.state.Threads = map[string]*threadView{}
	}
	if m.state.DraftThreads == nil {
		m.state.DraftThreads = map[string]*threadView{}
	}
	m.threadSearch.SetValue(m.state.ThreadFilter)
	if !m.creatingThread() && !slices.ContainsFunc(snapshot.Threads, func(t protocol.Thread) bool { return t.ID == m.state.Active && !jobThread(t) }) && len(snapshot.Threads) > 0 {
		m.state.Active = m.jobFallbackThread(m.state.Active)
	}
	for _, t := range snapshot.Threads {
		v := m.state.Threads[t.ID]
		if v == nil {
			m.state.Threads[t.ID] = &threadView{Settings: t.Selected, Answers: map[string][]string{}, Pinned: true}
			continue
		}
		// A view saved before follow-the-end existed restores its offset
		// unpinned with no seen count; its current messages count as read so
		// the jump control does not announce the whole thread as new.
		if !v.Pinned && v.SeenActivity == 0 {
			v.SeenActivity = messageCount(t)
		}
	}
	m.migrateQuestionDrafts()
	// Saved with the next real change; loading alone does not dirty the view.
	m.pruneQuestionDrafts()
	m.migrateBottomSessions()
	m.applyIcons()
	m.busy = m.state.Pending
	m.busyAction = m.state.PendingAction
	m.reconcileProjects()
	m.reconcileThreadMembership()
	m.loadDraft()
	m.configureInputs()
	return m
}

func (m *Model) thread() protocol.Thread {
	if m.creatingThread() {
		p, _ := m.projectByID(m.state.DraftProjectID)
		v := m.viewState()
		return protocol.Thread{ProjectID: m.state.DraftProjectID, Project: p.Name, Checkout: p.Path, Title: "New thread", Agent: v.Agent, State: "idle", Selected: v.Settings, Effective: v.Settings}
	}
	for _, t := range m.snapshot.Threads {
		if t.ID == m.state.Active {
			return t
		}
	}
	return protocol.Thread{}
}

func (m *Model) viewState() *threadView {
	if m.creatingThread() {
		if m.state.DraftThreads[m.state.DraftProjectID] == nil {
			m.state.DraftThreads[m.state.DraftProjectID] = m.newDraftView()
		}
		return m.state.DraftThreads[m.state.DraftProjectID]
	}
	if m.state.Active == "" {
		return &m.emptyView
	}
	v := m.state.Threads[m.state.Active]
	if v == nil {
		v = &threadView{Settings: m.thread().Selected, Answers: map[string][]string{}, Pinned: true}
		m.state.Threads[m.state.Active] = v
	}
	if v.Answers == nil {
		v.Answers = map[string][]string{}
	}
	return v
}

func (m *Model) loadDraft() { v := m.viewState(); m.prompt.SetValue(v.Draft); m.loadAnswer() }

func (m *Model) markDirty() { m.dirty = true; m.generation++; m.state.Generation++ }

// Init focuses the composer and starts the activity, checkout and save tickers.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.prompt.Focus(), m.nextActivityTick(), m.nextCheckoutInspection(), m.nextDraftStartInspection(), m.resumePendingStarts(), tea.Tick(time.Second, func(time.Time) tea.Msg { return saveTick{} }))
}

func (m *Model) requests() []protocol.Request {
	var r []protocol.Request
	for _, q := range m.requestThread().Requests {
		if q.State == "pending" {
			r = append(r, q)
		}
	}
	return r
}

func (m *Model) request() (protocol.Request, bool) {
	r := m.requests()
	if len(r) == 0 {
		return protocol.Request{}, false
	}
	return r[m.requestIndex(r)], true
}

// Selection follows request identity. RequestIndex remains the saved-view
// fallback and the neighbor position after the selected request disappears.
func (m *Model) requestIndex(pending []protocol.Request) int {
	v := m.viewState()
	if len(pending) == 0 {
		return 0
	}
	if v.RequestID != "" {
		if i := slices.IndexFunc(pending, func(r protocol.Request) bool { return r.ID == v.RequestID }); i >= 0 {
			return i
		}
	}
	return max(0, v.RequestIndex) % len(pending)
}

func (m *Model) pinRequest() {
	v, pending := m.viewState(), m.requests()
	id, i := "", 0
	if len(pending) > 0 {
		i = m.requestIndex(pending)
		id = pending[i].ID
	}
	if v != &m.emptyView {
		// Derived from the snapshot; persisted with the next real change.
		v.RequestID, v.RequestIndex = id, i
	}
}

func (m *Model) selectRequest(i int) {
	pending := m.requests()
	if len(pending) == 0 {
		return
	}
	v := m.viewState()
	v.RequestIndex = (i%len(pending) + len(pending)) % len(pending)
	v.RequestID = pending[v.RequestIndex].ID
	v.QuestionIndex, v.RequestScroll = 0, 0
	m.loadAnswer()
}

// requestBound reports whether a menu action still targets the pending request
// and revision it was built for. Unbound actions target the visible card.
func (m *Model) requestBound(a action) bool {
	if a.ID == "" {
		return true
	}
	r, ok := m.request()
	return ok && r.ID == a.ID && r.Revision == a.Revision
}

func requestScoped(a action) bool {
	return a.ID != "" && slices.Contains([]string{"approve", "answer-choice", "answer-other", "answer-action", "question-index"}, a.Kind)
}

// reconcileRequests runs after each snapshot. A vanished selection moves to a
// neighbor, but never with focus left in an answer control.
func (m *Model) reconcileRequests() tea.Cmd {
	var cmd tea.Cmd
	v, pending := m.viewState(), m.requests()
	if v.RequestID != "" && !slices.ContainsFunc(pending, func(r protocol.Request) bool { return r.ID == v.RequestID }) {
		v.RequestID = ""
		v.RequestIndex = max(0, min(v.RequestIndex, len(pending)-1))
		m.markDirty()
		if m.focus == "answer" || m.focus == "answer-other" || m.focus == "answer-submit" || m.focus == "answer-decline" || m.focus == "answer-cancel" || m.focus == "answer-actions" || strings.HasPrefix(m.focus, "option:") || strings.HasPrefix(m.focus, "approve:") {
			m.setFocus("request-body")
		}
		if len(pending) > 0 {
			cmd = m.showNotice("Request resolved elsewhere · showing another pending request")
		}
	}
	m.pinRequest()
	if len(m.menu) > 0 && m.projectMode == "" && slices.ContainsFunc(m.menu, func(item menuItem) bool { return requestScoped(item.Action) && !m.requestBound(item.Action) }) {
		m.menu = nil
		cmd = m.showNotice("Request changed · menu closed, nothing sent")
	}
	return cmd
}

func (m *Model) configureInputs() {
	m.prompt.Placeholder = promptPlaceholder
	p := m.colors()
	s := textarea.Styles{}
	s.Focused = textarea.StyleState{Base: style(p.text, p.input), Text: style(p.text, p.input), Placeholder: style(p.muted, p.input), CursorLine: style(p.text, p.input), Selection: style(p.text, p.selected)}
	s.Blurred = s.Focused
	s.Cursor.Color = lipColor(p.blue)
	m.prompt.SetStyles(s)
	m.answer.SetStyles(s)
	m.projectInput.SetStyles(s)
	m.threadSearch.SetStyles(s)
	m.threadSearch.SetWidth(max(1, threadSearchRect(m.workspaceGeometry(m.footerHeight()).Left).W))
	m.projectInput.SetWidth(max(1, min(68, max(20, m.width-6))-4))
	if m.settingsPage != "" {
		m.configureSettingsFocus(m.measure())
		return
	}
	m.promptRows = minPromptRows
	f := m.measure()
	promptWidth, answerWidth := max(1, f.prompt.W), max(1, f.answer.W)
	// Bubbles' empty-placeholder rows style only the end-of-buffer glyph.
	// Fill that row before its viewport adds unstyled horizontal padding.
	s.Focused.EndOfBuffer = style(p.text, p.input).Width(promptWidth)
	s.Blurred.EndOfBuffer = s.Focused.EndOfBuffer
	m.prompt.SetStyles(s)
	promptValue, answerValue := m.prompt.Value(), m.answer.Value()
	promptChanged := promptValue != m.promptLayoutValue || promptWidth != m.promptLayoutWidth || m.promptMetrics.Total == 0
	answerChanged := answerValue != m.answerLayoutValue || answerWidth != m.answerLayoutWidth || m.answerMetrics.Total == 0
	if promptChanged {
		m.promptMetrics.Total = inputRows(promptValue, promptWidth)
		m.promptLayoutValue, m.promptLayoutWidth = promptValue, promptWidth
	}
	if answerChanged {
		m.answerMetrics.Total = inputRows(answerValue, answerWidth)
		m.answerLayoutValue, m.answerLayoutWidth = answerValue, answerWidth
	}
	// Reserve app chrome, the fixed footer and at least one transcript row.
	limit := max(1, min(maxPromptRows, m.height-3-(m.footerHeight()-m.promptRows)))
	if !m.conversationVisible() {
		limit = min(3, limit)
	}
	m.promptRows = min(limit, max(minPromptRows, m.promptMetrics.Total))
	f = m.measure()
	if m.sizeKnown {
		m.clampScroll(f)
	}
	if promptChanged || m.prompt.Height() != max(1, f.prompt.H) {
		resizeInput(&m.prompt, promptWidth, max(1, f.prompt.H))
		m.promptView.Reset()
	}
	if answerChanged || m.answer.Height() != max(1, f.answer.H) {
		resizeInput(&m.answer, answerWidth, max(1, f.answer.H))
		m.answerView.Reset()
	}
	m.promptMetrics = inputViewportMetrics(m.prompt, m.promptMetrics.Total)
	m.answerMetrics = inputViewportMetrics(m.answer, m.answerMetrics.Total)
	if m.inputLight != m.state.Light {
		m.promptView.Refresh(&m.prompt, m.promptMetrics.Total)
		m.answerView.Refresh(&m.answer, m.answerMetrics.Total)
		m.inputLight = m.state.Light
	}
	if len(m.menu) == 0 && m.focus != "prompt" && m.focus != "answer" && !slices.ContainsFunc(f.hits, func(h hit) bool { return h.Key == m.focus }) {
		next := "prompt"
		_, hidden := m.composerLayout(max(1, f.geom.Center.W-2))
		if slices.ContainsFunc(hidden, func(c composerControl) bool { return c.key == m.focus }) && slices.ContainsFunc(f.hits, func(h hit) bool { return h.Key == "composer-more" }) {
			next = "composer-more"
		}
		m.setFocus(next)
	}
	if m.focus == "answer" && f.answer.W == 0 {
		m.setFocus("request-body")
	}
	if m.focus == "prompt" && !m.hasComposer() {
		m.setFocus("prompt")
	}
}

func (m *Model) setFocus(key string) tea.Cmd {
	if m.settingsPage != "" && (key == "prompt" || key == "answer" || key == "thread-search") {
		key = "sidebar-settings"
	}
	if key == "prompt" && !m.hasComposer() {
		key = "transcript"
		if m.state.Active == "" {
			key = "empty-new"
		}
	}
	m.focus = key
	m.prompt.Blur()
	m.answer.Blur()
	m.projectInput.Blur()
	m.threadSearch.Blur()
	if m.gitW.ready {
		m.gitW.message.Blur()
	}
	if m.gitR.ready {
		m.gitR.name.Blur()
	}
	if m.gitJ.ready {
		m.gitJ.input.Blur()
	}
	if key == gitBranchNameKey {
		return m.gitBranchInput().Focus()
	}
	if key == gitJobInputKey {
		return m.gitJobInput().Focus()
	}
	if key == gitMessageKey {
		return m.gitMsg().Focus()
	}
	if key == "prompt" {
		return m.prompt.Focus()
	}
	if key == "answer" {
		return m.answer.Focus()
	}
	if key == "project-input" {
		return m.projectInput.Focus()
	}
	if key == "thread-search" {
		return m.threadSearch.Focus()
	}
	return nil
}

func identity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (m *Model) command(c protocol.Command, a action) tea.Cmd {
	capability := "fixture-agent"
	if strings.HasPrefix(c.Kind, "terminal.") {
		capability = terminalCapability
	}
	if strings.HasPrefix(c.Kind, "queue.") || c.Kind == "prompt.send" {
		capability = "prompt-queue"
	}
	if c.Kind == "queue.steer" {
		capability = "fixture-steering"
	}
	if c.Kind == "thread.close" || c.Kind == "thread.reopen" || c.Kind == "thread.delete" {
		capability = "thread-lifecycle"
	}
	if strings.HasPrefix(c.Kind, "project.") || c.Kind == "thread.create" {
		capability = "project-management"
	}
	if c.Kind == "project.update" || c.Kind == "project.remove" {
		capability = "project-settings"
	}
	if c.Kind == "settings.update" {
		capability = "app-settings"
	}
	if c.Kind == "thread.start" {
		capability = "thread-start"
	}
	if c.Kind == "prompt.reopen-send" {
		capability = "closed-thread-send"
	}
	if c.Kind == "agent.probe" {
		capability = "agent-probe"
	}
	if strings.HasPrefix(c.Kind, "worktree.") {
		capability = "worktree-manage"
	}
	if !slices.Contains(m.snapshot.Capabilities, capability) {
		m.status = "Server capability unavailable: " + capability
		return nil
	}

	if m.busy != nil {
		m.status = "A command is pending. Use Retry to reconcile it."
		return nil
	}
	c.Version = protocol.Version
	c.ID = identity()
	global := capability == "project-management" || capability == "project-settings" || capability == "app-settings" || capability == "thread-start" || capability == "agent-probe" || capability == "worktree-manage"
	if c.ThreadID == "" && !global {
		c.ThreadID = m.state.Active
	}
	if c.ThreadID == "" && !global {
		m.status = "Select or create a thread first"
		return nil
	}
	c.ClientID = m.clientID
	m.busy = &c
	m.busyAction = a
	if c.Kind == "thread.start" {
		if v := m.state.DraftThreads[c.ProjectID]; v != nil {
			v.DraftID = identity()
		}
	}
	m.state.Pending = &c
	m.state.PendingAction = a
	m.markDirty()
	m.configureInputs()
	return m.dispatch(c, a, false)
}

// A first dispatch whose view save fails never sends the command. A Retry
// keeps the uncertain identity regardless of how that attempt fails.
func (m *Model) dispatch(c protocol.Command, a action, retry bool) tea.Cmd {
	m.inFlight = true
	connection := m.client
	ctx := m.ctx
	data, _ := json.Marshal(m.state)
	writer := m.writer
	return func() tea.Msg {
		if writer != nil {
			if err := writer.save(data); err != nil {
				return commandMsg{command: c, err: err, local: a, saveFailed: true, retry: retry}
			}
		}
		deadline, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		r, e := connection.Command(deadline, c)
		return commandMsg{command: c, receipt: r, err: e, local: a, retry: retry}
	}
}

func (m *Model) save() tea.Cmd {
	if !m.dirty || m.saving || !m.connected {
		return nil
	}
	m.viewState().Draft = m.prompt.Value()
	data, err := json.Marshal(m.state)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.saving = true
	gen := m.generation
	writer := m.writer
	return func() tea.Msg {
		if writer == nil {
			return saveMsg{generation: gen}
		}
		return saveMsg{writer.save(data), gen}
	}
}

// Update routes every message through the model's explicit state transitions.
// Graphics replies are consumed first; afterwards the composer's thumbnail
// loads follow whatever the message changed.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.graphicsUpdate(msg); handled {
		return m, tea.Batch(cmd, m.syncThumbnails())
	}
	if cmd, handled := m.acceptTerminalMsg(msg); handled {
		return m, tea.Batch(cmd, m.syncTerminals())
	}
	if cmd, handled := m.acceptDocMsg(msg); handled {
		return m, tea.Batch(cmd, m.syncDocuments())
	}
	_, cmd := m.update(msg)
	return m, tea.Batch(cmd, m.syncThumbnails(), m.syncTerminals(), m.syncDocuments())
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if cmd, handled := m.acceptWorktreeMsg(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case pathQueryReady:
		cmd = m.runPathQuery(msg)
	case pathQueryResult:
		m.acceptPathQuery(msg)
	case checkoutMsg:
		key, _, _ := m.checkoutTarget()
		if msg.key == m.checkoutKey && msg.key == key {
			m.checkoutLoading = false
			m.checkoutInfo = msg.info
			if msg.err != nil {
				m.checkoutInfo = protocol.WorkspaceInfo{Path: m.thread().Checkout, Kind: "unavailable", State: "unavailable", Error: safe(msg.err.Error())}
			}
		}
	case noticeExpired:
		if m.notice.generation == uint64(msg) {
			m.notice.text = ""
		}
		return m, nil
	case clipboardWriteMsg:
		cmd = m.acceptClipboardWrite(msg)
	case clipboardReadMsg:
		cmd = m.acceptClipboardRead(msg)
	case clipboardImageMsg:
		cmd = m.acceptClipboardImage(msg)
	case clipboardFilesMsg:
		cmd = m.acceptClipboardFiles(msg)
	case sendCaptureMsg:
		cmd = m.acceptSendCapture(msg)
	case viewerLoadMsg:
		cmd = m.acceptViewerLoad(msg)
	case filesListMsg:
		m.acceptFilesList(msg)
	case filesReadMsg:
		cmd = m.acceptFilesRead(msg)
	case filesStatMsg:
		m.acceptFilesStat(msg)
	case filesTickMsg:
		cmd = m.filesTick()
	case gitStatusMsg:
		m.acceptGitStatus(msg)
	case gitLogMsg:
		m.acceptGitLog(msg)
	case gitViewerMsg:
		m.acceptGitViewer(msg)
	case gitWriteMsg:
		cmd = m.acceptGitWrite(msg)
	case gitHeadMsg:
		cmd = m.acceptGitHead(msg)
	case gitCancelMsg:
		cmd = m.acceptGitCancel(msg)
	case gitOperationMsg:
		cmd = m.acceptGitOperation(msg)
	case gitConflictFileMsg:
		cmd = m.acceptConflictFile(msg)
	case gitReviewMsg:
		cmd = m.acceptGitReview(msg)
	case gitPreviewMsg:
		cmd = m.acceptGitPreview(msg)
	case viewerImageMsg:
		cmd = m.acceptViewerImage(msg)
	case thumbnailMsg:
		cmd = m.acceptThumbnail(msg)
	case tea.ResumeMsg:
		return m, m.reshowGraphics()
	case terminalReplyStarted:
		return m, tea.Tick(terminalReplyWindow, func(time.Time) tea.Msg { return terminalReplyExpired(msg) })
	case terminalReplyReplay:
		var commands []tea.Cmd
		for _, original := range msg {
			_, command := m.Update(original)
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	case tea.ColorProfileMsg:
		return m, m.updateColorProfile(msg.Profile)
	case tea.TerminalVersionMsg:
		return m, tea.Batch(m.probeTerminalColors(msg.Name), m.graphics.Start(msg.Name, m.colorProfile))
	case tea.CapabilityMsg:
		if m.colorProbe.capabilitiesRequested && (msg.Content == "RGB" || msg.Content == "Tc") {
			m.colorProbe.confirmed = true
		}
		return m, nil
	case activityTick:
		m.activityTickPending = false
		if m.activityAnimating() {
			m.activityPhase = (m.activityPhase + 1) % 12
		}
		return m, m.nextActivityTick()
	case tea.WindowSizeMsg:
		m.sizeKnown = true
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.configureInputs()
		cmd = m.resizeGraphics()
	case tea.KeyboardEnhancementsMsg:
		m.keyboard = "enhanced keyboard negotiated"
	case connectionMsg:
		m.paths.key = ""
		m.connected = msg.err == nil
		if msg.client != nil {
			m.client = msg.client
			if m.writer != nil {
				m.writer.connection.Store(msg.client)
			}
		}
		if msg.err != nil {
			m.status = "Disconnected · reconnecting; commands and drafts retained"
		} else {
			m.status = "Connected"
		}
		m.configureInputs()
	case snapshotMsg:
		oldRequest, _ := m.request()
		oldPage := m.viewState().QuestionIndex
		s := protocol.Snapshot(msg)
		if s.InstanceID != m.snapshot.InstanceID || s.Revision >= m.snapshot.Revision {
			if s.InstanceID != m.snapshot.InstanceID || !slices.Equal(s.Capabilities, m.snapshot.Capabilities) {
				m.paths.key = ""
			}
			m.reconcileActivityDismissals(s)
			m.snapshot = s
			m.reconcileProjects()
			m.reconcileThreadMembership()
		}
		cmd = tea.Batch(m.reconcileRequests(), m.syncDocSnapshot())
		if m.pruneQuestionDrafts() {
			m.markDirty()
		}
		newRequest, _ := m.request()
		if questionDraftKey(oldRequest) != questionDraftKey(newRequest) || oldPage >= len(newRequest.Questions) {
			m.viewState().QuestionIndex = 0
			m.viewState().RequestScroll = 0
			m.loadAnswer()
			if oldRequest.ID != "" && oldRequest.ID == newRequest.ID {
				m.status = "Question updated · previous draft retained in client storage"
			}
		}
		m.configureInputs()
	case commandMsg:
		if m.busy == nil || m.busy.ID != msg.command.ID {
			return m, nil
		}
		m.inFlight = false
		if isWorktreeStart(msg.command) && !(msg.err != nil && msg.saveFailed && !msg.retry) {
			// A worktree start never holds the global pending command while
			// Git runs; its draft follows it instead (worktrees.go).
			m.busy, m.state.Pending = nil, nil
			m.busyAction, m.state.PendingAction = action{}, action{}
			return m, m.acceptWorktreeStart(msg.command, msg.receipt, msg.err)
		}
		if msg.err != nil && msg.saveFailed && !msg.retry {
			m.busy, m.state.Pending = nil, nil
			m.busyAction, m.state.PendingAction = action{}, action{}
			m.status = "Not sent · " + safe(msg.err.Error())
			if msg.command.Kind == "request.answer" {
				m.setRequestFeedback(msg.command.ThreadID, msg.command.TargetID, msg.command.Revision, m.status, false)
			}
			m.markDirty()
			m.configureInputs()
			return m, m.showNoticeAs(noticeError, m.status)
		}
		if msg.err != nil {
			m.status = safe(msg.err.Error())
			for _, attachment := range msg.command.Attachments {
				if attachment.Kind == "workspace-file" {
					v := m.state.Threads[msg.command.ThreadID]
					if msg.command.Kind == "thread.start" {
						v = m.state.DraftThreads[msg.command.ProjectID]
					}
					if v != nil {
						v.ContextError = m.status
						m.markDirty()
					}
					break
				}
			}
			if msg.command.Kind == "request.answer" {
				m.setRequestFeedback(msg.command.ThreadID, msg.command.TargetID, msg.command.Revision, m.status, false)
			}
			if msg.command.Kind == "project.add" && m.projectMode == "add" || msg.command.Kind == "project.update" && m.projectMode == "rename" || msg.command.Kind == "settings.update" && m.projectMode == "project-root" {
				m.projectError = m.status
			}
			var err *protocol.Error
			// A failed Retry save wraps a view error; the command stays uncertain.
			rejected := errors.As(msg.err, &err) && !msg.saveFailed
			if rejected && err.Code == "stale_settings" && m.projectMode == "project-root" {
				m.projectDirectoryConflicted = true
				m.projectError = "Settings changed. Review the folder, then Save again."
			}
			if rejected && err.Code == "stale_project" && m.projectMode == "rename" {
				m.projectRenameConflicted = true
				m.projectError = "Project changed. Review the name, then Save again."
			}
			if rejected && err.Code == "workspace_unavailable" && (msg.command.Kind == "prompt.send" || msg.command.Kind == "prompt.reopen-send" || msg.command.Kind == "thread.resume") {
				m.status = "This thread's worktree is unavailable; your prompt is kept. Open the Worktree row below the prompt to relocate or forget it."
			}
			if rejected && err.Code == "checkout_busy" && msg.command.Kind == "thread.resume" {
				m.status = "Checkout busy: another thread is writing; Resume when it finishes"
			}
			if rejected {
				m.busy = nil
				m.state.Pending = nil
				m.markDirty()
			}
			m.configureInputs()
			if rejected && (msg.command.Kind == "thread.start" && msg.command.ProjectID == m.state.DraftProjectID ||
				(msg.command.Kind == "prompt.send" || msg.command.Kind == "prompt.reopen-send") && msg.command.ThreadID == m.state.Active) {
				return m, m.showSendError(m.status)
			}
			if strings.HasPrefix(msg.command.Kind, "worktree.") {
				if !rejected {
					return m, m.showNoticeAs(noticeError, "Worktree: "+m.status)
				}
				return m, m.worktreeCommandFailed(msg.command, msg.err)
			}
			if msg.command.Kind == "queue.steer" {
				message := m.status
				m.status = ""
				return m, m.showNoticeAs(noticeError, "Steer: "+message)
			}
			if msg.command.Kind == "settings.update" || msg.command.Kind == "project.update" || msg.command.Kind == "project.remove" || msg.command.Kind == "thread.create" || msg.command.Kind == "thread.start" || msg.command.Kind == "prompt.reopen-send" || msg.command.Kind == "thread.resume" {
				return m, m.showNoticeAs(noticeError, m.status)
			}
			return m, nil
		}
		m.busy = nil
		m.state.Pending = nil
		m.status = "Accepted"
		if msg.receipt.State != "" {
			m.status += " · " + msg.receipt.State
		}
		if strings.HasPrefix(msg.command.Kind, "worktree.") {
			m.pruneKey = ""
			m.markDirty()
			m.configureInputs()
			return m, m.showNoticeAs(noticeDone, worktreeDoneText(msg.command.Kind))
		}
		if msg.command.Kind == "thread.start" {
			if v := m.state.DraftThreads[msg.command.ProjectID]; v != nil {
				v.ContextError = ""
				// The branch now exists; a later draft names its own.
				if msg.command.Workspace != nil && msg.command.Workspace.Mode == workspaceWorktree && v.WorktreeBranch == msg.command.Workspace.Branch {
					v.WorktreeBranch = ""
				}
			}
		} else if msg.command.Kind == "prompt.send" || msg.command.Kind == "prompt.reopen-send" {
			if v := m.state.Threads[msg.command.ThreadID]; v != nil {
				v.ContextError = ""
			}
		}
		if msg.command.Kind == "queue.steer" {
			// Only a confirmed delivery is done; an accepted steer is still pending.
			severity, text := noticeActive, "Steer accepted · awaiting delivery"
			if steerDelivered(msg.receipt.State) {
				severity, text = noticeDone, "Message steered into the active turn"
			}
			cmd = m.showNoticeAs(severity, text)
		}
		if msg.command.Kind == "request.answer" {
			m.clearRequestFeedback(msg.command.ThreadID, msg.command.TargetID, msg.command.Revision, false)
			m.status = "Answer accepted by server · upstream confirmation unavailable"
			if msg.command.RequestAction != "" {
				m.status = questionActionLabel(msg.command.RequestAction) + " accepted by server · upstream confirmation unavailable"
			}
			cmd = m.showNotice(m.status)
		}
		if m.acceptThreadOperation(msg) {
			return m, m.nextActivityTick()
		}
		v := m.state.Threads[msg.command.ThreadID]
		if v == nil {
			return m, cmd
		}
		switch msg.local.Kind {
		case "send":
			// Exactly the sent captures leave the draft; attachments added
			// while Send captured files stay for the next Send.
			removeSentAttachments(v, msg.command.Attachments)
			if v.Draft == msg.command.Text {
				v.Draft = ""
				if m.state.Active == msg.command.ThreadID {
					m.prompt.SetValue("")
				}
			}
		case "save-edit":
			if m.state.Edit != nil {
				// Save sends trimmed text; compare the same way so trailing
				// whitespace cannot hold the editor in edit mode forever.
				if m.state.Edit.ID != msg.command.TargetID {
					break
				}
				if ev := m.state.Threads[m.state.Edit.ThreadID]; ev != nil {
					v = ev
				}
				if strings.TrimSpace(v.Draft) == strings.TrimSpace(msg.command.Text) && msg.command.Settings != nil && v.Settings == *msg.command.Settings {
					v.Draft = m.state.Edit.OldDraft
					v.Settings = m.state.Edit.OldSettings
					if m.state.Active == m.state.Edit.ThreadID && !m.creatingThread() {
						m.prompt.SetValue(v.Draft)
					}
					m.state.Edit = nil
				} else {
					m.state.Edit.Revision++
					m.status = "Saved earlier edit; newer text remains in the editor"
				}
			}
		case "terminal-open":
			if msg.local.Value == "bottom" {
				openTerminalTab(&v.Bottom, msg.receipt.TargetID)
				v.BottomScroll = 0
				if m.state.Active == msg.command.ThreadID && !m.singleColumn() {
					m.state.Layout.Bottom = true
				}
			} else {
				openTerminalTab(&v.Host, msg.receipt.TargetID)
				if !m.singleColumn() {
					v.RightVisible = true
					if m.state.Active == msg.command.ThreadID {
						m.state.Layout.Right = true
						// As for other opened surfaces: present the new tab even
						// when the host cannot fit beside the conversation.
						if m.state.Layout.Compute(m.width, m.height-1, m.footerHeight()).Right.W == 0 {
							m.state.Layout.RevealRight()
						}
					}
				}
			}
		case "close":
			if m.state.Active == msg.command.ThreadID {
				m.state.Layout.Close(&v.Host, msg.command.TargetID)
			} else {
				v.Host.Close(msg.command.TargetID)
			}
			if len(v.Host.Tabs) == 0 {
				v.RightVisible = false
				if m.state.Active == msg.command.ThreadID && m.singleColumn() && v.CompactColumn == shell.RightRegion {
					m.selectColumn(shell.CenterRegion)
				}
			}
		case "bottom-close":
			v.Bottom.Close(msg.command.TargetID)
			// The last bottom session ending hides the panel, as closing the
			// last right tab hides its host; showing it again opens a fresh one.
			if len(v.Bottom.Tabs) == 0 && m.state.Active == msg.command.ThreadID {
				if m.singleColumn() {
					if v.CompactColumn == shell.BottomRegion {
						m.selectColumn(shell.CenterRegion)
					}
				} else if m.state.Layout.Bottom {
					m.state.Layout.ToggleBottom()
				}
			}
		}
		m.markDirty()
		m.configureInputs()
	case saveTick:
		cmd = tea.Batch(m.save(), tea.Tick(time.Second, func(time.Time) tea.Msg { return saveTick{} }))
	case saveMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "Draft/layout save failed: " + safe(msg.err.Error())
		} else if msg.generation == m.generation {
			m.dirty = false
		}
	case tea.KeyPressMsg:
		cmd = m.key(msg)
	case tea.PasteMsg:
		if c, handled := m.terminalPaste(msg.Content); handled {
			cmd = c
		} else if c, handled := m.docPaste(msg.Content); handled {
			cmd = c
		} else if m.terminalTooSmall() || m.viewer != nil || m.projectMode == "" && (len(m.menu) > 0 || m.settingsPage != "" && (m.focus == "prompt" || m.focus == "answer")) {
			// Nothing behind a modal, the resize notice or settings accepts input.
			cmd = m.showNoticeAs(noticeUnavailable, "Paste ignored · no visible input")
		} else if m.projectMode != "" {
			cmd = updateInput(&m.projectInput, tea.PasteMsg{Content: singleLine(msg.Content)})
			m.menuIndex = 0
			m.refreshProjectMenu()
		} else if m.focus == "thread-search" && len(m.menu) == 0 {
			cmd = m.updateThreadSearch(tea.PasteMsg{Content: singleLine(msg.Content)})
		} else if m.focus == "answer" {
			m.answerView.Reset()
			cmd = updateInput(&m.answer, tea.PasteMsg{Content: safe(msg.Content)})
			m.storeAnswer(m.answer.Value())
		} else if m.focus == gitMessageKey {
			cmd = m.gitMessagePaste(msg.Content)
		} else if m.focus == gitBranchNameKey {
			cmd = m.gitBranchNamePaste(msg.Content)
		} else if m.focus == gitJobInputKey {
			cmd = m.gitJobInputPaste(msg.Content)
		} else if m.focus == "prompt" {
			m.promptView.Reset()
			cmd = updateInput(&m.prompt, tea.PasteMsg{Content: safe(msg.Content)})
			m.viewState().Draft = m.prompt.Value()
			m.markDirty()
		}
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		cmd = m.mouse(msg.(tea.MouseMsg))
	default:
		if m.projectMode != "" {
			cmd = updateInput(&m.projectInput, msg)
		} else if m.focus == "thread-search" {
			cmd = updateInput(&m.threadSearch, msg)
		} else if m.focus == "prompt" {
			cmd = updateInput(&m.prompt, msg)
		} else if m.focus == "answer" {
			cmd = updateInput(&m.answer, msg)
		}
	}
	if m.prompt.Value() != m.promptLayoutValue || m.answer.Value() != m.answerLayoutValue {
		m.configureInputs()
	}
	m.promptMetrics = inputViewportMetrics(m.prompt, m.promptMetrics.Total)
	m.answerMetrics = inputViewportMetrics(m.answer, m.answerMetrics.Total)
	if len(m.menu) > 0 {
		m.menuOffset = m.menuStart(m.menuVisibleItems())
	}
	return m, tea.Batch(cmd, m.nextActivityTick(), m.nextCheckoutInspection(), m.nextDraftStartInspection(), m.nextPruneInspection(), m.nextPathQuery(), m.nextGitRefresh(), m.nextFilesRefresh())
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.terminalKey(k); handled {
		return cmd
	}
	if cmd, handled := m.docKey(k); handled {
		return cmd
	}
	s := k.String()
	if m.docReview != nil && len(m.menu) == 0 {
		if cmd, handled := m.docReviewKey(k); handled {
			return cmd
		}
	}
	if m.viewer != nil && m.contextMenu == nil {
		if cmd, handled := m.gitViewerWriteKey(s); handled {
			return cmd
		}
		if cmd, handled := m.viewerKey(k); handled {
			return cmd
		}
	}
	if s == "esc" && m.focus == gitJobInputKey && len(m.menu) == 0 {
		return m.gitJobInputKeyPress(k)
	}
	if s == "esc" && m.focus == gitBranchNameKey && len(m.menu) == 0 {
		return m.closeGitCreate()
	}
	if s == "esc" && len(m.menu) == 0 && m.gitCF.viewer != nil && (gitFocusKey(m.focus) || m.focus == "right-body" || m.focus == "git-refresh") {
		return m.closeGitConflictViewer()
	}
	if len(m.menu) == 0 && contextMenuKey(k) {
		return m.openContextMenuForFocus()
	}
	if stroke := k.Keystroke(); stroke == "ctrl+v" || stroke == "ctrl+shift+v" || stroke == "super+v" || stroke == "shift+insert" {
		return m.pasteClipboard()
	}
	if s == "ctrl+q" {
		// The guard replaces any open menu, including its own.
		if m.docQuitGuard() {
			return nil
		}
		return m.quit()
	}
	if s == "ctrl+z" {
		return m.suspend()
	}
	if s == "f4" && m.contextMenu == nil {
		if m.settingsPage != "" && !m.terminalTooSmall() {
			m.openSettingsCommands()
		} else {
			m.openCommands()
		}
		return nil
	}
	if s == "ctrl+shift+c" || s == "ctrl+c" || k.Keystroke() == "super+c" {
		if m.selectionLive(m.measure()) && m.selectedText != "" {
			return m.copyText(m.selectedText)
		}
		if m.focus == "prompt" {
			normalizeInputSelection(&m.prompt)
			if m.prompt.HasSelection() {
				return m.copyText(m.prompt.SelectedText())
			}
		}
		if m.focus == "answer" {
			normalizeInputSelection(&m.answer)
			if m.answer.HasSelection() {
				return m.copyText(m.answer.SelectedText())
			}
		}
		if s == "ctrl+c" {
			if m.docQuitGuard() {
				return nil
			}
			return m.quit()
		}
		m.status = "No text is selected"
		return nil
	}
	if len(m.menu) > 0 {
		if m.projectMode != "" {
			return m.projectKey(k)
		}
		switch s {
		case "esc":
			if m.contextMenu != nil {
				m.closeContextMenu()
			} else {
				m.menu = nil
			}
		case "up", "shift+tab":
			m.menuIndex = m.menuStep(m.menuIndex, -1, true)
		case "down", "tab":
			m.menuIndex = m.menuStep(m.menuIndex, 1, true)
		case "delete", "backspace":
			if item := m.menu[m.menuIndex]; item.Action.Kind == "tab" {
				m.menu = nil
				return m.activate(action{Kind: "close", ID: item.Action.ID})
			}
		case "enter":
			if m.menu[m.menuIndex].Separator {
				return nil
			}
			if m.contextMenu != nil {
				return m.selectContextMenuItem(m.menuIndex)
			}
			a := m.menu[m.menuIndex].Action
			m.menu = nil
			return m.activate(a)
		}
		return nil
	}
	if m.terminalTooSmall() {
		// The resize notice has no visible editor. Keep its Commands control
		// reachable without sending or changing an invisible draft.
		if s == "enter" && m.focus == "commands" {
			return m.activate(action{Kind: "commands"})
		}
		if s == "tab" || s == "shift+tab" {
			return m.setFocus("commands")
		}
		return nil
	}
	if handled, cmd := m.mentionKey(k); handled {
		return cmd
	}
	if m.settingsPage != "" {
		if s == "f6" {
			if m.singleColumn() && m.settingsNavigation {
				return m.setFocus("settings-category:" + m.settingsPage)
			}
			return m.setFocus("sidebar-settings")
		}
		if s == "esc" {
			return m.activate(action{Kind: "settings-back"})
		}
		if s == "f2" {
			if m.singleColumn() {
				return m.activate(action{Kind: "settings-nav"})
			}
			return nil
		}
		if settingsPaneKey(s) {
			return nil
		}
	}
	if m.focus == "answer" {
		if handled, cmd := m.answerFieldKey(s); handled {
			return cmd
		}
	}
	switch s {
	case "esc":
		if m.sendCapture != nil && m.focus == "prompt" {
			return m.cancelSendCapture()
		}
		if m.state.Edit != nil {
			return m.activate(action{Kind: "cancel-edit"})
		}
		return m.setFocus("prompt")
	case "f2":
		return m.activate(action{Kind: "left"})
	case "f3":
		return m.activate(action{Kind: "right"})
	case "f5":
		return m.activate(action{Kind: "bottom"})
	case "f7":
		return m.activate(action{Kind: "maximize"})
	case "f8":
		return m.activate(action{Kind: "theme"})
	case "ctrl+s":
		// Inside the request card Ctrl+S answers the request; it never sends
		// the ordinary prompt from there.
		if requestControlKey(m.focus) {
			return m.submitFocusedRequest()
		}
		return m.activate(action{Kind: "send"})
	case "f6":
		f := m.measure()
		var keys []string
		if f.prompt.W > 0 {
			keys = append(keys, "prompt")
		}
		if !m.hasComposer() {
			keys = append(keys, "empty-new")
		} else if f.transcript.H > 0 {
			keys = append(keys, "transcript")
		}
		if f.detail.W > 0 {
			keys = append(keys, "right-body")
		}
		if f.filesTree.W > 0 {
			keys = append(keys, "files-tree")
		}
		if f.filesText.W > 0 {
			keys = append(keys, "files-text")
		}
		if f.answer.W > 0 {
			keys = append(keys, "answer")
		} else if f.request.W > 0 {
			keys = append(keys, "request-body")
		}
		if f.bottomBody.W > 0 {
			keys = append(keys, "bottom-body")
		}
		for _, pane := range f.terms {
			keys = append(keys, "terminal:"+pane.id)
		}
		if f.navigation.W > 0 {
			keys = append(keys, "navigation")
		}
		if f.closedNavigation.H > 0 {
			keys = append(keys, "closed-navigation")
		}
		if f.settingsBody.H > 0 {
			keys = append(keys, "sidebar-settings")
		}
		i := slices.Index(keys, m.focus)
		return m.setFocus(keys[(i+1)%len(keys)])
	case "tab", "shift+tab":
		f := m.measure()
		var keys []string
		for _, h := range f.hits {
			// Tree rows share the tree's one Tab stop.
			if !slices.Contains(keys, h.Key) && !strings.HasPrefix(h.Key, "files-row:") {
				keys = append(keys, h.Key)
			}
		}
		i := slices.Index(keys, m.focus)
		step := 1
		if s == "shift+tab" {
			step = -1
		}
		if len(keys) > 0 {
			i = (i + step + len(keys)) % len(keys)
			return m.setFocus(keys[i])
		}
	case "alt+left":
		return m.activate(action{Kind: "resize-right", Index: 2})
	case "alt+right":
		return m.activate(action{Kind: "resize-right", Index: -2})
	case "alt+up":
		return m.activate(action{Kind: "resize-bottom", Index: 1})
	case "alt+down":
		return m.activate(action{Kind: "resize-bottom", Index: -1})
	}
	if m.focus == "thread-search" {
		if s == "enter" || s == "down" {
			return m.setFocus("navigation")
		}
		if s == "shift+enter" || s == "ctrl+j" {
			return nil
		}
		return m.updateThreadSearch(k)
	}
	if m.focus == "prompt" {
		if s == "enter" {
			return m.activate(action{Kind: "send"})
		}
		m.promptView.Reset()
		if s == "shift+enter" || s == "ctrl+j" {
			k = tea.KeyPressMsg{Code: tea.KeyEnter}
		}
		c := updateInput(&m.prompt, k)
		m.viewState().Draft = m.prompt.Value()
		m.markDirty()
		return c
	}
	if m.focus == gitMessageKey {
		return m.gitMessageKeyPress(k)
	}
	if m.focus == gitBranchNameKey {
		return m.gitBranchNameKeyPress(k)
	}
	if m.focus == gitJobInputKey {
		return m.gitJobInputKeyPress(k)
	}
	if cmd, handled := m.gitRowKey(s); handled {
		return cmd
	}
	if cmd, handled := m.gitRefKey(s); handled {
		return cmd
	}
	if cmd, handled := m.filesKey(s); handled {
		return cmd
	}
	if m.focus == "answer" {
		m.answerView.Reset()
		if s == "shift+enter" || s == "ctrl+j" {
			k = tea.KeyPressMsg{Code: tea.KeyEnter}
		}
		c := updateInput(&m.answer, k)
		m.storeAnswer(m.answer.Value())
		return c
	}
	if a, ok := m.chipKey(s); ok {
		return m.activate(a)
	}
	if handled, cmd := m.questionCardKey(s); handled {
		return cmd
	}
	if s == "enter" || s == " " || s == "space" {
		for _, h := range m.measure().hits {
			if h.Key == m.focus {
				return m.activate(h.Action)
			}
		}
	}
	if cmd, handled := m.terminalHistoryKey(s); handled {
		return cmd
	}
	if gitFocusKey(m.focus) && (s == "up" || s == "down") {
		if s == "up" {
			return m.moveGitFocus(-1)
		}
		return m.moveGitFocus(1)
	}
	if s == "up" || s == "down" || s == "pgup" || s == "pgdown" || s == "home" || s == "end" {
		delta := 1
		if s == "up" || s == "pgup" {
			delta = -1
		}
		if strings.HasPrefix(s, "pg") {
			delta *= 8
		}
		f := m.measure()
		target := m.scrollFocus()
		if bar, ok := f.scrollbars[target]; ok {
			offset := bar.Bar.Offset + delta
			if s == "home" {
				offset = 0
			}
			if s == "end" {
				offset = bar.Bar.MaxOffset
			}
			m.scrollTo(target, offset, f)
		}
	}
	return nil
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	f := m.measure()
	p := msg.Mouse()
	if m.docReview != nil && len(m.menu) == 0 {
		if cmd, handled := m.docReviewMouse(msg, f); handled {
			return cmd
		}
	}
	if m.viewer != nil && m.contextMenu == nil {
		if cmd, handled := m.viewerMouse(msg, f); handled {
			return cmd
		}
	}
	if cmd, handled := m.docMouse(msg, f); handled {
		return cmd
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		d := 3
		switch p.Button {
		case tea.MouseWheelUp:
			d = -3
		case tea.MouseWheelDown:
		default:
			return nil
		}
		if r := m.mentionRect(f); r.Contains(p.X, p.Y) {
			m.mentionIndex = max(0, min(len(m.mentionEntries())-1, m.mentionIndex+d))
			return nil
		}
		if len(m.menu) == 0 && m.filesWheel(f, p.X, p.Y, d) {
			return nil
		}
		if len(m.menu) == 0 {
			for _, pane := range f.terms {
				if pane.grid.Contains(p.X, p.Y) {
					return m.terminalScroll(pane.id, -d)
				}
			}
		}
		target := m.wheelTarget(f, p.X, p.Y)
		if target == "menu" {
			m.menuIndex = m.menuStep(m.menuIndex, d, false)
		} else if bar, ok := f.scrollbars[target]; ok {
			m.scrollTo(target, bar.Bar.Offset+d, f)
		} else if target != "" {
			m.scrollTo(target, 0, f)
		}
	case tea.MouseClickMsg:
		if p.Button == tea.MouseRight {
			if len(m.menu) != 0 {
				return nil
			}
			return m.openContextMenuAtPointer(f, p.X, p.Y)
		}
		if p.Button != tea.MouseLeft {
			return nil
		}
		// Clicking anywhere outside the typing terminal's grid leaves it.
		if m.termFocus != "" && !slices.ContainsFunc(f.terms, func(t termPane) bool { return t.id == m.termFocus && t.grid.Contains(p.X, p.Y) }) {
			m.termFocus = ""
		}
		if handled, cmd := m.dismissMenuOutside(p.X, p.Y); handled {
			return cmd
		}
		if len(m.menu) == 0 {
			m.drag = f.geom.DividerAt(p.X, p.Y)
			m.lastX, m.lastY = p.X, p.Y
			if m.drag != shell.NoDivider {
				return nil
			}
		}
		for i := len(f.hits) - 1; i >= 0; i-- {
			h := f.hits[i]
			if h.Rect.Contains(p.X, p.Y) {
				if strings.HasPrefix(h.Action.Kind, "mention-") {
					return m.activate(h.Action)
				}
				if h.Action.Kind == "scrollbar" {
					target := f.scrollbars[h.Action.ID]
					if h.Action.Value == "thumb" {
						m.scrollDrag = h.Action.ID
						m.scrollGrab = h.Action.Index - target.Bar.ThumbStart
					} else {
						m.scrollTo(h.Action.ID, target.Bar.PageAt(h.Action.Index), f)
					}
					return nil
				}
				if m.contextMenu == nil {
					m.setFocus(h.Key)
				}
				if h.Key == "project-input" {
					m.projectInput.BeginSelection(p.X-h.Rect.X, p.Y-h.Rect.Y)
					return nil
				}
				if h.Key == "thread-search" {
					m.threadSearch.BeginSelection(p.X-h.Rect.X, 0)
					return nil
				}
				if h.Key == "prompt" {
					m.prompt.BeginSelection(p.X-f.prompt.X, m.promptView.RelativeY(&m.prompt, p.Y-f.prompt.Y))
					m.promptView.Refresh(&m.prompt, m.promptMetrics.Total)
					return nil
				}
				if h.Key == "answer" {
					m.answer.BeginSelection(p.X-f.answer.X, m.answerView.RelativeY(&m.answer, p.Y-f.answer.Y))
					m.answerView.Refresh(&m.answer, m.answerMetrics.Total)
					return nil
				}
				if h.Key == "transcript" || h.Key == "right-body" {
					m.selecting = true
					m.selectedText = ""
					m.selectionRegion = h.Rect
					m.selectionBasis = m.selectionBasisFor(f)
					m.selectionStart = [2]int{p.X, p.Y}
					m.selectionEnd = m.selectionStart
					return nil
				}
				return m.activate(h.Action)
			}
		}
	case tea.MouseMotionMsg:
		m.hover = ""
		for _, h := range f.hits {
			if h.Rect.Contains(p.X, p.Y) {
				m.hover = h.Key
			}
		}
		if m.scrollDrag != "" {
			if target, ok := f.scrollbars[m.scrollDrag]; ok {
				m.scrollTo(m.scrollDrag, target.Bar.DragTo(p.Y-target.Rect.Y, m.scrollGrab), f)
			} else {
				m.scrollDrag = ""
			}
		} else if m.drag != shell.NoDivider {
			// Track the pointer against the effective divider so an overshoot
			// leaves no hidden debt; resizePane clamps to the current viewport.
			d := p.X - f.geom.LeftDivider.X
			if m.drag == shell.RightDivider {
				d = f.geom.RightDivider.X - p.X
			}
			if m.drag == shell.BottomDivider {
				d = f.geom.BottomDivider.Y - p.Y
			}
			m.resizePane(m.drag, d)
			m.lastX, m.lastY = p.X, p.Y
			m.markDirty()
			m.configureInputs()
		} else if m.selecting {
			m.selectionEnd = [2]int{max(m.selectionRegion.X, min(p.X, m.selectionRegion.X+m.selectionRegion.W-1)), max(m.selectionRegion.Y, min(p.Y, m.selectionRegion.Y+m.selectionRegion.H-1))}
		} else if p.Button == tea.MouseLeft {
			if m.focus == "project-input" {
				for _, h := range f.hits {
					if h.Key == "project-input" {
						m.projectInput.ExtendSelection(p.X-h.Rect.X, p.Y-h.Rect.Y)
					}
				}
			} else if m.focus == "thread-search" {
				m.threadSearch.ExtendSelection(p.X-threadSearchRect(f.geom.Left).X, 0)
			} else if m.focus == "prompt" {
				m.prompt.ExtendSelection(p.X-f.prompt.X, m.promptView.RelativeY(&m.prompt, p.Y-f.prompt.Y))
				m.promptView.Refresh(&m.prompt, m.promptMetrics.Total)
			} else if m.focus == "answer" {
				m.answer.ExtendSelection(p.X-f.answer.X, m.answerView.RelativeY(&m.answer, p.Y-f.answer.Y))
				m.answerView.Refresh(&m.answer, m.answerMetrics.Total)
			}
		}
	case tea.MouseReleaseMsg:
		m.scrollDrag = ""
		m.drag = shell.NoDivider
		m.projectInput.EndSelection()
		m.threadSearch.EndSelection()
		normalizeInputSelection(&m.threadSearch)
		normalizeInputSelection(&m.projectInput)
		m.prompt.EndSelection()
		m.answer.EndSelection()
		normalizeInputSelection(&m.prompt)
		normalizeInputSelection(&m.answer)
		m.promptView.Refresh(&m.prompt, m.promptMetrics.Total)
		m.answerView.Refresh(&m.answer, m.answerMetrics.Total)
		if m.selecting {
			painted := m.render()
			if m.selectionLive(painted) {
				m.selectedText = painted.selection(m.selectionStart, m.selectionEnd, m.selectionRegion)
				m.status = "Text selected · Ctrl+C / Ctrl+Shift+C copies"
				m.selecting = false
				return m.showNotice(m.status)
			}
			m.selecting = false
		}
	}
	return nil
}

// removeSentAttachments removes from v each attachment a command sent, once
// per sent capture (matched by artifact id, else kind/name/source).
func removeSentAttachments(v *threadView, sent []protocol.Attachment) {
	for _, s := range sent {
		for i, d := range v.Attachments {
			if sameCapture(d, s) {
				v.Attachments = slices.Delete(v.Attachments, i, i+1)
				break
			}
		}
	}
	if len(v.Attachments) == 0 {
		v.Attachments = nil
	}
}

// Clamp before applying the delta so saved offsets from an older viewport (or
// older versions with unbounded overscroll) never create invisible scroll debt.
func scrollBy(offset *int, delta, limit int) bool {
	previous := *offset
	*offset = min(limit, max(0, min(limit, max(0, previous))+delta))
	return previous != *offset
}

func (m *Model) clampScroll(f frame) {
	v := m.viewState()
	changed := false
	if f.transcript.H > 0 {
		if v.Pinned || v.Scroll >= f.transcriptMax {
			// Following new output moves the offset every frame; only the
			// pin flip is a saved-view change worth a persisted revision.
			changed = !v.Pinned || changed
			v.Pinned, v.Scroll = true, f.transcriptMax
			v.SeenActivity = messageCount(m.thread())
		}
		changed = scrollBy(&v.Scroll, 0, f.transcriptMax) || changed
	}
	if f.detail.H > 0 {
		changed = scrollBy(&v.DetailScroll, 0, f.detailMax) || changed
	}
	if f.request.H > 0 {
		changed = scrollBy(&v.RequestScroll, 0, f.requestMax) || changed
	}
	if f.bottomBody.H > 0 {
		changed = scrollBy(&v.BottomScroll, 0, f.bottomMax) || changed
	}
	if changed {
		m.markDirty()
	}
}
