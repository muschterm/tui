package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// surface_panel.go builds right-host surface content from the panel
// constructs in panel.go: uppercase muted section headings, rules between
// entries, label/value pairs for short facts and a muted label above wrapped
// long values. Blocks are width-independent; surfaceRows wraps them into
// painted rows, and surfaceText flattens them into plain text.

type surfaceBlockKind int

const (
	surfaceHeadingBlock surfaceBlockKind = iota // uppercase muted heading, optional muted value at the right
	surfaceRuleBlock                            // full-width rule
	surfaceGapBlock                             // blank row
	surfaceStatusBlock                          // status glyph, title, muted state at the right
	surfacePairBlock                            // label/value pair; label above the value when it cannot fit
	surfaceLongBlock                            // muted label above a wrapped value
	surfaceTextBlock                            // wrapped text in ink
	surfaceRawBlock                             // sanitized raw output, wrapped
)

type surfaceBlock struct {
	kind                surfaceBlockKind
	label, value, glyph string
	ink                 string
	bold                bool
	// note is a muted qualifier of a pair's value, shown after it as
	// "value · note" when that fits, else right-aligned below the value.
	note string
}

type surfaceRowKind int

const (
	surfaceHeadingRow surfaceRowKind = iota
	surfaceRuleRow
	surfacePairRow
	surfaceStatusRow
	surfaceTextRow
)

// surfaceRow is one painted row of a surface body: text holds the heading,
// label or wrapped line; value is a right-aligned value (heading count, pair
// value or status); glyph is a status glyph in ink; indent offsets text.
type surfaceRow struct {
	kind        surfaceRowKind
	text, value string
	glyph, ink  string
	indent      int
	bold        bool
	note        string // muted suffix after a pair row's value
}

// detailBlocks renders retained detail as muted text lines. Activity detail
// is free-form server/tool text (tool details arrive as encoded JSON), so no
// line is ever split into a label/value pair: a "Note: x" or "Error: x" line
// stays text. Blank lines keep the structure of tool output and diffs; runs
// of them collapse to one, and leading or trailing ones are dropped.
func detailBlocks(m *Model, detail string) []surfaceBlock {
	var out []surfaceBlock
	blank := false
	for _, line := range strings.Split(detail, "\n") {
		if strings.TrimSpace(line) == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, surfaceBlock{kind: surfaceTextBlock, value: ""})
			blank = false
		}
		out = append(out, surfaceBlock{kind: surfaceTextBlock, value: line, ink: m.colors().muted})
	}
	return out
}

func statusBlock(m *Model, title, state string, bold bool) surfaceBlock {
	glyph, ink := panelStatusMark(m, state)
	if state == "" {
		glyph = "" // Messages without a lifecycle state carry no status mark.
	}
	return surfaceBlock{kind: surfaceStatusBlock, label: title, value: state, glyph: glyph, ink: ink, bold: bold}
}

func separated(blocks []surfaceBlock, next ...surfaceBlock) []surfaceBlock {
	if len(blocks) > 0 {
		blocks = append(blocks, surfaceBlock{kind: surfaceGapBlock}, surfaceBlock{kind: surfaceRuleBlock}, surfaceBlock{kind: surfaceGapBlock})
	}
	return append(blocks, next...)
}

