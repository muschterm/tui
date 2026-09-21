package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func queueReviewModel(width, height int) *Model {
	m := testModel()
	q := m.thread().Queue[0]
	m.snapshot.Threads[0].Queue = nil
	for i, text := range []string{"Review the layout\nwith 界 and é labels", "Check keyboard navigation", "Inspect the narrow layout"} {
		item := q
		item.ID, item.Text = fmt.Sprint("queued-", i), text
		m.snapshot.Threads[0].Queue = append(m.snapshot.Threads[0].Queue, item)
	}
	m.prompt.SetValue("Keep my unsent draft")
	m.viewState().Draft = m.prompt.Value()
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestQueueCardKeepsControlsInsideAndPreservesFooter(t *testing.T) {
	for _, size := range [][2]int{{160, 50}, {80, 30}, {48, 22}, {47, 22}, {40, 22}} {
		m := queueReviewModel(size[0], size[1])
		before, _ := json.Marshal(m.snapshot)
		f := m.render()
		header := controlHit(t, f, "queue")
		if !strings.Contains(f.rows[header.Rect.Y], "more…") {
			t.Fatal("queue header omitted access to hidden items")
		}
		for i := 0; i < m.queueVisibleRows(); i++ {
			id := fmt.Sprint("queued-", i)
			preview := controlHit(t, f, "queue-row:"+id)
			edit, remove := controlHit(t, f, "edit:"+id), controlHit(t, f, "remove:"+id)
			down := controlHit(t, f, "down:"+id)
			if preview.Rect.Y != header.Rect.Y+1+i || edit.Rect.Y != preview.Rect.Y || remove.Rect.Y != preview.Rect.Y || down.Rect.Y != preview.Rect.Y {
				t.Fatal("queue controls separated from their preview", size)
			}
			if preview.Rect.X+preview.Rect.W >= edit.Rect.X || edit.Rect.X+edit.Rect.W >= remove.Rect.X || down.Rect.X+down.Rect.W > f.prompt.X+f.prompt.W {
				t.Fatal("queue actions overlap or escape the inset", size)
			}
			if got := controlHit(t, m.measure(), edit.Key); got != edit {
				t.Fatal("queue paint and measurement disagree")
			}
		}
		if hasControl(f, "up:queued-0") || hasControl(f, "edit:queued-2") {
			t.Fatal("disabled or hidden action retained a hit target")
		}
		if header.Rect.Y+m.queueHeight() > f.request.Y || f.request.Y+f.request.H >= f.prompt.Y || !hasControl(f, "send") {
			t.Fatal("queue overlaps request or prompt", size)
		}
		for _, row := range f.rows {
			if ansi.StringWidth(row) != m.width {
				t.Fatal("queue widened the frame", size)
			}
		}
		after, _ := json.Marshal(m.snapshot)
		if string(before) != string(after) || m.busy != nil {
			t.Fatal("queue rendering changed authoritative work")
		}
		clickControl(m, header)
		if len(m.menu) != 15 || m.busy != nil || m.prompt.Value() != "Keep my unsent draft" {
			t.Fatal("queue header lost hidden-item access or changed the prompt")
		}
	}
}

func TestQueueCardMouseAndKeyboardTargetTheSameMessage(t *testing.T) {
	for _, keyboard := range []bool{false, true} {
		for _, key := range []string{"edit:queued-0", "remove:queued-0", "down:queued-0"} {
			m := queueReviewModel(48, 22)
			h := controlHit(t, m.render(), key)
			if keyboard {
				m.setFocus(key)
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			} else {
				clickControl(m, h)
			}
			switch key {
			case "edit:queued-0":
				if m.state.Edit == nil || m.state.Edit.ID != "queued-0" || m.state.Edit.OldDraft != "Keep my unsent draft" || m.prompt.Value() != m.thread().Queue[0].Text || m.busy != nil {
					t.Fatal("edit lost its message or draft")
				}
			case "remove:queued-0":
				if m.busy == nil || m.busy.Kind != "queue.remove" || m.busy.TargetID != "queued-0" || m.busy.Revision != m.thread().QueueRevision {
					t.Fatal("remove lost its target or revision")
				}
			case "down:queued-0":
				if m.busy == nil || m.busy.Kind != "queue.reorder" || strings.Join(m.busy.Order, ",") != "queued-1,queued-0,queued-2" || m.busy.Revision != m.thread().QueueRevision {
					t.Fatal("reorder lost its target or revision")
				}
			}
			if m.state.Edit == nil && m.prompt.Value() != "Keep my unsent draft" {
				t.Fatal("queue action replaced the ordinary draft")
			}
		}
	}
}

func TestQueueCardCaptures(t *testing.T) {
	dir := os.Getenv("TUI_GO_CAPTURE_DIR")
	if dir == "" {
		t.Skip("set TUI_GO_CAPTURE_DIR")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{160, 50}, {48, 22}} {
			m := queueReviewModel(size[0], size[1])
			m.state.Light = light
			m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "context"}}
			m.hover = "steer:queued-0"
			m.configureInputs()
			name := fmt.Sprintf("%dx%d-light%t-queue.ansi", size[0], size[1], light)
			if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(m.render().rows, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
