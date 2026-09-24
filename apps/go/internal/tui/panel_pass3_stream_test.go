package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// streamModel is a conversation with tool, MCP and answered-question rows in
// every status the transcript distinguishes, plus an active turn.
func streamModel(light bool, w, h int, state string) *Model {
	m := testModel()
	m.state.Light = light
	t := &m.snapshot.Threads[0]
	t.State = state
	answered := protocol.Request{ID: "q-ok", Kind: "question", State: "resolved", Delivery: "confirmed",
		Questions:       []protocol.Question{{ID: "a", Label: "Layout", Text: "Which layout?", Kind: "single", Options: []string{"Compact", "Roomy"}}},
		QuestionAnswers: []protocol.Answer{{Choices: []string{"Compact"}}}}
	failed := answered
	failed.ID, failed.Delivery = "q-bad", "acp-undeliverable"
	declined := answered
	declined.ID, declined.Action = "q-decl", protocol.RequestActionDecline
	t.Requests = []protocol.Request{answered, failed, declined}
	t.Activity = []protocol.Activity{
		{ID: "u", Role: "user", Text: "Run the checks"},
		{ID: "t1", Role: "tool", Title: "Read go.mod", State: "completed"},
		{ID: "t2", Role: "tool", Title: "Run go test", State: "failed"},
		{ID: "t3", Role: "mcp", Title: "docs.search", State: "interrupted"},
		{ID: "t4", Role: "tool", Title: "Build", State: "running"},
		{ID: "t5", Role: "tool", Title: "Probe", State: "unknown"},
		{ID: "qa1", Role: "question-answer", RequestID: "q-ok"},
		{ID: "qa2", Role: "question-answer", RequestID: "q-bad"},
		{ID: "qa3", Role: "question-answer", RequestID: "q-decl"},
		{ID: "a", Role: "agent", Text: "Checks ran; one failure."},
	}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func TestPanelPass3StreamCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR to write visual review artifacts")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, m *Model) {
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{160, 50}, {72, 40}} {
			prefix := fmt.Sprintf("%dx%d-light%t", size[0], size[1], light)
			m := streamModel(light, size[0], size[1], "running")
			m.showNoticeAs(noticeError, "Clipboard read failed: denied · use your terminal's paste shortcut")
			write(prefix+"-transcript", m)
			m = streamModel(light, size[0], size[1], "waiting")
			m.showNoticeAs(noticeUnavailable, "Pane resizing is available in the wider layout")
			write(prefix+"-waiting", m)
			m = streamModel(light, size[0], size[1], "idle")
			m.openUsageSummary()
			write(prefix+"-usage-menu", m)
		}
	}
}

var streamProfiles = map[string]func(*Model){
	"default":     func(*Model) {},
	"plain icons": func(m *Model) { m.plainIcons = true },
	"16 colors":   func(m *Model) { m.colorProfile = colorprofile.ANSI },
	"no color":    func(m *Model) { m.colorProfile = colorprofile.NoTTY },
}

// Tool and MCP rows lead with the panel status mark in state ink and keep a
// state word in the mark's ink after a muted separator, in the same number of rows as a mark-free header.
func TestTranscriptOperationRowsLeadWithStatusMark(t *testing.T) {
	for name, setup := range streamProfiles {
		m := testModel()
		setup(m)
		p := m.colors()
		for _, state := range []string{"completed", "running", "failed", "interrupted", "unknown", ""} {
			for _, role := range []string{"tool", "mcp"} {
				lines := m.activityLines([]protocol.Activity{{ID: "x", Role: role, Title: "Build", State: state, Text: "body"}}, 80)
				// Header, one body row and the spacer: the mark adds no row.
				if len(lines) != 3 {
					t.Fatalf("%s %s %q: %d rows", name, role, state, len(lines))
				}
				glyph, ink := panelStatusMark(m, state)
				h := lines[0]
				if h.lead != glyph || h.leadFG != ink || !strings.HasPrefix(h.text, glyph+" "+m.icon(role)+"  Build") {
					t.Fatalf("%s %s %q: header %+v", name, role, state, h)
				}
				if state != "" && (h.tail != state || h.tailFG != ink || h.sep != "  ·  " || h.sepFG != p.muted || !strings.HasSuffix(h.text, h.sep+h.tail)) {
					t.Fatalf("%s %q: state word not in mark ink after a muted separator: %+v", name, state, h)
				}
			}
		}
		if g, ink := panelStatusMark(m, "running"); m.colorProfile != colorprofile.NoTTY && ink == m.colors().green {
			t.Fatalf("%s: running mark %q uses success ink", name, g)
		}
	}
}

