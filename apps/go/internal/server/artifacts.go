package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF for DecodeConfig
	_ "image/jpeg" // register JPEG for DecodeConfig
	_ "image/png"  // register PNG for DecodeConfig
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/agent"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"github.com/muschterm/tui/apps/go/internal/storage"
)

// Artifact bounds. They are prototype limits, named so they are easy to move.
const (
	// artifactLimit bounds one uploaded artifact.
	artifactLimit = 16 << 20
	// stagedArtifactLimit bounds staged, unaccepted artifacts per server.
	stagedArtifactLimit = 256 << 20
	// imagePixelLimit rejects images whose decoded size would be unreasonable.
	imagePixelLimit = 40_000_000
	// stagedArtifactTTL expires staged artifacts no Send accepted.
	stagedArtifactTTL = 7 * 24 * time.Hour
	// artifactSweepInterval runs the cheap retention sweep while serving.
	artifactSweepInterval = 10 * time.Minute
	// acpPromptBudget bounds the JSON-encoded content blocks of one ACP prompt
	// (text, escaped text resources and base64 images/blobs), safely under the
	// Claude bridge's 8 MiB frame and the SDK's 10 MiB line.
	acpPromptBudget = 6 << 20
	// uploadSlots and previewSlots bound concurrent uploads and previews.
	uploadSlots  = 2
	previewSlots = 4
	// uploadReadTimeout bounds reading one upload body.
	uploadReadTimeout = 2 * time.Minute
)

// acquire takes a slot without waiting; a nil semaphore is unbounded.
func acquire(slots chan struct{}) (func(), bool) {
	if slots == nil {
		return func() {}, true
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFailure(w http.ResponseWriter, status int, err error) {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		writeJSON(w, status, pe)
		return
	}
	writeJSON(w, http.StatusInternalServerError, protocol.Error{Code: "storage", Message: "artifact storage failed"})
}

// uploadArtifact stages a raw request body. The name and claimed media type
// arrive as query parameters; the stored media type is sniffed.
func (e *engine) uploadArtifact(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	stopping := e.stopping
	e.mu.Unlock()
	if stopping {
		writeFailure(w, http.StatusConflict, failure("stopping", "server is shutting down"))
		return
	}
	release, ok := acquire(e.uploads)
	if !ok {
		writeFailure(w, http.StatusTooManyRequests, failure("busy", "other attachments are uploading; try again shortly"))
		return
	}
	defer release()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadTimeout))
	if r.ContentLength > artifactLimit {
		writeFailure(w, http.StatusRequestEntityTooLarge, failure("capacity", fmt.Sprintf("attachments are limited to %d MiB", artifactLimit>>20)))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, artifactLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeFailure(w, http.StatusRequestEntityTooLarge, failure("capacity", fmt.Sprintf("attachments are limited to %d MiB", artifactLimit>>20)))
		} else {
			writeFailure(w, http.StatusBadRequest, failure("invalid", "upload was interrupted"))
		}
		return
	}
	q := r.URL.Query()
	info, err := sniffArtifact(data, q.Get("name"), q.Get("media_type"))
	if err != nil {
		writeFailure(w, http.StatusBadRequest, err)
		return
	}
	info.ID, info.State = ID(), "staged"
	if err := e.store.PublishArtifact(storage.Artifact{ArtifactInfo: info, CreatedAt: time.Now()}, data, stagedArtifactLimit); err != nil {
		status := http.StatusConflict
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == "capacity" {
			status = http.StatusInsufficientStorage
		}
		writeFailure(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// sniffArtifact derives trusted metadata. A claimed image must decode as a
// supported image; unrecognized images are rejected rather than relabelled.
func sniffArtifact(data []byte, name, claimed string) (protocol.ArtifactInfo, error) {
	info := protocol.ArtifactInfo{Name: artifactName(name), Size: int64(len(data))}
	if len(data) == 0 {
		return info, failure("invalid", "attachment is empty")
	}
	sum := sha256.Sum256(data)
	info.SHA256 = hex.EncodeToString(sum[:])
	claimedBase, _, _ := mime.ParseMediaType(claimed)
	detected := http.DetectContentType(data)
	detectedBase, _, _ := mime.ParseMediaType(detected)
	if strings.HasPrefix(claimedBase, "image/") || strings.HasPrefix(detectedBase, "image/") {
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
			return info, failure("unsupported_media", fmt.Sprintf("%q is not a supported PNG, JPEG or GIF image", info.Name))
		}
		if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > imagePixelLimit {
			return info, failure("capacity", fmt.Sprintf("%q exceeds %d megapixels", info.Name, imagePixelLimit/1_000_000))
		}
		info.MediaType, info.Width, info.Height = "image/"+format, config.Width, config.Height
		return info, nil
	}
	info.MediaType = detectedBase
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		info.MediaType = "text/plain"
		// Verified text may keep a more specific text claim, such as Markdown.
		if strings.HasPrefix(claimedBase, "text/") || claimedBase == "application/json" {
			info.MediaType = claimedBase
		}
	}
	if info.MediaType == "" {
		info.MediaType = "application/octet-stream"
	}
	return info, nil
}

