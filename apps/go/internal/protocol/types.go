// Package protocol defines the bounded, fixture-only application protocol v1.
package protocol

import "encoding/json"

const Version = 1

type Snapshot struct {
	Projects     []Project  `json:"projects,omitempty"`
	Capabilities []string   `json:"capabilities"`
	Version      int        `json:"version"`
	Revision     int64      `json:"revision"`
	InstanceID   string     `json:"instance_id"`
	Threads      []Thread   `json:"threads"`
	Terminals    []Terminal `json:"terminals"`
}
type Settings struct{ Model, Effort, Permissions, Context, Speed string }
type Thread struct {
	ProjectID                                  string `json:"ProjectID,omitempty"`
	Closed                                     bool   `json:"Closed,omitempty"`
	LifecycleRevision                          int64  `json:"LifecycleRevision,omitempty"`
	ID, Project, Title, Checkout, Agent, State string
	NeedsResume                                bool
	Selected, Effective                        Settings
	Activity                                   []Activity
	Plan                                       []PlanStep
	Children                                   []Child
	Requests                                   []Request
	Queue                                      []Prompt
	QueueRevision                              int64
	Tick                                       int
}
type Activity struct{ ID, Role, Title, Text, State, Detail string }
type PlanStep struct{ Title, State string }
type Child struct {
	ID, ParentID, Name, State string
	Activity                  []Activity
}
type Question struct {
	ID, Text   string
	Label      string `json:"Label,omitempty"`
	Kind       string `json:"Kind,omitempty"`
	AllowOther bool   `json:"AllowOther,omitempty"`
	Required   *bool  `json:"Required,omitempty"`
	Options    []string
}
type Answer struct {
	Choices []string
	Text    string
}

type Request struct {
	ID, Kind, Mode, State, Title, Detail, Origin string
	Revision                                     int64
	Questions                                    []Question
	Choices                                      []string
	Answers                                      []string
	QuestionAnswers                              []Answer `json:"QuestionAnswers,omitempty"`
	Delivery                                     string
}
type Attachment struct{ Kind, Name, Source, Content string }
type Prompt struct {
	ID, Text    string
	Revision    int64
	Settings    Settings
	Attachments []Attachment
}
type Terminal struct {
	ID, ThreadID, State, Controller, Output string
	Revision                                int64
}
type Command struct {
	ProjectID                                    string `json:"ProjectID,omitempty"`
	Path                                         string `json:"Path,omitempty"`
	Version                                      int
	ID, Kind, ThreadID, TargetID, ClientID, Text string
	Revision                                     int64
	Settings                                     *Settings
	Attachments                                  []Attachment
	Order, Answers                               []string
	QuestionAnswers                              []Answer `json:"QuestionAnswers,omitempty"`
}
type Receipt struct {
	ID, State string
	Revision  int64
	TargetID  string
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Discovery struct {
	Version                      int
	URL, Token, InstanceID, Home string
	PID                          int
}
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