// The painted row carries the mark in its own ink without growing the row's
// text, and repeated painting keeps the retained ANSI bounded.
func TestTranscriptMarkPaintIsBounded(t *testing.T) {
	m := streamModel(false, 160, 50, "running")
	first := m.render()
	var size int
	for phase := range 12 {
		m.activityPhase = phase
		f := m.render()
		total := 0
		for _, row := range f.rows {
			total += len(row)
		}
		if size == 0 {
			size = total
		} else if total > size+64 {
			t.Fatalf("phase %d: frame grew from %d to %d bytes", phase, size, total)
		}
	}
	text := frameText(first)
	for _, want := range []string{"✕ " + m.icon("tool") + "  Run go test  ·  failed", "! " + m.icon("mcp") + "  docs.search  ·  interrupted", "✓ ANSWERED", "✕ SUBMITTED · not delivered"} {
		if !strings.Contains(text, want) {
			t.Fatalf("frame lacks %q", want)
		}
	}
}

// The latest status uses the panel marks: active pulses, waiting is "!" and a
// lost connection "?".
func TestLatestStatusUsesPanelMarks(t *testing.T) {
	for name, setup := range streamProfiles {
		for _, tc := range []struct {
			state, status, mark string
			connected           bool
		}{{"running", "Thinking…", "active", true}, {"waiting", "Waiting…", "waiting", true}, {"running", "Connection lost", "disconnected", false}} {
			m := streamModel(false, 160, 50, tc.state)
			setup(m)
			m.connected = tc.connected
			lines := m.transcriptLines(m.thread(), 100)
			last := lines[len(lines)-1]
			glyph, ink := panelStatusMark(m, tc.mark)
			if tc.mark == "active" {
				ink = m.activityColor(activitySummary{Working: true})
			}
			if last.text != tc.status || last.marker != glyph || last.markerFG != ink {
				t.Fatalf("%s %s: %+v", name, tc.status, last)
			}
		}
	}
}

// The answered card title is an uppercase muted heading whose mark carries
// the delivery color; only a confirmed answer takes the check, and the mark
// never adds a row to the card at any width.
func TestAnsweredCardTitleMarksAndHeight(t *testing.T) {
	m := testModel()
	p := m.colors()
	_, request, _ := questionHistoryFixture()
	for _, tc := range []struct {
		delivery, state, action, mark string
	}{
		{"fixture-confirmed", "resolved", "", "completed"},
		{"fixture-confirmed", "resolved", protocol.RequestActionDecline, ""},
		{"fixture-confirmed", "resolved", protocol.RequestActionCancel, ""},
		{"acp-unconfirmed", "closed", "", "pending"},
		{"acp-undeliverable", "closed", "", "failed"},
		{"acp-uncertain", "closed", "", "stale"},
		{"acp-cancelled", "closed", "", "cancelled"},
		{"", "closed", "", "unknown"},
	} {
		r := request
		r.Delivery, r.State, r.Action = tc.delivery, tc.state, tc.action
		glyph, ink := panelStatusMark(m, tc.mark)
		if tc.mark != "completed" && glyph == "✓" {
			t.Fatalf("%+v takes the answered check", tc)
		}
		for width := 20; width <= 140; width++ {
			for _, positionUnknown := range []bool{false, true} {
				card := m.questionHistoryCard(r, width, positionUnknown)
				row := m.questionHistoryStatusRow(r, positionUnknown)
				bare := strings.TrimPrefix(row.text, row.lead+" ")
				// The card wraps its title at the cap width; the painted title
				// must wrap into as many rows as the bare title would.
				extent := width + 2
				content := max(1, max(min(extent, 24), extent*4/5)-4)
				fitted := questionHistoryFitStatus(row, content)
				if !strings.Contains(card[1].text, questionHistoryWrap(fitted.text, content)[0]) && card[0].boxW-4 >= content {
					t.Fatalf("%+v width %d: card title %q is not %q", tc, width, card[1].text, fitted.text)
				}
				if titleRows, want := len(questionHistoryWrap(fitted.text, content)), len(questionHistoryWrap(bare, content)); titleRows != want {
					t.Fatalf("%+v width %d: title takes %d rows, bare title %d", tc, width, titleRows, want)
				}
				first := card[1]
				wantFG := p.muted
				if tc.mark == "failed" || tc.mark == "stale" || tc.mark == "cancelled" {
					wantFG = ink // Failed or uncertain delivery words keep the mark's emphasis.
				}
				if first.lead != "" && (first.lead != glyph || first.leadFG != ink || first.fg != wantFG) {
					t.Fatalf("%+v: title %+v", tc, first)
				}
				if first.lead == "" && first.fg != ink {
					t.Fatalf("%+v width %d: fallback title lost the mark ink: %+v", tc, width, first)
				}
			}
		}
	}
}

