package protocol

// Agent-planned interactive rebase (ADR 0027). An agent proposes a plan for
// an interactive rebase (ADR 0026); the user reviews it in the plan editor
// and starts it with an ordinary git.rebase. The agent never runs anything.
//
// A planning job is an agent turn in a job-kind thread (Thread.Job with Kind
// rebase_plan). Unlike a resolution job it is read-only work: it does not
// take the checkout writer lease, so it neither waits for nor blocks other
// threads or Git writes in the checkout. Instead the server pins the
// checkout (branch, HEAD, the whole index and the status of every tracked
// and untracked path) before each turn and compares it after the turn; any
// difference taints the proposal, whoever made it (Concurrent reports that
// application Git writes or other agent turns ran there meanwhile).
//
//   - git.rebase_plan_start (a journaled Git write that takes the
//     repository's Git slot but not the lease): AgentID and Settings (as for
//     a resolution job; an ACP agent is required), Base and Onto exactly as
//     for GET /v1/git/rebase/plan, optional Fingerprint (a plan read the user
//     saw; stale_plan when the branch changed since) and optional
//     Instructions (the user's request, at most 4 KiB). The server reads and
//     pins the plan, refuses one it could not plan (codes of
//     GitRebasePlan.Blocked: unborn, detached, empty_range,
//     too_many_commits, unsupported_message, not_supported; also
//     too_many_commits beyond GitRebasePlanJobCommitsMax), sends the agent the
//     commits (subjects, bodies, authors, per-commit numstat and bounded
//     diffs, see ADR 0027) and the answer format, and creates the job thread
//     with the proposal in state running. Receipt.TargetID is the
//     repository; GitResult.Message names the job thread
//     (rebase-plan-<command ID>).
//   - git.rebase_plan_revise (a journaled Git write, no lease): JobID,
//     ExpectedRevision (the proposal revision the user saw; stale_proposal
//     otherwise) and optional Instructions. Another turn in the same job
//     (also after a failure, cancel or restart), carrying the validation
//     errors or the checkout changes of the latest proposal when it has any
//     ("ask the agent to fix"). Refused while running (job_running), when
//     the branch changed since the plan was pinned (stale_plan: start a new
//     job) and after GitRebasePlanJobTurnsMax turns (too_many_turns). The
//     server never sends a revision by itself.
//   - git.rebase_plan_cancel (an ordinary command): JobID; interrupts the
//     running turn (not_active otherwise). The proposal becomes cancelled.
//   - git.rebase_plan_end (an ordinary command): JobID; deletes the job
//     thread and its proposal (job_running while a turn runs).
//
// The job thread refuses ordinary thread commands (job_thread). Approvals
// the agent requests during a planning turn are declined by the server
// (the reject option the agent offered), recorded in the transcript and
// counted (DeclinedApprovals). On a verified built-in bridge the job's agent
// is opened in the bridge's read-only mode (ReadOnlyMode; Permissions
// "plan" for Claude, "read-only" for Codex; a selected permission is
// replaced): Claude's plan mode does not edit files and asks before every
// command outside its read-only set (ApprovalGated: the server declines
// those), Codex's readOnly sandbox with approval policy never cannot write
// and escalates nothing. Other ACP agents are unverified: only the declines
// and the after-turn comparison apply there. GitRebaseProposal.Enforcement
// states which applied.
//
// The agent answers with exactly one fenced block whose info string is
// tui-rebase-plan, containing JSON (GitRebaseProposalWire). The server
// parses it (unknown fields, a missing or second block, exec and other
// actions are errors; a unique commit prefix of at least 7 hex digits is
// expanded to the full hash), then validates it with ValidateRebasePlan
// against the pinned plan.
//
// Read GET /v1/git/rebase/proposal?job=<job thread>&<target> for the full
// proposal and GET /v1/git/rebase/proposals?<target> for the checkout's
// planning jobs. Loading a proposal into the plan editor and starting it is
// the client's business: git.rebase with GitIntegrate.Interactive built from
// a fresh plan read, the proposal's Entries and UpdateRefs, and the user's
// own acknowledgements (merges, published commits); the agent cannot give
// them. Capability git-rebase-agent-plan.
//
// Codes: job_running, not_active, no_job, stale_proposal, stale_plan,
// too_many_turns, job_thread, plus those of the plan read.

// Planning job command kinds.
const (
	GitKindRebasePlanStart  = "git.rebase_plan_start"
	GitKindRebasePlanRevise = "git.rebase_plan_revise"
	GitKindRebasePlanCancel = "git.rebase_plan_cancel"
	GitKindRebasePlanEnd    = "git.rebase_plan_end"
)

// Limits of a planning job.
const (
	GitRebasePlanJobCommitsMax = 300
	GitRebasePlanJobTurnsMax   = 8
	GitRebaseRationaleMax      = 16 << 10
)

// GitRebaseProposalFence is the info string of the answer block.
const GitRebaseProposalFence = "tui-rebase-plan"

// Proposal states (GitRebaseProposalSummary.State, GitRebaseProposal.State).
//
//	running    the agent is working on this revision
//	proposed   a valid plan against the pinned plan, checkout unchanged
//	invalid    the answer could not be parsed or failed validation
//	           (Errors); Raw keeps the agent's text
//	tainted    the checkout changed during the turn (Changes, Reason);
//	           the answer is kept for inspection, never offered to load
//	failed     the turn failed, never started or the server restarted
//	           while it ran (Reason)
//	cancelled  the user stopped the turn
//	stale      reads only: proposed, but the branch no longer matches the
//	           pinned plan (CurrentFingerprint differs); start a new job
const (
	GitProposalRunning   = "running"
	GitProposalProposed  = "proposed"
	GitProposalInvalid   = "invalid"
	GitProposalTainted   = "tainted"
	GitProposalFailed    = "failed"
	GitProposalCancelled = "cancelled"
	GitProposalStale     = "stale"
)