// surfaceBlocks is the structured content of one right-host surface,
// honouring the view's DetailID filter.
func (m *Model) surfaceBlocks(s shell.Surface) []surfaceBlock {
	t := m.thread()
	v := m.viewState()
	p := m.colors()
	heading := func(text, value string) surfaceBlock {
		return surfaceBlock{kind: surfaceHeadingBlock, label: text, value: value}
	}
	gap := surfaceBlock{kind: surfaceGapBlock}
	var b []surfaceBlock
	switch s.Kind {
	case "plan":
		done := 0
		for _, step := range t.Plan {
			if step.State == "completed" {
				done++
			}
		}
		count := ""
		if len(t.Plan) > 0 {
			count = fmt.Sprintf("%d/%d", done, len(t.Plan))
		}
		b = append(b, heading("Current plan", count), gap)
		for _, step := range t.Plan {
			b = append(b, statusBlock(m, step.Title, step.State, false))
		}
		if len(t.Plan) == 0 {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "No plan reported", ink: p.muted})
		}
	case "agents":
		for _, c := range t.Children {
			if v.DetailID != "" && c.ID != v.DetailID {
				continue
			}
			entry := []surfaceBlock{statusBlock(m, c.Name, c.State, true)}
			if c.ParentID != "" {
				parent := c.ParentID
				if thread, ok := m.threadByID(parent); ok && thread.Title != "" {
					parent = thread.Title
				}
				entry = append(entry, surfaceBlock{kind: surfacePairBlock, label: "Parent", value: parent})
			}
			for _, a := range c.Activity {
				entry = append(entry, gap)
				entry = append(entry, activityBlocks(m, a)...)
			}
			b = separated(b, entry...)
		}
		if len(b) == 0 {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "Child history unavailable", ink: p.muted})
		}
		b = append([]surfaceBlock{heading("Agents", ""), gap}, b...)
	case "activity":
		if v.DetailID == "usage" {
			b = append(b, heading("Usage", ""), gap)
			for _, line := range m.usageLines() {
				b = append(b, pairOrText(m, line))
			}
			b = append(b, gap, surfaceBlock{kind: surfaceRuleBlock}, gap, heading("Capabilities", ""), gap)
			for _, c := range m.snapshot.Capabilities {
				b = append(b, surfaceBlock{kind: surfaceTextBlock, value: c, ink: p.text})
			}
			b = append(b, gap, surfaceBlock{kind: surfaceRuleBlock}, gap, heading("Terminal", ""), gap,
				surfaceBlock{kind: surfacePairBlock, label: "Keyboard", value: m.keyboard})
			b = append(b, colorBlocks(m)...)
			b = append(b, surfaceBlock{kind: surfacePairBlock, label: "Graphics", value: "text fallback"},
				surfaceBlock{kind: surfacePairBlock, label: "Image support", value: "not probed"})
			return b
		}
		var entries []surfaceBlock
		for _, a := range t.Activity {
			if v.DetailID != "" && a.ID != v.DetailID {
				continue
			}
			entries = separated(entries, activityBlocks(m, a)...)
		}
		for _, r := range t.Requests {
			if v.DetailID != "" && r.ID != v.DetailID {
				continue
			}
			entry := []surfaceBlock{statusBlock(m, r.Title, r.State, true)}
			if r.Detail != "" {
				entry = append(entry, surfaceBlock{kind: surfaceTextBlock, value: r.Detail, ink: p.text})
			}
			entry = append(entry,
				surfaceBlock{kind: surfacePairBlock, label: "State", value: r.State},
				surfaceBlock{kind: surfacePairBlock, label: "Delivery", value: requestDeliveryDescription(r.Delivery)},
				surfaceBlock{kind: surfacePairBlock, label: "Choices", value: strings.Join(r.Choices, ", ")})
			if r.Action != "" {
				entry = append(entry, surfaceBlock{kind: surfacePairBlock, label: "Response", value: questionActionOutcome(r.Action) + " · no answer sent"})
			}
			entries = separated(entries, entry...)
		}
		if len(entries) == 0 {
			entries = append(entries, surfaceBlock{kind: surfaceTextBlock, value: "No retained activity for this selection", ink: p.muted})
		}
		b = append(append(b, heading("Activity", ""), gap), entries...)
	case "terminal":
		// The right host opens every surface with its section heading; the
		// bottom panel reuses terminalBlocks without it (it has no title row).
		return append([]surfaceBlock{heading("Terminal", ""), gap}, m.terminalBlocks(s.ID)...)
	case "files":
		b = append(b, heading("Files", ""), gap,
			surfaceBlock{kind: surfaceLongBlock, label: "Checkout", value: t.Checkout}, gap,
			statusBlock(m, "Collaborative editor", "unavailable", true),
			surfaceBlock{kind: surfaceTextBlock, value: "File writes are not enabled in this slice.", ink: p.muted},
			gap, surfaceBlock{kind: surfaceRuleBlock}, gap, heading("Planned validation", ""), gap)
		for _, item := range []string{"Concurrent edits and own-edit undo", "Durable buffers versus disk saves", "External-change reconciliation"} {
			b = append(b, surfaceBlock{kind: surfaceTextBlock, value: "• " + item, ink: p.text})
		}
	case "git":
		b = append(b, heading("Git", ""), gap)
		b = append(b, m.gitCheckoutBlocks()...)
		b = append(b, gap, surfaceBlock{kind: surfaceRuleBlock}, gap, heading("Changes", ""), gap,
			statusBlock(m, "Git integration", "unavailable", true))
	}
	return b
}

