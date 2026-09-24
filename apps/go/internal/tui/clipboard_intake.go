package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// clipboard_intake.go turns an explicit Paste into image or copied-file
// attachments when a verified local clipboard adapter can report types
// (docs/research/go-image-clipboard-2026-09-24.md). Only Wayland's wl-paste
// is used; everything else keeps the text Paste path. The clipboard is never
// polled, and terminal bracketed paste stays text-only.

const (
	clipboardTypesTimeout = 2 * time.Second
	clipboardReadTimeout  = 10 * time.Second
	artifactTransferLimit = 60 * time.Second
	clipboardTypesLimit   = 64 << 10
	clipboardURIListLimit = 1 << 20
	attachmentCap         = 8
	// localReadTimeout bounds one local file read (a slow or network mount).
	localReadTimeout = 20 * time.Second
)

var errClipboardTooLarge = errors.New("clipboard data exceeds the size limit")

// clipboardSource is a local clipboard adapter that can list offered media
// types and read one of them with a byte bound. Tests inject a fake.
type clipboardSource interface {
	Types(ctx context.Context) ([]string, error)
	Read(ctx context.Context, mediaType string, limit int64) ([]byte, error)
}

// artifactAPI is the subset of the server client used for attachments.
type artifactAPI interface {
	UploadArtifact(ctx context.Context, name, mediaType string, data []byte) (protocol.ArtifactInfo, error)
	FetchArtifact(ctx context.Context, id string, limit int64) ([]byte, string, error)
	PreviewFile(ctx context.Context, projectID, threadID, path string) (protocol.FilePreview, error)
	DeleteArtifact(ctx context.Context, id string) error
}

func (m *Model) artifactClient() artifactAPI {
	if m.artifacts != nil {
		return m.artifacts
	}
	if m.client != nil {
		return m.client
	}
	return nil
}

var _ artifactAPI = (*client.Client)(nil)

// wlPaste reads the Wayland clipboard through wl-paste.
type wlPaste struct{ path string }

func (w wlPaste) Types(ctx context.Context) ([]string, error) {
	data, err := runBounded(ctx, clipboardTypesLimit, w.path, "--list-types")
	if err != nil {
		return nil, err
	}
	var types []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			types = append(types, line)
		}
	}
	return types, nil
}

func (w wlPaste) Read(ctx context.Context, mediaType string, limit int64) ([]byte, error) {
	return runBounded(ctx, limit, w.path, "--no-newline", "--type", mediaType)
}

// runBounded runs a helper and returns at most limit bytes of its stdout; more
// output kills it and reports errClipboardTooLarge. ctx bounds its lifetime.
func runBounded(ctx context.Context, limit int64, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if int64(len(data)) > limit {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errClipboardTooLarge
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("clipboard helper timed out")
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return data, nil
}

// nativeClipboardSource returns the verified local adapter, or nil when only
// text Paste is available (macOS, X11, missing wl-paste).
func nativeClipboardSource() clipboardSource {
	if runtime.GOOS != "linux" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == "" {
		return nil
	}
	path, err := exec.LookPath("wl-paste")
	if err != nil {
		return nil
	}
	return wlPaste{path: path}
}

// clipboardSourceFor chooses the adapter. Injected text readers (tests) never
// fall through to the real clipboard.
func (m *Model) clipboardSourceFor() clipboardSource {
	if m.clipboardSource != nil {
		return m.clipboardSource
	}
	if m.clipboardRead != nil {
		return nil
	}
	return nativeClipboard()
}

// nativeClipboard is replaced in tests so no test can reach the real
// clipboard.
var nativeClipboard = nativeClipboardSource

type intakeKind int

const (
	intakeText intakeKind = iota
	intakeFiles
	intakeImage
)

// classifyClipboard picks copied files, then an image, then text.
func classifyClipboard(types []string) (intakeKind, string) {
	has := map[string]bool{}
	for _, t := range types {
		has[strings.ToLower(strings.TrimSpace(t))] = true
	}
	for _, t := range []string{"text/uri-list", "x-special/gnome-copied-files"} {
		if has[t] {
			return intakeFiles, t
		}
	}
	for _, t := range []string{"image/png", "image/jpeg", "image/gif"} {
		if has[t] {
			return intakeImage, t
		}
	}
	return intakeText, ""
}

// parseCopiedFiles decodes a text/uri-list or GNOME copied-files payload into
// absolute local paths. Non-file schemes and other hosts are skipped with a
// reason; nothing here touches the filesystem.
func parseCopiedFiles(mediaType string, data []byte) (paths, skipped []string) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if mediaType == "x-special/gnome-copied-files" && len(lines) > 0 {
		if op := strings.TrimSpace(lines[0]); op == "copy" || op == "cut" {
			lines = lines[1:]
		}
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" {
			skipped = append(skipped, safe(singleLine(line))+" (not a local file)")
			continue
		}
		if host := strings.ToLower(u.Host); host != "" && host != "localhost" && host != localHostname() {
			skipped = append(skipped, safe(singleLine(line))+" (on another machine)")
			continue
		}
		if u.Path == "" || !filepath.IsAbs(u.Path) {
			skipped = append(skipped, safe(singleLine(line))+" (not an absolute path)")
			continue
		}
		paths = append(paths, filepath.Clean(u.Path))
	}
	return paths, skipped
}

