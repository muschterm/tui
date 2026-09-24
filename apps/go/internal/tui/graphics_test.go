package tui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func startedProbe(t *testing.T) graphicsProbe {
	t.Helper()
	p := newGraphicsProbe(envOf(nil))
	if cmd := p.Start("kitty(0.36.1)", colorprofile.TrueColor); cmd == nil {
		t.Fatal("query not sent")
	}
	if p.state != graphicsQuerying {
		t.Fatalf("state %v", p.state)
	}
	return p
}

func TestGraphicsProbeSupported(t *testing.T) {
	p := startedProbe(t)
	if !p.Update(uv.CellSizeEvent{Width: 10, Height: 21}) {
		t.Fatal("cell size not handled")
	}
	if !p.Update(uv.UnknownApcEvent("\x1b_Gi=31;OK\x1b\\")) || !p.Supported() {
		t.Fatalf("not supported: %+v", p)
	}
	// DA1 after the reply no longer belongs to the probe.
	if p.Update(uv.PrimaryDeviceAttributesEvent{62}) || !p.Supported() {
		t.Fatal("DA1 after OK changed state")
	}
	if w, h := p.CellPixels(); w != 10 || h != 21 {
		t.Fatalf("cell %d×%d", w, h)
	}
}

func TestGraphicsProbeUnsupportedTimeoutError(t *testing.T) {
	p := startedProbe(t)
	p.Update(uv.PrimaryDeviceAttributesEvent{62, 22})
	if p.Supported() || p.state != graphicsUnavailable {
		t.Fatal("DA1 without reply must be unsupported")
	}

	p = startedProbe(t)
	p.Update(graphicsProbeTimeoutMsg{seq: p.seq - 1}) // stale
	if p.state != graphicsQuerying {
		t.Fatal("stale timeout applied")
	}
	p.Update(graphicsProbeTimeoutMsg{seq: p.seq})
	if p.state != graphicsUnavailable || !strings.Contains(p.reason, "timed out") {
		t.Fatalf("timeout: %+v", p)
	}
	// A late OK after timeout does not upgrade.
	p.Update(uv.UnknownApcEvent("\x1b_Gi=31;OK\x1b\\"))
	if p.Supported() {
		t.Fatal("late OK upgraded")
	}

	p = startedProbe(t)
	p.Update(uv.UnknownApcEvent("\x1b_Gi=31;ENOTSUPPORTED:no\x1b[31m\x1b\\"))
	if p.Supported() || !strings.Contains(p.reason, "ENOTSUPPORTED") || strings.Contains(p.reason, "\x1b") {
		t.Fatalf("error reply: %q", p.reason)
	}

	p = startedProbe(t)
	if p.Update(uv.UnknownApcEvent("\x1b_Gi=7;OK\x1b\\")) || p.state != graphicsQuerying {
		t.Fatal("foreign image id accepted")
	}
}

func TestGraphicsProbeGates(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		term    string
		profile colorprofile.Profile
	}{
		{"no color", map[string]string{"NO_COLOR": "0"}, "kitty(0.36)", colorprofile.TrueColor},
		{"opt out", map[string]string{graphicsOptOutEnv: "Off"}, "kitty(0.36)", colorprofile.TrueColor},
		{"tmux", map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"}, "kitty(0.36)", colorprofile.TrueColor},
		{"herdr", map[string]string{"HERDR_ENV": "1"}, "ghostty 1.2", colorprofile.TrueColor},
		{"low profile", nil, "kitty(0.36)", colorprofile.ANSI},
		{"unknown terminal", map[string]string{"TERM": "xterm-kitty"}, "", colorprofile.TrueColor},
		{"iterm", nil, "iTerm2 3.5", colorprofile.TrueColor},
		{"foot", nil, "foot(1.20)", colorprofile.TrueColor},
	}
	for _, c := range cases {
		p := newGraphicsProbe(envOf(c.env))
		if cmd := p.Start(c.term, c.profile); cmd != nil || p.state != graphicsUnavailable || p.reason == "" {
			t.Errorf("%s: cmd=%v state=%v reason=%q", c.name, cmd != nil, p.state, p.reason)
		}
	}
	p := newGraphicsProbe(envOf(nil))
	if p.Start("ghostty 1.2.0", colorprofile.ANSI256) == nil {
		t.Fatal("ghostty ANSI256 should query")
	}
	if p.Start("ghostty 1.2.0", colorprofile.ANSI256) != nil {
		t.Fatal("second Start re-queried")
	}
	p.Update(uv.UnknownApcEvent("\x1b_Gi=31;OK\x1b\\"))
	p.ProfileChanged(colorprofile.ANSI)
	if p.Supported() {
		t.Fatal("profile drop kept graphics")
	}
}

