package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func (g *fakeGit) GitOperation(_ context.Context, t client.GitTarget) (protocol.GitOperationState, error) {
	g.record("operation:" + targetName(t))
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.oper, g.err
}

func (g *fakeGit) GitIntegratePreview(_ context.Context, t client.GitTarget, kind, ref string) (protocol.GitIntegratePreview, error) {
	g.record("preview:" + kind + ":" + ref)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.preview, g.err
}

func opModel(t *testing.T) (*Model, *fakeGitWriter) {
	t.Helper()
	return opModelSize(t, 144, 70)
}

func opModelSize(t *testing.T, w, h int) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := refModel(t, w, h)
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "git-operations")
	return m, api
}

// rebaseStop is a rebase stopped at step 2/5 with one binary conflict.
func rebaseStop() protocol.GitOperationState {
	return protocol.GitOperationState{
		Kind: "rebase", Source: "external", Branch: "feature/init", HeadOid: strings.Repeat("d", 40), OrigHead: strings.Repeat("0", 40),
		Target:  &protocol.GitOperationCommit{Oid: strings.Repeat("e", 40), Label: "origin/main", Subject: "Upstream work"},
		Current: &protocol.GitOperationCommit{Oid: strings.Repeat("c", 40), Subject: "Local \x1b[31mwork"},
		Step:    2, Steps: 5,
		Conflicts: []protocol.GitConflict{{Path: "img/logo.png", Kind: "UU", Binary: true}, {Path: "gone.go", Kind: "DU"}},
		Sides:     protocol.GitConflictSides{Ours: "origin/main + rebased", Theirs: "cccccccc Local work"},
		Can: protocol.GitOperationActions{Continue: protocol.GitOperationAction{Reason: "Resolve and stage 2 conflicts first"},
			Skip: protocol.GitOperationAction{Allowed: true}, Abort: protocol.GitOperationAction{Allowed: true}},
		WorktreeFingerprint: "wt-fp", StagedFingerprint: "st-fp", UnmergedFingerprint: "um-fp",
		DiscardsOnAbort: []string{"notes.txt", "a/b.go"}, DiscardsOnAbortFingerprint: "da-fp",
		BackupMissingOnAbort: []string{"huge.bin"}, BackupMissingOnAbortFingerprint: "bm-fp",
		DiscardsOnSkip: []string{"skip.txt"}, DiscardsOnSkipFingerprint: "ds-fp",
	}
}

func opStopped(t *testing.T, st protocol.GitOperationState) (*Model, *fakeGitWriter) {
	t.Helper()
	m, api := opModel(t)
	api.status.Operation = st.Kind
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	return m, api
}

func TestGitMergePreviewConfirm(t *testing.T) {
	m, api := opModel(t)
	api.preview = protocol.GitIntegratePreview{Kind: "merge", Source: "branch", Branch: "feature/init", HeadOid: api.status.HeadOid,
		TargetRef: "refs/heads/side", TargetOid: refTip, TargetLabel: "side", TargetSubject: "Side work", FastForward: true, MergeFF: "ff"}
	m.setFocus("git:branch:refs/heads/side")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModShift})
	gitWriteSettle(t, m, cmd)
	if !strings.Contains(menuText(m), "Merge side into feature/init…") || !strings.Contains(menuText(m), "Rebase feature/init onto side…") {
		t.Fatalf("branch menu:\n%s", menuText(m))
	}
	m.closeContextMenu()
	gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate", Value: "merge", ID: "refs/heads/side"}))
	text := menuText(m)
	if !strings.Contains(text, "Merge side 5ide000 into feature/init?") || !strings.Contains(text, "Fast-forward: feature/init moves to 5ide000") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("merge dialog:\n%s", text)
	}
	if api.count("preview:merge:refs/heads/side") != 1 || len(api.sent()) != 0 {
		t.Fatal("preview not read or sent early")
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	s := lastSent(t, api)
	in := s.Git.Integrate
	if s.Kind != protocol.GitKindMerge || in.TargetOid != refTip || in.TargetRef != "refs/heads/side" || in.ExpectedHead != api.status.HeadOid || in.ExpectedBranch != "feature/init" || in.AcknowledgePublished {
		t.Fatalf("merge %+v", in)
	}
}