func localHostname() string {
	h, _ := os.Hostname()
	return strings.ToLower(h)
}

// draftOwner identifies one composer draft: an existing thread, or the
// per-project new-thread draft.
type draftOwner struct{ threadID, projectID, draftID string }

func (m *Model) currentOwner() draftOwner {
	if m.creatingThread() {
		v := m.viewState()
		if v.DraftID == "" {
			v.DraftID = identity()
		}
		return draftOwner{projectID: m.state.DraftProjectID, draftID: v.DraftID}
	}
	return draftOwner{threadID: m.state.Active}
}

// ownerView returns the owner's stored draft without creating one.
func (m *Model) ownerView(o draftOwner) *threadView {
	if o.threadID != "" {
		return m.state.Threads[o.threadID]
	}
	if o.projectID != "" {
		// Only the same draft instance: one consumed by Send (thread.start)
		// has a new identity, and a later draft never receives its results.
		if v := m.state.DraftThreads[o.projectID]; v != nil && v.DraftID == o.draftID {
			return v
		}
	}
	return nil
}

func (m *Model) ownerActive(o draftOwner) bool { return m.hasComposer() && m.currentOwner() == o }

type clipboardFilesMsg struct {
	id      uint64
	owner   draftOwner
	files   []string
	skipped []string
	err     error
}

type clipboardImageMsg struct {
	id    uint64
	owner draftOwner
	name  string
	info  protocol.ArtifactInfo
	err   error
}

