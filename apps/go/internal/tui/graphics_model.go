package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/shell"
)

// graphics_model.go wires the kitty graphics library (graphics.go,
// graphics_query.go; ADR 0018) into the Model: probe replies, image
// preparation for the attachment viewer and composer thumbnails, and cleanup
// before suspend and exit. Rendering only reads this state; every transmit,
// placement and delete leaves through a command.

// viewerImageKey is the registry key of the viewer's single image.
const viewerImageKey = "viewer"

// Composer thumbnails fit a box of this many cells above their chip.
const (
	thumbCols = 8
	thumbRows = 2
)

// cellSizeRequest asks the terminal for its cell pixel size (CSI 16 t).
const cellSizeRequest = "\x1b[16t"

type viewerImageMsg struct {
	seq  uint64
	img  graphicsPrepared
	err  error
	box  graphicsBox
	open uint64 // the viewer load identity that supplied the bytes
}

// thumbnail is one composer image's load and preparation state.
type thumbnail struct {
	seq     uint64
	loading bool
	prep    *graphicsPrepared
	failed  bool
}

type thumbnailMsg struct {
	key string
	seq uint64
	img graphicsPrepared
	err error
}

func (m *Model) imgs() *graphicsRegistry {
	if m.images == nil {
		m.images = newGraphicsRegistry(0)
	}
	return m.images
}

// graphicsUpdate consumes probe replies and the registry's own messages. A
// capability or cell-size change re-prepares whatever is shown.
func (m *Model) graphicsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	was := m.graphics.Supported()
	cw, ch := m.graphics.CellPixels()
	if m.graphics.Update(msg) {
		now := m.graphics.Supported()
		nw, nh := m.graphics.CellPixels()
		switch {
		case now && !was:
			return m.startGraphics(), true
		case was && !now:
			return m.dropGraphics(), true
		case now && (nw != cw || nh != ch):
			return m.startGraphics(), true
		}
		return nil, true
	}
	if cmd, ok := m.imgs().Update(msg); ok {
		return cmd, true
	}
	return nil, false
}

// startGraphics (re)prepares the viewer image and drops thumbnails so the
// next sync reloads them at the current cell size.
func (m *Model) startGraphics() tea.Cmd {
	var cmds []tea.Cmd
	for key := range m.thumbs {
		cmds = append(cmds, m.imgs().Release(key))
	}
	m.thumbs = nil
	if vw := m.viewer; vw != nil && imageAttachment(vw.att) {
		switch {
		case vw.imgData != nil:
			vw.imgBox = graphicsBox{}
			cmds = append(cmds, m.syncViewerImage())
		case !vw.loading:
			cmds = append(cmds, m.loadViewerPreview(vw.source, vw.index))
		}
	}
	return tea.Batch(cmds...)
}

// dropGraphics deletes every owned image after graphics became unavailable;
// rendering falls back to metadata blocks.
func (m *Model) dropGraphics() tea.Cmd {
	m.thumbs = nil // in-flight thumbnail results find no entry
	if vw := m.viewer; vw != nil {
		vw.imgPrep, vw.imgPreparing, vw.imgBox = nil, false, graphicsBox{}
		m.graphicsSeq++
		vw.imgSeq = m.graphicsSeq // in-flight viewer preparation is stale
	}
	if seq := m.imgs().DeleteAllSequence(); seq != "" {
		return tea.Raw(seq)
	}
	return nil
}

func (m *Model) graphicsProfileChanged(profile colorprofile.Profile) tea.Cmd {
	was := m.graphics.Supported()
	m.graphics.ProfileChanged(profile)
	if was && !m.graphics.Supported() {
		return m.dropGraphics()
	}
	return nil
}

// graphicsCleanup deletes this client's images before next (suspend or
// exit), so none survive in the terminal. Other programs' images are kept.
func (m *Model) graphicsCleanup(next tea.Cmd) tea.Cmd {
	if seq := m.imgs().DeleteAllSequence(); seq != "" {
		return tea.Sequence(tea.Raw(seq), next)
	}
	return next
}

// quit closes the registry (no further transmits) and deletes every id that
// may have been transmitted before Quit.
func (m *Model) quit() tea.Cmd {
	if seq := m.imgs().Close(); seq != "" {
		return tea.Sequence(tea.Raw(seq), tea.Quit)
	}
	return tea.Quit
}

func (m *Model) suspend() tea.Cmd { return m.graphicsCleanup(tea.Suspend) }

