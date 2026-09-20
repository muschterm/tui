package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"github.com/charmbracelet/x/ansi"
)

func readingInput() textarea.Model {
	a := newInput("")
	a.MaxHeight = 0
	a.SetValue("zero\none\ntwo\nthree\nfour\nfive")
	a.Focus()
	resizeInput(&a, 12, 2)
	return a
}

func TestInputReadScrollingPreservesEditingState(t *testing.T) {
	a := readingInput()
	a.BeginSelection(0, 0)
	a.ExtendSelection(2, 1)
	a.EndSelection()
	beforeValue, beforeView := a.Value(), a.View()
	beforeRow, beforeCol, beforeOffset := a.Line(), a.Column(), a.ScrollYOffset()
	start, end, selected := a.Selection()
	var p inputPresentation
	p.ScrollTo(&a, 6, 0)
	rows := strings.Split(ansi.Strip(p.View(&a)), "\n")
	if len(rows) != 2 || strings.TrimSpace(rows[0]) != "zero" || strings.TrimSpace(rows[1]) != "one" {
		t.Fatalf("top view = %q", rows)
	}
	p.ScrollTo(&a, 6, 100)
	rows = strings.Split(ansi.Strip(p.View(&a)), "\n")
	if p.offset != 4 || len(rows) != 2 || strings.TrimSpace(rows[0]) != "four" {
		t.Fatalf("bottom view = %q, offset %d", rows, p.offset)
	}
	newStart, newEnd, newSelected := a.Selection()
	if a.Value() != beforeValue || a.View() != beforeView || a.Line() != beforeRow || a.Column() != beforeCol || a.ScrollYOffset() != beforeOffset || start != newStart || end != newEnd || selected != newSelected {
		t.Fatal("read scrolling changed editing state")
	}
	p.Reset()
	if p.View(&a) != a.View() || p.manual {
		t.Fatal("reset did not restore native editing viewport")
	}
}

func TestInputReadScrollPointerMappingAndSelection(t *testing.T) {
	a := readingInput()
	var p inputPresentation
	p.ScrollTo(&a, 6, 1)
	y := p.RelativeY(&a, 0)
	if got := a.PositionAt(1, y); got != (textarea.Position{Row: 1, Col: 1}) {
		t.Fatalf("pointer mapped to %+v", got)
	}
	a.BeginSelection(0, p.RelativeY(&a, 0))
	// BeginSelection can change the native offset. Translate again against
	// that current viewport, retaining the manual presentation offset.
	a.ExtendSelection(2, p.RelativeY(&a, 1))
	a.EndSelection()
	if got := a.SelectedText(); got != "one\ntw" {
		t.Fatalf("selected %q", got)
	}
	p.Refresh(&a, 6)
	if p.offset != 1 || strings.TrimSpace(strings.Split(ansi.Strip(p.View(&a)), "\n")[0]) != "one" {
		t.Fatal("selection refresh moved reading position")
	}
	if got := p.Metrics(inputVisualMetrics(a)); got.Offset != 1 || got.Total != 6 {
		t.Fatalf("presentation metrics = %+v", got)
	}
}

func TestInputReadScrollUnicodeWrappedRows(t *testing.T) {
	a := newInput("")
	a.MaxHeight = 0
	a.SetValue("世界世界\ne\u0301 cafe\n👩🏽‍💻 hello\nlast")
	resizeInput(&a, 6, 2)
	native := inputVisualMetrics(a)
	var p inputPresentation
	p.ScrollTo(&a, native.Total, 0)
	first := strings.Split(ansi.Strip(p.View(&a)), "\n")
	if len(first) != 2 || !strings.Contains(first[0]+first[1], "世界") {
		t.Fatalf("wrapped Unicode view = %q", first)
	}
	if got := a.PositionAt(2, p.RelativeY(&a, 0)); got != (textarea.Position{Row: 0, Col: 1}) {
		t.Fatalf("wide-character pointer mapping = %+v", got)
	}
	p.ScrollTo(&a, native.Total, native.Total)
	last := strings.Split(ansi.Strip(p.View(&a)), "\n")
	if len(last) != 2 || strings.TrimSpace(last[1]) != "last" {
		t.Fatalf("last Unicode view = %q", last)
	}
}

func TestFullInputViewPreservesUnicodeSelectionRendering(t *testing.T) {
	for _, value := range []string{"👩🏽‍💻 abc", "e\u0301 cafe", "世界", "👩🏽‍💻 abc\ne\u0301 cafe\n世界"} {
		a := newInput("")
		a.MaxHeight = 0
		a.SetValue(value)
		a.Focus()
		rows := inputRows(value, 20)
		resizeInput(&a, 20, rows)
		a.SelectAll()
		if got, want := fullInputView(&a, rows), a.View(); got != want {
			t.Fatalf("cloned selection rendering differs for %q:\ngot  %q\nwant %q", value, got, want)
		}
	}
}
