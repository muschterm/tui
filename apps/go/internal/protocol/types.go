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
}

// Settings are the execution settings selected for a prompt or reported as
// effective for a running turn.
type Settings struct{ Model, Effort, Permissions, Context, Speed string }

// Thread is one conversation with its chosen agent, queue, requests and
// lifecycle state.
type Thread struct {
	ProjectID                                  string `json:"ProjectID,omitempty"`
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
	TurnID                               string  `json:"TurnID,omitempty"`
	Prompt                               *Prompt `json:"Prompt,omitempty"`
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

// Request is a pending or resolved question or approval raised by an agent.
type Request struct {
	ID, Kind, Mode, State, Title, Detail, Origin string
	Revision                                     int64
	Questions                                    []Question
	Choices                                      []string
	Answers                                      []string
	QuestionAnswers                              []Answer `json:"QuestionAnswers,omitempty"`
	Delivery                                     string
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
