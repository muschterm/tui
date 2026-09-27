package protocol

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Interactive rebase (ADR 0026). A client reads a plan with GET
// /v1/git/rebase/plan, lets the user (or an agent, later) arrange its
// entries, and starts it with git.rebase carrying GitIntegrate.Interactive.
// The server validates the plan with ValidateRebasePlan against a fresh read
// whose Fingerprint must still match, and drives `git rebase -i` with
// server-owned editor helpers, so no editor ever runs for the user. Stops,
// conflicts, Continue, Skip and Abort use the ADR 0023 operation commands;
// GitOperationState.Interactive describes the stop and
// git.operation_commit commits staged changes while stopped (splitting a
// commit).

// GitKindOperationCommit commits the staged changes while an application
// interactive rebase is stopped for editing or at a break, and stays
// stopped (splitting a commit, or inserting one). Payload: GitOperationWrite
// with Kind rebase, ExpectedHead, ExpectedStep, StagedFingerprint, Message
// and Confirmed.
const GitKindOperationCommit = "git.operation_commit"

// Plan entry actions (GitRebaseEntry.Action).
const (
	GitRebasePick   = "pick"
	GitRebaseReword = "reword"
	GitRebaseEdit   = "edit"
	GitRebaseSquash = "squash"
	GitRebaseFixup  = "fixup"
	GitRebaseDrop   = "drop"
	GitRebaseBreak  = "break"
)

// Edit modes (GitRebaseEntry.EditMode). Reset (the default, Sublime Merge's
// "edit commit contents") stops with the commit soft-reset into the index,
// so it can be re-committed or split; amend stops with the commit applied,
// and Continue amends it with whatever is staged.
const (
	GitRebaseEditReset = "reset"
	GitRebaseEditAmend = "amend"
)

// Fixup variants (GitRebaseEntry.Fixup): "" melds the commit into the one
// before and discards its message; C keeps this commit's message instead;
// c uses the stored Message of the chain (Git's `fixup -c`).
const (
	GitRebaseFixupUseMessage  = "C"
	GitRebaseFixupEditMessage = "c"
)

// Limits of a plan.
const (
	GitRebasePlanCommitsMax = 1000
	GitRebaseBreaksMax      = 100
	GitRebaseMessageMax     = 64 << 10
)

// GitRebasePlan is GET /v1/git/rebase/plan?base=<full ref, full hash,
// "root" or "upstream">&onto=<full ref or full hash, optional> (plus the
// target): the
// commits an interactive rebase of the checked-out branch would rewrite and
// what it needs to be started, computed without changing anything.
//
// Base is where the rewritten range starts (exclusive): the branch's
// upstream to rebase onto it, the parent of a chosen commit for "interactive
// rebase from here", or "root" for every commit of the branch (`--root`).
// base=upstream resolves the checked-out branch's configured upstream (a
// remote-tracking or local branch) on the server: Base is then its full
// ref and BaseIsUpstream set; a branch without one is refused
// (no_upstream), and one whose configured upstream does not exist here (a
// remote branch never fetched or pruned, a deleted local branch) is refused
// with upstream_missing, naming the configured ref. Onto is the commit the rewritten commits are replayed on; it defaults to
// Base (OntoOid empty for root without an onto: Git then starts from a new
// empty root). Commits lists Base..HEAD oldest first, in the order Git's todo
// uses; Merge commits in it are never rebased (without --rebase-merges Git
// drops merges and linearises their side commits), so a plan lists every
// non-merge commit exactly once and needs AcknowledgeMerges when
// MergeCount > 0. Published reports that a commit in the range is on a
// remote-tracking ref (unknown counts as published); the start then needs
// AcknowledgePublished. UpdateRefs lists the local branches (other than the
// checked-out one and those checked out in another worktree) that point at
// a non-merge commit of the range: with the plan's UpdateRefs toggle on,
// they move with the rewritten commits (--update-refs). Fingerprint pins
// the branch, HEAD, base, onto, the commit list and UpdateRefs; the start
// sends it back and a changed repository is refused (stale_plan). Blocked is
// the refusal code a start would return now (detached, operation_in_progress,
// dirty_tree, empty_range, too_many_commits, would_overwrite, not_supported,
// ...), with BlockedMessage; a Blocked plan still lists its commits.
type GitRebasePlan struct {
	Branch         string                `json:"branch,omitempty"`
	HeadOid        string                `json:"head_oid,omitempty"`
	Base           string                `json:"base"`
	BaseOid        string                `json:"base_oid,omitempty"`
	BaseLabel      string                `json:"base_label,omitempty"`
	BaseIsUpstream bool                  `json:"base_is_upstream,omitempty"`
	Root           bool                  `json:"root,omitempty"`
	Onto           string                `json:"onto,omitempty"`
	OntoOid        string                `json:"onto_oid,omitempty"`
	OntoLabel      string                `json:"onto_label,omitempty"`
	Commits        []GitRebasePlanCommit `json:"commits"`
	MergeCount     int                   `json:"merge_count,omitempty"`
	Published      bool                  `json:"published,omitempty"`
	UpdateRefs     []GitRebaseUpdateRef  `json:"update_refs,omitempty"`
	// UpdateRefsUnsupported (additive) names local branches pointing into
	// the range whose names cannot be handled here (not valid UTF-8, for
	// example); starting with UpdateRefs is then refused
	// (update_refs_unsupported), without it they stay where they are.
	UpdateRefsUnsupported []string `json:"update_refs_unsupported,omitempty"`
	Fingerprint           string   `json:"fingerprint,omitempty"`
	Blocked               string   `json:"blocked,omitempty"`
	BlockedMessage        string   `json:"blocked_message,omitempty"`
}

