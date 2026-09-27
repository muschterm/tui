package client

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Interactive rebase (ADR 0026; wire contract in protocol/git_rebase.go).
// Read a plan with GitRebasePlan, arrange its entries (DefaultRebaseEntries
// is every commit picked in order; protocol.ValidateRebasePlan checks an
// arrangement locally), build the start with GitRebaseInteractiveCommand and
// send it with GitWrite. Stops use the operation commands of
// git_operation.go; GitOperationContinueMessageCommand continues with a
// message and GitOperationCommitCommand commits while stopped. Needs the
// capability git-rebase-interactive.

// GitRebasePlan reads the commits an interactive rebase of the checked-out
// branch would rewrite: base is a full ref (the upstream, for example), a
// full commit hash (the parent of a chosen commit) or "root"; onto is empty
// (onto base) or a full ref or hash.
func (c *Client) GitRebasePlan(ctx context.Context, target GitTarget, base, onto string) (protocol.GitRebasePlan, error) {
	q := target.query()
	q.Set("base", base)
	if onto != "" {
		q.Set("onto", onto)
	}
	var out protocol.GitRebasePlan
	err := c.git().request(ctx, "GET", "/v1/git/rebase/plan?"+q.Encode(), nil, &out)
	return out, err
}

// DefaultRebaseEntries is plan's commits picked in their order, merges left
// out (a rebase drops them).
func DefaultRebaseEntries(plan protocol.GitRebasePlan) []protocol.GitRebaseEntry {
	entries := make([]protocol.GitRebaseEntry, 0, len(plan.Commits))
	for _, c := range plan.Commits {
		if !c.Merge {
			entries = append(entries, protocol.GitRebaseEntry{Action: protocol.GitRebasePick, Commit: c.Oid})
		}
	}
	return entries
}

// RebaseOptions are the choices a confirmation collects: UpdateRefs moves
// plan.UpdateRefs with their commits; AcknowledgeMerges (required when
// plan.MergeCount > 0) and AcknowledgePublished (when plan.Published) must
// be true only after the user accepted dropping merges and rewriting
// published commits.
type RebaseOptions struct {
	UpdateRefs, AcknowledgeMerges, AcknowledgePublished bool
}

// GitRebaseInteractiveCommand starts an interactive rebase of plan (as read,
// with its fingerprint) running entries.
func GitRebaseInteractiveCommand(id string, target GitTarget, plan protocol.GitRebasePlan, entries []protocol.GitRebaseEntry, o RebaseOptions) protocol.Command {
	i := protocol.GitIntegrate{ExpectedBranch: plan.Branch, ExpectedHead: plan.HeadOid, AcknowledgePublished: o.AcknowledgePublished,
		Interactive: &protocol.GitRebaseInteractive{Base: plan.Base, Onto: plan.Onto, Fingerprint: plan.Fingerprint, Entries: entries,
			UpdateRefs: o.UpdateRefs, AcknowledgeMerges: o.AcknowledgeMerges}}
	return gitCommand(id, protocol.GitKindRebase, target, protocol.GitWrite{Integrate: &i})
}

// GitOperationContinueMessageCommand is GitOperationContinueCommand for an
// application interactive rebase with message for the commit this stop
// makes or amends (state.Interactive.Stop edit_amend with staged changes,
// edit_reset, message, commit_failed or conflict); an empty message keeps
// the stored or original one.
func GitOperationContinueMessageCommand(id string, target GitTarget, state protocol.GitOperationState, acknowledgeMarkers bool, message string) protocol.Command {
	c := GitOperationContinueCommand(id, target, state, acknowledgeMarkers)
	c.Git.Operation.Message = message
	return c
}

// GitOperationCommitCommand commits what is staged while an application
// interactive rebase is stopped for editing or at a break
// (state.Interactive.Stop edit_amend, edit_reset or break), staying
// stopped; at edit_reset the commit keeps the edited commit's author.
func GitOperationCommitCommand(id string, target GitTarget, state protocol.GitOperationState, message string) protocol.Command {
	w := protocol.GitOperationWrite{ExpectedStep: state.Step, StagedFingerprint: state.StagedFingerprint, Message: message}
	return operationCommand(id, protocol.GitKindOperationCommit, target, state, w)
}