func TestGraphicsQuerySequence(t *testing.T) {
	s := graphicsQuerySequence()
	for _, want := range []string{"\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\", "\x1b[16t", "\x1b[c"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	if strings.Index(s, "\x1b[c") < strings.Index(s, "a=q") {
		t.Error("DA1 must terminate the query")
	}
}

func TestGraphicsFit(t *testing.T) {
	cases := []struct {
		w, h       int
		box        graphicsBox
		cols, rows int
	}{
		{800, 400, graphicsBox{40, 20, 10, 20}, 40, 10}, // width-bound
		{400, 800, graphicsBox{40, 20, 10, 20}, 20, 20}, // height-bound
		{100, 100, graphicsBox{40, 20, 10, 20}, 10, 5},  // natural size, no upscale
		{100, 100, graphicsBox{40, 20, 0, 0}, 40, 20},   // 1:2 fallback fills: 40 cols ≈ 2·20 rows
		{1000, 100, graphicsBox{40, 20, 0, 0}, 40, 2},   // wide banner fallback
		{1, 10000, graphicsBox{40, 20, 10, 20}, 1, 20},  // never zero
		{0, 10, graphicsBox{40, 20, 10, 20}, 0, 0},      // invalid
		{3000, 2000, graphicsBox{4, 2, 8, 16}, 4, 1},    // thumbnail
	}
	for _, c := range cases {
		cols, rows := graphicsFit(c.w, c.h, c.box)
		if cols != c.cols || rows != c.rows {
			t.Errorf("%d×%d in %+v = %d×%d, want %d×%d", c.w, c.h, c.box, cols, rows, c.cols, c.rows)
		}
	}
}

func testImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	return img
}

func TestPrepareGraphics(t *testing.T) {
	var pngBuf, jpgBuf bytes.Buffer
	_ = png.Encode(&pngBuf, testImage(64, 32))
	_ = jpeg.Encode(&jpgBuf, testImage(64, 32), nil)
	box := graphicsBox{MaxCols: 20, MaxRows: 10, CellW: 8, CellH: 16}

	got, err := prepareGraphics("a", pngBuf.Bytes(), box)
	if err != nil || !bytes.Equal(got.PNG, pngBuf.Bytes()) || got.Cols != 8 || got.Rows != 2 || got.Width != 64 {
		t.Fatalf("png passthrough: %+v %v", got, err)
	}
	got, err = prepareGraphics("b", jpgBuf.Bytes(), box)
	if err != nil || !bytes.HasPrefix(got.PNG, []byte("\x89PNG")) {
		t.Fatalf("jpeg re-encode: %v", err)
	}
	// Downscale when the placement box is smaller than the image.
	got, err = prepareGraphics("c", pngBuf.Bytes(), graphicsBox{MaxCols: 2, MaxRows: 2, CellW: 8, CellH: 16})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := png.DecodeConfig(bytes.NewReader(got.PNG)); cfg.Width > got.Cols*8 || cfg.Height > got.Rows*16 {
		t.Fatalf("not downscaled: %d×%d for %d×%d cells", cfg.Width, cfg.Height, got.Cols, got.Rows)
	}

	msg := prepareGraphicsCmd("k", []byte("not an image"), box)().(graphicsPreparedMsg)
	if msg.Key != "k" || !errors.Is(msg.Err, errGraphicsFormat) {
		t.Fatalf("corrupt: %+v", msg)
	}
	truncated := pngBuf.Bytes()[:len(pngBuf.Bytes())/2]
	if _, err := prepareGraphics("t", truncated, box); !errors.Is(err, errGraphicsFormat) {
		t.Fatalf("truncated png: %v", err)
	}
	if _, err := prepareGraphics("x", make([]byte, graphicsByteLimit+1), box); !errors.Is(err, errGraphicsTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	// A header claiming 10000×5000 (50 MP) is rejected before decode.
	var big bytes.Buffer
	_ = png.Encode(&big, testImage(1, 1))
	hdr := big.Bytes()
	hdr = append([]byte{}, hdr...)
	putBE := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v) }
	putBE(hdr[16:20], 10000)
	putBE(hdr[20:24], 5000)
	putBE(hdr[29:33], crc32.ChecksumIEEE(hdr[12:29]))
	if _, err := prepareGraphics("mp", hdr, box); !errors.Is(err, errGraphicsTooManyPix) {
		t.Fatalf("over-MP: %v", err)
	}
}

// runRegistry drives the registry's command chain to completion, returning
// the raw escape output written through tea.Raw.
func runRegistry(t *testing.T, r *graphicsRegistry, cmd tea.Cmd) string {
	t.Helper()
	var out strings.Builder
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.RawMsg:
			out.WriteString(msg.Msg.(string))
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case nil:
		default:
			// tea.Sequence's message type is unexported; unpack by reflection.
			if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
				for i := range v.Len() {
					queue = append(queue, v.Index(i).Interface().(tea.Cmd))
				}
				continue
			}
			next, ok := r.Update(msg)
			if !ok {
				t.Fatalf("unhandled %T", msg)
			}
			queue = append(queue, next)
		}
	}
	return out.String()
}

