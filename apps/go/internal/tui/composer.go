package tui

import "charm.land/bubbles/v2/textarea"

const maxPromptRows = 8

// inputScroll describes visual rows, including soft wraps and the cursor's
// trailing cell. Offset and CursorRow are zero-based content row positions.
type inputScroll struct {
	Total, Visible, Offset, CursorRow int
}

// inputVisualMetrics uses the pinned textarea's own wrapping instead of
// approximating it with rune counts or another word-wrapping algorithm.
// Cursor exposes the viewport-relative position. A fresh measurement input is
// necessary: Bubbles copies share a pointer to their viewport.
func inputVisualMetrics(a textarea.Model) inputScroll {
	return inputViewportMetrics(a, inputRows(a.Value(), a.Width()))
}

// inputViewportMetrics reuses a cached total when only the cursor moves.
func inputViewportMetrics(a textarea.Model, total int) inputScroll {
	// Our inputs have no chrome. Clearing styles also makes the measurement
	// independent of focus colors and any future padding around the input.
	a.SetStyles(textarea.Styles{})
	a.Prompt = ""
	a.ShowLineNumbers = false
	a.SetVirtualCursor(false)
	a.Focus() // Only change the copy; no blink command needs to execute.
	return inputScroll{
		Total: total, Visible: a.Height(),
		Offset: a.ScrollYOffset(), CursorRow: a.Cursor().Y + a.ScrollYOffset(),
	}
}

func inputRows(value string, width int) int {
	probe := textarea.New()
	probe.Prompt = ""
	probe.ShowLineNumbers = false
	probe.SetStyles(textarea.Styles{})
	probe.CharLimit = 0
	probe.MaxHeight = 0
	probe.MaxWidth = 0
	probe.DynamicHeight = true
	probe.MinHeight = 1
	probe.SetWidth(max(1, width))
	probe.SetValue(value)
	return probe.Height()
}

// inputHeight measures at the proposed width without moving the real input's
// cursor, selection or viewport. A short window may lower the eight-row cap.
func inputHeight(a textarea.Model, width, limit int) int {
	return min(max(1, limit), inputRows(a.Value(), width))
}

// resizeInput fits the viewport while leaving the complete draft editable.
// In Bubbles v2.2.1 MaxHeight also limits logical-line insertion when
// MaxContentHeight is zero, so it must not carry the visible-height cap.
func resizeInput(a *textarea.Model, width, height int) {
	a.MaxHeight = max(1, height)
	a.DynamicHeight = true
	a.MinHeight = 1
	// Dynamic recalculation also clamps stale offsets after text is removed
	// or wraps disappear. SetHeight alone only keeps the cursor in view.
	a.SetWidth(max(1, width))
	a.DynamicHeight = false
	a.MaxHeight = 0
	// The upstream viewport clamps scroll offsets against its rendered content.
	// Refresh that content after wrapping changes before positioning the cursor.
	a.View()
	a.SetHeight(max(1, height))
}