// GitRebasePlanCommit is one commit of a plan. Subject is the first line of
// the message and Body the rest (after the separating blank line), both
// untrusted text; BodyTruncated reports a Body cut at 16 KiB. AuthorDate is
// strict ISO 8601. Merge commits (more than one parent) cannot be entries.
type GitRebasePlanCommit struct {
	Oid           string   `json:"oid"`
	Parents       []string `json:"parents,omitempty"`
	Subject       string   `json:"subject"`
	Body          string   `json:"body,omitempty"`
	BodyTruncated bool     `json:"body_truncated,omitempty"`
	AuthorName    string   `json:"author_name,omitempty"`
	AuthorEmail   string   `json:"author_email,omitempty"`
	AuthorDate    string   `json:"author_date,omitempty"`
	Published     bool     `json:"published,omitempty"`
	Merge         bool     `json:"merge,omitempty"`
	// Message (additive) is the commit's full raw message, byte for byte
	// (UTF-8), at most GitRebaseMessageMax bytes; MessageTruncated reports
	// a message cut at that limit. Clients send it unchanged for an
	// unedited message; empty from servers that do not fill it. A
	// truncated Message must never be sent back as a plan message, edited
	// or not (the server cannot tell it from a complete one): clients must
	// require a fully rewritten message for such a commit.
	Message          string `json:"message,omitempty"`
	MessageTruncated bool   `json:"message_truncated,omitempty"`
}

// GitRebaseUpdateRef is a local branch (full ref) and the commit it points
// at now.
type GitRebaseUpdateRef struct {
	Ref string `json:"ref"`
	Oid string `json:"oid"`
}

// GitRebaseEntry is one line of a plan, in the order it runs:
//
//   - pick, reword, edit, squash, fixup, drop name Commit (a full hash of a
//     non-merge commit of the plan); break has no Commit.
//   - reword needs Message (the new message).
//   - squash and fixup meld Commit into the commit produced by the entry
//     directly before, which must be pick, reword, edit, squash or fixup.
//     Such an entry and the squash/fixup entries following it form a chain;
//     when a chain contains a squash or a `fixup -c`, its last entry carries
//     the chain's Message (the message of the combined commit); no other
//     squash or fixup entry carries one.
//   - fixup's Fixup is "" (keep the earlier message), "C" (use Commit's
//     message) or "c" (use the chain's Message).
//   - edit's EditMode is reset (default) or amend.
//
// Every non-merge commit of the plan appears exactly once; drop is always
// explicit.
type GitRebaseEntry struct {
	Action   string `json:"action"`
	Commit   string `json:"commit,omitempty"`
	Message  string `json:"message,omitempty"`
	EditMode string `json:"edit_mode,omitempty"`
	Fixup    string `json:"fixup,omitempty"`
}

