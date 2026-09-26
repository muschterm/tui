package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

// Partial staging (ADR 0025): GET /v1/git/hunks and the git.stage /
// git.unstage commands with GitWrite.Partial. Contract in
// protocol/git_partial.go.
//
// Mechanism. The server computes the new index blob itself: it reads the
// diff's pinned pre-image blob (index for stage, HEAD for unstage), applies
// the selected changes (stage) or every unselected change (unstage) to its
// bytes (git_partial_select.go),
// writes the result with `hash-object -w --no-filters --stdin` and installs it
// with `update-index --cacheinfo`, then checks that the index holds exactly
// the predicted entry. `update-index` has no compare-and-swap and silently
// resolves conflicts (research B1, A12), so every input is pinned by the
// fingerprint, recomputed under the writer lease before journaling, and the
// index entry (plus the HEAD entry or worktree token) is checked again
// immediately before `update-index`.
//
// Reads (the endpoint and the fingerprint) use the read policy of git.go, as
// status pins do; only hash-object -w and update-index run with the write
// policy (CLI parity: the post-index-change hook runs, as for `git add`).

const (
	// gitPartialDiffMax bounds the diff that can be selected from; larger
	// diffs are too_large (whole-file staging remains).
	gitPartialDiffMax = 4 << 20
	// gitPartialBlobMax bounds the base blob read into memory. Git diffs
	// blobs over core.bigFileThreshold (8 MiB for reads) as binary anyway.
	gitPartialBlobMax = 16 << 20
	// gitPartialMaxSelection bounds len(Hunks)+len(Lines). POST /v1/command
	// bodies are capped at 768 KiB; 65 536 indices of up to seven digits
	// fit with room for the rest of the command.
	gitPartialMaxSelection = 65536
	// gitPartialMinGit is the oldest Git whose diff accepts every pinned
	// option: --no-relative appeared in Git 2.28.0 (absent from v2.27.0's
	// Documentation/diff-options.txt, present in v2.28.0's); the other
	// options and plumbing used here are older.
	gitPartialMinMajor, gitPartialMinMinor = 2, 28
)

// gitPartialSupported reports whether the installed Git supports partial
// staging (capability git-partial-stage).
func gitPartialSupported() bool {
	v, err := gitVersion()
	return err == nil && (v[0] > gitPartialMinMajor || (v[0] == gitPartialMinMajor && v[1] >= gitPartialMinMinor))
}

var errPartialGitTooOld = failure("unavailable", fmt.Sprintf("partial staging needs Git %d.%d or newer", gitPartialMinMajor, gitPartialMinMinor))

// gitPartialDiffOptions pin every diff setting that changes hunk boundaries
// or patch text, so the user's diff.* configuration cannot move them.
var gitPartialDiffOptions = []string{
	"-c", "diff.suppressBlankEmpty=false", "-c", "diff.mnemonicPrefix=false", "-c", "diff.noprefix=false", "-c", "core.quotePath=true",
	"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--full-index",
	"--src-prefix=a/", "--dst-prefix=b/", "-U3", "--inter-hunk-context=0", "--diff-algorithm=myers", "--indent-heuristic",
}

// gitPartialBeforeIndexHook, when set by a test, runs after the result
// object is written and before the final index recheck and update-index.
var gitPartialBeforeIndexHook func(top string)

// partialState is everything a partial stage or unstage is pinned to.
type partialState struct {
	view    protocol.GitHunks
	hunks   []diffHunk // as displayed; the pre-image is base
	base    []byte     // index blob (stage) or HEAD blob (unstage)
	invert  bool       // unstage: apply the changes that were not selected
	mode    string     // index entry mode kept by the result
	index   []byte     // ls-files -s -v record of the path
	head    []byte     // ls-tree record of the HEAD side (unstage)
	token   string     // worktree lstat token (stage)
	headRef string     // path whose HEAD entry is pinned (unstage)
}

