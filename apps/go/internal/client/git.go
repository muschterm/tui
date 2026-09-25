package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// gitCallTimeout bounds each Git read. It exceeds the server's 10s request
// budget so the server's own "unavailable" reply arrives before the client
// gives up; the shared client's 5s timeout is too short for large checkouts.
const gitCallTimeout = 15 * time.Second

// git returns a copy of c sharing its transport with the longer timeout.
func (c *Client) git() *Client {
	g := *c
	g.HTTP = &http.Client{Transport: c.HTTP.Transport, Timeout: gitCallTimeout}
	return &g
}

// GitTarget selects the checkout for a Git read: set exactly one field.
type GitTarget struct{ ProjectID, ThreadID string }

func (t GitTarget) query() url.Values {
	q := url.Values{}
	if t.ProjectID != "" {
		q.Set("project_id", t.ProjectID)
	}
	if t.ThreadID != "" {
		q.Set("thread_id", t.ThreadID)
	}
	return q
}

// GitStatus fetches the target checkout's read-only status. A non-Git,
// fixture or unavailable target succeeds with no entries; inspect
// status.Workspace.State.
func (c *Client) GitStatus(ctx context.Context, target GitTarget) (protocol.GitStatus, error) {
	var out protocol.GitStatus
	err := c.git().request(ctx, "GET", "/v1/git/status?"+target.query().Encode(), nil, &out)
	return out, err
}

// GitDiff fetches the bounded patch for one path in one status group
// (protocol.GitGroup*). The path must currently appear in that group.
func (c *Client) GitDiff(ctx context.Context, target GitTarget, path, group string) (protocol.GitDiff, error) {
	q := target.query()
	q.Set("path", path)
	q.Set("group", group)
	var out protocol.GitDiff
	err := c.git().request(ctx, "GET", "/v1/git/diff?"+q.Encode(), nil, &out)
	return out, err
}

// GitLog fetches up to limit commits from HEAD; limit <= 0 uses the server
// default (50) and the server caps it at 200.
func (c *Client) GitLog(ctx context.Context, target GitTarget, limit int) (protocol.GitLog, error) {
	q := target.query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out protocol.GitLog
	err := c.git().request(ctx, "GET", "/v1/git/log?"+q.Encode(), nil, &out)
	return out, err
}

// GitShow fetches one commit's metadata and bounded stat/patch text. commit
// must be a full or abbreviated hexadecimal hash.
func (c *Client) GitShow(ctx context.Context, target GitTarget, commit string) (protocol.GitShow, error) {
	q := target.query()
	q.Set("commit", commit)
	var out protocol.GitShow
	err := c.git().request(ctx, "GET", "/v1/git/show?"+q.Encode(), nil, &out)
	return out, err
}
