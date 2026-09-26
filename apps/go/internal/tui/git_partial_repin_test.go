package tui

import (
	"context"
	"errors"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// collect runs cmd and returns all produced messages (not delivered).
func collectMsgs(t *testing.T, m *Model, cmd tea.Cmd) []tea.Msg { return pump(t, m, cmd).msgs }

func deliver(t *testing.T, m *Model, msgs []tea.Msg, pick func(tea.Msg) bool) []tea.Msg {
	var out []tea.Msg
	for _, msg := range msgs {
		if pick(msg) {
			_, c := m.Update(msg)
			out = append(out, collectMsgs(t, m, c)...)
		}
	}
	return out
}

func isStatus(x tea.Msg) bool { _, ok := x.(gitStatusMsg); return ok }
func isDiff(x tea.Msg) bool   { _, ok := x.(gitViewerMsg); return ok }
func isHunks(x tea.Msg) bool  { _, ok := x.(gitHunksMsg); return ok }

type failer struct {
	*fakeGitPartial
	mu               sync.Mutex
	failDiff, failHk bool
}

func (f *failer) GitHunks(ctx context.Context, tg client.GitTarget, p, g string) (protocol.GitHunks, error) {
	f.mu.Lock()
	x := f.failHk
	f.mu.Unlock()
	if x {
		return protocol.GitHunks{}, errors.New("hunks boom")
	}
	return f.fakeGitPartial.GitHunks(ctx, tg, p, g)
}
func (f *failer) GitDiff(ctx context.Context, tg client.GitTarget, p, g string) (protocol.GitDiff, error) {
	f.mu.Lock()
	x := f.failDiff
	f.mu.Unlock()
	if x {
		return protocol.GitDiff{}, errors.New("diff boom")
	}
	return f.fakeGitPartial.GitDiff(ctx, tg, p, g)
}

func setPin(api *fakeGitPartial, pin string, drop bool) {
	s := partialStatus()
	if drop {
		s.Entries = s.Entries[:len(s.Entries)-2]
		s.Entries = append(s.Entries, partialStatus().Entries[len(partialStatus().Entries)-1])
	} else {
		s.Entries[len(s.Entries)-2].Pin = pin
	}
	api.fakeGit.status = s
}

func stuckProbe(t *testing.T, failDiff bool) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	f := &failer{fakeGitPartial: api}
	m.gitReads = f
	f.failDiff, f.failHk = failDiff, !failDiff
	setPin(api, "p2", false)
	gitWriteSettle(t, m, m.refreshGit())
	t.Logf("after failing reread: changed=%v pin=%s pending=%v", m.viewer.git.changed, m.viewer.git.entry.Pin, m.viewer.git.partial.pending != nil)
	f.failDiff, f.failHk = false, false
	gitWriteSettle(t, m, m.refreshGit()) // same pin p2
	n := len(api.sent())
	pkey(t, m, "S")
	t.Logf("after same-pin refresh: changed=%v pin=%s sentS=%v notice=%q", m.viewer.git.changed, m.viewer.git.entry.Pin, len(api.sent()) > n, m.notice.text)
	if m.viewer.git.changed {
		t.Errorf("STUCK (failDiff=%v): viewer stays changed after same-pin refresh; only reopen or a new pin recovers", failDiff)
	}
}

func TestGitPartialRepinStuckDiffErr(t *testing.T) { stuckProbe(t, true) }
func TestGitPartialRepinStuckHunkErr(t *testing.T) { stuckProbe(t, false) }

// Entry vanishes while the reread is in flight; late reads then clear changed.
func TestGitPartialRepinVanishMidReread(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	setPin(api, "p2", false)
	msgs := collectMsgs(t, m, m.refreshGit())
	reads := deliver(t, m, msgs, isStatus) // pending p2, reads produced
	setPin(api, "", true)                  // entry gone
	msgs2 := collectMsgs(t, m, m.refreshGit())
	deliver(t, m, msgs2, isStatus)
	t.Logf("after vanish status: changed=%v", m.viewer.git.changed)
	deliver(t, m, reads, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	g := m.viewer.git
	t.Logf("after late reads: changed=%v pin=%s active=%v", g.changed, g.entry.Pin, m.gitPartialActive())
	if !g.changed {
		n := len(api.sent())
		pkey(t, m, "S")
		t.Errorf("CONFIRMED: entry vanished from status but viewer re-pinned to p2 and cleared changed; S sent=%v", len(api.sent()) > n)
	}
}

// Older reread arrives after a newer status.
func TestGitPartialRepinLateOlderReread(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	setPin(api, "p2", false)
	r2 := deliver(t, m, collectMsgs(t, m, m.refreshGit()), isStatus)
	setPin(api, "p3", false)
	r3 := deliver(t, m, collectMsgs(t, m, m.refreshGit()), isStatus)
	deliver(t, m, r2, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	if !m.viewer.git.changed {
		t.Errorf("late p2 reads cleared changed; pin=%s", m.viewer.git.entry.Pin)
	}
	deliver(t, m, r3, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	t.Logf("after r3: changed=%v pin=%s", m.viewer.git.changed, m.viewer.git.entry.Pin)
	if m.viewer.git.changed || m.viewer.git.entry.Pin != "p3" {
		t.Errorf("did not adopt p3")
	}
	// hunks before diff ordering
	setPin(api, "p4", false)
	r4 := deliver(t, m, collectMsgs(t, m, m.refreshGit()), isStatus)
	deliver(t, m, r4, isHunks)
	if !m.viewer.git.changed {
		t.Error("adopted before diff")
	}
	deliver(t, m, r4, isDiff)
	if m.viewer.git.changed || m.viewer.git.entry.Pin != "p4" {
		t.Error("did not adopt p4")
	}
}

// Viewer closed and reopened mid-reread.
func TestGitPartialRepinReopenMidReread(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	setPin(api, "p2", false)
	r2 := deliver(t, m, collectMsgs(t, m, m.refreshGit()), isStatus)
	pkey(t, m, "esc")
	if m.viewer != nil {
		pkey(t, m, "esc")
	}
	h := partialHunks()
	h.Fingerprint = "fp-new"
	api.hmu.Lock()
	api.hunks["unstaged"] = h
	api.hmu.Unlock()
	open := collectMsgs(t, m, m.activate(action{Kind: "git-open", Value: "unstaged", ID: "main.go"}))
	deliver(t, m, r2, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	ps := m.viewer.git.partial
	t.Logf("after stale reads into new viewer: hunks=%v loading=%v", ps.hunks != nil, ps.loading)
	if ps.hunks != nil {
		t.Errorf("stale reread applied to reopened viewer")
	}
	deliver(t, m, open, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	t.Logf("fp=%s pin=%s changed=%v", ps.hunks.Fingerprint, m.viewer.git.entry.Pin, m.viewer.git.changed)
}

// Partial write in flight, status arrives, write reply, reads.
func TestGitPartialRepinWriteDuringPending(t *testing.T) {
	m, api := partialModel(t, 120, 50, "unstaged", "main.go")
	pkey(t, m, "down")
	pkey(t, m, "space")
	_, c := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	wmsgs := collectMsgs(t, m, c)
	setPin(api, "p2", false)
	reads := deliver(t, m, collectMsgs(t, m, m.refreshGit()), isStatus)
	n := len(api.sent())
	pkey(t, m, "S")
	pkey(t, m, "s")
	if len(api.sent()) != n {
		t.Errorf("write sent while pending/in-flight")
	}
	more := deliver(t, m, wmsgs, func(x tea.Msg) bool { _, ok := x.(gitWriteMsg); return ok })
	deliver(t, m, reads, func(x tea.Msg) bool { return isDiff(x) || isHunks(x) })
	t.Logf("changed=%v pin=%s loading=%v", m.viewer.git.changed, m.viewer.git.entry.Pin, m.viewer.git.partial.loading)
	deliver(t, m, more, func(x tea.Msg) bool { return isHunks(x) })
	t.Logf("after refetch: changed=%v pin=%s loading=%v active=%v", m.viewer.git.changed, m.viewer.git.entry.Pin, m.viewer.git.partial.loading, m.gitPartialActive())
}