// GitRebaseInteractive is GitIntegrate.Interactive: a plan to run as an
// interactive rebase of the branch GitIntegrate pins (ExpectedBranch and
// ExpectedHead; Source, TargetRef, TargetOid and ExpectedReplayCount stay
// empty). Base and Onto are exactly as sent to GET /v1/git/rebase/plan, and
// Fingerprint as it returned. UpdateRefs moves the plan's UpdateRefs
// branches with their commits (default off). AcknowledgeMerges accepts
// that merge commits in the range are dropped and their side commits
// linearised (required when the plan's MergeCount > 0);
// GitIntegrate.AcknowledgePublished accepts rewriting published commits.
type GitRebaseInteractive struct {
	Base              string           `json:"base"`
	Onto              string           `json:"onto,omitempty"`
	Fingerprint       string           `json:"fingerprint"`
	Entries           []GitRebaseEntry `json:"entries"`
	UpdateRefs        bool             `json:"update_refs,omitempty"`
	AcknowledgeMerges bool             `json:"acknowledge_merges,omitempty"`
}

// Interactive stops (GitRebaseProgress.Stop) of an application interactive
// rebase, derived from the repository and the plan entry's edit mode:
//
//	conflict       unmerged paths; resolve, stage, Continue (or Skip)
//	edit_amend     an edit entry in amend mode, also after its pick needed
//	               resolving (the server then commits the resolution itself
//	               and stops, ServerEdit), or a reset that could not
//	               be made (a root commit): Continue amends the commit
//	               with what is staged, keeping its message byte for byte
//	               unless Message replaces it. After commits were made at
//	               the stop (git.operation_commit), Continue refuses staged
//	               leftovers (staged_changes) and otherwise continues.
//	edit_reset     an edit entry in reset mode (also after its pick needed
//	               resolving, ServerEdit): the commit is soft-reset into the
//	               index. Commit parts with git.operation_commit;
//	               Continue commits what is still staged (Message, else the
//	               original message byte for byte, with the original
//	               author). With nothing staged and no commit made at the
//	               stop, Continue is refused (nothing_to_recommit): stage
//	               the changes to recommit them, or Abort.
//	break          paused by a break entry; git.operation_commit inserts
//	               commits; Continue needs nothing staged
//	message        a reword, squash or fixup step did not finish its
//	               message; Continue retries it (Message overrides the
//	               stored one)
//	commit_failed  Git stopped before committing a step without conflicts
//	               (its changes are staged); Continue commits it
//	rescheduled    Git could not start the step (for example an untracked
//	               file in the way) and put it back in the todo list; Continue
//	               retries it once the obstacle is gone
//	committed      the step's resolution or staged changes were committed at
//	               the stop (in a terminal, or by the server just before a
//	               restart); Continue goes on without committing, Skip is
//	               refused, and the commit is listed for Abort
//	empty          the commit of a pick, reword or edit step became empty:
//	               Continue keeps it as an empty commit (the plan's
//	               message for a reword, else the original message; the
//	               original author; byte for byte) and goes on without
//	               stopping to edit it, Skip drops it
//	unknown        anything else; Abort, or use a terminal
//
// Continue's Message applies only where the continue commits this step
// with its own message: pick, reword and edit steps, and the last squash or
// fixup of a chain whose plan carries the combined message; elsewhere
// (inside a chain, a fixup keeping the earlier message, a break, an empty
// commit) it is refused (invalid), so a plan's messages cannot change at a
// stop. Without a Message, a commit made at a stop uses the plan's message
// for that step, else exactly the message Git would have used without the
// stop (the step commit's original message; for a plain fixup, the message
// accumulated so far), never Git's commented editor text.
//
// Commits made at stops (git.operation_commit, the commit Continue makes
// at edit stops and empty stops, a reworded amend) are recorded
// (GitRebaseRecord.StopCommits). An Abort would discard them, so while any
// exist the state lists them in AbortDropsCommits/AbortDropsCount (with a
// commit changed at an amend stop outside the application) and Abort needs
// AcknowledgeDropped equal to AbortDropsFingerprint; before aborting, the
// server points the ref GitOperationResult.BackupRef
// (refs/tui-go/rebase-backup/<id>) at the rebase's HEAD so the work stays
// recoverable (`git log <ref>`; delete it with `git update-ref -d`).
const (
	GitRebaseStopConflict     = "conflict"
	GitRebaseStopEditAmend    = "edit_amend"
	GitRebaseStopEditReset    = "edit_reset"
	GitRebaseStopBreak        = "break"
	GitRebaseStopMessage      = "message"
	GitRebaseStopCommitFailed = "commit_failed"
	GitRebaseStopCommitted    = "committed"
	GitRebaseStopRescheduled  = "rescheduled"
	GitRebaseStopEmpty        = "empty"
	GitRebaseStopUnknown      = "unknown"
)

// Commit failures (GitRebaseProgress.Failure, GitRebaseFailure.Code), as
// reported by the command that left the stop (also a start that Git
// refused: GitResult.Code):
//
//	signing_failed  a commit could not be signed (commit.gpgSign); fix
//	                signing and Continue, or Abort
//	hook_rejected   a hook (Hook) refused the commit (or pre-rebase the
//	                start); Continue retries it
//	helper_failed   the server's editor helper failed (Detail), for example
//	                a message missing for a step; Continue retries, with a
//	                Message when one was missing
const (
	GitRebaseSigningFailed = "signing_failed"
	GitRebaseHookRejected  = "hook_rejected"
	GitRebaseHelperFailed  = "helper_failed"
)

// GitRebaseProgress is GitOperationState.Interactive for a merge-backend
// rebase in progress (whoever started it), read from Git's state files: Done
// todo lines executed including the current one, Remaining still to run,
// Command and CommandOid the current line (for example "edit" or
// "fixup -C"), Amend the commit an edit stop applied (Git's amend marker)
// and AmendParent its parent,
// Staged and Unstaged whether the index or tracked working-tree files differ
// (read with the operation's full state only), and UpdateRefs the branches
// Git moves when it finishes (with the commit each points at now).
//
// Plan is set when this is an application interactive rebase (its record
// carries the plan). Stop then classifies the stop (GitRebaseStop*), Entry
// is the index of the plan entry stopped at (-1 when none, for example an
// update-ref line), and Failure, Hook and Detail repeat the commit failure
// the latest command reported for this very stop.
type GitRebaseProgress struct {
	Done       int    `json:"done"`
	Remaining  int    `json:"remaining"`
	Command    string `json:"command,omitempty"`
	CommandOid string `json:"command_oid,omitempty"`
	Amend      string `json:"amend,omitempty"`
	// AmendParent (additive) is Amend's first parent: at an edit_reset stop
	// HEAD equals it until a commit is made at the stop.
	AmendParent string `json:"amend_parent,omitempty"`
	// Step facts (additive, ADR 0026), for a pick, reword or edit step
	// stopped without conflicts: StepCommitted reports that Git had made
	// the step's commit when it stopped (a reword whose message step then
	// failed, an edit), StepEmpty that it had not because the commit became
	// empty on HEAD (Current is then the step's commit), and PreHead the
	// HEAD before the step's commit. For an application rebase they are
	// the facts recorded when the stop was first observed
	// (GitRebaseRecord.Snapshot), not re-derived later; Late reports that
	// no observation right after Git stopped was recorded (a server crash),
	// so actions that could duplicate or drop a commit (Continue and Skip
	// at empty and message stops) are refused: Abort, or use a terminal.
	StepCommitted bool   `json:"step_committed,omitempty"`
	StepEmpty     bool   `json:"step_empty,omitempty"`
	PreHead       string `json:"pre_head,omitempty"`
	Late          bool   `json:"late,omitempty"`
	// StepMessage (additive) is the raw message the stopped step would
	// commit with (the plan's message, else the original or accumulated
	// one), for prefilling a Continue or commit message editor; at most
	// GitRebaseMessageMax bytes. Filled by GET /v1/git/operation and on
	// command results for application interactive rebases. When
	// StepMessageTruncated is set, the prefill is incomplete: a client must
	// never send it back, edited or not (the server cannot tell), and must
	// require a fully rewritten message instead.
	StepMessage          string `json:"step_message,omitempty"`
	StepMessageTruncated bool   `json:"step_message_truncated,omitempty"`
	// ServerEdit (additive) marks an edit stop the server made itself: the
	// step's pick needed resolving, Git would have gone on without
	// stopping, so Continue committed the resolution (Amend) and stopped
	// here as planned. It behaves exactly like Git's own edit stop.
	ServerEdit bool `json:"server_edit,omitempty"`
	// Next (additive) is the first remaining todo line ("pick <oid>").
	Next string `json:"next,omitempty"`
	// HeadAncestry is HEAD's recent first-parent history (server use only).
	HeadAncestry []GitOperationCommit `json:"-"`
	// The index state, the branches Git will move, and the application
	// plan's classification (see the type comment).
	Staged     bool                 `json:"staged,omitempty"`
	Unstaged   bool                 `json:"unstaged,omitempty"`
	UpdateRefs []GitRebaseUpdateRef `json:"update_refs,omitempty"`
	Plan       bool                 `json:"plan,omitempty"`
	Stop       string               `json:"stop,omitempty"`
	Entry      int                  `json:"entry,omitempty"`
	Failure    string               `json:"failure,omitempty"`
	Hook       string               `json:"hook,omitempty"`
	Detail     string               `json:"detail,omitempty"`
}