// reshowGraphics retransmits prepared images after resume.
func (m *Model) reshowGraphics() tea.Cmd {
	if !m.graphics.Supported() {
		return nil
	}
	var cmds []tea.Cmd
	if vw := m.viewer; vw != nil && vw.imgPrep != nil {
		cmds = append(cmds, m.imgs().Show(*vw.imgPrep))
	}
	for _, t := range m.thumbs {
		if t.prep != nil {
			cmds = append(cmds, m.imgs().Show(*t.prep))
		}
	}
	return tea.Batch(cmds...)
}

// resizeGraphics follows a window resize: it re-requests the cell size (a
// font change resizes the grid) and re-fits the viewer image.
func (m *Model) resizeGraphics() tea.Cmd {
	if !m.graphics.Supported() {
		return nil
	}
	return tea.Batch(tea.Raw(cellSizeRequest), m.syncViewerImage())
}

// --- Viewer ------------------------------------------------------------------

func (m *Model) viewerImageBox() graphicsBox {
	body := m.viewerLayout().body
	cw, ch := m.graphics.CellPixels()
	return graphicsBox{MaxCols: max(0, body.W), MaxRows: max(0, body.H), CellW: cw, CellH: ch}
}

// syncViewerImage prepares the viewer's image for the current body box when
// it differs from the prepared one (open, resize, expand/restore).
func (m *Model) syncViewerImage() tea.Cmd {
	vw := m.viewer
	if vw == nil || vw.imgData == nil || !m.graphics.Supported() {
		return nil
	}
	box := m.viewerImageBox()
	if box.MaxCols <= 0 || box.MaxRows <= 0 || box == vw.imgBox || vw.imgPreparing {
		// One preparation at a time; its result re-checks the latest box.
		return nil
	}
	vw.imgBox, vw.imgPreparing, vw.imgErr = box, true, ""
	m.graphicsSeq++
	seq, data, open := m.graphicsSeq, vw.imgData, vw.loadID
	vw.imgSeq = seq
	return func() tea.Msg {
		img, err := prepareGraphics(viewerImageKey, data, box)
		return viewerImageMsg{seq: seq, img: img, err: err, box: box, open: open}
	}
}

func (m *Model) acceptViewerImage(msg viewerImageMsg) tea.Cmd {
	vw := m.viewer
	if vw == nil || vw.imgSeq != msg.seq || vw.loadID != msg.open {
		return nil
	}
	vw.imgPreparing = false
	if !m.graphics.Supported() {
		return nil
	}
	if msg.box != m.viewerImageBox() {
		// Resized while preparing: prepare once more for the latest box.
		return m.syncViewerImage()
	}
	if msg.err != nil {
		vw.imgErr = safe(singleLine(msg.err.Error()))
		return nil
	}
	img := msg.img
	vw.imgPrep = &img
	if vw.att.Width == 0 || vw.att.Height == 0 {
		vw.att.Width, vw.att.Height = img.Width, img.Height
	}
	return m.imgs().Show(img)
}

// releaseViewerImage forgets the viewer's image once it is no longer shown.
func (m *Model) releaseViewerImage() tea.Cmd { return m.imgs().Release(viewerImageKey) }

// viewerImageGeometry returns the image block's size within body, the
// placeholder id when the image is ready, and whether it is still loading.
func (m *Model) viewerImageGeometry(body shell.Rect) (cols, rows, id int, loading bool) {
	vw := m.viewer
	a := vw.att
	cw, ch := m.graphics.CellPixels()
	box := graphicsBox{MaxCols: body.W, MaxRows: body.H, CellW: cw, CellH: ch}
	ready := false
	if m.graphics.Supported() && vw.imgPrep != nil && vw.imgBox == box {
		cols, rows = vw.imgPrep.Cols, vw.imgPrep.Rows
		id, ready = m.imgs().Ready(viewerImageKey)
	}
	if !ready {
		if a.Width > 0 && a.Height > 0 {
			cols, rows = graphicsFit(a.Width, a.Height, box)
		}
		// The fallback needs room for its name and metadata lines.
		cols = min(body.W, max(cols, 32))
		rows = min(body.H, max(rows, 4))
		id = 0
	}
	loading = !ready && vw.imgErr == "" && (vw.loading || m.graphics.Supported() && (vw.imgPreparing || vw.imgPrep != nil))
	return cols, rows, id, loading
}

