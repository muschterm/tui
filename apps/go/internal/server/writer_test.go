package server

import (
	"fmt"
	"sync"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// addFixtureThread appends an idle fixture thread on checkout.
func addFixtureThread(e *engine, id, checkout string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	base := e.snap.Threads[0]
	e.snap.Threads = append(e.snap.Threads, protocol.Thread{ProjectID: base.ProjectID, ID: id, Project: base.Project, Title: id, Checkout: checkout, Agent: base.Agent, State: "idle", Selected: base.Selected, Effective: base.Effective, QueueRevision: 1})
}

func sendTo(t *testing.T, e *engine, thread, id string) {
	t.Helper()
	if _, err := e.command(protocol.Command{Version: 1, ID: id, Kind: "prompt.send", ThreadID: thread, Text: id}); err != nil {
		t.Fatal(err)
	}
}

// assertOneWriter fails when two threads run a turn on one checkout.
func assertOneWriter(t *testing.T, s protocol.Snapshot) {
	t.Helper()
	active := map[string]string{}
	for _, th := range s.Threads {
		if th.Checkout == "" || (th.State != "running" && th.State != "waiting") {
			continue
		}
		if other, ok := active[th.Checkout]; ok {
			t.Fatalf("threads %s and %s both active on %s", other, th.ID, th.Checkout)
		}
		active[th.Checkout] = th.ID
	}
}

func tickUntil(t *testing.T, e *engine, what string, cond func(protocol.Snapshot) bool) protocol.Snapshot {
	t.Helper()
	for range 400 {
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
		s := e.current()
		assertOneWriter(t, s)
		if cond(s) {
			return s
		}
	}
	t.Fatalf("fixture never reached %s", what)
	return protocol.Snapshot{}
}

func TestWriterLeaseSerializesFixtureThreads(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-b", "fixture://workspace")
	sendTo(t, e, "thread-b", "b1")
	b := threadOf(e.current(), "thread-b")
	if b.State != "idle" || len(b.Queue) != 1 || b.WriterWait == nil || *b.WriterWait != (protocol.WriterWait{HolderThreadID: "thread-shell", Position: 1}) {
		t.Fatalf("blocked thread: state %s queue %d wait %+v", b.State, len(b.Queue), b.WriterWait)
	}
	s := tickUntil(t, e, "thread-b running", func(s protocol.Snapshot) bool { return threadOf(s, "thread-b").State == "running" })
	if b := threadOf(s, "thread-b"); b.WriterWait != nil || b.TurnID != "prompt-b1" {
		t.Fatalf("started waiter: %+v %s", b.WriterWait, b.TurnID)
	}
}

func TestWriterLeaseAllowsDifferentCheckouts(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-b", "fixture://other")
	sendTo(t, e, "thread-b", "b1")
	if b := threadOf(e.current(), "thread-b"); b.State != "running" || b.WriterWait != nil {
		t.Fatalf("independent checkout blocked: %s %+v", b.State, b.WriterWait)
	}
}

func TestWriterLeaseIsFIFO(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-b", "fixture://workspace")
	sendTo(t, e, "thread-shell", "a2")
	sendTo(t, e, "thread-b", "b1")
	s := tickUntil(t, e, "waiter start", func(s protocol.Snapshot) bool { return threadOf(s, "thread-b").State == "running" })
	a := threadOf(s, "thread-shell")
	if a.State != "idle" || len(a.Queue) == 0 || a.WriterWait == nil || a.WriterWait.HolderThreadID != "thread-b" || a.WriterWait.Position != 1 {
		t.Fatalf("former holder did not yield: %s queue %d wait %+v", a.State, len(a.Queue), a.WriterWait)
	}
	head := a.Queue[0].ID
	s = tickUntil(t, e, "former holder resumes its queue", func(s protocol.Snapshot) bool { return threadOf(s, "thread-shell").State == "running" })
	if a := threadOf(s, "thread-shell"); a.TurnID != head || a.WriterWait != nil {
		t.Fatalf("queued holder turn: %s %+v", a.TurnID, a.WriterWait)
	}
}

func TestWriterLeaseReleasedByInterruptAndDelete(t *testing.T) {
	for _, kind := range []string{"thread.interrupt", "thread.delete"} {
		t.Run(kind, func(t *testing.T) {
			e := testEngine(t)
			addFixtureThread(e, "thread-b", "fixture://workspace")
			sendTo(t, e, "thread-b", "b1")
			holder := threadOf(e.current(), "thread-shell")
			if _, err := e.command(protocol.Command{Version: 1, ID: "end", Kind: kind, ThreadID: "thread-shell", Revision: holder.LifecycleRevision}); err != nil {
				t.Fatal(err)
			}
			s := e.current()
			assertOneWriter(t, s)
			if b := threadOf(s, "thread-b"); b.State != "running" || b.WriterWait != nil {
				t.Fatalf("waiter not woken: %s %+v", b.State, b.WriterWait)
			}
			// A resume while another thread holds the checkout is refused.
			if kind == "thread.interrupt" {
				if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: "thread-shell"}); err == nil {
					t.Fatal("resume started a second writer on the checkout")
				}
			}
		})
	}
}

