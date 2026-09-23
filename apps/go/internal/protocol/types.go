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
}

// Activity is one transcript entry: a user prompt, agent reply, tool call or
// child summary.
type Activity struct {
	ID, Role, Title, Text, State, Detail string
	RequestID                            string  `json:"RequestID,omitempty"`
	TurnID                               string  `json:"TurnID,omitempty"`
	Prompt                               *Prompt `json:"Prompt,omitempty"`
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
type Question struct {
	ID, Text   string
	Label      string `json:"Label,omitempty"`
	Kind       string `json:"Kind,omitempty"`
	AllowOther bool   `json:"AllowOther,omitempty"`
	Required   *bool  `json:"Required,omitempty"`
	Options    []string
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
	// SourcePayload retains bounded source provenance, never a replayable route.
	SourcePayload json.RawMessage `json:"SourcePayload,omitempty"`
}

// Attachment kind workspace-file is a Send-time capture request: Source is a
// checkout-relative file path and Content must be empty. The server replaces it
// with a captured file attachment only in accepted state. Other kinds already
// carry snapshots (including fixture attachments) and are retained unchanged.
type Attachment struct{ Kind, Name, Source, Content string }

// Prompt is a captured submission with its settings and attachments.
type Prompt struct {
	ID, Text    string
	Revision    int64
	Settings    Settings
	Attachments []Attachment
}

// Terminal is a fixture terminal session owned by a thread.
type Terminal struct {
	ID, ThreadID, State, Controller, Output string
	Revision                                int64
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
}

// Receipt records the outcome of an accepted or rejected Command.
type Receipt struct {
	ID, State string
	Revision  int64
	TargetID  string
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
