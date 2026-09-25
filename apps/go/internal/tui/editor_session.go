package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/doc"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/reearth/ygo/crdt"
)

// editor_session.go connects open Files buffers to server-owned shared
// documents (ADR 0022; protocol/document.go, client/document.go). A text
// buffer whose file the server can edit opens its document with
// document.open; one session per document ID holds the stream, this
// client's replica and the edits the server has not acknowledged yet.
//
// Every edit is one replica update sent with a client-chosen op ID and the
// current EditGen. It stays pending until its ack, which the server sends
// only after the update is durable; pending updates are resent unchanged
// (same op ID and bytes) after a reconnect or an "unavailable" refusal, which
// the server treats as duplicates. A refusal that requires a resync discards
// the replica and rebuilds it from a fresh state under a new replica ID; the
// unacknowledged text is kept as a recoverable local copy, never merged back
// silently. Update never blocks on stream I/O: dials and reads are tea.Cmds
// tagged with the stream generation, writes go through a per-stream writer
// goroutine.

const (
	docCapability = "shared-documents"
	docQueue      = 512 // queued writes per stream
	docRetryMin   = 250 * time.Millisecond
	docRetryMax   = 5 * time.Second
	// docMaxPending bounds unacknowledged edits; typing past it is refused
	// until the server catches up.
	docMaxPending      = 2000
	docMaxPendingBytes = 32 << 20
)

// docResendWait pauses sending after an unavailable refusal; a status
// showing recovered storage resends sooner.
var docResendWait = 2 * time.Second

// docConn is one document stream (client.DocumentStream in production).
type docConn interface {
	Events() <-chan protocol.DocumentEvent
	Err() error
	SendUpdate(op string, gen int64, update []byte) error
	Sync(sv []byte) error
	Close() error
}

// docDialer opens a stream to document id as clientID with replica ID
// replica; ctx bounds the stream's lifetime.
type docDialer func(ctx context.Context, id, clientID string, replica uint64) (docConn, error)

// documentAPI is the command and versions half of the server client; tests
// inject a fake.
type documentAPI interface {
	Command(ctx context.Context, c protocol.Command) (protocol.Receipt, error)
	DocumentVersions(ctx context.Context, id string) (protocol.DocumentVersions, error)
}

var _ documentAPI = (*client.Client)(nil)

// docOp is one local update awaiting its durable ack. sent is per stream.
type docOp struct {
	id     string
	update []byte
	sent   bool
}

// docLost is text whose updates the server refused; it is kept for the
// user to copy, never merged back automatically.
type docLost struct {
	text, reason, path string
}

// docSession is this client's state for one open document.
type docSession struct {
	id, path string
	status   protocol.DocumentStatus
	// live marks status taken from the stream (exact) rather than the
	// coalesced snapshot.
	live bool

	gen       uint64
	conn      docConn
	writes    chan func(docConn) error
	dialing   bool
	retry     bool
	backoff   time.Duration
	connected bool // the current stream delivered its state
	// gone is why the document no longer exists here ("closed",
	// "not_found"); a gone session never redials.
	gone string

	replicaID uint64
	rep       *docReplica
	rev       int64
	pending   []docOp
	nonce     string
	opSeq     uint64
	// resending: an unavailable refusal paused sending until a resend.
	resending bool
	// lost holds refused texts kept for the user (newest last, bounded).
	lost []docLost
	// oldRep is the discarded replica whose unacknowledged edits are
	// re-applied to the rebuilt one; draftText is the visible text kept
	// instead when the replica itself failed; draftReason says why.
	// draftView paints draftText while recovering.
	draftText string
	draftBase string
	hasDraft  bool
	// draftPending is how many unacknowledged edits a failed replica held.
	draftPending int
	draftView    *edText
	// reapplying marks pending ops that re-applied such a draft: a second
	// refusal keeps the text as a copy instead of looping.
	oldRep      *docReplica
	draftReason string
	reapplying  bool

	ed docEditor
	// wantEdit: an edit or take-edit was accepted and edit mode starts once
	// the status names this client as the editor.
	wantEdit bool
	editing  *protocol.Command // edit/take-edit in flight
}

func (s *docSession) pendingBytes() int {
	n := 0
	for _, op := range s.pending {
		n += len(op.update)
	}
	return n
}

// recovering reports that unacknowledged edits are held as a draft while a
// fresh replica is being fetched.
func (s *docSession) recovering() bool {
	return s != nil && s.rep == nil && s.gone == "" && (s.oldRep != nil || s.hasDraft)
}

// unstoredText returns text this session holds that the server has not
// stored: the draft while recovering, else the replica with pending edits.
func (s *docSession) unstoredText() (string, bool) {
	switch {
	case s.oldRep != nil:
		return s.oldRep.txt.String(), true
	case s.hasDraft:
		return s.draftText, true
	case s.rep != nil && len(s.pending) > 0:
		return s.rep.txt.String(), true
	}
	return "", false
}

// viewText is the text painted for the session: the replica's, or the
// draft's while recovering.
func (s *docSession) viewText() *edText {
	switch {
	case s.rep != nil:
		return s.rep.txt
	case s.oldRep != nil:
		return s.oldRep.txt
	case s.hasDraft:
		if s.draftView == nil {
			s.draftView = newEdText(s.draftText)
		}
		return s.draftView
	}
	return nil
}