func TestWaitingHolderKeepsWriterLease(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-c", "fixture://review")
	sendTo(t, e, "thread-c", "c1")
	s := tickUntil(t, e, "a few ticks", func(protocol.Snapshot) bool { return true })
	c := threadOf(s, "thread-c")
	if threadOf(s, "thread-review").State != "waiting" || c.State != "idle" || c.WriterWait == nil || c.WriterWait.HolderThreadID != "thread-review" {
		t.Fatalf("waiting holder lost the lease: %s %+v", c.State, c.WriterWait)
	}
}

func TestPersistedWriterWaitDoesNotBlockAfterRestart(t *testing.T) {
	e := testEngine(t)
	snap := e.current()
	snap.Threads[0].State, snap.Threads[0].NeedsResume = "interrupted", true
	stale := snap.Threads[0]
	stale.ID, stale.State, stale.NeedsResume = "thread-b", "running", false
	stale.WriterWait = &protocol.WriterWait{HolderThreadID: "thread-gone", Position: 3}
	snap.Threads = append(snap.Threads, stale)
	// Continuation could revive two turns on one checkout; only one survives.
	demoteConcurrentWriters(&snap)
	if b := threadOf(snap, "thread-b"); b.State != "running" {
		t.Fatalf("sole active turn demoted: %s", b.State)
	}
	snap.Threads[1].State, snap.Threads[1].NeedsResume = "interrupted", true
	restarted := newEngine(snap, e.store)
	if b := threadOf(restarted.current(), "thread-b"); b.WriterWait != nil {
		t.Fatalf("persisted wait survived load: %+v", b.WriterWait)
	}
	if _, err := restarted.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: "thread-b"}); err != nil {
		t.Fatal(err)
	}
	if b := threadOf(restarted.current(), "thread-b"); b.State != "running" {
		t.Fatalf("resume blocked by stale state: %s", b.State)
	}

	two := e.current()
	dup := two.Threads[0]
	dup.ID = "thread-dup"
	two.Threads = append(two.Threads, dup)
	demoteConcurrentWriters(&two)
	if d := threadOf(two, "thread-dup"); d.State != "interrupted" || !d.NeedsResume {
		t.Fatalf("second active turn on one checkout survived recovery: %s", d.State)
	}
}

func TestConcurrentFixtureSendsKeepOneWriter(t *testing.T) {
	e := testEngine(t)
	for i := range 6 {
		addFixtureThread(e, fmt.Sprintf("thread-%d", i), "fixture://workspace")
	}
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 3 {
				if _, err := e.command(protocol.Command{Version: 1, ID: fmt.Sprintf("s-%d-%d", i, j), Kind: "prompt.send", ThreadID: fmt.Sprintf("thread-%d", i), Text: "x"}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			_ = e.tick()
			assertOneWriter(t, e.current())
		}
	}()
	wg.Wait()
	tickUntil(t, e, "every queue drained", func(s protocol.Snapshot) bool {
		for _, th := range s.Threads {
			if len(th.Queue) > 0 || th.State == "running" && th.ID != "thread-shell" {
				return false
			}
		}
		return true
	})
}

func TestACPWriterLeaseWaitsAndReleasesOnStop(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "cancel me please")
	waitFor(t, e, "holder running", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "running" })
	b := startACPThread(t, e, "b", "say pong")
	s := waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	if w := threadOf(s, b); w.State != "idle" || len(w.Queue) != 1 || *w.WriterWait != (protocol.WriterWait{HolderThreadID: a, Position: 1}) {
		t.Fatalf("blocked ACP thread: %s %d %+v", w.State, len(w.Queue), w.WriterWait)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: a}); err != nil {
		t.Fatal(err)
	}
	if done := waitTurn(t, e, b, "prompt-b"); done.WriterWait != nil || done.State != "idle" {
		t.Fatalf("waiter after release: %s %+v", done.State, done.WriterWait)
	}
}

