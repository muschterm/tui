package client

import (
	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Agent conflict resolution (ADR 0023, S4; wire contract in
// protocol/git_write.go). Send each command with GitWrite (start) or
// Command (the others); assign its ID once and reuse it on retry. Review the
// result in GitOperationState.Review, then accept a path with
// GitConflictResolveCommand (built from the reviewed item) or reject it with
// GitConflictRestoreCommand (the item's CopyID).

func resolveJobCommand(id, kind string, target GitTarget, job protocol.GitResolveJob) protocol.Command {
	return gitCommand(id, kind, target, protocol.GitWrite{ResolveJob: &job})
}

// GitResolveJobStartCommand starts a resolution job on the operation in
// state (its OperationID; empty adopts an operation started elsewhere) with
// agentID and settings (nil for the agent's defaults). paths limits the job
// to some conflicted paths (nil for all); instructions are appended to the
// prompt.
func GitResolveJobStartCommand(id string, target GitTarget, state protocol.GitOperationState, agentID string, settings *protocol.Settings, paths []string, instructions string) protocol.Command {
	return resolveJobCommand(id, protocol.GitKindResolveJobStart, target, protocol.GitResolveJob{
		OperationID: state.OperationID, AgentID: agentID, Settings: settings, Paths: paths, Instructions: instructions,
	})
}

// GitResolveJobFollowupCommand sends another prompt to the job.
func GitResolveJobFollowupCommand(id string, target GitTarget, operationID, prompt string) protocol.Command {
	return resolveJobCommand(id, protocol.GitKindResolveJobFollowup, target, protocol.GitResolveJob{OperationID: operationID, Prompt: prompt})
}

// GitResolveJobCancelCommand interrupts the job's running turn.
func GitResolveJobCancelCommand(id string, target GitTarget, operationID string) protocol.Command {
	return resolveJobCommand(id, protocol.GitKindResolveJobCancel, target, protocol.GitResolveJob{OperationID: operationID})
}

// GitResolveJobEndCommand detaches the job and removes its thread. The
// content gate on continue stays (GitOperationState.AgentChanges).
func GitResolveJobEndCommand(id string, target GitTarget, operationID string) protocol.Command {
	return resolveJobCommand(id, protocol.GitKindResolveJobEnd, target, protocol.GitResolveJob{OperationID: operationID})
}

// AcknowledgeAgentChanges marks a continue command as sent after the
// user reviewed the index changes no decision of theirs explains
// (changes.Items) and accepts committing them.
func AcknowledgeAgentChanges(cmd *protocol.Command, changes *protocol.GitAgentChanges) {
	if cmd.Git != nil && cmd.Git.Operation != nil && changes != nil {
		cmd.Git.Operation.AcknowledgeAgentChanges = changes.Fingerprint
	}
}

// GitReviewAcceptCommand stages a reviewed item's working file as the
// resolution (git.conflict_resolve pinned to what the review showed).
func GitReviewAcceptCommand(id string, target GitTarget, item protocol.GitResolveItem, acknowledgeAsIs bool) protocol.Command {
	w := protocol.GitConflictWrite{Path: item.Path, As: protocol.GitConflictAsContent, ConflictPin: item.ConflictPin, WorktreeToken: item.WorktreeToken}
	if item.Staged {
		// The agent already staged it: keep exactly the reviewed index entry.
		w.As = protocol.GitConflictAsKeepStaged
		return conflictCommand(id, protocol.GitKindConflictResolve, target, w)
	}
	if item.Deleted {
		w.As = protocol.GitConflictAsDeleted
	} else if acknowledgeAsIs {
		w.AcknowledgeMarkers = item.WorktreeToken
		if item.Binary {
			w.AcknowledgeBinary = item.WorktreeToken
		}
	}
	return conflictCommand(id, protocol.GitKindConflictResolve, target, w)
}

// GitReviewRejectCommand restores a reviewed item from its saved copy
// (git.conflict_restore).
func GitReviewRejectCommand(id string, target GitTarget, item protocol.GitResolveItem) protocol.Command {
	return conflictCommand(id, protocol.GitKindConflictRestore, target, protocol.GitConflictWrite{
		Path: item.Path, CopyID: item.CopyID, ConflictPin: item.ConflictPin, WorktreeToken: item.WorktreeToken,
	})
}
