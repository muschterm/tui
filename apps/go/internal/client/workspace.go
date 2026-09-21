package client

import (
	"context"
	"github.com/muschterm/tui/apps/go/internal/protocol"
	"net/url"
)

func (c *Client) Workspace(ctx context.Context, projectID, threadID string) (protocol.WorkspaceInfo, error) {
	q := url.Values{}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	if threadID != "" {
		q.Set("thread_id", threadID)
	}
	var info protocol.WorkspaceInfo
	err := c.request(ctx, "GET", "/v1/workspace?"+q.Encode(), nil, &info)
	return info, err
}
