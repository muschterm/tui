package tui

// statusGlyph is the panelStatusMark glyph for state without its ink. Every
// feature-specific mark takes its glyph (and plain-icon fallback) from here so
// the status vocabulary cannot drift between features.
func statusGlyph(m *Model, state string) string {
	glyph, _ := panelStatusMark(m, state)
	return glyph
}
