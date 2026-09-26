package protocol

// Partial staging (ADR 0025): hunk- and line-level git.stage and git.unstage.
//
// Capability "git-partial-stage". A client first reads the addressable diff
// of one path with GET /v1/git/hunks?{project_id|thread_id}&path=&group=,
// where group is GitGroupUnstaged (worktree against the index: what can be
// staged) or GitGroupStaged (the index against HEAD: what can be unstaged).
// The path must currently appear in that status group, as for
// GET /v1/git/diff. It then sends git.stage (group unstaged) or git.unstage
// (group staged) with GitWrite.Partial set and no Paths; the command is
// durable, journaled, deduplicated by command ID, holds the checkout writer
// lease and is refused while a thread holds the checkout (checkout_busy),
// exactly like the whole-file writes in git_write.go.
//
// The server never runs `git apply`. It reads the pinned pre-image blob of
// the shown diff and applies changes to its bytes: for stage, the index blob
// with the selected changes applied; for unstage, the HEAD blob with every
// change that was NOT selected applied (which reverts exactly the selected
// changes). It writes the result with `git hash-object -w --no-filters
// --stdin` and installs it with `git update-index --cacheinfo`, keeping the
// index entry's mode. Stage reads the worktree file (Git's diff of it and
// its lstat token pin what was shown), but the staged content comes only
// from the shown diff; the worktree is never written. Only the
// post-index-change hook runs (as for `git add`).
//
// The diff is generated with fixed options that neutralize the user's diff
// configuration (context 3, inter-hunk context 0, Myers algorithm with the
// indent heuristic, no renames, no textconv or external diff, no color,
// fixed a/ and b/ prefixes, full object IDs, blank context lines kept), so
// hunk boundaries do not depend on diff.* settings. A staged rename is
// diffed from the HEAD blob of OrigPath to the index blob of Path, so its
// hunks can be unstaged while the rename stays staged.