// editable reports whether this client may type into the document now.
func (s *docSession) editable(clientID string) bool {
	return s != nil && s.rep != nil && s.gone == "" && s.status.Editor == clientID && s.status.State != protocol.DocumentStateReadOnly
}

// otherEditor reports that another client holds the editor role.
func (s *docSession) otherEditor(clientID string) bool {
	return s.status.Editor != "" && s.status.Editor != clientID
}

type (
	docCmdMsg struct {
		cmd     protocol.Command
		receipt protocol.Receipt
		err     error
		// key and path locate the buffer that sent a document.open.
		key, path string
	}
	docDialedMsg struct {
		id   string
		gen  uint64
		conn docConn
		err  error
	}
	docEventMsg struct {
		id  string
		gen uint64
		ev  protocol.DocumentEvent
	}
	docEndMsg struct {
		id  string
		gen uint64
		err error
	}
	docRetryMsg struct {
		id  string
		gen uint64
	}
	docResendMsg struct {
		id  string
		gen uint64
	}
)

func (m *Model) docsAvailable() bool {
	return slices.Contains(m.snapshot.Capabilities, docCapability) && m.docAPI() != nil
}

func (m *Model) docAPI() documentAPI {
	if m.docAPIs != nil {
		return m.docAPIs
	}
	if m.client != nil {
		return m.client
	}
	return nil
}

func (m *Model) docDialer() docDialer {
	if m.docDial != nil {
		return m.docDial
	}
	c := m.client
	if c == nil {
		return nil
	}
	return func(ctx context.Context, id, clientID string, replica uint64) (docConn, error) {
		s, err := c.OpenDocumentStream(ctx, id, clientID, replica)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
}

// bufferDoc returns the session of a buffer's document.
func (m *Model) bufferDoc(b *fileBuffer) *docSession {
	if b == nil || b.doc == "" {
		return nil
	}
	return m.docs[b.doc]
}

// sendDocCommand runs one document command; retries reuse its ID.
func (m *Model) sendDocCommand(c protocol.Command, key, path string) tea.Cmd {
	api := m.docAPI()
	if api == nil {
		return nil
	}
	ctx := m.filesContext()
	return func() tea.Msg {
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r, err := api.Command(deadline, c)
		return docCmdMsg{cmd: c, receipt: r, err: err, key: key, path: path}
	}
}

func (m *Model) docCommand(kind, id string) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: identity(), Kind: kind, TargetID: id, ClientID: m.clientID}
}

// maybeOpenDocument opens the shared document behind a text buffer once its
// read shows editable-looking text; the server decides editability.
func (m *Model) maybeOpenDocument(key string, target client.GitTarget, b *fileBuffer) tea.Cmd {
	if !m.docsAvailable() || b.read == nil || b.read.Kind != protocol.FileReadText || b.read.Truncated ||
		b.doc != "" || b.docOpen != nil || b.docRO != "" || m.filesFixture() {
		return nil
	}
	c := protocol.Command{Version: protocol.Version, ID: identity(), Kind: protocol.DocumentKindOpen, Path: b.path,
		ClientID: m.clientID, ThreadID: target.ThreadID, ProjectID: target.ProjectID}
	b.docOpen, b.docErr = &c, ""
	return m.sendDocCommand(c, key, b.path)
}

// acceptDocCommand applies a document command's reply.
func (m *Model) acceptDocCommand(msg docCmdMsg) tea.Cmd {
	var pe *protocol.Error
	rejected := errors.As(msg.err, &pe)
	if msg.cmd.Kind == protocol.DocumentKindOpen {
		return m.acceptDocOpen(msg, pe)
	}
	s := m.docs[msg.cmd.TargetID]
	switch msg.cmd.Kind {
	case protocol.DocumentKindEdit, protocol.DocumentKindTakeEdit:
		if s == nil || s.editing == nil || s.editing.ID != msg.cmd.ID {
			return nil
		}
		s.editing = nil
		switch {
		case rejected && pe.Code == "editor_busy":
			s.wantEdit = false
			return m.showNoticeAs(noticeUnavailable, protocol.DocumentSimultaneousUnavailable+" · another client is editing · Take over")
		case msg.err != nil:
			s.wantEdit = false
			return m.showNoticeAs(noticeError, "Editing unavailable · "+safe(singleLine(msg.err.Error())))
		}
		return m.enterWantedEdit(s)
	case protocol.DocumentKindResolve:
		return m.acceptDocResolve(msg, pe)
	case protocol.DocumentKindDismiss:
		if msg.err != nil {
			return m.showNoticeAs(noticeError, "Retained edits not deleted · "+safe(singleLine(msg.err.Error())))
		}
		return m.showNoticeAs(noticeDone, "Deleted the retained edits")
	case protocol.DocumentKindClose:
		// Closing is best effort: an opener left behind only keeps the
		// document loaded until it saves.
		return nil
	}
	return nil
}