// nativePaste lists the clipboard's types off the input path and routes the
// result: copied files and images become attachments of the originating
// draft; everything else falls back to the guarded text Paste.
func (m *Model) nativePaste(src clipboardSource, generation uint64, target clipboardPasteTarget) tea.Cmd {
	m.intakeSeq++
	id := m.intakeSeq
	owner := m.currentOwner()
	if m.intakes == nil {
		m.intakes = map[uint64]draftOwner{}
	}
	m.intakes[id] = owner
	api := m.artifactClient()
	ctx := m.ctx
	read := m.clipboardRead
	if read == nil {
		read = clipboard.ReadAll
	}
	room := attachmentCap - len(m.viewState().Attachments)
	now := time.Now()
	return func() tea.Msg {
		text := func() tea.Msg {
			s, err := read()
			return clipboardReadMsg{generation: generation, target: target, text: s, err: err}
		}
		tctx, cancel := context.WithTimeout(ctx, clipboardTypesTimeout)
		types, err := src.Types(tctx)
		cancel()
		if err != nil {
			return text()
		}
		kind, mediaType := classifyClipboard(types)
		switch kind {
		case intakeFiles:
			rctx, cancel := context.WithTimeout(ctx, clipboardReadTimeout)
			data, err := src.Read(rctx, mediaType, clipboardURIListLimit)
			cancel()
			if err != nil {
				return clipboardFilesMsg{id: id, owner: owner, err: err}
			}
			paths, skipped := parseCopiedFiles(mediaType, data)
			var files []string
			for _, p := range paths {
				info, err := os.Stat(p)
				switch {
				case err != nil:
					skipped = append(skipped, filepath.Base(p)+" (unreadable)")
				case !info.Mode().IsRegular():
					skipped = append(skipped, filepath.Base(p)+" (not a regular file)")
				default:
					files = append(files, p)
				}
			}
			return clipboardFilesMsg{id: id, owner: owner, files: files, skipped: skipped}
		case intakeImage:
			// Unique per paste, so identical-second pastes stay distinguishable.
			name := fmt.Sprintf("clipboard-%s-%d%s", now.Format("20060102-150405"), id, imageExtension(mediaType))
			if room <= 0 {
				return clipboardImageMsg{id: id, owner: owner, name: name, err: errors.New("attachment limit: 8")}
			}
			if api == nil {
				return clipboardImageMsg{id: id, owner: owner, name: name, err: errors.New("not connected to the server")}
			}
			rctx, cancel := context.WithTimeout(ctx, clipboardReadTimeout)
			data, err := src.Read(rctx, mediaType, client.ArtifactLimit)
			cancel()
			if err != nil {
				return clipboardImageMsg{id: id, owner: owner, name: name, err: err}
			}
			uctx, cancel := context.WithTimeout(ctx, artifactTransferLimit)
			info, err := api.UploadArtifact(uctx, name, mediaType, data)
			cancel()
			return clipboardImageMsg{id: id, owner: owner, name: name, info: info, err: err}
		}
		return text()
	}
}

func imageExtension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}

// takeIntake consumes a pending intake identity so duplicates never apply
// twice, and resolves its originating draft if that still exists.
func (m *Model) takeIntake(id uint64, owner draftOwner) (*threadView, bool) {
	if o, ok := m.intakes[id]; !ok || o != owner {
		return nil, false
	}
	delete(m.intakes, id)
	return m.ownerView(owner), true
}

func (m *Model) intakeBlocked(owner draftOwner) string {
	if e := m.state.Edit; e != nil && owner.threadID != "" && e.ThreadID == owner.threadID {
		return "finish the queued edit first"
	}
	return ""
}

func (m *Model) acceptClipboardImage(msg clipboardImageMsg) tea.Cmd {
	v, ok := m.takeIntake(msg.id, msg.owner)
	if !ok {
		return nil
	}
	m.status = ""
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Image paste failed: "+artifactErrorText(msg.err))
	}
	// A dropped upload is staged but unused: free it without waiting.
	if v == nil {
		return tea.Batch(m.deleteStagedArtifact(msg.info.ID), m.showNotice("Pasted image dropped · its draft no longer exists"))
	}
	if reason := m.intakeBlocked(msg.owner); reason != "" {
		return tea.Batch(m.deleteStagedArtifact(msg.info.ID), m.showNoticeAs(noticeUnavailable, "Pasted image not attached · "+reason))
	}
	if len(v.Attachments) >= attachmentCap {
		return tea.Batch(m.deleteStagedArtifact(msg.info.ID), m.showNoticeAs(noticeUnavailable, "Attachment limit: 8"))
	}
	name := msg.info.Name
	if name == "" {
		name = msg.name
	}
	v.Attachments = append(v.Attachments, protocol.Attachment{Kind: "artifact", Name: name, ArtifactID: msg.info.ID,
		MediaType: msg.info.MediaType, Size: msg.info.Size, SHA256: msg.info.SHA256, Width: msg.info.Width, Height: msg.info.Height})
	m.markDirty()
	m.configureInputs()
	text := "Attached " + safe(singleLine(name))
	if !m.ownerActive(msg.owner) {
		text += " to its originating draft"
	}
	return m.showNoticeAs(noticeDone, text)
}