func TestGraphicsTransmitChunking(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 3*4096) // 16384 base64 bytes → 4 chunks
	s := graphicsTransmitSequence(9, data)
	chunks := strings.Split(strings.TrimSuffix(s, "\x1b\\"), "\x1b\\")
	if len(chunks) != 4 {
		t.Fatalf("%d chunks", len(chunks))
	}
	var payload strings.Builder
	for i, c := range chunks {
		keys, p, _ := strings.Cut(strings.TrimPrefix(c, "\x1b_G"), ";")
		if len(p) > graphicsChunkSize {
			t.Fatalf("chunk %d is %d bytes", i, len(p))
		}
		payload.WriteString(p)
		switch {
		case i == 0 && keys != "a=t,f=100,t=d,i=9,q=2,m=1":
			t.Errorf("first keys %q", keys)
		case i > 0 && i < 3 && keys != "q=2,m=1":
			t.Errorf("middle keys %q", keys)
		case i == 3 && keys != "q=2,m=0":
			t.Errorf("last keys %q", keys)
		}
	}
	if dec, _ := base64.StdEncoding.DecodeString(payload.String()); !bytes.Equal(dec, data) {
		t.Fatal("payload mismatch")
	}
	if s := graphicsTransmitSequence(3, []byte("x")); s != "\x1b_Ga=t,f=100,t=d,i=3,q=2;eA==\x1b\\" {
		t.Fatalf("single chunk %q", s)
	}
}

func TestGraphicsRegistryLifecycle(t *testing.T) {
	r := newGraphicsRegistry(0)
	img := graphicsPrepared{Key: "a", PNG: []byte("png-a"), Cols: 4, Rows: 2}
	cmd := r.Show(img)
	if _, ok := r.Ready("a"); ok {
		t.Fatal("ready before transmit")
	}
	out := runRegistry(t, r, cmd)
	id, ok := r.Ready("a")
	if !ok || id != 16 || !strings.Contains(out, "a=t,f=100,t=d,i=16,q=2") || !strings.HasSuffix(out, "\x1b_Ga=p,U=1,i=16,c=4,r=2,q=2\x1b\\") {
		t.Fatalf("first show id=%d %q", id, out)
	}
	// Same image again: nothing is sent.
	if r.Show(img) != nil {
		t.Fatal("retransmitted unchanged image")
	}
	// Geometry change: re-place only.
	img.Cols = 6
	out = runRegistry(t, r, r.Show(img))
	if strings.Contains(out, "a=t") || !strings.Contains(out, "a=d,d=i,i=16") || !strings.Contains(out, "a=p,U=1,i=16,c=6,r=2") {
		t.Fatalf("re-place %q", out)
	}
	// One transmit in flight: a second image waits for the first write.
	c1 := r.Show(graphicsPrepared{Key: "b", PNG: []byte("b"), Cols: 1, Rows: 1})
	if r.Show(graphicsPrepared{Key: "c", PNG: []byte("c"), Cols: 1, Rows: 1}) != nil {
		t.Fatal("second transmit not queued")
	}
	out = runRegistry(t, r, c1)
	if !strings.Contains(out, "i=17,") || !strings.Contains(out, "i=18,") {
		t.Fatalf("queued transmits %q", out)
	}
	// Release deletes with d=I; unknown key is a no-op.
	if s := runRegistry(t, r, r.Release("b")); s != "\x1b_Ga=d,d=I,i=17,q=2\x1b\\" {
		t.Fatalf("release %q", s)
	}
	if r.Release("b") != nil {
		t.Fatal("double release")
	}
	// Suspend/exit deletes all owned ids, never d=A, and retransmits lazily.
	all := r.DeleteAllSequence()
	if !strings.Contains(all, "d=I,i=16") || !strings.Contains(all, "d=I,i=18") || strings.Contains(all, "d=A") || strings.Contains(all, "d=a") {
		t.Fatalf("delete all %q", all)
	}
	if _, ok := r.Ready("a"); ok {
		t.Fatal("ready after delete all")
	}
	if out := runRegistry(t, r, r.Show(img)); !strings.Contains(out, "a=t,f=100,t=d,i=16") {
		t.Fatalf("lazy retransmit %q", out)
	}
}

