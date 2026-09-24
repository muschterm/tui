package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func questionHistoryFixture() (protocol.Thread, protocol.Request, protocol.Activity) {
	optional := false
	request := protocol.Request{
		ID: "question-history", Kind: "question", State: "closed", Delivery: "acp-unconfirmed",
		SubmissionID: "answer-command", SubmittedRevision: 3, Origin: "fixture",
		Questions: []protocol.Question{
			{ID: "layout", Label: "Layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}},
			{ID: "features", Text: "Which features?", Kind: "multiple", Options: []string{"Files", "Git", "Terminal"}},
			{ID: "note", Text: "Anything else?", Kind: "text", Required: &optional},
		},
		QuestionAnswers: []protocol.Answer{
			{Choices: []string{"Compact"}},
			{Choices: []string{"Files", "Terminal"}, Text: "Use built-in previews"},
			{},
		},
	}
	activity := protocol.Activity{ID: "question-answer:question-history:3", Role: "question-answer", RequestID: request.ID, TurnID: "turn-1"}
	thread := protocol.Thread{ID: "thread", Requests: []protocol.Request{request}, Activity: []protocol.Activity{activity}}
	return thread, request, activity
}

func questionHistoryRenderedText(lines []contentLine) string {
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		values = append(values, ansi.Strip(line.text))
	}
	return strings.Join(values, "\n")
}

func questionHistoryStatusLine(t *testing.T, lines []contentLine) contentLine {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line.text), "SUBMITTED ·") || strings.Contains(ansi.Strip(line.text), "ANSWERED") {
			return line
		}
	}
	t.Fatal("question history has no status header")
	return contentLine{}
}

func TestQuestionHistoryShowsQuestionAnswerAndAvailableOptions(t *testing.T) {
	m := testModel()
	thread, _, activity := questionHistoryFixture()
	m.viewState().QuestionHistoryExpanded = map[string]bool{"question-history": true}
	lines, ok := m.questionHistoryLines(thread, activity, 100)
	if !ok {
		t.Fatal("accepted question response did not produce a history card")
	}
	text := questionHistoryRenderedText(lines)
	singlePicked := m.questionMarker("single", true) + " Compact"
	singleOther := m.questionMarker("single", false) + " Roomy"
	multiPicked := m.questionMarker("multiple", true)
	multiOther := m.questionMarker("multiple", false)
	for _, part := range []string{
		"SUBMITTED · provider confirmation unavailable", "Which layout?", singlePicked, singleOther,
		"Which features?", multiPicked + " Files", multiPicked + " Terminal", multiOther + " Git", multiPicked + " Other · Use built-in previews",
		"Anything else?", "Skipped (optional)",
	} {
		if !strings.Contains(text, part) {
			t.Errorf("history card missing %q:\n%s", part, text)
		}
	}
	if strings.Contains(strings.ToUpper(text), "ANSWERED") {
		t.Fatalf("unconfirmed response was labelled Answered:\n%s", text)
	}
	toggle := false
	for _, line := range lines {
		if line.action.Kind == "question-history-toggle" {
			toggle = true
			if !strings.Contains(line.text, "Collapse") {
				t.Fatalf("expanded card toggle label = %q", line.text)
			}
		} else if line.action.Kind != "" {
			t.Fatalf("read-only history gained a non-expansion action: %+v", line.action)
		}
	}
	if !toggle {
		t.Fatal("expanded long card did not expose its collapse control")
	}
}

func TestQuestionHistoryUsesConfirmedLabelOnlyForConfirmedResolution(t *testing.T) {
	m := testModel()
	thread, request, activity := questionHistoryFixture()
	request.State, request.Delivery = "resolved", "fixture-confirmed"
	thread.Requests[0] = request
	lines, ok := m.questionHistoryLines(thread, activity, 60)
	if !ok {
		t.Fatal("confirmed fixture response did not produce history")
	}
	text := questionHistoryRenderedText(lines)
	if !strings.Contains(text, "✓ ANSWERED") || strings.Contains(strings.ToUpper(text), "SUBMITTED") {
		t.Fatalf("confirmed response label = %q", text)
	}
}