func TestACPWriterLeaseKeptWhileWaitingAndReleasedOnDelete(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "ask permission to write")
	waitFor(t, e, "approval", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "waiting" })
	b := startACPThread(t, e, "b", "say pong")
	s := waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	assertOneWriter(t, s)
	holder := threadOf(s, a)
	if _, err := e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: a, Revision: holder.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, b, "prompt-b")
}

func TestACPWriterLeaseReleasedByFailedTurn(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "boom please")
	b := startACPThread(t, e, "b", "say pong")
	waitFor(t, e, "holder failed", func(s protocol.Snapshot) bool {
		assertOneWriter(t, s)
		return threadOf(s, a).State == "failed"
	})
	waitTurn(t, e, b, "prompt-b")
}

func TestConcurrentACPStartsKeepOneWriter(t *testing.T) {
	e, _, _ := acpEngine(t)
	var wg sync.WaitGroup
	ids := make([]string, 3)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			settings := fakeSettings()
			r, err := e.command(protocol.Command{Version: 1, ID: fmt.Sprintf("c%d", i), Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "cancel me please", Settings: &settings})
			if err != nil {
				t.Error(err)
			}
			ids[i] = r.TargetID
		}()
	}
	wg.Wait()
	for range ids {
		s := waitFor(t, e, "one holder", func(s protocol.Snapshot) bool {
			assertOneWriter(t, s)
			for _, id := range ids {
				if threadOf(s, id).State == "running" {
					return true
				}
			}
			return false
		})
		var running string
		for _, id := range ids {
			if threadOf(s, id).State == "running" {
				running = id
			}
		}
		if _, err := e.command(protocol.Command{Version: 1, ID: "stop-" + running, Kind: "thread.interrupt", ThreadID: running}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, e, "stopped", func(s protocol.Snapshot) bool {
			assertOneWriter(t, s)
			return threadOf(s, running).State == "interrupted"
		})
	}
	s := e.current()
	for _, id := range ids {
		if th := threadOf(s, id); th.State != "interrupted" || len(th.Queue) != 0 {
			t.Fatalf("thread %s never ran: %s %d", id, th.State, len(th.Queue))
		}
	}
}

func TestFixtureReopenSendAcquiresTheLease(t *testing.T) {
	for name, checkout := range map[string]string{"free": "fixture://other", "busy": "fixture://workspace"} {
		t.Run(name, func(t *testing.T) {
			e := testEngine(t)
			addFixtureThread(e, "thread-b", checkout)
			b := threadOf(e.current(), "thread-b")
			if _, err := e.command(protocol.Command{Version: 1, ID: "close", Kind: "thread.close", ThreadID: "thread-b", Revision: b.LifecycleRevision}); err != nil {
				t.Fatal(err)
			}
			b = threadOf(e.current(), "thread-b")
			set := b.Selected
			if _, err := e.command(protocol.Command{Version: 1, ID: "rs", Kind: "prompt.reopen-send", ThreadID: "thread-b", Revision: b.LifecycleRevision, Text: "hi", Settings: &set}); err != nil {
				t.Fatal(err)
			}
			b = threadOf(e.current(), "thread-b")
			if name == "free" {
				if b.State != "running" {
					t.Fatalf("reopen-send on a free checkout did not start: %s", b.State)
				}
				return
			}
			if b.State != "idle" || b.WriterWait == nil || b.WriterWait.HolderThreadID != "thread-shell" {
				t.Fatalf("reopen-send on a busy checkout: %s %+v", b.State, b.WriterWait)
			}
			tickUntil(t, e, "reopened waiter start", func(s protocol.Snapshot) bool { return threadOf(s, "thread-b").State == "running" })
		})
	}
}