func TestGraphicsRegistryStaleEncoding(t *testing.T) {
	r := newGraphicsRegistry(0)
	cmd := r.Show(graphicsPrepared{Key: "a", PNG: []byte("a"), Cols: 1, Rows: 1})
	enc := cmd()
	_ = r.DeleteAllSequence()
	if next, ok := r.Update(enc); !ok || next != nil {
		t.Fatal("stale encoding written")
	}
	if _, ok := r.Ready("a"); ok {
		t.Fatal("stale encoding marked ready")
	}
}

func TestGraphicsRegistryLRUReuse(t *testing.T) {
	r := newGraphicsRegistry(41) // base id 57
	for i := range graphicsIDCount {
		key := string(rune('A' + i))
		runRegistry(t, r, r.Show(graphicsPrepared{Key: key, PNG: []byte(key), Cols: 1, Rows: 1}))
	}
	if id, _ := r.Ready("A"); id != 57 {
		t.Fatalf("base id %d", id)
	}
	// Touch A so B is least recently used.
	r.Show(graphicsPrepared{Key: "A", PNG: []byte("A"), Cols: 1, Rows: 1})
	idB, _ := r.Ready("B")
	out := runRegistry(t, r, r.Show(graphicsPrepared{Key: "new", PNG: []byte("n"), Cols: 1, Rows: 1}))
	idNew, ok := r.Ready("new")
	if !ok || idNew != idB {
		t.Fatalf("reused %d want %d", idNew, idB)
	}
	if _, ok := r.Ready("B"); ok {
		t.Fatal("evicted entry still ready")
	}
	del := "\x1b_Ga=d,d=I,i=" + strconv.Itoa(idB) + ",q=2\x1b\\"
	if !strings.HasPrefix(out, del) {
		t.Fatalf("evicted id not deleted before retransmit: %q", out[:min(len(out), 80)])
	}
}

func TestGraphicsPlaceholderWidthAndColor(t *testing.T) {
	for _, id := range []int{16, 42, 255} {
		rows := graphicsPlaceholderRows(id, 7, 3)
		if len(rows) != 3 {
			t.Fatalf("rows %d", len(rows))
		}
		for _, row := range rows {
			styled := lipgloss.NewStyle().Background(lipgloss.Color("#1e2435")).Render(row)
			for _, w := range []int{ansi.StringWidth(row), ansi.StringWidthWc(row), uniseg.StringWidth(ansi.Strip(row)), lipgloss.Width(styled)} {
				if w != 7 {
					t.Fatalf("id %d width %d", id, w)
				}
			}
			for _, profile := range []colorprofile.Profile{colorprofile.ANSI256, colorprofile.TrueColor} {
				var buf bytes.Buffer
				w := &colorprofile.Writer{Forward: &buf, Profile: profile}
				_, _ = w.WriteString(styled)
				if !strings.Contains(buf.String(), "38;5;"+strconv.Itoa(id)+"m") {
					t.Fatalf("id %d lost under %v: %q", id, profile, buf.String())
				}
				if ansi.StringWidth(buf.String()) != 7 {
					t.Fatal("downsampled width changed")
				}
			}
		}
	}
	if graphicsPlaceholderRows(0, 1, 1) != nil || graphicsPlaceholderRows(15, 1, 1) != nil || graphicsPlaceholderRows(256, 1, 1) != nil {
		t.Fatal("invalid id accepted")
	}
	// Diacritics encode row and column.
	row := []rune(ansi.Strip(graphicsPlaceholderRows(16, 2, 2)[1]))
	if len(row) != 6 || row[1] != 0x030D || row[4] != 0x030D || row[2] != 0x0305 {
		t.Fatalf("diacritics %U", row)
	}
}

func TestGraphicsFallbackGeometry(t *testing.T) {
	p := colors(false)
	meta := graphicsMeta{Name: "screenshot-with-a-very-long-name\x1b[31m.png", MediaType: "image/png", Width: 1920, Height: 1080, Bytes: 345678}
	for _, loading := range []bool{false, true} {
		for _, size := range [][2]int{{1, 1}, {3, 1}, {12, 2}, {30, 8}, {4, 2}} {
			rows := graphicsFallbackRows(p, "󰋩", meta, size[0], size[1], loading)
			if len(rows) != size[1] {
				t.Fatalf("%v rows %d", size, len(rows))
			}
			for _, r := range rows {
				if w := ansi.StringWidth(r); w != size[0] {
					t.Fatalf("%v loading=%v width %d: %q", size, loading, w, r)
				}
				if strings.Contains(r, "\x1b[31m") {
					t.Fatal("unsafe name escaped")
				}
			}
		}
	}
	rows := strings.Join(graphicsFallbackRows(p, "#", meta, 30, 8, false), "\n")
	for _, want := range []string{"image/png", "1920×1080", "337.6 KiB"} {
		if !strings.Contains(ansi.Strip(rows), want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(graphicsFallbackRows(p, "#", meta, 30, 4, true), "")), "Loading preview") {
		t.Error("loading line missing")
	}
}
