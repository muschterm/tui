package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

const questionHistoryCompactRows = 12

type questionHistoryRow struct {
	text, fg string
}

// questionHistoryFallbackGroup is a submitted answer without a durable
// chronology marker. A non-empty TurnID means retained activity still
// identifies the turn that owns it; an empty TurnID means it belongs in the
// earlier-history section before the retained timeline.
type questionHistoryFallbackGroup struct {
	TurnID string
	Lines  []contentLine
}

// questionHistoryLines renders a durable answer at its server-recorded place
// in the transcript. Activity is only an ordering/link marker; the Request
// remains the canonical question, answer and delivery record.
func (m *Model) questionHistoryLines(t protocol.Thread, activity protocol.Activity, width int) ([]contentLine, bool) {
	if activity.Role != "question-answer" {
		return nil, false
	}
	if activity.RequestID == "" || !firstQuestionHistoryMarker(t, activity) {
		return nil, true
	}
	request, ok := questionRequestByID(t, activity.RequestID)
	if !ok || !questionHistoryHasSubmission(request) {
		return nil, true
	}
	return m.questionHistoryCard(request, width, activity.State == "question-history-position-unavailable"), true
}

// questionHistoryFallbackLines shows accepted answers from older snapshots or
// trimmed transcript ranges that lack a linked chronology marker. Their prior
// position is unknown, so the card says so instead of assigning a timestamp or
// pretending that its current location was the original resolution point.
func (m *Model) questionHistoryFallbackLines(t protocol.Thread, width int) []contentLine {
	var lines []contentLine
	for _, group := range m.questionHistoryFallbackGroups(t, width) {
		lines = append(lines, group.Lines...)
	}
	return lines
}

// questionHistoryFallbackGroups places each unanchored answer using only
// retained turn identity. Its location within that turn is unknown, so the
// renderer can place it after the turn's last retained activity and preserve
// the explicit position-unavailable label. If no activity from the request's
// turn remains, the card has no TurnID and belongs before the retained
// timeline.
func (m *Model) questionHistoryFallbackGroups(t protocol.Thread, width int) []questionHistoryFallbackGroup {
	anchored := make(map[string]bool)
	for _, activity := range t.Activity {
		if activity.Role == "question-answer" && activity.RequestID != "" {
			anchored[activity.RequestID] = true
		}
	}
	retainedTurns := make(map[string]bool)
	for _, activity := range t.Activity {
		if activity.TurnID != "" {
			retainedTurns[activity.TurnID] = true
		}
	}
	var groups []questionHistoryFallbackGroup
	for _, request := range t.Requests {
		if request.Kind != "question" || anchored[request.ID] || !questionHistoryHasSubmission(request) {
			continue
		}
		turnID := request.TurnID
		if !retainedTurns[turnID] {
			turnID = ""
		}
		lines := m.questionHistoryCard(request, width, true)
		if len(lines) == 0 {
			continue
		}
		groups = append(groups, questionHistoryFallbackGroup{
			TurnID: turnID,
			Lines:  lines,
		})
	}
	return groups
}

func firstQuestionHistoryMarker(t protocol.Thread, target protocol.Activity) bool {
	for _, activity := range t.Activity {
		if activity.Role == "question-answer" && activity.RequestID == target.RequestID {
			return activity.ID == target.ID
		}
	}
	return false
}

func questionRequestByID(t protocol.Thread, id string) (protocol.Request, bool) {
	for _, request := range t.Requests {
		if request.ID == id {
			return request, true
		}
	}
	return protocol.Request{}, false
}

func questionHistoryHasSubmission(request protocol.Request) bool {
	if request.Kind != "question" || len(request.Questions) == 0 {
		return false
	}
	if request.SubmissionID != "" || (request.State == "resolved" && deliveryConfirmed(request.Delivery)) {
		return true
	}
	// Older fixture records did not retain a submission identity. Their
	// resolved state plus preserved answers is the only available evidence.
	return request.State == "resolved" && (len(request.QuestionAnswers) > 0 || len(request.Answers) > 0)
}

