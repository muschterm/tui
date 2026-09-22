package agent

import (
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// PromptBlocks converts a captured prompt into ACP content blocks. It returns
// the blocks and a list of notices for attachments the connected agent cannot
// accept, so the omission is visible rather than silent.
//
// Captured file content is sent as an embedded resource only when the agent
// advertised embedded context; otherwise it is appended as fenced text, which
// every ACP agent must accept. The Send-time snapshot is used unchanged: a
// later change on disk never alters what was attached.
func PromptBlocks(p protocol.Prompt, checkout string, info Info) ([]acp.ContentBlock, []string) {
	blocks := []acp.ContentBlock{acp.TextBlock(p.Text)}
	var notices []string
	embedded := info.Has(CapEmbeddedPrompt)
	for _, a := range p.Attachments {
		name := a.Name
		if name == "" {
			name = a.Source
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
	return blocks, notices
}

func resourceURI(checkout string, a protocol.Attachment) string {
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
