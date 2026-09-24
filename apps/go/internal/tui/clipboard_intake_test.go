package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func init() {
	// No test may reach the real clipboard.
	nativeClipboard = func() clipboardSource { return nil }
}

type fakeClipboard struct {
	types    []string
	data     map[string][]byte
	typesErr error
	calls    []string
}

func (f *fakeClipboard) Types(context.Context) ([]string, error) {
	f.calls = append(f.calls, "types")
	return f.types, f.typesErr
}

func (f *fakeClipboard) Read(_ context.Context, mt string, limit int64) ([]byte, error) {
	f.calls = append(f.calls, "read "+mt)
	d := f.data[mt]
	if int64(len(d)) > limit {
		return nil, errClipboardTooLarge
	}
	return d, nil
}

type fakeArtifacts struct {
	uploads   []string
	uploadErr map[string]error
	fetch     map[string][]byte
	preview   protocol.FilePreview
	previewed []string
	seq       int
	fetchType map[string]string
	fetched   []string
	deleted   []string
}

func (f *fakeArtifacts) DeleteArtifact(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeArtifacts) UploadArtifact(_ context.Context, name, mt string, data []byte) (protocol.ArtifactInfo, error) {
	f.uploads = append(f.uploads, name)
	if err := f.uploadErr[name]; err != nil {
		return protocol.ArtifactInfo{}, err
	}
	f.seq++
	if mt == "" {
		mt = "text/plain; charset=utf-8"
	}
	return protocol.ArtifactInfo{ID: name + "-id", Name: name, MediaType: mt, Size: int64(len(data)), SHA256: "sum", State: "staged", Width: 4, Height: 3}, nil
}

func (f *fakeArtifacts) FetchArtifact(_ context.Context, id string, _ int64) ([]byte, string, error) {
	f.fetched = append(f.fetched, id)
	d, ok := f.fetch[id]
	if !ok {
		return nil, "", errors.New("missing")
	}
	if mt := f.fetchType[id]; mt != "" {
		return d, mt, nil
	}
	return d, "text/plain; charset=utf-8", nil
}

func (f *fakeArtifacts) PreviewFile(_ context.Context, projectID, threadID, path string) (protocol.FilePreview, error) {
	f.previewed = append(f.previewed, projectID+"|"+threadID+"|"+path)
	return f.preview, nil
}

func intakeModel(t *testing.T, clip *fakeClipboard) (*Model, *fakeArtifacts) {
	t.Helper()
	m := readyPasteModel(t)
	api := &fakeArtifacts{}
	m.clipboardSource, m.artifacts = clip, api
	// Never fall through to the real clipboard, even on the text path.
	m.clipboardRead = func() (string, error) { return "", nil }
	m.snapshot.Capabilities = append(m.snapshot.Capabilities, "workspace-file-context")
	return m, api
}

func runPaste(t *testing.T, m *Model) tea.Msg {
	t.Helper()
	cmd := m.pasteClipboard()
	if cmd == nil {
		t.Fatal("Paste returned no command")
	}
	return cmd()
}

func TestClassifyClipboardPriority(t *testing.T) {
	cases := []struct {
		types []string
		kind  intakeKind
		mt    string
	}{
		{[]string{"text/plain", "image/png", "text/uri-list"}, intakeFiles, "text/uri-list"},
		{[]string{"x-special/gnome-copied-files", "image/png"}, intakeFiles, "x-special/gnome-copied-files"},
		{[]string{"text/plain", "image/jpeg", "image/png"}, intakeImage, "image/png"},
		{[]string{"image/gif"}, intakeImage, "image/gif"},
		{[]string{"text/plain;charset=utf-8", "image/webp"}, intakeText, ""},
	}
	for _, c := range cases {
		if k, mt := classifyClipboard(c.types); k != c.kind || mt != c.mt {
			t.Fatalf("%v → %v %q", c.types, k, mt)
		}
	}
}

func TestParseCopiedFiles(t *testing.T) {
	paths, skipped := parseCopiedFiles("text/uri-list", []byte("# comment\r\nfile:///tmp/a%20b.txt\r\nfile://localhost/tmp/%C3%A9.md\nfile://elsewhere.example/tmp/x\nhttps://example.com/y\n\n"))
	if strings.Join(paths, "|") != "/tmp/a b.txt|/tmp/é.md" {
		t.Fatalf("paths = %q", paths)
	}
	if len(skipped) != 2 || !strings.Contains(skipped[0], "another machine") || !strings.Contains(skipped[1], "not a local file") {
		t.Fatalf("skipped = %q", skipped)
	}
	paths, _ = parseCopiedFiles("x-special/gnome-copied-files", []byte("copy\nfile:///a/one\nfile:///a/two"))
	if strings.Join(paths, "|") != "/a/one|/a/two" {
		t.Fatalf("gnome paths = %q", paths)
	}
}

func TestPasteCopiedFilesSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "my notes.txt")
	os.WriteFile(file, []byte("hello"), 0o600)
	clip := &fakeClipboard{types: []string{"text/uri-list", "text/plain"}, data: map[string][]byte{
		"text/uri-list": []byte("file://" + strings.ReplaceAll(file, " ", "%20") + "\nfile://" + dir + "\n"),
	}}
	m, api := intakeModel(t, clip)
	m.prompt.SetValue("keep")
	msg := runPaste(t, m)
	m.Update(msg)
	atts := m.viewState().Attachments
	if len(atts) != 1 || atts[0].Kind != "copied-file" || atts[0].Source != file || atts[0].Name != "my notes.txt" {
		t.Fatalf("attachments = %+v", atts)
	}
	if len(api.uploads) != 0 {
		t.Fatal("copied files uploaded at Paste")
	}
	if !strings.Contains(m.notice.text, "not a regular file") || m.prompt.Value() != "keep" {
		t.Fatalf("notice %q prompt %q", m.notice.text, m.prompt.Value())
	}
}

func TestPasteTextFallsBackWhenNoAttachmentType(t *testing.T) {
	clip := &fakeClipboard{types: []string{"text/plain"}}
	m, _ := intakeModel(t, clip)
	m.clipboardRead = func() (string, error) { return "words", nil }
	m.Update(runPaste(t, m))
	if m.prompt.Value() != "words" || len(m.viewState().Attachments) != 0 {
		t.Fatalf("prompt %q atts %v", m.prompt.Value(), m.viewState().Attachments)
	}
}

func TestPasteRemoteSessionNeverReadsClipboard(t *testing.T) {
	clip := &fakeClipboard{types: []string{"image/png"}}
	m, _ := intakeModel(t, clip)
	t.Setenv("SSH_TTY", "/dev/pts/9")
	m.pasteClipboard()
	if len(clip.calls) != 0 {
		t.Fatalf("remote Paste touched the clipboard: %v", clip.calls)
	}
	if !strings.Contains(m.notice.text, "attach files with @") {
		t.Fatalf("notice = %q", m.notice.text)
	}
}

func TestRunBoundedLimitsOutputAndTime(t *testing.T) {
	if _, err := runBounded(context.Background(), 10, "sh", "-c", "head -c 100 /dev/zero"); !errors.Is(err, errClipboardTooLarge) {
		t.Fatalf("oversized output err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runBounded(ctx, 10, "sleep", "5"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout err = %v after %v", err, time.Since(start))
	}
	if data, err := runBounded(context.Background(), 10, "printf", "ok"); err != nil || string(data) != "ok" {
		t.Fatalf("bounded read = %q %v", data, err)
	}
}

func TestPastedImageLandsOnOriginatingDraftAfterNavigation(t *testing.T) {
	clip := &fakeClipboard{types: []string{"image/png"}, data: map[string][]byte{"image/png": []byte("\x89PNG....")}}
	m, api := intakeModel(t, clip)
	origin := m.state.Active
	msg := runPaste(t, m)
	var other string
	for _, th := range m.snapshot.Threads {
		if th.ID != origin {
			other = th.ID
		}
	}
	m.state.Active = other
	m.loadDraft()
	m.Update(msg)
	m.Update(msg) // a duplicate never applies twice
	got := m.state.Threads[origin].Attachments
	if len(got) != 1 || got[0].Kind != "artifact" || got[0].ArtifactID == "" || got[0].Width != 4 || !strings.HasPrefix(got[0].Name, "clipboard-") {
		t.Fatalf("origin attachments = %+v", got)
	}
	if len(m.viewState().Attachments) != 0 {
		t.Fatal("image appeared in another thread's draft")
	}
	if len(api.uploads) != 1 {
		t.Fatalf("uploads = %v", api.uploads)
	}
}