func (m *Model) questionHistoryCard(request protocol.Request, width int, positionUnknown bool) []contentLine {
	if width < 2 {
		return nil
	}
	p := m.colors()
	b := componentBorder(roundedOutline, m.plainIcons)
	// One uniform rest outline, the prompt's and pending card's, on all four
	// sides: the delivery status is carried by the header text, never by
	// tinting one edge. Border cells sit on the canvas like componentBox's.
	outline := m.containerStyle(false, p.text, p.panel).border
	// The outline spans the transcript column plus one cell on each side: the
	// prompt outline's extent, shared with the user message box.
	boxWidth := width + 2
	contentWidth := max(1, boxWidth-4)

	rows := m.questionHistoryRows(request, positionUnknown)
	compact := questionHistoryVisualRows(rows, contentWidth) > questionHistoryCompactRows
	displayed := rows
	if compact && !m.viewState().QuestionHistoryExpanded[request.ID] {
		displayed = m.questionHistoryPreviewRows(request, contentWidth, positionUnknown)
	}

	lines := make([]contentLine, 0, len(displayed)+3)
	appendBorder := func(left, middle, right string, fg, bg string) {
		lines = append(lines, contentLine{text: left + strings.Repeat(middle, boxWidth-2) + right, fg: fg, bg: bg, outset: true})
	}
	appendRow := func(text, fg string) {
		for _, wrapped := range questionHistoryWrap(text, contentWidth) {
			lines = append(lines, contentLine{
				text: b.Left + " " + fit(wrapped, contentWidth) + " " + b.Right,
				fg:   fg, bg: p.panel, outset: true, border: outline,
			})
		}
	}
	appendBorder(b.TopLeft, b.Top, b.TopRight, outline, p.canvas)
	for _, row := range displayed {
		appendRow(row.text, row.fg)
	}
	if compact {
		label := "Expand · full questions and options"
		if m.viewState().QuestionHistoryExpanded[request.ID] {
			label = "Collapse"
		}
		lines = append(lines, contentLine{
			text: b.Left + " " + fit(label, contentWidth) + " " + b.Right,
			fg:   p.blue, bg: p.panel, outset: true, border: outline,
			action: action{Kind: "question-history-toggle", ID: request.ID},
		})
	}
	appendBorder(b.BottomLeft, b.Bottom, b.BottomRight, outline, p.canvas)
	return lines
}

func (m *Model) questionHistoryRows(request protocol.Request, positionUnknown bool) []questionHistoryRow {
	p := m.colors()
	status, confirmed := questionHistoryStatus(request)
	if positionUnknown {
		status += " · earlier position unavailable"
	}
	statusColor := questionHistoryStatusColor(request, confirmed, p)
	rows := []questionHistoryRow{{text: status, fg: statusColor}}
	if origin := m.questionHistoryOrigin(request.Origin); origin != "" {
		rows = append(rows, questionHistoryRow{text: origin, fg: p.muted})
	}
	answers := questionHistoryAnswers(request)
	for i, question := range request.Questions {
		if i > 0 {
			rows = append(rows, questionHistoryRow{text: "", fg: p.muted})
		}
		if prompt := questionHistoryPrompt(question); prompt != "" {
			rows = append(rows, questionHistoryRow{text: prompt, fg: p.text})
		}
		if request.Action != "" {
			// A decline or cancel keeps the original question and its offered
			// labels, all unselected, and records no answer.
			for _, option := range question.Options {
				rows = append(rows, questionHistoryRow{text: m.questionMarker(protocol.QuestionKind(question), false) + " " + option, fg: p.muted})
			}
			continue
		}
		var answer protocol.Answer
		if i < len(answers) {
			answer = answers[i]
		}
		selected := make(map[string]bool, len(answer.Choices))
		for _, choice := range answer.Choices {
			selected[choice] = true
		}
		for _, option := range question.Options {
			mark, fg := m.questionMarker(protocol.QuestionKind(question), false)+" ", p.muted
			if selected[option] {
				mark, fg = m.questionMarker(protocol.QuestionKind(question), true)+" ", p.text
			}
			rows = append(rows, questionHistoryRow{text: mark + option, fg: fg})
		}
		// Preserve accepted selected values even if an older option schema no
		// longer contains them; history must not silently rewrite an answer.
		for _, choice := range answer.Choices {
			if !slices.Contains(question.Options, choice) {
				rows = append(rows, questionHistoryRow{text: m.questionMarker(protocol.QuestionKind(question), true) + " " + choice, fg: p.text})
			}
		}
		if strings.TrimSpace(answer.Text) != "" {
			// A free-text Other answer is a selected choice: it takes the same
			// radio/checkbox marker as its siblings. Only an open-ended answer,
			// which has no choice control, keeps the answered check.
			label := m.icon("check") + " " + answer.Text
			if len(question.Options) > 0 {
				label = m.questionMarker(protocol.QuestionKind(question), true) + " Other · " + answer.Text
			}
			rows = append(rows, questionHistoryRow{text: label, fg: p.text})
		} else if len(answer.Choices) == 0 {
			if i >= len(answers) || protocol.QuestionRequired(question) {
				rows = append(rows, questionHistoryRow{text: "No answer recorded", fg: p.muted})
			} else {
				rows = append(rows, questionHistoryRow{text: "Skipped (optional)", fg: p.muted})
			}
		}
	}
	return rows
}

