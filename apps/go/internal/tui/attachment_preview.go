package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/muschterm/tui/apps/go/internal/client"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// attachment_preview.go loads viewer content off the render path: draft
// workspace files through the server's Send-capture rules, draft copied files
// from the local disk, and artifact bodies from the server. Results apply only
// to the viewer open that requested them; a draft preview records its digest
// as PreviewSHA256 on that same draft attachment.

const (
	previewTextLimit  = 64 << 10
	artifactTextLimit = 1 << 20
	previewTimeout    = 15 * time.Second
)

type viewerLoadMsg struct {
	id        uint64
	content   string
	mediaType string
	size      int64
	sha       string
	// unavailable explains why no content is shown (binary or too large).
	unavailable string
	// data is an image's bytes, kept only when kitty graphics can show it.
	data []byte
	err  error
}

// textMedia reports whether a media type is presented as text.
func textMedia(mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(strings.SplitN(mt, ";", 2)[0]))
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || mt == "application/xml" ||
		mt == "application/javascript" || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

func imageAttachment(a protocol.Attachment) bool {
	return a.Kind == "image" || strings.HasPrefix(strings.ToLower(a.MediaType), "image/")
}

func previewableText(data []byte) bool {
	return len(data) <= previewTextLimit && utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
}

// loadViewerPreview starts the viewer's asynchronous load, if its attachment
// needs one. It never blocks rendering; the viewer shows a loading state.
func (m *Model) loadViewerPreview(source string, index int) tea.Cmd {
	vw := m.viewer
	a := vw.att
	api := m.artifactClient()
	ctx := m.ctx
	var load func() viewerLoadMsg
	graphics := m.graphics.Supported()
	switch {
	case a.ArtifactID != "" && imageAttachment(a):
		// Unsupported terminals show the metadata fallback without fetching.
		if api == nil || !graphics {
			return nil
		}
		id := a.ArtifactID
		load = func() viewerLoadMsg {
			c, cancel := context.WithTimeout(ctx, previewTimeout)
			defer cancel()
			data, mt, err := api.FetchArtifact(c, id, graphicsByteLimit)
			if err != nil {
				return viewerLoadMsg{err: err}
			}
			return viewerLoadMsg{mediaType: mt, size: int64(len(data)), data: data}
		}
	case vw.draft && a.Kind == "workspace-file":
		if api == nil {
			return nil
		}
		owner := m.currentOwner()
		load = func() viewerLoadMsg {
			c, cancel := context.WithTimeout(ctx, previewTimeout)
			defer cancel()
			p, err := api.PreviewFile(c, owner.projectID, owner.threadID, a.Source)
			return viewerLoadMsg{content: p.Content, size: p.Size, sha: p.SHA256, err: err}
		}
	case vw.draft && a.Kind == "copied-file":
		path := a.Source
		load = func() viewerLoadMsg {
			c, cancel := context.WithTimeout(ctx, localReadTimeout)
			defer cancel()
			data, err := readLocalFile(c, path, client.ArtifactLimit)
			if err != nil {
				return viewerLoadMsg{err: err}
			}
			sum := sha256.Sum256(data)
			msg := viewerLoadMsg{size: int64(len(data)), sha: hex.EncodeToString(sum[:]), mediaType: http.DetectContentType(data)}
			switch {
			case previewableText(data):
				msg.content, msg.mediaType = string(data), "text/plain; charset=utf-8"
			case strings.HasPrefix(msg.mediaType, "image/"):
				if graphics {
					msg.data = data
				}
			case utf8.Valid(data) && len(data) > previewTextLimit:
				msg.unavailable = "Preview unavailable · text larger than 64 KiB"
			default:
				msg.unavailable = "Preview unavailable for " + msg.mediaType
			}
			return msg
		}
	case a.ArtifactID != "" && a.Content == "" && !imageAttachment(a):
		if api == nil {
			return nil
		}
		if a.MediaType != "" && !textMedia(a.MediaType) {
			vw.unavailable = "Preview unavailable for " + safe(singleLine(a.MediaType))
			return nil
		}
		id := a.ArtifactID
		load = func() viewerLoadMsg {
			c, cancel := context.WithTimeout(ctx, previewTimeout)
			defer cancel()
			data, mt, err := api.FetchArtifact(c, id, artifactTextLimit)
			if err != nil {
				return viewerLoadMsg{err: err}
			}
			msg := viewerLoadMsg{mediaType: mt, size: int64(len(data))}
			if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
				msg.content = string(data)
			} else {
				msg.unavailable = "Preview unavailable for " + mt
			}
			return msg
		}
	default:
		return nil
	}
	m.viewerSeq++
	id := m.viewerSeq
	vw.loadID, vw.loading = id, true
	vw.source, vw.index = source, index
	vw.owner = m.currentOwner()
	return func() tea.Msg {
		msg := load()
		msg.id = id
		return msg
	}
}

// acceptViewerLoad applies a load to the viewer open that requested it, and
// records a draft preview's digest on that same, unchanged attachment.
func (m *Model) acceptViewerLoad(msg viewerLoadMsg) tea.Cmd {
	vw := m.viewer
	if vw == nil || vw.loadID != msg.id || !vw.loading {
		return nil
	}
	vw.loading = false
	vw.cacheKey, vw.cacheLines, vw.clean = "", nil, nil
	if msg.err != nil {
		vw.loadErr = artifactErrorText(msg.err)
		return nil
	}
	vw.att.Content = msg.content
	vw.loaded = true
	if msg.mediaType != "" && vw.att.MediaType == "" {
		vw.att.MediaType = msg.mediaType
	}
	if msg.size > 0 {
		vw.att.Size = msg.size
	}
	vw.unavailable = safe(singleLine(msg.unavailable))
	var cmd tea.Cmd
	if msg.data != nil && imageAttachment(vw.att) {
		vw.imgData, vw.imgBox, vw.imgPrep = msg.data, graphicsBox{}, nil
		cmd = m.syncViewerImage()
	}
	if !vw.draft || msg.sha == "" {
		return cmd
	}
	v := m.ownerView(vw.owner)
	if v == nil || vw.index < 0 || vw.index >= len(v.Attachments) {
		return cmd
	}
	d := &v.Attachments[vw.index]
	if d.Kind == vw.att.Kind && d.Name == vw.att.Name && d.Source == vw.att.Source && d.ArtifactID == "" {
		if d.PreviewSHA256 != msg.sha {
			d.PreviewSHA256 = msg.sha
			m.markDirty()
		}
	}
	return cmd
}

// viewerImageBody is the image viewer's state text. renderViewerImage paints
// the image or its styled fallback instead; this text marks the body as a
// non-text state (no gutter, no selectable lines).
func (m *Model) viewerImageBody(width int) []string {
	a := m.viewer.att
	lines := []string{"Image preview unavailable"}
	var meta []string
	if a.Width > 0 && a.Height > 0 {
		meta = append(meta, fmt.Sprintf("%d × %d px", a.Width, a.Height))
	}
	if a.MediaType != "" {
		meta = append(meta, safe(singleLine(a.MediaType)))
	} else if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(a.Name)), "."); ext != "" {
		meta = append(meta, safe(singleLine(ext)))
	}
	if len(meta) > 0 {
		lines = append(lines, strings.Join(meta, " · "))
	}
	_ = width
	return lines
}
