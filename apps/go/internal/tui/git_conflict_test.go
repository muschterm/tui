package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (g *fakeGit) GitConflictFile(_ context.Context, t client.GitTarget, path, version string) (protocol.GitConflictFile, error) {
	g.record("conflict:" + path + ":" + version)
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.conflictFiles[path+"|"+version]
	if !ok {
		f = protocol.GitConflictFile{Path: path, Version: version}
	}
	return f, g.err
}

const cfPath = "src/app.go"

// conflictModel is a merge stopped with one text conflict whose pins are
// pin-1 / tok-1, with working, saved and side versions.
func conflictModel(t *testing.T) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := opModel(t)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-conflicts")
	st := rebaseStop()
	st.Kind = "merge"
	st.Conflicts = []protocol.GitConflict{{Path: cfPath, Kind: "UU", ConflictPin: "pin-1", WorktreeStat: "tok-1"}}
	st.Sides = protocol.GitConflictSides{Base: "merge base", Ours: "feature/init", Theirs: "side"}
	api.status.Operation = "merge"
	api.oper = st
	// Like the server today, the working version carries no Oid.
	working := protocol.GitConflictFile{Path: cfPath, Version: "working", Present: true, Kind: "file", Mode: "100644",
		Content: []byte("a\n<<<<<<< ours\nmine\x1b[2J\n=======\ntheirs\n>>>>>>> side\n"), HasMarkers: true,
		ConflictPin: "pin-1", WorktreeToken: "tok-1", CopyID: "copy-0", CopyReason: "at_stop", CopyCreatedAt: gitTestNow.Add(-2 * 3600e9).Format("2006-01-02T15:04:05Z07:00"),
		Copies: []protocol.GitConflictCopyRef{{CopyID: "copy-0", Reason: "at_stop"}, {CopyID: "copy-1", Reason: "before_overwrite", CreatedAt: gitTestNow.Add(-5 * 60e9).Format("2006-01-02T15:04:05Z07:00")}}}
	api.conflictFiles = map[string]protocol.GitConflictFile{
		cfPath + "|working": working,
		cfPath + "|saved":   {Path: cfPath, Version: "saved", Present: true, Oid: "s0", Mode: "100644", Content: []byte("orig\n")},
		cfPath + "|ours":    {Path: cfPath, Version: "ours", Present: true, Content: []byte("mine\n")},
		cfPath + "|base":    {Path: cfPath, Version: "base", Present: false, Kind: "absent"},
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	return m, api
}