func (m *Model) acceptDocOpen(msg docCmdMsg, pe *protocol.Error) tea.Cmd {
	var b *fileBuffer
	if v := m.filesViews[msg.key]; v != nil {
		for _, buf := range v.buffers {
			if buf.path == msg.path && buf.docOpen != nil && buf.docOpen.ID == msg.cmd.ID {
				b = buf
			}
		}
	}
	if b == nil {
		// The buffer closed while opening: do not stay an opener.
		if msg.err == nil && msg.receipt.TargetID != "" && m.docs[msg.receipt.TargetID] == nil {
			return m.sendDocCommand(m.docCommand(protocol.DocumentKindClose, msg.receipt.TargetID), "", "")
		}
		return nil
	}
	m.markDirty()
	switch {
	case pe != nil && pe.Code == "document_read_only":
		b.docOpen, b.docRO = nil, safe(singleLine(pe.Message))
		if b.docRO == "" {
			b.docRO = "This file cannot be edited"
		}
		return nil
	case pe != nil:
		// Refused before anything ran: a retry is a new command.
		b.docOpen, b.docErr = nil, safe(singleLine(pe.Message))
		return nil
	case msg.err != nil:
		// No reply: keep the command so Retry resends the same ID.
		b.docErr = safe(singleLine(msg.err.Error()))
		return nil
	}
	b.docOpen, b.docErr = nil, ""
	b.doc = msg.receipt.TargetID
	if m.docs == nil {
		m.docs = map[string]*docSession{}
	}
	if m.docs[b.doc] == nil {
		m.docs[b.doc] = &docSession{id: b.doc, path: b.path, nonce: identity()[:12], replicaID: newReplicaID()}
		for _, st := range m.snapshot.Documents {
			if st.ID == b.doc {
				m.docs[b.doc].status = st
			}
		}
	}
	return nil
}

// retryDocOpen resends a buffer's failed open (the same command when its
// reply was lost).
func (m *Model) retryDocOpen(key string, b *fileBuffer) tea.Cmd {
	if b.docOpen != nil {
		b.docErr = ""
		return m.sendDocCommand(*b.docOpen, key, b.path)
	}
	b.docErr = ""
	_, target := m.filesTarget()
	return m.maybeOpenDocument(key, target, b)
}

// docRefs lists the document IDs open in any buffer.
func (m *Model) docRefs() map[string]int {
	refs := map[string]int{}
	for _, v := range m.filesViews {
		for _, b := range v.buffers {
			if b.doc != "" {
				refs[b.doc]++
			}
		}
	}
	return refs
}

// syncDocuments runs after every update: it releases sessions no buffer
// uses (once nothing is pending), dials the streams of the others and drops
// edit mode that no longer applies.
func (m *Model) syncDocuments() tea.Cmd {
	if len(m.docs) == 0 {
		m.docEdit = ""
		return nil
	}
	refs := m.docRefs()
	var cmds []tea.Cmd
	for id, s := range m.docs {
		if s.gone == "" && s.status.Quarantined {
			cmds = append(cmds, m.docGone(s, "quarantined", "The stored document could not be loaded"))
		}
		if refs[id] == 0 && (s.gone != "" || len(s.pending) == 0 && !s.recovering()) {
			cmds = append(cmds, m.releaseDoc(s))
			continue
		}
		if s.gone == "" && s.conn == nil && !s.dialing && !s.retry && m.connected {
			cmds = append(cmds, m.dialDoc(s))
		}
	}
	m.docInput()
	return tea.Batch(cmds...)
}

// releaseDoc closes a session: its stream, replica and server opener.
func (m *Model) releaseDoc(s *docSession) tea.Cmd {
	// Kept copies outlive the session.
	orphaned := m.orphanLost(s.lost)
	s.lost = nil
	m.closeDocStream(s)
	s.rep.close()
	s.rep = nil
	delete(m.docs, s.id)
	if m.docEdit == s.id {
		m.docEdit = ""
	}
	if s.gone != "" {
		return orphaned
	}
	return tea.Batch(orphaned, m.sendDocCommand(m.docCommand(protocol.DocumentKindClose, s.id), "", ""))
}

func (m *Model) dialDoc(s *docSession) tea.Cmd {
	dial := m.docDialer()
	if dial == nil {
		return nil
	}
	m.docSeq++
	s.gen, s.dialing, s.connected = m.docSeq, true, false
	id, gen, ctx, clientID, replica := s.id, s.gen, m.filesContext(), m.clientID, s.replicaID
	return func() tea.Msg {
		conn, err := dial(ctx, id, clientID, replica)
		return docDialedMsg{id: id, gen: gen, conn: conn, err: err}
	}
}

func readDoc(id string, gen uint64, conn docConn) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-conn.Events()
		if !ok {
			return docEndMsg{id: id, gen: gen, err: conn.Err()}
		}
		return docEventMsg{id: id, gen: gen, ev: ev}
	}
}

// closeDocStream closes s's stream; later messages from it are stale.
func (m *Model) closeDocStream(s *docSession) {
	m.docSeq++
	s.gen = m.docSeq
	s.dialing, s.retry, s.connected, s.resending = false, false, false, false
	if s.conn != nil {
		conn, writes := s.conn, s.writes
		s.conn, s.writes = nil, nil
		go func() {
			_ = conn.Close()
			close(writes)
		}()
	}
	for i := range s.pending {
		s.pending[i].sent = false
	}
}

func startDocWriter(conn docConn) chan func(docConn) error {
	writes := make(chan func(docConn) error, docQueue)
	go func() {
		// A failed write ends the stream, which reconnects and resends.
		for write := range writes {
			_ = write(conn)
		}
	}()
	return writes
}

// sendPending queues every pending op not yet sent on this stream, in order.
func (m *Model) sendPending(s *docSession) tea.Cmd {
	if !s.connected || s.writes == nil || s.resending {
		return nil
	}
	gen := s.status.EditGen
	for i := range s.pending {
		if s.pending[i].sent {
			continue
		}
		op := s.pending[i]
		select {
		case s.writes <- func(c docConn) error { return c.SendUpdate(op.id, gen, op.update) }:
			s.pending[i].sent = true
		default:
			// The queue is full: resend the rest shortly.
			return m.scheduleDocResend(s)
		}
	}
	return nil
}

