package client

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// GitOperationRefreshReview reads the operation like GitOperation and asks
// the server to recompute an attached resolution job's review
// (GET /v1/git/operation?review=refresh, ADR 0023 S4).
func (c *Client) GitOperationRefreshReview(ctx context.Context, target GitTarget) (protocol.GitOperationState, error) {
	q := target.query()
	q.Set("review", "refresh")
	var out protocol.GitOperationState
	err := c.git().request(ctx, "GET", "/v1/git/operation?"+q.Encode(), nil, &out)
	return out, err
}