// gitCheckoutBlocks renders the read-only checkout context as explicit
// pairs: the checkout path, then Branch, HEAD or Revision only for the
// structured states the server reported. Values are plain text, never a
// success mark; loading, non-Git and unavailable values stay muted.
func (m *Model) gitCheckoutBlocks() []surfaceBlock {
	info := m.displayedCheckout()
	muted := m.colors().muted
	pair := func(label, value string) surfaceBlock {
		return surfaceBlock{kind: surfacePairBlock, label: label, value: value}
	}
	quiet := func(label, value string) surfaceBlock {
		return surfaceBlock{kind: surfacePairBlock, label: label, value: value, ink: muted}
	}
	out := []surfaceBlock{{kind: surfaceLongBlock, label: "Checkout", value: info.Path}}
	switch {
	case m.checkoutLoading:
		return append(out, quiet("Branch", "loading…"))
	case info.State == "branch":
		return append(out, pair("Branch", info.Branch))
	case info.State == "unborn":
		return append(out, pair("Branch", info.Branch), pair("HEAD", "unborn"))
	case info.State == "detached":
		return append(out, pair("HEAD", "detached"), pair("Revision", info.Revision))
	case info.State == "non-git":
		return append(out, quiet("Repository", "not a Git checkout"))
	}
	return append(out, quiet("Branch", "unavailable"))
}

// colorBlocks renders the color diagnostic as one Colors pair whose caveat,
// when the profile carries one, is a muted note of the value.
func colorBlocks(m *Model) []surfaceBlock {
	d := m.colorDiagnosticParts()
	return []surfaceBlock{{kind: surfacePairBlock, label: "Colors", value: d.profile, note: d.note}}
}

// pairOrText splits a "Label: value" line into a pair block. Use it only for
// lines the TUI itself composes (usage and terminal diagnostics); server or
// tool text goes through detailBlocks and is never split.
func pairOrText(m *Model, line string) surfaceBlock {
	if label, value, ok := strings.Cut(line, ": "); ok && label != "" {
		return surfaceBlock{kind: surfacePairBlock, label: label, value: value}
	}
	return surfaceBlock{kind: surfaceTextBlock, value: line, ink: m.colors().text}
}

// activityBlocks is one retained activity: a status row, its text, the
// free-form text of its detail and any captured prompt attachments, each
// with a muted label above its raw captured content.
func activityBlocks(m *Model, a protocol.Activity) []surfaceBlock {
	p := m.colors()
	var out []surfaceBlock
	if a.Title != "" || a.State != "" {
		out = append(out, statusBlock(m, activityTitle(a), a.State, true))
	}
	if a.Text != "" {
		out = append(out, surfaceBlock{kind: surfaceTextBlock, value: a.Text, ink: p.text})
	}
	if a.Tool != nil {
		out = append(out, toolBlocks(m, a.State, *a.Tool)...)
	} else if detail := detailBlocks(m, a.Detail); len(detail) > 0 {
		out = append(append(out, surfaceBlock{kind: surfaceGapBlock}), detail...)
	}
	if a.Prompt != nil {
		for _, at := range a.Prompt.Attachments {
			name := at.Name
			if at.Source != "" && at.Source != name {
				name += " · " + at.Source
			}
			out = append(out, surfaceBlock{kind: surfaceLongBlock, label: fmt.Sprintf("%s · %s · %d bytes captured", at.Kind, name, len(at.Content)), value: at.Content})
		}
	}
	return out
}

// toolBlocks renders the structured tool payload: explicit Kind, Status and
// Locations pairs (long values stack), then content and raw input/output as
// muted text. Pairs come only from these fields, never from Detail text.
//
// Status reflects the effective activity state, which server settle paths
// (interrupt, restart) change without touching the agent-reported tool
// status. A differing reported status stays visible as a plain muted
// Reported pair, so it never paints a stale active or success mark.
func toolBlocks(m *Model, state string, tool protocol.ToolDetail) []surfaceBlock {
	p := m.colors()
	out := []surfaceBlock{{kind: surfaceGapBlock}}
	if tool.Kind != "" {
		out = append(out, surfaceBlock{kind: surfacePairBlock, label: "Kind", value: tool.Kind})
	}
	effective := state
	if effective == "" {
		effective = tool.Status
	}
	// The activity's status row already shows a non-empty state; repeat it as
	// a pair only when the row has none and the tool reported one.
	if effective != "" && state == "" {
		glyph, ink := panelStatusMark(m, effective)
		out = append(out, surfaceBlock{kind: surfacePairBlock, label: "Status", value: glyph + " " + effective, ink: ink})
	}
	if tool.Status != "" && !sameToolStatus(tool.Status, effective) {
		out = append(out, surfaceBlock{kind: surfacePairBlock, label: "Reported", value: tool.Status, ink: p.muted})
	}
	switch len(tool.Locations) {
	case 0:
	case 1:
		out = append(out, surfaceBlock{kind: surfacePairBlock, label: "Location", value: tool.Locations[0]})
	default:
		out = append(out, surfaceBlock{kind: surfaceLongBlock, label: "Locations", value: strings.Join(tool.Locations, "\n")})
	}
	muted := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		out = append(out, surfaceBlock{kind: surfaceGapBlock}, surfaceBlock{kind: surfaceTextBlock, value: label, ink: p.muted, bold: true})
		out = append(out, detailBlocks(m, value)...)
	}
	muted("Content", strings.Join(tool.Content, "\n"))
	muted("Input", tool.RawInput)
	muted("Output", tool.RawOutput)
	return out
}

