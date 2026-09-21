package protocol

// WorkspaceInfo is a read-only observation of an authoritative checkout path.
// State is branch, detached, unborn, non-git, or unavailable. Kind is checkout,
// worktree, fixture, or unavailable. Observations may change after this response.
type WorkspaceInfo struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision,omitempty"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}
