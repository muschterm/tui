package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func stripHits(f frame, prefix string) []hit {
	var out []hit
	for _, h := range f.hits {
		if strings.HasPrefix(h.Key, prefix) {
			out = append(out, h)
		}
	}
	return out
}

func findHit(f frame, key string) (hit, bool) {
	for _, h := range f.hits {
		if h.Key == key {
			return h, true
		}
	}
	return hit{}, false
}

func stripModel(t *testing.T, height int, atts ...protocol.Attachment) (*Model, *fakeArtifacts) {
	t.Helper()
	m, api := intakeModel(t, &fakeClipboard{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: height})
	m.viewState().Attachments = slices.Clone(atts)
	m.configureInputs()
	return m, api
}

var stripAtts = []protocol.Attachment{
	{Kind: "workspace-file", Name: "docs/notes.md", Source: "docs/notes.md"},
	{Kind: "artifact", Name: "clipboard.png", ArtifactID: "art-a", MediaType: "image/png", Width: 4, Height: 3},
	{Kind: "copied-file", Name: "report.txt", Source: "/tmp/report.txt"},
}

func TestAttachmentStripChipsInsideComposer(t *testing.T) {
	m, _ := stripModel(t, 40, stripAtts...)
	f := m.render()
	chips := stripHits(f, chipPrefix)
	closes := stripHits(f, chipClosePrefix)
	if len(closes) != 3 || len(chips) != 6 {
		t.Fatalf("chips %d closes %d", len(chips), len(closes))
	}
	// One row inside the outline, directly above the padding and typing rows.
	y := closes[0].Rect.Y
	if y != f.composer.Y+1 || f.prompt.Y != y+2 {
		t.Fatalf("strip row %d composer %+v prompt %+v", y, f.composer, f.prompt)
	}
	if _, ok := findHit(f, "attachments"); ok {
		t.Fatal("aggregate control shown with the strip")
	}
	row := ansi.Strip(f.rows[y])
	for _, name := range []string{"docs/notes.md", "clipboard.png", "report.txt"} {
		if !strings.Contains(row, name) {
			t.Fatalf("strip row lacks %q: %q", name, row)
		}
	}
	// Close is the icon slot only (glyph and spill cell), separate from the label.
	c, s := closes[0], chips[0]
	if c.Rect.W != 2 || c.Slot.W != 3 || s.Rect.X != c.Rect.X+2 || c.Action.Kind != "attachment-remove" || s.Action.Kind != "attachment-view" {
		t.Fatalf("close %+v label %+v", c, s)
	}
	// Chips follow the prompt in Tab order.
	var keys []string
	for _, h := range f.hits {
		keys = append(keys, h.Key)
	}
	pi, ci := -1, -1
	for i, k := range keys {
		if k == "prompt" && pi < 0 {
			pi = i
		}
		if k == chipClosePrefix+"0" && ci < 0 {
			ci = i
		}
	}
	if pi < 0 || ci < pi {
		t.Fatal("chips precede the prompt in Tab order")
	}
	assertRowWidths(t, m, "strip")
}

func TestAttachmentChipCloseRevealedOnHoverAndFocus(t *testing.T) {
	m, _ := stripModel(t, 40, stripAtts...)
	closeGlyph := m.icon("close")
	f := m.render()
	c, _ := findHit(f, chipClosePrefix+"2")
	cell := func() string {
		return ansi.Strip(cutCells(m.render().rows[c.Rect.Y], c.Rect.X, c.Rect.X+2))
	}
	if strings.Contains(cell(), closeGlyph) {
		t.Fatal("close shown at rest")
	}
	m.hover = chipPrefix + "2"
	if !strings.Contains(cell(), closeGlyph) {
		t.Fatal("hover did not reveal close")
	}
	m.hover = ""
	m.setFocus(chipClosePrefix + "2")
	if !strings.Contains(cell(), closeGlyph) {
		t.Fatal("focus did not reveal close")
	}
	// Repaint with hover/focus changes never grows the rows.
	first := len(strings.Join(m.render().rows, ""))
	for range 5 {
		m.hover = chipPrefix + "1"
		m.render()
		m.hover = ""
	}
	if again := len(strings.Join(m.render().rows, "")); again != first {
		t.Fatalf("repaint grew %d → %d", first, again)
	}
}

