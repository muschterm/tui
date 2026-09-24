package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// attachment_strip.go is the composer's attachment strip: one square-fill
// chip per draft attachment inside the prompt outline, directly above the
// typing area. Like a surface tab, a chip's kind-icon slot reveals a close
// glyph on hover or focus and removes that attachment; the label opens the
// read-only viewer. With confirmed kitty graphics, image chips carry a small
// thumbnail above them. A short pane collapses the strip to the aggregate
// control above the composer.

const (
	chipPrefix      = "attachment-chip:"
	chipClosePrefix = "attachment-chip-close:"
	chipMaxWidth    = 24
	chipMinWidth    = 6
)

type stripChip struct {
	index, x, width int
	thumb           bool
}

type stripLayout struct {
	chips        []stripChip
	more         int // attachments hidden behind the +N chip
	moreX, moreW int
}

// attachmentsCollapsed reports whether the draft's attachments use the single
// aggregate control instead of the strip, keeping the prompt's typing rows and
// controls in a short pane.
func (m *Model) attachmentsCollapsed() bool {
	return len(m.viewState().Attachments) > 0 && m.compact()
}

// attachmentStripRows is the strip's height inside the prompt outline.
func (m *Model) attachmentStripRows() int {
	if len(m.viewState().Attachments) == 0 || m.compact() {
		return 0
	}
	if m.thumbnailStrip() {
		return thumbRows + 1
	}
	return 1
}

func chipName(a protocol.Attachment) string {
	if name := safe(singleLine(a.Name)); name != "" {
		return name
	}
	return "Attachment"
}

// stripLayout places chips left to right within width cells, one blank cell
// apart. When they do not all fit, a trailing +N chip stands for the rest.
func (m *Model) stripLayout(width int) stripLayout {
	atts := m.viewState().Attachments
	thumbs := m.thumbnailStrip()
	widths := make([]int, len(atts))
	total := max(0, len(atts)-1)
	for i, a := range atts {
		w := min(chipMaxWidth, ansi.StringWidth(chipName(a))+5)
		if thumbs && thumbKey(a) != "" {
			w = max(w, thumbCols+2)
		}
		widths[i] = max(chipMinWidth, w)
		total += widths[i]
	}
	var l stripLayout
	place := func(i, x, w int) {
		l.chips = append(l.chips, stripChip{index: i, x: x, width: w, thumb: thumbs && thumbKey(atts[i]) != ""})
	}
	if total <= width {
		x := 0
		for i, w := range widths {
			place(i, x, w)
			x += w + 1
		}
		return l
	}
	moreW := len("+"+strconv.Itoa(len(atts))) + 2
	available := width - moreW - 1
	x := 0
	for i, w := range widths {
		if x+w > available {
			if i == 0 && available >= chipMinWidth {
				place(0, 0, available)
				x = available + 1
			}
			break
		}
		place(i, x, w)
		x += w + 1
	}
	l.more = len(atts) - len(l.chips)
	l.moreW = len("+"+strconv.Itoa(l.more)) + 2
	l.moreX = x
	if x+l.moreW > width {
		l.moreW = 0
	}
	return l
}

// renderAttachmentStrip paints the strip's rows at (x, y) across width cells
// on the prompt fill. Chips sit on its last row.
func (m *Model) renderAttachmentStrip(f *frame, x, y, width, rows int) {
	if width <= 0 || rows <= 0 {
		return
	}
	p := m.colors()
	atts := m.viewState().Attachments
	l := m.stripLayout(width)
	chipY := y + rows - 1
	for _, c := range l.chips {
		a := atts[c.index]
		if c.thumb && rows > thumbRows {
			for i, line := range m.thumbnailRows(a, p.input) {
				f.put(shell.Rect{X: x + c.x + (c.width-thumbCols)/2, Y: chipY - thumbRows + i, W: thumbCols, H: 1}, line)
			}
		}
		m.attachmentChip(f, x+c.x, chipY, c.width, a, c.index)
	}
	if l.more > 0 && l.moreW > 0 {
		f.compactButton(m, x+l.moreX, chipY, l.moreW, "+"+strconv.Itoa(l.more), "attachments", action{Kind: "attachments"}, false, normalControl)
		f.hits[len(f.hits)-1].Label = fmt.Sprintf("%d more attachments · view / remove", l.more)
	}
}

