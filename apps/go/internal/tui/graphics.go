// Kitty graphics through Unicode placeholders (ADR 0018).
//
// This file and graphics_query.go are a self-contained library; Model wiring
// lives in graphics_model.go. The wiring follows:
//
//  1. Startup: m.graphics = newGraphicsProbe(os.Getenv) beside
//     terminalColorOptions, and m.images = newGraphicsRegistry(seed) with a
//     per-process seed (randomises the id base against other programs).
//  2. On tea.TerminalVersionMsg (the XTVERSION reply the colour probe already
//     requests): cmd := m.graphics.Start(msg.Name, m.colorProfile). On colour
//     profile changes call m.graphics.ProfileChanged(profile).
//  3. In Update, before other handling: if m.graphics.Update(msg) { return }
//     (it swallows uv.UnknownApcEvent kitty replies, uv.CellSizeEvent, the
//     DA1 reply while querying and graphicsProbeTimeoutMsg). Likewise
//     if cmd, ok := m.images.Update(msg); ok { return cmd }.
//  4. When an image should be shown in a box of maxCols×maxRows cells:
//     prepareGraphicsCmd(key, data, graphicsBox{...cell pixels from
//     m.graphics.CellPixels()}) runs off the render path and returns
//     graphicsPreparedMsg. On success, cmd := m.images.Show(msg.Image).
//     Keep the prepared value (or call Show again) whenever it is displayed so
//     LRU order stays current.
//  5. Rendering (pure): if m.graphics.Supported() and
//     id, ok := m.images.Ready(key) then graphicsPlaceholderRows(id, cols,
//     rows) else graphicsFallbackRows(...) with the same cols×rows (loading
//     while preparation or transmission is pending). Geometry never shifts.
//  6. When an image leaves the view permanently (viewer closed, attachment
//     removed) after the frame no longer shows it: cmd := m.images.Release(key).
//     Before tea.Suspend and before tea.Quit: tea.Sequence(
//     tea.Raw(m.images.DeleteAllSequence()), tea.Suspend). After resume,
//     Show re-transmits lazily; re-query only if the terminal may differ.
//
// Placeholder cells are ordinary text to the renderer (one cell each), so
// clipping, diffing and scrolling need no graphics special cases. Only
// transmit/place/delete escapes bypass the cell buffer via tea.Raw.
package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decode support
	_ "image/jpeg"
	"image/png"
	"math"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// Limits mirror the server's artifact limits (internal/server/artifacts.go).
const (
	graphicsByteLimit  = 16 << 20
	graphicsPixelLimit = 40_000_000
	// graphicsChunkSize is the kitty maximum base64 payload per chunk.
	graphicsChunkSize = 4096
	// graphicsMaxSide caps the transmitted side when cell pixels are unknown.
	graphicsMaxSide = 2048
)

var (
	errGraphicsTooLarge   = errors.New("image exceeds 16 MiB")
	errGraphicsTooManyPix = errors.New("image exceeds 40 megapixels")
	errGraphicsFormat     = errors.New("unsupported or corrupt image")
)

// graphicsBox is the target placement area and the terminal cell pixel size
// (0 when unknown, which selects a 1:2 width:height cell aspect).
type graphicsBox struct{ MaxCols, MaxRows, CellW, CellH int }

// graphicsPrepared is a PNG ready to transmit and its fitted placement.
type graphicsPrepared struct {
	Key           string
	PNG           []byte
	Width, Height int // pixel size of the source image
	Cols, Rows    int // placement cells, preserving aspect ratio
}

// graphicsPreparedMsg reports prepareGraphicsCmd's result for Key.
type graphicsPreparedMsg struct {
	Key   string
	Image graphicsPrepared
	Err   error
}

// prepareGraphicsCmd bounds, decodes and re-encodes data off the render path.
func prepareGraphicsCmd(key string, data []byte, box graphicsBox) tea.Cmd {
	return func() tea.Msg {
		img, err := prepareGraphics(key, data, box)
		return graphicsPreparedMsg{Key: key, Image: img, Err: err}
	}
}

func prepareGraphics(key string, data []byte, box graphicsBox) (graphicsPrepared, error) {
	if len(data) > graphicsByteLimit {
		return graphicsPrepared{}, errGraphicsTooLarge
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return graphicsPrepared{}, errGraphicsFormat
	}
	if int64(cfg.Width)*int64(cfg.Height) > graphicsPixelLimit {
		return graphicsPrepared{}, errGraphicsTooManyPix
	}
	cols, rows := graphicsFit(cfg.Width, cfg.Height, box)
	out := graphicsPrepared{Key: key, Width: cfg.Width, Height: cfg.Height, Cols: cols, Rows: rows}
	maxW, maxH := graphicsMaxSide, graphicsMaxSide
	if box.CellW > 0 && box.CellH > 0 {
		maxW, maxH = max(1, cols*box.CellW), max(1, rows*box.CellH)
	}
	if format == "png" && cfg.Width <= maxW && cfg.Height <= maxH {
		// Validate fully before passing the original through.
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			return graphicsPrepared{}, errGraphicsFormat
		}
		out.PNG = data
		return out, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return graphicsPrepared{}, errGraphicsFormat
	}
	src = graphicsDownscale(src, maxW, maxH)
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		return graphicsPrepared{}, fmt.Errorf("encode preview: %w", err)
	}
	out.PNG = buf.Bytes()
	return out, nil
}

