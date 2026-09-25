package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// editor_kept.go holds text the server did not store ("kept copies"): per
// document session, per Files buffer, and model-wide for copies whose file
// view or session no longer exists ("orphaned"). Copies are never dropped
// silently: moving them keeps them, only an explicit Dismiss or Close
// anyway removes them, and eviction past the bound is announced. The guards
// count every list.

// docMaxLost bounds each list of kept copies.
const docMaxLost = 20

// keepLost appends a copy, announcing when the oldest one is evicted.
func (m *Model) keepLost(list *[]docLost, l docLost) tea.Cmd {
	*list = append(*list, l)
	if len(*list) <= docMaxLost {
		return nil
	}
	*list = (*list)[len(*list)-docMaxLost:]
	return m.showNoticeAs(noticeUnavailable, fmt.Sprintf("Oldest kept copy dropped · %d kept", docMaxLost))
}

// orphanLost moves copies whose file view or session is going away to the
// model-wide list shown in the Files surface.
func (m *Model) orphanLost(ls []docLost) tea.Cmd {
	if len(ls) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for _, l := range ls {
		cmds = append(cmds, m.keepLost(&m.docOrphans, l))
	}
	m.markDirty()
	cmds = append(cmds, m.showNoticeAs(noticeUnavailable, fmt.Sprintf("Kept %d unsaved %s from a closed file · Files lists them", len(ls), docPlural(len(ls), "text", "texts"))))
	return tea.Batch(cmds...)
}

// orphanView moves a Files view's kept copies (and those of documents only
// it shows) to the model-wide list before the view is pruned.
func (m *Model) orphanView(v *filesView) tea.Cmd {
	var cmds []tea.Cmd
	for _, b := range v.buffers {
		cmds = append(cmds, m.orphanLost(b.docLost))
		b.docLost = nil
	}
	return tea.Batch(cmds...)
}

// keptSource resolves a kept-copies source: "orphans", or "buffer" for the
// active buffer and its session. The lists are ordered oldest first.
func (m *Model) keptSource(source string) []*[]docLost {
	if source == "orphans" {
		return []*[]docLost{&m.docOrphans}
	}
	v := m.currentFilesView()
	if v == nil || v.buffer() == nil {
		return nil
	}
	b := v.buffer()
	out := []*[]docLost{&b.docLost}
	if s := m.bufferDoc(b); s != nil {
		out = append(out, &s.lost)
	}
	return out
}

// keptAt returns the list and index of the i-th copy of a source.
func keptAt(lists []*[]docLost, i int) (*[]docLost, int, bool) {
	for _, l := range lists {
		if i < len(*l) {
			return l, i, true
		}
		i -= len(*l)
	}
	return nil, 0, false
}

func keptAll(lists []*[]docLost) []docLost {
	var out []docLost
	for _, l := range lists {
		out = append(out, *l...)
	}
	return out
}

func keptLabel(l docLost) string {
	lines := len(splitDocLines(l.text))
	label := fmt.Sprintf("%d %s", lines, docPlural(lines, "line", "lines"))
	if l.path != "" {
		label = safe(singleLine(l.path)) + " · " + label
	}
	return label + " · " + safe(singleLine(l.reason))
}

// showKept lists every copy of a source with Copy and Dismiss each.
func (m *Model) showKept(source string) {
	all := keptAll(m.keptSource(source))
	if len(all) == 0 {
		m.menu = nil
		return
	}
	items := []menuItem{{Note: fmt.Sprintf("%d unsaved %s the server did not store, newest first.", len(all), docPlural(len(all), "text", "texts"))}}
	for i := len(all) - 1; i >= 0; i-- {
		label := keptLabel(all[i])
		items = append(items,
			menuItem{Label: "Copy · " + label, Action: action{Kind: "doc-kept-copy", Value: source, Index: i}},
			menuItem{Label: "Dismiss · " + label, Action: action{Kind: "doc-kept-dismiss", Value: source, Index: i}})
	}
	items = append(items, menuItem{Label: "Copy all", Action: action{Kind: "doc-kept-copy-all", Value: source}})
	m.showMenu("Kept texts", items)
	m.menuIndex = 1
}

// keptText joins copies for Copy all, each under a header line.
func keptText(all []docLost) string {
	var b strings.Builder
	for i, l := range all {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "--- %s ---\n%s", keptLabel(l), l.text)
	}
	return b.String()
}

// keptAction handles the kept-copies menu actions.
func (m *Model) keptAction(a action) tea.Cmd {
	lists := m.keptSource(a.Value)
	switch a.Kind {
	case "doc-kept":
		m.showKept(a.Value)
	case "doc-kept-copy":
		if l, i, ok := keptAt(lists, a.Index); ok {
			return m.copyText(clipboardSafeText((*l)[i].text))
		}
	case "doc-kept-copy-all":
		if all := keptAll(lists); len(all) > 0 {
			return m.copyText(clipboardSafeText(keptText(all)))
		}
	case "doc-kept-dismiss":
		if l, i, ok := keptAt(lists, a.Index); ok {
			*l = append((*l)[:i:i], (*l)[i+1:]...)
			m.markDirty()
			if len(keptAll(lists)) > 0 {
				m.showKept(a.Value)
			}
		}
	}
	return nil
}

// docUnstoredFor counts unstored text in the Files views whose keys match.
func (m *Model) docUnstoredFor(match func(key string) bool) int {
	n := 0
	seen := map[string]bool{}
	for key, v := range m.filesViews {
		if !match(key) {
			continue
		}
		for _, b := range v.buffers {
			n += len(b.docLost)
			if s := m.bufferDoc(b); s != nil && !seen[s.id] {
				seen[s.id] = true
				n += len(s.pending) + len(s.lost)
				if s.recovering() {
					n++
				}
			}
		}
	}
	return n
}

// docDeleteNote adds a note to an open delete/remove confirmation when the
// affected Files views hold unstored document text.
func (m *Model) docDeleteNote(match func(key string) bool) {
	if n := m.docUnstoredFor(match); n > 0 && len(m.menu) > 0 {
		m.menu = append(m.menu, menuItem{Note: fmt.Sprintf("%d unsaved document %s · kept as copies in Files", n, docPlural(n, "change", "changes"))})
	}
}