// GitRebasePlanJob is GitWrite.RebasePlan.
type GitRebasePlanJob struct {
	JobID            string    `json:"job_id,omitempty"`
	AgentID          string    `json:"agent_id,omitempty"`
	Settings         *Settings `json:"settings,omitempty"`
	Base             string    `json:"base,omitempty"`
	Onto             string    `json:"onto,omitempty"`
	Fingerprint      string    `json:"fingerprint,omitempty"`
	Instructions     string    `json:"instructions,omitempty"`
	ExpectedRevision int64     `json:"expected_revision,omitempty"`
}

// GitRebaseProposalSummary is ThreadJob.Proposal: the latest revision of a
// planning job's proposal, in the snapshot every client receives. Revision
// increases with every state change. PromptID is the job turn the revision
// belongs to. Fingerprint, Branch, HeadOid, PlanBase (Base resolved: the
// upstream's full ref for base=upstream), Commits, MergeCount and Published
// describe the pinned plan. PlanID, CheckoutID and ResultID are opaque
// server storage identities.
type GitRebaseProposalSummary struct {
	Revision          int64    `json:"revision"`
	State             string   `json:"state"`
	PromptID          string   `json:"prompt_id"`
	Turns             int      `json:"turns"`
	Branch            string   `json:"branch,omitempty"`
	HeadOid           string   `json:"head_oid,omitempty"`
	Fingerprint       string   `json:"fingerprint"`
	PlanBase          string   `json:"plan_base,omitempty"`
	Commits           int      `json:"commits"`
	MergeCount        int      `json:"merge_count,omitempty"`
	Published         bool     `json:"published,omitempty"`
	Entries           int      `json:"entries,omitempty"`
	Errors            []string `json:"errors,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	Concurrent        bool     `json:"concurrent,omitempty"`
	Permissions       string   `json:"permissions,omitempty"`
	ApprovalGated     bool     `json:"approval_gated,omitempty"`
	ReadOnlyMode      bool     `json:"read_only_mode,omitempty"`
	DeclinedApprovals int      `json:"declined_approvals,omitempty"`
	PlanID            string   `json:"plan_id,omitempty"`
	CheckoutID        string   `json:"checkout_id,omitempty"`
	ResultID          string   `json:"result_id,omitempty"`
	UpdatedAt         string   `json:"updated_at,omitempty"`
}

// GitRebaseProposal is GET /v1/git/rebase/proposal: the summary's fields
// plus what is kept outside the snapshot. Plan is the pinned plan the agent
// was given. Entries and UpdateRefs are the parsed answer (validated when
// State is proposed or stale; for invalid they are what could be parsed,
// for tainted what the agent answered). Raw is the agent's final message of
// the turn (RawTruncated when it reached the transcript limit). Changes
// lists what differed in the checkout after the turn. StopReason is how the
// turn ended. CurrentFingerprint, Blocked and BlockedMessage come from a
// plan read made for this response (empty with CurrentError when it
// failed). Enforcement describes, honestly, what kept the agent from
// changing the checkout.
type GitRebaseProposal struct {
	JobThreadID        string                   `json:"job_thread_id"`
	Checkout           string                   `json:"checkout"`
	Base               string                   `json:"base"`
	Onto               string                   `json:"onto,omitempty"`
	Summary            GitRebaseProposalSummary `json:"summary"`
	State              string                   `json:"state"`
	Plan               *GitRebasePlan           `json:"plan,omitempty"`
	Entries            []GitRebaseEntry         `json:"entries,omitempty"`
	UpdateRefs         bool                     `json:"update_refs,omitempty"`
	Rationale          string                   `json:"rationale,omitempty"`
	Raw                string                   `json:"raw,omitempty"`
	RawTruncated       bool                     `json:"raw_truncated,omitempty"`
	Changes            []string                 `json:"changes,omitempty"`
	StopReason         string                   `json:"stop_reason,omitempty"`
	Settings           Settings                 `json:"settings"`
	Enforcement        string                   `json:"enforcement,omitempty"`
	CurrentFingerprint string                   `json:"current_fingerprint,omitempty"`
	CurrentError       string                   `json:"current_error,omitempty"`
	Blocked            string                   `json:"blocked,omitempty"`
	BlockedMessage     string                   `json:"blocked_message,omitempty"`
}

// GitRebaseProposalInfo is one entry of GET /v1/git/rebase/proposals: a
// planning job of the target's repository with its stored summary (State
// is never stale here; read the proposal for that).
type GitRebaseProposalInfo struct {
	JobThreadID string                   `json:"job_thread_id"`
	Checkout    string                   `json:"checkout"`
	Base        string                   `json:"base"`
	Onto        string                   `json:"onto,omitempty"`
	Agent       string                   `json:"agent,omitempty"`
	State       string                   `json:"state"`
	StartedAt   string                   `json:"started_at,omitempty"`
	Summary     GitRebaseProposalSummary `json:"summary"`
}

// GitRebaseProposalWire is the JSON inside the tui-rebase-plan block.
// Version is 1. Entries use GitRebaseEntry's JSON names. UpdateRefs asks to
// move the plan's UpdateRefs branches. Rationale (at most 16 KiB) explains
// the plan.
type GitRebaseProposalWire struct {
	Version    int              `json:"version"`
	Entries    []GitRebaseEntry `json:"entries"`
	UpdateRefs bool             `json:"update_refs,omitempty"`
	Rationale  string           `json:"rationale,omitempty"`
}