// A notice leads with its severity mark painted beside the text; the notice
// text and m.status stay unchanged and repeats coalesce.
func TestNoticeSeverityMarkIsPaintedSeparately(t *testing.T) {
	for name, setup := range streamProfiles {
		for _, tc := range []struct {
			severity noticeSeverity
			state    string
		}{{noticeInfo, ""}, {noticeUnavailable, "blocked"}, {noticeError, "failed"}, {noticeDone, "completed"}} {
			m := streamModel(false, 120, 40, "idle")
			setup(m)
			status := m.status
			if cmd := m.showNoticeAs(tc.severity, "Paste unavailable"); cmd == nil {
				t.Fatal("first notice has no expiry")
			}
			if cmd := m.showNoticeAs(tc.severity, "Paste unavailable"); cmd != nil {
				t.Fatal("repeated notice did not coalesce")
			}
			if m.notice.text != "Paste unavailable" || m.status != status {
				t.Fatalf("%s: notice text %q, status %q", name, m.notice.text, m.status)
			}
			glyph, _ := panelStatusMark(m, tc.state)
			bottom := ansi.Strip(m.render().rows[m.height-1])
			if !strings.Contains(bottom, "  ·  "+glyph+" Paste unavailable") {
				t.Fatalf("%s: bottom row %q", name, bottom)
			}
		}
	}
	m := testModel()
	m.showNotice("plain")
	if m.notice.severity != noticeInfo {
		t.Fatal("showNotice is not info")
	}
}

// The usage summary puts a rule above its action row; the rule has no hit
// rectangle and keyboard and wheel navigation skip it.
func TestMenuSeparatorIsNotFocusableOrClickable(t *testing.T) {
	m := streamModel(false, 120, 40, "idle")
	m.openUsageSummary()
	sep := -1
	for i, item := range m.menu {
		if item.Separator {
			sep = i
		}
	}
	if sep < 0 || sep != len(m.menu)-2 || m.menu[sep+1].Label != "Usage details" {
		t.Fatalf("separator not above Usage details: %+v", m.menu)
	}
	f := m.render()
	for _, h := range f.hits {
		if h.Key == fmt.Sprintf("menu:%d", sep) || h.Action.Kind == "menu-select" && h.Action.Index == sep {
			t.Fatalf("separator has a hit: %+v", h)
		}
	}
	m.menuIndex = sep - 1
	m.menuIndex = m.menuStep(m.menuIndex, 1, true)
	if m.menuIndex != sep+1 {
		t.Fatalf("down landed on %d", m.menuIndex)
	}
	m.menuIndex = m.menuStep(m.menuIndex, -1, true)
	if m.menuIndex != sep-1 {
		t.Fatalf("up landed on %d", m.menuIndex)
	}
	m.menuIndex = sep - 1
	if got := m.menuStep(m.menuIndex, 1, false); got != sep+1 {
		t.Fatalf("wheel landed on %d", got)
	}
	// Enter on a separator (reachable only through scrolling) does nothing.
	m.menuIndex = sep
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.menu) == 0 {
		t.Fatal("enter on a separator closed the menu")
	}
}

// The attached-context menu title is a static uppercase heading; attachment
// names in its rows keep their case.
func TestAttachedContextMenuKeepsNames(t *testing.T) {
	m := streamModel(false, 120, 40, "idle")
	m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "ReadMe.MD"}}
	m.activate(action{Kind: "attachments"})
	text := frameText(m.render())
	if !strings.Contains(text, "ATTACHED CONTEXT") || !strings.Contains(text, "Remove ReadMe.MD") {
		t.Fatalf("attached-context menu:\n%s", text)
	}
}

// The aggregate attachment button keeps one span and hit rectangle in every
// profile; fallbacks show [ ] in its reserved end cells.
func TestAttachmentsButtonGeometryAcrossProfiles(t *testing.T) {
	var want *hit
	for name, setup := range streamProfiles {
		m := streamModel(false, 120, 26, "idle") // compact: the aggregate control
		setup(m)
		m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "a.go"}}
		f := m.render()
		var got *hit
		for i := range f.hits {
			if f.hits[i].Key == "attachments" {
				got = &f.hits[i]
			}
		}
		if got == nil {
			t.Fatalf("%s: no attachments hit", name)
		}
		if want == nil {
			want = got
		} else if got.Rect != want.Rect {
			t.Fatalf("%s: rect %+v, want %+v", name, got.Rect, want.Rect)
		}
		cells := ansi.Strip(cutCells(f.rows[got.Rect.Y], got.Rect.X, got.Rect.X+got.Rect.W))
		rich := m.colorProfile > colorprofile.ANSI && !m.plainIcons
		if rich != !strings.HasPrefix(cells, "[") || !rich && !strings.HasSuffix(cells, "]") || !strings.Contains(cells, "1 context attachments") {
			t.Fatalf("%s: cells %q", name, cells)
		}
	}
}
