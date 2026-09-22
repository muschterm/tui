package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func pathModel() *Model {
	m := navigationModel()
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "path-completion", "workspace-file-context")
	m.snapshot.AppSettings.ProjectDirectory = "/work"
	m.beginThreadDraft("alpha")
	return m
}

func pathResults(m *Model, result protocol.BrowseResult) {
	m.nextPathQuery()
	m.acceptPathQuery(pathQueryResult{key: m.paths.key, generation: m.paths.generation, result: result})
}

func mentionModel(value string) *Model {
	m := pathModel()
	m.prompt.SetValue(value)
	inputSetCursor(&m.prompt, len([]rune(value)))
	m.setFocus("prompt")
	return m
}

func TestProjectDestinationAlwaysChoosesAndSearches(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		for _, filter := range []string{"", "alpha"} {
			t.Run(fmt.Sprintf("%d-%s", count, filter), func(t *testing.T) {
				m := pathModel()
				m.snapshot.Projects = m.snapshot.Projects[:count]
				m.state.ProjectFilter = filter
				m.prompt.SetValue("retain me")
				before := len(m.snapshot.Threads)
				m.activate(action{Kind: "thread-create"})
				if m.projectMode != "new-thread" || m.busy != nil || len(m.snapshot.Threads) != before || m.prompt.Value() != "retain me" {
					t.Fatal("creation skipped destination or changed draft")
				}
				if m.menu[len(m.menu)-1].Action.Kind != "project-add-thread" {
					t.Fatal("missing Add project destination")
				}
				m.projectInput.SetValue("/work/beta")
				m.refreshProjectMenu()
				if count == 2 && (len(m.menu) != 2 || m.menu[0].Action.Value != "beta") {
					t.Fatal("destination does not search paths")
				}
			})
		}
	}
}

func TestProjectDestinationAddedReceiptWaitsForSnapshotAndRestoresDraft(t *testing.T) {
	m := pathModel()
	m.beginThreadDraft("beta")
	m.prompt.SetValue("saved beta")
	m.viewState().Draft = "saved beta"
	m.beginThreadDraft("alpha")
	beta := m.snapshot.Projects[1]
	m.snapshot.Projects = m.snapshot.Projects[:1]
	m.activate(action{Kind: "thread-create"})
	m.activate(action{Kind: "project-add-thread"})
	m.acceptThreadOperation(commandMsg{command: protocol.Command{Kind: "project.add"}, local: action{Value: "new-thread"}, receipt: protocol.Receipt{TargetID: "beta", State: "accepted"}})
	if m.pendingProjectSelection != "beta" || !m.pendingProjectDraft {
		t.Fatal("receipt lost pending destination")
	}
	m.snapshot.Projects = append(m.snapshot.Projects, beta)
	m.reconcileThreadMembership()
	if m.prompt.Value() != "saved beta" || m.busy != nil {
		t.Fatalf("snapshot failed to restore draft: %q", m.prompt.Value())
	}
}

func TestPathFolderBrowsingCancelsAndRejectsStaleResultsWithoutMutation(t *testing.T) {
	m := pathModel()
	m.activate(action{Kind: "project-add"})
	before, _ := json.Marshal(m.snapshot)
	pathResults(m, protocol.BrowseResult{Directory: "/work", Entries: []protocol.PathEntry{{Name: "alpha", Path: "/work/alpha/", IsDir: true}}})
	oldKey, oldGeneration := m.paths.key, m.paths.generation
	canceled := false
	m.paths.cancel = func() { canceled = true }
	m.activate(action{Kind: "path-directory", Value: "/work/alpha/"})
	m.acceptPathQuery(pathQueryResult{key: oldKey, generation: oldGeneration, result: protocol.BrowseResult{Directory: "/wrong"}})
	after, _ := json.Marshal(m.snapshot)
	if !canceled || m.paths.result.Directory == "/wrong" || m.busy != nil || string(before) != string(after) || m.projectInput.Value() != "/work/alpha/" {
		t.Fatal("browse mutated state or accepted stale query")
	}
	m.activate(action{Kind: "project-cancel"})
	m.acceptPathQuery(pathQueryResult{key: m.paths.key, generation: m.paths.generation, result: protocol.BrowseResult{Directory: "/late"}})
	if m.paths.result.Directory == "/late" {
		t.Fatal("dismissed folder accepted result")
	}
}