func (m *Model) scheduleDocResend(s *docSession) tea.Cmd {
	if s.resending {
		return nil
	}
	s.resending = true
	id, gen := s.id, s.gen
	return tea.Tick(docResendWait, func(time.Time) tea.Msg { return docResendMsg{id: id, gen: gen} })
}

// queueLocal records the replica's new updates as pending ops and sends them.
func (m *Model) queueLocal(s *docSession) tea.Cmd {
	for _, u := range s.rep.takeUpdates() {
		s.opSeq++
		s.pending = append(s.pending, docOp{id: s.nonce + "-" + strconv.FormatUint(s.opSeq, 10), update: u})
	}
	m.markDirty()
	return m.sendPending(s)
}

func (m *Model) retryDoc(s *docSession) tea.Cmd {
	s.backoff = min(docRetryMax, max(docRetryMin, s.backoff*2))
	s.retry = true
	id, gen := s.id, s.gen
	return tea.Tick(s.backoff, func(time.Time) tea.Msg { return docRetryMsg{id: id, gen: gen} })
}

// acceptDocMsg routes stream messages; stale generations are dropped.
func (m *Model) acceptDocMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case docCmdMsg:
		return m.acceptDocCommand(msg), true
	case docVersionsMsg:
		return m.acceptDocVersions(msg), true
	case docPasteMsg:
		return m.acceptDocPaste(msg), true
	case docDialedMsg:
		s := m.docs[msg.id]
		if s == nil || s.gen != msg.gen || !s.dialing {
			if msg.conn != nil {
				go msg.conn.Close()
			}
			return nil, true
		}
		s.dialing = false
		if msg.err != nil {
			var pe *protocol.Error
			switch {
			case errors.Is(msg.err, client.ErrDocumentNotFound):
				return m.docGone(s, "not_found", "The document is no longer open on the server"), true
			case errors.As(msg.err, &pe) && pe.Code == "replica_conflict":
				m.docResync(s, "", false)
			}
			return m.retryDoc(s), true
		}
		s.conn, s.writes = msg.conn, startDocWriter(msg.conn)
		return readDoc(s.id, s.gen, s.conn), true
	case docEventMsg:
		s := m.docs[msg.id]
		if s == nil || s.gen != msg.gen || s.conn == nil {
			return nil, true
		}
		cmd := m.applyDocEvent(s, msg.ev)
		if s.conn == nil || s.gen != msg.gen {
			return cmd, true
		}
		return tea.Batch(cmd, readDoc(s.id, s.gen, s.conn)), true
	case docEndMsg:
		s := m.docs[msg.id]
		if s == nil || s.gen != msg.gen {
			return nil, true
		}
		m.closeDocStream(s)
		m.markDirty()
		if s.gone != "" {
			return nil, true
		}
		return m.retryDoc(s), true
	case docRetryMsg:
		if s := m.docs[msg.id]; s != nil && s.gen == msg.gen && s.retry {
			s.retry = false
			// syncDocuments redials it.
		}
		return nil, true
	case docResendMsg:
		if s := m.docs[msg.id]; s != nil && s.gen == msg.gen && s.resending {
			s.resending = false
			if s.status.State == protocol.DocumentStateFailed {
				// Storage is still failing: keep waiting for a status.
				return m.scheduleDocResend(s), true
			}
			for i := range s.pending {
				s.pending[i].sent = false
			}
			return m.sendPending(s), true
		}
		return nil, true
	}
	return nil, false
}

// applyDocEvent applies one stream event to its session.
func (m *Model) applyDocEvent(s *docSession, ev protocol.DocumentEvent) tea.Cmd {
	m.markDirty()
	switch ev.Type {
	case protocol.DocumentEventState:
		if ev.Status != nil {
			s.status, s.live = *ev.Status, true
		}
		s.backoff = 0
		var reapply tea.Cmd
		if s.rep == nil {
			rep, err := newDocReplica(s.replicaID, ev.Update)
			if err != nil {
				m.docResync(s, "", false)
				return tea.Batch(m.retryDoc(s), m.showNoticeAs(noticeError, "Document state unreadable · reconnecting"))
			}
			s.rep, s.rev = rep, ev.Rev
			s.ed.clamp(rep.txt)
			if s.oldRep != nil || s.hasDraft {
				reapply = m.reapplyDraft(s, ev.Update)
			}
			for _, v := range m.filesViews {
				for _, b := range v.buffers {
					if b.doc == s.id {
						b.docGoneN = 0
					}
				}
			}
		} else if err := m.applyDocRemote(s, ev.Update); err != nil {
			m.docResync(s, "", false)
			return m.showNoticeAs(noticeError, "Document out of sync · reloading it")
		}
		s.rev = max(s.rev, ev.Rev)
		s.connected = true
		for i := range s.pending {
			s.pending[i].sent = false
		}
		return tea.Batch(reapply, m.sendPending(s), m.enterWantedEdit(s))
	case protocol.DocumentEventUpdate, protocol.DocumentEventSync:
		if s.rep == nil {
			return nil
		}
		if err := m.applyDocRemote(s, ev.Update); err != nil {
			m.docResync(s, "", false)
			return m.showNoticeAs(noticeError, "Document out of sync · reloading it")
		}
		s.rev = max(s.rev, ev.Rev)
	case protocol.DocumentEventAck:
		for i, op := range s.pending {
			if op.id == ev.Op {
				s.pending = slices.Delete(s.pending, i, i+1)
				break
			}
		}
		s.rev = max(s.rev, ev.Rev)
		if len(s.pending) == 0 {
			s.resending, s.reapplying = false, false
		}
	case protocol.DocumentEventRejected:
		rej := ev.Rejected
		if rej == nil {
			return nil
		}
		if rej.Resync {
			return m.docResync(s, docRejectCopy(rej), false)
		}
		if rej.Reason == protocol.DocumentRejectUnavailable && ev.Op != "" {
			// Not applied and the replica is still valid: pause sending and
			// resend everything from the refused op on, in order.
			refused := false
			for i := range s.pending {
				refused = refused || s.pending[i].id == ev.Op
				if refused {
					s.pending[i].sent = false
				}
			}
			return m.scheduleDocResend(s)
		}
		return m.showNoticeAs(noticeError, "Document request refused · "+safe(singleLine(rej.Message)))
	case protocol.DocumentEventStatus:
		if ev.Status != nil {
			cmd := m.acceptDocStatus(s, *ev.Status, true)
			if s.resending && ev.Status.State != protocol.DocumentStateFailed {
				// Storage recovered: resend now rather than at the tick.
				s.resending = false
				return tea.Batch(cmd, m.sendPending(s))
			}
			return cmd
		}
	case protocol.DocumentEventClosed:
		reason := "The document was discarded"
		if ev.Reason != "" {
			reason = safe(singleLine(ev.Reason))
		}
		return m.docGone(s, "closed", reason)
	}
	return nil
}