// GitRebaseRecord is GitOperationRecord.Interactive: what an application
// interactive rebase pinned. Fingerprint is the plan's; Base is "root" or
// BaseOid; Entries the plan's length; Lines maps each line of the pinned
// todo to its entry index (-1 for an update-ref line); UpdateRefs the
// branches it may move (with their commits before the rebase, which an
// Abort must leave in place); MergesDropped the merge commits it drops
// (acknowledged). Failure is the latest commit failure reported for a stop
// (signing, hook or helper), shown while the operation is still at that
// stop.
type GitRebaseRecord struct {
	Fingerprint   string               `json:"fingerprint"`
	Base          string               `json:"base"`
	BaseOid       string               `json:"base_oid,omitempty"`
	Root          bool                 `json:"root,omitempty"`
	Entries       int                  `json:"entries"`
	Lines         []int                `json:"lines,omitempty"`
	UpdateRefs    []GitRebaseUpdateRef `json:"update_refs,omitempty"`
	MergesDropped int                  `json:"merges_dropped,omitempty"`
	Failure       *GitRebaseFailure    `json:"failure,omitempty"`
	// EditModes (additive) is each todo line's edit mode (reset or amend
	// for an edit line, "" otherwise), parallel to Lines. StopCommits are
	// the commits made at stops, oldest first (at most 200).
	EditModes   []string             `json:"edit_modes,omitempty"`
	StopCommits []GitOperationCommit `json:"stop_commits,omitempty"`
	// Snapshot (additive) is the current stop as first observed.
	Snapshot *GitRebaseStopSnapshot `json:"snapshot,omitempty"`
	// EditStop (additive) is a server-made edit stop (GitRebaseProgress
	// .ServerEdit): its identity, the resolution commit and its parent.
	EditStop *GitRebaseEditStop `json:"edit_stop,omitempty"`
}

// GitRebaseEditStop is GitRebaseRecord.EditStop.
type GitRebaseEditStop struct {
	Done    int    `json:"done"`
	StepOid string `json:"step_oid"`
	Next    string `json:"next,omitempty"`
	Commit  string `json:"commit"`
	Parent  string `json:"parent"`
}