func (e *engine) gitHunks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e.gitHandle(w, r, func(ctx context.Context, dir string) (any, error) {
		p, group := q.Get("path"), q.Get("group")
		if err := validPartialTarget(p, group); err != nil {
			return nil, err
		}
		if !gitPartialSupported() {
			return nil, errPartialGitTooOld
		}
		if !gitReadable(inspectWorkspace(ctx, dir)) {
			return nil, errGitNotRepository
		}
		g, err := newGitReader(ctx, dir)
		if err != nil {
			return nil, err
		}
		st, err := readPartialState(ctx, g, p, group)
		if err != nil {
			return nil, err
		}
		return st.view, nil
	})
}

func validPartialTarget(p, group string) error {
	if group != protocol.GitGroupStaged && group != protocol.GitGroupUnstaged {
		return failure("invalid", "group must be staged or unstaged")
	}
	if !utf8.ValidString(p) || !validGitPath(p) {
		return failure("invalid", "path must be a relative checkout path outside .git")
	}
	return nil
}

// errPartialChanged reports inputs that changed between the reads that pin
// them; the endpoint answers unavailable (try again), a write stale_diff.
var errPartialChanged = failure("unavailable", "the file or index changed while it was read; try again")

// unsupported marks the state as not partially stageable.
func (s *partialState) unsupported(code, message string) *partialState {
	s.view.Unsupported, s.view.Message, s.view.Fingerprint = code, message, ""
	return s
}

// lsFilesRecord is one `ls-files -s -v` record: tag, mode, oid, stage.
type lsFilesRecord struct{ tag, mode, oid, stage string }

func parseLsFiles(out []byte) []lsFilesRecord {
	var recs []lsFilesRecord
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, _, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		switch len(f) {
		case 4:
			recs = append(recs, lsFilesRecord{tag: f[0], mode: f[1], oid: f[2], stage: f[3]})
		case 3:
			recs = append(recs, lsFilesRecord{mode: f[0], oid: f[1], stage: f[2]})
		}
	}
	return recs
}

