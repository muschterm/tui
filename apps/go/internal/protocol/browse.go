package protocol

// BrowseRequest selects directories for project registration or context files
// within exactly one authoritative project/thread checkout. Queries complete one
// path segment; directory paths end in a slash and can be queried again.
type BrowseRequest struct {
	Scope, ProjectID, ThreadID, Query string
}

// BrowseResult lists one directory's entries for a BrowseRequest.
type BrowseResult struct {
	Root, Directory string
	Entries         []PathEntry
	Truncated       bool
}

// PathEntry is one browsable file or directory.
type PathEntry struct {
	Name, Path string
	IsDir      bool
}
