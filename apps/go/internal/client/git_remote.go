package client

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Ref and remote actions (ADR 0021; wire contract and codes in
// protocol/git_write.go). Build a command with one of the helpers below from
// exactly what the user was shown, assign its ID once, and send it with
// GitWrite (branch create, switch, soft reset) or GitSync (fetch, pull,
// push); reuse the same command on retry. A refusal is a *protocol.Error and
// nothing ran; otherwise Receipt.Git carries the result, including its Ref,
// Fetch, Integration or Push part.

// gitSyncTimeout exceeds the server's 15 minute budget for fetch, pull and
// push, so the server's own outcome arrives first.
const gitSyncTimeout = 16 * time.Minute

// GitSync submits git.fetch, git.pull or git.push and waits for the outcome.
// While it runs, Snapshot.GitOps shows the command with Progress and
// Cancellable; send GitCancelCommand from another goroutine to cancel it.
func (c *Client) GitSync(ctx context.Context, cmd protocol.Command) (protocol.Receipt, error) {
	g := *c
	g.HTTP = &http.Client{Transport: c.HTTP.Transport, Timeout: gitSyncTimeout}
	return g.Command(ctx, cmd)
}

func headPin(status protocol.GitStatus) string {
	if status.HeadOid == "" {
		return protocol.GitUnbornHead
	}
	return status.HeadOid
}

func refCommand(id, kind string, target GitTarget, ref protocol.GitRefWrite) protocol.Command {
	return gitCommand(id, kind, target, protocol.GitWrite{Ref: &ref})
}

func syncCommand(id, kind string, target GitTarget, sync protocol.GitSync) protocol.Command {
	return gitCommand(id, kind, target, protocol.GitWrite{Sync: &sync})
}

// carry is the AcknowledgeCarry value for status: the number of entries the
// user was shown, once they accepted carrying them.
func carry(status protocol.GitStatus, acknowledged bool) int {
	if !acknowledged {
		return 0
	}
	return len(status.Entries)
}

// GitBranchCreateCommand creates branch name at startOid (a full commit
// hash, for example a selected GitCommit.Hash) without switching to it.
func GitBranchCreateCommand(id string, target GitTarget, name, startOid string) protocol.Command {
	return refCommand(id, protocol.GitKindBranchCreate, target, protocol.GitRefWrite{Name: name, StartOid: startOid})
}

// GitSwitchCommand switches to the existing local branch whose tip was shown
// as targetOid. status is the status the user reviewed; when it lists
// entries, acknowledgeCarry must be true only after the user accepted that
// those len(status.Entries) changes are carried to the branch (Git refuses,
// with would_overwrite, when one would be overwritten).
func GitSwitchCommand(id string, target GitTarget, status protocol.GitStatus, branch, targetOid string, acknowledgeCarry bool) protocol.Command {
	return refCommand(id, protocol.GitKindSwitch, target, protocol.GitRefWrite{
		Branch: branch, TargetOid: targetOid, ExpectedBranch: status.Branch, ExpectedHead: headPin(status),
		WorktreeFingerprint: protocol.GitWorktreeFingerprint(status), AcknowledgeCarry: carry(status, acknowledgeCarry),
	})
}

// GitSwitchCreateCommand creates branch name at startOid and switches to it,
// with the same carry rules as GitSwitchCommand.
func GitSwitchCreateCommand(id string, target GitTarget, status protocol.GitStatus, name, startOid string, acknowledgeCarry bool) protocol.Command {
	return refCommand(id, protocol.GitKindSwitch, target, protocol.GitRefWrite{
		Name: name, StartOid: startOid, ExpectedBranch: status.Branch, ExpectedHead: headPin(status),
		WorktreeFingerprint: protocol.GitWorktreeFingerprint(status), AcknowledgeCarry: carry(status, acknowledgeCarry),
	})
}

// GitResetSoftCommand soft-resets the current branch (or detached HEAD) shown
// in status to targetOid. acknowledgePublished is needed when commits
// leaving the branch are on a remote-tracking ref (published_commit), and
// acknowledgeNotAncestor when the target is not on the branch's history
// (not_ancestor); ask the user only after the server refused with that code.
func GitResetSoftCommand(id string, target GitTarget, status protocol.GitStatus, targetOid string, acknowledgePublished, acknowledgeNotAncestor bool) protocol.Command {
	return refCommand(id, protocol.GitKindResetSoft, target, protocol.GitRefWrite{
		TargetOid: targetOid, ExpectedBranch: status.Branch, ExpectedHead: headPin(status),
		AcknowledgePublished: acknowledgePublished, AcknowledgeNotAncestor: acknowledgeNotAncestor,
	})
}

// GitUndoResetSoftCommand undoes a succeeded soft reset: a soft reset back
// to its Ref.PreviousHead, pinned to the branch and head the reset left, so
// it is refused as stale when anything moved since. It carries no
// acknowledgement: a back-to-descendant undo needs none, and any refusal
// (published_commit, not_ancestor) is asked like an ordinary reset. ok is
// false when result is not a succeeded soft reset.
func GitUndoResetSoftCommand(id string, target GitTarget, result protocol.GitResult) (cmd protocol.Command, ok bool) {
	r := result.Ref
	if result.Op != "reset_soft" || result.State != protocol.GitStateSucceeded || r == nil || r.PreviousHead == "" || r.Head == "" {
		return protocol.Command{}, false
	}
	return refCommand(id, protocol.GitKindResetSoft, target, protocol.GitRefWrite{
		TargetOid: r.PreviousHead, ExpectedBranch: r.Branch, ExpectedHead: r.Head,
	}), true
}

// GitFetchCommand fetches remote, or the current branch's upstream remote
// when remote is empty.
func GitFetchCommand(id string, target GitTarget, remote string) protocol.Command {
	return syncCommand(id, protocol.GitKindFetch, target, protocol.GitSync{Remote: remote})
}

// GitFetchAllCommand fetches every configured remote (capability
// git-fetch-all); prune also removes remote-tracking refs whose branch is
// gone, and without it pruning is off whatever the configuration says.
func GitFetchAllCommand(id string, target GitTarget, prune bool) protocol.Command {
	return syncCommand(id, protocol.GitKindFetch, target, protocol.GitSync{All: true, Prune: prune})
}

// GitPullCommand fetches the upstream shown in status and fast-forwards the
// branch when possible; it never merges or rebases (diverged is reported).
func GitPullCommand(id string, target GitTarget, status protocol.GitStatus) protocol.Command {
	return syncCommand(id, protocol.GitKindPull, target, protocol.GitSync{
		Upstream: status.Upstream, ExpectedBranch: status.Branch, ExpectedHead: status.HeadOid,
	})
}

// GitPushCommand pushes the branch shown in status to its upstream, never
// forced. expectedUpstreamOid is optional: the remote-tracking tip the user
// saw (for example a fetch result's Fetch.UpstreamAfter), refused as
// stale_upstream when it has moved.
func GitPushCommand(id string, target GitTarget, status protocol.GitStatus, expectedUpstreamOid string) protocol.Command {
	return syncCommand(id, protocol.GitKindPush, target, protocol.GitSync{
		Upstream: status.Upstream, ExpectedBranch: status.Branch, ExpectedHead: status.HeadOid, ExpectedUpstreamOid: expectedUpstreamOid,
	})
}

// GitCancelCommand requests cancellation of the running command commandID
// (GitOp.Cancellable). Send it with Command; it is not journaled, so a new
// ID per request is fine.
func GitCancelCommand(id, commandID string) protocol.Command {
	return protocol.Command{Version: protocol.Version, ID: id, Kind: protocol.GitKindCancel, Git: &protocol.GitWrite{Cancel: &protocol.GitCancel{CommandID: commandID}}}
}

// GitLeaveCommitsCount extracts the number of commits a refused switch from
// a detached HEAD would leave behind (code leaves_commits; the count leads
// the message). Show it, and after the user accepts, resend the switch
// with GitAcknowledgeLeaveCommits under a new command ID.
func GitLeaveCommitsCount(err error) (int, bool) {
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "leaves_commits" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.SplitN(pe.Message, " ", 2)[0])
	return n, err == nil && n > 0
}

// GitAcknowledgeLeaveCommits returns a switch command that accepts leaving
// count commits reachable only from a detached HEAD.
func GitAcknowledgeLeaveCommits(cmd protocol.Command, count int) protocol.Command {
	if cmd.Git != nil && cmd.Git.Ref != nil {
		ref := *cmd.Git.Ref
		ref.AcknowledgeLeaveCommits = count
		w := *cmd.Git
		w.Ref = &ref
		cmd.Git = &w
	}
	return cmd
}