// readPartialState reads and pins one path's partial-staging inputs. A path
// that cannot be partially staged is returned with view.Unsupported set;
// errors are for requests that cannot be answered at all.
func readPartialState(ctx context.Context, g *gitReader, p, group string) (*partialState, error) {
	s := &partialState{view: protocol.GitHunks{Path: p, Group: group, Hunks: []protocol.GitHunk{}}}
	scope := p
	if group == protocol.GitGroupStaged {
		scope = "" // staged renames pair up only in an unscoped status
	}
	st, err := revalidatedStatus(ctx, g, scope)
	if err != nil {
		return nil, err
	}
	if findEntry(st, p, protocol.GitGroupConflicted) != nil {
		return s.unsupported(protocol.GitPartialUnsupportedConflicted, "this path is conflicted; resolve the conflict first"), nil
	}
	entry := findEntry(st, p, group)
	if entry == nil {
		return nil, failure("not_found", "path has no "+group+" change in current status")
	}
	s.view.OrigPath = entry.OrigPath
	if entry.Pin == "" {
		return s.unsupported(protocol.GitPartialUnsupportedUnparsable, "this path name cannot be changed here"), nil
	}
	if entry.Submodule || entry.ModeHead == "160000" || entry.ModeIndex == "160000" || entry.ModeWorktree == "160000" {
		return s.unsupported(protocol.GitPartialUnsupportedSubmodule, "submodules can only be staged as a whole"), nil
	}

	stage := group == protocol.GitGroupUnstaged
	letter := entry.Index
	if stage {
		letter = entry.Worktree
	}
	switch {
	case stage && entry.IntentToAdd && letter == "R":
		return s.unsupported(protocol.GitPartialUnsupportedRename, "a renamed intent-to-add file can only be staged as a whole"), nil
	case letter == "D":
		return s.unsupported(protocol.GitPartialUnsupportedDeleted, "a deletion can only be staged or unstaged as a whole"), nil
	case letter == "T":
		return s.unsupported(protocol.GitPartialUnsupportedTypeChange, "a file type change can only be staged or unstaged as a whole"), nil
	case entry.ModeHead == "120000" || entry.ModeIndex == "120000" || entry.ModeWorktree == "120000":
		return s.unsupported(protocol.GitPartialUnsupportedSymlink, "symlinks can only be staged or unstaged as a whole"), nil
	}
	if stage {
		switch tokenKind(entry.WorktreeStat) {
		case "reg":
		case tokenUnavailable:
			return nil, failure("unavailable", "the file could not be examined safely")
		case tokenAbsent:
			return s.unsupported(protocol.GitPartialUnsupportedDeleted, "the file no longer exists"), nil
		default:
			return s.unsupported(protocol.GitPartialUnsupportedNotRegular, "only regular files can be staged partially"), nil
		}
	}

	index, recs, err := readIndexRecord(ctx, g, p)
	if err != nil {
		return nil, err
	}
	s.index = index
	if len(recs) != 1 || recs[0].stage != "0" {
		if len(recs) > 1 {
			return s.unsupported(protocol.GitPartialUnsupportedConflicted, "this path is conflicted; resolve the conflict first"), nil
		}
		return nil, errPartialChanged
	}
	rec := recs[0]
	if rec.tag != "H" {
		// S: skip-worktree; lowercase: assume-unchanged. update-index
		// would drop those bits.
		return s.unsupported(protocol.GitPartialUnsupportedSkip, "skip-worktree and assume-unchanged entries can only be changed from a terminal"), nil
	}
	s.mode, s.view.Mode = rec.mode, rec.mode
	if rec.mode != "100644" && rec.mode != "100755" {
		return s.unsupported(protocol.GitPartialUnsupportedNotRegular, "only regular files can be staged partially"), nil
	}
	if stage {
		s.view.ModeChanged = entry.ModeWorktree != "" && entry.ModeWorktree != rec.mode
	} else {
		s.view.ModeChanged = entry.ModeHead != "000000" && entry.ModeHead != "" && entry.ModeHead != rec.mode
	}

	attrs, truncated, err := g.read(ctx, 64<<10, "check-attr", "-z", "filter", "diff", "working-tree-encoding", "--", p)
	if err != nil || truncated {
		return nil, failure("unavailable", "the path's attributes could not be read")
	}
	fields := strings.Split(string(attrs), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		name, value := fields[i+1], fields[i+2]
		if value == "unspecified" {
			continue
		}
		switch {
		case name == "filter" && value != "unset":
			return s.unsupported(protocol.GitPartialUnsupportedFilter, "a filter attribute (for example Git LFS) applies to this path; stage it as a whole"), nil
		case name == "working-tree-encoding" && value != "unset":
			return s.unsupported(protocol.GitPartialUnsupportedEncoding, "a working-tree-encoding attribute applies to this path; stage it as a whole"), nil
		case name == "diff" && value == "unset":
			return s.unsupported(protocol.GitPartialUnsupportedBinary, "this path is marked binary; stage it as a whole"), nil
		}
	}

	var diff []byte
	var worktreeOid, pre, post string
	if stage {
		s.token = entry.WorktreeStat
		if worktreeStat(g.dir, p) != s.token {
			return nil, errPartialChanged
		}
		diff, truncated, err = g.read(ctx, gitPartialDiffMax, append(append([]string{}, gitPartialDiffOptions...), "--", p)...)
		if err != nil {
			return nil, gitError(err)
		}
		if worktreeStat(g.dir, p) != s.token {
			return nil, errPartialChanged
		}
		// The worktree's post-image object ID is the one Git's own diff
		// computed (the "index" header with --full-index). It applies the
		// index-aware conversion rules that `git add` uses, such as keeping
		// CRLF under core.autocrlf when the index blob already has CR;
		// `hash-object --path` does not consult the index (review d).
		worktreeOid = diffPostOid(diff)
		pre, post = rec.oid, worktreeOid
	} else {
		oldOid := entry.HeadOid
		if strings.Trim(oldOid, "0") == "" {
			empty, _, err := g.readInput(ctx, 256, strings.NewReader(""), "hash-object", "--stdin")
			if err != nil {
				return nil, failure("unavailable", "the empty object ID could not be computed")
			}
			oldOid = strings.TrimSpace(string(empty))
		}
		s.headRef = p
		if entry.OrigPath != "" {
			s.headRef = entry.OrigPath
		}
		if s.head, err = readHeadEntry(ctx, g, s.headRef); err != nil {
			return nil, err
		}
		diff, truncated, err = g.read(ctx, gitPartialDiffMax, append(append([]string{}, gitPartialDiffOptions...), oldOid, rec.oid)...)
		if err != nil {
			return nil, gitError(err)
		}
		pre, post, s.invert = oldOid, rec.oid, true
	}
	if truncated {
		return s.unsupported(protocol.GitPartialUnsupportedTooLarge, "this change is too large to select from here; stage it as a whole"), nil
	}
	hunks, err := parseHunks(diff)
	switch {
	case errors.Is(err, errDiffBinary):
		return s.unsupported(protocol.GitPartialUnsupportedBinary, "Git treats this file as binary; stage it as a whole"), nil
	case err != nil:
		return s.unsupported(protocol.GitPartialUnsupportedUnparsable, "this change could not be split safely; stage it as a whole"), nil
	}
	s.hunks = hunks
	s.view.Hunks, s.view.LineCount = hunksView(hunks)
	if len(hunks) == 0 {
		return s.unsupported(protocol.GitPartialUnsupportedNoContent, "only the file mode changed; stage it as a whole"), nil
	}

	// The base is the diff's pre-image: the index blob for stage (the
	// selection is applied), the HEAD blob for unstage (every change that
	// was not selected is applied, which reverts exactly the selection).
	s.base, truncated, err = g.read(ctx, gitPartialBlobMax, "cat-file", "blob", pre)
	if err != nil {
		return nil, failure("unavailable", "the file's content could not be read")
	}
	if truncated {
		return s.unsupported(protocol.GitPartialUnsupportedTooLarge, "this file is too large to select from here; stage it as a whole"), nil
	}
	// End-to-end check of the parse: the whole diff applied to the
	// pre-image must reproduce the post-image object exactly.
	all, err := applySelection(splitBlob(s.base), hunks, func(int) bool { return true })
	if err != nil {
		return nil, errPartialChanged
	}
	if oid, err := hashBlob(ctx, g, joinBlob(all)); err != nil {
		return nil, err
	} else if post == "" || oid != post {
		return s.unsupported(protocol.GitPartialUnsupportedUnparsable, "this change could not be split safely; stage it as a whole"), nil
	}

	// Length-prefixed fields: the index record and attributes contain NULs.
	h := sha256.New()
	diffSum := sha256.Sum256(diff)
	for _, f := range []string{"partial-v2", group, p, entry.OrigPath, entry.Pin, string(index), string(s.head), string(attrs), s.token, worktreeOid, hex.EncodeToString(diffSum[:])} {
		fmt.Fprintf(h, "%d:", len(f))
		h.Write([]byte(f))
	}
	s.view.Fingerprint = "p2-" + hex.EncodeToString(h.Sum(nil))
	return s, nil
}