// sameToolStatus reports whether an ACP tool status names the given activity
// state (in_progress is running; completed and failed keep their names).
func sameToolStatus(reported, state string) bool {
	if reported == "in_progress" {
		reported = "running"
	}
	return strings.EqualFold(reported, state)
}

// activityTitle names an untitled activity by its role, so a status row
// never shows a bare glyph.
func activityTitle(a protocol.Activity) string {
	if t := strings.TrimSpace(a.Title); t != "" {
		return a.Title
	}
	switch a.Role {
	case "user":
		return "Prompt"
	case "agent":
		return "Reply"
	case "thought":
		return "Thinking"
	case "question-answer":
		return "Answer"
	case "tool":
		return "Tool call"
	case "mcp":
		return "MCP call"
	case "":
		return "Activity"
	}
	return title(a.Role)
}

func (m *Model) terminalBlocks(id string) []surfaceBlock {
	for _, t := range m.snapshot.Terminals {
		if t.ID == id {
			return []surfaceBlock{
				statusBlock(m, "Terminal", t.State, true),
				{kind: surfacePairBlock, label: "Session", value: t.ID},
				{kind: surfacePairBlock, label: "Controller", value: t.Controller},
				{kind: surfaceGapBlock}, {kind: surfaceRuleBlock}, {kind: surfaceGapBlock},
				{kind: surfaceRawBlock, value: t.Output},
			}
		}
	}
	return []surfaceBlock{{kind: surfaceTextBlock, value: "Terminal session unavailable · open a new session explicitly", ink: m.colors().muted}}
}

// surfaceText flattens a surface's blocks to plain text: pairs read
// "Label: value" and status rows "glyph title · state".
func (m *Model) surfaceText(s shell.Surface) string {
	var lines []string
	for _, b := range m.surfaceBlocks(s) {
		switch b.kind {
		case surfaceHeadingBlock:
			lines = append(lines, strings.TrimSpace(strings.ToUpper(b.label)+" "+b.value))
		case surfaceRuleBlock, surfaceGapBlock:
			lines = append(lines, "")
		case surfaceStatusBlock:
			line := strings.TrimSpace(b.glyph + " " + b.label)
			if b.value != "" {
				line += " · " + b.value
			}
			lines = append(lines, line)
		case surfacePairBlock:
			line := b.label + ": " + b.value
			if b.note != "" {
				line += " · " + b.note
			}
			lines = append(lines, line)
		case surfaceLongBlock:
			lines = append(lines, b.label, b.value)
		default:
			lines = append(lines, b.value)
		}
	}
	return strings.Join(lines, "\n")
}

// surfacePairInline keeps a pair on one row when panelPairShort allows it or
// when the value is a short status word or phrase (at most 20 cells) that
// fits beside its label: narrow hosts would otherwise stack values such as
// "legacy keyboard" under their labels.
func surfacePairInline(width int, label, value string) bool {
	return panelPairShort(width, label, value) ||
		(panelPairFits(width, label, value) && ansi.StringWidth(singleLine(value)) <= 20)
}

func wrapCells(s string, width int) []string {
	return strings.Split(ansi.Wrap(safe(s), max(1, width), ""), "\n")
}