// Queued work that never started is gated after restart, even with
// continuation enabled; explicit Resume makes it a candidate again.
func TestWaiterIsGatedAfterRestart(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		e := testEngine(t)
		addFixtureThread(e, "thread-b", "fixture://workspace")
		sendTo(t, e, "thread-b", "b1")
		snap := e.current()
		snap.AppSettings.ContinueAfterRestart = continuation
		recoverThreads(&snap)
		if b := threadOf(snap, "thread-b"); b.State != "idle" || !b.NeedsResume || len(b.Queue) != 1 {
			t.Fatalf("recovery did not gate the waiter: %s %v", b.State, b.NeedsResume)
		}
		r := newEngine(snap, e.store)
		tickFixture(t, r, 20)
		if b := threadOf(r.current(), "thread-b"); b.State != "idle" || len(b.Queue) != 1 {
			t.Fatalf("gated waiter started without Resume: %s", b.State)
		}
		if _, err := r.command(protocol.Command{Version: 1, ID: "resume-b", Kind: "thread.resume", ThreadID: "thread-b"}); err != nil {
			t.Fatal(err)
		}
		s := r.current()
		b := threadOf(s, "thread-b")
		if threadOf(s, "thread-shell").State == "running" {
			// Continuation revived the holder: the resumed thread waits in line.
			if b.NeedsResume || b.WriterWait == nil {
				t.Fatalf("resumed waiter: %v %+v", b.NeedsResume, b.WriterWait)
			}
			tickUntil(t, r, "resumed waiter start", func(s protocol.Snapshot) bool { return threadOf(s, "thread-b").State == "running" })
		} else if b.State != "running" {
			t.Fatalf("resume on a free checkout did not start: %s", b.State)
		}
	}
}

func TestACPQueuedThreadGatedAfterRestart(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "cancel me please")
	waitFor(t, e, "holder running", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "running" })
	b := startACPThread(t, e, "b", "say pong")
	snap := waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	recoverThreads(&snap)
	if th := threadOf(snap, b); th.State != "idle" || !th.NeedsResume {
		t.Fatalf("ACP waiter not gated: %s %v", th.State, th.NeedsResume)
	}
	e.stopAgents()
	r := newEngine(snap, e.store)
	r.launch = e.launch
	t.Cleanup(r.stopAgents)
	if _, err := r.command(protocol.Command{Version: 1, ID: "resume-b", Kind: "thread.resume", ThreadID: b}); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, r, b, "prompt-b")
}

func TestFixtureResumeDoesNotJumpWaiters(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-b", "fixture://workspace")
	addFixtureThread(e, "thread-c", "fixture://workspace")
	sendTo(t, e, "thread-b", "b1")
	sendTo(t, e, "thread-c", "c1")
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: "thread-shell"}); err != nil {
		t.Fatal(err)
	}
	// thread-b now holds the lease and thread-c waits: Resume is refused.
	if _, err := e.command(protocol.Command{Version: 1, ID: "resume", Kind: "thread.resume", ThreadID: "thread-shell"}); err == nil {
		t.Fatal("resume jumped the writer line")
	}
	// A candidate waiting on a free checkout also refuses Resume.
	e.mu.Lock()
	e.claiming = map[string]string{"ghost": "fixture://workspace"}
	for i := range e.snap.Threads {
		if e.snap.Threads[i].ID == "thread-b" {
			e.snap.Threads[i].State = "idle"
		}
	}
	e.waitKey(threadByID(&e.snap, "thread-c"))
	delete(e.claiming, "ghost")
	blocked := e.writerBlocked(&e.snap, threadByID(&e.snap, "thread-shell"))
	e.mu.Unlock()
	if !blocked {
		t.Fatal("resume ignores an earlier waiting candidate")
	}
}

// A claim outlives its deleted thread until the runner's dispatch returns.
func TestClaimOfDeletedThreadHoldsTheLease(t *testing.T) {
	e := testEngine(t)
	addFixtureThread(e, "thread-b", "fixture://other")
	e.mu.Lock()
	e.claiming = map[string]string{"deleted-holder": "fixture://other"}
	e.mu.Unlock()
	sendTo(t, e, "thread-b", "b1")
	if b := threadOf(e.current(), "thread-b"); b.State != "idle" || b.WriterWait == nil || b.WriterWait.HolderThreadID != "deleted-holder" {
		t.Fatalf("deleted claim released early: %s %+v", b.State, b.WriterWait)
	}
	e.mu.Lock()
	delete(e.claiming, "deleted-holder")
	e.rebalanceWritersAndFlushLocked()
	e.mu.Unlock()
	if b := threadOf(e.current(), "thread-b"); b.State != "running" {
		t.Fatalf("waiter not woken after dispatch returned: %s", b.State)
	}
}

func TestACPWaiterSurvivesAgentReprobe(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "cancel me please")
	waitFor(t, e, "holder running", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "running" })
	b := startACPThread(t, e, "b", "say pong")
	waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	var prev string
	e.commitMutate(func(s *protocol.Snapshot) {
		ag := agent.Find(s, "claude")
		prev = ag.State
		ag.State = agent.StateProbing
	})
	e.commitMutate(func(s *protocol.Snapshot) { agent.Find(s, "claude").State = prev })
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: a}); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, b, "prompt-b")
}

