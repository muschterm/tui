package tui

import (
	"bytes"
	"image/png"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// pumped is what pump observed: raw terminal writes and framework messages.
type pumped struct {
	raw  strings.Builder
	msgs []tea.Msg
}

// pump runs cmd and feeds the graphics, viewer and thumbnail messages it
// produces back through Update until nothing is left. Commands that do not
// finish promptly (ticks, cursor blink) are dropped.
func pump(t *testing.T, m *Model, cmd tea.Cmd) *pumped {
	t.Helper()
	out := &pumped{}
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 500; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var msg tea.Msg
		select {
		case msg = <-done:
		case <-time.After(200 * time.Millisecond):
			continue
		}
		if msg == nil {
			continue
		}
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := range v.Len() {
				queue = append(queue, v.Index(i).Interface().(tea.Cmd))
			}
			continue
		}
		switch msg := msg.(type) {
		case tea.RawMsg:
			out.raw.WriteString(msg.Msg.(string))
		case viewerLoadMsg, viewerImageMsg, thumbnailMsg, graphicsEncodedMsg, graphicsWrittenMsg, graphicsPreparedMsg:
			_, next := m.Update(msg)
			queue = append(queue, next)
		default:
			out.msgs = append(out.msgs, msg)
		}
	}
	return out
}

func (p *pumped) has(want tea.Msg) bool {
	for _, m := range p.msgs {
		if reflect.TypeOf(m) == reflect.TypeOf(want) {
			return true
		}
	}
	return false
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// graphicsModel is a 120×40 model whose terminal confirmed kitty graphics.
func graphicsModel(t *testing.T) (*Model, *fakeArtifacts) {
	t.Helper()
	m, api := intakeModel(t, &fakeClipboard{})
	m.colorProfile = colorprofile.TrueColor
	m.graphics.state = graphicsSupported
	m.graphics.cellW, m.graphics.cellH = 10, 20
	return m, api
}

func TestGraphicsProbeStartsOnlyForConfirmedTerminals(t *testing.T) {
	for name, want := range map[string]graphicsSupport{
		"kitty(0.43.0)": graphicsQuerying,
		"ghostty 1.2.0": graphicsQuerying,
		"iTerm2 3.6.11": graphicsUnavailable,
		"xterm(390)":    graphicsUnavailable,
		"WezTerm 2024":  graphicsUnavailable,
		"":              graphicsUnavailable,
	} {
		m := testModel()
		m.terminalColorOptions(func(string) string { return "" })
		m.updateColorProfile(colorprofile.TrueColor)
		m.Update(tea.TerminalVersionMsg{Name: name})
		if m.graphics.state != want {
			t.Fatalf("%q: state %v, want %v", name, m.graphics.state, want)
		}
	}
	m := testModel()
	m.terminalColorOptions(func(string) string { return "" })
	m.updateColorProfile(colorprofile.TrueColor)
	m.Update(tea.TerminalVersionMsg{Name: "kitty(0.43.0)"})
	m.Update(uv.UnknownApcEvent("\x1b_Gi=31;OK\x1b\\"))
	if !m.graphics.Supported() {
		t.Fatal("OK reply did not enable graphics")
	}
	m.Update(uv.CellSizeEvent{Width: 9, Height: 18})
	if w, h := m.graphics.CellPixels(); w != 9 || h != 18 {
		t.Fatal("cell size not recorded")
	}
	// A later downgrade below 256 colors disables graphics.
	m.updateColorProfile(colorprofile.ANSI)
	if m.graphics.Supported() {
		t.Fatal("ANSI profile kept graphics")
	}
}

func showTestImage(t *testing.T, m *Model, key string) int {
	t.Helper()
	if m.imgs().closed {
		m.images = newGraphicsRegistry(0) // a quit closed the previous one
	}
	pump(t, m, m.imgs().Show(graphicsPrepared{Key: key, PNG: []byte("png"), Cols: 2, Rows: 1}))
	id, ok := m.imgs().Ready(key)
	if !ok {
		t.Fatal("image not ready")
	}
	return id
}

func TestGraphicsCleanupBeforeSuspendAndQuit(t *testing.T) {
	m, _ := graphicsModel(t)
	id := showTestImage(t, m, "a")
	del := "a=d,d=I,i=" + strconv.Itoa(id)
	out := pump(t, m, m.key(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}))
	if !strings.Contains(out.raw.String(), del) || !out.has(tea.SuspendMsg{}) {
		t.Fatalf("suspend: raw %q msgs %v", out.raw.String(), out.msgs)
	}
	if _, ok := m.imgs().Ready("a"); ok {
		t.Fatal("image still ready after suspend cleanup")
	}
	for _, c := range []func() tea.Cmd{
		func() tea.Cmd { return m.key(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}) },
		func() tea.Cmd { return m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) },
		func() tea.Cmd { return m.activate(action{Kind: "quit"}) },
		func() tea.Cmd { return m.activate(action{Kind: "suspend"}) },
	} {
		showTestImage(t, m, "a")
		if out := pump(t, m, c()); !strings.Contains(out.raw.String(), "a=d,d=I") {
			t.Fatalf("quit/suspend path without cleanup: %q", out.raw.String())
		}
	}
	del = "a=d,d=I,i=" + strconv.Itoa(showTestImage(t, m, "a"))
	out = pump(t, m, m.key(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}))
	if !strings.Contains(out.raw.String(), del) || !out.has(tea.QuitMsg{}) || strings.Contains(out.raw.String(), "d=A") {
		t.Fatalf("quit: raw %q", out.raw.String())
	}
	// Nothing ever transmitted: quit is plain.
	m.images = newGraphicsRegistry(0)
	if out := pump(t, m, m.quit()); out.raw.Len() != 0 || !out.has(tea.QuitMsg{}) {
		t.Fatal("empty cleanup wrote sequences")
	}
}

