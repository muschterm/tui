package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

func manyQuestionsModel() (*Model, protocol.Request) {
	m, req := questionReviewModel()
	req.Questions = nil
	for i := 0; i < 9; i++ {
		req.Questions = append(req.Questions, protocol.Question{ID: fmt.Sprint(i), Label: fmt.Sprint("Review ", i+1), Kind: "single", Text: "Which area?", Options: []string{"Layout", "Keyboard"}})
	}
	m.snapshot.Threads[0].Requests = []protocol.Request{req}
	m.loadAnswer()
	m.configureInputs()
	return m, req
}

func TestQuestionTabsReserveArrowsAndKeepCurrentVisible(t *testing.T) {
	for _, height := range []int{1} {
		for _, plain := range []bool{false, true} {
			m, req := manyQuestionsModel()
			m.plainIcons = plain
			req.Questions[3].Label = strings.Repeat("界é", 12)
			m.snapshot.Threads[0].Requests[0] = req
			for _, width := range []int{28, 34, 48, 80, 200} {
				for active := range req.Questions {
					m.viewState().QuestionIndex = active
					r := shell.Rect{X: 5, W: width, H: height}
					f := frame{rows: make([]string, height)}
					for row := range f.rows {
						f.rows[row] = strings.Repeat(" ", width+10)
					}
					m.renderQuestionTabs(&f, r, req)
					current := controlHit(t, f, fmt.Sprint("question-page:", active))
					if current.Rect.W < 3 || hasControl(f, "question-back") != (active > 0) || hasControl(f, "question-next") != (active+1 < len(req.Questions)) {
						t.Fatal("current question or end controls incorrect")
					}
					visible := 0
					for i, h := range f.hits {
						if h.Rect.X < r.X || h.Rect.X+h.Rect.W > r.X+r.W || h.Rect.Y != r.Y || h.Rect.H != height {
							t.Fatal("header control outside bounds", h)
						}
						for _, other := range f.hits[:i] {
							if h.Rect.X < other.Rect.X+other.Rect.W && other.Rect.X < h.Rect.X+h.Rect.W {
								t.Fatal("header controls overlap", h, other)
							}
						}
						text := ansi.Strip(ansi.Cut(f.rows[height/2], h.Rect.X, h.Rect.X+h.Rect.W))
						if strings.HasPrefix(h.Key, "question-page:") {
							visible++
							if h.Rect.X < r.X+questionArrowWidth+1 || h.Rect.X+h.Rect.W > r.X+r.W-questionArrowWidth-1 {
								t.Fatal("question tab entered a reserved arrow slot", h)
							}

						}
						if h.Key == "question-back" && (h.Rect.X != r.X || !strings.Contains(text, m.icon("previous"))) {
							t.Fatal("Back moved or did not render its icon")
						}
						if h.Key == "question-next" && (h.Rect.X+h.Rect.W != r.X+r.W || !strings.Contains(text, m.icon("next"))) {
							t.Fatal("Next moved or did not render its icon")
						}
					}
					if hasControl(f, "question-tabs") != (visible < len(req.Questions)) {
						t.Fatal("overflow shown without hidden questions")
					}
				}
			}
		}
	}
}

func TestQuestionArrowNavigationRevealsPagesWithoutSubmitting(t *testing.T) {
	for _, height := range []int{22, 45} {
		for _, pointer := range []bool{false, true} {
			m, req := manyQuestionsModel()
			m.Update(tea.WindowSizeMsg{Width: 48, Height: height})
			m.prompt.SetValue("unsent prompt")
			m.viewState().Draft = m.prompt.Value()
			m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Layout"}})
			for _, direction := range []int{1, -1} {
				for step := 0; step < len(req.Questions)-1; step++ {
					key := "question-next"
					if direction < 0 {
						key = "question-back"
					}
					want := m.viewState().QuestionIndex + direction
					h := controlHit(t, m.measure(), key)
					if pointer {
						clickControl(m, h)
					} else {
						m.setFocus(key)
						m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					}
					if m.viewState().QuestionIndex != want || !hasControl(m.measure(), fmt.Sprint("question-page:", want)) || m.busy != nil {
						t.Fatal("arrow did not reveal the next current question without submitting")
					}
				}
			}
			if m.prompt.Value() != "unsent prompt" || len(m.questionDraft(req, 0).Choices) != 1 {
				t.Fatal("navigation discarded prompt or answer draft")
			}
			clickControl(m, controlHit(t, m.measure(), "question-tabs"))
			if len(m.menu) != len(req.Questions) {
				t.Fatal("question overflow lost direct access to a page")
			}
			m.menuIndex = 7
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.viewState().QuestionIndex != 7 || !hasControl(m.measure(), "question-page:7") || m.busy != nil {
				t.Fatal("overflow failed to select and reveal the chosen page")
			}
		}
	}
}

func TestQuestionAnsweredMarkerKeepsTabGeometry(t *testing.T) {
	m, req := questionReviewModel()
	r := shell.Rect{W: 70, H: 1}
	before, after := frame{}, frame{}
	m.renderQuestionTabs(&before, r, req)
	m.saveQuestionDraft(req, 0, answerDraft{Choices: []string{"Compact"}})
	m.renderQuestionTabs(&after, r, req)
	for i, h := range before.hits {
		if after.hits[i].Rect != h.Rect || after.hits[i].Key != h.Key {
			t.Fatal("answering moved the header controls")
		}
	}
	if !strings.Contains(controlHit(t, after, "question-page:0").Label, "✓") {
		t.Fatal("answered marker was lost")
	}
}

func TestApprovalOverflowHasNoHiddenApproveTargets(t *testing.T) {
	m, req := questionReviewModel()
	req.Kind, req.Choices = "approval", []string{"Allow once", "Always allow this action", "Reject"}
	m.snapshot.Threads[0].Requests = []protocol.Request{req}
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 22})
	f := m.render()
	if !hasControl(f, "approval-options") {
		t.Fatal("narrow approval missing choice menu")
	}
	for _, h := range f.hits {
		if h.Action.Kind == "approve" {
			t.Fatal("invisible approval target under overflow button", h)
		}
	}
}