// graphicsFit returns the largest cols×rows within the box that preserves the
// image aspect ratio, never exceeding the image's natural cell size when the
// cell pixel size is known. Unknown cell size assumes 1:2 (width:height).
func graphicsFit(w, h int, box graphicsBox) (cols, rows int) {
	if w <= 0 || h <= 0 || box.MaxCols <= 0 || box.MaxRows <= 0 {
		return 0, 0
	}
	cw, ch := float64(box.CellW), float64(box.CellH)
	known := cw > 0 && ch > 0
	if !known {
		cw, ch = 1, 2
	}
	colsF, rowsF := float64(w)/cw, float64(h)/ch
	scale := math.Min(float64(box.MaxCols)/colsF, float64(box.MaxRows)/rowsF)
	if known && scale > 1 {
		scale = 1
	}
	cols = min(box.MaxCols, max(1, int(math.Round(colsF*scale))))
	rows = min(box.MaxRows, max(1, int(math.Round(rowsF*scale))))
	return cols, rows
}

// graphicsDownscale box-filters src to fit within maxW×maxH (stdlib only).
func graphicsDownscale(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return src
	}
	s := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	nw, nh := max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := range nh {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+max((y+1)*h/nh, y*h/nh+1)
		for x := range nw {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			if a == 0 {
				continue
			}
			// Un-premultiply the averaged colour for NRGBA.
			dst.Pix[i+0] = uint8(r * 0xff / a)
			dst.Pix[i+1] = uint8(g * 0xff / a)
			dst.Pix[i+2] = uint8(bl * 0xff / a)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}

// --- Registry --------------------------------------------------------------

type graphicsEntry struct {
	key                 string
	id                  int
	png                 []byte
	cols, rows          int
	transmitted, placed bool // placed: virtual placement matches cols×rows
	used                uint64
	queued              bool
}

// graphicsEncodedMsg carries a fully encoded escape sequence built off the
// Update path. gen drops encodings made before DeleteAllSequence.
type graphicsEncodedMsg struct {
	key, seq string
	id, gen  int
	cols     int
	rows     int
}

// graphicsWrittenMsg follows the tea.Raw write of the in-flight transmit.
type graphicsWrittenMsg struct{ gen int }

// Image ids are 16–255: a 256-colour index below 16 may be re-emitted by the
// renderer as a basic 30–37/90–97 SGR, which would break the placeholder id.
const (
	graphicsFirstID = 16
	graphicsIDCount = 256 - graphicsFirstID
)

// graphicsRegistry owns client image ids 16–255, LRU-reused. At most one
// transmit is in flight, bounding the bytes interleaved with frames.
type graphicsRegistry struct {
	entries  map[string]*graphicsEntry
	byID     [256]*graphicsEntry
	base     int
	clock    uint64
	gen      int
	queue    []*graphicsEntry
	inFlight bool
	pending  []string // deletes of evicted ids, emitted before the next transmit
	// sent marks every id whose transmit was ever dispatched this session,
	// including one still being written; exit deletes all of them.
	sent   [256]bool
	closed bool // after Close: nothing more is transmitted
}

func newGraphicsRegistry(seed int) *graphicsRegistry {
	return &graphicsRegistry{entries: map[string]*graphicsEntry{}, base: ((seed%graphicsIDCount)+graphicsIDCount)%graphicsIDCount + graphicsFirstID}
}

// Show registers (or refreshes) a prepared image and returns a command that
// transmits and/or places it when needed. It never retransmits a
// transmitted image; a geometry change only re-places it.
func (r *graphicsRegistry) Show(img graphicsPrepared) tea.Cmd {
	if r.closed || img.Key == "" || len(img.PNG) == 0 || img.Cols <= 0 || img.Rows <= 0 {
		return nil
	}
	r.clock++
	e := r.entries[img.Key]
	if e == nil {
		e = &graphicsEntry{key: img.Key, id: r.allocate()}
		r.entries[img.Key] = e
		r.byID[e.id] = e
	}
	e.used = r.clock
	if !bytes.Equal(e.png, img.PNG) {
		e.png, e.transmitted, e.placed = img.PNG, false, false
	}
	if e.cols != img.Cols || e.rows != img.Rows {
		e.cols, e.rows, e.placed = img.Cols, img.Rows, false
	}
	if e.transmitted && e.placed {
		return nil
	}
	if !e.queued {
		e.queued = true
		r.queue = append(r.queue, e)
	}
	return r.pump()
}

// Ready returns the id to render placeholders with once the image has been
// transmitted and placed at its current geometry.
func (r *graphicsRegistry) Ready(key string) (int, bool) {
	e := r.entries[key]
	if e == nil || !e.transmitted || !e.placed {
		return 0, false
	}
	return e.id, true
}

// Release forgets key and deletes its terminal image (a=d,d=I).
func (r *graphicsRegistry) Release(key string) tea.Cmd {
	e := r.entries[key]
	if e == nil {
		return nil
	}
	r.forget(e)
	if !e.transmitted {
		return nil
	}
	return tea.Raw(graphicsDeleteSequence(e.id))
}

// DeleteAllSequence deletes every owned image (never d=A, which would
// remove other programs' images) and marks entries untransmitted so a later
// Show retransmits. Use before suspend and exit.
func (r *graphicsRegistry) DeleteAllSequence() string {
	var b strings.Builder
	for _, s := range r.pending {
		b.WriteString(s)
	}
	r.pending = nil
	for id := 1; id < len(r.byID); id++ {
		if e := r.byID[id]; e != nil && e.transmitted {
			b.WriteString(graphicsDeleteSequence(id))
		}
		if e := r.byID[id]; e != nil {
			e.transmitted, e.placed, e.queued = false, false, false
		}
	}
	r.gen++
	r.queue, r.inFlight = nil, false
	return b.String()
}

// Close stops all further transmits and returns deletes for every id that
// may have been transmitted this session (current, evicted, released or
// still being written). Deleting an id twice is harmless. Use at exit.
func (r *graphicsRegistry) Close() string {
	r.closed = true
	_ = r.DeleteAllSequence()
	var b strings.Builder
	for id := graphicsFirstID; id < len(r.sent); id++ {
		if r.sent[id] {
			b.WriteString(graphicsDeleteSequence(id))
		}
	}
	return b.String()
}

// Update handles the registry's own messages.
func (r *graphicsRegistry) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case graphicsEncodedMsg:
		if msg.gen != r.gen || r.closed {
			return nil, true
		}
		e := r.entries[msg.key]
		if e == nil || e.id != msg.id {
			// Released or evicted while encoding: nothing was written.
			r.inFlight = false
			return r.pump(), true
		}
		e.transmitted, e.placed = true, e.cols == msg.cols && e.rows == msg.rows
		gen := r.gen
		return tea.Sequence(tea.Raw(msg.seq), func() tea.Msg { return graphicsWrittenMsg{gen: gen} }), true
	case graphicsWrittenMsg:
		if msg.gen == r.gen {
			r.inFlight = false
			return r.pump(), true
		}
		return nil, true
	}
	return nil, false
}