// acceptDocStatus takes a newer status and reacts to editor changes.
func (m *Model) acceptDocStatus(s *docSession, st protocol.DocumentStatus, live bool) tea.Cmd {
	lost := s.status.Editor == m.clientID && st.Editor != m.clientID
	s.status = st
	s.live = s.live || live
	if lost {
		s.wantEdit = false
		if m.docEdit == s.id {
			m.docEdit = ""
		}
		if st.Editor != "" {
			return m.showNoticeAs(noticeUnavailable, "Another client took over editing "+safe(singleLine(s.path)))
		}
	}
	return m.enterWantedEdit(s)
}

// applyDocRemote applies a server update, keeping this client's cursor,
// selection anchor and each buffer's first visible line on the same text.
func (m *Model) applyDocRemote(s *docSession, update []byte) error {
	t := s.rep.txt
	keep := []int{t.offset(s.ed.cur), t.offset(s.ed.anchor)}
	type top struct {
		b      *fileBuffer
		within int
	}
	var tops []top
	for _, v := range m.filesViews {
		for _, b := range v.buffers {
			if b.doc == s.id {
				line, within := t.rowLine(b.scroll, docWrapRoom(b))
				tops = append(tops, top{b, within})
				keep = append(keep, t.lineStart(line))
			}
		}
	}
	out, err := s.rep.applyRemote(update, keep)
	if err != nil {
		return err
	}
	t = s.rep.txt
	s.ed.cur = s.ed.snap(t, t.posAt(out[0]))
	s.ed.anchor = s.ed.snap(t, t.posAt(out[1]))
	for i, tp := range tops {
		room := docWrapRoom(tp.b)
		line := t.posAt(out[2+i]).line
		tp.b.scroll = line
		if room > 0 {
			tp.b.scroll = t.rowStart(line, room) + min(tp.within, t.rowCount(line, room)-1)
		}
	}
	return nil
}

// docResync discards the replica after a refusal that requires it (or after
// the replica itself failed, local) and reconnects with a fresh replica ID.
// Unacknowledged edits are kept as a draft: the old replica is merged into
// the rebuilt one's state and the difference re-applied as fresh
// transactions (reapplyDraft); a failed replica's visible text is diffed
// against the new state instead. A draft whose re-application is refused
// again is kept as text to copy. Nothing is dropped silently.
func (m *Model) docResync(s *docSession, reason string, local bool) tea.Cmd {
	if reason == "" {
		reason = "the server could not accept them"
	}
	var notice tea.Cmd
	switch {
	case s.rep == nil:
		// Already recovering (or never built): keep the draft as it is.
	case len(s.pending) == 0 && !local:
		s.rep.close()
	case s.reapplying && !local:
		notice = tea.Batch(m.keepLost(&s.lost, docLost{text: s.rep.txt.String(), reason: reason, path: s.path}),
			m.showNoticeAs(noticeError, "Edits not stored · "+reason+" · Copy keeps them"))
		s.rep.close()
	case local:
		// The caller sets the draft (intended text and its base); the
		// failed replica is kept in case its items can still be merged.
		s.oldRep, s.draftReason, s.hasDraft = s.rep, reason, true
		s.draftPending = len(s.pending)
		notice = m.showNoticeAs(noticeError, "Editor error · recovering your text from the server")
	default:
		s.oldRep, s.draftReason = s.rep, reason
	}
	s.pending, s.reapplying, s.resending = nil, false, false
	s.rep = nil
	s.replicaID = newReplicaID()
	if m.docEdit == s.id {
		if s.recovering() {
			s.wantEdit = true
		} else {
			m.docEdit = ""
		}
	}
	m.closeDocStream(s)
	m.markDirty()
	return notice
}

