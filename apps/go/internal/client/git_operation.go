package client

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Merge and rebase (ADR 0023; wire contract and codes in
// protocol/git_write.go). Read the operation in progress with GitOperation
// and a planned merge or rebase with GitIntegratePreview, build a command
// with one of the helpers below from exactly what the user was shown, assign
// its ID once and send it with GitWrite; reuse the same command on retry. A
// refusal is a *protocol.Error and nothing ran; otherwise Receipt.Git carries
// the result and Receipt.Git.Operation what happened to the operation
// (Outcome, and State when one is still in progress).

// GitOperation reads the merge, rebase, cherry-pick, revert or bisect in
// progress in the target checkout (Kind empty when none), with its unmerged
// paths, side labels and which commands are available (Can).
func (c *Client) GitOperation(ctx context.Context, target GitTarget) (protocol.GitOperationState, error) {
	var out protocol.GitOperationState
	err := c.git().request(ctx, "GET", "/v1/git/operation?"+target.query().Encode(), nil, &out)
	return out, err
}

// GitIntegratePreview reports what merging (kind protocol.GitOperationMerge)
// or rebasing onto (protocol.GitOperationRebase) target would do: target is a
// full ref (GitBranch.Ref, or the upstream) or a full commit hash.
func (c *Client) GitIntegratePreview(ctx context.Context, target GitTarget, kind, ref string) (protocol.GitIntegratePreview, error) {
	q := target.query()
	q.Set("kind", kind)
	q.Set("target", ref)
	var out protocol.GitIntegratePreview
	err := c.git().request(ctx, "GET", "/v1/git/integrate/preview?"+q.Encode(), nil, &out)
	return out, err
}

// GitOperationBackupFile reads one file of an abort or skip backup
// (protocol.GitOperationBackup.Oid for the working-tree copy, IndexOid for
// the staged copy), read-only.
func (c *Client) GitOperationBackupFile(ctx context.Context, target GitTarget, oid, path string) (protocol.GitBackupFile, error) {
	q := target.query()
	q.Set("oid", oid)
	q.Set("path", path)
	var out protocol.GitBackupFile
	err := c.git().request(ctx, "GET", "/v1/git/operation/backup?"+q.Encode(), nil, &out)
	return out, err
}

func integrateCommand(id, kind string, target GitTarget, status protocol.GitStatus, preview protocol.GitIntegratePreview, acknowledgePublished bool) protocol.Command {
	i := protocol.GitIntegrate{Source: preview.Source, TargetRef: preview.TargetRef, TargetOid: preview.TargetOid,
		ExpectedBranch: status.Branch, ExpectedHead: headPin(status)}
	if kind == protocol.GitKindRebase {
		i.ExpectedReplayCount, i.AcknowledgePublished = preview.ReplayCount, acknowledgePublished
	}
	return gitCommand(id, kind, target, protocol.GitWrite{Integrate: &i})
}

// GitMergeCommand merges the preview's target (a merge preview) into the
// branch shown in status. The branch needs a clean tracked tree; nothing is
// stashed.
func GitMergeCommand(id string, target GitTarget, status protocol.GitStatus, preview protocol.GitIntegratePreview) protocol.Command {
	return integrateCommand(id, protocol.GitKindMerge, target, status, preview, false)
}

// GitRebaseCommand rebases the branch shown in status onto the preview's
// target (a rebase preview), replaying preview.ReplayCount commits.
// acknowledgePublished must be true only after the user accepted rewriting
// published commits (preview.Published).
func GitRebaseCommand(id string, target GitTarget, status protocol.GitStatus, preview protocol.GitIntegratePreview, acknowledgePublished bool) protocol.Command {
	return integrateCommand(id, protocol.GitKindRebase, target, status, preview, acknowledgePublished)
}

func operationCommand(id, kind string, target GitTarget, state protocol.GitOperationState, w protocol.GitOperationWrite) protocol.Command {
	w.OperationID, w.Kind, w.ExpectedHead, w.Confirmed = state.OperationID, state.Kind, state.HeadOid, true
	return gitCommand(id, kind, target, protocol.GitWrite{Operation: &w})
}

// GitOperationAbortCommand aborts the operation in state, discarding its
// resolution work; send it only after the user confirmed that. When
// state.DiscardsOnAbort is not empty the confirmation must name those files,
// whose changes the abort also resets, and acknowledgeDiscard must be true
// only after the user accepted that.
//
// For a cherry-pick or revert sequence (state.AbortDropsCount > 0) the
// abort also removes the commits the sequence already made; the
// confirmation must name them (state.AbortDropsCommits, and the count when
// state.AbortDropsIncomplete), and acknowledgeDropped must be true only
// after the user accepted that.
func GitOperationAbortCommand(id string, target GitTarget, state protocol.GitOperationState, acknowledgeDiscard, acknowledgeDropped bool) protocol.Command {
	w := protocol.GitOperationWrite{WorktreeFingerprint: state.WorktreeFingerprint}
	if acknowledgeDiscard {
		// The same confirmation names the files the backup cannot hold.
		w.DiscardsFingerprint, w.AcknowledgeBackupMissing = state.DiscardsOnAbortFingerprint, state.BackupMissingOnAbortFingerprint
	}
	if acknowledgeDropped {
		w.AcknowledgeDropped = state.AbortDropsFingerprint
	}
	return operationCommand(id, protocol.GitKindOperationAbort, target, state, w)
}

// GitOperationContinueCommand continues the operation in state once
// state.Can.Continue is allowed (every conflict resolved and staged); send
// it only after the user reviewed what is staged. When state.MarkerPaths is
// not empty, acknowledgeMarkers must be true only after the user accepted
// committing those files with conflict markers in them.
func GitOperationContinueCommand(id string, target GitTarget, state protocol.GitOperationState, acknowledgeMarkers bool) protocol.Command {
	w := protocol.GitOperationWrite{ExpectedStep: state.Step, UnmergedFingerprint: state.UnmergedFingerprint, StagedFingerprint: state.StagedFingerprint}
	if acknowledgeMarkers {
		w.MarkersFingerprint = state.MarkersFingerprint
	}
	return operationCommand(id, protocol.GitKindOperationContinue, target, state, w)
}

// GitOperationSkipCommand drops the commit a rebase is stopped at
// (state.Current) and continues; send it only after the user confirmed
// dropping that commit and, when state.DiscardsOnSkip is not empty, resetting
// those files (acknowledgeDiscard). ok is false when state is not a rebase
// stopped at a conflicting commit.
func GitOperationSkipCommand(id string, target GitTarget, state protocol.GitOperationState, acknowledgeDiscard bool) (cmd protocol.Command, ok bool) {
	if state.Kind != protocol.GitOperationRebase || state.Current == nil || state.StopReason != "" {
		return protocol.Command{}, false
	}
	w := protocol.GitOperationWrite{ExpectedStep: state.Step, SkipOid: state.Current.Oid, WorktreeFingerprint: state.WorktreeFingerprint}
	if acknowledgeDiscard {
		w.DiscardsFingerprint, w.AcknowledgeBackupMissing = state.DiscardsOnSkipFingerprint, state.BackupMissingOnSkipFingerprint
	}
	return operationCommand(id, protocol.GitKindOperationSkip, target, state, w), true
}
