package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// ArtifactLimit is the server's per-artifact upload bound; FetchArtifact never
// reads more than this.
const ArtifactLimit = 16 << 20

// transfer is bounded by ctx rather than the default short request timeout,
// since artifact bodies are up to ArtifactLimit bytes.
func (c *Client) transfer(ctx context.Context, method, path, contentType string, body io.Reader, length int64) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.Discovery.URL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		r.ContentLength = length
	}
	r.Header.Set("Authorization", "Bearer "+c.Discovery.Token)
	r.Header.Set("X-TUI-Protocol", "1")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	transport := &http.Client{Transport: c.HTTP.Transport}
	resp, err := transport.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var pe protocol.Error
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&pe) == nil && pe.Code != "" {
			return nil, &pe
		}
		return nil, fmt.Errorf("server HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// UploadArtifact stages data as an artifact. name and mediaType are claims;
// the server sniffs the stored media type. Cancel ctx to abandon the upload.
func (c *Client) UploadArtifact(ctx context.Context, name, mediaType string, data []byte) (protocol.ArtifactInfo, error) {
	var info protocol.ArtifactInfo
	if len(data) > ArtifactLimit {
		return info, &protocol.Error{Code: "capacity", Message: fmt.Sprintf("attachments are limited to %d MiB", ArtifactLimit>>20)}
	}
	q := url.Values{"name": {name}, "media_type": {mediaType}}
	resp, err := c.transfer(ctx, http.MethodPost, "/v1/artifacts?"+q.Encode(), "application/octet-stream", bytesReader(data), int64(len(data)))
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&info)
	return info, err
}

// FetchArtifact returns an artifact's bytes and media type, reading at most
// limit bytes (capped at ArtifactLimit).
func (c *Client) FetchArtifact(ctx context.Context, id string, limit int64) ([]byte, string, error) {
	if limit <= 0 || limit > ArtifactLimit {
		limit = ArtifactLimit
	}
	resp, err := c.transfer(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(id), "", nil, 0)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > limit {
		return nil, "", &protocol.Error{Code: "capacity", Message: "artifact exceeds the requested read bound"}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", &protocol.Error{Code: "capacity", Message: "artifact exceeds the requested read bound"}
	}
	sum := sha256.Sum256(data)
	if want := resp.Header.Get("X-TUI-Artifact-SHA256"); want == "" || hex.EncodeToString(sum[:]) != want {
		return nil, "", &protocol.Error{Code: "unavailable", Message: "artifact content did not match its recorded digest"}
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// DeleteArtifact removes a staged attachment the draft no longer uses, which
// frees staged quota. Sent attachments stay with their thread (an "accepted"
// error); an unknown id reports "not_found", so callers may treat that as done.
func (c *Client) DeleteArtifact(ctx context.Context, id string) error {
	resp, err := c.transfer(ctx, http.MethodDelete, "/v1/artifacts/"+url.PathEscape(id), "", nil, 0)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// PreviewFile reads a draft workspace-file attachment under the Send capture
// rules. Exactly one of projectID and threadID selects the checkout; path is
// checkout-relative. Keep the returned SHA256 as the attachment's
// PreviewSHA256 so Send can report whether the file changed since.
func (c *Client) PreviewFile(ctx context.Context, projectID, threadID, path string) (protocol.FilePreview, error) {
	q := url.Values{"project_id": {projectID}, "thread_id": {threadID}, "path": {path}}
	var preview protocol.FilePreview
	err := c.request(ctx, http.MethodGet, "/v1/preview?"+q.Encode(), nil, &preview)
	return preview, err
}

func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }
