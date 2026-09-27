// Package protocol defines the bounded, fixture-only application protocol v1.
package protocol

import "encoding/json"

// Version is the application protocol version clients and servers must share.
const Version = 1

// Snapshot is the complete server-owned state at one revision.
type Snapshot struct {
	AppSettings  AppSettings `json:"app_settings"`
	Projects     []Project   `json:"projects,omitempty"`
	Capabilities []string    `json:"capabilities"`
	Version      int         `json:"version"`
	Revision     int64       `json:"revision"`
	InstanceID   string      `json:"instance_id"`
	Threads      []Thread    `json:"threads"`
	Terminals    []Terminal  `json:"terminals"`
	// Agents lists every configured agent connection with its last probed
	// state. The fixture agent is always present.
	Agents []Agent `json:"agents,omitempty"`
	// GitOps holds the latest Git write per repository (git_write.go).
	GitOps []GitOp `json:"git_ops,omitempty"`
	// GitOperations holds the latest application-started merge or rebase per
	// repository toplevel (git_write.go, ADR 0023). Additive.
	GitOperations []GitOperationRecord `json:"git_operations,omitempty"`
	// GitBackups lists recorded abort and skip backups (ADR 0023). Additive.
	GitBackups []GitBackupEntry `json:"git_backups,omitempty"`
	// GitConflictCopies lists saved copies of conflicted files (S3).
	GitConflictCopies []GitConflictCopy `json:"git_conflict_copies,omitempty"`
	// Documents lists loaded shared documents (document.go). It is a
	// projection of the documents tables, rebuilt at server start.
	Documents []DocumentStatus `json:"documents,omitempty"`
	// Worktrees lists the Git worktrees the application created for
	// threads (ADR 0024). Additive.
	Worktrees []ManagedWorktree `json:"worktrees,omitempty"`
}

// Managed worktree states (ManagedWorktree.State, ADR 0024).
const (
	// WorktreeCreating: `git worktree add` was journaled and may be running.
	WorktreeCreating = "creating"
	// WorktreePresent: the directory and Git's registration agree.
	WorktreePresent = "present"
	// WorktreeMissing: the directory is gone (Git may still list it).
	WorktreeMissing = "missing"
	// WorktreeMoved: Git registers it at MovedTo, an existing directory
	// that points back (git worktree move or repair).
	WorktreeMoved = "moved"
	// WorktreeUnregistered: the directory exists but Git no longer
	// registers it there.
	WorktreeUnregistered = "unregistered"
	// WorktreeUnattached: present, but no thread uses it (creation could
	// not attach its thread, or the thread was deleted).
	WorktreeUnattached = "unattached"
	// WorktreeRemoved: the application removed it; the branch is kept.
	WorktreeRemoved = "removed"
)

// ManagedWorktree is one worktree the application created for a thread
// (ADR 0024). Path is its real path (under the application home unless it
// was relocated); CommonDir the repository's common Git directory and
// AdminName its entry under CommonDir/worktrees. RelPath is the project
// folder relative to the repository toplevel ("." for the toplevel): the
// thread's checkout is Path joined with RelPath. Branch was created at
// StartOid by the command CommandID. State is one of the Worktree*
// constants; MovedTo is set while State is moved.
type ManagedWorktree struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	CommonDir string `json:"common_dir"`
	AdminName string `json:"admin_name,omitempty"`
	RelPath   string `json:"rel_path"`
	Branch    string `json:"branch"`
	StartOid  string `json:"start_oid"`
	CommandID string `json:"command_id"`
	CreatedAt string `json:"created_at"`
	State     string `json:"state"`
	MovedTo   string `json:"moved_to,omitempty"`
	// Detail explains a failure that left the worktree unattached.
	Detail string `json:"detail,omitempty"`
	// Unverified marks a directory `git worktree add` left behind that
	// failed verification (Git failed, it is not on the new branch at the
	// start commit, or Git's initialization lock remains). No thread is ever
	// attached to it; the user inspects it and removes or forgets it.
	Unverified bool `json:"unverified,omitempty"`
}