// attachmentChip mirrors a surface tab: end cap, icon slot (glyph and its
// spill cell, which close), a gap, the name, end cap. The close glyph shows
// while the chip is hovered or its icon slot has keyboard focus.
func (m *Model) attachmentChip(f *frame, x, y, width int, a protocol.Attachment, index int) {
	p := m.colors()
	name := chipName(a)
	selectKey, closeKey := chipPrefix+strconv.Itoa(index), chipClosePrefix+strconv.Itoa(index)
	state := m.controlState(false, selectKey, closeKey)
	v := m.componentStyle(squareFill, state, p.text, p.panel)
	icon := m.attachmentKindIcon(a.Kind)
	if imageAttachment(a) || thumbKey(a) != "" {
		icon = m.attachmentKindIcon("image")
	}
	if state.Hovered || m.focus == closeKey {
		icon = m.icon("close")
	}
	view := action{Kind: "attachment-view", Value: "draft:" + a.Name, Index: index}
	remove := action{Kind: "attachment-remove", Value: attachmentID(a, index), Index: index}
	f.compactControl(m, x, y, width, fit(icon, 3)+fit(ansi.Truncate(name, width-5, "…"), width-5), v)
	f.hits = append(f.hits,
		hit{Rect: shell.Rect{X: x + 1, Y: y, W: 2, H: 1}, Action: remove, Label: "Remove " + name + " · Delete", Key: closeKey, Slot: shell.Rect{X: x + 1, Y: y, W: 3, H: 1}},
		hit{Rect: shell.Rect{X: x + 3, Y: y, W: width - 3, H: 1}, Action: view, Label: name + " · Enter view · Delete remove", Key: selectKey},
		hit{Rect: shell.Rect{X: x, Y: y, W: 1, H: 1}, Action: view, Label: name + " · Enter view · Delete remove", Key: selectKey},
	)
}

// chipIndex parses a chip or chip-close focus key.
func chipIndex(key string) (int, bool) {
	for _, prefix := range []string{chipPrefix, chipClosePrefix} {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			i, err := strconv.Atoi(rest)
			return i, err == nil
		}
	}
	return 0, false
}

// chipKey handles Delete/Backspace on a focused chip.
func (m *Model) chipKey(s string) (action, bool) {
	i, ok := chipIndex(m.focus)
	if !ok || s != "delete" && s != "backspace" {
		return action{}, false
	}
	atts := m.viewState().Attachments
	if i >= len(atts) || m.attachmentsCollapsed() {
		// A chip hidden by the collapsed layout is not a removal target.
		return action{}, false
	}
	return action{Kind: "attachment-remove", Index: i, Value: attachmentID(atts[i], i)}, true
}

// fixChipFocus keeps keyboard focus on the strip after a removal: the chip
// that took the removed one's place, else the new last chip, else the prompt.
func (m *Model) fixChipFocus() tea.Cmd {
	i, ok := chipIndex(m.focus)
	if !ok {
		return nil
	}
	n := len(m.viewState().Attachments)
	switch {
	case n == 0:
		return m.setFocus("prompt")
	case i >= n:
		m.focus = chipPrefix + strconv.Itoa(n-1)
	}
	return nil
}

// focusable reports whether a restored focus key still names a control;
// only strip chips can disappear while the viewer is open.
func (m *Model) focusable(key string) bool {
	i, ok := chipIndex(key)
	return !ok || i < len(m.viewState().Attachments) && !m.attachmentsCollapsed()
}

// attachmentID is a removal guard unique within one draft: the staged
// artifact id when there is one, else source, name and position.
func attachmentID(a protocol.Attachment, index int) string {
	if a.ArtifactID != "" {
		return "artifact:" + a.ArtifactID
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", a.Kind, a.Source, a.Name, index)
}

// artifactInUse reports whether a staged artifact id is still referenced by
// anything this client knows: the in-flight Send capture, a pending or
// uncertain command (Retry), an unreconciled started draft, any draft, or a
// queued prompt. Such an id is never deleted from staging.
func (m *Model) artifactInUse(id string) bool {
	if id == "" {
		return true
	}
	has := func(list []protocol.Attachment) bool {
		for _, a := range list {
			if a.ArtifactID == id {
				return true
			}
		}
		return false
	}
	if sc := m.sendCapture; sc != nil && has(sc.command.Attachments) {
		return true
	}
	for _, c := range []*protocol.Command{m.busy, m.state.Pending} {
		if c != nil && has(c.Attachments) {
			return true
		}
	}
	if sd := m.state.StartedDraft; sd != nil && has(sd.Command.Attachments) {
		return true
	}
	for _, views := range []map[string]*threadView{m.state.Threads, m.state.DraftThreads} {
		for _, v := range views {
			if v != nil && has(v.Attachments) {
				return true
			}
		}
	}
	for _, t := range m.snapshot.Threads {
		for _, q := range t.Queue {
			if has(q.Attachments) {
				return true
			}
		}
	}
	return false
}