func (m *Model) acceptClipboardFiles(msg clipboardFilesMsg) tea.Cmd {
	v, ok := m.takeIntake(msg.id, msg.owner)
	if !ok {
		return nil
	}
	m.status = ""
	if msg.err != nil {
		return m.showNoticeAs(noticeError, "Copied-file paste failed: "+safe(msg.err.Error()))
	}
	if v == nil {
		return m.showNotice("Copied files dropped · their draft no longer exists")
	}
	if reason := m.intakeBlocked(msg.owner); reason != "" {
		return m.showNoticeAs(noticeUnavailable, "Copied files not attached · "+reason)
	}
	added, capped := 0, 0
	for _, path := range msg.files {
		dup := false
		for _, a := range v.Attachments {
			if a.Kind == "copied-file" && a.Source == path {
				dup = true
			}
		}
		if dup {
			continue
		}
		if len(v.Attachments) >= attachmentCap {
			capped++
			continue
		}
		v.Attachments = append(v.Attachments, protocol.Attachment{Kind: "copied-file", Name: filepath.Base(path), Source: path})
		added++
	}
	if added > 0 {
		m.markDirty()
		m.configureInputs()
	}
	var parts []string
	severity := noticeDone
	switch {
	case added == 1:
		parts = append(parts, "Attached 1 copied file")
	case added > 1:
		parts = append(parts, fmt.Sprintf("Attached %d copied files", added))
	case len(msg.files) == 0 && len(msg.skipped) == 0:
		parts = append(parts, "Clipboard lists no files")
	default:
		severity = noticeUnavailable
		parts = append(parts, "No copied files attached")
	}
	if capped > 0 {
		severity = noticeUnavailable
		parts = append(parts, fmt.Sprintf("%d over the limit of 8", capped))
	}
	if len(msg.skipped) > 0 {
		severity = noticeUnavailable
		parts = append(parts, "skipped "+strings.Join(msg.skipped, ", "))
	}
	return m.showNoticeAs(severity, strings.Join(parts, " · "))
}

// sendCapture is one Send attempt whose copied files are being read and
// uploaded. command is the draft as it was when Send was pressed.
type sendCapture struct {
	id      uint64
	owner   draftOwner
	command protocol.Command
	local   action
	cancel  context.CancelFunc
}

type captureUpload struct {
	index int
	name  string
	info  protocol.ArtifactInfo
	err   error
}

type sendCaptureMsg struct {
	id      uint64
	uploads []captureUpload
}

// sendAttachment converts one draft attachment into its Send form. Uploaded
// captures travel as artifact references; the server fills metadata.
func sendAttachment(a protocol.Attachment) protocol.Attachment {
	if a.ArtifactID != "" && (a.Kind == "artifact" || a.Kind == "copied-file") {
		out := protocol.Attachment{Kind: "artifact", Name: a.Name, ArtifactID: a.ArtifactID, PreviewSHA256: a.PreviewSHA256}
		if a.Kind == "copied-file" {
			// The client-local path is display identity only (Activity shows
			// where the capture came from); the server never reads it.
			out.Source = a.Source
		}
		return out
	}
	if a.Kind == "workspace-file" {
		return protocol.Attachment{Kind: a.Kind, Name: a.Name, Source: a.Source, PreviewSHA256: a.PreviewSHA256}
	}
	return a
}