func TestGitRebasePreviewPublishedAndBlocked(t *testing.T) {
	m, api := opModel(t)
	api.preview = protocol.GitIntegratePreview{Kind: "rebase", Source: "branch", Branch: "feature/init", HeadOid: api.status.HeadOid,
		TargetRef: "refs/heads/side", TargetOid: refTip, TargetLabel: "side", ReplayCount: 3, Published: true}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate", Value: "rebase", ID: "refs/heads/side"}))
	text := menuText(m)
	if !strings.Contains(text, "Replays 3 commits") || !strings.Contains(text, "rebasing rewrites published history") || !strings.Contains(text, "Rebase published commits") {
		t.Fatalf("rebase dialog:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate-confirm"}))
	in := lastSent(t, api).Git.Integrate
	if !in.AcknowledgePublished || in.ExpectedReplayCount != 3 {
		t.Fatalf("rebase %+v", in)
	}
	m.menu = nil
	api.preview = protocol.GitIntegratePreview{Kind: "merge", Branch: "feature/init", HeadOid: api.status.HeadOid, TargetOid: refTip, TargetLabel: "side",
		Blocked: "dirty_tree", BlockedMessage: "2 changed \x1b[2Jfiles"}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate", Value: "merge", ID: "refs/heads/side"}))
	text = menuText(m)
	if !strings.Contains(text, "Not possible now · Commit or discard changes first") || strings.Contains(text, "\x1b") {
		t.Fatalf("blocked dialog:\n%q", text)
	}
	for _, item := range m.menu {
		if item.Action.Kind == "git-integrate-confirm" {
			t.Fatal("blocked preview offered a confirm")
		}
	}
}

func TestGitPreviewStaleDropped(t *testing.T) {
	m, api := opModel(t)
	api.preview = protocol.GitIntegratePreview{Kind: "merge", Branch: "feature/init", HeadOid: api.status.HeadOid, TargetOid: refTip, TargetLabel: "side", FastForward: true}
	key, target := m.gitTarget()
	first := m.startGitPreview(key, target, "merge", "refs/heads/side")
	m.startGitPreview(key, target, "rebase", "refs/heads/side")
	for _, msg := range pump(t, m, first).msgs {
		if pm, ok := msg.(gitPreviewMsg); ok {
			m.Update(pm)
		}
	}
	if len(m.menu) != 0 {
		t.Fatalf("stale preview opened:\n%s", menuText(m))
	}
}

func TestGitOperationPanel(t *testing.T) {
	m, api := opStopped(t, rebaseStop())
	if api.count("operation:") == 0 {
		t.Fatal("operation not read")
	}
	text := gitSurfaceText(m)
	for _, want := range []string{"Rebase in progress · step 2/5", "Started: in a terminal", "Onto: origin/main eeeeeee Upstream work", "Current: ccccccc Local work",
		"UNMERGED 2", "UU img/logo.png binary", "DU gone.go", "Continue unavailable · Resolve and stage 2 conflicts first", "Skip commit…", "Abort rebase…"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "Read-only here") {
		t.Fatalf("panel:\n%q", text)
	}
	st := rebaseStop()
	st.StopReason = "edit"
	st.Conflicts = nil
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if !strings.Contains(gitSurfaceText(m), "Stopped for an edit · continue in a terminal") {
		t.Fatalf("stop reason:\n%s", gitSurfaceText(m))
	}
}