func TestQuestionHistoryCopyIncludesFullOriginalChoicesAndAcceptedValues(t *testing.T) {
	_, request, _ := questionHistoryFixture()
	text := questionHistoryText(request)
	for _, part := range []string{
		"Submitted · provider confirmation unavailable", "Which layout?", "◉ Compact", "○ Roomy",
		"Which features?", "☑ Files", "☐ Git", "☑ Terminal", "☑ Other · Use built-in previews",
		"Skipped (optional)",
	} {
		if !strings.Contains(text, part) {
			t.Errorf("copied question history missing %q:\n%s", part, text)
		}
	}
}

func TestQuestionHistoryFallbackReportsUnknownPositionAndDeduplicates(t *testing.T) {
	m := testModel()
	thread, _, _ := questionHistoryFixture()
	thread.Activity = nil
	lines := m.questionHistoryFallbackLines(thread, 80)
	text := questionHistoryRenderedText(lines)
	// The card hugs the 80% cap, so the status header may wrap between words.
	flat := strings.Join(strings.Fields(strings.ReplaceAll(text, "│", " ")), " ")
	if !strings.Contains(flat, "earlier position unavailable") || strings.Count(text, "unavailable") < 2 || !strings.Contains(text, "Which layout?") {
		t.Fatalf("legacy answer fallback lacks position or question:\n%s", text)
	}
	thread.Activity = []protocol.Activity{
		{ID: "first-copy", Role: "question-answer", RequestID: "question-history"},
		{ID: "duplicate-copy", Role: "question-answer", RequestID: "question-history"},
	}
	if lines := m.questionHistoryFallbackLines(thread, 80); len(lines) != 0 {
		t.Fatalf("anchored answer was duplicated in fallback: %q", questionHistoryRenderedText(lines))
	}
	first, ok := m.questionHistoryLines(thread, thread.Activity[0], 80)
	if !ok {
		t.Fatal("first anchored card was not rendered")
	}
	duplicate, handled := m.questionHistoryLines(thread, thread.Activity[1], 80)
	if !handled || len(duplicate) != 0 {
		t.Fatal("duplicate request marker was not swallowed without a second card")
	}
	if !strings.Contains(questionHistoryRenderedText(first), "SUBMITTED") {
		t.Fatal("deduplicated anchor lost its card")
	}
	thread.Activity[1].State = "question-history-position-unavailable"
	duplicate, handled = m.questionHistoryLines(thread, thread.Activity[1], 80)
	if !handled || len(duplicate) != 0 {
		t.Fatal("duplicate position-unavailable marker was not swallowed")
	}
}

func TestQuestionHistoryFallbackStaysWithItsRetainedTurn(t *testing.T) {
	m := testModel()
	thread, request, _ := questionHistoryFixture()
	request.TurnID = "older-turn"
	thread.Requests[0] = request
	thread.Activity = []protocol.Activity{
		{ID: "prompt-old", Role: "user", TurnID: "older-turn", Text: "original older prompt"},
		{ID: "answer-old", Role: "agent", TurnID: "older-turn", Text: "end of older turn"},
		{ID: "prompt-new", Role: "user", TurnID: "newer-turn", Text: "later user prompt"},
		{ID: "answer-new", Role: "agent", TurnID: "newer-turn", Text: "latest response"},
	}

	text := questionHistoryRenderedText(m.transcriptLines(thread, 100))
	turnEnd := strings.Index(text, "end of older turn")
	history := strings.Index(text, "Which layout?")
	laterTurn := strings.Index(text, "later user prompt")
	if turnEnd < 0 || history < 0 || laterTurn < 0 || !(turnEnd < history && history < laterTurn) {
		t.Fatalf("unanchored answer was not placed after its retained turn and before later messages:\n%s", text)
	}
	if !strings.Contains(text, "earlier position unavailable") || !strings.Contains(text, "SUBMITTED · provider confirmation unavailable") || strings.Contains(strings.ToUpper(text), "ANSWERED") {
		t.Fatalf("fallback overstated its position or delivery:\n%s", text)
	}
}