// beginSendCapture reads and uploads every copied file that has no capture
// yet, then sends c. A retry reuses already recorded artifact ids.
func (m *Model) beginSendCapture(c protocol.Command, a action, drafts []protocol.Attachment) tea.Cmd {
	api := m.artifactClient()
	if api == nil {
		return m.showSendError("Not sent · not connected to the server")
	}
	m.captureSeq++
	id := m.captureSeq
	owner := m.currentOwner()
	if c.Kind != "thread.start" {
		c.ThreadID = owner.threadID
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.sendCapture = &sendCapture{id: id, owner: owner, command: c, local: a, cancel: cancel}
	type job struct {
		index      int
		name, path string
	}
	var jobs []job
	for i, d := range drafts {
		if d.Kind == "copied-file" && d.ArtifactID == "" {
			jobs = append(jobs, job{i, d.Name, d.Source})
		}
	}
	m.status = "Capturing attached files… · Esc cancels"
	return func() tea.Msg {
		defer cancel()
		out := sendCaptureMsg{id: id}
		for _, j := range jobs {
			u := captureUpload{index: j.index, name: j.name}
			rctx, rcancel := context.WithTimeout(ctx, localReadTimeout)
			data, err := readLocalFile(rctx, j.path, client.ArtifactLimit)
			rcancel()
			if err == nil {
				uctx, cancel := context.WithTimeout(ctx, artifactTransferLimit)
				u.info, err = api.UploadArtifact(uctx, j.name, "", data)
				cancel()
			}
			u.err = err
			out.uploads = append(out.uploads, u)
			if err != nil {
				break
			}
		}
		return out
	}
}

// readLocalFile reads a regular file with a byte bound. It checks the path
// before opening, opens without blocking (a FIFO or device never stalls the
// open), verifies the opened file is the same regular file, and gives up when
// ctx ends.
func readLocalFile(ctx context.Context, path string, limit int64) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if before.Size() > limit {
		return nil, fmt.Errorf("larger than %d MiB", limit>>20)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, errors.New("file changed while opening")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("larger than %d MiB", limit>>20)
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(f, limit+1))
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		if int64(len(r.data)) > limit {
			return nil, fmt.Errorf("larger than %d MiB", limit>>20)
		}
		return r.data, nil
	case <-ctx.Done():
		_ = f.Close() // unblocks the reader where the file system allows
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("reading timed out")
		}
		return nil, errors.New("cancelled")
	}
}

// cancelSendCapture abandons an in-progress Send capture, keeping the draft.
func (m *Model) cancelSendCapture() tea.Cmd {
	sc := m.sendCapture
	if sc == nil {
		return nil
	}
	sc.cancel()
	m.sendCapture = nil
	m.status = ""
	return m.showNoticeAs(noticeUnavailable, "Send cancelled · draft kept")
}

func (m *Model) acceptSendCapture(msg sendCaptureMsg) tea.Cmd {
	sc := m.sendCapture
	if sc == nil || sc.id != msg.id {
		// A cancelled capture's finished uploads are staged but unused.
		var cmds []tea.Cmd
		for _, u := range msg.uploads {
			if u.err == nil {
				cmds = append(cmds, m.deleteStagedArtifact(u.info.ID))
			}
		}
		return tea.Batch(cmds...)
	}
	m.sendCapture = nil
	m.status = ""
	v := m.ownerView(sc.owner)
	var failed *captureUpload
	for i := range msg.uploads {
		u := &msg.uploads[i]
		if u.err != nil {
			failed = u
			continue
		}
		// Record the capture on the draft so a retry never re-reads or
		// re-uploads; only the same unchanged attachment receives it.
		if v != nil && u.index < len(v.Attachments) {
			d := &v.Attachments[u.index]
			sent := sc.command.Attachments[u.index]
			if d.Kind == "copied-file" && d.ArtifactID == "" && d.Name == sent.Name && d.Source == sent.Source {
				d.ArtifactID, d.MediaType, d.Size, d.SHA256 = u.info.ID, u.info.MediaType, u.info.Size, u.info.SHA256
				m.markDirty()
			}
		}
		sc.command.Attachments[u.index].ArtifactID = u.info.ID
	}
	if failed != nil {
		return m.showSendError("Not sent · " + safe(singleLine(failed.name)) + ": " + artifactErrorText(failed.err))
	}
	if m.busy != nil {
		// Another command became pending during the capture. The captures
		// are recorded on the draft, so a later Send reuses them.
		return m.showSendError("Not sent · A command is pending; use Retry to reconcile it")
	}
	for i, a := range sc.command.Attachments {
		sc.command.Attachments[i] = sendAttachment(a)
	}
	return m.command(sc.command, sc.local)
}

// sameCapture reports whether a draft attachment is the one a command sent.
func sameCapture(draft, sent protocol.Attachment) bool {
	if sent.Kind == "artifact" || sent.ArtifactID != "" {
		return draft.ArtifactID != "" && draft.ArtifactID == sent.ArtifactID
	}
	return draft.Kind == sent.Kind && draft.Name == sent.Name && draft.Source == sent.Source
}