func TestIntakeRespectsAttachmentCap(t *testing.T) {
	clip := &fakeClipboard{types: []string{"image/png"}, data: map[string][]byte{"image/png": []byte("png")}}
	m, api := intakeModel(t, clip)
	for i := 0; i < 8; i++ {
		m.viewState().Attachments = append(m.viewState().Attachments, protocol.Attachment{Kind: "file", Name: "x"})
	}
	m.Update(runPaste(t, m))
	if len(m.viewState().Attachments) != 8 || len(api.uploads) != 0 {
		t.Fatalf("cap exceeded: %d uploads %v", len(m.viewState().Attachments), api.uploads)
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("a"), 0o600)
	os.WriteFile(b, []byte("b"), 0o600)
	m.viewState().Attachments = m.viewState().Attachments[:7]
	clip.types = []string{"text/uri-list"}
	clip.data["text/uri-list"] = []byte("file://" + a + "\nfile://" + b)
	m.Update(runPaste(t, m))
	if len(m.viewState().Attachments) != 8 || !strings.Contains(m.notice.text, "over the limit") {
		t.Fatalf("files cap: %d %q", len(m.viewState().Attachments), m.notice.text)
	}
}

func copiedDraft(t *testing.T, m *Model, names ...string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte("content of "+n), 0o600)
		paths = append(paths, p)
		m.viewState().Attachments = append(m.viewState().Attachments, protocol.Attachment{Kind: "copied-file", Name: n, Source: p})
	}
	m.prompt.SetValue("please review")
	m.viewState().Draft = m.prompt.Value()
	return paths
}

func TestSendCapturesCopiedFilesThenSends(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt", "two.txt")
	m.viewState().Attachments[1].PreviewSHA256 = "previewed"
	cmd := m.activate(action{Kind: "send"})
	if cmd == nil || m.sendCapture == nil || m.busy != nil {
		t.Fatal("Send did not start capture")
	}
	// Edits after Send do not alter the captured command.
	m.prompt.SetValue("changed")
	m.viewState().Draft = "changed"
	if m.activate(action{Kind: "send"}); m.sendCapture == nil || len(api.uploads) != 0 {
		t.Fatal("second Send while capturing changed state")
	}
	m.Update(cmd())
	if m.busy == nil {
		t.Fatalf("command not dispatched: %q", m.status)
	}
	c := *m.busy
	if c.Text != "please review" || len(c.Attachments) != 2 {
		t.Fatalf("command = %+v", c)
	}
	for i, a := range c.Attachments {
		// Copied files keep their client-local path as display identity.
		if a.Kind != "artifact" || a.ArtifactID == "" || a.Source != m.viewState().Attachments[i].Source || a.Source == "" {
			t.Fatalf("attachment %d = %+v", i, a)
		}
	}
	if c.Attachments[1].PreviewSHA256 != "previewed" || c.ThreadID != m.state.Active {
		t.Fatalf("preview/thread = %+v", c)
	}
	if got := m.viewState().Attachments[0].ArtifactID; got != "one.txt-id" {
		t.Fatalf("capture not recorded on draft: %q", got)
	}
}

func TestSendCaptureFailureKeepsDraftAndRetryReusesUploads(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "one.txt", "two.txt")
	api.uploadErr = map[string]error{"two.txt": errors.New("disk full")}
	cmd := m.activate(action{Kind: "send"})
	m.Update(cmd())
	if m.busy != nil || m.sendCapture != nil {
		t.Fatal("failed capture sent or stayed pending")
	}
	if !strings.Contains(m.status, "two.txt") || !strings.Contains(m.status, "disk full") {
		t.Fatalf("status = %q", m.status)
	}
	if m.viewState().Draft != "please review" || len(m.viewState().Attachments) != 2 {
		t.Fatal("draft lost")
	}
	m.menu = nil
	api.uploadErr = nil
	cmd = m.activate(action{Kind: "send"})
	m.Update(cmd())
	if strings.Join(api.uploads, ",") != "one.txt,two.txt,two.txt" {
		t.Fatalf("uploads = %v", api.uploads)
	}
	if m.busy == nil || m.busy.Attachments[0].ArtifactID != "one.txt-id" {
		t.Fatal("retry did not send with the reused capture")
	}
}

func TestSendCaptureMissingFileNamesAttachment(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	paths := copiedDraft(t, m, "gone.txt")
	os.Remove(paths[0])
	cmd := m.activate(action{Kind: "send"})
	m.Update(cmd())
	if m.busy != nil || len(api.uploads) != 0 || !strings.Contains(m.status, "gone.txt") {
		t.Fatalf("busy %v uploads %v status %q", m.busy, api.uploads, m.status)
	}
}