func TestPathStartingFolderUsesAppScopeAndOpeningRevision(t *testing.T) {
	m := pathModel()
	m.snapshot.AppSettings.Revision = 7
	m.openSidebarSettings("general", "")
	m.activate(action{Kind: "app-project-directory"})
	if m.projectMode != "project-root" || m.projectInput.Value() != "/work/" {
		t.Fatal("starting folder editor missing")
	}
	m.snapshot.AppSettings.Revision = 8
	m.saveProjectDirectory("/elsewhere")
	if m.busy == nil || m.busy.Revision != 7 || m.busy.ProjectID != "" || m.busy.AppSettings.ProjectDirectory != "/elsewhere" || m.snapshot.AppSettings.ProjectDirectory != "/work" {
		t.Fatal("setting lost CAS or app scope")
	}
	other := pathModel()
	other.openSidebarSettings("general", "alpha")
	other.activate(action{Kind: "app-project-directory"})
	if other.projectMode != "" || other.busy != nil {
		t.Fatal("project scope exposed app starting folder")
	}
}

func TestMentionCursorBoundaryAndQuotedUnicode(t *testing.T) {
	for _, tc := range []struct {
		value  string
		cursor int
		query  string
		valid  bool
	}{
		{"mail a@b.com", 12, "", false}, {"read @src later", 9, "src", true}, {"界面 @\"目录/my f", 12, "目录/my f", true}, {"read @closed ", 13, "", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			m := mentionModel(tc.value)
			inputSetCursor(&m.prompt, tc.cursor)
			got, ok := m.activeMention()
			if ok != tc.valid || ok && got.query != tc.query {
				t.Fatalf("mention=%+v valid=%t", got, ok)
			}
		})
	}
}

func TestMentionBrowseSelectAndRemoveThroughKeyboardAndMouse(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		t.Run(fmt.Sprint(mouse), func(t *testing.T) {
			m := mentionModel("read @界 suffix")
			inputSetCursor(&m.prompt, len([]rune("read @界")))
			pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Directory: "", Entries: []protocol.PathEntry{{Name: "界 面", Path: "界 面/", IsDir: true}}})
			choose := func() {
				if mouse {
					clickControl(m, controlHit(t, m.measure(), "mention:0"))
				} else {
					m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
			}
			choose()
			if m.prompt.Value() != "read @\"界 面/ suffix" || len(m.viewState().Attachments) != 0 || m.busy != nil {
				t.Fatalf("directory selection changed work: %q", m.prompt.Value())
			}
			pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Directory: "", Entries: []protocol.PathEntry{{Name: "file 文.txt", Path: "界 面/file 文.txt"}}})
			choose()
			if m.prompt.Value() != "read @\"界 面/file 文.txt\"  suffix" || len(m.viewState().Attachments) != 1 || m.viewState().Attachments[0].Source != "界 面/file 文.txt" || m.viewState().Attachments[0].Content != "" || m.busy != nil {
				t.Fatalf("file selection failed: %q %+v", m.prompt.Value(), m.viewState().Attachments)
			}
			value := m.prompt.Value()
			m.activate(action{Kind: "attachment-remove", Index: 0})
			if len(m.viewState().Attachments) != 0 || m.prompt.Value() != value {
				t.Fatal("removal damaged prompt")
			}
		})
	}
}