// WorkspaceRequest is thread.start's explicit workspace choice (capability
// worktree-create). Mode "checkout" uses the project's checkout whatever
// the workspace default says; Mode "worktree" creates a new branch Branch
// at the commit StartOid (a full object ID) in a new worktree under the
// application home. An existing branch name is refused; there is no
// detached mode.
type WorkspaceRequest struct {
	Mode     string `json:"mode"`
	StartOid string `json:"start_oid,omitempty"`
	Branch   string `json:"branch,omitempty"`
}

// WorktreeAction is the payload of the worktree.* commands. ID names the
// managed worktree (or, for worktree.prune, Command.ProjectID the
// repository); Confirm is the Fingerprint of the removal or prune preview
// the user confirmed.
type WorktreeAction struct {
	ID      string `json:"id,omitempty"`
	Confirm string `json:"confirm,omitempty"`
}

// WorktreeRemoval is GET /v1/worktrees/removal: what worktree.remove would
// do. Ignored counts the ignored entries (files or whole directories) that
// removal deletes with the directory; Blockers lists why it is refused
// now (empty when allowed). The branch is always kept.
type WorktreeRemoval struct {
	ID                string   `json:"id"`
	Path              string   `json:"path"`
	Branch            string   `json:"branch"`
	Head              string   `json:"head,omitempty"`
	Ignored           int      `json:"ignored"`
	IgnoredIncomplete bool     `json:"ignored_incomplete,omitempty"`
	IgnoredSample     []string `json:"ignored_sample,omitempty"`
	Blockers          []string `json:"blockers"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
}

// WorktreePrune is GET /v1/worktrees/prune: the stale worktree
// registrations `git worktree prune` would remove from the project's
// repository (any worktree, not only managed ones).
type WorktreePrune struct {
	ProjectID   string   `json:"project_id"`
	Entries     []string `json:"entries"`
	Fingerprint string   `json:"fingerprint,omitempty"`
}

// Agent is one configured agent connection. Kind is "fixture" or "acp". For
// acp, Command/Args launch a user-owned ACP stdio executable; State reports the
// last probe: unprobed, probing, ready, unauthenticated or unavailable, with
// Detail explaining unavailability or listing authentication methods. Options
// are the config options advertised by a provisional session; Fields records
// which option ID feeds each Settings field (empty means that field is not
// selectable for this agent and the fixed display value applies).
type Agent struct {
	ID, Name, Kind string
	Command        string   `json:"Command,omitempty"`
	Args           []string `json:"Args,omitempty"`
	State, Detail  string
	Version        string `json:"Version,omitempty"`
	ProbedAt       string `json:"ProbedAt,omitempty"`
	Revision       int64
	Options        []ConfigOption `json:"Options,omitempty"`
	Fields         SettingFields  `json:"Fields,omitempty"`
	// Capabilities are agent-reported facts from initialize: "load-session",
	// "image-prompt", "embedded-context", "audio-prompt", "session-close".
	// Absence means the agent did not report it, never an assumption.
	Capabilities []string `json:"Capabilities,omitempty"`
}

// SettingFields maps composer settings fields to the option IDs that supply
// their values. Settings values are option value IDs, never display names.
type SettingFields struct{ Model, Effort, Permissions, Context, Speed string }

// ConfigOption mirrors one ACP session config option. Type is "select" or
// "boolean" (Values then holds "true"/"false"); other types are retained with
// Current only and are not selectable.
type ConfigOption struct {
	ID, Name, Category, Type string
	Description              string `json:"Description,omitempty"`
	Current                  string
	Values                   []ConfigValue `json:"Values,omitempty"`
}

// ConfigValue is one selectable option value.
type ConfigValue struct {
	Value, Name string
	Description string   `json:"Description,omitempty"`
	Models      []string `json:"Models,omitempty"`
}

// Settings are the execution settings selected for a prompt or reported as
// effective for a running turn.
type Settings struct{ Model, Effort, Permissions, Context, Speed string }

// Thread is one conversation with its chosen agent, queue, requests and
// lifecycle state.
type Thread struct {
	ProjectID  string `json:"ProjectID,omitempty"`
	AgentID    string `json:"AgentID,omitempty"`
	SessionID  string `json:"SessionID,omitempty"`
	StopReason string `json:"StopReason,omitempty"`
	Error      string `json:"Error,omitempty"`
	// Options is the live session's current config option catalogue, replaced
	// whole from every set_config_option response or config_option_update; it
	// supersedes the probed Agent.Options while the session exists.
	Options []ConfigOption `json:"Options,omitempty"`
	// Usage is the latest agent-supplied context telemetry; nil when never
	// reported. Amounts are as supplied, not computed.
	Usage                                      *Usage `json:"Usage,omitempty"`
	Closed                                     bool   `json:"Closed,omitempty"`
	LifecycleRevision                          int64  `json:"LifecycleRevision,omitempty"`
	ID, Project, Title, Checkout, Agent, State string
	TurnID                                     string `json:"TurnID,omitempty"`
	NeedsResume                                bool
	RestartEligible                            bool `json:"RestartEligible,omitempty"`
	Selected, Effective                        Settings
	Activity                                   []Activity
	Plan                                       []PlanStep
	Children                                   []Child
	Requests                                   []Request
	Queue                                      []Prompt
	QueueRevision                              int64
	Tick                                       int
	// WriterWait is set only while this thread has eligible queued work that
	// another thread's checkout writer lease blocks. It is derived by the
	// server and recomputed on load; a persisted value never blocks dispatch.
	WriterWait *WriterWait `json:"writer_wait,omitempty"`
	// WorktreeID (additive, ADR 0024) names the managed worktree this
	// thread was created in; Checkout is then inside it.
	WorktreeID string `json:"worktree_id,omitempty"`
	// Job (additive, ADR 0023 S4) marks a job-kind thread: an agent working
	// on one Git operation's conflicts. Job threads belong to the Git
	// operation panel, not the ordinary thread list.
	Job *ThreadJob `json:"job,omitempty"`
}

// Thread job kinds (ThreadJob.Kind). A rebase_plan job (ADR 0027) is an
// agent proposing an interactive rebase plan; it has no Git operation.
const (
	ThreadJobConflictResolution = "conflict_resolution"
	ThreadJobRebasePlan         = "rebase_plan"
)

// ThreadJob describes a job-kind thread. OperationID is the Git operation it
// works on, Checkout that operation's repository toplevel and Paths the
// conflicted paths it may edit. The Base fields are what the server saw
// before the agent started, for the review: HEAD, the stop key, the other
// unmerged paths and the status entries outside the job's paths
// (BaseOutside, "group NUL path NUL pin" lines, at most 2000;
// OutsideIncomplete when there were more, then only BaseOutsideFingerprint
// is compared).
type ThreadJob struct {
	Kind                   string   `json:"kind"`
	OperationID            string   `json:"operation_id"`
	Checkout               string   `json:"checkout"`
	Paths                  []string `json:"paths"`
	BaseHead               string   `json:"base_head,omitempty"`
	BaseStop               string   `json:"base_stop,omitempty"`
	BaseOtherUnmerged      []string `json:"base_other_unmerged,omitempty"`
	BaseOutside            []string `json:"base_outside,omitempty"`
	BaseOutsideFingerprint string   `json:"base_outside_fingerprint,omitempty"`
	OutsideIncomplete      bool     `json:"outside_incomplete,omitempty"`
	StartedAt              string   `json:"started_at,omitempty"`
	// Additive (S4 review). BaseCopy is the before_job copy of every job
	// path (working tree and index) taken after open documents were saved,
	// immediately before the agent started: the review's "before" and what
	// reject restores. Decisions records the user's accept or reject per
	// path since the job's latest turn (DecisionsTurn).
	BaseCopy      string            `json:"base_copy,omitempty"`
	Decisions     map[string]string `json:"decisions,omitempty"`
	DecisionsTurn string            `json:"decisions_turn,omitempty"`
	// Additive (ADR 0027), rebase_plan jobs only: Base and Onto exactly as
	// the job was started with (as for GET /v1/git/rebase/plan) and the
	// latest proposal's summary (the full proposal is
	// GET /v1/git/rebase/proposal).
	Base     string                    `json:"base,omitempty"`
	Onto     string                    `json:"onto,omitempty"`
	Proposal *GitRebaseProposalSummary `json:"proposal,omitempty"`
}

// Activity is one transcript entry: a user prompt, agent reply, tool call or
// child summary.
type Activity struct {
	ID, Role, Title, Text, State, Detail string
	RequestID                            string  `json:"RequestID,omitempty"`
	TurnID                               string  `json:"TurnID,omitempty"`
	Prompt                               *Prompt `json:"Prompt,omitempty"`
	// Tool is the structured inspector payload of a tool or MCP call. It is
	// optional: older payloads and non-tool activity carry only Detail, which
	// stays populated for older clients.
	Tool *ToolDetail `json:"Tool,omitempty"`
}

// ToolDetail holds agent-reported tool call facts as explicit fields. Every
// string is sanitized and independently bounded by the server; RawInput,
// RawOutput and Content are display summaries, not re-parseable payloads.
type ToolDetail struct {
	Kind      string   `json:"kind,omitempty"`
	Status    string   `json:"status,omitempty"`
	Locations []string `json:"locations,omitempty"`
	Content   []string `json:"content,omitempty"`
	RawInput  string   `json:"rawInput,omitempty"`
	RawOutput string   `json:"rawOutput,omitempty"`
}

// Usage is agent-reported context occupancy: Used tokens of Size capacity,
// with Source naming the reporting notification kind and ReportedAt RFC3339.
type Usage struct {
	Used, Size         int64
	Source, ReportedAt string
	// A negative Used or zero Size marks that part of context as unavailable.
	Model, Scope, SessionID string     `json:",omitempty"`
	Cost                    *UsageCost `json:"Cost,omitempty"`
}

// UsageCost is a cumulative snapshot, never an increment. Decimal text retains
// the provider's precision; an estimate is not a verified account charge.
type UsageCost struct {
	Amount, Currency, Source, ReportedAt, Scope string
	Estimated                                   bool
}

// PlanStep is one entry of an agent-reported plan.
type PlanStep struct{ Title, State string }

// Child is a subagent with its own activity history.
type Child struct {
	ID, ParentID, Name, State string
	Activity                  []Activity
}

// Question is one question inside a request; empty Options means free text.
// OptionDescriptions, when present, parallels Options: entry i is supplied
// detail for Options[i] and may be empty. It is display text only; answers
// always carry the exact option value.
type Question struct {
	ID, Text           string
	Label              string `json:"Label,omitempty"`
	Kind               string `json:"Kind,omitempty"`
	AllowOther         bool   `json:"AllowOther,omitempty"`
	Required           *bool  `json:"Required,omitempty"`
	Options            []string
	OptionDescriptions []string `json:"OptionDescriptions,omitempty"`
}

// Answer is a draft or accepted answer to one Question.
type Answer struct {
	Choices []string
	Text    string
}

// Request retains a question or approval and its local delivery lifecycle.
// Closed ACP callbacks are not evidence of upstream resolution.
type Request struct {
	ID, Kind, Mode, State, Title, Detail, Origin string
	Revision                                     int64
	Questions                                    []Question
	Choices                                      []string
	ChoiceIDs                                    []string `json:"ChoiceIDs,omitempty"`
	Answers                                      []string
	QuestionAnswers                              []Answer `json:"QuestionAnswers,omitempty"`
	Delivery                                     string
	TurnID                                       string `json:"TurnID,omitempty"`
	DeliveryRoute                                string `json:"DeliveryRoute,omitempty"`
	SubmissionID                                 string `json:"SubmissionID,omitempty"`
	SubmittedRevision                            int64  `json:"SubmittedRevision,omitempty"`
	ApprovalChoiceID                             string `json:"ApprovalChoiceID,omitempty"`
	// Actions lists the non-answer responses the upstream contract supports
	// for this question request ("decline", "cancel"); empty offers none.
	// Action records the one a client chose instead of answering; it is
	// empty for answers and for the server's own withdrawal of a request.
	Actions []string `json:"Actions,omitempty"`
	Action  string   `json:"Action,omitempty"`
	// SourcePayload retains bounded source provenance, never a replayable route.
	SourcePayload json.RawMessage `json:"SourcePayload,omitempty"`
}

// Attachment kind workspace-file is a Send-time capture request: Source is a
// checkout-relative file path and Content must be empty. The server replaces it
// with a captured file attachment only in accepted state. Other kinds already
// carry snapshots (including fixture attachments) and are retained unchanged.
//
// An attachment with ArtifactID references bytes uploaded to the server's
// artifact store. At acceptance the server replaces Kind ("image" or "file"),
// Name, MediaType, Size, SHA256 and image dimensions with its stored record,
// and fills Content only for UTF-8 text up to 64 KiB; artifact bytes are never
// embedded in snapshots otherwise. PreviewSHA256 is the digest of a draft
// preview the client showed for a workspace-file; the server sets
// ChangedSincePreview when the captured content differs from it. Every added
// field is optional so older snapshots and command identities are unchanged.
type Attachment struct {
	Kind, Name, Source, Content string
	ArtifactID                  string `json:",omitempty"`
	MediaType                   string `json:",omitempty"`
	Size                        int64  `json:",omitempty"`
	SHA256                      string `json:",omitempty"`
	Width                       int    `json:",omitempty"`
	Height                      int    `json:",omitempty"`
	PreviewSHA256               string `json:",omitempty"`
	ChangedSincePreview         bool   `json:",omitempty"`
}

// ArtifactInfo is server-owned metadata for an uploaded artifact. State is
// "staged" until a Send accepts it, then "accepted". MediaType is sniffed by
// the server; Width and Height are set for decoded images. Unavailable marks a
// record whose bytes are missing; its content is never presented as empty.
type ArtifactInfo struct {
	ID, Name, MediaType, SHA256, State string
	Size                               int64
	Width                              int  `json:",omitempty"`
	Height                             int  `json:",omitempty"`
	Unavailable                        bool `json:",omitempty"`
}

// FilePreview is a read-only draft preview of a workspace file under the
// same rules Send capture applies.
type FilePreview struct {
	Source, Name, Content, SHA256 string
	Size                          int64
}

// Prompt is a captured submission with its settings and attachments.
type Prompt struct {
	ID, Text    string
	Revision    int64
	Settings    Settings
	Attachments []Attachment
}

// Terminal is a server-owned embedded terminal session belonging to a thread.
// State is one of the Terminal* lifecycle constants in terminal.go. Controller
// is the ClientID allowed to send input and resize, valid only together with
// ControlGen; see terminal.go for the stream protocol. Output is retained only
// for legacy fixture records and is empty for real sessions, whose screen is
// delivered by the terminal stream, never by snapshots.
type Terminal struct {
	ID, ThreadID, State, Controller, Output string
	Revision                                int64
	// ControlGen increments on every control transfer; input and resize must
	// carry the current value.
	ControlGen int64 `json:"ControlGen,omitempty"`
	// Dir is the working directory the shell started in and Shell its
	// executable; Title is the sanitized OSC title. Cols, Rows and Title are
	// coalesced from the live session and may lag the stream briefly.
	Dir   string `json:"Dir,omitempty"`
	Shell string `json:"Shell,omitempty"`
	Title string `json:"Title,omitempty"`
	Cols  int    `json:"Cols,omitempty"`
	Rows  int    `json:"Rows,omitempty"`
	// Exit is set once the process is confirmed reaped; EndReason says why
	// an ended terminal ended (TerminalEnd* constants).
	Exit      *TerminalExit `json:"Exit,omitempty"`
	EndReason string        `json:"EndReason,omitempty"`
	// Error explains close_uncertain.
	Error string `json:"Error,omitempty"`
}

// Command is a client request to change server state. ID identifies it across
// retries; Kind selects the operation and which fields it reads.
type Command struct {
	ApprovalChoiceID                             string           `json:"ApprovalChoiceID,omitempty"`
	Agent                                        string           `json:"Agent,omitempty"`
	AppSettings                                  *AppSettings     `json:"AppSettings,omitempty"`
	ProjectSettings                              *ProjectSettings `json:"ProjectSettings,omitempty"`
	ExpectedTurnID                               string           `json:"ExpectedTurnID,omitempty"`
	ProjectID                                    string           `json:"ProjectID,omitempty"`
	Path                                         string           `json:"Path,omitempty"`
	Version                                      int
	ID, Kind, ThreadID, TargetID, ClientID, Text string
	Revision                                     int64
	Settings                                     *Settings
	Attachments                                  []Attachment
	Order, Answers                               []string
	QuestionAnswers                              []Answer `json:"QuestionAnswers,omitempty"`
	// RequestAction declines or cancels a question request (request.answer)
	// instead of answering it; answers must then be empty.
	RequestAction string `json:"RequestAction,omitempty"`
	// TerminalSize is terminal.open's optional initial PTY size; omitted uses
	// 80×24. The controller resizes through the terminal stream afterwards.
	TerminalSize *TerminalSize `json:"TerminalSize,omitempty"`
	// Git is the payload of the git.* write commands (git_write.go).
	Git *GitWrite `json:"Git,omitempty"`
	// Workspace is thread.start's explicit workspace choice (ADR 0024).
	Workspace *WorkspaceRequest `json:"Workspace,omitempty"`
	// Worktree is the payload of the worktree.* commands (ADR 0024).
	Worktree *WorktreeAction `json:"Worktree,omitempty"`
	// DocumentDisk is document.resolve's reviewed disk version
	// (DocumentVersions.DiskID); see document.go.
	DocumentDisk string `json:"DocumentDisk,omitempty"`
}

// Receipt records the outcome of an accepted or rejected Command.
type Receipt struct {
	ID, State string
	Revision  int64
	TargetID  string
	// Git is the outcome of a git.* command; State then mirrors Git.State.
	Git *GitResult `json:"Git,omitempty"`
	// Error (additive) explains State "failed" of a two-phase command that
	// is not a Git write (a worktree thread start, ADR 0024).
	Error *Error `json:"Error,omitempty"`
}

// Error is a structured protocol failure with a stable Code.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Discovery is the record a running server writes into its application home
// so clients can find and authenticate to it.
type Discovery struct {
	Version                      int
	URL, Token, InstanceID, Home string
	PID                          int
}

// View is a client-owned document stored through the server with optimistic
// revision checks.
type View struct {
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

// ShutdownOutcome records verified shutdown of one server incarnation.
type ShutdownOutcome struct {
	InstanceID string
	Success    bool
	Error      string
}

// WriterWait reports a thread waiting for its checkout's writer lease.
// Position 1 is next in line. While a Git write holds the lease,
// HolderThreadID is empty and HolderGitCommandID names that command. While a
// merge, rebase, cherry-pick or revert is in progress in the checkout's
// repository (started here or outside the application; ADR 0023),
// HolderOperation names its kind and HolderOperationID the application record,
// when there is one.
type WriterWait struct {
	HolderThreadID     string `json:"holder_thread_id"`
	Position           int    `json:"position"`
	HolderGitCommandID string `json:"holder_git_command_id,omitempty"`
	HolderOperation    string `json:"holder_operation,omitempty"`
	HolderOperationID  string `json:"holder_operation_id,omitempty"`
}