// reapplyDraft re-applies a recovering session's draft to the rebuilt
// replica. An old replica's items are merged into a scratch copy of the new
// state, so edits made meanwhile by others stay; the difference between the
// new text and the merged text becomes local transactions.
func (m *Model) reapplyDraft(s *docSession, state []byte) tea.Cmd {
	local, merged := s.hasDraft, s.draftText
	var err error
	var cmds []tea.Cmd
	if local {
		// The failed replica's items (if still readable) merged into the new
		// state give the others' concurrent edits plus this client's
		// unacknowledged ones; else the new state alone. Only the intended
		// change relative to the last good text is merged in, three-way.
		theirs := s.rep.txt.String()
		usable := false
		if s.oldRep != nil {
			if m0, e := mergedDraft(s.oldRep, state); e == nil {
				theirs, usable = m0, true
			}
		}
		var clean bool
		_, merged, clean, err = doc.Merge3(s.draftBase, s.draftText, theirs)
		if err == nil && !clean {
			err = errors.New("conflict")
		}
		if err != nil {
			merged = s.draftText
		} else if !usable && s.draftPending > 0 {
			// Unacknowledged edits before the failure may be missing from
			// the merge: keep the whole draft too.
			cmds = append(cmds, m.keepLost(&s.lost, docLost{text: s.draftText, reason: "your text before an editor error · check the file", path: s.path}))
		}
	} else if old := s.oldRep; old != nil {
		merged, err = mergedDraft(old, state)
		if err != nil {
			merged = old.txt.String()
		}
	}
	s.oldRep.close()
	s.oldRep, s.draftText, s.draftBase, s.draftView, s.hasDraft = nil, "", "", nil, false
	keep := func(reason string) tea.Cmd {
		s.wantEdit = false
		if m.docEdit == s.id {
			m.docEdit = ""
		}
		return tea.Batch(append(cmds, m.keepLost(&s.lost, docLost{text: merged, reason: reason, path: s.path}),
			m.showNoticeAs(noticeError, "Edits not stored · "+reason+" · Copy keeps them"))...)
	}
	if local && err != nil {
		return keep("it conflicts with changes made meanwhile")
	}
	switch {
	case err != nil:
		return keep(s.draftReason)
	case s.otherEditor(m.clientID):
		return keep("another client is editing")
	case s.status.State == protocol.DocumentStateReadOnly:
		return keep("the file became read-only")
	}
	full := s.rep.txt.String()
	edits := doc.DiffEdits(full, merged)
	if len(edits) == 0 {
		return nil
	}
	if s.status.Editor != m.clientID {
		// Nobody holds the role: keep the text rather than guess.
		return keep(s.draftReason)
	}
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		if err := s.rep.edit(u16Len(full[:e.Start]), u16Len(full[e.Start:e.End]), e.Text, i < len(edits)-1); err != nil {
			s.rep.takeUpdates()
			return keep(s.draftReason)
		}
	}
	for _, u := range s.rep.takeUpdates() {
		s.opSeq++
		s.pending = append(s.pending, docOp{id: s.nonce + "-" + strconv.FormatUint(s.opSeq, 10), update: u})
	}
	s.reapplying = true
	t := s.rep.txt
	s.ed.cur = s.ed.snap(t, t.posAt(t.offset(s.ed.cur)))
	s.ed.anchor = s.ed.cur
	if local {
		return tea.Batch(append(cmds, m.showNoticeAs(noticeUnavailable, "Recovered your text after an editor error · check it"))...)
	}
	return m.showNoticeAs(noticeInfo, "Resynchronized · your unsaved edits were applied again")
}

// mergedDraft returns the text of state merged with everything old has.
func mergedDraft(old *docReplica, state []byte) (_ string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("merge failed: %v", p)
		}
	}()
	scratch := crdt.New()
	if err := safeApplyUpdate(scratch, state, nil); err != nil {
		return "", err
	}
	diff := crdt.EncodeStateAsUpdateV1(old.doc, scratch.StateVector())
	if err := safeApplyUpdate(scratch, diff, nil); err != nil {
		return "", err
	}
	if p := scratch.PendingStats(); p.Items > 0 || p.DeleteRanges > 0 {
		return "", errors.New("draft depends on missing state")
	}
	text := scratch.GetText(protocol.DocumentTextName).ToString()
	return normalizeDocText(text), nil
}

// docRejectCopy explains a refused update in user terms.
func docRejectCopy(r *protocol.DocumentRejected) string {
	switch r.Reason {
	case protocol.DocumentRejectNotEditor, protocol.DocumentRejectStaleGeneration:
		return "another client took over editing"
	case "too_large":
		return "the change was too large"
	case "rejected":
		if r.Message != "" {
			return safe(singleLine(r.Message))
		}
	}
	return "the server refused them (" + safe(singleLine(r.Reason)) + ")"
}