// diffPostOid is the post-image object ID from a --full-index diff's
// "index <pre>..<post>" header, or "" when there is none.
func diffPostOid(diff []byte) string {
	for _, line := range strings.Split(string(diff), "\n") {
		if strings.HasPrefix(line, "@@ ") {
			break
		}
		if rest, ok := strings.CutPrefix(line, "index "); ok {
			rng, _, _ := strings.Cut(rest, " ")
			if _, post, ok := strings.Cut(rng, ".."); ok && gitFullHash.MatchString(post) {
				return post
			}
		}
	}
	return ""
}

// readIndexRecord is the path's `ls-files --stage -v` record plus its
// intent-to-add flag, which that record does not show: an intent-to-add
// entry and an ordinary empty file have the same mode, object ID and tag.
// The flag comes from --debug's "flags:" word (CE_INTENT_TO_ADD, 1<<29);
// the stat data there is ignored because refreshes change it.
func readIndexRecord(ctx context.Context, g *gitReader, p string) ([]byte, []lsFilesRecord, error) {
	index, truncated, err := g.read(ctx, 64<<10, "ls-files", "--stage", "-v", "-z", "--", p)
	if err != nil || truncated {
		return nil, nil, failure("unavailable", "the index entry could not be read")
	}
	debug, truncated, err := g.read(ctx, 64<<10, "ls-files", "--debug", "-z", "--", p)
	if err != nil || truncated {
		return nil, nil, failure("unavailable", "the index entry could not be read")
	}
	var flags []string
	for _, line := range strings.Split(string(debug), "\n") {
		if _, word, ok := strings.Cut(line, "flags: "); ok {
			v, err := strconv.ParseUint(strings.TrimSpace(word), 16, 32)
			if err != nil {
				return nil, nil, failure("unavailable", "the index entry could not be read")
			}
			flags = append(flags, strconv.FormatBool(v&(1<<29) != 0))
		}
	}
	record := append(append([]byte{}, index...), []byte("\x00ita="+strings.Join(flags, ","))...)
	return record, parseLsFiles(index), nil
}