func TestQuestionHistoryWithoutRetainedTurnPrecedesRetainedTimeline(t *testing.T) {
	m := testModel()
	thread, request, _ := questionHistoryFixture()
	request.TurnID = "trimmed-turn"
	thread.Requests[0] = request
	thread.Activity = []protocol.Activity{
		{ID: "retained-prompt", Role: "user", TurnID: "retained-turn", Text: "retained user prompt"},
		{ID: "retained-answer", Role: "agent", TurnID: "retained-turn", Text: "retained agent response"},
	}

	text := questionHistoryRenderedText(m.transcriptLines(thread, 100))
	history := strings.Index(text, "Which layout?")
	retained := strings.Index(text, "retained user prompt")
	if history < 0 || retained < 0 || history > retained {
		t.Fatalf("answer with no retained turn did not precede the retained timeline:\n%s", text)
	}
	if !strings.Contains(text, "Earlier history") || !strings.Contains(text, "earlier position unavailable") {
		t.Fatalf("earlier-history card omitted its position label:\n%s", text)
	}
}

func TestAnchoredQuestionHistoryRemainsInActivityChronology(t *testing.T) {
	m := testModel()
	thread, request, marker := questionHistoryFixture()
	request.TurnID = "question-turn"
	thread.Requests[0] = request
	marker.TurnID = "question-turn"
	thread.Activity = []protocol.Activity{
		{ID: "before-question", Role: "user", TurnID: "question-turn", Text: "before question"},
		marker,
		{ID: "after-question", Role: "agent", TurnID: "question-turn", Text: "after question"},
		{ID: "next-turn", Role: "user", TurnID: "next-turn", Text: "next turn"},
	}

	text := questionHistoryRenderedText(m.transcriptLines(thread, 100))
	before := strings.Index(text, "before question")
	history := strings.Index(text, "Which layout?")
	after := strings.Index(text, "after question")
	next := strings.Index(text, "next turn")
	if before < 0 || history < 0 || after < 0 || next < 0 || !(before < history && history < after && after < next) {
		t.Fatalf("anchored answer did not retain its activity position:\n%s", text)
	}
	if strings.Count(text, "Which layout?") != 1 || !strings.Contains(text, "SUBMITTED · provider confirmation unavailable") || strings.Contains(text, "earlier position unavailable") {
		t.Fatalf("anchored answer duplicated or lost truthful delivery status:\n%s", text)
	}
}