// docGone ends a session whose document no longer exists on the server.
// Its buffers fall back to reading the file and reopen it; unstored text is
// kept on the buffers as copies. Repeated disappearance stops reopening.
func (m *Model) docGone(s *docSession, why, reason string) tea.Cmd {
	var cmds []tea.Cmd
	if text, ok := s.unstoredText(); ok {
		cmds = append(cmds, m.keepLost(&s.lost, docLost{text: text, reason: reason, path: s.path}))
	}
	s.oldRep.close()
	s.oldRep, s.draftText, s.draftBase, s.draftView, s.hasDraft = nil, "", "", nil, false
	s.gone = why
	s.pending = nil
	m.closeDocStream(s)
	shown := false
	for key, v := range m.filesViews {
		for _, b := range v.buffers {
			if b.doc != s.id {
				continue
			}
			b.doc = ""
			if !shown {
				// One buffer receives the copies; others would duplicate them.
				for _, l := range s.lost {
					cmds = append(cmds, m.keepLost(&b.docLost, l))
				}
				shown = true
			}
			b.docGoneN++
			if b.docGoneN >= 3 {
				// Reopening keeps failing: stop until the user retries.
				b.docErr = "the document keeps closing on the server"
				continue
			}
			if key == m.filesShownKey() {
				cmds = append(cmds, m.readFilesBuffer(b))
			} else {
				b.read = nil
			}
		}
	}
	if !shown {
		cmds = append(cmds, m.orphanLost(s.lost))
	}
	s.lost = nil
	if m.docEdit == s.id {
		m.docEdit = ""
	}
	return tea.Batch(append(cmds, m.showNoticeAs(noticeUnavailable, reason+" · showing the file"))...)
}

func (m *Model) filesShownKey() string {
	key, _ := m.filesTarget()
	return key
}

// syncDocSnapshot refreshes statuses of sessions without a live stream from
// the coalesced snapshot.
func (m *Model) syncDocSnapshot() tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range m.docs {
		if s.connected {
			continue
		}
		for _, st := range m.snapshot.Documents {
			if st.ID == s.id {
				cmds = append(cmds, m.acceptDocStatus(s, st, false))
			}
		}
	}
	return tea.Batch(cmds...)
}

// docStatusLine is the save state in user terms, with its panel status mark
// state. Saved is shown only when the file holds the durable revision and
// nothing typed here is still unacknowledged.
func (m *Model) docStatusLine(s *docSession) (state, text string) {
	st := s.status
	pending := len(s.pending)
	switch {
	case s.gone != "":
		return "unavailable", "Closed"
	case s.recovering():
		return "active", "Recovering your edits…"
	case st.Quarantined:
		return "failed", "Failed · stored document damaged"
	case st.State == protocol.DocumentStateReadOnly:
		return "unavailable", "Read-only · " + docReason(st.Reason, "the file cannot be edited")
	case st.State == protocol.DocumentStateDeleted:
		return "failed", "Deleted on disk · autosave paused"
	case st.State == protocol.DocumentStatePausedConflict:
		return "stale", "Paused · changed on disk"
	case pending > 0 && (!s.connected || s.resending):
		return "waiting", fmt.Sprintf("Not yet stored · retrying · %d %s", pending, docPlural(pending, "edit", "edits"))
	case pending > 0:
		return "active", "Storing…"
	case !s.connected && s.rep != nil:
		return "disconnected", "Reconnecting…"
	case st.State == protocol.DocumentStateFailed:
		return "failed", "Failed · " + docReason(st.Error, st.Reason) + " · retrying"
	case st.State == protocol.DocumentStateReconciling:
		return "active", "Checking the file…"
	case st.State == protocol.DocumentStateSaving:
		return "active", "Saving…"
	case st.State == protocol.DocumentStateSaved && st.SavedRev == st.DurableRev && s.rev >= st.DurableRev:
		return "completed", "Saved"
	case st.DurableRev > st.SavedRev && st.SavedRev >= 0:
		n := st.DurableRev - st.SavedRev
		return "pending", fmt.Sprintf("Unsaved · %d pending", n)
	}
	return "pending", "Unsaved"
}

func docReason(reason, fallback string) string {
	if r := safe(singleLine(reason)); r != "" {
		return r
	}
	return fallback
}

func docPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// docAction handles the document controls ("doc-" actions).
func (m *Model) docAction(a action) tea.Cmd {
	if strings.HasPrefix(a.Kind, "doc-review-") || strings.HasPrefix(a.Kind, "doc-resolve") {
		return m.docReviewAction(a)
	}
	s := m.docs[a.ID]
	var b *fileBuffer
	if v := m.currentFilesView(); v != nil {
		b = v.buffer()
	}
	switch a.Kind {
	case "doc-cancel":
		return nil
	case "doc-take":
		if s != nil && s.gone == "" {
			m.takeOverDoc(s)
		}
		return nil
	case "doc-take-confirm":
		if s == nil || s.gone != "" {
			return m.showNoticeAs(noticeUnavailable, "The document is no longer open")
		}
		if s.status.Editor == m.clientID {
			s.wantEdit = true
			return m.enterWantedEdit(s)
		}
		c := m.docCommand(protocol.DocumentKindTakeEdit, s.id)
		s.editing, s.wantEdit = &c, true
		return tea.Batch(m.setFocus("files-text"), m.sendDocCommand(c, "", ""))
	case "doc-review":
		return m.openDocReview(s)
	case "doc-kept", "doc-kept-copy", "doc-kept-copy-all", "doc-kept-dismiss":
		return m.keptAction(a)
	case "doc-lost-copy":
		if l, ok := m.newestLost(s, b); ok {
			return m.copyText(clipboardSafeText(l.text))
		}
	case "doc-lost-dismiss":
		switch {
		case s != nil && len(s.lost) > 0:
			s.lost = s.lost[:len(s.lost)-1]
		case b != nil && len(b.docLost) > 0:
			b.docLost = b.docLost[:len(b.docLost)-1]
		}
		m.markDirty()
	case "doc-copy-live":
		if t := s.viewText(); s != nil && t != nil {
			return m.copyText(clipboardSafeText(t.String()))
		}
	case "doc-retry":
		if b != nil && b.doc == "" {
			key, _ := m.filesTarget()
			return m.retryDocOpen(key, b)
		}
	case "doc-close-force":
		v := m.currentFilesView()
		if v == nil {
			return nil
		}
		for i, buf := range v.buffers {
			if buf.doc == a.ID && buf.path == a.Value {
				// Explicitly discarded by the user.
				buf.docLost = nil
				if s != nil {
					s.pending, s.lost = nil, nil
					s.oldRep.close()
					s.oldRep, s.draftText, s.draftBase, s.draftView, s.hasDraft = nil, "", "", nil, false
				}
				return m.closeFilesBuffer(i)
			}
		}
	case "doc-quit-force":
		return m.quit()
	case "doc-dismiss":
		m.docConfirm("Delete retained edits · ", a.Value, []string{
			"Deletes edits kept from an earlier session of this file.",
			"They could not be loaded and are not in the file on disk.",
			"This cannot be undone.",
		}, "", menuItem{Label: "Delete retained edits", Action: action{Kind: "doc-dismiss-confirm", ID: a.ID}})
	case "doc-dismiss-confirm":
		c := m.docCommand(protocol.DocumentKindDismiss, a.ID)
		c.Text = protocol.DocumentDismissConfirm
		return m.sendDocCommand(c, "", "")
	}
	return nil
}