// renderViewerImage draws the image (or its styled fallback) centred in the
// viewer body on the dialog background.
func (m *Model) renderViewerImage(f *frame, body shell.Rect) {
	if body.W <= 0 || body.H <= 0 {
		return
	}
	p := m.colors()
	vw := m.viewer
	cols, rows, id, loading := m.viewerImageGeometry(body)
	x := body.X + (body.W-cols)/2
	y := body.Y + (body.H-rows)/2
	var lines []string
	if id > 0 {
		for _, row := range graphicsPlaceholderRows(id, cols, rows) {
			lines = append(lines, style(p.text, p.input).Render(row))
		}
	} else {
		lines = graphicsFallbackRows(p, m.attachmentKindIcon("image"), m.viewerImageMeta(), cols, rows, loading)
	}
	for i, line := range lines {
		f.put(shell.Rect{X: x, Y: y + i, W: cols, H: 1}, line)
	}
	if vw.imgErr != "" && y+rows < body.Y+body.H {
		f.text(body.X, body.Y+body.H-1, body.W, "Preview unavailable · "+vw.imgErr, p.muted, p.input)
	}
}

func (m *Model) viewerImageMeta() graphicsMeta {
	a := m.viewer.att
	meta := graphicsMeta{Name: singleLine(a.Name), MediaType: singleLine(a.MediaType), Width: a.Width, Height: a.Height, Bytes: a.Size}
	if meta.MediaType == "" {
		meta.MediaType = strings.TrimPrefix(strings.ToLower(filepath.Ext(a.Name)), ".")
	}
	return meta
}

// --- Composer thumbnails -------------------------------------------------------

// thumbKey is a draft attachment's thumbnail identity, or "" when it is not
// an image the client can load.
func thumbKey(a protocol.Attachment) string {
	switch {
	case a.ArtifactID != "" && imageAttachment(a):
		return "thumb:" + a.ArtifactID
	case a.Kind == "copied-file" && a.ArtifactID == "" && a.Source != "" && imageName(a.Name):
		return "thumb:file:" + a.Source
	}
	return ""
}

func imageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// thumbnailStrip reports whether the composer strip shows image thumbnails:
// graphics are confirmed, the layout has room and the draft has an image.
func (m *Model) thumbnailStrip() bool {
	if !m.graphics.Supported() || m.compact() || !m.hasComposer() || m.settingsPage != "" {
		return false
	}
	for _, a := range m.viewState().Attachments {
		if thumbKey(a) != "" {
			return true
		}
	}
	return false
}