// hashBlob is the object ID of data stored as a blob without filters.
func hashBlob(ctx context.Context, g *gitReader, data []byte) (string, error) {
	out, _, err := g.readInput(ctx, 256, bytes.NewReader(data), "hash-object", "--no-filters", "--stdin")
	if err != nil {
		return "", failure("unavailable", "content could not be hashed")
	}
	return strings.TrimSpace(string(out)), nil
}

// readHeadEntry is HEAD's tree record for p, empty when HEAD is unborn or
// has no such path.
func readHeadEntry(ctx context.Context, g *gitReader, p string) ([]byte, error) {
	if !headExists(ctx, g) {
		return nil, nil
	}
	out, truncated, err := g.read(ctx, 64<<10, "ls-tree", "-z", "--full-tree", "HEAD", "--", p)
	if err != nil || truncated {
		return nil, failure("unavailable", "the HEAD entry could not be read")
	}
	return out, nil
}

// preparePartial revalidates a partial stage or unstage and computes its
// result blob; the plan installs it.
func preparePartial(ctx context.Context, g *gitReader, w *gitWriter, sel protocol.GitPartial) (*gitPlan, error) {
	if !gitPartialSupported() {
		return nil, errPartialGitTooOld
	}
	st, err := readPartialState(ctx, g, sel.Path, sel.Group)
	if err != nil {
		var pe *protocol.Error
		if errors.Is(err, errPartialChanged) || (errors.As(err, &pe) && pe.Code == "not_found") {
			return nil, failure("stale_diff", "this path no longer has these changes; refresh and select again")
		}
		return nil, err
	}
	switch {
	case st.view.Unsupported == protocol.GitPartialUnsupportedConflicted:
		return nil, failure("conflicted", st.view.Message)
	case st.view.Unsupported != "":
		return nil, failure("not_supported", st.view.Message)
	case st.view.Fingerprint != sel.Fingerprint:
		return nil, failure("stale_diff", "this change is no longer what was shown; refresh and select again")
	}
	if err := w.checkLocks(false); err != nil {
		return nil, err
	}
	set, err := selectionSet(st.hunks, sel)
	if err != nil {
		return nil, err
	}
	lines, err := applySelection(splitBlob(st.base), st.hunks, func(i int) bool { return set[i] != st.invert })
	if err != nil {
		return nil, failure("stale_diff", "this change is no longer what was shown; refresh and select again")
	}
	result := joinBlob(lines)
	predicted, err := hashBlob(ctx, g, result)
	if err != nil {
		return nil, err
	}
	verb, done := "stage", "Staged"
	if sel.Group == protocol.GitGroupStaged {
		verb, done = "unstage", "Unstaged"
	}
	summary := fmt.Sprintf("%s %d selected line(s) of %s", done, len(set), sel.Path)
	return &gitPlan{paths: []string{sel.Path}, run: func(ctx context.Context) protocol.GitResult {
		got, output, err := w.run(ctx, bytes.NewReader(result), false, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return failedGit("git_failed", "Git could not store the selected content; nothing was "+verb+"d", output)
		}
		if oid := strings.TrimSpace(string(got)); oid != predicted {
			return gitResult(protocol.GitStateFailed, "internal_error", "the stored content differs from the prediction; nothing was "+verb+"d", output)
		}
		if gitPartialBeforeIndexHook != nil {
			gitPartialBeforeIndexHook(w.top)
		}
		// Last look: update-index has no compare-and-swap.
		if r := recheckPartial(ctx, g, st); r != nil {
			return *r
		}
		_, output, err = w.run(ctx, nil, false, "update-index", "--cacheinfo", st.mode+","+predicted+","+sel.Path)
		if err != nil {
			return failedGit("git_failed", "Git could not "+verb+" the selected lines", output)
		}
		after, _, err := g.read(ctx, 64<<10, "ls-files", "--stage", "-z", "--", sel.Path)
		if err != nil {
			return gitResult(protocol.GitStateSucceeded, "", summary+"; the result could not be verified", output)
		}
		if recs := parseLsFiles(after); len(recs) != 1 || recs[0].mode != st.mode || recs[0].oid != predicted || recs[0].stage != "0" {
			return gitResult(protocol.GitStateSucceeded, "index_changed", summary+", but the index entry changed at the same moment; review the staged changes", output)
		}
		return gitResult(protocol.GitStateSucceeded, "", summary, output)
	}}, nil
}

