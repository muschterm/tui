package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	agentoptions "github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (m *Model) approvalAction(r protocol.Request, index int) action {
	a := action{Kind: "approve", ID: r.ID, Revision: r.Revision, Value: r.Choices[index]}
	if m.hasCapability("approval-choice-ids") && index < len(r.ChoiceIDs) {
		a.ApprovalChoiceID = r.ChoiceIDs[index]
	}
	return a
}

func (m *Model) submitApproval(a action) tea.Cmd {
	r, ok := m.request()
	if !ok || r.Kind != "approval" {
		return nil
	}
	reject := func(message string) tea.Cmd {
		m.status = message
		m.setRequestFeedback(m.state.Active, r.ID, r.Revision, message, false)
		m.configureInputs()
		return m.showNoticeAs(noticeUnavailable, message)
	}
	if m.thread().NeedsResume {
		return reject("Resume this thread first (F4 → Resume).")
	}
	if !m.connected {
		return reject("Disconnected · wait for the server to reconnect.")
	}
	c := protocol.Command{Kind: "request.answer", TargetID: r.ID, Revision: r.Revision}
	matches := 0
	origin, _ := m.agentByID(r.Origin)
	requiresIDs := len(r.ChoiceIDs) > 0 || r.DeliveryRoute == "native-response" || strings.HasPrefix(r.Delivery, "acp-") || origin.Kind == "acp" || agentoptions.IsACP(m.thread().AgentID)
	if m.hasCapability("approval-choice-ids") && requiresIDs {
		labelMatches := false
		for i, id := range r.ChoiceIDs {
			if id != "" && id == a.ApprovalChoiceID {
				matches++
				labelMatches = i < len(r.Choices) && r.Choices[i] == a.Value
			}
		}
		if matches != 1 || !labelMatches {
			return reject("Approval choice identity unavailable or changed · nothing sent")
		}
		c.ApprovalChoiceID = a.ApprovalChoiceID
	} else {
		if a.ApprovalChoiceID != "" {
			return reject("Approval choice support changed · reopen approval choices")
		}
		for _, label := range r.Choices {
			if label == a.Value {
				matches++
			}
		}
		if matches != 1 {
			return reject("Approval choice is ambiguous or changed · server needs choice ID support")
		}
		c.Answers = []string{a.Value}
	}
	return m.command(c, a)
}

func requestDeliveryDescription(delivery string) string {
	if delivery == "acp-turn-confirmed" {
		return "Answer taken by provider · turn completed"
	}
	if deliveryConfirmed(delivery) {
		return delivery + " · confirmed"
	}
	switch delivery {
	case "acp-accepted":
		return "Accepted by server · upstream confirmation unavailable"
	case "acp-delivered":
		return "Legacy delivery record · upstream confirmation unavailable"
	case "acp-unconfirmed":
		return "Response prepared · upstream confirmation unavailable"
	case "acp-undeliverable":
		return "Not delivered · request is no longer available"
	case "acp-cancelled":
		return "Cancelled · no upstream confirmation"
	case "acp-uncertain":
		return "Delivery uncertain · no upstream confirmation"
	case "":
		return "Unavailable"
	default:
		return delivery + " · unconfirmed"
	}
}