func (r *graphicsRegistry) pump() tea.Cmd {
	if r.inFlight {
		return nil
	}
	for len(r.queue) > 0 {
		e := r.queue[0]
		r.queue = r.queue[1:]
		e.queued = false
		if r.entries[e.key] != e || (e.transmitted && e.placed) {
			continue
		}
		prefix := strings.Join(r.pending, "")
		r.pending = nil
		r.inFlight = true
		r.sent[e.id] = true
		key, id, gen, cols, rows, data, send := e.key, e.id, r.gen, e.cols, e.rows, e.png, !e.transmitted
		return func() tea.Msg {
			seq := prefix
			if send {
				seq += graphicsTransmitSequence(id, data)
			} else {
				seq += graphicsUnplaceSequence(id)
			}
			seq += graphicsPlaceSequence(id, cols, rows)
			return graphicsEncodedMsg{key: key, seq: seq, id: id, gen: gen, cols: cols, rows: rows}
		}
	}
	return nil
}

func (r *graphicsRegistry) allocate() int {
	for i := range graphicsIDCount {
		id := (r.base-graphicsFirstID+i)%graphicsIDCount + graphicsFirstID
		if r.byID[id] == nil {
			return id
		}
	}
	var lru *graphicsEntry
	for id := graphicsFirstID; id < len(r.byID); id++ {
		if e := r.byID[id]; lru == nil || e.used < lru.used {
			lru = e
		}
	}
	r.forget(lru)
	if lru.transmitted {
		r.pending = append(r.pending, graphicsDeleteSequence(lru.id))
	}
	return lru.id
}

func (r *graphicsRegistry) forget(e *graphicsEntry) {
	delete(r.entries, e.key)
	if r.byID[e.id] == e {
		r.byID[e.id] = nil
	}
}

