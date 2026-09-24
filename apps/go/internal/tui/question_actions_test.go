package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// questionActionModel is the review card with the given offered actions.
func questionActionModel(actions ...string) (*Model, protocol.Request) {
	m, req := questionReviewModel()
	req.Actions = actions
	m.snapshot.Threads[0].Requests[0] = req
	m.configureInputs()
	return m, req
}

func TestQuestionDeclineShownOnlyWhenOffered(t *testing.T) {
	m, _ := questionActionModel()
	f := m.render()
	if hasHit(f, "answer-decline") || hasHit(f, "answer-cancel") || hasHit(f, "answer-actions") {
		t.Fatal("actions shown although the request offers none")
	}
	m, _ = questionActionModel("decline")
	f = m.render()
	submit, decline := controlHit(t, f, "answer-submit"), controlHit(t, f, "answer-decline")
	if hasHit(f, "answer-cancel") || decline.Rect.Y != submit.Rect.Y || decline.Rect.X+decline.Rect.W+1 != submit.Rect.X || decline.Rect.W != questionDeclineWidth {
		t.Fatalf("Decline %+v not directly left of Submit %+v", decline.Rect, submit.Rect)
	}
	if !strings.Contains(ansi.Strip(f.rows[decline.Rect.Y]), "Decline") {
		t.Fatal("Decline label missing")
	}
	m, _ = questionActionModel("decline", "cancel")
	f = m.render()
	decline, cancel := controlHit(t, f, "answer-decline"), controlHit(t, f, "answer-cancel")
	if cancel.Rect.X+cancel.Rect.W+1 != decline.Rect.X || controlHit(t, f, "answer-submit").Rect.X+questionSubmitWidth != f.request.X+2+max(1, f.request.W-4) {
		t.Fatalf("Cancel %+v, Decline %+v or Submit misplaced", cancel.Rect, decline.Rect)
	}
}

// Submit is never displaced: Cancel moves into More… first, then Decline.
func TestQuestionActionsOverflowNeverCrowdsSubmit(t *testing.T) {
	m, req := questionActionModel("decline", "cancel")
	keys := func(room int) []string {
		var out []string
		for _, b := range m.questionActionsPlan(req, room) {
			out = append(out, b.key)
		}
		return out
	}
	for _, tc := range []struct {
		room int
		want []string
	}{
		{80, []string{"answer-cancel", "answer-decline", "answer-submit"}},
		{questionCancelWidth + questionDeclineWidth + questionSubmitWidth + 1, []string{"answer-actions", "answer-decline", "answer-submit"}},
		{questionMoreWidth + questionDeclineWidth + questionSubmitWidth + 1, []string{"answer-actions", "answer-submit"}},
		{questionMoreWidth + questionSubmitWidth, []string{"answer-submit"}},
	} {
		if got := keys(tc.room); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("room %d: %v, want %v", tc.room, got, tc.want)
		}
	}
	m.activate(action{Kind: "answer-actions"})
	if len(m.menu) != 2 || m.menu[0].Label != "Decline" || m.menu[1].Label != "Cancel" || m.menu[1].Action.Value != "cancel" || m.menu[1].Action.Revision != req.Revision {
		t.Fatalf("More… menu: %+v", m.menu)
	}
	m.activate(m.menu[1].Action)
	if m.busy == nil || m.busy.RequestAction != "cancel" {
		t.Fatalf("menu Cancel sent %+v", m.busy)
	}
}

func TestQuestionDeclineSendsNoAnswersAndKeepsDraftsOnFailure(t *testing.T) {
	m, req := questionActionModel("decline", "cancel")
	m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
	m.saveQuestionDraft(req, 2, answerDraft{Text: "draft note"})
	m.prompt.SetValue("preserve my prompt")
	f := m.render()
	m.activate(controlHit(t, f, "answer-decline").Action)
	if m.busy == nil {
		t.Fatalf("Decline not sent: %q", m.status)
	}
	c := *m.busy
	if c.Kind != "request.answer" || c.RequestAction != "decline" || c.TargetID != req.ID || c.Revision != req.Revision || c.QuestionAnswers != nil || c.Answers != nil {
		t.Fatalf("decline command: %+v", c)
	}
	if message, _ := m.requestCardNotice(req); message != "Declining…" {
		t.Fatalf("in-flight notice %q", message)
	}
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "stale_request", Message: "request changed"}})
	if d := m.questionDraft(req, 0); !reflect.DeepEqual(d.Choices, []string{"Compact"}) || m.questionDraft(req, 2).Text != "draft note" || m.prompt.Value() != "preserve my prompt" {
		t.Fatalf("failed decline lost drafts: %+v", d)
	}
	if message, problem := m.requestCardNotice(req); !problem || !strings.Contains(message, "request changed") {
		t.Fatalf("failure feedback %q", message)
	}
	// An accepted decline reports its own outcome, never an answer.
	m.busy = nil
	m.activate(action{Kind: "answer-action", ID: req.ID, Revision: req.Revision, Value: "decline"})
	c = *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{State: "accepted"}})
	if !strings.HasPrefix(m.status, "Decline accepted by server") || !strings.Contains(m.status, "confirmation unavailable") {
		t.Fatalf("acceptance status %q", m.status)
	}
}