// syncThumbnails starts loads for newly shown draft images and releases
// thumbnails that left the strip. It runs after every Update and is cheap
// when nothing changed.
func (m *Model) syncThumbnails() tea.Cmd {
	want := map[string]protocol.Attachment{}
	if m.thumbnailStrip() {
		for _, a := range m.viewState().Attachments {
			if k := thumbKey(a); k != "" {
				want[k] = a
			}
		}
	}
	var cmds []tea.Cmd
	for key := range m.thumbs {
		if _, ok := want[key]; !ok {
			cmds = append(cmds, m.imgs().Release(key))
			delete(m.thumbs, key)
		}
	}
	for key, a := range want {
		if m.thumbs[key] == nil {
			cmds = append(cmds, m.loadThumbnail(key, a))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) loadThumbnail(key string, a protocol.Attachment) tea.Cmd {
	if m.thumbs == nil {
		m.thumbs = map[string]*thumbnail{}
	}
	m.graphicsSeq++
	cw, ch := m.graphics.CellPixels()
	if img, ok := m.thumbCache.get(thumbCacheKey(key, cw, ch)); ok {
		// Switching back to a draft reuses the prepared PNG: no fetch or decode.
		m.thumbs[key] = &thumbnail{seq: m.graphicsSeq, prep: &img}
		return m.imgs().Show(img)
	}
	t := &thumbnail{seq: m.graphicsSeq, loading: true}
	m.thumbs[key] = t
	api := m.artifactClient()
	ctx := m.ctx
	box := graphicsBox{MaxCols: thumbCols, MaxRows: thumbRows, CellW: cw, CellH: ch}
	seq := t.seq
	return func() tea.Msg {
		var data []byte
		var err error
		if a.ArtifactID != "" {
			if api == nil {
				return thumbnailMsg{key: key, seq: seq, err: errors.New("not connected")}
			}
			c, cancel := context.WithTimeout(ctx, previewTimeout)
			data, _, err = api.FetchArtifact(c, a.ArtifactID, graphicsByteLimit)
			cancel()
		} else {
			c, cancel := context.WithTimeout(ctx, localReadTimeout)
			data, err = readLocalFile(c, a.Source, graphicsByteLimit)
			cancel()
		}
		if err != nil {
			return thumbnailMsg{key: key, seq: seq, err: err}
		}
		img, err := prepareGraphics(key, data, box)
		return thumbnailMsg{key: key, seq: seq, img: img, err: err}
	}
}

func (m *Model) acceptThumbnail(msg thumbnailMsg) tea.Cmd {
	t := m.thumbs[msg.key]
	if t == nil || t.seq != msg.seq || !m.graphics.Supported() {
		return nil
	}
	t.loading = false
	if msg.err != nil {
		t.failed = true
		return nil
	}
	img := msg.img
	t.prep = &img
	cw, ch := m.graphics.CellPixels()
	m.thumbCache.put(thumbCacheKey(msg.key, cw, ch), img)
	return m.imgs().Show(img)
}

// Prepared thumbnails are kept in a small LRU so switching drafts does not
// refetch and re-decode them. Kitty ids are still released on leave.
const (
	thumbCacheEntries = 16
	thumbCacheBytes   = 32 << 20
)

type thumbCache struct {
	entries map[string]*thumbCacheEntry
	clock   uint64
	bytes   int
}

type thumbCacheEntry struct {
	img  graphicsPrepared
	used uint64
}

func thumbCacheKey(key string, cw, ch int) string {
	return fmt.Sprintf("%s@%dx%d", key, cw, ch)
}

func (c *thumbCache) get(key string) (graphicsPrepared, bool) {
	e := c.entries[key]
	if e == nil {
		return graphicsPrepared{}, false
	}
	c.clock++
	e.used = c.clock
	return e.img, true
}

func (c *thumbCache) put(key string, img graphicsPrepared) {
	if len(img.PNG) > thumbCacheBytes {
		return
	}
	if c.entries == nil {
		c.entries = map[string]*thumbCacheEntry{}
	}
	if old := c.entries[key]; old != nil {
		c.bytes -= len(old.img.PNG)
	}
	c.clock++
	c.entries[key] = &thumbCacheEntry{img: img, used: c.clock}
	c.bytes += len(img.PNG)
	for len(c.entries) > thumbCacheEntries || c.bytes > thumbCacheBytes {
		var lruKey string
		var lru *thumbCacheEntry
		for k, e := range c.entries {
			if lru == nil || e.used < lru.used {
				lruKey, lru = k, e
			}
		}
		c.bytes -= len(lru.img.PNG)
		delete(c.entries, lruKey)
	}
}

// thumbnailRows renders one attachment's thumbnail block of exactly
// thumbCols×thumbRows cells on bg: placeholders when ready, else a fallback.
func (m *Model) thumbnailRows(a protocol.Attachment, bg string) []string {
	p := m.colors()
	key := thumbKey(a)
	t := m.thumbs[key]
	if t != nil && t.prep != nil {
		if id, ok := m.imgs().Ready(key); ok {
			cols, rows := t.prep.Cols, t.prep.Rows
			left := (thumbCols - cols) / 2
			top := (thumbRows - rows) / 2
			pad := style(p.text, bg)
			out := make([]string, thumbRows)
			placeholders := graphicsPlaceholderRows(id, cols, rows)
			for i := range out {
				if i < top || i >= top+rows {
					out[i] = pad.Render(strings.Repeat(" ", thumbCols))
					continue
				}
				out[i] = pad.Render(strings.Repeat(" ", left)+placeholders[i-top]) + pad.Render(strings.Repeat(" ", thumbCols-left-cols))
			}
			return out
		}
	}
	loading := t == nil || !t.failed
	return graphicsFallbackRows(p, m.attachmentKindIcon("image"), graphicsMeta{Name: singleLine(a.Name)}, thumbCols, thumbRows, loading)
}

// --- Artifacts -----------------------------------------------------------------

// deleteStagedArtifact frees a staged artifact the draft no longer uses.
// Already accepted ("accepted") and unknown ("not_found") ids are benign, and
// any failure only leaves the artifact to the server's staged expiry; it
// never blocks the UI.
func (m *Model) deleteStagedArtifact(id string) tea.Cmd {
	api := m.artifactClient()
	if api == nil || id == "" {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, previewTimeout)
		defer cancel()
		_ = api.DeleteArtifact(c, id)
		return nil
	}
}

// artifactErrorText is an honest, sanitized reason for an artifact transfer
// failure.
func artifactErrorText(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "busy":
			return "the server is busy with other transfers · try again shortly"
		case "unavailable", "capacity", "not_found":
			if pe.Message != "" {
				return safe(singleLine(pe.Message))
			}
		}
	}
	return safe(singleLine(err.Error()))
}
