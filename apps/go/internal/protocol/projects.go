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
}
type ProjectSettings struct{ Name, Icon, Color, WorkspaceDefault string }

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