func TestAttachmentChipKeyboardViewAndRemove(t *testing.T) {
	m, api := stripModel(t, 40, stripAtts...)
	m.prompt.SetValue("keep me")
	m.viewState().Draft = "keep me"
	m.setFocus(chipPrefix + "0")
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.viewer == nil || m.viewer.att.Name != "docs/notes.md" || m.viewer.origin != chipPrefix+"0" {
		t.Fatalf("Enter did not open the chip's viewer: %+v", m.viewer)
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != chipPrefix+"0" {
		t.Fatalf("focus %q after close", m.focus)
	}
	// Delete on the image chip removes it and frees its staged artifact.
	m.setFocus(chipPrefix + "1")
	pump(t, m, m.key(tea.KeyPressMsg{Code: tea.KeyDelete}))
	atts := m.viewState().Attachments
	if len(atts) != 2 || atts[1].Name != "report.txt" || len(api.deleted) != 1 || api.deleted[0] != "art-a" {
		t.Fatalf("attachments %v deleted %v", atts, api.deleted)
	}
	if m.focus != chipPrefix+"1" {
		t.Fatalf("focus %q after removal", m.focus)
	}
	// Backspace on the last chip moves focus to the new last chip; files
	// that were never uploaded delete nothing.
	pump(t, m, m.key(tea.KeyPressMsg{Code: tea.KeyBackspace}))
	if len(m.viewState().Attachments) != 1 || m.focus != chipPrefix+"0" || len(api.deleted) != 1 {
		t.Fatalf("focus %q deleted %v", m.focus, api.deleted)
	}
	pump(t, m, m.key(tea.KeyPressMsg{Code: tea.KeyDelete}))
	if len(m.viewState().Attachments) != 0 || m.focus != "prompt" || m.prompt.Value() != "keep me" {
		t.Fatalf("focus %q draft %q", m.focus, m.prompt.Value())
	}
	// A stale removal (different attachment at the index) removes nothing.
	m.viewState().Attachments = []protocol.Attachment{{Kind: "file", Name: "b"}}
	m.activate(action{Kind: "attachment-remove", Index: 0, Value: "a"})
	if len(m.viewState().Attachments) != 1 {
		t.Fatal("stale removal removed another attachment")
	}
}

func TestAttachmentChipPointerCloseAndView(t *testing.T) {
	m, _ := stripModel(t, 40, stripAtts...)
	f := m.render()
	label, _ := findHit(f, chipPrefix+"2")
	m.Update(tea.MouseClickMsg{X: label.Rect.X + 2, Y: label.Rect.Y, Button: tea.MouseLeft})
	if m.viewer == nil || m.viewer.att.Name != "report.txt" {
		t.Fatal("label click did not open the viewer")
	}
	m.closeAttachmentViewer()
	c, _ := findHit(m.render(), chipClosePrefix+"0")
	m.Update(tea.MouseClickMsg{X: c.Rect.X, Y: c.Rect.Y, Button: tea.MouseLeft})
	if len(m.viewState().Attachments) != 2 || m.viewer != nil {
		t.Fatal("icon-slot click did not remove only that attachment")
	}
}

func TestAttachmentStripOverflowAndCollapse(t *testing.T) {
	var many []protocol.Attachment
	for _, n := range []string{"a-rather-long-file-name-1.go", "a-rather-long-file-name-2.go", "a-rather-long-file-name-3.go", "a-rather-long-file-name-4.go", "a-rather-long-file-name-5.go", "a-rather-long-file-name-6.go"} {
		many = append(many, protocol.Attachment{Kind: "file", Name: n})
	}
	m, _ := stripModel(t, 40, many...)
	f := m.render()
	more, ok := findHit(f, "attachments")
	shown := len(stripHits(f, chipClosePrefix))
	if !ok || shown == 0 || shown >= len(many) || !strings.Contains(ansi.Strip(f.rows[more.Rect.Y]), "+"+string(rune('0'+len(many)-shown))) {
		t.Fatalf("overflow: shown %d more %+v", shown, more)
	}
	m.activate(more.Action)
	if menuIndexOf(m, "View "+many[5].Name) < 0 || menuIndexOf(m, "Remove "+many[5].Name) < 0 {
		t.Fatal("+N does not open the attachments menu")
	}
	m.menu = nil
	assertRowWidths(t, m, "overflow")
	// A short pane keeps the prompt rows: one aggregate control, no chips.
	m, _ = stripModel(t, 26, stripAtts...)
	f = m.render()
	if len(stripHits(f, chipPrefix)) != 0 || m.attachmentStripRows() != 0 {
		t.Fatal("chips in a short pane")
	}
	if h, ok := findHit(f, "attachments"); !ok || !strings.Contains(ansi.Strip(f.rows[h.Rect.Y]), "3 context attachments") {
		t.Fatal("no aggregate control in a short pane")
	}
	assertRowWidths(t, m, "collapsed")
}

func TestAttachmentStripThumbnailsOnlyWithGraphics(t *testing.T) {
	m, api := graphicsModel(t)
	api.fetch = map[string][]byte{"art-a": pngBytes(t, 40, 30)}
	api.fetchType = map[string]string{"art-a": "image/png"}
	dir := t.TempDir()
	local := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(local, pngBytes(t, 20, 20), 0o600); err != nil {
		t.Fatal(err)
	}
	atts := []protocol.Attachment{stripAtts[0], stripAtts[1], {Kind: "copied-file", Name: "photo.png", Source: local}}
	m.viewState().Attachments = atts
	m.configureInputs()
	if m.attachmentStripRows() != 3 {
		t.Fatalf("thumbnail strip rows %d", m.attachmentStripRows())
	}
	// Loading: fallback blocks of the same size, no placeholders yet.
	for _, row := range m.render().rows {
		if placeholderRow(row) {
			t.Fatal("placeholders before the thumbnails are ready")
		}
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	pump(t, m, cmd)
	if len(m.thumbs) != 2 {
		t.Fatalf("thumbs %v", m.thumbs)
	}
	f := m.render()
	count := 0
	for _, row := range f.rows {
		if placeholderRow(row) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("placeholder rows %d, want 2", count)
	}
	closeHit, _ := findHit(f, chipClosePrefix+"1")
	if closeHit.Rect.Y != f.composer.Y+3 || f.prompt.Y != closeHit.Rect.Y+2 {
		t.Fatalf("chip row %d composer %+v prompt %+v", closeHit.Rect.Y, f.composer, f.prompt)
	}
	assertRowWidths(t, m, "thumbnails")
	first := len(strings.Join(m.render().rows, ""))
	for range 5 {
		m.hover = chipPrefix + "1"
		m.render()
		m.hover = ""
	}
	if again := len(strings.Join(m.render().rows, "")); again != first {
		t.Fatalf("thumbnail repaint grew %d → %d", first, again)
	}
	// Removing an image releases its thumbnail's terminal image.
	id, _ := m.imgs().Ready("thumb:art-a")
	out := pump(t, m, m.activate(action{Kind: "attachment-remove", Index: 1, Value: "artifact:art-a"}))
	_, next := m.Update(nil)
	out2 := pump(t, m, next)
	if raw := out.raw.String() + out2.raw.String(); !strings.Contains(raw, "a=d,d=I,i="+strconv.Itoa(id)) {
		t.Fatalf("thumbnail not released: %q", raw)
	}
	// Without graphics the strip stays one row.
	m.graphics.state = graphicsUnavailable
	if m.attachmentStripRows() != 1 {
		t.Fatal("thumbnails without graphics")
	}
	assertRowWidths(t, m, "no graphics")
}
