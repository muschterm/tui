package tui

import (
	"testing"

	"github.com/muschterm/tui/apps/go/internal/protocol"

	"github.com/charmbracelet/colorprofile"
)

func TestSteerDeliveredOnlyForConfirmedStates(t *testing.T) {
	for state, want := range map[string]bool{"delivered": true, "fixture-delivered": true, "accepted": false, "": false} {
		if steerDelivered(state) != want {
			t.Fatalf("steerDelivered(%q) = %t", state, !want)
		}
	}
}

func TestColorDiagnosticParts(t *testing.T) {
	m := &Model{}
	m.colorProbe.noColor = true
	if d := m.colorDiagnosticParts(); d.profile != "disabled by NO_COLOR" || d.note != "" {
		t.Fatalf("NO_COLOR = %+v", d)
	}
	m.colorProbe.noColor, m.colorProfile = false, colorprofile.ANSI256
	if d := m.colorDiagnosticParts(); d.note != "true color unconfirmed" || m.colorDiagnostics() != "Colors: "+d.profile+" · true color unconfirmed" {
		t.Fatalf("fallback = %+v / %q", d, m.colorDiagnostics())
	}
}

func TestNoticeSeverityForCommandResults(t *testing.T) {
	for _, tc := range []struct {
		msg  commandMsg
		want noticeSeverity
	}{
		{commandMsg{command: protocol.Command{ID: "c1", Kind: "project.remove"}, err: &protocol.Error{Code: "active_work", Message: "busy"}}, noticeError},
		{commandMsg{command: protocol.Command{ID: "c2", Kind: "queue.steer"}, err: &protocol.Error{Code: "stale", Message: "gone"}}, noticeError},
		{commandMsg{command: protocol.Command{ID: "c3", Kind: "queue.steer"}, receipt: protocol.Receipt{State: "accepted"}}, noticeActive},
		{commandMsg{command: protocol.Command{ID: "c4", Kind: "queue.steer"}, receipt: protocol.Receipt{State: "fixture-delivered"}}, noticeDone},
	} {
		m := testModel()
		c := tc.msg.command
		m.busy, m.state.Pending = &c, &c
		m.Update(tc.msg)
		if m.notice.severity != tc.want || m.notice.text == "" {
			t.Fatalf("%s: notice %+v, want severity %d", tc.msg.command.ID, m.notice, tc.want)
		}
	}
}

func TestColorBlocksNoteBelongsToPair(t *testing.T) {
	m := testModel()
	m.colorProfile = colorprofile.ANSI256
	b := colorBlocks(m)
	if len(b) != 1 || b[0].label != "Colors" || b[0].kind != surfacePairBlock || b[0].note != "true color unconfirmed" {
		t.Fatalf("blocks = %+v", b)
	}
	m.colorProbe.noColor = true
	if b = colorBlocks(m); len(b) != 1 || b[0].value != "disabled by NO_COLOR" || b[0].note != "" {
		t.Fatalf("NO_COLOR blocks = %+v", b)
	}
}