// recheckPartial compares the pinned index entry, and the HEAD entry
// (unstage) or worktree token (stage), immediately before update-index.
func recheckPartial(ctx context.Context, g *gitReader, st *partialState) *protocol.GitResult {
	stale := func() *protocol.GitResult {
		r := gitResult(protocol.GitStateFailed, "stale_diff", "the file changed after review; nothing was changed", nil)
		return &r
	}
	index, _, err := readIndexRecord(ctx, g, st.view.Path)
	if err != nil || !bytes.Equal(index, st.index) {
		return stale()
	}
	if st.token != "" && worktreeStat(g.dir, st.view.Path) != st.token {
		return stale()
	}
	if st.headRef != "" {
		head, err := readHeadEntry(ctx, g, st.headRef)
		if err != nil || !bytes.Equal(head, st.head) {
			return stale()
		}
	}
	return nil
}

// validatePartial checks a GitWrite.Partial payload's shape.
func validatePartial(kind string, w *protocol.GitWrite) error {
	sel := w.Partial
	switch kind {
	case protocol.GitKindStage, protocol.GitKindUnstage:
	case protocol.GitKindDiscard:
		return failure("not_supported", "discarding selected lines is not supported; discard the whole file")
	default:
		return failure("invalid", "partial selections apply only to git.stage and git.unstage")
	}
	if len(w.Paths) != 0 || w.Confirmed || w.Message != "" || w.Amend || w.ExpectedHead != "" || w.StagedFingerprint != "" || w.AcknowledgePublished {
		return failure("invalid", "a partial selection carries only its partial payload")
	}
	if err := validPartialTarget(sel.Path, sel.Group); err != nil {
		return err
	}
	if kind == protocol.GitKindStage && sel.Group != protocol.GitGroupUnstaged {
		return failure("invalid", "only unstaged changes can be staged")
	}
	if kind == protocol.GitKindUnstage && sel.Group != protocol.GitGroupStaged {
		return failure("invalid", "only staged changes can be unstaged")
	}
	if sel.Fingerprint == "" {
		return failure("invalid", "fingerprint from GET /v1/git/hunks is required")
	}
	if len(sel.Hunks)+len(sel.Lines) == 0 {
		return failure("invalid", "select at least one hunk or line")
	}
	if len(sel.Hunks)+len(sel.Lines) > gitPartialMaxSelection {
		return failure("too_large", fmt.Sprintf("a selection names at most %d hunks and lines; select whole hunks instead of their lines", gitPartialMaxSelection))
	}
	return nil
}
