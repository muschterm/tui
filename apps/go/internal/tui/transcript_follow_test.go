package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func lastTranscriptRow(m *Model) (string, frame) {
	f := m.render()
	r := f.transcript
	return ansi.Strip(f.rows[r.Y+r.H-1]), f
}

func appendAgentActivity(m *Model, text string) {
	th := &m.snapshot.Threads[0]
	th.Activity = append(th.Activity, protocol.Activity{ID: fmt.Sprintf("follow-%d", len(th.Activity)), Role: "agent", Text: text})
	m.markDirty()
}

func transcriptEndHit(f frame) (hit, bool) {
	for _, h := range f.hits {
		if h.Key == "transcript-end" {
			return h, true
		}
	}
	return hit{}, false
}

func TestPinnedTranscriptFollowsNewActivity(t *testing.T) {
	m := scrollModel()
	m.viewState().Pinned = true
	m.clampScroll(m.measure())
	appendAgentActivity(m, "newest agent line")
	m.clampScroll(m.measure())
	f := m.render()
	if _, ok := transcriptEndHit(f); ok {
		t.Fatal("pinned transcript painted the jump overlay")
	}
	found := false
	for y := f.transcript.Y; y < f.transcript.Y+f.transcript.H; y++ {
		found = found || strings.Contains(ansi.Strip(f.rows[y]), "newest agent line")
	}
	if !found || m.viewState().Scroll != f.transcriptMax {
		t.Fatal("pinned transcript did not follow the new last line")
	}
}

func TestScrolledTranscriptCountsNewActivityAndJumps(t *testing.T) {
	for _, useKey := range []bool{false, true} {
		m := scrollModel()
		m.setFocus("transcript")
		m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
		m.key(tea.KeyPressMsg{Code: tea.KeyUp})
		v := m.viewState()
		if v.Pinned {
			t.Fatal("scrolling up kept the transcript pinned")
		}
		offset := v.Scroll
		appendAgentActivity(m, "one")
		appendAgentActivity(m, "two")
		m.clampScroll(m.measure())
		row, f := lastTranscriptRow(m)
		if v.Scroll != offset {
			t.Fatalf("new activity moved the reading position: %d -> %d", offset, v.Scroll)
		}
		if !strings.Contains(row, "2 new messages") {
			t.Fatalf("last row lacks the new-message count: %q", row)
		}
		h, ok := transcriptEndHit(f)
		if !ok || h.Rect.Y != f.transcript.Y+f.transcript.H-1 {
			t.Fatal("overlay hit missing from the last transcript row")
		}
		if useKey {
			m.key(tea.KeyPressMsg{Code: tea.KeyEnd})
		} else {
			m.activate(h.Action)
		}
		f = m.render()
		if !v.Pinned || v.Scroll != f.transcriptMax {
			t.Fatal("jumping did not pin to the end")
		}
		if _, ok := transcriptEndHit(f); ok {
			t.Fatal("overlay remained after jumping to the end")
		}
	}
}

func TestShortTranscriptHasNoJumpOverlay(t *testing.T) {
	m := testModel()
	m.width, m.height = 160, 50
	m.configureInputs()
	m.viewState().Scroll, m.viewState().Pinned = 0, false
	f := m.render()
	if f.transcriptMax != 0 {
		t.Skip("fixture transcript overflows at this size")
	}
	if _, ok := transcriptEndHit(f); ok {
		t.Fatal("fitting transcript painted the jump overlay")
	}
}

func TestJumpOverlayMeasurementMatchesPaint(t *testing.T) {
	m := scrollModel()
	m.viewState().Scroll, m.viewState().Pinned = 0, false
	measured, painted := m.measure(), m.render()
	if _, ok := transcriptEndHit(painted); !ok {
		t.Fatal("overlay not visible")
	}
	painted.rows = nil
	if !reflect.DeepEqual(measured, painted) {
		t.Fatal("measure and render diverged with the overlay visible")
	}
}

func TestSingleBlankRowSeparatesTranscriptBlocks(t *testing.T) {
	m := testModel()
	th := protocol.Thread{ID: "t", Activity: []protocol.Activity{
		{ID: "a", Role: "agent", Text: "agent reply"},
		{ID: "b", Role: "tool", Title: "Read"},
	}}
	lines := m.transcriptLines(th, 60)
	for i, l := range lines {
		if strings.Contains(l.text, "agent reply") {
			if lines[i+1].text != "" || lines[i+2].text == "" {
				t.Fatalf("want exactly one blank row after the agent message: %q %q", lines[i+1].text, lines[i+2].text)
			}
			return
		}
	}
	t.Fatal("agent message not found")
}

func TestViewsFromOlderSavesRestoreWithoutFalseNewCount(t *testing.T) {
	// A view saved before follow-the-end existed has an offset but neither
	// Pinned nor SeenActivity; a thread first seen at load starts pinned.
	base := scrollModel()
	id := base.snapshot.Threads[0].ID
	raw := []byte(`{"Active":"` + id + `","Threads":{"` + id + `":{"Scroll":3}}}`)
	m := New(nil, "test", base.snapshot, raw)
	m.width, m.height = 120, 40
	m.configureInputs()
	m.clampScroll(m.measure())
	v := m.viewState()
	if v.Pinned || v.Scroll != 3 {
		t.Fatalf("older view did not restore its offset: pinned=%v scroll=%d", v.Pinned, v.Scroll)
	}
	row, _ := lastTranscriptRow(m)
	if !strings.Contains(row, "Jump to bottom") || strings.Contains(row, "new message") {
		t.Fatalf("older view announced existing messages as new: %q", row)
	}
	appendAgentActivity(m, "arrived later")
	m.clampScroll(m.measure())
	if row, _ = lastTranscriptRow(m); !strings.Contains(row, "1 new message") {
		t.Fatalf("later message was not counted: %q", row)
	}
	fresh := New(nil, "test", base.snapshot, nil)
	if !fresh.state.Threads[id].Pinned {
		t.Fatal("a thread first seen at load did not start pinned")
	}
}

func TestFollowingDoesNotDirtyTheViewAndJumpRestoresTranscriptFocus(t *testing.T) {
	m := scrollModel()
	m.viewState().Pinned = true
	m.clampScroll(m.measure())
	m.dirty = false
	appendAgentActivity(m, "streamed")
	m.dirty = false
	m.clampScroll(m.measure())
	if m.dirty {
		t.Fatal("following streamed output dirtied the saved view")
	}
	m.setFocus("transcript")
	m.key(tea.KeyPressMsg{Code: tea.KeyUp})
	m.setFocus("transcript-end")
	m.activate(action{Kind: "transcript-end"})
	if m.focus != "transcript" || !m.viewState().Pinned {
		t.Fatalf("jump left focus=%q pinned=%v", m.focus, m.viewState().Pinned)
	}
}
