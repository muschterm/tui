package tui

// Control glyphs from Nerd Fonts v3.4.0 glyphnames.json;
// these are interface symbols, never substitutes for provider brand artwork.
// A Nerd Font is the requested default; TUI_GO_ICONS=ascii is an explicit
// fallback because terminal protocols cannot identify the user's font reliably.
var icons = map[string]struct{ glyph, plain string }{
	"left": {"\uebf3", "L+"}, "left-off": {"\uec02", "L-"},
	"right": {"\uebf4", "R+"}, "right-off": {"\uec00", "R-"},
	"bottom": {"\uebf2", "B+"}, "bottom-off": {"\uec01", "B-"},
	"maximize": {"\ueb4c", "[]"}, "restore": {"\ueb4d", "><"},
	"files": {"\ueaf0", "F"}, "git": {"\uea68", "G"},
	"terminal": {"\uea85", ">_"}, "agents": {"\uea7e", "A"},
	"plan": {"\ueb67", "P"}, "activity": {"\ueb31", "~"},
	"close": {"\uea76", "x"}, "add": {"\uea60", "+"},
	"more": {"\uea7c", "..."}, "theme": {"\ueac6", "*"},
	"more-vertical": {"\ueb10", ":"}, // cod-kebab_vertical
	// fa-circle_arrow_up, fa-stop_circle and fa-paperclip.
	"send": {"\uf0aa", "^"}, "stop": {"\uf28d", "o"}, "attach": {"\uf0c6", "+"},
	// fa-circle_o, fa-dot_circle_o, fa-square_o, fa-square_check.
	"radio": {"\uf10c", "( )"}, "radio-on": {"\uf192", "(*)"},
	"checkbox": {"\uf096", "[ ]"}, "checkbox-on": {"\uf14a", "[x]"},
	"thread": {"\uea6b", "o"}, "trash": {"\uea81", "x"},
	"folder": {"\uea83", "P"}, "project-add": {"\uea80", "+"},
	"context":   {"\ueb15", "+"},
	"attention": {"\ueaa2", "!"}, "tool": {"\ueb6d", "T"},
	"mcp": {"\ueb15", "M"}, "check": {"\ueab2", "+"},
}

func (m *Model) icon(name string) string {
	i, ok := icons[name]
	if !ok {
		return "?"
	}
	if m.plainIcons {
		return i.plain
	}
	return i.glyph
}

func (m *Model) paneIcon(name string, open bool) string {
	if !open {
		name += "-off"
	}
	return "  " + m.icon(name) + "  "
}

func paneHelp(name string, open bool, key string) string {
	verb := "Show "
	if open {
		verb = "Hide "
	}
	return verb + name + " · " + key
}