func TestGitConflictViewerTabs(t *testing.T) {
	m, api := conflictModel(t)
	m.setFocus("git:conflict:" + cfPath)
	gitKey(t, m, "enter")
	text := gitSurfaceText(m)
	for _, want := range []string{"CONFLICT UU", cfPath, "base | ours | theirs | [working] | saved", "Contains conflict markers", "<<<<<<< ours", "Choose ours (feature/init)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("unsanitized content")
	}
	h, _ := findHit(m.measure(), "git:conflict-tab:base")
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	if api.count("conflict:"+cfPath+":base") != 1 || !strings.Contains(gitSurfaceText(m), "Absent on this side (deleted)") || !strings.Contains(gitSurfaceText(m), "base (merge base)") {
		t.Fatalf("base tab:\n%s", gitSurfaceText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-close"}))
	if m.gitCF.viewer != nil || len(api.sent()) != 0 {
		t.Fatal("close")
	}
}

func TestGitConflictChooseSameSendsRowPins(t *testing.T) {
	m, api := conflictModel(t)
	f := api.conflictFiles[cfPath+"|working"]
	f.Oid = "s0"
	api.conflictFiles[cfPath+"|working"] = f
	m.setFocus("git:conflict:" + cfPath)
	gitKey(t, m, "o")
	c := lastSent(t, api).Git.Conflict
	if c.Side != "ours" || c.ConflictPin != "pin-1" || c.WorktreeToken != "tok-1" || c.Path != cfPath {
		t.Fatalf("choose %+v", c)
	}
}

func TestGitConflictChooseConfirmsEdits(t *testing.T) {
	m, api := conflictModel(t)
	m.setFocus("git:conflict:" + cfPath)
	gitKey(t, m, "t")
	text := menuText(m)
	if !strings.Contains(text, "Replace your edits in src/app.go with theirs (side)?") || !strings.Contains(text, "A copy is kept") || m.menu[m.menuIndex].Label != "Cancel" || len(api.sent()) != 0 {
		t.Fatalf("choose dialog:\n%s", text)
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	if c := lastSent(t, api).Git.Conflict; c.Side != "theirs" || c.WorktreeToken != "tok-1" {
		t.Fatalf("choose %+v", c)
	}
}

func TestGitConflictStalePinsRefresh(t *testing.T) {
	m, api := conflictModel(t)
	f := api.conflictFiles[cfPath+"|working"]
	f.WorktreeToken = "tok-2"
	api.conflictFiles[cfPath+"|working"] = f
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if len(api.sent()) != 0 || len(m.menu) != 0 || !strings.Contains(m.notice.text, "changed since shown") {
		t.Fatalf("stale: %q %v", m.notice.text, api.sent())
	}
}

func TestGitConflictResolveAcknowledgements(t *testing.T) {
	m, api := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if !strings.Contains(menuText(m), "src/app.go still contains conflict markers") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("markers dialog:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	c := lastSent(t, api).Git.Conflict
	if c.As != "content" || c.AcknowledgeMarkers != "tok-1" || c.AcknowledgeBinary != "" {
		t.Fatalf("resolve %+v", c)
	}
	m.menu = nil
	delete(m.gitW.writes, m.gitW.draftKey)
	f := api.conflictFiles[cfPath+"|working"]
	f.HasMarkers, f.Binary = false, true
	api.conflictFiles[cfPath+"|working"] = f
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if !strings.Contains(menuText(m), "is binary") {
		t.Fatalf("binary dialog:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.AcknowledgeBinary != "tok-1" {
		t.Fatalf("binary %+v", c)
	}
	// A clean file stages directly.
	m.menu = nil
	delete(m.gitW.writes, m.gitW.draftKey)
	f.Binary = false
	api.conflictFiles[cfPath+"|working"] = f
	n := len(api.sent())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if len(api.sent()) != n+1 || lastSent(t, api).Git.Conflict.AcknowledgeMarkers != "" {
		t.Fatal("clean resolve")
	}
}

func TestGitConflictResolveDeleted(t *testing.T) {
	m, api := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-delete", ID: cfPath}))
	if !strings.Contains(menuText(m), "the file itself stays on disk as untracked") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("delete:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.As != "deleted" || c.ConflictPin != "pin-1" {
		t.Fatalf("delete %+v", c)
	}
}

func TestGitConflictRestoreAndPrevious(t *testing.T) {
	m, api := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-restore-menu", ID: cfPath}))
	text := menuText(m)
	if !strings.Contains(text, "Restore the original conflict (2h)…") || !strings.Contains(text, "Restore the copy before overwrite (5m)…") {
		t.Fatalf("restore menu: %q\n%s", m.notice.text, text)
	}
	m.menu = nil
	refReply(api, protocol.GitResult{State: protocol.GitStateSucceeded, Operation: &protocol.GitOperationResult{Kind: "merge", Outcome: "unchanged",
		Previous: &protocol.GitConflictPrevious{CopyID: "copy-9"}, Evicted: []protocol.GitConflictCopyRef{{CopyID: "old"}}}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-restore", ID: cfPath, Value: "copy-1"}))
	if !strings.Contains(menuText(m), "Replace src/app.go with the copy before overwrite (5m)?") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("restore confirm:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	m.menu = nil
	if c := lastSent(t, api).Git.Conflict; c.CopyID != "copy-1" || c.WorktreeToken != "tok-1" {
		t.Fatalf("restore %+v", c)
	}
	text = gitSurfaceText(m)
	if !strings.Contains(text, "Your previous content was saved") || !strings.Contains(text, "1 older saved copy of this stop was evicted") {
		t.Fatalf("result:\n%s", text)
	}
	m.viewState().DetailScroll = 0
	h, ok := findHit(m.measure(), "git:conflict-previous")
	if !ok {
		t.Fatal("no restore previous")
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.CopyID != "copy-9" {
		t.Fatalf("previous restore %+v", c)
	}
}

func TestGitConflictUnsavedAcknowledgement(t *testing.T) {
	m, api := conflictModel(t)
	f := api.conflictFiles[cfPath+"|working"]
	f.Oid = "s0"
	api.conflictFiles[cfPath+"|working"] = f
	first := true
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		if first {
			first = false
			return protocol.Receipt{}, &protocol.Error{Code: "unsaved_unacknowledged"}
		}
		return protocol.Receipt{ID: cmd.ID, State: "succeeded", Git: &protocol.GitResult{Op: "conflict_choose", State: "succeeded"}}, nil
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-choose-ours", ID: cfPath}))
	if !strings.Contains(menuText(m), "cannot be copied first") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("unsaved dialog:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	s := api.sent()
	if len(s) != 2 || s[1].Git.Conflict.AcknowledgeUnsaved != "tok-1" || s[0].ID == s[1].ID || s[0].Git.Conflict.AcknowledgeUnsaved != "" {
		t.Fatalf("resend %+v", s)
	}
}

func TestGitConflictMenuAndKeyParity(t *testing.T) {
	m, api := conflictModel(t)
	m.setFocus("git-cact:menu:" + cfPath)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	gitWriteSettle(t, m, cmd)
	text := menuText(m)
	for _, want := range []string{"Choose ours (feature/init) (o)", "Choose theirs (side) (t)", "Edit in Files (e)", "Mark resolved (m)", "Resolve as deleted…", "Restore a saved copy…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	for i, it := range m.menu {
		if strings.HasPrefix(it.Label, "Choose theirs") {
			m.menuIndex = i
		}
	}
	gitKey(t, m, "enter")
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	pointer := lastSent(t, api)
	m2, api2 := conflictModel(t)
	m2.setFocus("git:conflict:" + cfPath)
	gitKey(t, m2, "t")
	gitWriteSettle(t, m2, m2.activate(action{Kind: "git-conflict-confirm"}))
	key := lastSent(t, api2)
	if fmt.Sprint(*pointer.Git.Conflict) != fmt.Sprint(*key.Git.Conflict) {
		t.Fatalf("menu %+v vs key %+v", pointer.Git.Conflict, key.Git.Conflict)
	}
}

func TestGitConflictCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"viewer", "choose", "restore"} {
			m, _ := conflictModel(t)
			switch name {
			case "viewer":
				gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-view", ID: cfPath}))
			case "choose":
				gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-choose-theirs", ID: cfPath}))
			case "restore":
				gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-restore-menu", ID: cfPath}))
			}
			m.state.Light = light
			m.markDirty()
			file := filepath.Join(dir, fmt.Sprintf("144x70-git-conflict-%s-%s.ansi", name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGitJobThreadsHiddenFromNavigation(t *testing.T) {
	m := testModel()
	job := m.snapshot.Threads[0]
	job.ID, job.Title, job.Job = "job-1", "Resolve conflicts", &protocol.ThreadJob{Kind: "conflict_resolution"}
	job.Requests = []protocol.Request{{ID: "r", State: "pending", Title: "Q"}}
	before := m.attentionCount()
	m.snapshot.Threads = append(m.snapshot.Threads, job)
	open, closed := m.navigationSections()
	for _, r := range append(open, closed...) {
		if strings.Contains(fmt.Sprint(r), "job-1") {
			t.Fatal("job thread in navigation")
		}
	}
	if m.attentionCount() != before+1 {
		t.Fatal("job thread question not in the attention bell")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "attention-item", ID: "job-1", Value: "r"}))
	if m.state.Active == "job-1" {
		t.Fatal("attention opened the hidden job thread")
	}
	if active, ok := m.viewState().Host.Active(); !ok || active.Kind != "git" {
		t.Fatal("attention did not open the Git surface")
	}
	if m.jobFallbackThread("job-1") == "job-1" {
		t.Fatal("job thread kept active")
	}
	line := m.writerWaitLine(protocol.Thread{ID: "w", WriterWait: &protocol.WriterWait{HolderThreadID: "job-1"}}, m.colors())
	if line.action.Kind != "open" || line.action.Value != "git" {
		t.Fatalf("writer wait link %+v", line.action)
	}
	job.Closed = true
	m.snapshot.Threads[len(m.snapshot.Threads)-1] = job
	if m.nextOpenThread("") == "job-1" {
		t.Fatal("job thread chosen as next")
	}
}

func probeConflictMsgs(t *testing.T, m *Model, cmd tea.Cmd) []gitConflictFileMsg {
	t.Helper()
	var out []gitConflictFileMsg
	for _, msg := range pump(t, m, cmd).msgs {
		if f, ok := msg.(gitConflictFileMsg); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestGitConflictChooseNoSavedCopyConfirms(t *testing.T) {
	m, api := conflictModel(t)
	for _, msg := range probeConflictMsgs(t, m, m.activate(action{Kind: "git-conflict-choose-ours", ID: cfPath})) {
		if msg.version == "saved" {
			msg.err, msg.file = &protocol.Error{Code: "not_found"}, protocol.GitConflictFile{}
		}
		m.Update(msg)
	}
	if len(api.sent()) != 0 || !strings.Contains(menuText(m), "No saved copy of this stop holds it") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("no saved copy: %q\n%s", m.notice.text, menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.Side != "ours" || c.WorktreeToken != "tok-1" {
		t.Fatalf("choose %+v", c)
	}
}

func TestGitConflictViewerDropsStaleReads(t *testing.T) {
	m, api := conflictModel(t)
	first := probeConflictMsgs(t, m, m.activate(action{Kind: "git-conflict-view", ID: cfPath}))
	st := api.oper
	st.Conflicts = []protocol.GitConflict{{Path: cfPath, Kind: "UU", ConflictPin: "pin-1", WorktreeStat: "tok-B"}}
	api.oper = st
	api.conflictFiles[cfPath+"|working"] = protocol.GitConflictFile{Path: cfPath, Version: "working", Present: true, Kind: "file", Content: []byte("clean B\n"), ConflictPin: "pin-1", WorktreeToken: "tok-B"}
	var second []gitConflictFileMsg
	for _, msg := range pump(t, m, m.refreshGit()).msgs {
		if om, ok := msg.(gitOperationMsg); ok {
			_, c := m.Update(om)
			second = append(second, probeConflictMsgs(t, m, c)...)
		} else {
			m.Update(msg)
		}
	}
	for _, msg := range append(second, first...) {
		m.Update(msg)
	}
	text := gitSurfaceText(m)
	if !strings.Contains(text, "clean B") || strings.Contains(text, "<<<<<<< ours") {
		t.Fatalf("stale read shown:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if c := lastSent(t, api).Git.Conflict; c.WorktreeToken != "tok-B" || c.AcknowledgeMarkers != "" {
		t.Fatalf("resolve %+v", c)
	}
}

func TestGitConflictViewerMismatchRefuses(t *testing.T) {
	m, api := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-view", ID: cfPath}))
	// The viewer shows tok-1; a fresh read now says tok-2 while the row
	// still says tok-1.
	f := api.conflictFiles[cfPath+"|working"]
	f.WorktreeToken = "tok-2"
	api.conflictFiles[cfPath+"|working"] = f
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	if len(api.sent()) != 0 || len(m.menu) != 0 {
		t.Fatal("sent against a changed file")
	}
}

func TestGitConflictPrepNeverClobbersWrite(t *testing.T) {
	m, api := conflictModel(t)
	f := api.conflictFiles[cfPath+"|working"]
	f.HasMarkers = false
	api.conflictFiles[cfPath+"|working"] = f
	msgs := probeConflictMsgs(t, m, m.activate(action{Kind: "git-conflict-resolve", ID: cfPath}))
	key, _ := m.gitTarget()
	if reason, _ := m.gitWriteBlock(key, m.gitViews[key]); reason == "" {
		t.Fatal("pending conflict action does not block other writes")
	}
	if m.gitW.writes == nil {
		m.gitW.writes = map[string]*gitWriteState{}
	}
	m.gitW.writes[key] = &gitWriteState{cmd: protocol.Command{ID: "abort-1", Kind: protocol.GitKindOperationAbort}, running: true}
	for _, msg := range msgs {
		m.Update(msg)
	}
	if st := m.gitWriteFor(key); st.cmd.ID != "abort-1" || len(api.sent()) != 0 {
		t.Fatalf("clobbered: %s sent %d", st.cmd.ID, len(api.sent()))
	}
}

func TestGitConflictViewerEsc(t *testing.T) {
	m, _ := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-view", ID: cfPath}))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.gitCF.viewer != nil || m.focus != "git:conflict:"+cfPath {
		t.Fatalf("esc: viewer %v focus %q", m.gitCF.viewer != nil, m.focus)
	}
}

func TestGitConflictRestoreAfterResolve(t *testing.T) {
	m, api := conflictModel(t)
	st := api.oper
	st.Conflicts = nil
	api.oper = st
	gitWriteSettle(t, m, m.refreshGit())
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-restore-menu", ID: cfPath}))
	if !strings.Contains(menuText(m), "Restore the original conflict") {
		t.Fatalf("restore after resolve: %q\n%s", m.notice.text, menuText(m))
	}
	m.menu = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-restore", ID: cfPath, Value: "copy-0"}))
	if !strings.Contains(menuText(m), "Its saved conflict stages make the path unmerged again") {
		t.Fatalf("restore confirm:\n%s", menuText(m))
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-confirm"}))
	if c := lastSent(t, api).Git.Conflict; c.CopyID != "copy-0" || c.WorktreeToken != "tok-1" {
		t.Fatalf("restore %+v", c)
	}
}

func TestGitConflictEditPathMapping(t *testing.T) {
	if _, reason := gitConflictEditPath("", "/r", "a.go"); reason == "" {
		t.Fatal("edit without toplevel")
	}
	if p, reason := gitConflictEditPath("/r", "/r/sub", "sub/x/a.go"); reason != "" || p != "x/a.go" {
		t.Fatalf("mapping %q %q", p, reason)
	}
	if _, reason := gitConflictEditPath("/r", "/r/sub", "other/a.go"); !strings.Contains(reason, "outside this checkout") {
		t.Fatalf("outside %q", reason)
	}
	m, _ := conflictModel(t)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-conflict-edit", ID: cfPath}))
	if !strings.Contains(m.notice.text, "newer server") {
		t.Fatalf("edit notice %q", m.notice.text)
	}
}

func TestGitConflictRestoreLabelsAndViewerCaps(t *testing.T) {
	now := gitTestNow.Format("2006-01-02T15:04:05Z07:00")
	f := &protocol.GitConflictFile{Path: "p", CopyID: "a", CopyReason: "at_stop", CopyCreatedAt: now,
		Copies: []protocol.GitConflictCopyRef{{CopyID: "a"}, {CopyID: "b", Reason: "before_overwrite", CreatedAt: now}, {CopyID: "c", Reason: "before_overwrite", CreatedAt: gitTestNow.Add(-30e9).Format("2006-01-02T15:04:05Z07:00")}, {CopyID: "d", Reason: "before_job", CreatedAt: now}}}
	m, _ := conflictModel(t)
	m.showRestoreMenu(&gitConflictPrep{conflict: protocol.GitConflict{Path: "p"}, working: f})
	text := menuText(m)
	if !strings.Contains(text, "copy before the agent ran") || strings.Count(text, " · ") < 2 {
		t.Fatalf("labels:\n%s", text)
	}
	long := &protocol.GitConflictFile{Present: true, Content: []byte(strings.Repeat("x", 10000) + "\n<<<<<<< a\n")}
	lines := gitViewerLines(long)
	if len(lines) != 2 || !lines[1].marker || len([]rune(lines[0].text)) > gitViewerLineCells {
		t.Fatalf("lines %d %v", len(lines), lines[1])
	}
}

func TestGitJobLeaseCopy(t *testing.T) {
	m, _ := conflictModel(t)
	job := m.snapshot.Threads[0]
	job.ID, job.State, job.Checkout, job.Job = "job-2", "running", "/src/repo", &protocol.ThreadJob{Kind: "conflict_resolution"}
	m.snapshot.Threads = append(m.snapshot.Threads, job)
	key, _ := m.gitTarget()
	if reason, _ := m.gitWriteBlock(key, m.gitViews[key]); reason != gitJobLeaseCopy {
		t.Fatalf("lease copy %q", reason)
	}
}