func TestQuestionHistoryExpandIsLocalAndShowsEveryAlternative(t *testing.T) {
	m := testModel()
	request := protocol.Request{
		ID: "large-history", Kind: "question", State: "closed", Delivery: "acp-unconfirmed",
		SubmissionID: "answer-large", Questions: []protocol.Question{{
			ID: "choice", Text: "Which option should be selected?", Kind: "single",
			Options: []string{"Option 0", "Option 1", "Option 2", "Option 3", "Option 4", "Option 5", "Option 6", "Option 7", "Option 8", "Option 9", "Option 10", "Option 11", "Option 12", "Option 13"},
		}}, QuestionAnswers: []protocol.Answer{{Choices: []string{"Option 0"}}},
	}
	activity := protocol.Activity{ID: "question-answer:large-history:1", Role: "question-answer", RequestID: request.ID}
	thread := &m.snapshot.Threads[0]
	thread.Requests, thread.Activity = []protocol.Request{request}, []protocol.Activity{activity}

	collapsed, ok := m.questionHistoryLines(*thread, activity, 100)
	if !ok {
		t.Fatal("long answer did not render")
	}
	preview := questionHistoryRenderedText(collapsed)
	if !strings.Contains(preview, "Which option should be selected?") || !strings.Contains(preview, m.questionMarker("single", true)+" Option 0") || !strings.Contains(preview, "13 other option(s) hidden") {
		t.Fatalf("compact preview lost question/selected choice or hidden count:\n%s", preview)
	}
	if strings.Contains(preview, m.questionMarker("single", false)+" Option 1") {
		t.Fatal("compact preview rendered overflow choices before expansion")
	}
	if m.busy != nil {
		t.Fatal("history preview submitted an answer")
	}

	m.activate(action{Kind: "question-history-toggle", ID: request.ID})
	if !m.viewState().QuestionHistoryExpanded[request.ID] || m.busy != nil {
		t.Fatal("expand did not update only the local read-only view")
	}
	raw, err := json.Marshal(m.state)
	if err != nil {
		t.Fatal(err)
	}
	restored := New(nil, "test", m.snapshot, raw)
	if !restored.viewState().QuestionHistoryExpanded[request.ID] {
		t.Fatal("per-thread history expansion did not survive view restoration")
	}
	expanded, ok := m.questionHistoryLines(*thread, activity, 100)
	if !ok {
		t.Fatal("expanded history did not render")
	}
	allOptions := questionHistoryRenderedText(expanded)
	for _, option := range request.Questions[0].Options {
		if !strings.Contains(allOptions, option) {
			t.Errorf("expanded history omitted available option %q", option)
		}
	}
	if m.busy != nil {
		t.Fatal("expanding history submitted an answer")
	}
}

func TestQuestionHistoryExpansionHasKeyboardAndMousePaths(t *testing.T) {
	m := testModel()
	request := protocol.Request{
		ID: "history-controls", Kind: "question", State: "closed", Delivery: "acp-unconfirmed", SubmissionID: "answer-controls",
		Questions:       []protocol.Question{{ID: "choice", Text: "Choose one", Kind: "single", Options: []string{"Selected", "Other 1", "Other 2", "Other 3", "Other 4", "Other 5", "Other 6", "Other 7", "Other 8", "Other 9", "Other 10", "Other 11", "Other 12"}}},
		QuestionAnswers: []protocol.Answer{{Choices: []string{"Selected"}}},
	}
	activity := protocol.Activity{ID: "question-answer:history-controls:1", Role: "question-answer", RequestID: request.ID}
	thread := &m.snapshot.Threads[0]
	thread.Requests, thread.Activity = []protocol.Request{request}, []protocol.Activity{activity}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	button := controlHit(t, m.render(), "question-history:"+request.ID)
	m.setFocus(button.Key)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.viewState().QuestionHistoryExpanded[request.ID] {
		t.Fatal("Enter did not expand the focused read-only card")
	}
	f := m.render()
	m.viewState().Scroll = f.transcriptMax
	button = controlHit(t, m.render(), "question-history:"+request.ID)
	m.Update(tea.MouseClickMsg{X: button.Rect.X, Y: button.Rect.Y, Button: tea.MouseLeft})
	if m.viewState().QuestionHistoryExpanded[request.ID] || m.busy != nil {
		t.Fatal("mouse click did not collapse the card locally")
	}
}

