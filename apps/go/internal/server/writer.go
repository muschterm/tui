package server

import (
	"cmp"
	"slices"
	"strings"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Writer coordination (docs/design/workspaces.md): at most one thread per
// checkout runs a turn at a time. No read-only policy is enforced yet, so every
// turn is treated as write-capable.
//
// The lease is derived, never persisted: a thread holds its checkout's lease
// while its State is running or waiting, or while its ACP runner has claimed
// the queue head and its dispatch has not yet returned (e.claiming). The claim
// outlives a deleted thread until the runner's dispatch returns, so a deleted
// holder's adapter cannot overlap the next writer. Deriving the lease from
// state means every way a turn ends releases it without a separate release
// call, and a restart holds no lease until a turn runs in this process. All
// checks run under e.mu in the same critical section as the transition to
// running, so two turns can never start on one checkout.
//
// A thread with an empty Checkout has no key to coordinate on and is never
// blocked.
//
// Waiters are derived too: every writer-eligible thread (idle, queued, open
// and ungated) is a candidate. Candidates are ordered by
// e.waitSince, a sequence assigned the first time a thread is observed as a
// candidate and dropped when it stops being one or starts; the snapshot order
// breaks ties. A holder that ends a turn with more queued work gets a fresh
// sequence and so yields to earlier waiters. Nothing has to register a waiter,
// so rejected commands, agent re-probes and restarts cannot strand one.
//
// A running Git write (git_write.go) also holds the lease, as a non-thread
// holder keyed by its repository toplevel and matched by path overlap, and a
// Git write is refused outright while a thread holds an overlapping lease.
// Git writes that change neither the index nor the working tree (fetch, push,
// branch creation; ADR 0021) take only the repository's Git slot, not the
// lease, so they neither wait for nor block agent turns.
//
// A merge, rebase, cherry-pick, revert or am in progress in the checkout's
// repository is a third holder (git_operation.go, ADR 0023), whether it was
// started here or outside the application: it is derived from a stat of the
// operation markers in the worktree's Git directory, so it ends as soon as
// the operation does, and waiters are re-evaluated once a second while any
// waits on one. Bisect is not a holder. The reserved JobThreadID of a
// recorded operation is exempt, so its resolution job can work there.
//
// Known limits: between threads the lease key is the registered project path,
// so nested projects in one working tree (/repo and /repo/sub) are not
// coordinated with each other; a
// fixture Resume that continues its turn in place is refused (checkout_busy)
// rather than queued.

func activeTurn(t *protocol.Thread) bool {
	return t.State == "running" || t.State == "waiting"
}

// writerHolder reports the thread other than except that holds key's lease.
func (e *engine) writerHolder(s *protocol.Snapshot, key, except string) string {
	if key == "" {
		return ""
	}
	for i := range s.Threads {
		t := &s.Threads[i]
		if t.ID != except && t.Checkout == key && activeTurn(t) {
			return t.ID
		}
	}
	for id, claimed := range e.claiming {
		if id != except && claimed == key {
			return id
		}
	}
	// A running Git write holds every checkout overlapping its repository
	// toplevel, so a nested project in the same working tree waits too.
	if id := e.gitWriteHolderLocked(key); id != "" {
		return gitHolderPrefix + id
	}
	return e.operationHolderLocked(s, key, except)
}

// writerEligible reports whether t has queued work that only the lease blocks.
// Agent readiness is deliberately not required: a waiter whose agent is not
// ready stays in line and, at the head of a free checkout, is woken so claim
// records the ordinary not-ready failure instead of stranding it silently.
func writerEligible(t *protocol.Thread) bool {
	return t != nil && t.Checkout != "" && !t.Closed && !t.NeedsResume && t.State == "idle" && len(t.Queue) > 0
}

// waitKey returns t's place in line, assigning the next sequence on first sight.
func (e *engine) waitKey(t *protocol.Thread) uint64 {
	if e.waitSince == nil {
		e.waitSince = map[string]uint64{}
	}
	seq, ok := e.waitSince[t.ID]
	if !ok {
		e.waitSeq++
		seq = e.waitSeq
		e.waitSince[t.ID] = seq
	}
	return seq
}

// candidates lists key's eligible threads in wait order.
func (e *engine) candidates(s *protocol.Snapshot, key string) []*protocol.Thread {
	var list []*protocol.Thread
	for i := range s.Threads {
		// A thread whose managed worktree is unavailable keeps its queue
		// and waits outside the line until the worktree is back (ADR 0024).
		if t := &s.Threads[i]; t.Checkout == key && writerEligible(t) && worktreeUnavailable(s, t) == nil {
			e.waitKey(t)
			list = append(list, t)
		}
	}
	slices.SortStableFunc(list, func(a, b *protocol.Thread) int {
		return cmp.Compare(e.waitSince[a.ID], e.waitSince[b.ID])
	})
	return list
}

// writerBlocked reports whether t must wait: another thread holds the lease or
// an earlier candidate is in line. It records t's place when it is a candidate.
func (e *engine) writerBlocked(s *protocol.Snapshot, t *protocol.Thread) bool {
	key := t.Checkout
	if key == "" {
		return false
	}
	if e.writerHolder(s, key, t.ID) != "" {
		if writerEligible(t) {
			e.waitKey(t)
		}
		return true
	}
	mine, eligible := uint64(0), writerEligible(t)
	if eligible {
		mine = e.waitKey(t)
	}
	// While an operation reserves the checkout, only its resolution job may
	// write there, so the waiters ahead of it do not delay it.
	if e.jobExemptLocked(s, key, t.ID) {
		return false
	}
	for _, c := range e.candidates(s, key) {
		if c.ID == t.ID {
			break
		}
		if !eligible || e.waitSince[c.ID] <= mine {
			return true
		}
	}
	return false
}

// acquireWriter takes t's checkout lease in s. The caller must start the turn
// in the same critical section when it returns true.
func (e *engine) acquireWriter(s *protocol.Snapshot, t *protocol.Thread) bool {
	if e.writerBlocked(s, t) {
		return false
	}
	delete(e.waitSince, t.ID)
	return true
}

// rebalanceWritersLocked forgets stale wait positions, wakes the head
// candidate of every free checkout and recomputes each thread's WriterWait. It
// returns whether e.snap changed; the caller persists and publishes.
func (e *engine) rebalanceWritersLocked() bool {
	s := &e.snap
	for id := range e.waitSince {
		if !writerEligible(threadByID(s, id)) {
			delete(e.waitSince, id)
		}
	}
	changed := false
	byKey := map[string][]*protocol.Thread{}
	for i := range s.Threads {
		key := s.Threads[i].Checkout
		if _, seen := byKey[key]; key != "" && !seen {
			byKey[key] = e.candidates(s, key)
		}
	}
	for key, list := range byKey {
		if len(list) == 0 || e.stopping || e.flushErr != nil {
			continue
		}
		head := list[0]
		// An operation's resolution job (JobThreadID) is exempt from that
		// operation's hold and goes ahead of the waiters it holds back.
		if holder := e.writerHolder(s, key, ""); holder != "" {
			if _, op := operationWait(holder); !op {
				continue
			}
			head = nil
			for _, c := range list {
				if e.writerHolder(s, key, c.ID) == "" {
					head = c
					break
				}
			}
			if head == nil {
				continue
			}
		}
		if agent.IsACP(head.AgentID) {
			// The runner's claim acquires; this candidate is first in line.
			e.ensureRunLocked(head.ID)
		} else if e.acquireWriter(s, head) {
			startFixturePrompt(head)
			changed = true
		}
	}
	operationWaits := false
	for i := range s.Threads {
		t := &s.Threads[i]
		var wait *protocol.WriterWait
		if writerEligible(t) {
			if holder := e.writerHolder(s, t.Checkout, t.ID); holder != "" {
				wait = &protocol.WriterWait{HolderThreadID: holder}
				if id, ok := strings.CutPrefix(holder, gitHolderPrefix); ok {
					wait = &protocol.WriterWait{HolderGitCommandID: id}
				} else if w, ok := operationWait(holder); ok {
					wait, operationWaits = w, true
				}
				for _, c := range e.candidates(s, t.Checkout) {
					wait.Position++
					if c.ID == t.ID {
						break
					}
				}
			}
		}
		if !equalWait(t.WriterWait, wait) {
			t.WriterWait = wait
			changed = true
		}
	}
	if operationWaits {
		e.ensureOperationPollLocked()
	}
	return changed
}

// rebalanceWritersAndFlushLocked commits any wait-state change immediately.
func (e *engine) rebalanceWritersAndFlushLocked() {
	if e.rebalanceWritersLocked() {
		e.snap.Revision++
		e.flushLocked()
	}
}

func equalWait(a, b *protocol.WriterWait) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// clearWriterWaits drops persisted wait markers; they are derived on load.
func clearWriterWaits(s *protocol.Snapshot) {
	for i := range s.Threads {
		s.Threads[i].WriterWait = nil
	}
}

// demoteConcurrentWriters keeps at most one active turn per checkout after
// recovery. Continuation after restart could otherwise revive two turns on one
// checkout from state written before writer coordination existed.
func demoteConcurrentWriters(s *protocol.Snapshot) {
	held := map[string]bool{}
	for i := range s.Threads {
		t := &s.Threads[i]
		if t.Checkout == "" || !activeTurn(t) {
			continue
		}
		if held[t.Checkout] {
			t.State, t.NeedsResume = "interrupted", true
			for j := range t.Children {
				if t.Children[j].State == "running" {
					t.Children[j].State = "interrupted"
				}
			}
			continue
		}
		held[t.Checkout] = true
	}
}

// startQueuedFixture starts an idle fixture thread's queue head in s when the
// checkout lease is available, and otherwise leaves it queued as a waiter.
func (e *engine) startQueuedFixture(s *protocol.Snapshot, t *protocol.Thread) {
	if t == nil || agent.IsACP(t.AgentID) || t.Closed || t.NeedsResume || t.State != "idle" || len(t.Queue) == 0 {
		return
	}
	if worktreeUnavailable(s, t) != nil {
		return // stays queued until its worktree is back (ADR 0024)
	}
	if e.acquireWriter(s, t) {
		startFixturePrompt(t)
	}
}
