package protocol

// Read-only Files surface reads (GET /v1/files/*). Every path is relative to
// the selected project or thread checkout, slash-separated and clean; no
// component may be "..", "." or .git (case-insensitive). Symlinks are listed
// and reported, never followed.

// File entry and read kinds.
const (
	FileKindFile    = "file"
	FileKindDir     = "dir"
	FileKindSymlink = "symlink"
	FileKindOther   = "other"
	// FileKindAbsent is a stat of a path that no longer exists.
	FileKindAbsent = "absent"

	FileReadText       = "text"
	FileReadBinary     = "binary"
	FileReadTooLarge   = "too_large"
	FileReadNotRegular = "not_regular"
)

// File read limits: text is returned up to FileTextLimit bytes; files up to
// FileHashLimit are hashed whole and shown truncated; larger files return
// metadata only.
const (
	FileTextLimit = 1 << 20
	FileHashLimit = 16 << 20
	FileListPage  = 500
)

// FileEntry is one listed directory entry. Token changes whenever the entry's
// type, size, times, inode or mode change.
type FileEntry struct {
	Name, Kind string
	Size       int64
	Token      string
}

// FileList is one page of a directory listing: directories first, then the
// rest, each in byte order of Name. Next is an opaque cursor for the page
// after this one, empty on the last page. Truncated reports that the
// directory had more entries than the server scans; Skipped counts names that
// are not valid UTF-8 and cannot be addressed.
type FileList struct {
	Dir       string
	Entries   []FileEntry
	Next      string `json:",omitempty"`
	Truncated bool   `json:",omitempty"`
	Skipped   int    `json:",omitempty"`
}

// FileRead is one file's bounded, read-only content. Kind is text, binary,
// too_large or not_regular. Text is present only for text (up to
// FileTextLimit bytes, Truncated when the file is longer); a leading UTF-8
// BOM is removed from Text and reported. Encoding "invalid" means the text
// contained invalid UTF-8, replaced with U+FFFD. Sha256 covers the whole file
// when it is at most FileHashLimit bytes. A symlink is not_regular with its
// LinkTarget; its target is never read.
type FileRead struct {
	Path, Kind        string
	Size              int64
	Token, Sha256     string `json:",omitempty"`
	Encoding, Newline string `json:",omitempty"`
	BOM               bool   `json:",omitempty"`
	Text              string `json:",omitempty"`
	Truncated         bool   `json:",omitempty"`
	LinkTarget        string `json:",omitempty"`
}

// FileStat is a cheap change poll: Kind is a FileEntry kind or absent.
type FileStat struct {
	Path, Kind string
	Size       int64
	Token      string `json:",omitempty"`
}
