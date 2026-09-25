package client

import (
	"context"
	"net/http"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// filesCallTimeout exceeds the server's 5s Files budget so its own reply
// arrives first.
const filesCallTimeout = 8 * time.Second

func (c *Client) files() *Client {
	g := *c
	g.HTTP = &http.Client{Transport: c.HTTP.Transport, Timeout: filesCallTimeout}
	return &g
}

// FilesList lists one page of dir ("" for the checkout root) in the target
// checkout. Pass the previous page's Next as cursor for the following page.
// hidden includes dot names; .git is never listed.
func (c *Client) FilesList(ctx context.Context, target GitTarget, dir, cursor string, hidden bool) (protocol.FileList, error) {
	q := target.query()
	q.Set("dir", dir)
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if hidden {
		q.Set("hidden", "1")
	}
	var out protocol.FileList
	err := c.files().request(ctx, "GET", "/v1/files/list?"+q.Encode(), nil, &out)
	return out, err
}

// FilesRead reads one file's bounded, read-only content.
func (c *Client) FilesRead(ctx context.Context, target GitTarget, path string) (protocol.FileRead, error) {
	q := target.query()
	q.Set("path", path)
	var out protocol.FileRead
	err := c.files().request(ctx, "GET", "/v1/files/read?"+q.Encode(), nil, &out)
	return out, err
}

// FilesStat reports a path's kind and change token.
func (c *Client) FilesStat(ctx context.Context, target GitTarget, path string) (protocol.FileStat, error) {
	q := target.query()
	q.Set("path", path)
	var out protocol.FileStat
	err := c.request(ctx, "GET", "/v1/files/stat?"+q.Encode(), nil, &out)
	return out, err
}
