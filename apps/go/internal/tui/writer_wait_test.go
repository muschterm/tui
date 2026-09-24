package tui

import (
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// TestWriterWaitStatusLine covers the conversation status line shown for a
// thread with queued work blocked behind another thread's checkout writer
// lease: base wording, the holder-title suffix (with the click-through
// action wired to the same thread-select command path as navigation), an
// omitted holder when unknown, and the position-in-line suffix.
func TestWriterWaitStatusLine(t *testing.T) {
	m := testModel()
	m.snapshot.Threads = append(m.snapshot.Threads, protocol.Thread{ID: "holder-1", Title: "Holder Thread"})

	waiting := protocol.Thread{ID: "waiter-1", State: "idle", WriterWait: &protocol.WriterWait{HolderThreadID: "holder-1", Position: 1}}
	if activeTurn(waiting) {
		t.Fatal("a writer-waiting thread must not read as an active turn")
	}
	lines := m.transcriptLines(waiting, 80)
	line := lines[len(lines)-1]
	if want := "· Waiting for checkout · Holder Thread"; strings.TrimRight(line.text, " ") != want {
		t.Fatalf("text = %q, want %q", line.text, want)
	}
	if line.action.Kind != "thread" || line.action.ID != "holder-1" {
		t.Fatalf("action = %+v, want thread-select on holder-1", line.action)
	}

	// Position 1 ("next") omits the position suffix.
	if strings.Contains(line.text, "in line") {
		t.Fatalf("position 1 should not append a position suffix: %q", line.text)
	}

	waiting.WriterWait = &protocol.WriterWait{HolderThreadID: "holder-1", Position: 2}
	lines = m.transcriptLines(waiting, 80)
	line = lines[len(lines)-1]
	if want := "· Waiting for checkout · Holder Thread · 2nd in line"; strings.TrimRight(line.text, " ") != want {
		t.Fatalf("text = %q, want %q", line.text, want)
	}

	// An empty or unrecognized holder omits the holder suffix and the
	// click-through action, since there is nothing to select.
	waiting.WriterWait = &protocol.WriterWait{HolderThreadID: "", Position: 1}
	lines = m.transcriptLines(waiting, 80)
	line = lines[len(lines)-1]
	if want := "· Waiting for checkout"; strings.TrimRight(line.text, " ") != want {
		t.Fatalf("empty holder: text = %q, want %q", line.text, want)
	}
	if line.action.Kind != "" {
		t.Fatalf("empty holder must not be clickable: %+v", line.action)
	}

	waiting.WriterWait = &protocol.WriterWait{HolderThreadID: "unknown-thread", Position: 1}
	lines = m.transcriptLines(waiting, 80)
	line = lines[len(lines)-1]
	if want := "· Waiting for checkout"; strings.TrimRight(line.text, " ") != want {
		t.Fatalf("unknown holder: text = %q, want %q", line.text, want)
	}
	if line.action.Kind != "" {
		t.Fatalf("unknown holder must not be clickable: %+v", line.action)
	}
}

// TestWriterWaitDoesNotOfferStop confirms a writer-waiting thread has no
// active turn to interrupt: Stop stays off the composer, and queued prompts
// remain editable/removable as usual.
func TestWriterWaitDoesNotOfferStop(t *testing.T) {
	m := testModel()
	th := &m.snapshot.Threads[0]
	th.State = "idle"
	th.Queue = []protocol.Prompt{{ID: "q1", Text: "queued while waiting"}}
	th.WriterWait = &protocol.WriterWait{HolderThreadID: "", Position: 1}
	m.state.Active = th.ID
	m.configureInputs()

	visible, overflow := m.composerLayout(120)
	for _, c := range append(visible, overflow...) {
		if c.key == "interrupt" {
			t.Fatal("Stop offered for a thread with no active turn")
		}
	}
}

// TestThreadIndicatorWriterWait covers the navigation card indicator: a
// distinct, neutral state for writer-waiting threads that still yields to
// the existing failed/attention precedence.
func TestThreadIndicatorWriterWait(t *testing.T) {
	cases := []struct {
		name   string
		thread protocol.Thread
		want   threadIndicatorState
	}{
		{
			"waiting on checkout",
			protocol.Thread{State: "idle", Queue: []protocol.Prompt{{}}, WriterWait: &protocol.WriterWait{HolderThreadID: "holder", Position: 1}},
			threadCheckoutWaiting,
		},
		{
			"attention still wins over checkout wait",
			protocol.Thread{State: "idle", Requests: []protocol.Request{{State: "pending"}}, WriterWait: &protocol.WriterWait{HolderThreadID: "holder", Position: 1}},
			threadAttention,
		},
		{
			"failure still wins over checkout wait",
			protocol.Thread{State: "idle", Children: []protocol.Child{{State: "error"}}, WriterWait: &protocol.WriterWait{HolderThreadID: "holder", Position: 1}},
			threadFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadIndicator(tc.thread); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	m := testModel()
	p := m.colors()
	if got := m.threadIndicatorColor(threadCheckoutWaiting); got != p.muted {
		t.Fatalf("checkout-wait color = %q, want muted %q", got, p.muted)
	}
	if got, working, attention := m.threadIndicatorColor(threadCheckoutWaiting), m.threadIndicatorColor(threadWorking), m.threadIndicatorColor(threadAttention); got == working || got == attention {
		t.Fatalf("checkout-wait color must differ from working/attention: %q vs working %q, attention %q", got, working, attention)
	}
}

// TestCheckoutBusyResumeShowsNotice covers a rejected thread.resume: the
// existing command-error notice path must surface a clear, specific message
// instead of staying silent.
func TestCheckoutBusyResumeShowsNotice(t *testing.T) {
	m := testModel()
	c := protocol.Command{ID: "resume-1", Kind: "thread.resume", ThreadID: m.state.Active}
	m.busy, m.state.Pending = &c, &c
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "checkout_busy", Message: "another thread is running on this checkout; resume after it finishes"}})
	if m.notice.severity != noticeError {
		t.Fatalf("severity = %v, want noticeError", m.notice.severity)
	}
	if !strings.Contains(m.notice.text, "Checkout busy") {
		t.Fatalf("notice text = %q, want a clear checkout-busy message", m.notice.text)
	}
}
