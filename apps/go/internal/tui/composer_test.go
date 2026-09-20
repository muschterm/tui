package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputVisualRows(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		width, rows int
	}{
		{"empty", "", 10, 1},
		{"short", "hello", 10, 1},
		{"cursor at full row", "abcdefghij", 10, 2},
		{"wrap", "abcdefghijk", 10, 2},
		{"word wrap", "hello world", 10, 2},
		{"newlines", "one\ntwo\n", 10, 3},
		{"wide characters", "世界世界", 5, 2},
		{"combining clusters", "e\u0301e\u0301", 10, 1},
		{"emoji sequence", "👩🏽‍💻", 10, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newInput("")
			a.MaxHeight = 0
			a.SetValue(tc.value)
			resizeInput(&a, tc.width, 1)
			got := inputVisualMetrics(a)
			if got.Total != tc.rows {
				t.Fatalf("rows = %d, want %d", got.Total, tc.rows)
			}
			if got.Offset != got.CursorRow || got.Visible != 1 {
				t.Fatalf("one-row viewport does not follow cursor: %+v", got)
			}
		})
	}
}

func TestInputHeightLimitsAndResize(t *testing.T) {
	a := newInput("")
	a.MaxHeight = 0
	a.SetValue(strings.Repeat("x", 99))
	if got := inputHeight(a, 10, maxPromptRows); got != 8 {
		t.Fatalf("height = %d, want 8", got)
	}
	if got := inputHeight(a, 10, 2); got != 2 {
		t.Fatalf("short-window height = %d, want 2", got)
	}
	if got := inputHeight(a, 100, maxPromptRows); got != 1 {
		t.Fatalf("wide-window height = %d, want 1", got)
	}
	resizeInput(&a, 10, 8)
	if got := inputVisualMetrics(a); got.Total != 10 || got.Offset != 2 || got.CursorRow != 9 {
		t.Fatalf("wrapped viewport = %+v", got)
	}
	resizeInput(&a, 100, 1)
	if got := inputVisualMetrics(a); got.Total != 1 || got.Offset != 0 || got.CursorRow != 0 {
		t.Fatalf("widened viewport = %+v", got)
	}
}

func TestInputPasteBeyondVisibleCap(t *testing.T) {
	a := newInput("")
	a.Focus()
	resizeInput(&a, 20, maxPromptRows)
	value := strings.Repeat("line\n", 15) + "last"
	updateInput(&a, tea.PasteMsg{Content: value})
	resizeInput(&a, 20, inputHeight(a, 20, maxPromptRows))
	if a.Value() != value {
		t.Fatal("visible height truncated pasted draft")
	}
	if got := inputVisualMetrics(a); got.Total != 16 || got.Visible != 8 || got.Offset != 8 {
		t.Fatalf("pasted viewport = %+v", got)
	}
	a.MoveToBegin()
	resizeInput(&a, 20, 8)
	if got := inputVisualMetrics(a); got.Offset != 0 || got.CursorRow != 0 {
		t.Fatalf("beginning viewport = %+v", got)
	}
}

func TestInputMeasurementPreservesEditingState(t *testing.T) {
	a := newInput("")
	a.Focus()
	a.MaxHeight = 0
	a.SetValue(strings.Repeat("hello world\n", 10))
	resizeInput(&a, 10, 3)
	a.BeginSelection(0, 0)
	a.ExtendSelection(4, 1)
	a.EndSelection()
	beforeView, beforeValue := a.View(), a.Value()
	beforeRow, beforeCol := a.Line(), a.Column()
	start, end, selected := a.Selection()
	before := inputVisualMetrics(a)
	inputHeight(a, 40, 8)
	after := inputVisualMetrics(a)
	newStart, newEnd, newSelected := a.Selection()
	if a.View() != beforeView || a.Value() != beforeValue || a.Line() != beforeRow || a.Column() != beforeCol || start != newStart || end != newEnd || selected != newSelected || before != after {
		t.Fatal("measurement changed textarea editing state")
	}
}
