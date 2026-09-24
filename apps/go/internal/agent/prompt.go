package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// ArtifactLoader returns the verified bytes of an accepted artifact.
type ArtifactLoader func(id string) ([]byte, error)

// PromptBlocks converts a captured prompt into ACP content blocks. It returns
// the blocks and a list of notices for legacy attachments the connected agent
// cannot accept, so the omission is visible rather than silent.
//
// Captured file content is sent as an embedded resource only when the agent
// advertised embedded context; otherwise it is appended as fenced text, which
// every ACP agent must accept. The Send-time snapshot is used unchanged: a
// later change on disk never alters what was attached.
//
// Uploaded artifacts without text content travel as image blocks or embedded
// blob resources. Their support was validated at Send; if the live agent no
// longer advertises it, or the bytes cannot be loaded, an error is returned so
// the prompt stays queued instead of being sent without them.
func PromptBlocks(p protocol.Prompt, checkout string, info Info, load ArtifactLoader) ([]acp.ContentBlock, []string, error) {
	blocks := []acp.ContentBlock{acp.TextBlock(p.Text)}
	var notices []string
	embedded := info.Has(CapEmbeddedPrompt)
	for _, a := range p.Attachments {
		name := a.Name
		if name == "" && a.ArtifactID == "" {
			name = a.Source
		}
		if a.ArtifactID != "" && a.Content == "" {
			image := strings.HasPrefix(a.MediaType, "image/")
			if image && !info.Has(CapImagePrompt) {
				return nil, nil, fmt.Errorf("image attachment %s cannot be sent: this agent session did not advertise image prompt support", label(name))
			}
			if !image && !embedded {
				return nil, nil, fmt.Errorf("attachment %s cannot be sent: this agent session did not advertise embedded context support", label(name))
			}
			if load == nil {
				return nil, nil, fmt.Errorf("attachment %s cannot be loaded", label(name))
			}
			data, err := load(a.ArtifactID)
			if err != nil {
				return nil, nil, fmt.Errorf("attachment %s cannot be loaded: %w", label(name), err)
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			if image {
				blocks = append(blocks, acp.ImageBlock(encoded, a.MediaType))
				continue
			}
			mediaType := a.MediaType
			blocks = append(blocks, acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{Uri: artifactURI(a), Blob: encoded, MimeType: &mediaType}}))
			continue
		}
		if a.Kind == "image" {
			notices = append(notices, "Image attachment "+label(name)+" was not sent: this agent did not advertise image prompt support.")
			continue
		}
		if a.Content == "" {
			notices = append(notices, "Attachment "+label(name)+" was not sent: it carried no captured content.")
			continue
		}
		if embedded {
			blocks = append(blocks, acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{Uri: resourceURI(checkout, a), Text: a.Content}}))
			continue
		}
		blocks = append(blocks, acp.TextBlock(fenced(name, a.Content)))
	}
	return blocks, notices, nil
}

// artifactURI names an uploaded artifact by its identity. A client-supplied
// Source is display identity only and never becomes a file URI.
func artifactURI(a protocol.Attachment) string {
	return "attachment://" + a.ArtifactID + "/" + url.PathEscape(a.Name)
}

// PromptPayloadBytes is the JSON size of the content blocks PromptBlocks
// would build, computed without loading artifact bytes: their base64 length
// follows from the recorded size.
func PromptPayloadBytes(p protocol.Prompt, checkout string, info Info) (int, error) {
	extra := 0
	blocks, _, err := PromptBlocks(p, checkout, info, func(id string) ([]byte, error) {
		for _, a := range p.Attachments {
			if a.ArtifactID == id {
				extra += base64.StdEncoding.EncodedLen(int(a.Size))
			}
		}
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(blocks)
	return len(encoded) + extra, err
}

func resourceURI(checkout string, a protocol.Attachment) string {
	if a.ArtifactID != "" {
		return artifactURI(a)
	}
	source := a.Source
	if source == "" {
		source = a.Name
	}
	if checkout != "" && !strings.Contains(checkout, "://") && !filepath.IsAbs(source) {
		return "file://" + filepath.Join(checkout, source)
	}
	if filepath.IsAbs(source) {
		return "file://" + source
	}
	return "attachment:" + source
}

// fenced wraps captured content in a fence long enough that the content cannot
// close it early.
func fenced(name, content string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return "Attached file " + name + ":\n" + fence + "\n" + content + "\n" + fence
}