func imageActivityModel(t *testing.T, supported bool) (*Model, *fakeArtifacts) {
	t.Helper()
	m, api := graphicsModel(t)
	if !supported {
		m.graphics.state = graphicsUnavailable
	}
	api.fetch = map[string][]byte{"art-img": pngBytes(t, 400, 200)}
	api.fetchType = map[string]string{"art-img": "image/png"}
	idx := activeThreadIndex(m)
	m.snapshot.Threads[idx].Activity = append(m.snapshot.Threads[idx].Activity, protocol.Activity{ID: "pi", Role: "user", Prompt: &protocol.Prompt{Attachments: []protocol.Attachment{
		{Kind: "image", Name: "wide.png", ArtifactID: "art-img", MediaType: "image/png", Size: 900, Width: 400, Height: 200},
	}}})
	return m, api
}

func placeholderRow(row string) bool { return strings.ContainsRune(row, kitty.Placeholder) }

func TestViewerImageLoadsPreparesAndPlaces(t *testing.T) {
	m, api := imageActivityModel(t, true)
	cmd := m.activate(action{Kind: "attachment-view", Value: "activity", ID: "pi", Index: 0})
	if text := screenText(m); !strings.Contains(text, "Loading preview…") {
		t.Fatalf("no loading state:\n%s", text)
	}
	assertRowWidths(t, m, "graphics")
	pump(t, m, cmd)
	if len(api.fetched) != 1 || m.viewer.imgPrep == nil {
		t.Fatalf("fetched %v prep %v err %q", api.fetched, m.viewer.imgPrep, m.viewer.imgErr)
	}
	id, ok := m.imgs().Ready(viewerImageKey)
	if !ok {
		t.Fatal("viewer image not placed")
	}
	f := m.render()
	count := 0
	for _, row := range f.rows {
		if placeholderRow(row) {
			count++
			if !strings.Contains(row, "38;5;"+strconv.Itoa(id)+"m") {
				t.Fatalf("placeholder row lost id %d: %q", id, row)
			}
		}
	}
	if count != m.viewer.imgPrep.Rows || count == 0 {
		t.Fatalf("placeholder rows %d, prepared %d", count, m.viewer.imgPrep.Rows)
	}
	// 400×200 px at 10×20 px cells is 40×10 cells: aspect kept.
	if p := m.viewer.imgPrep; p.Cols != 40 || p.Rows != 10 {
		t.Fatalf("placement %d×%d", p.Cols, p.Rows)
	}
	assertRowWidths(t, m, "graphics")
	// Repainting never grows the styled rows.
	first := strings.Join(m.render().rows, "\n")
	for range 5 {
		m.hover = "viewer-close"
		m.render()
		m.hover = ""
	}
	if again := strings.Join(m.render().rows, "\n"); len(again) != len(first) {
		t.Fatalf("repaint grew %d → %d bytes", len(first), len(again))
	}
	// Expand re-fits (a new box), then re-places without retransmitting.
	before := m.viewer.imgBox
	out := pump(t, m, m.activate(action{Kind: "viewer-expand"}))
	if m.viewer.imgBox == before || strings.Contains(out.raw.String(), "a=t") {
		t.Fatalf("expand: box %+v raw %q", m.viewer.imgBox, out.raw.String())
	}
	// Resume retransmits lazily after a suspend cleanup.
	pump(t, m, m.suspend())
	_, resume := m.Update(tea.ResumeMsg{})
	out = pump(t, m, resume)
	if _, ok := m.imgs().Ready(viewerImageKey); !ok || !strings.Contains(out.raw.String(), "a=t") {
		t.Fatalf("resume did not retransmit: %q", out.raw.String())
	}
	// Closing releases the image.
	id, _ = m.imgs().Ready(viewerImageKey)
	out = pump(t, m, m.closeAttachmentViewer())
	if !strings.Contains(out.raw.String(), "a=d,d=I,i="+strconv.Itoa(id)) {
		t.Fatalf("close did not delete: %q", out.raw.String())
	}
	if _, ok := m.imgs().Ready(viewerImageKey); ok {
		t.Fatal("released image still ready")
	}
}