// surfaceRows wraps blocks into rows of the given width.
func (m *Model) surfaceRows(blocks []surfaceBlock, width int) []surfaceRow {
	p := m.colors()
	var rows []surfaceRow
	long := func(label, value string) {
		rows = append(rows, surfaceRow{kind: surfaceTextRow, text: safe(singleLine(label)), ink: p.muted})
		for _, line := range wrapCells(value, width) {
			rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: p.text})
		}
	}
	for _, b := range blocks {
		switch b.kind {
		case surfaceHeadingBlock:
			rows = append(rows, surfaceRow{kind: surfaceHeadingRow, text: safe(b.label), value: safe(b.value)})
		case surfaceRuleBlock:
			rows = append(rows, surfaceRow{kind: surfaceRuleRow})
		case surfaceGapBlock:
			// Collapse repeated gaps and never start with one.
			if len(rows) > 0 && !(rows[len(rows)-1].kind == surfaceTextRow && rows[len(rows)-1].text == "") {
				rows = append(rows, surfaceRow{kind: surfaceTextRow})
			}
		case surfacePairBlock:
			label, value, note := safe(singleLine(b.label)), safe(b.value), safe(singleLine(b.note))
			switch {
			case note != "" && !strings.Contains(value, "\n") && surfacePairInline(width, label, value) && panelPairFits(width, label, value+" · "+note):
				// The value alone decides inline placement; its note only
				// needs room beside it.
				rows = append(rows, surfaceRow{kind: surfacePairRow, text: label, value: value, ink: b.ink, note: note})
			case !strings.Contains(value, "\n") && surfacePairInline(width, label, value):
				rows = append(rows, surfaceRow{kind: surfacePairRow, text: label, value: value, ink: b.ink})
				if note != "" {
					// The note continues the value: right-aligned beneath it.
					rows = append(rows, surfaceRow{kind: surfacePairRow, value: note, ink: p.muted})
				}
			default:
				long(label, value)
				if note != "" {
					for _, line := range wrapCells(note, width) {
						rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: p.muted})
					}
				}
			}
		case surfaceLongBlock:
			long(b.label, b.value)
		case surfaceStatusBlock:
			state := safe(singleLine(b.value))
			sw := ansi.StringWidth(state)
			gw := 0
			if b.glyph != "" {
				gw = ansi.StringWidth(b.glyph) + 1
			}
			first := width - gw - sw - 2
			below := ""
			if first < 8 {
				// Too narrow for the state beside the title: the state moves to
				// its own muted row below the title.
				below, state, first = state, "", width-gw
			}
			lines := wrapCells(singleLine(b.label), max(1, first))
			if len(lines) > 1 {
				// Rewrap continuation at the full indented width.
				rest := strings.TrimSpace(strings.TrimPrefix(safe(singleLine(b.label)), lines[0]))
				lines = append(lines[:1], wrapCells(rest, max(1, width-gw))...)
			}
			rows = append(rows, surfaceRow{kind: surfaceStatusRow, text: lines[0], value: state, glyph: b.glyph, ink: b.ink, bold: b.bold})
			for _, line := range lines[1:] {
				rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: p.text, indent: gw, bold: b.bold})
			}
			if below != "" {
				for _, line := range wrapCells(below, max(1, width-gw)) {
					ink := p.muted
					if b.glyph != "" && b.ink != "" {
						ink = b.ink
					}
					rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: ink, indent: gw})
				}
			}
		case surfaceRawBlock:
			for _, line := range wrapCells(b.value, width) {
				rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: p.text})
			}
		default:
			for _, line := range wrapCells(b.value, width) {
				rows = append(rows, surfaceRow{kind: surfaceTextRow, text: line, ink: b.ink})
			}
		}
	}
	for len(rows) > 0 && rows[len(rows)-1].kind == surfaceTextRow && rows[len(rows)-1].text == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}