func (m *Model) questionHistoryPreviewRows(request protocol.Request, width int, positionUnknown bool) []questionHistoryRow {
	p := m.colors()
	status, confirmed := questionHistoryStatus(request)
	if positionUnknown {
		status += " · earlier position unavailable"
	}
	statusColor := questionHistoryStatusColor(request, confirmed, p)
	rows := []questionHistoryRow{{text: status, fg: statusColor}}
	if origin := m.questionHistoryOrigin(request.Origin); origin != "" {
		rows = append(rows, questionHistoryRow{text: origin, fg: p.muted})
	}
	answers := questionHistoryAnswers(request)
	hiddenOptions := 0
	for i, question := range request.Questions {
		prompt := questionHistoryPrompt(question)
		rows = append(rows, questionHistoryRow{text: questionHistoryPreview("Q · "+prompt, width), fg: p.text})
		if request.Action != "" {
			hiddenOptions += len(question.Options)
			continue
		}
		var answer protocol.Answer
		if i < len(answers) {
			answer = answers[i]
		}
		answerText := m.questionHistoryAnswerPreview(question, answer)
		if answerText == "" {
			if i >= len(answers) || protocol.QuestionRequired(question) {
				answerText = "No answer recorded"
			} else {
				answerText = "Skipped (optional)"
			}
		}
		rows = append(rows, questionHistoryRow{text: questionHistoryPreview(answerText, width), fg: p.text})
		selected := make(map[string]bool, len(answer.Choices))
		for _, value := range answer.Choices {
			selected[value] = true
		}
		for _, option := range question.Options {
			if !selected[option] {
				hiddenOptions++
			}
		}
	}
	if hiddenOptions > 0 {
		rows = append(rows, questionHistoryRow{text: fmt.Sprintf("· %d other option(s) hidden", hiddenOptions), fg: p.muted})
	} else {
		rows = append(rows, questionHistoryRow{text: "· Full question text or answers hidden", fg: p.muted})
	}
	return rows
}

func questionHistoryVisualRows(rows []questionHistoryRow, width int) int {
	count := 0
	for _, row := range rows {
		count += len(questionHistoryWrap(row.text, width))
	}
	return count
}

// questionHistoryStatus names the recorded outcome. A decline or cancel
// replaces "Answered"/"Submitted" with "Declined"/"Cancelled" and keeps the
// same delivery sub-status, so an unconfirmed decline never reads as settled.
func questionHistoryStatus(request protocol.Request) (string, bool) {
	confirmed := request.State == "resolved" && deliveryConfirmed(request.Delivery)
	outcome, pending := "Answered", "Submitted"
	if request.Action != "" {
		outcome = questionActionOutcome(request.Action)
		pending = outcome
	}
	if confirmed {
		return outcome, true
	}
	switch request.Delivery {
	case "acp-accepted":
		return pending + " · accepted by server, upstream unconfirmed", false
	case "acp-unconfirmed":
		return pending + " · provider confirmation unavailable", false
	case "acp-delivered":
		return pending + " · legacy delivery status unconfirmed", false
	case "acp-uncertain":
		return pending + " · delivery uncertain", false
	case "acp-undeliverable":
		return pending + " · not delivered", false
	case "acp-cancelled":
		return pending + " · cancelled before confirmation", false
	default:
		return pending + " · delivery status unknown", false
	}
}

func questionHistoryStatusColor(request protocol.Request, confirmed bool, p palette) string {
	if confirmed && request.Action != "" {
		// A confirmed decline or cancel is settled but not an answer: neutral.
		return p.muted
	}
	if confirmed {
		return p.green
	}
	switch request.Delivery {
	case "acp-accepted", "acp-unconfirmed", "acp-delivered":
		return p.muted
	case "acp-undeliverable":
		return p.red
	default:
		// Uncertain, cancelled and unknown outcomes remain visible as warnings.
		return p.gold
	}
}