// --- Escape sequences --------------------------------------------------------

// graphicsTransmitSequence sends PNG data for id (a=t,f=100,q=2), chunked at
// ≤4096 base64 bytes. Only the first chunk carries the control keys.
func graphicsTransmitSequence(id int, data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	first := true
	for first || len(enc) > 0 {
		n := min(len(enc), graphicsChunkSize)
		chunk := enc[:n]
		enc = enc[n:]
		more := "0"
		if len(enc) > 0 {
			more = "1"
		}
		var opts []string
		if first {
			opts = []string{"a=t", "f=" + strconv.Itoa(kitty.PNG), "t=d", "i=" + strconv.Itoa(id), "q=2"}
		} else {
			opts = []string{"q=2"}
		}
		if more == "1" || !first {
			opts = append(opts, "m="+more)
		}
		b.WriteString(ansi.KittyGraphics([]byte(chunk), opts...))
		first = false
	}
	return b.String()
}

// graphicsPlaceSequence creates the virtual (Unicode placeholder) placement.
func graphicsPlaceSequence(id, cols, rows int) string {
	return ansi.KittyGraphics(nil, "a=p", "U=1", "i="+strconv.Itoa(id), "c="+strconv.Itoa(cols), "r="+strconv.Itoa(rows), "q=2")
}

// graphicsUnplaceSequence removes id's placements but keeps its data, so a
// resize re-places without retransmitting.
func graphicsUnplaceSequence(id int) string {
	return ansi.KittyGraphics(nil, "a=d", "d=i", "i="+strconv.Itoa(id), "q=2")
}

// graphicsDeleteSequence deletes id's placements and frees its data.
func graphicsDeleteSequence(id int) string {
	return ansi.KittyGraphics(nil, "a=d", "d=I", "i="+strconv.Itoa(id), "q=2")
}

// --- Rendering (pure) --------------------------------------------------------

// graphicsPlaceholderRows returns rows of cols placeholder cells for id. Each
// row sets the 256-colour foreground to the id and resets only the
// foreground, so callers may wrap it in a background style.
func graphicsPlaceholderRows(id, cols, rows int) []string {
	if id < graphicsFirstID || id > 255 || cols <= 0 || rows <= 0 {
		return nil
	}
	fg := "\x1b[38;5;" + strconv.Itoa(id) + "m"
	out := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		b.WriteString(fg)
		for c := range cols {
			b.WriteRune(kitty.Placeholder)
			b.WriteRune(kitty.Diacritic(r))
			b.WriteRune(kitty.Diacritic(c))
		}
		b.WriteString("\x1b[39m")
		out[r] = b.String()
	}
	return out
}

// graphicsMeta describes an image for the fallback block. Zero values are
// omitted.
type graphicsMeta struct {
	Name, MediaType string
	Width, Height   int
	Bytes           int64
}

// graphicsFallbackRows draws a panel-style metadata block of exactly
// cols×rows cells: the image glyph and name, media type, pixel size and
// byte size on the panel background, vertically centred, truncated per
// line. loading replaces the details with a muted "Loading preview…" line.
func graphicsFallbackRows(p palette, glyph string, meta graphicsMeta, cols, rows int, loading bool) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	name := safe(meta.Name)
	if name == "" {
		name = "Image"
	}
	type line struct{ text, fg string }
	lines := []line{{strings.TrimSpace(safe(glyph) + " " + name), p.text}}
	if loading {
		lines = append(lines, line{"Loading preview…", p.muted})
	} else {
		var details []string
		if meta.MediaType != "" {
			details = append(details, safe(meta.MediaType))
		}
		if meta.Width > 0 && meta.Height > 0 {
			details = append(details, fmt.Sprintf("%d×%d", meta.Width, meta.Height))
		}
		if meta.Bytes > 0 {
			details = append(details, graphicsByteSize(meta.Bytes))
		}
		// One detail per line when there is room, else joined.
		if len(lines)+len(details) <= rows {
			for _, d := range details {
				lines = append(lines, line{d, p.muted})
			}
		} else if len(details) > 0 {
			lines = append(lines, line{strings.Join(details, " · "), p.muted})
		}
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	top := (rows - len(lines)) / 2
	blank := style(p.text, p.panel).Render(strings.Repeat(" ", cols))
	out := make([]string, rows)
	inner := max(0, cols-2)
	for i := range out {
		li := i - top
		if li < 0 || li >= len(lines) || inner == 0 {
			out[i] = blank
			continue
		}
		text := ansi.Truncate(lines[li].text, inner, "…")
		pad := cols - ansi.StringWidth(text)
		left := 1
		out[i] = style(lines[li].fg, p.panel).Render(strings.Repeat(" ", left) + text + strings.Repeat(" ", pad-left))
	}
	return out
}

func graphicsByteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