func TestQuestionActionRefusedWhenNotOfferedOrBlocked(t *testing.T) {
	m, req := questionActionModel("decline")
	m.activate(action{Kind: "answer-action", ID: req.ID, Revision: req.Revision, Value: "cancel"})
	if m.busy != nil {
		t.Fatal("unoffered Cancel sent")
	}
	for _, block := range []string{"resume", "disconnected"} {
		m, req := questionActionModel("decline")
		m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Roomy"}})
		if block == "resume" {
			m.snapshot.Threads[0].NeedsResume = true
		} else {
			m.connected = false
		}
		m.configureInputs()
		cells := func() string {
			f := m.render()
			h := controlHit(t, f, "answer-decline")
			var b strings.Builder
			for x := h.Rect.X; x < h.Rect.X+h.Rect.W; x++ {
				b.WriteString(cellSGR(f, x, h.Rect.Y) + "|")
			}
			return b.String()
		}
		idle := cells()
		m.hover = "answer-decline"
		if cells() != idle {
			t.Fatalf("%s: disabled Decline changed on hover", block)
		}
		m.activate(action{Kind: "answer-action", ID: req.ID, Revision: req.Revision, Value: "decline"})
		if m.busy != nil || !reflect.DeepEqual(m.questionDraft(req, 0).Choices, []string{"Roomy"}) {
			t.Fatalf("%s: blocked Decline sent or cleared the draft", block)
		}
	}
	enabled, _ := questionActionModel("decline")
	blocked, _ := questionActionModel("decline")
	blocked.connected = false
	sgr := func(m *Model) string {
		f := m.render()
		h := controlHit(t, f, "answer-decline")
		return cellSGR(f, h.Rect.X+2, h.Rect.Y)
	}
	if sgr(enabled) == sgr(blocked) {
		t.Fatal("disabled Decline looks enabled")
	}
}

