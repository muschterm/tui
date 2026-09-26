package client

import (
	"context"
	"net/url"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Managed worktrees (ADR 0024; capabilities worktree-create and
// worktree-manage).

// WorktreeStartCommand is thread.start in a new worktree: a new branch
// named branch at the commit startOid (a full object ID). The receipt is
// "accepted" (TargetID is the thread) or "failed" with Receipt.Error; a
// failed start that left an unattached worktree is attached by retrying the
// same command (same ID and content).
func WorktreeStartCommand(id, projectID, agentID, text string, settings protocol.Settings, attachments []protocol.Attachment, startOid, branch string) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "thread.start", ProjectID: projectID, Agent: agentID, Text: text,
		Settings: &settings, Attachments: attachments, Workspace: &protocol.WorkspaceRequest{Mode: "worktree", StartOid: startOid, Branch: branch}}
}

// CheckoutStartCommand is thread.start in the project's checkout, chosen
// explicitly (the workspace default is not consulted).
func CheckoutStartCommand(id, projectID, agentID, text string, settings protocol.Settings, attachments []protocol.Attachment) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "thread.start", ProjectID: projectID, Agent: agentID, Text: text,
		Settings: &settings, Attachments: attachments, Workspace: &protocol.WorkspaceRequest{Mode: "checkout"}}
}

// ReceiptError is the failure a two-phase receipt records, or nil.
func ReceiptError(r protocol.Receipt) error {
	if r.State == "failed" && r.Error != nil {
		return r.Error
	}
	return nil
}

// WorktreeRelocateCommand follows a worktree Git registers elsewhere
// (state moved); its idle threads' checkouts move with it.
func WorktreeRelocateCommand(id, worktreeID string) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "worktree.relocate", Worktree: &protocol.WorktreeAction{ID: worktreeID}}
}

// WorktreeForgetCommand drops the application's record of a worktree that
// no longer exists where it was; nothing on disk changes.
func WorktreeForgetCommand(id, worktreeID string) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "worktree.forget", Worktree: &protocol.WorktreeAction{ID: worktreeID}}
}

// WorktreeRemoveCommand removes the worktree the preview describes (its
// directory, including the ignored entries it counts); the branch is kept.
func WorktreeRemoveCommand(id string, preview protocol.WorktreeRemoval) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "worktree.remove", Worktree: &protocol.WorktreeAction{ID: preview.ID, Confirm: preview.Fingerprint}}
}

// WorktreePruneCommand removes the stale registrations the preview lists
// from the project's repository.
func WorktreePruneCommand(id, projectID string, preview protocol.WorktreePrune) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: "worktree.prune", ProjectID: projectID, Worktree: &protocol.WorktreeAction{Confirm: preview.Fingerprint}}
}

// WorktreeRemoval previews worktree.remove.
func (c *Client) WorktreeRemoval(ctx context.Context, worktreeID string) (protocol.WorktreeRemoval, error) {
	var out protocol.WorktreeRemoval
	err := c.git().request(ctx, "GET", "/v1/worktrees/removal?"+url.Values{"id": {worktreeID}}.Encode(), nil, &out)
	return out, err
}

// WorktreePrune previews worktree.prune.
func (c *Client) WorktreePrune(ctx context.Context, projectID string) (protocol.WorktreePrune, error) {
	var out protocol.WorktreePrune
	err := c.git().request(ctx, "GET", "/v1/worktrees/prune?"+url.Values{"project_id": {projectID}}.Encode(), nil, &out)
	return out, err
}
