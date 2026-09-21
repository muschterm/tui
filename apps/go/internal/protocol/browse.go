package protocol

// BrowseRequest selects directories for project registration or context files
// within exactly one authoritative project/thread checkout. Queries complete one
// path segment; directory paths end in a slash and can be queried again.
type BrowseRequest struct {
	Scope, ProjectID, ThreadID, Query string
}
type BrowseResult struct {
	Root, Directory string
	Entries         []PathEntry
	Truncated       bool
}
type PathEntry struct {
	Name, Path string
	IsDir      bool
}