func TestQuestionHistoryCollapsesWrappedLongText(t *testing.T) {
	m := testModel()
	request := protocol.Request{
		ID: "long-history", Kind: "question", State: "closed", Delivery: "acp-uncertain", SubmissionID: "answer-long",
		Questions:       []protocol.Question{{ID: "text", Kind: "text", Text: strings.Repeat("Original question wording ", 24)}},
		QuestionAnswers: []protocol.Answer{{Text: strings.Repeat("accepted answer text ", 36)}},
	}
	activity := protocol.Activity{ID: "question-answer:long-history:1", Role: "question-answer", RequestID: request.ID}
	thread := &m.snapshot.Threads[0]
	thread.Requests, thread.Activity = []protocol.Request{request}, []protocol.Activity{activity}
	compact, ok := m.questionHistoryLines(*thread, activity, 80)
	if !ok || len(compact) >= questionHistoryCompactRows+5 {
		t.Fatalf("long Q&A did not collapse to a bounded preview: handled=%v rows=%d", ok, len(compact))
	}
	preview := questionHistoryRenderedText(compact)
	if !strings.Contains(preview, "…") || !strings.Contains(preview, "Expand") || !strings.Contains(preview, "SUBMITTED · delivery uncertain") {
		t.Fatalf("compact preview lacks truncation, status or expansion:\n%s", preview)
	}
	m.activate(action{Kind: "question-history-toggle", ID: request.ID})
	expanded, ok := m.questionHistoryLines(*thread, activity, 80)
	if !ok || len(expanded) <= len(compact) {
		t.Fatalf("expanded long Q&A did not reveal the preserved content: handled=%v rows=%d compact=%d", ok, len(expanded), len(compact))
	}
	full := questionHistoryRenderedText(expanded)
	if !strings.Contains(full, "accepted answer text") || !strings.Contains(full, "Collapse") {
		t.Fatalf("expanded Q&A lost the answer or collapse control:\n%s", full)
	}
}

func TestQuestionHistoryDeliveryLabelsRemainTruthful(t *testing.T) {
	for _, tc := range []struct {
		delivery string
		want     string
	}{
		{"acp-accepted", "accepted by server, upstream unconfirmed"},
		{"acp-unconfirmed", "provider confirmation unavailable"},
		{"acp-uncertain", "delivery uncertain"},
		{"acp-undeliverable", "not delivered"},
		{"acp-cancelled", "cancelled before confirmation"},
		{"acp-delivered", "legacy delivery status unconfirmed"},
	} {
		t.Run(tc.delivery, func(t *testing.T) {
			request := protocol.Request{Kind: "question", State: "closed", Delivery: tc.delivery, SubmissionID: "answer", Questions: []protocol.Question{{Text: "Which?"}}, QuestionAnswers: []protocol.Answer{{Text: "yes"}}}
			got, confirmed := questionHistoryStatus(request)
			if confirmed || !strings.Contains(got, tc.want) || strings.Contains(got, "Answered") {
				t.Fatalf("%q -> (%q,%v)", tc.delivery, got, confirmed)
			}
		})
	}
}