func TestACPDeleteOfRunningHolderWaitsForDispatch(t *testing.T) {
	e, _, _ := acpEngine(t)
	a := startACPThread(t, e, "a", "cancel me please")
	waitFor(t, e, "holder running", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "running" })
	b := startACPThread(t, e, "b", "say pong")
	s := waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	if _, err := e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: a, Revision: threadOf(s, a).LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "waiter start after the deleted dispatch returned", func(protocol.Snapshot) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		th := threadByID(&e.snap, b)
		_, claimed := e.claiming[a]
		if claimed && th.State != "idle" {
			t.Fatal("waiter started while the deleted holder's dispatch was still live")
		}
		return th.State != "idle" || len(th.Queue) == 0
	})
	waitTurn(t, e, b, "prompt-b")
}

// A woken waiter whose claim fails must hand the lease on at once.
func TestFailedClaimWakesTheNextWaiter(t *testing.T) {
	e, _, checkout := acpEngine(t)
	b := startACPThread(t, e, "b", "say pong")
	waitTurn(t, e, b, "prompt-b")
	e.mu.Lock()
	base := *threadByID(&e.snap, b)
	fixtureThread := protocol.Thread{ProjectID: base.ProjectID, ID: "thread-c", Title: "c", Checkout: checkout, Agent: "Fixture agent", State: "idle", QueueRevision: 1,
		Queue: []protocol.Prompt{{ID: "prompt-c", Text: "c", Revision: 1}}}
	e.snap.Threads = append(e.snap.Threads, fixtureThread)
	bt := threadByID(&e.snap, b)
	bt.Queue = []protocol.Prompt{{ID: "prompt-b2", Text: "again", Revision: 1, Settings: base.Selected}}
	e.waitKey(bt)
	e.waitKey(threadByID(&e.snap, "thread-c"))
	e.ensureRunLocked(b)
	agent.Find(&e.snap, "claude").State = agent.StateProbing
	e.mu.Unlock()
	s := waitFor(t, e, "next waiter start", func(s protocol.Snapshot) bool { return threadOf(s, "thread-c").State == "running" })
	if th := threadOf(s, b); th.State != "failed" {
		t.Fatalf("claim outcome: %s", th.State)
	}
}

// A waiter whose agent becomes unavailable fails honestly at the head of a
// free checkout, and the next candidate still proceeds.
func TestACPWaiterWithUnavailableAgentFailsAtHead(t *testing.T) {
	e, _, checkout := acpEngine(t)
	a := startACPThread(t, e, "a", "cancel me please")
	waitFor(t, e, "holder running", func(s protocol.Snapshot) bool { return threadOf(s, a).State == "running" })
	b := startACPThread(t, e, "b", "say pong")
	waitFor(t, e, "writer wait", func(s protocol.Snapshot) bool { return threadOf(s, b).WriterWait != nil })
	e.mu.Lock()
	base := *threadByID(&e.snap, b)
	e.snap.Threads = append(e.snap.Threads, protocol.Thread{ProjectID: base.ProjectID, ID: "thread-c", Title: "c", Checkout: checkout, Agent: "Fixture agent", State: "idle", QueueRevision: 1,
		Queue: []protocol.Prompt{{ID: "prompt-c", Text: "c", Revision: 1}}})
	e.mu.Unlock()
	e.commitMutate(func(s *protocol.Snapshot) {
		ag := agent.Find(s, "claude")
		ag.State, ag.Detail = agent.StateUnavailable, "gone"
	})
	if w := threadOf(e.current(), b).WriterWait; w == nil || w.HolderThreadID != a {
		t.Fatalf("not-ready waiter left the line while blocked: %+v", w)
	}
	if _, err := e.command(protocol.Command{Version: 1, ID: "stop", Kind: "thread.interrupt", ThreadID: a}); err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "next candidate start", func(s protocol.Snapshot) bool { return threadOf(s, "thread-c").State == "running" })
	if th := threadOf(s, b); th.State != "failed" || th.Error == "" || len(th.Queue) != 1 || th.WriterWait != nil {
		t.Fatalf("not-ready waiter: %s %q queue %d wait %+v", th.State, th.Error, len(th.Queue), th.WriterWait)
	}
}