// GitHunks is the reply of GET /v1/git/hunks.
//
// Fingerprint is set only when the path supports partial staging; send it
// back unchanged in GitPartial.Fingerprint. It pins the status entry, the
// index entry (mode, object ID, stage, skip-worktree/assume-unchanged tag,
// intent-to-add), the HEAD entry (unstage), the worktree lstat token and
// object ID as Git's diff computes it (stage), the path's diff, filter and
// working-tree-encoding attributes, and a SHA-256 of the exact diff bytes
// the hunks were parsed from. Any
// change refuses the write with stale_diff; fetch the hunks again.
//
// Unsupported, when set, is a GitPartialUnsupported* code and Message
// explains it; whole-file stage/unstage remains available. Hunks may still
// be present for display (for example a filter-attribute pointer diff).
//
// Line indices (GitHunkLine.Index) are unique across the whole response,
// ascending in display order from 0 to LineCount-1, and hunk indices from 0.
// They are only meaningful together with this Fingerprint.
type GitHunks struct {
	Path        string    `json:"path"`
	OrigPath    string    `json:"orig_path,omitempty"`
	Group       string    `json:"group"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Unsupported string    `json:"unsupported,omitempty"`
	Message     string    `json:"message,omitempty"`
	Hunks       []GitHunk `json:"hunks"`
	LineCount   int       `json:"line_count"`
	// Mode is the index entry mode that the result keeps. ModeChanged
	// reports that the other side has a different mode, which partial
	// staging leaves as it is (stage or unstage the whole file for it).
	Mode        string `json:"mode,omitempty"`
	ModeChanged bool   `json:"mode_changed,omitempty"`
}

// GitHunk is one unified-diff hunk. Header is the full "@@ -a,b +c,d @@ ..."
// line (the text after the second "@@" is Git's function context). Old* are
// the pre-image (index for unstaged, HEAD for staged) and New* the
// post-image (worktree for unstaged, index for staged) ranges, 1-based as in
// the header.
type GitHunk struct {
	Index    int           `json:"index"`
	Header   string        `json:"header"`
	OldStart int           `json:"old_start"`
	OldLines int           `json:"old_lines"`
	NewStart int           `json:"new_start"`
	NewLines int           `json:"new_lines"`
	Lines    []GitHunkLine `json:"lines"`
}

// GitHunkLine is one line of a hunk. Kind is a GitHunkLine* constant. Text is
// the line's bytes without the diff prefix and without its "\n" (a "\r" of a
// CRLF line is kept); bytes that are not valid UTF-8 are replaced with U+FFFD
// by JSON encoding and terminal control sequences are not removed, so
// sanitize before painting. OldLine/NewLine are 1-based line numbers on the
// side(s) the line exists on (0 otherwise). NoNewline marks Git's
// "\ No newline at end of file" for this line.
type GitHunkLine struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	OldLine   int    `json:"old_line,omitempty"`
	NewLine   int    `json:"new_line,omitempty"`
	NoNewline bool   `json:"no_newline,omitempty"`
}

// GitHunkLine kinds. Only add and delete lines are selectable.
const (
	GitHunkLineContext = "context"
	GitHunkLineAdd     = "add"
	GitHunkLineDelete  = "delete"
)

// GitHunks.Unsupported codes.
const (
	GitPartialUnsupportedConflicted = "conflicted"    // unmerged path
	GitPartialUnsupportedSubmodule  = "submodule"     // gitlink entry
	GitPartialUnsupportedSymlink    = "symlink"       // either side is a symlink
	GitPartialUnsupportedBinary     = "binary"        // Git diffs it as binary, or -diff
	GitPartialUnsupportedFilter     = "filter"        // a filter attribute applies (LFS, custom clean/smudge)
	GitPartialUnsupportedEncoding   = "encoding"      // working-tree-encoding attribute
	GitPartialUnsupportedDeleted    = "deleted"       // the target side has no file (deletion)
	GitPartialUnsupportedTypeChange = "type_change"   // file type changed
	GitPartialUnsupportedRename     = "rename"        // worktree rename of an intent-to-add file
	GitPartialUnsupportedNotRegular = "not_regular"   // the worktree path is not a regular file
	GitPartialUnsupportedSkip       = "skip_worktree" // skip-worktree or assume-unchanged entry
	GitPartialUnsupportedNoContent  = "no_content"    // no content hunks (mode-only change)
	GitPartialUnsupportedTooLarge   = "too_large"     // diff or blob exceeds the server bound
	GitPartialUnsupportedUnparsable = "unparsable"    // the diff could not be parsed safely
)

// GitPartial is GitWrite.Partial: a hunk/line selection for git.stage (Group
// GitGroupUnstaged) or git.unstage (Group GitGroupStaged). Paths must be
// empty and Confirmed false. Hunks selects every add/delete line of those
// hunks; Lines selects individual add/delete lines by GitHunkLine.Index. The
// union must be non-empty, indices must exist in the GitHunks the
// Fingerprint came from, contain no duplicates, and context lines cannot be
// selected (invalid).
//
// Selection size: len(Hunks)+len(Lines) is at most 65 536 (refused with
// too_large otherwise; command bodies are capped at 768 KiB). Select whole
// hunks by hunk index rather than listing every line of a large change.
//
// Result. Stage: the index becomes the index content with exactly the
// selected changes applied. Unstage: the index becomes the HEAD content
// with every unselected change applied, i.e. the index with exactly the
// selected changes reverted (deleted HEAD lines restored at their HEAD
// position, added lines removed); the rest stays staged.
//
// Ordering inside one change block (adjacent delete and add lines, in the
// displayed diff): the selected add lines take the place of the first
// selected delete line; unselected delete lines before it stay in front of
// them and those after it follow them. In "-a -b +A +B", selecting "-a +A"
// gives "A b" and selecting "-b +B" gives "a B". A block with no selected
// delete line keeps its old lines and appends the selected add lines after
// them. For unstage the same rule applies to the unselected changes, which
// are the ones applied to HEAD (ADR 0025).
//
// A missing final newline is a property of whichever line ends up last. A
// line that only gained or lost its final newline appears as a delete/add
// pair with the same text ("-b" with NoNewline, "+b"); selecting only one
// side of such a pair keeps both copies or drops the line, faithfully to
// the selection (for example, unstaging only "-b" from the staged lines
// "-b" (NoNewline), "+b", "+c" leaves "b" twice). Select both sides of such
// a pair together. The entry's mode
// and, for a staged rename, the rename itself are unchanged; an
// intent-to-add entry becomes an ordinary entry holding the selected lines.
//
// Additional refusal codes (protocol.Error.Code, nothing recorded):
//
//	stale_diff     the fingerprint no longer matches (index, HEAD, worktree,
//	               attributes or diff changed); fetch GET /v1/git/hunks again
//	not_supported  the path does not support partial staging now (message
//	               says why; GitHunks.Unsupported gives the code); also
//	               git.discard with Partial (partial discard is deferred)
//	conflicted     the path is unmerged
//	too_large      more than 65 536 hunk and line indices; select hunks
//	unavailable    also: the installed Git is older than 2.28 (the server
//	               then does not advertise git-partial-stage)
//
// Final GitResult codes in addition to git_write.go's: stale_diff (failed:
// the index entry, HEAD entry or worktree changed after the checks and just
// before `update-index`; nothing was changed); internal_error (failed: the
// written object did not match the prediction; the index was not changed);
// and, on a succeeded result, index_changed (after `update-index` the entry
// is not the predicted one: another process changed the index at the same
// moment; review the staged diff).
type GitPartial struct {
	Path        string `json:"path"`
	Group       string `json:"group"`
	Fingerprint string `json:"fingerprint"`
	Hunks       []int  `json:"hunks,omitempty"`
	Lines       []int  `json:"lines,omitempty"`
}
