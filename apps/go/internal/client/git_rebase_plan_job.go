package client

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Agent-planned interactive rebase (ADR 0027; wire contract in
// protocol/git_rebase_plan_job.go). Start a planning job with
// GitRebasePlanStartCommand (sent with GitWrite), follow it in the job
// thread's Job.Proposal summary, read the proposal with GitRebaseProposal
// and load a proposed one's Entries into the plan editor. Starting the
// rebase is an ordinary GitRebaseInteractiveCommand built from a fresh
// plan read, with the user's own acknowledgements.

func rebasePlanCommand(id, kind string, target GitTarget, job protocol.GitRebasePlanJob) protocol.Command {
	return gitCommand(id, kind, target, protocol.GitWrite{RebasePlan: &job})
}

// GitRebasePlanStartCommand asks agentID (with settings, nil for the
// agent's defaults) to plan an interactive rebase of the checked-out branch
// from base (optionally onto onto), as for GitRebasePlan. fingerprint is
// the plan read the user saw ("" to skip that check); instructions are the
// user's request.
func GitRebasePlanStartCommand(id string, target GitTarget, agentID string, settings *protocol.Settings, base, onto, fingerprint, instructions string) protocol.Command {
	return rebasePlanCommand(id, protocol.GitKindRebasePlanStart, target, protocol.GitRebasePlanJob{
		AgentID: agentID, Settings: settings, Base: base, Onto: onto, Fingerprint: fingerprint, Instructions: instructions,
	})
}

// GitRebasePlanReviseCommand asks the job's agent for another plan: the
// latest proposal's errors or checkout changes are sent with instructions.
// revision is the proposal revision the user saw.
func GitRebasePlanReviseCommand(id string, target GitTarget, jobID string, revision int64, instructions string) protocol.Command {
	return rebasePlanCommand(id, protocol.GitKindRebasePlanRevise, target, protocol.GitRebasePlanJob{JobID: jobID, ExpectedRevision: revision, Instructions: instructions})
}

// GitRebasePlanCancelCommand stops the job's running turn.
func GitRebasePlanCancelCommand(id string, target GitTarget, jobID string) protocol.Command {
	return rebasePlanCommand(id, protocol.GitKindRebasePlanCancel, target, protocol.GitRebasePlanJob{JobID: jobID})
}

// GitRebasePlanEndCommand deletes the job thread and its proposal.
func GitRebasePlanEndCommand(id string, target GitTarget, jobID string) protocol.Command {
	return rebasePlanCommand(id, protocol.GitKindRebasePlanEnd, target, protocol.GitRebasePlanJob{JobID: jobID})
}

// GitRebaseProposal reads a planning job's proposal (with a fresh plan
// read: State is stale when the branch changed since it was pinned).
func (c *Client) GitRebaseProposal(ctx context.Context, target GitTarget, jobID string) (protocol.GitRebaseProposal, error) {
	q := target.query()
	q.Set("job", jobID)
	var out protocol.GitRebaseProposal
	err := c.git().request(ctx, "GET", "/v1/git/rebase/proposal?"+q.Encode(), nil, &out)
	return out, err
}

// GitRebaseProposals lists the planning jobs of the target's repository.
func (c *Client) GitRebaseProposals(ctx context.Context, target GitTarget) ([]protocol.GitRebaseProposalInfo, error) {
	var out []protocol.GitRebaseProposalInfo
	err := c.git().request(ctx, "GET", "/v1/git/rebase/proposals?"+target.query().Encode(), nil, &out)
	return out, err
}

// ProposalLoadable reports whether a proposal read may be loaded into the
// plan editor: proposed, with entries, and not stale.
func ProposalLoadable(p protocol.GitRebaseProposal) bool {
	return p.State == protocol.GitProposalProposed && len(p.Entries) > 0
}