func (m *Model) paintSurfaceRow(f *frame, x, y, width int, row surfaceRow) {
	p := m.colors()
	bg := p.panel
	switch row.kind {
	case surfaceHeadingRow:
		vw := ansi.StringWidth(row.value)
		panelSectionHeadingOn(f, m, x, y, width, row.text, bg)
		if vw > 0 && vw+2 < width {
			f.text(x+width-vw, y, vw, row.value, p.muted, bg)
		}
	case surfaceRuleRow:
		panelRuleOn(f, m, x, y, width, bg)
	case surfacePairRow:
		ink := row.ink
		if ink == "" {
			ink = p.text
		}
		if row.note == "" {
			panelPairRowStyled(f, x, y, width, row.text, row.value, p.muted, ink, bg)
			break
		}
		suffix := " · " + row.note
		panelPairRowStyled(f, x, y, width, row.text, row.value+suffix, p.muted, ink, bg)
		sw := ansi.StringWidth(suffix)
		f.text(x+width-sw, y, sw, suffix, p.muted, bg)
	case surfaceStatusRow:
		gw := 0
		f.text(x, y, width, "", p.text, bg)
		if row.glyph != "" {
			gw = ansi.StringWidth(row.glyph) + 1
			f.text(x, y, gw-1, row.glyph, row.ink, bg)
		}
		vw := ansi.StringWidth(row.value)
		v := componentVisual{foreground: p.text, background: bg, bold: row.bold}
		room := width - gw
		if vw > 0 {
			room -= vw + 2
		}
		f.componentText(x+gw, y, max(0, room), row.text, v)
		if vw > 0 {
			// The state word shares its mark's semantic ink; unmarked
			// states stay muted.
			ink := p.muted
			if row.glyph != "" && row.ink != "" {
				ink = row.ink
			}
			f.text(x+width-vw, y, vw, row.value, ink, bg)
		}
	default:
		ink := row.ink
		if ink == "" {
			ink = p.text
		}
		f.text(x, y, width, "", ink, bg)
		f.componentText(x+row.indent, y, max(0, width-row.indent), row.text, componentVisual{foreground: ink, background: bg, bold: row.bold})
	}
}

// chooserTileColumns picks the empty host's tile grid: three columns when
// wide, otherwise two, or zero when bands are unsupported, a label does not
// fit its tile or the host is too short for the grid.
func (m *Model) chooserTileColumns(r shell.Rect, w int, labels []string) int {
	if !panelBandsSupported(m) {
		return 0
	}
	for _, cols := range []int{3, 2} {
		if cols == 3 && w < 54 {
			continue
		}
		rows := (len(labels) + cols - 1) / cols
		// Heading, gap, then three-row bands separated by one blank row, with
		// the host's top and bottom margins.
		need := 2 + rows*3 + rows - 1 + 2
		if r.H < need {
			continue
		}
		fit := true
		for i := 0; i < len(labels); i += cols {
			if !panelSegmentsFit(w, labels[i:min(len(labels), i+cols)]) {
				fit = false
			}
		}
		// Every row shares the full row's cell width, so check a full row.
		if fit && panelSegmentsFit(w, padLabels(labels, cols)) {
			return cols
		}
	}
	return 0
}

// padLabels returns the widest labels spread across one full row of cols.
func padLabels(labels []string, cols int) []string {
	widest := ""
	for _, l := range labels {
		if ansi.StringWidth(l) > ansi.StringWidth(widest) {
			widest = l
		}
	}
	out := make([]string, cols)
	for i := range out {
		out[i] = widest
	}
	return out
}

// renderChooserTiles paints the empty host's chooser as a grid of banded
// square-fill tiles, each an icon and name, keeping the `chooser:<kind>` hit
// keys and open actions of the list fallback. It reports false, painting
// nothing, when the grid cannot be used.
func (m *Model) renderChooserTiles(f *frame, r shell.Rect, x, y, w int, kinds []string) bool {
	p := m.colors()
	labels := make([]string, len(kinds))
	for i, kind := range kinds {
		labels[i] = m.icon(kind) + "  " + title(kind)
	}
	cols := m.chooserTileColumns(r, w, labels)
	if cols == 0 {
		return false
	}
	panelSectionHeadingOn(f, m, x, y, w, "Add surface", p.panel)
	xs, ws := panelSegmentLayout(x, w, cols)
	for i, kind := range kinds {
		top := y + 2 + (i/cols)*4
		tx, tw := xs[i%cols], ws[i%cols]
		key := "chooser:" + kind
		v := m.componentStyle(squareFill, m.controlState(false, key), p.text, p.input)
		v.base = p.panel
		for band := -1; band <= 1; band++ {
			row := top + band + 1
			if f.paintPanelBandEdge(tx, row, tw, band, 3, v) {
				continue
			}
			label := centered(labels[i], tw)
			f.componentText(tx, row, tw, label, v)
			// The icon keeps the accent; the name takes the tile's ink.
			iconX := tx + ansi.StringWidth(label) - ansi.StringWidth(strings.TrimLeft(label, " "))
			icon := m.icon(kind)
			icv := v
			icv.foreground = p.blue
			f.componentText(iconX, row, ansi.StringWidth(icon), icon, icv)
			if v.focused {
				f.focusMark(tx-1, row, v, v.base)
			}
		}
		f.hits = append(f.hits, hit{Rect: shell.Rect{X: tx, Y: top, W: tw, H: 3}, Action: action{Kind: "open", Value: kind}, Label: labels[i], Key: key})
	}
	return true
}
