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
// (protocol.GitGroup*). The path must currently appear in that group. An
// empty path with the staged or unstaged group fetches that whole group.
func (c *Client) GitDiff(ctx context.Context, target GitTarget, path, group string) (protocol.GitDiff, error) {
	q := target.query()
	q.Set("path", path)
	q.Set("group", group)
	var out protocol.GitDiff
	err := c.git().request(ctx, "GET", "/v1/git/diff?"+q.Encode(), nil, &out)
	return out, err
}

// GitLog fetches up to limit commits in topological order; limit <= 0 uses
// the server default (50) and the server caps it at 200. scope is
// protocol.GitLogScopeHead (HEAD plus its upstream; also used when empty)
// or protocol.GitLogScopeAll (every branch and remote-tracking ref).
func (c *Client) GitLog(ctx context.Context, target GitTarget, limit int, scope string) (protocol.GitLog, error) {
	q := target.query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if scope != "" {
		q.Set("scope", scope)
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

// GitBranches lists local branches then remote-tracking refs (at most 500).
func (c *Client) GitBranches(ctx context.Context, target GitTarget) (protocol.GitBranches, error) {
	var out protocol.GitBranches
	err := c.git().request(ctx, "GET", "/v1/git/branches?"+target.query().Encode(), nil, &out)
	return out, err
}

// GitCompare compares head against base. Each must be HEAD, a full ref name
// from GitBranches (refs/heads/..., refs/remotes/...) or a full commit hash.
func (c *Client) GitCompare(ctx context.Context, target GitTarget, base, head string) (protocol.GitCompare, error) {
	q := target.query()
	q.Set("base", base)
	q.Set("head", head)
	var out protocol.GitCompare
	err := c.git().request(ctx, "GET", "/v1/git/compare?"+q.Encode(), nil, &out)
	return out, err
}

// gitWriteTimeout exceeds the server's 5 minute budget for a Git write and
// its hooks, so the server's own outcome arrives first.
const gitWriteTimeout = 6 * time.Minute

// GitWrite submits a git.* command built by one of the Git*Command helpers
// and waits for its outcome. Assign cmd.ID once and reuse the same command
// on retry: the server never runs Git twice for one ID, and a retry returns
// the recorded receipt (waiting if the first attempt is still running).
//
// A refusal before Git runs (stale pins, busy checkout, invalid input) is
// returned as a *protocol.Error and nothing is recorded, so correct the input
// and send a new command with a new ID. Otherwise the receipt's Git field
// carries the result: State succeeded (possibly with a warning Code), failed
// or outcome_unknown. See protocol/git_write.go for every code.
func (c *Client) GitWrite(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error) {
	g := *c
	g.HTTP = &http.Client{Transport: c.HTTP.Transport, Timeout: gitWriteTimeout}
	return g.Command(ctx, cmd)
}

func gitCommand(id, kind string, target GitTarget, w protocol.GitWrite) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: kind, ThreadID: target.ThreadID, ProjectID: target.ProjectID, Git: &w}
}

func gitPin(e protocol.GitStatusEntry) []protocol.GitPathPin {
	return []protocol.GitPathPin{{Path: e.Path, Group: e.Group, Pin: e.Pin}}
}

// GitStageCommand stages one unstaged or untracked status entry.
func GitStageCommand(id string, target GitTarget, entry protocol.GitStatusEntry) protocol.Command {
	return gitCommand(id, protocol.GitKindStage, target, protocol.GitWrite{Paths: gitPin(entry)})
}

// GitUnstageCommand unstages one staged status entry (both sides of a rename).
func GitUnstageCommand(id string, target GitTarget, entry protocol.GitStatusEntry) protocol.Command {
	return gitCommand(id, protocol.GitKindUnstage, target, protocol.GitWrite{Paths: gitPin(entry)})
}

// GitDiscardCommand permanently discards one unstaged or untracked entry.
// Build it only after the user confirmed exactly this entry.
func GitDiscardCommand(id string, target GitTarget, entry protocol.GitStatusEntry) protocol.Command {
	return gitCommand(id, protocol.GitKindDiscard, target, protocol.GitWrite{Paths: gitPin(entry), Confirmed: true})
}

// GitCommitCommand commits the staged set shown in status with message.
// amend replaces status's HEAD commit; acknowledgePublished must be true when
// status.HeadOnUpstream was shown and the user accepted rewriting it.
func GitCommitCommand(id string, target GitTarget, status protocol.GitStatus, message string, amend, acknowledgePublished bool) protocol.Command {
	head := status.HeadOid
	if head == "" {
		head = protocol.GitUnbornHead
	}
	return gitCommand(id, protocol.GitKindCommit, target, protocol.GitWrite{
		Message: message, Amend: amend, ExpectedHead: head,
		StagedFingerprint: status.StagedFingerprint, AcknowledgePublished: acknowledgePublished,
	})
}
