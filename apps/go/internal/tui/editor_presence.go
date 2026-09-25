package tui

import (
	"bytes"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// editor_presence.go shares cursors between simultaneous editors (slice C).
// While this client edits a document it publishes its selection anchor and
// cursor as ygo RelativePosition encodings (so they stay on the same text as
// the document changes): when they move, at most one send per
// docPresenceMin, a keepalive every docPresenceKeepalive while they rest (the
// server forgets presence after protocol.DocumentPresenceTimeout), and a
// clear when edit mode ends. Other streams' cursors arrive as presence
// events; their encoded positions are kept and resolved against this replica
// on every paint (so after every applied update), and a position that does
// not resolve yet (its edit has not arrived) hides that cursor until it
// does. They never move this client's own cursor, selection or scroll.

const (
	docPresenceMin       = time.Duration(protocol.DocumentPresenceInterval) * time.Millisecond
	docPresenceKeepalive = 10 * time.Second
	// docPeerStale drops a peer that has sent nothing for longer than the
	// server keeps it (the server's Removed normally comes first).
	docPeerStale = time.Duration(protocol.DocumentPresenceTimeout+10) * time.Second
)

// docPresence is this client's last published cursor.
type docPresence struct {
	active bool // a cursor is published (not cleared)
	// shown: the server may be showing a cursor of ours (a non-nil
	// presence was queued since the last clear), so leaving must clear it.
	shown bool
	// slot is the latest presence waiting for room in the writer queue
	// (latest wins; a clear replaces a move and is never dropped).
	slot           *docPresenceSend
	cur, anchor    edPos
	anchorB, headB []byte
	sentAt         time.Time
	due, ticking   bool // a throttled send / the keepalive tick is scheduled
}

// docPeer is another stream's cursor.
type docPeer struct {
	client       string
	color        int
	anchor, head crdt.RelativePosition
	selection    bool
	seen         time.Time
}

type docPresenceSend struct{ anchor, head []byte }

type docPresenceMsg struct {
	id        string
	gen       uint64
	keepalive bool
}

// presenceNow is the clock for presence (the editor's, injectable).
func (s *docSession) presenceNow() time.Time { return s.ed.clock() }

// syncPresence publishes, refreshes or clears this client's cursor.
func (m *Model) syncPresence(s *docSession) tea.Cmd {
	m.pruneStalePeers(s)
	if s.writes == nil || !s.connected || s.rep == nil {
		return nil
	}
	m.flushPresence(s)
	now := s.presenceNow()
	// Only servers reporting simultaneous editing take presence.
	editing := m.docEdit == s.id && s.editable(m.clientID) && s.status.Collaborative
	if !editing {
		if s.pres.shown || s.pres.slot != nil && s.pres.slot.head != nil {
			slot := s.pres.slot
			s.pres = docPresence{slot: slot}
			m.queuePresence(s, nil, nil)
		}
		return nil
	}
	moved := !s.pres.active || s.ed.cur != s.pres.cur || s.ed.anchor != s.pres.anchor
	elapsed := now.Sub(s.pres.sentAt)
	var cmds []tea.Cmd
	switch {
	case !moved && elapsed < docPresenceKeepalive:
	case elapsed < docPresenceMin:
		if !s.pres.due {
			s.pres.due = true
			id, gen := s.id, s.gen
			cmds = append(cmds, tea.Tick(docPresenceMin-elapsed, func(time.Time) tea.Msg { return docPresenceMsg{id: id, gen: gen} }))
		}
	default:
		t := s.rep.txt
		enc := func(p edPos) []byte {
			return crdt.EncodeRelativePosition(crdt.CreateRelativePositionFromIndex(s.rep.text, t.offset(p), 0))
		}
		a, h := enc(s.ed.anchor), enc(s.ed.cur)
		s.pres.cur, s.pres.anchor = s.ed.cur, s.ed.anchor
		if !s.pres.active || moved && (!bytes.Equal(a, s.pres.anchorB) || !bytes.Equal(h, s.pres.headB)) || elapsed >= docPresenceKeepalive {
			s.pres.active, s.pres.anchorB, s.pres.headB, s.pres.sentAt = true, a, h, now
			cmds = append(cmds, m.queuePresence(s, a, h))
		}
	}
	if s.pres.active && !s.pres.ticking {
		s.pres.ticking = true
		id, gen := s.id, s.gen
		wait := max(docPresenceMin, docPresenceKeepalive-now.Sub(s.pres.sentAt))
		cmds = append(cmds, tea.Tick(wait, func(time.Time) tea.Msg { return docPresenceMsg{id: id, gen: gen, keepalive: true} }))
	}
	return tea.Batch(cmds...)
}

// queuePresence sends a presence (nil, nil clears) on the stream's writer,
// or keeps it as the latest waiting one when the queue is full.
func (m *Model) queuePresence(s *docSession, anchor, head []byte) tea.Cmd {
	s.pres.slot = &docPresenceSend{anchor, head}
	s.pres.shown = s.pres.shown || head != nil
	m.flushPresence(s)
	return nil
}

// flushPresence hands the waiting presence to the writer if it has room;
// it runs on every update, so it is retried as the queue drains.
func (m *Model) flushPresence(s *docSession) {
	p := s.pres.slot
	if p == nil || s.writes == nil {
		return
	}
	select {
	case s.writes <- func(c docConn) error { return c.SendPresence(p.anchor, p.head) }:
		s.pres.slot = nil
		if p.head == nil {
			s.pres.shown = false
		}
	default:
	}
}

// pruneStalePeers drops peers silent for longer than the server keeps them.
func (m *Model) pruneStalePeers(s *docSession) {
	now := s.presenceNow()
	for k, p := range s.peers {
		if now.Sub(p.seen) > docPeerStale {
			delete(s.peers, k)
			m.markDirty()
		}
	}
}

// acceptPresence applies a presence event.
func (m *Model) acceptPresence(s *docSession, peers []protocol.DocumentPeer) {
	if s.peers == nil {
		s.peers = map[string]*docPeer{}
	}
	now := s.presenceNow()
	for _, p := range peers {
		if p.Removed || p.Head == nil {
			delete(s.peers, p.Peer)
			continue
		}
		head, err := crdt.DecodeRelativePosition(p.Head)
		if err != nil {
			delete(s.peers, p.Peer)
			continue
		}
		dp := &docPeer{client: p.Client, color: p.Color, head: head, anchor: head, seen: now}
		if a, err := crdt.DecodeRelativePosition(p.Anchor); err == nil && p.Anchor != nil {
			dp.anchor, dp.selection = a, !bytes.Equal(p.Anchor, p.Head)
		}
		s.peers[p.Peer] = dp
	}
	m.markDirty()
}

// peerMark is a peer cursor resolved against the current text.
type peerMark struct {
	client     string
	color      int
	head, a, z edPos
	selection  bool
}

// livePeers resolves the peers' cursors, dropping stale ones, in a stable
// order.
func (m *Model) livePeers(s *docSession) []peerMark {
	if s.rep == nil || len(s.peers) == 0 {
		return nil
	}
	now := s.presenceNow()
	t := s.rep.txt
	resolve := func(rp crdt.RelativePosition) (edPos, bool) {
		abs, ok := crdt.ToAbsolutePosition(s.rep.doc, rp)
		if !ok {
			return edPos{}, false
		}
		p := t.posAt(min(max(0, abs.Index), t.Len()))
		p.col = snapGrapheme(t.lines[p.line], p.col)
		return p, true
	}
	keys := make([]string, 0, len(s.peers))
	for k, p := range s.peers {
		// Stale peers are pruned in Update; painting only skips them.
		if now.Sub(p.seen) <= docPeerStale {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []peerMark
	for _, k := range keys {
		p := s.peers[k]
		head, ok := resolve(p.head)
		if !ok {
			continue
		}
		mk := peerMark{client: p.client, color: p.color, head: head, a: head, z: head}
		if p.selection {
			if a, ok := resolve(p.anchor); ok && a != head {
				mk.a, mk.z, mk.selection = a, head, true
				if head.less(a) {
					mk.a, mk.z = head, a
				}
			}
		}
		out = append(out, mk)
	}
	return out
}

// peerColor maps a server color index to a theme accent.
func (m *Model) peerColor(i int) string {
	p := m.colors()
	accents := []string{p.blue, p.violet, p.cyan, p.pink, p.gold, p.green, p.red, p.muted}
	return accents[((i%len(accents))+len(accents))%len(accents)]
}

// peerTint is the selection fill for a peer color: the accent mixed into
// the panel in true color, else the theme's selection fill.
func (m *Model) peerTint(i int) string {
	p := m.colors()
	a, b := parseHex(m.peerColor(i)), parseHex(p.panel)
	if a == nil || b == nil {
		return p.selected
	}
	mix := func(x, y int) int { return (x*35 + y*65) / 100 }
	return "#" + hex2(mix(a[0], b[0])) + hex2(mix(a[1], b[1])) + hex2(mix(a[2], b[2]))
}

func hex2(v int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4&15], digits[v&15]})
}

// shortClient is a peer's short label.
func shortClient(id string) string {
	id = safe(singleLine(id))
	if lineWidth(id) <= 8 {
		return id
	}
	end := 0
	walkCells(id, func(c edCell) bool {
		if c.col+c.w > 7 {
			return false
		}
		end = c.e
		return true
	})
	return id[:end] + "…"
}
