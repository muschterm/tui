package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Other is a local selection, independent of its unfinished text. Answer.Text
// alone cannot represent a checked but still empty Other field.
type answerDraft struct {
	Choices []string
	Text    string
	Other   bool
}

func questionDraftKey(r protocol.Request) string {
	key, _ := json.Marshal([]any{r.ID, r.Revision, r.Questions})
	return fmt.Sprintf("%x", sha256.Sum256(key))
}

func (m *Model) questionDraft(r protocol.Request, i int) answerDraft {
	drafts := m.viewState().QuestionDrafts[questionDraftKey(r)]
	if i < 0 || i >= len(drafts) {
		return answerDraft{}
	}
	return drafts[i]
}

func (m *Model) saveQuestionDraft(r protocol.Request, i int, draft answerDraft) {
	m.clearRequestFeedback(m.state.Active, r.ID, r.Revision, true)
	v := m.viewState()
	if v.QuestionDrafts == nil {
		v.QuestionDrafts = map[string][]answerDraft{}
	}
	key := questionDraftKey(r)
	drafts := v.QuestionDrafts[key]
	for len(drafts) < len(r.Questions) {
		drafts = append(drafts, answerDraft{})
	}
	drafts[i] = draft
	v.QuestionDrafts[key] = drafts
	m.markDirty()
}

func draftAnswer(q protocol.Question, d answerDraft) protocol.Answer {
	a := protocol.Answer{Choices: slices.Clone(d.Choices)}
	if protocol.QuestionKind(q) == "text" || d.Other {
		a.Text = d.Text
	}
	return a
}

func validateDraft(q protocol.Question, d answerDraft) error {
	if d.Other && strings.TrimSpace(d.Text) == "" {
		return fmt.Errorf("enter an answer for Other or deselect it")
	}
	return protocol.ValidateAnswer(q, draftAnswer(q, d))
}

// Upgrade saved text drafts once, against the snapshot loaded at startup.
// Later revisions/schema changes get separate drafts, never automatic answers.
func (m *Model) migrateQuestionDrafts() {
	for _, t := range m.snapshot.Threads {
		v := m.state.Threads[t.ID]
		if v == nil {
			continue
		}
		if v.QuestionDrafts == nil {
			v.QuestionDrafts = map[string][]answerDraft{}
		}
		for _, r := range t.Requests {
			key := questionDraftKey(r)
			if _, exists := v.QuestionDrafts[key]; exists || len(v.Answers[r.ID]) == 0 {
				continue
			}
			drafts := make([]answerDraft, len(r.Questions))
			for i, text := range v.Answers[r.ID] {
				if i >= len(drafts) {
					break
				}
				if protocol.QuestionKind(r.Questions[i]) != "text" && slices.Contains(r.Questions[i].Options, text) {
					drafts[i].Choices = []string{text}
				} else {
					drafts[i].Text = text
					drafts[i].Other = text != "" && protocol.QuestionKind(r.Questions[i]) != "text"
				}
			}
			v.QuestionDrafts[key] = drafts
			delete(v.Answers, r.ID)
		}
	}
}

func (m *Model) loadAnswer() {
	m.pinRequest()
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		m.answer.SetValue("")
		return
	}
	i := min(max(0, m.viewState().QuestionIndex), len(r.Questions)-1)
	m.viewState().QuestionIndex = i
	m.answer.SetValue(m.questionDraft(r, i).Text)
	m.answerView.Reset()
}

func (m *Model) storeAnswer(text string) {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	i := m.viewState().QuestionIndex
	d := m.questionDraft(r, i)
	d.Text = text
	if protocol.QuestionKind(r.Questions[i]) != "text" {
		d.Other = true
		if protocol.QuestionKind(r.Questions[i]) == "single" {
			d.Choices = nil
		}
	}
	m.saveQuestionDraft(r, i, d)
}

func (m *Model) selectQuestion(index int) {
	r, ok := m.request()
	if !ok || index < 0 || index >= len(r.Questions) {
		return
	}
	v := m.viewState()
	v.QuestionIndex, v.RequestScroll = index, 0
	m.loadAnswer()
	m.configureInputs()
	if protocol.QuestionKind(r.Questions[index]) == "text" || m.questionDraft(r, index).Other {
		m.setFocus("answer")
	} else {
		m.focusQuestionOption(0)
	}
}

func (m *Model) chooseAnswer(value string) {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	i := m.viewState().QuestionIndex
	q := r.Questions[i]
	if !slices.Contains(q.Options, value) || protocol.QuestionKind(q) == "text" {
		return
	}
	d := m.questionDraft(r, i)
	if protocol.QuestionKind(q) == "multiple" {
		if j := slices.Index(d.Choices, value); j >= 0 {
			d.Choices = slices.Delete(slices.Clone(d.Choices), j, j+1)
		} else {
			d.Choices = append(slices.Clone(d.Choices), value)
		}
	} else {
		d.Choices, d.Other = []string{value}, false
	}
	m.saveQuestionDraft(r, i, d)
	if protocol.QuestionKind(q) == "single" && i+1 < len(r.Questions) {
		m.selectQuestion(i + 1)
	}
}

func (m *Model) toggleOther() {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	i := m.viewState().QuestionIndex
	q := r.Questions[i]
	if !protocol.QuestionAllowsOther(q) {
		return
	}
	d := m.questionDraft(r, i)
	d.Other = !d.Other
	if d.Other && protocol.QuestionKind(q) == "single" {
		d.Choices = nil
	}
	m.saveQuestionDraft(r, i, d)
	m.loadAnswer()
	m.configureInputs()
	if d.Other {
		m.setFocus("answer")
	}
}

func (m *Model) submitAnswers(a action) tea.Cmd {
	r, ok := m.request()
	if !ok || r.Kind == "approval" {
		return nil
	}
	reject := func(message string, validation bool) tea.Cmd {
		m.status = message
		m.setRequestFeedback(m.state.Active, r.ID, r.Revision, message, validation)
		m.configureInputs()
		return nil
	}
	if m.thread().NeedsResume {
		return reject("Resume this thread first (F4 → Resume).", false)
	}
	if !m.connected {
		return reject("Disconnected · wait for the server to reconnect.", false)
	}
	answers := make([]protocol.Answer, len(r.Questions))
	legacy := true
	for i, q := range r.Questions {
		legacy = legacy && q.Kind == ""
		d := m.questionDraft(r, i)
		if err := validateDraft(q, d); err != nil {
			m.selectQuestion(i)
			return reject(fmt.Sprintf("Question %d: %s", i+1, err), true)
		}
		answers[i] = draftAnswer(q, d)
	}
	c := protocol.Command{Kind: "request.answer", TargetID: r.ID, Revision: r.Revision, QuestionAnswers: answers}
	if legacy {
		// A still-running original server only accepts Answers. Its untyped
		// questions support one choice or free text, so this is lossless.
		c.QuestionAnswers = nil
		c.Answers = make([]string, len(answers))
		for i, answer := range answers {
			c.Answers[i] = answer.Text
			if len(answer.Choices) == 1 {
				c.Answers[i] = answer.Choices[0]
			}
		}
	}
	cmd := m.command(c, a)
	if cmd == nil {
		return reject(m.status, false)
	}
	m.clearRequestFeedback(m.state.Active, r.ID, r.Revision, false)
	return cmd
}
