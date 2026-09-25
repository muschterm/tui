package tui

import "github.com/charmbracelet/x/ansi"

// Control glyphs from Nerd Fonts v3.4.0 glyphnames.json;
// these are interface symbols, never substitutes for provider brand artwork.
// A Nerd Font is the requested default; TUI_GO_ICONS=ascii is an explicit
// fallback because terminal protocols cannot identify the user's font reliably.
var icons = map[string]struct{ glyph, plain string }{
	"settings": {"\ueaf8", "*"}, "code": {"\ueac4", "<>"},
	"star": {"\ueb59", "*"}, "rocket": {"\uead3", "^"},
	"caret-up": {"▴", "^"}, "caret-down": {"▾", "v"},
	"menu": {"☰", "="},
	"left": {"\uebf3", "L+"}, "left-off": {"\uec02", "L-"},
	"right": {"\uebf4", "R+"}, "right-off": {"\uec00", "R-"},
	"bottom": {"\uebf2", "B+"}, "bottom-off": {"\uec01", "B-"},
	// fa-up_right_and_down_left_from_center / fa-down_left_and_up_right_to_center
	// (Nerd Fonts ≥ 3.2.1): the diagonal double arrows of T3's Maximize2/Minimize2.
	"maximize": {"\ued4f", "[]"}, "restore": {"\ued4d", "><"},
	"files": {"\ueaf0", "F"}, "git": {"\uea68", "G"},
	"refresh":  {"\ueb37", "R"}, // cod-refresh
	"terminal": {"\uea85", ">_"}, "agents": {"\uea7e", "A"},
	"plan": {"\ueb67", "P"}, "activity": {"\ueb31", "~"},
	"close": {"\uea76", "x"}, "add": {"\uea60", "+"},
	// cod-remove and cod-discard: the Git surface's Unstage and Discard row actions.
	"unstage": {"\ueb3b", "-"}, "discard": {"\ueae2", "<"},
	// The Font Awesome folder pair shares one 923×808 box and the pen square
	// is 916×916 with the same center, so the header actions align even in
	// wide Nerd Font variants whose glyphs spill past the cell; the earlier
	// cod-new_folder was 20% taller than cod-folder and read as misaligned.
	"compose": {"\uf044", "+"}, // fa-pen_to_square, matching T3's SquarePen action
	"more":    {"\uea7c", "..."}, "theme": {"\ueac6", "*"},
	"more-vertical": {"\ueb10", ":"}, // cod-kebab_vertical
	// Unicode filled triangles remain legible without a private-use font glyph.
	"previous": {"◀", "<"}, "next": {"▶", ">"},
	// fa-circle_arrow_up, fa-stop_circle and fa-paperclip.
	"jump-end": {"\uf063", "v"}, // fa-arrow_down
	"send":     {"\uf0aa", "^"}, "stop": {"\uf28d", "o"}, "attach": {"\uf0c6", "+"},
	// Choice markers: fa-circle_o (U+F10C), fa-dot_circle_o (U+F192, the
	// filled-dot circle), fa-square_o (U+F096) and fa-check_square (U+F14A),
	// Nerd Fonts v3.4.0, each one cell wide by ansi.StringWidth. Every selected
	// single choice, Other… included, uses radio-on in the pending card, the
	// answered card and its preview; multiple choice uses checkbox-on. "check"
	// below marks an answered tab or open-ended answer, never a chosen option.
	"radio": {"\uf10c", "( )"}, "radio-on": {"\uf192", "(*)"},
	"checkbox": {"\uf096", "[ ]"}, "checkbox-on": {"\uf14a", "[x]"},
	"thread": {"\uea6b", "o"}, "trash": {"\uea81", "x"},
	"folder": {"\uf114", "P"}, "project-add": {"\ueec7", "+"}, // fa-folder_o, fa-folder_plus (FA6, Nerd Fonts ≥ 3.2.1)
	"context":   {"\ueb15", "+"},
	"attention": {"\ueaa2", "!"}, "tool": {"\ueb6d", "T"},
	"mcp": {"\ueb15", "M"}, "check": {"\ueab2", "+"},
	// cod-question and cod-shield: the pending question and approval card headers.
	"question": {"\ueb32", "?"}, "approval": {"\ueb53", "!"},
	"reopen": {"\U000f17b3", "<"}, // md-arrow_u_left_top, matching T3's Undo2 un-settle row action
	// Keyboard focus: a plain Unicode bullet in the cell before the focused
	// control, translating T3's focus ring without moving the control.
	"focus": {"•", ">"},
	// Files surface: tree carets, entry types (cod-file, fa-folder_open_o,
	// cod-file_symlink_file, cod-file_binary) and buffer controls (cod-arrow_left,
	// cod-word_wrap, cod-copy).
	"tree-closed": {"▸", ">"}, "tree-open": {"▾", "v"},
	"file": {"\uea7b", " "}, "folder-open": {"\uf115", "+"},
	"file-link": {"\ueaee", "@"}, "file-other": {"\ueae8", "?"},
	"back": {"\uea9b", "<"}, "wrap": {"\ueb80", "w"}, "copy": {"\uebcc", "c"},
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

func (m *Model) paneIcon(name string, open bool, width int) string {
	if !open {
		name += "-off"
	}
	return slotIcon(m.icon(name), width)
}

// slotIcon keeps a one-cell glyph at the chrome's fixed two-cell inset, so
// adjacent controls stay evenly spaced; wider plain symbols are centered.
func slotIcon(icon string, width int) string {
	if ansi.StringWidth(icon) == 1 {
		return fit("  "+icon, width)
	}
	return centered(icon, width)
}

func paneHelp(name string, open bool, key string) string {
	verb := "Show "
	if open {
		verb = "Hide "
	}
	return verb + name + " · " + key
}

// iconsSetting is the effective symbol set: the client's saved Appearance
// choice, else the TUI_GO_ICONS environment default, else Nerd Font glyphs.
func (m *Model) iconsSetting() string {
	if m.state.Icons == "ascii" || m.state.Icons == "nerd" {
		return m.state.Icons
	}
	if m.envIcons == "ascii" {
		return "ascii"
	}
	return "nerd"
}

func (m *Model) applyIcons() { m.plainIcons = m.iconsSetting() == "ascii" }

// setEnvIcons records the environment default without overriding a saved choice.
func (m *Model) setEnvIcons(value string) {
	m.envIcons = value
	m.applyIcons()
}

func iconsLabel(value string) string {
	if value == "ascii" {
		return "ASCII"
	}
	return "Nerd Font"
}