func artifactName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return "attachment"
	}
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

// deleteArtifact removes a staged draft attachment so it stops counting
// against the staged quota. Accepted attachments belong to their thread.
func (e *engine) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	err := e.store.DeleteArtifact(r.PathValue("id"))
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}
	status := http.StatusInternalServerError
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "not_found":
			status = http.StatusNotFound
		case "accepted":
			status = http.StatusConflict
		}
	}
	writeFailure(w, status, err)
}

// getArtifact streams stored bytes with their stored media type.
func (e *engine) getArtifact(w http.ResponseWriter, r *http.Request) {
	a, f, err := e.store.OpenArtifact(r.PathValue("id"))
	if err != nil {
		status := http.StatusGone
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == "not_found" {
			status = http.StatusNotFound
		}
		writeFailure(w, status, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", a.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.Header().Set("X-TUI-Artifact-SHA256", a.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, io.LimitReader(f, a.Size))
}

// previewFile returns a read-only draft preview under the Send capture rules.
func (e *engine) previewFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := protocol.BrowseRequest{Scope: "files", ProjectID: q.Get("project_id"), ThreadID: q.Get("thread_id")}
	e.mu.Lock()
	base, err := browseRoot(e.snap, req)
	stopping := e.stopping
	e.mu.Unlock()
	if stopping {
		writeFailure(w, http.StatusConflict, failure("stopping", "server is shutting down"))
		return
	}
	if err != nil {
		writeFailure(w, http.StatusBadRequest, err)
		return
	}
	// A slot is held until the read finishes, even after a timeout, so reads
	// stuck on a hung mount cannot accumulate without bound.
	release, ok := acquire(e.previews)
	if !ok {
		writeFailure(w, http.StatusTooManyRequests, failure("busy", "earlier previews are still reading; try again shortly"))
		return
	}
	preview, err := previewWorkspaceFile(r.Context(), base, q.Get("path"), release)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func previewWorkspaceFile(ctx context.Context, base, source string, release func()) (protocol.FilePreview, error) {
	if release == nil {
		release = func() {}
	}
	if strings.HasPrefix(base, "fixture://") {
		release()
		return protocol.FilePreview{}, failure("unavailable", "fixture workspace has no local files")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	type result struct {
		content []byte
		err     error
	}
	done := make(chan result, 1)
	go func() {
		defer release()
		root, err := os.OpenRoot(base)
		if err != nil {
			done <- result{err: failure("attachment", "cannot open checkout: "+err.Error())}
			return
		}
		defer root.Close()
		content, err := readWorkspaceFile(root, source)
		done <- result{content, err}
	}()
	select {
	case <-ctx.Done():
		return protocol.FilePreview{}, failure("attachment", "preview timed out")
	case res := <-done:
		if res.err != nil {
			return protocol.FilePreview{}, res.err
		}
		sum := sha256.Sum256(res.content)
		return protocol.FilePreview{Source: source, Name: filepath.Base(source), Content: string(res.content), Size: int64(len(res.content)), SHA256: hex.EncodeToString(sum[:])}, nil
	}
}

func usesArtifacts(c protocol.Command) bool {
	if c.Kind != "thread.start" && c.Kind != "prompt.send" && c.Kind != "prompt.reopen-send" {
		return false
	}
	for _, a := range c.Attachments {
		if a.ArtifactID != "" {
			return true
		}
	}
	return false
}

// captureArtifacts replaces client-supplied metadata with stored records. It
// checks readiness only; the command transaction performs the binding.
func captureArtifacts(s protocol.Snapshot, c protocol.Command, store *storage.Store) (protocol.Command, error) {
	threadID := c.ThreadID
	if c.Kind == "thread.start" {
		threadID = ""
	}
	attachments := make([]protocol.Attachment, len(c.Attachments))
	copy(attachments, c.Attachments)
	for i, a := range attachments {
		if a.ArtifactID == "" {
			continue
		}
		label := a.Name
		if label == "" {
			label = a.ArtifactID
		}
		if a.Kind == "workspace-file" || a.Content != "" {
			return c, attachmentFailure(label, "an uploaded attachment cannot also supply a source or content")
		}
		record, ok, err := store.Artifact(a.ArtifactID)
		if err != nil {
			return c, err
		}
		if ok && record.Unavailable {
			// A restored file clears the flag; a still-missing one rejects.
			if _, f, openErr := store.OpenArtifact(record.ID); openErr == nil {
				f.Close()
				record.Unavailable = false
			}
		}
		if !ok || record.Unavailable || (record.State == "accepted" && (threadID == "" || record.ThreadID != threadID)) {
			return c, attachmentFailure(label, "attachment is missing, expired or unavailable; attach it again")
		}
		next := protocol.Attachment{Kind: "file", Name: record.Name, Source: a.Source, ArtifactID: record.ID, MediaType: record.MediaType, Size: record.Size, SHA256: record.SHA256, Width: record.Width, Height: record.Height, PreviewSHA256: a.PreviewSHA256}
		// A differing preview is identified on the accepted capture, not blocked.
		next.ChangedSincePreview = a.PreviewSHA256 != "" && a.PreviewSHA256 != record.SHA256
		if strings.HasPrefix(record.MediaType, "image/") {
			next.Kind = "image"
		} else if record.Size <= captureLimit {
			_, data, err := store.ReadArtifact(record.ID, captureLimit)
			if err != nil {
				return c, attachmentFailure(label, "attachment content is unavailable; attach it again")
			}
			if capturableText(data) {
				next.Content = string(data)
			}
		}
		attachments[i] = next
	}
	c.Attachments = attachments
	return c, nil
}

// validateArtifactDelivery rejects a Send whose uploaded attachments the
// thread's agent did not advertise support for, instead of dropping or
// converting them at dispatch. Text captures always travel as text.
func validateArtifactDelivery(s *protocol.Snapshot, c protocol.Command) error {
	if !usesArtifacts(c) {
		return nil
	}
	agentID := ""
	if c.Kind == "thread.start" {
		requested := c.Agent
		if requested == agent.FixtureName {
			requested = agent.FixtureID
		}
		agentID = requested
	} else if t := threadByID(s, c.ThreadID); t != nil {
		agentID = t.AgentID
	}
	if !agent.IsACP(agentID) {
		return nil
	}
	record := agent.Find(s, agentID)
	if record == nil {
		return failure("unsupported_agent", "this thread's agent is no longer configured")
	}
	has := func(capability string) bool {
		for _, reported := range record.Capabilities {
			if reported == capability {
				return true
			}
		}
		return false
	}
	largest := protocol.Attachment{}
	for _, a := range c.Attachments {
		if a.ArtifactID != "" && a.Size > largest.Size {
			largest = a
		}
		if a.ArtifactID == "" || a.Content != "" {
			continue
		}
		if strings.HasPrefix(a.MediaType, "image/") {
			if !has(agent.CapImagePrompt) {
				return attachmentFailure(a.Name, record.Name+" does not accept image prompts; remove the image to send")
			}
		} else if !has(agent.CapEmbeddedPrompt) {
			return attachmentFailure(a.Name, record.Name+" does not accept embedded files, including text over 64 KiB; remove the attachment to send")
		}
	}
	// The whole prompt must fit one ACP frame, so dispatch never fails on size
	// after the prompt has been accepted.
	size, err := agent.PromptPayloadBytes(protocol.Prompt{Text: c.Text, Attachments: c.Attachments}, "", agent.Info{Capabilities: record.Capabilities})
	if err != nil {
		return attachmentFailure(largest.Name, err.Error())
	}
	if size > acpPromptBudget {
		return attachmentFailure(largest.Name, fmt.Sprintf("the prompt with its attachments would be %d KiB; agents accept at most %d MiB per prompt", size>>10, acpPromptBudget>>20))
	}
	return nil
}

// sweepArtifacts runs retention; failures are logged, never fatal to serving.
func (e *engine) sweepArtifacts() {
	result, err := e.store.SweepArtifacts(time.Now(), stagedArtifactTTL)
	if err != nil {
		e.logf("artifact sweep failed", "error", err)
		return
	}
	if result != (storage.SweepResult{}) {
		e.logf("artifact sweep", "temporary", result.Temporary, "orphaned", result.Orphaned, "expired", result.Expired, "unavailable", result.Unavailable)
	}
}
