package protocol

// Project names a server-side checkout root. Adding one records an existing
// directory; it does not initialize Git, create a worktree or launch work.
type Project struct {
	ID, Name, Path                string
	Revision                      int64
	Icon, Color, WorkspaceDefault string
}

// AppSettings belongs to the connected server environment.
type AppSettings struct {
	Revision         int64
	WorkspaceDefault string
	// Empty/omitted on update preserves the current directory; "~" resets it.
	ProjectDirectory     string
	ContinueAfterRestart bool
	// Nil on an update preserves the current default for older clients. An
	// empty value explicitly restores the built-in new-thread behavior.
	NewThreadDefaults *NewThreadDefaults `json:"NewThreadDefaults,omitempty"`
}

// NewThreadDefaults applies only when a client first creates a local draft.
// Values are agent catalogue IDs, never inferred from display names.
type NewThreadDefaults struct {
	AgentID  string
	Settings Settings
}

// ProjectSettings are the per-project fields a project.update command may change.
type ProjectSettings struct{ Name, Icon, Color, WorkspaceDefault string }

// EffectiveWorkspaceDefault resolves a project's workspace default through the
// app default to the built-in "checkout".
func EffectiveWorkspaceDefault(app AppSettings, project Project) string {
	if project.WorkspaceDefault != "" {
		return project.WorkspaceDefault
	}
	if app.WorkspaceDefault != "" {
		return app.WorkspaceDefault
	}
	return "checkout"
}

// ProjectRemoveBlocked checks current authoritative work, without cancelling it.
func ProjectRemoveBlocked(s Snapshot, projectID string) string {
	for _, t := range s.Threads {
		if t.ProjectID == projectID {
			if reason := ThreadCloseBlocked(t); reason != "" {
				return reason
			}
		}
	}
	return ""
}
