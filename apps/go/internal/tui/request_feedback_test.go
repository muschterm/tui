package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

type legacyAnswerTransport struct {
	t       *testing.T
	answers []string
	calls   int
}

func (s *legacyAnswerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Original command schema: strict decoding must reject the newer field.
	var c struct {
		Version                                      int
		ID, Kind, ThreadID, TargetID, ClientID, Text string
		Revision                                     int64
		Settings                                     *protocol.Settings
		Attachments                                  []protocol.Attachment
		Order, Answers                               []string
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		s.t.Fatal(err)
	}
	if r.URL.Path != "/v1/command" || c.Kind != "request.answer" || c.TargetID != "legacy-request" || c.Revision != 4 {
		s.t.Fatalf("wrong request identity: %+v", c)
	}
	s.answers, s.calls = c.Answers, s.calls+1
	b, _ := json.Marshal(protocol.Receipt{ID: c.ID, State: "accepted", TargetID: c.TargetID})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
}

func TestMouseSubmitPreservesLegacyQuestionWireContract(t *testing.T) {
	m := testModel()
	r := protocol.Request{ID: "legacy-request", Kind: "question", State: "pending", Revision: 4, Questions: []protocol.Question{
		{ID: "choice", Options: []string{"A", "B"}}, {ID: "other", Options: []string{"C"}},
	}}
	m.snapshot.Threads[0].Requests = []protocol.Request{r}
	m.snapshot.Threads[0].NeedsResume = false
	m.saveQuestionDraft(r, 0, answerDraft{Choices: []string{"B"}})
	m.saveQuestionDraft(r, 1, answerDraft{Other: true, Text: "Custom choice"})
	m.prompt.SetValue("preserved prompt")
	m.viewState().Draft = m.prompt.Value()
	transport := &legacyAnswerTransport{t: t}
	m.client = &client.Client{Discovery: protocol.Discovery{URL: "http://test"}, HTTP: &http.Client{Transport: transport}}
	m.configureInputs()
	h := controlHit(t, m.render(), "answer-submit")
	cmd := m.mouse(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("visible Submit did not dispatch")
	}
	msg := cmd().(commandMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	m.Update(msg)
	if transport.calls != 1 || !reflect.DeepEqual(transport.answers, []string{"B", "Custom choice"}) || m.prompt.Value() != "preserved prompt" || m.busy != nil {
		t.Fatal("legacy Submit lost answers, prompt or acknowledgment")
	}
}

func TestQuestionValidationRemainsVisibleUnderSubmitHover(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}} {
		m, r := questionReviewModel()
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.prompt.SetValue("preserved")
		m.configureInputs()
		h := controlHit(t, m.render(), "answer-submit")
		m.hover = h.Key
		m.mouse(tea.MouseClickMsg{X: h.Rect.X, Y: h.Rect.Y, Button: tea.MouseLeft})
		f := m.render()
		submit := controlHit(t, f, "answer-submit")
		if !strings.Contains(ansi.Strip(f.rows[submit.Rect.Y-1]), "Question 1: answer is required") || m.busy != nil || m.prompt.Value() != "preserved" {
			t.Fatalf("%v: invalid Submit appeared inert or lost draft", size)
		}
		if submit.Rect.Y >= f.request.Y+f.request.H || f.prompt.Y <= submit.Rect.Y || f.request.H > maxQuestionCardRows {
			t.Fatalf("%v: error displaced fixed controls", size)
		}
		if !reflect.DeepEqual(f.hits, m.measure().hits) {
			t.Fatal("error broke paint/hit agreement")
		}
		m.chooseAnswer("Compact")
		if message, _ := m.requestNotice(r); message != "" {
			t.Fatal("correcting validation did not clear feedback")
		}
	}
}

func TestQuestionSubmissionFeedbackAndRevisionIsolation(t *testing.T) {
	m, r := questionReviewModel()
	m.chooseAnswer("Compact")
	m.chooseAnswer("Files")
	m.hover = "answer-submit"
	m.submitAnswers(action{Kind: "answer-submit"})
	c := *m.busy
	if message, _ := m.requestNotice(r); message != "Submitting answer…" {
		t.Fatal("missing progress feedback")
	}
	m.Update(commandMsg{command: c, err: &protocol.Error{Code: "invalid", Message: "server rejected answer"}})
	f := m.render()
	h := controlHit(t, f, "answer-submit")
	if !strings.Contains(ansi.Strip(f.rows[h.Rect.Y-1]), "server rejected answer") || m.busy != nil {
		t.Fatal("server rejection hidden by hover")
	}
	changed := r
	changed.Revision++
	if message, _ := m.requestNotice(changed); message != "" {
		t.Fatal("feedback leaked into a changed request")
	}
	m.selectThread(m.snapshot.Threads[1].ID)
	if message, _ := m.requestNotice(r); message != "" {
		t.Fatal("feedback leaked into another thread")
	}
	m.selectThread(c.ThreadID)
	m.submitAnswers(action{Kind: "answer-submit"})
	c = *m.busy
	m.Update(commandMsg{command: c, err: errors.New("connection interrupted")})
	if message, _ := m.requestNotice(r); !strings.Contains(message, "unconfirmed") || m.busy == nil || m.busy.ID != c.ID {
		t.Fatal("uncertain submission lost retry identity or notice")
	}
}

func TestQuestionSubmitDoesNotImplicitlyResumeOrSendOffline(t *testing.T) {
	for _, resume := range []bool{false, true} {
		m, r := questionReviewModel()
		m.snapshot.Threads[0].NeedsResume = resume
		m.connected = resume
		m.submitAnswers(action{Kind: "answer-submit"})
		if message, _ := m.requestNotice(r); message == "" || m.busy != nil {
			t.Fatal("blocked submission attempted work or hid reason")
		}
	}
}