func (m *Model) questionHistoryOrigin(origin string) string {
	origin = strings.TrimSpace(safe(origin))
	if origin == "" || strings.EqualFold(origin, "agent") {
		return ""
	}
	for _, candidate := range m.snapshot.Agents {
		if candidate.ID == origin && candidate.Name != "" {
			return safe(candidate.Name)
		}
	}
	return origin
}

func questionHistoryPrompt(question protocol.Question) string {
	prompt := question.Text
	if question.Label != "" && question.Label != question.Text {
		if prompt == "" {
			prompt = question.Label
		} else {
			prompt = question.Label + " · " + prompt
		}
	}
	return prompt
}

func (m *Model) questionHistoryAnswerPreview(question protocol.Question, answer protocol.Answer) string {
	values := append([]string(nil), answer.Choices...)
	if strings.TrimSpace(answer.Text) != "" {
		if len(question.Options) > 0 {
			values = append(values, "Other: "+answer.Text)
		} else {
			values = append(values, answer.Text)
		}
	}
	if len(values) == 0 {
		return ""
	}
	marker := m.icon("check")
	if len(answer.Choices) > 0 || len(question.Options) > 0 {
		marker = m.questionMarker(protocol.QuestionKind(question), true)
	}
	return marker + " " + strings.Join(values, " · ")
}

func questionHistoryPreview(text string, width int) string {
	text = strings.Join(strings.Fields(safe(text)), " ")
	if text == "" {
		return ""
	}
	return ansi.Truncate(text, max(1, width), "…")
}

func questionHistoryAnswers(request protocol.Request) []protocol.Answer {
	if request.QuestionAnswers != nil {
		return request.QuestionAnswers
	}
	if request.Answers != nil {
		answers, err := protocol.NormalizeQuestionAnswers(request.Questions, nil, request.Answers)
		if err == nil {
			return answers
		}
	}
	return nil
}

func questionHistoryWrap(text string, width int) []string {
	text = safe(text)
	if text == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

// copiedQuestionMarker is the clipboard form of a choice marker and its gap:
// Unicode radio and ballot boxes, independent of the user's font, so a copied
// selected Other reads like its selected siblings. The check stays reserved
// for open-ended answers, which have no choice control.
func copiedQuestionMarker(kind string, selected bool) string {
	switch {
	case kind == "multiple" && selected:
		return "☑ "
	case kind == "multiple":
		return "☐ "
	case selected:
		return "◉ "
	}
	return "○ "
}

// questionHistoryText returns the complete, selectable plain-text card for
// transcript copying and read-only inspection.
func questionHistoryText(request protocol.Request) string {
	status, _ := questionHistoryStatus(request)
	var builder strings.Builder
	builder.WriteString(status)
	if origin := strings.TrimSpace(safe(request.Origin)); origin != "" && !strings.EqualFold(origin, "agent") {
		builder.WriteString(" · ")
		builder.WriteString(origin)
	}
	answers := questionHistoryAnswers(request)
	for i, question := range request.Questions {
		builder.WriteString("\n\n")
		builder.WriteString(safe(questionHistoryPrompt(question)))
		var answer protocol.Answer
		if i < len(answers) {
			answer = answers[i]
		}
		selected := make(map[string]bool, len(answer.Choices))
		for _, choice := range answer.Choices {
			selected[choice] = true
		}
		kind := protocol.QuestionKind(question)
		for _, option := range question.Options {
			builder.WriteString("\n")
			builder.WriteString(copiedQuestionMarker(kind, selected[option]))
			builder.WriteString(safe(option))
		}
		if request.Action != "" {
			continue
		}
		for _, choice := range answer.Choices {
			if !slices.Contains(question.Options, choice) {
				builder.WriteString("\n")
				builder.WriteString(copiedQuestionMarker(kind, true))
				builder.WriteString(safe(choice))
			}
		}
		if strings.TrimSpace(answer.Text) != "" {
			builder.WriteString("\n")
			if len(question.Options) > 0 {
				builder.WriteString(copiedQuestionMarker(kind, true))
				builder.WriteString("Other · ")
			} else {
				builder.WriteString("✓ ")
			}
			builder.WriteString(safe(answer.Text))
		} else if len(answer.Choices) == 0 {
			builder.WriteString("\n")
			if i < len(answers) && !protocol.QuestionRequired(question) {
				builder.WriteString("Skipped (optional)")
			} else {
				builder.WriteString("No answer recorded")
			}
		}
	}
	return builder.String()
}