// GitRebaseStopSnapshot is what the server recorded when it first observed
// a stop: its identity (Done todo lines and the step commit StepOid), HEAD
// then (Head), the HEAD before the step's commit (PreHead), whether Git had
// committed the step (Committed) or the step had become empty (Empty), and
// whether the index had conflicts or staged changes (Conflict, Staged).
// Late marks a snapshot taken on a later read instead of right after Git
// stopped. Later reads classify the stop relative to it: a commit made at
// the stop (by the server or in a terminal) is recognised as made, never
// made again.
type GitRebaseStopSnapshot struct {
	Done    int    `json:"done"`
	StepOid string `json:"step_oid,omitempty"`
	// Next (additive) is the first remaining todo line, part of the stop's
	// identity (a step Git rescheduled leaves Done unchanged).
	Next      string `json:"next,omitempty"`
	Head      string `json:"head"`
	PreHead   string `json:"pre_head,omitempty"`
	Committed bool   `json:"committed,omitempty"`
	Empty     bool   `json:"empty,omitempty"`
	Conflict  bool   `json:"conflict,omitempty"`
	Staged    bool   `json:"staged,omitempty"`
	Late      bool   `json:"late,omitempty"`
}

// GitRebaseFailure is a commit failure of an interactive rebase stop:
// StopKey identifies the stop, Code is signing_failed, hook_rejected or
// helper_failed, Hook names a refusing hook, Detail says more.
type GitRebaseFailure struct {
	StopKey string `json:"stop_key"`
	Code    string `json:"code"`
	Hook    string `json:"hook,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

var rebaseFullHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// commitProducing reports actions after which a squash or fixup may follow.
func commitProducing(action string) bool {
	switch action {
	case GitRebasePick, GitRebaseReword, GitRebaseEdit, GitRebaseSquash, GitRebaseFixup:
		return true
	}
	return false
}

func rebasePlanError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ValidRebaseMessage reports whether a stored message is acceptable: UTF-8
// without NUL, not only whitespace, at most GitRebaseMessageMax bytes.
func ValidRebaseMessage(m string) bool {
	return len(m) <= GitRebaseMessageMax && utf8.ValidString(m) && !strings.ContainsRune(m, 0) && strings.TrimSpace(m) != ""
}

// ValidateRebaseEntryShapes checks each entry on its own (action, hash,
// edit mode, fixup variant, message encoding), without a plan. It is
// what the server checks before reading anything.
func ValidateRebaseEntryShapes(entries []GitRebaseEntry) *Error {
	if len(entries) == 0 {
		return rebasePlanError("invalid_plan", "the plan has no entries")
	}
	if len(entries) > GitRebasePlanCommitsMax+GitRebaseBreaksMax {
		return rebasePlanError("invalid_plan", "the plan has too many entries")
	}
	breaks := 0
	for i, e := range entries {
		n := i + 1
		switch e.Action {
		case GitRebaseBreak:
			breaks++
			if e.Commit != "" {
				return rebasePlanError("invalid_plan", "entry %d: break names no commit", n)
			}
		case GitRebasePick, GitRebaseReword, GitRebaseEdit, GitRebaseSquash, GitRebaseFixup, GitRebaseDrop:
			if !rebaseFullHash.MatchString(e.Commit) {
				return rebasePlanError("invalid_plan", "entry %d: %s needs the commit's full hash", n, e.Action)
			}
		default:
			return rebasePlanError("invalid_plan", "entry %d: unknown action %q", n, e.Action)
		}
		switch {
		case e.EditMode != "" && e.Action != GitRebaseEdit:
			return rebasePlanError("invalid_plan", "entry %d: only edit has an edit mode", n)
		case e.EditMode != "" && e.EditMode != GitRebaseEditReset && e.EditMode != GitRebaseEditAmend:
			return rebasePlanError("invalid_plan", "entry %d: edit mode must be reset or amend", n)
		case e.Fixup != "" && e.Action != GitRebaseFixup:
			return rebasePlanError("invalid_plan", "entry %d: only fixup has a fixup variant", n)
		case e.Fixup != "" && e.Fixup != GitRebaseFixupUseMessage && e.Fixup != GitRebaseFixupEditMessage:
			return rebasePlanError("invalid_plan", "entry %d: fixup variant must be empty, C or c", n)
		case e.Message != "" && !ValidRebaseMessage(e.Message):
			return rebasePlanError("invalid_plan", "entry %d: the message must be UTF-8 text without NUL, up to 64 KiB, and not only whitespace", n)
		}
	}
	if breaks > GitRebaseBreaksMax {
		return rebasePlanError("invalid_plan", "the plan has more than %d breaks", GitRebaseBreaksMax)
	}
	return nil
}

// ValidateRebasePlan checks a plan against the commits it was read with
// (GitRebasePlan): the entry shapes, every non-merge commit exactly once and
// nothing else, each squash or fixup directly after a commit-producing
// entry, and exactly the messages each entry needs. It does not check the
// fingerprint, acknowledgements or the repository; the server does that
// when starting. Manual plans and agent-proposed plans use the same check.
func ValidateRebasePlan(plan GitRebasePlan, entries []GitRebaseEntry) *Error {
	if err := ValidateRebaseEntryShapes(entries); err != nil {
		return err
	}
	pinned := map[string]bool{}
	merges := map[string]bool{}
	for _, c := range plan.Commits {
		if c.Merge {
			merges[c.Oid] = true
		} else {
			pinned[c.Oid] = true
		}
	}
	seen := map[string]int{}
	for i, e := range entries {
		n := i + 1
		if e.Action == GitRebaseBreak {
			continue
		}
		switch {
		case merges[e.Commit]:
			return rebasePlanError("invalid_plan", "entry %d: %s is a merge commit, which is never rebased here (merges are dropped)", n, e.Commit[:12])
		case !pinned[e.Commit]:
			return rebasePlanError("invalid_plan", "entry %d: %s is not one of the plan's commits", n, e.Commit[:12])
		case seen[e.Commit] != 0:
			return rebasePlanError("invalid_plan", "entry %d: %s already appears at entry %d", n, e.Commit[:12], seen[e.Commit])
		}
		seen[e.Commit] = n
	}
	for _, c := range plan.Commits {
		if !c.Merge && seen[c.Oid] == 0 {
			return rebasePlanError("invalid_plan", "commit %s %s is missing from the plan; drop it explicitly to remove it", c.Oid[:12], c.Subject)
		}
	}
	for i, e := range entries {
		n := i + 1
		chainMember := e.Action == GitRebaseSquash || e.Action == GitRebaseFixup
		if chainMember && (i == 0 || !commitProducing(entries[i-1].Action)) {
			return rebasePlanError("invalid_plan", "entry %d: %s needs a pick, reword, edit, squash or fixup directly before it", n, e.Action)
		}
		if e.Action == GitRebaseReword && e.Message == "" {
			return rebasePlanError("message_required", "entry %d: reword needs the new message", n)
		}
		if !chainMember {
			if e.Message != "" && e.Action != GitRebaseReword {
				return rebasePlanError("invalid_plan", "entry %d: %s takes no message", n, e.Action)
			}
			continue
		}
		last := i+1 >= len(entries) || (entries[i+1].Action != GitRebaseSquash && entries[i+1].Action != GitRebaseFixup)
		if !last {
			if e.Message != "" {
				return rebasePlanError("invalid_plan", "entry %d: only the last squash or fixup of a chain carries the combined message", n)
			}
			continue
		}
		// The chain runs from the entry before its first member to i.
		start := i
		for start > 0 && (entries[start].Action == GitRebaseSquash || entries[start].Action == GitRebaseFixup) {
			start--
		}
		edits := false
		for _, m := range entries[start+1 : i+1] {
			edits = edits || m.Action == GitRebaseSquash || m.Fixup == GitRebaseFixupEditMessage
		}
		switch {
		case edits && e.Message == "":
			return rebasePlanError("message_required", "entry %d: the squash ending here needs the combined message", n)
		case !edits && e.Message != "":
			return rebasePlanError("invalid_plan", "entry %d: a chain of fixups without -c keeps a commit's message and takes none", n)
		}
	}
	return nil
}