func TestQuestionActionsReachableByTab(t *testing.T) {
	m, _ := questionActionModel("decline", "cancel")
	m.setFocus("answer-submit")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focus != "answer-decline" {
		t.Fatalf("Shift+Tab from Submit reached %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focus != "answer-cancel" {
		t.Fatalf("Shift+Tab from Decline reached %q", m.focus)
	}
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.focus != "answer-submit" {
		t.Fatalf("Tab order returned to %q", m.focus)
	}
	m.setFocus("answer-decline")
	pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == nil || m.busy.RequestAction != "decline" {
		t.Fatal("Enter on focused Decline did not decline")
	}
}

func TestQuestionOptionDescriptionsRenderMutedUnderLabel(t *testing.T) {
	m, req := questionReviewModel()
	before := m.requestHeight(80)
	req.Questions[0].OptionDescriptions = []string{"Tighter rows for small terminals", ""}
	m.snapshot.Threads[0].Requests[0] = req
	lines := m.questionLines(req, 60)
	var details []questionLine
	for _, line := range lines {
		if line.detail {
			details = append(details, line)
		}
	}
	indent := ansi.StringWidth(m.icon("radio")) + 1
	if len(details) != 1 || details[0].text != "Tighter rows for small terminals" || details[0].key != "" || details[0].indent != indent {
		t.Fatalf("description rows: %+v", details)
	}
	if after := m.requestHeight(80); after != before+1 {
		t.Fatalf("description not counted in the card budget: %d -> %d", before, after)
	}
	m.hover = "option:0"
	f := m.render()
	h := controlHit(t, f, "option:0")
	if want := indent + ansi.StringWidth("Compact"); h.Rect.W != want {
		t.Fatalf("hover extent %d, want %d", h.Rect.W, want)
	}
	y := h.Rect.Y + 1
	x := f.request.X + 2 + indent
	if got := ansi.Strip(cutCells(f.rows[y], x, x+len("Tighter"))); got != "Tighter" || !foregroundAt(f, x, y, m.colors().muted) || backgroundAt(f, x, y, m.hoverFill()) {
		t.Fatalf("description row %q not muted under the label", got)
	}
	// The Options… menu lists labels only.
	m.activate(action{Kind: "answer-options"})
	for _, item := range m.menu {
		if strings.Contains(item.Label, "Tighter") {
			t.Fatalf("menu shows description: %q", item.Label)
		}
	}
	// A malformed, non-parallel slice is ignored rather than guessed.
	req.Questions[0].OptionDescriptions = []string{"only one"}
	for _, line := range m.questionLines(req, 60) {
		if line.detail {
			t.Fatal("malformed descriptions rendered")
		}
	}
}

func TestQuestionHistoryDeclinedAndCancelled(t *testing.T) {
	m := testModel()
	p := m.colors()
	for _, tc := range []struct {
		action, delivery, state, status, color string
	}{
		{"decline", "fixture-confirmed", "resolved", "Declined", p.muted},
		{"cancel", "fixture-confirmed", "resolved", "Cancelled", p.muted},
		{"decline", "acp-unconfirmed", "closed", "Declined · provider confirmation unavailable", p.muted},
		{"cancel", "acp-undeliverable", "closed", "Cancelled · not delivered", p.red},
	} {
		thread, request, activity := questionHistoryFixture()
		request.Action, request.Delivery, request.State, request.QuestionAnswers = tc.action, tc.delivery, tc.state, nil
		request.Questions[0].OptionDescriptions = []string{"Tighter rows", ""}
		thread.Requests[0] = request
		lines, ok := m.questionHistoryLines(thread, activity, 80)
		if !ok || len(lines) == 0 {
			t.Fatalf("%s: no history card", tc.status)
		}
		text := questionHistoryRenderedText(lines)
		outcome, detail, split := strings.Cut(tc.status, " · ")
		rendered := strings.ToUpper(outcome)
		if split {
			rendered += " · " + detail
		}
		if !strings.Contains(text, rendered) || strings.Contains(strings.ToUpper(text), "ANSWERED") || strings.Contains(strings.ToUpper(text), "SUBMITTED") || strings.Contains(text, "No answer recorded") || strings.Contains(text, "Tighter rows") {
			t.Fatalf("%s: %s", tc.status, text)
		}
		if !strings.Contains(text, "Which layout?") || !strings.Contains(text, "Roomy") || strings.Contains(text, m.icon("radio-on")) {
			t.Fatalf("%s: original questions missing or an option shown selected: %s", tc.status, text)
		}
		for _, line := range lines {
			// A decline or cancel never takes the answered check; its mark
			// carries the delivery color.
			if strings.Contains(ansi.Strip(line.text), rendered) && (line.leadFG != tc.color || line.lead == "✓" || line.lead == "+") {
				t.Fatalf("%s: status mark %q color %q, want %q", tc.status, line.lead, line.leadFG, tc.color)
			}
		}
		if copied := questionHistoryText(request); !strings.HasPrefix(copied, tc.status) || strings.Contains(copied, "No answer recorded") {
			t.Fatalf("%s: copied %q", tc.status, copied)
		}
	}
}

func TestTurnConfirmedDeliveryIsConfirmed(t *testing.T) {
	if !deliveryConfirmed("acp-turn-confirmed") {
		t.Fatal("turn-confirmed delivery not treated as confirmed")
	}
	if got := requestDeliveryDescription("acp-turn-confirmed"); got != "Answer taken by provider · turn completed" {
		t.Fatalf("description %q", got)
	}
	request := protocol.Request{Kind: "question", State: "resolved", Delivery: "acp-turn-confirmed", SubmissionID: "a", Questions: []protocol.Question{{Text: "Which?"}}, QuestionAnswers: []protocol.Answer{{Text: "yes"}}}
	if status, confirmed := questionHistoryStatus(request); status != "Answered" || !confirmed {
		t.Fatalf("history %q %v", status, confirmed)
	}
}

// The queue outline follows keyboard focus inside it, never hover.
func TestQueueOutlineFollowsFocusNotHover(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	if len(m.thread().Queue) == 0 {
		t.Fatal("fixture has no queued prompt")
	}
	m.setFocus("prompt")
	edge := func() string {
		f := m.render()
		h := controlHit(t, f, "queue")
		return cellSGR(f, h.Rect.X-2, h.Rect.Y+1)
	}
	rest := edge()
	for _, key := range []string{"queue", "queue-row:prompt-initial", "edit:prompt-initial"} {
		m.hover = key
		if edge() != rest {
			t.Fatalf("hover on %s recolored the queue outline", key)
		}
	}
	m.hover = ""
	m.setFocus("edit:prompt-initial")
	if edge() == rest {
		t.Fatal("focus inside the queue kept the rest outline")
	}
}
