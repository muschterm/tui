package client

import (
	"context"
	"net/url"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func (c *Client) Browse(ctx context.Context, request protocol.BrowseRequest) (protocol.BrowseResult, error) {
	q := url.Values{"scope": {request.Scope}, "project_id": {request.ProjectID}, "thread_id": {request.ThreadID}, "query": {request.Query}}
	var result protocol.BrowseResult
	err := c.request(ctx, "GET", "/v1/browse?"+q.Encode(), nil, &result)
	return result, err
}
