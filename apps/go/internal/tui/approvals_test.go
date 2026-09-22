package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func approvalTestModel(ids bool) *Model {
	m := testModel()
	m.snapshot.Threads[0].NeedsResume = false
	m.snapshot.Threads[0].Requests = []protocol.Request{{ID: "permission", Revision: 4, Kind: "approval", State: "pending", Choices: []string{"Allow", "Allow"}, ChoiceIDs: []string{"once", "always"}}}
	if ids {
		m.snapshot.Capabilities = append(m.snapshot.Capabilities, "approval-choice-ids")
	}
	m.prompt.SetValue("preserved draft")
	return m
}

func TestApprovalDuplicateLabelsUseExactIDs(t *testing.T) {
	for _, menu := range []bool{false, true} {
		for i, want := range []string{"once", "always"} {
			m := approvalTestModel(true)
			var a action
			if menu {
				m.activate(action{Kind: "approval-options"})
				a = m.menu[i].Action
			} else {
				a = controlHit(t, m.render(), "approve:"+string(rune('0'+i))).Action
			}
			m.activate(a)
			if m.busy == nil || m.busy.ApprovalChoiceID != want || len(m.busy.Answers) != 0 || len(m.busy.QuestionAnswers) != 0 || m.busy.TargetID != "permission" || m.busy.Revision != 4 {
				t.Fatalf("menu=%v index=%d: wrong approval command: %+v", menu, i, m.busy)
			}
			if m.prompt.Value() != "preserved draft" {
				t.Fatal("approval changed composer draft")
			}
		}
	}
}

func TestLegacyApprovalRejectsDuplicateLabels(t *testing.T) {
	m := approvalTestModel(false)
	r, _ := m.request()
	m.activate(m.approvalAction(r, 1))
	message, _ := m.requestNotice(r)
	if m.busy != nil || !strings.Contains(message, "ambiguous") {
		t.Fatalf("ambiguous legacy approval was sent or unexplained: %q %+v", message, m.busy)
	}
	m.snapshot.Threads[0].Requests[0].Choices = []string{"Reject", "Allow"}
	r, _ = m.request()
	m.activate(m.approvalAction(r, 1))
	if m.busy == nil || m.busy.ApprovalChoiceID != "" || len(m.busy.Answers) != 1 || m.busy.Answers[0] != "Allow" {
		t.Fatalf("legacy approval encoding: %+v", m.busy)
	}
	wire, err := json.Marshal(m.busy)
	if err != nil || strings.Contains(string(wire), "ApprovalChoiceID") || strings.Contains(string(wire), "QuestionAnswers") {
		t.Fatalf("legacy wire includes unsupported fields: %s (%v)", wire, err)
	}
}

func TestFixtureApprovalUsesLabelsWithGlobalChoiceIDCapability(t *testing.T) {
	m := approvalTestModel(true)
	r := &m.snapshot.Threads[0].Requests[0]
	r.Origin, r.Choices, r.ChoiceIDs = fixtureAgentName, []string{"Allow once", "Deny"}, nil
	m.activate(m.approvalAction(*r, 0))
	if m.busy == nil || m.busy.ApprovalChoiceID != "" || len(m.busy.Answers) != 1 || m.busy.Answers[0] != "Allow once" {
		t.Fatalf("fixture approval blocked by global capability: %+v", m.busy)
	}
}

func TestACPOriginApprovalWithoutIDsFailsClosed(t *testing.T) {
	m := approvalTestModel(true)
	m.snapshot.Agents = append(m.snapshot.Agents, protocol.Agent{ID: "provider", Name: "Provider", Kind: "acp"})
	r := &m.snapshot.Threads[0].Requests[0]
	r.Origin, r.Choices, r.ChoiceIDs = "provider", []string{"Allow once", "Deny"}, nil
	m.activate(m.approvalAction(*r, 0))
	if message, _ := m.requestNotice(*r); m.busy != nil || !strings.Contains(message, "identity") {
		t.Fatalf("ACP request without IDs sent or unexplained: %q", message)
	}
}

func TestApprovalMissingOrAmbiguousIDsAreNotSent(t *testing.T) {
	for _, ids := range [][]string{nil, {"once"}, {"same", "same"}, {"once", ""}} {
		m := approvalTestModel(true)
		m.snapshot.Threads[0].Requests[0].ChoiceIDs = ids
		m.snapshot.Threads[0].Requests[0].DeliveryRoute = "native-response"
		r, _ := m.request()
		m.activate(m.approvalAction(r, 1))
		if message, _ := m.requestNotice(r); m.busy != nil || !strings.Contains(message, "identity") {
			t.Fatalf("invalid IDs %v sent or unexplained: %q", ids, message)
		}
	}
}

func TestApprovalMenuRemainsBoundToRequestRevision(t *testing.T) {
	for _, changedID := range []bool{false, true} {
		m := approvalTestModel(true)
		m.activate(action{Kind: "approval-options"})
		a := m.menu[1].Action
		if changedID {
			m.snapshot.Threads[0].Requests[0].ID = "replacement"
		} else {
			m.snapshot.Threads[0].Requests[0].Revision++
		}
		m.activate(a)
		if m.busy != nil || m.prompt.Value() != "preserved draft" {
			t.Fatal("stale menu approval sent or draft changed")
		}
	}
}

func TestApprovalDeliveryIsNeverOptimisticallyConfirmed(t *testing.T) {
	for _, delivery := range []string{"acp-accepted", "acp-unconfirmed", "acp-delivered", "fixture-revalidated", "acp-undeliverable", "acp-cancelled", "acp-uncertain", "unknown", ""} {
		if deliveryConfirmed(delivery) || strings.Contains(requestDeliveryDescription(delivery), " · confirmed") {
			t.Fatalf("%q treated as receipt confirmation", delivery)
		}
	}
	for _, delivery := range []string{"fixture-confirmed", "confirmed"} {
		if !deliveryConfirmed(delivery) {
			t.Fatalf("%q explicit confirmation lost", delivery)
		}
	}
	m := approvalTestModel(true)
	r, _ := m.request()
	m.activate(m.approvalAction(r, 0))
	c := *m.busy
	m.Update(commandMsg{command: c, receipt: protocol.Receipt{State: "accepted"}})
	if !strings.Contains(m.status, "accepted by server") || !strings.Contains(m.status, "confirmation unavailable") || m.prompt.Value() != "preserved draft" {
		t.Fatalf("acceptance feedback or draft incorrect: %q", m.status)
	}
}
