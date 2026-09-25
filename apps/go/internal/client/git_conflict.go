package client

import (
	"context"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Manual conflict resolution (ADR 0023, S3; wire contract in
// protocol/git_write.go). Read a version with GitConflictFile, build a
// command from what the user was shown, assign its ID once and send it with
// GitWrite.

// GitConflictFile reads one version (protocol.GitConflictVersion*) of a
// conflicted path, with the path's current pins.
func (c *Client) GitConflictFile(ctx context.Context, target GitTarget, path, version string) (protocol.GitConflictFile, error) {
	q := target.query()
	q.Set("path", path)
	q.Set("version", version)
	var out protocol.GitConflictFile
	err := c.git().request(ctx, "GET", "/v1/git/conflict?"+q.Encode(), nil, &out)
	return out, err
}

func conflictCommand(id, kind string, target GitTarget, w protocol.GitConflictWrite) protocol.Command {
	return gitCommand(id, kind, target, protocol.GitWrite{Conflict: &w})
}

// GitConflictChooseCommand writes side (protocol.GitConflictSide*) of the
// conflict as shown into the working tree; the path stays unmerged.
func GitConflictChooseCommand(id string, target GitTarget, conflict protocol.GitConflict, side string) protocol.Command {
	return conflictCommand(id, protocol.GitKindConflictChoose, target, protocol.GitConflictWrite{
		Path: conflict.Path, Side: side, ConflictPin: conflict.ConflictPin, WorktreeToken: conflict.WorktreeStat,
	})
}

// GitConflictResolveCommand stages the reviewed working-tree file (as
// protocol.GitConflictAsContent) or resolves the path as deleted.
// working is the GitConflictFile (version working) the user reviewed; when
// it reported HasMarkers, MarkersUnknown or Binary (markers cannot be
// checked), acknowledgeAsIs must be true only after the user accepted
// staging it as it is.
func GitConflictResolveCommand(id string, target GitTarget, working protocol.GitConflictFile, as string, acknowledgeAsIs bool) protocol.Command {
	w := protocol.GitConflictWrite{Path: working.Path, As: as, ConflictPin: working.ConflictPin, WorktreeToken: working.WorktreeToken}
	if acknowledgeAsIs && as == protocol.GitConflictAsContent {
		w.AcknowledgeMarkers = working.WorktreeToken
		if working.Binary {
			w.AcknowledgeBinary = working.WorktreeToken
		}
	}
	return conflictCommand(id, protocol.GitKindConflictResolve, target, w)
}

// GitConflictRestoreCommand puts a saved copy of a path back (working tree
// and index entries). file is any GitConflictFile of the path read now (for
// its pins); copyID is file.CopyID (the original conflict) or one of
// file.Copies. What the restore replaces is copied first
// (GitOperationResult.Previous).
func GitConflictRestoreCommand(id string, target GitTarget, file protocol.GitConflictFile, copyID string) protocol.Command {
	return conflictCommand(id, protocol.GitKindConflictRestore, target, protocol.GitConflictWrite{
		Path: file.Path, CopyID: copyID, ConflictPin: file.ConflictPin, WorktreeToken: file.WorktreeToken,
	})
}