func TestQuestionHistoryHeaderToneMatchesDeliveryConfidence(t *testing.T) {
	m := testModel()
	p := m.colors()
	base := protocol.Request{
		ID: "status-tone", Kind: "question", State: "closed", SubmissionID: "answer",
		Questions:       []protocol.Question{{ID: "long", Kind: "text", Text: strings.Repeat("Original question wording ", 24)}},
		QuestionAnswers: []protocol.Answer{{Text: strings.Repeat("accepted answer text ", 32)}},
	}
	for _, tc := range []struct {
		delivery string
		status   string
		color    string
	}{
		{"acp-unconfirmed", "○ SUBMITTED · provider confirmation unavailable", p.muted},
		{"acp-uncertain", "! SUBMITTED · delivery uncertain", p.gold},
		{"acp-undeliverable", "✕ SUBMITTED · not delivered", p.red},
	} {
		t.Run(tc.delivery, func(t *testing.T) {
			request := base
			request.Delivery = tc.delivery
			activity := protocol.Activity{ID: "status-marker", Role: "question-answer", RequestID: request.ID}
			thread := protocol.Thread{ID: "status-thread", Requests: []protocol.Request{request}, Activity: []protocol.Activity{activity}}
			before, err := json.Marshal(thread.Requests[0])
			if err != nil {
				t.Fatal(err)
			}

			for _, expanded := range []bool{true, false} {
				m.viewState().QuestionHistoryExpanded = map[string]bool{request.ID: expanded}
				lines, ok := m.questionHistoryLines(thread, activity, 80)
				if !ok {
					t.Fatal("submitted answer did not render")
				}
				statusLine := questionHistoryStatusLine(t, lines)
				// The mark carries the delivery color; failed or uncertain
				// delivery words share it, otherwise the heading is muted.
				if statusLine.leadFG != tc.color || statusLine.fg != tc.color {
					t.Errorf("expanded=%v status mark color %q (text %q), want %q", expanded, statusLine.leadFG, statusLine.fg, tc.color)
				}
				text := questionHistoryRenderedText(lines)
				if !strings.Contains(text, tc.status) || strings.Contains(strings.ToUpper(text), "ANSWERED") {
					t.Errorf("expanded=%v status overstated delivery: %q", expanded, text)
				}
			}
			after, err := json.Marshal(thread.Requests[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("render changed request delivery state: before=%s after=%s", before, after)
			}
		})
	}
}

// A single-choice question answered through Other… keeps the filled radio of
// a selected option in the card, its compact preview and the copied text; the
// answered check is reserved for open-ended answers.
func TestQuestionHistorySelectedOtherUsesRadioMarker(t *testing.T) {
	m := testModel()
	request := protocol.Request{
		ID: "history-other", Kind: "question", State: "resolved", Delivery: "fixture-confirmed", SubmissionID: "answer-other",
		Questions: []protocol.Question{
			{ID: "layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}, AllowOther: true},
			{ID: "note", Text: "Anything else?", Kind: "text"},
		},
		QuestionAnswers: []protocol.Answer{{Text: "Wide inspector"}, {Text: "Keep it calm"}},
	}
	activity := protocol.Activity{ID: "question-answer:history-other:1", Role: "question-answer", RequestID: request.ID}
	thread := protocol.Thread{ID: "thread", Requests: []protocol.Request{request}, Activity: []protocol.Activity{activity}}
	radioOn, radio, check := m.questionMarker("single", true), m.questionMarker("single", false), m.icon("check")

	lines, _ := m.questionHistoryLines(thread, activity, 80)
	card := questionHistoryRenderedText(lines)
	for _, want := range []string{radioOn + " Other · Wide inspector", radio + " Compact", radio + " Roomy", check + " Keep it calm"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, check+" Other") {
		t.Fatalf("card marks Other with the answered check:\n%s", card)
	}
	if got := m.questionHistoryAnswerPreview(request.Questions[0], request.QuestionAnswers[0]); got != radioOn+" Other: Wide inspector" {
		t.Fatalf("preview = %q", got)
	}
	if got := m.questionHistoryAnswerPreview(request.Questions[1], request.QuestionAnswers[1]); got != check+" Keep it calm" {
		t.Fatalf("open-ended preview = %q", got)
	}
	copied := questionHistoryText(request)
	for _, want := range []string{"○ Compact", "○ Roomy", "◉ Other · Wide inspector", "✓ Keep it calm"} {
		if !strings.Contains(copied, want) {
			t.Fatalf("copy missing %q:\n%s", want, copied)
		}
	}
}

// The answered card has one muted rest outline on all four sides, like the
// prompt and pending card; delivery status colours only its header text.
func TestQuestionHistoryOutlineIsUniform(t *testing.T) {
	for _, delivery := range []string{"fixture-confirmed", "acp-undeliverable", "acp-uncertain"} {
		m := testModel()
		request := protocol.Request{
			ID: "history-outline", Kind: "question", State: "resolved", Delivery: delivery, SubmissionID: "answer-outline",
			Questions:       []protocol.Question{{ID: "layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}}},
			QuestionAnswers: []protocol.Answer{{Choices: []string{"Compact"}}},
		}
		thread := &m.snapshot.Threads[0]
		thread.Requests = []protocol.Request{request}
		thread.Activity = []protocol.Activity{{ID: "question-answer:history-outline:1", Role: "question-answer", RequestID: request.ID}}
		thread.Queue = nil
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m.viewState().Scroll = 0
		f := m.render()
		lines := m.transcriptLines(*thread, f.transcript.W)
		if len(lines) < 5 || !lines[0].outset {
			t.Fatalf("%s: transcript does not start with the card", delivery)
		}
		height := slices.IndexFunc(lines, func(l contentLine) bool { return !l.outset })
		// The card ends at the prompt outline's extent and hugs its content.
		right := f.transcript.X + f.transcript.W + 1
		box := shell.Rect{X: right - lines[0].boxW, Y: f.transcript.Y, W: lines[0].boxW, H: height}
		p := m.colors()
		assertUniformOutline(t, m, f, box, m.containerStyle(false, p.text, p.panel).border)
		status := strings.Index(ansi.Strip(f.rows[box.Y+1]), "ANSWERED")
		if delivery != "fixture-confirmed" {
			status = strings.Index(ansi.Strip(f.rows[box.Y+1]), "SUBMITTED")
		}
		if status < 0 {
			t.Fatalf("%s: status header missing: %q", delivery, ansi.Strip(f.rows[box.Y+1]))
		}
	}
}

// The answered card is the user's contribution, drawn like the user message
// box: right-aligned at the prompt outline's extent, hugging its content up to
// the same 80% cap, with the canvas left of a narrow card.
func TestQuestionHistoryCardHugsContentAtUserBoxCap(t *testing.T) {
	m := testModel()
	short := protocol.Request{
		ID: "history-short", Kind: "question", State: "resolved", Delivery: "fixture-confirmed", SubmissionID: "answer-short",
		Questions:       []protocol.Question{{ID: "layout", Text: "Layout?", Kind: "single", Options: []string{"A", "B"}}},
		QuestionAnswers: []protocol.Answer{{Choices: []string{"A"}}},
	}
	long := protocol.Request{
		ID: "history-long", Kind: "question", State: "resolved", Delivery: "fixture-confirmed", SubmissionID: "answer-long",
		Questions:       []protocol.Question{{ID: "text", Text: strings.Repeat("x", 300), Kind: "text"}},
		QuestionAnswers: []protocol.Answer{{Text: strings.Repeat("y", 300)}},
	}
	thread := &m.snapshot.Threads[0]
	thread.Requests = []protocol.Request{short, long}
	thread.Activity = []protocol.Activity{
		{ID: "question-answer:short", Role: "question-answer", RequestID: short.ID},
		{ID: "question-answer:long", Role: "question-answer", RequestID: long.ID},
	}
	thread.Queue = nil
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.viewState().Scroll, m.viewState().Pinned = 0, false
	f := m.render()
	r := f.transcript
	extent := r.W + 2
	maxBox := max(min(extent, 24), extent*4/5)
	lines := m.transcriptLines(*thread, r.W)
	var widths []int
	for i, line := range lines {
		if line.outset && (i == 0 || !lines[i-1].outset) {
			widths = append(widths, line.boxW)
		}
		if line.outset && ansi.StringWidth(line.text) != line.boxW {
			t.Fatalf("row %d text width %d, want its card width %d", i, ansi.StringWidth(line.text), line.boxW)
		}
	}
	if len(widths) != 2 || widths[0] >= maxBox || widths[1] != maxBox {
		t.Fatalf("card widths %v, want a hugging card below and a capped card at %d", widths, maxBox)
	}
	right := r.X + r.W + 1
	p := m.colors()
	for i := 0; i < len(lines) && i < r.H; i++ {
		if !lines[i].outset {
			continue
		}
		row := f.rows[r.Y+i]
		left := right - lines[i].boxW
		if got := ansi.Strip(cutCells(row, right-1, right)); !strings.ContainsAny(got, "│╮╯") {
			t.Fatalf("row %d right edge %q is not at the extent", i, got)
		}
		if got := ansi.Strip(cutCells(row, left, left+1)); !strings.ContainsAny(got, "│╭╰") {
			t.Fatalf("row %d left edge %q is not at x=%d", i, got, left)
		}
		if left > r.X && !backgroundAt(f, left-1, r.Y+i, p.canvas) {
			t.Fatalf("row %d paints beyond its card's left edge", i)
		}
	}
}
