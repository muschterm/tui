package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// hugePNG is a signature and IHDR only: DecodeConfig reads no further.
func hugePNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func protocolCode(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestArtifactUploadFetchAndPreviewOverHTTP(t *testing.T) {
	home := t.TempDir()
	c, stop := startTestServer(t, home)
	defer stop()
	ctx := context.Background()

	// Unauthenticated reads are refused like every other route.
	resp, err := http.Get(c.Discovery.URL + "/v1/artifacts/" + strings.Repeat("0", 32))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("unauthenticated artifact read", resp.StatusCode)
	}

	img := pngBytes(t, 3, 2)
	info, err := c.UploadArtifact(ctx, "../shot.png", "application/octet-stream", img)
	if err != nil {
		t.Fatal(err)
	}
	if info.MediaType != "image/png" || info.Width != 3 || info.Height != 2 || info.Name != "shot.png" || info.State != "staged" || info.Size != int64(len(img)) {
		t.Fatalf("image metadata %+v", info)
	}
	data, mediaType, err := c.FetchArtifact(ctx, info.ID, 0)
	if err != nil || !bytes.Equal(data, img) || mediaType != "image/png" {
		t.Fatal("fetch", err, mediaType)
	}
	if _, _, err := c.FetchArtifact(ctx, info.ID, 8); protocolCode(err) != "capacity" {
		t.Fatal("client read bound ignored", err)
	}
	req, _ := http.NewRequest(http.MethodGet, c.Discovery.URL+"/v1/artifacts/"+info.ID, nil)
	req.Header.Set("Authorization", "Bearer "+c.Discovery.Token)
	req.Header.Set("X-TUI-Protocol", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatal("artifact headers", resp.Header)
	}

	if _, err := c.UploadArtifact(ctx, "fake.png", "image/png", []byte("not an image at all")); protocolCode(err) != "unsupported_media" {
		t.Fatal("claimed image accepted", err)
	}
	if _, err := c.UploadArtifact(ctx, "huge.png", "image/png", hugePNG(10000, 5000)); protocolCode(err) != "capacity" {
		t.Fatal("over-megapixel image accepted", err)
	}
	// The client refuses oversized input; the server enforces it independently.
	if _, err := c.UploadArtifact(ctx, "big", "", make([]byte, artifactLimit+1)); protocolCode(err) != "capacity" {
		t.Fatal("client oversize", err)
	}
	big := bytes.NewReader(make([]byte, artifactLimit+1))
	req, _ = http.NewRequest(http.MethodPost, c.Discovery.URL+"/v1/artifacts?name=big", io.NopCloser(big))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+c.Discovery.Token)
	req.Header.Set("X-TUI-Protocol", "1")
	resp, err = http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatal("server oversize status", resp.StatusCode)
		}
	}

	md, err := c.UploadArtifact(ctx, "notes.md", "text/markdown", []byte("# notes\n"))
	if err != nil || md.MediaType != "text/markdown" {
		t.Fatal("verified text claim", md, err)
	}
	if _, _, err := c.FetchArtifact(ctx, strings.Repeat("f", 32), 0); protocolCode(err) != "not_found" {
		t.Fatal("missing artifact", err)
	}

	// Draft preview follows the capture rules and never writes.
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "good.txt"), []byte("preview me"), 0600))
	must(os.WriteFile(filepath.Join(dir, "binary"), []byte{0xff, 0xfe, 0}, 0600))
	must(os.Mkdir(filepath.Join(dir, ".git"), 0700))
	must(os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret"), 0600))
	must(os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "gitlink")))
	receipt, err := c.Command(ctx, protocol.Command{Version: 1, ID: client.ID(), Kind: "project.add", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := c.PreviewFile(ctx, receipt.TargetID, "", "good.txt")
	sum := sha256.Sum256([]byte("preview me"))
	if err != nil || preview.Content != "preview me" || preview.SHA256 != hex.EncodeToString(sum[:]) || preview.Size != 10 {
		t.Fatal("preview", preview, err)
	}
	for _, path := range []string{"../escape", "/etc/passwd", ".git/config", "gitlink/config", "binary", "missing"} {
		if _, err := c.PreviewFile(ctx, receipt.TargetID, "", path); protocolCode(err) != "attachment" {
			t.Fatalf("preview %q accepted: %v", path, err)
		}
	}
	if _, err := c.PreviewFile(ctx, "", "", "good.txt"); err == nil {
		t.Fatal("preview without a target")
	}

	// A fixture Send accepts both uploads and fills text content.
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued := len(threadOf(s, "thread-shell").Queue)
	send := protocol.Command{Version: 1, ID: client.ID(), Kind: "prompt.send", ThreadID: "thread-shell", Text: "look", Attachments: []protocol.Attachment{{Kind: "artifact", ArtifactID: info.ID, Name: "claimed"}, {ArtifactID: md.ID}}}
	first, err := c.Command(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Command(ctx, send)
	if err != nil || again != first {
		t.Fatal("retry not idempotent", again, err)
	}
	s, _ = c.Snapshot(ctx)
	thread := threadOf(s, "thread-shell")
	if len(thread.Queue) != queued+1 {
		t.Fatal("retry duplicated the prompt", len(thread.Queue))
	}
	var pe *protocol.Error
	if err := c.DeleteArtifact(ctx, info.ID); !errors.As(err, &pe) || pe.Code != "accepted" {
		t.Fatal("accepted artifact deletable", err)
	}
	spare, err := c.UploadArtifact(ctx, "spare.txt", "", []byte("spare"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteArtifact(ctx, spare.ID); err != nil {
		t.Fatal("staged delete", err)
	}
	if err := c.DeleteArtifact(ctx, spare.ID); !errors.As(err, &pe) || pe.Code != "not_found" {
		t.Fatal("repeat delete", err)
	}
	got := thread.Queue[len(thread.Queue)-1].Attachments
	if got[0].Kind != "image" || got[0].Name != "shot.png" || got[0].Content != "" || got[0].Width != 3 || got[1].Kind != "file" || got[1].Content != "# notes\n" || got[1].MediaType != "text/markdown" {
		t.Fatalf("accepted attachments %+v", got)
	}
}

func artifactEngine(t *testing.T) (*engine, string) {
	t.Helper()
	e := testEngine(t)
	dir := filepath.Join(t.TempDir(), "artifacts")
	if err := e.store.UseArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	return e, dir
}

func stage(t *testing.T, st *storage.Store, name, claimed string, data []byte, created time.Time) protocol.ArtifactInfo {
	t.Helper()
	info, err := sniffArtifact(data, name, claimed)
	if err != nil {
		t.Fatal(err)
	}
	info.ID, info.State = ID(), "staged"
	if err := st.PublishArtifact(storage.Artifact{ArtifactInfo: info, CreatedAt: created}, data, stagedArtifactLimit); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestSendRejectsMissingExpiredAndForeignArtifacts(t *testing.T) {
	e, _ := artifactEngine(t)
	queued := len(e.snap.Threads[0].Queue)
	sendWith := func(id, thread, artifact string) error {
		_, err := e.command(protocol.Command{Version: 1, ID: id, Kind: "prompt.send", ThreadID: thread, Text: "t", Attachments: []protocol.Attachment{{Name: "pic", ArtifactID: artifact}}})
		return err
	}
	if err := sendWith("missing", "thread-shell", strings.Repeat("a", 32)); protocolCode(err) != "attachment" || !strings.Contains(err.Error(), `"pic"`) {
		t.Fatal("missing artifact", err)
	}
	old := stage(t, e.store, "old.txt", "", []byte("old"), time.Now().Add(-8*24*time.Hour))
	e.sweepArtifacts()
	if err := sendWith("expired", "thread-shell", old.ID); protocolCode(err) != "attachment" {
		t.Fatal("expired artifact", err)
	}
	fresh := stage(t, e.store, "a.txt", "", []byte("a"), time.Now())
	if err := sendWith("review", "thread-review", fresh.ID); err != nil {
		t.Fatal(err)
	}
	if err := sendWith("foreign", "thread-shell", fresh.ID); protocolCode(err) != "attachment" {
		t.Fatal("artifact accepted for another thread", err)
	}
	if len(e.snap.Threads[0].Queue) != queued {
		t.Fatal("rejected sends changed the queue")
	}
	for _, id := range []string{"missing", "expired", "foreign"} {
		if r, _ := e.store.Lookup(protocol.Command{Version: 1, ID: id}); r != nil {
			t.Fatal("rejected send recorded a receipt", id)
		}
	}
	// A deleted thread takes its accepted artifacts with it.
	review := threadOf(e.snap, "thread-review")
	if _, err := e.command(protocol.Command{Version: 1, ID: "delete", Kind: "thread.delete", ThreadID: "thread-review", Revision: review.LifecycleRevision}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.store.Artifact(fresh.ID); ok {
		t.Fatal("deleted thread artifact retained")
	}
}

func TestChangedSincePreview(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	s := protocol.Snapshot{Projects: []protocol.Project{{ID: "p", Path: dir}}}
	preview, err := previewWorkspaceFile(context.Background(), dir, "f.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.Command{Kind: "thread.start", ProjectID: "p", Attachments: []protocol.Attachment{{Kind: "workspace-file", Source: "f.txt", PreviewSHA256: preview.SHA256}}}
	captured, err := captureCommand(context.Background(), s, c, nil)
	if err != nil || captured.Attachments[0].ChangedSincePreview || captured.Attachments[0].SHA256 != preview.SHA256 {
		t.Fatal("unchanged file flagged", captured.Attachments, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err = captureCommand(context.Background(), s, c, nil)
	if err != nil || !captured.Attachments[0].ChangedSincePreview || captured.Attachments[0].Content != "after" {
		t.Fatal("changed file not identified", captured.Attachments, err)
	}
	if c.Attachments[0].ChangedSincePreview {
		t.Fatal("durable command mutated")
	}
}

func TestArtifactCapabilitiesAtSendAndDispatch(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	if err := e.store.UseArtifacts(filepath.Join(t.TempDir(), "artifacts")); err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings()
	img := stage(t, e.store, "shot.png", "", pngBytes(t, 2, 2), time.Now())
	start := protocol.Command{Version: 1, ID: "image", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "see", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: img.ID}}}
	before := len(e.current().Threads)
	if _, err := e.command(start); protocolCode(err) != "attachment" || !strings.Contains(err.Error(), "shot.png") {
		t.Fatal("image sent to an agent without image prompts", err)
	}
	if len(e.current().Threads) != before || fleet.count() != 0 {
		t.Fatal("rejected image send created or dispatched work")
	}
	if a, _, _ := e.store.Artifact(img.ID); a.State != "staged" {
		t.Fatal("rejected send bound the artifact")
	}
	// Binary files travel as embedded blobs when embedded context is advertised.
	blob := stage(t, e.store, "data.bin", "", []byte{0xff, 0x00, 0x01}, time.Now())
	text := stage(t, e.store, "notes.txt", "", []byte("plain words"), time.Now())
	start = protocol.Command{Version: 1, ID: "blob", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "read", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: blob.ID}, {ArtifactID: text.ID}}}
	receipt, err := e.command(start)
	if err != nil {
		t.Fatal(err)
	}
	waitTurn(t, e, receipt.TargetID, "prompt-blob")
	prompts, _, _, _ := fleet.agents[0].snapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "resource:attachment://"+blob.ID+"/data.bin:") || !strings.Contains(prompts[0], "resource:attachment://"+text.ID+"/notes.txt:plain words") || strings.Contains(prompts[0], "file://") {
		t.Fatalf("dispatched prompt %q", prompts)
	}
	if a, _, _ := e.store.Artifact(blob.ID); a.State != "accepted" || a.ThreadID != receipt.TargetID {
		t.Fatal("artifact not bound at acceptance", a)
	}
}

func TestPromptBlocksRefusesUnadvertisedArtifacts(t *testing.T) {
	p := protocol.Prompt{Text: "x", Attachments: []protocol.Attachment{{Kind: "image", Name: "a.png", ArtifactID: "id", MediaType: "image/png"}}}
	load := func(string) ([]byte, error) { return []byte{1, 2}, nil }
	if _, _, err := agent.PromptBlocks(p, "", agent.Info{}, load); err == nil {
		t.Fatal("image dropped or converted instead of refused")
	}
	blocks, _, err := agent.PromptBlocks(p, "", agent.Info{Capabilities: []string{agent.CapImagePrompt}}, load)
	if err != nil || len(blocks) != 2 || blocks[1].Image == nil || blocks[1].Image.Data != "AQI=" || blocks[1].Image.MimeType != "image/png" {
		t.Fatal("image block", blocks, err)
	}
}

var fixtureSettings = protocol.Settings{Model: "fixture-model", Effort: "low", Permissions: "fixture-only", Context: "unavailable", Speed: "standard"}

func TestArtifactResourceURIsIgnoreClientSource(t *testing.T) {
	p := protocol.Prompt{Text: "x", Attachments: []protocol.Attachment{
		{Kind: "file", Name: "notes md", Source: "/etc/secret", ArtifactID: "abc", Content: "text"},
		{Kind: "file", Name: "b.bin", Source: "../../x", ArtifactID: "def", MediaType: "application/octet-stream", Size: 2},
	}}
	blocks, _, err := agent.PromptBlocks(p, "/checkout", agent.Info{Capabilities: []string{agent.CapEmbeddedPrompt}}, func(string) ([]byte, error) { return []byte{1, 2}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if uri := blocks[1].Resource.Resource.TextResourceContents.Uri; uri != "attachment://abc/notes%20md" {
		t.Fatal("text artifact URI", uri)
	}
	if uri := blocks[2].Resource.Resource.BlobResourceContents.Uri; uri != "attachment://def/b.bin" {
		t.Fatal("blob artifact URI", uri)
	}
}

func TestSendRejectsPromptOverACPBudget(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	if err := e.store.UseArtifacts(filepath.Join(t.TempDir(), "artifacts")); err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings()
	big := stage(t, e.store, "big.bin", "", bytes.Repeat([]byte{0xff, 0}, 2400<<10), time.Now())
	before := len(e.current().Threads)
	_, err := e.command(protocol.Command{Version: 1, ID: "big", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "x", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: big.ID}}})
	if protocolCode(err) != "attachment" || !strings.Contains(err.Error(), "big.bin") {
		t.Fatal("over-budget prompt accepted", err)
	}
	if len(e.current().Threads) != before || fleet.count() != 0 {
		t.Fatal("over-budget prompt created work")
	}
}

func TestDispatchKeepsPromptQueuedWhenBytesAreWrong(t *testing.T) {
	e, fleet, _ := acpEngine(t)
	dir := filepath.Join(t.TempDir(), "artifacts")
	if err := e.store.UseArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	settings := fakeSettings()
	data := bytes.Repeat([]byte{0xff, 0}, 40<<10)
	corrupt := stage(t, e.store, "c.bin", "", data, time.Now())
	// Same size, different bytes: acceptance does not read files over 64 KiB.
	if err := os.WriteFile(filepath.Join(dir, corrupt.ID), bytes.Repeat([]byte{1}, len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.command(protocol.Command{Version: 1, ID: "corrupt", Kind: "thread.start", ProjectID: "project-acp", Agent: "claude", Text: "x", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: corrupt.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, e, "dispatch failure", func(s protocol.Snapshot) bool { return threadOf(s, receipt.TargetID).State == "failed" })
	if thread := threadOf(s, receipt.TargetID); len(thread.Queue) != 1 || !strings.Contains(thread.Error, "c.bin") {
		t.Fatalf("prompt not kept queued: %+v", thread.Queue)
	}
	for _, a := range fleet.agents {
		if prompts, _, _, _ := a.snapshot(); len(prompts) != 0 {
			t.Fatal("corrupt attachment was sent")
		}
	}
	// A missing file fails the same way on Resume-free retry via Send.
	if err := os.Remove(filepath.Join(dir, corrupt.ID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.ReadArtifact(corrupt.ID, artifactLimit); err == nil {
		t.Fatal("missing bytes read")
	}
}

func TestConcurrentSendsOfOneArtifact(t *testing.T) {
	e, _ := artifactEngine(t)
	a := stage(t, e.store, "a.txt", "", []byte("shared"), time.Now())
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, thread := range []string{"thread-shell", "thread-review"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.command(protocol.Command{Version: 1, ID: "send-" + thread, Kind: "prompt.send", ThreadID: thread, Text: "t", Attachments: []protocol.Attachment{{ArtifactID: a.ID}}})
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatal("exactly one Send must bind the artifact", errs)
	}
}

func TestReopenSendAndStartRetryWithArtifacts(t *testing.T) {
	e, _ := artifactEngine(t)
	thread := &e.snap.Threads[0]
	thread.Closed, thread.State, thread.Requests, thread.Children = true, "idle", nil, nil
	a := stage(t, e.store, "a.txt", "", []byte("reopen"), time.Now())
	settings := fixtureSettings
	if _, err := e.command(protocol.Command{Version: 1, ID: "reopen", Kind: "prompt.reopen-send", ThreadID: thread.ID, Revision: thread.LifecycleRevision, Text: "t", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: a.ID, PreviewSHA256: a.SHA256}}}); err != nil {
		t.Fatal(err)
	}
	reopened := threadOf(e.snap, "thread-shell")
	if reopened.Closed {
		t.Fatal("thread not reopened")
	}
	if record, _, _ := e.store.Artifact(a.ID); record.State != "accepted" || record.ThreadID != "thread-shell" {
		t.Fatal("reopen-send did not bind", record)
	}
	b := stage(t, e.store, "b.txt", "", []byte("start"), time.Now())
	start := protocol.Command{Version: 1, ID: "start", Kind: "thread.start", ProjectID: "project-fixture", Agent: "Fixture agent", Text: "t", Settings: &settings, Attachments: []protocol.Attachment{{ArtifactID: b.ID, PreviewSHA256: "stale"}}}
	first, err := e.command(start)
	if err != nil {
		t.Fatal(err)
	}
	count := len(e.snap.Threads)
	again, err := e.command(start)
	if err != nil || again != first || len(e.snap.Threads) != count {
		t.Fatal("thread.start retry", again, err)
	}
	if record, _, _ := e.store.Artifact(b.ID); record.ThreadID != first.TargetID || record.CommandID != "start" {
		t.Fatal("start binding", record)
	}
	created := threadOf(e.snap, first.TargetID)
	captured := created.Activity
	var att protocol.Attachment
	for _, p := range append(created.Queue, func() []protocol.Prompt {
		var ps []protocol.Prompt
		for _, a := range captured {
			if a.Prompt != nil {
				ps = append(ps, *a.Prompt)
			}
		}
		return ps
	}()...) {
		if len(p.Attachments) > 0 {
			att = p.Attachments[0]
		}
	}
	if !att.ChangedSincePreview || att.Content != "start" {
		t.Fatal("artifact preview change not identified", att)
	}
	var reopenAtt protocol.Attachment
	for _, p := range reopened.Queue {
		if len(p.Attachments) > 0 {
			reopenAtt = p.Attachments[0]
		}
	}
	for _, act := range reopened.Activity {
		if act.Prompt != nil && len(act.Prompt.Attachments) > 0 && act.Prompt.Attachments[0].ArtifactID == a.ID {
			reopenAtt = act.Prompt.Attachments[0]
		}
	}
	if reopenAtt.ArtifactID != a.ID || reopenAtt.ChangedSincePreview {
		t.Fatal("matching preview flagged", reopenAtt)
	}
}

func TestUploadAndPreviewAreBounded(t *testing.T) {
	e, _ := artifactEngine(t)
	e.uploads, e.previews = make(chan struct{}, 1), make(chan struct{}, 1)
	e.uploads <- struct{}{}
	w := httptest.NewRecorder()
	e.uploadArtifact(w, httptest.NewRequest(http.MethodPost, "/v1/artifacts?name=a", strings.NewReader("a")))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "busy") {
		t.Fatal("upload not bounded", w.Code)
	}
	e.snap.Projects = append(e.snap.Projects, protocol.Project{ID: "p", Path: t.TempDir()})
	e.previews <- struct{}{}
	w = httptest.NewRecorder()
	e.previewFile(w, httptest.NewRequest(http.MethodGet, "/v1/preview?project_id=p&path=x", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("preview not bounded", w.Code)
	}
	e.stopping = true
	w = httptest.NewRecorder()
	e.previewFile(w, httptest.NewRequest(http.MethodGet, "/v1/preview?project_id=p&path=x", nil))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "stopping") {
		t.Fatal("preview while stopping", w.Code)
	}
}

func TestFetchArtifactVerifiesDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-TUI-Artifact-SHA256", strings.Repeat("0", 64))
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	c := client.New(protocol.Discovery{URL: srv.URL, Token: "t"})
	if _, _, err := c.FetchArtifact(context.Background(), "id", 0); protocolCode(err) != "unavailable" {
		t.Fatal("digest mismatch accepted", err)
	}
}