func TestWorkspacePreviewRecordsDigestForSend(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	api.preview = protocol.FilePreview{Source: "docs/a.md", Name: "a.md", Content: "# Hello\nbody", SHA256: "abc", Size: 12}
	m.viewState().Attachments = []protocol.Attachment{{Kind: "workspace-file", Name: "docs/a.md", Source: "docs/a.md"}}
	cmd := m.activate(action{Kind: "attachment-view", Value: "draft:docs/a.md", Index: 0})
	if m.viewer == nil || !m.viewer.loading || !strings.Contains(screenText(m), "Loading preview") {
		t.Fatal("viewer not loading")
	}
	m.Update(drainBatch(cmd))
	if !strings.Contains(screenText(m), "body") {
		t.Fatalf("content not shown: %s", screenText(m))
	}
	if got := m.viewState().Attachments[0].PreviewSHA256; got != "abc" {
		t.Fatalf("PreviewSHA256 = %q", got)
	}
	m.closeAttachmentViewer()
	m.prompt.SetValue("send it")
	m.activate(action{Kind: "send"})
	if m.busy == nil || m.busy.Attachments[0].PreviewSHA256 != "abc" {
		t.Fatalf("Send lacks preview digest: %+v", m.busy)
	}
}

// drainBatch runs cmd and returns the first viewerLoadMsg it yields.
func drainBatch(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if r, ok := c().(viewerLoadMsg); ok {
				return r
			}
		}
		return nil
	}
	return msg
}

func TestCopiedFilePreviewAndStaleResultIgnored(t *testing.T) {
	m, _ := intakeModel(t, &fakeClipboard{})
	copiedDraft(t, m, "a.txt")
	cmd := m.activate(action{Kind: "attachment-view", Value: "draft:a.txt", Index: 0})
	msg := drainBatch(cmd)
	m.closeAttachmentViewer()
	m.Update(msg)
	if m.viewState().Attachments[0].PreviewSHA256 != "" {
		t.Fatal("stale preview recorded a digest")
	}
	cmd = m.activate(action{Kind: "attachment-view", Value: "draft:a.txt", Index: 0})
	m.Update(drainBatch(cmd))
	if !strings.Contains(screenText(m), "content of a.txt") || m.viewState().Attachments[0].PreviewSHA256 == "" {
		t.Fatal("copied-file preview not shown/recorded")
	}
}

func TestViewerArtifactStates(t *testing.T) {
	m, api := intakeModel(t, &fakeClipboard{})
	api.fetch = map[string][]byte{"art-1": []byte("fetched text")}
	idx := activeThreadIndex(m)
	m.snapshot.Threads[idx].Activity = append(m.snapshot.Threads[idx].Activity, protocol.Activity{ID: "p1", Role: "user", Prompt: &protocol.Prompt{Attachments: []protocol.Attachment{
		{Kind: "file", Name: "big.log", ArtifactID: "art-1", MediaType: "text/plain", Size: 70000, ChangedSincePreview: true},
		{Kind: "file", Name: "blob.bin", ArtifactID: "art-2", MediaType: "application/octet-stream", Size: 5},
		{Kind: "image", Name: "shot.png", ArtifactID: "art-3", MediaType: "image/png", Size: 9, Width: 640, Height: 480},
	}}})
	cmd := m.activate(action{Kind: "attachment-view", Value: "activity", ID: "p1", Index: 0})
	m.Update(drainBatch(cmd))
	text := screenText(m)
	if !strings.Contains(text, "fetched text") || !strings.Contains(text, "Changed since preview") {
		t.Fatalf("text artifact: %s", text)
	}
	m.closeAttachmentViewer()
	m.activate(action{Kind: "attachment-view", Value: "activity", ID: "p1", Index: 1})
	if !strings.Contains(screenText(m), "Preview unavailable for application/octet-stream") {
		t.Fatal("binary state missing")
	}
	m.closeAttachmentViewer()
	m.activate(action{Kind: "attachment-view", Value: "activity", ID: "p1", Index: 2})
	// Unsupported graphics: the styled metadata fallback, nothing fetched.
	if text := screenText(m); !strings.Contains(text, "640×480") || !strings.Contains(text, "640 × 480 px") || strings.Contains(text, "Loading") || slices.Contains(api.fetched, "art-3") {
		t.Fatalf("image state: %s", text)
	}
	m.closeAttachmentViewer()
	var found bool
	for _, b := range activityBlocks(m, m.snapshot.Threads[idx].Activity[len(m.snapshot.Threads[idx].Activity)-1]) {
		if b.kind == surfaceStatusBlock && b.label == "Changed since preview" && b.glyph != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("activity row lacks the Changed since preview mark")
	}
}