func TestGitAbortDialogSendsShownFingerprints(t *testing.T) {
	st := rebaseStop()
	m, api := opStopped(t, st)
	m.viewState().DetailScroll = 0
	h, ok := findHit(m.measure(), "git:op-abort")
	if !ok {
		t.Fatalf("no abort control:\n%s", gitSurfaceText(m))
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	text := menuText(m)
	for _, want := range []string{"Abort the rebase and return feature/init to 0000000?", "Also resets changes to:\n  notes.txt\n  a/b.go", "Not backed up (overwritten without a copy):\n  huge.bin"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if m.menu[m.menuIndex].Label != "Cancel" || len(api.sent()) != 0 {
		t.Fatal("abort not defaulting to Cancel")
	}
	m.menuIndex = len(m.menu) - 1
	gitKey(t, m, "enter")
	o := lastSent(t, api).Git.Operation
	if o.DiscardsFingerprint != "da-fp" || o.AcknowledgeBackupMissing != "bm-fp" || o.WorktreeFingerprint != "wt-fp" || o.ExpectedHead != st.HeadOid || !o.Confirmed || o.AcknowledgeDropped != "" {
		t.Fatalf("abort %+v", o)
	}
}

func TestGitAbortSequenceDropsCommits(t *testing.T) {
	st := rebaseStop()
	st.Kind = "cherry-pick"
	st.AbortDropsCount, st.AbortDropsIncomplete, st.AbortDropsFingerprint = 3, true, "drop-fp"
	st.AbortDropsCommits = []protocol.GitOperationCommit{{Oid: strings.Repeat("1", 40), Subject: "Pick one"}}
	st.DiscardsOnAbort, st.BackupMissingOnAbort = nil, nil
	m, api := opStopped(t, st)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	if text := menuText(m); !strings.Contains(text, "Removes 3 commits the cherry-pick already made:\n  1111111 Pick one\n  and 2 more") {
		t.Fatalf("drops:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
	if o := lastSent(t, api).Git.Operation; o.AcknowledgeDropped != "drop-fp" || o.DiscardsFingerprint != "" {
		t.Fatalf("abort %+v", o)
	}
}

func TestGitSkipAndContinueDialogs(t *testing.T) {
	st := rebaseStop()
	m, api := opStopped(t, st)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-skip"}))
	if text := menuText(m); !strings.Contains(text, "Skip ccccccc Local work?") || !strings.Contains(text, "  skip.txt") || m.menu[m.menuIndex].Label != "Cancel" {
		t.Fatalf("skip:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationSkip}))
	if o := lastSent(t, api).Git.Operation; o.SkipOid != st.Current.Oid || o.DiscardsFingerprint != "ds-fp" || o.ExpectedStep != 2 {
		t.Fatalf("skip %+v", o)
	}
	m.menu = nil
	delete(m.gitW.writes, m.gitW.draftKey)
	st.Can.Continue = protocol.GitOperationAction{Allowed: true}
	st.Conflicts = nil
	st.MarkerPaths, st.MarkersFingerprint, st.MarkersIncomplete = []string{"x.go"}, "mk-fp", true
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
	text := menuText(m)
	if !strings.Contains(text, "Staged files still contain conflict markers:\n  x.go") || !strings.Contains(text, "Not every staged file could be checked") || !strings.Contains(text, "Continue with conflict markers") {
		t.Fatalf("continue:\n%s", text)
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}))
	o := lastSent(t, api).Git.Operation
	if o.MarkersFingerprint != "mk-fp" || !o.AcknowledgeMarkersIncomplete || o.StagedFingerprint != "st-fp" || o.UnmergedFingerprint != "um-fp" {
		t.Fatalf("continue %+v", o)
	}
}

func TestGitOperationStaleStateDropsDialog(t *testing.T) {
	st := rebaseStop()
	m, api := opStopped(t, st)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	st.Step = 3
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
	if len(api.sent()) != 0 || len(m.menu) != 0 {
		t.Fatalf("stale abort sent or dialog kept: %v", api.sent())
	}
}

func TestGitOperationRetryAndResults(t *testing.T) {
	st := rebaseStop()
	st.Can.Continue = protocol.GitOperationAction{Allowed: true}
	m, api := opStopped(t, st)
	api.reply = func(cmd protocol.Command) (protocol.Receipt, error) {
		return protocol.Receipt{}, errors.New("reset by peer")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-continue"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}))
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "nothing_to_commit", Operation: &protocol.GitOperationResult{Kind: "rebase", Outcome: "unchanged"}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-retry"}))
	s := api.sent()
	if len(s) != 2 || s[0].ID != s[1].ID {
		t.Fatalf("retry %+v", s)
	}
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Nothing to commit for this step · Skip it") {
		t.Fatalf("nothing_to_commit:\n%s", text)
	}
	m.menu = nil
	m.viewState().DetailScroll = 0
	if _, ok := findHit(m.measure(), "git:result-skip"); !ok {
		t.Fatal("no Skip offered")
	}
	refReply(api, protocol.GitResult{State: protocol.GitStateSucceeded, Operation: &protocol.GitOperationResult{Kind: "rebase", Outcome: "aborted",
		Backup: &protocol.GitOperationBackup{Oid: strings.Repeat("b", 40), IndexOid: strings.Repeat("9", 40), Paths: []string{"a", "b"}}}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
	text = gitSurfaceText(m)
	if !strings.Contains(text, "Aborted the rebase") || !strings.Contains(text, "Backup bbbbbbb · 2 files copied before overwriting") || !strings.Contains(text, "git show "+strings.Repeat("b", 40)+":<path> > <path>") {
		t.Fatalf("abort result:\n%s", text)
	}
}

func TestGitOperationCopyForCodes(t *testing.T) {
	for _, code := range []string{"dirty_tree", "range_has_merges", "published_commit", "ff_only_configured", "discards_unacknowledged", "drops_unacknowledged",
		"markers_unacknowledged", "markers_incomplete", "backup_incomplete", "not_supported", "timeout", "abort_incomplete", "stopped_conflicts", "nothing_to_commit", "stale_operation", "no_operation", "stale_range"} {
		if c := gitRefCopy(protocol.GitKindRebase, code); c == gitErrorCopy("zzz") || c == "" {
			t.Fatalf("%s has no copy", code)
		}
	}
}

func TestGitWriterWaitNamesOperation(t *testing.T) {
	m := testModel()
	line := m.writerWaitLine(protocol.Thread{ID: "w", WriterWait: &protocol.WriterWait{HolderOperation: "rebase"}}, m.colors())
	if !strings.Contains(line.text, "Waiting for the rebase in this checkout") { // no checkout path known
		t.Fatalf("wait line %q", line.text)
	}
}

func TestGitDivergedOffersMergeRebase(t *testing.T) {
	m, api := opModel(t)
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "diverged", Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "diverged", To: strings.Repeat("f", 40)}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
	text := gitSurfaceText(m)
	if !strings.Contains(text, "Merge origin/feature/init…") || !strings.Contains(text, "Rebase onto origin/feature/init…") {
		t.Fatalf("diverged:\n%s", text)
	}
	m.viewState().DetailScroll = 0
	h, _ := findHit(m.measure(), "git:pull-rebase")
	_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	gitWriteSettle(t, m, cmd)
	// Without the log's full upstream ref, the fetched commit itself.
	if api.count("preview:rebase:"+strings.Repeat("f", 40)) != 1 {
		t.Fatalf("preview calls %v", api.calls)
	}
}

func TestGitOperationCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, name := range []string{"preview", "panel", "abort"} {
			var m *Model
			switch name {
			case "preview":
				var api *fakeGitWriter
				m, api = opModel(t)
				api.preview = protocol.GitIntegratePreview{Kind: "rebase", Branch: "feature/init", HeadOid: api.status.HeadOid, TargetRef: "refs/heads/side", TargetOid: refTip, TargetLabel: "side", TargetSubject: "Side work", ReplayCount: 3, Published: true}
				gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate", Value: "rebase", ID: "refs/heads/side"}))
			case "panel":
				m, _ = opStopped(t, rebaseStop())
				m.viewState().DetailScroll = 0
				m.hover = "git:conflict:img/logo.png"
			case "abort":
				m, _ = opStopped(t, rebaseStop())
				gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
			}
			m.state.Light = light
			m.markDirty()
			rows := m.render().rows
			for i, r := range rows {
				if w := ansi.StringWidth(r); w != m.width {
					t.Fatalf("%s row %d is %d cells", name, i, w)
				}
			}
			file := filepath.Join(dir, fmt.Sprintf("144x70-git-op-%s-%s.ansi", name, map[bool]string{false: "dark", true: "light"}[light]))
			if err := os.WriteFile(file, []byte(strings.Join(rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGitDivergedUsesFullUpstreamRef(t *testing.T) {
	m, api := opModel(t)
	api.log.Upstream = "refs/remotes/up/feature/init"
	refReply(api, protocol.GitResult{State: protocol.GitStateFailed, Code: "diverged", Fetch: &protocol.GitFetchResult{Remote: "origin", State: "succeeded"}, Integration: &protocol.GitIntegration{State: "diverged", To: strings.Repeat("f", 40)}})
	gitWriteSettle(t, m, m.activate(action{Kind: "git-pull"}))
	m.menu = nil
	gitWriteSettle(t, m, m.activate(action{Kind: "git-integrate", Value: "merge", ID: api.log.Upstream}))
	h := false
	for _, b := range m.gitSurfaceBlocks() {
		if b.git != nil && b.git.key == "git:pull-merge" && b.git.action.ID == "refs/remotes/up/feature/init" {
			h = true
		}
	}
	if !h {
		t.Fatal("merge entry does not use the full upstream ref")
	}
}

// reviewModel opens Abort for a stop with 65 long, distinct paths.
func reviewModel(t *testing.T, w, h int) (*Model, *fakeGitWriter, []string) {
	t.Helper()
	m, api := opModelSize(t, w, h)
	if m.singleColumn() {
		m.selectColumn(shell.RightRegion)
	}
	if w < 100 {
		// At 80x24 the fixture's pending question and queue leave the
		// stacked surface one row; a thread without them leaves it room.
		i := activeThreadIndex(m)
		m.snapshot.Threads[i].Requests, m.snapshot.Threads[i].Queue = nil, nil
	}
	st := rebaseStop()
	var paths []string
	for i := range 65 {
		paths = append(paths, fmt.Sprintf("src/very/long/shared/prefix/for/every/path/in/this/list/file-%02d.go", i))
	}
	st.DiscardsOnAbort = paths
	api.status.Operation = st.Kind
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	return m, api, paths
}

func TestGitReviewPanelRequiresSeeingAllItems(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		m, api, paths := reviewModel(t, size[0], size[1])
		if len(m.menu) != 0 || m.gitO.review == nil || m.focus != "git:review-cancel" {
			t.Fatalf("%v: review not opened (menu %d focus %q notice %q single %v small %v visible %v)", size, len(m.menu), m.focus, m.notice.text, m.singleColumn(), m.terminalTooSmall(), m.gitVisible())
		}
		frames := screenText(m)
		if _, ok := findHit(m.measure(), "git:review-confirm"); ok {
			t.Fatalf("%v: confirm offered before review", size)
		}
		if !strings.Contains(frames, "Scroll to review all 66 items") && !strings.Contains(gitSurfaceText(m), "Scroll to review all 66 items") {
			t.Fatalf("%v: no scroll hint seen=%v\n%s", size, m.gitO.review.seen, gitSurfaceText(m))
		}
		gitWriteSettle(t, m, m.activate(action{Kind: "git-op-confirm", Value: protocol.GitKindOperationAbort}))
		if len(api.sent()) != 0 || !strings.Contains(m.notice.text, "Scroll to review all 66 items") {
			t.Fatalf("%v: confirmed unseen list: %q", size, m.notice.text)
		}
		for i := 0; i < 200 && !m.gitO.review.seen; i++ {
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			gitWriteSettle(t, m, cmd)
			frames += "\n" + screenText(m)
		}
		if !m.gitO.review.seen {
			t.Fatalf("%v: never seen through", size)
		}
		for _, p := range paths {
			name := p[strings.LastIndex(p, "/")+1:]
			if !strings.Contains(frames, name) {
				t.Fatalf("%v: %s never rendered", size, name)
			}
		}
		// Scroll a little more so the confirm is on screen, then use it.
		for range 3 {
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
			gitWriteSettle(t, m, cmd)
		}
		h, ok := findHit(m.measure(), "git:review-confirm")
		if !ok {
			t.Fatalf("%v: confirm not offered after review:\n%s", size, screenText(m))
		}
		_, cmd := m.Update(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
		gitWriteSettle(t, m, cmd)
		if o := lastSent(t, api).Git.Operation; o.DiscardsFingerprint != "da-fp" || o.AcknowledgeBackupMissing != "bm-fp" {
			t.Fatalf("%v: abort %+v", size, o)
		}
	}
}

func TestGitReviewCancelAndStaleFingerprint(t *testing.T) {
	m, api, _ := reviewModel(t, 120, 40)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-review-cancel"}))
	if m.gitO.review != nil || m.gitO.op != nil || len(api.sent()) != 0 {
		t.Fatal("cancel kept the review")
	}
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	st := api.oper
	st.DiscardsOnAbortFingerprint = "changed"
	api.oper = st
	gitWriteSettle(t, m, m.activate(action{Kind: "git-refresh"}))
	if m.gitO.review != nil || m.gitO.op != nil {
		t.Fatal("review kept after its list changed")
	}
}

func TestGitDialogPathsLeftTruncated(t *testing.T) {
	st := rebaseStop()
	long := "src/" + strings.Repeat("deep/", 15) + "keep-this-name.go"
	st.DiscardsOnAbort = []string{long}
	st.BackupMissingOnAbort = nil
	st.Branch = "feature/" + strings.Repeat("x", 80)
	m, _ := opStopped(t, st)
	gitWriteSettle(t, m, m.activate(action{Kind: "git-op-abort"}))
	if m.gitO.review == nil {
		// A path too long to show whole goes to the review panel, wrapped.
		t.Fatalf("long path kept in the menu:\n%s", menuText(m))
	}
	if !strings.Contains(gitSurfaceText(m), "keep-this-name.go") || !strings.Contains(gitSurfaceText(m), "to 0000000?") {
		t.Fatalf("review:\n%s", gitSurfaceText(m))
	}
	if got := truncatePathLeft(long, gitDialogPathWidth); !strings.HasSuffix(got, "keep-this-name.go") {
		t.Fatalf("left truncation %q", got)
	}
}

func TestGitDestructiveStylingAndBadges(t *testing.T) {
	if !menuItemDestructive(menuItem{Action: action{Kind: "git-integrate-confirm", Value: "published"}}) ||
		!menuItemDestructive(menuItem{Action: action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue, ID: "markers"}}) ||
		menuItemDestructive(menuItem{Action: action{Kind: "git-op-confirm", Value: protocol.GitKindOperationContinue}}) {
		t.Fatal("destructive styling")
	}
	if b := gitConflictBadge("\x1b[31mUU"); strings.Contains(b, "\x1b") || len(b) != 2 {
		t.Fatalf("badge %q", b)
	}
	if txt := gitExtraRowText(&gitRow{kind: "conflict", conflict: protocol.GitConflict{Kind: "\x1b]0;x\aUU"}, text: "a"}); strings.Contains(txt, "\x1b") {
		t.Fatalf("row text %q", txt)
	}
}

func TestGitWriterWaitArticleAndAction(t *testing.T) {
	m := testModel()
	line := m.writerWaitLine(protocol.Thread{ID: "w", Checkout: "/home/u/src/repo", WriterWait: &protocol.WriterWait{HolderOperation: "am"}}, m.colors())
	if !strings.Contains(line.text, "Waiting for an am in src/repo") || line.action.Kind != "open" || line.action.Value != "git" {
		t.Fatalf("wait line %q %+v", line.text, line.action)
	}
}

func TestGitReviewSeenByWheel(t *testing.T) {
	m, _, _ := reviewModel(t, 120, 40)
	f := m.measure()
	for i := 0; i < 300 && !m.gitO.review.seen; i++ {
		_, cmd := m.Update(tea.MouseWheelMsg{X: f.detail.X + 2, Y: f.detail.Y + 2, Button: tea.MouseWheelDown})
		gitWriteSettle(t, m, cmd)
	}
	if !m.gitO.review.seen {
		t.Fatal("wheel never reached the end of the review")
	}
}