func TestMentionEnterWithNoMatchesNeverSendsAndEscapePreservesText(t *testing.T) {
	m := mentionModel("@missing")
	m.activate(action{Kind: "setting-model"})
	pathResults(m, protocol.BrowseResult{})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil || m.prompt.Value() != "@missing" {
		t.Fatal("Enter submitted unmatched mention")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := m.activeMention(); ok || m.prompt.Value() != "@missing" {
		t.Fatal("Escape did not dismiss safely")
	}
}

func TestMentionLateResultsCannotCrossQueryDestinationOrSettings(t *testing.T) {
	for _, change := range []string{"query", "draft", "settings", "focus"} {
		t.Run(change, func(t *testing.T) {
			m := mentionModel("@a")
			m.nextPathQuery()
			key, generation := m.paths.key, m.paths.generation
			switch change {
			case "query":
				m.prompt.SetValue("@b")
				inputSetCursor(&m.prompt, 2)
			case "draft":
				m.beginThreadDraft("beta")
				m.prompt.SetValue("@a")
				inputSetCursor(&m.prompt, 2)
			case "settings":
				m.openSidebarSettings("general", "")
			case "focus":
				m.setFocus("navigation")
			}
			m.acceptPathQuery(pathQueryResult{key: key, generation: generation, result: protocol.BrowseResult{Entries: []protocol.PathEntry{{Name: "stale", Path: "stale"}}}})
			if len(m.paths.result.Entries) > 0 {
				t.Fatal("late result leaked into changed context")
			}
		})
	}
}

func TestPathCompletionCapturesAndNarrowHitBounds(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, width := range []int{47, 160} {
		for _, light := range []bool{false, true} {
			for _, scene := range []string{"folder", "destination", "general", "mention"} {
				t.Run(fmt.Sprintf("%d-%t-%s", width, light, scene), func(t *testing.T) {
					m := mentionModel("Inspect @")
					height := 30
					if width == 47 {
						height = 22
					}
					m.state.Light = light
					m.Update(tea.WindowSizeMsg{Width: width, Height: height})
					switch scene {
					case "folder":
						m.activate(action{Kind: "project-add"})
						pathResults(m, protocol.BrowseResult{Directory: "/work", Entries: []protocol.PathEntry{{Name: "界 面", Path: "/work/界 面/", IsDir: true}}})
					case "destination":
						m.activate(action{Kind: "thread-create"})
					case "general":
						m.openSidebarSettings("general", "")
					case "mention":
						pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Entries: []protocol.PathEntry{{Name: "src", Path: "src/", IsDir: true}, {Name: "README.md", Path: "README.md"}}})
					}
					f := m.render()
					for _, h := range f.hits {
						if strings.HasPrefix(h.Key, "mention:") || h.Key == "mention-dismiss" {
							if h.Rect.X < 0 || h.Rect.Y < 0 || h.Rect.X+h.Rect.W > width || h.Rect.Y+h.Rect.H > height {
								t.Fatalf("popup hit out of bounds: %+v", h)
							}
						}
					}
					for _, row := range f.rows {
						if ansi.StringWidth(row) > width {
							t.Fatal("render overflow")
						}
					}
					if scene == "mention" && (!hasControl(f, "mention:0") || !hasControl(f, "mention-dismiss")) {
						t.Fatal("mention controls unavailable")
					}
					if dir != "" {
						file := filepath.Join(dir, fmt.Sprintf("%dx%d-light%t-path-%s.ansi", width, height, light, scene))
						if err := os.WriteFile(file, []byte(strings.Join(f.rows, "\n")), 0600); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestMentionAttachmentLimitPreservesDraftAndDeduplicatesSource(t *testing.T) {
	m := mentionModel("@R")
	result := protocol.BrowseResult{Entries: []protocol.PathEntry{{Name: "README.md", Path: "README.md"}}}
	m.viewState().Attachments = []protocol.Attachment{{Kind: "workspace-file", Name: "README.md", Source: "README.md"}}
	pathResults(m, result)
	m.selectMention(0)
	if len(m.viewState().Attachments) != 1 {
		t.Fatal("reselecting source duplicated attachment")
	}
	m.prompt.SetValue("@R")
	inputSetCursor(&m.prompt, 2)
	for len(m.viewState().Attachments) < 8 {
		m.viewState().Attachments = append(m.viewState().Attachments, protocol.Attachment{Kind: "file", Source: fmt.Sprint(len(m.viewState().Attachments))})
	}
	result.Entries[0] = protocol.PathEntry{Name: "ROADMAP.md", Path: "ROADMAP.md"}
	pathResults(m, result)
	m.selectMention(0)
	if m.prompt.Value() != "@R" || len(m.viewState().Attachments) != 8 || m.busy != nil {
		t.Fatal("full attachment list damaged draft or submitted work")
	}
}

func TestPathOlderServerShowsUpgradeWithoutIssuingQuery(t *testing.T) {
	m := mentionModel("@")
	m.snapshot.Capabilities = nil
	if cmd := m.nextPathQuery(); cmd != nil || m.paths.loading || !strings.Contains(m.paths.err, "Update this server") {
		t.Fatal("old backend did not receive explicit unavailable state")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil || m.prompt.Value() != "@" {
		t.Fatal("unsupported completion submitted prompt")
	}
}

func TestMentionDirectoryDefaultSelectsFirstFileBeforeParent(t *testing.T) {
	m := mentionModel("@src/")
	pathResults(m, protocol.BrowseResult{Root: "/work/alpha", Directory: "src", Entries: []protocol.PathEntry{{Name: "hello.txt", Path: "src/hello.txt"}}})
	entries := m.mentionEntries()
	if len(entries) != 2 || entries[0].Path != "src/hello.txt" || entries[1].Name != ".." {
		t.Fatalf("parent took default selection: %+v", entries)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.prompt.Value() != "@src/hello.txt " || len(m.viewState().Attachments) != 1 || m.viewState().Attachments[0].Source != "src/hello.txt" || m.busy != nil {
		t.Fatalf("Enter navigated away instead of attaching file: %q", m.prompt.Value())
	}
}

func TestPathCaptureErrorPersistsAfterNoticeAndClearsOnResolution(t *testing.T) {
	for _, resolution := range []string{"ack", "remove"} {
		t.Run(resolution, func(t *testing.T) {
			m := mentionModel("read this file")
			m.activate(action{Kind: "setting-model"})
			m.viewState().Attachments = []protocol.Attachment{{Kind: "workspace-file", Name: "missing.txt", Source: "missing.txt"}}
			m.activate(action{Kind: "send"})
			if m.busy == nil {
				t.Fatal("missing initial Send")
			}
			command := *m.busy
			m.Update(commandMsg{command: command, err: &protocol.Error{Code: "invalid_attachment", Message: "Cannot capture missing.txt: file is unavailable"}})
			m.Update(noticeExpired(m.notice.generation))
			if m.notice.text != "" || m.viewState().ContextError == "" || m.prompt.Value() != "read this file" || len(m.viewState().Attachments) != 1 || !hasControl(m.render(), "context-error") {
				t.Fatalf("capture failure lost draft or persistent error: %q", m.viewState().ContextError)
			}
			if resolution == "remove" {
				m.activate(action{Kind: "attachment-remove", Index: 0})
			} else {
				m.activate(action{Kind: "send"})
				if m.busy == nil {
					t.Fatal("cannot retry Send")
				}
				command = *m.busy
				m.Update(commandMsg{command: command, receipt: protocol.Receipt{State: "accepted", TargetID: "accepted-file-thread", Revision: m.snapshot.Revision + 1}})
			}
			if m.viewState().ContextError != "" || hasControl(m.render(), "context-error") {
				t.Fatal("resolved error remained visible")
			}
		})
	}
}

func TestPathUnmatchedPrefixCannotRegisterParent(t *testing.T) {
	m := pathModel()
	m.activate(action{Kind: "project-add"})
	m.projectInput.SetValue("/work/missing")
	pathResults(m, protocol.BrowseResult{Directory: "/work"})
	for _, item := range m.menu {
		if item.Action.Kind == "project-submit" {
			t.Fatal("incomplete path offered to register its parent")
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy != nil || m.projectInput.Value() != "/work/missing" {
		t.Fatal("unmatched Enter mutated destination")
	}
}