// quarantinedDoc returns a quarantined stored document for b's file.
func (m *Model) quarantinedDoc(b *fileBuffer) (protocol.DocumentStatus, bool) {
	checkout := filepath.Clean(m.thread().Checkout)
	for _, st := range m.snapshot.Documents {
		if st.Quarantined && st.Path == b.path && filepath.Clean(st.Checkout) == checkout {
			return st, true
		}
	}
	return protocol.DocumentStatus{}, false
}

// docUnstored counts what this client holds that the server has not
// stored: unacknowledged edits, recovering drafts and refused copies.
func (m *Model) docUnstored() int {
	n := 0
	for _, s := range m.docs {
		n += len(s.pending) + len(s.lost)
		if s.recovering() {
			n++
		}
	}
	for _, v := range m.filesViews {
		for _, b := range v.buffers {
			n += len(b.docLost)
		}
	}
	return n + len(m.docOrphans)
}

// docQuitGuard asks before detaching while anything is unstored (closing
// any open menu first); it reports whether it did.
func (m *Model) docQuitGuard() bool {
	n := m.docUnstored()
	if n == 0 {
		return false
	}
	m.contextMenu, m.projectMode = nil, ""
	m.docConfirm("Edits not stored yet", "", []string{
		fmt.Sprintf("%d %s here %s not stored by the server.", n, docPlural(n, "change", "changes"), docPlural(n, "is", "are")),
		"Detaching now loses " + docPlural(n, "it", "them") + ".",
	}, "Keep editing", menuItem{Label: "Detach anyway", Action: action{Kind: "doc-quit-force"}})
	return true
}

// newestLost returns the most recent refused text for a buffer.
func (m *Model) newestLost(s *docSession, b *fileBuffer) (docLost, bool) {
	if s != nil && len(s.lost) > 0 {
		return s.lost[len(s.lost)-1], true
	}
	if b != nil && len(b.docLost) > 0 {
		return b.docLost[len(b.docLost)-1], true
	}
	return docLost{}, false
}

// docCloseGuard asks before closing a buffer that holds unstored text (the
// last buffer of a document with unacknowledged edits or a draft, or kept
// copies); it reports whether it did.
func (m *Model) docCloseGuard(b *fileBuffer) bool {
	n := len(b.docLost)
	s := m.bufferDoc(b)
	last := s != nil && m.docRefs()[s.id] <= 1
	if last {
		n += len(s.pending) + len(s.lost)
		if s.recovering() {
			n++
		}
	}
	if n == 0 {
		return false
	}
	id := ""
	if s != nil {
		id = s.id
	}
	choices := []menuItem{{Label: "Copy my text", Action: action{Kind: "doc-copy-live", ID: id}}}
	if len(b.docLost) > 0 || last && len(s.lost) > 0 {
		choices = append(choices, menuItem{Label: "Copy all kept texts", Action: action{Kind: "doc-kept-copy-all", Value: "buffer"}})
	}
	if s == nil || s.viewText() == nil {
		choices = choices[1:]
	}
	m.docConfirm("Edits not stored yet · ", b.path, []string{
		"This file holds text the server has not stored.",
		fmt.Sprintf("Closing now discards %d %s.", n, docPlural(n, "change", "changes")),
	}, "Keep open", append(choices, menuItem{Label: "Close anyway", Action: action{Kind: "doc-close-force", ID: id, Value: b.path}})...)
	return true
}

// docConfirm shows a confirmation: short note rows naming the consequence,
// then cancel (focused, the default; "" names it Cancel) and the choices,
// the confirming action last.
func (m *Model) docConfirm(title, user string, notes []string, cancel string, choices ...menuItem) {
	if cancel == "" {
		cancel = "Cancel"
	}
	var items []menuItem
	for _, n := range notes {
		items = append(items, menuItem{Note: n})
	}
	items = append(items, menuItem{Label: cancel, Action: action{Kind: "doc-cancel"}})
	items = append(items, choices...)
	if user != "" {
		m.showMenuFor(title, safe(singleLine(user)), items)
	} else {
		m.showMenu(title, items)
	}
	m.menuIndex = len(notes)
}
