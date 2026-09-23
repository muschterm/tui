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

// Drafts belong to a request identity and its question schema. A revision
// bump with unchanged questions keeps the draft; the revision guards only the
// submission. A changed schema gets a fresh draft while the old one is kept
// for recovery until the request is no longer pending.
func questionDraftKey(r protocol.Request) string {
	schema, _ := json.Marshal(r.Questions)
	return r.ID + "#" + fmt.Sprintf("%x", sha256.Sum256(schema))
}

// legacyQuestionDraftKey is the pre-2026-09-23 revision-bound key.
func legacyQuestionDraftKey(r protocol.Request) string {
	key, _ := json.Marshal([]any{r.ID, r.Revision, r.Questions})
	return fmt.Sprintf("%x", sha256.Sum256(key))
}

func questionDraftRequestID(key string) (string, bool) {
	id, _, ok := strings.Cut(key, "#")
	return id, ok
}

// pruneQuestionDrafts drops drafts whose request is no longer live (pending or
// submitted) in the current snapshot, including unmigrated revision-bound
// drafts. A submitted request keeps its draft until it resolves so a failed
// handoff never loses typed text. Threads absent from the snapshot are left
// alone. It reports whether it removed any.
func (m *Model) pruneQuestionDrafts() bool {
	pruned := false
	for _, t := range m.snapshot.Threads {
		v := m.state.Threads[t.ID]
		if v == nil || len(v.QuestionDrafts) == 0 {
			continue
		}
		pending := map[string]bool{}
		for _, r := range t.Requests {
			if r.State == "pending" || r.State == "submitted" {
				pending[r.ID] = true
			}
		}
		for key := range v.QuestionDrafts {
			if id, ok := questionDraftRequestID(key); !ok || !pending[id] {
				delete(v.QuestionDrafts, key)
				pruned = true
			}
		}
	}
	return pruned
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
			// Revision-bound drafts of the loaded revision move to the schema
			// key once; other legacy keys are pruned with resolved requests.
			if legacy, ok := v.QuestionDrafts[legacyQuestionDraftKey(r)]; ok {
				if _, exists := v.QuestionDrafts[key]; !exists {
					v.QuestionDrafts[key] = legacy
				}
				delete(v.QuestionDrafts, legacyQuestionDraftKey(r))
			}
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
	i := m.questionIndex(r)
	m.viewState().QuestionIndex = i
	m.answer.SetValue(m.questionDraft(r, i).Text)
	m.answerView.Reset()
}

func (m *Model) storeAnswer(text string) {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	i := m.questionIndex(r)
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
	// Back, Next, tabs and the header only navigate: focus stays on the
	// activating control, or the active tab when that arrow disappeared.
	// Answering (a choice, the answer field, validation) moves on to the new
	// question's input. Read focus first: re-measuring drops a vanished key.
	focus := m.focus
	v := m.viewState()
	v.QuestionIndex, v.RequestScroll = index, 0
	m.loadAnswer()
	m.configureInputs()
	if questionNavigationFocus(focus) {
		m.setFocus(focus)
		if !hasHit(m.measure(), focus) {
			m.setFocus(fmt.Sprint("question-page:", index))
		}
		return
	}
	if protocol.QuestionKind(r.Questions[index]) == "text" || m.questionDraft(r, index).Other {
		m.setFocus("answer")
	} else {
		m.focusQuestionOption(0)
	}
}

func hasHit(f frame, key string) bool {
	return slices.ContainsFunc(f.hits, func(h hit) bool { return h.Key == key })
}

// chooseQuestionOption selects (radio) or toggles (checkbox) the option with
// index n of the active question, as a digit key does. Other focuses its
// field; a radio selection advances as a click does.
func (m *Model) chooseQuestionOption(n int) {
	r, ok := m.request()
	if !ok {
		return
	}
	options := m.questionOptions(r)
	if n < 0 || n >= len(options) {
		return
	}
	o := options[n]
	if o.key != "answer-other" {
		before := m.questionIndex(r)
		m.chooseAnswer(o.label)
		// A tab-focused selection that advanced follows the active tab.
		if strings.HasPrefix(m.focus, "question-page:") && m.questionIndex(r) != before {
			m.setFocus(fmt.Sprint("question-page:", m.questionIndex(r)))
		}
		return
	}
	if q, _, _ := m.activeQuestion(r); o.selected && protocol.QuestionKind(q) == "single" {
		m.setFocus("answer")
		return
	}
	m.toggleOther()
}

func (m *Model) chooseAnswer(value string) {
	r, ok := m.request()
	if !ok || len(r.Questions) == 0 {
		return
	}
	i := m.questionIndex(r)
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
	i := m.questionIndex(r)
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

// submitRequestAction declines or cancels the visible question request
// through the same gates and feedback as Submit. It sends no answers and
// leaves every question draft intact, so a refused or failed action can be
// followed by an ordinary answer.
func (m *Model) submitRequestAction(a action) tea.Cmd {
	r, ok := m.request()
	if !ok || r.Kind == "approval" {
		return nil
	}
	reject := func(message string) tea.Cmd {
		m.status = message
		m.setRequestFeedback(m.state.Active, r.ID, r.Revision, message, false)
		m.configureInputs()
		return nil
	}
	if !slices.Contains(questionOfferedActions(r), a.Value) {
		return reject("This request does not offer " + strings.ToLower(questionActionLabel(a.Value)) + " · nothing sent")
	}
	if m.thread().NeedsResume {
		return reject("Resume this thread first (F4 → Resume).")
	}
	if !m.connected {
		return reject("Disconnected · wait for the server to reconnect.")
	}
	c := protocol.Command{Kind: "request.answer", TargetID: r.ID, Revision: r.Revision, RequestAction: a.Value}
	cmd := m.command(c, a)
	if cmd == nil {
		return reject(m.status)
	}
	m.clearRequestFeedback(m.state.Active, r.ID, r.Revision, false)
	return cmd
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