func TestViewerImageFallbackWhenUnsupported(t *testing.T) {
	m, api := imageActivityModel(t, false)
	pump(t, m, m.activate(action{Kind: "attachment-view", Value: "activity", ID: "pi", Index: 0}))
	text := screenText(m)
	if len(api.fetched) != 0 || strings.Contains(text, "Loading") || !strings.Contains(text, "wide.png") || !strings.Contains(text, "400×200") {
		t.Fatalf("fallback: fetched %v\n%s", api.fetched, text)
	}
	for _, row := range m.render().rows {
		if placeholderRow(row) {
			t.Fatal("placeholders without graphics support")
		}
	}
	assertRowWidths(t, m, "graphics")
}

func TestViewerImageStaleResultIgnored(t *testing.T) {
	m, _ := imageActivityModel(t, true)
	cmd := m.activate(action{Kind: "attachment-view", Value: "activity", ID: "pi", Index: 0})
	load := drainBatch(cmd)
	m.closeAttachmentViewer()
	pump(t, m, func() tea.Msg { return load })
	if _, ok := m.imgs().Ready(viewerImageKey); ok || m.viewer != nil {
		t.Fatal("stale load placed an image")
	}
}

func TestQueuedEditPasteDeletesDroppedUpload(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	owner := m.currentOwner()
	m.intakes = map[uint64]draftOwner{7: owner}
	m.state.Edit = &editState{ThreadID: owner.threadID}
	pump(t, m, m.acceptClipboardImage(clipboardImageMsg{id: 7, owner: owner, name: "x.png", info: protocol.ArtifactInfo{ID: "staged-1"}}))
	if len(api.deleted) != 1 || api.deleted[0] != "staged-1" || len(m.viewState().Attachments) != 0 {
		t.Fatalf("deleted %v attachments %v", api.deleted, m.viewState().Attachments)
	}
	// A duplicate delivery of an already consumed intake never deletes.
	pump(t, m, m.acceptClipboardImage(clipboardImageMsg{id: 7, owner: owner, name: "x.png", info: protocol.ArtifactInfo{ID: "staged-1"}}))
	if len(api.deleted) != 1 {
		t.Fatal("duplicate intake deleted again")
	}
}

func TestArtifactErrorText(t *testing.T) {
	if s := artifactErrorText(&protocol.Error{Code: "busy", Message: "too many"}); !strings.Contains(s, "busy") || !strings.Contains(s, "try again") {
		t.Fatal(s)
	}
	if s := artifactErrorText(&protocol.Error{Code: "unavailable", Message: "artifact content did not match its recorded digest"}); s != "artifact content did not match its recorded digest" {
		t.Fatal(s)
	}
}
